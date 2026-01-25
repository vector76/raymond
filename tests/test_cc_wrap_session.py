import asyncio
import pytest
from unittest.mock import AsyncMock, patch
from src.cc_wrap import wrap_claude_code, wrap_claude_code_stream, ClaudeCodeTimeoutError


class MockStreamReader:
    """Mock StreamReader that supports .read() for chunk-based reading."""
    
    def __init__(self, data: bytes, chunk_limit: int = None):
        """
        Args:
            data: The data to return from reads
            chunk_limit: If set, limit each read to this many bytes (simulates slow/chunked I/O)
        """
        self.data = data
        self.pos = 0
        self.chunk_limit = chunk_limit
    
    async def read(self, n: int) -> bytes:
        """Read up to n bytes from the stream."""
        if self.pos >= len(self.data):
            return b""
        # Apply chunk_limit if set (to simulate partial reads)
        max_read = min(n, self.chunk_limit) if self.chunk_limit else n
        chunk = self.data[self.pos:self.pos + max_read]
        self.pos += len(chunk)  # Advance by actual bytes read
        return chunk


class TestWrapClaudeCodeSession:
    """Tests for wrap_claude_code() session support (Step 2.2.1-2.2.5)."""

    @pytest.mark.asyncio
    async def test_wrap_claude_code_accepts_session_id_parameter(self):
        """Test 2.2.1: wrap_claude_code() accepts optional session_id parameter."""
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            mock_process.stdout = MockStreamReader(b'{"type": "content", "text": "test"}\n')
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
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            mock_process.stdout = MockStreamReader(b'{"type": "content", "text": "test"}\n')
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
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            mock_process.stdout = MockStreamReader(b'{"type": "content", "text": "test"}\n')
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
        mock_data = (
            b'{"type": "session", "session_id": "session_abc123"}\n'
            b'{"type": "content", "text": "Some response"}\n'
            b'{"type": "message", "session_id": "session_abc123", "content": "text"}\n'
        )
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            mock_process.stdout = MockStreamReader(mock_data)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            results, session_id = await wrap_claude_code("test prompt")
            
            # Should extract session_id from output
            assert session_id == "session_abc123"
            assert len(results) > 0

    @pytest.mark.asyncio
    async def test_wrap_claude_code_handles_long_lines(self):
        """Test that wrap_claude_code handles lines longer than 64KB.
        
        This tests the fix for asyncio.LimitOverrunError which occurs when
        Claude Code outputs JSON lines exceeding the default 64KB buffer limit.
        """
        # Create a JSON line that exceeds 64KB (the default asyncio readline limit)
        large_content = "x" * (70 * 1024)  # 70KB of content
        large_json = f'{{"type": "content", "text": "{large_content}"}}\n'.encode('utf-8')
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            mock_process.stdout = MockStreamReader(large_json)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            # This should NOT raise ValueError: "Separator is not found, and chunk exceed the limit"
            results, session_id = await wrap_claude_code("test prompt")
            
            # Verify the large content was parsed correctly
            assert len(results) == 1
            assert results[0]["type"] == "content"
            assert len(results[0]["text"]) == 70 * 1024

    @pytest.mark.asyncio
    async def test_wrap_claude_code_handles_chunked_reads(self):
        """Test that wrap_claude_code correctly reassembles lines split across chunks.
        
        This simulates the real-world scenario where data arrives in small chunks
        and lines may be split across multiple read() calls.
        """
        mock_data = (
            b'{"type": "start", "id": 1}\n'
            b'{"type": "content", "text": "Hello world"}\n'
            b'{"type": "end", "id": 1}\n'
        )
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            # Use chunk_limit=10 to force data to be read in 10-byte chunks
            # This will split lines across multiple reads
            mock_process.stdout = MockStreamReader(mock_data, chunk_limit=10)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            results, session_id = await wrap_claude_code("test prompt")
            
            # Should correctly parse all 3 JSON objects despite chunked reading
            assert len(results) == 3
            assert results[0]["type"] == "start"
            assert results[1]["type"] == "content"
            assert results[1]["text"] == "Hello world"
            assert results[2]["type"] == "end"

    @pytest.mark.asyncio
    async def test_wrap_claude_code_handles_no_trailing_newline(self):
        """Test that wrap_claude_code handles data without a trailing newline."""
        # No trailing newline on the last line
        mock_data = b'{"type": "content", "text": "test"}'
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            mock_process.stdout = MockStreamReader(mock_data)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            results, session_id = await wrap_claude_code("test prompt")
            
            # Should still parse the JSON even without trailing newline
            assert len(results) == 1
            assert results[0]["type"] == "content"


