"""Console output reporter for workflow execution."""

import os
import sys
import shutil
from pathlib import Path
from typing import Optional, Dict, Tuple
from datetime import datetime


class ConsoleReporter:
    """Handles formatted console output for workflow execution.

    Provides real-time visibility into workflow execution with compact,
    hierarchical output format. Supports quiet mode and terminal capability
    detection (colors, Unicode, width).
    """

    # Color palette for agent IDs (cycling through distinct colors)
    AGENT_COLORS = [
        '\033[36m',  # Cyan
        '\033[33m',  # Yellow
        '\033[35m',  # Magenta
        '\033[32m',  # Green
        '\033[34m',  # Blue
        '\033[31m',  # Red
    ]
    RESET_COLOR = '\033[0m'
    ERROR_COLOR = '\033[31m'  # Red for errors
    WARNING_COLOR = '\033[33m'  # Yellow for warnings

    # Width constants for dynamic truncation
    MIN_CONTENT_WIDTH = 40   # Minimum characters for truncated content
    MAX_CONTENT_WIDTH = 160  # Maximum characters for truncated content
    DEFAULT_TERMINAL_WIDTH = 80  # Default when detection fails

    # Prefix lengths for width calculation
    PREFIX_TREE_BRANCH = 5   # "  ├─ "
    PREFIX_TOOL_ERROR_BASE = 4  # "  ! "
    PREFIX_RESULT = 15       # '  ⇒ Result: "' plus closing quote

    def __init__(self, quiet: bool = False, width: Optional[int] = None):
        """Initialize console reporter.

        Args:
            quiet: If True, suppress progress messages and tool invocations.
                Still shows state headers, transitions, errors, costs, and results.
            width: Override terminal width. If None, auto-detect from environment.
                In Docker/non-TTY environments, set COLUMNS env var or use this parameter.
        """
        self.quiet = quiet
        self._width_override = width
        self._agent_colors: Dict[str, str] = {}
        self._agent_counter = 0
        self._last_tool: Dict[str, Tuple[str, Optional[str]]] = {}  # agent_id -> (tool_name, detail)

        # State tracking for interleaved multi-agent output
        self._agent_states: Dict[str, str] = {}  # agent_id -> current_state
        self._last_context: Optional[Tuple[str, str]] = None  # (agent_id, state) of last output

        # Detect terminal capabilities
        self._supports_color = self._detect_color_support()
        self._supports_unicode = self._detect_unicode_support()
        self._terminal_width = self._detect_terminal_width()
        
        # Choose symbols based on Unicode support
        if self._supports_unicode:
            self.TREE_BRANCH = "├─"
            self.TREE_END = "└─"
            self.ARROW = "→"
            self.RESULT_ARROW = "⇒"
            self.FORK_SYMBOL = "⑂"
        else:
            self.TREE_BRANCH = "|-"
            self.TREE_END = "`-"
            self.ARROW = "->"
            self.RESULT_ARROW = "=>"
            self.FORK_SYMBOL = "++"
    
    def _detect_color_support(self) -> bool:
        """Detect if terminal supports colors."""
        if not sys.stdout.isatty():
            return False
        
        # Check for NO_COLOR environment variable
        if os.getenv('NO_COLOR'):
            return False
        
        # Check terminal type
        term = os.getenv('TERM', '')
        if term and 'color' in term.lower():
            return True
        
        # Windows Terminal detection
        if os.getenv('WT_SESSION'):
            return True
        
        # Check encoding (UTF-8 terminals often support colors)
        if hasattr(sys.stdout, 'encoding') and sys.stdout.encoding:
            if 'utf' in sys.stdout.encoding.lower():
                return True
        
        return False
    
    def _detect_unicode_support(self) -> bool:
        """Detect if terminal supports Unicode box-drawing characters."""
        if not sys.stdout.isatty():
            return False
        
        # Check encoding
        if hasattr(sys.stdout, 'encoding') and sys.stdout.encoding:
            encoding = sys.stdout.encoding.lower()
            if 'utf' in encoding or 'utf-8' in encoding:
                return True
        
        # Check TERM environment variable
        term = os.getenv('TERM', '')
        if term and ('xterm' in term.lower() or 'utf' in term.lower()):
            return True
        
        # Windows Terminal supports Unicode
        if os.getenv('WT_SESSION'):
            return True
        
        return False
    
    def _detect_terminal_width(self) -> int:
        """Detect terminal width.

        Detection priority:
        1. Explicit width override (from constructor parameter)
        2. COLUMNS environment variable (user-configurable)
        3. shutil.get_terminal_size() (works when TTY is attached)
        4. Default of 80 columns

        In Docker/non-TTY environments, shutil.get_terminal_size() often returns
        the default 80 because there's no actual TTY. Users can override this by:
        - Setting COLUMNS environment variable: COLUMNS=120 raymond ...
        - Adding width to config file

        Returns:
            Terminal width in columns
        """
        # Priority 1: Explicit override from constructor
        if self._width_override is not None:
            return self._width_override

        # Priority 2: COLUMNS environment variable
        columns_env = os.getenv('COLUMNS')
        if columns_env:
            try:
                width = int(columns_env)
                if width > 0:
                    return width
            except ValueError:
                pass  # Fall through to next detection method

        # Priority 3: shutil.get_terminal_size()
        # Note: In Docker/non-TTY environments, this returns the default (80, 24)
        try:
            size = shutil.get_terminal_size()
            return size.columns
        except (OSError, AttributeError):
            pass

        # Priority 4: Default fallback
        return self.DEFAULT_TERMINAL_WIDTH
    
    def _get_agent_color(self, agent_id: str) -> str:
        """Get color code for an agent ID, assigning colors on first use."""
        if not self._supports_color:
            return ""
        
        if agent_id not in self._agent_colors:
            color = self.AGENT_COLORS[self._agent_counter % len(self.AGENT_COLORS)]
            self._agent_colors[agent_id] = color
            self._agent_counter += 1
        
        return self._agent_colors[agent_id]
    
    def _format_agent_id(self, agent_id: str) -> str:
        """Format agent ID with color if supported."""
        color = self._get_agent_color(agent_id)
        if color:
            return f"{color}[{agent_id}]{self.RESET_COLOR}"
        return f"[{agent_id}]"
    
    def _truncate_message(self, message: str, max_width: Optional[int] = None) -> str:
        """Truncate message to fit terminal width."""
        if max_width is None:
            max_width = self._terminal_width

        if len(message) <= max_width:
            return message

        # Leave room for ellipsis
        return message[:max_width - 3] + "..."

    def _available_width(self, prefix_length: int) -> int:
        """Calculate available width for message content.

        Re-detects terminal width on each call to handle terminal resize,
        unless a width override is set.
        Clamps result between MIN_CONTENT_WIDTH and MAX_CONTENT_WIDTH.

        Args:
            prefix_length: Length of the prefix before the content

        Returns:
            Available character width for content
        """
        terminal_width = self._detect_terminal_width()
        available = terminal_width - prefix_length - 2  # 2 = safety margin
        return max(self.MIN_CONTENT_WIDTH, min(available, self.MAX_CONTENT_WIDTH))

    def _print(self, message: str) -> None:
        """Print message to stdout."""
        print(message, flush=True)
    
    def _ensure_context(self, agent_id: str) -> None:
        """Ensure state header is displayed if context changed.
        
        Checks if the current agent/state context differs from the last output.
        If different, automatically inserts a state header before the next message.
        
        Args:
            agent_id: Agent identifier for the current message
        """
        current_state = self._agent_states.get(agent_id)
        if current_state is None:
            # Agent state not tracked yet - this shouldn't happen in normal flow,
            # but we'll skip header insertion to avoid errors
            return
        
        current_context = (agent_id, current_state)
        if current_context != self._last_context:
            # Context changed - print header
            # Note: We call the internal logic directly to avoid recursion
            # and to ensure _last_context is updated correctly
            self._last_context = current_context
            
            agent_str = self._format_agent_id(agent_id)
            self._print(f"{agent_str} {current_state}")
    
    def workflow_started(self, workflow_id: str, scope_dir: str, debug_dir: Optional[Path]) -> None:
        """Display workflow startup information.
        
        Args:
            workflow_id: Workflow identifier
            scope_dir: Scope directory path
            debug_dir: Optional debug directory path
        """
        timestamp = datetime.now().strftime("%H:%M:%S")
        self._print(f"[{timestamp}] Workflow: {workflow_id}")
        self._print(f"[{timestamp}] Scope: {scope_dir}")
        if debug_dir:
            self._print(f"[{timestamp}] Debug: {debug_dir}")
        self._print("")  # Empty line after startup
    
    def workflow_completed(self, total_cost: float) -> None:
        """Display workflow completion message.
        
        Args:
            total_cost: Total cost accumulated across all agents
        """
        self._print(f"\nWorkflow completed. Total cost: ${total_cost:.4f}")
    
    def state_started(self, agent_id: str, state: str) -> None:
        """Display state execution header.
        
        Args:
            agent_id: Agent identifier
            state: State filename (e.g., "START.md")
        """
        # Update state tracking
        self._agent_states[agent_id] = state
        self._last_context = (agent_id, state)
        
        # Display header
        # Note: Always print header, even if _last_context was already set to the same value
        # This ensures headers appear on retry attempts (per design doc requirement)
        agent_str = self._format_agent_id(agent_id)
        self._print(f"{agent_str} {state}")
    
    def progress_message(self, agent_id: str, message: str) -> None:
        """Display progress message from assistant.

        Args:
            agent_id: Agent identifier
            message: Progress message text (will be truncated if long)
        """
        if self.quiet:
            return

        # Ensure context header is displayed if needed
        self._ensure_context(agent_id)

        truncated = self._truncate_message(message, max_width=self._available_width(self.PREFIX_TREE_BRANCH))
        self._print(f"  {self.TREE_BRANCH} {truncated}")
    
    def tool_invocation(self, agent_id: str, tool_name: str, detail: Optional[str] = None) -> None:
        """Display tool invocation.
        
        Args:
            agent_id: Agent identifier
            tool_name: Name of the tool (e.g., "Read", "Write", "Bash")
            detail: Optional detail (e.g., filename for Read/Write, command for Bash)
        """
        if self.quiet:
            return
        
        # Ensure context header is displayed if needed
        self._ensure_context(agent_id)
        
        # Track last tool for error messages
        self._last_tool[agent_id] = (tool_name, detail)
        
        if detail:
            self._print(f"  {self.TREE_BRANCH} [{tool_name}] {detail}")
        else:
            self._print(f"  {self.TREE_BRANCH} [{tool_name}]")
    
    def tool_error(self, agent_id: str, error_message: str, tool_name: Optional[str] = None) -> None:
        """Display tool execution error.
        
        Args:
            agent_id: Agent identifier
            error_message: Error message text
            tool_name: Optional tool name (if not provided, uses last tool invocation)
        """
        # Ensure context header is displayed if needed
        self._ensure_context(agent_id)
        
        # Use provided tool_name or fall back to last tool
        if tool_name is None:
            last_tool_info = self._last_tool.get(agent_id)
            if last_tool_info:
                tool_name = last_tool_info[0]

        # Calculate prefix length for dynamic truncation
        # With tool name: "  ! [ToolName] error: " = 14 + len(tool_name)
        # Without tool name: "  ! Tool error: " = 16
        if tool_name:
            prefix_len = 14 + len(tool_name)
        else:
            prefix_len = 16

        # Truncate long error messages
        truncated_error = self._truncate_message(error_message, max_width=self._available_width(prefix_len))

        if tool_name:
            error_str = f"! [{tool_name}] error: {truncated_error}"
        else:
            error_str = f"! Tool error: {truncated_error}"
        
        if self._supports_color:
            error_str = f"{self.ERROR_COLOR}{error_str}{self.RESET_COLOR}"
        
        self._print(f"  {error_str}")
    
    def state_completed(self, agent_id: str, cost: float, total_cost: float) -> None:
        """Display state completion with cost information.
        
        Args:
            agent_id: Agent identifier
            cost: Cost for this specific invocation/state
            total_cost: Workflow-wide accumulated total cost
        """
        # Ensure context header is displayed if needed
        self._ensure_context(agent_id)
        
        self._print(f"  {self.TREE_END} Done (${cost:.4f}, total: ${total_cost:.4f})")
    
    def transition(self, agent_id: str, target: str, transition_type: str, spawned_agent_id: Optional[str] = None) -> None:
        """Display state transition.
        
        Args:
            agent_id: Agent identifier
            target: Target state filename
            transition_type: Type of transition ("goto", "reset", "function", "call", "fork", "result")
            spawned_agent_id: Optional spawned agent ID (for fork transitions)
        """
        # Ensure context header is displayed if needed
        self._ensure_context(agent_id)
        
        if transition_type == "fork":
            # Fork transition uses special format
            agent_str = self._format_agent_id(agent_id)
            if spawned_agent_id:
                self._print(f"{agent_str} {self.FORK_SYMBOL} {target} {self.ARROW} {spawned_agent_id}")
            else:
                self._print(f"{agent_str} {self.FORK_SYMBOL} {target}")
        elif transition_type == "result":
            # Result transitions are handled by agent_terminated
            pass
        else:
            # Regular transitions (goto, reset, function, call) all use same arrow
            self._print(f"  {self.ARROW} {target}")
    
    def agent_terminated(self, agent_id: str, result: str) -> None:
        """Display agent termination with result.
        
        Args:
            agent_id: Agent identifier
            result: Result payload (may contain <result> tags to extract)
        """
        # Ensure context header is displayed if needed
        self._ensure_context(agent_id)
        
        # Extract result from <result> tags if present
        if "<result>" in result and "</result>" in result:
            result = result.split("<result>")[1].split("</result>")[0]
        
        # Truncate long results
        truncated_result = self._truncate_message(result, max_width=self._available_width(self.PREFIX_RESULT))
        self._print(f"  {self.RESULT_ARROW} Result: \"{truncated_result}\"")
        
        # Clean up state tracking for terminated agent
        self._agent_states.pop(agent_id, None)
        self._last_tool.pop(agent_id, None)  # Also clean up tool tracking for consistency
    
    def error(self, agent_id: str, message: str) -> None:
        """Display error or warning message.
        
        Args:
            agent_id: Agent identifier
            message: Error/warning message
        """
        # Ensure context header is displayed if needed
        self._ensure_context(agent_id)
        
        error_str = f"! {message}"
        if self._supports_color:
            error_str = f"{self.ERROR_COLOR}{error_str}{self.RESET_COLOR}"
        self._print(f"  {error_str}")

    def agent_paused(self, agent_id: str, reason: str) -> None:
        """Display agent paused notification.

        Args:
            agent_id: Agent identifier
            reason: Reason for pausing (e.g., "timeout")
        """
        self._ensure_context(agent_id)
        pause_str = f"|| Paused: {reason}"
        if self._supports_color:
            pause_str = f"{self.WARNING_COLOR}{pause_str}{self.RESET_COLOR}"
        self._print(f"  {pause_str}")

    def workflow_paused(self, workflow_id: str, total_cost: float, paused_count: int) -> None:
        """Display workflow paused message.

        Args:
            workflow_id: Workflow identifier for resume command
            total_cost: Total cost accumulated across all agents
            paused_count: Number of paused agents
        """
        self._print(f"\nWorkflow paused ({paused_count} agent(s) paused). Cost: ${total_cost:.4f}")
        self._print(f"Resume with: raymond --resume {workflow_id}")

    def agent_spawned(self, parent_id: str, child_id: str, target_state: str) -> None:
        """Display agent spawn notification (optional, for future use).
        
        Args:
            parent_id: Parent agent identifier
            child_id: Child agent identifier
            target_state: Target state for new agent
        """
        # Currently not used - fork transitions handle this via transition()
        pass
    
    def script_started(self, agent_id: str, state: str) -> None:
        """Display script execution start.
        
        Args:
            agent_id: Agent identifier
            state: Script state filename (e.g., "CHECK.bat")
        """
        # Update state tracking (same as state_started)
        self._agent_states[agent_id] = state
        self._last_context = (agent_id, state)
        
        agent_str = self._format_agent_id(agent_id)
        self._print(f"{agent_str} {state}")
        
        if not self.quiet:
            self._print(f"  {self.TREE_BRANCH} Executing script...")
    
    def script_completed(self, agent_id: str, exit_code: int, duration_ms: float) -> None:
        """Display script execution completion.
        
        Args:
            agent_id: Agent identifier
            exit_code: Script exit code
            duration_ms: Execution time in milliseconds
        """
        # Ensure context header is displayed if needed
        self._ensure_context(agent_id)
        
        # Format duration (round to nearest integer)
        duration_str = f"{int(round(duration_ms))}ms"
        self._print(f"  {self.TREE_END} Done (exit {exit_code}, {duration_str})")


# Module-level singleton instance
_reporter: Optional[ConsoleReporter] = None


def get_reporter() -> ConsoleReporter:
    """Get the global console reporter instance.

    Returns:
        ConsoleReporter instance (creates one if not initialized)
    """
    global _reporter
    if _reporter is None:
        _reporter = ConsoleReporter(quiet=False)
    return _reporter


def init_reporter(quiet: bool = False, width: Optional[int] = None) -> None:
    """Initialize the global console reporter instance.

    Args:
        quiet: If True, suppress progress messages and tool invocations
        width: Override terminal width. If None, auto-detect from environment.
            In Docker/non-TTY environments, set COLUMNS env var or use this parameter.
    """
    global _reporter
    _reporter = ConsoleReporter(quiet=quiet, width=width)
