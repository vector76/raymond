// Package transitions implements the transition handlers for the raymond
// workflow orchestrator.
//
// Each transition tag produced by an agent state has a corresponding handler:
//   - goto:              simple state change; session preserved
//   - reset:             fresh start; session cleared, stack preserved
//   - function:          stateless sub-call; caller session pushed, fresh session
//   - call:              context-branching sub-call; caller session forked
//   - fork:              spawn independent worker agent while parent continues
//   - result:            return from function/call, or terminate if stack is empty
//   - call-workflow:     blocking cross-workflow call; forks caller session
//   - function-workflow: blocking cross-workflow call; fresh session, cd allowed
//   - fork-workflow:     non-blocking cross-workflow spawn; fresh session, cd allowed
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
	"github.com/vector76/raymond/internal/prompts"
	"github.com/vector76/raymond/internal/specifier"
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

	// Save transient fields before clearing — used for input template rendering
	// in the cross-workflow transition cases below.
	origPendingResult := copy.PendingResult
	origForkAttributes := copy.ForkAttributes

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
	case "fork-workflow":
		tr := withRenderedInput(transition, origPendingResult, origForkAttributes)
		res, err := specifier.Resolve(tr.Target, copy.ScopeDir)
		if err != nil {
			copy.Status = wfstate.AgentStatusPaused
			copy.Error = fmt.Sprintf("fork-workflow: %s", err.Error())
			return TransitionResult{Agent: &copy}, nil
		}
		result, handlerErr := HandleForkWorkflow(copy, tr, wfState, res)
		if handlerErr != nil {
			copy.Status = wfstate.AgentStatusPaused
			copy.Error = handlerErr.Error()
			return TransitionResult{Agent: &copy}, nil
		}
		return result, nil
	case "call-workflow":
		tr := withRenderedInput(transition, origPendingResult, origForkAttributes)
		res, err := specifier.Resolve(tr.Target, copy.ScopeDir)
		if err != nil {
			copy.Status = wfstate.AgentStatusPaused
			copy.Error = fmt.Sprintf("call-workflow: %s", err.Error())
			return TransitionResult{Agent: &copy}, nil
		}
		result, handlerErr := HandleCallWorkflow(copy, tr, wfState, res)
		if handlerErr != nil {
			copy.Status = wfstate.AgentStatusPaused
			copy.Error = handlerErr.Error()
			return TransitionResult{Agent: &copy}, nil
		}
		return result, nil
	case "function-workflow":
		tr := withRenderedInput(transition, origPendingResult, origForkAttributes)
		res, err := specifier.Resolve(tr.Target, copy.ScopeDir)
		if err != nil {
			copy.Status = wfstate.AgentStatusPaused
			copy.Error = fmt.Sprintf("function-workflow: %s", err.Error())
			return TransitionResult{Agent: &copy}, nil
		}
		result, handlerErr := HandleFunctionWorkflow(copy, tr, wfState, res)
		if handlerErr != nil {
			copy.Status = wfstate.AgentStatusPaused
			copy.Error = handlerErr.Error()
			return TransitionResult{Agent: &copy}, nil
		}
		return result, nil
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
			var session *string
			if frame.Session != nil {
				s := *frame.Session
				session = &s
			}
			newStack[i] = wfstate.StackFrame{
				Session:      session,
				State:        frame.State,
				ScopeDir:     frame.ScopeDir,
				Cwd:          frame.Cwd,
				NestingDepth: frame.NestingDepth,
			}
		}
	}
	c.Stack = newStack

	return c
}

