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


class TestImplicitTransitions:
    """Tests for implicit transition optimization (Step 5.2)."""

    @pytest.mark.asyncio
    async def test_implicit_transition_single_goto_no_tag(self, tmp_path):
        """Test that single goto transition works without explicit tag."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-implicit-001"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file with single allowed goto transition
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
---
# Start Prompt
This state automatically transitions to NEXT.md.""")
        
        # Create NEXT.md file
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("""---
allowed_transitions:
  - { tag: result }
---
# Next Prompt
This is the next state.""")
        
        # Mock wrap_claude_code to return output WITHOUT tag (should use implicit)
        mock_output_no_tag = [{"type": "content", "text": "Some output without any tag"}]
        mock_output_result = [{"type": "content", "text": "Done\n<result>Complete</result>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            # First call returns no tag (should use implicit goto), second returns result
            mock_wrap.side_effect = [
                (mock_output_no_tag, None),
                (mock_output_result, None)
            ]
            
            # Should complete successfully using implicit transition
            await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify wrap_claude_code was called twice
            assert mock_wrap.call_count == 2

    @pytest.mark.asyncio
    async def test_implicit_transition_tag_matching_policy_accepted(self, tmp_path):
        """Test that explicit tag matching policy is accepted even when implicit is available."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-implicit-002"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file with single allowed goto transition
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
---
# Start Prompt
This state automatically transitions to NEXT.md.""")
        
        # Create NEXT.md file
        next_file = Path(scope_dir) / "NEXT.md"
        next_file.write_text("""---
allowed_transitions:
  - { tag: result }
---
# Next Prompt
This is the next state.""")
        
        # Mock wrap_claude_code to return output WITH matching tag
        mock_output_with_tag = [{"type": "content", "text": "Some output\n<goto>NEXT.md</goto>"}]
        mock_output_result = [{"type": "content", "text": "Done\n<result>Complete</result>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            # First call returns matching tag (should be accepted), second returns result
            mock_wrap.side_effect = [
                (mock_output_with_tag, None),
                (mock_output_result, None)
            ]
            
            # Should complete successfully
            await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify wrap_claude_code was called twice
            assert mock_wrap.call_count == 2

    @pytest.mark.asyncio
    async def test_implicit_transition_tag_not_matching_policy_raises(self, tmp_path):
        """Test that explicit tag not matching policy raises error even when implicit is available."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-implicit-003"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file with single allowed goto transition
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
---
# Start Prompt
This state automatically transitions to NEXT.md.""")
        
        # Create OTHER.md file (not allowed by policy)
        other_file = Path(scope_dir) / "OTHER.md"
        other_file.write_text("Other prompt")
        
        # Mock wrap_claude_code to return output with WRONG tag
        mock_output_wrong_tag = [{"type": "content", "text": "Some output\n<goto>OTHER.md</goto>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output_wrong_tag, None)
            
            # Should raise PolicyViolationError
            with pytest.raises(PolicyViolationError, match="is not allowed"):
                await run_all_agents(workflow_id, state_dir=str(state_dir))

    @pytest.mark.asyncio
    async def test_implicit_transition_result_tag_always_required(self, tmp_path):
        """Test that result tags always require explicit emission even if only one allowed."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-implicit-004"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file with single allowed result transition
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: result }
---
# Start Prompt
This state must explicitly emit result tag.""")
        
        # Mock wrap_claude_code to return output WITHOUT tag
        mock_output_no_tag = [{"type": "content", "text": "Some output without any tag"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output_no_tag, None)
            
            # Should raise ValueError (no transition found, and result can't be implicit)
            with pytest.raises(ValueError, match="Expected exactly one transition"):
                await run_all_agents(workflow_id, state_dir=str(state_dir))

    @pytest.mark.asyncio
    async def test_implicit_transition_multiple_allowed_still_requires_tag(self, tmp_path):
        """Test that multiple allowed transitions still require explicit tag."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-implicit-005"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file with multiple allowed transitions
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
  - { tag: goto, target: DONE.md }
---
# Start Prompt
This state requires explicit tag.""")
        
        # Mock wrap_claude_code to return output WITHOUT tag
        mock_output_no_tag = [{"type": "content", "text": "Some output without any tag"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output_no_tag, None)
            
            # Should raise ValueError (no transition found, multiple allowed so can't be implicit)
            with pytest.raises(ValueError, match="Expected exactly one transition"):
                await run_all_agents(workflow_id, state_dir=str(state_dir))

    @pytest.mark.asyncio
    async def test_implicit_transition_call_with_attributes(self, tmp_path):
        """Test that implicit transition works for call with return attribute."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-implicit-006"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file with single allowed call transition
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - tag: call
    target: CHILD.md
    return: RETURN.md
---
# Start Prompt
This state calls CHILD.md.""")
        
        # Create CHILD.md and RETURN.md files
        child_file = Path(scope_dir) / "CHILD.md"
        child_file.write_text("""---
allowed_transitions:
  - { tag: result }
---
# Child Prompt""")
        
        return_file = Path(scope_dir) / "RETURN.md"
        return_file.write_text("""---
allowed_transitions:
  - { tag: result }
---
# Return Prompt""")
        
        # Mock wrap_claude_code to return output WITHOUT tag (should use implicit call)
        mock_output_no_tag = [{"type": "content", "text": "Some output without any tag"}]
        mock_output_result = [{"type": "content", "text": "Done\n<result>Complete</result>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            # First call returns no tag (should use implicit call), 
            # second call (child) returns result,
            # third call (return) returns result
            mock_wrap.side_effect = [
                (mock_output_no_tag, None),
                (mock_output_result, None),
                (mock_output_result, None)
            ]
            
            # Should complete successfully using implicit transition
            await run_all_agents(workflow_id, state_dir=str(state_dir))
            
            # Verify wrap_claude_code was called three times
            assert mock_wrap.call_count == 3


class TestModelSelection:
    """Tests for model selection precedence (Step 5.3.1)."""

    @pytest.mark.asyncio
    async def test_frontmatter_model_overrides_cli_default(self, tmp_path):
        """Test 5.3.1.9: frontmatter model overrides CLI default."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-model-001"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file with model in frontmatter
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
model: haiku
allowed_transitions:
  - { tag: result }
---
# Start Prompt
This is the start.""")
        
        mock_output = [{"type": "content", "text": "Done\n<result>Complete</result>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, None)
            
            # Run with CLI default model "sonnet", but frontmatter specifies "haiku"
            await run_all_agents(workflow_id, state_dir=str(state_dir), default_model="sonnet")
            
            # Verify wrap_claude_code was called with haiku (from frontmatter), not sonnet (from CLI)
            assert mock_wrap.called
            # Check keyword arguments (call_args is a tuple of (args, kwargs))
            call_kwargs = mock_wrap.call_args.kwargs
            assert call_kwargs.get("model") == "haiku"

    @pytest.mark.asyncio
    async def test_cli_model_used_when_no_frontmatter_model(self, tmp_path):
        """Test 5.3.1.10: CLI model used when no frontmatter model."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-model-002"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file WITHOUT model in frontmatter
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: result }
---
# Start Prompt
This is the start.""")
        
        mock_output = [{"type": "content", "text": "Done\n<result>Complete</result>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, None)
            
            # Run with CLI default model "sonnet"
            await run_all_agents(workflow_id, state_dir=str(state_dir), default_model="sonnet")
            
            # Verify wrap_claude_code was called with sonnet (from CLI)
            assert mock_wrap.called
            # Check keyword arguments
            call_kwargs = mock_wrap.call_args.kwargs
            assert call_kwargs.get("model") == "sonnet"

    @pytest.mark.asyncio
    async def test_goto_transition_breaks_loop_to_refresh_state(self, tmp_path):
        """Test that after a goto transition, orchestrator breaks inner loop to re-read state.
        
        This test reproduces a bug where after a goto transition, the orchestrator
        would continue processing without breaking the inner loop, causing the agent
        to be invoked again with stale state (old current_state).
        
        The bug manifests when:
        1. Agent is in STATE_A
        2. Agent transitions via goto to STATE_B
        3. Orchestrator updates state but doesn't break inner loop
        4. If there are other agents (workers) or the loop continues, agent might
           be invoked again with stale STATE_A state
        
        Expected behavior: After goto A -> B, the orchestrator should break the
        inner loop, re-read state, and create fresh tasks. The agent should only
        be invoked once in state B, not again in state A.
        """
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-goto-refresh"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "STATE_A.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt files with distinct content so we can track which is loaded
        state_a_file = Path(scope_dir) / "STATE_A.md"
        state_a_file.write_text("STATE_A_PROMPT")
        
        state_b_file = Path(scope_dir) / "STATE_B.md"
        state_b_file.write_text("STATE_B_PROMPT")
        
        # Track which prompts are being sent (which state is active) and call order
        prompts_sent = []
        call_order = []
        
        async def mock_wrap_claude_code(prompt, **kwargs):
            prompts_sent.append(prompt)
            # Track which state file content is in the prompt
            if "STATE_A_PROMPT" in prompt:
                call_order.append("STATE_A")
            elif "STATE_B_PROMPT" in prompt:
                call_order.append("STATE_B")
            else:
                call_order.append("UNKNOWN")
            
            # First call: STATE_A -> STATE_B (goto)
            if len(prompts_sent) == 1:
                if "STATE_A_PROMPT" not in prompt:
                    raise AssertionError(f"First call should be in STATE_A, but got: {prompt[:100]}")
                return ([{"type": "content", "text": "Transitioning\n<goto>STATE_B.md</goto>"}], "session_1")
            # Second call: Should be STATE_B -> result (terminate)
            elif len(prompts_sent) == 2:
                if "STATE_B_PROMPT" not in prompt:
                    raise AssertionError(
                        f"Second call should be in STATE_B (after goto), but got STATE_A. "
                        f"This indicates stale state! Prompt: {prompt[:100]}"
                    )
                return ([{"type": "content", "text": "Done\n<result>Complete</result>"}], "session_1")
            else:
                # Should not be called more than twice
                # The bug would cause a third call with STATE_A_PROMPT (stale state)
                raise AssertionError(
                    f"Unexpected invocation #{len(prompts_sent)}. "
                    f"Call order so far: {call_order}. "
                    f"Prompt: {prompt[:100]}... "
                    f"This indicates the agent was invoked with stale state after goto transition."
                )
        
        with patch('src.orchestrator.wrap_claude_code', side_effect=mock_wrap_claude_code):
            await run_all_agents(workflow_id, state_dir=str(state_dir))
        
        # Verify exactly 2 invocations (STATE_A -> STATE_B, then STATE_B -> result)
        # The bug would cause 3+ invocations if the agent is invoked again in STATE_A
        # after the goto transition
        assert len(prompts_sent) == 2, (
            f"Expected 2 invocations, got {len(prompts_sent)}. "
            f"Call order: {call_order}. "
            f"This indicates the agent was invoked with stale state after goto transition."
        )
        
        # Verify the call order is correct: STATE_A, then STATE_B
        assert call_order == ["STATE_A", "STATE_B"], (
            f"Expected call order ['STATE_A', 'STATE_B'], but got {call_order}. "
            f"If STATE_A appears after STATE_B, that indicates stale state bug."
        )

    @pytest.mark.asyncio
    async def test_no_model_passed_when_neither_specified(self, tmp_path):
        """Test 5.3.1.11: no model passed when neither frontmatter nor CLI specify."""
        state_dir = tmp_path / ".raymond" / "state"
        state_dir.mkdir(parents=True)
        
        workflow_id = "test-model-003"
        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)
        
        # Create initial state
        state = create_initial_state(workflow_id, scope_dir, "START.md")
        write_state(workflow_id, state, state_dir=str(state_dir))
        
        # Create prompt file WITHOUT model in frontmatter
        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: result }
---
# Start Prompt
This is the start.""")
        
        mock_output = [{"type": "content", "text": "Done\n<result>Complete</result>"}]
        
        with patch('src.orchestrator.wrap_claude_code') as mock_wrap:
            mock_wrap.return_value = (mock_output, None)
            
            # Run with no CLI default model (None)
            await run_all_agents(workflow_id, state_dir=str(state_dir), default_model=None)
            
            # Verify wrap_claude_code was called with model=None (or not passed)
            assert mock_wrap.called
            # Check keyword arguments
            call_kwargs = mock_wrap.call_args.kwargs
            # Model should be None or not in kwargs
            assert call_kwargs.get("model") is None or "model" not in call_kwargs
