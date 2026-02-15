# Shell Script States

This document describes the problem of LLM overhead for deterministic operations
and proposes shell scripts (`.sh` on Unix, `.bat` on Windows) as an alternative
state implementation alongside markdown prompt files.

## Problem Statement

Raymond workflows currently use markdown files as states. Each state invokes
Claude Code (an LLM) to interpret the prompt, perform work, and emit a
transition tag. This works well when reasoning is needed, but introduces
unnecessary cost and complexity for deterministic operations.

### Problem 1: Token Cost for Deterministic Work

Consider a polling loop that checks GitHub every 60 seconds:

```
POLL.md:
  "Check the GitHub API for new issues assigned to me.
   If found, respond with <goto>PROCESS.md</goto>
   If none, run `sleep 60` then respond with <reset>POLL.md</reset>"
```

The check is purely deterministic: call an API, parse JSON, branch on result.
No LLM reasoning is required, yet we're paying for:

- Input tokens (the prompt)
- Output tokens (the response)
- Model inference time
- Round-trip latency

This is "LLM as a shell proxy" — the model is just executing a script we could
have written directly.

### Problem 2: Timeout Fragility

For long-running operations, LLM tool calls can timeout. If we want a 5-minute
delay between polls:

```
POLL.md:
  "...run `sleep 300` then respond with <reset>POLL.md</reset>"
```

Claude Code has session timeouts. A 5-minute sleep ties up an LLM tool call,
and the response (from client back to LLM) may fail if the session times out.
The workflow breaks even though nothing actually went wrong.

### Problem 3: Inefficient Polling Granularity

With LLM-based polling, each check has a cost floor (minimum tokens, latency).
This makes frequent polling impractical. Checking every second for an hour
would cost thousands of dollars in tokens for what should be a trivial
operation.

## Beyond Polling: General Applicability

These problems aren't specific to polling. Any time a state would be "LLM as a
shell proxy" — executing a deterministic sequence without meaningful reasoning
— a shell script is more efficient.

**Examples:**

| Use Case | Why Shell Scripts Help |
|----------|----------------------|
| **Build/deploy pipelines** | `npm install && npm build && npm deploy` needs no reasoning, may take 10+ minutes |
| **Data processing** | Running `ffmpeg`, `imagemagick`, or ETL scripts — deterministic, potentially long |
| **Environment setup** | Creating directories, setting permissions, installing dependencies |
| **Git operations** | Clone, checkout, pull, apply patches — predetermined steps |
| **Test execution** | Run the test suite; only invoke LLM if tests fail and reasoning is needed |
| **Log/artifact collection** | Gather files, compress, upload — completely deterministic |
| **Health checks** | Verify services are running before starting work |
| **Cleanup/teardown** | Delete temp files, stop services, release resources |

**The pattern:** Whenever a markdown state's prompt essentially says "run these
commands in order and tell me if it worked," that's a candidate for a shell
script state.

## Proposed Solution: Shell Scripts as States

Allow states to be implemented as shell scripts (`.sh` or `.bat`) instead of
markdown files (`.md`). The orchestrator detects the file type and executes
accordingly:

- **Markdown states** (`.md`): Sent to Claude Code for LLM interpretation
- **Script states** (`.sh`/`.bat`): Executed directly by the orchestrator

### Same Transition Protocol

Shell scripts emit the same transition tags as LLM responses. The script's
stdout is parsed for transition directives:

```bash
#!/bin/bash
# POLL.sh - Check GitHub for new issues

response=$(curl -s -H "Authorization: token $GITHUB_TOKEN" \
  "https://api.github.com/issues?assignee=me&state=open")

count=$(echo "$response" | jq 'length')

if [ "$count" -gt 0 ]; then
    echo "<goto>PROCESS.md</goto>"
else
    sleep 60
    echo "<reset>POLL.sh</reset>"
fi
```

The orchestrator parses `<goto>`, `<reset>`, `<result>`, etc. from the script's
output exactly as it would from LLM output.

### Design Decision: File Extension Determines Execution Mode

**Recommendation:** The file extension (`.md` vs `.sh`/`.bat`) determines how
the state is executed, not the transition tag type.

**Rationale:**

