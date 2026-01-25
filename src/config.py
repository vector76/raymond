"""Configuration file management for Raymond.

This module handles loading and validation of per-project configuration files
from `.raymond/config.toml`. Configuration files are discovered by searching
upward from the current working directory until a `.git` directory is found.
"""

import argparse
import sys
from pathlib import Path
from typing import Any, Dict, Optional

# Check Python version for tomllib support (Python 3.11+)
if sys.version_info < (3, 11):
    raise RuntimeError(
        "Raymond requires Python 3.11 or greater for tomllib support. "
        f"Current version: {sys.version_info.major}.{sys.version_info.minor}"
    )

import tomllib  # Python 3.11+ standard library


class ConfigError(Exception):
    """Raised when configuration file operations fail."""
    pass


def find_project_root(cwd: Path) -> Path:
    """Find project root (directory containing .git) or return cwd if not found.
    
    Args:
        cwd: Current working directory to start search from
        
    Returns:
        Path to project root (directory with .git) or cwd if not found
    """
    current = Path(cwd).resolve()  # Resolve symlinks and normalize
    root = Path(current.anchor)  # Filesystem root
    
    while current != root:
        if (current / ".git").exists():
            return current
        current = current.parent
    
    # No .git found, return original cwd
    return Path(cwd).resolve()


def find_raymond_dir(cwd: Path, create_if_missing: bool = False) -> Optional[Path]:
    """Find .raymond directory by searching upward from cwd.
    
    Stops at .git directory (project boundary) or filesystem root.
    Uses resolve() to handle symlinks and normalize paths.
    
    Args:
        cwd: Current working directory to start search from
        create_if_missing: If True, create .raymond directory at project root if not found
    
    Returns:
        The .raymond directory path if found or created, None otherwise
    """
    current = Path(cwd).resolve()  # Resolve symlinks and normalize
    root = Path(current.anchor)  # Filesystem root
    project_root = None
    
    # First, search for existing .raymond directory
    while current != root:
        raymond_dir = current / ".raymond"
        if raymond_dir.is_dir():
            return raymond_dir
        # Note: If .raymond exists but is a file (not directory), is_dir() returns False
        # and we continue searching - this is correct behavior
        
        # Track project root (where .git is)
        if (current / ".git").exists():
            project_root = current
            break
            
        current = current.parent
    
    # If not found and create_if_missing, create at project root (or cwd if no .git)
    if create_if_missing:
        target_dir = project_root if project_root is not None else Path(cwd).resolve()
        raymond_dir = target_dir / ".raymond"
        raymond_dir.mkdir(parents=True, exist_ok=True)
        return raymond_dir
    
    return None


def find_config_file(cwd: Path) -> Optional[Path]:
    """Find .raymond/config.toml config file by searching upward from cwd.
    
    Stops at .git directory (project boundary) or filesystem root.
    
    Args:
        cwd: Current working directory to start search from
        
    Returns:
        Path to config file if found, None otherwise
    """
    raymond_dir = find_raymond_dir(cwd)
    if raymond_dir is None:
        return None
    
    config_file = raymond_dir / "config.toml"
    if config_file.is_file():
        return config_file
    
    return None


def validate_config(config: Dict[str, Any], config_file: Path) -> Dict[str, Any]:
    """Validate configuration values and filter out unknown keys.
    
    Args:
        config: Dictionary of configuration values
        config_file: Path to config file (for error messages)
        
    Returns:
        Validated config dictionary with only known keys
        
    Raises:
        ConfigError: If any validation fails
    """
    # Known configuration keys
    known_keys = {
        "budget", "dangerously_skip_permissions", "model", "timeout",
        "no_debug", "verbose"
    }
    
    # Filter out unknown keys (forward compatibility)
    validated_config = {k: v for k, v in config.items() if k in known_keys}
    
    # Type validation
    if "budget" in validated_config:
        if not isinstance(validated_config["budget"], (int, float)):
            raise ConfigError(
                f"Invalid value for 'budget' in {config_file}: "
                f"expected number, got {type(validated_config['budget']).__name__}"
            )
        if validated_config["budget"] <= 0:
            raise ConfigError(
                f"Invalid value for 'budget' in {config_file}: "
                f"must be positive, got {validated_config['budget']}"
            )
    
    if "timeout" in validated_config:
        if not isinstance(validated_config["timeout"], (int, float)):
            raise ConfigError(
                f"Invalid value for 'timeout' in {config_file}: "
                f"expected number, got {type(validated_config['timeout']).__name__}"
            )
        if validated_config["timeout"] < 0:
            raise ConfigError(
                f"Invalid value for 'timeout' in {config_file}: "
                f"must be non-negative, got {validated_config['timeout']}"
            )
    
    if "dangerously_skip_permissions" in validated_config:
        if not isinstance(validated_config["dangerously_skip_permissions"], bool):
            raise ConfigError(
                f"Invalid value for 'dangerously_skip_permissions' in {config_file}: "
                f"expected boolean, got {type(validated_config['dangerously_skip_permissions']).__name__}"
            )
    
    if "no_debug" in validated_config:
        if not isinstance(validated_config["no_debug"], bool):
            raise ConfigError(
                f"Invalid value for 'no_debug' in {config_file}: "
                f"expected boolean, got {type(validated_config['no_debug']).__name__}"
            )
    
    if "verbose" in validated_config:
        if not isinstance(validated_config["verbose"], bool):
            raise ConfigError(
                f"Invalid value for 'verbose' in {config_file}: "
                f"expected boolean, got {type(validated_config['verbose']).__name__}"
            )
    
    if "model" in validated_config:
        if not isinstance(validated_config["model"], str):
            raise ConfigError(
                f"Invalid value for 'model' in {config_file}: "
                f"expected string, got {type(validated_config['model']).__name__}"
            )
        if validated_config["model"] not in ("opus", "sonnet", "haiku"):
            raise ConfigError(
                f"Invalid value for 'model' in {config_file}: "
                f"must be one of 'opus', 'sonnet', 'haiku', got '{validated_config['model']}'"
            )
    
    return validated_config


