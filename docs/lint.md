# raymond lint

`raymond lint` statically analyzes a workflow directory or zip archive and
reports issues as diagnostics with severity levels: **error**, **warning**, and
**info**.

Errors indicate problems that will cause a workflow to fail or behave
incorrectly at runtime. Warnings flag likely mistakes or dead code. Info
messages highlight non-obvious behavior that is worth knowing about.

---

## Usage

```
raymond lint <path>
```

`<path>` is either a workflow directory or a `.zip` archive path.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--win` | false | Windows platform mode — resolves extensionless state names against `.bat`/`.ps1` instead of `.sh` |
| `--json` | false | Emit diagnostics as a JSON array instead of human-readable text |
| `--level <info\|warning\|error>` | `warning` | Minimum severity to display. `info` shows everything; `warning` shows warnings and errors; `error` shows only errors. The summary line always counts all diagnostics regardless of this filter. |

### Examples

```sh
# Lint a workflow directory (default output, warnings and above)
raymond lint ./my-workflow/

# Lint with all info messages visible
raymond lint --level info ./my-workflow/

# Show only errors
raymond lint --level error ./my-workflow/

# Windows platform mode
raymond lint --win ./my-workflow/

# JSON output (useful for tooling integration)
raymond lint --json ./my-workflow/

# JSON output, errors only
raymond lint --json --level error ./my-workflow/
```

---

## Exit codes

| Code | Condition |
|------|-----------|
| `0` | Lint ran successfully and no error-severity diagnostics were found (warnings and info may be present) |
| `1` | One or more error-severity diagnostics were found, **or** the path could not be accessed |

---

## Output formats

### Text (default)

One line per diagnostic, in the form `<severity>: <message>`, followed by a
summary line. Diagnostics are sorted: errors first, then warnings, then info;
within each severity group, by filename ascending, then check name ascending.

Example:

```
error: no entry point found: workflow must contain 1_START or START (with .md, .sh, .bat, or .ps1 extension)
error: <goto> in 10_FETCH.md references "MISSING" which does not exist in this workflow
warning: 20_PROCESS.md: allowed_transitions lists target "RETRY" which is not mentioned in the prompt body
warning: 30_CLEANUP.md is unreachable: no transitions lead to this state
2 errors, 2 warnings
```

When there are no issues: `No issues found.`

The summary counts all diagnostics regardless of the `--level` filter. If
`--level error` is set, warning/info lines are suppressed but the summary still
reflects the total.

### JSON (`--json`)

A JSON array. Each element is an object with four string fields:

| Field | Description |
|-------|-------------|
| `severity` | `"error"`, `"warning"`, or `"info"` |
| `file` | Relative filename within the workflow (empty string `""` for workflow-level checks such as `no-entry-point`) |
| `message` | Human-readable description of the issue |
| `check` | Check ID string (see table below) |

Example:

```json
[
  {"severity":"error","file":"10_FETCH.md","message":"\u003cgoto\u003e in 10_FETCH.md references \"MISSING\" which does not exist in this workflow","check":"missing-target"},
  {"severity":"warning","file":"30_CLEANUP.md","message":"30_CLEANUP.md is unreachable: no transitions lead to this state","check":"unreachable-state"}
]
```

The `--level` filter applies: only diagnostics at or above the requested
severity appear in the array.

---

## Lint checks reference

### Errors

| Check ID | What triggers it |
|----------|-----------------|
| `frontmatter-parse-error` | A state file contains a YAML frontmatter block that cannot be parsed. |
| `invalid-model` | A state file's frontmatter specifies a `model` value that is not one of `opus`, `sonnet`, `haiku`. |
| `invalid-effort` | A state file's frontmatter specifies an `effort` value that is not one of `low`, `medium`, `high`. |
| `missing-target` | A `<goto>`, `<reset>`, `<call>`, `<function>`, or `<fork>` tag references a state that does not exist in the workflow. Cross-workflow tags are excluded (see below). |
| `missing-return` | A `<call>` or `<function>` tag (non-cross-workflow) is missing a `return` attribute, or the `return` attribute references a state that does not exist. |
| `missing-fork-next` | A state contains one or more `<fork>` tags, none of them have a `next` attribute, and there is no `<goto>` tag in the same state — meaning the parent agent has no continuation after the fork. |
| `ambiguous-state-resolution` | An extensionless transition target (e.g., `<goto>REVIEW</goto>`) matches both a `.md` file and a platform script file (`.sh` on Linux/macOS, `.bat`/`.ps1` on Windows). The runtime would fail to resolve it. |
| `ambiguous-entry-point` | Both `1_START` and `START` exist in the workflow directory, making the entry point ambiguous. |
| `no-entry-point` | No entry point file (`1_START` or `START`, with `.md`, `.sh`, `.bat`, or `.ps1` extension) was found in the workflow. |

### Warnings

| Check ID | What triggers it |
|----------|-----------------|
| `fork-next-mismatch` | A state has two or more `<fork>` tags with non-empty `next` attributes that disagree with each other. All forks in a state should send the parent to the same continuation. |
| `unused-allowed-transition` | A state's `allowed_transitions` policy lists a target (excluding `result`) that does not appear anywhere in the prompt body text. Only fires when there are **two or more** allowed transitions; a single allowed transition is implicit and is not expected to be named in the prompt. |
| `unreachable-state` | No transition path from the entry point leads to this state. The state can never be visited during a workflow run. |
| `dead-end-state` | A `.md` state file contains no outgoing transitions at all (no `<goto>`, `<result>`, `<call>`, etc.). An agent running this state would have no valid move. Script states (`.sh`, `.bat`, `.ps1`) are excluded because their transitions are determined at runtime. |
| `call-without-result-path` | A `<call>` or `<function>` tag references a callee state that is present in this workflow, but no state reachable from the callee (via `<goto>`/`<reset>` edges) emits `<result>`. The call would therefore never return. |

### Info

| Check ID | What triggers it |
|----------|-----------------|
| `implicit-transition` | A state has exactly one `allowed_transitions` entry with a concrete target (or, for `<result>`, a fixed payload). The agent does not need to emit the tag explicitly — the runtime applies it automatically. This is informational: the workflow is correct as-is. |
| `script-state-no-static-analysis` | The state is a script file (`.sh`, `.bat`, `.ps1`). Its transitions are determined at runtime by the script's exit value or output, so lint cannot fully validate them statically. |

---

## Cross-workflow tags

The tags `<call-workflow>`, `<function-workflow>`, `<fork-workflow>`, and
`<reset-workflow>` are intentionally **excluded** from `missing-target` and
`missing-return` validation. Their targets reference external workflows that are
not present in the current workflow directory and therefore cannot be validated
statically. Lint skips these tags rather than producing false-positive errors.

---

## ZIP archive support

`raymond lint` accepts both directory paths and `.zip` archive paths. When a
zip path is given:

1. **Hash validation** runs first: the SHA-256 hash embedded in the filename is
   verified against the archive contents. Failure is a hard error; no lint
   checks run.
2. **Layout detection** runs next: the archive must have a valid layout (flat
   or single-folder). An invalid layout is also a hard error.
3. If both checks pass, lint proceeds normally against the contents of the
   archive.
