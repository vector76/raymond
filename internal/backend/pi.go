package backend

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/vector76/raymond/internal/backendcfg"
	"github.com/vector76/raymond/internal/piwrap"
)

// piInvokeStreamFn is the launcher used by PiBackend.
// Overridable in tests via SetPiInvokeStreamFnForTest.
var piInvokeStreamFn = piwrap.InvokeStream

// SetPiInvokeStreamFnForTest overrides the stream launcher used by PiBackend.
// The returned restore function reinstates the previous value and should be
// called from t.Cleanup or a defer.
func SetPiInvokeStreamFnForTest(
	fn func(ctx context.Context, spec piwrap.CommandSpec, cwd string, idleTimeout float64) <-chan piwrap.StreamItem,
) (restore func()) {
	orig := piInvokeStreamFn
	piInvokeStreamFn = fn
	return func() { piInvokeStreamFn = orig }
}

// PiBackend runs an agent turn by invoking the pi CLI via piwrap and
// translating pi's --mode json event stream into the backend-neutral Sink
// callbacks and TurnResult.
//
// Workflow-level backend options are baked in at construction time (via
// NewPiBackend). Per-turn inputs (prompt, model, effort, session, cwd) arrive
// through TurnSpec on each RunTurn call.
type PiBackend struct {
	opts backendcfg.BackendOptions
}

// NewPiBackend constructs a PiBackend with the given workflow-level options.
func NewPiBackend(opts backendcfg.BackendOptions) *PiBackend {
	return &PiBackend{opts: opts}
}

// RunTurn implements Backend. It launches pi via piwrap, consumes the
// --mode json event stream, and returns a TurnResult populated from the
// agent_start and agent_end events. After pi exits, it reads the session JSONL
// to populate cost and token counts.
//
// ContinueLatest (--continue-and-fork) is implemented by resolving the
// most-recently-modified session file in pi's cwd-keyed session dir to an
// explicit id and then forking from it — pi has no atomic continue-and-fork
// flag combination, so raymond does the lookup itself.
func (b *PiBackend) RunTurn(ctx context.Context, spec TurnSpec, sink Sink) (TurnResult, error) {
	if spec.ContinueLatest {
		latest, err := piwrap.FindLatestSessionID(b.opts.SessionDir, spec.Cwd)
		if err != nil {
			return TurnResult{}, &RunError{
				Msg: fmt.Sprintf("--continue-and-fork: looking up latest pi session: %v", err),
			}
		}
		if latest == "" {
			return TurnResult{}, &RunError{
				Msg: "--continue-and-fork: no pi session found in this working directory to continue from",
			}
		}
		spec.SessionID = latest
		spec.Fork = true
	}

	cmdSpec := piwrap.CommandSpec{
		Prompt:                     spec.Prompt,
		Model:                      spec.Model,
		Effort:                     spec.Effort,
		SessionID:                  spec.SessionID,
		Fork:                       spec.Fork,
		Provider:                   b.opts.Provider,
		ThinkingDefault:            b.opts.Thinking,
		Tools:                      b.opts.Tools,
		NoBuiltinTools:             b.opts.NoBuiltinTools,
		NoTools:                    b.opts.NoTools,
		NoExtensions:               b.opts.NoExtensions,
		NoSkills:                   b.opts.NoSkills,
		Extensions:                 b.opts.Extensions,
		Skills:                     b.opts.Skills,
		SessionDir:                 b.opts.SessionDir,
		DangerouslySkipPermissions: spec.DangerouslySkipPermissions,
	}

	// Read the session's accumulated cost *before* pi runs so we can return a
	// per-turn delta (the Backend.RunTurn contract is per-turn cost, not
	// cumulative). This applies to both resume (--session) AND fork (--fork)
	// because pi's fork operation *copies* the caller's full message history
	// into the new session file; without subtracting the caller's prior cost,
	// every <call> would re-charge the caller's history. Only a truly fresh
	// session (spec.SessionID empty) has a zero prior.
	var priorCost piwrap.SessionCost
	if spec.SessionID != "" {
		sc, err := piwrap.ReadSessionCost(spec.SessionID, b.opts.SessionDir, spec.Cwd)
		if err != nil {
			log.Printf("piwrap: warning: could not read prior session cost for %s: %v", spec.SessionID, err)
		} else {
			priorCost = sc
		}
	}

	ch := piInvokeStreamFn(ctx, cmdSpec, spec.Cwd, spec.IdleTimeout)

	var sessionID string
	var outputText string

	for item := range ch {
		if item.Err != nil {
			return TurnResult{}, mapPiStreamError(item.Err)
		}
		obj := item.Object

		if sink.OnRaw != nil {
			sink.OnRaw(obj)
		}

		eventType, _ := obj["type"].(string)
		switch eventType {
		case "session":
			// pi emits the session id on a top-of-stream "session" event;
			// agent_start in v0.74+ has no sessionId field.
			if id, ok := obj["id"].(string); ok && id != "" {
				sessionID = id
			}

		case "agent_start":
			// Fallback for pi versions that put the id on agent_start.
			if id, ok := obj["sessionId"].(string); ok && id != "" {
				sessionID = id
			}

		case "message_update":
			// Progress from text_delta sub-events.
			if kind, _ := obj["updateType"].(string); kind == "text_delta" {
				if text, ok := obj["text"].(string); ok && text != "" {
					firstLine := text
					if idx := strings.Index(text, "\n"); idx >= 0 {
						firstLine = text[:idx]
					}
					if sink.OnProgress != nil && firstLine != "" {
						sink.OnProgress(firstLine)
					}
				}
			}

		case "tool_execution_start":
			toolName, _ := obj["toolName"].(string)
			if toolName == "" {
				toolName = "unknown"
			}
			detail := extractPiToolDetail(toolName, obj)
			if sink.OnToolUse != nil {
				sink.OnToolUse(toolName, detail)
			}

		case "tool_execution_end":
			isErr, _ := obj["isError"].(bool)
			if isErr && sink.OnToolError != nil {
				msg, _ := obj["result"].(string)
				sink.OnToolError(msg)
			}

		case "agent_end":
			// pi v0.74 puts the final assistant turn in messages[]; concatenate
			// the text parts of the last assistant message.
			outputText = extractPiAgentEndText(obj)
		}
	}

	// Read cost from session JSONL after the turn completes, then subtract the
	// prior cost (zero only for a fresh session) to return a per-turn delta.
	var costUSD float64
	var inputTokens *int64
	if sessionID != "" {
		sc, costErr := piwrap.ReadSessionCost(sessionID, b.opts.SessionDir, spec.Cwd)
		if costErr != nil {
			log.Printf("piwrap: warning: could not read session cost for %s: %v", sessionID, costErr)
		} else {
			costUSD = sc.CostUSD - priorCost.CostUSD
			if costUSD < 0 {
				costUSD = 0 // defensive: prior > after would mean we read wrong file
			}
			turnTokens := sc.InputTokens - priorCost.InputTokens
			if turnTokens > 0 {
				inputTokens = &turnTokens
			}
		}
	}

	return TurnResult{
		OutputText:  outputText,
		SessionID:   sessionID,
		CostUSD:     costUSD,
		InputTokens: inputTokens,
	}, nil
}

