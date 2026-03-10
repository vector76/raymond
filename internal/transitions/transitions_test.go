package transitions_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/specifier"
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
	require.Len(t, result.Agent.Stack, 1)             // preserved
	assert.Equal(t, "OLD.md", result.Agent.Stack[0].State)
	require.NotNil(t, result.Agent.Stack[0].Session)
	assert.Equal(t, "old", *result.Agent.Stack[0].Session)
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

func TestForkWorkerInheritsScopeDir(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = "/abs/path/to/workflow"
	tr := parsing.Transition{
		Tag: "fork", Target: "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	assert.Equal(t, "/abs/path/to/workflow", result.Worker.ScopeDir)
	assert.Equal(t, "/abs/path/to/workflow", result.Agent.ScopeDir) // parent unchanged
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

// ----------------------------------------------------------------------------
// StackFrame ScopeDir/Cwd/NestingDepth: push saves, pop restores
// ----------------------------------------------------------------------------

func TestFunctionPushSavesScopeDirAndCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = "workflows/myapp"
	agent.Cwd = "/repo/myapp"
	tr := parsing.Transition{
		Tag: "function", Target: "EVAL.md",
		Attributes: map[string]string{"return": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.Len(t, result.Agent.Stack, 1)
	assert.Equal(t, "workflows/myapp", result.Agent.Stack[0].ScopeDir)
	assert.Equal(t, "/repo/myapp", result.Agent.Stack[0].Cwd)
}

func TestCallPushSavesScopeDirAndCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = "workflows/service"
	agent.Cwd = "/repo/service"
	tr := parsing.Transition{
		Tag: "call", Target: "CHILD.md",
		Attributes: map[string]string{"return": "NEXT.md"},
	}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.Len(t, result.Agent.Stack, 1)
	assert.Equal(t, "workflows/service", result.Agent.Stack[0].ScopeDir)
	assert.Equal(t, "/repo/service", result.Agent.Stack[0].Cwd)
}

func TestResultRestoresScopeDirAndCwdFromFrame(t *testing.T) {
	frame := wfstate.StackFrame{
		Session:  strPtr("caller_session"),
		State:    "RETURN.md",
		ScopeDir: "workflows/caller",
		Cwd:      "/repo/caller",
	}
	agent := makeAgent("main", "EVAL.md", nil)
	agent.ScopeDir = "workflows/callee"
	agent.Cwd = "/repo/callee"
	agent.Stack = []wfstate.StackFrame{frame}
	tr := parsing.Transition{Tag: "result", Payload: "done", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "workflows/caller", result.Agent.ScopeDir)
	assert.Equal(t, "/repo/caller", result.Agent.Cwd)
}

func TestResultDoesNotOverwriteScopeDirCwdFromOldFrame(t *testing.T) {
	// Old frame has empty ScopeDir and Cwd (loaded from pre-existing state file).
	frame := wfstate.StackFrame{
		Session:  strPtr("caller_session"),
		State:    "RETURN.md",
		ScopeDir: "", // old frame — field absent
		Cwd:      "", // old frame — field absent
	}
	agent := makeAgent("main", "EVAL.md", nil)
	agent.ScopeDir = "workflows/current"
	agent.Cwd = "/repo/current"
	agent.Stack = []wfstate.StackFrame{frame}
	tr := parsing.Transition{Tag: "result", Payload: "done", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	// Old frame must not overwrite existing agent values.
	assert.Equal(t, "workflows/current", result.Agent.ScopeDir)
	assert.Equal(t, "/repo/current", result.Agent.Cwd)
}

func TestResultAlwaysRestoresNestingDepth(t *testing.T) {
	frame := wfstate.StackFrame{
		Session:      strPtr("caller_session"),
		State:        "RETURN.md",
		NestingDepth: 0,
	}
	agent := makeAgent("main", "EVAL.md", nil)
	agent.Stack = []wfstate.StackFrame{frame}
	tr := parsing.Transition{Tag: "result", Payload: "done", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, 0, result.Agent.NestingDepth)
}

// ----------------------------------------------------------------------------
// HandleForkWorkflow
// ----------------------------------------------------------------------------

func makeResolution(scopeDir, entryPoint, abbrev string) specifier.Resolution {
	return specifier.Resolution{
		ScopeDir:   scopeDir,
		EntryPoint: entryPoint,
		Abbrev:     abbrev,
	}
}

func forkWorkflowTransition(attrs map[string]string) parsing.Transition {
	return parsing.Transition{
		Tag:        "fork-workflow",
		Attributes: attrs,
	}
}

func TestForkWorkflowWorkerFieldsSetCorrectly(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/repo"
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md"})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.NotNil(t, result.Worker)
	assert.Equal(t, "/workflows/child", result.Worker.ScopeDir)
	assert.Equal(t, "1_START.md", result.Worker.CurrentState)
	assert.Nil(t, result.Worker.SessionID)
	assert.Nil(t, result.Worker.ForkSessionID)
	assert.Empty(t, result.Worker.Stack)
}

func TestForkWorkflowWorkerIDFormat(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/myapp", "1_START.md", "myapp")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md"})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Equal(t, "main_myapp1", result.Worker.ID)
}

func TestForkWorkflowCounterIncrementsPerAbbrev(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md"})

	result1, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)
	require.NoError(t, err)

	// Use updated caller for second fork.
	result2, err := transitions.HandleForkWorkflow(*result1.Agent, tr, wfState, resolution)
	require.NoError(t, err)

	assert.Equal(t, "main_child1", result1.Worker.ID)
	assert.Equal(t, "main_child2", result2.Worker.ID)
}

