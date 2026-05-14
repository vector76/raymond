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
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
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
	completedClean, _, runErr = runWorkflowCapture(t, scopeDir, initialState, budgetUSD, nil, mutOpts...)
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
		counterFile := filepath.Join(os.TempDir(), "reset_counter.txt")
		_ = os.Remove(counterFile)
		t.Cleanup(func() { _ = os.Remove(counterFile) })
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
		counterFile := filepath.Join(os.TempDir(), "reset_counter.txt")
		_ = os.Remove(counterFile)
		t.Cleanup(func() { _ = os.Remove(counterFile) })
	}

	stateDir := t.TempDir()
	tc := testCasesDir()
	id := "integration-crash-wf"

	ws := wfstate.CreateInitialState(id, tc, scriptName, 10.0, nil, "")
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

	zipPath := filepath.Join(t.TempDir(), "script_zip.zip")
	require.NoError(t, buildZipFromFiles(testCasesDir(), zipPath, []string{
		"SCRIPT_RESULT.sh",
	}))

	completed, err := runWorkflow(t, zipPath, "SCRIPT_RESULT.sh", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "ZIP-scope SCRIPT_RESULT workflow should complete cleanly")
}

// --------------------------------------------------------------------------
// Build verification (no Claude required)
// --------------------------------------------------------------------------

// TestRayBinaryBuilds verifies that the ray binary compiles successfully
// from its cmd package.
func TestRayBinaryBuilds(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	tmpDir := t.TempDir()

	binName := "ray"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(tmpDir, binName)

	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/ray")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build ./cmd/ray failed:\n%s", string(out))

	info, statErr := os.Stat(binPath)
	require.NoError(t, statErr, "ray binary should exist after build")
	assert.Greater(t, info.Size(), int64(0), "ray binary should be non-empty")
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

	pollFile := filepath.Join(os.TempDir(), "poll_counter.txt")
	_ = os.Remove(pollFile)
	t.Cleanup(func() { _ = os.Remove(pollFile) })

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

	completed, err := runWorkflow(t, tc, "1_START.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "3-step story workflow (START→CONFLICT→RESOLUTION) should complete")
}

// TestCallReturnWorkflow verifies the call/return pattern: MAIN calls RESEARCH
// (which returns a result payload), then SUMMARIZE receives the payload via
// {{input}} substitution and terminates.
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

	zipPath := filepath.Join(t.TempDir(), "story_zip.zip")
	require.NoError(t, buildZipFromFiles(tc, zipPath, []string{
		"1_START.md",
		"CONFLICT.md",
		"RESOLUTION.md",
	}))

	completed, err := runWorkflow(t, zipPath, "1_START.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "ZIP-scope story workflow should complete")
}

// --------------------------------------------------------------------------
// Cross-workflow integration tests (require claude CLI)
// --------------------------------------------------------------------------

