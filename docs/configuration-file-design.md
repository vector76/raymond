# Configuration File Design

## Overview

This document explores design alternatives for a per-project configuration file to avoid repeatedly specifying options like `--dangerously-skip-permissions` or `--budget` on the command line.

## Requirements

- **Per-project configuration**: Different projects may need different settings
- **Cross-platform**: Must work on both Windows and Linux
- **Simple to use**: Easy to create and maintain
- **CLI precedence**: Command-line arguments should override config file values
- **Optional**: Should not break existing workflows that don't use config files

## Configuration Options to Support

Based on `src/cli.py`, the following options would benefit from configuration:

- `budget` (float, default: 10.0)
- `dangerously_skip_permissions` (bool, default: False)
- `model` (str: "opus"|"sonnet"|"haiku", default: None)
- `timeout` (float, default: 600, 0=none)
- `no_debug` (bool, default: False) - Matches CLI `--no-debug` semantics (default: false means debug mode is enabled)
- `verbose` (bool, default: False)

**Note on `state_dir`**: The `--state-dir` CLI option is not included in the config file. With project-based `.raymond` directory location, all workflows in a project naturally use the same state directory (`.raymond/state`). Since workflows have unique IDs, they can coexist in the same directory. The CLI `--state-dir` option remains available primarily for **testing** (to use temporary directories for test isolation), but for normal end-user operation, the default project-based location is sufficient and doesn't need to be configured.

## File Format: TOML

**File**: `.raymond/config.toml` (located in the `.raymond` directory at project root)

**Decision**: TOML format located in `.raymond/config.toml`

**Rationale:**
- Already using TOML for `pyproject.toml` (familiar format)
- Human-readable, supports comments (using `#`)
- Good type support (strings, numbers, booleans, arrays)
- Standard library support via `tomllib` (Python 3.11+)
- Common in Python ecosystem (pyproject.toml, Poetry, etc.)
- Natural location alongside other `.raymond` directory contents (state, debug, etc.)

**Python Version Requirement**: Python 3.11 or greater is required (uses `tomllib` from standard library)

**Example:**
```toml
# Raymond configuration file
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
```

## File Location Strategy

### Per-Project Discovery

The `.raymond` directory location follows the same search policy as the config file discovery:

**For config file discovery:**
1. Start at `cwd`
2. Check for `.raymond` directory in current directory
3. If not found, check parent directory
4. Continue until:
   - `.raymond` directory is found, OR
   - `.git` directory is encountered (project boundary - stop here)
5. If `.raymond` directory is found, check for `.raymond/config.toml` within it
6. If config file found, load it; if not found, use defaults

**For creating `.raymond` directory (when needed for state/debug/config):**
1. Find project root (search upward until `.git` is found)
2. If no `.git` found, use current working directory
3. Create `.raymond` directory at that location
4. This ensures all `.raymond` contents are in the same project-based location

**Key Points:**
- Search for `.raymond` directory stops at `.git` directory (project root boundary) or filesystem root
- If no `.git` directory is found in the hierarchy, search continues until filesystem root is reached (then stops)
- If multiple `.raymond` directories exist in the search path, the first one found (closest to CWD) is used
- If `.raymond` directory is found before reaching `.git`, that directory is used (allows intentional placement)
- Config file is located at `.raymond/config.toml` within the discovered `.raymond` directory
- This makes the `.raymond` directory location consistent and project-based (same policy as config discovery)
- If `.raymond` directory doesn't exist yet, it will be created at the project root when needed (for state files, debug files, or config generation)
- For normal operation, if `.raymond` directory doesn't exist, defaults are used (no config file found)

**Implementation Note:** This behavior is implemented in `src/config.py` and `src/state.py`.

**⚠️ Breaking Change Note:**
This change to `.raymond` directory location is a **breaking change** for existing workflows:
- Existing workflows created with CWD-based `.raymond` directories will not be found in the new project-based location
- Users may need to migrate existing state files manually (move `.raymond` directories to project root)
- Consider providing migration guidance or a migration tool

**Windows Considerations:**
- Handle both `C:\` and UNC paths (`\\server\share`)
- Case-insensitive filesystem (but preserve case in code)
- Path separators: use `pathlib.Path` for cross-platform handling

**Linux Considerations:**
- Case-sensitive filesystem
- Home directory could be project root (unlikely but possible)
- Path separators: standard `/`

**Implementation:**
```python
from pathlib import Path
from typing import Optional

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

