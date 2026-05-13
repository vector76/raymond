package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunFleet is an in-memory runFleet used by ShutdownCoordinator tests.
// Each "run" is identified by its ID and exposes a done channel the test
// closes manually to simulate the run reaching a terminal state. Quiesce and
// Cancel are recorded plus optionally trigger drains for runs flagged to
// drain on those signals.
type fakeRunFleet struct {
	mu sync.Mutex

	runs map[string]*fakeRunEntry

	quiesceCalls int
	cancelCalls  int
}

type fakeRunEntry struct {
	id         string
	workflowID string
	status     string
	done       chan struct{}
	doneOnce   sync.Once

	// drainOnQuiesce / drainOnCancel — when set, calling QuiesceAll / CancelAll
	// closes this run's done channel synchronously.
	drainOnQuiesce bool
	drainOnCancel  bool
}

func newFakeFleet() *fakeRunFleet {
	return &fakeRunFleet{runs: make(map[string]*fakeRunEntry)}
}

// addRun registers a run with the fleet. drainOnQuiesce / drainOnCancel
// control the simulated reaction to those tier transitions.
func (f *fakeRunFleet) addRun(id string, drainOnQuiesce, drainOnCancel bool) *fakeRunEntry {
	r := &fakeRunEntry{
		id:             id,
		workflowID:     "wf-" + id,
		status:         RunStatusRunning,
		done:           make(chan struct{}),
		drainOnQuiesce: drainOnQuiesce,
		drainOnCancel:  drainOnCancel,
	}
	f.mu.Lock()
	f.runs[id] = r
	f.mu.Unlock()
	return r
}

// drain marks a run as terminal by closing its done channel. Idempotent.
func (f *fakeRunFleet) drain(id string) {
	f.mu.Lock()
	r, ok := f.runs[id]
	f.mu.Unlock()
	if !ok {
		return
	}
	r.doneOnce.Do(func() { close(r.done) })
}

func (f *fakeRunFleet) WaitAllDone(ctx context.Context) <-chan struct{} {
	f.mu.Lock()
	dones := make([]chan struct{}, 0, len(f.runs))
	for _, r := range f.runs {
		dones = append(dones, r.done)
	}
	f.mu.Unlock()

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

func (f *fakeRunFleet) QuiesceAll() {
	f.mu.Lock()
	f.quiesceCalls++
	targets := make([]*fakeRunEntry, 0, len(f.runs))
	for _, r := range f.runs {
		if r.drainOnQuiesce {
			targets = append(targets, r)
		}
	}
	f.mu.Unlock()
	for _, r := range targets {
		r.doneOnce.Do(func() { close(r.done) })
	}
}

func (f *fakeRunFleet) CancelAll() {
	f.mu.Lock()
	f.cancelCalls++
	targets := make([]*fakeRunEntry, 0, len(f.runs))
	for _, r := range f.runs {
		if r.drainOnCancel {
			targets = append(targets, r)
		}
	}
	f.mu.Unlock()
	for _, r := range targets {
		r.doneOnce.Do(func() { close(r.done) })
	}
}

func (f *fakeRunFleet) SnapshotActive() []RunSummary {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RunSummary, 0, len(f.runs))
	for _, r := range f.runs {
		// Only include runs that haven't drained yet.
		select {
		case <-r.done:
			continue
		default:
		}
		out = append(out, RunSummary{
			ID:         r.id,
			WorkflowID: r.workflowID,
			Status:     r.status,
		})
	}
	return out
}

func (f *fakeRunFleet) DoneCh(runID string) <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[runID]
	if !ok {
		return nil
	}
	return r.done
}

// collectProgress drains the progress channel into a slice (caller-side
// guard: the channel must close, otherwise this hangs).
func collectProgress(ch <-chan TierPhase) []TierPhase {
	var got []TierPhase
	for p := range ch {
		got = append(got, p)
	}
	return got
}

