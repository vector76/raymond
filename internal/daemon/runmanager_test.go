package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
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

// assertOrchestratorRanFor returns nil if the fake recorded a call for
// workflowID, or a descriptive error otherwise. Used by recovery tests to
// confirm the orchestrator goroutine was launched with the existing run id
// rather than a freshly-generated one.
func assertOrchestratorRanFor(f *fakeOrchestrator, workflowID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.WorkflowID == workflowID {
			return nil
		}
	}
	return fmt.Errorf("orchestrator was not invoked for workflow %q; calls=%+v", workflowID, f.calls)
}

// captureLog redirects the default logger to a buffer for the duration of
// the test and returns the buffer. Used to assert the recovery path logs
// the id of a skipped malformed state file.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return buf
}

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
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := rm.LaunchRun(ctx, testWorkflowEntry(t, scopeDir), "hello", 5.0, "sonnet", false, "", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, runID)

	// The orchestrator should have been called.
	// Give the goroutine a moment to start.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, fake.callCount())
}

func TestLaunchRun_PropagatesDangerouslySkipPermissions(t *testing.T) {
	// LaunchRun must thread its dangerouslySkipPermissions argument into
	// both the orchestrator's RunOptions and the persisted LaunchParams,
	// so daemon-launched runs honour the same configuration as CLI-launched
	// ones and a subsequent --resume restores the same value.
	for _, dsp := range []bool{true, false} {
		t.Run(fmt.Sprintf("dsp=%v", dsp), func(t *testing.T) {
			stateDir := ensureStateDir(t)
			scopeDir := t.TempDir()

			fake := &fakeOrchestrator{}
			rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			runID, err := rm.LaunchRun(ctx, testWorkflowEntry(t, scopeDir), "", 5.0, "", dsp, "", nil)
			require.NoError(t, err)

			// Wait for the orchestrator to be invoked so RunOptions is captured.
			require.Eventually(t, func() bool {
				return fake.callCount() == 1
			}, time.Second, 10*time.Millisecond)

			fake.mu.Lock()
			gotOpts := fake.calls[0].Opts
			fake.mu.Unlock()
			assert.Equal(t, dsp, gotOpts.DangerouslySkipPermissions,
				"RunOptions.DangerouslySkipPermissions must mirror the LaunchRun arg")

			ws, err := wfstate.ReadState(runID, stateDir)
			require.NoError(t, err)
			require.NotNil(t, ws.LaunchParams)
			assert.Equal(t, dsp, ws.LaunchParams.DangerouslySkipPermissions,
				"persisted LaunchParams must record the launch-time skip-perms value")
		})
	}
}

func TestGetRun_ReturnsCorrectStatusAfterLaunch(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{} // blocks forever
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := rm.LaunchRun(ctx, testWorkflowEntry(t, scopeDir), "input", 5.0, "", false, "", nil)
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
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	_, ok := rm.GetRun("nonexistent")
	assert.False(t, ok)
}

func TestCancelRun_CancelsRunningWorkflow(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{} // blocks until ctx cancelled
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx := context.Background()
	runID, err := rm.LaunchRun(ctx, testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	// Let the goroutine start.
	time.Sleep(50 * time.Millisecond)

	err = rm.CancelRun(runID)
	require.NoError(t, err)

	info, ok := rm.GetRun(runID)
	require.True(t, ok)
	assert.Equal(t, RunStatusCancelled, info.Status)
}

func TestCancelRun_AskingRun(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)
			b.Emit(events.AgentAskStarted{
				AgentID:   "main",
				AskID:   "input-1",
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
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	// Wait until the run reaches asking.
	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)

	info, ok := rm.GetRun(runID)
	require.True(t, ok)
	assert.Equal(t, RunStatusAsking, info.Status)

	// Cancelling an asking run should succeed and change status.
	err = rm.CancelRun(runID)
	require.NoError(t, err)

	info, ok = rm.GetRun(runID)
	require.True(t, ok)
	assert.Equal(t, RunStatusCancelled, info.Status)
}

func TestCancelRun_NotFound(t *testing.T) {
	stateDir := ensureStateDir(t)
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
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
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	// Wait for the run to reach terminal state.
	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)

	err = rm.CancelRun(runID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "terminal state")
}

func TestDeleteRun_RemovesTerminalRun(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	// Seed a tasks dir beside the state dir so we can verify cleanup.
	tasksRoot := filepath.Join(filepath.Dir(stateDir), "tasks")
	require.NoError(t, os.MkdirAll(tasksRoot, 0o755))

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)
			b.Emit(events.WorkflowCompleted{
				WorkflowID: workflowID, TotalCostUSD: 0.1, Timestamp: time.Now(),
			})
			return nil
		},
	}
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)

	// Create the per-run tasks dir after launch so DeleteRun has something to clean up.
	runTasksDir := filepath.Join(tasksRoot, runID)
	require.NoError(t, os.MkdirAll(filepath.Join(runTasksDir, "main"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runTasksDir, "main", "output.txt"), []byte("hi"), 0o644))

	require.NoError(t, rm.DeleteRun(runID))

	// Removed from tracking.
	_, ok := rm.GetRun(runID)
	assert.False(t, ok)

	// Tasks dir wiped.
	_, statErr := os.Stat(runTasksDir)
	assert.True(t, os.IsNotExist(statErr), "tasks dir should be removed")
}

