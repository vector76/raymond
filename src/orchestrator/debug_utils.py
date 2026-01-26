"""Debug utilities for the orchestrator.

This module contains functions for saving debug output during workflow execution,
including Claude Code responses, script output, error information, and transition logs.

These utilities are used by the executors, observers, and workflow module to capture
debug information for troubleshooting and analysis.
"""

import json
import logging
import traceback
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, List, Optional

# Import src.orchestrator as a module to support test patching
import src.orchestrator as orchestrator

logger = logging.getLogger(__name__)


def create_debug_directory(workflow_id: str, state_dir: Optional[str] = None) -> Optional[Path]:
    """Create debug directory for workflow execution.

    Args:
        workflow_id: Workflow identifier
        state_dir: Optional custom state directory (used to determine .raymond location)

    Returns:
        Path to debug directory, or None if creation fails
    """
    # Determine base directory (same parent as state_dir)
    state_path = orchestrator.get_state_dir(state_dir)
    base_dir = state_path.parent  # .raymond/

    # Generate timestamp
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")

    # Create debug directory path
    debug_dir = base_dir / "debug" / f"{workflow_id}_{timestamp}"

    try:
        debug_dir.mkdir(parents=True, exist_ok=True)
        return debug_dir
    except OSError as e:
        logger.warning(f"Failed to create debug directory: {e}")
        return None


def save_claude_output(
    debug_dir: Path,
    agent_id: str,
    state_name: str,
    step_number: int,
    results: List[Dict[str, Any]]
) -> None:
    """Save Claude Code JSON output to debug directory (non-streaming version).

    NOTE: For production use, prefer the streaming functions below which write
    progressively as data arrives, preventing data loss on crashes/timeouts.

    Args:
        debug_dir: Debug directory path
        agent_id: Agent identifier
        state_name: State name (filename without .md)
        step_number: Step number for this agent
        results: Raw JSON results from Claude Code
    """
    filename = f"{agent_id}_{state_name}_{step_number:03d}.json"
    filepath = debug_dir / filename

    try:
        with open(filepath, 'w', encoding='utf-8') as f:
            json.dump(results, f, indent=2, ensure_ascii=False)
    except OSError as e:
        logger.warning(f"Failed to save Claude output to {filepath}: {e}")


def get_claude_output_filepath(
    debug_dir: Path,
    agent_id: str,
    state_name: str,
    step_number: int
) -> Path:
    """Get the filepath for a Claude Code JSONL output file.

    Args:
        debug_dir: Debug directory path
        agent_id: Agent identifier
        state_name: State name (filename without extension)
        step_number: Step number for this agent

    Returns:
        Path to the JSONL file
    """
    filename = f"{agent_id}_{state_name}_{step_number:03d}.jsonl"
    return debug_dir / filename


def append_claude_output_line(
    filepath: Path,
    json_object: Dict[str, Any]
) -> None:
    """Append a single JSON object to a JSONL debug file.

    This function is used for progressive/streaming writes where each JSON
    object from Claude Code is written immediately as it arrives. This ensures
    debug data is preserved even if the process crashes or times out.

    Args:
        filepath: Path to the JSONL file (will be created if doesn't exist)
        json_object: Single JSON object to append

    Note:
        Each object is written on its own line with no trailing comma.
        The file uses JSONL (JSON Lines) format, not a JSON array.
    """
    try:
        with open(filepath, 'a', encoding='utf-8') as f:
            f.write(json.dumps(json_object, ensure_ascii=False) + '\n')
    except OSError as e:
        logger.warning(f"Failed to append Claude output to {filepath}: {e}")


def save_script_output(
    debug_dir: Path,
    agent_id: str,
    state_name: str,
    step_number: int,
    stdout: str,
    stderr: str
) -> None:
    """Save script execution output to debug directory.

    Creates separate .stdout.txt and .stderr.txt files for each script execution.

    Args:
        debug_dir: Debug directory path
        agent_id: Agent identifier
        state_name: State name (filename without extension)
        step_number: Step number for this agent
        stdout: Script stdout output
        stderr: Script stderr output
    """
    base_filename = f"{agent_id}_{state_name}_{step_number:03d}"
    stdout_filepath = debug_dir / f"{base_filename}.stdout.txt"
    stderr_filepath = debug_dir / f"{base_filename}.stderr.txt"

    try:
        with open(stdout_filepath, 'w', encoding='utf-8') as f:
            f.write(stdout)
    except OSError as e:
        logger.warning(f"Failed to save script stdout to {stdout_filepath}: {e}")

    try:
        with open(stderr_filepath, 'w', encoding='utf-8') as f:
            f.write(stderr)
    except OSError as e:
        logger.warning(f"Failed to save script stderr to {stderr_filepath}: {e}")