// withRenderedInput returns transition unchanged when the "input" attribute is
// absent or empty. Otherwise it renders the input value through
// prompts.RenderPrompt with variables built from pendingResult (as "result")
// and all forkAttributes entries — the same pattern used in
// MarkdownExecutor.Execute. A shallow copy of the attributes map is made only
// when rendering actually changes the value.
func withRenderedInput(
	transition parsing.Transition,
	pendingResult *string,
	forkAttributes map[string]string,
) parsing.Transition {
	input, ok := transition.Attributes["input"]
	if !ok || input == "" {
		return transition
	}

	variables := make(map[string]any)
	if pendingResult != nil {
		variables["result"] = *pendingResult
	}
	for k, v := range forkAttributes {
		variables[k] = v
	}

	rendered := prompts.RenderPrompt(input, variables)
	if rendered == input {
		return transition
	}

	attrs := make(map[string]string, len(transition.Attributes))
	for k, v := range transition.Attributes {
		attrs[k] = v
	}
	attrs["input"] = rendered

	return parsing.Transition{
		Tag:        transition.Tag,
		Target:     transition.Target,
		Payload:    transition.Payload,
		Attributes: attrs,
	}
}

// HandleGoto handles the <goto> transition tag.
//
// Updates current_state to the transition target. Session and stack are
// preserved unchanged.
func HandleGoto(agent wfstate.AgentState, transition parsing.Transition) TransitionResult {
	agent.CurrentState = transition.Target
	if input, ok := transition.Attributes["input"]; ok && input != "" {
		agent.PendingResult = &input
	}
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

	if input, ok := transition.Attributes["input"]; ok && input != "" {
		agent.PendingResult = &input
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
		Session:      agent.SessionID,
		State:        returnState,
		ScopeDir:     agent.ScopeDir,
		Cwd:          agent.Cwd,
		NestingDepth: agent.NestingDepth,
	}
	agent.Stack = append(agent.Stack, frame)
	agent.SessionID = nil
	agent.CurrentState = transition.Target
	if input, ok := transition.Attributes["input"]; ok && input != "" {
		agent.PendingResult = &input
	}

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
		Session:      callerSession,
		State:        returnState,
		ScopeDir:     agent.ScopeDir,
		Cwd:          agent.Cwd,
		NestingDepth: agent.NestingDepth,
	}
	agent.Stack = append(agent.Stack, frame)
	agent.ForkSessionID = callerSession
	agent.CurrentState = transition.Target
	if input, ok := transition.Attributes["input"]; ok && input != "" {
		agent.PendingResult = &input
	}

	return TransitionResult{Agent: &agent}, nil
}

// CreateForkWorker builds a new worker agent for a <fork> transition without
// advancing the calling agent. This is the worker-construction core used by
// both HandleFork (single-fork path) and the multi-fork orchestrator dispatch.
//
// The "next" attribute is intentionally not required here — caller advancement
// is the responsibility of the caller (HandleFork or the orchestrator).
func CreateForkWorker(
	agent wfstate.AgentState,
	transition parsing.Transition,
	wfState *wfstate.WorkflowState,
) (wfstate.AgentState, error) {
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
		if k != "next" && k != "cd" && k != "input" {
			forkAttrs[k] = v
		}
	}
	if len(forkAttrs) > 0 {
		worker.ForkAttributes = forkAttrs
	}

	if input, ok := transition.Attributes["input"]; ok && input != "" {
		worker.PendingResult = &input
	}

	return worker, nil
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

	worker, err := CreateForkWorker(agent, transition, wfState)
	if err != nil {
		return TransitionResult{}, err
	}

	// Advance parent to next state; session and stack are preserved.
	agent.CurrentState = nextState

	return TransitionResult{Agent: &agent, Worker: &worker}, nil
}