func TestDeleteRun_RemovesRecoveredRunAfterCancel(t *testing.T) {
	// Under Phase-3 semantics a recovered non-terminal run is active, so
	// DeleteRun must reject it until it's cancelled. This test exercises
	// the cancel-then-delete sequence to confirm DeleteRun still cleans up
	// the on-disk state file once the run has drained.
	stateDir := ensureStateDir(t)

	ws := &wfstate.WorkflowState{
		WorkflowID: "workflow_recovered_1",
		ScopeDir:   "/tmp/scope",
		Agents: []wfstate.AgentState{
			{ID: "main", CurrentState: "START.md", Status: wfstate.AgentStatusPaused},
		},
		StartedAt: time.Now().Add(-time.Hour),
	}
	require.NoError(t, wfstate.WriteState(ws.WorkflowID, ws, stateDir))

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	info, ok := rm.GetRun(ws.WorkflowID)
	require.True(t, ok)
	require.Equal(t, RunStatusRunning, info.Status,
		"recovered non-terminal runs are now active, not failed")

	// Active run cannot be deleted; cancel first, then delete.
	require.NoError(t, rm.CancelRun(ws.WorkflowID))
	_, err = rm.WaitForCompletion(ws.WorkflowID, 2*time.Second)
	require.NoError(t, err)

	require.NoError(t, rm.DeleteRun(ws.WorkflowID))

	// State file gone.
	_, statErr := os.Stat(filepath.Join(stateDir, ws.WorkflowID+".json"))
	assert.True(t, os.IsNotExist(statErr))

	// Run no longer tracked.
	_, ok = rm.GetRun(ws.WorkflowID)
	assert.False(t, ok)
}

func TestDeleteRun_RejectsActiveRun(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{} // blocks until cancelled
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := rm.LaunchRun(ctx, testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)

	err = rm.DeleteRun(runID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRunActive)

	// Still tracked.
	_, ok := rm.GetRun(runID)
	assert.True(t, ok)
}

func TestDeleteRun_NotFound(t *testing.T) {
	stateDir := ensureStateDir(t)
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	err = rm.DeleteRun("nonexistent")
	assert.ErrorIs(t, err, ErrRunNotFound)
}

func TestAgentAskStarted_FlipsRunToAsking(t *testing.T) {
	// In daemon mode the orchestrator emits AgentAskStarted but NOT
	// WorkflowPaused (siblings keep running). The run-level Status must
	// still flip to "asking" so the UI knows to poll for pending
	// inputs and surface an input card.
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	started := make(chan struct{})
	cont := make(chan struct{})
	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)
			b.Emit(events.AgentAskStarted{
				AgentID:   "main",
				AskID:   "input-1",
				Prompt:    "Please review",
				NextState: "NEXT.md",
				Timestamp: time.Now(),
			})
			close(started)
			<-cont
			return nil
		},
	}

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	<-started
	// Event delivery is synchronous on the bus, so by the time
	// AgentAskStarted returns the subscriber has already applied its
	// update. Small sleep only as a safety margin against future async
	// changes in bus delivery.
	time.Sleep(20 * time.Millisecond)

	info, ok := rm.GetRun(runID)
	require.True(t, ok)
	assert.Equal(t, RunStatusAsking, info.Status,
		"run status should flip to asking when an agent enters <ask> in daemon mode")
	require.Len(t, info.Agents, 1)
	assert.Equal(t, wfstate.AgentStatusAsking, info.Agents[0].Status)

	close(cont)
	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)
}

func TestStateStarted_FlipsRunBackToRunning(t *testing.T) {
	// After an asking agent receives input and enters its next state,
	// the run should stop reading as asking.
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	asked := make(chan struct{})
	resumed := make(chan struct{})
	done := make(chan struct{})
	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)
			b.Emit(events.AgentAskStarted{
				AgentID:   "main",
				AskID:   "input-1",
				NextState: "REVIEW.md",
				Timestamp: time.Now(),
			})
			close(asked)
			<-resumed
			b.Emit(events.StateStarted{
				AgentID:   "main",
				StateName: "REVIEW.md",
				StateType: "markdown",
				Timestamp: time.Now(),
			})
			<-done
			return nil
		},
	}

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	<-asked
	time.Sleep(20 * time.Millisecond)
	info, _ := rm.GetRun(runID)
	require.Equal(t, RunStatusAsking, info.Status)

	close(resumed)
	time.Sleep(20 * time.Millisecond)
	info, _ = rm.GetRun(runID)
	assert.Equal(t, RunStatusRunning, info.Status,
		"run should go back to running once the asking agent enters its next state")

	close(done)
	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)
}

