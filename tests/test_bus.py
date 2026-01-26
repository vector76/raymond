"""Tests for orchestrator EventBus implementation.

These tests verify the publish/subscribe functionality of the EventBus,
including handler registration, event dispatch, and exception handling.
"""

import pytest
import logging
from datetime import datetime

from src.orchestrator.bus import EventBus
from src.orchestrator.events import (
    StateStarted,
    StateCompleted,
    TransitionOccurred,
    ErrorOccurred,
)


class TestEventBusSubscription:
    """Tests for event subscription and unsubscription."""

    def test_subscribe_and_emit(self):
        """Handler receives event when subscribed."""
        bus = EventBus()
        received_events = []

        def handler(event):
            received_events.append(event)

        bus.on(StateStarted, handler)

        event = StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        )
        bus.emit(event)

        assert len(received_events) == 1
        assert received_events[0] is event

    def test_multiple_handlers(self):
        """All handlers for an event type are called."""
        bus = EventBus()
        calls = {"handler1": 0, "handler2": 0, "handler3": 0}

        def handler1(event):
            calls["handler1"] += 1

        def handler2(event):
            calls["handler2"] += 1

        def handler3(event):
            calls["handler3"] += 1

        bus.on(StateStarted, handler1)
        bus.on(StateStarted, handler2)
        bus.on(StateStarted, handler3)

        event = StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        )
        bus.emit(event)

        assert calls["handler1"] == 1
        assert calls["handler2"] == 1
        assert calls["handler3"] == 1

    def test_unsubscribe(self):
        """Handler no longer called after unsubscription."""
        bus = EventBus()
        received_events = []

        def handler(event):
            received_events.append(event)

        bus.on(StateStarted, handler)

        # Emit first event - should be received
        event1 = StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        )
        bus.emit(event1)
        assert len(received_events) == 1

        # Unsubscribe
        bus.off(StateStarted, handler)

        # Emit second event - should not be received
        event2 = StateStarted(
            agent_id="main",
            state_name="NEXT.md",
            state_type="markdown",
        )
        bus.emit(event2)
        assert len(received_events) == 1  # Still 1, not 2

    def test_unsubscribe_nonexistent_handler(self):
        """Unsubscribing non-existent handler is a no-op."""
        bus = EventBus()

        def handler(event):
            pass

        # Should not raise
        bus.off(StateStarted, handler)

    def test_unsubscribe_from_empty_type(self):
        """Unsubscribing from type with no handlers is a no-op."""
        bus = EventBus()

        def handler(event):
            pass

        # Register for different type
        bus.on(StateCompleted, handler)

        # Should not raise when unsubscribing from type with no handlers
        bus.off(StateStarted, handler)


class TestEventBusEmission:
    """Tests for event emission behavior."""

    def test_emit_no_handlers(self):
        """Emitting event with no handlers does not raise."""
        bus = EventBus()

        event = StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        )
        # Should not raise
        bus.emit(event)

    def test_emit_to_correct_type_only(self):
        """Events are dispatched only to handlers for that type."""
        bus = EventBus()
        started_calls = []
        completed_calls = []

        def started_handler(event):
            started_calls.append(event)

        def completed_handler(event):
            completed_calls.append(event)

        bus.on(StateStarted, started_handler)
        bus.on(StateCompleted, completed_handler)

        # Emit StateStarted
        event = StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        )
        bus.emit(event)

        assert len(started_calls) == 1
        assert len(completed_calls) == 0

    def test_handler_exception_caught(self, caplog):
        """Handler exception is caught and logged, not propagated."""
        bus = EventBus()
        successful_calls = []

        def failing_handler(event):
            raise ValueError("Handler error")

        def successful_handler(event):
            successful_calls.append(event)

        bus.on(StateStarted, failing_handler)
        bus.on(StateStarted, successful_handler)

        event = StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        )

        # Should not raise
        with caplog.at_level(logging.ERROR):
            bus.emit(event)

        # Successful handler should still be called
        assert len(successful_calls) == 1

        # Error should be logged
        assert "failing_handler" in caplog.text
        assert "ValueError" in caplog.text or "Handler error" in caplog.text

    def test_handler_exception_does_not_prevent_other_handlers(self):
        """Exception in one handler doesn't prevent others from running."""
        bus = EventBus()
        call_order = []

        def handler1(event):
            call_order.append(1)
            raise RuntimeError("Handler 1 failed")

        def handler2(event):
            call_order.append(2)

        def handler3(event):
            call_order.append(3)
            raise RuntimeError("Handler 3 failed")

        def handler4(event):
            call_order.append(4)

        bus.on(StateStarted, handler1)
        bus.on(StateStarted, handler2)
        bus.on(StateStarted, handler3)
        bus.on(StateStarted, handler4)

        event = StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        )
        bus.emit(event)

        # All handlers should have been called despite exceptions
        assert call_order == [1, 2, 3, 4]


