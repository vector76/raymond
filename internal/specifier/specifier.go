// Package specifier resolves raw workflow specifier strings into validated
// absolute scope directories and entry points.
//
// A specifier may point to one of four things:
//   - A directory: ScopeDir = dir, EntryPoint = resolved entry point (1_START or START, any extension)
//   - A .zip archive: ScopeDir = zip path, EntryPoint = resolved entry point (1_START or START, any extension)
//   - An explicit .md file: ScopeDir = parent dir, EntryPoint = filename
//   - An explicit state name (no extension, not an existing directory): ScopeDir = parent dir, EntryPoint = resolved state file
//
// Relative specifiers are resolved against the caller's scope directory.
// For zip callers the effective base is the zip stem path (zip filename minus
// extension), treating the zip as a virtual directory at its own location.
package specifier

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/vector76/raymond/internal/prompts"
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
//     - .zip: verify hash, layout, and resolve entry point (1_START or START)
//     - .md:  verify the file exists
//     - other: three-way disambiguation based on os.Stat:
//       (a) path is a directory → resolve its entry point (1_START or START)
//       (b) path is a non-directory file, or does not exist → treat
//           filepath.Base as an explicit state name within filepath.Dir
//  4. Derive Abbrev: base name (or zip stem), lowercased and capped at 6 chars.
func Resolve(rawSpecifier string, callerScopeDir string) (Resolution, error) {
	// 1. Normalize separators.
	spec := filepath.FromSlash(rawSpecifier)

	// Capture trailing slash before filepath.Join strips it.
	trailingSlash := strings.HasSuffix(spec, string(filepath.Separator))

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
		return resolveDir(spec, trailingSlash)
	}
}

func resolveZip(zipPath string) (Resolution, error) {
	if err := zipscope.VerifyZipHash(zipPath); err != nil {
		return Resolution{}, fmt.Errorf("zip hash verification failed for %q: %w", zipPath, err)
	}
	if _, err := zipscope.DetectLayout(zipPath); err != nil {
		return Resolution{}, fmt.Errorf("invalid zip %q: %w", zipPath, err)
	}
	entryPoint, err := ResolveEntryPoint(zipPath)
	if err != nil {
		return Resolution{}, fmt.Errorf("cannot resolve entry point in zip archive %q: %w", zipPath, err)
	}
	base := filepath.Base(zipPath)
	stem := base[:len(base)-len(filepath.Ext(base))]
	return Resolution{
		ScopeDir:   zipPath,
		EntryPoint: entryPoint,
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

func resolveDir(dirPath string, trailingSlash bool) (Resolution, error) {
	info, err := os.Stat(dirPath)
	if err != nil {
		// ENOTDIR means a path component is a regular file (e.g. foo.zip/STATE).
		// Treat this the same as ENOENT: last component is an entry state name.
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return Resolution{}, err
		}
		// Path doesn't exist or a component is not a directory — treat last component as an entry state name.
		return resolveStateInDir(dirPath, trailingSlash)
	}
	if info.IsDir() {
		// Full path is a directory — resolve its entry point.
		entryPoint, err := ResolveEntryPoint(dirPath)
		if err != nil {
			return Resolution{}, fmt.Errorf("cannot resolve entry point in directory %s: %w", dirPath, err)
		}
		return Resolution{
			ScopeDir:   dirPath,
			EntryPoint: entryPoint,
			Abbrev:     abbrev(filepath.Base(dirPath)),
		}, nil
	}
	// A file exists at the path (not a directory) — treat last component as entry state name.
	return resolveStateInDir(dirPath, trailingSlash)
}

// resolveStateInDir interprets filepath.Base(dirPath) as an extension-less entry
// state name and filepath.Dir(dirPath) as the scope directory.
func resolveStateInDir(dirPath string, trailingSlash bool) (Resolution, error) {
	scopeDir := filepath.Dir(dirPath)
	stateName := filepath.Base(dirPath)

	if _, err := os.Stat(scopeDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Resolution{}, fmt.Errorf("scope directory does not exist: %s", scopeDir)
		}
		return Resolution{}, fmt.Errorf("cannot access scope directory %s: %w", scopeDir, err)
	}

	if strings.ToLower(filepath.Ext(scopeDir)) == ".zip" {
		if trailingSlash {
			return Resolution{}, fmt.Errorf("trailing slash on inner component of zip specifier is not allowed: %q", dirPath)
		}
		if err := zipscope.VerifyZipHash(scopeDir); err != nil {
			return Resolution{}, fmt.Errorf("zip hash verification failed for %q: %w", scopeDir, err)
		}
		if _, err := zipscope.DetectLayout(scopeDir); err != nil {
			return Resolution{}, fmt.Errorf("invalid zip %q: %w", scopeDir, err)
		}
		entryPoint, err := prompts.ResolveState(scopeDir, stateName)
		if err != nil {
			return Resolution{}, fmt.Errorf("cannot resolve state %q in %s: %w", stateName, scopeDir, err)
		}
		base := filepath.Base(scopeDir)
		stem := base[:len(base)-len(filepath.Ext(base))]
		return Resolution{ScopeDir: scopeDir, EntryPoint: entryPoint, Abbrev: abbrev(stem)}, nil
	}

	entryPoint, err := prompts.ResolveState(scopeDir, stateName)
	if err != nil {
		return Resolution{}, fmt.Errorf("cannot resolve state %q in %s: %w", stateName, scopeDir, err)
	}

	return Resolution{
		ScopeDir:   scopeDir,
		EntryPoint: entryPoint,
		Abbrev:     abbrev(filepath.Base(scopeDir)),
	}, nil
}

// ResolveEntryPoint tries to find the workflow entry point in scopeDir.
// It prefers "1_START" (any extension), falls back to "START" (any extension),
// and returns an error if both exist or neither exists.
func ResolveEntryPoint(scopeDir string) (string, error) {
	oneStart, oneErr := prompts.ResolveState(scopeDir, "1_START")
	start, startErr := prompts.ResolveState(scopeDir, "START")

	switch {
	case oneErr == nil && startErr == nil:
		return "", fmt.Errorf(
			"ambiguous entry point: both %s and %s exist; remove one",
			oneStart, start)
	case oneErr == nil:
		return oneStart, nil
	case startErr == nil:
		return start, nil
	default:
		// Both failed. Report the 1_START error since it is the primary
		// entry point name and its error is more specific (e.g. ambiguity
		// between 1_START.md and 1_START.sh).
		return "", fmt.Errorf("no entry point found: %w", oneErr)
	}
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
