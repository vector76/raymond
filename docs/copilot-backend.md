---
status: proposed
---

# GitHub Copilot Backend (Feature Specification)

This document describes the **GitHub Copilot backend** for raymond — a new
agent backend that lets workflow authors run states under the GitHub Copilot
CLI (`copilot`) instead of Claude Code (`claude`). It is a feature
specification: it states *what* the backend exposes, what semantics each
raymond feature has under it, and what features are unavailable or degraded.
It does **not** prescribe implementation structure (interface shape, file
layout, parser code).

For the surrounding multi-backend context, see
[multi-backend-design.md](multi-backend-design.md). For the workflow surface
the backend has to support, see [workflow-protocol.md](workflow-protocol.md).

## 1. Motivation

The Copilot CLI is the GitHub-native developer agent. Authors who already
live in the GitHub ecosystem (PR review, issues, Actions, an internal MCP
server published as a Copilot extension) will prefer it over a separately
authenticated Claude Code instance, even though the underlying model is
typically still in the Claude family. The Copilot backend gives raymond a
seat in that ecosystem without changing how workflows are authored.

The aim is **parity with Claude Code where Copilot supports it, and explicit
degradation where it does not** — never a silent semantic difference.

## 2. Selection and configuration surface

### 2.1 Workflow-level declaration

A workflow opts into the Copilot backend through the workflow manifest:

```yaml
# workflow.yaml or YAML-scope manifest header
backend: copilot
```

The selection is workflow-level, not state-level. Every state in the
workflow runs on Copilot. (This matches the rule in
[multi-backend-design.md §2](multi-backend-design.md).) A workflow that
needs heterogeneous backends composes them via cross-workflow invocation.

Structured form is permitted for backend-specific options:

```yaml
backend:
  name: copilot
  options:
    agent: my-raymond-agent           # see §4.4
    config_dir: ~/.copilot-raymond    # see §4.5
    require_auth: true                # preflight, see §6
    use_acp: false                    # see §3
    excluded_tools: []                # see §4.3
    available_tools: []               # see §4.3
    usd_per_premium_request: 0.0      # see §5.3
```

### 2.2 Defaults

| Option | Default | Notes |
|---|---|---|
| `model` | unset → CLI default (`claude-sonnet-4.5` at time of writing) | Per-state `model:` frontmatter overrides. |
| `effort` | unset | Per-state `effort:` frontmatter maps to `--reasoning-effort`. |
| `agent` | unset | When unset, Copilot uses its built-in default agent. |
| `config_dir` | unset → `~/.copilot` | Used to isolate raymond runs from a user's interactive Copilot state when desired. |
| `require_auth` | `true` | Preflight checks GH auth before launching the first state. |
| `use_acp` | `false` | When `true`, raymond drives Copilot via Agent Client Protocol instead of `-p` one-shots. v1 is `-p`-only. |
| `auto_approve` | derived from raymond's `--dangerously-skip-permissions` flag | Maps to `--yolo`. |

Copilot's `-p` mode is non-interactive — there is no terminal to prompt
on — so any tool that would normally request permission cannot get one.
The exact failure mode (silently abort vs. fail-the-state) is
implementation-defined by the CLI and has historically shifted between
versions. To avoid relying on that behavior, the backend **requires
`--dangerously-skip-permissions`** in v1, mapped to `--yolo`. Workflows
launched without it fail preflight with a clear message rather than
risking an inconsistent mid-state failure. Future versions may relax
this when ACP mode (§3) lands.

### 2.3 Model field

The `model` field is **opaque pass-through**: whatever the workflow writes
goes verbatim to `--model`. Recognized values today are `claude-sonnet-4.5`,
`claude-sonnet-4`, `gpt-5`, and any new entries Copilot exposes via
`/model`. Workflows that need to be portable across backends should keep
the field unset or wrap their states in two backend-specific workflows
joined by cross-workflow calls.

### 2.4 Effort levels

`effort` maps to `--reasoning-effort` with values `low | medium | high`.
The same per-state frontmatter that raymond already uses for Claude works
unchanged. When the chosen `model` does not support reasoning, the flag is
silently dropped before launch (Copilot CLI rejects `--reasoning-effort`
on unsupported models, so raymond gates this client-side).

## 3. Invocation mode (v1)

