// Package piwrap wraps the pi CLI for use as a managed subprocess.
//
// Architecture:
//
//   - BuildPiCommand constructs the full argument list (testable without
//     running pi). It handles session flags, model/provider, thinking level,
//     tool allowlist, extensions, skills, and the prompt as the final
//     positional argument.
//   - InvokeStream is the streaming interface: it launches pi and returns a
//     channel of parsed JSON objects with an idle-timeout that resets on each
//     line received. Matches the InvokeStream pattern from ccwrap.
//   - ReadSessionCost reads the pi session JSONL file after a turn completes
//     and sums usage records to derive cost and input token counts.
//
// Environment: piwrap passes the parent environment unchanged. Unlike ccwrap,
// there is no CLAUDECODE nesting marker to strip.
//
// Process isolation: pi is Node.js-based, same as claude. Raymond isolates it
// from the controlling terminal via setsid (Unix) / CREATE_NO_WINDOW (Windows)
// so that pi's TUI cannot emit terminal control sequences.
package piwrap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// defaultTools is the conservative tool allowlist used when
// dangerously_skip_permissions is false and no explicit tools list is declared.
var defaultTools = []string{"read", "edit", "write", "grep", "find", "ls"}

// piExe is the pi CLI executable name or path.
// Override in tests via SetPiExeForTest.
var piExe = "pi"

// SetPiExeForTest overrides the pi binary name used by piwrap. The returned
// restore function reinstates the previous value and should be called from
// t.Cleanup or a defer. This allows tests to run without a real pi installation.
func SetPiExeForTest(name string) (restore func()) {
	orig := piExe
	piExe = name
	return func() { piExe = orig }
}

// CommandSpec describes all inputs needed to construct a pi CLI invocation.
type CommandSpec struct {
	// Per-turn fields.
	Prompt    string
	Model     string // --model; empty = omit
	Effort    string // per-state effort: translated to --thinking; empty = use ThinkingDefault
	SessionID string // empty = new session; non-empty with Fork=false = --session
	Fork      bool   // true: --fork <SessionID> instead of --session

	// Workflow-level backend options baked in at backend construction time.
	Provider        string   // --provider; empty = omit
	ThinkingDefault string   // fallback --thinking when Effort is empty; empty = omit
	Tools           []string // explicit --tools allowlist; nil = derive from defaults
	NoBuiltinTools  bool     // --no-builtin-tools
	NoTools         bool     // --no-tools
	NoExtensions    bool     // --no-extensions
	NoSkills        bool     // --no-skills
	Extensions      []string // --extension (repeatable)
	Skills          []string // --skill (repeatable)
	SessionDir      string   // --session-dir; empty = omit

	DangerouslySkipPermissions bool
}

// BuildPiCommand constructs the full pi CLI argument slice for one turn.
// The returned slice is suitable for exec.Command(args[0], args[1:]...).
//
// Tool selection logic (in priority order):
//  1. no_tools: --no-tools
//  2. no_builtin_tools: --no-builtin-tools
//  3. explicit tools list: --tools <comma-list>
//  4. dangerously_skip_permissions=true, no list: no tools flag (pi default surface)
//  5. default: --tools read,edit,write,grep,find,ls (conservative built-in set)
//
// Thinking level: per-state Effort takes precedence over ThinkingDefault.
// Low/medium/high map 1:1; other values pass verbatim to pi.
func BuildPiCommand(spec CommandSpec) []string {
	cmd := []string{piExe, "--mode", "json"}

	if spec.SessionDir != "" {
		cmd = append(cmd, "--session-dir", spec.SessionDir)
	}

	// Session continuation flags.
	if spec.Fork && spec.SessionID != "" {
		cmd = append(cmd, "--fork", spec.SessionID)
	} else if !spec.Fork && spec.SessionID != "" {
		cmd = append(cmd, "--session", spec.SessionID)
	}

	if spec.Model != "" {
		cmd = append(cmd, "--model", spec.Model)
	}
	if spec.Provider != "" {
		cmd = append(cmd, "--provider", spec.Provider)
	}

	// Thinking level: per-state effort beats workflow-level default.
	thinking := spec.ThinkingDefault
	if spec.Effort != "" {
		thinking = spec.Effort
	}
	if thinking != "" {
		cmd = append(cmd, "--thinking", thinking)
	}

	// Tool flags.
	switch {
	case spec.NoTools:
		cmd = append(cmd, "--no-tools")
	case spec.NoBuiltinTools:
		cmd = append(cmd, "--no-builtin-tools")
	case len(spec.Tools) > 0:
		cmd = append(cmd, "--tools", strings.Join(spec.Tools, ","))
	case spec.DangerouslySkipPermissions:
		// No tools flag: pi uses its default surface.
	default:
		cmd = append(cmd, "--tools", strings.Join(defaultTools, ","))
	}

	if spec.NoExtensions {
		cmd = append(cmd, "--no-extensions")
	}
	for _, ext := range spec.Extensions {
		cmd = append(cmd, "--extension", ext)
	}
	if spec.NoSkills {
		cmd = append(cmd, "--no-skills")
	}
	for _, skill := range spec.Skills {
		cmd = append(cmd, "--skill", skill)
	}

	// Prompt is the final positional argument, passed as a raw argv element.
	cmd = append(cmd, spec.Prompt)

	return cmd
}

