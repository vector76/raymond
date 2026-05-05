# Pure-Ray Container — Implementation Plan

## Background

This container serves two simultaneous roles that are easy to confuse:

- **Role A — Developing raymond:** The container has the Go toolchain and builds the `ray` binary from source. Raymond itself is the *subject* of development here.
- **Role B — Using raymond:** The container runs `raymond serve` as a long-lived daemon. Raymond workflows orchestrate the actual development work (feature dialog, bead creation, coding, review, commit, push).

The goal is to replace the `backlog_manager` (`bm`) + `beads_server` + `work_and_agent.yaml` stack with a pure-raymond equivalent, as described in `pure-ray.md` and `pure-ray-container.md`.

---

## Stage 1 — Container skeleton `[COMPLETE]`

**Goal:** All container infrastructure files are in place and the image builds successfully.

**What was done:**
- `Dockerfile` — rewrote from scratch: Ubuntu 26.04 base (Go 1.26 from default repos, no PPA); dropped Tauri/Rust/OCCT/Node; added `bs` (beads_server CLI) built from source; bootstrap `ray` binary via `go install`; copied container scripts. Build context is `container_dev/` itself — all COPY paths are relative to it. Workflows will live in `container_dev/workflows/`, making `container_dev/` fully self-contained.
- `entrypoint.sh` — extracted from inline Dockerfile heredoc into a proper file; handles Docker-level setup (ownership fix, git credentials, gosu handoff)
- `setup.sh` — new: first-time repo clone and directory layout
- `build-ray.sh` — new: builds ray binary from the source worktree
- `start-ray.sh` — new: starts beads_server + raymond, launches workers idempotently
- `serve.bat` — new: host-side one-command session start
- `cbash.bat` — fixed: removed auto-stop-on-last-shell-exit (raymond must keep running when shell closes)
- `rebuild.bat` — updated: build context is now project root (`..`), port vars renamed to `RAYMOND_PORT`/`BEADS_PORT`, `ORIGIN_URL` added
- `.env.container` / `.env.container.example` — updated: ports 7100 (raymond) and 7101 (beads), `ORIGIN_URL` field

**You verify:** `rebuild.bat` runs without error. `docker images | findstr ray-dev` shows the image.

---

## Stage 2 — First-time bootstrap

**Goal:** The container is running, Claude is authenticated, and the raymond repo is cloned inside the container.

**You do (in order):**

1. Run `rebuild.bat` from `container_dev/`.
2. Run `cbash.bat` to open a shell.
3. Check Claude auth: `claude auth status`. If not logged in: `claude auth login`.
4. Run `setup.sh` to clone the repo and create directory layout.
5. Verify: `git --git-dir=$HOME/work/repo.git log --oneline -3` — should show raymond commits. (`--git-dir` does not expand `~`; use `$HOME`.)
6. Exit the shell. Run `docker ps` from Windows — **container must still be running** (verifies the cbash auto-stop fix).

**Known issue to watch for:** `setup.sh` needs `ORIGIN_URL` and working GitHub credentials. Both come from `secrets.bat` → `rebuild.bat` → container env vars. If git clone fails, the error message will point to the credential issue.

---

## Stage 3 — Build raymond from source

**Goal:** The `ray` binary inside the container is built from the current source, not the bootstrap install.

**You do:**

1. Inside the container: `build-ray.sh`
2. Verify: `ray --help` prints usage.

**Note:** `build-ray.sh` creates a `~/work/worktrees/build` worktree, fetches `origin/main`, and runs `go build`. Re-run this any time you want to pick up source changes (e.g., after adding `--autostart-config` to raymond).

---

## Stage 4 — Verify `bs` CLI commands `[COMPLETE]`

**Goal:** Confirm `beads_server` has the subcommands that `work.yaml` will need.

**Findings:**

All required commands confirmed present. Additional useful commands also available: `edit`, `comment`, `move`, `reopen`, `search`, `show`, `unlink`, `whoami`.

| Command | Status | Notes |
|---|---|---|
| `bs serve` | ✓ | Flags: `--data-file` (not `--data`), `--port`, `--token` |
| `bs claim` | ✓ | |
| `bs wait-ready` | ✓ | |
| `bs mine` | ✓ | |
| `bs close` | ✓ | |
| `bs add` | ✓ | |
| `bs link` | ✓ | |
| `bs list` | ✓ | |

