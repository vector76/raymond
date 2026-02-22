package events_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/vector76/raymond/internal/events"
)

// ----------------------------------------------------------------------------
// Workflow-level events
// ----------------------------------------------------------------------------

func TestWorkflowStartedConstruction(t *testing.T) {
	e := events.WorkflowStarted{
		WorkflowID: "test-001",
		ScopeDir:   "/path/to/scope",
		DebugDir:   "/path/to/debug",
		Timestamp:  time.Now(),
	}
	assert.Equal(t, "test-001", e.WorkflowID)
	assert.Equal(t, "/path/to/scope", e.ScopeDir)
	assert.Equal(t, "/path/to/debug", e.DebugDir)
	assert.False(t, e.Timestamp.IsZero())
}

func TestWorkflowStartedDebugDirEmpty(t *testing.T) {
	// Empty DebugDir signals debug is disabled (replaces Python's Optional[Path]=None)
	e := events.WorkflowStarted{
		WorkflowID: "test-001",
		ScopeDir:   "/path/to/scope",
		DebugDir:   "",
	}
	assert.Equal(t, "", e.DebugDir)
}

func TestWorkflowCompletedConstruction(t *testing.T) {
	e := events.WorkflowCompleted{
		WorkflowID:   "test-001",
		TotalCostUSD: 1.23,
		Timestamp:    time.Now(),
	}
	assert.Equal(t, "test-001", e.WorkflowID)
	assert.Equal(t, 1.23, e.TotalCostUSD)
}

func TestWorkflowPausedConstruction(t *testing.T) {
	e := events.WorkflowPaused{
		WorkflowID:       "test-001",
		TotalCostUSD:     0.50,
		PausedAgentCount: 2,
		Timestamp:        time.Now(),
	}
	assert.Equal(t, "test-001", e.WorkflowID)
	assert.Equal(t, 0.50, e.TotalCostUSD)
	assert.Equal(t, 2, e.PausedAgentCount)
}

func TestWorkflowWaitingConstruction(t *testing.T) {
	reset := time.Now().Add(time.Minute)
	e := events.WorkflowWaiting{
		WorkflowID:       "test-001",
		TotalCostUSD:     0.50,
		PausedAgentCount: 1,
		ResetTime:        reset,
		WaitSeconds:      60.0,
		Timestamp:        time.Now(),
	}
	assert.Equal(t, 60.0, e.WaitSeconds)
	assert.Equal(t, reset, e.ResetTime)
}

func TestWorkflowResumingConstruction(t *testing.T) {
	e := events.WorkflowResuming{
		WorkflowID: "test-001",
		Timestamp:  time.Now(),
	}
	assert.Equal(t, "test-001", e.WorkflowID)
}

// ----------------------------------------------------------------------------
// State execution events
// ----------------------------------------------------------------------------

func TestStateStartedConstruction(t *testing.T) {
	e := events.StateStarted{
		AgentID:   "main",
		StateName: "START.md",
		StateType: "markdown",
		Timestamp: time.Now(),
	}
	assert.Equal(t, "main", e.AgentID)
	assert.Equal(t, "START.md", e.StateName)
	assert.Equal(t, "markdown", e.StateType)
}

func TestStateStartedScriptType(t *testing.T) {
	e := events.StateStarted{
		AgentID:   "main",
		StateName: "CHECK.sh",
		StateType: "script",
	}
	assert.Equal(t, "script", e.StateType)
}

func TestStateCompletedConstruction(t *testing.T) {
	e := events.StateCompleted{
		AgentID:      "main",
		StateName:    "START.md",
		CostUSD:      0.05,
		TotalCostUSD: 0.15,
		SessionID:    "sess-123",
		DurationMS:   1500.5,
		Timestamp:    time.Now(),
	}
	assert.Equal(t, "main", e.AgentID)
	assert.Equal(t, 0.05, e.CostUSD)
	assert.Equal(t, 0.15, e.TotalCostUSD)
	assert.Equal(t, "sess-123", e.SessionID)
	assert.Equal(t, 1500.5, e.DurationMS)
}

