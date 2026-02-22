"""Tests for src/zip_scope.py — zip archive access module."""

import hashlib
import os
import zipfile
from pathlib import Path

import pytest

from src.zip_scope import (
    ZipFileNotFoundError,
    ZipFilenameAmbiguousError,
    ZipHashMismatchError,
    ZipLayoutError,
    detect_layout,
    extract_hash_from_filename,
    extract_script,
    file_exists,
    is_zip_scope,
    list_files,
    read_text,
    verify_zip_hash,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def make_zip(tmp_path, entries: dict[str, bytes]) -> str:
    """Create a zip file at tmp_path / 'test.zip' with the given entries.

    entries: mapping of archive path → file bytes.
    Returns the str path to the created zip file.
    """
    zip_path = str(tmp_path / "test.zip")
    with zipfile.ZipFile(zip_path, 'w') as zf:
        for name, content in entries.items():
            zf.writestr(name, content)
    return zip_path


# ---------------------------------------------------------------------------
# is_zip_scope
# ---------------------------------------------------------------------------

class TestIsZipScope:

    def test_returns_true_for_zip_extension(self):
        assert is_zip_scope("workflow.zip") is True

    def test_returns_true_for_uppercase_extension(self):
        assert is_zip_scope("WORKFLOW.ZIP") is True

    def test_returns_true_for_mixed_case_extension(self):
        assert is_zip_scope("workflow.Zip") is True

    def test_returns_true_for_path_with_zip(self):
        assert is_zip_scope("/some/path/workflow.zip") is True

    def test_returns_false_for_directory_path(self):
        assert is_zip_scope("/some/path/workflow") is False

    def test_returns_false_for_md_file(self):
        assert is_zip_scope("workflow.md") is False

    def test_returns_false_for_zip_in_middle_of_path(self):
        assert is_zip_scope("/some/workflow.zip/subdir") is False

    def test_returns_false_for_empty_string(self):
        assert is_zip_scope("") is False


# ---------------------------------------------------------------------------
# detect_layout
# ---------------------------------------------------------------------------

class TestDetectLayout:

    def test_flat_layout_returns_empty_prefix(self, tmp_path):
        zip_path = make_zip(tmp_path, {
            "START.md": b"start",
            "CHECK.sh": b"#!/bin/bash",
            "END.md": b"end",
        })
        assert detect_layout(zip_path) == ""

    def test_single_folder_layout_returns_prefix(self, tmp_path):
        zip_path = make_zip(tmp_path, {
            "mywf/START.md": b"start",
            "mywf/CHECK.sh": b"#!/bin/bash",
        })
        assert detect_layout(zip_path) == "mywf/"

    def test_single_folder_with_directory_entry_in_archive(self, tmp_path):
        """Directory entries (ending in /) should be ignored during layout detection."""
        zip_path = str(tmp_path / "test.zip")
        with zipfile.ZipFile(zip_path, 'w') as zf:
            zf.mkdir("mywf")
            zf.writestr("mywf/START.md", b"start")
            zf.writestr("mywf/END.md", b"end")
        assert detect_layout(zip_path) == "mywf/"

    def test_empty_archive_raises_layout_error(self, tmp_path):
        zip_path = make_zip(tmp_path, {})
        with pytest.raises(ZipLayoutError, match="Empty zip archive"):
            detect_layout(zip_path)

    def test_multiple_top_level_folders_raises_layout_error(self, tmp_path):
        zip_path = make_zip(tmp_path, {
            "wf1/START.md": b"start",
            "wf2/END.md": b"end",
        })
        with pytest.raises(ZipLayoutError, match="multiple top-level folders"):
            detect_layout(zip_path)

    def test_mixed_root_files_and_folder_raises_layout_error(self, tmp_path):
        zip_path = make_zip(tmp_path, {
            "START.md": b"start",
            "mywf/END.md": b"end",
        })
        with pytest.raises(ZipLayoutError, match="mix of top-level files"):
            detect_layout(zip_path)

    def test_deep_nesting_raises_layout_error(self, tmp_path):
        zip_path = make_zip(tmp_path, {
            "mywf/sub/START.md": b"start",
        })
        with pytest.raises(ZipLayoutError, match="nested more than one level"):
            detect_layout(zip_path)

    def test_corrupt_archive_raises_layout_error(self, tmp_path):
        zip_path = str(tmp_path / "bad.zip")
        with open(zip_path, 'wb') as f:
            f.write(b"this is not a zip file")
        with pytest.raises(ZipLayoutError, match="Corrupt or unreadable"):
            detect_layout(zip_path)

    def test_nonexistent_archive_raises_file_not_found(self, tmp_path):
        with pytest.raises(FileNotFoundError, match="not found"):
            detect_layout(str(tmp_path / "missing.zip"))

    def test_error_message_includes_zip_path(self, tmp_path):
        zip_path = make_zip(tmp_path, {})
        with pytest.raises(ZipLayoutError) as exc_info:
            detect_layout(zip_path)
        assert zip_path in str(exc_info.value)


# ---------------------------------------------------------------------------
# list_files
# ---------------------------------------------------------------------------

class TestListFiles:

    def test_flat_layout_returns_all_filenames(self, tmp_path):
        zip_path = make_zip(tmp_path, {
            "START.md": b"start",
            "CHECK.sh": b"check",
            "END.md": b"end",
        })
        assert list_files(zip_path) == {"START.md", "CHECK.sh", "END.md"}

    def test_single_folder_layout_returns_bare_names(self, tmp_path):
        zip_path = make_zip(tmp_path, {
            "mywf/START.md": b"start",
            "mywf/CHECK.sh": b"check",
        })
        assert list_files(zip_path) == {"START.md", "CHECK.sh"}

    def test_directory_entries_excluded(self, tmp_path):
        zip_path = str(tmp_path / "test.zip")
        with zipfile.ZipFile(zip_path, 'w') as zf:
            zf.mkdir("mywf")
            zf.writestr("mywf/START.md", b"start")
        files = list_files(zip_path)
        assert "mywf/" not in files
        assert "START.md" in files

    def test_invalid_layout_raises_layout_error(self, tmp_path):
        zip_path = make_zip(tmp_path, {})
        with pytest.raises(ZipLayoutError):
            list_files(zip_path)


# ---------------------------------------------------------------------------
# file_exists
# ---------------------------------------------------------------------------

class TestFileExists:

    def test_returns_true_for_present_file_flat(self, tmp_path):
        zip_path = make_zip(tmp_path, {"START.md": b"start"})
        assert file_exists(zip_path, "START.md") is True

    def test_returns_true_for_present_file_single_folder(self, tmp_path):
        zip_path = make_zip(tmp_path, {"mywf/START.md": b"start"})
        assert file_exists(zip_path, "START.md") is True

    def test_returns_false_for_absent_file(self, tmp_path):
        zip_path = make_zip(tmp_path, {"START.md": b"start"})
        assert file_exists(zip_path, "MISSING.md") is False

    def test_returns_false_for_invalid_layout(self, tmp_path):
        zip_path = make_zip(tmp_path, {})
        assert file_exists(zip_path, "START.md") is False

    def test_returns_false_for_nonexistent_archive(self, tmp_path):
        assert file_exists(str(tmp_path / "missing.zip"), "START.md") is False

    def test_prefix_not_treated_as_bare_name(self, tmp_path):
        """The folder prefix itself should not appear as a file."""
        zip_path = make_zip(tmp_path, {"mywf/START.md": b"start"})
        assert file_exists(zip_path, "mywf/START.md") is False


# ---------------------------------------------------------------------------
# read_text
# ---------------------------------------------------------------------------

class TestReadText:

    def test_returns_content_flat_layout(self, tmp_path):
        zip_path = make_zip(tmp_path, {"START.md": b"Hello, world!"})
        assert read_text(zip_path, "START.md") == "Hello, world!"

    def test_returns_content_single_folder_layout(self, tmp_path):
        zip_path = make_zip(tmp_path, {"mywf/START.md": b"Hello from folder!"})
        assert read_text(zip_path, "START.md") == "Hello from folder!"

    def test_returns_multiline_content(self, tmp_path):
        content = "line one\nline two\nline three\n"
        zip_path = make_zip(tmp_path, {"NOTES.md": content.encode()})
        assert read_text(zip_path, "NOTES.md") == content

    def test_missing_file_raises_zip_file_not_found_error(self, tmp_path):
        zip_path = make_zip(tmp_path, {"START.md": b"start"})
        with pytest.raises(ZipFileNotFoundError, match="MISSING.md"):
            read_text(zip_path, "MISSING.md")

    def test_error_message_includes_zip_path(self, tmp_path):
        zip_path = make_zip(tmp_path, {"START.md": b"start"})
        with pytest.raises(ZipFileNotFoundError) as exc_info:
            read_text(zip_path, "MISSING.md")
        assert zip_path in str(exc_info.value)

    def test_invalid_layout_raises_layout_error(self, tmp_path):
        zip_path = make_zip(tmp_path, {})
        with pytest.raises(ZipLayoutError):
            read_text(zip_path, "START.md")

    def test_nonexistent_archive_raises_file_not_found(self, tmp_path):
        with pytest.raises(FileNotFoundError):
            read_text(str(tmp_path / "missing.zip"), "START.md")


# ---------------------------------------------------------------------------
# extract_script
# ---------------------------------------------------------------------------

class TestExtractScript:

    def test_extracted_file_contains_correct_content(self, tmp_path):
        content = b"#!/bin/bash\necho hello\n"
        zip_path = make_zip(tmp_path, {"run.sh": content})
        out_path = extract_script(zip_path, "run.sh")
        try:
            with open(out_path, 'rb') as f:
                assert f.read() == content
        finally:
            os.unlink(out_path)

    def test_extracted_file_preserves_extension(self, tmp_path):
        zip_path = make_zip(tmp_path, {"run.sh": b"#!/bin/bash"})
        out_path = extract_script(zip_path, "run.sh")
        try:
            assert out_path.endswith(".sh")
        finally:
            os.unlink(out_path)

    def test_extracted_bat_file_preserves_extension(self, tmp_path):
        zip_path = make_zip(tmp_path, {"run.bat": b"@echo off"})
        out_path = extract_script(zip_path, "run.bat")
        try:
            assert out_path.endswith(".bat")
        finally:
            os.unlink(out_path)

    def test_two_calls_produce_different_paths(self, tmp_path):
        zip_path = make_zip(tmp_path, {"run.sh": b"#!/bin/bash"})
        path1 = extract_script(zip_path, "run.sh")
        path2 = extract_script(zip_path, "run.sh")
        try:
            assert path1 != path2
        finally:
            os.unlink(path1)
            os.unlink(path2)

    def test_temp_file_can_be_deleted_by_caller(self, tmp_path):
        zip_path = make_zip(tmp_path, {"run.sh": b"#!/bin/bash"})
        out_path = extract_script(zip_path, "run.sh")
        assert os.path.exists(out_path)
        os.unlink(out_path)
        assert not os.path.exists(out_path)

    def test_works_with_single_folder_layout(self, tmp_path):
        content = b"#!/bin/bash\necho folder layout\n"
        zip_path = make_zip(tmp_path, {"mywf/run.sh": content})
        out_path = extract_script(zip_path, "run.sh")
        try:
            with open(out_path, 'rb') as f:
                assert f.read() == content
        finally:
            os.unlink(out_path)

    def test_missing_file_raises_zip_file_not_found_error(self, tmp_path):
        zip_path = make_zip(tmp_path, {"run.sh": b"#!/bin/bash"})
        with pytest.raises(ZipFileNotFoundError, match="MISSING.sh"):
            extract_script(zip_path, "MISSING.sh")

    def test_nonexistent_archive_raises_file_not_found(self, tmp_path):
        with pytest.raises(FileNotFoundError):
            extract_script(str(tmp_path / "missing.zip"), "run.sh")


# ---------------------------------------------------------------------------
# extract_hash_from_filename
# ---------------------------------------------------------------------------

class TestExtractHashFromFilename:

    def test_no_hex_runs_returns_none(self):
        assert extract_hash_from_filename("nohex.txt") is None

    def test_hex_runs_shorter_than_64_returns_none(self):
        assert extract_hash_from_filename("abc123def.zip") is None

    def test_single_64_char_run_mid_filename_returns_hash(self):
        h = "a" * 64
        assert extract_hash_from_filename(f"workflow-{h}.zip") == h

    def test_single_64_char_run_is_full_basename_returns_hash(self):
        h = "a" * 64
        assert extract_hash_from_filename(h) == h

    def test_run_longer_than_64_raises_ambiguous_error(self):
        with pytest.raises(ZipFilenameAmbiguousError):
            extract_hash_from_filename("a" * 65)

    def test_two_64_char_runs_raises_ambiguous_error(self):
        with pytest.raises(ZipFilenameAmbiguousError):
            extract_hash_from_filename("a" * 64 + "-" + "b" * 64)

    def test_uppercase_hex_normalized_to_lowercase(self):
        h = "A" * 64
        assert extract_hash_from_filename(h) == "a" * 64

    def test_merging_prefix_creates_run_over_64_raises_ambiguous_error(self):
        # 'a' + 'c' * 64 = 65 contiguous hex chars → ambiguous
        with pytest.raises(ZipFilenameAmbiguousError):
            extract_hash_from_filename("a" + "c" * 64 + ".zip")

    def test_zip_extension_terminates_adjacent_hex_run(self):
        # 64-char hex run immediately before '.zip'; z/i/p are not hex
        h = "a" * 64
        assert extract_hash_from_filename(h + ".zip") == h


# ---------------------------------------------------------------------------
# verify_zip_hash
# ---------------------------------------------------------------------------

class TestVerifyZipHash:

    def test_no_hash_in_filename_returns_without_error(self, tmp_path):
        zip_path = make_zip(tmp_path, {"1_START.md": b"start"})
        verify_zip_hash(zip_path)  # should not raise

    def test_correct_hash_in_filename_returns_without_error(self, tmp_path):
        zip_path = make_zip(tmp_path, {"1_START.md": b"start"})
        actual_hash = hashlib.sha256(Path(zip_path).read_bytes()).hexdigest()
        new_path = Path(zip_path).rename(tmp_path / f"workflow-{actual_hash}.zip")
        verify_zip_hash(str(new_path))  # should not raise

    def test_incorrect_hash_in_filename_raises_mismatch_error(self, tmp_path):
        zip_path = make_zip(tmp_path, {"1_START.md": b"start"})
        actual_hash = hashlib.sha256(Path(zip_path).read_bytes()).hexdigest()
        wrong_hash = "0" * 64
        new_path = Path(zip_path).rename(tmp_path / f"workflow-{wrong_hash}.zip")
        with pytest.raises(ZipHashMismatchError) as exc_info:
            verify_zip_hash(str(new_path))
        assert exc_info.value.expected == wrong_hash
        assert exc_info.value.actual == actual_hash

    def test_ambiguous_filename_run_over_64_raises_ambiguous_error(self, tmp_path):
        path = str(tmp_path / ("workflow-" + "a" * 65 + ".zip"))
        with pytest.raises(ZipFilenameAmbiguousError):
            verify_zip_hash(path)

    def test_ambiguous_filename_two_64_char_runs_raises_ambiguous_error(self, tmp_path):
        path = str(tmp_path / ("a" * 64 + "-" + "b" * 64 + ".zip"))
        with pytest.raises(ZipFilenameAmbiguousError):
            verify_zip_hash(path)
