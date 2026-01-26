"""Event dataclasses for orchestrator event bus.

This module defines all events that can be emitted during workflow execution.
Events are frozen dataclasses that describe what happened during orchestration.

Agent-level events include agent_id to support concurrent agents emitting events
simultaneously. Workflow-level events (WorkflowStarted, WorkflowCompleted,
WorkflowPaused) use workflow_id instead since they apply to the entire workflow.

Events are immutable and serve as the communication mechanism between the core
orchestration logic and pluggable observers (debug logging, console output, etc.).
"""

from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, Optional


@dataclass(frozen=True)
class WorkflowStarted:
    """Emitted when a workflow begins execution.

    Attributes:
        workflow_id: Unique identifier for the workflow
        scope_dir: Directory containing the workflow definition
        debug_dir: Directory for debug output (None if debug disabled)
        timestamp: When the workflow started
    """
    workflow_id: str
    scope_dir: str
    debug_dir: Optional[Path]
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class WorkflowCompleted:
    """Emitted when all agents in a workflow have terminated.

    Attributes:
        workflow_id: Unique identifier for the workflow
        total_cost_usd: Total cost accumulated across all agents
        timestamp: When the workflow completed
    """
    workflow_id: str
    total_cost_usd: float
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class WorkflowPaused:
    """Emitted when all agents in a workflow are paused.

    Attributes:
        workflow_id: Unique identifier for the workflow
        total_cost_usd: Total cost accumulated across all agents
        paused_agent_count: Number of agents currently paused
        timestamp: When the workflow was paused
    """
    workflow_id: str
    total_cost_usd: float
    paused_agent_count: int
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class StateStarted:
    """Emitted when an agent begins executing a state.

    Attributes:
        agent_id: Identifier of the agent executing the state
        state_name: Name of the state file (e.g., "START.md", "CHECK.sh")
        state_type: Type of state ("markdown" or "script")
        timestamp: When execution started
    """
    agent_id: str
    state_name: str
    state_type: str  # "markdown" or "script"
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class StateCompleted:
    """Emitted when an agent finishes executing a state.

    Attributes:
        agent_id: Identifier of the agent
        state_name: Name of the state file
        cost_usd: Cost of this specific invocation
        total_cost_usd: Workflow-wide accumulated total cost
        session_id: Claude Code session ID (None for scripts)
        duration_ms: Execution time in milliseconds
        timestamp: When execution completed
    """
    agent_id: str
    state_name: str
    cost_usd: float
    total_cost_usd: float
    session_id: Optional[str]
    duration_ms: float
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class TransitionOccurred:
    """Emitted when an agent transitions between states.

    Attributes:
        agent_id: Identifier of the agent
        from_state: State being transitioned from
        to_state: State being transitioned to (None if terminated)
        transition_type: Type of transition ("goto", "reset", "function", "call", "fork", "result")
        metadata: Additional transition-specific data (e.g., spawned_agent_id for fork)
        timestamp: When the transition occurred
    """
    agent_id: str
    from_state: str
    to_state: Optional[str]
    transition_type: str
    metadata: Dict[str, Any] = field(default_factory=dict)
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class AgentSpawned:
    """Emitted when a fork creates a new agent.

    Attributes:
        parent_agent_id: Identifier of the parent agent
        new_agent_id: Identifier assigned to the new agent
        initial_state: Starting state for the new agent
        timestamp: When the agent was spawned
    """
    parent_agent_id: str
    new_agent_id: str
    initial_state: str
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class AgentTerminated:
    """Emitted when an agent terminates.

    Attributes:
        agent_id: Identifier of the agent
        result_payload: The result value from the <result> tag
        timestamp: When the agent terminated
    """
    agent_id: str
    result_payload: str
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class AgentPaused:
    """Emitted when an agent is paused.

    Attributes:
        agent_id: Identifier of the agent
        reason: Reason for pausing (e.g., "timeout", "limit")
        timestamp: When the agent was paused
    """
    agent_id: str
    reason: str
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class ClaudeStreamOutput:
    """Emitted for each JSON object received from Claude Code stream.

    Used for debug logging - writes progressive JSONL output.

    Attributes:
        agent_id: Identifier of the agent
        state_name: Name of the state being executed
        step_number: Step number within the workflow (for file naming)
        json_object: The JSON object from the stream
        timestamp: When the output was received
    """
    agent_id: str
    state_name: str
    step_number: int
    json_object: Dict[str, Any]
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class ClaudeInvocationStarted:
    """Emitted when a Claude Code invocation begins.

    Attributes:
        agent_id: Identifier of the agent
        state_name: Name of the state being executed
        session_id: Session ID for resumption (None for new session)
        is_fork: Whether this invocation is for a forked agent
        is_reminder: Whether this is a reminder prompt retry
        reminder_attempt: Attempt number for reminder prompts (0 if not a reminder)
        timestamp: When the invocation started
    """
    agent_id: str
    state_name: str
    session_id: Optional[str]
    is_fork: bool
    is_reminder: bool
    reminder_attempt: int
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class ScriptOutput:
    """Emitted when a script execution completes.

    Attributes:
        agent_id: Identifier of the agent
        state_name: Name of the script state
        step_number: Step number within the workflow
        stdout: Standard output from the script
        stderr: Standard error from the script
        exit_code: Exit code from the script
        execution_time_ms: How long the script ran
        env_vars: Environment variables passed to the script
        timestamp: When the output was captured
    """
    agent_id: str
    state_name: str
    step_number: int
    stdout: str
    stderr: str
    exit_code: int
    execution_time_ms: float
    env_vars: Dict[str, str]
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class ToolInvocation:
    """Emitted when Claude invokes a tool.

    Attributes:
        agent_id: Identifier of the agent
        tool_name: Name of the tool (e.g., "Read", "Write", "Bash")
        detail: Optional detail (filename for Read/Write, command preview for Bash)
        timestamp: When the tool was invoked
    """
    agent_id: str
    tool_name: str
    detail: Optional[str] = None
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class ProgressMessage:
    """Emitted for text progress messages from Claude.

    Attributes:
        agent_id: Identifier of the agent
        message: The progress message text
        timestamp: When the message was emitted
    """
    agent_id: str
    message: str
    timestamp: datetime = field(default_factory=datetime.now)


@dataclass(frozen=True)
class ErrorOccurred:
    """Emitted when an error occurs during execution.

    Attributes:
        agent_id: Identifier of the agent (or "workflow" for workflow-level errors)
        error_type: Type/class name of the error
        error_message: Human-readable error message
        current_state: State being executed when error occurred
        is_retryable: Whether the error can be retried
        retry_count: Current retry attempt number
        max_retries: Maximum retries configured
        timestamp: When the error occurred
    """
    agent_id: str
    error_type: str
    error_message: str
    current_state: Optional[str]
    is_retryable: bool
    retry_count: int
    max_retries: int
    timestamp: datetime = field(default_factory=datetime.now)


# All event types for type checking and registration
ALL_EVENTS = (
    WorkflowStarted,
    WorkflowCompleted,
    WorkflowPaused,
    StateStarted,
    StateCompleted,
    TransitionOccurred,
    AgentSpawned,
    AgentTerminated,
    AgentPaused,
    ClaudeStreamOutput,
    ClaudeInvocationStarted,
    ScriptOutput,
    ToolInvocation,
    ProgressMessage,
    ErrorOccurred,
)
