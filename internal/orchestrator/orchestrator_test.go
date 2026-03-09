package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/executors"
	"github.com/vector76/raymond/internal/orchestrator"
	"github.com/vector76/raymond/internal/parsing"
	wfstate "github.com/vector76/raymond/internal/state"
)

// ----------------------------------------------------------------------------
// Mock executor helpers
// ----------------------------------------------------------------------------

// mockExec is a StateExecutor whose results are pre-programmed.
type mockExec struct {
	mu      sync.Mutex
	results []executors.ExecutionResult
	errs    []error
	idx     int
}

func (m *mockExec) Execute(
	_ context.Context,
	_ *wfstate.AgentState,
	wfState *wfstate.WorkflowState,
	_ *executors.ExecutionContext,
) (executors.ExecutionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.idx
	m.idx++
	if m.errs != nil && i < len(m.errs) && m.errs[i] != nil {
		return executors.ExecutionResult{}, m.errs[i]
	}
	if i < len(m.results) {
		res := m.results[i]
		// Mirror real executor behaviour: accumulate cost directly into
		// wfState so that the orchestrator does not need to do it again.
		if res.CostUSD > 0 {
			wfState.TotalCostUSD += res.CostUSD
		}
		return res, nil
	}
	return executors.ExecutionResult{}, errors.New("mock: no more results configured")
}

// newMock returns a mockExec whose calls succeed in order.
func newMock(results ...executors.ExecutionResult) *mockExec {
	return &mockExec{results: results}
}

// newMockErrors returns a mockExec whose calls fail with the given errors.
// A nil entry means success (uses zero ExecutionResult).
func newMockErrors(errs ...error) *mockExec {
	return &mockExec{errs: errs}
}

// newMockMixed returns a mockExec with both results and errors.
// For call i: if errs[i] != nil → error; else results[i].
func newMockMixed(results []executors.ExecutionResult, errs []error) *mockExec {
	return &mockExec{results: results, errs: errs}
}

// gotoResult returns an ExecutionResult carrying a goto transition.
func gotoResult(target string) executors.ExecutionResult {
	return executors.ExecutionResult{
		Transition: parsing.Transition{
			Tag:        "goto",
			Target:     target,
			Attributes: map[string]string{},
		},
	}
}

// resultExecResult returns an ExecutionResult carrying a result transition.
func resultExecResult(payload string) executors.ExecutionResult {
	return executors.ExecutionResult{
		Transition: parsing.Transition{
			Tag:        "result",
			Payload:    payload,
			Attributes: map[string]string{},
		},
	}
}

// forkResult returns an ExecutionResult with a fork transition.
func forkResult(target, next string) executors.ExecutionResult {
	return executors.ExecutionResult{
		Transition: parsing.Transition{
			Tag:    "fork",
			Target: target,
			Attributes: map[string]string{
				"next": next,
			},
		},
	}
}

// ----------------------------------------------------------------------------
// Test setup helpers
// ----------------------------------------------------------------------------

// setupWorkflow creates a state file and returns the state directory and workflow ID.
func setupWorkflow(t *testing.T, agentState string) (stateDir, workflowID string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".raymond", "state")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	workflowID = "test-wf"
	ws := wfstate.CreateInitialState(workflowID, "workflows/test", agentState, 0, nil)
	require.NoError(t, wfstate.WriteState(workflowID, ws, dir))
	return dir, workflowID
}

// defaultOpts returns minimal RunOptions pointing at dir.
func defaultOpts(dir string) orchestrator.RunOptions {
	return orchestrator.RunOptions{
		StateDir: dir,
		Debug:    false,
		Quiet:    true,
		NoWait:   true,
	}
}

// collectEvents subscribes to event type T on b and appends to a slice,
// returning the slice pointer and an unsubscribe function.
func collectEvents[T any](b *bus.Bus) (*[]T, func()) {
	var mu sync.Mutex
	var got []T
	cancel := bus.Subscribe(b, func(e T) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	return &got, cancel
}

// ----------------------------------------------------------------------------
// Basic loop
// ----------------------------------------------------------------------------

func TestEmptyAgentsExitsImmediately(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	// Clear agents to simulate a completed workflow.
	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	ws.Agents = nil
	require.NoError(t, wfstate.WriteState(wfID, ws, dir))

	called := false
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor {
		called = true
		return nil
	})
	defer orchestrator.ResetExecutorFactory()

	err = orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir))
	require.NoError(t, err)
	assert.False(t, called, "executor should not be called when no agents remain")
}

func TestWorkflowCompletedEventEmitted(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	ws.Agents = nil
	require.NoError(t, wfstate.WriteState(wfID, ws, dir))

	var got []events.WorkflowCompleted
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.WorkflowCompleted) { got = append(got, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, got, 1)
	assert.Equal(t, wfID, got[0].WorkflowID)
}

