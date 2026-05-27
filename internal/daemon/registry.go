// Package daemon implements the workflow registry and server components for
// the `ray serve` subcommand.
package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/vector76/raymond/internal/manifest"
	"github.com/vector76/raymond/internal/yamlscope"
	"github.com/vector76/raymond/internal/zipscope"
)

// workflowIDPattern is the legal-id pattern enforced on the basename of any
// discovered workflow (directory name, yaml-file basename, or zip-file
// basename). Must start with an alphanumeric or underscore so we don't
// accidentally register hidden files (e.g. ".git" directories). Dot is
// allowed inside so file extensions survive in ids (e.g. "foo.yaml").
var workflowIDPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// WorkflowEntry holds the indexed metadata for a single discovered workflow.
type WorkflowEntry struct {
	ID                 string
	Name               string
	Description        string
	Input              manifest.InputSpec
	DefaultBudget      float64
	RequiresHumanInput bool
	// Backend is the manifest's declared backend name ("" = default Claude,
	// "pi" = pi backend). Surfaced through the HTTP API so the dashboard can
	// show which backend a workflow uses.
	Backend      string
	ScopeDir     string
	ManifestPath string
}

// Registry holds an index of available workflows discovered by scanning
// configured scope root directories.
type Registry struct {
	mu    sync.RWMutex
	roots []string
	index map[string]WorkflowEntry
}

// NewRegistry creates a Registry that scans the given root directories for:
//
//   - directories containing a workflow.yaml manifest
//   - zip archives containing a workflow.yaml entry
//   - YAML scope files (any file with a top-level `states:` key)
//
// The workflow id for each discovered entry is derived from the on-disk
// basename (directory name for directory scopes; full filename including
// extension for yaml and zip files). Entries whose basename is not a valid
// identifier are skipped.
func NewRegistry(roots []string) (*Registry, error) {
	r := &Registry{
		roots: roots,
		index: make(map[string]WorkflowEntry),
	}
	if err := r.scan(); err != nil {
		return nil, err
	}
	return r, nil
}

// ListWorkflows returns all indexed workflow entries, sorted by ID.
func (r *Registry) ListWorkflows() []WorkflowEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := make([]WorkflowEntry, 0, len(r.index))
	for _, e := range r.index {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ID < entries[j].ID
	})
	return entries
}

// GetWorkflow returns the WorkflowEntry for the given ID and true if found,
// or nil and false if the ID is not in the index.
func (r *Registry) GetWorkflow(id string) (*WorkflowEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	e, ok := r.index[id]
	if !ok {
		return nil, false
	}
	return &e, true
}

// Rescan re-scans all configured roots, rebuilding the workflow index from
// scratch. This picks up newly added or removed workflows. The rebuild is
// atomic: if scanning fails (e.g. a root became unreadable), the previous
// index is left intact rather than wiped, so a transient error can't blank
// out the workflow list.
func (r *Registry) Rescan() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev := r.index
	r.index = make(map[string]WorkflowEntry)
	if err := r.scan(); err != nil {
		r.index = prev
		return err
	}
	return nil
}

// scan walks the configured roots and populates the index. Caller must hold
// r.mu for writing (or be inside NewRegistry before the registry is shared).
func (r *Registry) scan() error {
	for _, root := range r.roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return fmt.Errorf("resolving root %q: %w", root, err)
		}
		entries, err := os.ReadDir(absRoot)
		if err != nil {
			return fmt.Errorf("reading root %q: %w", absRoot, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			fullPath := filepath.Join(absRoot, name)

			if entry.IsDir() {
				r.tryIndexDir(fullPath)
			} else if zipscope.IsZipScope(fullPath) {
				r.tryIndexZip(fullPath)
			} else if yamlscope.IsYamlScope(fullPath) {
				r.tryIndexYaml(fullPath)
			}
		}
	}
	return nil
}

// deriveID returns the workflow id derived from the on-disk basename of a
// discovery unit. Directories use the bare name; files (yaml and zip) keep
// their extension so a `foo.zip` archive can coexist with a `foo/` directory
// in the same root. Returns ("", false) if the basename isn't a clean id —
// the registry skips-and-logs those.
func deriveID(basename string) (string, bool) {
	if !workflowIDPattern.MatchString(basename) {
		return "", false
	}
	return basename, true
}

