# pi Backend

**Status:** Implemented. This document specifies the pi backend for raymond
and records the rationale for adding it. **pi is the sole planned non-Claude
backend** — codex, gemini, cursor, and copilot are postponed indefinitely;
the reasoning is below under "Why pi, and only pi."

For background on the existing Claude Code integration that this feature
parallels, see [code-structure.md](code-structure.md) and "Backend
abstraction (where pi plugs in)" below.

## Why pi, and only pi

Two strategic principles drive the choice — coverage and stability — and a
third practical observation (mechanical fit with raymond's existing surface)
seals it.

### Coverage: Claude + pi reaches every provider on favorable terms

Raymond's existing Claude Code path uses subsidized Anthropic tokens via a
Claude Code subscription — the discounted rate that makes Claude usage
economical for long-running workflows. Pi covers the rest of the provider
landscape on equivalently favorable terms:

- **OpenAI** — pi can drive an OpenAI Codex subscription, getting the same
  discounted-token economics that Codex CLI users enjoy.
- **Microsoft / enterprise** — pi can drive a GitHub Copilot subscription,
  which is the path enterprises typically have budget and approval for.
- **Google and others** — pi's multi-provider selector
  (`--provider <name>`, `--model <pattern>`, `--thinking <level>`) handles
  the remaining providers via their own credentials.

Notably, pi driving Claude does *not* use the Claude Code subscription —
it falls through to retail Anthropic API pricing ("extra usage"). That's
why raymond keeps its own direct Claude Code integration rather than
routing Claude through pi: each backend is responsible for the provider
it serves cheapest.

Between Claude Code (for Anthropic, subsidized) and pi (for everyone
else, subsidized where the provider offers it), the practical provider
matrix is covered. Adding codex / gemini / cursor / copilot as
first-class backends would mostly duplicate routes pi already covers, at
the cost of more integration surface to maintain.

### Stability: pi is a thin foundation, not an opinionated harness

Model behavior will always drift — that's an unavoidable cost of building
on LLMs. What raymond *can* avoid is a *second* layer of drift on top:
the layer introduced by an opinionated harness sitting between raymond
and the model.

Claude Code, Codex CLI, Gemini CLI, Cursor, and Copilot CLI all have
their own opinions about prompt shaping, permission flows, tool
surfaces, and stream-event semantics. Those opinions change. A raymond
workflow that works today against one of those harnesses can fail
tomorrow when the harness changes its mind about, say, how `<tool_use>`
blocks are emitted or which tools auto-approve.

Pi is deliberately bare-bones-but-extensible. Its protocol surface
(`--mode json`, `--mode rpc`) is small, its prompt handling is
unopinionated, and its philosophy is to expose the provider's behavior
rather than reshape it. That makes pi a more stable foundation for
raymond to build on. Raymond still inherits model drift via pi, but it
does not inherit a second moving target on top of that.

The historical risk is real: GitHub Copilot CLI removed its `--headless
--stdio` flags in 2026 (issue #1606) and pushed integrators toward
`--acp` instead — a breaking change for any wrapper. Pi's smaller
surface and explicit thin-wrapper framing make that class of disruption
less likely.

### Mechanical fit with raymond's existing surface

Beyond the strategic case, pi is also a good mechanical fit for the
surface raymond already drives:

- It exposes a structured machine protocol (`--mode json` for an event stream
  and `--mode rpc` for a bidirectional JSONL channel on stdin/stdout) that is
  well-suited to long-lived orchestration.
- It supports first-class `--system-prompt` (replace) and
  `--append-system-prompt` (append) — strictly more than Claude, which has
  only `--append-system-prompt`. Useful as a forward-looking knob even
  though raymond doesn't drive system prompts today.
- It supports session resume by id (`--session <path|id>`, `-c` for most
  recent) and a separate `--fork <path|id>` operation that branches from a
  specific session — which is what raymond's `<call>` and cross-workflow
  call/return need (the callee inheriting the caller's history).
- Its multi-provider model selector (`--provider <name>`, `--model <pattern>`,
  `--thinking <level>`) lets raymond authors run workflows against models from
  Anthropic, OpenAI, Google, and others without changing backend.

The trade-off is that pi has a smaller user base and is less battle-tested
than the vendor CLIs, and a few raymond-internal assumptions
(per-event-cost telemetry on the live stream and Claude's
`--permission-mode acceptEdits` shape) have no direct equivalent on pi
and degrade as documented in "Features that are unavailable" below.

## What was considered

Prior versions of this strategy surveyed five candidate backends — Codex
CLI, Gemini CLI, Cursor CLI, pi, and GitHub Copilot CLI — with a
capability comparison table covering one-shot mode, stream-JSON, system
prompts, auto-approve, session resume, continue-and-fork, cost
reporting, and working-directory handling. That survey is in the git
history of `docs/pi-backend-rationale.md`; it is not reproduced here
because the strategy decision above renders it moot. Git history also
preserves the corresponding (now-deleted) `copilot-backend.md` feature
spec.

## Scope

What this backend covers:

- pi runs as a second backend behind the `internal/backend` abstraction,
  alongside the original Claude-only path.
- Workflow-level `backend:` declaration in the manifest (Claude Code
  remains the default when absent).
- Per-state knobs (`model`, `effort`, `allowed_transitions`, timeout) and
  the tool disallow list are mapped onto pi's equivalents, degrading
  cleanly where pi has no direct equivalent.

Out of scope, now and for the foreseeable future:

- Per-state backend switching. A workflow that genuinely needs both
  Claude and pi composes them via cross-workflow invocation
  (see [cross-workflow-design.md](cross-workflow-design.md)).
- Any other backend (codex, gemini, cursor, copilot) as a first-class
  raymond integration. Workflows that want those providers should use
  pi with the appropriate `--provider` setting.

## Backend abstraction (where pi plugs in)

The Claude Code integration is narrow and well-isolated, and the backend
abstraction (`internal/backend`) is in place. The pi implementation is a
sibling of `ClaudeBackend`; the executor, orchestrator, and observers are
unchanged at the seam.

- **Command construction:** `internal/ccwrap/ccwrap.go` —
  `BuildClaudeCommand` assembles flags. The binary name `claude` is
  held in the `claudeExe` package variable (overridable in tests).
  `BuildClaudeEnv` strips `CLAUDECODE` to prevent nested sessions, and
  `InvokeStream` / `Invoke` wrap execution. Flags assembled today:
  `--output-format stream-json`, `-p`, `--permission-mode acceptEdits`
  or `--dangerously-skip-permissions`, `--model`, `--effort`,
  `--resume <id>` or `-c --fork-session`, `--disallowed-tools`
  (hardcoded: `EnterPlanMode`, `ExitPlanMode`, `AskUserQuestion`,
  `NotebookEdit`), and the prompt after `--`.

- **Stream parsing:** `internal/backend/claude.go` —
  `emitClaudeStreamEvents` translates Claude stream-JSON objects into
  Sink callbacks. Message types handled: `assistant` (text and
  `tool_use` items), `user` (tool results, with `is_error` driving
  ErrorOccurred via Sink.OnToolError), and `result` (final, with
  `is_error` driving usage-limit detection via `isClaudeLimitResult`).
  Cost is read from `total_cost_usd` and tokens summed from
  `usage.cache_creation_input_tokens`, `cache_read_input_tokens`, and
  `input_tokens` (`extractClaudeCost`, `extractClaudeTokens` in the
  same file). Session ID is read from `session_id` or
  `metadata.session_id` via `ccwrap.ExtractSessionID`.

- **Backend interface:** `internal/backend/backend.go` defines
  `Backend.RunTurn(ctx, TurnSpec, Sink) (TurnResult, error)` plus the
  neutral error types (`TimeoutError`, `LimitError`, `RunError`). The
  Claude implementation lives in `internal/backend/claude.go`. A pi
  implementation goes alongside it (`internal/backend/pi.go`).

- **Per-state agent config:** `internal/policy/policy.go` defines
  `Policy` with `AllowedTransitions`, `Model`, `Effort`. Per-workflow
  launch defaults live in `internal/state/state.go` as
  `LaunchParams` (`DangerouslySkipPermissions`, `Model`, `Effort`,
  `Timeout`, `ContinueAndFork`, `OnAsk`).

- **Executor abstraction:** `internal/executors/executors.go` defines
  a `StateExecutor` interface with two implementations
  (`MarkdownExecutor`, `ScriptExecutor`). Selection is by file
  extension (`GetExecutor`). The markdown executor now calls
  `backend.Backend.RunTurn` (defaulting to the Claude backend); the
  pi-vs-Claude split happens at that call.

- **Manifest:** `internal/manifest/manifest.go` defines the `Manifest`
  struct. This is where the `backend:` field lands.

## Resolved questions

The original five-candidate design notes left eight open questions.
Most resolve automatically under the pi-only strategy because pi has
session resume, fork, system-prompt flags, and stream-JSON. The
remainder are resolved in the rest of this document:

- **`backend:` shape** — structured block. A workflow writes either the
  bare string `backend: pi` or a name plus options (`backend:` with
  `name:` and `options:` children, exposing pi-specific knobs like
  `session_dir`, `tools`, `extensions`, `skills`). That lets pi's flag
  surface come through faithfully without inventing a generic
  cross-backend vocabulary for them.
- **Model field shape** — opaque pass-through. The workflow author writes
  Claude vocabulary (`opus`, `sonnet`) when targeting Claude or pi
  vocabulary (`provider/id`, e.g. `anthropic/claude-sonnet-4-6`) when
  targeting pi.
- **Tool disallow list** — Claude-specific scaffolding; no-op on pi.
  Raymond's hardcoded list (`EnterPlanMode`, `ExitPlanMode`,
  `AskUserQuestion`, `NotebookEdit`) names tools that don't exist in
  pi's surface.
- **MCP** — **not supported under pi.** Pi is not MCP-native; it uses
  `--extension` and `--skill` instead. Workflows that require an
  MCP-hosted tool can't run under pi unless the tool also ships as a
  pi extension or skill. The `ray serve` daemon's own MCP surface
  (what external clients call) is unaffected — that's separate from
  the agent-side tool surface.
- **Availability preflight** — `pi --version` is run once at workflow
  start (and on resume), failing fast with a clear message if pi
  isn't installed.

## User-visible surface

### Declaring the backend

A workflow opts into the pi backend in its manifest:

```yaml
# workflow.yaml
id: my-workflow
backend: pi
```

For YAML scope (single-file) workflows, the same `backend:` key sits at the
top of the YAML alongside `id`, `name`, `description`. For multi-file or
zipped workflows it sits in `workflow.yaml`.

When `backend:` is absent, raymond uses the existing Claude Code default. The
backend cannot vary between states within a single workflow; mixed-backend
work composes via cross-workflow invocation
(see [cross-workflow-design.md](cross-workflow-design.md)).

### Declaring backend options

For options that are pi-specific, the `backend:` key accepts a structured
form:

```yaml
backend:
  name: pi
  options:
    provider: anthropic              # --provider
    thinking: medium                 # --thinking <off|low|medium|high|xhigh>
    tools: [read, edit, write, grep, find, ls]  # --tools allowlist (omit `bash` unless needed)
    no_builtin_tools: true           # --no-builtin-tools / -nbt (mutually exclusive with `tools`)
    no_tools: true                   # --no-tools / -nt (mutually exclusive with the above)
    extensions:                      # --extension (repeatable; npm package, git URL, or local path)
      - <extension-source>
    skills:                          # --skill (repeatable; local path)
      - ./skills/<skill-dir>
    no_extensions: true              # --no-extensions (disables auto-discovery)
    no_skills: true                  # --no-skills (disables auto-discovery)
    session_dir: <path>              # --session-dir (override pi's default)
```

All fields are optional; the example above shows the shapes, not a
recommended set. The intent is that pi's flag surface is exposed faithfully
under `options:` rather than translated through a generic vocabulary,
because the meanings are pi-specific (especially the tool allowlist and
skill/extension model). The boolean flags shown as `true` above are pi's
"opt out" switches; their default is implicitly `false` (don't pass the
corresponding pi flag).

### Per-state model knobs

Per-state YAML frontmatter continues to declare `model:` and `effort:` (the
existing knobs from `internal/policy/policy.go`), but their interpretation
adapts to pi:

| Frontmatter field | Claude backend | pi backend |
|---|---|---|
| `model: <id>` | `--model <id>` (e.g. `sonnet`, `opus`) | `--model <pattern>`; the workflow author may use pi's `provider/id` syntax (e.g. `anthropic/claude-sonnet-4-6`) when the workflow targets pi specifically |
| `effort: <level>` | `--effort <id>` (Claude's vocabulary) | `--thinking <off\|low\|medium\|high\|xhigh>`. Raymond translates `low/medium/high` 1:1; values outside pi's vocabulary are passed verbatim and produce a clear error if rejected by pi |

Workflow authors who want to keep the workflow portable across backends should
prefer values that are valid for both. Workflows that target pi exclusively
may use pi-native model patterns directly.

### Launch flags

`ray run` and `ray serve` keep their existing flags. The interpretation
of `--dangerously-skip-permissions` adapts: pi has no per-call permission
prompts the way Claude does. Instead, pi's safety boundary is the
`--tools` allowlist and the `--no-tools` / `--no-builtin-tools` flags. In the
pi backend:

- `--dangerously-skip-permissions` (raymond CLI) ⇒ raymond passes the workflow's
  declared `tools` allowlist if present, otherwise no `--tools` flag at all
  (pi runs with its default tool surface).
- The default (no flag) ⇒ raymond passes the workflow's declared `tools`
  allowlist if present, otherwise it passes a conservative built-in default:
  `read, edit, write, grep, find, ls` — pi's built-in tools minus `bash`. A
  workflow that needs `bash` under the default safety mode must opt in by
  listing it explicitly in `backend.options.tools`. This preserves the
  spirit of "ask before risky things" for pi, even though the mechanism
  differs from Claude's per-call prompts.

This is the most user-visible semantic difference between the two backends and
will be called out in the authoring guide.

### Preflight check

When a workflow declares `backend: pi`, raymond probes for the `pi` binary
once at workflow start (and once on resume) via a single `pi --version`
invocation with a 5-second timeout, and fails fast with a clear "pi not
found in PATH" message that points at install instructions (pi is
distributed as an npm package — `npm install -g @mariozechner/pi-coding-agent`
— and therefore requires a Node.js runtime on the host). The cost is one
process spawn; the payoff is that a missing binary surfaces immediately
instead of mid-workflow.

Raymond does **not** validate API credentials at preflight. Pi is expected
to be already authenticated against whatever provider the workflow uses
(`pi auth` or environment variables); credential errors surface from pi's
first real invocation just as they do for Claude today.

## What is preserved (parity with Claude backend)

The following raymond features work identically under the pi backend. The
backend abstraction handles the translation; workflow authors notice no
difference.

- **State graph and transition tags.** All seven transition tags (`goto`,
  `reset`, `call`, `function`, `fork`, `ask`, `result`) work unchanged.
  Their semantics live in the orchestrator, not in the backend.
- **Shell-script states.** Shell, batch, and PowerShell states do not use any
  backend; they continue to run as plain subprocesses with zero token cost.
- **`<ask>` and human-in-the-loop.** The orchestrator suspends the workflow
  to disk and resumes when input arrives. Independent of the backend.
- **Cross-workflow invocation.** `<call-workflow>`, `<function-workflow>`,
  `<fork-workflow>`, `<reset-workflow>` — independent of backend. (A called
  workflow may declare its own backend; raymond launches the appropriate
  backend per nested workflow.)
- **Per-workflow cost budget.** The dollar budget is enforced at the
  orchestrator level. Raymond reads pi's per-turn cost by parsing the
  session JSONL file after each turn, then applies the same
  budget-overrides-transition rule as Claude.
- **Crash recovery and `--resume`.** Raymond's persisted workflow state is
  backend-agnostic; resume continues to work. Each agent state record carries
  its backend-specific session id (a pi session UUID for pi, a Claude session
  id for Claude), and raymond passes the right one to the right backend on
  resume.
- **`ray lint`, `ray diagram`, `ray convert`.** Static analysis
  is over the workflow graph; the chosen backend has no effect.
- **`ray serve` daemon.** HTTP API, MCP tool surface, web UI, and input
  delivery all continue to work. The dashboard learns to display the active
  backend and per-agent backend session ids.
- **`<call>` (stack frame that inherits parent context).** Implemented by
  invoking pi with `--fork <caller-session-id>`, which branches a new
  session off the caller's so the callee starts with the caller's history.
  Same shape as Claude (`--fork-session` after `--resume <caller>`); only
  the flag name changes. **Important for cost accounting:** pi's `--fork`
  *copies* the caller's full message history into the new session file
  (verified empirically on pi v0.74). The pi backend reads the caller's
  prior cost before invoking pi and subtracts it from the post-turn read
  so the call only charges the caller for the new turn's spend; otherwise
  the caller's history would be re-charged on every `<call>`.
- **`<fork>` (spawn a parallel agent).** Despite the flag name overlap,
  `<fork>` is *not* related to pi's `--fork` (or Claude's `--fork-session`).
  It is a Unix-`fork()`-style operation that launches an additional
  independent agent within the workflow. Whether the child inherits any
  session history is decided at the orchestrator/transitions layer (the
  same way it's decided for Claude today); the pi backend just executes
  whatever session-id wiring the orchestrator hands it.
- **`<function>` (stack frame with fresh context) and `<reset>` (replace
  current context).** Both run pi with neither `--session` nor `--fork`, so
  the agent starts a brand-new pi session with no inherited history. There
  is no fork flag involved — the previous session is simply abandoned.
  `<reset>` additionally clears the agent's persisted session id so
  subsequent `<goto>` turns continue against the new session.

## What changes (different mechanism, same intent)

These behaviors continue to work but are wired differently under pi.

### Stream parsing

Raymond parses Claude's `--output-format stream-json` shape inside the
Claude backend (`internal/backend/claude.go`, `emitClaudeStreamEvents`).
Under the pi backend, a sibling implementation parses pi's `--mode json`
event stream instead. The event types raymond consumes from pi:

- `session` — top-of-stream event carrying the session UUID
  (`{"type":"session","id":"<uuid>", ...}`). Raymond records this id on the
  agent state so the next turn can pass it to `--session` or `--fork`.
  (Earlier drafts of this spec expected the id on `agent_start`; pi v0.74
  emits a bare `agent_start` and puts the id on `session`. The pi backend
  reads from `session` and falls back to `agent_start.sessionId` for
  forward-compat.)
- `message_update` (assistant `text_delta` events) — drives `ProgressMessage`
  events on the bus.
- `tool_execution_start` (with `toolName`, `args`) — drives `ToolInvocation`
  events.
- `tool_execution_end` (with `result`, `isError`) — drives `ErrorOccurred`
  events when `isError` is true.
- `agent_end` — terminal event; raymond uses it to know the turn is complete
  and to extract the assistant's final message text (which carries the
  transition tag raymond is looking for). The text lives at
  `messages[-1].content[i].text` for assistant role — *not* on a top-level
  `text` field.

Other pi event types (`turn_start`, `turn_end`, `message_start`,
`message_end`, `compaction_start`, `compaction_end`, `queue_update`) are
read off the stream but produce no orchestrator-facing events. They may be
written to the per-state debug log if `--debug-dir` is set. In particular,
**pi's automatic context compaction is assumed to be transparent to
raymond**: the session id is unchanged across compaction, and the next
turn's `--session <id>` invocation should work as if compaction had not
happened.

> **Verification status (pi v0.74):** session-id stability across normal
> multi-turn resume is confirmed (`workflows/pi_smoke_resume` exercises a
> 2-turn `<goto>` loop; the `session` event reports the same UUID on both
> turns). Session-id stability across compaction is confirmed by source
> reading: both `agent-session.js`'s manual `compact()` (line 1306) and
> the auto-compact path in `_checkCompaction()` (line 1543) funnel through
> `sessionManager.appendCompaction(...)`, which appends a `compaction`
> entry to the existing branch and never touches `this.sessionId` or
> `this.sessionFile`. Triggering compaction at runtime from
> `--print --mode json` isn't possible (`/compact` is TUI-only) and the
> auto-compact threshold is far past a smoke test, but the code path is
> unambiguous. Revisit if a workflow ever reports a session-id mismatch on
> resume.

The backend abstraction normalizes the consumed events into the same
orchestrator-facing events the Claude path emits today, so the rest of
raymond is unchanged.

### Idle and total timeouts

Raymond's idle timeout (default 600 s, resets on each chunk received) and
total timeout apply to pi turns identically: any line on pi's stdout
resets the idle timer. The same `ClaudeCodeTimeoutError`-shaped error is
raised on timeout (renamed at the abstraction layer to a backend-neutral
`AgentTimeoutError` or similar — orchestrator-level naming is implementer's
choice).

### Process model

Raymond invokes pi the same way it invokes claude: **one pi process per
state turn**. Each turn either resumes the agent's existing session
(`--session <id>` for `<goto>`), branches a new session off a caller's
history (`--fork <caller-session-id>` for `<call>`), or starts fresh (no
session flag, for `<reset>` / `<function>` / first turn). When the turn
completes (pi emits `agent_end` and exits), raymond reads cost/usage and
decides the next state. No long-lived pi process is held open between
turns.

This matches the Claude code path (`InvokeStream` per turn) and keeps
crash recovery simple: workflow state on disk plus the pi session JSONL
on disk are sufficient to resume after any failure.

### Stream protocol

Raymond uses pi's `--mode json` (per-turn event stream on stdout). Pi's
`--mode rpc` (bidirectional long-lived JSONL channel) is **not** used in
v1. Rationale:

- `--mode json` matches the per-turn invocation pattern raymond already
  uses for Claude; the existing `InvokeStream`-shaped abstraction maps
  directly.
- The features `--mode rpc` adds (`steer`, `abort`, queue management,
  `get_session_stats`) are not currently needed: raymond's cancellation
  is process-kill, raymond never queues prompts to a running agent, and
  cost/usage can be read from the session JSONL file at turn end.
- One process per turn means pi's exit code is the natural failure
  signal, mirroring Claude's contract.

The protocol mode is an internal detail of the pi backend, not exposed to
workflow authors.

### Cost and token accounting

Pi's `--mode json` event stream does not include per-event cost or token
counts. After each turn (when pi exits), raymond parses the session JSONL
file and sums per-assistant-message usage records to derive
`total_cost_usd` and token counts identical in shape to what Claude
reports. The session JSONL lives under pi's session storage (default
`~/.pi/agent/sessions/<mangled-cwd>/<timestamp>_<id>.jsonl`, or under
`backend.options.session_dir` if set); raymond locates the file by the
session id captured from the `session` event, matching by suffix because
pi prepends an ISO-timestamp to the filename. The cwd-to-directory
mangling pi performs (strip leading/trailing `/`, replace `/` with `-`,
wrap in `--`) is mirrored in `internal/piwrap/piwrap.go`.

