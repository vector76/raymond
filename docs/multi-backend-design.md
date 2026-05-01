# Multi-Backend Support (Design Notes)

This document captures research and open design questions for letting raymond
drive agent backends other than Claude Code. It is a working document — it
records what we learned about each candidate backend, where the existing
Claude Code integration sits in the codebase, and the open decisions that
will shape the eventual feature design. It is intentionally not yet a
finished spec; it exists so we can return to it and prioritize which backend
to land first.

For the surrounding context, see [code-structure.md](code-structure.md) and
[yaml-workflows.md](yaml-workflows.md).

## Motivation

Raymond's original intent was to support multiple agent backends, but the
current implementation is hardcoded to Claude Code. We now want to broaden
the supported set so that workflow authors can pick the backend that best
fits the model preferences, cost profile, sandboxing model, or ecosystem
they need.

The proposed model is:

- **Workflow-level metadata declares the backend.** Claude Code remains the
  default.
- **The backend cannot switch per state.** Every state in a workflow runs on
  the same backend. A workflow that genuinely needs heterogeneous backends
  composes them via cross-workflow invocation
  (see [cross-workflow-design.md](cross-workflow-design.md)).
- **Whole-yaml workflows declare the backend in their YAML.** Multi-file or
  zipped workflows declare it in the same manifest YAML that already carries
  the workflow's name, description, and input spec.

## Scope

In scope:

- A backend abstraction that wraps process invocation, stream parsing, and
  session management for a chosen agent CLI.
- Workflow-level `backend:` declaration in the manifest.
- Mapping the existing per-state knobs (`model`, `effort`,
  `allowed_transitions`, timeout) and the existing tool disallow list onto
  each backend's flags / config files with sensible degradation when a
  backend lacks a feature.

Out of scope (for now):

- Per-state backend switching.
- A unified cost/token accounting model across heterogeneous backends.
- Provider-API-direct invocation (no CLI wrapper). All supported backends
  are external CLIs.

## Candidate backends

The five candidate backends, with Claude Code listed as the existing
reference for comparison:

| Backend | Binary | Repo |
|---|---|---|
| Claude Code (existing default) | `claude` | `anthropics/claude-code` |
| OpenAI Codex CLI | `codex` | `openai/codex` |
| Google Gemini CLI | `gemini` | `google-gemini/gemini-cli` |
| Cursor CLI | `cursor-agent` | docs at `cursor.com/docs/cli` |
| pi (Mario Zechner) | `pi` | `badlogic/pi-mono` |
| GitHub Copilot CLI | `copilot` | `github/copilot-cli` |

### Capability comparison

