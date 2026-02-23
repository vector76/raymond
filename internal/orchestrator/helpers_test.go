package orchestrator

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vector76/raymond/internal/executors"
	wfstate "github.com/vector76/raymond/internal/state"
)

// ---------------------------------------------------------------------------
// allPaused
// ---------------------------------------------------------------------------

func TestAllPaused_Empty(t *testing.T) {
	assert.False(t, allPaused(nil), "empty slice should return false")
	assert.False(t, allPaused([]wfstate.AgentState{}), "empty slice should return false")
}

func TestAllPaused_AllPaused(t *testing.T) {
	agents := []wfstate.AgentState{
		{ID: "a", Status: wfstate.AgentStatusPaused},
		{ID: "b", Status: wfstate.AgentStatusPaused},
	}
	assert.True(t, allPaused(agents))
}

func TestAllPaused_NoneActive(t *testing.T) {
	agents := []wfstate.AgentState{
		{ID: "a", Status: ""}, // running
	}
	assert.False(t, allPaused(agents))
}

func TestAllPaused_Mixed(t *testing.T) {
	agents := []wfstate.AgentState{
		{ID: "a", Status: wfstate.AgentStatusPaused},
		{ID: "b", Status: ""}, // running
	}
	assert.False(t, allPaused(agents))
}

func TestAllPaused_Single(t *testing.T) {
	assert.True(t, allPaused([]wfstate.AgentState{{ID: "a", Status: wfstate.AgentStatusPaused}}))
	assert.False(t, allPaused([]wfstate.AgentState{{ID: "a", Status: ""}}))
}

// ---------------------------------------------------------------------------
// firstActiveIndex
// ---------------------------------------------------------------------------

func TestFirstActiveIndex_AllPaused(t *testing.T) {
	agents := []wfstate.AgentState{
		{ID: "a", Status: wfstate.AgentStatusPaused},
		{ID: "b", Status: wfstate.AgentStatusPaused},
	}
	assert.Equal(t, -1, firstActiveIndex(agents))
}

func TestFirstActiveIndex_FirstIsActive(t *testing.T) {
	agents := []wfstate.AgentState{
		{ID: "a", Status: ""},
		{ID: "b", Status: wfstate.AgentStatusPaused},
	}
	assert.Equal(t, 0, firstActiveIndex(agents))
}

func TestFirstActiveIndex_SecondIsActive(t *testing.T) {
	agents := []wfstate.AgentState{
		{ID: "a", Status: wfstate.AgentStatusPaused},
		{ID: "b", Status: ""},
	}
	assert.Equal(t, 1, firstActiveIndex(agents))
}

func TestFirstActiveIndex_Empty(t *testing.T) {
	assert.Equal(t, -1, firstActiveIndex(nil))
	assert.Equal(t, -1, firstActiveIndex([]wfstate.AgentState{}))
}

// ---------------------------------------------------------------------------
// isLimitError
// ---------------------------------------------------------------------------

func TestIsLimitError_WithLimitError(t *testing.T) {
	err := &executors.ClaudeCodeLimitError{Msg: "limit"}
	assert.True(t, isLimitError(err))
}

func TestIsLimitError_WithOtherError(t *testing.T) {
	assert.False(t, isLimitError(errors.New("generic")))
	assert.False(t, isLimitError(&executors.ClaudeCodeError{Msg: "claude"}))
	assert.False(t, isLimitError(&executors.ScriptError{Msg: "script"}))
}

func TestIsLimitError_WithWrappedLimitError(t *testing.T) {
	wrapped := fmt.Errorf("wrapped: %w", &executors.ClaudeCodeLimitError{Msg: "limit"})
	assert.True(t, isLimitError(wrapped))
}

func TestIsLimitError_Nil(t *testing.T) {
	assert.False(t, isLimitError(nil))
}

// ---------------------------------------------------------------------------
// isTimeoutError
// ---------------------------------------------------------------------------

func TestIsTimeoutError_WithTimeoutError(t *testing.T) {
	err := &executors.ClaudeCodeTimeoutWrappedError{Msg: "timeout"}
	assert.True(t, isTimeoutError(err))
}

func TestIsTimeoutError_WithOtherError(t *testing.T) {
	assert.False(t, isTimeoutError(errors.New("generic")))
	assert.False(t, isTimeoutError(&executors.ClaudeCodeLimitError{Msg: "limit"}))
}

func TestIsTimeoutError_Nil(t *testing.T) {
	assert.False(t, isTimeoutError(nil))
}

// ---------------------------------------------------------------------------
// isRetryableError
// ---------------------------------------------------------------------------

func TestIsRetryableError_ClaudeCodeError(t *testing.T) {
	assert.True(t, isRetryableError(&executors.ClaudeCodeError{Msg: "claude"}))
}

func TestIsRetryableError_TimeoutError(t *testing.T) {
	assert.True(t, isRetryableError(&executors.ClaudeCodeTimeoutWrappedError{Msg: "timeout"}))
}

func TestIsRetryableError_PromptFileError(t *testing.T) {
	assert.True(t, isRetryableError(&executors.PromptFileError{Msg: "prompt"}))
}

func TestIsRetryableError_ScriptErrorIsNotRetryable(t *testing.T) {
	assert.False(t, isRetryableError(&executors.ScriptError{Msg: "script"}))
}

func TestIsRetryableError_LimitErrorIsNotRetryable(t *testing.T) {
	// LimitError causes a pause, not a retry.
	assert.False(t, isRetryableError(&executors.ClaudeCodeLimitError{Msg: "limit"}))
}

func TestIsRetryableError_GenericErrorIsNotRetryable(t *testing.T) {
	assert.False(t, isRetryableError(errors.New("unknown")))
}

func TestIsRetryableError_Nil(t *testing.T) {
	assert.False(t, isRetryableError(nil))
}

// ---------------------------------------------------------------------------
// agentPausedReason
// ---------------------------------------------------------------------------

func TestAgentPausedReason_Timeout(t *testing.T) {
	err := &executors.ClaudeCodeTimeoutWrappedError{Msg: "timed out"}
	assert.Equal(t, "timeout", agentPausedReason(err))
}

func TestAgentPausedReason_PromptError(t *testing.T) {
	err := &executors.PromptFileError{Msg: "missing"}
	assert.Equal(t, "prompt_error", agentPausedReason(err))
}

func TestAgentPausedReason_ClaudeError(t *testing.T) {
	err := &executors.ClaudeCodeError{Msg: "claude error"}
	assert.Equal(t, "claude_error", agentPausedReason(err))
}

func TestAgentPausedReason_GenericErrorFallsBackToClaudeError(t *testing.T) {
	assert.Equal(t, "claude_error", agentPausedReason(errors.New("unknown")))
}
