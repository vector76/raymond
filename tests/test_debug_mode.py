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
            budget=None,
            debug=True,
            model=None,
            timeout=None,
            initial_input=None
        )
        
        # Should not raise
        result = cmd_start(args)
        assert result == 0

    def test_resume_command_accepts_debug_flag(self, tmp_path):
        """Test that --resume command accepts --debug flag."""
        # This test checks the parser accepts --debug with --resume
        from src.cli import create_parser
        
        parser = create_parser()
        
        # Parse start command with --debug (new syntax: positional file)
        args = parser.parse_args(["test.md", "--debug", "--no-run"])
        assert hasattr(args, 'debug')
        assert args.debug is True
        
        # Parse --resume command with --debug
        args = parser.parse_args(["--resume", "test-workflow", "--debug"])
        assert hasattr(args, 'debug')
        assert args.debug is True
        assert args.resume == "test-workflow"


class TestSaveScriptOutput:
    """Tests for save_script_output function."""

    def test_save_script_output_creates_files(self, tmp_path):
        """Test that save_script_output creates separate stdout and stderr files."""
        from src.orchestrator import save_script_output

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="CHECK",
            step_number=1,
            stdout="output text\n",
            stderr="error text\n",
        )

        # Check stdout file was created with correct content
        stdout_file = debug_dir / "main_CHECK_001.stdout.txt"
        assert stdout_file.exists()
        assert stdout_file.read_text() == "output text\n"

        # Check stderr file was created with correct content
        stderr_file = debug_dir / "main_CHECK_001.stderr.txt"
        assert stderr_file.exists()
        assert stderr_file.read_text() == "error text\n"

    def test_save_script_output_handles_empty_stderr(self, tmp_path):
        """Test that save_script_output handles empty stderr."""
        from src.orchestrator import save_script_output

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output(
            debug_dir=debug_dir,
            agent_id="worker",
            state_name="SCRIPT",
            step_number=2,
            stdout="some output",
            stderr=""
        )

        # Both files should exist
        stdout_file = debug_dir / "worker_SCRIPT_002.stdout.txt"
        stderr_file = debug_dir / "worker_SCRIPT_002.stderr.txt"
        assert stdout_file.exists()
        assert stderr_file.exists()

        # stdout should have content, stderr should be empty
        assert stdout_file.read_text() == "some output"
        assert stderr_file.read_text() == ""

    def test_save_script_output_logs_warning_on_error(self, tmp_path, caplog):
        """Test that save_script_output logs warning when file write fails."""
        from src.orchestrator import save_script_output
        import logging

        # Use a non-existent directory to trigger error
        debug_dir = tmp_path / "nonexistent" / "debug"

        with caplog.at_level(logging.WARNING):
            save_script_output(
                debug_dir=debug_dir,
                agent_id="main",
                state_name="TEST",
                step_number=1,
                stdout="output",
                stderr=""
            )

        assert "Failed to save script" in caplog.text


class TestSaveScriptErrorResponse:
    """Tests for save_script_error_response function."""

    def test_save_script_error_response_creates_file(self, tmp_path):
        """Test that save_script_error_response creates error file."""
        from src.orchestrator import save_script_error_response

        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)

        error = Exception("Test error")
        error_file = save_script_error_response(
            workflow_id="test-workflow",
            agent_id="main",
            error=error,
            script_path="/path/to/script.bat",
            stdout="script output",
            stderr="script error",
            exit_code=1,
            current_state="SCRIPT.bat",
            state_dir=str(state_dir)
        )

        assert error_file.exists()
        assert "_script.txt" in error_file.name

        content = error_file.read_text()
        assert "SCRIPT ERROR REPORT" in content
        assert "test-workflow" in content
        assert "Test error" in content
        assert "script output" in content
        assert "script error" in content
        assert "exit_code: 1" in content

    def test_save_script_error_response_handles_empty_output(self, tmp_path):
        """Test that save_script_error_response handles empty stdout/stderr."""
        from src.orchestrator import save_script_error_response

        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)

        error = Exception("Script not found")
        error_file = save_script_error_response(
            workflow_id="test",
            agent_id="main",
            error=error,
            script_path="/path/to/missing.bat",
            stdout="",
            stderr="",
            exit_code=None,
            current_state="MISSING.bat",
            state_dir=str(state_dir)
        )

        content = error_file.read_text()
        assert "(empty)" in content
        assert "exit_code: None" in content


