package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/orchestrator"
	"github.com/vector76/raymond/internal/specifier"
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

// TestLaunchRun_YamlScopeFromRegistry is an end-to-end smoke test that pins the
// registry→run-manager wiring for YAML scopes with embedded manifests. It
// guards against regressions in the Phase 5 assumption that downstream
// integration (LaunchRun → specifier.ResolveEntryPoint) is a no-op for YAML
// scope paths.
func TestLaunchRun_YamlScopeFromRegistry(t *testing.T) {
	root := t.TempDir()
	yamlPath := filepath.Join(root, "review.yaml")
	yamlContent := `id: smoke-yaml
name: Smoke YAML
description: End-to-end smoke test workflow

states:
  1_START:
    prompt: |
      Do the work.
    allowed_transitions:
      - { tag: result }
`
	require.NoError(t, os.WriteFile(yamlPath, []byte(yamlContent), 0o644))

	registry, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := registry.GetWorkflow("smoke-yaml")
	require.True(t, ok, "registry should discover YAML workflow with embedded manifest")
	assert.Equal(t, yamlPath, entry.ScopeDir)
	assert.Equal(t, yamlPath, entry.ManifestPath)

	// Sanity-check that the specifier layer can resolve the discovered scope
	// to a valid entry point — hardens against specifier-layer regressions.
	res, err := specifier.Resolve(entry.ScopeDir, "")
	require.NoError(t, err)
	assert.NotEmpty(t, res.EntryPoint)

	stateDir := ensureStateDir(t)
	fake := &fakeOrchestrator{}
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := rm.LaunchRun(ctx, *entry, "hello", 5.0, "", "", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, runID)
}

// ----------------------------------------------------------------------------
// parseRunIDTimestamp
// ----------------------------------------------------------------------------

func TestParseRunIDTimestampCanonical(t *testing.T) {
	got, ok := parseRunIDTimestamp("workflow_2026-04-23_18-37-29-948850")
	require.True(t, ok)
	want := time.Date(2026, 4, 23, 18, 37, 29, 948850*1000, time.Local)
	assert.True(t, got.Equal(want), "got %v want %v", got, want)
}

func TestParseRunIDTimestampWithCounter(t *testing.T) {
	got, ok := parseRunIDTimestamp("workflow_2026-04-23_18-37-29-948850_2")
	require.True(t, ok)
	want := time.Date(2026, 4, 23, 18, 37, 29, 948850*1000, time.Local)
	assert.True(t, got.Equal(want), "got %v want %v", got, want)
}

func TestParseRunIDTimestampMissingPrefix(t *testing.T) {
	_, ok := parseRunIDTimestamp("custom-id-no-prefix")
	assert.False(t, ok)
}

func TestParseRunIDTimestampGarbage(t *testing.T) {
	_, ok := parseRunIDTimestamp("workflow_not-a-date")
	assert.False(t, ok)
}

func TestParseRunIDTimestampNonNumericMicros(t *testing.T) {
	_, ok := parseRunIDTimestamp("workflow_2026-04-23_18-37-29-XXXXXX")
	assert.False(t, ok)
}

// Format guarantees micros are at most 6 digits. Reject larger values so a
// hand-crafted id can't silently overflow into the seconds field via t.Add.
func TestParseRunIDTimestampMicrosTooLarge(t *testing.T) {
	_, ok := parseRunIDTimestamp("workflow_2026-04-23_18-37-29-9999999")
	assert.False(t, ok)
}

func TestParseRunIDTimestampOrdering(t *testing.T) {
	// Stable ordering: an earlier ID parses to an earlier time, even when
	// they share the same date prefix that the previous truncation collided on.
	earlier, ok := parseRunIDTimestamp("workflow_2026-04-23_18-37-29-100000")
	require.True(t, ok)
	later, ok := parseRunIDTimestamp("workflow_2026-04-23_18-37-29-200000")
	require.True(t, ok)
	assert.True(t, earlier.Before(later))
}