// TestShutdownCoordinator_BeginQuiesce_EmitsQuiescePhaseImmediately checks
// the BeginQuiesce contract: QuiesceAll is called synchronously and
// PhaseQuiesce is emitted before BeginQuiesce returns to its caller. A
// subscriber created before the call observes PhaseQuiesce in its stream.
func TestShutdownCoordinator_BeginQuiesce_EmitsQuiescePhaseImmediately(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", true, false)
	c := NewShutdownCoordinator(fleet, nil)

	progress := c.Progress()
	c.BeginQuiesce(context.Background())

	fleet.mu.Lock()
	assert.Equal(t, 1, fleet.quiesceCalls,
		"BeginQuiesce must call QuiesceAll synchronously")
	fleet.mu.Unlock()

	select {
	case p := <-progress:
		assert.Equal(t, PhaseQuiesce, p)
	case <-time.After(time.Second):
		t.Fatal("expected PhaseQuiesce on Progress channel")
	}

	<-c.WaitComplete()
}

// TestShutdownCoordinator_InProgress_TransitionsOnBegin verifies the
// predicate flips from false to true at BeginQuiesce and stays true.
func TestShutdownCoordinator_InProgress_TransitionsOnBegin(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", true, false)
	c := NewShutdownCoordinator(fleet, nil)

	assert.False(t, c.InProgress(), "InProgress must be false before BeginQuiesce")

	c.BeginQuiesce(context.Background())
	assert.True(t, c.InProgress(), "InProgress must be true after BeginQuiesce")

	<-c.WaitComplete()
	assert.True(t, c.InProgress(),
		"InProgress remains true after completion; the coordinator is one-shot")
}

// TestShutdownCoordinator_BeginQuiesce_Idempotent: concurrent BeginQuiesce
// callers must share one sequence. QuiesceAll fires exactly once, all
// callers observe the same Result, and a single PhaseQuiesce is emitted.
func TestShutdownCoordinator_BeginQuiesce_Idempotent(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", true, false)
	fleet.addRun("b", true, false)
	c := NewShutdownCoordinator(fleet, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.BeginQuiesce(context.Background())
	}()
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		c.BeginQuiesce(context.Background())
	}()
	wg.Wait()

	<-c.WaitComplete()

	fleet.mu.Lock()
	assert.Equal(t, 1, fleet.quiesceCalls,
		"QuiesceAll must be invoked exactly once across concurrent BeginQuiesce callers")
	assert.Equal(t, 0, fleet.cancelCalls,
		"CancelAll must not fire when quiesce drains everything")
	fleet.mu.Unlock()

	r := c.Result()
	assert.Equal(t, OutcomeQuiesced, r.Outcomes["a"])
	assert.Equal(t, OutcomeQuiesced, r.Outcomes["b"])
}

// TestShutdownCoordinator_EscalateToCancel_Idempotent: concurrent
// EscalateToCancel callers (and re-calls) must produce exactly one CancelAll
// and one PhaseCancel emission.
func TestShutdownCoordinator_EscalateToCancel_Idempotent(t *testing.T) {
	fleet := newFakeFleet()
	// Run drains only on cancel, so quiesce alone never completes.
	fleet.addRun("a", false, true)
	c := NewShutdownCoordinator(fleet, nil)

	progress := c.Progress()
	c.BeginQuiesce(context.Background())

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.EscalateToCancel()
		}()
	}
	wg.Wait()

	<-c.WaitComplete()

	fleet.mu.Lock()
	assert.Equal(t, 1, fleet.cancelCalls,
		"CancelAll must be invoked exactly once across concurrent EscalateToCancel callers")
	fleet.mu.Unlock()

	// Phase stream must be strictly [Quiesce, Cancel, Complete]. This is a
	// regression guard for the ordering race where a synchronous CancelAll
	// drain (drainOnCancel=true) used to let driveSequence emit Complete
	// between EscalateToCancel's CancelAll and its emit(PhaseCancel), producing
	// the wrong [Quiesce, Complete, Cancel] order.
	phases := collectProgress(progress)
	assert.Equal(t, []TierPhase{PhaseQuiesce, PhaseCancel, PhaseComplete}, phases,
		"PhaseCancel must precede PhaseComplete even when CancelAll triggers immediate drain")
}

// TestShutdownCoordinator_EscalateToCancel_NoOpIfNotStarted: the strict
// policy is that an EscalateToCancel before any BeginQuiesce is a no-op —
// CancelAll must not fire, and the coordinator stays in not-started state.
func TestShutdownCoordinator_EscalateToCancel_NoOpIfNotStarted(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", false, true)
	c := NewShutdownCoordinator(fleet, nil)

	c.EscalateToCancel()

	fleet.mu.Lock()
	assert.Equal(t, 0, fleet.cancelCalls,
		"EscalateToCancel before BeginQuiesce must not call CancelAll")
	fleet.mu.Unlock()
	assert.False(t, c.InProgress(),
		"EscalateToCancel before BeginQuiesce must leave the coordinator in not-started state")
}