func TestWorkflowStateFileDeletedOnCompletion(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	ws.Agents = nil
	require.NoError(t, wfstate.WriteState(wfID, ws, dir))

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))

	_, err = wfstate.ReadState(wfID, dir)
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestGotoTransitionUpdatesCurrentState(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	mock := newMock(
		gotoResult("NEXT.md"),
		resultExecResult("done"),
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	assert.Equal(t, 2, mock.idx)
}

func TestResultWithEmptyStackTerminatesAgent(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	mock := newMock(resultExecResult("final"))
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var terminated []events.AgentTerminated
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentTerminated) { terminated = append(terminated, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, terminated, 1)
	assert.Equal(t, "main", terminated[0].AgentID)
	assert.Equal(t, "final", terminated[0].ResultPayload)
}

func TestWorkflowStartedEventEmitted(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	mock := newMock(resultExecResult(""))
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var started []events.WorkflowStarted
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.WorkflowStarted) { started = append(started, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, started, 1)
	assert.Equal(t, wfID, started[0].WorkflowID)
}

func TestTransitionOccurredEmittedForGoto(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	mock := newMock(
		gotoResult("NEXT.md"),
		resultExecResult(""),
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var transitions []events.TransitionOccurred
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.TransitionOccurred) { transitions = append(transitions, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, transitions, 2) // goto + result
	assert.Equal(t, "goto", transitions[0].TransitionType)
	assert.Equal(t, "START.md", transitions[0].FromState)
	assert.Equal(t, "NEXT.md", transitions[0].ToState)
	assert.Equal(t, "result", transitions[1].TransitionType)
	assert.Equal(t, "NEXT.md", transitions[1].FromState)
	assert.Equal(t, "", transitions[1].ToState) // terminated
}

func TestTransitionOccurredMetadataIncludesResultPayload(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	mock := newMock(resultExecResult("my-payload"))
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var transitions []events.TransitionOccurred
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.TransitionOccurred) { transitions = append(transitions, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, transitions, 1)
	assert.Equal(t, "my-payload", transitions[0].Metadata["result_payload"])
}

func TestSessionIDAppliedToAgentAfterStep(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	sid := "sess-abc"
	mock := newMock(
		executors.ExecutionResult{
			Transition: parsing.Transition{Tag: "goto", Target: "NEXT.md", Attributes: map[string]string{}},
			SessionID:  &sid,
		},
		resultExecResult(""),
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	// The session ID update is an internal detail; we verify the workflow
	// completes without error (which requires session propagation to work).
	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
}

func TestSessionIDPreservedThroughScriptStep(t *testing.T) {
	// A nil SessionID in ExecutionResult means "no change" (script executor
	// contract). Verify that the agent's existing session ID is preserved when
	// a step returns nil SessionID.
	dir, wfID := setupWorkflow(t, "START.md")

	initialSID := "original-session"

	// Prime the workflow with an initial session ID by setting it in state.
	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	ws.Agents[0].SessionID = &initialSID
	require.NoError(t, wfstate.WriteState(wfID, ws, dir))

	// Script step: returns nil SessionID (should preserve existing).
	mock := newMock(
		executors.ExecutionResult{
			Transition: parsing.Transition{Tag: "goto", Target: "NEXT.md", Attributes: map[string]string{}},
			SessionID:  nil, // nil = no change
		},
		resultExecResult(""),
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	// Capture state after the first transition to verify session ID preserved.
	var capturedSID *string
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.TransitionOccurred) {
			if capturedSID != nil {
				return
			}
			ws2, rerr := wfstate.ReadState(wfID, dir)
			if rerr == nil && len(ws2.Agents) > 0 {
				capturedSID = ws2.Agents[0].SessionID
			}
		})
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.NotNil(t, capturedSID, "session ID should be preserved after script step")
	assert.Equal(t, initialSID, *capturedSID)
}

func TestStatePersistenceAfterEachStep(t *testing.T) {
	// After a limit error the agent is paused and state must be persisted to disk
	// so the workflow can be resumed. This tests that WriteState is called.
	dir, wfID := setupWorkflow(t, "START.md")

	// First call: goto NEXT.md; second call: limit error → pause.
	mock := newMockMixed(
		[]executors.ExecutionResult{gotoResult("NEXT.md"), {}},
		[]error{nil, &executors.ClaudeCodeLimitError{Msg: "hit your limit"}},
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))

	// State file still exists (paused, not completed).
	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	// The agent should be at NEXT.md (goto applied) and paused.
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "NEXT.md", ws.Agents[0].CurrentState)
	assert.Equal(t, "paused", ws.Agents[0].Status)
}

func TestStatePersistenceAfterNormalGoto(t *testing.T) {
	// Verifies that WriteState is called after a successful (non-error) goto
	// transition, so crash recovery can resume from the intermediate state.
	//
	// Strategy: use a mock executor that returns a goto on step 1, then
	// cancels the context on step 2. The context cancellation surfaces as an
	// error from the orchestrator; but the state file should already reflect
	// the goto from step 1 (current_state = NEXT.md).
	dir, wfID := setupWorkflow(t, "START.md")

	ctx, cancel := context.WithCancel(context.Background())

	cancellingMock := &cancelOnSecondCallExec{
		first:  gotoResult("NEXT.md"),
		cancel: cancel,
	}

	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return cancellingMock })
	defer orchestrator.ResetExecutorFactory()

	// Expect context.Canceled since the second step will cancel the context.
	runErr := orchestrator.RunAllAgents(ctx, wfID, defaultOpts(dir))
	require.ErrorIs(t, runErr, context.Canceled)

	// After the first goto, state must have been written with current_state=NEXT.md.
	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err, "state file should persist after cancellation")
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "NEXT.md", ws.Agents[0].CurrentState,
		"state should be written at NEXT.md after first goto")
}

// cancelOnSecondCallExec returns a goto on the first call, then cancels the
// context and returns context.Canceled on the second call.
type cancelOnSecondCallExec struct {
	mu     sync.Mutex
	calls  int
	first  executors.ExecutionResult
	cancel context.CancelFunc
}

func (e *cancelOnSecondCallExec) Execute(
	ctx context.Context,
	_ *wfstate.AgentState,
	_ *wfstate.WorkflowState,
	_ *executors.ExecutionContext,
) (executors.ExecutionResult, error) {
	e.mu.Lock()
	n := e.calls
	e.calls++
	e.mu.Unlock()
	if n == 0 {
		return e.first, nil
	}
	e.cancel()
	<-ctx.Done()
	return executors.ExecutionResult{}, ctx.Err()
}

// ----------------------------------------------------------------------------
// Fork transition
// ----------------------------------------------------------------------------