// TestCallWorkflowRoundTrip verifies the call-workflow round trip: the caller
// emits <call-workflow> to invoke a sub-workflow, which returns a result, and
// the caller resumes at the return state with {{input}} populated.
func TestCallWorkflowRoundTrip(t *testing.T) {
	skipIfNoClaude(t)

	scopeDir := filepath.Join(testCasesDir(), "cross_workflow_call")
	completed, err := runWorkflow(t, scopeDir, "1_START.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "call-workflow round-trip should complete cleanly")
}

// TestFunctionWorkflowWithCwd verifies the function-workflow transition: the
// sub-workflow runs with the caller-specified cwd (/tmp) and the caller resumes
// at the return state after the sub-workflow completes.
func TestFunctionWorkflowWithCwd(t *testing.T) {
	skipIfNoClaude(t)

	scopeDir := filepath.Join(testCasesDir(), "cross_workflow_function")
	completed, err := runWorkflow(t, scopeDir, "1_START.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "function-workflow with cwd=/tmp should complete cleanly")
}

// TestForkWorkflowParallel verifies that two fork-workflow tags spawn two
// independent worker agents and the caller advances to the join state after
// all workers complete.
func TestForkWorkflowParallel(t *testing.T) {
	skipIfNoClaude(t)

	scopeDir := filepath.Join(testCasesDir(), "cross_workflow_fork")
	completed, err := runWorkflow(t, scopeDir, "1_START.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "fork-workflow parallel dispatch should complete cleanly")
}

// TestCallWorkflowExplicitEntry verifies that call-workflow can target a
// non-default entry state in a sub-workflow using the explicit entry specifier.
func TestCallWorkflowExplicitEntry(t *testing.T) {
	skipIfNoClaude(t)

	scopeDir := filepath.Join(testCasesDir(), "cross_workflow_entry_call")
	completed, err := runWorkflow(t, scopeDir, "1_START.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "call-workflow explicit entry should complete cleanly")
}

// TestForkWorkflowExplicitEntry verifies that fork-workflow can target a
// non-default entry state in a sub-workflow using the explicit entry specifier.
func TestForkWorkflowExplicitEntry(t *testing.T) {
	skipIfNoClaude(t)

	scopeDir := filepath.Join(testCasesDir(), "cross_workflow_entry_fork")
	completed, err := runWorkflow(t, scopeDir, "1_START.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "fork-workflow explicit entry should complete cleanly")
}

// TestFunctionWorkflowExplicitEntry verifies that function-workflow can target
// a non-default entry state in a sub-workflow using the explicit entry specifier.
func TestFunctionWorkflowExplicitEntry(t *testing.T) {
	skipIfNoClaude(t)

	scopeDir := filepath.Join(testCasesDir(), "cross_workflow_entry_function")
	completed, err := runWorkflow(t, scopeDir, "1_START.md", 10.0)
	require.NoError(t, err)
	assert.True(t, completed, "function-workflow explicit entry should complete cleanly")
}

// TestCallWorkflowZipExplicitEntry verifies that call-workflow can target a
// non-default entry state inside a ZIP-scoped sub-workflow.
//
// The sub-workflow's two entry-point fixtures (1_START.md, OTHER_ENTRY.md) use
// the implicit-transition mechanism: their frontmatter declares a single
// allowed `result` tag with a fixed, distinct payload. The LLM is asked an
// unrelated trivial question and emits no transition tag; the markdown
// executor synthesizes the result from frontmatter. This makes the test
// deterministic with respect to LLM phrasing while still exercising the
// markdown executor codepath inside a zip scope.
//
// The caller side is fully script-driven for determinism: 1_START.sh emits
// the `<call-workflow>` tag, and 2_DONE.sh re-emits RAYMOND_INPUT as its own
// <result>. The workflow's terminal payload therefore equals whatever the
// sub-workflow returned, which lets us assert the explicit entry (OTHER_ENTRY)
// ran rather than the default (1_START).
func TestCallWorkflowZipExplicitEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses .sh caller/return states; skipping on Windows")
	}
	skipIfNoClaude(t)

	// Build zip from the source fixture directory.
	tmpZipPath := filepath.Join(t.TempDir(), "cross_wf_zip.zip")
	err := buildZipFromDir(filepath.Join(testCasesDir(), "cross_workflow_zip_entry_sub_src"), tmpZipPath)
	require.NoError(t, err)

	// Build a caller dir with script states wrapping a markdown sub-workflow:
	// 1_START.sh emits a <call-workflow> tag pointing into the zip at
	// OTHER_ENTRY; 2_DONE.sh re-emits the returned RAYMOND_INPUT so we can
	// observe which sub-workflow entry actually ran.
	callerDir := t.TempDir()
	startScript := fmt.Sprintf(
		"#!/bin/bash\necho '<call-workflow return=\"2_DONE.sh\">%s/OTHER_ENTRY</call-workflow>'\n",
		tmpZipPath,
	)
	require.NoError(t, os.WriteFile(filepath.Join(callerDir, "1_START.sh"), []byte(startScript), 0o755))
	doneScript := "#!/bin/bash\necho \"<result>$RAYMOND_INPUT</result>\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(callerDir, "2_DONE.sh"), []byte(doneScript), 0o755))

	completed, result, err := runWorkflowCapture(t, callerDir, "1_START.sh", 10.0, nil)
	require.NoError(t, err)
	assert.True(t, completed, "call-workflow zip explicit entry should complete cleanly")
	assert.Equal(t, "explicit_zip_entry_used", result,
		"explicit entry OTHER_ENTRY should have run (not default 1_START)")
}

// --------------------------------------------------------------------------
// reset-workflow integration tests (no Claude required)
// --------------------------------------------------------------------------

// runWorkflowCapture is like runWorkflow but also returns the ResultPayload
// from the terminating AgentTerminated event. initialInput, when non-nil, is
// set as the agent's PendingResult before the workflow starts.
func runWorkflowCapture(
	t *testing.T,
	scopeDir, initialState string,
	budgetUSD float64,
	initialInput *string,
	mutOpts ...func(*orchestrator.RunOptions),
) (completedClean bool, resultPayload string, runErr error) {
	t.Helper()

	stateDir := t.TempDir()
	id := "integration-wf"
	ws := wfstate.CreateInitialState(id, scopeDir, initialState, budgetUSD, initialInput, "")
	require.NoError(t, wfstate.WriteState(id, ws, stateDir))

	var captured string
	opts := orchestrator.RunOptions{
		StateDir: stateDir,
		Quiet:    true,
		Timeout:  120.0,
		NoWait:   true,
		ObserverSetup: func(b *bus.Bus) {
			bus.Subscribe(b, func(ev events.AgentTerminated) {
				captured = ev.ResultPayload
			})
		},
	}
	for _, m := range mutOpts {
		m(&opts)
	}

	runErr = orchestrator.RunAllAgents(context.Background(), id, opts)

	_, statErr := wfstate.ReadState(id, stateDir)
	completedClean = statErr != nil
	return completedClean, captured, runErr
}

// buildZipWithHash builds a ZIP from srcDir, computes its SHA256, and renames
// it to destDir/<hash>.zip. Returns the path to the created zip file.
func buildZipWithHash(t *testing.T, srcDir, destDir string) string {
	t.Helper()

	tmpPath := filepath.Join(t.TempDir(), "temp.zip")
	require.NoError(t, buildZipFromDir(srcDir, tmpPath))

	// Stream the zip through sha256 without loading it fully into memory.
	f, err := os.Open(tmpPath)
	require.NoError(t, err)
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	require.NoError(t, err)

	hashStr := hex.EncodeToString(h.Sum(nil))
	finalPath := filepath.Join(destDir, hashStr+".zip")
	require.NoError(t, os.Rename(tmpPath, finalPath))
	return finalPath
}

// TestResetWorkflowBasic verifies that a script can emit <reset-workflow> to
// transfer control to an external workflow directory, and that the target runs
// to completion.
func TestResetWorkflowBasic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("reset-workflow tests use .sh scripts; skipping on Windows")
	}

	scopeDir := filepath.Join(testCasesDir(), "reset_workflow_basic")
	completed, result, err := runWorkflowCapture(t, scopeDir, "1_START.sh", 10.0, nil)
	require.NoError(t, err)
	assert.True(t, completed, "basic reset-workflow should complete cleanly")
	assert.Equal(t, "reached_target", result, "target workflow should produce 'reached_target'")
}

// TestResetWorkflowInputForwarded verifies that the input attribute on
// <reset-workflow> is forwarded as RAYMOND_INPUT to the target workflow's
// first state.
func TestResetWorkflowInputForwarded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("reset-workflow tests use .sh scripts; skipping on Windows")
	}

	scopeDir := filepath.Join(testCasesDir(), "reset_workflow_input")
	completed, result, err := runWorkflowCapture(t, scopeDir, "1_START.sh", 10.0, nil)
	require.NoError(t, err)
	assert.True(t, completed, "input-forwarding reset-workflow should complete cleanly")
	assert.Equal(t, "hello_from_caller", result, "input attribute should be forwarded as RAYMOND_INPUT")
}

