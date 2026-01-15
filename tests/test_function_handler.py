import pytest
from pathlib import Path
from src.orchestrator import handle_function_transition, handle_result_transition
from src.parsing import Transition


class TestFunctionHandler:
    """Tests for <function> handler (Step 2.4.1-2.4.5)."""

    def test_function_handler_pushes_frame_to_stack(self, tmp_path):
        """Test 2.4.1: <function> handler pushes frame to stack."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        transition = Transition("function", "EVAL.md", {"return": "NEXT.md"}, "")
        
        updated_agent = handle_function_transition(agent, transition, {})
        
        assert len(updated_agent["stack"]) == 1
        frame = updated_agent["stack"][0]
        assert "session" in frame
        assert "state" in frame

    def test_pushed_frame_contains_caller_session_id_and_return_state(self, tmp_path):
        """Test 2.4.2: pushed frame contains caller's session_id and return state."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        transition = Transition("function", "EVAL.md", {"return": "NEXT.md"}, "")
        
        updated_agent = handle_function_transition(agent, transition, {})
        
        frame = updated_agent["stack"][0]
        assert frame["session"] == "session_123"
        assert frame["state"] == "NEXT.md"

    def test_function_handler_sets_session_id_to_none(self, tmp_path):
        """Test 2.4.3: <function> handler sets session_id to None (fresh)."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        transition = Transition("function", "EVAL.md", {"return": "NEXT.md"}, "")
        
        updated_agent = handle_function_transition(agent, transition, {})
        
        assert updated_agent["session_id"] is None

    def test_function_handler_updates_current_state_to_function_target(self, tmp_path):
        """Test 2.4.4: <function> handler updates current_state to function target."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        transition = Transition("function", "EVAL.md", {"return": "NEXT.md"}, "")
        
        updated_agent = handle_function_transition(agent, transition, {})
        
        assert updated_agent["current_state"] == "EVAL.md"


class TestResultWithStack:
    """Tests for <result> handler with non-empty stack (Step 2.4.6-2.4.10)."""

    def test_result_with_non_empty_stack_pops_frame(self, tmp_path):
        """Test 2.4.6: <result> with non-empty stack pops frame."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "EVAL.md",
            "session_id": None,  # Function runs in fresh session
            "stack": [
                {"session": "session_caller", "state": "NEXT.md"}
            ]
        }
        
        transition = Transition("result", "", {}, "Evaluation result")
        
        updated_agent = handle_result_transition(agent, transition, {})
        
        assert updated_agent["stack"] == []

    def test_result_resumes_caller_session_id(self, tmp_path):
        """Test 2.4.7: <result> resumes caller's session_id."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        caller_session = "session_caller_123"
        agent = {
            "id": "main",
            "current_state": "EVAL.md",
            "session_id": None,
            "stack": [
                {"session": caller_session, "state": "NEXT.md"}
            ]
        }
        
        transition = Transition("result", "", {}, "Evaluation result")
        
        updated_agent = handle_result_transition(agent, transition, {})
        
        assert updated_agent["session_id"] == caller_session

    def test_result_sets_current_state_to_return_state_from_frame(self, tmp_path):
        """Test 2.4.8: <result> sets current_state to return state from frame."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        return_state = "NEXT.md"
        agent = {
            "id": "main",
            "current_state": "EVAL.md",
            "session_id": None,
            "stack": [
                {"session": "session_caller", "state": return_state}
            ]
        }
        
        transition = Transition("result", "", {}, "Evaluation result")
        
        updated_agent = handle_result_transition(agent, transition, {})
        
        assert updated_agent["current_state"] == return_state

    def test_result_payload_available_as_template_variable(self, tmp_path):
        """Test 2.4.9: <result> payload available as {{result}} variable.
        
        This test verifies that when we resume the caller, the result payload
        is stored in pending_result for template substitution in step_agent.
        """
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        result_payload = "Evaluation complete: all tests passing"
        agent = {
            "id": "main",
            "current_state": "EVAL.md",
            "session_id": None,
            "stack": [
                {"session": "session_caller", "state": "NEXT.md"}
            ]
        }
        
        transition = Transition("result", "", {}, result_payload)
        
        updated_agent = handle_result_transition(agent, transition, {})
        
        # Verify the result payload is stored in pending_result
        assert updated_agent["pending_result"] == result_payload
        assert updated_agent["current_state"] == "NEXT.md"
