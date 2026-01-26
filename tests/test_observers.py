"""Tests for orchestrator observers.

These tests verify the observer implementations that handle side effects
(debug file writing, console output) based on orchestration events.
"""

import json
import logging
import pytest
from datetime import datetime
from pathlib import Path
from unittest.mock import MagicMock, call, patch

from src.orchestrator.bus import EventBus
from src.orchestrator.events import (
    AgentPaused,
    AgentSpawned,
    AgentTerminated,
    ClaudeStreamOutput,
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
from src.orchestrator.observers.debug import DebugObserver
from src.orchestrator.observers.console import ConsoleObserver


class TestDebugObserverStateTracking:
    """Tests for DebugObserver state name tracking.

    Note: State name extraction logic is tested in test_executors.py via
    extract_state_name(). These tests focus on the observer's state tracking
    behavior.
    """

    def test_state_started_tracks_state(self, tmp_path):
        """StateStarted event updates agent state tracking."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown"
        ))

        assert observer._agent_states["main"] == "START"

        observer.close()


class TestDebugObserverClaudeOutput:
    """Tests for Claude Code JSONL output writing."""

    def test_claude_stream_output_writes_jsonl(self, tmp_path):
        """ClaudeStreamOutput event appends to JSONL file."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        json_obj = {"type": "assistant", "message": {"content": "Hello"}}

        bus.emit(ClaudeStreamOutput(
            agent_id="main",
            state_name="START.md",
            step_number=1,
            json_object=json_obj
        ))

        # Flush and check file
        observer.close()

        filepath = tmp_path / "main_START_001.jsonl"
        assert filepath.exists()

        with open(filepath, 'r') as f:
            line = f.readline()
            parsed = json.loads(line)
            assert parsed == json_obj

    def test_claude_stream_output_multiple_objects(self, tmp_path):
        """Multiple ClaudeStreamOutput events append to same JSONL file."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        objects = [
            {"type": "system", "message": "start"},
            {"type": "assistant", "message": "thinking"},
            {"type": "assistant", "message": "done"},
        ]

        for obj in objects:
            bus.emit(ClaudeStreamOutput(
                agent_id="main",
                state_name="PROCESS.md",
                step_number=2,
                json_object=obj
            ))

        observer.close()

        filepath = tmp_path / "main_PROCESS_002.jsonl"
        with open(filepath, 'r') as f:
            lines = f.readlines()
            assert len(lines) == 3
            for i, line in enumerate(lines):
                assert json.loads(line) == objects[i]

    def test_claude_stream_output_multiple_agents(self, tmp_path):
        """JSONL files are separate per agent."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(ClaudeStreamOutput(
            agent_id="agent1",
            state_name="START.md",
            step_number=1,
            json_object={"agent": 1}
        ))
        bus.emit(ClaudeStreamOutput(
            agent_id="agent2",
            state_name="START.md",
            step_number=1,
            json_object={"agent": 2}
        ))

        observer.close()

        file1 = tmp_path / "agent1_START_001.jsonl"
        file2 = tmp_path / "agent2_START_001.jsonl"

        assert file1.exists()
        assert file2.exists()

        with open(file1, 'r') as f:
            assert json.loads(f.readline())["agent"] == 1
        with open(file2, 'r') as f:
            assert json.loads(f.readline())["agent"] == 2

    def test_state_completed_closes_file(self, tmp_path):
        """StateCompleted closes the JSONL file handle."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(ClaudeStreamOutput(
            agent_id="main",
            state_name="START.md",
            step_number=1,
            json_object={"test": True}
        ))

        assert "main" in observer._open_files

        bus.emit(StateCompleted(
            agent_id="main",
            state_name="START.md",
            cost_usd=0.05,
            total_cost_usd=0.05,
            session_id="sess-123",
            duration_ms=1000
        ))

        assert "main" not in observer._open_files

        observer.close()


class TestDebugObserverScriptOutput:
    """Tests for script output file writing."""

    def test_script_output_writes_stdout(self, tmp_path):
        """ScriptOutput event writes stdout file."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(ScriptOutput(
            agent_id="main",
            state_name="CHECK.sh",
            step_number=3,
            stdout="Output from script",
            stderr="",
            exit_code=0,
            execution_time_ms=150.5,
            env_vars={"VAR": "value"}
        ))

        observer.close()

        filepath = tmp_path / "main_CHECK_003.stdout.txt"
        assert filepath.exists()
        assert filepath.read_text() == "Output from script"

    def test_script_output_writes_stderr(self, tmp_path):
        """ScriptOutput event writes stderr file."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(ScriptOutput(
            agent_id="main",
            state_name="CHECK.sh",
            step_number=3,
            stdout="",
            stderr="Error message",
            exit_code=1,
            execution_time_ms=100.0,
            env_vars={}
        ))

        observer.close()

        filepath = tmp_path / "main_CHECK_003.stderr.txt"
        assert filepath.exists()
        assert filepath.read_text() == "Error message"

    def test_script_output_writes_metadata(self, tmp_path):
        """ScriptOutput event writes metadata JSON file."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(ScriptOutput(
            agent_id="main",
            state_name="RUN.bat",
            step_number=5,
            stdout="success",
            stderr="",
            exit_code=0,
            execution_time_ms=250.75,
            env_vars={"PATH": "/bin", "HOME": "/home/user"}
        ))

        observer.close()

        filepath = tmp_path / "main_RUN_005.meta.json"
        assert filepath.exists()

        with open(filepath, 'r') as f:
            metadata = json.load(f)

        assert metadata["exit_code"] == 0
        assert metadata["execution_time_ms"] == 250.75
        assert metadata["env_vars"]["PATH"] == "/bin"
        assert metadata["env_vars"]["HOME"] == "/home/user"

    def test_script_output_writes_all_files(self, tmp_path):
        """ScriptOutput creates stdout, stderr, and metadata files."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(ScriptOutput(
            agent_id="worker",
            state_name="VALIDATE.sh",
            step_number=10,
            stdout="ok",
            stderr="warn",
            exit_code=0,
            execution_time_ms=50.0,
            env_vars={"TEST": "1"}
        ))

        observer.close()

        assert (tmp_path / "worker_VALIDATE_010.stdout.txt").exists()
        assert (tmp_path / "worker_VALIDATE_010.stderr.txt").exists()
        assert (tmp_path / "worker_VALIDATE_010.meta.json").exists()


class TestDebugObserverTransitionLog:
    """Tests for transition log writing."""

    def test_transition_occurred_writes_log(self, tmp_path):
        """TransitionOccurred event appends to transitions.log."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        timestamp = datetime(2025, 1, 15, 10, 30, 0)
        bus.emit(TransitionOccurred(
            agent_id="main",
            from_state="START.md",
            to_state="PROCESS.md",
            transition_type="goto",
            metadata={"session_id": "sess-123"},
            timestamp=timestamp
        ))

        observer.close()

        logfile = tmp_path / "transitions.log"
        assert logfile.exists()

        content = logfile.read_text()
        assert "[main]" in content
        assert "START.md -> PROCESS.md" in content
        assert "(goto)" in content
        assert "session_id: sess-123" in content

    def test_transition_occurred_terminated(self, tmp_path):
        """TransitionOccurred with to_state=None shows terminated."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(TransitionOccurred(
            agent_id="main",
            from_state="FINAL.md",
            to_state=None,
            transition_type="result",
            metadata={}
        ))

        observer.close()

        logfile = tmp_path / "transitions.log"
        content = logfile.read_text()
        assert "FINAL.md -> (result, terminated)" in content

    def test_transition_occurred_multiple_entries(self, tmp_path):
        """Multiple transitions append to same log file."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(TransitionOccurred(
            agent_id="main",
            from_state="START.md",
            to_state="MID.md",
            transition_type="goto",
            metadata={}
        ))
        bus.emit(TransitionOccurred(
            agent_id="main",
            from_state="MID.md",
            to_state="END.md",
            transition_type="call",
            metadata={"stack_depth": 1}
        ))

        observer.close()

        logfile = tmp_path / "transitions.log"
        content = logfile.read_text()
        assert "START.md -> MID.md" in content
        assert "MID.md -> END.md" in content
        assert "stack_depth: 1" in content

    def test_transition_occurred_with_metadata(self, tmp_path):
        """TransitionOccurred includes all metadata in log."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(TransitionOccurred(
            agent_id="worker",
            from_state="A.md",
            to_state="B.md",
            transition_type="fork",
            metadata={
                "spawned_agent_id": "worker_001",
                "cost_usd": 0.05,
                "session_id": "sess-456"
            }
        ))

        observer.close()

        logfile = tmp_path / "transitions.log"
        content = logfile.read_text()
        assert "spawned_agent_id: worker_001" in content
        assert "cost_usd: 0.05" in content
        assert "session_id: sess-456" in content


class TestDebugObserverErrorHandling:
    """Tests for error resilience in DebugObserver."""

    def test_file_write_error_is_caught(self, tmp_path, caplog):
        """File write errors are logged but don't propagate."""
        bus = EventBus()

        # Use a non-existent directory to trigger write error
        bad_dir = tmp_path / "nonexistent" / "deeply" / "nested"
        observer = DebugObserver(bad_dir, bus)

        # Should not raise
        with caplog.at_level(logging.WARNING):
            bus.emit(ScriptOutput(
                agent_id="main",
                state_name="TEST.sh",
                step_number=1,
                stdout="test",
                stderr="",
                exit_code=0,
                execution_time_ms=10.0,
                env_vars={}
            ))

        # Warning should be logged
        assert "Failed to save" in caplog.text

        observer.close()

    def test_close_handles_already_closed_file(self, tmp_path):
        """Closing observer handles already-closed file handles gracefully."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(ClaudeStreamOutput(
            agent_id="main",
            state_name="START.md",
            step_number=1,
            json_object={"test": True}
        ))

        # Manually close the file handle
        observer._open_files["main"].close()

        # Should not raise when close() tries to close again
        observer.close()


class TestDebugObserverCleanup:
    """Tests for observer cleanup."""

    def test_close_unsubscribes_from_events(self, tmp_path):
        """close() unsubscribes from all events."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        assert bus.has_handlers(StateStarted)
        assert bus.has_handlers(ClaudeStreamOutput)
        assert bus.has_handlers(ScriptOutput)
        assert bus.has_handlers(TransitionOccurred)

        observer.close()

        assert not bus.has_handlers(StateStarted)
        assert not bus.has_handlers(ClaudeStreamOutput)
        assert not bus.has_handlers(ScriptOutput)
        assert not bus.has_handlers(TransitionOccurred)

    def test_close_clears_state(self, tmp_path):
        """close() clears internal state dictionaries."""
        bus = EventBus()
        observer = DebugObserver(tmp_path, bus)

        bus.emit(StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown"
        ))
        bus.emit(ClaudeStreamOutput(
            agent_id="main",
            state_name="START.md",
            step_number=1,
            json_object={"test": True}
        ))

        assert len(observer._agent_states) > 0
        assert len(observer._open_files) > 0

        observer.close()

        assert len(observer._agent_states) == 0
        assert len(observer._open_files) == 0