// TestResetWorkflowCdApplied verifies that the cd attribute on <reset-workflow>
// sets the working directory for the target workflow's execution.
func TestResetWorkflowCdApplied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("reset-workflow tests use .sh scripts; skipping on Windows")
	}

	scopeDir := filepath.Join(testCasesDir(), "reset_workflow_cd")
	completed, result, err := runWorkflowCapture(t, scopeDir, "1_START.sh", 10.0, nil)
	require.NoError(t, err)
	assert.True(t, completed, "cd-attribute reset-workflow should complete cleanly")
	assert.Equal(t, "/tmp", result, "cd attribute should set working directory to /tmp")
}

// TestResetWorkflowStackCleared verifies that <reset-workflow> clears the
// call stack. The outer workflow calls the inner workflow (pushing a return
// frame), but the inner workflow does a reset-workflow. The target's
// <result>done</result> should terminate the workflow directly (not resume
// at outer's DONE.sh, which would produce "outer_sentinel").
func TestResetWorkflowStackCleared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("reset-workflow tests use .sh scripts; skipping on Windows")
	}

	scopeDir := filepath.Join(testCasesDir(), "reset_workflow_stack_cleared_outer")
	completed, result, err := runWorkflowCapture(t, scopeDir, "1_START.sh", 10.0, nil)
	require.NoError(t, err)
	assert.True(t, completed, "stack-cleared reset-workflow should complete cleanly")
	assert.Equal(t, "done", result, "stack should be cleared; result must be 'done', not 'outer_sentinel'")
}

// TestResetWorkflowZipTarget verifies that <reset-workflow> can target a ZIP
// archive and that hash validation passes when the zip filename encodes its
// SHA256. The target zip's entry state runs and produces "zip_ok".
func TestResetWorkflowZipTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("reset-workflow tests use .sh scripts; skipping on Windows")
	}

	zipPath := buildZipWithHash(t,
		filepath.Join(testCasesDir(), "reset_workflow_zip_src"),
		t.TempDir(),
	)

	callerDir := filepath.Join(testCasesDir(), "reset_workflow_zip_caller")
	completed, result, err := runWorkflowCapture(t, callerDir, "1_START.sh", 10.0, &zipPath)
	require.NoError(t, err)
	assert.True(t, completed, "zip-target reset-workflow should complete cleanly")
	assert.Equal(t, "zip_ok", result, "zip target entry state should produce 'zip_ok'")
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

// buildZipFromFiles creates a flat ZIP archive at dst containing exactly the
// listed top-level files from src. This is used by ZIP-scope tests that need
// to zip a curated subset of workflows/test_cases/ — the directory contains a
// mix of top-level state files and subdirectories that the zipscope layout
// validator (correctly) rejects, so tests that conceptually want only a few
// flat states must spell them out.
func buildZipFromFiles(src, dst string, files []string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return err
		}
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return nil
}

// --------------------------------------------------------------------------
// ray serve graceful-shutdown end-to-end test
// --------------------------------------------------------------------------

// syncBuffer is a concurrent-safe wrapper around bytes.Buffer. exec.Cmd's
// stdout/stderr copier goroutines write into the buffer in parallel with
// the test goroutine reading it for diagnostics, which is a data race
// against a plain bytes.Buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// --------------------------------------------------------------------------
// Shared helpers for the shutdown end-to-end tests below.
// --------------------------------------------------------------------------

// buildRayBinary compiles ./cmd/ray into a fresh temp dir and returns the
// path. Each shutdown test calls this once so the subprocess they spawn
// reflects the current working tree.
func buildRayBinary(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "ray")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/ray")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build ./cmd/ray failed:\n%s", out)
	return binPath
}

// pickFreePort binds 127.0.0.1:0, reads the assigned port, and closes the
// socket. The window between Close() and the daemon's bind is a TOCTOU race
// but small enough in practice to be fine for these tests.
func pickFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// serveProc is a running `ray serve` subprocess plus its captured streams.
// Tests own its lifecycle: send the eventual stop signal and call waitForExit.
// The t.Cleanup registered by startServeDaemon is a defensive SIGKILL for
// tests that fail before reaching their reap step.
type serveProc struct {
	cmd        *exec.Cmd
	stdout     *syncBuffer
	stderr     *syncBuffer
	baseURL    string
	httpClient *http.Client
	workflowID string
	tempRoot   string
}

func (p *serveProc) diagnostics() string {
	return fmt.Sprintf("\n-- daemon stdout --\n%s\n-- daemon stderr --\n%s",
		p.stdout.String(), p.stderr.String())
}

