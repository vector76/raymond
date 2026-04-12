package platform_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/platform"
)

// skipUnix skips the test when not running on Unix.
func skipUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only test")
	}
}

// skipWindows skips the test when not running on Windows.
func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}
}

// writeScript creates an executable script file in dir with the given content
// and returns its path. On Unix it sets 0755 permissions.
func writeScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	return path
}

// ----------------------------------------------------------------------------
// Platform detection
// ----------------------------------------------------------------------------

func TestIsWindowsAndIsUnixMutuallyExclusive(t *testing.T) {
	assert.NotEqual(t, platform.IsWindows(), platform.IsUnix())
}

func TestIsWindowsMatchesRuntimeGOOS(t *testing.T) {
	expected := runtime.GOOS == "windows"
	assert.Equal(t, expected, platform.IsWindows())
}

func TestIsUnixMatchesRuntimeGOOS(t *testing.T) {
	expected := runtime.GOOS != "windows"
	assert.Equal(t, expected, platform.IsUnix())
}

// ----------------------------------------------------------------------------
// ScriptTimeoutError
// ----------------------------------------------------------------------------

func TestScriptTimeoutErrorMessage(t *testing.T) {
	err := &platform.ScriptTimeoutError{ScriptPath: "test.sh", Timeout: 5.0}
	msg := err.Error()
	assert.Contains(t, msg, "test.sh")
	assert.Contains(t, strings.ToLower(msg), "timeout")
	assert.Contains(t, msg, "5")
}

func TestScriptTimeoutErrorIsError(t *testing.T) {
	var err error = &platform.ScriptTimeoutError{ScriptPath: "x.sh", Timeout: 1.0}
	assert.NotNil(t, err)
}

// ----------------------------------------------------------------------------
// ScriptResult
// ----------------------------------------------------------------------------

func TestScriptResultFields(t *testing.T) {
	r := &platform.ScriptResult{Stdout: "out", Stderr: "err", ExitCode: 42}
	assert.Equal(t, "out", r.Stdout)
	assert.Equal(t, "err", r.Stderr)
	assert.Equal(t, 42, r.ExitCode)
}

// ----------------------------------------------------------------------------
// BuildScriptEnv
// ----------------------------------------------------------------------------

func TestBuildScriptEnvSetsWorkflowID(t *testing.T) {
	env := platform.BuildScriptEnv("wf-123", "main", "", nil, nil)
	assert.Equal(t, "wf-123", env["RAYMOND_WORKFLOW_ID"])
}

func TestBuildScriptEnvSetsAgentID(t *testing.T) {
	env := platform.BuildScriptEnv("wf-1", "worker_7", "", nil, nil)
	assert.Equal(t, "worker_7", env["RAYMOND_AGENT_ID"])
}

func TestBuildScriptEnvRequiredVarsPresent(t *testing.T) {
	env := platform.BuildScriptEnv("wf-abc", "agent-xyz", "", nil, nil)
	assert.Contains(t, env, "RAYMOND_WORKFLOW_ID")
	assert.Contains(t, env, "RAYMOND_AGENT_ID")
}

func TestBuildScriptEnvNoResultByDefault(t *testing.T) {
	env := platform.BuildScriptEnv("wf-1", "main", "", nil, nil)
	assert.NotContains(t, env, "RAYMOND_RESULT")
}

func TestBuildScriptEnvResultSetWhenNonNil(t *testing.T) {
	result := "Task completed"
	env := platform.BuildScriptEnv("wf-1", "main", "", &result, nil)
	assert.Equal(t, "Task completed", env["RAYMOND_RESULT"])
}

func TestBuildScriptEnvResultEmptyStringIncluded(t *testing.T) {
	result := ""
	env := platform.BuildScriptEnv("wf-1", "main", "", &result, nil)
	assert.Contains(t, env, "RAYMOND_RESULT")
	assert.Equal(t, "", env["RAYMOND_RESULT"])
}

func TestBuildScriptEnvNoResultWhenNil(t *testing.T) {
	env := platform.BuildScriptEnv("wf-1", "main", "", nil, nil)
	assert.NotContains(t, env, "RAYMOND_RESULT")
}

