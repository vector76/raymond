//go:build integration

// Package integration_test contains end-to-end integration tests for raymond.
//
// Tests are gated behind the "integration" build tag so they are excluded from
// the normal `go test ./...` run and only execute when invoked explicitly:
//
//	go test -tags integration ./tests/integration/...
//
// Tests that require the claude CLI check its availability via claudeAvailable()
// and call skipIfNoClaude(t) to skip gracefully when claude is not installed.
//
// Working directory during test execution is tests/integration/, so paths to
// workflow fixtures use the relative prefix "../../".
package integration_test

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/orchestrator"
	wfstate "github.com/vector76/raymond/internal/state"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// testCasesDir returns the path to workflows/test_cases/ relative to the
// package directory (tests/integration/).
func testCasesDir() string {
	return filepath.Join("..", "..", "workflows", "test_cases")
}

// claudeAvailable reports whether the claude CLI is in PATH.
func claudeAvailable() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// skipIfNoClaude skips t if claude is not in PATH.
func skipIfNoClaude(t *testing.T) {
	t.Helper()
	if !claudeAvailable() {
		t.Skip("claude CLI not found in PATH; skipping integration test that requires LLM")
	}
}

// runWorkflow creates a fresh workflow state, runs the orchestrator to
// completion (or error), and returns:
//   - completedClean: true if the state file was deleted (workflow finished all agents)
//   - runErr: the error returned by RunAllAgents (nil on clean completion or pause)
//
// mutOpts can be used to override RunOptions fields before the run.
func runWorkflow(
	t *testing.T,
	scopeDir, initialState string,
	budgetUSD float64,
	mutOpts ...func(*orchestrator.RunOptions),
) (completedClean bool, runErr error) {
	t.Helper()

	stateDir := t.TempDir()
	id := "integration-wf"
	ws := wfstate.CreateInitialState(id, scopeDir, initialState, budgetUSD, nil)
	require.NoError(t, wfstate.WriteState(id, ws, stateDir))

	opts := orchestrator.RunOptions{
		StateDir: stateDir,
		Quiet:    true,
		Timeout:  120.0,
		NoWait:   true, // never block waiting for usage-limit resets in tests
	}
	for _, m := range mutOpts {
		m(&opts)
	}

	runErr = orchestrator.RunAllAgents(context.Background(), id, opts)

	// State file absent ↔ clean completion (RunAllAgents deleted it).
	_, statErr := wfstate.ReadState(id, stateDir)
	completedClean = statErr != nil
	return completedClean, runErr
}

// --------------------------------------------------------------------------
// Script-only integration tests (no Claude required)
// --------------------------------------------------------------------------

// TestScriptResultWorkflow verifies that a purely-script workflow that emits
// a <result> tag on its first execution terminates cleanly.
func TestScriptResultWorkflow(t *testing.T) {
	scriptName := "SCRIPT_RESULT.sh"
	if runtime.GOOS == "windows" {
		scriptName = "SCRIPT_RESULT.bat"
	}

	completed, err := runWorkflow(t, testCasesDir(), scriptName, 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "SCRIPT_RESULT workflow should complete cleanly")
}

// TestScriptResetWorkflow verifies that a script that uses <reset> to re-run
// itself multiple times eventually emits <result> and terminates.
// SCRIPT_RESET.sh uses /tmp/reset_counter.txt; we clean it before and after.
func TestScriptResetWorkflow(t *testing.T) {
	scriptName := "SCRIPT_RESET.sh"
	if runtime.GOOS == "windows" {
		scriptName = "SCRIPT_RESET.bat"
	}

	if runtime.GOOS != "windows" {
		_ = os.Remove("/tmp/reset_counter.txt")
		t.Cleanup(func() { _ = os.Remove("/tmp/reset_counter.txt") })
	}

	completed, err := runWorkflow(t, testCasesDir(), scriptName, 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "SCRIPT_RESET workflow should complete after 3 iterations")
}