// startServeDaemon launches `ray serve --root <workflowsDir> --port <free>`
// with cwd = tempRoot (so the subprocess's FindRaymondDir resolves to
// tempRoot/.raymond), waits for the HTTP endpoint to come up, and waits for
// workflowID to appear in `/workflows`. Verifying discovery (not just port
// readiness) catches the silent-fail case where tryIndexDir swallows a
// manifest parse error.
func startServeDaemon(t *testing.T, rayBin, tempRoot, workflowsDir, workflowID string) *serveProc {
	t.Helper()
	port := pickFreePort(t)
	cmd := exec.Command(rayBin, "serve",
		"--root", workflowsDir,
		"--port", fmt.Sprintf("%d", port),
	)
	cmd.Dir = tempRoot
	var stdout, stderr syncBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// WaitDelay belts-and-braces in case a copier goroutine hangs on the
	// pipe after the process exits — keeps Wait() bounded.
	cmd.WaitDelay = 5 * time.Second
	require.NoError(t, cmd.Start())

	p := &serveProc{
		cmd:        cmd,
		stdout:     &stdout,
		stderr:     &stderr,
		baseURL:    fmt.Sprintf("http://127.0.0.1:%d", port),
		httpClient: &http.Client{Timeout: 2 * time.Second},
		workflowID: workflowID,
		tempRoot:   tempRoot,
	}

	// Defensive cleanup: if the test fails before reaping the process, kill
	// it so the subprocess doesn't outlive the test run.
	t.Cleanup(func() {
		if p.cmd.ProcessState == nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(syscall.SIGKILL)
			_, _ = p.cmd.Process.Wait()
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	var lastBody []byte
	want := []byte(fmt.Sprintf(`"id":%q`, workflowID))
	for {
		resp, err := p.httpClient.Get(p.baseURL + "/workflows")
		if err == nil && resp.StatusCode == http.StatusOK {
			lastBody, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if bytes.Contains(lastBody, want) {
				return p
			}
		} else if resp != nil {
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not start listening or discover %q within 10s; last /workflows body: %s%s",
				workflowID, lastBody, p.diagnostics())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// createRun POSTs /runs and returns the run id, failing the test on any
// non-201 response. Workflows used by these tests all declare `input.mode:
// none`, so the body carries only the workflow id.
func (p *serveProc) createRun(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"workflow_id": p.workflowID})
	require.NoError(t, err)
	resp, err := p.httpClient.Post(p.baseURL+"/runs", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"POST /runs returned %d: %s%s", resp.StatusCode, respBody, p.diagnostics())
	var created struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(respBody, &created))
	require.NotEmpty(t, created.RunID, "POST /runs did not return a run_id")
	return created.RunID
}

// waitForFile polls path until it exists or timeout elapses.
func waitForFile(t *testing.T, path string, timeout time.Duration, diag func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s never appeared within %s%s", path, timeout, diag())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForExit blocks until the daemon process exits or timeout elapses; on
// timeout the process is SIGKILLed and reaped so the test does not leak it.
// Returns the elapsed wait time, exit code (negative on timeout), and a
// timed-out flag.
func (p *serveProc) waitForExit(t *testing.T, timeout time.Duration) (waited time.Duration, exitCode int, timedOut bool) {
	t.Helper()
	start := time.Now()
	waitCh := make(chan error, 1)
	go func() { waitCh <- p.cmd.Wait() }()
	select {
	case waitErr := <-waitCh:
		if waitErr != nil {
			if _, ok := waitErr.(*exec.ExitError); !ok {
				t.Fatalf("unexpected wait error: %v%s", waitErr, p.diagnostics())
			}
		}
		return time.Since(start), p.cmd.ProcessState.ExitCode(), false
	case <-time.After(timeout):
		_ = p.cmd.Process.Signal(syscall.SIGKILL)
		<-waitCh
		return time.Since(start), -1, true
	}
}

// shutdownEvent is a parsed SSE frame from GET /events. Only the fields these
// tests consult are extracted; the marshalSSEEvent envelope's `type` key
// distinguishes shutdown_requested from shutdown_complete, and Outcomes is
// populated only on the latter.
type shutdownEvent struct {
	Type     string            `json:"type"`
	Outcomes map[string]string `json:"outcomes"`
}

// subscribeShutdownEvents opens an SSE connection to GET /events and returns
// a channel emitting decoded frames until the connection closes. Cancel
// tears the stream down. The stream's transport carries no read deadline:
// these tests need to observe events that arrive seconds apart, with the
// final shutdown_complete frame arriving just before the daemon exits.
func subscribeShutdownEvents(t *testing.T, baseURL string) (<-chan shutdownEvent, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/events", nil)
	require.NoError(t, err)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET /events: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("GET /events status = %d (want 200)", resp.StatusCode)
	}

	out := make(chan shutdownEvent, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			var ev shutdownEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, func() {
		cancel()
		_ = resp.Body.Close()
	}
}

// awaitShutdownEvent reads from ch until it observes a frame whose `type`
// matches one of wantTypes and returns it. Fails the test if the stream
// closes or timeout elapses first.
func awaitShutdownEvent(t *testing.T, ch <-chan shutdownEvent, timeout time.Duration, diag func() string, wantTypes ...string) shutdownEvent {
	t.Helper()
	want := make(map[string]struct{}, len(wantTypes))
	for _, w := range wantTypes {
		want[w] = struct{}{}
	}
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("event stream closed before observing %v%s", wantTypes, diag())
			}
			if _, hit := want[ev.Type]; hit {
				return ev
			}
		case <-deadline:
			t.Fatalf("never received %v within %s%s", wantTypes, timeout, diag())
		}
	}
}

// writeShutdownConfig drops a minimal config.toml at raymondDir/config.toml.
// Kept as a helper so each test reads the same pinned shape — empty
// [raymond] and [raymond.serve] sections — without copying the literal.
func writeShutdownConfig(t *testing.T, raymondDir string) {
	t.Helper()
	configToml := strings.Join([]string{
		"[raymond]",
		"",
		"[raymond.serve]",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(raymondDir, "config.toml"), []byte(configToml), 0o644))
}

// writeShutdownManifest writes a workflow.yaml with `input.mode: none` so
// POST /runs needs no body fields beyond workflow_id.
func writeShutdownManifest(t *testing.T, wfDir, id, description string) {
	t.Helper()
	manifestYAML := strings.Join([]string{
		"id: " + id,
		"name: Shutdown integration test",
		"description: " + description,
		"input:",
		"  mode: none",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "workflow.yaml"), []byte(manifestYAML), 0o644))
}

// --------------------------------------------------------------------------
// ray serve graceful-shutdown end-to-end tests.
//
// Each test below spawns a real `ray serve` subprocess, drives it through one
// path of the two-phase shutdown contract (see docs/graceful-shutdown.md),
// and inspects the published outcomes (SSE /events stream) and on-disk
// residue (state files, absence of the deleted sentinel file).
// --------------------------------------------------------------------------

