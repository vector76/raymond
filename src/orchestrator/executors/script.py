"""Script state executor for shell/batch script execution.

This module contains the ScriptExecutor class that handles .sh and .bat states
by running them as subprocesses.

ScriptExecutor is responsible for:
- Building environment variables for the script
- Executing the script via subprocess
- Parsing transitions from stdout
- Emitting events for debug logging and console output

Key differences from MarkdownExecutor:
- No Claude Code invocation - runs subprocess via run_script()
- No reminder prompts - errors are fatal
- Preserves existing session_id (scripts don't create sessions)
- Cost is always $0.00
"""

import json
import logging
import time
from pathlib import Path
from typing import Any, Dict, Optional

# Import src.orchestrator as a module to support test patching
import src.orchestrator as orchestrator

from src.orchestrator.errors import ScriptError
from src.orchestrator.events import (
    StateStarted,
    StateCompleted,
    ScriptOutput,
)
from src.orchestrator.executors.base import ExecutionResult
from src.orchestrator.executors.context import ExecutionContext
from src.orchestrator.executors.utils import extract_state_name, resolve_transition_targets

logger = logging.getLogger(__name__)


class ScriptExecutor:
    """Executor for script (.sh, .bat) states.

    Handles script states by running them as subprocesses and parsing
    transitions from stdout.

    Scripts:
    - Must emit exactly one transition tag on stdout
    - Return non-zero exit code on failure
    - Don't support reminder prompts (errors are fatal)
    - Don't modify session_id (preserve existing)
    - Contribute $0.00 to cost tracking
    """

    async def execute(
        self,
        agent: Dict[str, Any],
        state: Dict[str, Any],
        context: ExecutionContext
    ) -> ExecutionResult:
        """Execute a script state.

        Args:
            agent: Agent state dictionary
            state: Full workflow state dictionary
            context: Execution context

        Returns:
            ExecutionResult with transition, session_id (preserved), and cost (0.0)

        Raises:
            ScriptError: If script execution fails, times out, or produces
                invalid output
        """
        scope_dir = state["scope_dir"]
        workflow_id = state.get("workflow_id", context.workflow_id)
        current_state = agent["current_state"]
        agent_id = agent.get("id", "unknown")
        session_id = agent.get("session_id")  # Preserved, not modified

        # Build full path to script file
        script_path = str(Path(scope_dir) / current_state)

        # Build environment variables for the script
        pending_result = agent.get("pending_result")
        fork_attributes = agent.get("fork_attributes", {})

        env = orchestrator.build_script_env(
            workflow_id=workflow_id,
            agent_id=agent_id,
            state_dir=scope_dir,
            state_file=script_path,
            result=pending_result,
            fork_attributes=fork_attributes
        )

        # Emit StateStarted event (ConsoleObserver handles display)
        context.bus.emit(StateStarted(
            agent_id=agent_id,
            state_name=current_state,
            state_type="script",
        ))

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
        start_time = time.perf_counter()
        script_result = None

        try:
            script_result = await orchestrator.run_script(
                script_path, timeout=context.timeout, env=env
            )
        except orchestrator.ScriptTimeoutError as e:
            logger.error(
                f"Script timeout for agent {agent_id}: {current_state}",
                extra={
                    "workflow_id": workflow_id,
                    "agent_id": agent_id,
                    "current_state": current_state,
                    "timeout": context.timeout
                }
            )
            error = ScriptError(f"Script timeout: {e}")
            self._try_save_script_error(
                workflow_id, agent_id, error, script_path,
                "", "", None, current_state, context.state_dir
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
            self._try_save_script_error(
                workflow_id, agent_id, error, script_path,
                "", "", None, current_state, context.state_dir
            )
            raise error from e
        except ValueError as e:
            logger.error(
                f"Script execution error for agent {agent_id}: {e}",
                extra={
                    "workflow_id": workflow_id,
                    "agent_id": agent_id,
                    "current_state": current_state
                }
            )
            error = ScriptError(f"Script execution error: {e}")
            self._try_save_script_error(
                workflow_id, agent_id, error, script_path,
                "", "", None, current_state, context.state_dir
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

        # Get step number and save debug output
        step_number = None
        if context.debug_dir is not None:
            try:
                step_number = context.get_next_step_number(agent_id)
                state_name = extract_state_name(current_state)

                # Save script output files
                self._save_script_output(
                    context.debug_dir, agent_id, state_name, step_number,
                    script_result.stdout, script_result.stderr
                )

                # Save metadata
                self._save_script_output_metadata(
                    context.debug_dir, agent_id, state_name, step_number,
                    script_result.exit_code, execution_time_ms, env
                )
            except Exception as e:
                logger.warning(f"Failed to save script debug files: {e}")

        # Emit ScriptOutput event
        if step_number is not None:
            context.bus.emit(ScriptOutput(
                agent_id=agent_id,
                state_name=current_state,
                step_number=step_number,
                stdout=script_result.stdout,
                stderr=script_result.stderr,
                exit_code=script_result.exit_code,
                execution_time_ms=execution_time_ms,
                env_vars=env,
            ))

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
            self._try_save_script_error(
                workflow_id, agent_id, error, script_path,
                script_result.stdout, script_result.stderr,
                script_result.exit_code, current_state, context.state_dir
            )
            raise error

        # Parse transitions from stdout
        output_text = script_result.stdout
        transitions = orchestrator.parse_transitions(output_text)

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
            self._try_save_script_error(
                workflow_id, agent_id, error, script_path,
                script_result.stdout, script_result.stderr,
                script_result.exit_code, current_state, context.state_dir
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
            self._try_save_script_error(
                workflow_id, agent_id, error, script_path,
                script_result.stdout, script_result.stderr,
                script_result.exit_code, current_state, context.state_dir
            )
            raise error

        transition = transitions[0]

        # Resolve transition targets
        try:
            transition = resolve_transition_targets(transition, scope_dir)
        except FileNotFoundError as e:
            error = ScriptError(f"Transition target not found: {e}")
            self._try_save_script_error(
                workflow_id, agent_id, error, script_path,
                script_result.stdout, script_result.stderr,
                script_result.exit_code, current_state, context.state_dir
            )
            raise error from e
        except ValueError as e:
            error = ScriptError(f"Transition resolution error: {e}")
            self._try_save_script_error(
                workflow_id, agent_id, error, script_path,
                script_result.stdout, script_result.stderr,
                script_result.exit_code, current_state, context.state_dir
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

        # Emit StateCompleted event
        total_cost = state.get("total_cost_usd", 0.0)
        context.bus.emit(StateCompleted(
            agent_id=agent_id,
            state_name=current_state,
            cost_usd=0.0,  # Scripts are free
            total_cost_usd=total_cost,
            session_id=session_id,
            duration_ms=execution_time_ms,
        ))

        return ExecutionResult(
            transition=transition,
            session_id=session_id,  # Preserved, not modified
            cost_usd=0.0  # Scripts are free
        )

    def _try_save_script_error(
        self,
        workflow_id: str,
        agent_id: str,
        error: Exception,
        script_path: str,
        stdout: str,
        stderr: str,
        exit_code: Optional[int],
        current_state: str,
        state_dir: Optional[str]
    ) -> None:
        """Attempt to save script error response."""
        try:
            orchestrator.save_script_error_response(
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
        except Exception as e:
            logger.warning(f"Failed to save script error response: {e}")

    def _save_script_output(
        self,
        debug_dir: Path,
        agent_id: str,
        state_name: str,
        step_number: int,
        stdout: str,
        stderr: str
    ) -> None:
        """Save script stdout and stderr to debug directory."""
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

    def _save_script_output_metadata(
        self,
        debug_dir: Path,
        agent_id: str,
        state_name: str,
        step_number: int,
        exit_code: int,
        execution_time_ms: float,
        env_vars: Dict[str, str]
    ) -> None:
        """Save script execution metadata to debug directory."""
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
