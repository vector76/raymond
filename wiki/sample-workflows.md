# Sample Workflows

These sample workflows test Raymond's orchestration mechanisms without modifying
code or making dangerous changes. They operate on text files in a `sandbox/`
directory.

## Platform note (production vs development)

- **Production**: Linux only (typically inside a Linux container with broad permissions, for containment).
- **Development**: Tests and local experimentation can be done on Windows, but some workflow examples use Linux commands.

## Setup

Create a sandbox directory for test workflows:
```
sandbox/
  input.txt      # Test input file
  output.txt     # Test output file (created by workflows)
```

## Test 1: Pure Function (Pattern 1)

**Purpose:** Test stateless evaluation with no context.

**Workflow:** Classify the sentiment of text as POSITIVE, NEGATIVE, or NEUTRAL.

**Files:**

`workflows/test-pure/CLASSIFY.md`:
```markdown
Read the file sandbox/input.txt and classify its sentiment.

Respond with exactly one word: POSITIVE, NEGATIVE, or NEUTRAL.

Do not include any other text in your response.
```

**Test procedure:**
1. Write "I love this beautiful sunny day!" to `sandbox/input.txt`
2. Run workflow with CLASSIFY.md as pure function
3. Verify orchestrator captures output containing "POSITIVE"
4. Write "This is terrible and I hate it." to `sandbox/input.txt`
5. Run workflow again
6. Verify orchestrator captures output containing "NEGATIVE"

**Success criteria:** Each invocation is independent, orchestrator captures
single-word classification from Claude Code output. (Pure function results are
captured by the orchestrator, not written to files.)

---

## Test 2: Resume/Goto (Pattern 3)

**Purpose:** Test session continuation across state transitions.

**Workflow:** A three-step story builder that maintains context.

**Files:**

`workflows/test-goto/START.md`:
```markdown
We're going to write a short story together. I'll guide you through three
phases.

First, create a character. Give them a name, occupation, and one interesting
trait. Write 2-3 sentences introducing this character.

When done, respond with <goto>CONFLICT.md</goto>
```

`workflows/test-goto/CONFLICT.md`:
```markdown
Good! Now introduce a conflict or challenge for the character you just created.
The conflict should relate to their occupation or trait. Write 2-3 sentences.

Remember the character from the previous step - refer to them by name.

When done, respond with <goto>RESOLUTION.md</goto>
```

`workflows/test-goto/RESOLUTION.md`:
```markdown
Now write a brief resolution to the conflict. The character should use their
interesting trait to solve the problem. Write 2-3 sentences.

Reference details from both previous steps to demonstrate continuity.

When done, write the complete mini-story to sandbox/output.txt and respond with:
<result>Story complete</result>
```

**Test procedure:**
1. Start workflow at START.md with a fresh session
2. Verify it transitions to CONFLICT.md
3. Verify CONFLICT.md references the character by name (context preserved)
4. Verify it transitions to RESOLUTION.md
5. Verify RESOLUTION.md references both character and conflict
6. Verify workflow terminates and output.txt contains the story

**Success criteria:** Context flows through all three states, character details
persist.

---

## Test 3: Call with Return (Pattern 2)

**Purpose:** Test isolated subtask that returns a result to parent context.

**Workflow:** Parent calls child to research something, child returns summary.

**Files:**

`workflows/test-call/MAIN.md`:
```markdown
You are managing a task. You need to find out information about a topic.

Read sandbox/input.txt to see what topic to research.

Then signal that you want to delegate the research:
<call return="SUMMARIZE.md">RESEARCH.md</call>
```

Note: The `return="SUMMARIZE.md"` attribute tells the orchestrator which state
to resume at when the called RESEARCH.md workflow completes.

`workflows/test-call/RESEARCH.md`:
```markdown
You are a research assistant. Read sandbox/input.txt for the topic.

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

`workflows/test-call/SUMMARIZE.md`:
```markdown
You received research results from your assistant:

{{result}}

Write a one-paragraph summary of the research to sandbox/output.txt.

Mention at least two of the facts from the research in your summary.

Then respond with:
<result>Summary written to sandbox/output.txt</result>
```

Note: The `{{result}}` placeholder is replaced with the content from the
child's `<result>` tag. This is how the parent receives the child's return value.

**Test procedure:**
1. Write "purple elephants" to `sandbox/input.txt`
2. Start workflow at MAIN.md
3. Verify it calls RESEARCH.md as a child workflow
4. Verify RESEARCH.md returns facts about purple elephants via `<result>` tag
5. Verify parent resumes at SUMMARIZE.md with `{{result}}` populated
6. Verify output.txt contains a summary referencing the fictional facts

**Success criteria:** Child context is isolated, only result returns to parent.

---

## Test 4: Independent Spawn

**Purpose:** Test spawning independent workflows that run in parallel.

**Workflow:** A dispatcher that spawns separate workers for multiple items.

**Files:**

`workflows/test-spawn/DISPATCH.md`:
```markdown
You are a task dispatcher. Read sandbox/input.txt which contains a list of
items (one per line).