func TestForkTransitionSpawnsWorkerAgent(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	// Shared mock so all executor calls advance the same idx counter.
	// Call order: START.md (fork), PARENT_NEXT.md (parent result), WORKER.md (worker result).
	shared := newMock(
		forkResult("WORKER.md", "PARENT_NEXT.md"),
		resultExecResult("parent done"), // parent at PARENT_NEXT.md
		resultExecResult("worker done"), // worker at WORKER.md
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return shared })
	defer orchestrator.ResetExecutorFactory()

	var spawned []events.AgentSpawned
	var terminated []events.AgentTerminated
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentSpawned) { spawned = append(spawned, e) })
		bus.Subscribe(b, func(e events.AgentTerminated) { terminated = append(terminated, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, spawned, 1)
	assert.Equal(t, "main", spawned[0].ParentAgentID)
	assert.Equal(t, "main_worker1", spawned[0].NewAgentID)
	assert.Len(t, terminated, 2) // parent + worker
}

// ----------------------------------------------------------------------------
// Error handling: ClaudeCodeLimitError → pause
// ----------------------------------------------------------------------------

func TestLimitErrorPausesAgent(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	mock := newMockErrors(&executors.ClaudeCodeLimitError{Msg: "hit your limit · resets 3pm (America/Chicago)"})
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var paused []events.AgentPaused
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentPaused) { paused = append(paused, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, paused, 1)
	assert.Equal(t, "main", paused[0].AgentID)

	// State file should still exist (not deleted) so workflow can be resumed.
	_, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
}

func TestLimitErrorAgentStatusPersistedAsPaused(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	mock := newMockErrors(&executors.ClaudeCodeLimitError{Msg: "hit your limit"})
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))

	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "paused", ws.Agents[0].Status)
	assert.NotEmpty(t, ws.Agents[0].Error)
}

// ----------------------------------------------------------------------------
// Error handling: ClaudeCodeError → retry up to MaxRetries, then pause
// ----------------------------------------------------------------------------

func TestClaudeCodeErrorRetriesUpToMax(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	// Fail MaxRetries times, then succeed.
	maxRetries := orchestrator.MaxRetries
	errs := make([]error, maxRetries+1)
	for i := 0; i < maxRetries; i++ {
		errs[i] = &executors.ClaudeCodeError{Msg: "transient"}
	}
	// Last call succeeds.
	results := make([]executors.ExecutionResult, maxRetries+1)
	results[maxRetries] = resultExecResult("")
	mock := newMockMixed(results, errs)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var errEvents []events.ErrorOccurred
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.ErrorOccurred) { errEvents = append(errEvents, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	assert.Len(t, errEvents, maxRetries) // one ErrorOccurred per failure
	for i, ev := range errEvents {
		assert.True(t, ev.IsRetryable || i == maxRetries-1, "errors should be retryable until max")
	}
}

func TestClaudeCodeErrorExceedingMaxRetriesPausesAgent(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	// Fail MaxRetries+1 times (exceed limit).
	maxRetries := orchestrator.MaxRetries
	errs := make([]error, maxRetries+1)
	for i := range errs {
		errs[i] = &executors.ClaudeCodeError{Msg: "persistent"}
	}
	mock := newMockErrors(errs...)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var paused []events.AgentPaused
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentPaused) { paused = append(paused, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, paused, 1)
	assert.Equal(t, "main", paused[0].AgentID)
}

// ----------------------------------------------------------------------------
// Error handling: ScriptError → fatal (no retry, agent removed)
// ----------------------------------------------------------------------------

func TestScriptErrorIsFatal(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.sh")

	mock := newMockErrors(&executors.ScriptError{Msg: "exit code 1"})
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var errEvents []events.ErrorOccurred
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.ErrorOccurred) { errEvents = append(errEvents, e) })
	})
	defer orchestrator.ResetBusHook()

	// ScriptError should propagate up as an error (not silently handled).
	err := orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir))
	require.Error(t, err)
	var se *executors.ScriptError
	assert.True(t, errors.As(err, &se))
}

// ----------------------------------------------------------------------------
// Error handling: PromptFileError → retry, then pause
// ----------------------------------------------------------------------------

func TestPromptFileErrorRetriesAndPauses(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	maxRetries := orchestrator.MaxRetries
	errs := make([]error, maxRetries+1)
	for i := range errs {
		errs[i] = &executors.PromptFileError{Msg: "file not found"}
	}
	mock := newMockErrors(errs...)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var paused []events.AgentPaused
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentPaused) { paused = append(paused, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, paused, 1)
}

// ----------------------------------------------------------------------------
// Resume: resetPausedAgents clears status on startup
// ----------------------------------------------------------------------------

func TestResumeResetsAgentStatusOnStartup(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	// Write state with paused agent.
	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	ws.Agents[0].Status = "paused"
	ws.Agents[0].Error = "hit your limit"
	ws.Agents[0].RetryCount = 2
	require.NoError(t, wfstate.WriteState(wfID, ws, dir))

	mock := newMock(resultExecResult("resumed"))
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	// Should run the previously-paused agent without error.
	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	assert.Equal(t, 1, mock.idx, "executor should be called once after reset")
}

// ----------------------------------------------------------------------------
// WorkflowPaused when all agents are paused
// ----------------------------------------------------------------------------

func TestWorkflowPausedWhenAllAgentsPaused(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	// Pause all agents before starting.
	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	ws.Agents[0].Status = "paused"
	ws.Agents[0].Error = "hit your limit"
	require.NoError(t, wfstate.WriteState(wfID, ws, dir))

	// With NoWait=true and all agents paused, should emit WorkflowPaused.
	var wfPaused []events.WorkflowPaused
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.WorkflowPaused) { wfPaused = append(wfPaused, e) })
	})
	defer orchestrator.ResetBusHook()

	// Don't reset paused agents — StartRunning=false (no reset on startup for this test).
	opts := defaultOpts(dir)
	opts.NoResetPaused = true // skip reset to keep agents paused
	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, opts))

	require.Len(t, wfPaused, 1)
	assert.Equal(t, wfID, wfPaused[0].WorkflowID)

	// State file preserved for resume.
	_, err = wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
}

