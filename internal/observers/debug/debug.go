// Package debug implements a debug observer that records complete workflow
// execution history to disk for analysis and troubleshooting.
//
// DebugObserver subscribes to ClaudeStreamOutput and TransitionOccurred events
// and writes:
//   - One JSONL file per agent step:
//     {debugDir}/{agentID}_{stateStem}_{step:03d}.jsonl
//   - A chronological state transition log: {debugDir}/transitions.log
//
// The debug directory path is taken from WorkflowStarted.DebugDir, which is
// set by the orchestrator. When DebugDir is empty (debug disabled) the
// observer is a no-op.
//
// All file I/O errors are silently ignored so that debug mode never disrupts
// workflow execution.
package debug

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
)

// DebugObserver writes JSONL step files and a transitions log.
type DebugObserver struct {
	mu       sync.Mutex
	debugDir string // set by WorkflowStarted; empty = no-op
	cancels  []func()
}

// New creates a DebugObserver subscribed to b.
func New(b *bus.Bus) *DebugObserver {
	o := &DebugObserver{}
	o.cancels = []func(){
		bus.Subscribe(b, o.onWorkflowStarted),
		bus.Subscribe(b, o.onClaudeStreamOutput),
		bus.Subscribe(b, o.onTransitionOccurred),
	}
	return o
}

// Close unregisters all subscriptions from the bus.
func (o *DebugObserver) Close() {
	for _, cancel := range o.cancels {
		cancel()
	}
	o.cancels = nil
}

func (o *DebugObserver) onWorkflowStarted(e events.WorkflowStarted) {
	if e.DebugDir == "" {
		return
	}
	_ = os.MkdirAll(e.DebugDir, 0o755)
	o.mu.Lock()
	o.debugDir = e.DebugDir
	o.mu.Unlock()
}

func (o *DebugObserver) onClaudeStreamOutput(e events.ClaudeStreamOutput) {
	o.mu.Lock()
	dir := o.debugDir
	o.mu.Unlock()
	if dir == "" {
		return
	}

	stem := stripExt(e.StateName)
	filename := fmt.Sprintf("%s_%s_%03d.jsonl", e.AgentID, stem, e.StepNumber)
	path := filepath.Join(dir, filename)

	line, err := json.Marshal(e.JSONObject)
	if err != nil {
		return
	}
	appendToFile(path, string(line)+"\n")
}

func (o *DebugObserver) onTransitionOccurred(e events.TransitionOccurred) {
	o.mu.Lock()
	dir := o.debugDir
	o.mu.Unlock()
	if dir == "" {
		return
	}

	logPath := filepath.Join(dir, "transitions.log")
	var sb strings.Builder

	ts := e.Timestamp.Format("2006-01-02T15:04:05.000000")
	if e.ToState != "" {
		fmt.Fprintf(&sb, "%s [%s] %s -> %s (%s)\n",
			ts, e.AgentID, e.FromState, e.ToState, e.TransitionType)
	} else {
		fmt.Fprintf(&sb, "%s [%s] %s -> (result, terminated)\n",
			ts, e.AgentID, e.FromState)
	}

	// Write sorted metadata key-value pairs for deterministic output.
	for _, k := range sortedKeys(e.Metadata) {
		fmt.Fprintf(&sb, "  %s: %v\n", k, e.Metadata[k])
	}
	sb.WriteString("\n")

	appendToFile(logPath, sb.String())
}

// appendToFile appends text to path, creating the file if necessary.
// Errors are silently ignored.
func appendToFile(path, text string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(text)
}

// stripExt removes the last known workflow-state extension from name.
func stripExt(name string) string {
	lower := strings.ToLower(name)
	for _, ext := range []string{".md", ".sh", ".bat", ".ps1"} {
		if strings.HasSuffix(lower, ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name
}

// sortedKeys returns the keys of m in ascending sorted order.
// Uses simple insertion sort (maps are small).
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
