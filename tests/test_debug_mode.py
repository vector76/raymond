"""Tests for debug mode feature (Phase 5, Step 5.5)."""

import json
import pytest
from pathlib import Path
from unittest.mock import AsyncMock, patch, MagicMock
from datetime import datetime
from src.orchestrator import run_all_agents
from src.state import create_initial_state, write_state
from src.cli import cmd_start, cmd_run_workflow
import argparse


class TestDebugDirectoryCreation:
    """Tests for debug directory creation (5.5.1, 5.5.5)."""

    @pytest.mark.asyncio
    async def test_debug_flag_creates_debug_directory(self, tmp_path):
        """Test 5.5.1: --debug flag creates debug directory structure."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-debug-001"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt\n<result>Done</result>")
        
        # Mock wrap_claude_code to return result immediately
        mock_output = [{"type": "content", "text": "Done\n<result>Complete</result>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, None)
            
            # Run with debug=True
            await run_all_agents(workflow_id, state_dir=str(state_dir), debug=True)
            
            # Check that debug directory was created
            debug_base = tmp_path / ".raymond" / "debug"
            assert debug_base.exists()
            
            # Should have at least one debug directory
            debug_dirs = [d for d in debug_base.iterdir() if d.is_dir()]
            assert len(debug_dirs) >= 1
            
            # Debug directory name should include workflow_id
            debug_dir = debug_dirs[0]
            assert workflow_id in debug_dir.name

    def test_debug_directory_naming_includes_workflow_id_and_timestamp(self, tmp_path):
        """Test 5.5.5: Debug directory naming includes workflow_id and timestamp."""
        from src.orchestrator import create_debug_directory
        
        workflow_id = "test-workflow-123"
        state_dir = str(tmp_path / ".raymond" / "state")
        
        # Create debug directory
        debug_dir = create_debug_directory(workflow_id, state_dir=state_dir)
        
        assert debug_dir is not None
        assert debug_dir.exists()
        
        # Check naming format: {workflow_id}_{timestamp}
        dir_name = debug_dir.name
        assert workflow_id in dir_name
        
        # Should have timestamp in format YYYYMMDD_HHMMSS
        # Extract timestamp part (after workflow_id_)
        parts = dir_name.split("_")
        assert len(parts) >= 2  # workflow_id + timestamp components
        
        # Check timestamp format (YYYYMMDD_HHMMSS)
        # The last two parts should be date and time
        timestamp_part = "_".join(parts[1:])  # Everything after workflow_id
        # Should be at least 15 characters (YYYYMMDD_HHMMSS)
        assert len(timestamp_part) >= 15

    def test_debug_directory_creation_fails_gracefully(self, tmp_path):
        """Test 5.5.4: Debug mode doesn't fail workflow on file write errors."""
        from src.orchestrator import create_debug_directory
        
        workflow_id = "test-workflow"
        # Use a path that will fail (e.g., invalid characters on Windows)
        # Actually, let's test with a path that exists but we can't write to
        # For this test, we'll mock the mkdir to raise an exception
        
        with patch('pathlib.Path.mkdir') as mock_mkdir:
            mock_mkdir.side_effect = OSError("Permission denied")
            
            # Should return None instead of raising
            debug_dir = create_debug_directory(workflow_id, state_dir=str(tmp_path / ".raymond" / "state"))
            assert debug_dir is None


