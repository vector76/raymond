import asyncio
import copy
import json
import logging
import time
import traceback
from datetime import datetime
from pathlib import Path
from typing import Dict, Any, List, Optional, Tuple

from .cc_wrap import wrap_claude_code, wrap_claude_code_stream, ClaudeCodeTimeoutError
from .state import read_state, write_state, delete_state, StateFileError as StateFileErrorFromState, get_state_dir
from .prompts import load_prompt, render_prompt, resolve_state, get_state_type
from .parsing import parse_transitions, validate_single_transition, Transition
from .policy import validate_transition_policy, PolicyViolationError, can_use_implicit_transition, get_implicit_transition, should_use_reminder_prompt, generate_reminder_prompt
from .scripts import run_script, build_script_env, ScriptTimeoutError

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


class ScriptError(OrchestratorError):
    """Raised when script execution fails."""
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
    """Save Claude Code JSON output to debug directory (non-streaming version).

    NOTE: For production use, prefer the streaming functions below which write
    progressively as data arrives, preventing data loss on crashes/timeouts.

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


def get_claude_output_filepath(
    debug_dir: Path,
    agent_id: str,
    state_name: str,
    step_number: int
) -> Path:
    """Get the filepath for a Claude Code JSONL output file.

    Args:
        debug_dir: Debug directory path
        agent_id: Agent identifier
        state_name: State name (filename without extension)
        step_number: Step number for this agent

    Returns:
        Path to the JSONL file
    """
    filename = f"{agent_id}_{state_name}_{step_number:03d}.jsonl"
    return debug_dir / filename


def append_claude_output_line(
    filepath: Path,
    json_object: Dict[str, Any]
) -> None:
    """Append a single JSON object to a JSONL debug file.

    This function is used for progressive/streaming writes where each JSON
    object from Claude Code is written immediately as it arrives. This ensures
    debug data is preserved even if the process crashes or times out.

    Args:
        filepath: Path to the JSONL file (will be created if doesn't exist)
        json_object: Single JSON object to append

    Note:
        Each object is written on its own line with no trailing comma.
        The file uses JSONL (JSON Lines) format, not a JSON array.
    """
    try:
        with open(filepath, 'a', encoding='utf-8') as f:
            f.write(json.dumps(json_object, ensure_ascii=False) + '\n')
    except OSError as e:
        logger.warning(f"Failed to append Claude output to {filepath}: {e}")


def save_script_output(
    debug_dir: Path,
    agent_id: str,
    state_name: str,
    step_number: int,
    stdout: str,
    stderr: str
) -> None:
    """Save script execution output to debug directory.

    Creates separate .stdout.txt and .stderr.txt files for each script execution.

    Args:
        debug_dir: Debug directory path
        agent_id: Agent identifier
        state_name: State name (filename without extension)
        step_number: Step number for this agent
        stdout: Script stdout output
        stderr: Script stderr output
    """
    base_filename = f"{agent_id}_{state_name}_{step_number:03d}"
    stdout_filepath = debug_dir / f"{base_filename}.stdout.txt"
    stderr_filepath = debug_dir / f"{base_filename}.stderr.txt"

    try:
        with open(stdout_filepath, 'w', encoding='utf-8') as f:
            f.write(stdout)
    except OSError as e:
        logger.warning(f"Failed to save script stdout to {stdout_filepath}: {e}")

    try:
        with open(stderr_filepath, 'w', encoding='utf-8') as f:
            f.write(stderr)
    except OSError as e:
        logger.warning(f"Failed to save script stderr to {stderr_filepath}: {e}")


def save_script_output_metadata(
    debug_dir: Path,
    agent_id: str,
    state_name: str,
    step_number: int,
    exit_code: int,
    execution_time_ms: float,
    env_vars: Dict[str, str]
) -> None:
    """Save script execution metadata to debug directory.

    Creates a .meta.json file for each script execution containing
    execution time, exit code, and environment variables.

    Args:
        debug_dir: Debug directory path
        agent_id: Agent identifier
        state_name: State name (filename without extension)
        step_number: Step number for this agent
        exit_code: Script exit code
        execution_time_ms: Script execution time in milliseconds
        env_vars: Environment variables passed to the script
    """
    base_filename = f"{agent_id}_{state_name}_{step_number:03d}"
    metadata_filepath = debug_dir / f"{base_filename}.meta.json"

    metadata = {
        "exit_code": exit_code,
        "execution_time_ms": execution_time_ms,
        "env_vars": env_vars
    }

    try:
        with open(metadata_filepath, 'w', encoding='utf-8') as f:
            json.dump(metadata, f, indent=2, ensure_ascii=False)
    except OSError as e:
        logger.warning(f"Failed to save script metadata to {metadata_filepath}: {e}")


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


def save_script_error_response(
    workflow_id: str,
    agent_id: str,
    error: Exception,
    script_path: str,
    stdout: str,
    stderr: str,
    exit_code: Optional[int],
    current_state: str,
    state_dir: Optional[str] = None
) -> Path:
    """Save failed script execution information to a text file for analysis.

    Args:
        workflow_id: Workflow identifier
        agent_id: Agent identifier
        error: The exception that occurred
        script_path: Full path to the script file
        stdout: Script stdout output (may be empty if script didn't run)
        stderr: Script stderr output (may be empty if script didn't run)
        exit_code: Script exit code (None if script didn't complete)
        current_state: Current state/script file
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
    filename = f"{workflow_id}_{agent_id}_{timestamp}_script.txt"
    error_file = errors_dir / filename

    # Prepare error information
    error_info = {
        "timestamp": datetime.now().isoformat(),
        "workflow_id": workflow_id,
        "agent_id": agent_id,
        "current_state": current_state,
        "script_path": script_path,
        "error_type": type(error).__name__,
        "error_message": str(error),
        "exit_code": exit_code,
    }

    # Write error file
    with open(error_file, 'w', encoding='utf-8') as f:
        f.write("=" * 80 + "\n")
        f.write("SCRIPT ERROR REPORT\n")
        f.write("=" * 80 + "\n\n")

        # Error metadata
        f.write("ERROR INFORMATION:\n")
        f.write("-" * 80 + "\n")
        for key, value in error_info.items():
            f.write(f"{key}: {value}\n")
        f.write("\n")

        # Script stdout
        f.write("SCRIPT STDOUT:\n")
        f.write("-" * 80 + "\n")
        f.write(stdout if stdout else "(empty)\n")
        if stdout and not stdout.endswith('\n'):
            f.write('\n')
        f.write("\n")

        # Script stderr
        f.write("SCRIPT STDERR:\n")
        f.write("-" * 80 + "\n")
        f.write(stderr if stderr else "(empty)\n")
        if stderr and not stderr.endswith('\n'):
            f.write('\n')
        f.write("\n")

        # Full traceback if available
        f.write("TRACEBACK:\n")
        f.write("-" * 80 + "\n")
        f.write(traceback.format_exc())
        f.write("\n")

    logger.info(
        f"Saved script error response to {error_file}",
        extra={
            "workflow_id": workflow_id,
            "agent_id": agent_id,
            "error_file": str(error_file)
        }
    )

    return error_file


def _try_save_script_error(
    workflow_id: str,
    agent_id: str,
    error: Exception,
    script_path: str,
    stdout: str,
    stderr: str,
    exit_code: Optional[int],
    current_state: str,
    state_dir: Optional[str] = None
) -> None:
    """Attempt to save script error response, logging failures without raising.

    This is a wrapper around save_script_error_response that catches exceptions
    to ensure the original error is always raised, even if error saving fails.
    """
    try:
        save_script_error_response(
            workflow_id=workflow_id,
            agent_id=agent_id,
            error=error,
            script_path=script_path,
            stdout=stdout,
            stderr=stderr,
            exit_code=exit_code,
            current_state=current_state,
            state_dir=state_dir
        )
    except Exception as save_error:
        logger.warning(f"Failed to save script error response: {save_error}")


def _extract_state_name(state_filename: str) -> str:
    """Extract state name from a state filename by removing the extension.

    Handles .md, .sh, and .bat extensions case-insensitively.

    Args:
        state_filename: State filename (e.g., "CHECK.md", "SCRIPT.bat")

    Returns:
        State name without extension (e.g., "CHECK", "SCRIPT")
    """
    for ext in ('.md', '.sh', '.bat'):
        if state_filename.lower().endswith(ext):
            return state_filename[:-len(ext)]
    return state_filename


def _resolve_transition_targets(transition: Transition, scope_dir: str) -> Transition:
    """Resolve abstract state names in a transition to concrete filenames.
    
    This function resolves all state references in a transition:
    - The main target (for goto, reset, function, call, fork)
    - The 'return' attribute (for function, call)
    - The 'next' attribute (for fork)
    
    Args:
        transition: Original transition with potentially abstract state names
        scope_dir: Directory containing state files
        
    Returns:
        New Transition with all state references resolved
        
    Raises:
        FileNotFoundError: If any referenced state file doesn't exist
        ValueError: If any state name is ambiguous (multiple files match)
    """
    # Result tags have no state references to resolve
    if transition.tag == "result":
        return transition
    
    # Resolve the main target
    resolved_target = resolve_state(scope_dir, transition.target)
    
    # Resolve attributes that contain state references
    resolved_attributes = dict(transition.attributes)
    
    if "return" in resolved_attributes:
        resolved_attributes["return"] = resolve_state(scope_dir, resolved_attributes["return"])
    
    if "next" in resolved_attributes:
        resolved_attributes["next"] = resolve_state(scope_dir, resolved_attributes["next"])
    
    # Return new transition with resolved values
    return Transition(
        tag=transition.tag,
        target=resolved_target,
        attributes=resolved_attributes,
        payload=transition.payload
    )


async def run_all_agents(workflow_id: str, state_dir: str = None, debug: bool = True, default_model: Optional[str] = None, timeout: Optional[float] = None, dangerously_skip_permissions: bool = False) -> None:
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
        debug: If True, enable debug mode (save outputs and log transitions). Defaults to True.
        default_model: Optional model to use if not specified in frontmatter
        timeout: Optional timeout in seconds for Claude Code invocations (default: 600)
        dangerously_skip_permissions: If True, passes --dangerously-skip-permissions
            to Claude instead of --permission-mode acceptEdits. WARNING: This allows
            Claude to execute any action without prompting for permission.
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
                    agent, state, state_dir, debug_dir, agent_step_counters, default_model, timeout, dangerously_skip_permissions
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
    state_dir: Optional[str] = None
) -> Optional[Dict[str, Any]]:
    """Execute a script state and process its output.

    Script states are executed directly without invoking Claude Code.
    They emit transition tags via stdout which are parsed the same way
    as LLM output. Script states:
    - Don't modify the agent's session_id (preserve session across scripts)
    - Contribute $0.00 to cost tracking
    - Don't support reminder prompts (fatal errors on failure)

    Args:
        agent: Agent state dictionary
        state: Full workflow state dictionary
        scope_dir: Directory containing state files
        workflow_id: Workflow identifier
        current_state: Current state filename (e.g., "CHECK.bat")
        agent_id: Agent identifier
        session_id: Current session ID (preserved, not modified)
        debug_dir: Optional debug directory path
        agent_step_counters: Optional dict to track step numbers per agent
        timeout: Optional timeout in seconds for script execution
        state_dir: Optional custom state directory (for error saving)

    Returns:
        Updated agent state dictionary, or None if agent terminated

    Raises:
        ScriptError: If script execution fails (non-zero exit, timeout, no tag)
    """
    # Build full path to script file
    script_path = str(Path(scope_dir) / current_state)

    # Build environment variables for the script
    pending_result = agent.get("pending_result")
    fork_attributes = agent.get("fork_attributes", {})

    env = build_script_env(
        workflow_id=workflow_id,
        agent_id=agent_id,
        state_dir=scope_dir,
        state_file=script_path,
        result=pending_result,
        fork_attributes=fork_attributes
    )

    logger.info(
        f"Executing script state for agent {agent_id}: {current_state}",
        extra={
            "workflow_id": workflow_id,
            "agent_id": agent_id,
            "current_state": current_state,
            "script_path": script_path
        }
    )

    # Execute the script and track execution time
    script_result = None
    start_time = time.perf_counter()
    try:
        script_result = await run_script(script_path, timeout=timeout, env=env)
    except ScriptTimeoutError as e:
        logger.error(
            f"Script timeout for agent {agent_id}: {current_state}",
            extra={
                "workflow_id": workflow_id,
                "agent_id": agent_id,
                "current_state": current_state,
                "timeout": timeout
            }
        )
        error = ScriptError(f"Script timeout: {e}")
        _try_save_script_error(
            workflow_id=workflow_id,
            agent_id=agent_id,
            error=error,
            script_path=script_path,
            stdout="",
            stderr="",
            exit_code=None,
            current_state=current_state,
            state_dir=state_dir
        )
        raise error from e
    except FileNotFoundError as e:
        logger.error(
            f"Script not found for agent {agent_id}: {current_state}",
            extra={
                "workflow_id": workflow_id,
                "agent_id": agent_id,
                "current_state": current_state,
                "script_path": script_path
            }
        )
        error = ScriptError(f"Script not found: {e}")
        _try_save_script_error(
            workflow_id=workflow_id,
            agent_id=agent_id,
            error=error,
            script_path=script_path,
            stdout="",
            stderr="",
            exit_code=None,
            current_state=current_state,
            state_dir=state_dir
        )
        raise error from e
    except ValueError as e:
        # Platform mismatch or unsupported extension
        logger.error(
            f"Script execution error for agent {agent_id}: {e}",
            extra={
                "workflow_id": workflow_id,
                "agent_id": agent_id,
                "current_state": current_state
            }
        )
        error = ScriptError(f"Script execution error: {e}")
        _try_save_script_error(
            workflow_id=workflow_id,
            agent_id=agent_id,
            error=error,
            script_path=script_path,
            stdout="",
            stderr="",
            exit_code=None,
            current_state=current_state,
            state_dir=state_dir
        )
        raise error from e

    # Calculate execution time
    end_time = time.perf_counter()
    execution_time_ms = (end_time - start_time) * 1000

    logger.debug(
        f"Script completed for agent {agent_id}: exit_code={script_result.exit_code}",
        extra={
            "workflow_id": workflow_id,
            "agent_id": agent_id,
            "current_state": current_state,
            "exit_code": script_result.exit_code,
            "stdout_length": len(script_result.stdout),
            "stderr_length": len(script_result.stderr),
            "execution_time_ms": execution_time_ms
        }
    )

    # Save script output to debug directory if debug mode is enabled
    if debug_dir is not None and agent_step_counters is not None:
        try:
            # Increment step counter for this agent
            if agent_id not in agent_step_counters:
                agent_step_counters[agent_id] = 0
            agent_step_counters[agent_id] += 1
            step_number = agent_step_counters[agent_id]

            # Extract state name (filename without extension)
            state_name = _extract_state_name(current_state)

            # Save the script output
            save_script_output(
                debug_dir=debug_dir,
                agent_id=agent_id,
                state_name=state_name,
                step_number=step_number,
                stdout=script_result.stdout,
                stderr=script_result.stderr
            )

            # Save execution metadata (Step 5.2)
            save_script_output_metadata(
                debug_dir=debug_dir,
                agent_id=agent_id,
                state_name=state_name,
                step_number=step_number,
                exit_code=script_result.exit_code,
                execution_time_ms=execution_time_ms,
                env_vars=env
            )
        except Exception as e:
            # Debug operations should not fail the workflow
            logger.warning(f"Failed to save script debug files: {e}")

    # Check exit code - non-zero is fatal
    if script_result.exit_code != 0:
        error_msg = (
            f"Script '{current_state}' failed with exit code {script_result.exit_code}. "
            f"stderr: {script_result.stderr[:500]}"
        )
        logger.error(
            f"Script failed for agent {agent_id}: {error_msg}",
            extra={
                "workflow_id": workflow_id,
                "agent_id": agent_id,
                "current_state": current_state,
                "exit_code": script_result.exit_code,
                "stderr": script_result.stderr
            }
        )
        error = ScriptError(error_msg)
        _try_save_script_error(
            workflow_id=workflow_id,
            agent_id=agent_id,
            error=error,
            script_path=script_path,
            stdout=script_result.stdout,
            stderr=script_result.stderr,
            exit_code=script_result.exit_code,
            current_state=current_state,
            state_dir=state_dir
        )
        raise error

    # Parse transitions from stdout
    output_text = script_result.stdout
    transitions = parse_transitions(output_text)

    # Validate exactly one transition
    if len(transitions) == 0:
        error_msg = f"Script '{current_state}' produced no transition tag in stdout"
        logger.error(
            f"No transition tag from script for agent {agent_id}",
            extra={
                "workflow_id": workflow_id,
                "agent_id": agent_id,
                "current_state": current_state,
                "stdout": output_text[:500]
            }
        )
        error = ScriptError(error_msg)
        _try_save_script_error(
            workflow_id=workflow_id,
            agent_id=agent_id,
            error=error,
            script_path=script_path,
            stdout=script_result.stdout,
            stderr=script_result.stderr,
            exit_code=script_result.exit_code,
            current_state=current_state,
            state_dir=state_dir
        )
        raise error

    if len(transitions) > 1:
        error_msg = f"Script '{current_state}' produced {len(transitions)} transition tags (expected 1)"
        logger.error(
            f"Multiple transition tags from script for agent {agent_id}",
            extra={
                "workflow_id": workflow_id,
                "agent_id": agent_id,
                "current_state": current_state,
                "transition_count": len(transitions)
            }
        )
        error = ScriptError(error_msg)
        _try_save_script_error(
            workflow_id=workflow_id,
            agent_id=agent_id,
            error=error,
            script_path=script_path,
            stdout=script_result.stdout,
            stderr=script_result.stderr,
            exit_code=script_result.exit_code,
            current_state=current_state,
            state_dir=state_dir
        )
        raise error

    transition = transitions[0]

    # Resolve abstract state names in transition
    try:
        transition = _resolve_transition_targets(transition, scope_dir)
    except FileNotFoundError as e:
        error = ScriptError(f"Transition target not found: {e}")
        _try_save_script_error(
            workflow_id=workflow_id,
            agent_id=agent_id,
            error=error,
            script_path=script_path,
            stdout=script_result.stdout,
            stderr=script_result.stderr,
            exit_code=script_result.exit_code,
            current_state=current_state,
            state_dir=state_dir
        )
        raise error from e
    except ValueError as e:
        error = ScriptError(f"Transition resolution error: {e}")
        _try_save_script_error(
            workflow_id=workflow_id,
            agent_id=agent_id,
            error=error,
            script_path=script_path,
            stdout=script_result.stdout,
            stderr=script_result.stderr,
            exit_code=script_result.exit_code,
            current_state=current_state,
            state_dir=state_dir
        )
        raise error from e

    logger.debug(
        f"Parsed transition from script for agent {agent_id}: {transition.tag} -> {transition.target}",
        extra={
            "workflow_id": workflow_id,
            "agent_id": agent_id,
            "transition_tag": transition.tag,
            "transition_target": transition.target
        }
    )

    # Create a deep copy of agent for handler (to avoid mutating original)
    agent_copy = copy.deepcopy(agent)

    # Script states do NOT update session_id - preserve the existing session
    # This is different from markdown states which get new session_id from Claude Code

    # Clear pending_result after using it (it was only for this step)
    if "pending_result" in agent_copy:
        del agent_copy["pending_result"]

    # Clear fork_session_id (not used by scripts, but clear for consistency)
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
        raise ScriptError(f"Unknown transition tag from script: {transition.tag}")

    # Call handler
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
            "state_type": "script",
            "cost": "$0.00",  # Scripts contribute no cost
            "exit_code": script_result.exit_code,
            "execution_time_ms": execution_time_ms,
        }

        # Add session_id information
        if session_id:
            metadata["session_id"] = f"{session_id} (preserved)"

        # Add transition-specific metadata
        if transition.tag == "function" or transition.tag == "call":
            if "return" in transition.attributes:
                metadata["return_state"] = transition.attributes["return"]
        elif transition.tag == "fork":
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


async def step_agent(
    agent: Dict[str, Any],
    state: Dict[str, Any],
    state_dir: Optional[str] = None,
    debug_dir: Optional[Path] = None,
    agent_step_counters: Optional[Dict[str, int]] = None,
    default_model: Optional[str] = None,
    timeout: Optional[float] = None,
    dangerously_skip_permissions: bool = False
) -> Optional[Dict[str, Any]]:
    """Step a single agent: load prompt, invoke Claude Code, parse output, dispatch.
    
    Args:
        agent: Agent state dictionary
        state: Full workflow state dictionary
        state_dir: Optional custom state directory
        debug_dir: Optional debug directory path (for saving outputs)
        agent_step_counters: Optional dict to track step numbers per agent
        default_model: Optional model to use if not specified in frontmatter
        timeout: Optional timeout in seconds for Claude Code invocations (default: 600)
        dangerously_skip_permissions: If True, passes --dangerously-skip-permissions
            to Claude instead of --permission-mode acceptEdits.
        
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

    # Determine state type and dispatch accordingly
    state_type = get_state_type(current_state)

    if state_type == "script":
        # Script execution path - execute directly without LLM
        return await _step_agent_script(
            agent, state, scope_dir, workflow_id, current_state, agent_id, session_id,
            debug_dir, agent_step_counters, timeout, state_dir
        )

    # Markdown state - continue with LLM execution path

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
        
        # Prepare debug file path for progressive writes if debug mode is enabled
        debug_filepath = None
        if debug_dir is not None and agent_step_counters is not None:
            try:
                # Increment step counter for this agent
                if agent_id not in agent_step_counters:
                    agent_step_counters[agent_id] = 0
                agent_step_counters[agent_id] += 1
                step_number = agent_step_counters[agent_id]
                
                # Extract state name (filename without extension)
                state_name = _extract_state_name(current_state)

                # Get filepath for JSONL output (progressive writes)
                debug_filepath = get_claude_output_filepath(
                    debug_dir=debug_dir,
                    agent_id=agent_id,
                    state_name=state_name,
                    step_number=step_number
                )
            except OSError as e:
                # Debug operations should not fail the workflow
                logger.warning(f"Failed to prepare debug filepath: {e}")
        
        try:
            # Use streaming to get progressive output with idle timeout
            # This allows debug writes as data arrives and properly handles
            # long-running Claude Code executions
            results = []
            
            # Determine which session to use
            if fork_session_id is not None and reminder_attempt == 0:
                # Only use fork_session_id on first attempt
                use_session_id = fork_session_id
                use_fork = True
            else:
                # Use regular session_id (or new_session_id from previous attempt)
                use_session_id = new_session_id
                use_fork = False
            
            # Stream JSON objects from Claude Code
            async for json_obj in wrap_claude_code_stream(
                prompt,
                model=model_to_use,
                session_id=use_session_id,
                timeout=timeout,
                dangerously_skip_permissions=dangerously_skip_permissions,
                fork=use_fork
            ):
                # Append to results list (needed for text/cost extraction later)
                results.append(json_obj)
                
                # Progressive write to debug file
                # Wrapped in try/except because debug operations should not fail the workflow
                if debug_filepath is not None:
                    try:
                        append_claude_output_line(debug_filepath, json_obj)
                    except OSError as e:
                        logger.warning(f"Failed to append Claude output for debug: {e}")
                
                # Extract session_id from JSON objects if present
                # Claude Code may output session_id in various formats
                if isinstance(json_obj, dict):
                    if "session_id" in json_obj:
                        new_session_id = json_obj["session_id"]
                    # Also check for nested session_id (e.g., in metadata)
                    elif "metadata" in json_obj and isinstance(json_obj["metadata"], dict):
                        if "session_id" in json_obj["metadata"]:
                            new_session_id = json_obj["metadata"]["session_id"]
            
            logger.debug(
                f"Claude Code invocation completed for agent {agent_id}",
                extra={
                    "workflow_id": workflow_id,
                    "agent_id": agent_id,
                    "new_session_id": new_session_id,
                    "result_count": len(results)
                }
            )
        except ClaudeCodeTimeoutError as e:
            # Handle idle timeout specifically
            logger.error(
                f"Claude Code idle timeout for agent {agent_id}",
                extra={
                    "workflow_id": workflow_id,
                    "agent_id": agent_id,
                    "current_state": current_state,
                    "error_message": str(e)
                }
            )
            # Save error information (debug file may have partial data)
            save_error_response(
                workflow_id=workflow_id,
                agent_id=agent_id,
                error=e,
                output_text="Claude Code idle timeout - partial output may be in debug file",
                raw_results=[],  # Partial results already in debug file
                session_id=new_session_id or session_id or fork_session_id,
                current_state=current_state,
                state_dir=state_dir
            )
            raise ClaudeCodeError(f"Claude Code idle timeout: {e}") from e
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
        budget_usd = state.get("budget_usd", 10.0)  # Default budget if not set
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
            
            # Resolve abstract state names in implicit transition (same as explicit transitions)
            try:
                transition = _resolve_transition_targets(transition, scope_dir)
            except FileNotFoundError as e:
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
            except ValueError as e:
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
            
            # Resolve abstract state names in transition before policy validation
            # This ensures policies work correctly with both abstract ("NEXT") and
            # explicit ("NEXT.md") state references
            try:
                transition = _resolve_transition_targets(transition, scope_dir)
            except FileNotFoundError as e:
                # Target doesn't exist - will be raised properly in handler
                # but we can fail fast here with better context
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
            except ValueError as e:
                # Ambiguous state name or other resolution error
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
    
    Note: The transition target is already resolved by step_agent before this
    handler is called, so we use transition.target directly.
    
    Args:
        agent: Agent state dictionary
        transition: Transition object with resolved target filename
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
    - Parent agent continues at next state (preserves session and stack)
    - Fork attributes (beyond 'next') are available as template variables for new agent
    
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
        transition: Transition object with resolved target filename and next attribute
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
    
    # Store fork attributes (excluding 'next') for template substitution
    fork_attributes = {
        k: v for k, v in transition.attributes.items() if k != "next"
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
