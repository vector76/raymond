package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

// TestShutdownCoordinator_ConcurrentCallersShareResult: two goroutines call
// Run concurrently. Per the subscribe-or-start contract, the second goroutine
// must attach to the in-flight sequence, the tier sequence must execute
// exactly once (QuiesceAll invoked ≤ 1 time), and both callers must observe
// the same ShutdownResult value.
//
// This case is kept because it pins the subscribe-or-start contract, which
// the two-phase rewrite preserves. The OutcomeClean-asserting and
// PhaseTier1Wait-emitting cases that previously lived in this file were
// removed in bead-1 since they pin concepts the rewrite deletes; the
// coordinator-rewrite bead (bead-2) replaces the broader test surface
// against the new API.
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

// TODO(bead-2): test BeginQuiesce / EscalateToCancel / InProgress /
// WaitComplete contract. The methods do not exist yet, so referencing them
// here would break the build. The coordinator-rewrite bead authors these
// test functions against the now-real methods; no skip-gate is needed
// because nothing is authored here.
