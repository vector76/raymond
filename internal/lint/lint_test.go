package lint_test

import (
	"archive/zip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/lint"
)

func fixtureDir(name string) string {
	return filepath.Join("..", "..", "workflows", "test_cases", "lint", name)
}

// writeTestZip creates a zip file containing the given files and returns the path.
// The zip filename includes its SHA256 hash so zipscope's hash verification passes.
func writeTestZip(t *testing.T, dir string, files map[string]string) string {
	t.Helper()

	// Write zip content to a temp buffer first to compute hash.
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

	// Compute SHA256 of the zip file.
	data, err := os.ReadFile(tmpPath)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	hash := fmt.Sprintf("%x", sum)

	// Rename to include hash in filename.
	finalPath := filepath.Join(dir, hash+".zip")
	require.NoError(t, os.Rename(tmpPath, finalPath))
	return finalPath
}

func TestNoEntryPoint(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("missing_entry"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "no-entry-point" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"no-entry-point\" and Severity==Error, got", diags)
}

func TestAmbiguousEntryPoint(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("ambiguous_entry"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "ambiguous-entry-point" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"ambiguous-entry-point\" and Severity==Error, got", diags)
}

func TestZipScopeValidWorkflow(t *testing.T) {
	dir := t.TempDir()
	zipPath := writeTestZip(t, dir, map[string]string{
		"1_START.md": "<goto>DONE.md</goto>",
		"DONE.md":    "<result>ok</result>",
	})

	diags, err := lint.Lint(zipPath, lint.Options{})
	require.NoError(t, err)
	assert.Empty(t, diags, "expected no diagnostics for valid zip workflow")
}

func TestMissingTarget(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("missing_target"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "missing-target" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"missing-target\" and Severity==Error, got", diags)
}

func TestMissingReturnAbsent(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("missing_return_absent"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "missing-return" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"missing-return\" and Severity==Error, got", diags)
}

func TestMissingReturnBadTarget(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("missing_return_bad_target"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "missing-return" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"missing-return\" and Severity==Error, got", diags)
}

func TestMissingForkNext(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("missing_fork_next"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "missing-fork-next" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"missing-fork-next\" and Severity==Error, got", diags)
}

func TestAmbiguousResolution(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("ambiguous_resolution"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "ambiguous-state-resolution" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"ambiguous-state-resolution\" and Severity==Error, got", diags)
}

