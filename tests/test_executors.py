"""Tests for state executors.

This module tests the executor abstraction including:
- ExecutionContext behavior
- ExecutionResult structure
- MarkdownExecutor for .md states
- ScriptExecutor for .sh/.bat states
- get_executor() factory function
- Shared utility functions
"""

import pytest
import json
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

from src.orchestrator.bus import EventBus
from src.orchestrator.events import (
    StateStarted,
    StateCompleted,
    ClaudeStreamOutput,
    ClaudeInvocationStarted,
    ScriptOutput,
    ToolInvocation,
    ProgressMessage,
    ErrorOccurred,
)
from src.orchestrator.executors import (
    get_executor,
    ExecutionContext,
    ExecutionResult,
    MarkdownExecutor,
    ScriptExecutor,
)
from src.orchestrator.executors.utils import (
    extract_state_name,
    resolve_transition_targets,
)
from src.orchestrator.errors import (
    ClaudeCodeError,
    ClaudeCodeLimitError,
    ClaudeCodeTimeoutWrappedError,
    PromptFileError,
    ScriptError,
)
from src.parsing import Transition


def create_mock_stream(json_objects, session_id=None):
    """Create a mock async generator that yields JSON objects."""
    async def mock_generator(*args, **kwargs):
        for i, obj in enumerate(json_objects):
            if session_id and i == len(json_objects) - 1:
                obj = dict(obj)
                obj["session_id"] = session_id
            yield obj
    return mock_generator


class MockReporter:
    """Mock console reporter for testing."""
    def __init__(self):
        self.calls = []

    def state_started(self, agent_id, state_name):
        self.calls.append(("state_started", agent_id, state_name))

    def state_completed(self, agent_id, cost, total_cost):
        self.calls.append(("state_completed", agent_id, cost, total_cost))

    def script_started(self, agent_id, state_name):
        self.calls.append(("script_started", agent_id, state_name))

    def script_completed(self, agent_id, exit_code, time_ms):
        self.calls.append(("script_completed", agent_id, exit_code, time_ms))

    def tool_invocation(self, agent_id, tool_name, detail):
        self.calls.append(("tool_invocation", agent_id, tool_name, detail))

    def progress_message(self, agent_id, message):
        self.calls.append(("progress_message", agent_id, message))

    def error(self, agent_id, message):
        self.calls.append(("error", agent_id, message))

    def tool_error(self, agent_id, message):
        self.calls.append(("tool_error", agent_id, message))

    def transition(self, agent_id, target, tag, spawned_id=None):
        self.calls.append(("transition", agent_id, target, tag, spawned_id))


class MockEventBus:
    """Mock event bus for testing."""
    def __init__(self):
        self.events = []

    def emit(self, event):
        self.events.append(event)

    def on(self, event_type, handler):
        pass

    def off(self, event_type, handler):
        pass

    def emitted(self, event_type, **kwargs):
        """Check if an event of the given type was emitted with matching fields."""
        for event in self.events:
            if isinstance(event, event_type):
                match = True
                for key, value in kwargs.items():
                    if getattr(event, key, None) != value:
                        match = False
                        break
                if match:
                    return True
        return False

    def get_events_of_type(self, event_type):
        """Get all events of a specific type."""
        return [e for e in self.events if isinstance(e, event_type)]