// TestServeSIGTERMFastCancel drives the SIGTERM path of the two-phase
// shutdown contract end-to-end. The daemon is signalled while a long-sleeping
// shell step is in flight; the contract is "go straight to cancel, no
// quiesce attempt, exit inside the patience window" (graceful-shutdown.md
// §Signal mapping).
//
// Assertions:
//
//   - the daemon exits within 8 seconds of SIGTERM (5s patience + slack);
//   - the SSE /events stream emits a shutdown_complete frame whose Outcomes
//     map classifies the run as "cancelled";
//   - the run's state file (in the serve pool) records the agent as still
//     parked at the script state it was killed in — the cancel-path
//     contract of "agent in STATE_X" rather than at a next-state boundary;
//   - restarting `ray serve` against the same pool auto-recovers the run;
//     the marker-aware script short-circuits to <result> and the
//     orchestrator deletes the state file.
//
// The marker-flip approach keeps the recovery side deterministic without
// requiring a second workflow definition: the same `1_START.sh` short-
// circuits to a <result> when it observes the marker.
func TestServeSIGTERMFastCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM-driven shutdown semantics differ on Windows; POSIX-only test")
	}

	tempRoot := t.TempDir()
	workflowsDir := filepath.Join(tempRoot, "workflows")
	wfDir := filepath.Join(workflowsDir, "test-shutdown")
	raymondDir := filepath.Join(tempRoot, ".raymond")
	serveStateDir := filepath.Join(raymondDir, "serve-state")
	cliStateDir := filepath.Join(raymondDir, "state")
	marker := filepath.Join(tempRoot, "marker.txt")

	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	require.NoError(t, os.MkdirAll(raymondDir, 0o755))
	writeShutdownConfig(t, raymondDir)
	writeShutdownManifest(t, wfDir, "test-shutdown",
		"Sleeps in a loop until the daemon shuts down.")

	// The script touches the marker file *before* sleeping so the test
	// can poll for the marker to know the run has reached in-progress.
	// The marker doubles as the resume short-circuit: on the second
	// invocation the marker exists, so the script produces a terminal
	// <result> immediately without sleeping.
	script := fmt.Sprintf(`#!/bin/bash
MARKER=%q
if [ -f "$MARKER" ]; then
  echo '<result>resumed_ok</result>'
  exit 0
fi
touch "$MARKER"
sleep 60
echo '<goto>1_START.sh</goto>'
`, marker)
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "1_START.sh"), []byte(script), 0o755))

	rayBin := buildRayBinary(t)
	p := startServeDaemon(t, rayBin, tempRoot, workflowsDir, "test-shutdown")
	events, eventsCancel := subscribeShutdownEvents(t, p.baseURL)
	defer eventsCancel()

	runID := p.createRun(t)
	waitForFile(t, marker, 10*time.Second, p.diagnostics)

	// SIGTERM goes straight to cancel — no quiesce attempt. The contract
	// (docs/graceful-shutdown.md §Cancel's patience window) is 5s patience
	// for goroutines to honour ctx.Done(); 8s is the bead-spec upper bound
	// on the daemon's wall-clock from signal to process exit (5s patience
	// + generous slack).
	signalSentAt := time.Now()
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))

	complete := awaitShutdownEvent(t, events, 8*time.Second, p.diagnostics, "shutdown_complete")
	assert.Equal(t, "cancelled", complete.Outcomes[runID],
		"SIGTERM should classify the in-flight run as cancelled, got %q%s",
		complete.Outcomes[runID], p.diagnostics())

	// Release our SSE subscription before waiting for exit: the daemon's
	// deferred srv.Shutdown blocks on active HTTP handlers, and the only
	// one in flight is the /events stream this test owns. Closing it lets
	// the daemon's exit path complete promptly so the 8s bead-spec bound
	// is observable rather than dominated by srv.Shutdown's 5s grace.
	eventsCancel()

	waited, exitCode, timedOut := p.waitForExit(t, 8*time.Second)
	require.False(t, timedOut,
		"daemon did not exit within 8s of SIGTERM (bead spec); exit took %s%s",
		waited, p.diagnostics())
	require.Equal(t, 0, exitCode,
		"daemon exit code = %d (want 0); exit took %s%s", exitCode, waited, p.diagnostics())
	assert.LessOrEqual(t, time.Since(signalSentAt), 8*time.Second,
		"end-to-end daemon-exit wall-clock should fit the 8s bound%s", p.diagnostics())

	// State file survives in the serve pool, recording the cancel-path
	// "agent in STATE_X" position (1_START.sh) rather than a next-state
	// boundary that a quiesce would have produced.
	statePath := filepath.Join(serveStateDir, runID+".json")
	info, err := os.Stat(statePath)
	require.NoError(t, err,
		"state file %q must persist across cancel%s", statePath, p.diagnostics())
	require.Greater(t, info.Size(), int64(0), "state file should be non-empty")

	ws, err := wfstate.ReadState(runID, serveStateDir)
	require.NoError(t, err, "state file must be parseable JSON")
	require.NotEmpty(t, ws.Agents,
		"state file must record at least one agent so recovery has a transition target to pick up")
	assert.Equal(t, "1_START.sh", ws.Agents[0].CurrentState,
		"the agent should still be parked at the script state it was killed in (not a next-state boundary)")

	// Disjoint-pool invariant: a daemon-only flow must not touch the CLI
	// pool. The directory may not even exist; either is fine.
	if entries, statErr := os.ReadDir(cliStateDir); statErr == nil {
		assert.Empty(t, entries,
			"CLI pool %s should not be touched by a daemon-only run", cliStateDir)
	} else {
		assert.True(t, os.IsNotExist(statErr),
			"unexpected error reading CLI pool: %v", statErr)
	}

	// Auto-resume on next `ray serve` startup: the marker-aware script
	// short-circuits to <result>, so the orchestrator deletes the state
	// file. Completion is driven by restarting the daemon, not by a
	// per-run resume call (disjoint-pool model).
	p2 := startServeDaemon(t, rayBin, tempRoot, workflowsDir, "test-shutdown")

	completionDeadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(statePath); os.IsNotExist(err) {
			break
		}
		if time.Now().After(completionDeadline) {
			t.Fatalf("recovered run did not complete within 30s; state file %s still present%s%s",
				statePath, p.diagnostics(), p2.diagnostics())
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Tidy: shut the second daemon down. Failure here is informational —
	// the auto-resume assertion above is the real point of this step.
	_ = p2.cmd.Process.Signal(syscall.SIGTERM)
	_, _, _ = p2.waitForExit(t, 15*time.Second)
}

