"""Console observer for orchestrator event bus.

This module provides the ConsoleObserver class that bridges orchestration events
to the existing ConsoleReporter implementation. It acts as a thin adapter layer
with no business logic, simply mapping events to the appropriate reporter methods.
"""

import logging

from ..bus import EventBus
from ..events import (
    AgentPaused,
    AgentSpawned,
    AgentTerminated,
    ErrorOccurred,
    ProgressMessage,
    ScriptOutput,
    StateCompleted,
    StateStarted,
    ToolInvocation,
    TransitionOccurred,
    WorkflowCompleted,
    WorkflowPaused,
    WorkflowStarted,
)
from ...console import ConsoleReporter

logger = logging.getLogger(__name__)


class ConsoleObserver:
    """Observer that bridges events to the ConsoleReporter.

    Subscribes to relevant events from the EventBus and calls the corresponding
    methods on the ConsoleReporter. All method calls are wrapped in try/except
    to ensure reporter failures don't propagate to the orchestration loop.

    This is a thin adapter layer - it contains no business logic, only event-to-method
    mapping.

    Attributes:
        reporter: The ConsoleReporter instance to send output to
    """

    def __init__(self, reporter: ConsoleReporter, bus: EventBus) -> None:
        """Initialize the console observer.

        Args:
            reporter: ConsoleReporter instance for output
            bus: Event bus to subscribe to
        """
        self.reporter = reporter
        self._bus = bus

        # Subscribe to events
        self._subscribe()

    def _subscribe(self) -> None:
        """Subscribe to relevant events on the bus."""
        self._bus.on(WorkflowStarted, self._on_workflow_started)
        self._bus.on(WorkflowCompleted, self._on_workflow_completed)
        self._bus.on(WorkflowPaused, self._on_workflow_paused)
        self._bus.on(StateStarted, self._on_state_started)
        self._bus.on(StateCompleted, self._on_state_completed)
        self._bus.on(ScriptOutput, self._on_script_output)
        self._bus.on(TransitionOccurred, self._on_transition_occurred)
        self._bus.on(ToolInvocation, self._on_tool_invocation)
        self._bus.on(ProgressMessage, self._on_progress_message)
        self._bus.on(ErrorOccurred, self._on_error_occurred)
        self._bus.on(AgentTerminated, self._on_agent_terminated)
        self._bus.on(AgentPaused, self._on_agent_paused)
        self._bus.on(AgentSpawned, self._on_agent_spawned)

    def _unsubscribe(self) -> None:
        """Unsubscribe from all events on the bus."""
        self._bus.off(WorkflowStarted, self._on_workflow_started)
        self._bus.off(WorkflowCompleted, self._on_workflow_completed)
        self._bus.off(WorkflowPaused, self._on_workflow_paused)
        self._bus.off(StateStarted, self._on_state_started)
        self._bus.off(StateCompleted, self._on_state_completed)
        self._bus.off(ScriptOutput, self._on_script_output)
        self._bus.off(TransitionOccurred, self._on_transition_occurred)
        self._bus.off(ToolInvocation, self._on_tool_invocation)
        self._bus.off(ProgressMessage, self._on_progress_message)
        self._bus.off(ErrorOccurred, self._on_error_occurred)
        self._bus.off(AgentTerminated, self._on_agent_terminated)
        self._bus.off(AgentPaused, self._on_agent_paused)
        self._bus.off(AgentSpawned, self._on_agent_spawned)

    def close(self) -> None:
        """Clean up resources - unsubscribe from events.

        Should be called when the workflow completes.
        """
        self._unsubscribe()

    def _on_workflow_started(self, event: WorkflowStarted) -> None:
        """Handle WorkflowStarted event."""
        try:
            self.reporter.workflow_started(
                workflow_id=event.workflow_id,
                scope_dir=event.scope_dir,
                debug_dir=event.debug_dir
            )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on workflow_started: {e}")

    def _on_workflow_completed(self, event: WorkflowCompleted) -> None:
        """Handle WorkflowCompleted event."""
        try:
            self.reporter.workflow_completed(total_cost=event.total_cost_usd)
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on workflow_completed: {e}")

    def _on_workflow_paused(self, event: WorkflowPaused) -> None:
        """Handle WorkflowPaused event."""
        try:
            self.reporter.workflow_paused(
                workflow_id=event.workflow_id,
                total_cost=event.total_cost_usd,
                paused_count=event.paused_agent_count
            )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on workflow_paused: {e}")

    def _on_state_started(self, event: StateStarted) -> None:
        """Handle StateStarted event."""
        try:
            if event.state_type == "script":
                self.reporter.script_started(
                    agent_id=event.agent_id,
                    state=event.state_name
                )
            else:
                self.reporter.state_started(
                    agent_id=event.agent_id,
                    state=event.state_name
                )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on state_started: {e}")

    def _on_state_completed(self, event: StateCompleted) -> None:
        """Handle StateCompleted event."""
        try:
            self.reporter.state_completed(
                agent_id=event.agent_id,
                cost=event.cost_usd,
                total_cost=event.total_cost_usd
            )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on state_completed: {e}")

    def _on_script_output(self, event: ScriptOutput) -> None:
        """Handle ScriptOutput event."""
        try:
            self.reporter.script_completed(
                agent_id=event.agent_id,
                exit_code=event.exit_code,
                duration_ms=event.execution_time_ms
            )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on script_output: {e}")

    def _on_transition_occurred(self, event: TransitionOccurred) -> None:
        """Handle TransitionOccurred event."""
        try:
            # Extract spawned agent ID for fork transitions
            spawned_agent_id = event.metadata.get("spawned_agent_id")

            # Skip result transitions to terminated state (handled by AgentTerminated)
            if event.transition_type == "result" and event.to_state is None:
                return

            self.reporter.transition(
                agent_id=event.agent_id,
                target=event.to_state or "(terminated)",
                transition_type=event.transition_type,
                spawned_agent_id=spawned_agent_id
            )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on transition_occurred: {e}")

    def _on_tool_invocation(self, event: ToolInvocation) -> None:
        """Handle ToolInvocation event."""
        try:
            self.reporter.tool_invocation(
                agent_id=event.agent_id,
                tool_name=event.tool_name,
                detail=event.detail
            )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on tool_invocation: {e}")

    def _on_progress_message(self, event: ProgressMessage) -> None:
        """Handle ProgressMessage event."""
        try:
            self.reporter.progress_message(
                agent_id=event.agent_id,
                message=event.message
            )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on progress_message: {e}")

    def _on_error_occurred(self, event: ErrorOccurred) -> None:
        """Handle ErrorOccurred event."""
        try:
            # Build error message with retry info if retryable
            message = event.error_message
            if event.is_retryable:
                message = f"{message} (retry {event.retry_count}/{event.max_retries})"

            self.reporter.error(
                agent_id=event.agent_id,
                message=message
            )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on error_occurred: {e}")

    def _on_agent_terminated(self, event: AgentTerminated) -> None:
        """Handle AgentTerminated event."""
        try:
            self.reporter.agent_terminated(
                agent_id=event.agent_id,
                result=event.result_payload
            )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on agent_terminated: {e}")

    def _on_agent_paused(self, event: AgentPaused) -> None:
        """Handle AgentPaused event."""
        try:
            self.reporter.agent_paused(
                agent_id=event.agent_id,
                reason=event.reason
            )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on agent_paused: {e}")

    def _on_agent_spawned(self, event: AgentSpawned) -> None:
        """Handle AgentSpawned event."""
        try:
            self.reporter.agent_spawned(
                parent_id=event.parent_agent_id,
                child_id=event.new_agent_id,
                target_state=event.initial_state
            )
        except Exception as e:
            logger.warning(f"ConsoleObserver failed on agent_spawned: {e}")
