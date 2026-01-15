import asyncio
import logging
from typing import Dict, Any, List, Optional
from cc_wrap import wrap_claude_code
from state import read_state, write_state, StateFileError as StateFileErrorFromState
from prompts import load_prompt, render_prompt
from parsing import parse_transitions, validate_single_transition, Transition

logger = logging.getLogger(__name__)


# Custom exception classes for error handling
class OrchestratorError(Exception):
    """Base exception for orchestrator errors."""
    pass


class ClaudeCodeError(OrchestratorError):
    """Raised when Claude Code execution fails."""
    pass


class PromptFileError(OrchestratorError):
    """Raised when prompt file operations fail."""
    pass


# Import StateFileError from state module (aliased to avoid conflict)
StateFileError = StateFileErrorFromState


# Recovery strategies
class RecoveryStrategy:
    """Recovery strategies for handling errors."""
    RETRY = "retry"
    SKIP = "skip"
    ABORT = "abort"


# Default retry configuration
MAX_RETRIES = 3


async def run_all_agents(workflow_id: str, state_dir: str = None) -> None:
    """Run all agents in a workflow until they all terminate.
    
    This is the main orchestrator loop that:
    1. Reads state file
    2. For each agent, creates async task to step that agent
    3. Uses asyncio.wait(..., return_when=FIRST_COMPLETED) to process completions
    4. For each completed task: parses output, dispatches to handler, updates state
    5. Repeats until all agents terminate
    
    Args:
        workflow_id: Unique identifier for the workflow
        state_dir: Optional custom state directory. If None, uses default.
    """
    logger.info(f"Starting orchestrator for workflow: {workflow_id}")
    
    while True:
        # Read state file (may raise StateFileError)
        state = read_state(workflow_id, state_dir=state_dir)
        
        # Exit if no agents remain
        if not state.get("agents", []):
            logger.info(f"Workflow {workflow_id} completed: no agents remaining")
            break
        
        logger.debug(
            f"Workflow {workflow_id}: {len(state['agents'])} agent(s) active",
            extra={"workflow_id": workflow_id, "agent_count": len(state["agents"])}
        )
        
        # Create async tasks for each agent
        pending_tasks = {}
        for agent in state["agents"]:
            task = asyncio.create_task(step_agent(agent, state, state_dir))
            pending_tasks[task] = agent
        
        # Wait for any task to complete
        while pending_tasks:
            done, pending = await asyncio.wait(
                pending_tasks.keys(),
                return_when=asyncio.FIRST_COMPLETED
            )
            
            # Process completed tasks
            for task in done:
                agent = pending_tasks.pop(task)
                try:
                    result = task.result()
                    # result contains updated agent state or None if agent terminated
                    if result is not None:
                        # Update agent in state
                        agent_idx = next(
                            i for i, a in enumerate(state["agents"])
                            if a["id"] == agent["id"]
                        )
                        old_state = state["agents"][agent_idx].get("current_state")
                        new_state = result.get("current_state")
                        
                        # Log state transition
                        if old_state != new_state:
                            logger.info(
                                f"Agent {agent['id']} transition: {old_state} -> {new_state}",
                                extra={
                                    "workflow_id": workflow_id,
                                    "agent_id": agent["id"],
                                    "old_state": old_state,
                                    "new_state": new_state
                                }
                            )
                        
                        state["agents"][agent_idx] = result
                        # Clear retry counter on success
                        if "retry_count" in state["agents"][agent_idx]:
                            del state["agents"][agent_idx]["retry_count"]
                    else:
                        # Agent terminated - remove from agents array
                        logger.info(
                            f"Agent {agent['id']} terminated",
                            extra={"workflow_id": workflow_id, "agent_id": agent["id"]}
                        )
                        state["agents"] = [
                            a for a in state["agents"]
                            if a["id"] != agent["id"]
                        ]
                except (ClaudeCodeError, PromptFileError) as e:
                    # Handle recoverable errors with retry logic
                    agent_idx = next(
                        i for i, a in enumerate(state["agents"])
                        if a["id"] == agent["id"]
                    )
                    current_agent = state["agents"][agent_idx]
                    
                    # Increment retry counter
                    retry_count = current_agent.get("retry_count", 0) + 1
                    current_agent["retry_count"] = retry_count
                    
                    logger.warning(
                        f"Agent {agent['id']} error (attempt {retry_count}/{MAX_RETRIES}): {e}",
                        extra={
                            "workflow_id": workflow_id,
                            "agent_id": agent["id"],
                            "error_type": type(e).__name__,
                            "retry_count": retry_count,
                            "max_retries": MAX_RETRIES,
                            "error_message": str(e)
                        }
                    )
                    
                    if retry_count >= MAX_RETRIES:
                        # Max retries exceeded - mark agent as failed
                        logger.error(
                            f"Agent {agent['id']} failed after {MAX_RETRIES} retries. "
                            f"Marking as failed.",
                            extra={
                                "workflow_id": workflow_id,
                                "agent_id": agent["id"],
                                "error_type": type(e).__name__,
                                "retry_count": retry_count,
                                "error_message": str(e)
                            }
                        )
                        current_agent["status"] = "failed"
                        current_agent["error"] = str(e)
                        # Remove failed agent from active agents
                        state["agents"] = [
                            a for a in state["agents"]
                            if a["id"] != agent["id"]
                        ]
                    else:
                        # Keep agent in state for retry (don't advance state)
                        state["agents"][agent_idx] = current_agent
                        
                except StateFileError as e:
                    # State file errors are critical - abort workflow
                    logger.error(
                        f"State file error: {e}. Aborting workflow.",
                        extra={
                            "workflow_id": workflow_id,
                            "agent_id": agent["id"],
                            "error_type": "StateFileError",
                            "error_message": str(e)
                        },
                        exc_info=True
                    )
                    raise
                except Exception as e:
                    # Unexpected errors - log and re-raise
                    logger.error(
                        f"Unexpected error in agent {agent['id']}: {e}",
                        extra={
                            "workflow_id": workflow_id,
                            "agent_id": agent["id"],
                            "error_type": type(e).__name__,
                            "error_message": str(e)
                        },
                        exc_info=True
                    )
                    raise
            
            # Write updated state
            write_state(workflow_id, state, state_dir=state_dir)
            
            # If we removed an agent, break inner loop to re-read state
            if any(task not in pending_tasks for task in done):
                break


