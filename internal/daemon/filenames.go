package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Filename and path safety errors. These are returned by NormalizeUploadFilename
// and SafeJoinUnderDir so callers can branch with errors.Is and surface
// structured messages to clients.
var (
	ErrFilenameEmpty         = errors.New("filename is empty")
	ErrFilenamePathSeparator = errors.New("filename contains a path separator")
	ErrFilenameNullByte      = errors.New("filename contains a null byte")
	ErrFilenameControlChar   = errors.New("filename contains a control character")
	ErrFilenameLeadingDot    = errors.New("filename has a leading dot")
	ErrFilenameReserved      = errors.New("filename uses a platform-reserved name")

	ErrPathEmpty       = errors.New("path is empty")
	ErrPathAbsolute    = errors.New("path must be relative")
	ErrPathTraversal   = errors.New("path contains traversal segments")
	ErrPathEscapesBase = errors.New("path resolves outside base directory")
)

// windowsReservedNames is the set of Windows-reserved basename stems that are
// forbidden as filenames (compared case-insensitively, with or without an
// extension — e.g. CON.txt is also reserved).
var windowsReservedNames = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {},
	"COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {},
	"LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// NormalizeUploadFilename validates a user-supplied upload filename. It
// rejects path separators, null bytes, ASCII control characters (< 0x20),
// leading dots, platform-reserved names, and the empty string. On success it
// returns the input unchanged — callers can rely on the returned name being
// identical to the input. Filenames are never silently rewritten; non-conforming
// names must be surfaced to the user for correction.
func NormalizeUploadFilename(name string) (string, error) {
	if name == "" {
		return "", ErrFilenameEmpty
	}
	if strings.ContainsAny(name, `/\`) {
		return "", ErrFilenamePathSeparator
	}
	for _, r := range name {
		if r == 0x00 {
			return "", ErrFilenameNullByte
		}
		if r < 0x20 {
			return "", ErrFilenameControlChar
		}
	}
	if name[0] == '.' {
		return "", ErrFilenameLeadingDot
	}
	stem := name
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		stem = name[:dot]
	}
	if _, ok := windowsReservedNames[strings.ToUpper(stem)]; ok {
		return "", ErrFilenameReserved
	}
	return name, nil
}

// SafeJoinUnderDir joins relPath onto baseDir and verifies that the result
// stays within baseDir. It rejects absolute paths and any input whose cleaned
// form contains a leading ".." segment. Symlinks are resolved on the deepest
// existing prefix of the joined path and confirmed to stay under the resolved
// base — so an intermediate symlink that escapes the base is rejected even
// when the final target does not yet exist.
//
// Non-existent targets are otherwise accepted: the function is intended for
// the egress path resolver, which may compute paths for files about to be
// written. A broken symlink encountered during resolution is surfaced as an
// error rather than silently allowed.
func SafeJoinUnderDir(baseDir, relPath string) (string, error) {
	if relPath == "" {
		return "", ErrPathEmpty
	}
	if filepath.IsAbs(relPath) {
		return "", ErrPathAbsolute
	}

	cleaned := filepath.Clean(relPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", ErrPathTraversal
	}

	joined := filepath.Join(baseDir, cleaned)

	rel, err := filepath.Rel(baseDir, joined)
	if err != nil {
		return "", fmt.Errorf("compute relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrPathEscapesBase
	}

	// Resolve symlinks on the deepest existing prefix of the joined path and
	// confirm it remains under the resolved base. Walking up rather than just
	// EvalSymlinks(joined) is essential: it catches escapes through an
	// intermediate symlink even when the final target does not yet exist (the
	// common case for the egress resolver, which computes paths for files about
	// to be written).
	existing := joined
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			existing = ""
			break
		}
		existing = parent
	}
	if existing != "" {
		evalExisting, evalErr := filepath.EvalSymlinks(existing)
		if evalErr != nil {
			return "", fmt.Errorf("evaluate symlinks: %w", evalErr)
		}
		evalBase, baseErr := filepath.EvalSymlinks(baseDir)
		if baseErr != nil {
			return "", fmt.Errorf("evaluate base symlinks: %w", baseErr)
		}
		rel2, relErr := filepath.Rel(evalBase, evalExisting)
		if relErr != nil {
			return "", fmt.Errorf("compute relative path after symlink resolution: %w", relErr)
		}
		if rel2 == ".." || strings.HasPrefix(rel2, ".."+string(filepath.Separator)) {
			return "", ErrPathEscapesBase
		}
	}

	return joined, nil
}