// mapPiStreamError translates errors from piwrap into backend-neutral types.
func mapPiStreamError(err error) error {
	var te *piwrap.TimeoutError
	if errors.As(err, &te) {
		return &TimeoutError{Timeout: te.Timeout, Idle: te.Idle}
	}
	if strings.Contains(err.Error(), "pi command failed") {
		return &RunError{Msg: fmt.Sprintf("pi execution failed: %v", err)}
	}
	return &RunError{Msg: err.Error()}
}

// extractPiAgentEndText walks the messages[] array on a pi agent_end event,
// concatenating the text parts of the last assistant message. Returns "" if
// no assistant text is present.
func extractPiAgentEndText(obj map[string]any) string {
	msgs, ok := obj["messages"].([]any)
	if !ok {
		return ""
	}
	// Walk from the end and use the last assistant message.
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "assistant" {
			continue
		}
		var b strings.Builder
		switch content := msg["content"].(type) {
		case string:
			return content
		case []any:
			for _, part := range content {
				p, ok := part.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := p["type"].(string); t != "text" {
					continue
				}
				if txt, ok := p["text"].(string); ok {
					b.WriteString(txt)
				}
			}
		}
		return b.String()
	}
	return ""
}

// extractPiToolDetail returns a short detail string for a tool invocation,
// extracting the file path for file-oriented tools or the command for bash.
func extractPiToolDetail(toolName string, obj map[string]any) string {
	args, ok := obj["args"].(map[string]any)
	if !ok {
		return ""
	}
	switch toolName {
	case "read", "write", "edit", "Read", "Write", "Edit":
		if fp, ok := args["file_path"].(string); ok {
			return filepath.Base(fp)
		}
		if fp, ok := args["path"].(string); ok {
			return filepath.Base(fp)
		}
	case "bash", "Bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	}
	return ""
}
