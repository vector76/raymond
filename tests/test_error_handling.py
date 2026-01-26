import pytest
from pathlib import Path
from unittest.mock import patch, AsyncMock
from src.orchestrator import step_agent, ClaudeCodeError, ClaudeCodeLimitError, PromptFileError
from src.state import read_state, StateFileError


class TestClaudeCodeErrorHandling:
    """Tests for Claude Code non-zero exit error handling (Step 4.1.1)."""

    @pytest.mark.asyncio
    async def test_claude_code_non_zero_exit_raises_exception(self, tmp_path):
        """Test 4.1.1: Claude Code non-zero exit raises appropriate exception."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Use test-specific state directory to avoid polluting real error files
        state_dir = str(tmp_path / ".raymond" / "state")
        
        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        
        state = {
            "workflow_id": "test-workflow",
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": None,
                "stack": []
            }]
        }
        
        # Mock wrap_claude_code_stream to raise RuntimeError (simulating non-zero exit)
        async def mock_stream_error(*args, **kwargs):
            raise RuntimeError(
                "Claude command failed with return code 1\nStderr: Error message"
            )
            yield  # Make it a generator (never reached)
        
        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream_error):
            # step_agent should raise ClaudeCodeError (wrapped from RuntimeError)
            with pytest.raises(ClaudeCodeError) as exc_info:
                await step_agent(state["agents"][0], state, state_dir=state_dir)
            
            assert "Claude Code execution failed" in str(exc_info.value)
            
            # Verify error file was created in test directory
            errors_dir = Path(state_dir).parent / "errors"
            error_files = list(errors_dir.glob("test-workflow_main_*.txt"))
            assert len(error_files) > 0, "Error file should be created in test directory"


class TestMissingPromptFileErrorHandling:
    """Tests for missing prompt file error handling (Step 4.1.2)."""

    @pytest.mark.asyncio
    async def test_missing_prompt_file_raises_exception(self, tmp_path):
        """Test 4.1.2: missing prompt file raises appropriate exception."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        state = {
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "MISSING.md",  # File doesn't exist
                "session_id": None,
                "stack": []
            }]
        }
        
        # step_agent should raise PromptFileError (wrapped from FileNotFoundError)
        with pytest.raises(PromptFileError) as exc_info:
            await step_agent(state["agents"][0], state, None)
        
        assert "MISSING.md" in str(exc_info.value) or "not found" in str(exc_info.value).lower()


class TestMalformedStateFileErrorHandling:
    """Tests for malformed state file error handling (Step 4.1.3)."""

    def test_malformed_state_file_raises_exception(self, tmp_path):
        """Test 4.1.3: malformed state file raises appropriate exception."""
        state_dir = str(tmp_path / "state")
        Path(state_dir).mkdir(parents=True)
        
        workflow_id = "test_workflow"
        state_file = Path(state_dir) / f"{workflow_id}.json"
        
        # Write invalid JSON
        state_file.write_text("{ invalid json }")
        
        # read_state should raise StateFileError (wrapped from JSONDecodeError)
        with pytest.raises(StateFileError) as exc_info:
            read_state(workflow_id, state_dir=state_dir)
        
        assert "Malformed state file" in str(exc_info.value)
    
    def test_missing_state_file_raises_exception(self, tmp_path):
        """Test that missing state file raises FileNotFoundError."""
        state_dir = str(tmp_path / "state")
        Path(state_dir).mkdir(parents=True)
        
        workflow_id = "nonexistent_workflow"
        
        # read_state should raise FileNotFoundError
        with pytest.raises(FileNotFoundError):
            read_state(workflow_id, state_dir=state_dir)