// StreamItem holds a single parsed JSON object from the pi stream, or an
// error if parsing or process execution failed.
type StreamItem struct {
	Object map[string]any
	Err    error
}

// TimeoutError is returned when a pi invocation hits its idle timeout.
type TimeoutError struct {
	Timeout float64
	Idle    bool
}

func (e *TimeoutError) Error() string {
	if e.Idle {
		return fmt.Sprintf("pi idle timeout: no data received for %.6g seconds", e.Timeout)
	}
	return fmt.Sprintf("pi invocation timed out after %.6g seconds", e.Timeout)
}

// invokeStreamFn is the actual launch function used by InvokeStream.
// Overridable in tests via SetInvokeStreamFnForTest.
var invokeStreamFn = defaultInvokeStream

// SetInvokeStreamFnForTest overrides the stream launcher. The returned restore
// function reinstates the previous value.
func SetInvokeStreamFnForTest(
	fn func(ctx context.Context, spec CommandSpec, cwd string, idleTimeout float64) <-chan StreamItem,
) (restore func()) {
	orig := invokeStreamFn
	invokeStreamFn = fn
	return func() { invokeStreamFn = orig }
}

// InvokeStream launches a pi subprocess and returns a channel that yields
// parsed JSON objects (StreamItem.Object) as they arrive on stdout.
//
//   - idleTimeout <= 0: no idle timeout
//   - idleTimeout > 0: sends TimeoutError if no data arrives for that many
//     seconds; the timer resets each time data is received
//   - cwd == "": child inherits the caller's working directory
//
// The channel is always closed when the subprocess exits. A non-nil
// StreamItem.Err in the last item indicates failure or timeout.
func InvokeStream(ctx context.Context, spec CommandSpec, cwd string, idleTimeout float64) <-chan StreamItem {
	return invokeStreamFn(ctx, spec, cwd, idleTimeout)
}

// lineMsg is an internal message from the scanner goroutine.
type lineMsg struct {
	line string
	err  error
}

