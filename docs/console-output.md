# Console Output Design

This document describes the console output format for the Raymond orchestrator, designed to provide real-time visibility into workflow execution.

## Design Goals

1. **Real-time feedback** - Show progress during Claude Code invocations, not just after completion
2. **Compact prefixes** - Reduce screen space consumed by timestamps and logger names
3. **Hierarchical structure** - State transitions are visually prominent; intra-state messages are indented
4. **Scannable** - Easy to see workflow flow at a glance

## Current Output Format

The current format uses Python's logging with full timestamps and module names:

```
2026-01-24 21:06:17 [INFO] src.orchestrator: Starting orchestrator for workflow: workflow_2026-01-24_21-06-17-009077
2026-01-24 21:06:17 [INFO] src.orchestrator: Debug mode enabled: C:\Users\Vector\Desktop\cursor_projects\raymond\.raymond\debug\...
2026-01-24 21:06:17 [INFO] src.orchestrator: Invoking Claude Code for agent main
2026-01-24 21:06:27 [INFO] src.orchestrator: Cost for agent main invocation: $0.0353, Total cost: $0.0353
2026-01-24 21:06:27 [INFO] src.orchestrator: Agent main transition: START.md -> CONFLICT.md
```

**Problems:**
- 47-character prefix before useful content begins (`2026-01-24 21:06:17 [INFO] src.orchestrator: `)
- No visibility into what Claude is doing during invocation
- Flat structure makes it hard to see workflow shape

## Proposed Output Format

### Startup Messages

```
[21:06:17] Workflow: workflow_2026-01-24_21-06-17-009077
[21:06:17] Scope: workflows/test_cases/
[21:06:17] Debug: .raymond/debug/workflow_2026-01-24_21-06-17-009077_20260124_210617/
```

### State Execution (Single Agent)

```
[main] START.md
  ├─ I'll begin the story by introducing our main character...
  ├─ [Write] story.txt
  └─ Done ($0.0353, total: $0.0353)
  → CONFLICT.md

[main] CONFLICT.md
  ├─ Now I'll develop the central conflict...
  ├─ [Read] story.txt
  ├─ [Edit] story.txt
  └─ Done ($0.0134, total: $0.0487)
  → RESOLUTION.md

[main] RESOLUTION.md
  ├─ Time to bring the story to a satisfying conclusion...
  ├─ [Edit] story.txt
  └─ Done ($0.0944, total: $0.1430)
  ⇒ Result: "Story complete"
```

### Visual Elements

| Symbol | Meaning |
|--------|---------|
| `[agent_id]` | Agent identifier (bold or colored if terminal supports) |
| `├─` | Progress message within a state (text or tool invocation) |
| `└─` | Completion line showing cost (always last, printed after stream ends) |
| `!` | Error or warning (e.g., retry needed, budget exceeded) |
| `→` | State transition (goto, reset, function, call) - all use same visual |
| `⇒` | Agent termination (result) |
| `⑂` | Fork (spawning new agent) - format: `⑂ TARGET.md → new_agent_id` - ASCII fallback: `++ TARGET.md -> new_agent_id` (with `->` instead of `→`) |

### Multi-Agent Output

When multiple agents run concurrently, their output naturally interleaves. Each line is prefixed with the agent ID, so context is always clear:

```
[main] DISPATCH.md
  ├─ I'll spawn a worker for each item...
  ├─ [Read] items.txt
  └─ Done ($0.02, total: $0.02)
  ⑂ WORKER.md → main_worker1

[main] MONITOR.md
  ├─ Waiting for worker to complete...
[main_worker1] WORKER.md
  ├─ Processing the assigned item...
  ├─ [Read] item1.txt
[main] MONITOR.md
  ├─ Still waiting...
[main_worker1] WORKER.md
  ├─ [Write] result1.txt
  └─ Done ($0.03, total: $0.05)
  ⇒ Result: "Item 1 processed"
[main] MONITOR.md
  └─ Done ($0.01, total: $0.06)
  → DONE.md
```

### Script Execution

Script states show execution without LLM messages. The state header (`[agent_id] state_name`) is displayed via `state_started()` when script execution begins:

```
[main] CHECK.bat
  ├─ Executing script...
  └─ Done (exit 0, 125ms)
  → NEXT.md
```

Note: Scripts contribute $0.00 to cost tracking, so the "Done" line shows exit code and execution time instead of cost.

### Error States

Errors and warnings use `!` to stand out from normal progress:

