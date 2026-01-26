"""Event bus implementation for orchestrator.

This module provides a simple synchronous publish/subscribe system for decoupling
core orchestration logic from side effects (debug logging, console output, etc.).

The bus is synchronous (not async) because events are emitted inline during
execution and observers perform quick I/O operations. Handler exceptions are
caught and logged to ensure observers cannot crash the core orchestration loop.
"""

import logging
from collections import defaultdict
from typing import Any, Callable, Dict, List, Type, TypeVar

logger = logging.getLogger(__name__)

# Type variable for event types
E = TypeVar('E')

# Type alias for event handlers
EventHandler = Callable[[Any], None]


class EventBus:
    """Simple synchronous event bus for publish/subscribe pattern.

    Maintains a dictionary mapping event types to lists of handler functions.
    When an event is emitted, all registered handlers for that event type are
    called synchronously.

    Handler exceptions are caught and logged, not propagated, to ensure
    observers cannot crash the core orchestration loop.

    Example usage:
        bus = EventBus()

        def on_state_started(event: StateStarted):
            print(f"Agent {event.agent_id} started {event.state_name}")

        bus.on(StateStarted, on_state_started)
        bus.emit(StateStarted(agent_id="main", state_name="START.md", state_type="markdown"))
    """

    def __init__(self) -> None:
        """Initialize an empty event bus."""
        self._handlers: Dict[Type, List[EventHandler]] = defaultdict(list)

    def on(self, event_type: Type[E], handler: Callable[[E], None]) -> None:
        """Subscribe a handler to an event type.

        Args:
            event_type: The event class to subscribe to
            handler: Function to call when events of this type are emitted.
                     Should accept a single argument (the event instance).
        """
        self._handlers[event_type].append(handler)

    def off(self, event_type: Type[E], handler: Callable[[E], None]) -> None:
        """Unsubscribe a handler from an event type.

        If the handler is not currently subscribed, this is a no-op.

        Args:
            event_type: The event class to unsubscribe from
            handler: The handler function to remove
        """
        handlers = self._handlers.get(event_type)
        if handlers and handler in handlers:
            handlers.remove(handler)

    def emit(self, event: Any) -> None:
        """Dispatch an event to all registered handlers.

        Calls each handler registered for the event's type. Handler exceptions
        are caught and logged but not propagated, ensuring that one failing
        observer cannot affect others or crash the orchestration loop.

        Args:
            event: The event instance to dispatch
        """
        event_type = type(event)
        handlers = self._handlers.get(event_type, [])

        for handler in handlers:
            try:
                handler(event)
            except Exception as e:
                # Log the error but don't propagate - observers must not crash workflow
                logger.error(
                    f"Event handler {handler.__name__} raised exception for "
                    f"{event_type.__name__}: {e}",
                    exc_info=True
                )

    def has_handlers(self, event_type: Type) -> bool:
        """Check if any handlers are registered for an event type.

        Args:
            event_type: The event class to check

        Returns:
            True if at least one handler is registered
        """
        return bool(self._handlers.get(event_type))

    def clear(self) -> None:
        """Remove all registered handlers.

        Useful for cleanup in tests.
        """
        self._handlers.clear()
