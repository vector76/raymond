package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/backend"
	"github.com/vector76/raymond/internal/executors"
	"github.com/vector76/raymond/internal/orchestrator"
	"github.com/vector76/raymond/internal/parsing"
	wfstate "github.com/vector76/raymond/internal/state"
)

// setupPiWorkflow creates a directory scope with workflow.yaml declaring
// backend: pi and returns stateDir and workflowID.
func setupPiWorkflow(t *testing.T, initialState string) (stateDir, workflowID string) {
	t.Helper()
	tmpDir := t.TempDir()
	stateDir = filepath.Join(tmpDir, ".raymond", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	// Write a scope directory with workflow.yaml declaring pi backend.
	scopeDir := filepath.Join(tmpDir, "workflow")
	require.NoError(t, os.MkdirAll(scopeDir, 0o755))
	manifestContent := "id: pi-test\nbackend: pi\n"
	require.NoError(t, os.WriteFile(filepath.Join(scopeDir, "workflow.yaml"), []byte(manifestContent), 0o644))

	workflowID = "pi-test-wf"
	ws := wfstate.CreateInitialState(workflowID, scopeDir, initialState, 0, nil, "")
	require.NoError(t, wfstate.WriteState(workflowID, ws, stateDir))
	return stateDir, workflowID
}

// setupPiYamlWorkflow creates a YAML scope with backend: pi and returns
// stateDir and workflowID.
func setupPiYamlWorkflow(t *testing.T, initialState string) (stateDir, workflowID string) {
	t.Helper()
	content := fmt.Sprintf(`backend: pi
states:
  %s:
    prompt: "Hello"
`, strings.TrimSuffix(initialState, ".md"))
	return setupYamlWorkflow(t, content, initialState)
}

// TestPiBackend_PreflightFailure verifies that a pi workflow fails fast with a
// clear error when the pi binary is not found.
func TestPiBackend_PreflightFailure(t *testing.T) {
	stateDir, wfID := setupPiWorkflow(t, "start.md")

	orchestrator.SetPiPreflightFn(func(_ context.Context) error {
		return errors.New("pi not found in PATH\nInstall with: npm install -g @mariozechner/pi-coding-agent")
	})
	defer orchestrator.ResetPiPreflightFn()

	err := orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(stateDir))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pi not found")
}

// TestPiBackend_PreflightRerunOnResume verifies that the preflight check also
// runs when resuming a workflow (RunAllAgents is called again after a crash).
func TestPiBackend_PreflightRerunOnResume(t *testing.T) {
	stateDir, wfID := setupPiWorkflow(t, "start.md")

	calls := 0
	orchestrator.SetPiPreflightFn(func(_ context.Context) error {
		calls++
		return errors.New("pi missing")
	})
	defer orchestrator.ResetPiPreflightFn()

	// First run — should fail at preflight.
	_ = orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(stateDir))
	// Second run (simulating resume) — preflight runs again.
	_ = orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(stateDir))
	assert.Equal(t, 2, calls, "preflight should run on every RunAllAgents call")
}

// TestPiBackend_ContinueAndForkAccepted verifies that a pi workflow no longer
// rejects --continue-and-fork at launch time. The pi backend resolves the
// most-recent session in its session dir and forks from it (see
// internal/backend/pi.go and TestPiBackend_ContinueLatest_ResolvesAndForks for
// the unit-level coverage of the resolve-and-fork behavior).
func TestPiBackend_ContinueAndForkAccepted(t *testing.T) {
	stateDir, wfID := setupPiWorkflow(t, "start.md")

	// Preflight succeeds.
	orchestrator.SetPiPreflightFn(func(_ context.Context) error { return nil })
	defer orchestrator.ResetPiPreflightFn()

	// Set ContinueAndFork on the agent.
	ws, err := wfstate.ReadState(wfID, stateDir)
	require.NoError(t, err)
	ws.Agents[0].ContinueAndFork = true
	require.NoError(t, wfstate.WriteState(wfID, ws, stateDir))

	// Mock executor so we don't actually invoke pi; the test is that
	// RunAllAgents does not reject the flag at launch.
	exec := newMock(resultExecResult("done"))
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return exec })
	defer orchestrator.ResetExecutorFactory()

	runErr := orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(stateDir))
	require.NoError(t, runErr,
		"pi workflow must accept --continue-and-fork now that the backend resolves it via session-dir lookup")
}

