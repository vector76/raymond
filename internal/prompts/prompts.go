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
//  2. .sh   (Unix only)  / .bat (Windows only)
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
func RenderPrompt(template string, variables map[string]any) string {
	result := template
	for key, value := range variables {
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
// recognized extensions in priority order: .md > platform script (.sh/.bat).
// If both .md and the platform script exist the name is ambiguous — an error
// is returned. A wrong-platform script that is the only match also returns
// an error.
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
			"Cannot use Unix script %q on Windows. Use a .bat file instead.", stateName)
	}
	if ext == ".bat" && platform.IsUnix() {
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
	platformExt, otherExt := scriptExts()

	mdPath := filepath.Join(scopeDir, stateName+".md")
	scriptPath := filepath.Join(scopeDir, stateName+platformExt)
	otherPath := filepath.Join(scopeDir, stateName+otherExt)

	mdExists := pathExists(mdPath)
	scriptExists := pathExists(scriptPath)
	otherExists := pathExists(otherPath)

	if mdExists && scriptExists {
		return "", fmt.Errorf(
			"Ambiguous state %q: both %s.md and %s%s exist. Use explicit extension.",
			stateName, stateName, stateName, platformExt)
	}
	if mdExists {
		return stateName + ".md", nil
	}
	if scriptExists {
		return stateName + platformExt, nil
	}
	if otherExists {
		return "", fmt.Errorf(
			"State %q not found. Only %s%s exists, which is not compatible with this platform.",
			stateName, stateName, otherExt)
	}
	return "", fmt.Errorf(
		"State %q not found in %s. Looked for: %s.md, %s%s",
		stateName, scopeDir, stateName, stateName, platformExt)
}

// resolveStateFromZip applies the same resolution logic for zip archives.
func resolveStateFromZip(zipPath, stateName string) (string, error) {
	ext := strings.ToLower(filepath.Ext(stateName))

	if ext != "" {
		// Explicit extension — validate platform, then check existence.
		if ext == ".sh" && platform.IsWindows() {
			return "", fmt.Errorf(
				"Cannot use Unix script %q on Windows. Use a .bat file instead.", stateName)
		}
		if ext == ".bat" && platform.IsUnix() {
			return "", fmt.Errorf(
				"Cannot use Windows script %q on Unix. Use a .sh file instead.", stateName)
		}
		if !zipscope.FileExists(zipPath, stateName) {
			return "", fmt.Errorf(
				"State file not found in zip archive: %s (in %s)", stateName, zipPath)
		}
		return stateName, nil
	}

	// Abstract name — list all zip entries once.
	platformExt, otherExt := scriptExts()
	mdName := stateName + ".md"
	scriptName := stateName + platformExt
	otherName := stateName + otherExt

	available, err := zipscope.ListFiles(zipPath)
	if err != nil {
		return "", fmt.Errorf("failed to list zip contents: %w", err)
	}
	avail := make(map[string]bool, len(available))
	for _, f := range available {
		avail[f] = true
	}

	mdExists := avail[mdName]
	scriptExists := avail[scriptName]
	otherExists := avail[otherName]

	if mdExists && scriptExists {
		return "", fmt.Errorf(
			"Ambiguous state %q: both %s and %s exist. Use explicit extension.",
			stateName, mdName, scriptName)
	}
	if mdExists {
		return mdName, nil
	}
	if scriptExists {
		return scriptName, nil
	}
	if otherExists {
		return "", fmt.Errorf(
			"State %q not found. Only %s exists, which is not compatible with this platform.",
			stateName, otherName)
	}
	return "", fmt.Errorf(
		"State %q not found in %s. Looked for: %s, %s",
		stateName, zipPath, mdName, scriptName)
}

// GetStateType returns "markdown" for .md files or "script" for the
// platform-appropriate script extension (.sh on Unix, .bat on Windows).
// Any other extension (including the wrong-platform script) returns an error.
func GetStateType(filename string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return "", fmt.Errorf(
			"Unsupported state file %q: no extension. "+
				"State files must have .md, .sh, or .bat extension.", filename)
	}
	if ext == ".md" {
		return "markdown", nil
	}
	if ext == ".sh" {
		if platform.IsWindows() {
			return "", fmt.Errorf(
				"Cannot use Unix script %q on Windows. Use a .bat file instead.", filename)
		}
		return "script", nil
	}
	if ext == ".bat" {
		if platform.IsUnix() {
			return "", fmt.Errorf(
				"Cannot use Windows script %q on Unix. Use a .sh file instead.", filename)
		}
		return "script", nil
	}
	return "", fmt.Errorf(
		"Unsupported state file extension %q in %q. "+
			"Supported extensions: .md, .sh (Unix), .bat (Windows).", ext, filename)
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// scriptExts returns (platformExt, otherExt) for the current OS.
func scriptExts() (string, string) {
	if platform.IsWindows() {
		return ".bat", ".sh"
	}
	return ".sh", ".bat"
}

// pathExists reports whether path exists on the filesystem.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
