package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// piSessionEvt returns a stream item matching pi v0.74's top-of-stream
// session event (which carries the session id).
func piSessionEvt(id string) piwrap.StreamItem {
	return piwrap.StreamItem{Object: map[string]any{"type": "session", "id": id}}
}

// piAgentEndText returns a stream item matching pi v0.74's agent_end event
// shape, where the final assistant text lives under messages[-1].content[].
func piAgentEndText(text string) piwrap.StreamItem {
	return piwrap.StreamItem{Object: map[string]any{
		"type": "agent_end",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": text},
				},
			},
		},
	}}
}

func TestPiBackend_SessionIDFromSessionEvent(t *testing.T) {
	// pi v0.74 emits the session id on a "session" event, not on agent_start.
	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piSessionEvt("uuid-abc-123"),
		piwrap.StreamItem{Object: map[string]any{"type": "agent_start"}},
		piAgentEndText("<goto>done</goto>"),
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	result, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, Sink{})
	require.NoError(t, err)
	assert.Equal(t, "uuid-abc-123", result.SessionID)
}

func TestPiBackend_SessionIDFromAgentStartFallback(t *testing.T) {
	// Forward-compat: if a future pi version moves the id back to agent_start,
	// we still capture it.
	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{"type": "agent_start", "sessionId": "fallback-id"}},
		piAgentEndText("<goto>done</goto>"),
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	result, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, Sink{})
	require.NoError(t, err)
	assert.Equal(t, "fallback-id", result.SessionID)
}

func TestPiBackend_OutputTextFromAgentEnd(t *testing.T) {
	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piSessionEvt("s1"),
		piAgentEndText("<goto>next_state</goto>"),
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
		piAgentEndText("<result>done</result>"),
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
		piAgentEndText("<result>done</result>"),
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
		piAgentEndText("<result>done</result>"),
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
		piAgentEndText("<result>done</result>"),
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

func TestPiBackend_ContinueLatest_ResolvesAndForks(t *testing.T) {
	// ContinueLatest on pi resolves the most-recent session file in the
	// session dir to a session id, then forks from it (pi has no atomic
	// continue-and-fork flag, so raymond does the lookup).
	dir := t.TempDir()
	// Two sessions; the second has a later mtime and must be selected.
	// We push older's mtime into the past explicitly so the comparison is
	// not subject to filesystem mtime granularity (1s on some filesystems).
	older := filepath.Join(dir, "2025-01-01T00:00:00Z_uuid-older.jsonl")
	newer := filepath.Join(dir, "2025-01-02T00:00:00Z_uuid-newer.jsonl")
	require.NoError(t, os.WriteFile(older, []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(newer, []byte("{}\n"), 0o644))
	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(older, past, past))

	var capturedSpec piwrap.CommandSpec
	stream := func(_ context.Context, spec piwrap.CommandSpec, _ string, _ float64) <-chan piwrap.StreamItem {
		capturedSpec = spec
		ch := make(chan piwrap.StreamItem, 2)
		ch <- piSessionEvt("forked-session-id")
		ch <- piAgentEndText("<goto>done</goto>")
		close(ch)
		return ch
	}
	restore := SetPiInvokeStreamFnForTest(stream)
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{SessionDir: dir})
	_, err := b.RunTurn(context.Background(), TurnSpec{
		Prompt:         "p",
		ContinueLatest: true,
	}, Sink{})
	require.NoError(t, err)
	assert.True(t, capturedSpec.Fork, "ContinueLatest must run pi with Fork=true")
	assert.Equal(t, "uuid-newer", capturedSpec.SessionID,
		"ContinueLatest must select the most-recently-modified session file")
}

func TestPiBackend_ContinueLatest_NoSessionFound(t *testing.T) {
	dir := t.TempDir() // empty session dir
	b := NewPiBackend(backendcfg.BackendOptions{SessionDir: dir})
	_, err := b.RunTurn(context.Background(), TurnSpec{
		Prompt:         "p",
		ContinueLatest: true,
	}, Sink{})
	require.Error(t, err)
	var re *RunError
	require.ErrorAs(t, err, &re)
	assert.Contains(t, re.Msg, "no pi session found")
}

