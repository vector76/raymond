package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/backendcfg"
	"github.com/vector76/raymond/internal/piwrap"
)

// makePiStream returns a function suitable for SetPiInvokeStreamFnForTest that
// sends the given items on a buffered channel and then closes it.
func makePiStream(items ...piwrap.StreamItem) func(context.Context, piwrap.CommandSpec, string, float64) <-chan piwrap.StreamItem {
	return func(_ context.Context, _ piwrap.CommandSpec, _ string, _ float64) <-chan piwrap.StreamItem {
		ch := make(chan piwrap.StreamItem, len(items))
		for _, it := range items {
			ch <- it
		}
		close(ch)
		return ch
	}
}

func TestPiBackend_SessionIDFromAgentStart(t *testing.T) {
	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{"type": "agent_start", "sessionId": "uuid-abc-123"}},
		piwrap.StreamItem{Object: map[string]any{"type": "agent_end", "text": "<goto>done</goto>"}},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	result, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, Sink{})
	require.NoError(t, err)
	assert.Equal(t, "uuid-abc-123", result.SessionID)
}

func TestPiBackend_OutputTextFromAgentEnd(t *testing.T) {
	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{"type": "agent_start", "sessionId": "s1"}},
		piwrap.StreamItem{Object: map[string]any{"type": "agent_end", "text": "<goto>next_state</goto>"}},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	result, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, Sink{})
	require.NoError(t, err)
	assert.Equal(t, "<goto>next_state</goto>", result.OutputText)
}

func TestPiBackend_ToolInvocationFromToolExecutionStart(t *testing.T) {
	var capturedName, capturedDetail string
	sink := Sink{
		OnToolUse: func(name, detail string) {
			capturedName = name
			capturedDetail = detail
		},
	}

	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{
			"type":     "tool_execution_start",
			"toolName": "read",
			"args":     map[string]any{"file_path": "/home/user/project/main.go"},
		}},
		piwrap.StreamItem{Object: map[string]any{"type": "agent_end", "text": "<result>done</result>"}},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, sink)
	require.NoError(t, err)
	assert.Equal(t, "read", capturedName)
	assert.Equal(t, "main.go", capturedDetail)
}

func TestPiBackend_BashToolFromToolExecutionStart(t *testing.T) {
	var capturedName, capturedDetail string
	sink := Sink{
		OnToolUse: func(name, detail string) {
			capturedName = name
			capturedDetail = detail
		},
	}

	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{
			"type":     "tool_execution_start",
			"toolName": "bash",
			"args":     map[string]any{"command": "ls -la /tmp"},
		}},
		piwrap.StreamItem{Object: map[string]any{"type": "agent_end", "text": "<result>done</result>"}},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, sink)
	require.NoError(t, err)
	assert.Equal(t, "bash", capturedName)
	assert.Equal(t, "ls -la /tmp", capturedDetail)
}

func TestPiBackend_ToolErrorFromToolExecutionEnd(t *testing.T) {
	var capturedErr string
	sink := Sink{
		OnToolError: func(msg string) { capturedErr = msg },
	}

	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{
			"type":     "tool_execution_end",
			"toolName": "edit",
			"isError":  true,
			"result":   "file not found",
		}},
		piwrap.StreamItem{Object: map[string]any{"type": "agent_end", "text": "<result>done</result>"}},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, sink)
	require.NoError(t, err)
	assert.Equal(t, "file not found", capturedErr)
}

func TestPiBackend_ToolSuccessDoesNotCallOnToolError(t *testing.T) {
	called := false
	sink := Sink{
		OnToolError: func(string) { called = true },
	}

	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{
			"type":    "tool_execution_end",
			"isError": false,
			"result":  "ok",
		}},
		piwrap.StreamItem{Object: map[string]any{"type": "agent_end", "text": "<result>done</result>"}},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, sink)
	require.NoError(t, err)
	assert.False(t, called)
}

func TestPiBackend_IdleTimeoutMapsToTimeoutError(t *testing.T) {
	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Err: &piwrap.TimeoutError{Timeout: 30, Idle: true}},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, Sink{})
	require.Error(t, err)
	var te *TimeoutError
	require.ErrorAs(t, err, &te)
	assert.True(t, te.Idle)
	assert.Equal(t, 30.0, te.Timeout)
}

func TestPiBackend_ContinueLatestRejected(t *testing.T) {
	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{
		Prompt:         "p",
		ContinueLatest: true,
	}, Sink{})
	require.Error(t, err)
	var re *RunError
	require.ErrorAs(t, err, &re)
	assert.Contains(t, re.Msg, "--continue-and-fork")
}

func TestPiBackend_ZeroCostWhenNoSessionFile(t *testing.T) {
	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{"type": "agent_start", "sessionId": "no-such-session-id"}},
		piwrap.StreamItem{Object: map[string]any{"type": "agent_end", "text": "<result>done</result>"}},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{SessionDir: "/nonexistent/dir"})
	result, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, Sink{})
	require.NoError(t, err)
	assert.Equal(t, 0.0, result.CostUSD)
	assert.Nil(t, result.InputTokens)
}

func TestPiBackend_ProgressFromTextDelta(t *testing.T) {
	var got []string
	sink := Sink{
		OnProgress: func(line string) { got = append(got, line) },
	}

	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{
			"type": "message_update", "updateType": "text_delta", "text": "thinking...",
		}},
		piwrap.StreamItem{Object: map[string]any{
			"type": "message_update", "updateType": "text_delta", "text": "line one\nline two",
		}},
		piwrap.StreamItem{Object: map[string]any{"type": "agent_end", "text": "<result>done</result>"}},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, sink)
	require.NoError(t, err)
	assert.Equal(t, []string{"thinking...", "line one"}, got)
}

func TestPiBackend_ProgressNotFiredForNonTextDelta(t *testing.T) {
	called := false
	sink := Sink{OnProgress: func(string) { called = true }}

	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{
			"type": "message_update", "updateType": "other_kind", "text": "ignored",
		}},
		piwrap.StreamItem{Object: map[string]any{"type": "agent_end", "text": "<result>done</result>"}},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, sink)
	require.NoError(t, err)
	assert.False(t, called)
}

func TestPiBackend_CommandFailureBecomesRunError(t *testing.T) {
	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Err: errors.New("pi command failed with return code 1\nStderr: oom")},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, Sink{})
	require.Error(t, err)
	var re *RunError
	require.ErrorAs(t, err, &re)
	assert.Contains(t, re.Msg, "pi execution failed")
}

func TestPiBackend_GenericStreamErrorBecomesRunError(t *testing.T) {
	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Err: errors.New("reading pi output: unexpected EOF")},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, Sink{})
	require.Error(t, err)
	var re *RunError
	require.ErrorAs(t, err, &re)
	assert.Contains(t, re.Msg, "reading pi output")
}

func TestPiBackend_RawObjectForwardedToOnRaw(t *testing.T) {
	var rawObjects []map[string]any
	sink := Sink{
		OnRaw: func(obj map[string]any) { rawObjects = append(rawObjects, obj) },
	}

	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{"type": "turn_start"}},
		piwrap.StreamItem{Object: map[string]any{"type": "agent_end", "text": "<result>ok</result>"}},
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, sink)
	require.NoError(t, err)
	assert.Len(t, rawObjects, 2)
}
