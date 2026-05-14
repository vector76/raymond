package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/backend"
	"github.com/vector76/raymond/internal/executors"
	"github.com/vector76/raymond/internal/orchestrator"
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

// TestPiBackend_ContinueAndForkRejected verifies that a pi workflow rejects
// the --continue-and-fork flag at launch time.
func TestPiBackend_ContinueAndForkRejected(t *testing.T) {
	stateDir, wfID := setupPiWorkflow(t, "start.md")

	// Preflight succeeds.
	orchestrator.SetPiPreflightFn(func(_ context.Context) error { return nil })
	defer orchestrator.ResetPiPreflightFn()

	// Set ContinueAndFork on the agent.
	ws, err := wfstate.ReadState(wfID, stateDir)
	require.NoError(t, err)
	ws.Agents[0].ContinueAndFork = true
	require.NoError(t, wfstate.WriteState(wfID, ws, stateDir))

	runErr := orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(stateDir))
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "--continue-and-fork")
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