// tryIndexDir attempts to load a workflow.yaml manifest from a directory
// and index the workflow if successful. The workflow id is the directory
// basename.
func (r *Registry) tryIndexDir(dir string) {
	id, ok := deriveID(filepath.Base(dir))
	if !ok {
		return // not a workflow-eligible directory name; silently skip
	}
	manifestPath, ok := manifest.FindManifest(dir)
	if !ok {
		return
	}
	m, err := manifest.ParseManifest(manifestPath)
	if err != nil {
		log.Printf("ray serve: skipping workflow %q: %v", dir, err)
		return
	}

	humanInput := resolveHumanInputField(m.RequiresHumanInput)
	r.index[id] = WorkflowEntry{
		ID:                 id,
		Name:               m.Name,
		Description:        m.Description,
		Input:              m.Input,
		DefaultBudget:      m.DefaultBudget,
		RequiresHumanInput: humanInput,
		Backend:            m.Backend.Name,
		ScopeDir:           dir,
		ManifestPath:       manifestPath,
	}
}

// tryIndexZip attempts to read a workflow.yaml manifest from a zip archive
// and index the workflow if successful. The workflow id is the zip basename
// (extension included).
func (r *Registry) tryIndexZip(zipPath string) {
	id, ok := deriveID(filepath.Base(zipPath))
	if !ok {
		log.Printf("ray serve: skipping zip %q: basename is not a valid workflow id", zipPath)
		return
	}
	exists, err := zipscope.FileExists(zipPath, "workflow.yaml")
	if err != nil || !exists {
		return
	}
	content, err := zipscope.ReadText(zipPath, "workflow.yaml")
	if err != nil {
		log.Printf("ray serve: skipping zip %q: %v", zipPath, err)
		return
	}
	m, err := manifest.ParseManifestData([]byte(content))
	if err != nil {
		log.Printf("ray serve: skipping zip %q: %v", zipPath, err)
		return
	}

	humanInput := resolveHumanInputField(m.RequiresHumanInput)
	r.index[id] = WorkflowEntry{
		ID:                 id,
		Name:               m.Name,
		Description:        m.Description,
		Input:              m.Input,
		DefaultBudget:      m.DefaultBudget,
		RequiresHumanInput: humanInput,
		Backend:            m.Backend.Name,
		ScopeDir:           zipPath,
		ManifestPath:       zipPath,
	}
}

// tryIndexYaml indexes a YAML workflow file. The workflow id is the file
// basename (extension included). Every well-formed yaml-scope file in a
// serve root is registered — there is no manifest-field opt-in.
func (r *Registry) tryIndexYaml(yamlPath string) {
	id, ok := deriveID(filepath.Base(yamlPath))
	if !ok {
		log.Printf("ray serve: skipping yaml %q: basename is not a valid workflow id", yamlPath)
		return
	}
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		log.Printf("ray serve: skipping yaml %q: %v", yamlPath, err)
		return
	}
	m, err := manifest.ExtractEmbeddedManifest(data)
	if err != nil {
		log.Printf("ray serve: skipping yaml %q: %v", yamlPath, err)
		return
	}
	if m == nil {
		// Not a yaml-scope file (no "states" key); treat as non-workflow.
		return
	}

	humanInput := resolveHumanInputField(m.RequiresHumanInput)
	r.index[id] = WorkflowEntry{
		ID:                 id,
		Name:               m.Name,
		Description:        m.Description,
		Input:              m.Input,
		DefaultBudget:      m.DefaultBudget,
		RequiresHumanInput: humanInput,
		Backend:            m.Backend.Name,
		ScopeDir:           yamlPath,
		ManifestPath:       yamlPath,
	}
}

// resolveHumanInputField converts the manifest's requires_human_input string
// to a boolean. "true" → true, "false" → false, "auto" → false (the daemon
// cannot perform dynamic auto-detection at discovery time).
func resolveHumanInputField(value string) bool {
	return strings.EqualFold(value, "true")
}
