package daemon

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/manifest"
)

// makeWorkflowDir creates a workflow directory with a workflow.yaml manifest
// inside the given root. Returns the workflow directory path. The directory
// name is the workflow id under the derived-id scheme.
func makeWorkflowDir(t *testing.T, root, dirName, manifestYAML string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte(manifestYAML), 0o644))
	return dir
}

// makeWorkflowYaml writes yamlContent to <root>/<filename> and returns the
// absolute file path. Used for YAML workflow files; the filename (with
// extension) becomes the workflow id.
func makeWorkflowYaml(t *testing.T, root, filename, yamlContent string) string {
	t.Helper()
	path := filepath.Join(root, filename)
	require.NoError(t, os.WriteFile(path, []byte(yamlContent), 0o644))
	return path
}

// makeWorkflowZip creates a flat zip archive containing workflow.yaml at the
// root level inside the given root directory. Returns the zip file path. The
// zip filename (with .zip extension) becomes the workflow id.
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
	makeWorkflowDir(t, root, "alpha", `
name: Alpha Workflow
description: First workflow
default_budget: 10.0
`)
	makeWorkflowDir(t, root, "beta", `
name: Beta Workflow
description: Second workflow
input:
  mode: required
  label: Query
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
	assert.Equal(t, filepath.Join(root, "alpha"), entries[0].ScopeDir)
	assert.Equal(t, filepath.Join(root, "alpha", "workflow.yaml"), entries[0].ManifestPath)

	assert.Equal(t, "beta", entries[1].ID)
	assert.Equal(t, "Beta Workflow", entries[1].Name)
	assert.True(t, entries[1].RequiresHumanInput)
	assert.Equal(t, manifest.InputSpec{Mode: "required", Label: "Query"}, entries[1].Input)

	// Workflow without an input block defaults to optional mode.
	assert.Equal(t, manifest.InputSpec{Mode: "optional"}, entries[0].Input)
}

func TestNewRegistry_CapturesBackendField(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "claude-default", `
name: Claude Workflow
`)
	makeWorkflowDir(t, root, "pi-explicit", `
name: Pi Workflow
backend: pi
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 2)

	// Sorted by ID: claude-default comes first.
	assert.Equal(t, "claude-default", entries[0].ID)
	assert.Equal(t, "", entries[0].Backend, "absent backend field stays empty (implicit Claude default)")

	assert.Equal(t, "pi-explicit", entries[1].ID)
	assert.Equal(t, "pi", entries[1].Backend, "manifest backend: pi must surface on the registry entry")
}

func TestNewRegistry_SkipsWorkflowWithoutManifest(t *testing.T) {
	root := t.TempDir()

	// Directory with manifest.
	makeWorkflowDir(t, root, "has-manifest", `name: Has Manifest`)

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
	makeWorkflowDir(t, root, "valid-wf", `name: Valid`)

	// Invalid manifest (rejects with the deprecated id field).
	makeWorkflowDir(t, root, "invalid", "id: should-not-be-here\n")

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "valid-wf", entries[0].ID)
}

func TestNewRegistry_SkipsYAMLScopeDirectory(t *testing.T) {
	root := t.TempDir()

	// Valid workflow.
	makeWorkflowDir(t, root, "valid-wf", `name: Valid`)

	// Directory with a YAML scope (has "states" key) as its workflow.yaml
	// instead of a manifest — that's not a discoverable manifest.
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
name: Packed Workflow
description: A zipped workflow
default_budget: 25.0
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	// The zip's filename (extension included) is the workflow id.
	assert.Equal(t, "packed.zip", entries[0].ID)
	assert.Equal(t, "Packed Workflow", entries[0].Name)
	assert.Equal(t, 25.0, entries[0].DefaultBudget)
	assert.Equal(t, filepath.Join(root, "packed.zip"), entries[0].ScopeDir)
}

func TestNewRegistry_MultipleRoots(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	makeWorkflowDir(t, root1, "a", `name: A`)
	makeWorkflowDir(t, root2, "b", `name: B`)

	reg, err := NewRegistry([]string{root1, root2})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 2)
	assert.Equal(t, "a", entries[0].ID)
	assert.Equal(t, "b", entries[1].ID)
}

