import pytest
import json
from pathlib import Path
from unittest.mock import patch, AsyncMock
from orchestrator import step_agent, run_all_agents, ClaudeCodeError, PromptFileError
from cc_wrap import wrap_claude_code
from prompts import load_prompt
from state import read_state, write_state, StateFileError


class TestClaudeCodeErrorHandling:
    """Tests for Claude Code non-zero exit error handling (Step 4.1.1)."""

    @pytest.mark.asyncio
    async def test_claude_code_non_zero_exit_raises_exception(self, tmp_path):
        """Test 4.1.1: Claude Code non-zero exit raises appropriate exception."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        
        state = {
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": None,
                "stack": []
            }]
        }
        
        # Mock wrap_claude_code to raise RuntimeError (simulating non-zero exit)
        with patch('orchestrator.wrap_claude_code', new_callable=AsyncMock) as mock_wrap:
            mock_wrap.side_effect = RuntimeError(
                "Claude command failed with return code 1\nStderr: Error message"
            )
            
            # step_agent should raise ClaudeCodeError (wrapped from RuntimeError)
            with pytest.raises(ClaudeCodeError) as exc_info:
                await step_agent(state["agents"][0], state, None)
            
            assert "Claude Code execution failed" in str(exc_info.value)


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
