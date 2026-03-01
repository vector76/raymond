// Package orchestrator implements the main workflow execution loop for raymond.
//
// RunAllAgents is the primary entry point. It reads workflow state, runs each
// agent step sequentially (one agent at a time, cycling through all active
// agents per iteration), handles errors, emits events, and persists state
// after every step for crash-recovery.
//
// Concurrency note: the current implementation is sequential. When only one
// agent is active this matches the Python asyncio behaviour exactly. With
// multiple agents the Go version executes them round-robin rather than truly
// in parallel, which is correct for all existing tests. True goroutine-per-
// agent concurrency can be layered on top in a later phase.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/executors"
	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/specifier"
	wfstate "github.com/vector76/raymond/internal/state"
	"github.com/vector76/raymond/internal/transitions"
)

// MaxRetries is the number of retryable errors allowed before an agent is
// paused.  Matches the Python MAX_RETRIES constant.
const MaxRetries = 3

// executorFactory returns the StateExecutor for the given filename.
// Overridable in tests via export_test.go.
var executorFactory func(string) executors.StateExecutor = executors.GetExecutor

// busHook, if non-nil, is called immediately after the Bus is created.
// Overridable in tests to subscribe to events before the workflow runs.
var busHook func(*bus.Bus)

// RunOptions configures a RunAllAgents invocation.
type RunOptions struct {
	// StateDir is the directory that holds workflow state files.
	// Empty string uses the default location (.raymond/state/).
	StateDir string

	// Debug enables debug output (JSONL files, per-step stdout/stderr dumps).
	Debug bool

	// DefaultModel overrides the model when not specified in frontmatter.
	DefaultModel string

	// DefaultEffort overrides the effort level when not specified in frontmatter.
	DefaultEffort string

	// Timeout is the Claude Code invocation timeout in seconds. ≤ 0 means no limit.
	Timeout float64

	// DangerouslySkipPermissions passes --dangerously-skip-permissions to Claude.
	DangerouslySkipPermissions bool

	// Quiet suppresses console observer output.
	Quiet bool

	// NoWait disables automatic waiting for usage-limit reset; the workflow
	// is paused and exits immediately instead.
	NoWait bool

	// NoResetPaused skips the reset of paused agents on startup (used in tests
	// to simulate a mid-run resume without the initial status clearing).
	NoResetPaused bool

	// ObserverSetup, if non-nil, is called with the Bus immediately after it
	// is created (before WorkflowStarted is emitted). Use it to register
	// observers from production code (e.g. the CLI). Tests use SetBusHook
	// from export_test.go instead.
	ObserverSetup func(*bus.Bus)
}