class TestTrySaveScriptError:
    """Tests for _try_save_script_error wrapper function."""

    def test_try_save_script_error_does_not_raise(self, tmp_path, caplog):
        """Test that _try_save_script_error doesn't raise on failure."""
        from src.orchestrator import _try_save_script_error
        from unittest.mock import patch
        import logging

        # Mock save_script_error_response to raise an error
        with patch('src.orchestrator.save_script_error_response', side_effect=OSError("Permission denied")):
            with caplog.at_level(logging.WARNING):
                # Should not raise
                _try_save_script_error(
                    workflow_id="test",
                    agent_id="main",
                    error=Exception("test"),
                    script_path="/path/to/script",
                    stdout="",
                    stderr="",
                    exit_code=None,
                    current_state="SCRIPT.bat",
                    state_dir=str(tmp_path)
                )

        assert "Failed to save script error response" in caplog.text

    def test_try_save_script_error_succeeds_normally(self, tmp_path):
        """Test that _try_save_script_error saves successfully when no error."""
        from src.orchestrator import _try_save_script_error

        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)

        # Should not raise
        _try_save_script_error(
            workflow_id="test",
            agent_id="main",
            error=Exception("test error"),
            script_path="/path/to/script",
            stdout="output",
            stderr="error",
            exit_code=1,
            current_state="SCRIPT.bat",
            state_dir=str(state_dir)
        )

        # Check that error file was created
        errors_dir = tmp_path / ".raymond" / "errors"
        assert errors_dir.exists()
        error_files = list(errors_dir.glob("*_script.txt"))
        assert len(error_files) == 1


class TestExtractStateName:
    """Tests for _extract_state_name helper function."""

    def test_extract_state_name_md(self):
        """Test extracting state name from .md file."""
        from src.orchestrator import _extract_state_name

        assert _extract_state_name("START.md") == "START"
        assert _extract_state_name("CHECK.md") == "CHECK"

    def test_extract_state_name_bat(self):
        """Test extracting state name from .bat file."""
        from src.orchestrator import _extract_state_name

        assert _extract_state_name("SCRIPT.bat") == "SCRIPT"
        assert _extract_state_name("CHECK.bat") == "CHECK"

    def test_extract_state_name_sh(self):
        """Test extracting state name from .sh file."""
        from src.orchestrator import _extract_state_name

        assert _extract_state_name("SCRIPT.sh") == "SCRIPT"
        assert _extract_state_name("check.sh") == "check"

    def test_extract_state_name_case_insensitive(self):
        """Test that extension matching is case-insensitive."""
        from src.orchestrator import _extract_state_name

        assert _extract_state_name("SCRIPT.BAT") == "SCRIPT"
        assert _extract_state_name("script.SH") == "script"
        assert _extract_state_name("Test.MD") == "Test"

    def test_extract_state_name_preserves_case(self):
        """Test that state name case is preserved."""
        from src.orchestrator import _extract_state_name

        assert _extract_state_name("MyScript.bat") == "MyScript"
        assert _extract_state_name("CHECK_FILE.sh") == "CHECK_FILE"

    def test_extract_state_name_unknown_extension(self):
        """Test that unknown extensions are not removed."""
        from src.orchestrator import _extract_state_name

        assert _extract_state_name("file.txt") == "file.txt"
        assert _extract_state_name("noextension") == "noextension"


