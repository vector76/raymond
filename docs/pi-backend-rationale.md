# Alternative Backend Strategy: pi Only

This document records raymond's strategy for supporting agent backends other
than Claude Code. The short version: **pi is the sole planned alternative.
All other backends (codex, gemini, cursor, copilot) are postponed
indefinitely.** This file exists to capture the reasoning so future
contributors don't have to rederive it.

For the feature specification of the pi backend itself, see
[pi-backend.md](pi-backend.md). For the surrounding context, see
[code-structure.md](code-structure.md) and [yaml-workflows.md](yaml-workflows.md).

## Why pi, and only pi

Two principles drive the choice.

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

## What was considered

Prior versions of this document surveyed five candidate backends — Codex
CLI, Gemini CLI, Cursor CLI, pi, and GitHub Copilot CLI — with a
capability comparison table covering one-shot mode, stream-JSON, system
prompts, auto-approve, session resume, continue-and-fork, cost
reporting, and working-directory handling. That survey is in the git
history of this file if it is ever needed; it is not reproduced here
because the strategy decision above renders it moot. Git history also
preserves the corresponding (now-deleted) `copilot-backend.md` feature
spec.

## Scope

In scope:

- Adding pi as a second backend behind a small abstraction in the
  current Claude-only execution path.
- Workflow-level `backend:` declaration in the manifest (Claude Code
  remains the default when absent).
- Mapping per-state knobs (`model`, `effort`, `allowed_transitions`,
  timeout) and the tool disallow list onto pi's equivalents, degrading
  cleanly where pi has no direct equivalent.

Out of scope, now and for the foreseeable future:

- Per-state backend switching. A workflow that genuinely needs both
  Claude and pi composes them via cross-workflow invocation
  (see [cross-workflow-design.md](cross-workflow-design.md)).
- Any other backend (codex, gemini, cursor, copilot) as a first-class
  raymond integration. Workflows that want those providers should use
  pi with the appropriate `--provider` setting.

## Existing Claude Code integration (what gets abstracted)

The current integration is narrow and well-isolated, so introducing a
backend interface for pi should not require invasive changes:

- **Command construction:** `internal/ccwrap/ccwrap.go` —
  `BuildClaudeCommand` (lines 92-141) assembles flags. The binary name
  `claude` is held in the `claudeExe` package variable (line 50,
  overridable in tests). `BuildClaudeEnv` (lines 71-83) strips
  `CLAUDECODE` to prevent nested sessions, and `InvokeStream` / `Invoke`
  (lines 177-199, 356-396) wrap execution. Flags assembled today:
  `--output-format stream-json`, `-p`, `--permission-mode acceptEdits`
  or `--dangerously-skip-permissions`, `--model`, `--effort`,
  `--resume <id>` or `-c --fork-session`, `--disallowed-tools`
  (hardcoded: `EnterPlanMode`, `ExitPlanMode`, `AskUserQuestion`,
  `NotebookEdit`), and the prompt after `--`.

- **Stream parsing:** `internal/executors/markdown.go:485-593`
  (`processStreamForConsole`) parses the Claude-shaped stream-JSON.
  Message types handled: `assistant` (text and `tool_use` items),
  `user` (tool results, with `is_error` driving ErrorOccurred events),
  and `result` (final, with `is_error` driving usage-limit detection).
  Cost is read from `total_cost_usd`
  (`internal/executors/executors.go:191-210`); tokens are summed from
  `usage.cache_creation_input_tokens`, `cache_read_input_tokens`, and
  `input_tokens` (`internal/executors/executors.go:213-246`). Session
  ID is read from `session_id` or `metadata.session_id`.

- **Per-state agent config:** `internal/policy/policy.go:31-35` defines
  `Policy` with `AllowedTransitions`, `Model`, `Effort`. Per-workflow
  launch defaults live in `internal/state/state.go:122-129` as
  `LaunchParams` (`DangerouslySkipPermissions`, `Model`, `Effort`,
  `Timeout`, `ContinueAndFork`, `OnAsk`).

- **Executor abstraction:** `internal/executors/executors.go:99-106`
  already defines a `StateExecutor` interface with two implementations
  (`MarkdownExecutor`, `ScriptExecutor`). Selection is by file
  extension (`GetExecutor`, line 117). There is **no** backend-level
  abstraction below this — the markdown executor calls `ccwrap`
  directly. The new pi-vs-Claude split would sit between
  `MarkdownExecutor` and the backend-specific command/parse code.

- **Manifest:** `internal/manifest/manifest.go:24-33` defines the
  `Manifest` struct. This is where the `backend:` field lands.

## Resolved questions

The original five-candidate design notes left eight open questions.
Most resolve automatically under the pi-only strategy because pi has
session resume, fork, system-prompt flags, and stream-JSON. The
remainder are resolved in [pi-backend.md](pi-backend.md):

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

See [pi-backend.md](pi-backend.md) for the full feature surface.
