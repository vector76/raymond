"""Main workflow loop for orchestrator.

This module provides the run_all_agents() function, the main entry point for
running agent workflows. It coordinates:
- State execution via Executors
- Transition handling
- Error recovery and retry logic
- Event emission via EventBus
- Observer management (debug, console)

The workflow loop uses asyncio.wait with FIRST_COMPLETED to process agent
completions as they arrive, enabling concurrent multi-agent execution.
"""

import asyncio
import logging
from pathlib import Path
from typing import Any, Dict, Optional

# Import src.orchestrator as a module to support test patching
import src.orchestrator as orchestrator

from src.orchestrator.bus import EventBus
from src.orchestrator.errors import (
    ClaudeCodeError,
    ClaudeCodeLimitError,
    ClaudeCodeTimeoutWrappedError,
    PromptFileError,
    ScriptError,
)
from src.orchestrator.events import (
    WorkflowStarted,
    WorkflowCompleted,
    WorkflowPaused,
    WorkflowWaiting,
    WorkflowResuming,
    TransitionOccurred,
    AgentSpawned,
    AgentTerminated,
    AgentPaused,
    ErrorOccurred,
)
from src.orchestrator.limit_wait import (
    parse_limit_reset_time,
    calculate_wait_seconds,
)
from src.orchestrator.executors import get_executor, ExecutionContext
from src.orchestrator.observers.console import ConsoleObserver
from src.orchestrator.observers.debug import DebugObserver
from src.orchestrator.transitions import apply_transition

logger = logging.getLogger(__name__)

# Maximum number of retries for transient errors
MAX_RETRIES = 3


def _create_debug_directory(workflow_id: str, state_dir: str = None) -> Optional[Path]:
    """Create a debug directory for the workflow.

    Creates a timestamped directory under the workflow's scope_dir/.raymond/debug/
    to store debug output files.

    Args:
        workflow_id: Unique identifier for the workflow
        state_dir: Optional custom state directory

    Returns:
        Path to the debug directory, or None if creation failed
    """
    # Import here to avoid circular imports during module loading
    from src.orchestrator.debug_utils import create_debug_directory
    return create_debug_directory(workflow_id, state_dir=state_dir)


async def _step_agent(
    agent: Dict[str, Any],
    state: Dict[str, Any],
    context: ExecutionContext,
    bus: EventBus
) -> Optional[Dict[str, Any]]:
    """Execute a single agent step and apply the transition.

    This helper function:
    1. Gets the appropriate executor for the agent's current state
    2. Executes the state via the executor
    3. Applies the transition using apply_transition()
    4. Emits transition-related events
    5. Updates session_id from the result

    Args:
        agent: Agent state dictionary
        state: Full workflow state dictionary
        context: Execution context
        bus: EventBus for emitting events

    Returns:
        Updated agent dictionary, or None if agent terminated

    Raises:
        Any exception from executor or transition handler
    """
    current_state = agent["current_state"]
    agent_id = agent.get("id", "unknown")

    # Get state type for metadata in transition events
    state_type = orchestrator.get_state_type(current_state)

    # Get the appropriate executor
    executor = get_executor(current_state)

    # Execute the state
    result = await executor.execute(agent, state, context)

    # Apply the transition
    transition = result.transition
    from_state = current_state

    # Call apply_transition - returns updated agent, (parent, child) tuple, or None
    transition_result = apply_transition(agent, transition, state)

    # Handle different transition results and emit events
    if transition_result is None:
        # Agent terminated (result with empty stack)
        termination_results = state.get("_agent_termination_results", {})
        result_payload = termination_results.get(agent_id, "")

        # Emit TransitionOccurred event
        bus.emit(TransitionOccurred(
            agent_id=agent_id,
            from_state=from_state,
            to_state=None,
            transition_type=transition.tag,
            metadata={"result_payload": result_payload, "state_type": state_type}
        ))

        # Emit AgentTerminated event
        bus.emit(AgentTerminated(
            agent_id=agent_id,
            result_payload=result_payload
        ))

        return None

    elif isinstance(transition_result, tuple):
        # Fork transition - (updated parent, new worker)
        updated_agent, new_agent = transition_result

        # Update session_id from execution result
        if result.session_id is not None:
            updated_agent["session_id"] = result.session_id

        # Add new agent to state
        state["agents"].append(new_agent)

        # Emit TransitionOccurred for parent
        bus.emit(TransitionOccurred(
            agent_id=agent_id,
            from_state=from_state,
            to_state=updated_agent.get("current_state"),
            transition_type=transition.tag,
            metadata={"spawned_agent_id": new_agent.get("id"), "state_type": state_type}
        ))

        # Emit AgentSpawned event
        bus.emit(AgentSpawned(
            parent_agent_id=agent_id,
            new_agent_id=new_agent.get("id"),
            initial_state=new_agent.get("current_state")
        ))

        return updated_agent

    else:
        # Normal transition - updated agent dict
        updated_agent = transition_result

        # Update session_id from execution result
        if result.session_id is not None:
            updated_agent["session_id"] = result.session_id

        # Emit TransitionOccurred event
        bus.emit(TransitionOccurred(
            agent_id=agent_id,
            from_state=from_state,
            to_state=updated_agent.get("current_state"),
            transition_type=transition.tag,
            metadata={"state_type": state_type}
        ))

        return updated_agent


