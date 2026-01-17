import pytest
from pathlib import Path
from unittest.mock import AsyncMock, patch
from src.orchestrator import run_all_agents
from src.state import create_initial_state, write_state
from src.parsing import Transition
from src.policy import PolicyViolationError


class TestBasicOrchestratorLoop:
    """Tests for basic orchestrator loop (Step 2.1)."""

    @pytest.mark.asyncio
    async def test_run_all_agents_reads_state_file_at_start(self, tmp_path):
        """Test 2.1.1: run_all_agents() reads state file at start."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-001"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        
        # Mock wrap_claude_code to return output with a transition tag
        mock_output = [{"type": "content", "text": "Some output\n<goto>NEXT.md</goto>"}]
        
        # Create NEXT.md file to avoid FileNotFoundError on next iteration
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next prompt")
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            # First call returns goto, second call returns result to terminate
            mock_wrap.side_effect = [
                (mock_output, None),
                ([{"type": "content", "text": "Done\n<result>Complete</result>"}], None)
            ]
            
            # Should read state file and process until completion
            await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify wrap_claude_code was called (which means state was read)
            assert mock_wrap.called

    @pytest.mark.asyncio
    async def test_orchestrator_exits_when_agents_array_empty(self, tmp_path):
        """Test 2.1.2: orchestrator exits when agents array is empty."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-002"
        scope_dir = str(tmp_path / "workflows" / "test")
        
        # Create state with empty agents array
        state = {
            "workflow_id": workflow_id,
            "scope_dir": scope_dir,
            "agents": []
        }
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Orchestrator should exit immediately
        await run_all_agents(workflow_id, state_dir=str(state_dir))
        
        # If we get here without exception, it exited successfully

    @pytest.mark.asyncio
    async def test_orchestrator_calls_claude_code_wrapper(self, tmp_path):
        """Test 2.1.3: orchestrator calls Claude Code wrapper for each agent."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-003"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        
        # Mock wrap_claude_code to return output with a transition tag
        mock_output = [{"type": "content", "text": "Some output\n<goto>NEXT.md</goto>"}]
        
        # Create NEXT.md file to avoid FileNotFoundError on next iteration
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next prompt")
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            # First call returns goto, second call returns result to terminate
            mock_wrap.side_effect = [
                (mock_output, None),
                ([{"type": "content", "text": "Done\n<result>Complete</result>"}], None)
            ]
            
            await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify wrap_claude_code was called
            assert mock_wrap.called

    @pytest.mark.asyncio
    async def test_orchestrator_parses_output_and_dispatches_to_handler(self, tmp_path):
        """Test 2.1.4: orchestrator parses output and dispatches to handler."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-004"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        
        # Mock wrap_claude_code to return output with a tag
        mock_output = [{"type": "content", "text": "Some output\n<goto>NEXT.md</goto>"}]
        
        # Create NEXT.md file to avoid FileNotFoundError on next iteration
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next prompt")
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            # First call returns goto, second call returns result to terminate
            mock_wrap.side_effect = [
                (mock_output, None),
                ([{"type": "content", "text": "Done\n<result>Complete</result>"}], None)
            ]
            
            # Mock parse_transitions
            with patch('src.orchestrator.parse_transitions') as mock_parse:
                # First call returns goto, second returns result
                mock_parse.side_effect = [
                    [Transition("goto", "NEXT.md", {}, "")],
                    [Transition("result", "", {}, "Complete")]
                ]
                
                await run_all_agents(workflow_id, state_dir=str(state_dir))
                
                # Verify parse_transitions was called
                assert mock_parse.called

    @pytest.mark.asyncio
    async def test_parse_error_zero_tags_raises_exception(self, tmp_path):
        """Test 2.1.5: parse error (zero tags) raises exception."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-005"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        
        # Mock wrap_claude_code to return output with no tags
        mock_output = [{"type": "content", "text": "Some output with no tags"}]
        mock_session_id = "test-session-123"
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, mock_session_id)
            
            # Mock parse_transitions to return empty list
            with patch('src.orchestrator.parse_transitions') as mock_parse:
                mock_parse.return_value = []
                
                # Should raise an exception for zero tags
                with pytest.raises(ValueError, match="Expected exactly one transition"):
                    await run_all_agents(workflow_id, state_dir=str(state_dir))
                
                # Verify error file was created
                errors_dir = state_dir.parent / "errors"
                error_files = list(errors_dir.glob(f"{workflow_id}_main_*.txt"))
                assert len(error_files) > 0, "Error file should be created"
                
                # Verify error file contents
                error_file = error_files[0]
                error_content = error_file.read_text(encoding='utf-8')
                assert "ERROR REPORT" in error_content
                assert workflow_id in error_content
                assert "main" in error_content
                assert mock_session_id in error_content
                assert "Some output with no tags" in error_content

    @pytest.mark.asyncio
    async def test_parse_error_multiple_tags_raises_exception(self, tmp_path):
        """Test 2.1.6: parse error (multiple tags) raises exception."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-006"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create a prompt file
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")
        
        # Mock wrap_claude_code
        mock_output = [{"type": "content", "text": "Some output"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, None)
            
            # Mock parse_transitions to return multiple tags
            with patch('src.orchestrator.parse_transitions') as mock_parse:
                mock_parse.return_value = [
                    Transition("goto", "A.md", {}, ""),
                    Transition("goto", "B.md", {}, "")
                ]
                
                # Should raise an exception for multiple tags
                with pytest.raises(ValueError, match="Expected exactly one transition"):
                    await run_all_agents(workflow_id, state_dir=str(state_dir))


class TestPolicyEnforcement:
    """Tests for policy enforcement in orchestrator (Step 5.3)."""

    @pytest.mark.asyncio
    async def test_policy_violation_disallowed_tag_raises(self, tmp_path):
        """Test that policy violation for disallowed tag raises PolicyViolationError."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-policy-001"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file with policy that disallows fork
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
  - { tag: result }
---
# Start Prompt
This state only allows goto and result.""")
        
        # Mock wrap_claude_code to return output with disallowed fork tag
        mock_output = [{"type": "content", "text": "Some output\n<fork next=\"NEXT.md\">WORKER.md</fork>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, None)
            
            # Should raise PolicyViolationError
            with pytest.raises(PolicyViolationError, match="Tag 'fork' is not allowed"):
                await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify error file was created
            errors_dir = state_dir.parent / "errors"
            error_files = list(errors_dir.glob(f"{workflow_id}_main_*.txt"))
            assert len(error_files) > 0, "Error file should be created"

    @pytest.mark.asyncio
    async def test_policy_violation_disallowed_target_raises(self, tmp_path):
        """Test that policy violation for disallowed target raises PolicyViolationError."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-policy-002"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file with policy that restricts goto targets
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
---
# Start Prompt
This state only allows goto to NEXT.md.""")
        
        # Create OTHER.md file (not allowed by policy)
        other_file = Path(scope_dir) / "OTHER.md"
        other_file.write_text("Other prompt")
        
        # Mock wrap_claude_code to return output with disallowed target
        mock_output = [{"type": "content", "text": "Some output\n<goto>OTHER.md</goto>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, None)
            
            # Should raise PolicyViolationError
            with pytest.raises(PolicyViolationError, match="is not allowed"):
                await run_all_agents(workflow_id, state_dir=str(state_dir))

    @pytest.mark.asyncio
    async def test_policy_allows_valid_transition(self, tmp_path):
        """Test that valid transitions according to policy are allowed."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-policy-003"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file with policy
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
  - { tag: result }
---
# Start Prompt
This state allows goto to NEXT.md.""")
        
        # Create NEXT.md file (allowed by policy)
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next prompt")
        
        # Mock wrap_claude_code to return output with allowed transition
        mock_output = [{"type": "content", "text": "Some output\n<goto>NEXT.md</goto>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            # First call returns goto, second call returns result to terminate
            mock_wrap.side_effect = [
                (mock_output, None),
                ([{"type": "content", "text": "Done\n<result>Complete</result>"}], None)
            ]
            
            # Should complete successfully without policy violation
            await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify wrap_claude_code was called
            assert mock_wrap.called

    @pytest.mark.asyncio
    async def test_no_policy_allows_all_transitions(self, tmp_path):
        """Test that absence of policy allows all transitions."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-policy-004"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file without policy (no frontmatter)
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("# Start Prompt\n\nNo policy restrictions.")
        
        # Create NEXT.md file
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("Next prompt")
        
        # Mock wrap_claude_code to return output with any transition
        mock_output = [{"type": "content", "text": "Some output\n<goto>NEXT.md</goto>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            # First call returns goto, second call returns result to terminate
            mock_wrap.side_effect = [
                (mock_output, None),
                ([{"type": "content", "text": "Done\n<result>Complete</result>"}], None)
            ]
            
            # Should complete successfully (no policy = no restrictions)
            await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify wrap_claude_code was called
            assert mock_wrap.called
