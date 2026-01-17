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
        
        # Verify new agent ID follows the pattern: {parent_id}_{state_abbrev}{counter}
        # First fork to WORKER.md should be main_worker1
        assert new_agent["id"] == "main_worker1"
        assert new_agent["id"].startswith(agent["id"])
        assert "worker" in new_agent["id"]

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
    
    def test_fork_counters_persist_across_multiple_forks(self, tmp_path):
        """Test that fork counters persist and increment correctly for multiple forks."""
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
        
        # First fork
        updated_agent, new_agent_1 = handle_fork_transition(agent, transition, state)
        assert new_agent_1["id"] == "main_worker1"
        assert state.get("fork_counters", {}).get("main") == 1
        
        # Add first worker to agents array (simulating what orchestrator does)
        state["agents"].append(new_agent_1)
        
        # Second fork
        updated_agent, new_agent_2 = handle_fork_transition(updated_agent, transition, state)
        assert new_agent_2["id"] == "main_worker2"
        assert state.get("fork_counters", {}).get("main") == 2
        
        # Third fork
        updated_agent, new_agent_3 = handle_fork_transition(updated_agent, transition, state)
        assert new_agent_3["id"] == "main_worker3"
        assert state.get("fork_counters", {}).get("main") == 3
        
        # Verify all IDs are unique
        agent_ids = {new_agent_1["id"], new_agent_2["id"], new_agent_3["id"]}
        assert len(agent_ids) == 3
    
    def test_fork_counters_work_after_worker_termination(self, tmp_path):
        """Test that fork counters continue incrementing even after workers terminate.
        
        This ensures that agent names remain unique even if previous workers
        have been removed from the agents array.
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
            "agents": [agent],
            "fork_counters": {"main": 2}  # Simulate previous forks
        }
        
        transition = Transition("fork", "WORKER.md", {"next": "NEXT.md"}, "")
        
        # Fork should use counter 3, not reuse 1 or 2
        updated_agent, new_agent = handle_fork_transition(agent, transition, state)
        assert new_agent["id"] == "main_worker3"
        assert state.get("fork_counters", {}).get("main") == 3
    
    def test_nested_forks_use_hierarchical_naming(self, tmp_path):
        """Test that nested forks create hierarchical names with state-based abbreviations."""
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
        
        # First fork: main -> WORKER.md
        transition1 = Transition("fork", "WORKER.md", {"next": "NEXT.md"}, "")
        updated_agent, worker_agent = handle_fork_transition(agent, transition1, state)
        assert worker_agent["id"] == "main_worker1"
        state["agents"].append(worker_agent)
        
        # Second fork: main_worker1 -> ANALYZE.md (nested)
        transition2 = Transition("fork", "ANALYZE.md", {"next": "CONTINUE.md"}, "")
        updated_worker, analyze_agent = handle_fork_transition(worker_agent, transition2, state)
        assert analyze_agent["id"] == "main_worker1_analyz1"
        
        # Third fork: main_worker1_analyz1 -> PROCESS.md (deeply nested)
        transition3 = Transition("fork", "PROCESS.md", {"next": "DONE.md"}, "")
        updated_analyze, process_agent = handle_fork_transition(analyze_agent, transition3, state)
        assert process_agent["id"] == "main_worker1_analyz1_proces1"
        
        # Verify state names are truncated to 6 characters
        assert len("analyz") == 6
        assert len("proces") == 6
        
        # Verify different states create different abbreviations
        # Note: counter is 2 because we already forked once from main (to WORKER.md)
        transition4 = Transition("fork", "DISPATCH.md", {"next": "NEXT.md"}, "")
        updated_agent, dispatch_agent = handle_fork_transition(agent, transition4, state)
        assert dispatch_agent["id"] == "main_dispat2"  # "dispatch" truncated to 6 chars, counter is 2