**Parsing/transition errors:**
```
[main] BROKEN.md
  ├─ I'll analyze this data...
  ├─ [Read] data.txt
  └─ Done ($0.03, total: $0.03)
  ! No transition tag - retrying (1/3)
[main] BROKEN.md
  ├─ Let me try again with the proper format...
  └─ Done ($0.02, total: $0.05)
  → FIXED.md
```

Note: Error messages (timeouts, parsing failures, policy violations, etc.) are displayed via `error()` method and appear with the `!` prefix. When retries occur, the state header is shown again for the retry attempt.

**Tool errors (from tool execution failures):**
```
[main] WRITE.md
  ├─ I'll write the output file...
  ├─ [Write] output.txt
  ! [Write] error: File has not been read yet. Read it first before writing...
  ├─ [Read] output.txt
  ├─ [Write] output.txt
  └─ Done ($0.05, total: $0.05)
  → NEXT.md
```

Note: The implementation tracks the last tool invocation per agent when processing the stream, allowing the tool name to be included in error messages (see Implementation Decisions).

Budget exceeded (detected after invocation completes):
```
[main] EXPENSIVE.md
  ├─ Processing large dataset...
  └─ Done ($10.05, total: $10.05)
  ! BUDGET EXCEEDED ($10.05 > $10.00)
  ⇒ Result: "Workflow terminated: budget exceeded"
```

## Message Sources

### What Gets Displayed

Messages come from several sources:

1. **Claude Code assistant messages** - Extracted from streaming JSON (text content)
2. **Tool invocations** - When Claude calls tools (read file, write, etc.) - shown when tool_use appears
3. **Tool errors** - When tool execution fails (shown from user messages with `is_error: true`)
4. **State transitions** - When agent moves to new state
5. **Cost updates** - After each invocation completes
6. **Errors/retries** - When issues occur (parsing failures, policy violations, etc.)

Note: Successful tool results (the `"type": "user"` messages with `"is_error": false`) are not displayed to avoid console clutter. However, tool errors (`"is_error": true`) are shown to indicate failures.

### Extracting Messages from Claude Code Stream

The Claude Code stream-json format provides JSON objects with these top-level types:
- `"type": "system"` with `"subtype": "init"` - initialization (contains session_id, model, tools)
- `"type": "assistant"` - assistant messages (text and tool_use are in `message.content`)
- `"type": "user"` - tool results (contains `"is_error"` flag; errors should be displayed)
- `"type": "result"` - final result (contains `"result"` payload, `"total_cost_usd"`, `"duration_ms"`, `"permission_denials"`)

We extract displayable content from the stream. This pseudocode shows the extraction logic; the actual implementation calls `ConsoleReporter` methods:

```python
# Assistant messages (text and tool invocations)
if obj.get("type") == "assistant" and "message" in obj:
    for content in obj["message"].get("content", []):
        if content.get("type") == "text":
            # Display first line or ~80 chars of text as progress
            text = content.get("text", "")
            if text:
                first_line = text.split('\n')[0]
                display_text = first_line[:80] + ("..." if len(first_line) > 80 else "")
                # In implementation: console_reporter.progress_message(agent_id, display_text)
                print(f"  ├─ {display_text}")
        elif content.get("type") == "tool_use":
            tool_name = content.get("name", "unknown")
            tool_input = content.get("input", {})
            # Show relevant detail for common tools
            if tool_name in ("Read", "Write", "Edit") and "file_path" in tool_input:
                from pathlib import Path
                detail = Path(tool_input["file_path"]).name  # filename only (cross-platform)
                # In implementation: console_reporter.tool_invocation(agent_id, tool_name, detail)
                print(f"  ├─ [{tool_name}] {detail}")
            elif tool_name == "Bash" and "command" in tool_input:
                cmd = tool_input["command"][:40]
                # In implementation: console_reporter.tool_invocation(agent_id, tool_name, cmd)
                print(f"  ├─ [Bash] {cmd}")
            else:
                # In implementation: console_reporter.tool_invocation(agent_id, tool_name)
                print(f"  ├─ [{tool_name}]")

# Tool errors (from user messages)
elif obj.get("type") == "user":
    for content in obj.get("message", {}).get("content", []):
        if content.get("type") == "tool_result" and content.get("is_error"):
            error_msg = content.get("content", "Tool error")
            # Extract error message (may be wrapped in <tool_use_error> tags)
            if "<tool_use_error>" in error_msg:
                error_msg = error_msg.split("<tool_use_error>")[1].split("</tool_use_error>")[0]
            # Truncate long error messages
            if len(error_msg) > 60:
                error_msg = error_msg[:60] + "..."
            # In implementation: console_reporter.tool_error(agent_id, error_msg, tool_name)
            # tool_name is tracked from the last tool_invocation call
            # If tool_name is available, format as: f"! [{tool_name}] error: {error_msg}"
            # Otherwise: f"! Tool error: {error_msg}"
            print(f"  ! Tool error: {error_msg}")

# Final result (extract cost and result payload)
elif obj.get("type") == "result":
    cost = obj.get("total_cost_usd", 0.0)
    result_payload = obj.get("result", "")
    # result_payload may contain <result>...</result> tags - extract content
    # If no tags, use the payload as-is (could be plain text)
    if "<result>" in result_payload and "</result>" in result_payload:
        result_payload = result_payload.split("<result>")[1].split("</result>")[0]
    # Cost and result are displayed separately (see state_completed and agent_terminated)
    # In implementation: cost is passed to state_completed(), result_payload to agent_terminated()
```

