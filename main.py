"""Entry point launcher for the raymond CLI.

This file provides a simple entry point that can be run with `python main.py`.
For production use, install the package with `pip install -e .` and use the
`raymond` command directly.
"""
import sys

if __name__ == "__main__":
    from src.cli import main
    sys.exit(main())
