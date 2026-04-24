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
	"github.com/vector76/raymond/internal/manifest"
	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/policy"
	"github.com/vector76/raymond/internal/registry"
	"github.com/vector76/raymond/internal/specifier"
	wfstate "github.com/vector76/raymond/internal/state"
	"github.com/vector76/raymond/internal/transitions"
	"github.com/vector76/raymond/internal/yamlscope"
	"github.com/vector76/raymond/internal/zipscope"
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

// AwaitInput carries a response to a pending await from the daemon layer.
type AwaitInput struct {
	InputID  string
	Response string
}

// AwaitingInputError is returned from RunAllAgents when the quiesce point is
// reached with OnAwait="pause". It carries structured data describing the
// active await so the CLI can emit JSON and exit with code 2.
type AwaitingInputError struct {
	Status       string                `json:"status"`
	RunID        string                `json:"run_id"`
	Workflow     string                `json:"workflow"`
	Awaiting     AwaitingInputDetail   `json:"awaiting"`
	PendingCount int                   `json:"pending_count"`
	Resume       string                `json:"resume"`
}

// AwaitingInputDetail describes the active await within an AwaitingInputError.
type AwaitingInputDetail struct {
	InputID string `json:"input_id"`
	AgentID string `json:"agent_id"`
	Prompt  string `json:"prompt"`
}

