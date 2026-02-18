"""Tests for --disallowed-tools flag injection in command builder."""

import pytest
from unittest.mock import AsyncMock, patch

from src.cc_wrap import _build_claude_command, wrap_claude_code, DISALLOWED_TOOLS


class MockStreamReader:
    """Mock StreamReader that supports .read() for chunk-based reading."""

    def __init__(self, data: bytes):
        self.data = data
        self.pos = 0

    async def read(self, n: int) -> bytes:
        """Read up to n bytes from the stream."""
        if self.pos >= len(self.data):
            return b""
        chunk = self.data[self.pos:self.pos + n]
        self.pos += len(chunk)
        return chunk


class TestBuildClaudeCommandDisallowedTools:
    """Tests for _build_claude_command disallowed-tools flag injection."""

    def _get_disallowed_tools_from_cmd(self, cmd):
        """Extract the tool names from the comma-separated value after --disallowed-tools in cmd."""
        if "--disallowed-tools" not in cmd:
            return []
        idx = cmd.index("--disallowed-tools")
        return cmd[idx + 1].split(",")

    def test_disallowed_tools_flag_present_by_default(self):
        """Test that --disallowed-tools is always present in the command."""
        cmd = _build_claude_command("test prompt")
        assert "--disallowed-tools" in cmd

    def test_all_four_tool_names_present(self):
        """Test that all four disallowed tool names appear in the command."""
        cmd = _build_claude_command("test prompt")
        tools = self._get_disallowed_tools_from_cmd(cmd)
        assert "EnterPlanMode" in tools
        assert "ExitPlanMode" in tools
        assert "AskUserQuestion" in tools
        assert "NotebookEdit" in tools

    def test_disallowed_tools_matches_constant(self):
        """Test that the injected tools match the DISALLOWED_TOOLS constant exactly."""
        cmd = _build_claude_command("test prompt")
        tools = self._get_disallowed_tools_from_cmd(cmd)
        assert set(tools) == set(DISALLOWED_TOOLS)

    def test_disallowed_tools_present_with_dangerously_skip_permissions(self):
        """Test that --disallowed-tools is present alongside --dangerously-skip-permissions."""
        cmd = _build_claude_command("test prompt", dangerously_skip_permissions=True)
        assert "--disallowed-tools" in cmd
        tools = self._get_disallowed_tools_from_cmd(cmd)
        assert set(tools) == set(DISALLOWED_TOOLS)

    def test_disallowed_tools_present_with_model_override(self):
        """Test that --disallowed-tools is present when a model is specified."""
        cmd = _build_claude_command("test prompt", model="haiku")
        assert "--disallowed-tools" in cmd
        tools = self._get_disallowed_tools_from_cmd(cmd)
        assert set(tools) == set(DISALLOWED_TOOLS)

    def test_disallowed_tools_present_with_session_id(self):
        """Test that --disallowed-tools is present when a session_id is specified."""
        cmd = _build_claude_command("test prompt", session_id="session-abc-123")
        assert "--disallowed-tools" in cmd
        tools = self._get_disallowed_tools_from_cmd(cmd)
        assert set(tools) == set(DISALLOWED_TOOLS)

    def test_disallowed_tools_present_with_all_flags(self):
        """Test that --disallowed-tools is present with all other flags combined."""
        cmd = _build_claude_command(
            "test prompt",
            model="sonnet",
            session_id="session-abc-123",
            dangerously_skip_permissions=True,
        )
        assert "--disallowed-tools" in cmd
        tools = self._get_disallowed_tools_from_cmd(cmd)
        assert set(tools) == set(DISALLOWED_TOOLS)

    def test_disallowed_tools_appear_before_prompt(self):
        """Test that --disallowed-tools flag appears before the prompt argument."""
        prompt = "test prompt"
        cmd = _build_claude_command(prompt)
        disallowed_idx = cmd.index("--disallowed-tools")
        prompt_idx = cmd.index(prompt)
        assert disallowed_idx < prompt_idx

    def test_double_dash_appears_immediately_before_prompt(self):
        """Test that -- separator appears immediately before the prompt to end option parsing."""
        prompt = "test prompt"
        cmd = _build_claude_command(prompt)
        prompt_idx = cmd.index(prompt)
        assert cmd[prompt_idx - 1] == "--"


class TestWrapClaudeCodeDisallowedTools:
    """Tests for disallowed-tools flag propagation through the wrapper layer."""

    @pytest.mark.asyncio
    async def test_wrap_claude_code_propagates_disallowed_tools(self):
        """Test that wrap_claude_code passes --disallowed-tools to the subprocess."""
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            mock_process.stdout = MockStreamReader(b'{"type": "content", "text": "test"}\n')
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process

            await wrap_claude_code("test prompt")

            call_args = mock_subprocess.call_args[0]
            cmd = list(call_args)

            assert "--disallowed-tools" in cmd
            idx = cmd.index("--disallowed-tools")
            injected = cmd[idx + 1].split(",")
            assert set(injected) == set(DISALLOWED_TOOLS)
