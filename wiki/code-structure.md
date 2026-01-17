# Code Organization Structure

## Overview

This document defines the code organization for the raymond project.

## Project Structure

```
raymond/
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
├── wiki/                   # Architecture and design documentation
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
- **`wiki/`**: Architecture and design documentation
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
raymond start workflows/test_cases/CLASSIFY.md

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
