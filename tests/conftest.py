import sys
from pathlib import Path

# Add src directory to path for test imports
# This allows tests to import from src modules without installing the package
# Once the package is installed (pip install -e .), this is no longer needed
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))