func TestForkNextMismatch(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("fork_next_mismatch"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "fork-next-mismatch" && d.Severity == lint.Warning {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"fork-next-mismatch\" and Severity==Warning, got", diags)
}

func TestUnusedAllowedTransition(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("unused_allowed_transition"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "unused-allowed-transition" && d.Severity == lint.Warning {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"unused-allowed-transition\" and Severity==Warning, got", diags)
}

func TestSingleTransitionNoUnusedWarning(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("single_transition_no_warn"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "unused-allowed-transition" {
			assert.Fail(t, "single allowed transition should not produce unused-allowed-transition warning", d)
		}
	}
}

func TestImplicitTransition(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("implicit_transition"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "implicit-transition" && d.Severity == lint.Info {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"implicit-transition\" and Severity==Info, got", diags)
}

func TestScriptStateInfo(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("script_state"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "script-state-no-static-analysis" && d.Severity == lint.Info {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"script-state-no-static-analysis\" and Severity==Info, got", diags)
}

func TestUnreachableState(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("unreachable_state"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "unreachable-state" && d.Severity == lint.Warning {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"unreachable-state\" and Severity==Warning, got", diags)
}

func TestDeadEndState(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("dead_end_state"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "dead-end-state" && d.Severity == lint.Warning {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"dead-end-state\" and Severity==Warning, got", diags)
}

func TestCallWithoutResultPath(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("call_without_result"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "call-without-result-path" && d.Severity == lint.Warning {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"call-without-result-path\" and Severity==Warning, got", diags)
}

func TestFrontmatterParseError(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("bad_frontmatter"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "frontmatter-parse-error" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"frontmatter-parse-error\" and Severity==Error, got", diags)
}

func TestInvalidModel(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("invalid_model"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "invalid-model" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"invalid-model\" and Severity==Error, got", diags)
}

func TestInvalidEffort(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("invalid_effort"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "invalid-effort" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"invalid-effort\" and Severity==Error, got", diags)
}

// writeTestYaml writes a YAML workflow file to dir and returns its path.
func writeTestYaml(t *testing.T, dir string, content string) string {
	t.Helper()
	path := filepath.Join(dir, "workflow.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestYamlScopeValidWorkflow(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writeTestYaml(t, dir, `states:
  1_START:
    prompt: |
      Do the work.
    allowed_transitions:
      - { tag: goto, target: DONE.md }
  DONE:
    prompt: |
      Finished.
    allowed_transitions:
      - { tag: result }
`)

	diags, err := lint.Lint(yamlPath, lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Severity == lint.Error {
			assert.Fail(t, "expected no error-severity diagnostics for valid YAML workflow", d)
		}
	}
}

func TestYamlScopeNoEntryPoint(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writeTestYaml(t, dir, `states:
  REVIEW:
    prompt: |
      Review the work.
    allowed_transitions:
      - { tag: goto, target: DONE.md }
  DONE:
    prompt: |
      Finished.
    allowed_transitions:
      - { tag: result }
`)

	diags, err := lint.Lint(yamlPath, lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "no-entry-point" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"no-entry-point\" and Severity==Error, got", diags)
}

func TestYamlScopeAmbiguousEntryPoint(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writeTestYaml(t, dir, `states:
  1_START:
    prompt: |
      First entry.
    allowed_transitions:
      - { tag: goto, target: DONE.md }
  START:
    prompt: |
      Second entry.
    allowed_transitions:
      - { tag: goto, target: DONE.md }
  DONE:
    prompt: |
      Finished.
    allowed_transitions:
      - { tag: result }
`)

	diags, err := lint.Lint(yamlPath, lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "ambiguous-entry-point" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"ambiguous-entry-point\" and Severity==Error, got", diags)
}

func TestYamlScopeMissingTarget(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writeTestYaml(t, dir, `states:
  1_START:
    prompt: |
      Do the work.
    allowed_transitions:
      - { tag: goto, target: NONEXISTENT.md }
`)

	diags, err := lint.Lint(yamlPath, lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "missing-target" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"missing-target\" and Severity==Error, got", diags)
}

func TestYamlScopeUnreachableState(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writeTestYaml(t, dir, `states:
  1_START:
    prompt: |
      Do the work.
    allowed_transitions:
      - { tag: goto, target: REACHABLE.md }
  REACHABLE:
    prompt: |
      Reachable state.
    allowed_transitions:
      - { tag: result }
  ORPHAN:
    prompt: |
      Orphan state.
    allowed_transitions:
      - { tag: result }
`)

	diags, err := lint.Lint(yamlPath, lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "unreachable-state" && d.Severity == lint.Warning {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"unreachable-state\" and Severity==Warning, got", diags)
}

func TestYamlScopeDeadEnd(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writeTestYaml(t, dir, `states:
  1_START:
    prompt: |
      Do the work.
    allowed_transitions:
      - { tag: goto, target: STUCK.md }
  STUCK:
    prompt: |
      No transitions here.
`)

	diags, err := lint.Lint(yamlPath, lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "dead-end-state" && d.Severity == lint.Warning {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"dead-end-state\" and Severity==Warning, got", diags)
}

func TestZipScopeNoEntryPoint(t *testing.T) {
	dir := t.TempDir()
	zipPath := writeTestZip(t, dir, map[string]string{
		"STEP1.md": "<goto>STEP2.md</goto>",
		"STEP2.md": "<result>done</result>",
	})

	diags, err := lint.Lint(zipPath, lint.Options{})
	require.NoError(t, err)

	found := false
	for _, d := range diags {
		if d.Check == "no-entry-point" && d.Severity == lint.Error {
			found = true
			break
		}
	}
	assert.True(t, found, "expected no-entry-point diagnostic for zip missing entry point, got %v", diags)
}

// writeTaskWorkflow creates a temp dir with 1_START.md (the given content) and
// DONE.md (<result>ok</result>) and returns the dir path.
func writeTaskWorkflow(t *testing.T, startContent string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte(startContent), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "DONE.md"), []byte("<result>ok</result>"), 0o644))
	return dir
}

// hasTaskDiag returns true if diags contains a diagnostic with the given check name.
func hasTaskDiag(diags []lint.Diagnostic, check string) bool {
	for _, d := range diags {
		if d.Check == check {
			return true
		}
	}
	return false
}

// Valid combinations: should produce no invalid-task-value or unsupported-task-attribute errors.

func TestTaskNewOnReset(t *testing.T) {
	dir := writeTaskWorkflow(t, `<reset task="new">DONE.md</reset>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.False(t, hasTaskDiag(diags, "invalid-task-value"), "unexpected invalid-task-value: %v", diags)
	assert.False(t, hasTaskDiag(diags, "unsupported-task-attribute"), "unexpected unsupported-task-attribute: %v", diags)
}

func TestTaskNewOnFork(t *testing.T) {
	dir := writeTaskWorkflow(t, `<fork task="new" next="DONE">DONE.md</fork>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.False(t, hasTaskDiag(diags, "invalid-task-value"), "unexpected invalid-task-value: %v", diags)
	assert.False(t, hasTaskDiag(diags, "unsupported-task-attribute"), "unexpected unsupported-task-attribute: %v", diags)
}

func TestTaskInheritOnFork(t *testing.T) {
	dir := writeTaskWorkflow(t, `<fork task="inherit" next="DONE">DONE.md</fork>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.False(t, hasTaskDiag(diags, "invalid-task-value"), "unexpected invalid-task-value: %v", diags)
	assert.False(t, hasTaskDiag(diags, "unsupported-task-attribute"), "unexpected unsupported-task-attribute: %v", diags)
}

func TestTaskNewOnResetWorkflow(t *testing.T) {
	dir := writeTaskWorkflow(t, `<reset-workflow task="new">/some/workflow</reset-workflow>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.False(t, hasTaskDiag(diags, "invalid-task-value"), "unexpected invalid-task-value: %v", diags)
	assert.False(t, hasTaskDiag(diags, "unsupported-task-attribute"), "unexpected unsupported-task-attribute: %v", diags)
}

func TestTaskNewOnForkWorkflow(t *testing.T) {
	dir := writeTaskWorkflow(t, `<fork-workflow task="new" next="DONE">/some/workflow</fork-workflow>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.False(t, hasTaskDiag(diags, "invalid-task-value"), "unexpected invalid-task-value: %v", diags)
	assert.False(t, hasTaskDiag(diags, "unsupported-task-attribute"), "unexpected unsupported-task-attribute: %v", diags)
}

func TestTaskInheritOnForkWorkflow(t *testing.T) {
	dir := writeTaskWorkflow(t, `<fork-workflow task="inherit" next="DONE">/some/workflow</fork-workflow>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.False(t, hasTaskDiag(diags, "invalid-task-value"), "unexpected invalid-task-value: %v", diags)
	assert.False(t, hasTaskDiag(diags, "unsupported-task-attribute"), "unexpected unsupported-task-attribute: %v", diags)
}

// Invalid combinations: unsupported-task-attribute errors.

func TestTaskInheritOnReset(t *testing.T) {
	dir := writeTaskWorkflow(t, `<reset task="inherit">DONE.md</reset>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.True(t, hasTaskDiag(diags, "unsupported-task-attribute"), "expected unsupported-task-attribute, got: %v", diags)
}

func TestTaskInheritOnResetWorkflow(t *testing.T) {
	dir := writeTaskWorkflow(t, `<reset-workflow task="inherit">/some/workflow</reset-workflow>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.True(t, hasTaskDiag(diags, "unsupported-task-attribute"), "expected unsupported-task-attribute, got: %v", diags)
}

func TestTaskNewOnGoto(t *testing.T) {
	dir := writeTaskWorkflow(t, `<goto task="new">DONE.md</goto>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.True(t, hasTaskDiag(diags, "unsupported-task-attribute"), "expected unsupported-task-attribute, got: %v", diags)
}

func TestTaskNewOnCall(t *testing.T) {
	dir := writeTaskWorkflow(t, `<call task="new" return="DONE">DONE.md</call>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.True(t, hasTaskDiag(diags, "unsupported-task-attribute"), "expected unsupported-task-attribute, got: %v", diags)
}

func TestTaskNewOnFunction(t *testing.T) {
	dir := writeTaskWorkflow(t, `<function task="new" return="DONE">DONE.md</function>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.True(t, hasTaskDiag(diags, "unsupported-task-attribute"), "expected unsupported-task-attribute, got: %v", diags)
}

func TestTaskNewOnResult(t *testing.T) {
	dir := writeTaskWorkflow(t, `<result task="new">ok</result>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.True(t, hasTaskDiag(diags, "unsupported-task-attribute"), "expected unsupported-task-attribute, got: %v", diags)
}

func TestTaskNewOnCallWorkflow(t *testing.T) {
	dir := writeTaskWorkflow(t, `<call-workflow task="new" return="DONE">/some/workflow</call-workflow>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.True(t, hasTaskDiag(diags, "unsupported-task-attribute"), "expected unsupported-task-attribute, got: %v", diags)
}

func TestTaskNewOnFunctionWorkflow(t *testing.T) {
	dir := writeTaskWorkflow(t, `<function-workflow task="new" return="DONE">/some/workflow</function-workflow>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.True(t, hasTaskDiag(diags, "unsupported-task-attribute"), "expected unsupported-task-attribute, got: %v", diags)
}

// Invalid value: invalid-task-value error.

func TestTaskInvalidValueOnFork(t *testing.T) {
	dir := writeTaskWorkflow(t, `<fork task="invalid" next="DONE">DONE.md</fork>`)
	diags, err := lint.Lint(dir, lint.Options{})
	require.NoError(t, err)
	assert.True(t, hasTaskDiag(diags, "invalid-task-value"), "expected invalid-task-value, got: %v", diags)
}