class TestWrapClaudeCodeStream:
    """Tests for wrap_claude_code_stream() chunk-based reading."""

    @pytest.mark.asyncio
    async def test_wrap_claude_code_stream_handles_long_lines(self):
        """Test that wrap_claude_code_stream handles lines longer than 64KB."""
        # Create a JSON line that exceeds 64KB
        large_content = "y" * (70 * 1024)
        large_json = f'{{"type": "content", "text": "{large_content}"}}\n'.encode('utf-8')
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            mock_process.stdout = MockStreamReader(large_json)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            results = []
            async for obj in wrap_claude_code_stream("test prompt"):
                results.append(obj)
            
            assert len(results) == 1
            assert results[0]["type"] == "content"
            assert len(results[0]["text"]) == 70 * 1024

    @pytest.mark.asyncio
    async def test_wrap_claude_code_stream_handles_chunked_reads(self):
        """Test that wrap_claude_code_stream correctly reassembles lines split across chunks."""
        mock_data = (
            b'{"type": "start", "id": 1}\n'
            b'{"type": "content", "text": "Hello world"}\n'
            b'{"type": "end", "id": 1}\n'
        )
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            
            # Use chunk_limit=10 to force data to be read in 10-byte chunks
            mock_process.stdout = MockStreamReader(mock_data, chunk_limit=10)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_subprocess.return_value = mock_process
            
            results = []
            async for obj in wrap_claude_code_stream("test prompt"):
                results.append(obj)
            
            assert len(results) == 3
            assert results[0]["type"] == "start"
            assert results[1]["type"] == "content"
            assert results[2]["type"] == "end"


class MockStreamReaderWithDelays:
    """Mock StreamReader that can simulate delays between data chunks.
    
    This is used to test idle timeout behavior where we need to control
    the timing of data arrival.
    """
    
    def __init__(self, chunks_with_delays: list):
        """
        Args:
            chunks_with_delays: List of tuples (data_bytes, delay_seconds_before)
                Each tuple contains the data to return and the delay before returning it.
                Example: [(b'first chunk', 0.0), (b'second chunk', 0.5)]
        """
        self.chunks = chunks_with_delays
        self.index = 0
    
    async def read(self, n: int) -> bytes:
        """Read the next chunk, with delay if specified."""
        if self.index >= len(self.chunks):
            return b""
        
        data, delay = self.chunks[self.index]
        self.index += 1
        
        if delay > 0:
            await asyncio.sleep(delay)
        
        return data