func TestBuildScriptEnvResultJSONPayload(t *testing.T) {
	payload := `{"status":"ok","count":42}`
	env := platform.BuildScriptEnv("wf-1", "main", "", &payload, nil)
	assert.Equal(t, payload, env["RAYMOND_RESULT"])
}

func TestBuildScriptEnvSingleForkAttribute(t *testing.T) {
	env := platform.BuildScriptEnv("wf-1", "worker", "", nil, map[string]string{"item": "task1"})
	assert.Equal(t, "task1", env["item"])
}

func TestBuildScriptEnvMultipleForkAttributes(t *testing.T) {
	attrs := map[string]string{"item": "t1", "priority": "high", "index": "3"}
	env := platform.BuildScriptEnv("wf-1", "w", "", nil, attrs)
	assert.Equal(t, "t1", env["item"])
	assert.Equal(t, "high", env["priority"])
	assert.Equal(t, "3", env["index"])
}

func TestBuildScriptEnvNoForkAttributesByDefault(t *testing.T) {
	env := platform.BuildScriptEnv("wf-1", "main", "", nil, nil)
	assert.Equal(t, map[string]string{
		"RAYMOND_WORKFLOW_ID": "wf-1",
		"RAYMOND_AGENT_ID":    "main",
		"RAYMOND_TASK_FOLDER": "",
	}, env)
}

func TestBuildScriptEnvEmptyForkAttributes(t *testing.T) {
	env := platform.BuildScriptEnv("wf-1", "main", "", nil, map[string]string{})
	assert.Equal(t, map[string]string{
		"RAYMOND_WORKFLOW_ID": "wf-1",
		"RAYMOND_AGENT_ID":    "main",
		"RAYMOND_TASK_FOLDER": "",
	}, env)
}

func TestBuildScriptEnvTaskFolderSet(t *testing.T) {
	env := platform.BuildScriptEnv("wf-1", "main", "/output/main_task1", nil, nil)
	assert.Equal(t, "/output/main_task1", env["RAYMOND_TASK_FOLDER"])
}

func TestBuildScriptEnvTaskFolderEmpty(t *testing.T) {
	env := platform.BuildScriptEnv("wf-1", "main", "", nil, nil)
	assert.Equal(t, "", env["RAYMOND_TASK_FOLDER"])
}

func TestBuildScriptEnvForkAttributesCoexistWithCoreVars(t *testing.T) {
	attrs := map[string]string{"item": "val"}
	env := platform.BuildScriptEnv("wf-1", "main", "", nil, attrs)
	assert.Equal(t, "wf-1", env["RAYMOND_WORKFLOW_ID"])
	assert.Equal(t, "main", env["RAYMOND_AGENT_ID"])
	assert.Equal(t, "val", env["item"])
}

func TestBuildScriptEnvResultAndForkAttributesTogether(t *testing.T) {
	res := "some result"
	env := platform.BuildScriptEnv("wf-1", "w1", "", &res, map[string]string{"item": "t1", "priority": "low"})
	assert.Equal(t, "wf-1", env["RAYMOND_WORKFLOW_ID"])
	assert.Equal(t, "some result", env["RAYMOND_RESULT"])
	assert.Equal(t, "t1", env["item"])
	assert.Equal(t, "low", env["priority"])
}

func TestBuildScriptEnvAllValuesAreStrings(t *testing.T) {
	res := "result"
	env := platform.BuildScriptEnv("wf-1", "main", "", &res, map[string]string{"k": "v"})
	for k, v := range env {
		assert.IsType(t, "", k)
		assert.IsType(t, "", v)
	}
}

func TestBuildScriptEnvStateVarsNotPresent(t *testing.T) {
	env := platform.BuildScriptEnv("wf-1", "main", "", nil, nil)
	assert.NotContains(t, env, "RAYMOND_STATE_DIR")
	assert.NotContains(t, env, "RAYMOND_STATE_FILE")
}

