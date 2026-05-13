# Shutdown Signals for `ray serve`

This document describes a planned change to how `ray serve` handles
SIGINT and SIGTERM, and a simplification of the graceful-shutdown
model that goes with it.

It is a forward-looking change document. Until the change lands,
`docs/graceful-shutdown.md` describes the current three-tier model
(Tier 1 voluntary exit, Tier 2 quiesce, Tier 3 cancel) and the
`RAYMOND_STOP_REQUESTED` / `RAYMOND_STOP_SENTINEL` mechanism that
enables Tier 1.

## Summary

The change has three independent parts that compose:

1. **Signal mapping.** SIGINT and SIGTERM trigger different paths.
   SIGINT (1st) enters quiesce indefinitely; SIGINT (2nd) cancels.
   SIGTERM goes straight to cancel — no quiesce attempt.
2. **T2 timeout removal.** The quiesce phase itself stays — it is one
   of raymond's two remaining shutdown phases — but the previous
   5-minute `shutdown_tier2_timeout` cap is removed. Quiesce stays
   indefinitely until either a second SIGINT, a SIGTERM, SIGKILL from
   a supervisor, or all runs naturally park at state transitions.
3. **T1 removal.** Raymond no longer prescribes a voluntary-exit
   convention. The env var `RAYMOND_STOP_REQUESTED`, the env var
   `RAYMOND_STOP_SENTINEL`, and the on-disk `.raymond/shutdown.sentinel`
   file all go away. "Workflow notices a stop is wanted and exits
   terminally" remains a useful pattern, but it is implemented
   entirely in workflow-space using whatever coordination mechanism
   the project chooses (a stop-file convention, a scheduled-time
   check, a battery monitor, etc.).

After this change, raymond's own contribution to graceful shutdown is
exactly two phases: **quiesce** (park each run at its next state
transition) and **cancel** (propagate context cancellation, brief
bounded wait, exit). Anything cleaner than quiesce lives in
workflow-space.

## Signal mapping

| Trigger | Daemon behavior |
|---------|-----------------|
| SIGINT (1st) | Enter quiesce. No raymond-side timeout. Runs park at their next state transition; the daemon waits indefinitely for all of them. |
| SIGINT (2nd, or any subsequent SIGINT) | Cancel: propagate context cancellation to in-flight executors, bounded patience window for goroutines to honor it, then exit. |
| SIGTERM (any, including while in quiesce) | Equivalent to a 2nd SIGINT — go straight to cancel. No quiesce attempt. No "press twice" semantic; SIGTERM is a single shot from a supervisor. Subsequent SIGTERMs are idempotent. |
| SIGKILL | Kernel terminates the daemon process. No in-process handling possible. State files reflect the last completed atomic write (state writes use temp-then-rename — see `internal/state/state.go`); in-progress writes are lost and the prior version remains in place. Orphan `.tmp` files may be left behind in the serve pool (`.raymond/serve-state/`). |
| `POST /shutdown` | Equivalent to 1st SIGINT — enter quiesce. A second `POST /shutdown` while already quiescing is equivalent to 2nd SIGINT — cancel. No query parameters; the previous `?t1=…&t2=…` overrides are removed along with the timeouts. |
| Other signals (SIGHUP, SIGQUIT, etc.) | Not handled by raymond. The Go runtime's default disposition applies (e.g. SIGQUIT dumps goroutine stacks before exit; SIGHUP terminates). None of these are part of raymond's shutdown contract. |

### Why SIGTERM diverges from convention

The conventional Unix mapping is that SIGTERM means "please clean up
gracefully" and SIGKILL means "die now". Raymond does not follow that
convention for `ray serve`, and the reason is the operational reality
of containerized deployments:

- `docker stop` sends SIGTERM and then waits the configured grace
  period (default 10s) before escalating to SIGKILL.
- Kubernetes sends SIGTERM and then waits `terminationGracePeriodSeconds`
  before SIGKILL.
- A typical raymond state — an LLM call, a shell script doing real
  work — does not finish in 10 seconds. Trying to quiesce inside that
  window is wishful thinking; the supervisor SIGKILLs us mid-quiesce
  and we get the worst of both behaviors.

So `ray serve` treats SIGTERM as "the supervisor is in a hurry, do the
fast clean exit". The fast clean exit cancels run contexts (which
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
closing the ray server" case, and the behavior in that case is now
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

## T2 timeout removal

The quiesce phase (formerly Tier 2) is retained as one of raymond's
two shutdown phases. What is removed is its time bound: the
`shutdown_tier2_timeout` config key (and its 5-minute default) goes
away, and there is no raymond-imposed time bound on quiesce.

In practice, quiesce still terminates within a bounded time for most
workflows because:

- Per-state timeouts already cap any single step.
- A state that opts into no timeout is presumed intentional; the
  operator can wait, or escalate via a second SIGINT / a SIGTERM /
  SIGKILL.
- Supervisor grace windows (docker, k8s) are an external time bound
  that raymond does not need to duplicate.

