package cli_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/cli"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/orchestrator"
	wfstate "github.com/vector76/raymond/internal/state"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// newCmd builds a root command backed by a test CLI writing to out/errOut.
func newCmd(out, errOut *bytes.Buffer) *cli.CLI {
	return cli.NewTestCLI(out, errOut)
}

// run executes the CLI with the given args and returns (stdout, stderr, error).
func run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	c := newCmd(&out, &errOut)
	cmd := c.NewRootCmd()
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// makeStateDir creates a temp .raymond/state directory and returns its path.
func makeStateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".raymond", "state")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

// writeTestZip creates a valid zip file at path with the given files.
func writeTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
}

// writeWorkflow writes a workflow state file to stateDir and returns the ID.
func writeWorkflow(t *testing.T, id, scopeDir, initialState, stateDir string) {
	t.Helper()
	ws := wfstate.CreateInitialState(id, scopeDir, initialState, 10.0, nil, "")
	require.NoError(t, wfstate.WriteState(id, ws, stateDir))
}

// --------------------------------------------------------------------------
// --list
// --------------------------------------------------------------------------

func TestListEmpty(t *testing.T) {
	stateDir := makeStateDir(t)
	out, _, err := run(t, "--list", "--state-dir", stateDir)
	require.NoError(t, err)
	assert.Contains(t, out, "No workflows found")
}

func TestListOneWorkflow(t *testing.T) {
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-abc", "workflows/test", "START.md", stateDir)

	out, _, err := run(t, "--list", "--state-dir", stateDir)
	require.NoError(t, err)
	assert.Contains(t, out, "wf-abc")
}

func TestListMultipleWorkflows(t *testing.T) {
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-001", "workflows/a", "START.md", stateDir)
	writeWorkflow(t, "wf-002", "workflows/b", "START.md", stateDir)

	out, _, err := run(t, "--list", "--state-dir", stateDir)
	require.NoError(t, err)
	assert.Contains(t, out, "wf-001")
	assert.Contains(t, out, "wf-002")
}

// --------------------------------------------------------------------------
// --recover
// --------------------------------------------------------------------------

func TestRecoverEmpty(t *testing.T) {
	stateDir := makeStateDir(t)
	out, _, err := run(t, "--recover", "--state-dir", stateDir)
	require.NoError(t, err)
	assert.Contains(t, out, "No recoverable")
}

func TestRecoverWithActiveWorkflow(t *testing.T) {
	stateDir := makeStateDir(t)
	// CreateInitialState produces a workflow with one active agent.
	writeWorkflow(t, "wf-active", "workflows/test", "START.md", stateDir)

	out, _, err := run(t, "--recover", "--state-dir", stateDir)
	require.NoError(t, err)
	assert.Contains(t, out, "wf-active")
}

func TestRecoverSkipsCompletedWorkflow(t *testing.T) {
	stateDir := makeStateDir(t)
	// A workflow with no agents (already completed / all removed).
	ws := &wfstate.WorkflowState{
		WorkflowID: "wf-done",
		ScopeDir:   "workflows/test",
		Agents:     nil,
	}
	require.NoError(t, wfstate.WriteState("wf-done", ws, stateDir))

	out, _, err := run(t, "--recover", "--state-dir", stateDir)
	require.NoError(t, err)
	assert.NotContains(t, out, "wf-done")
}

// --------------------------------------------------------------------------
// --status
// --------------------------------------------------------------------------

func TestStatusNotFound(t *testing.T) {
	stateDir := makeStateDir(t)
	// Phase 7: cmdStatus falls back to the serve pool on CLI-pool miss.
	// Point the serve pool at an isolated temp dir so a populated working
	// tree (real .raymond/serve-state/ next to the project) can't flake
	// this test by happening to contain a file named nonexistent.json.
	serveDir := makeServeStateDir(t)
	_, _, err := run(t, "--status", "nonexistent",
		"--state-dir", stateDir, "--serve-state-dir", serveDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStatusFound(t *testing.T) {
	stateDir := makeStateDir(t)
	serveDir := makeServeStateDir(t)
	writeWorkflow(t, "wf-xyz", "workflows/test", "START.md", stateDir)

	out, _, err := run(t, "--status", "wf-xyz",
		"--state-dir", stateDir, "--serve-state-dir", serveDir)
	require.NoError(t, err)
	assert.Contains(t, out, "wf-xyz")
	assert.Contains(t, out, "workflows/test")
	assert.Contains(t, out, "main")
	assert.Contains(t, out, "START.md")
}

func TestStatusShowsBudget(t *testing.T) {
	stateDir := makeStateDir(t)
	serveDir := makeServeStateDir(t)
	ws := wfstate.CreateInitialState("wf-budget", "workflows/test", "START.md", 25.0, nil, "")
	require.NoError(t, wfstate.WriteState("wf-budget", ws, stateDir))

	out, _, err := run(t, "--status", "wf-budget",
		"--state-dir", stateDir, "--serve-state-dir", serveDir)
	require.NoError(t, err)
	assert.Contains(t, out, "25.00")
}

func TestStatusPausedAgent(t *testing.T) {
	stateDir := makeStateDir(t)
	serveDir := makeServeStateDir(t)
	ws := wfstate.CreateInitialState("wf-paused", "workflows/test", "START.md", 10.0, nil, "")
	ws.Agents[0].Status = "paused"
	ws.Agents[0].Error = "usage limit hit"
	require.NoError(t, wfstate.WriteState("wf-paused", ws, stateDir))

	out, _, err := run(t, "--status", "wf-paused",
		"--state-dir", stateDir, "--serve-state-dir", serveDir)
	require.NoError(t, err)
	assert.Contains(t, out, "paused")
	assert.Contains(t, out, "usage limit hit")
}

// --------------------------------------------------------------------------
// Phase 7: serve-pool listing + pool-agnostic `ray status`
// --------------------------------------------------------------------------

// makeServeStateDir creates a temp .raymond/serve-state directory and returns
// its path. Parallel to makeStateDir for the CLI pool.
func makeServeStateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".raymond", "serve-state")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

// makeBothPoolDirs creates a temp .raymond/state and .raymond/serve-state
// directory under a *shared* parent so a single test can seed both pools.
func makeBothPoolDirs(t *testing.T) (cliDir, serveDir string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".raymond")
	cliDir = filepath.Join(root, "state")
	serveDir = filepath.Join(root, "serve-state")
	require.NoError(t, os.MkdirAll(cliDir, 0o755))
	require.NoError(t, os.MkdirAll(serveDir, 0o755))
	return cliDir, serveDir
}

// writeServePoolWorkflow writes a state file directly into the given serve-pool
// directory using PoolServe semantics, so the test exercises the same
// listing path production uses.
func writeServePoolWorkflow(t *testing.T, id, scopeDir, initialState, serveStateDir string) {
	t.Helper()
	ws := wfstate.CreateInitialState(id, scopeDir, initialState, 10.0, nil, "")
	require.NoError(t, wfstate.WriteStateIn(id, ws, wfstate.PoolServe, serveStateDir))
}

// Test (a): `ray serve list` enumerates only serve-pool entries, skips both
// the abandoned/<ts>/ archives and any CLI-pool files, and emits ids in
// deterministic (alphanumeric) order.
func TestServeListSkipsAbandonedAndCLIPool(t *testing.T) {
	cliDir, serveDir := makeBothPoolDirs(t)

	// Seed three live serve-pool entries (intentionally out of alphabetical
	// order on disk; ListWorkflowsIn relies on os.ReadDir's sort to give
	// callers a deterministic order regardless of write order).
	writeServePoolWorkflow(t, "wf-serve-c", "workflows/c", "START.md", serveDir)
	writeServePoolWorkflow(t, "wf-serve-a", "workflows/a", "START.md", serveDir)
	writeServePoolWorkflow(t, "wf-serve-b", "workflows/b", "START.md", serveDir)

	// Seed an archived file under abandoned/<ts>/ — this MUST NOT appear in
	// the listing (state.ListWorkflowsIn reads only top level, so any
	// subdirectory under serve-state/ is inert by construction).
	abandonedTS := filepath.Join(serveDir, "abandoned", "2026-05-12T00-00-00.000000000Z")
	require.NoError(t, os.MkdirAll(abandonedTS, 0o755))
	abandonedWS := wfstate.CreateInitialState("wf-abandoned", "workflows/old", "START.md", 10.0, nil, "")
	abandonedBytes, err := json.MarshalIndent(abandonedWS, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(abandonedTS, "wf-abandoned.json"), abandonedBytes, 0o644))

	// Seed a CLI-pool entry that must not leak into the serve listing.
	writeWorkflow(t, "wf-cli-only", "workflows/test", "START.md", cliDir)

	out, _, err := run(t, "serve", "list", "--state-dir", serveDir)
	require.NoError(t, err)

	// Expected stdout, in order: the three live serve ids, alphanumerically.
	expected := "wf-serve-a\nwf-serve-b\nwf-serve-c\n"
	assert.Equal(t, expected, out)

	// Defensive: neither the abandoned id nor the CLI-pool id appears.
	assert.NotContains(t, out, "wf-abandoned")
	assert.NotContains(t, out, "wf-cli-only")
}