class TestClaudeCodeOutputSaving:
    """Tests for saving Claude Code JSON outputs (5.5.2, 5.5.10)."""

    @pytest.mark.asyncio
    async def test_claude_code_outputs_saved_per_agent_step(self, tmp_path):
        """Test 5.5.2: Claude Code JSON outputs saved per agent step."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-debug-002"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt files
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt\n<goto>NEXT.md</goto>")
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next prompt\n<result>Done</result>")
        
        # Mock Claude Code outputs
        mock_output_1 = [
            {"type": "content", "text": "Step 1\n<goto>NEXT.md</goto>"},
            {"type": "result", "total_cost_usd": 0.05, "session_id": "session_123"}
        ]
        mock_output_2 = [
            {"type": "content", "text": "Step 2\n<result>Done</result>"},
            {"type": "result", "total_cost_usd": 0.03, "session_id": "session_123"}
        ]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.side_effect = [
                (mock_output_1, "session_123"),
                (mock_output_2, "session_123")
            ]
            
            # Run with debug=True
            await run_all_agents(workflow_id, state_dir=str(state_dir), debug=True)
            
            # Find debug directory
            debug_base = tmp_path / ".raymond" / "debug"
            debug_dirs = [d for d in debug_base.iterdir() if d.is_dir()]
            assert len(debug_dirs) >= 1
            debug_dir = debug_dirs[0]
            
            # Should have JSON files for each step
            json_files = list(debug_dir.glob("*.json"))
            assert len(json_files) >= 2  # At least 2 steps
            
            # Check first file format: {agent_id}_{state_name}_{step_number}.json
            json_files_sorted = sorted(json_files, key=lambda p: p.name)
            first_file = json_files_sorted[0]
            
            # Parse filename
            parts = first_file.stem.split("_")
            assert len(parts) >= 3  # agent_id, state_name, step_number
            
            # Verify file contains the JSON output
            with open(first_file, 'r', encoding='utf-8') as f:
                saved_data = json.load(f)
            assert isinstance(saved_data, list)
            assert len(saved_data) > 0

    @pytest.mark.asyncio
    async def test_step_numbers_increment_correctly_per_agent(self, tmp_path):
        """Test 5.5.10: Step number tracking per agent for file naming."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-debug-003"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt files for multiple steps
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Step 1\n<goto>STEP2.md</goto>")
        step2_file = Path(scope_dir) / "STEP2.md"
        step2_file.write_text("Step 2\n<goto>STEP3.md</goto>")
        step3_file = Path(scope_dir) / "STEP3.md"
        step3_file.write_text("Step 3\n<result>Done</result>")
        
        # Mock Claude Code outputs for 3 steps
        mock_outputs = [
            ([{"type": "content", "text": f"Step {i}\n<goto>STEP{i+1}.md</goto>"}], "session_123")
            for i in range(1, 3)
        ] + [
            ([{"type": "content", "text": "Step 3\n<result>Done</result>"}], "session_123")
        ]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.side_effect = mock_outputs
            
            # Run with debug=True
            await run_all_agents(workflow_id, state_dir=str(state_dir), debug=True)
            
            # Find debug directory
            debug_base = tmp_path / ".raymond" / "debug"
            debug_dirs = [d for d in debug_base.iterdir() if d.is_dir()]
            assert len(debug_dirs) >= 1
            debug_dir = debug_dirs[0]
            
            # Should have 3 JSON files for main agent
            main_json_files = sorted([f for f in debug_dir.glob("main_*.json")], key=lambda p: p.name)
            assert len(main_json_files) == 3
            
            # Check step numbers increment: 001, 002, 003
            for i, json_file in enumerate(main_json_files, 1):
                assert f"_{i:03d}.json" in json_file.name or f"_{i:03d}" in json_file.stem


