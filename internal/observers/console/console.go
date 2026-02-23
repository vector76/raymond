// Package console implements the real-time console output observer for raymond.
//
// ConsoleObserver subscribes to the event bus and writes formatted human-
// readable output to a writer (default os.Stdout). The output format uses
// box-drawing characters (or ASCII fallbacks) to show workflow structure:
//
//	[main] START.md
//	  ├─ I'll begin the story...
//	  ├─ [Write] story.txt
//	  └─ Done ($0.0353, total: $0.0353)
//	  → CONFLICT.md
//
// Quiet mode suppresses assistant text progress messages and tool invocations,
// showing only state headers, transitions, errors, done lines, and results.
//
// Unicode symbols are selected automatically based on whether the writer is a
// terminal; the NewWithWriter constructor accepts an explicit unicode flag for
// tests and non-terminal use cases.
package console

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
)

// symbols holds the display characters used in console output.
// Two variants: unicode box-drawing and plain ASCII.
type symbols struct {
	progress  string // ├─  or  |-
	done      string // └─  or  \-
	arrow     string // →   or  ->
	result    string // ⇒   or  =>
	fork      string // ⑂   or  ++
	forkArrow string // →   or  ->
	warn      string // !
}

var unicodeSyms = symbols{
	progress:  "├─",
	done:      "└─",
	arrow:     "→",
	result:    "⇒",
	fork:      "⑂",
	forkArrow: "→",
	warn:      "!",
}

var asciiSyms = symbols{
	progress:  "|-",
	done:      `\-`,
	arrow:     "->",
	result:    "=>",
	fork:      "++",
	forkArrow: "->",
	warn:      "!",
}

// ----------------------------------------------------------------------------
// ConsoleReporter
// ----------------------------------------------------------------------------

// ConsoleReporter formats and writes console output lines.
// It tracks per-agent state needed to format certain messages correctly.
type ConsoleReporter struct {
	mu  sync.Mutex
	w   io.Writer
	sym symbols

	// quiet and verbose are immutable after construction; safe to read without
	// the lock.
	quiet   bool
	verbose bool // show transition type labels and extra tool detail

	// Per-agent tracking — protected by mu.
	lastStateType map[string]string // agentID → "markdown" or "script"
	lastExitCode  map[string]int    // agentID → script exit code
	lastTool      map[string]string // agentID → last tool name (for error ctx)
}

func newReporter(w io.Writer, quiet, verbose, unicode bool) *ConsoleReporter {
	sym := asciiSyms
	if unicode {
		sym = unicodeSyms
	}
	return &ConsoleReporter{
		w:             w,
		sym:           sym,
		quiet:         quiet,
		verbose:       verbose,
		lastStateType: make(map[string]string),
		lastExitCode:  make(map[string]int),
		lastTool:      make(map[string]string),
	}
}

// --- event handlers (called from ConsoleObserver subscriptions) ---

func (r *ConsoleReporter) onWorkflowStarted(e events.WorkflowStarted) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ts := e.Timestamp.Format("15:04:05")
	fmt.Fprintf(r.w, "[%s] Workflow: %s\n", ts, e.WorkflowID)
	fmt.Fprintf(r.w, "[%s] Scope: %s\n", ts, e.ScopeDir)
	if e.DebugDir != "" {
		fmt.Fprintf(r.w, "[%s] Debug: %s\n", ts, e.DebugDir)
	}
}

func (r *ConsoleReporter) onWorkflowCompleted(e events.WorkflowCompleted) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, "Workflow completed. Total cost: $%.4f\n", e.TotalCostUSD)
}

func (r *ConsoleReporter) onWorkflowPaused(e events.WorkflowPaused) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, "Workflow paused. %d agent(s) paused. Total cost: $%.4f\n",
		e.PausedAgentCount, e.TotalCostUSD)
}

func (r *ConsoleReporter) onWorkflowWaiting(e events.WorkflowWaiting) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, "Usage limit reached. Waiting %.0f seconds before resuming.\n", e.WaitSeconds)
}

func (r *ConsoleReporter) onWorkflowResuming(e events.WorkflowResuming) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, "Resuming workflow.\n")
}

func (r *ConsoleReporter) onStateStarted(e events.StateStarted) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastStateType[e.AgentID] = e.StateType
	fmt.Fprintf(r.w, "[%s] %s\n", e.AgentID, e.StateName)
	if e.StateType == events.StateTypeScript && !r.quiet {
		fmt.Fprintf(r.w, "  %s Executing script...\n", r.sym.progress)
	}
}

func (r *ConsoleReporter) onStateCompleted(e events.StateCompleted) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stateType := r.lastStateType[e.AgentID]
	if stateType == events.StateTypeScript {
		exitCode := r.lastExitCode[e.AgentID]
		fmt.Fprintf(r.w, "  %s Done (exit %d, %.0fms)\n", r.sym.done, exitCode, e.DurationMS)
	} else {
		fmt.Fprintf(r.w, "  %s Done ($%.4f, total: $%.4f)\n",
			r.sym.done, e.CostUSD, e.TotalCostUSD)
	}
}

func (r *ConsoleReporter) onProgressMessage(e events.ProgressMessage) {
	if r.quiet {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, "  %s %s\n", r.sym.progress, e.Message)
}

