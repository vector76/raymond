# Orchestration Design

## Terminology

| Term | Meaning |
|------|---------|
| **Prompt folder** | A directory (or zip archive) of state files (`.md` prompts or `.sh`/`.bat` scripts) that reference each other via transition tags. Represents the static definition of a workflow. |
| **Orchestrator** | The running Python program. Single-threaded but async, enabling concurrent Claude Code executions. Each orchestrator instance manages exactly one state file. |
| **State file** | JSON file persisting all agent state for one orchestrator run. One orchestrator = one state file. It is an error for multiple orchestrators to access the same state file. |
| **Agent** | A logical thread of execution within the orchestrator. Has a current state (prompt filename) and a return stack. Created initially or via `<fork>`. Terminates when it emits `<result>` with an empty stack. |
| **Workflow** | An abstract chain or DAG of steps designed by a prompt engineer. May refer to the static definition (prompt folder) or the conceptual flow. Context clarifies meaning. |

## The Ralph Approach

Ralph is a simple bash loop that runs Claude Code repeatedly with a fixed
prompt:

```bash
while :; do cat PROMPT.md | claude-code; done
```

Each iteration gets a fresh context window. This works well when:
- Tasks are self-contained and completable in one shot
- No state needs to carry between iterations
- The prompt file contains everything needed

Limitations:
- Always-fresh context means rebuilding understanding each iteration
- No selective preservation of useful context
- Cannot orchestrate multi-phase workflows with different prompts
- No branching based on outcomes

## Raymond's State Machine Model

Raymond treats workflows as a state machine where:
- Each state is a markdown prompt file (`.md`) or a shell script (`.sh`/`.bat`)
- Transitions are declared within the prompts/scripts themselves
- The Python orchestrator parses transition tags and routes accordingly

**Markdown vs. Script states:** Markdown states are interpreted by Claude Code
(LLM execution), while script states execute directly (no LLM). Both emit the
same transition tags. Scripts are efficient for deterministic operations like
polling, builds, and data processing. See `docs/bash-states.md` for details.

**Protocol note:** The authoritative protocol (including the return stack model
and workflow scoping) is defined in `docs/workflow-protocol.md`.

Two key protocol points worth calling out here:
- The agent's final message must contain **exactly one** protocol tag, and that
  tag may appear **anywhere** in the message.
- Each state may optionally declare a YAML frontmatter policy (allowed tags /
  allowed targets). The orchestrator enforces this as part of interpreting the
  workflow.

### Self-Describing Transitions

Prompts instruct the AI how to signal transitions using distinct tags:

```markdown
Review the code for issues. If you find problems, fix them. If everything
looks good, end your response with <goto>COMMIT.md</goto>
```

The Python orchestrator:
1. Parses transition tags (`<goto>`, `<reset>`, `<call>`, `<fork>`, `<function>`) from output
2. Reads the referenced file to get the next prompt
3. Launches the next Claude Code session with that prompt
4. Acts as an interpreter for a small "workflow language" defined in markdown: it follows the declared transitions and enforces the rules of what transitions are allowed

**Workflow scoping (important):**
- A workflow is started from a specific prompt file path, a directory, or a zip
  archive (e.g. `workflows/coding/START.md`, `workflows/coding/`, or
  `workflows/coding.zip`).
- Transitions that reference a filename (e.g. `<goto>REVIEW.md</goto>`) are
  resolved **only within the workflow scope** (the starting file's directory, or
  the zip archive).
- Cross-scope transitions are not allowed. This keeps workflow collections
  self-contained and prevents name collisions.

