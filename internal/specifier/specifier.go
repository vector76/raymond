// Package specifier resolves raw workflow specifier strings into validated
// absolute scope directories and entry points.
//
// A specifier may point to one of three things:
//   - A directory: ScopeDir = dir, EntryPoint = "1_START.md"
//   - A .zip archive: ScopeDir = zip path, EntryPoint = "1_START.md"
//   - An explicit .md file: ScopeDir = parent dir, EntryPoint = filename
//
// Relative specifiers are resolved against the caller's scope directory.
// For zip callers the effective base is the zip stem path (zip filename minus
// extension), treating the zip as a virtual directory at its own location.
package specifier

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vector76/raymond/internal/zipscope"
)

// Resolution holds the resolved components of a workflow specifier.
type Resolution struct {
	ScopeDir   string // absolute path (directory or .zip file)
	EntryPoint string // state filename within the scope (e.g. "1_START.md")
	Abbrev     string // short name derived from the specifier base for use in agent IDs
}

// Resolve resolves rawSpecifier into a validated Resolution.
//
// Steps:
//  1. Normalize path separators to OS-native (filepath.FromSlash).
//  2. If not absolute, resolve relative to callerScopeDir. For zip callers
//     the effective base is the zip stem path (zip filename minus extension),
//     so "../sibling/" from caller.zip navigates to the zip's parent directory.
//  3. Classify by extension and validate:
//     - .zip: verify hash, layout, and presence of 1_START.md
//     - .md:  verify the file exists
//     - other: verify filepath.Join(path, "1_START.md") exists
//  4. Derive Abbrev: base name (or zip stem), lowercased and capped at 6 chars.
func Resolve(rawSpecifier string, callerScopeDir string) (Resolution, error) {
	// 1. Normalize separators.
	spec := filepath.FromSlash(rawSpecifier)

	// 2. Resolve relative paths.
	if !filepath.IsAbs(spec) {
		base := callerScopeDir
		if zipscope.IsZipScope(callerScopeDir) {
			// Treat the zip as a virtual directory at its stem path, so that
			// "../sibling/" works the same way for zip callers as for directory
			// callers.  E.g. /wf/workflow1.zip → virtual base /wf/workflow1/.
			base = strings.TrimSuffix(callerScopeDir, filepath.Ext(callerScopeDir))
		}
		spec = filepath.Join(base, spec)
	}

	// 3. Classify and validate.
	switch strings.ToLower(filepath.Ext(spec)) {
	case ".zip":
		return resolveZip(spec)
	case ".md":
		return resolveMd(spec)
	default:
		return resolveDir(spec)
	}
}

func resolveZip(zipPath string) (Resolution, error) {
	if err := zipscope.VerifyZipHash(zipPath); err != nil {
		return Resolution{}, fmt.Errorf("zip hash verification failed for %q: %w", zipPath, err)
	}
	if _, err := zipscope.DetectLayout(zipPath); err != nil {
		return Resolution{}, fmt.Errorf("invalid zip %q: %w", zipPath, err)
	}
	exists, err := zipscope.FileExists(zipPath, "1_START.md")
	if err != nil {
		return Resolution{}, fmt.Errorf("error checking 1_START.md in %q: %w", zipPath, err)
	}
	if !exists {
		return Resolution{}, fmt.Errorf("1_START.md not found in zip archive: %s", zipPath)
	}
	base := filepath.Base(zipPath)
	stem := base[:len(base)-len(filepath.Ext(base))]
	return Resolution{
		ScopeDir:   zipPath,
		EntryPoint: "1_START.md",
		Abbrev:     abbrev(stem),
	}, nil
}

func resolveMd(mdPath string) (Resolution, error) {
	if _, err := os.Stat(mdPath); err != nil {
		return Resolution{}, fmt.Errorf("state file not found: %s", mdPath)
	}
	entryPoint := filepath.Base(mdPath)
	stem := strings.TrimSuffix(entryPoint, filepath.Ext(entryPoint))
	return Resolution{
		ScopeDir:   filepath.Dir(mdPath),
		EntryPoint: entryPoint,
		Abbrev:     abbrev(stem),
	}, nil
}

func resolveDir(dirPath string) (Resolution, error) {
	startFile := filepath.Join(dirPath, "1_START.md")
	if _, err := os.Stat(startFile); err != nil {
		return Resolution{}, fmt.Errorf("1_START.md not found in directory: %s", dirPath)
	}
	return Resolution{
		ScopeDir:   dirPath,
		EntryPoint: "1_START.md",
		Abbrev:     abbrev(filepath.Base(dirPath)),
	}, nil
}

// abbrev derives a short identifier: lowercased base name capped at 6 characters.
// The same truncation rule (lowercase + 6-char limit) is used by HandleFork in
// transitions, but applied there to state filenames rather than workflow names.
func abbrev(baseName string) string {
	lower := strings.ToLower(baseName)
	if len(lower) > 6 {
		return lower[:6]
	}
	return lower
}