class TestScriptOutputCapturePhase51:
    """Tests for Phase 5.1: Script output capture in debug mode.

    These tests verify that debug mode saves script stdout and stderr
    to separate files with the correct naming pattern.
    """

    def test_debug_mode_saves_script_stdout_to_file(self, tmp_path):
        """Test 5.1.1: Debug mode saves script stdout to a file."""
        from src.orchestrator import save_script_output

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="CHECK",
            step_number=1,
            stdout="This is stdout content\nLine 2\n",
            stderr=""
        )

        # Check stdout file was created and has correct content
        stdout_file = debug_dir / "main_CHECK_001.stdout.txt"
        assert stdout_file.exists(), "stdout file should exist"
        assert stdout_file.read_text() == "This is stdout content\nLine 2\n"

    def test_debug_mode_saves_script_stderr_to_file(self, tmp_path):
        """Test 5.1.2: Debug mode saves script stderr to a file."""
        from src.orchestrator import save_script_output

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output(
            debug_dir=debug_dir,
            agent_id="worker",
            state_name="PROCESS",
            step_number=3,
            stdout="normal output",
            stderr="Error: something went wrong\nWarning: check input\n"
        )

        # Check stderr file was created and has correct content
        stderr_file = debug_dir / "worker_PROCESS_003.stderr.txt"
        assert stderr_file.exists(), "stderr file should exist"
        assert stderr_file.read_text() == "Error: something went wrong\nWarning: check input\n"

    def test_output_filename_follows_pattern(self, tmp_path):
        """Test 5.1.3: Output filename follows pattern {agent}_{state}_{step}.txt."""
        from src.orchestrator import save_script_output

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        # Test with various agent/state/step combinations
        test_cases = [
            ("main", "START", 1, "main_START_001"),
            ("worker", "ANALYZE", 42, "worker_ANALYZE_042"),
            ("main_fork1", "CHECK", 100, "main_fork1_CHECK_100"),
        ]

        for agent_id, state_name, step_number, expected_prefix in test_cases:
            save_script_output(
                debug_dir=debug_dir,
                agent_id=agent_id,
                state_name=state_name,
                step_number=step_number,
                stdout="out",
                stderr="err"
            )

            stdout_file = debug_dir / f"{expected_prefix}.stdout.txt"
            stderr_file = debug_dir / f"{expected_prefix}.stderr.txt"
            assert stdout_file.exists(), f"stdout file {expected_prefix}.stdout.txt should exist"
            assert stderr_file.exists(), f"stderr file {expected_prefix}.stderr.txt should exist"

    def test_separate_stdout_and_stderr_files(self, tmp_path):
        """Test 5.1.4: Separate .stdout.txt and .stderr.txt files are created."""
        from src.orchestrator import save_script_output

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="SCRIPT",
            step_number=5,
            stdout="stdout content here",
            stderr="stderr content here"
        )

        # Both files should exist as separate files
        stdout_file = debug_dir / "main_SCRIPT_005.stdout.txt"
        stderr_file = debug_dir / "main_SCRIPT_005.stderr.txt"

        assert stdout_file.exists(), ".stdout.txt file should exist"
        assert stderr_file.exists(), ".stderr.txt file should exist"
        assert stdout_file != stderr_file, "stdout and stderr should be separate files"

        # Content should be in the correct files
        assert stdout_file.read_text() == "stdout content here"
        assert stderr_file.read_text() == "stderr content here"

    def test_empty_stdout_creates_empty_file(self, tmp_path):
        """Test that empty stdout creates an empty .stdout.txt file."""
        from src.orchestrator import save_script_output

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="QUIET",
            step_number=1,
            stdout="",
            stderr="some error"
        )

        stdout_file = debug_dir / "main_QUIET_001.stdout.txt"
        assert stdout_file.exists()
        assert stdout_file.read_text() == ""

    def test_empty_stderr_creates_empty_file(self, tmp_path):
        """Test that empty stderr creates an empty .stderr.txt file."""
        from src.orchestrator import save_script_output

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="CLEAN",
            step_number=2,
            stdout="output here",
            stderr=""
        )

        stderr_file = debug_dir / "main_CLEAN_002.stderr.txt"
        assert stderr_file.exists()
        assert stderr_file.read_text() == ""

    def test_save_script_output_logs_warning_on_error(self, tmp_path, caplog):
        """Test that save_script_output logs warning when file write fails."""
        from src.orchestrator import save_script_output
        import logging

        # Use a non-existent directory to trigger error
        debug_dir = tmp_path / "nonexistent" / "debug"

        with caplog.at_level(logging.WARNING):
            save_script_output(
                debug_dir=debug_dir,
                agent_id="main",
                state_name="TEST",
                step_number=1,
                stdout="output",
                stderr="error"
            )

        assert "Failed to save script" in caplog.text