// TestServeSIGINTQuiesce drives the 1st-SIGINT path: BeginQuiesce engages,
// and the daemon stays alive until the in-flight run naturally reaches a
// terminal state (the run's "next state transition" in the contract is the
// terminal <result>; either form of natural draining counts as quiesced).
//
// The script writes a marker, sleeps a short, deterministic interval (long
// enough for the test goroutine to send SIGINT while the shell is sleeping),
// then emits <result>. Because EscalateToCancel is never engaged, the
// coordinator classifies the run as quiesced.
func TestServeSIGINTQuiesce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT-driven shutdown semantics differ on Windows; POSIX-only test")
	}

	tempRoot := t.TempDir()
	workflowsDir := filepath.Join(tempRoot, "workflows")
	wfDir := filepath.Join(workflowsDir, "test-quiesce")
	raymondDir := filepath.Join(tempRoot, ".raymond")
	marker := filepath.Join(tempRoot, "marker.txt")

	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	require.NoError(t, os.MkdirAll(raymondDir, 0o755))
	writeShutdownConfig(t, raymondDir)
	writeShutdownManifest(t, wfDir, "test-quiesce",
		"Sleeps briefly then completes naturally; used to exercise quiesce.")

	// Sleep 3s is long enough that the test reliably sees the script
	// in-flight (so SIGINT lands while quiesce has work to do) but short
	// enough that the test wall-clock stays well under any CI budget.
	script := fmt.Sprintf(`#!/bin/bash
touch %q
sleep 3
echo '<result>quiesced_ok</result>'
`, marker)
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "1_START.sh"), []byte(script), 0o755))

	rayBin := buildRayBinary(t)
	p := startServeDaemon(t, rayBin, tempRoot, workflowsDir, "test-quiesce")
	events, eventsCancel := subscribeShutdownEvents(t, p.baseURL)
	defer eventsCancel()

	runID := p.createRun(t)
	waitForFile(t, marker, 10*time.Second, p.diagnostics)

	// Single SIGINT — the daemon enters quiesce and waits without any
	// raymond-imposed timeout for the run to drain naturally.
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGINT))

	// shutdown_requested fires synchronously inside BeginQuiesce, so it
	// should land almost immediately. The wide bound is just slack for
	// SSE flush.
	awaitShutdownEvent(t, events, 5*time.Second, p.diagnostics, "shutdown_requested")

	// The script sleeps ~3s past SIGINT. Allow generous slack for daemon
	// teardown after the run drains.
	complete := awaitShutdownEvent(t, events, 15*time.Second, p.diagnostics, "shutdown_complete")
	assert.Equal(t, "quiesced", complete.Outcomes[runID],
		"a single SIGINT followed by natural drain should classify the run as quiesced, got %q%s",
		complete.Outcomes[runID], p.diagnostics())

	// Release the SSE subscription so srv.Shutdown's 5s grace doesn't stall
	// on our own connection; see TestServeSIGTERMFastCancel's comment.
	eventsCancel()

	waited, exitCode, timedOut := p.waitForExit(t, 10*time.Second)
	require.False(t, timedOut, "daemon did not exit within 10s after natural drain%s", p.diagnostics())
	require.Equal(t, 0, exitCode,
		"daemon exit code = %d (want 0); exit took %s%s", exitCode, waited, p.diagnostics())
}

// TestServeSIGINTEscalation drives the 2nd-SIGINT path: a first SIGINT
// engages quiesce, and a second SIGINT arriving while quiesce is still in
// flight escalates to cancel. The test waits for the shutdown_requested
// event (the contractual InProgress proxy) before sending the second
// signal, then asserts the daemon exits inside the patience window with
// the run classified cancelled.
func TestServeSIGINTEscalation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT-driven shutdown semantics differ on Windows; POSIX-only test")
	}

	tempRoot := t.TempDir()
	workflowsDir := filepath.Join(tempRoot, "workflows")
	wfDir := filepath.Join(workflowsDir, "test-escalate")
	raymondDir := filepath.Join(tempRoot, ".raymond")
	marker := filepath.Join(tempRoot, "marker.txt")

	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	require.NoError(t, os.MkdirAll(raymondDir, 0o755))
	writeShutdownConfig(t, raymondDir)
	writeShutdownManifest(t, wfDir, "test-escalate",
		"Long-sleeping workflow; only the second SIGINT brings it down.")

	// 120s sleep — well past any expected test wall-clock so the only way
	// the daemon exits is via the escalated cancel.
	script := fmt.Sprintf(`#!/bin/bash
touch %q
sleep 120
echo '<goto>1_START.sh</goto>'
`, marker)
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "1_START.sh"), []byte(script), 0o755))

	rayBin := buildRayBinary(t)
	p := startServeDaemon(t, rayBin, tempRoot, workflowsDir, "test-escalate")
	events, eventsCancel := subscribeShutdownEvents(t, p.baseURL)
	defer eventsCancel()

	runID := p.createRun(t)
	waitForFile(t, marker, 10*time.Second, p.diagnostics)

	// 1st SIGINT — enter quiesce. The shutdown_requested event proves the
	// coordinator has actually engaged before we escalate.
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGINT))
	awaitShutdownEvent(t, events, 5*time.Second, p.diagnostics, "shutdown_requested")

	// 2nd SIGINT — escalate to cancel. Track when we sent it so the
	// cancel-side wall-clock assertion is anchored to the escalation
	// moment, not the original quiesce start.
	escalateAt := time.Now()
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGINT))

	complete := awaitShutdownEvent(t, events, 8*time.Second, p.diagnostics, "shutdown_complete")
	assert.Equal(t, "cancelled", complete.Outcomes[runID],
		"a run that never drained between the two SIGINTs should be classified cancelled, got %q%s",
		complete.Outcomes[runID], p.diagnostics())

	// Release the SSE subscription before waiting for exit (see the
	// TestServeSIGTERMFastCancel comment for why this matters): srv.Shutdown
	// otherwise stalls on this very stream for its 5s grace period.
	eventsCancel()

	waited, exitCode, timedOut := p.waitForExit(t, 8*time.Second)
	require.False(t, timedOut,
		"daemon did not exit within 8s of second SIGINT (bead spec: patience window); exit took %s%s",
		waited, p.diagnostics())
	require.Equal(t, 0, exitCode,
		"daemon exit code = %d (want 0); exit took %s%s", exitCode, waited, p.diagnostics())
	assert.LessOrEqual(t, time.Since(escalateAt), 8*time.Second,
		"end-to-end daemon-exit wall-clock after escalation should fit the 8s patience-window bound%s",
		p.diagnostics())
}

