"""Tests for cost tracking and budget enforcement (Phase 5, Step 5.1)."""

import pytest
from pathlib import Path
from unittest.mock import AsyncMock, patch
from src.orchestrator import run_all_agents, extract_cost_from_results
from src.state import create_initial_state, write_state, read_state


class TestCostExtraction:
    """Tests for extracting cost from Claude Code responses (5.1.1)."""

    def test_extract_cost_from_results_with_total_cost_usd(self):
        """Test extracting cost when total_cost_usd is in final result object."""
        results = [
            {"type": "content", "text": "Some output"},
            {"type": "result", "total_cost_usd": 0.05}
        ]
        cost = extract_cost_from_results(results)
        assert cost == 0.05

    def test_extract_cost_from_results_missing_field(self):
        """Test extracting cost when total_cost_usd is missing (returns 0.0)."""
        results = [
            {"type": "content", "text": "Some output"}
        ]
        cost = extract_cost_from_results(results)
        assert cost == 0.0

    def test_extract_cost_from_results_multiple_objects(self):
        """Test extracting cost from last object with total_cost_usd."""
        results = [
            {"type": "content", "text": "Some output", "total_cost_usd": 0.01},
            {"type": "result", "total_cost_usd": 0.05}
        ]
        cost = extract_cost_from_results(results)
        # Should use the last one found
        assert cost == 0.05

    def test_extract_cost_from_results_nested_structure(self):
        """Test extracting cost from nested structure if needed."""
        results = [
            {"type": "content", "text": "Some output"},
            {"type": "result", "metadata": {"total_cost_usd": 0.03}}
        ]
        # For now, we'll only check top-level, but this test documents expected behavior
        cost = extract_cost_from_results(results)
        # If nested, would need to check nested structure
        # For initial implementation, we'll check top-level only
        assert cost == 0.0  # No top-level total_cost_usd


class TestCostTrackingInState:
    """Tests for cost tracking in workflow state (5.1.2)."""

    @pytest.mark.asyncio
    async def test_state_includes_total_cost_usd_field(self, tmp_path):
        """Test that state file includes total_cost_usd field, initialized to 0.0."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-cost-001"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Read back and verify
        read_state_dict = read_state(workflow_id, state_dir=str(state_dir))
        assert "total_cost_usd" in read_state_dict
        assert read_state_dict["total_cost_usd"] == 0.0

    @pytest.mark.asyncio
    async def test_state_includes_budget_usd_field(self, tmp_path):
        """Test that state file includes budget_usd field with default value."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-cost-002"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Read back and verify
        read_state_dict = read_state(workflow_id, state_dir=str(state_dir))
        assert "budget_usd" in read_state_dict
        assert read_state_dict["budget_usd"] == 10.0  # Default budget

    @pytest.mark.asyncio
    async def test_cost_accumulates_across_invocations(self, tmp_path):
        """Test that cost accumulates across multiple Claude Code invocations."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-cost-003"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt files
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next prompt\n<goto>LOOP.md</goto>")  # Don't terminate immediately
        loop_file = Path(scope_dir) / "LOOP.md"
        loop_file.write_text("Loop prompt")
        
        # Mock wrap_claude_code to return cost information
        mock_output_1 = [
            {"type": "content", "text": "Some output\n<goto>NEXT.md</goto>"},
            {"type": "result", "total_cost_usd": 0.05}
        ]
        mock_output_2 = [
            {"type": "content", "text": "More output\n<goto>LOOP.md</goto>"},
            {"type": "result", "total_cost_usd": 0.03}
        ]
        mock_output_3 = [
            {"type": "content", "text": "Done\n<result>Complete</result>"},
            {"type": "result", "total_cost_usd": 0.02}
        ]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.side_effect = [
                (mock_output_1, None),
                (mock_output_2, None),
                (mock_output_3, None)
            ]
            
            # Run workflow but check state after first invocation
            # We'll use a custom approach: patch write_state to capture state
            captured_states = []
            original_write_state = write_state
            
            def capture_write_state(wf_id, state_dict, state_dir=None):
                if wf_id == workflow_id:
                    captured_states.append(state_dict.copy())
                return original_write_state(wf_id, state_dict, state_dir)
            
            with patch('src.orchestrator.write_state', side_effect=capture_write_state):
                await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify cost was accumulated across invocations
            # Check that we captured states with increasing costs
            costs = [s.get("total_cost_usd", 0.0) for s in captured_states]
            # Costs should be non-decreasing
            assert len(costs) > 0
            # First cost should be 0.05 (from first invocation)
            assert costs[0] == 0.05
            # Later costs should be higher (accumulated)
            assert any(cost >= 0.08 for cost in costs)  # 0.05 + 0.03 = 0.08

    @pytest.mark.asyncio
    async def test_cost_tracking_with_custom_budget(self, tmp_path):
        """Test that custom budget can be set when creating state."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-cost-004"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state with custom budget
        state = create_initial_state(workflow_id, scope_dir, "START.md", budget_usd=0.50)
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Read back and verify
        read_state_dict = read_state(workflow_id, state_dir=str(state_dir))
        assert read_state_dict["budget_usd"] == 0.50


