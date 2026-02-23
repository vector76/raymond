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
.raymond/debug/{workflow_id}/
```

Where `{workflow_id}` is the workflow identifier (e.g., `workflow_2026-01-15_14-30-22`).

Example: `.raymond/debug/workflow_2026-01-15_14-30-22/`

Each workflow run writes its debug files to the same directory. Files within the directory use `{agent_id}_{state_name}_{step_number}` naming to distinguish steps across runs.

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
