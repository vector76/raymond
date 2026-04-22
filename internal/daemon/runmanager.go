package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/orchestrator"
	"github.com/vector76/raymond/internal/specifier"
	wfstate "github.com/vector76/raymond/internal/state"
)

// Run status constants.
const (
	RunStatusRunning       = "running"
	RunStatusCompleted     = "completed"
	RunStatusFailed        = "failed"
	RunStatusCancelled     = "cancelled"
	RunStatusAwaitingInput = "awaiting_input"
)

// AgentInfo holds a snapshot of an agent's state within a run.
type AgentInfo struct {
	ID           string
	CurrentState string
	Status       string
}

// RunInfo holds a snapshot of a run's current state.
type RunInfo struct {
	RunID           string
	WorkflowID      string
	Status          string
	CurrentState    string
	Agents          []AgentInfo
	CostUSD         float64
	StartedAt       time.Time
	ElapsedDuration time.Duration
	Result          string
}

// runEntry is the internal bookkeeping for a tracked run.
type runEntry struct {
	mu     sync.Mutex
	info   RunInfo
	cancel context.CancelFunc // nil for recovered (non-running) entries
	doneCh chan struct{}       // closed when the run reaches a terminal state
}

// Orchestrator is the interface for launching workflow runs. It exists so
// tests can substitute a fake without importing the real orchestrator.
type Orchestrator interface {
	RunAllAgents(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error
}

// defaultOrchestrator delegates to the real orchestrator package function.
type defaultOrchestrator struct{}

func (defaultOrchestrator) RunAllAgents(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
	return orchestrator.RunAllAgents(ctx, workflowID, opts)
}

// RunManager tracks active and completed workflow runs for the daemon.
type RunManager struct {
	mu           sync.RWMutex
	runs         map[string]*runEntry
	stateDir     string
	cwd          string // daemon working directory (fallback for workDir)
	orchestrator Orchestrator
}

// NewRunManager creates a RunManager and recovers any in-progress workflows
// found in stateDir.
func NewRunManager(stateDir string, cwd string) (*RunManager, error) {
	rm := &RunManager{
		runs:         make(map[string]*runEntry),
		stateDir:     stateDir,
		cwd:          cwd,
		orchestrator: defaultOrchestrator{},
	}
	if err := rm.recoverRuns(); err != nil {
		return nil, fmt.Errorf("recover runs: %w", err)
	}
	return rm, nil
}

// newRunManagerWithOrchestrator creates a RunManager with a custom orchestrator
// (used in tests).
func newRunManagerWithOrchestrator(stateDir string, cwd string, orch Orchestrator) (*RunManager, error) {
	rm := &RunManager{
		runs:         make(map[string]*runEntry),
		stateDir:     stateDir,
		cwd:          cwd,
		orchestrator: orch,
	}
	if err := rm.recoverRuns(); err != nil {
		return nil, fmt.Errorf("recover runs: %w", err)
	}
	return rm, nil
}

// LaunchRun starts a new workflow run. It creates initial state, launches the
// orchestrator in a goroutine, and returns the generated run ID.
//
// Working directory resolution: workDir (per-run override) > cwd (daemon default).
// Environment: env is passed through for future use by executors.
func (rm *RunManager) LaunchRun(
	ctx context.Context,
	entry WorkflowEntry,
	input string,
	budget float64,
	model string,
	workDir string,
	env map[string]string,
) (string, error) {
	stateDir := wfstate.GetStateDir(rm.stateDir)

	runID, err := wfstate.GenerateWorkflowID(stateDir)
	if err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}

	// Resolve the entry point (e.g. "1_START.md" or "START.md").
	initialState, err := specifier.ResolveEntryPoint(entry.ScopeDir)
	if err != nil {
		return "", fmt.Errorf("resolve entry point for %q: %w", entry.ID, err)
	}

	// Create and persist initial workflow state.
	var inputPtr *string
	if input != "" {
		inputPtr = &input
	}

	lp := &wfstate.LaunchParams{
		Model:   model,
		OnAwait: "pause", // daemon always allows await
	}

	ws := wfstate.CreateInitialState(runID, entry.ScopeDir, initialState, budget, inputPtr, "", lp)
	if err := wfstate.WriteState(runID, ws, stateDir); err != nil {
		return "", fmt.Errorf("write initial state: %w", err)
	}

	runCtx, runCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})

	re := &runEntry{
		info: RunInfo{
			RunID:      runID,
			WorkflowID: entry.ID,
			Status:     RunStatusRunning,
			Agents: []AgentInfo{{
				ID:           "main",
				CurrentState: initialState,
			}},
			StartedAt: time.Now(),
		},
		cancel: runCancel,
		doneCh: doneCh,
	}

	rm.mu.Lock()
	rm.runs[runID] = re
	rm.mu.Unlock()

	opts := orchestrator.RunOptions{
		StateDir:     stateDir,
		DefaultModel: model,
		Quiet:        true,
		OnAwait:      "pause",
		ObserverSetup: func(b *bus.Bus) {
			rm.subscribeEvents(b, re)
		},
	}

	go rm.runOrchestrator(runCtx, runID, re, opts, doneCh)

	return runID, nil
}