// CreateForkWorkflowWorker builds a new worker agent for a <fork-workflow>
// transition without advancing the calling agent. This is the worker-
// construction core used by both HandleForkWorkflow (single-fork path) and the
// multi-fork orchestrator dispatch.
//
// The "next" attribute is intentionally not required here — caller advancement
// is the responsibility of HandleForkWorkflow or the orchestrator.
func CreateForkWorkflowWorker(
	agent wfstate.AgentState,
	transition parsing.Transition,
	ws *wfstate.WorkflowState,
	resolution specifier.Resolution,
) (wfstate.AgentState, error) {
	// Allocate a unique worker ID using persistent per-parent+abbrev counters.
	if ws.ForkCounters == nil {
		ws.ForkCounters = make(map[string]int)
	}
	counterKey := agent.ID + "_" + resolution.Abbrev
	ws.ForkCounters[counterKey]++
	counter := ws.ForkCounters[counterKey]
	workerID := fmt.Sprintf("%s_%s%d", agent.ID, resolution.Abbrev, counter)

	// Determine worker Cwd: inherit caller's unless overridden by "cd" attribute.
	workerCwd := agent.Cwd
	if cd, ok := transition.Attributes["cd"]; ok {
		workerCwd = ResolveCd(cd, agent.Cwd)
	}

	// Build the worker agent.
	worker := wfstate.AgentState{
		ID:           workerID,
		CurrentState: resolution.EntryPoint,
		ScopeDir:     resolution.ScopeDir,
		Cwd:          workerCwd,
		SessionID:    nil,
		Stack:        []wfstate.StackFrame{},
		// Workers inherit the caller's depth but do not increment it.
		// Only blocking cross-workflow calls (call-workflow / function-workflow)
		// consume a depth slot; non-blocking spawns do not add to the call tree.
		NestingDepth: agent.NestingDepth,
	}

	// Set PendingResult from "input" attribute if present and non-empty.
	if input, ok := transition.Attributes["input"]; ok && input != "" {
		worker.PendingResult = &input
	}

	return worker, nil
}

// HandleForkWorkflow handles the <fork-workflow> transition tag.
//
// Similar to HandleFork but targets an external workflow specifier. The
// resolution provides the ScopeDir, EntryPoint, and Abbrev for the worker.
//
// Worker IDs use the same hierarchical notation as HandleFork:
//
//	{parent_id}_{abbrev}{counter}
//
// where abbrev comes from resolution.Abbrev and counter is a per-parent+abbrev
// persistent integer keyed by agent.ID + "_" + resolution.Abbrev.
//
// Unlike HandleFork, the "next" attribute is optional:
//   - With "next": caller advances to that state.
//   - Without "next": caller remains at its current state (multi-fork dispatch).
//
// The "cd" attribute sets the worker's working directory; when absent the
// caller's Cwd is inherited. The "input" attribute, if non-empty, sets the
// worker's PendingResult.
func HandleForkWorkflow(
	agent wfstate.AgentState,
	transition parsing.Transition,
	ws *wfstate.WorkflowState,
	resolution specifier.Resolution,
) (TransitionResult, error) {
	worker, err := CreateForkWorkflowWorker(agent, transition, ws, resolution)
	if err != nil {
		return TransitionResult{}, err
	}

	// Advance caller to "next" state when present; otherwise leave it unchanged.
	if next, ok := transition.Attributes["next"]; ok {
		agent.CurrentState = next
	}

	return TransitionResult{Agent: &agent, Worker: &worker}, nil
}

// HandleCallWorkflow handles the <call-workflow> transition tag.
//
// Enters an external workflow that inherits the caller's context via session
// forking:
//   - Validates that "return" attribute is present; errors if absent.
//   - Validates that "cd" attribute is absent; errors if present (forbidden
//     because the session-binding constraint makes changing directory unsafe).
//   - Returns an error when agent.NestingDepth >= 4 (depth limit enforced).
//   - Pushes {caller session, return state, ScopeDir, Cwd, NestingDepth}
//     frame onto the stack, then increments agent.NestingDepth.
//   - Sets ForkSessionID to caller's session (sub-workflow inherits context).
//   - Clears SessionID (fresh Claude session for sub-workflow).
//   - Updates ScopeDir and CurrentState from the resolution.
//   - Sets PendingResult from "input" attribute if non-empty.
func HandleCallWorkflow(
	agent wfstate.AgentState,
	transition parsing.Transition,
	ws *wfstate.WorkflowState,
	resolution specifier.Resolution,
) (TransitionResult, error) {
	returnState, ok := transition.Attributes["return"]
	if !ok || returnState == "" {
		return TransitionResult{}, fmt.Errorf(
			"<call-workflow> tag requires 'return' attribute. " +
				"Example: <call-workflow return=\"NEXT.md\">./child-workflow</call-workflow>",
		)
	}

	if _, hasCd := transition.Attributes["cd"]; hasCd {
		return TransitionResult{}, fmt.Errorf(
			"<call-workflow> does not support 'cd' attribute: " +
				"the session-binding constraint makes changing directory unsafe for call-workflow transitions",
		)
	}

	if agent.NestingDepth >= 4 {
		return TransitionResult{}, fmt.Errorf(
			"<call-workflow> maximum cross-workflow nesting depth (4) exceeded",
		)
	}

	callerSession := agent.SessionID

	frame := wfstate.StackFrame{
		Session:      callerSession,
		State:        returnState,
		ScopeDir:     agent.ScopeDir,
		Cwd:          agent.Cwd,
		NestingDepth: agent.NestingDepth,
	}
	agent.Stack = append(agent.Stack, frame)
	agent.NestingDepth = agent.NestingDepth + 1
	agent.ForkSessionID = callerSession
	agent.SessionID = nil
	agent.ScopeDir = resolution.ScopeDir
	agent.CurrentState = resolution.EntryPoint

	if input, ok := transition.Attributes["input"]; ok && input != "" {
		agent.PendingResult = &input
	}

	return TransitionResult{Agent: &agent}, nil
}

