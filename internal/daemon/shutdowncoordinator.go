package daemon

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vector76/raymond/internal/events"
)

// TierPhase identifies which stage of the three-tier shutdown sequence the
// coordinator is currently in. Subscribers to Progress() receive one value per
// phase the coordinator actually enters; phases that are skipped (e.g. T2 when
// every run drained during T1) are not emitted.
type TierPhase string

const (
	// PhaseTier1Wait — the daemon has flipped the shutdown signal and is
	// waiting up to T1 for in-flight runs to finish on their own.
	PhaseTier1Wait TierPhase = "tier_1_wait"
	// PhaseTier2Quiesce — T1 elapsed with surviving runs; QuiesceAll has been
	// called and we are waiting up to T2 for orchestrators to drain to a
	// state boundary.
	PhaseTier2Quiesce TierPhase = "tier_2_quiesce"
	// PhaseForceKill — T2 elapsed with surviving runs; CancelAll has been
	// invoked and we are giving goroutines a small bounded patience window
	// to exit.
	PhaseForceKill TierPhase = "force_kill"
	// PhaseComplete — terminal phase emitted just before Progress() closes.
	PhaseComplete TierPhase = "complete"
)

// RunOutcome classifies how each run reached its terminal state during
// shutdown. The coordinator records one outcome per run that was active when
// shutdown was requested.
type RunOutcome string

const (
	// OutcomeClean — the run drained on its own during the T1 window.
	OutcomeClean RunOutcome = "clean"
	// OutcomeQuiesced — the run drained after QuiesceAll was called, during
	// the T2 window.
	OutcomeQuiesced RunOutcome = "quiesced"
	// OutcomeKilled — the run was still alive at T2 expiry and required
	// CancelAll. Includes runs that never drained even after the kill
	// patience window.
	OutcomeKilled RunOutcome = "killed"
)

// ShutdownResult is returned by ShutdownCoordinator.Run. Outcomes maps each
// run that was active at shutdown-request time to its classified outcome.
type ShutdownResult struct {
	Outcomes map[string]RunOutcome
}

// RunSummary is a minimal snapshot of a tracked run, used both to seed the
// ShutdownRequested event payload and to give the coordinator something
// concrete to iterate over when classifying outcomes.
type RunSummary struct {
	ID         string
	WorkflowID string
	Status     string
}

// Compile-time guarantee that the production *RunManager satisfies runFleet,
// so callers can pass it directly to NewShutdownCoordinator without an
// adapter shim.
var _ runFleet = (*RunManager)(nil)

// runFleet is the slice of *RunManager that the shutdown coordinator depends
// on. Defining it here (rather than taking *RunManager directly) keeps the
// coordinator testable with a tiny in-memory fake.
type runFleet interface {
	// WaitAllDone returns a channel that closes once every run active at
	// call time has reached a terminal state (or ctx is cancelled).
	WaitAllDone(ctx context.Context) <-chan struct{}
	// QuiesceAll signals every active run to drain gracefully — Tier 2.
	QuiesceAll()
	// CancelAll cancels every active run's context — Tier 3 force kill.
	CancelAll()
	// SnapshotActive returns a snapshot of currently-active (non-terminal)
	// runs. The slice is owned by the caller.
	SnapshotActive() []RunSummary
	// DoneCh returns the per-run done channel that closes when the run
	// reaches a terminal state. Returns nil for unknown runs (already gone).
	DoneCh(runID string) <-chan struct{}
}

// killPatienceWindow is the bounded grace period the coordinator gives
// goroutines to exit after CancelAll. Kept short — by this point we have
// already waited T1+T2.
const killPatienceWindow = 10 * time.Second

// progressBufferSize is the buffer for the Progress channel. The coordinator
// emits at most one value per TierPhase (4 total) and then closes, so a
// buffer this size makes every send non-blocking even with no consumer.
const progressBufferSize = 4

