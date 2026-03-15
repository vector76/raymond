package specifier_test

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/specifier"
)

// makeHashedZip creates a zip with the given files, then renames it to
// "<sha256>.zip" so that zipscope.VerifyZipHash passes. Returns the final path.
func makeHashedZip(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	tmp := filepath.Join(dir, "tmp.zip")

	f, err := os.Create(tmp)
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

	data, err := os.ReadFile(tmp)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])

	finalPath := filepath.Join(dir, h+".zip")
	require.NoError(t, os.Rename(tmp, finalPath))
	return finalPath
}

// mockFetcher returns a Fetcher that maps URL→localPath using the provided map.
func mockFetcher(mapping map[string]string) specifier.Fetcher {
	return func(rawURL, hash string) (string, error) {
		p, ok := mapping[rawURL]
		if !ok {
			return "", fmt.Errorf("mock fetcher: no entry for URL %q", rawURL)
		}
		return p, nil
	}
}

// errFetcher always returns an error.
func errFetcher(msg string) specifier.Fetcher {
	return func(rawURL, hash string) (string, error) {
		return "", errors.New(msg)
	}
}

// --------------------------------------------------------------------------
// Sibling zip resolution
// --------------------------------------------------------------------------

func TestResolveFromURL_SiblingZip(t *testing.T) {
	// Create a valid zip with 1_START.md.
	zipPath := makeHashedZip(t, map[string]string{"1_START.md": "# start"})
	hash := strings.TrimSuffix(filepath.Base(zipPath), ".zip")

	scopeURL := fmt.Sprintf("https://host/wfs/wf1_%s.zip", hash)
	specifierStr := fmt.Sprintf("../sibling_%s.zip", hash)
	resolvedURL := fmt.Sprintf("https://host/wfs/sibling_%s.zip", hash)

	fetch := mockFetcher(map[string]string{resolvedURL: zipPath})

	res, err := specifier.ResolveFromURL(specifierStr, scopeURL, fetch)
	require.NoError(t, err)
	assert.Equal(t, zipPath, res.ScopeDir)
	assert.Equal(t, "1_START.md", res.EntryPoint)
	assert.Equal(t, resolvedURL, res.ScopeURL)
	// Abbrev from URL filename stem "sibling_<hash>" → first 6 chars lowercased.
	assert.Equal(t, "siblin", res.Abbrev)
}

// --------------------------------------------------------------------------
// Two-level navigation
// --------------------------------------------------------------------------

func TestResolveFromURL_TwoLevel(t *testing.T) {
	zipPath := makeHashedZip(t, map[string]string{"1_START.md": "# start"})
	hash := strings.TrimSuffix(filepath.Base(zipPath), ".zip")

	scopeURL := fmt.Sprintf("https://host/wfs/wf1_%s.zip", hash)
	specifierStr := fmt.Sprintf("../../other/wf_%s.zip", hash)
	resolvedURL := fmt.Sprintf("https://host/other/wf_%s.zip", hash)

	fetch := mockFetcher(map[string]string{resolvedURL: zipPath})

	res, err := specifier.ResolveFromURL(specifierStr, scopeURL, fetch)
	require.NoError(t, err)
	assert.Equal(t, resolvedURL, res.ScopeURL)
	assert.Equal(t, "1_START.md", res.EntryPoint)
}

// --------------------------------------------------------------------------
// Inner-component resolution
// --------------------------------------------------------------------------

func TestResolveFromURL_InnerComponent(t *testing.T) {
	// Zip contains STATE.md (for inner-component) and 1_START.md.
	zipPath := makeHashedZip(t, map[string]string{
		"1_START.md": "# start",
		"STATE.md":   "# state",
	})
	hash := strings.TrimSuffix(filepath.Base(zipPath), ".zip")

	scopeURL := fmt.Sprintf("https://host/wfs/wf1_%s.zip", hash)
	specifierStr := fmt.Sprintf("../wf2_%s.zip/STATE", hash)
	resolvedURL := fmt.Sprintf("https://host/wfs/wf2_%s.zip", hash)

	fetch := mockFetcher(map[string]string{resolvedURL: zipPath})

	res, err := specifier.ResolveFromURL(specifierStr, scopeURL, fetch)
	require.NoError(t, err)
	assert.Equal(t, zipPath, res.ScopeDir)
	assert.Equal(t, "STATE.md", res.EntryPoint)
	assert.Equal(t, resolvedURL, res.ScopeURL)
}