// Test (a) extra: empty serve pool reports "No workflows found" rather than
// erroring or printing a blank line — parity with `ray --list`.
func TestServeListEmpty(t *testing.T) {
	serveDir := makeServeStateDir(t)
	out, _, err := run(t, "serve", "list", "--state-dir", serveDir)
	require.NoError(t, err)
	assert.Contains(t, out, "No workflows found")
}

// Test (b): `ray --list` and `ray --recover` stay scoped to the CLI pool
// even when the serve pool is non-empty. Locks in the comment guarantee in
// cli.go's cmdList/cmdRecover.
func TestCLISurfacesStayCLIOnly(t *testing.T) {
	cliDir, serveDir := makeBothPoolDirs(t)
	writeWorkflow(t, "wf-cli-1", "workflows/a", "START.md", cliDir)
	writeServePoolWorkflow(t, "wf-serve-1", "workflows/b", "START.md", serveDir)
	writeServePoolWorkflow(t, "wf-serve-2", "workflows/c", "START.md", serveDir)

	listOut, _, err := run(t, "--list", "--state-dir", cliDir, "--serve-state-dir", serveDir)
	require.NoError(t, err)
	assert.Contains(t, listOut, "wf-cli-1")
	assert.NotContains(t, listOut, "wf-serve-1")
	assert.NotContains(t, listOut, "wf-serve-2")

	recOut, _, err := run(t, "--recover", "--state-dir", cliDir, "--serve-state-dir", serveDir)
	require.NoError(t, err)
	assert.Contains(t, recOut, "wf-cli-1")
	assert.NotContains(t, recOut, "wf-serve-1")
	assert.NotContains(t, recOut, "wf-serve-2")
}

// Test (c): `ray status` falls through to the serve pool when the id is
// absent from the CLI pool, and remains find-it-in-CLI for ids only in the
// CLI pool (no regression).
func TestStatusFallsThroughToServePool(t *testing.T) {
	cliDir, serveDir := makeBothPoolDirs(t)

	// Seed an id only in the serve pool with a distinguishable scope dir.
	writeServePoolWorkflow(t, "wf-serve-only", "workflows/from-serve", "START.md", serveDir)

	out, _, err := run(t, "--status", "wf-serve-only",
		"--state-dir", cliDir, "--serve-state-dir", serveDir)
	require.NoError(t, err)
	assert.Contains(t, out, "wf-serve-only")
	assert.Contains(t, out, "workflows/from-serve")

	// And the historical CLI-only path still works.
	writeWorkflow(t, "wf-cli-only", "workflows/from-cli", "START.md", cliDir)
	out, _, err = run(t, "--status", "wf-cli-only",
		"--state-dir", cliDir, "--serve-state-dir", serveDir)
	require.NoError(t, err)
	assert.Contains(t, out, "wf-cli-only")
	assert.Contains(t, out, "workflows/from-cli")

	// Miss in both pools surfaces the generic not-found error — never the
	// pool-specific path or the serve-pool's existence.
	_, _, err = run(t, "--status", "wf-nowhere",
		"--state-dir", cliDir, "--serve-state-dir", serveDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.NotContains(t, err.Error(), "serve-state")
	assert.NotContains(t, err.Error(), "serve pool")
}

// Edge case for the strict fallback semantic: a MALFORMED state file in the
// CLI pool must NOT silently fall through to the serve pool. The CLI pool is
// authoritative whenever its file is present (per the documented precedence
// rule in cmdStatus); a corrupted CLI copy is a corruption signal, not a
// miss. The operator gets the same generic "not found" the pre-Phase-7
// cmdStatus returned for any read error — no regression in error text, no
// leak of pool layout, and no masking of CLI-side corruption by a serve copy.
func TestStatusMalformedCLIDoesNotFallThrough(t *testing.T) {
	cliDir, serveDir := makeBothPoolDirs(t)

	// Same id in both pools; the CLI side is intentionally corrupted.
	const id = "wf-corrupt"
	require.NoError(t, os.WriteFile(
		filepath.Join(cliDir, id+".json"),
		[]byte("{not valid json"),
		0o644,
	))
	serveWS := wfstate.CreateInitialState(id, "workflows/from-serve", "START.md", 10.0, nil, "")
	require.NoError(t, wfstate.WriteStateIn(id, serveWS, wfstate.PoolServe, serveDir))

	_, _, err := run(t, "--status", id,
		"--state-dir", cliDir, "--serve-state-dir", serveDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.NotContains(t, err.Error(), "serve-state")
}

// Test (d): when the same id is present in both pools, the CLI pool's copy
// wins. We make the copies distinguishable by their ScopeDir so we can
// assert which one rendered.
func TestStatusCLITakesPrecedenceOverServe(t *testing.T) {
	cliDir, serveDir := makeBothPoolDirs(t)

	const id = "wf-shared"
	cliWS := wfstate.CreateInitialState(id, "workflows/from-cli", "START.md", 10.0, nil, "")
	require.NoError(t, wfstate.WriteStateIn(id, cliWS, wfstate.PoolCLI, cliDir))

	serveWS := wfstate.CreateInitialState(id, "workflows/from-serve", "START.md", 10.0, nil, "")
	require.NoError(t, wfstate.WriteStateIn(id, serveWS, wfstate.PoolServe, serveDir))

	out, _, err := run(t, "--status", id,
		"--state-dir", cliDir, "--serve-state-dir", serveDir)
	require.NoError(t, err)
	assert.Contains(t, out, "workflows/from-cli",
		"CLI pool must take precedence when the id is present in both pools")
	assert.NotContains(t, out, "workflows/from-serve",
		"serve-pool copy must not be opened when the CLI pool already has the id")
}

// --------------------------------------------------------------------------
// --resume
// --------------------------------------------------------------------------

func TestResumeNotFound(t *testing.T) {
	stateDir := makeStateDir(t)
	_, _, err := run(t, "--resume", "nonexistent-id", "--state-dir", stateDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResumeExisting(t *testing.T) {
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-resume", "workflows/test", "START.md", stateDir)

	// The test CLI runner is a no-op, so this just verifies dispatch works.
	_, _, err := run(t, "--resume", "wf-resume", "--state-dir", stateDir)
	require.NoError(t, err)
}

// --------------------------------------------------------------------------
// Start mode (positional arg)
// --------------------------------------------------------------------------

func TestVersionFlag(t *testing.T) {
	out, _, err := run(t, "--version")
	require.NoError(t, err)
	assert.Equal(t, "raymond version dev\n", out)
}

func TestNoArgsError(t *testing.T) {
	stateDir := makeStateDir(t)
	_, _, err := run(t, "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workflow specified")
}

func TestStartFileNotFound(t *testing.T) {
	stateDir := makeStateDir(t)
	_, _, err := run(t, "/nonexistent/path/START.md", "--state-dir", stateDir)
	require.Error(t, err)
}

func TestStartDirectory(t *testing.T) {
	stateDir := makeStateDir(t)
	workflowDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "1_START.md"), []byte("# Start"), 0o644))

	// The test runner is a no-op, so this just verifies the CLI dispatches
	// to start mode and parses the directory correctly.
	_, _, err := run(t, workflowDir, "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestStartDirectoryEntryPoint(t *testing.T) {
	stateDir := makeStateDir(t)
	workflowDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "1_START.md"), []byte("# Start"), 0o644))

	// The test CLI has a no-op runner, so the state file is written and we can
	// inspect the resolved initial state.
	_, _, err := run(t, workflowDir, "--state-dir", stateDir)
	require.NoError(t, err)

	ids, listErr := wfstate.ListWorkflows(stateDir)
	require.NoError(t, listErr)
	require.Len(t, ids, 1)

	ws, readErr := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, readErr)
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "1_START.md", ws.Agents[0].CurrentState)
}