class TestClaudeCodeLimitErrorHandling:
    """Tests for Claude Code limit error handling (non-retryable)."""

    @pytest.mark.asyncio
    async def test_claude_code_limit_error_detection(self, tmp_path):
        """Test that limit error JSON response raises ClaudeCodeLimitError."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Use test-specific state directory to avoid polluting real error files
        state_dir = str(tmp_path / ".raymond" / "state")
        
        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        
        state = {
            "workflow_id": "test-workflow",
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": None,
                "stack": []
            }]
        }
        
        # Mock wrap_claude_code_stream to return limit error JSON
        async def mock_stream_limit_error(*args, **kwargs):
            # Simulate the limit error response
            yield {
                "type": "result",
                "subtype": "success",
                "is_error": True,
                "duration_ms": 1054,
                "duration_api_ms": 0,
                "num_turns": 1,
                "result": "You've hit your limit · resets 3am (America/Chicago)",
                "session_id": "69564624-0057-4b4a-8e76-4e1a4ad13338",
                "total_cost_usd": 0,
                "usage": {
                    "input_tokens": 0,
                    "cache_creation_input_tokens": 0,
                    "cache_read_input_tokens": 0,
                    "output_tokens": 0,
                    "server_tool_use": {
                        "web_search_requests": 0,
                        "web_fetch_requests": 0
                    },
                    "service_tier": "standard",
                    "cache_creation": {
                        "ephemeral_1h_input_tokens": 0,
                        "ephemeral_5m_input_tokens": 0
                    }
                },
                "modelUsage": {},
                "permission_denials": [],
                "uuid": "7dd717a2-9242-433c-a126-d11a3ed574c4"
            }
        
        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream_limit_error):
            # step_agent should raise ClaudeCodeLimitError
            with pytest.raises(ClaudeCodeLimitError) as exc_info:
                await step_agent(state["agents"][0], state, state_dir=state_dir)
            
            assert "hit your limit" in str(exc_info.value).lower()
            
            # Verify error file was created in test directory
            errors_dir = Path(state_dir).parent / "errors"
            error_files = list(errors_dir.glob("test-workflow_main_*.txt"))
            assert len(error_files) > 0, "Error file should be created in test directory"

    @pytest.mark.asyncio
    async def test_claude_code_limit_error_no_retry(self, tmp_path):
        """Test that limit errors pause agent (no retry) and allow resume."""
        from src.orchestrator import run_all_agents
        from src.state import write_state, read_state

        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        state_dir = str(tmp_path / ".raymond" / "state")
        Path(state_dir).mkdir(parents=True)

        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")

        workflow_id = "test-limit-workflow"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": None,
                "stack": []
            }]
        }

        # Write initial state
        write_state(workflow_id, state, state_dir=state_dir)

        # Track how many times the stream is called (should only be called once, not retried)
        call_count = {"count": 0}

        # Mock wrap_claude_code_stream to return limit error JSON
        async def mock_stream_limit_error(*args, **kwargs):
            call_count["count"] += 1
            yield {
                "type": "result",
                "subtype": "success",
                "is_error": True,
                "result": "You've hit your limit · resets 3am (America/Chicago)",
                "session_id": "test-session"
            }

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream_limit_error):
            # run_all_agents should handle limit error without retrying
            await run_all_agents(workflow_id, state_dir=state_dir, quiet=True)

            # Verify that wrap_claude_code_stream was only called once (no retries)
            assert call_count["count"] == 1, f"Expected 1 call (no retries), but got {call_count['count']} calls"

            # Verify state file still exists (workflow is paused, not completed)
            state_file = Path(state_dir) / f"{workflow_id}.json"
            assert state_file.exists(), "State file should exist for paused workflow"

            # Verify agent is paused (not removed)
            final_state = read_state(workflow_id, state_dir=state_dir)
            assert len(final_state["agents"]) == 1, "Agent should still be in state"
            assert final_state["agents"][0]["status"] == "paused", "Agent should have status 'paused'"
            assert "error" in final_state["agents"][0], "Agent should have error message"

            # Verify error file was created
            errors_dir = Path(state_dir).parent / "errors"
            error_files = list(errors_dir.glob(f"{workflow_id}_main_*.txt"))
            assert len(error_files) > 0, "Error file should be created for limit error"

            # Verify error file contains limit message
            error_file = error_files[0]
            error_content = error_file.read_text()
            assert "hit your limit" in error_content.lower(), "Error file should contain limit message"


class TestTimeoutPauseBehavior:
    """Tests for timeout pause/resume behavior."""

    @pytest.mark.asyncio
    async def test_timeout_after_max_retries_pauses_agent(self, tmp_path):
        """Test that agent is paused (not removed) after max timeout retries."""
        from src.orchestrator import run_all_agents, MAX_RETRIES
        from src.state import write_state, read_state
        from src.cc_wrap import ClaudeCodeTimeoutError

        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        state_dir = str(tmp_path / ".raymond" / "state")
        Path(state_dir).mkdir(parents=True)

        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")

        workflow_id = "test-timeout-workflow"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": None,
                "stack": []
            }]
        }

        # Write initial state
        write_state(workflow_id, state, state_dir=state_dir)

        # Track how many times the stream is called
        call_count = {"count": 0}

        # Mock wrap_claude_code_stream to always timeout
        async def mock_stream_timeout(*args, **kwargs):
            call_count["count"] += 1
            raise ClaudeCodeTimeoutError("Idle timeout")
            yield  # Make it a generator (never reached)

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream_timeout):
            # run_all_agents should handle timeout and pause agent after max retries
            await run_all_agents(workflow_id, state_dir=state_dir, quiet=True)

            # Verify that wrap_claude_code_stream was called MAX_RETRIES times
            assert call_count["count"] == MAX_RETRIES, f"Expected {MAX_RETRIES} calls, but got {call_count['count']}"

            # Verify state file still exists (not deleted)
            state_file = Path(state_dir) / f"{workflow_id}.json"
            assert state_file.exists(), "State file should exist for paused workflow"

            # Verify agent is paused (not removed)
            final_state = read_state(workflow_id, state_dir=state_dir)
            assert len(final_state["agents"]) == 1, "Agent should still be in state"
            assert final_state["agents"][0]["status"] == "paused", "Agent should have status 'paused'"
            assert "error" in final_state["agents"][0], "Agent should have error message"

    @pytest.mark.asyncio
    async def test_resume_resets_paused_agent(self, tmp_path):
        """Test that paused agent is reset and runs after resume."""
        from src.orchestrator import run_all_agents
        from src.state import write_state, read_state

        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        state_dir = str(tmp_path / ".raymond" / "state")
        Path(state_dir).mkdir(parents=True)

        # Create a prompt file that will complete (via result transition)
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")

        workflow_id = "test-resume-workflow"
        # Create a state with a paused agent
        state = {
            "workflow_id": workflow_id,
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": "previous-session-123",  # Preserved from before pause
                "stack": [],
                "status": "paused",
                "retry_count": 3,
                "error": "Claude Code idle timeout"
            }]
        }

        # Write initial state (simulating a paused workflow)
        write_state(workflow_id, state, state_dir=state_dir)

        # Mock wrap_claude_code_stream to succeed with a result transition
        async def mock_stream_success(*args, **kwargs):
            yield {
                "type": "result",
                "subtype": "success",
                "result": "<result>Done</result>",
                "session_id": "new-session-456"
            }

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream_success):
            # Resume the workflow
            await run_all_agents(workflow_id, state_dir=state_dir, quiet=True)

            # Verify state file is deleted (workflow completed)
            state_file = Path(state_dir) / f"{workflow_id}.json"
            assert not state_file.exists(), "State file should be deleted after completion"

    @pytest.mark.asyncio
    async def test_non_timeout_error_still_fails(self, tmp_path):
        """Test that non-timeout errors still fail/remove agent after max retries."""
        from src.orchestrator import run_all_agents, MAX_RETRIES
        from src.state import write_state

        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        state_dir = str(tmp_path / ".raymond" / "state")
        Path(state_dir).mkdir(parents=True)

        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")

        workflow_id = "test-nontimeout-workflow"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": None,
                "stack": []
            }]
        }

        # Write initial state
        write_state(workflow_id, state, state_dir=state_dir)

        # Track how many times the stream is called
        call_count = {"count": 0}

        # Mock wrap_claude_code_stream to fail with a non-timeout error
        async def mock_stream_error(*args, **kwargs):
            call_count["count"] += 1
            raise RuntimeError("Claude command failed with return code 1")
            yield  # Make it a generator (never reached)

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream_error):
            # run_all_agents should handle error and remove agent after max retries
            await run_all_agents(workflow_id, state_dir=state_dir, quiet=True)

            # Verify that wrap_claude_code_stream was called MAX_RETRIES times
            assert call_count["count"] == MAX_RETRIES, f"Expected {MAX_RETRIES} calls, but got {call_count['count']}"

            # Verify state file is deleted (agent was removed, not paused)
            state_file = Path(state_dir) / f"{workflow_id}.json"
            assert not state_file.exists(), "State file should be deleted after agent failure"
