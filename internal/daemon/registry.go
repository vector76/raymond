// Package daemon implements the workflow registry and server components for
// the raymond serve subcommand.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/vector76/raymond/internal/manifest"
	"github.com/vector76/raymond/internal/zipscope"
)

// WorkflowEntry holds the indexed metadata for a single discovered workflow.
type WorkflowEntry struct {
	ID                 string
	Name               string
	Description        string
	InputSchema        map[string]string
	DefaultBudget      float64
	RequiresHumanInput bool
	ScopeDir           string
	ManifestPath       string
}

// Registry holds an index of available workflows discovered by scanning
// configured scope root directories.
type Registry struct {
	mu    sync.RWMutex
	roots []string
	index map[string]WorkflowEntry
}

// NewRegistry creates a Registry that scans the given root directories for
// workflow directories and zip archives containing workflow.yaml manifests.
// Workflows without manifests are skipped.
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
// scratch. This picks up newly added or removed workflows.
func (r *Registry) Rescan() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.index = make(map[string]WorkflowEntry)
	return r.scan()
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
			}
		}
	}
	return nil
}

// tryIndexDir attempts to load a workflow.yaml manifest from a directory
// and index the workflow if successful.
func (r *Registry) tryIndexDir(dir string) {
	manifestPath, ok := manifest.FindManifest(dir)
	if !ok {
		return
	}
	m, err := manifest.ParseManifest(manifestPath)
	if err != nil {
		return
	}

	humanInput := resolveHumanInputField(m.RequiresHumanInput)
	r.index[m.ID] = WorkflowEntry{
		ID:                 m.ID,
		Name:               m.Name,
		Description:        m.Description,
		InputSchema:        m.InputSchema,
		DefaultBudget:      m.DefaultBudget,
		RequiresHumanInput: humanInput,
		ScopeDir:           dir,
		ManifestPath:       manifestPath,
	}
}

// tryIndexZip attempts to read a workflow.yaml manifest from a zip archive
// and index the workflow if successful.
func (r *Registry) tryIndexZip(zipPath string) {
	exists, err := zipscope.FileExists(zipPath, "workflow.yaml")
	if err != nil || !exists {
		return
	}
	content, err := zipscope.ReadText(zipPath, "workflow.yaml")
	if err != nil {
		return
	}
	m, err := manifest.ParseManifestData([]byte(content))
	if err != nil {
		return
	}

	humanInput := resolveHumanInputField(m.RequiresHumanInput)
	r.index[m.ID] = WorkflowEntry{
		ID:                 m.ID,
		Name:               m.Name,
		Description:        m.Description,
		InputSchema:        m.InputSchema,
		DefaultBudget:      m.DefaultBudget,
		RequiresHumanInput: humanInput,
		ScopeDir:           zipPath,
		ManifestPath:       zipPath,
	}
}

// resolveHumanInputField converts the manifest's requires_human_input string
// to a boolean. "true" → true, "false" → false, "auto" → false (the daemon
// cannot perform dynamic auto-detection at discovery time).
func resolveHumanInputField(value string) bool {
	return strings.EqualFold(value, "true")
}