func TestStartDirectoryScopeDirIsAbsolute(t *testing.T) {
	stateDir := makeStateDir(t)
	workflowDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "1_START.md"), []byte("# Start"), 0o644))

	// Change to the parent of workflowDir and pass a relative path so that
	// parseScopeAndState must absolutize it.
	origWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	require.NoError(t, os.Chdir(filepath.Dir(workflowDir)))
	relDir := filepath.Base(workflowDir)

	_, _, err := run(t, relDir, "--state-dir", stateDir)
	require.NoError(t, err)

	ids, listErr := wfstate.ListWorkflows(stateDir)
	require.NoError(t, listErr)
	require.Len(t, ids, 1)

	ws, readErr := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, readErr)
	assert.True(t, filepath.IsAbs(ws.ScopeDir),
		"workflow ScopeDir should be absolute, got: %s", ws.ScopeDir)
	require.Len(t, ws.Agents, 1)
	assert.True(t, filepath.IsAbs(ws.Agents[0].ScopeDir),
		"agent ScopeDir should be absolute, got: %s", ws.Agents[0].ScopeDir)
	assert.Equal(t, ws.ScopeDir, ws.Agents[0].ScopeDir,
		"agent ScopeDir should match workflow ScopeDir")
}

func TestStartZipFileEntryPoint(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	zipFile := filepath.Join(dir, "workflow.zip")
	writeTestZip(t, zipFile, map[string]string{"1_START.md": "# Start"})

	_, _, err := run(t, zipFile, "--state-dir", stateDir)
	require.NoError(t, err)

	ids, listErr := wfstate.ListWorkflows(stateDir)
	require.NoError(t, listErr)
	require.Len(t, ids, 1)

	ws, readErr := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, readErr)
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "1_START.md", ws.Agents[0].CurrentState)
}

func TestStartFile(t *testing.T) {
	stateDir := makeStateDir(t)

	// Create a temp file to use as the workflow state file.
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestStartZipFile(t *testing.T) {
	stateDir := makeStateDir(t)

	// Create a valid zip file so hash/layout validation passes.
	dir := t.TempDir()
	zipFile := filepath.Join(dir, "workflow.zip")
	writeTestZip(t, zipFile, map[string]string{"1_START.md": "# Start"})

	_, _, err := run(t, zipFile, "--state-dir", stateDir)
	// The no-op runner returns nil regardless of scope type.
	require.NoError(t, err)
}

func TestStartZipFileWithHashMismatch(t *testing.T) {
	stateDir := makeStateDir(t)

	dir := t.TempDir()
	badHash := "0000000000000000000000000000000000000000000000000000000000000000"
	zipFile := filepath.Join(dir, "workflow-"+badHash+".zip")
	writeTestZip(t, zipFile, map[string]string{"START.md": "# Start"})

	_, _, err := run(t, zipFile, "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hash")
}

func TestStartZipFileWithInvalidLayout(t *testing.T) {
	stateDir := makeStateDir(t)

	// Empty zip archive — layout validation will fail.
	dir := t.TempDir()
	zipFile := filepath.Join(dir, "workflow.zip")
	writeTestZip(t, zipFile, map[string]string{}) // empty zip → ZipLayoutError

	_, _, err := run(t, zipFile, "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "layout")
}

func TestResumeZipNotFound(t *testing.T) {
	stateDir := makeStateDir(t)

	// Create a workflow state whose scope points to a zip that does not exist.
	dir := t.TempDir()
	missingZip := filepath.Join(dir, "gone.zip")
	ws := wfstate.CreateInitialState("wf-zip-gone", missingZip, "START.md", 10.0, nil, "")
	require.NoError(t, wfstate.WriteState("wf-zip-gone", ws, stateDir))

	_, _, err := run(t, "--resume", "wf-zip-gone", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResumeZipWithHashInNameNotFound(t *testing.T) {
	stateDir := makeStateDir(t)

	// Zip whose filename contains a 64-char hex hash but the file itself is absent.
	// VerifyZipHash sees the hash, tries os.Open, gets ErrNotExist — must still
	// report "not found" rather than "hash validation failed".
	dir := t.TempDir()
	fakeHash := strings.Repeat("a", 64)
	missingZip := filepath.Join(dir, "workflow-"+fakeHash+".zip")
	ws := wfstate.CreateInitialState("wf-zip-hash-gone", missingZip, "START.md", 10.0, nil, "")
	require.NoError(t, wfstate.WriteState("wf-zip-hash-gone", ws, stateDir))

	_, _, err := run(t, "--resume", "wf-zip-hash-gone", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.NotContains(t, err.Error(), "hash validation failed")
}

func TestResumeZipHashMismatch(t *testing.T) {
	stateDir := makeStateDir(t)

	// Zip file exists but its content hash doesn't match the hash in the filename.
	dir := t.TempDir()
	badHash := strings.Repeat("0", 64)
	zipFile := filepath.Join(dir, "workflow-"+badHash+".zip")
	writeTestZip(t, zipFile, map[string]string{"START.md": "# Start"})

	ws := wfstate.CreateInitialState("wf-zip-hash-mismatch", zipFile, "START.md", 10.0, nil, "")
	require.NoError(t, wfstate.WriteState("wf-zip-hash-mismatch", ws, stateDir))

	_, _, err := run(t, "--resume", "wf-zip-hash-mismatch", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hash")
}

func TestResumeZipInvalidLayout(t *testing.T) {
	stateDir := makeStateDir(t)

	// Zip file exists but is empty — layout validation fails.
	dir := t.TempDir()
	zipFile := filepath.Join(dir, "workflow.zip")
	writeTestZip(t, zipFile, map[string]string{}) // empty zip → ZipLayoutError

	ws := wfstate.CreateInitialState("wf-zip-bad-layout", zipFile, "START.md", 10.0, nil, "")
	require.NoError(t, wfstate.WriteState("wf-zip-bad-layout", ws, stateDir))

	_, _, err := run(t, "--resume", "wf-zip-bad-layout", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "layout")
}

// --------------------------------------------------------------------------
// --init-config
// --------------------------------------------------------------------------

func TestInitConfig(t *testing.T) {
	dir := t.TempDir()
	// Simulate project root by creating a .git directory.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))

	origWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	require.NoError(t, os.Chdir(dir))

	out, _, err := run(t, "--init-config")
	require.NoError(t, err)
	assert.Contains(t, out, "Created")

	configPath := filepath.Join(dir, ".raymond", "config.toml")
	_, statErr := os.Stat(configPath)
	assert.NoError(t, statErr, "config.toml should have been created")
}

func TestInitConfigAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".raymond"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".raymond", "config.toml"), []byte("[raymond]\n"), 0o644,
	))

	origWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	require.NoError(t, os.Chdir(dir))

	_, _, err := run(t, "--init-config")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// --------------------------------------------------------------------------
// Flag defaults and parsing
// --------------------------------------------------------------------------

func TestFlagDefaultsDoNotErrorOnStart(t *testing.T) {
	// Verify that the default flags are accepted without error.
	// The start command with a real file should work with all defaults.
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestQuietFlag(t *testing.T) {
	// Verify --quiet is accepted without error.
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-q", "workflows/test", "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-q", "--quiet", "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestNoDebugFlag(t *testing.T) {
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-nd", "workflows/test", "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-nd", "--no-debug", "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestModelFlag(t *testing.T) {
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-m", "workflows/test", "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-m", "--model", "opus", "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestBudgetFlag(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--budget", "5.00", "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestInputFlag(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--input", "hello world", "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestDangerouslySkipPermissionsFlag(t *testing.T) {
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-dsp", "workflows/test", "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-dsp",
		"--dangerously-skip-permissions", "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestDangerouslySkipPermissionsExplicitFalse(t *testing.T) {
	// The flag accepts --dangerously-skip-permissions=false to opt out of
	// the new default-true behaviour and use --permission-mode=acceptEdits.
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-dsp-false", "workflows/test", "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-dsp-false",
		"--dangerously-skip-permissions=false", "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestInvalidModelFlagReturnsError(t *testing.T) {
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-m", "workflows/test", "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-m", "--model", "gpt5", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gpt5")
	assert.Contains(t, err.Error(), "model")
}

func TestNegativeBudgetFlagReturnsError(t *testing.T) {
	// --budget rejects negative values. Without this, a negative budget would
	// silently disable the cap (the executor's check is gated on BudgetUSD > 0),
	// which is more dangerous than the historic "kill on first cost" behaviour.
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-negbudget", "workflows/test", "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-negbudget", "--budget", "-5", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "budget")
	assert.Contains(t, err.Error(), "non-negative")
}

func TestNegativeTimeoutFlagReturnsError(t *testing.T) {
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-negtimeout", "workflows/test", "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-negtimeout", "--timeout", "-1", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
	assert.Contains(t, err.Error(), "non-negative")
}

func TestZeroBudgetFlagAccepted(t *testing.T) {
	// --budget 0 is the unlimited sentinel — must not error.
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-zerobudget", "workflows/test", "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-zerobudget", "--budget", "0", "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestInvalidEffortFlagReturnsError(t *testing.T) {
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-e", "workflows/test", "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-e", "--effort", "extreme", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extreme")
	assert.Contains(t, err.Error(), "effort")
}

func TestValidEffortFlagValues(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high"} {
		t.Run(effort, func(t *testing.T) {
			stateDir := makeStateDir(t)
			writeWorkflow(t, "wf-ef-"+effort, "workflows/test", "START.md", stateDir)

			_, _, err := run(t, "--resume", "wf-ef-"+effort, "--effort", effort, "--state-dir", stateDir)
			require.NoError(t, err)
		})
	}
}

// --------------------------------------------------------------------------
// Launch params persistence: --start saves params; --resume restores them
// --------------------------------------------------------------------------

// runCapturing executes the CLI with a capturing runner and returns captured RunOptions.
func runCapturing(t *testing.T, args ...string) ([]orchestrator.RunOptions, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	var captured []orchestrator.RunOptions
	c := cli.NewTestCLICapturing(&out, &errOut, &captured)
	cmd := c.NewRootCmd()
	cmd.SetArgs(args)
	err := cmd.Execute()
	return captured, err
}

func TestStartSavesLaunchParamsToStateFile(t *testing.T) {
	stateDir := makeStateDir(t)
	scope := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(scope, "START.md"), []byte("start"), 0o644))

	_, _, err := run(t, filepath.Join(scope, "START.md"),
		"--model", "opus",
		"--effort", "high",
		"--dangerously-skip-permissions",
		"--state-dir", stateDir,
	)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)
	require.NotNil(t, ws.LaunchParams, "launch_params should be saved in state file")
	assert.Equal(t, "opus", ws.LaunchParams.Model)
	assert.Equal(t, "high", ws.LaunchParams.Effort)
	assert.True(t, ws.LaunchParams.DangerouslySkipPermissions)
}

func TestStartSavesDangerouslySkipPermissionsFalseToLaunchParams(t *testing.T) {
	// An explicit --dangerously-skip-permissions=false at start time must
	// be persisted to launch_params so a later resume preserves the opt-out
	// even if the global default has shifted.
	stateDir := makeStateDir(t)
	scope := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(scope, "START.md"), []byte("start"), 0o644))

	_, _, err := run(t, filepath.Join(scope, "START.md"),
		"--dangerously-skip-permissions=false",
		"--state-dir", stateDir,
	)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)
	require.NotNil(t, ws.LaunchParams)
	assert.False(t, ws.LaunchParams.DangerouslySkipPermissions,
		"explicit --dangerously-skip-permissions=false must round-trip to launch_params")
}

func TestStartSavesDefaultLaunchParamsWhenFlagsUnspecified(t *testing.T) {
	stateDir := makeStateDir(t)
	scope := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(scope, "START.md"), []byte("start"), 0o644))

	_, _, err := run(t, filepath.Join(scope, "START.md"), "--state-dir", stateDir)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)
	// launch_params must exist even when no explicit flags are passed.
	require.NotNil(t, ws.LaunchParams)
	// Model and Effort should be empty when not specified (config.toml doesn't set them).
	assert.Equal(t, "", ws.LaunchParams.Model)
	assert.Equal(t, "", ws.LaunchParams.Effort)
	// Note: DangerouslySkipPermissions is intentionally not checked here because
	// the project's .raymond/config.toml may set it, making the value environment-dependent.
}

