# Example: Using the Effort Feature

## Command Line Usage

### Start a workflow with default effort level:
```bash
raymond workflow.md --effort medium
```

### Start with high effort (for complex tasks):
```bash
raymond workflow.md --effort high
```

### Start with low effort (for simple tasks):
```bash
raymond workflow.md --effort low
```

## Frontmatter Override

Create a prompt file with effort specified in frontmatter:

```markdown
---
effort: high
allowed_transitions:
  - tag: goto
    target: next.md
---

This prompt will use HIGH effort level regardless of CLI default.
```

## Configuration File

Add to `.raymond/config.toml`:

```toml
[raymond]
# Default effort level for all workflows
effort = "medium"
```

## Priority Order

1. **Frontmatter** (`effort:` in YAML) - highest priority
2. **CLI argument** (`--effort high`) - overrides config file
3. **Config file** (`effort = "medium"`) - overrides nothing (default)
4. **None** - Claude's default behavior

## Valid Values

- `low` - Faster responses, less extended thinking
- `medium` - Balanced approach
- `high` - More extended thinking, better for complex tasks

## Notes

- Only applies to markdown (`.md`) states
- Scripts (`.sh`, `.bat`) don't use Claude Code, so effort is ignored
- Invalid values are logged as warnings but passed to Claude Code
