package convert

import (
	"archive/zip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFile is a test helper that writes content to a file in dir.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
}

// writeTestZip creates a zip file containing the given files and returns the path.
func writeTestZip(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	tmpPath := filepath.Join(dir, "workflow_tmp.zip")
	f, err := os.Create(tmpPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(files[name]))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	data, err := os.ReadFile(tmpPath)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	hash := fmt.Sprintf("%x", sum)

	finalPath := filepath.Join(dir, hash+".zip")
	require.NoError(t, os.Rename(tmpPath, finalPath))
	return finalPath
}

func TestConvert_SimpleMarkdownWorkflow(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", "Hello, this is the start.\n<goto>NEXT.md</goto>")
	writeFile(t, dir, "NEXT.md", "You are at NEXT.\n<result>done</result>")

	yamlStr, warnings, err := Convert(dir)
	require.NoError(t, err)
	assert.Empty(t, warnings)

	// START should appear before NEXT (BFS order).
	startIdx := strings.Index(yamlStr, "START:")
	nextIdx := strings.Index(yamlStr, "NEXT:")
	require.NotEqual(t, -1, startIdx)
	require.NotEqual(t, -1, nextIdx)
	assert.Less(t, startIdx, nextIdx)

	// Should contain the prompt text.
	assert.Contains(t, yamlStr, "Hello, this is the start.")
	assert.Contains(t, yamlStr, "You are at NEXT.")

	// Top-level key should be "states".
	assert.True(t, strings.HasPrefix(yamlStr, "states:"))
}

func TestConvert_1STARTPreferred(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", "Entry point.\n<goto>END.md</goto>")
	writeFile(t, dir, "END.md", "<result>finished</result>")

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)

	// 1_START should appear first.
	idx1start := strings.Index(yamlStr, "1_START:")
	idxEnd := strings.Index(yamlStr, "END:")
	require.NotEqual(t, -1, idx1start)
	require.NotEqual(t, -1, idxEnd)
	assert.Less(t, idx1start, idxEnd)
}

func TestConvert_AmbiguousEntryPoint(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", "entry 1")
	writeFile(t, dir, "START.md", "entry 2")

	_, _, err := Convert(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
}

func TestConvert_NoEntryPoint(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "REVIEW.md", "some state")

	_, _, err := Convert(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no entry point")
}

func TestConvert_SkipsReadme(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", "start\n<result>done</result>")
	writeFile(t, dir, "README.md", "# Instructions")

	yamlStr, warnings, err := Convert(dir)
	require.NoError(t, err)
	assert.Empty(t, warnings) // README is silently skipped, no warning.
	assert.NotContains(t, yamlStr, "README")
}

func TestConvert_WarnsNonStateFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", "start\n<result>done</result>")
	writeFile(t, dir, "notes.txt", "some notes")

	_, warnings, err := Convert(dir)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "skipping non-state file: notes.txt")
}

func TestConvert_ScriptState(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", "start\n<goto>BUILD.sh</goto>")
	writeFile(t, dir, "BUILD.sh", "#!/bin/bash\necho \"building\"\n# <result>done</result>")

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)

	// BUILD should appear as a script state with "sh" key.
	assert.Contains(t, yamlStr, "BUILD:")
	assert.Contains(t, yamlStr, "sh:")
	// Raw script should be preserved (no quote escape stripping).
	assert.Contains(t, yamlStr, "#!/bin/bash")
}

func TestConvert_MultiPlatformScripts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", "start\n<goto>DEPLOY.sh</goto>")
	writeFile(t, dir, "DEPLOY.sh", "#!/bin/bash\necho deploy")
	writeFile(t, dir, "DEPLOY.ps1", "Write-Host deploy")
	writeFile(t, dir, "DEPLOY.bat", "@echo deploy")

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)

	// All platform keys should appear in order: sh, ps1, bat.
	assert.Contains(t, yamlStr, "DEPLOY:")
	shIdx := strings.Index(yamlStr, "sh:")
	ps1Idx := strings.Index(yamlStr, "ps1:")
	batIdx := strings.Index(yamlStr, "bat:")
	require.NotEqual(t, -1, shIdx)
	require.NotEqual(t, -1, ps1Idx)
	require.NotEqual(t, -1, batIdx)
	assert.Less(t, shIdx, ps1Idx)
	assert.Less(t, ps1Idx, batIdx)
}