| Capability | Claude | codex | gemini | cursor | pi | copilot |
|---|---|---|---|---|---|---|
| One-shot mode | `-p` | `exec "..."` | `-p` | `-p` | `-p` | `-p` |
| Stream-JSON events | yes (raymond's reference shape) | `--json` (event-typed; different schema) | `--output-format stream-json` (less stable) | `--output-format stream-json` (Claude-shaped) | `--mode json` / `--mode rpc` | `--output-format` JSONL (limited public schema) |
| `--append-system-prompt` flag | yes | no (use `AGENTS.md` / `-c instructions=`) | no (use `GEMINI.md`) | no (use `.cursor/rules`) | yes (`--system-prompt`, `--append-system-prompt`) | no (use `--agent`) |
| Auto-approve flag | `--dangerously-skip-permissions` or `--permission-mode acceptEdits` | `--full-auto` / `--dangerously-bypass-approvals-and-sandbox` | YOLO mode + settings | `--force` / `--yolo` | per-tool allowlist via `--tools` | `--yolo` |
| Resume by session ID | yes (`--resume <id>`) | yes (`exec resume <id\|--last>`) | only via interactive `/chat resume` | yes (`--resume`) | yes (`-c`, `--session`) | yes (`--resume`, `--continue`) |
| Continue-and-fork | yes (`-c --fork-session`) | no native equivalent | no | unclear | no native equivalent | no |
| Reports cost / tokens | yes | partial | minimal | yes (in result event) | yes | minimal |
| Working directory | inherits | `--cd <dir>`; requires git repo unless `--skip-git-repo-check` | inherits; `--include-directories` for extra roots | inherits | inherits; sessions keyed by cwd | inherits; `--add-dir`, `--config-dir` |
| Notable caveats | — | — | — | community reports of `-p` hanging in some shells | smaller ecosystem, less battle-tested | recent breaking removal of `--headless --stdio` (issue #1606); migration to `--acp` recommended |

### Per-backend notes

**Codex CLI.** `codex exec "<prompt>"` is the dedicated one-shot subcommand,
with `--json` for an NDJSON event stream and `--output-last-message` for the
final assistant text. Sandbox modes (`read-only`, `workspace-write`,
`danger-full-access`) are first-class. No `--append-system-prompt`; we'd
inject system content via `AGENTS.md` or `-c instructions=...`.

**Gemini CLI.** `gemini -p "<prompt>"` for one-shot. Persistent context
lives in `GEMINI.md`. Stream-JSON events are documented but the schema is
less stable than the others. Programmatic session resume is the weakest
of the five — `/chat save` and `/chat resume` are interactive slash
commands, not flags.

**Cursor CLI (`cursor-agent`).** Stream-JSON shape is intentionally close
to Claude Code's, which makes it the easiest to slot in alongside the
existing parser. `--force` / `--yolo` for auto-approve. Project rules in
`.cursor/rules/`.

**pi.** The most flexible non-Claude option for our purposes: it has
first-class `--system-prompt` and `--append-system-prompt`, a
multi-provider model selector (`--model provider/id:thinking-level`), and
a unique `--mode rpc` that exposes a bidirectional JSONL protocol on
stdin/stdout — well-suited to long-lived wrapping. Smaller ecosystem and
less battle-tested than the vendor CLIs.

**GitHub Copilot CLI.** Headless surface is currently in flux: the
`--headless --stdio` flags were removed in 2026 (issue #1606) and the
recommended SDK integration path is now `--acp` (Agent Client Protocol).
`-p` is still available for one-shot but has reported issues on WSL2 and
some VS Code terminals (issue #1181). Notably, the default model is from
the Claude family — this CLI is itself a Claude wrapper, so picking it
mostly buys GitHub MCP / agent integration rather than a different model
family.

## Existing Claude Code integration (what would be abstracted)

The current integration is narrow and well-isolated, so introducing a
backend interface should not require invasive changes:

- **Command construction:** `internal/ccwrap/ccwrap.go` — `BuildClaudeCommand`
  (lines 92-141) assembles flags. The binary name `claude` is held in the
  `claudeExe` package variable (line 50, overridable in tests).
  `BuildClaudeEnv` (lines 71-83) strips `CLAUDECODE` to prevent nested
  sessions, and `InvokeStream` / `Invoke` (lines 177-199, 356-396) wrap
  execution. Flags assembled today: `--output-format stream-json`, `-p`,
  `--permission-mode acceptEdits` or `--dangerously-skip-permissions`,
  `--model`, `--effort`, `--resume <id>` or `-c --fork-session`,
  `--disallowed-tools` (hardcoded: `EnterPlanMode`, `ExitPlanMode`,
  `AskUserQuestion`, `NotebookEdit`), and the prompt after `--`.

- **Stream parsing:** `internal/executors/markdown.go:483-589` parses the
  Claude-shaped stream-JSON. Message types handled: `assistant` (text and
  `tool_use` items), `user` (tool results, with `is_error` driving
  ErrorOccurred events), and `result` (final, with `is_error` driving
  usage-limit detection). Cost is read from `total_cost_usd`; tokens are
  summed from `usage.cache_creation_input_tokens`,
  `cache_read_input_tokens`, and `input_tokens`
  (`internal/executors/executors.go:178-231`). Session ID is read from
  `session_id` or `metadata.session_id`.

- **Per-state agent config:** `internal/policy/policy.go:31-35` defines
  `Policy` with `AllowedTransitions`, `Model`, `Effort`. Per-workflow
  launch defaults live in `internal/state/state.go:122-129` as
  `LaunchParams` (`DangerouslySkipPermissions`, `Model`, `Effort`,
  `Timeout`, `ContinueAndFork`, `OnAwait`).

- **Executor abstraction:** `internal/executors/executors.go:84-91` already
  defines a `StateExecutor` interface with two implementations
  (`MarkdownExecutor`, `ScriptExecutor`). Selection is by file extension
  (`GetExecutor`, line 102). There is **no** backend-level abstraction below
  this — the markdown executor calls `ccwrap` directly. The new backend
  interface would sit between `MarkdownExecutor` and `ccwrap`.

- **Manifest:** `internal/manifest/manifest.go:24-33` defines the `Manifest`
  struct. This is where a `backend:` field would land.

- **No existing pluggability docs.** The only mentions of "backend" in
  `docs/` refer to external storage backends being explicitly out-of-scope.

## Open design questions

The following decisions need to be made before implementation. Answers
will shape the design doc this file evolves into. The numbering is for
reference, not priority.

### 1. System prompts (forward-looking)

Raymond does not currently use system prompts at all — the state markdown
file is passed verbatim as the user prompt after `--`, and there is no
`--append-system-prompt` invocation in `BuildClaudeCommand`. So there is
no immediate parity problem. The question is forward-looking: if a future
feature wants to inject orchestration-level instructions as a system
prompt (e.g. transition syntax reminders, tool-use guardrails) rather
than baking them into every state file, only Claude Code and pi expose a
flag for it. For codex / gemini / cursor / copilot the candidate
strategies would be:

- **(a) Prepend the system content into the user prompt** for each turn.
  Simplest, works everywhere, slightly weakens the system/user separation.
- **(b) Write a transient context file** in the working directory before
  each invocation (`AGENTS.md` for codex, `GEMINI.md` for gemini,
  `.cursor/rules/<name>.md` for cursor, an `--agent` definition for
  copilot). Faithful to each backend's idiom, but mutates the working
  directory and risks colliding with the user's own files.
- **(c) Defer** the feature on backends that lack a flag, and only enable
  it on Claude / pi.

This is the lowest-priority of the open questions because nothing today
depends on it; it is recorded so we don't quietly assume parity.

### 2. Session resume / continue-and-fork

Raymond's orchestration relies on `--resume <sessionID>` and continue-and-fork.
Gemini cannot resume programmatically; codex / cursor / copilot support
resume but not fork. Candidate strategies:

- **(i) Stateless replay.** Each turn replays the relevant transcript from
  the workflow log. Universal, but expensive and may fall foul of context
  limits.
- **(ii) Capability gating.** Declare some backends only compatible with
  workflows that don't need resume or fork. Honest but reduces the value
  of those backends.
- **(iii) Fork-as-fresh-session-with-replay.** When the workflow asks to
  fork, start a fresh session and replay the transcript up to the fork
  point. A hybrid that keeps fork semantics available everywhere.

This is the question most likely to constrain the v1 backend choice.

### 3. Model field shape

Today `model: opus|sonnet|haiku` is Claude-specific. Options:

- **Opaque pass-through.** Whatever the workflow writes is passed verbatim
  to the backend, so codex sees `gpt-5-codex`, pi sees
  `provider/id:thinking-level`, etc. Workflow author owns the coupling.
- **Namespaced.** `claude.model: sonnet`, `codex.model: gpt-5-codex`. More
  verbose but a workflow can switch backends without changing model fields.
- **Symbolic.** `model: fast|balanced|deep` resolved per-backend. Most
  portable, hardest to reason about.

### 4. Backend-specific options

Should `backend:` be a string or a structured block?

```yaml
backend: codex
```

vs

```yaml
backend:
  name: codex
  options:
    sandbox: workspace-write
    skip_git_repo_check: true
```

A structured form lets us expose codex sandbox modes, copilot effort
levels, pi tool allowlists, etc., without inventing a generic vocabulary
for them.

### 5. Tool allow / disallow list

Today raymond hardcodes a Claude-tool disallow list (`EnterPlanMode`,
`ExitPlanMode`, `AskUserQuestion`, `NotebookEdit`) so the agent doesn't
wander into modes raymond doesn't drive. The intent generalizes — "no
plan mode, no notebooks, no asking the user mid-state" — but the names
don't. Two strategies:

- **No-op on other backends.** Their tool surfaces don't have those names
  anyway; the disallow list becomes Claude-specific scaffolding.
- **Generalize to a capability list.** `disable: [planning, notebooks,
  ask-user]` resolved per-backend. More work, but extends naturally as
  more backends are added.

### 6. MCP servers

Claude / codex / gemini / cursor / copilot all support MCP, each with its
own config file. Pi uses its own skill system. If a workflow assumes an
MCP server is wired up (e.g. the mechanism described in
[input-file-attachments-design.md](input-file-attachments-design.md)), do
we:

- Declare the requirement workflow-side and let the operator configure
  each backend's MCP store out-of-band, or
- Have raymond write the necessary MCP config into the chosen backend's
  expected location pre-launch (mutating user config), or
- Provide our own MCP launcher and rely on each backend's ability to
  attach to a process we start?

### 7. v1 scope

Two reasonable shapes for v1:

- **Abstraction + one new backend.** Land the backend interface and one
  carefully-chosen second backend (likely codex for stability or cursor
  for stream-JSON shape similarity). Iterate on the others.
- **All five at once.** More integration risk; payoff is a single big
  release that demonstrates the full breadth.

The capability table above is the prioritization input we'll come back
to. Backends with fork support, stable stream-JSON, and resume by
session ID are cheaper to land first; backends that lack one of those
(gemini in particular) require deciding the question 2 strategy before
they're tractable.

### 8. Availability / preflight check

When a workflow declares a non-default backend, should raymond probe
`which <binary>` (and possibly a `--version` ping) at workflow start and
fail fast with a clear message, or let the first state execution surface
the missing-binary error? The first costs a process spawn; the second
makes the failure mode noisier.

## Next steps

When we return to this:

1. Pick directional answers for questions 2, 4, and 7. Those three shape
   the rest of the design. (Question 1 is forward-looking and can be
   deferred.)
2. Decide which backend to land first based on the capability table and
   on which of our existing workflows we want to demonstrate it on.
3. Promote this document from "design notes" to a finished design doc by
   removing the open questions section and replacing it with the chosen
   approach for each.
