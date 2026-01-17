# Implementation Plan

This document outlines the steps to implement Raymond, prioritizing simplicity
and iterating toward more sophisticated mechanisms.

## Terminology

| Term | Meaning |
|------|---------|
| **Prompt folder** | A directory of markdown files that reference each other via transition tags. Represents the static definition of a workflow. |
| **Orchestrator** | The running Python program. Single-threaded but async, enabling concurrent Claude Code executions. Each orchestrator instance manages exactly one state file. |
| **State file** | JSON file persisting all agent state for one orchestrator run. One orchestrator = one state file. It is an error for multiple orchestrators to access the same state file. |
| **Agent** | A logical thread of execution within the orchestrator. Has a current state (prompt filename) and a return stack. Created initially or via `<fork>`. Terminates when it emits `<result>` with an empty stack. |
| **Workflow** | An abstract chain or DAG of steps designed by a prompt engineer. May refer to the static definition (prompt folder) or the conceptual flow. Context clarifies meaning. |

## Phase 1: Core Infrastructure

### Step 1.1: Transition Tag Parsing

Add a utility function to extract transition tags from Claude Code output.

```python
def parse_transitions(output: str) -> List[Transition]:
    """Extract transition tags from output.

    Returns list of Transition(tag, filename, attributes) objects.
    Recognizes:
        <goto>FILE.md</goto>
        <reset>FILE.md</reset>
        <function return="NEXT.md">EVAL.md</function>
        <call return="NEXT.md">CHILD.md</call>
        <fork next="NEXT.md" item="foo">WORKER.md</fork>
        <result>...</result>

    Returns a list for potential future multi-tag support, but the
    initial implementation requires exactly one tag per response.
    """
```

**Deliverable:** `src/parsing.py` with `parse_transitions()` function and tests.

**Note (initial scope):** Multi-tag semantics (multiple protocol tags in a
single response) are a future feature. The initial implementation should accept
exactly **one** protocol tag per response, as defined in
`wiki/workflow-protocol.md`.

**Parsing detail:** The single protocol tag may appear anywhere in the agent's
final message; parsing should not depend on it being on the last line.

**Path safety:** Tag targets (filenames) must not contain `/` or `\`. The parser
should validate this and raise an exception for malformed filenames. Tests
should cover attempted path traversal (e.g., `../SECRET.md`, `foo/bar.md`).

### Step 1.2: State File Management

Add functions to read/write workflow state JSON files.

```python
def read_state(workflow_id: str) -> dict
def write_state(workflow_id: str, state: dict) -> None
def list_workflows() -> List[str]
```

The state model supports **multi-agent workflows from the beginning**, even
though `<fork>` is not implemented until Phase 3. Each workflow has an `agents`
array, where each agent has its own `id` and `stack`. A single-agent workflow
is simply one with a single entry in `agents`.

```json
{
  "workflow_id": "example-001",
  "scope_dir": "workflows/coding",
  "agents": [
    {
      "id": "main",
      "current_state": "IMPLEMENT.md",
      "session_id": "session_...",
      "stack": []
    }
  ]
}
```

**Deliverable:** `src/state.py` with state management functions and tests.

### Step 1.3: Prompt File Loading

Add a function to load prompt files from a workflows directory.

```python
def load_prompt(scope_dir: str, filename: str) -> str
```

**Deliverable:** Extend `src/state.py` or create `src/prompts.py`.

### Step 1.4: Template Substitution

Add a function to substitute `{{variable}}` placeholders in prompts.

```python
def render_prompt(template: str, variables: dict) -> str
    """Replace {{key}} placeholders with values from variables dict."""
