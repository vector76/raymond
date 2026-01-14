# Code Organization Structure

## Overview

This document defines the code organization for the raymond project.

## Project Structure

```
raymond/
├── main.py                 # Entry point launcher (uses runpy to run src.main)
├── src/                    # All source code
│   ├── __init__.py
│   ├── main.py            # Main application logic
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
├── requirements-dev.txt    # Development dependencies (pytest, etc.)
├── AGENTS.md              # Agent instructions
├── CLAUDE.md              # (symlink to AGENTS.md)
└── README.md
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
- Use `conftest.py` to add `src/` to the import path for tests
- Within `src/`, modules use **relative imports** (e.g., `from .cc_wrap import ...`)
- This ensures IDE navigation (ctrl-click) works properly without runtime path manipulation

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
# main.py - Simple launcher that runs src.main as a module
import runpy

if __name__ == "__main__":
    runpy.run_module("src.main", run_name="__main__")
```

**Why this approach?**
- No `sys.path` manipulation needed
- IDE can resolve imports statically (ctrl-click works)
- Clean package structure with proper relative imports
- Can also run directly: `python -m src.main`

## Virtual Environment

```bash
python -m venv .venv
.venv\Scripts\activate  # Windows
source .venv/bin/activate  # Linux/Mac
```

## Running the Application

```bash
# Run via root launcher (recommended)
python main.py

# Or run directly as a module
python -m src.main
```

## Running Tests

```bash
pytest                          # Run all tests
pytest tests/test_cc_wrap.py    # Run specific test file
pytest -v                       # Verbose output
```
