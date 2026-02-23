package executors

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vector76/raymond/internal/ccwrap"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/policy"
	"github.com/vector76/raymond/internal/prompts"
	wfstate "github.com/vector76/raymond/internal/state"
)

// maxReminderAttempts is the maximum number of reminder prompts before giving up.
const maxReminderAttempts = 3

// invokeStreamFn is the ccwrap.InvokeStream implementation used by
// MarkdownExecutor. Overridable in tests.
var invokeStreamFn = func(
	ctx context.Context,
	prompt, model, effort, sessionID string,
	idleTimeout float64,
	dangerouslySkipPermissions, fork bool,
	cwd string,
) <-chan ccwrap.StreamItem {
	return ccwrap.InvokeStream(ctx, prompt, model, effort, sessionID, idleTimeout,
		dangerouslySkipPermissions, fork, cwd)
}

// MarkdownExecutor handles .md states by invoking Claude Code.
//
//   - Loads and renders the prompt template.
//   - Invokes Claude via ccwrap.InvokeStream with an optional reminder loop
//     (up to maxReminderAttempts times) when no valid transition is found.
//   - Extracts session_id and cost from the stream output.
//   - Emits the full set of observer events.
type MarkdownExecutor struct{}

// Execute runs the markdown state and returns the parsed transition.
func (e *MarkdownExecutor) Execute(
	ctx context.Context,
	agent *wfstate.AgentState,
	wfState *wfstate.WorkflowState,
	execCtx *ExecutionContext,
) (ExecutionResult, error) {
	agentID := agent.ID
	currentState := agent.CurrentState
	scopeDir := wfState.ScopeDir
	sessionID := agent.SessionID

	// Emit StateStarted.
	execCtx.Bus.Emit(events.StateStarted{
		AgentID:   agentID,
		StateName: currentState,
		StateType: "markdown",
		Timestamp: time.Now(),
	})

	// Load and parse the prompt file.
	body, pol, err := prompts.LoadPrompt(scopeDir, currentState)
	if err != nil {
		return ExecutionResult{}, &PromptFileError{
			Msg: fmt.Sprintf("Prompt file not found: %v", err),
		}
	}

	// Build template variables.
	variables := make(map[string]any)
	if agent.PendingResult != nil {
		variables["result"] = *agent.PendingResult
	}
	for k, v := range agent.ForkAttributes {
		variables[k] = v
	}
	basePrompt := prompts.RenderPrompt(body, variables)

	// Determine model and effort (frontmatter takes precedence over defaults).
	model := ""
	if pol != nil && pol.Model != "" {
		model = pol.Model
	} else if execCtx.DefaultModel != "" {
		model = strings.ToLower(execCtx.DefaultModel)
	}

	effort := ""
	if pol != nil && pol.Effort != "" {
		effort = pol.Effort
	} else if execCtx.DefaultEffort != "" {
		effort = execCtx.DefaultEffort
	}

	// Reminder loop.
	var transition *parsing.Transition
	newSessionID := sessionID
	reminderAttempt := 0
	var invocationCost float64
	startTime := time.Now()

	for transition == nil {
		// Build prompt (append reminder on retries).
		prompt := basePrompt
		if reminderAttempt > 0 {
			reminder, err := policy.GenerateReminderPrompt(pol)
			if err != nil {
				return ExecutionResult{}, err
			}
			prompt = basePrompt + reminder
		}

		// Determine session to use (fork on first call if fork_session_id is set).
		useSessionID := sessionIDStr(newSessionID)
		useFork := false
		if agent.ForkSessionID != nil && reminderAttempt == 0 {
			useSessionID = *agent.ForkSessionID
			useFork = true
		}

		// Emit ClaudeInvocationStarted.
		execCtx.Bus.Emit(events.ClaudeInvocationStarted{
			AgentID:         agentID,
			StateName:       currentState,
			SessionID:       useSessionID,
			IsFork:          useFork,
			IsReminder:      reminderAttempt > 0,
			ReminderAttempt: reminderAttempt,
			Timestamp:       time.Now(),
		})

		// Prepare debug file path.
		debugFilePath := ""
		stepNumber := 0
		if execCtx.DebugDir != "" {
			stepNumber = execCtx.GetNextStepNumber(agentID)
			stateName := ExtractStateName(currentState)
			debugFilePath = filepath.Join(execCtx.DebugDir,
				fmt.Sprintf("%s_%s_%03d.jsonl", agentID, stateName, stepNumber))
		}

		// Invoke Claude Code and collect results.
		var results []map[string]any
		var streamErr error

		ch := invokeStreamFn(ctx, prompt, model, effort, useSessionID,
			execCtx.Timeout, execCtx.DangerouslySkipPermissions, useFork, agent.Cwd)

		streamLoop:
		for item := range ch {
			if item.Err != nil {
				streamErr = item.Err
				break streamLoop
			}

			obj := item.Object
			results = append(results, obj)

			// Extract session_id.
			if sid, ok := obj["session_id"].(string); ok && sid != "" {
				s := sid
				newSessionID = &s
			} else if meta, ok := obj["metadata"].(map[string]any); ok {
				if sid, ok := meta["session_id"].(string); ok && sid != "" {
					s := sid
					newSessionID = &s
				}
			}

			// Check usage limit.
			if isLimitError(obj) {
				limitMsg := ""
				if r, ok := obj["result"].(string); ok {
					limitMsg = r
				}
				if limitMsg == "" {
					limitMsg = "Claude Code usage limit reached"
				}
				return ExecutionResult{}, &ClaudeCodeLimitError{Msg: limitMsg}
			}

			// Emit ClaudeStreamOutput when debug is active.
			if stepNumber > 0 {
				execCtx.Bus.Emit(events.ClaudeStreamOutput{
					AgentID:    agentID,
					StateName:  currentState,
					StepNumber: stepNumber,
					JSONObject: obj,
					Timestamp:  time.Now(),
				})
			}

			// Append to JSONL debug file.
			if debugFilePath != "" {
				e.appendJSONL(debugFilePath, obj)
			}

			// Emit progress/tool events for console observers.
			e.processStreamForConsole(obj, agentID, execCtx)
		}

		if streamErr != nil {
			if te, ok := streamErr.(*ccwrap.ClaudeCodeTimeoutError); ok {
				return ExecutionResult{}, &ClaudeCodeTimeoutWrappedError{
					Msg: fmt.Sprintf("Claude Code timeout: %v", te),
				}
			}
			if strings.Contains(streamErr.Error(), "claude command failed") {
				return ExecutionResult{}, &ClaudeCodeError{
					Msg: fmt.Sprintf("Claude Code execution failed: %v", streamErr),
				}
			}
			return ExecutionResult{}, &ClaudeCodeError{Msg: streamErr.Error()}
		}

		// Extract and accumulate cost.
		invocationCost = ExtractCostFromResults(results)
		if invocationCost > 0 {
			wfState.TotalCostUSD += invocationCost
		}

		// Budget check.
		if wfState.TotalCostUSD > wfState.BudgetUSD {
			budgetExceeded := parsing.Transition{
				Tag:     "result",
				Payload: fmt.Sprintf("Workflow terminated: budget exceeded ($%.4f > $%.4f)", wfState.TotalCostUSD, wfState.BudgetUSD),
			}
			transition = &budgetExceeded
			break
		}

		// Parse and validate transitions.
		t, retry, err := e.parseAndValidate(
			results, pol, scopeDir,
			agentID, currentState, newSessionID,
			execCtx, reminderAttempt,
		)
		if err != nil {
			return ExecutionResult{}, err
		}
		if retry {
			reminderAttempt++
			if reminderAttempt >= maxReminderAttempts {
				return ExecutionResult{}, fmt.Errorf(
					"Expected exactly one transition, found 0 after %d reminder attempts",
					maxReminderAttempts,
				)
			}
			continue
		}
		transition = t
	}

	durationMS := float64(time.Since(startTime).Milliseconds())

	execCtx.Bus.Emit(events.StateCompleted{
		AgentID:      agentID,
		StateName:    currentState,
		CostUSD:      invocationCost,
		TotalCostUSD: wfState.TotalCostUSD,
		SessionID:    sessionIDStr(newSessionID),
		DurationMS:   durationMS,
		Timestamp:    time.Now(),
	})

	return ExecutionResult{
		Transition: *transition,
		SessionID:  newSessionID,
		CostUSD:    invocationCost,
	}, nil
}

