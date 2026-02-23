// Package ccwrap wraps the claude CLI for use as a managed subprocess.
//
// Architecture:
//
//   - BuildClaudeCommand constructs the full argument list (testable without
//     running claude).
//   - BuildClaudeEnv copies the process environment and strips CLAUDECODE so
//     that claude does not treat itself as nested inside another session.
//   - InvokeStream is the preferred interface: it launches claude and returns a
//     channel of parsed JSON objects with an idle-timeout that resets on each
//     chunk received.
//   - Invoke is a convenience wrapper that collects all objects and extracts the
//     session ID; it uses a total timeout via context cancellation.
//
// The subprocess is started without a controlling terminal (setsid on Unix) so
// that claude's Node.js/Ink TUI cannot open /dev/tty and emit terminal control
// sequences that would interfere with raymond's console output.
package ccwrap

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
	"strings"
	"time"
)

const (
	// DefaultTimeout is the default idle/total timeout for Claude Code
	// invocations (10 minutes).
	DefaultTimeout = 600.0
)

// DisallowedTools lists tools that raymond-managed agents must never use;
// they are orchestrator-level concerns and must not be delegated to agents.
var DisallowedTools = []string{
	"EnterPlanMode", "ExitPlanMode", "AskUserQuestion", "NotebookEdit",
}

// claudeExe is the claude CLI executable name or path.
// Override in tests via overrideClaudeExe.
var claudeExe = "claude"

// ClaudeCodeTimeoutError is returned when a Claude Code invocation times out.
type ClaudeCodeTimeoutError struct {
	Timeout float64
	Idle    bool // true = idle timeout; false = total timeout
}

func (e *ClaudeCodeTimeoutError) Error() string {
	if e.Idle {
		return fmt.Sprintf(
			"Claude Code idle timeout: no data received for %.6g seconds", e.Timeout)
	}
	return fmt.Sprintf(
		"Claude Code invocation timed out after %.6g seconds", e.Timeout)
}

// BuildClaudeEnv returns a copy of the current process environment with the
// CLAUDECODE key removed so that claude does not treat itself as nested inside
// another session (raymond intentionally launches claude as a managed
// subprocess, not as a nested session).
func BuildClaudeEnv() map[string]string {
	env := make(map[string]string)
	for _, kv := range os.Environ() {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			k := kv[:idx]
			if k == "CLAUDECODE" {
				continue
			}
			env[k] = kv[idx+1:]
		}
	}
	return env
}

// BuildClaudeCommand constructs the full claude CLI argument slice.
//
//   - model == ""     → omit --model
//   - effort == ""    → omit --effort
//   - sessionID == "" → omit --resume
//   - fork == true    → append --fork-session after the prompt separator
func BuildClaudeCommand(
	prompt, model, effort, sessionID string,
	dangerouslySkipPermissions, fork bool,
) []string {
	cmd := []string{
		claudeExe,
		"-p", // headless/print mode
		"--output-format", "stream-json",
		"--verbose",
	}

	if dangerouslySkipPermissions {
		cmd = append(cmd, "--dangerously-skip-permissions")
	} else {
		cmd = append(cmd, "--permission-mode", "acceptEdits")
	}

	if model != "" {
		cmd = append(cmd, "--model", model)
	}

	if effort != "" {
		cmd = append(cmd, "--effort", effort)
	}

	if sessionID != "" {
		cmd = append(cmd, "--resume", sessionID)
	}

	// Unconditionally prevent orchestrator-level tools from being used by
	// managed agents.
	cmd = append(cmd, "--disallowed-tools", strings.Join(DisallowedTools, ","))

	cmd = append(cmd, "--", prompt)

	if fork {
		cmd = append(cmd, "--fork-session")
	}

	return cmd
}

// ExtractSessionID returns the session ID embedded in a parsed JSON object.
// It checks the top-level "session_id" key first, then "metadata.session_id".
// Returns "" if no session ID is found.
func ExtractSessionID(obj map[string]any) string {
	if sid, ok := obj["session_id"].(string); ok && sid != "" {
		return sid
	}
	if meta, ok := obj["metadata"].(map[string]any); ok {
		if sid, ok := meta["session_id"].(string); ok && sid != "" {
			return sid
		}
	}
	return ""
}

// StreamItem holds a single parsed JSON object from the Claude Code stream,
// or an error if parsing or process execution failed.
type StreamItem struct {
	Object map[string]any
	Err    error
}

// InvokeStream launches a Claude Code subprocess and returns a channel that
// yields parsed JSON objects (StreamItem.Object) as they arrive on stdout.
//
//   - idleTimeout <= 0: no idle timeout
//   - idleTimeout > 0: sends ClaudeCodeTimeoutError if no data arrives for
//     that many seconds; the timer resets each time data is received
//   - cwd == "":        child inherits the caller's working directory
//
// The channel is always closed when the subprocess exits (or on error). The
// caller must drain the channel (or cancel ctx) to avoid goroutine leaks. A
// non-nil StreamItem.Err in the last item indicates subprocess failure or
// timeout.
func InvokeStream(
	ctx context.Context,
	prompt, model, effort, sessionID string,
	idleTimeout float64,
	dangerouslySkipPermissions, fork bool,
	cwd string,
) <-chan StreamItem {
	ch := make(chan StreamItem, 64)
	go func() {
		defer close(ch)
		err := runStream(ctx, ch, prompt, model, effort, sessionID, idleTimeout,
			dangerouslySkipPermissions, fork, cwd)
		if err != nil {
			select {
			case ch <- StreamItem{Err: err}:
			case <-ctx.Done():
				// Caller already cancelled; don't block sending the error.
			}
		}
	}()
	return ch
}

