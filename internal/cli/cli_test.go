package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/cli"
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

// writeWorkflow writes a workflow state file to stateDir and returns the ID.
func writeWorkflow(t *testing.T, id, scopeDir, initialState, stateDir string) {
	t.Helper()
	ws := wfstate.CreateInitialState(id, scopeDir, initialState, 10.0, nil)
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
	_, _, err := run(t, "--status", "nonexistent", "--state-dir", stateDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStatusFound(t *testing.T) {
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-xyz", "workflows/test", "START.md", stateDir)

	out, _, err := run(t, "--status", "wf-xyz", "--state-dir", stateDir)
	require.NoError(t, err)
	assert.Contains(t, out, "wf-xyz")
	assert.Contains(t, out, "workflows/test")
	assert.Contains(t, out, "main")
	assert.Contains(t, out, "START.md")
}

func TestStatusShowsBudget(t *testing.T) {
	stateDir := makeStateDir(t)
	ws := wfstate.CreateInitialState("wf-budget", "workflows/test", "START.md", 25.0, nil)
	require.NoError(t, wfstate.WriteState("wf-budget", ws, stateDir))

	out, _, err := run(t, "--status", "wf-budget", "--state-dir", stateDir)
	require.NoError(t, err)
	assert.Contains(t, out, "25.00")
}

func TestStatusPausedAgent(t *testing.T) {
	stateDir := makeStateDir(t)
	ws := wfstate.CreateInitialState("wf-paused", "workflows/test", "START.md", 10.0, nil)
	ws.Agents[0].Status = "paused"
	ws.Agents[0].Error = "usage limit hit"
	require.NoError(t, wfstate.WriteState("wf-paused", ws, stateDir))

	out, _, err := run(t, "--status", "wf-paused", "--state-dir", stateDir)
	require.NoError(t, err)
	assert.Contains(t, out, "paused")
	assert.Contains(t, out, "usage limit hit")
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
	// Use a directory that actually exists (the stateDir itself works).
	workflowDir := t.TempDir()

	// The test runner is a no-op, so this just verifies the CLI dispatches
	// to start mode and parses the directory correctly.
	_, _, err := run(t, workflowDir, "--state-dir", stateDir)
	require.NoError(t, err)
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

	// Create a minimal fake .zip file (just needs to exist).
	dir := t.TempDir()
	zipFile := filepath.Join(dir, "workflow.zip")
	require.NoError(t, os.WriteFile(zipFile, []byte("PK"), 0o644))

	_, _, err := run(t, zipFile, "--state-dir", stateDir)
	// The no-op runner returns nil regardless of scope type.
	require.NoError(t, err)
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