// parseAndValidate parses and validates transitions from stream results.
// Returns (transition, false, nil) on success.
// Returns (nil, true, nil) when a reminder retry is needed.
// Returns (nil, false, err) on a fatal error.
func (e *MarkdownExecutor) parseAndValidate(
	results []map[string]any,
	pol *policy.Policy,
	scopeDir string,
	agentID, currentState string,
	sessionID *string,
	execCtx *ExecutionContext,
	reminderAttempt int,
) (*parsing.Transition, bool, error) {
	outputText := extractOutputText(results)

	transitions, err := parsing.ParseTransitions(outputText)
	if err != nil {
		return nil, false, fmt.Errorf("transition parse error: %w", err)
	}

	// Implicit transition (no tag, policy has exactly one allowed non-result transition with target).
	if len(transitions) == 0 && policy.CanUseImplicitTransition(pol) {
		implicit, err := policy.GetImplicitTransition(pol)
		if err != nil {
			return nil, false, err
		}
		resolved, err := ResolveTransitionTargets(implicit, scopeDir)
		if err != nil {
			return nil, false, err
		}
		return &resolved, false, nil
	}

	// No tag and no implicit transition.
	if len(transitions) == 0 {
		if policy.ShouldUseReminderPrompt(pol) {
			if reminderAttempt+1 >= maxReminderAttempts {
				return nil, false, fmt.Errorf(
					"Expected exactly one transition, found 0 after %d reminder attempts",
					maxReminderAttempts,
				)
			}
			execCtx.Bus.Emit(events.ErrorOccurred{
				AgentID:      agentID,
				ErrorType:    "NoTransitionTag",
				ErrorMessage: "No transition tag found in output",
				CurrentState: currentState,
				IsRetryable:  true,
				RetryCount:   reminderAttempt + 1,
				MaxRetries:   maxReminderAttempts,
				Timestamp:    time.Now(),
			})
			return nil, true, nil // retry
		}
		return nil, false, fmt.Errorf("Expected exactly one transition, found 0")
	}

	// Validate exactly one transition.
	if err := parsing.ValidateSingleTransition(transitions); err != nil {
		if policy.ShouldUseReminderPrompt(pol) {
			if reminderAttempt+1 >= maxReminderAttempts {
				return nil, false, err
			}
			execCtx.Bus.Emit(events.ErrorOccurred{
				AgentID:      agentID,
				ErrorType:    "MultipleTransitions",
				ErrorMessage: err.Error(),
				CurrentState: currentState,
				IsRetryable:  true,
				RetryCount:   reminderAttempt + 1,
				MaxRetries:   maxReminderAttempts,
				Timestamp:    time.Now(),
			})
			return nil, true, nil // retry
		}
		return nil, false, err
	}

	transition := transitions[0]

	// Resolve abstract state names.
	resolved, resolveErr := ResolveTransitionTargets(transition, scopeDir)
	if resolveErr != nil {
		if policy.ShouldUseReminderPrompt(pol) {
			if reminderAttempt+1 >= maxReminderAttempts {
				return nil, false, resolveErr
			}
			execCtx.Bus.Emit(events.ErrorOccurred{
				AgentID:      agentID,
				ErrorType:    "TargetResolutionError",
				ErrorMessage: resolveErr.Error(),
				CurrentState: currentState,
				IsRetryable:  true,
				RetryCount:   reminderAttempt + 1,
				MaxRetries:   maxReminderAttempts,
				Timestamp:    time.Now(),
			})
			return nil, true, nil // retry
		}
		return nil, false, resolveErr
	}

	// Validate against policy.
	if polErr := policy.ValidateTransitionPolicy(resolved, pol); polErr != nil {
		if policy.ShouldUseReminderPrompt(pol) {
			if reminderAttempt+1 >= maxReminderAttempts {
				return nil, false, polErr
			}
			execCtx.Bus.Emit(events.ErrorOccurred{
				AgentID:      agentID,
				ErrorType:    "PolicyViolation",
				ErrorMessage: polErr.Error(),
				CurrentState: currentState,
				IsRetryable:  true,
				RetryCount:   reminderAttempt + 1,
				MaxRetries:   maxReminderAttempts,
				Timestamp:    time.Now(),
			})
			return nil, true, nil // retry
		}
		return nil, false, polErr
	}

	return &resolved, false, nil
}

