package console_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/observers/console"
)

// newObs creates a ConsoleObserver backed by buf using ASCII symbols, no color.
func newObs(b *bus.Bus, buf *bytes.Buffer, quiet bool) *console.ConsoleObserver {
	return console.NewWithWriter(b, quiet, 0, buf, false, false)
}

// ----------------------------------------------------------------------------
// Workflow lifecycle
// ----------------------------------------------------------------------------

func TestConsoleWorkflowStarted(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.WorkflowStarted{
		WorkflowID: "wf1",
		ScopeDir:   "workflows/test",
		DebugDir:   "",
		Timestamp:  time.Date(2026, 1, 15, 14, 30, 22, 0, time.UTC),
	})

	out := buf.String()
	assert.Contains(t, out, "[14:30:22] Workflow: wf1")
	assert.Contains(t, out, "[14:30:22] Scope: workflows/test")
	assert.NotContains(t, out, "Debug:")
}

func TestConsoleWorkflowStartedWithDebugDir(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.WorkflowStarted{
		WorkflowID: "wf1",
		ScopeDir:   "workflows/test",
		DebugDir:   ".raymond/debug/wf1_20260115",
		Timestamp:  time.Date(2026, 1, 15, 14, 30, 22, 0, time.UTC),
	})

	assert.Contains(t, buf.String(), "Debug: .raymond/debug/wf1_20260115")
}

func TestConsoleWorkflowCompleted(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.WorkflowCompleted{TotalCostUSD: 0.1430, Timestamp: time.Now()})

	out := buf.String()
	assert.Contains(t, out, "Workflow completed")
	assert.Contains(t, out, "0.1430")
}

func TestConsoleWorkflowPaused(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.WorkflowPaused{PausedAgentCount: 2, TotalCostUSD: 0.05, Timestamp: time.Now()})

	out := buf.String()
	assert.Contains(t, out, "paused")
	assert.Contains(t, out, "2")
}

// ----------------------------------------------------------------------------
// State lifecycle
// ----------------------------------------------------------------------------

func TestConsoleStateStarted(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})

	assert.Contains(t, buf.String(), "[main] START.md")
}

func TestConsoleScriptStateStartedShowsProgress(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "CHECK.sh", StateType: "script"})

	assert.Contains(t, buf.String(), "Executing script...")
}

func TestConsoleScriptStateStartedQuietNoProgress(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, true)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "CHECK.sh", StateType: "script"})

	assert.NotContains(t, buf.String(), "Executing script...")
	assert.Contains(t, buf.String(), "[main] CHECK.sh") // header still shown
}

func TestConsoleStateCompletedMarkdown(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})
	b.Emit(events.StateCompleted{
		AgentID:      "main",
		CostUSD:      0.0353,
		TotalCostUSD: 0.0353,
		DurationMS:   1234,
	})

	out := buf.String()
	assert.Contains(t, out, `\-`)    // ASCII done symbol
	assert.Contains(t, out, "Done")
	assert.Contains(t, out, "0.0353")
}

func TestConsoleStateCompletedScript(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "CHECK.sh", StateType: "script", Timestamp: time.Now()})
	b.Emit(events.ScriptOutput{AgentID: "main", ExitCode: 0, ExecutionTimeMS: 125})
	b.Emit(events.StateCompleted{AgentID: "main", StateName: "CHECK.sh", DurationMS: 125, Timestamp: time.Now()})

	out := buf.String()
	assert.Contains(t, out, "exit 0")
	assert.Contains(t, out, "125ms")
	assert.NotContains(t, out, "$") // no cost shown for scripts
}

func TestConsoleStateCompletedScriptNonZeroExit(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "FAIL.sh", StateType: "script", Timestamp: time.Now()})
	b.Emit(events.ScriptOutput{AgentID: "main", ExitCode: 1, ExecutionTimeMS: 50})
	b.Emit(events.StateCompleted{AgentID: "main", StateName: "FAIL.sh", DurationMS: 50, Timestamp: time.Now()})

	assert.Contains(t, buf.String(), "exit 1")
}

// ----------------------------------------------------------------------------
// Progress messages and tool invocations
// ----------------------------------------------------------------------------

func TestConsoleProgressMessage(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.ProgressMessage{AgentID: "main", Message: "I'll analyze this data..."})

	assert.Contains(t, buf.String(), "I'll analyze this data...")
}

