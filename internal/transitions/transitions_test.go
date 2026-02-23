package transitions_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/parsing"
	wfstate "github.com/vector76/raymond/internal/state"
	"github.com/vector76/raymond/internal/transitions"
)

// helpers

func strPtr(s string) *string { return &s }

func makeAgent(id, currentState string, sessionID *string) wfstate.AgentState {
	return wfstate.AgentState{
		ID:           id,
		CurrentState: currentState,
		SessionID:    sessionID,
		Stack:        []wfstate.StackFrame{},
	}
}

func gotoTransition(target string) parsing.Transition {
	return parsing.Transition{Tag: "goto", Target: target, Attributes: map[string]string{}}
}

// ----------------------------------------------------------------------------
// ApplyTransition: deep copy and dispatch
// ----------------------------------------------------------------------------

func TestApplyTransitionDeepCopiesAgent(t *testing.T) {
	original := makeAgent("main", "START.md", strPtr("session_123"))
	tr := gotoTransition("NEXT.md")
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&original, tr, wfState)

	require.NoError(t, err)
	// Original must not be mutated.
	assert.Equal(t, "START.md", original.CurrentState)
	// Result reflects the transition.
	require.NotNil(t, result.Agent)
	assert.Equal(t, "NEXT.md", result.Agent.CurrentState)
}

func TestApplyTransitionClearsPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.PendingResult = strPtr("previous result")
	tr := gotoTransition("NEXT.md")

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Nil(t, result.Agent.PendingResult)
}

func TestApplyTransitionClearsForkSessionID(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ForkSessionID = strPtr("forked_session")
	tr := gotoTransition("NEXT.md")

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Nil(t, result.Agent.ForkSessionID)
}

func TestApplyTransitionClearsForkAttributes(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ForkAttributes = map[string]string{"item": "task_123"}
	tr := gotoTransition("NEXT.md")

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Nil(t, result.Agent.ForkAttributes)
}

func TestApplyTransitionDispatchesGoto(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := gotoTransition("NEXT.md")

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "NEXT.md", result.Agent.CurrentState)
	require.NotNil(t, result.Agent.SessionID)
	assert.Equal(t, "session_123", *result.Agent.SessionID) // preserved
}

func TestApplyTransitionDispatchesReset(t *testing.T) {
	frame := wfstate.StackFrame{Session: strPtr("old"), State: "OLD.md"}
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Stack = []wfstate.StackFrame{frame}
	tr := parsing.Transition{Tag: "reset", Target: "FRESH.md", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "FRESH.md", result.Agent.CurrentState)
	assert.Nil(t, result.Agent.SessionID)             // cleared
	assert.Empty(t, result.Agent.Stack)               // cleared
}

func TestApplyTransitionDispatchesFunction(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "function", Target: "EVAL.md",
		Attributes: map[string]string{"return": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "EVAL.md", result.Agent.CurrentState)
	assert.Nil(t, result.Agent.SessionID) // fresh for function
	require.Len(t, result.Agent.Stack, 1)
	assert.Equal(t, "session_123", *result.Agent.Stack[0].Session)
	assert.Equal(t, "NEXT.md", result.Agent.Stack[0].State)
}

func TestApplyTransitionDispatchesCall(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "call", Target: "CHILD.md",
		Attributes: map[string]string{"return": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "CHILD.md", result.Agent.CurrentState)
	require.NotNil(t, result.Agent.ForkSessionID)
	assert.Equal(t, "session_123", *result.Agent.ForkSessionID) // set for --fork-session
	require.Len(t, result.Agent.Stack, 1)
}

func TestApplyTransitionDispatchesFork(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	require.NotNil(t, result.Worker)
	assert.Equal(t, "NEXT.md", result.Agent.CurrentState)
	assert.Equal(t, "WORKER.md", result.Worker.CurrentState)
	assert.Equal(t, "main_worker1", result.Worker.ID)
}