class TestExecutionContext:
    """Tests for ExecutionContext dataclass."""

    def test_context_creation(self):
        """Test basic context creation with required fields."""
        bus = EventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id="test-001",
            scope_dir="/path/to/workflow",
        )

        assert context.bus is bus
        assert context.workflow_id == "test-001"
        assert context.scope_dir == "/path/to/workflow"
        assert context.debug_dir is None
        assert context.state_dir is None
        assert context.default_model is None
        assert context.timeout is None
        assert context.dangerously_skip_permissions is False
        assert context.step_counters == {}
        assert context.reporter is None

    def test_context_with_all_fields(self, tmp_path):
        """Test context creation with all optional fields."""
        bus = EventBus()
        reporter = MockReporter()
        debug_dir = tmp_path / "debug"

        context = ExecutionContext(
            bus=bus,
            workflow_id="test-002",
            scope_dir="/path/to/workflow",
            debug_dir=debug_dir,
            state_dir="/path/to/state",
            default_model="sonnet",
            timeout=300.0,
            dangerously_skip_permissions=True,
            step_counters={"main": 5},
            reporter=reporter,
        )

        assert context.debug_dir == debug_dir
        assert context.state_dir == "/path/to/state"
        assert context.default_model == "sonnet"
        assert context.timeout == 300.0
        assert context.dangerously_skip_permissions is True
        assert context.step_counters == {"main": 5}
        assert context.reporter is reporter

    def test_get_next_step_number_first_call(self):
        """Test get_next_step_number returns 1 on first call for agent."""
        bus = EventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id="test",
            scope_dir="/path",
        )

        step = context.get_next_step_number("main")
        assert step == 1

    def test_get_next_step_number_increments(self):
        """Test get_next_step_number increments on each call."""
        bus = EventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id="test",
            scope_dir="/path",
        )

        assert context.get_next_step_number("main") == 1
        assert context.get_next_step_number("main") == 2
        assert context.get_next_step_number("main") == 3

    def test_get_next_step_number_per_agent(self):
        """Test get_next_step_number is tracked per agent."""
        bus = EventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id="test",
            scope_dir="/path",
        )

        assert context.get_next_step_number("main") == 1
        assert context.get_next_step_number("worker1") == 1
        assert context.get_next_step_number("main") == 2
        assert context.get_next_step_number("worker1") == 2


class TestExecutionResult:
    """Tests for ExecutionResult dataclass."""

    def test_result_creation(self):
        """Test basic result creation."""
        transition = Transition(
            tag="goto",
            target="NEXT.md",
            attributes={},
            payload=None
        )

        result = ExecutionResult(
            transition=transition,
            session_id="sess-123",
            cost_usd=0.05
        )

        assert result.transition is transition
        assert result.session_id == "sess-123"
        assert result.cost_usd == 0.05

    def test_result_with_none_session(self):
        """Test result with None session_id (for scripts)."""
        transition = Transition(
            tag="result",
            target="",
            attributes={},
            payload="done"
        )

        result = ExecutionResult(
            transition=transition,
            session_id=None,
            cost_usd=0.0
        )

        assert result.session_id is None
        assert result.cost_usd == 0.0


class TestExecutorUtils:
    """Tests for shared utility functions."""

    def test_extract_state_name_md(self):
        """Test extracting state name from .md file."""
        assert extract_state_name("START.md") == "START"

    def test_extract_state_name_sh(self):
        """Test extracting state name from .sh file."""
        assert extract_state_name("CHECK.sh") == "CHECK"

    def test_extract_state_name_bat(self):
        """Test extracting state name from .bat file."""
        assert extract_state_name("SCRIPT.bat") == "SCRIPT"

    def test_extract_state_name_case_insensitive(self):
        """Test case-insensitive extension handling."""
        assert extract_state_name("START.MD") == "START"
        assert extract_state_name("CHECK.SH") == "CHECK"

    def test_extract_state_name_no_extension(self):
        """Test state name with no recognized extension."""
        assert extract_state_name("NOEXT") == "NOEXT"
        assert extract_state_name("file.txt") == "file.txt"

    def test_resolve_transition_targets_result(self):
        """Test that result transitions are returned unchanged."""
        transition = Transition(
            tag="result",
            target="",
            attributes={},
            payload="done"
        )
        result = resolve_transition_targets(transition, "/some/path")
        assert result is transition

    def test_resolve_transition_targets_goto(self, tmp_path):
        """Test resolving goto transition target."""
        # Create target file
        (tmp_path / "NEXT.md").write_text("prompt")

        transition = Transition(
            tag="goto",
            target="NEXT",
            attributes={},
            payload=None
        )
        result = resolve_transition_targets(transition, str(tmp_path))

        assert result.tag == "goto"
        assert result.target == "NEXT.md"

    def test_resolve_transition_targets_with_return(self, tmp_path):
        """Test resolving transition with return attribute."""
        # Create target files
        (tmp_path / "FUNC.md").write_text("prompt")
        (tmp_path / "CALLER.md").write_text("prompt")

        transition = Transition(
            tag="function",
            target="FUNC",
            attributes={"return": "CALLER"},
            payload=None
        )
        result = resolve_transition_targets(transition, str(tmp_path))

        assert result.target == "FUNC.md"
        assert result.attributes["return"] == "CALLER.md"

    def test_resolve_transition_targets_with_next(self, tmp_path):
        """Test resolving transition with next attribute."""
        # Create target files
        (tmp_path / "FORK.md").write_text("prompt")
        (tmp_path / "AFTER.md").write_text("prompt")

        transition = Transition(
            tag="fork",
            target="FORK",
            attributes={"next": "AFTER"},
            payload=None
        )
        result = resolve_transition_targets(transition, str(tmp_path))

        assert result.target == "FORK.md"
        assert result.attributes["next"] == "AFTER.md"

    def test_resolve_transition_targets_not_found(self, tmp_path):
        """Test FileNotFoundError for missing target."""
        transition = Transition(
            tag="goto",
            target="MISSING",
            attributes={},
            payload=None
        )

        with pytest.raises(FileNotFoundError):
            resolve_transition_targets(transition, str(tmp_path))