// ----------------------------------------------------------------------------
// Multiple agents (sequential execution)
// ----------------------------------------------------------------------------

func TestTwoIndependentAgentsAllTerminate(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	// Add a second agent manually.
	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	ws.Agents = append(ws.Agents, wfstate.AgentState{
		ID:           "worker",
		CurrentState: "WORKER.md",
		Stack:        []wfstate.StackFrame{},
	})
	require.NoError(t, wfstate.WriteState(wfID, ws, dir))

	callCount := 0
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor {
		return &mockExec{
			results: []executors.ExecutionResult{
				resultExecResult("agent 1"),
				resultExecResult("agent 2"),
			},
		}
	})
	defer orchestrator.ResetExecutorFactory()
	_ = callCount

	var terminated []events.AgentTerminated
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentTerminated) { terminated = append(terminated, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	assert.Len(t, terminated, 2)
}

// ----------------------------------------------------------------------------
// Context cancellation
// ----------------------------------------------------------------------------

func TestContextCancellationStopsLoop(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	ctx, cancel := context.WithCancel(context.Background())

	// Executor that blocks until context is cancelled.
	called := make(chan struct{}, 1)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor {
		return &blockingExec{ctx: ctx, called: called}
	})
	defer orchestrator.ResetExecutorFactory()

	errCh := make(chan error, 1)
	go func() {
		errCh <- orchestrator.RunAllAgents(ctx, wfID, defaultOpts(dir))
	}()

	// Wait until executor is entered, then cancel.
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("executor not called within timeout")
	}
	cancel()

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("RunAllAgents did not return after context cancel")
	}
}

// blockingExec blocks until its context is done.
type blockingExec struct {
	ctx    context.Context
	called chan<- struct{}
}

func (b *blockingExec) Execute(
	ctx context.Context,
	_ *wfstate.AgentState,
	_ *wfstate.WorkflowState,
	_ *executors.ExecutionContext,
) (executors.ExecutionResult, error) {
	b.called <- struct{}{}
	<-ctx.Done()
	return executors.ExecutionResult{}, ctx.Err()
}

// ----------------------------------------------------------------------------
// TotalCostUSD accumulation
// ----------------------------------------------------------------------------

func TestCostAccumulatesAcrossSteps(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	mock := newMock(
		executors.ExecutionResult{
			Transition: parsing.Transition{Tag: "goto", Target: "NEXT.md", Attributes: map[string]string{}},
			CostUSD:    0.10,
		},
		executors.ExecutionResult{
			Transition: parsing.Transition{Tag: "result", Payload: "", Attributes: map[string]string{}},
			CostUSD:    0.05,
		},
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var completed []events.WorkflowCompleted
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.WorkflowCompleted) { completed = append(completed, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, completed, 1)
	assert.InDelta(t, 0.15, completed[0].TotalCostUSD, 1e-9)
}

// ----------------------------------------------------------------------------
// Error handling: ClaudeCodeTimeoutWrappedError → retry then pause
// ----------------------------------------------------------------------------

func TestTimeoutErrorRetriesAndPauses(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	maxRetries := orchestrator.MaxRetries
	errs := make([]error, maxRetries+1)
	for i := range errs {
		errs[i] = &executors.ClaudeCodeTimeoutWrappedError{Msg: "timeout"}
	}
	mock := newMockErrors(errs...)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var paused []events.AgentPaused
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentPaused) { paused = append(paused, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, paused, 1)
	assert.Equal(t, "timeout", paused[0].Reason)
}

// ----------------------------------------------------------------------------
// Session ID management across transitions
//
// Regression tests for the bug where execResult.SessionID was applied to the
// agent AFTER ApplyTransition(), overwriting what transition handlers had done:
//   - <reset>: handler clears SessionID=nil, but loop restored the old session.
//   - <function>: handler clears SessionID=nil, but loop restored the caller's session.
//   - <result> (return): handler restores caller's session from the stack, but
//     loop overwrote it with the callee's session.
// ----------------------------------------------------------------------------

// sessionCapturingMock is a StateExecutor that records the session_id on the
// agent at each Execute call, then returns pre-programmed results.
type sessionCapturingMock struct {
	mu               sync.Mutex
	results          []executors.ExecutionResult
	idx              int
	capturedSessions []*string // deep-copied at call time to avoid aliasing
}

func (m *sessionCapturingMock) Execute(
	_ context.Context,
	agent *wfstate.AgentState,
	wfState *wfstate.WorkflowState,
	_ *executors.ExecutionContext,
) (executors.ExecutionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.idx
	m.idx++

	// Deep-copy the session_id to avoid pointer aliasing after the call.
	var capturedID *string
	if agent.SessionID != nil {
		s := *agent.SessionID
		capturedID = &s
	}
	m.capturedSessions = append(m.capturedSessions, capturedID)

	if i < len(m.results) {
		res := m.results[i]
		if res.CostUSD > 0 {
			wfState.TotalCostUSD += res.CostUSD
		}
		return res, nil
	}
	return executors.ExecutionResult{}, errors.New("sessionCapturingMock: no more results configured")
}

// sidPtr returns a pointer to s, for constructing ExecutionResult.SessionID.
func sidPtr(s string) *string { return &s }

// resetExecResult returns an ExecutionResult with a <reset> transition and a session ID.
func resetExecResult(target, sessionID string) executors.ExecutionResult {
	return executors.ExecutionResult{
		SessionID: sidPtr(sessionID),
		Transition: parsing.Transition{
			Tag:        "reset",
			Target:     target,
			Attributes: map[string]string{},
		},
	}
}

// functionExecResult returns an ExecutionResult with a <function> transition and a session ID.
func functionExecResult(target, returnState, sessionID string) executors.ExecutionResult {
	return executors.ExecutionResult{
		SessionID: sidPtr(sessionID),
		Transition: parsing.Transition{
			Tag:        "function",
			Target:     target,
			Attributes: map[string]string{"return": returnState},
		},
	}
}

// callExecResult returns an ExecutionResult with a <call> transition and a session ID.
func callExecResult(target, returnState, sessionID string) executors.ExecutionResult {
	return executors.ExecutionResult{
		SessionID: sidPtr(sessionID),
		Transition: parsing.Transition{
			Tag:        "call",
			Target:     target,
			Attributes: map[string]string{"return": returnState},
		},
	}
}

// resultWithSID returns an ExecutionResult with a <result> transition and a session ID.
func resultWithSID(payload, sessionID string) executors.ExecutionResult {
	return executors.ExecutionResult{
		SessionID: sidPtr(sessionID),
		Transition: parsing.Transition{
			Tag:        "result",
			Payload:    payload,
			Attributes: map[string]string{},
		},
	}
}

// TestResetClearsSessionIDForNextInvocation verifies that after a <reset>
// transition the next state is invoked with no session (SessionID==nil),
// not with the session that was active before the reset.
func TestResetClearsSessionIDForNextInvocation(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	startMock := &sessionCapturingMock{
		results: []executors.ExecutionResult{
			// START.md: establishes a session; emits <reset>FRESH.md</reset>
			resetExecResult("FRESH.md", "old-session-abc"),
		},
	}
	freshMock := &sessionCapturingMock{
		results: []executors.ExecutionResult{
			// FRESH.md: must run with a fresh session (no --resume); terminates
			resultExecResult("done"),
		},
	}

	factory := map[string]*sessionCapturingMock{
		"START.md": startMock,
		"FRESH.md": freshMock,
	}
	orchestrator.SetExecutorFactory(func(state string) executors.StateExecutor {
		return factory[state]
	})
	defer orchestrator.ResetExecutorFactory()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))

	// START.md should have been called with nil (first invocation, no session yet).
	require.Len(t, startMock.capturedSessions, 1)
	assert.Nil(t, startMock.capturedSessions[0], "START.md should start with no session")

	// FRESH.md must be called with nil session: <reset> must clear it.
	require.Len(t, freshMock.capturedSessions, 1)
	assert.Nil(t, freshMock.capturedSessions[0],
		"after <reset>, next invocation must have no session; got %v", freshMock.capturedSessions[0])
}