def load_config(cwd: Optional[Path] = None) -> Dict[str, Any]:
    """Load configuration from .raymond/config.toml file, returning empty dict if not found.
    
    Raises an exception if the config file exists but cannot be parsed, so users can fix errors.
    
    Args:
        cwd: Current working directory to start search from. If None, uses Path.cwd()
        
    Returns:
        Dictionary with configuration values, or empty dict if config file doesn't exist
        
    Raises:
        ConfigError: If config file exists but contains invalid TOML, values, or cannot be read
    """
    if cwd is None:
        cwd = Path.cwd()
    
    config_file = find_config_file(cwd)
    if config_file is None:
        return {}
    
    # If config file exists, it must be valid - raise on parse errors
    try:
        with open(config_file, "rb") as f:
            data = tomllib.load(f)
            config = data.get("raymond", {})
            
            # Validate config values and filter unknown keys
            validated_config = validate_config(config, config_file)
            
            return validated_config
    except tomllib.TOMLDecodeError as e:
        raise ConfigError(
            f"Failed to parse {config_file}: Invalid TOML syntax - {e}"
        ) from e
    except OSError as e:
        raise ConfigError(
            f"Failed to read {config_file}: {e}"
        ) from e


def merge_config_and_args(config: Dict[str, Any], args: argparse.Namespace) -> argparse.Namespace:
    """Merge config file values into args namespace, CLI args take precedence.
    
    For boolean flags with store_true actions:
    - If CLI value is True (user provided flag), use CLI value (don't override)
    - If CLI value is False (user didn't provide flag), config can enable it
    
    For non-boolean values:
    - If CLI value is None (not specified), use config value
    - If CLI value is set, use CLI value (override config)
    
    Args:
        config: Dictionary of configuration values from config file
        args: Parsed command-line arguments namespace
        
    Returns:
        Modified args namespace with config values merged in
    """
    # Budget: only set if CLI didn't specify
    if not hasattr(args, 'budget') or args.budget is None:
        if "budget" in config:
            args.budget = config["budget"]
    
    # Boolean flags: only set if CLI didn't explicitly set to True
    # If CLI is False (default for store_true when not provided), config can enable it
    if hasattr(args, 'dangerously_skip_permissions'):
        if not args.dangerously_skip_permissions and config.get("dangerously_skip_permissions", False):
            args.dangerously_skip_permissions = True
    
    # No-debug: matches CLI --no-debug semantics directly
    if hasattr(args, 'no_debug'):
        if not args.no_debug and config.get("no_debug", False):
            args.no_debug = True
    
    # Verbose: similar to dangerously_skip_permissions
    # Note: If CLI explicitly set verbose=True, it stays True (not overridden)
    # If CLI is False (not provided), config can enable it
    if hasattr(args, 'verbose'):
        if not args.verbose and config.get("verbose", False):
            args.verbose = True
    
    # Model: only set if CLI didn't specify
    if not hasattr(args, 'model') or args.model is None:
        if "model" in config:
            args.model = config["model"]
    
    # Timeout: only set if CLI didn't specify
    if not hasattr(args, 'timeout') or args.timeout is None:
        if "timeout" in config:
            args.timeout = config["timeout"]
    
    # Note: state_dir is not merged from config - CLI --state-dir is primarily for testing
    # With project-based .raymond directory, all workflows use .raymond/state by default
    
    return args


def init_config(cwd: Optional[Path] = None) -> int:
    """Generate a new .raymond/config.toml file with all options commented out.
    
    Args:
        cwd: Current working directory to start search from. If None, uses Path.cwd()
        
    Returns:
        0 on success, 1 on error
    """
    if cwd is None:
        cwd = Path.cwd()
    
    # Check if config file already exists
    existing_config = find_config_file(cwd)
    if existing_config is not None:
        print(
            f"Error: Configuration file already exists at {existing_config}",
            file=sys.stderr
        )
        print(
            "Refusing to generate a new config file. "
            "Delete or rename the existing file first.",
            file=sys.stderr
        )
        return 1
    
    # Find project root and create .raymond directory if needed
    project_root = find_project_root(cwd)
    raymond_dir = find_raymond_dir(project_root, create_if_missing=True)
    
    if raymond_dir is None:
        print("Error: Failed to create .raymond directory", file=sys.stderr)
        return 1
    
    config_file = raymond_dir / "config.toml"
    
    # Generate config file with all options commented out
    config_content = """# Raymond configuration file
# Command-line arguments override values in this file
# Uncomment and modify values as needed

[raymond]
# Cost budget limit in USD (default: 10.0)
# budget = 10.0

# Skip permission prompts (WARNING: allows any action without prompting) (default: false)
# dangerously_skip_permissions = false

# Default model: "opus", "sonnet", or "haiku" (default: None)
# model = "sonnet"

# Timeout per Claude Code invocation in seconds (default: 600, 0=none)
# timeout = 600.0

# Disable debug mode (default: false, meaning debug mode is enabled by default)
# no_debug = false

# Enable verbose logging (default: false)
# verbose = false
"""
    
    try:
        config_file.write_text(config_content, encoding="utf-8")
        print(f"Created configuration file at {config_file}")
        return 0
    except OSError as e:
        print(f"Error: Failed to write configuration file: {e}", file=sys.stderr)
        return 1