**Key details:**
- Default port is 9999; we override to `$BEADS_PORT` (7101)
- Auth: static bearer token set via `--token` on server and `BS_TOKEN` env var on clients
- Data stored in a single JSON file; path set via `--data-file` (default `beads.json`)
- `BS_URL` env var controls which server clients connect to (default `http://localhost:9999`)
- `BS_TOKEN` added to `secrets.bat` and passed to container via `rebuild.bat`
- `start-ray.sh` updated with correct flags; sets `BS_URL` before starting raymond so workflows inherit it

---

## Stage 5 — First raymond startup

**Goal:** Raymond is running and the web UI is accessible in a browser.

**You do:**

1. Add `BS_TOKEN` to `secrets.bat`, run `rebuild.bat`, then `cbash.bat`.
2. Inside the container: `start-ray.sh` — or from the host: `serve.bat`.
3. Open `http://localhost:7100` in a browser.

**Verify:** Raymond web UI loads. `curl http://localhost:7100/workflows` returns JSON.
Logs at `~/work/raymond/state/ray-serve.log` and `~/work/beads/server.log` if anything fails.

`work.yaml` does not exist yet, so `start-ray.sh` will say "No work.yaml — skipping worker launch." That's expected.

---

## Stage 6 — Port `work.yaml` (bm → bs)

**Goal:** A single worker can claim a bead, do work, commit, push, and close — the full loop.

**I do:** Write `container_dev/workflows/work.yaml` — a port of the existing `work_and_agent.yaml` work loop with:
- `bm` calls replaced by `bs` equivalents
- Worktree self-management: INIT creates `~/work/worktrees/$RAYMOND_INPUT` from the bare clone; TERMINATE removes it on idle exit (Pattern A from the design doc — workers don't self-terminate yet, just loop on `bs wait-ready`)

**You do:**
1. Image rebuild (new COPY for workflows/).
2. Create one test bead: `bs add "test: print hello world"`.
3. Run `start-ray.sh` — worker launches, claims the bead, works, commits, pushes.

**Verify:** `bs list` shows the bead as closed. A commit appears on the remote.

---

## Stage 7 — `feature_dialog.yaml`

**Goal:** A human can start a feature dialog from the raymond UI, go through one or more `<await>` rounds, type `done`, and see a `features/001_<slug>.md` file committed and pushed.

**I do:** Write `container_dev/workflows/feature_dialog.yaml` following the ANALYZE → (multi-round `<await>`) → FINALIZE structure from `pure-ray.md`.

**You do:** Launch from UI, go through two dialog rounds, type `done`.

**Verify:** `git --git-dir=$HOME/work/repo.git log --oneline -3` shows a new commit with a `features/` file.

---

## Stage 8 — `feat_to_beads.yaml`

**Goal:** Given a finished feature file, the workflow creates the corresponding beads in beads_server.

**I do:** Write `container_dev/workflows/feat_to_beads.yaml` — port of `agent.yaml`'s GENERATE branch, reading from `features/NNN_<slug>.md`, writing beads via `bs add`/`bs link`.

**You do:** Run against the feature file from Stage 7.

**Verify:** `bs list` shows new beads with descriptions citing the feature file.

---

## Stage 9 — Restart resilience

**Goal:** Stopping and restarting the container does not create duplicate workers or leave stale claimed beads.

**You do:**
1. Start a worker with a bead in progress.
2. `docker stop ray-dev-container`.
3. `serve.bat` to restart.
4. Check raymond UI — worker should either resume or BAIL_OUT cleanly.

**Verify:** No duplicate workers in the UI. Bead is either being worked or is back in ready state.

---

## Stage 10 — Multi-worker + claim atomicity

**Goal:** Two concurrent workers do not double-claim the same bead.

**I do:** Bump `NUM_WORKERS` default in `start-ray.sh` to 2 (or add it to `.env.container`).

**You do:**
1. Create ~10 trivial beads.
2. `serve.bat` — both workers start.
3. Watch until all beads are closed.

**Verify:** Each bead closed exactly once. No bead touched by two workers. (`bs list` or the beads UI shows clean history.)

**Do not proceed past this stage without a clean result** — non-atomic claim is a silent corruption risk under concurrency.

---

## Deferred (not in scope until the above is solid)

- `feature_dialog.yaml` push-race handling (two dialogs finalizing simultaneously) — rare; the pull-rebase loop handles it but is not stress-tested
- Pattern A.1 (idle-terminate workers) and Pattern B (counting supervisor) — add only when Pattern A's "workers loop forever" becomes operationally annoying
- `ray serve --autostart-config` — the `start-ray.sh` shell workaround is fine for now; add this to raymond as a feature ticket when the rest is stable
- Pruning: closed beads aging out, abandoned worktrees garbage-collected — a scheduled raymond workflow, deferred until the basic loop works
