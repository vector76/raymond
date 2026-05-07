# Transition from `bm` to pure `raymond serve`

This document captures the design discussion and decisions for retiring
`backlog_manager` (`bm`) in favor of running everything through
`raymond serve` and the filesystem. Self-contained — read this without
prior conversation context.

## Baseline (what's running today)

- **`backlog_manager` (`bm`)** — Go client/server. Server (`bm serve`) runs
  on the host with REST API + multi-project web dashboard. The `bm` CLI
  runs inside this container, talks to the server via `BM_URL` + `BM_TOKEN`.
  Source: `~/work/backlog_manager/`.
- **`beads_server`** — separate service, tracks beads (atomic work items).
  `bm` wraps beads_server: `bm claim` / `bm list` / `bm close` etc. proxy to
  beads_server. The atomic claim mechanism is in beads_server, not bm.
- **`raymond` (`ray`)** — workflow orchestrator. Source: `~/work/raymond/`.
- **Active workflow:** `~/work/workflow_samples/work_and_agent.yaml`. Single
  YAML bundling two coordinated workflows in one ray invocation:
  - **Agent fork (`agent.yaml`)** — polls `bm claim-feature`, branches into
    DIALOG (refine feature spec) or GENERATE (plan → bead-list → create
    beads in beads_server). Uses per-feature scratch in `.bm/<agent_id>/`.
  - **Work loop** — `bm wait-ready` then loop CLAIM → WORK → REVIEW →
    DID_FIX/AGAIN_CHOICE → COMMIT → PUSH_ATTEMPT → (REBASE/RESOLVE/RETEST/
    REWORK on push rejection) → CLOSE_TASK, with BAIL_OUT releasing the
    bead. (REVIEW `<call>`s DID_FIX, which `<result>`s to AGAIN_CHOICE;
    AGAIN_CHOICE loops to REVIEW or proceeds to COMMIT.) Counter-capped
    push retries via `.raymond/.push_attempts` (MAX_ATTEMPTS=5).

`bm`'s job, distilled, is: (a) durable feature lifecycle (`draft` →
dialog → `fully_specified` → `generating` → `beads_created` → `done`),
(b) human-input routing (which feature is asking whose response),
(c) multi-project web dashboard.

## Why the transition is sound now

`bm` was built before raymond had `<ask>`. With raymond's `<ask>` tag
plus `raymond serve`'s pending-input registry, web UI, run recovery, and
SSE streaming, raymond covers (a) and (b) natively. (c) is the deliberate
trade — see "Accepted losses" below.

A reference example for `<ask>` is at `~/work/wf_new/story_input.yaml`
(workflow uses `input: mode: required` to bootstrap, then `<ask
next="REVIEW">prompt</ask>` to ask the human, with run recovery making
it durable across daemon restarts).

Crucially: **`beads_server` is the load-bearing piece for multi-worker
coordination, not `bm`.** Atomic claim is in beads_server. So the WORK
side of the existing workflow needs only `bm` → `bs` substitution; the
agent side is what genuinely needs replacing.

## Accepted losses (vs. `bm`)

| Lost | Mitigation |
|---|---|
| Multi-project dashboard | Single project; if ever needed, one daemon per project on different ports |
| Cross-run "feature is in `awaiting_human`" board | Raymond UI's pending-asks list covers active features; idle features visible by listing `features/` |
| Bead → feature backreference | Bead descriptions cite `features/NNN_<slug>.md` (greppable). Optionally label/epic the bead in beads_server. |
| Round-by-round dialog audit trail | Skip by default. If missed later, finalize step can dump transcript alongside feature doc. |

## Target architecture

### Components

- One `raymond serve` daemon, single project.
- `beads_server` retained as-is.
- Three workflows replace `bm` + `agent.yaml` + work loop:
  1. **`feature_dialog.yaml`** — interactive spec refinement via `<ask>`.
  2. **`feat_to_beads.yaml`** — turns finalized feature doc into beads.
  3. **`work.yaml`** — claim a bead, implement, commit, push, rebase on
     rejection. (The current work loop with `bm` → `bs` substitution.)
- Workers run as separate concurrent `work.yaml` runs.

### File layout in the repo

- **Features live as flat numbered files: `features/NNN_<slug>.md`.**
  Three-digit zero-padded number, kebab-case slug.
  Example: `features/001_add_backup_restore_capability.md`.