// RunAllAgents executes the workflow identified by workflowID until all agents
// have terminated or the workflow is paused due to an unrecoverable error.
//
// The function:
//  1. Reads workflow state from disk.
//  2. Resets any agents that were previously paused (unless NoResetPaused).
//  3. Creates an EventBus and an ExecutionContext.
//  4. Loops: picks the first active (non-paused) agent, executes one step,
//     applies the transition, handles errors, and writes state for recovery.
//  5. Exits when no agents remain (workflow completed) or all are paused.
func RunAllAgents(ctx context.Context, workflowID string, opts RunOptions) error {
	stateDir := wfstate.GetStateDir(opts.StateDir)

	ws, err := wfstate.ReadState(workflowID, stateDir)
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}

	if !opts.NoResetPaused {
		resetPausedAgents(ws)
	}

	// Initialise transient map that is never persisted (json:"-") but must be
	// writable from the first HandleResult call.
	ws.AgentTerminationResults = make(map[string]string)

	// Create debug directory if debug is enabled.
	debugDir := ""
	if opts.Debug {
		var debugErr error
		debugDir, debugErr = createDebugDirectory(workflowID, stateDir)
		if debugErr != nil {
			// Non-fatal: warn but continue without debug output.
			fmt.Fprintf(os.Stderr, "warning: debug mode disabled: %v\n", debugErr)
		}
	}

	b := bus.New()
	if busHook != nil {
		busHook(b)
	}
	if opts.ObserverSetup != nil {
		opts.ObserverSetup(b)
	}

	execCtx := &executors.ExecutionContext{
		Bus:                        b,
		WorkflowID:                 workflowID,
		ScopeDir:                   ws.ScopeDir,
		DebugDir:                   debugDir,
		StateDir:                   stateDir,
		DefaultModel:               opts.DefaultModel,
		DefaultEffort:              opts.DefaultEffort,
		Timeout:                    opts.Timeout,
		DangerouslySkipPermissions: opts.DangerouslySkipPermissions,
	}

	b.Emit(events.WorkflowStarted{
		WorkflowID: workflowID,
		ScopeDir:   ws.ScopeDir,
		DebugDir:   debugDir,
		Timestamp:  time.Now(),
	})

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// --- Exit if no agents remain ---
		if len(ws.Agents) == 0 {
			b.Emit(events.WorkflowCompleted{
				WorkflowID:   workflowID,
				TotalCostUSD: ws.TotalCostUSD,
				Timestamp:    time.Now(),
			})
			_ = wfstate.DeleteState(workflowID, stateDir)
			return nil
		}

		// --- Check if all agents are paused ---
		if allPaused(ws.Agents) {
			if !opts.NoWait {
				// Attempt auto-wait: parse reset times from paused agents.
				if waitSec, ok := computeAutoWait(ws.Agents); ok {
					now := time.Now()
					resetTime := now.Add(time.Duration(float64(time.Second) * waitSec))
					b.Emit(events.WorkflowWaiting{
						WorkflowID:       workflowID,
						TotalCostUSD:     ws.TotalCostUSD,
						PausedAgentCount: len(ws.Agents),
						ResetTime:        resetTime,
						WaitSeconds:      waitSec,
						Timestamp:        now,
					})
					if err := wfstate.WriteState(workflowID, ws, stateDir); err != nil {
						return err
					}
					if waitSec > 0 {
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-time.After(time.Duration(waitSec * float64(time.Second))):
						}
					}
					b.Emit(events.WorkflowResuming{WorkflowID: workflowID, Timestamp: time.Now()})
					resetPausedAgents(ws)
					continue
				}
			}
			// Pause-and-exit.
			b.Emit(events.WorkflowPaused{
				WorkflowID:       workflowID,
				TotalCostUSD:     ws.TotalCostUSD,
				PausedAgentCount: len(ws.Agents),
				Timestamp:        time.Now(),
			})
			return wfstate.WriteState(workflowID, ws, stateDir)
		}

		// --- Find the first active (non-paused) agent ---
		agentIdx := firstActiveIndex(ws.Agents)
		if agentIdx < 0 {
			// Shouldn't happen — allPaused guard above would have caught this.
			break
		}

		// Copy the agent before stepping so we have the fromState for events.
		agentBefore := ws.Agents[agentIdx]

		// Use the current agent's ScopeDir for this step.
		execCtx.ScopeDir = ws.Agents[agentIdx].ScopeDir

		// --- Execute one step ---
		tr, stepErr := stepAgent(ctx, &ws.Agents[agentIdx], ws, execCtx, b)

		if stepErr != nil {
			if err := handleStepError(stepErr, agentIdx, ws, execCtx, b); err != nil {
				// Fatal or unhandled error — propagate up.
				return err
			}
		} else {
			// Apply the transition result.
			applyResult(tr, agentIdx, agentBefore.CurrentState, ws, b)
		}

		// --- Write state for crash recovery ---
		if err := wfstate.WriteState(workflowID, ws, stateDir); err != nil {
			return fmt.Errorf("write state: %w", err)
		}
	}
	return nil
}