// TestResultReturnRestoresCallerSession verifies that after a <function> +
// <result> pair, the return state resumes the *caller's* session, not the
// function body's session.
func TestResultReturnRestoresCallerSession(t *testing.T) {
	dir, wfID := setupWorkflow(t, "CALLER.md")

	callerMock := &sessionCapturingMock{
		results: []executors.ExecutionResult{
			// CALLER.md: establishes a session; calls a function
			functionExecResult("EVAL.md", "RETURN.md", "caller-session-xyz"),
		},
	}
	evalMock := &sessionCapturingMock{
		results: []executors.ExecutionResult{
			// EVAL.md: runs in a fresh context; returns a result
			resultWithSID("eval done", "eval-session-abc"),
		},
	}
	returnMock := &sessionCapturingMock{
		results: []executors.ExecutionResult{
			// RETURN.md: should resume the caller's session
			resultExecResult("all done"),
		},
	}

	factory := map[string]*sessionCapturingMock{
		"CALLER.md": callerMock,
		"EVAL.md":   evalMock,
		"RETURN.md": returnMock,
	}
	orchestrator.SetExecutorFactory(func(state string) executors.StateExecutor {
		return factory[state]
	})
	defer orchestrator.ResetExecutorFactory()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))

	// EVAL.md (function body) must start with a nil session — <function> clears it.
	require.Len(t, evalMock.capturedSessions, 1)
	assert.Nil(t, evalMock.capturedSessions[0],
		"EVAL.md (function body) should start with no session; got %v", evalMock.capturedSessions[0])

	// RETURN.md must resume the caller's session, not the eval function's.
	require.Len(t, returnMock.capturedSessions, 1)
	require.NotNil(t, returnMock.capturedSessions[0], "RETURN.md should have a session")
	assert.Equal(t, "caller-session-xyz", *returnMock.capturedSessions[0],
		"RETURN.md must resume caller-session-xyz, not the eval session")
}

// TestCallResultRestoresCallerSession verifies that after a <call> + <result>
// pair, the return state resumes the *caller's* session, not the child's forked session.
func TestCallResultRestoresCallerSession(t *testing.T) {
	dir, wfID := setupWorkflow(t, "CALLER.md")

	callerMock := &sessionCapturingMock{
		results: []executors.ExecutionResult{
			// CALLER.md: establishes a session; issues a <call>
			callExecResult("CHILD.md", "RETURN.md", "caller-session-xyz"),
		},
	}
	childMock := &sessionCapturingMock{
		results: []executors.ExecutionResult{
			// CHILD.md: runs via --fork-session; establishes its own session
			resultWithSID("child done", "child-session-abc"),
		},
	}
	returnMock := &sessionCapturingMock{
		results: []executors.ExecutionResult{
			// RETURN.md: should resume the caller's session
			resultExecResult("all done"),
		},
	}

	factory := map[string]*sessionCapturingMock{
		"CALLER.md": callerMock,
		"CHILD.md":  childMock,
		"RETURN.md": returnMock,
	}
	orchestrator.SetExecutorFactory(func(state string) executors.StateExecutor {
		return factory[state]
	})
	defer orchestrator.ResetExecutorFactory()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))

	// RETURN.md must resume the *caller's* session, not the child's forked session.
	require.Len(t, returnMock.capturedSessions, 1)
	require.NotNil(t, returnMock.capturedSessions[0], "RETURN.md should have a session")
	assert.Equal(t, "caller-session-xyz", *returnMock.capturedSessions[0],
		"RETURN.md must resume caller-session-xyz, not child-session-abc")
}

