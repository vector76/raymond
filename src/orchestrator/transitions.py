"""Transition handlers for agent state transitions.

This module contains the 6 transition handlers that process different
transition tags emitted by agents:
- goto: Simple state transition with session resume
- reset: Fresh start with new session
- function: Stateless/pure evaluation that returns to caller
- call: Subroutine-like workflow with context branching
- fork: Spawn independent agent while parent continues
- result: Return from function/call or terminate agent

Each handler takes (agent, transition, state) and returns an updated agent
dictionary (or None for termination, or tuple for fork).

The apply_transition() wrapper provides a unified interface for applying
transitions, handling deep copying and transient field cleanup.
"""

import copy
import logging
import os
from typing import Any, Dict, Optional, Tuple, Union

from src.parsing import Transition

logger = logging.getLogger(__name__)


def _resolve_cd(cd_value: str, base_cwd: Optional[str]) -> str:
    """Resolve a cd attribute value to an absolute, normalized path.

    If cd_value is absolute, it is normalized and returned as-is.
    If cd_value is relative, it is resolved against base_cwd (the agent's
    current working directory). If base_cwd is None (agent has no cwd set),
    relative paths are resolved against the orchestrator's working directory.

    Args:
        cd_value: The cd attribute value from the transition tag.
        base_cwd: The agent's current cwd, or None if unset.

    Returns:
        Absolute, normalized path string.
    """
    if os.path.isabs(cd_value):
        return os.path.normpath(cd_value)
    base = base_cwd if base_cwd is not None else os.getcwd()
    return os.path.normpath(os.path.join(base, cd_value))


# Map transition tags to their handler functions
_TRANSITION_HANDLERS: Dict[str, Any] = {}  # Populated after function definitions


def apply_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
) -> Union[Dict[str, Any], Tuple[Dict[str, Any], Dict[str, Any]], None]:
    """Apply a transition to an agent, returning the updated agent state.

    This wrapper function:
    1. Deep copies the agent to avoid mutating the original
    2. Clears transient fields (pending_result, fork_session_id, fork_attributes)
    3. Dispatches to the appropriate handler based on transition.tag
    4. Returns the updated agent (or None for termination, or tuple for fork)

    Note: Event emission (TransitionOccurred, AgentSpawned, AgentTerminated) will
    be added in Phase 6 when the EventBus is integrated into the workflow.

    Args:
        agent: Agent state dictionary (will not be mutated)
        transition: Transition object with tag, target, attributes, payload
        state: Full workflow state dictionary (may be mutated by fork handler)

    Returns:
        - Updated agent dictionary for most transitions
        - Tuple of (updated parent, new worker) for fork transitions
        - None if agent terminates (result with empty stack)

    Raises:
        ValueError: If transition.tag is unknown
    """
    # Deep copy to avoid mutating the original agent
    agent_copy = copy.deepcopy(agent)

    # Clear transient fields that should not persist across transitions
    # These are set by handlers and consumed by the next step
    for field in ("pending_result", "fork_session_id", "fork_attributes"):
        agent_copy.pop(field, None)

    # Dispatch to appropriate handler
    handler = _TRANSITION_HANDLERS.get(transition.tag)
    if handler is None:
        raise ValueError(f"Unknown transition tag: {transition.tag}")

    return handler(agent_copy, transition, state)


def handle_goto_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
) -> Dict[str, Any]:
    """Handle <goto> transition tag.

    Updates agent's current_state to the transition target.
    Preserves session_id for resume in next step.
    Preserves return stack unchanged.

    Note: The transition target is already resolved by step_agent before this
    handler is called, so we use transition.target directly.

    Args:
        agent: Agent state dictionary
        transition: Transition object with resolved target filename
        state: Full workflow state dictionary (unused, for handler signature consistency)

    Returns:
        Updated agent state dictionary
    """
    agent["current_state"] = transition.target
    # session_id handling:
    # - For markdown states: step_agent updates session_id from Claude Code before calling this handler
    # - For script states: session_id is preserved (scripts don't create new sessions)
    # In both cases, this handler doesn't modify session_id - it just preserves whatever was set.
    # Return stack is preserved (unchanged)
    return agent


