package ccsession

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeTranscript creates <root>/<project>/<id>.jsonl with the given raw lines.
func writeTranscript(t *testing.T, root, project, id string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, id+".jsonl")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// asstLine is a minimal assistant transcript line carrying a usage block.
func asstLine(input, cacheCreate, cacheRead, output int) string {
	return `{"type":"assistant","message":{"usage":{` +
		`"input_tokens":` + itoa(input) + `,` +
		`"cache_creation_input_tokens":` + itoa(cacheCreate) + `,` +
		`"cache_read_input_tokens":` + itoa(cacheRead) + `,` +
		`"output_tokens":` + itoa(output) + `}}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestContextTokensSingleTurn(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-user-proj", "sess-1", asstLine(3, 6448, 12881, 161))

	got, err := contextTokensInRoot(root, "sess-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := int64(3 + 6448 + 12881 + 161); got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestContextTokensLastAssistantWins(t *testing.T) {
	// A tool-use loop produces several assistant lines with growing usage; the
	// last one is the current context size.
	root := t.TempDir()
	writeTranscript(t, root, "proj", "sess-2",
		asstLine(10, 0, 100, 5),
		`{"type":"user","message":{"content":[]}}`,
		asstLine(10, 0, 500, 20), // last assistant turn
	)
	got, err := contextTokensInRoot(root, "sess-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := int64(10 + 500 + 20); got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestContextTokensSkipsSidechain(t *testing.T) {
	// The last line is a subagent (sidechain) turn; it must not be reported as
	// the main-thread context size.
	root := t.TempDir()
	writeTranscript(t, root, "proj", "sess-3",
		asstLine(10, 0, 100, 5), // main thread
		`{"type":"assistant","isSidechain":true,"message":{"usage":{"input_tokens":99999,"output_tokens":1}}}`,
	)
	got, err := contextTokensInRoot(root, "sess-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := int64(10 + 100 + 5); got != want {
		t.Errorf("got %d, want %d (sidechain leaked)", got, want)
	}
}

func TestContextTokensToleratesTruncatedFinalLine(t *testing.T) {
	root := t.TempDir()
	// Last line is a partial/truncated JSON object (live append mid-write).
	writeTranscript(t, root, "proj", "sess-4",
		asstLine(10, 0, 100, 5),
		`{"type":"assistant","message":{"usage":{"input_tok`,
	)
	got, err := contextTokensInRoot(root, "sess-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := int64(10 + 100 + 5); got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestContextTokensNotFound(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "proj", "other", asstLine(1, 0, 0, 0))

	_, err := contextTokensInRoot(root, "missing")
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected *NotFoundError, got %v", err)
	}
}

func TestContextTokensAmbiguous(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "proj-a", "dup", asstLine(1, 0, 0, 0))
	writeTranscript(t, root, "proj-b", "dup", asstLine(2, 0, 0, 0))

	_, err := contextTokensInRoot(root, "dup")
	var amb *AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("expected *AmbiguousError, got %v", err)
	}
	if len(amb.Paths) != 2 {
		t.Errorf("expected 2 paths, got %d", len(amb.Paths))
	}
}

func TestContextTokensIgnoresSubagentDirectory(t *testing.T) {
	// A sibling <id>/ directory (subagent transcripts) must not be matched as
	// if it were the session's own <id>.jsonl.
	root := t.TempDir()
	writeTranscript(t, root, "proj", "sess-5", asstLine(7, 0, 0, 3))
	// Create a directory literally named sess-5.jsonl-like; the real layout is
	// projects/<proj>/<id>/ for subagents. Ensure a nested dir doesn't match.
	if err := os.MkdirAll(filepath.Join(root, "proj", "sess-5", "inner"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := contextTokensInRoot(root, "sess-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := int64(7 + 3); got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestContextTokensNoUsageReturnsZero(t *testing.T) {
	root := t.TempDir()
	// File exists but no assistant turn has completed yet.
	writeTranscript(t, root, "proj", "sess-6",
		`{"type":"user","message":{"content":[]}}`,
	)
	got, err := contextTokensInRoot(root, "sess-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestContextTokensRejectsBadID(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"", "../escape", "a/b", "glob*"} {
		if _, err := contextTokensInRoot(root, bad); err == nil {
			t.Errorf("expected error for id %q, got nil", bad)
		}
	}
}
