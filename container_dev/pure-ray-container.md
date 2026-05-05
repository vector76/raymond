# Full-takeover container model for pure-ray

Sketch of the operational shape if we commit to "one container per
project, container hosts raymond serve and the workers." Builds on
`pure-ray.md` (which covers the workflow/file-layout side); this doc
covers container topology, lifecycle, and operations.

## Premise

- **One container per project**, vs. today's multiple projects per
  container. Significant operational shift — more containers running —
  but each container becomes a self-contained "project autopilot."
- The container is always alive (CMD = `tail -f /dev/null`); raymond
  and beads_server are launched by an in-container `start-ray.sh`
  script after first-time credential setup. See "Host-side ergonomics"
  below for why.
- Raymond's web UI is exposed on a container port; humans drive
  workflows from a browser.
- Worktrees inside the container provide N concurrent workers
  (target: 1–3 workers, per `pure-ray.md`).
- **Outside development is still possible.** Humans/agents working on
  separate checkouts coordinate with the container through `git origin`
  — push to origin, container's workers fetch and rebase. The container
  is not authoritative; origin is.

## What the container is (and is not)

| Owns | Doesn't own |
|---|---|
| Long-running `raymond serve` daemon | The "real" repo (origin lives elsewhere) |
| `beads_server` instance | Outside humans' checkouts |
| Bare clone of origin (fetch target) | Other projects |
| N worktrees for workers + dialog/generation | Anything project-agnostic |
| Persistent run state, beads data, pending inputs | |

## Suggested layout inside the container

Two separate filesystem trees inside the container, with very
different lifetimes:

```
/opt/raymond/workflows/        baked into the image by the Dockerfile;
                               feature_dialog.yaml, feat_to_beads.yaml,
                               work.yaml (and any project-specific
                               variants). Refreshed on image rebuild.

/work/                         (volume-mounted, persistent across rebuilds)
├── repo.git/                  bare clone of origin (separate from any host
│                              checkout — see "Host-side ergonomics" below)
├── worktrees/                 created on demand by workflows, not entrypoint
│   │                          (each worker-N created by work.yaml's INIT,
│   │                           removed by TERMINATE on idle exit; idempotent
│   │                           across resume and crash-restart)
│   ├── worker-1/
│   ├── worker-2/
│   ├── worker-3/
│   └── specs-pool/            ephemeral checkouts for feature_dialog / feat_to_beads
│       └── (created on demand for each dialog/generation run)
├── raymond/state/             daemon state, per-run task folders, pending_inputs.jsonl
└── beads/data/                beads_server data dir
```

The split matters: workflows are source code (versioned with the
project, refreshed by image rebuild), not user data. Putting them in
`/work` would hide them behind the volume mount on subsequent
container starts, so image rebuilds wouldn't actually update them.
Keeping them in `/opt/` avoids the volume-shadows-image trap.

The image is **per-project, built from a Dockerfile in the project's
repo** — see "Per-project Dockerfile" below. The infrastructure layer
(raymond, beads_server, git, jq, shell) is the same across projects;
the project layer (language toolchain, build deps, project-specific
config) is what makes one project's image different from another's.
Runtime-mutable state (origin URL, secrets, workflow runs in flight)
still comes in via env vars and the mounted `/work` volume.

## Per-project Dockerfile

The image is built from a Dockerfile **in the project's own repo**,
specifically at `container_dev/Dockerfile` (per the Host-side
ergonomics section below). This is the right home for it:

- **Self-contained.** Cloning the project repo is enough to reproduce
  the container — no parallel knowledge of "what image template to use"
  or "how to layer raymond on top of something else." Anyone (or any
  CI) can build from a fresh clone (typically
  `docker build -f container_dev/Dockerfile .` from the project root,
  so the build context is the project root and the Dockerfile's COPY
  paths can refer to `workflows/`, `container_dev/entrypoint.sh`, etc.
  relative to that root).
- **Resilient to upstream drift.** Raymond's preferred packaging may
  shift over time (new install method, repo layout, dependencies). A
  project that pins its own build recipe doesn't break when those
  conventions change. Older projects keep building from older recipes
  until someone deliberately updates them.
