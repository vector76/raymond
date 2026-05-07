# Pure-Ray Container — Implementation Plan

## Background

This container serves two simultaneous roles that are easy to confuse:

- **Role A — Developing raymond:** The container has the Go toolchain and builds the `ray` binary from source. Raymond itself is the *subject* of development here.
- **Role B — Using raymond:** The container runs `raymond serve` as a long-lived daemon. Raymond workflows orchestrate the actual development work (feature dialog, bead creation, coding, review, commit, push).

The goal is to replace the `backlog_manager` (`bm`) + `beads_server` + `work_and_agent.yaml` stack with a pure-raymond equivalent.

## Design source of truth

The current workflow strategy is `workflows/approach.md`. Per-workflow detail in `workflows/work.pseudo.md`, `feature_dialog.pseudo.md`, `feat_to_beads.pseudo.md`, `launcher.pseudo.md`. **Read approach.md before acting on stages 6+ below.** Earlier sessions used different models (worktrees, autostart-launcher, env-var-on-launch identity) that have since been superseded.

The broader design rationale (older but still useful) is in `pure-ray.md` (workflow design) and `pure-ray-container.md` (container topology).

---

## Stage 1 — Container skeleton `[COMPLETE]`

**Goal:** All container infrastructure files in place; image builds successfully.

**Done:**
- `Dockerfile` rewritten from scratch: Ubuntu 26.04 base (Go 1.26), dropped Tauri/Rust/OCCT/Node, added `bs` (beads_server CLI) built from source, bootstrap `ray` via `go install`. Build context is `container_dev/` itself; COPY paths are bare.
- `entrypoint.sh` extracted from inline Dockerfile heredoc; handles ownership, git creds, gosu handoff.
- `setup.sh` — first-time clone of origin into `/work/repo.git` and dir layout (worktree-era; will need rework, see Stage 11 below).
- `build-ray.sh` — builds ray binary from source.
- `start-ray.sh` — starts beads_server + raymond. (Worker-launch loop currently disabled by TODO comment; see Stage 11.)
- `serve.bat` — host-side one-command session start.
- `cbash.bat` — fixed: removed auto-stop-on-last-shell-exit (raymond must keep running).
- `rebuild.bat` — build context is `container_dev/`; ports renamed `RAYMOND_PORT`/`BEADS_PORT`; `ORIGIN_URL` added.
- `.env.container` / `.env.container.example` — ports 7100 (raymond) and 7101 (beads), `ORIGIN_URL` field.

---

## Stage 2 — First-time bootstrap `[COMPLETE]`

**Goal:** Container running, Claude authenticated, raymond repo cloned inside.

**Done:** rebuild.bat → cbash.bat → claude auth → setup.sh → verified clone.

---

## Stage 3 — Build raymond from source `[COMPLETE]`

**Goal:** `ray` binary built from current source, not bootstrap install.

**Done:** `build-ray.sh` produces a working `ray --help`.

---

## Stage 4 — Verify `bs` CLI commands `[COMPLETE]`

**Goal:** Confirm `beads_server` has the subcommands work.yaml needs.

**Done:** `bs serve`, `claim`, `wait-ready`, `mine`, `close`, `add`, `link`, `list` all confirmed. Auth via `--token` + `BS_TOKEN` env var. Default port 9999, overridden to `$BEADS_PORT` (7101). Data file via `--data-file`.

---

## Stage 5 — First raymond startup `[COMPLETE]`

**Goal:** Raymond running, web UI accessible.

**Done:** `start-ray.sh` brings up beads_server + raymond. UI at `http://localhost:7100`. Logs at `~/work/raymond/state/ray-serve.log` and `~/work/beads/server.log`.

---

## Stage 6 — `work.yaml` v1 (manual launch, self-bootstrap, single worker)

**Goal:** A single manually-launched `work.yaml` run can claim a bead, do work, commit, push, and close — the full loop. Worker self-bootstraps its folder, `.env`, and clone with no external setup.

**I do:** Convert `workflows/work.pseudo.md` to `workflows/work.yaml` per the current design:
- START state does idempotent `mkdir`, `cd`, write-`.env`, `git clone`-if-missing, `.git/info/exclude` for `.env`.
- Every shell step starts with `cd "$HOME/work/workers/$RAYMOND_INPUT"`.
- Plain `git pull --rebase`, `git rebase origin/main`, `git push origin main` — no detached HEAD, no `HEAD:main` notation.
- States: START → WAIT_READY → CLAIM → WORK → REVIEW → DID_FIX/AGAIN_CHOICE → COMMIT → PUSH_ATTEMPT → (REBASE/RESOLVE/RETEST/REWORK on rejection) → CLOSE_TASK → DONE → loop. BAIL_OUT releases the bead.

**Pre-yaml verifications** (impl-detail items in approach.md): confirm `bs` reads `.env` from CWD; spot-check `bs` flag specifics; decide canonical-vs-direct-clone source.

