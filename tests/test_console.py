"""Tests for console output feature."""

import pytest
import sys
import os
from pathlib import Path
from unittest.mock import patch, MagicMock
from io import StringIO

from src.console import ConsoleReporter


class TestConsoleReporter:
    """Tests for ConsoleReporter class."""

    def test_workflow_started_displays_info(self, capsys):
        """Test workflow_started displays workflow information."""
        reporter = ConsoleReporter(quiet=False)
        reporter.workflow_started("test-workflow", "/path/to/workflow", None)
        
        captured = capsys.readouterr()
        assert "Workflow: test-workflow" in captured.out
        assert "Scope: /path/to/workflow" in captured.out

    def test_workflow_started_with_debug_dir(self, capsys):
        """Test workflow_started displays debug directory when provided."""
        reporter = ConsoleReporter(quiet=False)
        debug_path = Path("/path/to/debug")
        reporter.workflow_started("test-workflow", "/path/to/workflow", debug_path)
        
        captured = capsys.readouterr()
        assert "Debug:" in captured.out
        assert str(debug_path) in captured.out or "debug" in captured.out

    def test_state_started_displays_state_header(self, capsys):
        """Test state_started displays agent and state name."""
        reporter = ConsoleReporter(quiet=False)
        reporter.state_started("main", "START.md")
        
        captured = capsys.readouterr()
        assert "[main] START.md" in captured.out

    def test_progress_message_displays_with_prefix(self, capsys):
        """Test progress_message displays with tree prefix."""
        reporter = ConsoleReporter(quiet=False)
        reporter.progress_message("main", "I'll begin the story...")
        
        captured = capsys.readouterr()
        assert "├─" in captured.out or "|-" in captured.out
        assert "I'll begin the story" in captured.out

    def test_progress_message_not_shown_in_quiet_mode(self, capsys):
        """Test progress_message is suppressed in quiet mode."""
        reporter = ConsoleReporter(quiet=True)
        reporter.progress_message("main", "I'll begin the story...")
        
        captured = capsys.readouterr()
        assert "I'll begin the story" not in captured.out

    def test_tool_invocation_displays_tool_name(self, capsys):
        """Test tool_invocation displays tool name."""
        reporter = ConsoleReporter(quiet=False)
        reporter.tool_invocation("main", "Write", "story.txt")
        
        captured = capsys.readouterr()
        assert "[Write]" in captured.out
        assert "story.txt" in captured.out

    def test_tool_invocation_without_detail(self, capsys):
        """Test tool_invocation displays tool name without detail."""
        reporter = ConsoleReporter(quiet=False)
        reporter.tool_invocation("main", "Bash")
        
        captured = capsys.readouterr()
        assert "[Bash]" in captured.out

    def test_tool_invocation_not_shown_in_quiet_mode(self, capsys):
        """Test tool_invocation is suppressed in quiet mode."""
        reporter = ConsoleReporter(quiet=True)
        reporter.tool_invocation("main", "Write", "story.txt")
        
        captured = capsys.readouterr()
        assert "[Write]" not in captured.out

    def test_tool_error_displays_error_message(self, capsys):
        """Test tool_error displays error with ! prefix."""
        reporter = ConsoleReporter(quiet=False)
        reporter.tool_error("main", "File not found", "Read")
        
        captured = capsys.readouterr()
        assert "!" in captured.out
        assert "File not found" in captured.out
        assert "[Read]" in captured.out

    def test_tool_error_without_tool_name(self, capsys):
        """Test tool_error displays error without tool name if not available."""
        reporter = ConsoleReporter(quiet=False)
        reporter.tool_error("main", "Unknown error")
        
        captured = capsys.readouterr()
        assert "!" in captured.out
        assert "Unknown error" in captured.out

    def test_state_completed_displays_cost(self, capsys):
        """Test state_completed displays cost information."""
        reporter = ConsoleReporter(quiet=False)
        reporter.state_completed("main", 0.0353, 0.0353)
        
        captured = capsys.readouterr()
        assert "Done" in captured.out
        assert "$0.0353" in captured.out
        assert "total: $0.0353" in captured.out

    def test_state_completed_shown_in_quiet_mode(self, capsys):
        """Test state_completed is shown even in quiet mode."""
        reporter = ConsoleReporter(quiet=True)
        reporter.state_completed("main", 0.0353, 0.0353)
        
        captured = capsys.readouterr()
        assert "Done" in captured.out
        assert "$0.0353" in captured.out

    def test_transition_displays_arrow(self, capsys):
        """Test transition displays with arrow symbol."""
        reporter = ConsoleReporter(quiet=False)
        reporter.transition("main", "NEXT.md", "goto")

        captured = capsys.readouterr()
        assert "→" in captured.out or "->" in captured.out
        assert "NEXT.md" in captured.out
        assert "goto" in captured.out

    def test_transition_fork_displays_fork_symbol(self, capsys):
        """Test fork transition displays with fork symbol."""
        reporter = ConsoleReporter(quiet=False)
        reporter.transition("main", "WORKER.md", "fork", "main_worker1")

        captured = capsys.readouterr()
        assert "⑂" in captured.out or "++" in captured.out
        assert "WORKER.md" in captured.out
        assert "main_worker1" in captured.out

    def test_transition_reset_displays_label(self, capsys):
        """Test reset transition displays type label and target."""
        reporter = ConsoleReporter(quiet=False)
        reporter.transition("main", "TARGET.md", "reset")

        captured = capsys.readouterr()
        assert "reset" in captured.out
        assert "TARGET.md" in captured.out

    def test_transition_call_displays_label(self, capsys):
        """Test call transition displays type label and target."""
        reporter = ConsoleReporter(quiet=False)
        reporter.transition("main", "TARGET.md", "call")

        captured = capsys.readouterr()
        assert "call" in captured.out
        assert "TARGET.md" in captured.out

    def test_transition_function_displays_label(self, capsys):
        """Test function transition displays type label and target."""
        reporter = ConsoleReporter(quiet=False)
        reporter.transition("main", "TARGET.md", "function")

        captured = capsys.readouterr()
        assert "function" in captured.out
        assert "TARGET.md" in captured.out

    def test_transition_fork_does_not_display_fork_label(self, capsys):
        """Test fork transition uses fork symbol, not a 'fork' type label prefix."""
        reporter = ConsoleReporter(quiet=False)
        reporter.transition("main", "WORKER.md", "fork", "worker_1")

        captured = capsys.readouterr()
        assert "⑂" in captured.out or "++" in captured.out
        # The word "fork" should not appear as a label before the target
        lines = [l for l in captured.out.splitlines() if "WORKER.md" in l]
        assert lines, "Expected a line containing WORKER.md"
        assert not any(line.lstrip().startswith("fork") for line in lines)

    def test_agent_terminated_displays_result(self, capsys):
        """Test agent_terminated displays result with ⇒ symbol."""
        reporter = ConsoleReporter(quiet=False)
        reporter.agent_terminated("main", "Story complete")
        
        captured = capsys.readouterr()
        assert "⇒" in captured.out or "=>" in captured.out
        assert "Result:" in captured.out
        assert "Story complete" in captured.out

    def test_error_displays_with_exclamation(self, capsys):
        """Test error displays with ! prefix."""
        reporter = ConsoleReporter(quiet=False)
        reporter.error("main", "No transition tag - retrying (1/3)")
        
        captured = capsys.readouterr()
        assert "!" in captured.out
        assert "No transition tag" in captured.out

    def test_error_shown_in_quiet_mode(self, capsys):
        """Test error is shown even in quiet mode."""
        reporter = ConsoleReporter(quiet=True)
        reporter.error("main", "Budget exceeded")
        
        captured = capsys.readouterr()
        assert "!" in captured.out
        assert "Budget exceeded" in captured.out

    def test_workflow_completed_displays_total_cost(self, capsys):
        """Test workflow_completed displays total cost."""
        reporter = ConsoleReporter(quiet=False)
        reporter.workflow_completed(0.1430)

        captured = capsys.readouterr()
        assert "Workflow completed" in captured.out
        assert "$0.1430" in captured.out

    def test_agent_paused_displays_notification(self, capsys):
        """Test agent_paused displays pause notification."""
        reporter = ConsoleReporter(quiet=False)
        reporter.state_started("main", "START.md")  # Set up context
        reporter.agent_paused("main", "timeout")

        captured = capsys.readouterr()
        assert "Paused" in captured.out
        assert "timeout" in captured.out

    def test_workflow_paused_displays_message_with_workflow_id(self, capsys):
        """Test workflow_paused displays pause message with workflow ID."""
        reporter = ConsoleReporter(quiet=False)
        reporter.workflow_paused("test-workflow-123", 0.0567, 2)

        captured = capsys.readouterr()
        assert "Workflow paused" in captured.out
        assert "2 agent(s) paused" in captured.out
        assert "$0.0567" in captured.out
        assert "raymond --resume test-workflow-123" in captured.out

    def test_script_started_displays_message(self, capsys):
        """Test script_started displays execution message."""
        reporter = ConsoleReporter(quiet=False)
        reporter.script_started("main", "CHECK.bat")
        
        captured = capsys.readouterr()
        assert "[main] CHECK.bat" in captured.out

    def test_script_state_completed_displays_exit_code_time_and_cost(self, capsys):
        """Test script_state_completed displays exit code, execution time, and costs."""
        reporter = ConsoleReporter(quiet=False)
        reporter.script_state_completed("main", 0, 125.5, 0.0, 14.8081)

        captured = capsys.readouterr()
        assert "Done" in captured.out
        assert "exit 0" in captured.out
        assert "125ms" in captured.out or "126ms" in captured.out
        assert "$0.0000" in captured.out
        assert "total: $14.8081" in captured.out

    def test_script_state_completed_displays_all_values(self, capsys):
        """Test script_state_completed displays exit code, duration, and costs in one Done line."""
        reporter = ConsoleReporter(quiet=False)
        reporter.script_state_completed("main", 0, 125.5, 0.0353, 14.8081)

        captured = capsys.readouterr()
        assert "Done" in captured.out
        assert "exit 0" in captured.out
        assert "125ms" in captured.out or "126ms" in captured.out
        assert "$0.0353" in captured.out
        assert "total: $14.8081" in captured.out

    def test_tool_error_tracks_last_tool(self, capsys):
        """Test tool_error uses last tool invocation for context."""
        reporter = ConsoleReporter(quiet=False)
        reporter.tool_invocation("main", "Write", "file.txt")
        reporter.tool_error("main", "Permission denied")
        
        captured = capsys.readouterr()
        # Should show [Write] in error message
        assert "[Write]" in captured.out
        assert "Permission denied" in captured.out

    def test_message_truncation_to_terminal_width(self, capsys):
        """Test long messages are truncated to terminal width."""
        reporter = ConsoleReporter(quiet=False)
        long_message = "A" * 200
        reporter.progress_message("main", long_message)
        
        captured = capsys.readouterr()
        # Message should be truncated
        assert len(captured.out.split("\n")[0]) < 250  # Some overhead for prefix

    def test_agent_id_coloring(self, capsys):
        """Test agent IDs can be colored if terminal supports it."""
        # This test verifies the coloring mechanism exists
        # Actual color output depends on terminal capabilities
        reporter = ConsoleReporter(quiet=False)
        reporter.state_started("main", "START.md")
        
        captured = capsys.readouterr()
        assert "[main]" in captured.out

    def test_unicode_fallback_to_ascii(self, capsys):
        """Test Unicode symbols fall back to ASCII when not supported."""
        # Create reporter - it will detect terminal capabilities
        reporter = ConsoleReporter(quiet=False)
        reporter.transition("main", "NEXT.md", "goto")
        
        # Check that transition was printed (either Unicode or ASCII format is acceptable)
        captured = capsys.readouterr()
        assert "NEXT.md" in captured.out
        assert "goto" in captured.out
        # Should contain either Unicode arrow (→) or ASCII arrow (->)
        assert ("→" in captured.out or "->" in captured.out)

    # Context switching tests
    def test_context_switching_between_agents(self, capsys):
        """Test headers appear when output switches between different agents."""
        reporter = ConsoleReporter(quiet=False)
        
        # Agent 1 starts state
        reporter.state_started("main", "DISPATCH.md")
        reporter.progress_message("main", "Starting dispatch...")
        
        # Agent 2 starts state
        reporter.state_started("worker1", "WORKER.md")
        reporter.progress_message("worker1", "Processing item...")
        
        # Agent 1 outputs again - should show header
        reporter.progress_message("main", "Still dispatching...")
        
        captured = capsys.readouterr()
        lines = captured.out.strip().split('\n')
        
        # Should have headers for both agents
        assert "[main] DISPATCH.md" in captured.out
        assert "[worker1] WORKER.md" in captured.out
        
        # Count occurrences - main header should appear twice (initial + context switch)
        main_headers = [line for line in lines if "[main] DISPATCH.md" in line]
        assert len(main_headers) == 2, f"Expected 2 main headers, got {len(main_headers)}"

    def test_no_duplicate_headers_same_agent_same_state(self, capsys):
        """Test multiple messages from same agent/state don't print duplicate headers."""
        reporter = ConsoleReporter(quiet=False)
        
        # Agent starts state
        reporter.state_started("main", "START.md")
        reporter.progress_message("main", "First message")
        reporter.progress_message("main", "Second message")
        reporter.progress_message("main", "Third message")
        
        captured = capsys.readouterr()
        lines = captured.out.strip().split('\n')
        
        # Should only have one header
        main_headers = [line for line in lines if "[main] START.md" in line]
        assert len(main_headers) == 1, f"Expected 1 header, got {len(main_headers)}"

    def test_context_switch_after_state_transition(self, capsys):
        """Test new state header appears when agent transitions to different state."""
        reporter = ConsoleReporter(quiet=False)
        
        # Agent starts in first state
        reporter.state_started("main", "START.md")
        reporter.progress_message("main", "In START state")
        reporter.state_completed("main", 0.01, 0.01)
        reporter.transition("main", "NEXT.md", "goto")
        
        # Agent starts in new state
        reporter.state_started("main", "NEXT.md")
        reporter.progress_message("main", "In NEXT state")
        
        captured = capsys.readouterr()
        # Should have both state headers
        assert "[main] START.md" in captured.out
        assert "[main] NEXT.md" in captured.out

    def test_interleaved_output_shows_headers(self, capsys):
        """Test interleaved output from multiple agents shows headers correctly."""
        reporter = ConsoleReporter(quiet=False)
        
        # Simulate interleaved output
        reporter.state_started("main", "MONITOR.md")
        reporter.progress_message("main", "Waiting...")
        
        reporter.state_started("worker1", "WORKER.md")
        reporter.progress_message("worker1", "Processing...")
        
        reporter.progress_message("main", "Still waiting...")
        reporter.progress_message("worker1", "Almost done...")
        reporter.progress_message("main", "Final wait...")
        
        captured = capsys.readouterr()
        lines = captured.out.strip().split('\n')
        
        # Count headers - each should appear multiple times
        main_headers = [line for line in lines if "[main] MONITOR.md" in line]
        worker_headers = [line for line in lines if "[worker1] WORKER.md" in line]
        
        # Both should have multiple headers due to context switching
        assert len(main_headers) >= 2, f"Expected at least 2 main headers, got {len(main_headers)}"
        assert len(worker_headers) >= 2, f"Expected at least 2 worker headers, got {len(worker_headers)}"

    def test_retry_loop_shows_header_again(self, capsys):
        """Test retry loop shows header again even for same agent/state."""
        reporter = ConsoleReporter(quiet=False)
        
        # First attempt
        reporter.state_started("main", "START.md")
        reporter.progress_message("main", "First attempt")
        reporter.error("main", "No transition tag - retrying (1/3)")
        
        # Retry - state_started called again with same state
        reporter.state_started("main", "START.md")
        reporter.progress_message("main", "Retry attempt")
        
        captured = capsys.readouterr()
        lines = captured.out.strip().split('\n')
        
        # Should have header twice (initial + retry)
        main_headers = [line for line in lines if "[main] START.md" in line]
        assert len(main_headers) == 2, f"Expected 2 headers (initial + retry), got {len(main_headers)}"

    def test_agent_termination_checks_context(self, capsys):
        """Test agent_terminated checks context and shows header if needed."""
        reporter = ConsoleReporter(quiet=False)
        
        # Agent 1 starts
        reporter.state_started("main", "DISPATCH.md")
        reporter.progress_message("main", "Dispatching...")
        
        # Agent 2 starts
        reporter.state_started("worker1", "WORKER.md")
        reporter.progress_message("worker1", "Working...")
        
        # Agent 1 terminates - should show header
        reporter.agent_terminated("main", "Dispatch complete")
        
        captured = capsys.readouterr()
        lines = captured.out.strip().split('\n')
        
        # Should have main header multiple times (initial + before termination)
        main_headers = [line for line in lines if "[main] DISPATCH.md" in line]
        assert len(main_headers) >= 2, f"Expected at least 2 main headers, got {len(main_headers)}"
        assert "Result:" in captured.out

    def test_missing_state_tracking_graceful_handling(self, capsys):
        """Test graceful handling when progress_message called before state_started."""
        reporter = ConsoleReporter(quiet=False)
        
        # Call progress_message without state_started first
        reporter.progress_message("main", "Message without state")
        
        captured = capsys.readouterr()
        # Should still print message, just without header
        assert "Message without state" in captured.out
        # Should not have header (headers start with [agent_id] followed by state name)
        # Check that no line starts with "[main] " (which would be a header)
        lines = captured.out.strip().split('\n')
        header_lines = [line for line in lines if line.startswith("[main] ")]
        assert len(header_lines) == 0, f"Expected no header lines, but found: {header_lines}"

    def test_transition_checks_context(self, capsys):
        """Test transition checks context and shows header if needed."""
        reporter = ConsoleReporter(quiet=False)
        
        # Agent 1 starts
        reporter.state_started("main", "DISPATCH.md")
        reporter.progress_message("main", "Dispatching...")
        
        # Agent 2 starts
        reporter.state_started("worker1", "WORKER.md")
        reporter.progress_message("worker1", "Working...")
        
        # Agent 1 transitions - should show header
        reporter.transition("main", "NEXT.md", "goto")
        
        captured = capsys.readouterr()
        lines = captured.out.strip().split('\n')
        
        # Should have main header multiple times (initial + before transition)
        main_headers = [line for line in lines if "[main] DISPATCH.md" in line]
        assert len(main_headers) >= 2, f"Expected at least 2 main headers, got {len(main_headers)}"
        assert "NEXT.md" in captured.out

    def test_script_started_updates_tracking(self, capsys):
        """Test script_started updates state tracking like state_started."""
        reporter = ConsoleReporter(quiet=False)

        # Script starts
        reporter.script_started("main", "CHECK.bat")
        reporter.script_state_completed("main", 0, 100.0, 0.0, 0.0)

        captured = capsys.readouterr()
        assert "[main] CHECK.bat" in captured.out
        assert "Done" in captured.out

    def test_script_state_completed_checks_context(self, capsys):
        """Test script_state_completed checks context and shows header if needed."""
        reporter = ConsoleReporter(quiet=False)

        # Script starts
        reporter.script_started("main", "CHECK.bat")

        # Another agent outputs
        reporter.state_started("worker1", "WORKER.md")
        reporter.progress_message("worker1", "Working...")

        # Script completes - should show header
        reporter.script_state_completed("main", 0, 100.0, 0.0, 0.0)

        captured = capsys.readouterr()
        lines = captured.out.strip().split('\n')

        # Should have script header multiple times (initial + before completion)
        script_headers = [line for line in lines if "[main] CHECK.bat" in line]
        assert len(script_headers) >= 2, f"Expected at least 2 script headers, got {len(script_headers)}"

    def test_tool_error_checks_context(self, capsys):
        """Test tool_error checks context and shows header if needed."""
        reporter = ConsoleReporter(quiet=False)
        
        # Agent 1 starts
        reporter.state_started("main", "WRITE.md")
        reporter.tool_invocation("main", "Write", "file.txt")
        
        # Agent 2 starts
        reporter.state_started("worker1", "WORKER.md")
        reporter.progress_message("worker1", "Working...")
        
        # Agent 1 has tool error - should show header
        reporter.tool_error("main", "Permission denied", "Write")
        
        captured = capsys.readouterr()
        lines = captured.out.strip().split('\n')
        
        # Should have main header multiple times (initial + before error)
        main_headers = [line for line in lines if "[main] WRITE.md" in line]
        assert len(main_headers) >= 2, f"Expected at least 2 main headers, got {len(main_headers)}"
        assert "Permission denied" in captured.out

    def test_state_completed_checks_context(self, capsys):
        """Test state_completed checks context and shows header if needed."""
        reporter = ConsoleReporter(quiet=False)
        
        # Agent 1 starts
        reporter.state_started("main", "START.md")
        reporter.progress_message("main", "Starting...")
        
        # Agent 2 starts
        reporter.state_started("worker1", "WORKER.md")
        reporter.progress_message("worker1", "Working...")
        
        # Agent 1 completes - should show header
        reporter.state_completed("main", 0.01, 0.01)
        
        captured = capsys.readouterr()
        lines = captured.out.strip().split('\n')
        
        # Should have main header multiple times (initial + before completion)
        main_headers = [line for line in lines if "[main] START.md" in line]
        assert len(main_headers) >= 2, f"Expected at least 2 main headers, got {len(main_headers)}"
        assert "Done" in captured.out

    def test_error_checks_context(self, capsys):
        """Test error checks context and shows header if needed."""
        reporter = ConsoleReporter(quiet=False)
        
        # Agent 1 starts
        reporter.state_started("main", "START.md")
        reporter.progress_message("main", "Starting...")
        
        # Agent 2 starts
        reporter.state_started("worker1", "WORKER.md")
        reporter.progress_message("worker1", "Working...")
        
        # Agent 1 has error - should show header
        reporter.error("main", "Budget exceeded")
        
        captured = capsys.readouterr()
        lines = captured.out.strip().split('\n')
        
        # Should have main header multiple times (initial + before error)
        main_headers = [line for line in lines if "[main] START.md" in line]
        assert len(main_headers) >= 2, f"Expected at least 2 main headers, got {len(main_headers)}"
        assert "Budget exceeded" in captured.out

    def test_agent_termination_cleanup(self, capsys):
        """Test agent_terminated cleans up state tracking."""
        reporter = ConsoleReporter(quiet=False)
        
        # Agent starts and terminates
        reporter.state_started("main", "START.md")
        reporter.agent_terminated("main", "Complete")
        
        # Verify state was cleaned up: if we try to output from same agent without state_started,
        # should not show header (since state was cleaned up)
        reporter.progress_message("main", "After termination")
        
        captured = capsys.readouterr()
        lines = captured.out.strip().split('\n')
        main_headers = [line for line in lines if "[main] START.md" in line]
        # Should have 1 header: from state_started (agent_terminated doesn't print duplicate
        # if context hasn't changed, which is correct behavior)
        assert len(main_headers) == 1, f"Expected 1 header, got {len(main_headers)}"
        # Progress message should appear but without header (state cleaned up)
        assert "After termination" in captured.out
        # Verify no header was printed before "After termination" message
        # (since state was cleaned up, _ensure_context returns early)
        assert "Result:" in captured.out


