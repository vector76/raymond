# Graceful Shutdown (`ray serve`)

When `ray serve` is asked to stop — by `POST /shutdown`, by `SIGINT`,
or by a container runtime sending `SIGTERM` to PID 1 — the daemon runs
a deterministic two-phase sequence: **quiesce** (park each run at its
next state transition) and **cancel** (propagate context cancellation,
brief bounded wait, exit). Which phases run, and in what order, depends
on the trigger.

This document covers the architecture and rationale. The general daemon
HTTP surface and operator usage are documented in
[daemon-server.md](daemon-server.md).

## Signal mapping

| Trigger | Daemon behavior |
|---------|-----------------|
| SIGINT (1st) | Enter quiesce. No raymond-side timeout. Runs park at their next state transition; the daemon waits indefinitely for all of them. |
| SIGINT (2nd, or any subsequent SIGINT) | Cancel: propagate context cancellation to in-flight executors, bounded patience window for goroutines to honor it, then exit. |
| SIGTERM (any, including while in quiesce) | Equivalent to a 2nd SIGINT — go straight to cancel. No quiesce attempt. No "press twice" semantic; SIGTERM is a single shot from a supervisor. Subsequent SIGTERMs are idempotent. |
| SIGKILL | Kernel terminates the daemon process. No in-process handling possible. State files reflect the last completed atomic write (state writes use temp-then-rename — see `internal/state/state.go`); in-progress writes are lost and the prior version remains in place. Orphan `.tmp` files may be left behind in the serve pool (`.raymond/serve-state/`). |
| `POST /shutdown` | Equivalent to 1st SIGINT — enter quiesce. A second `POST /shutdown` while already quiescing is equivalent to 2nd SIGINT — cancel. No query parameters. |
| Other signals (SIGHUP, SIGQUIT, etc.) | Not handled by raymond. The Go runtime's default disposition applies (e.g. SIGQUIT dumps goroutine stacks before exit; SIGHUP terminates). None of these are part of raymond's shutdown contract. |

### Why SIGTERM diverges from convention

The conventional Unix mapping is that SIGTERM means "please clean up
gracefully" and SIGKILL means "die now". Raymond does not follow that
convention for `ray serve`, and the reason is the operational
reality of containerized deployments:

- `docker stop` sends SIGTERM and then waits the configured grace
  period (default 10s) before escalating to SIGKILL.
- Kubernetes sends SIGTERM and then waits `terminationGracePeriodSeconds`
  before SIGKILL.
- A typical raymond state — an LLM call, a shell script doing real
  work — does not finish in 10 seconds. Trying to quiesce inside that
  window is wishful thinking; the supervisor SIGKILLs us mid-quiesce
  and we get the worst of both behaviors.

So `ray serve` treats SIGTERM as "the supervisor is in a hurry, do
the fast clean exit". The fast clean exit cancels run contexts (which
propagates to in-flight shell children) and gives goroutines a few
seconds to honor the cancel before the daemon exits anyway. State
files end up recording "agent was in `STATE_X`"; auto-resume on the
next `ray serve` startup (see [serve-run-pool.md](serve-run-pool.md))
re-enters that state from the beginning.

If an operator does want graceful quiesce in a containerized
environment, the path is:

1. POST `/shutdown` (or send SIGINT to the daemon process directly).
2. Watch the streamed response for quiesce progress. The daemon
   continues to quiesce regardless of whether the HTTP client stays
   connected; a second POST `/shutdown` (or a SIGTERM) is the
   in-band escalation to cancel.
3. Then run `docker stop` or equivalent once the runs have parked.

Plain `docker stop` without a preceding graceful step gets the fast
cancel — which is the "someone stopped the container without first
closing the ray server" case, and the behavior in that case is
well-defined.

### Cancel's patience window

The cancel path has a fixed, non-configurable bounded wait for
goroutines to honor `ctx.Done()`. The target value is **5 seconds**:
long enough for context propagation through executor → orchestrator
→ runmanager to complete and for in-flight shell children to receive
and act on cancel signals; short enough to comfortably finish inside
a supervisor's default 10s grace window (docker, k8s) before SIGKILL
arrives.

Making this tunable invites the operator-vs-supervisor mistuning
problem this design exists to avoid: a long patience window gets
SIGKILL'd mid-cleanup; a short one is the same as not having one.
The constant is code-defined and documented, not a knob.

### Container deployers still need raymond-specific docs

Raymond's SIGTERM behavior diverges from the conventional Unix
contract (graceful), so a container deployer cannot rely on the
supervisor's grace window doing what they might assume. The signal
mapping table above is the contract. In particular:

- Plain `docker stop` / `kubectl delete pod` produces a fast cancel,
  not a quiesce. State files are at "agent in `STATE_X`".
- If graceful quiesce is wanted, the supervisor (or an entrypoint
  script) must POST `/shutdown` and wait for it to settle before
  letting the supervisor's SIGTERM fall through.

## The two-phase model

