// Package bus provides a simple synchronous publish/subscribe event bus.
//
// The bus decouples core orchestration logic from side-effect observers
// (console output, debug logging, etc.). Handler panics are recovered and
// logged so that a misbehaving observer cannot crash the orchestration loop.
//
// Registration uses Go generics so handlers are typed at the call site:
//
//	cancel := bus.Subscribe(b, func(e events.StateStarted) { ... })
//	defer cancel()
//
// Emit dispatches by the concrete type of the event value:
//
//	b.Emit(events.StateStarted{...})
package bus

import (
	"log"
	"reflect"
	"sync"
)

// Bus is a synchronous publish/subscribe event dispatcher.
// The zero value is not usable; create with New.
type Bus struct {
	mu       sync.Mutex
	handlers map[reflect.Type][]*entry
	nextID   uint64
}

type entry struct {
	id uint64
	fn func(any)
}

// New creates and returns a ready-to-use Bus.
func New() *Bus {
	return &Bus{handlers: make(map[reflect.Type][]*entry)}
}

// Subscribe registers handler for events of type T and returns a cancel
// function. Calling cancel removes the handler; calling it more than once
// is safe (idempotent).
func Subscribe[T any](b *Bus, handler func(T)) func() {
	t := reflect.TypeOf((*T)(nil)).Elem()
	e := &entry{fn: func(v any) { handler(v.(T)) }}

	b.mu.Lock()
	b.nextID++
	e.id = b.nextID
	b.handlers[t] = append(b.handlers[t], e)
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		entries := b.handlers[t]
		for i, h := range entries {
			if h.id == e.id {
				b.handlers[t] = append(entries[:i], entries[i+1:]...)
				return
			}
		}
	}
}

// Emit dispatches event to all handlers registered for its concrete type.
// Panics inside handlers are recovered and logged; remaining handlers
// for that event continue to run.
func (b *Bus) Emit(event any) {
	t := reflect.TypeOf(event)

	b.mu.Lock()
	// Copy the slice so we can release the lock before calling handlers.
	// This prevents deadlock if a handler itself calls Emit or Subscribe.
	snapshot := make([]*entry, len(b.handlers[t]))
	copy(snapshot, b.handlers[t])
	b.mu.Unlock()

	for _, e := range snapshot {
		safeCall(e.fn, event)
	}
}

// HasHandlers reports whether any handlers are registered for type T.
func HasHandlers[T any](b *Bus) bool {
	t := reflect.TypeOf((*T)(nil)).Elem()
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.handlers[t]) > 0
}

// Clear removes all registered handlers. Useful for test cleanup.
func (b *Bus) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = make(map[reflect.Type][]*entry)
}

// safeCall invokes fn(event), recovering any panic and logging it.
func safeCall(fn func(any), event any) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("bus: event handler panicked for %T: %v", event, r)
		}
	}()
	fn(event)
}