class TestEventBusUtilities:
    """Tests for utility methods."""

    def test_has_handlers_true(self):
        """has_handlers returns True when handlers exist."""
        bus = EventBus()

        def handler(event):
            pass

        bus.on(StateStarted, handler)
        assert bus.has_handlers(StateStarted) is True

    def test_has_handlers_false(self):
        """has_handlers returns False when no handlers exist."""
        bus = EventBus()
        assert bus.has_handlers(StateStarted) is False

    def test_has_handlers_after_unsubscribe(self):
        """has_handlers returns False after all handlers unsubscribed."""
        bus = EventBus()

        def handler(event):
            pass

        bus.on(StateStarted, handler)
        assert bus.has_handlers(StateStarted) is True

        bus.off(StateStarted, handler)
        assert bus.has_handlers(StateStarted) is False

    def test_clear(self):
        """clear removes all handlers."""
        bus = EventBus()
        calls = []

        def handler1(event):
            calls.append(1)

        def handler2(event):
            calls.append(2)

        bus.on(StateStarted, handler1)
        bus.on(StateCompleted, handler2)

        assert bus.has_handlers(StateStarted) is True
        assert bus.has_handlers(StateCompleted) is True

        bus.clear()

        assert bus.has_handlers(StateStarted) is False
        assert bus.has_handlers(StateCompleted) is False

        # Emit should not call any handlers
        bus.emit(StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        ))
        assert calls == []


class TestEventBusIntegration:
    """Integration tests with real event types."""

    def test_multiple_event_types(self):
        """Bus handles multiple event types correctly."""
        bus = EventBus()
        events_by_type = {
            "started": [],
            "completed": [],
            "transition": [],
        }

        bus.on(StateStarted, lambda e: events_by_type["started"].append(e))
        bus.on(StateCompleted, lambda e: events_by_type["completed"].append(e))
        bus.on(TransitionOccurred, lambda e: events_by_type["transition"].append(e))

        # Emit various events
        bus.emit(StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        ))
        bus.emit(StateCompleted(
            agent_id="main",
            state_name="START.md",
            cost_usd=0.05,
            total_cost_usd=0.05,
            session_id="sess-123",
            duration_ms=1000,
        ))
        bus.emit(TransitionOccurred(
            agent_id="main",
            from_state="START.md",
            to_state="NEXT.md",
            transition_type="goto",
        ))
        bus.emit(StateStarted(
            agent_id="main",
            state_name="NEXT.md",
            state_type="markdown",
        ))

        assert len(events_by_type["started"]) == 2
        assert len(events_by_type["completed"]) == 1
        assert len(events_by_type["transition"]) == 1

    def test_same_handler_multiple_types(self):
        """Same handler can be registered for multiple event types."""
        bus = EventBus()
        all_events = []

        def universal_handler(event):
            all_events.append(event)

        bus.on(StateStarted, universal_handler)
        bus.on(StateCompleted, universal_handler)
        bus.on(ErrorOccurred, universal_handler)

        bus.emit(StateStarted(
            agent_id="main",
            state_name="START.md",
            state_type="markdown",
        ))
        bus.emit(ErrorOccurred(
            agent_id="main",
            error_type="TestError",
            error_message="Test",
            current_state="START.md",
            is_retryable=False,
            retry_count=0,
            max_retries=3,
        ))

        assert len(all_events) == 2
        assert isinstance(all_events[0], StateStarted)
        assert isinstance(all_events[1], ErrorOccurred)