// TestRestartRecovery_PrefersPersistedStartedAt verifies that recoverRuns
// uses ws.StartedAt when present, ignoring the run_id timestamp. This is
// the timezone-safe path: the persisted moment round-trips exactly, while
// parseRunIDTimestamp re-interprets the run_id digits in the recovering
// process's local timezone and would drift if that changed.
func TestRestartRecovery_PrefersPersistedStartedAt(t *testing.T) {
	stateDir := ensureStateDir(t)

	// Use a launch time well after the timestamp encoded in the run_id so
	// a regression that falls back to parseRunIDTimestamp is detectable.
	persisted := time.Date(2030, 7, 15, 12, 34, 56, 789000000, time.UTC)
	ws := &wfstate.WorkflowState{
		WorkflowID: "workflow_2026-04-23_18-37-29-948850",
		ScopeDir:   "/some/scope",
		StartedAt:  persisted,
		Agents: []wfstate.AgentState{
			{ID: "main", CurrentState: "X.md", Status: wfstate.AgentStatusPaused, Stack: []wfstate.StackFrame{}},
		},
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, ws.WorkflowID+".json"),
		data, 0o644,
	))

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	info, ok := rm.GetRun(ws.WorkflowID)
	require.True(t, ok)
	assert.True(t, info.StartedAt.Equal(persisted),
		"recoverRuns should prefer persisted StartedAt; got %v want %v",
		info.StartedAt, persisted)
}

// TestRestartRecovery_FallsBackToRunIDTimestamp verifies that recoverRuns
// falls back to parsing the run_id when ws.StartedAt is the zero time
// (state files written before the field existed).
func TestRestartRecovery_FallsBackToRunIDTimestamp(t *testing.T) {
	stateDir := ensureStateDir(t)

	ws := &wfstate.WorkflowState{
		WorkflowID: "workflow_2026-04-23_18-37-29-948850",
		ScopeDir:   "/some/scope",
		// StartedAt left as zero (legacy state file).
		Agents: []wfstate.AgentState{
			{ID: "main", CurrentState: "X.md", Status: wfstate.AgentStatusPaused, Stack: []wfstate.StackFrame{}},
		},
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, ws.WorkflowID+".json"),
		data, 0o644,
	))

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	info, ok := rm.GetRun(ws.WorkflowID)
	require.True(t, ok)
	require.False(t, info.StartedAt.IsZero(), "StartedAt should be parsed from the run_id")
	want, parsedOK := parseRunIDTimestamp(ws.WorkflowID)
	require.True(t, parsedOK)
	assert.True(t, info.StartedAt.Equal(want))
}

// TestSubscribeRunEvents_DeliversLiveEventsAfterReplay verifies that a
// subscriber attached mid-run receives the replay snapshot AND any events
// that arrive after subscription. Earlier tests subscribed only after the
// run completed (replay-only path); this exercises the fan-out path.
func TestSubscribeRunEvents_DeliversLiveEventsAfterReplay(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	// release lets the test trigger the orchestrator to emit a "live" event
	// (one observed after the subscription is established).
	release := make(chan struct{})
	exit := make(chan struct{})

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)
			// Pre-subscription event: ends up in the replay buffer.
			b.Emit(events.WorkflowStarted{
				WorkflowID: workflowID,
				Timestamp:  time.Now(),
			})
			// Wait until the test has subscribed before emitting the live one.
			<-release
			b.Emit(events.AgentSpawned{
				ParentAgentID: "main",
				NewAgentID:    "live",
				InitialState:  "X.md",
				Timestamp:     time.Now(),
			})
			<-exit
			return nil
		},
	}
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", "", nil)
	require.NoError(t, err)

	// Wait for the bus to be wired up before subscribing. SubscribeRunEvents
	// blocks on busReady internally, but waiting here prevents the test
	// from racing the pre-sub event past the subscribe call.
	ctx, cancelCtx := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelCtx()
	ch, cancelSub, err := rm.SubscribeRunEvents(ctx, runID)
	require.NoError(t, err)
	defer cancelSub()

	// Replay should have delivered WorkflowStarted by the time we get here.
	select {
	case ev := <-ch:
		_, isStart := ev.(events.WorkflowStarted)
		assert.True(t, isStart, "first replayed event should be WorkflowStarted, got %T", ev)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for replayed WorkflowStarted")
	}

	// Now release the orchestrator to emit the live event.
	close(release)

	select {
	case ev := <-ch:
		spawned, ok := ev.(events.AgentSpawned)
		require.True(t, ok, "live event should be AgentSpawned, got %T", ev)
		assert.Equal(t, "live", spawned.NewAgentID)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for live AgentSpawned event")
	}

	close(exit)
}