func TestStateCompleted_CapturesSessionIDOnAgent(t *testing.T) {
	// The dashboard surfaces each agent's backend session id (pi UUID or
	// Claude session id) so users can identify which session belongs to which
	// agent at a glance. The runManager populates AgentInfo.SessionID from
	// each StateCompleted event for that agent.
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)

			b.Emit(events.StateStarted{AgentID: "main", StateName: "S1", StateType: "markdown", Timestamp: time.Now()})
			b.Emit(events.StateCompleted{
				AgentID: "main", StateName: "S1",
				SessionID: "first-session-uuid", CostUSD: 0.01, TotalCostUSD: 0.01,
				Timestamp: time.Now(),
			})
			// A second completed event for the same agent (e.g. a <goto> loop)
			// must replace the session id, not append.
			b.Emit(events.StateCompleted{
				AgentID: "main", StateName: "S2",
				SessionID: "second-session-uuid", CostUSD: 0.01, TotalCostUSD: 0.02,
				Timestamp: time.Now(),
			})
			// Script states emit StateCompleted with empty SessionID; the
			// agent's existing session id must not be wiped.
			b.Emit(events.StateCompleted{
				AgentID: "main", StateName: "S3-script",
				SessionID: "", CostUSD: 0, TotalCostUSD: 0.02,
				Timestamp: time.Now(),
			})
			b.Emit(events.AgentTerminated{AgentID: "main", Timestamp: time.Now()})
			b.Emit(events.WorkflowCompleted{WorkflowID: workflowID, TotalCostUSD: 0.02, Timestamp: time.Now()})
			return nil
		},
	}

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "go", 5.0, "", false, "", nil)
	require.NoError(t, err)

	info, err := rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, info.Agents, 1)
	assert.Equal(t, "second-session-uuid", info.Agents[0].SessionID,
		"latest non-empty session id wins; script-state empty SessionID must not clobber it")
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

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "go", 5.0, "", false, "", nil)
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
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := rm.LaunchRun(ctx, testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	_, err = rm.WaitForCompletion(runID, 100*time.Millisecond)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestWaitForCompletion_NotFound(t *testing.T) {
	stateDir := ensureStateDir(t)
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
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

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	entry := testWorkflowEntry(t, scopeDir)
	ctx := context.Background()

	id1, err := rm.LaunchRun(ctx, entry, "run1", 5.0, "", false, "", nil)
	require.NoError(t, err)
	id2, err := rm.LaunchRun(ctx, entry, "run2", 5.0, "", false, "", nil)
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

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	entry := testWorkflowEntry(t, scopeDir)
	ctx := context.Background()

	id1, err := rm.LaunchRun(ctx, entry, "", 5.0, "", false, "", nil)
	require.NoError(t, err)
	id2, err := rm.LaunchRun(ctx, entry, "", 5.0, "", false, "", nil)
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
	// Phase-3 semantics: a "previously running" (paused-with-no-ask) state
	// file is auto-resumed through the orchestrator on startup. The run
	// comes up as RunStatusRunning, not the historical RunStatusFailed,
	// and the orchestrator goroutine is actively running against the
	// injected fake.
	stateDir := ensureStateDir(t)

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

	fake := &fakeOrchestrator{} // default: blocks until ctx cancelled
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rm.CancelRun("recovered-run-1") })

	info, ok := rm.GetRun("recovered-run-1")
	require.True(t, ok)
	assert.Equal(t, RunStatusRunning, info.Status,
		"a recovered non-terminal run is re-launched through the orchestrator, not parked as failed")
	assert.Equal(t, 2.5, info.CostUSD)
	require.Len(t, info.Agents, 1)
	assert.Equal(t, "main", info.Agents[0].ID)
	assert.Equal(t, "PROCESS.md", info.Agents[0].CurrentState)

	// The orchestrator goroutine should have been launched against the
	// existing run id and existing state file.
	assert.Eventually(t, func() bool { return fake.callCount() == 1 },
		time.Second, 10*time.Millisecond, "orchestrator should be invoked for the recovered run")
	require.NoError(t, assertOrchestratorRanFor(fake, "recovered-run-1"))
}

func TestRestartRecovery_AskingWorkflow(t *testing.T) {
	// Phase-3 semantics: an asking-shape state file is also re-launched
	// through the orchestrator (full DeliverInput answerability is bead-5;
	// here we just assert the run comes up as `asking` and the goroutine
	// runs).
	stateDir := ensureStateDir(t)

	ws := &wfstate.WorkflowState{
		WorkflowID:   "asking-run",
		ScopeDir:     "/some/scope",
		TotalCostUSD: 1.0,
		BudgetUSD:    10.0,
		Agents: []wfstate.AgentState{
			{
				ID:           "main",
				CurrentState: "WAIT.md",
				Status:       wfstate.AgentStatusAsking,
				Stack:        []wfstate.StackFrame{},
			},
		},
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "asking-run.json"),
		data, 0o644,
	))

	fake := &fakeOrchestrator{}
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rm.CancelRun("asking-run") })

	info, ok := rm.GetRun("asking-run")
	require.True(t, ok)
	assert.Equal(t, RunStatusAsking, info.Status)

	assert.Eventually(t, func() bool { return fake.callCount() == 1 },
		time.Second, 10*time.Millisecond, "orchestrator should be invoked for the recovered asking run")
	require.NoError(t, assertOrchestratorRanFor(fake, "asking-run"))
}

func TestRestartRecovery_EmptyStateDir(t *testing.T) {
	stateDir := ensureStateDir(t)

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	runs := rm.ListRuns()
	assert.Empty(t, runs)
}

func TestRestartRecovery_MalformedStateFileSkipped(t *testing.T) {
	// Failure-mode policy: a malformed state file in the serve pool is
	// skipped (the id is logged), startup continues, and other state
	// files in the pool still come up.
	stateDir := ensureStateDir(t)

	// (1) A malformed JSON state file — not even parseable.
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "broken-run.json"),
		[]byte("{not-json"),
		0o644,
	))

	// (2) A well-formed neighbour that should still be recovered and
	// re-launched alongside the malformed one.
	ws := &wfstate.WorkflowState{
		WorkflowID: "healthy-run",
		ScopeDir:   "/some/scope",
		Agents: []wfstate.AgentState{
			{ID: "main", CurrentState: "S.md", Stack: []wfstate.StackFrame{}},
		},
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "healthy-run.json"),
		data, 0o644,
	))

	// Capture the log output to assert the skipped id is surfaced.
	logBuf := captureLog(t)

	fake := &fakeOrchestrator{}
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rm.CancelRun("healthy-run") })

	// Malformed run is NOT tracked.
	_, ok := rm.GetRun("broken-run")
	assert.False(t, ok, "malformed state file should be skipped, not tracked")

	// Healthy neighbour comes up and the orchestrator is invoked for it.
	info, ok := rm.GetRun("healthy-run")
	require.True(t, ok)
	assert.Equal(t, RunStatusRunning, info.Status)
	assert.Eventually(t, func() bool { return fake.callCount() == 1 },
		time.Second, 10*time.Millisecond, "the healthy run alongside the malformed one should still launch")
	require.NoError(t, assertOrchestratorRanFor(fake, "healthy-run"))

	// The skipped id is logged so an operator can chase down the file.
	assert.Contains(t, logBuf.String(), "broken-run",
		"the malformed state file's id should be logged on skip")
}

