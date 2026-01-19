"""Tests for --dangerously-skip-permissions flag."""

import pytest
from unittest.mock import AsyncMock, patch

from src.cc_wrap import _build_claude_command, wrap_claude_code


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


class TestBuildClaudeCommandPermissions:
    """Tests for _build_claude_command permission handling."""

    def test_default_uses_permission_mode_accept_edits(self):
        """Test that default behavior uses --permission-mode acceptEdits."""
        cmd = _build_claude_command("test prompt")
        
        assert "--permission-mode" in cmd
        pm_idx = cmd.index("--permission-mode")
        assert cmd[pm_idx + 1] == "acceptEdits"
        assert "--dangerously-skip-permissions" not in cmd

    def test_dangerously_skip_permissions_false_uses_permission_mode(self):
        """Test that dangerously_skip_permissions=False uses --permission-mode acceptEdits."""
        cmd = _build_claude_command("test prompt", dangerously_skip_permissions=False)
        
        assert "--permission-mode" in cmd
        pm_idx = cmd.index("--permission-mode")
        assert cmd[pm_idx + 1] == "acceptEdits"
        assert "--dangerously-skip-permissions" not in cmd

    def test_dangerously_skip_permissions_true_uses_flag(self):
        """Test that dangerously_skip_permissions=True uses --dangerously-skip-permissions."""
        cmd = _build_claude_command("test prompt", dangerously_skip_permissions=True)
        
        assert "--dangerously-skip-permissions" in cmd
        assert "--permission-mode" not in cmd

    def test_dangerously_skip_permissions_with_model(self):
        """Test that dangerously_skip_permissions works alongside model parameter."""
        cmd = _build_claude_command("test prompt", model="haiku", dangerously_skip_permissions=True)
        
        assert "--dangerously-skip-permissions" in cmd
        assert "--model" in cmd
        model_idx = cmd.index("--model")
        assert cmd[model_idx + 1] == "haiku"
        assert "--permission-mode" not in cmd

    def test_dangerously_skip_permissions_with_session_id(self):
        """Test that dangerously_skip_permissions works alongside session_id parameter."""
        cmd = _build_claude_command(
            "test prompt", 
            session_id="session_123", 
            dangerously_skip_permissions=True
        )
        
        assert "--dangerously-skip-permissions" in cmd
        assert "--resume" in cmd
        resume_idx = cmd.index("--resume")
        assert cmd[resume_idx + 1] == "session_123"
        assert "--permission-mode" not in cmd


class TestWrapClaudeCodePermissions:
    """Tests for wrap_claude_code permission handling."""

    @pytest.mark.asyncio
    async def test_wrap_claude_code_default_uses_permission_mode(self):
        """Test that wrap_claude_code default behavior uses --permission-mode acceptEdits."""
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            mock_process.stdout = MockStreamReader(b'{"type": "content", "text": "test"}\n')
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            await wrap_claude_code("test prompt")
            
            call_args = mock_subprocess.call_args[0]
            cmd = list(call_args)
            
            assert "--permission-mode" in cmd
            pm_idx = cmd.index("--permission-mode")
            assert cmd[pm_idx + 1] == "acceptEdits"
            assert "--dangerously-skip-permissions" not in cmd

    @pytest.mark.asyncio
    async def test_wrap_claude_code_dangerously_skip_permissions(self):
        """Test that wrap_claude_code passes --dangerously-skip-permissions when requested."""
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            mock_process.stdout = MockStreamReader(b'{"type": "content", "text": "test"}\n')
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            await wrap_claude_code("test prompt", dangerously_skip_permissions=True)
            
            call_args = mock_subprocess.call_args[0]
            cmd = list(call_args)
            
            assert "--dangerously-skip-permissions" in cmd
            assert "--permission-mode" not in cmd

    @pytest.mark.asyncio
    async def test_wrap_claude_code_dangerously_skip_permissions_with_fork(self):
        """Test that dangerously_skip_permissions works with fork=True."""
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            mock_process.stdout = MockStreamReader(b'{"type": "content", "text": "test"}\n')
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            await wrap_claude_code(
                "test prompt", 
                session_id="session_123",
                dangerously_skip_permissions=True,
                fork=True
            )
            
            call_args = mock_subprocess.call_args[0]
            cmd = list(call_args)
            
            assert "--dangerously-skip-permissions" in cmd
            assert "--fork-session" in cmd
            assert "--resume" in cmd
            assert "--permission-mode" not in cmd
