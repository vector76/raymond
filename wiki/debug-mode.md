# Debug Mode Feature

## Overview

Debug mode preserves complete workflow execution history for analysis and troubleshooting. Since state files are overwritten during execution, debug mode creates a separate directory structure to retain all information from a workflow run.

## Motivation

The orchestrator's state file is overwritten on each state transition, making it difficult to:
- Analyze what happened during a workflow execution
- Debug why a workflow took a particular path
- Review Claude Code responses for each step
- Understand the sequence of state transitions
- Track costs and session IDs across the workflow

Debug mode solves this by creating a permanent record of every step.

## Usage

Debug mode is **enabled by default**. Debug information is automatically saved to `.raymond/debug/` for all workflow runs.

To disable debug mode, use the `--no-debug` flag:

```bash
raymond workflows/test/START.md --no-debug
raymond --resume workflow_2026-01-15_14-30-22 --no-debug
```

By default (without `--no-debug`):

```bash
raymond workflows/test/START.md
raymond --resume workflow_2026-01-15_14-30-22
```

## Directory Structure

When debug mode is enabled (the default), a debug directory is created for each workflow run:

```
.raymond/debug/{workflow_id}_{timestamp}/
```

Where:
- `{workflow_id}` is the workflow identifier (e.g., `workflow_2026-01-15_14-30-22`)
- `{timestamp}` is the execution start time in format `YYYYMMDD_HHMMSS` (e.g., `20260115_143022`)

Example: `.raymond/debug/workflow_2026-01-15_14-30-22_20260115_143022/`

This ensures each run gets a unique directory, even if the same workflow is run multiple times.

## Files Saved

### 1. Claude Code JSONL Outputs

One JSONL (JSON Lines) file is saved for each agent step (each invocation of Claude Code).

**Why JSONL?**
Debug files use JSONL format (one JSON object per line) instead of a JSON array because:
- **Progressive writes**: Each JSON object is written immediately as it arrives from Claude Code
- **Crash resilience**: If Claude Code times out or crashes, all received data is preserved
- **Streaming support**: Enables real-time console output of Claude Code steps
- **Idle timeout support**: The streaming approach allows detecting "stuck" executions

**Filename Format:**
```
{agent_id}_{state_name}_{step_number}.jsonl
```

Where:
- `{agent_id}` is the agent identifier (e.g., `main`, `main_worker1`, `main_worker1_analyz1`)
  - Forked agents use compact hierarchical underscore notation: `{parent_id}_{state_abbrev}{counter}`
  - `state_abbrev` is the first 6 characters of the target state name (lowercase, no .md)
  - Counters start at 1 and increment for each fork from the same parent
  - This ensures unique, informative names even after previous workers have terminated
  - Names use underscores, making them valid identifiers
  - Examples: `main_worker1`, `main_analyz1`, `main_worker1_proces1`
- `{state_name}` is the prompt filename without extension (e.g., `START`, `REVIEW`, `ANALYZE`)
- `{step_number}` is a zero-padded 3-digit sequence number per agent (e.g., `001`, `002`, `003`)

**Examples:**
- `main_START_001.jsonl` - First step of main agent in START.md
- `main_REVIEW_002.jsonl` - Second step of main agent in REVIEW.md
- `main_worker1_ANALYZE_001.jsonl` - First step of forked worker agent (from WORKER.md) in ANALYZE.md
- `main_worker1_analyz1_PROCESS_001.jsonl` - First step of nested worker agent

**File Contents:**
Each line contains a single JSON object from Claude Code's stream-json output. Objects are written progressively as they arrive, including:
- Message content
- Session IDs
- Cost information (`total_cost_usd`)
- Any other metadata returned by Claude Code

**Example File:**
```jsonl
{"type": "content", "text": "I'll analyze this code."}
{"type": "content", "text": "\n<goto>REVIEW.md</goto>"}
{"type": "result", "total_cost_usd": 0.05, "session_id": "session_abc123"}
```

**Reading JSONL Files:**
```python
import json

def read_jsonl(filepath):
    results = []
    with open(filepath, 'r', encoding='utf-8') as f:
        for line in f:
            if line.strip():
                results.append(json.loads(line))
    return results
```

### 2. State Transition Log

A single text file `transitions.log` contains a chronological log of all state transitions.