func TestLaunchRun_FailedOrchestrator(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			return assert.AnError
		},
	}
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	info, err := rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, RunStatusFailed, info.Status)
	assert.Contains(t, info.Result, "assert.AnError")
}

func TestLaunchRun_AskingStatus(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)

			b.Emit(events.AgentAskStarted{
				AgentID:   "main",
				AskID:   "input-1",
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

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	info, err := rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, RunStatusAsking, info.Status)
	assert.Equal(t, 0.3, info.CostUSD)

	// Verify agent-level status.
	require.Len(t, info.Agents, 1)
	assert.Equal(t, wfstate.AgentStatusAsking, info.Agents[0].Status)
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

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
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
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := rm.LaunchRun(ctx, *entry, "hello", 5.0, "", false, "", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, runID)
}

// ----------------------------------------------------------------------------
// QuiesceAll — per-run stop signal fan-out
// ----------------------------------------------------------------------------

// quiesceObservingOrchestrator captures the StopSignalCh it receives so the
// test can assert that the close propagates from QuiesceAll to the
// orchestrator goroutine. The behaviour blocks until either the stop signal
// channel closes or the context is cancelled, mirroring what the real
// orchestrator does on a graceful quiesce.
func quiesceObservingOrchestrator() (*fakeOrchestrator, func() <-chan struct{}) {
	var (
		mu     sync.Mutex
		stopCh <-chan struct{}
		ready  = make(chan struct{})
	)

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			mu.Lock()
			stopCh = opts.StopSignalCh
			mu.Unlock()
			close(ready)
			select {
			case <-opts.StopSignalCh:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}

	getStop := func() <-chan struct{} {
		<-ready
		mu.Lock()
		defer mu.Unlock()
		return stopCh
	}
	return fake, getStop
}

// (a) QuiesceAll closes the stop channel that the orchestrator received.
func TestQuiesceAll_ClosesPerRunStopChannel(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake, getStop := quiesceObservingOrchestrator()
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	stopCh := getStop()
	require.NotNil(t, stopCh, "orchestrator must receive a non-nil StopSignalCh")

	rm.QuiesceAll()

	select {
	case <-stopCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for StopSignalCh to close after QuiesceAll")
	}

	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)
}

// (b) A single QuiesceAll call fans out across every active run tracked by
// a manager. One orchestrator instance, channels keyed by the run_id passed
// to RunAllAgents.
func TestQuiesceAll_ClosesEveryActiveRun(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	var (
		mu      sync.Mutex
		byRunID = make(map[string]<-chan struct{})
		waitFor = make(chan string, 4)
	)
	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			mu.Lock()
			byRunID[workflowID] = opts.StopSignalCh
			mu.Unlock()
			waitFor <- workflowID
			select {
			case <-opts.StopSignalCh:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	id1, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)
	id2, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	// Wait until both orchestrator goroutines have captured their channels.
	for i := 0; i < 2; i++ {
		select {
		case <-waitFor:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for both orchestrators to start")
		}
	}

	rm.QuiesceAll()

	for _, id := range []string{id1, id2} {
		mu.Lock()
		ch := byRunID[id]
		mu.Unlock()
		require.NotNil(t, ch, "run %s did not receive a StopSignalCh", id)
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("run %s: StopSignalCh did not close after QuiesceAll", id)
		}
	}

	_, err = rm.WaitForCompletion(id1, 5*time.Second)
	require.NoError(t, err)
	_, err = rm.WaitForCompletion(id2, 5*time.Second)
	require.NoError(t, err)
}

// (c) QuiesceAll skips recovered entries (cancel == nil) and does not panic.
func TestQuiesceAll_SkipsRecoveredEntries(t *testing.T) {
	stateDir := ensureStateDir(t)

	ws := &wfstate.WorkflowState{
		WorkflowID: "recovered-quiesce",
		ScopeDir:   "/some/scope",
		Agents: []wfstate.AgentState{
			{ID: "main", CurrentState: "S.md", Status: wfstate.AgentStatusPaused, Stack: []wfstate.StackFrame{}},
		},
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, ws.WorkflowID+".json"), data, 0o644))

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	// Confirm the entry was recovered with no live orchestrator.
	_, ok := rm.GetRun(ws.WorkflowID)
	require.True(t, ok)

	// Should be a no-op; in particular, must not panic on the nil stopSignalCh.
	require.NotPanics(t, func() { rm.QuiesceAll() })
}

// (d) Calling QuiesceAll twice is a no-op the second time — sync.Once on each
// entry prevents a double-close panic.
func TestQuiesceAll_DoubleInvokeIsNoOp(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake, getStop := quiesceObservingOrchestrator()
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	stopCh := getStop()
	require.NotNil(t, stopCh)

	require.NotPanics(t, func() {
		rm.QuiesceAll()
		rm.QuiesceAll()
	})

	select {
	case <-stopCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for StopSignalCh to close after QuiesceAll")
	}

	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)
}

