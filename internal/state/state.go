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
//	  "budget_usd":     0.0,
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
	"github.com/vector76/raymond/internal/parsing"
)

// Agent status constants. The zero value ("") means the agent is active.
const (
	AgentStatusPaused   = "paused"
	AgentStatusFailed   = "failed"
	AgentStatusAsking = "asking"
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
	Session      *string `json:"session"`                 // caller session_id; null when the caller had no session
	State        string  `json:"state"`                   // return state filename
	ScopeDir     string  `json:"scope_dir,omitempty"`     // caller's scope directory
	Cwd          string  `json:"cwd,omitempty"`           // caller's working directory
	NestingDepth int     `json:"nesting_depth,omitempty"` // caller's cross-workflow nesting depth at the time of the call
	ScopeURL     string  `json:"scope_url,omitempty"`     // original URL when scope was fetched from a remote URL
	TaskFolder   string  `json:"task_folder,omitempty"`   // output folder assigned to this call frame
}

// AgentState holds the persisted state of a single agent within a workflow.
type AgentState struct {
	ID             string       `json:"id"`
	CurrentState   string       `json:"current_state"`
	SessionID      *string      `json:"session_id"`                 // null when no session has been started
	Stack          []StackFrame `json:"stack"`                      // call-return stack of frames
	PendingResult  *string      `json:"pending_result,omitempty"`   // absent from JSON when nil
	PendingAskID string       `json:"pending_ask_id,omitempty"` // {{ask_id}} for the immediately-following state
	Cwd            string       `json:"cwd,omitempty"`              // per-agent working directory; empty = inherit
	ScopeDir       string       `json:"scope_dir,omitempty"`        // per-agent scope directory; empty = use workflow ScopeDir
	ScopeURL       string       `json:"scope_url,omitempty"`        // original URL when scope was fetched from a remote URL

	// Cross-workflow nesting depth. Incremented by call-workflow/function-workflow,
	// restored by result. Capped at 4 to prevent runaway recursion.
	NestingDepth int `json:"nesting_depth,omitempty"`

	// Orchestrator-managed lifecycle fields.
	Status     string `json:"status,omitempty"`      // "paused", "failed", "asking", or "" (active)
	RetryCount int    `json:"retry_count,omitempty"` // transient error retry counter
	Error      string `json:"error,omitempty"`       // last error message when paused/failed

	// ContinueAndFork, when true, tells the executor to use `-c --fork-session`
	// on the very first invocation (continuing from the user's most recent
	// interactive Claude session). Cleared after first successful use.
	ContinueAndFork bool `json:"continue_and_fork,omitempty"`

	TaskFolder string `json:"task_folder,omitempty"` // output folder for this agent's tasks
	TaskCount  int    `json:"task_count,omitempty"`  // number of tasks spawned so far

	// Ask fields — populated by the ask transition handler when an agent
	// emits an <ask> tag. All zero-value safe for backward compatibility.
	AskPrompt      string `json:"ask_prompt,omitempty"`       // human-facing prompt text from <ask> tag content
	AskNextState   string `json:"ask_next_state,omitempty"`   // state to transition to when input arrives
	AskTimeout     string `json:"ask_timeout,omitempty"`      // timeout duration string (e.g. "24h")
	AskTimeoutNext string `json:"ask_timeout_next,omitempty"` // state to transition to on timeout
	AskID     string `json:"ask_id,omitempty"`     // unique identifier for correlating the response

	// File-affordance fields — populated when a file-bearing ask is entered.
	// All zero-value safe for backward compatibility.
	AskFileAffordance *parsing.FileAffordance `json:"ask_file_affordance,omitempty"` // descriptor parsed from the <ask> tag
	AskStagedFiles    []FileRecord            `json:"ask_staged_files,omitempty"`    // display files staged into the input subdirectory at ask-entry
	AskEnteredAt      time.Time               `json:"ask_entered_at,omitempty"`      // wall-clock time the ask was entered; stamped onto the ResolvedInput record on resume

	// Transient execution fields — not persisted to JSON.
	// Set by the orchestrator / transition handlers; consumed by the next executor step.
	ForkSessionID  *string           `json:"-"` // session to fork from (call transitions)
	ForkAttributes map[string]string `json:"-"` // template variables from fork
}

