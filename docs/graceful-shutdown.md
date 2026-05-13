# Graceful Shutdown (`raymond serve`)

> **Planned rewrite.** The model described here is the current
> behavior. A planned change will collapse the three-tier model to two
> phases (quiesce, cancel), differentiate SIGINT and SIGTERM, and
> remove the `RAYMOND_STOP_REQUESTED` / `RAYMOND_STOP_SENTINEL`
> mechanism along with Tier 1. See
> [serve-shutdown-signals.md](serve-shutdown-signals.md) for the
> design and [serve-run-pool.md](serve-run-pool.md) for the
> disjoint-pool + auto-resume change it depends on.

When `raymond serve` is asked to stop — by `POST /shutdown`, by `SIGINT` /
`SIGTERM`, or by a container runtime sending `SIGTERM` to PID 1 — the daemon
runs a deterministic three-tier sequence that gives in-flight runs a chance to
finish cleanly, then to pause at a safe boundary, and finally to be cancelled.

This document covers the architecture and rationale. The general daemon HTTP
surface and operator usage are documented in
[daemon-server.md](daemon-server.md).

## The three-tier cleanness model

Cleanness is graded by how the run reached its terminal state. The
distinction matters because each tier has different cost (dollars, side
effects) and different resume semantics.

### Tier 1 — voluntary exit (clean)

The workflow notices that shutdown was requested and emits a terminal
transition of its own accord, typically `<result>STOPPED</result>`. The run
finishes the same way it would on any normal completion: state file is
finalised, the run is removed from the active set, and no resume is needed.

This is the only fully-lossless tier. The orchestrator did not have to
interrupt anything; the workflow chose its own exit point.

The Tier-1 window must be long enough to accommodate the workflow's natural
unit of work — for the worker-bead style of workflows that motivated this
design, that means a full bead (≈45–90 minutes). Default is one hour
(`shutdown_tier1_timeout = 3600`).

### Tier 2 — pause at next state transition (quiesced)

T1 elapsed with the run still alive. The coordinator now calls
`QuiesceAll()`, which tells each orchestrator: *finish the step you are in,
then stop before entering the next state*. The in-flight step (an LLM call
or a shell process) is allowed to complete naturally; only when the
orchestrator returns to its main loop and sees a transition tag does it
park the agent.

A state transition is a natural safe point because by the time the
orchestrator has parsed a transition tag, the shell or LLM call has fully
returned — there is no half-finished side effect, and the state file written
at this boundary is consistent with "agent is about to enter the next
state". Resuming re-enters that next state from scratch, which is lossless
in the sense that no work performed before the boundary is re-done.

T2 is the time given to one such step to finish. Default is five minutes
(`shutdown_tier2_timeout = 300`).

### Tier 3 — force-kill (killed)

T2 also elapsed: a step is genuinely stuck or its natural runtime exceeds
the budget. The coordinator calls `CancelAll()`, which cancels the run's
context and propagates to any in-flight shell process. A brief bounded
patience window lets goroutines honour the cancel; whatever has not
terminated by then is recorded as killed and the daemon exits anyway.

The state file at this point records "agent was in `STATE_X`". On resume,
the agent re-enters `STATE_X` from the beginning. Work performed inside
that state runs again from scratch: dollars spent on a partially-completed
LLM call are spent again, and any non-idempotent side effects from a
shell step are at risk of running twice.

## Stop signal: `RAYMOND_STOP_REQUESTED`

Every shell step that *starts* after `POST /shutdown` is dispatched with
`RAYMOND_STOP_REQUESTED=1` in its environment. The check is done at
executor spawn time, so the env var is cheap and accurate for steps that
begin during the shutdown window:

```go
if execCtx.ShutdownSignal != nil && execCtx.ShutdownSignal.IsRequested() {
    env["RAYMOND_STOP_REQUESTED"] = "1"
    env["RAYMOND_STOP_SENTINEL"] = execCtx.ShutdownSignal.SentinelPath()
}
```