// ----------------------------------------------------------------------------
// RunScript — error cases (platform-agnostic)
// ----------------------------------------------------------------------------

func TestRunScriptMissingFileError(t *testing.T) {
	_, err := platform.RunScript(context.Background(), filepath.Join(t.TempDir(), "no_such_file.sh"), 0, nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Script not found")
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestRunScriptUnsupportedExtensionError(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "test.py", "print('hello')\n")
	_, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "unsupported")
}

func TestRunScriptTxtExtensionError(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "test.txt", "hello\n")
	_, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "unsupported")
}

// ----------------------------------------------------------------------------
// RunScript — Unix (.sh)
// ----------------------------------------------------------------------------

func TestRunScriptShCapturesStdout(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh", "#!/bin/bash\necho 'Hello from bash'\n")

	result, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "Hello from bash")
}

func TestRunScriptShMultilineStdout(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh", "#!/bin/bash\necho 'Line 1'\necho 'Line 2'\necho 'Line 3'\n")

	result, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "Line 1")
	assert.Contains(t, result.Stdout, "Line 2")
	assert.Contains(t, result.Stdout, "Line 3")
}

func TestRunScriptShCapturesStderrSeparately(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh",
		"#!/bin/bash\necho 'stdout message'\necho 'stderr message' >&2\n")

	result, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "stdout message")
	assert.Contains(t, result.Stderr, "stderr message")
	assert.NotContains(t, result.Stdout, "stderr message")
}

func TestRunScriptShExitCodeZero(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh", "#!/bin/bash\nexit 0\n")

	result, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
}

func TestRunScriptShExitCodeNonZero(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh", "#!/bin/bash\nexit 42\n")

	result, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 42, result.ExitCode)
}

func TestRunScriptShCompletesWithinTimeout(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh", "#!/bin/bash\necho 'fast'\n")

	result, err := platform.RunScript(context.Background(), script, 10.0, nil, "")
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "fast")
	assert.Equal(t, 0, result.ExitCode)
}

func TestRunScriptShTimeoutError(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh", "#!/bin/bash\nsleep 30\necho 'done'\n")

	_, err := platform.RunScript(context.Background(), script, 0.3, nil, "")
	require.Error(t, err)
	var timeoutErr *platform.ScriptTimeoutError
	assert.True(t, errors.As(err, &timeoutErr), "expected ScriptTimeoutError, got %T: %v", err, err)
}

func TestRunScriptBatRaisesOnUnix(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.bat", "@echo off\necho test\n")

	_, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.Error(t, err)
	msg := strings.ToLower(err.Error())
	assert.True(t,
		strings.Contains(msg, "unix") || strings.Contains(msg, "windows") || strings.Contains(msg, ".bat"),
		"error should mention platform or extension: %s", err.Error(),
	)
}

