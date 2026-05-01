# pi Backend (Feature Specification)

This document specifies the pi backend for raymond — *what* is to be built,
not how. It assumes the broader backend abstraction described in
[multi-backend-design.md](multi-backend-design.md) and picks pi as the first
non-Claude backend to land.

For background on the existing Claude Code integration that this feature
parallels, see [code-structure.md](code-structure.md) and the
"Existing Claude Code integration" section of
[multi-backend-design.md](multi-backend-design.md).

## Why pi

Of the candidate backends surveyed in the multi-backend design notes, **pi**
(`badlogic/pi-mono`, binary `pi`) is the closest match to the surface raymond
already drives. Specifically:

- It exposes a structured machine protocol (`--mode json` for an event stream
  and `--mode rpc` for a bidirectional JSONL channel on stdin/stdout) that is
  better-suited to long-lived orchestration than codex / gemini / cursor /
  copilot.
- It supports first-class `--system-prompt` and `--append-system-prompt`
  (parity with Claude on a forward-looking knob).
- It supports session resume by id (`--session <path|id>`, `-c` for most
  recent) and a separate `--fork <path|id>` operation that branches from a
  specific session — which maps cleanly onto raymond's `<fork>` and
  cross-workflow call/return semantics.
- Its multi-provider model selector (`--provider <name>`, `--model <pattern>`,
  `--thinking <level>`) lets raymond authors run workflows against models from
  Anthropic, OpenAI, Google, and others without changing backend.

The trade-off is that pi has a smaller user base and is less battle-tested than
the vendor CLIs, and a few raymond-internal assumptions (cost-per-event,
permission-mode shape, Claude-specific tool disallow names) need to be
generalized before the backend can be added.

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
    tools: [read, bash, edit, write, grep, find, ls]  # --tools allowlist
    no_builtin_tools: false          # --no-builtin-tools / -nbt
    no_tools: false                  # --no-tools / -nt
    extensions:                      # --extension (repeatable)
      - "@some-org/pi-extension-foo"
    skills:                          # --skill (repeatable)
      - ./skills/my-skill
    no_extensions: false             # --no-extensions
    no_skills: false                 # --no-skills
    session_dir: ""                  # --session-dir
