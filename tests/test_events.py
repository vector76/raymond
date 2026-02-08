"""Tests for orchestrator event dataclasses.

These tests verify that events are properly constructable, frozen (immutable),
and contain the expected fields.
"""

import pytest
from datetime import datetime
from pathlib import Path

from src.orchestrator.events import (
    WorkflowStarted,
    WorkflowCompleted,
    WorkflowPaused,
    WorkflowWaiting,
    WorkflowResuming,
    StateStarted,
    StateCompleted,
    TransitionOccurred,
    AgentSpawned,
    AgentTerminated,
    AgentPaused,
    ClaudeStreamOutput,
    ClaudeInvocationStarted,
    ScriptOutput,
    ToolInvocation,
    ProgressMessage,
    ErrorOccurred,
    ALL_EVENTS,
)


class TestWorkflowEvents:
    """Tests for workflow-level events."""

    def test_workflow_started_construction(self):
        """WorkflowStarted can be constructed with required fields."""
        event = WorkflowStarted(
            workflow_id="test-001",
            scope_dir="/path/to/scope",
            debug_dir=Path("/path/to/debug"),
        )
        assert event.workflow_id == "test-001"
        assert event.scope_dir == "/path/to/scope"
        assert event.debug_dir == Path("/path/to/debug")
        assert isinstance(event.timestamp, datetime)

    def test_workflow_started_debug_dir_none(self):
        """WorkflowStarted accepts None for debug_dir."""
        event = WorkflowStarted(
            workflow_id="test-001",
            scope_dir="/path/to/scope",
            debug_dir=None,
        )
        assert event.debug_dir is None

    def test_workflow_started_is_frozen(self):
        """WorkflowStarted is immutable."""
        event = WorkflowStarted(
            workflow_id="test-001",
            scope_dir="/path/to/scope",
            debug_dir=None,
        )
        with pytest.raises(AttributeError):
            event.workflow_id = "modified"

    def test_workflow_completed_construction(self):
        """WorkflowCompleted can be constructed with required fields."""
        event = WorkflowCompleted(
            workflow_id="test-001",
            total_cost_usd=1.23,
        )
        assert event.workflow_id == "test-001"
        assert event.total_cost_usd == 1.23
        assert isinstance(event.timestamp, datetime)

    def test_workflow_completed_is_frozen(self):
        """WorkflowCompleted is immutable."""
        event = WorkflowCompleted(
            workflow_id="test-001",
            total_cost_usd=1.23,
        )
        with pytest.raises(AttributeError):
            event.total_cost_usd = 9.99

    def test_workflow_paused_construction(self):
        """WorkflowPaused can be constructed with required fields."""
        event = WorkflowPaused(
            workflow_id="test-001",
            total_cost_usd=0.50,
            paused_agent_count=2,
        )
        assert event.workflow_id == "test-001"
        assert event.total_cost_usd == 0.50
        assert event.paused_agent_count == 2
        assert isinstance(event.timestamp, datetime)


class TestStateEvents:
    """Tests for state execution events."""

    def test_state_started_construction(self):
        """StateStarted can be constructed with required fields."""
        event = StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        )
        assert event.agent_id == "main"
        assert event.state_name == "START.md"
        assert event.state_type == "markdown"
        assert isinstance(event.timestamp, datetime)

    def test_state_started_script_type(self):
        """StateStarted accepts script state type."""
        event = StateStarted(
            agent_id="main",
            state_name="CHECK.sh",
            state_type="script",
        )
        assert event.state_type == "script"

    def test_state_started_is_frozen(self):
        """StateStarted is immutable."""
        event = StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        )
        with pytest.raises(AttributeError):
            event.agent_id = "modified"

    def test_state_completed_construction(self):
        """StateCompleted can be constructed with required fields."""
        event = StateCompleted(
            agent_id="main",
            state_name="START.md",
            cost_usd=0.05,
            total_cost_usd=0.15,
            session_id="sess-123",
            duration_ms=1500.5,
        )
        assert event.agent_id == "main"
        assert event.state_name == "START.md"
        assert event.cost_usd == 0.05
        assert event.total_cost_usd == 0.15
        assert event.session_id == "sess-123"
        assert event.duration_ms == 1500.5

    def test_state_completed_session_id_none(self):
        """StateCompleted accepts None for session_id (scripts)."""
        event = StateCompleted(
            agent_id="main",
            state_name="CHECK.sh",
            cost_usd=0.0,
            total_cost_usd=0.10,
            session_id=None,
            duration_ms=50.0,
        )
        assert event.session_id is None