**Path safety rule:** Transition targets are filenames, not paths. Tag targets
must not contain `/` or `\` anywhere.

This keeps workflow definitions in markdown, not Python code.

**Tag types:**
- `<goto>FILE.md</goto>` - continue in same context
- `<reset>FILE.md</reset>` - discard context, start fresh, continue workflow
- `<function return="NEXT.md">EVAL.md</function>` - stateless evaluation with return
- `<call return="NEXT.md">CHILD.md</call>` - isolated subtask with return
- `<fork next="NEXT.md" item="data">WORKER.md</fork>` - independent spawn (parent continues at `next`)
- `<result>...</result>` - return/terminate

### Branching and Looping

States can loop or branch based on output. For example, a review state might
iterate up to five times, with the prompt instructing:

```markdown
If no issues are found, respond with <goto>COMMIT.md</goto>
Otherwise, fix the issues and respond with <goto>REVIEW.md</goto>
```

A lightweight evaluator (pattern match or small model) can also inspect output
to determine branches. This enables conditions like "max $10.00 cost budget" at the
Python level - if the AI outputs `<goto>REVIEW.md</goto>` but the orchestrator
detects we've exceeded the cost budget, it can override and terminate the workflow
instead.

**Cost Budget Limits:** The orchestrator tracks the cumulative cost of all Claude Code
invocations across a workflow. By default, workflows have a $10.00 budget limit (configurable
via the `--budget` CLI flag when starting a workflow). When the total cost exceeds the budget,
the orchestrator overrides any transition the AI requests and terminates the workflow cleanly.
This provides a safety mechanism to prevent runaway costs from infinite loops or unexpectedly
expensive operations. The cost is extracted from Claude Code's JSON response (`total_cost_usd`
field) and accumulated in the workflow state file.

**Permission Mode:** By default, Raymond invokes Claude with `--permission-mode acceptEdits`,
which allows Claude to edit files without prompting but still requires permission for certain
dangerous operations. For fully autonomous workflows that need to run without any permission
prompts, you can use the `--dangerously-skip-permissions` flag:

```bash
raymond workflow.md --dangerously-skip-permissions
```

⚠️ **WARNING:** This flag passes `--dangerously-skip-permissions` to Claude, which allows it
to execute any action without prompting for permission. Only use this for trusted workflows
in controlled environments. This flag is intended for batch processing and CI/CD scenarios
where human interaction is not possible.

## Context Management: The Call Stack Parallel

Traditional programs use a call stack for function calls:
- Calling a function pushes a new stack frame with local variables
- The function executes in isolation
- Returning pops the frame, discarding locals, passing only the return value
- The caller resumes with its original context plus the result

Raymond achieves similar behavior using Claude Code session mechanisms (e.g.
`--resume`) and, where useful, Claude Code's history-branching flag (`--fork-session`).

### Claude Code `--fork-session` as History Branching (Implementation Detail)

```
main context: "Create plan for issue 195"
    │
    ├── (Claude Code --fork-session) → child context: "Refine the plan iteratively"
    │          (may iterate multiple times, accumulating noise)
    │          returns: "Plan finalized in plan-195.md"
    │
    resume main ← "Plan complete. Now implement per plan-195.md"