// TestShutdownCoordinator_EscalateToCancel_NoOpBeforeQuiesceEmitted: the
// strict policy also covers the gap between BeginQuiesce setting started=true
// and emit(PhaseQuiesce) landing. We exercise that gap deterministically by
// using a fleet whose QuiesceAll blocks until released by the test — while
// QuiesceAll is in-flight, started=true but PhaseQuiesce has not been
// emitted, so an EscalateToCancel issued in that window must be dropped.
func TestShutdownCoordinator_EscalateToCancel_NoOpBeforeQuiesceEmitted(t *testing.T) {
	release := make(chan struct{})
	fleet := &blockingQuiesceFleet{
		fakeRunFleet: newFakeFleet(),
		release:      release,
	}
	fleet.addRun("a", false, true)
	c := NewShutdownCoordinator(fleet, nil)

	// Start BeginQuiesce in a goroutine. It will block inside QuiesceAll
	// until we close `release`. While it's blocked, started=true but
	// PhaseQuiesce has not yet been emitted.
	beginDone := make(chan struct{})
	go func() {
		c.BeginQuiesce(context.Background())
		close(beginDone)
	}()

	// Wait until InProgress flips true — that's our signal that
	// BeginQuiesce has taken the gating lock and entered the setup window.
	deadline := time.After(time.Second)
	for !c.InProgress() {
		select {
		case <-deadline:
			t.Fatal("BeginQuiesce never flipped InProgress to true")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// Now race the escalation. With the gap-policy guard in place, this
	// must be dropped because PhaseQuiesce hasn't been published yet.
	c.EscalateToCancel()

	fleet.mu.Lock()
	assert.Equal(t, 0, fleet.cancelCalls,
		"EscalateToCancel during BeginQuiesce setup must be dropped")
	fleet.mu.Unlock()

	// Release QuiesceAll so the sequence can proceed and the test can
	// terminate. The run drains only on cancel, so we now legitimately
	// call EscalateToCancel a second time (after PhaseQuiesce has been
	// emitted) so the sequence actually completes.
	close(release)
	<-beginDone

	c.EscalateToCancel()
	<-c.WaitComplete()

	// Subscriber-visible phase order must be the canonical sequence.
	phases := collectProgress(c.Progress())
	assert.Equal(t, []TierPhase{PhaseQuiesce, PhaseCancel, PhaseComplete}, phases)
}

// blockingQuiesceFleet wraps fakeRunFleet so QuiesceAll blocks until the
// test closes `release`. Used to deterministically observe the
// started-but-not-yet-emitted-PhaseQuiesce window.
type blockingQuiesceFleet struct {
	*fakeRunFleet
	release chan struct{}
}

func (f *blockingQuiesceFleet) QuiesceAll() {
	<-f.release
	f.fakeRunFleet.QuiesceAll()
}

// TestShutdownCoordinator_OutcomeQuiesced: a run that drains before
// EscalateToCancel is engaged must be classified as OutcomeQuiesced.
func TestShutdownCoordinator_OutcomeQuiesced(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", true, false) // drains on quiesce
	c := NewShutdownCoordinator(fleet, nil)

	c.BeginQuiesce(context.Background())
	<-c.WaitComplete()

	r := c.Result()
	require.Contains(t, r.Outcomes, "a")
	assert.Equal(t, OutcomeQuiesced, r.Outcomes["a"])
}

// TestShutdownCoordinator_OutcomeCancelled: a run that drains only after
// EscalateToCancel is engaged must be classified as OutcomeCancelled.
func TestShutdownCoordinator_OutcomeCancelled(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", false, true) // drains only on cancel
	c := NewShutdownCoordinator(fleet, nil)

	c.BeginQuiesce(context.Background())
	// Give the driver goroutine a tick to settle into the wait so the
	// "before vs after cancel" boundary is unambiguous.
	time.Sleep(20 * time.Millisecond)
	c.EscalateToCancel()
	<-c.WaitComplete()

	r := c.Result()
	require.Contains(t, r.Outcomes, "a")
	assert.Equal(t, OutcomeCancelled, r.Outcomes["a"])
}

// TestShutdownCoordinator_OutcomeCancelled_DrainAfterCancel: a run whose
// done channel closes after EscalateToCancel has engaged is classified as
// OutcomeCancelled, even though the drain itself happens long before the
// cancelPatienceWindow expires. This pins the before/after-cancel boundary
// independently of the patience timer. The patience-expired path (a run
// that never drains) is reachable but not covered by a dedicated test
// because waiting 5s in unit tests is unattractive; the classification
// logic for that path is identical (drainedAt[id] = false → cancelled).
func TestShutdownCoordinator_OutcomeCancelled_DrainAfterCancel(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", false, false) // does not react to quiesce or cancel
	c := NewShutdownCoordinator(fleet, nil)

	c.BeginQuiesce(context.Background())
	time.Sleep(10 * time.Millisecond)
	c.EscalateToCancel()
	// Now drain "a" manually after cancel has engaged but well before the
	// patience window elapses.
	time.Sleep(20 * time.Millisecond)
	fleet.drain("a")

	<-c.WaitComplete()

	r := c.Result()
	require.Contains(t, r.Outcomes, "a")
	assert.Equal(t, OutcomeCancelled, r.Outcomes["a"])
}

// TestShutdownCoordinator_LateSubscriber_ReplaysHistory: a Progress() call
// after PhaseQuiesce has been emitted must receive PhaseQuiesce in the
// replay history, plus any subsequent phases as they fire.
func TestShutdownCoordinator_LateSubscriber_ReplaysHistory(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", true, false)
	c := NewShutdownCoordinator(fleet, nil)

	c.BeginQuiesce(context.Background())
	<-c.WaitComplete()

	// Subscribe after the sequence is complete; channel should already
	// contain the full replay and be closed.
	late := c.Progress()
	phases := collectProgress(late)
	assert.Equal(t, []TierPhase{PhaseQuiesce, PhaseComplete}, phases,
		"late subscriber must replay every emitted phase and observe close")
}

// TestShutdownCoordinator_LateSubscriber_DuringSequence: a Progress() call
// while the sequence is in flight (after PhaseQuiesce, before PhaseComplete)
// must replay PhaseQuiesce and then receive PhaseCancel / PhaseComplete as
// they fire.
func TestShutdownCoordinator_LateSubscriber_DuringSequence(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", false, true) // drains on cancel only
	c := NewShutdownCoordinator(fleet, nil)

	c.BeginQuiesce(context.Background())
	// Subscribe after PhaseQuiesce has fired, but before EscalateToCancel.
	late := c.Progress()
	c.EscalateToCancel()
	<-c.WaitComplete()

	phases := collectProgress(late)
	assert.Equal(t, []TierPhase{PhaseQuiesce, PhaseCancel, PhaseComplete}, phases)
}

// TestShutdownCoordinator_WaitComplete_FiresExactlyOnce: WaitComplete()
// returns a channel that closes once and only once. Multiple reads after
// closure remain non-blocking.
func TestShutdownCoordinator_WaitComplete_FiresExactlyOnce(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", true, false)
	c := NewShutdownCoordinator(fleet, nil)

	c.BeginQuiesce(context.Background())

	wc := c.WaitComplete()
	select {
	case <-wc:
	case <-time.After(time.Second):
		t.Fatal("WaitComplete channel did not close")
	}

	// Second read on the same channel must also be non-blocking (closed
	// channels are always readable). Re-fetch the channel and assert the
	// same — WaitComplete returns the same channel across calls.
	wc2 := c.WaitComplete()
	select {
	case <-wc2:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("second WaitComplete read blocked; channel must remain closed")
	}
}

// TestShutdownCoordinator_NoActiveRuns: BeginQuiesce on an empty fleet must
// proceed to PhaseComplete without hanging.
func TestShutdownCoordinator_NoActiveRuns(t *testing.T) {
	fleet := newFakeFleet()
	c := NewShutdownCoordinator(fleet, nil)

	c.BeginQuiesce(context.Background())

	select {
	case <-c.WaitComplete():
	case <-time.After(time.Second):
		t.Fatal("WaitComplete did not fire for an empty fleet")
	}

	r := c.Result()
	assert.Empty(t, r.Outcomes, "no active runs → no outcomes")
}