// ----------------------------------------------------------------------------
// CancelAll — fleet-wide context cancellation (Tier 3 force-kill)
// ----------------------------------------------------------------------------

// TestCancelAll_CancelsEveryActiveRun launches several runs whose
// orchestrators capture their context, then asserts every captured context
// is cancelled by a single CancelAll call.
func TestCancelAll_CancelsEveryActiveRun(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	var (
		mu      sync.Mutex
		ctxs    = make(map[string]context.Context)
		started = make(chan struct{}, 4)
	)
	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			mu.Lock()
			ctxs[workflowID] = ctx
			mu.Unlock()
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	entry := testWorkflowEntry(t, scopeDir)
	id1, err := rm.LaunchRun(context.Background(), entry, "", 5.0, "", false, "", nil)
	require.NoError(t, err)
	id2, err := rm.LaunchRun(context.Background(), entry, "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for orchestrators to start")
		}
	}

	rm.CancelAll()

	for _, id := range []string{id1, id2} {
		mu.Lock()
		cctx := ctxs[id]
		mu.Unlock()
		require.NotNil(t, cctx, "no captured ctx for run %s", id)
		select {
		case <-cctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("run %s ctx was not cancelled by CancelAll", id)
		}
	}

	_, err = rm.WaitForCompletion(id1, 5*time.Second)
	require.NoError(t, err)
	_, err = rm.WaitForCompletion(id2, 5*time.Second)
	require.NoError(t, err)
}

// TestCancelAll_SkipsRecoveredEntries: a recovered entry has cancel == nil.
// CancelAll must not panic and must not attempt to invoke a nil cancel.
func TestCancelAll_SkipsRecoveredEntries(t *testing.T) {
	stateDir := ensureStateDir(t)

	ws := &wfstate.WorkflowState{
		WorkflowID: "recovered-cancelall",
		ScopeDir:   "/some/scope",
		Agents: []wfstate.AgentState{
			{ID: "main", CurrentState: "S.md", Status: wfstate.AgentStatusPaused, Stack: []wfstate.StackFrame{}},
		},
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, ws.WorkflowID+".json"), data, 0o644))

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	// Confirm the entry was recovered and has no live orchestrator.
	_, ok := rm.GetRun(ws.WorkflowID)
	require.True(t, ok)

	require.NotPanics(t, func() { rm.CancelAll() })
}

// ----------------------------------------------------------------------------
// WaitAllDone — snapshot-based drain wait
// ----------------------------------------------------------------------------

// releaseGate gives tests a per-run blocking channel keyed by run ID. Each
// orchestrator goroutine blocks until its gate channel is closed, letting
// the test stagger run terminations.
type releaseGate struct {
	mu       sync.Mutex
	channels map[string]chan struct{}
}

func newReleaseGate() *releaseGate {
	return &releaseGate{channels: make(map[string]chan struct{})}
}

func (g *releaseGate) get(id string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch, ok := g.channels[id]
	if !ok {
		ch = make(chan struct{})
		g.channels[id] = ch
	}
	return ch
}

// TestWaitAllDone_EmptySetReturnsClosedChannel: with no active runs the
// returned channel is already closed so callers can select on it without a
// special case.
func TestWaitAllDone_EmptySetReturnsClosedChannel(t *testing.T) {
	stateDir := ensureStateDir(t)
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	ch := rm.WaitAllDone(context.Background())
	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("WaitAllDone on empty set should return an already-closed channel")
	}
}

// TestWaitAllDone_ClosesWhenLastDoneChCloses: the returned channel must
// remain open until the *last* run in the snapshot has terminated.
func TestWaitAllDone_ClosesWhenLastDoneChCloses(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	gate := newReleaseGate()
	started := make(chan string, 4)
	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			started <- workflowID
			<-gate.get(workflowID)
			return nil
		},
	}
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	entry := testWorkflowEntry(t, scopeDir)
	const n = 3
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id, err := rm.LaunchRun(context.Background(), entry, "", 5.0, "", false, "", nil)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	// Make sure every orchestrator is parked at its gate before we snapshot.
	for i := 0; i < n; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("orchestrator did not start in time")
		}
	}

	waitCh := rm.WaitAllDone(context.Background())

	// Release the first n-1 runs one at a time. waitCh must stay open until
	// the final one is released.
	for i := 0; i < n-1; i++ {
		close(gate.get(ids[i]))
		_, err = rm.WaitForCompletion(ids[i], 2*time.Second)
		require.NoError(t, err)
		select {
		case <-waitCh:
			t.Fatalf("WaitAllDone closed after only %d/%d runs finished", i+1, n)
		case <-time.After(50 * time.Millisecond):
			// expected: still waiting on the last run
		}
	}

	// Release the last run; waitCh must close.
	close(gate.get(ids[n-1]))
	select {
	case <-waitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitAllDone did not close after the last run finished")
	}
}

