// Package ccsession reads Claude Code session transcripts off disk to report
// how full a session's context window is.
//
// Claude Code stores each session as a JSONL transcript at
//
//	<config-dir>/projects/<encoded-working-folder>/<session-id>.jsonl
//
// where <config-dir> is $CLAUDE_CONFIG_DIR (when set) or ~/.claude. Session ids
// are globally unique, so this package locates a session by globbing every
// project directory rather than requiring the caller to know which working
// folder produced it. Zero matches and more-than-one match are both errors:
// there is no folder-based disambiguation by design.
//
// The reported number is the context size as of the most recent completed
// assistant turn, computed via ctxtokens.FromUsage so it matches the live
// in-process number raymond records during a run.
package ccsession

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vector76/raymond/internal/ctxtokens"
)

// NotFoundError is returned when no transcript matches the session id.
type NotFoundError struct{ SessionID string }

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no Claude session transcript found for session id %q", e.SessionID)
}

// AmbiguousError is returned when more than one transcript matches the session
// id (e.g. the same id was resumed under two different working folders). The
// matching paths are included so the caller can investigate.
type AmbiguousError struct {
	SessionID string
	Paths     []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("session id %q matches %d transcripts: %s",
		e.SessionID, len(e.Paths), strings.Join(e.Paths, ", "))
}

// ContextTokensForSession returns the context-token count for the given session
// id, located under the default projects root ($CLAUDE_CONFIG_DIR/projects or
// ~/.claude/projects).
//
// Returns *NotFoundError when no transcript matches and *AmbiguousError when
// more than one does.
func ContextTokensForSession(sessionID string) (int64, error) {
	root, err := projectsRoot()
	if err != nil {
		return 0, err
	}
	return contextTokensInRoot(root, sessionID)
}

// projectsRoot resolves the Claude Code projects directory, honoring
// CLAUDE_CONFIG_DIR and falling back to ~/.claude.
func projectsRoot() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate home directory for Claude config: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// contextTokensInRoot is the testable core: it searches projectsRoot for
// exactly one <id>.jsonl transcript and returns its context-token count.
func contextTokensInRoot(projectsRoot, sessionID string) (int64, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("session id is empty")
	}
	// Session ids are UUIDs; reject anything that could escape the glob into
	// other directories or smuggle in glob metacharacters.
	if strings.ContainsAny(sessionID, `/\*?[]`) {
		return 0, fmt.Errorf("invalid session id %q", sessionID)
	}

	// Match <projectsRoot>/<project>/<id>.jsonl. The single "*" descends one
	// level into the per-folder project directories and never matches the
	// sibling <id>/ subagent directories (those have no .jsonl suffix).
	pattern := filepath.Join(projectsRoot, "*", sessionID+".jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("searching for session transcript: %w", err)
	}
	switch len(matches) {
	case 0:
		return 0, &NotFoundError{SessionID: sessionID}
	case 1:
		// fall through
	default:
		return 0, &AmbiguousError{SessionID: sessionID, Paths: matches}
	}

	return contextTokensFromFile(matches[0])
}

// contextTokensFromFile parses one transcript and returns the context size as
// of its last completed main-thread assistant turn.
//
// The transcript is a sequence of JSON-per-line records of many types; a single
// user turn with tool use produces multiple assistant records, each with its
// own (growing) usage. We want the LAST assistant record that carries a usage
// block — which also naturally reflects the post-compaction size after a
// context compaction. Sidechain (subagent) records are skipped so the number
// reflects the main thread, not a subagent's context. A record that fails to
// parse is skipped, which tolerates a truncated final line in a transcript that
// is still being appended to by a live session.
func contextTokensFromFile(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("reading session transcript %s: %w", path, err)
	}

	var lastTokens int64
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip malformed / truncated lines
		}
		if t, _ := rec["type"].(string); t != "assistant" {
			continue
		}
		if sc, _ := rec["isSidechain"].(bool); sc {
			continue
		}
		usage := assistantUsage(rec)
		if usage == nil {
			continue
		}
		lastTokens = ctxtokens.FromUsage(usage)
	}
	return lastTokens, nil
}

// assistantUsage extracts message.usage from a transcript assistant record,
// returning nil when it is absent or not an object.
func assistantUsage(rec map[string]any) map[string]any {
	msg, ok := rec["message"].(map[string]any)
	if !ok {
		return nil
	}
	usage, ok := msg["usage"].(map[string]any)
	if !ok {
		return nil
	}
	return usage
}
