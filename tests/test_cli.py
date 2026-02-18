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
            verbose=False,
            budget=None,
            no_debug=False,
            model=None,
            timeout=None,
            initial_input=None,
            dangerously_skip_permissions=False
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
            verbose=False,
            budget=None,
            no_debug=False,
            model=None,
            timeout=None,
            initial_input=None,
            dangerously_skip_permissions=False
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
            verbose=False,
            budget=None,
            no_debug=False,
            model=None,
            timeout=None,
            initial_input=None,
            dangerously_skip_permissions=False
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
            verbose=False,
            budget=None,
            no_debug=False,
            model=None,
            timeout=None,
            initial_input=None,
            dangerously_skip_permissions=False
        )

        # Run start command
        result = cmd_start(args)

        # Should fail
        assert result == 1

    def test_start_with_initial_input(self, tmp_path):
        """Test that start command passes initial_input as pending_result."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)

        initial_file = scope_dir / "START.md"
        initial_file.write_text("Process this: {{result}}")

        state_dir = tmp_path / ".raymond" / "state"

        # Create args namespace with initial_input
        args = argparse.Namespace(
            workflow_id="input-test",
            initial_file=str(initial_file),
            state_dir=str(state_dir),
            no_run=True,
            verbose=False,
            budget=None,
            no_debug=False,
            model=None,
            timeout=None,
            initial_input="hello, there",
            dangerously_skip_permissions=False
        )

        # Run start command
        result = cmd_start(args)

        # Should succeed
        assert result == 0

        # Verify state was created with pending_result
        state = read_state("input-test", state_dir=str(state_dir))
        assert state["agents"][0]["pending_result"] == "hello, there"

    def test_start_without_initial_input(self, tmp_path):
        """Test that start command without initial_input has no pending_result."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)

        initial_file = scope_dir / "START.md"
        initial_file.write_text("No input here")

        state_dir = tmp_path / ".raymond" / "state"

        # Create args namespace without initial_input
        args = argparse.Namespace(
            workflow_id="no-input-test",
            initial_file=str(initial_file),
            state_dir=str(state_dir),
            no_run=True,
            verbose=False,
            budget=None,
            no_debug=False,
            model=None,
            timeout=None,
            initial_input=None,
            dangerously_skip_permissions=False
        )

        # Run start command
        result = cmd_start(args)

        # Should succeed
        assert result == 0

        # Verify state was created without pending_result
        state = read_state("no-input-test", state_dir=str(state_dir))
        assert "pending_result" not in state["agents"][0]


class TestCLIStartDirectory:
    """Tests for the start command when given a directory path instead of a file."""

    def test_start_with_directory_containing_1_start_md(self, tmp_path):
        """Test that start command accepts a directory when 1_START.md is present."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)

        start_file = scope_dir / "1_START.md"
        start_file.write_text("Test prompt")

        state_dir = tmp_path / ".raymond" / "state"

        args = argparse.Namespace(
            workflow_id=None,
            initial_file=str(scope_dir),
            state_dir=str(state_dir),
            no_run=True,
            verbose=False,
            budget=None,
            no_debug=False,
            model=None,
            timeout=None,
            initial_input=None,
            dangerously_skip_permissions=False,
            quiet=False,
            effort=None,
        )

        result = cmd_start(args)

        assert result == 0

        workflows = list_workflows(state_dir=str(state_dir))
        assert len(workflows) == 1

        workflow_id = workflows[0]
        state = read_state(workflow_id, state_dir=str(state_dir))
        assert state["scope_dir"] == str(scope_dir.resolve())
        assert state["agents"][0]["current_state"] == "1_START.md"

    def test_start_with_directory_missing_1_start_md(self, tmp_path, capsys):
        """Test that start command gives informative error when directory lacks 1_START.md."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        # No 1_START.md created

        state_dir = tmp_path / ".raymond" / "state"

        args = argparse.Namespace(
            workflow_id=None,
            initial_file=str(scope_dir),
            state_dir=str(state_dir),
            no_run=True,
            verbose=False,
            budget=None,
            no_debug=False,
            model=None,
            timeout=None,
            initial_input=None,
            dangerously_skip_permissions=False,
            quiet=False,
            effort=None,
        )

        result = cmd_start(args)

        assert result == 1
        captured = capsys.readouterr()
        assert "1_START.md" in captured.err

    def test_start_with_nonexistent_path(self, tmp_path, capsys):
        """Test that start command gives a distinct error for a path that is neither file nor directory."""
        nonexistent = tmp_path / "does_not_exist"
        # Neither created as file nor directory

        state_dir = tmp_path / ".raymond" / "state"

        args = argparse.Namespace(
            workflow_id=None,
            initial_file=str(nonexistent),
            state_dir=str(state_dir),
            no_run=True,
            verbose=False,
            budget=None,
            no_debug=False,
            model=None,
            timeout=None,
            initial_input=None,
            dangerously_skip_permissions=False,
            quiet=False,
            effort=None,
        )

        result = cmd_start(args)

        assert result == 1
        captured = capsys.readouterr()
        # Should give a distinct message — not the generic "does not exist" file message
        assert "not a file or directory" in captured.err.lower()

    def test_start_with_directory_and_cli_options(self, tmp_path):
        """Test that start command accepts directory path alongside --workflow-id, --budget, --input, --no-run."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)

        start_file = scope_dir / "1_START.md"
        start_file.write_text("Process this: {{result}}")

        state_dir = tmp_path / ".raymond" / "state"

        args = argparse.Namespace(
            workflow_id="dir-options-test",
            initial_file=str(scope_dir),
            state_dir=str(state_dir),
            no_run=True,
            verbose=False,
            budget=5.0,
            no_debug=False,
            model=None,
            timeout=None,
            initial_input="hello from dir",
            dangerously_skip_permissions=False,
            quiet=False,
            effort=None,
        )

        result = cmd_start(args)

        assert result == 0

        state = read_state("dir-options-test", state_dir=str(state_dir))
        assert state["workflow_id"] == "dir-options-test"
        assert state["budget_usd"] == 5.0
        assert state["agents"][0]["current_state"] == "1_START.md"
        assert state["agents"][0]["pending_result"] == "hello from dir"


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
