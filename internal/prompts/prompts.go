// Package prompts handles loading, rendering, and resolving workflow state files.
//
// Four public functions:
//
//   - LoadPrompt  — reads a state file from a directory or zip scope and
//     splits out any YAML frontmatter into a Policy.
//   - RenderPrompt — replaces {{key}} template placeholders with values.
//   - ResolveState — maps an abstract state name (e.g. "NEXT") to a concrete
//     filename (e.g. "NEXT.md"), applying platform and priority rules.
//   - GetStateType — returns "markdown" or "script" based on the file extension,
//     with platform-specific validation.
//
// Resolution priority for abstract state names:
//
//  1. .md   (preferred on all platforms)
//  2. .sh   (Unix only)  / .ps1 or .bat (Windows only; error if both present)
//
// Explicit filenames (e.g. "NEXT.md", "NEXT.sh") skip the search but are still
// validated for platform compatibility.
//
// Zip scopes: when scopeDir ends in ".zip", all file operations are redirected
// to the zip archive via the zipscope package.
package prompts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vector76/raymond/internal/platform"
	"github.com/vector76/raymond/internal/policy"
	"github.com/vector76/raymond/internal/zipscope"
)

// LoadPrompt reads filename from scopeDir (or a zip archive) and parses any
// YAML frontmatter. It returns the body (with frontmatter stripped), the
// Policy (nil when no frontmatter is present), and any error.
//
// filename must not contain "/" or "\" (path traversal defense).
func LoadPrompt(scopeDir, filename string) (string, *policy.Policy, error) {
	if strings.ContainsAny(filename, "/\\") {
		return "", nil, fmt.Errorf(
			"Filename %q contains path separator. Filenames must not contain / or \\",
			filename)
	}

	var content string
	var err error

	if zipscope.IsZipScope(scopeDir) {
		content, err = zipscope.ReadText(scopeDir, filename)
		if err != nil {
			var znf *zipscope.ZipFileNotFoundError
			if errors.As(err, &znf) {
				return "", nil, fmt.Errorf(
					"Prompt file not found in zip archive: %s (in %s)", filename, scopeDir)
			}
			return "", nil, err
		}
	} else {
		promptPath := filepath.Join(scopeDir, filename)
		data, err := os.ReadFile(promptPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", nil, fmt.Errorf("Prompt file not found: %s", promptPath)
			}
			return "", nil, fmt.Errorf("failed to read prompt file %s: %w", promptPath, err)
		}
		content = string(data)
	}

	p, body, err := policy.ParseFrontmatter(content)
	if err != nil {
		return "", nil, err
	}
	return body, p, nil
}

// RenderPrompt replaces every {{key}} placeholder in template with the
// corresponding value from variables. Values that are not strings are
// converted with fmt.Sprintf("%v", value). Placeholders with no matching key
// are left unchanged.
//
// Keys are sorted longest-first before substitution so that longer key names
// (e.g. "firstname") are replaced before shorter ones that are substrings of
// them (e.g. "name"), giving deterministic results regardless of map iteration
// order.
func RenderPrompt(template string, variables map[string]any) string {
	keys := make([]string, 0, len(variables))
	for k := range variables {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })

	result := template
	for _, key := range keys {
		value := variables[key]
		placeholder := "{{" + key + "}}"
		var str string
		if s, ok := value.(string); ok {
			str = s
		} else {
			str = fmt.Sprintf("%v", value)
		}
		result = strings.ReplaceAll(result, placeholder, str)
	}
	return result
}

// ResolveState maps stateName to a concrete filename inside scopeDir.
//
// Abstract names (no extension) are resolved by searching for files with
// recognized extensions in priority order: .md > platform script (.sh on Unix;
// .ps1 or .bat on Windows). If both .md and a platform script exist the name
// is ambiguous — an error is returned. On Windows, if both .ps1 and .bat exist
// (without .md), that is also ambiguous. A wrong-platform script that is the
// only match also returns an error.
//
// Explicit names (with extension) skip the search but are validated for
// platform compatibility and existence.
//
// stateName must not contain "/" or "\" (path traversal defense).
func ResolveState(scopeDir, stateName string) (string, error) {
	if strings.ContainsAny(stateName, "/\\") {
		return "", fmt.Errorf(
			"State name %q contains path separator. State names must not contain / or \\",
			stateName)
	}

	if zipscope.IsZipScope(scopeDir) {
		return resolveStateFromZip(scopeDir, stateName)
	}

	ext := strings.ToLower(filepath.Ext(stateName))
	if ext != "" {
		return resolveExplicitExtension(scopeDir, stateName, ext)
	}
	return resolveAbstractName(scopeDir, stateName)
}

