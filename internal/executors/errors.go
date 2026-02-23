// Package executors provides state executor types and supporting infrastructure
// for running workflow states (markdown and script).
package executors

// ClaudeCodeError is returned when Claude Code execution fails.
type ClaudeCodeError struct{ Msg string }

func (e *ClaudeCodeError) Error() string { return e.Msg }

// ClaudeCodeLimitError is returned when Claude Code hits its usage limit.
// This is non-retryable; the agent should be paused and resumed later.
type ClaudeCodeLimitError struct{ Msg string }

func (e *ClaudeCodeLimitError) Error() string { return e.Msg }

// ClaudeCodeTimeoutWrappedError is returned when Claude Code times out.
// Allows pause/resume — the session_id is preserved for continuation.
type ClaudeCodeTimeoutWrappedError struct{ Msg string }

func (e *ClaudeCodeTimeoutWrappedError) Error() string { return e.Msg }

// PromptFileError is returned when prompt file operations fail.
type PromptFileError struct{ Msg string }

func (e *PromptFileError) Error() string { return e.Msg }

// ScriptError is returned when script execution fails (non-zero exit, timeout,
// missing file, or invalid transition output).
type ScriptError struct{ Msg string }

func (e *ScriptError) Error() string { return e.Msg }