```

All fields are optional. The intent is that pi's flag surface is exposed
faithfully under `options:` rather than translated through a generic
vocabulary, because the meanings are pi-specific (especially the tool
allowlist and skill/extension model).

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

`raymond run` and `raymond serve` keep their existing flags. The interpretation
of `--dangerously-skip-permissions` adapts: pi has no per-call permission
prompts the way Claude does. Instead, pi's safety boundary is the
`--tools` allowlist and the `--no-tools` / `--no-builtin-tools` flags. In the
pi backend:

- `--dangerously-skip-permissions` (raymond CLI) ⇒ raymond passes the workflow's
  declared `tools` allowlist (or pi's default full allowlist if none was
  declared) without further restriction.
- The default (no flag) ⇒ raymond passes a conservative allowlist that
  excludes `bash` unless the workflow has explicitly opted in via
  `backend.options.tools`. This preserves the spirit of "ask before risky
  things" for pi, even though the mechanism differs.

This is the most user-visible semantic difference between the two backends and
will be called out in the authoring guide.

### Preflight check

When a workflow declares `backend: pi`, raymond probes for the `pi` binary at
workflow start (a single `pi --version` invocation) and fails fast with a
clear "pi not found in PATH" message that points at install instructions
(pi is distributed as an npm package — `npm install -g
@mariozechner/pi-coding-agent` — and therefore requires a Node.js runtime
on the host). The cost is one process spawn; the payoff is that a missing
binary surfaces immediately instead of mid-workflow.

## What is preserved (parity with Claude backend)

The following raymond features work identically under the pi backend. The
backend abstraction handles the translation; workflow authors notice no
difference.

- **State graph and transition tags.** All seven transition tags (`goto`,
  `reset`, `call`, `function`, `fork`, `await`, `result`) work unchanged.
  Their semantics live in the orchestrator, not in the backend.
- **Shell-script states.** Shell, batch, and PowerShell states do not use any
  backend; they continue to run as plain subprocesses with zero token cost.
- **`<await>` and human-in-the-loop.** The orchestrator suspends the workflow
  to disk and resumes when input arrives. Independent of the backend.
- **Cross-workflow invocation.** `<call-workflow>`, `<function-workflow>`,
  `<fork-workflow>`, `<reset-workflow>` — independent of backend. (A called
  workflow may declare its own backend; raymond launches the appropriate
  backend per nested workflow.)
- **Per-workflow cost budget.** The dollar budget is enforced at the
  orchestrator level. Raymond reads pi's per-turn cost via `get_session_stats`
  (RPC mode) or by parsing the session JSONL after each turn, then applies the
  same budget-overrides-transition rule as Claude.
- **Crash recovery and `--resume`.** Raymond's persisted workflow state is
  backend-agnostic; resume continues to work. Each agent state record carries
  its backend-specific session id (a pi session UUID for pi, a Claude session
  id for Claude), and raymond passes the right one to the right backend on
  resume.
- **`raymond lint`, `raymond diagram`, `raymond convert`.** Static analysis
  is over the workflow graph; the chosen backend has no effect.
- **`raymond serve` daemon.** HTTP API, MCP tool surface, web UI, and input
  delivery all continue to work. The dashboard learns to display the active
  backend and per-agent backend session ids.
- **`<fork>` (multi-agent within a workflow), `<call>` (stack frame that
  inherits parent context).** Both are implemented by invoking pi with
  `--fork <parent-session-id>`, which branches a new session off the
  parent's. The orchestrator-level distinction (parallel worker vs. push a
  return frame) is unchanged from Claude.
- **`<function>` (stack frame with fresh context).** Started as a brand-new
  pi session with no `--fork` / `--session` flag, so the callee has no
  inherited history.

## What changes (different mechanism, same intent)

These behaviors continue to work but are wired differently under pi.

### Stream parsing

Raymond currently parses Claude's `--output-format stream-json` shape
(`internal/executors/markdown.go` lines 483–589). Under the pi backend, the
backend implementation parses pi's `--mode json` event stream instead. The
event types raymond consumes from pi:

- `agent_start` — once at session begin, exposes the session UUID.
- `message_update` (assistant `text_delta` events) — drives `ProgressMessage`
  events on the bus.
- `tool_execution_start` (with `toolName`, `args`) — drives `ToolInvocation`
  events.
- `tool_execution_end` (with `result`, `isError`) — drives `ErrorOccurred`
  events when `isError` is true.
- `agent_end` — terminal event; raymond uses it to know the turn is complete
  and to extract the assistant's final message text (which carries the
  transition tag raymond is looking for).

The backend abstraction normalizes these into the same orchestrator-facing
events the Claude path emits today, so the rest of raymond is unchanged.

### Cost and token accounting

Pi's `--mode json` event stream does not include per-event cost or token
counts. Raymond gets this data either by:

- Running pi in `--mode rpc` and calling `get_session_stats` after each turn,
  or
- Parsing pi's session JSONL file (`~/.pi/agent/sessions/<...>/<id>.jsonl`)
  after each turn and summing usage records.

Either way, the orchestrator receives the same `total_cost_usd` and token
counts it gets from Claude, and the budget enforcement rule is unchanged.
Which path is taken depends on the protocol-mode decision in "Open issues"
below.

### Per-state command construction

The pi-backend equivalent of `BuildClaudeCommand` assembles a different flag
list:

- Always: `pi`, `-p` (or `--mode json` / `--mode rpc` depending on the
  selected protocol — see "Open issues" below).
- If model is set: `--model <value>` (and optionally `--provider <value>` from
  `backend.options.provider`).
- If effort is set: `--thinking <translated value>`.
- For session continuation: `--session <session-id>` to resume, or
  `--fork <session-id>` to branch. Pi's `-c` ("most recent session") is *not*
  used — raymond always knows the exact session id it wants and passes it
  explicitly.
- Tool allowlist derived from `--dangerously-skip-permissions` and
  `backend.options.tools` (see "Launch flags" above).
- If declared: `--system-prompt` / `--append-system-prompt` (currently unused
  by raymond; reserved for future use).
- If declared: each `--extension <source>` and `--skill <path>` from
  `backend.options`.
- The state's prompt body, delivered either as a positional argument
  (`-p` mode) or as an RPC `prompt` request (rpc mode).

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
   (`internal/executors/markdown.go` `limitPatterns`) and treats them as a
   special class of failure. Pi has no equivalent provider-level message in
   its event stream; provider rate limits surface as ordinary `tool_execution_end`
   errors or `agent_end` errors, and raymond classifies them as generic
   failures.

4. **Per-event cost reporting on the live event stream.** Cost arrives at
   `agent_end` (or via a separate `get_session_stats` call) rather than on
   each `result` message as Claude does. The end-of-turn cost is identical;
   what is missing is intra-turn cost telemetry that some observers display
   in the dashboard. The dashboard will show `—` for in-flight cost on pi
   states until the turn completes.

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
   *daemon* MCP surface (the tools `raymond serve` exposes to external
   clients): that is a property of the daemon, not the backend, and works
   regardless of which backend a given workflow uses. (See open question 6
   in the multi-backend design notes.)

8. **The `effort: <Claude-specific level>` vocabulary on per-state policies.**
   Claude's `--effort` accepts a different vocabulary than pi's `--thinking`.
   Values that overlap (`low`, `medium`, `high`) translate cleanly; other
   values produce a clear error from pi rather than a silent reinterpretation.

## Backend-comparison matrix (raymond features × backends)

| Raymond feature | Claude backend | pi backend |
|---|---|---|
| State graph + transition tags | ✓ | ✓ (orchestrator-level) |
| Shell-script states | ✓ | ✓ (orchestrator-level) |
| `<await>` / human-in-the-loop | ✓ | ✓ (orchestrator-level) |
| `<fork>` (parallel agents) | ✓ | ✓ via `--fork <session-id>` |
| `<call>` / `<function>` | ✓ | ✓ (`call` forks the parent session; `function` starts fresh) |
| Cross-workflow invocation | ✓ | ✓ (orchestrator-level) |
| Per-workflow cost budget | ✓ | ✓ (cost via `get_session_stats` or session JSONL) |
| `--resume <run_id>` after crash | ✓ | ✓ (pi session UUID persisted) |
| `raymond lint` / `diagram` / `convert` | ✓ | ✓ (orchestrator-level) |
| `raymond serve` daemon + web UI | ✓ | ✓ (UI shows pi session ids) |
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
4. Avoid `--continue-and-fork` at the CLI; use explicit session resume if
   needed.
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

## Open issues

These are decisions deferred to implementation; the feature spec does not
fix them.

1. **`--mode json` vs `--mode rpc`.** Both can satisfy the requirements
   above. RPC mode allows in-flight commands (`steer`, `abort`, queue
   management) and a single long-lived process per workflow run; JSON mode
   is simpler and matches the per-turn invocation pattern raymond already
   uses for Claude. The first implementation may pick whichever is faster
   to land and revisit later.

2. **Cost data path.** Per-turn `get_session_stats` over RPC vs.
   post-turn JSONL parse. Tied to the previous decision.

3. **Mapping between raymond's persisted "session id" and pi's session
   storage.** Pi sessions live as files under `~/.pi/agent/sessions/<cwd>/`;
   raymond may want to set `--session-dir` to a workflow-local directory
   (e.g. `.raymond/state/<run_id>/pi-sessions/`) so that workflow runs are
   self-contained and resume is independent of the user's home directory.

4. **System prompt usage.** Raymond does not currently set a system prompt
   for either backend. If the orchestrator-level instructions feature
   (open question 1 in the multi-backend design notes) lands, pi already
   supports `--append-system-prompt`; no further pi-specific work is
   required.

5. **Termux / non-glibc support.** Pi explicitly documents Termux and
   Windows support. If raymond ever targets those platforms, pi may be a
   better-supported backend than Claude there. Out of scope for this
   feature, but worth noting.