// runOrchestrator calls RunAllAgents and sets the final status based on the
// outcome. Runs in a dedicated goroutine.
func (rm *RunManager) runOrchestrator(ctx context.Context, runID string, re *runEntry, opts orchestrator.RunOptions, doneCh chan struct{}) {
	defer close(doneCh)
	defer re.cancel() // release context resources when orchestrator exits

	err := rm.orchestrator.RunAllAgents(ctx, runID, opts)

	re.mu.Lock()
	defer re.mu.Unlock()

	re.info.ElapsedDuration = time.Since(re.info.StartedAt)

	// If already in a terminal state (set by event handlers), keep it.
	switch re.info.Status {
	case RunStatusCompleted, RunStatusFailed, RunStatusCancelled, RunStatusAwaitingInput:
		return
	}

	if err != nil {
		if ctx.Err() != nil {
			re.info.Status = RunStatusCancelled
		} else {
			re.info.Status = RunStatusFailed
			re.info.Result = err.Error()
		}
	} else {
		// RunAllAgents returned nil without a terminal event: treat as completed.
		re.info.Status = RunStatusCompleted
	}
}

// subscribeEvents registers event handlers on the bus to keep the RunInfo
// up to date as the orchestrator executes.
func (rm *RunManager) subscribeEvents(b *bus.Bus, re *runEntry) {
	// Track state transitions per agent.
	bus.Subscribe(b, func(e events.StateStarted) {
		re.mu.Lock()
		defer re.mu.Unlock()
		for i := range re.info.Agents {
			if re.info.Agents[i].ID == e.AgentID {
				re.info.Agents[i].CurrentState = e.StateName
				re.info.Agents[i].Status = ""
				break
			}
		}
		// Update run-level current state from the first agent.
		if len(re.info.Agents) > 0 {
			re.info.CurrentState = re.info.Agents[0].CurrentState
		}
	})

	// Track cost updates.
	bus.Subscribe(b, func(e events.StateCompleted) {
		re.mu.Lock()
		defer re.mu.Unlock()
		re.info.CostUSD = e.TotalCostUSD
		re.info.ElapsedDuration = time.Since(re.info.StartedAt)
	})

	// Track new agents from fork transitions.
	bus.Subscribe(b, func(e events.AgentSpawned) {
		re.mu.Lock()
		defer re.mu.Unlock()
		re.info.Agents = append(re.info.Agents, AgentInfo{
			ID:           e.NewAgentID,
			CurrentState: e.InitialState,
		})
	})

	// Track agent termination.
	bus.Subscribe(b, func(e events.AgentTerminated) {
		re.mu.Lock()
		defer re.mu.Unlock()
		for i := range re.info.Agents {
			if re.info.Agents[i].ID == e.AgentID {
				re.info.Agents[i].Status = "terminated"
				break
			}
		}
		// Capture the result payload from the main agent.
		if e.AgentID == "main" && e.ResultPayload != "" {
			re.info.Result = e.ResultPayload
		}
	})

	// Track agent pauses.
	bus.Subscribe(b, func(e events.AgentPaused) {
		re.mu.Lock()
		defer re.mu.Unlock()
		for i := range re.info.Agents {
			if re.info.Agents[i].ID == e.AgentID {
				re.info.Agents[i].Status = "paused"
				break
			}
		}
	})

	// Track await start (agent is waiting for human input).
	bus.Subscribe(b, func(e events.AgentAwaitStarted) {
		re.mu.Lock()
		defer re.mu.Unlock()
		for i := range re.info.Agents {
			if re.info.Agents[i].ID == e.AgentID {
				re.info.Agents[i].Status = wfstate.AgentStatusAwaiting
				break
			}
		}
	})

	// Workflow completed: all agents terminated.
	bus.Subscribe(b, func(e events.WorkflowCompleted) {
		re.mu.Lock()
		defer re.mu.Unlock()
		re.info.Status = RunStatusCompleted
		re.info.CostUSD = e.TotalCostUSD
		re.info.ElapsedDuration = time.Since(re.info.StartedAt)
	})

	// Workflow paused: check if it's awaiting input or just paused.
	bus.Subscribe(b, func(e events.WorkflowPaused) {
		re.mu.Lock()
		defer re.mu.Unlock()
		re.info.CostUSD = e.TotalCostUSD
		re.info.ElapsedDuration = time.Since(re.info.StartedAt)

		// If any agent is in awaiting status, the run is awaiting input.
		for _, a := range re.info.Agents {
			if a.Status == wfstate.AgentStatusAwaiting {
				re.info.Status = RunStatusAwaitingInput
				return
			}
		}
		// Otherwise it's a failure-pause (rate limit, error, etc.).
		re.info.Status = RunStatusFailed
	})
}

