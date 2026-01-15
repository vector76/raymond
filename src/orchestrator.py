import asyncio
from typing import Dict, Any, List, Optional
from cc_wrap import wrap_claude_code
from state import read_state, write_state
from prompts import load_prompt, render_prompt
from parsing import parse_transitions, validate_single_transition, Transition


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
    
    # Render template with any variables (for now, empty)
    prompt = render_prompt(prompt_template, {})
    
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
    
    # Update session_id if returned
    if new_session_id:
        agent["session_id"] = new_session_id
    
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
    
    # Call handler with agent, transition, and state
    updated_agent = await handler(agent, transition, state, state_dir)
    
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
    
    Raises NotImplementedError until Step 2.3.5.
    """
    raise NotImplementedError("handle_reset_transition not yet implemented")


async def handle_function_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
    state_dir: Optional[str] = None
) -> Dict[str, Any]:
    """Handle <function> transition tag.
    
    Raises NotImplementedError until Step 2.4.5.
    """
    raise NotImplementedError("handle_function_transition not yet implemented")


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
    If return stack is non-empty: pops frame and resumes caller (Step 2.4.10).
    
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
    
    # Non-empty stack case: will be implemented in Step 2.4.10
    # For now, raise NotImplementedError
    raise NotImplementedError(
        "handle_result_transition with non-empty stack not yet implemented (Step 2.4.10)"
    )