def handle_reset_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
) -> Dict[str, Any]:
    """Handle <reset> transition tag.

    Starts a fresh session and continues at the target state.
    - Updates current_state to transition target
    - Sets session_id to None (fresh start)
    - Clears return stack (logs warning if non-empty)
    - If ``cd`` attribute is present, sets agent's working directory

    Note: The transition target is already resolved by step_agent before this
    handler is called, so we use transition.target directly.

    Args:
        agent: Agent state dictionary
        transition: Transition object with resolved target filename.
            Supports optional ``cd`` attribute to change the agent's working
            directory for all subsequent state executions. Relative paths are
            resolved against the agent's current cwd (or the orchestrator's
            cwd if no agent cwd is set). The result is always stored as an
            absolute, normalized path.
        state: Full workflow state dictionary (unused, for handler signature consistency)

    Returns:
        Updated agent state dictionary
    """
    # Log warning if return stack is non-empty
    stack = agent.get("stack", [])
    if stack:
        logger.warning(
            f"Agent {agent.get('id')} executing <reset> with non-empty return stack. "
            f"Stack will be cleared, discarding {len(stack)} pending return(s).",
            extra={
                "agent_id": agent.get("id"),
                "stack_size": len(stack),
                "transition_tag": "reset"
            }
        )

    agent["current_state"] = transition.target
    agent["session_id"] = None  # Fresh start
    agent["stack"] = []  # Clear return stack

    # Apply cd attribute if specified (changes agent's working directory)
    # Relative paths are resolved against the agent's current cwd.
    if "cd" in transition.attributes:
        agent["cwd"] = _resolve_cd(transition.attributes["cd"], agent.get("cwd"))

    return agent


def handle_function_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
) -> Dict[str, Any]:
    """Handle <function> transition tag.

    Runs a stateless/pure evaluation task that returns to the caller.
    - Pushes frame to return stack (caller session + return state)
    - Sets session_id to None (fresh context)
    - Updates current_state to function target

    Note: The transition target and attributes are already resolved by step_agent
    before this handler is called, so we use them directly.

    Args:
        agent: Agent state dictionary
        transition: Transition object with resolved target filename and return attribute
        state: Full workflow state dictionary (unused, for handler signature consistency)

    Returns:
        Updated agent state dictionary
    """
    # Validate required return attribute
    if "return" not in transition.attributes:
        raise ValueError(
            f"<function> tag requires 'return' attribute. "
            f"Example: <function return=\"NEXT.md\">EVAL.md</function>"
        )

    caller_session_id = agent.get("session_id")

    # Push frame to return stack (attributes already contain resolved return state)
    stack = agent.get("stack", [])
    frame = {
        "session": caller_session_id,
        "state": transition.attributes["return"]
    }
    agent["stack"] = stack + [frame]  # Push to end (LIFO)

    # Set session_id to None (fresh context for stateless function)
    agent["session_id"] = None

    # Update current_state to function target (already resolved)
    agent["current_state"] = transition.target

    return agent


def handle_call_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
) -> Dict[str, Any]:
    """Handle <call> transition tag.

    Enters a subroutine-like workflow that will eventually return to the caller.
    - Pushes frame to return stack (caller session + return state)
    - Sets fork_session_id to caller's session_id (for --fork-session in next step)
    - Updates current_state to callee target

    Unlike <function>, <call> preserves context from the caller via history
    branching (Claude Code --fork-session flag), which is useful when the callee needs
    to see what the caller was working on.

    Note: The transition target and attributes are already resolved by step_agent
    before this handler is called, so we use them directly.

    Args:
        agent: Agent state dictionary
        transition: Transition object with resolved target filename and return attribute
        state: Full workflow state dictionary (unused, for handler signature consistency)

    Returns:
        Updated agent state dictionary
    """
    # Validate required return attribute
    if "return" not in transition.attributes:
        raise ValueError(
            f"<call> tag requires 'return' attribute. "
            f"Example: <call return=\"NEXT.md\">CHILD.md</call>"
        )

    caller_session_id = agent.get("session_id")

    # Push frame to return stack (attributes already contain resolved return state)
    stack = agent.get("stack", [])
    frame = {
        "session": caller_session_id,
        "state": transition.attributes["return"]
    }
    agent["stack"] = stack + [frame]  # Push to end (LIFO)

    # Set fork_session_id to trigger --fork-session in next step_agent invocation
    # This branches context from the caller's session
    agent["fork_session_id"] = caller_session_id

    # Update current_state to callee target (already resolved)
    agent["current_state"] = transition.target

    return agent


