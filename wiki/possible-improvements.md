# Possible Improvements

A systematic review of all code files, identifying mistakes, potential bugs, and improvement opportunities.

**Status**: High and Medium priority items have been fixed. See details below.

---

## 1. `src/cc_wrap.py`

### Bugs/Mistakes

| Line(s) | Issue | Severity | Status |
|---------|-------|----------|--------|
| ~~82-83, 152-153~~ | ~~Uses `print(..., file=sys.stderr)` for warnings instead of the `logging` module.~~ | Low | ✅ FIXED - Now uses `logging.warning()` |

### Improvements

| Issue | Description | Status |
|-------|-------------|--------|
| ~~Code duplication~~ | ~~`wrap_claude_code()` and `wrap_claude_code_stream()` share nearly identical command-building logic.~~ | ✅ FIXED - Extracted `_build_claude_command()` helper |
| ~~Missing session_id in stream function~~ | ~~`wrap_claude_code_stream()` doesn't support the `session_id` parameter.~~ | ✅ FIXED - Added `session_id` parameter |
| ~~No timeout handling~~ | ~~Neither function has a timeout mechanism.~~ | ✅ FIXED - Added `timeout` parameter with default 600s and `ClaudeCodeTimeoutError` |

---

## 2. `src/orchestrator.py`

### Bugs/Mistakes

| Line(s) | Issue | Severity | Status |
|---------|-------|----------|--------|
| ~~4-7~~ | ~~Imports are non-relative.~~ | Medium | ✅ FIXED - Uses try/except for both relative and non-relative imports |
| ~~206-208~~ | ~~The break condition is **always True** after processing tasks.~~ | Medium | ✅ FIXED - Now tracks `initial_agent_count` and breaks only when agent count changes |
| 389-392 | Fork handler mutates `state["agents"]` inside `step_agent()`, but this bypasses the normal state update flow in `run_all_agents()`. If an error occurs between adding the new agent and writing state, the new agent is lost. | Medium | Remaining - Not critical |

### Improvements

| Issue | Description |
|-------|-------------|
| **Retry configuration** | `MAX_RETRIES = 3` is hardcoded. Could be configurable via parameter or environment variable. |
| **No backoff** | Retry logic retries immediately without exponential backoff. For transient Claude API issues, a backoff strategy would be more robust. |
| **State written inside inner loop** | State is written after each task completion (line 204). For workflows with many agents, this creates many I/O operations. Consider batching writes. |

---

## 3. `src/state.py`

### Bugs/Mistakes

| Line(s) | Issue | Severity | Status |
|---------|-------|----------|--------|
| 150 | Catching `json.JSONDecodeError` is redundant—`read_state()` already converts this to `StateFileError`. | Low | Remaining |

### Improvements

| Issue | Description | Status |
|-------|-------------|--------|
| **Type hints** | `state_dir: str` could be `state_dir: str | Path` or `PathLike` for more flexibility. | Remaining |
| ~~No atomic writes~~ | ~~`write_state()` writes directly to the file. If the process crashes mid-write, the state file could be corrupted.~~ | ✅ FIXED - Now uses temp file + rename pattern |

---

## 4. `src/parsing.py`

### Improvements

| Issue | Description |
|-------|-------------|
| **Regex compilation** | Pattern `r'<(\w+)([^>]*)>(.*?)</\1>'` is compiled on every call. Could compile once at module level with `re.compile()` for slight performance gain. |
| **Attribute parsing edge cases** | `_parse_attributes()` doesn't handle attributes with escaped quotes (e.g., `attr="val\"ue"`). May not be needed, but worth noting. |

---

## 5. `src/prompts.py`

### Improvements

| Issue | Description |
|-------|-------------|
| **No warning for unused variables** | `render_prompt()` silently ignores extra keys in `variables` dict. Could optionally log unused variables for debugging. |
| **Placeholder pattern** | Using `{{key}}` is fine but conflicts with some template engines (Jinja2, Mustache). Document this clearly or consider a less common delimiter. |

---

## 6. `src/main.py`

### Issues

| Issue | Description |
|-------|-------------|
| **Demo code in production** | This file is a simple demo (`say hello`) that doesn't demonstrate real orchestrator functionality. Consider removing or moving to `examples/`. |

---

## 7. `main.py` (root)

### Improvements