// --------------------------------------------------------------------------
// Abbrev derived from URL filename, not local cache path
// --------------------------------------------------------------------------

func TestResolveFromURL_AbbrevFromURLNotLocalPath(t *testing.T) {
	zipPath := makeHashedZip(t, map[string]string{"1_START.md": "# start"})
	hash := strings.TrimSuffix(filepath.Base(zipPath), ".zip")

	// URL filename stem is "myworkflow_<hash>" → abbrev "mywork" (first 6 of lowercased stem)
	// But the local cache path is "<hash>.zip" → abbrev would be first 6 of hash
	// (which is definitely not "mywork").
	scopeURL := fmt.Sprintf("https://host/wfs/caller_%s.zip", hash)
	specifierStr := fmt.Sprintf("../myworkflow_%s.zip", hash)
	resolvedURL := fmt.Sprintf("https://host/wfs/myworkflow_%s.zip", hash)

	fetch := mockFetcher(map[string]string{resolvedURL: zipPath})

	res, err := specifier.ResolveFromURL(specifierStr, scopeURL, fetch)
	require.NoError(t, err)
	assert.Equal(t, "mywork", res.Abbrev)
}

// --------------------------------------------------------------------------
// Error: absolute URL specifier rejected (step 1)
// --------------------------------------------------------------------------

func TestResolveFromURL_AbsoluteURLSpecifierRejected(t *testing.T) {
	_, err := specifier.ResolveFromURL(
		"https://host/wfs/wf_abc123.zip",
		"https://host/wfs/caller_abc123.zip",
		errFetcher("should not be called"),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute URL")
}

// --------------------------------------------------------------------------
// Error: missing hash in resolved URL (step 5)
// --------------------------------------------------------------------------

func TestResolveFromURL_MissingHashInResolvedURL(t *testing.T) {
	// Resolved URL filename has no 64-char hex hash.
	scopeURL := "https://host/wfs/wf1_" + strings.Repeat("a", 64) + ".zip"
	// Specifier resolves to a URL with no hash in filename.
	specifierStr := "../no_hash_here.zip"

	_, err := specifier.ResolveFromURL(specifierStr, scopeURL, errFetcher("should not be called"))
	require.Error(t, err)
}

// --------------------------------------------------------------------------
// Error: fetcher error propagated (step 6)
// --------------------------------------------------------------------------

func TestResolveFromURL_FetcherErrorPropagated(t *testing.T) {
	hash := strings.Repeat("a", 64)
	scopeURL := fmt.Sprintf("https://host/wfs/wf1_%s.zip", hash)
	specifierStr := fmt.Sprintf("../sibling_%s.zip", hash)

	_, err := specifier.ResolveFromURL(specifierStr, scopeURL, errFetcher("network failure"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network failure")
}

// --------------------------------------------------------------------------
// Error: mixed-scope (resolved URL is not a remote URL) — step 4
// --------------------------------------------------------------------------

func TestResolveFromURL_MixedScopeError(t *testing.T) {
	// A file:// scopeURL causes the resolved URL to also be file://, which is
	// not a remote workflow URL — triggering the mixed-scope check.
	hash := strings.Repeat("b", 64)
	scopeURL := fmt.Sprintf("file:///wfs/wf1_%s.zip", hash)
	specifierStr := fmt.Sprintf("../sibling_%s.zip", hash)

	_, err := specifier.ResolveFromURL(specifierStr, scopeURL, errFetcher("should not be called"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-remote")
}
