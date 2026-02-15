"""Tests for the transitions module.

This module tests the apply_transition wrapper function and the transition
handler dispatch table.
"""

import os

import pytest
from src.orchestrator.transitions import (
    _resolve_cd,
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


class TestResolveCd:
    """Tests for the _resolve_cd helper function."""

    def test_absolute_path_returned_as_is(self):
        assert _resolve_cd("/absolute/path", None) == "/absolute/path"

    def test_absolute_path_normalized(self):
        assert _resolve_cd("/a/b/../c/./d", None) == "/a/c/d"

    def test_relative_path_with_base_cwd(self):
        assert _resolve_cd("subdir", "/base") == "/base/subdir"

    def test_relative_dotdot_with_base_cwd(self):
        assert _resolve_cd("../sibling", "/base/child") == "/base/sibling"

    def test_relative_path_without_base_uses_orchestrator_cwd(self):
        result = _resolve_cd("subdir", None)
        expected = os.path.normpath(os.path.join(os.getcwd(), "subdir"))
        assert result == expected

    def test_complex_relative_path_normalized(self):
        assert _resolve_cd("../foo/../bar/../baz", "/repo/project") == "/repo/baz"

    def test_dot_path_resolves_to_base(self):
        assert _resolve_cd(".", "/base/dir") == "/base/dir"

    def test_absolute_path_ignores_base_cwd(self):
        assert _resolve_cd("/new/path", "/old/path") == "/new/path"


class TestResetTransitionCd:
    """Tests for cd attribute on reset transitions."""

    def test_reset_with_absolute_cd_sets_agent_cwd(self):
        """Reset with absolute cd attribute sets the agent's cwd field."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition("reset", "FRESH.md", {"cd": "/path/to/worktree"}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert result["current_state"] == "FRESH.md"
        assert result["session_id"] is None
        assert result["cwd"] == "/path/to/worktree"

    def test_reset_without_cd_does_not_set_cwd(self):
        """Reset without cd attribute does not add a cwd field."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition("reset", "FRESH.md", {}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert "cwd" not in result

    def test_reset_with_cd_preserves_existing_cwd_when_no_cd(self):
        """Reset without cd preserves the agent's existing cwd."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [],
            "cwd": "/existing/path"
        }

        transition = Transition("reset", "FRESH.md", {}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        # Existing cwd should be preserved (not cleared by reset)
        assert result["cwd"] == "/existing/path"

    def test_reset_with_cd_overrides_existing_cwd(self):
        """Reset with absolute cd attribute overrides existing cwd."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [],
            "cwd": "/old/path"
        }

        transition = Transition("reset", "FRESH.md", {"cd": "/new/path"}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert result["cwd"] == "/new/path"

    def test_reset_relative_cd_resolved_against_agent_cwd(self):
        """Relative cd is resolved against the agent's current cwd."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [],
            "cwd": "/repo/project"
        }

        transition = Transition("reset", "FRESH.md", {"cd": "../other-project"}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert result["cwd"] == "/repo/other-project"

    def test_reset_relative_cd_resolved_against_orchestrator_cwd_when_no_agent_cwd(self):
        """Relative cd is resolved against orchestrator's cwd when agent has no cwd."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition("reset", "FRESH.md", {"cd": "subdir"}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        expected = os.path.normpath(os.path.join(os.getcwd(), "subdir"))
        assert result["cwd"] == expected

    def test_reset_cd_normalizes_path(self):
        """cd paths are normalized (no redundant .. or . components)."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [],
            "cwd": "/repo/project"
        }

        transition = Transition("reset", "FRESH.md", {"cd": "../foo/../bar/../baz"}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert result["cwd"] == "/repo/baz"

    def test_reset_absolute_cd_is_normalized(self):
        """Absolute cd paths are also normalized."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition("reset", "FRESH.md", {"cd": "/a/b/../c/./d"}, "")
        state = {}

        result = apply_transition(agent, transition, state)

        assert result["cwd"] == "/a/c/d"


class TestForkTransitionCd:
    """Tests for cd attribute on fork transitions."""

    def test_fork_with_absolute_cd_sets_worker_cwd(self):
        """Fork with absolute cd attribute sets the new worker's cwd field."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition(
            "fork", "WORKER.md",
            {"next": "NEXT.md", "cd": "/path/to/worktree"},
            ""
        )
        state = {}

        parent, worker = apply_transition(agent, transition, state)

        assert worker["cwd"] == "/path/to/worktree"
        # Parent should not get the cd
        assert "cwd" not in parent

    def test_fork_without_cd_does_not_set_worker_cwd(self):
        """Fork without cd attribute does not add cwd to worker."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition(
            "fork", "WORKER.md",
            {"next": "NEXT.md"},
            ""
        )
        state = {}

        parent, worker = apply_transition(agent, transition, state)

        assert "cwd" not in worker

    def test_fork_cd_not_included_in_fork_attributes(self):
        """cd attribute is NOT passed as a fork attribute (template variable)."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition(
            "fork", "WORKER.md",
            {"next": "NEXT.md", "cd": "/path/to/worktree", "item": "task1"},
            ""
        )
        state = {}

        parent, worker = apply_transition(agent, transition, state)

        # cd should NOT be in fork_attributes
        assert "cd" not in worker.get("fork_attributes", {})
        # But other attributes should be
        assert worker["fork_attributes"]["item"] == "task1"

    def test_fork_parent_preserves_its_cwd(self):
        """Fork does not change parent's existing cwd."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [],
            "cwd": "/parent/dir"
        }

        transition = Transition(
            "fork", "WORKER.md",
            {"next": "NEXT.md", "cd": "/worker/dir"},
            ""
        )
        state = {}

        parent, worker = apply_transition(agent, transition, state)

        assert parent["cwd"] == "/parent/dir"
        assert worker["cwd"] == "/worker/dir"

    def test_fork_relative_cd_resolved_against_parent_cwd(self):
        """Relative cd on fork is resolved against the parent agent's cwd."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [],
            "cwd": "/repo"
        }

        transition = Transition(
            "fork", "WORKER.md",
            {"next": "NEXT.md", "cd": "worktrees/feature-a"},
            ""
        )
        state = {}

        parent, worker = apply_transition(agent, transition, state)

        assert worker["cwd"] == "/repo/worktrees/feature-a"
        assert parent["cwd"] == "/repo"

    def test_fork_relative_cd_resolved_against_orchestrator_cwd_when_no_parent_cwd(self):
        """Relative cd on fork uses orchestrator's cwd when parent has no cwd."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": []
        }

        transition = Transition(
            "fork", "WORKER.md",
            {"next": "NEXT.md", "cd": "worktrees/feature-a"},
            ""
        )
        state = {}

        parent, worker = apply_transition(agent, transition, state)

        expected = os.path.normpath(os.path.join(os.getcwd(), "worktrees/feature-a"))
        assert worker["cwd"] == expected

    def test_fork_cd_normalizes_path(self):
        """Fork cd paths are normalized."""
        agent = {
            "id": "main",
            "current_state": "START.md",
            "session_id": "session_123",
            "stack": [],
            "cwd": "/repo"
        }

        transition = Transition(
            "fork", "WORKER.md",
            {"next": "NEXT.md", "cd": "./a/../b/./c"},
            ""
        )
        state = {}

        parent, worker = apply_transition(agent, transition, state)

        assert worker["cwd"] == "/repo/b/c"