// TestWaitAllDone_SnapshotIgnoresLaterLaunches: a run launched *after*
// WaitAllDone returns must not extend the wait — the snapshot is fixed at
// call time. Verifies the contract that callers (e.g. graceful shutdown)
// can rely on the wait completing even if some other path tries to launch
// a new run while shutdown is in progress.
func TestWaitAllDone_SnapshotIgnoresLaterLaunches(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	gate := newReleaseGate()
	started := make(chan string, 4)
	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			started <- workflowID
			<-gate.get(workflowID)
			return nil
		},
	}
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	entry := testWorkflowEntry(t, scopeDir)
	id1, err := rm.LaunchRun(context.Background(), entry, "", 5.0, "", false, "", nil)
	require.NoError(t, err)
	id2, err := rm.LaunchRun(context.Background(), entry, "", 5.0, "", false, "", nil)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("initial orchestrators did not start")
		}
	}

	// Snapshot {id1, id2}.
	waitCh := rm.WaitAllDone(context.Background())

	// New run launched after the snapshot. Its doneCh must NOT extend waitCh.
	id3, err := rm.LaunchRun(context.Background(), entry, "", 5.0, "", false, "", nil)
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("post-snapshot run did not start")
	}

	// Drain the original two; the wait should close even though id3 is
	// still running.
	close(gate.get(id1))
	close(gate.get(id2))

	select {
	case <-waitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitAllDone should close once the snapshot drains, regardless of new runs")
	}

	// Sanity: id3 is still active at this point.
	info, ok := rm.GetRun(id3)
	require.True(t, ok)
	assert.Equal(t, RunStatusRunning, info.Status,
		"post-snapshot run should still be running when WaitAllDone returns")

	// Cleanup: release id3 so the orchestrator goroutine exits before the
	// test ends.
	close(gate.get(id3))
	_, err = rm.WaitForCompletion(id3, 5*time.Second)
	require.NoError(t, err)
}

// TestWaitAllDone_CtxCancelClosesEarly: the documented ctx contract says
// cancelling ctx closes the returned channel early even if runs in the
// snapshot are still running. Pins that contract so a future refactor that
// drops the ctx.Done() arm is caught.
func TestWaitAllDone_CtxCancelClosesEarly(t *testing.T) {
	stateDir := ensureStateDir(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{} // default: blocks on ctx.Done()
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	// Let the orchestrator goroutine reach its ctx-wait.
	require.Eventually(t, func() bool { return fake.callCount() == 1 },
		2*time.Second, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	waitCh := rm.WaitAllDone(ctx)

	// Active run hasn't terminated, so waitCh must still be open.
	select {
	case <-waitCh:
		t.Fatal("WaitAllDone closed prematurely before ctx-cancel or run drain")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case <-waitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitAllDone did not close after ctx-cancel")
	}

	// Cleanup the still-running orchestrator.
	rm.CancelAll()
	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)
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

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
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

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", &fakeOrchestrator{})
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
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
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

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
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

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
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

	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
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

// ----------------------------------------------------------------------------
// Pool routing — Phase 2 of the disjoint run-pool plan.
//
// The daemon owns the serve pool (.raymond/serve-state/) and must never read
// from or write to the CLI pool (.raymond/state/). These tests pin that
// invariant at the manager level: a stale state file seeded into the CLI
// pool is invisible to the daemon, and a daemon-launched run writes only
// into the serve pool.
// ----------------------------------------------------------------------------

// ensureServePoolLayout mirrors ensureStateDir but creates both the CLI and
// serve pool directories under a synthetic raymond directory. Returns the
// (raymondDir, cliStateDir, serveStateDir) triple so the caller can seed
// stale state into the CLI pool while pointing the run manager at the
// serve pool.
func ensureServePoolLayout(t *testing.T) (raymondDir, cliStateDir, serveStateDir string) {
	t.Helper()
	raymondDir = filepath.Join(t.TempDir(), ".raymond")
	cliStateDir = filepath.Join(raymondDir, "state")
	serveStateDir = filepath.Join(raymondDir, "serve-state")
	require.NoError(t, os.MkdirAll(cliStateDir, 0o755))
	require.NoError(t, os.MkdirAll(serveStateDir, 0o755))
	return raymondDir, cliStateDir, serveStateDir
}

func TestRunManager_PoolIsolation_IgnoresCLIPoolPollution(t *testing.T) {
	// A daemon pointed at the serve pool must not surface runs that
	// happen to live in the sibling CLI pool — the two pools are disjoint
	// by directory.
	_, cliStateDir, serveStateDir := ensureServePoolLayout(t)

	// Seed two stale runs into the CLI pool; both have an active main
	// agent so they would pass RecoverWorkflows' filter if the daemon
	// were (incorrectly) reading from this pool.
	for _, id := range []string{"stale-cli-run-1", "stale-cli-run-2"} {
		ws := &wfstate.WorkflowState{
			WorkflowID: id,
			ScopeDir:   "/tmp/scope",
			Agents: []wfstate.AgentState{
				{ID: "main", CurrentState: "START.md", Status: wfstate.AgentStatusPaused},
			},
			StartedAt: time.Now().Add(-time.Hour),
		}
		require.NoError(t, wfstate.WriteState(id, ws, cliStateDir))
	}

	rm, err := NewRunManagerWithOrchestrator(serveStateDir, "/tmp", &fakeOrchestrator{})
	require.NoError(t, err)

	assert.Empty(t, rm.ListRuns(),
		"daemon must not surface runs from the CLI pool")
}

func TestLaunchRun_WritesToServePoolNotCLIPool(t *testing.T) {
	// LaunchRun must persist initial state in the serve pool; the CLI
	// pool stays empty. Verifies the WriteState routing change.
	_, cliStateDir, serveStateDir := ensureServePoolLayout(t)
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{}
	rm, err := NewRunManagerWithOrchestrator(serveStateDir, "/tmp", fake)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := rm.LaunchRun(ctx, testWorkflowEntry(t, scopeDir), "input", 5.0, "", false, "", nil)
	require.NoError(t, err)

	// State file present in the serve pool.
	servePath := filepath.Join(serveStateDir, runID+".json")
	_, err = os.Stat(servePath)
	assert.NoError(t, err, "state file should land in the serve pool")

	// And NOT in the CLI pool.
	cliPath := filepath.Join(cliStateDir, runID+".json")
	_, statErr := os.Stat(cliPath)
	assert.True(t, os.IsNotExist(statErr),
		"state file must not be written into the CLI pool (%s)", cliPath)
}

func TestDeleteRun_RemovesTasksDirViaPlumbedRaymondDir(t *testing.T) {
	// DeleteRun derives the tasks directory from the plumbed-in raymond
	// directory, not by stripping a segment off the state path. Place
	// raymondDir somewhere that is NOT filepath.Dir(stateDir) so the
	// plumbed value can be distinguished from the path-stripping fallback.
	tmp := t.TempDir()
	raymondDir := filepath.Join(tmp, "alt-raymond")        // plumbed value
	require.NoError(t, os.MkdirAll(raymondDir, 0o755))
	serveStateDir := filepath.Join(tmp, "elsewhere", "serve-state") // not under raymondDir
	require.NoError(t, os.MkdirAll(serveStateDir, 0o755))
	scopeDir := t.TempDir()

	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			b := bus.New()
			opts.ObserverSetup(b)
			b.Emit(events.WorkflowCompleted{
				WorkflowID: workflowID, TotalCostUSD: 0.1, Timestamp: time.Now(),
			})
			return nil
		},
	}
	rm, err := NewRunManagerWithOrchestrator(serveStateDir, "/tmp", fake)
	require.NoError(t, err)
	rm.SetRaymondDir(raymondDir)

	runID, err := rm.LaunchRun(context.Background(), testWorkflowEntry(t, scopeDir), "", 5.0, "", false, "", nil)
	require.NoError(t, err)

	_, err = rm.WaitForCompletion(runID, 5*time.Second)
	require.NoError(t, err)

	// Seed the per-run tasks dir at the plumbed-in location.
	runTasksDir := filepath.Join(raymondDir, "tasks", runID)
	require.NoError(t, os.MkdirAll(filepath.Join(runTasksDir, "main"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runTasksDir, "main", "out.txt"), []byte("hi"), 0o644))

	// Also drop a decoy tasks dir at the path the legacy fallback would
	// have computed (filepath.Dir(serveStateDir)/tasks/<id>); DeleteRun
	// must NOT touch it.
	decoyTasksDir := filepath.Join(filepath.Dir(serveStateDir), "tasks", runID)
	require.NoError(t, os.MkdirAll(decoyTasksDir, 0o755))

	require.NoError(t, rm.DeleteRun(runID))

	_, statErr := os.Stat(runTasksDir)
	assert.True(t, os.IsNotExist(statErr),
		"tasks dir at <raymondDir>/tasks/<id>/ must be removed by DeleteRun")

	// The decoy at the legacy-fallback location must be untouched —
	// proof that the plumbed raymondDir wins over path stripping.
	_, decoyErr := os.Stat(decoyTasksDir)
	assert.NoError(t, decoyErr,
		"DeleteRun must not strip a segment off the state path to derive the tasks dir")

	// State file is gone from the serve pool too.
	_, statErr = os.Stat(filepath.Join(serveStateDir, runID+".json"))
	assert.True(t, os.IsNotExist(statErr), "serve-pool state file must be removed")
}

