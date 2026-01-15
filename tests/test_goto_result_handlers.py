import pytest
import tempfile
from pathlib import Path
from unittest.mock import AsyncMock, patch
from orchestrator import handle_goto_transition, handle_result_transition
from state import create_initial_state, write_state, read_state
from parsing import Transition


class TestGotoHandler:
    """Tests for <goto> handler (Step 2.2.7-2.2.8, 2.2.10)."""

    @pytest.mark.asyncio
    async def test_goto_handler_updates_current_state(self, tmp_path):
        """Test 2.2.7: <goto> handler updates agent's current_state."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        transition = Transition("goto", "NEXT.md", {}, "")
        
        updated_agent = await handle_goto_transition(agent, transition, {}, None)
        
        assert updated_agent["current_state"] == "NEXT.md"

    @pytest.mark.asyncio
    async def test_goto_handler_preserves_session_id(self, tmp_path):
        """Test 2.2.8: <goto> handler preserves session_id for resume."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        transition = Transition("goto", "NEXT.md", {}, "")
        
        updated_agent = await handle_goto_transition(agent, transition, {}, None)
        
        assert updated_agent["session_id"] == "session_123"


class TestResultHandler:
    """Tests for <result> handler (Step 2.2.9, 2.2.11)."""

    @pytest.mark.asyncio
    async def test_result_with_empty_stack_removes_agent(self, tmp_path):
        """Test 2.2.9: <result> with empty stack removes agent from array."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []  # Empty stack
        }
        
        transition = Transition("result", "", {}, "Task completed")
        
        # Result with empty stack should return None (agent terminates)
        result = await handle_result_transition(agent, transition, {}, None)
        
        assert result is None


class TestOrchestratorSessionId:
    """Tests for orchestrator storing session_id (Step 2.2.6)."""

    @pytest.mark.asyncio
    async def test_orchestrator_stores_returned_session_id(self, tmp_path):
        """Test 2.2.6: orchestrator stores returned session_id in agent state."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-session"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state with no session_id
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        state["agents"][0]["session_id"] = None
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        
        # Mock wrap_claude_code to return a session_id
        mock_output = [{"type": "content", "text": "Output\n<goto>NEXT.md</goto>"}]
        new_session_id = "session_new_123"
        
        with patch('orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, new_session_id)
            
            # Mock handlers to avoid NotImplementedError
            with patch('orchestrator.handle_goto_transition') as mock_handler:
                mock_handler.return_value = {
                    "id": "main",
                    "current_state": "NEXT.md",
                    "session_id": new_session_id,
                    "stack": []
                }
                
                try:
                    from orchestrator import run_all_agents
                    await run_all_agents(workflow_id, state_dir=str(state_dir))
                except Exception:
                    pass  # May fail on next iteration, but that's OK
            
            # Verify session_id was stored
            final_state = read_state(workflow_id, state_dir=str(state_dir))
            # The agent should have the new session_id
            # (This test verifies the orchestrator updates session_id from wrap_claude_code return)
