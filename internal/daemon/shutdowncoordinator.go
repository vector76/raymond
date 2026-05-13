package daemon

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vector76/raymond/internal/events"
)

// TierPhase identifies which stage of the two-phase shutdown sequence the
// coordinator is currently in. Subscribers to Progress() receive one value
// per phase the coordinator actually enters; PhaseCancel is only emitted if
// EscalateToCancel() is called, which is optional.
type TierPhase string

const (
	// PhaseQuiesce — BeginQuiesce has been called: QuiesceAll() has run and
	// the coordinator is waiting (indefinitely) for in-flight runs to park at
	// their next state boundary. Emitted exactly once per sequence.
	PhaseQuiesce TierPhase = "quiesce"
	// PhaseCancel — EscalateToCancel() has been called: CancelAll() has run
	// and the coordinator is waiting up to cancelPatienceWindow for runs to
	// honour the cancel before classifying any survivors. Emitted at most
	// once per sequence.
	PhaseCancel TierPhase = "cancel"
	// PhaseComplete — terminal phase emitted just before Progress() closes.
	PhaseComplete TierPhase = "complete"
)

// RunOutcome classifies how each run reached its terminal state during
// shutdown. The coordinator records one outcome per run that was active when
// BeginQuiesce() was called.
type RunOutcome string

const (
	// OutcomeQuiesced — the run drained before EscalateToCancel() was
	// engaged. This covers both runs that exited voluntarily during quiesce
	// and runs that finished naturally between the snapshot and the cancel.
	OutcomeQuiesced RunOutcome = "quiesced"
	// OutcomeCancelled — EscalateToCancel() was engaged before the run
	// drained. Includes runs that drained after CancelAll() and runs that
	// never drained even after cancelPatienceWindow elapsed.
	OutcomeCancelled RunOutcome = "cancelled"
)

// ShutdownResult is the per-run outcome map produced once the sequence
// reaches PhaseComplete. Read it via Result() after WaitComplete() fires.
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
	// QuiesceAll signals every active run to drain gracefully.
	QuiesceAll()
	// CancelAll cancels every active run's context.
	CancelAll()
	// SnapshotActive returns a snapshot of currently-active (non-terminal)
	// runs. The slice is owned by the caller.
	SnapshotActive() []RunSummary
	// DoneCh returns the per-run done channel that closes when the run
	// reaches a terminal state. Returns nil for unknown runs (already gone).
	DoneCh(runID string) <-chan struct{}
}

// cancelPatienceWindow is the bounded grace period the coordinator gives
// goroutines to honour EscalateToCancel() before any surviving runs are
// classified as OutcomeCancelled. The 5-second target comes from the
// feature doc (docs/serve-shutdown-signals.md, §"signal mapping"): once
// cancel is engaged, the daemon waits a short, bounded window for in-flight
// goroutines to exit and then returns regardless. The constant is in code
// (not config) because it is a property of the daemon's exit contract, not
// an operator-tunable knob.
const cancelPatienceWindow = 5 * time.Second

// progressBufferSize is the buffer for each per-subscriber Progress channel.
// The coordinator emits at most one value per TierPhase — PhaseQuiesce,
// optionally PhaseCancel, then PhaseComplete (3 total) — and then closes, so
// a buffer this size makes every send non-blocking even with no consumer and
// is also large enough to replay every prior phase to a late subscriber.
const progressBufferSize = 3

