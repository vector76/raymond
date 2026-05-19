package backend

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/vector76/raymond/internal/ccwrap"
	"github.com/vector76/raymond/internal/parsing"
)

// claudeInvokeStreamFn is the launcher actually used by ClaudeBackend.
// Defaults to ccwrap.InvokeStream; overridable in tests via
// SetClaudeInvokeStreamFnForTest.
var claudeInvokeStreamFn = ccwrap.InvokeStream

// SetClaudeInvokeStreamFnForTest overrides the launcher used by
// ClaudeBackend. The returned restore function reinstates the previous
// value and should be called from t.Cleanup or a defer.
//
// This is a cross-package test hook (rather than an export_test.go
// symbol) so the executors package — which historically owned the
// equivalent var — can keep its public SetInvokeStreamFn working without
// the test setup needing to know about the backend package directly.
func SetClaudeInvokeStreamFnForTest(
	fn func(
		ctx context.Context,
		prompt, model, effort, sessionID string,
		idleTimeout float64,
		dangerouslySkipPermissions, fork bool,
		cwd string,
		continueSession bool,
	) <-chan ccwrap.StreamItem,
) (restore func()) {
	orig := claudeInvokeStreamFn
	claudeInvokeStreamFn = fn
	return func() { claudeInvokeStreamFn = orig }
}

// ClaudeBackend runs an agent turn by invoking the claude CLI via ccwrap
// and translating its stream-JSON output into the backend-neutral Sink
// and TurnResult shapes.
type ClaudeBackend struct{}

// NewClaudeBackend returns the default Claude backend.
func NewClaudeBackend() *ClaudeBackend { return &ClaudeBackend{} }

// RunTurn implements Backend by launching claude, consuming its
// stream-JSON output, and returning a TurnResult assembled from the
// final results. Per-object handling:
//
//  1. Extract session_id (top-level or metadata.session_id); the last
//     non-empty value wins.
//  2. Detect Claude's usage-limit signal and short-circuit with
//     *LimitError.
//  3. Forward the raw object to Sink.OnRaw (the debug-JSONL observer's
//     feed) before any normalized events fire.
//  4. Emit OnProgress / OnToolUse / OnToolError for the relevant
//     assistant/user message items.
//
// Transport-level failures from ccwrap are mapped to *TimeoutError,
// *LimitError (when a stderr-only limit message is detected), or
// *RunError.
func (b *ClaudeBackend) RunTurn(ctx context.Context, spec TurnSpec, sink Sink) (TurnResult, error) {
	ch := claudeInvokeStreamFn(ctx,
		spec.Prompt, spec.Model, spec.Effort, spec.SessionID,
		spec.IdleTimeout, spec.DangerouslySkipPermissions,
		spec.Fork, spec.Cwd, spec.ContinueLatest)

	var results []map[string]any
	var sessionID string
	var printBuf string

	for item := range ch {
		if item.Err != nil {
			return TurnResult{}, mapClaudeStreamError(item.Err)
		}
		obj := item.Object
		results = append(results, obj)

		if sid := ccwrap.ExtractSessionID(obj); sid != "" {
			sessionID = sid
		}

		if isClaudeLimitResult(obj) {
			msg, _ := obj["result"].(string)
			if msg == "" {
				msg = "Claude Code usage limit reached"
			}
			return TurnResult{}, &LimitError{Msg: msg}
		}

		if sink.OnRaw != nil {
			sink.OnRaw(obj)
		}
		emitClaudeStreamEvents(obj, sink)

		if t, _ := obj["type"].(string); t == "assistant" {
			if message, ok := obj["message"].(map[string]any); ok {
				if content, ok := message["content"].([]any); ok {
					for _, rawItem := range content {
						if ci, ok := rawItem.(map[string]any); ok {
							if ci["type"] == "text" {
								if text, _ := ci["text"].(string); text != "" {
									printBuf += text
								}
							}
						}
					}
				}
			}
		}

		payloads, remainder := parsing.ExtractPrintTags(printBuf)
		for _, payload := range payloads {
			if sink.OnPrint != nil {
				sink.OnPrint(payload)
			}
		}
		printBuf = remainder
	}

	return TurnResult{
		OutputText:  extractClaudeOutputText(results),
		SessionID:   sessionID,
		CostUSD:     extractClaudeCost(results),
		InputTokens: extractClaudeTokens(results),
	}, nil
}

// mapClaudeStreamError translates errors coming out of ccwrap's stream
// into backend-neutral error types.
func mapClaudeStreamError(err error) error {
	var te *ccwrap.ClaudeCodeTimeoutError
	if errors.As(err, &te) {
		return &TimeoutError{Timeout: te.Timeout, Idle: te.Idle}
	}
	msg := err.Error()
	// Claude sometimes surfaces a usage-limit message only via stderr +
	// non-zero exit; recover the special case so the orchestrator can
	// pause instead of retry.
	if isClaudeLimitMessage(msg) {
		return &LimitError{Msg: msg}
	}
	if strings.Contains(msg, "claude command failed") {
		return &RunError{Msg: fmt.Sprintf("Claude Code execution failed: %v", err)}
	}
	return &RunError{Msg: msg}
}

// claudeLimitPatterns are substrings (lowercase) that identify a Claude
// usage-limit error in either a stream result or stderr.
var claudeLimitPatterns = []string{
	"hit your limit",
	"out of extra usage",
}