- **Per-project toolchain is honest.** A Go project needs Go; a Rust
  project needs cargo + a specific rustc; a project with native deps
  needs gcc + headers. There is no plausible "generic" image that
  covers all cases without being bloated. Putting the toolchain
  decision next to the project that needs it is the natural place.

### Two flavors

**Layered.** A community-maintained or internal "raymond-base" image
provides the infrastructure layer (raymond, beads_server, git, jq,
entrypoint script, default workflows). The project's Dockerfile starts
`FROM raymond-base:<pinned-tag>` and adds the project toolchain plus
any project-specific workflow customizations.

```dockerfile
FROM raymond-base:0.7.2
RUN apt-get update && apt-get install -y golang-1.22 make
COPY workflows/ /opt/raymond/workflows/
COPY container_dev/entrypoint.sh /usr/local/bin/
COPY container_dev/init.sh        /usr/local/bin/
COPY container_dev/start-ray.sh   /usr/local/bin/
RUN chmod +x /usr/local/bin/entrypoint.sh \
             /usr/local/bin/init.sh \
             /usr/local/bin/start-ray.sh
# ENTRYPOINT inherited from raymond-base; override here if needed.
```

Pros: small project Dockerfile, faster builds (base layer cached).
Cons: implicit dependency on the base image's choices; if the base
disappears or changes incompatibly, projects break in lockstep.

**Self-contained.** Project Dockerfile builds everything from a stock
OS base, installing raymond, beads_server, and the toolchain explicitly
with pinned versions.

```dockerfile
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y git jq curl ca-certificates gosu
RUN curl -L https://github.com/.../raymond-v0.7.2-linux-amd64.tar.gz \
      | tar -xz -C /usr/local/bin
RUN curl -L https://github.com/.../bs-v1.4.0-linux-amd64.tar.gz \
      | tar -xz -C /usr/local/bin
RUN apt-get install -y golang-1.22 make
COPY container_dev/entrypoint.sh /usr/local/bin/
COPY container_dev/init.sh        /usr/local/bin/
COPY container_dev/start-ray.sh   /usr/local/bin/
COPY workflows/                   /opt/raymond/workflows/
RUN chmod +x /usr/local/bin/entrypoint.sh \
             /usr/local/bin/init.sh \
             /usr/local/bin/start-ray.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["tail", "-f", "/dev/null"]
```

Pros: maximum resilience — no base-image dependency, every binary
version pinned. Cons: longer Dockerfile, slower builds without a layer
cache, more places where pinned URLs/versions need updating.

### Recommendation

Default to the **layered** flavor for ergonomics. Switch a specific
project to **self-contained** if it needs to outlive a base-image
breaking change, or if it's the kind of project where reproducibility
matters more than build time (long-lived infrastructure, regulated
codebases, etc.). Both can coexist — different projects in the same
ecosystem can pick differently.

### Workflows in the repo

Worth noting that **workflows live in the project repo**, copied into
`/opt/raymond/workflows/` by the Dockerfile. The default workflows
from `pure-ray.md` (`feature_dialog.yaml`, `feat_to_beads.yaml`,
`work.yaml`) start as a vendored copy that the project owns and can
customize — the COMMIT state's test commands, the CLAIM state's
`git pull` flags, anything project-specific gets edited in place. To
update workflows in a running container: edit the source, rebuild
the image, recreate the container.

This resolves the "workflow distribution" open question: workflows are
first-class project artifacts, versioned with the code they drive,
delivered through the image rather than the volume.

## Host-side ergonomics: bootstrap and the `container_dev/` folder

