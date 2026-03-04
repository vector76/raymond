package cli_test

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/cli"
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
	ws := wfstate.CreateInitialState("wf-zip-gone", missingZip, "START.md", 10.0, nil)
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
	ws := wfstate.CreateInitialState("wf-zip-hash-gone", missingZip, "START.md", 10.0, nil)
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

	ws := wfstate.CreateInitialState("wf-zip-hash-mismatch", zipFile, "START.md", 10.0, nil)
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

	ws := wfstate.CreateInitialState("wf-zip-bad-layout", zipFile, "START.md", 10.0, nil)
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

func TestInvalidModelFlagReturnsError(t *testing.T) {
	stateDir := makeStateDir(t)
	writeWorkflow(t, "wf-m", "workflows/test", "START.md", stateDir)

	_, _, err := run(t, "--resume", "wf-m", "--model", "gpt5", "--state-dir", stateDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gpt5")
	assert.Contains(t, err.Error(), "model")
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
	ws := wfstate.CreateInitialState("wf-restore-model", "scope", "START.md", 10.0, nil, lp)
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
	ws := wfstate.CreateInitialState("wf-override-model", "scope", "START.md", 10.0, nil, lp)
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
	ws := wfstate.CreateInitialState("wf-dsp-restore", "scope", "START.md", 10.0, nil, lp)
	require.NoError(t, wfstate.WriteState("wf-dsp-restore", ws, stateDir))

	captured, err := runCapturing(t, "--resume", "wf-dsp-restore", "--state-dir", stateDir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	assert.True(t, captured[0].DangerouslySkipPermissions,
		"resume should restore dangerously-skip-permissions=true from launch_params")
}

func TestResumeNoLaunchParamsUsesDefaults(t *testing.T) {
	stateDir := makeStateDir(t)
	// Old-style state file with no launch_params.
	ws := wfstate.CreateInitialState("wf-no-lp", "scope", "START.md", 10.0, nil)
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
