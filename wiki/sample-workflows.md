# Sample Workflows

These sample workflows test Raymond's orchestration mechanisms without modifying
code or making dangerous changes. All sample workflows are organized in a single
`workflows/test_cases/` directory.

## Platform note (production vs development)

- **Production**: Linux only (typically inside a Linux container with broad permissions, for containment).
- **Development**: Tests and local experimentation can be done on Windows, but some workflow examples use Linux commands.

## Directory Structure

All sample workflows are consolidated in one location:
```
workflows/test_cases/
  CLASSIFY.md           # Test 1: Pure function workflow
  START.md              # Test 2: Goto/resume workflow
  CONFLICT.md           # Test 2: Goto/resume workflow
  RESOLUTION.md         # Test 2: Goto/resume workflow
  MAIN.md               # Test 3: Call workflow
  RESEARCH.md           # Test 3: Call workflow
  SUMMARIZE.md          # Test 3: Call workflow
  DISPATCH.md           # Test 4: Fork workflow
  DISPATCH-ANOTHER.md   # Test 4: Fork workflow
  WORKER.md             # Test 4: Fork workflow
  DONE.md               # Test 4: Fork workflow
  IMPROVE.md            # Test 5: Evaluator override workflow
  PHASE1.md             # Test 6: Reset workflow
  PHASE2.md             # Test 6: Reset workflow
  test_files/           # Test input files (tracked in git)
    input1.txt          # Test 1: Classification input
    research-input.txt  # Test 3: Research topic input
    dispatch-items.txt  # Test 4: Items to dispatch
    ...
  test_outputs/         # Test output files (ignored by git)
    story-output.txt    # Test 2: Story output
    research-summary.txt # Test 3: Research summary output
    dispatch-log.txt    # Test 4: Dispatch log
    worker-*.txt        # Test 4: Worker outputs (one per item)
    improve-output.txt # Test 5: Improvement iterations
    reset-output.txt    # Test 6: Reset test output
    ...
```

**Important:** All workflow markdown files must have distinct names since they
coexist in the same directory. Test data files in `test_files/` (inputs) and
`test_outputs/` (outputs) should also use distinct names to avoid collisions
between workflows.

## Test 1: Pure Function (Pattern 1)

**Purpose:** Test stateless evaluation with no context.

**Workflow:** Classify the sentiment of text as POSITIVE, NEGATIVE, or NEUTRAL.

**Files:**

`workflows/test_cases/CLASSIFY.md`:
```markdown
Read the file workflows/test_cases/test_files/input1.txt and classify its sentiment.

Respond with a pair of "<result>" XML tags enclosing exactly one word: POSITIVE, NEGATIVE, or NEUTRAL, for example, you could respond with "<result>NEUTRAL</result>"
```

**Test procedure:**
1. Write "I love this beautiful sunny day!" to `workflows/test_cases/test_files/input1.txt`
2. Run workflow: `raymond workflows/test_cases/CLASSIFY.md`
3. Verify orchestrator captures output containing "POSITIVE" and prints: `Agent main terminated with result: POSITIVE`
4. Write "This is terrible and I hate it." to `workflows/test_cases/test_files/input1.txt`
5. Run workflow again
6. Verify orchestrator captures output containing "NEGATIVE" and prints: `Agent main terminated with result: NEGATIVE`

**Success criteria:** Each invocation is independent, orchestrator captures
single-word classification from Claude Code output. (Pure function results are
captured by the orchestrator, not written to files.)

---

## Test 2: Resume/Goto (Pattern 3)

**Purpose:** Test session continuation across state transitions.

**Workflow:** A three-step story builder that maintains context.

**Files:**

`workflows/test_cases/START.md`:
```markdown
We're going to write a short story together. I'll guide you through three
phases.

First, create a character. Give them a name, occupation, and one interesting
trait. Write 2-3 sentences introducing this character.

When done, respond with <goto>CONFLICT.md</goto>
```

`workflows/test_cases/CONFLICT.md`:
```markdown
Good! Now introduce a conflict or challenge for the character you just created.
The conflict should relate to their occupation or trait. Write 2-3 sentences.

Remember the character from the previous step - refer to them by name.

When done, respond with <goto>RESOLUTION.md</goto>
```

`workflows/test_cases/RESOLUTION.md`:
```markdown
Now write a brief resolution to the conflict. The character should use their
interesting trait to solve the problem. Write 2-3 sentences.

Reference details from both previous steps to demonstrate continuity.

When done, write the complete mini-story to workflows/test_cases/test_outputs/story-output.txt and respond with:
<result>Story complete</result>
```

**Test procedure:**
1. Start workflow: `raymond workflows/test_cases/START.md`
2. Verify it transitions to CONFLICT.md
3. Verify CONFLICT.md references the character by name (context preserved)
4. Verify it transitions to RESOLUTION.md
5. Verify RESOLUTION.md references both character and conflict
6. Verify workflow terminates with result message and `workflows/test_cases/test_outputs/story-output.txt` contains the story

