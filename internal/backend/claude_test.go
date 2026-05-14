package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/vector76/raymond/internal/ccwrap"
)

// fakeStream returns a closed channel pre-loaded with the given items.
func fakeStream(items ...ccwrap.StreamItem) <-chan ccwrap.StreamItem {
	ch := make(chan ccwrap.StreamItem, len(items))
	for _, it := range items {
		ch <- it
	}
	close(ch)
	return ch
}

// installStream replaces the package launcher for the duration of the
// test. It returns the captured TurnSpec the launcher last saw, so a
// test can assert the executor passed through the right parameters.
func installStream(t *testing.T, build func() <-chan ccwrap.StreamItem) *capturedArgs {
	t.Helper()
	cap := &capturedArgs{}
	restore := SetClaudeInvokeStreamFnForTest(func(
		_ context.Context,
		prompt, model, effort, sessionID string,
		idle float64,
		dskp, fork bool,
		cwd string,
		cont bool,
	) <-chan ccwrap.StreamItem {
		cap.prompt = prompt
		cap.model = model
		cap.effort = effort
		cap.sessionID = sessionID
		cap.idle = idle
		cap.dskp = dskp
		cap.fork = fork
		cap.cwd = cwd
		cap.cont = cont
		return build()
	})
	t.Cleanup(restore)
	return cap
}

type capturedArgs struct {
	prompt, model, effort, sessionID, cwd string
	idle                                  float64
	dskp, fork, cont                      bool
}

// TestClaudeBackend_PassesSpecThrough verifies that fields on TurnSpec
// reach the underlying launcher unmodified.
func TestClaudeBackend_PassesSpecThrough(t *testing.T) {
	args := installStream(t, func() <-chan ccwrap.StreamItem {
		return fakeStream(ccwrap.StreamItem{Object: map[string]any{
			"session_id": "sess-1", "total_cost_usd": 0.0,
		}})
	})

	spec := TurnSpec{
		Prompt: "hello", Model: "opus", Effort: "high",
		SessionID: "sess-prev", Fork: true, ContinueLatest: false,
		Cwd: "/tmp", IdleTimeout: 12.5, DangerouslySkipPermissions: true,
	}
	_, err := NewClaudeBackend().RunTurn(context.Background(), spec, Sink{})
	if err != nil {
		t.Fatalf("RunTurn error: %v", err)
	}

	if args.prompt != "hello" || args.model != "opus" || args.effort != "high" {
		t.Errorf("prompt/model/effort not passed through: %+v", args)
	}
	if args.sessionID != "sess-prev" || !args.fork || args.cont {
		t.Errorf("session flags wrong: %+v", args)
	}
	if args.cwd != "/tmp" || args.idle != 12.5 || !args.dskp {
		t.Errorf("misc flags wrong: %+v", args)
	}
}

// TestClaudeBackend_SessionAndCost verifies TurnResult reflects the
// session id, cost, and tokens read from the stream's result objects.
func TestClaudeBackend_SessionAndCost(t *testing.T) {
	installStream(t, func() <-chan ccwrap.StreamItem {
		return fakeStream(
			ccwrap.StreamItem{Object: map[string]any{
				"type": "result", "result": "<goto>NEXT.md</goto>",
			}},
			ccwrap.StreamItem{Object: map[string]any{
				"session_id":     "sess-final",
				"total_cost_usd": 0.07,
				"usage": map[string]any{
					"input_tokens": float64(40), "cache_read_input_tokens": float64(60),
				},
			}},
		)
	})

	tr, err := NewClaudeBackend().RunTurn(context.Background(), TurnSpec{}, Sink{})
	if err != nil {
		t.Fatalf("RunTurn error: %v", err)
	}
	if tr.SessionID != "sess-final" {
		t.Errorf("SessionID = %q, want sess-final", tr.SessionID)
	}
	if tr.CostUSD != 0.07 {
		t.Errorf("CostUSD = %v, want 0.07", tr.CostUSD)
	}
	if tr.InputTokens == nil || *tr.InputTokens != 100 {
		t.Errorf("InputTokens = %v, want 100", tr.InputTokens)
	}
	if tr.OutputText != "<goto>NEXT.md</goto>" {
		t.Errorf("OutputText = %q, want goto tag", tr.OutputText)
	}
}

