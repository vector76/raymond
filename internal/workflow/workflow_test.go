package workflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/workflow"
)

// writeFile is a test helper that writes content to a file in dir.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

// --- FilterStateFiles ---

func TestFilterStateFiles_UnixMode(t *testing.T) {
	input := []string{"1_START.md", "step.sh", "step.bat", "step.ps1", "README.md", "notes.txt"}
	got := workflow.FilterStateFiles(input, false)
	assert.Equal(t, []string{"1_START.md", "step.sh"}, got)
}

func TestFilterStateFiles_WindowsMode(t *testing.T) {
	input := []string{"1_START.md", "step.sh", "step.bat", "step.ps1", "README.md"}
	got := workflow.FilterStateFiles(input, true)
	assert.Equal(t, []string{"1_START.md", "step.bat", "step.ps1"}, got)
}

func TestFilterStateFiles_ExcludesREADME(t *testing.T) {
	input := []string{"README.md", "readme.md", "Readme.md", "state.md"}
	got := workflow.FilterStateFiles(input, false)
	assert.Equal(t, []string{"state.md"}, got)
}

func TestFilterStateFiles_Sorted(t *testing.T) {
	input := []string{"z.md", "a.md", "m.md"}
	got := workflow.FilterStateFiles(input, false)
	assert.Equal(t, []string{"a.md", "m.md", "z.md"}, got)
}

func TestFilterStateFiles_Empty(t *testing.T) {
	got := workflow.FilterStateFiles(nil, false)
	assert.Empty(t, got)
}

// --- ListStateFiles ---

func TestListStateFiles_Directory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", "# start")
	writeFile(t, dir, "DONE.md", "# done")
	writeFile(t, dir, "helper.sh", "#!/bin/bash")
	writeFile(t, dir, "README.md", "# readme")
	writeFile(t, dir, "config.json", "{}")

	got, err := workflow.ListStateFiles(dir, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"1_START.md", "DONE.md", "helper.sh"}, got)
}

func TestListStateFiles_WindowsMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", "# start")
	writeFile(t, dir, "run.bat", "@echo off")
	writeFile(t, dir, "run.sh", "#!/bin/bash")

	got, err := workflow.ListStateFiles(dir, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"1_START.md", "run.bat"}, got)
}

func TestListStateFiles_MissingDir(t *testing.T) {
	_, err := workflow.ListStateFiles("/nonexistent/path/xyz", false)
	assert.Error(t, err)
}

// --- BFSReachable ---

func TestBFSReachable_Single(t *testing.T) {
	adj := map[string][]string{}
	got := workflow.BFSReachable("A", adj)
	assert.Equal(t, map[string]bool{"A": true}, got)
}

func TestBFSReachable_Chain(t *testing.T) {
	adj := map[string][]string{
		"A": {"B"},
		"B": {"C"},
	}
	got := workflow.BFSReachable("A", adj)
	assert.Equal(t, map[string]bool{"A": true, "B": true, "C": true}, got)
}

func TestBFSReachable_Cycle(t *testing.T) {
	adj := map[string][]string{
		"A": {"B"},
		"B": {"A", "C"},
	}
	got := workflow.BFSReachable("A", adj)
	assert.Equal(t, map[string]bool{"A": true, "B": true, "C": true}, got)
}

func TestBFSReachable_Unreachable(t *testing.T) {
	adj := map[string][]string{
		"A": {"B"},
		"C": {"D"},
	}
	got := workflow.BFSReachable("A", adj)
	assert.Equal(t, map[string]bool{"A": true, "B": true}, got)
	assert.NotContains(t, got, "C")
	assert.NotContains(t, got, "D")
}

// --- ExtractFileData ---

func TestExtractFileData_BodyFallback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "state.md", `Do work.
<goto>NEXT.md</goto>`)

	transitions, pol, fmErr, bodyText, err := workflow.ExtractFileData(dir, "state.md")
	require.NoError(t, err)
	require.NoError(t, fmErr)
	assert.Nil(t, pol)
	assert.Equal(t, "Do work.\n<goto>NEXT.md</goto>", bodyText)
	require.Len(t, transitions, 1)
	assert.Equal(t, "goto", transitions[0].Tag)
	assert.Equal(t, "NEXT.md", transitions[0].Target)
}

