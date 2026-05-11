package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/events"
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

// makeTempSignal returns a ShutdownSignal rooted in a tmp dir so the
// sentinel write in Request() doesn't hit anything outside the test sandbox.
func makeTempSignal(t *testing.T) *ShutdownSignal {
	t.Helper()
	return NewShutdownSignal(t.TempDir())
}

func TestShutdownCoordinator_AllDrainDuringT1(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", false, false)
	fleet.addRun("b", false, false)

	sig := makeTempSignal(t)
	c := NewShutdownCoordinator(fleet, sig, nil)

	// Drain both runs shortly after Run() begins, well within T1.
	go func() {
		time.Sleep(10 * time.Millisecond)
		fleet.drain("a")
		fleet.drain("b")
	}()

	result := c.Run(context.Background(), 200*time.Millisecond, 200*time.Millisecond)

	assert.Equal(t, OutcomeClean, result.Outcomes["a"])
	assert.Equal(t, OutcomeClean, result.Outcomes["b"])

	phases := collectProgress(c.Progress())
	assert.Equal(t, []TierPhase{PhaseTier1Wait, PhaseComplete}, phases,
		"only T1 wait and completion should be emitted when all runs drain in T1")

	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	assert.Equal(t, 0, fleet.quiesceCalls, "QuiesceAll must not be called when T1 succeeds")
	assert.Equal(t, 0, fleet.cancelCalls, "CancelAll must not be called when T1 succeeds")
}

func TestShutdownCoordinator_MixedT1AndT2(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("clean", false, false)
	// "stubborn" only drains when QuiesceAll is invoked — i.e. it survives T1.
	fleet.addRun("stubborn", true, false)

	sig := makeTempSignal(t)
	c := NewShutdownCoordinator(fleet, sig, nil)

	// Drain "clean" inside T1.
	go func() {
		time.Sleep(20 * time.Millisecond)
		fleet.drain("clean")
	}()

	t1 := 100 * time.Millisecond
	t2 := 200 * time.Millisecond
	result := c.Run(context.Background(), t1, t2)

	assert.Equal(t, OutcomeClean, result.Outcomes["clean"])
	assert.Equal(t, OutcomeQuiesced, result.Outcomes["stubborn"])

	phases := collectProgress(c.Progress())
	assert.Equal(t, []TierPhase{PhaseTier1Wait, PhaseTier2Quiesce, PhaseComplete}, phases)

	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	assert.Equal(t, 1, fleet.quiesceCalls)
	assert.Equal(t, 0, fleet.cancelCalls, "CancelAll must not be called when T2 drains everything")
}

func TestShutdownCoordinator_SurvivorsForcedKilled(t *testing.T) {
	fleet := newFakeFleet()
	// "killer" never honours QuiesceAll; only CancelAll drains it.
	fleet.addRun("killer", false, true)

	sig := makeTempSignal(t)
	c := NewShutdownCoordinator(fleet, sig, nil)

	t1 := 50 * time.Millisecond
	t2 := 50 * time.Millisecond
	result := c.Run(context.Background(), t1, t2)

	assert.Equal(t, OutcomeKilled, result.Outcomes["killer"])

	phases := collectProgress(c.Progress())
	assert.Equal(t, []TierPhase{PhaseTier1Wait, PhaseTier2Quiesce, PhaseForceKill, PhaseComplete}, phases)

	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	assert.Equal(t, 1, fleet.quiesceCalls)
	assert.Equal(t, 1, fleet.cancelCalls)
}

func TestShutdownCoordinator_ZeroActiveRuns(t *testing.T) {
	fleet := newFakeFleet() // no runs registered

	sig := makeTempSignal(t)
	c := NewShutdownCoordinator(fleet, sig, nil)

	start := time.Now()
	result := c.Run(context.Background(), 500*time.Millisecond, 500*time.Millisecond)
	elapsed := time.Since(start)

	assert.Empty(t, result.Outcomes, "no runs → no outcomes")
	assert.Less(t, elapsed, 200*time.Millisecond, "empty shutdown should complete near-instantly")

	phases := collectProgress(c.Progress())
	assert.Equal(t, []TierPhase{PhaseTier1Wait, PhaseComplete}, phases)

	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	assert.Equal(t, 0, fleet.quiesceCalls)
	assert.Equal(t, 0, fleet.cancelCalls)
}

