"""Execution context for state executors.

This module defines the ExecutionContext dataclass that holds shared state
passed to executors. It replaces the many parameters currently passed through
function calls, providing a cleaner interface for executor invocations.
"""

from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, Optional

from src.orchestrator.bus import EventBus


@dataclass
class ExecutionContext:
    """Context for state execution.

    Holds shared configuration and state passed to executors. This dataclass
    centralizes execution parameters that would otherwise be passed as many
    individual arguments.

    Attributes:
        bus: EventBus for emitting events during execution
        workflow_id: Unique identifier for the current workflow
        scope_dir: Directory containing state files
        debug_dir: Directory for debug output (None if debug disabled)
        state_dir: Custom state directory if specified (for error saving)
        default_model: Model override from CLI (None uses prompt default)
        timeout: Timeout in seconds for Claude Code/script execution
        dangerously_skip_permissions: If True, skip permission prompts in Claude Code
        step_counters: Mutable dict tracking step numbers per agent (for debug files)
        reporter: Console reporter for output (may be None if quiet mode)
    """
    bus: EventBus
    workflow_id: str
    scope_dir: str
    debug_dir: Optional[Path] = None
    state_dir: Optional[str] = None
    default_model: Optional[str] = None
    timeout: Optional[float] = None
    dangerously_skip_permissions: bool = False
    step_counters: Dict[str, int] = field(default_factory=dict)
    reporter: Any = None  # Optional[ConsoleReporter], but avoiding import cycle

    def get_next_step_number(self, agent_id: str) -> int:
        """Get and increment the step counter for an agent.

        Used for debug file naming to ensure unique sequential filenames.

        Args:
            agent_id: The agent identifier

        Returns:
            The next step number (1-indexed)
        """
        if agent_id not in self.step_counters:
            self.step_counters[agent_id] = 0
        self.step_counters[agent_id] += 1
        return self.step_counters[agent_id]