// resolveExplicitExtension handles state names that already include an extension.
func resolveExplicitExtension(scopeDir, stateName, ext string) (string, error) {
	if ext == ".sh" && platform.IsWindows() {
		return "", fmt.Errorf(
			"Cannot use Unix script %q on Windows. Use a .ps1 or .bat file instead.", stateName)
	}
	if (ext == ".bat" || ext == ".ps1") && platform.IsUnix() {
		return "", fmt.Errorf(
			"Cannot use Windows script %q on Unix. Use a .sh file instead.", stateName)
	}
	fullPath := filepath.Join(scopeDir, stateName)
	if _, err := os.Stat(fullPath); errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("State file not found: %s", fullPath)
	}
	return stateName, nil
}

// resolveAbstractName handles state names without an extension.
func resolveAbstractName(scopeDir, stateName string) (string, error) {
	mdPath := filepath.Join(scopeDir, stateName+".md")
	mdExists := pathExists(mdPath)

	if platform.IsWindows() {
		ps1Exists := pathExists(filepath.Join(scopeDir, stateName+".ps1"))
		batExists := pathExists(filepath.Join(scopeDir, stateName+".bat"))
		shExists := pathExists(filepath.Join(scopeDir, stateName+".sh"))

		if mdExists && (ps1Exists || batExists) {
			switch {
			case ps1Exists && batExists:
				return "", fmt.Errorf(
					"Ambiguous state %q: %s.md, %s.ps1, and %s.bat all exist. Use explicit extension.",
					stateName, stateName, stateName, stateName)
			case ps1Exists:
				return "", fmt.Errorf(
					"Ambiguous state %q: both %s.md and %s.ps1 exist. Use explicit extension.",
					stateName, stateName, stateName)
			default:
				return "", fmt.Errorf(
					"Ambiguous state %q: both %s.md and %s.bat exist. Use explicit extension.",
					stateName, stateName, stateName)
			}
		}
		if mdExists {
			return stateName + ".md", nil
		}
		if ps1Exists && batExists {
			return "", fmt.Errorf(
				"Ambiguous state %q: both %s.ps1 and %s.bat exist. Use explicit extension.",
				stateName, stateName, stateName)
		}
		if ps1Exists {
			return stateName + ".ps1", nil
		}
		if batExists {
			return stateName + ".bat", nil
		}
		if shExists {
			return "", fmt.Errorf(
				"State %q not found. Only %s.sh exists, which is not compatible with Windows.",
				stateName, stateName)
		}
		return "", fmt.Errorf(
			"State %q not found in %s. Looked for: %s.md, %s.ps1, %s.bat",
			stateName, scopeDir, stateName, stateName, stateName)
	}

	// Unix path.
	shExists := pathExists(filepath.Join(scopeDir, stateName+".sh"))
	batExists := pathExists(filepath.Join(scopeDir, stateName+".bat"))
	ps1Exists := pathExists(filepath.Join(scopeDir, stateName+".ps1"))

	if mdExists && shExists {
		return "", fmt.Errorf(
			"Ambiguous state %q: both %s.md and %s.sh exist. Use explicit extension.",
			stateName, stateName, stateName)
	}
	if mdExists {
		return stateName + ".md", nil
	}
	if shExists {
		return stateName + ".sh", nil
	}
	switch {
	case batExists && ps1Exists:
		return "", fmt.Errorf(
			"State %q not found. Only Windows scripts (%s.bat, %s.ps1) exist, which are not compatible with this platform.",
			stateName, stateName, stateName)
	case batExists:
		return "", fmt.Errorf(
			"State %q not found. Only %s.bat exists, which is not compatible with this platform.",
			stateName, stateName)
	case ps1Exists:
		return "", fmt.Errorf(
			"State %q not found. Only %s.ps1 exists, which is not compatible with this platform.",
			stateName, stateName)
	}
	return "", fmt.Errorf(
		"State %q not found in %s. Looked for: %s.md, %s.sh",
		stateName, scopeDir, stateName, stateName)
}

