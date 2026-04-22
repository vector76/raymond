package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/orchestrator"
	wfstate "github.com/vector76/raymond/internal/state"
)

// fakeOrchestrator implements the Orchestrator interface for tests.
// It captures ObserverSetup so tests can emit events, and blocks until
// the context is cancelled or the done channel is closed.
type fakeOrchestrator struct {
	mu        sync.Mutex
	calls     []fakeCall
	behaviour func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error
}

type fakeCall struct {
	WorkflowID string
	Opts       orchestrator.RunOptions
}

func (f *fakeOrchestrator) RunAllAgents(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{WorkflowID: workflowID, Opts: opts})
	behaviour := f.behaviour
	f.mu.Unlock()

	if behaviour != nil {
		return behaviour(ctx, workflowID, opts)
	}
	// Default: block until context cancelled.
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeOrchestrator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// testWorkflowEntry returns a WorkflowEntry suitable for testing. It creates
// a START.md file in scopeDir so that ResolveEntryPoint succeeds.
func testWorkflowEntry(t *testing.T, scopeDir string) WorkflowEntry {
	t.Helper()
	// ResolveEntryPoint needs an actual state file to exist.
	startFile := filepath.Join(scopeDir, "START.md")
	if err := os.WriteFile(startFile, []byte("# Start\nDo something."), 0o644); err != nil {
		t.Fatalf("write START.md: %v", err)
	}
	return WorkflowEntry{
		ID:            "test-workflow",
		Name:          "Test Workflow",
		Description:   "A test workflow",
		DefaultBudget: 5.0,
		ScopeDir:      scopeDir,
	}
}

// ensureStateDir creates the state directory inside a temp dir and returns
// the state dir path.
func ensureStateDir(t *testing.T) string {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), ".raymond", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	return stateDir
}

func TestLaunchRun_CreatesRunAndReturnsID(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{}
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := rm.LaunchRun(ctx, testWorkflowEntry(t, scopeDir), "hello", 5.0, "sonnet", "", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, runID)

	// The orchestrator should have been called.
	// Give the goroutine a moment to start.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, fake.callCount())
}

func TestGetRun_ReturnsCorrectStatusAfterLaunch(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{} // blocks forever
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := rm.LaunchRun(ctx, testWorkflowEntry(t, scopeDir), "input", 5.0, "", "", nil)
	require.NoError(t, err)

	info, ok := rm.GetRun(runID)
	require.True(t, ok)
	assert.Equal(t, RunStatusRunning, info.Status)
	assert.Equal(t, "test-workflow", info.WorkflowID)
	assert.Equal(t, runID, info.RunID)
	require.Len(t, info.Agents, 1)
	assert.Equal(t, "main", info.Agents[0].ID)
	assert.Equal(t, "START.md", info.Agents[0].CurrentState)
}

func TestGetRun_NotFound(t *testing.T) {
	stateDir := ensureStateDir(t)
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	_, ok := rm.GetRun("nonexistent")
	assert.False(t, ok)
}

func TestCancelRun_CancelsRunningWorkflow(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{} // blocks until ctx cancelled
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx := context.Background()
	runID, err := rm.LaunchRun(ctx, testWorkflowEntry(t, scopeDir), "", 5.0, "", "", nil)
	require.NoError(t, err)

	// Let the goroutine start.
	time.Sleep(50 * time.Millisecond)

	err = rm.CancelRun(runID)
	require.NoError(t, err)

	info, ok := rm.GetRun(runID)
	require.True(t, ok)
	assert.Equal(t, RunStatusCancelled, info.Status)
}

