# Implementation Plan: Shell Script States

This document outlines the implementation plan for shell script states as
described in `wiki/bash-states.md`. The feature allows workflow states to be
implemented as shell scripts (`.sh`/`.bat`) that execute directly, bypassing
the LLM for deterministic operations.

## Overview

Currently, all states are markdown files interpreted by Claude Code. This plan
adds support for shell scripts as an alternative state implementation:

- **Markdown states** (`.md`): Sent to Claude Code (existing behavior)
- **Script states** (`.sh`/`.bat`): Executed directly by the orchestrator (new)

Both state types emit the same transition tags and participate in the same
workflow protocol.

## Design Principles

1. **Same protocol**: Scripts emit `<goto>`, `<reset>`, `<result>`, etc. just
   like LLM responses
2. **Extension-based dispatch**: File extension determines execution mode, not
   transition type
3. **Abstract state names**: Transitions can reference states without extensions;
   orchestrator resolves to the appropriate file
4. **Fatal errors**: Script failures terminate the workflow (no re-prompting)

## Phases

### Phase 1: State Resolution

Modify state resolution to support multiple file extensions and abstract state
names.

**Current behavior:** `<goto>NEXT.md</goto>` loads `NEXT.md` directly.

**New behavior:** `<goto>NEXT</goto>` (or `<goto>NEXT.md</goto>`) resolves to
`NEXT.md`, `NEXT.sh`, or `NEXT.bat` based on what exists.

### Phase 2: Script Execution

Add infrastructure to execute shell scripts and capture their output.

- Run `.sh` files with bash (Unix) or error (Windows without WSL)
- Run `.bat` files with cmd.exe (Windows)
- Capture stdout for transition parsing
- Capture stderr for debugging
- Handle timeouts and exit codes
- **Async execution**: Use `asyncio.create_subprocess_exec()` to avoid blocking
- **Working directory**: Scripts run in orchestrator's directory (not scope_dir)

### Phase 3: Environment Variables

Pass workflow context to scripts via environment variables:

- `RAYMOND_WORKFLOW_ID`
- `RAYMOND_AGENT_ID`
- `RAYMOND_STATE_DIR`
- `RAYMOND_STATE_FILE`
- `RAYMOND_RESULT` (for call returns)
- Fork attributes as variables

### Phase 4: Integration

Update `step_agent()` to dispatch between LLM execution and script execution
based on file type.

### Phase 5: Debug Mode Support

Extend debug mode to capture script execution:

- Save stdout/stderr to debug directory
- Log execution time and exit codes
- Include environment variables in debug output

---

## Detailed Task Checklist

Tasks are ordered for TDD: write tests first, then implement.

### Phase 1: State Resolution

#### Step 1.1: Abstract State Name Resolution

Platform-specific tests use pytest markers (`@pytest.mark.unix`, `@pytest.mark.windows`)
and are skipped on incompatible platforms.

**Cross-platform tests:**
- [x] **1.1.1** Write tests: `resolve_state("NEXT")` finds `NEXT.md` when it exists
- [x] **1.1.2** Write tests: `resolve_state("NEXT.md")` returns `NEXT.md` (explicit extension)
- [x] **1.1.3** Write tests: `resolve_state("NEXT")` raises when no matching file exists
- [x] **1.1.4** Write tests: `resolve_state("NEXT.md")` raises when `NEXT.md` doesn't exist (explicit, no fallback)
- [x] **1.1.5** Write tests: resolution respects scope_dir parameter
- [x] **1.1.6** Write tests: `resolve_state("NEXT")` succeeds when `.sh` and `.bat` both exist (uses platform-appropriate)

**Unix-only tests (`@pytest.mark.unix`):**
- [ ] **1.1.7** Write tests: `resolve_state("NEXT")` finds `NEXT.sh` when `.md` doesn't exist
- [ ] **1.1.8** Write tests: `resolve_state("NEXT.sh")` returns `NEXT.sh` (explicit extension)
- [ ] **1.1.9** Write tests: `resolve_state("NEXT")` raises when only `.bat` exists (no `.md` or `.sh`)
- [ ] **1.1.10** Write tests: `resolve_state("NEXT")` raises when `.md` and `.sh` both exist (ambiguous)
- [ ] **1.1.11** Write tests: `resolve_state("NEXT.bat")` raises (wrong platform)

