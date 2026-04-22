package daemon

import (
	"fmt"
	"sync"
	"time"
)

// TimeoutDelivery describes the result of a timeout expiration to be delivered
// to the orchestrator layer.
type TimeoutDelivery struct {
	RunID       string
	AgentID     string
	InputID     string
	Response    string // empty when TimeoutNext is set
	TimeoutNext string // state to transition to (empty when Err is set)
	Err         error  // non-nil when TimeoutNext is empty (timeout error)
}

// TimeoutCallback is called by the monitor when a pending input times out.
// The callback should deliver the response (or error) to the orchestrator's
// await input channel.
type TimeoutCallback func(TimeoutDelivery)

// TimeoutMonitor periodically checks the pending registry for expired inputs
// and delivers timeout responses via the callback.
type TimeoutMonitor struct {
	registry *PendingRegistry
	callback TimeoutCallback

	mu     sync.Mutex
	stopCh chan struct{}
}

// NewTimeoutMonitor creates a TimeoutMonitor that checks the given registry
// for expired inputs and delivers results via callback.
func NewTimeoutMonitor(registry *PendingRegistry, callback TimeoutCallback) *TimeoutMonitor {
	return &TimeoutMonitor{
		registry: registry,
		callback: callback,
	}
}

// Start begins the background timeout checking loop at the given interval.
func (tm *TimeoutMonitor) Start(interval time.Duration) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.stopCh != nil {
		return // already running
	}
	tm.stopCh = make(chan struct{})

	go tm.loop(interval, tm.stopCh)
}

// Stop halts the background loop. It is safe to call multiple times.
func (tm *TimeoutMonitor) Stop() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.stopCh == nil {
		return
	}
	close(tm.stopCh)
	tm.stopCh = nil
}

// CheckNow performs a single immediate check of all pending inputs for
// timeouts. This is called by the background loop and can also be invoked
// directly in tests.
func (tm *TimeoutMonitor) CheckNow() {
	now := time.Now()

	pending := tm.registry.ListAll()
	for _, pi := range pending {
		if pi.TimeoutAt == nil || now.Before(*pi.TimeoutAt) {
			continue
		}

		// This input has expired.
		var delivery TimeoutDelivery
		if pi.TimeoutNext != "" {
			// Timeout with a next state: deliver an empty response so the
			// orchestrator transitions the agent to TimeoutNext with
			// {{result}} empty.
			delivery = TimeoutDelivery{
				RunID:       pi.RunID,
				AgentID:     pi.AgentID,
				InputID:     pi.InputID,
				Response:    "",
				TimeoutNext: pi.TimeoutNext,
				Err:         nil,
			}
		} else {
			// Timeout without a next state: deliver an error.
			delivery = TimeoutDelivery{
				RunID:   pi.RunID,
				AgentID: pi.AgentID,
				InputID: pi.InputID,
				Err:     fmt.Errorf("await timeout expired for agent %q input %q", pi.AgentID, pi.InputID),
			}
		}

		tm.callback(delivery)

		// Remove the expired record from the registry. Ignore errors from
		// concurrent removal (e.g. input was delivered just before timeout).
		_ = tm.registry.Remove(pi.InputID)
	}
}

// loop is the background goroutine that periodically calls CheckNow.
func (tm *TimeoutMonitor) loop(interval time.Duration, stopCh <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			tm.CheckNow()
		}
	}
}