func TestForkWorkflowCounterKeyIncludesAbbrev(t *testing.T) {
	// Two different abbrevs under the same parent get independent counters.
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md"})

	res1 := makeResolution("/workflows/alpha", "1_START.md", "alpha")
	res2 := makeResolution("/workflows/beta", "1_START.md", "beta")

	r1, err := transitions.HandleForkWorkflow(agent, tr, wfState, res1)
	require.NoError(t, err)
	r2, err := transitions.HandleForkWorkflow(*r1.Agent, tr, wfState, res2)
	require.NoError(t, err)

	// Each abbrev starts at counter 1 independently.
	assert.Equal(t, "main_alpha1", r1.Worker.ID)
	assert.Equal(t, "main_beta1", r2.Worker.ID)
}

func TestForkWorkflowCwdAttributeOverridesInheritance(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/parent/dir"
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md", "cd": "/worker/dir"})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Equal(t, "/worker/dir", result.Worker.Cwd)
	assert.Equal(t, "/parent/dir", result.Agent.Cwd) // parent unchanged
}

func TestForkWorkflowRelativeCdResolved(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/parent/dir"
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md", "cd": "../sibling"})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Equal(t, "/parent/sibling", result.Worker.Cwd)
}

func TestForkWorkflowAbsentCwdInheritsCallerCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/parent/dir"
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md"})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Equal(t, "/parent/dir", result.Worker.Cwd)
}

func TestForkWorkflowInputAttributeSetsWorkerPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md", "input": "task data"})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.NotNil(t, result.Worker.PendingResult)
	assert.Equal(t, "task data", *result.Worker.PendingResult)
}

func TestForkWorkflowAbsentInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md"})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Nil(t, result.Worker.PendingResult)
}

func TestForkWorkflowEmptyInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md", "input": ""})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Nil(t, result.Worker.PendingResult)
}

func TestForkWorkflowWithNextCallerAdvances(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md"})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "AFTER.md", result.Agent.CurrentState)
	require.NotNil(t, result.Worker)
}

func TestForkWorkflowWithoutNextCallerNotAdvanced(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "START.md", result.Agent.CurrentState) // unchanged
	require.NotNil(t, result.Worker)
}

func TestForkWorkflowWorkerInheritsNestingDepth(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.NestingDepth = 2
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md"})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Worker.NestingDepth)
}

func TestForkWorkflowInitialisesNilForkCounters(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{} // ForkCounters is nil
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md"})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Equal(t, "main_child1", result.Worker.ID)
	assert.NotNil(t, wfState.ForkCounters)
}

