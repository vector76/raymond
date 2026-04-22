package daemon

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeWorkflowDir creates a workflow directory with a workflow.yaml manifest
// inside the given root. Returns the workflow directory path.
func makeWorkflowDir(t *testing.T, root, dirName, manifestYAML string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte(manifestYAML), 0o644))
	return dir
}

// makeWorkflowYaml writes yamlContent to <root>/<filename> and returns the
// absolute file path. Used for YAML workflow files with embedded manifests.
func makeWorkflowYaml(t *testing.T, root, filename, yamlContent string) string {
	t.Helper()
	path := filepath.Join(root, filename)
	require.NoError(t, os.WriteFile(path, []byte(yamlContent), 0o644))
	return path
}

// makeWorkflowZip creates a flat zip archive containing workflow.yaml at the
// root level inside the given root directory. Returns the zip file path.
func makeWorkflowZip(t *testing.T, root, zipName, manifestYAML string) string {
	t.Helper()
	zipPath := filepath.Join(root, zipName)
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	fw, err := w.Create("workflow.yaml")
	require.NoError(t, err)
	_, err = fw.Write([]byte(manifestYAML))
	require.NoError(t, err)
	// Also add a dummy state file so it's a valid workflow.
	fw2, err := w.Create("1_START.md")
	require.NoError(t, err)
	_, err = fw2.Write([]byte("# Start\nDo something."))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
	return zipPath
}

func TestNewRegistry_ScansDirectoryWorkflows(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "wf-alpha", `
id: alpha
name: Alpha Workflow
description: First workflow
default_budget: 10.0
`)
	makeWorkflowDir(t, root, "wf-beta", `
id: beta
name: Beta Workflow
description: Second workflow
input_schema:
  query: string
requires_human_input: "true"
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 2)

	assert.Equal(t, "alpha", entries[0].ID)
	assert.Equal(t, "Alpha Workflow", entries[0].Name)
	assert.Equal(t, "First workflow", entries[0].Description)
	assert.Equal(t, 10.0, entries[0].DefaultBudget)
	assert.False(t, entries[0].RequiresHumanInput)
	assert.Equal(t, filepath.Join(root, "wf-alpha"), entries[0].ScopeDir)
	assert.Equal(t, filepath.Join(root, "wf-alpha", "workflow.yaml"), entries[0].ManifestPath)

	assert.Equal(t, "beta", entries[1].ID)
	assert.Equal(t, "Beta Workflow", entries[1].Name)
	assert.True(t, entries[1].RequiresHumanInput)
	assert.Equal(t, map[string]string{"query": "string"}, entries[1].InputSchema)
}

func TestNewRegistry_SkipsWorkflowWithoutManifest(t *testing.T) {
	root := t.TempDir()

	// Directory with manifest.
	makeWorkflowDir(t, root, "with-manifest", `id: has-manifest`)

	// Directory without workflow.yaml (just a random file).
	noManifest := filepath.Join(root, "no-manifest")
	require.NoError(t, os.MkdirAll(noManifest, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(noManifest, "1_START.md"), []byte("# Start"), 0o644))

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "has-manifest", entries[0].ID)
}

func TestNewRegistry_SkipsInvalidManifest(t *testing.T) {
	root := t.TempDir()

	// Valid workflow.
	makeWorkflowDir(t, root, "valid", `id: valid-wf`)

	// Invalid manifest (missing required id field).
	makeWorkflowDir(t, root, "invalid", `name: No ID`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "valid-wf", entries[0].ID)
}

func TestNewRegistry_SkipsYAMLScopeFile(t *testing.T) {
	root := t.TempDir()

	// Valid workflow.
	makeWorkflowDir(t, root, "valid", `id: valid-wf`)

	// Directory with a YAML scope (has "states" key) instead of a manifest.
	makeWorkflowDir(t, root, "yaml-scope", `
states:
  START:
    prompt: "Hello"
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "valid-wf", entries[0].ID)
}

