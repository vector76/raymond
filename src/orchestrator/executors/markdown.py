"""Markdown state executor for Claude Code invocations.

This module contains the MarkdownExecutor class that handles .md states by
invoking Claude Code.

MarkdownExecutor is responsible for:
- Loading and rendering prompt templates
- Invoking Claude Code with streaming
- Handling the reminder prompt retry loop
- Parsing and validating transitions
- Emitting events for debug logging and console output
- Cost tracking and budget checking
"""

import json
import logging
import time
from contextlib import aclosing
from pathlib import Path
from typing import Any, Dict, List, Optional

# Import src.orchestrator as a module to support test patching
import src.orchestrator as orchestrator

from src.orchestrator.errors import (
    ClaudeCodeError,
    ClaudeCodeLimitError,
    ClaudeCodeTimeoutWrappedError,
    PromptFileError,
)
from src.orchestrator.events import (
    StateStarted,
    StateCompleted,
    ClaudeStreamOutput,
    ClaudeInvocationStarted,
    ToolInvocation,
    ProgressMessage,
    ErrorOccurred,
)
from src.orchestrator.executors.base import ExecutionResult
from src.orchestrator.executors.context import ExecutionContext
from src.orchestrator.executors.utils import extract_state_name, resolve_transition_targets

logger = logging.getLogger(__name__)

# Maximum number of reminder prompts before terminating
MAX_REMINDER_ATTEMPTS = 3


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