// ----------------------------------------------------------------------------
// HandleCallWorkflow
// ----------------------------------------------------------------------------

func callWorkflowTransition(attrs map[string]string) parsing.Transition {
	return parsing.Transition{
		Tag:        "call-workflow",
		Attributes: attrs,
	}
}

func functionWorkflowTransition(attrs map[string]string) parsing.Transition {
	return parsing.Transition{
		Tag:        "function-workflow",
		Attributes: attrs,
	}
}

func TestCallWorkflowHappyPath(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = "/workflows/caller"
	agent.Cwd = "/repo/caller"
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := callWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleCallWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "/workflows/child", result.Agent.ScopeDir)
	assert.Equal(t, "1_START.md", result.Agent.CurrentState)
	assert.Nil(t, result.Agent.SessionID)
	require.NotNil(t, result.Agent.ForkSessionID)
	assert.Equal(t, "session_123", *result.Agent.ForkSessionID)
}

func TestCallWorkflowPushesStackFrame(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = "/workflows/caller"
	agent.Cwd = "/repo/caller"
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := callWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleCallWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.Len(t, result.Agent.Stack, 1)
	frame := result.Agent.Stack[0]
	require.NotNil(t, frame.Session)
	assert.Equal(t, "session_123", *frame.Session)
	assert.Equal(t, "AFTER.md", frame.State)
	assert.Equal(t, "/workflows/caller", frame.ScopeDir)
	assert.Equal(t, "/repo/caller", frame.Cwd)
	assert.Equal(t, 0, frame.NestingDepth)
}

func TestCallWorkflowMissingReturnErrors(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := callWorkflowTransition(map[string]string{})

	_, err := transitions.HandleCallWorkflow(agent, tr, wfState, resolution)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "return")
}

func TestCallWorkflowCwdAttributeForbidden(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := callWorkflowTransition(map[string]string{"return": "AFTER.md", "cd": "/some/path"})

	_, err := transitions.HandleCallWorkflow(agent, tr, wfState, resolution)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cd")
}

func TestCallWorkflowSetsForkSessionID(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("caller_session"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := callWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleCallWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.NotNil(t, result.Agent.ForkSessionID)
	assert.Equal(t, "caller_session", *result.Agent.ForkSessionID)
	assert.Nil(t, result.Agent.SessionID)
}

func TestCallWorkflowInputAttributeSetsPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := callWorkflowTransition(map[string]string{"return": "AFTER.md", "input": "task data"})

	result, err := transitions.HandleCallWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.NotNil(t, result.Agent.PendingResult)
	assert.Equal(t, "task data", *result.Agent.PendingResult)
}

func TestCallWorkflowAbsentInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := callWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleCallWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Nil(t, result.Agent.PendingResult)
}

func TestCallWorkflowEmptyInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := callWorkflowTransition(map[string]string{"return": "AFTER.md", "input": ""})

	result, err := transitions.HandleCallWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Nil(t, result.Agent.PendingResult)
}

func TestCallWorkflowNoWorkerReturned(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := callWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleCallWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Nil(t, result.Worker)
}

// ----------------------------------------------------------------------------
// HandleFunctionWorkflow
// ----------------------------------------------------------------------------

func TestFunctionWorkflowHappyPath(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = "/workflows/caller"
	agent.Cwd = "/repo/caller"
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "/workflows/child", result.Agent.ScopeDir)
	assert.Equal(t, "1_START.md", result.Agent.CurrentState)
	assert.Nil(t, result.Agent.SessionID)
	assert.Nil(t, result.Agent.ForkSessionID) // no context inheritance
}

func TestFunctionWorkflowPushesStackFrame(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = "/workflows/caller"
	agent.Cwd = "/repo/caller"
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.Len(t, result.Agent.Stack, 1)
	frame := result.Agent.Stack[0]
	require.NotNil(t, frame.Session)
	assert.Equal(t, "session_123", *frame.Session)
	assert.Equal(t, "AFTER.md", frame.State)
	assert.Equal(t, "/workflows/caller", frame.ScopeDir)
	assert.Equal(t, "/repo/caller", frame.Cwd)
	assert.Equal(t, 0, frame.NestingDepth)
}

