// Package transitions implements the six transition handlers for the raymond
// workflow orchestrator.
//
// Each transition tag produced by an agent state has a corresponding handler:
//   - goto:     simple state change; session preserved
//   - reset:    fresh start; session cleared, stack preserved
//   - function: stateless sub-call; caller session pushed, fresh session
//   - call:     context-branching sub-call; caller session forked
//   - fork:     spawn independent worker agent while parent continues
//   - result:   return from function/call, or terminate if stack is empty
//
// The primary entry point is ApplyTransition, which deep-copies the agent,
// clears transient fields, and dispatches to the appropriate handler.
package transitions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vector76/raymond/internal/parsing"
	wfstate "github.com/vector76/raymond/internal/state"
)

// TransitionResult is returned by ApplyTransition and the individual handlers.
//
// The three possible outcomes are:
//  1. Normal update: Agent non-nil, Worker nil.
//  2. Fork: Agent (updated parent) non-nil, Worker (new worker) non-nil.
//  3. Termination: Agent nil, Worker nil.
type TransitionResult struct {
	Agent  *wfstate.AgentState // updated agent; nil when the agent terminates
	Worker *wfstate.AgentState // non-nil only for fork transitions
}

// ResolveCd resolves a cd attribute value to an absolute, normalised path.
//
// If cdValue is absolute it is normalised and returned as-is.
// If cdValue is relative it is resolved against baseCwd; when baseCwd is ""
// (agent has no cwd set) the process's current working directory is used.
func ResolveCd(cdValue, baseCwd string) string {
	if filepath.IsAbs(cdValue) {
		return filepath.Clean(cdValue)
	}
	base := baseCwd
	if base == "" {
		base, _ = os.Getwd()
	}
	return filepath.Clean(filepath.Join(base, cdValue))
}

// ApplyTransition applies a transition to an agent and returns the result.
//
// Steps:
//  1. Deep-copies the agent (original is never mutated).
//  2. Clears transient fields (PendingResult, ForkSessionID, ForkAttributes).
//  3. Dispatches to the appropriate handler based on transition.Tag.
//
// Returns an error when the tag is unknown or a required attribute is missing.
func ApplyTransition(
	agent *wfstate.AgentState,
	transition parsing.Transition,
	wfState *wfstate.WorkflowState,
) (TransitionResult, error) {
	// Deep copy — handlers must not mutate the original agent.
	copy := deepCopyAgent(*agent)

	// Clear transient fields before the handler runs so handlers can set
	// fresh values without accidentally inheriting stale ones.
	copy.PendingResult = nil
	copy.ForkSessionID = nil
	copy.ForkAttributes = nil

	switch transition.Tag {
	case "goto":
		return HandleGoto(copy, transition), nil
	case "reset":
		return HandleReset(copy, transition), nil
	case "function":
		return HandleFunction(copy, transition)
	case "call":
		return HandleCall(copy, transition)
	case "fork":
		return HandleFork(copy, transition, wfState)
	case "result":
		return HandleResult(copy, transition, wfState), nil
	default:
		return TransitionResult{}, fmt.Errorf("unknown transition tag: %s", transition.Tag)
	}
}

// deepCopyAgent returns a fully independent copy of a, including deep copies
// of all pointer and reference fields.
func deepCopyAgent(a wfstate.AgentState) wfstate.AgentState {
	c := a // copies all value-type fields (ID, CurrentState, Cwd)

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

	// Allocate a new backing array for the stack so handlers can append
	// without aliasing the original. Skip allocation for the common empty case.
	var newStack []wfstate.StackFrame
	if len(a.Stack) > 0 {
		newStack = make([]wfstate.StackFrame, len(a.Stack))
		for i, frame := range a.Stack {
			newStack[i] = wfstate.StackFrame{State: frame.State}
			if frame.Session != nil {
				s := *frame.Session
				newStack[i].Session = &s
			}
		}
	}
	c.Stack = newStack

	return c
}

// HandleGoto handles the <goto> transition tag.
//
// Updates current_state to the transition target. Session and stack are
// preserved unchanged.
func HandleGoto(agent wfstate.AgentState, transition parsing.Transition) TransitionResult {
	agent.CurrentState = transition.Target
	return TransitionResult{Agent: &agent}
}

// HandleReset handles the <reset> transition tag.
//
// Starts a fresh session at the target state:
//   - Sets current_state to target
//   - Clears session_id (fresh start)
//   - Applies cd attribute if present
func HandleReset(agent wfstate.AgentState, transition parsing.Transition) TransitionResult {
	agent.CurrentState = transition.Target
	agent.SessionID = nil

	if cd, ok := transition.Attributes["cd"]; ok {
		agent.Cwd = ResolveCd(cd, agent.Cwd)
	}

	return TransitionResult{Agent: &agent}
}

