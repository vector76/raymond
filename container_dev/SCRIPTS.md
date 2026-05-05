# Script Inventory

This container involves two distinct sets of scripts: **host scripts** (`.bat` files, run on Windows from the `container_dev/` directory) and **container scripts** (shell scripts, run inside the container). They never overlap — host scripts never run inside the container, container scripts are never run directly on Windows.

---

## Host scripts (Windows — run from `container_dev/`)

### `rebuild.bat`
**When:** Occasionally — when the Dockerfile changes, toolchain versions need updating, or you're setting up on a fresh machine.
**What:** Builds the Docker image using `container_dev/` as the build context (so all `COPY` paths in the Dockerfile are relative to `container_dev/`), then creates (but does not start) a fresh container with the right volume mounts, port bindings, and environment variables. Destroys the old image and container first. The `work/` volume is preserved across rebuilds.
**Does not start the container.**

### `cbash.bat`
**When:** Anytime you want a shell inside the running container for inspection, debugging, or running container scripts manually.
**What:** Starts the container if it is stopped, then opens an interactive bash session as `devuser`. **Exiting the shell does NOT stop the container** — raymond keeps running. To stop the container explicitly: `docker stop ray-dev-container`.

### `serve.bat`
**When:** Start of each working session (the normal daily-driver command).
**What:** Starts the container if stopped, then tells the container to run `start-ray.sh`. After this command returns, raymond and beads_server are running inside the container and accessible in your browser. You do not need a shell open.

---

## Container scripts (Linux — run inside the container)

These are installed to `/usr/local/bin/` by the Dockerfile and are on PATH inside the container.

### `entrypoint.sh`
**Who runs it:** Docker, automatically, as PID 1 on every container start.
**You never call this directly.**
**What:** Runs as root before handing off to `devuser`. Fixes the work directory ownership (a Windows→Linux volume mount artifact), bootstraps `~/.bashrc` if the home volume is empty, sets the timezone, writes `~/.gitconfig` with `GIT_USER_NAME`/`GIT_USER_EMAIL`, and writes `~/.git-credentials` from `GITHUB_TOKEN`/`GITHUB_USERNAME` so that git push works from any process (not just interactive shells). Then hands control to `devuser` via `gosu`.

### `setup.sh`
**Who runs it:** You, once, the first time the container is used on a new machine or after a fresh `rebuild.bat`.
**What:** Clones the raymond repository as a bare clone into `~/work/repo.git` (this is the shared git source that all worktrees branch from). Also creates the directory skeleton: `~/work/worktrees/`, `~/work/raymond/state/`, `~/work/beads/data/`. Idempotent — safe to re-run; skips the clone if the directory already exists.
**Prerequisite:** `GITHUB_TOKEN` and `GITHUB_USERNAME` must be set (they are, via `secrets.bat` → `rebuild.bat` → container env). `entrypoint.sh` wrote `~/.git-credentials` at container start.

### `build-ray.sh`
**Who runs it:** You, once after `setup.sh`, and again whenever you want to update the `ray` binary to the latest source.
**What:** Creates a `~/work/worktrees/build` worktree from the bare clone (if absent), fetches `origin/main`, resets the worktree to it, then runs `go build` to produce a fresh `ray` binary at `~/go/bin/ray`. This binary is what `start-ray.sh` and all container workflows use.
**Prerequisite:** `setup.sh` must have run first.

### `start-ray.sh`
**Who runs it:** You (directly or via `serve.bat` from the host) at the start of each working session.
**What:** 
1. Starts `beads_server` (the `bs serve` daemon) in the background if not already running, writing logs to `~/work/beads/server.log`.
2. Starts `ray serve` if not already running, writing logs to `~/work/raymond/state/ray-serve.log`.
3. Waits for the raymond HTTP API to respond.
4. For each configured worker slot (default: 1), checks the `/runs` API — if a worker run already exists from run recovery, skips it; otherwise launches a new `work.yaml` run.

Idempotent — safe to re-run while services are already up (detects and skips).
**Prerequisite:** `build-ray.sh` must have run at least once. `setup.sh` must have run.

---

## Relationship summary

```
Host                          Container
─────────────────────────     ──────────────────────────────────────────
rebuild.bat  ──────────────►  [builds image + creates container]
cbash.bat    ──────────────►  bash session (you type container scripts here)
serve.bat    ──────────────►  start-ray.sh (automated, no shell needed)

                              entrypoint.sh  (automatic, every container start)
                              setup.sh       (you run once, first time)
                              build-ray.sh   (you run after setup, and to update)
                              start-ray.sh   (you run each session, or via serve.bat)
```
