# Code Organization Structure

## Overview

This document defines the code organization for the raymond project.

## Project Structure

```
raymond/
├── README.md               # Project overview and quick start
├── main.py                 # Entry point launcher (delegates to src.cli)
├── src/                    # All source code
│   ├── __init__.py
│   ├── cli.py             # Command-line interface (argparse)
│   ├── orchestrator.py    # Main orchestration logic
│   ├── cc_wrap.py         # Claude code wrapper
│   └── ...                # Additional modules as needed
│
├── tests/                  # Test files (mirrors src/ structure)
│   ├── __init__.py
│   ├── conftest.py        # Pytest configuration and fixtures
│   ├── test_cc_wrap.py
│   └── ...                # Test file per source module
│
├── docs/                   # Architecture and design documentation
│
├── .gitignore
├── requirements.txt        # Production dependencies
├── AGENTS.md              # Agent instructions
└── CLAUDE.md              # Copy of AGENTS.md (keep synchronized)
```

## Key Principles

### Separation of Concerns
- **`src/`**: All production code
- **`tests/`**: All test code, mirroring source structure
- **`docs/`**: Architecture and design documentation
- **Root**: Configuration files, entry point (`main.py`), and project-level docs

### Test Organization
- Each module `src/foo.py` has a corresponding `tests/test_foo.py`
- Use `conftest.py` for shared fixtures and path setup
- Run tests with `pytest` or `pytest tests/`

### Package Structure
- `src/` is a Python package (with `__init__.py`)
- **Current test setup**: `tests/conftest.py` adds `src/` to the import path so tests can import modules like `cc_wrap` directly.
- **Runtime entrypoints**: root `main.py` runs `src.main` as a module (via `runpy`) so runtime imports work without `sys.path` manipulation.
- Within `src/`, modules use **relative imports** (e.g., `from .cc_wrap import ...`) to behave well when run as a package.

**Note on IDE support:** The current `sys.path` approach in tests is pragmatic
and matches the repository today, but some IDEs may provide better intellisense
with an installable package / editable install. Treat this as a future
refinement rather than a hard requirement.

## Testing

- **Test files**: One test file per source module (1:1 mapping)
- **Naming**: `test_<module_name>.py`
- **Framework**: pytest with pytest-asyncio for async functions
- **Fixtures**: Shared fixtures go in `conftest.py`

## Import Patterns

### In tests (via conftest.py)
```python
# tests/conftest.py
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

# tests/test_cc_wrap.py
from cc_wrap import wrap_claude_code
```

### Within src/ (use relative imports)
```python
# src/main.py
from .cc_wrap import wrap_claude_code_stream

# src/orchestrator.py
from .cc_wrap import wrap_claude_code
from .models import SomeModel
```

### Root main.py (launcher)
```python
# main.py - Simple launcher that runs the CLI
import sys
from src.cli import main

if __name__ == "__main__":
    sys.exit(main())
```

**Why this approach?**
- No `sys.path` manipulation needed
- IDE can resolve imports statically (ctrl-click works)
- Clean package structure with proper relative imports
- Can also run directly: `python -m src.cli`

## Virtual Environment

```bash
python -m venv .venv
.venv\Scripts\activate  # Windows
source .venv/bin/activate  # Linux/Mac
```

## Running the Application

```bash
# Run via installed command (recommended after pip install -e .)
raymond --help
raymond workflows/test_cases/CLASSIFY.md

# Or run via root launcher
python main.py --help

# Or run directly as a module
python -m src.cli
```

## Running Tests

```bash
pytest                          # Run all tests
pytest tests/test_cc_wrap.py    # Run specific test file
pytest -v                       # Verbose output
```

## Platform Support

- **Production**: Linux only (typically in a Linux container for containment).
- **Development**: Windows is supported for development/testing, but some docs
  include Linux-specific commands.

## Docker Setup

Modern Debian/Ubuntu-based Docker images use PEP 668 to protect the system
Python. In a Docker container (which is disposable), use
`--break-system-packages` to bypass this:

```bash
pip install --break-system-packages -e .
pip install --break-system-packages -r requirements.txt
```

The `-e` (editable) flag creates a link to your source code so changes take
effect immediately. Packages install to `~/.local/bin/`.

**Verify installation:**
```bash
which raymond  # Should show: /home/devuser/.local/bin/raymond
raymond --help
```

**If command not found**, ensure `~/.local/bin` is in PATH:
```bash
export PATH="$HOME/.local/bin:$PATH"  # Add to ~/.bashrc for persistence
```

**Alternative:** Install `python3-venv` in the container and use a virtual
environment instead:
```bash
python3 -m venv .venv
source .venv/bin/activate
pip install -e .
pip install -r requirements.txt
```

## WSL Integration (Windows)

When developing on Windows, WSL can run Unix shell scripts and tests.

### Path Mangling

Git Bash auto-converts Unix paths to Windows paths, breaking WSL commands.
Use double slash (`//`) to prevent this:

```bash
# Broken (Git Bash mangles the path):
wsl cat /mnt/c/Users/user/project/file.txt

# Working:
wsl -- cat //mnt/c/Users/user/project/file.txt
```

### CRLF Line Endings

Shell scripts created on Windows often have CRLF line endings, causing
`$'\r': command not found` errors in bash. Solutions:

1. **`.gitattributes`** (recommended): `*.sh text eol=lf`
2. **On-the-fly conversion**: `wsl -- bash -c "tr -d '\r' < //mnt/c/path/script.sh | bash"`
3. **Permanent conversion**: `wsl -- dos2unix //mnt/c/path/script.sh`

If `.gitattributes` was added after files were committed, re-normalize:
```bash
git rm --cached -r .
git add .
git commit -m "Normalize line endings per .gitattributes"
```

### Running Tests via WSL

Use `bash -l` (login shell) so `~/.local/bin` is on PATH:

```bash
# Install dependencies (one-time)
wsl -- pip3 install --break-system-packages -r //mnt/c/path/to/raymond/requirements.txt

# Run tests (use -l for login shell)
wsl -- bash -l -c "cd /mnt/c/path/to/raymond && python3 -m pytest tests/ -q"

# Run only Unix-specific tests
wsl -- bash -l -c "cd /mnt/c/path/to/raymond && python3 -m pytest tests/ -v -k 'unix'"
```

### Recommendations

1. **Production use:** Windows users should use `.bat` files; the orchestrator
   routes to platform-appropriate scripts automatically.
2. **Development/testing:** Use WSL to run Unix-specific tests. Ensure
   `.gitattributes` has `*.sh text eol=lf`.
3. **CI/CD:** Run the full test suite on a Linux runner for complete coverage.
