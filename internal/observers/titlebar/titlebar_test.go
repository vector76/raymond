package titlebar_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/observers/titlebar"
)

func TestTitleBarOnStateStarted(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := titlebar.NewWithWriter(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md"})

	assert.Equal(t, "\x1b]2;ray: START\x07", buf.String())
}

func TestTitleBarStripsShExtension(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := titlebar.NewWithWriter(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "CHECK.sh"})

	assert.Equal(t, "\x1b]2;ray: CHECK\x07", buf.String())
}

func TestTitleBarStripsBatExtension(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := titlebar.NewWithWriter(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "CHECK.bat"})

	assert.Equal(t, "\x1b]2;ray: CHECK\x07", buf.String())
}

func TestTitleBarStripsLastExtensionOnly(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := titlebar.NewWithWriter(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "foo.bar.md"})

	assert.Equal(t, "\x1b]2;ray: foo.bar\x07", buf.String())
}

func TestTitleBarNoExtension(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := titlebar.NewWithWriter(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "NOOP"})

	assert.Equal(t, "\x1b]2;ray: NOOP\x07", buf.String())
}

func TestTitleBarMultipleStateTransitions(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := titlebar.NewWithWriter(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md"})
	b.Emit(events.StateStarted{AgentID: "main", StateName: "NEXT.md"})

	assert.Equal(t, "\x1b]2;ray: START\x07\x1b]2;ray: NEXT\x07", buf.String())
}

func TestTitleBarMultipleAgents(t *testing.T) {
	// With multiple agents, last-write-wins — both fire to the same writer.
	b := bus.New()
	var buf bytes.Buffer
	obs := titlebar.NewWithWriter(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "A.md"})
	b.Emit(events.StateStarted{AgentID: "main_worker1", StateName: "B.md"})

	out := buf.String()
	assert.Contains(t, out, "\x1b]2;ray: A\x07")
	assert.Contains(t, out, "\x1b]2;ray: B\x07")
}

func TestTitleBarUnsubscribesOnClose(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := titlebar.NewWithWriter(b, &buf)
	obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md"})

	assert.Empty(t, buf.String())
}

func TestTitleBarCloseIdempotent(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := titlebar.NewWithWriter(b, &buf)

	obs.Close()
	obs.Close() // should not panic
}
