# Orchestration Design

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
- Each state is a markdown prompt file
- Transitions are declared within the prompts themselves
- The Python orchestrator parses transition tags and routes accordingly

### Self-Describing Transitions

Prompts instruct the AI how to signal transitions using distinct tags:

```markdown
Review the code for issues. If you find problems, fix them. If everything
looks good, end your response with <goto>COMMIT.md</goto>
```

The Python orchestrator:
1. Parses transition tags (`<goto>`, `<call>`, `<fork>`, `<function>`) from output
2. Reads the referenced file to get the next prompt
3. Launches the next Claude Code session with that prompt
4. Remains agnostic to workflow logic - it just follows the declared transitions

This keeps workflow definitions in markdown, not Python code.

**Tag types:**
- `<goto>FILE.md</goto>` - continue in same context (Pattern 3)
- `<function>FILE.md</function>` - stateless evaluation (Pattern 1)
- `<call return="NEXT.md">CHILD.md</call>` - fork with return (Pattern 2)
- `<fork item="data">WORKER.md</fork>` - independent spawn

### Branching and Looping

States can loop or branch based on output. For example, a review state might
iterate up to five times, with the prompt instructing:

```markdown
If no issues are found, respond with <goto>COMMIT.md</goto>
Otherwise, fix the issues and respond with <goto>REVIEW.md</goto>
```

A lightweight evaluator (pattern match or small model) can also inspect output
to determine branches. This enables conditions like "max 5 iterations" at the
Python level - if the AI outputs `<goto>REVIEW.md</goto>` but the orchestrator
detects we've hit the iteration limit, it can override and transition to
COMMIT.md instead.

## Context Management: The Call Stack Parallel

Traditional programs use a call stack for function calls:
- Calling a function pushes a new stack frame with local variables
- The function executes in isolation
- Returning pops the frame, discarding locals, passing only the return value
- The caller resumes with its original context plus the result

Raymond achieves similar behavior using Claude Code's `--fork` and `--resume`:

### Fork as Function Call

```
main context: "Create plan for issue 195"
    │
    ├── fork → child context: "Refine the plan iteratively"
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

When a forked task completes:
1. The child's prompt instructs it to end with a summary (e.g., "End your
   response with a one-line summary of what was accomplished")
2. Python extracts this summary from the child's final output
3. Resumes the parent context with `--resume`
4. Passes the summary as the next prompt to the parent

The parent context never sees the messy iterations - just like a caller never
sees a function's internal variables, only the return value.

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
7. The cycle continues until a terminal state (no transition tag)

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

## The Three Invocation Patterns

Raymond supports three distinct patterns for invoking Claude Code sessions.
Each pattern serves different needs and maps to familiar programming concepts.

The workflow definition (or orchestrator configuration) determines which
pattern to use for each state transition. The AI signals *what* state to
transition to; the orchestrator decides *how* to invoke it.

Pattern selection is determined by the tag itself:
- `<goto>FILE.md</goto>` - Pattern 3 (resume in same context)
- `<function>FILE.md</function>` - Pattern 1 (stateless)
- `<call return="NEXT.md">CHILD.md</call>` - Pattern 2 (fork with return)
- `<fork item="data">WORKER.md</fork>` - independent spawn

Each tag name is self-documenting: `<goto>` continues, `<call>` invokes a
subroutine, `<function>` evaluates statelessly, `<fork>` spawns independently.

### Pattern 1: Pure Function (No Context)

**Invocation:** Launch Claude Code with only a prompt, no session history.

**Characteristics:**
- Completely stateless - no prior context
- Output depends only on the prompt (and model behavior)
- Fast and cheap (minimal tokens)
- Reproducible setup (same prompt always starts from the same state)

**Programming analogy:** A pure function like `f(x) -> y`. Given the same input,
the function's behavior is self-contained. It cannot access variables from the
caller's scope.

**Example use cases:**
- **Evaluators**: "Given this message, does it indicate the task is complete?
  Respond YES or NO."
- **Classifiers**: "Categorize this error message: SYNTAX, RUNTIME, or LOGIC."
- **Decision points**: "Should this code change be committed or does it need
  more review? Respond COMMIT or REVIEW."

**Implementation:**
```python
# Invoke with no session context
result = await wrap_claude_code(prompt, model="haiku")
```

**When to use:**
- The task requires no context beyond what's in the prompt
- You want fast, cheap evaluations
- You need a "circuit breaker" or decision point in the workflow
- Isolation is more important than continuity

### Pattern 2: Fork with Return (Isolated Subtask)

**Invocation:** Fork from current session, execute subtask, return result to
parent via resume.

**Characteristics:**
- Child session inherits parent's context at fork point
- Child can iterate, make mistakes, accumulate noise
- Only the final result returns to the parent
- Parent's context stays clean

**Programming analogy:** A function call with a stack frame. The function
receives parameters (context at fork), creates local variables (intermediate
work), and returns a value (final result). When the function returns, its stack
frame is discarded - the caller never sees the local variables.

```
caller's scope                    function's scope
┌─────────────────┐              ┌─────────────────┐
│ variables...    │   call →     │ local vars...   │
│                 │              │ intermediate... │
│                 │   ← return   │ more work...    │
│ + result        │              │ final result    │
└─────────────────┘              └─────────────────┘
                                   (discarded)
