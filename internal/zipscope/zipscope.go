// Package zipscope provides access to workflow scopes stored inside zip archives.
//
// A "zip scope" is a .zip file used in place of a directory to bundle all
// workflow state files into a single distributable artifact. The functions in
// this package mirror the directory-based equivalents used elsewhere (os.ReadFile,
// os.Stat, os.ReadDir) but operate on the zip archive's virtual file system.
//
// Detection: IsZipScope returns true when the scope path ends in ".zip"
// (case-insensitive). All other paths are treated as plain directories.
//
// Valid archive layouts:
//   - Flat: all workflow files live at the root of the archive (prefix = "")
//   - Single-folder: all workflow files live inside one top-level directory
//     (prefix = "foldername/")
package zipscope

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ZipLayoutError is returned when a zip archive has an invalid or unsupported layout.
// This covers empty archives, multiple top-level folders, mixed root content
// (files and a folder at the same level), files nested more than one level deep,
// and corrupt or unreadable archives.
type ZipLayoutError struct {
	msg string
}

func (e *ZipLayoutError) Error() string { return e.msg }

// ZipFileNotFoundError is returned by ReadText when the requested file does
// not exist inside the zip archive.
type ZipFileNotFoundError struct {
	ZipPath  string
	Filename string
}

func (e *ZipFileNotFoundError) Error() string {
	return fmt.Sprintf("file not found in zip: %s (in %s)", e.Filename, e.ZipPath)
}

// ZipFilenameAmbiguousError is returned when a zip filename contains an
// ambiguous hash-like hex run (a run longer than 64 chars, or multiple
// 64-char runs).
type ZipFilenameAmbiguousError struct {
	msg string
}

func (e *ZipFilenameAmbiguousError) Error() string { return e.msg }

// ZipHashMismatchError is returned when a zip file's SHA256 does not match
// the hash embedded in its filename.
type ZipHashMismatchError struct {
	Expected string
	Actual   string
}

func (e *ZipHashMismatchError) Error() string {
	return fmt.Sprintf("Hash mismatch: expected %s, got %s", e.Expected, e.Actual)
}

// hexRunRe matches maximal contiguous sequences of hex characters.
var hexRunRe = regexp.MustCompile(`[0-9a-f]+`)

