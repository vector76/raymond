"""Exception classes for orchestrator errors.

This module defines the exception hierarchy used throughout the orchestrator.
The base OrchestratorError class provides a foundation for all orchestrator-specific
errors, with specialized subclasses for different failure modes.

StateFileError is re-exported from the state module for convenience.
"""

from src.state import StateFileError


class OrchestratorError(Exception):
    """Base exception for orchestrator errors.

    All orchestrator-specific exceptions inherit from this class,
    allowing callers to catch all orchestrator errors with a single handler.
    """
    pass


class ClaudeCodeError(OrchestratorError):
    """Raised when Claude Code execution fails.

    This is the base class for Claude Code related errors.
    Subclasses distinguish between different failure modes.
    """
    pass


class ClaudeCodeLimitError(ClaudeCodeError):
    """Raised when Claude Code hits its usage limit.

    This is a non-retryable error that should pause the agent
    rather than terminate it. The workflow can be resumed later
    when usage limits reset.
    """
    pass


class ClaudeCodeTimeoutWrappedError(ClaudeCodeError):
    """Raised when Claude Code times out.

    This error allows pause/resume behavior - the agent is paused
    rather than terminated, and can be resumed with --resume flag.
    The session_id is preserved for continuation.
    """
    pass


class PromptFileError(OrchestratorError):
    """Raised when prompt file operations fail.

    This includes errors loading, rendering, or accessing prompt files.
    """
    pass


class ScriptError(OrchestratorError):
    """Raised when script execution fails.

    This can be due to:
    - Non-zero exit code
    - Script timeout
    - Invalid transition output
    - Missing script file
    """
    pass


# Re-export for convenience
__all__ = [
    'OrchestratorError',
    'ClaudeCodeError',
    'ClaudeCodeLimitError',
    'ClaudeCodeTimeoutWrappedError',
    'PromptFileError',
    'ScriptError',
    'StateFileError',
]