func TestConsoleProgressMessageSuppressedInQuiet(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, true)
	defer obs.Close()

	b.Emit(events.ProgressMessage{AgentID: "main", Message: "I'll analyze this data..."})

	assert.NotContains(t, buf.String(), "I'll analyze this data...")
}

func TestConsoleToolInvocationWithDetail(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.ToolInvocation{AgentID: "main", ToolName: "Write", Detail: "story.txt"})

	assert.Contains(t, buf.String(), "[Write] story.txt")
}

func TestConsoleToolInvocationNoDetail(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.ToolInvocation{AgentID: "main", ToolName: "Task"})

	out := buf.String()
	assert.Contains(t, out, "[Task]")
	assert.NotContains(t, out, "[Task] ") // no trailing space/detail
}

func TestConsoleToolInvocationSuppressedInQuiet(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, true)
	defer obs.Close()

	b.Emit(events.ToolInvocation{AgentID: "main", ToolName: "Write", Detail: "story.txt"})

	assert.NotContains(t, buf.String(), "[Write]")
}

// ----------------------------------------------------------------------------
// Transitions
// ----------------------------------------------------------------------------

func TestConsoleGotoTransition(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "START.md",
		ToState:        "NEXT.md",
		TransitionType: "goto",
		Metadata:       map[string]any{},
	})

	out := buf.String()
	assert.Contains(t, out, "goto -> NEXT.md")
}

func TestConsoleResetTransition(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "A.md",
		ToState:        "START.md",
		TransitionType: "reset",
		Metadata:       map[string]any{},
	})

	assert.Contains(t, buf.String(), "reset -> START.md")
}

func TestConsoleCallTransition(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "A.md",
		ToState:        "FUNC.md",
		TransitionType: "call",
		Metadata:       map[string]any{},
	})

	assert.Contains(t, buf.String(), "call -> FUNC.md")
}

func TestConsoleFunctionTransition(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "A.md",
		ToState:        "FUNC.md",
		TransitionType: "function",
		Metadata:       map[string]any{},
	})

	assert.Contains(t, buf.String(), "function -> FUNC.md")
}

func TestConsoleReturnTransitionWithSnippet(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "FUNC.md",
		ToState:        "CALLER.md",
		TransitionType: "result",
		Metadata:       map[string]any{"result_payload": "Analysis complete"},
	})

	out := buf.String()
	assert.Contains(t, out, "return")
	assert.Contains(t, out, "Analysis complete")
	assert.Contains(t, out, "CALLER.md")
}

func TestConsoleReturnTransitionEmptyPayload(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "FUNC.md",
		ToState:        "CALLER.md",
		TransitionType: "result",
		Metadata:       map[string]any{"result_payload": ""},
	})

	out := buf.String()
	assert.Contains(t, out, "return")
	assert.Contains(t, out, "CALLER.md")
	assert.NotContains(t, out, "()")
}

func TestConsoleReturnSnippetTruncatedAt20Chars(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "FUNC.md",
		ToState:        "CALLER.md",
		TransitionType: "result",
		Metadata:       map[string]any{"result_payload": "This is a very long result payload that exceeds twenty chars"},
	})

	out := buf.String()
	assert.Contains(t, out, "...")
	// The snippet must be at most 20 chars + "..." = 23 chars in parens.
	assert.NotContains(t, out, "twenty chars")
}

func TestConsoleReturnSnippetFirstLineOnly(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "FUNC.md",
		ToState:        "CALLER.md",
		TransitionType: "result",
		Metadata:       map[string]any{"result_payload": "first line\nsecond line"},
	})

	out := buf.String()
	assert.Contains(t, out, "first line")
	assert.NotContains(t, out, "second line")
}

func TestConsoleReturnSnippetWhitespaceTrimmed(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "FUNC.md",
		ToState:        "CALLER.md",
		TransitionType: "result",
		Metadata:       map[string]any{"result_payload": "   trimmed   "},
	})

	out := buf.String()
	assert.Contains(t, out, "(trimmed)")
}

func TestConsoleTerminationTransitionSkipsArrow(t *testing.T) {
	// TransitionOccurred with ToState="" (termination) should produce no output;
	// AgentTerminated handles the display.
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "LAST.md",
		ToState:        "",
		TransitionType: "result",
		Metadata:       map[string]any{},
	})

	assert.Empty(t, buf.String())
}