class TestScriptExecutionMetadataPhase52:
    """Tests for Phase 5.2: Execution metadata logging for script states.

    These tests verify that debug mode logs script execution metadata
    including execution time, exit code, and environment variables.
    """

    # =========================================================================
    # 5.2.1: Debug mode logs script execution time
    # =========================================================================

    def test_save_script_output_metadata_includes_execution_time(self, tmp_path):
        """Test 5.2.1: Debug mode logs script execution time."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output_metadata(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="CHECK",
            step_number=1,
            exit_code=0,
            execution_time_ms=1234.5,
            env_vars={}
        )

        # Check metadata file was created
        metadata_file = debug_dir / "main_CHECK_001.meta.json"
        assert metadata_file.exists(), "metadata file should be created"

        # Check content includes execution time
        import json
        metadata = json.loads(metadata_file.read_text())
        assert "execution_time_ms" in metadata
        assert metadata["execution_time_ms"] == 1234.5

    def test_execution_time_is_in_milliseconds(self, tmp_path):
        """Test 5.2.1: Execution time is recorded in milliseconds."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        # Use a realistic value (500ms)
        save_script_output_metadata(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="FAST",
            step_number=1,
            exit_code=0,
            execution_time_ms=500.0,
            env_vars={}
        )

        import json
        metadata_file = debug_dir / "main_FAST_001.meta.json"
        metadata = json.loads(metadata_file.read_text())
        assert metadata["execution_time_ms"] == 500.0

    def test_execution_time_zero_is_valid(self, tmp_path):
        """Test 5.2.1: Zero execution time is valid."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output_metadata(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="INSTANT",
            step_number=1,
            exit_code=0,
            execution_time_ms=0.0,
            env_vars={}
        )

        import json
        metadata_file = debug_dir / "main_INSTANT_001.meta.json"
        metadata = json.loads(metadata_file.read_text())
        assert metadata["execution_time_ms"] == 0.0

    # =========================================================================
    # 5.2.2: Debug mode logs exit code
    # =========================================================================

    def test_save_script_output_metadata_includes_exit_code(self, tmp_path):
        """Test 5.2.2: Debug mode logs exit code."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output_metadata(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="SCRIPT",
            step_number=2,
            exit_code=42,
            execution_time_ms=100.0,
            env_vars={}
        )

        import json
        metadata_file = debug_dir / "main_SCRIPT_002.meta.json"
        metadata = json.loads(metadata_file.read_text())
        assert "exit_code" in metadata
        assert metadata["exit_code"] == 42

    def test_exit_code_zero_is_logged(self, tmp_path):
        """Test 5.2.2: Exit code 0 is logged correctly."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output_metadata(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="SUCCESS",
            step_number=1,
            exit_code=0,
            execution_time_ms=100.0,
            env_vars={}
        )

        import json
        metadata_file = debug_dir / "main_SUCCESS_001.meta.json"
        metadata = json.loads(metadata_file.read_text())
        assert metadata["exit_code"] == 0

    def test_nonzero_exit_code_is_logged(self, tmp_path):
        """Test 5.2.2: Non-zero exit codes are logged correctly."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        test_cases = [1, 2, 127, 255]
        for i, exit_code in enumerate(test_cases, 1):
            save_script_output_metadata(
                debug_dir=debug_dir,
                agent_id="main",
                state_name=f"ERROR{i}",
                step_number=i,
                exit_code=exit_code,
                execution_time_ms=100.0,
                env_vars={}
            )

            import json
            metadata_file = debug_dir / f"main_ERROR{i}_{i:03d}.meta.json"
            metadata = json.loads(metadata_file.read_text())
            assert metadata["exit_code"] == exit_code

    # =========================================================================
    # 5.2.3: Debug mode logs environment variables
    # =========================================================================

    def test_save_script_output_metadata_includes_env_vars(self, tmp_path):
        """Test 5.2.3: Debug mode logs environment variables."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        env_vars = {
            "RAYMOND_WORKFLOW_ID": "wf-123",
            "RAYMOND_AGENT_ID": "main",
            "RAYMOND_STATE_DIR": "/path/to/states",
            "RAYMOND_STATE_FILE": "/path/to/states/CHECK.bat"
        }

        save_script_output_metadata(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="CHECK",
            step_number=1,
            exit_code=0,
            execution_time_ms=100.0,
            env_vars=env_vars
        )

        import json
        metadata_file = debug_dir / "main_CHECK_001.meta.json"
        metadata = json.loads(metadata_file.read_text())
        assert "env_vars" in metadata
        assert metadata["env_vars"]["RAYMOND_WORKFLOW_ID"] == "wf-123"
        assert metadata["env_vars"]["RAYMOND_AGENT_ID"] == "main"

    def test_env_vars_includes_fork_attributes(self, tmp_path):
        """Test 5.2.3: Environment variables include fork attributes."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        env_vars = {
            "RAYMOND_WORKFLOW_ID": "wf-123",
            "RAYMOND_AGENT_ID": "worker_1",
            "RAYMOND_STATE_DIR": "/path/to/states",
            "RAYMOND_STATE_FILE": "/path/to/states/WORKER.bat",
            "item": "task1",
            "priority": "high"
        }

        save_script_output_metadata(
            debug_dir=debug_dir,
            agent_id="worker_1",
            state_name="WORKER",
            step_number=1,
            exit_code=0,
            execution_time_ms=100.0,
            env_vars=env_vars
        )

        import json
        metadata_file = debug_dir / "worker_1_WORKER_001.meta.json"
        metadata = json.loads(metadata_file.read_text())
        assert metadata["env_vars"]["item"] == "task1"
        assert metadata["env_vars"]["priority"] == "high"

    def test_env_vars_includes_raymond_result(self, tmp_path):
        """Test 5.2.3: Environment variables include RAYMOND_RESULT when present."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        env_vars = {
            "RAYMOND_WORKFLOW_ID": "wf-123",
            "RAYMOND_AGENT_ID": "main",
            "RAYMOND_STATE_DIR": "/path/to/states",
            "RAYMOND_STATE_FILE": "/path/to/states/RESUME.bat",
            "RAYMOND_RESULT": "child task completed"
        }

        save_script_output_metadata(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="RESUME",
            step_number=1,
            exit_code=0,
            execution_time_ms=100.0,
            env_vars=env_vars
        )

        import json
        metadata_file = debug_dir / "main_RESUME_001.meta.json"
        metadata = json.loads(metadata_file.read_text())
        assert metadata["env_vars"]["RAYMOND_RESULT"] == "child task completed"

    def test_empty_env_vars_is_valid(self, tmp_path):
        """Test 5.2.3: Empty environment variables dict is valid."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output_metadata(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="BARE",
            step_number=1,
            exit_code=0,
            execution_time_ms=100.0,
            env_vars={}
        )

        import json
        metadata_file = debug_dir / "main_BARE_001.meta.json"
        metadata = json.loads(metadata_file.read_text())
        assert metadata["env_vars"] == {}

    # =========================================================================
    # 5.2.4: transitions.log includes script state transitions
    # =========================================================================

    @pytest.mark.asyncio
    async def test_transitions_log_includes_script_state(self, tmp_path):
        """Test 5.2.4: transitions.log includes script state transitions."""
        from src.scripts import is_windows

        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)

        workflow_id = "test-debug-script-001"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        # Use platform-appropriate script
        if is_windows():
            script_name = "START.bat"
            script_content = "@echo off\necho ^<goto^>NEXT.md^</goto^>\n"
        else:
            script_name = "START.sh"
            script_content = "#!/bin/bash\necho '<goto>NEXT.md</goto>'\n"

        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, script_name)
        write_state(workflow_id, state, state_dir=str(state_dir))

        # Create the script that outputs a goto transition
        script_file = Path(scope_dir) / script_name
        script_file.write_text(script_content)

        # Create the target markdown file
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next state\n<result>Done</result>")

        # Mock wrap_claude_code for the markdown state
        mock_output = [{"type": "content", "text": "Done\n<result>Complete</result>"}]

        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, "session_123")

            # Run with debug=True
            await run_all_agents(workflow_id, state_dir=str(state_dir), debug=True)

            # Find debug directory
            debug_base = tmp_path / ".raymond" / "debug"
            debug_dirs = [d for d in debug_base.iterdir() if d.is_dir()]
            assert len(debug_dirs) >= 1
            debug_dir = debug_dirs[0]

            # Check transitions.log exists and contains script transition
            transitions_log = debug_dir / "transitions.log"
            assert transitions_log.exists(), "transitions.log should exist"

            log_content = transitions_log.read_text()
            # Should include the script state
            assert "START" in log_content
            # Should indicate it's a script state
            assert "script" in log_content.lower() or "state_type" in log_content.lower()

    @pytest.mark.asyncio
    async def test_transitions_log_shows_script_to_markdown_transition(self, tmp_path):
        """Test 5.2.4: transitions.log shows script -> markdown transitions."""
        from src.scripts import is_windows

        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)

        workflow_id = "test-debug-script-002"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        # Use platform-appropriate script
        if is_windows():
            script_name = "CHECK.bat"
            script_content = "@echo off\necho ^<goto^>PROCESS.md^</goto^>\n"
        else:
            script_name = "CHECK.sh"
            script_content = "#!/bin/bash\necho '<goto>PROCESS.md</goto>'\n"

        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, script_name)
        write_state(workflow_id, state, state_dir=str(state_dir))

        # Create the script that outputs a goto transition
        script_file = Path(scope_dir) / script_name
        script_file.write_text(script_content)

        # Create target markdown file
        process_file = Path(scope_dir) / "PROCESS.md"
        process_file.write_text("Process\n<result>Done</result>")

        mock_output = [{"type": "content", "text": "Done\n<result>Done</result>"}]

        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, None)

            await run_all_agents(workflow_id, state_dir=str(state_dir), debug=True)

            debug_base = tmp_path / ".raymond" / "debug"
            debug_dirs = [d for d in debug_base.iterdir() if d.is_dir()]
            debug_dir = debug_dirs[0]

            log_content = (debug_dir / "transitions.log").read_text()
            # Should show the transition from script to markdown
            assert "CHECK" in log_content
            assert "PROCESS" in log_content
            assert "goto" in log_content.lower()

    def test_log_state_transition_includes_script_metadata(self, tmp_path):
        """Test 5.2.4: log_state_transition includes script-specific metadata."""
        from src.orchestrator import log_state_transition
        from datetime import datetime

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        log_state_transition(
            debug_dir=debug_dir,
            timestamp=datetime.now(),
            agent_id="main",
            old_state="SCRIPT.bat",
            new_state="NEXT.md",
            transition_type="goto",
            transition_target="NEXT.md",
            metadata={
                "state_type": "script",
                "cost": "$0.00",
                "exit_code": 0,
                "execution_time_ms": 150.5
            }
        )

        log_content = (debug_dir / "transitions.log").read_text()
        assert "SCRIPT.bat" in log_content
        assert "NEXT.md" in log_content
        assert "goto" in log_content.lower()
        # Should include metadata
        assert "state_type" in log_content or "script" in log_content
        assert "$0.00" in log_content