func TestListWorkflows_SortedByID(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "zulu", `name: Z`)
	makeWorkflowDir(t, root, "alpha", `name: A`)
	makeWorkflowDir(t, root, "mike", `name: M`)

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
name: My Workflow
description: Test get
`)
	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("my-wf")
	require.True(t, ok)
	assert.Equal(t, "my-wf", entry.ID)
	assert.Equal(t, "My Workflow", entry.Name)
	assert.Equal(t, "Test get", entry.Description)
}

func TestGetWorkflow_NotFound(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "existing", `name: Exists`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("nonexistent")
	assert.False(t, ok)
	assert.Nil(t, entry)
}

func TestRescan_PicksUpNewWorkflows(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "first", `name: F`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "first", entries[0].ID)

	// Add a new workflow after initial scan.
	makeWorkflowDir(t, root, "second", `name: S`)

	require.NoError(t, reg.Rescan())

	entries = reg.ListWorkflows()
	require.Len(t, entries, 2)
	assert.Equal(t, "first", entries[0].ID)
	assert.Equal(t, "second", entries[1].ID)
}

func TestRescan_RemovesDeletedWorkflows(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "keep", `name: K`)
	dir := makeWorkflowDir(t, root, "remove", `name: R`)

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

func TestRescan_PreservesIndexOnError(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "keep", `name: K`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)
	require.Len(t, reg.ListWorkflows(), 1)

	// Make the root unreadable by removing it so scan() errors out.
	require.NoError(t, os.RemoveAll(root))

	require.Error(t, reg.Rescan())

	// The last good index must survive a failed rescan.
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
	makeWorkflowDir(t, root, "no-human", `
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
	makeWorkflowDir(t, root, "auto-human", ``)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("auto-human")
	require.True(t, ok)
	// "auto" resolves to false at discovery time.
	assert.False(t, entry.RequiresHumanInput)
}

func TestNewRegistry_SkipsZipWithoutManifest(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "valid", `name: Valid`)

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
	// A plain file (not a dir, not a .zip, not a .yaml) should be ignored.
	require.NoError(t, os.WriteFile(filepath.Join(root, "readme.txt"), []byte("hello"), 0o644))
	makeWorkflowDir(t, root, "valid", `name: Valid`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "valid", entries[0].ID)
}

func TestNewRegistry_ScansYamlWorkflows(t *testing.T) {
	root := t.TempDir()
	yamlPath := makeWorkflowYaml(t, root, "review.yaml", `
name: Review Workflow
description: Embedded review workflow
input:
  mode: required
  label: Query
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
	// The yaml filename (extension included) is the workflow id.
	assert.Equal(t, "review.yaml", e.ID)
	assert.Equal(t, "Review Workflow", e.Name)
	assert.Equal(t, "Embedded review workflow", e.Description)
	assert.Equal(t, manifest.InputSpec{Mode: "required", Label: "Query"}, e.Input)
	assert.Equal(t, 7.5, e.DefaultBudget)
	assert.True(t, e.RequiresHumanInput)
	assert.Equal(t, yamlPath, e.ScopeDir)
	assert.Equal(t, yamlPath, e.ManifestPath)
}

func TestNewRegistry_ScansMixedScopeTypes(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "dir-wf", `name: Dir WF`)
	makeWorkflowZip(t, root, "packed.zip", `name: Zip WF`)
	makeWorkflowYaml(t, root, "embedded.yaml", `
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
	_, ok = reg.GetWorkflow("packed.zip")
	assert.True(t, ok)
	_, ok = reg.GetWorkflow("embedded.yaml")
	assert.True(t, ok)
}

// TestNewRegistry_RegistersYamlWithoutOptIn asserts every well-formed
// yaml-scope file in a serve root is discoverable — there is no manifest-field
// opt-in (previously the `id:` field served as both id and opt-in signal).
func TestNewRegistry_RegistersYamlWithoutOptIn(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "no-meta.yaml", `
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "no-meta.yaml", entries[0].ID)
}

func TestNewRegistry_SkipsYamlWithoutStates(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "no-states.yaml", `
name: No States Here
description: Not a YAML workflow
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	assert.Empty(t, reg.ListWorkflows())
}

func TestNewRegistry_SkipsYamlWithExplicitID(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "bad.yaml", `
id: deprecated
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	// The deprecated `id:` field is hard-rejected; the file is logged and
	// skipped.
	assert.Empty(t, reg.ListWorkflows())
}

func TestNewRegistry_SkipsYamlWithInvalidHumanInput(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "bad-human.yaml", `
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
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("flow.yml")
	require.True(t, ok)
	assert.Equal(t, "flow.yml", entry.ID)
}

func TestNewRegistry_YamlExtensionCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "Flow.YAML", `
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("Flow.YAML")
	require.True(t, ok)
	assert.Equal(t, "Flow.YAML", entry.ID)
}

func TestNewRegistry_YamlRequiresHumanInput_True(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "wf.yaml", `
requires_human_input: "true"
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("wf.yaml")
	require.True(t, ok)
	assert.True(t, entry.RequiresHumanInput)
}

func TestNewRegistry_YamlRequiresHumanInput_False(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "wf.yaml", `
requires_human_input: "false"
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("wf.yaml")
	require.True(t, ok)
	assert.False(t, entry.RequiresHumanInput)
}

func TestNewRegistry_YamlRequiresHumanInput_AutoResolvesToFalse(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "wf.yaml", `
requires_human_input: "auto"
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("wf.yaml")
	require.True(t, ok)
	assert.False(t, entry.RequiresHumanInput)
}