**Format:**
Each entry includes:
- Timestamp (ISO format)
- Agent ID
- Old state → New state
- Transition type (goto, reset, function, call, fork, result)
- Transition target (if applicable)
- Session ID (if changed)
- Stack depth
- Cost information (invocation cost and cumulative total)
- For fork transitions: spawned agent ID
- Any other relevant metadata

**Example Log:**
```
2026-01-15T14:30:22.123456 [main] START.md -> NEXT.md (goto)
  session_id: session_abc123 (new)
  cost: $0.05
  total_cost: $0.05
  stack_depth: 0

2026-01-15T14:30:45.789012 [main] NEXT.md -> REVIEW.md (goto)
  session_id: session_abc123 (resumed)
  cost: $0.03
  total_cost: $0.08
  stack_depth: 0

2026-01-15T14:31:02.345678 [main] REVIEW.md -> ANALYZE.md (function)
  return_state: REVIEW.md
  session_id: None (fresh)
  cost: $0.02
  total_cost: $0.10
  stack_depth: 1

2026-01-15T14:31:15.567890 [main] ANALYZE.md -> REVIEW.md (result)
  session_id: session_abc123 (resumed)
  result_payload: "Analysis complete: 3 issues found"
  cost: $0.01
  total_cost: $0.11
  stack_depth: 0

2026-01-15T14:31:25.123456 [main] DISPATCH.md -> DISPATCH.md (fork)
  spawned_agent: main_worker1
  session_id: session_abc123 (resumed)
  cost: $0.02
  total_cost: $0.13
  stack_depth: 0

2026-01-15T14:31:30.123456 [main] REVIEW.md -> (result, terminated)
  session_id: session_abc123
  result_payload: "Review complete"
  cost: $0.02
  total_cost: $0.15
  stack_depth: 0
```

## Implementation Details

### 1. CLI Integration

Debug mode is enabled by default. Add `--no-debug` flag to argument parser for both `start` and `run` commands:

```python
parser.add_argument(
    "--no-debug",
    dest="no_debug",
    action="store_true",
    help="Disable debug mode (debug mode is enabled by default)"
)
```

The debug flag is passed through to `run_all_agents()` with a default of `True`:

```python
async def run_all_agents(
    workflow_id: str, 
    state_dir: str = None,
    debug: bool = True
) -> None:
```

### 2. Debug Directory Creation

Create a function to initialize the debug directory:

```python
def create_debug_directory(workflow_id: str, state_dir: Optional[str] = None) -> Optional[Path]:
    """Create debug directory for workflow execution.
    
    Args:
        workflow_id: Workflow identifier
        state_dir: Optional custom state directory (used to determine .raymond location)
        
    Returns:
        Path to debug directory, or None if creation fails
    """
    # Determine base directory (same parent as state_dir)
    state_path = get_state_dir(state_dir)
    base_dir = state_path.parent  # .raymond/
    
    # Generate timestamp
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    
    # Create debug directory path
    debug_dir = base_dir / "debug" / f"{workflow_id}_{timestamp}"
    
    try:
        debug_dir.mkdir(parents=True, exist_ok=True)
        return debug_dir
    except OSError as e:
        logger.warning(f"Failed to create debug directory: {e}")
        return None
```

Call this at the start of `run_all_agents()` when `debug=True` (the default).

### 3. Saving Claude Code Outputs (Progressive/Streaming)

Debug files are written progressively as JSON objects stream from Claude Code. This approach:
- Preserves data even if Claude Code times out or crashes
- Enables real-time console output
- Supports idle timeout detection

In `step_agent()`, before invoking Claude Code, prepare the debug filepath:

```python
# Get filepath for progressive writes
debug_filepath = get_claude_output_filepath(
    debug_dir=debug_dir,
    agent_id=agent_id,
    state_name=state_name,
    step_number=step_number
)
```

Then stream from Claude Code and write each object as it arrives:

```python
# Stream JSON objects from Claude Code
async for json_obj in wrap_claude_code_stream(prompt, ...):
    results.append(json_obj)
    
    # Progressive write to debug file
    if debug_filepath is not None:
        append_claude_output_line(debug_filepath, json_obj)
    
    # Extract session_id if present
    if "session_id" in json_obj:
        new_session_id = json_obj["session_id"]
```

