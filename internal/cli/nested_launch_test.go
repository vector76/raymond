package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/cli"
	"github.com/vector76/raymond/internal/daemon"
	"github.com/vector76/raymond/internal/orchestrator"
)

// noopOrchestrator implements daemon.Orchestrator for the nested-launch
// regression test. It blocks until ctx is cancelled so the outer "run"
// stays in the running (non-terminal) state for the duration of the test.
type noopOrchestrator struct{}

func (noopOrchestrator) RunAllAgents(ctx context.Context, _ string, _ orchestrator.RunOptions) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestNestedRayLaunchRoutesToCLIPool pins Phase 6 of the disjoint run-pool
// plan: a workflow that shells out to `ray <workflow>` from inside a
// serve-pool run lands the nested state file in the CLI pool
// (.raymond/state/), NOT in the serve pool. The ray binary's default
// state-dir resolution is PoolCLI; there is no env var, file marker, or
// process-inheritance mechanism that overrides that based on whether the
// parent process is itself a ray run. This test confirms that absence of
// special-casing by exercising both sides of the boundary.
//
// Assertions (mirroring the bead's checklist):
//
//	(a) The outer run's state file lives in serve-state/.
//	(b) The inner run's state file lives in .raymond/state/ (the CLI pool).
//	(c) The inner run does NOT appear in the daemon's RunManager.ListRuns().
//	(d) After `ray serve --clean` (here: ArchiveNonTerminalServeState, the
//	    primitive the --clean flag dispatches to), the outer non-terminal
//	    state file is moved under serve-state/abandoned/<ts>/ while the
//	    inner CLI-pool state file is left strictly untouched.
//
// No production change is exercised — this is a guard against any future
// "ray invoked from ray" detection logic being added accidentally.
func TestNestedRayLaunchRoutesToCLIPool(t *testing.T) {
	// Chdir into an empty temp dir so the inner CLI's config.LoadConfig
	// finds no .raymond/config.toml and cannot be perturbed by the
	// project's own config or whatever a developer happens to have in
	// their working tree. The outer daemon is constructed with an
	// explicit serveStateDir so its routing is independent of cwd
	// regardless.
	t.Chdir(t.TempDir())

	// Two pool dirs under a shared parent, matching the real
	// .raymond/{state,serve-state} layout.
	root := filepath.Join(t.TempDir(), ".raymond")
	serveStateDir := filepath.Join(root, "serve-state")
	cliStateDir := filepath.Join(root, "state")
	require.NoError(t, os.MkdirAll(serveStateDir, 0o755))
	require.NoError(t, os.MkdirAll(cliStateDir, 0o755))

	// ---- Outer run: launched into the serve pool via the daemon's
	// RunManager with a no-op orchestrator that blocks on ctx. This is the
	// same mechanism `ray serve --launch` uses, just without the HTTP
	// layer above it.
	outerScope := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(outerScope, "START.md"),
		[]byte("# Outer\nDo it."),
		0o644,
	))
	rm, err := daemon.NewRunManagerWithOrchestrator(serveStateDir, outerScope, noopOrchestrator{})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outerID, err := rm.LaunchRun(
		ctx,
		daemon.WorkflowEntry{
			ID:       "outer-workflow",
			ScopeDir: outerScope,
		},
		"",    // input
		0,     // budget
		"",    // model
		false, // dangerouslySkipPermissions
		"",    // workDir
		nil,   // env
	)
	require.NoError(t, err)
	require.NotEmpty(t, outerID)

	// (a) Outer state file is in the serve pool.
	outerStatePath := filepath.Join(serveStateDir, outerID+".json")
	require.FileExists(t, outerStatePath,
		"outer run's state file must be written into the serve pool")
	// And the same id is NOT present in the CLI pool — pool isolation
	// holds in both directions, not just one.
	require.NoFileExists(t, filepath.Join(cliStateDir, outerID+".json"),
		"outer run id must not leak into the CLI pool")

	// ---- Inner run: simulate a child `ray <workflow>` invocation that
	// the outer's script step might have executed. We drive the same
	// CLI entry point used by the ray binary; NewTestCLI's no-op runner
	// stands in for the real orchestrator. The CLI default state-dir
	// resolution is PoolCLI (state.GetStateDir → ResolvePoolDir(PoolCLI,
	// override)); `--state-dir cliStateDir` makes that resolution
	// hermetic against any ambient .raymond/ in the working tree.
	//
	// Inherit the RAYMOND_* env vars that platform.BuildScriptEnv sets
	// on every script step — these are exactly what a real child `ray`
	// binary spawned by a script transition would see. Setting them here
	// pins the rule that the CLI's pool resolution ignores them: any
	// future code that branched on RAYMOND_WORKFLOW_ID / RAYMOND_AGENT_ID
	// to route a nested run into the serve pool would flip assertion (b)
	// or (c) and be caught.
	t.Setenv("RAYMOND_WORKFLOW_ID", outerID)
	t.Setenv("RAYMOND_AGENT_ID", "main")

	innerScope := t.TempDir()
	innerEntry := filepath.Join(innerScope, "START.md")
	require.NoError(t, os.WriteFile(innerEntry, []byte("# Inner\nDo it."), 0o644))

	var out, errOut bytes.Buffer
	innerCLI := cli.NewTestCLI(&out, &errOut)
	innerCmd := innerCLI.NewRootCmd()
	innerCmd.SetArgs([]string{
		innerEntry,
		"--state-dir", cliStateDir,
		"--workflow-id", "inner-run",
	})
	require.NoError(t, innerCmd.Execute(),
		"inner ray invocation should succeed; stderr=%s", errOut.String())

	// (b) Inner state file is in the CLI pool.
	innerStatePath := filepath.Join(cliStateDir, "inner-run.json")
	require.FileExists(t, innerStatePath,
		"inner run's state file must be written into the CLI pool")

	// And the inner id must NOT have leaked into the serve pool.
	require.NoFileExists(t, filepath.Join(serveStateDir, "inner-run.json"),
		"inner run must NOT appear in the serve pool")

	// (c) Daemon's run-tracking surface — what `GET /runs` returns
	// verbatim, see http.go's handleListRuns — sees only the outer run.
	// The inner run is invisible to the daemon because the inner
	// invocation never touched any daemon-managed structure: a child
	// ray process has no handle on the parent's RunManager. LaunchRun
	// inserts into rm.runs synchronously before returning, so no
	// settling poll is required.
	runs := rm.ListRuns()
	require.Len(t, runs, 1, "daemon should track exactly one run (the outer)")
	assert.Equal(t, outerID, runs[0].RunID,
		"the one tracked run must be the outer, not the inner")

	// (d) --clean sub-test. ArchiveNonTerminalServeState is exactly what
	// `ray serve --clean` runs at startup (see cli/serve.go). Capture the
	// inner CLI-pool file's bytes BEFORE running --clean so we can byte-
	// compare after to prove --clean did not touch it.
	innerBytesBefore, err := os.ReadFile(innerStatePath)
	require.NoError(t, err)

	abandonDir, archived, err := daemon.ArchiveNonTerminalServeState(serveStateDir, time.Now)
	require.NoError(t, err)
	require.Equal(t, []string{outerID}, archived,
		"only the outer non-terminal serve-state file should be archived")

	// The outer state file has moved out of serve-state/'s top level into
	// abandoned/<ts>/.
	require.NoFileExists(t, outerStatePath,
		"outer state file must be moved out of serve-state/ top level")
	require.FileExists(t, filepath.Join(abandonDir, outerID+".json"),
		"outer state file must live under serve-state/abandoned/<ts>/")

	// The inner CLI-pool state file is byte-identical: --clean's scope is
	// the serve pool only; the CLI pool is intentionally untouched.
	innerBytesAfter, err := os.ReadFile(innerStatePath)
	require.NoError(t, err,
		"inner CLI-pool state file must remain present after --clean")
	require.Equal(t, innerBytesBefore, innerBytesAfter,
		"inner CLI-pool state file must be byte-identical after --clean")
}
