"""Orchestrator package for managing agent workflows.

This module provides the orchestration layer for running agent workflows,
including state execution, transition handling, and error management.

Phase 7 of refactoring: Cleanup - removed orchestrator_old.py and consolidated
all code into the modular architecture.

IMPORTANT: Dependencies that tests patch must be imported FIRST, before
importing from other modules. This allows modules to import these from
src.orchestrator, making patches work correctly.
"""

# First, import dependencies that tests may patch.
# These must be imported BEFORE other orchestrator modules to avoid circular import issues.
from src.cc_wrap import wrap_claude_code, wrap_claude_code_stream, ClaudeCodeTimeoutError
from src.state import read_state, write_state, delete_state, StateFileError as StateFileErrorFromState, get_state_dir
from src.parsing import parse_transitions, validate_single_transition, Transition
from src.policy import validate_transition_policy, PolicyViolationError, can_use_implicit_transition, get_implicit_transition, should_use_reminder_prompt, generate_reminder_prompt
from src.scripts import run_script, build_script_env, ScriptTimeoutError
from src.prompts import load_prompt, render_prompt, resolve_state, get_state_type
from src.console import init_reporter, get_reporter

# Re-export StateFileError with proper name
StateFileError = StateFileErrorFromState

# Import transition handlers from the transitions module
from src.orchestrator.transitions import (
    handle_goto_transition,
    handle_reset_transition,
    handle_function_transition,
    handle_call_transition,
    handle_fork_transition,
    handle_result_transition,
    apply_transition,
)

# Import error classes from the errors module
from src.orchestrator.errors import (
    OrchestratorError,
    ClaudeCodeError,
    ClaudeCodeLimitError,
    ClaudeCodeTimeoutWrappedError,
    PromptFileError,
    ScriptError,
    RecoveryStrategy,
)

# Import the main workflow function from the workflow module
from src.orchestrator.workflow import run_all_agents, MAX_RETRIES

# Import debug utilities from the debug_utils module
from src.orchestrator.debug_utils import (
    create_debug_directory,
    save_claude_output,
    get_claude_output_filepath,
    append_claude_output_line,
    save_script_output,
    save_script_output_metadata,
    log_state_transition,
    save_error_response,
    save_script_error_response,
    _try_save_script_error,
)

# Import utility functions from executors
from src.orchestrator.executors.utils import (
    _extract_state_name,
    _resolve_transition_targets,
)

# Import cost extraction from markdown executor
from src.orchestrator.executors.markdown import (
    extract_cost_from_results,
    MAX_REMINDER_ATTEMPTS,
)

# Import backward-compatible step_agent functions
from src.orchestrator.compat import (
    step_agent,
    _step_agent_script,
)

__all__ = [
    # Exception classes
    "OrchestratorError",
    "ClaudeCodeError",
    "ClaudeCodeLimitError",
    "ClaudeCodeTimeoutWrappedError",
    "PromptFileError",
    "ScriptError",
    "StateFileError",
    # Recovery strategy
    "RecoveryStrategy",
    # Constants
    "MAX_RETRIES",
    "MAX_REMINDER_ATTEMPTS",
    # Cost extraction
    "extract_cost_from_results",
    # Debug utilities
    "create_debug_directory",
    "save_claude_output",
    "get_claude_output_filepath",
    "append_claude_output_line",
    "save_script_output",
    "save_script_output_metadata",
    "log_state_transition",
    "save_error_response",
    "save_script_error_response",
    "_try_save_script_error",
    "_extract_state_name",
    # Main functions
    "run_all_agents",
    "step_agent",
    "_step_agent_script",
    # Transition handlers
    "handle_goto_transition",
    "handle_reset_transition",
    "handle_function_transition",
    "handle_call_transition",
    "handle_fork_transition",
    "handle_result_transition",
    "apply_transition",
    # Internal helper
    "_resolve_transition_targets",
    # Dependencies (for patching in tests)
    "wrap_claude_code",
    "wrap_claude_code_stream",
    "ClaudeCodeTimeoutError",
    "read_state",
    "write_state",
    "delete_state",
    "get_state_dir",
    "parse_transitions",
    "validate_single_transition",
    "Transition",
    "validate_transition_policy",
    "PolicyViolationError",
    "can_use_implicit_transition",
    "get_implicit_transition",
    "should_use_reminder_prompt",
    "generate_reminder_prompt",
    "run_script",
    "build_script_env",
    "ScriptTimeoutError",
    "load_prompt",
    "render_prompt",
    "resolve_state",
    "get_state_type",
    "init_reporter",
    "get_reporter",
]