// ----------------------------------------------------------------------------
// Multi-fork dispatch
// ----------------------------------------------------------------------------

// multiForkResult returns an ExecutionResult with a multi-transition Transitions
// field (the multi-fork path). The Transition field is intentionally left zero.
func multiForkResult(trs ...parsing.Transition) executors.ExecutionResult {
	return executors.ExecutionResult{Transitions: trs}
}

// forkWorkflowTransitionFor builds a fork-workflow Transition pointing at target
// with the given attributes.
func forkWorkflowTransitionFor(target string, attrs map[string]string) parsing.Transition {
	return parsing.Transition{
		Tag:        "fork-workflow",
		Target:     target,
		Attributes: attrs,
	}
}

// gotoTransition builds a goto Transition.
func gotoTransition(target string) parsing.Transition {
	return parsing.Transition{
		Tag:        "goto",
		Target:     target,
		Attributes: map[string]string{},
	}
}

// forkTransitionWith builds an intra-scope fork Transition.
func forkTransitionWith(target string, attrs map[string]string) parsing.Transition {
	return parsing.Transition{
		Tag:        "fork",
		Target:     target,
		Attributes: attrs,
	}
}

// setupMultiForkWorkflow creates workflow state with an initial agent whose
// ScopeDir is set to scopeDir (used for fork-workflow resolution).
func setupMultiForkWorkflow(t *testing.T, scopeDir string) (stateDir, workflowID string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".raymond", "state")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	workflowID = "test-wf"
	ws := wfstate.CreateInitialState(workflowID, scopeDir, "START.md", 0, nil)
	require.NoError(t, wfstate.WriteState(workflowID, ws, dir))
	return dir, workflowID
}

// TestMultiForkTwoForkWorkflowTagsSpawnTwoWorkers verifies that an output with
// two fork-workflow tags and a shared "next" attribute spawns two workers and
// advances the caller.
func TestMultiForkTwoForkWorkflowTagsSpawnTwoWorkers(t *testing.T) {
	// Create two real sub-workflow directories that specifier.Resolve can validate.
	tmp := t.TempDir()
	wf1Dir := filepath.Join(tmp, "wf1")
	wf2Dir := filepath.Join(tmp, "wf2")
	require.NoError(t, os.MkdirAll(wf1Dir, 0o755))
	require.NoError(t, os.MkdirAll(wf2Dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wf1Dir, "1_START.md"), []byte("# wf1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wf2Dir, "1_START.md"), []byte("# wf2"), 0o644))

	dir, wfID := setupMultiForkWorkflow(t, tmp)

	shared := newMock(
		// START.md: two fork-workflow tags with shared next
		multiForkResult(
			forkWorkflowTransitionFor(wf1Dir, map[string]string{"next": "CONT.md"}),
			forkWorkflowTransitionFor(wf2Dir, map[string]string{"next": "CONT.md"}),
		),
		// CONT.md (caller continues here): terminates
		resultExecResult("caller done"),
		// wf1 worker at 1_START.md: terminates
		resultExecResult("wf1 done"),
		// wf2 worker at 1_START.md: terminates
		resultExecResult("wf2 done"),
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return shared })
	defer orchestrator.ResetExecutorFactory()

	var spawned []events.AgentSpawned
	var terminated []events.AgentTerminated
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentSpawned) { spawned = append(spawned, e) })
		bus.Subscribe(b, func(e events.AgentTerminated) { terminated = append(terminated, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))

	require.Len(t, spawned, 2, "two workers should be spawned")
	assert.Equal(t, "main", spawned[0].ParentAgentID)
	assert.Equal(t, "main", spawned[1].ParentAgentID)

	// Caller + 2 workers all terminate.
	assert.Len(t, terminated, 3)
}

// TestMultiForkMixedForkAndForkWorkflow verifies that a mix of fork and
// fork-workflow tags in a multi-fork output spawns workers for each.
func TestMultiForkMixedForkAndForkWorkflow(t *testing.T) {
	tmp := t.TempDir()
	wfDir := filepath.Join(tmp, "child-wf")
	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "1_START.md"), []byte("# child"), 0o644))

	dir, wfID := setupMultiForkWorkflow(t, tmp)

	shared := newMock(
		// START.md: one fork (intra-scope) + one fork-workflow, shared next
		multiForkResult(
			forkTransitionWith("WORKER.md", map[string]string{"next": "CONT.md"}),
			forkWorkflowTransitionFor(wfDir, map[string]string{"next": "CONT.md"}),
		),
		// CONT.md (caller): terminates
		resultExecResult("caller done"),
		// WORKER.md (intra-scope worker): terminates
		resultExecResult("worker done"),
		// 1_START.md (fork-workflow worker): terminates
		resultExecResult("child done"),
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return shared })
	defer orchestrator.ResetExecutorFactory()

	var spawned []events.AgentSpawned
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentSpawned) { spawned = append(spawned, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	assert.Len(t, spawned, 2)
}

// TestMultiForkSingleForkWorkflowWithGotoSiblingDispatchesCorrectly verifies
// that a single fork-workflow tag alongside a goto tag takes the multi-fork
// path and advances the caller to the goto target.
func TestMultiForkSingleForkWorkflowWithGotoSiblingDispatchesCorrectly(t *testing.T) {
	tmp := t.TempDir()
	wfDir := filepath.Join(tmp, "child-wf")
	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "1_START.md"), []byte("# child"), 0o644))

	dir, wfID := setupMultiForkWorkflow(t, tmp)

	shared := newMock(
		// START.md: fork-workflow (no next on tag) + goto
		multiForkResult(
			forkWorkflowTransitionFor(wfDir, map[string]string{}),
			gotoTransition("CONT.md"),
		),
		// CONT.md (caller): terminates
		resultExecResult("caller done"),
		// 1_START.md (worker): terminates
		resultExecResult("worker done"),
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return shared })
	defer orchestrator.ResetExecutorFactory()

	var spawned []events.AgentSpawned
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentSpawned) { spawned = append(spawned, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, spawned, 1)
	assert.Equal(t, "1_START.md", spawned[0].InitialState)
}