func (e *AwaitingInputError) Error() string {
	return fmt.Sprintf("workflow %q is awaiting input (input_id=%s)", e.RunID, e.Awaiting.InputID)
}

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

	// TaskFolderPattern overrides the task folder location pattern.
	// Supports {{workflow_id}} and {{agent_id}} template variables.
	// Empty string uses the default (.raymond/tasks/{{workflow_id}}/{{agent_id}}).
	TaskFolderPattern string

	// Fetcher is the function used to download remote workflow zip files.
	// If nil, a registry.Registry rooted at the state directory's parent is used.
	Fetcher specifier.Fetcher

	// OnAwait controls behaviour when a workflow declares or produces <await>
	// transitions. "pause" allows awaiting; "reject" (default when empty)
	// rejects at launch time and at runtime.
	OnAwait string

	// DaemonMode changes await behaviour: when true the orchestrator does NOT
	// quiesce sibling agents when one hits <await>. Instead it calls
	// AwaitCallback and keeps running. The awaiting agent stays idle until
	// input arrives on AwaitInputCh.
	DaemonMode bool

	// AwaitCallback is called (from the main goroutine) when an agent enters
	// <await> in daemon mode. It notifies the daemon layer of the new await.
	AwaitCallback func(agentID, inputID, prompt, nextState string)

	// AwaitInputCh delivers responses to pending awaits in daemon mode. The
	// orchestrator reads from this channel in its main select loop and
	// resumes the matching agent.
	AwaitInputCh <-chan AwaitInput

	// AwaitInput is the --input value provided on --resume. When non-empty
	// and agents are in the awaiting state, this value is delivered to the
	// active await agent before the main loop starts.
	AwaitInput string

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
	raymondDir := filepath.Dir(stateDir)

	// Resolve the fetcher: use the injected one or construct from the registry.
	fetch := opts.Fetcher
	if fetch == nil {
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

	// Launch-time check: if OnAwait is "reject" (or empty, which defaults to
	// reject), determine whether the workflow requires human input and reject
	// early with a helpful error. Skipped when DaemonMode is true because
	// daemon mode handles awaits natively.
	//
	// When the scope has a workflow manifest, use ResolveRequiresHumanInput
	// (which honours the manifest's requires_human_input field and follows
	// cross-workflow references transitively). Otherwise fall back to the
	// simple frontmatter scan.
	if opts.OnAwait != "pause" && !opts.DaemonMode {
		manifestUsed := false
		if manifestPath, ok := manifest.FindManifest(ws.ScopeDir); ok {
			m, parseErr := manifest.ParseManifest(manifestPath)
			if parseErr != nil && !errors.Is(parseErr, manifest.ErrNotManifest) {
				return fmt.Errorf("parse manifest: %w", parseErr)
			}
			if parseErr == nil {
				requiresHuman, scanErr := manifest.ResolveRequiresHumanInput(m, ws.ScopeDir, fetch)
				if scanErr != nil {
					return fmt.Errorf("resolve requires_human_input: %w", scanErr)
				}
				if requiresHuman {
					return fmt.Errorf(
						"workflow %q requires human input. "+
							"Use --on-await=pause to allow awaiting, or use `raymond serve` for interactive workflows",
						workflowID,
					)
				}
				manifestUsed = true
			}
			// ErrNotManifest (YAML scope file named workflow.yaml): fall
			// through to the frontmatter scan below.
		}
		if !manifestUsed {
			awaitStates, scanErr := scanForAwaitTransitions(ws.ScopeDir)
			if scanErr != nil {
				return fmt.Errorf("scan for await transitions: %w", scanErr)
			}
			if len(awaitStates) > 0 {
				return fmt.Errorf(
					"workflow %q declares <await> transitions in state(s): %s. "+
						"Use --on-await=pause to allow awaiting, or use `raymond serve` for interactive workflows",
					workflowID, strings.Join(awaitStates, ", "),
				)
			}
		}
	}

	// Initialise transient map that is never persisted (json:"-") but must be
	// writable from the first HandleResult call.
	ws.AgentTerminationResults = make(map[string]string)

	// Resolve and store the task folder pattern.
	pattern := opts.TaskFolderPattern
	if pattern == "" {
		pattern = filepath.Join(raymondDir, "tasks", "{{workflow_id}}", "{{agent_id}}")
	}
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(raymondDir, pattern)
	}
	ws.TaskFolderPattern = pattern

	if err := initTaskFolders(ws); err != nil {
		return fmt.Errorf("init task folders: %w", err)
	}

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

	// Resume-from-await: when resuming a paused workflow, detect agents in
	// the awaiting state and either deliver input or re-present the prompt.
	if hasAwaitingAgents(ws.Agents) {
		if opts.AwaitInput == "" {
			// No input provided — re-present the active await's prompt so
			// the caller can see what's pending.
			activeAgent, pendingCount := firstAwaitingAndCount(ws.Agents)
			return &AwaitingInputError{
				Status:   "awaiting_input",
				RunID:    workflowID,
				Workflow: filepath.Base(ws.ScopeDir),
				Awaiting: AwaitingInputDetail{
					InputID: activeAgent.AwaitInputID,
					AgentID: activeAgent.ID,
					Prompt:  activeAgent.AwaitPrompt,
				},
				PendingCount: pendingCount - 1,
				Resume:       fmt.Sprintf("raymond --resume %s --input \"[your response]\"", workflowID),
			}
		}

		// Deliver input to the first awaiting agent.
		activeIdx := firstAwaitingIndex(ws.Agents)
		a := &ws.Agents[activeIdx]
		awaitInput := opts.AwaitInput
		inputID := a.AwaitInputID
		a.PendingResult = &awaitInput
		a.CurrentState = a.AwaitNextState
		a.AwaitPrompt = ""
		a.AwaitNextState = ""
		a.AwaitTimeout = ""
		a.AwaitTimeoutNext = ""
		a.AwaitInputID = ""
		a.Status = ""

		b.Emit(events.AgentAwaitResumed{
			AgentID:   a.ID,
			InputID:   inputID,
			Timestamp: time.Now(),
		})

		// Check for remaining awaiting agents (pre-await queue).
		if nextAgent, remaining := firstAwaitingAndCount(ws.Agents); nextAgent != nil {
			// More awaiting agents remain. Persist the delivery and return
			// the next agent's prompt — no agents proceed yet.
			if err := wfstate.WriteState(workflowID, ws, stateDir); err != nil {
				return fmt.Errorf("write state: %w", err)
			}
			return &AwaitingInputError{
				Status:   "awaiting_input",
				RunID:    workflowID,
				Workflow: filepath.Base(ws.ScopeDir),
				Awaiting: AwaitingInputDetail{
					InputID: nextAgent.AwaitInputID,
					AgentID: nextAgent.ID,
					Prompt:  nextAgent.AwaitPrompt,
				},
				PendingCount: remaining - 1,
				Resume:       fmt.Sprintf("raymond --resume %s --input \"[your response]\"", workflowID),
			}
		}

		// No more awaiting agents — persist the delivery and let all agents
		// proceed together in the normal main loop below.
		if err := wfstate.WriteState(workflowID, ws, stateDir); err != nil {
			return fmt.Errorf("write state: %w", err)
		}
	} else if opts.AwaitInput != "" {
		return fmt.Errorf(
			"no agents are awaiting input; " +
				"the --input flag on --resume is only valid when a workflow is paused at an <await> point",
		)
	}

	// Buffered channel so goroutines can send results without blocking.
	resultCh := make(chan stepResult, 64)

	// running tracks agent IDs that currently have goroutines executing.
	// Only accessed from the main goroutine.
	running := make(map[string]bool)

	// Quiesce state: when an agent hits <await> and OnAwait == "pause",
	// pausing prevents launching new agent goroutines so the system can
	// reach a clean quiesce point.
	pausing := false

	// Resolved once: the channel for daemon-mode await input delivery.
	// nil when not in daemon mode (nil channels block forever in select,
	// effectively disabling the case).
	daemonInputCh := awaitInputCh(opts)
	activeAwait := ""
	var preAwaitQueue []string

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
	// agents (including newly spawned workers) are running. Agents with any
	// non-empty status (paused, awaiting, failed) are skipped.
	launchActive := func() error {
		if err := initTaskFolders(ws); err != nil {
			return err
		}
		if pausing {
			return nil
		}
		for i, a := range ws.Agents {
			if a.Status == "" && !running[a.ID] {
				launch(ws.Agents[i])
			}
		}
		return nil
	}

	// Start: launch goroutines for all initial active agents.
	if err := launchActive(); err != nil {
		return err
	}

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

			// Daemon mode: if agents are awaiting input, don't exit.
			// Fall through to the select loop which will read from
			// AwaitInputCh and resume agents as input arrives.
			if opts.DaemonMode && hasAwaitingAgents(ws.Agents) {
				// fall through to select
			} else if pausing && opts.OnAwait == "pause" {
				// Quiesce point: all goroutines have drained while pausing was
				// in effect (at least one agent hit <await>). The remaining
				// agents are either awaiting or held at their next state
				// boundary. Write state and exit so the caller can serve the
				// await or resume later.
				b.Emit(events.WorkflowPaused{
					WorkflowID:       workflowID,
					TotalCostUSD:     ws.TotalCostUSD,
					PausedAgentCount: len(ws.Agents),
					Timestamp:        time.Now(),
				})
				if err := wfstate.WriteState(workflowID, ws, stateDir); err != nil {
					return err
				}

				// Find the active awaiting agent to populate the structured output.
				var activeAgent *wfstate.AgentState
				if activeAwait != "" {
					if idx := findAgentByID(ws.Agents, activeAwait); idx >= 0 {
						activeAgent = &ws.Agents[idx]
					}
				}
				if activeAgent == nil {
					// Fallback: find the first awaiting agent.
					for i := range ws.Agents {
						if ws.Agents[i].Status == wfstate.AgentStatusAwaiting {
							activeAgent = &ws.Agents[i]
							break
						}
					}
				}
				if activeAgent != nil {
					return &AwaitingInputError{
						Status:   "awaiting_input",
						RunID:    workflowID,
						Workflow: filepath.Base(ws.ScopeDir),
						Awaiting: AwaitingInputDetail{
							InputID: activeAgent.AwaitInputID,
							AgentID: activeAgent.ID,
							Prompt:  activeAgent.AwaitPrompt,
						},
						PendingCount: len(preAwaitQueue),
						Resume:       fmt.Sprintf("raymond --resume %s --input \"[your response]\"", workflowID),
					}
				}
				return nil
			} else if allPaused(ws.Agents) {
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
						if err := launchActive(); err != nil {
							return err
						}
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
			} else {
				// Active agents with no goroutines: should not be reachable since
				// launchActive() is called before every check. Return an error
				// rather than silently exiting with nil.
				return fmt.Errorf("internal error: %d agents remain but no goroutines are running", len(ws.Agents))
			}
		}

		// Wait for the next goroutine result or daemon-mode await input.
		select {
		case <-ctx.Done():
			return ctx.Err()

		case input := <-daemonInputCh:
			idx := findAwaitingAgent(ws.Agents, input.InputID)
			if idx < 0 {
				continue // unknown InputID; ignore
			}
			a := &ws.Agents[idx]
			pr := input.Response
			a.PendingResult = &pr
			a.CurrentState = a.AwaitNextState
			a.Status = ""
			a.AwaitPrompt = ""
			a.AwaitNextState = ""
			a.AwaitTimeout = ""
			a.AwaitTimeoutNext = ""
			a.AwaitInputID = ""
			b.Emit(events.AgentAwaitResumed{
				AgentID:   a.ID,
				InputID:   input.InputID,
				Timestamp: time.Now(),
			})
			if err := launchActive(); err != nil {
				return err
			}
			if err := wfstate.WriteState(workflowID, ws, stateDir); err != nil {
				return fmt.Errorf("write state: %w", err)
			}

		case result := <-resultCh:
			delete(running, result.agentID)

			agentIdx := findAgentByID(ws.Agents, result.agentID)
			if agentIdx < 0 {
				// Agent was already removed (shouldn't happen).
				if err := launchActive(); err != nil {
					return err
				}
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

				// Await handling: detect when an agent enters awaiting
				// status and apply the mode-specific strategy.
				if idx := findAgentByID(ws.Agents, agentBefore.ID); idx >= 0 && ws.Agents[idx].Status == wfstate.AgentStatusAwaiting {
					if opts.DaemonMode {
						// Daemon mode: siblings keep running. Notify
						// the daemon layer via callback; the agent
						// stays awaiting until input arrives on
						// AwaitInputCh.
						b.Emit(events.AgentAwaitStarted{
							AgentID:   ws.Agents[idx].ID,
							InputID:   ws.Agents[idx].AwaitInputID,
							Prompt:    ws.Agents[idx].AwaitPrompt,
							NextState: ws.Agents[idx].AwaitNextState,
							Timeout:   ws.Agents[idx].AwaitTimeout,
							Timestamp: time.Now(),
						})
						if opts.AwaitCallback != nil {
							opts.AwaitCallback(
								ws.Agents[idx].ID,
								ws.Agents[idx].AwaitInputID,
								ws.Agents[idx].AwaitPrompt,
								ws.Agents[idx].AwaitNextState,
							)
						}
					} else if opts.OnAwait == "pause" {
						// CLI pause mode: quiesce all agents. The
						// first agent to await is the active await;
						// subsequent ones are queued.
						if activeAwait == "" {
							activeAwait = ws.Agents[idx].ID
						} else {
							preAwaitQueue = append(preAwaitQueue, ws.Agents[idx].ID)
						}
						pausing = true
						b.Emit(events.AgentAwaitStarted{
							AgentID:   ws.Agents[idx].ID,
							InputID:   ws.Agents[idx].AwaitInputID,
							Prompt:    ws.Agents[idx].AwaitPrompt,
							NextState: ws.Agents[idx].AwaitNextState,
							Timeout:   ws.Agents[idx].AwaitTimeout,
							Timestamp: time.Now(),
						})
					} else {
						// Runtime reject: fail the agent immediately.
						ws.Agents[idx].Status = wfstate.AgentStatusPaused
						ws.Agents[idx].Error = fmt.Sprintf(
							"agent %q produced <await> but --on-await=reject is in effect; "+
								"use --on-await=pause or `raymond serve`",
							agentBefore.ID,
						)
						ws.Agents[idx].AwaitPrompt = ""
						ws.Agents[idx].AwaitNextState = ""
						ws.Agents[idx].AwaitTimeout = ""
						ws.Agents[idx].AwaitTimeoutNext = ""
						ws.Agents[idx].AwaitInputID = ""
						b.Emit(events.AgentPaused{
							AgentID:   agentBefore.ID,
							Reason:    "await_rejected",
							Error:     ws.Agents[idx].Error,
							Timestamp: time.Now(),
						})
					}
				}
			}

			// Launch goroutines for all active agents that don't have one yet
			// (includes any new workers just appended to ws.Agents).
			if err := launchActive(); err != nil {
				return err
			}

			// Write state for crash recovery.
			if err := wfstate.WriteState(workflowID, ws, stateDir); err != nil {
				return fmt.Errorf("write state: %w", err)
			}
		}
	}
}