The orchestrator's per-workflow cost budget is enforced from these reads
exactly as it is for Claude.

### Session storage

By default raymond does **not** pass `--session-dir`; pi stores sessions in
its normal location (`~/.pi/agent/sessions/<mangled-cwd>/`), the same way
the existing Claude integration relies on Claude's normal session storage
under `~/.claude/`.

Pi organizes session files under sub-directories keyed by the working
directory at session-creation time (with the cwd path mangled into a
flat dirname — see "Cost and token accounting" above). This is *not* a
new constraint for raymond: Claude Code's session storage has the same
property, and raymond already only allows the working directory to change
at points where the session is being abandoned anyway (`<reset>`,
`<function>`, `<fork>`, cross-workflow boundaries). Within a single
continuous session — `<goto>` loops, `<call>`/return — the cwd stays put,
so the cwd-keyed organization is invisible.

A workflow author may set `backend.options.session_dir` to relocate
sessions — for example to keep them with the workflow run state for
archival, or to share them with an interactive pi TUI. Raymond passes the
value through to pi's `--session-dir` flag verbatim.

### Per-state command construction

The pi-backend equivalent of `BuildClaudeCommand` assembles a different flag
list:

- Always: `pi --mode json` (and `--session-dir <dir>` only if the workflow
  set `backend.options.session_dir`).