class TestConsoleObserverBasic:
    """Basic tests for ConsoleObserver event handling."""

    def test_workflow_started(self):
        """WorkflowStarted event calls reporter.workflow_started()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        debug_dir = Path("/tmp/debug")
        bus.emit(WorkflowStarted(
            workflow_id="wf-123",
            scope_dir="/project",
            debug_dir=debug_dir
        ))

        reporter.workflow_started.assert_called_once_with(
            workflow_id="wf-123",
            scope_dir="/project",
            debug_dir=debug_dir
        )

        observer.close()

    def test_workflow_completed(self):
        """WorkflowCompleted event calls reporter.workflow_completed()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(WorkflowCompleted(
            workflow_id="wf-123",
            total_cost_usd=1.25
        ))

        reporter.workflow_completed.assert_called_once_with(total_cost=1.25)

        observer.close()

    def test_workflow_paused(self):
        """WorkflowPaused event calls reporter.workflow_paused()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(WorkflowPaused(
            workflow_id="wf-123",
            total_cost_usd=0.50,
            paused_agent_count=2
        ))

        reporter.workflow_paused.assert_called_once_with(
            workflow_id="wf-123",
            total_cost=0.50,
            paused_count=2
        )

        observer.close()


class TestConsoleObserverStateEvents:
    """Tests for state-related event handling."""

    def test_state_started_markdown(self):
        """StateStarted with markdown type calls reporter.state_started()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown"
        ))

        reporter.state_started.assert_called_once_with(
            agent_id="main",
            state="START.md"
        )
        reporter.script_started.assert_not_called()

        observer.close()

    def test_state_started_script(self):
        """StateStarted with script type calls reporter.script_started()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(StateStarted(
            agent_id="main",
            state_name="CHECK.sh",
            state_type="script"
        ))

        reporter.script_started.assert_called_once_with(
            agent_id="main",
            state="CHECK.sh"
        )
        reporter.state_started.assert_not_called()

        observer.close()

    def test_state_completed(self):
        """StateCompleted event calls reporter.state_completed()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(StateCompleted(
            agent_id="main",
            state_name="START.md",
            cost_usd=0.05,
            total_cost_usd=0.10,
            session_id="sess-123",
            duration_ms=1500
        ))

        reporter.state_completed.assert_called_once_with(
            agent_id="main",
            cost=0.05,
            total_cost=0.10
        )

        observer.close()

    def test_script_output(self):
        """ScriptOutput event calls reporter.script_completed()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(ScriptOutput(
            agent_id="main",
            state_name="CHECK.sh",
            step_number=1,
            stdout="ok",
            stderr="",
            exit_code=0,
            execution_time_ms=250.5,
            env_vars={}
        ))

        reporter.script_completed.assert_called_once_with(
            agent_id="main",
            exit_code=0,
            duration_ms=250.5
        )

        observer.close()


class TestConsoleObserverTransitionEvents:
    """Tests for transition event handling."""

    def test_transition_goto(self):
        """TransitionOccurred goto calls reporter.transition()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(TransitionOccurred(
            agent_id="main",
            from_state="START.md",
            to_state="NEXT.md",
            transition_type="goto",
            metadata={}
        ))

        reporter.transition.assert_called_once_with(
            agent_id="main",
            target="NEXT.md",
            transition_type="goto",
            spawned_agent_id=None
        )

        observer.close()

    def test_transition_fork_with_spawned_id(self):
        """TransitionOccurred fork includes spawned_agent_id."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(TransitionOccurred(
            agent_id="main",
            from_state="SPAWN.md",
            to_state="WORKER.md",
            transition_type="fork",
            metadata={"spawned_agent_id": "worker_001"}
        ))

        reporter.transition.assert_called_once_with(
            agent_id="main",
            target="WORKER.md",
            transition_type="fork",
            spawned_agent_id="worker_001"
        )

        observer.close()

    def test_transition_result_terminated_skipped(self):
        """TransitionOccurred result with to_state=None is skipped."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(TransitionOccurred(
            agent_id="main",
            from_state="END.md",
            to_state=None,
            transition_type="result",
            metadata={}
        ))

        # Should not call transition for terminated result
        reporter.transition.assert_not_called()

        observer.close()

    def test_transition_result_with_target(self):
        """TransitionOccurred result with to_state is handled."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(TransitionOccurred(
            agent_id="main",
            from_state="FUNC.md",
            to_state="CALLER.md",
            transition_type="result",
            metadata={}
        ))

        reporter.transition.assert_called_once()

        observer.close()


class TestConsoleObserverProgressEvents:
    """Tests for progress-related event handling."""

    def test_tool_invocation(self):
        """ToolInvocation event calls reporter.tool_invocation()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(ToolInvocation(
            agent_id="main",
            tool_name="Read",
            detail="/path/to/file.txt"
        ))

        reporter.tool_invocation.assert_called_once_with(
            agent_id="main",
            tool_name="Read",
            detail="/path/to/file.txt"
        )

        observer.close()

    def test_tool_invocation_no_detail(self):
        """ToolInvocation without detail passes None."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(ToolInvocation(
            agent_id="main",
            tool_name="Bash"
        ))

        reporter.tool_invocation.assert_called_once_with(
            agent_id="main",
            tool_name="Bash",
            detail=None
        )

        observer.close()

    def test_progress_message(self):
        """ProgressMessage event calls reporter.progress_message()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(ProgressMessage(
            agent_id="main",
            message="Processing data..."
        ))

        reporter.progress_message.assert_called_once_with(
            agent_id="main",
            message="Processing data..."
        )

        observer.close()