func defaultInvokeStream(ctx context.Context, spec CommandSpec, cwd string, idleTimeout float64) <-chan StreamItem {
	ch := make(chan StreamItem, 64)
	go func() {
		defer close(ch)
		err := runStream(ctx, ch, spec, cwd, idleTimeout)
		if err != nil {
			select {
			case ch <- StreamItem{Err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return ch
}

func runStream(
	ctx context.Context,
	ch chan<- StreamItem,
	spec CommandSpec,
	cwd string,
	idleTimeout float64,
) error {
	cmdSlice := BuildPiCommand(spec)
	execCmd := exec.Command(cmdSlice[0], cmdSlice[1:]...)
	execCmd.Stdin = nil

	// Pass the parent environment unchanged — pi has no CLAUDECODE marker to strip.
	execCmd.Env = os.Environ()

	if cwd != "" {
		execCmd.Dir = cwd
	}

	// Platform-specific: detach from controlling terminal.
	setupPiCmd(execCmd)

	stdoutPipe, err := execCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	execCmd.Stderr = &stderrBuf

	if err := execCmd.Start(); err != nil {
		return fmt.Errorf("failed to start pi: %w", err)
	}

	// Scanner goroutine.
	lineCh := make(chan lineMsg, 32)
	go func() {
		defer close(lineCh)
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			lineCh <- lineMsg{line: scanner.Text()}
		}
		if err := scanner.Err(); err != nil {
			lineCh <- lineMsg{err: err}
		}
	}()

	// Idle timeout setup.
	var idleTimer *time.Timer
	var idleExpired <-chan time.Time
	resetIdle := func() {}
	if idleTimeout > 0 {
		d := time.Duration(float64(time.Second) * idleTimeout)
		idleTimer = time.NewTimer(d)
		idleExpired = idleTimer.C
		resetIdle = func() {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(d)
		}
		defer idleTimer.Stop()
	}

	killAndDrain := func() {
		_ = killPiProcess(execCmd)
		_ = execCmd.Wait()
		for range lineCh {
		}
	}

	for {
		select {
		case <-ctx.Done():
			killAndDrain()
			return ctx.Err()

		case <-idleExpired:
			if ctx.Err() != nil {
				killAndDrain()
				return ctx.Err()
			}
			killAndDrain()
			return &TimeoutError{Timeout: idleTimeout, Idle: true}

		case msg, ok := <-lineCh:
			if !ok {
				if waitErr := execCmd.Wait(); waitErr != nil {
					if exitErr, ok2 := waitErr.(*exec.ExitError); ok2 {
						return fmt.Errorf(
							"pi command failed with return code %d\nStderr: %s",
							exitErr.ExitCode(), stderrBuf.String())
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return fmt.Errorf("pi Wait: %w", waitErr)
				}
				return nil
			}

			if msg.err != nil {
				killAndDrain()
				return fmt.Errorf("reading pi output: %w", msg.err)
			}

			resetIdle()

			line := strings.TrimSpace(msg.line)
			if line == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				log.Printf("piwrap: skipping non-JSON line from pi: %q", line)
				continue
			}

			select {
			case ch <- StreamItem{Object: obj}:
			case <-ctx.Done():
				killAndDrain()
				return ctx.Err()
			}
		}
	}
}

// SessionCost holds the cost and token counts extracted from a pi session file.
type SessionCost struct {
	CostUSD     float64
	InputTokens int64
}

// ReadSessionCost locates and parses the pi session JSONL file after a turn,
// summing per-assistant-message usage records to derive cost and input
// token counts. See parseSessionCost for the record shape.
//
// Directory resolution:
//   - If sessionDir is non-empty: <sessionDir>/
//   - Otherwise: ~/.pi/agent/sessions/<mangled-cwd>/
//
// Within the directory the session file is named
// "<ISO-timestamp>_<sessionID>.jsonl"; we glob for the suffix because the
// timestamp prefix is unknown to raymond.
//
// Returns zero-value SessionCost (not an error) when the session file is not
// found, since pi may not have written it yet for a fresh session or when the
// turn produced no usage records.
func ReadSessionCost(sessionID, sessionDir, cwd string) (SessionCost, error) {
	dir, err := sessionDirPath(sessionDir, cwd)
	if err != nil {
		return SessionCost{}, err
	}

	path, err := findSessionFile(dir, sessionID)
	if err != nil {
		return SessionCost{}, err
	}
	if path == "" {
		return SessionCost{}, nil // no file yet — caller treats as zero cost
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SessionCost{}, nil
		}
		return SessionCost{}, fmt.Errorf("reading pi session file %s: %w", path, err)
	}

	return parseSessionCost(data), nil
}

// sessionDirPath returns the directory pi stores this session in (no filename).
// When cwd is empty, pi inherits the parent process's cwd, so we resolve via
// os.Getwd to match — otherwise the lookup would mangle "" to "----" and miss.
func sessionDirPath(sessionDir, cwd string) (string, error) {
	if sessionDir != "" {
		return sessionDir, nil
	}
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolving working directory: %w", err)
		}
		cwd = wd
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".pi", "agent", "sessions", piCwdDirName(cwd)), nil
}

