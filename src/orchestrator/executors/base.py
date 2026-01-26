"""Base types for state executors.

This module defines the StateExecutor protocol and ExecutionResult dataclass
that form the foundation of the executor abstraction. Executors are responsible
for executing individual states (markdown or script) and returning the result.

The protocol allows for polymorphic handling of different state types while
maintaining a consistent interface for the workflow loop.
"""

from dataclasses import dataclass
from typing import Any, Dict, Optional, Protocol, TYPE_CHECKING

if TYPE_CHECKING:
    from src.parsing import Transition
    from src.orchestrator.executors.context import ExecutionContext


@dataclass
class ExecutionResult:
    """Result of executing a state.

    Contains the parsed and resolved transition, session information,
    and cost tracking data.

    Attributes:
        transition: The parsed and resolved transition to apply
        session_id: New/updated session ID (None for scripts, which preserve existing)
        cost_usd: Cost of this invocation (0.0 for scripts)
    """
    transition: "Transition"
    session_id: Optional[str]
    cost_usd: float


class StateExecutor(Protocol):
    """Protocol for state executors.

    Executors handle the execution of different state types (markdown, script).
    Each executor is responsible for:
    1. Emitting StateStarted at the beginning
    2. Performing the actual execution (Claude Code or subprocess)
    3. Emitting streaming/output events during execution
    4. Parsing and validating the transition
    5. Emitting StateCompleted at the end
    6. Returning the result (transition handlers are called by the workflow loop)

    Executors are stateless singletons - all mutable state is in the agent dict
    and ExecutionContext.
    """

    async def execute(
        self,
        agent: Dict[str, Any],
        state: Dict[str, Any],
        context: "ExecutionContext"
    ) -> ExecutionResult:
        """Execute a state and return the result.

        Args:
            agent: Agent state dictionary containing:
                - id: Agent identifier
                - current_state: State filename to execute
                - session_id: Current session ID (may be None)
                - stack: Return stack for function/call transitions
                - pending_result: Result from previous function/call return (optional)
                - fork_session_id: Session to fork from (optional, for call transitions)
                - fork_attributes: Template variables from fork (optional)
            state: Full workflow state dictionary containing:
                - scope_dir: Directory containing state files
                - workflow_id: Workflow identifier
                - agents: List of all agents
                - total_cost_usd: Accumulated cost
                - budget_usd: Budget limit
            context: ExecutionContext with shared execution parameters

        Returns:
            ExecutionResult containing the parsed transition, session ID, and cost

        Raises:
            ClaudeCodeError: If Claude Code execution fails (markdown states)
            ClaudeCodeLimitError: If Claude Code hits usage limit (non-retryable)
            ClaudeCodeTimeoutWrappedError: If Claude Code times out (allows pause/resume)
            ScriptError: If script execution fails (script states)
            PromptFileError: If prompt file operations fail (markdown states)
            ValueError: If transition parsing or validation fails
        """
        ...
