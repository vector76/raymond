"""Tests for CLI start command with zip input."""

import argparse
import zipfile

import pytest

from src.cli import cmd_start
from src.state import read_state


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def make_zip(tmp_path, entries: dict, zip_name: str = "workflow.zip") -> str:
    """Create a zip archive with given text entries. Returns the str path."""
    zip_path = str(tmp_path / zip_name)
    with zipfile.ZipFile(zip_path, "w") as zf:
        for name, content in entries.items():
            zf.writestr(name, content)
    return zip_path


def make_args(initial_file: str, state_dir: str, workflow_id: str = "test-wf") -> argparse.Namespace:
    """Build a minimal Namespace for cmd_start()."""
    return argparse.Namespace(
        workflow_id=workflow_id,
        initial_file=initial_file,
        state_dir=state_dir,
        no_run=True,
        verbose=False,
        budget=None,
        no_debug=False,
        model=None,
        timeout=None,
        initial_input=None,
        dangerously_skip_permissions=False,
    )


# ---------------------------------------------------------------------------
# Happy path
# ---------------------------------------------------------------------------

class TestCmdStartZipHappyPath:

    def test_happy_path_flat_zip(self, tmp_path):
        """raymond start workflow.zip succeeds; scope_dir is zip path, initial_state is 1_START.md."""
        zip_path = make_zip(tmp_path, {"1_START.md": "# Start"})
        state_dir = str(tmp_path / "state")

        args = make_args(zip_path, state_dir)
        result = cmd_start(args)

        assert result == 0
        state = read_state("test-wf", state_dir=state_dir)
        assert state["scope_dir"] == zip_path
        assert state["agents"][0]["current_state"] == "1_START.md"

    def test_happy_path_single_folder_zip(self, tmp_path):
        """Zip with single top-level folder is accepted."""
        zip_path = make_zip(tmp_path, {"mywf/1_START.md": "# Start"})
        state_dir = str(tmp_path / "state")

        args = make_args(zip_path, state_dir)
        result = cmd_start(args)

        assert result == 0
        state = read_state("test-wf", state_dir=state_dir)
        assert state["scope_dir"] == zip_path

    def test_output_label_is_workflow_scope_for_zip(self, tmp_path, capsys):
        """Output label reads 'Workflow scope' for zip input."""
        zip_path = make_zip(tmp_path, {"1_START.md": "# Start"})
        state_dir = str(tmp_path / "state")

        args = make_args(zip_path, state_dir)
        cmd_start(args)

        captured = capsys.readouterr()
        assert "Workflow scope:" in captured.out
        assert "Scope directory:" not in captured.out


# ---------------------------------------------------------------------------
# Error cases
# ---------------------------------------------------------------------------

class TestCmdStartZipErrors:

    def test_error_zip_not_found(self, tmp_path):
        """Non-existent zip file produces error and returns non-zero."""
        missing = str(tmp_path / "missing.zip")
        state_dir = str(tmp_path / "state")

        args = make_args(missing, state_dir)
        result = cmd_start(args)

        assert result != 0

    def test_error_corrupt_zip(self, tmp_path):
        """Corrupt zip file produces error and returns non-zero."""
        bad_zip = tmp_path / "bad.zip"
        bad_zip.write_bytes(b"this is not a zip file")
        state_dir = str(tmp_path / "state")

        args = make_args(str(bad_zip), state_dir)
        result = cmd_start(args)

        assert result != 0

    def test_error_invalid_layout_multiple_top_level_folders(self, tmp_path):
        """Zip with multiple top-level folders produces error and returns non-zero."""
        zip_path = make_zip(tmp_path, {
            "folder_a/1_START.md": "# Start",
            "folder_b/OTHER.md": "# Other",
        })
        state_dir = str(tmp_path / "state")

        args = make_args(zip_path, state_dir)
        result = cmd_start(args)

        assert result != 0

    def test_error_no_1_start_md(self, tmp_path):
        """Zip without 1_START.md produces error and returns non-zero."""
        zip_path = make_zip(tmp_path, {"OTHER.md": "# Not Start"})
        state_dir = str(tmp_path / "state")

        args = make_args(zip_path, state_dir)
        result = cmd_start(args)

        assert result != 0

    def test_error_no_1_start_md_message(self, tmp_path, capsys):
        """Error message mentions 1_START.md when it is missing."""
        zip_path = make_zip(tmp_path, {"OTHER.md": "content"})
        state_dir = str(tmp_path / "state")

        args = make_args(zip_path, state_dir)
        cmd_start(args)

        captured = capsys.readouterr()
        assert "1_START.md" in captured.err


# ---------------------------------------------------------------------------
# Regression: directory-scope input unaffected
# ---------------------------------------------------------------------------

class TestCmdStartDirectoryRegression:

    def test_directory_input_still_works(self, tmp_path):
        """Directory input path is unaffected by zip changes."""
        scope_dir = tmp_path / "workflow"
        scope_dir.mkdir()
        (scope_dir / "1_START.md").write_text("# Start")
        state_dir = str(tmp_path / "state")

        args = make_args(str(scope_dir), state_dir)
        result = cmd_start(args)

        assert result == 0
        state = read_state("test-wf", state_dir=state_dir)
        assert state["scope_dir"] == str(scope_dir.resolve())
        assert state["agents"][0]["current_state"] == "1_START.md"

    def test_file_input_still_works(self, tmp_path):
        """Direct .md file input path is unaffected by zip changes."""
        scope_dir = tmp_path / "workflow"
        scope_dir.mkdir()
        start_file = scope_dir / "START.md"
        start_file.write_text("# Start")
        state_dir = str(tmp_path / "state")

        args = make_args(str(start_file), state_dir)
        result = cmd_start(args)

        assert result == 0
        state = read_state("test-wf", state_dir=state_dir)
        assert state["agents"][0]["current_state"] == "START.md"

    def test_output_label_is_workflow_scope_for_directory(self, tmp_path, capsys):
        """Output label reads 'Workflow scope' for directory input too."""
        scope_dir = tmp_path / "workflow"
        scope_dir.mkdir()
        (scope_dir / "1_START.md").write_text("# Start")
        state_dir = str(tmp_path / "state")

        args = make_args(str(scope_dir), state_dir)
        cmd_start(args)

        captured = capsys.readouterr()
        assert "Workflow scope:" in captured.out
        assert "Scope directory:" not in captured.out
