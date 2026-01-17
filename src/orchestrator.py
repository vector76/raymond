import asyncio
import copy
import json
import logging
from datetime import datetime
from pathlib import Path
from typing import Dict, Any, List, Optional, Tuple

from .cc_wrap import wrap_claude_code
from .state import read_state, write_state, delete_state, StateFileError as StateFileErrorFromState, get_state_dir
from .prompts import load_prompt, render_prompt
from .parsing import parse_transitions, validate_single_transition, Transition
from .policy import validate_transition_policy, PolicyViolationError, can_use_implicit_transition, get_implicit_transition, should_use_reminder_prompt, generate_reminder_prompt

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


def extract_cost_from_results(results: List[Dict[str, Any]]) -> float:
    """Extract total_cost_usd from Claude Code response results.
    
    Claude Code returns cost information in the final result object.
    This function searches through the results list for total_cost_usd.
    
    Args:
        results: List of JSON objects from Claude Code stream-json output
        
    Returns:
        Cost in USD (float), or 0.0 if not found
    """
    # Search through results for total_cost_usd field
    # Check in reverse order (final result is likely at the end)
    for result in reversed(results):
        if isinstance(result, dict):
            if "total_cost_usd" in result:
                cost = result["total_cost_usd"]
                # Ensure it's a number
                if isinstance(cost, (int, float)):
                    return float(cost)
    return 0.0


# Recovery strategies
class RecoveryStrategy:
    """Recovery strategies for handling errors."""
    RETRY = "retry"
    SKIP = "skip"
    ABORT = "abort"


# Default retry configuration
MAX_RETRIES = 3
MAX_REMINDER_ATTEMPTS = 3  # Maximum number of reminder prompts before terminating


def create_debug_directory(workflow_id: str, state_dir: Optional[str] = None) -> Optional[Path]:
    """Create debug directory for workflow execution.
    
    Args:
        workflow_id: Workflow identifier
        state_dir: Optional custom state directory (used to determine .raymond location)
        
    Returns:
        Path to debug directory, or None if creation fails
    """
    # Determine base directory (same parent as state_dir)
    state_path = get_state_dir(state_dir)
    base_dir = state_path.parent  # .raymond/
    
    # Generate timestamp
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    
    # Create debug directory path
    debug_dir = base_dir / "debug" / f"{workflow_id}_{timestamp}"
    
    try:
        debug_dir.mkdir(parents=True, exist_ok=True)
        return debug_dir
    except OSError as e:
        logger.warning(f"Failed to create debug directory: {e}")
        return None


def save_claude_output(
    debug_dir: Path,
    agent_id: str,
    state_name: str,
    step_number: int,
    results: List[Dict[str, Any]]
) -> None:
    """Save Claude Code JSON output to debug directory.
    
    Args:
        debug_dir: Debug directory path
        agent_id: Agent identifier
        state_name: State name (filename without .md)
        step_number: Step number for this agent
        results: Raw JSON results from Claude Code
    """
    filename = f"{agent_id}_{state_name}_{step_number:03d}.json"
    filepath = debug_dir / filename
    
    try:
        with open(filepath, 'w', encoding='utf-8') as f:
            json.dump(results, f, indent=2, ensure_ascii=False)
    except OSError as e:
        logger.warning(f"Failed to save Claude output to {filepath}: {e}")


def log_state_transition(
    debug_dir: Optional[Path],
    timestamp: datetime,
    agent_id: str,
    old_state: str,
    new_state: Optional[str],
    transition_type: str,
    transition_target: Optional[str],
    metadata: Dict[str, Any]
) -> None:
    """Log state transition to transitions.log file.
    
    Args:
        debug_dir: Debug directory path (None if debug disabled)
        timestamp: Transition timestamp
        agent_id: Agent identifier
        old_state: Previous state filename
        new_state: New state filename (None if agent terminated)
        transition_type: Type of transition (goto, reset, function, call, fork, result)
        transition_target: Transition target filename
        metadata: Additional metadata (session_id, cost, stack_depth, etc.)
    """
    if debug_dir is None:
        return
    
    log_file = debug_dir / "transitions.log"
    
    try:
        with open(log_file, 'a', encoding='utf-8') as f:
            # Format log entry
            if new_state:
                f.write(f"{timestamp.isoformat()} [{agent_id}] {old_state} -> {new_state} ({transition_type})\n")
            else:
                f.write(f"{timestamp.isoformat()} [{agent_id}] {old_state} -> (result, terminated)\n")
            
            # Write metadata
            for key, value in metadata.items():
                f.write(f"  {key}: {value}\n")
            
            f.write("\n")
    except OSError as e:
        logger.warning(f"Failed to write to transitions.log: {e}")


