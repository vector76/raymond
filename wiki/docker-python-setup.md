# Docker Python Package Setup

## Problem: Externally-Managed Python Environment

Modern Debian/Ubuntu-based Docker images use PEP 668 to protect the system Python installation. This prevents direct `pip install` commands from modifying system packages, showing the error:

```
error: externally-managed-environment
```

This is normally good - it prevents breaking system tools that depend on Python. However, in a Docker container, we need a way to install development packages.

## Solution: Install with --break-system-packages

For Docker containers (which are disposable and rebuildable), using `--break-system-packages` is a pragmatic solution:

```bash
cd ~/work/raymond
pip install --break-system-packages -e .
pip install --break-system-packages -r requirements.txt
```

### Why This Works in Docker

1. **Isolation**: The container is isolated from your host system
2. **Disposable**: If something breaks, rebuild the container
3. **Not production**: Development containers can be less strict
4. **No venv tools**: The container may not have `python3-venv` installed

### What Gets Installed

- Package installed to `~/.local/bin/` (user directory, not system-wide)
- Entry point scripts created based on `pyproject.toml` `[project.scripts]` section
- Commands become globally available (if `~/.local/bin` is in PATH)

## Step-by-Step Setup

### 1. Install the Package in Editable Mode

```bash
cd ~/work/raymond
pip install --break-system-packages -e .
```

**What `-e` (editable) means:**
- Creates a link to your source code instead of copying it
- Changes to your code take effect immediately
- Perfect for development workflows

### 2. Install Dependencies

```bash
pip install --break-system-packages -r requirements.txt
```

### 3. Verify Installation

```bash
which raymond  # Should show: /home/devuser/.local/bin/raymond
raymond --help # Should show the help message
```

## Key Python Packaging Concepts

### What is a Python Package?

A **package** is bundled Python code that can be shared and reused. Your project becomes a package when it has:
- A `pyproject.toml` file (defines metadata and entry points)
- Source code in a structured directory (like `src/`)

### Installing vs Editable Installing

| Normal Install | Editable Install (`-e`) |
|---------------|------------------------|
| Copies code to Python's package directory | Creates a link to your source code |
| Changes require reinstalling | Changes take effect immediately |
| Good for using packages | Good for developing packages |

### Virtual Environments

A **virtual environment** is an isolated Python installation with its own packages. Think of it as a separate Python world for each project.

**Why you usually need one:**
- Projects have different dependency requirements
- Prevents conflicts between package versions
- Keeps system Python clean

**Why we didn't need one here:**
- Docker container is already isolated
- We used `--break-system-packages` to bypass the restriction
- Container is disposable (breaks don't matter)

## Alternative: Rebuild Container with venv Support

If you prefer the "proper" way, add to your Dockerfile:

```dockerfile
RUN apt-get update && apt-get install -y python3-venv
```

Then use a virtual environment:

```bash
cd ~/work/raymond
python3 -m venv .venv
source .venv/bin/activate
pip install -e .
pip install -r requirements.txt
```

**Pros:** Cleaner, more isolated, no system packages warning
**Cons:** Requires rebuilding container, need to activate venv each time

## Entry Points Configuration

The `pyproject.toml` defines which commands get created:

```toml
[project.scripts]
raymond = "src.cli:main"
ray = "src.cli:main"
```

This creates two commands:
- `raymond` → runs the `main()` function from `src/cli.py`
- `ray` → shorter alias for the same function

When you run `raymond`, Python:
1. Finds the script in `~/.local/bin/raymond`
2. Imports `src.cli`
3. Calls the `main()` function
4. Returns the exit code

## Troubleshooting

### Command not found after install

Check if `~/.local/bin` is in your PATH:

```bash
echo $PATH | grep ".local/bin"
```

If not, add to `~/.bashrc`:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

### ModuleNotFoundError after install

Dependencies not installed. Run:

```bash
pip install --break-system-packages -r requirements.txt
```

### Changes to code not taking effect

1. Verify editable install: `pip show raymond` should show location pointing to your source
2. Reinstall in editable mode: `pip install --break-system-packages -e .`
3. Check you're editing the right files (not accidentally in a copy)

## Summary

For Docker development containers:
- Use `--break-system-packages` to bypass PEP 668 restrictions
- Install in editable mode (`-e`) so code changes take effect immediately
- Install to user directory (`~/.local`) for isolation
- This approach is pragmatic and appropriate for disposable containers