func isClaudeLimitMessage(msg string) bool {
	lower := strings.ToLower(msg)
	for _, pat := range claudeLimitPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// isClaudeLimitResult reports whether obj is a Claude `result` object
// signalling a usage-limit error.
func isClaudeLimitResult(obj map[string]any) bool {
	if t, _ := obj["type"].(string); t != "result" {
		return false
	}
	isErr, _ := obj["is_error"].(bool)
	if !isErr {
		return false
	}
	result, _ := obj["result"].(string)
	return isClaudeLimitMessage(result)
}

// emitClaudeStreamEvents translates one Claude stream object into Sink
// callbacks. Mirrors the original processStreamForConsole behaviour from
// internal/executors/markdown.go; centralising the translation here keeps
// the executor backend-neutral.
func emitClaudeStreamEvents(obj map[string]any, sink Sink) {
	objType, _ := obj["type"].(string)

	if objType == "user" {
		// `user` messages carry tool_result items; we only surface the
		// ones whose is_error flag is set.
		message, ok := obj["message"].(map[string]any)
		if !ok {
			return
		}
		content, ok := message["content"].([]any)
		if !ok {
			return
		}
		for _, rawItem := range content {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := item["type"].(string); t != "tool_result" {
				continue
			}
			isErr, _ := item["is_error"].(bool)
			if !isErr {
				continue
			}
			errMsg, _ := item["content"].(string)
			if errMsg == "" {
				errMsg = "Tool error"
			}
			// Extract content between <tool_use_error> markers when present.
			if start := strings.Index(errMsg, "<tool_use_error>"); start >= 0 {
				start += len("<tool_use_error>")
				if end := strings.Index(errMsg[start:], "</tool_use_error>"); end >= 0 {
					errMsg = errMsg[start : start+end]
				}
			}
			if sink.OnToolError != nil {
				sink.OnToolError(errMsg)
			}
		}
		return
	}

	if objType != "assistant" {
		return
	}
	message, ok := obj["message"].(map[string]any)
	if !ok {
		return
	}
	content, ok := message["content"].([]any)
	if !ok {
		return
	}
	for _, rawItem := range content {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := item["type"].(string)
		switch itemType {
		case "text":
			text, _ := item["text"].(string)
			if text == "" {
				continue
			}
			firstLine := text
			if idx := strings.Index(text, "\n"); idx >= 0 {
				firstLine = text[:idx]
			}
			if sink.OnProgress != nil {
				sink.OnProgress(firstLine)
			}
		case "tool_use":
			toolName, _ := item["name"].(string)
			if toolName == "" {
				toolName = "unknown"
			}
			detail := ""
			if input, ok := item["input"].(map[string]any); ok {
				switch toolName {
				case "Read", "Write", "Edit":
					if fp, ok := input["file_path"].(string); ok {
						detail = filepath.Base(fp)
					}
				case "Bash":
					if cmd, ok := input["command"].(string); ok {
						detail = cmd
					}
				}
			}
			if sink.OnToolUse != nil {
				sink.OnToolUse(toolName, detail)
			}
		}
	}
}

// extractClaudeOutputText assembles the assistant text the executor will
// parse transitions from. Priority matches the original implementation:
// `result` fields take precedence over `message.content[].text`, and a
// handful of fallback shapes are accepted for tests and older payloads.
func extractClaudeOutputText(results []map[string]any) string {
	var sb strings.Builder
	hasResult := false
	for _, r := range results {
		if s, ok := r["result"].(string); ok {
			sb.WriteString(s)
			hasResult = true
		}
	}
	if hasResult {
		return sb.String()
	}

	sb.Reset()
	for _, r := range results {
		if msg, ok := r["message"].(map[string]any); ok {
			switch c := msg["content"].(type) {
			case []any:
				for _, rawItem := range c {
					if item, ok := rawItem.(map[string]any); ok {
						if t, ok := item["text"].(string); ok {
							sb.WriteString(t)
						}
					}
				}
			case string:
				sb.WriteString(c)
			}
		} else if t, ok := r["text"].(string); ok {
			sb.WriteString(t)
		} else if c, ok := r["content"].(string); ok {
			sb.WriteString(c)
		} else if items, ok := r["content"].([]any); ok {
			for _, rawItem := range items {
				if item, ok := rawItem.(map[string]any); ok {
					if t, ok := item["text"].(string); ok {
						sb.WriteString(t)
					}
				}
			}
		}
	}
	return sb.String()
}

// extractClaudeCost returns the last total_cost_usd value seen in
// results, or 0 when none is present.
func extractClaudeCost(results []map[string]any) float64 {
	for i := len(results) - 1; i >= 0; i-- {
		v, ok := results[i]["total_cost_usd"]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return 0.0
}

// extractClaudeTokens returns the prompt-token sum from the last result
// object that carries a usage field, or nil when none does.
func extractClaudeTokens(results []map[string]any) *int64 {
	for i := len(results) - 1; i >= 0; i-- {
		usageRaw, ok := results[i]["usage"]
		if !ok {
			continue
		}
		usage, ok := usageRaw.(map[string]any)
		if !ok {
			return nil
		}
		var sum int64
		for _, key := range []string{"cache_creation_input_tokens", "cache_read_input_tokens", "input_tokens"} {
			switch v := usage[key].(type) {
			case float64:
				sum += int64(v)
			case int:
				sum += int64(v)
			case int64:
				sum += v
			}
		}
		return &sum
	}
	return nil
}
