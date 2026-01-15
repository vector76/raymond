import pytest
from pathlib import Path
from src.orchestrator import handle_fork_transition
from src.parsing import Transition


class TestForkHandler:
    """Tests for <fork> handler (Step 3.1.1-3.1.8)."""

    def test_fork_handler_creates_new_agent_in_agents_array(self, tmp_path):
        """Test 3.1.1: <fork> handler creates new agent in agents array."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        state = {
            "scope_dir": scope_dir,
            "agents": [agent]
        }
        
        transition = Transition("fork", "WORKER.md", {"next": "NEXT.md"}, "")
        
        updated_agent, new_agent = handle_fork_transition(agent, transition, state)
        
        # Verify new agent was created
        assert new_agent is not None
        assert new_agent["id"] != agent["id"]

    def test_new_agent_has_unique_id(self, tmp_path):
        """Test 3.1.2: new agent has unique ID."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        state = {
            "scope_dir": scope_dir,
            "agents": [agent]
        }
        
        transition = Transition("fork", "WORKER.md", {"next": "NEXT.md"}, "")
        
        updated_agent, new_agent = handle_fork_transition(agent, transition, state)
        
        # Verify IDs are different
        assert new_agent["id"] != agent["id"]
        assert new_agent["id"] != "main"
        
        # Verify new agent ID follows a pattern (e.g., "main_worker_1" or similar)
        assert "worker" in new_agent["id"].lower() or new_agent["id"].startswith(agent["id"])

    def test_new_agent_has_empty_return_stack(self, tmp_path):
        """Test 3.1.3: new agent has empty return stack."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        state = {
            "scope_dir": scope_dir,
            "agents": [agent]
        }
        
        transition = Transition("fork", "WORKER.md", {"next": "NEXT.md"}, "")
        
        updated_agent, new_agent = handle_fork_transition(agent, transition, state)
        
        # Verify new agent has empty stack
        assert new_agent.get("stack", []) == []

    def test_new_agent_has_session_id_none(self, tmp_path):
        """Test 3.1.4: new agent has session_id = None (fresh)."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        state = {
            "scope_dir": scope_dir,
            "agents": [agent]
        }
        
        transition = Transition("fork", "WORKER.md", {"next": "NEXT.md"}, "")
        
        updated_agent, new_agent = handle_fork_transition(agent, transition, state)
        
        # Verify new agent has fresh session
        assert new_agent.get("session_id") is None

    def test_new_agent_current_state_is_fork_target(self, tmp_path):
        """Test 3.1.5: new agent's current_state is fork target."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        state = {
            "scope_dir": scope_dir,
            "agents": [agent]
        }
        
        transition = Transition("fork", "WORKER.md", {"next": "NEXT.md"}, "")
        
        updated_agent, new_agent = handle_fork_transition(agent, transition, state)
        
        # Verify new agent's current_state is the fork target
        assert new_agent["current_state"] == "WORKER.md"

    def test_parent_agent_continues_at_next_state(self, tmp_path):
        """Test 3.1.6: parent agent continues at next state."""
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        state = {
            "scope_dir": scope_dir,
            "agents": [agent]
        }
        
        transition = Transition("fork", "WORKER.md", {"next": "NEXT.md"}, "")
        
        updated_agent, new_agent = handle_fork_transition(agent, transition, state)
        
        # Verify parent agent's current_state is updated to next
        assert updated_agent["current_state"] == "NEXT.md"
        
        # Verify parent's session_id is preserved (like goto)
        assert updated_agent["session_id"] == "session_123"
        
        # Verify parent's stack is preserved
        assert updated_agent.get("stack", []) == []

    def test_fork_attributes_available_as_template_variables(self, tmp_path):
        """Test 3.1.7: fork attributes available as template variables for new agent.
        
        This test verifies that fork attributes (beyond 'next') are stored
        and will be available as template variables when the new agent runs.
        The actual template substitution happens in step_agent.
        """
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        
        state = {
            "scope_dir": scope_dir,
            "agents": [agent]
        }
        
        # Fork with additional attributes
        transition = Transition(
            "fork", 
            "WORKER.md", 
            {"next": "NEXT.md", "item": "task_123", "priority": "high"}, 
            ""
        )
        
        updated_agent, new_agent = handle_fork_transition(agent, transition, state)
        
        # Verify fork attributes are stored in new agent for template substitution
        # These will be used when step_agent loads the worker prompt
        assert "fork_attributes" in new_agent
        assert new_agent["fork_attributes"]["item"] == "task_123"
        assert new_agent["fork_attributes"]["priority"] == "high"
        # 'next' should not be in fork_attributes (it's for parent, not worker)
        assert "next" not in new_agent["fork_attributes"]
