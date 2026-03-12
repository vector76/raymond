package specifier_test

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/specifier"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// mkDir creates a named subdirectory inside a temp parent, writes a 1_START file
// with the given extension (default ".md"), and returns the subdirectory path.
func mkDir(t *testing.T, name string, ext ...string) string {
	t.Helper()
	parent := t.TempDir()
	dir := filepath.Join(parent, name)
	require.NoError(t, os.MkdirAll(dir, 0700))
	e := ".md"
	if len(ext) > 0 {
		e = ext[0]
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START"+e), []byte("# Start"), 0600))
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
	dir := t.TempDir() // no entry point file

	_, err := specifier.Resolve(dir, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "entry point")
}

func TestResolveDir_NonExistentPath(t *testing.T) {
	_, err := specifier.Resolve("/nonexistent/path/that/cannot/exist/xyz", "")

	require.Error(t, err)
}

func TestResolveDir_ExplicitStateName_Success(t *testing.T) {
	// Path doesn't exist as a directory; base is treated as a state name in parent.
	parent := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(parent, "PROCESS.md"), []byte("# Process"), 0600))
	spec := filepath.Join(parent, "PROCESS") // no extension, not a directory

	res, err := specifier.Resolve(spec, "")

	require.NoError(t, err)
	assert.Equal(t, parent, res.ScopeDir)
	assert.Equal(t, "PROCESS.md", res.EntryPoint)
	assert.Equal(t, abbrevOf(filepath.Base(parent)), res.Abbrev)
}

func TestResolveDir_ExplicitStateName_ScopeDirMissing(t *testing.T) {
	// Neither the path nor its parent exists.
	_, err := specifier.Resolve("/nonexistent/parent/MYSTATE", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "scope directory does not exist")
}

func TestResolveDir_ExplicitStateName_ZipParent(t *testing.T) {
	// Scope dir is a directory whose name ends in .zip — unsupported.
	// Using a directory (not a file) ensures os.Stat on the spec path returns
	// ErrNotExist rather than ENOTDIR, so resolveStateInDir is reached and the
	// .zip extension check fires.
	base := t.TempDir()
	zipDir := filepath.Join(base, "workflow.zip") // directory with .zip extension
	require.NoError(t, os.MkdirAll(zipDir, 0700))
	spec := filepath.Join(zipDir, "MYSTATE") // doesn't exist inside zipDir

	_, err := specifier.Resolve(spec, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "zip")
}

func TestResolveDir_ExplicitStateName_StateNotFound(t *testing.T) {
	// Scope dir exists but no matching state file.
	parent := t.TempDir()
	spec := filepath.Join(parent, "NONEXISTENT_STATE")

	_, err := specifier.Resolve(spec, "")

	require.Error(t, err)
}

