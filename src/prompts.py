import sys
from pathlib import Path
from typing import Any, Dict, Tuple, Optional
from .policy import Policy, parse_frontmatter


def is_windows() -> bool:
    """Return True if running on Windows."""
    return sys.platform.startswith('win')


def is_unix() -> bool:
    """Return True if running on Unix (Linux/macOS)."""
    return not is_windows()

# Placeholder format for template variables
PLACEHOLDER_PREFIX = "{{"
PLACEHOLDER_SUFFIX = "}}"


def load_prompt(scope_dir: str, filename: str) -> Tuple[str, Optional[Policy]]:
    """Load a prompt file from the scope directory and parse frontmatter.
    
    Args:
        scope_dir: Directory containing prompt files
        filename: Name of the prompt file to load
        
    Returns:
        Tuple of (prompt content without frontmatter, Policy object or None)
        
    Raises:
        FileNotFoundError: If the prompt file does not exist
        ValueError: If filename contains path separators (defense in depth)
        ValueError: If frontmatter contains invalid YAML
    """
    # Defense in depth: validate filename doesn't contain path separators
    if "/" in filename or "\\" in filename:
        raise ValueError(
            f"Filename '{filename}' contains path separator. "
            "Filenames must not contain / or \\"
        )
    
    prompt_path = Path(scope_dir) / filename
    
    if not prompt_path.exists():
        raise FileNotFoundError(f"Prompt file not found: {prompt_path}")
    
    with open(prompt_path, 'r', encoding='utf-8') as f:
        content = f.read()
    
    # Parse frontmatter and return content + policy
    policy, body = parse_frontmatter(content)
    return body, policy


def render_prompt(template: str, variables: Dict[str, Any]) -> str:
    """Replace {{key}} placeholders with values from variables dict.
    
    Args:
        template: Template string with {{key}} placeholders
        variables: Dictionary mapping placeholder keys to values.
            Values are converted to strings if not already strings.
        
    Returns:
        Template with placeholders replaced. Missing keys leave placeholders unchanged.
    """
    result = template
    
    # Replace each variable in the template
    for key, value in variables.items():
        placeholder = PLACEHOLDER_PREFIX + key + PLACEHOLDER_SUFFIX
        # Convert value to string if needed
        str_value = value if isinstance(value, str) else str(value)
        result = result.replace(placeholder, str_value)
    
    return result


# Supported script file extensions by platform
SCRIPT_EXTENSIONS_UNIX = {'.sh'}
SCRIPT_EXTENSIONS_WINDOWS = {'.bat'}


def _get_platform_script_extension() -> str:
    """Return the script extension for the current platform."""
    return '.bat' if is_windows() else '.sh'


def _get_other_platform_script_extension() -> str:
    """Return the script extension for the other platform."""
    return '.sh' if is_windows() else '.bat'


def resolve_state(scope_dir: str, state_name: str) -> str:
    """Resolve an abstract state name to a concrete filename.
    
    This function handles both explicit filenames (e.g., "NEXT.md") and abstract
    state names (e.g., "NEXT"). For abstract names, it searches for matching files
    with supported extensions (.md, .sh, .bat) and returns the appropriate one.
    
    Resolution priority for abstract names:
    1. .md files are preferred (markdown states)
    2. Platform-appropriate script files (.sh on Unix, .bat on Windows)
    
    Args:
        scope_dir: Directory containing state files
        state_name: State name, either abstract (e.g., "NEXT") or explicit (e.g., "NEXT.md")
        
    Returns:
        The resolved filename (e.g., "NEXT.md", "NEXT.sh", "NEXT.bat")
        
    Raises:
        FileNotFoundError: If no matching file exists
        ValueError: If the state name is ambiguous (multiple valid files exist)
        ValueError: If an explicit extension is for the wrong platform
        ValueError: If state_name contains path separators (defense in depth)
    """
    # Defense in depth: validate state_name doesn't contain path separators
    if "/" in state_name or "\\" in state_name:
        raise ValueError(
            f"State name '{state_name}' contains path separator. "
            "State names must not contain / or \\"
        )
    
    scope_path = Path(scope_dir)
    
    # Check if state_name has an explicit extension
    name_path = Path(state_name)
    extension = name_path.suffix.lower()
    
    if extension:
        # Explicit extension provided - validate and check existence
        return _resolve_explicit_extension(scope_path, state_name, extension)
    else:
        # Abstract name - search for matching files
        return _resolve_abstract_name(scope_path, state_name)