```

The child context is like a function's stack frame:
- It has its own "local variables" (conversation history, iterations, mistakes)
- This noise stays contained in the child
- Only the clean result propagates back

### Resume as Return

When a called child task completes:
1. The child's prompt instructs it to end with a `<result>` tag containing a
   summary of what was accomplished
2. Python extracts this result from the child's final output
3. Resumes the parent context with `--resume`
4. Injects the result into the return state's prompt via `{{result}}` template

The parent context never sees the messy iterations - just like a caller never
sees a function's internal variables, only the return value.

**Important naming note:** This section is about Claude Code's `--fork-session` flag
(branching conversation history). It is unrelated to Raymond's `<fork>...</fork>`
transition tag, which represents spawning an independent agent (Unix fork()
analogy).

### When to Fork vs. Continue

**Fork** (isolated context) when:
- The subtask may iterate or produce noise
- You want to discard intermediate steps
- The parent only needs the final result

**Continue** (same context) when:
- History is valuable for the next step
- Creating a commit message needs to see what was implemented
- Continuity matters more than cleanliness

## The Tool-Calling Parallel

There is a useful parallel between Raymond's state transitions and the standard
tool-calling pattern in LLM applications.

### Standard Tool Calling

In typical LLM tool use, the flow is:

1. Model receives a prompt and context
2. Model decides it needs to call a tool (e.g., "search the web for X")
3. Model outputs a structured tool request instead of a final response
4. Client intercepts the request and executes the tool
5. Tool result is injected back into the **same context** as a `tool_response`
6. Model continues with the augmented context

The key characteristic: the tool result returns to the same conversation
context. The model "pauses" while the tool runs, then resumes with new
information.

### Raymond's Transition-as-Tool-Call

Raymond's state transitions follow a similar pattern, but with a crucial
difference:

1. Model receives a prompt and context
2. Model completes its task and signals a transition (e.g.,
   `<goto>REVIEW.md</goto>`)
3. Model outputs a final response (the session ends)
4. Orchestrator intercepts the transition tag
5. Orchestrator reads REVIEW.md to get the next state's prompt
6. Orchestrator launches a new Claude Code session with that prompt
7. The cycle continues until a terminal `<result>...</result>` with an empty
   return stack (workflow termination)

The key difference: instead of injecting results into the same context, the
transition **ends the current session**. The orchestrator controls whether the
next session starts fresh, forks from the current context, or resumes a parent
context.

In effect, the model is "calling a tool" where the tool is: "end this session
and start another Claude Code session with a different prompt."

### Relationship to Sub-Agents

Claude Code has built-in sub-agent capabilities that may handle some of this
internally. However, Raymond provides explicit control over:

- Which invocation pattern to use (see below)
- What context carries forward vs. gets discarded
- How results flow between sessions
- Branching logic based on outcomes

This explicit control is valuable when you need predictable, auditable behavior
in multi-step workflows.

## Invocation Patterns

Raymond supports five transition types, each with different context semantics:

| Tag | Context behavior | Session | Programming analogy |
|-----|-----------------|---------|-------------------|
| `<goto>` | Preserved | Resume current | Sequential code in same scope |
| `<reset>` | Discarded (stack cleared) | Fresh | New function after writing results to disk |
| `<call>` | Child branches from caller | Branched, caller resumed on return | Function call with stack frame |
| `<function>` | Child starts fresh | Fresh, caller resumed on return | Pure function `f(x) → y` |
| `<fork>` | Worker starts fresh | Fresh (independent lifecycle) | Unix `fork()` — independent process |

The transition type is determined by the tag itself. Each run must emit exactly
one protocol tag.

For detailed guidance on when to use each pattern and complete examples, see
[authoring-guide.md](authoring-guide.md#choosing-the-right-pattern).

### Implementation Details

**Goto:** Resumes the existing Claude Code session via `--resume`.

**Reset:** Creates a new session. Updates the session ID in the state file for
future `<goto>` transitions. Clears the return stack.

**Call:** Pushes a return frame (caller's session + return state) onto the
stack, then starts the child via `--fork-session` (branching from the caller's
context). When the child emits `<result>`, the orchestrator pops the frame and
resumes the caller.

**Function:** Same stack behavior as `<call>`, but the child starts in a fresh
session (no context inheritance).

**Fork:** Creates a new agent entry in the state file with an empty return
stack and fresh session. The parent continues at `next` via resume (like
`<goto>`). See the Fork section below for naming and lifecycle details.

## Persistent State and Crash Recovery

The Python orchestrator should be mostly stateless, keeping critical workflow
state in the filesystem rather than in memory. This shares a virtue with the
Ralph loop: if the process crashes, minimal context is lost.

### The Problem

Without persistent state, a crash mid-workflow creates problems:
- An issue is claimed but no record exists of which workflow owns it
- A git branch is created but the session ID is lost
- The current state (which prompt file) is unknown
- Partially completed work cannot be resumed

### The Solution: State File

Each active workflow writes its state to a lightweight JSON file. The schema
shown here is illustrative and may evolve during implementation:

```json
{
  "workflow_id": "issue-195-abc123",
  "current_state": "IMPLEMENT.md",
  "session_id": "session_2024-01-15_abc123",
  "parent_session_id": "session_2024-01-15_xyz789",
  "started_at": "2024-01-15T10:30:00Z",
  "iteration_count": 3,
  "metadata": {
    "issue": "bd-195",
    "branch": "feature/bd-195-user-auth"
  }
}
```

**Key fields:**
- `current_state`: The prompt file for the current state
- `session_id`: Claude Code session ID for `--resume`
- `parent_session_id`: For returning from `<call>` subtasks to parent
- `metadata`: Workflow-specific data (issue numbers, branch names, etc.)

### Stateless Orchestrator Design

The orchestrator operates as a simple loop:

```
1. Read state file
2. Determine next action based on current_state
3. Invoke Claude Code (with appropriate pattern)
4. Parse output for transition tags
5. Update state file with new state
6. Repeat until terminal state
```

**On "stuck" states:** In headless mode, Claude Code should not wait for human
input. However, processes can still hang (e.g., slow network, tool deadlock).
The orchestrator should apply timeouts and retry logic. In streaming mode,
timeouts can be based on "no output seen for N seconds" rather than a single
hard limit for an entire long run.

If the orchestrator crashes at any point:
- Steps 1-2: No changes made, restart picks up where it left off
- Steps 3-4: Claude Code session exists, can be resumed or restarted
- Step 5: State file has old state, but session ID allows recovery
- Step 6: Clean state, restart continues normally

The main risk is if the Python process dies while Claude Code is mid-execution
(e.g., editing files). In practice this is rare and usually recoverable - the
session can be resumed, or at worst the workflow restarts from the current
state with a fresh session.

### Multiple Concurrent Workflows

A single Python process manages all active workflows using async/await. No
worker threads, multiprocessing, or separate application instances are needed.
The orchestrator runs multiple Claude Code invocations concurrently and
processes each completion as it arrives:

```python
async def run_orchestrator():
    pending = {asyncio.create_task(step_workflow(wf)): wf
               for wf in active_workflows}

    while pending:
        # Wait for ANY task to complete (not all)
        done, _ = await asyncio.wait(pending.keys(), return_when=FIRST_COMPLETED)

        for task in done:
            wf = pending.pop(task)
            result = task.result()
            # Process this completion immediately
            # Maybe fork new agents, update state file, etc.
            if next_state := get_next_state(result):
                new_task = asyncio.create_task(step_workflow(wf))
                pending[new_task] = wf