def handle_fork_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
) -> Tuple[Dict[str, Any], Dict[str, Any]]:
    """Handle <fork> transition tag.

    Spawns an independent agent ("process-like") while the current agent continues.
    - Creates new agent in agents array
    - New agent has unique ID, empty return stack, fresh session
    - New agent's current_state is fork target
    - If ``cd`` attribute is present, sets the worker's working directory
    - Parent agent continues at next state (preserves session and stack)
    - Fork attributes (beyond ``next`` and ``cd``) are available as template
      variables for new agent

    Agent naming uses persistent fork counters per parent agent to ensure unique
    names even after previous workers have terminated. Names use a compact
    hierarchical underscore notation with state-based abbreviations:
    {parent_id}_{state_abbrev}{counter} where:
    - state_abbrev is the first 6 characters of the target state name (lowercase, no extension)
    - counter starts at 1 and increments for each fork from that parent
    Examples: main_worker1, main_worker1_analyz1, main_dispat1

    Note: The transition target and attributes are already resolved by step_agent
    before this handler is called, so we use them directly.

    Args:
        agent: Agent state dictionary (the parent)
        transition: Transition object with resolved target filename and next attribute.
            Supports optional ``cd`` attribute to set the worker's working directory.
            Relative paths are resolved against the parent agent's current cwd (or
            the orchestrator's cwd if no parent cwd is set). The result is always
            stored as an absolute, normalized path. The ``cd`` attribute is consumed
            by the orchestrator and not passed as a fork attribute.
        state: Full workflow state dictionary (used to track fork counters)

    Returns:
        Tuple of (updated parent agent, new worker agent)
    """
    # Validate required next attribute
    if "next" not in transition.attributes:
        raise ValueError(
            f"<fork> tag requires 'next' attribute. "
            f"Example: <fork next=\"NEXT.md\">WORKER.md</fork>"
        )

    parent_id = agent.get("id", "main")

    # Extract state name from fork target (e.g., "WORKER.md" -> "worker")
    # Remove state file extensions (.md, .sh, .bat) and convert to lowercase
    state_name = transition.target
    for ext in ('.md', '.sh', '.bat'):
        if state_name.lower().endswith(ext):
            state_name = state_name[:-len(ext)]
            break
    state_name = state_name.lower()

    # Truncate to 6 characters to keep names compact while informative
    # This gives us names like "worker", "analyz", "dispat", etc.
    state_abbrev = state_name[:6] if len(state_name) > 6 else state_name

    # Generate unique ID for new agent using persistent fork counter
    # This ensures unique names even if previous workers have terminated
    # Use underscore notation for hierarchy: parent_state_abbrev{counter}
    fork_counters = state.setdefault("fork_counters", {})
    fork_counters[parent_id] = fork_counters.get(parent_id, 0) + 1
    counter = fork_counters[parent_id]
    worker_id = f"{parent_id}_{state_abbrev}{counter}"

    # Create new worker agent (target already resolved)
    new_agent = {
        "id": worker_id,
        "current_state": transition.target,
        "session_id": None,  # Fresh session
        "stack": []  # Empty return stack
    }

    # Apply cd attribute if specified (sets worker's working directory)
    # Relative paths are resolved against the parent agent's current cwd.
    if "cd" in transition.attributes:
        new_agent["cwd"] = _resolve_cd(transition.attributes["cd"], agent.get("cwd"))

    # Store fork attributes (excluding 'next' and 'cd') for template substitution
    fork_attributes = {
        k: v for k, v in transition.attributes.items() if k not in ("next", "cd")
    }
    if fork_attributes:
        new_agent["fork_attributes"] = fork_attributes

    # Update parent agent with next state (already resolved, like goto - preserves session and stack)
    agent["current_state"] = transition.attributes["next"]
    # session_id and stack are preserved (unchanged)

    return (agent, new_agent)


def handle_result_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
) -> Optional[Dict[str, Any]]:
    """Handle <result> transition tag.

    If return stack is empty: agent terminates (returns None).
    If return stack is non-empty: pops frame, resumes caller session, and transitions
    to return state with result payload available as {{result}} variable.

    Args:
        agent: Agent state dictionary
        transition: Transition object with payload
        state: Full workflow state dictionary (unused, for handler signature consistency)

    Returns:
        Updated agent state dictionary, or None if agent terminates
    """
    stack = agent.get("stack", [])

    # Empty stack case: agent terminates
    if not stack:
        # Store result payload in state for console output when agent is removed
        # Use agent_id as key to track termination results
        agent_id = agent.get("id", "unknown")
        if "_agent_termination_results" not in state:
            state["_agent_termination_results"] = {}
        state["_agent_termination_results"][agent_id] = transition.payload
        return None

    # Non-empty stack case: pop frame and resume caller
    # Pop the most recent frame (LIFO - last in, first out)
    frame = stack[-1]
    remaining_stack = stack[:-1]

    # Restore caller's session_id
    agent["session_id"] = frame["session"]

    # Set current_state to return state from frame
    agent["current_state"] = frame["state"]

    # Update stack (remove popped frame)
    agent["stack"] = remaining_stack

    # Store result payload for template substitution in next step
    # step_agent will use this when rendering the return state prompt
    agent["pending_result"] = transition.payload

    return agent


# Register handlers in the dispatch table
# This must be done after the functions are defined
_TRANSITION_HANDLERS.update({
    "goto": handle_goto_transition,
    "reset": handle_reset_transition,
    "function": handle_function_transition,
    "call": handle_call_transition,
    "fork": handle_fork_transition,
    "result": handle_result_transition,
})
