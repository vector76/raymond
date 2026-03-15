// Package titlebar implements a terminal titlebar observer for raymond.
//
// TitleBarObserver subscribes to StateStarted events on the event bus and
// updates the terminal window title via OSC 2 escape sequences:
//
//	ESC ] 2 ; ray: <stem> BEL
//
// where <stem> is the state filename with its last extension stripped.
// "START.md" → "ray: START", "CHECK.sh" → "ray: CHECK".
//
// OSC 2 is silently ignored by terminals that do not support it, so the
// observer degrades gracefully. There is no flag to disable it; the cost
// of a few invisible bytes per state transition is negligible.
package titlebar

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
)

// TitleBarObserver writes OSC 2 terminal title updates on each StateStarted
// event. Multiple agents share the same output stream; last-write-wins.
type TitleBarObserver struct {
	w      io.Writer
	cancel func()
	name   string
}

// New creates a TitleBarObserver subscribed to b that writes to os.Stdout.
func New(b *bus.Bus, name string) *TitleBarObserver {
	return NewWithWriter(b, os.Stdout, name)
}

// NewWithWriter creates a TitleBarObserver that writes to w. Used in tests
// to capture output without writing to the real terminal.
func NewWithWriter(b *bus.Bus, w io.Writer, name string) *TitleBarObserver {
	o := &TitleBarObserver{w: w, name: name}
	o.cancel = bus.Subscribe(b, o.onStateStarted)
	return o
}

// Close unregisters the observer from the bus. Safe to call multiple times.
func (o *TitleBarObserver) Close() {
	if o.cancel != nil {
		o.cancel()
		o.cancel = nil
	}
}

func (o *TitleBarObserver) onStateStarted(e events.StateStarted) {
	if o.name != "" {
		fmt.Fprintf(o.w, "\x1b]2;%s ray: %s\x07", o.name, stateStem(e.StateName))
	} else {
		fmt.Fprintf(o.w, "\x1b]2;ray: %s\x07", stateStem(e.StateName))
	}
}

// stateStem strips the last extension from a filename.
//
//	"START.md"    → "START"
//	"foo.bar.md"  → "foo.bar"
//	"NOOP"        → "NOOP"
func stateStem(name string) string {
	ext := filepath.Ext(name)
	if ext == "" {
		return name
	}
	return strings.TrimSuffix(name, ext)
}
