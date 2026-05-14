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
// ContinueLatest (--continue-and-fork) is not supported for pi and returns an
// error immediately.
func (b *PiBackend) RunTurn(ctx context.Context, spec TurnSpec, sink Sink) (TurnResult, error) {
	if spec.ContinueLatest {
		return TurnResult{}, &RunError{
			Msg: "--continue-and-fork is not supported for the pi backend; " +
				"use --session <id> if you need to resume a specific session",
		}
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
		case "agent_start":
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
				msg := ""
				if result, ok := obj["result"].(string); ok {
					msg = result
				}
				if msg == "" {
					msg = fmt.Sprintf("tool error in %s", toolNameFromEvent(obj))
				}
				sink.OnToolError(msg)
			}

		case "agent_end":
			if text, ok := obj["text"].(string); ok {
				outputText = text
			}
		}
	}

	// Read cost from session JSONL after the turn completes.
	var costUSD float64
	var inputTokens *int64
	if sessionID != "" {
		sc, costErr := piwrap.ReadSessionCost(sessionID, b.opts.SessionDir, spec.Cwd)
		if costErr != nil {
			log.Printf("piwrap: warning: could not read session cost for %s: %v", sessionID, costErr)
		} else {
			costUSD = sc.CostUSD
			if sc.InputTokens > 0 {
				t := sc.InputTokens
				inputTokens = &t
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

// toolNameFromEvent extracts the tool name from a tool_execution_end event.
func toolNameFromEvent(obj map[string]any) string {
	if name, ok := obj["toolName"].(string); ok {
		return name
	}
	return "unknown"
}