func TestConvert_ConflictingMdAndScript(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", "start\n<goto>BUILD.md</goto>")
	writeFile(t, dir, "BUILD.md", "prompt for build")
	writeFile(t, dir, "BUILD.sh", "#!/bin/bash\necho build")

	_, _, err := Convert(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicting")
	assert.Contains(t, err.Error(), "BUILD")
}

func TestConvert_FrontmatterPolicy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", `---
allowed_transitions:
  - tag: goto
    target: REVIEW.md
  - tag: goto
    target: DONE.md
model: opus
effort: high
---
Please choose a direction.
`)
	writeFile(t, dir, "REVIEW.md", "<goto>DONE.md</goto>\nReview content")
	writeFile(t, dir, "DONE.md", "<result>finished</result>")

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)

	// Targets should be normalized (no .md extension).
	assert.Contains(t, yamlStr, "target: REVIEW")
	assert.Contains(t, yamlStr, "target: DONE")
	// But NOT contain the extension in target values.
	assert.NotContains(t, yamlStr, "target: REVIEW.md")
	assert.NotContains(t, yamlStr, "target: DONE.md")

	// Model and effort should appear.
	assert.Contains(t, yamlStr, "model: opus")
	assert.Contains(t, yamlStr, "effort: high")

	// Prompt should appear.
	assert.Contains(t, yamlStr, "Please choose a direction.")
}

func TestConvert_NormalizesTransitionTargets(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", `---
allowed_transitions:
  - tag: call
    target: HELPER.md
    return: CONTINUE.md
---
Do work.
`)
	writeFile(t, dir, "HELPER.md", "<result>ok</result>")
	writeFile(t, dir, "CONTINUE.md", "<result>done</result>")

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)

	// target and return should be stripped of extensions.
	assert.Contains(t, yamlStr, "target: HELPER")
	assert.Contains(t, yamlStr, "return: CONTINUE")
	assert.NotContains(t, yamlStr, "HELPER.md")
	assert.NotContains(t, yamlStr, "CONTINUE.md")
}

func TestConvert_CrossWorkflowTargetPreserved(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", `---
allowed_transitions:
  - tag: call-workflow
    target: workflows/sub.zip
    return: BACK.md
---
Cross-workflow call.
`)
	writeFile(t, dir, "BACK.md", "<result>done</result>")

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)

	// Cross-workflow target should NOT be stripped.
	assert.Contains(t, yamlStr, "target: workflows/sub.zip")
	// But return should be stripped.
	assert.Contains(t, yamlStr, "return: BACK")
	assert.NotContains(t, yamlStr, "return: BACK.md")
}

func TestConvert_BFSOrdering(t *testing.T) {
	// ALPHA → BRAVO → CHARLIE, ALPHA → DELTA.
	// BRAVO and DELTA at distance 1, CHARLIE at distance 2.
	// Within same distance, alphabetical: BRAVO before DELTA.
	dir := t.TempDir()
	writeFile(t, dir, "START.md", "<goto>BRAVO.md</goto>\n<goto>DELTA.md</goto>")
	writeFile(t, dir, "BRAVO.md", "<goto>CHARLIE.md</goto>")
	writeFile(t, dir, "CHARLIE.md", "<result>done</result>")
	writeFile(t, dir, "DELTA.md", "<result>done</result>")

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)

	// Extract ordering by finding state keys as top-level mapping entries.
	// State keys appear as "\n  NAME:" under the states mapping.
	var stateOrder []string
	for _, line := range strings.Split(yamlStr, "\n") {
		trimmed := strings.TrimRight(line, " ")
		// State keys are indented by 2 spaces under "states:".
		if strings.HasPrefix(trimmed, "  ") && !strings.HasPrefix(trimmed, "    ") && strings.HasSuffix(trimmed, ":") {
			name := strings.TrimSpace(strings.TrimSuffix(trimmed, ":"))
			stateOrder = append(stateOrder, name)
		}
	}

	require.Equal(t, []string{"START", "BRAVO", "DELTA", "CHARLIE"}, stateOrder)
}

func TestConvert_UnreachableStateSorted(t *testing.T) {
	// Unreachable states get MaxInt distance, sorted alphabetically after reachable ones.
	dir := t.TempDir()
	writeFile(t, dir, "START.md", "<goto>A.md</goto>")
	writeFile(t, dir, "A.md", "<result>done</result>")
	writeFile(t, dir, "ORPHAN.md", "I am unreachable")

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)

	startIdx := strings.Index(yamlStr, "START:")
	aIdx := strings.Index(yamlStr, "\n  A:")
	orphanIdx := strings.Index(yamlStr, "ORPHAN:")
	require.NotEqual(t, -1, startIdx)
	require.NotEqual(t, -1, aIdx)
	require.NotEqual(t, -1, orphanIdx)
	assert.Less(t, startIdx, aIdx)
	assert.Less(t, aIdx, orphanIdx)
}

func TestConvert_ZipScope(t *testing.T) {
	dir := t.TempDir()
	zipPath := writeTestZip(t, dir, map[string]string{
		"START.md": "start prompt\n<goto>END.md</goto>",
		"END.md":   "<result>done</result>",
	})

	yamlStr, warnings, err := Convert(zipPath)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Contains(t, yamlStr, "START:")
	assert.Contains(t, yamlStr, "END:")
	assert.Contains(t, yamlStr, "start prompt")
}

func TestConvert_ZipScopeWarnsNonState(t *testing.T) {
	dir := t.TempDir()
	zipPath := writeTestZip(t, dir, map[string]string{
		"START.md": "start\n<result>done</result>",
		"notes.txt": "extra file",
	})

	_, warnings, err := Convert(zipPath)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "notes.txt")
}

func TestConvert_ScriptRawContentPreserved(t *testing.T) {
	// ExtractFileData applies StripScriptQuoteEscapes, but Convert should
	// use ReadFileContent for raw script content in the YAML output.
	dir := t.TempDir()
	writeFile(t, dir, "START.md", "start\n<goto>RUN.sh</goto>")
	writeFile(t, dir, "RUN.sh", `#!/bin/bash
echo \"hello\"
# <result>done</result>`)

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)

	// Raw content with escaped quotes should be preserved.
	assert.Contains(t, yamlStr, `echo \"hello\"`)
}

