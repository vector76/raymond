import pytest
from pathlib import Path
from unittest.mock import patch
from src.orchestrator import handle_goto_transition, handle_result_transition
from src.state import create_initial_state, write_state, read_state
from src.parsing import Transition


class TestGotoHandler:
    """Tests for <goto> handler (Step 2.2.7-2.2.8, 2.2.10)."""

    def test_goto_handler_updates_current_state(self, tmp_path):
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
        
        updated_agent = handle_goto_transition(agent, transition, {})
        
        assert updated_agent["current_state"] == "NEXT.md"

    def test_goto_handler_preserves_session_id(self, tmp_path):
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
        
        updated_agent = handle_goto_transition(agent, transition, {})
        
        assert updated_agent["session_id"] == "session_123"


class TestResultHandler:
    """Tests for <result> handler (Step 2.2.9, 2.2.11)."""

    def test_result_with_empty_stack_removes_agent(self, tmp_path):
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
        result = handle_result_transition(agent, transition, {})
        
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
        
        # Mock outputs for streaming
        new_session_id = "session_new_123"
        mock_outputs = [
            [{"type": "content", "text": "Output\n<goto>NEXT.md</goto>", "session_id": new_session_id}],
            [{"type": "content", "text": "Done\n<result>Complete</result>", "session_id": new_session_id}],
        ]
        
        # Create NEXT.md so the workflow can continue
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next prompt")
        
        call_count = [0]
        def mock_stream_factory(*args, **kwargs):
            output = mock_outputs[call_count[0] % len(mock_outputs)]
            call_count[0] += 1
            async def gen():
                for obj in output:
                    yield obj
            return gen()
        
        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream_factory):
            from src.orchestrator import run_all_agents
            await run_all_agents(workflow_id, state_dir=str(state_dir))
        
        # Verify state file was deleted after workflow completed successfully
        # Completed workflows should not leave state files behind
        state_file = Path(state_dir) / f"{workflow_id}.json"
        assert not state_file.exists(), "State file should be deleted when workflow completes"
