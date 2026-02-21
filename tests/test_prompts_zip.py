"""Tests for zip-scope support in load_prompt() and resolve_state()."""

import zipfile

import pytest

from src.prompts import load_prompt, resolve_state


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def make_zip(tmp_path, entries: dict[str, str], zip_name: str = "test.zip") -> str:
    """Create a zip archive at tmp_path/zip_name with the given text entries.

    entries: mapping of archive path → text content.
    Returns the str path to the created zip file.
    """
    zip_path = str(tmp_path / zip_name)
    with zipfile.ZipFile(zip_path, 'w') as zf:
        for name, content in entries.items():
            zf.writestr(name, content)
    return zip_path


# ---------------------------------------------------------------------------
# load_prompt() — zip scope
# ---------------------------------------------------------------------------

class TestLoadPromptZip:

    def test_returns_content_from_flat_zip(self, tmp_path):
        """load_prompt returns correct content from a flat zip archive."""
        zip_path = make_zip(tmp_path, {"START.md": "Hello from zip!"})
        content, policy = load_prompt(zip_path, "START.md")
        assert content == "Hello from zip!"
        assert policy is None

    def test_returns_content_from_single_folder_zip(self, tmp_path):
        """load_prompt returns correct content from a single-folder zip archive."""
        zip_path = make_zip(tmp_path, {"mywf/START.md": "Folder layout content."})
        content, policy = load_prompt(zip_path, "START.md")
        assert content == "Folder layout content."
        assert policy is None

    def test_parses_frontmatter_from_zip(self, tmp_path):
        """load_prompt parses YAML frontmatter from a zip file."""
        md_content = "---\nmodel: sonnet\n---\nBody text here."
        zip_path = make_zip(tmp_path, {"PROMPT.md": md_content})
        content, policy = load_prompt(zip_path, "PROMPT.md")
        assert content == "Body text here."
        assert policy is not None
        assert policy.model == "sonnet"

    def test_raises_file_not_found_for_missing_file(self, tmp_path):
        """load_prompt raises FileNotFoundError for a file absent from the zip."""
        zip_path = make_zip(tmp_path, {"START.md": "content"})
        with pytest.raises(FileNotFoundError):
            load_prompt(zip_path, "MISSING.md")

    def test_path_traversal_guard_fires_for_zip_scope(self, tmp_path):
        """load_prompt raises ValueError for filenames with path separators (zip scope)."""
        zip_path = make_zip(tmp_path, {"START.md": "content"})
        with pytest.raises(ValueError, match="contains path separator"):
            load_prompt(zip_path, "../SECRET.md")
        with pytest.raises(ValueError, match="contains path separator"):
            load_prompt(zip_path, "subdir/file.md")


# ---------------------------------------------------------------------------
# resolve_state() — zip scope
# ---------------------------------------------------------------------------

class TestResolveStateZip:

    @pytest.mark.unix
    def test_abstract_name_resolves_to_md(self, tmp_path):
        """Abstract name resolves to .md when only .md is present."""
        zip_path = make_zip(tmp_path, {"NEXT.md": "content"})
        assert resolve_state(zip_path, "NEXT") == "NEXT.md"

    @pytest.mark.unix
    def test_abstract_name_resolves_to_sh_when_no_md(self, tmp_path):
        """Abstract name resolves to .sh on Unix when no .md is present."""
        zip_path = make_zip(tmp_path, {"PROCESS.sh": "#!/bin/bash"})
        assert resolve_state(zip_path, "PROCESS") == "PROCESS.sh"

    @pytest.mark.unix
    def test_abstract_name_raises_ambiguity_when_md_and_sh_exist(self, tmp_path):
        """Abstract name raises ambiguity error when both .md and .sh exist."""
        zip_path = make_zip(tmp_path, {
            "CHECK.md": "content",
            "CHECK.sh": "#!/bin/bash",
        })
        with pytest.raises(ValueError, match="Ambiguous state"):
            resolve_state(zip_path, "CHECK")

    @pytest.mark.unix
    def test_abstract_name_raises_for_missing_state(self, tmp_path):
        """Abstract name raises FileNotFoundError when no matching file exists."""
        zip_path = make_zip(tmp_path, {"OTHER.md": "content"})
        with pytest.raises(FileNotFoundError):
            resolve_state(zip_path, "MISSING")

    @pytest.mark.unix
    def test_abstract_name_raises_for_wrong_platform_script(self, tmp_path):
        """Abstract name raises FileNotFoundError with helpful message when only .bat exists on Unix."""
        zip_path = make_zip(tmp_path, {"WORK.bat": "@echo off"})
        with pytest.raises(FileNotFoundError, match="not compatible with this platform"):
            resolve_state(zip_path, "WORK")

    @pytest.mark.unix
    def test_explicit_md_extension_resolves(self, tmp_path):
        """Explicit .md extension resolves correctly."""
        zip_path = make_zip(tmp_path, {"NEXT.md": "content"})
        assert resolve_state(zip_path, "NEXT.md") == "NEXT.md"

    @pytest.mark.unix
    def test_explicit_sh_extension_resolves_on_unix(self, tmp_path):
        """Explicit .sh extension resolves on Unix."""
        zip_path = make_zip(tmp_path, {"RUN.sh": "#!/bin/bash"})
        assert resolve_state(zip_path, "RUN.sh") == "RUN.sh"

    @pytest.mark.unix
    def test_explicit_bat_extension_raises_on_unix(self, tmp_path):
        """Explicit .bat extension raises ValueError on Unix."""
        zip_path = make_zip(tmp_path, {"RUN.bat": "@echo off"})
        with pytest.raises(ValueError, match="not compatible|Unix"):
            resolve_state(zip_path, "RUN.bat")

    @pytest.mark.unix
    def test_explicit_extension_raises_file_not_found_if_absent(self, tmp_path):
        """Explicit extension raises FileNotFoundError if file not in zip."""
        zip_path = make_zip(tmp_path, {"OTHER.md": "content"})
        with pytest.raises(FileNotFoundError):
            resolve_state(zip_path, "MISSING.md")

    def test_path_traversal_guard_fires_for_zip_scope(self, tmp_path):
        """resolve_state raises ValueError for state names with path separators."""
        zip_path = make_zip(tmp_path, {"START.md": "content"})
        with pytest.raises(ValueError, match="contains path separator"):
            resolve_state(zip_path, "../ESCAPE")
        with pytest.raises(ValueError, match="contains path separator"):
            resolve_state(zip_path, "subdir/STATE")

    @pytest.mark.unix
    def test_works_with_single_folder_zip_layout(self, tmp_path):
        """resolve_state works correctly with single-folder zip layout."""
        zip_path = make_zip(tmp_path, {"mywf/NEXT.md": "content"})
        assert resolve_state(zip_path, "NEXT") == "NEXT.md"

    @pytest.mark.windows
    def test_abstract_name_resolves_to_bat_on_windows(self, tmp_path):
        """Abstract name resolves to .bat on Windows when no .md is present."""
        zip_path = make_zip(tmp_path, {"PROCESS.bat": "@echo off"})
        assert resolve_state(zip_path, "PROCESS") == "PROCESS.bat"

    @pytest.mark.windows
    def test_explicit_sh_extension_raises_on_windows(self, tmp_path):
        """Explicit .sh extension raises ValueError on Windows."""
        zip_path = make_zip(tmp_path, {"RUN.sh": "#!/bin/bash"})
        with pytest.raises(ValueError, match="not compatible|Windows"):
            resolve_state(zip_path, "RUN.sh")