```

Each workflow has its own state file:

```
.raymond/
  workflows/
    issue-195-abc123.json
    issue-196-def456.json
    issue-197-ghi789.json
```

This keeps the architecture simple: one Python process, multiple async Claude
Code invocations, state persisted to disk for crash recovery.

## Fork: Spawning Independent Agents

Beyond the call-and-return pattern (`<call>`), Raymond supports spawning
independent agents that run in parallel. This is what the `<fork>...</fork>`
transition tag represents (Unix fork() analogy), and it is distinct from Claude
Code's `--fork-session` flag (which branches conversation history).

### Unix fork() Analogy

In Unix, `fork()` creates a child process that runs independently:
- Parent and child execute concurrently
- Each has its own execution path
- They don't wait for each other (unless explicitly synchronized)

Raymond achieves similar behavior with agents:

```
Agent A (dispatcher)                Agent B (spawned)
┌─────────────────────┐            ┌─────────────────────┐
│ Check for issues    │            │                     │
│ Found issue 195     │──fork───→  │ Work on issue 195   │
│ Continue checking   │            │ Plan, implement...  │
│ Found issue 196     │──fork───→  │ Commit, close       │
│ Continue checking   │            └─────────────────────┘
│ ...                 │            ┌─────────────────────┐
└─────────────────────┘            │ Agent C (spawned)   │
                                   │ Work on issue 196   │
                                   │ ...                 │
                                   └─────────────────────┘
```

All agents exist within the same orchestrator instance and state file.

### Contrast with `<call>`

| Aspect | `<call>` | `<fork>` |
|--------|------------------|------|
| Parent waits? | Yes, for result | No, continues immediately |
| Result returns? | Yes, via resume | No, independent completion |
| Lifecycle | Tied to caller's stack | Fully independent |
| Use case | Subtasks | Parallel workstreams |

### Implementation

Fork adds a new agent to the same state file:

```python
# On <fork next="NEXT.md" cd="/path/to/worktree" item="foo">WORKER.md</fork>
# Extract state name and create compact abbreviation
state_name = transition.target.replace('.md', '').lower()[:6]  # e.g., "worker"
# Fork counter ensures unique names even after workers terminate
fork_counters = state.setdefault("fork_counters", {})
fork_counters[parent_id] = fork_counters.get(parent_id, 0) + 1
worker_id = f"{parent_id}.{state_name}{fork_counters[parent_id]}"

