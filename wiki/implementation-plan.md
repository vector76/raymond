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
    Handles:
        <goto>FILE.md</goto>
        <reset>FILE.md</reset>
        <function return="NEXT.md">EVAL.md</function>
        <call return="NEXT.md">CHILD.md</call>
        <fork next="NEXT.md" item="foo">WORKER.md</fork>
        <result>...</result>

    Multiple tags may be present (e.g., multiple <fork> for spawning).
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
def load_prompt(filename: str) -> str
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

Implement the core async loop:
1. Read state file
2. For each agent, create an async task to step that agent
3. Use `asyncio.wait(..., return_when=FIRST_COMPLETED)` to process completions
4. For each completed task:
   a. Parse output for exactly one protocol tag (`goto`, `reset`, `function`, `call`, `fork`, or `result`)
   b. If zero tags or multiple tags, raise a parse error (re-prompting is a future feature)
   c. If `<fork>` tag, raise "not implemented" (until Phase 3)
   d. Apply the tag's semantics per `wiki/workflow-protocol.md`
   e. Update state file
5. Repeat until all agents terminate

**Termination semantics:**
- `<result>` with non-empty stack: pop frame, resume caller session at return state
- `<result>` with empty stack: agent terminates (removed from `agents` array)
- When `agents` array is empty, workflow is complete

**Deferred:** YAML frontmatter policy enforcement. Initially, we rely on the
model producing compliant tags.

**Deliverable:** `src/orchestrator.py` with async `run_workflow()` function.

### Step 2.2: Pattern 1 - Function (Stateless with Return)

Implement stateless invocation for `<function>` tags. Despite being "stateless"
(no session context), `<function>` is **not** a fire-and-forget operation:

- Push a frame to the agent's return stack (caller session + return state)
- Run the function prompt in a fresh session (no `--resume`)
- When the function emits `<result>`, pop the stack and resume the caller

This differs from a pure evaluator (which returns immediately without stack
manipulation) and from `<call>` (which may branch context from the caller).

**Deliverable:** Integrate existing `wrap_claude_code()`, implement stack
push/pop for `<function>` tags.

### Step 2.3: Pattern 3 - Resume (Goto)

Implement session continuation using `--resume` flag. This requires:
- Storing session ID in state file
- Passing `--resume` to Claude Code on subsequent invocations

**Deliverable:** Extend `wrap_claude_code()` to accept `session_id` parameter.

### Step 2.4: Pattern 2 - Call with Return

Implement call-and-return:
- Store parent session ID before calling child
- Run child workflow (may iterate)
- Extract result from child's `<result>` tag
- Resume parent with result injected via `{{result}}` template variable

**Deliverable:** `run_child_workflow()` function.

### Step 2.5: Pattern 4 - Reset (Fresh Start)

Implement context reset:
- Start a fresh Claude Code session (no `--resume`)
- Update session ID in state file (so future `<goto>` uses new session)
- Workflow continues from new state

This is simpler than Pattern 3 - just don't pass `--resume`. The key is
updating the state file's session ID.

**Deliverable:** Handle `<reset>` tag in orchestrator loop.

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
- Iteration counting and limits

### Step 5.2: Result Extraction

Add configurable result extraction from child workflows:
- Tag-based extraction (e.g., `<result>...</result>`)
- Summary generation prompts

### Step 5.3: Workflow Configuration

Add optional configuration mechanism:
- YAML frontmatter in prompt files for per-state policy (allowed tags/targets)
- Or separate workflow definition file

### Step 5.4: Protocol Reminder on Parse Failure

When the model produces zero or multiple protocol tags, instead of raising an
exception, re-prompt with a short reminder of the expected protocol.

## Testing Strategy

Each phase includes tests:
- Unit tests for individual functions
- Integration tests using sample workflows (see sample-workflows.md)
- The sample workflows use safe operations (no code changes to Raymond itself)

## Suggested Order

1. Start with Phase 1 (infrastructure) - these are independent utilities
2. Phase 2.1-2.2 gives us a working async orchestrator with single agent
3. Test with sample workflows before adding complexity
4. Phase 2.3-2.5 adds session management patterns (goto, call, reset)
5. Phase 3 enables `<fork>` to spawn additional agents
6. Phase 4-5 as needed based on real usage