// ShutdownCoordinator orchestrates the three-tier shutdown sequence:
//
//  1. Flip the ShutdownSignal (so new shell steps see the env vars and the
//     sentinel exists on disk) and wait up to T1 for runs to drain on their
//     own.
//  2. If runs survive T1, call QuiesceAll() and wait up to T2 for graceful
//     drain.
//  3. If runs still survive, call CancelAll() and wait a short bounded
//     window for goroutines to exit.
//
// Per-run outcomes are recorded based on which tier was current when the
// run's done channel closed.
//
// This bead intentionally leaves Run non-reentrant; bead-10 will add
// subscribe-or-start so multiple concurrent callers share one in-flight
// sequence.
type ShutdownCoordinator struct {
	runs      runFleet
	signal    *ShutdownSignal
	eventSink func(any) // bead-15 wires this to Server.PublishGlobalEvent

	progress chan TierPhase
}

// NewShutdownCoordinator constructs a coordinator. eventSink may be nil
// (tests that don't care about global events can pass nil).
func NewShutdownCoordinator(runs runFleet, sig *ShutdownSignal, sink func(any)) *ShutdownCoordinator {
	return &ShutdownCoordinator{
		runs:      runs,
		signal:    sig,
		eventSink: sink,
		progress:  make(chan TierPhase, progressBufferSize),
	}
}

// Progress returns the channel on which TierPhase transitions are streamed.
// The coordinator sends exactly the phases it enters (Tier1Wait, then any of
// Tier2Quiesce / ForceKill that actually fire, finishing with Complete) and
// then closes the channel. Subscribers may iterate with for-range.
func (c *ShutdownCoordinator) Progress() <-chan TierPhase {
	return c.progress
}