If the list is empty, write "Dispatched 0 workers" to sandbox/dispatch-log.txt
and respond with:
<goto>DONE.md</goto>

Otherwise, respond with:
<goto>DISPATCH-ANOTHER.md</goto>
```

`workflows/test-spawn/DISPATCH-ANOTHER.md`:
```markdown
You are dispatching workers one at a time.

From the current conversation context, keep track of which items from
sandbox/input.txt have already had workers spawned.

If all items already have workers, write "Dispatched N workers" to
sandbox/dispatch-log.txt (where N is the total count) and respond with:
<goto>DONE.md</goto>

Otherwise, choose ONE remaining item that does not yet have a worker and spawn
exactly one worker by responding with:
<fork next="DISPATCH-ANOTHER.md" item="[the item]">WORKER.md</fork>
```

Note: Each `item="..."` attribute becomes metadata in the spawned workflow's
state file, accessible to WORKER.md.

`workflows/test-spawn/WORKER.md`:
```markdown
Your assigned item is: {{item}}

Write a haiku about this item.

Write your haiku to sandbox/worker-{{item}}.txt.

Then respond with:
<result>Haiku written for {{item}}</result>
```

`workflows/test-spawn/DONE.md`:
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
1. Write three items to `sandbox/input.txt`:
   ```
   mountains
   rivers
   clouds
   ```
2. Start workflow at DISPATCH.md
3. Verify three independent WORKER workflows are spawned
4. Verify each worker creates its own output file with a haiku
5. Verify dispatch-log.txt says "Dispatched 3 workers"
6. Verify all workflows complete independently

**Success criteria:** Multiple independent workflows run concurrently, each
produces its own output.

---

## Test 5: Evaluator Override

**Purpose:** Test orchestrator overriding AI's transition based on iteration
limit.

**Workflow:** A loop that could run forever, but orchestrator limits iterations.

**Files:**

`workflows/test-eval/IMPROVE.md`:
```markdown
Read sandbox/output.txt (create it if it doesn't exist with "Draft 1").

"Improve" the content by incrementing the draft number and adding one word.
Write the result back to sandbox/output.txt.

Example: "Draft 1" becomes "Draft 2 banana"

Then request another iteration:
<goto>IMPROVE.md</goto>
```

**Test procedure:**
1. Delete sandbox/output.txt if it exists
2. Start workflow at IMPROVE.md with initial state file containing:
   ```json
   {
     "workflow_id": "test-eval-001",
     "current_state": "IMPROVE.md",
     "max_iterations": 3,
     "iteration_count": 0
   }
   ```
3. Verify workflow runs exactly 3 times despite AI requesting more iterations
4. Verify output.txt contains "Draft 3" with two added words
5. Verify state file shows `iteration_count: 3` and workflow terminated

**Success criteria:** Orchestrator tracks iterations in state file, overrides
AI's transition when limit is reached, terminates workflow cleanly.

---

## Test 6: Reset (Fresh Context)

**Purpose:** Test intentional context discard between workflow phases.

**Workflow:** Two-phase process where phase 1 writes to a file, then resets
to phase 2 which reads from the file (proving context was discarded).

**Files:**

`workflows/test-reset/PHASE1.md`:
```markdown
You are in phase 1. Generate a random 4-digit number and remember it.

Write "Phase 1 generated: [your number]" to sandbox/output.txt.

Also write something that ONLY exists in your context (do not write it to any
file): "The secret word is: elephant"

When done, signal a reset to phase 2:
<reset>PHASE2.md</reset>
```

`workflows/test-reset/PHASE2.md`:
```markdown
You are in phase 2, starting with fresh context.

First, read sandbox/output.txt to see what phase 1 generated.

Now answer these questions by appending to sandbox/output.txt:
1. What number did phase 1 generate? (read from file)
2. What was the secret word from phase 1? (you should NOT know this)

Be honest - if you don't know the secret word, say "I don't know".

Then respond with:
<result>Reset test complete</result>
```

**Test procedure:**
1. Start workflow at PHASE1.md
2. Verify it generates a number and writes to output.txt
3. Verify it resets to PHASE2.md (new session ID in state file)
4. Verify PHASE2.md can read the number from file
5. Verify PHASE2.md does NOT know the secret word (context was discarded)

**Success criteria:** Phase 2 can access file-persisted data but NOT context
from phase 1. This proves reset discarded the context while continuing the
workflow.

---

## Running the Tests

Each test should be runnable independently.

Note: The CLI shown below is illustrative and not implemented yet; it represents
the intended developer experience once a `raymond` module/CLI exists.

```bash
# Test pure function
python -m raymond test-pure

# Test goto/resume
python -m raymond test-goto

# Test call (child workflow with return)
python -m raymond test-call

# Test spawn (independent workflows)
python -m raymond test-spawn

# Test evaluator override
python -m raymond test-eval --max-iterations=3

# Test reset (fresh context)
python -m raymond test-reset
```

## Cleanup

After tests, the sandbox directory can be deleted:

```bash
# Linux / macOS
rm -rf sandbox/

# Windows (PowerShell)
Remove-Item -Recurse -Force sandbox
```