def _resolve_explicit_extension(scope_path: Path, state_name: str, extension: str) -> str:
    """Resolve a state name with explicit extension.
    
    Args:
        scope_path: Path to scope directory
        state_name: Full filename with extension
        extension: The file extension (e.g., ".md", ".sh")
        
    Returns:
        The state_name if file exists and is valid for platform
        
    Raises:
        FileNotFoundError: If file doesn't exist
        ValueError: If extension is for wrong platform
    """
    # Check platform compatibility for script extensions
    if extension in SCRIPT_EXTENSIONS_UNIX and is_windows():
        raise ValueError(
            f"Cannot use Unix script '{state_name}' on Windows. "
            "Use a .bat file instead."
        )
    if extension in SCRIPT_EXTENSIONS_WINDOWS and is_unix():
        raise ValueError(
            f"Cannot use Windows script '{state_name}' on Unix. "
            "Use a .sh file instead."
        )
    
    # Check if file exists
    full_path = scope_path / state_name
    if not full_path.exists():
        raise FileNotFoundError(f"State file not found: {full_path}")
    
    return state_name


def get_state_type(filename: str) -> str:
    """Determine the state type from a filename based on its extension.
    
    This function determines whether a state file should be executed as a
    markdown state (sent to Claude Code) or a script state (executed directly).
    
    Args:
        filename: The state filename with extension (e.g., "NEXT.md", "POLL.sh")
        
    Returns:
        "markdown" for .md files
        "script" for platform-appropriate script files (.sh on Unix, .bat on Windows)
        
    Raises:
        ValueError: If the extension is unsupported or for the wrong platform
    """
    extension = Path(filename).suffix.lower()
    
    if not extension:
        raise ValueError(
            f"Unsupported state file '{filename}': no extension. "
            "State files must have .md, .sh, or .bat extension."
        )
    
    # Markdown files work on all platforms
    if extension == '.md':
        return "markdown"
    
    # Script files are platform-specific
    if extension in SCRIPT_EXTENSIONS_UNIX:
        if is_windows():
            raise ValueError(
                f"Cannot use Unix script '{filename}' on Windows. "
                "Use a .bat file instead."
            )
        return "script"
    
    if extension in SCRIPT_EXTENSIONS_WINDOWS:
        if is_unix():
            raise ValueError(
                f"Cannot use Windows script '{filename}' on Unix. "
                "Use a .sh file instead."
            )
        return "script"
    
    # Unknown extension
    raise ValueError(
        f"Unsupported state file extension '{extension}' in '{filename}'. "
        "Supported extensions: .md, .sh (Unix), .bat (Windows)."
    )


def _resolve_abstract_name(scope_path: Path, state_name: str) -> str:
    """Resolve an abstract state name (no extension) to a concrete filename.
    
    Args:
        scope_path: Path to scope directory
        state_name: Abstract state name without extension
        
    Returns:
        The resolved filename with extension
        
    Raises:
        FileNotFoundError: If no matching file exists
        ValueError: If multiple valid files exist (ambiguous)
    """
    # Check what files exist
    md_path = scope_path / f"{state_name}.md"
    platform_script_ext = _get_platform_script_extension()
    other_script_ext = _get_other_platform_script_extension()
    
    script_path = scope_path / f"{state_name}{platform_script_ext}"
    other_script_path = scope_path / f"{state_name}{other_script_ext}"
    
    md_exists = md_path.exists()
    script_exists = script_path.exists()
    other_script_exists = other_script_path.exists()
    
    # Check for ambiguity: .md and platform-appropriate script both exist
    if md_exists and script_exists:
        raise ValueError(
            f"Ambiguous state '{state_name}': both {state_name}.md and "
            f"{state_name}{platform_script_ext} exist. Use explicit extension."
        )
    
    # Return .md if it exists
    if md_exists:
        return f"{state_name}.md"
    
    # Return platform-appropriate script if it exists
    if script_exists:
        return f"{state_name}{platform_script_ext}"
    
    # No valid file found
    if other_script_exists:
        # Only wrong-platform script exists
        raise FileNotFoundError(
            f"State '{state_name}' not found. Only {state_name}{other_script_ext} exists, "
            f"which is not compatible with this platform."
        )
    
    raise FileNotFoundError(
        f"State '{state_name}' not found in {scope_path}. "
        f"Looked for: {state_name}.md, {state_name}{platform_script_ext}"
    )
