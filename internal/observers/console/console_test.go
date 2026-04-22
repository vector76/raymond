package console_test

import (
	"bytes"
	"fmt"
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

	tokens := int64(95307)
	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})
	b.Emit(events.StateCompleted{
		AgentID:      "main",
		CostUSD:      0.0353,
		TotalCostUSD: 0.0353,
		DurationMS:   1234,
		InputTokens:  &tokens,
	})

	out := buf.String()
	assert.Contains(t, out, `\-`)    // ASCII done symbol
	assert.Contains(t, out, "Done")
	assert.Contains(t, out, "0.0353")
	assert.Contains(t, out, "95.3k tokens")
}

func TestConsoleStateCompletedMarkdownZeroTokens(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	tokens := int64(0)
	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})
	b.Emit(events.StateCompleted{
		AgentID:      "main",
		CostUSD:      0.0,
		TotalCostUSD: 0.0,
		DurationMS:   100,
		InputTokens:  &tokens,
	})

	assert.Contains(t, buf.String(), "0.0k tokens")
}

func TestConsoleStateCompletedMarkdownMissingTokens(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})
	b.Emit(events.StateCompleted{
		AgentID:      "main",
		CostUSD:      0.0,
		TotalCostUSD: 0.0,
		DurationMS:   100,
		InputTokens:  nil,
	})

	assert.Contains(t, buf.String(), "--- tokens")
}

func TestConsoleStateCompletedMarkdownRounding(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	emitCompleted := func(tokens int64) {
		b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})
		b.Emit(events.StateCompleted{AgentID: "main", InputTokens: &tokens})
	}

	// 950 tokens → math.Round(9.5)/10 = 1.0 → "1.0k tokens" (rounds up at midpoint)
	emitCompleted(950)
	assert.Contains(t, buf.String(), "1.0k tokens")
	buf.Reset()

	// 949 tokens → math.Round(9.49)/10 = 0.9 → "0.9k tokens"
	emitCompleted(949)
	assert.Contains(t, buf.String(), "0.9k tokens")
	buf.Reset()

	// 105507 → math.Round(1055.07)/10 = 1055/10 = 105.5 → "105.5k tokens"
	emitCompleted(105507)
	assert.Contains(t, buf.String(), "105.5k tokens")
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
	assert.NotContains(t, out, "tokens")
	assert.NotContains(t, out, "---")
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
// AgentAwaitStarted / AgentAwaitResumed
// ----------------------------------------------------------------------------

func TestConsoleAgentAwaitStartedShowsPrompt(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.AgentAwaitStarted{
		AgentID:   "main",
		InputID:   "input-1",
		Prompt:    "Please provide the API key",
		NextState: "NEXT.md",
		Timestamp: time.Now(),
	})

	out := buf.String()
	assert.Contains(t, out, "[main]")
	assert.Contains(t, out, "awaiting human input")
	assert.Contains(t, out, "Please provide the API key")
}

func TestConsoleAgentAwaitResumedShowsResume(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.AgentAwaitResumed{
		AgentID:   "main",
		InputID:   "input-1",
		Timestamp: time.Now(),
	})

	out := buf.String()
	assert.Contains(t, out, "[main]")
	assert.Contains(t, out, "input received, resuming")
}

func TestConsoleAgentAwaitStartedLongPromptTruncated(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObsWithWidth(b, &buf, 80)
	defer obs.Close()

	longPrompt := strings.Repeat("q", 200)
	b.Emit(events.AgentAwaitStarted{
		AgentID:   "main",
		InputID:   "input-2",
		Prompt:    longPrompt,
		NextState: "NEXT.md",
		Timestamp: time.Now(),
	})

	out := buf.String()
	assert.Contains(t, out, "...")
	assert.Contains(t, out, "awaiting human input")
	// Line must fit within terminal width.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.LessOrEqual(t, len(lines[0]), 80)
}

func TestConsoleAgentAwaitStartedEmptyPrompt(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.AgentAwaitStarted{
		AgentID:   "main",
		InputID:   "input-e",
		Prompt:    "",
		NextState: "NEXT.md",
		Timestamp: time.Now(),
	})

	out := buf.String()
	assert.Contains(t, out, "[main]")
	assert.Contains(t, out, "awaiting human input")
}