// Run executes the three-tier sequence with the supplied per-tier timeouts.
// It blocks until the sequence finishes (either all runs drained or the kill
// patience window expired) and returns the per-run outcome map.
//
// Order is deliberate: signal.Request() runs before the T1 timer starts so
// that any shell step launched within the T1 window observes the shutdown
// env vars (bead-5).
func (c *ShutdownCoordinator) Run(ctx context.Context, t1, t2 time.Duration) ShutdownResult {
	// Snapshot the active runs *before* flipping the signal. The snapshot
	// is what we'll classify outcomes against; runs launched after this
	// point are not part of this shutdown's accounting.
	snapshot := c.runs.SnapshotActive()

	// Step 1: flip the signal first, so new shell steps that race with
	// shutdown see the env vars. signal.Request() also writes the on-disk
	// sentinel as a side effect (see ShutdownSignal.Request).
	c.signal.Request()

	// Publish ShutdownRequested. The coordinator owns payload construction
	// so the sink boundary stays a plain func(any).
	if c.eventSink != nil {
		active := make([]events.ActiveRunSnapshot, 0, len(snapshot))
		for _, s := range snapshot {
			active = append(active, events.ActiveRunSnapshot{
				ID:       s.ID,
				Workflow: s.WorkflowID,
				Status:   s.Status,
			})
		}
		c.eventSink(events.ShutdownRequested{
			ActiveRuns:       active,
			Tier1TimeoutSecs: t1.Seconds(),
			Tier2TimeoutSecs: t2.Seconds(),
			RequestedAt:      time.Now(),
		})
	}

	// Derive a coordinator-scoped context for the WaitAllDone goroutines
	// inside RunManager so they exit promptly when Run returns, even if a
	// run's done channel never closes (in pathological cases beyond the
	// kill patience window). Without this, those goroutines would linger
	// for the lifetime of the unclosed channel.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// currentTier tracks the active phase so the per-run drain trackers
	// can stamp each outcome with the tier in effect at drain time. Using
	// an atomic avoids any lock between the main goroutine's tier
	// transitions and the trackers' reads.
	var currentTier atomic.Int32
	const (
		tierWait    = int32(1)
		tierQuiesce = int32(2)
		tierKill    = int32(3)
	)
	currentTier.Store(tierWait)

	// Per-run drain bookkeeping. Each tracker fires once when its done
	// channel closes, recording the tier in effect at that moment. classifyDone
	// is closed at the end of the sequence so trackers for runs that never
	// drained can exit without leaking.
	var drainMu sync.Mutex
	drainedAtTier := make(map[string]int32)
	classifyDone := make(chan struct{})
	defer close(classifyDone)

	for _, s := range snapshot {
		done := c.runs.DoneCh(s.ID)
		if done == nil {
			// Run already gone by the time we asked — credit it to the
			// tier currently in effect (still tierWait at this point).
			drainMu.Lock()
			drainedAtTier[s.ID] = currentTier.Load()
			drainMu.Unlock()
			continue
		}
		go func() {
			select {
			case <-done:
				tier := currentTier.Load()
				drainMu.Lock()
				// Guard against overwriting a value the settle loop
				// (below) may have already recorded. First writer wins.
				if _, ok := drainedAtTier[s.ID]; !ok {
					drainedAtTier[s.ID] = tier
				}
				drainMu.Unlock()
			case <-classifyDone:
				// Classification window closed; tracker exits without
				// recording. The run's outcome is already "killed".
			}
		}()
	}

	// Step 2: T1 wait.
	c.progress <- PhaseTier1Wait

	select {
	case <-c.runs.WaitAllDone(runCtx):
		// All drained during T1 — skip straight to outcome computation.
	case <-time.After(t1):
		// Step 3: T2 — graceful quiesce.
		currentTier.Store(tierQuiesce)
		c.runs.QuiesceAll()
		c.progress <- PhaseTier2Quiesce

		select {
		case <-c.runs.WaitAllDone(runCtx):
			// All drained during T2.
		case <-time.After(t2):
			// Step 4: force kill.
			currentTier.Store(tierKill)
			c.runs.CancelAll()
			c.progress <- PhaseForceKill

			// Bounded patience for goroutines to honour the cancel.
			select {
			case <-c.runs.WaitAllDone(runCtx):
			case <-time.After(killPatienceWindow):
			}
		}
	}

	// Give drain trackers a brief moment to record any drains that
	// happened just before the wait returned but whose tracker goroutine
	// hadn't yet been scheduled. The deadline is a single global budget
	// (not per-run): once 50ms has elapsed, any unrecorded run is
	// classified as killed regardless. Per-run we still skip immediately
	// once we observe the value is recorded.
	drainSettleDeadline := time.After(50 * time.Millisecond)
	for _, s := range snapshot {
		drainMu.Lock()
		_, recorded := drainedAtTier[s.ID]
		drainMu.Unlock()
		if recorded {
			continue
		}
		done := c.runs.DoneCh(s.ID)
		if done == nil {
			continue
		}
		select {
		case <-done:
			tier := currentTier.Load()
			drainMu.Lock()
			if _, ok := drainedAtTier[s.ID]; !ok {
				drainedAtTier[s.ID] = tier
			}
			drainMu.Unlock()
		case <-drainSettleDeadline:
			// Stop waiting for this run; it'll be classified as killed.
		}
	}

	// Step 5: classify each run.
	outcomes := make(map[string]RunOutcome, len(snapshot))
	drainMu.Lock()
	for _, s := range snapshot {
		tier, ok := drainedAtTier[s.ID]
		if !ok {
			outcomes[s.ID] = OutcomeKilled
			continue
		}
		switch tier {
		case tierWait:
			outcomes[s.ID] = OutcomeClean
		case tierQuiesce:
			outcomes[s.ID] = OutcomeQuiesced
		case tierKill:
			outcomes[s.ID] = OutcomeKilled
		default:
			outcomes[s.ID] = OutcomeKilled
		}
	}
	drainMu.Unlock()

	result := ShutdownResult{Outcomes: outcomes}

	if c.eventSink != nil {
		out := make(map[string]string, len(outcomes))
		for k, v := range outcomes {
			out[k] = string(v)
		}
		c.eventSink(events.ShutdownComplete{Outcomes: out})
	}

	c.progress <- PhaseComplete
	close(c.progress)
	return result
}
