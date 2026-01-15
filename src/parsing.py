import re
from typing import List, Dict, NamedTuple

# Pre-compiled regex patterns for better performance
# Pattern to match transition tags with optional attributes
# Matches: <tag attr="value">content</tag>
# Captures: tag name, attributes string, content
_TRANSITION_PATTERN = re.compile(r'<(\w+)([^>]*)>(.*?)</\1>', re.DOTALL)

# Pattern to match key="value" or key='value' attributes
_ATTRIBUTE_PATTERN = re.compile(r'(\w+)=["\']([^"\']*)["\']')


class Transition(NamedTuple):
    """Represents a transition tag parsed from agent output.
    
    Attributes:
        tag: The tag name (goto, reset, function, call, fork, result)
        target: The target filename (empty for result tag)
        attributes: Dictionary of tag attributes (e.g., {"return": "X.md"})
        payload: The text content between tags (for result tag)
    """
    tag: str
    target: str
    attributes: Dict[str, str]
    payload: str


def parse_transitions(output: str) -> List[Transition]:
    r"""Extract transition tags from output.
    
    Recognizes:
        <goto>FILE.md</goto>
        <reset>FILE.md</reset>
        <function return="NEXT.md">EVAL.md</function>
        <call return="NEXT.md">CHILD.md</call>
        <fork next="NEXT.md" item="foo">WORKER.md</fork>
        <result>...</result>
    
    Args:
        output: The agent output text to parse
        
    Returns:
        List of Transition objects found in the output
        
    Raises:
        ValueError: If any tag target contains path separators (/ or \)
    """
    transitions = []
    
    for match in _TRANSITION_PATTERN.finditer(output):
        tag_name = match.group(1)
        attrs_str = match.group(2).strip()
        content = match.group(3)
        
        # Only process known transition tags
        if tag_name not in ("goto", "reset", "function", "call", "fork", "result"):
            continue
        
        # Parse attributes
        attributes = _parse_attributes(attrs_str)
        
        # Extract target and payload
        if tag_name == "result":
            # Result tag: payload is the content, no target
            target = ""
            payload = content
        else:
            # Other tags: target is the content (filename), no payload
            target = content.strip()
            payload = ""
            
            # Validate target is not empty
            if not target:
                raise ValueError(
                    f"Tag <{tag_name}> has empty target. "
                    "Non-result tags must specify a target filename."
                )
            
            # Validate path safety: target must not contain / or \
            if "/" in target or "\\" in target:
                raise ValueError(
                    f"Path '{target}' contains path separator. "
                    "Tag targets must be filenames only, not paths."
                )
        
        transitions.append(Transition(
            tag=tag_name,
            target=target,
            attributes=attributes,
            payload=payload
        ))
    
    return transitions


def _parse_attributes(attrs_str: str) -> Dict[str, str]:
    """Parse HTML-style attributes from a string.
    
    Args:
        attrs_str: String like 'return="X.md" item="foo"'
        
    Returns:
        Dictionary of attribute names to values
    """
    attributes = {}
    
    if not attrs_str:
        return attributes
    
    for match in _ATTRIBUTE_PATTERN.finditer(attrs_str):
        key = match.group(1)
        value = match.group(2)
        attributes[key] = value
    
    return attributes


def validate_single_transition(transitions: List[Transition]) -> None:
    """Validate that exactly one transition exists.
    
    Args:
        transitions: List of Transition objects
        
    Raises:
        ValueError: If the list does not contain exactly one transition
    """
    if len(transitions) != 1:
        raise ValueError(
            f"Expected exactly one transition, found {len(transitions)}"
        )
