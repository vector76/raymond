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
	continueSession bool,
) <-chan ccwrap.StreamItem {
	return ccwrap.InvokeStream(ctx, prompt, model, effort, sessionID, idleTimeout,
		dangerouslySkipPermissions, fork, cwd, continueSession)
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
	// Wrap with a cancel so that the InvokeStream goroutine exits cleanly
	// whenever Execute returns early (error, budget exceeded, etc.).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	agentID := agent.ID
	currentState := agent.CurrentState
	scopeDir := agent.ScopeDir
	sessionID := agent.SessionID

	// Emit StateStarted.
	execCtx.Bus.Emit(events.StateStarted{
		AgentID:   agentID,
		StateName: currentState,
		StateType: events.StateTypeMarkdown,
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
	// Default to "sonnet" when nothing else specifies, for a predictable
	// baseline instead of relying on ~/.claude/settings.json.
	model := "sonnet"
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
	var stateTotalCost float64
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
		useContinue := false
		if agent.ContinueAndFork && reminderAttempt == 0 {
			useContinue = true
		} else if agent.ForkSessionID != nil && reminderAttempt == 0 {
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
			execCtx.Timeout, execCtx.DangerouslySkipPermissions, useFork, agent.Cwd, useContinue)

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
			// When claude exits with a non-zero code and the error text
			// contains a known limit message, treat it as a limit error
			// so the orchestrator pauses immediately instead of retrying.
			errText := streamErr.Error()
			if isLimitMessage(errText) {
				return ExecutionResult{}, &ClaudeCodeLimitError{Msg: errText}
			}
			if strings.Contains(errText, "claude command failed") {
				return ExecutionResult{}, &ClaudeCodeError{
					Msg: fmt.Sprintf("Claude Code execution failed: %v", streamErr),
				}
			}
			return ExecutionResult{}, &ClaudeCodeError{Msg: errText}
		}

		// Extract and accumulate cost.
		invocationCost := ExtractCostFromResults(results)
		if invocationCost > 0 {
			stateTotalCost += invocationCost
			wfState.TotalCostUSD += invocationCost
		}

		// Budget check. The check is intentionally performed after accumulating
		// the cost: a single invocation may push spend above the budget limit,
		// but that overage is bounded to one LLM call. Strict pre-invocation
		// checking would require estimating token costs upfront, which is not
		// reliably possible. The chosen approach keeps the logic simple and
		// predictable: every invocation runs to completion, then the budget is
		// evaluated.
		if wfState.TotalCostUSD > wfState.BudgetUSD {
			budgetExceeded := parsing.Transition{
				Tag:     "result",
				Payload: fmt.Sprintf("Workflow terminated: budget exceeded ($%.4f > $%.4f)", wfState.TotalCostUSD, wfState.BudgetUSD),
			}
			transition = &budgetExceeded
			break
		}

		// Parse and validate transitions.
		allTrs, singleTr, doRetry, parseErr := e.parseAndValidate(
			results, pol, scopeDir,
			agentID, currentState, newSessionID,
			execCtx, reminderAttempt,
		)
		if parseErr != nil {
			return ExecutionResult{}, parseErr
		}
		if doRetry {
			reminderAttempt++
			if reminderAttempt >= maxReminderAttempts {
				return ExecutionResult{}, fmt.Errorf(
					"Expected exactly one transition, found 0 after %d reminder attempts",
					maxReminderAttempts,
				)
			}
			continue
		}
		if allTrs != nil {
			// Multi-fork: return full list without selecting a single transition.
			agent.ContinueAndFork = false
			return ExecutionResult{
				Transitions: allTrs,
				SessionID:   newSessionID,
				CostUSD:     stateTotalCost,
			}, nil
		}
		transition = singleTr
	}

	// Clear continue-and-fork so it only fires once.
	agent.ContinueAndFork = false

	durationMS := float64(time.Since(startTime).Milliseconds())

	execCtx.Bus.Emit(events.StateCompleted{
		AgentID:      agentID,
		StateName:    currentState,
		CostUSD:      stateTotalCost,
		TotalCostUSD: wfState.TotalCostUSD,
		SessionID:    sessionIDStr(newSessionID),
		DurationMS:   durationMS,
		Timestamp:    time.Now(),
	})

	return ExecutionResult{
		Transition: *transition,
		SessionID:  newSessionID,
		CostUSD:    stateTotalCost,
	}, nil
}

// isMultiFork reports whether a transition list should be dispatched via the
// multi-fork path: multiple fork-family tags, or fork-family + goto together.
func isMultiFork(trs []parsing.Transition) bool {
	forkCount := 0
	hasGoto := false
	for _, t := range trs {
		switch t.Tag {
		case "fork", "fork-workflow":
			forkCount++
		case "goto":
			hasGoto = true
		}
	}
	return forkCount >= 2 || (forkCount >= 1 && hasGoto)
}

