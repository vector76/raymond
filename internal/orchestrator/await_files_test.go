package orchestrator_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/orchestrator"
	"github.com/vector76/raymond/internal/parsing"
)

func TestStageInputFiles_HappyPath(t *testing.T) {
	task := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(task, "out"), 0o755))
	pdfBody := []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<< >>\nendobj\n")
	require.NoError(t, os.WriteFile(filepath.Join(task, "out", "report.pdf"), pdfBody, 0o644))
	pngBody := minimalPNG()
	require.NoError(t, os.WriteFile(filepath.Join(task, "chart.png"), pngBody, 0o644))

	fa := parsing.FileAffordance{
		Mode: parsing.ModeDisplayOnly,
		DisplayFiles: []parsing.DisplaySpec{
			{SourcePath: "out/report.pdf", DisplayName: "Final Report.pdf"},
			{SourcePath: "chart.png"},
		},
	}

	records, err := orchestrator.StageInputFiles(task, "inp_main_42", fa)
	require.NoError(t, err)
	require.Len(t, records, 2)

	assert.Equal(t, "Final Report.pdf", records[0].Name)
	assert.Equal(t, int64(len(pdfBody)), records[0].Size)
	assert.Equal(t, "application/pdf", records[0].ContentType)
	assert.Equal(t, "display", records[0].Source)

	assert.Equal(t, "chart.png", records[1].Name)
	assert.Equal(t, int64(len(pngBody)), records[1].Size)
	assert.Equal(t, "image/png", records[1].ContentType)
	assert.Equal(t, "display", records[1].Source)

	inputDir := filepath.Join(task, "inputs", "inp_main_42")
	for _, name := range []string{"Final Report.pdf", "chart.png"} {
		_, err := os.Stat(filepath.Join(inputDir, name))
		require.NoError(t, err, "%s should be staged", name)
	}
}

func TestStageInputFiles_CreatesDirectoryWhenNoDisplayFiles(t *testing.T) {
	task := t.TempDir()
	records, err := orchestrator.StageInputFiles(task, "inp_solo", parsing.FileAffordance{})
	require.NoError(t, err)
	assert.Nil(t, records)

	info, err := os.Stat(filepath.Join(task, "inputs", "inp_solo"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestStageInputFiles_MissingSource(t *testing.T) {
	task := t.TempDir()
	fa := parsing.FileAffordance{
		DisplayFiles: []parsing.DisplaySpec{{SourcePath: "missing.pdf"}},
	}

	_, err := orchestrator.StageInputFiles(task, "inp_x", fa)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.pdf")
}

func TestStageInputFiles_RejectsTraversal(t *testing.T) {
	task := t.TempDir()
	parent := filepath.Dir(task)
	require.NoError(t, os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("nope"), 0o644))

	cases := []struct {
		name string
		src  string
	}{
		{"dotdot", "../secret.txt"},
		{"absolute", filepath.Join(parent, "secret.txt")},
		{"unix-rooted", "/etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fa := parsing.FileAffordance{
				DisplayFiles: []parsing.DisplaySpec{{SourcePath: tc.src}},
			}
			_, err := orchestrator.StageInputFiles(task, "inp_t", fa)
			require.Error(t, err)
		})
	}
}

func TestStageInputFiles_RejectsSymlinkEscape(t *testing.T) {
	task := t.TempDir()
	parent := filepath.Dir(task)
	target := filepath.Join(parent, "outside.txt")
	require.NoError(t, os.WriteFile(target, []byte("escape"), 0o644))

	link := filepath.Join(task, "linked.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	fa := parsing.FileAffordance{
		DisplayFiles: []parsing.DisplaySpec{{SourcePath: "linked.txt"}},
	}
	_, err := orchestrator.StageInputFiles(task, "inp_sym", fa)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes task folder")
}

func TestStageInputFiles_IdempotentReentry(t *testing.T) {
	task := t.TempDir()
	body := minimalPNG()
	require.NoError(t, os.WriteFile(filepath.Join(task, "art.png"), body, 0o644))

	fa := parsing.FileAffordance{
		DisplayFiles: []parsing.DisplaySpec{{SourcePath: "art.png", DisplayName: "art.png"}},
	}

	first, err := orchestrator.StageInputFiles(task, "inp_y", fa)
	require.NoError(t, err)
	require.Len(t, first, 1)

	stagedPath := filepath.Join(task, "inputs", "inp_y", "art.png")
	stagedInfoBefore, err := os.Stat(stagedPath)
	require.NoError(t, err)
	mtimeBefore := stagedInfoBefore.ModTime()

	second, err := orchestrator.StageInputFiles(task, "inp_y", fa)
	require.NoError(t, err, "re-entry on a same-sized staged file should not error")
	require.Len(t, second, 1)
	assert.Equal(t, first[0].Name, second[0].Name)
	assert.Equal(t, first[0].Size, second[0].Size)
	assert.Equal(t, first[0].ContentType, second[0].ContentType)

	entries, err := os.ReadDir(filepath.Join(task, "inputs", "inp_y"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no duplicate file should be created")

	stagedInfoAfter, err := os.Stat(stagedPath)
	require.NoError(t, err)
	assert.True(t, stagedInfoAfter.ModTime().Equal(mtimeBefore),
		"staged file should not be re-written on idempotent re-entry")
}

// minimalPNG returns a small byte sequence whose 8-byte header marks it as a
// PNG for http.DetectContentType.
func minimalPNG() []byte {
	header := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	return append(header, []byte("body bytes for sniffing")...)
}
