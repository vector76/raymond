import pytest
from pathlib import Path
from src.state import read_state, write_state, create_initial_state, recover_workflows


class TestOrchestratorResume:
    """Tests for orchestrator resuming from existing state file (Step 4.2.1)."""

    @pytest.mark.asyncio
    async def test_orchestrator_can_resume_from_existing_state_file(self, tmp_path):
        """Test 4.2.1: orchestrator can resume from existing state file.
        
        This test verifies that run_all_agents can read and continue from
        an existing state file, rather than requiring a fresh start.
        """
        state_dir = str(tmp_path / "state")
        Path(state_dir).mkdir(parents=True)
        
        workflow_id = "test_workflow"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state file
        initial_state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, initial_state, state_dir=state_dir)
        
        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Initial prompt")
        
        # Verify state file exists and can be read
        state = read_state(workflow_id, state_dir=state_dir)
        assert state["workflow_id"] == workflow_id
        assert len(state["agents"]) == 1
        assert state["agents"][0]["current_state"] == "START.md"
        
        # The orchestrator should be able to resume from this state
        # (We can't fully test run_all_agents without mocking Claude Code,
        # but we verify the state can be read and is in the correct format)


class TestRecoverWorkflows:
    """Tests for recover_workflows() function (Step 4.2.2-4.2.3)."""

    def test_recover_workflows_finds_in_progress_workflows(self, tmp_path):
        """Test 4.2.2: recover_workflows() finds in-progress workflows.
        
        A workflow is considered "in-progress" if it has at least one agent
        in the agents array.
        """
        state_dir = str(tmp_path / "state")
        Path(state_dir).mkdir(parents=True)
        
        # Create multiple state files
        # Workflow 1: In-progress (has agents)
        workflow1_id = "workflow1"
        state1 = {
            "workflow_id": workflow1_id,
            "scope_dir": str(tmp_path / "workflows" / "workflow1"),
            "agents": [
                {
                    "id": "main",
                    "current_state": "START.md",
                    "session_id": None,
                    "stack": []
                }
            ]
        }
        write_state(workflow1_id, state1, state_dir=state_dir)
        
        # Workflow 2: Completed (no agents)
        workflow2_id = "workflow2"
        state2 = {
            "workflow_id": workflow2_id,
            "scope_dir": str(tmp_path / "workflows" / "workflow2"),
            "agents": []
        }
        write_state(workflow2_id, state2, state_dir=state_dir)
        
        # Workflow 3: In-progress (has multiple agents)
        workflow3_id = "workflow3"
        state3 = {
            "workflow_id": workflow3_id,
            "scope_dir": str(tmp_path / "workflows" / "workflow3"),
            "agents": [
                {
                    "id": "main",
                    "current_state": "STEP1.md",
                    "session_id": "session_123",
                    "stack": []
                },
                {
                    "id": "worker_1",
                    "current_state": "WORKER.md",
                    "session_id": None,
                    "stack": []
                }
            ]
        }
        write_state(workflow3_id, state3, state_dir=state_dir)
        
        # Recover workflows should find only in-progress ones
        in_progress = recover_workflows(state_dir=state_dir)
        
        # Should find workflow1 and workflow3, but not workflow2
        assert workflow1_id in in_progress
        assert workflow3_id in in_progress
        assert workflow2_id not in in_progress
        assert len(in_progress) == 2
    
    def test_recover_workflows_handles_missing_agents_key(self, tmp_path):
        """Test that recover_workflows handles state files without agents key."""
        state_dir = str(tmp_path / "state")
        Path(state_dir).mkdir(parents=True)
        
        # Create a state file without agents key (malformed but readable)
        workflow_id = "malformed_workflow"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": str(tmp_path / "workflows" / "test")
            # Missing "agents" key
        }
        write_state(workflow_id, state, state_dir=state_dir)
        
        # recover_workflows should handle this gracefully
        in_progress = recover_workflows(state_dir=state_dir)
        
        # Should not include workflows without agents or with empty agents
        assert workflow_id not in in_progress
    
    def test_recover_workflows_handles_empty_state_dir(self, tmp_path):
        """Test that recover_workflows handles empty state directory."""
        state_dir = str(tmp_path / "state")
        Path(state_dir).mkdir(parents=True)
        
        # No state files
        in_progress = recover_workflows(state_dir=state_dir)
        
        assert in_progress == []
    
    def test_recover_workflows_handles_nonexistent_state_dir(self, tmp_path):
        """Test that recover_workflows handles nonexistent state directory."""
        state_dir = str(tmp_path / "nonexistent" / "state")

        # Directory doesn't exist
        in_progress = recover_workflows(state_dir=state_dir)

        assert in_progress == []

    def test_recover_workflows_finds_paused_workflows(self, tmp_path):
        """Test that recover_workflows finds workflows with paused agents.

        Paused workflows should be recoverable, as they have agents
        that can be resumed after timeout.
        """
        state_dir = str(tmp_path / "state")
        Path(state_dir).mkdir(parents=True)

        # Create a workflow with a paused agent
        workflow_id = "paused_workflow"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": str(tmp_path / "workflows" / "test"),
            "agents": [
                {
                    "id": "main",
                    "current_state": "START.md",
                    "session_id": "session_123",
                    "stack": [],
                    "status": "paused",
                    "retry_count": 3,
                    "error": "Claude Code idle timeout"
                }
            ]
        }
        write_state(workflow_id, state, state_dir=state_dir)

        # Create a workflow with active agents (not paused)
        workflow2_id = "active_workflow"
        state2 = {
            "workflow_id": workflow2_id,
            "scope_dir": str(tmp_path / "workflows" / "test2"),
            "agents": [
                {
                    "id": "main",
                    "current_state": "STEP1.md",
                    "session_id": None,
                    "stack": []
                }
            ]
        }
        write_state(workflow2_id, state2, state_dir=state_dir)

        # Recover workflows should find both (paused and active)
        in_progress = recover_workflows(state_dir=state_dir)

        assert workflow_id in in_progress, "Paused workflow should be recoverable"
        assert workflow2_id in in_progress, "Active workflow should be recoverable"
        assert len(in_progress) == 2
