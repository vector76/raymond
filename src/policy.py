"""Policy validation for workflow transitions.

This module handles YAML frontmatter parsing and policy enforcement
for per-state transition restrictions.
"""
import re
import yaml
from typing import Dict, Any, List, Optional, Union
from dataclasses import dataclass
from .parsing import Transition


@dataclass
class Policy:
    """Represents a workflow state's transition policy.
    
    Attributes:
        allowed_transitions: List of allowed transition combinations.
            Each entry is a dict with 'tag' and optionally 'target', 'return', 'next', etc.
            Example: [{"tag": "goto", "target": "NEXT.md"}, {"tag": "result"}]
    """
    allowed_transitions: List[Dict[str, Any]]


class PolicyViolationError(ValueError):
    """Raised when a transition violates the state's policy."""
    pass


def parse_frontmatter(content: str) -> tuple[Optional[Policy], str]:
    """Parse YAML frontmatter from markdown content.
    
    Frontmatter is expected to be at the start of the file, delimited by `---`
    on separate lines. If no frontmatter is present, returns (None, content).
    
    Args:
        content: The full file content (may include frontmatter)
        
    Returns:
        Tuple of (Policy object or None, body content without frontmatter)
        
    Raises:
        ValueError: If frontmatter is malformed YAML
    """
    # Pattern to match frontmatter: ---\n...\n---
    # Must match at the start of content
    # Handle both empty frontmatter (---\n---\n) and non-empty (---\ncontent\n---\n)
    # First try to match with content, then try empty frontmatter
    frontmatter_pattern = re.compile(r'^---\s*\n(.+?)\n---\s*\n', re.DOTALL)
    empty_frontmatter_pattern = re.compile(r'^---\s*\n---\s*\n', re.DOTALL)
    
    match = frontmatter_pattern.match(content)
    if match:
        yaml_content = match.group(1)
        body = content[match.end():]
    else:
        # Try empty frontmatter
        empty_match = empty_frontmatter_pattern.match(content)
        if empty_match:
            yaml_content = ""
            body = content[empty_match.end():]
        else:
            # No frontmatter found
            return None, content
    
    # Parse empty frontmatter as no policy
    if not yaml_content.strip():
        return None, body
    
    try:
        data = yaml.safe_load(yaml_content)
        if not data:
            return None, body
        
        # Extract allowed_transitions (default to empty list)
        allowed_transitions = data.get("allowed_transitions", [])
        if not isinstance(allowed_transitions, list):
            allowed_transitions = []
        
        # Validate that each entry is a dict with at least a 'tag' key
        validated_transitions = []
        for entry in allowed_transitions:
            if isinstance(entry, dict) and "tag" in entry:
                validated_transitions.append(entry)
        
        policy = Policy(allowed_transitions=validated_transitions)
        
        return policy, body
    except yaml.YAMLError as e:
        raise ValueError(f"Invalid YAML frontmatter: {e}") from e


def validate_transition_policy(transition: Transition, policy: Optional[Policy]) -> None:
    """Validate that a transition complies with the state's policy.
    
    Args:
        transition: The transition to validate
        policy: The policy to validate against (None means no restrictions)
        
    Raises:
        PolicyViolationError: If the transition violates the policy
    """
    # No policy means no restrictions
    if policy is None:
        return
    
    # If no allowed_transitions specified, allow all
    if not policy.allowed_transitions:
        return
    
    # Check if transition matches any allowed entry
    for allowed in policy.allowed_transitions:
        # Tag must match
        if allowed.get("tag") != transition.tag:
            continue
        
        # For result tag, no target/attributes to check
        if transition.tag == "result":
            return  # Match found
        
        # Check target matches (if specified in policy)
        if "target" in allowed:
            if allowed["target"] != transition.target:
                continue
        
        # Check all other attributes match
        # For call/function: check return attribute
        if "return" in allowed:
            if transition.attributes.get("return") != allowed["return"]:
                continue
        
        # For fork: check next attribute
        if "next" in allowed:
            if transition.attributes.get("next") != allowed["next"]:
                continue
        
        # All checks passed - this is a valid transition
        return
    
    # No matching entry found
    # Build helpful error message
    allowed_for_tag = [a for a in policy.allowed_transitions if a.get("tag") == transition.tag]
    if allowed_for_tag:
        # Tag is allowed but this specific combination isn't
        raise PolicyViolationError(
            f"Transition '{transition.tag}' with target '{transition.target}' "
            f"and attributes {transition.attributes} is not allowed. "
            f"Allowed combinations for '{transition.tag}': {allowed_for_tag}"
        )
    else:
        # Tag itself is not allowed
        allowed_tags = list(set(a.get("tag") for a in policy.allowed_transitions))
        raise PolicyViolationError(
            f"Tag '{transition.tag}' is not allowed. "
            f"Allowed tags: {allowed_tags}"
        )