// ShutdownCoordinator orchestrates the two-phase shutdown sequence:
//
//  1. BeginQuiesce: QuiesceAll() is called immediately and the coordinator
//     waits indefinitely for in-flight runs to park at their next state
//     boundary.
//  2. EscalateToCancel (optional): CancelAll() propagates context
//     cancellation; the coordinator then waits up to cancelPatienceWindow
//     for goroutines to exit before classifying any survivors as
//     OutcomeCancelled.
//
// Per-run outcomes are recorded based on whether EscalateToCancel() was
// engaged before the run's done channel closed: drained-before-cancel
// becomes OutcomeQuiesced; everything else becomes OutcomeCancelled.
//
// Subscribe-or-start contract: concurrent calls to BeginQuiesce share one
// in-flight sequence and all observe the same terminal Result(). The first
// caller drives the sequence; subsequent callers are no-ops.
//
// Progress fan-out: each Progress() call returns a freshly-made
// per-subscriber channel. Phases the sequence has already emitted are
// replayed into the new channel's buffer so a late subscriber still observes
// the full stream. All subscriber channels close together after PhaseComplete.
type ShutdownCoordinator struct {
	runs      runFleet
	eventSink func(any)

	// mu guards every field below except cancelEngagedAt (atomic) and the
	// runs/eventSink fields (immutable after construction). It is taken only
	// at sequence transitions and on Progress() subscription so contention
	// is negligible.
	mu sync.Mutex

	// started flips true under mu when BeginQuiesce first claims the
	// sequence. Subsequent BeginQuiesce calls observe true and return.
	started bool
	// cancelEngagedClosed is true once EscalateToCancel() has run. The
	// strict-no-op-if-not-started policy means EscalateToCancel before any
	// BeginQuiesce is also a no-op; we track that via !started above.
	cancelEngagedClosed bool
	// cancelEngagedCh is closed by EscalateToCancel(); the driver goroutine
	// watches it to start the patience timer. Allocated by BeginQuiesce so
	// it is non-nil for the lifetime of an in-flight sequence.
	cancelEngagedCh chan struct{}
	// cancelEngagedAt is the drain-tracker's view of whether cancel had
	// already been engaged at the moment a run's done channel closed. It's
	// atomic so trackers can read without taking mu.
	cancelEngagedAt atomic.Bool

	// doneCh closes once the sequence finishes and result is fully visible.
	doneCh chan struct{}
	// result is the final ShutdownResult. Set before doneCh is closed; the
	// close→receive happens-before edge makes it safely readable after
	// WaitComplete() fires.
	result ShutdownResult

	// phasesSeen records the phases already emitted, in order, so a late
	// Progress() subscriber can replay them.
	phasesSeen []TierPhase
	// phasesClosed is set true after the final PhaseComplete fan-out; later
	// Progress() callers receive a pre-closed channel containing the
	// replayed history.
	phasesClosed bool
	// subscribers are the still-open per-Progress() channels that pending
	// fan-out will write to.
	subscribers []chan TierPhase
}

// NewShutdownCoordinator constructs a coordinator. sink may be nil (tests
// that don't care about global events can pass nil).
func NewShutdownCoordinator(runs runFleet, sink func(any)) *ShutdownCoordinator {
	return &ShutdownCoordinator{
		runs:      runs,
		eventSink: sink,
		doneCh:    make(chan struct{}),
	}
}

// Progress returns a fresh channel on which TierPhase transitions for the
// coordinator's single in-flight sequence are streamed. Each call returns a
// new, independently-buffered channel; phases emitted before this call are
// replayed into the channel's buffer so every subscriber observes the same
// sequence (PhaseQuiesce, optionally PhaseCancel, then PhaseComplete). All
// subscriber channels close together after PhaseComplete.
func (c *ShutdownCoordinator) Progress() <-chan TierPhase {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan TierPhase, progressBufferSize)
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

// InProgress reports whether BeginQuiesce has been called. It stays true
// from the first BeginQuiesce through PhaseComplete and beyond — once a
// sequence has started, the coordinator is one-shot.
func (c *ShutdownCoordinator) InProgress() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

// WaitComplete returns a channel that closes once the sequence reaches
// PhaseComplete. Read Result() after the channel fires.
func (c *ShutdownCoordinator) WaitComplete() <-chan struct{} {
	return c.doneCh
}

// Result returns the per-run outcome map computed at PhaseComplete. Calling
// before WaitComplete() fires returns a zero-value ShutdownResult.
func (c *ShutdownCoordinator) Result() ShutdownResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}