class TestDynamicWidth:
    """Tests for dynamic terminal width handling."""

    def test_available_width_returns_correct_width(self):
        """Test _available_width calculates correct available width."""
        reporter = ConsoleReporter(quiet=False)
        # With a typical terminal width of 80, prefix of 5 and safety margin of 2
        # available = 80 - 5 - 2 = 73, but clamped to max 160
        with patch('shutil.get_terminal_size') as mock_size:
            mock_size.return_value = MagicMock(columns=80)
            width = reporter._available_width(5)
            assert width == 73  # 80 - 5 - 2

    def test_available_width_respects_minimum(self):
        """Test _available_width clamps to MIN_CONTENT_WIDTH on narrow terminals."""
        reporter = ConsoleReporter(quiet=False)
        with patch('shutil.get_terminal_size') as mock_size:
            # Very narrow terminal: 30 columns
            mock_size.return_value = MagicMock(columns=30)
            width = reporter._available_width(5)
            # 30 - 5 - 2 = 23, but should be clamped to minimum of 40
            assert width == 40

    def test_available_width_respects_maximum(self):
        """Test _available_width clamps to MAX_CONTENT_WIDTH on wide terminals."""
        reporter = ConsoleReporter(quiet=False)
        with patch('shutil.get_terminal_size') as mock_size:
            # Very wide terminal: 300 columns
            mock_size.return_value = MagicMock(columns=300)
            width = reporter._available_width(5)
            # 300 - 5 - 2 = 293, but should be clamped to maximum of 160
            assert width == 160

    def test_available_width_at_minimum_boundary(self):
        """Test _available_width at exact minimum boundary."""
        reporter = ConsoleReporter(quiet=False)
        with patch('shutil.get_terminal_size') as mock_size:
            # Terminal width that produces exactly MIN_CONTENT_WIDTH
            # 40 + 5 + 2 = 47 columns needed for prefix 5
            mock_size.return_value = MagicMock(columns=47)
            width = reporter._available_width(5)
            assert width == 40

    def test_available_width_at_maximum_boundary(self):
        """Test _available_width at exact maximum boundary."""
        reporter = ConsoleReporter(quiet=False)
        with patch('shutil.get_terminal_size') as mock_size:
            # Terminal width that produces exactly MAX_CONTENT_WIDTH
            # 160 + 5 + 2 = 167 columns needed for prefix 5
            mock_size.return_value = MagicMock(columns=167)
            width = reporter._available_width(5)
            assert width == 160

    def test_progress_message_uses_dynamic_width(self, capsys):
        """Test progress_message uses dynamic terminal width for truncation."""
        reporter = ConsoleReporter(quiet=False)
        with patch('shutil.get_terminal_size') as mock_size:
            # Set a narrow terminal width
            mock_size.return_value = MagicMock(columns=60)
            # With prefix of 5 and safety margin of 2, available is 53
            # But clamped to minimum of 40
            long_message = "A" * 100
            reporter.progress_message("main", long_message)

            captured = capsys.readouterr()
            # Message should be truncated (will have ... suffix)
            assert "..." in captured.out

    def test_tool_error_uses_dynamic_width(self, capsys):
        """Test tool_error uses dynamic terminal width for truncation."""
        reporter = ConsoleReporter(quiet=False)
        with patch('shutil.get_terminal_size') as mock_size:
            mock_size.return_value = MagicMock(columns=80)
            long_error = "E" * 200
            reporter.tool_error("main", long_error, "Write")

            captured = capsys.readouterr()
            # Message should be truncated (will have ... suffix)
            assert "..." in captured.out
            assert "[Write]" in captured.out

    def test_agent_terminated_uses_dynamic_width(self, capsys):
        """Test agent_terminated uses dynamic terminal width for truncation."""
        reporter = ConsoleReporter(quiet=False)
        with patch('shutil.get_terminal_size') as mock_size:
            mock_size.return_value = MagicMock(columns=80)
            long_result = "R" * 200
            reporter.agent_terminated("main", long_result)

            captured = capsys.readouterr()
            # Result should be truncated (will have ... suffix)
            assert "..." in captured.out
            assert "Result:" in captured.out

    def test_debug_path_not_truncated(self, capsys):
        """Test long debug paths are not truncated in workflow_started."""
        reporter = ConsoleReporter(quiet=False)
        # Create a very long debug path (over 100 characters)
        long_path = Path("/very/long/path" + "/subdir" * 15 + "/debug")
        reporter.workflow_started("test-workflow", "/path/to/workflow", long_path)

        captured = capsys.readouterr()
        assert "Debug:" in captured.out
        # The full path should appear (not truncated with ...)
        # Check that the path contains the full "subdir" pattern repeated
        assert "/subdir" in captured.out
        # Should not have truncation ellipsis at the start
        assert "..." not in captured.out.split("Debug:")[1].strip()

    def test_no_hardcoded_width_values(self):
        """Test that hardcoded width values are removed from truncation calls."""
        import inspect
        from src.console import ConsoleReporter

        # Get source code of the relevant methods
        progress_source = inspect.getsource(ConsoleReporter.progress_message)
        tool_error_source = inspect.getsource(ConsoleReporter.tool_error)
        agent_terminated_source = inspect.getsource(ConsoleReporter.agent_terminated)

        # Check that hardcoded max_width values are not present
        assert "max_width=80" not in progress_source, "progress_message still has hardcoded max_width=80"
        assert "max_width=60" not in tool_error_source, "tool_error still has hardcoded max_width=60"
        assert "max_width=60" not in agent_terminated_source, "agent_terminated still has hardcoded max_width=60"

    def test_prefix_constants_defined(self):
        """Test that prefix constants are defined on ConsoleReporter."""
        assert hasattr(ConsoleReporter, 'MIN_CONTENT_WIDTH')
        assert hasattr(ConsoleReporter, 'MAX_CONTENT_WIDTH')
        assert hasattr(ConsoleReporter, 'PREFIX_TREE_BRANCH')
        assert hasattr(ConsoleReporter, 'PREFIX_TOOL_ERROR_BASE')
        assert hasattr(ConsoleReporter, 'PREFIX_RESULT')

        # Verify values
        assert ConsoleReporter.MIN_CONTENT_WIDTH == 40
        assert ConsoleReporter.MAX_CONTENT_WIDTH == 160
        assert ConsoleReporter.DEFAULT_TERMINAL_WIDTH == 80
        assert ConsoleReporter.PREFIX_TREE_BRANCH == 5
        assert ConsoleReporter.PREFIX_TOOL_ERROR_BASE == 4
        assert ConsoleReporter.PREFIX_RESULT == 15

    def test_width_override_via_constructor(self, capsys):
        """Test width override via constructor parameter."""
        reporter = ConsoleReporter(quiet=False, width=120)
        # The override should be used for width calculation
        with patch('shutil.get_terminal_size') as mock_size:
            # Even if shutil returns 80, the override should be used
            mock_size.return_value = MagicMock(columns=80)
            width = reporter._detect_terminal_width()
            assert width == 120

    def test_width_override_takes_precedence(self, capsys):
        """Test width override takes precedence over COLUMNS env var."""
        reporter = ConsoleReporter(quiet=False, width=150)
        # Even with COLUMNS set, the constructor override should win
        with patch.dict(os.environ, {'COLUMNS': '100'}):
            width = reporter._detect_terminal_width()
            assert width == 150

    def test_columns_env_var_respected(self, capsys):
        """Test COLUMNS environment variable is respected when no override."""
        reporter = ConsoleReporter(quiet=False)
        with patch.dict(os.environ, {'COLUMNS': '200'}):
            width = reporter._detect_terminal_width()
            assert width == 200

    def test_columns_env_var_invalid_fallback(self, capsys):
        """Test fallback when COLUMNS has invalid value."""
        reporter = ConsoleReporter(quiet=False)
        with patch.dict(os.environ, {'COLUMNS': 'not_a_number'}):
            with patch('shutil.get_terminal_size') as mock_size:
                mock_size.return_value = MagicMock(columns=80)
                width = reporter._detect_terminal_width()
                # Should fall back to shutil detection
                assert width == 80

    def test_available_width_uses_override(self, capsys):
        """Test _available_width uses width override."""
        reporter = ConsoleReporter(quiet=False, width=150)
        # Width 150 - prefix 5 - safety 2 = 143, but capped at MAX_CONTENT_WIDTH 160
        width = reporter._available_width(5)
        assert width == 143

    def test_init_reporter_accepts_width(self):
        """Test init_reporter accepts width parameter."""
        from src.console import init_reporter, get_reporter
        init_reporter(quiet=False, width=180)
        reporter = get_reporter()
        assert reporter._width_override == 180