This is sufficient for any state whose top-of-script check happens before
the workflow does any real work — the workflow simply sees the env var and
emits its terminal transition.

## Sentinel file: `RAYMOND_STOP_SENTINEL`

A shell step that was *already running* when shutdown was requested has a
stale environment — the env was captured at the executor's spawn time,
before the signal flipped, and the process's environment cannot be
retroactively mutated.

To give long-running steps a way to notice, the daemon also writes a
sentinel file on disk:

- Path: `.raymond/shutdown.sentinel` (joined onto the project's raymond
  directory).
- Exposed to shell steps via `RAYMOND_STOP_SENTINEL` (alongside
  `RAYMOND_STOP_REQUESTED`).
- Created by `ShutdownSignal.Request()` under the same lock that flips the
  in-memory state, so any observer that sees `IsRequested() == true` also
  sees the file on disk. Removed when the daemon exits cleanly; a stale
  sentinel from a crash is cleared at the next `serve` startup.

Long-running shell loops (workers polling `bs wait-ready`, polling loops
inside scripts, anything that does not return to the orchestrator
frequently) should re-check the sentinel each iteration:

```bash
if [ "$RAYMOND_STOP_REQUESTED" = "1" ] || [ -f "$RAYMOND_STOP_SENTINEL" ]; then
    echo "<result>STOPPED</result>"
    exit 0
fi
```

The env-var check covers the fast path; the file check covers the
already-running path. Both are needed.

## Config keys

Tier timeouts live under `[raymond.serve]` in `.raymond/config.toml`:

```toml
[raymond.serve]
# Voluntary-exit window. Must accommodate the workflow's natural unit of
# work — for worker beads that's ~45–90 minutes.
shutdown_tier1_timeout = 3600   # seconds, default 1h

# Quiesce window for the in-flight step to finish and the orchestrator
# to park at the next state boundary.
shutdown_tier2_timeout = 300    # seconds, default 5m
```

Per-invocation overrides are accepted on the HTTP endpoint:

```
POST /shutdown?t1=60&t2=30
```

Precedence (highest first): query → config → built-in default. Both query
parameters are non-negative numbers of seconds and may be fractional.

## SSE events

Two events are published on the global event stream (`GET /events`) so
operator UIs see them regardless of whether any per-run stream is open.

### `shutdown_requested`

Fired the moment `POST /shutdown` is handled (or the daemon is signalled).
Payload:

| Field | Meaning |
|-------|---------|
| `active_runs` | Snapshot of the runs that were active at request time: `{id, workflow, status}` each. |
| `tier_1_timeout_secs` | The T1 budget the daemon will honour (after query/config resolution). |
| `tier_2_timeout_secs` | The T2 budget. |
| `requested_at` | Wall-clock timestamp of the request. |

### `shutdown_complete`

Fired just before the daemon exits. Payload:

| Field | Meaning |
|-------|---------|
| `outcomes` | Map of run ID → `"clean"` / `"quiesced"` / `"killed"`, one entry per run that was active at request time. |

Both events are broadcast on the global stream. The web UI listens on
`/events` and surfaces tier transitions and per-run outcomes without
needing per-run subscriptions.

## Workflow-author guidance: opting in to Tier 1

Tier 1 is the only fully-clean shutdown path. Workflows opt in by checking
the stop signals at safe points and emitting a terminal transition. The
pattern depends on the workflow's shape.

### Top-of-loop env-var check

For workflows structured around a poll loop or a recurring "next bead"
state, the cheapest check is the env var at the top of each iteration:

```bash
if [ "$RAYMOND_STOP_REQUESTED" = "1" ]; then
    echo "<result>STOPPED</result>"
    exit 0
fi
```

This catches any iteration that starts after shutdown was requested — that
is, any time the orchestrator re-enters the state.

### In-loop sentinel-file check