class TestGetExecutorFactory:
    """Tests for get_executor() factory function."""

    def test_get_executor_markdown(self):
        """Test get_executor returns MarkdownExecutor for .md files."""
        executor = get_executor("START.md")
        assert isinstance(executor, MarkdownExecutor)

    def test_get_executor_shell(self):
        """Test get_executor returns ScriptExecutor for .sh files."""
        executor = get_executor("CHECK.sh")
        assert isinstance(executor, ScriptExecutor)

    @pytest.mark.skipif(
        __import__('sys').platform != 'win32',
        reason="Windows-only test"
    )
    def test_get_executor_batch(self):
        """Test get_executor returns ScriptExecutor for .bat files."""
        executor = get_executor("CHECK.bat")
        assert isinstance(executor, ScriptExecutor)

    def test_get_executor_case_insensitive(self):
        """Test get_executor handles case-insensitive extensions."""
        executor = get_executor("START.MD")
        assert isinstance(executor, MarkdownExecutor)

    def test_get_executor_singletons(self):
        """Test executors are singletons (same instance returned)."""
        executor1 = get_executor("A.md")
        executor2 = get_executor("B.md")
        assert executor1 is executor2

        executor3 = get_executor("A.sh")
        executor4 = get_executor("B.sh")
        assert executor3 is executor4