func TestFunctionWorkflowMissingReturnErrors(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{})

	_, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "return")
}

func TestFunctionWorkflowCwdAttributeUpdatesCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/old/path"
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md", "cd": "/new/path"})

	result, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Equal(t, "/new/path", result.Agent.Cwd)
}

func TestFunctionWorkflowRelativeCdResolved(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/old/path"
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md", "cd": "../sibling"})

	result, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Equal(t, "/old/sibling", result.Agent.Cwd)
}

func TestFunctionWorkflowAbsentCwdPreservesAgentCwd(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.Cwd = "/existing/path"
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Equal(t, "/existing/path", result.Agent.Cwd)
}

func TestFunctionWorkflowDoesNotSetForkSessionID(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Nil(t, result.Agent.ForkSessionID)
}

func TestFunctionWorkflowInputAttributeSetsPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md", "input": "my input"})

	result, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.NotNil(t, result.Agent.PendingResult)
	assert.Equal(t, "my input", *result.Agent.PendingResult)
}

func TestFunctionWorkflowAbsentInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Nil(t, result.Agent.PendingResult)
}

func TestFunctionWorkflowEmptyInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md", "input": ""})

	result, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Nil(t, result.Agent.PendingResult)
}

func TestFunctionWorkflowNoWorkerReturned(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	assert.Nil(t, result.Worker)
}

func TestCallWorkflowVsFunctionWorkflowForkSessionDifference(t *testing.T) {
	// call-workflow sets ForkSessionID; function-workflow does not.
	agent := makeAgent("main", "START.md", strPtr("sess"))
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	retAttr := map[string]string{"return": "AFTER.md"}

	callResult, err := transitions.HandleCallWorkflow(agent, callWorkflowTransition(retAttr), wfState, resolution)
	require.NoError(t, err)

	funcResult, err := transitions.HandleFunctionWorkflow(agent, functionWorkflowTransition(retAttr), wfState, resolution)
	require.NoError(t, err)

	assert.NotNil(t, callResult.Agent.ForkSessionID, "call-workflow must set ForkSessionID")
	assert.Nil(t, funcResult.Agent.ForkSessionID, "function-workflow must NOT set ForkSessionID")
}

// ----------------------------------------------------------------------------
// ApplyTransition: cross-workflow dispatch via specifier.Resolve
// ----------------------------------------------------------------------------

// makeChildWorkflow creates a temporary directory containing 1_START.md and
// returns its absolute path. The directory is removed after the test completes.
func makeChildWorkflow(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("# start"), 0o644))
	return dir
}

func TestApplyTransitionDispatchesCallWorkflow(t *testing.T) {
	childDir := makeChildWorkflow(t)

	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = filepath.Dir(childDir)
	tr := parsing.Transition{
		Tag:    "call-workflow",
		Target: childDir,
		Attributes: map[string]string{
			"return": "NEXT.md",
		},
	}
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Empty(t, result.Agent.Status) // active, not paused
	assert.Equal(t, childDir, result.Agent.ScopeDir)
	assert.Equal(t, "1_START.md", result.Agent.CurrentState)
	assert.NotNil(t, result.Agent.ForkSessionID, "call-workflow sets ForkSessionID")
}

func TestApplyTransitionCallWorkflowResolverErrorPausesAgent(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = "/some/scope"
	tr := parsing.Transition{
		Tag:    "call-workflow",
		Target: "/nonexistent/no/such/path",
		Attributes: map[string]string{
			"return": "NEXT.md",
		},
	}
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err) // error is converted to a paused agent, not propagated
	require.NotNil(t, result.Agent)
	assert.Equal(t, "paused", result.Agent.Status)
	assert.NotEmpty(t, result.Agent.Error)
	assert.Contains(t, result.Agent.Error, "call-workflow")
}