// stepAgent executes one step for the agent at ws.Agents[agentIdx].
//
// It:
//  1. Invokes the executor for the agent's current state.
//  2. Updates session_id on the agent from the execution result (BEFORE transition).
//  3. Checks for multi-fork (multiple fork-family tags or fork+goto). If detected,
//     dispatches to applyMultiFork which directly appends workers to ws.Agents.
//  4. Otherwise calls transitions.ApplyTransition for the single-transition path.
//  5. Emits transition-related events (TransitionOccurred, AgentSpawned, AgentTerminated).
//
// Returns the TransitionResult (describing the new agent state) or an error.
func stepAgent(
	ctx context.Context,
	agent *wfstate.AgentState,
	ws *wfstate.WorkflowState,
	execCtx *executors.ExecutionContext,
	b *bus.Bus,
) (transitions.TransitionResult, error) {
	fromState := agent.CurrentState
	exec := executorFactory(fromState)

	execResult, err := exec.Execute(ctx, agent, ws, execCtx)
	if err != nil {
		return transitions.TransitionResult{}, err
	}

	// Apply the session_id from the execution result to the agent BEFORE handling
	// the transition. Transition handlers need to see the post-execution session_id:
	//   - function/call: save it in the return-stack frame (caller's session to
	//     restore later), so it must reflect what was actually used during execution.
	//   - reset/function: then replace it with nil for a fresh start.
	//   - result (return): pops the saved caller session from the stack.
	// Script states return the agent's existing session_id unchanged (they have no
	// Claude session of their own), so this update is a no-op for script states.
	if execResult.SessionID != nil {
		agent.SessionID = execResult.SessionID
	}

	// Multi-fork path: multiple fork-family tags or fork-family alongside goto.
	if isMultiForkTransitions(execResult.Transitions) {
		return applyMultiFork(agent, execResult.Transitions, ws, b, fromState)
	}

	// Single-transition path: apply transition (deep-copies agent, clears transients).
	tr, err := transitions.ApplyTransition(agent, execResult.Transition, ws)
	if err != nil {
		return transitions.TransitionResult{}, err
	}

	// Note: cost is already accumulated into ws.TotalCostUSD by the executor
	// (markdown executor adds directly; script executor contributes 0). We do
	// not add execResult.CostUSD here to avoid double-counting.

	// Emit TransitionOccurred.
	toState := ""
	if tr.Agent != nil {
		toState = tr.Agent.CurrentState
	}
	meta := map[string]any{"state_type": stateType(fromState)}
	if execResult.Transition.Tag == "result" {
		meta["result_payload"] = execResult.Transition.Payload
	}
	if tr.Worker != nil {
		meta["spawned_agent_id"] = tr.Worker.ID
	}
	b.Emit(events.TransitionOccurred{
		AgentID:        agent.ID,
		FromState:      fromState,
		ToState:        toState,
		TransitionType: execResult.Transition.Tag,
		Metadata:       meta,
		Timestamp:      time.Now(),
	})

	// Emit type-specific events.
	if tr.Agent == nil {
		// Agent terminated.
		payload := ""
		if ws.AgentTerminationResults != nil {
			payload = ws.AgentTerminationResults[agent.ID]
		}
		b.Emit(events.AgentTerminated{
			AgentID:       agent.ID,
			ResultPayload: payload,
			Timestamp:     time.Now(),
		})
	}
	if tr.Worker != nil {
		b.Emit(events.AgentSpawned{
			ParentAgentID: agent.ID,
			NewAgentID:    tr.Worker.ID,
			InitialState:  tr.Worker.CurrentState,
			Timestamp:     time.Now(),
		})
	}

	return tr, nil
}