1. **Same transition semantics**: All transition types (`goto`, `reset`, `call`,
   `fork`, `function`, `result`) work identically for both markdown and script
   states. The transition describes *what* happens next; the file type describes
   *how* the current state executes.

2. **Swappable implementations**: You can convert a markdown state to a script
   (or vice versa) without changing any transitions that reference it:
   
   ```
   # These both work, regardless of whether POLL is .md or .sh
   <goto>POLL</goto>  # Orchestrator resolves to POLL.md or POLL.sh
   ```

3. **Clean mental model**: State names are abstract identifiers. Whether `POLL`
   is implemented via LLM reasoning or direct execution is an implementation
   detail, not a workflow design concern.

4. **Protocol uniformity**: The workflow protocol doesn't change. States emit
   transitions; the orchestrator interprets them. Only the execution mechanism
   differs.

**Alternative considered:** A distinct transition tag like `<shell>POLL</shell>`.
This was rejected because:

- It conflates "how to execute" with "how to transition"
- It would require workflow authors to know implementation details of target states
- Changing a state from markdown to script would require updating all callers

### State Resolution

State resolution is **platform-specific**. When resolving an abstract state name
(e.g., `POLL` from `<goto>POLL</goto>`), the orchestrator checks:

**On Unix (Linux/macOS):**
1. `POLL.md` — Markdown prompt (LLM execution)
2. `POLL.sh` — Shell script (direct execution)

**On Windows:**
1. `POLL.md` — Markdown prompt (LLM execution)
2. `POLL.bat` — Batch file (direct execution)

There is no cross-platform fallback. If only `.bat` exists on Unix, that's an
error (and vice versa for `.sh` on Windows).

**Explicit extensions bypass resolution:**

When a transition specifies an extension (e.g., `<goto>POLL.sh</goto>`), no
resolution occurs — that exact file must exist. Specifying a platform-incompatible
extension is an error:
- `<goto>POLL.sh</goto>` on Windows → Error
- `<goto>POLL.bat</goto>` on Unix → Error

**Cross-platform workflows:**

Workflows can provide both `.sh` and `.bat` for the same state name. Each
platform uses its native script type:

| Files Present | Unix Resolves To | Windows Resolves To |
|---------------|------------------|---------------------|
| `POLL.md` | `POLL.md` | `POLL.md` |
| `POLL.sh` | `POLL.sh` | Error |
| `POLL.bat` | Error | `POLL.bat` |
| `POLL.sh`, `POLL.bat` | `POLL.sh` | `POLL.bat` |
| `POLL.md`, `POLL.sh` | Error (ambiguous) | `POLL.md` |
| `POLL.md`, `POLL.bat` | `POLL.md` | Error (ambiguous) |

**Execution:**

- On Unix: `.sh` files are executed with `/bin/bash`
- On Windows: `.bat` files are executed with `cmd.exe`

### Context and Variables

Shell scripts receive workflow context via environment variables. These
variables are injected by the orchestrator before script execution.

#### Core Environment Variables

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `RAYMOND_WORKFLOW_ID` | Unique identifier for the workflow run | `wf-2024-01-15-abc123` |
| `RAYMOND_AGENT_ID` | Identifier for the current agent | `main`, `main_worker1` |
| `RAYMOND_STATE_DIR` | Absolute path to the workflow states directory | `/home/user/workflows/test_cases` |
| `RAYMOND_STATE_FILE` | Absolute path to the current state file | `/home/user/workflows/test_cases/POLL.sh` |

**Example usage (Unix):**
```bash
#!/bin/bash
echo "Workflow: $RAYMOND_WORKFLOW_ID"
echo "Agent: $RAYMOND_AGENT_ID"
echo "State dir: $RAYMOND_STATE_DIR"
echo "Current state: $RAYMOND_STATE_FILE"
```

**Example usage (Windows):**
```batch
@echo off
echo Workflow: %RAYMOND_WORKFLOW_ID%
echo Agent: %RAYMOND_AGENT_ID%
echo State dir: %RAYMOND_STATE_DIR%
echo Current state: %RAYMOND_STATE_FILE%
```

#### Result Variable (Call Returns)

| Variable | Description | When Set |
|----------|-------------|----------|
| `RAYMOND_RESULT` | Payload from the `<result>` tag of a called subroutine | Only when returning from `<call>` |