func TestConsoleAgentAwaitStartedMultilinePromptUsesFirstLine(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false)
	defer obs.Close()

	b.Emit(events.AgentAwaitStarted{
		AgentID:   "main",
		InputID:   "input-3",
		Prompt:    "First line of prompt\nSecond line of prompt\nThird line",
		NextState: "NEXT.md",
		Timestamp: time.Now(),
	})

	out := buf.String()
	assert.Contains(t, out, "First line of prompt")
	assert.NotContains(t, out, "Second line of prompt")
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

// newColorUnicodeObs creates a ConsoleObserver with color=true and unicode=true.
func newColorUnicodeObs(b *bus.Bus, buf *bytes.Buffer) *console.ConsoleObserver {
	return console.NewWithWriter(b, false, 0, buf, true, true)
}

func TestConsoleColorProgressSymbolColored(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.ProgressMessage{AgentID: "main", Message: "hello", Timestamp: time.Now()})

	out := buf.String()
	assert.Contains(t, out, "\x1b[36m|-\x1b[0m")
	assert.Contains(t, out, "\x1b[0m hello\n")
}

func TestConsoleColorDoneSymbolColored(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})
	b.Emit(events.StateCompleted{AgentID: "main", CostUSD: 0.01, TotalCostUSD: 0.01, DurationMS: 100})

	out := buf.String()
	assert.Contains(t, out, "\x1b[36m\\-\x1b[0m")
}

func TestConsoleColorForkSymbolUsesParentColor(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.StateStarted{AgentID: "main", StateName: "START.md", StateType: "markdown"})
	b.Emit(events.StateStarted{AgentID: "worker1", StateName: "TASK.md", StateType: "markdown"})
	b.Emit(events.AgentSpawned{ParentAgentID: "main", NewAgentID: "worker1", InitialState: "WORKER.md"})

	out := buf.String()
	assert.Contains(t, out, "\x1b[36m++\x1b[0m")    // cyan = parent's color
	assert.NotContains(t, out, "\x1b[33m++\x1b[0m") // not yellow = new agent's color
}

func TestConsoleColorResultSymbolColored(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.AgentTerminated{AgentID: "main", ResultPayload: "done"})

	out := buf.String()
	assert.Contains(t, out, "\x1b[36m=>\x1b[0m")
}

func TestConsoleColorSymbolsInUnicodeMode(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorUnicodeObs(b, &buf)
	defer obs.Close()

	b.Emit(events.ProgressMessage{AgentID: "main", Message: "hi", Timestamp: time.Now()})
	assert.Contains(t, buf.String(), "\x1b[36m├─\x1b[0m")

	b.Emit(events.StateCompleted{AgentID: "main", CostUSD: 0.01, TotalCostUSD: 0.01, DurationMS: 100})
	assert.Contains(t, buf.String(), "\x1b[36m└─\x1b[0m")
}

func TestConsoleColorSecondAgentSymbolDifferentColor(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.ProgressMessage{AgentID: "main", Message: "msg1", Timestamp: time.Now()})
	b.Emit(events.ProgressMessage{AgentID: "worker1", Message: "msg2", Timestamp: time.Now()})

	out := buf.String()
	assert.Contains(t, out, "\x1b[36m|-\x1b[0m") // main = cyan
	assert.Contains(t, out, "\x1b[33m|-\x1b[0m") // worker1 = yellow
}

func TestConsoleColorGotoTransitionWordColored(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{AgentID: "main", TransitionType: "goto", ToState: "NEXT.md"})

	out := buf.String()
	assert.Contains(t, out, "\x1b[36mgoto\x1b[0m")
	assert.Contains(t, out, "\x1b[0m -> NEXT.md\n")
}

func TestConsoleColorReturnWithSnippetWordColored(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		TransitionType: "result",
		ToState:        "CALLER.md",
		Metadata:       map[string]any{"result_payload": "ok"},
	})

	out := buf.String()
	assert.Contains(t, out, "\x1b[36mreturn\x1b[0m")
	assert.Contains(t, out, "\x1b[0m (ok) -> CALLER.md\n")
}