func TestResolveDir_ExplicitStateName_FileAtPath(t *testing.T) {
	// A regular file exists at the path (not a directory) — treat as state name in parent.
	parent := t.TempDir()
	// The "specifier" path is a file, and there's a PROCESS.md in the same dir.
	require.NoError(t, os.WriteFile(filepath.Join(parent, "PROCESS"), []byte("not a dir"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(parent, "PROCESS.md"), []byte("# Process"), 0600))
	spec := filepath.Join(parent, "PROCESS")

	res, err := specifier.Resolve(spec, "")

	require.NoError(t, err)
	assert.Equal(t, parent, res.ScopeDir)
	assert.Equal(t, "PROCESS.md", res.EntryPoint)
}

func TestResolveDir_ExplicitStateName_AbbrevFromScopeDir(t *testing.T) {
	// Abbrev is derived from the scope directory name, not the state name.
	parent := t.TempDir()
	scopeDir := filepath.Join(parent, "MyWorkflowDir")
	require.NoError(t, os.MkdirAll(scopeDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(scopeDir, "STEP.md"), []byte("# Step"), 0600))
	spec := filepath.Join(scopeDir, "STEP")

	res, err := specifier.Resolve(spec, "")

	require.NoError(t, err)
	assert.Equal(t, "mywork", res.Abbrev) // from "MyWorkflowDir", not "STEP"
}

// abbrevOf mirrors the internal abbrev logic for test assertions.
func abbrevOf(s string) string {
	lower := strings.ToLower(s)
	if len(lower) > 6 {
		return lower[:6]
	}
	return lower
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
	assert.Contains(t, err.Error(), "entry point")
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
// Extension-agnostic 1_START resolution
// --------------------------------------------------------------------------

func TestResolveDir_ShellEntryPoint(t *testing.T) {
	dir := mkDir(t, "shworkflow", ".sh")

	res, err := specifier.Resolve(dir, "/caller/scope")

	require.NoError(t, err)
	assert.Equal(t, dir, res.ScopeDir)
	assert.Equal(t, "1_START.sh", res.EntryPoint)
}

func TestResolveZip_ShellEntryPoint(t *testing.T) {
	zipPath := mkZip(t, "workflow.zip", map[string]string{"1_START.sh": "#!/bin/sh\necho start"})

	res, err := specifier.Resolve(zipPath, "/caller/scope")

	require.NoError(t, err)
	assert.Equal(t, zipPath, res.ScopeDir)
	assert.Equal(t, "1_START.sh", res.EntryPoint)
}

func TestResolveDir_TwoExtensionsFor1Start(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "wf")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("# Start"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.sh"), []byte("#!/bin/sh"), 0600))

	_, err := specifier.Resolve(dir, "/caller/scope")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Ambiguous")
}

// --------------------------------------------------------------------------
// START fallback (when 1_START does not exist)
// --------------------------------------------------------------------------

func TestResolveDir_StartFallback(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "wf")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "START.md"), []byte("# Start"), 0600))

	res, err := specifier.Resolve(dir, "")

	require.NoError(t, err)
	assert.Equal(t, dir, res.ScopeDir)
	assert.Equal(t, "START.md", res.EntryPoint)
}

func TestResolveDir_StartFallbackShell(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "wf")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "START.sh"), []byte("#!/bin/sh"), 0600))

	res, err := specifier.Resolve(dir, "")

	require.NoError(t, err)
	assert.Equal(t, "START.sh", res.EntryPoint)
}

func TestResolveDir_1StartPreferredOverStart(t *testing.T) {
	// When only 1_START exists, it is used even if START does not exist.
	dir := mkDir(t, "wf") // creates 1_START.md

	res, err := specifier.Resolve(dir, "")

	require.NoError(t, err)
	assert.Equal(t, "1_START.md", res.EntryPoint)
}

func TestResolveDir_BothStartAndOneStartIsFatal(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "wf")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("# Start"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "START.md"), []byte("# Start"), 0600))

	_, err := specifier.Resolve(dir, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
}

func TestResolveZip_StartFallback(t *testing.T) {
	zipPath := mkZip(t, "workflow.zip", map[string]string{"START.md": "start"})

	res, err := specifier.Resolve(zipPath, "")

	require.NoError(t, err)
	assert.Equal(t, "START.md", res.EntryPoint)
}

