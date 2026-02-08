"""Script execution infrastructure for shell script states.

This module provides async script execution for .sh (Unix) and .bat (Windows)
files, capturing stdout, stderr, and exit codes.
"""

import asyncio
import ctypes
import ctypes.util
import os
import signal
import sys
from dataclasses import dataclass
from pathlib import Path


# Track whether we've set up subreaper for this process
_subreaper_initialized = False


def _setup_subreaper() -> None:
    """Set up this process as a subreaper for orphaned descendants (Linux only).

    On Linux, when a child process dies, its children (grandchildren of this
    process) are normally re-parented to init (PID 1). By setting the
    PR_SET_CHILD_SUBREAPER flag, orphaned descendants are instead re-parented
    to this process, allowing us to reap them and prevent zombies.

    This is a no-op on non-Linux systems or if already initialized.
    """
    global _subreaper_initialized
    if _subreaper_initialized or not is_unix():
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
    if not is_unix():
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


async def _kill_process_group(process: asyncio.subprocess.Process) -> None:
    """Kill a subprocess and its entire process group, then reap zombies."""
    pgid = process.pid
    if is_unix() and pgid is not None:
        try:
            os.killpg(pgid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
        await process.wait()
        _reap_process_group(pgid)
    else:
        process.kill()
        await process.wait()


def build_script_env(
    workflow_id: str,
    agent_id: str,
    state_dir: str,
    state_file: str,
    result: str | None = None,
    fork_attributes: dict[str, str] | None = None
) -> dict[str, str]:
    """Build environment variables dict for script execution.

    Creates a dictionary of environment variables that provide workflow
    context to script states. This dict should be passed to run_script()
    via the env parameter.

    Args:
        workflow_id: The unique identifier for the workflow.
        agent_id: The identifier for the current agent.
        state_dir: The directory containing state files (scope_dir).
        state_file: The full path to the current state file being executed.
        result: Optional result payload from a <call> return. If provided,
            sets RAYMOND_RESULT environment variable.
        fork_attributes: Optional dict of attributes from a <fork> tag.
            Each key-value pair becomes an environment variable.

    Returns:
        Dictionary of environment variable names to values.

    Example:
        env = build_script_env(
            workflow_id="wf-123",
            agent_id="main",
            state_dir="/workflows/my_wf",
            state_file="/workflows/my_wf/CHECK.bat",
            result="task completed",
            fork_attributes={"item": "task1", "priority": "high"}
        )
        result = await run_script(script_path, env=env)
    """
    env = {
        "RAYMOND_WORKFLOW_ID": workflow_id,
        "RAYMOND_AGENT_ID": agent_id,
        "RAYMOND_STATE_DIR": state_dir,
        "RAYMOND_STATE_FILE": state_file,
    }

    # Add RAYMOND_RESULT if provided (including empty string)
    if result is not None:
        env["RAYMOND_RESULT"] = result

    # Add fork attributes as environment variables
    if fork_attributes:
        env.update(fork_attributes)

    return env


def is_windows() -> bool:
    """Check if running on Windows."""
    return sys.platform.startswith('win')


def is_unix() -> bool:
    """Check if running on Unix (Linux, macOS, etc.)."""
    return not is_windows()


@dataclass
class ScriptResult:
    """Result of script execution.

    Attributes:
        stdout: Standard output from the script.
        stderr: Standard error from the script.
        exit_code: Exit code of the script process.
    """
    stdout: str
    stderr: str
    exit_code: int


class ScriptTimeoutError(Exception):
    """Raised when a script execution times out.

    Attributes:
        script_path: Path to the script that timed out.
        timeout: The timeout value in seconds.
    """

    def __init__(self, script_path: str, timeout: float):
        self.script_path = script_path
        self.timeout = timeout
        super().__init__(
            f"Script timeout: '{script_path}' exceeded {timeout} seconds"
        )


async def run_script(
    script_path: str,
    timeout: float | None = None,
    env: dict[str, str] | None = None
) -> ScriptResult:
    """Execute a shell script asynchronously and capture its output.

    Runs .sh files with bash (Unix) or .bat files with cmd.exe (Windows).
    The script runs in the orchestrator's current working directory, not
    in the directory containing the script.

    Args:
        script_path: Path to the script file (absolute or relative).
        timeout: Maximum execution time in seconds. None for no timeout.
        env: Optional environment variables to pass to the script.
             If provided, these are added to (not replacing) the current environment.

    Returns:
        ScriptResult containing stdout, stderr, and exit code.

    Raises:
        ScriptTimeoutError: If the script exceeds the timeout.
        ValueError: If the script extension is not supported on the current platform.
        FileNotFoundError: If the script file doesn't exist.
    """
    # Set up subreaper on first call (Linux only) so we can reap orphaned
    # grandchildren that would otherwise become zombies
    _setup_subreaper()

    path = Path(script_path)

    if not path.exists():
        raise FileNotFoundError(f"Script not found: {script_path}")

    extension = path.suffix.lower()

    # Determine the shell command based on file extension and platform
    if extension == '.sh':
        if is_windows():
            raise ValueError(
                f"Cannot execute .sh file on Windows: {script_path}. "
                "Use .bat files on Windows."
            )
        # Use bash to execute .sh files
        cmd = ['bash', str(path)]

    elif extension == '.bat':
        if is_unix():
            raise ValueError(
                f"Cannot execute .bat file on Unix: {script_path}. "
                "Use .sh files on Unix."
            )
        # Use cmd.exe to execute .bat files
        cmd = ['cmd.exe', '/c', str(path)]

    else:
        raise ValueError(f"Unsupported script extension: {extension}")

    # Prepare environment
    process_env = os.environ.copy()
    if env:
        process_env.update(env)

    # Create the subprocess
    # Note: We don't set cwd, so the script runs in the orchestrator's directory
    # On Unix, start_new_session=True creates a new process group so we can
    # kill the entire group (including child processes) on timeout
    process = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        env=process_env,
        start_new_session=is_unix(),
    )

    try:
        # Wait for completion with optional timeout
        stdout_bytes, stderr_bytes = await asyncio.wait_for(
            process.communicate(),
            timeout=timeout
        )
    except asyncio.TimeoutError:
        await _kill_process_group(process)
        raise ScriptTimeoutError(script_path, timeout)
    except BaseException:
        # Any other exception (CancelledError, KeyboardInterrupt, etc.)
        # — kill the process group so child processes don't leak
        await _kill_process_group(process)
        raise
    finally:
        # Last-resort cleanup: if the process is somehow still alive
        # (e.g., await failed in an except handler during shutdown),
        # at least send the kill signal synchronously
        if process.returncode is None:
            try:
                pgid = process.pid
                if is_unix() and pgid is not None:
                    os.killpg(pgid, signal.SIGKILL)
                else:
                    process.kill()
            except (ProcessLookupError, PermissionError, OSError):
                pass

    # Decode output
    stdout = stdout_bytes.decode('utf-8', errors='replace')
    stderr = stderr_bytes.decode('utf-8', errors='replace')

    return ScriptResult(
        stdout=stdout,
        stderr=stderr,
        exit_code=process.returncode
    )
