package state

import "time"

// FileRecord captures metadata for a file associated with an input step.
// Source distinguishes a file that the runtime staged from a workflow-declared
// display path ("display") from one that the user uploaded ("upload").
type FileRecord struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
	Source      string `json:"source,omitempty"`
}

// ResolvedInput is the durable history record for an input step that has
// resolved. It lets the run-history view re-display the full context of a
// past input (prompt, files the user saw, text they returned, files they
// uploaded) after the await has resolved and the per-agent await fields have
// been cleared.
type ResolvedInput struct {
	InputID       string       `json:"input_id"`
	AgentID       string       `json:"agent_id"`
	Prompt        string       `json:"prompt,omitempty"`
	NextState     string       `json:"next_state,omitempty"`
	ResponseText  string       `json:"response_text,omitempty"`
	StagedFiles   []FileRecord `json:"staged_files,omitempty"`
	UploadedFiles []FileRecord `json:"uploaded_files,omitempty"`
	EnteredAt     time.Time    `json:"entered_at,omitempty"`
	ResolvedAt    time.Time    `json:"resolved_at,omitempty"`
}