// HandleFunction handles the <function> transition tag.
//
// Runs a stateless sub-evaluation that returns to the caller:
//   - Pushes {caller session, return state} frame onto the stack
//   - Clears session_id (fresh context)
//   - Updates current_state to the function target
//
// Returns an error when the required "return" attribute is absent.
func HandleFunction(agent wfstate.AgentState, transition parsing.Transition) (TransitionResult, error) {
	returnState, ok := transition.Attributes["return"]
	if !ok {
		return TransitionResult{}, fmt.Errorf(
			"<function> tag requires 'return' attribute. "+
				"Example: <function return=\"NEXT.md\">EVAL.md</function>",
		)
	}

	frame := wfstate.StackFrame{
		Session: agent.SessionID,
		State:   returnState,
	}
	agent.Stack = append(agent.Stack, frame)
	agent.SessionID = nil
	agent.CurrentState = transition.Target

	return TransitionResult{Agent: &agent}, nil
}

// HandleCall handles the <call> transition tag.
//
// Enters a subroutine that inherits the caller's context via session forking:
//   - Pushes {caller session, return state} frame onto the stack
//   - Sets fork_session_id to trigger --fork-session in the next executor step
//   - Updates current_state to the callee target
//
// Returns an error when the required "return" attribute is absent.
func HandleCall(agent wfstate.AgentState, transition parsing.Transition) (TransitionResult, error) {
	returnState, ok := transition.Attributes["return"]
	if !ok {
		return TransitionResult{}, fmt.Errorf(
			"<call> tag requires 'return' attribute. "+
				"Example: <call return=\"NEXT.md\">CHILD.md</call>",
		)
	}

	callerSession := agent.SessionID

	frame := wfstate.StackFrame{
		Session: callerSession,
		State:   returnState,
	}
	agent.Stack = append(agent.Stack, frame)
	agent.ForkSessionID = callerSession
	agent.CurrentState = transition.Target

	return TransitionResult{Agent: &agent}, nil
}

// HandleFork handles the <fork> transition tag.
//
// Spawns an independent worker agent while the parent continues:
//   - Creates a new worker with a unique ID, empty stack, fresh session
//   - Worker's current_state is the fork target
//   - Applies cd attribute to worker if present
//   - Attributes other than "next" and "cd" become the worker's ForkAttributes
//   - Parent advances to the "next" state; its session and stack are preserved
//
// Worker IDs use compact hierarchical notation:
//
//	{parent_id}_{state_abbrev}{counter}
//
// where state_abbrev is the first 6 lowercase characters of the target state
// name (without extension) and counter is a per-parent persistent integer.
//
// Returns an error when the required "next" attribute is absent.
func HandleFork(
	agent wfstate.AgentState,
	transition parsing.Transition,
	wfState *wfstate.WorkflowState,
) (TransitionResult, error) {
	nextState, ok := transition.Attributes["next"]
	if !ok {
		return TransitionResult{}, fmt.Errorf(
			"<fork> tag requires 'next' attribute. "+
				"Example: <fork next=\"NEXT.md\">WORKER.md</fork>",
		)
	}

	// Derive state abbreviation from the fork target filename.
	stateName := strings.ToLower(parsing.ExtractStateName(transition.Target))
	stateAbbrev := stateName
	if len(stateAbbrev) > 6 {
		stateAbbrev = stateAbbrev[:6]
	}

	// Allocate a unique worker ID using persistent per-parent counters.
	if wfState.ForkCounters == nil {
		wfState.ForkCounters = make(map[string]int)
	}
	wfState.ForkCounters[agent.ID]++
	counter := wfState.ForkCounters[agent.ID]
	workerID := fmt.Sprintf("%s_%s%d", agent.ID, stateAbbrev, counter)

	// Build the worker agent.
	worker := wfstate.AgentState{
		ID:           workerID,
		CurrentState: transition.Target,
		SessionID:    nil,
		Stack:        []wfstate.StackFrame{},
	}

	if cd, ok := transition.Attributes["cd"]; ok {
		worker.Cwd = ResolveCd(cd, agent.Cwd)
	}

	// Collect fork attributes (exclude "next" and "cd").
	forkAttrs := make(map[string]string)
	for k, v := range transition.Attributes {
		if k != "next" && k != "cd" {
			forkAttrs[k] = v
		}
	}
	if len(forkAttrs) > 0 {
		worker.ForkAttributes = forkAttrs
	}

	// Advance parent to next state; session and stack are preserved.
	agent.CurrentState = nextState

	return TransitionResult{Agent: &agent, Worker: &worker}, nil
}

// HandleResult handles the <result> transition tag.
//
// If the return stack is empty the agent terminates (Agent == nil in result).
// The termination payload is stored in wfState.AgentTerminationResults.
//
// If the return stack is non-empty, the top frame is popped and the agent
// resumes at the return state with the caller's session restored and
// PendingResult set to the transition payload.
func HandleResult(
	agent wfstate.AgentState,
	transition parsing.Transition,
	wfState *wfstate.WorkflowState,
) TransitionResult {
	if len(agent.Stack) == 0 {
		// Agent terminates — store payload for orchestrator consumption.
		if wfState.AgentTerminationResults == nil {
			wfState.AgentTerminationResults = make(map[string]string)
		}
		wfState.AgentTerminationResults[agent.ID] = transition.Payload
		return TransitionResult{}
	}

	// Pop top frame (LIFO).
	frame := agent.Stack[len(agent.Stack)-1]
	agent.Stack = agent.Stack[:len(agent.Stack)-1]

	agent.SessionID = frame.Session
	agent.CurrentState = frame.State
	payload := transition.Payload
	agent.PendingResult = &payload

	return TransitionResult{Agent: &agent}
}