func TestConsoleForkTransitionSkipsArrow(t *testing.T) {
	// TransitionOccurred with type "fork" should produce no output;
	// AgentSpawned handles the fork display.
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "START.md",
		ToState:        "PARENT_NEXT.md",
		TransitionType: "fork",
		Metadata:       map[string]any{"spawned_agent_id": "main_worker1"},
	})

	// Fork display comes from AgentSpawned, not TransitionOccurred.
	assert.NotContains(t, buf.String(), "PARENT_NEXT.md")
}

// ----------------------------------------------------------------------------
// Fork (AgentSpawned)
// ----------------------------------------------------------------------------

func TestConsoleForkDisplayViaAgentSpawned(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.AgentSpawned{
		ParentAgentID: "main",
		NewAgentID:    "main_worker1",
		InitialState:  "WORKER.md",
	})

	out := buf.String()
	assert.Contains(t, out, "WORKER.md")
	assert.Contains(t, out, "main_worker1")
	// ASCII fork symbol and arrow.
	assert.Contains(t, out, "++")
	assert.Contains(t, out, "->")
}

// ----------------------------------------------------------------------------
// Agent termination
// ----------------------------------------------------------------------------

func TestConsoleAgentTerminatedWithPayload(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.AgentTerminated{AgentID: "main", ResultPayload: "Story complete"})

	out := buf.String()
	assert.Contains(t, out, "Story complete")
	assert.Contains(t, out, "Result:")
}

func TestConsoleAgentTerminatedEmptyPayload(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.AgentTerminated{AgentID: "main", ResultPayload: ""})

	assert.Contains(t, buf.String(), "(terminated)")
}

func TestConsoleAgentTerminatedWhitespacePaddedPayload(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.AgentTerminated{AgentID: "main", ResultPayload: "  \nStory complete\n  "})

	out := buf.String()
	assert.Contains(t, out, "Story complete")
	assert.NotContains(t, out, "Result: \"  ")
	assert.NotContains(t, out, "Result: \"\\n")
}

func TestConsoleAgentTerminatedWhitespaceOnlyPayload(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.AgentTerminated{AgentID: "main", ResultPayload: "   \n  "})

	out := buf.String()
	assert.Contains(t, out, "(terminated)")
	assert.NotContains(t, out, "Result:")
}

// ----------------------------------------------------------------------------
// Errors
// ----------------------------------------------------------------------------

func TestConsoleErrorRetryMessage(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.ErrorOccurred{
		AgentID:      "main",
		ErrorMessage: "No transition tag",
		IsRetryable:  true,
		RetryCount:   1,
		MaxRetries:   3,
	})

	out := buf.String()
	assert.Contains(t, out, "No transition tag")
	assert.Contains(t, out, "retrying")
	assert.Contains(t, out, "1/3")
}

func TestConsoleErrorFatalMessage(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.ErrorOccurred{
		AgentID:      "main",
		ErrorMessage: "Usage limit hit",
		IsRetryable:  false,
	})

	out := buf.String()
	assert.Contains(t, out, "Usage limit hit")
	assert.NotContains(t, out, "retrying")
}

// ----------------------------------------------------------------------------
// Unicode vs ASCII symbols
// ----------------------------------------------------------------------------

func TestConsoleUnicodeArrow(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := console.NewWithWriter(b, false, 0, &buf, true, false) // unicode=true
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "A.md",
		ToState:        "B.md",
		TransitionType: "goto",
		Metadata:       map[string]any{},
	})

	assert.Contains(t, buf.String(), "goto → B.md")
	assert.NotContains(t, buf.String(), "->")
}

func TestConsoleASCIIArrow(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false) // unicode=false
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "A.md",
		ToState:        "B.md",
		TransitionType: "goto",
		Metadata:       map[string]any{},
	})

	assert.Contains(t, buf.String(), "goto -> B.md")
	assert.NotContains(t, buf.String(), "→")
}

func TestConsoleUnicodeFork(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := console.NewWithWriter(b, false, 0, &buf, true, false)
	defer obs.Close()

	b.Emit(events.AgentSpawned{
		ParentAgentID: "main",
		NewAgentID:    "main_worker1",
		InitialState:  "WORKER.md",
	})

	out := buf.String()
	assert.Contains(t, out, "⑂")
	assert.Contains(t, out, "→")
}

