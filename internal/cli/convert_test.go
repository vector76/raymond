package cli_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// convert subcommand tests
// --------------------------------------------------------------------------

func TestConvertFolderToStdout(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("# Start\n<!-- goto: REVIEW -->"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "REVIEW.md"), []byte("# Review"), 0o644))

	stdout, stderr, err := run(t, "convert", dir)
	require.NoError(t, err)
	assert.Contains(t, stdout, "states:")
	assert.Contains(t, stdout, "1_START")
	assert.Contains(t, stdout, "REVIEW")
	// stderr may be empty or contain only warnings
	_ = stderr
}

func TestConvertFolderWithOutput(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("# Start\n<!-- goto: REVIEW -->"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "REVIEW.md"), []byte("# Review"), 0o644))

	outPath := filepath.Join(t.TempDir(), "output.yaml")
	stdout, _, err := run(t, "convert", dir, "--output", outPath)
	require.NoError(t, err)
	assert.Empty(t, stdout, "stdout should be empty when --output is used")

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "states:")
	assert.Contains(t, string(data), "1_START")
}

func TestConvertZipToStdout(t *testing.T) {
	dir := t.TempDir()
	// Create zip without hash first, then rename with correct hash.
	tmpZip := filepath.Join(dir, "workflow.zip")
	writeTestZip(t, tmpZip, map[string]string{
		"1_START.md": "# Start\n<!-- goto: REVIEW -->",
		"REVIEW.md":  "# Review",
	})

	// Compute hash and rename to include it.
	data, err := os.ReadFile(tmpZip)
	require.NoError(t, err)
	h := sha256.Sum256(data)
	hashStr := hex.EncodeToString(h[:])
	hashedZip := filepath.Join(dir, "workflow-"+hashStr+".zip")
	require.NoError(t, os.Rename(tmpZip, hashedZip))

	stdout, _, err := run(t, "convert", hashedZip)
	require.NoError(t, err)
	assert.Contains(t, stdout, "states:")
	assert.Contains(t, stdout, "1_START")
}

func TestConvertYamlRejected(t *testing.T) {
	yamlPath := filepath.Join(t.TempDir(), "workflow.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte("entry_point: START\nstates: {}"), 0o644))

	_, _, err := run(t, "convert", yamlPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already a YAML workflow")
}

func TestConvertNoEntryPoint(t *testing.T) {
	dir := t.TempDir()
	// Only REVIEW.md, no START or 1_START.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "REVIEW.md"), []byte("# Review"), 0o644))

	_, _, err := run(t, "convert", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no entry point")
}

func TestConvertNonStateFileWarnings(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1_START.md"), []byte("# Start\n<!-- goto: REVIEW -->"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "REVIEW.md"), []byte("# Review"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("some notes"), 0o644))

	stdout, stderr, err := run(t, "convert", dir)
	require.NoError(t, err)
	assert.Contains(t, stderr, "warning:")
	assert.Contains(t, stderr, "notes.txt")
	// YAML on stdout should not contain warning text.
	assert.NotContains(t, stdout, "warning:")
	assert.Contains(t, stdout, "states:")
}

func TestConvertZipHashMismatch(t *testing.T) {
	dir := t.TempDir()
	badHash := "0000000000000000000000000000000000000000000000000000000000000000"
	zipFile := filepath.Join(dir, "workflow-"+badHash+".zip")
	writeTestZip(t, zipFile, map[string]string{"1_START.md": "# Start"})

	_, _, err := run(t, "convert", zipFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hash")
}
