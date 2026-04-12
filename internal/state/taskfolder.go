package state

import (
	"fmt"
	"strings"
)

// ComputeTaskFolderPath computes the output folder path for a task by
// substituting template variables in pattern.
//
// If taskCount > 0, the suffix "_task<taskCount>" is appended to agentID
// before substitution (e.g. agentID "main" with count 2 → "main_task2").
//
// Template variables replaced:
//   - {{workflow_id}} → workflowID
//   - {{agent_id}}    → agentID (possibly with task suffix)
//
// The caller guarantees pattern is already an absolute path. This function
// performs no filesystem I/O.
func ComputeTaskFolderPath(pattern, workflowID, agentID string, taskCount int) string {
	effectiveAgentID := agentID
	if taskCount > 0 {
		effectiveAgentID = fmt.Sprintf("%s_task%d", agentID, taskCount)
	}
	result := strings.ReplaceAll(pattern, "{{workflow_id}}", workflowID)
	result = strings.ReplaceAll(result, "{{agent_id}}", effectiveAgentID)
	return result
}
