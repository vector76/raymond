package specifier_test

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/specifier"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// mkDir creates a named subdirectory inside a temp parent, writes 1_START.md,
// and returns the subdirectory path.
func mkDir(t *testing.T, name string) string {
	t.Helper()
	parent := t.TempDir()
	dir := filepath.Join(parent, name)
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("# Start"), 0600))
	return dir
}

// mkZip creates a zip file with the given name inside a temp directory and
// populates it with the provided filename→content map. Returns the zip path.
func mkZip(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	writeZip(t, path, files)
	return path
}

// writeZip creates (or overwrites) a zip file at path with the provided files.
func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
}

// --------------------------------------------------------------------------
// Directory specifier
// --------------------------------------------------------------------------

func TestResolveDir_Absolute(t *testing.T) {
	dir := mkDir(t, "myworkflow")

	res, err := specifier.Resolve(dir, "/caller/scope")

	require.NoError(t, err)
	assert.Equal(t, dir, res.ScopeDir)
	assert.Equal(t, "1_START.md", res.EntryPoint)
}

func TestResolveDir_Relative(t *testing.T) {
	parent := t.TempDir()
	sub := filepath.Join(parent, "sub")
	require.NoError(t, os.MkdirAll(sub, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "1_START.md"), []byte("start"), 0600))

	res, err := specifier.Resolve("sub", parent)

	require.NoError(t, err)
	assert.Equal(t, sub, res.ScopeDir)
	assert.Equal(t, "1_START.md", res.EntryPoint)
}

func TestResolveDir_Missing1Start(t *testing.T) {
	dir := t.TempDir() // no 1_START.md

	_, err := specifier.Resolve(dir, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "1_START.md")
}

func TestResolveDir_NonExistentPath(t *testing.T) {
	_, err := specifier.Resolve("/nonexistent/path/that/cannot/exist/xyz", "")

	require.Error(t, err)
}

// --------------------------------------------------------------------------
// .md specifier
// --------------------------------------------------------------------------

func TestResolveMd_Absolute(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "3_PROCESS.md")
	require.NoError(t, os.WriteFile(mdPath, []byte("# Process"), 0600))

	res, err := specifier.Resolve(mdPath, "/caller/scope")

	require.NoError(t, err)
	assert.Equal(t, dir, res.ScopeDir)
	assert.Equal(t, "3_PROCESS.md", res.EntryPoint)
}

func TestResolveMd_Relative(t *testing.T) {
	parent := t.TempDir()
	mdPath := filepath.Join(parent, "MYSTATE.md")
	require.NoError(t, os.WriteFile(mdPath, []byte("# State"), 0600))

	res, err := specifier.Resolve("MYSTATE.md", parent)

	require.NoError(t, err)
	assert.Equal(t, parent, res.ScopeDir)
	assert.Equal(t, "MYSTATE.md", res.EntryPoint)
}

func TestResolveMd_NonExistent(t *testing.T) {
	_, err := specifier.Resolve("/nonexistent/STATE.md", "")

	require.Error(t, err)
}

// --------------------------------------------------------------------------
// Zip specifier
// --------------------------------------------------------------------------

func TestResolveZip_Absolute(t *testing.T) {
	zipPath := mkZip(t, "workflow.zip", map[string]string{"1_START.md": "start"})

	res, err := specifier.Resolve(zipPath, "/caller/scope")

	require.NoError(t, err)
	assert.Equal(t, zipPath, res.ScopeDir)
	assert.Equal(t, "1_START.md", res.EntryPoint)
}

func TestResolveZip_Relative(t *testing.T) {
	parent := t.TempDir()
	zipPath := filepath.Join(parent, "workflow.zip")
	writeZip(t, zipPath, map[string]string{"1_START.md": "start"})

	res, err := specifier.Resolve("workflow.zip", parent)

	require.NoError(t, err)
	assert.Equal(t, zipPath, res.ScopeDir)
	assert.Equal(t, "1_START.md", res.EntryPoint)
}

func TestResolveZip_Missing1Start(t *testing.T) {
	zipPath := mkZip(t, "test.zip", map[string]string{"OTHER.md": "content"})

	_, err := specifier.Resolve(zipPath, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "1_START.md")
}

func TestResolveZip_BadLayout_Empty(t *testing.T) {
	zipPath := mkZip(t, "empty.zip", map[string]string{})

	_, err := specifier.Resolve(zipPath, "")

	require.Error(t, err)
}

func TestResolveZip_BadLayout_MultipleTopLevelFolders(t *testing.T) {
	zipPath := mkZip(t, "bad.zip", map[string]string{
		"folder1/1_START.md": "start",
		"folder2/OTHER.md":   "other",
	})

	_, err := specifier.Resolve(zipPath, "")

	require.Error(t, err)
}

func TestResolveZip_NonExistent(t *testing.T) {
	_, err := specifier.Resolve("/nonexistent/path/workflow.zip", "")

	require.Error(t, err)
}

// --------------------------------------------------------------------------
// Relative specifier from zip caller
// --------------------------------------------------------------------------

