package executors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vector76/raymond/internal/backend"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/policy"
	"github.com/vector76/raymond/internal/prompts"
	wfstate "github.com/vector76/raymond/internal/state"
)

// maxReminderAttempts is the maximum number of reminder prompts before giving up.
const maxReminderAttempts = 3

// MarkdownExecutor handles .md states by invoking the configured agent
// backend.
//
//   - Loads and renders the prompt template.
//   - Invokes the backend with an optional reminder loop (up to
//     maxReminderAttempts) when no valid transition is found.
//   - Records the session id, cost, and token counts returned by each
//     turn.
//   - Emits the full set of observer events.
type MarkdownExecutor struct{}

// Execute runs the markdown state and returns the parsed transition.
func (e *MarkdownExecutor) Execute(
	ctx context.Context,
	agent *wfstate.AgentState,
	wfState *wfstate.WorkflowState,
	execCtx *ExecutionContext,
) (ExecutionResult, error) {
	// Wrap with a cancel so the backend exits cleanly whenever Execute
	// returns early (error, budget exceeded, etc.).
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
		variables["input"] = *agent.PendingResult
	}
	if agent.PendingAskID != "" {
		variables["ask_id"] = agent.PendingAskID
	}
	for k, v := range agent.ForkAttributes {
		variables[k] = v
	}
	variables["workflow_id"] = wfState.WorkflowID
	variables["agent_id"] = agentID
	variables["task_folder"] = agent.TaskFolder
	basePrompt := prompts.RenderPrompt(body, variables)

	// Determine model and effort (frontmatter takes precedence over defaults).
	// The active backend is resolved first because the fallback differs:
	// Claude has a predictable "sonnet" baseline that avoids relying on
	// ~/.claude/settings.json, but pi has its own configured default model
	// and accepts pi-native model patterns — injecting "sonnet" sends pi
	// fuzzy-matching against the wrong provider.
	activeBackend := execCtx.Backend
	if activeBackend == nil {
		activeBackend = backend.NewClaudeBackend()
	}
	_, isClaude := activeBackend.(*backend.ClaudeBackend)

	model := ""
	if isClaude {
		model = "sonnet"
	}
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

	var transition *parsing.Transition
	newSessionID := sessionID
	reminderAttempt := 0
	var stateTotalCost float64
	var lastInvocationTokens *int64
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

		// Build the Sink that bridges normalized backend events onto the
		// raymond event bus. Allocating per turn is cheap and keeps the
		// captured agent/state context lexically obvious.
		stepNumber := 0
		if execCtx.DebugDir != "" {
			stepNumber = execCtx.GetNextStepNumber(agentID)
		}
		sink := backend.Sink{
			OnProgress: func(text string) {
				execCtx.Bus.Emit(events.ProgressMessage{
					AgentID:   agentID,
					Message:   text,
					Timestamp: time.Now(),
				})
			},
			OnToolUse: func(name, detail string) {
				execCtx.Bus.Emit(events.ToolInvocation{
					AgentID:   agentID,
					ToolName:  name,
					Detail:    detail,
					Timestamp: time.Now(),
				})
			},
			OnToolError: func(msg string) {
				execCtx.Bus.Emit(events.ErrorOccurred{
					AgentID:      agentID,
					ErrorType:    "ToolError",
					ErrorMessage: msg,
					IsRetryable:  false,
					Timestamp:    time.Now(),
				})
			},
			OnPrint: func(text string) {
				execCtx.Bus.Emit(events.PrintOutput{
					AgentID:   agentID,
					Content:   text,
					Timestamp: time.Now(),
				})
			},
		}
		if stepNumber > 0 {
			// stepNumber is declared with := inside the loop body, so each
			// iteration gets its own variable; the closure captures the
			// per-iteration value safely.
			sink.OnRaw = func(obj map[string]any) {
				execCtx.Bus.Emit(events.ClaudeStreamOutput{
					AgentID:    agentID,
					StateName:  currentState,
					StepNumber: stepNumber,
					JSONObject: obj,
					Timestamp:  time.Now(),
				})
			}
		}

		// Run one turn.
		turnResult, runErr := activeBackend.RunTurn(ctx, backend.TurnSpec{
			Prompt:                     prompt,
			Model:                      model,
			Effort:                     effort,
			SessionID:                  useSessionID,
			Fork:                       useFork,
			ContinueLatest:             useContinue,
			Cwd:                        agent.Cwd,
			IdleTimeout:                execCtx.Timeout,
			DangerouslySkipPermissions: execCtx.DangerouslySkipPermissions,
		}, sink)

		if runErr != nil {
			return ExecutionResult{}, mapBackendError(runErr)
		}

		// Update session id from the turn (last non-empty wins).
		if turnResult.SessionID != "" {
			s := turnResult.SessionID
			newSessionID = &s
		}

		// Extract and accumulate cost.
		lastInvocationTokens = turnResult.InputTokens
		if turnResult.CostUSD > 0 {
			stateTotalCost += turnResult.CostUSD
			wfState.TotalCostUSD += turnResult.CostUSD
		}

		// Budget check. The check is intentionally performed after accumulating
		// the cost: a single invocation may push spend above the budget limit,
		// but that overage is bounded to one LLM call. Strict pre-invocation
		// checking would require estimating token costs upfront, which is not
		// reliably possible. The chosen approach keeps the logic simple and
		// predictable: every invocation runs to completion, then the budget is
		// evaluated.
		//
		// BudgetUSD == 0 means unlimited and skips this check entirely.
		if wfState.BudgetUSD > 0 && wfState.TotalCostUSD > wfState.BudgetUSD {
			budgetExceeded := parsing.Transition{
				Tag:     "result",
				Payload: fmt.Sprintf("Workflow terminated: budget exceeded ($%.4f > $%.4f)", wfState.TotalCostUSD, wfState.BudgetUSD),
			}
			transition = &budgetExceeded
			break
		}

		// Parse and validate transitions.
		allTrs, singleTr, doRetry, parseErr := e.parseAndValidate(
			turnResult.OutputText, pol, scopeDir,
			agentID, currentState,
			execCtx, reminderAttempt, variables,
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
		InputTokens:  lastInvocationTokens,
		Timestamp:    time.Now(),
	})

	return ExecutionResult{
		Transition: *transition,
		SessionID:  newSessionID,
		CostUSD:    stateTotalCost,
	}, nil
}