class TestTransitionEvents:
    """Tests for transition-related events."""

    def test_transition_occurred_construction(self):
        """TransitionOccurred can be constructed with required fields."""
        event = TransitionOccurred(
            agent_id="main",
            from_state="START.md",
            to_state="NEXT.md",
            transition_type="goto",
        )
        assert event.agent_id == "main"
        assert event.from_state == "START.md"
        assert event.to_state == "NEXT.md"
        assert event.transition_type == "goto"
        assert event.metadata == {}

    def test_transition_occurred_with_metadata(self):
        """TransitionOccurred accepts metadata dict."""
        event = TransitionOccurred(
            agent_id="main",
            from_state="START.md",
            to_state="FORK.md",
            transition_type="fork",
            metadata={"spawned_agent_id": "fork-1"},
        )
        assert event.metadata == {"spawned_agent_id": "fork-1"}

    def test_transition_occurred_to_state_none(self):
        """TransitionOccurred accepts None for to_state (termination)."""
        event = TransitionOccurred(
            agent_id="main",
            from_state="END.md",
            to_state=None,
            transition_type="result",
        )
        assert event.to_state is None

    def test_agent_spawned_construction(self):
        """AgentSpawned can be constructed with required fields."""
        event = AgentSpawned(
            parent_agent_id="main",
            new_agent_id="fork-1",
            initial_state="PROCESS.md",
        )
        assert event.parent_agent_id == "main"
        assert event.new_agent_id == "fork-1"
        assert event.initial_state == "PROCESS.md"

    def test_agent_terminated_construction(self):
        """AgentTerminated can be constructed with required fields."""
        event = AgentTerminated(
            agent_id="main",
            result_payload="Task completed successfully",
        )
        assert event.agent_id == "main"
        assert event.result_payload == "Task completed successfully"

    def test_agent_paused_construction(self):
        """AgentPaused can be constructed with required fields."""
        event = AgentPaused(
            agent_id="main",
            reason="timeout",
        )
        assert event.agent_id == "main"
        assert event.reason == "timeout"


class TestClaudeStreamEvents:
    """Tests for Claude Code stream events."""

    def test_claude_stream_output_construction(self):
        """ClaudeStreamOutput can be constructed with required fields."""
        json_obj = {"type": "content", "text": "Hello"}
        event = ClaudeStreamOutput(
            agent_id="main",
            state_name="START.md",
            step_number=1,
            json_object=json_obj,
        )
        assert event.agent_id == "main"
        assert event.state_name == "START.md"
        assert event.step_number == 1
        assert event.json_object == json_obj

    def test_claude_invocation_started_construction(self):
        """ClaudeInvocationStarted can be constructed with required fields."""
        event = ClaudeInvocationStarted(
            agent_id="main",
            state_name="START.md",
            session_id=None,
            is_fork=False,
            is_reminder=False,
            reminder_attempt=0,
        )
        assert event.agent_id == "main"
        assert event.state_name == "START.md"
        assert event.session_id is None
        assert event.is_fork is False
        assert event.is_reminder is False
        assert event.reminder_attempt == 0

    def test_claude_invocation_started_reminder(self):
        """ClaudeInvocationStarted handles reminder prompts."""
        event = ClaudeInvocationStarted(
            agent_id="main",
            state_name="START.md",
            session_id="sess-123",
            is_fork=False,
            is_reminder=True,
            reminder_attempt=2,
        )
        assert event.is_reminder is True
        assert event.reminder_attempt == 2
        assert event.session_id == "sess-123"


