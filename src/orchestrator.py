import asyncio
import logging
from typing import Dict, Any, List, Optional
from cc_wrap import wrap_claude_code
from state import read_state, write_state
from prompts import load_prompt, render_prompt
from parsing import parse_transitions, validate_single_transition, Transition

logger = logging.getLogger(__name__)


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
    while True:
        # Read state file
        state = read_state(workflow_id, state_dir=state_dir)
        
        # Exit if no agents remain
        if not state.get("agents", []):
            break
        
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
                        state["agents"][agent_idx] = result
                    else:
                        # Agent terminated - remove from agents array
                        state["agents"] = [
                            a for a in state["agents"]
                            if a["id"] != agent["id"]
                        ]
                except Exception as e:
                    # Handle errors (for now, just re-raise)
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
    current_state = agent["current_state"]
    session_id = agent.get("session_id")
    
    # Load prompt for current state
    prompt_template = load_prompt(scope_dir, current_state)
    
    # Prepare template variables
    variables = {}
    
    # If there's a pending result from a function/call return, include it
    pending_result = agent.get("pending_result")
    if pending_result is not None:
        variables["result"] = pending_result
    
    # Render template with variables
    prompt = render_prompt(prompt_template, variables)
    
    # Invoke Claude Code
    results, new_session_id = await wrap_claude_code(prompt, session_id=session_id)
    
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
    
    # Create a copy of agent for handler (to avoid mutating original)
    agent_copy = agent.copy()
    
    # Update session_id if returned
    if new_session_id:
        agent_copy["session_id"] = new_session_id
    
    # Clear pending_result after using it (it was only for this step's template)
    if "pending_result" in agent_copy:
        del agent_copy["pending_result"]
    
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
    updated_agent = await handler(agent_copy, transition, state, state_dir)
    
    return updated_agent


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
            f"Stack will be cleared, discarding {len(stack)} pending return(s)."
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
    
    Raises NotImplementedError until Step 2.5.4.
    """
    raise NotImplementedError("handle_call_transition not yet implemented")


async def handle_fork_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
    state_dir: Optional[str] = None
) -> Dict[str, Any]:
    """Handle <fork> transition tag.
    
    Raises NotImplementedError until Step 3.1.8.
    """
    raise NotImplementedError("handle_fork_transition not yet implemented")


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