**Note on `├─` vs `└─`:** During streaming, we cannot know which message is the last one. The implementation always uses `├─` for progress messages and prints the "Done" line separately after the stream completes. If a state has no progress messages (only tool invocations or nothing), the "Done" line still appears.

**Note on cost display:** The "Done" line shows `Done ($X.XX, total: $Y.YY)` where `$X.XX` is the cost for this specific invocation/state and `$Y.YY` is the workflow-wide accumulated total cost across all agents and invocations.

**Note on transition types:** All non-fork, non-result transitions (goto, reset, function, call) use the same `→` symbol. The distinction is internal to the orchestrator (e.g., function/call push to return stack, reset clears it) but doesn't need to be shown in console output for clarity.

## Implementation Approach

### Separate Logging from Console Output

Keep existing `logging` calls for debug/file output, but add a separate **console reporter** that handles user-facing output:

```python
class ConsoleReporter:
    def workflow_started(self, workflow_id: str, scope_dir: str, debug_dir: Optional[Path]) -> None
    def workflow_completed(self, total_cost: float) -> None
    def state_started(self, agent_id: str, state: str) -> None
    def progress_message(self, agent_id: str, message: str) -> None
    def tool_invocation(self, agent_id: str, tool_name: str, detail: Optional[str] = None) -> None
    def tool_error(self, agent_id: str, error_message: str, tool_name: Optional[str] = None) -> None
    def state_completed(self, agent_id: str, cost: float, total_cost: float) -> None  # cost is per-invocation cost for this state, total_cost is workflow-wide accumulated total
    def transition(self, agent_id: str, target: str, transition_type: str, spawned_agent_id: Optional[str] = None) -> None  # transition_type: "goto", "reset", "function", "call", "fork", "result". spawned_agent_id is used when transition_type is "fork"
    def agent_terminated(self, agent_id: str, result: str) -> None
    def error(self, agent_id: str, message: str) -> None
    def agent_spawned(self, parent_id: str, child_id: str, target_state: str) -> None  # Optional: may be used for alternative fork display or future features
    def script_started(self, agent_id: str, state: str) -> None
    def script_completed(self, agent_id: str, exit_code: int, duration_ms: float) -> None
```

Note: `tool_invocation` accepts an optional `detail` parameter (e.g., filename for Read/Write, command for Bash) so the reporter can format it consistently. `tool_error` accepts an optional `tool_name` to show which tool failed (requires tracking the last tool invocation per agent).

**Note on fork transitions**: Fork transitions are displayed via `transition(agent_id, target, "fork", spawned_agent_id)` where `target` is the fork target state (e.g., "WORKER.md") and `spawned_agent_id` is the new agent's ID (e.g., "main_worker1"). The `agent_spawned` method is provided for potential future use or alternative display formats.

### Integration Points

1. **run_all_agents()** - Workflow start/end, agent termination, workflow completion
2. **step_agent()** - State start, cost tracking, transitions, error/retry messages
3. **Stream processing loop (within step_agent)** - Extract and display progress messages from `wrap_claude_code_stream()` output
4. **_step_agent_script()** - Script execution start/end

The console reporter is accessed via a module-level singleton (see Implementation Decisions). The singleton is initialized in `run_all_agents()` with verbosity settings from CLI arguments.

## Configuration