**You do:**
1. Image rebuild (new `workflows/work.yaml` in COPY).
2. Create one test bead: `bs add "test: print hello world"`.
3. From raymond UI, launch `work.yaml` with input `worker-1`. (No `--launch`, no autostart.)
4. Watch the run: claim → work → commit → push → close.

**Verify:** `bs list` shows the bead as closed; commit appears on origin; `~/work/workers/worker-1/` exists with `.env` and `.git/`.

---

## Stage 7 — Multi-worker (still manual launch)

**Goal:** Two workers in distinct folders, claiming distinct beads, no double-claim.

**You do:**
1. Create 5–10 trivial beads.
2. From raymond UI, launch a second `work.yaml` run with input `worker-2`. (Worker-1 from Stage 6 still running.)
3. Watch both workers process beads concurrently.

**Verify:** Each bead closed exactly once; `bs claim` atomicity holds; no bead touched by two workers.

**Do not proceed if:** any double-claim happens. Atomicity is load-bearing for everything past this point.

---

## Stage 8 — `feature_dialog.yaml`

**Goal:** Human launches `feature_dialog.yaml` from the UI, completes a multi-round dialog ending in `done`, sees `features/NNN_<slug>.md` committed and pushed.

**I do:**
1. Convert `feature_dialog.pseudo.md` from worktree to clone model (mechanical pass): `git worktree add --detach` → `git clone`, drop `HEAD:main` for `main`, align paths to `~/work/specs-pool/<run_id>/`.
2. Resolve "where does per-run scratch state live?" (impl-detail #1 in approach.md).
3. Convert pseudocode to `workflows/feature_dialog.yaml`.

**You do:** Launch from UI with a seed description; go through 1–2 dialog rounds; type `done`.

**Verify:** A new commit appears with `features/NNN_<slug>.md`; ephemeral specs-pool clone is removed at TERMINATE.

---

## Stage 9 — `feat_to_beads.yaml`

**Goal:** Given a finalized feature file, the workflow creates the corresponding beads.

**I do:**
1. Convert `feat_to_beads.pseudo.md` from worktree to clone model.
2. Convert pseudocode to `workflows/feat_to_beads.yaml`.

**You do:** Launch from UI against the feature file from Stage 8.

**Verify:** `bs list` shows new beads with descriptions citing the feature file; ephemeral clone cleaned up; no git writes happened.

---

## Stage 10 — Restart resilience

**Goal:** Container stop/start preserves in-flight work; no double-claims, no orphaned beads.

**You do:**
1. Start a worker with a bead in progress (mid-WORK).
2. `docker stop`, then `serve.bat` to restart.
3. Either let raymond's recovery bring the worker back, or manually relaunch with the same `worker-N` input from the UI.

**Verify:** Worker's `bs mine` returns the previously-claimed bead; CLAIM resumes via that bead; partial work on disk continues from where it stopped, OR BAIL_OUT cleanly releases. No double-claim.

---

## Stage 11 — Simplify `start-ray.sh` and surrounding scripts

**Goal:** Strip the curl-POST autostart worker-launch loop now that the v1 model is manual launch from UI. Decide whether `setup.sh` should retain the bare-repo creation step (likely no — move to a `canonical/` clone instead, optional and lazy-created).

**I do:**
- Edit `start-ray.sh` — remove the worker-launch loop (currently TODO-flagged); keep beads_server + raymond startup.
- Edit `setup.sh` — replace `git clone --bare` with optional `git clone <ORIGIN_URL> ~/work/canonical` or remove entirely (workers self-bootstrap from `$ORIGIN_URL` if no canonical).

**Verify:** Container restart leaves raymond and beads_server running; no workers auto-launch; UI is ready for manual launches.

---

## Deferred (not in v1)

- **`launcher.yaml`** — automated worker fan-out via `--launch` flag. Pseudocode preserved at `workflows/launcher.pseudo.md`. Add when v1 manual-launch loop is rock-solid and the friction of clicking-to-launch matters. Workers don't change between v1 and v2 — only the launch surface does.
- **`feat_poller.yaml`** — automated `feat_to_beads.yaml` triggering on new feature files. Defer until explicit feature lifecycle metadata exists; bead-text-search is brittle.
- **Feature lifecycle metadata** — explicit roadmap / frontmatter / tags to track feature completion. Add when by-inspection tracking gets noisy.
- **Feature queueing** — "feature N+1's beads only generate after feature N's beads are all closed." Falls out of poller + lifecycle metadata together.
- **Pattern A.1 (idle-terminate workers).** If stale workers in quiet periods become annoying.
- **Pattern B (counting supervisor).** If A.1's manual-relaunch becomes annoying.
- **Plan / bead-list transcript preservation.** Currently discarded by feat_to_beads; commit alongside feature file if missed.
- **Multi-container topology.** Single container for foreseeable future.
- **Pruning** — closed beads aged out, abandoned worker folders garbage-collected. Scheduled raymond workflow.