func TestResumeRestoresSavedModel(t *testing.T) {
	stateDir := makeStateDir(t)
	lp := &wfstate.LaunchParams{Model: "haiku", Effort: "low", DangerouslySkipPermissions: false}
	ws := wfstate.CreateInitialState("wf-restore-model", "scope", "START.md", 10.0, nil, "", lp)
	require.NoError(t, wfstate.WriteState("wf-restore-model", ws, stateDir))

	captured, err := runCapturing(t, "--resume", "wf-restore-model", "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	assert.Equal(t, "haiku", captured[0].DefaultModel,
		"resume should restore saved model when --model not specified on CLI")
	assert.Equal(t, "low", captured[0].DefaultEffort,
		"resume should restore saved effort when --effort not specified on CLI")
}

func TestResumeCLIModelOverridesSavedModel(t *testing.T) {
	stateDir := makeStateDir(t)
	lp := &wfstate.LaunchParams{Model: "haiku", Effort: "low"}
	ws := wfstate.CreateInitialState("wf-override-model", "scope", "START.md", 10.0, nil, "", lp)
	require.NoError(t, wfstate.WriteState("wf-override-model", ws, stateDir))

	// CLI explicitly passes --model opus; should override saved haiku.
	captured, err := runCapturing(t, "--resume", "wf-override-model", "--model", "opus", "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	assert.Equal(t, "opus", captured[0].DefaultModel,
		"CLI --model should override saved model on resume")
}

func TestResumeDangerouslySkipPermissionsRestored(t *testing.T) {
	stateDir := makeStateDir(t)
	lp := &wfstate.LaunchParams{DangerouslySkipPermissions: true}
	ws := wfstate.CreateInitialState("wf-dsp-restore", "scope", "START.md", 10.0, nil, "", lp)
	require.NoError(t, wfstate.WriteState("wf-dsp-restore", ws, stateDir))

	captured, err := runCapturing(t, "--resume", "wf-dsp-restore", "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	assert.True(t, captured[0].DangerouslySkipPermissions,
		"resume should restore dangerously-skip-permissions=true from launch_params")
}

func TestResumeDangerouslySkipPermissionsFalseRestored(t *testing.T) {
	// A workflow started with --dangerously-skip-permissions=false must
	// resume with that opt-out preserved, not silently inherit the new
	// default of true.
	stateDir := makeStateDir(t)
	lp := &wfstate.LaunchParams{DangerouslySkipPermissions: false}
	ws := wfstate.CreateInitialState("wf-dsp-restore-false", "scope", "START.md", 10.0, nil, "", lp)
	require.NoError(t, wfstate.WriteState("wf-dsp-restore-false", ws, stateDir))

	captured, err := runCapturing(t, "--resume", "wf-dsp-restore-false", "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	assert.False(t, captured[0].DangerouslySkipPermissions,
		"resume should restore dangerously-skip-permissions=false from launch_params")
}

func TestResumeCLIExplicitFalseOverridesSavedTrue(t *testing.T) {
	// CLI --dangerously-skip-permissions=false must beat a saved true.
	stateDir := makeStateDir(t)
	lp := &wfstate.LaunchParams{DangerouslySkipPermissions: true}
	ws := wfstate.CreateInitialState("wf-dsp-cli-override", "scope", "START.md", 10.0, nil, "", lp)
	require.NoError(t, wfstate.WriteState("wf-dsp-cli-override", ws, stateDir))

	captured, err := runCapturing(t,
		"--resume", "wf-dsp-cli-override",
		"--dangerously-skip-permissions=false",
		"--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	assert.False(t, captured[0].DangerouslySkipPermissions,
		"explicit --dangerously-skip-permissions=false on CLI should override saved true")
}