// findSessionFile locates the per-session JSONL file inside dir. Pi names
// session files "<ISO-timestamp>_<sessionID>.jsonl"; we match by suffix.
// Returns ("", nil) if the directory exists but no file matches yet, or if
// the directory itself doesn't exist.
func findSessionFile(dir, sessionID string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("reading pi session dir %s: %w", dir, err)
	}
	suffix := "_" + sessionID + ".jsonl"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, suffix) || name == sessionID+".jsonl" {
			return filepath.Join(dir, name), nil
		}
	}
	return "", nil
}

// piCwdDirName mirrors pi's cwd-to-dirname mangling: strip leading/trailing
// path separators, replace remaining separators with `-`, and wrap in `--`.
// e.g. "/home/x/y" -> "--home-x-y--".
//
// Verified empirically against pi v0.74 on Linux (see workflows/pi_smoke).
// On Windows we first normalize backslashes to forward slashes via
// filepath.ToSlash, but pi's actual Windows mangling is unverified — the
// drive-letter colon (`C:`) in particular is not handled here. On Windows
// the cost-reading path will likely miss the session file and silently
// report $0 per turn (non-fatal; the stream parser still drives the
// workflow). Verify and tighten when a Windows user reports it.
func piCwdDirName(cwd string) string {
	normalized := filepath.ToSlash(cwd)
	trimmed := strings.Trim(normalized, "/")
	return "--" + strings.ReplaceAll(trimmed, "/", "-") + "--"
}

// parseSessionCost sums per-assistant-message usage records from pi session
// JSONL content.
//
// The on-disk session file shape differs from pi's live event stream: each
// finalized assistant message is persisted as one
// `{"type":"message","message":{"role":"assistant","usage":{...}}}` record.
// We sum these (one per assistant turn) to derive total session cost and
// input-side token count.
//
// pi v0.74 usage shape (inside message.usage):
//
//	"usage": {
//	  "input": <int>,
//	  "output": <int>,
//	  "cacheRead": <int>,
//	  "cacheWrite": <int>,
//	  "totalTokens": <int>,
//	  "cost": {"input": <float>, "output": <float>,
//	           "cacheRead": <float>, "cacheWrite": <float>, "total": <float>}
//	}
//
// Input-side tokens mirror Claude's "input_tokens" semantics (excluding the
// model's output): sum of input + cacheRead + cacheWrite.
//
// `turn_end` is recognized too — that's the live-stream-only event shape,
// and being permissive lets the same parser handle a captured stream.
func parseSessionCost(data []byte) SessionCost {
	var result SessionCost
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			continue
		}
		if !isAssistantUsageRecord(obj) {
			continue
		}
		usage := findUsage(obj)
		if usage == nil {
			continue
		}
		result.CostUSD += readCostTotal(usage)
		result.InputTokens += readInputSideTokens(usage)
	}
	return result
}

// isAssistantUsageRecord returns true for record shapes that carry final
// per-turn usage in pi sessions: a persisted assistant `message` record, or
// a live-stream `turn_end`. Anything else (user messages, model_change,
// streaming message_update snapshots, …) is filtered out to avoid
// over-counting.
func isAssistantUsageRecord(obj map[string]any) bool {
	t, _ := obj["type"].(string)
	switch t {
	case "turn_end":
		return true
	case "message":
		if msg, ok := obj["message"].(map[string]any); ok {
			role, _ := msg["role"].(string)
			return role == "assistant"
		}
	}
	return false
}

// findUsage returns the "usage" object directly on the record, or on the
// nested "message" field (pi puts usage inside message on message_start /
// message_end / turn_end).
func findUsage(obj map[string]any) map[string]any {
	if u, ok := obj["usage"].(map[string]any); ok {
		return u
	}
	if msg, ok := obj["message"].(map[string]any); ok {
		if u, ok := msg["usage"].(map[string]any); ok {
			return u
		}
	}
	return nil
}

func readCostTotal(usage map[string]any) float64 {
	cost, ok := usage["cost"].(map[string]any)
	if !ok {
		return 0
	}
	total, _ := cost["total"].(float64)
	return total
}

func readInputSideTokens(usage map[string]any) int64 {
	var sum int64
	for _, key := range []string{"input", "cacheRead", "cacheWrite"} {
		switch v := usage[key].(type) {
		case float64:
			sum += int64(v)
		case int64:
			sum += v
		}
	}
	return sum
}
