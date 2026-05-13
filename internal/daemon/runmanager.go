package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	debugobs "github.com/vector76/raymond/internal/observers/debug"
	"github.com/vector76/raymond/internal/orchestrator"
	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/specifier"
	wfstate "github.com/vector76/raymond/internal/state"
)

// Run status constants.
const (
	RunStatusRunning       = "running"
	RunStatusCompleted     = "completed"
	RunStatusFailed        = "failed"
	RunStatusCancelled     = "cancelled"
	RunStatusAsking = "asking"
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

// subscriberHeadroom is added to len(eventLog) when sizing each subscriber's
// channel. It absorbs short bursts of live events while the consumer is still
// draining the replay snapshot.
const subscriberHeadroom = 1024

// Sentinel errors returned (wrapped) by RunManager methods. Callers should
// match these via errors.Is rather than inspecting the error message text.
var (
	ErrRunNotFound           = errors.New("run not found")
	ErrPendingAskNotFound  = errors.New("pending input not found")
	ErrPendingAskMismatch  = errors.New("pending input does not belong to run")
)

// runEntry is the internal bookkeeping for a tracked run.
type runEntry struct {
	mu           sync.Mutex
	info         RunInfo
	cancel       context.CancelFunc                // nil only for recovered terminal entries (history-only)
	doneCh       chan struct{}                      // closed when the run reaches a terminal state
	eventBus     *bus.Bus                           // set by ObserverSetup; nil for recovered terminal entries
	busReady     chan struct{}                      // closed when eventBus is set
	askInputCh chan orchestrator.AskInput // delivers responses to pending asks

	// stopSignalCh is closed by QuiesceAll to ask the orchestrator to
	// gracefully drain in-flight executors. nil for recovered terminal entries
	// that have no live orchestrator goroutine. stopSignalOnce guards the close
	// so repeated QuiesceAll calls are safe.
	stopSignalCh   chan struct{}
	stopSignalOnce sync.Once

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
//
// Every state-touching method on RunManager targets the serve pool
// (state.PoolServe, .raymond/serve-state/) so that daemon-managed runs are
// physically disjoint from CLI-managed runs that live in .raymond/state/.
// The stateDir field is interpreted as the pool *override*: when non-empty
// it points directly at the directory to read/write, bypassing pool
// resolution. Tests use this to inject a temp directory; production sets it
// to the resolved serve-pool path computed at construction time.
type RunManager struct {
	mu              sync.RWMutex
	runs            map[string]*runEntry
	stateDir        string // serve-pool override directory (see type doc)
	raymondDir      string // project .raymond/ directory; empty falls back to the parent of the resolved serve-pool dir
	cwd             string // daemon working directory (fallback for workDir)
	orchestrator    Orchestrator
	pendingRegistry *PendingRegistry
	askNotify     func(runID, askID, prompt string) // optional, called when an agent enters <ask>
}

// NewRunManager creates a RunManager and recovers any in-progress workflows
// found in the serve pool resolved from stateDir.
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

// NewRunManagerWithOrchestrator creates a RunManager with a custom orchestrator.
// Used by tests and by the CLI helper for `ray serve --launch`, which injects
// a noop orchestrator so its tests don't spawn real LLM work.
func NewRunManagerWithOrchestrator(stateDir string, cwd string, orch Orchestrator) (*RunManager, error) {
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

// NewRunManagerForServe is the daemon-mode constructor. It installs the
// already-constructed PendingRegistry BEFORE recovery so:
//
//   - Recovered asking-state runs see a non-nil registry on the relaunch path
//     and have their askInputCh / AskCallback wired the same way LaunchRun
//     wires a fresh run. The acceptance criterion for bead-5 is that
//     DeliverInput succeeds against a recovered ask without any per-run
//     resume call; that depends on this ordering.
//   - The dangling-record drop policy runs against the replayed registry
//     before any relaunch, so the in-memory and on-disk views are consistent
//     by the time the first orchestrator goroutine starts.
//
// pr may be nil — the daemon construction path always supplies one, but
// keeping it optional makes the constructor usable in tests that don't
// exercise the registry surface.
func NewRunManagerForServe(stateDir string, cwd string, orch Orchestrator, pr *PendingRegistry) (*RunManager, error) {
	rm := &RunManager{
		runs:            make(map[string]*runEntry),
		stateDir:        stateDir,
		cwd:             cwd,
		orchestrator:    orch,
		pendingRegistry: pr,
	}
	rm.pruneDanglingPendingEntries()
	if err := rm.recoverRuns(); err != nil {
		return nil, fmt.Errorf("recover runs: %w", err)
	}
	return rm, nil
}

// pruneDanglingPendingEntries drops pending-registry entries whose serve-pool
// state file is missing. The actual drop + log lives in
// PendingRegistry.PruneDangling — this method is just the production wiring
// that supplies the serve-pool stat predicate. Future callers (--clean flag
// in bead-7, manual cleanup tooling) reuse the same registry method.
//
// The stat predicate hits one file per distinct RunID at most (PruneDangling
// caches results) so even a registry holding many entries for many missing
// runs only walks the disk once per run.
func (rm *RunManager) pruneDanglingPendingEntries() {
	if rm.pendingRegistry == nil {
		return
	}
	poolDir := wfstate.ResolvePoolDir(wfstate.PoolServe, rm.stateDir)
	rm.pendingRegistry.PruneDangling(func(runID string) bool {
		_, err := os.Stat(filepath.Join(poolDir, runID+".json"))
		return err == nil
	})
}

// SetRaymondDir configures the project's raymond directory so DeleteRun can
// derive the per-run tasks directory (`<raymondDir>/tasks/<id>/`) directly
// rather than by stripping a segment off the state path. Called once by
// `ray serve` during daemon construction. When unset, DeleteRun falls back
// to the parent of the resolved serve-pool dir — equivalent to the
// historical path-stripping behavior — so tests that don't bother plumbing
// it explicitly keep working.
func (rm *RunManager) SetRaymondDir(dir string) {
	rm.raymondDir = dir
}

// resolvedRaymondDir returns the project's raymond directory, preferring
// the value plumbed in via SetRaymondDir. The fallback resolves the
// serve-pool directory (honouring rm.stateDir's override semantics, so an
// empty rm.stateDir does not yield filepath.Dir("") == ".") and returns
// its parent. This preserves the historical "tasks live beside the state
// dir" assumption for tests that don't bother plumbing raymondDir in.
func (rm *RunManager) resolvedRaymondDir() string {
	if rm.raymondDir != "" {
		return rm.raymondDir
	}
	return filepath.Dir(wfstate.ResolvePoolDir(wfstate.PoolServe, rm.stateDir))
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
	dangerouslySkipPermissions bool,
	workDir string,
	env map[string]string,
) (string, error) {
	// Resolve once so the orchestrator's RunOptions.StateDir, the
	// initial WriteState, and the run-ID collision check all agree on the
	// same on-disk directory. rm.stateDir acts as an override (test
	// injection); when empty the serve pool resolves to
	// .raymond/serve-state/.
	stateDir := wfstate.ResolvePoolDir(wfstate.PoolServe, rm.stateDir)

	runID, err := wfstate.GenerateWorkflowIDIn(wfstate.PoolServe, rm.stateDir)
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
		DangerouslySkipPermissions: dangerouslySkipPermissions,
		Model:                      model,
		OnAsk:                    "pause", // daemon always allows ask
	}

	ws := wfstate.CreateInitialState(runID, entry.ScopeDir, initialState, budget, inputPtr, "", lp)
	if err := wfstate.WriteStateIn(runID, ws, wfstate.PoolServe, rm.stateDir); err != nil {
		return "", fmt.Errorf("write initial state: %w", err)
	}

	runCtx, runCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	stopSignalCh := make(chan struct{})

	re := &runEntry{
		info: RunInfo{
			RunID:      runID,
			WorkflowID: entry.ID,
			Status:     RunStatusRunning,
			Agents: []AgentInfo{{
				ID:           "main",
				CurrentState: initialState,
			}},
			// Reuse the StartedAt that CreateInitialState already stamped
			// into ws so the live and recovered (post-restart) views of
			// this run report the same wall-clock launch time.
			StartedAt: ws.StartedAt,
		},
		cancel:       runCancel,
		doneCh:       doneCh,
		busReady:     make(chan struct{}),
		stopSignalCh: stopSignalCh,
	}

	rm.mu.Lock()
	rm.runs[runID] = re
	rm.mu.Unlock()

	opts := orchestrator.RunOptions{
		StateDir:                   stateDir,
		DefaultModel:               model,
		DangerouslySkipPermissions: dangerouslySkipPermissions,
		Quiet:                      true,
		OnAsk:                    "pause",
		StopSignalCh:               stopSignalCh,
		// Match the CLI's default behavior so raw Claude stream output
		// lands on disk (.raymond/debug/<run_id>/*.jsonl). Without this,
		// daemon-launched runs produce no artifact for diagnosing what
		// the agent actually emitted.
		Debug: true,
		ObserverSetup: func(b *bus.Bus) {
			re.mu.Lock()
			re.eventBus = b
			re.mu.Unlock()
			re.startRecorder(b)
			rm.subscribeEvents(b, re)
			debugobs.New(b)
			close(re.busReady)
		},
	}

	// When a pending registry is configured, enable daemon mode so the
	// orchestrator calls AskCallback instead of quiescing on <ask>.
	if rm.pendingRegistry != nil {
		askInputCh := make(chan orchestrator.AskInput, 16)
		re.askInputCh = askInputCh
		opts.DaemonMode = true
		opts.AskInputCh = askInputCh
		opts.AskCallback = func(agentID, askID, prompt, nextState string, affordance *parsing.FileAffordance, stagedFiles []wfstate.FileRecord) {
			pi := PendingAsk{
				RunID:          runID,
				AgentID:        agentID,
				AskID:        askID,
				WorkflowID:     entry.ID,
				Prompt:         prompt,
				NextState:      nextState,
				CreatedAt:      time.Now(),
				FileAffordance: affordance,
				StagedFiles:    stagedFiles,
			}
			if err := rm.pendingRegistry.Register(pi); err != nil {
				return // registration failed; skip notification
			}
			if rm.askNotify != nil {
				rm.askNotify(runID, askID, prompt)
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
	case RunStatusCompleted, RunStatusFailed, RunStatusCancelled, RunStatusAsking:
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
		// An agent that just entered a new state has cleared its ask
		// status; recompute the run-level status so the UI moves the run
		// out of "asking" when the last asking agent resumes.
		recomputeRunStatus(&re.info)
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

	// Track ask start (agent is waiting for human input).
	bus.Subscribe(b, func(e events.AgentAskStarted) {
		re.mu.Lock()
		defer re.mu.Unlock()
		for i := range re.info.Agents {
			if re.info.Agents[i].ID == e.AgentID {
				re.info.Agents[i].Status = wfstate.AgentStatusAsking
				break
			}
		}
		// Daemon mode doesn't emit WorkflowPaused for a single ask
		// (siblings keep running), so the run-level status won't flip to
		// "asking" via that path. Recompute here so the UI polls
		// the pending-inputs endpoint and surfaces an input card.
		recomputeRunStatus(&re.info)
	})

	// Workflow completed: all agents terminated.
	bus.Subscribe(b, func(e events.WorkflowCompleted) {
		re.mu.Lock()
		defer re.mu.Unlock()
		re.info.Status = RunStatusCompleted
		re.info.CostUSD = e.TotalCostUSD
		re.info.ElapsedDuration = time.Since(re.info.StartedAt)
	})

	// Workflow paused: check if it's pending an ask or just paused.
	bus.Subscribe(b, func(e events.WorkflowPaused) {
		re.mu.Lock()
		defer re.mu.Unlock()
		re.info.CostUSD = e.TotalCostUSD
		re.info.ElapsedDuration = time.Since(re.info.StartedAt)

		// If any agent is in asking status, the run is pending an ask.
		for _, a := range re.info.Agents {
			if a.Status == wfstate.AgentStatusAsking {
				re.info.Status = RunStatusAsking
				return
			}
		}
		// Otherwise it's a failure-pause (rate limit, error, etc.).
		re.info.Status = RunStatusFailed
	})
}

// recomputeRunStatus flips a non-terminal run between "running" and
// "asking" based on whether any agent currently has
// AgentStatusAsking. Terminal statuses (completed, failed, cancelled)
// are left alone — once a run is done it does not un-terminate just
// because an event handler runs late.
//
// Caller must hold re.mu.
func recomputeRunStatus(info *RunInfo) {
	switch info.Status {
	case RunStatusCompleted, RunStatusFailed, RunStatusCancelled:
		return
	}
	for _, a := range info.Agents {
		if a.Status == wfstate.AgentStatusAsking {
			info.Status = RunStatusAsking
			return
		}
	}
	info.Status = RunStatusRunning
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

// SnapshotActive returns a snapshot of currently-active (non-terminal) runs.
// "Active" means status is RunStatusRunning or RunStatusAsking — recovered
// or already-terminal runs are skipped, since the shutdown coordinator only
// needs to track runs that could still be making progress.
//
// The returned slice is owned by the caller; mutating it does not affect
// internal state. Each entry carries just the minimum the coordinator needs
// to identify and report on the run.
func (rm *RunManager) SnapshotActive() []RunSummary {
	rm.mu.RLock()
	entries := make([]*runEntry, 0, len(rm.runs))
	for _, re := range rm.runs {
		entries = append(entries, re)
	}
	rm.mu.RUnlock()

	out := make([]RunSummary, 0, len(entries))
	for _, re := range entries {
		re.mu.Lock()
		status := re.info.Status
		summary := RunSummary{
			ID:         re.info.RunID,
			WorkflowID: re.info.WorkflowID,
			Status:     status,
		}
		re.mu.Unlock()
		switch status {
		case RunStatusRunning, RunStatusAsking:
			out = append(out, summary)
		}
	}
	return out
}

// DoneCh returns the per-run done channel that closes when the run reaches
// a terminal state. Returns nil for unknown runs. Used by the shutdown
// coordinator to classify per-run outcomes by observing close timing.
func (rm *RunManager) DoneCh(runID string) <-chan struct{} {
	rm.mu.RLock()
	re, ok := rm.runs[runID]
	rm.mu.RUnlock()
	if !ok {
		return nil
	}
	return re.doneCh
}

// QuiesceAll signals every active run to drain gracefully by closing its
// per-run stop channel. The orchestrator observes the closed channel and
// stops launching new executors, letting in-flight agents reach their
// next-state boundary and persist state.
//
// Terminal-only recovered entries (cancel == nil) have no live orchestrator
// goroutine and are skipped; non-terminal recovered entries do — under
// Phase-3 auto-resume they are launched with their own stopSignalCh and
// quiesce just like LaunchRun runs. Each entry's sync.Once guards against a
// double-close panic if QuiesceAll is invoked more than once.
func (rm *RunManager) QuiesceAll() {
	rm.mu.RLock()
	entries := make([]*runEntry, 0, len(rm.runs))
	for _, re := range rm.runs {
		entries = append(entries, re)
	}
	rm.mu.RUnlock()

	for _, re := range entries {
		re.mu.Lock()
		isRecovered := re.cancel == nil
		ch := re.stopSignalCh
		re.mu.Unlock()
		if isRecovered || ch == nil {
			continue
		}
		re.stopSignalOnce.Do(func() {
			close(ch)
		})
	}
}

// CancelAll cancels every active run by invoking its context.CancelFunc.
// Terminal-only recovered entries (cancel == nil) have no live orchestrator
// goroutine and are skipped; non-terminal recovered entries are auto-resumed
// at startup and therefore carry a cancel.
//
// This is the Tier 3 force-kill surface and is intentionally distinct from
// QuiesceAll: QuiesceAll signals each run's stop channel so the orchestrator
// can drain in-flight executors to a clean boundary, leaving the context
// intact. CancelAll cuts the context immediately; in-flight work is
// abandoned. QuiesceAll does NOT call the per-run cancel.
func (rm *RunManager) CancelAll() {
	rm.mu.RLock()
	entries := make([]*runEntry, 0, len(rm.runs))
	for _, re := range rm.runs {
		entries = append(entries, re)
	}
	rm.mu.RUnlock()

	for _, re := range entries {
		re.mu.Lock()
		cancel := re.cancel
		re.mu.Unlock()
		if cancel == nil {
			continue // recovered entry, no live orchestrator
		}
		cancel()
	}
}

// WaitAllDone returns a channel that closes when every run active at call
// time has reached a terminal state, or when ctx is cancelled.
//
// The waited-on set is a *snapshot* taken under the manager lock at call
// time. Runs launched after the call do not extend the wait, and entries
// added by a future recoverRuns invocation are likewise ignored. Each entry
// in the snapshot is observed through its existing doneCh — the same channel
// WaitForCompletion blocks on — so there is no parallel bookkeeping.
//
// ctx semantics: if ctx is cancelled before every doneCh in the snapshot has
// closed, the internal waiter exits and the returned channel is closed
// early. The channel is therefore always eventually closed: either every
// run finished, or the caller asked to stop waiting. An empty snapshot
// yields an already-closed channel so callers can use the same select arm
// without a special case.
func (rm *RunManager) WaitAllDone(ctx context.Context) <-chan struct{} {
	rm.mu.RLock()
	dones := make([]chan struct{}, 0, len(rm.runs))
	for _, re := range rm.runs {
		dones = append(dones, re.doneCh)
	}
	rm.mu.RUnlock()

	out := make(chan struct{})
	if len(dones) == 0 {
		close(out)
		return out
	}

	go func() {
		defer close(out)
		for _, dc := range dones {
			select {
			case <-dc:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out
}

// CancelRun cancels a running workflow by cancelling its context.
func (rm *RunManager) CancelRun(runID string) error {
	rm.mu.RLock()
	re, ok := rm.runs[runID]
	rm.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrRunNotFound, runID)
	}

	re.mu.Lock()
	if re.cancel == nil {
		// Only recovered TERMINAL entries reach this branch under Phase-3
		// auto-resume; non-terminal recovered runs are launched with a
		// cancel of their own. Terminal entries fall through to the
		// status check below, which catches them with a clearer message.
		re.mu.Unlock()
		return fmt.Errorf("run %q is not active", runID)
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
	case RunStatusRunning, RunStatusAsking:
		re.info.Status = RunStatusCancelled
		re.info.ElapsedDuration = time.Since(re.info.StartedAt)
	}
	re.mu.Unlock()

	return nil
}

// ErrRunActive is returned by DeleteRun when the run is still running or
// pending an ask. The caller must cancel the run before it can be deleted.
var ErrRunActive = errors.New("run is active")

// DeleteRun removes a terminal run from tracking and deletes its on-disk
// artifacts: the state file (if still present) and the per-run tasks
// directory. Returns ErrRunNotFound if the run is unknown and ErrRunActive
// if the run is still running or pending an ask.
//
// A missing tasks directory is not an error (RemoveAll is idempotent), but
// a RemoveAll failure (e.g. EACCES) is surfaced so the caller can tell the
// user. The tasks directory is only cleaned up at the default layout,
// `<raymond-dir>/tasks/<workflow_id>/` (sibling of the state directory);
// runs that used a custom task_folder_pattern pointing elsewhere will need
// manual cleanup.
func (rm *RunManager) DeleteRun(runID string) error {
	rm.mu.Lock()
	re, ok := rm.runs[runID]
	if !ok {
		rm.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrRunNotFound, runID)
	}

	re.mu.Lock()
	status := re.info.Status
	re.mu.Unlock()

	switch status {
	case RunStatusRunning, RunStatusAsking:
		rm.mu.Unlock()
		return fmt.Errorf("%w: %q (status: %s); cancel it first", ErrRunActive, runID, status)
	}

	delete(rm.runs, runID)
	rm.mu.Unlock()

	// Remove the state file (idempotent if already deleted on completion).
	if err := wfstate.DeleteStateIn(runID, wfstate.PoolServe, rm.stateDir); err != nil {
		return fmt.Errorf("delete state: %w", err)
	}

	// Remove the per-run tasks directory. The tasks root lives at
	// `<raymondDir>/tasks/`, derived directly from the plumbed-in raymond
	// directory rather than by stripping a segment off the state path —
	// the pool layout no longer guarantees `state` is a single-segment
	// sibling of `tasks`. Best-effort: a missing directory is fine
	// (RemoveAll is idempotent); a RemoveAll failure is surfaced so the
	// caller can tell the user. Runs that used a custom task_folder_pattern
	// pointing elsewhere will need manual cleanup.
	tasksDir := filepath.Join(rm.resolvedRaymondDir(), "tasks", runID)
	if err := os.RemoveAll(tasksDir); err != nil {
		return fmt.Errorf("delete tasks dir %s: %w", tasksDir, err)
	}

	return nil
}

// WaitForCompletion blocks until the run reaches a terminal state or the
// timeout expires. A zero timeout waits indefinitely.
func (rm *RunManager) WaitForCompletion(runID string, timeout time.Duration) (*RunInfo, error) {
	rm.mu.RLock()
	re, ok := rm.runs[runID]
	rm.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrRunNotFound, runID)
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

// recoverRuns scans the serve pool for existing workflow state files and
// re-launches non-terminal runs through the orchestrator using their existing
// run ids and existing state files. Terminal runs (no remaining agents) are
// registered as inactive history entries the same way they always were.
//
// This is the daemon-side analogue of the CLI's `--resume` path: the
// orchestrator reads the persisted WorkflowState from disk and continues
// agent execution from where the previous daemon instance left off. No new
// run id is generated and no fresh state file is written.
//
// Malformed or unreadable state files are skipped — the id is logged and
// startup continues so a single bad file in the pool can't block the daemon.
func (rm *RunManager) recoverRuns() error {
	// Enumerate every json file in the pool (not just RecoverWorkflowsIn,
	// which silently filters malformed entries) so we can log skips
	// explicitly per the failure-mode policy.
	ids, err := wfstate.ListWorkflowsIn(wfstate.PoolServe, rm.stateDir)
	if err != nil {
		return err
	}

	for _, id := range ids {
		ws, err := wfstate.ReadStateIn(id, wfstate.PoolServe, rm.stateDir)
		if err != nil {
			// Preserve the continue-on-error pattern RecoverWorkflows
			// itself exhibits, but surface the id so an operator can
			// chase down the corrupted file after startup.
			log.Printf("daemon: skipping unreadable state file %q during recovery: %v", id, err)
			continue
		}

		// Prefer the StartedAt persisted in the state file (timezone-safe).
		// For older state files written before that field existed, fall
		// back to parsing the timestamp out of the run_id. Both paths give
		// the UI a stable sort key across daemon restarts.
		startedAt := ws.StartedAt
		if startedAt.IsZero() {
			startedAt, _ = parseRunIDTimestamp(id)
		}

		if isTerminalRecoveredState(ws) {
			rm.registerTerminalRecovered(id, ws, startedAt)
			continue
		}

		rm.relaunchRecoveredRun(id, ws, startedAt)
	}
	return nil
}

// isTerminalRecoveredState reports whether a persisted state file represents
// a workflow that has nothing left to do. Today that means "no agents
// remain" — the orchestrator deletes state on WorkflowCompleted, so a state
// file with zero agents is the lingering-artifact case rather than the
// common one. Workflows where agents are merely paused, failed, or asking
// are NOT terminal: the daemon resumes them through the orchestrator on
// startup so `failed` stays reserved for workflow-level failures rather
// than "process restarted while you were running".
func isTerminalRecoveredState(ws *wfstate.WorkflowState) bool {
	return len(ws.Agents) == 0
}

// registerTerminalRecovered installs a recovered terminal run into the
// in-memory view as an inactive history entry. Mirrors the pre-Phase-3
// behaviour so the UI still shows historical runs even when their state
// files outlived completion.
func (rm *RunManager) registerTerminalRecovered(id string, ws *wfstate.WorkflowState, startedAt time.Time) {
	agents := agentInfosFromState(ws.Agents)

	doneCh := make(chan struct{})
	close(doneCh)

	re := &runEntry{
		info: RunInfo{
			RunID:      id,
			WorkflowID: ws.WorkflowID,
			Status:     RunStatusCompleted,
			Agents:     agents,
			CostUSD:    ws.TotalCostUSD,
			StartedAt:  startedAt,
		},
		doneCh: doneCh,
	}
	if len(agents) > 0 {
		re.info.CurrentState = agents[0].CurrentState
	}

	rm.runs[id] = re
}

// relaunchRecoveredRun installs a non-terminal recovered run in the registry
// and launches the orchestrator goroutine against the existing run id and
// state file. The wiring mirrors LaunchRun (observer setup, stop-signal
// channel, shutdown-signal propagation, ask plumbing) so live and recovered
// runs publish the same events and accept input through the same surfaces.
//
// askInputCh is installed when a pending registry is configured so a
// recovered asking-state run is answerable through DeliverInput without an
// intervening per-run resume call — the in-process wiring that did not
// survive the daemon restart. The paired pending_inputs.jsonl entry has
// already been replayed by the time we get here (NewRunManagerForServe
// enforces that ordering), so DeliverInput's GetAndRemove sees the same
// PendingAsk the previous daemon instance registered.
func (rm *RunManager) relaunchRecoveredRun(id string, ws *wfstate.WorkflowState, startedAt time.Time) {
	agents := agentInfosFromState(ws.Agents)
	initialStatus := classifyRecoveredStatus(ws.Agents)

	stateDir := wfstate.ResolvePoolDir(wfstate.PoolServe, rm.stateDir)

	runCtx, runCancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	stopSignalCh := make(chan struct{})

	re := &runEntry{
		info: RunInfo{
			RunID:      id,
			WorkflowID: ws.WorkflowID,
			Status:     initialStatus,
			Agents:     agents,
			CostUSD:    ws.TotalCostUSD,
			StartedAt:  startedAt,
		},
		cancel:       runCancel,
		doneCh:       doneCh,
		busReady:     make(chan struct{}),
		stopSignalCh: stopSignalCh,
	}
	if len(agents) > 0 {
		re.info.CurrentState = agents[0].CurrentState
	}

	opts := orchestrator.RunOptions{
		StateDir:     stateDir,
		Quiet:        true,
		OnAsk:        "pause",
		StopSignalCh: stopSignalCh,
		// Match LaunchRun's default so debug artefacts still land on disk
		// for diagnosing a recovered run that misbehaves.
		Debug: true,
		ObserverSetup: func(b *bus.Bus) {
			re.mu.Lock()
			re.eventBus = b
			re.mu.Unlock()
			re.startRecorder(b)
			rm.subscribeEvents(b, re)
			debugobs.New(b)
			close(re.busReady)
		},
	}

	// Restore the launch-time flags the original `LaunchRun` persisted so
	// the recovered run sees the same model / DSP configuration its first
	// incarnation had.
	if ws.LaunchParams != nil {
		opts.DefaultModel = ws.LaunchParams.Model
		opts.DangerouslySkipPermissions = ws.LaunchParams.DangerouslySkipPermissions
	}

	// Mirror LaunchRun's ask wiring. The AskCallback's Register call is
	// effectively a no-op for the recovered-asking case (the entry was
	// replayed before recovery, so the duplicate-key Register fails silently
	// and the existing PendingAsk continues to serve DeliverInput). The
	// callback only does real work if the orchestrator later re-emits an
	// ask after this initial resume — i.e. a fresh ask in the same run —
	// which must register normally.
	if rm.pendingRegistry != nil {
		askInputCh := make(chan orchestrator.AskInput, 16)
		re.askInputCh = askInputCh
		opts.DaemonMode = true
		opts.AskInputCh = askInputCh
		workflowID := ws.WorkflowID
		opts.AskCallback = func(agentID, askID, prompt, nextState string, affordance *parsing.FileAffordance, stagedFiles []wfstate.FileRecord) {
			pi := PendingAsk{
				RunID:          id,
				AgentID:        agentID,
				AskID:          askID,
				WorkflowID:     workflowID,
				Prompt:         prompt,
				NextState:      nextState,
				CreatedAt:      time.Now(),
				FileAffordance: affordance,
				StagedFiles:    stagedFiles,
			}
			if err := rm.pendingRegistry.Register(pi); err != nil {
				return // already registered (recovered replay) or registration failed
			}
			if rm.askNotify != nil {
				rm.askNotify(id, askID, prompt)
			}
		}
	}

	rm.runs[id] = re
	go rm.runOrchestrator(runCtx, id, re, opts, doneCh)
}

// classifyRecoveredStatus returns the fresh-launch status for a non-terminal
// recovered run. An asking agent flips the run to "asking"; everything else
// is "running" because the daemon is about to launch the orchestrator
// against the persisted state. The terminal "no agents left" case is
// classified by the caller (see isTerminalRecoveredState), not here, so
// this function never returns the historical "failed" classification —
// `failed` is now reserved for genuine workflow-level failures.
func classifyRecoveredStatus(agents []wfstate.AgentState) string {
	for _, a := range agents {
		if a.Status == wfstate.AgentStatusAsking {
			return RunStatusAsking
		}
	}
	return RunStatusRunning
}

// agentInfosFromState projects persisted AgentStates onto the lighter
// AgentInfo view RunInfo carries. Extracted so the terminal and
// non-terminal recovery paths share one copy of the field projection.
func agentInfosFromState(states []wfstate.AgentState) []AgentInfo {
	agents := make([]AgentInfo, len(states))
	for i, a := range states {
		agents[i] = AgentInfo{
			ID:           a.ID,
			CurrentState: a.CurrentState,
			Status:       a.Status,
		}
	}
	return agents
}

// startRecorder registers a single set of bus subscriptions that both append
// each event to the ring buffer and fan out to live subscribers. This replaces
// the previous per-subscriber bus subscription pattern so that subscribers get
// a replay of recent events plus a live stream, with a single lock serializing
// append and fan-out to preserve ordering.
//
// Subscriptions are unsubscribed when re.doneCh closes so the closures
// (which capture re and the bus) don't pin memory beyond the run's lifetime.
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
				// Drop event if subscriber is too slow. The buffer is
				// generous (see SubscribeRunEvents) so this only triggers
				// for unusually chatty runs with a stalled SSE consumer.
			}
		}
		re.logMu.Unlock()
	}

	cancels := []func(){
		bus.Subscribe(b, func(e events.WorkflowStarted) { record(e) }),
		bus.Subscribe(b, func(e events.WorkflowCompleted) { record(e) }),
		bus.Subscribe(b, func(e events.WorkflowPaused) { record(e) }),
		bus.Subscribe(b, func(e events.WorkflowWaiting) { record(e) }),
		bus.Subscribe(b, func(e events.WorkflowResuming) { record(e) }),
		bus.Subscribe(b, func(e events.StateStarted) { record(e) }),
		bus.Subscribe(b, func(e events.StateCompleted) { record(e) }),
		bus.Subscribe(b, func(e events.TransitionOccurred) { record(e) }),
		bus.Subscribe(b, func(e events.AgentSpawned) { record(e) }),
		bus.Subscribe(b, func(e events.AgentTerminated) { record(e) }),
		bus.Subscribe(b, func(e events.AgentPaused) { record(e) }),
		bus.Subscribe(b, func(e events.AgentAskStarted) { record(e) }),
		bus.Subscribe(b, func(e events.AgentAskResumed) { record(e) }),
		bus.Subscribe(b, func(e events.ClaudeStreamOutput) { record(e) }),
		bus.Subscribe(b, func(e events.ClaudeInvocationStarted) { record(e) }),
		bus.Subscribe(b, func(e events.ScriptOutput) { record(e) }),
		bus.Subscribe(b, func(e events.ToolInvocation) { record(e) }),
		bus.Subscribe(b, func(e events.ProgressMessage) { record(e) }),
		bus.Subscribe(b, func(e events.ErrorOccurred) { record(e) }),
	}

	go func() {
		<-re.doneCh
		for _, c := range cancels {
			c()
		}
	}()
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
		return nil, nil, fmt.Errorf("%w: %q", ErrRunNotFound, runID)
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
	// Buffer sized for the initial replay plus generous headroom for live
	// events so a briefly-slow consumer doesn't cause drops during the
	// snapshot-drain phase. Drops in the recorder's fan-out (silent, by
	// design — see startRecorder) only kick in for unusually chatty runs
	// with a stalled SSE consumer.
	ch := make(chan any, len(re.eventLog)+subscriberHeadroom)
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

// BroadcastToAllRuns delivers evt to every active per-run subscriber so a
// client already attached to a per-run /runs/{id}/output stream sees a
// daemon-wide event (e.g. a shutdown frame) in-band, without having to also
// be subscribed to /events. The send to each subscriber is non-blocking,
// matching the per-event recorder's drop-on-slow-consumer policy
// (see runEntry.startRecorder). The event is intentionally not appended to
// each entry's replay ring buffer (re.eventLog): that buffer is for events
// originating on the run's own bus, and a global event delivered at time T
// to one subscriber should not retroactively appear in another subscriber's
// initial replay if they connect at T+1 — they will see a fresh /events
// stream instead.
func (rm *RunManager) BroadcastToAllRuns(evt any) {
	rm.mu.RLock()
	entries := make([]*runEntry, 0, len(rm.runs))
	for _, re := range rm.runs {
		entries = append(entries, re)
	}
	rm.mu.RUnlock()

	for _, re := range entries {
		re.logMu.Lock()
		for _, ch := range re.subscribers {
			select {
			case ch <- evt:
			default:
			}
		}
		re.logMu.Unlock()
	}
}

// LookupResolvedInput reads the workflow state for runID and returns the
// ResolvedInput record matching askID, if any. The state file is the source
// of truth after a pending input has been claimed and removed from the
// pending registry. Returns false when the state cannot be read or no such
// resolved input exists.
func (rm *RunManager) LookupResolvedInput(runID, askID string) (*wfstate.ResolvedInput, bool) {
	ws, err := wfstate.ReadStateIn(runID, wfstate.PoolServe, rm.stateDir)
	if err != nil {
		return nil, false
	}
	for i := range ws.ResolvedInputs {
		if ws.ResolvedInputs[i].AskID == askID {
			ri := ws.ResolvedInputs[i]
			return &ri, true
		}
	}
	return nil, false
}

// ListResolvedInputs reads the workflow state for runID and returns a copy of
// its ResolvedInputs history in persisted order. Returns nil and false when
// the state file cannot be read; an empty history yields an empty slice and
// true.
func (rm *RunManager) ListResolvedInputs(runID string) ([]wfstate.ResolvedInput, bool) {
	ws, err := wfstate.ReadStateIn(runID, wfstate.PoolServe, rm.stateDir)
	if err != nil {
		return nil, false
	}
	out := make([]wfstate.ResolvedInput, len(ws.ResolvedInputs))
	copy(out, ws.ResolvedInputs)
	return out, true
}

// SetPendingRegistry configures the pending input registry. When set, runs
// are launched in daemon mode with an AskCallback that registers pending
// inputs automatically.
func (rm *RunManager) SetPendingRegistry(pr *PendingRegistry) {
	rm.pendingRegistry = pr
}

// SetAskNotifier sets an optional callback invoked when an agent enters
// <ask> in daemon mode. The callback receives (runID, askID, prompt).
// It is called from the orchestrator's main goroutine and must not block.
func (rm *RunManager) SetAskNotifier(fn func(runID, askID, prompt string)) {
	rm.askNotify = fn
}

// DeliverInput delivers a response to a pending ask input. If runID is
// non-empty, the input must belong to that run. uploadedFiles carries
// metadata for any files the caller attached to the response; nil is
// equivalent to an empty slice.
//
// It uses GetAndRemove to atomically claim the input, preventing duplicate
// delivery when multiple callers race on the same input ID.
func (rm *RunManager) DeliverInput(runID, askID, response string, uploadedFiles []wfstate.FileRecord) error {
	if rm.pendingRegistry == nil {
		return fmt.Errorf("pending registry not configured")
	}

	// Peek first to validate run ownership before the destructive remove.
	if runID != "" {
		pi, ok := rm.pendingRegistry.Get(askID)
		if !ok {
			return fmt.Errorf("%w: %q", ErrPendingAskNotFound, askID)
		}
		if pi.RunID != runID {
			return fmt.Errorf("%w: input %q belongs to run %q, not %q",
				ErrPendingAskMismatch, askID, pi.RunID, runID)
		}
	}

	// Atomically claim the input so no other caller can deliver it.
	pi, ok := rm.pendingRegistry.GetAndRemove(askID)
	if !ok {
		return fmt.Errorf("%w: %q", ErrPendingAskNotFound, askID)
	}

	rm.mu.RLock()
	re, ok := rm.runs[pi.RunID]
	rm.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrRunNotFound, pi.RunID)
	}

	if re.askInputCh == nil {
		return fmt.Errorf("run %q has no active ask input channel", pi.RunID)
	}

	select {
	case re.askInputCh <- orchestrator.AskInput{AskID: askID, Response: response, UploadedFiles: uploadedFiles}:
		return nil
	default:
		return fmt.Errorf("ask input channel for run %q is full", pi.RunID)
	}
}
