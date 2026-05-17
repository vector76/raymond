// Package backend provides a thin abstraction over agent-CLI backends.
//
// A Backend runs a single agent "turn": one invocation that streams
// normalized events to a Sink and returns a TurnResult containing the
// assistant output text (the input to transition parsing), an updated
// session id, accumulated cost, and token counts.
//
// Today only the Claude Code backend is implemented (claude.go). The
// interface is shaped so adding a pi backend (see docs/pi-backend.md)
// is purely additive: orchestrator, executor, and observers are
// unaffected. Errors returned by Backend.RunTurn are also backend-
// neutral (TimeoutError / LimitError / RunError); the executor maps
// them onto its orchestrator-facing error types.
package backend

import (
	"context"
	"fmt"
)

// TurnSpec describes a single agent-turn invocation.
type TurnSpec struct {
	// Prompt is the rendered prompt body. Backends MUST deliver it as a
	// single positional argv element so no shell interprets it.
	Prompt string

	// Model and Effort are backend-specific identifiers. Empty means "omit".
	Model  string
	Effort string

	// SessionID resumes an existing session when non-empty; empty starts
	// a fresh session.
	SessionID string

	// Fork branches a new session off SessionID (used to implement
	// <call>-style stack frames that inherit caller context). For the
	// Claude backend this maps to --fork-session; for pi to --fork.
	Fork bool

	// ContinueLatest picks up the user's most-recent session in the agent's
	// working directory. Claude maps this to `-c --fork-session`; the pi
	// backend resolves the most-recently-modified file in pi's session dir
	// for this cwd and forks from it (see docs/pi-backend.md).
	ContinueLatest bool

	// Cwd is the agent's working directory; empty inherits the parent's.
	Cwd string

	// IdleTimeout is the per-turn idle timeout in seconds. Values <=0
	// disable the timeout.
	IdleTimeout float64

	// DangerouslySkipPermissions bypasses per-call permission prompts on
	// backends that have them (Claude). Backends without that mechanism
	// (pi) translate it per their own contract.
	DangerouslySkipPermissions bool
}

// TurnResult is the outcome of a completed turn.
type TurnResult struct {
	// OutputText is the assembled assistant text emitted during the turn;
	// it is the input to transition-tag parsing.
	OutputText string

	// SessionID is the (possibly new) session id reported by the backend.
	// Empty when the backend did not report one this turn.
	SessionID string

	// CostUSD is the cost accumulated during this single turn.
	CostUSD float64

	// InputTokens is the prompt-token count for this turn, or nil when
	// the backend did not report usage.
	InputTokens *int64
}

// Sink receives normalized events during a turn. Any nil function is
// silently skipped; implementations MUST nil-check before calling.
type Sink struct {
	// OnProgress reports a one-line assistant message — typically the
	// first line of an assistant text block.
	OnProgress func(text string)

	// OnToolUse reports a tool invocation. detail is a short tool-specific
	// identifier (file path for Read/Write/Edit, command for Bash, etc.).
	OnToolUse func(name, detail string)

	// OnToolError reports a tool result whose is_error flag is set.
	OnToolError func(message string)

	// OnRaw delivers the raw protocol object from the backend, intended
	// for the debug-JSONL observer. Callers leave this nil when debug
	// logging is disabled.
	OnRaw func(obj map[string]any)
}

// Backend runs one agent turn.
//
// RunTurn returns when the underlying process exits (or fails). It maps
// transport-level errors to one of *TimeoutError, *LimitError, or
// *RunError so callers can react in a backend-neutral way.
type Backend interface {
	RunTurn(ctx context.Context, spec TurnSpec, sink Sink) (TurnResult, error)
}

// TimeoutError is returned when a turn hits its idle (or total) timeout.
type TimeoutError struct {
	Timeout float64
	Idle    bool // true = idle timeout; false = total timeout
}

func (e *TimeoutError) Error() string {
	if e.Idle {
		return fmt.Sprintf("agent idle timeout: no data received for %.6g seconds", e.Timeout)
	}
	return fmt.Sprintf("agent invocation timed out after %.6g seconds", e.Timeout)
}

// LimitError is returned when the provider rejects a turn due to a usage
// limit. Only the Claude backend produces this today; other backends
// surface usage limits as a generic RunError.
type LimitError struct {
	Msg string
}

func (e *LimitError) Error() string { return e.Msg }

// RunError wraps any other backend execution failure (non-zero exit,
// parse error, etc.). Msg is the user-facing message.
type RunError struct {
	Msg string
}

func (e *RunError) Error() string { return e.Msg }
