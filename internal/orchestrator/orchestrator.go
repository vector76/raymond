// Package orchestrator implements the main workflow execution loop for raymond.
//
// RunAllAgents is the primary entry point. It reads workflow state, runs each
// agent concurrently (one goroutine per active agent), handles results from a
// channel, applies transitions, and persists state after every step for
// crash-recovery.
//
// Concurrency model: each active agent runs its executor in its own goroutine.
// Goroutines send stepResult values to a shared channel. The main goroutine
// receives results one at a time, applies transitions (which may add new worker
// agents), writes state, and relaunches goroutines for any active agents that
// do not yet have one running. This gives true parallelism: multiple Claude
// Code processes can run simultaneously.
//
// Shared-state safety: the WorkflowState (ws) is only accessed by the main
// goroutine. Each agent goroutine receives its own deep copy of the agent and
// a lightweight local snapshot of the workflow state needed by the executor
// (WorkflowID, BudgetUSD, TotalCostUSD). Cost returned by each goroutine is
// accumulated into ws by the main goroutine.
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
	"github.com/vector76/raymond/internal/registry"
	"github.com/vector76/raymond/internal/specifier"
	wfstate "github.com/vector76/raymond/internal/state"
	"github.com/vector76/raymond/internal/transitions"
	"github.com/vector76/raymond/internal/yamlscope"
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

	// Fetcher is the function used to download remote workflow zip files.
	// If nil, a registry.Registry rooted at the state directory's parent is used.
	Fetcher specifier.Fetcher

	// ObserverSetup, if non-nil, is called with the Bus immediately after it
	// is created (before WorkflowStarted is emitted). Use it to register
	// observers from production code (e.g. the CLI). Tests use SetBusHook
	// from export_test.go instead.
	ObserverSetup func(*bus.Bus)
}

// stepResult is sent by an agent goroutine when its executor call completes.
type stepResult struct {
	agentID    string
	execResult executors.ExecutionResult
	err        error
}

