package bus_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
)

// makeStarted is a helper to construct a minimal StateStarted event.
func makeStarted(name string) events.StateStarted {
	return events.StateStarted{AgentID: "main", StateName: name, StateType: "markdown"}
}

func makeCompleted(name string) events.StateCompleted {
	return events.StateCompleted{AgentID: "main", StateName: name}
}

// ----------------------------------------------------------------------------
// Subscription and dispatch
// ----------------------------------------------------------------------------

func TestSubscribeAndEmit(t *testing.T) {
	b := bus.New()
	var received []events.StateStarted

	bus.Subscribe(b, func(e events.StateStarted) {
		received = append(received, e)
	})

	evt := makeStarted("START.md")
	b.Emit(evt)

	assert.Len(t, received, 1)
	assert.Equal(t, evt, received[0])
}

// TestMultipleHandlersAllCalled verifies that all registered handlers receive
// an emitted event. Handler execution order is intentionally undefined (handlers
// are stored in a map and iterated in non-deterministic order); this test only
// verifies that all handlers are called, not that they are called in any
// particular sequence.
func TestMultipleHandlersAllCalled(t *testing.T) {
	b := bus.New()
	counts := map[string]int{"h1": 0, "h2": 0, "h3": 0}

	bus.Subscribe(b, func(e events.StateStarted) { counts["h1"]++ })
	bus.Subscribe(b, func(e events.StateStarted) { counts["h2"]++ })
	bus.Subscribe(b, func(e events.StateStarted) { counts["h3"]++ })

	b.Emit(makeStarted("START.md"))

	assert.Equal(t, 1, counts["h1"])
	assert.Equal(t, 1, counts["h2"])
	assert.Equal(t, 1, counts["h3"])
}

func TestCancelStopsDelivery(t *testing.T) {
	b := bus.New()
	var received []events.StateStarted

	cancel := bus.Subscribe(b, func(e events.StateStarted) {
		received = append(received, e)
	})

	b.Emit(makeStarted("START.md"))
	assert.Len(t, received, 1)

	cancel()

	b.Emit(makeStarted("NEXT.md"))
	assert.Len(t, received, 1) // still 1, not 2
}

func TestCancelIdempotent(t *testing.T) {
	b := bus.New()
	cancel := bus.Subscribe(b, func(e events.StateStarted) {})

	// Cancelling twice should not panic
	cancel()
	cancel()
}

func TestCancelUnregisteredIsNoop(t *testing.T) {
	b := bus.New()
	// Subscribe to a different type and then cancel — should not panic
	cancel := bus.Subscribe(b, func(e events.StateCompleted) {})
	cancel()
	// No StateStarted handlers registered; emitting is safe
	b.Emit(makeStarted("START.md"))
}

// ----------------------------------------------------------------------------
// Emission behaviour
// ----------------------------------------------------------------------------

func TestEmitWithNoHandlersSafe(t *testing.T) {
	b := bus.New()
	// Should not panic
	b.Emit(makeStarted("START.md"))
}

func TestEmitDispatchesToCorrectTypeOnly(t *testing.T) {
	b := bus.New()
	var startedCalls, completedCalls int

	bus.Subscribe(b, func(e events.StateStarted) { startedCalls++ })
	bus.Subscribe(b, func(e events.StateCompleted) { completedCalls++ })

	b.Emit(makeStarted("START.md"))

	assert.Equal(t, 1, startedCalls)
	assert.Equal(t, 0, completedCalls)
}

func TestPanicInHandlerCaughtOtherHandlersContinue(t *testing.T) {
	b := bus.New()
	callOrder := []int{}

	bus.Subscribe(b, func(e events.StateStarted) {
		callOrder = append(callOrder, 1)
		panic("handler 1 panicked")
	})
	bus.Subscribe(b, func(e events.StateStarted) {
		callOrder = append(callOrder, 2)
	})
	bus.Subscribe(b, func(e events.StateStarted) {
		callOrder = append(callOrder, 3)
		panic("handler 3 panicked")
	})
	bus.Subscribe(b, func(e events.StateStarted) {
		callOrder = append(callOrder, 4)
	})

	// Must not panic at the call site
	assert.NotPanics(t, func() { b.Emit(makeStarted("START.md")) })

	// All handlers must have been called despite panics
	assert.Equal(t, []int{1, 2, 3, 4}, callOrder)
}