func TestCancelRun_AwaitingRun(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)
			b.Emit(events.AgentAwaitStarted{
				AgentID:   "main",
				InputID:   "input-1",
				Prompt:    "What next?",
				NextState: "NEXT.md",
				Timestamp: time.Now(),
			})
			b.Emit(events.WorkflowPaused{
				WorkflowID:       workflowID,
				TotalCostUSD:     0.1,
				PausedAgentCount: 1,
				Timestamp:        time.Now(),
			})
			return nil
		},
	}
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", "", nil)
	require.NoError(t, err)

	// Wait until the run reaches awaiting_input.
	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)

	info, ok := rm.GetRun(runID)
	require.True(t, ok)
	assert.Equal(t, RunStatusAwaitingInput, info.Status)

	// Cancelling an awaiting run should succeed and change status.
	err = rm.CancelRun(runID)
	require.NoError(t, err)

	info, ok = rm.GetRun(runID)
	require.True(t, ok)
	assert.Equal(t, RunStatusCancelled, info.Status)
}

func TestCancelRun_NotFound(t *testing.T) {
	stateDir := ensureStateDir(t)
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	err = rm.CancelRun("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCancelRun_AlreadyTerminal(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	// Orchestrator that completes immediately with success.
	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			// Emit WorkflowCompleted to set terminal status.
			b := bus.New()
			opts.ObserverSetup(b)
			b.Emit(events.WorkflowCompleted{
				WorkflowID:   workflowID,
				TotalCostUSD: 1.0,
				Timestamp:    time.Now(),
			})
			return nil
		},
	}
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", "", nil)
	require.NoError(t, err)

	// Wait for the run to reach terminal state.
	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)

	err = rm.CancelRun(runID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "terminal state")
}

func TestWaitForCompletion_BlocksUntilDone(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)

			// Simulate some work.
			b.Emit(events.StateStarted{
				AgentID:   "main",
				StateName: "START.md",
				StateType: "markdown",
				Timestamp: time.Now(),
			})
			b.Emit(events.StateCompleted{
				AgentID:      "main",
				StateName:    "START.md",
				CostUSD:      0.5,
				TotalCostUSD: 0.5,
				Timestamp:    time.Now(),
			})
			b.Emit(events.AgentTerminated{
				AgentID:       "main",
				ResultPayload: "task done",
				Timestamp:     time.Now(),
			})
			b.Emit(events.WorkflowCompleted{
				WorkflowID:   workflowID,
				TotalCostUSD: 0.5,
				Timestamp:    time.Now(),
			})
			return nil
		},
	}

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "go", 5.0, "", "", nil)
	require.NoError(t, err)

	info, err := rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, RunStatusCompleted, info.Status)
	assert.Equal(t, 0.5, info.CostUSD)
	assert.Equal(t, "task done", info.Result)
}