Console output verbosity is controlled via CLI flag:

```
raymond --quiet workflow.md      # Only state transitions, errors, and final result
raymond workflow.md              # Default: transitions + all progress messages
raymond --verbose workflow.md    # Default output + DEBUG-level logging
```

**Note**: The `--quiet` flag needs to be added to the CLI argument parser. The `--verbose` flag already exists and controls logging verbosity; it will also enable DEBUG-level logging in addition to the default console output.

### Default behavior (no flag)

- Show state transitions
- Show all progress messages (assistant text, tool invocations)
- Show costs
- Show errors

### Quiet mode (`--quiet`)

- Show state headers (`[agent_id] state_name`) when states begin
- Show state transitions (the `→` lines)
- Show errors and warnings (the `!` lines)
- Show the "Done" line with cost (important summary information)
- Show final result (the `⇒ Result: ...` line)
- Show workflow completion message with total cost
- Do not show: assistant text messages, tool invocations, or script execution progress messages

Note: With multiple agents, transitions may interleave, but each line is prefixed with the agent ID for context. State headers are shown to provide context for transitions and errors.

### Verbose mode (`--verbose`)

Same as default, plus DEBUG-level log messages. This is primarily useful for debugging the orchestrator itself, not for normal workflow monitoring.

## Terminal Capabilities

Detect terminal capabilities and adjust:
- **Color support**: Use colors for agent IDs, errors
- **Unicode support**: Use box-drawing characters (├─ └─ → ⇒) or fall back to ASCII (|- -> =>). Fork symbol `⑂` falls back to `++`.
- **Width**: Truncate long messages to terminal width

## Design Decisions

1. **File paths in tool messages**: Show filename only (not full path) to keep lines compact
2. **Long assistant messages**: Truncate to first line, max 80 characters
3. **Multi-agent colors**: Assign distinct colors per agent ID, cycling through a palette (see Implementation Decisions)
4. **Transition type display**: All transitions (goto, reset, function, call) use the same `→` symbol for simplicity, even though they have different internal behaviors (stack management, session handling)

## Implementation Decisions

This section documents specific implementation choices made for the console output feature.

### ConsoleReporter Instantiation

**Decision**: Use a module-level singleton pattern. The `ConsoleReporter` will be instantiated once in the `orchestrator` module and accessed globally. This simplifies the implementation since console output is a cross-cutting concern throughout the orchestrator. The singleton can be initialized with verbosity settings from CLI arguments and accessed from any function that needs to report console output.

**Rationale**: Passing the reporter through every function call would require extensive signature changes. A singleton is appropriate here since there's only one console output stream and the reporter state (verbosity, terminal capabilities) is global.

### Tool Error Tracking