func TestApplyTransitionFunctionWorkflowResolverErrorPausesAgent(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = "/some/scope"
	tr := parsing.Transition{
		Tag:    "function-workflow",
		Target: "/nonexistent/no/such/path",
		Attributes: map[string]string{
			"return": "NEXT.md",
		},
	}
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "paused", result.Agent.Status)
	assert.Contains(t, result.Agent.Error, "function-workflow")
}

func TestApplyTransitionForkWorkflowResolverErrorPausesAgent(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = "/some/scope"
	tr := parsing.Transition{
		Tag:    "fork-workflow",
		Target: "/nonexistent/no/such/path",
		Attributes: map[string]string{
			"next": "AFTER.md",
		},
	}
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "paused", result.Agent.Status)
	assert.Contains(t, result.Agent.Error, "fork-workflow")
}

func TestApplyTransitionCallWorkflowInputTemplateRendered(t *testing.T) {
	childDir := makeChildWorkflow(t)

	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = filepath.Dir(childDir)
	agent.PendingResult = strPtr("computed-value")
	tr := parsing.Transition{
		Tag:    "call-workflow",
		Target: childDir,
		Attributes: map[string]string{
			"return": "NEXT.md",
			"input":  "Process: {{result}}",
		},
	}
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	require.NotNil(t, result.Agent.PendingResult)
	assert.Equal(t, "Process: computed-value", *result.Agent.PendingResult)
}

func TestApplyTransitionFunctionWorkflowInputTemplateRenderedFromForkAttrs(t *testing.T) {
	childDir := makeChildWorkflow(t)

	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = filepath.Dir(childDir)
	agent.ForkAttributes = map[string]string{"item": "widget"}
	tr := parsing.Transition{
		Tag:    "function-workflow",
		Target: childDir,
		Attributes: map[string]string{
			"return": "NEXT.md",
			"input":  "Build: {{item}}",
		},
	}
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	require.NotNil(t, result.Agent.PendingResult)
	assert.Equal(t, "Build: widget", *result.Agent.PendingResult)
}

// ----------------------------------------------------------------------------
// Nesting depth tracking
// ----------------------------------------------------------------------------

func TestCallWorkflowDepth3IncrementsTo4AndSavesFrameDepth(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.NestingDepth = 3
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := callWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleCallWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, 4, result.Agent.NestingDepth)
	require.Len(t, result.Agent.Stack, 1)
	assert.Equal(t, 3, result.Agent.Stack[0].NestingDepth)
}

func TestFunctionWorkflowDepth3IncrementsTo4AndSavesFrameDepth(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.NestingDepth = 3
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md"})

	result, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, 4, result.Agent.NestingDepth)
	require.Len(t, result.Agent.Stack, 1)
	assert.Equal(t, 3, result.Agent.Stack[0].NestingDepth)
}

func TestCallWorkflowDepth4ReturnsError(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.NestingDepth = 4
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := callWorkflowTransition(map[string]string{"return": "AFTER.md"})

	_, err := transitions.HandleCallWorkflow(agent, tr, wfState, resolution)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nesting depth")
}

func TestFunctionWorkflowDepth4ReturnsError(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.NestingDepth = 4
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := functionWorkflowTransition(map[string]string{"return": "AFTER.md"})

	_, err := transitions.HandleFunctionWorkflow(agent, tr, wfState, resolution)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nesting depth")
}

func TestCallWorkflowDepth4PausesAgentViaApplyTransition(t *testing.T) {
	childDir := makeChildWorkflow(t)

	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = filepath.Dir(childDir)
	agent.NestingDepth = 4
	tr := parsing.Transition{
		Tag:        "call-workflow",
		Target:     childDir,
		Attributes: map[string]string{"return": "NEXT.md"},
	}
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "paused", result.Agent.Status)
	assert.Contains(t, result.Agent.Error, "nesting depth")
}

func TestFunctionWorkflowDepth4PausesAgentViaApplyTransition(t *testing.T) {
	childDir := makeChildWorkflow(t)

	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.ScopeDir = filepath.Dir(childDir)
	agent.NestingDepth = 4
	tr := parsing.Transition{
		Tag:        "function-workflow",
		Target:     childDir,
		Attributes: map[string]string{"return": "NEXT.md"},
	}
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, "paused", result.Agent.Status)
	assert.Contains(t, result.Agent.Error, "nesting depth")
}