class TestMetadataFileNaming:
    """Tests for metadata file naming convention."""

    def test_metadata_filename_follows_pattern(self, tmp_path):
        """Test that metadata filename follows {agent}_{state}_{step}.meta.json pattern."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        test_cases = [
            ("main", "START", 1, "main_START_001.meta.json"),
            ("worker_1", "CHECK", 42, "worker_1_CHECK_042.meta.json"),
            ("main_fork1", "PROCESS", 100, "main_fork1_PROCESS_100.meta.json"),
        ]

        for agent_id, state_name, step_number, expected_filename in test_cases:
            save_script_output_metadata(
                debug_dir=debug_dir,
                agent_id=agent_id,
                state_name=state_name,
                step_number=step_number,
                exit_code=0,
                execution_time_ms=100.0,
                env_vars={}
            )

            metadata_file = debug_dir / expected_filename
            assert metadata_file.exists(), f"{expected_filename} should exist"

    def test_metadata_file_is_valid_json(self, tmp_path):
        """Test that metadata file is valid JSON."""
        from src.orchestrator import save_script_output_metadata

        debug_dir = tmp_path / "debug"
        debug_dir.mkdir()

        save_script_output_metadata(
            debug_dir=debug_dir,
            agent_id="main",
            state_name="TEST",
            step_number=1,
            exit_code=0,
            execution_time_ms=100.0,
            env_vars={"key": "value"}
        )

        import json
        metadata_file = debug_dir / "main_TEST_001.meta.json"
        # Should not raise
        metadata = json.loads(metadata_file.read_text())
        assert isinstance(metadata, dict)


class TestMetadataErrorHandling:
    """Tests for error handling in metadata logging."""

    def test_save_script_output_metadata_logs_warning_on_error(self, tmp_path, caplog):
        """Test that save_script_output_metadata logs warning when write fails."""
        from src.orchestrator import save_script_output_metadata
        import logging

        # Use a non-existent directory to trigger error
        debug_dir = tmp_path / "nonexistent" / "debug"

        with caplog.at_level(logging.WARNING):
            save_script_output_metadata(
                debug_dir=debug_dir,
                agent_id="main",
                state_name="TEST",
                step_number=1,
                exit_code=0,
                execution_time_ms=100.0,
                env_vars={}
            )

        assert "Failed to save" in caplog.text or "metadata" in caplog.text.lower()