// lineMsg is an internal message from the scanner goroutine.
type lineMsg struct {
	line string
	err  error
}

func runStream(
	ctx context.Context,
	ch chan<- StreamItem,
	prompt, model, effort, sessionID string,
	idleTimeout float64,
	dangerouslySkipPermissions, fork bool,
	cwd string,
) error {
	cmdSlice := BuildClaudeCommand(prompt, model, effort, sessionID, dangerouslySkipPermissions, fork)
	execCmd := exec.Command(cmdSlice[0], cmdSlice[1:]...)
	execCmd.Stdin = nil // → /dev/null per exec.Cmd docs

	envMap := BuildClaudeEnv()
	envSlice := make([]string, 0, len(envMap))
	for k, v := range envMap {
		envSlice = append(envSlice, k+"="+v)
	}
	execCmd.Env = envSlice

	if cwd != "" {
		execCmd.Dir = cwd
	}

	// Platform-specific: detach from controlling terminal.
	setupClaudeCmd(execCmd)

	stdoutPipe, err := execCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	execCmd.Stderr = &stderrBuf

	if err := execCmd.Start(); err != nil {
		return fmt.Errorf("failed to start claude: %w", err)
	}

	// Scanner goroutine — reads stdout line by line and sends to lineCh.
	// It closes lineCh when done, signalling the main loop to call Wait().
	lineCh := make(chan lineMsg, 32)
	go func() {
		defer close(lineCh)
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1 MB max line length
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

	// killAndDrain kills the entire process group (so orphaned child processes
	// cannot hold the stdout/stderr pipes open), waits for process exit, then
	// drains lineCh so the scanner goroutine can exit cleanly.
	killAndDrain := func() {
		_ = killClaudeProcess(execCmd)
		_ = execCmd.Wait()
		for range lineCh {}
	}

	for {
		select {
		case <-ctx.Done():
			killAndDrain()
			return ctx.Err()

		case <-idleExpired:
			// Prefer context cancellation over idle timeout when both fire
			// simultaneously (e.g. total timeout deadline expired at the same
			// moment the idle timer fired).
			if ctx.Err() != nil {
				killAndDrain()
				return ctx.Err()
			}
			killAndDrain()
			return &ClaudeCodeTimeoutError{Timeout: idleTimeout, Idle: true}

		case msg, ok := <-lineCh:
			if !ok {
				// Scanner goroutine closed lineCh → stdout EOF; process exited.
				if waitErr := execCmd.Wait(); waitErr != nil {
					if exitErr, ok2 := waitErr.(*exec.ExitError); ok2 {
						return fmt.Errorf(
							"claude command failed with return code %d\nStderr: %s",
							exitErr.ExitCode(), stderrBuf.String())
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return fmt.Errorf("claude Wait: %w", waitErr)
				}
				return nil
			}

			if msg.err != nil {
				killAndDrain()
				return fmt.Errorf("reading claude output: %w", msg.err)
			}

			resetIdle()

			line := strings.TrimSpace(msg.line)
			if line == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				// Skip non-JSON lines (mirrors Python's warning + continue).
				log.Printf("ccwrap: skipping non-JSON line from claude: %q", line)
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

// Invoke runs Claude Code non-streamingly, collecting all JSON objects and
// returning them along with the extracted session ID.
//
//   - totalTimeout <= 0: no timeout (relies on any deadline in ctx)
//   - totalTimeout > 0: ClaudeCodeTimeoutError if total duration exceeded
func Invoke(
	ctx context.Context,
	prompt, model, effort, sessionID string,
	totalTimeout float64,
	dangerouslySkipPermissions, fork bool,
	cwd string,
) ([]map[string]any, string, error) {
	if totalTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(
			ctx, time.Duration(float64(time.Second)*totalTimeout))
		defer cancel()
	}

	ch := InvokeStream(ctx, prompt, model, effort, sessionID, 0,
		dangerouslySkipPermissions, fork, cwd)

	var results []map[string]any
	var extractedSID string
	for item := range ch {
		if item.Err != nil {
			if errors.Is(item.Err, context.DeadlineExceeded) {
				return nil, "", &ClaudeCodeTimeoutError{Timeout: totalTimeout, Idle: false}
			}
			return nil, "", item.Err
		}
		results = append(results, item.Object)
		if sid := ExtractSessionID(item.Object); sid != "" {
			extractedSID = sid
		}
	}

	// Safety check: handle timeout if the error was dropped by InvokeStream's
	// select (both ctx.Done() and ch send were simultaneously ready).
	if totalTimeout > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, "", &ClaudeCodeTimeoutError{Timeout: totalTimeout, Idle: false}
	}

	return results, extractedSID, nil
}