func TestApplyTransitionDispatchesResultTermination(t *testing.T) {
	agent := makeAgent("main", "END.md", strPtr("session_123"))
	// Empty stack — agent terminates.
	tr := parsing.Transition{Tag: "result", Payload: "Task completed", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Nil(t, result.Agent) // terminated
	assert.Nil(t, result.Worker)
}

func TestApplyTransitionDispatchesResultReturn(t *testing.T) {
	frame := wfstate.StackFrame{Session: strPtr("caller_session"), State: "RETURN.md"}
	agent := makeAgent("main", "EVAL.md", nil)
	agent.Stack = []wfstate.StackFrame{frame}
	tr := parsing.Transition{Tag: "result", Payload: "evaluation result", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "RETURN.md", result.Agent.CurrentState)
	require.NotNil(t, result.Agent.SessionID)
	assert.Equal(t, "caller_session", *result.Agent.SessionID) // restored
	require.NotNil(t, result.Agent.PendingResult)
	assert.Equal(t, "evaluation result", *result.Agent.PendingResult)
	assert.Empty(t, result.Agent.Stack) // popped
}

func TestApplyTransitionUnknownTagReturnsError(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "unknown_tag", Target: "SOMEWHERE.md", Attributes: map[string]string{}}

	_, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown transition tag: unknown_tag")
}

// ----------------------------------------------------------------------------
// Transient field clearing
// ----------------------------------------------------------------------------

func TestAllTransientFieldsCleared(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.PendingResult = strPtr("old result")
	agent.ForkSessionID = strPtr("old fork session")
	agent.ForkAttributes = map[string]string{"item": "old item"}
	tr := gotoTransition("NEXT.md")

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Nil(t, result.Agent.PendingResult)
	assert.Nil(t, result.Agent.ForkSessionID)
	assert.Nil(t, result.Agent.ForkAttributes)
}

func TestTransientFieldsClearedBeforeHandlerSetsNew(t *testing.T) {
	frame := wfstate.StackFrame{Session: strPtr("old_session"), State: "OLD.md"}
	agent := makeAgent("main", "EVAL.md", nil)
	agent.Stack = []wfstate.StackFrame{frame}
	agent.PendingResult = strPtr("old result") // will be cleared, then set fresh by result handler
	tr := parsing.Transition{Tag: "result", Payload: "new result", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent.PendingResult)
	assert.Equal(t, "new result", *result.Agent.PendingResult)
}

func TestCallHandlerSetsForkSessionIDFresh(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("new_session"))
	agent.ForkSessionID = strPtr("old_fork_session") // will be cleared, then set fresh
	tr := parsing.Transition{
		Tag: "call", Target: "CHILD.md",
		Attributes: map[string]string{"return": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent.ForkSessionID)
	// fork_session_id should be the caller's session (new_session), not the old value
	assert.Equal(t, "new_session", *result.Agent.ForkSessionID)
}

// ----------------------------------------------------------------------------
// ResolveCd
// ----------------------------------------------------------------------------

func TestResolveCdAbsolutePathReturnedAsIs(t *testing.T) {
	assert.Equal(t, "/absolute/path", transitions.ResolveCd("/absolute/path", ""))
}

func TestResolveCdAbsolutePathNormalized(t *testing.T) {
	assert.Equal(t, "/a/c/d", transitions.ResolveCd("/a/b/../c/./d", ""))
}

func TestResolveCdRelativeWithBaseCwd(t *testing.T) {
	assert.Equal(t, "/base/subdir", transitions.ResolveCd("subdir", "/base"))
}

func TestResolveCdRelativeDotdotWithBase(t *testing.T) {
	assert.Equal(t, "/base/sibling", transitions.ResolveCd("../sibling", "/base/child"))
}

func TestResolveCdRelativeNoBaseUsesWd(t *testing.T) {
	result := transitions.ResolveCd("subdir", "")
	wd, err := os.Getwd()
	require.NoError(t, err)
	expected := filepath.Clean(filepath.Join(wd, "subdir"))
	assert.Equal(t, expected, result)
}

func TestResolveCdComplexRelativeNormalized(t *testing.T) {
	assert.Equal(t, "/repo/baz", transitions.ResolveCd("../foo/../bar/../baz", "/repo/project"))
}

func TestResolveCdDotPath(t *testing.T) {
	assert.Equal(t, "/base/dir", transitions.ResolveCd(".", "/base/dir"))
}

func TestResolveCdAbsoluteIgnoresBase(t *testing.T) {
	assert.Equal(t, "/new/path", transitions.ResolveCd("/new/path", "/old/path"))
}

// ----------------------------------------------------------------------------
// Reset transition: cd attribute
// ----------------------------------------------------------------------------