def find_project_root(cwd: Path) -> Path:
    """Find project root (directory containing .git) or return cwd if not found."""
    current = Path(cwd).resolve()
    root = Path(current.anchor)
    
    while current != root:
        if (current / ".git").exists():
            return current
        current = current.parent
    
    # No .git found, return original cwd
    return Path(cwd).resolve()

def find_config_file(cwd: Path) -> Optional[Path]:
    """Find .raymond/config.toml config file by searching upward from cwd.
    
    Stops at .git directory (project boundary) or filesystem root.
    """
    raymond_dir = find_raymond_dir(cwd)
    if raymond_dir is None:
        return None
    
    config_file = raymond_dir / "config.toml"
    if config_file.is_file():
        return config_file
    
    return None
```

## Precedence Order

1. **Command-line arguments** (highest priority)
2. **Config file values**
3. **Default values** (lowest priority)

Example: If config file has `budget = 50.0` but CLI specifies `--budget 100.0`, use 100.0.

## File Naming

**Decision**: Use `.raymond/config.toml` (config file within `.raymond` directory)

- Natural location alongside other `.raymond` directory contents (state, debug, etc.)
- Keeps project root directory clean
- Consistent with existing `.raymond` directory structure
- Users can discover via `--init-config` command (see below)

## Config File Generation

A command-line option `--init-config` will generate a `.raymond/config.toml` file with all options commented out.

**Behavior:**
1. Search for existing `.raymond/config.toml` file using the same upward search logic
2. If any `.raymond/config.toml` file exists in the search path:
   - Report the location of the existing file
   - Refuse to generate and exit with error
3. If no `.raymond/config.toml` file exists:
   - Find project root (directory containing `.git`) using `find_project_root()`
   - If no `.git` directory found, use current working directory
   - Use `find_raymond_dir(create_if_missing=True)` to ensure `.raymond` directory exists
   - Generate `.raymond/config.toml` in that directory with all options commented out
   - Include comments explaining each option and their default values
   - Report success message with file location

**Example:**
```bash
raymond --init-config
# Creates .raymond/config.toml in project root (or cwd if no .git found)
```

**Generated file will include:**
- All configuration options commented out (with default values shown in comments)
- Comments explaining each option
- Users uncomment and modify only the options they want to change
- Uncommenting without changing the value has no effect (uses default)

This provides a starting point that users can customize by uncommenting only the options they need.

## Implementation Considerations

### Loading Config

```python
from pathlib import Path
from typing import Optional, Dict, Any
import tomllib  # Python 3.11+ (required)

def load_config(cwd: Optional[Path] = None) -> Dict[str, Any]:
    """Load configuration from .raymond/config.toml file, returning empty dict if not found.
    
    Raises an exception if the config file exists but cannot be parsed, so users can fix errors.
    
    Returns:
        Dictionary with configuration values, or empty dict if config file doesn't exist
        
    Raises:
        TOMLDecodeError: If config file exists but contains invalid TOML
        OSError: If config file exists but cannot be read
    """
    if cwd is None:
        cwd = Path.cwd()
    
    config_file = find_config_file(cwd)
    if config_file is None:
        return {}
    
    # If config file exists, it must be valid - raise on parse errors
    with open(config_file, "rb") as f:
        data = tomllib.load(f)
        config = data.get("raymond", {})
        
        # Note: Type and value validation should be performed after loading
        # (see Validation section for details on what to validate)
        
        return config
```

### Merging with CLI Args

```python
def merge_config_and_args(config: Dict[str, Any], args: argparse.Namespace) -> argparse.Namespace:
    """Merge config file values into args namespace, CLI args take precedence."""
    # Budget: only set if CLI didn't specify
    if args.budget is None and "budget" in config:
        args.budget = config["budget"]
    
    # Boolean flags: only set if CLI didn't specify (CLI defaults to False for store_true)
    if not args.dangerously_skip_permissions and config.get("dangerously_skip_permissions", False):
        args.dangerously_skip_permissions = True
    
    # No-debug: matches CLI --no-debug semantics directly
    if not args.no_debug and config.get("no_debug", False):
        args.no_debug = True
    
    # Verbose: similar to dangerously_skip_permissions
    if not args.verbose and config.get("verbose", False):
        args.verbose = True
    
    # Model: only set if CLI didn't specify
    if args.model is None and "model" in config:
        args.model = config["model"]
    
    # Timeout: only set if CLI didn't specify
    if args.timeout is None and "timeout" in config:
        args.timeout = config["timeout"]
    
    # Note: state_dir is not merged from config - CLI --state-dir is primarily for testing
    # With project-based .raymond directory, all workflows use .raymond/state by default
    
    return args