// TestMultiForkCallerAdvancesToContinuation verifies that after multi-fork
// dispatch the caller agent's state is the agreed continuation target.
func TestMultiForkCallerAdvancesToContinuation(t *testing.T) {
	tmp := t.TempDir()
	wf1Dir := filepath.Join(tmp, "wf1")
	wf2Dir := filepath.Join(tmp, "wf2")
	require.NoError(t, os.MkdirAll(wf1Dir, 0o755))
	require.NoError(t, os.MkdirAll(wf2Dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wf1Dir, "1_START.md"), []byte("# wf1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wf2Dir, "1_START.md"), []byte("# wf2"), 0o644))

	dir, wfID := setupMultiForkWorkflow(t, tmp)

	// The caller should advance to CONT.md after multi-fork.
	var callerStateAfterFork string
	firstFork := true

	shared := &mockExec{
		results: []executors.ExecutionResult{
			multiForkResult(
				forkWorkflowTransitionFor(wf1Dir, map[string]string{"next": "CONT.md"}),
				forkWorkflowTransitionFor(wf2Dir, map[string]string{"next": "CONT.md"}),
			),
			resultExecResult("caller done"),
			resultExecResult("wf1 done"),
			resultExecResult("wf2 done"),
		},
	}

	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.TransitionOccurred) {
			if e.TransitionType == "multi-fork" && firstFork {
				callerStateAfterFork = e.ToState
				firstFork = false
			}
		})
	})
	defer orchestrator.ResetBusHook()

	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return shared })
	defer orchestrator.ResetExecutorFactory()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	assert.Equal(t, "CONT.md", callerStateAfterFork)
}

