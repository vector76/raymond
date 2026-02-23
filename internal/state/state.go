// Package state handles reading, writing, and managing workflow state files.
//
// State files are stored as JSON in a configurable directory (default:
// .raymond/state/ at the project root). Each workflow has exactly one state
// file named <workflow-id>.json.
//
// Writes are atomic: the JSON is written to a temporary file in the same
// directory then renamed over the destination, preventing partial writes from
// corrupting state.
//
// State format:
//
//	{
//	  "workflow_id": "workflow_2024-01-15_12-30-45-123456",
//	  "scope_dir":   "workflows/myapp",
//	  "total_cost_usd": 0.0,
//	  "budget_usd":     10.0,
//	  "agents": [
//	    {
//	      "id":            "main",
//	      "current_state": "START.md",
//	      "session_id":    null,
//	      "stack":         []
//	    }
//	  ]
//	}
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vector76/raymond/internal/config"
)

// Agent status constants. The zero value ("") means the agent is active.
const (
	AgentStatusPaused = "paused"
	AgentStatusFailed = "failed"
)

// StateFileError is returned when a state file cannot be parsed.
type StateFileError struct {
	msg string
}

func (e *StateFileError) Error() string { return e.msg }

// StackFrame is one entry on an agent's call-return stack.
// It records the caller's session and the state to return to after the
// current function/call completes.
type StackFrame struct {
	Session *string `json:"session"` // caller session_id; null when the caller had no session
	State   string  `json:"state"`   // return state filename
}

// AgentState holds the persisted state of a single agent within a workflow.
type AgentState struct {
	ID            string       `json:"id"`
	CurrentState  string       `json:"current_state"`
	SessionID     *string      `json:"session_id"`               // null when no session has been started
	Stack         []StackFrame `json:"stack"`                    // call-return stack of frames
	PendingResult *string      `json:"pending_result,omitempty"` // absent from JSON when nil
	Cwd           string       `json:"cwd,omitempty"`            // per-agent working directory; empty = inherit

	// Orchestrator-managed lifecycle fields.
	Status     string `json:"status,omitempty"`      // "paused", "failed", or "" (active)
	RetryCount int    `json:"retry_count,omitempty"` // transient error retry counter
	Error      string `json:"error,omitempty"`       // last error message when paused/failed

	// Transient execution fields — not persisted to JSON.
	// Set by the orchestrator / transition handlers; consumed by the next executor step.
	ForkSessionID  *string           `json:"-"` // session to fork from (call transitions)
	ForkAttributes map[string]string `json:"-"` // template variables from fork
}

// WorkflowState is the top-level structure persisted for each workflow.
type WorkflowState struct {
	WorkflowID   string         `json:"workflow_id"`
	ScopeDir     string         `json:"scope_dir"`
	TotalCostUSD float64        `json:"total_cost_usd"`
	BudgetUSD    float64        `json:"budget_usd"`
	Agents       []AgentState   `json:"agents"`
	ForkCounters map[string]int `json:"fork_counters,omitempty"` // per-parent agent fork counters

	// Transient: populated by HandleResult when an agent terminates; consumed by orchestrator.
	AgentTerminationResults map[string]string `json:"-"`
}

// GetStateDir returns the state directory to use. If stateDir is non-empty it
// is returned unchanged. Otherwise the default location (.raymond/state/ at
// the project root) is computed using config.FindRaymondDir.
func GetStateDir(stateDir string) string {
	if stateDir != "" {
		return stateDir
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	raymondDir, err := config.FindRaymondDir(cwd, true)
	if err != nil || raymondDir == "" {
		raymondDir = filepath.Join(cwd, ".raymond")
	}
	return filepath.Join(raymondDir, "state")
}

// ReadState reads the workflow state for workflowID from stateDir.
//
// Returns os.ErrNotExist (via errors.Is) when the state file does not exist.
// Returns *StateFileError when the file exists but contains invalid JSON.
func ReadState(workflowID, stateDir string) (*WorkflowState, error) {
	path := statePath(stateDir, workflowID)

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("state file not found: %s: %w", path, os.ErrNotExist)
		}
		return nil, fmt.Errorf("failed to read state file %s: %w", path, err)
	}

	var ws WorkflowState
	if err := json.Unmarshal(data, &ws); err != nil {
		return nil, &StateFileError{msg: fmt.Sprintf("malformed state file %s: %v", path, err)}
	}
	return &ws, nil
}