The daemon's contribution to graceful shutdown is exactly two phases.
Anything cleaner than quiesce lives in workflow-space (see
[Workflow-author guidance](#workflow-author-guidance-voluntary-exit)
below).

### Quiesce — pause at next state transition

The coordinator calls `QuiesceAll()`, which tells each orchestrator:
*finish the step you are in, then stop before entering the next
state*. The in-flight step (an LLM call or a shell process) is allowed
to complete naturally; only when the orchestrator returns to its main
loop and sees a transition tag does it park the agent.

A state transition is a natural safe point because by the time the
orchestrator has parsed a transition tag, the shell or LLM call has
fully returned — there is no half-finished side effect, and the state
file written at this boundary is consistent with "agent is about to
enter the next state". Resuming re-enters that next state from
scratch, which is lossless in the sense that no work performed before
the boundary is re-done.

Quiesce has no raymond-imposed time bound. It runs indefinitely until
either all runs naturally park, a second SIGINT or any SIGTERM
escalates to cancel, or the supervisor SIGKILLs the process. Per-state
timeouts already cap any single step; a state that opts into no
timeout is presumed intentional, and the operator can escalate
out-of-band.

### Cancel — context cancellation and bounded exit

The coordinator calls `CancelAll()`, which cancels each run's context
and propagates to any in-flight shell process. A brief bounded
patience window lets goroutines honour the cancel (see
[Cancel's patience window](#cancels-patience-window) above); whatever
has not terminated by then is recorded as cancelled and the daemon
exits anyway.

The state file at this point records "agent was in `STATE_X`". On the
next `ray serve` startup, auto-resume re-enters `STATE_X` from
the beginning. Work performed inside that state runs again from
scratch: dollars spent on a partially-completed LLM call are spent
again, and any non-idempotent side effects from a shell step are at
risk of running twice.

## SSE events

Two events are published on the global event stream (`GET /events`) so
operator UIs see them regardless of whether any per-run stream is open.

### `shutdown_requested`

Fired the moment `POST /shutdown` is handled (or the daemon is
signalled). Payload:

| Field | Meaning |
|-------|---------|
| `active_runs` | Snapshot of the runs that were active at request time: `{id, workflow, status}` each. |
| `requested_at` | Wall-clock timestamp of the request. |

### `shutdown_complete`

Fired just before the daemon exits. Payload:

| Field | Meaning |
|-------|---------|
| `outcomes` | Map of run ID → `"quiesced"` / `"cancelled"`, one entry per run that was active at request time. |

Both events are broadcast on the global stream. The web UI listens on
`/events` and surfaces phase transitions and per-run outcomes without
needing per-run subscriptions.

## Workflow-author guidance: voluntary exit

Raymond does not prescribe a voluntary-exit convention. There is no
env var or sentinel file injected into shell steps to signal "shutdown
was requested" — workflows that want to exit cleanly at a chosen
moment coordinate among themselves using whatever mechanism the
project picks (a stop-file convention, a scheduled-time check, a
coordinator workflow's verdict, a battery monitor, …).

If a workflow does emit any terminal `<result>…</result>` of its own
accord (in response to a project-defined trigger or otherwise), the
run finishes the same way it would on any normal completion: state
file is finalised, the run is removed from the active set, and no
resume is needed. Raymond treats any terminal result the same way —
the literal string `STOPPED` is a convention, not a runtime
requirement.

### Reference idiom: a stop-file workflow

For projects that want a Ctrl-C-equivalent voluntary-exit trigger —
"some operator action causes long-running workflows to exit cleanly
at their next safe point" — the conventional pattern is a tiny
workflow that drops a sentinel file on demand, plus a check in the
workflows that should respect it.

Sketch (`drop-stop-file.yaml`), using the project's single-file yaml
workflow shape (see `docs/yaml-workflows.md`):

```yaml
id: drop-stop-file
states:
  DROP:
    sh: |
      #!/usr/bin/env bash
      set -euo pipefail
      touch "$HOME/work/STOP_PLEASE"
      echo "<result>dropped</result>"
```

Worker check (top of state, or inside a polling loop):

```bash
if [ -f "$HOME/work/STOP_PLEASE" ]; then
    echo "<result>STOPPED</result>"
    exit 0
fi
```

The path, the filename, the placement of the check — all up to the
project. Raymond does not enforce any of it.

### Ask-driven workflows need nothing

Dialog-style workflows that pause on `<ask>` between rounds are
already drained whenever they reach the `asking` status — no shell or
LLM call is in flight at that moment. Quiesce explicitly drains
`asking` runs, so these workflows reach a clean stop without any
author-side changes.

## Resume guarantees

The state-file format is unchanged by graceful shutdown; what differs
is where in the workflow a resume picks up.

| Phase | State file written at | Auto-resume behaviour |
|-------|----------------------|-----------------------|
| Quiesce | Next-state boundary, after the in-flight step's transition was parsed. | Re-enters that next state cleanly. Behaviourally identical to resuming an `<ask>`-paused run. |
| Cancel | Records the in-progress state the agent was in. | Re-enters that state from the beginning. Interrupted work runs again from scratch. Lossy. |

In serve mode, the daemon scans the serve pool
(`.raymond/serve-state/`) on startup and resumes every non-terminal
state file as an active run — see
[serve-run-pool.md](serve-run-pool.md) for the pool layout and
auto-resume contract.

## Out of scope (deferred to follow-up)

The following concerns are interrelated and will be addressed together
in a follow-up design. Graceful shutdown deliberately stops short of
them:

- **`--launch` and `--resume` coexistence / idempotency.** A daemon
  invoked with both flags, or restarted with the same `--launch` list,
  must decide whether previously-launched runs are skipped, replaced, or
  duplicated. The semantics are not yet pinned down.
- **Agent introspection of live runs.** Inspecting a still-running
  orchestrator (state, step, recent output) from outside the daemon
  process is not yet exposed beyond the existing run-status surface.

Anything in this list that currently happens to work for some workflow
shape is incidental, not contractual.