class TestMarkdownExecutor:
    """Tests for MarkdownExecutor."""

    @pytest.fixture
    def setup_workflow(self, tmp_path):
        """Set up a basic workflow for testing."""
        scope_dir = tmp_path / "workflow"
        scope_dir.mkdir()

        # Create prompt file
        prompt_file = scope_dir / "START.md"
        prompt_file.write_text("Test prompt for {{result}}")

        # Create target file
        next_file = scope_dir / "NEXT.md"
        next_file.write_text("Next prompt")

        return {
            "scope_dir": str(scope_dir),
            "workflow_id": "test-001",
            "agents": [
                {
                    "id": "main",
                    "current_state": "START.md",
                    "session_id": None,
                    "stack": []
                }
            ],
            "total_cost_usd": 0.0,
            "budget_usd": 10.0,
        }

    @pytest.mark.asyncio
    async def test_emits_state_started_event(self, setup_workflow):
        """Test MarkdownExecutor emits StateStarted event."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
        )

        mock_outputs = [
            {"type": "content", "text": "Output\n<goto>NEXT.md</goto>"},
            {"session_id": "sess-123", "total_cost_usd": 0.01}
        ]
        mock_stream = create_mock_stream(mock_outputs)

        executor = MarkdownExecutor()
        agent = setup_workflow["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream):
            await executor.execute(agent, setup_workflow, context)

        assert bus.emitted(StateStarted, agent_id="main", state_name="START.md", state_type="markdown")

    @pytest.mark.asyncio
    async def test_emits_state_completed_event(self, setup_workflow):
        """Test MarkdownExecutor emits StateCompleted event."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
        )

        mock_outputs = [
            {"type": "content", "text": "Output\n<goto>NEXT.md</goto>"},
            {"session_id": "sess-123", "total_cost_usd": 0.05}
        ]
        mock_stream = create_mock_stream(mock_outputs)

        executor = MarkdownExecutor()
        agent = setup_workflow["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream):
            await executor.execute(agent, setup_workflow, context)

        completed_events = bus.get_events_of_type(StateCompleted)
        assert len(completed_events) == 1
        assert completed_events[0].agent_id == "main"
        assert completed_events[0].state_name == "START.md"
        assert completed_events[0].cost_usd == 0.05

    @pytest.mark.asyncio
    async def test_returns_execution_result_with_transition(self, setup_workflow):
        """Test MarkdownExecutor returns ExecutionResult with parsed transition."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
        )

        mock_outputs = [
            {"type": "content", "text": "Output\n<goto>NEXT.md</goto>"},
            {"session_id": "sess-123", "total_cost_usd": 0.02}
        ]
        mock_stream = create_mock_stream(mock_outputs)

        executor = MarkdownExecutor()
        agent = setup_workflow["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream):
            result = await executor.execute(agent, setup_workflow, context)

        assert isinstance(result, ExecutionResult)
        assert result.transition.tag == "goto"
        assert result.transition.target == "NEXT.md"
        assert result.session_id == "sess-123"
        assert result.cost_usd == 0.02

    @pytest.mark.asyncio
    async def test_extracts_session_id_from_response(self, setup_workflow):
        """Test session_id is extracted from Claude response."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
        )

        mock_outputs = [
            {"type": "content", "text": "<goto>NEXT.md</goto>"},
            {"session_id": "new-sess-456", "total_cost_usd": 0.01}
        ]
        mock_stream = create_mock_stream(mock_outputs)

        executor = MarkdownExecutor()
        agent = setup_workflow["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream):
            result = await executor.execute(agent, setup_workflow, context)

        assert result.session_id == "new-sess-456"

    @pytest.mark.asyncio
    async def test_accumulates_cost(self, setup_workflow):
        """Test cost is accumulated in state."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
        )

        setup_workflow["total_cost_usd"] = 1.00

        mock_outputs = [
            {"type": "content", "text": "<goto>NEXT.md</goto>"},
            {"total_cost_usd": 0.10}
        ]
        mock_stream = create_mock_stream(mock_outputs)

        executor = MarkdownExecutor()
        agent = setup_workflow["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream):
            result = await executor.execute(agent, setup_workflow, context)

        assert result.cost_usd == 0.10
        assert setup_workflow["total_cost_usd"] == 1.10

    @pytest.mark.asyncio
    async def test_raises_prompt_file_error_when_not_found(self, setup_workflow, tmp_path):
        """Test PromptFileError is raised when prompt file not found."""
        bus = MockEventBus()

        # Use a different scope_dir without the prompt file
        empty_dir = tmp_path / "empty"
        empty_dir.mkdir()
        setup_workflow["scope_dir"] = str(empty_dir)

        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
        )

        executor = MarkdownExecutor()
        agent = setup_workflow["agents"][0]

        with pytest.raises(PromptFileError):
            await executor.execute(agent, setup_workflow, context)

    @pytest.mark.asyncio
    async def test_raises_limit_error_on_usage_limit(self, setup_workflow):
        """Test ClaudeCodeLimitError is raised on usage limit."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
        )

        mock_outputs = [
            {"type": "result", "is_error": True, "result": "You've hit your limit for today"}
        ]
        mock_stream = create_mock_stream(mock_outputs)

        executor = MarkdownExecutor()
        agent = setup_workflow["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream):
            with pytest.raises(ClaudeCodeLimitError):
                await executor.execute(agent, setup_workflow, context)

    @pytest.mark.asyncio
    async def test_emits_claude_stream_output_with_debug(self, setup_workflow, tmp_path):
        """Test ClaudeStreamOutput events are emitted when debug is enabled."""
        bus = MockEventBus()
        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
            debug_dir=debug_dir,
        )

        mock_outputs = [
            {"type": "content", "text": "Output\n<goto>NEXT.md</goto>"},
            {"session_id": "sess-123", "total_cost_usd": 0.01}
        ]
        mock_stream = create_mock_stream(mock_outputs)

        executor = MarkdownExecutor()
        agent = setup_workflow["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream):
            await executor.execute(agent, setup_workflow, context)

        stream_events = bus.get_events_of_type(ClaudeStreamOutput)
        assert len(stream_events) == 2  # Two objects yielded

    @pytest.mark.asyncio
    async def test_emits_state_started_event_for_markdown(self, setup_workflow):
        """Test StateStarted event is emitted with correct state_type."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
        )

        mock_outputs = [
            {"type": "content", "text": "<goto>NEXT.md</goto>"},
            {"total_cost_usd": 0.01}
        ]
        mock_stream = create_mock_stream(mock_outputs)

        executor = MarkdownExecutor()
        agent = setup_workflow["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream):
            await executor.execute(agent, setup_workflow, context)

        # Verify StateStarted event was emitted with correct state_type
        state_started_events = bus.get_events_of_type(StateStarted)
        assert len(state_started_events) == 1
        assert state_started_events[0].agent_id == "main"
        assert state_started_events[0].state_name == "START.md"
        assert state_started_events[0].state_type == "markdown"

    @pytest.mark.asyncio
    async def test_handles_result_transition(self, setup_workflow):
        """Test handling of result transition (agent termination)."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
        )

        mock_outputs = [
            {"type": "content", "text": "Done\n<result>Task completed</result>"},
            {"total_cost_usd": 0.01}
        ]
        mock_stream = create_mock_stream(mock_outputs)

        executor = MarkdownExecutor()
        agent = setup_workflow["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream):
            result = await executor.execute(agent, setup_workflow, context)

        assert result.transition.tag == "result"
        assert result.transition.payload == "Task completed"


class TestScriptExecutor:
    """Tests for ScriptExecutor."""

    @pytest.fixture
    def setup_script_workflow(self, tmp_path):
        """Set up a workflow with a script state."""
        scope_dir = tmp_path / "workflow"
        scope_dir.mkdir()

        # Create script file
        script_file = scope_dir / "CHECK.sh"
        script_file.write_text("#!/bin/bash\necho '<goto>NEXT.md</goto>'")
        script_file.chmod(0o755)

        # Create target file
        next_file = scope_dir / "NEXT.md"
        next_file.write_text("Next prompt")

        return {
            "scope_dir": str(scope_dir),
            "workflow_id": "test-001",
            "agents": [
                {
                    "id": "main",
                    "current_state": "CHECK.sh",
                    "session_id": "existing-sess",
                    "stack": []
                }
            ],
            "total_cost_usd": 0.0,
            "budget_usd": 10.0,
        }

    @pytest.mark.asyncio
    async def test_emits_state_started_event(self, setup_script_workflow):
        """Test ScriptExecutor emits StateStarted event."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_script_workflow["workflow_id"],
            scope_dir=setup_script_workflow["scope_dir"],
        )

        # Mock script result
        mock_result = MagicMock()
        mock_result.stdout = "<goto>NEXT.md</goto>\n"
        mock_result.stderr = ""
        mock_result.exit_code = 0

        executor = ScriptExecutor()
        agent = setup_script_workflow["agents"][0]

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            await executor.execute(agent, setup_script_workflow, context)

        assert bus.emitted(StateStarted, agent_id="main", state_name="CHECK.sh", state_type="script")

    @pytest.mark.asyncio
    async def test_emits_state_completed_event(self, setup_script_workflow):
        """Test ScriptExecutor emits StateCompleted event."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_script_workflow["workflow_id"],
            scope_dir=setup_script_workflow["scope_dir"],
        )

        mock_result = MagicMock()
        mock_result.stdout = "<goto>NEXT.md</goto>\n"
        mock_result.stderr = ""
        mock_result.exit_code = 0

        executor = ScriptExecutor()
        agent = setup_script_workflow["agents"][0]

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            await executor.execute(agent, setup_script_workflow, context)

        completed_events = bus.get_events_of_type(StateCompleted)
        assert len(completed_events) == 1
        assert completed_events[0].agent_id == "main"
        assert completed_events[0].state_name == "CHECK.sh"
        assert completed_events[0].cost_usd == 0.0  # Scripts are free

    @pytest.mark.asyncio
    async def test_preserves_session_id(self, setup_script_workflow):
        """Test ScriptExecutor preserves existing session_id."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_script_workflow["workflow_id"],
            scope_dir=setup_script_workflow["scope_dir"],
        )

        mock_result = MagicMock()
        mock_result.stdout = "<goto>NEXT.md</goto>\n"
        mock_result.stderr = ""
        mock_result.exit_code = 0

        executor = ScriptExecutor()
        agent = setup_script_workflow["agents"][0]

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            result = await executor.execute(agent, setup_script_workflow, context)

        assert result.session_id == "existing-sess"  # Preserved

    @pytest.mark.asyncio
    async def test_returns_zero_cost(self, setup_script_workflow):
        """Test ScriptExecutor returns zero cost."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_script_workflow["workflow_id"],
            scope_dir=setup_script_workflow["scope_dir"],
        )

        mock_result = MagicMock()
        mock_result.stdout = "<goto>NEXT.md</goto>\n"
        mock_result.stderr = ""
        mock_result.exit_code = 0

        executor = ScriptExecutor()
        agent = setup_script_workflow["agents"][0]

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            result = await executor.execute(agent, setup_script_workflow, context)

        assert result.cost_usd == 0.0

    @pytest.mark.asyncio
    async def test_parses_transition_from_stdout(self, setup_script_workflow):
        """Test transition is parsed from script stdout."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_script_workflow["workflow_id"],
            scope_dir=setup_script_workflow["scope_dir"],
        )

        mock_result = MagicMock()
        mock_result.stdout = "Some debug output\n<goto>NEXT.md</goto>\nMore output"
        mock_result.stderr = ""
        mock_result.exit_code = 0

        executor = ScriptExecutor()
        agent = setup_script_workflow["agents"][0]

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            result = await executor.execute(agent, setup_script_workflow, context)

        assert result.transition.tag == "goto"
        assert result.transition.target == "NEXT.md"

    @pytest.mark.asyncio
    async def test_raises_error_on_nonzero_exit(self, setup_script_workflow):
        """Test ScriptError is raised on non-zero exit code."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_script_workflow["workflow_id"],
            scope_dir=setup_script_workflow["scope_dir"],
        )

        mock_result = MagicMock()
        mock_result.stdout = ""
        mock_result.stderr = "Error occurred"
        mock_result.exit_code = 1

        executor = ScriptExecutor()
        agent = setup_script_workflow["agents"][0]

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            with pytest.raises(ScriptError) as exc_info:
                await executor.execute(agent, setup_script_workflow, context)

            assert "exit code 1" in str(exc_info.value)

    @pytest.mark.asyncio
    async def test_raises_error_on_no_transition(self, setup_script_workflow):
        """Test ScriptError is raised when no transition in stdout."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_script_workflow["workflow_id"],
            scope_dir=setup_script_workflow["scope_dir"],
        )

        mock_result = MagicMock()
        mock_result.stdout = "Just some output without transition"
        mock_result.stderr = ""
        mock_result.exit_code = 0

        executor = ScriptExecutor()
        agent = setup_script_workflow["agents"][0]

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            with pytest.raises(ScriptError) as exc_info:
                await executor.execute(agent, setup_script_workflow, context)

            assert "no transition tag" in str(exc_info.value)

    @pytest.mark.asyncio
    async def test_raises_error_on_multiple_transitions(self, setup_script_workflow):
        """Test ScriptError is raised when multiple transitions in stdout."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_script_workflow["workflow_id"],
            scope_dir=setup_script_workflow["scope_dir"],
        )

        mock_result = MagicMock()
        mock_result.stdout = "<goto>NEXT.md</goto><result>done</result>"
        mock_result.stderr = ""
        mock_result.exit_code = 0

        executor = ScriptExecutor()
        agent = setup_script_workflow["agents"][0]

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            with pytest.raises(ScriptError) as exc_info:
                await executor.execute(agent, setup_script_workflow, context)

            assert "2 transition tags" in str(exc_info.value)

    @pytest.mark.asyncio
    async def test_emits_script_output_event_with_debug(self, setup_script_workflow, tmp_path):
        """Test ScriptOutput event is emitted when debug is enabled."""
        bus = MockEventBus()
        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_script_workflow["workflow_id"],
            scope_dir=setup_script_workflow["scope_dir"],
            debug_dir=debug_dir,
        )

        mock_result = MagicMock()
        mock_result.stdout = "<goto>NEXT.md</goto>\n"
        mock_result.stderr = "some stderr"
        mock_result.exit_code = 0

        executor = ScriptExecutor()
        agent = setup_script_workflow["agents"][0]

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            await executor.execute(agent, setup_script_workflow, context)

        output_events = bus.get_events_of_type(ScriptOutput)
        assert len(output_events) == 1
        assert output_events[0].stdout == "<goto>NEXT.md</goto>\n"
        assert output_events[0].stderr == "some stderr"
        assert output_events[0].exit_code == 0

    @pytest.mark.asyncio
    async def test_emits_state_started_event_for_script(self, setup_script_workflow):
        """Test StateStarted event is emitted with correct state_type for scripts."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_script_workflow["workflow_id"],
            scope_dir=setup_script_workflow["scope_dir"],
        )

        mock_result = MagicMock()
        mock_result.stdout = "<goto>NEXT.md</goto>\n"
        mock_result.stderr = ""
        mock_result.exit_code = 0

        executor = ScriptExecutor()
        agent = setup_script_workflow["agents"][0]

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            await executor.execute(agent, setup_script_workflow, context)

        # Verify StateStarted event was emitted with correct state_type
        state_started_events = bus.get_events_of_type(StateStarted)
        assert len(state_started_events) == 1
        assert state_started_events[0].agent_id == "main"
        assert state_started_events[0].state_name == "CHECK.sh"
        assert state_started_events[0].state_type == "script"

    @pytest.mark.asyncio
    async def test_handles_result_transition(self, setup_script_workflow):
        """Test handling of result transition from script."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_script_workflow["workflow_id"],
            scope_dir=setup_script_workflow["scope_dir"],
        )

        mock_result = MagicMock()
        mock_result.stdout = "<result>Script finished successfully</result>"
        mock_result.stderr = ""
        mock_result.exit_code = 0

        executor = ScriptExecutor()
        agent = setup_script_workflow["agents"][0]

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            result = await executor.execute(agent, setup_script_workflow, context)

        assert result.transition.tag == "result"
        assert result.transition.payload == "Script finished successfully"


