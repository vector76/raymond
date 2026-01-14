# Implementation Assumptions

This document records assumptions made during implementation where multiple
reasonable choices existed. These can be revisited if they prove problematic.

## State File Location

**Assumption:** State files live in `.raymond/workflows/` relative to the
project root.

**Rationale:** Dot-prefix keeps it hidden from casual directory listings.
Dedicated `workflows/` subdirectory keeps state files separate from any future
config files.

**Alternative considered:** XDG-style config directories, or alongside prompt
files.

## Prompt File Location

**Assumption:** Prompt files live in `workflows/` (without dot prefix) relative
to project root.

**Rationale:** These are user-authored content that should be version
controlled and easily browsable. Separate from state files which are runtime
artifacts.

**Alternative considered:** Same directory as state files, or configurable
location.

## Transition Tag Format

**Assumption:** Use distinct tags for each transition type:
- `<goto>FILE.md</goto>` - Pattern 3 (resume/continue in same context)
- `<reset>FILE.md</reset>` - Pattern 4 (discard context, start fresh, continue)
- `<function>FILE.md</function>` - Pattern 1 (stateless/pure, disposable)
- `<call return="NEXT.md">CHILD.md</call>` - Pattern 2 (call with return)
- `<fork item="data">WORKER.md</fork>` - Independent spawn

The `<call>` tag requires a `return` attribute specifying which state to
resume at when the child completes. The `<fork>` tag accepts arbitrary
attributes that become metadata for the spawned workflow.

**Rationale:** Distinct tag names are self-documenting. `<goto>` clearly
indicates same-context continuation. `<reset>` indicates intentional context
clearing. `<call>` evokes subroutine semantics. `<function>` suggests stateless
evaluation. `<fork>` matches Unix fork() semantics (independent child process).

**Alternative considered:** Single `<transition>` tag with pattern attribute,
naming conventions, YAML frontmatter.

**Note on first invocation:** The very first invocation of a workflow has no
session to resume. The orchestrator treats this as an implicit fresh start.
Subsequent `<goto>` tags resume the existing session. `<reset>` explicitly
creates a new session, discarding the current one.

**Future consideration:** A `<compact>` tag could perform context summarization
rather than full discard - partially preserving context while reducing token
usage. However, this is deferred; the philosophy is to avoid context overflow
in the first place through intentional `<reset>` at phase boundaries.

## Default Model

**Assumption:** Use the default Claude Code model (currently Sonnet) unless
explicitly specified. The `<function>` tag supports an optional `model`
attribute: `<function model="haiku">EVAL.md</function>`

**Rationale:** Let Claude Code manage model selection by default. Override only
when cost/speed tradeoffs matter (evaluators). The `model` attribute on
`<function>` makes this explicit in the prompt where the decision is made.

**Alternative considered:** Always specify model, configure per-workflow, or
use naming conventions.

## Template Substitution

**Assumption:** Prompt files support `{{variable}}` placeholders that the
orchestrator substitutes before sending to Claude Code. Variables come from:
- Transition tag attributes (e.g., `<fork item="X">` makes `{{item}}` available)
- Workflow metadata in the state file
- Result tags from child workflows (for `<call>` returns)

**Rationale:** Simple, familiar syntax (Mustache/Jinja-style). Allows prompt
files to be reusable templates while keeping them as plain markdown.

**Initial implementation:** Simple regex-based substitution for `{{key}}`.

**Future enhancement:** LLM-based substitution. Instead of regex, invoke a
`<function>` that instructs the LLM to identify all `{{placeholders}}` in the
template and fill them in with "what makes sense" given the available context.
The LLM returns the completed prompt. Benefits:
- More robust than regex (handles edge cases, escaping, nested structures)
- Can infer missing values or adapt phrasing contextually
- Substitution runs in isolated context (doesn't pollute main workflow)

This is deferred to avoid debugging complexity during initial implementation.

**Alternative considered:** Environment variables, separate config files,
runtime prompt construction in Python.

## Session ID Format

**Assumption:** Use whatever session ID format Claude Code provides via
`--resume` / `--fork` flags. Store it opaquely in the state file.

**Rationale:** Don't couple to Claude Code internals. Treat session ID as an
opaque string.

## Result Extraction

**Assumption:** Child workflows signal their result using a `<result>` tag:
`<result>Summary of what was accomplished</result>`

The prompt for child workflows (invoked via `<call>`) should instruct the AI
to include this tag.
The orchestrator extracts it and passes it to the parent on resume.

**Rationale:** Consistent with the transition tag pattern. Explicit is better
than trying to summarize arbitrary output.

**Alternative considered:** Last paragraph, AI-generated summary, or structured
JSON output.

## Error Handling Strategy

**Assumption:** On Claude Code failure:
1. Log the error
2. Keep the workflow in current state (don't advance)
3. Increment a retry counter in state file
4. After N retries (default 3), mark workflow as failed

**Rationale:** Simple retry logic handles transient failures. Persistent
failures need human attention.

**Alternative considered:** Immediate failure, exponential backoff, error
recovery prompts.

## Workflow ID Generation

**Assumption:** Generate workflow IDs as `{descriptive-prefix}-{uuid4-short}`,
e.g., `issue-195-a1b2c3d4`.

**Rationale:** Human-readable prefix aids debugging. UUID suffix ensures
uniqueness.

**Alternative considered:** Pure UUID, timestamp-based, or sequential numbers.

## Concurrent Workflow Limit

**Assumption:** No hard limit on concurrent workflows initially. The natural
limit is API rate limits and system resources.

**Rationale:** Start simple. Add limits if problems emerge.

**Alternative considered:** Configurable semaphore limiting concurrent
invocations.

## Iteration Limits

**Assumption:** Iteration limits are specified in the state file when the
workflow is started, e.g., `{"max_iterations": 5}`. The orchestrator tracks
`iteration_count` in the state file and overrides transitions when the limit
is reached.

**Rationale:** Keeps limits external to the prompt files (which don't change
between runs). Allows the same prompt to be used with different limits.

**Alternative considered:** Limits in prompt file frontmatter, or as attributes
on transition tags.

## Logging Format

**Assumption:** Use Python's standard logging module with structured messages.
Log to stderr by default, with optional file output.

**Rationale:** Standard tooling, easy to integrate with existing infrastructure.

## State File Atomicity

**Assumption:** Write state files atomically using write-to-temp-then-rename
pattern.

**Rationale:** Prevents corrupted state files from partial writes during crash.

## Prompt File Encoding

**Assumption:** All prompt files are UTF-8 encoded plain text (markdown).

**Rationale:** Universal standard, matches Claude Code expectations.