def save_error_response(
    workflow_id: str,
    agent_id: str,
    error: Exception,
    output_text: str,
    raw_results: List[Any],
    session_id: Optional[str],
    current_state: str,
    state_dir: Optional[str] = None
) -> Path:
    """Save failed response and error information to a text file for analysis.
    
    Args:
        workflow_id: Workflow identifier
        agent_id: Agent identifier
        error: The exception that occurred
        output_text: Extracted text output from Claude
        raw_results: Raw results from Claude Code
        session_id: Claude session ID
        current_state: Current state/prompt file
        state_dir: Optional custom state directory
        
    Returns:
        Path to the saved error file
    """
    # Create errors directory next to state directory
    state_path = get_state_dir(state_dir)
    errors_dir = state_path.parent / "errors"
    errors_dir.mkdir(parents=True, exist_ok=True)
    
    # Generate filename with timestamp
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    filename = f"{workflow_id}_{agent_id}_{timestamp}.txt"
    error_file = errors_dir / filename
    
    # Prepare error information
    error_info = {
        "timestamp": datetime.now().isoformat(),
        "workflow_id": workflow_id,
        "agent_id": agent_id,
        "current_state": current_state,
        "error_type": type(error).__name__,
        "error_message": str(error),
        "session_id": session_id,
    }
    
    # Write error file
    with open(error_file, 'w', encoding='utf-8') as f:
        f.write("=" * 80 + "\n")
        f.write("ERROR REPORT\n")
        f.write("=" * 80 + "\n\n")
        
        # Error metadata
        f.write("ERROR INFORMATION:\n")
        f.write("-" * 80 + "\n")
        for key, value in error_info.items():
            f.write(f"{key}: {value}\n")
        f.write("\n")
        
        # Raw results (JSON formatted)
        f.write("RAW CLAUDE CODE RESULTS:\n")
        f.write("-" * 80 + "\n")
        try:
            f.write(json.dumps(raw_results, indent=2, ensure_ascii=False))
        except (TypeError, ValueError):
            # Fallback if results aren't JSON serializable
            f.write(str(raw_results))
        f.write("\n\n")
        
        # Extracted text output
        f.write("EXTRACTED TEXT OUTPUT:\n")
        f.write("-" * 80 + "\n")
        f.write(output_text)
        f.write("\n\n")
        
        # Full traceback if available
        import traceback
        f.write("TRACEBACK:\n")
        f.write("-" * 80 + "\n")
        f.write(traceback.format_exc())
        f.write("\n")
    
    logger.info(
        f"Saved error response to {error_file}",
        extra={
            "workflow_id": workflow_id,
            "agent_id": agent_id,
            "error_file": str(error_file)
        }
    )
    
    return error_file


