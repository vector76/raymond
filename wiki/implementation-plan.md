# Implementation Plan

This document outlines the steps to implement Raymond, prioritizing simplicity
and iterating toward more sophisticated mechanisms.

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
        <function model="haiku">FILE.md</function>
        <call return="NEXT.md">CHILD.md</call>
        <fork item="foo">WORKER.md</fork>

    Multiple tags may be present (e.g., multiple <fork> for spawning).
    """
```

**Deliverable:** `src/parsing.py` with `parse_transitions()` function and tests.

**Note (initial scope):** Multi-tag semantics (e.g., multiple transitions in a
single response) are a future feature. The initial implementation should accept
exactly one transition tag per response, or no transition tag (in which case a
`<result>...</result>` tag is required).

### Step 1.2: State File Management

Add functions to read/write workflow state JSON files.

```python
def read_state(workflow_id: str) -> dict
def write_state(workflow_id: str, state: dict) -> None
def list_workflows() -> List[str]
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

### Step 2.1: Basic Orchestrator Loop

Implement the core loop for a single workflow:
1. Read state file
2. Load prompt for current state
3. Invoke Claude Code
4. Parse transition tag
5. If no transition tag is present, require a `<result>...</result>` tag; if
   neither is present, re-prompt with a short reminder to emit a valid tag
6. Update state file
6. Repeat or terminate

**Deliverable:** `src/orchestrator.py` with `run_workflow()` function.

### Step 2.2: Pattern 1 - Pure Function

Implement stateless invocation (no session context). This is the simplest
pattern and already supported by the existing `wrap_claude_code()`.

**Deliverable:** Integrate existing wrapper, add pattern selection logic.

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

## Phase 3: Concurrent Workflows

### Step 3.1: Async Orchestrator

Refactor orchestrator to manage multiple workflows concurrently using
`asyncio.wait()` with `FIRST_COMPLETED`.

**Deliverable:** `run_orchestrator()` async function managing multiple workflows.

### Step 3.2: Independent Spawn

Implement workflow spawning:
- Create new state file for spawned workflow
- Add to active workflow set
- Parent continues without waiting

**Deliverable:** `spawn_workflow()` function.

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
- YAML frontmatter in prompt files
- Or separate workflow definition file

## Testing Strategy

Each phase includes tests:
- Unit tests for individual functions
- Integration tests using sample workflows (see sample-workflows.md)
- The sample workflows use safe operations (no code changes to Raymond itself)

## Suggested Order

1. Start with Phase 1 (infrastructure) - these are independent utilities
2. Phase 2.1-2.2 gives us a working single-workflow orchestrator
3. Test with sample workflows before adding complexity
4. Phase 2.3-2.4 adds session management
5. Phase 3 adds concurrency
6. Phase 4-5 as needed based on real usage