func TestResetWithAbsoluteCdSetsAgentCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "reset", Target: "FRESH.md",
		Attributes: map[string]string{"cd": "/path/to/worktree"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "FRESH.md", result.Agent.CurrentState)
	assert.Nil(t, result.Agent.SessionID)
	assert.Equal(t, "/path/to/worktree", result.Agent.Cwd)
}

func TestResetWithoutCdDoesNotChangeCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	// No cwd set on agent, no cd in transition.
	tr := parsing.Transition{Tag: "reset", Target: "FRESH.md", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "", result.Agent.Cwd)
}

func TestResetPreservesExistingCwdWhenNoCd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/existing/path"
	tr := parsing.Transition{Tag: "reset", Target: "FRESH.md", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "/existing/path", result.Agent.Cwd)
}

func TestResetWithCdOverridesExistingCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/old/path"
	tr := parsing.Transition{
		Tag: "reset", Target: "FRESH.md",
		Attributes: map[string]string{"cd": "/new/path"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "/new/path", result.Agent.Cwd)
}

func TestResetRelativeCdResolvedAgainstAgentCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/repo/project"
	tr := parsing.Transition{
		Tag: "reset", Target: "FRESH.md",
		Attributes: map[string]string{"cd": "../other-project"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "/repo/other-project", result.Agent.Cwd)
}

func TestResetRelativeCdUsesWdWhenNoAgentCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "reset", Target: "FRESH.md",
		Attributes: map[string]string{"cd": "subdir"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	wd, err2 := os.Getwd()
	require.NoError(t, err2)
	assert.Equal(t, filepath.Clean(filepath.Join(wd, "subdir")), result.Agent.Cwd)
}

func TestResetCdNormalizesPath(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/repo/project"
	tr := parsing.Transition{
		Tag: "reset", Target: "FRESH.md",
		Attributes: map[string]string{"cd": "../foo/../bar/../baz"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "/repo/baz", result.Agent.Cwd)
}

func TestResetAbsoluteCdNormalized(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "reset", Target: "FRESH.md",
		Attributes: map[string]string{"cd": "/a/b/../c/./d"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "/a/c/d", result.Agent.Cwd)
}

// ----------------------------------------------------------------------------
// Fork transition: cd attribute
// ----------------------------------------------------------------------------

func TestForkWithAbsoluteCdSetsWorkerCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md", "cd": "/path/to/worktree"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "/path/to/worktree", result.Worker.Cwd)
	assert.Equal(t, "", result.Agent.Cwd) // parent unchanged
}

func TestForkWithoutCdDoesNotSetWorkerCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "", result.Worker.Cwd)
}

func TestForkCdNotInForkAttributes(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md", "cd": "/path/to/worktree", "item": "task1"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	_, hasCd := result.Worker.ForkAttributes["cd"]
	assert.False(t, hasCd, "cd must not be in fork attributes")
	assert.Equal(t, "task1", result.Worker.ForkAttributes["item"])
}

func TestForkParentPreservesItsCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/parent/dir"
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md", "cd": "/worker/dir"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "/parent/dir", result.Agent.Cwd)
	assert.Equal(t, "/worker/dir", result.Worker.Cwd)
}

func TestForkRelativeCdResolvedAgainstParentCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/repo"
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md", "cd": "worktrees/feature-a"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "/repo/worktrees/feature-a", result.Worker.Cwd)
	assert.Equal(t, "/repo", result.Agent.Cwd)
}

func TestForkRelativeCdUsesWdWhenNoParentCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md", "cd": "worktrees/feature-a"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	wd, err2 := os.Getwd()
	require.NoError(t, err2)
	expected := filepath.Clean(filepath.Join(wd, "worktrees/feature-a"))
	assert.Equal(t, expected, result.Worker.Cwd)
}

func TestForkCdNormalizesPath(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/repo"
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md", "cd": "./a/../b/./c"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "/repo/b/c", result.Worker.Cwd)
}

// ----------------------------------------------------------------------------
// Handler-level error cases
// ----------------------------------------------------------------------------

func TestFunctionMissingReturnAttrErrors(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "function", Target: "EVAL.md", Attributes: map[string]string{}}

	_, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "return")
}

func TestCallMissingReturnAttrErrors(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "call", Target: "CHILD.md", Attributes: map[string]string{}}

	_, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "return")
}

