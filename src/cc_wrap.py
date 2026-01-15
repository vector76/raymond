import asyncio
import json
import sys
from typing import List, Dict, Any, AsyncIterator, Tuple, Optional


async def wrap_claude_code(
    prompt: str, 
    model: str = None, 
    session_id: Optional[str] = None,
    **kwargs
) -> Tuple[List[Dict[str, Any]], Optional[str]]:
    """
    Wraps claude code invocation in headless mode with stream-json output.
    This is an async function that can be run concurrently with other instances.

    Args:
        prompt: The prompt to send to claude
        model: The model to use (e.g., "haiku", "sonnet", "opus")
        session_id: Optional session ID to resume an existing session (passes --resume flag)
        **kwargs: Additional arguments to pass to claude command

    Returns:
        Tuple of (list of parsed JSON objects from the stream, extracted session_id or None)
    """
    # Build the command
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
        if value is True:
            cmd.append(f"--{key.replace('_', '-')}")
        elif value is not False and value is not None:
            cmd.append(f"--{key.replace('_', '-')}")
            cmd.append(str(value))

    # Run the command asynchronously and capture stdout
    process = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )

    results = []
    extracted_session_id = None

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
            print(f"Warning: Failed to parse JSON line: {line}", file=sys.stderr)
            print(f"Error: {e}", file=sys.stderr)

    # Wait for process to complete
    returncode = await process.wait()

    # Check for errors
    if returncode != 0:
        stderr_output = await process.stderr.read()
        stderr_text = stderr_output.decode('utf-8') if stderr_output else ""
        raise RuntimeError(
            f"Claude command failed with return code {returncode}\n"
            f"Stderr: {stderr_text}"
        )

    return results, extracted_session_id


async def wrap_claude_code_stream(prompt: str, model: str = None, **kwargs) -> AsyncIterator[Dict[str, Any]]:
    """
    Wraps claude code invocation and yields JSON objects as they arrive.
    This is an async generator that can be run concurrently with other instances.

    Args:
        prompt: The prompt to send to claude
        model: The model to use (e.g., "haiku", "sonnet", "opus")
        **kwargs: Additional arguments to pass to claude command

    Yields:
        Parsed JSON objects from the stream as they arrive
    """
    # Build the command
    cmd = [
        "claude",
        "-p",  # headless/print mode
        "--output-format", "stream-json",
        "--verbose",
    ]

    if model:
        cmd.extend(["--model", model])

    cmd.append(prompt)

    # Add any additional kwargs as command-line arguments
    for key, value in kwargs.items():
        if value is True:
            cmd.append(f"--{key.replace('_', '-')}")
        elif value is not False and value is not None:
            cmd.append(f"--{key.replace('_', '-')}")
            cmd.append(str(value))

    # Run the command asynchronously and capture stdout
    process = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )

    # Read and parse the streamed JSON output line by line
    async for line in process.stdout:
        line = line.decode('utf-8').strip()
        if not line:
            continue

        try:
            parsed = json.loads(line)
            yield parsed
        except json.JSONDecodeError as e:
            # If a line isn't valid JSON, log it but continue
            print(f"Warning: Failed to parse JSON line: {line}", file=sys.stderr)
            print(f"Error: {e}", file=sys.stderr)

    # Wait for process to complete
    returncode = await process.wait()

    # Check for errors
    if returncode != 0:
        stderr_output = await process.stderr.read()
        stderr_text = stderr_output.decode('utf-8') if stderr_output else ""
        raise RuntimeError(
            f"Claude command failed with return code {returncode}\n"
            f"Stderr: {stderr_text}"
        )