For shell steps with their own internal long-running loop (workers polling
a queue, builds watching for changes, anything that does not return
between iterations), the env var alone is not enough: it was captured at
spawn time. The sentinel file is the way in:

```bash
while true; do
    if [ -f "$RAYMOND_STOP_SENTINEL" ]; then
        echo "<result>STOPPED</result>"
        exit 0
    fi
    bs wait-ready --next || sleep 5
done
```

### Combining both: a worker polling `bs wait-ready`

A realistic worker that picks the next bead and runs it should check the
env var once at the top of the state (cheap, catches the case where the
orchestrator re-entered after shutdown was already requested) *and* the
sentinel file inside its polling loop (catches the case where shutdown
was requested while this step was already mid-iteration):

```bash
# Top-of-state fast path: shutdown was already requested before we ran.
if [ "$RAYMOND_STOP_REQUESTED" = "1" ]; then
    echo "<result>STOPPED</result>"
    exit 0
fi

# Polling loop: env var is stale here (captured at spawn), so consult
# the on-disk sentinel each iteration.
while ! bs wait-ready --next > /tmp/bead.json 2>/dev/null; do
    if [ -f "$RAYMOND_STOP_SENTINEL" ]; then
        echo "<result>STOPPED</result>"
        exit 0
    fi
    sleep 5
done
```

Both checks are needed because the env var reflects the snapshot taken
when the executor spawned this step; a stop that flips *during* the step
is only visible through the sentinel.

### Ask-driven workflows usually don't need Tier 1

Dialog-style workflows that pause on `<ask>` between rounds are already
drained whenever they reach the `asking` status — no shell or LLM call is
in flight at that moment. T2's quiesce step explicitly drains `asking`
runs, so these workflows reach a clean stop without any author-side
changes. Adding an env-var check is harmless but unnecessary.

## Resume guarantees per tier

The state-file format is unchanged by graceful shutdown; what differs is
where in the workflow a resume picks up.

| Tier | State file written at | `raymond --resume <id>` behaviour |
|------|----------------------|-----------------------------------|
| Tier 1 | Run finished. State file is terminal. | No resume needed; the run is done. |
| Tier 2 | Next-state boundary, after the in-flight step's transition was parsed. | Re-enters that next state cleanly. Behaviourally identical to resuming an `<ask>`-paused run today. |
| Tier 3 | Records the in-progress state the agent was in. | Re-enters that state from the beginning. Interrupted work runs again from scratch. Lossy. |

In serve mode, current passive recovery behaviour is unchanged: at daemon
startup, the state directory is scanned and existing state files are
registered as inactive run entries (visible in the UI, resumable via the
CLI). An operator restarting the daemon decides per-run whether to
resume; automatic multi-run active resume is deferred — see "Out of
scope" below.

## Out of scope (deferred to follow-up)

The following four concerns are interrelated and will be addressed
together in a follow-up design. Graceful shutdown deliberately stops
short of them:

- **Active resume in serve mode (`--resume-all`).** A future flag could
  walk the state directory at startup and resume every non-terminal run
  automatically. Today the operator picks runs to resume by hand.
- **`--launch` and `--resume` coexistence / idempotency.** A daemon
  invoked with both flags, or restarted with the same `--launch` list,
  must decide whether previously-launched runs are skipped, replaced, or
  duplicated. The semantics are not yet pinned down.
- **Agent introspection of live runs.** Inspecting a still-running
  orchestrator (state, step, recent output) from outside the daemon
  process is not yet exposed beyond the existing run-status surface.
- **`asking`-state runs becoming answerable post-restart.** Today an
  `<ask>`-paused run registered after a fresh `serve` boot is inactive
  until explicitly resumed via the CLI; surfacing it as answerable
  through the daemon HTTP/UI without an explicit resume is future work.

Anything in this list that currently happens to work for some workflow
shape is incidental, not contractual.

