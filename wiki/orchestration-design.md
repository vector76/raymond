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

Prompts instruct the AI how to signal transitions:

```markdown
Review the code for issues. If you find problems, fix them. If everything
looks good, end your response with <transition>COMMIT.md</transition>
```

The Python orchestrator:
1. Parses `<transition>FILE.md</transition>` from output
2. Launches the next Claude Code instance with that file as the prompt
3. Remains agnostic to workflow logic - it just follows the declared transitions

This keeps workflow definitions in markdown, not Python code.

### Branching and Looping

States can loop or branch based on output. For example, a review state might
iterate up to five times, with the prompt instructing:

```markdown
If no issues are found, respond with <transition>COMMIT.md</transition>
Otherwise, fix the issues and respond with <transition>REVIEW.md</transition>
```

A lightweight evaluator (pattern match or small model) can also inspect output
to determine branches, enabling conditions like "max 5 iterations" at the
Python level.

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
1. Python captures the result/summary from the child
2. Resumes the parent context with `--resume`
3. Passes the child's result as the next prompt

The parent context never sees the messy iterations - just like a caller never
sees a function's internal variables.

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
   `<transition>REVIEW.md</transition>`)
3. Model outputs a final response (the session ends)
4. Orchestrator intercepts the transition tag
5. Orchestrator launches a new Claude Code session with the referenced prompt
6. Result becomes the prompt for the **next session**, not a tool response

The key difference: instead of injecting results into the same context, the
transition **ends the current session** and the result flows to a new (or
resumed) session. This gives the orchestrator control over context boundaries.

In effect, the model is "calling a tool" where the tool is: "start another
Claude Code session with this prompt and return what it produces."

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

### Pattern 1: Pure Function (No Context)

**Invocation:** Launch Claude Code with only a prompt, no session history.

**Characteristics:**
- Completely stateless - no prior context
- Output depends only on the prompt (and model behavior)
- Fast and cheap (minimal tokens)
- Deterministic in structure (though not in exact output)

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

**Programming analogy:** A `goto` statement within a function. Execution jumps
to a new label, but you're still in the same scope with access to all the same
variables. Nothing is discarded.

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
1. [Pattern 1] Evaluator decides which issue to work on
       ↓
2. [Pattern 3] Main context: "Work on issue 195"
       ↓
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
```

The main context stays clean (steps 2, 4, 6, 8) while messy work happens in
isolated forks (steps 3, 5) and quick decisions use stateless calls (steps 1,
7).

## Summary

| Aspect | Ralph | Raymond |
|--------|-------|---------|
| Context | Always fresh | Selective (fork/resume) |
| Workflow | Single repeated prompt | Multi-state machine |
| Branching | None | Declared in prompts |
| State carry | None | Via resume to parent |
| Configuration | One prompt file | Multiple markdown files |
| Invocation patterns | One (fresh) | Three (pure/fork/resume) |
