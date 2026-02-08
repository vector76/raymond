import asyncio
import ctypes
import ctypes.util
import json
import logging
import os
import signal
import sys
from typing import List, Dict, Any, AsyncIterator, Tuple, Optional

logger = logging.getLogger(__name__)

# Default timeout for Claude Code invocations (in seconds)
# Can be overridden via timeout parameter
DEFAULT_TIMEOUT = 600  # 10 minutes

# Track whether we've set up subreaper for this process
_subreaper_initialized = False


def _is_unix() -> bool:
    """Check if running on Unix (Linux, macOS, etc.)."""
    return not sys.platform.startswith('win')


def _setup_subreaper() -> None:
    """Set up this process as a subreaper for orphaned descendants (Linux only).

    On Linux, when a child process dies, its children (grandchildren of this
    process) are normally re-parented to init (PID 1). By setting the
    PR_SET_CHILD_SUBREAPER flag, orphaned descendants are instead re-parented
    to this process, allowing us to reap them and prevent zombies.

    This is a no-op on non-Linux systems or if already initialized.
    """
    global _subreaper_initialized
    if _subreaper_initialized or not _is_unix():
        return

    # PR_SET_CHILD_SUBREAPER is Linux-specific
    PR_SET_CHILD_SUBREAPER = 36

    try:
        libc = ctypes.CDLL(ctypes.util.find_library('c'), use_errno=True)
        result = libc.prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
        if result == 0:
            _subreaper_initialized = True
    except (OSError, AttributeError):
        # prctl not available (non-Linux) or library not found
        pass


def _reap_process_group(pgid: int) -> int:
    """Reap zombie processes from a specific process group.

    After killing a process group, orphaned grandchildren may become zombies.
    This function reaps only zombies that were in the specified process group,
    avoiding interference with other child processes (like pytest workers).

    Args:
        pgid: The process group ID to reap zombies from.

    Returns:
        Number of zombies reaped.
    """
    if not _is_unix():
        return 0

    reaped = 0
    # Use waitid with P_PGID to only wait for processes in the specific group.
    # This is safer than waitpid(-1) which would reap ANY child.
    while True:
        try:
            result = os.waitid(os.P_PGID, pgid, os.WEXITED | os.WNOHANG)
            if result is None:
                # No more children in this process group to reap
                break
            reaped += 1
        except ChildProcessError:
            # No child processes in this group
            break
        except OSError:
            # Process group doesn't exist or other error
            break
    return reaped


def _kill_process_tree(process: asyncio.subprocess.Process) -> int | None:
    """Kill a process and all its descendants on Unix, or just the process on Windows.

    Uses process group kill on Unix to ensure child processes are also terminated.

    Returns:
        The process group ID on Unix (for later reaping), None on Windows.
    """
    pgid = process.pid
    if _is_unix() and pgid is not None:
        try:
            os.killpg(pgid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            # Process group may already be gone
            pass
        return pgid
    else:
        process.kill()
        return None


class ClaudeCodeTimeoutError(Exception):
    """Raised when Claude Code invocation times out."""
    pass


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

    cmd.append(prompt)

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
        **kwargs: Additional arguments to pass to claude command.
            Supported kwargs include:
            - fork (bool): If True, passes --fork-session flag to branch from session_id

    Returns:
        Tuple of (list of parsed JSON objects from the stream, extracted session_id or None)
        
    Raises:
        ClaudeCodeTimeoutError: If the total timeout is exceeded
        RuntimeError: If the command fails with non-zero exit code
    """
    # Set up subreaper on first call (Linux only) so we can reap orphaned
    # grandchildren that would otherwise become zombies
    _setup_subreaper()

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
    # On Unix, start_new_session=True creates a new process group so we can
    # kill the entire group (including child processes) on timeout
    process = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        start_new_session=_is_unix(),
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
        # Kill the process on timeout
        # On Unix, kill the entire process group to clean up child processes
        logger.error(f"Claude Code timed out after {effective_timeout}s, killing process")
        pgid = _kill_process_tree(process)
        await process.wait()
        # Reap any orphaned grandchildren from this specific process group
        if pgid is not None:
            _reap_process_group(pgid)
        raise ClaudeCodeTimeoutError(
            f"Claude Code invocation timed out after {effective_timeout} seconds"
        )
    except BaseException:
        # Any other exception (CancelledError, KeyboardInterrupt, etc.)
        # — kill the process group so child processes don't leak
        if process.returncode is None:
            pgid = _kill_process_tree(process)
            await process.wait()
            if pgid is not None:
                _reap_process_group(pgid)
        raise
    finally:
        # Last-resort cleanup: if the process is somehow still alive
        # (e.g., await failed in an except handler during shutdown),
        # at least send the kill signal synchronously
        if process.returncode is None:
            try:
                pgid = process.pid
                if _is_unix() and pgid is not None:
                    os.killpg(pgid, signal.SIGKILL)
                else:
                    process.kill()
            except (ProcessLookupError, PermissionError, OSError):
                pass

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
        **kwargs: Additional arguments to pass to claude command.
            Supported kwargs include:
            - fork (bool): If True, passes --fork-session flag to branch from session_id

    Yields:
        Parsed JSON objects from the stream as they arrive
        
    Raises:
        ClaudeCodeTimeoutError: If idle timeout is exceeded (no data received for timeout seconds)
        RuntimeError: If the command fails with non-zero exit code
    """
    # Set up subreaper on first call (Linux only) so we can reap orphaned
    # grandchildren that would otherwise become zombies
    _setup_subreaper()

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
    # On Unix, start_new_session=True creates a new process group so we can
    # kill the entire group (including child processes) on timeout
    process = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        start_new_session=_is_unix(),
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
                    pgid = _kill_process_tree(process)
                    await process.wait()
                    # Reap any orphaned grandchildren from this specific process group
                    if pgid is not None:
                        _reap_process_group(pgid)
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
        # Kill the process group immediately — no graceful terminate, because
        # we're shutting down and the 2s grace period blocks CTRL-C responsiveness
        if process.returncode is None:
            logger.debug("Cleaning up Claude Code process due to generator close")
            try:
                pgid = _kill_process_tree(process)
                await process.wait()
                if pgid is not None:
                    _reap_process_group(pgid)
            except Exception as e:
                logger.debug(f"Error during process cleanup on generator close: {e}")
        raise
    except asyncio.TimeoutError:
        logger.error(f"Claude Code cleanup timed out, killing process")
        pgid = _kill_process_tree(process)
        await process.wait()
        if pgid is not None:
            _reap_process_group(pgid)
        raise ClaudeCodeTimeoutError("Claude Code process cleanup timed out")
    finally:
        # Ensure process is cleaned up even if exception occurs during iteration
        # This handles cases where the generator is interrupted (e.g., limit errors)
        if process.returncode is None:
            try:
                pgid = _kill_process_tree(process)
                await process.wait()
                if pgid is not None:
                    _reap_process_group(pgid)
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