func TestPiBackend_ResumeReturnsPerTurnDeltaNotCumulative(t *testing.T) {
	// Backend.RunTurn contract: CostUSD is per-turn cost, not cumulative.
	// On resume, the on-disk session file contains BOTH the prior turn's
	// assistant message and the just-completed one. The backend must subtract
	// the pre-existing cost so the orchestrator (which adds CostUSD to total)
	// doesn't double-count.
	dir := t.TempDir()
	sessionID := "resume-test-session"

	// Simulate state at end of turn 1: one assistant message at $0.005.
	priorFile := filepath.Join(dir, sessionID+".jsonl")
	require.NoError(t, os.WriteFile(priorFile,
		[]byte(`{"type":"message","message":{"role":"assistant","usage":{"input":100,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.005}}}}`+"\n"),
		0o644))

	// Stub pi's behavior for turn 2: append the new assistant message
	// (cost $0.003) before emitting the session id, mimicking pi's "writes
	// the file, then closes" sequence as seen by ReadSessionCost.
	stream := func(_ context.Context, _ piwrap.CommandSpec, _ string, _ float64) <-chan piwrap.StreamItem {
		require.NoError(t, os.WriteFile(priorFile,
			[]byte(`{"type":"message","message":{"role":"assistant","usage":{"input":100,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.005}}}}
{"type":"message","message":{"role":"assistant","usage":{"input":50,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.003}}}}
`),
			0o644))
		ch := make(chan piwrap.StreamItem, 3)
		ch <- piSessionEvt(sessionID)
		ch <- piAgentEndText("<result>done</result>")
		close(ch)
		return ch
	}
	restore := SetPiInvokeStreamFnForTest(stream)
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{SessionDir: dir})
	result, err := b.RunTurn(context.Background(),
		TurnSpec{Prompt: "p", SessionID: sessionID}, Sink{})
	require.NoError(t, err)
	assert.InDelta(t, 0.003, result.CostUSD, 1e-9, "must return turn-2 delta, not session total")
	require.NotNil(t, result.InputTokens)
	assert.Equal(t, int64(50), *result.InputTokens)
}

func TestPiBackend_ForkSubtractsCallerHistoryFromForkedFile(t *testing.T) {
	// Pi's --fork operation *copies* the caller's full message history into
	// the new session file. The forked file then contains caller_msgs +
	// new_fork_msg. If the backend reported only the after-cost, the caller's
	// history would be charged twice (once on the caller's <goto> loop, once
	// when <call> copies them into the fork). The backend must subtract the
	// caller's prior cost so only the new fork's turn is reported.
	dir := t.TempDir()
	callerID := "caller-session"
	forkedID := "forked-session"

	// Caller history: $0.020 of assistant cost.
	callerLines := `{"type":"message","message":{"role":"assistant","usage":{"input":1000,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.020}}}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, callerID+".jsonl"),
		[]byte(callerLines), 0o644))

	stream := func(_ context.Context, _ piwrap.CommandSpec, _ string, _ float64) <-chan piwrap.StreamItem {
		// Forked file: caller history copy + new fork message.
		require.NoError(t, os.WriteFile(filepath.Join(dir, forkedID+".jsonl"),
			[]byte(callerLines+
				`{"type":"message","message":{"role":"assistant","usage":{"input":80,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.004}}}}`+"\n"),
			0o644))
		ch := make(chan piwrap.StreamItem, 3)
		ch <- piSessionEvt(forkedID)
		ch <- piAgentEndText("<result>done</result>")
		close(ch)
		return ch
	}
	restore := SetPiInvokeStreamFnForTest(stream)
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{SessionDir: dir})
	result, err := b.RunTurn(context.Background(),
		TurnSpec{Prompt: "p", SessionID: callerID, Fork: true}, Sink{})
	require.NoError(t, err)
	assert.InDelta(t, 0.004, result.CostUSD, 1e-9,
		"fork must report only the new turn's cost (not re-charge caller history)")
}

func TestPiBackend_ZeroCostWhenNoSessionFile(t *testing.T) {
	restore := SetPiInvokeStreamFnForTest(makePiStream(
		piwrap.StreamItem{Object: map[string]any{"type": "agent_start", "sessionId": "no-such-session-id"}},
		piAgentEndText("<result>done</result>"),
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
		piAgentEndText("<result>done</result>"),
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
		piAgentEndText("<result>done</result>"),
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
		piAgentEndText("<result>ok</result>"),
	))
	defer restore()

	b := NewPiBackend(backendcfg.BackendOptions{})
	_, err := b.RunTurn(context.Background(), TurnSpec{Prompt: "p"}, sink)
	require.NoError(t, err)
	assert.Len(t, rawObjects, 2)
}
