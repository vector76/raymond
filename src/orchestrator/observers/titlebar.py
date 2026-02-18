"""Title bar observer for orchestrator event bus.

Updates the terminal title bar via OSC 2 escape sequence each time a state
begins executing. The title format is 'ray: <stem>' where stem is the state
filename with its extension stripped.
"""

import logging
import sys
from pathlib import Path

from ..bus import EventBus
from ..events import StateStarted

logger = logging.getLogger(__name__)


class TitleBarObserver:
    """Observer that updates the terminal title bar on state transitions.

    Subscribes to StateStarted events and writes an OSC 2 escape sequence
    to stdout to update the terminal window/tab title. Always active — no
    configuration surface.
    """

    def __init__(self, bus: EventBus) -> None:
        self._bus = bus
        self._subscribe()

    def _subscribe(self) -> None:
        self._bus.on(StateStarted, self._on_state_started)

    def _unsubscribe(self) -> None:
        self._bus.off(StateStarted, self._on_state_started)

    def close(self) -> None:
        self._unsubscribe()

    def _on_state_started(self, event: StateStarted) -> None:
        try:
            stem = Path(event.state_name).stem
            title = f"ray: {stem}"
            sys.stdout.write(f"\x1b]2;{title}\x07")
            sys.stdout.flush()
        except Exception as e:
            logger.warning(f"TitleBarObserver failed on state_started: {e}")
