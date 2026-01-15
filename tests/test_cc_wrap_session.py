import pytest
import json
from unittest.mock import AsyncMock, patch
from cc_wrap import wrap_claude_code


class TestWrapClaudeCodeSession:
    """Tests for wrap_claude_code() session support (Step 2.2.1-2.2.5)."""

    @pytest.mark.asyncio
    async def test_wrap_claude_code_accepts_session_id_parameter(self):
        """Test 2.2.1: wrap_claude_code() accepts optional session_id parameter."""
        with patch('cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            async def mock_stdout():
                yield b'{"type": "content", "text": "test"}\n'
            
            mock_process.stdout = mock_stdout()
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            # Call with session_id parameter - should not raise
            result, session_id = await wrap_claude_code("test prompt", session_id="session_123")
            
            # Verify it was called
            assert mock_subprocess.called

    @pytest.mark.asyncio
    async def test_session_id_provided_passes_resume_flag(self):
        """Test 2.2.2: when session_id provided, passes --resume flag."""
        with patch('cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            async def mock_stdout():
                yield b'{"type": "content", "text": "test"}\n'
            
            mock_process.stdout = mock_stdout()
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            await wrap_claude_code("test prompt", session_id="session_123")
            
            # Verify --resume flag was passed
            # asyncio.create_subprocess_exec receives unpacked args, so call_args[0] is a tuple
            call_args = mock_subprocess.call_args[0]
            cmd = list(call_args)  # Convert tuple to list
            
            # Check that --resume and session_id are in the command
            assert "--resume" in cmd
            resume_idx = cmd.index("--resume")
            assert resume_idx + 1 < len(cmd)
            assert cmd[resume_idx + 1] == "session_123"

    @pytest.mark.asyncio
    async def test_session_id_none_no_resume_flag(self):
        """Test 2.2.3: when session_id is None, no --resume flag."""
        with patch('cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            async def mock_stdout():
                yield b'{"type": "content", "text": "test"}\n'
            
            mock_process.stdout = mock_stdout()
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            await wrap_claude_code("test prompt", session_id=None)
            
            # Verify --resume flag was NOT passed
            call_args = mock_subprocess.call_args[0]
            cmd = list(call_args)  # Convert tuple to list
            
            assert "--resume" not in cmd

    @pytest.mark.asyncio
    async def test_wrap_claude_code_returns_session_id(self):
        """Test 2.2.4: wrap_claude_code() returns session_id from Claude Code output.
        
        Assumes Claude Code outputs session_id in JSON objects with key "session_id".
        """
        # Mock JSON output that includes session_id
        mock_json_lines = [
            b'{"type": "session", "session_id": "session_abc123"}\n',
            b'{"type": "content", "text": "Some response"}\n',
            b'{"type": "message", "session_id": "session_abc123", "content": "text"}\n',
        ]
        
        with patch('cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            async def mock_stdout():
                for line in mock_json_lines:
                    yield line
            
            mock_process.stdout = mock_stdout()
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            results, session_id = await wrap_claude_code("test prompt")
            
            # Should extract session_id from output
            assert session_id == "session_abc123"
            assert len(results) > 0