// ----------------------------------------------------------------------------
// SubscribeRunEvents — replay + live stream
// ----------------------------------------------------------------------------

// drainAll reads every event from ch until ch is closed or the deadline is
// reached. Used by subscribe tests that expect a finite stream.
func drainAll(t *testing.T, ch <-chan any, timeout time.Duration) []any {
	t.Helper()
	var got []any
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-deadline:
			t.Fatalf("timed out after %v; received %d events", timeout, len(got))
		}
	}
}

// Subscribing to a completed run should replay the recent events so a user
// who clicks into a no-longer-active run can see what happened, rather than
// getting a blank output pane.
func TestSubscribeRunEvents_ReplaysPastEventsAfterCompletion(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)

			b.Emit(events.StateStarted{AgentID: "main", StateName: "S1", Timestamp: time.Now()})
			b.Emit(events.StateCompleted{AgentID: "main", StateName: "S1", Timestamp: time.Now()})
			b.Emit(events.AgentTerminated{AgentID: "main", Timestamp: time.Now()})
			b.Emit(events.WorkflowCompleted{WorkflowID: workflowID, Timestamp: time.Now()})
			return nil
		},
	}

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", "", nil)
	require.NoError(t, err)

	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)

	ch, cancel, err := rm.SubscribeRunEvents(context.Background(), runID)
	require.NoError(t, err)
	defer cancel()

	got := drainAll(t, ch, 2*time.Second)
	require.Len(t, got, 4, "expected replay of all four emitted events")
	assert.IsType(t, events.StateStarted{}, got[0])
	assert.IsType(t, events.StateCompleted{}, got[1])
	assert.IsType(t, events.AgentTerminated{}, got[2])
	assert.IsType(t, events.WorkflowCompleted{}, got[3])
}

// When more events are emitted than the ring buffer capacity, only the most
// recent events are retained — the oldest are evicted. The cap bounds memory
// for long-running runs.
func TestSubscribeRunEvents_RingBufferEvictsOldestEvents(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	overflow := 50
	total := eventLogCap + overflow

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)
			for i := 0; i < total; i++ {
				// Use StateStarted so the sequence index is recoverable from the event.
				b.Emit(events.StateStarted{
					AgentID:   "main",
					StateName: "S" + strconv.Itoa(i),
					Timestamp: time.Now(),
				})
			}
			b.Emit(events.AgentTerminated{AgentID: "main", Timestamp: time.Now()})
			return nil
		},
	}

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", "", nil)
	require.NoError(t, err)

	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)

	ch, cancel, err := rm.SubscribeRunEvents(context.Background(), runID)
	require.NoError(t, err)
	defer cancel()

	got := drainAll(t, ch, 2*time.Second)
	require.Len(t, got, eventLogCap, "ring buffer should cap retained events")

	// After eviction the first retained StateStarted is for index (overflow+1):
	// total = cap + overflow; plus one trailing AgentTerminated pushes the
	// oldest (overflow + 1) extra events out of the buffer.
	first, ok := got[0].(events.StateStarted)
	require.True(t, ok)
	assert.Equal(t, "S"+strconv.Itoa(overflow+1), first.StateName)

	// The tail terminator should be the most recently appended event.
	_, ok = got[len(got)-1].(events.AgentTerminated)
	assert.True(t, ok)
}

// Two subscribers opened in sequence to the same run should each see the full
// replay independently; one subscriber's reads must not drain events from the
// other.
func TestSubscribeRunEvents_MultipleSubscribersIndependent(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)
			b.Emit(events.StateStarted{AgentID: "main", StateName: "A", Timestamp: time.Now()})
			b.Emit(events.StateStarted{AgentID: "main", StateName: "B", Timestamp: time.Now()})
			b.Emit(events.AgentTerminated{AgentID: "main", Timestamp: time.Now()})
			return nil
		},
	}

	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", "", nil)
	require.NoError(t, err)

	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)

	ch1, cancel1, err := rm.SubscribeRunEvents(context.Background(), runID)
	require.NoError(t, err)
	defer cancel1()

	ch2, cancel2, err := rm.SubscribeRunEvents(context.Background(), runID)
	require.NoError(t, err)
	defer cancel2()

	got1 := drainAll(t, ch1, 2*time.Second)
	got2 := drainAll(t, ch2, 2*time.Second)
	assert.Len(t, got1, 3)
	assert.Len(t, got2, 3)
}