// TestCrashRecovery simulates a process crash by running the orchestrator with
// a pre-cancelled context (so it exits immediately without running any steps),
// then resumes from the preserved state file and verifies clean completion.
func TestCrashRecovery(t *testing.T) {
	scriptName := "SCRIPT_RESET.sh"
	if runtime.GOOS == "windows" {
		scriptName = "SCRIPT_RESET.bat"
	}

	if runtime.GOOS != "windows" {
		_ = os.Remove("/tmp/reset_counter.txt")
		t.Cleanup(func() { _ = os.Remove("/tmp/reset_counter.txt") })
	}

	stateDir := t.TempDir()
	tc := testCasesDir()
	id := "integration-crash-wf"

	ws := wfstate.CreateInitialState(id, tc, scriptName, 10.0, nil)
	require.NoError(t, wfstate.WriteState(id, ws, stateDir))

	opts := orchestrator.RunOptions{
		StateDir: stateDir,
		Quiet:    true,
		Timeout:  120.0,
		NoWait:   true,
	}

	// "Crash": run with a pre-cancelled context so no steps execute.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	crashErr := orchestrator.RunAllAgents(cancelledCtx, id, opts)
	require.ErrorIs(t, crashErr, context.Canceled, "pre-cancelled context should surface as Canceled")

	// State file must have survived the crash.
	_, readErr := wfstate.ReadState(id, stateDir)
	require.NoError(t, readErr, "state file must persist after a crash so recovery is possible")

	// Resume: run with a fresh context — should reach clean completion.
	resumeErr := orchestrator.RunAllAgents(context.Background(), id, opts)
	require.NoError(t, resumeErr)

	// State file should now be gone (workflow completed).
	_, statErr := wfstate.ReadState(id, stateDir)
	assert.Error(t, statErr, "state file should be deleted after successful resume completion")
}

// TestZIPScopeScriptWorkflow verifies that a purely-script workflow runs
// correctly when the workflow scope is a ZIP archive instead of a directory.
func TestZIPScopeScriptWorkflow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ZIP scope script test uses .sh files; skipping on Windows")
	}

	tc := testCasesDir()
	zipPath := filepath.Join(t.TempDir(), "test_cases.zip")
	require.NoError(t, buildZipFromDir(tc, zipPath))

	completed, err := runWorkflow(t, zipPath, "SCRIPT_RESULT.sh", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "ZIP-scope SCRIPT_RESULT workflow should complete cleanly")
}

// --------------------------------------------------------------------------
// Build verification (no Claude required)
// --------------------------------------------------------------------------

// TestBothBinariesBuild verifies that both the raymond and ray binaries
// compile successfully from their respective cmd packages.
func TestBothBinariesBuild(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	tmpDir := t.TempDir()

	for _, pkg := range []struct{ importPath, name string }{
		{"./cmd/raymond", "raymond"},
		{"./cmd/ray", "ray"},
	} {
		binName := pkg.name
		if runtime.GOOS == "windows" {
			binName += ".exe"
		}
		binPath := filepath.Join(tmpDir, binName)

		cmd := exec.Command("go", "build", "-o", binPath, pkg.importPath)
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "go build %s failed:\n%s", pkg.importPath, string(out))

		info, statErr := os.Stat(binPath)
		require.NoError(t, statErr, "%s binary should exist after build", pkg.name)
		assert.Greater(t, info.Size(), int64(0), "%s binary should be non-empty", pkg.name)
	}
}

// --------------------------------------------------------------------------
// LLM integration tests (require claude CLI)
// --------------------------------------------------------------------------

