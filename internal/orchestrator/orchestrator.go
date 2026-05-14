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

// AskInput carries a response to a pending ask from the daemon layer.
type AskInput struct {
	AskID         string
	Response      string
	UploadedFiles []wfstate.FileRecord
}

// PendingAskError is returned from RunAllAgents when the quiesce point is
// reached with OnAsk="pause". It carries structured data describing the
// active ask so the CLI can emit JSON and exit with code 2.
type PendingAskError struct {
	Status       string           `json:"status"`
	RunID        string           `json:"run_id"`
	Workflow     string           `json:"workflow"`
	Asking       PendingAskDetail `json:"asking"`
	PendingCount int              `json:"pending_count"`
	Resume       string           `json:"resume"`
}

// PendingAskDetail describes the active ask within a PendingAskError.
type PendingAskDetail struct {
	AskID   string `json:"ask_id"`
	AgentID string `json:"agent_id"`
	Prompt  string `json:"prompt"`
}

func (e *PendingAskError) Error() string {
	return fmt.Sprintf("workflow %q has a pending ask (ask_id=%s)", e.RunID, e.Asking.AskID)
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

	// OnAsk controls behaviour when a workflow declares or produces <ask>
	// transitions. "pause" allows asking; "reject" (default when empty)
	// rejects at launch time and at runtime.
	OnAsk string

	// DaemonMode changes ask behaviour: when true the orchestrator does NOT
	// quiesce sibling agents when one hits <ask>. Instead it calls
	// AskCallback and keeps running. The asking agent stays idle until
	// input arrives on AskInputCh.
	DaemonMode bool

	// AskCallback is called (from the main goroutine) when an agent enters
	// <ask> in daemon mode. It notifies the daemon layer of the new ask,
	// passing the file affordance descriptor (nil for text-only asks) and
	// any files staged for the per-input directory so the daemon can record
	// them on the pending input.
	AskCallback func(agentID, askID, prompt, nextState string, affordance *parsing.FileAffordance, stagedFiles []wfstate.FileRecord)

	// AskInputCh delivers responses to pending asks in daemon mode. The
	// orchestrator reads from this channel in its main select loop and
	// resumes the matching agent.
	AskInputCh <-chan AskInput

	// AskInput is the --input value provided on --resume. When non-empty
	// and agents are in the asking state, this value is delivered to the
	// active ask agent before the main loop starts.
	AskInput string

	// ObserverSetup, if non-nil, is called with the Bus immediately after it
	// is created (before WorkflowStarted is emitted). Use it to register
	// observers from production code (e.g. the CLI). Tests use SetBusHook
	// from export_test.go instead.
	ObserverSetup func(*bus.Bus)

	// StopSignalCh, when receivable, triggers a graceful quiesce: the
	// orchestrator stops launching new executors and lets in-flight
	// agent goroutines drain to their next-state boundary, then writes
	// state. The return value is nil for a pure stop-signal pause, or
	// *PendingAskError if at least one agent ends up in the asking
	// status (ask-driven semantics win when both apply). Sending a
	// single value or closing the channel are both valid ways to
	// signal. A nil channel never selects, so leaving this unset
	// disables the feature.
	StopSignalCh <-chan struct{}
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

	// Launch-time check: if OnAsk is "reject" (or empty, which defaults to
	// reject), determine whether the workflow requires human input and reject
	// early with a helpful error. Skipped when DaemonMode is true because
	// daemon mode handles asks natively.
	//
	// When the scope has a workflow manifest, use ResolveRequiresHumanInput
	// (which honours the manifest's requires_human_input field and follows
	// cross-workflow references transitively). Otherwise fall back to the
	// simple frontmatter scan.
	if opts.OnAsk != "pause" && !opts.DaemonMode {
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
							"Use --on-ask=pause to allow asking, or use `raymond serve` for interactive workflows",
						workflowID,
					)
				}
				manifestUsed = true
			}
			// ErrNotManifest (YAML scope file named workflow.yaml): fall
			// through to the frontmatter scan below.
		}
		if !manifestUsed {
			askStates, scanErr := scanForAskTransitions(ws.ScopeDir)
			if scanErr != nil {
				return fmt.Errorf("scan for ask transitions: %w", scanErr)
			}
			if len(askStates) > 0 {
				return fmt.Errorf(
					"workflow %q declares <ask> transitions in state(s): %s. "+
						"Use --on-ask=pause to allow asking, or use `raymond serve` for interactive workflows",
					workflowID, strings.Join(askStates, ", "),
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

	// Resume-from-ask: when resuming a paused workflow, detect agents in
	// the asking state and either deliver input or re-present the prompt.
	//
	// Daemon-mode carve-out: when DaemonMode is set, the daemon's recovery
	// path relaunches the run with the asking state intact and expects input
	// to arrive later via AskInputCh. The main loop's len(running) == 0
	// branch (further down) has an explicit DaemonMode-and-asking case that
	// drops into the select and reads from daemonInputCh. Returning
	// PendingAskError here would short-circuit that path and make a
	// recovered run unanswerable through DeliverInput.
	if hasAskingAgents(ws.Agents) && !opts.DaemonMode {
		if opts.AskInput == "" {
			// No input provided — re-present the active ask's prompt so
			// the caller can see what's pending.
			activeAgent, pendingCount := firstAskingAndCount(ws.Agents)
			return &PendingAskError{
				Status:   "asking",
				RunID:    workflowID,
				Workflow: filepath.Base(ws.ScopeDir),
				Asking: PendingAskDetail{
					AskID: activeAgent.AskID,
					AgentID: activeAgent.ID,
					Prompt:  activeAgent.AskPrompt,
				},
				PendingCount: pendingCount - 1,
				Resume:       fmt.Sprintf("ray --resume %s --input \"[your response]\"", workflowID),
			}
		}

		// Deliver input to the first asking agent.
		activeIdx := firstAskingIndex(ws.Agents)
		a := &ws.Agents[activeIdx]
		askInput := opts.AskInput
		askID := a.AskID
		a.PendingResult = &askInput
		a.PendingAskID = askID
		a.CurrentState = a.AskNextState
		a.AskPrompt = ""
		a.AskNextState = ""
		a.AskTimeout = ""
		a.AskTimeoutNext = ""
		a.AskID = ""
		a.Status = ""

		b.Emit(events.AgentAskResumed{
			AgentID:   a.ID,
			AskID:   askID,
			Timestamp: time.Now(),
		})

		// Check for remaining asking agents (pre-ask queue).
		if nextAgent, remaining := firstAskingAndCount(ws.Agents); nextAgent != nil {
			// More asking agents remain. Persist the delivery and return
			// the next agent's prompt — no agents proceed yet.
			if err := wfstate.WriteState(workflowID, ws, stateDir); err != nil {
				return fmt.Errorf("write state: %w", err)
			}
			return &PendingAskError{
				Status:   "asking",
				RunID:    workflowID,
				Workflow: filepath.Base(ws.ScopeDir),
				Asking: PendingAskDetail{
					AskID: nextAgent.AskID,
					AgentID: nextAgent.ID,
					Prompt:  nextAgent.AskPrompt,
				},
				PendingCount: remaining - 1,
				Resume:       fmt.Sprintf("ray --resume %s --input \"[your response]\"", workflowID),
			}
		}

		// No more asking agents — persist the delivery and let all agents
		// proceed together in the normal main loop below.
		if err := wfstate.WriteState(workflowID, ws, stateDir); err != nil {
			return fmt.Errorf("write state: %w", err)
		}
	} else if !hasAskingAgents(ws.Agents) && opts.AskInput != "" {
		return fmt.Errorf(
			"no agents are pending an ask; " +
				"the --input flag on --resume is only valid when a workflow is paused at an <ask> point",
		)
	}

	// Buffered channel so goroutines can send results without blocking.
	resultCh := make(chan stepResult, 64)

	// running tracks agent IDs that currently have goroutines executing.
	// Only accessed from the main goroutine.
	running := make(map[string]bool)

	// Quiesce state: when an agent hits <ask> and OnAsk == "pause",
	// pausing prevents launching new agent goroutines so the system can
	// reach a clean quiesce point.
	pausing := false

	// Resolved once: the channel for daemon-mode ask input delivery.
	// nil when not in daemon mode (nil channels block forever in select,
	// effectively disabling the case).
	daemonInputCh := askInputCh(opts)

	// Local copy of the stop-signal channel so we can nil it out after
	// the first receive. Without that, a closed StopSignalCh would
	// fire on every select iteration (busy-spin) while we wait for
	// in-flight executors to drain.
	stopSignalCh := opts.StopSignalCh

	activeAsk := ""
	var preAskQueue []string

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
	// non-empty status (paused, asking, failed) are skipped.
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

			// Quiesce takes priority over the daemon-mode asking-wait:
			// once pausing is true, the orchestrator must exit so the
			// shutdown coordinator can see the run drain. Without this
			// ordering, a daemon-mode run whose only agents are asking
			// would loop back into the select after StopSignalCh fired
			// and never reach the exit path below — Tier-2 quiesce
			// would deadlock until force-kill (bead-10).
			if pausing {
				// Quiesce point: all goroutines have drained while pausing
				// was in effect. Three paths lead here:
				//   1. At least one agent hit <ask> with OnAsk="pause".
				//      The remaining agents are either asking or held at
				//      their next-state boundary; return PendingAskError
				//      so the caller can serve the ask or resume later.
				//   2. An external StopSignalCh triggered a graceful
				//      quiesce. No asking agents are present; return nil
				//      after persisting state so the workflow can be
				//      resumed cleanly.
				//   3. StopSignalCh fired in daemon mode while at least
				//      one agent was already asking. Returns
				//      PendingAskError — the asking agent is itself a
				//      safe pause point, so the run is "done" from the
				//      shutdown coordinator's perspective.
				b.Emit(events.WorkflowPaused{
					WorkflowID:       workflowID,
					TotalCostUSD:     ws.TotalCostUSD,
					PausedAgentCount: len(ws.Agents),
					Timestamp:        time.Now(),
				})
				if err := wfstate.WriteState(workflowID, ws, stateDir); err != nil {
					return err
				}

				if !hasAskingAgents(ws.Agents) {
					// Stop-signal-induced pause with no pending asks
					// → clean exit. Same state-file write path used
					// above; no schema change.
					return nil
				}

				// Find the active asking agent to populate the structured output.
				var activeAgent *wfstate.AgentState
				if activeAsk != "" {
					if idx := findAgentByID(ws.Agents, activeAsk); idx >= 0 {
						activeAgent = &ws.Agents[idx]
					}
				}
				if activeAgent == nil {
					// Fallback: find the first asking agent.
					for i := range ws.Agents {
						if ws.Agents[i].Status == wfstate.AgentStatusAsking {
							activeAgent = &ws.Agents[i]
							break
						}
					}
				}
				if activeAgent != nil {
					return &PendingAskError{
						Status:   "asking",
						RunID:    workflowID,
						Workflow: filepath.Base(ws.ScopeDir),
						Asking: PendingAskDetail{
							AskID: activeAgent.AskID,
							AgentID: activeAgent.ID,
							Prompt:  activeAgent.AskPrompt,
						},
						PendingCount: len(preAskQueue),
						Resume:       fmt.Sprintf("ray --resume %s --input \"[your response]\"", workflowID),
					}
				}
				return nil
			} else if opts.DaemonMode && hasAskingAgents(ws.Agents) {
				// Daemon mode: agents are pending an ask but we are not
				// quiescing. Don't exit — fall through to the select
				// loop which will read from AskInputCh and resume agents
				// as input arrives (or StopSignalCh to begin a quiesce).
				// fall through to select
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

		// Wait for the next goroutine result or daemon-mode ask input.
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-stopSignalCh:
			// External quiesce request: stop launching successors and
			// let in-flight executors drain to their next-state
			// boundary. The existing launchActive short-circuit on
			// pausing prevents any new launches; we wait for the
			// running set to empty and then hit the terminal block
			// above. Nil out the local channel so a closed signal
			// does not re-fire on every subsequent select iteration.
			pausing = true
			stopSignalCh = nil

		case input := <-daemonInputCh:
			idx := findAskingAgent(ws.Agents, input.AskID)
			if idx < 0 {
				continue // unknown AskID; ignore
			}
			a := &ws.Agents[idx]
			pr := input.Response
			a.PendingResult = &pr
			a.PendingAskID = input.AskID

			ws.ResolvedInputs = append(ws.ResolvedInputs, wfstate.ResolvedInput{
				AskID:       input.AskID,
				AgentID:       a.ID,
				Prompt:        a.AskPrompt,
				NextState:     a.AskNextState,
				ResponseText:  input.Response,
				StagedFiles:   a.AskStagedFiles,
				UploadedFiles: input.UploadedFiles,
				EnteredAt:     a.AskEnteredAt,
				ResolvedAt:    time.Now(),
			})

			a.CurrentState = a.AskNextState
			a.Status = ""
			a.AskPrompt = ""
			a.AskNextState = ""
			a.AskTimeout = ""
			a.AskTimeoutNext = ""
			a.AskID = ""
			a.AskFileAffordance = nil
			a.AskStagedFiles = nil
			a.AskEnteredAt = time.Time{}
			b.Emit(events.AgentAskResumed{
				AgentID:   a.ID,
				AskID:   input.AskID,
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

				// Ask handling: detect when an agent enters asking
				// status and apply the mode-specific strategy.
				if idx := findAgentByID(ws.Agents, agentBefore.ID); idx >= 0 && ws.Agents[idx].Status == wfstate.AgentStatusAsking {
					// Stamp the ask-entry timestamp for every ask
					// (text-only or file-bearing) so the eventual
					// resolved-input record carries a true entry time.
					ws.Agents[idx].AskEnteredAt = time.Now()

					// Stage display files (and create the per-input
					// directory so uploads have a place to land) before
					// notifying anyone the ask has started, so the
					// notification carries the staged-file metadata.
					stageFailed := false
					var stageErr error
					if affordance := ws.Agents[idx].AskFileAffordance; affordance != nil {
						records, err := StageInputFiles(
							ws.Agents[idx].TaskFolder,
							ws.Agents[idx].AskID,
							*affordance,
						)
						if err != nil {
							stageFailed = true
							stageErr = err
						} else {
							ws.Agents[idx].AskStagedFiles = records
						}
					}

					if stageFailed {
						// Do not enter the ask: clear the ask fields
						// and pause the agent with a descriptive error
						// (mirrors the on-ask=reject branch below).
						a := &ws.Agents[idx]
						a.Status = wfstate.AgentStatusPaused
						a.Error = fmt.Sprintf(
							"failed to stage files for <ask> in agent %q: %v",
							agentBefore.ID, stageErr,
						)
						a.AskPrompt = ""
						a.AskNextState = ""
						a.AskTimeout = ""
						a.AskTimeoutNext = ""
						a.AskID = ""
						a.AskFileAffordance = nil
						a.AskStagedFiles = nil
						a.AskEnteredAt = time.Time{}
						b.Emit(events.AgentPaused{
							AgentID:   agentBefore.ID,
							Reason:    "ask_stage_error",
							Error:     a.Error,
							Timestamp: time.Now(),
						})
					} else if opts.DaemonMode {
						// Daemon mode: siblings keep running. Notify
						// the daemon layer via callback; the agent
						// stays asking until input arrives on
						// AskInputCh.
						b.Emit(events.AgentAskStarted{
							AgentID:   ws.Agents[idx].ID,
							AskID:   ws.Agents[idx].AskID,
							Prompt:    ws.Agents[idx].AskPrompt,
							NextState: ws.Agents[idx].AskNextState,
							Timeout:   ws.Agents[idx].AskTimeout,
							Timestamp: time.Now(),
						})
						if opts.AskCallback != nil {
							opts.AskCallback(
								ws.Agents[idx].ID,
								ws.Agents[idx].AskID,
								ws.Agents[idx].AskPrompt,
								ws.Agents[idx].AskNextState,
								ws.Agents[idx].AskFileAffordance,
								ws.Agents[idx].AskStagedFiles,
							)
						}
					} else if opts.OnAsk == "pause" {
						// CLI pause mode: quiesce all agents. The
						// first agent to ask is the active ask;
						// subsequent ones are queued.
						if activeAsk == "" {
							activeAsk = ws.Agents[idx].ID
						} else {
							preAskQueue = append(preAskQueue, ws.Agents[idx].ID)
						}
						pausing = true
						b.Emit(events.AgentAskStarted{
							AgentID:   ws.Agents[idx].ID,
							AskID:   ws.Agents[idx].AskID,
							Prompt:    ws.Agents[idx].AskPrompt,
							NextState: ws.Agents[idx].AskNextState,
							Timeout:   ws.Agents[idx].AskTimeout,
							Timestamp: time.Now(),
						})
					} else {
						// Runtime reject: fail the agent immediately.
						ws.Agents[idx].Status = wfstate.AgentStatusPaused
						ws.Agents[idx].Error = fmt.Sprintf(
							"agent %q produced <ask> but --on-ask=reject is in effect; "+
								"use --on-ask=pause or `raymond serve`",
							agentBefore.ID,
						)
						ws.Agents[idx].AskPrompt = ""
						ws.Agents[idx].AskNextState = ""
						ws.Agents[idx].AskTimeout = ""
						ws.Agents[idx].AskTimeoutNext = ""
						ws.Agents[idx].AskID = ""
						b.Emit(events.AgentPaused{
							AgentID:   agentBefore.ID,
							Reason:    "ask_rejected",
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

// hasAskingAgents reports whether any agent in the slice has asking status.
func hasAskingAgents(agents []wfstate.AgentState) bool {
	for _, a := range agents {
		if a.Status == wfstate.AgentStatusAsking {
			return true
		}
	}
	return false
}

// firstAskingIndex returns the index of the first agent with asking status,
// or -1 if none exists.
func firstAskingIndex(agents []wfstate.AgentState) int {
	for i, a := range agents {
		if a.Status == wfstate.AgentStatusAsking {
			return i
		}
	}
	return -1
}

// firstAskingAndCount returns the first asking agent and the total count of
// asking agents. Returns (nil, 0) when none are asking.
func firstAskingAndCount(agents []wfstate.AgentState) (*wfstate.AgentState, int) {
	var first *wfstate.AgentState
	count := 0
	for i, a := range agents {
		if a.Status == wfstate.AgentStatusAsking {
			if first == nil {
				first = &agents[i]
			}
			count++
		}
	}
	return first, count
}

// findAskingAgent returns the index of the asking agent whose AskID
// matches askID, or -1 if not found.
func findAskingAgent(agents []wfstate.AgentState, askID string) int {
	for i, a := range agents {
		if a.Status == wfstate.AgentStatusAsking && a.AskID == askID {
			return i
		}
	}
	return -1
}

// askInputCh returns opts.AskInputCh when daemon mode is active, or a nil
// channel (which blocks forever in select) when it is not.
func askInputCh(opts RunOptions) <-chan AskInput {
	if opts.DaemonMode && opts.AskInputCh != nil {
		return opts.AskInputCh
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
	callerCopy.PendingAskID = ""
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
// asking external input). Returns false for an empty slice.
func allPaused(agents []wfstate.AgentState) bool {
	if len(agents) == 0 {
		return false
	}
	for _, a := range agents {
		if a.Status != wfstate.AgentStatusPaused && a.Status != wfstate.AgentStatusAsking {
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
// no parseable reset time or if no paused agents exist (e.g. all asking).
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

// scanForAskTransitions examines all state files in scopeDir for
// allowed_transitions entries with tag "ask". Returns the list of state
// filenames that declare ask transitions.
//
// Supports directory, zip, and YAML scopes.
func scanForAskTransitions(scopeDir string) ([]string, error) {
	if yamlscope.IsYamlScope(scopeDir) {
		return scanYamlForAsk(scopeDir)
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

	var askStates []string
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
			if entry["tag"] == "ask" {
				askStates = append(askStates, f)
				break
			}
		}
	}
	return askStates, nil
}

// scanYamlForAsk checks a YAML scope for states declaring ask transitions.
func scanYamlForAsk(yamlPath string) ([]string, error) {
	wf, err := yamlscope.Parse(yamlPath)
	if err != nil {
		return nil, err
	}
	var askStates []string
	for _, name := range wf.StateOrder {
		st := wf.States[name]
		for _, entry := range st.AllowedTransitions {
			if entry["tag"] == "ask" {
				askStates = append(askStates, name+".md")
				break
			}
		}
	}
	return askStates, nil
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