class TestIdleTimeout:
    """Tests for idle timeout behavior in wrap_claude_code_stream().
    
    The streaming function uses an idle timeout that resets whenever data is received.
    This allows long-running Claude Code executions to complete as long as they
    continuously produce output, while detecting "stuck" executions.
    """

    @pytest.mark.asyncio
    async def test_no_timeout_with_continuous_data(self):
        """Test that continuous data flow doesn't trigger timeout even with short timeout."""
        # Data arrives in rapid succession (no significant delays)
        chunks = [
            (b'{"type": "start"}\n', 0.0),
            (b'{"type": "content", "text": "part1"}\n', 0.05),
            (b'{"type": "content", "text": "part2"}\n', 0.05),
            (b'{"type": "end"}\n', 0.05),
        ]
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            mock_process.stdout = MockStreamReaderWithDelays(chunks)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_process.kill = AsyncMock()
            mock_subprocess.return_value = mock_process
            
            # Use a 0.5 second timeout - data arrives faster than this
            results = []
            async for obj in wrap_claude_code_stream("test", timeout=0.5):
                results.append(obj)
            
            # All data should be received without timeout
            assert len(results) == 4
            assert results[0]["type"] == "start"
            assert results[-1]["type"] == "end"

    @pytest.mark.asyncio
    async def test_timeout_with_long_idle_gap(self):
        """Test that a long gap between data chunks triggers idle timeout."""
        # First chunk arrives, then a long delay exceeding timeout
        chunks = [
            (b'{"type": "start"}\n', 0.0),
            (b'{"type": "end"}\n', 0.5),  # This delay exceeds the 0.2s timeout
        ]
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            mock_process.stdout = MockStreamReaderWithDelays(chunks)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_process.kill = AsyncMock()
            mock_subprocess.return_value = mock_process
            
            # Use a 0.2 second timeout - second chunk arrives after 0.5s
            with pytest.raises(ClaudeCodeTimeoutError) as exc_info:
                results = []
                async for obj in wrap_claude_code_stream("test", timeout=0.2):
                    results.append(obj)
            
            # First chunk should have been received before timeout
            assert len(results) == 1
            assert results[0]["type"] == "start"
            
            # Error message should indicate idle timeout
            assert "idle timeout" in str(exc_info.value).lower()
            
            # Process should have been killed
            mock_process.kill.assert_called_once()

    @pytest.mark.asyncio
    async def test_timeout_resets_on_each_data_received(self):
        """Test that timeout resets each time data is received.
        
        This is the key test for idle timeout: if each gap is shorter than
        the timeout, even if total time exceeds timeout, no error should occur.
        """
        # Each gap is 0.15s, timeout is 0.3s - no single gap exceeds timeout
        # But total time is 0.6s which would exceed a total timeout of 0.3s
        chunks = [
            (b'{"type": "1"}\n', 0.0),
            (b'{"type": "2"}\n', 0.15),  # Gap: 0.15s < 0.3s timeout
            (b'{"type": "3"}\n', 0.15),  # Gap: 0.15s < 0.3s timeout
            (b'{"type": "4"}\n', 0.15),  # Gap: 0.15s < 0.3s timeout
            (b'{"type": "5"}\n', 0.15),  # Gap: 0.15s < 0.3s timeout
        ]
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            mock_process.stdout = MockStreamReaderWithDelays(chunks)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_process.kill = AsyncMock()
            mock_subprocess.return_value = mock_process
            
            # Timeout is 0.3s per gap, total time ~0.6s
            results = []
            async for obj in wrap_claude_code_stream("test", timeout=0.3):
                results.append(obj)
            
            # All 5 chunks should be received (idle timeout resets on each)
            assert len(results) == 5
            
            # Process should NOT have been killed
            mock_process.kill.assert_not_called()

    @pytest.mark.asyncio
    async def test_no_timeout_when_timeout_is_zero(self):
        """Test that timeout=0 disables timeout entirely."""
        # Long delay that would normally trigger timeout
        chunks = [
            (b'{"type": "start"}\n', 0.0),
            (b'{"type": "end"}\n', 0.3),  # Would timeout if timeout were 0.1s
        ]
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            mock_process.stdout = MockStreamReaderWithDelays(chunks)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_process.kill = AsyncMock()
            mock_subprocess.return_value = mock_process
            
            # timeout=0 means no timeout
            results = []
            async for obj in wrap_claude_code_stream("test", timeout=0):
                results.append(obj)
            
            # Should receive all data despite the delay
            assert len(results) == 2
            mock_process.kill.assert_not_called()

    @pytest.mark.asyncio
    async def test_partial_data_preserved_on_timeout(self):
        """Test that data received before timeout is preserved."""
        chunks = [
            (b'{"type": "first"}\n', 0.0),
            (b'{"type": "second"}\n', 0.05),
            (b'{"type": "third"}\n', 0.5),  # This delay exceeds timeout
        ]
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            mock_process.stdout = MockStreamReaderWithDelays(chunks)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_process.kill = AsyncMock()
            mock_subprocess.return_value = mock_process
            
            # First two chunks arrive quickly, third has long delay
            results = []
            with pytest.raises(ClaudeCodeTimeoutError):
                async for obj in wrap_claude_code_stream("test", timeout=0.2):
                    results.append(obj)
            
            # First two chunks should have been received
            assert len(results) == 2
            assert results[0]["type"] == "first"
            assert results[1]["type"] == "second"

    @pytest.mark.asyncio
    async def test_timeout_on_initial_delay(self):
        """Test that timeout triggers if no data arrives initially."""
        # Long delay before any data
        chunks = [
            (b'{"type": "data"}\n', 0.5),  # Delay before first data exceeds timeout
        ]
        
        with patch('src.cc_wrap.asyncio.create_subprocess_exec') as mock_subprocess:
            mock_process = AsyncMock()
            mock_process.stdout = MockStreamReaderWithDelays(chunks)
            mock_process.wait = AsyncMock(return_value=0)
            mock_process.stderr.read = AsyncMock(return_value=b'')
            mock_process.kill = AsyncMock()
            mock_subprocess.return_value = mock_process
            
            with pytest.raises(ClaudeCodeTimeoutError):
                results = []
                async for obj in wrap_claude_code_stream("test", timeout=0.2):
                    results.append(obj)
            
            # No data should have been received
            assert len(results) == 0
            mock_process.kill.assert_called_once()
