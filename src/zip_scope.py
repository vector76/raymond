"""Zip archive access for workflow scope directories.

This module provides all zip file access operations needed when a workflow's
scope directory is a zip archive rather than a plain directory. A scope value
ending in '.zip' (case-insensitive) is treated as a zip archive.

Valid archive layouts:
- Flat: all workflow files live at the root of the archive (prefix = "")
- Single-folder: all workflow files live inside one top-level directory
  (prefix = "foldername/")
"""

import os
import re
import tempfile
import zipfile
from pathlib import Path


class ZipLayoutError(Exception):
    """Raised when a zip archive has an invalid or unsupported layout.

    This covers empty archives, multiple top-level folders, mixed root
    content (files and a folder at the same level), files nested more
    than one level deep, and corrupt or unreadable archives.
    """


class ZipFileNotFoundError(Exception):
    """Raised when a requested file is not found within a zip archive."""


class ZipFilenameAmbiguousError(Exception):
    """Raised when a zip filename contains an ambiguous hash-like hex run."""


def is_zip_scope(scope_dir: str) -> bool:
    """Return True if scope_dir refers to a zip archive.

    Detection is based solely on the path suffix — no filesystem access.

    Args:
        scope_dir: Path string to test.

    Returns:
        True if scope_dir ends with '.zip' (case-insensitive), False otherwise.
    """
    return scope_dir.lower().endswith('.zip')


def detect_layout(zip_path: str) -> str:
    """Open a zip archive, detect its layout, and return the effective prefix.

    Valid layouts:
    - Flat: all files at root → returns ""
    - Single-folder: all files inside one top-level directory → returns "foldername/"

    Args:
        zip_path: Path to the zip archive.

    Returns:
        Effective prefix string ("" for flat, "foldername/" for single-folder).

    Raises:
        ZipLayoutError: If the archive is empty, has multiple top-level folders,
            mixes top-level files and a folder, contains files nested more than
            one level deep, or is corrupt/unreadable.
        FileNotFoundError: If zip_path does not exist.
    """
    if not Path(zip_path).exists():
        raise FileNotFoundError(f"Zip archive not found: {zip_path}")

    try:
        with zipfile.ZipFile(zip_path, 'r') as zf:
            names = zf.namelist()
    except zipfile.BadZipFile as e:
        raise ZipLayoutError(f"Corrupt or unreadable zip archive: {zip_path}") from e

    # Filter out directory entries (entries ending with '/')
    file_names = [n for n in names if not n.endswith('/')]

    if not file_names:
        raise ZipLayoutError(f"Empty zip archive (no files): {zip_path}")

    # Flat layout: all files are directly at root (no subdirectories)
    root_files = [n for n in file_names if '/' not in n]
    if len(root_files) == len(file_names):
        # All files at root — flat layout
        return ""

    # Check for files at root mixed with a subdirectory
    if root_files:
        raise ZipLayoutError(
            f"Invalid zip layout in {zip_path}: mix of top-level files and "
            f"subdirectories at root"
        )

    # All files are inside subdirectories — check there's exactly one top-level folder
    top_level_folders = set(name.split('/')[0] for name in file_names)
    if len(top_level_folders) > 1:
        raise ZipLayoutError(
            f"Invalid zip layout in {zip_path}: multiple top-level folders "
            f"({', '.join(sorted(top_level_folders))})"
        )

    # Single folder — check no deep nesting (all files must be exactly one level inside)
    folder = next(iter(top_level_folders))
    for name in file_names:
        parts = name.split('/')
        # parts[0] is folder name, parts[1] is filename — any more means deep nesting
        if len(parts) > 2:
            raise ZipLayoutError(
                f"Invalid zip layout in {zip_path}: files nested more than one "
                f"level deep (e.g. '{name}')"
            )

    return folder + '/'