func TestWaitForCompletion_Timeout(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{} // blocks forever
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := rm.LaunchRun(ctx, testWorkflowEntry(t, scopeDir), "", 5.0, "", "", nil)
	require.NoError(t, err)

	_, err = rm.WaitForCompletion(runID, 100*time.Millisecond)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestWaitForCompletion_NotFound(t *testing.T) {
	stateDir := ensureStateDir(t)
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	_, err = rm.WaitForCompletion("nonexistent", time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestConcurrentRuns_DoNotInterfere(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	var mu sync.Mutex
	runningConcurrently := 0
	maxConcurrent := 0

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			mu.Lock()
			runningConcurrently++
			if runningConcurrently > maxConcurrent {
				maxConcurrent = runningConcurrently
			}
			mu.Unlock()

			// Give time for both goroutines to be running.
			time.Sleep(100 * time.Millisecond)

			b := bus.New()
			opts.ObserverSetup(b)
			b.Emit(events.WorkflowCompleted{
				WorkflowID:   workflowID,
				TotalCostUSD: 1.0,
				Timestamp:    time.Now(),
			})

			mu.Lock()
			runningConcurrently--
			mu.Unlock()
			return nil
		},
	}

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	entry := testWorkflowEntry(t, scopeDir)
	ctx := context.Background()

	id1, err := rm.LaunchRun(ctx, entry, "run1", 5.0, "", "", nil)
	require.NoError(t, err)
	id2, err := rm.LaunchRun(ctx, entry, "run2", 5.0, "", "", nil)
	require.NoError(t, err)

	assert.NotEqual(t, id1, id2, "run IDs must be unique")

	info1, err := rm.WaitForCompletion(id1, 5*time.Second)
	require.NoError(t, err)
	info2, err := rm.WaitForCompletion(id2, 5*time.Second)
	require.NoError(t, err)

	assert.Equal(t, RunStatusCompleted, info1.Status)
	assert.Equal(t, RunStatusCompleted, info2.Status)

	mu.Lock()
	assert.Equal(t, 2, maxConcurrent, "both runs should execute concurrently")
	mu.Unlock()
}

func TestListRuns_ReturnsAllRuns(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)
			b.Emit(events.WorkflowCompleted{
				WorkflowID:   workflowID,
				TotalCostUSD: 1.0,
				Timestamp:    time.Now(),
			})
			return nil
		},
	}

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	entry := testWorkflowEntry(t, scopeDir)
	ctx := context.Background()

	id1, err := rm.LaunchRun(ctx, entry, "", 5.0, "", "", nil)
	require.NoError(t, err)
	id2, err := rm.LaunchRun(ctx, entry, "", 5.0, "", "", nil)
	require.NoError(t, err)

	// Wait for both to finish.
	_, err = rm.WaitForCompletion(id1, 5*time.Second)
	require.NoError(t, err)
	_, err = rm.WaitForCompletion(id2, 5*time.Second)
	require.NoError(t, err)

	runs := rm.ListRuns()
	assert.Len(t, runs, 2)

	ids := map[string]bool{}
	for _, r := range runs {
		ids[r.RunID] = true
		assert.Equal(t, RunStatusCompleted, r.Status)
	}
	assert.True(t, ids[id1])
	assert.True(t, ids[id2])
}

func TestRestartRecovery_DiscoversInProgressWorkflows(t *testing.T) {
	stateDir := ensureStateDir(t)

	// Write a state file that looks like an in-progress workflow.
	ws := &wfstate.WorkflowState{
		WorkflowID:   "recovered-run-1",
		ScopeDir:     "/some/scope",
		TotalCostUSD: 2.5,
		BudgetUSD:    10.0,
		Agents: []wfstate.AgentState{
			{
				ID:           "main",
				CurrentState: "PROCESS.md",
				Status:       wfstate.AgentStatusPaused,
				Stack:        []wfstate.StackFrame{},
			},
		},
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "recovered-run-1.json"),
		data, 0o644,
	))

	// Create a RunManager — it should discover the state file.
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	info, ok := rm.GetRun("recovered-run-1")
	require.True(t, ok)
	assert.Equal(t, RunStatusFailed, info.Status, "paused agents without await should be classified as failed")
	assert.Equal(t, 2.5, info.CostUSD)
	require.Len(t, info.Agents, 1)
	assert.Equal(t, "main", info.Agents[0].ID)
	assert.Equal(t, "PROCESS.md", info.Agents[0].CurrentState)
	assert.Equal(t, wfstate.AgentStatusPaused, info.Agents[0].Status)
}

func TestRestartRecovery_AwaitingWorkflow(t *testing.T) {
	stateDir := ensureStateDir(t)

	ws := &wfstate.WorkflowState{
		WorkflowID:   "awaiting-run",
		ScopeDir:     "/some/scope",
		TotalCostUSD: 1.0,
		BudgetUSD:    10.0,
		Agents: []wfstate.AgentState{
			{
				ID:           "main",
				CurrentState: "WAIT.md",
				Status:       wfstate.AgentStatusAwaiting,
				Stack:        []wfstate.StackFrame{},
			},
		},
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "awaiting-run.json"),
		data, 0o644,
	))

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	info, ok := rm.GetRun("awaiting-run")
	require.True(t, ok)
	assert.Equal(t, RunStatusAwaitingInput, info.Status)
}

func TestRestartRecovery_EmptyStateDir(t *testing.T) {
	stateDir := ensureStateDir(t)

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	runs := rm.ListRuns()
	assert.Empty(t, runs)
}