func TestResolveZip_BothStartAndOneStartIsFatal(t *testing.T) {
	zipPath := mkZip(t, "workflow.zip", map[string]string{
		"1_START.md": "start",
		"START.md":   "also start",
	})

	_, err := specifier.Resolve(zipPath, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
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

// --------------------------------------------------------------------------
// Directory-scope explicit entry state (three-way disambiguation)
// --------------------------------------------------------------------------

func TestResolve_DirectoryWithExplicitEntry(t *testing.T) {
	t.Run("happy path .md", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "mywf")
		require.NoError(t, os.MkdirAll(dir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "OTHER_ENTRY.md"), []byte("# Entry"), 0600))
		spec := filepath.Join(dir, "OTHER_ENTRY")

		res, err := specifier.Resolve(spec, "")

		require.NoError(t, err)
		assert.Equal(t, dir, res.ScopeDir)
		assert.Equal(t, "OTHER_ENTRY.md", res.EntryPoint)
		assert.Equal(t, "mywf", res.Abbrev) // first 6 chars of "mywf" lowercased
	})

	t.Run("happy path .sh (Unix only)", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix-only test")
		}
		parent := t.TempDir()
		dir := filepath.Join(parent, "mywf")
		require.NoError(t, os.MkdirAll(dir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "OTHER_ENTRY.sh"), []byte("#!/bin/sh"), 0600))
		spec := filepath.Join(dir, "OTHER_ENTRY")

		res, err := specifier.Resolve(spec, "")

		require.NoError(t, err)
		assert.Equal(t, "OTHER_ENTRY.sh", res.EntryPoint)
	})

	t.Run("happy path .ps1 (Windows only)", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("Windows-only test")
		}
		parent := t.TempDir()
		dir := filepath.Join(parent, "mywf")
		require.NoError(t, os.MkdirAll(dir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "OTHER_ENTRY.ps1"), []byte("# ps1"), 0600))
		spec := filepath.Join(dir, "OTHER_ENTRY")

		res, err := specifier.Resolve(spec, "")

		require.NoError(t, err)
		assert.Equal(t, "OTHER_ENTRY.ps1", res.EntryPoint)
	})

	t.Run("regression - bare directory with 1_START", func(t *testing.T) {
		dir := mkDir(t, "mywf") // creates 1_START.md

		res, err := specifier.Resolve(dir, "")

		require.NoError(t, err)
		assert.Equal(t, dir, res.ScopeDir)
		assert.Equal(t, "1_START.md", res.EntryPoint)
	})

	t.Run("scope directory does not exist", func(t *testing.T) {
		nonexistent := t.TempDir() + "_gone"
		spec := filepath.Join(nonexistent, "mywf", "OTHER_ENTRY")

		_, err := specifier.Resolve(spec, "")

		require.Error(t, err)
		assert.Contains(t, err.Error(), nonexistent)
	})

	t.Run("named entry state not found", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "mywf")
		require.NoError(t, os.MkdirAll(dir, 0700))
		// No OTHER_ENTRY.* file exists
		spec := filepath.Join(dir, "OTHER_ENTRY")

		_, err := specifier.Resolve(spec, "")

		require.Error(t, err)
	})

	t.Run("ambiguity - both .md and platform script", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix-only ambiguity test")
		}
		parent := t.TempDir()
		dir := filepath.Join(parent, "mywf")
		require.NoError(t, os.MkdirAll(dir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "OTHER_ENTRY.md"), []byte("# Entry"), 0600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "OTHER_ENTRY.sh"), []byte("#!/bin/sh"), 0600))
		spec := filepath.Join(dir, "OTHER_ENTRY")

		_, err := specifier.Resolve(spec, "")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "Ambiguous")
	})

	t.Run("full path is a directory with no 1_START or START", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "mywf")
		require.NoError(t, os.MkdirAll(dir, 0700))
		// No entry point files
		spec := filepath.Join(parent, "mywf")

		_, err := specifier.Resolve(spec, "")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "entry point")
	})
}

// --------------------------------------------------------------------------
// Zip-parent branch (zip file as scope directory with explicit state name)
// --------------------------------------------------------------------------

func TestResolveZipParent_HappyPath(t *testing.T) {
	// Spec: /path/to/workflow.zip/MYSTATE — flat layout zip with MYSTATE.md inside.
	base := t.TempDir()
	zipPath := filepath.Join(base, "workflow.zip")
	writeZip(t, zipPath, map[string]string{"MYSTATE.md": "# State"})
	spec := filepath.Join(zipPath, "MYSTATE")

	res, err := specifier.Resolve(spec, "")

	require.NoError(t, err)
	assert.Equal(t, zipPath, res.ScopeDir)
	assert.Equal(t, "MYSTATE.md", res.EntryPoint)
	assert.Equal(t, "workfl", res.Abbrev) // "workflow" → 6 chars
}