When a script state is entered via a `<call>` return (i.e., the caller used
`<call return="THIS_STATE.sh">SUBROUTINE.md</call>`), the subroutine's result
payload is available in `RAYMOND_RESULT`.

**Example:**
```bash
#!/bin/bash
# RESUME_AFTER_CALL.sh - Entered after a <call> returns

if [ -n "$RAYMOND_RESULT" ]; then
    echo "Subroutine returned: $RAYMOND_RESULT"
else
    echo "No result (not returning from a call)"
fi

echo "<goto>NEXT.md</goto>"
```

#### Fork Attributes

When a script is spawned via `<fork>`, all attributes from the fork tag become
environment variables. This allows parent states to pass data to worker scripts.

**Fork tag example:**
```
<fork next="CONTINUE.md" item="issue-123" priority="high">WORKER.sh</fork>
```

**Available in WORKER.sh:**
```bash
#!/bin/bash
echo "Processing item: $item"        # "issue-123"
echo "Priority level: $priority"     # "high"

# Do work based on the item...
echo "<result>Processed $item</result>"
```

**Windows equivalent:**
```batch
@echo off
echo Processing item: %item%
echo Priority level: %priority%

REM Do work based on the item...
echo ^<result^>Processed %item%^</result^>
```

**Note:** The `next` and `cd` attributes are used by the orchestrator and are
NOT passed to the worker script as environment variables. Only user-defined
attributes (like `item`, `priority`, etc.) become environment variables.

#### Variable Naming

Fork attribute names become environment variable names directly:
- `item="value"` → `$item` (Unix) or `%item%` (Windows)
- `task_id="123"` → `$task_id` or `%task_id%`
- `myVar="test"` → `$myVar` or `%myVar%`

Avoid using attribute names that conflict with existing environment variables
or shell builtins (e.g., `PATH`, `HOME`, `COMSPEC`).

#### Persisting Data Between Script Runs

Scripts that use `<reset>` to loop back to themselves start with fresh context
each time. To persist data across iterations, write to files:

```bash
#!/bin/bash
# POLL.sh - Polling with persistent counter

counter_file="${RAYMOND_STATE_DIR}/poll_counter.txt"

# Read or initialize counter
if [ -f "$counter_file" ]; then
    count=$(cat "$counter_file")
else
    count=0
fi

# Increment and save
count=$((count + 1))
echo $count > "$counter_file"

echo "Poll iteration: $count"

if [ $count -lt 5 ]; then
    echo "<reset>POLL.sh</reset>"
else
    rm -f "$counter_file"  # Cleanup
    echo "<result>Polling complete after $count iterations</result>"
fi
```

### Return Values and Results

Scripts return results via `<result>` tags, just like LLM states:

```bash
#!/bin/bash
# BUILD.sh - Build the project

npm install
npm run build

if [ $? -eq 0 ]; then
    echo "<result>Build succeeded</result>"
else
    echo "<result>Build failed with exit code $?</result>"
fi
```

For `<call>` transitions, the result is captured and made available to the
return state via `{{result}}` template variable (for markdown) or
`RAYMOND_RESULT` environment variable (for scripts).

### Error Handling

Script execution errors are handled as follows:

| Condition | Behavior |
|-----------|----------|
| Script exits with code 0, valid transition tag | Normal transition |
| Script exits with code 0, no transition tag | Error: missing transition |
| Script exits with non-zero code | Error: script failed |
| Script outputs multiple transition tags | Error: ambiguous transition |
| Script times out | Error: timeout (configurable per-state) |

Errors in script states are fatal by default (workflow terminates). This
differs from LLM states, which can re-prompt on parse failures. Scripts are
expected to be deterministic and correct; if they fail, it's a bug.

### No Transition Constraints (No Frontmatter)

Shell scripts have no equivalent to YAML frontmatter for constraining allowed
transitions. This is analogous to a markdown file without frontmatter: **all
transition tags are permitted**.

The orchestrator still validates the emitted tag:

- The tag must be syntactically valid (`<goto>`, `<reset>`, `<result>`, etc.)
- The tag's target must resolve to an existing state file
- Exactly one transition tag must be present

However, because there is no policy defining allowed transitions, **no
"reminding" is possible**. When a markdown state with `allowed_transitions`
frontmatter emits an invalid or missing tag, the orchestrator can re-prompt
with a reminder of valid options. For script states, there is no LLM to
re-prompt — the script has already terminated.