- **No nested per-feature directories.** Numeric prefix gives chronological
  signal at a glance; nesting just adds noise.
- **No persistent `plan.md` / `beads.md`.** They are transient artifacts of
  bead generation, used to drive `feat_to_beads.yaml`, then discarded.
- **No append-to-feature with plan content.** The feature doc stays a clean
  what-spec; the plan is how-spec; mixing dilutes both. Once beads exist,
  the beads themselves are the materialized plan. If this turns out to be
  missed later, adding it is a one-line workflow change; easy to add, hard
  to retract once it's a convention.

### State partitioning (drafts vs. finalized)

| Artifact | Where it lives |
|---|---|
| In-progress dialog state (working draft, prior questions, prior responses) | Raymond run state (per-run task folder), outside the repo |
| Finalized feature spec | `features/NNN_<slug>.md` in the repo |
| Plan and bead-list during `feat_to_beads.yaml` | Raymond run state, transient, never committed |
| Beads | `beads_server` (long-term) |

The transition from "draft in run state" to "finalized in repo" is an
explicit step in the workflow: when the human signals done, the workflow
commits and pushes the feature file before triggering bead generation.
That commit is the moral equivalent of `bm`'s `awaiting_client [final
flag] → fully_specified` transition.

## Workflow specifications

### `feature_dialog.yaml`

- `input.mode: required` to capture the seed description from the human
  on workflow launch.
- One long-lived run, multiple `<ask>` rounds inside it. Run recovery
  brings it back across daemon restarts.
- Roughly:
  - **ANALYZE**: read the working draft from run state, identify gaps and
    ambiguities, update the draft, emit
    `<ask next="ANALYZE">questions/assumptions</ask>`. Finalization
    conventions (mirroring `feat_dev_tg`'s `PROMPT.sh`; matching is
    case-insensitive and trims whitespace):
    - Last non-blank line is `done`, with other content above it →
      apply the response to the draft (with the trailing `done` line
      stripped), then transition to FINALIZE.
    - Whole response is just `done` (or empty) → transition directly to
      FINALIZE without applying further changes (locks in the current
      draft as-is).
    - To abandon a dialog: cancel the run via the daemon (`POST
      /runs/{id}/cancel`). No in-workflow abandon path needed.
  - **FINALIZE**:
    1. `git pull --rebase`
    2. Scan `features/` for the highest existing number; pick next.
    3. Agent generates kebab-case slug from the feature title.
    4. Write file at `features/NNN_<slug>.md`.
    5. `git add` + commit.
    6. `git push`.
    7. On push rejection, loop back to step 1. Each retry re-picks the
       number and renames the file (a concurrent finalize may have taken
       the slot you scanned for).
  - Run terminates. The user separately launches `feat_to_beads.yaml`
    against the new file.

**Default to one long-lived run with multiple `<ask>` rounds** rather
than one-run-per-round. Matches `<ask>`'s intent and is simpler.
Reconsider only if run recovery proves flaky in practice.

### `feat_to_beads.yaml`

- Input: `--input features/NNN_<slug>.md`.
- Mostly a mechanical port of `agent.yaml`'s GENERATE branch:
  GENERATE_DRAFT_PLAN → REVIEW_PLAN → EXPLORE_CODEBASE → REVIEW_BEADS →
  CREATE_BEADS → VALIDATE.
- Plan and bead-list live in raymond run state during the run. Discarded
  on cleanup. Not committed.
- Creates beads in `beads_server` via `bs add` / `bs link` / `bs edit`.
- Workflow terminates after VALIDATE.

**Direct-to-bead path.** `bm`'s data model has a `direct_to_bead` flag
for features that skip the dialog entirely. The equivalent in the new
world: hand-write `features/NNN_<slug>.md`, commit, and launch
`feat_to_beads.yaml` against it directly. No special workflow needed —
the absence of `feature_dialog.yaml` *is* the direct path.

### `work.yaml`

- Existing WORK loop from `work_and_agent.yaml`, with `bm` calls swapped
  for `bs` calls.
- States preserved: CLAIM → WORK → REVIEW → DID_FIX/AGAIN_CHOICE → COMMIT
  → PUSH_ATTEMPT → (REBASE/RESOLVE/RETEST/REWORK on rejection) →
  CLOSE_TASK, with BAIL_OUT releasing the bead.
- CLAIM continues to `git pull` so the worker has the current `features/`
  directory available before working on the bead.

## Bead self-containedness principle

The original `bm` model treated beads as fully self-contained and that's
correct — beads run in fresh Claude contexts with no inter-bead
communication. Self-containedness is load-bearing for parallel workers.

`feat_to_beads.yaml` writes bead descriptions that:

- **Inline-quote or paraphrase the relevant slices of the feature doc**
  (specific behaviors, constraints, file paths the bead touches).
- **End with a fallback citation** along the lines of:
  `For broader context if needed, see features/NNN_<slug>.md.`
- **Aim to not need the citation in the common case.**

Mental model: **the feature doc is for humans and for bead authoring; beads
are for workers. The doc is not part of the worker's working set.** The
citation is a fallback when a bead encounters genuine ambiguity, not the
primary source of truth for the bead.

This means the feature document can be large without polluting worker
context — only the slices actually relevant to a given bead get pulled
into that bead's description. (Plan and bead-list documents are never
seen by workers at all; they live only in `feat_to_beads.yaml`'s run
state and are discarded.)

## Multi-worker plan

### Scope and rationale

- Target: **1–3 workers**, not 10+.
- Reason: rebase difficulty grows nonlinearly with concurrent workers.
- Future possibility (out of scope now): a smart allocator that predicts
  bead overlap and permits more concurrency when overlap is low. Worth
  noting only so we don't design ourselves out of it.

### Topology options

- **Worktrees in the same container** — cheapest. One daemon, multiple
  `work.yaml` runs, each with a distinct `working_directory`. Unified
  dashboard. **Start here.**
- **Separate containers per worker** — needed if isolation or dependency
  divergence demands it. Each container runs its own `raymond serve`. All
  containers point at the shared `beads_server`. No unified dashboard
  until/unless an aggregator is built.

Decide between these only after the verification spike below reveals
whether single-container worktrees are sufficient.

### Working-directory partitioning

A subtle but real consequence of concurrent runs: **every concurrent run
must have its own `working_directory`.** Today, `work_and_agent.yaml`
combines agent + worker in one ray invocation sharing one directory,
which is fine because there's only one of each. In the new design you
can have, simultaneously: several `feature_dialog` runs, a `feat_to_beads`
run, and N `work.yaml` runs — all doing git operations, with workers
holding long-lived uncommitted state.

Suggested layout per worker / dialog / generation:

- One worktree per active `work.yaml` run (long-lived, dirty between
  bead claim and push).
- A separate worktree (or pool of ephemeral ones) for `feature_dialog`
  runs — clean, only touched at FINALIZE for the commit-and-push.
- `feat_to_beads` doesn't write to git (beads go to beads_server), but
  still needs a clean checkout to read `features/NNN_<slug>.md` from a
  known-current state. Can share an ephemeral pool with `feature_dialog`
  or use its own.

Mixing a dialog/generation run into a worker's worktree is a recipe for
weird interactions with the worker's in-flight bead. Keep them separate.

### Coordination model

- All cross-worker coordination flows through `beads_server`'s atomic
  claim. No cross-worker raymond machinery is needed.
- The existing rebase loop in `work_and_agent.yaml` is correct as-is and
  carries over unchanged (modulo `bm` → `bs`).

### Pre-commitment verification

Before relying on multi-worker:

1. **Confirm `bs claim` is genuinely atomic under concurrent invocation.**
   Read the beads_server claim path. Run a deliberate two-worker spike:
   two worktrees, same beads_server, ~10 trivial beads, watch for
   double-claim.
2. **Observe rebase frequency under N workers.** Roughly N× more rebases
   than single-worker. With N=3, manageable. The push retry cap
   (`MAX_ATTEMPTS=5` in `.raymond/.push_attempts`) is reasonable per-bead;
   raise if it's hit too aggressively in practice.

## Race conditions the workflows must handle

### Feature-numbering race

Two `feature_dialog` runs finalize at the same moment, both pick the same
`NNN`, one push wins, the other rejects.

Handled by the FINALIZE state's pull-rebase/rescan-max/rewrite/commit/push
loop. Each rejection re-picks the number and renames the file. Rare with
1–3 humans but the workflow must implement it.

### Feature commit vs. bead push race

`feature_dialog` FINALIZE commits to the shared trunk branch while
workers are pushing bead commits to the same branch. The standard rebase
loop handles it; FINALIZE needs the same fetch/rebase/push machinery as
the work loop's PUSH_ATTEMPT. (The work loop reads the current branch
via `git rev-parse --abbrev-ref HEAD` and is not hardcoded to any branch
name; FINALIZE should be similarly branch-agnostic.)

**Recommendation:** factor a shared shell script (or small workflow) for
"commit-and-push with rebase retry" usable from any state that pushes —
both FINALIZE and PUSH_ATTEMPT.

### Push collisions among workers

N workers pushing to the same branch → collisions handled by the existing
PUSH_ATTEMPT → REBASE → RETEST → REWORK chain. No new design.

## Suggested implementation order

1. **Build `feature_dialog.yaml`** with `<ask>`. Reference:
   `wf_new/story_input.yaml`. Test end-to-end on one made-up feature.
   This is the riskiest piece because `bm`'s dialog behavior is least
   mechanical to translate.
2. **Build `feat_to_beads.yaml`** driven by filesystem state. Mostly a
   mechanical port of `agent.yaml`'s GENERATE branch. The existing prompts
   barely change.
3. **Swap `bm` → `bs` in the work loop** to produce `work.yaml`. Run the
   atomic-claim verification spike (two worktrees against the same
   beads_server).
4. **Decide worktree-vs-container** based on isolation needs revealed in
   step 3.
5. **Optional polish:** a small "feature index" status workflow listing
   `features/*.md` and any active runs against them, if the dashboard view
   is missed.

## Conventions decided

- **Feature filename:** `features/NNN_<kebab-case-slug>.md`. Three-digit
  zero-padded number, flat directory, no nesting.
- **Finalize signal:** two variants (mirroring `feat_dev_tg`'s
  `PROMPT.sh`; case-insensitive, whitespace-trimmed). Last non-blank
  line is `done` with content above → apply the response (less the
  `done` line), then finalize. Whole response is just `done` (or empty)
  → finalize the current draft as-is. Abandon by cancelling the run via
  the daemon API; not a workflow-level path.
- **Beads:** self-contained descriptions, with feature-doc citation as
  fallback only.
- **No persistent plan or bead-list documents.**
- **No append-to-feature with plan content.**
- **Beads in `beads_server`** stay the work-decomposition record.

## Open design questions (deferred)

- **Long-lived dialog run vs one-run-per-round.** Default: long-lived.
  Reconsider only if run recovery is flaky in practice.
- **Slug generation for the filename.** Agent generates kebab-case slug
  from the feature title at FINALIZE. Slug collisions are fine — the
  numeric prefix disambiguates.
- **Plan transcript preservation.** Skipped by default. Easy to add later
  by extending `feat_to_beads.yaml` to commit `plan.md` (and/or
  `bead-list.md`) alongside the feature file at the end of VALIDATE.
- **Daemon-per-container vs. single-daemon-many-worktrees.** Decide after
  step 3 of the implementation order.

## Reference: relevant files in the current repo

- `~/work/raymond/docs/daemon-server.md` — `raymond serve` API and UI
  surface, including pending inputs and run recovery.
- `~/work/raymond/docs/workflow-protocol.md` — `<ask>` and other
  transition tags.
- `~/work/wf_new/story_input.yaml` — minimal `<ask>` example.
- `~/work/workflow_samples/work_and_agent.yaml` — current production
  workflow (the thing being replaced).
- `~/work/workflow_samples/agent.yaml` — current dialog + generate
  branches (mechanical reference for `feature_dialog.yaml` and
  `feat_to_beads.yaml`).
- `~/work/workflow_samples/feat_dev_tg/` — existing precedent for
  filesystem-driven feature dialog (the `done`-terminated-response
  convention comes from here).
- `~/work/workflow_samples/fullspec_to_beads/` — existing precedent for
  filesystem-driven feature → beads workflow.
- `~/work/backlog_manager/` — `bm` source, retained for reference until
  the transition is complete and verified.
- `~/work/beads_server/` — beads_server source; check claim atomicity
  here before committing to multi-worker.