func TestStateCompletedSessionIDEmpty(t *testing.T) {
	// Empty SessionID for script states (replaces Python's Optional[str]=None)
	e := events.StateCompleted{
		AgentID:      "main",
		StateName:    "CHECK.sh",
		CostUSD:      0.0,
		TotalCostUSD: 0.10,
		SessionID:    "",
		DurationMS:   50.0,
	}
	assert.Equal(t, "", e.SessionID)
}

// ----------------------------------------------------------------------------
// Transition events
// ----------------------------------------------------------------------------

func TestTransitionOccurredConstruction(t *testing.T) {
	e := events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "START.md",
		ToState:        "NEXT.md",
		TransitionType: "goto",
		Timestamp:      time.Now(),
	}
	assert.Equal(t, "main", e.AgentID)
	assert.Equal(t, "START.md", e.FromState)
	assert.Equal(t, "NEXT.md", e.ToState)
	assert.Equal(t, "goto", e.TransitionType)
	assert.Nil(t, e.Metadata) // default nil map is fine
}

func TestTransitionOccurredWithMetadata(t *testing.T) {
	e := events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "START.md",
		ToState:        "FORK.md",
		TransitionType: "fork",
		Metadata:       map[string]any{"spawned_agent_id": "fork-1"},
	}
	assert.Equal(t, "fork-1", e.Metadata["spawned_agent_id"])
}

func TestTransitionOccurredToStateEmpty(t *testing.T) {
	// Empty ToState signals agent termination (replaces Python's Optional[str]=None)
	e := events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "END.md",
		ToState:        "",
		TransitionType: "result",
	}
	assert.Equal(t, "", e.ToState)
}

func TestAgentSpawnedConstruction(t *testing.T) {
	e := events.AgentSpawned{
		ParentAgentID: "main",
		NewAgentID:    "fork-1",
		InitialState:  "PROCESS.md",
		Timestamp:     time.Now(),
	}
	assert.Equal(t, "main", e.ParentAgentID)
	assert.Equal(t, "fork-1", e.NewAgentID)
	assert.Equal(t, "PROCESS.md", e.InitialState)
}

func TestAgentTerminatedConstruction(t *testing.T) {
	e := events.AgentTerminated{
		AgentID:       "main",
		ResultPayload: "Task completed successfully",
		Timestamp:     time.Now(),
	}
	assert.Equal(t, "main", e.AgentID)
	assert.Equal(t, "Task completed successfully", e.ResultPayload)
}

func TestAgentPausedConstruction(t *testing.T) {
	e := events.AgentPaused{
		AgentID:   "main",
		Reason:    "timeout",
		Timestamp: time.Now(),
	}
	assert.Equal(t, "main", e.AgentID)
	assert.Equal(t, "timeout", e.Reason)
}

// ----------------------------------------------------------------------------
// Claude stream events
// ----------------------------------------------------------------------------

func TestClaudeStreamOutputConstruction(t *testing.T) {
	jsonObj := map[string]any{"type": "content", "text": "Hello"}
	e := events.ClaudeStreamOutput{
		AgentID:    "main",
		StateName:  "START.md",
		StepNumber: 1,
		JSONObject: jsonObj,
		Timestamp:  time.Now(),
	}
	assert.Equal(t, "main", e.AgentID)
	assert.Equal(t, 1, e.StepNumber)
	assert.Equal(t, jsonObj, e.JSONObject)
}

func TestClaudeInvocationStartedConstruction(t *testing.T) {
	e := events.ClaudeInvocationStarted{
		AgentID:         "main",
		StateName:       "START.md",
		SessionID:       "",
		IsFork:          false,
		IsReminder:      false,
		ReminderAttempt: 0,
		Timestamp:       time.Now(),
	}
	assert.Equal(t, "main", e.AgentID)
	assert.Equal(t, "", e.SessionID)
	assert.False(t, e.IsFork)
	assert.False(t, e.IsReminder)
	assert.Equal(t, 0, e.ReminderAttempt)
}

func TestClaudeInvocationStartedReminder(t *testing.T) {
	e := events.ClaudeInvocationStarted{
		AgentID:         "main",
		StateName:       "START.md",
		SessionID:       "sess-123",
		IsFork:          false,
		IsReminder:      true,
		ReminderAttempt: 2,
	}
	assert.True(t, e.IsReminder)
	assert.Equal(t, 2, e.ReminderAttempt)
	assert.Equal(t, "sess-123", e.SessionID)
}

