// Package registry provides a local cache for workflow zip files downloaded from URLs.
// Downloaded files are stored in .raymond/registry/<hash>.zip and validated against
// the SHA256 hash embedded in the URL filename.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Registry manages a local cache of downloaded workflow zip files.
type Registry struct {
	Dir string // absolute path to .raymond/registry/
}

// New creates a Registry rooted at raymondDir/registry.
func New(raymondDir string) *Registry {
	return &Registry{Dir: filepath.Join(raymondDir, "registry")}
}

// Lookup returns (path, true) if .raymond/registry/<hash>.zip exists, else ("", false).
func (r *Registry) Lookup(hash string) (string, bool) {
	dest := filepath.Join(r.Dir, hash+".zip")
	if _, err := os.Stat(dest); err == nil {
		return dest, true
	}
	return "", false
}

// Fetch checks the cache first; if not found, downloads the URL to a temp file,
// computes SHA256, compares against hash, and on success moves the file atomically
// to .raymond/registry/<hash>.zip. Returns the local path on success.
func (r *Registry) Fetch(url, hash string) (string, error) {
	// Check cache first.
	if path, ok := r.Lookup(hash); ok {
		return path, nil
	}

	// Ensure registry directory exists.
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return "", fmt.Errorf("registry: failed to create directory %s: %w", r.Dir, err)
	}

	// Download to a temp file in the same directory for atomic rename.
	tmp, err := os.CreateTemp(r.Dir, "download-*.tmp")
	if err != nil {
		return "", fmt.Errorf("registry: failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Ensure temp file is cleaned up on any error.
	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	// Perform HTTP GET.
	resp, err := http.Get(url) //nolint:gosec // URL comes from user-supplied workflow spec
	if err != nil {
		tmp.Close()
		return "", fmt.Errorf("registry: download failed for %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		return "", fmt.Errorf("registry: HTTP %d for %s", resp.StatusCode, url)
	}

	// Write response body to temp file while computing SHA256.
	h := sha256.New()
	w := io.MultiWriter(tmp, h)
	if _, err := io.Copy(w, resp.Body); err != nil {
		tmp.Close()
		return "", fmt.Errorf("registry: failed to write downloaded content from %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("registry: failed to close temp file: %w", err)
	}

	// Verify hash.
	actualHash := hex.EncodeToString(h.Sum(nil))
	if actualHash != hash {
		return "", fmt.Errorf("registry: hash mismatch for %s: expected %s, got %s", url, hash, actualHash)
	}

	// Atomic move to final destination.
	dest := filepath.Join(r.Dir, hash+".zip")
	if err := os.Rename(tmpPath, dest); err != nil {
		return "", fmt.Errorf("registry: failed to move file to %s: %w", dest, err)
	}
	success = true

	return dest, nil
}