// mapBackendError translates backend.* error types into the executor's
// orchestrator-facing error types (which the orchestrator already
// switches on for pause/retry/limit handling).
func mapBackendError(err error) error {
	var te *backend.TimeoutError
	if errors.As(err, &te) {
		return &ClaudeCodeTimeoutWrappedError{
			Msg: fmt.Sprintf("Claude Code timeout: %v", te),
		}
	}
	var le *backend.LimitError
	if errors.As(err, &le) {
		return &ClaudeCodeLimitError{Msg: le.Msg}
	}
	var re *backend.RunError
	if errors.As(err, &re) {
		return &ClaudeCodeError{Msg: re.Msg}
	}
	// Unknown backend error: wrap conservatively.
	return &ClaudeCodeError{Msg: err.Error()}
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

// parseAndValidate parses and validates transitions from the assistant
// output text the backend produced for this turn.
//
// Returns:
//   - (all, nil, false, nil) when multi-fork is detected (all is the full list).
//   - (nil, single, false, nil) on single-transition success.
//   - (nil, nil, true, nil) when a reminder retry is needed.
//   - (nil, nil, false, err) on a fatal error.
func (e *MarkdownExecutor) parseAndValidate(
	outputText string,
	pol *policy.Policy,
	scopeDir string,
	agentID, currentState string,
	execCtx *ExecutionContext,
	reminderAttempt int,
	variables map[string]any,
) (all []parsing.Transition, single *parsing.Transition, retry bool, err error) {
	transitions, parseErr := parsing.ParseTransitions(outputText)
	if parseErr != nil {
		return nil, nil, false, fmt.Errorf("transition parse error: %w", parseErr)
	}

	// Multi-fork: pass the full list through without single-transition validation.
	if isMultiFork(transitions) {
		return transitions, nil, false, nil
	}

	// Implicit transition (no tag, policy has exactly one allowed transition with a target or a fixed-payload result).
	if len(transitions) == 0 && policy.CanUseImplicitTransition(pol) {
		implicit, implicitErr := policy.GetImplicitTransition(pol)
		if implicitErr != nil {
			return nil, nil, false, implicitErr
		}
		// Render the "input" attribute as a template so that {{input}} and
		// fork attributes are substituted before the transition is dispatched.
		if input, ok := implicit.Attributes["input"]; ok && input != "" {
			rendered := prompts.RenderPrompt(input, variables)
			if rendered != input {
				attrs := make(map[string]string, len(implicit.Attributes))
				for k, v := range implicit.Attributes {
					attrs[k] = v
				}
				attrs["input"] = rendered
				implicit.Attributes = attrs
			}
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

	// Normalize result payload: strip surrounding whitespace so callers receive
	// a clean value even when the LLM emits extra newlines around the content.
	if resolved.Tag == "result" {
		resolved.Payload = strings.TrimSpace(resolved.Payload)
	}

	return nil, &resolved, false, nil
}