// parseAndValidate parses and validates transitions from stream results.
//
// Returns:
//   - (all, nil, false, nil) when multi-fork is detected (all is the full list).
//   - (nil, single, false, nil) on single-transition success.
//   - (nil, nil, true, nil) when a reminder retry is needed.
//   - (nil, nil, false, err) on a fatal error.
func (e *MarkdownExecutor) parseAndValidate(
	results []map[string]any,
	pol *policy.Policy,
	scopeDir string,
	agentID, currentState string,
	sessionID *string,
	execCtx *ExecutionContext,
	reminderAttempt int,
) (all []parsing.Transition, single *parsing.Transition, retry bool, err error) {
	outputText := extractOutputText(results)

	transitions, parseErr := parsing.ParseTransitions(outputText)
	if parseErr != nil {
		return nil, nil, false, fmt.Errorf("transition parse error: %w", parseErr)
	}

	// Multi-fork: pass the full list through without single-transition validation.
	if isMultiFork(transitions) {
		return transitions, nil, false, nil
	}

	// Implicit transition (no tag, policy has exactly one allowed non-result transition with target).
	if len(transitions) == 0 && policy.CanUseImplicitTransition(pol) {
		implicit, implicitErr := policy.GetImplicitTransition(pol)
		if implicitErr != nil {
			return nil, nil, false, implicitErr
		}
		resolved, resolveErr := ResolveTransitionTargets(implicit, scopeDir)
		if resolveErr != nil {
			return nil, nil, false, resolveErr
		}
		return nil, &resolved, false, nil
	}

	// No tag and no implicit transition.
	if len(transitions) == 0 {
		if policy.ShouldUseReminderPrompt(pol) {
			if reminderAttempt+1 >= maxReminderAttempts {
				return nil, nil, false, fmt.Errorf(
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
			return nil, nil, true, nil // retry
		}
		return nil, nil, false, fmt.Errorf("Expected exactly one transition, found 0")
	}

	// Validate exactly one transition.
	if singleErr := parsing.ValidateSingleTransition(transitions); singleErr != nil {
		if policy.ShouldUseReminderPrompt(pol) {
			if reminderAttempt+1 >= maxReminderAttempts {
				return nil, nil, false, singleErr
			}
			execCtx.Bus.Emit(events.ErrorOccurred{
				AgentID:      agentID,
				ErrorType:    "MultipleTransitions",
				ErrorMessage: singleErr.Error(),
				CurrentState: currentState,
				IsRetryable:  true,
				RetryCount:   reminderAttempt + 1,
				MaxRetries:   maxReminderAttempts,
				Timestamp:    time.Now(),
			})
			return nil, nil, true, nil // retry
		}
		return nil, nil, false, singleErr
	}

	transition := transitions[0]

	// Resolve abstract state names.
	resolved, resolveErr := ResolveTransitionTargets(transition, scopeDir)
	if resolveErr != nil {
		if policy.ShouldUseReminderPrompt(pol) {
			if reminderAttempt+1 >= maxReminderAttempts {
				return nil, nil, false, resolveErr
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
			return nil, nil, true, nil // retry
		}
		return nil, nil, false, resolveErr
	}

	// Validate against policy.
	if polErr := policy.ValidateTransitionPolicy(resolved, pol); polErr != nil {
		if policy.ShouldUseReminderPrompt(pol) {
			if reminderAttempt+1 >= maxReminderAttempts {
				return nil, nil, false, polErr
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
			return nil, nil, true, nil // retry
		}
		return nil, nil, false, polErr
	}

	return nil, &resolved, false, nil
}

// processStreamForConsole emits ProgressMessage, ToolInvocation, and ErrorOccurred
// events for assistant and user messages in the Claude stream.
func (e *MarkdownExecutor) processStreamForConsole(obj map[string]any, agentID string, execCtx *ExecutionContext) {
	objType, _ := obj["type"].(string)

	// "user" messages carry tool_result items when a tool call fails.
	// Extract those errors and emit ErrorOccurred so observers can display them.
	if objType == "user" {
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
			if itemType != "tool_result" {
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
			// Extract content between <tool_use_error> tags when present.
			if start := strings.Index(errMsg, "<tool_use_error>"); start >= 0 {
				start += len("<tool_use_error>")
				if end := strings.Index(errMsg[start:], "</tool_use_error>"); end >= 0 {
					errMsg = errMsg[start : start+end]
				}
			}
			execCtx.Bus.Emit(events.ErrorOccurred{
				AgentID:      agentID,
				ErrorType:    "ToolError",
				ErrorMessage: errMsg,
				IsRetryable:  false,
				Timestamp:    time.Now(),
			})
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

// appendJSONL appends obj as a JSON line to path.
// I/O errors are written to stderr so that debug output failures are visible.
func (e *MarkdownExecutor) appendJSONL(path string, obj map[string]any) {
	data, err := json.Marshal(obj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "markdown executor: debug marshal error for %s: %v\n", path, err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "markdown executor: debug write error for %s: %v\n", path, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "markdown executor: debug write error for %s: %v\n", path, err)
	}
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

// limitPatterns lists substrings (lowercase) that identify a Claude usage-limit
// error. The stream result and stderr fallback paths both use this list.
var limitPatterns = []string{
	"hit your limit",
	"out of extra usage",
}

// isLimitMessage returns true when msg (case-insensitive) matches any known
// Claude usage-limit pattern.
func isLimitMessage(msg string) bool {
	lower := strings.ToLower(msg)
	for _, pat := range limitPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
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
	return isLimitMessage(result)
}
