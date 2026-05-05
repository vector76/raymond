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
    excluded_tools: []                # see §4.3
    available_tools: []               # see §4.3
    usd_per_premium_request: 0.0      # see §5.2
```

Auto-approve is not a workflow-author option. Whether Copilot runs in
auto-approve mode is controlled at the raymond-invocation level by the
existing `--dangerously-skip-permissions` flag (and the corresponding
config-file setting), exactly as on the Claude backend. When that flag
is set, raymond passes `--yolo` to Copilot; otherwise it does not.
Workflows do not opt themselves into or out of `--yolo`.

### 2.2 Defaults

| Option | Default | Notes |
|---|---|---|
| `model` | unset → CLI default (`claude-sonnet-4.5` at time of writing) | Per-state `model:` frontmatter overrides. |
| `effort` | unset | Per-state `effort:` frontmatter maps to `--reasoning-effort`. |
| `agent` | unset | When unset, Copilot uses its built-in default agent. |

Copilot's `-p` mode is non-interactive — there is no terminal to prompt
on — so any tool that would normally request permission cannot get one.
The exact failure mode (silently abort vs. fail-the-state) is
implementation-defined by the CLI and has historically shifted between
versions. Running without `--yolo` is therefore likely to produce poor
or surprising outcomes for any workflow whose tools would normally
request approval, but it is fully legal: raymond does not refuse to
launch in that configuration, mirroring the Claude backend's stance
where `--dangerously-skip-permissions` is also optional. Workflow
authors and operators who want reliable execution under `-p` should
pass `--dangerously-skip-permissions` (or set it in the raymond config
file).

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
unchanged. The flag is **passed through verbatim** alongside `--model`;
when the model/effort combination is unsupported, Copilot rejects the
invocation and the resulting error surfaces as a normal state failure.
Raymond does not maintain a model→effort compatibility table, consistent
with the opaque-pass-through stance for `model` in §2.3.

## 3. Invocation mode

The backend uses **non-interactive one-shot invocations**: each state
spawns a fresh `copilot -p "<prompt>" --output-format=json …` subprocess,
streams its JSONL events, and exits when the process closes its stdout.
This mirrors the current Claude Code wrapping and reuses raymond's
streaming infrastructure (idle timeout, kill-and-drain, debug recording).

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
| `--continue-session` | **Available.** Raymond passes Copilot's `--continue` flag (resumes the most recent local session in `cwd` without forking). The intended hand-off use case — quitting an interactive Copilot session and launching raymond to continue from there — works exactly as on Claude. Subsequent `<goto>` operations continue against that session, which is the desired behavior. |
| Cost tracking | Best-effort. See §5.2. |
| Token tracking | Available, summed from `assistant.usage` events. |
| Per-state model / effort | Available via `--model` and `--reasoning-effort`. |
| `--dangerously-skip-permissions` | Optional (same as Claude). When set, raymond passes `--yolo`; otherwise it does not. See §2.2. |
| Tool disallow list | Reinterpreted (§4.3). |
| MCP servers | Operator-configured via Copilot's own MCP config; raymond is oblivious (§4.5). |
| Crash recovery / resume by run id | Available. Session IDs are persisted as today; resuming a run reattaches to the live Copilot session via `--resume`. |
| Debug stream capture | Available. Raymond records the raw JSONL stream as it does for Claude; on-disk format is the Copilot envelope shape (different from Claude's), so debug viewers must be backend-aware. |
| Lint / diagram | Identical. These are static-analysis surfaces and do not run the backend. |

### 4.1 Session identity

Copilot session events do not carry a `session_id` field in their
envelope (events form a `parentId`-linked list within a single
subprocess). Across raymond's per-state subprocesses, session continuity
is preserved by **naming the session deterministically**:

- The first invocation for an agent passes
  `--name raymond-<workflow-id>-<agent-id>`. The name is stable for the
  lifetime of the agent. Raymond's `<workflow-id>` is a per-run
  identifier (timestamp-based, generated by `state.GenerateWorkflowID`),
  so concurrent runs of the same workflow definition do not collide.
- Subsequent invocations pass `--resume raymond-<workflow-id>-<agent-id>`.
  Copilot's `--resume` accepts a session name as well as a session ID
  or task ID, so raymond does not need to extract the canonical session
  ID from the events.jsonl path.

The agent's `SessionID` field in raymond's persisted state holds this
deterministic name; the existing crash-recovery path therefore works
unchanged. Raymond uses `--continue` only at launch when the operator
passed `raymond --continue-session` (the explicit hand-off path
described in the §4 capability table); during normal state-to-state
transitions it always uses the deterministic `--resume <name>` form,
which never collides with another raymond run or an unrelated
interactive session.

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
backend translates Copilot's `assistant.usage` fields into the same
internal running total. The exact field names (assumed `inputTokens` /
`cacheReadTokens` / `cacheWriteTokens` / `outputTokens` in camelCase)
are taken from the live Copilot stream during implementation and pinned
there; this spec records the mapping intent rather than the wire names.
The summed input-side total comprises input + cache-read + cache-write.
Output tokens are tracked separately and surfaced to observers but are
**not** added to the running input-side total, mirroring how the Claude
extractor treats output tokens today.

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

### 4.4 Agent option

Copilot exposes `--agent <name>`, which selects a named agent
definition resolved by Copilot itself from its native agents directory
(the exact location, filename, and extension are Copilot's contract,
not raymond's). The backend exposes this through
`backend.options.agent: <name>` for workflow authors who want their
states to run under a particular Copilot agent definition. Raymond does
not write or mutate agent files — they are managed out-of-band.

System prompts are out of scope. Raymond does not currently expose any
system-prompt affordance to workflows on the Claude backend
(`--append-system-prompt` is not invoked anywhere in `ccwrap`, and
neither the manifest nor the per-state policy carries a system-prompt
field), so there is no parity gap to close on Copilot. If a system-prompt
feature is added to raymond in the future, the cross-backend strategy
will be designed at that time.

### 4.5 MCP servers

Raymond is oblivious to MCP at the orchestrator and backend layers in
v1. MCP servers, if any, are configured by the operator via Copilot's
normal mechanisms; raymond neither writes that config, validates its
contents, nor declares MCP requirements in workflow manifests.
Workflows that depend on a particular MCP server simply assume Copilot
is configured for it on the host; if it is not, the failure surfaces
from the agent's first attempt to use the missing tool.

### 4.6 Working directory

Each state inherits its working directory from the agent's `Cwd` field
(same as today). Copilot does not have a Codex-style `--cd` flag, so
the working directory is set on the spawned subprocess. `--add-dir` is
not used in v1; workflows that need additional read roots must place
the files inside the agent's task folder.

## 5. Unavailable or degraded features

These are the explicit gaps a workflow author must know about.

### 5.1 No session forking (`<call>` and `<call-workflow>` degrade)

Two raymond features rely on Claude's `--fork-session` to make a
callee inherit the caller's transcript:

- `<call>` (in-workflow subroutine call)
- `<call-workflow>` (cross-workflow blocking call)

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

### 5.2 Cost reporting is approximate

Copilot reports cost in two forms:

- `assistant.usage.cost` — a per-call cost value emitted on each turn.
  Treated as USD when present (this is an assumption about Copilot's
  field semantics that the implementation pins against the live stream;
  if Copilot publishes a unit other than USD, this is reduced to a
  display-only field and the premium-request count becomes the sole
  budget input).
- `session.shutdown.totalPremiumRequests` — a count of premium-request
  units consumed by the session, which is the unit Copilot bills in.
  Treated as session-cumulative; raymond computes the increment per
  state from the difference between successive `session.shutdown`
  observations.

Raymond's `BudgetUSD` accounting accumulates `assistant.usage.cost`
when present and falls back to a configurable
`backend.options.usd_per_premium_request` multiplier (default `0.0`,
i.e. unbudgeted) otherwise. The rendered total is therefore **best
effort**: if the user has not configured a multiplier and Copilot has
not emitted a per-call cost, the budget will read `$0.00` even though
real usage occurred. The dashboard surfaces both numbers (USD and
premium requests) so the user can see when they are diverging.

A workflow declared with a non-zero `default_budget` and
`backend: copilot` whose `usd_per_premium_request` is `0.0` produces a
**lint warning** that the budget *may* not be enforceable: Copilot is
not contractually required to emit `assistant.usage.cost`, and when it
does not the budget cap will never trip without a multiplier. The
warning is conservative — if `cost` is emitted, the budget accumulates
normally.

### 5.3 No nested-session protection

Claude Code respects the `CLAUDECODE` env var to prevent nested
sessions, and raymond strips it deliberately
([ccwrap.go:71-83](../internal/ccwrap/ccwrap.go)). Copilot does not
currently expose an equivalent variable, so a workflow whose agent
spawns a `copilot` subprocess from inside its tools will produce nested
Copilot sessions with no special handling. Workflow authors should
avoid having Copilot agents shell out to `copilot` themselves; the
disallow list cannot enforce this because tool names are dynamic.

### 5.4 Stream-JSON shape is not interchangeable

Debug captures, transcript dumps, and any third-party tooling that
parses raymond's recorded stream are **backend-specific**. A debug
stream file recorded on Copilot is not consumable by tools that expect
the Claude shape, and vice versa. Recorded streams therefore include
a backend tag header so consumers can route correctly.

## 6. Observability and event mapping

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

## 7. Compatibility with the rest of raymond

- **YAML scope and zip workflows.** The `backend:` field is read from
  the same manifest layer as `name`, `description`, and `input`, with
  no change to the existing parser surface.
- **Skill packaging.** Skills declare their backend in their manifest
  the same way workflows do. The daemon's skill discovery
  ([skill-packaging.md](skill-packaging.md)) lists the declared backend
  alongside other metadata; skills that require Copilot are visible as
  such in the picker.
- **Lint.** New checks: `<call>` and `<call-workflow>` degradation
  warning (§5.1), unenforceable-budget warning (§5.2).
- **Diagram.** No change; the diagram surface is backend-agnostic.
- **Daemon UI.** Run rows show a backend badge. The cost cell shows
  USD and (for Copilot) an additional "PR" (premium-request) count.
  The MCP tools surface is not affected — MCP tools exposed *by*
  raymond are independent of which backend a run uses internally.

## 8. Out of scope for v1

Captured here so future work has a record of what was deliberately
deferred:

- ACP mode (`copilot --acp`, a long-lived bidirectional protocol
  intended for SDK integrations) as an alternative to one-shot. Noted
  here only as a possible future direction; nothing in the v1 surface
  is shaped around it.
- A non-`--yolo` execution path that surfaces Copilot's permission
  prompts through raymond rather than letting tools fail under `-p`.
- Raymond-managed MCP server launching.
- Copilot autopilot / experimental modes.
- Enterprise SSO / token-refresh / non-interactive auth flows. Raymond
  relies on Copilot already being authenticated on the host (typically
  via a prior `copilot` interactive `/login`) and does not introspect,
  refresh, or supplement Copilot's credential store.
- A unified, cross-backend cost normalization model. The Copilot cost
  is reported as it is; comparisons across backends are the user's
  responsibility.
- Per-state backend switching (precluded by the multi-backend design).

## 9. Acceptance criteria

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
5. Lint produces the §7 warnings on the documented inputs.
6. The Claude backend continues to pass its existing test suite
   unchanged — i.e. introducing the Copilot backend has not regressed
   the default path.