class MarkdownExecutor:
    """Executor for markdown (.md) states.

    Handles markdown states by invoking Claude Code, processing streaming
    output, and parsing transitions from the response.

    This executor implements the reminder prompt retry loop for handling:
    - Missing transition tags
    - Multiple transition tags
    - Unresolved transition targets
    - Policy violations
    """

    async def execute(
        self,
        agent: Dict[str, Any],
        state: Dict[str, Any],
        context: ExecutionContext
    ) -> ExecutionResult:
        """Execute a markdown state.

        Args:
            agent: Agent state dictionary
            state: Full workflow state dictionary
            context: Execution context

        Returns:
            ExecutionResult with transition, session_id, and cost

        Raises:
            ClaudeCodeError: If Claude Code execution fails
            ClaudeCodeLimitError: If Claude Code hits usage limit
            ClaudeCodeTimeoutWrappedError: If Claude Code times out
            PromptFileError: If prompt file operations fail
            ValueError: If transition parsing fails after max retries
        """
        scope_dir = state["scope_dir"]
        workflow_id = state.get("workflow_id", context.workflow_id)
        current_state = agent["current_state"]
        agent_id = agent.get("id", "unknown")
        session_id = agent.get("session_id")

        # Emit StateStarted event
        context.bus.emit(StateStarted(
            agent_id=agent_id,
            state_name=current_state,
            state_type="markdown",
        ))

        # Display state header via reporter
        if context.reporter:
            try:
                context.reporter.state_started(agent_id, current_state)
            except Exception as e:
                logger.warning(f"Failed to display state header: {e}")

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

        # Load prompt for current state
        try:
            prompt_template, policy = orchestrator.load_prompt(scope_dir, current_state)
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
        base_prompt = orchestrator.render_prompt(prompt_template, variables)

        # Determine which model to use
        model_to_use = None
        if policy and policy.model:
            model_to_use = policy.model
        elif context.default_model:
            model_to_use = context.default_model.lower() if isinstance(context.default_model, str) else context.default_model

        # Retry loop for reminder prompts
        transition = None
        new_session_id = session_id
        reminder_attempt = 0
        invocation_cost = 0.0
        start_time = time.perf_counter()

        while transition is None:
            # Show state header again on retry
            if reminder_attempt > 0:
                if context.reporter:
                    try:
                        context.reporter.state_started(agent_id, current_state)
                    except Exception as e:
                        logger.warning(f"Failed to display state header on retry: {e}")

            # Build prompt (base + reminder if this is a retry)
            prompt = base_prompt
            if reminder_attempt > 0:
                try:
                    reminder = orchestrator.generate_reminder_prompt(policy)
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
                    logger.error(
                        f"Failed to generate reminder prompt: {e}",
                        extra={
                            "workflow_id": workflow_id,
                            "agent_id": agent_id,
                            "current_state": current_state
                        }
                    )
                    raise

            # Emit ClaudeInvocationStarted event
            context.bus.emit(ClaudeInvocationStarted(
                agent_id=agent_id,
                state_name=current_state,
                session_id=new_session_id,
                is_fork=fork_session_id is not None and reminder_attempt == 0,
                is_reminder=reminder_attempt > 0,
                reminder_attempt=reminder_attempt,
            ))

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

            # Prepare debug file path for progressive writes
            debug_filepath = None
            step_number = None
            if context.debug_dir is not None:
                try:
                    step_number = context.get_next_step_number(agent_id)
                    state_name = extract_state_name(current_state)
                    filename = f"{agent_id}_{state_name}_{step_number:03d}.jsonl"
                    debug_filepath = context.debug_dir / filename
                except OSError as e:
                    logger.warning(f"Failed to prepare debug filepath: {e}")

            try:
                results = []

                # Determine which session to use
                if fork_session_id is not None and reminder_attempt == 0:
                    use_session_id = fork_session_id
                    use_fork = True
                else:
                    use_session_id = new_session_id
                    use_fork = False

                # Stream JSON objects from Claude Code
                async with aclosing(orchestrator.wrap_claude_code_stream(
                    prompt,
                    model=model_to_use,
                    session_id=use_session_id,
                    timeout=context.timeout,
                    dangerously_skip_permissions=context.dangerously_skip_permissions,
                    fork=use_fork
                )) as stream:
                    async for json_obj in stream:
                        results.append(json_obj)

                        # Extract session_id from JSON objects
                        if isinstance(json_obj, dict):
                            if "session_id" in json_obj:
                                new_session_id = json_obj["session_id"]
                            elif "metadata" in json_obj and isinstance(json_obj["metadata"], dict):
                                if "session_id" in json_obj["metadata"]:
                                    new_session_id = json_obj["metadata"]["session_id"]

                        # Check for limit error (non-retryable)
                        if isinstance(json_obj, dict):
                            if (json_obj.get("type") == "result" and
                                json_obj.get("is_error") is True and
                                isinstance(json_obj.get("result"), str) and
                                "hit your limit" in json_obj.get("result", "").lower()):
                                limit_message = json_obj.get("result", "Claude Code usage limit reached")
                                logger.error(
                                    f"Claude Code limit reached for agent {agent_id}: {limit_message}",
                                    extra={
                                        "workflow_id": workflow_id,
                                        "agent_id": agent_id,
                                        "current_state": current_state,
                                        "limit_message": limit_message
                                    }
                                )
                                self._save_error_response(
                                    workflow_id, agent_id,
                                    ClaudeCodeLimitError(limit_message),
                                    limit_message, results,
                                    new_session_id or session_id or fork_session_id,
                                    current_state, context.state_dir
                                )
                                raise ClaudeCodeLimitError(limit_message)

                        # Emit ClaudeStreamOutput event for debug
                        if step_number is not None:
                            context.bus.emit(ClaudeStreamOutput(
                                agent_id=agent_id,
                                state_name=current_state,
                                step_number=step_number,
                                json_object=json_obj,
                            ))

                        # Progressive write to debug file
                        if debug_filepath is not None:
                            self._append_claude_output_line(debug_filepath, json_obj)

                        # Process stream for console output
                        self._process_stream_for_console(
                            json_obj, agent_id, context
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

            except orchestrator.ClaudeCodeTimeoutError as e:
                logger.error(
                    f"Claude Code idle timeout for agent {agent_id}",
                    extra={
                        "workflow_id": workflow_id,
                        "agent_id": agent_id,
                        "current_state": current_state,
                        "error_message": str(e)
                    }
                )
                self._save_error_response(
                    workflow_id, agent_id, e,
                    "Claude Code idle timeout - partial output may be in debug file",
                    [],
                    new_session_id or session_id or fork_session_id,
                    current_state, context.state_dir
                )
                raise ClaudeCodeTimeoutWrappedError(str(e)) from e

            except RuntimeError as e:
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
                    self._save_error_response(
                        workflow_id, agent_id, e,
                        "Claude Code execution failed - no output received",
                        [],
                        new_session_id or session_id or fork_session_id,
                        current_state, context.state_dir
                    )
                    raise ClaudeCodeError(f"Claude Code execution failed: {e}") from e
                raise

            # Extract text output from results
            output_text = self._extract_output_text(results)

            # Extract and accumulate cost
            invocation_cost = extract_cost_from_results(results)
            total_cost = state.get("total_cost_usd", 0.0)
            if invocation_cost > 0:
                if "total_cost_usd" not in state:
                    state["total_cost_usd"] = 0.0
                state["total_cost_usd"] += invocation_cost
                total_cost = state["total_cost_usd"]

                logger.info(
                    f"Cost for agent {agent_id} invocation: ${invocation_cost:.4f}, "
                    f"Total cost: ${total_cost:.4f}",
                    extra={
                        "workflow_id": workflow_id,
                        "agent_id": agent_id,
                        "invocation_cost": invocation_cost,
                        "total_cost": total_cost
                    }
                )

            # Display state completion
            if context.reporter:
                try:
                    context.reporter.state_completed(agent_id, invocation_cost, total_cost)
                except Exception as e:
                    logger.warning(f"Failed to display state completion: {e}")

            # Check budget limit
            budget_usd = state.get("budget_usd", 10.0)

            if total_cost > budget_usd:
                if context.reporter:
                    try:
                        context.reporter.error(
                            agent_id,
                            f"BUDGET EXCEEDED (${total_cost:.4f} > ${budget_usd:.4f})"
                        )
                    except Exception as e:
                        logger.warning(f"Failed to display budget exceeded message: {e}")
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
                # Force termination with result transition
                from src.parsing import Transition
                transition = Transition(
                    tag="result",
                    target="",
                    attributes={},
                    payload=f"Workflow terminated: budget exceeded (${total_cost:.4f} > ${budget_usd:.4f})"
                )
                break

            # Parse and validate transitions
            transition = self._parse_and_validate_transitions(
                output_text, results, policy, scope_dir,
                workflow_id, agent_id, current_state, new_session_id,
                context, reminder_attempt
            )

            if transition is None:
                # Need to retry with reminder
                reminder_attempt += 1
                if reminder_attempt >= MAX_REMINDER_ATTEMPTS:
                    error = ValueError(
                        f"Expected exactly one transition, found 0 after {MAX_REMINDER_ATTEMPTS} reminder attempts"
                    )
                    self._save_error_response(
                        workflow_id, agent_id, error, output_text, results,
                        new_session_id, current_state, context.state_dir
                    )
                    error._error_saved = True
                    raise error
                continue

        # Calculate duration
        end_time = time.perf_counter()
        duration_ms = (end_time - start_time) * 1000

        # Emit StateCompleted event
        total_cost = state.get("total_cost_usd", 0.0)
        context.bus.emit(StateCompleted(
            agent_id=agent_id,
            state_name=current_state,
            cost_usd=invocation_cost,
            total_cost_usd=total_cost,
            session_id=new_session_id,
            duration_ms=duration_ms,
        ))

        return ExecutionResult(
            transition=transition,
            session_id=new_session_id,
            cost_usd=invocation_cost
        )

    def _extract_output_text(self, results: List[Dict[str, Any]]) -> str:
        """Extract text output from Claude Code results.

        Priority: result field > message.content > top-level text/content

        Args:
            results: List of JSON objects from Claude Code stream

        Returns:
            Concatenated text output
        """
        output_text = ""
        has_result_field = False

        # First pass: check for result field
        for result in results:
            if isinstance(result, dict) and "result" in result and isinstance(result["result"], str):
                output_text += result["result"]
                has_result_field = True

        if has_result_field:
            return output_text

        # Second pass: extract from other sources
        for result in results:
            if isinstance(result, dict):
                if "message" in result and isinstance(result["message"], dict):
                    content = result["message"].get("content", [])
                    if isinstance(content, list):
                        for item in content:
                            if isinstance(item, dict) and "text" in item:
                                output_text += item["text"]
                    elif isinstance(content, str):
                        output_text += content
                elif "text" in result:
                    output_text += result["text"]
                elif "content" in result:
                    if isinstance(result["content"], str):
                        output_text += result["content"]
                    elif isinstance(result["content"], list):
                        for item in result["content"]:
                            if isinstance(item, dict) and "text" in item:
                                output_text += item["text"]

        return output_text

    def _parse_and_validate_transitions(
        self,
        output_text: str,
        results: List[Dict[str, Any]],
        policy,
        scope_dir: str,
        workflow_id: str,
        agent_id: str,
        current_state: str,
        session_id: Optional[str],
        context: ExecutionContext,
        reminder_attempt: int
    ):
        """Parse and validate transitions from output.

        Returns transition if valid, None if retry needed.
        Raises on fatal errors.
        """
        transitions = orchestrator.parse_transitions(output_text)

        # Check for implicit transition
        if len(transitions) == 0 and orchestrator.can_use_implicit_transition(policy):
            transition = orchestrator.get_implicit_transition(policy)
            try:
                transition = resolve_transition_targets(transition, scope_dir)
            except (FileNotFoundError, ValueError) as e:
                self._save_error_response(
                    workflow_id, agent_id, e, output_text, results,
                    session_id, current_state, context.state_dir
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
            return transition

        # No tag and no implicit transition
        if len(transitions) == 0:
            if orchestrator.should_use_reminder_prompt(policy):
                # Check if max retries exceeded
                if reminder_attempt + 1 >= MAX_REMINDER_ATTEMPTS:
                    error = ValueError(
                        f"Expected exactly one transition, found 0 after {MAX_REMINDER_ATTEMPTS} reminder attempts"
                    )
                    self._save_error_response(
                        workflow_id, agent_id, error, output_text, results,
                        session_id, current_state, context.state_dir
                    )
                    error._error_saved = True
                    raise error

                if context.reporter:
                    try:
                        context.reporter.error(
                            agent_id,
                            f"No transition tag - retrying ({reminder_attempt + 1}/{MAX_REMINDER_ATTEMPTS})"
                        )
                    except Exception as e:
                        logger.warning(f"Failed to display error message: {e}")

                context.bus.emit(ErrorOccurred(
                    agent_id=agent_id,
                    error_type="NoTransitionTag",
                    error_message="No transition tag found in output",
                    current_state=current_state,
                    is_retryable=True,
                    retry_count=reminder_attempt + 1,
                    max_retries=MAX_REMINDER_ATTEMPTS,
                ))
                return None  # Signal retry needed
            else:
                error = ValueError("Expected exactly one transition, found 0")
                self._save_error_response(
                    workflow_id, agent_id, error, output_text, results,
                    session_id, current_state, context.state_dir
                )
                error._error_saved = True
                raise error

        # Validate exactly one transition
        try:
            orchestrator.validate_single_transition(transitions)
        except ValueError as e:
            if orchestrator.should_use_reminder_prompt(policy):
                # Check if max retries exceeded
                if reminder_attempt + 1 >= MAX_REMINDER_ATTEMPTS:
                    self._save_error_response(
                        workflow_id, agent_id, e, output_text, results,
                        session_id, current_state, context.state_dir
                    )
                    e._error_saved = True
                    raise

                if context.reporter:
                    try:
                        context.reporter.error(
                            agent_id,
                            f"{str(e)} - retrying ({reminder_attempt + 1}/{MAX_REMINDER_ATTEMPTS})"
                        )
                    except Exception as e2:
                        logger.warning(f"Failed to display error message: {e2}")

                context.bus.emit(ErrorOccurred(
                    agent_id=agent_id,
                    error_type="MultipleTransitions",
                    error_message=str(e),
                    current_state=current_state,
                    is_retryable=True,
                    retry_count=reminder_attempt + 1,
                    max_retries=MAX_REMINDER_ATTEMPTS,
                ))
                return None
            else:
                self._save_error_response(
                    workflow_id, agent_id, e, output_text, results,
                    session_id, current_state, context.state_dir
                )
                e._error_saved = True
                raise

        transition = transitions[0]

        # Resolve transition targets
        try:
            transition = resolve_transition_targets(transition, scope_dir)
        except (FileNotFoundError, ValueError) as e:
            if orchestrator.should_use_reminder_prompt(policy):
                # Check if max retries exceeded
                if reminder_attempt + 1 >= MAX_REMINDER_ATTEMPTS:
                    self._save_error_response(
                        workflow_id, agent_id, e, output_text, results,
                        session_id, current_state, context.state_dir
                    )
                    e._error_saved = True
                    raise

                if context.reporter:
                    try:
                        context.reporter.error(
                            agent_id,
                            f"Target resolution error: {str(e)} - retrying ({reminder_attempt + 1}/{MAX_REMINDER_ATTEMPTS})"
                        )
                    except Exception as e2:
                        logger.warning(f"Failed to display error message: {e2}")

                logger.warning(
                    f"Target resolution error for agent {agent_id} in state {current_state}: {e}. "
                    f"Re-prompting with reminder (attempt {reminder_attempt + 1})",
                    extra={
                        "workflow_id": workflow_id,
                        "agent_id": agent_id,
                        "current_state": current_state,
                        "transition_tag": transition.tag,
                        "transition_target": transition.target,
                        "reminder_attempt": reminder_attempt + 1
                    }
                )

                context.bus.emit(ErrorOccurred(
                    agent_id=agent_id,
                    error_type="TargetResolutionError",
                    error_message=str(e),
                    current_state=current_state,
                    is_retryable=True,
                    retry_count=reminder_attempt + 1,
                    max_retries=MAX_REMINDER_ATTEMPTS,
                ))
                return None
            else:
                self._save_error_response(
                    workflow_id, agent_id, e, output_text, results,
                    session_id, current_state, context.state_dir
                )
                e._error_saved = True
                raise

        # Validate against policy
        try:
            orchestrator.validate_transition_policy(transition, policy)
        except orchestrator.PolicyViolationError as e:
            if orchestrator.should_use_reminder_prompt(policy):
                # Check if max retries exceeded
                if reminder_attempt + 1 >= MAX_REMINDER_ATTEMPTS:
                    self._save_error_response(
                        workflow_id, agent_id, e, output_text, results,
                        session_id, current_state, context.state_dir
                    )
                    e._error_saved = True
                    raise

                if context.reporter:
                    try:
                        context.reporter.error(
                            agent_id,
                            f"Policy violation: {str(e)} - retrying ({reminder_attempt + 1}/{MAX_REMINDER_ATTEMPTS})"
                        )
                    except Exception as e2:
                        logger.warning(f"Failed to display error message: {e2}")

                logger.warning(
                    f"Policy violation for agent {agent_id} in state {current_state}: {e}. "
                    f"Re-prompting with reminder (attempt {reminder_attempt + 1})",
                    extra={
                        "workflow_id": workflow_id,
                        "agent_id": agent_id,
                        "current_state": current_state,
                        "transition_tag": transition.tag,
                        "transition_target": transition.target,
                        "reminder_attempt": reminder_attempt + 1
                    }
                )

                context.bus.emit(ErrorOccurred(
                    agent_id=agent_id,
                    error_type="PolicyViolation",
                    error_message=str(e),
                    current_state=current_state,
                    is_retryable=True,
                    retry_count=reminder_attempt + 1,
                    max_retries=MAX_REMINDER_ATTEMPTS,
                ))
                return None
            else:
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
                self._save_error_response(
                    workflow_id, agent_id, e, output_text, results,
                    session_id, current_state, context.state_dir
                )
                e._error_saved = True
                raise

        logger.debug(
            f"Parsed transition for agent {agent_id}: {transition.tag} -> {transition.target}",
            extra={
                "workflow_id": workflow_id,
                "agent_id": agent_id,
                "transition_tag": transition.tag,
                "transition_target": transition.target
            }
        )

        return transition

    def _process_stream_for_console(
        self,
        json_obj: Dict[str, Any],
        agent_id: str,
        context: ExecutionContext
    ) -> None:
        """Process stream object for console output.

        Extracts tool invocations and progress messages from stream
        and displays them via reporter.
        """
        if context.reporter is None:
            return

        try:
            if not isinstance(json_obj, dict):
                return

            # Assistant messages
            if json_obj.get("type") == "assistant" and "message" in json_obj:
                message = json_obj.get("message", {})
                content = message.get("content", [])
                if isinstance(content, list):
                    for item in content:
                        if isinstance(item, dict):
                            if item.get("type") == "text":
                                text = item.get("text", "")
                                if text:
                                    first_line = text.split('\n')[0]
                                    display_text = first_line[:80] + ("..." if len(first_line) > 80 else "")
                                    context.reporter.progress_message(agent_id, display_text)

                                    context.bus.emit(ProgressMessage(
                                        agent_id=agent_id,
                                        message=display_text,
                                    ))

                            elif item.get("type") == "tool_use":
                                tool_name = item.get("name", "unknown")
                                tool_input = item.get("input", {})
                                detail = None

                                if tool_name in ("Read", "Write", "Edit") and "file_path" in tool_input:
                                    detail = Path(tool_input["file_path"]).name
                                elif tool_name == "Bash" and "command" in tool_input:
                                    cmd = tool_input["command"]
                                    detail = cmd[:40] + ("..." if len(cmd) > 40 else "")

                                context.reporter.tool_invocation(agent_id, tool_name, detail)

                                context.bus.emit(ToolInvocation(
                                    agent_id=agent_id,
                                    tool_name=tool_name,
                                    detail=detail,
                                ))

            # Tool errors
            elif json_obj.get("type") == "user":
                message = json_obj.get("message", {})
                content = message.get("content", [])
                if isinstance(content, list):
                    for item in content:
                        if isinstance(item, dict) and item.get("type") == "tool_result" and item.get("is_error"):
                            error_msg = item.get("content", "Tool error")
                            if "<tool_use_error>" in error_msg:
                                error_msg = error_msg.split("<tool_use_error>")[1].split("</tool_use_error>")[0]
                            context.reporter.tool_error(agent_id, error_msg)

        except Exception as e:
            logger.warning(f"Failed to process console output: {e}")

    def _save_error_response(
        self,
        workflow_id: str,
        agent_id: str,
        error: Exception,
        output_text: str,
        raw_results: List[Any],
        session_id: Optional[str],
        current_state: str,
        state_dir: Optional[str]
    ) -> None:
        """Save error response to file."""
        try:
            orchestrator.save_error_response(
                workflow_id=workflow_id,
                agent_id=agent_id,
                error=error,
                output_text=output_text,
                raw_results=raw_results,
                session_id=session_id,
                current_state=current_state,
                state_dir=state_dir
            )
        except Exception as e:
            logger.warning(f"Failed to save error response: {e}")

    def _append_claude_output_line(
        self,
        filepath: Path,
        json_object: Dict[str, Any]
    ) -> None:
        """Append a single JSON object to a JSONL debug file."""
        try:
            with open(filepath, 'a', encoding='utf-8') as f:
                f.write(json.dumps(json_object, ensure_ascii=False) + '\n')
        except OSError as e:
            logger.warning(f"Failed to append Claude output to {filepath}: {e}")