func TestExtractFileData_FrontmatterPreferred(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "state.md", `---
allowed_transitions:
  - { tag: goto, target: REVIEW.md }
---
Do stuff. <goto>WRONG.md</goto>`)

	transitions, pol, fmErr, bodyText, err := workflow.ExtractFileData(dir, "state.md")
	require.NoError(t, err)
	require.NoError(t, fmErr)
	require.NotNil(t, pol)
	assert.NotEmpty(t, bodyText)
	require.Len(t, transitions, 1)
	assert.Equal(t, "goto", transitions[0].Tag)
	assert.Equal(t, "REVIEW.md", transitions[0].Target)
}

func TestExtractFileData_EmptyAllowedTransitionsFallsBackToBody(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "state.md", `---
allowed_transitions: []
---
<goto>BODY_TARGET.md</goto>`)

	transitions, pol, fmErr, _, err := workflow.ExtractFileData(dir, "state.md")
	require.NoError(t, err)
	require.NoError(t, fmErr)
	assert.NotNil(t, pol) // policy was parsed
	require.Len(t, transitions, 1)
	assert.Equal(t, "BODY_TARGET.md", transitions[0].Target)
}

func TestExtractFileData_BadFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "state.md", `---
: bad yaml: [
---
<goto>FALLBACK.md</goto>`)

	transitions, pol, fmErr, _, err := workflow.ExtractFileData(dir, "state.md")
	require.NoError(t, err)
	assert.Error(t, fmErr)
	assert.Nil(t, pol)
	// Falls back to full-content body parse.
	require.Len(t, transitions, 1)
	assert.Equal(t, "FALLBACK.md", transitions[0].Target)
}

func TestExtractFileData_ScriptFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "run.sh", `#!/bin/bash
echo "<goto target=\"NEXT.md\">go</goto>"`)

	transitions, pol, fmErr, bodyText, err := workflow.ExtractFileData(dir, "run.sh")
	require.NoError(t, err)
	require.NoError(t, fmErr)
	assert.Nil(t, pol)
	assert.Empty(t, bodyText) // scripts don't have markdown body
	require.Len(t, transitions, 1)
	assert.Equal(t, "goto", transitions[0].Tag)
}

func TestExtractFileData_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, _, _, _, err := workflow.ExtractFileData(dir, "nonexistent.md")
	assert.Error(t, err)
}

func TestExtractFileData_ResultTransition(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "done.md", `<result>complete</result>`)

	transitions, _, _, _, err := workflow.ExtractFileData(dir, "done.md")
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "result", transitions[0].Tag)
}

// --- YAML scope helpers ---

// writeYaml creates a temp YAML file with the given content and returns its path.
func writeYaml(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// --- ListStateFiles — YAML scope ---

func TestListStateFiles_YamlScope(t *testing.T) {
	yamlPath := writeYaml(t, `states:
  START:
    prompt: "Start the workflow."
  CHECK:
    sh: |
      #!/bin/sh
      echo '<goto>DONE.md</goto>'
  DONE:
    prompt: "All done."
`)
	got, err := workflow.ListStateFiles(yamlPath, false)
	require.NoError(t, err)
	// On Unix: .sh is kept, .md is kept. Sorted.
	assert.Equal(t, []string{"CHECK.sh", "DONE.md", "START.md"}, got)
}

func TestListStateFiles_YamlScope_WindowsMode(t *testing.T) {
	yamlPath := writeYaml(t, `states:
  START:
    prompt: "Start the workflow."
  CHECK:
    sh: |
      #!/bin/sh
      echo hi
    ps1: |
      Write-Host "hi"
  DONE:
    prompt: "All done."
`)
	got, err := workflow.ListStateFiles(yamlPath, true)
	require.NoError(t, err)
	// Windows mode: .sh filtered out, .ps1 kept, .md kept. Sorted.
	assert.Equal(t, []string{"CHECK.ps1", "DONE.md", "START.md"}, got)
}

// --- ReadFileContent — YAML scope ---

func TestReadFileContent_YamlScope_Markdown(t *testing.T) {
	yamlPath := writeYaml(t, `states:
  START:
    prompt: "Hello from YAML."
`)
	got, err := workflow.ReadFileContent(yamlPath, "START.md")
	require.NoError(t, err)
	assert.Contains(t, got, "Hello from YAML.")
}

func TestReadFileContent_YamlScope_Script(t *testing.T) {
	yamlPath := writeYaml(t, `states:
  CHECK:
    sh: |
      #!/bin/sh
      echo done
`)
	got, err := workflow.ReadFileContent(yamlPath, "CHECK.sh")
	require.NoError(t, err)
	assert.Contains(t, got, "#!/bin/sh")
	assert.Contains(t, got, "echo done")
}
