"""Observers for the orchestrator event bus.

This package provides observer classes that subscribe to events and perform
side effects (debug file writing, console output, etc.).

Observers are decoupled from core orchestration logic and can be attached or
detached dynamically. They follow the observer pattern where each observer
subscribes to relevant events and handles them independently.
"""

from .console import ConsoleObserver
from .debug import DebugObserver
from .titlebar import TitleBarObserver

__all__ = [
    "ConsoleObserver",
    "DebugObserver",
    "TitleBarObserver",
]