**Windows-only tests (`@pytest.mark.windows`):**
- [x] **1.1.12** Write tests: `resolve_state("NEXT")` finds `NEXT.bat` when `.md` doesn't exist
- [x] **1.1.13** Write tests: `resolve_state("NEXT.bat")` returns `NEXT.bat` (explicit extension)
- [x] **1.1.14** Write tests: `resolve_state("NEXT")` raises when only `.sh` exists (no `.md` or `.bat`)
- [x] **1.1.15** Write tests: `resolve_state("NEXT")` raises when `.md` and `.bat` both exist (ambiguous)
- [x] **1.1.16** Write tests: `resolve_state("NEXT.sh")` raises (wrong platform)

- [x] **1.1.17** Implement `resolve_state()` function in `src/prompts.py`

#### Step 1.2: State Type Detection

- [x] **1.2.1** Write tests: `get_state_type("NEXT.md")` returns `"markdown"`
- [x] **1.2.2** Write tests: `get_state_type("NEXT.sh")` returns `"script"` on Unix, raises on Windows
- [x] **1.2.3** Write tests: `get_state_type("NEXT.bat")` returns `"script"` on Windows, raises on Unix
- [x] **1.2.4** Write tests: `get_state_type("NEXT.py")` raises (unsupported)
- [x] **1.2.5** Implement `get_state_type()` function

#### Step 1.3: Update Transition Handling

- [x] **1.3.1** Write tests: `<goto>NEXT</goto>` resolves correctly (no extension)
- [x] **1.3.2** Write tests: `<goto>NEXT.md</goto>` still works (backward compatible)
- [x] **1.3.3** Write tests: `<reset>POLL.sh</reset>` works with explicit extension
- [x] **1.3.4** Update transition handlers to use `resolve_state()`

### Phase 2: Script Execution

#### Step 2.1: Script Runner Infrastructure