// TestClaudeBackend_LimitInStream maps an in-stream limit object to
// *LimitError without consuming the rest of the stream.
func TestClaudeBackend_LimitInStream(t *testing.T) {
	installStream(t, func() <-chan ccwrap.StreamItem {
		return fakeStream(ccwrap.StreamItem{Object: map[string]any{
			"type":     "result",
			"is_error": true,
			"result":   "You hit your limit. Try later.",
		}})
	})

	_, err := NewClaudeBackend().RunTurn(context.Background(), TurnSpec{}, Sink{})
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LimitError, got %T: %v", err, err)
	}
	if le.Msg == "" {
		t.Error("LimitError.Msg should not be empty")
	}
}

// TestClaudeBackend_StderrLimitMapped covers the path where the stream
// errors with a stderr-only "out of extra usage" message: it should
// still surface as *LimitError so the orchestrator pauses.
func TestClaudeBackend_StderrLimitMapped(t *testing.T) {
	installStream(t, func() <-chan ccwrap.StreamItem {
		return fakeStream(ccwrap.StreamItem{Err: errors.New(
			"claude command failed with return code 1\nStderr: out of extra usage",
		)})
	})

	_, err := NewClaudeBackend().RunTurn(context.Background(), TurnSpec{}, Sink{})
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LimitError, got %T: %v", err, err)
	}
}

// TestClaudeBackend_TimeoutMapped translates a ccwrap timeout error into
// the backend-neutral *TimeoutError.
func TestClaudeBackend_TimeoutMapped(t *testing.T) {
	installStream(t, func() <-chan ccwrap.StreamItem {
		return fakeStream(ccwrap.StreamItem{Err: &ccwrap.ClaudeCodeTimeoutError{
			Timeout: 5.5, Idle: true,
		}})
	})

	_, err := NewClaudeBackend().RunTurn(context.Background(), TurnSpec{}, Sink{})
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("expected *TimeoutError, got %T: %v", err, err)
	}
	if te.Timeout != 5.5 || !te.Idle {
		t.Errorf("TimeoutError fields wrong: %+v", te)
	}
}

// TestClaudeBackend_RunErrorOnGenericFailure exercises the fallback
// path for non-timeout, non-limit transport errors.
func TestClaudeBackend_RunErrorOnGenericFailure(t *testing.T) {
	installStream(t, func() <-chan ccwrap.StreamItem {
		return fakeStream(ccwrap.StreamItem{Err: errors.New(
			"claude command failed: pipe closed",
		)})
	})

	_, err := NewClaudeBackend().RunTurn(context.Background(), TurnSpec{}, Sink{})
	var re *RunError
	if !errors.As(err, &re) {
		t.Fatalf("expected *RunError, got %T: %v", err, err)
	}
}

// TestClaudeBackend_SinkProgressAndToolEvents verifies that assistant
// text and tool_use items reach the Sink in the expected shape.
func TestClaudeBackend_SinkProgressAndToolEvents(t *testing.T) {
	installStream(t, func() <-chan ccwrap.StreamItem {
		return fakeStream(
			ccwrap.StreamItem{Object: map[string]any{
				"type": "assistant",
				"message": map[string]any{
					"content": []any{
						map[string]any{"type": "text", "text": "first line\nsecond"},
						map[string]any{"type": "tool_use", "name": "Read",
							"input": map[string]any{"file_path": "/a/b/c.go"}},
					},
				},
			}},
			ccwrap.StreamItem{Object: map[string]any{
				"type": "result", "result": "<goto>NEXT.md</goto>",
				"total_cost_usd": 0.0,
			}},
		)
	})

	var progress []string
	var tools []string
	sink := Sink{
		OnProgress: func(s string) { progress = append(progress, s) },
		OnToolUse:  func(n, d string) { tools = append(tools, n+":"+d) },
	}
	_, err := NewClaudeBackend().RunTurn(context.Background(), TurnSpec{}, sink)
	if err != nil {
		t.Fatalf("RunTurn error: %v", err)
	}
	if len(progress) != 1 || progress[0] != "first line" {
		t.Errorf("progress = %v, want [first line]", progress)
	}
	if len(tools) != 1 || tools[0] != "Read:c.go" {
		t.Errorf("tools = %v, want [Read:c.go]", tools)
	}
}

