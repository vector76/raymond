package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/orchestrator"
)

// TestPublishGlobalEvent_FansOutToSubscribers verifies the daemon-wide
// broadcast surface delivers a single Publish to every active subscriber.
func TestPublishGlobalEvent_FansOutToSubscribers(t *testing.T) {
	srv, _, _ := newTestServer(t)

	ch1, cancel1 := srv.SubscribeGlobalEvents()
	defer cancel1()
	ch2, cancel2 := srv.SubscribeGlobalEvents()
	defer cancel2()

	evt := events.ShutdownRequested{
		ActiveRuns:  []events.ActiveRunSnapshot{{ID: "r1"}},
		RequestedAt: time.Now(),
	}
	srv.PublishGlobalEvent(evt)

	for i, ch := range []<-chan any{ch1, ch2} {
		select {
		case got := <-ch:
			gotEvt, ok := got.(events.ShutdownRequested)
			require.True(t, ok, "subscriber %d: expected ShutdownRequested, got %T", i, got)
			assert.Equal(t, evt.ActiveRuns, gotEvt.ActiveRuns)
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for event", i)
		}
	}
}

// TestSubscribeGlobalEvents_CancelRemovesSubscriber verifies the cancel
// returned by SubscribeGlobalEvents drops the channel from the broadcast
// list and closes it, so a subsequent Publish does not surface to the
// cancelled subscriber.
func TestSubscribeGlobalEvents_CancelRemovesSubscriber(t *testing.T) {
	srv, _, _ := newTestServer(t)

	ch, cancel := srv.SubscribeGlobalEvents()
	cancel()

	// Channel must be closed.
	select {
	case _, ok := <-ch:
		assert.False(t, ok, "expected channel to be closed after cancel")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel close")
	}

	// Publish should not panic and should have no surviving subscribers.
	srv.PublishGlobalEvent(events.ShutdownComplete{Outcomes: map[string]string{}})
	srv.globalMu.Lock()
	count := len(srv.globalSubscribers)
	srv.globalMu.Unlock()
	assert.Equal(t, 0, count, "subscriber slice should be empty after cancel")
}

// TestPublishGlobalEvent_ConcurrentCancelDoesNotPanic stresses the
// publish/cancel concurrency. An earlier draft snapshotted the subscriber
// list under the lock and then sent outside it; a cancel running between
// snapshot and send would close the channel and the publisher would panic
// with "send on closed channel". The current implementation holds globalMu
// during fan-out (matching the per-run recorder pattern); this test would
// reliably reproduce the panic against the buggy version with -race.
func TestPublishGlobalEvent_ConcurrentCancelDoesNotPanic(t *testing.T) {
	srv, _, _ := newTestServer(t)

	stop := make(chan struct{})
	var publisherWg sync.WaitGroup
	publisherWg.Add(1)
	go func() {
		defer publisherWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				srv.PublishGlobalEvent(events.ShutdownComplete{
					Outcomes: map[string]string{"r": "quiesced"},
				})
			}
		}
	}()

	var subWg sync.WaitGroup
	for i := 0; i < 200; i++ {
		subWg.Add(1)
		go func() {
			defer subWg.Done()
			ch, cancel := srv.SubscribeGlobalEvents()
			// Drain one event so the subscribe is racing with deliveries
			// in flight, then cancel.
			select {
			case <-ch:
			case <-time.After(50 * time.Millisecond):
			}
			cancel()
		}()
	}
	subWg.Wait()
	close(stop)
	publisherWg.Wait()
}

