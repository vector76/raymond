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

// progressBufferSize is the buffer for each per-subscriber Progress channel.
// The coordinator emits at most one value per TierPhase (4 total: Tier1Wait,
// optional Tier2Quiesce, optional ForceKill, Complete) and then closes, so a
// buffer this size makes every send non-blocking even with no consumer and
// is also large enough to replay every prior phase to a late subscriber.
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
// Subscribe-or-start contract (bead-10): concurrent calls to Run share one
// in-flight sequence and all observe the same ShutdownResult. The first
// caller drives the tier sequence; subsequent callers block on doneCh and
// return the same recorded result.
//
// Progress fan-out (bead-10): each Progress() call returns a freshly-made
// per-subscriber channel. Phases the sequence has already emitted are
// replayed into the new channel's buffer so a late subscriber still observes
// the full stream. All subscriber channels close together after PhaseComplete.
type ShutdownCoordinator struct {
	runs      runFleet
	signal    *ShutdownSignal
	eventSink func(any) // bead-15 wires this to Server.PublishGlobalEvent

	// mu guards every field below. It is taken only at sequence
	// transitions (start, phase emission, completion) and on Progress()
	// subscription, so contention is negligible.
	mu sync.Mutex

	// started flips true under mu when the first Run goroutine claims
	// the sequence. Subsequent callers see it true and become subscribers.
	started bool
	// doneCh closes once the sequence finishes and result is fully visible.
	doneCh chan struct{}
	// result is the final ShutdownResult. Set before doneCh is closed; the
	// close→receive happens-before edge makes it safely readable by any
	// subscriber that has observed doneCh closed.
	result ShutdownResult

	// phasesSeen records the phases already emitted, in order, so a
	// Progress() call after the sequence began can replay them.
	phasesSeen []TierPhase
	// phasesClosed is set true after the final PhaseComplete fan-out;
	// later Progress() callers receive a pre-closed channel containing
	// the replayed history.
	phasesClosed bool
	// subscribers are the still-open per-Progress() channels that pending
	// fan-out will write to.
	subscribers []chan TierPhase
}

// NewShutdownCoordinator constructs a coordinator. eventSink may be nil
// (tests that don't care about global events can pass nil).
func NewShutdownCoordinator(runs runFleet, sig *ShutdownSignal, sink func(any)) *ShutdownCoordinator {
	return &ShutdownCoordinator{
		runs:      runs,
		signal:    sig,
		eventSink: sink,
		doneCh:    make(chan struct{}),
	}
}

// Progress returns a fresh channel on which TierPhase transitions for the
// coordinator's single in-flight sequence are streamed. Each call returns a
// new, independently-buffered channel; phases emitted before this call are
// replayed into the channel's buffer so every subscriber observes the same
// sequence (Tier1Wait, optionally Tier2Quiesce / ForceKill, then Complete).
// All subscriber channels close together after PhaseComplete.
func (c *ShutdownCoordinator) Progress() <-chan TierPhase {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan TierPhase, progressBufferSize)
	// Replay history. The buffer is sized to hold every possible phase so
	// these sends never block even when called after PhaseComplete.
	for _, p := range c.phasesSeen {
		ch <- p
	}
	if c.phasesClosed {
		close(ch)
		return ch
	}
	c.subscribers = append(c.subscribers, ch)
	return ch
}

// emitPhase fans p out to every current subscriber and records it in the
// replay history. Must be called from the sequence-driver goroutine only.
func (c *ShutdownCoordinator) emitPhase(p TierPhase) {
	c.mu.Lock()
	c.phasesSeen = append(c.phasesSeen, p)
	subs := append([]chan TierPhase(nil), c.subscribers...)
	c.mu.Unlock()
	for _, s := range subs {
		// Buffer is progressBufferSize == total possible phases, so this
		// is non-blocking even for a subscriber that never reads.
		s <- p
	}
}

// closeProgressSubscribers closes every per-subscriber channel and marks
// the coordinator's progress stream terminal. Late Progress() callers will
// receive a pre-closed channel containing the full replayed history.
func (c *ShutdownCoordinator) closeProgressSubscribers() {
	c.mu.Lock()
	c.phasesClosed = true
	subs := c.subscribers
	c.subscribers = nil
	c.mu.Unlock()
	for _, s := range subs {
		close(s)
	}
}

// Run executes the three-tier sequence with the supplied per-tier timeouts.
// It blocks until the sequence finishes (either all runs drained or the kill
// patience window expired) and returns the per-run outcome map.
//
// Order is deliberate: signal.Request() runs before the T1 timer starts so
// that any shell step launched within the T1 window observes the shutdown
// env vars (bead-5).
func (c *ShutdownCoordinator) Run(ctx context.Context, t1, t2 time.Duration) ShutdownResult {
	// Subscribe-or-start: the first caller claims the sequence; everyone
	// else attaches to the in-flight one and returns the same result once
	// it finishes. doneCh closing acts as the publication barrier for
	// c.result (the receive happens-after the close, which happens-after
	// the result write).
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		<-c.doneCh
		return c.result
	}
	c.started = true
	c.mu.Unlock()

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
	c.emitPhase(PhaseTier1Wait)

	select {
	case <-c.runs.WaitAllDone(runCtx):
		// All drained during T1 — skip straight to outcome computation.
	case <-time.After(t1):
		// Step 3: T2 — graceful quiesce.
		currentTier.Store(tierQuiesce)
		c.runs.QuiesceAll()
		c.emitPhase(PhaseTier2Quiesce)

		select {
		case <-c.runs.WaitAllDone(runCtx):
			// All drained during T2.
		case <-time.After(t2):
			// Step 4: force kill.
			currentTier.Store(tierKill)
			c.runs.CancelAll()
			c.emitPhase(PhaseForceKill)

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

	// Publish the result before closing doneCh: any subscriber that wakes
	// from <-doneCh is guaranteed by Go's memory model to observe the
	// stored value (close happens-before receive, write happens-before
	// close).
	c.mu.Lock()
	c.result = result
	close(c.doneCh)
	c.mu.Unlock()

	// Final phase + subscriber close. Fan-out happens after doneCh so a
	// caller that only cares about the result can return without waiting
	// on Progress consumers.
	c.emitPhase(PhaseComplete)
	c.closeProgressSubscribers()
	return result
}