// TestServeShutdownEnvAbsence is the subprocess-level regression guard for
// the deleted T1 env-injection mechanism (RAYMOND_STOP_REQUESTED /
// RAYMOND_STOP_SENTINEL). The script captures its env to a file before
// sleeping; the test triggers shutdown while it is sleeping and asserts
// neither variable appears in the captured environment.
//
// Distinct from the per-package env-absence test
// (internal/executors/script_shutdown_env_test.go), which captures the
// executor's env-build path without running a real subprocess.
func TestServeShutdownEnvAbsence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("subprocess env capture uses /bin/sh idioms; POSIX-only test")
	}

	tempRoot := t.TempDir()
	workflowsDir := filepath.Join(tempRoot, "workflows")
	wfDir := filepath.Join(workflowsDir, "test-env-absence")
	raymondDir := filepath.Join(tempRoot, ".raymond")
	envFile := filepath.Join(tempRoot, "env.txt")
	captured := filepath.Join(tempRoot, "captured.marker")

	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	require.NoError(t, os.MkdirAll(raymondDir, 0o755))
	writeShutdownConfig(t, raymondDir)
	writeShutdownManifest(t, wfDir, "test-env-absence",
		"Captures its env and then sleeps until the daemon shuts down.")

	// Dump env to the file *before* sleeping so the captured env reflects
	// the live script's environment (rather than the env at exit, which
	// nothing observes anyway). The `captured` marker is touched *after*
	// `env` has finished writing — that's what the test waits for, so the
	// race "shell opened envFile but env hasn't written yet" is impossible
	// by construction. The trailing sleep keeps the script in-flight while
	// the test signals shutdown.
	script := fmt.Sprintf(`#!/bin/bash
env > %q
touch %q
sleep 60
echo '<goto>1_START.sh</goto>'
`, envFile, captured)
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "1_START.sh"), []byte(script), 0o755))

	rayBin := buildRayBinary(t)
	p := startServeDaemon(t, rayBin, tempRoot, workflowsDir, "test-env-absence")
	events, eventsCancel := subscribeShutdownEvents(t, p.baseURL)
	defer eventsCancel()

	_ = p.createRun(t)
	waitForFile(t, captured, 10*time.Second, p.diagnostics)

	// Use SIGTERM so the test settles inside the patience window without
	// needing two signals — env-absence is the same property regardless of
	// which signal triggered the shutdown.
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	awaitShutdownEvent(t, events, 8*time.Second, p.diagnostics, "shutdown_complete")
	eventsCancel()
	_, _, timedOut := p.waitForExit(t, 8*time.Second)
	require.False(t, timedOut, "daemon did not exit within 8s of SIGTERM%s", p.diagnostics())

	envBytes, err := os.ReadFile(envFile)
	require.NoError(t, err, "captured env file should exist")
	envText := string(envBytes)
	require.NotEmpty(t, envText,
		"captured env file should be non-empty (otherwise the absence assertion is vacuous)")
	for _, banned := range []string{"RAYMOND_STOP_REQUESTED", "RAYMOND_STOP_SENTINEL"} {
		// Match the variable as it would appear at the start of an `env`
		// line ("NAME=…"); avoid false positives if a value happens to
		// contain the string.
		marker := banned + "="
		assert.False(t, strings.Contains(envText, marker),
			"%s must never appear in a shell step's environment; captured env contained it",
			banned)
	}
}

// TestServeShutdownSentinelAbsence is the on-disk regression guard for the
// deleted T1 sentinel-file mechanism. Across a full shutdown sequence the
// daemon must never write .raymond/shutdown.sentinel. The test polls
// throughout the shutdown so a transient creation-and-delete would still
// be caught.
func TestServeShutdownSentinelAbsence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only test (matches the sibling shutdown tests)")
	}

	tempRoot := t.TempDir()
	workflowsDir := filepath.Join(tempRoot, "workflows")
	wfDir := filepath.Join(workflowsDir, "test-sentinel")
	raymondDir := filepath.Join(tempRoot, ".raymond")
	sentinelPath := filepath.Join(raymondDir, "shutdown.sentinel")
	marker := filepath.Join(tempRoot, "marker.txt")

	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	require.NoError(t, os.MkdirAll(raymondDir, 0o755))
	writeShutdownConfig(t, raymondDir)
	writeShutdownManifest(t, wfDir, "test-sentinel",
		"Long-sleeping workflow; the test drives shutdown via SIGINT escalation.")

	script := fmt.Sprintf(`#!/bin/bash
touch %q
sleep 60
echo '<goto>1_START.sh</goto>'
`, marker)
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "1_START.sh"), []byte(script), 0o755))

	rayBin := buildRayBinary(t)
	p := startServeDaemon(t, rayBin, tempRoot, workflowsDir, "test-sentinel")
	events, eventsCancel := subscribeShutdownEvents(t, p.baseURL)
	defer eventsCancel()

	// Asserts the sentinel doesn't exist right now. Used at every polling
	// step so a transient touch-and-remove would still trip the check.
	assertNoSentinel := func() {
		t.Helper()
		if _, err := os.Stat(sentinelPath); !os.IsNotExist(err) {
			t.Fatalf("shutdown sentinel %s must never exist (stat err = %v)%s",
				sentinelPath, err, p.diagnostics())
		}
	}
	assertNoSentinel()

	_ = p.createRun(t)
	waitForFile(t, marker, 10*time.Second, p.diagnostics)
	assertNoSentinel()

	// Poll the sentinel path in a background goroutine for the duration of
	// the shutdown sequence — exits when stopPoll closes. Any creation
	// during the test window flips sentinelSeen. The deferred close runs
	// on every test exit path (including require.* failures via Goexit) so
	// the poller never outlives the test goroutine.
	stopPoll := make(chan struct{})
	defer close(stopPoll)
	var sentinelMu sync.Mutex
	var sentinelSeen bool
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := os.Stat(sentinelPath); err == nil {
					sentinelMu.Lock()
					sentinelSeen = true
					sentinelMu.Unlock()
				}
			case <-stopPoll:
				return
			}
		}
	}()

	// Drive a full SIGINT-escalation sequence so quiesce and cancel both
	// run — if any path were going to write the sentinel, one of them is
	// where it would happen.
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGINT))
	awaitShutdownEvent(t, events, 5*time.Second, p.diagnostics, "shutdown_requested")
	assertNoSentinel()
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGINT))
	awaitShutdownEvent(t, events, 8*time.Second, p.diagnostics, "shutdown_complete")
	eventsCancel()
	_, _, timedOut := p.waitForExit(t, 8*time.Second)
	require.False(t, timedOut, "daemon did not exit within 8s%s", p.diagnostics())

	sentinelMu.Lock()
	seen := sentinelSeen
	sentinelMu.Unlock()
	assert.False(t, seen,
		"shutdown sentinel %s must never be written during the shutdown sequence%s",
		sentinelPath, p.diagnostics())
	assertNoSentinel()
}

