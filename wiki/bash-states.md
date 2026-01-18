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
    # Extract first issue for processing
    issue=$(echo "$response" | jq -r '.[0].number')
    echo "<goto issue=\"$issue\">PROCESS.md</goto>"
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

### State Resolution Order

When resolving a state name (e.g., `POLL` from `<goto>POLL</goto>`), the
orchestrator checks for files in this order:

1. `POLL.md` — Markdown prompt (LLM execution)
2. `POLL.sh` — Unix shell script (direct execution on Unix)
3. `POLL.bat` — Windows batch file (direct execution on Windows)

**Platform-specific behavior:**

- On Unix: `.sh` files are executed with `/bin/bash` (or `$SHELL`)
- On Windows: `.bat` files are executed with `cmd.exe`
- Cross-platform workflows can provide both `.sh` and `.bat` for the same state

**Ambiguity rule:** If multiple files exist for the same state name (e.g., both
`POLL.md` and `POLL.sh`), this is an error. Each state must have exactly one
implementation.

### Context and Variables

Shell scripts receive workflow context via environment variables:

| Variable | Description |
|----------|-------------|
| `RAYMOND_WORKFLOW_ID` | Current workflow identifier |
| `RAYMOND_AGENT_ID` | Current agent identifier |
| `RAYMOND_STATE_DIR` | Directory containing workflow states |
| `RAYMOND_STATE_FILE` | Path to the workflow's JSON state file |
| `RAYMOND_RESULT` | Result from previous `<call>` (if returning from subroutine) |

Template variables from `<fork>` attributes are also passed as environment
variables:

```bash
# If forked with <fork item="issue-123">WORKER.sh</fork>
echo "Processing: $item"  # "issue-123"
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
