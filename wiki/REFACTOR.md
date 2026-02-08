# Task: Refactor Orchestrator to Executor + Observer Architecture

## Overview

Refactor `src/orchestrator.py` (~2700 lines) into a modular architecture using:
1. **Executors**: Polymorphic classes that handle different state types (markdown vs script)
2. **Event Bus**: Simple pub/sub system for decoupling core logic from side effects
3. **Observers**: Pluggable handlers for debug output, console display, and future extensibility

### Goals
- Separate concerns: orchestration logic vs execution logic vs recording/display
- Make debug/console output pluggable rather than interleaved
- Reduce cognitive load when reading core orchestration code
- Enable easier testing of individual components
- Prepare for future extensibility (metrics, new state types, etc.)

### Non-Goals
- Changing the concurrency model (asyncio.wait with FIRST_COMPLETED stays)
- Refactoring Agent to a class (may come later)
- Changing the external API (`run_all_agents` signature stays compatible)
- Modifying other modules (parsing.py, policy.py, prompts.py, etc.)

---

## Current Architecture

### File: `src/orchestrator.py` (2728 lines)

| Section | Lines | Description |
|---------|-------|-------------|
| Exceptions | 24-56 | OrchestratorError hierarchy (5 classes) |
| Debug utilities | 58-410 | ~15 functions for saving debug files, errors, transitions |
| Helper functions | 410-598 | State name extraction, transition resolution |
| `run_all_agents()` | 600-1060 | Main loop with concurrency, error handling, retry logic |
| `_step_agent_script()` | 1062-1517 | Script state execution (~450 lines) |
| `step_agent()` | 1519-2415 | Markdown state execution (~900 lines) |
| Transition handlers | 2417-2728 | 6 handlers: goto, reset, function, call, fork, result |

### Problems with Current Structure
1. Debug/console code is interleaved throughout execution logic (~400 lines scattered)
2. Two execution paths (script vs markdown) are separate functions but share patterns
3. Large functions are hard to test in isolation
4. Adding new observers (e.g., metrics) requires modifying core orchestration code

---

## Target Architecture

### Directory Structure

```
src/
├── orchestrator/
│   ├── __init__.py           # Public API: run_all_agents, exceptions
│   ├── errors.py             # Exception classes
│   ├── events.py             # Event dataclasses
│   ├── bus.py                # EventBus implementation
│   ├── workflow.py           # Main loop: run_all_agents
│   ├── transitions.py        # Transition handlers
│   ├── executors/
│   │   ├── __init__.py       # Exports get_executor()
│   │   ├── base.py           # StateExecutor protocol, ExecutionResult
│   │   ├── context.py        # ExecutionContext dataclass
│   │   ├── markdown.py       # MarkdownExecutor
│   │   └── script.py         # ScriptExecutor
│   └── observers/
│       ├── __init__.py       # Exports observer classes
│       ├── debug.py          # DebugObserver
│       └── console.py        # ConsoleObserver
├── console.py                # Keep existing (reporter implementation)
└── ... (other existing modules unchanged)
```

### Backward Compatibility

The public API must remain unchanged:
```python
from src.orchestrator import run_all_agents, OrchestratorError, ClaudeCodeError
```

---

## Component Specifications

### 1. Events (`events.py`)

Events are frozen dataclasses that describe what happened during orchestration. Every event must include `agent_id` to support concurrent agents emitting events simultaneously.

**Required Events:**

