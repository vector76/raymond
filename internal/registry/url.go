package registry

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/vector76/raymond/internal/zipscope"
)

// IsRemoteWorkflowURL returns true if s has an http:// or https:// prefix.
func IsRemoteWorkflowURL(s string) bool {
	return len(s) >= 7 && s[:7] == "http://" ||
		len(s) >= 8 && s[:8] == "https://"
}

// ValidateRemoteURL parses rawURL and extracts the SHA256 hash from the
// filename portion of its path. Returns the hash on success, or an error if
// the URL cannot be parsed, has no filename, or the filename contains no
// unambiguous 64-character lowercase hex hash. Only lowercase hex is accepted.
func ValidateRemoteURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}

	filename := path.Base(parsed.Path)
	if filename == "" || filename == "." || filename == "/" {
		return "", fmt.Errorf("URL %q has no filename in its path", rawURL)
	}

	hash, err := zipscope.ExtractHashFromFilename(filename)
	if err != nil {
		return "", fmt.Errorf("URL %q: %w", rawURL, err)
	}
	if hash == "" {
		return "", fmt.Errorf("URL %q: filename %q contains no 64-character hex hash", rawURL, filename)
	}

	// Confirm the hash appears literally (lowercase) in the filename,
	// rejecting filenames whose hex run uses uppercase characters.
	if !strings.Contains(filename, hash) {
		return "", fmt.Errorf("URL %q: filename %q contains no 64-character lowercase hex hash", rawURL, filename)
	}

	return hash, nil
}
