"""Debug observer for orchestrator event bus.

This module provides the DebugObserver class that subscribes to events and writes
debug files. It replaces the scattered save_* and log_* functions from the old
orchestrator code by centralizing all debug file operations in one place.

Debug files are written to the debug directory with the following naming convention:
- {agent_id}_{state_name}_{step_number:03d}.jsonl - Claude Code streaming output
- {agent_id}_{state_name}_{step_number:03d}.stdout.txt - Script stdout
- {agent_id}_{state_name}_{step_number:03d}.stderr.txt - Script stderr
- {agent_id}_{state_name}_{step_number:03d}.meta.json - Script execution metadata
- transitions.log - State transition history
"""

import json
import logging
from pathlib import Path
from typing import Any, Dict, Optional, TextIO

from ..bus import EventBus
from ..events import (
    ClaudeStreamOutput,
    ScriptOutput,
    StateCompleted,
    StateStarted,
    TransitionOccurred,
)
from ..executors.utils import extract_state_name

logger = logging.getLogger(__name__)


class DebugObserver:
    """Observer that writes debug files based on orchestration events.

    Subscribes to relevant events from the EventBus and writes debug files
    to a specified directory. All file operations are wrapped in try/except
    to ensure failures don't propagate to the orchestration loop.

    The observer maintains open file handles for JSONL streaming output to
    minimize I/O overhead. Call close() when the workflow completes to ensure
    all file handles are properly closed.

    Attributes:
        debug_dir: Directory where debug files are written
    """

    def __init__(self, debug_dir: Path, bus: EventBus) -> None:
        """Initialize the debug observer.

        Args:
            debug_dir: Directory for debug output files
            bus: Event bus to subscribe to
        """
        self.debug_dir = debug_dir
        self._bus = bus

        # Track open file handles for JSONL streaming (agent_id -> file handle)
        self._open_files: Dict[str, TextIO] = {}

        # Track current state name per agent for file naming
        self._agent_states: Dict[str, str] = {}

        # Subscribe to events
        self._subscribe()

    def _subscribe(self) -> None:
        """Subscribe to relevant events on the bus."""
        self._bus.on(StateStarted, self._on_state_started)
        self._bus.on(StateCompleted, self._on_state_completed)
        self._bus.on(ClaudeStreamOutput, self._on_claude_stream_output)
        self._bus.on(ScriptOutput, self._on_script_output)
        self._bus.on(TransitionOccurred, self._on_transition_occurred)

    def _unsubscribe(self) -> None:
        """Unsubscribe from all events on the bus."""
        self._bus.off(StateStarted, self._on_state_started)
        self._bus.off(StateCompleted, self._on_state_completed)
        self._bus.off(ClaudeStreamOutput, self._on_claude_stream_output)
        self._bus.off(ScriptOutput, self._on_script_output)
        self._bus.off(TransitionOccurred, self._on_transition_occurred)

    def close(self) -> None:
        """Clean up resources - close open file handles and unsubscribe.

        Should be called when the workflow completes to ensure all data
        is flushed and file handles are properly closed.
        """
        # Close all open JSONL file handles
        for agent_id, file_handle in self._open_files.items():
            try:
                file_handle.close()
            except Exception as e:
                logger.warning(f"Failed to close file handle for agent {agent_id}: {e}")

        self._open_files.clear()
        self._agent_states.clear()

        # Unsubscribe from events
        self._unsubscribe()

    def _get_jsonl_filepath(
        self,
        agent_id: str,
        state_name: str,
        step_number: int
    ) -> Path:
        """Get the filepath for a Claude Code JSONL output file.

        Args:
            agent_id: Agent identifier
            state_name: State name (without extension)
            step_number: Step number for this agent

        Returns:
            Path to the JSONL file
        """
        filename = f"{agent_id}_{state_name}_{step_number:03d}.jsonl"
        return self.debug_dir / filename

    def _on_state_started(self, event: StateStarted) -> None:
        """Handle StateStarted event - track current state for file naming.

        Args:
            event: StateStarted event
        """
        state_name = extract_state_name(event.state_name)
        self._agent_states[event.agent_id] = state_name

    def _on_state_completed(self, event: StateCompleted) -> None:
        """Handle StateCompleted event - close any open file handles for the agent.

        Args:
            event: StateCompleted event
        """
        # Close JSONL file handle if open
        if event.agent_id in self._open_files:
            try:
                self._open_files[event.agent_id].close()
            except Exception as e:
                logger.warning(
                    f"Failed to close JSONL file for agent {event.agent_id}: {e}"
                )
            del self._open_files[event.agent_id]

    def _on_claude_stream_output(self, event: ClaudeStreamOutput) -> None:
        """Handle ClaudeStreamOutput event - append JSON to JSONL file.

        Uses progressive writes with immediate flush to preserve data on crash.

        Args:
            event: ClaudeStreamOutput event
        """
        state_name = extract_state_name(event.state_name)
        filepath = self._get_jsonl_filepath(
            event.agent_id,
            state_name,
            event.step_number
        )

        try:
            # Get or create file handle for this agent
            if event.agent_id not in self._open_files:
                self._open_files[event.agent_id] = open(
                    filepath, 'a', encoding='utf-8'
                )

            file_handle = self._open_files[event.agent_id]

            # Write JSON line and flush immediately
            file_handle.write(json.dumps(event.json_object, ensure_ascii=False) + '\n')
            file_handle.flush()

        except Exception as e:
            logger.warning(f"Failed to append Claude output to {filepath}: {e}")

    def _on_script_output(self, event: ScriptOutput) -> None:
        """Handle ScriptOutput event - write stdout, stderr, and metadata files.

        Args:
            event: ScriptOutput event
        """
        state_name = extract_state_name(event.state_name)
        base_filename = f"{event.agent_id}_{state_name}_{event.step_number:03d}"

        # Write stdout
        stdout_filepath = self.debug_dir / f"{base_filename}.stdout.txt"
        try:
            with open(stdout_filepath, 'w', encoding='utf-8') as f:
                f.write(event.stdout)
        except Exception as e:
            logger.warning(f"Failed to save script stdout to {stdout_filepath}: {e}")

        # Write stderr
        stderr_filepath = self.debug_dir / f"{base_filename}.stderr.txt"
        try:
            with open(stderr_filepath, 'w', encoding='utf-8') as f:
                f.write(event.stderr)
        except Exception as e:
            logger.warning(f"Failed to save script stderr to {stderr_filepath}: {e}")

        # Write metadata
        metadata_filepath = self.debug_dir / f"{base_filename}.meta.json"
        metadata = {
            "exit_code": event.exit_code,
            "execution_time_ms": event.execution_time_ms,
            "env_vars": event.env_vars
        }
        try:
            with open(metadata_filepath, 'w', encoding='utf-8') as f:
                json.dump(metadata, f, indent=2, ensure_ascii=False)
        except Exception as e:
            logger.warning(f"Failed to save script metadata to {metadata_filepath}: {e}")

    def _on_transition_occurred(self, event: TransitionOccurred) -> None:
        """Handle TransitionOccurred event - append to transitions.log.

        Args:
            event: TransitionOccurred event
        """
        log_file = self.debug_dir / "transitions.log"

        try:
            with open(log_file, 'a', encoding='utf-8') as f:
                # Format log entry
                if event.to_state:
                    f.write(
                        f"{event.timestamp.isoformat()} [{event.agent_id}] "
                        f"{event.from_state} -> {event.to_state} ({event.transition_type})\n"
                    )
                else:
                    f.write(
                        f"{event.timestamp.isoformat()} [{event.agent_id}] "
                        f"{event.from_state} -> (result, terminated)\n"
                    )

                # Write metadata if present
                for key, value in event.metadata.items():
                    f.write(f"  {key}: {value}\n")

                f.write("\n")

        except Exception as e:
            logger.warning(f"Failed to write to transitions.log: {e}")