func TestResolveZipParent_FolderLayout(t *testing.T) {
	// Single-folder layout zip: all files inside a top-level directory.
	base := t.TempDir()
	zipPath := filepath.Join(base, "mywf.zip")
	writeZip(t, zipPath, map[string]string{"mywf/STEP.md": "# Step"})
	spec := filepath.Join(zipPath, "STEP")

	res, err := specifier.Resolve(spec, "")

	require.NoError(t, err)
	assert.Equal(t, zipPath, res.ScopeDir)
	assert.Equal(t, "STEP.md", res.EntryPoint)
	assert.Equal(t, "mywf", res.Abbrev)
}

func TestResolveZipParent_RelativeSpecifier(t *testing.T) {
	// Relative specifier "workflow.zip/MYSTATE" resolved against a caller directory.
	base := t.TempDir()
	zipPath := filepath.Join(base, "workflow.zip")
	writeZip(t, zipPath, map[string]string{"MYSTATE.md": "# State"})

	res, err := specifier.Resolve("workflow.zip/MYSTATE", base)

	require.NoError(t, err)
	assert.Equal(t, zipPath, res.ScopeDir)
	assert.Equal(t, "MYSTATE.md", res.EntryPoint)
}

func TestResolveZipParent_TrailingSlash(t *testing.T) {
	// "workflow.zip/MYSTATE/" has a trailing slash — should be rejected.
	base := t.TempDir()
	zipPath := filepath.Join(base, "workflow.zip")
	writeZip(t, zipPath, map[string]string{"MYSTATE.md": "# State"})
	// Forward slash is normalized by filepath.FromSlash; trailing slash is captured.
	_, err := specifier.Resolve("workflow.zip/MYSTATE/", base)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "trailing slash")
}

func TestResolveZipParent_BadLayout(t *testing.T) {
	// Zip with multiple top-level folders → layout error.
	base := t.TempDir()
	zipPath := filepath.Join(base, "bad.zip")
	writeZip(t, zipPath, map[string]string{
		"folder1/MYSTATE.md": "# State",
		"folder2/OTHER.md":   "# Other",
	})
	spec := filepath.Join(zipPath, "MYSTATE")

	_, err := specifier.Resolve(spec, "")

	require.Error(t, err)
}

func TestResolveZipParent_StateNotFound(t *testing.T) {
	// Zip is valid but does not contain the requested state.
	base := t.TempDir()
	zipPath := filepath.Join(base, "workflow.zip")
	writeZip(t, zipPath, map[string]string{"OTHER.md": "# Other"})
	spec := filepath.Join(zipPath, "MISSING")

	_, err := specifier.Resolve(spec, "")

	require.Error(t, err)
}

func TestResolveZipParent_AbbrevFromZipStem(t *testing.T) {
	// Abbrev is derived from the zip filename stem, not the state name.
	base := t.TempDir()
	zipPath := filepath.Join(base, "MyPipeline.zip")
	writeZip(t, zipPath, map[string]string{"STEP.md": "# Step"})
	spec := filepath.Join(zipPath, "STEP")

	res, err := specifier.Resolve(spec, "")

	require.NoError(t, err)
	assert.Equal(t, "mypipe", res.Abbrev) // "MyPipeline" → lowercase → truncate to 6
}

// --------------------------------------------------------------------------
// Zip-scope explicit entry state (TestResolve_ZipWithExplicitEntry)
// --------------------------------------------------------------------------