// RunAllAgents executes the workflow identified by workflowID until all agents
// have terminated or the workflow is paused due to an unrecoverable error.
//
// Agents run concurrently: each active agent has a goroutine executing its
// current state. The main goroutine collects results via a channel, applies
// transitions (potentially spawning new workers), writes state for crash
// recovery, and launches goroutines for any new or reactivated agents.
func RunAllAgents(ctx context.Context, workflowID string, opts RunOptions) error {
	stateDir := wfstate.GetStateDir(opts.StateDir)

	// Resolve the fetcher: use the injected one or construct from the registry.
	fetch := opts.Fetcher
	if fetch == nil {
		raymondDir := filepath.Dir(stateDir)
		reg := registry.New(raymondDir)
		fetch = reg.Fetch
	}

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

	execCtx := executors.NewExecutionContext()
	execCtx.Bus = b
	execCtx.WorkflowID = workflowID
	execCtx.DebugDir = debugDir
	execCtx.StateDir = stateDir
	execCtx.DefaultModel = opts.DefaultModel
	execCtx.DefaultEffort = opts.DefaultEffort
	execCtx.Timeout = opts.Timeout
	execCtx.DangerouslySkipPermissions = opts.DangerouslySkipPermissions

	b.Emit(events.WorkflowStarted{
		WorkflowID: workflowID,
		ScopeDir:   ws.ScopeDir,
		DebugDir:   debugDir,
		Timestamp:  time.Now(),
	})

	// Cancel context on exit so any still-running goroutines are signalled to stop.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Buffered channel so goroutines can send results without blocking.
	resultCh := make(chan stepResult, 64)

	// running tracks agent IDs that currently have goroutines executing.
	// Only accessed from the main goroutine.
	running := make(map[string]bool)

	// launch starts an executor goroutine for the given agent. It takes a
	// snapshot of the mutable workflow fields needed by the executor (to avoid
	// data races: ws is only accessed by the main goroutine).
	launch := func(agent wfstate.AgentState) {
		running[agent.ID] = true
		agentCopy := deepCopyOrchestratorAgent(agent)
		localWS := &wfstate.WorkflowState{
			WorkflowID:   ws.WorkflowID,
			TotalCostUSD: ws.TotalCostUSD,
			BudgetUSD:    ws.BudgetUSD,
			ScopeDir:     ws.ScopeDir,
		}
		exec := executorFactory(agentCopy.CurrentState)

		// Compute the effective timeout for this state.
		effectiveTimeout := execCtx.Timeout
		if yamlscope.IsYamlScope(localWS.ScopeDir) {
			stateName := executors.ExtractStateName(agentCopy.CurrentState)
			perStateTimeout, err := yamlscope.GetStateTimeout(localWS.ScopeDir, stateName)
			if err != nil {
				resultCh <- stepResult{
					agentID: agentCopy.ID,
					err:     err,
				}
				return
			}
			if perStateTimeout != nil {
				effectiveTimeout = *perStateTimeout
			}
		}

		// Create a per-launch copy of execCtx with the effective timeout.
		launchCtx := *execCtx
		launchCtx.Timeout = effectiveTimeout

		go func() {
			execResult, execErr := exec.Execute(ctx, &agentCopy, localWS, &launchCtx)
			resultCh <- stepResult{
				agentID:    agentCopy.ID,
				execResult: execResult,
				err:        execErr,
			}
		}()
	}

	// launchActive launches goroutines for all active agents that don't already
	// have one running. Called after every state change to ensure all active
	// agents (including newly spawned workers) are running.
	launchActive := func() {
		for i, a := range ws.Agents {
			if a.Status != wfstate.AgentStatusPaused && !running[a.ID] {
				launch(ws.Agents[i])
			}
		}
	}

	// Start: launch goroutines for all initial active agents.
	launchActive()

	for {
		// When no goroutines are in flight, evaluate terminal conditions.
		if len(running) == 0 {
			if len(ws.Agents) == 0 {
				b.Emit(events.WorkflowCompleted{
					WorkflowID:   workflowID,
					TotalCostUSD: ws.TotalCostUSD,
					Timestamp:    time.Now(),
				})
				_ = wfstate.DeleteState(workflowID, stateDir)
				return nil
			}

			if allPaused(ws.Agents) {
				if !opts.NoWait {
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
						launchActive()
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

			// Active agents with no goroutines: should not be reachable since
			// launchActive() is called before every check. Return an error
			// rather than silently exiting with nil.
			return fmt.Errorf("internal error: %d agents remain but no goroutines are running", len(ws.Agents))
		}

		// Wait for the next goroutine result.
		select {
		case <-ctx.Done():
			return ctx.Err()

		case result := <-resultCh:
			delete(running, result.agentID)

			agentIdx := findAgentByID(ws.Agents, result.agentID)
			if agentIdx < 0 {
				// Agent was already removed (shouldn't happen).
				launchActive()
				continue
			}

			agentBefore := ws.Agents[agentIdx]

			if result.err != nil {
				if fatalErr := handleStepError(result.err, agentIdx, ws, execCtx, b); fatalErr != nil {
					return fatalErr
				}
				// handleStepError updated ws.Agents[agentIdx] in place.
				// launchActive below will relaunch the agent if still active.
			} else {
				// Apply execResult.SessionID to agent BEFORE transition (handlers
				// rely on the post-execution session ID; e.g. function pushes it
				// onto the stack, reset/result then overwrite it).
				if result.execResult.SessionID != nil {
					ws.Agents[agentIdx].SessionID = result.execResult.SessionID
				}

				// Clear ContinueAndFork: the executor consumed it in its copy;
				// we must clear it in persistent state so it does not fire again
				// on resume.
				ws.Agents[agentIdx].ContinueAndFork = false

				// Accumulate cost into the shared total.
				ws.TotalCostUSD += result.execResult.CostUSD

				// Apply the transition.
				var tr transitions.TransitionResult
				var transErr error

				if isMultiForkTransitions(result.execResult.Transitions) {
					tr, transErr = applyMultiFork(&ws.Agents[agentIdx], result.execResult.Transitions, ws, b, agentBefore.CurrentState, fetch)
				} else {
					tr, transErr = transitions.ApplyTransition(&ws.Agents[agentIdx], result.execResult.Transition, ws, fetch)
					if transErr == nil {
						emitTransitionEvents(tr, agentBefore, result.execResult, ws, b)
					}
				}
				if transErr != nil {
					return transErr
				}

				// Apply transition result: update/remove agent in ws.Agents,
				// append worker if present.
				applyResult(tr, agentIdx, agentBefore.CurrentState, ws, b)
			}

			// Launch goroutines for all active agents that don't have one yet
			// (includes any new workers just appended to ws.Agents).
			launchActive()

			// Write state for crash recovery.
			if err := wfstate.WriteState(workflowID, ws, stateDir); err != nil {
				return fmt.Errorf("write state: %w", err)
			}
		}
	}
	return nil
}

// findAgentByID returns the index of the agent with the given ID, or -1.
func findAgentByID(agents []wfstate.AgentState, id string) int {
	for i, a := range agents {
		if a.ID == id {
			return i
		}
	}
	return -1
}

// firstActiveIndex returns the index of the first non-paused agent, or -1.
func firstActiveIndex(agents []wfstate.AgentState) int {
	for i, a := range agents {
		if a.Status != wfstate.AgentStatusPaused {
			return i
		}
	}
	return -1
}

// emitTransitionEvents emits TransitionOccurred, AgentTerminated, and
// AgentSpawned events for the single-transition path. (The multi-fork path
// emits its own events inside applyMultiFork.)
func emitTransitionEvents(
	tr transitions.TransitionResult,
	agentBefore wfstate.AgentState,
	execResult executors.ExecutionResult,
	ws *wfstate.WorkflowState,
	b *bus.Bus,
) {
	toState := ""
	if tr.Agent != nil {
		toState = tr.Agent.CurrentState
	}
	meta := map[string]any{"state_type": stateType(agentBefore.CurrentState)}
	if execResult.Transition.Tag == "result" {
		meta["result_payload"] = execResult.Transition.Payload
	}
	if tr.Worker != nil {
		meta["spawned_agent_id"] = tr.Worker.ID
	}
	b.Emit(events.TransitionOccurred{
		AgentID:        agentBefore.ID,
		FromState:      agentBefore.CurrentState,
		ToState:        toState,
		TransitionType: execResult.Transition.Tag,
		Metadata:       meta,
		Timestamp:      time.Now(),
	})

	if tr.Agent == nil {
		// Agent terminated.
		payload := ""
		if ws.AgentTerminationResults != nil {
			payload = ws.AgentTerminationResults[agentBefore.ID]
		}
		b.Emit(events.AgentTerminated{
			AgentID:       agentBefore.ID,
			ResultPayload: payload,
			Timestamp:     time.Now(),
		})
	}
	if tr.Worker != nil {
		b.Emit(events.AgentSpawned{
			ParentAgentID: agentBefore.ID,
			NewAgentID:    tr.Worker.ID,
			InitialState:  tr.Worker.CurrentState,
			Timestamp:     time.Now(),
		})
	}
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
	fetch specifier.Fetcher,
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
			var res specifier.Resolution
			var resErr error
			if callerCopy.ScopeURL != "" {
				res, resErr = specifier.ResolveFromURL(tr.Target, callerCopy.ScopeURL, fetch)
			} else {
				res, resErr = specifier.Resolve(tr.Target, callerCopy.ScopeDir)
			}
			if resErr != nil {
				agentCopy := *agent
				agentCopy.Status = wfstate.AgentStatusPaused
				agentCopy.Error = fmt.Sprintf("fork-workflow: %v", resErr)
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
			"state_type": stateType(fromState),
			"fork_count": len(workers),
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
// orchestrator's goroutine launch path.
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
		for i, frame := range a.Stack {
			newStack[i] = frame
			if frame.Session != nil {
				s := *frame.Session
				newStack[i].Session = &s
			}
		}
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
