import asyncio
import json
import logging
import sys
from typing import List, Dict, Any, AsyncIterator, Tuple, Optional

logger = logging.getLogger(__name__)

# Default timeout for Claude Code invocations (in seconds)
# Can be overridden via timeout parameter
DEFAULT_TIMEOUT = 600  # 10 minutes


class ClaudeCodeTimeoutError(Exception):
    """Raised when Claude Code invocation times out."""
    pass


def _build_claude_command(
    prompt: str,
    model: str = None,
    session_id: Optional[str] = None,
    **kwargs
) -> List[str]:
    """Build the claude CLI command with all arguments.
    
    Args:
        prompt: The prompt to send to claude
        model: The model to use (e.g., "haiku", "sonnet", "opus")
        session_id: Optional session ID to resume an existing session
        **kwargs: Additional arguments to pass to claude command
        
    Returns:
        List of command arguments
    """
    cmd = [
        "claude",
        "-p",  # headless/print mode
        "--output-format", "stream-json",
        "--verbose",
    ]

    if model:
        cmd.extend(["--model", model])

    # Add --resume flag if session_id is provided
    if session_id is not None:
        cmd.extend(["--resume", session_id])

    cmd.append(prompt)

    # Add any additional kwargs as command-line arguments
    for key, value in kwargs.items():
        if key == "timeout":
            # timeout is handled separately, not passed to CLI
            continue
        if value is True:
            cmd.append(f"--{key.replace('_', '-')}")
        elif value is not False and value is not None:
            cmd.append(f"--{key.replace('_', '-')}")
            cmd.append(str(value))
    
    return cmd


async def wrap_claude_code(
    prompt: str, 
    model: str = None, 
    session_id: Optional[str] = None,
    timeout: Optional[float] = None,
    **kwargs
) -> Tuple[List[Dict[str, Any]], Optional[str]]:
    """
    Wraps claude code invocation in headless mode with stream-json output.
    This is an async function that can be run concurrently with other instances.

    Args:
        prompt: The prompt to send to claude
        model: The model to use (e.g., "haiku", "sonnet", "opus")
        session_id: Optional session ID to resume an existing session (passes --resume flag)
        timeout: Optional timeout in seconds (default: 600). Set to None for no timeout.
        **kwargs: Additional arguments to pass to claude command

    Returns:
        Tuple of (list of parsed JSON objects from the stream, extracted session_id or None)
        
    Raises:
        ClaudeCodeTimeoutError: If the command times out
        RuntimeError: If the command fails with non-zero exit code
    """
    # Build the command
    cmd = _build_claude_command(prompt, model, session_id, **kwargs)
    
    # Use default timeout if not specified
    effective_timeout = timeout if timeout is not None else DEFAULT_TIMEOUT

    # Run the command asynchronously and capture stdout
    process = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )

    results = []
    extracted_session_id = None

    async def read_output():
        nonlocal extracted_session_id
        # Read and parse the streamed JSON output line by line
        async for line in process.stdout:
            line = line.decode('utf-8').strip()
            if not line:
                continue

            try:
                parsed = json.loads(line)
                results.append(parsed)
                
                # Extract session_id from JSON objects if present
                # Claude Code may output session_id in various formats
                if isinstance(parsed, dict):
                    if "session_id" in parsed:
                        extracted_session_id = parsed["session_id"]
                    # Also check for nested session_id (e.g., in metadata)
                    elif "metadata" in parsed and isinstance(parsed["metadata"], dict):
                        if "session_id" in parsed["metadata"]:
                            extracted_session_id = parsed["metadata"]["session_id"]
            except json.JSONDecodeError as e:
                # If a line isn't valid JSON, log it but continue
                logger.warning(f"Failed to parse JSON line: {line}", exc_info=e)

    try:
        # Wait for output reading with timeout
        await asyncio.wait_for(read_output(), timeout=effective_timeout)
        
        # Wait for process to complete
        returncode = await asyncio.wait_for(
            process.wait(), 
            timeout=30  # Short timeout for cleanup after output is done
        )
    except asyncio.TimeoutError:
        # Kill the process on timeout
        logger.error(f"Claude Code timed out after {effective_timeout}s, killing process")
        process.kill()
        await process.wait()
        raise ClaudeCodeTimeoutError(
            f"Claude Code invocation timed out after {effective_timeout} seconds"
        )

    # Check for errors
    if returncode != 0:
        stderr_output = await process.stderr.read()
        stderr_text = stderr_output.decode('utf-8') if stderr_output else ""
        raise RuntimeError(
            f"Claude command failed with return code {returncode}\n"
            f"Stderr: {stderr_text}"
        )

    return results, extracted_session_id


async def wrap_claude_code_stream(
    prompt: str, 
    model: str = None,
    session_id: Optional[str] = None,
    timeout: Optional[float] = None,
    **kwargs
) -> AsyncIterator[Dict[str, Any]]:
    """
    Wraps claude code invocation and yields JSON objects as they arrive.
    This is an async generator that can be run concurrently with other instances.

    Args:
        prompt: The prompt to send to claude
        model: The model to use (e.g., "haiku", "sonnet", "opus")
        session_id: Optional session ID to resume an existing session (passes --resume flag)
        timeout: Optional timeout in seconds (default: 600). Set to None for no timeout.
        **kwargs: Additional arguments to pass to claude command

    Yields:
        Parsed JSON objects from the stream as they arrive
        
    Raises:
        ClaudeCodeTimeoutError: If the command times out
        RuntimeError: If the command fails with non-zero exit code
    """
    # Build the command
    cmd = _build_claude_command(prompt, model, session_id, **kwargs)
    
    # Use default timeout if not specified
    effective_timeout = timeout if timeout is not None else DEFAULT_TIMEOUT

    # Run the command asynchronously and capture stdout
    process = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )

    start_time = asyncio.get_event_loop().time()

    # Read and parse the streamed JSON output line by line
    try:
        async for line in process.stdout:
            # Check timeout
            elapsed = asyncio.get_event_loop().time() - start_time
            if elapsed > effective_timeout:
                logger.error(f"Claude Code timed out after {effective_timeout}s, killing process")
                process.kill()
                await process.wait()
                raise ClaudeCodeTimeoutError(
                    f"Claude Code invocation timed out after {effective_timeout} seconds"
                )
            
            line = line.decode('utf-8').strip()
            if not line:
                continue

            try:
                parsed = json.loads(line)
                yield parsed
            except json.JSONDecodeError as e:
                # If a line isn't valid JSON, log it but continue
                logger.warning(f"Failed to parse JSON line: {line}", exc_info=e)

        # Wait for process to complete
        returncode = await asyncio.wait_for(process.wait(), timeout=30)
    except asyncio.TimeoutError:
        logger.error(f"Claude Code cleanup timed out, killing process")
        process.kill()
        await process.wait()
        raise ClaudeCodeTimeoutError("Claude Code process cleanup timed out")

    # Check for errors
    if returncode != 0:
        stderr_output = await process.stderr.read()
        stderr_text = stderr_output.decode('utf-8') if stderr_output else ""
        raise RuntimeError(
            f"Claude command failed with return code {returncode}\n"
            f"Stderr: {stderr_text}"
        )
