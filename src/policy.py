"""Policy validation for workflow transitions.

This module handles YAML frontmatter parsing and policy enforcement
for per-state transition restrictions.
"""
import re
import logging
import yaml
from typing import Dict, Any, List, Optional, Union
from dataclasses import dataclass
from .parsing import Transition

logger = logging.getLogger(__name__)


@dataclass
class Policy:
    """Represents a workflow state's transition policy.
    
    Attributes:
        allowed_transitions: List of allowed transition combinations.
            Each entry is a dict with 'tag' and optionally 'target', 'return', 'next', etc.
            Example: [{"tag": "goto", "target": "NEXT.md"}, {"tag": "result"}]
        model: Optional model specification from frontmatter (e.g., "opus", "sonnet", "haiku").
            If specified, this model will be used for Claude Code invocations for this state.
    """
    allowed_transitions: List[Dict[str, Any]]
    model: Optional[str] = None


class PolicyViolationError(ValueError):
    """Raised when a transition violates the state's policy."""
    pass


def should_use_reminder_prompt(policy: Optional[Policy]) -> bool:
    """Check if reminder prompt should be used for parse failures.
    
    Reminder prompts are only used when:
    - Policy exists
    - Policy has non-empty allowed_transitions list
    
    If no policy or no allowed_transitions, parse failures should
    terminate the agent with an error.
    
    Args:
        policy: The policy to check (None means no policy)
        
    Returns:
        True if reminder prompt should be used, False if error should be raised
    """
    if policy is None:
        return False
    
    if not policy.allowed_transitions:
        return False
    
    return True


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
        
        # Extract model field (optional)
        model = data.get("model")
        if model is not None:
            if not isinstance(model, str):
                # Model must be a string if present
                model = None
            else:
                # Normalize: strip whitespace and convert to lowercase
                model = model.strip().lower()
                # Treat empty strings as None
                if not model:
                    model = None
                # Validate model value (only for Claude Code currently)
                # Valid values: opus, sonnet, haiku
                elif model not in ("opus", "sonnet", "haiku"):
                    # Invalid model - log warning but don't fail
                    # We'll let Claude Code handle invalid models
                    # This allows for future expansion to other agent CLIs
                    logger.warning(
                        f"Unknown model '{model}' in frontmatter. "
                        f"Valid values for Claude Code: opus, sonnet, haiku. "
                        f"Passing to Claude Code as-is."
                    )
        
        policy = Policy(allowed_transitions=validated_transitions, model=model)
        
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


def can_use_implicit_transition(policy: Optional[Policy]) -> bool:
    """Check if implicit transition optimization can be used.
    
    Implicit transitions can be used when:
    - Policy exists and has exactly one allowed transition
    - The transition is NOT a result tag (result tags have variable payload)
    - All required information (tag, target, attributes) is predetermined
    
    Args:
        policy: The policy to check (None means no policy)
        
    Returns:
        True if implicit transition can be used, False otherwise
    """
    # No policy means we can't use implicit transitions
    if policy is None:
        return False
    
    # Empty policy means no restrictions, can't use implicit
    if not policy.allowed_transitions:
        return False
    
    # Must have exactly one allowed transition
    if len(policy.allowed_transitions) != 1:
        return False
    
    # Get the single allowed transition
    allowed = policy.allowed_transitions[0]
    tag = allowed.get("tag")
    
    # Result tags cannot be implicit (payload is variable)
    if tag == "result":
        return False
    
    # For non-result tags, we need at least a target
    # If target is missing, we can't construct the transition
    if "target" not in allowed:
        return False
    
    # All checks passed - can use implicit transition
    return True


def get_implicit_transition(policy: Optional[Policy]) -> Transition:
    """Get the implicit transition from a policy.
    
    This function constructs a Transition object from the single allowed
    transition in the policy. It should only be called when
    can_use_implicit_transition() returns True.
    
    Args:
        policy: The policy with exactly one non-result allowed transition
        
    Returns:
        Transition object constructed from the policy
        
    Raises:
        ValueError: If policy is None, empty, has multiple transitions,
                   or the only transition is a result tag
    """
    if not can_use_implicit_transition(policy):
        raise ValueError(
            "Cannot get implicit transition: policy must have exactly one "
            "non-result allowed transition"
        )
    
    allowed = policy.allowed_transitions[0]
    tag = allowed.get("tag")
    target = allowed.get("target", "")
    
    # Build attributes dictionary (exclude 'tag' and 'target' which are not attributes)
    attributes = {}
    for key, value in allowed.items():
        if key not in ("tag", "target"):
            attributes[key] = value
    
    return Transition(
        tag=tag,
        target=target,
        attributes=attributes,
        payload=""
    )


def generate_reminder_prompt(policy: Policy) -> str:
    """Generate a reminder prompt from allowed transitions.
    
    This function creates a helpful reminder message that lists all
    allowed transitions for the current state. It formats the transitions
    in a clear, readable way that can be appended to the original prompt.
    
    Args:
        policy: The policy containing allowed_transitions
        
    Returns:
        A formatted reminder string listing all allowed transitions
        
    Raises:
        ValueError: If policy is None or has no allowed_transitions
    """
    if policy is None:
        raise ValueError("Cannot generate reminder: policy is None")
    
    if not policy.allowed_transitions:
        raise ValueError("Cannot generate reminder: no allowed_transitions in policy")
    
    lines = [
        "",
        "---",
        "REMINDER: You must emit exactly one of the following transition tags:",
        ""
    ]
    
    for i, allowed in enumerate(policy.allowed_transitions, 1):
        tag = allowed.get("tag", "")
        target = allowed.get("target")
        
        # Build attributes list (excluding tag and target which are not attributes)
        attrs = []
        for key, value in allowed.items():
            if key not in ("tag", "target"):
                # Use appropriate quotes to avoid XML parsing issues
                # The parser supports both single and double quotes
                value_str = str(value)
                if '"' in value_str:
                    # Value contains double quote - use single quotes
                    # Note: If value contains both quote types, this will still work
                    # as the parser stops at the first quote, but this edge case
                    # shouldn't occur in practice (filenames don't contain quotes)
                    attrs.append(f"{key}='{value_str}'")
                else:
                    # No double quotes - use double quotes (standard)
                    # Single quotes in value are fine with double-quoted attribute
                    attrs.append(f'{key}="{value_str}"')
        
        # Build the complete XML tag string
        if tag == "result":
            # Result tag: <result>...</result> with variable payload
            tag_str = f"<{tag}>...</{tag}>"
            description = "(return control to caller or terminate)"
        else:
            # Other tags: need target, may have attributes
            if not target:
                # Missing target is unusual but handle gracefully
                target = "TARGET"
            
            if attrs:
                # Tag with attributes: <tag attr="value">target</tag>
                attrs_str = " ".join(attrs)
                tag_str = f"<{tag} {attrs_str}>{target}</{tag}>"
            else:
                # Tag without attributes: <tag>target</tag>
                tag_str = f"<{tag}>{target}</{tag}>"
            description = ""
        
        # Format the line
        if description:
            lines.append(f"{i}. {tag_str} {description}")
        else:
            lines.append(f"{i}. {tag_str}")
    
    lines.append("")
    lines.append("Please emit exactly one of these tags in your response.")
    lines.append("---")
    
    return "\n".join(lines)