def _reset_paused_agents(state: Dict[str, Any], workflow_id: str) -> None:
    """Reset all paused agents so they can run again.

    Clears the status, retry_count, and error fields while preserving
    session_id for Claude Code --resume.

    Args:
        state: Full workflow state dictionary.
        workflow_id: Workflow identifier (for logging).
    """
    for agent in state.get("agents", []):
        if agent.get("status") == "paused":
            agent.pop("status", None)
            agent.pop("retry_count", None)
            agent.pop("error", None)
            # Keep session_id for Claude Code --resume
            logger.info(
                f"Reset paused agent {agent['id']} for resume",
                extra={"workflow_id": workflow_id, "agent_id": agent["id"]}
            )


# Buffer minutes to add after the stated reset time
_LIMIT_RESET_BUFFER_MINUTES = 5

# Threshold in hours for printing a "long wait" warning
_LONG_WAIT_THRESHOLD_HOURS = 5


async def run_all_agents(
    workflow_id: str,
    state_dir: str = None,
    debug: bool = True,
    default_model: Optional[str] = None,
    default_effort: Optional[str] = None,
    timeout: Optional[float] = None,
    dangerously_skip_permissions: bool = False,
    quiet: bool = False,
    width: Optional[int] = None,
    no_wait: bool = False
) -> None:
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
        default_effort: Optional effort level to use if not specified in frontmatter
        timeout: Optional timeout in seconds for Claude Code invocations (default: 600)
        dangerously_skip_permissions: If True, passes --dangerously-skip-permissions
            to Claude instead of --permission-mode acceptEdits. WARNING: This allows
            Claude to execute any action without prompting for permission.
        quiet: If True, suppress progress messages and tool invocations in console output
        width: Override terminal width for console output. If None, auto-detect from
            environment. Useful in Docker/non-TTY environments.
        no_wait: If True, don't auto-wait for limit reset; pause and exit immediately.
    """
    # Initialize console reporter
    orchestrator.init_reporter(quiet=quiet, width=width)
    reporter = orchestrator.get_reporter()

    logger.info(f"Starting orchestrator for workflow: {workflow_id}")

    # Create debug directory if debug mode is enabled
    debug_dir = None
    if debug:
        debug_dir = _create_debug_directory(workflow_id, state_dir=state_dir)
        if debug_dir:
            logger.info(f"Debug mode enabled: {debug_dir}")
        else:
            logger.warning("Debug mode requested but directory creation failed, continuing without debug")

    # Read state to get scope_dir for console output
    state = orchestrator.read_state(workflow_id, state_dir=state_dir)

    # Reset paused agents on resume (allows them to run again)
    _reset_paused_agents(state, workflow_id)

    scope_dir = state.get("scope_dir", "unknown")

    # Create EventBus
    bus = EventBus()

    # Create and attach DebugObserver if debug mode is enabled
    debug_observer = None
    if debug and debug_dir:
        debug_observer = DebugObserver(debug_dir, bus)

    # Create and attach ConsoleObserver if not quiet
    console_observer = None
    if not quiet:
        console_observer = ConsoleObserver(reporter, bus)

    # Emit WorkflowStarted event
    bus.emit(WorkflowStarted(
        workflow_id=workflow_id,
        scope_dir=scope_dir,
        debug_dir=debug_dir
    ))

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
        reporter=reporter if not quiet else None
    )

    # Track running tasks by agent ID - ensures exactly one task per agent
    running_tasks: Dict[str, asyncio.Task] = {}

    async def _cleanup_running_tasks():
        """Cancel all running tasks to prevent zombie tasks on error exit."""
        if not running_tasks:
            return
        task_count = len(running_tasks)
        logger.debug(f"Cleaning up {task_count} running task(s) before exit")
        for task in list(running_tasks.values()):
            if not task.done():
                task.cancel()
        try:
            await asyncio.gather(*running_tasks.values(), return_exceptions=True)
        except asyncio.CancelledError:
            pass
        running_tasks.clear()

    try:
        while True:
            agents = state.get("agents", [])

            # Exit if no agents remain
            if not agents:
                logger.info(f"Workflow {workflow_id} completed: no agents remaining")
                total_cost = state.get("total_cost_usd", 0.0)

                # Emit WorkflowCompleted event
                bus.emit(WorkflowCompleted(
                    workflow_id=workflow_id,
                    total_cost_usd=total_cost
                ))

                # Clean up state file - workflow completed successfully
                state.pop("_agent_termination_results", None)
                orchestrator.delete_state(workflow_id, state_dir=state_dir)
                logger.debug(f"Deleted state file for completed workflow: {workflow_id}")
                break

            # Check if all remaining agents are paused
            paused_agents = [a for a in agents if a.get("status") == "paused"]
            if len(paused_agents) == len(agents):
                total_cost = state.get("total_cost_usd", 0.0)
                logger.info(
                    f"Workflow {workflow_id} paused: all {len(paused_agents)} agent(s) paused",
                    extra={
                        "workflow_id": workflow_id,
                        "paused_agent_count": len(paused_agents),
                        "total_cost": total_cost
                    }
                )

                # Try auto-wait if enabled: parse reset times from all paused agents
                should_auto_wait = False
                latest_reset_time = None

                if not no_wait:
                    reset_times = []
                    for agent in paused_agents:
                        error_msg = agent.get("error", "")
                        reset_time = parse_limit_reset_time(error_msg)
                        if reset_time is not None:
                            reset_times.append(reset_time)
                        else:
                            # Agent paused for non-limit reason or unparseable message
                            # Fall back to pause-and-exit
                            reset_times = []
                            break

                    if reset_times:
                        latest_reset_time = max(reset_times)
                        should_auto_wait = True

                if should_auto_wait and latest_reset_time is not None:
                    # Auto-wait path: save state, sleep, then resume
                    wait_seconds = calculate_wait_seconds(
                        latest_reset_time,
                        buffer_minutes=_LIMIT_RESET_BUFFER_MINUTES
                    )

                    # Emit WorkflowWaiting event (console observer shows wait message)
                    bus.emit(WorkflowWaiting(
                        workflow_id=workflow_id,
                        total_cost_usd=total_cost,
                        paused_agent_count=len(paused_agents),
                        reset_time=latest_reset_time,
                        wait_seconds=wait_seconds
                    ))

                    # Warn if wait is unusually long
                    wait_hours = wait_seconds / 3600
                    if wait_hours > _LONG_WAIT_THRESHOLD_HOURS:
                        hours = int(wait_hours)
                        minutes = int((wait_seconds % 3600) / 60)
                        logger.warning(
                            f"Wait time is {hours}h {minutes}m — this is unusually long",
                            extra={"workflow_id": workflow_id}
                        )

                    # Save state before sleeping (Ctrl+C during wait leaves resumable state)
                    orchestrator.write_state(workflow_id, state, state_dir=state_dir)

                    # Sleep until reset time + buffer (or return immediately if past)
                    if wait_seconds > 0:
                        await asyncio.sleep(wait_seconds)

                    # Emit WorkflowResuming event
                    bus.emit(WorkflowResuming(workflow_id=workflow_id))

                    # Reset paused agents and continue the loop
                    _reset_paused_agents(state, workflow_id)
                    continue
                else:
                    # Pause-and-exit path (original behavior)
                    bus.emit(WorkflowPaused(
                        workflow_id=workflow_id,
                        total_cost_usd=total_cost,
                        paused_agent_count=len(paused_agents)
                    ))

                    # Save state file for resume (don't delete)
                    orchestrator.write_state(workflow_id, state, state_dir=state_dir)
                    break

            logger.debug(
                f"Workflow {workflow_id}: {len(agents)} agent(s) active, "
                f"{len(running_tasks)} task(s) running",
                extra={
                    "workflow_id": workflow_id,
                    "agent_count": len(agents),
                    "running_task_count": len(running_tasks)
                }
            )

            # Create tasks only for agents that don't already have a running task
            # Skip paused agents - they are waiting for resume
            for agent in agents:
                agent_id = agent["id"]
                if agent.get("status") == "paused":
                    continue
                if agent_id not in running_tasks:
                    task = asyncio.create_task(_step_agent(
                        agent, state, context, bus
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
            try:
                done, _ = await asyncio.wait(
                    running_tasks.values(),
                    return_when=asyncio.FIRST_COMPLETED
                )
            except asyncio.CancelledError:
                await _cleanup_running_tasks()
                raise

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

                # Remove from running tasks
                del running_tasks[agent_id]

                # Get the agent dict
                agent = next(
                    (a for a in state["agents"] if a["id"] == agent_id),
                    None
                )

                try:
                    result = task.result()

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
                        logger.info(
                            f"Agent {agent_id} terminated",
                            extra={
                                "workflow_id": workflow_id,
                                "agent_id": agent_id
                            }
                        )
                        state["agents"] = [
                            a for a in state["agents"]
                            if a["id"] != agent_id
                        ]

                except ClaudeCodeLimitError as e:
                    # Handle limit errors with pause/resume behavior (no retries)
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

                    # Emit error event
                    bus.emit(ErrorOccurred(
                        agent_id=agent_id,
                        error_type="ClaudeCodeLimitError",
                        error_message=str(e),
                        current_state=current_agent.get("current_state"),
                        is_retryable=False,
                        retry_count=0,
                        max_retries=0
                    ))

                    logger.warning(
                        f"Agent {agent_id} hit Claude Code limit. Pausing for later resume.",
                        extra={
                            "workflow_id": workflow_id,
                            "agent_id": agent_id,
                            "error_type": type(e).__name__,
                            "error_message": str(e)
                        }
                    )

                    # Pause agent
                    current_agent["status"] = "paused"
                    current_agent["error"] = str(e)
                    state["agents"][agent_idx] = current_agent

                    # Emit AgentPaused event
                    bus.emit(AgentPaused(
                        agent_id=agent_id,
                        reason="usage limit"
                    ))

                except ClaudeCodeTimeoutWrappedError as e:
                    # Handle timeout errors with pause/resume behavior
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
                        f"Agent {agent_id} timeout (attempt {retry_count}/{MAX_RETRIES}): {e}",
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
                        # Max retries exceeded - pause agent
                        logger.warning(
                            f"Agent {agent_id} timed out after {MAX_RETRIES} retries. "
                            f"Pausing agent for later resume.",
                            extra={
                                "workflow_id": workflow_id,
                                "agent_id": agent_id,
                                "error_type": type(e).__name__,
                                "retry_count": retry_count,
                                "error_message": str(e)
                            }
                        )

                        bus.emit(ErrorOccurred(
                            agent_id=agent_id,
                            error_type="ClaudeCodeTimeoutWrappedError",
                            error_message=f"{str(e)} (pausing after {MAX_RETRIES} attempts)",
                            current_state=current_agent.get("current_state"),
                            is_retryable=False,
                            retry_count=retry_count,
                            max_retries=MAX_RETRIES
                        ))

                        current_agent["status"] = "paused"
                        current_agent["error"] = str(e)
                        state["agents"][agent_idx] = current_agent

                        bus.emit(AgentPaused(
                            agent_id=agent_id,
                            reason="timeout"
                        ))
                    else:
                        # Retry
                        bus.emit(ErrorOccurred(
                            agent_id=agent_id,
                            error_type="ClaudeCodeTimeoutWrappedError",
                            error_message=str(e),
                            current_state=current_agent.get("current_state"),
                            is_retryable=True,
                            retry_count=retry_count,
                            max_retries=MAX_RETRIES
                        ))
                        state["agents"][agent_idx] = current_agent

                except (ClaudeCodeError, PromptFileError) as e:
                    # Handle recoverable errors with retry logic
                    # Note: ScriptError is NOT retried - it's a fatal error
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

                        bus.emit(ErrorOccurred(
                            agent_id=agent_id,
                            error_type=type(e).__name__,
                            error_message=f"{str(e)} (failed after {MAX_RETRIES} attempts)",
                            current_state=current_agent.get("current_state"),
                            is_retryable=False,
                            retry_count=retry_count,
                            max_retries=MAX_RETRIES
                        ))

                        current_agent["status"] = "failed"
                        current_agent["error"] = str(e)
                        # Remove failed agent from active agents
                        state["agents"] = [
                            a for a in state["agents"]
                            if a["id"] != agent_id
                        ]
                    else:
                        # Retry
                        bus.emit(ErrorOccurred(
                            agent_id=agent_id,
                            error_type=type(e).__name__,
                            error_message=str(e),
                            current_state=current_agent.get("current_state"),
                            is_retryable=True,
                            retry_count=retry_count,
                            max_retries=MAX_RETRIES
                        ))
                        state["agents"][agent_idx] = current_agent

                except orchestrator.StateFileError as e:
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
                    await _cleanup_running_tasks()
                    raise

                except Exception as e:
                    # Unexpected errors - save error info and re-raise
                    if getattr(e, '_error_saved', False):
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
                        await _cleanup_running_tasks()
                        raise

                    # Error wasn't saved yet
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

                    # Try to save error information
                    try:
                        orchestrator.save_error_response(
                            workflow_id=workflow_id,
                            agent_id=agent_id,
                            error=e,
                            output_text="No output captured",
                            raw_results=[],
                            session_id=error_session_id,
                            current_state=agent.get("current_state", "unknown") if agent else "unknown",
                            state_dir=state_dir
                        )
                    except Exception as save_error:
                        logger.warning(f"Failed to save error response: {save_error}")

                    await _cleanup_running_tasks()
                    raise

            # Write updated state for crash recovery
            orchestrator.write_state(workflow_id, state, state_dir=state_dir)

    finally:
        # Clean up observers
        if debug_observer:
            debug_observer.close()
        if console_observer:
            console_observer.close()