func TestRunScriptShReceivesWorkflowIDEnv(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh", `#!/bin/bash
echo "WORKFLOW_ID=$RAYMOND_WORKFLOW_ID"
`)
	env := platform.BuildScriptEnv("test-workflow-123", "main", "", nil, nil)
	result, err := platform.RunScript(context.Background(), script, 0, env, "")
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "WORKFLOW_ID=test-workflow-123")
}

func TestRunScriptShReceivesAgentIDEnv(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh", `#!/bin/bash
echo "AGENT_ID=$RAYMOND_AGENT_ID"
`)
	env := platform.BuildScriptEnv("wf-1", "worker_7", "", nil, nil)
	result, err := platform.RunScript(context.Background(), script, 0, env, "")
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "AGENT_ID=worker_7")
}

func TestRunScriptShReceivesRaymondResult(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh", `#!/bin/bash
echo "RESULT=$RAYMOND_RESULT"
`)
	res := "child task completed"
	env := platform.BuildScriptEnv("wf-1", "main", "", &res, nil)
	result, err := platform.RunScript(context.Background(), script, 0, env, "")
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "RESULT=child task completed")
}

func TestRunScriptShReceivesForkAttributes(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh", `#!/bin/bash
echo "ITEM=$item"
echo "PRIO=$priority"
echo "IDX=$index"
`)
	env := platform.BuildScriptEnv("wf-1", "w", "", nil,
		map[string]string{"item": "task1", "priority": "high", "index": "5"})
	result, err := platform.RunScript(context.Background(), script, 0, env, "")
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "ITEM=task1")
	assert.Contains(t, result.Stdout, "PRIO=high")
	assert.Contains(t, result.Stdout, "IDX=5")
}

func TestRunScriptShRunsInOrchestratorDirectory(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	scopeDir := filepath.Join(dir, "workflows", "test")
	require.NoError(t, os.MkdirAll(scopeDir, 0o755))
	script := writeScript(t, scopeDir, "test.sh", "#!/bin/bash\npwd\n")

	cwd, err := os.Getwd()
	require.NoError(t, err)

	result, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, cwd)
}

func TestRunScriptShWithCwdRunsInSpecifiedDirectory(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	workdir := filepath.Join(dir, "workdir")
	require.NoError(t, os.Mkdir(workdir, 0o755))
	script := writeScript(t, dir, "test.sh", "#!/bin/bash\npwd\n")

	result, err := platform.RunScript(context.Background(), script, 0, nil, workdir)
	require.NoError(t, err)
	// Resolve symlinks so /var/... vs /private/var/... don't cause false failures.
	realWorkdir, _ := filepath.EvalSymlinks(workdir)
	realStdout := strings.TrimSpace(result.Stdout)
	realOut, _ := filepath.EvalSymlinks(realStdout)
	assert.Equal(t, realWorkdir, realOut)
}

func TestRunScriptShCwdDoesNotChangeCallerDirectory(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	workdir := filepath.Join(dir, "workdir")
	require.NoError(t, os.Mkdir(workdir, 0o755))
	script := writeScript(t, dir, "test.sh", "#!/bin/bash\npwd\n")

	originalCwd, err := os.Getwd()
	require.NoError(t, err)

	_, err = platform.RunScript(context.Background(), script, 0, nil, workdir)
	require.NoError(t, err)
	assert.Equal(t, originalCwd, func() string { d, _ := os.Getwd(); return d }())
}

// ----------------------------------------------------------------------------
// RunScript — Windows (.bat)
// ----------------------------------------------------------------------------

func TestRunScriptBatCapturesStdout(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.bat", "@echo off\necho Hello from batch\n")

	result, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "Hello from batch")
}

func TestRunScriptBatExitCodeNonZero(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.bat", "@echo off\nexit /b 42\n")

	result, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 42, result.ExitCode)
}

func TestRunScriptShRaisesOnWindows(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "test.sh", "#!/bin/bash\necho test\n")

	_, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.Error(t, err)
	msg := strings.ToLower(err.Error())
	assert.True(t,
		strings.Contains(msg, "windows") || strings.Contains(msg, "unix") || strings.Contains(msg, ".sh"),
		"error should mention platform or extension: %s", err.Error(),
	)
}

// ----------------------------------------------------------------------------
// RunScript — CLAUDECODE env var stripping
// ----------------------------------------------------------------------------

// TestRunScriptStripsClaudeCodeFromEnv verifies that the CLAUDECODE environment
// variable is stripped from the child process environment even when set in the
// parent process. This prevents child processes from behaving as nested Claude
// sessions unexpectedly.
func TestRunScriptStripsClaudeCodeFromEnv(t *testing.T) {
	skipUnix(t)
	// Set CLAUDECODE in the parent environment.
	t.Setenv("CLAUDECODE", "1")

	dir := t.TempDir()
	// Print the value of CLAUDECODE; if stripped it will be empty.
	script := writeScript(t, dir, "test.sh", "#!/bin/bash\necho \"CC=${CLAUDECODE}\"\n")

	result, err := platform.RunScript(context.Background(), script, 0, nil, "")
	require.NoError(t, err)
	// CLAUDECODE must not appear in the child environment.
	assert.Equal(t, "CC=\n", result.Stdout,
		"CLAUDECODE should be stripped from the child process environment")
}
