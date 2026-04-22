package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimeoutMonitor_ExpiresWithTimeoutNext(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	// Register a pending input that has already expired.
	expired := time.Now().Add(-1 * time.Second)
	require.NoError(t, reg.Register(PendingInput{
		RunID:       "run-1",
		AgentID:     "main",
		InputID:     "inp-001",
		Prompt:      "Enter value",
		NextState:   "NEXT.md",
		CreatedAt:   time.Now().Add(-1 * time.Hour),
		TimeoutAt:   &expired,
		TimeoutNext: "TIMEOUT.md",
	}))

	var mu sync.Mutex
	var deliveries []TimeoutDelivery
	callback := func(d TimeoutDelivery) {
		mu.Lock()
		deliveries = append(deliveries, d)
		mu.Unlock()
	}

	tm := NewTimeoutMonitor(reg, callback)
	tm.CheckNow()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, 1)
	assert.Equal(t, "run-1", deliveries[0].RunID)
	assert.Equal(t, "inp-001", deliveries[0].InputID)
	assert.Equal(t, "", deliveries[0].Response, "TimeoutNext present → empty response")
	assert.Equal(t, "TIMEOUT.md", deliveries[0].TimeoutNext)
	assert.NoError(t, deliveries[0].Err)

	// Record should be removed from registry.
	_, ok := reg.Get("inp-001")
	assert.False(t, ok, "expired input should be removed from registry")
}

func TestTimeoutMonitor_ExpiresWithoutTimeoutNext(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	// Register a pending input with no TimeoutNext — should produce an error.
	expired := time.Now().Add(-1 * time.Second)
	require.NoError(t, reg.Register(PendingInput{
		RunID:       "run-1",
		AgentID:     "main",
		InputID:     "inp-001",
		Prompt:      "Enter value",
		NextState:   "NEXT.md",
		CreatedAt:   time.Now().Add(-1 * time.Hour),
		TimeoutAt:   &expired,
		TimeoutNext: "",
	}))

	var mu sync.Mutex
	var deliveries []TimeoutDelivery
	callback := func(d TimeoutDelivery) {
		mu.Lock()
		deliveries = append(deliveries, d)
		mu.Unlock()
	}

	tm := NewTimeoutMonitor(reg, callback)
	tm.CheckNow()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, 1)
	assert.Equal(t, "inp-001", deliveries[0].InputID)
	assert.Equal(t, "", deliveries[0].TimeoutNext)
	assert.Error(t, deliveries[0].Err, "no TimeoutNext → error delivery")

	// Record should be removed from registry.
	_, ok := reg.Get("inp-001")
	assert.False(t, ok, "expired input should be removed from registry")
}

func TestTimeoutMonitor_NonExpiredNotAffected(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	// Register a pending input with a future timeout.
	future := time.Now().Add(1 * time.Hour)
	require.NoError(t, reg.Register(PendingInput{
		RunID:       "run-1",
		AgentID:     "main",
		InputID:     "inp-001",
		Prompt:      "Enter value",
		NextState:   "NEXT.md",
		TimeoutAt:   &future,
		TimeoutNext: "TIMEOUT.md",
	}))

	var mu sync.Mutex
	var deliveries []TimeoutDelivery
	callback := func(d TimeoutDelivery) {
		mu.Lock()
		deliveries = append(deliveries, d)
		mu.Unlock()
	}

	tm := NewTimeoutMonitor(reg, callback)
	tm.CheckNow()

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, deliveries, 0, "future timeout should not be triggered")

	// Record should still exist.
	_, ok := reg.Get("inp-001")
	assert.True(t, ok)
}

func TestTimeoutMonitor_NilTimeoutNotAffected(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	// Register a pending input with no timeout.
	require.NoError(t, reg.Register(PendingInput{
		RunID:   "run-1",
		AgentID: "main",
		InputID: "inp-001",
	}))

	var mu sync.Mutex
	var deliveries []TimeoutDelivery
	callback := func(d TimeoutDelivery) {
		mu.Lock()
		deliveries = append(deliveries, d)
		mu.Unlock()
	}

	tm := NewTimeoutMonitor(reg, callback)
	tm.CheckNow()

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, deliveries, 0, "nil timeout should not be triggered")

	_, ok := reg.Get("inp-001")
	assert.True(t, ok)
}

func TestTimeoutMonitor_BackgroundLoop(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	// Register an input that will expire soon.
	expired := time.Now().Add(-1 * time.Millisecond)
	require.NoError(t, reg.Register(PendingInput{
		RunID:       "run-1",
		AgentID:     "main",
		InputID:     "inp-001",
		TimeoutAt:   &expired,
		TimeoutNext: "TIMEOUT.md",
	}))

	deliveryCh := make(chan TimeoutDelivery, 1)
	callback := func(d TimeoutDelivery) {
		select {
		case deliveryCh <- d:
		default:
		}
	}

	tm := NewTimeoutMonitor(reg, callback)
	// Start the background loop with a short interval for testing.
	tm.Start(50 * time.Millisecond)
	defer tm.Stop()

	select {
	case d := <-deliveryCh:
		assert.Equal(t, "inp-001", d.InputID)
		assert.NoError(t, d.Err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for background monitor to trigger")
	}
}

func TestTimeoutMonitor_MultipleExpired(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	expired := time.Now().Add(-1 * time.Second)
	require.NoError(t, reg.Register(PendingInput{
		RunID: "run-1", AgentID: "main", InputID: "inp-001",
		TimeoutAt: &expired, TimeoutNext: "T1.md",
	}))
	require.NoError(t, reg.Register(PendingInput{
		RunID: "run-2", AgentID: "main", InputID: "inp-002",
		TimeoutAt: &expired, TimeoutNext: "",
	}))

	var mu sync.Mutex
	var deliveries []TimeoutDelivery
	callback := func(d TimeoutDelivery) {
		mu.Lock()
		deliveries = append(deliveries, d)
		mu.Unlock()
	}

	tm := NewTimeoutMonitor(reg, callback)
	tm.CheckNow()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, 2)

	// Both should be removed.
	assert.Len(t, reg.ListAll(), 0)
}

func TestTimeoutMonitor_StopIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	tm := NewTimeoutMonitor(reg, func(TimeoutDelivery) {})

	// Stop without Start should not panic.
	tm.Stop()
	tm.Stop()
}
