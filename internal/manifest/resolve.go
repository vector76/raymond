package manifest

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/vector76/raymond/internal/policy"
	"github.com/vector76/raymond/internal/specifier"
	"github.com/vector76/raymond/internal/yamlscope"
	"github.com/vector76/raymond/internal/zipscope"
)

// ResolveRequiresHumanInput determines whether a workflow requires human input
// based on the manifest's RequiresHumanInput field.
//
//   - "true"  → always returns true.
//   - "false" → always returns false.
//   - "auto"  → scans state files for await transitions, including transitive
//     propagation through call-workflow and function-workflow targets.
//
// fetcher is used for resolving remote cross-workflow targets (may be nil if
// only local workflows are expected). visited workflows are tracked internally
// to avoid infinite loops from circular cross-workflow references.
func ResolveRequiresHumanInput(m *Manifest, scopeDir string, fetcher specifier.Fetcher) (bool, error) {
	switch m.RequiresHumanInput {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default: // "auto"
		visited := make(map[string]bool)
		return scanScopeForHumanInput(scopeDir, fetcher, visited)
	}
}

// scanScopeForHumanInput recursively scans a workflow scope for await
// transitions. It dispatches to YAML-specific or file-based scanning depending
// on the scope type, and follows call-workflow / function-workflow targets
// transitively.
func scanScopeForHumanInput(scopeDir string, fetcher specifier.Fetcher, visited map[string]bool) (bool, error) {
	absDir, err := filepath.Abs(scopeDir)
	if err != nil {
		return false, err
	}
	if visited[absDir] {
		return false, nil // cycle — already checked
	}
	visited[absDir] = true

	if yamlscope.IsYamlScope(scopeDir) {
		return scanYamlScopeForHumanInput(scopeDir, fetcher, visited)
	}
	return scanFileScopeForHumanInput(scopeDir, fetcher, visited)
}

// scanFileScopeForHumanInput scans .md files in a directory or zip scope.
func scanFileScopeForHumanInput(scopeDir string, fetcher specifier.Fetcher, visited map[string]bool) (bool, error) {
	var files []string
	var err error
	if zipscope.IsZipScope(scopeDir) {
		files, err = zipscope.ListFiles(scopeDir)
	} else {
		files, err = listDirFiles(scopeDir)
	}
	if err != nil {
		return false, err
	}

	var crossTargets []string

	for _, f := range files {
		if !strings.HasSuffix(strings.ToLower(f), ".md") {
			continue
		}
		var content string
		if zipscope.IsZipScope(scopeDir) {
			content, err = zipscope.ReadText(scopeDir, f)
		} else {
			var data []byte
			data, err = os.ReadFile(filepath.Join(scopeDir, f))
			if err == nil {
				content = string(data)
			}
		}
		if err != nil {
			continue // skip unreadable files
		}
		p, _, parseErr := policy.ParseFrontmatter(content)
		if parseErr != nil || p == nil {
			continue
		}
		for _, entry := range p.AllowedTransitions {
			tag := entry["tag"]
			if tag == "await" {
				return true, nil
			}
			if (tag == "call-workflow" || tag == "function-workflow") && entry["target"] != "" {
				crossTargets = append(crossTargets, entry["target"])
			}
		}
	}

	return checkCrossWorkflowTargets(crossTargets, scopeDir, fetcher, visited)
}

// scanYamlScopeForHumanInput scans a YAML scope's states for await transitions.
func scanYamlScopeForHumanInput(yamlPath string, fetcher specifier.Fetcher, visited map[string]bool) (bool, error) {
	wf, err := yamlscope.Parse(yamlPath)
	if err != nil {
		return false, err
	}

	var crossTargets []string

	for _, name := range wf.StateOrder {
		st := wf.States[name]
		for _, entry := range st.AllowedTransitions {
			tag := entry["tag"]
			if tag == "await" {
				return true, nil
			}
			if (tag == "call-workflow" || tag == "function-workflow") && entry["target"] != "" {
				crossTargets = append(crossTargets, entry["target"])
			}
		}
	}

	return checkCrossWorkflowTargets(crossTargets, yamlPath, fetcher, visited)
}

// checkCrossWorkflowTargets resolves each target and recursively checks the
// child workflow for human input requirements.
func checkCrossWorkflowTargets(targets []string, callerScopeDir string, fetcher specifier.Fetcher, visited map[string]bool) (bool, error) {
	for _, target := range targets {
		res, err := specifier.Resolve(target, callerScopeDir)
		if err != nil {
			continue // skip unresolvable targets
		}
		childDir := res.ScopeDir

		// If the child has a valid manifest, honour its RequiresHumanInput.
		// "true" returns immediately; "false" skips this child entirely.
		// Parse failures (including ErrNotManifest for YAML scope files that
		// happen to be named workflow.yaml) fall through to the scan below.
		if manifestPath, ok := FindManifest(childDir); ok {
			childManifest, parseErr := ParseManifest(manifestPath)
			if parseErr == nil {
				switch childManifest.RequiresHumanInput {
				case "true":
					return true, nil
				case "false":
					continue // explicitly false — skip this child
				}
				// "auto" falls through to the scan below.
			}
		}

		// No valid manifest, or manifest says "auto" — scan the child scope.
		found, scanErr := scanScopeForHumanInput(childDir, fetcher, visited)
		if scanErr != nil {
			continue
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// listDirFiles lists non-directory entries in a directory.
func listDirFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