The v1 backend uses **non-interactive one-shot invocations**: each state
spawns a fresh `copilot -p "<prompt>" --output-format=json …` subprocess,
streams its JSONL events, and exits when the process closes its stdout.
This mirrors the current Claude Code wrapping and reuses raymond's
streaming infrastructure (idle timeout, kill-and-drain, debug recording).

**ACP mode (`copilot --acp`)** — a long-lived bidirectional protocol
intended for SDK integrations — is **out of scope for v1** but explicitly
preserved as a forward path. The backend's selection block reserves
`use_acp` so workflows do not need rewriting when ACP mode lands.

## 4. Per-feature behavior

The table below states what each existing raymond feature does on the
Copilot backend. Anything not listed behaves identically to the Claude
backend.

| Raymond feature | Behavior on Copilot |
|---|---|
| `<goto>` | Identical: same-session continuation via `--resume <SESSION_ID>`. |
| `<reset>` | Identical: discards the current session ID, starts the next state with no `--resume`. |
| `<call>` | **Degrades to `<function>` semantics on Copilot.** On Claude, `<call>` sets `ForkSessionID` ([transitions.go:446](../internal/transitions/transitions.go)) so the callee inherits the caller's transcript via `--fork-session`. Copilot cannot fork, so the callee runs with a fresh session and no transcript. The caller's session is still resumed on return. Lint warns. See §5.1. |
| `<function>` | Identical (fresh session by design on both backends). |
| `<await>` | Identical. Await semantics (suspend the agent, expose a prompt to the human, resume on input) live above the backend layer; file-bearing awaits work without change. |
| `<result>` | Identical. |
| `<fork>` (parallel agents) | Identical. `<fork>` already uses a fresh session on Claude (`SessionID: nil` in [transitions.go:485](../internal/transitions/transitions.go)); no transcript carry-over to lose. Template variables passed via `ForkAttributes` work unchanged. |
| `<call-workflow>` | **Degrades to `<function-workflow>` semantics on Copilot**, for the same reason as `<call>`. Lint warns. See §5.1. |
| `<function-workflow>` / `<fork-workflow>` / `<reset-workflow>` | Identical (these already use a fresh session by design). |
| Mixing backends across cross-workflow calls | **Allowed.** Per [multi-backend-design.md §2](multi-backend-design.md), cross-workflow invocation is the supported way to compose heterogeneous backends. Each callee runs on whatever backend its own manifest declares; sessions never cross backend boundaries. A caller's session ID is meaningless to a callee on a different backend, so any cross-backend `<call-workflow>` likewise degrades to fresh-session semantics. |
| `--continue-session` (resume the user's most recent interactive Claude session) | **Unavailable.** See §5.2. |
| Cost tracking | Best-effort. See §5.3. |
| Token tracking | Available, summed from `assistant.usage` events. |
| Per-state model / effort | Available via `--model` and `--reasoning-effort`. |
| `--dangerously-skip-permissions` | Required (§2.2). Maps to `--yolo`. |
| Tool disallow list | Reinterpreted (§4.3). |
| MCP servers | Available via Copilot's own MCP config (§4.5). |
| Crash recovery / resume by run id | Available. Session IDs are persisted as today; resuming a run reattaches to the live Copilot session via `--resume`. |
| Debug stream capture | Available. Raymond records the raw JSONL stream as it does for Claude; on-disk format is the Copilot envelope shape (different from Claude's), so debug viewers must be backend-aware. |
| Lint / diagram | Identical. These are static-analysis surfaces and do not run the backend. |

### 4.1 Session identity

Copilot session events do not carry a `session_id` field in their
envelope (events form a `parentId`-linked list within a single
subprocess). Across raymond's per-state subprocesses, session continuity
is preserved by **naming the session deterministically**:

- The first invocation for an agent passes `--name raymond-<workflow-id>-<agent-id>`.
  The name is stable for the lifetime of the agent.
- Subsequent invocations pass `--resume raymond-<workflow-id>-<agent-id>`.
  Copilot's `--resume` accepts a session name as well as a session ID
  or task ID, so raymond does not need to extract the canonical session
  ID from the events.jsonl path.

The agent's `SessionID` field in raymond's persisted state holds this
deterministic name; the existing crash-recovery path therefore works
unchanged. Raymond never uses `--continue`, which means "most recent
local session in `cwd`" — a global notion that would race between
concurrent agents and could pick up an unrelated user session.

### 4.2 Stream parsing

The Copilot envelope shape is **not Claude-shaped**. Raymond translates
the following event types into the same internal events the Claude
parser produces today, so observers (`ProgressMessage`, `ToolInvocation`,
`ErrorOccurred`) work without change:

| Copilot event | Internal event |
|---|---|
| `assistant.message` (with `content`) | `ProgressMessage` (first line of content) |
| `assistant.message` (with `toolRequests[]`) | `ToolInvocation` per request |
| `tool.execution_start` | `ToolInvocation` (when not already emitted via `toolRequests`) |
| `tool.execution_complete` (with `error`) | `ErrorOccurred` (`ToolError`) |
| `session.error` | `ErrorOccurred` (`SessionError`) |
| `assistant.usage` | accumulated for cost / token reporting (see token field mapping below) |
| `session.shutdown` | terminal marker; closes the state |

**Token field mapping.** Raymond's existing extractor sums Claude-shaped
fields (`cache_creation_input_tokens`, `cache_read_input_tokens`,
`input_tokens` — see `internal/executors/executors.go`). The Copilot
backend translates Copilot's camelCase fields into the same internal
running total: `inputTokens` + `cacheReadTokens` + `cacheWriteTokens`.
`outputTokens` is tracked separately and surfaced to observers but is
**not** added to the running input-side total, mirroring how the Claude
extractor treats output tokens today (it does not include them).

The `assistant.message_delta`, `assistant.reasoning_delta`,
`assistant.reasoning`, `tool.execution_partial_result`, and
`session.usage_info` events are **ignored for orchestration purposes**
(they are `ephemeral: true` or auxiliary), but are still recorded to the
debug stream when debug capture is on, so workflow authors can see them
post-hoc.

### 4.3 Tool disallow list

The hardcoded Claude disallow list (`EnterPlanMode`, `ExitPlanMode`,
`AskUserQuestion`, `NotebookEdit`) does not apply on Copilot — those
tool names do not exist in its surface. The intent generalizes to "do
not let the agent escape into modes raymond does not drive," and is
reinterpreted as follows for Copilot:

- **Plan / autopilot.** Copilot's `--experimental` autopilot mode is
  **never enabled** by raymond. The flag is not passed regardless of
  what the user has set in their interactive shell.
- **Ask-the-user.** Copilot has no equivalent "ask the user" tool, so
  there is nothing to gate. Future tool surfaces of this shape will be
  added to `--excluded-tools` as they appear.
- **Workflow-author overrides.** A workflow may add to the excluded
  list via `backend.options.excluded_tools: [shell(npm publish), …]` and
  to the allow list via `backend.options.available_tools`. These map
  directly to `--excluded-tools` and `--available-tools`.

### 4.4 Agent files (system prompt analogue)

Raymond does not currently use system prompts. Copilot does not expose
`--append-system-prompt`; instead it has `--agent <name>`, which selects
a named agent definition resolved by Copilot itself from its agents
directory (under the active `config_dir`; the exact filename and
extension are Copilot's contract, not raymond's). The backend exposes
this through `backend.options.agent: <name>`. Raymond does not write or
mutate agent files for the user — they are managed out-of-band.

If a future raymond feature wants to inject orchestration-level
instructions as a system prompt, the chosen strategy on Copilot is **(a)
prepend into the user prompt** for v1 (per
[multi-backend-design.md §1](multi-backend-design.md)). Writing into
agent files is rejected because it mutates user configuration.

### 4.5 MCP servers

Copilot's MCP configuration lives under `<config_dir>/mcp-config.json`
(default `~/.copilot/mcp-config.json`). The backend takes one of three
positions on MCP:

- **Default:** raymond does not write MCP config. Operators wire up
  their MCP servers via Copilot's normal mechanisms, and workflows
  declare an MCP requirement in metadata. Raymond preflight verifies
  declared servers are present in the config and fails fast if they
  are not.
- **Optional `config_dir` isolation:** when `backend.options.config_dir`
  is set, raymond uses that directory for the run. This lets operators
  keep a raymond-specific MCP / agent / settings tree separate from
  their interactive Copilot setup, by pointing `config_dir` at a tree
  they pre-populate.
- **No raymond-managed MCP servers in v1.** The "raymond launches the
  MCP server itself and points the backend at it" path described in
  [multi-backend-design.md §6](multi-backend-design.md) is deferred.

### 4.6 Working directory

Each state inherits its working directory from the agent's `Cwd` field
(same as today). Copilot does not have a Codex-style `--cd` flag, so
the working directory is set on the spawned subprocess. `--add-dir` is
not used in v1; workflows that need additional read roots must place
the files inside the agent's task folder.

## 5. Unavailable or degraded features

These are the explicit gaps a workflow author must know about.

### 5.1 No session forking (`<call>` and `<call-workflow>` degrade)

Three raymond features rely on Claude's `--fork-session` to make a
callee inherit the caller's transcript:

- `<call>` (in-workflow subroutine call)
- `<call-workflow>` (cross-workflow blocking call)
- `--continue-session` at launch (covered separately in §5.2 because
  its failure mode is a launch-time error, not a runtime degradation)

**Copilot has no fork-session equivalent.** On Copilot, `<call>` and
`<call-workflow>` therefore degrade to their non-forking siblings —
`<function>` and `<function-workflow>` respectively — at the executor
level: the callee launches with a fresh session and no transcript. The
return-stack mechanics are unchanged: when the callee finishes, the
caller's session is resumed exactly as on Claude.

Mitigation paths for workflow authors:

- Pass shared context through call attributes (`input="…"` is already a
  first-class raymond feature; on Copilot it is the only context channel).
- Have the caller write context to a file in the task folder; the
  callee reads it as part of its prompt.
- For workflows where transcript carry-over is essential, the backend
  is **not the right fit**; switch to Claude or compose with a Claude
  sub-workflow via cross-workflow call.

Lint: a `<call>` or `<call-workflow>` in a workflow declared
`backend: copilot` produces a **warning** (not an error) pointing to
this section. Note that `<fork>` is **not** affected — it already uses
a fresh session on Claude.

### 5.2 No `--continue-session` at launch

`raymond --continue-session` (which uses Claude's `-c --fork-session`
to pick up the user's most recent interactive Claude session and fork
it for the workflow) has **no counterpart on Copilot**. Copilot's
`--continue` resumes the most recent local session in `cwd` but does
not fork it, so any subsequent `<goto>` would mutate the user's
interactive session — unacceptable.

Behavior on Copilot: invoking raymond with `--continue-session` against
a workflow declared `backend: copilot` is a **launch-time error** with
a message that names this section.

### 5.3 Cost reporting is approximate

Copilot reports cost in two forms:

- `assistant.usage.cost` — a per-call cost value emitted on each turn.
  Treated as USD when present.
- `session.shutdown.totalPremiumRequests` — a count of premium-request
  units consumed by the session, which is the unit Copilot bills in.

Raymond's `BudgetUSD` accounting accumulates `assistant.usage.cost`
when present and falls back to a configurable
`backend.options.usd_per_premium_request` multiplier (default `0.0`,
i.e. unbudgeted) otherwise. The rendered total is therefore **best
effort**: if the user has not configured a multiplier and Copilot has
not emitted a per-call cost, the budget will read `$0.00` even though
real usage occurred. The dashboard surfaces both numbers (USD and
premium requests) so the user can see when they are diverging.

A workflow declared with a non-zero `default_budget` and
`backend: copilot` whose multiplier is `0.0` produces a **lint warning**
that the budget will not be enforceable.

### 5.4 No `--append-system-prompt` flag

Already covered in §4.4. The forward-looking system-prompt feature, if
it lands, will use the prepend-into-user-prompt strategy on Copilot.

### 5.5 No nested-session protection

Claude Code respects the `CLAUDECODE` env var to prevent nested
sessions, and raymond strips it deliberately
([ccwrap.go:71-83](../internal/ccwrap/ccwrap.go)). Copilot does not
currently expose an equivalent variable, so a workflow whose agent
spawns a `copilot` subprocess from inside its tools will produce nested
Copilot sessions with no special handling. Workflow authors should
avoid having Copilot agents shell out to `copilot` themselves; the
disallow list cannot enforce this because tool names are dynamic.

### 5.6 Stream-JSON shape is not interchangeable

Debug captures, transcript dumps, and any third-party tooling that
parses raymond's recorded stream are **backend-specific**. A debug
stream file recorded on Copilot is not consumable by tools that expect
the Claude shape, and vice versa. Recorded streams therefore include
a backend tag header so consumers can route correctly.

## 6. Preflight and authentication

When the first state of a Copilot-backed workflow is about to launch,
raymond performs the following checks. Each failure produces a clear,
actionable error before the agent process is spawned:

- **§6.1 Binary present.** `copilot` is on `PATH` (or the configured
  override path). Failure → "GitHub Copilot CLI not found; install
  per …".
- **§6.2 Auth available.** One of:
  - `GH_TOKEN` or `GITHUB_TOKEN` is set with a token that includes the
    Copilot scope, or
  - A prior `/login` has populated the configured `config_dir` with a
    usable credential.

  Raymond does not interactively log the user in. Failure → "GitHub
  Copilot is not authenticated. Run `copilot` interactively and
  `/login`, or set GH_TOKEN."
- **§6.3 Auto-approve compatibility.** If the workflow does not have
  `--dangerously-skip-permissions`, fail per §2.2.
- **§6.4 `--continue-session` rejection.** If the workflow was launched
  with `--continue-session`, fail per §5.2.
- **§6.5 MCP requirements.** Any MCP server declared in workflow
  metadata is present in Copilot's MCP configuration under the active
  `config_dir`.
- **§6.6 Agent file presence.** If `backend.options.agent: <name>` is
  set, the corresponding agent definition resolves under the active
  `config_dir`'s agents directory.

Preflight runs at workflow start, not per state — once per run.

## 7. Observability and event mapping

Beyond the event-mapping table in §4.2, the backend surfaces two
Copilot-specific signals to the existing observers:

- **Premium-request count.** A new field on the per-state cost summary
  records the increment in `totalPremiumRequests` since the prior
  state's `session.shutdown`. The dashboard renders this alongside the
  USD figure.
- **Tool name namespace.** Copilot tools (e.g. `shell`, `write`,
  `edit`, MCP-namespaced tools like `github.create_pr`) flow through
  `ToolInvocation` events as-is. The `Detail` extractor in
  [markdown.go:563-580](../internal/executors/markdown.go) is extended
  with a Copilot-aware branch (file path for `write`/`edit`, command
  for `shell`).

## 8. Compatibility with the rest of raymond

- **YAML scope and zip workflows.** The `backend:` field is read from
  the same manifest layer as `name`, `description`, and `input`, with
  no change to the existing parser surface.
- **Skill packaging.** Skills declare their backend in their manifest
  the same way workflows do. The daemon's skill discovery
  ([skill-packaging.md](skill-packaging.md)) lists the declared backend
  alongside other metadata; skills that require Copilot are visible as
  such in the picker.
- **Lint.** New checks: `<call>` and `<call-workflow>` degradation
  warning (§5.1), `--continue-session` launch-time error (§5.2),
  unenforceable-budget warning (§5.3), declared-but-missing MCP server
  error (§6.5).
- **Diagram.** No change; the diagram surface is backend-agnostic.
- **Daemon UI.** Run rows show a backend badge. The cost cell shows
  USD and (for Copilot) an additional "PR" (premium-request) count.
  The MCP tools surface is not affected — MCP tools exposed *by*
  raymond are independent of which backend a run uses internally.

## 9. Out of scope for v1

Captured here so future work has a record of what was deliberately
deferred:

- ACP mode (`copilot --acp`) as an alternative to one-shot.
- Auto-approve semantics that work without `--yolo` (i.e. interactive
  permission-prompt-on-stderr).
- Raymond-managed MCP server launching.
- Copilot autopilot / experimental modes.
- Enterprise SSO / token-refresh flows beyond honoring `GH_TOKEN`.
- A unified, cross-backend cost normalization model. The Copilot cost
  is reported as it is; comparisons across backends are the user's
  responsibility.
- Per-state backend switching (precluded by the multi-backend design).

## 10. Acceptance criteria

The Copilot backend is considered complete when:

1. A workflow declaring `backend: copilot` runs end-to-end with the
   same `<goto>`, `<reset>`, `<call>`, `<function>`, `<await>`,
   `<result>` semantics as the same workflow on Claude — verified by
   parallel test workflows in `workflows/test_cases/backends/`.
2. `<call>` runs and the lint warning fires; the callee receives a
   fresh session and the caller's session is correctly resumed on
   return, as documented in §5.1.
3. Crash-resume (`raymond --resume <run-id>`) reattaches to the
   correct Copilot session.
4. The dashboard displays the run with backend-correct cost, tool
   names, and progress messages.
5. Preflight produces clear errors for each of the §6 failure modes,
   with no false positives in the happy path.
6. Lint produces the §8 warnings/errors on the documented inputs.
7. The Claude backend continues to pass its existing test suite
   unchanged — i.e. introducing the Copilot backend has not regressed
   the default path.