// LaunchParams holds the runtime parameters that were used when a workflow was
// started. They are persisted in the state file so that --resume can restore
// them as defaults for any flag not explicitly specified on the CLI.
type LaunchParams struct {
	DangerouslySkipPermissions bool    `json:"dangerously_skip_permissions"`
	Model                      string  `json:"model,omitempty"`
	Effort                     string  `json:"effort,omitempty"`
	Timeout                    float64 `json:"timeout,omitempty"`
	ContinueAndFork            bool    `json:"continue_and_fork,omitempty"`
	OnAsk                    string  `json:"on_ask,omitempty"`
}

// WorkflowState is the top-level structure persisted for each workflow.
type WorkflowState struct {
	WorkflowID   string  `json:"workflow_id"`
	ScopeDir     string  `json:"scope_dir"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	// BudgetUSD is the cost cap in USD enforced by the markdown executor.
	// Zero means unlimited: the budget check is skipped entirely when
	// BudgetUSD == 0. Set at workflow creation time and never mutated.
	BudgetUSD      float64         `json:"budget_usd"`
	StartedAt      time.Time       `json:"started_at"` // wall-clock launch time, recovered after restarts
	Agents         []AgentState    `json:"agents"`
	ForkCounters   map[string]int  `json:"fork_counters,omitempty"`   // per-parent agent fork counters
	LaunchParams   *LaunchParams   `json:"launch_params,omitempty"`   // persisted for --resume restoration
	ResolvedInputs []ResolvedInput `json:"resolved_inputs,omitempty"` // durable history of resolved input steps (file-bearing or text-only)

	// Transient: populated by HandleResult when an agent terminates; consumed by orchestrator.
	AgentTerminationResults map[string]string `json:"-"`
	TaskFolderPattern       string            `json:"-"` // pattern for computing per-task output folders
}

// Pool identifies which run-state pool a caller wants to operate on.
//
// The CLI pool (PoolCLI) holds state files for `ray <workflow>` invocations
// and resolves to .raymond/state/ — the historical, byte-for-byte default.
// The serve pool (PoolServe) holds state files for the `ray serve` daemon
// and resolves to the sibling .raymond/serve-state/. The two pools are
// siblings rather than parent-and-child so that any future "enumerate
// everything" tool walks two independent roots without one descending into
// the other. See docs/serve-run-pool.md for the full rationale.
type Pool int

const (
	// PoolCLI is the run-state pool owned by `ray <workflow>` invocations.
	// Resolves to .raymond/state/ under the project's raymond directory.
	PoolCLI Pool = iota

	// PoolServe is the run-state pool owned by the `ray serve` daemon.
	// Resolves to .raymond/serve-state/ under the project's raymond
	// directory — a sibling of, not nested inside, the CLI pool.
	PoolServe
)

// poolSubdir returns the directory name (relative to the project's raymond
// directory) that holds the given pool's state files.
func (p Pool) poolSubdir() string {
	switch p {
	case PoolServe:
		return "serve-state"
	default:
		// PoolCLI and any unknown value fall back to the historical CLI
		// path so that a forgotten/zero Pool never silently writes to the
		// serve pool.
		return "state"
	}
}

// ResolvePoolDir resolves the on-disk state directory for the given pool.
//
// When override is non-empty, it is returned unchanged. This preserves the
// existing test-injection contract: tests (and the hidden `--state-dir`
// flag) can point any pool at an arbitrary directory.
//
// When override is empty, the project's raymond directory is discovered via
// config.FindRaymondDir and the pool-specific subdirectory is appended:
// .raymond/state/ for PoolCLI, .raymond/serve-state/ for PoolServe. If the
// raymond directory cannot be discovered, ResolvePoolDir falls back to
// "<cwd>/.raymond/<subdir>" — the same fallback GetStateDir has always used.
//
// Pool selection is intentionally a caller-side decision: there is no env
// var, file marker, or process-inheritance mechanism that lets a child `ray`
// process learn it was invoked from inside another ray run. A workflow that
// shells out to `ray <workflow>` from inside a serve-pool run therefore
// lands the nested state file in the CLI pool — `ray <workflow>` always
// resolves to PoolCLI. Authors who want in-process orchestration should use
// <fork> / <fork-workflow> instead of shelling out. The
// TestNestedRayLaunchRoutesToCLIPool regression test pins this absence of
// special-casing.
func ResolvePoolDir(pool Pool, override string) string {
	if override != "" {
		return override
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	raymondDir, err := config.FindRaymondDir(cwd, true)
	if err != nil || raymondDir == "" {
		raymondDir = filepath.Join(cwd, ".raymond")
	}
	return filepath.Join(raymondDir, pool.poolSubdir())
}

// GetStateDir returns the CLI-pool state directory to use. If stateDir is
// non-empty it is returned unchanged. Otherwise the default location
// (.raymond/state/ at the project root) is computed using
// config.FindRaymondDir.
//
// GetStateDir is preserved as a thin wrapper around ResolvePoolDir(PoolCLI, …)
// so that existing call sites continue to compile while higher layers migrate
// to the typed Pool surface.
func GetStateDir(stateDir string) string {
	return ResolvePoolDir(PoolCLI, stateDir)
}

// ReadStateIn reads the workflow state for workflowID from the given pool.
//
// override has the same semantics as ResolvePoolDir's override argument: empty
// means use the pool's default directory; non-empty is the exact directory to
// read from. This is how the hidden `--state-dir` test-injection flag works.
//
// Returns os.ErrNotExist (via errors.Is) when the state file does not exist.
// Returns *StateFileError when the file exists but contains invalid JSON.
func ReadStateIn(workflowID string, pool Pool, override string) (*WorkflowState, error) {
	return readStateFromDir(workflowID, ResolvePoolDir(pool, override))
}

// ReadState reads the workflow state for workflowID from stateDir.
//
// ReadState is a CLI-pool shim around ReadStateIn — it exists so legacy
// callers and tests that pass a literal directory continue to compile. New
// callers should prefer ReadStateIn with an explicit Pool.
//
// Returns os.ErrNotExist (via errors.Is) when the state file does not exist.
// Returns *StateFileError when the file exists but contains invalid JSON.
func ReadState(workflowID, stateDir string) (*WorkflowState, error) {
	return ReadStateIn(workflowID, PoolCLI, stateDir)
}

// readStateFromDir is the pool-agnostic core. dir must be the already-resolved
// directory containing <workflowID>.json.
func readStateFromDir(workflowID, dir string) (*WorkflowState, error) {
	path := filepath.Join(dir, workflowID+".json")

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

	// Migration: propagate workflow-level ScopeDir to agents that don't have their own.
	// This handles state files written before ScopeDir was added to AgentState.
	if ws.ScopeDir != "" {
		for i := range ws.Agents {
			if ws.Agents[i].ScopeDir == "" {
				ws.Agents[i].ScopeDir = ws.ScopeDir
			}
		}
	}

	return &ws, nil
}

// WriteStateIn atomically writes ws into the given pool's directory.
//
// override has the same semantics as ResolvePoolDir's override argument: empty
// means use the pool's default directory; non-empty is the exact directory to
// write into.
//
// The directory is created if it does not exist. The write is atomic on all
// supported platforms: the JSON is written to a temporary file and then
// renamed over the destination.
//
// Nil-pointer fields in WorkflowState and AgentState are serialized as JSON
// null (not omitted). Callers that want to preserve an existing field value
// must read the current state first and carry that value forward —
// WriteStateIn always overwrites the complete state file.
func WriteStateIn(workflowID string, ws *WorkflowState, pool Pool, override string) error {
	return writeStateToDir(workflowID, ws, ResolvePoolDir(pool, override))
}

// WriteState is a CLI-pool shim around WriteStateIn.
func WriteState(workflowID string, ws *WorkflowState, stateDir string) error {
	return WriteStateIn(workflowID, ws, PoolCLI, stateDir)
}

func writeStateToDir(workflowID string, ws *WorkflowState, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create state directory %s: %w", dir, err)
	}

	final := filepath.Join(dir, workflowID+".json")

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

// DeleteStateIn removes the state file for workflowID from the given pool's
// directory. If the file does not exist, DeleteStateIn returns nil
// (idempotent).
func DeleteStateIn(workflowID string, pool Pool, override string) error {
	return deleteStateInDir(workflowID, ResolvePoolDir(pool, override))
}

// DeleteState is a CLI-pool shim around DeleteStateIn.
func DeleteState(workflowID, stateDir string) error {
	return DeleteStateIn(workflowID, PoolCLI, stateDir)
}

func deleteStateInDir(workflowID, dir string) error {
	path := filepath.Join(dir, workflowID+".json")
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to delete state file %s: %w", path, err)
	}
	return nil
}

// ListWorkflowsIn returns the workflow IDs of all .json files in the given
// pool's directory. Non-.json files are ignored. Returns nil (not an error)
// when the directory does not exist — every pool tolerates a missing pool
// directory the same way.
func ListWorkflowsIn(pool Pool, override string) ([]string, error) {
	return listWorkflowsInDir(ResolvePoolDir(pool, override))
}

// ListWorkflows is a CLI-pool shim around ListWorkflowsIn.
func ListWorkflows(stateDir string) ([]string, error) {
	return ListWorkflowsIn(PoolCLI, stateDir)
}

func listWorkflowsInDir(dir string) ([]string, error) {
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
// (even if the value is an empty string), making it available as {{input}}
// when the first state's prompt is rendered.
func CreateInitialState(workflowID, scopeDir, initialState string, budgetUSD float64, initialInput *string, scopeURL string, launchParams ...*LaunchParams) *WorkflowState {
	agent := AgentState{
		ID:           "main",
		CurrentState: initialState,
		SessionID:    nil,
		Stack:        []StackFrame{},
		ScopeDir:     scopeDir,
		ScopeURL:     scopeURL,
	}
	if initialInput != nil {
		agent.PendingResult = initialInput
	}
	if len(launchParams) > 0 && launchParams[0] != nil && launchParams[0].ContinueAndFork {
		agent.ContinueAndFork = true
	}

	ws := &WorkflowState{
		WorkflowID:   workflowID,
		ScopeDir:     scopeDir,
		TotalCostUSD: 0.0,
		BudgetUSD:    budgetUSD,
		StartedAt:    time.Now(),
		Agents:       []AgentState{agent},
	}
	if len(launchParams) > 0 && launchParams[0] != nil {
		ws.LaunchParams = launchParams[0]
	}
	return ws
}

// GenerateWorkflowIDIn generates a unique workflow ID of the form
// workflow_YYYY-MM-DD_HH-MM-SS-ffffff (timestamp with microseconds) for the
// given pool.
//
// If a collision with an existing state file in the *same pool* is detected,
// a counter suffix is appended until the ID is unique within that pool. The
// collision check is intentionally scoped to one pool: cross-pool collisions
// are vanishingly rare in practice (microsecond timestamps), and the two
// pools manage independent id namespaces by design — see
// docs/serve-run-pool.md.
func GenerateWorkflowIDIn(pool Pool, override string) (string, error) {
	return generateWorkflowIDInDir(ResolvePoolDir(pool, override))
}

// GenerateWorkflowID is a CLI-pool shim around GenerateWorkflowIDIn.
func GenerateWorkflowID(stateDir string) (string, error) {
	return GenerateWorkflowIDIn(PoolCLI, stateDir)
}

func generateWorkflowIDInDir(dir string) (string, error) {
	existing, err := listWorkflowsInDir(dir)
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

// RecoverWorkflowsIn returns the IDs of workflows in the given pool that have
// at least one active agent (i.e. workflows that can be resumed). Malformed
// or unreadable state files are silently skipped. Returns nil (not an error)
// when the pool's directory does not exist — every pool tolerates a missing
// pool directory the same way.
func RecoverWorkflowsIn(pool Pool, override string) ([]string, error) {
	return recoverWorkflowsInDir(ResolvePoolDir(pool, override))
}

// RecoverWorkflows is a CLI-pool shim around RecoverWorkflowsIn.
func RecoverWorkflows(stateDir string) ([]string, error) {
	return RecoverWorkflowsIn(PoolCLI, stateDir)
}

func recoverWorkflowsInDir(dir string) ([]string, error) {
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
