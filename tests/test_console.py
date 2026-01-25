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

    def test_transition_fork_displays_fork_symbol(self, capsys):
        """Test fork transition displays with fork symbol."""
        reporter = ConsoleReporter(quiet=False)
        reporter.transition("main", "WORKER.md", "fork", "main_worker1")
        
        captured = capsys.readouterr()
        assert "⑂" in captured.out or "++" in captured.out
        assert "WORKER.md" in captured.out
        assert "main_worker1" in captured.out

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

    def test_script_started_displays_message(self, capsys):
        """Test script_started displays execution message."""
        reporter = ConsoleReporter(quiet=False)
        reporter.script_started("main", "CHECK.bat")
        
        captured = capsys.readouterr()
        assert "[main] CHECK.bat" in captured.out

    def test_script_completed_displays_exit_code_and_time(self, capsys):
        """Test script_completed displays exit code and execution time."""
        reporter = ConsoleReporter(quiet=False)
        reporter.script_completed("main", 0, 125.5)
        
        captured = capsys.readouterr()
        assert "Done" in captured.out
        assert "exit 0" in captured.out
        assert "125ms" in captured.out or "126ms" in captured.out

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
        reporter.script_completed("main", 0, 100.0)
        
        captured = capsys.readouterr()
        assert "[main] CHECK.bat" in captured.out
        assert "Done" in captured.out

    def test_script_completed_checks_context(self, capsys):
        """Test script_completed checks context and shows header if needed."""
        reporter = ConsoleReporter(quiet=False)
        
        # Script starts
        reporter.script_started("main", "CHECK.bat")
        
        # Another agent outputs
        reporter.state_started("worker1", "WORKER.md")
        reporter.progress_message("worker1", "Working...")
        
        # Script completes - should show header
        reporter.script_completed("main", 0, 100.0)
        
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