func TestConvert_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	_, _, err := Convert(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no entry point")
}

func TestConvert_PayloadNotModified(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", `---
allowed_transitions:
  - tag: result
    payload: some.file.md
---
Produce result.
`)

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)
	// payload should not have extension stripped.
	assert.Contains(t, yamlStr, "payload: some.file.md")
}

func TestConvert_ForkNextNormalized(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", `---
allowed_transitions:
  - tag: fork
    target: WORKER.md
    next: CONTINUE.md
---
Forking.
`)
	writeFile(t, dir, "WORKER.md", "<result>done</result>")
	writeFile(t, dir, "CONTINUE.md", "<result>done</result>")

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)
	assert.Contains(t, yamlStr, "target: WORKER")
	assert.Contains(t, yamlStr, "next: CONTINUE")
	assert.NotContains(t, yamlStr, "WORKER.md")
	assert.NotContains(t, yamlStr, "CONTINUE.md")
}

func TestConvert_FrontmatterParseError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", "---\nbad: [yaml: :\n---\nSome prompt.\n<result>done</result>")

	yamlStr, warnings, err := Convert(dir)
	require.NoError(t, err)

	// Should produce a frontmatter parse warning, not a fatal error.
	require.NotEmpty(t, warnings)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "frontmatter parse error") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected frontmatter parse error warning, got: %v", warnings)

	// The state should still appear in output with body text.
	assert.Contains(t, yamlStr, "START:")
	assert.Contains(t, yamlStr, "prompt:")
}

func TestConvert_ScriptEntryPoint(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.sh", "#!/bin/bash\necho \"hello\"\n# <result>done</result>")

	yamlStr, _, err := Convert(dir)
	require.NoError(t, err)

	assert.Contains(t, yamlStr, "START:")
	assert.Contains(t, yamlStr, "sh:")
	assert.Contains(t, yamlStr, "#!/bin/bash")
}

func TestResolveEntryPoint_Multiple1START(t *testing.T) {
	_, err := resolveEntryPoint([]string{"1_START.md", "1_START.sh"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple 1_START")
}

func TestResolveEntryPoint_MultipleSTART(t *testing.T) {
	_, err := resolveEntryPoint([]string{"START.md", "START.sh"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple START")
}