func TestConsoleColorReturnWithoutSnippetWordColored(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		TransitionType: "result",
		ToState:        "CALLER.md",
		Metadata:       map[string]any{"result_payload": ""},
	})

	out := buf.String()
	assert.Contains(t, out, "\x1b[36mreturn\x1b[0m")
	assert.Contains(t, out, "\x1b[0m -> CALLER.md\n")
}

func TestConsoleNoColorSymbolsNoEscapes(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObs(b, &buf, false) // color=false
	defer obs.Close()

	b.Emit(events.ProgressMessage{AgentID: "main", Message: "hello", Timestamp: time.Now()})

	out := buf.String()
	assert.NotContains(t, out, "\x1b[")
}

// ----------------------------------------------------------------------------
// Terminal-width-aware truncation
// ----------------------------------------------------------------------------

// newObsWithWidth creates a ConsoleObserver with an explicit terminal width.
func newObsWithWidth(b *bus.Bus, buf *bytes.Buffer, width int) *console.ConsoleObserver {
	return console.NewWithWriter(b, false, width, buf, false, false)
}

func TestConsoleProgressMessageTruncatedToTerminalWidth(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObsWithWidth(b, &buf, 60)
	defer obs.Close()

	// "  |- " = 5 chars prefix, 2 safety margin = 53 chars available
	longMsg := strings.Repeat("x", 80)
	b.Emit(events.ProgressMessage{AgentID: "main", Message: longMsg, Timestamp: time.Now()})

	out := buf.String()
	// Should be truncated with "..."
	assert.Contains(t, out, "...")
	// Total line should not exceed terminal width (prefix + content + newline)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.LessOrEqual(t, len(lines[0]), 60)
}

func TestConsoleProgressMessageShortNotTruncated(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObsWithWidth(b, &buf, 80)
	defer obs.Close()

	b.Emit(events.ProgressMessage{AgentID: "main", Message: "short msg", Timestamp: time.Now()})

	out := buf.String()
	assert.Contains(t, out, "short msg")
	assert.NotContains(t, out, "...")
}

func TestConsoleToolInvocationDetailTruncatedToTerminalWidth(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newObsWithWidth(b, &buf, 60)
	defer obs.Close()

	longCmd := strings.Repeat("a", 100)
	b.Emit(events.ToolInvocation{AgentID: "main", ToolName: "Bash", Detail: longCmd, Timestamp: time.Now()})

	out := buf.String()
	assert.Contains(t, out, "...")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.LessOrEqual(t, len(lines[0]), 60)
}

func TestConsoleMinContentWidthFloor(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	// Very narrow terminal — content should still get at least 40 chars
	obs := newObsWithWidth(b, &buf, 20)
	defer obs.Close()

	msg := strings.Repeat("z", 50)
	b.Emit(events.ProgressMessage{AgentID: "main", Message: msg, Timestamp: time.Now()})

	out := buf.String()
	// With min width 40 and message of 50, it should truncate to 40
	// (37 chars + "...") rather than some tiny number
	assert.Contains(t, out, strings.Repeat("z", 37)+"...")
}

// ----------------------------------------------------------------------------
// Collision-avoidance color tests
// ----------------------------------------------------------------------------

func TestConsoleColorCollisionAvoidanceBasic(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	// main→cyan (pos 0), worker1→yellow (pos 1); both remain live.
	b.Emit(events.StateStarted{AgentID: "main", StateName: "A.md", StateType: "markdown"})
	b.Emit(events.StateStarted{AgentID: "worker1", StateName: "B.md", StateType: "markdown"})

	// worker2 assigned while main and worker1 live: pos 2=magenta is free → magenta.
	b.Emit(events.StateStarted{AgentID: "worker2", StateName: "C.md", StateType: "markdown"})

	// Terminate main; worker3 assigned: pos 3=green is free → green (not yellow, worker1 still live).
	b.Emit(events.AgentTerminated{AgentID: "main", ResultPayload: "done"})
	b.Emit(events.StateStarted{AgentID: "worker3", StateName: "D.md", StateType: "markdown"})

	out := buf.String()
	assert.Contains(t, out, "\x1b[35m[worker2]\x1b[0m") // magenta — not cyan or yellow (both occupied)
	assert.Contains(t, out, "\x1b[32m[worker3]\x1b[0m") // green — not yellow (worker1 still live)
}

