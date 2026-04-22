package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// pendingLogFile is the JSONL file that stores the append-only operation log.
const pendingLogFile = "pending_inputs.jsonl"

// PendingInput represents a pending await input request from a running workflow.
type PendingInput struct {
	RunID       string     `json:"run_id"`
	AgentID     string     `json:"agent_id"`
	InputID     string     `json:"input_id"`
	Prompt      string     `json:"prompt,omitempty"`
	NextState   string     `json:"next_state,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	TimeoutAt   *time.Time `json:"timeout_at,omitempty"`
	TimeoutNext string     `json:"timeout_next,omitempty"`
}

// logEntry is the on-disk JSONL record format.
type logEntry struct {
	Op    string       `json:"op"`
	Input PendingInput `json:"input,omitempty"`
	// ID is used for "remove" operations where we only need the input ID.
	ID string `json:"id,omitempty"`
}

// PendingRegistry tracks pending await inputs with durable JSONL-based storage.
// On startup it replays the log to reconstruct in-memory state and compacts
// the log to remove stale entries.
type PendingRegistry struct {
	mu      sync.RWMutex
	inputs  map[string]PendingInput // keyed by InputID
	logPath string
}

// NewPendingRegistry creates a PendingRegistry backed by a JSONL file in dir.
// It replays any existing log to reconstruct state and compacts it.
func NewPendingRegistry(dir string) (*PendingRegistry, error) {
	logPath := filepath.Join(dir, pendingLogFile)
	pr := &PendingRegistry{
		inputs:  make(map[string]PendingInput),
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
func (pr *PendingRegistry) Register(pi PendingInput) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if _, exists := pr.inputs[pi.InputID]; exists {
		return fmt.Errorf("pending input %q already registered", pi.InputID)
	}

	entry := logEntry{Op: "register", Input: pi}
	if err := pr.appendLog(entry); err != nil {
		return err
	}

	pr.inputs[pi.InputID] = pi
	return nil
}

// Remove removes a pending input from the registry and persists the removal.
func (pr *PendingRegistry) Remove(inputID string) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if _, exists := pr.inputs[inputID]; !exists {
		return fmt.Errorf("pending input %q not found", inputID)
	}

	entry := logEntry{Op: "remove", ID: inputID}
	if err := pr.appendLog(entry); err != nil {
		return err
	}

	delete(pr.inputs, inputID)
	return nil
}

// Get returns a pending input by InputID.
func (pr *PendingRegistry) Get(inputID string) (*PendingInput, bool) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	pi, ok := pr.inputs[inputID]
	if !ok {
		return nil, false
	}
	return &pi, true
}

// ListAll returns all pending inputs across all runs.
func (pr *PendingRegistry) ListAll() []PendingInput {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	result := make([]PendingInput, 0, len(pr.inputs))
	for _, pi := range pr.inputs {
		result = append(result, pi)
	}
	return result
}

// ListByRun returns pending inputs filtered by run ID.
func (pr *PendingRegistry) ListByRun(runID string) []PendingInput {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	result := []PendingInput{}
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
			pr.inputs[entry.Input.InputID] = entry.Input
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