def list_files(zip_path: str) -> set[str]:
    """Return the set of workflow-relevant filenames at the effective prefix.

    Returns bare filenames only (no path prefix). Directory entries are excluded.

    Args:
        zip_path: Path to the zip archive.

    Returns:
        Set of bare filenames found at the effective prefix level.

    Raises:
        ZipLayoutError: If the archive layout is invalid.
        FileNotFoundError: If zip_path does not exist.
    """
    prefix = detect_layout(zip_path)

    try:
        with zipfile.ZipFile(zip_path, 'r') as zf:
            names = zf.namelist()
    except zipfile.BadZipFile as e:
        raise ZipLayoutError(f"Corrupt or unreadable zip archive: {zip_path}") from e

    result = set()
    for name in names:
        if name.endswith('/'):
            continue
        if prefix:
            if name.startswith(prefix):
                bare = name[len(prefix):]
                if bare:
                    result.add(bare)
        else:
            result.add(name)
    return result


def file_exists(zip_path: str, filename: str) -> bool:
    """Return True if filename exists at the effective prefix in the archive.

    Args:
        zip_path: Path to the zip archive.
        filename: Bare filename to look for (no path prefix).

    Returns:
        True if the file exists, False otherwise. Returns False if the archive
        has an invalid layout.
    """
    try:
        files = list_files(zip_path)
    except (ZipLayoutError, FileNotFoundError):
        return False
    return filename in files


def read_text(zip_path: str, filename: str) -> str:
    """Read and return the UTF-8 decoded content of a file from the archive.

    Args:
        zip_path: Path to the zip archive.
        filename: Bare filename to read (no path prefix).

    Returns:
        UTF-8 decoded string content of the file.

    Raises:
        ZipFileNotFoundError: If filename is not found at the effective prefix.
        ZipLayoutError: If the archive layout is invalid.
        FileNotFoundError: If zip_path does not exist.
    """
    prefix = detect_layout(zip_path)
    full_name = prefix + filename

    try:
        with zipfile.ZipFile(zip_path, 'r') as zf:
            try:
                data = zf.read(full_name)
            except KeyError:
                raise ZipFileNotFoundError(
                    f"File '{filename}' not found in zip archive: {zip_path}"
                )
    except zipfile.BadZipFile as e:
        raise ZipLayoutError(f"Corrupt or unreadable zip archive: {zip_path}") from e

    return data.decode('utf-8')


def extract_script(zip_path: str, filename: str) -> str:
    """Extract a file from the archive to a uniquely-named temp file.

    The caller is responsible for deleting the temp file when done.

    Args:
        zip_path: Path to the zip archive.
        filename: Bare filename to extract (no path prefix).

    Returns:
        Absolute path string of the extracted temp file.

    Raises:
        ZipFileNotFoundError: If filename is not found at the effective prefix.
        ZipLayoutError: If the archive layout is invalid.
        FileNotFoundError: If zip_path does not exist.
    """
    prefix = detect_layout(zip_path)
    full_name = prefix + filename

    try:
        with zipfile.ZipFile(zip_path, 'r') as zf:
            try:
                data = zf.read(full_name)
            except KeyError:
                raise ZipFileNotFoundError(
                    f"File '{filename}' not found in zip archive: {zip_path}"
                )
    except zipfile.BadZipFile as e:
        raise ZipLayoutError(f"Corrupt or unreadable zip archive: {zip_path}") from e

    suffix = Path(filename).suffix
    fd, tmp_path = tempfile.mkstemp(suffix=suffix)
    try:
        os.write(fd, data)
    finally:
        os.close(fd)

    return tmp_path


def extract_hash_from_filename(basename: str) -> str | None:
    """Extract a SHA256 hash from a zip filename, if unambiguously present.

    Scans basename for maximal contiguous hex runs. A 64-character hex run
    is interpreted as a SHA256 hash.

    Args:
        basename: The filename (without directory path) to inspect.

    Returns:
        The 64-character lowercase hex hash string if exactly one such run
        exists, or None if no 64-character run is found.

    Raises:
        ZipFilenameAmbiguousError: If any hex run exceeds 64 characters, or
            if more than one 64-character hex run is present.
    """
    lower = basename.lower()
    runs = re.findall(r'[0-9a-f]+', lower)

    if any(len(r) > 64 for r in runs):
        raise ZipFilenameAmbiguousError(
            f"Filename '{basename}' contains a hex run longer than 64 characters"
        )

    runs_64 = [r for r in runs if len(r) == 64]

    if len(runs_64) > 1:
        raise ZipFilenameAmbiguousError(
            f"Filename '{basename}' contains multiple 64-character hex runs"
        )

    if len(runs_64) == 1:
        return runs_64[0]

    return None