func (r *ConsoleReporter) onToolInvocation(e events.ToolInvocation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastTool[e.AgentID] = e.ToolName
	if r.quiet {
		return
	}
	if e.Detail != "" {
		fmt.Fprintf(r.w, "  %s [%s] %s\n", r.sym.progress, e.ToolName, e.Detail)
	} else {
		fmt.Fprintf(r.w, "  %s [%s]\n", r.sym.progress, e.ToolName)
	}
}

func (r *ConsoleReporter) onScriptOutput(e events.ScriptOutput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastExitCode[e.AgentID] = e.ExitCode
}

func (r *ConsoleReporter) onTransitionOccurred(e events.TransitionOccurred) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch {
	case e.ToState == "":
		// Agent termination — displayed by onAgentTerminated; skip here.
		return

	case e.TransitionType == "fork":
		// Fork — displayed by onAgentSpawned; skip here.
		return

	case e.TransitionType == "result":
		// Return transition (result with non-empty return stack).
		payload := ""
		if p, ok := e.Metadata["result_payload"].(string); ok {
			payload = p
		}
		snippet := returnSnippet(payload)
		if snippet != "" {
			fmt.Fprintf(r.w, "  return (%s) %s %s\n", snippet, r.sym.arrow, e.ToState)
		} else {
			fmt.Fprintf(r.w, "  return %s %s\n", r.sym.arrow, e.ToState)
		}

	default:
		// goto, reset, call, function — all use the same arrow.
		if r.verbose {
			fmt.Fprintf(r.w, "  %s (%s) %s\n", r.sym.arrow, e.TransitionType, e.ToState)
		} else {
			fmt.Fprintf(r.w, "  %s %s\n", r.sym.arrow, e.ToState)
		}
	}
}

func (r *ConsoleReporter) onAgentSpawned(e events.AgentSpawned) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// ⑂ WORKER.md → main_worker1
	fmt.Fprintf(r.w, "  %s %s %s %s\n",
		r.sym.fork, e.InitialState, r.sym.forkArrow, e.NewAgentID)
}

func (r *ConsoleReporter) onAgentTerminated(e events.AgentTerminated) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.ResultPayload != "" {
		fmt.Fprintf(r.w, "  %s Result: %q\n", r.sym.result, e.ResultPayload)
	} else {
		fmt.Fprintf(r.w, "  %s (terminated)\n", r.sym.result)
	}
}

func (r *ConsoleReporter) onErrorOccurred(e events.ErrorOccurred) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.IsRetryable {
		fmt.Fprintf(r.w, "  %s %s - retrying (%d/%d)\n",
			r.sym.warn, e.ErrorMessage, e.RetryCount, e.MaxRetries)
	} else {
		fmt.Fprintf(r.w, "  %s %s\n", r.sym.warn, e.ErrorMessage)
	}
}

// returnSnippet extracts a short display snippet from a return-transition
// result payload, applying these rules in order:
//
//  1. Trim leading/trailing whitespace.
//  2. Take only the first line.
//  3. Truncate to 20 characters, appending "..." if truncated.
//
// Returns an empty string when the trimmed payload is empty.
func returnSnippet(payload string) string {
	s := strings.TrimSpace(payload)
	if s == "" {
		return ""
	}
	// First line only.
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)
	if len(s) > 20 {
		s = s[:20] + "..."
	}
	return s
}

// ----------------------------------------------------------------------------
// ConsoleObserver
// ----------------------------------------------------------------------------

// ConsoleObserver subscribes to the event bus and writes formatted console
// output via a ConsoleReporter.
type ConsoleObserver struct {
	reporter *ConsoleReporter
	cancels  []func()
}

// New creates a ConsoleObserver writing to os.Stdout. Unicode symbols are
// used automatically when os.Stdout is a character device (terminal).
func New(b *bus.Bus, quiet, verbose bool, width int) *ConsoleObserver {
	unicode := isCharDevice(os.Stdout)
	return NewWithWriter(b, quiet, verbose, width, os.Stdout, unicode)
}

// NewWithWriter creates a ConsoleObserver writing to w with an explicit
// unicode setting. Use this in tests to capture output predictably.
func NewWithWriter(b *bus.Bus, quiet, verbose bool, _ int, w io.Writer, unicode bool) *ConsoleObserver {
	r := newReporter(w, quiet, verbose, unicode)
	o := &ConsoleObserver{reporter: r}
	o.cancels = []func(){
		bus.Subscribe(b, r.onWorkflowStarted),
		bus.Subscribe(b, r.onWorkflowCompleted),
		bus.Subscribe(b, r.onWorkflowPaused),
		bus.Subscribe(b, r.onWorkflowWaiting),
		bus.Subscribe(b, r.onWorkflowResuming),
		bus.Subscribe(b, r.onStateStarted),
		bus.Subscribe(b, r.onStateCompleted),
		bus.Subscribe(b, r.onProgressMessage),
		bus.Subscribe(b, r.onToolInvocation),
		bus.Subscribe(b, r.onScriptOutput),
		bus.Subscribe(b, r.onTransitionOccurred),
		bus.Subscribe(b, r.onAgentSpawned),
		bus.Subscribe(b, r.onAgentTerminated),
		bus.Subscribe(b, r.onErrorOccurred),
	}
	return o
}

// Close unregisters all bus subscriptions.
func (o *ConsoleObserver) Close() {
	for _, cancel := range o.cancels {
		cancel()
	}
	o.cancels = nil
}

// isCharDevice reports whether f is a character device (i.e. a terminal).
func isCharDevice(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
