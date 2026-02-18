import asyncio
import json
import logging
import os
from typing import List, Dict, Any, AsyncIterator, Tuple, Optional

logger = logging.getLogger(__name__)

# Default timeout for Claude Code invocations (in seconds)
# Can be overridden via timeout parameter
DEFAULT_TIMEOUT = 600  # 10 minutes

# Tools that raymond-managed agents must never use (they are orchestrator-level concerns)
DISALLOWED_TOOLS = ["EnterPlanMode", "ExitPlanMode", "AskUserQuestion", "NotebookEdit"]


class ClaudeCodeTimeoutError(Exception):
    """Raised when Claude Code invocation times out."""
    pass


def _build_claude_env() -> dict[str, str]:
    """Build environment for Claude Code subprocesses.

    Strips the CLAUDECODE env var so that claude doesn't think it's being
    nested inside another Claude Code session (raymond intentionally launches
    claude as a managed subprocess, not as a nested session).
    """
    env = os.environ.copy()
    env.pop("CLAUDECODE", None)
    return env


def _build_claude_command(
    prompt: str,
    model: Optional[str] = None,
    session_id: Optional[str] = None,
    dangerously_skip_permissions: bool = False,
    **kwargs
) -> List[str]:
    """Build the claude CLI command with all arguments.
    
    Args:
        prompt: The prompt to send to claude
        model: The model to use (e.g., "haiku", "sonnet", "opus")
        session_id: Optional session ID to resume an existing session
        dangerously_skip_permissions: If True, passes --dangerously-skip-permissions
            instead of --permission-mode acceptEdits. WARNING: This allows Claude
            to execute any action without prompting for permission.
        **kwargs: Additional arguments to pass to claude command.
            Supported kwargs include:
            - fork (bool): If True, passes --fork-session flag to branch from session_id
            - Any other kwargs are converted to --key value CLI arguments
        
    Returns:
        List of command arguments
    """
    cmd = [
        "claude",
        "-p",  # headless/print mode
        "--output-format", "stream-json",
        "--verbose",
    ]
    
    # Add permission mode: either --dangerously-skip-permissions or --permission-mode acceptEdits
    if dangerously_skip_permissions:
        cmd.append("--dangerously-skip-permissions")
    else:
        cmd.extend(["--permission-mode", "acceptEdits"])

    if model:
        cmd.extend(["--model", model])

    # Add --resume flag if session_id is provided
    if session_id is not None:
        cmd.extend(["--resume", session_id])

    # Unconditionally disallow orchestrator-level tools from managed agents
    cmd.extend(["--disallowed-tools", ",".join(DISALLOWED_TOOLS)])

    cmd.extend(["--", prompt])

    # Add any additional kwargs as command-line arguments
    for key, value in kwargs.items():
        if key == "timeout":
            # timeout is handled separately, not passed to CLI
            continue
        if key == "fork":
            # fork is converted to --fork-session flag
            if value is True:
                cmd.append("--fork-session")
            continue
        if value is True:
            cmd.append(f"--{key.replace('_', '-')}")
        elif value is not False and value is not None:
            cmd.append(f"--{key.replace('_', '-')}")
            cmd.append(str(value))
    
    return cmd


