"""Tests for CLI commands."""

import argparse
import pytest
from pathlib import Path
from unittest.mock import patch
from src.cli import cmd_start, validate_workflow_id
from src.state import generate_workflow_id, list_workflows, read_state


class TestCLIStart:
    """Tests for the start command."""

    def test_start_with_auto_generated_workflow_id(self, tmp_path):
        """Test that start command auto-generates workflow ID when not provided."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        
        initial_file = scope_dir / "START.md"
        initial_file.write_text("Test prompt")
        
        state_dir = tmp_path / ".raymond" / "state"
        
        # Create args namespace without workflow_id
        # Use --no-run to avoid actually running the orchestrator in tests
        args = argparse.Namespace(
            workflow_id=None,
            initial_file=str(initial_file),
            state_dir=str(state_dir),
            no_run=True,
            verbose=False
        )
        
        # Run start command
        result = cmd_start(args)
        
        # Should succeed
        assert result == 0
        
        # Should have created a workflow with auto-generated ID
        workflows = list_workflows(state_dir=str(state_dir))
        assert len(workflows) == 1
        
        # Generated ID should match pattern
        workflow_id = workflows[0]
        assert workflow_id.startswith("workflow_")
        assert "_" in workflow_id  # Should have timestamp separator
        
        # Verify state was created correctly
        state = read_state(workflow_id, state_dir=str(state_dir))
        assert state["workflow_id"] == workflow_id
        assert state["scope_dir"] == str(scope_dir.resolve())
        assert state["agents"][0]["current_state"] == "START.md"

    def test_start_with_provided_workflow_id(self, tmp_path):
        """Test that start command uses provided workflow ID."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        
        initial_file = scope_dir / "START.md"
        initial_file.write_text("Test prompt")
        
        state_dir = tmp_path / ".raymond" / "state"
        
        # Create args namespace with workflow_id
        # Use --no-run to avoid actually running the orchestrator in tests
        args = argparse.Namespace(
            workflow_id="my-custom-workflow",
            initial_file=str(initial_file),
            state_dir=str(state_dir),
            no_run=True,
            verbose=False
        )
        
        # Run start command
        result = cmd_start(args)
        
        # Should succeed
        assert result == 0
        
        # Should have created workflow with provided ID
        workflows = list_workflows(state_dir=str(state_dir))
        assert "my-custom-workflow" in workflows
        
        # Verify state was created correctly
        state = read_state("my-custom-workflow", state_dir=str(state_dir))
        assert state["workflow_id"] == "my-custom-workflow"
        assert state["scope_dir"] == str(scope_dir.resolve())
        assert state["agents"][0]["current_state"] == "START.md"

    def test_start_rejects_duplicate_workflow_id(self, tmp_path):
        """Test that start command rejects duplicate workflow IDs."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        
        initial_file = scope_dir / "START.md"
        initial_file.write_text("Test prompt")
        
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        # Create existing workflow
        from src.state import create_initial_state, write_state
        existing_state = create_initial_state("existing-workflow", str(scope_dir), "START.md")
        write_state("existing-workflow", existing_state, state_dir=str(state_dir))
        
        # Create args namespace with duplicate workflow_id
        # Use --no-run to avoid actually running the orchestrator in tests
        args = argparse.Namespace(
            workflow_id="existing-workflow",
            initial_file=str(initial_file),
            state_dir=str(state_dir),
            no_run=True,
            verbose=False
        )
        
        # Run start command
        result = cmd_start(args)
        
        # Should fail
        assert result == 1

    def test_start_rejects_missing_file(self, tmp_path):
        """Test that start command rejects non-existent initial file."""
        state_dir = tmp_path / ".raymond" / "state"
        
        # Create args namespace with non-existent file
        args = argparse.Namespace(
            workflow_id=None,
            initial_file=str(tmp_path / "nonexistent" / "START.md"),
            state_dir=str(state_dir),
            no_run=True,
            verbose=False
        )
        
        # Run start command
        result = cmd_start(args)
        
        # Should fail
        assert result == 1


class TestWorkflowIDGeneration:
    """Tests for workflow ID generation."""

    def test_generate_workflow_id_creates_unique_ids(self, tmp_path):
        """Test that generate_workflow_id creates unique IDs."""
        state_dir = tmp_path / ".raymond" / "state"
        
        # Generate multiple IDs with small delay to ensure different timestamps
        import time
        id1 = generate_workflow_id(state_dir=str(state_dir))
        time.sleep(0.01)  # Small delay to ensure different microseconds
        id2 = generate_workflow_id(state_dir=str(state_dir))
        
        # Should be different (microseconds should differ)
        assert id1 != id2
        
        # Should match pattern
        assert id1.startswith("workflow_")
        assert id2.startswith("workflow_")

    def test_generate_workflow_id_handles_collisions(self, tmp_path):
        """Test that generate_workflow_id handles collisions with counters."""
        from unittest.mock import patch
        from datetime import datetime
        from src.state import create_initial_state, write_state
        
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        # Use a fixed timestamp to force a collision
        fixed_timestamp = "2024-01-15_12-00-00-123456"
        colliding_id = f"workflow_{fixed_timestamp}"
        
        # Manually create a workflow file with that exact ID
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        state = create_initial_state(colliding_id, str(scope_dir), "START.md")
        write_state(colliding_id, state, state_dir=str(state_dir))
        
        # Mock datetime to return the same timestamp, forcing a collision
        with patch('src.state.datetime') as mock_datetime:
            mock_datetime.now.return_value.strftime.return_value = fixed_timestamp
            mock_datetime.now.return_value = type('obj', (object,), {
                'strftime': lambda self, fmt: fixed_timestamp
            })()
            
            # Generate new ID - should append counter since colliding_id exists
            new_id = generate_workflow_id(state_dir=str(state_dir))
        
        # Should be different from colliding_id
        assert new_id != colliding_id
        # Should have counter appended (format: base_id_counter)
        assert new_id.startswith(f"{colliding_id}_")
        # Should end with a counter
        assert new_id.endswith("_1")


class TestWorkflowIDValidation:
    """Tests for workflow ID validation."""

    def test_validate_workflow_id_accepts_valid_ids(self):
        """Test that valid workflow IDs pass validation."""
        assert validate_workflow_id("test-123") is None
        assert validate_workflow_id("workflow_001") is None
        assert validate_workflow_id("my_workflow") is None
        assert validate_workflow_id("a1b2c3") is None

    def test_validate_workflow_id_rejects_empty(self):
        """Test that empty workflow IDs are rejected."""
        error = validate_workflow_id("")
        assert error is not None
        assert "empty" in error.lower()

    def test_validate_workflow_id_rejects_invalid_characters(self):
        """Test that workflow IDs with invalid characters are rejected."""
        error = validate_workflow_id("test@workflow")
        assert error is not None
        assert "invalid" in error.lower()
        
        error = validate_workflow_id("test workflow")
        assert error is not None
        
        error = validate_workflow_id("test.workflow")
        assert error is not None

    def test_validate_workflow_id_rejects_reserved_names(self):
        """Test that reserved Windows names are rejected."""
        error = validate_workflow_id("CON")
        assert error is not None
        assert "reserved" in error.lower()
        
        error = validate_workflow_id("com1")
        assert error is not None
