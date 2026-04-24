// Package events defines all event types emitted during workflow execution.
//
// Events are plain structs passed by value. They are the communication
// mechanism between the orchestration core and pluggable observers (console
// output, debug logging, etc.).
//
// Convention for optional string fields: empty string "" represents "none",
// replacing Python's Optional[str] = None.
package events

import "time"

// State type constants used in StateStarted.StateType.
const (
	StateTypeMarkdown = "markdown"
	StateTypeScript   = "script"
)

// AgentPaused reason constants used in AgentPaused.Reason.
const (
	PauseReasonUsageLimit  = "usage limit"
	PauseReasonTimeout     = "timeout"
	PauseReasonPromptError = "prompt_error"
	PauseReasonClaudeError = "claude_error"
)

// WorkflowStarted is emitted when a workflow begins execution.
type WorkflowStarted struct {
	WorkflowID string
	ScopeDir   string
	DebugDir   string // empty if debug is disabled
	Timestamp  time.Time
}

// WorkflowCompleted is emitted when all agents in a workflow have terminated.
type WorkflowCompleted struct {
	WorkflowID   string
	TotalCostUSD float64
	Timestamp    time.Time
}

// WorkflowPaused is emitted when all agents in a workflow are paused.
type WorkflowPaused struct {
	WorkflowID       string
	TotalCostUSD     float64
	PausedAgentCount int
	Timestamp        time.Time
}

// WorkflowWaiting is emitted when the workflow is waiting for a usage limit reset.
type WorkflowWaiting struct {
	WorkflowID       string
	TotalCostUSD     float64
	PausedAgentCount int
	ResetTime        time.Time
	WaitSeconds      float64
	Timestamp        time.Time
}

// WorkflowResuming is emitted when the workflow resumes after a usage limit wait.
type WorkflowResuming struct {
	WorkflowID string
	Timestamp  time.Time
}

// StateStarted is emitted when an agent begins executing a state.
type StateStarted struct {
	AgentID   string
	StateName string
	StateType string // "markdown" or "script"
	Timestamp time.Time
}

// StateCompleted is emitted when an agent finishes executing a state.
type StateCompleted struct {
	AgentID      string
	StateName    string
	CostUSD      float64
	TotalCostUSD float64
	SessionID    string // empty for script states
	DurationMS   float64
	InputTokens  *int64
	Timestamp    time.Time
}

// TransitionOccurred is emitted when an agent transitions between states.
// ToState is empty when the agent terminates (result with empty stack).
type TransitionOccurred struct {
	AgentID        string
	FromState      string
	ToState        string // empty if agent terminated
	TransitionType string
	Metadata       map[string]any // e.g. {"spawned_agent_id": "fork-1"}, {"result_payload": "..."}
	Timestamp      time.Time
}

// AgentSpawned is emitted when a fork creates a new agent.
type AgentSpawned struct {
	ParentAgentID string
	NewAgentID    string
	InitialState  string
	Timestamp     time.Time
}

// AgentTerminated is emitted when an agent terminates via a result tag with an empty stack.
type AgentTerminated struct {
	AgentID       string
	ResultPayload string
	Timestamp     time.Time
}

// AgentPaused is emitted when an agent is paused (e.g. rate limit hit).
// Error carries the human-readable failure message when the pause was caused
// by a recoverable validation or resolution failure; it is empty for routine
// pauses such as usage limits.
type AgentPaused struct {
	AgentID   string
	Reason    string
	Error     string
	Timestamp time.Time
}

// AgentAwaitStarted is emitted when an agent enters an await state, waiting
// for human input.
type AgentAwaitStarted struct {
	AgentID   string
	InputID   string
	Prompt    string
	NextState string
	Timeout   string // empty if no timeout
	Timestamp time.Time
}

// AgentAwaitResumed is emitted when an awaiting agent receives input and
// resumes execution.
type AgentAwaitResumed struct {
	AgentID   string
	InputID   string
	Timestamp time.Time
}

// ClaudeStreamOutput is emitted for each JSON object received from the claude stream.
type ClaudeStreamOutput struct {
	AgentID    string
	StateName  string
	StepNumber int
	JSONObject map[string]any
	Timestamp  time.Time
}

// ClaudeInvocationStarted is emitted when a claude invocation begins.
type ClaudeInvocationStarted struct {
	AgentID         string
	StateName       string
	SessionID       string // empty for a new session
	IsFork          bool
	IsReminder      bool
	ReminderAttempt int
	Timestamp       time.Time
}

// ScriptOutput is emitted when a script state completes execution.
type ScriptOutput struct {
	AgentID         string
	StateName       string
	StepNumber      int
	Stdout          string
	Stderr          string
	ExitCode        int
	ExecutionTimeMS float64
	EnvVars         map[string]string
	Timestamp       time.Time
}

// ToolInvocation is emitted when Claude invokes a tool.
// Detail is empty when no detail is available.
type ToolInvocation struct {
	AgentID   string
	ToolName  string
	Detail    string // empty if not applicable
	Timestamp time.Time
}

// ProgressMessage is emitted for text progress messages from Claude.
type ProgressMessage struct {
	AgentID   string
	Message   string
	Timestamp time.Time
}

// ErrorOccurred is emitted when an error occurs during execution.
// CurrentState is empty for workflow-level errors not tied to a specific state.
type ErrorOccurred struct {
	AgentID      string
	ErrorType    string
	ErrorMessage string
	CurrentState string // empty if not in a state
	IsRetryable  bool
	RetryCount   int
	MaxRetries   int
	Timestamp    time.Time
}