class TestMarkdownExecutorReminderPrompts:
    """Tests for reminder prompt retry logic in MarkdownExecutor."""

    @pytest.fixture
    def setup_workflow_with_policy(self, tmp_path):
        """Set up workflow with a policy that allows reminders."""
        scope_dir = tmp_path / "workflow"
        scope_dir.mkdir()

        # Create prompt file with allowed_transitions policy
        prompt_file = scope_dir / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
  - { tag: result }
---
Test prompt
""")

        # Create target file
        next_file = scope_dir / "NEXT.md"
        next_file.write_text("Next prompt")

        return {
            "scope_dir": str(scope_dir),
            "workflow_id": "test-001",
            "agents": [
                {
                    "id": "main",
                    "current_state": "START.md",
                    "session_id": None,
                    "stack": []
                }
            ],
            "total_cost_usd": 0.0,
            "budget_usd": 10.0,
        }

    @pytest.mark.asyncio
    async def test_emits_error_event_on_retry(self, setup_workflow_with_policy):
        """Test ErrorOccurred event is emitted when retrying."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow_with_policy["workflow_id"],
            scope_dir=setup_workflow_with_policy["scope_dir"],
        )

        # First call: no transition, second call: valid transition
        call_count = [0]
        def mock_stream_factory(*args, **kwargs):
            call_count[0] += 1
            if call_count[0] == 1:
                return create_mock_stream([
                    {"type": "content", "text": "No transition here"},
                    {"total_cost_usd": 0.01}
                ])(*args, **kwargs)
            else:
                return create_mock_stream([
                    {"type": "content", "text": "<goto>NEXT.md</goto>"},
                    {"total_cost_usd": 0.01}
                ])(*args, **kwargs)

        executor = MarkdownExecutor()
        agent = setup_workflow_with_policy["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream_factory):
            result = await executor.execute(agent, setup_workflow_with_policy, context)

        # Should have emitted an error event for the retry
        error_events = bus.get_events_of_type(ErrorOccurred)
        assert len(error_events) == 1
        assert error_events[0].is_retryable is True
        assert error_events[0].retry_count == 1

    @pytest.mark.asyncio
    async def test_raises_after_max_retries(self, setup_workflow_with_policy):
        """Test ValueError is raised after MAX_REMINDER_ATTEMPTS."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow_with_policy["workflow_id"],
            scope_dir=setup_workflow_with_policy["scope_dir"],
        )

        # Always return no transition
        mock_stream = create_mock_stream([
            {"type": "content", "text": "No transition"},
            {"total_cost_usd": 0.01}
        ])

        executor = MarkdownExecutor()
        agent = setup_workflow_with_policy["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream):
            with pytest.raises(ValueError) as exc_info:
                await executor.execute(agent, setup_workflow_with_policy, context)

            assert "3 reminder attempts" in str(exc_info.value)


class TestExecutorDebugOutput:
    """Tests for debug file output from executors."""

    @pytest.fixture
    def setup_workflow(self, tmp_path):
        """Set up workflow for debug testing."""
        scope_dir = tmp_path / "workflow"
        scope_dir.mkdir()

        prompt_file = scope_dir / "START.md"
        prompt_file.write_text("Test prompt")

        next_file = scope_dir / "NEXT.md"
        next_file.write_text("Next prompt")

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        return {
            "scope_dir": str(scope_dir),
            "workflow_id": "test-001",
            "agents": [
                {
                    "id": "main",
                    "current_state": "START.md",
                    "session_id": None,
                    "stack": []
                }
            ],
            "total_cost_usd": 0.0,
            "budget_usd": 10.0,
            "debug_dir": debug_dir,
        }

    @pytest.mark.asyncio
    async def test_writes_jsonl_debug_file(self, setup_workflow):
        """Test MarkdownExecutor writes JSONL debug file."""
        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
            debug_dir=setup_workflow["debug_dir"],
        )

        mock_outputs = [
            {"type": "content", "text": "<goto>NEXT.md</goto>"},
            {"session_id": "sess-123", "total_cost_usd": 0.01}
        ]
        mock_stream = create_mock_stream(mock_outputs)

        executor = MarkdownExecutor()
        agent = setup_workflow["agents"][0]

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream):
            await executor.execute(agent, setup_workflow, context)

        # Check debug file was written
        debug_files = list(setup_workflow["debug_dir"].glob("*.jsonl"))
        assert len(debug_files) == 1

        # Verify content
        with open(debug_files[0]) as f:
            lines = f.readlines()

        assert len(lines) == 2
        obj1 = json.loads(lines[0])
        assert obj1["type"] == "content"

    @pytest.mark.asyncio
    async def test_script_writes_debug_files(self, setup_workflow, tmp_path):
        """Test ScriptExecutor writes stdout/stderr/meta debug files."""
        scope_dir = Path(setup_workflow["scope_dir"])

        # Create script file
        script_file = scope_dir / "CHECK.sh"
        script_file.write_text("#!/bin/bash\necho '<goto>NEXT.md</goto>'")
        script_file.chmod(0o755)

        bus = MockEventBus()
        context = ExecutionContext(
            bus=bus,
            workflow_id=setup_workflow["workflow_id"],
            scope_dir=setup_workflow["scope_dir"],
            debug_dir=setup_workflow["debug_dir"],
        )

        mock_result = MagicMock()
        mock_result.stdout = "<goto>NEXT.md</goto>\n"
        mock_result.stderr = "debug info"
        mock_result.exit_code = 0

        executor = ScriptExecutor()
        agent = setup_workflow["agents"][0].copy()
        agent["current_state"] = "CHECK.sh"

        with patch('src.orchestrator.run_script', new_callable=AsyncMock, return_value=mock_result):
            await executor.execute(agent, setup_workflow, context)

        # Check debug files were written
        stdout_files = list(setup_workflow["debug_dir"].glob("*.stdout.txt"))
        stderr_files = list(setup_workflow["debug_dir"].glob("*.stderr.txt"))
        meta_files = list(setup_workflow["debug_dir"].glob("*.meta.json"))

        assert len(stdout_files) == 1
        assert len(stderr_files) == 1
        assert len(meta_files) == 1

        # Verify content
        assert stdout_files[0].read_text() == "<goto>NEXT.md</goto>\n"
        assert stderr_files[0].read_text() == "debug info"

        meta = json.loads(meta_files[0].read_text())
        assert meta["exit_code"] == 0
