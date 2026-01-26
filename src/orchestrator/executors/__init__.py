"""Executors package for state execution.

This package provides the executor abstraction for handling different state types.
Executors are polymorphic classes that execute states and return results containing
parsed transitions.

Usage:
    from src.orchestrator.executors import get_executor, ExecutionContext, ExecutionResult

    context = ExecutionContext(bus=bus, workflow_id=wf_id, scope_dir=scope_dir)
    executor = get_executor(agent["current_state"])
    result = await executor.execute(agent, state, context)

Exports:
    - get_executor: Factory function to get appropriate executor for a state
    - ExecutionContext: Context dataclass with execution parameters
    - ExecutionResult: Result dataclass with transition, session_id, cost
    - StateExecutor: Protocol for executor implementations
    - MarkdownExecutor: Executor for .md states (Claude Code)
    - ScriptExecutor: Executor for .sh/.bat states (subprocess)
"""

from src.orchestrator.executors.base import ExecutionResult, StateExecutor
from src.orchestrator.executors.context import ExecutionContext
from src.orchestrator.executors.markdown import MarkdownExecutor
from src.orchestrator.executors.script import ScriptExecutor

# Import get_state_type to determine executor type
import src.orchestrator as orchestrator

# Singleton instances (executors are stateless)
_markdown_executor = MarkdownExecutor()
_script_executor = ScriptExecutor()


def get_executor(state_filename: str) -> StateExecutor:
    """Get the appropriate executor for a state file.

    Uses the state file extension to determine which executor to use:
    - .md files are handled by MarkdownExecutor
    - .sh and .bat files are handled by ScriptExecutor

    Args:
        state_filename: The state filename (e.g., "START.md", "CHECK.sh")

    Returns:
        The appropriate executor for the state type

    Raises:
        ValueError: If the state type is unknown
    """
    state_type = orchestrator.get_state_type(state_filename)

    if state_type == "markdown":
        return _markdown_executor
    elif state_type == "script":
        return _script_executor
    else:
        raise ValueError(f"Unknown state type: {state_type} for {state_filename}")


__all__ = [
    "get_executor",
    "ExecutionContext",
    "ExecutionResult",
    "StateExecutor",
    "MarkdownExecutor",
    "ScriptExecutor",
]
