"""Backward-compatible functions for testing.

This module provides the original step_agent and _step_agent_script function
signatures that tests depend on, implemented using the new executor architecture.

These functions are provided for backward compatibility with tests that directly
call step_agent rather than run_all_agents. New code should use the executor
architecture directly via run_all_agents.
"""

from pathlib import Path
from typing import Any, Dict, Optional

# Import src.orchestrator as a module to support test patching
import src.orchestrator as orchestrator

from src.orchestrator.bus import EventBus
from src.orchestrator.executors import get_executor, ExecutionContext
from src.orchestrator.transitions import apply_transition


async def step_agent(
    agent: Dict[str, Any],
    state: Dict[str, Any],
    state_dir: Optional[str] = None,
    debug_dir: Optional[Path] = None,
    agent_step_counters: Optional[Dict[str, int]] = None,
    default_model: Optional[str] = None,
    default_effort: Optional[str] = None,
    timeout: Optional[float] = None,
    dangerously_skip_permissions: bool = False,
    quiet: bool = False
) -> Optional[Dict[str, Any]]:
    """Step a single agent: load prompt, invoke Claude Code, parse output, dispatch.

    This function provides backward compatibility with tests that call step_agent
    directly. It delegates to the new executor architecture.

    Args:
        agent: Agent state dictionary
        state: Full workflow state dictionary
        state_dir: Optional custom state directory
        debug_dir: Optional debug directory path (for saving outputs)
        agent_step_counters: Optional dict to track step numbers per agent
        default_model: Optional model to use if not specified in frontmatter
        default_effort: Optional effort level to use if not specified in frontmatter
        timeout: Optional timeout in seconds for Claude Code invocations (default: 600)
        dangerously_skip_permissions: If True, passes --dangerously-skip-permissions
            to Claude instead of --permission-mode acceptEdits.
        quiet: If True, suppress progress messages.

    Returns:
        Updated agent state dictionary, or None if agent terminated
    """
    scope_dir = state["scope_dir"]
    workflow_id = state.get("workflow_id", "unknown")
    current_state = agent["current_state"]

    # Get console reporter if not quiet
    reporter = None if quiet else orchestrator.get_reporter()

    # Create a simple EventBus (no observers needed for step_agent calls)
    bus = EventBus()

    # Create ExecutionContext
    context = ExecutionContext(
        bus=bus,
        workflow_id=workflow_id,
        scope_dir=scope_dir,
        debug_dir=debug_dir,
        state_dir=state_dir,
        default_model=default_model,
        default_effort=default_effort,
        timeout=timeout,
        dangerously_skip_permissions=dangerously_skip_permissions,
        reporter=reporter,
        step_counters=agent_step_counters or {}
    )

    # Get the appropriate executor
    executor = get_executor(current_state)

    # Execute the state
    result = await executor.execute(agent, state, context)

    # Apply the transition
    transition = result.transition
    transition_result = apply_transition(agent, transition, state)

    # Handle different transition results
    if transition_result is None:
        # Agent terminated
        return None
    elif isinstance(transition_result, tuple):
        # Fork transition - (updated parent, new worker)
        updated_agent, new_agent = transition_result

        # Update session_id from execution result
        if result.session_id is not None:
            updated_agent["session_id"] = result.session_id

        # Add new agent to state
        state["agents"].append(new_agent)

        return updated_agent
    else:
        # Normal transition
        updated_agent = transition_result

        # Update session_id from execution result
        if result.session_id is not None:
            updated_agent["session_id"] = result.session_id

        return updated_agent


async def _step_agent_script(
    agent: Dict[str, Any],
    state: Dict[str, Any],
    scope_dir: str,
    workflow_id: str,
    current_state: str,
    agent_id: str,
    session_id: Optional[str],
    debug_dir: Optional[Path],
    agent_step_counters: Optional[Dict[str, int]],
    timeout: Optional[float],
    state_dir: Optional[str],
    reporter
) -> Optional[Dict[str, Any]]:
    """Step an agent through a script state.

    This function provides backward compatibility with tests that call
    _step_agent_script directly. It delegates to step_agent which uses
    the new executor architecture.

    Args:
        agent: Agent state dictionary
        state: Full workflow state dictionary
        scope_dir: Directory containing state files
        workflow_id: Workflow identifier
        current_state: Current state filename
        agent_id: Agent identifier
        session_id: Current session ID
        debug_dir: Optional debug directory path
        agent_step_counters: Optional dict to track step numbers per agent
        timeout: Optional timeout in seconds
        state_dir: Optional custom state directory
        reporter: Console reporter instance

    Returns:
        Updated agent state dictionary, or None if agent terminated
    """
    # Delegate to step_agent - it will detect the script state and use ScriptExecutor
    return await step_agent(
        agent=agent,
        state=state,
        state_dir=state_dir,
        debug_dir=debug_dir,
        agent_step_counters=agent_step_counters,
        timeout=timeout
    )