func TestResumeNoLaunchParamsUsesDefaults(t *testing.T) {
	stateDir := makeStateDir(t)
	// Old-style state file with no launch_params.
	ws := wfstate.CreateInitialState("wf-no-lp", "scope", "START.md", 10.0, nil, "")
	require.NoError(t, wfstate.WriteState("wf-no-lp", ws, stateDir))

	captured, err := runCapturing(t, "--resume", "wf-no-lp", "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	// With no launch_params, model should be empty (the .raymond/config.toml doesn't set it).
	assert.Equal(t, "", captured[0].DefaultModel)
	// Note: DangerouslySkipPermissions is intentionally not checked here because
	// the project's .raymond/config.toml may set it, making the value environment-dependent.
}

// --------------------------------------------------------------------------
// --workflow-id flag
// --------------------------------------------------------------------------

func TestWorkflowIDFlagCreatesWorkflowWithSpecifiedID(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--workflow-id", "my-custom-wf", "--state-dir", stateDir)
	require.NoError(t, err)

	// The workflow should exist with the specified ID.
	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)
	assert.Equal(t, "my-custom-wf", ids[0])
}

func TestWorkflowIDFlagWithHyphensAndUnderscores(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--workflow-id", "my_wf-001", "--state-dir", stateDir)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)
	assert.Equal(t, "my_wf-001", ids[0])
}

func TestWorkflowIDFlagInvalidCharactersReturnsError(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--workflow-id", "invalid/id", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestWorkflowIDFlagTooLongReturnsError(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	longID := strings.Repeat("a", 256)
	_, _, err := run(t, startFile, "--workflow-id", longID, "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too long")
}

func TestWorkflowIDFlagDuplicateIDReturnsError(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	// Create an existing workflow with the target ID.
	writeWorkflow(t, "taken-id", dir, "START.md", stateDir)

	// Attempting to start a new workflow with the same ID must fail.
	_, _, err := run(t, startFile, "--workflow-id", "taken-id", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "--resume")
}

// --------------------------------------------------------------------------
// --continue-session flag
// --------------------------------------------------------------------------

func TestContinueSessionFlagSetsLaunchParamsAndAgentState(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--continue-session", "--state-dir", stateDir)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)

	// LaunchParams should have ContinueAndFork set.
	require.NotNil(t, ws.LaunchParams)
	assert.True(t, ws.LaunchParams.ContinueAndFork,
		"LaunchParams.ContinueAndFork should be true")

	// The agent should also have ContinueAndFork set.
	require.Len(t, ws.Agents, 1)
	assert.True(t, ws.Agents[0].ContinueAndFork,
		"AgentState.ContinueAndFork should be true")
}

func TestContinueSessionFlagDefaultFalse(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--state-dir", stateDir)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)
	require.Len(t, ws.Agents, 1)
	assert.False(t, ws.Agents[0].ContinueAndFork,
		"AgentState.ContinueAndFork should be false by default")
}

// --------------------------------------------------------------------------
// Remote URL workflow support
// --------------------------------------------------------------------------

// buildWorkflowZipBytes creates a valid workflow zip in memory (flat layout,
// contains START.md) and returns its bytes plus the SHA256 hex hash.
func buildWorkflowZipBytes(t *testing.T) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, err := w.Create("START.md")
	require.NoError(t, err)
	_, err = fw.Write([]byte("# Start\nThis is the entry point."))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	data := buf.Bytes()
	h := sha256.Sum256(data)
	return data, hex.EncodeToString(h[:])
}

func TestRemoteURLFreshDownload(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".raymond", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	zipData, hash := buildWorkflowZipBytes(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipData)
	}))
	defer srv.Close()

	url := fmt.Sprintf("%s/workflow_%s.zip", srv.URL, hash)
	captured, err := runCapturing(t, url, "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1, "runner should have been invoked once")

	registryPath := filepath.Join(tmpDir, ".raymond", "registry", hash+".zip")
	require.FileExists(t, registryPath, "downloaded zip should exist in registry")
}

func TestRemoteURLCacheHit(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".raymond", "state")
	registryDir := filepath.Join(tmpDir, ".raymond", "registry")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.MkdirAll(registryDir, 0o755))

	zipData, hash := buildWorkflowZipBytes(t)

	// Pre-populate the registry cache.
	cachedPath := filepath.Join(registryDir, hash+".zip")
	require.NoError(t, os.WriteFile(cachedPath, zipData, 0o644))

	// Port 1 is never listening; if Fetch tries the network it will fail.
	url := fmt.Sprintf("http://127.0.0.1:1/workflow_%s.zip", hash)
	captured, err := runCapturing(t, url, "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1, "runner should have been invoked from cache without network")
}

func TestRemoteURLBadScheme(t *testing.T) {
	hash := strings.Repeat("a", 64)
	url := fmt.Sprintf("ftp://host/workflow_%s.zip", hash)
	_, _, err := run(t, url, "--state-dir", makeStateDir(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported URL scheme")
}

func TestRemoteURLMissingHash(t *testing.T) {
	_, _, err := run(t, "http://host/workflow.zip", "--state-dir", makeStateDir(t))
	require.Error(t, err)
}

func TestRemoteURLAmbiguousHash(t *testing.T) {
	hash := strings.Repeat("a", 64)
	// Two 64-char hex sequences in the filename.
	url := fmt.Sprintf("http://host/workflow_%s_%s.zip", hash, hash)
	_, _, err := run(t, url, "--state-dir", makeStateDir(t))
	require.Error(t, err)
}

func TestRemoteURLHashMismatchAfterDownload(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".raymond", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	// Serve content that does NOT match the hash in the URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("wrong content"))
	}))
	defer srv.Close()

	hash := strings.Repeat("b", 64)
	url := fmt.Sprintf("%s/workflow_%s.zip", srv.URL, hash)
	_, _, err := run(t, url, "--state-dir", stateDir)
	require.Error(t, err)
}

func TestResumeUsesLocalRegistryPath(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".raymond", "state")
	registryDir := filepath.Join(tmpDir, ".raymond", "registry")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.MkdirAll(registryDir, 0o755))

	zipData, hash := buildWorkflowZipBytes(t)

	cachedPath := filepath.Join(registryDir, hash+".zip")
	require.NoError(t, os.WriteFile(cachedPath, zipData, 0o644))

	writeWorkflow(t, "wf-local-resume", cachedPath, "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-local-resume", "--state-dir", stateDir)
	require.NoError(t, err)
}

// --------------------------------------------------------------------------
// ScopeURL wiring
// --------------------------------------------------------------------------

func TestURLStartPopulatesScopeURL(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".raymond", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	zipData, hash := buildWorkflowZipBytes(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipData)
	}))
	defer srv.Close()

	url := fmt.Sprintf("%s/workflow_%s.zip", srv.URL, hash)
	_, _, err := run(t, url, "--state-dir", stateDir)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, url, ws.Agents[0].ScopeURL, "ScopeURL should equal the original remote URL")
}

func TestLocalPathStartScopeURLEmpty(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--state-dir", stateDir)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "", ws.Agents[0].ScopeURL, "ScopeURL should be empty for local-path starts")
}

// --------------------------------------------------------------------------
// --name flag / title bar wiring
// --------------------------------------------------------------------------

// runTitlebar runs the CLI with the given args, then manually exercises the
// ObserverSetup closure (which is normally called by the real runner) by
// emitting a StateStarted event on a fresh bus. Returns whatever the
// title bar observer wrote to the CLI's captured stdout buffer.
func runTitlebar(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	var captured []orchestrator.RunOptions
	c := cli.NewTestCLICapturing(&out, &errOut, &captured)
	cmd := c.NewRootCmd()
	cmd.SetArgs(args)
	err := cmd.Execute()
	if len(captured) == 1 && captured[0].ObserverSetup != nil {
		b := bus.New()
		captured[0].ObserverSetup(b)
		b.Emit(events.StateStarted{StateName: "START.md"})
	}
	return out.String(), err
}