// BeginQuiesce starts the shutdown sequence. The first caller drives it;
// subsequent callers are no-ops (the in-flight sequence's Progress() stream
// and WaitComplete() channel remain the shared observation surface). Returns
// immediately — wait for WaitComplete() to know when the sequence finishes.
//
// Order of effects on the first call: SnapshotActive() snapshots the runs
// that will be classified, ShutdownRequested is published to eventSink, the
// per-run drain trackers are wired up, QuiesceAll() is invoked, PhaseQuiesce
// is emitted, and finally the driver goroutine begins waiting on
// WaitAllDone / EscalateToCancel.
func (c *ShutdownCoordinator) BeginQuiesce(ctx context.Context) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.cancelEngagedCh = make(chan struct{})
	cancelEngagedCh := c.cancelEngagedCh
	c.mu.Unlock()

	// Snapshot before QuiesceAll so the classification accounting reflects
	// who was active at sequence-start, independent of how the QuiesceAll
	// call itself shifts state.
	snapshot := c.runs.SnapshotActive()

	// Publish ShutdownRequested. The coordinator owns payload construction
	// so the sink boundary stays a plain func(any). Tier1/Tier2 timeout
	// fields are populated with zero values; the struct fields go away in
	// bead-9 (SSE-events bead).
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
			ActiveRuns: active,
			// TODO(bead-9): drop these fields with the struct.
			Tier1TimeoutSecs: 0,
			Tier2TimeoutSecs: 0,
			RequestedAt:      time.Now(),
		})
	}

	// Per-run drain bookkeeping. Each tracker fires once when its done
	// channel closes, recording whether cancel had already been engaged at
	// that moment. classifyDone is closed when the driver finishes so
	// trackers for runs that never drained exit without leaking.
	var (
		drainMu             sync.Mutex
		drainedBeforeCancel = make(map[string]bool)
		drainedAt           = make(map[string]bool) // true if recorded
	)
	classifyDone := make(chan struct{})

	for _, s := range snapshot {
		done := c.runs.DoneCh(s.ID)
		id := s.ID
		if done == nil {
			// Run already gone by the time we asked — credit it to before
			// any cancel can possibly have been engaged.
			drainMu.Lock()
			drainedAt[id] = true
			drainedBeforeCancel[id] = true
			drainMu.Unlock()
			continue
		}
		go func(done <-chan struct{}) {
			select {
			case <-done:
				before := !c.cancelEngagedAt.Load()
				drainMu.Lock()
				if !drainedAt[id] {
					drainedAt[id] = true
					drainedBeforeCancel[id] = before
				}
				drainMu.Unlock()
			case <-classifyDone:
				// Classification window closed; tracker exits.
			}
		}(done)
	}

	// QuiesceAll runs synchronously so a test that calls BeginQuiesce can
	// inspect the fleet's quiesceCalls counter immediately. The PhaseQuiesce
	// emission follows the QuiesceAll() call so any subscriber that reads
	// PhaseQuiesce is guaranteed QuiesceAll has already run.
	c.runs.QuiesceAll()
	c.emitPhase(PhaseQuiesce)

	go c.driveSequence(ctx, snapshot, cancelEngagedCh, &drainMu, drainedAt, drainedBeforeCancel, classifyDone)
}

// driveSequence is the per-sequence goroutine. It waits for either all runs
// to drain or EscalateToCancel; if cancel is engaged it then waits up to
// cancelPatienceWindow before classifying outcomes and closing doneCh.
func (c *ShutdownCoordinator) driveSequence(
	ctx context.Context,
	snapshot []RunSummary,
	cancelEngagedCh chan struct{},
	drainMu *sync.Mutex,
	drainedAt map[string]bool,
	drainedBeforeCancel map[string]bool,
	classifyDone chan struct{},
) {
	// Scope a context for the WaitAllDone helper goroutine inside the
	// fleet so it doesn't leak past sequence completion. Cancelled in the
	// defer below.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	var (
		patienceCh <-chan time.Time
		cancelCh   = cancelEngagedCh
	)
	waitDone := c.runs.WaitAllDone(runCtx)

