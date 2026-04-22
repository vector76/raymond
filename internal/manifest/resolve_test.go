package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeStateFile writes a .md state file into dir with the given content.
func makeStateFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

// makeManifest writes a workflow.yaml manifest into dir.
func makeManifest(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte(content), 0o644))
}

// --- ResolveRequiresHumanInput: explicit true/false ---

func TestResolveRequiresHumanInput_TrueRegardlessOfStateFiles(t *testing.T) {
	dir := t.TempDir()
	// State file has no await — but manifest says "true".
	makeStateFile(t, dir, "1_START.md", "---\nallowed_transitions:\n  - {tag: goto, target: DONE.md}\n---\nHello\n")
	makeStateFile(t, dir, "DONE.md", "done")

	m := &Manifest{ID: "test", RequiresHumanInput: "true"}
	result, err := ResolveRequiresHumanInput(m, dir, nil)
	require.NoError(t, err)
	assert.True(t, result)
}

func TestResolveRequiresHumanInput_FalseEvenWithAwaitInStates(t *testing.T) {
	dir := t.TempDir()
	// State file declares await — but manifest says "false".
	makeStateFile(t, dir, "1_START.md", "---\nallowed_transitions:\n  - {tag: await}\n---\nWaiting\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "false"}
	result, err := ResolveRequiresHumanInput(m, dir, nil)
	require.NoError(t, err)
	assert.False(t, result)
}

// --- ResolveRequiresHumanInput: auto mode ---

func TestResolveRequiresHumanInput_AutoWithAwaitInStates(t *testing.T) {
	dir := t.TempDir()
	makeStateFile(t, dir, "1_START.md", "---\nallowed_transitions:\n  - {tag: goto, target: WAIT.md}\n---\nStart\n")
	makeStateFile(t, dir, "WAIT.md", "---\nallowed_transitions:\n  - {tag: await}\n---\nWaiting for input\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, dir, nil)
	require.NoError(t, err)
	assert.True(t, result)
}

func TestResolveRequiresHumanInput_AutoWithoutAwait(t *testing.T) {
	dir := t.TempDir()
	makeStateFile(t, dir, "1_START.md", "---\nallowed_transitions:\n  - {tag: goto, target: DONE.md}\n---\nStart\n")
	makeStateFile(t, dir, "DONE.md", "---\nallowed_transitions:\n  - {tag: result}\n---\nDone\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, dir, nil)
	require.NoError(t, err)
	assert.False(t, result)
}

// --- Transitive propagation ---

func TestResolveRequiresHumanInput_TransitiveCallWorkflow(t *testing.T) {
	// Parent workflow calls child workflow which has await.
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	require.NoError(t, os.Mkdir(child, 0o755))

	// Child workflow has await in its state.
	makeStateFile(t, child, "1_START.md", "---\nallowed_transitions:\n  - {tag: await}\n---\nChild awaits\n")

	// Parent state references child via call-workflow.
	makeStateFile(t, parent, "1_START.md", "---\nallowed_transitions:\n  - {tag: call-workflow, target: ./child/, return: DONE.md}\n---\nCalling child\n")
	makeStateFile(t, parent, "DONE.md", "---\nallowed_transitions:\n  - {tag: result}\n---\nDone\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, parent, nil)
	require.NoError(t, err)
	assert.True(t, result, "should detect await in child workflow via call-workflow")
}

func TestResolveRequiresHumanInput_TransitiveFunctionWorkflow(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	require.NoError(t, os.Mkdir(child, 0o755))

	makeStateFile(t, child, "1_START.md", "---\nallowed_transitions:\n  - {tag: await}\n---\nChild awaits\n")

	// Parent uses function-workflow.
	makeStateFile(t, parent, "1_START.md", "---\nallowed_transitions:\n  - {tag: function-workflow, target: ./child/, return: DONE.md}\n---\nCalling child\n")
	makeStateFile(t, parent, "DONE.md", "---\nallowed_transitions:\n  - {tag: result}\n---\nDone\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, parent, nil)
	require.NoError(t, err)
	assert.True(t, result, "should detect await in child workflow via function-workflow")
}

func TestResolveRequiresHumanInput_TransitiveChildHasManifestTrue(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	require.NoError(t, os.Mkdir(child, 0o755))

	// Child has manifest with requires_human_input: true (no await in states needed).
	makeManifest(t, child, "id: child-wf\nrequires_human_input: \"true\"\n")
	makeStateFile(t, child, "1_START.md", "No await here\n")

	makeStateFile(t, parent, "1_START.md", "---\nallowed_transitions:\n  - {tag: call-workflow, target: ./child/, return: DONE.md}\n---\nCalling child\n")
	makeStateFile(t, parent, "DONE.md", "Done\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, parent, nil)
	require.NoError(t, err)
	assert.True(t, result, "should honour child manifest requires_human_input=true")
}

func TestResolveRequiresHumanInput_TransitiveChildHasManifestFalse(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	require.NoError(t, os.Mkdir(child, 0o755))

	// Child has manifest with requires_human_input: false — even though its
	// state declares await, the manifest takes precedence.
	makeManifest(t, child, "id: child-wf\nrequires_human_input: \"false\"\n")
	makeStateFile(t, child, "1_START.md", "---\nallowed_transitions:\n  - {tag: await}\n---\nWaiting\n")

	makeStateFile(t, parent, "1_START.md", "---\nallowed_transitions:\n  - {tag: call-workflow, target: ./child/, return: DONE.md}\n---\nCalling child\n")
	makeStateFile(t, parent, "DONE.md", "Done\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, parent, nil)
	require.NoError(t, err)
	assert.False(t, result, "child manifest requires_human_input=false overrides await in child states")
}

func TestResolveRequiresHumanInput_TransitiveNoChildManifest(t *testing.T) {
	// Child has no manifest, but has await in states — scan detects it.
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	require.NoError(t, os.Mkdir(child, 0o755))

	makeStateFile(t, child, "1_START.md", "---\nallowed_transitions:\n  - {tag: await}\n---\nWaiting\n")

	makeStateFile(t, parent, "1_START.md", "---\nallowed_transitions:\n  - {tag: call-workflow, target: ./child/, return: DONE.md}\n---\nCalling child\n")
	makeStateFile(t, parent, "DONE.md", "Done\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, parent, nil)
	require.NoError(t, err)
	assert.True(t, result, "should fall back to scanning child states when no manifest exists")
}

func TestResolveRequiresHumanInput_TransitiveNoAwaitAnywhere(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	require.NoError(t, os.Mkdir(child, 0o755))

	makeStateFile(t, child, "1_START.md", "---\nallowed_transitions:\n  - {tag: result}\n---\nDone\n")

	makeStateFile(t, parent, "1_START.md", "---\nallowed_transitions:\n  - {tag: call-workflow, target: ./child/, return: DONE.md}\n---\nCalling child\n")
	makeStateFile(t, parent, "DONE.md", "Done\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, parent, nil)
	require.NoError(t, err)
	assert.False(t, result, "no await anywhere should return false")
}

// --- Cycle detection ---

func TestResolveRequiresHumanInput_CycleDetection(t *testing.T) {
	// Two workflows that reference each other — must not infinite-loop.
	root := t.TempDir()
	wfA := filepath.Join(root, "wf-a")
	wfB := filepath.Join(root, "wf-b")
	require.NoError(t, os.Mkdir(wfA, 0o755))
	require.NoError(t, os.Mkdir(wfB, 0o755))

	// wf-a calls wf-b.
	makeStateFile(t, wfA, "1_START.md", "---\nallowed_transitions:\n  - {tag: call-workflow, target: ../wf-b/, return: DONE.md}\n---\nA\n")
	makeStateFile(t, wfA, "DONE.md", "Done\n")

	// wf-b calls wf-a (cycle).
	makeStateFile(t, wfB, "1_START.md", "---\nallowed_transitions:\n  - {tag: call-workflow, target: ../wf-a/, return: DONE.md}\n---\nB\n")
	makeStateFile(t, wfB, "DONE.md", "Done\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, wfA, nil)
	require.NoError(t, err)
	assert.False(t, result, "cycle should terminate and return false when no await found")
}

// --- No manifest fallback (direct scan) ---

func TestScanScopeForHumanInput_DirectScanWithAwait(t *testing.T) {
	dir := t.TempDir()
	makeStateFile(t, dir, "1_START.md", "---\nallowed_transitions:\n  - {tag: await}\n---\nWaiting\n")

	visited := make(map[string]bool)
	result, err := scanScopeForHumanInput(dir, nil, visited)
	require.NoError(t, err)
	assert.True(t, result)
}

func TestScanScopeForHumanInput_DirectScanWithoutAwait(t *testing.T) {
	dir := t.TempDir()
	makeStateFile(t, dir, "1_START.md", "---\nallowed_transitions:\n  - {tag: goto, target: DONE.md}\n---\nHello\n")
	makeStateFile(t, dir, "DONE.md", "Done\n")

	visited := make(map[string]bool)
	result, err := scanScopeForHumanInput(dir, nil, visited)
	require.NoError(t, err)
	assert.False(t, result)
}

// --- Edge cases ---

func TestResolveRequiresHumanInput_NoStateFiles(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, dir, nil)
	require.NoError(t, err)
	assert.False(t, result, "empty directory should return false")
}

func TestResolveRequiresHumanInput_NonMDFilesIgnored(t *testing.T) {
	dir := t.TempDir()
	// Only a .sh file with await-like text — should not be detected.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "1_START.sh"),
		[]byte("echo '<await>prompt</await>'\n"),
		0o644,
	))

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, dir, nil)
	require.NoError(t, err)
	assert.False(t, result, "script files should not be scanned for frontmatter")
}

func TestResolveRequiresHumanInput_ChildYamlScopeNotManifest(t *testing.T) {
	// Child directory has a workflow.yaml that is a YAML scope (has "states"),
	// not a manifest. FindManifest finds the file, but ParseManifest returns
	// ErrNotManifest. The resolver should fall through and scan the child's
	// .md state files rather than skipping the child entirely.
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	require.NoError(t, os.Mkdir(child, 0o755))

	// workflow.yaml is a YAML scope — ParseManifest will return ErrNotManifest.
	require.NoError(t, os.WriteFile(filepath.Join(child, "workflow.yaml"), []byte(`
states:
  START:
    prompt: hello
    allowed_transitions:
      - { tag: goto, target: WAIT }
  WAIT:
    prompt: waiting
    allowed_transitions:
      - { tag: await }
`), 0o644))
	// Also have a .md state file with await (since we scan .md files, not YAML).
	makeStateFile(t, child, "1_START.md", "---\nallowed_transitions:\n  - {tag: await}\n---\nWaiting\n")

	makeStateFile(t, parent, "1_START.md", "---\nallowed_transitions:\n  - {tag: call-workflow, target: ./child/, return: DONE.md}\n---\nCalling child\n")
	makeStateFile(t, parent, "DONE.md", "Done\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, parent, nil)
	require.NoError(t, err)
	assert.True(t, result, "should fall through to scan when child workflow.yaml is a YAML scope, not a manifest")
}

func TestResolveRequiresHumanInput_UnresolvableCrossWorkflowTarget(t *testing.T) {
	dir := t.TempDir()
	// Reference a nonexistent child — should not error, just skip.
	makeStateFile(t, dir, "1_START.md", "---\nallowed_transitions:\n  - {tag: call-workflow, target: ./nonexistent/, return: DONE.md}\n---\nHello\n")
	makeStateFile(t, dir, "DONE.md", "Done\n")

	m := &Manifest{ID: "test", RequiresHumanInput: "auto"}
	result, err := ResolveRequiresHumanInput(m, dir, nil)
	require.NoError(t, err)
	assert.False(t, result, "unresolvable target should be skipped gracefully")
}