// TestScriptGotoMarkdownWorkflow verifies the script→markdown transition path:
// SCRIPT_GOTO.sh emits <goto>SCRIPT_TARGET.md</goto>; the markdown state
// asks Claude to respond with a final <result>.
func TestScriptGotoMarkdownWorkflow(t *testing.T) {
	skipIfNoClaude(t)

	scriptName := "SCRIPT_GOTO.sh"
	if runtime.GOOS == "windows" {
		scriptName = "SCRIPT_GOTO.bat"
	}

	completed, err := runWorkflow(t, testCasesDir(), scriptName, 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "script→markdown workflow should complete")
}

// TestHybridWorkflow verifies a three-state hybrid workflow that alternates
// between script and markdown states: HYBRID_START → HYBRID_MIDDLE → HYBRID_END.
func TestHybridWorkflow(t *testing.T) {
	skipIfNoClaude(t)

	scriptName := "HYBRID_START.sh"
	if runtime.GOOS == "windows" {
		scriptName = "HYBRID_START.bat"
	}

	completed, err := runWorkflow(t, testCasesDir(), scriptName, 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "hybrid script→markdown→script workflow should complete")
}

// TestPollWorkflow verifies the polling pattern: a script loops (resetting)
// until a condition is met, then transitions to a markdown state for final processing.
func TestPollWorkflow(t *testing.T) {
	skipIfNoClaude(t)

	if runtime.GOOS == "windows" {
		t.Skip("poll workflow uses .sh scripts; skipping on Windows")
	}

	_ = os.Remove("/tmp/poll_counter.txt")
	t.Cleanup(func() { _ = os.Remove("/tmp/poll_counter.txt") })

	completed, err := runWorkflow(t, testCasesDir(), "POLL_EXAMPLE.sh", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "poll→process workflow should complete after condition is met")
}

// TestStoryWorkflow verifies a pure-LLM three-step story workflow using goto
// transitions: START → CONFLICT → RESOLUTION → result.
func TestStoryWorkflow(t *testing.T) {
	skipIfNoClaude(t)

	tc := testCasesDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tc, "test_outputs"), 0o755))

	completed, err := runWorkflow(t, tc, "START.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "3-step story workflow (START→CONFLICT→RESOLUTION) should complete")
}

// TestCallReturnWorkflow verifies the call/return pattern: MAIN calls RESEARCH
// (which returns a result payload), then SUMMARIZE receives the payload via
// {{result}} substitution and terminates.
func TestCallReturnWorkflow(t *testing.T) {
	skipIfNoClaude(t)

	tc := testCasesDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tc, "test_outputs"), 0o755))

	completed, err := runWorkflow(t, tc, "MAIN.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "call/return workflow (MAIN→RESEARCH→SUMMARIZE) should complete")
}

// TestResetContextWorkflow verifies that a <reset> transition clears the
// agent's LLM session context: PHASE1 stores a secret only in context; PHASE2
// (fresh context) should not know the secret.
func TestResetContextWorkflow(t *testing.T) {
	skipIfNoClaude(t)

	tc := testCasesDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tc, "test_outputs"), 0o755))

	completed, err := runWorkflow(t, tc, "PHASE1.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "reset-context workflow (PHASE1→PHASE2) should complete")
}

// TestZIPScopeLLMWorkflow verifies that an LLM workflow runs correctly when
// the workflow scope is a ZIP archive.
func TestZIPScopeLLMWorkflow(t *testing.T) {
	skipIfNoClaude(t)

	tc := testCasesDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tc, "test_outputs"), 0o755))

	zipPath := filepath.Join(t.TempDir(), "test_cases.zip")
	require.NoError(t, buildZipFromDir(tc, zipPath))

	completed, err := runWorkflow(t, zipPath, "START.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "ZIP-scope story workflow should complete")
}

// --------------------------------------------------------------------------
// ZIP helper
// --------------------------------------------------------------------------

// buildZipFromDir creates a ZIP archive at dst containing all non-directory
// files found by recursively walking src. Entry paths within the archive use
// forward slashes and are relative to src.
func buildZipFromDir(src, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		w, err := zw.Create(rel)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
}