func TestNewRegistry_YamlRequiresHumanInput_OmittedResolvesToFalse(t *testing.T) {
	root := t.TempDir()
	makeWorkflowYaml(t, root, "wf.yaml", `
states:
  START:
    prompt: Hello
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entry, ok := reg.GetWorkflow("wf.yaml")
	require.True(t, ok)
	assert.False(t, entry.RequiresHumanInput)
}

func TestNewRegistry_YamlAlongsideDirectoryAndZipWorkflows(t *testing.T) {
	root := t.TempDir()

	dirPath := makeWorkflowDir(t, root, "dirflow", `
name: Dir Flow
`)

	zipPath := makeWorkflowZip(t, root, "packed.zip", `
name: Zip Flow
`)

	yamlPath := makeWorkflowYaml(t, root, "review.yaml", `
name: YAML Flow
states:
  START:
    prompt: Hi
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	require.Len(t, reg.ListWorkflows(), 3)

	dirEntry, ok := reg.GetWorkflow("dirflow")
	require.True(t, ok)
	assert.Equal(t, dirPath, dirEntry.ScopeDir)

	zipEntry, ok := reg.GetWorkflow("packed.zip")
	require.True(t, ok)
	assert.Equal(t, zipPath, zipEntry.ScopeDir)

	yamlEntry, ok := reg.GetWorkflow("review.yaml")
	require.True(t, ok)
	assert.Equal(t, yamlPath, yamlEntry.ScopeDir)

	assert.NotEqual(t, dirEntry.ScopeDir, yamlEntry.ScopeDir)
	assert.NotEqual(t, zipEntry.ScopeDir, yamlEntry.ScopeDir)
	assert.NotEqual(t, dirEntry.ScopeDir, zipEntry.ScopeDir)
}

// TestNewRegistry_DirAndZipWithSameBaseNameCoexist verifies the rationale
// behind keeping the extension on file-based ids: a working `foo/` directory
// and a release-snapshot `foo.zip` can register under distinct ids in the
// same root.
func TestNewRegistry_DirAndZipWithSameBaseNameCoexist(t *testing.T) {
	root := t.TempDir()
	makeWorkflowDir(t, root, "foo", `name: Dir Version`)
	makeWorkflowZip(t, root, "foo.zip", `name: Zip Version`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	dirEntry, ok := reg.GetWorkflow("foo")
	require.True(t, ok)
	assert.Equal(t, "Dir Version", dirEntry.Name)

	zipEntry, ok := reg.GetWorkflow("foo.zip")
	require.True(t, ok)
	assert.Equal(t, "Zip Version", zipEntry.Name)
}

func TestNewRegistry_YamlScopeNamedWorkflowYaml(t *testing.T) {
	root := t.TempDir()

	// A yaml scope file literally named workflow.yaml inside a serve root is
	// indistinguishable from a directory-style manifest at the filesystem
	// level — but here the root *is* the serve root, so the file is treated
	// as a yaml-scope workflow whose id is its filename.
	yamlPath := makeWorkflowYaml(t, root, "workflow.yaml", `
name: Named Like A Manifest
states:
  START:
    prompt: Hi
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "workflow.yaml", entries[0].ID)
	assert.Equal(t, yamlPath, entries[0].ScopeDir)
	assert.Equal(t, yamlPath, entries[0].ManifestPath)
}

func TestNewRegistry_YamlScopeInSubdirectoryIsNotDiscovered(t *testing.T) {
	root := t.TempDir()

	// Subdirectory with NO workflow.yaml manifest, but containing a YAML scope
	// file. The registry's single-level scan must not recurse into it.
	subdir := filepath.Join(root, "nested")
	require.NoError(t, os.MkdirAll(subdir, 0o755))
	makeWorkflowYaml(t, subdir, "hidden.yaml", `
states:
  START:
    prompt: Hi
`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	assert.Empty(t, reg.ListWorkflows())
	_, ok := reg.GetWorkflow("hidden.yaml")
	assert.False(t, ok)
}

// TestNewRegistry_SkipsDirectoryWithInvalidName covers basenames that don't
// match the legal-id pattern (e.g. starting with a dot, or containing
// spaces). They're silently skipped rather than erroring the whole scan.
func TestNewRegistry_SkipsDirectoryWithInvalidName(t *testing.T) {
	root := t.TempDir()
	// Hidden dir (leading dot) — also covers the .git case.
	makeWorkflowDir(t, root, ".hidden", `name: Hidden`)
	makeWorkflowDir(t, root, "good", `name: Good`)

	reg, err := NewRegistry([]string{root})
	require.NoError(t, err)

	entries := reg.ListWorkflows()
	require.Len(t, entries, 1)
	assert.Equal(t, "good", entries[0].ID)
}