**Success criteria:** Context flows through all three states, character details
persist.

---

## Test 3: Call with Return (Pattern 2)

**Purpose:** Test isolated subtask that returns a result to parent context.

**Workflow:** Parent calls child to research something, child returns summary.

**Files:**

`workflows/test_cases/MAIN.md`:
```markdown
You are managing a task. You need to find out information about a topic.

Read workflows/test_cases/test_files/research-input.txt to see what topic to research.

Then signal that you want to delegate the research:
<call return="SUMMARIZE.md">RESEARCH.md</call>
```

Note: The `return="SUMMARIZE.md"` attribute tells the orchestrator which state
to resume at when the called RESEARCH.md workflow completes.

`workflows/test_cases/RESEARCH.md`:
```markdown
You are a research assistant. Read workflows/test_cases/test_files/research-input.txt for the topic.

Do some "research" by making up 3 interesting fictional facts about this topic.
Be creative and specific.

When done, provide your findings in a result tag:
<result>
Three facts about [topic]:
1. [fact 1]
2. [fact 2]
3. [fact 3]
</result>

Do not include any other protocol tags.
```

`workflows/test_cases/SUMMARIZE.md`:
```markdown
You received research results from your assistant:

{{result}}

Write a one-paragraph summary of the research to workflows/test_cases/test_outputs/research-summary.txt.

Mention at least two of the facts from the research in your summary.

Then respond with:
<result>Summary written to workflows/test_cases/test_outputs/research-summary.txt</result>
```

Note: The `{{result}}` placeholder is replaced with the content from the
child's `<result>` tag. This is how the parent receives the child's return value.

**Test procedure:**
1. Write "purple elephants" to `workflows/test_cases/test_files/research-input.txt`
2. Start workflow: `raymond workflows/test_cases/MAIN.md`
3. Verify it calls RESEARCH.md as a child workflow
4. Verify RESEARCH.md returns facts about purple elephants via `<result>` tag
5. Verify parent resumes at SUMMARIZE.md with `{{result}}` populated
6. Verify research-summary.txt contains a summary referencing the fictional facts

**Success criteria:** Child context is isolated, only result returns to parent.

---

## Test 4: Fork (Independent Agents)

**Purpose:** Test spawning independent agents that run in parallel.

**Workflow:** A dispatcher that forks separate worker agents for multiple items.

**Files:**

`workflows/test_cases/DISPATCH.md`:
```markdown
You are a task dispatcher. Read workflows/test_cases/test_files/dispatch-items.txt which contains a list of
items (one per line).

If the list is empty, write "Dispatched 0 workers" to workflows/test_cases/test_outputs/dispatch-log.txt
and respond with:
<goto>DONE.md</goto>

Otherwise, respond with:
<goto>DISPATCH-ANOTHER.md</goto>
```

`workflows/test_cases/DISPATCH-ANOTHER.md`:
```markdown
You are dispatching workers one at a time.

From the current conversation context, keep track of which items from
workflows/test_cases/test_files/dispatch-items.txt have already had workers spawned.

If all items already have workers, write "Dispatched N workers" to
workflows/test_cases/test_outputs/dispatch-log.txt (where N is the total count) and respond with:
<goto>DONE.md</goto>

Otherwise, choose ONE remaining item that does not yet have a worker and spawn
exactly one worker by responding with:
<fork next="DISPATCH-ANOTHER.md" item="[the item]">WORKER.md</fork>
```

Note: Each `item="..."` attribute becomes metadata for the spawned agent,
accessible to WORKER.md via template substitution.

`workflows/test_cases/WORKER.md`:
```markdown
Your assigned item is: {{item}}

Write a haiku about this item.

Write your haiku to workflows/test_cases/test_outputs/worker-{{item}}.txt.

Then respond with:
<result>Haiku written for {{item}}</result>
```

`workflows/test_cases/DONE.md`:
```markdown
All workers have been dispatched.

Respond with:
<result>Dispatch complete</result>
```

Note: The orchestrator performs template substitution before sending the prompt
to Claude Code. The `{{item}}` placeholder is replaced with the value from the
`item="..."` attribute in the `<fork>` tag. This is simpler than trying to pass
metadata through Claude Code's session state.

**Test procedure:**
1. Write three items to `workflows/test_cases/test_files/dispatch-items.txt`:
   ```
   mountains
   rivers
   clouds
   ```
2. Start workflow: `raymond workflows/test_cases/DISPATCH.md`
3. Verify three independent WORKER agents are spawned (all in same state file)
4. Verify each worker creates its own output file with a haiku
5. Verify `workflows/test_cases/test_outputs/dispatch-log.txt` says "Dispatched 3 workers"
6. Verify all agents complete independently

**Success criteria:** Multiple independent agents run concurrently, each
produces its own output.

---

## Test 5: Evaluator Override (Cost Budget)

