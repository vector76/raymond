# Shell Script States (Design Rationale)

This document describes the problem of LLM overhead for deterministic operations
and the design decisions behind shell scripts (`.sh` on Unix, `.bat` or `.ps1` on Windows)
as an alternative state implementation alongside markdown prompt files.

For how to write shell script states, see
[authoring-guide.md](authoring-guide.md#shell-script-states).

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

Allow states to be implemented as shell scripts (`.sh` on Unix, `.bat` or `.ps1`
on Windows) instead of markdown files (`.md`). The orchestrator detects the file
type and executes accordingly:

- **Markdown states** (`.md`): Sent to Claude Code for LLM interpretation
- **Script states** (`.sh`/`.bat`/`.ps1`): Executed directly by the orchestrator

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

**Recommendation:** The file extension (`.md` vs `.sh`/`.bat`/`.ps1`) determines how
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
2. `POLL.ps1` — PowerShell script (direct execution)
3. `POLL.bat` — Batch file (direct execution)

If both `.ps1` and `.bat` exist for the same state name on Windows, that is an
ambiguity error. There is no cross-platform fallback. If only `.bat` or `.ps1`
exists on Unix, that's an error (and vice versa for `.sh` on Windows).

**Explicit extensions bypass resolution:**

When a transition specifies an extension (e.g., `<goto>POLL.sh</goto>`), no
resolution occurs — that exact file must exist. Specifying a platform-incompatible
extension is an error:
- `<goto>POLL.sh</goto>` on Windows → Error
- `<goto>POLL.bat</goto>` on Unix → Error
- `<goto>POLL.ps1</goto>` on Unix → Error

**Cross-platform workflows:**

Workflows can provide `.sh` for Unix and `.bat` or `.ps1` for Windows with the
same state name. Each platform uses its native script type:

| Files Present | Unix Resolves To | Windows Resolves To |
|---------------|------------------|---------------------|
| `POLL.md` | `POLL.md` | `POLL.md` |
| `POLL.sh` | `POLL.sh` | Error |
| `POLL.bat` | Error | `POLL.bat` |
| `POLL.ps1` | Error | `POLL.ps1` |
| `POLL.sh`, `POLL.bat` | `POLL.sh` | `POLL.bat` |
| `POLL.sh`, `POLL.ps1` | `POLL.sh` | `POLL.ps1` |
| `POLL.bat`, `POLL.ps1` | Error | Error (ambiguous) |
| `POLL.md`, `POLL.sh` | Error (ambiguous) | `POLL.md` |
| `POLL.md`, `POLL.bat` | `POLL.md` | Error (ambiguous) |
| `POLL.md`, `POLL.ps1` | `POLL.md` | Error (ambiguous) |
| `POLL.sh`, `POLL.bat`, `POLL.ps1` | `POLL.sh` | Error (ambiguous) |

**Execution:**

- On Unix: `.sh` files are executed with `/bin/bash`
- On Windows: `.bat` files are executed with `cmd.exe /c`; `.ps1` files are executed with PowerShell

### Error Handling

Script execution errors are fatal by default (workflow terminates). This
differs from LLM states, which can re-prompt on parse failures. Scripts are
expected to be deterministic and correct; if they fail, it's a bug.

| Condition | Behavior |
|-----------|----------|
| Script exits with code 0, valid transition tag | Normal transition |
| Script exits with code 0, no transition tag | Error: missing transition |
| Script exits with non-zero code | Error: script failed |
| Script outputs multiple transition tags | Error: ambiguous transition |
| Script times out | Error: timeout (configurable per-state) |

### No Frontmatter Policy

Shell scripts have no equivalent to YAML frontmatter. All transition tags are
permitted, but because there is no policy defining allowed transitions, no
"reminding" is possible — validation failures are always fatal.

## Implementation Notes

### Orchestrator Changes

The orchestrator's state resolution logic needs to:

1. Accept state names without extensions in transition targets
2. Search for `.md`, `.sh`, `.bat`, `.ps1` files in resolution order
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
`docs/workflow-protocol.md` for details.

### Async Execution

Script execution is synchronous within each agent step. The orchestrator runs
scripts via `os/exec.Cmd` with context cancellation support (see
`internal/platform/`). The sequential orchestration loop steps through agents
one at a time, so script execution blocks the current agent step but does not
block other agents.

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