// --------------------------------------------------------------------------
// Pi backend integration tests
// --------------------------------------------------------------------------

// piFixtureDir returns the path to the pi test fixture workflow.
func piFixtureDir() string {
	return filepath.Join("..", "..", "workflows", "pi_test_fixture")
}

// piAvailable reports whether the pi CLI is in PATH.
func piAvailable() bool {
	_, err := exec.LookPath("pi")
	return err == nil
}

// makePiStub writes a minimal stub pi script to a temp directory and returns
// the directory path. The stub responds to --version and exits successfully.
// All other invocations exit with an error to prevent accidental LLM calls.
func makePiStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "pi")
	content := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "pi 0.0.0-test-stub"
  exit 0
fi
echo "pi stub: unexpected invocation: $@" >&2
exit 1
`
	require.NoError(t, os.WriteFile(stub, []byte(content), 0o755))
	return dir
}

// TestPiIntegration_PreflightFailure verifies that a pi workflow fails fast
// with a clear, user-facing error when the pi binary is not in PATH.
func TestPiIntegration_PreflightFailure(t *testing.T) {
	// Point PATH at an empty directory so 'pi' cannot be found.
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	_, runErr := runWorkflow(t, piFixtureDir(), "START.sh", 0.0)
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "pi")
}

// TestPiIntegration_ContinueAndForkRejected verifies that a pi workflow
// rejects --continue-and-fork at launch with a clear, user-facing error.
func TestPiIntegration_ContinueAndForkRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pi stub script requires a Unix shell; skipping on Windows")
	}
	// Preflight: stub so it passes without a real pi binary.
	stubDir := makePiStub(t)
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+origPath)

	stateDir := t.TempDir()
	workflowID := "pi-cf-reject"
	ws := wfstate.CreateInitialState(workflowID, piFixtureDir(), "START.sh", 0, nil, "")
	ws.Agents[0].ContinueAndFork = true
	require.NoError(t, wfstate.WriteState(workflowID, ws, stateDir))

	runErr := orchestrator.RunAllAgents(context.Background(), workflowID,
		orchestrator.RunOptions{StateDir: stateDir, Quiet: true, NoWait: true},
	)
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "--continue-and-fork")
}

// TestPiIntegration_ScriptOnlyWorkflowWithStub verifies the full plumbing
// for a pi workflow that uses only script states: manifest parsing, preflight
// (via stub binary), backend construction, and executor wiring all work
// end-to-end without a real pi installation or LLM calls.
func TestPiIntegration_ScriptOnlyWorkflowWithStub(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pi stub script requires a Unix shell; skipping on Windows")
	}

	// Install the stub on PATH.
	stubDir := makePiStub(t)
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+origPath)

	completed, runErr := runWorkflow(t, piFixtureDir(), "START.sh", 0.0)
	require.NoError(t, runErr)
	assert.True(t, completed, "pi script-only workflow should complete cleanly")
}

// TestPiSessionID_RoundTrip verifies that a *string SessionID round-trips
// correctly through the JSON state file (write then read).
func TestPiSessionID_RoundTrip(t *testing.T) {
	stateDir := t.TempDir()
	workflowID := "pi-session-rt"
	ws := wfstate.CreateInitialState(workflowID, piFixtureDir(), "START.sh", 0, nil, "")

	sessionID := "some-uuid"
	ws.Agents[0].SessionID = &sessionID
	require.NoError(t, wfstate.WriteState(workflowID, ws, stateDir))

	ws2, err := wfstate.ReadState(workflowID, stateDir)
	require.NoError(t, err)
	require.NotNil(t, ws2.Agents[0].SessionID)
	assert.Equal(t, "some-uuid", *ws2.Agents[0].SessionID)
}

// TestPiIntegration_RealPi is an optional test that runs the pi test fixture
// against a real pi installation. It is skipped when pi is not in PATH.
// To enable: install pi (npm install -g @mariozechner/pi-coding-agent) and
// run with -tags integration.
func TestPiIntegration_RealPi(t *testing.T) {
	if !piAvailable() {
		t.Skip("pi CLI not found in PATH; skipping real-pi integration test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("pi script state requires Unix shell; skipping on Windows")
	}

	completed, runErr := runWorkflow(t, piFixtureDir(), "START.sh", 0.0)
	require.NoError(t, runErr)
	assert.True(t, completed, "pi script-only workflow should complete cleanly with real pi binary")
}