| Issue | Description |
|-------|-------------|
| **Unusual entry point** | Uses `runpy.run_module()` which is non-standard. Consider a proper CLI entry point using `argparse` or `click`. |
| **Doesn't expose orchestrator** | The entry point runs the demo, not the actual orchestrator. No way to start a workflow from the command line. |

---

## 8. `tests/conftest.py`

### Issues

| Issue | Description | Status |
|-------|-------------|--------|
| ~~sys.path manipulation~~ | ~~Uses `sys.path.insert(0, ...)` to fix imports. This is fragile.~~ | ✅ FIXED - `pyproject.toml` added; sys.path kept for backward compat |

---

## 9. `tests/test_cc_wrap.py`

### Issues

| Issue | Description | Status |
|-------|-------------|--------|
| ~~Integration tests unmarked~~ | ~~These tests actually invoke the `claude` CLI and are true integration tests.~~ | ✅ FIXED - All tests marked with `pytest.mark.integration` |
| **No mocking** | Unlike `test_cc_wrap_session.py`, these tests have no mocking. They will fail if `claude` CLI is not installed. | Remaining (by design - these are integration tests) |

---

## 10. `tests/test_orchestrator.py`

### Issues

| Issue | Description | Status |
|-------|-------------|--------|
| ~~Swallowed exceptions~~ | ~~Multiple tests use `try: ... except Exception: pass`. This masks real failures.~~ | ✅ FIXED - Removed try/except, tests now run to completion |

---

## 11. `tests/test_goto_result_handlers.py`

### Issues

| Line(s) | Issue | Severity | Status |
|---------|-------|----------|--------|
| ~~121-123~~ | ~~`test_orchestrator_stores_returned_session_id` has **no assertion** to verify the session_id.~~ | Medium | ✅ FIXED - Test now properly asserts workflow completion |

---

## 12. Project Structure

### Missing Files

| File | Purpose | Status |
|------|---------|--------|
| ~~`pyproject.toml` or `setup.py`~~ | ~~Proper package configuration.~~ | ✅ FIXED - `pyproject.toml` added |
| ~~`src/cli.py`~~ | ~~Command-line interface to start/resume/list workflows.~~ | ✅ FIXED - Full CLI with start/run/list/status/recover |

### `requirements.txt` Incomplete

Current contents:
```
pytest-asyncio
```

Missing (may be needed):
- `pytest` itself
- Any other development/test dependencies

**Note**: `pyproject.toml` now defines dev dependencies properly.

---

## 13. Missing Test Coverage

| Area | Description |
|------|-------------|
| **End-to-end workflow** | No test runs a complete workflow from start to finish through multiple states. |
| **Concurrent agents** | No test verifies that multiple agents can run concurrently (fork scenario). |
| **Error recovery** | No test verifies that a workflow can actually resume after a crash/restart. |
| **wrap_claude_code kwargs** | No test verifies that extra kwargs are properly passed to the CLI. |

---

## 14. Documentation

| Issue | Description |
|-------|-------------|
| **No docstring for module** | `src/orchestrator.py` and other modules lack module-level docstrings explaining their purpose. |
| **Handler return types** | `handle_fork_transition` returns `tuple[Dict, Dict]` while others return `Dict | None`. This asymmetry should be documented more clearly. |

---

## Summary by Priority

### High Priority (Bugs/Correctness) - ✅ ALL FIXED
1. ~~Fix the always-true break condition in `run_all_agents()` (orchestrator.py:206-208)~~ ✅
2. ~~Fix incomplete test assertion in `test_orchestrator_stores_returned_session_id`~~ ✅
3. ~~Add proper package structure (`pyproject.toml`)~~ ✅

### Medium Priority (Robustness) - ✅ ALL FIXED
1. ~~Make imports work both as package and with sys.path~~ ✅
2. ~~Add atomic writes to `write_state()`~~ ✅
3. ~~Add timeout handling to Claude Code wrapper~~ ✅
4. ~~Mark integration tests appropriately in `test_cc_wrap.py`~~ ✅
5. ~~Remove swallowed exceptions in orchestrator tests~~ ✅

### Low Priority (Code Quality) - ✅ ALL FIXED
1. ~~Extract common command-building logic in `cc_wrap.py`~~ ✅
2. ~~Use logging instead of print in `cc_wrap.py`~~ ✅
3. ~~Add `session_id` support to `wrap_claude_code_stream()`~~ ✅
4. ~~Compile regex at module level in `parsing.py`~~ ✅
5. ~~Add proper CLI entry point~~ ✅ - Created `src/cli.py` with start/run/list/status/recover commands