// ----------------------------------------------------------------------------
// Phase-3 tail (bead-5): recovered-ask answerability + dangling-record policy.
//
// The serve daemon must come up such that a recovered asking-state run is
// reachable via DeliverInput without an intervening per-run resume call (the
// orchestrator goroutine is wired with AskInputCh through the same path
// LaunchRun uses for fresh runs), and any pending-registry entry whose paired
// state file is missing is dropped at startup with a single shared policy.
// ----------------------------------------------------------------------------

// seedAskingRunForRecovery writes an asking-state state file into the serve
// pool and a paired PendingAsk into the on-disk pending_inputs.jsonl. It
// returns a freshly-built PendingRegistry that has replayed the seeded log —
// the same state a daemon would see on startup after a previous instance
// crashed mid-ask.
func seedAskingRunForRecovery(t *testing.T, raymondDir, serveStateDir, runID, askID string) *PendingRegistry {
	t.Helper()

	ws := &wfstate.WorkflowState{
		WorkflowID: runID,
		ScopeDir:   "/some/scope",
		Agents: []wfstate.AgentState{
			{
				ID:           "main",
				CurrentState: "WAIT.md",
				Status:       wfstate.AgentStatusAsking,
				AskID:        askID,
				AskPrompt:    "what next?",
				AskNextState: "NEXT.md",
				Stack:        []wfstate.StackFrame{},
			},
		},
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(serveStateDir, runID+".json"), data, 0o644))

	// Register a pending entry via a throwaway registry so the on-disk log
	// is populated, then return a fresh registry whose replay reads the
	// same log back — exactly the path NewRunManagerForServe drives.
	seedPR, err := NewPendingRegistry(raymondDir)
	require.NoError(t, err)
	require.NoError(t, seedPR.Register(PendingAsk{
		RunID:      runID,
		AgentID:    "main",
		AskID:      askID,
		WorkflowID: runID,
		Prompt:     "what next?",
		NextState:  "NEXT.md",
		CreatedAt:  time.Now(),
	}))

	pr, err := NewPendingRegistry(raymondDir)
	require.NoError(t, err)
	return pr
}