// resolveStateFromZip applies the same resolution logic for zip archives.
func resolveStateFromZip(zipPath, stateName string) (string, error) {
	ext := strings.ToLower(filepath.Ext(stateName))

	if ext != "" {
		// Explicit extension — validate platform, then check existence.
		if ext == ".sh" && platform.IsWindows() {
			return "", fmt.Errorf(
				"Cannot use Unix script %q on Windows. Use a .ps1 or .bat file instead.", stateName)
		}
		if (ext == ".bat" || ext == ".ps1") && platform.IsUnix() {
			return "", fmt.Errorf(
				"Cannot use Windows script %q on Unix. Use a .sh file instead.", stateName)
		}
		found, ferr := zipscope.FileExists(zipPath, stateName)
		if ferr != nil {
			return "", fmt.Errorf("checking zip for %s: %w", stateName, ferr)
		}
		if !found {
			return "", fmt.Errorf(
				"State file not found in zip archive: %s (in %s)", stateName, zipPath)
		}
		return stateName, nil
	}

	// Abstract name — list all zip entries once.
	mdName := stateName + ".md"

	available, err := zipscope.ListFiles(zipPath)
	if err != nil {
		return "", fmt.Errorf("failed to list zip contents: %w", err)
	}
	avail := make(map[string]bool, len(available))
	for _, f := range available {
		avail[f] = true
	}

	mdExists := avail[mdName]

	if platform.IsWindows() {
		ps1Name := stateName + ".ps1"
		batName := stateName + ".bat"
		shName := stateName + ".sh"
		ps1Exists := avail[ps1Name]
		batExists := avail[batName]
		shExists := avail[shName]

		if mdExists && (ps1Exists || batExists) {
			switch {
			case ps1Exists && batExists:
				return "", fmt.Errorf(
					"Ambiguous state %q: %s, %s, and %s all exist. Use explicit extension.",
					stateName, mdName, ps1Name, batName)
			case ps1Exists:
				return "", fmt.Errorf(
					"Ambiguous state %q: both %s and %s exist. Use explicit extension.",
					stateName, mdName, ps1Name)
			default:
				return "", fmt.Errorf(
					"Ambiguous state %q: both %s and %s exist. Use explicit extension.",
					stateName, mdName, batName)
			}
		}
		if mdExists {
			return mdName, nil
		}
		if ps1Exists && batExists {
			return "", fmt.Errorf(
				"Ambiguous state %q: both %s and %s exist. Use explicit extension.",
				stateName, ps1Name, batName)
		}
		if ps1Exists {
			return ps1Name, nil
		}
		if batExists {
			return batName, nil
		}
		if shExists {
			return "", fmt.Errorf(
				"State %q not found. Only %s exists, which is not compatible with Windows.",
				stateName, shName)
		}
		return "", fmt.Errorf(
			"State %q not found in %s. Looked for: %s, %s, %s",
			stateName, zipPath, mdName, ps1Name, batName)
	}

	// Unix path.
	shName := stateName + ".sh"
	batName := stateName + ".bat"
	ps1Name := stateName + ".ps1"
	shExists := avail[shName]
	batExists := avail[batName]
	ps1Exists := avail[ps1Name]

	if mdExists && shExists {
		return "", fmt.Errorf(
			"Ambiguous state %q: both %s and %s exist. Use explicit extension.",
			stateName, mdName, shName)
	}
	if mdExists {
		return mdName, nil
	}
	if shExists {
		return shName, nil
	}
	switch {
	case batExists && ps1Exists:
		return "", fmt.Errorf(
			"State %q not found. Only Windows scripts (%s, %s) exist, which are not compatible with this platform.",
			stateName, batName, ps1Name)
	case batExists:
		return "", fmt.Errorf(
			"State %q not found. Only %s exists, which is not compatible with this platform.",
			stateName, batName)
	case ps1Exists:
		return "", fmt.Errorf(
			"State %q not found. Only %s exists, which is not compatible with this platform.",
			stateName, ps1Name)
	}
	return "", fmt.Errorf(
		"State %q not found in %s. Looked for: %s, %s",
		stateName, zipPath, mdName, shName)
}

// GetStateType returns "markdown" for .md files or "script" for the
// platform-appropriate script extension (.sh on Unix; .ps1 or .bat on Windows).
// Any other extension (including the wrong-platform script) returns an error.
func GetStateType(filename string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return "", fmt.Errorf(
			"Unsupported state file %q: no extension. "+
				"State files must have .md, .sh, .ps1, or .bat extension.", filename)
	}
	if ext == ".md" {
		return "markdown", nil
	}
	if ext == ".sh" {
		if platform.IsWindows() {
			return "", fmt.Errorf(
				"Cannot use Unix script %q on Windows. Use a .ps1 or .bat file instead.", filename)
		}
		return "script", nil
	}
	if ext == ".ps1" || ext == ".bat" {
		if platform.IsUnix() {
			return "", fmt.Errorf(
				"Cannot use Windows script %q on Unix. Use a .sh file instead.", filename)
		}
		return "script", nil
	}
	return "", fmt.Errorf(
		"Unsupported state file extension %q in %q. "+
			"Supported extensions: .md, .sh (Unix), .ps1/.bat (Windows).", ext, filename)
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// pathExists reports whether path exists on the filesystem.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