func TestShutdownCoordinator_RequestsSignalBeforeT1Wait(t *testing.T) {
	// Order matters: the signal must flip *before* the T1 timer starts so
	// shell steps that race shutdown see the env vars. Run sends
	// PhaseTier1Wait *after* signal.Request(), so by the time a consumer
	// observes Tier1Wait, IsRequested() must already be true.
	fleet := newFakeFleet()
	fleet.addRun("a", false, false)

	sig := makeTempSignal(t)
	c := NewShutdownCoordinator(fleet, sig, nil)

	go func() {
		time.Sleep(5 * time.Millisecond)
		fleet.drain("a")
	}()

	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		first, ok := <-c.Progress()
		require.True(t, ok, "Progress channel closed before any phase emitted")
		assert.Equal(t, PhaseTier1Wait, first)
		assert.True(t, sig.IsRequested(),
			"signal must be flipped before Tier1Wait is emitted")
		// Drain the rest so the producer can close cleanly.
		for range c.Progress() {
		}
	}()

	_ = c.Run(context.Background(), 200*time.Millisecond, 200*time.Millisecond)
	<-consumerDone
}

// TestShutdownCoordinator_AskingRunsDrainInT2: runs whose only agents are in
// "asking" status appear to QuiesceAll as a safe pause point — the
// orchestrator's PendingAskError closes the run's done channel during Tier 2,
// so the coordinator must classify them as Quiesced and never escalate to
// force-kill. See bd-lu7f acceptance criterion 1.
func TestShutdownCoordinator_AskingRunsDrainInT2(t *testing.T) {
	fleet := newFakeFleet()
	// "asker" stays alive through T1 (status="asking") and only drains
	// when the coordinator asks the fleet to quiesce. drainOnQuiesce=true
	// models the orchestrator returning a PendingAskError under
	// StopSignalCh: the run's done channel closes during Tier 2.
	r := fleet.addRun("asker", true, false)
	r.status = RunStatusAsking

	sig := makeTempSignal(t)
	c := NewShutdownCoordinator(fleet, sig, nil)

	t1 := 50 * time.Millisecond
	t2 := 200 * time.Millisecond
	result := c.Run(context.Background(), t1, t2)

	assert.Equal(t, OutcomeQuiesced, result.Outcomes["asker"],
		"asking-status run must be classified Quiesced when it drains in T2")

	phases := collectProgress(c.Progress())
	assert.Equal(t, []TierPhase{PhaseTier1Wait, PhaseTier2Quiesce, PhaseComplete}, phases,
		"asking-state runs must drain in Tier 2 — no force_kill expected")
	for _, p := range phases {
		assert.NotEqual(t, PhaseForceKill, p, "force_kill must not be emitted")
	}

	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	assert.Equal(t, 1, fleet.quiesceCalls)
	assert.Equal(t, 0, fleet.cancelCalls)
}

// TestShutdownCoordinator_ConcurrentCallersShareResult: two goroutines call
// Run concurrently. Per the subscribe-or-start contract, the second goroutine
// must attach to the in-flight sequence, the tier sequence must execute
// exactly once (QuiesceAll invoked ≤ 1 time), and both callers must observe
// the same ShutdownResult value.
func TestShutdownCoordinator_ConcurrentCallersShareResult(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", true, false)
	fleet.addRun("b", true, false)

	sig := makeTempSignal(t)
	c := NewShutdownCoordinator(fleet, sig, nil)

	t1 := 30 * time.Millisecond
	t2 := 200 * time.Millisecond

	var wg sync.WaitGroup
	var r1, r2 ShutdownResult
	wg.Add(2)
	go func() {
		defer wg.Done()
		r1 = c.Run(context.Background(), t1, t2)
	}()
	go func() {
		defer wg.Done()
		// Stagger slightly so the second caller almost certainly arrives
		// after the first has set started=true. The contract holds either
		// way, but staggering exercises the subscriber path deterministically.
		time.Sleep(5 * time.Millisecond)
		r2 = c.Run(context.Background(), t1, t2)
	}()
	wg.Wait()

	assert.Equal(t, r1, r2, "both callers must observe the same ShutdownResult")
	assert.Equal(t, OutcomeQuiesced, r1.Outcomes["a"])
	assert.Equal(t, OutcomeQuiesced, r1.Outcomes["b"])

	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	assert.LessOrEqual(t, fleet.quiesceCalls, 1,
		"QuiesceAll must be invoked at most once across concurrent Run callers")
	assert.Equal(t, 0, fleet.cancelCalls, "CancelAll must not fire when T2 drains everything")
}