class TestScriptEvents:
    """Tests for script execution events."""

    def test_script_output_construction(self):
        """ScriptOutput can be constructed with required fields."""
        event = ScriptOutput(
            agent_id="main",
            state_name="CHECK.sh",
            step_number=3,
            stdout="<goto>NEXT.md</goto>\n",
            stderr="",
            exit_code=0,
            execution_time_ms=125.5,
            env_vars={"WORKFLOW_ID": "test-001"},
        )
        assert event.agent_id == "main"
        assert event.state_name == "CHECK.sh"
        assert event.step_number == 3
        assert event.stdout == "<goto>NEXT.md</goto>\n"
        assert event.stderr == ""
        assert event.exit_code == 0
        assert event.execution_time_ms == 125.5
        assert event.env_vars == {"WORKFLOW_ID": "test-001"}

    def test_script_output_with_errors(self):
        """ScriptOutput captures error output."""
        event = ScriptOutput(
            agent_id="main",
            state_name="CHECK.sh",
            step_number=3,
            stdout="",
            stderr="Error: file not found",
            exit_code=1,
            execution_time_ms=50.0,
            env_vars={},
        )
        assert event.exit_code == 1
        assert event.stderr == "Error: file not found"


class TestToolEvents:
    """Tests for tool invocation events."""

    def test_tool_invocation_construction(self):
        """ToolInvocation can be constructed with required fields."""
        event = ToolInvocation(
            agent_id="main",
            tool_name="Read",
            detail="src/main.py",
        )
        assert event.agent_id == "main"
        assert event.tool_name == "Read"
        assert event.detail == "src/main.py"

    def test_tool_invocation_no_detail(self):
        """ToolInvocation accepts None for detail."""
        event = ToolInvocation(
            agent_id="main",
            tool_name="Bash",
        )
        assert event.detail is None

    def test_progress_message_construction(self):
        """ProgressMessage can be constructed with required fields."""
        event = ProgressMessage(
            agent_id="main",
            message="Processing data...",
        )
        assert event.agent_id == "main"
        assert event.message == "Processing data..."


class TestErrorEvents:
    """Tests for error events."""

    def test_error_occurred_construction(self):
        """ErrorOccurred can be constructed with required fields."""
        event = ErrorOccurred(
            agent_id="main",
            error_type="ClaudeCodeError",
            error_message="Connection failed",
            current_state="START.md",
            is_retryable=True,
            retry_count=1,
            max_retries=3,
        )
        assert event.agent_id == "main"
        assert event.error_type == "ClaudeCodeError"
        assert event.error_message == "Connection failed"
        assert event.current_state == "START.md"
        assert event.is_retryable is True
        assert event.retry_count == 1
        assert event.max_retries == 3

    def test_error_occurred_not_retryable(self):
        """ErrorOccurred handles non-retryable errors."""
        event = ErrorOccurred(
            agent_id="main",
            error_type="ClaudeCodeLimitError",
            error_message="Usage limit exceeded",
            current_state="START.md",
            is_retryable=False,
            retry_count=0,
            max_retries=3,
        )
        assert event.is_retryable is False

    def test_error_occurred_current_state_none(self):
        """ErrorOccurred accepts None for current_state."""
        event = ErrorOccurred(
            agent_id="workflow",
            error_type="StateFileError",
            error_message="State file corrupted",
            current_state=None,
            is_retryable=False,
            retry_count=0,
            max_retries=3,
        )
        assert event.current_state is None


class TestAllEvents:
    """Tests for ALL_EVENTS tuple."""

    def test_all_events_contains_expected_types(self):
        """ALL_EVENTS contains all defined event classes."""
        expected = {
            WorkflowStarted,
            WorkflowCompleted,
            WorkflowPaused,
            WorkflowWaiting,
            WorkflowResuming,
            StateStarted,
            StateCompleted,
            TransitionOccurred,
            AgentSpawned,
            AgentTerminated,
            AgentPaused,
            ClaudeStreamOutput,
            ClaudeInvocationStarted,
            ScriptOutput,
            ToolInvocation,
            ProgressMessage,
            ErrorOccurred,
        }
        assert set(ALL_EVENTS) == expected

    def test_all_events_are_dataclasses(self):
        """All events in ALL_EVENTS are frozen dataclasses."""
        from dataclasses import is_dataclass
        for event_class in ALL_EVENTS:
            assert is_dataclass(event_class), f"{event_class.__name__} is not a dataclass"

    def test_all_events_have_timestamp(self):
        """All events have a timestamp field with default value."""
        for event_class in ALL_EVENTS:
            fields = event_class.__dataclass_fields__
            assert "timestamp" in fields, f"{event_class.__name__} missing timestamp field"