// validateFilename rejects filenames that contain path separators, which would
// allow directory traversal outside the zip scope.
func validateFilename(filename string) error {
	if strings.ContainsAny(filename, `/\`) {
		return fmt.Errorf("invalid filename (must not contain path separators): %q", filename)
	}
	return nil
}

// IsZipScope reports whether scopeDir refers to a zip archive (i.e. ends in
// ".zip", case-insensitive). All other paths are treated as plain directories.
func IsZipScope(scopeDir string) bool {
	return strings.ToLower(filepath.Ext(scopeDir)) == ".zip"
}

// DetectLayout opens a zip archive, detects its layout, and returns the
// effective path prefix for files within the archive.
//
// Valid layouts:
//   - Flat: all files at root → returns ""
//   - Single-folder: all files inside one top-level directory → returns "foldername/"
//
// Returns a *ZipLayoutError if the archive is empty, has multiple top-level
// folders, mixes top-level files and a folder, contains files nested more
// than one level deep, or is corrupt/unreadable. Returns an error wrapping
// os.ErrNotExist when zipPath does not exist (mirrors Python's FileNotFoundError).
func DetectLayout(zipPath string) (string, error) {
	if _, err := os.Stat(zipPath); errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("zip archive not found: %s: %w", zipPath, os.ErrNotExist)
	}

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", &ZipLayoutError{msg: fmt.Sprintf("corrupt or unreadable zip archive: %s", zipPath)}
	}
	defer r.Close()

	// Collect all non-directory entries.
	var fileNames []string
	for _, f := range r.File {
		if !strings.HasSuffix(f.Name, "/") {
			fileNames = append(fileNames, f.Name)
		}
	}

	if len(fileNames) == 0 {
		return "", &ZipLayoutError{msg: fmt.Sprintf("empty zip archive (no files): %s", zipPath)}
	}

	// Flat layout: all files are directly at root (no subdirectories).
	var rootFiles []string
	for _, name := range fileNames {
		if !strings.Contains(name, "/") {
			rootFiles = append(rootFiles, name)
		}
	}
	if len(rootFiles) == len(fileNames) {
		return "", nil
	}

	// Check for files at root mixed with a subdirectory.
	if len(rootFiles) > 0 {
		return "", &ZipLayoutError{
			msg: fmt.Sprintf("invalid zip layout in %s: mix of top-level files and subdirectories at root", zipPath),
		}
	}

	// All files are inside subdirectories — check there's exactly one top-level folder.
	topLevel := make(map[string]bool)
	for _, name := range fileNames {
		folder := name[:strings.Index(name, "/")+1]
		topLevel[folder] = true
	}
	if len(topLevel) > 1 {
		var folders []string
		for f := range topLevel {
			folders = append(folders, strings.TrimSuffix(f, "/"))
		}
		sort.Strings(folders)
		return "", &ZipLayoutError{
			msg: fmt.Sprintf("invalid zip layout in %s: multiple top-level folders (%s)", zipPath, strings.Join(folders, ", ")),
		}
	}

	// Single folder — check no deep nesting (all files must be exactly one level inside).
	for _, name := range fileNames {
		parts := strings.Split(name, "/")
		// parts[0] = folder, parts[1] = filename; any more means deep nesting.
		if len(parts) > 2 {
			return "", &ZipLayoutError{
				msg: fmt.Sprintf("invalid zip layout in %s: files nested more than one level deep (e.g. %q)", zipPath, name),
			}
		}
	}

	// Return the single top-level folder as prefix (with trailing slash).
	for folder := range topLevel {
		return folder, nil
	}
	return "", nil
}

// ExtractHashFromFilename scans basename for maximal contiguous lowercase-hex
// runs. A 64-character hex run is interpreted as a SHA256 hash.
//
// Returns (hash, nil) when exactly one 64-char run is found, ("", nil) when
// no 64-char run exists, or ("", *ZipFilenameAmbiguousError) when any run
// exceeds 64 chars or multiple 64-char runs are present.
func ExtractHashFromFilename(basename string) (string, error) {
	lower := strings.ToLower(basename)
	runs := hexRunRe.FindAllString(lower, -1)

	var runs64 []string
	for _, r := range runs {
		if len(r) > 64 {
			return "", &ZipFilenameAmbiguousError{
				msg: fmt.Sprintf("filename %q contains a hex run longer than 64 characters", basename),
			}
		}
		if len(r) == 64 {
			runs64 = append(runs64, r)
		}
	}

	if len(runs64) > 1 {
		return "", &ZipFilenameAmbiguousError{
			msg: fmt.Sprintf("filename %q contains multiple 64-character hex runs", basename),
		}
	}

	if len(runs64) == 1 {
		return runs64[0], nil
	}

	return "", nil
}

// VerifyZipHash verifies that a zip file's SHA256 matches the hash embedded
// in its filename. If no hash is present in the filename, it returns
// immediately (no-op). If the filename is ambiguous, ZipFilenameAmbiguousError
// is returned.
func VerifyZipHash(zipPath string) error {
	expected, err := ExtractHashFromFilename(filepath.Base(zipPath))
	if err != nil {
		return err
	}
	if expected == "" {
		return nil
	}

	h := sha256.New()
	f, err := os.Open(zipPath)
	if err != nil {
		return fmt.Errorf("failed to open zip for hash verification: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("failed to read zip for hash verification: %w", err)
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return &ZipHashMismatchError{Expected: expected, Actual: actual}
	}
	return nil
}

// ReadText reads filename from the zip archive at zipPath and returns its
// contents as a UTF-8 string. Returns *ZipFileNotFoundError when the file does
// not exist inside the archive. Transparently handles both flat and
// single-folder layouts.
func ReadText(zipPath, filename string) (string, error) {
	if err := validateFilename(filename); err != nil {
		return "", err
	}

	prefix, err := DetectLayout(zipPath)
	if err != nil {
		return "", err
	}
	fullName := prefix + filename

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("failed to open zip %s: %w", zipPath, err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name == fullName {
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("failed to open %s in zip %s: %w", filename, zipPath, err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return "", fmt.Errorf("failed to read %s from zip %s: %w", filename, zipPath, err)
			}
			return string(data), nil
		}
	}
	return "", &ZipFileNotFoundError{ZipPath: zipPath, Filename: filename}
}

// FileExists reports whether filename exists inside the zip archive at zipPath.
// Returns (false, nil) when the file is not present, the layout is invalid
// (*ZipLayoutError), or the archive does not exist (os.ErrNotExist) — matching
// Python's file_exists() which catches both ZipLayoutError and FileNotFoundError.
// Returns (false, err) when the archive is unreadable or the filename is invalid.
func FileExists(zipPath, filename string) (bool, error) {
	if err := validateFilename(filename); err != nil {
		return false, err
	}

	files, err := ListFiles(zipPath)
	if err != nil {
		var layoutErr *ZipLayoutError
		if errors.As(err, &layoutErr) || errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	for _, f := range files {
		if f == filename {
			return true, nil
		}
	}
	return false, nil
}

// ExtractScript extracts filename from the zip archive at zipPath to a
// temporary file and returns its path. The caller is responsible for removing
// the temp file when done. Returns *ZipFileNotFoundError when the file does
// not exist inside the archive. Transparently handles both flat and
// single-folder layouts.
func ExtractScript(zipPath, filename string) (string, error) {
	content, err := ReadText(zipPath, filename)
	if err != nil {
		return "", err
	}

	ext := filepath.Ext(filename)
	tmp, err := os.CreateTemp("", "raymond-script-*"+ext)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file for script: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("failed to write temp script file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("failed to close temp script file: %w", err)
	}
	return tmp.Name(), nil
}

// ListFiles returns the bare filenames of all non-directory entries in the zip
// archive at zipPath, stripped of any layout prefix. Transparently handles
// both flat and single-folder layouts.
func ListFiles(zipPath string) ([]string, error) {
	prefix, err := DetectLayout(zipPath)
	if err != nil {
		return nil, err
	}

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open zip %s: %w", zipPath, err)
	}
	defer r.Close()

	var files []string
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		name := f.Name
		if prefix != "" {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			name = name[len(prefix):]
		}
		if name != "" {
			files = append(files, name)
		}
	}
	return files, nil
}
