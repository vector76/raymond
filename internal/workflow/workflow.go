// Package workflow provides shared utilities for working with Raymond workflow
// state files: listing and filtering state files, reading content, extracting
// transitions and policy data, and graph reachability helpers.
package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/policy"
	"github.com/vector76/raymond/internal/yamlscope"
	"github.com/vector76/raymond/internal/zipscope"
)

// StateExtensions lists recognized state file extensions (lowercase).
var StateExtensions = map[string]bool{
	".md": true, ".sh": true, ".bat": true, ".ps1": true,
}

// ListStateFiles returns filenames of state files in the scope, excluding README.md.
// winMode selects Windows script extensions (.bat/.ps1) over Unix (.sh).
func ListStateFiles(scopeDir string, winMode bool) ([]string, error) {
	var names []string
	if yamlscope.IsYamlScope(scopeDir) {
		files, err := yamlscope.ListFiles(scopeDir)
		if err != nil {
			return nil, err
		}
		names = files
	} else if zipscope.IsZipScope(scopeDir) {
		files, err := zipscope.ListFiles(scopeDir)
		if err != nil {
			return nil, err
		}
		names = files
	} else {
		entries, err := os.ReadDir(scopeDir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
	}
	return FilterStateFiles(names, winMode), nil
}

// FilterStateFiles filters a list of filenames to only state files, applying
// the platform mode to select the appropriate script extension set.
func FilterStateFiles(files []string, winMode bool) []string {
	var result []string
	for _, name := range files {
		ext := strings.ToLower(filepath.Ext(name))
		if !StateExtensions[ext] {
			continue
		}
		if strings.EqualFold(name, "README.md") {
			continue
		}
		switch ext {
		case ".sh":
			if winMode {
				continue
			}
		case ".bat", ".ps1":
			if !winMode {
				continue
			}
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// ReadFileContent reads a file from a YAML, zip, or directory scope.
func ReadFileContent(scopeDir, filename string) (string, error) {
	if yamlscope.IsYamlScope(scopeDir) {
		return yamlscope.ReadText(scopeDir, filename)
	}
	if zipscope.IsZipScope(scopeDir) {
		return zipscope.ReadText(scopeDir, filename)
	}
	data, err := os.ReadFile(filepath.Join(scopeDir, filename))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// StripScriptQuoteEscapes removes shell quoting artifacts from script source
// so that attribute values like return=\"DONE\" or return=`"DONE`" are parsed
// as return="DONE".
func StripScriptQuoteEscapes(s string) string {
	// Bash: \" → "
	s = strings.ReplaceAll(s, `\"`, `"`)
	// PowerShell: `" → "
	s = strings.ReplaceAll(s, "`\"", `"`)
	return s
}

// frontmatterToTransitions converts allowed_transitions entries to Transitions.
func frontmatterToTransitions(entries []map[string]string, filename string) ([]parsing.Transition, []string) {
	var transitions []parsing.Transition
	var warnings []string

	for _, entry := range entries {
		tag := entry["tag"]
		if tag == "" {
			continue
		}
		target := entry["target"]
		// Await uses "next" instead of "target" in frontmatter.
		if tag == "await" {
			target = entry["next"]
		}

		if tag != "result" && target == "" {
			warnings = append(warnings, fmt.Sprintf(
				"%s: frontmatter entry with tag=%q has no target; omitting from diagram",
				filename, tag))
			continue
		}

		attrs := make(map[string]string)
		for k, v := range entry {
			if k != "tag" && k != "target" && k != "payload" {
				attrs[k] = v
			}
		}
		transitions = append(transitions, parsing.Transition{
			Tag:        tag,
			Target:     target,
			Attributes: attrs,
			Payload:    entry["payload"],
		})
	}
	return transitions, warnings
}

// parseBodyTransitions extracts transitions from body text.
// Filters out transitions with multiline targets (spurious matches from
// comments that contain tag-like text) since valid state names are always
// single-line.
func parseBodyTransitions(body, filename string) ([]parsing.Transition, []string) {
	transitions, err := parsing.ParseTransitions(body)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: parse error: %v", filename, err)}
	}
	var filtered []parsing.Transition
	for _, t := range transitions {
		if t.Tag != "result" && strings.Contains(t.Target, "\n") {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered, nil
}

// BFSReachable returns all nodes reachable from start (including start itself).
func BFSReachable(start string, adj map[string][]string) map[string]bool {
	visited := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, next := range adj[node] {
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
	}
	return visited
}

// ExtractFileData reads a state file, parses its transitions and policy, and
// returns all relevant data items.
//
//   - transitions: the parsed transitions for diagram/orchestration use
//   - pol: the parsed policy (may be nil if no frontmatter or not a .md file)
//   - fmErr: frontmatter parse error (non-nil only for .md files with bad YAML)
//   - bodyText: the markdown body after frontmatter is stripped (empty for scripts)
//   - err: I/O or read error
//
// For .md files: calls policy.ParseFrontmatter. If pol is non-nil and has
// AllowedTransitions, transitions come from frontmatter; otherwise they are
// parsed from the body text. If fmErr is non-nil, transitions are parsed from
// the full original content.
//
// For script files: applies StripScriptQuoteEscapes then parses body transitions.
func ExtractFileData(scopeDir, filename string) (transitions []parsing.Transition, pol *policy.Policy, fmErr error, bodyText string, err error) {
	content, err := ReadFileContent(scopeDir, filename)
	if err != nil {
		return nil, nil, nil, "", err
	}

	ext := strings.ToLower(filepath.Ext(filename))

	if ext == ".md" {
		var body string
		pol, body, fmErr = policy.ParseFrontmatter(content)
		if fmErr != nil {
			// Bad frontmatter — parse using full content.
			transitions, _ = parseBodyTransitions(content, filename)
			return transitions, nil, fmErr, "", nil
		}
		bodyText = body
		if pol != nil && len(pol.AllowedTransitions) > 0 {
			transitions, _ = frontmatterToTransitions(pol.AllowedTransitions, filename)
			return transitions, pol, nil, bodyText, nil
		}
		// No frontmatter or empty allowed_transitions — parse body.
		transitions, _ = parseBodyTransitions(body, filename)
		return transitions, pol, nil, bodyText, nil
	}

	// Script file.
	content = StripScriptQuoteEscapes(content)
	transitions, _ = parseBodyTransitions(content, filename)
	return transitions, nil, nil, "", nil
}