def save_script_output_metadata(
    debug_dir: Path,
    agent_id: str,
    state_name: str,
    step_number: int,
    exit_code: int,
    execution_time_ms: float,
    env_vars: Dict[str, str]
) -> None:
    """Save script execution metadata to debug directory.

    Creates a .meta.json file for each script execution containing
    execution time, exit code, and environment variables.

    Args:
        debug_dir: Debug directory path
        agent_id: Agent identifier
        state_name: State name (filename without extension)
        step_number: Step number for this agent
        exit_code: Script exit code
        execution_time_ms: Script execution time in milliseconds
        env_vars: Environment variables passed to the script
    """
    base_filename = f"{agent_id}_{state_name}_{step_number:03d}"
    metadata_filepath = debug_dir / f"{base_filename}.meta.json"

    metadata = {
        "exit_code": exit_code,
        "execution_time_ms": execution_time_ms,
        "env_vars": env_vars
    }

    try:
        with open(metadata_filepath, 'w', encoding='utf-8') as f:
            json.dump(metadata, f, indent=2, ensure_ascii=False)
    except OSError as e:
        logger.warning(f"Failed to save script metadata to {metadata_filepath}: {e}")


def log_state_transition(
    debug_dir: Optional[Path],
    timestamp: datetime,
    agent_id: str,
    old_state: str,
    new_state: Optional[str],
    transition_type: str,
    transition_target: Optional[str],
    metadata: Dict[str, Any]
) -> None:
    """Log state transition to transitions.log file.

    Args:
        debug_dir: Debug directory path (None if debug disabled)
        timestamp: Transition timestamp
        agent_id: Agent identifier
        old_state: Previous state filename
        new_state: New state filename (None if agent terminated)
        transition_type: Type of transition (goto, reset, function, call, fork, result)
        transition_target: Transition target filename
        metadata: Additional metadata (session_id, cost, stack_depth, etc.)
    """
    if debug_dir is None:
        return

    log_file = debug_dir / "transitions.log"

    try:
        with open(log_file, 'a', encoding='utf-8') as f:
            # Format log entry
            if new_state:
                f.write(f"{timestamp.isoformat()} [{agent_id}] {old_state} -> {new_state} ({transition_type})\n")
            else:
                f.write(f"{timestamp.isoformat()} [{agent_id}] {old_state} -> (result, terminated)\n")

            # Write metadata
            for key, value in metadata.items():
                f.write(f"  {key}: {value}\n")

            f.write("\n")
    except OSError as e:
        logger.warning(f"Failed to write to transitions.log: {e}")


def save_error_response(
    workflow_id: str,
    agent_id: str,
    error: Exception,
    output_text: str,
    raw_results: List[Any],
    session_id: Optional[str],
    current_state: str,
    state_dir: Optional[str] = None
) -> Path:
    """Save failed response and error information to a text file for analysis.

    Args:
        workflow_id: Workflow identifier
        agent_id: Agent identifier
        error: The exception that occurred
        output_text: Extracted text output from Claude
        raw_results: Raw results from Claude Code
        session_id: Claude session ID
        current_state: Current state/prompt file
        state_dir: Optional custom state directory

    Returns:
        Path to the saved error file
    """
    # Create errors directory next to state directory
    state_path = orchestrator.get_state_dir(state_dir)
    errors_dir = state_path.parent / "errors"
    errors_dir.mkdir(parents=True, exist_ok=True)

    # Generate filename with timestamp
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    filename = f"{workflow_id}_{agent_id}_{timestamp}.txt"
    error_file = errors_dir / filename

    # Prepare error information
    error_info = {
        "timestamp": datetime.now().isoformat(),
        "workflow_id": workflow_id,
        "agent_id": agent_id,
        "current_state": current_state,
        "error_type": type(error).__name__,
        "error_message": str(error),
        "session_id": session_id,
    }

    # Write error file
    with open(error_file, 'w', encoding='utf-8') as f:
        f.write("=" * 80 + "\n")
        f.write("ERROR REPORT\n")
        f.write("=" * 80 + "\n\n")

        # Error metadata
        f.write("ERROR INFORMATION:\n")
        f.write("-" * 80 + "\n")
        for key, value in error_info.items():
            f.write(f"{key}: {value}\n")
        f.write("\n")

        # Raw results (JSON formatted)
        f.write("RAW CLAUDE CODE RESULTS:\n")
        f.write("-" * 80 + "\n")
        try:
            f.write(json.dumps(raw_results, indent=2, ensure_ascii=False))
        except (TypeError, ValueError):
            # Fallback if results aren't JSON serializable
            f.write(str(raw_results))
        f.write("\n\n")

        # Extracted text output
        f.write("EXTRACTED TEXT OUTPUT:\n")
        f.write("-" * 80 + "\n")
        f.write(output_text)
        f.write("\n\n")

        # Full traceback if available
        f.write("TRACEBACK:\n")
        f.write("-" * 80 + "\n")
        f.write(traceback.format_exc())
        f.write("\n")

    logger.info(
        f"Saved error response to {error_file}",
        extra={
            "workflow_id": workflow_id,
            "agent_id": agent_id,
            "error_file": str(error_file)
        }
    )

    return error_file