// ----------------------------------------------------------------------------
// Script events
// ----------------------------------------------------------------------------

func TestScriptOutputConstruction(t *testing.T) {
	e := events.ScriptOutput{
		AgentID:         "main",
		StateName:       "CHECK.sh",
		StepNumber:      3,
		Stdout:          "<goto>NEXT.md</goto>\n",
		Stderr:          "",
		ExitCode:        0,
		ExecutionTimeMS: 125.5,
		EnvVars:         map[string]string{"WORKFLOW_ID": "test-001"},
		Timestamp:       time.Now(),
	}
	assert.Equal(t, "main", e.AgentID)
	assert.Equal(t, 3, e.StepNumber)
	assert.Equal(t, "<goto>NEXT.md</goto>\n", e.Stdout)
	assert.Equal(t, 0, e.ExitCode)
	assert.Equal(t, 125.5, e.ExecutionTimeMS)
}

func TestScriptOutputWithErrors(t *testing.T) {
	e := events.ScriptOutput{
		AgentID:    "main",
		StateName:  "CHECK.sh",
		StepNumber: 3,
		Stdout:     "",
		Stderr:     "Error: file not found",
		ExitCode:   1,
	}
	assert.Equal(t, 1, e.ExitCode)
	assert.Equal(t, "Error: file not found", e.Stderr)
}

// ----------------------------------------------------------------------------
// Tool events
// ----------------------------------------------------------------------------

func TestToolInvocationConstruction(t *testing.T) {
	e := events.ToolInvocation{
		AgentID:   "main",
		ToolName:  "Read",
		Detail:    "src/main.py",
		Timestamp: time.Now(),
	}
	assert.Equal(t, "main", e.AgentID)
	assert.Equal(t, "Read", e.ToolName)
	assert.Equal(t, "src/main.py", e.Detail)
}

func TestToolInvocationNoDetail(t *testing.T) {
	// Empty Detail replaces Python's Optional[str]=None
	e := events.ToolInvocation{
		AgentID:  "main",
		ToolName: "Bash",
		Detail:   "",
	}
	assert.Equal(t, "", e.Detail)
}

func TestProgressMessageConstruction(t *testing.T) {
	e := events.ProgressMessage{
		AgentID:   "main",
		Message:   "Processing data...",
		Timestamp: time.Now(),
	}
	assert.Equal(t, "main", e.AgentID)
	assert.Equal(t, "Processing data...", e.Message)
}

// ----------------------------------------------------------------------------
// Error events
// ----------------------------------------------------------------------------

func TestErrorOccurredConstruction(t *testing.T) {
	e := events.ErrorOccurred{
		AgentID:      "main",
		ErrorType:    "ClaudeCodeError",
		ErrorMessage: "Connection failed",
		CurrentState: "START.md",
		IsRetryable:  true,
		RetryCount:   1,
		MaxRetries:   3,
		Timestamp:    time.Now(),
	}
	assert.Equal(t, "main", e.AgentID)
	assert.Equal(t, "ClaudeCodeError", e.ErrorType)
	assert.Equal(t, "Connection failed", e.ErrorMessage)
	assert.Equal(t, "START.md", e.CurrentState)
	assert.True(t, e.IsRetryable)
	assert.Equal(t, 1, e.RetryCount)
	assert.Equal(t, 3, e.MaxRetries)
}

func TestErrorOccurredNotRetryable(t *testing.T) {
	e := events.ErrorOccurred{
		AgentID:      "main",
		ErrorType:    "ClaudeCodeLimitError",
		ErrorMessage: "Usage limit exceeded",
		CurrentState: "START.md",
		IsRetryable:  false,
		RetryCount:   0,
		MaxRetries:   3,
	}
	assert.False(t, e.IsRetryable)
}

func TestErrorOccurredCurrentStateEmpty(t *testing.T) {
	// Empty CurrentState replaces Python's Optional[str]=None
	e := events.ErrorOccurred{
		AgentID:      "workflow",
		ErrorType:    "StateFileError",
		ErrorMessage: "State file corrupted",
		CurrentState: "",
		IsRetryable:  false,
		RetryCount:   0,
		MaxRetries:   3,
	}
	assert.Equal(t, "", e.CurrentState)
}