class TestConsoleObserverErrorEvents:
    """Tests for error-related event handling."""

    def test_error_occurred_non_retryable(self):
        """ErrorOccurred non-retryable calls reporter.error()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(ErrorOccurred(
            agent_id="main",
            error_type="ValidationError",
            error_message="Invalid input",
            current_state="START.md",
            is_retryable=False,
            retry_count=0,
            max_retries=3
        ))

        reporter.error.assert_called_once_with(
            agent_id="main",
            message="Invalid input"
        )

        observer.close()

    def test_error_occurred_retryable(self):
        """ErrorOccurred retryable includes retry info in message."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(ErrorOccurred(
            agent_id="main",
            error_type="TimeoutError",
            error_message="Request timed out",
            current_state="CALL.md",
            is_retryable=True,
            retry_count=2,
            max_retries=3
        ))

        reporter.error.assert_called_once_with(
            agent_id="main",
            message="Request timed out (retry 2/3)"
        )

        observer.close()


class TestConsoleObserverAgentEvents:
    """Tests for agent lifecycle event handling."""

    def test_agent_terminated(self):
        """AgentTerminated event calls reporter.agent_terminated()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(AgentTerminated(
            agent_id="main",
            result_payload="<result>success</result>"
        ))

        reporter.agent_terminated.assert_called_once_with(
            agent_id="main",
            result="<result>success</result>"
        )

        observer.close()

    def test_agent_paused(self):
        """AgentPaused event calls reporter.agent_paused()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(AgentPaused(
            agent_id="main",
            reason="timeout"
        ))

        reporter.agent_paused.assert_called_once_with(
            agent_id="main",
            reason="timeout"
        )

        observer.close()

    def test_agent_spawned(self):
        """AgentSpawned event calls reporter.agent_spawned()."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        bus.emit(AgentSpawned(
            parent_agent_id="main",
            new_agent_id="worker_001",
            initial_state="WORKER.md"
        ))

        reporter.agent_spawned.assert_called_once_with(
            parent_id="main",
            child_id="worker_001",
            target_state="WORKER.md"
        )

        observer.close()


class TestConsoleObserverErrorHandling:
    """Tests for error resilience in ConsoleObserver."""

    def test_reporter_exception_is_caught(self, caplog):
        """Reporter exceptions are logged but don't propagate."""
        bus = EventBus()
        reporter = MagicMock()
        reporter.workflow_started.side_effect = RuntimeError("Reporter failed")
        observer = ConsoleObserver(reporter, bus)

        # Should not raise
        with caplog.at_level(logging.WARNING):
            bus.emit(WorkflowStarted(
                workflow_id="wf-123",
                scope_dir="/project",
                debug_dir=None
            ))

        assert "ConsoleObserver failed" in caplog.text

        observer.close()

    def test_all_events_have_error_handling(self, caplog):
        """All event handlers catch exceptions."""
        bus = EventBus()
        reporter = MagicMock()

        # Make all methods raise
        for method in [
            'workflow_started', 'workflow_completed', 'workflow_paused',
            'state_started', 'script_started', 'state_completed',
            'script_completed', 'transition', 'tool_invocation',
            'progress_message', 'error', 'agent_terminated',
            'agent_paused', 'agent_spawned'
        ]:
            getattr(reporter, method).side_effect = RuntimeError(f"{method} failed")

        observer = ConsoleObserver(reporter, bus)

        # Emit all event types - none should raise
        with caplog.at_level(logging.WARNING):
            bus.emit(WorkflowStarted(workflow_id="wf", scope_dir="/", debug_dir=None))
            bus.emit(WorkflowCompleted(workflow_id="wf", total_cost_usd=0))
            bus.emit(WorkflowPaused(workflow_id="wf", total_cost_usd=0, paused_agent_count=1))
            bus.emit(StateStarted(agent_id="a", state_name="S.md", state_type="markdown"))
            bus.emit(StateStarted(agent_id="a", state_name="S.sh", state_type="script"))
            bus.emit(StateCompleted(agent_id="a", state_name="S.md", cost_usd=0,
                                    total_cost_usd=0, session_id=None, duration_ms=0))
            bus.emit(ScriptOutput(agent_id="a", state_name="S.sh", step_number=1,
                                  stdout="", stderr="", exit_code=0, execution_time_ms=0, env_vars={}))
            bus.emit(TransitionOccurred(agent_id="a", from_state="A", to_state="B",
                                        transition_type="goto", metadata={}))
            bus.emit(ToolInvocation(agent_id="a", tool_name="T"))
            bus.emit(ProgressMessage(agent_id="a", message="m"))
            bus.emit(ErrorOccurred(agent_id="a", error_type="E", error_message="e",
                                   current_state="S", is_retryable=False, retry_count=0, max_retries=3))
            bus.emit(AgentTerminated(agent_id="a", result_payload="r"))
            bus.emit(AgentPaused(agent_id="a", reason="r"))
            bus.emit(AgentSpawned(parent_agent_id="p", new_agent_id="c", initial_state="S"))

        # All failures should be logged
        assert caplog.text.count("ConsoleObserver failed") >= 10

        observer.close()


