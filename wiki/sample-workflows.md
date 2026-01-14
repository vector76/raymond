# Sample Workflows

These sample workflows test Raymond's orchestration mechanisms without modifying
code or making dangerous changes. They operate on text files in a `sandbox/`
directory.

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

When done, write the complete mini-story to sandbox/output.txt and respond with
no transition tag (this ends the workflow).
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

## Test 3: Fork with Return (Pattern 2)

**Purpose:** Test isolated subtask that returns a result to parent context.

**Workflow:** Parent asks child to research something, child returns summary.

**Files:**

`workflows/test-fork/MAIN.md`:
```markdown
You are managing a task. You need to find out information about a topic.

Read sandbox/input.txt to see what topic to research.

Then signal that you want to delegate the research:
<call return="SUMMARIZE.md">RESEARCH.md</call>
```

Note: The `return="SUMMARIZE.md"` attribute tells the orchestrator which state
to resume at when the called RESEARCH.md workflow completes.

`workflows/test-fork/RESEARCH.md`:
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

End with no transition tag.
```

`workflows/test-fork/SUMMARIZE.md`:
```markdown
You received research results from your assistant. Review what they found.

Write a one-paragraph summary of the research to sandbox/output.txt.

Mention at least two of the facts from the research in your summary.

End with no transition tag.
```

**Test procedure:**
1. Write "purple elephants" to `sandbox/input.txt`
2. Start workflow at MAIN.md
3. Verify it forks to RESEARCH.md
4. Verify RESEARCH.md returns facts about purple elephants
5. Verify parent resumes at SUMMARIZE.md with the research result
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

For EACH item in the list, spawn an independent worker by outputting a fork
tag with the item as an attribute. For example, if the items are "mountains",
"rivers", "clouds", output:

<fork item="mountains">WORKER.md</fork>
<fork item="rivers">WORKER.md</fork>
<fork item="clouds">WORKER.md</fork>

After listing all fork tags, write "Dispatched N workers" to
sandbox/dispatch-log.txt (where N is the count) and end with no further text.
```

Note: Each `item="..."` attribute becomes metadata in the spawned workflow's
state file, accessible to WORKER.md.

`workflows/test-spawn/WORKER.md`:
```markdown
You are a worker processing one item.

Check this workflow's metadata to find your assigned item. The orchestrator
will have set an "item" field based on the spawn transition.

"Process" this item by writing a haiku about it.

Write your haiku to sandbox/worker-{item}.txt (replace {item} with your
assigned item name, no spaces).

End with no transition tag.
```

Note: In practice, the orchestrator would inject the item into the prompt or
make it available through a convention (e.g., prepending "Your item: X" to the
prompt).

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

## Running the Tests

Each test should be runnable independently:

```bash
# Test pure function
python -m raymond test-pure

# Test goto/resume
python -m raymond test-goto

# Test fork
python -m raymond test-fork

# Test spawn
python -m raymond test-spawn

# Test evaluator
python -m raymond test-eval --max-iterations=3
```

## Cleanup

After tests, the sandbox directory can be deleted:
```bash
rm -rf sandbox/
```
