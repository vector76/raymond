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
// Unicode symbols and ANSI colors are selected automatically based on whether
// the writer is a terminal. Colors are suppressed when the NO_COLOR environment
// variable is set. NewWithWriter accepts explicit unicode and color flags for
// tests and non-terminal use cases.
package console

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
)

const (
	minContentWidth     = 40  // Minimum characters for truncated content
	maxContentWidth     = 160 // Maximum characters for truncated content
	defaultTerminalWidth = 80  // Default when detection fails

	// Prefix lengths for width calculation (character counts including indentation).
	prefixTreeBranch = 5 // "  ├─ "
	prefixResult     = 15 // `  ⇒ Result: "` plus closing quote
)

// agentColors is the cycling palette for agent ID labels, matching the Python
// reference implementation order: cyan, yellow, magenta, green, blue, red.
var agentColors = []string{
	"\x1b[36m", // cyan
	"\x1b[33m", // yellow
	"\x1b[35m", // magenta
	"\x1b[32m", // green
	"\x1b[34m", // blue
	"\x1b[31m", // red
}

const (
	colorReset   = "\x1b[0m"
	colorError   = "\x1b[31m" // red
	colorWarning = "\x1b[33m" // yellow
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

	// quiet, color, and widthOverride are immutable after construction; safe to read without the lock.
	quiet         bool
	color         bool // emit ANSI color codes
	widthOverride int  // explicit terminal width; 0 = auto-detect

	// Per-agent tracking — protected by mu.
	lastStateType map[string]string // agentID → "markdown" or "script"
	lastExitCode  map[string]int    // agentID → script exit code
	lastTool      map[string]string // agentID → last tool name (for error ctx)
	agentColorMap map[string]string // agentID → assigned ANSI color code
	agentCounter  int               // number of unique agents seen (for cycling)
}

func newReporter(w io.Writer, quiet, unicode, color bool, width int) *ConsoleReporter {
	sym := asciiSyms
	if unicode {
		sym = unicodeSyms
	}
	return &ConsoleReporter{
		w:             w,
		sym:           sym,
		quiet:         quiet,
		color:         color,
		widthOverride: width,
		lastStateType: make(map[string]string),
		lastExitCode:  make(map[string]int),
		lastTool:      make(map[string]string),
		agentColorMap: make(map[string]string),
	}
}

// detectTerminalWidth returns the current terminal width using the same
// priority as the Python reference: explicit override → COLUMNS env var →
// terminal query → default 80.
func (r *ConsoleReporter) detectTerminalWidth() int {
	if r.widthOverride > 0 {
		return r.widthOverride
	}
	if s := os.Getenv("COLUMNS"); s != "" {
		if w, err := strconv.Atoi(s); err == nil && w > 0 {
			return w
		}
	}
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return defaultTerminalWidth
}

// availableWidth calculates the number of characters available for message
// content after accounting for the prefix and a 2-character safety margin.
// The result is clamped to [minContentWidth, maxContentWidth].
func (r *ConsoleReporter) availableWidth(prefixLen int) int {
	tw := r.detectTerminalWidth()
	avail := tw - prefixLen - 2
	if avail < minContentWidth {
		return minContentWidth
	}
	if avail > maxContentWidth {
		return maxContentWidth
	}
	return avail
}

// truncateMessage truncates msg to maxWidth runes, appending "..." if
// truncated.  It operates on runes (not bytes) so that multi-byte UTF-8
// characters are never split.
func truncateMessage(msg string, maxWidth int) string {
	runes := []rune(msg)
	if len(runes) <= maxWidth {
		return msg
	}
	if maxWidth <= 3 {
		return string(runes[:maxWidth])
	}
	return string(runes[:maxWidth-3]) + "..."
}

// agentColor returns the ANSI color code assigned to agentID, assigning one
// on first use by cycling through agentColors. Must be called with r.mu held.
func (r *ConsoleReporter) agentColor(agentID string) string {
	if !r.color {
		return ""
	}
	if c, ok := r.agentColorMap[agentID]; ok {
		return c
	}
	c := agentColors[r.agentCounter%len(agentColors)]
	r.agentColorMap[agentID] = c
	r.agentCounter++
	return c
}

// colorToken wraps token in the agent's assigned color and a reset, or returns
// token unchanged when color is disabled. Must be called with r.mu held.
func (r *ConsoleReporter) colorToken(agentID, token string) string {
	c := r.agentColor(agentID)
	if c == "" {
		return token
	}
	return c + token + colorReset
}

// formatAgentID returns "[agentID]" wrapped in the agent's assigned color when
// color output is enabled. Must be called with r.mu held.
func (r *ConsoleReporter) formatAgentID(agentID string) string {
	c := r.agentColor(agentID)
	if c == "" {
		return "[" + agentID + "]"
	}
	return c + "[" + agentID + "]" + colorReset
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
	if r.color {
		fmt.Fprintf(r.w, "%sUsage limit reached. Waiting %.0f seconds before resuming.%s\n",
			colorWarning, e.WaitSeconds, colorReset)
	} else {
		fmt.Fprintf(r.w, "Usage limit reached. Waiting %.0f seconds before resuming.\n", e.WaitSeconds)
	}
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
	fmt.Fprintf(r.w, "%s %s\n", r.formatAgentID(e.AgentID), e.StateName)
	if e.StateType == events.StateTypeScript && !r.quiet {
		fmt.Fprintf(r.w, "  %s Executing script...\n", r.colorToken(e.AgentID, r.sym.progress))
	}
}