// TestClaudeBackend_SinkToolError covers the tool_result-with-is_error
// path including extraction of <tool_use_error> body text.
func TestClaudeBackend_SinkToolError(t *testing.T) {
	installStream(t, func() <-chan ccwrap.StreamItem {
		return fakeStream(
			ccwrap.StreamItem{Object: map[string]any{
				"type": "user",
				"message": map[string]any{
					"content": []any{
						map[string]any{
							"type":     "tool_result",
							"is_error": true,
							"content":  "prefix <tool_use_error>perm denied</tool_use_error> suffix",
						},
					},
				},
			}},
			ccwrap.StreamItem{Object: map[string]any{
				"type": "result", "result": "<goto>X.md</goto>",
			}},
		)
	})

	var errs []string
	sink := Sink{OnToolError: func(m string) { errs = append(errs, m) }}
	_, err := NewClaudeBackend().RunTurn(context.Background(), TurnSpec{}, sink)
	if err != nil {
		t.Fatalf("RunTurn error: %v", err)
	}
	if len(errs) != 1 || errs[0] != "perm denied" {
		t.Errorf("errs = %v, want [perm denied]", errs)
	}
}

// TestClaudeBackend_SinkRawFiresBeforeNormalized verifies the order in
// which the Sink callbacks are invoked: raw first (debug log), then the
// normalised events.
func TestClaudeBackend_SinkRawFiresBeforeNormalized(t *testing.T) {
	installStream(t, func() <-chan ccwrap.StreamItem {
		return fakeStream(ccwrap.StreamItem{Object: map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "msg"},
				},
			},
		}})
	})

	var order []string
	sink := Sink{
		OnRaw:      func(_ map[string]any) { order = append(order, "raw") },
		OnProgress: func(_ string) { order = append(order, "progress") },
	}
	_, err := NewClaudeBackend().RunTurn(context.Background(), TurnSpec{}, sink)
	if err != nil {
		t.Fatalf("RunTurn error: %v", err)
	}
	if len(order) != 2 || order[0] != "raw" || order[1] != "progress" {
		t.Errorf("event order = %v, want [raw progress]", order)
	}
}

// TestClaudeBackend_NilSinkCallbacksSafe confirms that backends nil-check
// every Sink callback. Passing the zero Sink must not panic.
func TestClaudeBackend_NilSinkCallbacksSafe(t *testing.T) {
	installStream(t, func() <-chan ccwrap.StreamItem {
		return fakeStream(
			ccwrap.StreamItem{Object: map[string]any{
				"type": "assistant",
				"message": map[string]any{
					"content": []any{
						map[string]any{"type": "text", "text": "hi"},
						map[string]any{"type": "tool_use", "name": "Bash",
							"input": map[string]any{"command": "ls"}},
					},
				},
			}},
			ccwrap.StreamItem{Object: map[string]any{
				"type": "user",
				"message": map[string]any{
					"content": []any{
						map[string]any{"type": "tool_result", "is_error": true,
							"content": "err"},
					},
				},
			}},
		)
	})

	_, err := NewClaudeBackend().RunTurn(context.Background(), TurnSpec{}, Sink{})
	if err != nil {
		t.Fatalf("RunTurn with empty Sink panicked or errored: %v", err)
	}
}

// TestIsClaudeLimitMessage_PatternMatches exercises the case-insensitive
// substring match used to detect provider-side limit messages.
func TestIsClaudeLimitMessage_PatternMatches(t *testing.T) {
	cases := map[string]bool{
		"You hit your limit, sorry":    true,
		"Out of extra usage for today": true,
		"normal failure":               false,
		"":                             false,
	}
	for in, want := range cases {
		if got := isClaudeLimitMessage(in); got != want {
			t.Errorf("isClaudeLimitMessage(%q) = %v, want %v", in, got, want)
		}
	}
}