async def wrap_claude_code(
    prompt: str,
    model: Optional[str] = None,
    session_id: Optional[str] = None,
    timeout: Optional[float] = None,
    dangerously_skip_permissions: bool = False,
    cwd: Optional[str] = None,
    **kwargs
) -> Tuple[List[Dict[str, Any]], Optional[str]]:
    """
    Wraps claude code invocation in headless mode with stream-json output.
    This is an async function that can be run concurrently with other instances.
    
    NOTE: For production use, prefer wrap_claude_code_stream() which:
    - Enables progressive output processing (debug files, console output)
    - Uses idle timeout (resets on each data received) instead of total timeout
    - Allows detection of "stuck" executions while supporting long-running tasks
    
    This non-streaming version is retained for simpler test cases and backwards
    compatibility, but uses a TOTAL timeout rather than idle timeout.

    Args:
        prompt: The prompt to send to claude
        model: The model to use (e.g., "haiku", "sonnet", "opus")
        session_id: Optional session ID to resume an existing session (passes --resume flag)
        timeout: Optional total timeout in seconds (default: 600). Set to 0 for no timeout.
            WARNING: This is a TOTAL timeout, not an idle timeout. For long-running
            tasks that may exceed this duration, use wrap_claude_code_stream() instead.
        dangerously_skip_permissions: If True, passes --dangerously-skip-permissions
            instead of --permission-mode acceptEdits. WARNING: This allows Claude
            to execute any action without prompting for permission.
        cwd: Optional working directory for the subprocess. If None, inherits the
            orchestrator's working directory.
        **kwargs: Additional arguments to pass to claude command.
            Supported kwargs include:
            - fork (bool): If True, passes --fork-session flag to branch from session_id

    Returns:
        Tuple of (list of parsed JSON objects from the stream, extracted session_id or None)
        
    Raises:
        ClaudeCodeTimeoutError: If the total timeout is exceeded
        RuntimeError: If the command fails with non-zero exit code
    """
    # Build the command
    cmd = _build_claude_command(prompt, model, session_id, dangerously_skip_permissions, **kwargs)

    # Use default timeout if not specified
    # timeout=0 means no timeout (None for asyncio.wait_for)
    # timeout=None means use default
    if timeout == 0:
        effective_timeout = None
    elif timeout is not None:
        effective_timeout = timeout
    else:
        effective_timeout = DEFAULT_TIMEOUT

    # Run the command asynchronously and capture stdout
    # stdin=DEVNULL prevents the child from inheriting the terminal's stdin,
    # which would allow it to put the terminal in raw mode and disable SIGINT
    # generation from CTRL-C.
    process = await asyncio.create_subprocess_exec(
        *cmd,
        stdin=asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        env=_build_claude_env(),
        cwd=cwd,
    )

    results = []
    extracted_session_id = None

    async def read_output():
        nonlocal extracted_session_id
        # Read in chunks to handle arbitrarily long lines
        # (default asyncio readline has a 64KB limit that can be exceeded by Claude Code)
        buffer = b""
        chunk_size = 1024 * 1024  # 1MB chunks
        
        while True:
            chunk = await process.stdout.read(chunk_size)
            if not chunk:
                # Process any remaining data in buffer
                if buffer:
                    line = buffer.decode('utf-8').strip()
                    if line:
                        try:
                            parsed = json.loads(line)
                            results.append(parsed)
                            if isinstance(parsed, dict):
                                if "session_id" in parsed:
                                    extracted_session_id = parsed["session_id"]
                                elif "metadata" in parsed and isinstance(parsed["metadata"], dict):
                                    if "session_id" in parsed["metadata"]:
                                        extracted_session_id = parsed["metadata"]["session_id"]
                        except json.JSONDecodeError as e:
                            logger.warning(f"Failed to parse JSON line: {line}", exc_info=e)
                break
            
            buffer += chunk
            
            # Process complete lines from buffer
            while b'\n' in buffer:
                line_bytes, buffer = buffer.split(b'\n', 1)
                line = line_bytes.decode('utf-8').strip()
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
        logger.error(f"Claude Code timed out after {effective_timeout}s, killing process")
        process.kill()
        await process.wait()
        raise ClaudeCodeTimeoutError(
            f"Claude Code invocation timed out after {effective_timeout} seconds"
        )
    except BaseException:
        # Any other exception (CancelledError, KeyboardInterrupt, etc.)
        if process.returncode is None:
            process.kill()
            await process.wait()
        raise
    finally:
        # Last-resort cleanup: if the process is somehow still alive
        if process.returncode is None:
            try:
                process.kill()
                await process.wait()
            except Exception as e:
                logger.debug(f"Error during process cleanup: {e}")

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
    model: Optional[str] = None,
    session_id: Optional[str] = None,
    timeout: Optional[float] = None,
    dangerously_skip_permissions: bool = False,
    cwd: Optional[str] = None,
    **kwargs
) -> AsyncIterator[Dict[str, Any]]:
    """
    Wraps claude code invocation and yields JSON objects as they arrive.
    This is an async generator that can be run concurrently with other instances.
    
    This is the PREFERRED way to invoke Claude Code as it:
    - Enables progressive output processing (debug files, console output)
    - Uses idle timeout (resets on each data received) instead of total timeout
    - Allows detection of "stuck" executions while supporting long-running tasks

    Args:
        prompt: The prompt to send to claude
        model: The model to use (e.g., "haiku", "sonnet", "opus")
        session_id: Optional session ID to resume an existing session (passes --resume flag)
        timeout: Optional idle timeout in seconds (default: 600). This is the maximum
            time to wait between receiving data chunks. Set to 0 for no timeout.
            Note: This is an IDLE timeout, not a total timeout. Long-running Claude
            Code executions will not timeout as long as they continue producing output.
        dangerously_skip_permissions: If True, passes --dangerously-skip-permissions
            instead of --permission-mode acceptEdits. WARNING: This allows Claude
            to execute any action without prompting for permission.
        cwd: Optional working directory for the subprocess. If None, inherits the
            orchestrator's working directory.
        **kwargs: Additional arguments to pass to claude command.
            Supported kwargs include:
            - fork (bool): If True, passes --fork-session flag to branch from session_id

    Yields:
        Parsed JSON objects from the stream as they arrive
        
    Raises:
        ClaudeCodeTimeoutError: If idle timeout is exceeded (no data received for timeout seconds)
        RuntimeError: If the command fails with non-zero exit code
    """
    # Build the command
    cmd = _build_claude_command(prompt, model, session_id, dangerously_skip_permissions, **kwargs)

    # Use default timeout if not specified
    # timeout=0 means no timeout (None)
    # timeout=None means use default
    if timeout == 0:
        idle_timeout = None
    elif timeout is not None:
        idle_timeout = timeout
    else:
        idle_timeout = DEFAULT_TIMEOUT

    # Run the command asynchronously and capture stdout
    # stdin=DEVNULL prevents the child from inheriting the terminal's stdin,
    # which would allow it to put the terminal in raw mode and disable SIGINT
    # generation from CTRL-C.
    process = await asyncio.create_subprocess_exec(
        *cmd,
        stdin=asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        env=_build_claude_env(),
        cwd=cwd,
    )

    loop = asyncio.get_running_loop()
    last_data_time = loop.time()  # Track time of last data received for idle timeout

    # Read in chunks to handle arbitrarily long lines
    # (default asyncio readline has a 64KB limit that can be exceeded by Claude Code)
    buffer = b""
    chunk_size = 1024 * 1024  # 1MB chunks
    returncode = None
    
    try:
        while True:
            # Calculate remaining idle time for this read
            if idle_timeout is not None:
                elapsed_idle = loop.time() - last_data_time
                remaining_idle = idle_timeout - elapsed_idle
                
                if remaining_idle <= 0:
                    logger.error(
                        f"Claude Code idle timeout: no data received for {idle_timeout}s, killing process"
                    )
                    process.kill()
                    await process.wait()
                    raise ClaudeCodeTimeoutError(
                        f"Claude Code idle timeout: no data received for {idle_timeout} seconds"
                    )
                
                # Use remaining idle time as read timeout, capped at a reasonable max
                # to allow periodic idle timeout checks
                read_timeout = min(remaining_idle, 60.0)
            else:
                read_timeout = None  # No timeout
            
            try:
                if read_timeout is not None:
                    chunk = await asyncio.wait_for(
                        process.stdout.read(chunk_size),
                        timeout=read_timeout
                    )
                else:
                    chunk = await process.stdout.read(chunk_size)
            except asyncio.TimeoutError:
                # Read timed out - loop will check idle timeout on next iteration
                continue
            
            if not chunk:
                # EOF - process any remaining data in buffer
                if buffer:
                    line = buffer.decode('utf-8').strip()
                    if line:
                        try:
                            parsed = json.loads(line)
                            yield parsed
                        except json.JSONDecodeError as e:
                            logger.warning(f"Failed to parse final JSON line: {line}", exc_info=e)
                break
            
            # Data received - reset idle timer
            last_data_time = loop.time()
            buffer += chunk
            
            # Process complete lines from buffer
            while b'\n' in buffer:
                line_bytes, buffer = buffer.split(b'\n', 1)
                line = line_bytes.decode('utf-8').strip()
                if not line:
                    continue

                try:
                    parsed = json.loads(line)
                    yield parsed
                except json.JSONDecodeError as e:
                    # If a line isn't valid JSON, log it but continue
                    logger.warning(f"Failed to parse JSON line: {line}", exc_info=e)

        # Wait for process to complete (only reached if loop exits normally)
        returncode = await asyncio.wait_for(process.wait(), timeout=30)
    except GeneratorExit:
        # Generator is being closed (e.g., due to cancellation or CTRL-C)
        if process.returncode is None:
            logger.debug("Cleaning up Claude Code process due to generator close")
            try:
                process.kill()
                await process.wait()
            except Exception as e:
                logger.debug(f"Error during process cleanup on generator close: {e}")
        raise
    except asyncio.TimeoutError:
        logger.error(f"Claude Code cleanup timed out, killing process")
        process.kill()
        await process.wait()
        raise ClaudeCodeTimeoutError("Claude Code process cleanup timed out")
    finally:
        # Ensure process is cleaned up even if exception occurs during iteration
        if process.returncode is None:
            try:
                process.kill()
                await process.wait()
            except Exception as e:
                # Log but don't raise - we're in cleanup
                logger.debug(f"Error during process cleanup: {e}")
    
    # Check for errors (only if we got a returncode from normal completion)
    if returncode is not None and returncode != 0:
        stderr_output = await process.stderr.read()
        stderr_text = stderr_output.decode('utf-8') if stderr_output else ""
        raise RuntimeError(
            f"Claude command failed with return code {returncode}\n"
            f"Stderr: {stderr_text}"
        )