new_agent = {
    "id": worker_id,  # e.g., "main_worker1", "main_worker1_analyz1", etc.
    "current_state": "WORKER.md",
    "session_id": None,  # Fresh session
    "stack": [],         # Empty return stack
    "cwd": "/path/to/worktree",  # Per-agent working directory (from cd attribute)
    "fork_attributes": {"item": "foo"}  # Available as template variables
}
state["agents"].append(new_agent)

# Parent agent continues at NEXT.md (like goto)
parent_agent["current_state"] = "NEXT.md"
```

**Working directory (`cd` attribute):** The `cd` attribute on `<fork>` sets the
worker's working directory. The worker's Claude Code and script subprocesses
will execute in this directory instead of the orchestrator's directory. The
parent agent's working directory is unaffected. The `cd` attribute is consumed
by the orchestrator and excluded from `fork_attributes`. The same attribute is
also supported on `<reset>` to change the current agent's working directory.
See `docs/workflow-protocol.md` for full details.

**Agent Naming Strategy:**

Forked agents are named using a compact hierarchical dot notation with
state-based abbreviations. Names use persistent counters stored in the state
file's `fork_counters` dictionary. Each parent agent maintains its own counter
that increments for each fork, ensuring unique names even if previous workers
have terminated and been removed from the agents array.

The naming pattern is: `{parent_id}_{state_abbrev}{counter}` where:
- `state_abbrev` is the first 6 characters of the target state name (lowercase, `.md` removed)
- `counter` starts at 1 and increments for each fork from that parent

Examples:
- First fork from `main` to `WORKER.md`: `main_worker1`
- Second fork from `main` to `WORKER.md`: `main_worker2`
- Fork from `main` to `ANALYZE.md`: `main_analyz1`
- Nested fork from `main_worker1` to `ANALYZE.md`: `main_worker1_analyz1`
- Deeply nested: `main_worker1_analyz1_proces1` (forking to `PROCESS.md`)

This approach guarantees that:
- Agent names are always unique within a workflow
- Names are compact and readable even with deep nesting
- Names are informative, showing both hierarchy and target state
- Names use underscores, making them valid identifiers
- Names remain consistent and traceable in debug logs
- No name reuse occurs even after workers terminate
- The relationship between parent and worker is clear from the name

The spawned agent:
- Is added to the `agents` array in the same state file
- Managed by the same orchestrator instance
- Runs concurrently via `asyncio.wait()`
- Terminates when it emits `<result>` with an empty stack

### Use Cases

**Timed polling loop:**
```
1. [Loop] Check issue tracker every 5 minutes
2. [Fork] For each new issue, fork an agent to handle it
3. [Continue] Dispatcher agent continues checking
```

**Parallel batch processing:**
```
1. [Start] Given list of 10 files to refactor
2. [Fork] Fork an agent for each file
3. [Complete] Each agent terminates independently when done
```

### Coordination (Optional)

Forked agents are independent by default, but can coordinate if needed:
- **Shared files**: Write to common files in the workspace
- **File locks**: Prevent conflicts on shared resources
- **Completion markers**: Write a `.done` file when finished

## Summary

| Aspect | Ralph | Raymond |
|--------|-------|---------|
| Context | Always fresh | Selective (fork/resume) |
| Workflow | Single repeated prompt | Multi-state machine |
| Branching | None | Declared in prompts |
| State carry | None | Via resume to parent |
| Configuration | One prompt file | Multiple markdown files |
| Invocation patterns | One (fresh) | Five (goto/reset/call/fork/function) |
| Crash recovery | Restart from scratch | Resume via state file |
| Concurrency | Single loop | Multiple independent workflows |
