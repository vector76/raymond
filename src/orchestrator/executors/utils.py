"""Shared utilities for state executors.

This module contains helper functions used by both MarkdownExecutor and
ScriptExecutor to avoid code duplication.
"""

import src.orchestrator as orchestrator
from src.parsing import Transition


def extract_state_name(state_filename: str) -> str:
    """Extract state name from a state filename by removing the extension.

    Handles .md, .sh, and .bat extensions case-insensitively.

    Args:
        state_filename: State filename (e.g., "CHECK.md", "SCRIPT.bat")

    Returns:
        State name without extension (e.g., "CHECK", "SCRIPT")
    """
    for ext in ('.md', '.sh', '.bat'):
        if state_filename.lower().endswith(ext):
            return state_filename[:-len(ext)]
    return state_filename


def resolve_transition_targets(transition: Transition, scope_dir: str) -> Transition:
    """Resolve abstract state names in a transition to concrete filenames.

    This function resolves all state references in a transition:
    - The main target (for goto, reset, function, call, fork)
    - The 'return' attribute (for function, call)
    - The 'next' attribute (for fork)

    Args:
        transition: Original transition with potentially abstract state names
        scope_dir: Directory containing state files

    Returns:
        New Transition with all state references resolved

    Raises:
        FileNotFoundError: If any referenced state file doesn't exist
        ValueError: If any state name is ambiguous (multiple files match)
    """
    # Result tags have no state references to resolve
    if transition.tag == "result":
        return transition

    # Resolve the main target
    resolved_target = orchestrator.resolve_state(scope_dir, transition.target)

    # Resolve attributes that contain state references
    resolved_attributes = dict(transition.attributes)

    if "return" in resolved_attributes:
        resolved_attributes["return"] = orchestrator.resolve_state(
            scope_dir, resolved_attributes["return"]
        )

    if "next" in resolved_attributes:
        resolved_attributes["next"] = orchestrator.resolve_state(
            scope_dir, resolved_attributes["next"]
        )

    # Return new transition with resolved values
    return Transition(
        tag=transition.tag,
        target=resolved_target,
        attributes=resolved_attributes,
        payload=transition.payload
    )