async def run_all_agents(workflow_id: str, state_dir: str = None, debug: bool = False, default_model: Optional[str] = None) -> None:
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
        debug: If True, enable debug mode (save outputs and log transitions)
    """
    logger.info(f"Starting orchestrator for workflow: {workflow_id}")
    
    # Create debug directory if debug mode is enabled
    debug_dir = None
    if debug:
        debug_dir = create_debug_directory(workflow_id, state_dir=state_dir)
        if debug_dir:
            logger.info(f"Debug mode enabled: {debug_dir}")
        else:
            logger.warning("Debug mode requested but directory creation failed, continuing without debug")
    
    # Track step numbers per agent for debug file naming
    agent_step_counters: Dict[str, int] = {}
    
    # Read state once at startup - in-memory state is authoritative during execution
    # State file is only for crash recovery (written after each step for persistence)
    state = read_state(workflow_id, state_dir=state_dir)
    
    # Track running tasks by agent ID - ensures exactly one task per agent
    # This makes stale tasks impossible by construction
    running_tasks: Dict[str, asyncio.Task] = {}
    
    while True:
        # Exit if no agents remain
        if not state.get("agents", []):
            logger.info(f"Workflow {workflow_id} completed: no agents remaining")
            # Clean up state file - workflow completed successfully
            state.pop("_agent_termination_results", None)
            delete_state(workflow_id, state_dir=state_dir)
            logger.debug(f"Deleted state file for completed workflow: {workflow_id}")
            break
        
        logger.debug(
            f"Workflow {workflow_id}: {len(state['agents'])} agent(s) active, "
            f"{len(running_tasks)} task(s) running",
            extra={
                "workflow_id": workflow_id,
                "agent_count": len(state["agents"]),
                "running_task_count": len(running_tasks)
            }
        )
        
        # Create tasks only for agents that don't already have a running task
        # This ensures exactly one task per agent at any time
        for agent in state["agents"]:
            agent_id = agent["id"]
            if agent_id not in running_tasks:
                task = asyncio.create_task(step_agent(
                    agent, state, state_dir, debug_dir, agent_step_counters, default_model
                ))
                running_tasks[agent_id] = task
        
        # If no tasks are running, we're stuck (shouldn't happen normally)
        if not running_tasks:
            logger.warning(
                f"Workflow {workflow_id}: no running tasks but agents remain",
                extra={"workflow_id": workflow_id, "agents": [a["id"] for a in state["agents"]]}
            )
            break
        
        # Wait for any task to complete
        done, _ = await asyncio.wait(
            running_tasks.values(),
            return_when=asyncio.FIRST_COMPLETED
        )
        
        # Process completed tasks
        for task in done:
            # Find which agent this task was for
            agent_id = next(
                (aid for aid, t in running_tasks.items() if t is task),
                None
            )
            if agent_id is None:
                logger.warning("Completed task not found in running_tasks")
                continue
            
            # Remove from running tasks - task is done
            del running_tasks[agent_id]
            
            # Get the agent dict (may have been updated by other operations)
            agent = next(
                (a for a in state["agents"] if a["id"] == agent_id),
                None
            )
            
            try:
                result = task.result()
                
                # result contains updated agent state or None if agent terminated
                if result is not None:
                    # Update agent in state
                    agent_idx = next(
                        (i for i, a in enumerate(state["agents"]) if a["id"] == agent_id),
                        None
                    )
                    if agent_idx is None:
                        logger.warning(
                            f"Agent {agent_id} not found in state, skipping update",
                            extra={"workflow_id": workflow_id, "agent_id": agent_id}
                        )
                        continue
                    old_state = state["agents"][agent_idx].get("current_state")
                    new_state = result.get("current_state")
                    
                    # Log state transition
                    if old_state != new_state:
                        logger.info(
                            f"Agent {agent_id} transition: {old_state} -> {new_state}",
                            extra={
                                "workflow_id": workflow_id,
                                "agent_id": agent_id,
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
                    # Get termination result from state (stored by handle_result_transition)
                    termination_results = state.get("_agent_termination_results", {})
                    termination_result = termination_results.pop(agent_id, "")
                    
                    print(f"Agent {agent_id} terminated with result: {termination_result}")
                    logger.info(
                        f"Agent {agent_id} terminated",
                        extra={
                            "workflow_id": workflow_id,
                            "agent_id": agent_id,
                            "result": termination_result
                        }
                    )
                    state["agents"] = [
                        a for a in state["agents"]
                        if a["id"] != agent_id
                    ]
            except (ClaudeCodeError, PromptFileError) as e:
                # Handle recoverable errors with retry logic
                agent_idx = next(
                    (i for i, a in enumerate(state["agents"]) if a["id"] == agent_id),
                    None
                )
                if agent_idx is None:
                    logger.warning(
                        f"Agent {agent_id} not found in state during error handling",
                        extra={"workflow_id": workflow_id, "agent_id": agent_id}
                    )
                    continue
                current_agent = state["agents"][agent_idx]
                
                # Increment retry counter
                retry_count = current_agent.get("retry_count", 0) + 1
                current_agent["retry_count"] = retry_count
                
                logger.warning(
                    f"Agent {agent_id} error (attempt {retry_count}/{MAX_RETRIES}): {e}",
                    extra={
                        "workflow_id": workflow_id,
                        "agent_id": agent_id,
                        "error_type": type(e).__name__,
                        "retry_count": retry_count,
                        "max_retries": MAX_RETRIES,
                        "error_message": str(e)
                    }
                )
                
                if retry_count >= MAX_RETRIES:
                    # Max retries exceeded - mark agent as failed
                    logger.error(
                        f"Agent {agent_id} failed after {MAX_RETRIES} retries. "
                        f"Marking as failed.",
                        extra={
                            "workflow_id": workflow_id,
                            "agent_id": agent_id,
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
                        if a["id"] != agent_id
                    ]
                else:
                    # Keep agent in state for retry (don't advance state)
                    # A new task will be created in the next loop iteration
                    state["agents"][agent_idx] = current_agent
                    
            except StateFileError as e:
                # State file errors are critical - abort workflow
                logger.error(
                    f"State file error: {e}. Aborting workflow.",
                    extra={
                        "workflow_id": workflow_id,
                        "agent_id": agent_id,
                        "error_type": "StateFileError",
                        "error_message": str(e)
                    },
                    exc_info=True
                )
                raise
            except Exception as e:
                # Unexpected errors - save error info and re-raise
                # Check if error was already saved in step_agent
                if getattr(e, '_error_saved', False):
                    # Error was already saved with full context, just log and re-raise
                    logger.error(
                        f"Unexpected error in agent {agent_id}: {e}",
                        extra={
                            "workflow_id": workflow_id,
                            "agent_id": agent_id,
                            "error_type": type(e).__name__,
                            "error_message": str(e)
                        },
                        exc_info=True
                    )
                    raise
                
                # Error wasn't saved yet - try to extract error context if available
                error_output_text = ""
                error_raw_results = []
                error_session_id = agent.get("session_id") if agent else None
                
                logger.error(
                    f"Unexpected error in agent {agent_id}: {e}",
                    extra={
                        "workflow_id": workflow_id,
                        "agent_id": agent_id,
                        "error_type": type(e).__name__,
                        "error_message": str(e)
                    },
                    exc_info=True
                )
                
                # Save error information if we have context
                # This is for errors that occur outside of step_agent
                try:
                    save_error_response(
                        workflow_id=workflow_id,
                        agent_id=agent_id,
                        error=e,
                        output_text=error_output_text or "No output captured",
                        raw_results=error_raw_results,
                        session_id=error_session_id,
                        current_state=agent.get("current_state", "unknown") if agent else "unknown",
                        state_dir=state_dir
                    )
                except Exception as save_error:
                    # Don't fail if we can't save the error
                    logger.warning(f"Failed to save error response: {save_error}")
                
                raise
        
        # Write updated state for crash recovery
        write_state(workflow_id, state, state_dir=state_dir)


async def step_agent(
    agent: Dict[str, Any],
    state: Dict[str, Any],
    state_dir: Optional[str] = None,
    debug_dir: Optional[Path] = None,
    agent_step_counters: Optional[Dict[str, int]] = None,
    default_model: Optional[str] = None
) -> Optional[Dict[str, Any]]:
    """Step a single agent: load prompt, invoke Claude Code, parse output, dispatch.
    
    Args:
        agent: Agent state dictionary
        state: Full workflow state dictionary
        state_dir: Optional custom state directory
        debug_dir: Optional debug directory path (for saving outputs)
        agent_step_counters: Optional dict to track step numbers per agent
        default_model: Optional model to use if not specified in frontmatter
        
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
        prompt_template, policy = load_prompt(scope_dir, current_state)
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
    except ValueError as e:
        # YAML parsing error in frontmatter
        logger.error(
            f"Invalid frontmatter in prompt file for agent {agent_id}: {current_state}",
            extra={
                "workflow_id": workflow_id,
                "agent_id": agent_id,
                "current_state": current_state,
                "scope_dir": scope_dir
            }
        )
        raise PromptFileError(f"Invalid frontmatter in prompt file: {e}") from e
    
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
    base_prompt = render_prompt(prompt_template, variables)
    
    # Determine which model to use based on precedence:
    # 1. Frontmatter model (highest priority)
    # 2. Default model from CLI (if no frontmatter model)
    # 3. None (let Claude Code use its default)
    model_to_use = None
    if policy and policy.model:
        model_to_use = policy.model
    elif default_model:
        # Normalize CLI model to lowercase for consistency
        model_to_use = default_model.lower() if isinstance(default_model, str) else default_model
    
    # Retry loop for reminder prompts
    # If allowed_transitions are defined, we can re-prompt with reminders
    # Otherwise, parse failures terminate the agent
    transition = None
    new_session_id = session_id
    reminder_attempt = 0
    
    while transition is None:
        # Build prompt (base + reminder if this is a retry)
        prompt = base_prompt
        if reminder_attempt > 0:
            # Append reminder prompt
            try:
                reminder = generate_reminder_prompt(policy)
                prompt = base_prompt + reminder
                logger.info(
                    f"Re-prompting agent {agent_id} with reminder (attempt {reminder_attempt})",
                    extra={
                        "workflow_id": workflow_id,
                        "agent_id": agent_id,
                        "current_state": current_state,
                        "reminder_attempt": reminder_attempt
                    }
                )
            except ValueError as e:
                # This shouldn't happen if we checked should_use_reminder_prompt
                logger.error(
                    f"Failed to generate reminder prompt: {e}",
                    extra={
                        "workflow_id": workflow_id,
                        "agent_id": agent_id,
                        "current_state": current_state
                    }
                )
                raise
        
        # Invoke Claude Code (may raise ClaudeCodeError)
        logger.info(
            f"Invoking Claude Code for agent {agent_id}",
            extra={
                "workflow_id": workflow_id,
                "agent_id": agent_id,
                "current_state": current_state,
                "session_id": new_session_id,
                "fork_session_id": fork_session_id,
                "using_fork": fork_session_id is not None,
                "model": model_to_use or "default",
                "reminder_attempt": reminder_attempt
            }
        )
        
        try:
            if fork_session_id is not None and reminder_attempt == 0:
                # Only use fork_session_id on first attempt
                results, new_session_id = await wrap_claude_code(
                    prompt, 
                    model=model_to_use,
                    session_id=fork_session_id,
                    fork=True
                )
            else:
                # Use regular session_id (or new_session_id from previous attempt)
                results, new_session_id = await wrap_claude_code(
                    prompt, 
                    model=model_to_use,
                    session_id=new_session_id
                )
            
            logger.debug(
                f"Claude Code invocation completed for agent {agent_id}",
                extra={
                    "workflow_id": workflow_id,
                    "agent_id": agent_id,
                    "new_session_id": new_session_id,
                    "result_count": len(results)
                }
            )
            
            # Save Claude Code output to debug directory if debug mode is enabled
            if debug_dir is not None and agent_step_counters is not None:
                try:
                    # Increment step counter for this agent
                    if agent_id not in agent_step_counters:
                        agent_step_counters[agent_id] = 0
                    agent_step_counters[agent_id] += 1
                    step_number = agent_step_counters[agent_id]
                    
                    # Extract state name (filename without .md extension)
                    state_name = current_state.replace('.md', '')
                    
                    # Save the JSON output
                    save_claude_output(
                        debug_dir=debug_dir,
                        agent_id=agent_id,
                        state_name=state_name,
                        step_number=step_number,
                        results=results
                    )
                except OSError as e:
                    # Debug operations should not fail the workflow
                    logger.warning(f"Failed to save Claude output for debug: {e}")
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
                # Save error information
                save_error_response(
                    workflow_id=workflow_id,
                    agent_id=agent_id,
                    error=e,
                    output_text="Claude Code execution failed - no output received",
                    raw_results=[],
                    session_id=new_session_id or session_id or fork_session_id,
                    current_state=current_state,
                    state_dir=state_dir
                )
                raise ClaudeCodeError(f"Claude Code execution failed: {e}") from e
            raise
        
        # Extract text output from results
        # Claude Code stream-json format may vary, so we'll concatenate text fields
        # Text can be at top level or nested in message.content[].text
        # Priority: result field (if present, use only that) > message.content > top-level text/content
        output_text = ""
        has_result_field = False
        
        # First pass: check for result field (highest priority)
        for result in results:
            if isinstance(result, dict) and "result" in result and isinstance(result["result"], str):
                output_text += result["result"]
                has_result_field = True
        
        # If we found a result field, use only that (skip other extraction)
        if has_result_field:
            pass  # Already extracted, don't extract from other sources
        else:
            # Second pass: extract from message.content or top-level fields
            for result in results:
                if isinstance(result, dict):
                    # Check nested message.content structure (Claude Code format)
                    if "message" in result and isinstance(result["message"], dict):
                        content = result["message"].get("content", [])
                        if isinstance(content, list):
                            for item in content:
                                if isinstance(item, dict) and "text" in item:
                                    output_text += item["text"]
                        elif isinstance(content, str):
                            output_text += content
                    # Check top-level text field
                    elif "text" in result:
                        output_text += result["text"]
                    # Check top-level content field
                    elif "content" in result:
                        if isinstance(result["content"], str):
                            output_text += result["content"]
                        elif isinstance(result["content"], list):
                            for item in result["content"]:
                                if isinstance(item, dict) and "text" in item:
                                    output_text += item["text"]
        
        # Extract cost from Claude Code response and accumulate in state
        invocation_cost = extract_cost_from_results(results)
        if invocation_cost > 0:
            # Initialize cost tracking if not present (for backward compatibility)
            if "total_cost_usd" not in state:
                state["total_cost_usd"] = 0.0
            state["total_cost_usd"] += invocation_cost
            
            logger.info(
                f"Cost for agent {agent_id} invocation: ${invocation_cost:.4f}, "
                f"Total cost: ${state['total_cost_usd']:.4f}",
                extra={
                    "workflow_id": workflow_id,
                    "agent_id": agent_id,
                    "invocation_cost": invocation_cost,
                    "total_cost": state["total_cost_usd"]
                }
            )
        
        # Check budget limit
        budget_usd = state.get("budget_usd", 1.0)  # Default budget if not set
        total_cost = state.get("total_cost_usd", 0.0)
        
        if total_cost > budget_usd:
            logger.warning(
                f"Budget exceeded: ${total_cost:.4f} > ${budget_usd:.4f}. "
                f"Terminating workflow.",
                extra={
                    "workflow_id": workflow_id,
                    "agent_id": agent_id,
                    "total_cost": total_cost,
                    "budget": budget_usd
                }
            )
            # Override transition: force termination by creating a <result> transition
            # This will terminate the agent cleanly
            from .parsing import Transition
            transition = Transition(
                tag="result",
                target="",
                attributes={},
                payload=f"Workflow terminated: budget exceeded (${total_cost:.4f} > ${budget_usd:.4f})"
            )
            break
        
        # Parse transitions from output
        transitions = parse_transitions(output_text)
        
        # Check if we can use implicit transition optimization
        if len(transitions) == 0 and can_use_implicit_transition(policy):
            # No tag emitted, but we have an implicit transition available
            # Use the implicit transition from the policy
            transition = get_implicit_transition(policy)
            logger.debug(
                f"Using implicit transition for agent {agent_id}: {transition.tag} -> {transition.target}",
                extra={
                    "workflow_id": workflow_id,
                    "agent_id": agent_id,
                    "transition_tag": transition.tag,
                    "transition_target": transition.target
                }
            )
            break
        elif len(transitions) == 0:
            # No tag emitted and no implicit transition available
            if should_use_reminder_prompt(policy):
                # Can re-prompt with reminder
                reminder_attempt += 1
                if reminder_attempt >= MAX_REMINDER_ATTEMPTS:
                    # Too many reminder attempts - terminate
                    error = ValueError(
                        f"Expected exactly one transition, found 0 after {MAX_REMINDER_ATTEMPTS} reminder attempts"
                    )
                    save_error_response(
                        workflow_id=workflow_id,
                        agent_id=agent_id,
                        error=error,
                        output_text=output_text,
                        raw_results=results,
                        session_id=new_session_id,
                        current_state=current_state,
                        state_dir=state_dir
                    )
                    error._error_saved = True
                    raise error
                # Continue loop to re-prompt with reminder
                continue
            else:
                # No reminder available - terminate with error
                error = ValueError(
                    "Expected exactly one transition, found 0"
                )
                save_error_response(
                    workflow_id=workflow_id,
                    agent_id=agent_id,
                    error=error,
                    output_text=output_text,
                    raw_results=results,
                    session_id=new_session_id,
                    current_state=current_state,
                    state_dir=state_dir
                )
                error._error_saved = True
                raise error
        else:
            # Tag(s) were emitted - validate exactly one
            try:
                validate_single_transition(transitions)
            except ValueError as e:
                if should_use_reminder_prompt(policy):
                    # Can re-prompt with reminder
                    reminder_attempt += 1
                    if reminder_attempt >= MAX_REMINDER_ATTEMPTS:
                        # Too many reminder attempts - terminate
                        save_error_response(
                            workflow_id=workflow_id,
                            agent_id=agent_id,
                            error=e,
                            output_text=output_text,
                            raw_results=results,
                            session_id=new_session_id,
                            current_state=current_state,
                            state_dir=state_dir
                        )
                        e._error_saved = True
                        raise
                    # Continue loop to re-prompt with reminder
                    continue
                else:
                    # No reminder available - terminate with error
                    save_error_response(
                        workflow_id=workflow_id,
                        agent_id=agent_id,
                        error=e,
                        output_text=output_text,
                        raw_results=results,
                        session_id=new_session_id,
                        current_state=current_state,
                        state_dir=state_dir
                    )
                    e._error_saved = True
                    raise
            
            transition = transitions[0]
            
            # Validate transition against state policy (if policy exists)
            # Policy violations can also trigger reminder prompts if allowed_transitions are defined
            try:
                validate_transition_policy(transition, policy)
            except PolicyViolationError as e:
                if should_use_reminder_prompt(policy):
                    # Can re-prompt with reminder
                    reminder_attempt += 1
                    logger.warning(
                        f"Policy violation for agent {agent_id} in state {current_state}: {e}. "
                        f"Re-prompting with reminder (attempt {reminder_attempt})",
                        extra={
                            "workflow_id": workflow_id,
                            "agent_id": agent_id,
                            "current_state": current_state,
                            "transition_tag": transition.tag,
                            "transition_target": transition.target,
                            "reminder_attempt": reminder_attempt
                        }
                    )
                    if reminder_attempt >= MAX_REMINDER_ATTEMPTS:
                        # Too many reminder attempts - terminate
                        save_error_response(
                            workflow_id=workflow_id,
                            agent_id=agent_id,
                            error=e,
                            output_text=output_text,
                            raw_results=results,
                            session_id=new_session_id,
                            current_state=current_state,
                            state_dir=state_dir
                        )
                        e._error_saved = True
                        raise
                    # Reset transition and continue loop to re-prompt with reminder
                    transition = None
                    continue
                else:
                    # No reminder available - terminate with error
                    logger.warning(
                        f"Policy violation for agent {agent_id} in state {current_state}: {e}",
                        extra={
                            "workflow_id": workflow_id,
                            "agent_id": agent_id,
                            "current_state": current_state,
                            "transition_tag": transition.tag,
                            "transition_target": transition.target
                        }
                    )
                    # Save error information before re-raising
                    save_error_response(
                        workflow_id=workflow_id,
                        agent_id=agent_id,
                        error=e,
                        output_text=output_text,
                        raw_results=results,
                        session_id=new_session_id,
                        current_state=current_state,
                        state_dir=state_dir
                    )
                    # Mark that this error was already saved to avoid duplicate saves
                    e._error_saved = True
                    raise
            
            # Transition is valid - break out of loop
            break
    
    logger.debug(
        f"Parsed transition for agent {agent_id}: {transition.tag} -> {transition.target}",
        extra={
            "workflow_id": workflow_id,
            "agent_id": agent_id,
            "transition_tag": transition.tag,
            "transition_target": transition.target
        }
    )
    
    # Create a deep copy of agent for handler (to avoid mutating original)
    # Deep copy ensures nested structures like stack are also copied
    agent_copy = copy.deepcopy(agent)
    
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
    # Handlers are sync functions - no await needed
    handler_result = handler(agent_copy, transition, state)
    
    # Log state transition for debug mode
    if debug_dir is not None:
        old_state = current_state
        new_state = None
        transition_target = transition.target
        stack_depth = len(agent_copy.get("stack", []))
        
        # Determine new state and whether agent terminated
        spawned_agent_id = None
        if handler_result is None:
            # Agent terminated
            new_state = None
        elif isinstance(handler_result, tuple):
            # Fork handler - parent agent continues
            updated_agent, new_agent = handler_result
            new_state = updated_agent.get("current_state")
            spawned_agent_id = new_agent.get("id")
        else:
            # Regular handler
            new_state = handler_result.get("current_state")
        
        # Prepare metadata
        metadata = {
            "stack_depth": stack_depth,
        }
        
        # Add session_id information
        if new_session_id:
            if session_id is None:
                metadata["session_id"] = f"{new_session_id} (new)"
            else:
                metadata["session_id"] = f"{new_session_id} (resumed)"
        elif session_id:
            metadata["session_id"] = session_id
        
        # Add cost information
        if invocation_cost > 0:
            metadata["cost"] = f"${invocation_cost:.4f}"
            total_cost = state.get("total_cost_usd", 0.0)
            metadata["total_cost"] = f"${total_cost:.4f}"
        
        # Add transition-specific metadata
        if transition.tag == "function" or transition.tag == "call":
            if "return" in transition.attributes:
                metadata["return_state"] = transition.attributes["return"]
        elif transition.tag == "fork":
            # Add spawned agent ID for fork transitions
            if spawned_agent_id:
                metadata["spawned_agent"] = spawned_agent_id
        elif transition.tag == "result":
            if transition.payload:
                metadata["result_payload"] = transition.payload
        
        # Log the transition
        try:
            log_state_transition(
                debug_dir=debug_dir,
                timestamp=datetime.now(),
                agent_id=agent_id,
                old_state=old_state,
                new_state=new_state,
                transition_type=transition.tag,
                transition_target=transition_target,
                metadata=metadata
            )
        except OSError as e:
            # Debug operations should not fail the workflow
            logger.warning(f"Failed to log state transition for debug: {e}")
    
    # Fork handler returns a tuple (updated_agent, new_agent)
    # Other handlers return just updated_agent
    if transition.tag == "fork" and isinstance(handler_result, tuple):
        updated_agent, new_agent = handler_result
        # Add new agent to state's agents array
        state["agents"].append(new_agent)
        return updated_agent
    else:
        return handler_result