// TestNoBackend_UsesClaudeBackend verifies that a workflow with no backend
// declaration still uses Claude (regression test for the default path).
func TestNoBackend_UsesClaudeBackend(t *testing.T) {
	stateDir, wfID := setupWorkflow(t, "START.md")

	var capturedBackend backend.Backend
	exec := newMock(resultExecResult("done"))
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor {
		return &backendCapturingExec{inner: exec, capture: &capturedBackend}
	})
	defer orchestrator.ResetExecutorFactory()

	err := orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(stateDir))
	require.NoError(t, err)
	// Backend is set; since no backend declared it should be the Claude backend.
	assert.NotNil(t, capturedBackend)
	_, isClaude := capturedBackend.(*backend.ClaudeBackend)
	assert.True(t, isClaude, "expected Claude backend when no backend declared")
}

// TestPiBackend_PassedThroughToExecutor verifies that a pi-declaring workflow
// sets a PiBackend in the ExecutionContext passed to the executor.
func TestPiBackend_PassedThroughToExecutor(t *testing.T) {
	stateDir, wfID := setupPiWorkflow(t, "start.md")

	// Preflight succeeds.
	orchestrator.SetPiPreflightFn(func(_ context.Context) error { return nil })
	defer orchestrator.ResetPiPreflightFn()

	var capturedBackend backend.Backend
	exec := newMock(resultExecResult("done"))
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor {
		return &backendCapturingExec{inner: exec, capture: &capturedBackend}
	})
	defer orchestrator.ResetExecutorFactory()

	err := orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(stateDir))
	require.NoError(t, err)
	assert.NotNil(t, capturedBackend)
	_, isPi := capturedBackend.(*backend.PiBackend)
	assert.True(t, isPi, "expected PiBackend for pi workflow")
}

// TestPiYamlScope_BackendResolved verifies that GetBackend is used for YAML
// scope workflows and the pi backend is constructed correctly.
func TestPiYamlScope_BackendResolved(t *testing.T) {
	stateDir, wfID := setupPiYamlWorkflow(t, "start.md")

	orchestrator.SetPiPreflightFn(func(_ context.Context) error { return nil })
	defer orchestrator.ResetPiPreflightFn()

	var capturedBackend backend.Backend
	exec := newMock(resultExecResult("done"))
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor {
		return &backendCapturingExec{inner: exec, capture: &capturedBackend}
	})
	defer orchestrator.ResetExecutorFactory()

	err := orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(stateDir))
	require.NoError(t, err)
	assert.NotNil(t, capturedBackend)
	_, isPi := capturedBackend.(*backend.PiBackend)
	assert.True(t, isPi, "expected PiBackend for pi YAML scope workflow")
}

// backendCapturingExec wraps a mockExec and captures the Backend from the
// ExecutionContext so tests can verify which backend was selected.
type backendCapturingExec struct {
	inner   *mockExec
	capture *backend.Backend
}

func (e *backendCapturingExec) Execute(
	ctx context.Context,
	agent *wfstate.AgentState,
	wfState *wfstate.WorkflowState,
	execCtx *executors.ExecutionContext,
) (executors.ExecutionResult, error) {
	if execCtx != nil {
		*e.capture = execCtx.Backend
	}
	return e.inner.Execute(ctx, agent, wfState, execCtx)
}

// perStateBackendCapture records (CurrentState, Backend) for every Execute
// call against a single shared mockExec. Used by the cross-workflow tests to
// verify the backend tracks the agent's current ScopeDir rather than being
// fixed at the outer workflow's backend.
type perStateBackendCapture struct {
	inner   *mockExec
	mu      *sync.Mutex
	entries *[]capturedExec
}

type capturedExec struct {
	state   string
	backend backend.Backend
}

func (e *perStateBackendCapture) Execute(
	ctx context.Context,
	agent *wfstate.AgentState,
	wfState *wfstate.WorkflowState,
	execCtx *executors.ExecutionContext,
) (executors.ExecutionResult, error) {
	e.mu.Lock()
	*e.entries = append(*e.entries, capturedExec{
		state:   agent.CurrentState,
		backend: execCtx.Backend,
	})
	e.mu.Unlock()
	return e.inner.Execute(ctx, agent, wfState, execCtx)
}

