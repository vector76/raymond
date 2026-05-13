# Disjoint Run Pools: `ray` vs `ray serve`

This document describes the run-state layout: runs created by the CLI
(`ray <workflow>`) and runs created by the daemon (`ray serve`) live in
**disjoint state directories** and never appear in each other's recovery
scans.

## Motivation

Before the split, both `ray` and `ray serve` resolved their state
directory through `state.GetStateDir("")`, which returned
`<project>/.raymond/state/` for any caller. That single shared pool had
two operational consequences:

1. **The daemon's recovery scan swept up CLI residue.** Many workflows
   have no natural termination — they crunch until the operator hits
   Ctrl-C. Each such CLI run left a non-terminal state file. The next
   `ray serve` startup registered all of them as inactive entries,
   surfacing them in the UI and HTTP API even though they had nothing to
   do with the daemon's session.

2. **Active resume on startup was unsafe by default.** Because the pool
   was contaminated with stale CLI runs, "resume every non-terminal
   entry on `serve` startup" could not be the default — it would revive
   arbitrarily old experiments along with the daemon's own work. The
   compromise then in force (recovered runs are inactive, operator picks
   per-run) was a workaround for this contamination.

Disjoint pools fix the root cause. With the daemon's pool containing
only runs the daemon itself created, "resume everything non-terminal" is
the default, and the recovered-but-inactive intermediate state is gone
on the serve path.

## The layout

### Directory layout

| Pool | Path | Owner |
|------|------|-------|
| CLI | `<project>/.raymond/state/` | `ray <workflow>` invocations |
| Serve | `<project>/.raymond/serve-state/` | `ray serve` daemon |

The two are siblings, not parent-and-child. An enumeration that wants to
list both pools iterates two independent roots; no risk of one scan
accidentally descending into the other.

### Auto-resume on serve startup

`ray serve` resumes every non-terminal state file in `serve-state/` as an
**active** run on startup. There is no "recovered but inactive"
intermediate state for serve-pool runs — if the file is there and not
terminal, the daemon brings the run back to life.

Rationale: the serve pool is now curated. Every entry in it was created
by an earlier invocation of `ray serve` in this project. Reviving them
matches the operator's intent of "the daemon picks up where it left off",
analogous to `ray --resume <id>` for a single CLI run.

`asking`-state runs are resumed actively as well — their pending input
records (already in `pending_inputs.jsonl`) become answerable through
the HTTP/MCP transports immediately, without an explicit per-run resume
step. This closes the "asking runs becoming answerable post-restart"
item from `graceful-shutdown.md`'s out-of-scope list.

### `--clean` flag

`ray serve --clean` permanently abandons every non-terminal run in
`serve-state/` before the daemon starts:

- Abandoned state files are moved (not deleted) to
  `<project>/.raymond/serve-state/abandoned/<timestamp>/` so forensics
  remain possible.
- After the move, `serve-state/` is empty of non-terminal entries and the
  daemon starts with no active runs.
- Terminal state files (already-completed runs) are left in place.

This is safe to make destructive-by-move only because the pool is
disjoint: `--clean` cannot accidentally discard CLI work, because CLI
work is not in this directory.

### No cross-pool resume

Resume operations are scoped to a single pool. Specifically:

- `ray --resume <id>` looks for `<id>` in the CLI pool only. If the id
  exists only in the serve pool, the command fails with a message
  pointing the operator at the daemon: *"run `<id>` is managed by `ray
  serve`; resume it via the daemon."*
- The daemon's recovery scan reads only `serve-state/`. State files in
  `.raymond/state/` are not loaded into the daemon's view under any
  circumstance.
- There is no flag to "import" a CLI run into the serve pool or vice
  versa. If that capability is needed later, it is a separate, explicit
  feature, not a side effect of resume.

The prohibition is symmetric and unconditional. It preserves the
property that each state file is owned by exactly one runtime — the
CLI binary for files in `.raymond/state/`, the daemon for files in
`.raymond/serve-state/`. Disjoint directories make this property
true by construction; the no-cross-pool-resume rule keeps it true.

### Nested launches go to the CLI pool

A workflow that shells out to `ray <workflow_id>` — whether that
workflow is itself running under the CLI or under `serve` — writes the
nested run's state into `.raymond/state/` (the CLI pool). The `ray`
binary is not treated specially when invoked from another raymond
process: it is a normal shell command that happens to launch a workflow,
and its state lands wherever the CLI's resolution rule says.

This is intentional. Examples that motivate keeping the rule simple:

- A workflow could legitimately `cd ../other_project && ray <wf>` to
  drive work in a different project. The nested run is not part of the
  caller's serve session in any meaningful sense.
- Even when the working directory does not change, the nested process
  has its own lifecycle (its own SIGINT handling, its own budget, its
  own state file). Bundling it into the parent's pool would imply a
  parent/child resume relationship the runtime does not actually
  maintain.

The right primitive for "this nested work is part of my serve run" is
`<fork>` or `<fork-workflow>`, which run in-process under the same
orchestrator and produce no separate state file. Shelling out to `ray`
is the explicit choice to detach.

This means a serve-pool workflow that shells to `ray` will not see its
nested run in the daemon UI, and the daemon's `--clean` will not touch
it. Both are the intended consequences of choosing the shell-out path.

### Migration: none

Existing `.raymond/state/*.json` files became the CLI pool by definition
on first upgrade. No copy, no move, no rewrite.

`serve-state/` is created lazily on first `ray serve` startup. It starts
empty, so the first post-upgrade `serve` session recovers zero runs —
the correct outcome, because no previous serve session had a curated
pool to leave behind.

### Diagnostic surface

The CLI listing (`ray list`) continues to enumerate the CLI pool.

`ray serve list` enumerates the serve pool. The two views are not
merged into a single command by default. An operator who wants both
unions them explicitly.

`ray status <id>` is the one documented exception to "CLI tooling
never reads serve-state, and vice versa": as a read-only diagnostic
it may consult both pools. Ids are timestamp-with-microseconds plus
an in-pool collision counter (see `state.GenerateWorkflowID`), so
cross-pool collisions are vanishingly rare in practice but not
guaranteed impossible. The lookup checks the CLI pool first, then the
serve pool, and returns the first match; the error message on a
not-found stays generic.

## Related changes and dependencies

This change was one of a pair. The companion document
[serve-shutdown-signals.md](serve-shutdown-signals.md) rewrites the
signal-handling and graceful-shutdown surface (SIGINT/SIGTERM mapping,
removal of Tier 1 entirely, removal of Tier 2's *timeout* — quiesce
itself stays as raymond's graceful phase — and removal of
`RAYMOND_STOP_REQUESTED`).

Disjoint pools and auto-resume landed first; the shutdown-signals
change depends on auto-resume for its cost calculus — without
auto-resume, dropping Tier 1 and the Tier-2 timeout would leave parked
runs that an operator had to revive by hand.

## Out of scope for this change

- **Cross-project serve scopes.** If a single `ray serve` invocation
  ever serves multiple `.raymond/` directories, this document's
  "per-project pool" framing has to be revisited. Today serve is
  per-project and the rule is unambiguous.
- **Lock-file ownership enforcement.** Disjoint pools give the
  "one runtime per state file" property by construction (CLI cannot
  reach into the serve pool, daemon cannot reach into the CLI pool).
  Adding explicit locks (e.g. for two concurrent `ray serve`
  invocations in the same project) is a separate question.

## Implementation pointers

Pointers into the code; not contractual surface.

- `internal/state/state.go` (`ResolvePoolDir`, `GetStateDir`) — the
  resolution point. CLI callers in `internal/cli/cli.go` request the CLI
  pool; callers in `internal/cli/serve.go` and
  `internal/daemon/runmanager.go` request the serve pool.
- `internal/daemon/runmanager.go` (`NewRunManagerForServe` →
  `recoverRuns`) — registers every non-terminal serve-pool entry as an
  active run. The "inactive entry" code path remains in the runmanager
  for the CLI-side `ray --resume` flow but the daemon no longer
  produces inactive entries on its own.
- `internal/cli/serve.go` — the `--clean` flag and its move-to-
  `abandoned/<timestamp>/` behavior, ahead of `NewRunManagerForServe`.

## Regression coverage

Tests pinning the pool contract:

- A serve startup with stale CLI state files present in `.raymond/state/`
  must not register them in the daemon. (Pollution-isolation test.)
- A serve startup with non-terminal state files in `serve-state/` must
  register them as active runs, not inactive. (Auto-resume test.)
- `ray --resume <serve-pool-id>` must fail with the
  pool-mismatch message. (Cross-pool prohibition test.)
- `ray serve --clean` must move non-terminal files to
  `serve-state/abandoned/<timestamp>/` and start with an empty active
  set; terminal files in `serve-state/` must be left untouched.
- A workflow that shells out to `ray <wf>` from inside a serve run must
  produce a state file in `.raymond/state/`, not `serve-state/`, and
  must not appear in the daemon's run list. (Nested-launch routing
  test.)
- An `asking`-state run recovered from `serve-state/` on serve startup
  must have its pending input answerable via the HTTP transport without
  an explicit resume step. (Recovered-ask answerability test.)