// HandleFunctionWorkflow handles the <function-workflow> transition tag.
//
// Enters an external workflow with a fresh session (no conversation context
// inheritance):
//   - Validates that "return" attribute is present; errors if absent.
//   - Returns an error when agent.NestingDepth >= 4 (depth limit enforced).
//   - Pushes {caller session, return state, ScopeDir, Cwd, NestingDepth}
//     frame onto the stack, then increments agent.NestingDepth.
//   - Clears SessionID (fresh Claude session; no ForkSessionID set).
//   - Updates ScopeDir and CurrentState from the resolution.
//   - Updates Cwd from "cd" attribute if present.
//   - Sets PendingResult from "input" attribute if non-empty.
func HandleFunctionWorkflow(
	agent wfstate.AgentState,
	transition parsing.Transition,
	ws *wfstate.WorkflowState,
	resolution specifier.Resolution,
) (TransitionResult, error) {
	returnState, ok := transition.Attributes["return"]
	if !ok || returnState == "" {
		return TransitionResult{}, fmt.Errorf(
			"<function-workflow> tag requires 'return' attribute. " +
				"Example: <function-workflow return=\"NEXT.md\">./child-workflow</function-workflow>",
		)
	}

	if agent.NestingDepth >= 4 {
		return TransitionResult{}, fmt.Errorf(
			"<function-workflow> maximum cross-workflow nesting depth (4) exceeded",
		)
	}

	frame := wfstate.StackFrame{
		Session:      agent.SessionID,
		State:        returnState,
		ScopeDir:     agent.ScopeDir,
		Cwd:          agent.Cwd,
		NestingDepth: agent.NestingDepth,
	}
	agent.Stack = append(agent.Stack, frame)
	agent.NestingDepth = agent.NestingDepth + 1
	agent.SessionID = nil
	agent.ScopeDir = resolution.ScopeDir
	agent.CurrentState = resolution.EntryPoint

	if cd, ok := transition.Attributes["cd"]; ok {
		agent.Cwd = ResolveCd(cd, agent.Cwd)
	}

	if input, ok := transition.Attributes["input"]; ok && input != "" {
		agent.PendingResult = &input
	}

	return TransitionResult{Agent: &agent}, nil
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

	// Restore ScopeDir and Cwd only if the frame has non-empty values.
	// Empty means the frame came from an old state file that predates these fields;
	// in that case we must not overwrite the agent's existing values.
	if frame.ScopeDir != "" {
		agent.ScopeDir = frame.ScopeDir
	}
	if frame.Cwd != "" {
		agent.Cwd = frame.Cwd
	}
	// NestingDepth is always restored; 0 is the correct default for both old frames
	// and frames saved from a depth-0 agent.
	agent.NestingDepth = frame.NestingDepth

	return TransitionResult{Agent: &agent}
}
