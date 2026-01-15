import json
import pytest
import tempfile
import os
from pathlib import Path
from state import read_state, write_state, list_workflows, create_initial_state


class TestStateFileManagement:
    """Tests for state file management functions."""

    def test_write_state_creates_file(self, tmp_path):
        """Test that write_state() creates file with correct JSON structure."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-001"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": "workflows/test",
            "agents": [
                {
                    "id": "main",
                    "current_state": "START.md",
                    "session_id": None,
                    "stack": []
                }
            ]
        }
        
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        state_file = state_dir / f"{workflow_id}.json"
        assert state_file.exists()
        
        with open(state_file, 'r') as f:
            written_data = json.load(f)
        
        assert written_data == state

    def test_read_state_returns_dict(self, tmp_path):
        """Test that read_state() returns dict matching written state."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-002"
        expected_state = {
            "workflow_id": workflow_id,
            "scope_dir": "workflows/test",
            "agents": [
                {
                    "id": "main",
                    "current_state": "START.md",
                    "session_id": "session_123",
                    "stack": []
                }
            ]
        }
        
        state_file = state_dir / f"{workflow_id}.json"
        with open(state_file, 'w') as f:
            json.dump(expected_state, f)
        
        actual_state = read_state(workflow_id, state_dir=str(state_dir))
        assert actual_state == expected_state

    def test_read_state_raises_for_missing_file(self, tmp_path):
        """Test that read_state() raises for missing file."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "nonexistent"
        
        with pytest.raises(FileNotFoundError):
            read_state(workflow_id, state_dir=str(state_dir))

    def test_list_workflows_returns_ids(self, tmp_path):
        """Test that list_workflows() returns IDs of existing state files."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        # Create multiple state files
        for workflow_id in ["workflow-1", "workflow-2", "workflow-3"]:
            state_file = state_dir / f"{workflow_id}.json"
            with open(state_file, 'w') as f:
                json.dump({"workflow_id": workflow_id, "agents": []}, f)
        
        # Create a non-JSON file (should be ignored)
        (state_dir / "not-a-state.txt").write_text("test")
        
        workflows = list_workflows(state_dir=str(state_dir))
        
        # Should return all three workflow IDs
        assert set(workflows) == {"workflow-1", "workflow-2", "workflow-3"}

    def test_list_workflows_empty_directory(self, tmp_path):
        """Test that list_workflows() returns empty list for empty directory."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflows = list_workflows(state_dir=str(state_dir))
        assert workflows == []

    def test_list_workflows_nonexistent_directory(self, tmp_path):
        """Test that list_workflows() returns empty list for nonexistent directory."""
        state_dir = tmp_path / ".raymond" / "state"
        
        workflows = list_workflows(state_dir=str(state_dir))
        assert workflows == []


class TestCreateInitialState:
    """Tests for create_initial_state() helper function."""

    def test_create_initial_state(self):
        """Test that create_initial_state() creates correct structure."""
        workflow_id = "test-003"
        scope_dir = "workflows/test"
        initial_state = "START.md"
        
        state = create_initial_state(workflow_id, scope_dir, initial_state)
        
        assert state["workflow_id"] == workflow_id
        assert state["scope_dir"] == scope_dir
        assert len(state["agents"]) == 1
        assert state["agents"][0]["id"] == "main"
        assert state["agents"][0]["current_state"] == initial_state
        assert state["agents"][0]["session_id"] is None
        assert state["agents"][0]["stack"] == []