func TestNameFlagTitleBar(t *testing.T) {
	tests := []struct {
		testName string
		nameArgs []string
		want     string
	}{
		{"with name", []string{"--name", "foo"}, "foo ray: "},
		{"no name", []string{}, "ray: "},
		{"name with spaces trimmed", []string{"--name", "  foo  "}, "foo ray: "},
	}
	for _, tc := range tests {
		t.Run(tc.testName, func(t *testing.T) {
			stateDir := makeStateDir(t)
			dir := t.TempDir()
			startFile := filepath.Join(dir, "START.md")
			require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

			args := append([]string{startFile, "--state-dir", stateDir}, tc.nameArgs...)
			out, err := runTitlebar(t, args...)
			require.NoError(t, err)
			assert.Contains(t, out, tc.want)
		})
	}
}

func TestNameFromConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".raymond"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".raymond", "config.toml"),
		[]byte("[raymond]\nname = \"bar\"\n"),
		0o644,
	))
	stateDir := filepath.Join(dir, ".raymond", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	origWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	require.NoError(t, os.Chdir(dir))

	out, err := runTitlebar(t, startFile, "--state-dir", stateDir)
	require.NoError(t, err)
	assert.Contains(t, out, "bar ray: ")
}

func TestNameCLIOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".raymond"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".raymond", "config.toml"),
		[]byte("[raymond]\nname = \"cfg\"\n"),
		0o644,
	))
	stateDir := filepath.Join(dir, ".raymond", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	origWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	require.NoError(t, os.Chdir(dir))

	out, err := runTitlebar(t, startFile, "--name", "cli", "--state-dir", stateDir)
	require.NoError(t, err)
	assert.Contains(t, out, "cli ray: ")
	assert.NotContains(t, out, "cfg ray: ")
}

// --------------------------------------------------------------------------
// diagram subcommand
// --------------------------------------------------------------------------

func TestDiagramDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("<goto>NEXT.md</goto>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "NEXT.md"), []byte("<result>done</result>"), 0o644))

	out, errOut, err := run(t, "diagram", dir)
	require.NoError(t, err)
	assert.Contains(t, out, "flowchart TD")
	assert.Contains(t, out, "1_START -->|goto| NEXT")
	assert.Empty(t, errOut)
}

func TestDiagramZipFile(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "wf.zip")
	writeTestZip(t, zipPath, map[string]string{
		"1_START.md": "<goto>NEXT.md</goto>",
		"NEXT.md":    "<result>done</result>",
	})

	out, _, err := run(t, "diagram", zipPath)
	require.NoError(t, err)
	assert.Contains(t, out, "flowchart TD")
	assert.Contains(t, out, "1_START -->|goto| NEXT")
}

func TestDiagramFileNotDir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.md")
	require.NoError(t, os.WriteFile(f, []byte("hello"), 0o644))

	_, _, err := run(t, "diagram", f)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory, zip archive, or YAML workflow")
}

func TestDiagramNonexistentPath(t *testing.T) {
	_, _, err := run(t, "diagram", "/nonexistent/path")
	assert.Error(t, err)
}

func TestDiagramWarningsToStderr(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("<goto>MISSING.md</goto>"), 0o644))

	out, errOut, err := run(t, "diagram", dir)
	require.NoError(t, err)
	// Diagram should still be generated on stdout.
	assert.Contains(t, out, "flowchart TD")
	// Missing node should appear with dashed style on stdout.
	assert.Contains(t, out, "style MISSING stroke-dasharray: 5 5")
	// Warning about the missing state must appear on stderr, not stdout.
	assert.Contains(t, errOut, "warning:")
	assert.NotContains(t, out, "warning:")
}

func TestDiagramWinFlag(t *testing.T) {
	dir := t.TempDir()
	// Use distinct stems so the two nodes are distinguishable in the output.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "UNIX_STEP.sh"), []byte("echo '<result>done</result>'"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "WIN_STEP.bat"), []byte("echo ^<result^>done^</result^>"), 0o644))

	out, _, err := run(t, "diagram", "--win", dir)
	require.NoError(t, err)
	assert.Contains(t, out, "WIN_STEP")
	assert.NotContains(t, out, "UNIX_STEP")
}

func TestDiagramDefaultExcludesWindows(t *testing.T) {
	dir := t.TempDir()
	// Use distinct stems so the two nodes are distinguishable in the output.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "UNIX_STEP.sh"), []byte("echo '<result>done</result>'"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "WIN_STEP.bat"), []byte("echo ^<result^>done^</result^>"), 0o644))

	out, _, err := run(t, "diagram", dir)
	require.NoError(t, err)
	assert.Contains(t, out, "UNIX_STEP")
	assert.NotContains(t, out, "WIN_STEP")
}

func TestDiagramHTMLOutput(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("<goto>NEXT.md</goto>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "NEXT.md"), []byte("<result>done</result>"), 0o644))
	outFile := filepath.Join(t.TempDir(), "out.html")

	_, _, err := run(t, "diagram", "--html", "--output", outFile, dir)
	require.NoError(t, err)

	data, readErr := os.ReadFile(outFile)
	require.NoError(t, readErr)
	content := string(data)
	assert.Contains(t, content, "flowchart TD")
	assert.Contains(t, content, "<html")
}

func TestDiagramHTMLNoStdout(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("<result>done</result>"), 0o644))
	outFile := filepath.Join(t.TempDir(), "out.html")

	out, _, err := run(t, "diagram", "--html", "--output", outFile, dir)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestDiagramHTMLWarningsToStderr(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("<goto>MISSING.md</goto>"), 0o644))
	outFile := filepath.Join(t.TempDir(), "out.html")

	_, errOut, err := run(t, "diagram", "--html", "--output", outFile, dir)
	require.NoError(t, err)
	assert.Contains(t, errOut, "warning:")
}

func TestDiagramOutputWithoutHTML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("<result>done</result>"), 0o644))

	_, _, err := run(t, "diagram", "--output", "foo.html", dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "--output requires --html")
}

// --------------------------------------------------------------------------
// lint subcommand
// --------------------------------------------------------------------------

// fixturePath returns the path to a lint test fixture directory relative to
// the CLI package.
func fixturePath(name string) string {
	return filepath.Join("..", "..", "workflows", "test_cases", "lint", name)
}

// isLintSentinel reports whether err is a LintFoundErrorsError sentinel.
func isLintSentinel(err error) bool {
	var sentinel cli.LintFoundErrorsError
	return errors.As(err, &sentinel)
}

func TestLintCleanWorkflow(t *testing.T) {
	out, _, err := run(t, "lint", fixturePath("valid_simple"))
	require.NoError(t, err)
	assert.Contains(t, out, "No issues found.")
	assert.NotContains(t, out, "error")
	assert.NotContains(t, out, "warning")
}

func TestLintWorkflowWithErrors(t *testing.T) {
	out, _, err := run(t, "lint", fixturePath("missing_target"))
	require.True(t, isLintSentinel(err), "expected LintFoundErrorsError sentinel, got: %v", err)
	assert.Contains(t, out, "error:")
	assert.Contains(t, out, "missing-target")
	// Summary line is present (e.g. "1 error").
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Greater(t, len(lines), 1, "expected a summary line after diagnostic lines")
}

func TestLintJSONOutput(t *testing.T) {
	out, _, err := run(t, "lint", "--json", fixturePath("missing_target"))
	require.True(t, isLintSentinel(err), "expected LintFoundErrorsError sentinel, got: %v", err)

	// Strip trailing newline before unmarshaling.
	var diags []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &diags))
	require.NotEmpty(t, diags)

	// Every element must have non-empty string values for the required keys.
	for _, d := range diags {
		for _, key := range []string{"severity", "file", "message", "check"} {
			val, ok := d[key]
			require.True(t, ok, "missing key %q in diagnostic", key)
			str, ok := val.(string)
			require.True(t, ok, "key %q is not a string", key)
			assert.NotEmpty(t, str, "key %q is empty", key)
		}
	}

	// At least one diagnostic has check "missing-target".
	found := false
	for _, d := range diags {
		if d["check"] == "missing-target" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected at least one diagnostic with check==\"missing-target\"")
}

func TestLintLevelFilterWithErrors(t *testing.T) {
	// --level error: only errors printed, warnings suppressed; sentinel returned.
	out, _, err := run(t, "lint", "--level", "error", fixturePath("mixed_errors_warnings"))
	require.True(t, isLintSentinel(err), "expected LintFoundErrorsError sentinel, got: %v", err)
	assert.Contains(t, out, "error:")
	assert.NotContains(t, out, "warning:")
	// Summary still reflects total unfiltered count (both error and warning).
	assert.Contains(t, out, "warning")
}

