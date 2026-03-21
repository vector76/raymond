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