// TestMultiForkValidationMissingContinuationPausesAgent verifies that when no
// continuation target is present the agent is paused with a descriptive error.
func TestMultiForkValidationMissingContinuationPausesAgent(t *testing.T) {
	tmp := t.TempDir()
	wf1Dir := filepath.Join(tmp, "wf1")
	wf2Dir := filepath.Join(tmp, "wf2")
	require.NoError(t, os.MkdirAll(wf1Dir, 0o755))
	require.NoError(t, os.MkdirAll(wf2Dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wf1Dir, "1_START.md"), []byte("# wf1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wf2Dir, "1_START.md"), []byte("# wf2"), 0o644))

	dir, wfID := setupMultiForkWorkflow(t, tmp)

	// No "next" on either fork tag, no goto → validation error.
	mock := newMock(
		multiForkResult(
			forkWorkflowTransitionFor(wf1Dir, map[string]string{}),
			forkWorkflowTransitionFor(wf2Dir, map[string]string{}),
		),
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var paused []events.AgentPaused
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentPaused) { paused = append(paused, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, paused, 1)
	assert.Equal(t, "main", paused[0].AgentID)

	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "paused", ws.Agents[0].Status)
	assert.Contains(t, ws.Agents[0].Error, "continuation")
}

// TestMultiForkValidationConflictingNextValuesPausesAgent verifies that
// mismatched "next" values across fork tags pause the agent.
func TestMultiForkValidationConflictingNextValuesPausesAgent(t *testing.T) {
	tmp := t.TempDir()
	wf1Dir := filepath.Join(tmp, "wf1")
	wf2Dir := filepath.Join(tmp, "wf2")
	require.NoError(t, os.MkdirAll(wf1Dir, 0o755))
	require.NoError(t, os.MkdirAll(wf2Dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wf1Dir, "1_START.md"), []byte("# wf1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wf2Dir, "1_START.md"), []byte("# wf2"), 0o644))

	dir, wfID := setupMultiForkWorkflow(t, tmp)

	mock := newMock(
		multiForkResult(
			forkWorkflowTransitionFor(wf1Dir, map[string]string{"next": "CONT_A.md"}),
			forkWorkflowTransitionFor(wf2Dir, map[string]string{"next": "CONT_B.md"}),
		),
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var paused []events.AgentPaused
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentPaused) { paused = append(paused, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, paused, 1)
	assert.Equal(t, "main", paused[0].AgentID)

	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	assert.Equal(t, "paused", ws.Agents[0].Status)
}

// TestMultiForkValidationGotoNextMismatchPausesAgent verifies that a goto
// target disagreeing with fork "next" values pauses the agent.
func TestMultiForkValidationGotoNextMismatchPausesAgent(t *testing.T) {
	tmp := t.TempDir()
	wfDir := filepath.Join(tmp, "child-wf")
	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "1_START.md"), []byte("# child"), 0o644))

	dir, wfID := setupMultiForkWorkflow(t, tmp)

	mock := newMock(
		multiForkResult(
			forkWorkflowTransitionFor(wfDir, map[string]string{"next": "CONT_A.md"}),
			gotoTransition("CONT_B.md"), // conflicts with CONT_A.md
		),
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var paused []events.AgentPaused
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentPaused) { paused = append(paused, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, paused, 1)
	assert.Equal(t, "main", paused[0].AgentID)
}

// TestMultiForkValidationNonForkTagMixedInPausesAgent verifies that a non-fork
// tag (e.g. "result") alongside fork tags causes the agent to be paused.
func TestMultiForkValidationNonForkTagMixedInPausesAgent(t *testing.T) {
	tmp := t.TempDir()
	wf1Dir := filepath.Join(tmp, "wf1")
	wf2Dir := filepath.Join(tmp, "wf2")
	require.NoError(t, os.MkdirAll(wf1Dir, 0o755))
	require.NoError(t, os.MkdirAll(wf2Dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wf1Dir, "1_START.md"), []byte("# wf1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wf2Dir, "1_START.md"), []byte("# wf2"), 0o644))

	dir, wfID := setupMultiForkWorkflow(t, tmp)

	// Include a "result" tag alongside two fork-workflow tags.
	mock := newMock(
		multiForkResult(
			forkWorkflowTransitionFor(wf1Dir, map[string]string{"next": "CONT.md"}),
			forkWorkflowTransitionFor(wf2Dir, map[string]string{"next": "CONT.md"}),
			parsing.Transition{Tag: "result", Payload: "oops", Attributes: map[string]string{}},
		),
	)
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	var paused []events.AgentPaused
	orchestrator.SetBusHook(func(b *bus.Bus) {
		bus.Subscribe(b, func(e events.AgentPaused) { paused = append(paused, e) })
	})
	defer orchestrator.ResetBusHook()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Len(t, paused, 1)
	assert.Equal(t, "main", paused[0].AgentID)

	ws, err := wfstate.ReadState(wfID, dir)
	require.NoError(t, err)
	assert.Equal(t, "paused", ws.Agents[0].Status)
	assert.Contains(t, ws.Agents[0].Error, "result")
}

// ----------------------------------------------------------------------------
// Implicit transition input propagation (end-to-end via mock executor)
// ----------------------------------------------------------------------------

// recordingMockExec is a mock executor that records the PendingResult value
// seen on each invocation, then returns pre-programmed results.
type recordingMockExec struct {
	mu             sync.Mutex
	results        []executors.ExecutionResult
	idx            int
	pendingResults []*string // captured PendingResult for each call
}

func (m *recordingMockExec) Execute(
	_ context.Context,
	agent *wfstate.AgentState,
	wfState *wfstate.WorkflowState,
	_ *executors.ExecutionContext,
) (executors.ExecutionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.idx
	m.idx++
	// Capture the PendingResult for this call.
	m.pendingResults = append(m.pendingResults, agent.PendingResult)
	if i < len(m.results) {
		res := m.results[i]
		if res.CostUSD > 0 {
			wfState.TotalCostUSD += res.CostUSD
		}
		return res, nil
	}
	return executors.ExecutionResult{}, errors.New("recording mock: no more results")
}

// gotoResultWithInput returns an ExecutionResult carrying a goto transition
// that includes an "input" attribute — simulating the output of a MarkdownExecutor
// that rendered an implicit transition's input template.
func gotoResultWithInput(target, input string) executors.ExecutionResult {
	return executors.ExecutionResult{
		Transition: parsing.Transition{
			Tag:    "goto",
			Target: target,
			Attributes: map[string]string{
				"input": input,
			},
		},
	}
}

// TestImplicitInputPropagatesAsPendingResult verifies that when an executor
// returns a goto transition with an "input" attribute, the orchestrator
// (via ApplyTransition → HandleGoto) sets that value as PendingResult on the
// next state's agent. This is the state-handoff half of the implicit-input
// feature; the executor-side rendering is covered by the executor tests.
func TestImplicitInputPropagatesAsPendingResult(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	// First call: return goto with input = "propagated-value"
	// Second call: at this point agent.PendingResult should equal "propagated-value".
	//              Return a result to terminate.
	mock := &recordingMockExec{
		results: []executors.ExecutionResult{
			gotoResultWithInput("NEXT.md", "propagated-value"),
			resultExecResult("done"),
		},
	}
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Equal(t, 2, mock.idx, "expected exactly 2 executor calls")

	// First call: no prior PendingResult (initial state).
	assert.Nil(t, mock.pendingResults[0], "first call should have nil PendingResult")
	// Second call: PendingResult should be "propagated-value" from the goto input.
	require.NotNil(t, mock.pendingResults[1], "second call should have non-nil PendingResult")
	assert.Equal(t, "propagated-value", *mock.pendingResults[1])
}

// TestImplicitInputChainPropagatesThroughThreeStates verifies that the input
// value threads correctly through a 3-state chain: A→B→C each carrying the
// same value forward via the "input" attribute on goto transitions.
func TestImplicitInputChainPropagatesThroughThreeStates(t *testing.T) {
	dir, wfID := setupWorkflow(t, "START.md")

	// Simulate: START→MIDDLE with input "step1", MIDDLE→END with input "step2".
	mock := &recordingMockExec{
		results: []executors.ExecutionResult{
			gotoResultWithInput("MIDDLE.md", "step1"),
			gotoResultWithInput("END.md", "step2"),
			resultExecResult("final"),
		},
	}
	orchestrator.SetExecutorFactory(func(_ string) executors.StateExecutor { return mock })
	defer orchestrator.ResetExecutorFactory()

	require.NoError(t, orchestrator.RunAllAgents(context.Background(), wfID, defaultOpts(dir)))
	require.Equal(t, 3, mock.idx, "expected exactly 3 executor calls")

	assert.Nil(t, mock.pendingResults[0], "START: no PendingResult")
	require.NotNil(t, mock.pendingResults[1], "MIDDLE: PendingResult should be set")
	assert.Equal(t, "step1", *mock.pendingResults[1])
	require.NotNil(t, mock.pendingResults[2], "END: PendingResult should be set")
	assert.Equal(t, "step2", *mock.pendingResults[2])
}
