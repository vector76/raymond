# Test configuration for pytest
#
# Note: Tests require the package to be installed.
# Run `pip install -e .` from the project root before running tests.
# This installs the package in development mode, allowing proper imports.

import sys
import pytest


def pytest_configure(config):
    """Register custom markers for platform-specific tests."""
    config.addinivalue_line("markers", "unix: mark test to run only on Unix")
    config.addinivalue_line("markers", "windows: mark test to run only on Windows")


def pytest_collection_modifyitems(config, items):
    """Skip platform-specific tests on incompatible platforms."""
    is_windows = sys.platform.startswith('win')
    skip_unix = pytest.mark.skip(reason="Unix-only test")
    skip_windows = pytest.mark.skip(reason="Windows-only test")
    
    for item in items:
        if "unix" in item.keywords and is_windows:
            item.add_marker(skip_unix)
        if "windows" in item.keywords and not is_windows:
            item.add_marker(skip_windows)
