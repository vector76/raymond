package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeUploadFilename_Accepts(t *testing.T) {
	cases := []string{
		"foo.txt",
		"image.png",
		"data.csv",
		"report-2025.pdf",
		"My File (1).docx",
		"a",
		"résumé.pdf",
		"COM0",
		"COM10",
		"LPT0",
		"LPT10",
		"console.log",
		"communication.txt",
		"lpt.txt",
		"foo.CON",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := NormalizeUploadFilename(name)
			require.NoError(t, err, "want %q to be accepted", name)
			assert.Equal(t, name, got, "must return name unchanged")
		})
	}
}

func TestNormalizeUploadFilename_RejectsEmpty(t *testing.T) {
	_, err := NormalizeUploadFilename("")
	assert.ErrorIs(t, err, ErrFilenameEmpty)
}

func TestNormalizeUploadFilename_RejectsPathSeparator(t *testing.T) {
	cases := []string{
		"foo/bar",
		"foo\\bar",
		"/etc/passwd",
		"..\\foo",
		"sub/file.txt",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeUploadFilename(name)
			assert.ErrorIs(t, err, ErrFilenamePathSeparator)
		})
	}
}

func TestNormalizeUploadFilename_RejectsNullByte(t *testing.T) {
	cases := []string{
		"\x00foo",
		"foo\x00bar",
		"foo\x00",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeUploadFilename(name)
			assert.ErrorIs(t, err, ErrFilenameNullByte)
		})
	}
}

func TestNormalizeUploadFilename_RejectsControlChars(t *testing.T) {
	cases := []string{
		"\nfoo",
		"foo\tbar",
		"foo\rbar",
		"\x01file",
		"file\x1f",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeUploadFilename(name)
			assert.ErrorIs(t, err, ErrFilenameControlChar)
		})
	}
}

func TestNormalizeUploadFilename_RejectsLeadingDot(t *testing.T) {
	cases := []string{
		".hidden",
		".",
		"..",
		".gitignore",
		"...weird",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeUploadFilename(name)
			assert.ErrorIs(t, err, ErrFilenameLeadingDot)
		})
	}
}

func TestNormalizeUploadFilename_RejectsReservedNames(t *testing.T) {
	cases := []string{
		"CON",
		"con",
		"Con",
		"PRN",
		"AUX",
		"NUL",
		"COM1",
		"com9",
		"LPT1",
		"LPT9",
		"CON.txt",
		"con.txt",
		"NUL.log",
		"COM3.csv",
		"lpt5.dat",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeUploadFilename(name)
			assert.ErrorIs(t, err, ErrFilenameReserved)
		})
	}
}

func TestSafeJoinUnderDir_AcceptsSimpleNames(t *testing.T) {
	base := t.TempDir()
	got, err := SafeJoinUnderDir(base, "foo.txt")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "foo.txt"), got)
}

func TestSafeJoinUnderDir_AcceptsSubdirectoryPath(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "sub"), 0o755))
	got, err := SafeJoinUnderDir(base, "sub/foo.txt")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "sub", "foo.txt"), got)
}

func TestSafeJoinUnderDir_AcceptsCleanableButContained(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "sub"), 0o755))
	// "sub/../sub/foo.txt" cleans to "sub/foo.txt" — entirely under base.
	got, err := SafeJoinUnderDir(base, "sub/../sub/foo.txt")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "sub", "foo.txt"), got)
}

func TestSafeJoinUnderDir_RejectsAbsolutePath(t *testing.T) {
	base := t.TempDir()
	_, err := SafeJoinUnderDir(base, "/etc/passwd")
	assert.ErrorIs(t, err, ErrPathAbsolute)
}

func TestSafeJoinUnderDir_RejectsLeadingTraversal(t *testing.T) {
	base := t.TempDir()
	cases := []string{
		"../foo",
		"../../etc/passwd",
		"..",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			_, err := SafeJoinUnderDir(base, p)
			require.Error(t, err)
			assert.True(t,
				errors.Is(err, ErrPathTraversal) || errors.Is(err, ErrPathEscapesBase),
				"want traversal/escape error, got %v", err)
		})
	}
}

func TestSafeJoinUnderDir_RejectsTraversalThatEscapes(t *testing.T) {
	base := t.TempDir()
	// "a/../../b" cleans to "../b" — escapes base.
	_, err := SafeJoinUnderDir(base, "a/../../b")
	require.Error(t, err)
	assert.True(t,
		errors.Is(err, ErrPathTraversal) || errors.Is(err, ErrPathEscapesBase),
		"want traversal/escape error, got %v", err)
}

func TestSafeJoinUnderDir_RejectsEmpty(t *testing.T) {
	base := t.TempDir()
	_, err := SafeJoinUnderDir(base, "")
	require.Error(t, err)
}

func TestSafeJoinUnderDir_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.MkdirAll(base, 0o755))
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644))

	// Create a symlink inside base that points to outside.
	linkPath := filepath.Join(base, "escape")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}

	_, err := SafeJoinUnderDir(base, "escape/secret.txt")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPathEscapesBase)
}

func TestSafeJoinUnderDir_AllowsSymlinkInsideBase(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "data.txt"), []byte("x"), 0o644))

	linkPath := filepath.Join(base, "alias")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}

	got, err := SafeJoinUnderDir(base, "alias/data.txt")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "alias", "data.txt"), got)
}

func TestSafeJoinUnderDir_RejectsSymlinkEscapeWhenTargetMissing(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.MkdirAll(base, 0o755))
	require.NoError(t, os.MkdirAll(outside, 0o755))

	// Symlink inside base points to a sibling directory outside base.
	linkPath := filepath.Join(base, "escape")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}

	// Target file does not exist; the escape via the intermediate symlink
	// must still be detected so a subsequent write does not land outside base.
	_, err := SafeJoinUnderDir(base, "escape/willbe.txt")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPathEscapesBase)
}

func TestSafeJoinUnderDir_AllowsNonexistentTarget(t *testing.T) {
	base := t.TempDir()
	got, err := SafeJoinUnderDir(base, "not/yet/created.txt")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "not", "yet", "created.txt"), got)
}
