package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vector76/raymond/internal/parsing"
	wfstate "github.com/vector76/raymond/internal/state"
)

// pendingLogFile is the JSONL file that stores the append-only operation log.
const pendingLogFile = "pending_inputs.jsonl"

// PendingAsk represents a pending ask input request from a running workflow.
type PendingAsk struct {
	RunID          string                  `json:"run_id"`
	AgentID        string                  `json:"agent_id"`
	AskID        string                  `json:"ask_id"`
	WorkflowID     string                  `json:"workflow_id,omitempty"`
	Prompt         string                  `json:"prompt,omitempty"`
	NextState      string                  `json:"next_state,omitempty"`
	CreatedAt      time.Time               `json:"created_at"`
	TimeoutAt      *time.Time              `json:"timeout_at,omitempty"`
	TimeoutNext    string                  `json:"timeout_next,omitempty"`
	FileAffordance *parsing.FileAffordance `json:"file_affordance,omitempty"`
	StagedFiles    []wfstate.FileRecord    `json:"staged_files,omitempty"`
}

// logEntry is the on-disk JSONL record format.
type logEntry struct {
	Op    string       `json:"op"`
	Input PendingAsk `json:"input,omitempty"`
	// ID is used for "remove" operations where we only need the input ID.
	ID string `json:"id,omitempty"`
}

// PendingRegistry tracks pending ask inputs with durable JSONL-based storage.
// On startup it replays the log to reconstruct in-memory state and compacts
// the log to remove stale entries.
type PendingRegistry struct {
	mu      sync.RWMutex
	inputs  map[string]PendingAsk // keyed by AskID
	logPath string
}

// NewPendingRegistry creates a PendingRegistry backed by a JSONL file in dir.
// It replays any existing log to reconstruct state and compacts it.
func NewPendingRegistry(dir string) (*PendingRegistry, error) {
	logPath := filepath.Join(dir, pendingLogFile)
	pr := &PendingRegistry{
		inputs:  make(map[string]PendingAsk),
		logPath: logPath,
	}

	if err := pr.replay(); err != nil {
		return nil, fmt.Errorf("pending registry replay: %w", err)
	}
	if err := pr.compact(); err != nil {
		return nil, fmt.Errorf("pending registry compact: %w", err)
	}

	return pr, nil
}

// Register adds a pending input to the registry and persists it.
func (pr *PendingRegistry) Register(pi PendingAsk) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if _, exists := pr.inputs[pi.AskID]; exists {
		return fmt.Errorf("pending input %q already registered", pi.AskID)
	}

	entry := logEntry{Op: "register", Input: pi}
	if err := pr.appendLog(entry); err != nil {
		return err
	}

	pr.inputs[pi.AskID] = pi
	return nil
}

// Remove removes a pending input from the registry and persists the removal.
func (pr *PendingRegistry) Remove(askID string) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if _, exists := pr.inputs[askID]; !exists {
		return fmt.Errorf("pending input %q not found", askID)
	}

	entry := logEntry{Op: "remove", ID: askID}
	if err := pr.appendLog(entry); err != nil {
		return err
	}

	delete(pr.inputs, askID)
	return nil
}

// Get returns a pending input by AskID.
func (pr *PendingRegistry) Get(askID string) (*PendingAsk, bool) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	pi, ok := pr.inputs[askID]
	if !ok {
		return nil, false
	}
	return &pi, true
}

// GetAndRemove atomically retrieves and removes a pending input. It returns
// the input and true if it existed, or nil and false otherwise. This prevents
// duplicate delivery when multiple callers race to claim the same input.
func (pr *PendingRegistry) GetAndRemove(askID string) (*PendingAsk, bool) {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	pi, exists := pr.inputs[askID]
	if !exists {
		return nil, false
	}

	entry := logEntry{Op: "remove", ID: askID}
	if err := pr.appendLog(entry); err != nil {
		return nil, false
	}

	delete(pr.inputs, askID)
	return &pi, true
}