func TestRestartRecovery_RecoveredRunDoneChannelClosed(t *testing.T) {
	stateDir := ensureStateDir(t)

	ws := &wfstate.WorkflowState{
		WorkflowID: "old-run",
		ScopeDir:   "/scope",
		Agents: []wfstate.AgentState{
			{ID: "main", CurrentState: "S.md", Status: wfstate.AgentStatusPaused, Stack: []wfstate.StackFrame{}},
		},
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "old-run.json"), data, 0o644))

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	// WaitForCompletion on a recovered run should return immediately since
	// the done channel is already closed.
	info, err := rm.WaitForCompletion("old-run", time.Second)
	require.NoError(t, err)
	assert.Equal(t, RunStatusFailed, info.Status)
}

func TestLaunchRun_FailedOrchestrator(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			return assert.AnError
		},
	}
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", "", nil)
	require.NoError(t, err)

	info, err := rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, RunStatusFailed, info.Status)
	assert.Contains(t, info.Result, "assert.AnError")
}

func TestLaunchRun_AwaitingInputStatus(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)

			b.Emit(events.AgentAwaitStarted{
				AgentID:   "main",
				InputID:   "input-1",
				Prompt:    "What next?",
				NextState: "NEXT.md",
				Timestamp: time.Now(),
			})
			b.Emit(events.WorkflowPaused{
				WorkflowID:       workflowID,
				TotalCostUSD:     0.3,
				PausedAgentCount: 1,
				Timestamp:        time.Now(),
			})
			return nil
		},
	}

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", "", nil)
	require.NoError(t, err)

	info, err := rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, RunStatusAwaitingInput, info.Status)
	assert.Equal(t, 0.3, info.CostUSD)

	// Verify agent-level status.
	require.Len(t, info.Agents, 1)
	assert.Equal(t, wfstate.AgentStatusAwaiting, info.Agents[0].Status)
}

func TestLaunchRun_EventsUpdateAgentInfo(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)

			// Simulate agent executing states and spawning a worker.
			b.Emit(events.StateStarted{
				AgentID:   "main",
				StateName: "PROCESS.md",
				StateType: "markdown",
				Timestamp: time.Now(),
			})
			b.Emit(events.AgentSpawned{
				ParentAgentID: "main",
				NewAgentID:    "fork-1",
				InitialState:  "WORKER.md",
				Timestamp:     time.Now(),
			})
			b.Emit(events.StateCompleted{
				AgentID:      "main",
				StateName:    "PROCESS.md",
				CostUSD:      0.2,
				TotalCostUSD: 0.2,
				Timestamp:    time.Now(),
			})
			b.Emit(events.AgentTerminated{
				AgentID:       "fork-1",
				ResultPayload: "worker done",
				Timestamp:     time.Now(),
			})
			b.Emit(events.AgentTerminated{
				AgentID:       "main",
				ResultPayload: "all done",
				Timestamp:     time.Now(),
			})
			b.Emit(events.WorkflowCompleted{
				WorkflowID:   workflowID,
				TotalCostUSD: 0.2,
				Timestamp:    time.Now(),
			})
			return nil
		},
	}

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", "", nil)
	require.NoError(t, err)

	info, err := rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)

	assert.Equal(t, RunStatusCompleted, info.Status)
	assert.Equal(t, "all done", info.Result)
	require.Len(t, info.Agents, 2)

	// Main agent was updated.
	assert.Equal(t, "main", info.Agents[0].ID)
	assert.Equal(t, "PROCESS.md", info.Agents[0].CurrentState)
	assert.Equal(t, "terminated", info.Agents[0].Status)

	// Fork agent was tracked.
	assert.Equal(t, "fork-1", info.Agents[1].ID)
	assert.Equal(t, "WORKER.md", info.Agents[1].CurrentState)
	assert.Equal(t, "terminated", info.Agents[1].Status)
}