class TestConsoleObserverCleanup:
    """Tests for observer cleanup."""

    def test_close_unsubscribes_from_events(self):
        """close() unsubscribes from all events."""
        bus = EventBus()
        reporter = MagicMock()
        observer = ConsoleObserver(reporter, bus)

        assert bus.has_handlers(WorkflowStarted)
        assert bus.has_handlers(StateStarted)
        assert bus.has_handlers(ToolInvocation)

        observer.close()

        assert not bus.has_handlers(WorkflowStarted)
        assert not bus.has_handlers(StateStarted)
        assert not bus.has_handlers(ToolInvocation)


class TestObserverIntegration:
    """Integration tests with both observers together."""

    def test_both_observers_receive_events(self, tmp_path):
        """Both observers receive the same events."""
        bus = EventBus()
        reporter = MagicMock()

        debug_observer = DebugObserver(tmp_path, bus)
        console_observer = ConsoleObserver(reporter, bus)

        # Emit a state started event
        bus.emit(StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown"
        ))

        # DebugObserver should track state
        assert debug_observer._agent_states["main"] == "START"

        # ConsoleObserver should call reporter
        reporter.state_started.assert_called_once()

        debug_observer.close()
        console_observer.close()

    def test_observer_failure_doesnt_affect_other(self, tmp_path, caplog):
        """One observer failing doesn't prevent other from receiving events."""
        bus = EventBus()
        reporter = MagicMock()
        reporter.state_started.side_effect = RuntimeError("Console failed")

        debug_observer = DebugObserver(tmp_path, bus)
        console_observer = ConsoleObserver(reporter, bus)

        # Emit event - console will fail
        with caplog.at_level(logging.WARNING):
            bus.emit(StateStarted(
                agent_id="main",
                state_name="START.md",
                state_type="markdown"
            ))

        # Debug observer should still work
        assert debug_observer._agent_states["main"] == "START"

        debug_observer.close()
        console_observer.close()