func TestForkWorkflowWorkerInheritsDepthNotIncremented(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.NestingDepth = 3
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}
	tr := forkWorkflowTransition(map[string]string{"next": "AFTER.md"})

	result, err := transitions.HandleForkWorkflow(agent, tr, wfState, resolution)

	require.NoError(t, err)
	// Worker inherits caller's depth (not incremented).
	assert.Equal(t, 3, result.Worker.NestingDepth)
	// Caller's own depth is unchanged.
	assert.Equal(t, 3, result.Agent.NestingDepth)
}

func TestResultRestoresDepthToPreCallValue(t *testing.T) {
	// Simulate agent at depth 1 (after a cross-workflow call), with frame saving depth 0.
	frame := wfstate.StackFrame{
		Session:      strPtr("caller_session"),
		State:        "RETURN.md",
		NestingDepth: 0,
	}
	agent := makeAgent("main", "EVAL.md", nil)
	agent.NestingDepth = 1
	agent.Stack = []wfstate.StackFrame{frame}
	tr := parsing.Transition{Tag: "result", Payload: "done", Attributes: map[string]string{}}

	result, err := transitions.ApplyTransition(&agent, tr, &wfstate.WorkflowState{})

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Equal(t, 0, result.Agent.NestingDepth)
}

func TestSequentialCallsDoNotAccumulateDepth(t *testing.T) {
	// Call at depth 0 → depth 1 → result restores to 0 → call again → depth 1 (not 2).
	resolution := makeResolution("/workflows/child", "1_START.md", "child")
	wfState := &wfstate.WorkflowState{}

	agent := makeAgent("main", "START.md", strPtr("session_a"))
	agent.NestingDepth = 0

	// First call: depth 0 → 1.
	r1, err := transitions.HandleCallWorkflow(agent, callWorkflowTransition(map[string]string{"return": "AFTER.md"}), wfState, resolution)
	require.NoError(t, err)
	assert.Equal(t, 1, r1.Agent.NestingDepth)

	// Simulate result: pop frame, restore depth to 0.
	r1.Agent.Stack[0].NestingDepth = 0
	resultTr := parsing.Transition{Tag: "result", Payload: "done", Attributes: map[string]string{}}
	r2, err := transitions.ApplyTransition(r1.Agent, resultTr, wfState)
	require.NoError(t, err)
	assert.Equal(t, 0, r2.Agent.NestingDepth)

	// Second call: depth 0 → 1 (not 2).
	r3, err := transitions.HandleCallWorkflow(*r2.Agent, callWorkflowTransition(map[string]string{"return": "AFTER.md"}), wfState, resolution)
	require.NoError(t, err)
	assert.Equal(t, 1, r3.Agent.NestingDepth)
}

func TestIntraScopeForkDoesNotChangeDepth(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.NestingDepth = 2
	tr := parsing.Transition{
		Tag:        "fork",
		Target:     "WORKER.md",
		Attributes: map[string]string{"next": "NEXT.md"},
	}
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Agent.NestingDepth)
	assert.Equal(t, 0, result.Worker.NestingDepth) // intra-scope worker is independent
}

func TestIntraScopeCallDoesNotChangeDepth(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.NestingDepth = 2
	tr := parsing.Transition{
		Tag:        "call",
		Target:     "CHILD.md",
		Attributes: map[string]string{"return": "NEXT.md"},
	}
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Agent.NestingDepth)
}

func TestIntraScopeFunctionDoesNotChangeDepth(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.NestingDepth = 2
	tr := parsing.Transition{
		Tag:        "function",
		Target:     "EVAL.md",
		Attributes: map[string]string{"return": "NEXT.md"},
	}
	wfState := &wfstate.WorkflowState{}

	result, err := transitions.ApplyTransition(&agent, tr, wfState)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Agent.NestingDepth)
}

