package zipscope

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// makeZip creates a temporary zip file with the given name→content mapping.
func makeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.zip")

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
	return path
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
// ReadText
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
// FileExists
// --------------------------------------------------------------------------

func TestFileExists_ExistingFile(t *testing.T) {
	zp := makeZip(t, map[string]string{"START.md": "content"})
	if !FileExists(zp, "START.md") {
		t.Error("expected FileExists to return true for existing file")
	}
}

func TestFileExists_MissingFile(t *testing.T) {
	zp := makeZip(t, map[string]string{"START.md": "content"})
	if FileExists(zp, "OTHER.md") {
		t.Error("expected FileExists to return false for missing file")
	}
}

// --------------------------------------------------------------------------
// ListFiles
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

func TestListFiles_EmptyZip(t *testing.T) {
	zp := makeZip(t, map[string]string{})
	files, err := ListFiles(zp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty list, got %v", files)
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
// ZipFileNotFoundError
// --------------------------------------------------------------------------

func TestZipFileNotFoundError_Message(t *testing.T) {
	err := &ZipFileNotFoundError{ZipPath: "/a/b.zip", Filename: "START.md"}
	msg := err.Error()
	if msg == "" {
		t.Error("expected non-empty error message")
	}
	// Should mention both the zip path and the filename.
	for _, substr := range []string{"START.md", "/a/b.zip"} {
		if !contains(msg, substr) {
			t.Errorf("error message %q does not contain %q", msg, substr)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