// TestPublishGlobalEvent_SlowSubscriberDoesNotBlock fills a subscriber's
// buffer and then publishes additional events. PublishGlobalEvent must not
// block waiting for the slow consumer; the events simply drop for that
// subscriber.
func TestPublishGlobalEvent_SlowSubscriberDoesNotBlock(t *testing.T) {
	srv, _, _ := newTestServer(t)

	// Subscribe but never read.
	_, cancel := srv.SubscribeGlobalEvents()
	defer cancel()

	done := make(chan struct{})
	go func() {
		// Publish far more than the subscriber buffer (globalSubscriberBuffer = 16).
		for i := 0; i < 1000; i++ {
			srv.PublishGlobalEvent(events.ShutdownComplete{
				Outcomes: map[string]string{"run": "quiesced"},
			})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("PublishGlobalEvent blocked on slow subscriber")
	}
}

// TestPublishGlobalEvent_MirrorsToPerRunStreams verifies that a client
// already subscribed to a per-run output stream sees the daemon-wide event
// in-band, with the correct SSE type discriminator after marshalling.
func TestPublishGlobalEvent_MirrorsToPerRunStreams(t *testing.T) {
	srv, ts, fake := newTestServer(t)

	busReady := make(chan struct{})
	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}
		close(busReady)
		<-ctx.Done()
		return ctx.Err()
	}

	createResp, err := http.Post(ts.URL+"/runs", "application/json",
		strings.NewReader(`{"workflow_id": "test-workflow"}`))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&cr))

	select {
	case <-busReady:
	case <-time.After(3 * time.Second):
		t.Fatal("orchestrator never set up bus")
	}

	subCtx, subCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer subCancel()
	eventCh, cancel, err := srv.runManager.SubscribeRunEvents(subCtx, cr.RunID)
	require.NoError(t, err)
	defer cancel()

	evt := events.ShutdownRequested{
		ActiveRuns:  []events.ActiveRunSnapshot{{ID: "r1"}},
		RequestedAt: time.Now(),
	}
	srv.PublishGlobalEvent(evt)

	// Drain until we see the mirrored shutdown frame (the per-run stream
	// may also deliver replay events from its own bus first).
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got, ok := <-eventCh:
			if !ok {
				t.Fatal("per-run stream closed before delivering mirrored event")
			}
			if sr, ok := got.(events.ShutdownRequested); ok {
				assert.Equal(t, evt.ActiveRuns, sr.ActiveRuns)
				// Also exercise the SSE marshalling path so the test
				// covers the discriminator wire-shape callers depend on.
				data, err := marshalSSEEvent(sr)
				require.NoError(t, err)
				var env map[string]any
				require.NoError(t, json.Unmarshal(data, &env))
				assert.Equal(t, "shutdown_requested", env["type"])
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for mirrored shutdown event on per-run stream")
		}
	}
}

// TestGlobalEventsEndpoint_StreamsPublishedEvents exercises the
// GET /events SSE endpoint end-to-end through httptest.
func TestGlobalEventsEndpoint_StreamsPublishedEvents(t *testing.T) {
	srv, ts, _ := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// Wait briefly for the handler to subscribe before publishing, so the
	// event is not dropped on a not-yet-attached subscriber.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		srv.globalMu.Lock()
		n := len(srv.globalSubscribers)
		srv.globalMu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	srv.PublishGlobalEvent(events.ShutdownRequested{
		ActiveRuns:  []events.ActiveRunSnapshot{{ID: "r1"}},
		RequestedAt: time.Now(),
	})

	scanner := bufio.NewScanner(resp.Body)
	var gotEvent bool
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var env map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &env))
		assert.Equal(t, "shutdown_requested", env["type"])
		_, hasT1 := env["tier_1_timeout_secs"]
		assert.False(t, hasT1, "tier_1_timeout_secs must not appear on the wire")
		gotEvent = true
		break
	}
	assert.True(t, gotEvent, "expected at least one SSE data line")
}

// TestShutdownEventsShape_PostRewrite asserts the post-rewrite shape of the
// shutdown SSE events:
//
//   - ShutdownRequested JSON has no tier_1_timeout_secs / tier_2_timeout_secs
//     keys (quiesce is unbounded; the cancel patience window is a code
//     constant rather than a wire field).
//   - ShutdownComplete outcome values are drawn from the closed set
//     {"quiesced", "cancelled"}; the legacy "clean" / "killed" values are
//     gone.
//
// Unblocked by bead-9, which removes the dead fields from
// events.ShutdownRequested and rewires the coordinator's outcome
// classification to the two-value set.
func TestShutdownEventsShape_PostRewrite(t *testing.T) {
	// ShutdownRequested must serialise without the tier-timeout fields.
	req := events.ShutdownRequested{
		ActiveRuns:  []events.ActiveRunSnapshot{{ID: "r1"}},
		RequestedAt: time.Unix(0, 0),
	}
	reqBytes, err := json.Marshal(req)
	require.NoError(t, err)
	var reqMap map[string]any
	require.NoError(t, json.Unmarshal(reqBytes, &reqMap))
	_, hasT1 := reqMap["tier_1_timeout_secs"]
	_, hasT2 := reqMap["tier_2_timeout_secs"]
	assert.False(t, hasT1, "tier_1_timeout_secs must not appear in ShutdownRequested JSON")
	assert.False(t, hasT2, "tier_2_timeout_secs must not appear in ShutdownRequested JSON")

	// ShutdownComplete's outcome value set is {"quiesced", "cancelled"}.
	comp := events.ShutdownComplete{
		Outcomes: map[string]string{
			"r1": "quiesced",
			"r2": "cancelled",
		},
	}
	compBytes, err := json.Marshal(comp)
	require.NoError(t, err)
	var compMap struct {
		Outcomes map[string]string `json:"outcomes"`
	}
	require.NoError(t, json.Unmarshal(compBytes, &compMap))
	allowed := map[string]struct{}{"quiesced": {}, "cancelled": {}}
	for runID, outcome := range compMap.Outcomes {
		_, ok := allowed[outcome]
		assert.Truef(t, ok,
			"ShutdownComplete outcome for %q = %q; must be one of {quiesced, cancelled}",
			runID, outcome)
	}
}