func TestIntraScopeFunctionResultPreservesDepth(t *testing.T) {
	// Agent at depth 2 makes an intra-scope function call; after result, depth must remain 2.
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.NestingDepth = 2
	wfState := &wfstate.WorkflowState{}

	// Intra-scope function call.
	callTr := parsing.Transition{
		Tag:        "function",
		Target:     "EVAL.md",
		Attributes: map[string]string{"return": "NEXT.md"},
	}
	r1, err := transitions.ApplyTransition(&agent, callTr, wfState)
	require.NoError(t, err)
	assert.Equal(t, 2, r1.Agent.NestingDepth) // unchanged during call

	// Intra-scope result.
	resultTr := parsing.Transition{Tag: "result", Payload: "done", Attributes: map[string]string{}}
	r2, err := transitions.ApplyTransition(r1.Agent, resultTr, wfState)
	require.NoError(t, err)
	require.NotNil(t, r2.Agent)
	assert.Equal(t, 2, r2.Agent.NestingDepth) // must be preserved, not reset to 0
}

func TestIntraScopeCallResultPreservesDepth(t *testing.T) {
	// Agent at depth 2 makes an intra-scope call; after result, depth must remain 2.
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	agent.NestingDepth = 2
	wfState := &wfstate.WorkflowState{}

	// Intra-scope call.
	callTr := parsing.Transition{
		Tag:        "call",
		Target:     "CHILD.md",
		Attributes: map[string]string{"return": "NEXT.md"},
	}
	r1, err := transitions.ApplyTransition(&agent, callTr, wfState)
	require.NoError(t, err)
	assert.Equal(t, 2, r1.Agent.NestingDepth) // unchanged during call

	// Intra-scope result.
	resultTr := parsing.Transition{Tag: "result", Payload: "done", Attributes: map[string]string{}}
	r2, err := transitions.ApplyTransition(r1.Agent, resultTr, wfState)
	require.NoError(t, err)
	require.NotNil(t, r2.Agent)
	assert.Equal(t, 2, r2.Agent.NestingDepth) // must be preserved, not reset to 0
}

// ----------------------------------------------------------------------------
// HandleGoto: input attribute
// ----------------------------------------------------------------------------

func TestGotoInputAttributeSetsPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "goto", Target: "NEXT.md", Attributes: map[string]string{"input": "task data"}}

	result := transitions.HandleGoto(agent, tr)

	require.NotNil(t, result.Agent)
	require.NotNil(t, result.Agent.PendingResult)
	assert.Equal(t, "task data", *result.Agent.PendingResult)
}

func TestGotoAbsentInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "goto", Target: "NEXT.md", Attributes: map[string]string{}}

	result := transitions.HandleGoto(agent, tr)

	require.NotNil(t, result.Agent)
	assert.Nil(t, result.Agent.PendingResult)
}

func TestGotoEmptyInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "goto", Target: "NEXT.md", Attributes: map[string]string{"input": ""}}

	result := transitions.HandleGoto(agent, tr)

	require.NotNil(t, result.Agent)
	assert.Nil(t, result.Agent.PendingResult)
}

// ----------------------------------------------------------------------------
// HandleReset: input attribute
// ----------------------------------------------------------------------------

func TestResetInputAttributeSetsPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "reset", Target: "START.md", Attributes: map[string]string{"input": "task data"}}

	result := transitions.HandleReset(agent, tr)

	require.NotNil(t, result.Agent)
	require.NotNil(t, result.Agent.PendingResult)
	assert.Equal(t, "task data", *result.Agent.PendingResult)
}

func TestResetAbsentInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "reset", Target: "START.md", Attributes: map[string]string{}}

	result := transitions.HandleReset(agent, tr)

	require.NotNil(t, result.Agent)
	assert.Nil(t, result.Agent.PendingResult)
}

func TestResetEmptyInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "reset", Target: "START.md", Attributes: map[string]string{"input": ""}}

	result := transitions.HandleReset(agent, tr)

	require.NotNil(t, result.Agent)
	assert.Nil(t, result.Agent.PendingResult)
}