func TestLintLevelFilterNoErrors(t *testing.T) {
	// --level error on a warnings-only workflow: sentinel NOT returned, exit 0.
	out, _, err := run(t, "lint", "--level", "error", fixturePath("unreachable_state"))
	require.NoError(t, err)
	// Warning diagnostic lines are filtered out.
	assert.NotContains(t, out, "warning:")
	// Summary still reflects the warning count.
	assert.Contains(t, out, "warning")
	// "No issues found." is NOT printed (there are warnings, just filtered).
	assert.NotContains(t, out, "No issues found.")
}

func TestLintBadPath(t *testing.T) {
	_, _, err := run(t, "lint", "/nonexistent/path/that/does/not/exist")
	require.Error(t, err)
	assert.False(t, isLintSentinel(err), "I/O error must not be a sentinel")
}

func TestLintNonDirectoryNonZip(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "plain.txt")
	require.NoError(t, os.WriteFile(plain, []byte("hello"), 0o644))

	_, _, err := run(t, "lint", plain)
	require.Error(t, err)
}

func TestLintZipValid(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "workflow.zip")
	writeTestZip(t, zipPath, map[string]string{
		"1_START.md": "<result></result>",
	})

	out, _, err := run(t, "lint", zipPath)
	require.NoError(t, err)
	assert.Contains(t, out, "No issues found.")
}

func TestLintZipMissingEntryPoint(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "workflow.zip")
	writeTestZip(t, zipPath, map[string]string{
		"NOTSTART.md": "<result></result>",
	})

	out, _, err := run(t, "lint", "--json", zipPath)
	require.True(t, isLintSentinel(err), "expected LintFoundErrorsError sentinel, got: %v", err)
	assert.Contains(t, out, "no-entry-point")
}

func TestLintLevelErrorNoIssues(t *testing.T) {
	// --level error on a clean workflow: exit 0, "No issues found." printed.
	out, _, err := run(t, "lint", "--level", "error", fixturePath("valid_simple"))
	require.NoError(t, err)
	assert.Contains(t, out, "No issues found.")
}

func TestLintWinFlag(t *testing.T) {
	dir := t.TempDir()
	// 1_START.md transitions to ACTION (extensionless).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("<goto>ACTION</goto>"), 0o644))
	// Both .md and .bat variants exist — ambiguous on Windows, not on Unix.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ACTION.md"), []byte("<result></result>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ACTION.bat"), []byte(""), 0o644))

	// Without --win: .bat is excluded → only ACTION.md → no ambiguity.
	out, _, err := run(t, "lint", dir)
	require.NoError(t, err)
	assert.Contains(t, out, "No issues found.")

	// With --win: both ACTION.md and ACTION.bat included → ambiguous-state-resolution error.
	out, _, err = run(t, "lint", "--win", "--json", dir)
	require.True(t, isLintSentinel(err), "expected LintFoundErrorsError sentinel with --win, got: %v", err)
	assert.Contains(t, out, "ambiguous-state-resolution")
}

// writeTestYaml writes a YAML workflow file and returns its path.
func writeTestYaml(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "workflow.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// --------------------------------------------------------------------------
// lint — YAML scope
// --------------------------------------------------------------------------

func TestLintYamlValid(t *testing.T) {
	yamlPath := writeTestYaml(t, t.TempDir(), `id: test-lint-yaml
name: Test Lint YAML
description: Regression fixture - embedded manifest fields must not affect CLI behavior.

states:
  1_START:
    prompt: |
      Do the work.
    allowed_transitions:
      - { tag: goto, target: DONE.md }
  DONE:
    prompt: |
      Finished.
    allowed_transitions:
      - { tag: result }
`)
	out, _, err := run(t, "lint", yamlPath)
	require.NoError(t, err)
	// No error-severity diagnostics — cmdLint returns nil (exit 0).
	assert.NotContains(t, out, "error:")
}

func TestLintYamlInvalid(t *testing.T) {
	yamlPath := writeTestYaml(t, t.TempDir(), `not_states: true`)
	_, _, err := run(t, "lint", yamlPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "YAML workflow invalid")
}

func TestLintYamlWithDiagnostics(t *testing.T) {
	yamlPath := writeTestYaml(t, t.TempDir(), `states:
  1_START:
    prompt: |
      Do the work.
    allowed_transitions:
      - { tag: goto, target: NONEXISTENT.md }
`)
	out, _, err := run(t, "lint", "--json", yamlPath)
	require.True(t, isLintSentinel(err), "expected LintFoundErrorsError sentinel, got: %v", err)
	assert.Contains(t, out, "missing-target")
}

// --------------------------------------------------------------------------
// diagram — YAML scope
// --------------------------------------------------------------------------

func TestDiagramYamlFile(t *testing.T) {
	yamlPath := writeTestYaml(t, t.TempDir(), `states:
  1_START:
    prompt: |
      Do the work.
    allowed_transitions:
      - { tag: goto, target: NEXT.md }
  NEXT:
    prompt: |
      Finished.
    allowed_transitions:
      - { tag: result }
`)
	out, _, err := run(t, "diagram", yamlPath)
	require.NoError(t, err)
	assert.Contains(t, out, "flowchart TD")
	assert.Contains(t, out, "1_START")
	assert.Contains(t, out, "NEXT")
}

// --------------------------------------------------------------------------
// Start/Resume — YAML scope
// --------------------------------------------------------------------------

const testYamlWorkflow = `states:
  1_START:
    prompt: |
      Do the work.
    allowed_transitions:
      - { tag: goto, target: DONE.md }
  DONE:
    prompt: |
      Finished.
    allowed_transitions:
      - { tag: result }
`

func TestParseScopeAndState_YamlFile(t *testing.T) {
	stateDir := makeStateDir(t)
	yamlPath := writeTestYaml(t, t.TempDir(), testYamlWorkflow)

	_, _, err := run(t, yamlPath, "--state-dir", stateDir)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)
	assert.Equal(t, yamlPath, ws.ScopeDir)
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "1_START.md", ws.Agents[0].CurrentState,
		"entry point should be resolved to 1_START.md virtual file")
}

func TestParseScopeAndState_YmlExtension(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	ymlPath := filepath.Join(dir, "workflow.yml")
	require.NoError(t, os.WriteFile(ymlPath, []byte(testYamlWorkflow), 0o644))

	_, _, err := run(t, ymlPath, "--state-dir", stateDir)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)
	assert.Equal(t, ymlPath, ws.ScopeDir)
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "1_START.md", ws.Agents[0].CurrentState)
}

func TestParseScopeAndState_YamlSlashState(t *testing.T) {
	stateDir := makeStateDir(t)
	yamlPath := writeTestYaml(t, t.TempDir(), testYamlWorkflow)

	// Use "workflow.yaml/DONE" syntax to specify an initial state.
	_, _, err := run(t, yamlPath+"/DONE", "--state-dir", stateDir)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)
	assert.Equal(t, yamlPath, ws.ScopeDir)
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "DONE.md", ws.Agents[0].CurrentState,
		"state name should be resolved to DONE.md virtual file")
}

func TestStartYamlValidFile(t *testing.T) {
	stateDir := makeStateDir(t)
	yamlPath := writeTestYaml(t, t.TempDir(), testYamlWorkflow)

	_, _, err := run(t, yamlPath, "--state-dir", stateDir)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(ws.ScopeDir), "scope dir should be absolute")
	assert.Equal(t, "1_START.md", ws.Agents[0].CurrentState)
}