- [x] **2.1.1** Write tests: `run_script()` executes `.sh` file and captures stdout
- [x] **2.1.2** Write tests: `run_script()` executes `.bat` file and captures stdout
- [x] **2.1.3** Write tests: `run_script()` captures stderr separately
- [x] **2.1.4** Write tests: `run_script()` returns exit code
- [x] **2.1.5** Write tests: `run_script()` respects timeout parameter
- [x] **2.1.6** Write tests: `run_script()` raises on timeout
- [x] **2.1.7** Write tests: `run_script()` uses correct shell for file type
- [x] **2.1.8** Write tests: `run_script()` is async (doesn't block event loop)
- [x] **2.1.9** Write tests: `run_script()` runs in orchestrator's working directory (not scope_dir)
- [x] **2.1.10** Implement `run_script()` function in new `src/scripts.py` using `asyncio.create_subprocess_exec()`

#### Step 2.2: Platform Detection

**Cross-platform tests:**
- [x] **2.2.1** Write tests: `is_unix()` and `is_windows()` detect platform correctly

**Unix-only tests (`@pytest.mark.unix`):**
- [x] **2.2.2** Write tests: `.sh` execution works
- [x] **2.2.3** Write tests: attempting to run `.bat` raises clear error

**Windows-only tests (`@pytest.mark.windows`):**
- [x] **2.2.4** Write tests: `.bat` execution works
- [x] **2.2.5** Write tests: attempting to run `.sh` raises clear error

- [x] **2.2.6** Implement platform-aware script execution

#### Step 2.3: Output Parsing

- [x] **2.3.1** Write tests: parse transition tag from script stdout
- [x] **2.3.2** Write tests: transition tag can appear anywhere in stdout (not just last line)
- [x] **2.3.3** Write tests: extract tag attributes from script output
- [x] **2.3.4** Write tests: handle `<result>payload</result>` from scripts
- [x] **2.3.5** Reuse existing `parse_transitions()` for script output

### Phase 3: Environment Variables

#### Step 3.1: Core Environment Variables

- [ ] **3.1.1** Write tests: `RAYMOND_WORKFLOW_ID` is set when running script
- [ ] **3.1.2** Write tests: `RAYMOND_AGENT_ID` is set when running script
- [ ] **3.1.3** Write tests: `RAYMOND_STATE_DIR` is set to scope directory
- [ ] **3.1.4** Write tests: `RAYMOND_STATE_FILE` is set to state file path
- [ ] **3.1.5** Implement environment variable injection in `run_script()`

#### Step 3.2: Result and Fork Variables

- [ ] **3.2.1** Write tests: `RAYMOND_RESULT` is set when returning from `<call>`
- [ ] **3.2.2** Write tests: fork attributes are set as environment variables
- [ ] **3.2.3** Write tests: `item` attribute from `<fork item="X">` becomes `$item`
- [ ] **3.2.4** Write tests: multiple fork attributes all become environment variables
- [ ] **3.2.5** Implement fork attribute injection

### Phase 4: Integration

#### Step 4.1: Dispatcher in step_agent

- [ ] **4.1.1** Write tests: `step_agent()` dispatches to LLM for `.md` files
- [ ] **4.1.2** Write tests: `step_agent()` dispatches to script runner for `.sh` files
- [ ] **4.1.3** Write tests: `step_agent()` dispatches to script runner for `.bat` files
- [ ] **4.1.4** Write tests: script result is processed same as LLM result
- [ ] **4.1.5** Write tests: workflow can start with script as initial state
- [ ] **4.1.6** Write tests: script states don't modify agent's session_id
- [ ] **4.1.7** Write tests: script states contribute $0.00 to cost tracking
- [ ] **4.1.8** Implement dispatcher logic in `step_agent()`

#### Step 4.2: Error Handling

- [ ] **4.2.1** Write tests: script exit code 0 with valid tag → normal transition
- [ ] **4.2.2** Write tests: script exit code 0 with no tag → fatal error
- [ ] **4.2.3** Write tests: script exit code non-zero → fatal error
- [ ] **4.2.4** Write tests: script with multiple tags → fatal error
- [ ] **4.2.5** Write tests: script timeout → fatal error
- [ ] **4.2.6** Write tests: fatal errors terminate workflow (no retry)
- [ ] **4.2.7** Implement error handling for script states

#### Step 4.3: All Transition Types

- [ ] **4.3.1** Write tests: `<goto>` works from script state
- [ ] **4.3.2** Write tests: `<reset>` works from script state
- [ ] **4.3.3** Write tests: `<result>` works from script state (with payload)
- [ ] **4.3.4** Write tests: `<call>` works from script state
- [ ] **4.3.5** Write tests: `<function>` works from script state
- [ ] **4.3.6** Write tests: `<fork>` works from script state
- [ ] **4.3.7** Write tests: can transition from script state to markdown state
- [ ] **4.3.8** Write tests: can transition from markdown state to script state
- [ ] **4.3.9** Verify all transition handlers work with script states

### Phase 5: Debug Mode Support

#### Step 5.1: Script Output Capture

- [ ] **5.1.1** Write tests: debug mode saves script stdout to file
- [ ] **5.1.2** Write tests: debug mode saves script stderr to file
- [ ] **5.1.3** Write tests: output filename follows pattern `{agent}_{state}_{step}.txt`
- [ ] **5.1.4** Write tests: separate `.stdout.txt` and `.stderr.txt` files
- [ ] **5.1.5** Implement script output capture in debug mode

#### Step 5.2: Execution Metadata

- [ ] **5.2.1** Write tests: debug mode logs script execution time
- [ ] **5.2.2** Write tests: debug mode logs exit code
- [ ] **5.2.3** Write tests: debug mode logs environment variables
- [ ] **5.2.4** Write tests: transitions.log includes script state transitions
- [ ] **5.2.5** Implement execution metadata logging

### Phase 6: Sample Workflows and Documentation

#### Step 6.1: Test Workflows

- [ ] **6.1.1** Create `workflows/test_cases/SCRIPT_GOTO.sh` - simple goto test
- [ ] **6.1.2** Create `workflows/test_cases/SCRIPT_RESULT.sh` - result with payload
- [ ] **6.1.3** Create `workflows/test_cases/SCRIPT_RESET.sh` - reset transition
- [ ] **6.1.4** Create hybrid workflow: script → markdown → script
- [ ] **6.1.5** Create polling example workflow (script with sleep)

#### Step 6.2: Windows Test Workflows

- [ ] **6.2.1** Create `.bat` equivalents of test workflows
- [ ] **6.2.2** Test on Windows with batch files
- [ ] **6.2.3** Verify cross-platform workflow (both `.sh` and `.bat` for same state)

#### Step 6.3: Documentation Updates

- [ ] **6.3.1** Update `wiki/sample-workflows.md` with script examples
- [ ] **6.3.2** Add script state examples to existing documentation
- [ ] **6.3.3** Document environment variables in detail

---

## Implementation Order

**Note:** Phase numbers group related functionality; the implementation order
below sequences them for incremental delivery (getting something working early).

Suggested order:

1. **Phase 1** (State Resolution) - Foundation for everything else
2. **Phase 2** (Script Execution) - Core capability
3. **Phase 4.1** (Dispatcher) - Minimal working integration
4. **Phase 4.2** (Error Handling) - Robustness
5. **Phase 3** (Environment Variables) - Context passing (deferred until basic execution works)
6. **Phase 4.3** (All Transitions) - Complete transition support
7. **Phase 5** (Debug Mode) - Observability
8. **Phase 6** (Samples/Docs) - Validation and documentation

**Integration checkpoint after Phase 4.1:** Can run a simple workflow with one
script state that emits `<goto>` to a markdown state.

**Integration checkpoint after Phase 4.3:** All transition types work between
script and markdown states in any combination.

---

## Testing Strategy

- Unit tests for each function (`resolve_state`, `run_script`, etc.)
- Integration tests using sample workflows in `workflows/test_cases/`
- Platform-specific tests use pytest markers and are auto-skipped on wrong platform

**Pytest marker configuration** (add to `conftest.py`):

```python
import sys
import pytest

def pytest_configure(config):
    config.addinivalue_line("markers", "unix: mark test to run only on Unix")
    config.addinivalue_line("markers", "windows: mark test to run only on Windows")

def pytest_collection_modifyitems(config, items):
    is_windows = sys.platform.startswith('win')
    skip_unix = pytest.mark.skip(reason="Unix-only test")
    skip_windows = pytest.mark.skip(reason="Windows-only test")
    
    for item in items:
        if "unix" in item.keywords and is_windows:
            item.add_marker(skip_unix)
        if "windows" in item.keywords and not is_windows:
            item.add_marker(skip_windows)
```

**Usage in tests:**

```python
@pytest.mark.unix
def test_sh_execution():
    # Only runs on Linux/macOS
    ...

@pytest.mark.windows
def test_bat_execution():
    # Only runs on Windows
    ...
```

## Files to Create/Modify

| File | Action | Description |
|------|--------|-------------|
| `src/scripts.py` | Create | Async script execution infrastructure |
| `src/prompts.py` | Modify | Add `resolve_state()`, `get_state_type()` |
| `src/orchestrator.py` | Modify | Update `step_agent()` to dispatch by file type |
| `tests/test_scripts.py` | Create | Tests for script execution |
| `tests/test_prompts.py` | Modify | Add tests for state resolution |
| `tests/test_orchestrator.py` | Modify | Add integration tests for script states |
| `workflows/test_cases/*.sh` | Create | Test script states (Unix) |
| `workflows/test_cases/*.bat` | Create | Test script states (Windows) |
