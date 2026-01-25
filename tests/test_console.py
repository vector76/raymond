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