- If model is set: `--model <value>` (and optionally `--provider <value>` from
  `backend.options.provider`).
- If effort is set: `--thinking <translated value>`.
- For session continuation: `--session <session-id>` to resume the agent's
  existing session (the `<goto>` case), or `--fork <caller-session-id>` to
  branch a new session from a caller's history (the `<call>` case). The
  first turn after `<reset>` or `<function>` passes neither flag, so pi
  starts a brand-new session. Pi's `-c` ("most recent session") is *not*
  used — raymond always knows the exact session id it wants and passes it
  explicitly.
- Tool allowlist derived from `--dangerously-skip-permissions` and
  `backend.options.tools` (see "Launch flags" above). One of `--tools`,
  `--no-tools`, or `--no-builtin-tools` may apply, derived as follows:
  `backend.options.no_tools: true` ⇒ `--no-tools`;
  `backend.options.no_builtin_tools: true` ⇒ `--no-builtin-tools`;
  otherwise `--tools <comma-list>` per the Launch-flags table.
- If declared: `--system-prompt` / `--append-system-prompt` (currently unused
  by raymond; reserved for future use).
- If declared: each `--extension <source>` and `--skill <path>` from
  `backend.options`.
- The state's prompt body, delivered as a single trailing positional
  argument. Raymond invokes pi via Go's `exec.Command`, which passes argv
  as raw bytes to the child process — **no shell interprets the prompt**,
  so quotes, newlines, backticks, dollar signs, etc. cannot be
  misinterpreted. This is the same pattern raymond uses for Claude
  (`ccwrap.go` line 217, with tests at `ccwrap_test.go:47-65` verifying
  the argv layout) and is robust by construction.

