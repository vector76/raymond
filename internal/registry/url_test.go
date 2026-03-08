package registry

import (
	"strings"
	"testing"
)

func TestIsRemoteWorkflowURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"http://example.com/workflow.zip", true},
		{"https://example.com/workflow.zip", true},
		{"https://", true},
		{"http://", true},
		{"/local/path/workflow.zip", false},
		{"workflow.zip", false},
		{"ftp://example.com/workflow.zip", false},
		{"", false},
		{"HTTP://example.com/workflow.zip", false}, // case-sensitive
	}

	for _, tt := range tests {
		got := IsRemoteWorkflowURL(tt.input)
		if got != tt.want {
			t.Errorf("IsRemoteWorkflowURL(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// validHash is a 64-character lowercase hex string for use in tests.
const validHash = "a3f5e1b2c4d6e7f8a3f5e1b2c4d6e7f8a3f5e1b2c4d6e7f8a3f5e1b2c4d6e7f8"

func TestValidateRemoteURL(t *testing.T) {
	t.Run("valid URL returns correct hash", func(t *testing.T) {
		rawURL := "https://example.com/workflows/" + validHash + ".zip"
		hash, err := ValidateRemoteURL(rawURL)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if hash != validHash {
			t.Errorf("got hash %q, want %q", hash, validHash)
		}
	})

	t.Run("URL with no hash returns error", func(t *testing.T) {
		rawURL := "https://example.com/workflows/myworkflow.zip"
		_, err := ValidateRemoteURL(rawURL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "no 64-character") {
			t.Errorf("error %q does not mention missing hash", err.Error())
		}
	})

	t.Run("URL with ambiguous multiple 64-char hex sequences returns error", func(t *testing.T) {
		rawURL := "https://example.com/" + validHash + "-" + validHash + ".zip"
		_, err := ValidateRemoteURL(rawURL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("URL with 63-char hex returns error", func(t *testing.T) {
		short := validHash[:63]
		rawURL := "https://example.com/" + short + ".zip"
		_, err := ValidateRemoteURL(rawURL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("URL with 65-char hex returns error", func(t *testing.T) {
		long := validHash + "a"
		rawURL := "https://example.com/" + long + ".zip"
		_, err := ValidateRemoteURL(rawURL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("unparseable URL returns error", func(t *testing.T) {
		rawURL := "http://[invalid"
		_, err := ValidateRemoteURL(rawURL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("URL with no path segment returns error", func(t *testing.T) {
		rawURL := "https://example.com"
		_, err := ValidateRemoteURL(rawURL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "no filename") {
			t.Errorf("error %q does not mention missing filename", err.Error())
		}
	})

	t.Run("URL with uppercase hex returns error", func(t *testing.T) {
		upperHash := strings.ToUpper(validHash)
		rawURL := "https://example.com/" + upperHash + ".zip"
		_, err := ValidateRemoteURL(rawURL)
		if err == nil {
			t.Fatal("expected error for uppercase hex, got nil")
		}
	})
}