class TestBudgetEnforcement:
    """Tests for budget limit enforcement (5.1.3)."""

    @pytest.mark.asyncio
    async def test_workflow_terminates_when_budget_exceeded(self, tmp_path):
        """Test that workflow terminates when budget is exceeded."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-budget-001"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state with small budget
        state = create_initial_state(workflow_id, scope_dir, "START.md", budget_usd=0.10)
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt files
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt\n<goto>START.md</goto>")  # Loop
        
        # Mock wrap_claude_code to return cost that exceeds budget
        mock_output = [
            {"type": "content", "text": "Some output\n<goto>START.md</goto>"},
            {"type": "result", "total_cost_usd": 0.15}  # Exceeds 0.10 budget
        ]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, None)
            
            await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify workflow terminated (state file should be deleted or agents empty)
            # After budget exceeded, workflow should terminate
            try:
                final_state = read_state(workflow_id, state_dir=str(state_dir))
                # If state exists, agents should be empty
                assert len(final_state.get("agents", [])) == 0
            except FileNotFoundError:
                # State file deleted on completion - also valid
                pass

    @pytest.mark.asyncio
    async def test_transition_overridden_when_budget_exceeded(self, tmp_path):
        """Test that AI's transition is overridden when budget is exceeded."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-budget-002"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state with small budget
        state = create_initial_state(workflow_id, scope_dir, "START.md", budget_usd=0.10)
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt files
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next prompt")
        
        # Mock wrap_claude_code to return cost that exceeds budget
        # AI requests <goto>NEXT.md but budget is exceeded, so should terminate
        mock_output = [
            {"type": "content", "text": "Some output\n<goto>NEXT.md</goto>"},
            {"type": "result", "total_cost_usd": 0.15}  # Exceeds 0.10 budget
        ]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, None)
            
            await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify that NEXT.md was not reached (transition was overridden)
            # The workflow should have terminated instead of going to NEXT.md
            # We can verify this by checking that wrap_claude_code was only called once
            # (not called again for NEXT.md)
            assert mock_wrap.call_count == 1

    @pytest.mark.asyncio
    async def test_budget_not_exceeded_workflow_continues(self, tmp_path):
        """Test that workflow continues normally when budget is not exceeded."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-budget-003"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state with larger budget
        state = create_initial_state(workflow_id, scope_dir, "START.md", budget_usd=1.00)
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt files
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next prompt")
        
        # Mock wrap_claude_code to return cost within budget
        mock_output_1 = [
            {"type": "content", "text": "Some output\n<goto>NEXT.md</goto>"},
            {"type": "result", "total_cost_usd": 0.05}
        ]
        mock_output_2 = [
            {"type": "content", "text": "Done\n<result>Complete</result>"},
            {"type": "result", "total_cost_usd": 0.03}
        ]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.side_effect = [
                (mock_output_1, None),
                (mock_output_2, None)
            ]
            
            await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify workflow continued normally (both calls made)
            assert mock_wrap.call_count == 2