### Tool disallow list

Raymond hardcodes a Claude-specific disallow list (`EnterPlanMode`,
`ExitPlanMode`, `AskUserQuestion`, `NotebookEdit`) so the agent does not enter
modes raymond doesn't drive. Pi has none of those tools, so the disallow list
is a no-op under the pi backend. The intent ("no plan mode, no notebooks, no
mid-state human prompts") is automatically satisfied by pi's smaller tool
surface.

### Working directory and environment

Pi inherits the parent process's working directory (matching Claude). Raymond
strips the `CLAUDECODE` environment variable today (`ccwrap.BuildClaudeEnv`)
to prevent Claude from treating itself as nested inside another session. The
pi backend does not need to strip `CLAUDECODE`; if pi adopts an analogous
nested-session marker in the future, the backend abstraction is the right
place to handle it.

## Features that are unavailable with the pi backend

These are features that exist for the Claude backend but cannot be supported
under pi as currently understood, or that have meaningful semantic differences
the workflow author should know about.

1. **Continue-and-fork from the *user's* most recent interactive session.**
   Raymond's `--continue-and-fork` flag (`internal/state/state.go` line 127)
   maps to Claude's `-c --fork-session` and lets a workflow attach to
   whatever session the user most recently ran in their terminal. Pi has
   `-c` ("continue most recent session", with sessions organized under
   `~/.pi/agent/sessions/` keyed by working directory), but the semantics of
   "most recent" are not directly equivalent to Claude's, and a workflow
   author who reaches for this flag is almost certainly relying on the
   Claude-specific behavior. **For the pi backend, the `--continue-and-fork`
   flag is rejected at workflow start** with an error pointing the user at
   pi's `--session <id>` if they want explicit resume from a known session.

2. **Per-tool approval prompts (`--permission-mode acceptEdits`).** Pi has no
   per-call permission prompt model; safety lives in the static `--tools`
   allowlist. Workflows that depended on Claude's mid-call accept-edit prompts
   to gate destructive tool calls have no equivalent under pi. The
   recommended migration is to declare a conservative `backend.options.tools`
   allowlist that omits `bash` (and any other risky tools) for those states.

3. **Claude usage-limit detection.** Raymond detects Claude's "hit your limit"
   / "out of extra usage" messages from the result stream
   (`internal/backend/claude.go` `claudeLimitPatterns`) and treats them as a
   special class of failure. Pi has no equivalent provider-level message in
   its event stream; provider rate limits surface as ordinary `tool_execution_end`
   errors or `agent_end` errors, and raymond classifies them as generic
   failures.

4. **Per-event cost reporting on the live event stream.** Cost is read
   from the session JSONL after the turn ends, not from each `result`
   message during the turn as Claude does. The end-of-turn cost is
   identical; what is missing is intra-turn cost telemetry that some
   observers display in the dashboard. The dashboard will show `—` for
   in-flight cost on pi states until the turn completes.

5. **Claude-specific `model:` values.** A workflow that hardcodes
   `model: opus` only makes sense against Claude. Under pi the workflow
   author must either use a value pi recognizes (a model id valid for the
   selected provider, with optional `provider/id` prefix) or omit the field
   and let pi pick its default. Raymond does not translate `opus` ⇒
   "anthropic/claude-opus-…" automatically; the field is passed through
   verbatim.

6. **Claude-specific tool names in `disallowed_tools` workflow overrides.**
   If a future feature lets workflows extend the hardcoded disallow list with
   their own entries, those entries are interpreted by Claude only. A pi
   workflow expresses the same intent through `backend.options.tools` (an
   allowlist).

7. **Agent-side MCP servers.** Pi does not natively speak MCP. Its tool
   surface is its built-in tools plus its own extension (`--extension`) and
   skill (`--skill`) mechanisms, which are pi-specific and not
   protocol-compatible with MCP. Workflows that depend on the agent calling
   tools exposed by an MCP server cannot run under pi unless an equivalent
   pi extension is available. Note this is distinct from raymond's own
   *daemon* MCP surface (the tools `ray serve` exposes to external
   clients): that is a property of the daemon, not the backend, and works
   regardless of which backend a given workflow uses. (See "Resolved
   questions" above for the same point in the context of the strategy.)

8. **The `effort: <Claude-specific level>` vocabulary on per-state policies.**
   Claude's `--effort` accepts a different vocabulary than pi's `--thinking`.
   Values that overlap (`low`, `medium`, `high`) translate cleanly; other
   values produce a clear error from pi rather than a silent reinterpretation.

## Backend-comparison matrix (raymond features × backends)

| Raymond feature | Claude backend | pi backend |
|---|---|---|
| State graph + transition tags | ✓ | ✓ (orchestrator-level) |
| Shell-script states | ✓ | ✓ (orchestrator-level) |
| `<ask>` / human-in-the-loop | ✓ | ✓ (orchestrator-level) |
| `<fork>` (spawn parallel agent) | ✓ | ✓ (orchestrator-level; not related to `--fork`) |
| `<call>` (inherit caller context) | ✓ (`--fork-session`) | ✓ (`--fork <caller-session-id>`) |
| `<function>` / `<reset>` (fresh context) | ✓ | ✓ (no `--session` or `--fork` flag) |
| Cross-workflow invocation | ✓ | ✓ (orchestrator-level) |
| Per-workflow cost budget | ✓ | ✓ (cost summed from session JSONL after each turn) |
| `--resume <run_id>` after crash | ✓ | ✓ (pi session UUID persisted) |
| `ray lint` / `diagram` / `convert` | ✓ | ✓ (orchestrator-level) |
| `ray serve` daemon + web UI | ✓ | ✓ (UI shows pi session ids) |
| `--dangerously-skip-permissions` | ✓ (acceptEdits / skip) | ✓ but different mechanism — controls tool allowlist |
| `--continue-and-fork` | ✓ | **✗ rejected at launch** |
| Per-state `model:` portability | Claude vocabulary | pi vocabulary (use `provider/id` for clarity) |
| Per-state `effort:` portability | Claude vocabulary | pi `--thinking` vocabulary; common values (`low`/`medium`/`high`) overlap |
| Hardcoded tool disallow list | enforced | no-op (those tools don't exist on pi) |
| Live per-event cost | ✓ | end-of-turn only |
| Usage-limit-specific error class | ✓ | generic failure |
| MCP servers (any source) | ✓ (Claude is MCP-native) | **✗ — pi is not MCP-native; equivalent capability via pi extensions only** |

## Authoring guidance

A workflow author writing for pi specifically should:

1. Declare `backend: pi` (or the structured form) in `workflow.yaml`.
2. If a specific model matters, set `model:` to a pi-recognized value
   (`provider/model-id` form is least ambiguous).
3. Decide tool safety up front: declare `backend.options.tools` with the
   minimum set the workflow needs. Pi has no per-call permission prompts to
   fall back on — the allowlist is the only gate.
4. Don't pass `--continue-and-fork` at the CLI — raymond rejects it for pi
   workflows. Use `--session <id>` explicitly if you need to resume a known
   session.
5. If the workflow integrates with external tools, do **not** assume MCP
   is available — pi is not MCP-native. Express tool integrations as pi
   extensions (`backend.options.extensions`) or skills
   (`backend.options.skills`). A workflow whose external tools are only
   available as MCP servers cannot run under pi.

A workflow that is meant to run portably across both backends should:

- Avoid backend-specific `model:` values (or rely on per-environment
  overrides from the launch CLI).
- Use only `effort:` values in the overlap (`low`, `medium`, `high`).
- Not depend on Claude usage-limit detection or live per-event cost.
- Not use `--continue-and-fork`.

## Open issues and validation status

Resolved in the body of this spec: protocol mode (`--mode json`), cost data
path (post-turn JSONL parse), session storage (pi's default with optional
override). Status of the rest:

### Pre-implementation validation (now complete)

All resolved against pi v0.74. Session-id stability across compaction is
confirmed by reading pi's source — see the "Stream parsing" section's
verification-status note. End-to-end behavior of `<call>` (pi's `--fork`,
including the prior-cost subtraction) and `<fork>` (parallel agents with
fresh sessions) is covered by `TestPiIntegration_RealPi_CallTransition`
and `TestPiIntegration_RealPi_ForkTransition`. Windows cwd mangling
(`piCwdDirName`) remains unverified on a real Windows host; the cost-read
path degrades to a silent $0/turn there until someone with a Windows box
ports pi's actual mangling.

### Genuinely open / forward-looking

1. **System prompt usage.** Raymond does not currently set a system prompt
   for either backend. If a future orchestrator-level instructions feature
   (injecting transition syntax reminders or tool-use guardrails as a
   system prompt rather than baking them into every state file) lands, pi
   already supports `--append-system-prompt`; no further pi-specific work
   is required.
