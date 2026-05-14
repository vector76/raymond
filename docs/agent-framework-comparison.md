# Raymond vs. Other Agent Systems

This document compares Raymond against four other systems that get raised in
the same conversations: **Hermes** (the Nous Research function-calling LLM
family), **Manus** (a hosted autonomous general-purpose agent), **CrewAI**
(a Python multi-agent orchestration framework), and **Sandcastle**
(`@ai-hero/sandcastle`, a TypeScript library for running coding agents in
sandboxes).

These systems are frequently lumped together as "AI agent stuff," but they sit
at different layers and solve different problems. The goal of this document
is to make those layers explicit so a reader can decide when Raymond is the
right tool, when it isn't, and where it could borrow ideas from the others.

> A note on "Hermes": the name is overloaded. In this document, *Hermes* refers
> to Nous Research's Hermes-series LLMs (Hermes 2, Hermes 3, etc.), which are
> open-weight models fine-tuned for structured tool use. If you encounter a
> different "Hermes" agent framework, the column below will not apply.

## Scope note: adjacent systems not covered

A few systems are deliberately omitted here because they would either dilute
the comparison or duplicate one of the columns:

- **LangGraph** (LangChain) — conceptually the closest of anything to Raymond:
  a graph of nodes connected by edges with conditional transitions and
  shared state. The differences are real (LangGraph is Python-first, the
  graph is built imperatively with a `StateGraph` builder, and it lacks
  Raymond's static analysis, transition-tag context discipline, and HIL
  pause-and-resume), but anyone evaluating Raymond seriously should also
  evaluate LangGraph. We do not include a column because the comparison
  warrants its own document.
- **AutoGen** (Microsoft) — multi-agent "conversation" framework where
  agents talk to each other in turns. Closer in spirit to CrewAI than to
  Raymond.
- **Goose** (Block) and **Aider** — local coding agents. These are *peers
  to Claude Code*, not orchestrators; they would sit underneath Raymond or
  Sandcastle, not next to them.
- **OpenHands** (formerly OpenDevin) and **Devin** — autonomous coding
  agents shaped like Manus, with a planner-driven loop and a sandbox. Peers
  to Manus.
- **OpenAI Swarm / AgentKit** — lightweight handoff-based multi-agent
  patterns. Closer to CrewAI in shape, smaller in scope.

Anthropic's **Computer Use** and the **Claude Agent SDK** are tools and
SDKs, not orchestrators — they would be used *by* an orchestrator, not
instead of one.

## Layer summary

The first thing to notice is that these systems do not occupy the same layer
of the stack:

| System     | What it actually is                                                      | Layer                   |
|------------|--------------------------------------------------------------------------|-------------------------|
| Raymond    | Workflow orchestrator that drives an external coding agent (Claude Code) | Orchestration / control |
| Hermes     | Open-weight LLM tuned for tool calling                                   | Model                   |
| Manus      | Hosted autonomous agent (SaaS)                                           | Product                 |
| CrewAI     | Python library for composing multi-role agents                           | Framework               |
| Sandcastle | TypeScript library for running coding agents in isolated sandboxes       | Execution / sandbox     |

Hermes is orthogonal to the others — it is a model any orchestrator could
drive. Raymond, Manus, CrewAI, and Sandcastle are all orchestrators in
different shapes: Raymond is a state-machine authoring tool, Manus is an
end-to-end product, CrewAI is a library you embed in Python, and Sandcastle is
a library focused on the execution-and-sandboxing layer beneath the agent
loop. Sandcastle and Raymond are, of the four, the most adjacent — see the
deep-dive section below.

## Capability matrix