func TestPanicDoesNotPropagateToEmitCaller(t *testing.T) {
	b := bus.New()
	bus.Subscribe(b, func(e events.StateStarted) { panic("boom") })
	assert.NotPanics(t, func() { b.Emit(makeStarted("X.md")) })
}

// ----------------------------------------------------------------------------
// HasHandlers
// ----------------------------------------------------------------------------

func TestHasHandlersTrue(t *testing.T) {
	b := bus.New()
	bus.Subscribe(b, func(e events.StateStarted) {})
	assert.True(t, bus.HasHandlers[events.StateStarted](b))
}

func TestHasHandlersFalse(t *testing.T) {
	b := bus.New()
	assert.False(t, bus.HasHandlers[events.StateStarted](b))
}

func TestHasHandlersFalseAfterCancel(t *testing.T) {
	b := bus.New()
	cancel := bus.Subscribe(b, func(e events.StateStarted) {})
	assert.True(t, bus.HasHandlers[events.StateStarted](b))
	cancel()
	assert.False(t, bus.HasHandlers[events.StateStarted](b))
}

// ----------------------------------------------------------------------------
// Clear
// ----------------------------------------------------------------------------

func TestClearRemovesAllHandlers(t *testing.T) {
	b := bus.New()
	var calls []int

	bus.Subscribe(b, func(e events.StateStarted) { calls = append(calls, 1) })
	bus.Subscribe(b, func(e events.StateCompleted) { calls = append(calls, 2) })

	assert.True(t, bus.HasHandlers[events.StateStarted](b))
	assert.True(t, bus.HasHandlers[events.StateCompleted](b))

	b.Clear()

	assert.False(t, bus.HasHandlers[events.StateStarted](b))
	assert.False(t, bus.HasHandlers[events.StateCompleted](b))

	b.Emit(makeStarted("START.md"))
	assert.Empty(t, calls)
}

// ----------------------------------------------------------------------------
// Integration
// ----------------------------------------------------------------------------

func TestMultipleEventTypes(t *testing.T) {
	b := bus.New()
	started, completed, transitioned := 0, 0, 0

	bus.Subscribe(b, func(e events.StateStarted) { started++ })
	bus.Subscribe(b, func(e events.StateCompleted) { completed++ })
	bus.Subscribe(b, func(e events.TransitionOccurred) { transitioned++ })

	b.Emit(makeStarted("START.md"))
	b.Emit(makeCompleted("START.md"))
	b.Emit(events.TransitionOccurred{AgentID: "main", FromState: "START.md", ToState: "NEXT.md", TransitionType: "goto"})
	b.Emit(makeStarted("NEXT.md"))

	assert.Equal(t, 2, started)
	assert.Equal(t, 1, completed)
	assert.Equal(t, 1, transitioned)
}

func TestSameHandlerOnMultipleTypes(t *testing.T) {
	b := bus.New()
	var allEvents []any

	bus.Subscribe(b, func(e events.StateStarted) { allEvents = append(allEvents, e) })
	bus.Subscribe(b, func(e events.StateCompleted) { allEvents = append(allEvents, e) })
	bus.Subscribe(b, func(e events.ErrorOccurred) { allEvents = append(allEvents, e) })

	b.Emit(makeStarted("START.md"))
	b.Emit(events.ErrorOccurred{AgentID: "main", ErrorType: "TestError"})

	assert.Len(t, allEvents, 2)
	assert.IsType(t, events.StateStarted{}, allEvents[0])
	assert.IsType(t, events.ErrorOccurred{}, allEvents[1])
}