func TestResolveDir_RelativeFromZipCaller(t *testing.T) {
	// Layout: /base/caller.zip (the caller) and /base/sibling/ (the target).
	// The zip is treated as a virtual directory at its stem (/base/caller/),
	// so "../sibling" navigates up to /base/ and then into sibling/ — matching
	// the behaviour of a non-zip caller at /base/caller/.
	base := t.TempDir()
	sibling := filepath.Join(base, "sibling")
	require.NoError(t, os.MkdirAll(sibling, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(sibling, "1_START.md"), []byte("start"), 0600))
	callerZip := filepath.Join(base, "caller.zip") // need not exist; only used as path

	res, err := specifier.Resolve("../sibling", callerZip)

	require.NoError(t, err)
	assert.Equal(t, sibling, res.ScopeDir)
	assert.Equal(t, "1_START.md", res.EntryPoint)
}

func TestResolveZip_RelativeFromZipCaller(t *testing.T) {
	// Layout: /base/caller.zip (the caller) and /base/target.zip (the target).
	// "../target.zip" from the virtual stem /base/caller/ resolves to /base/target.zip.
	base := t.TempDir()
	targetZip := filepath.Join(base, "target.zip")
	writeZip(t, targetZip, map[string]string{"1_START.md": "start"})
	callerZip := filepath.Join(base, "caller.zip") // path only, need not exist

	res, err := specifier.Resolve("../target.zip", callerZip)

	require.NoError(t, err)
	assert.Equal(t, targetZip, res.ScopeDir)
	assert.Equal(t, "1_START.md", res.EntryPoint)
}

func TestResolveMd_RelativeFromZipCaller(t *testing.T) {
	// Layout: /base/caller.zip (the caller) and /base/sibling/STATE.md (the target).
	// "../sibling/STATE.md" from virtual stem /base/caller/ resolves to /base/sibling/STATE.md.
	base := t.TempDir()
	sibling := filepath.Join(base, "sibling")
	require.NoError(t, os.MkdirAll(sibling, 0700))
	mdPath := filepath.Join(sibling, "STATE.md")
	require.NoError(t, os.WriteFile(mdPath, []byte("state"), 0600))
	callerZip := filepath.Join(base, "caller.zip") // path only, need not exist

	res, err := specifier.Resolve("../sibling/STATE.md", callerZip)

	require.NoError(t, err)
	assert.Equal(t, sibling, res.ScopeDir)
	assert.Equal(t, "STATE.md", res.EntryPoint)
}

// --------------------------------------------------------------------------
// Path normalization (forward slashes)
// --------------------------------------------------------------------------

func TestResolve_ForwardSlashInRelativePath(t *testing.T) {
	// A specifier using forward slash should resolve correctly on all platforms.
	parent := t.TempDir()
	sub := filepath.Join(parent, "sub")
	require.NoError(t, os.MkdirAll(sub, 0700))
	mdPath := filepath.Join(sub, "1_START.md")
	require.NoError(t, os.WriteFile(mdPath, []byte("start"), 0600))

	// "sub/1_START.md" uses forward slash — filepath.FromSlash normalizes it.
	res, err := specifier.Resolve("sub/1_START.md", parent)

	require.NoError(t, err)
	assert.Equal(t, sub, res.ScopeDir)
	assert.Equal(t, "1_START.md", res.EntryPoint)
}

// --------------------------------------------------------------------------
// Abbrev derivation
// --------------------------------------------------------------------------

func TestAbbrev_Directory_ShortName(t *testing.T) {
	dir := mkDir(t, "abc")

	res, err := specifier.Resolve(dir, "")

	require.NoError(t, err)
	assert.Equal(t, "abc", res.Abbrev) // 3 chars, no truncation
}

func TestAbbrev_Directory_LongName(t *testing.T) {
	dir := mkDir(t, "toolongname") // 11 chars

	res, err := specifier.Resolve(dir, "")

	require.NoError(t, err)
	assert.Equal(t, "toolon", res.Abbrev) // truncated to 6
}

func TestAbbrev_Directory_Lowercase(t *testing.T) {
	dir := mkDir(t, "MyWorkflow") // mixed case

	res, err := specifier.Resolve(dir, "")

	require.NoError(t, err)
	assert.Equal(t, "mywork", res.Abbrev) // lowercase, truncated to 6
}

func TestAbbrev_Zip_ShortStem(t *testing.T) {
	zipPath := mkZip(t, "ab.zip", map[string]string{"1_START.md": "start"})

	res, err := specifier.Resolve(zipPath, "")

	require.NoError(t, err)
	assert.Equal(t, "ab", res.Abbrev) // 2 chars, no truncation
}

func TestAbbrev_Zip_LongStem(t *testing.T) {
	// "workflow" = 8 chars → truncated to 6 → "workfl"
	zipPath := mkZip(t, "workflow.zip", map[string]string{"1_START.md": "start"})

	res, err := specifier.Resolve(zipPath, "")

	require.NoError(t, err)
	assert.Equal(t, "workfl", res.Abbrev)
}

func TestAbbrev_Md_ShortName(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "GO.md") // stem "GO" = 2 chars

	require.NoError(t, os.WriteFile(mdPath, []byte("go"), 0600))

	res, err := specifier.Resolve(mdPath, "")

	require.NoError(t, err)
	assert.Equal(t, "go", res.Abbrev)
}

func TestAbbrev_Md_LongName(t *testing.T) {
	dir := t.TempDir()
	// stem "3_PROCESS" = 9 chars → lowercase "3_process" → truncated to "3_proc"
	mdPath := filepath.Join(dir, "3_PROCESS.md")
	require.NoError(t, os.WriteFile(mdPath, []byte("process"), 0600))

	res, err := specifier.Resolve(mdPath, "")

	require.NoError(t, err)
	assert.Equal(t, "3_proc", res.Abbrev)
}