// processStreamForConsole emits ProgressMessage and ToolInvocation events
// for assistant messages in the Claude stream.
func (e *MarkdownExecutor) processStreamForConsole(obj map[string]any, agentID string, execCtx *ExecutionContext) {
	objType, _ := obj["type"].(string)
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
			if text != "" {
				firstLine := text
				if idx := strings.Index(text, "\n"); idx >= 0 {
					firstLine = text[:idx]
				}
				execCtx.Bus.Emit(events.ProgressMessage{
					AgentID:   agentID,
					Message:   firstLine,
					Timestamp: time.Now(),
				})
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
						if len(detail) > 40 {
							detail = detail[:37] + "..."
						}
					}
				}
			}
			execCtx.Bus.Emit(events.ToolInvocation{
				AgentID:   agentID,
				ToolName:  toolName,
				Detail:    detail,
				Timestamp: time.Now(),
			})
		}
	}
}

// appendJSONL appends obj as a JSON line to path, silently ignoring errors.
func (e *MarkdownExecutor) appendJSONL(path string, obj map[string]any) {
	data, err := json.Marshal(obj)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// extractOutputText extracts the Claude text output from stream results.
// Priority: "result" field > message.content[].text > text/content fields.
func extractOutputText(results []map[string]any) string {
	// First pass: look for result field.
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

	// Second pass: extract from other sources.
	sb.Reset()
	for _, r := range results {
		if msg, ok := r["message"].(map[string]any); ok {
			content := msg["content"]
			switch c := content.(type) {
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

// isLimitError returns true when obj is a Claude usage-limit result object.
func isLimitError(obj map[string]any) bool {
	if t, _ := obj["type"].(string); t != "result" {
		return false
	}
	isErr, _ := obj["is_error"].(bool)
	if !isErr {
		return false
	}
	result, _ := obj["result"].(string)
	return strings.Contains(strings.ToLower(result), "hit your limit")
}