// TestCrossWorkflow_NestedBackendResolvedFromNestedScope verifies the spec
// promise that "raymond launches the appropriate backend per nested
// workflow." The outer workflow declares no backend (Claude default); a
// <call-workflow> nests into a workflow that declares backend: pi. The
// nested state must run on PiBackend, and the post-return state must run on
// ClaudeBackend again. Before the fix, the backend was resolved once at
// startup from the outer scope and never re-resolved, so the nested state
// silently ran on Claude.
func TestCrossWorkflow_NestedBackendResolvedFromNestedScope(t *testing.T) {
	// Outer workflow: no backend declared → defaults to Claude.
	tmpDir := t.TempDir()
	outerDir := filepath.Join(tmpDir, "outer")
	require.NoError(t, os.MkdirAll(outerDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outerDir, "workflow.yaml"),
		[]byte("id: outer-wf\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outerDir, "START.md"),
		[]byte("# outer start"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outerDir, "DONE.md"),
		[]byte("# outer done"), 0o644))

	// Inner workflow: declares backend: pi.
	innerDir := filepath.Join(tmpDir, "inner")
	require.NoError(t, os.MkdirAll(innerDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(innerDir, "workflow.yaml"),
		[]byte("id: inner-wf\nbackend: pi\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(innerDir, "START.md"),
		[]byte("# inner start"), 0o644))

	stateDir := filepath.Join(tmpDir, ".raymond", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	wfID := "outer-wf"
	ws := wfstate.CreateInitialState(wfID, outerDir, "START.md", 0, nil, "")
	require.NoError(t, wfstate.WriteState(wfID, ws, stateDir))

	// Pi preflight succeeds — the test is about backend selection, not
	// preflight wiring.
	orchestrator.SetPiPreflightFn(func(_ context.Context) error { return nil })
	defer orchestrator.ResetPiPreflightFn()

	mock := newMock(
		// START.md (outer/Claude): call-workflow into inner.
		executors.ExecutionResult{Transition: parsing.Transition{
			Tag:        "call-workflow",
			Target:     innerDir,
			Attributes: map[string]string{"return": "DONE.md"},
		}},
		// START.md (inner/pi): return to caller.
		resultExecResult("inner done"),
		// DONE.md (outer/Claude): terminate.
		resultExecResult("outer done"),
	)
	var mu sync.Mutex
	var entries []capturedExec
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor {
		return &perStateBackendCapture{inner: mock, mu: &mu, entries: &entries}
	})
	defer orchestrator.ResetExecutorFactory()

	err := orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(stateDir))
	require.NoError(t, err)

	require.Len(t, entries, 3,
		"expected three executor calls: outer START → inner START → outer DONE; got %d", len(entries))

	// Outer START runs on Claude (outer workflow declares no backend).
	_, ok := entries[0].backend.(*backend.ClaudeBackend)
	assert.True(t, ok, "outer START.md must run on Claude; got %T", entries[0].backend)

	// Inner START runs on pi (inner workflow declares backend: pi). This is
	// the assertion that fails pre-fix.
	_, ok = entries[1].backend.(*backend.PiBackend)
	assert.True(t, ok,
		"inner START.md must run on Pi (declared by nested workflow); got %T", entries[1].backend)

	// Outer DONE runs on Claude again (agent popped back to outer scope).
	_, ok = entries[2].backend.(*backend.ClaudeBackend)
	assert.True(t, ok,
		"outer DONE.md must run on Claude after returning from nested call; got %T", entries[2].backend)
}