| Dimension                   | Raymond                                                                                                     | Hermes                              | Manus                          | CrewAI                                                  |
|-----------------------------|-------------------------------------------------------------------------------------------------------------|-------------------------------------|--------------------------------|---------------------------------------------------------|
| Primary unit of composition | State (markdown prompt or shell script) with declared transitions                                           | A single tool-calling turn          | A "task" the agent decomposes  | `Agent` + `Task` + `Crew` (sequential or hierarchical)  |
| Control flow                | Author-declared; transitions are explicit tags in the prompt; `allowed_transitions` enforced                | Implicit, inside the model loop     | Implicit, planner inside agent | Author wires Tasks; Process determines order            |
| Context management          | First-class: seven transition tags differ in what context they preserve, discard, branch, or pause          | None at framework level             | Opaque, agent-managed          | Per-agent memory; hand-off via task outputs             |
| Human-in-the-loop           | Built in (`<ask>` suspends; daemon delivers input via HTTP, web UI, or MCP)                               | Not provided                        | Yes, ad-hoc                    | `human_input=True` on a Task (blocking prompt)          |
| Determinism / shell         | Shell-script states with **zero token cost** for polling, builds, data prep                                 | N/A                                 | Agent shells out, LLM-mediated | Tools are Python; orchestration still LLM-driven        |
| Multi-agent                 | `<fork>` runs independent agents in parallel within one workflow                                            | Single agent                        | Single agent (opaque internals)| Native — multiple roles, sequential or hierarchical     |
| Cross-workflow reuse        | `<call-workflow>`, `<function-workflow>`, `<fork-workflow>`, `<reset-workflow>`; skill packaging            | N/A                                 | Closed                         | Crews can be composed; no formal call-stack semantics   |
| Static analysis             | `ray lint`, `ray diagram` (Mermaid), convertible YAML form                                          | None                                | None                           | None — behavior emerges at runtime                      |
| Cost budget                 | Per-workflow dollar budget that overrides transitions when exceeded                                         | Token cost only                     | Hosted credit / quota system   | Token cost only                                         |
| Persistence / resume        | Disk-persisted state; `--resume <run_id>` after crash or human input                                        | Stateless                           | Hosted, session-scoped         | In-memory by default                                    |
| Distribution                | Go binary plus daemon; self-hosted                                                                          | Open weights (HuggingFace)          | SaaS only                      | `pip install crewai`                                    |
| Model coupling              | Wraps Claude Code; model-agnostic w.r.t. Claude tier                                                        | Is the model                        | Proprietary stack              | Provider-agnostic via adapters                          |

## Where Raymond is distinctive

Three capabilities, taken together, set Raymond apart from the other three.

### 1. Transition-tag context semantics

The single most important idea in Raymond is that *each transition tag is a
different context discipline*. The author chooses, at every state, what the
agent should remember on the way out:

- `goto` — keep the conversation history. Good for retry loops where the
  agent benefits from seeing its previous attempts.
- `reset` — discard the history. Good for crossing a phase boundary where the
  intermediate reasoning has already been captured in a file (e.g., `plan.md`)
  and dragging it forward would just inflate the context window.
- `call` — push a stack frame; the callee inherits a branch of the caller's
  context, so it can use what the caller knew.
- `function` — push a stack frame, but run the callee in **fresh context** so
  the caller's history doesn't pollute it. Use this for stateless evaluations.
- `fork` — branch the workflow into independent agents.
- `ask` — suspend until a human or external system delivers input.
- `result` — terminate the current state.

CrewAI's task hand-off is coarser (the next agent sees the previous task's
output, not its reasoning), Manus is opaque, and Hermes has no notion of this
at all because it is a model, not an orchestrator.

### 2. Shell-script states as first-class

Many "agent" steps are deterministic: poll a URL until it returns 200, run a
build, transform a JSON file, count rows. Raymond lets these run as plain
shell scripts that participate in the workflow graph but cost zero tokens.

CrewAI tools are *invoked by* the LLM, so even a trivial deterministic step
incurs an LLM round trip. Manus shells out, but always under planner
supervision. Raymond is the only one of the four that distinguishes
"deterministic step" from "LLM step" at the workflow level.

### 3. Static analysis of the workflow graph

Because workflows are declared as a graph of states with explicit
`allowed_transitions`, Raymond can:

- `ray lint` — validate the workflow without running it (catches
  unreachable states, undeclared transitions, cycles that can't terminate).
- `ray diagram` — emit a Mermaid flowchart of the state space.
- `ray convert` — collapse a directory or zip workflow into a single YAML
  file for review or distribution.

CrewAI and Manus only reveal behavior at runtime; you find out what the agent
will do by watching it do it. Raymond lets you audit the possible state space
before spending a dollar.

## Closely related: Sandcastle

Of all the systems in this document, **Sandcastle** (`@ai-hero/sandcastle`,
TypeScript, MIT) is the closest to Raymond in spirit and the most worth
studying in detail. Both orchestrate coding agents through iterative loops,
both treat completion as a string the agent emits, and both are designed for
unattended ("AFK") operation. They differ on the axis they make first-class:
Raymond owns the **state graph**, Sandcastle owns the **sandbox**.

### What Sandcastle is

A library whose entry point is `sandcastle.run({ agent, sandbox, prompt })`.
Three orthogonal abstractions:

1. **Agent provider** — `claudeCode()`, `codex()`, `opencode()`, `pi()`.
   Pluggable.
2. **Sandbox provider** — `docker()`, `podman()`, `vercel()` (Firecracker
   microVMs), `noSandbox()`, or your own via `createBindMountSandboxProvider`
   / `createIsolatedSandboxProvider`. The contract is small: `exec`, `close`,
   `copyFileIn`/`copyFileOut`, `worktreePath`.
3. **Branch strategy** — `head` (write directly to host), `merge-to-head`
   (temp branch, merged back to HEAD), or `branch` (a named branch that
   persists). Sandcastle owns the git worktree.

The iteration loop is `maxIterations` (default 1) plus an idle timeout
(default 600 s); the agent stops the loop early by emitting a
`completionSignal` (default `<promise>COMPLETE</promise>`). Prompt files
support `{{KEY}}` substitution and `` !`command` `` expansion that runs
**inside the sandbox** at prompt-resolution time, after `onSandboxReady`
hooks. Higher-level building blocks include `createSandbox()` (a long-lived
container that takes multiple `run()` calls — the canonical
"implement-then-review" pattern) and `createWorktree()` (a worktree as an
independent first-class object). Five templates ship out of the box,
including `sequential-reviewer` and `parallel-planner-with-review`.

### Side-by-side

| Axis                          | Raymond                                                                       | Sandcastle                                                              |
|-------------------------------|-------------------------------------------------------------------------------|-------------------------------------------------------------------------|
| First-class abstraction       | The state graph (what the agent does next)                                    | The sandbox (where the agent runs)                                      |
| Authoring surface             | Markdown prompts + YAML; transitions declared inline                          | TypeScript; control flow is regular JS                                  |
| Control flow                  | Declarative tags (`<goto>`, `<reset>`, `<call>`, `<function>`, `<fork>`, …)  | Imperative — `for`, `Promise.all`, `ask`                              |
| Iteration / completion        | `<goto SELF>` loop with `<result>` to terminate                               | `maxIterations` + `completionSignal` (default `<promise>COMPLETE</promise>`) |
| Context discipline            | Per-tag (reset discards, goto keeps, function fresh, etc.)                    | Per-call: `prompt`, `resumeSession`, or share a long-lived `Sandbox`    |
| Sandboxing                    | None at framework level; relies on Claude Code's permission model             | **Core feature.** Docker / Podman / Vercel / custom; bind-mount or isolated |
| Git integration               | Doesn't manage git                                                            | Owns it — worktrees, branch strategies, automatic commits, merge-back   |
| Agent providers               | Claude Code only                                                              | Claude Code, Codex, OpenCode, pi                                        |
| Multi-agent / parallel        | `<fork>` spawns independent agents in one workflow                            | `Promise.all([run(...), run(...)])` — JS handles it                     |
| Human-in-the-loop             | `<ask>` pauses an unattended workflow; daemon delivers input async         | `interactive()` opens an attended TUI session; no async pause           |
| Static analysis               | `lint`, `diagram`, YAML form                                                  | None — behavior is whatever the JS does                                 |
| Persistence / resume          | Disk-persisted workflow state; `--resume <run_id>` for crashes and HIL        | Session capture (Claude JSONL) + `resumeSession`; not workflow-level    |
| Cost control                  | Per-workflow dollar budget that overrides transitions                         | `idleTimeoutSeconds` + `maxIterations` + `AbortSignal`                  |
| Server / daemon               | `ray serve` (HTTP + MCP + web UI)                                         | None — it's a library                                                   |
| Distribution                  | Go binary plus daemon; self-hosted                                            | npm package (`@ai-hero/sandcastle`)                                     |

### What Sandcastle does that Raymond doesn't

Four things stand out as architecturally interesting:

1. **Sandbox provider abstraction.** A clean two-flavor contract (bind-mount
   vs. isolated) with five required methods. This is the piece Raymond is
   missing entirely — Raymond has no concept of "where the agent runs."
   Adopting a Sandcastle-shaped sandbox provider underneath `<fork>` and
   `<call>` would let each subagent optionally launch in its own container
   without changing state-graph semantics; it's purely an executor swap.

2. **Branch strategies.** `head` / `merge-to-head` / `branch` is a small,
   complete taxonomy for "where do the agent's commits land?" Raymond
   currently has no opinion: the agent commits or doesn't, on whatever branch
   it happens to be on. Adopting branch strategies (especially per-`<fork>`)
   would make parallel agents safer by default.

3. **Cloud sandbox via Firecracker microVMs (`vercel()`).** This is what
   makes Sandcastle viable at fleet scale — the host doesn't need a local
   Docker daemon. Raymond's `serve` mode would benefit from the same
   property if it ever wants to run untrusted workflows for multiple users.

4. **`` !`command` `` prompt expansion run inside the sandbox.** Sandcastle
   prompts can pull dynamic context (`` !`gh issue list` ``) at
   prompt-resolution time, in the sandbox where the agent will see the same
   state. Raymond approximates this with shell-script states, but
   Sandcastle's variant is more ergonomic when the goal is just "inject this
   into the next prompt."

### What Raymond does that Sandcastle doesn't

The two systems are not the same product:

- **Long-running, multi-phase workflows.** Sandcastle's `maxIterations` is a
  single tight loop; reviewing-then-implementing-then-testing requires
  multiple `run()` calls in JS. Raymond's state graph models that natively,
  including fan-out / fan-in across phases.
- **Human-in-the-loop as async pause.** Sandcastle's `interactive()` is an
  attended TUI; there is no equivalent of `<ask>` where the agent suspends
  to disk and resumes when input arrives via API hours later.
- **Static analysis, cross-workflow reuse, skill packaging.** None of this
  exists in Sandcastle — equivalent reuse would mean writing JS modules and
  importing them.
- **Daemon with HTTP / MCP / web UI.** Sandcastle is a library, not a
  service.

### Convergent vocabulary

Several pieces of the two systems map onto each other almost one-to-one,
which is why the "flavor" feels similar:

| Sandcastle                           | Raymond                                |
|--------------------------------------|----------------------------------------|
| `<promise>COMPLETE</promise>`        | `<result>`                             |
| `maxIterations` loop                 | `<goto SELF>` self-loop                |
| `completionSignal`                   | `<result>` terminating a self-loop     |
| `{{KEY}}` placeholder substitution   | `{{input}}` and prompt-arg injection  |
| `resumeSession` (Claude JSONL)       | `--resume <run_id>` (workflow state)   |
| `interactive()` TUI                  | (no equivalent — Raymond is unattended)|
| `<fork>`-shaped parallelism          | `Promise.all([run(...), run(...)])`    |
| Templates (`parallel-planner`, etc.) | Example workflows under `workflows/`   |

### Positioning

Sandcastle and Raymond are **complementary, not competitive**. Sandcastle is
"the sandbox layer with a thin run-loop on top"; Raymond is "the workflow
layer assuming an executor exists." A future Raymond could plausibly use
Sandcastle (or a Sandcastle-shaped sandbox-provider abstraction) as its
execution backend — Raymond's state graph would compose naturally above
Sandcastle's `run()` and `createSandbox()`, with each Raymond state
delegating to a Sandcastle invocation.

## Token economy and context cost

LLM calls cost real money, and the cost is roughly linear in context length.
How a system manages cumulative context across steps is therefore one of the
most consequential design choices it makes. The five systems take very
different stances:

| System     | Default behavior across steps                                                       | Author's lever to shrink context                                            |
|------------|-------------------------------------------------------------------------------------|-----------------------------------------------------------------------------|
| Raymond    | Per-tag: `goto` keeps history; `reset`, `function`, `<fork>` start fresh            | Choose the transition tag; offload reasoning to files; use shell states for deterministic steps at zero token cost |
| Sandcastle | Each `run()` is a fresh prompt; `resumeSession` rehydrates a Claude JSONL on demand | Decide whether to share a long-lived `Sandbox`, start a new one, or `resumeSession` |
| CrewAI     | Agents accumulate context across their tasks; memory is a separate opt-in feature   | Configure per-agent memory; choose `Process` topology; manually trim outputs |
| Manus      | Opaque — managed by the planner                                                     | None exposed                                                                |
| Hermes     | N/A — Hermes is a model, not an orchestrator                                        | N/A                                                                         |

Two observations worth pulling out:

- **Raymond is the only system that exposes context discipline as the
  primary authoring decision.** Every transition tag is, in part, a
  declaration of how much history to carry forward. This is why Raymond
  workflows can run for many phases without the context window becoming the
  binding constraint.
- **Shell-script states give Raymond a zero-token escape hatch** that none
  of the others provide. CrewAI tools are LLM-invoked; Sandcastle's
  `` !`command` `` runs at prompt-resolution time but the result is still
  injected into an LLM prompt; Manus shells out under planner supervision.
  Only Raymond lets a deterministic step skip the model entirely.

## Observability

What can the operator see while a run is in progress, and what is preserved
after it ends?

| System     | During run                                                                                | After run                                                            |
|------------|-------------------------------------------------------------------------------------------|----------------------------------------------------------------------|
| Raymond    | State transition log; web UI under `ray serve`; lint and Mermaid diagram available *before* the run | Persisted run state on disk (resumable); per-state input/output captured |
| Sandcastle | `onAgentStreamEvent` callback (text and tool-call chunks); file or stdout logging         | Captured Claude session JSONL; `RunResult` with iterations, commits, usage |
| CrewAI     | Per-agent / per-task callbacks; verbose logging                                           | In-memory results; persistence requires user code                    |
| Manus      | Hosted web UI showing the planner's narration                                             | Session log in the product; not exportable as structured data        |
| Hermes     | N/A                                                                                       | N/A                                                                  |

Raymond's distinctive property here is the **pre-run** observability:
because workflows are static graphs, `ray lint` and `ray diagram`
let the operator inspect the *space of possible behaviors* before any LLM
call. Sandcastle and CrewAI only let you inspect actual runs.

## Failure modes and blast-radius control

What happens when an agent gets stuck, loops forever, exceeds its budget,
or corrupts the working directory?

| System     | Stuck / runaway loop                                                            | Cost overrun                                       | Blast radius if the agent goes wrong              |
|------------|---------------------------------------------------------------------------------|----------------------------------------------------|---------------------------------------------------|
| Raymond    | `allowed_transitions` constrains exits; `lint` flags non-terminating cycles; budget cap forces termination | Per-workflow dollar budget overrides transitions and ends the run | Whatever the underlying Claude Code session permits — Raymond does not isolate the host |
| Sandcastle | `idleTimeoutSeconds` (default 600); `maxIterations`; `completionSignal`; `AbortSignal` cancels mid-flight | Idle timeout + iteration cap; no native dollar budget | Container or microVM contains filesystem and network damage; `merge-to-head` strategy isolates branches |
| CrewAI     | Per-agent `max_iter` / `max_rpm`; otherwise the LLM decides when to stop        | Token-cost only, manually capped                   | Runs in-process; tools have full host access      |
| Manus      | Planner decides; quota stops it eventually                                      | Hosted credit / quota                              | Hosted VM contains it; opaque to operator         |
| Hermes     | N/A — single turn                                                               | N/A                                                | N/A                                               |

The asymmetry to take seriously: **Raymond has the strongest cost discipline
but the weakest blast-radius control; Sandcastle is the inverse.** Raymond
will reliably stop spending your money but will happily let an agent rewrite
your home directory. Sandcastle will contain a malicious or buggy agent to
its container but has no native concept of "you have spent too much."
Combining the two gets you both properties.

## Where the others win

Raymond is not the right tool for every problem. Each of the other systems
beats it on at least one axis.

**Manus** wins on time-to-first-result. The user types a goal in English, the
agent figures out the rest. Raymond requires the author to write a workflow,
which is a meaningful upfront cost. For one-shot ambiguous tasks ("research
this topic and produce a report"), Manus is a better fit.

**CrewAI** wins on multi-role modeling. CrewAI's "researcher / writer / critic"
pattern, with each agent having its own backstory, goal, and tool set, is well
beyond what Raymond's `<fork>` provides — `<fork>` is parallelism, not
role-played collaboration. If the problem is naturally framed as a small team
of specialists, CrewAI's abstractions match.

**Hermes** wins on portability of tool-call behavior. It is an open-weight
model you can run yourself; it is not in competition with Raymond at all.
Indeed, you could imagine Raymond driving an underlying agent backed by a
Hermes-tuned model rather than Claude.

**Sandcastle** wins on sandboxing and git lifecycle. It runs the agent in
Docker, Podman, or a Firecracker microVM by default; it owns the worktree
and the branch strategy; it handles commit accumulation and merge-back. Any
of those properties would be a major undertaking to add to Raymond, and they
are essentially free in Sandcastle. It also supports more agent providers
out of the box (Claude Code, Codex, OpenCode, pi).

## Where Raymond is weakest

Honest accounting:

- **No sandboxing.** Raymond runs the agent wherever the user runs Raymond.
  There is no container layer, no branch isolation, no merge-back. Sandcastle
  shows what a clean sandbox-provider abstraction looks like, and Raymond
  doesn't have one.
- **Authoring cost.** Workflows are real artifacts that have to be written,
  reviewed, and maintained. For ad-hoc or exploratory tasks this is overhead.
- **Coding-agent bias and provider lock-in.** Raymond is shaped around Claude
  Code as the executor. The orchestration model would generalize, but the
  current implementation does not provide adapters for arbitrary LLM agents
  the way CrewAI or Sandcastle do.
- **No role / persona modeling.** Forked agents are anonymous workers, not
  characters with goals and tools.
- **No built-in tool catalog.** CrewAI ships with a large library of tools
  (search, scraping, file I/O, vector stores). Raymond expects the underlying
  coding agent to bring its own tools.

## When to reach for which

A rough decision guide:

- **Use Manus** when you have a one-shot, ambiguous goal and you don't care
  how the work happens, only that it happens.
- **Use CrewAI** when the problem decomposes naturally into a small team of
  specialists collaborating on a structured artifact (a report, a plan, a
  piece of code reviewed by a critic).
- **Use Sandcastle** when the priority is running an agent (or a few agents)
  in a sandbox with proper git hygiene — Docker / Podman / Firecracker
  isolation, named branches, automatic merge-back — and the orchestration
  itself is simple enough to express in JS. It is the right tool for "fan
  out N agents over N issues, each on its own branch, in its own container,
  and merge the results."
- **Use Raymond** when you have a *recurring* multi-phase workflow — a coding
  task with planning, implementation, testing, and review phases; a
  long-running loop with human checkpoints; a process where context discipline
  matters because steps are expensive — and you want the workflow itself to
  be a reviewable, version-controlled artifact.
- **Use Hermes** as a model in any of the above, if you are self-hosting and
  want strong tool-calling behavior without proprietary weights.

A pragmatic combination: Sandcastle underneath, Raymond on top. Sandcastle
handles the sandbox + branch lifecycle; Raymond handles the multi-phase
workflow, the context discipline, and the human-in-the-loop. Neither system
supports this directly today, but the layering is natural.

## One-line positioning

Raymond is closer to **"Make / Airflow for coding agents"** than to a
multi-agent framework. The workflow author declares the state graph and the
context rules; the agent executes within that envelope. Manus and CrewAI move
authoring effort onto the LLM at runtime; Sandcastle moves it into JS;
Raymond moves it into a statically-checkable artifact that lives in git.