func TestResolve_ZipWithExplicitEntry(t *testing.T) {
	t.Run("regression - bare zip resolves 1_START", func(t *testing.T) {
		// No inner state component — bare zip resolves to 1_START.md as before.
		zipPath := mkZip(t, "wf.zip", map[string]string{"1_START.md": "start"})

		res, err := specifier.Resolve(zipPath, "")

		require.NoError(t, err)
		assert.Equal(t, zipPath, res.ScopeDir)
		assert.Equal(t, "1_START.md", res.EntryPoint)
	})

	t.Run("happy path - .md inside zip", func(t *testing.T) {
		// Zip contains 1_START.md and OTHER_ENTRY.md; explicit entry resolves correctly.
		zipPath := mkZip(t, "mywf.zip", map[string]string{
			"1_START.md":     "start",
			"OTHER_ENTRY.md": "other",
		})
		spec := filepath.Join(zipPath, "OTHER_ENTRY")

		res, err := specifier.Resolve(spec, "")

		require.NoError(t, err)
		assert.Equal(t, zipPath, res.ScopeDir)
		assert.Equal(t, "OTHER_ENTRY.md", res.EntryPoint)
		assert.Equal(t, "mywf", res.Abbrev) // from zip stem "mywf"
	})

	t.Run("trailing slash on inner component", func(t *testing.T) {
		// "{zip_path}/OTHER_ENTRY/" has a trailing slash — must be rejected.
		base := t.TempDir()
		zipPath := filepath.Join(base, "mywf.zip")
		writeZip(t, zipPath, map[string]string{"OTHER_ENTRY.md": "other"})

		_, err := specifier.Resolve("mywf.zip/OTHER_ENTRY/", base)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "trailing slash")
	})

	t.Run("named entry state not inside zip", func(t *testing.T) {
		// Zip contains 1_START.md but no MISSING.* — expect error.
		zipPath := mkZip(t, "mywf.zip", map[string]string{"1_START.md": "start"})
		spec := filepath.Join(zipPath, "MISSING")

		_, err := specifier.Resolve(spec, "")

		require.Error(t, err)
	})

	t.Run("ambiguity - both .md and platform script inside zip", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix-only ambiguity test (.sh is not a platform script on Windows)")
		}
		// Zip contains both OTHER_ENTRY.md and OTHER_ENTRY.sh — ambiguity error.
		zipPath := mkZip(t, "mywf.zip", map[string]string{
			"OTHER_ENTRY.md": "# Entry",
			"OTHER_ENTRY.sh": "#!/bin/sh",
		})
		spec := filepath.Join(zipPath, "OTHER_ENTRY")

		_, err := specifier.Resolve(spec, "")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "Ambiguous")
	})

	t.Run("zip hash verification fails", func(t *testing.T) {
		// Embed a wrong 64-char hex string in the filename so VerifyZipHash detects mismatch.
		base := t.TempDir()
		realZipPath := filepath.Join(base, "mywf.zip")
		writeZip(t, realZipPath, map[string]string{
			"1_START.md":     "start",
			"OTHER_ENTRY.md": "other",
		})
		// Rename to embed a wrong hash (all zeros ≠ actual SHA256).
		wrongHash := strings.Repeat("0", 64)
		wrongZipPath := filepath.Join(base, wrongHash+"_mywf.zip")
		require.NoError(t, os.Rename(realZipPath, wrongZipPath))
		spec := filepath.Join(wrongZipPath, "OTHER_ENTRY")

		_, err := specifier.Resolve(spec, "")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "hash")
	})

	t.Run("abbrev derived from zip stem", func(t *testing.T) {
		// "longworkflowname" is 16 chars → truncated to "longwo" (first 6).
		zipPath := mkZip(t, "longworkflowname.zip", map[string]string{"STEP.md": "step"})
		spec := filepath.Join(zipPath, "STEP")

		res, err := specifier.Resolve(spec, "")

		require.NoError(t, err)
		assert.Equal(t, "longwo", res.Abbrev)
	})
}