waitLoop:
	for {
		select {
		case <-waitDone:
			// All runs drained on their own (in quiesce, or after a cancel
			// that the runs honoured before patience expired). Either way,
			// classification proceeds and the drain trackers' before/after
			// records win.
			break waitLoop
		case <-cancelCh:
			// EscalateToCancel just fired. Start the bounded patience
			// timer; null out cancelCh so the closed channel stops firing
			// the select on every iteration.
			patienceCh = time.After(cancelPatienceWindow)
			cancelCh = nil
		case <-patienceCh:
			// Patience expired with surviving runs. They're classified as
			// OutcomeCancelled below.
			break waitLoop
		}
	}

	// Give in-flight drain trackers a brief moment to record any drains
	// that landed just before waitDone fired but whose tracker goroutine
	// hadn't yet been scheduled. The deadline is a global budget; once
	// 50ms has elapsed, any unrecorded run is classified as cancelled.
	settleDeadline := time.After(50 * time.Millisecond)
	for _, s := range snapshot {
		drainMu.Lock()
		recorded := drainedAt[s.ID]
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
			before := !c.cancelEngagedAt.Load()
			drainMu.Lock()
			if !drainedAt[s.ID] {
				drainedAt[s.ID] = true
				drainedBeforeCancel[s.ID] = before
			}
			drainMu.Unlock()
		case <-settleDeadline:
			// Stop waiting for this run; it'll be classified as cancelled.
		}
	}

	// Classify.
	outcomes := make(map[string]RunOutcome, len(snapshot))
	drainMu.Lock()
	for _, s := range snapshot {
		if drainedAt[s.ID] && drainedBeforeCancel[s.ID] {
			outcomes[s.ID] = OutcomeQuiesced
		} else {
			outcomes[s.ID] = OutcomeCancelled
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

	// Stop the drain trackers so any tracker for a run that never drained
	// exits without leaking.
	close(classifyDone)

	// Publish result + final phase + subscriber close in one critical
	// section so a late EscalateToCancel cannot squeeze a PhaseCancel
	// emission between PhaseComplete and the subscriber close (which would
	// produce a [Quiesce, Complete, Cancel] sequence on the wire). doneCh
	// is closed *after* mu releases so a WaitComplete() reader is guaranteed
	// to observe the published result.
	c.finalizeComplete(result)
	close(c.doneCh)
}

// finalizeComplete writes the final result, emits PhaseComplete to every
// subscriber, and closes every subscriber channel — all under one mutex
// hold. Subscribers seeing the channel close are guaranteed to have already
// received PhaseComplete, and any concurrent EscalateToCancel that locks mu
// after this returns will see phasesClosed and no-op.
func (c *ShutdownCoordinator) finalizeComplete(result ShutdownResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.result = result
	if c.phasesClosed {
		// Defensive: should be unreachable because driveSequence is the
		// only finalizer and runs exactly once per coordinator.
		return
	}
	c.phasesSeen = append(c.phasesSeen, PhaseComplete)
	for _, s := range c.subscribers {
		s <- PhaseComplete
	}
	c.phasesClosed = true
	for _, s := range c.subscribers {
		close(s)
	}
	c.subscribers = nil
}

// EscalateToCancel escalates an in-flight sequence to cancellation. It is
// idempotent and strict-no-op-if-not-started: if BeginQuiesce has not been
// called, or if EscalateToCancel has already engaged, the call returns
// without side effects. Otherwise it emits PhaseCancel, calls CancelAll(),
// and signals driveSequence to start the cancelPatienceWindow timer.
//
// Order is deliberate: PhaseCancel is emitted *before* any side effect that
// could let driveSequence advance to PhaseComplete. Since driveSequence only
// emits Complete after either runs.WaitAllDone fires (which CancelAll can
// trigger) or the patience timer fires (which only starts once cancelCh is
// closed), emitting PhaseCancel first under the same mutex guarantees the
// subscriber-visible order is [Quiesce, Cancel, Complete] rather than any
// permutation. cancelEngagedAt is set before CancelAll so any tracker that
// fires due to the cancel sees the flag true.
func (c *ShutdownCoordinator) EscalateToCancel() {
	c.mu.Lock()
	if !c.started || len(c.phasesSeen) == 0 || c.cancelEngagedClosed {
		// Strict no-op: an EscalateToCancel before any BeginQuiesce — or
		// during the tiny window between BeginQuiesce setting started=true
		// (under mu) and BeginQuiesce's later emit(PhaseQuiesce) call
		// (which takes mu again) — is dropped on the floor. The caller's
		// contract is to call BeginQuiesce first and let it publish
		// PhaseQuiesce before any cancel attempt; rather than implicitly
		// upgrading to a quiesce-then-cancel sequence or risking a
		// [Cancel, Quiesce] phase order on the wire, we drop the call so
		// signal-handler races don't accidentally engage cancel before
		// quiesce has had a chance to publish its event. len(phasesSeen)
		// == 0 is the cheap test for "PhaseQuiesce hasn't landed yet"
		// because PhaseQuiesce is always the first emitted phase.
		c.mu.Unlock()
		return
	}
	c.cancelEngagedClosed = true
	cancelCh := c.cancelEngagedCh
	c.mu.Unlock()

	c.emitPhase(PhaseCancel)
	c.cancelEngagedAt.Store(true)
	c.runs.CancelAll()
	close(cancelCh)
}

// emitPhase fans p out to every current subscriber and records it in the
// replay history. Holds mu through the sends so it is serialised with
// finalizeComplete (which closes the same channels) — without that, an
// EscalateToCancel emitting PhaseCancel could race with the terminal close
// and panic on send-to-closed-channel. The sends are non-blocking because
// progressBufferSize equals the total number of possible phases.
func (c *ShutdownCoordinator) emitPhase(p TierPhase) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phasesClosed {
		// Defensive: caller order guarantees this doesn't happen in normal
		// flow, but if it ever does we'd rather no-op than panic.
		return
	}
	c.phasesSeen = append(c.phasesSeen, p)
	for _, s := range c.subscribers {
		// Buffer is progressBufferSize == total possible phases, so this
		// is non-blocking even for a subscriber that never reads.
		s <- p
	}
}
