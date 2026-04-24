package daemon

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
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

// eventLogCap is the maximum number of events retained per run for replay to
// newly-connected subscribers. Bounded so a long-running run doesn't accumulate
// unlimited events in memory.
const eventLogCap = 200

// runEntry is the internal bookkeeping for a tracked run.
type runEntry struct {
	mu           sync.Mutex
	info         RunInfo
	cancel       context.CancelFunc                // nil for recovered (non-running) entries
	doneCh       chan struct{}                      // closed when the run reaches a terminal state
	eventBus     *bus.Bus                           // set by ObserverSetup; nil for recovered runs
	busReady     chan struct{}                      // closed when eventBus is set
	awaitInputCh chan orchestrator.AwaitInput // delivers responses to pending awaits

	// logMu protects eventLog and subscribers. Held briefly when recording a
	// new event (append to eventLog + fan-out to subscribers) and when adding
	// or removing a subscriber.
	logMu       sync.Mutex
	eventLog    []any       // ring buffer of recent events for replay
	subscribers []chan any  // live subscribers; each receives a copy of every new event
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
	mu              sync.RWMutex
	runs            map[string]*runEntry
	stateDir        string
	cwd             string // daemon working directory (fallback for workDir)
	orchestrator    Orchestrator
	pendingRegistry *PendingRegistry
	awaitNotify     func(runID, inputID, prompt string) // optional, called when an agent enters <await>
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
		cancel:   runCancel,
		doneCh:   doneCh,
		busReady: make(chan struct{}),
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
			re.mu.Lock()
			re.eventBus = b
			re.mu.Unlock()
			re.startRecorder(b)
			rm.subscribeEvents(b, re)
			close(re.busReady)
		},
	}

	// When a pending registry is configured, enable daemon mode so the
	// orchestrator calls AwaitCallback instead of quiescing on <await>.
	if rm.pendingRegistry != nil {
		awaitInputCh := make(chan orchestrator.AwaitInput, 16)
		re.awaitInputCh = awaitInputCh
		opts.DaemonMode = true
		opts.AwaitInputCh = awaitInputCh
		opts.AwaitCallback = func(agentID, inputID, prompt, nextState string) {
			pi := PendingInput{
				RunID:      runID,
				AgentID:    agentID,
				InputID:    inputID,
				WorkflowID: entry.ID,
				Prompt:     prompt,
				NextState:  nextState,
				CreatedAt:  time.Now(),
			}
			if err := rm.pendingRegistry.Register(pi); err != nil {
				return // registration failed; skip notification
			}
			if rm.awaitNotify != nil {
				rm.awaitNotify(runID, inputID, prompt)
			}
		}
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

// ListRuns returns snapshots of all tracked runs, sorted newest-first by
// StartedAt with RunID as a stable tiebreaker. The sort guarantees a
// deterministic order for every consumer (HTTP, MCP, the web UI), so callers
// don't have to defend against Go map iteration shuffling the result on each
// call.
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

	sort.Slice(result, func(i, j int) bool {
		if !result[i].StartedAt.Equal(result[j].StartedAt) {
			return result[i].StartedAt.After(result[j].StartedAt)
		}
		return result[i].RunID > result[j].RunID
	})
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

// parseRunIDTimestamp returns the launch time embedded in run IDs of the form
// "workflow_YYYY-MM-DD_HH-MM-SS-MICROSECONDS[_N]" produced by
// wfstate.GenerateUniqueWorkflowID. Returns the zero time and false for IDs
// that do not match this format (e.g. user-supplied custom IDs).
//
// Used during recovery to restore StartedAt for persisted runs so that the
// daemon's run history sorts deterministically rather than by map iteration
// order.
func parseRunIDTimestamp(id string) (time.Time, bool) {
	const prefix = "workflow_"
	if !strings.HasPrefix(id, prefix) {
		return time.Time{}, false
	}
	body := id[len(prefix):]

	// Strip an optional trailing "_N" disambiguator. The canonical body has
	// exactly one underscore (between date and time); a second underscore
	// marks the start of the counter suffix.
	if strings.Count(body, "_") > 1 {
		body = body[:strings.LastIndex(body, "_")]
	}

	// body should now be "YYYY-MM-DD_HH-MM-SS-MICROSECONDS".
	dash := strings.LastIndex(body, "-")
	if dash < 0 {
		return time.Time{}, false
	}
	micros, err := strconv.Atoi(body[dash+1:])
	// Format guarantees 0 <= micros <= 999999. Reject anything outside
	// that range so an id with extra digits can't silently spill into
	// the seconds component via t.Add.
	if err != nil || micros < 0 || micros >= 1_000_000 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02_15-04-05", body[:dash], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t.Add(time.Duration(micros) * time.Microsecond), true
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

		// Prefer the StartedAt persisted in the state file (timezone-safe).
		// For older state files written before that field existed, fall
		// back to parsing the timestamp out of the run_id. Both paths give
		// the UI a stable sort key across daemon restarts.
		startedAt := ws.StartedAt
		if startedAt.IsZero() {
			startedAt, _ = parseRunIDTimestamp(id)
		}

		re := &runEntry{
			info: RunInfo{
				RunID:      id,
				WorkflowID: ws.WorkflowID,
				Status:     status,
				Agents:     agents,
				CostUSD:    ws.TotalCostUSD,
				StartedAt:  startedAt,
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

// startRecorder registers a single set of bus subscriptions that both append
// each event to the ring buffer and fan out to live subscribers. This replaces
// the previous per-subscriber bus subscription pattern so that subscribers get
// a replay of recent events plus a live stream, with a single lock serializing
// append and fan-out to preserve ordering.
func (re *runEntry) startRecorder(b *bus.Bus) {
	record := func(e any) {
		re.logMu.Lock()
		// Ring buffer with O(N) eviction. N is bounded by eventLogCap (~200);
		// the copy keeps the underlying array from growing unboundedly.
		if len(re.eventLog) >= eventLogCap {
			copy(re.eventLog, re.eventLog[1:])
			re.eventLog = re.eventLog[:eventLogCap-1]
		}
		re.eventLog = append(re.eventLog, e)
		for _, ch := range re.subscribers {
			select {
			case ch <- e:
			default:
				// Drop event if subscriber is too slow.
			}
		}
		re.logMu.Unlock()
	}

	bus.Subscribe(b, func(e events.WorkflowStarted) { record(e) })
	bus.Subscribe(b, func(e events.WorkflowCompleted) { record(e) })
	bus.Subscribe(b, func(e events.WorkflowPaused) { record(e) })
	bus.Subscribe(b, func(e events.WorkflowWaiting) { record(e) })
	bus.Subscribe(b, func(e events.WorkflowResuming) { record(e) })
	bus.Subscribe(b, func(e events.StateStarted) { record(e) })
	bus.Subscribe(b, func(e events.StateCompleted) { record(e) })
	bus.Subscribe(b, func(e events.TransitionOccurred) { record(e) })
	bus.Subscribe(b, func(e events.AgentSpawned) { record(e) })
	bus.Subscribe(b, func(e events.AgentTerminated) { record(e) })
	bus.Subscribe(b, func(e events.AgentPaused) { record(e) })
	bus.Subscribe(b, func(e events.AgentAwaitStarted) { record(e) })
	bus.Subscribe(b, func(e events.AgentAwaitResumed) { record(e) })
	bus.Subscribe(b, func(e events.ClaudeStreamOutput) { record(e) })
	bus.Subscribe(b, func(e events.ClaudeInvocationStarted) { record(e) })
	bus.Subscribe(b, func(e events.ScriptOutput) { record(e) })
	bus.Subscribe(b, func(e events.ToolInvocation) { record(e) })
	bus.Subscribe(b, func(e events.ProgressMessage) { record(e) })
	bus.Subscribe(b, func(e events.ErrorOccurred) { record(e) })
}

// SubscribeRunEvents returns a channel that receives a replay of the run's
// recent events (up to eventLogCap) followed by a live stream of any new
// events. The caller must call the returned cancel function to unsubscribe
// and release resources. The channel is closed when cancel is called or the
// run completes.
//
// For completed or recovered runs with no active bus, the returned channel
// receives the replay (which is empty for recovered runs since events are not
// persisted across daemon restarts) and then closes. No error is returned —
// the blank-log case is expressed as a clean end-of-stream.
//
// ctx is used to bound the wait for the event bus to become available on
// active runs (the bus is set asynchronously when the orchestrator starts).
func (rm *RunManager) SubscribeRunEvents(ctx context.Context, runID string) (<-chan any, func(), error) {
	rm.mu.RLock()
	re, ok := rm.runs[runID]
	rm.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("run %q not found", runID)
	}

	// Wait for the bus to be ready on active runs. Recovered runs have
	// busReady == nil; skip the wait and deliver an empty replay.
	if re.busReady != nil {
		select {
		case <-re.busReady:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}

	re.logMu.Lock()
	// Buffer sized for the initial replay plus headroom for live events so a
	// briefly-slow consumer doesn't cause drops during the snapshot-drain phase.
	ch := make(chan any, len(re.eventLog)+256)
	for _, e := range re.eventLog {
		ch <- e
	}
	re.subscribers = append(re.subscribers, ch)
	re.logMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			re.logMu.Lock()
			for i, s := range re.subscribers {
				if s == ch {
					re.subscribers = append(re.subscribers[:i], re.subscribers[i+1:]...)
					break
				}
			}
			re.logMu.Unlock()
			close(ch)
		})
	}

	// Auto-cancel when the run completes. For already-done runs (completed
	// in this process or recovered), doneCh is already closed and this fires
	// immediately, letting the caller drain the buffered replay and see EOF.
	go func() {
		<-re.doneCh
		cancel()
	}()

	return ch, cancel, nil
}

// SetPendingRegistry configures the pending input registry. When set, runs
// are launched in daemon mode with an AwaitCallback that registers pending
// inputs automatically.
func (rm *RunManager) SetPendingRegistry(pr *PendingRegistry) {
	rm.pendingRegistry = pr
}

// SetAwaitNotifier sets an optional callback invoked when an agent enters
// <await> in daemon mode. The callback receives (runID, inputID, prompt).
// It is called from the orchestrator's main goroutine and must not block.
func (rm *RunManager) SetAwaitNotifier(fn func(runID, inputID, prompt string)) {
	rm.awaitNotify = fn
}

// DeliverInput delivers a response to a pending await input. If runID is
// non-empty, the input must belong to that run.
//
// It uses GetAndRemove to atomically claim the input, preventing duplicate
// delivery when multiple callers race on the same input ID.
func (rm *RunManager) DeliverInput(runID, inputID, response string) error {
	if rm.pendingRegistry == nil {
		return fmt.Errorf("pending registry not configured")
	}

	// Peek first to validate run ownership before the destructive remove.
	if runID != "" {
		pi, ok := rm.pendingRegistry.Get(inputID)
		if !ok {
			return fmt.Errorf("pending input %q not found", inputID)
		}
		if pi.RunID != runID {
			return fmt.Errorf("pending input %q does not belong to run %q", inputID, runID)
		}
	}

	// Atomically claim the input so no other caller can deliver it.
	pi, ok := rm.pendingRegistry.GetAndRemove(inputID)
	if !ok {
		return fmt.Errorf("pending input %q not found", inputID)
	}

	rm.mu.RLock()
	re, ok := rm.runs[pi.RunID]
	rm.mu.RUnlock()
	if !ok {
		return fmt.Errorf("run %q not found", pi.RunID)
	}

	if re.awaitInputCh == nil {
		return fmt.Errorf("run %q has no active await input channel", pi.RunID)
	}

	select {
	case re.awaitInputCh <- orchestrator.AwaitInput{InputID: inputID, Response: response}:
		return nil
	default:
		return fmt.Errorf("await input channel for run %q is full", pi.RunID)
	}
}