// initTaskFolders assigns and creates on-disk task folders for every agent in
// ws that does not already have one. Called at startup and before each
// launchActive to cover newly spawned workers.
func initTaskFolders(ws *wfstate.WorkflowState) error {
	for i := range ws.Agents {
		if ws.Agents[i].TaskFolder == "" {
			ws.Agents[i].TaskFolder = wfstate.ComputeTaskFolderPath(
				ws.TaskFolderPattern,
				ws.WorkflowID,
				ws.Agents[i].ID,
				ws.Agents[i].TaskCount,
			)
		}
		if err := os.MkdirAll(ws.Agents[i].TaskFolder, 0o755); err != nil {
			return fmt.Errorf("create task folder %s: %w", ws.Agents[i].TaskFolder, err)
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

// hasAwaitingAgents reports whether any agent in the slice has awaiting status.
func hasAwaitingAgents(agents []wfstate.AgentState) bool {
	for _, a := range agents {
		if a.Status == wfstate.AgentStatusAwaiting {
			return true
		}
	}
	return false
}

// firstAwaitingIndex returns the index of the first agent with awaiting status,
// or -1 if none exists.
func firstAwaitingIndex(agents []wfstate.AgentState) int {
	for i, a := range agents {
		if a.Status == wfstate.AgentStatusAwaiting {
			return i
		}
	}
	return -1
}

// firstAwaitingAndCount returns the first awaiting agent and the total count of
// awaiting agents. Returns (nil, 0) when none are awaiting.
func firstAwaitingAndCount(agents []wfstate.AgentState) (*wfstate.AgentState, int) {
	var first *wfstate.AgentState
	count := 0
	for i, a := range agents {
		if a.Status == wfstate.AgentStatusAwaiting {
			if first == nil {
				first = &agents[i]
			}
			count++
		}
	}
	return first, count
}

// findAwaitingAgent returns the index of the awaiting agent whose AwaitInputID
// matches inputID, or -1 if not found.
func findAwaitingAgent(agents []wfstate.AgentState, inputID string) int {
	for i, a := range agents {
		if a.Status == wfstate.AgentStatusAwaiting && a.AwaitInputID == inputID {
			return i
		}
	}
	return -1
}

// awaitInputCh returns opts.AwaitInputCh when daemon mode is active, or a nil
// channel (which blocks forever in select) when it is not.
func awaitInputCh(opts RunOptions) <-chan AwaitInput {
	if opts.DaemonMode && opts.AwaitInputCh != nil {
		return opts.AwaitInputCh
	}
	return nil
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
	} else if tr.Agent.Status == wfstate.AgentStatusPaused {
		// Resolution or validation failure (e.g. fork-workflow target not
		// found). The transition handler set agent.Error but did not emit
		// an event; surface it now so the live SSE stream can show why.
		b.Emit(events.AgentPaused{
			AgentID:   tr.Agent.ID,
			Reason:    "validation_error",
			Error:     tr.Agent.Error,
			Timestamp: time.Now(),
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
			Error:     agentCopy.Error,
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
					Error:     agentCopy.Error,
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
		b.Emit(events.AgentPaused{AgentID: agent.ID, Reason: events.PauseReasonUsageLimit, Error: agent.Error, Timestamp: time.Now()})
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
			b.Emit(events.AgentPaused{AgentID: agent.ID, Reason: reason, Error: agent.Error, Timestamp: time.Now()})
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

// allPaused reports whether every agent in the slice is quiesced (paused or
// awaiting external input). Returns false for an empty slice.
func allPaused(agents []wfstate.AgentState) bool {
	if len(agents) == 0 {
		return false
	}
	for _, a := range agents {
		if a.Status != wfstate.AgentStatusPaused && a.Status != wfstate.AgentStatusAwaiting {
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
// the longest wait in seconds plus true, or (0, false) if any paused agent has
// no parseable reset time or if no paused agents exist (e.g. all awaiting).
func computeAutoWait(agents []wfstate.AgentState) (float64, bool) {
	var maxWait float64
	found := false
	for _, a := range agents {
		if a.Status != wfstate.AgentStatusPaused {
			continue
		}
		found = true
		wait, ok := parseLimitResetWait(a.Error)
		if !ok {
			return 0, false
		}
		if wait > maxWait {
			maxWait = wait
		}
	}
	if !found {
		return 0, false
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

// scanForAwaitTransitions examines all state files in scopeDir for
// allowed_transitions entries with tag "await". Returns the list of state
// filenames that declare await transitions.
//
// Supports directory, zip, and YAML scopes.
func scanForAwaitTransitions(scopeDir string) ([]string, error) {
	if yamlscope.IsYamlScope(scopeDir) {
		return scanYamlForAwait(scopeDir)
	}

	// List files in scope (zip or directory).
	var files []string
	var err error
	if zipscope.IsZipScope(scopeDir) {
		files, err = zipscope.ListFiles(scopeDir)
	} else {
		files, err = listDirMDFiles(scopeDir)
	}
	if err != nil {
		return nil, err
	}

	var awaitStates []string
	for _, f := range files {
		if !strings.HasSuffix(strings.ToLower(f), ".md") {
			continue
		}
		var content string
		if zipscope.IsZipScope(scopeDir) {
			content, err = zipscope.ReadText(scopeDir, f)
		} else {
			var data []byte
			data, err = os.ReadFile(filepath.Join(scopeDir, f))
			if err == nil {
				content = string(data)
			}
		}
		if err != nil {
			continue // skip unreadable files
		}
		p, _, parseErr := policy.ParseFrontmatter(content)
		if parseErr != nil || p == nil {
			continue
		}
		for _, entry := range p.AllowedTransitions {
			if entry["tag"] == "await" {
				awaitStates = append(awaitStates, f)
				break
			}
		}
	}
	return awaitStates, nil
}

// scanYamlForAwait checks a YAML scope for states declaring await transitions.
func scanYamlForAwait(yamlPath string) ([]string, error) {
	wf, err := yamlscope.Parse(yamlPath)
	if err != nil {
		return nil, err
	}
	var awaitStates []string
	for _, name := range wf.StateOrder {
		st := wf.States[name]
		for _, entry := range st.AllowedTransitions {
			if entry["tag"] == "await" {
				awaitStates = append(awaitStates, name+".md")
				break
			}
		}
	}
	return awaitStates, nil
}

// listDirMDFiles lists .md files in a directory scope.
// Returns nil (not an error) when the directory does not exist.
func listDirMDFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			files = append(files, e.Name())
		}
	}
	return files, nil
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