func (r *ConsoleReporter) onStateCompleted(e events.StateCompleted) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stateType := r.lastStateType[e.AgentID]
	if stateType == events.StateTypeScript {
		exitCode := r.lastExitCode[e.AgentID]
		fmt.Fprintf(r.w, "  %s Done (exit %d, %.0fms)\n", r.colorToken(e.AgentID, r.sym.done), exitCode, e.DurationMS)
	} else {
		fmt.Fprintf(r.w, "  %s Done ($%.4f, total: $%.4f)\n",
			r.colorToken(e.AgentID, r.sym.done), e.CostUSD, e.TotalCostUSD)
	}
}

func (r *ConsoleReporter) onProgressMessage(e events.ProgressMessage) {
	if r.quiet {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	msg := truncateMessage(e.Message, r.availableWidth(prefixTreeBranch))
	fmt.Fprintf(r.w, "  %s %s\n", r.colorToken(e.AgentID, r.sym.progress), msg)
}

func (r *ConsoleReporter) onToolInvocation(e events.ToolInvocation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastTool[e.AgentID] = e.ToolName
	if r.quiet {
		return
	}
	if e.Detail != "" {
		// Prefix: "  ├─ [ToolName] " = 8 + len(tool_name)
		prefixLen := 8 + len(e.ToolName)
		detail := truncateMessage(e.Detail, r.availableWidth(prefixLen))
		fmt.Fprintf(r.w, "  %s [%s] %s\n", r.colorToken(e.AgentID, r.sym.progress), e.ToolName, detail)
	} else {
		fmt.Fprintf(r.w, "  %s [%s]\n", r.colorToken(e.AgentID, r.sym.progress), e.ToolName)
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
			fmt.Fprintf(r.w, "  %s (%s) %s %s\n", r.colorToken(e.AgentID, "return"), snippet, r.sym.arrow, e.ToState)
		} else {
			fmt.Fprintf(r.w, "  %s %s %s\n", r.colorToken(e.AgentID, "return"), r.sym.arrow, e.ToState)
		}

	default:
		// goto, reset, call, function — prefix with transition type.
		fmt.Fprintf(r.w, "  %s %s %s\n", r.colorToken(e.AgentID, e.TransitionType), r.sym.arrow, e.ToState)
	}
}

func (r *ConsoleReporter) onAgentSpawned(e events.AgentSpawned) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// ⑂ WORKER.md → main_worker1
	fmt.Fprintf(r.w, "  %s %s %s %s\n",
		r.colorToken(e.ParentAgentID, r.sym.fork), e.InitialState, r.sym.forkArrow, e.NewAgentID)
}

func (r *ConsoleReporter) onAgentTerminated(e events.AgentTerminated) {
	r.mu.Lock()
	defer r.mu.Unlock()
	payload := strings.TrimSpace(e.ResultPayload)
	if payload != "" {
		truncated := truncateMessage(payload, r.availableWidth(prefixResult))
		fmt.Fprintf(r.w, "  %s Result: %q\n", r.colorToken(e.AgentID, r.sym.result), truncated)
	} else {
		fmt.Fprintf(r.w, "  %s (terminated)\n", r.colorToken(e.AgentID, r.sym.result))
	}
}

func (r *ConsoleReporter) onErrorOccurred(e events.ErrorOccurred) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Prefix: "  ! " = 4 chars (before color codes, which are zero-width)
	msg := truncateMessage(e.ErrorMessage, r.availableWidth(4))
	if r.color {
		msg = colorError + msg + colorReset
	}
	if e.IsRetryable {
		fmt.Fprintf(r.w, "  %s %s - retrying (%d/%d)\n",
			r.sym.warn, msg, e.RetryCount, e.MaxRetries)
	} else {
		fmt.Fprintf(r.w, "  %s %s\n", r.sym.warn, msg)
	}
}

func (r *ConsoleReporter) onAgentPaused(e events.AgentPaused) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reason := e.Reason
	if r.color {
		reason = colorWarning + reason + colorReset
	}
	fmt.Fprintf(r.w, "%s Agent paused (%s)\n", r.formatAgentID(e.AgentID), reason)
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

// New creates a ConsoleObserver writing to os.Stdout. Unicode symbols and ANSI
// colors are enabled automatically when os.Stdout is a terminal. Colors are
// suppressed when the NO_COLOR environment variable is set.
func New(b *bus.Bus, quiet bool, width int) *ConsoleObserver {
	isTTY := isCharDevice(os.Stdout)
	color := isTTY && os.Getenv("NO_COLOR") == ""
	return NewWithWriter(b, quiet, width, os.Stdout, isTTY, color)
}

// NewWithWriter creates a ConsoleObserver writing to w with explicit unicode
// and color settings. Use this in tests to capture output predictably.
func NewWithWriter(b *bus.Bus, quiet bool, width int, w io.Writer, unicode, color bool) *ConsoleObserver {
	r := newReporter(w, quiet, unicode, color, width)
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
		bus.Subscribe(b, r.onAgentPaused),
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