```

Variables come from transition attributes, workflow metadata, and child results.

**Deliverable:** Add to `src/prompts.py` with tests.

## Phase 2: Single Workflow Orchestration

The orchestrator is **async from the beginning**, using `asyncio.wait()` with
`FIRST_COMPLETED` to manage agents. This design supports multi-agent workflows
(via `<fork>`) even though fork is not implemented until Phase 3. Initially,
workflows have a single agent, but the async infrastructure is ready.

### Step 2.1: Basic Orchestrator Loop

Implement the core async event pump:
1. Read state file
2. For each agent, create an async task to step that agent
3. Use `asyncio.wait(..., return_when=FIRST_COMPLETED)` to process completions
4. For each completed task:
   a. Parse output for exactly one protocol tag (`goto`, `reset`, `function`, `call`, `fork`, or `result`)
   b. If zero tags or multiple tags, raise a parse error (re-prompting is a future feature)
   c. Dispatch to tag handler (initially all raise "not implemented")
   d. Update state file
5. Repeat until all agents terminate

**Termination semantics** (implemented incrementally in later steps):
- `<result>` with non-empty stack: pop frame, resume caller session at return state
- `<result>` with empty stack: agent terminates (removed from `agents` array)
- When `agents` array is empty, orchestrator exits

**Deferred:** YAML frontmatter policy enforcement. Initially, we rely on the
model producing compliant tags.

**Deliverable:** `src/orchestrator.py` with async `run_all_agents()` function
and stub handlers for each tag type.

### Step 2.2: Goto (Resume Session)

Implement session continuation using `--resume` flag. This requires:
- Storing session ID in agent state
- Passing `--resume` to Claude Code on subsequent invocations

**Deliverable:** Extend `wrap_claude_code()` to accept `session_id` parameter.
Implement `<goto>` and `<result>` (with empty stack = termination) handlers.

### Step 2.3: Reset (Fresh Start)

Implement context reset:
- Start a fresh Claude Code session (no `--resume`)
- Update session ID in agent state (so future `<goto>` uses new session)
- Clear return stack (log warning if non-empty per protocol)
- Agent continues from new state

**Deliverable:** Handle `<reset>` tag in orchestrator loop.

### Step 2.4: Function (Stateless with Return)

Implement stateless invocation for `<function>` tags. Despite being "stateless"
(no session context), `<function>` is **not** a fire-and-forget operation:

- Push a frame to the agent's return stack (caller session + return state)
- Run the function prompt in a fresh session (no `--resume`)
- When the function emits `<result>`, pop the stack and resume the caller

This differs from a pure evaluator (which returns immediately without stack
manipulation) and from `<call>` (which may branch context from the caller).

**Deliverable:** Implement stack push/pop for `<function>` tags, extend
`<result>` handler for non-empty stack case.

### Step 2.5: Call with Return

Implement call-and-return:
- Push frame to return stack (caller session + return state)
- Start callee, typically by branching context from caller (Claude Code `--fork-session`)
- Callee may iterate through multiple states before returning
- On `<result>`, pop stack and resume caller with result injected via `{{result}}`

Unlike `<function>`, `<call>` preserves context from the caller via history
branching, which is useful when the callee needs to see what the caller was
working on.

**Deliverable:** `handle_call_transition()` function.

## Phase 3: Fork (Multi-Agent)

The async infrastructure from Phase 2 already supports multiple agents. This
phase enables the `<fork>` tag to actually spawn new agents.

### Step 3.1: Fork Implementation

Implement `<fork>` tag handling:
- Create a new agent entry in the `agents` array (same state file)
- New agent starts with empty return stack
- New agent runs `WORKER.md` in a fresh session
- Parent agent continues at `next` state (like `<goto>`)
- Both agents are now active and processed by `asyncio.wait()`

**Deliverable:** Handle `<fork>` tag in orchestrator loop.

## Phase 4: Robustness

### Step 4.1: Error Handling

Add error handling for:
- Claude Code failures (non-zero exit)
- Missing prompt files
- Invalid state files
- Network/API errors

**Deliverable:** Error handling throughout, with recovery strategies.

### Step 4.2: Crash Recovery

Test and refine crash recovery:
- Verify state file consistency
- Handle interrupted Claude Code sessions
- Add startup recovery scan

**Deliverable:** `recover_workflows()` function for startup.

### Step 4.3: Logging and Observability

Add logging to track:
- Workflow state transitions
- Claude Code invocations
- Errors and recovery actions

**Deliverable:** Structured logging throughout.

## Phase 5: Refinements

### Step 5.1: Evaluator Integration

Add support for lightweight evaluators:
- Configurable evaluator prompts
- Override transition based on evaluator result
- Cost budget tracking and limits (track cumulative cost from Claude Code invocations, terminate when budget exceeded)

### Step 5.3: Workflow Configuration

Add optional configuration mechanism:
- YAML frontmatter in prompt files for per-state policy (allowed tags/targets)
- Or separate workflow definition file

### Step 5.3.1: Model Selection

Add support for specifying which Claude model to use for each state or as a default.

**YAML Frontmatter:**
- Add optional `model` field to frontmatter (values: "opus", "sonnet", "haiku")
- Example:
  ```yaml
  ---
  model: sonnet
  allowed_transitions:
    - { tag: goto, target: NEXT.md }
  ---
  ```

**CLI Parameter:**
- Add `--model` parameter to `start` and `run` commands
- Accepts: "opus", "sonnet", or "haiku"
- Provides default model when frontmatter doesn't specify one

**Precedence:**
1. Frontmatter `model` field (highest priority) - overrides CLI default
2. CLI `--model` parameter - used if no frontmatter model
3. None - Claude Code uses its default (opus) when no `--model` flag is passed

**Implementation:**
- Extend `Policy` dataclass to include optional `model` field
- Update `parse_frontmatter()` to extract `model` from YAML
- Update `step_agent()` to determine model based on precedence
- Pass determined model to `wrap_claude_code()` (which already accepts `model` parameter)
- Add `--model` CLI argument to `start` and `run` commands
- Pass `default_model` through `run_all_agents()` to `step_agent()`

**Deliverable:**
- Model selection via frontmatter and CLI
- Precedence logic implemented correctly
- Tests verifying precedence behavior

### Step 5.4: Protocol Reminder on Parse Failure

When the model produces zero or multiple protocol tags, instead of raising an
exception, re-prompt with a short reminder of the expected protocol.

### Step 5.5: Debug Mode

Add a `--debug` command-line flag that preserves complete workflow execution
history for analysis. Since state files are overwritten during execution, debug
mode creates a separate directory structure to retain all information.

**Directory Structure:**
When `--debug` is enabled, create a debug directory for each workflow run:
```
.raymond/debug/{workflow_id}_{timestamp}/
```

Where `{timestamp}` is in format `YYYYMMDD_HHMMSS` (e.g., `20260115_143022`).

**Files Saved:**

1. **Claude Code JSON Outputs**: One JSON file per agent step/state transition
   - Filename format: `{agent_id}_{state_name}_{step_number}.json`
   - Example: `main_START_001.json`, `main_REVIEW_002.json`, `worker_ANALYZE_001.json`
   - Contains the complete raw JSON response from Claude Code (the `results` list
     returned by `wrap_claude_code()`)
   - Step numbers increment per agent (each agent has its own sequence)

2. **State Transition Log**: A single text file `transitions.log` containing:
   - Timestamp for each transition
   - Agent ID
   - Old state → New state
   - Transition type (goto, reset, function, call, fork, result)
   - Transition target
   - Any relevant metadata (session_id changes, stack depth, etc.)
   - Cost information (if available)

**Implementation Details:**

1. **CLI Integration:**
   - Add `--debug` flag to both `start` and `run` commands
   - Pass debug flag through to `run_all_agents()` function
   - Debug flag should be optional (default: False)

2. **Debug Directory Management:**
   - Create debug directory at start of workflow execution
   - Use workflow_id + timestamp to ensure unique directories
   - Create directory structure immediately when debug mode is enabled
   - Store debug directory path in a way accessible to orchestrator functions

3. **Saving Claude Code Outputs:**
   - Hook into `step_agent()` function after `wrap_claude_code()` returns
   - Save the `results` list (raw JSON objects) to a file
   - Include metadata: agent_id, current_state, step_number, timestamp
   - Use JSON formatting with indentation for readability
   - Track step numbers per agent (maintain a counter per agent_id)

4. **State Transition Logging:**
   - Hook into transition handlers (after transition is parsed and validated)
   - Log each transition with timestamp and full context
   - Append to `transitions.log` file (one file per workflow run)
   - Include both the transition requested and the actual state change
   - Log budget overrides if they occur

5. **Error Handling:**
   - If debug directory creation fails, log warning but don't fail workflow
   - If file writing fails, log error but continue workflow execution
   - Debug mode should never cause workflow to fail

**File Format Examples:**

`main_START_001.json`:
```json
[
  {
    "type": "content",
    "text": "I'll help you with this task.\n<goto>NEXT.md</goto>"
  },
  {
    "type": "result",
    "total_cost_usd": 0.05
  }
]
```

`transitions.log`:
```
2026-01-15 14:30:22 [main] START.md -> NEXT.md (goto)
  session_id: session_abc123
  cost: $0.05
  total_cost: $0.05

2026-01-15 14:30:45 [main] NEXT.md -> DONE.md (goto)
  session_id: session_abc123 (resumed)
  cost: $0.03
  total_cost: $0.08

2026-01-15 14:31:02 [main] DONE.md -> (result, terminated)
  session_id: session_abc123
  cost: $0.02
  total_cost: $0.10
  result: "Task completed successfully"
```

**Deliverable:** 
- `--debug` CLI flag on `start` and `run` commands
- Debug directory creation in orchestrator
- JSON output saving in `step_agent()`
- State transition logging throughout orchestrator
- Tests verifying debug files are created correctly

## Testing Strategy

Each phase includes tests:
- Unit tests for individual functions
- Integration tests using sample workflows (see sample-workflows.md)
- The sample workflows use safe operations (no code changes to Raymond itself)

## Suggested Order

1. Start with Phase 1 (infrastructure) - these are independent utilities
2. Phase 2.1-2.2 gives a working orchestrator: basic loop + goto + result
3. Phase 2.3 adds reset (simple, no stack complexity)
4. Test with sample workflows before adding stack-based patterns
5. Phase 2.4-2.5 adds function and call (stack push/pop)
6. Phase 3 enables `<fork>` to spawn additional agents
7. Phase 4-5 as needed based on real usage