// ----------------------------------------------------------------------------
// HandleFunction: input attribute
// ----------------------------------------------------------------------------

func TestFunctionInputAttributeSetsPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "function", Target: "EVAL.md", Attributes: map[string]string{"return": "NEXT.md", "input": "task data"}}

	result, err := transitions.HandleFunction(agent, tr)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	require.NotNil(t, result.Agent.PendingResult)
	assert.Equal(t, "task data", *result.Agent.PendingResult)
}

func TestFunctionAbsentInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "function", Target: "EVAL.md", Attributes: map[string]string{"return": "NEXT.md"}}

	result, err := transitions.HandleFunction(agent, tr)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Nil(t, result.Agent.PendingResult)
}

func TestFunctionEmptyInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "function", Target: "EVAL.md", Attributes: map[string]string{"return": "NEXT.md", "input": ""}}

	result, err := transitions.HandleFunction(agent, tr)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Nil(t, result.Agent.PendingResult)
}

// ----------------------------------------------------------------------------
// HandleCall: input attribute
// ----------------------------------------------------------------------------

func TestCallInputAttributeSetsPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "call", Target: "CHILD.md", Attributes: map[string]string{"return": "NEXT.md", "input": "task data"}}

	result, err := transitions.HandleCall(agent, tr)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	require.NotNil(t, result.Agent.PendingResult)
	assert.Equal(t, "task data", *result.Agent.PendingResult)
}

func TestCallAbsentInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "call", Target: "CHILD.md", Attributes: map[string]string{"return": "NEXT.md"}}

	result, err := transitions.HandleCall(agent, tr)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Nil(t, result.Agent.PendingResult)
}

func TestCallEmptyInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	tr := parsing.Transition{Tag: "call", Target: "CHILD.md", Attributes: map[string]string{"return": "NEXT.md", "input": ""}}

	result, err := transitions.HandleCall(agent, tr)

	require.NoError(t, err)
	require.NotNil(t, result.Agent)
	assert.Nil(t, result.Agent.PendingResult)
}

// ----------------------------------------------------------------------------
// CreateForkWorker: input attribute
// ----------------------------------------------------------------------------

func TestForkWorkerInputAttributeSetsPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	wfState := &wfstate.WorkflowState{}
	tr := parsing.Transition{Tag: "fork", Target: "WORKER.md", Attributes: map[string]string{"next": "AFTER.md", "input": "task data"}}

	worker, err := transitions.CreateForkWorker(agent, tr, wfState)

	require.NoError(t, err)
	require.NotNil(t, worker.PendingResult)
	assert.Equal(t, "task data", *worker.PendingResult)
}

func TestForkWorkerAbsentInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	wfState := &wfstate.WorkflowState{}
	tr := parsing.Transition{Tag: "fork", Target: "WORKER.md", Attributes: map[string]string{"next": "AFTER.md"}}

	worker, err := transitions.CreateForkWorker(agent, tr, wfState)

	require.NoError(t, err)
	assert.Nil(t, worker.PendingResult)
}

func TestForkWorkerEmptyInputLeavesNilPendingResult(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	wfState := &wfstate.WorkflowState{}
	tr := parsing.Transition{Tag: "fork", Target: "WORKER.md", Attributes: map[string]string{"next": "AFTER.md", "input": ""}}

	worker, err := transitions.CreateForkWorker(agent, tr, wfState)

	require.NoError(t, err)
	assert.Nil(t, worker.PendingResult)
}

func TestForkWorkerInputNotInForkAttributes(t *testing.T) {
	agent := makeAgent("main", "START.md", strPtr("session_123"))
	wfState := &wfstate.WorkflowState{}
	tr := parsing.Transition{Tag: "fork", Target: "WORKER.md", Attributes: map[string]string{"next": "AFTER.md", "input": "task data", "extra": "value"}}

	worker, err := transitions.CreateForkWorker(agent, tr, wfState)

	require.NoError(t, err)
	assert.NotContains(t, worker.ForkAttributes, "input")
	assert.Equal(t, "value", worker.ForkAttributes["extra"])
}