// isMultiForkTransitions reports whether the transition list should be handled
// by the multi-fork path. Triggered when:
//   - There are 2+ fork-family tags (fork or fork-workflow), OR
//   - Any fork-family tag appears alongside a goto tag.
func isMultiForkTransitions(trs []parsing.Transition) bool {
	if len(trs) == 0 {
		return false
	}
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

// validateMultiFork checks the multi-fork transition list for consistency and
// returns the single agreed continuation target, or an error describing the violation.
//
// Validation rules:
//  1. At least one continuation: a fork tag has "next" OR a goto is present.
//  2. All "next" values across fork tags are identical.
//  3. If both goto and "next" are present, their targets agree.
//  4. At most one goto tag.
//  5. No non-fork-family, non-goto tags.
func validateMultiFork(trs []parsing.Transition) (continuation string, err error) {
	var nextValues []string
	var gotoTargets []string
	gotoCount := 0

	for _, t := range trs {
		switch t.Tag {
		case "fork", "fork-workflow":
			if next, ok := t.Attributes["next"]; ok {
				nextValues = append(nextValues, next)
			}
		case "goto":
			gotoCount++
			gotoTargets = append(gotoTargets, t.Target)
		default:
			return "", fmt.Errorf(
				"multi-fork: non-fork transition %q mixed with fork tags; "+
					"only fork, fork-workflow, and goto are allowed alongside fork tags",
				t.Tag,
			)
		}
	}

	// Rule 4: at most one goto.
	if gotoCount > 1 {
		return "", fmt.Errorf("multi-fork: at most one <goto> tag allowed, found %d", gotoCount)
	}

	// Gather all continuation targets.
	allTargets := append(nextValues, gotoTargets...) //nolint:gocritic

	// Rule 1: at least one continuation.
	if len(allTargets) == 0 {
		return "", fmt.Errorf(
			"multi-fork: no continuation target; " +
				"at least one fork tag must have a 'next' attribute or a <goto> tag must be present",
		)
	}

	// Rules 2 & 3: all targets must agree.
	first := allTargets[0]
	for _, target := range allTargets[1:] {
		if target != first {
			return "", fmt.Errorf(
				"multi-fork: conflicting continuation targets %q and %q; "+
					"all fork 'next' attributes and any <goto> must agree",
				first, target,
			)
		}
	}

	return first, nil
}

// applyMultiFork handles multi-fork outputs: validates, creates workers, advances
// the caller, appends workers to ws.Agents, and emits events.
//
// On validation failure the caller is paused and a TransitionResult with the
// paused agent is returned (no error). Workers are appended to ws.Agents before
// returning so applyResult only needs to update the caller in-place.
func applyMultiFork(
	agent *wfstate.AgentState,
	trs []parsing.Transition,
	ws *wfstate.WorkflowState,
	b *bus.Bus,
	fromState string,
) (transitions.TransitionResult, error) {
	continuation, valErr := validateMultiFork(trs)
	if valErr != nil {
		// Pause the caller with a descriptive error.
		agentCopy := *agent
		agentCopy.Status = wfstate.AgentStatusPaused
		agentCopy.Error = valErr.Error()
		b.Emit(events.AgentPaused{
			AgentID:   agent.ID,
			Reason:    "validation_error",
			Timestamp: time.Now(),
		})
		return transitions.TransitionResult{Agent: &agentCopy}, nil
	}

	// Deep-copy the caller to use as the basis for worker creation and advancement.
	callerCopy := deepCopyOrchestratorAgent(*agent)

	// Create workers for each fork-family tag.
	var workers []wfstate.AgentState
	for _, tr := range trs {
		switch tr.Tag {
		case "fork":
			w, err := transitions.CreateForkWorker(callerCopy, tr, ws)
			if err != nil {
				return transitions.TransitionResult{}, err
			}
			workers = append(workers, w)
		case "fork-workflow":
			res, err := specifier.Resolve(tr.Target, callerCopy.ScopeDir)
			if err != nil {
				agentCopy := *agent
				agentCopy.Status = wfstate.AgentStatusPaused
				agentCopy.Error = fmt.Sprintf("fork-workflow: %v", err)
				b.Emit(events.AgentPaused{
					AgentID:   agent.ID,
					Reason:    "validation_error",
					Timestamp: time.Now(),
				})
				return transitions.TransitionResult{Agent: &agentCopy}, nil
			}
			w, err := transitions.CreateForkWorkflowWorker(callerCopy, tr, ws, res)
			if err != nil {
				return transitions.TransitionResult{}, err
			}
			workers = append(workers, w)
		}
		// goto tags are skipped here; they only set the continuation target.
	}

	// Advance the caller to the continuation state.
	callerCopy.CurrentState = continuation
	callerCopy.PendingResult = nil
	callerCopy.ForkSessionID = nil
	callerCopy.ForkAttributes = nil

	// Append all workers to ws.Agents.
	for i := range workers {
		ws.Agents = append(ws.Agents, workers[i])
	}

	// Emit TransitionOccurred for the multi-fork.
	b.Emit(events.TransitionOccurred{
		AgentID:        agent.ID,
		FromState:      fromState,
		ToState:        continuation,
		TransitionType: "multi-fork",
		Metadata: map[string]any{
			"state_type":  stateType(fromState),
			"fork_count":  len(workers),
		},
		Timestamp: time.Now(),
	})

	// Emit AgentSpawned for each worker.
	for i := range workers {
		b.Emit(events.AgentSpawned{
			ParentAgentID: agent.ID,
			NewAgentID:    workers[i].ID,
			InitialState:  workers[i].CurrentState,
			Timestamp:     time.Now(),
		})
	}

	return transitions.TransitionResult{Agent: &callerCopy}, nil
}

// deepCopyOrchestratorAgent returns a deep copy of a for safe mutation in the
// orchestrator's multi-fork dispatch path.
func deepCopyOrchestratorAgent(a wfstate.AgentState) wfstate.AgentState {
	c := a // copies all value-type fields

	if a.SessionID != nil {
		s := *a.SessionID
		c.SessionID = &s
	}
	if a.PendingResult != nil {
		p := *a.PendingResult
		c.PendingResult = &p
	}
	if a.ForkSessionID != nil {
		fs := *a.ForkSessionID
		c.ForkSessionID = &fs
	}
	if len(a.ForkAttributes) > 0 {
		m := make(map[string]string, len(a.ForkAttributes))
		for k, v := range a.ForkAttributes {
			m[k] = v
		}
		c.ForkAttributes = m
	}
	if len(a.Stack) > 0 {
		newStack := make([]wfstate.StackFrame, len(a.Stack))
		copy(newStack, a.Stack)
		c.Stack = newStack
	}
	return c
}

// applyResult applies a successful TransitionResult to the workflow state.
func applyResult(
	tr transitions.TransitionResult,
	agentIdx int,
	_ string, // fromState (unused here, kept for symmetry)
	ws *wfstate.WorkflowState,
	_ *bus.Bus,
) {
	if tr.Agent == nil {
		// Terminated: remove agent from slice.
		ws.Agents = append(ws.Agents[:agentIdx], ws.Agents[agentIdx+1:]...)
	} else {
		// Update agent in-place.
		updated := *tr.Agent
		updated.RetryCount = 0 // clear retry counter on success
		ws.Agents[agentIdx] = updated
	}
	if tr.Worker != nil {
		ws.Agents = append(ws.Agents, *tr.Worker)
	}
}

// handleStepError classifies err and applies the appropriate error-handling
// policy (retry, pause, or fatal propagation).
//
// Returns nil if the error was handled (the workflow loop should continue),
// or a non-nil error if the error is fatal and should terminate RunAllAgents.
func handleStepError(
	err error,
	agentIdx int,
	ws *wfstate.WorkflowState,
	execCtx *executors.ExecutionContext,
	b *bus.Bus,
) error {
	agent := &ws.Agents[agentIdx]

	switch {
	case isLimitError(err):
		// Usage limit hit — pause immediately without retrying.
		b.Emit(events.ErrorOccurred{
			AgentID:      agent.ID,
			ErrorType:    "ClaudeCodeLimitError",
			ErrorMessage: err.Error(),
			CurrentState: agent.CurrentState,
			IsRetryable:  false,
			RetryCount:   0,
			MaxRetries:   0,
			Timestamp:    time.Now(),
		})
		agent.Status = wfstate.AgentStatusPaused
		agent.Error = err.Error()
		b.Emit(events.AgentPaused{AgentID: agent.ID, Reason: events.PauseReasonUsageLimit, Timestamp: time.Now()})
		return nil

	case isRetryableError(err):
		// Transient error — retry up to MaxRetries.
		agent.RetryCount++
		retryable := agent.RetryCount < MaxRetries

		b.Emit(events.ErrorOccurred{
			AgentID:      agent.ID,
			ErrorType:    errorTypeName(err),
			ErrorMessage: err.Error(),
			CurrentState: agent.CurrentState,
			IsRetryable:  retryable,
			RetryCount:   agent.RetryCount,
			MaxRetries:   MaxRetries,
			Timestamp:    time.Now(),
		})

		if !retryable {
			// Exceeded max retries — pause.
			agent.Status = wfstate.AgentStatusPaused
			agent.Error = err.Error()
			reason := agentPausedReason(err)
			b.Emit(events.AgentPaused{AgentID: agent.ID, Reason: reason, Timestamp: time.Now()})
		}
		return nil

	default:
		// Fatal error (ScriptError, unexpected, etc.) — propagate.
		return err
	}
}

// resetPausedAgents clears the status/retry/error fields of all paused agents
// so they will run again. Called on resume.
func resetPausedAgents(ws *wfstate.WorkflowState) {
	for i := range ws.Agents {
		if ws.Agents[i].Status == wfstate.AgentStatusPaused {
			ws.Agents[i].Status = ""
			ws.Agents[i].RetryCount = 0
			ws.Agents[i].Error = ""
		}
	}
}

// allPaused reports whether every agent in the slice has status "paused".
func allPaused(agents []wfstate.AgentState) bool {
	if len(agents) == 0 {
		return false
	}
	for _, a := range agents {
		if a.Status != wfstate.AgentStatusPaused {
			return false
		}
	}
	return true
}

// firstActiveIndex returns the index of the first non-paused agent,
// or -1 if all are paused.
func firstActiveIndex(agents []wfstate.AgentState) int {
	for i, a := range agents {
		if a.Status != wfstate.AgentStatusPaused {
			return i
		}
	}
	return -1
}

// isLimitError reports whether err is a ClaudeCodeLimitError.
func isLimitError(err error) bool {
	var le *executors.ClaudeCodeLimitError
	return errors.As(err, &le)
}

// isTimeoutError reports whether err is a ClaudeCodeTimeoutWrappedError.
func isTimeoutError(err error) bool {
	var te *executors.ClaudeCodeTimeoutWrappedError
	return errors.As(err, &te)
}

// isRetryableError reports whether err should trigger the retry mechanism.
// Retryable: ClaudeCodeError, ClaudeCodeTimeoutWrappedError, PromptFileError.
// NOT retryable: ScriptError (fatal).
func isRetryableError(err error) bool {
	var ce *executors.ClaudeCodeError
	var te *executors.ClaudeCodeTimeoutWrappedError
	var pe *executors.PromptFileError
	return errors.As(err, &ce) || errors.As(err, &te) || errors.As(err, &pe)
}

// agentPausedReason returns a short reason string for AgentPaused events that
// distinguishes between timeout, prompt file errors, and Claude invocation errors.
func agentPausedReason(err error) string {
	var te *executors.ClaudeCodeTimeoutWrappedError
	if errors.As(err, &te) {
		return events.PauseReasonTimeout
	}
	var pe *executors.PromptFileError
	if errors.As(err, &pe) {
		return events.PauseReasonPromptError
	}
	return events.PauseReasonClaudeError
}

// errorTypeName returns a short type name string for use in ErrorOccurred events.
func errorTypeName(err error) string {
	var ce *executors.ClaudeCodeError
	if errors.As(err, &ce) {
		return "ClaudeCodeError"
	}
	var te *executors.ClaudeCodeTimeoutWrappedError
	if errors.As(err, &te) {
		return "ClaudeCodeTimeoutWrappedError"
	}
	var pe *executors.PromptFileError
	if errors.As(err, &pe) {
		return "PromptFileError"
	}
	return fmt.Sprintf("%T", err)
}

// stateType returns events.StateTypeScript for .sh/.bat files and
// events.StateTypeMarkdown for everything else.
func stateType(filename string) string {
	lower := strings.ToLower(filename)
	if strings.HasSuffix(lower, ".sh") || strings.HasSuffix(lower, ".bat") {
		return events.StateTypeScript
	}
	return events.StateTypeMarkdown
}

// computeAutoWait inspects all paused agents for limit-reset times and returns
// the longest wait in seconds plus true, or (0, false) if any agent has no
// parseable reset time.
func computeAutoWait(agents []wfstate.AgentState) (float64, bool) {
	var maxWait float64
	for _, a := range agents {
		if a.Status != wfstate.AgentStatusPaused {
			continue
		}
		wait, ok := parseLimitResetWait(a.Error)
		if !ok {
			return 0, false
		}
		if wait > maxWait {
			maxWait = wait
		}
	}
	return maxWait, true
}

// limitResetBufferMinutes is added after the stated reset time.
const limitResetBufferMinutes = 5

// parseLimitResetWait attempts to parse a usage-limit reset time from an error
// message and returns the seconds to wait (plus buffer).
//
// The error message format expected by Claude Code is:
//
//	"… resets HH(am|pm) (Timezone/Name) …"
//
// Returns (0, false) when the message cannot be parsed or the timezone is unknown.
func parseLimitResetWait(msg string) (float64, bool) {
	// Delegate to the standalone helper so the logic is easy to unit-test.
	secs, ok := parseResetWaitSeconds(msg, time.Now(), limitResetBufferMinutes)
	return secs, ok
}

// createDebugDirectory creates and returns the path to the per-workflow debug
// directory (.raymond/debug/<workflowID>/). Returns ("", error) on failure.
func createDebugDirectory(workflowID, stateDir string) (string, error) {
	// stateDir is typically "<root>/.raymond/state"; go up one level to get
	// <root>/.raymond/, then create debug/<workflowID>/.
	raymondDir := filepath.Dir(stateDir)
	dir := filepath.Join(raymondDir, "debug", workflowID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create debug directory %s: %w", dir, err)
	}
	return dir, nil
}