func TestConsoleColorSkipsAtRotationWrap(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	// Assign all 6 palette slots (counter goes 0→6, ending at position 0).
	agents := []string{"a", "b", "c", "d", "e", "f"}
	for _, id := range agents {
		b.Emit(events.StateStarted{AgentID: id, StateName: "X.md", StateType: "markdown"})
	}

	// Terminate "b"–"f", leaving only "a" (cyan) live. Counter is at position 0.
	for _, id := range []string{"b", "c", "d", "e", "f"} {
		b.Emit(events.AgentTerminated{AgentID: id, ResultPayload: "done"})
	}

	// Assign "g": position 0 is cyan, occupied by still-live "a" — skip must fire.
	b.Emit(events.StateStarted{AgentID: "g", StateName: "X.md", StateType: "markdown"})

	out := buf.String()
	// "g" must NOT be cyan (position 0 is occupied by "a"); it gets yellow (next free slot).
	assert.NotContains(t, out, "\x1b[36m[g]\x1b[0m")
	assert.Contains(t, out, "\x1b[33m[g]\x1b[0m")
}

func TestConsoleColorCounterAdvancesByOneNotBySkipCount(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	// Assign "main" (gets cyan, counter 0→1).
	b.Emit(events.StateStarted{AgentID: "main", StateName: "X.md", StateType: "markdown"})

	// Assign w1–w5 and immediately terminate each (counter 1→6, only "main" remains live).
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("w%d", i)
		b.Emit(events.StateStarted{AgentID: id, StateName: "X.md", StateType: "markdown"})
		b.Emit(events.AgentTerminated{AgentID: id, ResultPayload: "done"})
	}

	// counter=6, pos=0=cyan is occupied by "main". Skip fires → w6 gets yellow (pos 1). Counter→7.
	b.Emit(events.StateStarted{AgentID: "w6", StateName: "X.md", StateType: "markdown"})

	// Terminate w6 (releases yellow).
	b.Emit(events.AgentTerminated{AgentID: "w6", ResultPayload: "done"})

	// counter=7, pos=7%6=1=yellow, which is now free → w7 must get yellow.
	b.Emit(events.StateStarted{AgentID: "w7", StateName: "X.md", StateType: "markdown"})

	out := buf.String()
	// If counter had incorrectly advanced past the skip destination, w7 would get magenta instead.
	assert.Contains(t, out, "\x1b[33m[w7]\x1b[0m")
}

func TestConsoleColorFallbackWhenAllOccupied(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	// Fill all 6 palette slots (counter=6, position=0=cyan).
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		b.Emit(events.StateStarted{AgentID: id, StateName: "X.md", StateType: "markdown"})
	}

	// Assign 7th agent "g": all colors occupied, fallback fires → g gets cyan (pos 0).
	b.Emit(events.StateStarted{AgentID: "g", StateName: "X.md", StateType: "markdown"})

	out := buf.String()
	// No panic must occur, and g gets the fallback color: cyan (next-in-rotation, pos 0).
	assert.Contains(t, out, "\x1b[36m[g]\x1b[0m")
}

func TestConsoleColorTerminationReleasesColor(t *testing.T) {
	b := bus.New()
	var buf bytes.Buffer
	obs := newColorObs(b, &buf)
	defer obs.Close()

	// Fill all 6 palette slots (counter=6, all live).
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		b.Emit(events.StateStarted{AgentID: id, StateName: "X.md", StateType: "markdown"})
	}

	// Terminate only "a" (releases cyan). counter is at position 0.
	b.Emit(events.AgentTerminated{AgentID: "a", ResultPayload: "done"})

	// Assign "g": position 0=cyan is now free → g must take cyan.
	b.Emit(events.StateStarted{AgentID: "g", StateName: "X.md", StateType: "markdown"})

	out := buf.String()
	// Contrast with TestConsoleColorSkipsAtRotationWrap: here cyan is freed, so g takes it.
	assert.Contains(t, out, "\x1b[36m[g]\x1b[0m")
}