Therefore, validation failures in script states are always fatal errors that
terminate the workflow. Script authors must ensure their scripts emit exactly
one valid transition tag on all code paths.

### Example: Polling Workflow

**Before (LLM-only, expensive and fragile):**

```
POLL.md:
  Check GitHub API for issues. If found, <goto>PROCESS.md</goto>.
  Otherwise, sleep 60 and <reset>POLL.md</reset>.

PROCESS.md:
  Analyze the issue and implement a fix...
```

**After (hybrid, efficient):**

```
POLL.sh:
  #!/bin/bash
  response=$(curl -s "$GITHUB_API/issues")
  if [ $(echo "$response" | jq 'length') -gt 0 ]; then
      echo "<goto>PROCESS.md</goto>"
  else
      sleep 60
      echo "<reset>POLL.sh</reset>"
  fi

PROCESS.md:
  Analyze the issue and implement a fix...
  (LLM reasoning needed here)
```

The polling runs outside any LLM session. It can poll every second, sleep for
hours, retry on network errors — all without spending tokens or risking
timeouts. The LLM is only invoked when there's actual work requiring reasoning.

### Example: Build Pipeline

```
BUILD.sh:
  #!/bin/bash
  set -e
  npm install
  npm run lint
  npm run test
  npm run build
  echo "<result>Build completed successfully</result>"
```

This deterministic build sequence runs directly. If it fails, the workflow
terminates with an error. If it succeeds, control returns to the caller with
the result.

## Implementation Notes

### Orchestrator Changes

The orchestrator's state resolution logic needs to:

1. Accept state names without extensions in transition targets
2. Search for `.md`, `.sh`, `.bat` files in resolution order
3. Dispatch to appropriate executor based on file type
4. Parse transition tags from script stdout

### Working Directory

By default, scripts execute in the **orchestrator's working directory** (where
`raymond` was launched), not in the scope directory where state files reside.
This ensures path consistency with Claude Code:

- If Claude Code references `foo/bar/file.txt`, it's relative to the orchestrator
  directory
- Shell scripts see the same path `foo/bar/file.txt`
- The scope directory is only for resolving state file names, not for execution

**Per-agent working directory:** Agents can have their own working directory,
set via the `cd` attribute on `<fork>` or `<reset>` transitions. When set,
both Claude Code and script subprocesses for that agent execute in the
specified directory instead of the orchestrator's directory. See
`wiki/workflow-protocol.md` for details.

### Async Execution

Script execution must be non-blocking. The orchestrator uses async subprocess
APIs (`asyncio.create_subprocess_exec()`) to run scripts without blocking the
event loop. This allows multiple agents to make progress concurrently, even if
one agent is running a long script.

### Cost Tracking

Script states contribute **$0.00** to the workflow's cost tracking. Only markdown
states (which invoke the LLM) consume the token budget. This means:

- Polling loops with script states have zero token cost regardless of iteration count
- Long-running build/deploy scripts don't affect the cost budget
- Debug mode logs show `"cost": "$0.00"` for script state transitions

This is the primary benefit of script states for deterministic operations — they
eliminate the token cost floor that makes frequent polling or long operations
impractical with LLM-based states.

### LLM Context Gap

When a workflow transitions through script states, those steps are **invisible**
to the LLM. If a workflow goes markdown → script → markdown, the second markdown
invocation's Claude Code session has no visibility into what the script did.

This is intentional:

- Scripts handle deterministic work that doesn't benefit from LLM context
- The LLM resumes with its previous session state, unaware of intervening scripts
- If the script produces output the LLM needs, it should write to a file or
  include it in the transition attributes

### Security Considerations

Shell scripts execute arbitrary code. Workflows should only be run from trusted
sources. The same security model applies as running any shell script on your
system.

### Debugging

Script states should support the same debug mode as markdown states:

- Capture stdout/stderr to debug directory
- Log execution time and exit codes
- Include environment variables in debug output

### Future Extensions

- **Timeout configuration**: Per-state timeout via frontmatter or naming convention
- **Retry logic**: Configurable retry for transient failures
- **Output streaming**: Stream script output to logs in real-time
- **Cross-platform scripts**: Support for Python/Node scripts that work everywhere