| Event | Purpose | Key Fields |
|-------|---------|------------|
| `WorkflowStarted` | Workflow begins | workflow_id, scope_dir, debug_dir, timestamp |
| `WorkflowCompleted` | All agents terminated | workflow_id, total_cost_usd, timestamp |
| `WorkflowPaused` | All agents paused | workflow_id, total_cost_usd, paused_agent_count |
| `StateStarted` | Agent begins executing a state | agent_id, state_name, state_type ("markdown"/"script") |
| `StateCompleted` | Agent finishes a state | agent_id, state_name, cost_usd, total_cost_usd, session_id, duration_ms |
| `TransitionOccurred` | Agent transitions between states | agent_id, from_state, to_state (None if terminated), transition_type, metadata |
| `AgentSpawned` | Fork creates new agent | parent_agent_id, new_agent_id, initial_state |
| `AgentTerminated` | Agent terminates | agent_id, result_payload |
| `ClaudeStreamOutput` | Single JSON object from Claude stream | agent_id, state_name, step_number, json_object |
| `ClaudeInvocationStarted` | Claude Code invocation begins | agent_id, state_name, session_id, is_fork, is_reminder, reminder_attempt |
| `ScriptOutput` | Script execution completes | agent_id, state_name, step_number, stdout, stderr, exit_code, execution_time_ms, env_vars |
| `ToolInvocation` | Claude invokes a tool | agent_id, tool_name, detail (filename, command preview) |
| `ProgressMessage` | Text progress from Claude | agent_id, message |
| `ErrorOccurred` | Error during execution | agent_id, error_type, error_message, current_state, is_retryable, retry_count, max_retries |

Use `@dataclass(frozen=True)` for immutability. Include `timestamp: datetime` on events where timing matters for debug logs.

### 2. Event Bus (`bus.py`)

A simple synchronous publish/subscribe implementation. The bus maintains a dictionary mapping event types to lists of handler functions.

**Key behaviors:**
- `on(event_type, handler)` - Subscribe a handler to an event type
- `off(event_type, handler)` - Unsubscribe a handler
- `emit(event)` - Dispatch event to all handlers registered for that event's type
- Handler exceptions must be caught and logged, not propagated. This ensures observers cannot crash the core orchestration loop.

The bus is synchronous (not async) because events are emitted inline during execution and observers perform quick I/O operations. There's no need for async dispatch.

### 3. Execution Context (`executors/context.py`)

A dataclass that holds shared state passed to executors. This replaces the many parameters currently passed through function calls.

**Contents:**
- `bus: EventBus` - For emitting events
- `workflow_id: str` - Current workflow identifier
- `state_dir: Optional[str]` - Custom state directory if specified
- `default_model: Optional[str]` - Model override from CLI
- `timeout: Optional[float]` - Timeout in seconds for Claude Code
- `dangerously_skip_permissions: bool` - Permission mode flag
- `step_counters: dict[str, int]` - Mutable dict tracking step numbers per agent

Include a helper method `get_next_step_number(agent_id)` that increments and returns the counter for debug file naming.

### 4. Executor Protocol (`executors/base.py`)

Define a `StateExecutor` Protocol with a single async method:

```python
async def execute(self, agent: dict, state: dict, context: ExecutionContext) -> ExecutionResult
```

The `ExecutionResult` dataclass contains:
- `transition: Transition` - The parsed and resolved transition to apply
- `session_id: Optional[str]` - New/updated session ID (None for scripts, which preserve existing)
- `cost_usd: float` - Cost of this invocation

Executors are responsible for:
1. Emitting `StateStarted` at the beginning
2. Performing the actual execution (Claude Code or subprocess)
3. Emitting streaming/output events during execution
4. Parsing and validating the transition
5. Emitting `StateCompleted` at the end
6. Returning the result (transition handlers are called by the workflow loop, not the executor)

### 5. Markdown Executor (`executors/markdown.py`)

Handles `.md` states by invoking Claude Code. This extracts the logic currently in `step_agent()` (lines 1519-2415).

**Responsibilities:**
- Load and render prompt template using existing `load_prompt()` and `render_prompt()`
- Prepare template variables from `pending_result` and `fork_attributes`
- Determine model (frontmatter → CLI default → None)
- Implement reminder prompt retry loop for policy violations
- Invoke Claude Code via `wrap_claude_code_stream()`
- Emit `ClaudeStreamOutput` for each JSON object received
- Emit `ToolInvocation` and `ProgressMessage` events by parsing assistant messages
- Extract session_id from response
- Check for usage limit errors (raise `ClaudeCodeLimitError`)
- Extract output text and cost from results
- Check budget and force termination if exceeded
- Parse transitions using `parse_transitions()`
- Handle implicit transitions via `can_use_implicit_transition()` and `get_implicit_transition()`
- Validate transitions with `validate_single_transition()` and `validate_transition_policy()`
- Resolve abstract state names using `resolve_state()`
- Emit `ErrorOccurred` events for retryable errors during reminder loop