func TestNewRegistry_ScansZipArchives(t *testing.T) {
	root := t.TempDir()
	makeWorkflowZip(t, root, "packed.zip", `
id: packed
name: Packed Workflow
description: A zipped workflow
default_budget: 25.0
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "packed", entries[0].ID)
	assert.Equal(t, "Packed Workflow", entries[0].Name)
	assert.Equal(t, 25.0, entries[0].DefaultBudget)
	assert.Equal(t, filepath.Join(root, "packed.zip"), entries[0].ScopeDir)
}

func TestNewRegistry_MultipleRoots(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	makeWorkflowDir(t, root1, "wf-a", `id: a`)
	makeWorkflowDir(t, root2, "wf-b", `id: b`)

	reg, err := NewRegistry([]string{root1, root2})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 2)
	assert.Equal(t, "a", entries[0].ID)
	assert.Equal(t, "b", entries[1].ID)
}

func TestListWorkflows_SortedByID(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "wf-z", `id: zulu`)
	makeWorkflowDir(t, root, "wf-a", `id: alpha`)
	makeWorkflowDir(t, root, "wf-m", `id: mike`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 3)
	assert.Equal(t, "alpha", entries[0].ID)
	assert.Equal(t, "mike", entries[1].ID)
	assert.Equal(t, "zulu", entries[2].ID)
}

func TestGetWorkflow_Found(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "my-wf", `
id: my-wf
name: My Workflow
description: Test get
`)
	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("my-wf")
	assert.True(t, ok)
	assert.Equal(t, "my-wf", entry.ID)
	assert.Equal(t, "My Workflow", entry.Name)
	assert.Equal(t, "Test get", entry.Description)
}

func TestGetWorkflow_NotFound(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "existing", `id: existing`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("nonexistent")
	assert.False(t, ok)
	assert.Nil(t, entry)
}

func TestRescan_PicksUpNewWorkflows(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "first", `id: first`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "first", entries[0].ID)

	// Add a new workflow after initial scan.
	makeWorkflowDir(t, root, "second", `id: second`)

	require.NoError(t, reg.Rescan())

	entries = reg.ListWorkflows()
	require.Len(t, entries, 2)
	assert.Equal(t, "first", entries[0].ID)
	assert.Equal(t, "second", entries[1].ID)
}

func TestRescan_RemovesDeletedWorkflows(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "keep", `id: keep`)
	dir := makeWorkflowDir(t, root, "remove", `id: remove`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)
	require.Len(t, reg.ListWorkflows(), 2)

	// Remove the workflow directory.
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, reg.Rescan())

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "keep", entries[0].ID)
}

func TestNewRegistry_EmptyRoot(t *testing.T) {
	root := t.TempDir()

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	assert.Empty(t, entries)
}

func TestNewRegistry_NonexistentRoot(t *testing.T) {
	_, err := NewRegistry([]string{"/nonexistent/path/that/does/not/exist"})
	require.Error(t, err)
}

func TestNewRegistry_RequiresHumanInput_False(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "wf", `
id: no-human
requires_human_input: "false"
`)
	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("no-human")
	require.True(t, ok)
	assert.False(t, entry.RequiresHumanInput)
}

func TestNewRegistry_RequiresHumanInput_Auto(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "wf", `id: auto-human`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("auto-human")
	require.True(t, ok)
	// "auto" resolves to false at discovery time.
	assert.False(t, entry.RequiresHumanInput)
}

func TestNewRegistry_SkipsZipWithoutManifest(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "valid", `id: valid`)

	// Create a zip with no workflow.yaml — just a state file.
	zipPath := filepath.Join(root, "no-manifest.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	fw, err := w.Create("1_START.md")
	require.NoError(t, err)
	_, err = fw.Write([]byte("# Start"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "valid", entries[0].ID)
}

func TestNewRegistry_SkipsRegularFiles(t *testing.T) {
	root := t.TempDir()
	// A plain file (not a dir, not a .zip) should be ignored.
	require.NoError(t, os.WriteFile(filepath.Join(root, "readme.txt"), []byte("hello"), 0o644))
	makeWorkflowDir(t, root, "valid", `id: valid`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "valid", entries[0].ID)
}

func TestNewRegistry_ScansYamlWorkflows(t *testing.T) {
	root := t.TempDir()
	yamlPath := makeWorkflowYaml(t, root, "review.yaml", `
id: review
name: Review Workflow
description: Embedded review workflow
input_schema:
  query: string
default_budget: 7.5
requires_human_input: "true"
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)

	e := entries[0]
	assert.Equal(t, "review", e.ID)
	assert.Equal(t, "Review Workflow", e.Name)
	assert.Equal(t, "Embedded review workflow", e.Description)
	assert.Equal(t, map[string]string{"query": "string"}, e.InputSchema)
	assert.Equal(t, 7.5, e.DefaultBudget)
	assert.True(t, e.RequiresHumanInput)
	assert.Equal(t, yamlPath, e.ScopeDir)
	assert.Equal(t, yamlPath, e.ManifestPath)
}