// TestShutdownCoordinator_ConcurrentCallersSeeSameProgressStream: each
// Progress() subscriber receives the full tier sequence, including
// PhaseComplete, regardless of when it subscribed relative to the
// in-flight sequence.
func TestShutdownCoordinator_ConcurrentCallersSeeSameProgressStream(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", true, false)

	sig := makeTempSignal(t)
	c := NewShutdownCoordinator(fleet, sig, nil)

	// Two subscribers obtained *before* Run starts so neither relies on
	// the replay path; each must independently observe the full sequence.
	prog1 := c.Progress()
	prog2 := c.Progress()

	t1 := 30 * time.Millisecond
	t2 := 200 * time.Millisecond

	go func() {
		_ = c.Run(context.Background(), t1, t2)
	}()

	want := []TierPhase{PhaseTier1Wait, PhaseTier2Quiesce, PhaseComplete}
	assert.Equal(t, want, collectProgress(prog1),
		"first subscriber must see the full tier sequence")
	assert.Equal(t, want, collectProgress(prog2),
		"second subscriber must see the same full tier sequence")
}

// TestShutdownCoordinator_LateSubscriberReplay: a Progress() call after the
// sequence has already completed still observes the full tier sequence via
// the replay buffer, and the channel is pre-closed.
func TestShutdownCoordinator_LateSubscriberReplay(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", true, false)

	sig := makeTempSignal(t)
	c := NewShutdownCoordinator(fleet, sig, nil)

	_ = c.Run(context.Background(), 30*time.Millisecond, 200*time.Millisecond)

	// The sequence has finished. A new subscriber must still observe
	// the full replayed history terminated by a closed channel.
	late := c.Progress()
	assert.Equal(t,
		[]TierPhase{PhaseTier1Wait, PhaseTier2Quiesce, PhaseComplete},
		collectProgress(late),
		"late subscriber must replay the full tier sequence")
}

func TestShutdownCoordinator_PublishesShutdownEvents(t *testing.T) {
	fleet := newFakeFleet()
	fleet.addRun("a", false, false)
	fleet.addRun("b", true, false)

	sig := makeTempSignal(t)

	var sinkMu sync.Mutex
	var captured []any
	sink := func(evt any) {
		sinkMu.Lock()
		captured = append(captured, evt)
		sinkMu.Unlock()
	}

	c := NewShutdownCoordinator(fleet, sig, sink)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fleet.drain("a")
	}()

	t1 := 50 * time.Millisecond
	t2 := 150 * time.Millisecond
	_ = c.Run(context.Background(), t1, t2)
	collectProgress(c.Progress())

	sinkMu.Lock()
	defer sinkMu.Unlock()
	require.Len(t, captured, 2, "expected ShutdownRequested and ShutdownComplete")

	req, ok := captured[0].(events.ShutdownRequested)
	require.True(t, ok, "first event must be ShutdownRequested, got %T", captured[0])
	assert.Equal(t, t1.Seconds(), req.Tier1TimeoutSecs)
	assert.Equal(t, t2.Seconds(), req.Tier2TimeoutSecs)
	assert.Len(t, req.ActiveRuns, 2)

	comp, ok := captured[1].(events.ShutdownComplete)
	require.True(t, ok, "second event must be ShutdownComplete, got %T", captured[1])
	assert.Equal(t, "clean", comp.Outcomes["a"])
	assert.Equal(t, "quiesced", comp.Outcomes["b"])
}