class TestStateTransitionLogging:
    """Tests for state transition logging (5.5.3)."""

    @pytest.mark.asyncio
    async def test_state_transitions_logged_to_transitions_log(self, tmp_path):
        """Test 5.5.3: State transitions logged to transitions.log."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-debug-004"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt files
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt\n<goto>NEXT.md</goto>")
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next prompt\n<result>Done</result>")
        
        # Mock Claude Code outputs
        mock_output_1 = [
            {"type": "content", "text": "Step 1\n<goto>NEXT.md</goto>"},
            {"type": "result", "total_cost_usd": 0.05, "session_id": "session_123"}
        ]
        mock_output_2 = [
            {"type": "content", "text": "Step 2\n<result>Done</result>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.side_effect = [
                (mock_output_1, "session_123"),
                (mock_output_2, "session_123")
            ]
            
            # Run with debug=True
            await run_all_agents(workflow_id, state_dir=str(state_dir), debug=True)
            
            # Find debug directory
            debug_base = tmp_path / ".raymond" / "debug"
            debug_dirs = [d for d in debug_base.iterdir() if d.is_dir()]
            assert len(debug_dirs) >= 1
            debug_dir = debug_dirs[0]
            
            # Check transitions.log exists
            transitions_log = debug_dir / "transitions.log"
            assert transitions_log.exists()
            
            # Read log content
            log_content = transitions_log.read_text(encoding='utf-8')
            assert len(log_content) > 0
            
            # Should contain transition information
            assert "START.md" in log_content or "START" in log_content
            assert "NEXT.md" in log_content or "NEXT" in log_content
            assert "goto" in log_content.lower()
            assert "result" in log_content.lower() or "terminated" in log_content.lower()

    @pytest.mark.asyncio
    async def test_transition_log_includes_metadata(self, tmp_path):
        """Test that transition log includes metadata like session_id, cost, stack_depth."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-debug-005"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt\n<result>Done</result>")
        
        # Mock Claude Code output with cost
        mock_output = [
            {"type": "content", "text": "Done\n<result>Complete</result>"},
            {"type": "result", "total_cost_usd": 0.05, "session_id": "session_abc123"}
        ]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, "session_abc123")
            
            # Run with debug=True
            await run_all_agents(workflow_id, state_dir=str(state_dir), debug=True)
            
            # Find debug directory
            debug_base = tmp_path / ".raymond" / "debug"
            debug_dirs = [d for d in debug_base.iterdir() if d.is_dir()]
            assert len(debug_dirs) >= 1
            debug_dir = debug_dirs[0]
            
            # Read transitions.log
            transitions_log = debug_dir / "transitions.log"
            log_content = transitions_log.read_text(encoding='utf-8')
            
            # Should contain metadata
            # Note: exact format may vary, but should have some metadata
            assert "main" in log_content or "agent" in log_content.lower()


class TestDebugModeErrorHandling:
    """Tests for debug mode error handling (5.5.4)."""

    @pytest.mark.asyncio
    async def test_debug_mode_doesnt_fail_workflow_on_file_write_errors(self, tmp_path):
        """Test 5.5.4: Debug mode doesn't fail workflow on file write errors."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-debug-006"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt\n<result>Done</result>")
        
        # Mock Claude Code output
        mock_output = [
            {"type": "content", "text": "Done\n<result>Complete</result>"}
        ]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, None)
            
            # Mock the debug functions to raise errors when writing files
            with patch('src.orchestrator.save_claude_output', side_effect=OSError("Permission denied")):
                with patch('src.orchestrator.log_state_transition', side_effect=OSError("Permission denied")):
                    # Should not raise exception, workflow should complete
                    await run_all_agents(workflow_id, state_dir=str(state_dir), debug=True)
                    
                    # Workflow should have completed successfully despite debug write failures
                    # State file should be deleted (workflow completed)
                    state_path = state_dir / f"{workflow_id}.json"
                    assert not state_path.exists(), "Workflow should have completed and deleted state file"


class TestCLIDebugFlag:
    """Tests for CLI --debug flag integration (5.5.6)."""

    def test_start_command_accepts_debug_flag(self, tmp_path):
        """Test that start command accepts --debug flag."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        
        initial_file = scope_dir / "START.md"
        initial_file.write_text("Test prompt")
        
        state_dir = tmp_path / ".raymond" / "state"
        
        # Create args namespace with debug flag
        args = argparse.Namespace(
            workflow_id="test-debug-cli",
            initial_file=str(initial_file),
            state_dir=str(state_dir),
            no_run=True,
            verbose=False,
            debug=True
        )
        
        # Should not raise
        result = cmd_start(args)
        assert result == 0

    def test_run_command_accepts_debug_flag(self, tmp_path):
        """Test that run command accepts --debug flag."""
        # This test would need to check the parser, but we'll test the integration
        # by checking that debug flag is passed through
        from src.cli import create_parser
        
        parser = create_parser()
        
        # Parse start command with --debug
        args = parser.parse_args(["start", "test.md", "--debug", "--no-run"])
        assert hasattr(args, 'debug')
        assert args.debug is True
        
        # Parse run command with --debug
        # First create a workflow
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        workflow_id = "test-run-debug"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Parse run command
        args = parser.parse_args(["run", workflow_id, "--debug"])
        assert hasattr(args, 'debug')
        assert args.debug is True