func TestStartYamlSlashStateResolution(t *testing.T) {
	stateDir := makeStateDir(t)
	yamlPath := writeTestYaml(t, t.TempDir(), testYamlWorkflow)

	captured, err := runCapturing(t, yamlPath+"/DONE", "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1, "runner should have been invoked once")
}

func TestStartYamlInvalidFile(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	badYaml := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(badYaml, []byte("not_states: true"), 0o644))

	_, _, err := run(t, badYaml, "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "YAML workflow invalid")
}

func TestStartYamlFileNotFound(t *testing.T) {
	stateDir := makeStateDir(t)
	_, _, err := run(t, "/nonexistent/workflow.yaml", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot access")
}

func TestResumeYamlScope(t *testing.T) {
	stateDir := makeStateDir(t)
	yamlPath := writeTestYaml(t, t.TempDir(), testYamlWorkflow)

	// Create a workflow state that references the YAML scope.
	ws := wfstate.CreateInitialState("wf-yaml-resume", yamlPath, "1_START.md", 10.0, nil, "")
	require.NoError(t, wfstate.WriteState("wf-yaml-resume", ws, stateDir))

	_, _, err := run(t, "--resume", "wf-yaml-resume", "--state-dir", stateDir)
	require.NoError(t, err)
}

func TestResumeYamlNotFound(t *testing.T) {
	stateDir := makeStateDir(t)
	missingYaml := filepath.Join(t.TempDir(), "gone.yaml")

	ws := wfstate.CreateInitialState("wf-yaml-gone", missingYaml, "1_START.md", 10.0, nil, "")
	require.NoError(t, wfstate.WriteState("wf-yaml-gone", ws, stateDir))

	_, _, err := run(t, "--resume", "wf-yaml-gone", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "yaml workflow not found")
}

func TestResumeYamlInvalid(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	badYaml := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(badYaml, []byte("not_states: true"), 0o644))

	ws := wfstate.CreateInitialState("wf-yaml-bad", badYaml, "1_START.md", 10.0, nil, "")
	require.NoError(t, wfstate.WriteState("wf-yaml-bad", ws, stateDir))

	_, _, err := run(t, "--resume", "wf-yaml-bad", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "YAML workflow invalid")
}

// --------------------------------------------------------------------------
// --on-ask flag
// --------------------------------------------------------------------------

func TestOnAskPauseAcceptedAndPassedToRunOptions(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	captured, err := runCapturing(t, startFile, "--on-ask", "pause", "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	assert.Equal(t, "pause", captured[0].OnAsk)
}

func TestOnAskRejectAccepted(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	captured, err := runCapturing(t, startFile, "--on-ask", "reject", "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	assert.Equal(t, "reject", captured[0].OnAsk)
}

func TestOnAskDefaultIsReject(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	captured, err := runCapturing(t, startFile, "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	assert.Equal(t, "reject", captured[0].OnAsk)
}

func TestOnAskInvalidValueProducesError(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--on-ask", "foo", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --on-ask value")
	assert.Contains(t, err.Error(), "foo")
}

func TestResumeRestoresOnAskFromLaunchParams(t *testing.T) {
	stateDir := makeStateDir(t)
	lp := &wfstate.LaunchParams{OnAsk: "pause"}
	ws := wfstate.CreateInitialState("wf-ask-restore", "scope", "START.md", 10.0, nil, "", lp)
	require.NoError(t, wfstate.WriteState("wf-ask-restore", ws, stateDir))

	captured, err := runCapturing(t, "--resume", "wf-ask-restore", "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	assert.Equal(t, "pause", captured[0].OnAsk,
		"resume should restore on-ask from launch_params when --on-ask not specified on CLI")
}

func TestResumeExplicitOnAskOverridesLaunchParams(t *testing.T) {
	stateDir := makeStateDir(t)
	lp := &wfstate.LaunchParams{OnAsk: "pause"}
	ws := wfstate.CreateInitialState("wf-ask-override", "scope", "START.md", 10.0, nil, "", lp)
	require.NoError(t, wfstate.WriteState("wf-ask-override", ws, stateDir))

	captured, err := runCapturing(t, "--resume", "wf-ask-override", "--on-ask", "reject", "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	assert.Equal(t, "reject", captured[0].OnAsk,
		"CLI --on-ask should override saved value on resume")
}

func TestStartSavesOnAskToLaunchParams(t *testing.T) {
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	_, _, err := run(t, startFile, "--on-ask", "pause", "--state-dir", stateDir)
	require.NoError(t, err)

	ids, err := wfstate.ListWorkflows(stateDir)
	require.NoError(t, err)
	require.Len(t, ids, 1)

	ws, err := wfstate.ReadState(ids[0], stateDir)
	require.NoError(t, err)
	require.NotNil(t, ws.LaunchParams)
	assert.Equal(t, "pause", ws.LaunchParams.OnAsk)
}

// --------------------------------------------------------------------------
// PendingAskError → JSON output and exit code 2
// --------------------------------------------------------------------------

// newAskingCLI creates a CLI whose runner returns the given PendingAskError.
func newAskingCLI(stdout, stderr *bytes.Buffer, askErr *orchestrator.PendingAskError) *cli.CLI {
	return cli.NewTestCLIWithRunner(stdout, stderr, func(_ context.Context, _ string, _ orchestrator.RunOptions) error {
		return askErr
	})
}

func TestPendingAskErrorPropagatesThroughCLI(t *testing.T) {
	// Verify that *PendingAskError from the runner propagates back through
	// cobra's RunE → Execute() return value, so Run() can detect it.
	stateDir := makeStateDir(t)
	dir := t.TempDir()
	startFile := filepath.Join(dir, "START.md")
	require.NoError(t, os.WriteFile(startFile, []byte("# Start"), 0o644))

	askErr := &orchestrator.PendingAskError{
		Status:   "asking",
		RunID:    "test-wf-123",
		Workflow: "myworkflow",
		Asking: orchestrator.PendingAskDetail{
			AskID: "input-abc",
			AgentID: "main",
			Prompt:  "Please provide data",
		},
		PendingCount: 0,
		Resume:       `raymond --resume test-wf-123 --input "[your response]"`,
	}

	var out, errOut bytes.Buffer
	c := newAskingCLI(&out, &errOut, askErr)
	cmd := c.NewRootCmd()
	cmd.SetArgs([]string{startFile, "--state-dir", stateDir})
	err := cmd.Execute()

	// The error should be an PendingAskError detectable via errors.As.
	require.Error(t, err)
	var gotAsk *orchestrator.PendingAskError
	require.True(t, errors.As(err, &gotAsk), "expected *PendingAskError, got %T", err)

	assert.Equal(t, "asking", gotAsk.Status)
	assert.Equal(t, "test-wf-123", gotAsk.RunID)
	assert.Equal(t, "myworkflow", gotAsk.Workflow)
	assert.Equal(t, "input-abc", gotAsk.Asking.AskID)
	assert.Equal(t, "main", gotAsk.Asking.AgentID)
	assert.Equal(t, "Please provide data", gotAsk.Asking.Prompt)
	assert.Equal(t, 0, gotAsk.PendingCount)
}

func TestPendingAskErrorJSONIsParseable(t *testing.T) {
	// Verify that the JSON encoding of PendingAskError (using the same
	// indented NewEncoder path as Run()) is valid and contains all expected
	// fields with correct JSON keys.
	askErr := &orchestrator.PendingAskError{
		Status:   "asking",
		RunID:    "wf-42",
		Workflow: "demo",
		Asking: orchestrator.PendingAskDetail{
			AskID: "id-1",
			AgentID: "main",
			Prompt:  "Enter value",
		},
		PendingCount: 2,
		Resume:       `raymond --resume wf-42 --input "[your response]"`,
	}

	// Use the same encoder path as Run().
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	require.NoError(t, enc.Encode(askErr))

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))

	assert.Equal(t, "asking", parsed["status"])
	assert.Equal(t, "wf-42", parsed["run_id"])
	assert.Equal(t, "demo", parsed["workflow"])
	assert.Equal(t, float64(2), parsed["pending_count"])
	assert.Equal(t, `raymond --resume wf-42 --input "[your response]"`, parsed["resume"])

	asking, ok := parsed["asking"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "id-1", asking["ask_id"])
	assert.Equal(t, "main", asking["agent_id"])
	assert.Equal(t, "Enter value", asking["prompt"])
}

func TestPendingAskErrorPendingCountReflectsQueueLength(t *testing.T) {
	askErr := &orchestrator.PendingAskError{
		Status:   "asking",
		RunID:    "wf-multi",
		Workflow: "multi",
		Asking: orchestrator.PendingAskDetail{
			AskID: "id-active",
			AgentID: "alpha",
			Prompt:  "Primary",
		},
		PendingCount: 3,
		Resume:       `raymond --resume wf-multi --input "[your response]"`,
	}

	data, err := json.Marshal(askErr)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, float64(3), parsed["pending_count"])
}