```

### Validation

- **Type validation**: TOML preserves types, so validate that config values match expected types:
  - `budget` and `timeout` must be numbers (float/int), not strings
  - `dangerously_skip_permissions`, `no_debug`, `verbose` must be booleans
  - `model` must be a string
  - Provide clear error messages like: "Error: Invalid value for 'budget' in .raymond/config.toml: expected number, got string '50.0'"
- **Range validation**: Validate that numeric values are within expected ranges (e.g., budget > 0, timeout >= 0)
- **Choice validation**: Validate that model values are one of the allowed choices ("opus", "sonnet", "haiku")
- Handle missing keys gracefully (use defaults)
- **Unknown keys**: Ignore unknown keys in the `[raymond]` section silently (allows forward compatibility)
- **Missing section**: If config file exists but `[raymond]` section is missing, return empty dict (use defaults)
- **Fail loudly on TOML parsing errors**: If `.raymond/config.toml` exists but cannot be parsed, raise an exception with a clear error message pointing to the file location and the parse error. Example: "Error: Failed to parse .raymond/config.toml at /path/to/.raymond/config.toml: Invalid TOML syntax at line 5, column 10: expected '='"

## Final Design Summary

**Format**: TOML (`.raymond/config.toml`)

**Rationale:**
- Already familiar from `pyproject.toml`
- Good balance of readability and tooling support
- Supports comments (using `#`)
- Standard library support in Python 3.11+ via `tomllib`
- Common in Python ecosystem
- Natural location alongside other `.raymond` directory contents

**Location**: Per-project, search upward from CWD until `.git` directory is found

**Naming**: `.raymond/config.toml` (config file within `.raymond` directory)

**Python Requirement**: Python 3.11 or greater (uses `tomllib` from standard library)

**Precedence**: CLI args > Config file > Defaults

**Example Implementation Flow:**

1. User runs: `raymond workflow.md --budget 100.0`
2. System searches for `.raymond` directory starting from CWD, stopping at `.git` directory
3. If `.raymond` directory found, checks for `.raymond/config.toml`
4. If found, loads config (e.g., `budget = 50.0`)
5. Merges: CLI `--budget 100.0` overrides config `50.0`
6. Uses merged values for execution

**Config Generation:**
- `--init-config` command generates `.raymond/config.toml` with all options commented out
- Generates in project root (directory with `.git`) or current directory if no `.git` found
- Creates `.raymond` directory if it doesn't exist
- Refuses to generate if any `.raymond/config.toml` already exists in search path
- All options are commented out by default; users uncomment only what they need

**`.raymond` Directory Location:**
- The `.raymond` directory location will follow the same search policy (upward until `.git` is found)
- This makes it project-based rather than CWD-based
- Config file, state files, and debug files will all be in the same project-based `.raymond` directory

## Testing Considerations

- Test config file discovery (upward search for `.raymond` directory until `.git` boundary)
- Test `.raymond` directory location (should follow same search policy)
- Test precedence (CLI > config > defaults)
- Test invalid config values (error handling)
- Test missing config file (should work with defaults)
- Test TOML parsing errors (malformed config file should fail with clear error message pointing to file and error)
- Test both Windows and Linux path handling
- Test `.git` boundary detection (stops searching at project root)
- Test filesystem root boundary (stops at root, doesn't loop)
- Test symlink handling (resolve() normalizes symlinks)
- Test case where `.raymond` exists in subdirectory before `.git` (should use that directory)
- Test `--init-config` command:
  - Generates file in correct location (project root or cwd)
  - Creates `.raymond` directory if it doesn't exist
  - Refuses if `.raymond/config.toml` already exists
  - Reports location of existing file when refusing
  - All options are commented out in generated file
- Test that only `.raymond/config.toml` is searched (not other locations)
- Test validation of config values:
  - Type validation (string where number expected, etc.)
  - Range validation (budget > 0, timeout >= 0)
  - Choice validation (valid model choices: "opus", "sonnet", "haiku")
  - Unknown keys are ignored (forward compatibility)
  - Missing `[raymond]` section returns empty dict (uses defaults)
- Test case where `.raymond` exists but is a file (not directory) - should continue searching

## Implementation Status

**Implemented in:**
- `src/config.py` - Configuration loading, validation, and merging
- `src/state.py` - Updated to use project-based `.raymond` directory location
- `src/cli.py` - `--init-config` command and config integration
- `tests/test_config.py` - Comprehensive test coverage

**Migration Notes:**
- Existing workflows with CWD-based `.raymond` directories will not be found in the new project-based location
- Users may need to manually move `.raymond` directories to project root
- Users can adopt config files gradually using `raymond --init-config`