The previous T2 default was arbitrary middle-ground and surprised
both ends — too short for some workloads, too long when an operator
wanted out. Removing it lets the bound come from wherever the
deployment actually has one.

The `shutdown_tier1_timeout` config key also disappears as part of
the T1 removal below.

## T1 removal

The voluntary-exit tier — and the entire mechanism that supports it
— is removed:

- `RAYMOND_STOP_REQUESTED` (env var) — removed. No longer set on any
  shell step's environment.
- `RAYMOND_STOP_SENTINEL` (env var) — removed.
- `.raymond/shutdown.sentinel` (on-disk file) — removed. Not written
  on shutdown request; not cleaned up on startup (no longer relevant).
- `daemon.ShutdownSignal` (the in-process object that backed both) —
  removed entirely (see implementation notes below).
- `shutdown_tier1_timeout` config key — removed.

### Why

The argument for keeping T1 was that it is the only fully-clean
shutdown tier: the workflow chooses its own exit point and finishes
terminally, leaving no parked state to resume. That property is real,
but it depends on a single assumption that turns out to be wrong: that
raymond knows when a voluntary exit is appropriate.

The events that should cause a workflow to exit voluntarily are
domain-dependent, not raymond-dependent. Examples that the current
mechanism cannot express cleanly:

- "Exit at the top of the next hour."
- "Exit when the coordinator workflow says so."
- "Exit when the host machine reports low battery."
- "Exit when a downstream queue depth exceeds a threshold."
- "Exit when an upstream sentinel file appears."

The current sentinel-on-shutdown scheme handles only the last of
these, and only when the trigger is raymond's own shutdown signal.
Anything else has to either ignore the sentinel and build its own
coordination, or piggyback awkwardly on it. Both are worse than
raymond not having an opinion.

After this change, the workflow author's path to "voluntarily exit
when X" is:

1. Define what "X" is in workflow-space (a stop-file convention, a
   scheduled time, a coordinator workflow's verdict, …).
2. Check for X at safe points in the workflow.
3. Emit any terminal `<result>…</result>` when X is true. The literal
   string `STOPPED` is a convention, not a runtime requirement —
   raymond treats any terminal result the same way.

Raymond's role shrinks to "receive the OS signal and act on it
(quiesce or cancel)." No env-var injection, no sentinel files, no
prompting workflows toward a particular exit shape. Whatever in-band
coordination workflows want among themselves is up to them.

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

This is the exact replacement for the current top-of-state and
in-loop sentinel-file checks in `graceful-shutdown.md`'s
"Workflow-author guidance" section, with the difference that the
sentinel is project-defined, not raymond-defined.

## Dependency on the disjoint-pool change

The disjoint-pool / auto-resume change in
[serve-run-pool.md](serve-run-pool.md) is a **prerequisite**, not a
sibling. Implement disjoint pools + auto-resume first; then land
this change.

The dependency is on auto-resume specifically:

- Without auto-resume, parked runs from quiesce accumulate as inactive
  entries that an operator has to resume by hand. Tier 2's previous
  timeout was partly a way to cap how much accumulation could happen
  per shutdown.
- With auto-resume, every parked run from quiesce comes back to life
  on the next `ray serve` startup. The cost of "quiesce stayed in
  quiesce for a long time" is bounded by the operator's restart
  cadence, not by raymond.
- Similarly, the cost of SIGTERM's fast cancel — runs re-enter their
  in-progress state from scratch — is paid implicitly by the
  auto-resume pass on next startup, not by an operator resuming
  by hand.

Auto-resume is the underlying property that lets raymond's
graceful-shutdown surface shrink to "quiesce or cancel" without
imposing operational toil.

## Documentation impact

`docs/graceful-shutdown.md` is substantially rewritten by this
change:

- The "three-tier cleanness model" section collapses to two phases
  (quiesce and cancel). T1 is removed entirely; the descriptions of
  T2 and T3 lose their "Tier" framing and become the two named
  phases.
- The "Stop signal" and "Sentinel file" sections are deleted.
- The "Workflow-author guidance: opting in to Tier 1" section is
  replaced by a brief "Workflow-author guidance: voluntary exit"
  section that points at the project-defined stop-file pattern as a
  reference idiom rather than a runtime contract.
- The "Resume guarantees per tier" table loses the Tier-1 row and
  cross-references [serve-run-pool.md](serve-run-pool.md) for the
  auto-resume behavior that subsumes operator-driven resume.
- The "Out of scope" list loses the "Active resume in serve mode"
  and "`asking`-state runs becoming answerable post-restart" bullets
  (both resolved by [serve-run-pool.md](serve-run-pool.md)).

The deployer-facing contract for SIGTERM/SIGKILL lives in this
document's "Container deployers still need raymond-specific docs"
section. The earlier "Operator helper" section in
`graceful-shutdown.md` (which referenced `container_dev/stop-ray.sh`)
has already been removed alongside the deletion of that folder; no
further edit there is needed.

`docs/configuration-file-design.md` does not currently document the
shutdown timeout keys — they live only in `graceful-shutdown.md`'s
"Config keys" section — so no edit there is needed beyond the
forward-pointer already in place.

`docs/daemon-server.md` "Run Recovery" is rewritten by the
disjoint-pool change; this change adds nothing to that rewrite.

## Implementation notes

Pointers for the implementer; not contractual surface.

- `internal/cli/serve.go:294` — the signal-handling block. Replace
  the single `signal.Notify` + select with a two-signal handler that
  distinguishes SIGINT 1st / SIGINT 2nd / SIGTERM, dispatching to
  quiesce or cancel accordingly. SIGINT 1st triggers quiesce with no
  timeout; SIGINT 2nd and SIGTERM both call `CancelAll()` directly.
- `internal/daemon/shutdowncoordinator.go` — the `Run(ctx, t1, t2)`
  signature loses its `t1` and `t2` parameters; the coordinator
  exposes a quiesce path and a cancel path that callers pick between.
  The `POST /shutdown` handler keeps its current "graceful by default"
  semantics by invoking the quiesce path.
- `internal/daemon/shutdownsignal.go` and
  `internal/daemon/shutdownsignal_test.go` — deleted.
- `internal/executors/script.go` — the `RAYMOND_STOP_REQUESTED` and
  `RAYMOND_STOP_SENTINEL` injection at executor spawn time is
  removed. `internal/executors/script_shutdown_env_test.go` is
  deleted or rewritten to assert the absence.
- `internal/daemon/runmanager.go` — wherever the coordinator's tier
  outcomes (`"clean"`, `"quiesced"`, `"killed"`) are produced or
  consumed, drop the `"clean"` outcome and rename `"killed"` to
  `"cancelled"`. Final outcomes are `"quiesced"` and `"cancelled"`.
  The rename matches the existing `POST /runs/{id}/cancel` surface
  and removes the kernel-vs-daemon ambiguity of "killed" (SIGKILL is
  the kernel killing the daemon; cancel is what the daemon does to
  its runs).
- `daemon.ShutdownSignal` (the in-process object behind the env-var
  and sentinel mechanism) is removed entirely. Its current consumers
  in the executor path go away with the env-var injection; the
  quiesce coordinator already drives orchestrator-level parking
  through `QuiesceAll()` and does not need a separate signal object.
- SSE events — `shutdown_requested` and `shutdown_complete` payloads
  lose the `tier_1_timeout_secs` / `tier_2_timeout_secs` fields and
  the `clean` outcome value. The events themselves stay, since
  operator UIs still need to see the request and the completion.
- Configuration loading — remove `shutdown_tier1_timeout` and
  `shutdown_tier2_timeout` from the `[raymond.serve]` config schema
  and from any precedence-resolution code.

## Test strategy

Per project conventions (TDD), tests are written alongside or before
the implementation. Minimum coverage:

- A daemon under load receives a single SIGINT → enters quiesce →
  remains alive indefinitely → all runs park at their next state
  transition → daemon exits cleanly when the last run is parked.
  (Quiesce-no-timeout test.)
- A daemon in quiesce receives a second SIGINT → cancel path runs →
  daemon exits within the bounded patience window. (SIGINT escalation
  test.)
- A daemon under load receives SIGTERM → cancel path runs immediately,
  no quiesce → daemon exits within the bounded patience window →
  state files reflect in-progress states. (SIGTERM fast-cancel test.)
- A shell step running under `ray serve` does **not** see
  `RAYMOND_STOP_REQUESTED` or `RAYMOND_STOP_SENTINEL` in its
  environment, regardless of shutdown state. (Env-removal test.)
- No file at `.raymond/shutdown.sentinel` is created during shutdown.
  (Sentinel-removal test.)
- SSE events `shutdown_requested` and `shutdown_complete` are emitted
  with their reduced payload shape on a quiesce-triggered shutdown.
  (Event-shape test.)
- The reference stop-file idiom is **not** a raymond test target.
  It is a workflow-level pattern that uses only existing primitives
  (shell `-f` check, terminal `<result>`); raymond has nothing
  raymond-specific to verify about it. Document the pattern in the
  workflow-author guidance and let workflow tests (if any project
  wants them) live alongside the workflows.

## Out of scope

- **Per-run quiesce / cancel.** This change is about the daemon's
  process-wide signal handling. Whether the HTTP API gains per-run
  endpoints to quiesce or cancel a single run independently is a
  separate question. (`POST /runs/{id}/cancel` already exists for
  immediate per-run cancel; per-run quiesce does not.)
- **Configurable patience window for the cancel path.** Deliberately
  not exposed (see "Cancel's patience window" above).
- **HTTP endpoint for fast cancel.** A `POST /cancel` (or
  `POST /shutdown?force=1`) that bypasses quiesce is not introduced;
  callers that want fast cancel send SIGTERM to the daemon process,
  or POST `/shutdown` twice.
- **Reintroducing a clean-exit primitive under a different name.**
  Out of scope; the design choice is that voluntary exit is workflow-
  domain.