The container's operational model: container is always alive (CMD =
`tail -f /dev/null`), services and per-project bootstrap are launched
by explicit scripts inside. This trades a small amount of automation
for a lot of debuggability, and matches the pattern in
[`vector76/agent_in_docker`](https://github.com/vector76/agent_in_docker)
— the existing precedent for our containerized agent setups, which
the patterns below borrow from directly.

### Two-checkout pattern (host clone + container clone)

The host clone of the project repo is purely a build/launch artifact:
it carries the `container_dev/` folder (Dockerfile, scripts, secrets
template) and lets the human run `docker build` and `docker run`. No
project work happens on the host clone.

The container has its own clone of origin, done by an `init.sh` script
on first run, into `/work/repo.git`. Worktrees are created from this
container-side clone. This eliminates the host filesystem from the
worker's path entirely, sidestepping CRLF, file-ownership, and
gitignore-drift issues that bite when a host bind-mount carries a
working tree the container actively git-operates on.

The two clones coexist; the host one is essentially write-once
(updated only when you edit the Dockerfile or scripts), the container
one is the live working state.

### Suggested `container_dev/` layout

```
container_dev/
├── Dockerfile               build recipe (per "Per-project Dockerfile" above)
├── entrypoint.sh            PID-1 entrypoint (perms, gosu, exec "$@")
├── secrets.bat.example      template — copy + fill, gitignored
├── .env.container.example   template — env vars passed at docker run
├── rebuild.bat              host: build image + (re)create container
├── rebuild.sh               Linux/Mac equivalent
├── cbash.bat                host: exec bash into the running container
├── cbash.sh
├── serve.bat                host: one-shot launch of raymond inside the
│                            container (`docker exec <name> /usr/local/bin/start-ray.sh`)
├── serve.sh                 Linux/Mac equivalent
├── init.sh                  container-side: first-time clone of origin into /work/repo.git
├── start-ray.sh             container-side: start beads_server + ray serve
└── README.md                bootstrap walkthrough (below)
```

The `.bat`/`.sh` host scripts wrap docker commands. `entrypoint.sh`,
`init.sh`, and `start-ray.sh` are all copied into the container by
the Dockerfile to `/usr/local/bin/` (with `chmod +x`). `entrypoint.sh`
is wired up via `ENTRYPOINT`; the others are invoked manually or by
`serve.bat`. `serve.bat` / `serve.sh` is the host-side counterpart
that lets the human launch raymond without opening a shell — it
`docker start`s the container if stopped, then `docker exec`s
`start-ray.sh` synchronously and exits, leaving raymond running
inside the container.

### First-time bootstrap walkthrough

`container_dev/README.md` walks a new contributor through this:

1. Copy `secrets.bat.example` → `secrets.bat`, fill in tokens
   (`GITHUB_TOKEN`, `ANTHROPIC_API_KEY`, etc.).
2. Run `rebuild.bat` — builds image, creates container with mounts
   and port forwards, but does not start raymond.
3. Run `cbash.bat` to enter the container.
4. *(First time only)* Run `claude login` — OAuth flow, persisted via
   the `~/.claude` mount (see "Secrets" below).
5. Run `init.sh` — clones origin into `/work/repo.git` (this is when
   github creds get exercised; if they're wrong, this is where it
   surfaces).
6. Run `start-ray.sh` — starts beads_server, then `ray serve` with
   `--autostart-config` (which fires worker runs).
7. Open `http://localhost:8080` in your browser.
8. On subsequent days: just `serve.bat` from the host — one command,
   no need to enter the container. `serve.bat` should `docker start
   <container>` first if the container is stopped, then
   `docker exec` `start-ray.sh` (mirroring how `agent_in_docker`'s
   `cbash.bat` handles start-if-stopped). (`cbash.bat` is still
   available when you want a shell for inspection or debugging.)

Six of the eight steps are one-time. Daily flow is **one command**
(`serve.bat`).

### Why CMD stays `tail -f /dev/null` (not `ray serve`)

Tempting alternative: have the container's CMD directly invoke
`ray serve` so it runs automatically on `docker start`. We don't,
because:

- **First-launch credentials don't exist yet.** Raymond would fail
  immediately, the container would exit, and the human would have to
  override CMD just to get a shell. The `tail -f /dev/null` model
  decouples "container is up" from "raymond is running."
- **Debuggability.** Stopping/restarting raymond inside an alive
  container is `^C` + `start-ray.sh`, no docker churn. Tailing logs
  is `docker exec -it ... tail -f`, no orchestration layer involved.
- **Inspecting state.** Crashed worker? Bad bead? Just `cbash` in,
  poke around the worktree, fix it. No need to keep an unrelated
  service alive while you debug.

For this to work, `start-ray.sh` must be **idempotent** — safe to
re-run when raymond is already running (detect, skip-or-restart),
and safe to run before raymond has ever been started.

### Cross-platform helper scripts

`agent_in_docker` ships `.bat` (Windows-first) and `cbash.sh` for
Linux/macOS. For `container_dev/`, ship both `.bat` and `.sh` flavors
of every host script. Or, if maintaining one set is preferable, write
everything as bash and rely on Git Bash / WSL on Windows. A small
`docker-compose.yaml` is a clean cross-platform alternative —
declarative, no scripts — but adds a learning curve for users
unfamiliar with compose.

### Important divergence from `agent_in_docker`'s `cbash`

`agent_in_docker`'s `cbash.bat` / `cbash.sh` **stops the container
when the last shell exits.** That's wrong for pure-ray: raymond is
serving and needs the container to stay alive whether or not a shell
is open. **Do NOT copy that auto-stop logic** into our `cbash.bat` /
`cbash.sh`. Exiting the shell should leave the container (and raymond)
running. Stopping the container is an explicit `docker stop` operation,
not a side effect of closing a terminal.

## Container lifecycle

The lifecycle is split: the container itself is one thing, the
services running inside (beads_server + ray serve) are another. They
start, stop, and restart independently.

### Container start (`docker start`)

`tail -f /dev/null` keeps the container alive. The entrypoint runs as
PID 1, handles permissions and `gosu devuser`, then `exec "$@"` — no
service startup, no cloning. The human (or a host-side script) drives
everything else via `cbash` + the in-container scripts.

### `init.sh` (one-time, after first `cbash`)

- If `/work/repo.git` doesn't exist: `git clone --bare $ORIGIN_URL /work/repo.git`.
- Ensure `/work/worktrees/` parent directory exists (worktrees
  themselves are created on demand by `work.yaml`'s INIT state).
- Idempotent — re-running is a no-op once the bare clone exists.

### `start-ray.sh` (every time you want raymond running)

- If `beads_server` isn't already running: launch it in background
  pointing at `/work/beads/data`. Detect via `pgrep -f beads_server`
  or its known port.
- If `ray serve` isn't already running: launch with
  `--root /opt/raymond/workflows --port 8080
  --autostart-config /opt/raymond/autostart.yaml`. Autostart fires
  `NUM_WORKERS` copies of `work.yaml` (see "Launching workflows at
  server startup" below).
- **Liveness detection:** prefer an HTTP health check
  (`curl -fs http://localhost:8080/workflows`) over `pgrep` for
  `ray serve` — it confirms the daemon is actually responding, not
  just running. `pgrep` is fine for `beads_server` (no API to poll
  for cheap).
- Idempotent — re-running while services are alive is a no-op (or a
  controlled restart, depending on how the script handles it).

**NUM_WORKERS plumbing.** Two reasonable approaches:

- *Static* — `autostart.yaml` is a hand-written file with N entries,
  baked into the image at `/opt/raymond/autostart.yaml`. `start-ray.sh`
  passes `--autostart-config /opt/raymond/autostart.yaml`. Changing
  N requires editing the file and rebuilding the image. Simplest.
- *Templated* — `start-ray.sh` reads `$NUM_WORKERS` from env, generates
  `/work/raymond/autostart.yaml` on each invocation, and passes
  `--autostart-config /work/raymond/autostart.yaml`. Note the path
  swap (`/opt` → `/work`) — the generated file goes in the volume, not
  the image. Allows changing N without rebuilding. Slightly more
  script complexity.

Default to *static* for the first cut. Switch to *templated* if
changing worker count becomes a frequent need. Whichever you pick,
keep the `start-ray.sh` `--autostart-config` path matched.

### Stopping raymond without stopping the container

Ctrl-C in the cbash session (or `kill` from another exec'd shell).
Raymond's persisted state is unaffected; relaunch via `start-ray.sh`.

### Container shutdown (`docker stop`)

Volume state preserved. Active LLM-step runs at shutdown are marked
failed per raymond's recovery semantics; `<await>`-suspended runs come
back via run recovery on next `start-ray.sh`. Autostart skips runs
that recovery already brought back (see autostart section).

### Image upgrade

`docker rm` the container, run `rebuild.bat` to recreate from the
updated image. `/work` is preserved. Workflows under
`/opt/raymond/workflows` are refreshed automatically because they
live in the image, not the volume — that's the whole point of
the `/opt` vs. `/work` split.

## Worker model: self-managed worktrees and a graceful progression

The worker workflow owns its worktree end-to-end. The entrypoint and
the daemon don't need to know anything about which worktrees exist.

### Worker shape (any pattern)

`work.yaml` becomes:

- **INIT** (shell): if `/work/worktrees/$RAYMOND_INPUT` doesn't exist,
  `git --git-dir=/work/repo.git worktree add /work/worktrees/$RAYMOND_INPUT
  "${TRUNK_BRANCH:-main}"`. Either way, `cd` into it. Idempotent so
  resumed runs and crash-restart are both safe. `TRUNK_BRANCH` is set
  in the container's environment (defaults to `main`; some projects
  use `master`, `trunk`, `develop`, etc.).
- **CLAIM → WORK → REVIEW → COMMIT → PUSH** loop as in
  `pure-ray.md`'s `work.yaml` (the existing `work_and_agent.yaml` work
  loop with `bm` → `bs`).
- **TERMINATE** (shell, only reached on idle exit — see progression
  below): `cd ..`, `git worktree remove --force "$RAYMOND_INPUT"`,
  `<result>DONE</result>`.

Workers are launched by the daemon's autostart at boot (see next
section), each with a unique `input` like `worker-1`, `worker-2`,
`worker-3`. The input doubles as the worktree name.

### Resumption and crash recovery

Whichever pattern is used, INIT must handle resumption:

- **Resume after `<await>`**: not really applicable — `work.yaml` doesn't
  await. Daemon restart while a worker is mid-LLM-step marks the run
  failed per raymond's recovery semantics. The worktree on disk persists
  with potentially dirty state and a still-claimed bead.
- **On next launch of the same worker name**, INIT finds the worktree
  exists (idempotent skip) and CLAIM checks `bs mine` for an
  already-claimed bead. The existing CLAIM logic then either resumes
  the bead from WORK or BAIL_OUTs to release it. **Verify the
  BAIL_OUT-on-recovery path is solid** — it's the difference between
  graceful pickup and a stuck worker.

### Pattern progression: A → A.1 → B → C

For 1–3 workers, **start with Pattern A** and adopt later patterns only
when the previous step's pain is real.

**Pattern A — N independent runs (start here).** Daemon autostart
launches N copies of `work.yaml`, each with a distinct `input`
(worktree name). Each worker loops `bs wait-ready --timeout 3600`
(matching the existing `work_and_agent.yaml` cadence): on bead-ready
it proceeds to CLAIM, on timeout it loops back to `wait-ready`. The
worker effectively never terminates. Cancelling a run from the UI
cancels just that worker. No supervisor.

- Pros: dead simple, no parent/child coupling, raymond UI shows N
  worker entries with individual controls.
- Cons: no elasticity. An always-looping worker costs almost nothing
  (a shell blocked in `bs wait-ready` plus the daemon's bookkeeping),
  so this rarely matters. If a worker is cancelled, no one relaunches
  it until container restart.

**Pattern A.1 — workers self-terminate on idle (add when cleanliness
matters).** Same INIT and same loop body, with one change: shorten the
`bs wait-ready` timeout (e.g., 60s) and count consecutive timeouts.
After K (e.g., 5) consecutive timeouts with no work, goto TERMINATE.
Worker cleans up its worktree and exits. Useful if you want the
container to "settle" with no stale worktrees during quiet periods.
The cost: when work returns, no one relaunches the workers — humans
(or container restart) have to.

**Pattern B — minimal counting supervisor (add when manual relaunch
gets annoying).** A `supervisor.yaml` workflow that loops:

- Poll `bs list --ready` (cheap shell state).
- If ready beads exist: query the daemon's `/runs` API for active
  `work.yaml` runs. If count < N, `<fork-workflow input="worker-K">work.yaml</fork-workflow>`
  for the next free K.
- Sleep, loop.

Workers terminate themselves on idle (Pattern A.1 behavior); supervisor
backfills as needed. The supervisor only counts — it never tracks
individual children — which keeps coupling minimal. If the supervisor
dies mid-run, workers are unaffected; relaunch the supervisor and it
picks up.

**Pattern C — full supervisor with tracked children.** Only if
genuine parent/child coordination is needed (e.g., supervisor needs to
hand specific beads to specific workers based on overlap analysis —
the "smart allocator" idea). Out of scope for the foreseeable future.

The progression is non-destructive: each step adds one capability and
doesn't invalidate the previous step's design.

## Launching workflows at server startup

**This is a recommended raymond enhancement, not a current feature.**
Today `ray serve` only exposes workflow launching via its HTTP API,
which means `start-ray.sh` would have to do something like:

```bash
ray serve --root /opt/raymond/workflows --port 8080 &
RAY_PID=$!
# Wait until the daemon is accepting requests.
until curl -s http://localhost:8080/workflows >/dev/null; do sleep 1; done
curl -X POST http://localhost:8080/runs \
     -d '{"workflow_id": "work.yaml", "input": "worker-1"}'
# ... repeat per worker, with no clean idempotency story across restarts ...
wait $RAY_PID
```

The poll fixes the obvious race but the rest is still awkward: silent
on launch failures (a `ray serve` that exits during boot just makes
the until-loop spin forever without a timeout); awkward to make
idempotent across restarts (each invocation would have to enumerate
existing runs first to avoid double-launching workers recovery already
brought back); and it pushes raymond-specific knowledge into shell
scripting that should be internal to the daemon. The container model
needs first-class support.

**Recommended shape:**

```
ray serve --autostart-config /opt/raymond/autostart.yaml
```

(`autostart.yaml` is image-baked alongside the workflows, not in the
volume — same reason as workflows: an image rebuild should refresh it.)

with `autostart.yaml`:

```yaml
- workflow: work.yaml
  input: worker-1
- workflow: work.yaml
  input: worker-2
- workflow: work.yaml
  input: worker-3
```

Behavior to specify:

- **Order:** read after run recovery, before the daemon advertises as
  ready.
- **Idempotency:** for each entry, check if a run with the same
  `workflow_id` + `input` is already alive (recovered from prior
  shutdown). If so, skip. Otherwise launch.
- **Failure tolerance:** invalid entries (workflow not found, input
  schema violation) are logged but don't prevent the daemon from
  starting. Operator wants the daemon up even if one workflow ID was
  fat-fingered.
- **Optional fields per entry:** `budget`, `model`, `working_directory`,
  `environment` — same shape as `POST /runs`.

Alternatives considered:

- **Repeated `--start workflow@input` flags.** Fine for 1–2 entries,
  awkward for several with budgets/envs. Would still want the
  config-file form.
- **`.raymond/config.toml` autostart section.** Aligns with raymond's
  existing config conventions; possibly the right home for this
  rather than a separate flag/file. Either way, the on-disk form is
  what matters; the flag is just a pointer.

Until this lands, `start-ray.sh` can do the curl-poll hack shown
above — but it should be tracked as a known wart, and the autostart
mechanism should land before going to anything resembling production.

## Networking

- **Port 8080 (or configurable):** `raymond serve` HTTP API + web UI.
  Forward to host. This is the human's only required interface.
- **`beads_server` port:** localhost-only by default. Workers reach it
  via loopback. Expose to host only if outside agents need direct bead
  visibility (probably not needed in the single-container model).
- No `bm` port — `bm` is gone.

## Secrets and credentials

The credential-handling patterns from `agent_in_docker` translate
directly — they're pre-existing, working solutions, no need to
redesign:

- **Claude OAuth.** Either `CLAUDE_CODE_OAUTH_TOKEN` env var (for
  non-interactive cases) or a persistent mount of `~/.claude` and
  `~/.claude.json` (the `CLAUDE_PERSIST_FOLDER` pattern in
  `agent_in_docker`'s `rebuild.bat`) so `claude login` only runs once.
  The walkthrough's step 4 (`claude login`) writes into the persisted
  mount; subsequent rebuilds reuse it.
- **GitHub credentials.** `GITHUB_TOKEN` + `GITHUB_USERNAME`
  env-passed; the container's `.bashrc` writes `~/.git-credentials`
  (`https://user:token@github.com`) with `credential.helper store` on
  each shell start. `gh` CLI installed for non-git GitHub ops.
- **Anthropic API key** for raymond's Claude calls. Env var
  (`ANTHROPIC_API_KEY`).
- **Build-arg gating** of optional tools (`--build-arg INSTALL_*=true`
  keyed off whether the corresponding secret is set) keeps the image
  lean — only install Amp / Cursor / Copilot / etc. tooling for the
  flavors actually configured.
- **Fixed UID 2000** for `devuser` to dodge git "dubious ownership"
  errors across rebuilds.

Project-level concern not covered by `agent_in_docker`:

- **Raymond UI auth (if exposed beyond trusted network).** Raymond's
  current daemon doesn't have built-in auth — verify what's there
  before exposing. If exposed, put it behind a reverse proxy with
  basic-auth, or restrict to a tailscale-only / VPN-only network.

## Outside-development coexistence

The container is one of several possible writers to origin. Outside
humans/agents:

- Work in their own checkouts (laptops, other containers, CI).
- Push commits to origin like normal.
- The container's workers see new commits via their existing
  fetch+rebase loop on push rejection. No special handshake needed.
- Outside humans can also hand-write `features/NNN_<slug>.md` and push
  directly. Container picks them up next time someone launches
  `feat_to_beads.yaml` (or could auto-launch via a polling workflow if
  that becomes a pain).
- Outside humans **cannot** drive `feature_dialog` from the container's
  UI without network access to it. They'd either tunnel in, run their
  own raymond locally, or do the dialog by hand.

The numbering race for `features/NNN_<slug>.md` is the same as in
`pure-ray.md` — pull-rebase, rescan, renumber, retry. Multiple writers
across multiple checkouts/containers is a normal case for the existing
mitigation.

## Where `beads_server` lives

Inside the container, by default. Reasons:

- Simpler ops: one volume, one process group, no host coupling.
- Workers reach it via loopback — no auth surface to manage.
- Outside agents don't need it: they push commits, they don't claim
  beads.

If you ever want multi-container workers (e.g., a second container on
a different machine joining the same project), one container is
designated as the beads_server host with its port exposed; others
connect over the network. That's the multi-container path described in
`pure-ray.md`'s topology section. Don't pre-build for it.

## What this gives up vs. "raymond as a generic service"

The user's concern: ditching the "raymond serves any workflow" framing
in favor of "container is dedicated to project X."

The "generic raymond" property still holds at the **infrastructure
level** — raymond itself, the entrypoint script, the default workflows
are all reusable boilerplate. What changes per project is the
toolchain (Go vs. Rust vs. ...), the project-specific workflow tweaks,
and the build recipe (the Dockerfile in the project repo). That's
honest project-specificity, not raymond pretending to be generic when
it isn't.

What you genuinely give up:

- **Multi-tenancy in one container.** Today multiple projects share one
  container; tomorrow you run N containers for N projects. More running
  processes, more memory baseline, more port allocations to manage.
  Acceptable cost for the operational clarity.
- **A single image to rule them all.** Each project builds its own
  image (per "Per-project Dockerfile" above). Build time and image
  storage scale with the number of projects. Mitigated by base-image
  layering for the common infrastructure tier.
- **Ad-hoc raymond use.** If you sometimes use raymond for one-off
  workflows unrelated to any tracked project (manual scripts, debugging
  experiments), the dedicated-container model has nowhere natural to
  put them. Either run a separate "scratch" container (with its own
  small Dockerfile) or run raymond outside any container for those
  cases.

## Open questions / things to think about

- **Raymond UI authentication.** What's currently in the daemon?
  Anything? If exposed beyond loopback, this needs a clear answer
  before going to a less-trusted network.
- **Recovery of mid-LLM-step runs.** Covered in "Resumption and crash
  recovery" above — relies on autostart relaunching the worker by name,
  INIT skipping (worktree exists), CLAIM detecting the orphaned claim
  via `bs mine`, and BAIL_OUT releasing it. Verify this end-to-end
  before relying on it; if `bs mine` semantics aren't quite right for
  the recovery case, a startup janitor workflow that BAIL_OUTs orphaned
  claims is the fallback.
- **Pruning.** Worktrees and beads accumulate. Need a periodic cleanup
  (closed beads aged out, abandoned worktrees garbage-collected). Could
  be a scheduled raymond workflow.
- **Logging / observability.** Where do raymond's per-run logs go? Are
  they in the persistent volume so a restart doesn't lose them? Out of
  scope for a sketch but worth thinking about before going to
  production.

## Implementation order

1. **Land `--autostart-config` in raymond** (or equivalent). Everything
   below leans on this; without it, `start-ray.sh` becomes a
   sleep-and-curl mess.
2. **`container_dev/` skeleton** — Dockerfile (start with the layered
   flavor), `entrypoint.sh`, `secrets.bat.example`, `rebuild.bat` /
   `rebuild.sh`, `cbash.bat` / `cbash.sh`, `serve.bat` / `serve.sh`
   (with start-if-stopped logic), stub `init.sh` and `start-ray.sh`,
   `README.md` with the bootstrap walkthrough. `agent_in_docker`'s
   `rebuild.bat` and `cbash.bat` are good starting points to copy
   from — but **omit `cbash`'s auto-stop-when-last-shell-exits
   behavior** (see Cross-platform helpers' "Important divergence"
   section).
3. **Bootstrap walkthrough verified end-to-end** — fresh clone of the
   project, follow `container_dev/README.md` step-by-step on a clean
   host, get to "browser shows raymond UI." This is the primary UX test.
4. **Verify `bs` CLI command equivalence with `bm`.** `pure-ray.md`
   and this doc assume `bs` has 1:1 equivalents for every `bm`
   command we use: `bs wait-ready`, `bs mine`, `bs claim`,
   `bs list --ready`, `bs edit --assignee ""`, `bs edit --status open`,
   `bs close`, `bs add`, `bs link`. `bm` may have added commands
   beyond what's in `beads_server`'s CLI. Read `~/work/beads_server`
   and confirm before relying on the bm→bs port; missing commands
   either need to be added to beads_server or routed through the API
   directly.
5. **`work.yaml` with self-managed worktree** — INIT creates worktree
   from `$RAYMOND_INPUT`, loop runs, no TERMINATE yet (Pattern A
   always-on).
6. **One worker end-to-end** — `start-ray.sh` autostart launches one
   `work.yaml` run; confirm INIT creates the worktree,
   CLAIM/WORK/COMMIT/PUSH cycle works against beads_server.
7. **Web UI from a browser** — launch `feature_dialog.yaml` from the
   UI, complete a dialog, verify the feature file lands in `features/`
   and is pushed.
8. **Verify `bs claim` atomicity** (the multi-worker gate from
   `pure-ray.md`'s "Pre-commitment verification" section). Read the
   beads_server claim path; run a deliberate two-worker spike (two
   worktrees, same beads_server, ~10 trivial beads, watch for
   double-claim). **Do not proceed to step 9 without this** —
   non-atomic claim is a silent corruption risk under concurrency.
9. **Scale to N workers** — autostart launches N runs with
   `worker-1`..`worker-N` inputs. Run a backlog of trivial beads,
   confirm no double-claim and that rebase resolves push collisions.
10. **Restart resilience** — kill container mid-dialog (suspended on
    `<await>`) and mid-bead (LLM step in flight). Restart. Verify the
    dialog resumes via run recovery; verify the mid-bead worker, on
    autostart relaunch, either resumes cleanly via `bs mine` or
    BAIL_OUTs cleanly.
11. **Outside-coexistence test** — push a commit from a checkout
    outside the container while a worker is mid-bead. Verify the
    worker's rebase loop handles it.
12. **(Optional) Pattern A.1: idle-terminate** — add the K-empty-polls
    threshold to TERMINATE; verify the worktree is cleaned up on idle.
13. **(Optional) Pattern B-minimal: counting supervisor** — only if
    Pattern A.1's lack of auto-relaunch becomes annoying.

## Relationship to `pure-ray.md`

`pure-ray.md` is the "what we run" document: workflows, file layout,
conventions, race conditions intrinsic to the design. This document is
the "where and how we run it" companion: container topology, lifecycle,
secrets, persistence. Read `pure-ray.md` first.

The container model does not change any decision in `pure-ray.md` — the
workflows, the `features/NNN_<slug>.md` layout, the bead
self-containedness principle, the multi-worker rebase strategy — all
identical. The container just provides a clean operational envelope
around them.