def save_script_error_response(
    workflow_id: str,
    agent_id: str,
    error: Exception,
    script_path: str,
    stdout: str,
    stderr: str,
    exit_code: Optional[int],
    current_state: str,
    state_dir: Optional[str] = None
) -> Path:
    """Save failed script execution information to a text file for analysis.

    Args:
        workflow_id: Workflow identifier
        agent_id: Agent identifier
        error: The exception that occurred
        script_path: Full path to the script file
        stdout: Script stdout output (may be empty if script didn't run)
        stderr: Script stderr output (may be empty if script didn't run)
        exit_code: Script exit code (None if script didn't complete)
        current_state: Current state/script file
        state_dir: Optional custom state directory

    Returns:
        Path to the saved error file
    """
    # Create errors directory next to state directory
    state_path = orchestrator.get_state_dir(state_dir)
    errors_dir = state_path.parent / "errors"
    errors_dir.mkdir(parents=True, exist_ok=True)

    # Generate filename with timestamp
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    filename = f"{workflow_id}_{agent_id}_{timestamp}_script.txt"
    error_file = errors_dir / filename

    # Prepare error information
    error_info = {
        "timestamp": datetime.now().isoformat(),
        "workflow_id": workflow_id,
        "agent_id": agent_id,
        "current_state": current_state,
        "script_path": script_path,
        "error_type": type(error).__name__,
        "error_message": str(error),
        "exit_code": exit_code,
    }

    # Write error file
    with open(error_file, 'w', encoding='utf-8') as f:
        f.write("=" * 80 + "\n")
        f.write("SCRIPT ERROR REPORT\n")
        f.write("=" * 80 + "\n\n")

        # Error metadata
        f.write("ERROR INFORMATION:\n")
        f.write("-" * 80 + "\n")
        for key, value in error_info.items():
            f.write(f"{key}: {value}\n")
        f.write("\n")

        # Script stdout
        f.write("SCRIPT STDOUT:\n")
        f.write("-" * 80 + "\n")
        f.write(stdout if stdout else "(empty)\n")
        if stdout and not stdout.endswith('\n'):
            f.write('\n')
        f.write("\n")

        # Script stderr
        f.write("SCRIPT STDERR:\n")
        f.write("-" * 80 + "\n")
        f.write(stderr if stderr else "(empty)\n")
        if stderr and not stderr.endswith('\n'):
            f.write('\n')
        f.write("\n")

        # Full traceback if available
        f.write("TRACEBACK:\n")
        f.write("-" * 80 + "\n")
        f.write(traceback.format_exc())
        f.write("\n")

    logger.info(
        f"Saved script error response to {error_file}",
        extra={
            "workflow_id": workflow_id,
            "agent_id": agent_id,
            "error_file": str(error_file)
        }
    )

    return error_file


def _try_save_script_error(
    workflow_id: str,
    agent_id: str,
    error: Exception,
    script_path: str,
    stdout: str,
    stderr: str,
    exit_code: Optional[int],
    current_state: str,
    state_dir: Optional[str] = None
) -> None:
    """Attempt to save script error response, logging failures without raising.

    This is a wrapper around save_script_error_response that catches exceptions
    to ensure the original error is always raised, even if error saving fails.
    """
    try:
        # Call through orchestrator module to support test patching
        orchestrator.save_script_error_response(
            workflow_id=workflow_id,
            agent_id=agent_id,
            error=error,
            script_path=script_path,
            stdout=stdout,
            stderr=stderr,
            exit_code=exit_code,
            current_state=current_state,
            state_dir=state_dir
        )
    except Exception as save_error:
        logger.warning(f"Failed to save script error response: {save_error}")
