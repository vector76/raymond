package state_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vector76/raymond/internal/state"
)

func TestComputeTaskFolderPath_Count0_NoSuffix(t *testing.T) {
	result := state.ComputeTaskFolderPath("/output/{{agent_id}}", "wf-001", "main", 0)
	assert.Equal(t, "/output/main", result)
}

func TestComputeTaskFolderPath_Count1_Task1Suffix(t *testing.T) {
	result := state.ComputeTaskFolderPath("/output/{{agent_id}}", "wf-001", "main", 1)
	assert.Equal(t, "/output/main_task1", result)
}

func TestComputeTaskFolderPath_Count3_Task3Suffix(t *testing.T) {
	result := state.ComputeTaskFolderPath("/output/{{agent_id}}", "wf-001", "main", 3)
	assert.Equal(t, "/output/main_task3", result)
}

func TestComputeTaskFolderPath_WorkflowIDSubstitution(t *testing.T) {
	result := state.ComputeTaskFolderPath("/runs/{{workflow_id}}/out", "wf-abc", "worker", 0)
	assert.Equal(t, "/runs/wf-abc/out", result)
}

func TestComputeTaskFolderPath_BothSubstitutions(t *testing.T) {
	result := state.ComputeTaskFolderPath("/runs/{{workflow_id}}/{{agent_id}}", "wf-xyz", "main", 2)
	assert.Equal(t, "/runs/wf-xyz/main_task2", result)
}

func TestComputeTaskFolderPath_CustomAbsolutePattern(t *testing.T) {
	result := state.ComputeTaskFolderPath("/home/user/tasks/{{agent_id}}", "wf-001", "agent1", 0)
	assert.True(t, len(result) > 0 && result[0] == '/', "result should be an absolute path")
	assert.Equal(t, "/home/user/tasks/agent1", result)
}