**Purpose:** Test orchestrator overriding AI's transition based on cost budget
limit.

**Workflow:** A loop that could run forever, but orchestrator limits total cost.

**Files:**

`workflows/test_cases/IMPROVE.md`:
```markdown
Read workflows/test_cases/test_outputs/improve-output.txt (create it if it doesn't exist with "Draft 1").

"Improve" the content by incrementing the draft number and adding one word.
Write the result back to workflows/test_cases/test_outputs/improve-output.txt.

Example: "Draft 1" becomes "Draft 2 banana"

Then request another iteration:
<goto>IMPROVE.md</goto>
```

**Test procedure:**
1. Delete `workflows/test_cases/test_outputs/improve-output.txt` if it exists
2. Start workflow with a small budget: `raymond workflows/test_cases/IMPROVE.md --budget 0.10`
   (Note: Cost budget limiting is a future feature - this test documents the intended behavior)
3. Verify workflow runs until total cost exceeds budget, then terminates despite AI requesting more iterations
4. Verify `workflows/test_cases/test_outputs/improve-output.txt` contains multiple drafts (number depends on cost per iteration)
5. Verify state file shows `total_cost_usd` and `budget_usd` fields, and workflow terminated with budget exceeded

**Success criteria:** Orchestrator tracks cumulative cost across all Claude Code invocations in state file, overrides
AI's transition when budget is exceeded, terminates workflow cleanly.

---

## Test 6: Reset (Fresh Context)

**Purpose:** Test intentional context discard between workflow phases.

**Workflow:** Two-phase process where phase 1 writes to a file, then resets
to phase 2 which reads from the file (proving context was discarded).

**Files:**

`workflows/test_cases/PHASE1.md`:
```markdown
You are in phase 1. Generate a random 4-digit number and remember it.

Write "Phase 1 generated: [your number]" to workflows/test_cases/test_outputs/reset-output.txt.

Also write something that ONLY exists in your context (do not write it to any
file): "The secret word is: elephant"

When done, signal a reset to phase 2:
<reset>PHASE2.md</reset>
```

`workflows/test_cases/PHASE2.md`:
```markdown
You are in phase 2, starting with fresh context.

First, read workflows/test_cases/test_outputs/reset-output.txt to see what phase 1 generated.

Now answer these questions by appending to workflows/test_cases/test_outputs/reset-output.txt:
1. What number did phase 1 generate? (read from file)
2. What was the secret word from phase 1? (you should NOT know this)

Be honest - if you don't know the secret word, say "I don't know".

Then respond with:
<result>Reset test complete</result>
```

**Test procedure:**
1. Start workflow: `raymond workflows/test_cases/PHASE1.md`
2. Verify it generates a number and writes to `workflows/test_cases/test_outputs/reset-output.txt`
3. Verify it resets to PHASE2.md (new session ID in state file)
4. Verify PHASE2.md can read the number from file
5. Verify PHASE2.md does NOT know the secret word (context was discarded)

**Success criteria:** Phase 2 can access file-persisted data but NOT context
from phase 1. This proves reset discarded the context while continuing the
workflow.

---

## Running the Tests

Each test should be runnable independently using the `start` command with the
workflow file path:

```bash
# Test 1: Pure function
raymond workflows/test_cases/CLASSIFY.md

# Test 2: Goto/resume
raymond workflows/test_cases/START.md

# Test 3: Call (child workflow with return)
raymond workflows/test_cases/MAIN.md

# Test 4: Fork (independent agents)
raymond workflows/test_cases/DISPATCH.md

# Test 5: Evaluator override (cost budget, future feature)
raymond workflows/test_cases/IMPROVE.md --budget 0.10

# Test 6: Reset (fresh context)
raymond workflows/test_cases/PHASE1.md
```

## File Naming

Since all workflow markdown files coexist in `workflows/test_cases/`, each file
must have a unique name. The examples above use descriptive names that indicate
their purpose (CLASSIFY.md, START.md, CONFLICT.md, etc.).

Test data files in `test_files/` (inputs) and `test_outputs/` (outputs) should
also use distinct names to avoid collisions between workflows. Examples:
- Input files: `input1.txt`, `input2.txt`, `research-input.txt`, `dispatch-items.txt`
- Output files: `story-output.txt`, `research-summary.txt`, `dispatch-log.txt`, `worker-*.txt`

## Cleanup

After tests, the test output directory can be cleaned up (input files in
`test_files/` are typically preserved for reuse):

```bash
# Linux / macOS
rm -rf workflows/test_cases/test_outputs/*

# Windows (PowerShell)
Remove-Item workflows/test_cases/test_outputs/* -Force
```

Note: The workflow markdown files in `workflows/test_cases/` should be preserved
as they are the workflow definitions themselves. Input files in `test_files/` are
tracked in git and can be reused across test runs. Output files in `test_outputs/`
are ignored by git and can be safely deleted.