Implement step number tracking (per agent):

```python
# In run_all_agents(), maintain a dict:
agent_step_counters = {}  # {agent_id: step_number}

# In step_agent(), increment:
if agent_id not in agent_step_counters:
    agent_step_counters[agent_id] = 0
agent_step_counters[agent_id] += 1
step_number = agent_step_counters[agent_id]
```

Helper functions:

```python
def get_claude_output_filepath(debug_dir, agent_id, state_name, step_number) -> Path:
    """Get the filepath for a Claude Code JSONL output file."""
    filename = f"{agent_id}_{state_name}_{step_number:03d}.jsonl"
    return debug_dir / filename

def append_claude_output_line(filepath: Path, json_object: Dict[str, Any]) -> None:
    """Append a single JSON object to a JSONL debug file."""
    try:
        with open(filepath, 'a', encoding='utf-8') as f:
            f.write(json.dumps(json_object, ensure_ascii=False) + '\n')
    except OSError as e:
        logger.warning(f"Failed to append Claude output to {filepath}: {e}")
```

### 4. State Transition Logging

Create a function to log transitions:

```python
def log_state_transition(
    debug_dir: Optional[Path],
    timestamp: datetime,
    agent_id: str,
    old_state: str,
    new_state: Optional[str],
    transition_type: str,
    transition_target: Optional[str],
    metadata: Dict[str, Any]
) -> None:
    """Log state transition to transitions.log file.
    
    Args:
        debug_dir: Debug directory path (None if debug disabled)
        timestamp: Transition timestamp
        agent_id: Agent identifier
        old_state: Previous state filename
        new_state: New state filename (None if agent terminated)
        transition_type: Type of transition (goto, reset, function, call, fork, result)
        transition_target: Transition target filename
        metadata: Additional metadata (session_id, cost, stack_depth, etc.)
    """
    if debug_dir is None:
        return
    
    log_file = debug_dir / "transitions.log"
    
    try:
        with open(log_file, 'a', encoding='utf-8') as f:
            # Format log entry
            if new_state:
                f.write(f"{timestamp.isoformat()} [{agent_id}] {old_state} -> {new_state} ({transition_type})\n")
            else:
                f.write(f"{timestamp.isoformat()} [{agent_id}] {old_state} -> (result, terminated)\n")
            
            # Write metadata
            for key, value in metadata.items():
                f.write(f"  {key}: {value}\n")
            
            f.write("\n")
    except OSError as e:
        logger.warning(f"Failed to write to transitions.log: {e}")
```

Call this in transition handlers and in `step_agent()` after transitions are processed.

### 5. Error Handling

Debug mode should be non-blocking:
- If debug directory creation fails, log a warning but continue workflow
- If file writing fails, log a warning but continue workflow
- Never raise exceptions from debug operations
- Debug failures should not affect workflow execution

### 6. Integration Points

Key places to add debug logging:

1. **In `run_all_agents()`:**
   - Create debug directory at start (if `debug=True`)
   - Initialize step counters dict
   - Pass debug_dir to `step_agent()`

2. **In `step_agent()`:**
   - Save Claude Code results after `wrap_claude_code()` returns
   - Log transition after parsing and validation
   - Include cost information from state

3. **In transition handlers:**
   - Log transition with full context
   - Include stack depth, session_id changes, etc.

4. **In budget override:**
   - Log when budget is exceeded and transition is overridden

## Testing

Test cases should verify:

1. Debug directory is created with correct naming
2. Claude Code outputs are saved with correct filenames
3. Step numbers increment correctly per agent
4. State transitions are logged in chronological order
5. Debug mode doesn't fail workflow on file errors
6. Multiple agents get separate step sequences
7. Forked agents get their own JSON files
8. Debug directory structure is correct

## Benefits

- **Complete History**: Every Claude Code response is preserved
- **Reproducibility**: Can analyze exactly what happened
- **Debugging**: Easy to see where workflows went wrong
- **Cost Analysis**: Track costs per step
- **Session Tracking**: See how sessions are used/resumed
- **State Evolution**: Understand how state changed over time

## Future Enhancements

Potential additions:
- Save rendered prompts (before template substitution)
- Save state file snapshots at each transition
- Include timing information (duration per step)
- Save error information to debug directory
- Compress old debug directories
- Add debug directory cleanup command