// TestRestartRecovery_RecoveredAskAnswerableViaDeliverInput is the
// answerability acceptance test: a recovered asking-state run accepts
// DeliverInput through the manager API without any per-run resume call, and
// the input shows up on the orchestrator's AskInputCh.
func TestRestartRecovery_RecoveredAskAnswerableViaDeliverInput(t *testing.T) {
	raymondDir := filepath.Join(t.TempDir(), ".raymond")
	serveStateDir := filepath.Join(raymondDir, "serve-state")
	require.NoError(t, os.MkdirAll(serveStateDir, 0o755))

	const runID = "asking-recovered"
	const askID = "ask-1"

	pr := seedAskingRunForRecovery(t, raymondDir, serveStateDir, runID, askID)

	// Fake orchestrator simulates the real one's daemon-mode behaviour for
	// an already-asking agent: install observers, then block on AskInputCh
	// until DeliverInput feeds an answer. On receipt, emit StateStarted so
	// the manager flips the run out of asking.
	gotInput := make(chan orchestrator.AskInput, 1)
	fake := &fakeOrchestrator{
		behaviour: func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
			if opts.AskInputCh == nil {
				return fmt.Errorf("recovered asking run launched without AskInputCh; daemon-mode wiring is broken")
			}
			if !opts.DaemonMode {
				return fmt.Errorf("recovered run not launched in daemon mode")
			}
			b := bus.New()
			opts.ObserverSetup(b)
			select {
			case in := <-opts.AskInputCh:
				gotInput <- in
				b.Emit(events.StateStarted{
					AgentID:   "main",
					StateName: "NEXT.md",
					StateType: "markdown",
					Timestamp: time.Now(),
				})
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}

	rm, err := NewRunManagerForServe(serveStateDir, "/tmp", fake, pr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rm.CancelRun(runID) })

	// Initial classification: the run comes up as asking because the
	// persisted agent status is asking.
	info, ok := rm.GetRun(runID)
	require.True(t, ok)
	assert.Equal(t, RunStatusAsking, info.Status, "recovered asking-state run should start in asking status")

	// Wait until the fake orchestrator goroutine has started so we can be
	// sure the askInputCh is being read.
	assert.Eventually(t, func() bool { return fake.callCount() == 1 },
		time.Second, 10*time.Millisecond, "orchestrator should be invoked for the recovered asking run")

	// The acceptance criterion: DeliverInput succeeds without an
	// intervening resume call. The pending entry was replayed from disk,
	// so GetAndRemove finds it; the runEntry's askInputCh was installed by
	// relaunchRecoveredRun, so the send is not "no active ask input channel".
	require.NoError(t, rm.DeliverInput(runID, askID, "the-answer", nil),
		"DeliverInput on a recovered asking run should succeed without a per-run resume")

	select {
	case in := <-gotInput:
		assert.Equal(t, askID, in.AskID)
		assert.Equal(t, "the-answer", in.Response)
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrator never received input on AskInputCh")
	}

	// And the run transitions out of asking via the fake's signal.
	assert.Eventually(t, func() bool {
		info, _ := rm.GetRun(runID)
		return info != nil && info.Status != RunStatusAsking
	}, 2*time.Second, 10*time.Millisecond, "run should leave asking after delivered input")
}

// TestRestartRecovery_DanglingPendingEntryDropped verifies the
// dangling-record policy: an entry in pending_inputs.jsonl whose paired
// state file is missing from the serve pool is dropped from the in-memory
// registry at startup, the dropped id is logged, and the daemon comes up
// cleanly.
func TestRestartRecovery_DanglingPendingEntryDropped(t *testing.T) {
	raymondDir := filepath.Join(t.TempDir(), ".raymond")
	serveStateDir := filepath.Join(raymondDir, "serve-state")
	require.NoError(t, os.MkdirAll(serveStateDir, 0o755))

	const ghostRunID = "ghost-run"
	const ghostAskID = "ask-ghost"

	// Seed a pending entry whose state file does NOT exist in the serve
	// pool. Go through Register so the on-disk log has the entry, then
	// rebuild the registry to mimic the post-restart replay.
	seedPR, err := NewPendingRegistry(raymondDir)
	require.NoError(t, err)
	require.NoError(t, seedPR.Register(PendingAsk{
		RunID:      ghostRunID,
		AgentID:    "main",
		AskID:      ghostAskID,
		WorkflowID: ghostRunID,
		CreatedAt:  time.Now(),
	}))

	pr, err := NewPendingRegistry(raymondDir)
	require.NoError(t, err)
	// Sanity: the replayed registry sees the entry before the prune runs.
	if _, ok := pr.Get(ghostAskID); !ok {
		t.Fatalf("seed harness failed: registry replay did not surface %q", ghostAskID)
	}

	logBuf := captureLog(t)

	rm, err := NewRunManagerForServe(serveStateDir, "/tmp", &fakeOrchestrator{}, pr)
	require.NoError(t, err, "daemon should come up cleanly even with a dangling pending entry")
	require.NotNil(t, rm)

	// The dangling entry is gone from the in-memory registry.
	_, stillThere := pr.Get(ghostAskID)
	assert.False(t, stillThere, "dangling pending entry should be dropped from the in-memory registry")

	// The dangling run id is named in the captured log.
	assert.Contains(t, logBuf.String(), ghostRunID,
		"the dropped run id should be surfaced in the log so an operator can chase it down")

	// The daemon has no recovered run for the ghost id either — the
	// state file is absent, so there's nothing to surface.
	_, ok := rm.GetRun(ghostRunID)
	assert.False(t, ok, "no run entry should be created for a state-file-less id")
}