func TestForkMissingNextAttrErrors(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "fork", Target: "WORKER.md", Attributes: map[string]string{}}

	_, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "next")
}

// ----------------------------------------------------------------------------
// Fork counter persistence and worker ID generation
// ----------------------------------------------------------------------------

func TestForkCounterIncrementsPerParent(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	wfState := &wfstate.WorkflowState{}
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md"},
	}

	result1, err := transitions.ApplyTransition(&agent, tr, wfState)
	require.NoError(t, err)

	// Use updated parent for second fork
	result2, err := transitions.ApplyTransition(result1.Agent, tr, wfState)
	require.NoError(t, err)

	assert.Equal(t, "main_worker1", result1.Worker.ID)
	assert.Equal(t, "main_worker2", result2.Worker.ID)
}

func TestForkWorkerIDUsesStateAbbrev(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	wfState := &wfstate.WorkflowState{}
	// "ANALYZE.md" → abbrev "analyz" (6 chars)
	tr := parsing.Transition{
		Tag: "fork", Target: "ANALYZE.md",
		Attributes: map[string]string{"next": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	assert.Equal(t, "main_analyz1", result.Worker.ID)
}

func TestForkWorkerIDShortStateName(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	wfState := &wfstate.WorkflowState{}
	// "RUN.sh" → abbrev "run" (less than 6 chars, keep as-is)
	tr := parsing.Transition{
		Tag: "fork", Target: "RUN.sh",
		Attributes: map[string]string{"next": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	assert.Equal(t, "main_run1", result.Worker.ID)
}

func TestForkWorkerHasEmptyStack(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Empty(t, result.Worker.Stack)
	assert.Nil(t, result.Worker.SessionID)
}

func TestForkExtraAttributesBecomeWorkerForkAttributes(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md", "task": "build", "env": "prod"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "build", result.Worker.ForkAttributes["task"])
	assert.Equal(t, "prod", result.Worker.ForkAttributes["env"])
	_, hasNext := result.Worker.ForkAttributes["next"]
	assert.False(t, hasNext, "'next' must not be in fork attributes")
}

func TestForkNoExtraAttributesGivesNilForkAttributes(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Nil(t, result.Worker.ForkAttributes)
}

// ----------------------------------------------------------------------------
// Result: termination stores result in wfState
// ----------------------------------------------------------------------------

func TestResultTerminationStoresResultInWfState(t *testing.T) {
	agent := makeAgent("main", "END.md", strPtr("session_123"))
	wfState := &wfstate.WorkflowState{}
	tr := parsing.Transition{Tag: "result", Payload: "final answer", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	assert.Nil(t, result.Agent)
	assert.Equal(t, "final answer", wfState.AgentTerminationResults["main"])
}

// ----------------------------------------------------------------------------
// Stack depth: function/call with multiple frames
// ----------------------------------------------------------------------------

func TestFunctionPushesOntoExistingStack(t *testing.T) {
	existingFrame := wfstate.StackFrame{Session: strPtr("old_sess"), State: "OUTER.md"}
	agent := makeAgent("main", "MID.md", strPtr("mid_session"))
	agent.Stack = []wfstate.StackFrame{existingFrame}
	tr := parsing.Transition{
		Tag: "function", Target: "INNER.md",
		Attributes: map[string]string{"return": "MID.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.Len(t, result.Agent.Stack, 2)
	assert.Equal(t, "OUTER.md", result.Agent.Stack[0].State)
	assert.Equal(t, "MID.md", result.Agent.Stack[1].State)
}

func TestResultPopsOnlyTopFrame(t *testing.T) {
	frame1 := wfstate.StackFrame{Session: strPtr("sess1"), State: "OUTER.md"}
	frame2 := wfstate.StackFrame{Session: strPtr("sess2"), State: "INNER.md"}
	agent := makeAgent("main", "LEAF.md", nil)
	agent.Stack = []wfstate.StackFrame{frame1, frame2}
	tr := parsing.Transition{Tag: "result", Payload: "leaf result", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "INNER.md", result.Agent.CurrentState) // top frame popped
	require.NotNil(t, result.Agent.SessionID)
	assert.Equal(t, "sess2", *result.Agent.SessionID) // restored from top frame
	require.Len(t, result.Agent.Stack, 1)
	assert.Equal(t, "OUTER.md", result.Agent.Stack[0].State) // bottom frame remains
}