**Decision**: Track the last tool invocation per agent within the `ConsoleReporter` class. The reporter maintains a dictionary mapping `agent_id` to the last `(tool_name, detail)` tuple. When a tool error is reported, the reporter includes the tool name in the error message if available (e.g., `! [Write] error: ...`). If no tool name is available (shouldn't happen in normal operation), fall back to `! Tool error: ...`.

**Rationale**: Keeping this state in the reporter centralizes output formatting logic and makes it easier to maintain. The orchestrator just calls `tool_invocation()` and `tool_error()` without needing to track state itself. The tool name provides valuable context for debugging tool failures.

### Budget Exceeded Timing

**Decision**: Display budget exceeded message immediately after the stream completes and cost is calculated (current timing). The message appears after the "Done" line but before the transition/result line.

**Rationale**: The budget check happens after the invocation completes, which is the correct time to display it. The phrase "detected during invocation" in the design document refers to detection during the invocation cycle (after completion), not during the streaming phase.

### Quiet Mode Details

**Decision**: In quiet mode, show:
- State transitions (the `→` lines)
- Errors and warnings (the `!` lines)
- The "Done" line with cost (important summary information)
- Final result (the `⇒ Result: ...` line)
- Workflow completion message with total cost

Do not show:
- Assistant text messages (`├─ I'll begin...`)
- Tool invocations (`├─ [Write] file.txt`)
- Script execution progress (`├─ Executing script...`)

**Rationale**: The "Done" line provides essential cost information that users need to track spending. Quiet mode should still show the workflow structure (transitions) and important events (errors, completion), but suppress detailed progress.

### Fork Display Format

**Decision**: Display the full agent ID (e.g., `main_worker1`) in fork messages. Format: `⑂ WORKER.md → main_worker1` (or `++ WORKER.md -> main_worker1` in ASCII mode, with `->` instead of `→`). The format shows: fork symbol, target state file, arrow, spawned agent ID.

**Rationale**: The full agent ID is what users will see in all other contexts (debug files, state files, error messages), so consistency is important. The hierarchical naming already provides context about the parent-child relationship. The format `TARGET.md → agent_id` indicates that `agent_id` is starting execution at `TARGET.md`.

### Terminal Capabilities Detection

**Decision**: Use simple built-in detection without external dependencies:
- Check `sys.stdout.isatty()` to determine if output is to a terminal
- Check `os.getenv('TERM')` to detect terminal type (for Unicode support on Unix-like systems)
- For Windows, check `os.getenv('WT_SESSION')` for Windows Terminal, or detect UTF-8 capability via `sys.stdout.encoding`
- Fall back to ASCII if Unicode support is uncertain

**Rationale**: Avoid adding dependencies like `colorama` or `blessed` unless necessary. Python's standard library provides sufficient detection for basic needs. If more sophisticated terminal handling is needed later, it can be added.

### Script Execution Display

**Decision**: Show "Executing script..." message when script execution starts (before `run_script()` is called). Show the "Done" line with exit code and timing when script execution completes.

**Rationale**: Users should see that a script is running, especially since scripts may take time. The message appears at the start to provide immediate feedback, similar to how Claude Code invocations show progress messages.

### Workflow Completion Message

**Decision**: Display the "Workflow completed. Total cost: $X.XX" message in `run_all_agents()` when the workflow completes (when `state.get("agents", [])` is empty). Use `ConsoleReporter.workflow_completed(total_cost)` method, passing `state.get("total_cost_usd", 0.0)` as the total cost.

**Rationale**: This is workflow-level information, not agent-level, so it belongs in the main orchestrator loop. The reporter provides the formatting, but the orchestrator triggers it at the appropriate time. The total cost is workflow-wide (accumulated across all agents and invocations).

### Error/Retry Message Timing

**Decision**: Display error/retry messages immediately when detected, before the retry attempt begins. For example, after parsing fails and before the reminder prompt is sent, show: `! No transition tag - retrying (1/3)`.

**Rationale**: Users should know immediately when something goes wrong and that a retry is happening. This provides transparency into the orchestrator's error recovery process.

### Multi-Agent Output Interleaving

**Decision**: Print output immediately as it arrives (real-time interleaving). Do not buffer per-agent output. Each line is prefixed with the agent ID, so context is always clear even when output interleaves.

**Rationale**: Real-time output provides the best user experience - users see progress as it happens. Buffering would delay feedback and add complexity. The agent ID prefix on every line makes interleaved output readable.

### Terminal Width Handling

**Decision**: Detect terminal width using `shutil.get_terminal_size()` from the `shutil` module (if available) and truncate long messages to fit. If terminal width cannot be determined, use a default of 80 characters. Truncate with ellipsis (`...`) when needed.

**Rationale**: Long messages can break the visual structure. Truncating to terminal width keeps output readable while preserving the hierarchical format.

### Color Usage

**Decision**: Use colors by default if terminal supports them (no separate flag). Colors will be used for:
- Agent IDs (distinct color per agent, cycling through a palette)
- Error/warning messages (red/yellow)
- Transition arrows (subtle color to distinguish from regular text)

If terminal doesn't support colors, fall back to plain text.

**Rationale**: Colors improve readability when available, and there's no downside to using them automatically. Users who don't want colors can redirect output or use terminals that don't support them.

## Example Full Session

```
[21:06:17] Workflow: workflow_2026-01-24_21-06-17-009077
[21:06:17] Scope: workflows/test_cases/
[21:06:17] Debug: .raymond/debug/...

[main] START.md
  ├─ I'll begin the story by introducing our main character...
  ├─ [Write] story.txt
  └─ Done ($0.0353, total: $0.0353)
  → CONFLICT.md

[main] CONFLICT.md
  ├─ Now I'll develop the central conflict...
  ├─ [Read] story.txt
  ├─ [Edit] story.txt
  └─ Done ($0.0134, total: $0.0487)
  → RESOLUTION.md

[main] RESOLUTION.md
  ├─ Time to bring the story to a satisfying conclusion...
  ├─ [Edit] story.txt
  └─ Done ($0.0944, total: $0.1430)
  ⇒ Result: "Story complete"

Workflow completed. Total cost: $0.1430
```