// WriteState atomically writes ws to stateDir/<workflowID>.json.
//
// The directory is created if it does not exist. The write is atomic on
// all supported platforms: the JSON is written to a temporary file and then
// renamed over the destination.
func WriteState(workflowID string, ws *WorkflowState, stateDir string) error {
	dir := GetStateDir(stateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create state directory %s: %w", dir, err)
	}

	final := statePath(stateDir, workflowID)

	tmp, err := os.CreateTemp(dir, workflowID+"_*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpName := tmp.Name()

	writeErr := func() error {
		enc := json.NewEncoder(tmp)
		enc.SetIndent("", "  ")
		if err := enc.Encode(ws); err != nil {
			return err
		}
		return tmp.Close()
	}()
	if writeErr != nil {
		tmp.Close()
		if rmErr := os.Remove(tmpName); rmErr != nil {
			log.Printf("warning: failed to remove temp state file %s: %v", tmpName, rmErr)
		}
		return fmt.Errorf("failed to write state: %w", writeErr)
	}

	if err := os.Rename(tmpName, final); err != nil {
		if rmErr := os.Remove(tmpName); rmErr != nil {
			log.Printf("warning: failed to remove temp state file %s: %v", tmpName, rmErr)
		}
		return fmt.Errorf("failed to rename state file: %w", err)
	}
	return nil
}

// DeleteState removes the state file for workflowID. If the file does not
// exist, DeleteState returns nil (idempotent).
func DeleteState(workflowID, stateDir string) error {
	path := statePath(stateDir, workflowID)
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to delete state file %s: %w", path, err)
	}
	return nil
}

// ListWorkflows returns the workflow IDs of all .json files in stateDir.
// Non-.json files are ignored. Returns nil (not an error) when the directory
// does not exist.
func ListWorkflows(stateDir string) ([]string, error) {
	dir := GetStateDir(stateDir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read state directory %s: %w", dir, err)
	}

	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
	}
	return ids, nil
}

// CreateInitialState constructs a WorkflowState for a new workflow with a
// single "main" agent positioned at initialState.
//
// If initialInput is non-nil, the agent's PendingResult is set to its value
// (even if the value is an empty string), making it available as {{result}}
// when the first state's prompt is rendered.
func CreateInitialState(workflowID, scopeDir, initialState string, budgetUSD float64, initialInput *string) *WorkflowState {
	agent := AgentState{
		ID:           "main",
		CurrentState: initialState,
		SessionID:    nil,
		Stack:        []StackFrame{},
	}
	if initialInput != nil {
		agent.PendingResult = initialInput
	}

	return &WorkflowState{
		WorkflowID:   workflowID,
		ScopeDir:     scopeDir,
		TotalCostUSD: 0.0,
		BudgetUSD:    budgetUSD,
		Agents:       []AgentState{agent},
	}
}

// GenerateWorkflowID generates a unique workflow ID of the form
// workflow_YYYY-MM-DD_HH-MM-SS-ffffff (timestamp with microseconds).
//
// If a collision with an existing state file is detected, a counter suffix
// is appended until the ID is unique.
func GenerateWorkflowID(stateDir string) (string, error) {
	existing, err := ListWorkflows(stateDir)
	if err != nil {
		return "", err
	}
	existingSet := make(map[string]bool, len(existing))
	for _, id := range existing {
		existingSet[id] = true
	}

	t := time.Now()
	micro := t.Nanosecond() / 1000
	timestamp := fmt.Sprintf("%s-%06d", t.Format("2006-01-02_15-04-05"), micro)
	baseID := "workflow_" + timestamp

	id := baseID
	for counter := 1; existingSet[id]; counter++ {
		id = fmt.Sprintf("%s_%d", baseID, counter)
	}
	return id, nil
}

// RecoverWorkflows returns the IDs of workflows that have at least one active
// agent (i.e. workflows that can be resumed). Malformed or unreadable state
// files are silently skipped.
func RecoverWorkflows(stateDir string) ([]string, error) {
	dir := GetStateDir(stateDir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read state directory %s: %w", dir, err)
	}

	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // skip unreadable files
		}

		var ws WorkflowState
		if err := json.Unmarshal(data, &ws); err != nil {
			continue // skip malformed files
		}

		if len(ws.Agents) > 0 {
			ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	return ids, nil
}

// statePath returns the full path to the state file for workflowID.
// stateDir is used as-is (no GetStateDir expansion) so callers that already
// have the resolved dir can call this directly.
func statePath(stateDir, workflowID string) string {
	return filepath.Join(stateDir, workflowID+".json")
}

