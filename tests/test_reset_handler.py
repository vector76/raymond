import pytest
import logging
from pathlib import Path
from src.orchestrator import handle_reset_transition
from src.parsing import Transition


class TestResetHandler:
    """Tests for <reset> handler (Step 2.3.1-2.3.5)."""

    def test_reset_handler_updates_current_state(self, tmp_path):
        """Test 2.3.1: <reset> handler updates current_state."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        transition = Transition("reset", "NEXT.md", {}, "")
        
        updated_agent = handle_reset_transition(agent, transition, {})
        
        assert updated_agent["current_state"] == "NEXT.md"

    def test_reset_handler_sets_session_id_to_none(self, tmp_path):
        """Test 2.3.2: <reset> handler sets session_id to None (fresh start)."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        transition = Transition("reset", "NEXT.md", {}, "")
        
        updated_agent = handle_reset_transition(agent, transition, {})
        
        assert updated_agent["session_id"] is None

    def test_reset_handler_clears_return_stack(self, tmp_path):
        """Test 2.3.3: <reset> handler clears return stack."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [
                {"session": "session_caller", "state": "RETURN.md"}
            ]
        }
        
        transition = Transition("reset", "NEXT.md", {}, "")
        
        updated_agent = handle_reset_transition(agent, transition, {})
        
        assert updated_agent["stack"] == []

    def test_reset_with_non_empty_stack_logs_warning(self, tmp_path, caplog):
        """Test 2.3.4: <reset> with non-empty stack logs warning."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [
                {"session": "session_caller", "state": "RETURN.md"}
            ]
        }
        
        transition = Transition("reset", "NEXT.md", {}, "")
        
        with caplog.at_level(logging.WARNING):
            updated_agent = handle_reset_transition(agent, transition, {})
        
        # Verify warning was logged
        assert len(caplog.records) > 0
        assert any("non-empty return stack" in record.message.lower() 
                  or "return stack" in record.message.lower()
                  for record in caplog.records)