func TestNewRegistry_ScansMixedScopeTypes(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "wf-dir", `id: dir-wf
name: Dir WF
`)
	makeWorkflowZip(t, root, "packed.zip", `id: zip-wf
name: Zip WF
`)
	makeWorkflowYaml(t, root, "embedded.yaml", `
id: yaml-wf
name: Yaml WF
states:
  START:
    prompt: Hi
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 3)

	_, ok := reg.GetWorkflow("dir-wf")
	assert.True(t, ok)
	_, ok = reg.GetWorkflow("zip-wf")
	assert.True(t, ok)
	_, ok = reg.GetWorkflow("yaml-wf")
	assert.True(t, ok)
}

func TestNewRegistry_SkipsYamlWithoutId(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "no-id.yaml", `
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	assert.Empty(t, reg.ListWorkflows())
}

func TestNewRegistry_SkipsYamlWithoutStates(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "no-states.yaml", `
id: looks-like-manifest
name: No States Here
description: Not a YAML workflow
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	assert.Empty(t, reg.ListWorkflows())
}

func TestNewRegistry_SkipsYamlWithEmptyId(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "empty-id.yaml", `
id: ""
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	assert.Empty(t, reg.ListWorkflows())
}

func TestNewRegistry_SkipsYamlWithInvalidHumanInput(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "bad-human.yaml", `
id: bad-human
requires_human_input: "maybe"
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	assert.Empty(t, reg.ListWorkflows())
}

func TestNewRegistry_YamlExtensionYml(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "flow.yml", `
id: yml-ext
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("yml-ext")
	require.True(t, ok)
	assert.Equal(t, "yml-ext", entry.ID)
}

func TestNewRegistry_YamlExtensionCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "Flow.YAML", `
id: case-ext
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("case-ext")
	require.True(t, ok)
	assert.Equal(t, "case-ext", entry.ID)
}

func TestNewRegistry_YamlRequiresHumanInput_True(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "wf.yaml", `
id: yaml-human-true
requires_human_input: "true"
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("yaml-human-true")
	require.True(t, ok)
	assert.True(t, entry.RequiresHumanInput)
}

func TestNewRegistry_YamlRequiresHumanInput_False(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "wf.yaml", `
id: yaml-human-false
requires_human_input: "false"
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("yaml-human-false")
	require.True(t, ok)
	assert.False(t, entry.RequiresHumanInput)
}

func TestNewRegistry_YamlRequiresHumanInput_AutoResolvesToFalse(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "wf.yaml", `
id: yaml-human-auto
requires_human_input: "auto"
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("yaml-human-auto")
	require.True(t, ok)
	assert.False(t, entry.RequiresHumanInput)
}

func TestNewRegistry_YamlRequiresHumanInput_OmittedResolvesToFalse(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "wf.yaml", `
id: yaml-human-omitted
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("yaml-human-omitted")
	require.True(t, ok)
	assert.False(t, entry.RequiresHumanInput)
}
