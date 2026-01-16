import pytest
from pathlib import Path
from src.orchestrator import handle_call_transition
from src.parsing import Transition


class TestCallHandler:
    """Tests for <call> handler (Step 2.5.1-2.5.4)."""

    def test_call_handler_pushes_frame_to_stack(self, tmp_path):
        """Test 2.5.1: <call> handler pushes frame to stack (like function)."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        transition = Transition("call", "CHILD.md", {"return": "NEXT.md"}, "")
        
        updated_agent = handle_call_transition(agent, transition, {})
        
        assert len(updated_agent["stack"]) == 1
        frame = updated_agent["stack"][0]
        assert "session" in frame
        assert "state" in frame

    def test_call_handler_uses_fork_flag(self, tmp_path):
        """Test 2.5.2: <call> handler uses Claude Code --fork-session to branch context from caller.
        
        This test verifies that the agent state includes fork_session_id indicating that
        the next Claude Code invocation should use --fork-session. The actual --fork-session flag
        will be passed when step_agent invokes wrap_claude_code.
        """
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        caller_session = "session_caller_123"
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": caller_session,
            "stack": []
        }
        
        transition = Transition("call", "CHILD.md", {"return": "NEXT.md"}, "")
        
        updated_agent = handle_call_transition(agent, transition, {})
        
        # Verify the frame contains the caller's session for branching
        frame = updated_agent["stack"][0]
        assert frame["session"] == caller_session
        
        # Verify fork_session_id is set to trigger --fork-session in next step_agent invocation
        assert updated_agent["fork_session_id"] == caller_session

    def test_call_handler_updates_current_state_to_callee_target(self, tmp_path):
        """Test 2.5.3: <call> handler updates current_state to callee target."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        transition = Transition("call", "CHILD.md", {"return": "NEXT.md"}, "")
        
        updated_agent = handle_call_transition(agent, transition, {})
        
        assert updated_agent["current_state"] == "CHILD.md"