def handle_goto_transition(
    agent: Dict[str, Any],
    transition: Transition,
    state: Dict[str, Any],
) -> Dict[str, Any]:
    """Handle <goto> transition tag.
    
    Updates agent's current_state to the transition target.
    Preserves session_id for resume in next step.
    Preserves return stack unchanged.
    
    Args:
        agent: Agent state dictionary
        transition: Transition object with target filename
        state: Full workflow state dictionary (unused, for handler signature consistency)
        
    Returns:
        Updated agent state dictionary
    """
    agent["current_state"] = transition.target
    # session_id is already updated in step_agent if new_session_id was returned
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
    
    Args:
        agent: Agent state dictionary
        transition: Transition object with target filename
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
    
    Args:
        agent: Agent state dictionary
        transition: Transition object with target filename and return attribute
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
    
    return_state = transition.attributes["return"]
    caller_session_id = agent.get("session_id")
    
    # Push frame to return stack
    stack = agent.get("stack", [])
    frame = {
        "session": caller_session_id,
        "state": return_state
    }
    agent["stack"] = stack + [frame]  # Push to end (LIFO)
    
    # Set session_id to None (fresh context for stateless function)
    agent["session_id"] = None
    
    # Update current_state to function target
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
    
    Args:
        agent: Agent state dictionary
        transition: Transition object with target filename and return attribute
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
    
    return_state = transition.attributes["return"]
    caller_session_id = agent.get("session_id")
    
    # Push frame to return stack
    stack = agent.get("stack", [])
    frame = {
        "session": caller_session_id,
        "state": return_state
    }
    agent["stack"] = stack + [frame]  # Push to end (LIFO)
    
    # Set fork_session_id to trigger --fork-session in next step_agent invocation
    # This branches context from the caller's session
    agent["fork_session_id"] = caller_session_id
    
    # Update current_state to callee target
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
    - Parent agent continues at next state (preserves session and stack)
    - Fork attributes (beyond 'next') are available as template variables for new agent
    
    Agent naming uses persistent fork counters per parent agent to ensure unique
    names even after previous workers have terminated. Names use a compact
    hierarchical underscore notation with state-based abbreviations:
    {parent_id}_{state_abbrev}{counter} where:
    - state_abbrev is the first 6 characters of the target state name (lowercase, no .md)
    - counter starts at 1 and increments for each fork from that parent
    Examples: main_worker1, main_worker1_analyz1, main_dispat1
    
    Args:
        agent: Agent state dictionary (the parent)
        transition: Transition object with target filename and next attribute
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
    
    next_state = transition.attributes["next"]
    parent_id = agent.get("id", "main")
    
    # Extract state name from fork target (e.g., "WORKER.md" -> "worker")
    # Remove .md extension and convert to lowercase
    state_name = transition.target
    if state_name.endswith('.md'):
        state_name = state_name[:-3]  # Remove .md extension
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
    agent["current_state"] = next_state
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