// GetRun returns a snapshot of the run's current state.
func (rm *RunManager) GetRun(runID string) (*RunInfo, bool) {
	rm.mu.RLock()
	re, ok := rm.runs[runID]
	rm.mu.RUnlock()
	if !ok {
		return nil, false
	}

	re.mu.Lock()
	info := re.info
	// Copy the agents slice to avoid sharing internal state.
	info.Agents = make([]AgentInfo, len(re.info.Agents))
	copy(info.Agents, re.info.Agents)
	// Update elapsed for running workflows.
	if info.Status == RunStatusRunning {
		info.ElapsedDuration = time.Since(info.StartedAt)
	}
	re.mu.Unlock()

	return &info, true
}

// ListRuns returns snapshots of all tracked runs.
func (rm *RunManager) ListRuns() []RunInfo {
	rm.mu.RLock()
	entries := make([]*runEntry, 0, len(rm.runs))
	for _, re := range rm.runs {
		entries = append(entries, re)
	}
	rm.mu.RUnlock()

	result := make([]RunInfo, 0, len(entries))
	for _, re := range entries {
		re.mu.Lock()
		info := re.info
		info.Agents = make([]AgentInfo, len(re.info.Agents))
		copy(info.Agents, re.info.Agents)
		if info.Status == RunStatusRunning {
			info.ElapsedDuration = time.Since(info.StartedAt)
		}
		re.mu.Unlock()
		result = append(result, info)
	}
	return result
}

// CancelRun cancels a running workflow by cancelling its context.
func (rm *RunManager) CancelRun(runID string) error {
	rm.mu.RLock()
	re, ok := rm.runs[runID]
	rm.mu.RUnlock()
	if !ok {
		return fmt.Errorf("run %q not found", runID)
	}

	re.mu.Lock()
	if re.cancel == nil {
		re.mu.Unlock()
		return fmt.Errorf("run %q is not active (recovered run not yet resumed)", runID)
	}
	status := re.info.Status
	re.mu.Unlock()

	switch status {
	case RunStatusCompleted, RunStatusFailed, RunStatusCancelled:
		return fmt.Errorf("run %q already in terminal state %q", runID, status)
	}

	re.cancel()

	// Wait briefly for the goroutine to acknowledge cancellation and set status.
	select {
	case <-re.doneCh:
	case <-time.After(5 * time.Second):
	}

	re.mu.Lock()
	switch re.info.Status {
	case RunStatusRunning, RunStatusAwaitingInput:
		re.info.Status = RunStatusCancelled
		re.info.ElapsedDuration = time.Since(re.info.StartedAt)
	}
	re.mu.Unlock()

	return nil
}

// WaitForCompletion blocks until the run reaches a terminal state or the
// timeout expires. A zero timeout waits indefinitely.
func (rm *RunManager) WaitForCompletion(runID string, timeout time.Duration) (*RunInfo, error) {
	rm.mu.RLock()
	re, ok := rm.runs[runID]
	rm.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("run %q not found", runID)
	}

	if timeout > 0 {
		select {
		case <-re.doneCh:
		case <-time.After(timeout):
			return nil, fmt.Errorf("timeout waiting for run %q to complete", runID)
		}
	} else {
		<-re.doneCh
	}

	re.mu.Lock()
	info := re.info
	info.Agents = make([]AgentInfo, len(re.info.Agents))
	copy(info.Agents, re.info.Agents)
	re.mu.Unlock()

	return &info, nil
}

// recoverRuns scans the state directory for existing workflow state files and
// registers them in the run manager's tracking. Recovered runs are not
// actively running — they represent interrupted workflows from a previous
// daemon instance.
func (rm *RunManager) recoverRuns() error {
	ids, err := wfstate.RecoverWorkflows(rm.stateDir)
	if err != nil {
		return err
	}

	stateDir := wfstate.GetStateDir(rm.stateDir)

	for _, id := range ids {
		ws, err := wfstate.ReadState(id, stateDir)
		if err != nil {
			continue // skip unreadable state files
		}

		agents := make([]AgentInfo, len(ws.Agents))
		for i, a := range ws.Agents {
			agents[i] = AgentInfo{
				ID:           a.ID,
				CurrentState: a.CurrentState,
				Status:       a.Status,
			}
		}

		// Determine status based on agent states.
		status := classifyRecoveredStatus(ws.Agents)

		doneCh := make(chan struct{})
		close(doneCh) // recovered runs are not active

		re := &runEntry{
			info: RunInfo{
				RunID:      id,
				WorkflowID: ws.WorkflowID,
				Status:     status,
				Agents:     agents,
				CostUSD:    ws.TotalCostUSD,
				StartedAt:  time.Time{}, // unknown for recovered runs
			},
			cancel: nil,
			doneCh: doneCh,
		}

		// Set CurrentState from the first agent.
		if len(agents) > 0 {
			re.info.CurrentState = agents[0].CurrentState
		}

		rm.runs[id] = re
	}
	return nil
}

// classifyRecoveredStatus determines the status for a recovered run based on
// the persisted agent states.
func classifyRecoveredStatus(agents []wfstate.AgentState) string {
	if len(agents) == 0 {
		return RunStatusCompleted
	}

	for _, a := range agents {
		if a.Status == wfstate.AgentStatusAwaiting {
			return RunStatusAwaitingInput
		}
	}

	return RunStatusFailed
}
