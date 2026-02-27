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

Based on `internal/config/config.go`, the following options are supported in config files:

- `budget` (float, default: 10.0)
- `dangerously_skip_permissions` (bool, default: false)
- `effort` (str: "low"|"medium"|"high", default: none)
- `model` (str: "opus"|"sonnet"|"haiku", default: none)
- `timeout` (float, default: 600, 0=none)
- `no_debug` (bool, default: false) - Matches CLI `--no-debug` semantics (default: false means debug mode is enabled)
- `no_wait` (bool, default: false) - Do not auto-wait when usage limit is reached; exit immediately instead
- `verbose` (bool, default: false)

**Note on `state_dir`**: The `--state-dir` CLI option is not included in the config file. With project-based `.raymond` directory location, all workflows in a project naturally use the same state directory (`.raymond/state`). Since workflows have unique IDs, they can coexist in the same directory. The CLI `--state-dir` option remains available primarily for **testing** (to use temporary directories for test isolation), but for normal end-user operation, the default project-based location is sufficient and doesn't need to be configured.

## File Format: TOML

**File**: `.raymond/config.toml` (located in the `.raymond` directory at project root)

**Decision**: TOML format located in `.raymond/config.toml`

**Rationale:**
- Human-readable, supports comments (using `#`)
- Good type support (strings, numbers, booleans, arrays)
- Standard library support in Go via `github.com/BurntSushi/toml`
- Natural location alongside other `.raymond` directory contents (state, debug, etc.)

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

# Default effort level: "low", "medium", or "high" (default: None)
# effort = "medium"

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

**Implementation Note:** This behavior is implemented in `internal/config/config.go` and `internal/state/state.go`.

**⚠️ Breaking Change Note:**
This change to `.raymond` directory location is a **breaking change** for existing workflows:
- Existing workflows created with CWD-based `.raymond` directories will not be found in the new project-based location
- Users may need to migrate existing state files manually (move `.raymond` directories to project root)
- Consider providing migration guidance or a migration tool

**Windows Considerations:**
- Handle both `C:\` and UNC paths (`\\server\share`)
- Case-insensitive filesystem (but preserve case in code)
- Path separators: use `filepath.Join` (Go standard library) for cross-platform handling

**Linux Considerations:**
- Case-sensitive filesystem
- Home directory could be project root (unlikely but possible)
- Path separators: standard `/`

**Implementation:** `internal/config/config.go` — `FindProjectRoot`, `FindRaymondDir`, `FindConfigFile`

The algorithm walks upward from `cwd`, checking each directory for `.raymond/` (returning it immediately if found) and for `.git/` (recording it as the project-root boundary). If no `.raymond/` is found before the boundary, the search stops. When creating a new `.raymond/` directory (e.g. for `--init-config`), it is placed at the project root (the directory containing `.git/`), or at `cwd` if no `.git/` is found.

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

**Implementation:** `internal/config/config.go` — `LoadConfig`

Locates `.raymond/config.toml` via `FindConfigFile`, then parses the `[raymond]` TOML section using `github.com/BurntSushi/toml`. Returns an empty map when no config file is found. Raises a parse error (with the file path) if the file exists but is invalid TOML, so users can fix mistakes.

### Merging with CLI Args

**Implementation:** `internal/config/config.go` — `MergeConfig`

CLI-supplied values take precedence over config-file values. For boolean flags (`dangerously_skip_permissions`, `no_debug`, `no_wait`, `verbose`), the config value is only applied when the CLI flag was not explicitly set. For string values (`model`, `effort`) and numeric values (`budget`, `timeout`), the config value is used only when the CLI left the field at its zero value. `state_dir` is never read from the config file — it is a test-only flag.

### Validation

- **Type validation**: TOML preserves types, so validate that config values match expected types:
  - `budget` and `timeout` must be numbers (float/int), not strings
  - `dangerously_skip_permissions`, `no_debug`, `verbose` must be booleans
  - `effort` and `model` must be strings
  - Provide clear error messages like: "Error: Invalid value for 'budget' in .raymond/config.toml: expected number, got string '50.0'"
- **Range validation**: Validate that numeric values are within expected ranges (e.g., budget > 0, timeout >= 0)
- **Choice validation**: Validate that effort values are one of the allowed choices ("low", "medium", "high") and model values are one of the allowed choices ("opus", "sonnet", "haiku")
- Handle missing keys gracefully (use defaults)
- **Unknown keys**: Ignore unknown keys in the `[raymond]` section silently (allows forward compatibility)
- **Missing section**: If config file exists but `[raymond]` section is missing, return empty dict (use defaults)
- **Fail loudly on TOML parsing errors**: If `.raymond/config.toml` exists but cannot be parsed, raise an exception with a clear error message pointing to the file location and the parse error. Example: "Error: Failed to parse .raymond/config.toml at /path/to/.raymond/config.toml: Invalid TOML syntax at line 5, column 10: expected '='"

## Final Design Summary

**Format**: TOML (`.raymond/config.toml`)

**Rationale:**
- Human-readable, supports comments (using `#`)
- Good balance of readability and tooling support
- Parsed via `github.com/BurntSushi/toml` in Go
- Natural location alongside other `.raymond` directory contents

**Location**: Per-project, search upward from CWD until `.git` directory is found

**Naming**: `.raymond/config.toml` (config file within `.raymond` directory)

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
  - Choice validation (valid effort choices: "low", "medium", "high"; valid model choices: "opus", "sonnet", "haiku")
  - Unknown keys are ignored (forward compatibility)
  - Missing `[raymond]` section returns empty dict (uses defaults)
- Test case where `.raymond` exists but is a file (not directory) - should continue searching

## Implementation Status

**Implemented in:**
- `internal/config/config.go` - Configuration loading, validation, and merging
- `internal/state/state.go` - Updated to use project-based `.raymond` directory location
- `internal/cli/cli.go` - `--init-config` command and config integration
- `internal/config/config_test.go` - Comprehensive test coverage

**Migration Notes:**
- Existing workflows with CWD-based `.raymond` directories will not be found in the new project-based location
- Users may need to manually move `.raymond` directories to project root
- Users can adopt config files gradually using `raymond --init-config`
