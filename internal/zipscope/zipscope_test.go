package zipscope

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeZip creates a temporary zip file with the given name→content mapping.
// File names containing "/" are stored with that path (for single-folder layout tests).
func makeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.zip")
	makeZipAt(t, path, files)
	return path
}

// makeZipAt creates a zip file at a specific path (for hash-in-filename tests).
func makeZipAt(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// sha256OfFile computes the SHA256 hex digest of the file at path.
func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// --------------------------------------------------------------------------
// IsZipScope
// --------------------------------------------------------------------------

func TestIsZipScope_ZipExtension(t *testing.T) {
	if !IsZipScope("/path/to/workflows.zip") {
		t.Error("expected true for .zip extension")
	}
}

func TestIsZipScope_ZipExtensionUppercase(t *testing.T) {
	if !IsZipScope("/path/to/WORKFLOWS.ZIP") {
		t.Error("expected true for .ZIP extension")
	}
}

func TestIsZipScope_Directory(t *testing.T) {
	if IsZipScope("/path/to/workflows/") {
		t.Error("expected false for directory path")
	}
}

func TestIsZipScope_NoExtension(t *testing.T) {
	if IsZipScope("/path/to/workflows") {
		t.Error("expected false for path with no extension")
	}
}

func TestIsZipScope_OtherExtension(t *testing.T) {
	if IsZipScope("/path/to/workflows.tar.gz") {
		t.Error("expected false for non-zip extension")
	}
}

// --------------------------------------------------------------------------
// DetectLayout
// --------------------------------------------------------------------------

func TestDetectLayout_FlatLayout(t *testing.T) {
	zp := makeZip(t, map[string]string{
		"START.md": "start",
		"NEXT.md":  "next",
	})
	prefix, err := DetectLayout(zp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prefix != "" {
		t.Errorf("flat layout: expected empty prefix, got %q", prefix)
	}
}

func TestDetectLayout_SingleFolderLayout(t *testing.T) {
	zp := makeZip(t, map[string]string{
		"workflows/START.md": "start",
		"workflows/NEXT.md":  "next",
	})
	prefix, err := DetectLayout(zp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prefix != "workflows/" {
		t.Errorf("single-folder layout: expected %q, got %q", "workflows/", prefix)
	}
}

func TestDetectLayout_EmptyZip(t *testing.T) {
	zp := makeZip(t, map[string]string{})
	_, err := DetectLayout(zp)
	if err == nil {
		t.Fatal("expected error for empty zip")
	}
	var layoutErr *ZipLayoutError
	if !errors.As(err, &layoutErr) {
		t.Errorf("expected *ZipLayoutError, got %T: %v", err, err)
	}
}

func TestDetectLayout_MultipleFolders(t *testing.T) {
	zp := makeZip(t, map[string]string{
		"alpha/START.md": "start",
		"beta/NEXT.md":   "next",
	})
	_, err := DetectLayout(zp)
	if err == nil {
		t.Fatal("expected error for multiple top-level folders")
	}
	var layoutErr *ZipLayoutError
	if !errors.As(err, &layoutErr) {
		t.Errorf("expected *ZipLayoutError, got %T: %v", err, err)
	}
}

func TestDetectLayout_MixedRootFilesAndFolder(t *testing.T) {
	zp := makeZip(t, map[string]string{
		"ROOT.md":     "root file",
		"sub/NEXT.md": "nested",
	})
	_, err := DetectLayout(zp)
	if err == nil {
		t.Fatal("expected error for mixed root files and subdirectory")
	}
	var layoutErr *ZipLayoutError
	if !errors.As(err, &layoutErr) {
		t.Errorf("expected *ZipLayoutError, got %T: %v", err, err)
	}
}

func TestDetectLayout_DeeplyNested(t *testing.T) {
	zp := makeZip(t, map[string]string{
		"outer/inner/START.md": "deeply nested",
	})
	_, err := DetectLayout(zp)
	if err == nil {
		t.Fatal("expected error for deeply nested files")
	}
	var layoutErr *ZipLayoutError
	if !errors.As(err, &layoutErr) {
		t.Errorf("expected *ZipLayoutError, got %T: %v", err, err)
	}
}

func TestDetectLayout_FileNotFound(t *testing.T) {
	_, err := DetectLayout("/nonexistent/path/archive.zip")
	if err == nil {
		t.Fatal("expected error for nonexistent zip")
	}
	// FileExists relies on errors.Is(err, os.ErrNotExist) to silently suppress
	// missing-zip errors. Verify the contract is met.
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected error wrapping os.ErrNotExist, got %T: %v", err, err)
	}
}

func TestDetectLayout_CorruptZip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.zip")
	if err := os.WriteFile(path, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := DetectLayout(path)
	if err == nil {
		t.Fatal("expected error for corrupt zip")
	}
	var layoutErr *ZipLayoutError
	if !errors.As(err, &layoutErr) {
		t.Errorf("expected *ZipLayoutError, got %T: %v", err, err)
	}
}

// --------------------------------------------------------------------------
// ExtractHashFromFilename
// --------------------------------------------------------------------------

func TestExtractHashFromFilename_NoHash(t *testing.T) {
	hash, err := ExtractHashFromFilename("workflow.zip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash != "" {
		t.Errorf("expected empty hash, got %q", hash)
	}
}

func TestExtractHashFromFilename_ValidHash(t *testing.T) {
	validHash := "a" + strings.Repeat("b", 63)
	basename := "workflow-" + validHash + ".zip"
	hash, err := ExtractHashFromFilename(basename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash != validHash {
		t.Errorf("expected %q, got %q", validHash, hash)
	}
}

func TestExtractHashFromFilename_UppercaseNormalized(t *testing.T) {
	upperHash := "A" + strings.Repeat("B", 63)
	lowerHash := strings.ToLower(upperHash)
	basename := "workflow-" + upperHash + ".zip"
	hash, err := ExtractHashFromFilename(basename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash != lowerHash {
		t.Errorf("expected lowercase %q, got %q", lowerHash, hash)
	}
}

func TestExtractHashFromFilename_RunTooLong(t *testing.T) {
	longRun := strings.Repeat("a", 65)
	basename := "workflow-" + longRun + ".zip"
	_, err := ExtractHashFromFilename(basename)
	if err == nil {
		t.Fatal("expected error for hex run longer than 64 chars")
	}
	var ambigErr *ZipFilenameAmbiguousError
	if !errors.As(err, &ambigErr) {
		t.Errorf("expected *ZipFilenameAmbiguousError, got %T: %v", err, err)
	}
}

func TestExtractHashFromFilename_MultipleHashRuns(t *testing.T) {
	hash1 := strings.Repeat("a", 64)
	hash2 := strings.Repeat("b", 64)
	basename := "workflow-" + hash1 + "-" + hash2 + ".zip"
	_, err := ExtractHashFromFilename(basename)
	if err == nil {
		t.Fatal("expected error for multiple 64-char hex runs")
	}
	var ambigErr *ZipFilenameAmbiguousError
	if !errors.As(err, &ambigErr) {
		t.Errorf("expected *ZipFilenameAmbiguousError, got %T: %v", err, err)
	}
}

func TestExtractHashFromFilename_ShortHexRunsIgnored(t *testing.T) {
	// Short hex runs (< 64 chars) are not treated as hashes.
	basename := "workflow-abc123-def456.zip"
	hash, err := ExtractHashFromFilename(basename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash != "" {
		t.Errorf("expected empty hash for short hex runs, got %q", hash)
	}
}

// --------------------------------------------------------------------------
// VerifyZipHash
// --------------------------------------------------------------------------

func TestVerifyZipHash_NoHashInFilename(t *testing.T) {
	// The zip created by makeZip has a plain name (no hash) → no-op.
	zp := makeZip(t, map[string]string{"START.md": "content"})
	if err := VerifyZipHash(zp); err != nil {
		t.Errorf("expected no error when no hash in filename, got: %v", err)
	}
}

func TestVerifyZipHash_ValidHash(t *testing.T) {
	dir := t.TempDir()
	// First create the zip, compute its hash, then write a copy with hash in name.
	tmpZip := makeZip(t, map[string]string{"START.md": "content"})
	actualHash := sha256OfFile(t, tmpZip)

	hashedPath := filepath.Join(dir, "workflow-"+actualHash+".zip")
	data, err := os.ReadFile(tmpZip)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hashedPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := VerifyZipHash(hashedPath); err != nil {
		t.Errorf("expected no error for valid hash, got: %v", err)
	}
}

func TestVerifyZipHash_InvalidHash(t *testing.T) {
	dir := t.TempDir()
	badHash := strings.Repeat("0", 64)
	hashedPath := filepath.Join(dir, "workflow-"+badHash+".zip")
	// The zip content will produce a non-zero hash — mismatch expected.
	makeZipAt(t, hashedPath, map[string]string{"START.md": "content"})

	err := VerifyZipHash(hashedPath)
	if err == nil {
		t.Fatal("expected error for hash mismatch")
	}
	var mismatchErr *ZipHashMismatchError
	if !errors.As(err, &mismatchErr) {
		t.Errorf("expected *ZipHashMismatchError, got %T: %v", err, err)
	}
	if mismatchErr.Expected != badHash {
		t.Errorf("expected Expected=%q, got %q", badHash, mismatchErr.Expected)
	}
}

func TestVerifyZipHash_AmbiguousFilename(t *testing.T) {
	dir := t.TempDir()
	hash1 := strings.Repeat("a", 64)
	hash2 := strings.Repeat("b", 64)
	ambiguousPath := filepath.Join(dir, "workflow-"+hash1+"-"+hash2+".zip")
	makeZipAt(t, ambiguousPath, map[string]string{"START.md": "content"})

	err := VerifyZipHash(ambiguousPath)
	if err == nil {
		t.Fatal("expected error for ambiguous filename")
	}
	var ambigErr *ZipFilenameAmbiguousError
	if !errors.As(err, &ambigErr) {
		t.Errorf("expected *ZipFilenameAmbiguousError, got %T: %v", err, err)
	}
}

// --------------------------------------------------------------------------
// ReadText — flat and single-folder layouts
// --------------------------------------------------------------------------

func TestReadText_HappyPath(t *testing.T) {
	zp := makeZip(t, map[string]string{
		"START.md": "# Start\n\nHello.",
		"OTHER.md": "other",
	})
	got, err := ReadText(zp, "START.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "# Start\n\nHello." {
		t.Errorf("got %q, want %q", got, "# Start\n\nHello.")
	}
}

func TestReadText_SingleFolderLayout(t *testing.T) {
	zp := makeZip(t, map[string]string{
		"myworkflow/START.md": "# Start\n\nHello.",
		"myworkflow/OTHER.md": "other",
	})
	// Caller passes bare filename; prefix resolved transparently.
	got, err := ReadText(zp, "START.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "# Start\n\nHello." {
		t.Errorf("got %q, want %q", got, "# Start\n\nHello.")
	}
}

func TestReadText_MissingFile(t *testing.T) {
	zp := makeZip(t, map[string]string{"START.md": "content"})
	_, err := ReadText(zp, "NONEXISTENT.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	var znf *ZipFileNotFoundError
	if !errors.As(err, &znf) {
		t.Errorf("expected *ZipFileNotFoundError, got %T: %v", err, err)
	}
}

func TestReadText_InvalidZip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notazip.zip")
	if err := os.WriteFile(path, []byte("not a zip file"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadText(path, "file.md")
	if err == nil {
		t.Fatal("expected error for invalid zip")
	}
}

func TestReadText_EmptyContent(t *testing.T) {
	zp := makeZip(t, map[string]string{"empty.md": ""})
	got, err := ReadText(zp, "empty.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// --------------------------------------------------------------------------
// FileExists — flat and single-folder layouts
// --------------------------------------------------------------------------

func TestFileExists_ExistingFile(t *testing.T) {
	zp := makeZip(t, map[string]string{"START.md": "content"})
	found, err := FileExists(zp, "START.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("expected FileExists to return true for existing file")
	}
}

func TestFileExists_SingleFolderLayout(t *testing.T) {
	zp := makeZip(t, map[string]string{"myworkflow/START.md": "content"})
	found, err := FileExists(zp, "START.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("expected FileExists to return true for file in single-folder layout")
	}
}

func TestFileExists_MissingFile(t *testing.T) {
	zp := makeZip(t, map[string]string{"START.md": "content"})
	found, err := FileExists(zp, "OTHER.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected FileExists to return false for missing file")
	}
}

func TestFileExists_InvalidLayout_ReturnsFalse(t *testing.T) {
	// Empty zip → ZipLayoutError → FileExists returns (false, nil) per Python semantics.
	zp := makeZip(t, map[string]string{})
	found, err := FileExists(zp, "START.md")
	if err != nil {
		t.Errorf("expected nil error for layout error, got: %v", err)
	}
	if found {
		t.Error("expected false for invalid layout zip")
	}
}

func TestFileExists_InvalidZip(t *testing.T) {
	// A corrupt zip raises ZipLayoutError in DetectLayout, which FileExists
	// converts to (false, nil) — matching Python's file_exists() behaviour.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.zip")
	if err := os.WriteFile(bad, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := FileExists(bad, "file.md")
	if err != nil {
		t.Errorf("expected nil error for corrupt zip (layout error is suppressed), got: %v", err)
	}
	if found {
		t.Error("expected false for corrupt zip archive")
	}
}

func TestFileExists_NonExistentZip(t *testing.T) {
	// A non-existent zip wraps os.ErrNotExist, which FileExists converts to
	// (false, nil) — matching Python's file_exists() which catches FileNotFoundError.
	found, err := FileExists("/nonexistent/path/nope.zip", "file.md")
	if err != nil {
		t.Errorf("expected nil error for non-existent zip, got: %v", err)
	}
	if found {
		t.Error("expected false for non-existent zip archive")
	}
}

func TestFileExists_PathTraversal(t *testing.T) {
	zp := makeZip(t, map[string]string{"START.md": "content"})
	_, err := FileExists(zp, "../evil.md")
	if err == nil {
		t.Error("expected error for path traversal filename")
	}
}

// --------------------------------------------------------------------------
// ListFiles — flat and single-folder layouts
// --------------------------------------------------------------------------

func TestListFiles_ReturnsAllFiles(t *testing.T) {
	zp := makeZip(t, map[string]string{
		"START.md": "start",
		"NEXT.md":  "next",
		"POLL.sh":  "poll",
	})
	files, err := ListFiles(zp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("expected 3 files, got %d: %v", len(files), files)
	}
	names := make(map[string]bool)
	for _, f := range files {
		names[f] = true
	}
	for _, want := range []string{"START.md", "NEXT.md", "POLL.sh"} {
		if !names[want] {
			t.Errorf("missing %q in listed files: %v", want, files)
		}
	}
}

func TestListFiles_SingleFolderLayout(t *testing.T) {
	zp := makeZip(t, map[string]string{
		"myworkflow/START.md": "start",
		"myworkflow/NEXT.md":  "next",
	})
	files, err := ListFiles(zp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(files), files)
	}
	names := make(map[string]bool)
	for _, f := range files {
		names[f] = true
	}
	// Files must be returned as bare names, without the folder prefix.
	for _, want := range []string{"START.md", "NEXT.md"} {
		if !names[want] {
			t.Errorf("missing %q in listed files (got %v)", want, files)
		}
	}
	if names["myworkflow/START.md"] {
		t.Error("ListFiles should strip prefix; got full path instead of bare name")
	}
}

func TestListFiles_EmptyZip(t *testing.T) {
	zp := makeZip(t, map[string]string{})
	_, err := ListFiles(zp)
	if err == nil {
		t.Fatal("expected error for empty zip (layout error)")
	}
	var layoutErr *ZipLayoutError
	if !errors.As(err, &layoutErr) {
		t.Errorf("expected *ZipLayoutError, got %T: %v", err, err)
	}
}

func TestListFiles_InvalidZip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.zip")
	if err := os.WriteFile(path, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ListFiles(path)
	if err == nil {
		t.Fatal("expected error for invalid zip")
	}
}

// --------------------------------------------------------------------------
// ExtractScript
// --------------------------------------------------------------------------

func TestExtractScript_HappyPath(t *testing.T) {
	zp := makeZip(t, map[string]string{"run.sh": "#!/bin/sh\necho hello"})
	path, err := ExtractScript(zp, "run.sh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read extracted file: %v", err)
	}
	if string(data) != "#!/bin/sh\necho hello" {
		t.Errorf("content mismatch: got %q", string(data))
	}
}

func TestExtractScript_ExtensionPreserved(t *testing.T) {
	zp := makeZip(t, map[string]string{"run.sh": "content"})
	path, err := ExtractScript(zp, "run.sh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(path)

	if filepath.Ext(path) != ".sh" {
		t.Errorf("expected .sh extension on temp file, got %q", filepath.Ext(path))
	}
}

func TestExtractScript_MissingFile(t *testing.T) {
	zp := makeZip(t, map[string]string{"START.md": "content"})
	_, err := ExtractScript(zp, "nonexistent.sh")
	if err == nil {
		t.Fatal("expected error for missing script")
	}
	var znf *ZipFileNotFoundError
	if !errors.As(err, &znf) {
		t.Errorf("expected *ZipFileNotFoundError, got %T: %v", err, err)
	}
}

// --------------------------------------------------------------------------
// ZipFileNotFoundError
// --------------------------------------------------------------------------

func TestZipFileNotFoundError_Message(t *testing.T) {
	err := &ZipFileNotFoundError{ZipPath: "/a/b.zip", Filename: "START.md"}
	msg := err.Error()
	if msg == "" {
		t.Error("expected non-empty error message")
	}
	for _, substr := range []string{"START.md", "/a/b.zip"} {
		if !strings.Contains(msg, substr) {
			t.Errorf("error message %q does not contain %q", msg, substr)
		}
	}
}

// --------------------------------------------------------------------------
// ZipHashMismatchError
// --------------------------------------------------------------------------

func TestZipHashMismatchError_Message(t *testing.T) {
	expected := strings.Repeat("a", 64)
	actual := strings.Repeat("b", 64)
	err := &ZipHashMismatchError{Expected: expected, Actual: actual}
	msg := err.Error()
	if !strings.Contains(msg, expected) {
		t.Errorf("error message %q does not contain expected hash %q", msg, expected)
	}
	if !strings.Contains(msg, actual) {
		t.Errorf("error message %q does not contain actual hash %q", msg, actual)
	}
}