async def step_agent(
    agent: Dict[str, Any],
    state: Dict[str, Any],
    state_dir: Optional[str] = None
) -> Optional[Dict[str, Any]]:
    """Step a single agent: load prompt, invoke Claude Code, parse output, dispatch.
    
    Args:
        agent: Agent state dictionary
        state: Full workflow state dictionary
        state_dir: Optional custom state directory
        
    Returns:
        Updated agent state dictionary, or None if agent terminated
    """
    scope_dir = state["scope_dir"]
    workflow_id = state.get("workflow_id", "unknown")
    current_state = agent["current_state"]
    agent_id = agent.get("id", "unknown")
    session_id = agent.get("session_id")
    
    logger.debug(
        f"Stepping agent {agent_id} in state {current_state}",
        extra={
            "workflow_id": workflow_id,
            "agent_id": agent_id,
            "current_state": current_state,
            "has_session": session_id is not None
        }
    )
    
    # Check if we need to fork from a caller's session (for <call> transitions)
    fork_session_id = agent.get("fork_session_id")
    
    # Load prompt for current state (may raise PromptFileError)
    try:
        prompt_template = load_prompt(scope_dir, current_state)
    except FileNotFoundError as e:
        logger.error(
            f"Prompt file not found for agent {agent_id}: {current_state}",
            extra={
                "workflow_id": workflow_id,
                "agent_id": agent_id,
                "current_state": current_state,
                "scope_dir": scope_dir
            }
        )
        raise PromptFileError(f"Prompt file not found: {e}") from e
    
    # Prepare template variables
    variables = {}
    
    # If there's a pending result from a function/call return, include it
    pending_result = agent.get("pending_result")
    if pending_result is not None:
        variables["result"] = pending_result
    
    # If there are fork attributes, include them as template variables
    fork_attributes = agent.get("fork_attributes", {})
    variables.update(fork_attributes)
    
    # Render template with variables
    prompt = render_prompt(prompt_template, variables)
    
    # Invoke Claude Code (may raise ClaudeCodeError)
    logger.info(
        f"Invoking Claude Code for agent {agent_id}",
        extra={
            "workflow_id": workflow_id,
            "agent_id": agent_id,
            "current_state": current_state,
            "session_id": session_id,
            "fork_session_id": fork_session_id,
            "using_fork": fork_session_id is not None
        }
    )
    
    try:
        if fork_session_id is not None:
            results, new_session_id = await wrap_claude_code(
                prompt, 
                session_id=fork_session_id,
                fork=True
            )
        else:
            results, new_session_id = await wrap_claude_code(prompt, session_id=session_id)
        
        logger.debug(
            f"Claude Code invocation completed for agent {agent_id}",
            extra={
                "workflow_id": workflow_id,
                "agent_id": agent_id,
                "new_session_id": new_session_id,
                "result_count": len(results)
            }
        )
    except RuntimeError as e:
        # Wrap RuntimeError from cc_wrap as ClaudeCodeError
        if "Claude command failed" in str(e):
            logger.error(
                f"Claude Code execution failed for agent {agent_id}",
                extra={
                    "workflow_id": workflow_id,
                    "agent_id": agent_id,
                    "current_state": current_state,
                    "error_message": str(e)
                }
            )
            raise ClaudeCodeError(f"Claude Code execution failed: {e}") from e
        raise
    
    # Extract text output from results
    # Claude Code stream-json format may vary, so we'll concatenate text fields
    output_text = ""
    for result in results:
        if isinstance(result, dict):
            if "text" in result:
                output_text += result["text"]
            elif "content" in result:
                output_text += str(result["content"])
    
    # Parse transitions from output
    transitions = parse_transitions(output_text)
    
    # Validate exactly one transition
    validate_single_transition(transitions)
    
    transition = transitions[0]
    
    logger.debug(
        f"Parsed transition for agent {agent_id}: {transition.tag} -> {transition.target}",
        extra={
            "workflow_id": workflow_id,
            "agent_id": agent_id,
            "transition_tag": transition.tag,
            "transition_target": transition.target
        }
    )
    
    # Create a copy of agent for handler (to avoid mutating original)
    agent_copy = agent.copy()
    
    # Update session_id if returned
    if new_session_id:
        agent_copy["session_id"] = new_session_id
    
    # Clear pending_result after using it (it was only for this step's template)
    if "pending_result" in agent_copy:
        del agent_copy["pending_result"]
    
    # Clear fork_session_id after using it (fork only happens on first invocation)
    if "fork_session_id" in agent_copy:
        del agent_copy["fork_session_id"]
    
    # Clear fork_attributes after using them (they're only for first step)
    if "fork_attributes" in agent_copy:
        del agent_copy["fork_attributes"]
    
    # Dispatch to appropriate handler
    handler_map = {
        "goto": handle_goto_transition,
        "reset": handle_reset_transition,
        "function": handle_function_transition,
        "call": handle_call_transition,
        "fork": handle_fork_transition,
        "result": handle_result_transition,
    }
    
    handler = handler_map.get(transition.tag)
    if handler is None:
        raise ValueError(f"Unknown transition tag: {transition.tag}")
    
    # Call handler with agent_copy, transition, and state
    handler_result = await handler(agent_copy, transition, state, state_dir)
    
    # Fork handler returns a tuple (updated_agent, new_agent)
    # Other handlers return just updated_agent
    if transition.tag == "fork" and isinstance(handler_result, tuple):
        updated_agent, new_agent = handler_result
        # Add new agent to state's agents array
        state["agents"].append(new_agent)
        return updated_agent
    else:
        return handler_result