// ----------------------------------------------------------------------------
// AgentPaused
// ----------------------------------------------------------------------------

func TestConsoleAgentPausedShowsAgentAndReason(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.AgentPaused{AgentID: "main", Reason: events.PauseReasonUsageLimit, Timestamp: time.Now()})

	out := buf.String()
	assert.Contains(t, out, "[main]")
	assert.Contains(t, out, "paused")
	assert.Contains(t, out, events.PauseReasonUsageLimit)
}

func TestConsoleAgentPausedTimeoutReason(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.AgentPaused{AgentID: "worker1", Reason: events.PauseReasonTimeout, Timestamp: time.Now()})

	out := buf.String()
	assert.Contains(t, out, "[worker1]")
	assert.Contains(t, out, events.PauseReasonTimeout)
}

// ----------------------------------------------------------------------------
// Close
// ----------------------------------------------------------------------------

func TestConsoleCloseUnsubscribes(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	obs.Close()

	b.Emit(events.WorkflowStarted{
		WorkflowID: "wf1",
		ScopeDir:   "test",
		Timestamp:  time.Now(),
	})

	assert.Empty(t, buf.String())
}

// ----------------------------------------------------------------------------
// Color output
// ----------------------------------------------------------------------------

// newColorObs creates a ConsoleObserver with color=true and unicode=false.
func newColorObs(b *bus.Bus, buf *bytes.Buffer) *console.ConsoleObserver {
	return console.NewWithWriter(b, false, 0, buf, false, true)
}

func TestConsoleColorAgentIDWrappedInEscapes(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})

	out := buf.String()
	assert.Contains(t, out, "\x1b[")
	assert.Contains(t, out, "main")
	assert.Contains(t, out, "\x1b[0m")
	assert.Contains(t, out, "START.md")
}

func TestConsoleColorFirstAgentGetsCyan(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})

	// Cyan (\x1b[36m) is the first color in the palette.
	assert.Contains(t, buf.String(), "\x1b[36m[main]\x1b[0m")
}

func TestConsoleColorSecondAgentGetsYellow(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})
	b.Emit(events.StateStarted{AgentID: "worker1", StateName: "TASK.md", StateType: "markdown"})

	out := buf.String()
	assert.Contains(t, out, "\x1b[36m[main]\x1b[0m")    // cyan
	assert.Contains(t, out, "\x1b[33m[worker1]\x1b[0m") // yellow
}

func TestConsoleColorSameAgentSameColor(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "A.md", StateType: "markdown"})
	b.Emit(events.StateStarted{AgentID: "main", StateName: "B.md", StateType: "markdown"})

	out := buf.String()
	// Both occurrences of [main] must use the same (cyan) color.
	assert.Equal(t, 2, strings.Count(out, "\x1b[36m[main]\x1b[0m"))
}

func TestConsoleNoColorAgentIDPlain(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false) // color=false
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})

	out := buf.String()
	assert.Contains(t, out, "[main] START.md")
	assert.NotContains(t, out, "\x1b[")
}

func TestConsoleColorErrorMessageRed(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.ErrorOccurred{ErrorMessage: "something went wrong", IsRetryable: false})

	assert.Contains(t, buf.String(), "\x1b[31msomething went wrong\x1b[0m")
}

func TestConsoleColorAgentPausedYellowReason(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.AgentPaused{AgentID: "main", Reason: events.PauseReasonUsageLimit, Timestamp: time.Now()})

	out := buf.String()
	assert.Contains(t, out, "\x1b[36m[main]\x1b[0m")                            // agent cyan
	assert.Contains(t, out, "\x1b[33m"+events.PauseReasonUsageLimit+"\x1b[0m") // reason yellow
}

func TestConsoleColorCycleWrapsAfterSix(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	agents := []string{"a", "b", "c", "d", "e", "f", "g"}
	for _, id := range agents {
		b.Emit(events.StateStarted{AgentID: id, StateName: "X.md", StateType: "markdown"})
	}

	out := buf.String()
	// 7th agent wraps back to cyan (same as 1st).
	assert.Contains(t, out, "\x1b[36m[a]\x1b[0m")
	assert.Contains(t, out, "\x1b[36m[g]\x1b[0m")
}