```

**Example use cases:**
- **Iterative refinement**: Refine a plan through multiple passes, return only
  the final plan.
- **Implementation with debugging**: Implement a feature, fix failing tests
  through several attempts, return only "implementation complete."
- **Research tasks**: Explore a codebase to answer a question, return only the
  answer (not the exploration steps).

**Implementation:**
```python
# Fork from current session
child_result = await run_forked_session(parent_session_id, subtask_prompt)

# Resume parent with the result
await resume_session(parent_session_id, f"Subtask complete: {child_result}")
```

**When to use:**
- The subtask may require multiple iterations
- Intermediate steps would pollute the parent's context
- You want clean separation between "doing the work" and "using the result"
- The parent context is valuable and should be preserved

### Pattern 3: Resume with New State (Goto)

**Invocation:** Resume an existing session with a new prompt, continuing in the
same context.

**Characteristics:**
- Context accumulates across states
- No isolation - all history is visible
- Efficient when history is needed
- Can become cluttered over many transitions

**Programming analogy:** Sequential execution within a single function scope.
Each new state is like reaching the next block of code - you're still in the
same scope with access to all prior variables. Alternatively, think of it like
a `goto` to a new label within the same function: execution continues but at a
different point, with full access to accumulated state.

```
same scope throughout
┌─────────────────────────────────────┐
│ state A work...                     │
│ goto B                              │
│ state B work...  (sees A's work)    │
│ goto C                              │
│ state C work...  (sees A and B)     │
└─────────────────────────────────────┘
```

**Example use cases:**
- **Commit after implementation**: The commit message needs to see what was
  implemented.
- **Sequential phases**: Plan → implement → test, where each phase benefits
  from seeing the prior work.
- **Conversational continuity**: Follow-up questions that reference prior
  discussion.

**Implementation:**
```python
# Resume existing session with new prompt (next state)
await resume_session(session_id, new_state_prompt)
```

**When to use:**
- Later steps need visibility into earlier work
- Context continuity is more valuable than cleanliness
- The workflow is linear without need for isolation
- You're doing a "handoff" rather than a "subtask"

## Choosing the Right Pattern

| Question | Yes → | No → |
|----------|-------|------|
| Does the task need any prior context? | Pattern 2 or 3 | Pattern 1 |
| Should intermediate work be discarded? | Pattern 2 | Pattern 3 |
| Is this a decision/evaluation point? | Pattern 1 | Pattern 2 or 3 |
| Will there be messy iterations? | Pattern 2 | Pattern 3 |
| Does the next step need this step's history? | Pattern 3 | Pattern 1 or 2 |

### Combined Example

A complete workflow might use all three patterns:

```
1. [Pattern 1] Evaluator decides which issue to work on → "issue 195"
       ↓
2. [Fresh start] Main session begins: "Work on issue 195"
       ↓           (this session will be resumed later)
3. [Pattern 2] Fork: "Create and refine plan" (iterates 3x)
       ↓ returns: "Plan complete in plan-195.md"
4. [Pattern 3] Resume main: "Plan complete. Now implement."
       ↓
5. [Pattern 2] Fork: "Implement and fix until tests pass" (iterates 5x)
       ↓ returns: "Implementation complete, all tests passing"
6. [Pattern 3] Resume main: "Implementation complete. Review and commit."
       ↓
7. [Pattern 1] Evaluator: "Is this ready to commit?" → YES
       ↓
8. [Pattern 3] Resume main: "Commit and close issue."
       ↓
   [Terminal] No transition tag - workflow complete
```

The main context stays clean (steps 2, 4, 6, 8) while messy work happens in
isolated forks (steps 3, 5) and quick decisions use stateless calls (steps 1,
7).

**Note on step 2:** Starting a new main session is technically a fresh start
(like Pattern 1), but with the intent to resume it later. Pattern 1 is for
stateless evaluations that won't be resumed. The distinction is about intent:
Pattern 1 sessions are disposable; main sessions are persistent.

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

Each active workflow writes its state to a lightweight JSON file:

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
- `parent_session_id`: For returning from forked subtasks
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

**Natural resistance to "stuck" states:** Claude Code always terminates - it
either completes its task, hits an error, or reaches a stopping point. There's
no risk of it waiting indefinitely for human input (in headless mode). This
means the orchestrator doesn't need watchdog timers or timeout logic to detect
stuck processes. Completion of a Claude Code invocation is the natural signal
to take the next action.

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
            # Maybe spawn new workflows, update state file, etc.
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

## Independent Session Spawning

Beyond the fork-and-return pattern (Pattern 2), Raymond supports spawning
fully independent workflows that run in parallel with the original.

### Unix fork() Analogy

In Unix, `fork()` creates a child process that runs independently:
- Parent and child execute concurrently
- Each has its own execution path
- They don't wait for each other (unless explicitly synchronized)

Raymond can achieve similar behavior:

```
Workflow A (main loop)              Workflow B (spawned)
┌─────────────────────┐            ┌─────────────────────┐
│ Check for issues    │            │                     │
│ Found issue 195     │──spawn──→  │ Work on issue 195   │
│ Continue checking   │            │ Plan, implement...  │
│ Found issue 196     │──spawn──→  │ Commit, close       │
│ Continue checking   │            └─────────────────────┘
│ ...                 │            ┌─────────────────────┐
└─────────────────────┘            │ Work on issue 196   │
                                   │ ...                 │
                                   └─────────────────────┘
```

### Contrast with Pattern 2 (Fork with Return)

| Aspect | Pattern 2 (Fork) | Independent Spawn |
|--------|------------------|-------------------|
| Parent waits? | Yes, for result | No, continues immediately |
| Result returns? | Yes, via resume | No, independent completion |
| Lifecycle | Tied to parent | Fully independent |
| Use case | Subtasks | Parallel workstreams |

### Implementation

Spawning creates a new state file and (optionally) starts a new orchestrator:

```python
# Parent workflow spawns a child
child_state = {
    "workflow_id": f"issue-{issue_num}-{uuid}",
    "current_state": "START-ISSUE.md",
    "session_id": None,  # Fresh start
    "metadata": {"issue": issue_num}
}
write_state_file(f".raymond/workflows/{child_state['workflow_id']}.json", child_state)

# Parent continues immediately without waiting
```

The spawned workflow:
- Has its own state file
- Runs independently (same or different orchestrator process)
- Completes on its own timeline
- Can spawn further workflows if needed

### Use Cases

**Timed polling loop:**
```
1. [Loop] Check issue tracker every 5 minutes
2. [Spawn] For each new issue, spawn independent workflow
3. [Continue] Loop continues checking, doesn't wait
```

**Parallel batch processing:**
```
1. [Start] Given list of 10 files to refactor
2. [Spawn] Spawn independent workflow for each file
3. [Monitor] Optional: Track completion via state files
```

**Event-driven workflows:**
```
1. [Watch] Monitor for webhook events
2. [Spawn] On PR created, spawn review workflow
3. [Spawn] On issue labeled "urgent", spawn priority workflow
```

### Coordination (Optional)

Spawned workflows are independent by default, but can coordinate if needed:
- **Shared state**: Write to a common JSON file or database
- **File locks**: Prevent conflicts on shared resources
- **Completion markers**: Write a `.done` file when finished
- **Aggregation workflow**: A separate workflow that waits for others

## Summary

| Aspect | Ralph | Raymond |
|--------|-------|---------|
| Context | Always fresh | Selective (fork/resume) |
| Workflow | Single repeated prompt | Multi-state machine |
| Branching | None | Declared in prompts |
| State carry | None | Via resume to parent |
| Configuration | One prompt file | Multiple markdown files |
| Invocation patterns | One (fresh) | Three (pure/fork/resume) |
| Crash recovery | Restart from scratch | Resume via state file |
| Concurrency | Single loop | Multiple independent workflows |