async def handle_goto_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
    state_dir: Optional[str] = None
) -> Dict[str, Any]:
    """Handle <goto> transition tag.
    
    Updates agent's current_state to the transition target.
    Preserves session_id for resume in next step.
    Preserves return stack unchanged.
    
    Args:
        agent: Agent state dictionary
        transition: Transition object with target filename
        state: Full workflow state dictionary
        state_dir: Optional custom state directory
        
    Returns:
        Updated agent state dictionary
    """
    # Create updated agent with new current_state
    updated_agent = agent.copy()
    updated_agent["current_state"] = transition.target
    
    # session_id is already updated in step_agent if new_session_id was returned
    # Return stack is preserved (unchanged)
    
    return updated_agent


async def handle_reset_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
    state_dir: Optional[str] = None
) -> Dict[str, Any]:
    """Handle <reset> transition tag.
    
    Starts a fresh session and continues at the target state.
    - Updates current_state to transition target
    - Sets session_id to None (fresh start)
    - Clears return stack (logs warning if non-empty)
    
    Args:
        agent: Agent state dictionary
        transition: Transition object with target filename
        state: Full workflow state dictionary
        state_dir: Optional custom state directory
        
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
    
    # Create updated agent
    updated_agent = agent.copy()
    updated_agent["current_state"] = transition.target
    updated_agent["session_id"] = None  # Fresh start
    updated_agent["stack"] = []  # Clear return stack
    
    return updated_agent


async def handle_function_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
    state_dir: Optional[str] = None
) -> Dict[str, Any]:
    """Handle <function> transition tag.
    
    Runs a stateless/pure evaluation task that returns to the caller.
    - Pushes frame to return stack (caller session + return state)
    - Sets session_id to None (fresh context)
    - Updates current_state to function target
    
    Args:
        agent: Agent state dictionary
        transition: Transition object with target filename and return attribute
        state: Full workflow state dictionary
        state_dir: Optional custom state directory
        
    Returns:
        Updated agent state dictionary
    """
    # Validate required return attribute
    if "return" not in transition.attributes:
        raise ValueError(
            f"<function> tag requires 'return' attribute. "
            f"Example: <function return=\"NEXT.md\">EVAL.md</function>"
        )
    
    return_state = transition.attributes["return"]
    caller_session_id = agent.get("session_id")
    
    # Create updated agent
    updated_agent = agent.copy()
    
    # Push frame to return stack
    stack = updated_agent.get("stack", [])
    frame = {
        "session": caller_session_id,
        "state": return_state
    }
    updated_agent["stack"] = stack + [frame]  # Push to end (LIFO)
    
    # Set session_id to None (fresh context for stateless function)
    updated_agent["session_id"] = None
    
    # Update current_state to function target
    updated_agent["current_state"] = transition.target
    
    return updated_agent


async def handle_call_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
    state_dir: Optional[str] = None
) -> Dict[str, Any]:
    """Handle <call> transition tag.
    
    Enters a subroutine-like workflow that will eventually return to the caller.
    - Pushes frame to return stack (caller session + return state)
    - Sets fork_session_id to caller's session_id (for --fork in next step)
    - Updates current_state to callee target
    
    Unlike <function>, <call> preserves context from the caller via history
    branching (Claude Code --fork flag), which is useful when the callee needs
    to see what the caller was working on.
    
    Args:
        agent: Agent state dictionary
        transition: Transition object with target filename and return attribute
        state: Full workflow state dictionary
        state_dir: Optional custom state directory
        
    Returns:
        Updated agent state dictionary
    """
    # Validate required return attribute
    if "return" not in transition.attributes:
        raise ValueError(
            f"<call> tag requires 'return' attribute. "
            f"Example: <call return=\"NEXT.md\">CHILD.md</call>"
        )
    
    return_state = transition.attributes["return"]
    caller_session_id = agent.get("session_id")
    
    # Create updated agent
    updated_agent = agent.copy()
    
    # Push frame to return stack
    stack = updated_agent.get("stack", [])
    frame = {
        "session": caller_session_id,
        "state": return_state
    }
    updated_agent["stack"] = stack + [frame]  # Push to end (LIFO)
    
    # Set fork_session_id to trigger --fork in next step_agent invocation
    # This branches context from the caller's session
    updated_agent["fork_session_id"] = caller_session_id
    
    # Update current_state to callee target
    updated_agent["current_state"] = transition.target
    
    return updated_agent


async def handle_fork_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
    state_dir: Optional[str] = None
) -> tuple[Dict[str, Any], Dict[str, Any]]:
    """Handle <fork> transition tag.
    
    Spawns an independent agent ("process-like") while the current agent continues.
    - Creates new agent in agents array
    - New agent has unique ID, empty return stack, fresh session
    - New agent's current_state is fork target
    - Parent agent continues at next state (preserves session and stack)
    - Fork attributes (beyond 'next') are available as template variables for new agent
    
    Args:
        agent: Agent state dictionary (the parent)
        transition: Transition object with target filename and next attribute
        state: Full workflow state dictionary
        state_dir: Optional custom state directory
        
    Returns:
        Tuple of (updated parent agent, new worker agent)
    """
    # Validate required next attribute
    if "next" not in transition.attributes:
        raise ValueError(
            f"<fork> tag requires 'next' attribute. "
            f"Example: <fork next=\"NEXT.md\">WORKER.md</fork>"
        )
    
    next_state = transition.attributes["next"]
    parent_id = agent.get("id", "main")
    
    # Generate unique ID for new agent
    # Check existing agent IDs to ensure uniqueness
    existing_ids = {a.get("id") for a in state.get("agents", [])}
    worker_id = f"{parent_id}_worker"
    counter = 1
    while worker_id in existing_ids:
        worker_id = f"{parent_id}_worker_{counter}"
        counter += 1
    
    # Create new worker agent
    new_agent = {
        "id": worker_id,
        "current_state": transition.target,
        "session_id": None,  # Fresh session
        "stack": []  # Empty return stack
    }
    
    # Store fork attributes (excluding 'next') for template substitution
    fork_attributes = {
        k: v for k, v in transition.attributes.items() if k != "next"
    }
    if fork_attributes:
        new_agent["fork_attributes"] = fork_attributes
    
    # Update parent agent (like goto - preserves session and stack)
    updated_parent = agent.copy()
    updated_parent["current_state"] = next_state
    # session_id and stack are preserved (unchanged)
    
    return (updated_parent, new_agent)


async def handle_result_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
    state_dir: Optional[str] = None
) -> Optional[Dict[str, Any]]:
    """Handle <result> transition tag.
    
    If return stack is empty: agent terminates (returns None).
    If return stack is non-empty: pops frame, resumes caller session, and transitions
    to return state with result payload available as {{result}} variable.
    
    Args:
        agent: Agent state dictionary
        transition: Transition object with payload
        state: Full workflow state dictionary
        state_dir: Optional custom state directory
        
    Returns:
        Updated agent state dictionary, or None if agent terminates
    """
    stack = agent.get("stack", [])
    
    # Empty stack case: agent terminates
    if not stack:
        return None
    
    # Non-empty stack case: pop frame and resume caller
    # Pop the most recent frame (LIFO - last in, first out)
    frame = stack[-1]
    remaining_stack = stack[:-1]
    
    # Create updated agent
    updated_agent = agent.copy()
    
    # Restore caller's session_id
    updated_agent["session_id"] = frame["session"]
    
    # Set current_state to return state from frame
    updated_agent["current_state"] = frame["state"]
    
    # Update stack (remove popped frame)
    updated_agent["stack"] = remaining_stack
    
    # Store result payload for template substitution in next step
    # step_agent will use this when rendering the return state prompt
    updated_agent["pending_result"] = transition.payload
    
    return updated_agent
