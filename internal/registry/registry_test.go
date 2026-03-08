package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hashOf computes the hex-encoded SHA256 of content.
func hashOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func TestLookup_CacheHit(t *testing.T) {
	dir := t.TempDir()
	r := New(dir)

	content := []byte("fake zip content")
	hash := hashOf(content)

	// Pre-create the registry directory and file.
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(r.Dir, hash+".zip")
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		t.Fatal(err)
	}

	path, ok := r.Lookup(hash)
	if !ok {
		t.Fatal("expected cache hit, got miss")
	}
	if path != dest {
		t.Errorf("expected path %s, got %s", dest, path)
	}
}

func TestLookup_CacheMiss(t *testing.T) {
	dir := t.TempDir()
	r := New(dir)

	_, ok := r.Lookup("aabbcc")
	if ok {
		t.Fatal("expected cache miss, got hit")
	}
}

func TestFetch_CacheHit_NoHTTP(t *testing.T) {
	dir := t.TempDir()
	r := New(dir)

	content := []byte("cached zip")
	hash := hashOf(content)

	// Pre-create the cached file.
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(r.Dir, hash+".zip")
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Use an intentionally broken URL — cache hit must not trigger any HTTP call.
	path, err := r.Fetch("http://0.0.0.0:0/should-not-connect", hash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != dest {
		t.Errorf("expected path %s, got %s", dest, path)
	}
}

func TestFetch_SuccessfulDownload(t *testing.T) {
	content := []byte("valid zip content for download")
	hash := hashOf(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(content) //nolint:errcheck
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := New(dir)

	path, err := r.Fetch(srv.URL+"/"+hash+".zip", hash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join(r.Dir, hash+".zip")
	if path != expected {
		t.Errorf("expected path %s, got %s", expected, path)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read downloaded file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("file content mismatch: expected %q, got %q", content, got)
	}
}

func TestFetch_HashMismatch(t *testing.T) {
	content := []byte("content that does not match the hash")
	wrongHash := strings.Repeat("a", 64) // valid-looking but wrong hash

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(content) //nolint:errcheck
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := New(dir)

	_, err := r.Fetch(srv.URL+"/"+wrongHash+".zip", wrongHash)
	if err == nil {
		t.Fatal("expected error on hash mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("expected 'hash mismatch' in error, got: %v", err)
	}

	// Temp file must be cleaned up.
	entries, readErr := os.ReadDir(r.Dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file was not cleaned up: %s", e.Name())
		}
	}
}

func TestFetch_CreatesRegistryDir(t *testing.T) {
	content := []byte("fresh download, no dir yet")
	hash := hashOf(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(content) //nolint:errcheck
	}))
	defer srv.Close()

	// Point registry at a non-existent subdirectory.
	base := t.TempDir()
	raymondDir := filepath.Join(base, "project", ".raymond")
	r := New(raymondDir)

	// Confirm registry dir does not exist yet.
	if _, err := os.Stat(r.Dir); !os.IsNotExist(err) {
		t.Fatal("registry dir should not exist before Fetch")
	}

	path, err := r.Fetch(srv.URL+"/"+hash+".zip", hash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(r.Dir); err != nil {
		t.Errorf("registry dir was not created: %v", err)
	}

	expected := filepath.Join(r.Dir, hash+".zip")
	if path != expected {
		t.Errorf("expected path %s, got %s", expected, path)
	}
}

func TestFetch_NetworkError(t *testing.T) {
	// Start a real server, capture its URL, then close it before fetching
	// to guarantee a connection-refused error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	dir := t.TempDir()
	r := New(dir)
	hash := strings.Repeat("b", 64)

	_, err := r.Fetch(addr+"/"+hash+".zip", hash)
	if err == nil {
		t.Fatal("expected network error, got nil")
	}
}

func TestFetch_HTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := New(dir)
	hash := strings.Repeat("c", 64)

	_, err := r.Fetch(srv.URL+"/"+hash+".zip", hash)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected '404' in error, got: %v", err)
	}
}