// TestCrossWorkflow_NestedBackendResolvedFromNestedScope_PiToClaude is the
// symmetric case: outer pi calls into a Claude-declaring inner workflow. The
// inner state must run on Claude.
func TestCrossWorkflow_NestedBackendResolvedFromNestedScope_PiToClaude(t *testing.T) {
	tmpDir := t.TempDir()
	outerDir := filepath.Join(tmpDir, "outer")
	require.NoError(t, os.MkdirAll(outerDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outerDir, "workflow.yaml"),
		[]byte("id: outer-pi\nbackend: pi\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outerDir, "START.md"),
		[]byte("# outer start"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outerDir, "DONE.md"),
		[]byte("# outer done"), 0o644))

	innerDir := filepath.Join(tmpDir, "inner")
	require.NoError(t, os.MkdirAll(innerDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(innerDir, "workflow.yaml"),
		[]byte("id: inner-claude\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(innerDir, "START.md"),
		[]byte("# inner start"), 0o644))

	stateDir := filepath.Join(tmpDir, ".raymond", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	wfID := "outer-pi"
	ws := wfstate.CreateInitialState(wfID, outerDir, "START.md", 0, nil, "")
	require.NoError(t, wfstate.WriteState(wfID, ws, stateDir))

	orchestrator.SetPiPreflightFn(func(_ context.Context) error { return nil })
	defer orchestrator.ResetPiPreflightFn()

	mock := newMock(
		executors.ExecutionResult{Transition: parsing.Transition{
			Tag:        "call-workflow",
			Target:     innerDir,
			Attributes: map[string]string{"return": "DONE.md"},
		}},
		resultExecResult("inner done"),
		resultExecResult("outer done"),
	)
	var mu sync.Mutex
	var entries []capturedExec
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor {
		return &perStateBackendCapture{inner: mock, mu: &mu, entries: &entries}
	})
	defer orchestrator.ResetExecutorFactory()

	err := orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(stateDir))
	require.NoError(t, err)

	require.Len(t, entries, 3)
	_, ok := entries[0].backend.(*backend.PiBackend)
	assert.True(t, ok, "outer START.md must run on Pi; got %T", entries[0].backend)
	_, ok = entries[1].backend.(*backend.ClaudeBackend)
	assert.True(t, ok,
		"inner START.md must run on Claude (declared by nested workflow); got %T", entries[1].backend)
	_, ok = entries[2].backend.(*backend.PiBackend)
	assert.True(t, ok,
		"outer DONE.md must run on Pi after returning from nested Claude call; got %T", entries[2].backend)
}

// TestCrossWorkflow_NestedPiPreflightFiresLazily verifies that when the
// outer workflow is non-pi (so the upfront preflight check is skipped) and a
// nested workflow declares backend: pi but pi isn't installed, the failure
// surfaces with the pi-not-found error at the point the nested workflow is
// first resolved — not as a confused mid-execution failure. Guards the
// lazy-preflight branch of backendResolver.
func TestCrossWorkflow_NestedPiPreflightFiresLazily(t *testing.T) {
	tmpDir := t.TempDir()
	outerDir := filepath.Join(tmpDir, "outer")
	require.NoError(t, os.MkdirAll(outerDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outerDir, "workflow.yaml"),
		[]byte("id: outer-claude\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outerDir, "START.md"),
		[]byte("# outer start"), 0o644))

	innerDir := filepath.Join(tmpDir, "inner")
	require.NoError(t, os.MkdirAll(innerDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(innerDir, "workflow.yaml"),
		[]byte("id: inner-pi\nbackend: pi\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(innerDir, "START.md"),
		[]byte("# inner start"), 0o644))

	stateDir := filepath.Join(tmpDir, ".raymond", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	wfID := "outer-claude"
	ws := wfstate.CreateInitialState(wfID, outerDir, "START.md", 0, nil, "")
	require.NoError(t, wfstate.WriteState(wfID, ws, stateDir))

	// Pi preflight fails (simulating pi not in PATH). Because the outer
	// declares Claude, the upfront preflight in RunAllAgents is skipped;
	// the resolver must run preflight when it first sees backend: pi on
	// the nested call.
	preflightCalls := 0
	orchestrator.SetPiPreflightFn(func(_ context.Context) error {
		preflightCalls++
		return errors.New("pi not found in PATH\nInstall with: npm install -g @mariozechner/pi-coding-agent")
	})
	defer orchestrator.ResetPiPreflightFn()

	mock := newMock(
		executors.ExecutionResult{Transition: parsing.Transition{
			Tag:        "call-workflow",
			Target:     innerDir,
			Attributes: map[string]string{"return": "DONE.md"},
		}},
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	// RunAllAgents returns the pi-not-found error fatally — matches the
	// upfront preflight behavior (TestPiBackend_PreflightFailure). Retry
	// won't fix a missing binary, so the run aborts with actionable
	// diagnostics rather than silently pausing.
	runErr := orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(stateDir))
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "pi not found",
		"nested pi preflight failure must surface the pi-not-found message")
	assert.GreaterOrEqual(t, preflightCalls, 1,
		"resolver must call pi preflight when nested workflow declares backend: pi")
}
