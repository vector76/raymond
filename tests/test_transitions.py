"""Tests for the transitions module.

This module tests the apply_transition wrapper function and the transition
handler dispatch table.
"""

import pytest
from src.orchestrator.transitions import (
    apply_transition,
    handle_goto_transition,
    handle_reset_transition,
    handle_function_transition,
    handle_call_transition,
    handle_fork_transition,
    handle_result_transition,
)
from src.parsing import Transition


class TestApplyTransitionWrapper:
    """Tests for the apply_transition wrapper function."""

    def test_apply_transition_deep_copies_agent(self):
        """Verify apply_transition doesn't mutate the original agent."""
        original_agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }
        # Keep a copy of the original for comparison
        original_copy = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition("goto", "NEXT.md", {}, "")
        state = {}

        result = apply_transition(original_agent, transition, state)

        # Original should not be modified
        assert original_agent == original_copy
        # Result should have the update
        assert result["current_state"] == "NEXT.md"

    def test_apply_transition_clears_pending_result(self):
        """Verify apply_transition clears pending_result field."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [],
            "pending_result": "previous result"
        }

        transition = Transition("goto", "NEXT.md", {}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert "pending_result" not in result

    def test_apply_transition_clears_fork_session_id(self):
        """Verify apply_transition clears fork_session_id field."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [],
            "fork_session_id": "forked_session"
        }

        transition = Transition("goto", "NEXT.md", {}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert "fork_session_id" not in result

    def test_apply_transition_clears_fork_attributes(self):
        """Verify apply_transition clears fork_attributes field."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [],
            "fork_attributes": {"item": "task_123"}
        }

        transition = Transition("goto", "NEXT.md", {}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert "fork_attributes" not in result

    def test_apply_transition_dispatches_to_goto_handler(self):
        """Verify apply_transition dispatches goto to correct handler."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition("goto", "NEXT.md", {}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert result["current_state"] == "NEXT.md"
        assert result["session_id"] == "session_123"  # preserved

    def test_apply_transition_dispatches_to_reset_handler(self):
        """Verify apply_transition dispatches reset to correct handler."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [{"session": "old", "state": "OLD.md"}]
        }

        transition = Transition("reset", "FRESH.md", {}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert result["current_state"] == "FRESH.md"
        assert result["session_id"] is None  # cleared
        assert result["stack"] == []  # cleared

    def test_apply_transition_dispatches_to_function_handler(self):
        """Verify apply_transition dispatches function to correct handler."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition("function", "EVAL.md", {"return": "NEXT.md"}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert result["current_state"] == "EVAL.md"
        assert result["session_id"] is None  # fresh for function
        assert len(result["stack"]) == 1
        assert result["stack"][0] == {"session": "session_123", "state": "NEXT.md"}

    def test_apply_transition_dispatches_to_call_handler(self):
        """Verify apply_transition dispatches call to correct handler."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition("call", "CHILD.md", {"return": "NEXT.md"}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert result["current_state"] == "CHILD.md"
        assert result["fork_session_id"] == "session_123"  # set for --fork-session
        assert len(result["stack"]) == 1

    def test_apply_transition_dispatches_to_fork_handler(self):
        """Verify apply_transition dispatches fork to correct handler."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition("fork", "WORKER.md", {"next": "NEXT.md"}, "")
        state = {}

        updated_parent, new_worker = apply_transition(agent, transition, state)

        assert updated_parent["current_state"] == "NEXT.md"
        assert new_worker["current_state"] == "WORKER.md"
        assert new_worker["id"] == "main_worker1"

    def test_apply_transition_dispatches_to_result_handler_termination(self):
        """Verify apply_transition dispatches result with empty stack to termination."""
        agent = {
            "id": "main",
            "current_state": "END.md",
            "session_id": "session_123",
            "stack": []  # Empty - terminates
        }

        transition = Transition("result", "", {}, "Task completed")
        state = {}

        result = apply_transition(agent, transition, state)

        assert result is None  # Agent terminated

    def test_apply_transition_dispatches_to_result_handler_return(self):
        """Verify apply_transition dispatches result with stack to return."""
        agent = {
            "id": "main",
            "current_state": "EVAL.md",
            "session_id": None,
            "stack": [{"session": "caller_session", "state": "RETURN.md"}]
        }

        transition = Transition("result", "", {}, "evaluation result")
        state = {}

        result = apply_transition(agent, transition, state)

        assert result["current_state"] == "RETURN.md"
        assert result["session_id"] == "caller_session"  # restored
        assert result["pending_result"] == "evaluation result"
        assert len(result["stack"]) == 0  # popped

    def test_apply_transition_raises_for_unknown_tag(self):
        """Verify apply_transition raises ValueError for unknown transition tag."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition("unknown_tag", "SOMEWHERE.md", {}, "")
        state = {}

        with pytest.raises(ValueError, match="Unknown transition tag: unknown_tag"):
            apply_transition(agent, transition, state)


class TestApplyTransitionTransientFieldClearing:
    """Tests specifically for transient field clearing behavior."""

    def test_multiple_transient_fields_cleared(self):
        """Verify all transient fields are cleared together."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [],
            "pending_result": "old result",
            "fork_session_id": "old fork session",
            "fork_attributes": {"item": "old item"}
        }

        transition = Transition("goto", "NEXT.md", {}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert "pending_result" not in result
        assert "fork_session_id" not in result
        assert "fork_attributes" not in result

    def test_transient_fields_cleared_before_handler_runs(self):
        """Verify transient fields are cleared before handler, so handler can set them fresh."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [{"session": "old_session", "state": "OLD.md"}],
            "pending_result": "old result"  # This will be cleared, then set by result handler
        }

        transition = Transition("result", "", {}, "new result")
        state = {}

        result = apply_transition(agent, transition, state)

        # pending_result should be the NEW value set by result handler
        assert result["pending_result"] == "new result"

    def test_call_handler_sets_fork_session_id_fresh(self):
        """Verify call handler can set fork_session_id even after clearing."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "new_session",
            "stack": [],
            "fork_session_id": "old_fork_session"  # Should be cleared then set fresh
        }

        transition = Transition("call", "CHILD.md", {"return": "NEXT.md"}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        # fork_session_id should be the caller's session (new_session), not old_fork_session
        assert result["fork_session_id"] == "new_session"