// ListAll returns all pending inputs across all runs.
func (pr *PendingRegistry) ListAll() []PendingAsk {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	result := make([]PendingAsk, 0, len(pr.inputs))
	for _, pi := range pr.inputs {
		result = append(result, pi)
	}
	return result
}

// PruneDangling is the single shared drop-policy for pending-registry entries
// whose paired workflow state file is missing or unreadable. The caller
// supplies a stateExists predicate so this function stays independent of any
// particular pool convention — production wiring stats the resolved serve
// pool, future callers (e.g. bead-7's --clean flag) can reuse it with a
// different predicate.
//
// Behaviour: every entry whose RunID fails the predicate is removed from the
// in-memory map AND from the on-disk JSONL log (a "remove" op is appended so
// the next replay does not resurrect it). The set of dropped run ids is
// returned in sorted order and also emitted as a single log line so an
// operator can chase down the underlying cause (manual cleanup, crashed run,
// future --clean invocation, etc).
//
// The predicate is called at most once per distinct RunID — multiple pending
// asks for the same run share one stat — so the policy stays cheap even on a
// registry with many entries.
func (pr *PendingRegistry) PruneDangling(stateExists func(runID string) bool) []string {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	cache := make(map[string]bool)
	askToDrop := make([]string, 0)
	droppedRunIDs := make(map[string]struct{})
	for askID, pi := range pr.inputs {
		ok, cached := cache[pi.RunID]
		if !cached {
			ok = stateExists(pi.RunID)
			cache[pi.RunID] = ok
		}
		if !ok {
			askToDrop = append(askToDrop, askID)
			droppedRunIDs[pi.RunID] = struct{}{}
		}
	}

	for _, askID := range askToDrop {
		// Best-effort durable removal: even if the log append fails the
		// in-memory drop still happens, which is the behaviour callers rely
		// on at startup. A subsequent replay would resurrect the entry, but
		// the next prune would drop it again.
		_ = pr.appendLog(logEntry{Op: "remove", ID: askID})
		delete(pr.inputs, askID)
	}

	out := make([]string, 0, len(droppedRunIDs))
	for id := range droppedRunIDs {
		out = append(out, id)
	}
	sort.Strings(out)

	if len(out) > 0 {
		log.Printf("daemon: dropped %d pending-registry entr(y/ies) whose state file is missing; run ids: %s",
			len(askToDrop), strings.Join(out, ", "))
	}
	return out
}

// ListByRun returns pending inputs filtered by run ID.
func (pr *PendingRegistry) ListByRun(runID string) []PendingAsk {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	result := []PendingAsk{}
	for _, pi := range pr.inputs {
		if pi.RunID == runID {
			result = append(result, pi)
		}
	}
	return result
}

// appendLog appends a single JSON-encoded entry to the log file.
func (pr *PendingRegistry) appendLog(entry logEntry) error {
	f, err := os.OpenFile(pr.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open pending log: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal log entry: %w", err)
	}
	data = append(data, '\n')

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write log entry: %w", err)
	}
	return nil
}

// replay reads the JSONL log and reconstructs the in-memory state.
func (pr *PendingRegistry) replay() error {
	f, err := os.Open(pr.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry logEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip malformed lines
		}

		switch entry.Op {
		case "register":
			pr.inputs[entry.Input.AskID] = entry.Input
		case "remove":
			delete(pr.inputs, entry.ID)
		}
	}

	return scanner.Err()
}

// compact rewrites the log file to contain only the current live records.
func (pr *PendingRegistry) compact() error {
	// Write a new file, then atomically rename.
	tmpPath := pr.logPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create compact temp file: %w", err)
	}

	for _, pi := range pr.inputs {
		entry := logEntry{Op: "register", Input: pi}
		data, err := json.Marshal(entry)
		if err != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("marshal compact entry: %w", err)
		}
		data = append(data, '\n')
		if _, err := f.Write(data); err != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("write compact entry: %w", err)
		}
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close compact file: %w", err)
	}

	return os.Rename(tmpPath, pr.logPath)
}