The reminder prompt loop allows up to `MAX_REMINDER_ATTEMPTS` (3) retries when:
- No transition tag is emitted but `allowed_transitions` is defined in policy
- Multiple transitions are emitted
- Transition target doesn't resolve
- Policy violation occurs

### 6. Script Executor (`executors/script.py`)

Handles `.sh` and `.bat` states by running subprocesses. This extracts the logic currently in `_step_agent_script()` (lines 1062-1517).

**Key differences from MarkdownExecutor:**
- No Claude Code invocation - runs subprocess via `run_script()`
- No reminder prompts - errors are fatal (scripts must emit exactly one valid transition)
- Preserves existing `session_id` (scripts don't create sessions)
- Cost is always $0.00
- Emits `ScriptOutput` instead of `ClaudeStreamOutput`

**Responsibilities:**
- Build environment variables using `build_script_env()`
- Execute script via `run_script()` with timeout
- Emit `ScriptOutput` event with stdout, stderr, exit_code, timing, env
- Check exit code (non-zero is fatal `ScriptError`)
- Parse exactly one transition from stdout
- Resolve transition targets

### 7. Executor Factory (`executors/__init__.py`)

Provide a `get_executor(state_filename: str) -> StateExecutor` function that returns the appropriate executor based on file extension. Use `get_state_type()` from prompts module to determine type.

Executors can be singletons since they're stateless - all mutable state is in the agent dict and ExecutionContext.

### 8. Transition Handlers (`transitions.py`)

Move the six existing handlers here: `handle_goto_transition`, `handle_reset_transition`, `handle_function_transition`, `handle_call_transition`, `handle_fork_transition`, `handle_result_transition`.

Wrap them in a single `apply_transition(agent, transition, state, bus)` function that:
1. Deep copies the agent to avoid mutating the original
2. Clears transient fields (`pending_result`, `fork_session_id`, `fork_attributes`)
3. Dispatches to the appropriate handler
4. Emits `TransitionOccurred` event with appropriate metadata
5. For fork: also emits `AgentSpawned` and appends new agent to `state["agents"]`
6. For result with empty stack: emits `AgentTerminated`
7. Returns updated agent dict (or None if terminated)

The handlers themselves remain pure functions that transform agent state. Event emission happens in the wrapper.

### 9. Debug Observer (`observers/debug.py`)

Subscribes to events and writes debug files. Replaces all the `save_*` and `log_*` functions scattered through the current code.

**Subscriptions:**
- `ClaudeStreamOutput` → Append JSON to `{agent}_{state}_{step}.jsonl` (progressive writes with flush)
- `ScriptOutput` → Write `{agent}_{state}_{step}.stdout.txt`, `.stderr.txt`, `.meta.json`
- `TransitionOccurred` → Append to `transitions.log`
- `StateStarted` → Track current state for file naming
- `StateCompleted` → Close any open file handles

**Implementation notes:**
- Maintain `open_files: dict[str, file]` mapping agent_id to open JSONL file handles
- Track current state name per agent for file naming
- Extract state name by stripping `.md`, `.sh`, `.bat` extensions
- All file operations wrapped in try/except - failures logged but don't propagate
- Provide a `close()` method to clean up file handles on workflow completion

### 10. Console Observer (`observers/console.py`)

Bridges events to the existing `ConsoleReporter` implementation. This is a thin adapter layer.

**Subscriptions and mappings:**
- `WorkflowStarted` → `reporter.workflow_started()`
- `WorkflowCompleted` → `reporter.workflow_completed()`
- `WorkflowPaused` → `reporter.workflow_paused()`
- `StateStarted` → `reporter.state_started()` or `reporter.script_started()` based on state_type
- `StateCompleted` → `reporter.state_completed()`
- `ScriptOutput` → `reporter.script_completed()`
- `TransitionOccurred` → `reporter.transition()`
- `ToolInvocation` → `reporter.tool_invocation()`
- `ProgressMessage` → `reporter.progress_message()`
- `ErrorOccurred` → `reporter.error()` with retry info appended if retryable
- `AgentTerminated` → `reporter.agent_terminated()`

### 11. Workflow Loop (`workflow.py`)

The main `run_all_agents()` function, simplified to focus on concurrency and error handling.

**Structure:**
1. Initialize console reporter
2. Create EventBus
3. Create and attach DebugObserver if `debug=True`
4. Create and attach ConsoleObserver if `quiet=False`
5. Read initial state, reset paused agents
6. Emit `WorkflowStarted`
7. Create ExecutionContext
8. Main loop:
   - Check for completion (no agents) → emit `WorkflowCompleted`, delete state, break
   - Check for all paused → emit `WorkflowPaused`, save state, break
   - Create asyncio tasks for agents without running tasks (skip paused agents)
   - `await asyncio.wait(tasks, return_when=FIRST_COMPLETED)`
   - Process completed tasks:
     - Success: update agent in state, clear retry_count
     - `ClaudeCodeLimitError`: pause agent (no retries)
     - `ClaudeCodeTimeoutWrappedError`: retry with counter, pause after MAX_RETRIES
     - Other retryable errors: retry with counter, mark failed after MAX_RETRIES
     - `StateFileError`: re-raise (critical)
     - Other exceptions: log and re-raise
   - Save state for crash recovery
9. Finally: close DebugObserver

**Helper function `_step_agent(agent, state, context)`:**
1. Get executor via `get_executor(agent["current_state"])`
2. `result = await executor.execute(agent, state, context)`
3. `updated_agent = apply_transition(agent, result.transition, state, context.bus)`
4. Update `session_id` from result if not None
5. Return updated_agent

### 12. Errors (`errors.py`)

Move the exception classes here unchanged:
- `OrchestratorError` - Base class
- `ClaudeCodeError` - Claude Code execution failure
- `ClaudeCodeLimitError` - Usage limit (non-retryable)
- `ClaudeCodeTimeoutWrappedError` - Timeout (allows pause/resume)
- `PromptFileError` - Prompt loading failure
- `ScriptError` - Script execution failure
- Re-export `StateFileError` from state module

### 13. Package Init (`__init__.py`)

Export the public API:
- `run_all_agents` from workflow
- All exception classes from errors

---

## Migration Strategy

The migration follows a "strangler fig" pattern: new code is built alongside the old code, tested in isolation, then switched over in a single phase. **The program remains fully functional throughout phases 1-5.** Phase 6 is the switchover point.

### Phase 1: Create Package Structure

**What to do:**
- Create `src/orchestrator/` directory
- Create `__init__.py` that re-exports everything from the old `src/orchestrator.py`:
  ```python
  from src.orchestrator_old import run_all_agents, OrchestratorError, ...
  ```
- Rename `src/orchestrator.py` to `src/orchestrator_old.py` (temporary)

**Tests that must pass:** ALL existing tests. Nothing has changed behaviorally.

**How to verify:** Run `pytest`. Every test that passed before should pass now.

**Program state:** Fully functional. The package just re-exports from the old module.

---

### Phase 2: Extract Infrastructure

**What to do:**
- Create `src/orchestrator/events.py` with all event dataclasses
- Create `src/orchestrator/bus.py` with EventBus implementation
- Create `src/orchestrator/errors.py` with exception classes
- Write NEW unit tests for these modules

**Tests that must pass:**
- ALL existing tests (old code is unchanged and still runs)
- NEW tests in `tests/test_events.py` (events are constructable, frozen)
- NEW tests in `tests/test_bus.py`:
  - `test_subscribe_and_emit` - handler receives event
  - `test_multiple_handlers` - all handlers called
  - `test_handler_exception_caught` - exception logged, not propagated
  - `test_unsubscribe` - handler no longer called
  - `test_emit_no_handlers` - no error when no subscribers

**How to verify:** Run `pytest`. Old tests pass. New unit tests pass.

**Program state:** Fully functional. New modules exist but aren't used yet.

---

### Phase 3: Extract Transition Handlers

**What to do:**
- Create `src/orchestrator/transitions.py`
- Copy the 6 handler functions from `orchestrator_old.py`
- Create `apply_transition()` wrapper (without event emission for now)
- Update `orchestrator_old.py` to import handlers from new location
- Optionally write unit tests for handlers

**Tests that must pass:**
- ALL existing tests (handlers work the same, just imported from new location)
- NEW tests in `tests/test_transitions.py` (optional but recommended):
  - Test each handler in isolation with mock agent/transition/state
  - Test stack operations for function/call/result
  - Test fork ID generation

**How to verify:** Run `pytest`. All tests pass.

**Program state:** Fully functional. Handlers moved but behavior unchanged.

---

### Phase 4: Create Executors

**What to do:**
- Create `src/orchestrator/executors/` package
- Create `base.py` with StateExecutor protocol and ExecutionResult
- Create `context.py` with ExecutionContext
- Create `markdown.py` with MarkdownExecutor
- Create `script.py` with ScriptExecutor
- Create `__init__.py` with `get_executor()`
- Write unit tests for executors using mocks

**Tests that must pass:**
- ALL existing tests (old code still runs, doesn't use executors yet)
- NEW tests in `tests/test_executors.py`:
  - Test MarkdownExecutor with mocked `wrap_claude_code_stream`
  - Test ScriptExecutor with mocked `run_script`
  - Verify correct events are emitted (use mock EventBus)
  - Test transition parsing and resolution
  - Test error cases (timeout, limit, script failure)
  - Test reminder prompt retry logic

**How to verify:** Run `pytest`. All existing tests pass. New executor tests pass.

**Program state:** Fully functional via old code. Executors exist and are tested but not integrated.

**Important:** The executors must be testable in isolation. Inject dependencies (bus, Claude wrapper) so tests can use mocks. Example test structure:
```python
async def test_markdown_executor_emits_state_started():
    bus = MockEventBus()
    context = ExecutionContext(bus=bus, ...)
    executor = MarkdownExecutor()

    with mock_claude_code_stream(returns=[...]):
        result = await executor.execute(agent, state, context)

    assert bus.emitted(StateStarted, agent_id="test")
```

---

### Phase 5: Create Observers

**What to do:**
- Create `src/orchestrator/observers/` package
- Create `debug.py` with DebugObserver
- Create `console.py` with ConsoleObserver
- Write unit tests that emit events and verify observer behavior

**Tests that must pass:**
- ALL existing tests (old code still runs)
- NEW tests in `tests/test_observers.py`:
  - DebugObserver: emit `ClaudeStreamOutput`, verify JSONL file written
  - DebugObserver: emit `ScriptOutput`, verify stdout/stderr/meta files
  - DebugObserver: emit `TransitionOccurred`, verify transitions.log entry
  - ConsoleObserver: emit events, verify reporter methods called (mock reporter)
  - Test observer exception handling (observer failure doesn't propagate)

**How to verify:** Run `pytest`. All existing tests pass. New observer tests pass.

**Program state:** Fully functional via old code. Observers exist and are tested but not integrated.

**Important:** Use temporary directories for DebugObserver file tests. Use mock ConsoleReporter for ConsoleObserver tests.

---

### Phase 6: Integrate and Rewrite Workflow

**What to do:**
- Create `src/orchestrator/workflow.py` with new `run_all_agents()`
- Wire together: EventBus → Observers, Context → Executors
- Update `src/orchestrator/__init__.py` to import from `workflow.py` instead of `orchestrator_old.py`
- This is the switchover point

**Tests that must pass:**
- ALL existing tests - this is the critical validation
- Existing integration tests exercise the full workflow path
- If any existing test fails, the new implementation has a bug

**How to verify:**
1. Run `pytest` - all tests must pass
2. Run a real workflow manually and compare:
   - Does it produce the same state transitions?
   - Are debug files created correctly?
   - Does console output look right?
   - Is cost tracking accurate?
3. Optional: Create a behavioral equivalence test that runs the same workflow through old and new code paths and diffs the outputs

**Program state:** Fully functional via NEW code. Old code is no longer used but still exists.

**Risk mitigation:** If tests fail, you can revert `__init__.py` to import from `orchestrator_old.py` while debugging.

---

### Phase 7: Cleanup

**What to do:**
- Delete `src/orchestrator_old.py`
- Remove any compatibility shims or temporary code
- Update any stale imports elsewhere in codebase
- Final review of all module docstrings

**Tests that must pass:** ALL tests (existing + new unit tests from phases 2-5)

**How to verify:** Run `pytest`. Run manual workflow. Done.

**Program state:** Fully functional. Refactor complete.

---

### Summary: Test Expectations by Phase

| Phase | Existing Tests | New Tests | Program Functional? |
|-------|---------------|-----------|---------------------|
| 1. Package Structure | ALL PASS | None | Yes (via re-export) |
| 2. Infrastructure | ALL PASS | EventBus unit tests | Yes (old code runs) |
| 3. Handlers | ALL PASS | Handler unit tests (optional) | Yes (old code runs) |
| 4. Executors | ALL PASS | Executor unit tests (mocked) | Yes (old code runs) |
| 5. Observers | ALL PASS | Observer unit tests (mocked) | Yes (old code runs) |
| 6. Integration | ALL PASS (critical!) | Behavioral equivalence (optional) | Yes (NEW code runs) |
| 7. Cleanup | ALL PASS | None | Yes (new code only) |

The key invariant: **existing tests must pass after every phase.** If they don't, stop and fix before proceeding.

---

## Testing Strategy

### Unit Tests

**EventBus:**
- Subscribe and emit works
- Multiple handlers for same event
- Handler exception caught and logged
- Unsubscribe works

**Executors:**
- Mock Claude Code / subprocess calls
- Verify correct events emitted
- Verify transition parsing and resolution
- Test error handling paths
- Test reminder prompt logic (markdown only)

**Transition Handlers:**
- Each handler in isolation
- Stack push/pop operations
- Fork ID generation
- Event emission from `apply_transition()`

**Observers:**
- DebugObserver file writing (use temp directory)
- ConsoleObserver event bridging (mock reporter)
- Error resilience

### Integration Tests

All existing tests should pass. Additionally, create a behavioral equivalence test that runs identical workflows through old and new implementations and compares:
- Final state
- Debug file contents
- Captured console output
- Cost tracking

---

## Acceptance Criteria

1. **All existing tests pass** without modification (except import paths if needed)

2. **Behavioral equivalence**: Same inputs produce identical outputs (state, debug files, console, costs)

3. **Public API unchanged**: Existing imports continue to work

4. **File size reduction**: No single file exceeds 600 lines

5. **Test coverage**: New modules have >80% coverage

6. **Documentation**: Each module has docstrings explaining purpose

---

## Estimated File Sizes

| File | Lines (est.) |
|------|--------------|
| `__init__.py` | 30 |
| `errors.py` | 40 |
| `events.py` | 120 |
| `bus.py` | 50 |
| `workflow.py` | 300 |
| `transitions.py` | 250 |
| `executors/__init__.py` | 30 |
| `executors/base.py` | 50 |
| `executors/context.py` | 40 |
| `executors/markdown.py` | 400 |
| `executors/script.py` | 250 |
| `observers/__init__.py` | 10 |
| `observers/debug.py` | 200 |
| `observers/console.py` | 100 |

**Total**: ~1870 lines across 14 files (vs 2728 in one file)

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Behavioral regression | Extensive integration tests; behavioral equivalence testing |
| Performance degradation | Events are lightweight dataclasses; measure before/after |
| Circular imports | Careful module layering; use TYPE_CHECKING for type hints |
| Observer failure affects workflow | EventBus catches all handler exceptions |
| Lost context on errors | Ensure error events include all diagnostic fields |

---

## Open Questions for Implementer

1. **Error file saving**: The current code has `save_error_response()` for detailed error dumps. Should this become an ErrorObserver, or remain in executors? Recommendation: Create ErrorObserver.

2. **Agent paused events**: Currently `reporter.agent_paused()` is called directly in error handling. Add `AgentPaused` event for consistency?

3. **Transition resolution errors**: Currently these can trigger reminder prompts. The executor needs to catch resolution errors and either retry or re-raise based on policy. Ensure this logic is preserved.
