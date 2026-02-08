"""Tests for auto-resume after usage limit reset."""

import pytest
from datetime import datetime, timedelta
from pathlib import Path
from unittest.mock import patch, AsyncMock
from zoneinfo import ZoneInfo

from src.orchestrator.limit_wait import (
    parse_limit_reset_time,
    calculate_wait_seconds,
    format_wait_message,
)


class TestParseLimitResetTime:
    """Tests for parse_limit_reset_time() function."""

    def test_standard_pm_time(self):
        """Parse 'resets 3pm (America/Chicago)' -> valid datetime."""
        tz = ZoneInfo("America/Chicago")
        # Use a fixed "now" that is before 3pm Chicago
        now = datetime(2026, 2, 1, 10, 0, 0, tzinfo=tz)

        result = parse_limit_reset_time(
            "You've hit your limit · resets 3pm (America/Chicago)",
            now=now
        )

        assert result is not None
        assert result.hour == 15
        assert result.minute == 0
        assert result.day == 1  # Same day (3pm is in the future)
        assert str(result.tzinfo) == "America/Chicago"

    def test_standard_am_time(self):
        """Parse 'resets 3am (America/Chicago)' -> valid datetime."""
        tz = ZoneInfo("America/Chicago")
        # "now" is 4am, so 3am is in the past -> should be tomorrow
        now = datetime(2026, 2, 1, 4, 0, 0, tzinfo=tz)

        result = parse_limit_reset_time(
            "You've hit your limit · resets 3am (America/Chicago)",
            now=now
        )

        assert result is not None
        assert result.hour == 3
        assert result.day == 2  # Tomorrow (3am is in the past)

    def test_12am_midnight_edge_case(self):
        """Parse 'resets 12am (America/New_York)' -> midnight."""
        tz = ZoneInfo("America/New_York")
        now = datetime(2026, 2, 1, 14, 0, 0, tzinfo=tz)

        result = parse_limit_reset_time(
            "You've hit your limit · resets 12am (America/New_York)",
            now=now
        )

        assert result is not None
        assert result.hour == 0  # Midnight
        assert result.day == 2  # Tomorrow (midnight is in the past relative to 2pm)

    def test_12pm_noon_edge_case(self):
        """Parse 'resets 12pm (America/New_York)' -> noon."""
        tz = ZoneInfo("America/New_York")
        now = datetime(2026, 2, 1, 10, 0, 0, tzinfo=tz)

        result = parse_limit_reset_time(
            "You've hit your limit · resets 12pm (America/New_York)",
            now=now
        )

        assert result is not None
        assert result.hour == 12  # Noon
        assert result.day == 1  # Same day (noon is in the future)

    def test_reset_time_in_past_uses_tomorrow(self):
        """If the stated hour is in the past, the returned datetime is tomorrow."""
        tz = ZoneInfo("America/Chicago")
        now = datetime(2026, 2, 1, 16, 0, 0, tzinfo=tz)  # 4pm

        result = parse_limit_reset_time(
            "You've hit your limit · resets 3pm (America/Chicago)",
            now=now
        )

        assert result is not None
        assert result.day == 2  # Tomorrow
        assert result.hour == 15

    def test_reset_time_exactly_now_uses_tomorrow(self):
        """If the reset time is exactly now, use tomorrow."""
        tz = ZoneInfo("America/Chicago")
        now = datetime(2026, 2, 1, 15, 0, 0, tzinfo=tz)  # Exactly 3pm

        result = parse_limit_reset_time(
            "You've hit your limit · resets 3pm (America/Chicago)",
            now=now
        )

        assert result is not None
        assert result.day == 2  # Tomorrow

    def test_no_reset_time_returns_none(self):
        """'You've hit your limit for today' -> returns None."""
        result = parse_limit_reset_time("You've hit your limit for today")
        assert result is None

    def test_garbage_input_returns_none(self):
        """Random string -> returns None."""
        result = parse_limit_reset_time("something completely different")
        assert result is None

    def test_empty_string_returns_none(self):
        """Empty string -> returns None."""
        result = parse_limit_reset_time("")
        assert result is None

    def test_invalid_timezone_returns_none(self):
        """Invalid timezone -> returns None."""
        result = parse_limit_reset_time(
            "You've hit your limit · resets 3pm (Invalid/Timezone)"
        )
        assert result is None

    def test_different_timezone(self):
        """Parse with a different timezone."""
        tz = ZoneInfo("Europe/London")
        now = datetime(2026, 2, 1, 10, 0, 0, tzinfo=tz)

        result = parse_limit_reset_time(
            "You've hit your limit · resets 3pm (Europe/London)",
            now=now
        )

        assert result is not None
        assert result.hour == 15
        assert str(result.tzinfo) == "Europe/London"

    def test_case_insensitive_am_pm(self):
        """Parse should handle AM/PM case variants."""
        tz = ZoneInfo("America/Chicago")
        now = datetime(2026, 2, 1, 10, 0, 0, tzinfo=tz)

        result = parse_limit_reset_time(
            "You've hit your limit · resets 3PM (America/Chicago)",
            now=now
        )

        assert result is not None
        assert result.hour == 15


class TestCalculateWaitSeconds:
    """Tests for calculate_wait_seconds() function."""

    def test_future_reset_time(self):
        """Returns positive seconds for a future reset time."""
        tz = ZoneInfo("America/Chicago")
        reset_time = datetime(2026, 2, 1, 15, 0, 0, tzinfo=tz)
        now = datetime(2026, 2, 1, 14, 0, 0, tzinfo=tz)

        result = calculate_wait_seconds(reset_time, buffer_minutes=5, now=now)

        # 1 hour + 5 minutes = 3900 seconds
        assert result == 3900.0

    def test_past_reset_time_returns_zero(self):
        """Returns 0.0 if reset time + buffer is already past."""
        tz = ZoneInfo("America/Chicago")
        reset_time = datetime(2026, 2, 1, 13, 0, 0, tzinfo=tz)
        now = datetime(2026, 2, 1, 14, 0, 0, tzinfo=tz)

        result = calculate_wait_seconds(reset_time, buffer_minutes=5, now=now)

        assert result == 0.0

    def test_zero_buffer(self):
        """With zero buffer, waits exactly until reset time."""
        tz = ZoneInfo("America/Chicago")
        reset_time = datetime(2026, 2, 1, 15, 0, 0, tzinfo=tz)
        now = datetime(2026, 2, 1, 14, 30, 0, tzinfo=tz)

        result = calculate_wait_seconds(reset_time, buffer_minutes=0, now=now)

        assert result == 1800.0  # 30 minutes


class TestFormatWaitMessage:
    """Tests for format_wait_message() function."""

    def test_short_wait(self):
        """Format message for a short wait (< 1 hour)."""
        tz = ZoneInfo("America/Chicago")
        reset_time = datetime(2026, 2, 1, 15, 0, 0, tzinfo=tz)
        now = datetime(2026, 2, 1, 14, 30, 0, tzinfo=tz)

        msg = format_wait_message(reset_time, buffer_minutes=5, now=now)

        assert "America/Chicago" in msg
        assert "35 minutes" in msg
        assert "3:05pm" in msg

    def test_long_wait(self):
        """Format message for a long wait (> 1 hour)."""
        tz = ZoneInfo("America/Chicago")
        reset_time = datetime(2026, 2, 1, 18, 0, 0, tzinfo=tz)
        now = datetime(2026, 2, 1, 14, 0, 0, tzinfo=tz)

        msg = format_wait_message(reset_time, buffer_minutes=5, now=now)

        assert "4h 5m" in msg


class TestAutoWaitWorkflow:
    """Integration tests for auto-wait behavior in the workflow loop."""

    @pytest.mark.asyncio
    async def test_auto_wait_sleeps_and_resumes(self, tmp_path):
        """Test that auto-wait sleeps and resumes the workflow after limit error."""
        from src.orchestrator import run_all_agents
        from src.state import write_state, read_state

        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        state_dir = str(tmp_path / ".raymond" / "state")
        Path(state_dir).mkdir(parents=True)

        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")

        workflow_id = "test-auto-wait"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": None,
                "stack": []
            }]
        }

        write_state(workflow_id, state, state_dir=state_dir)

        # First call: return limit error. Second call: succeed.
        call_count = {"count": 0}

        async def mock_stream(*args, **kwargs):
            call_count["count"] += 1
            if call_count["count"] == 1:
                yield {
                    "type": "result",
                    "subtype": "success",
                    "is_error": True,
                    "result": "You've hit your limit · resets 3pm (America/Chicago)",
                    "session_id": "test-session"
                }
            else:
                yield {
                    "type": "result",
                    "subtype": "success",
                    "result": "<result>Done</result>",
                    "session_id": "test-session-2"
                }

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream), \
             patch('asyncio.sleep', new_callable=AsyncMock) as mock_sleep:
            await run_all_agents(workflow_id, state_dir=state_dir, quiet=True)

            # Verify asyncio.sleep was called (auto-wait)
            mock_sleep.assert_called_once()
            sleep_seconds = mock_sleep.call_args[0][0]
            assert sleep_seconds > 0, "Should sleep for a positive duration"

            # Verify workflow completed (both calls happened)
            assert call_count["count"] == 2
            state_file = Path(state_dir) / f"{workflow_id}.json"
            assert not state_file.exists(), "State file should be deleted after completion"

    @pytest.mark.asyncio
    async def test_no_wait_flag_disables_auto_wait(self, tmp_path):
        """Test that --no-wait disables auto-waiting."""
        from src.orchestrator import run_all_agents
        from src.state import write_state, read_state

        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        state_dir = str(tmp_path / ".raymond" / "state")
        Path(state_dir).mkdir(parents=True)

        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")

        workflow_id = "test-no-wait"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": None,
                "stack": []
            }]
        }

        write_state(workflow_id, state, state_dir=state_dir)

        async def mock_stream(*args, **kwargs):
            yield {
                "type": "result",
                "subtype": "success",
                "is_error": True,
                "result": "You've hit your limit · resets 3pm (America/Chicago)",
                "session_id": "test-session"
            }

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream), \
             patch('asyncio.sleep', new_callable=AsyncMock) as mock_sleep:
            await run_all_agents(workflow_id, state_dir=state_dir, quiet=True, no_wait=True)

            # Verify asyncio.sleep was NOT called
            mock_sleep.assert_not_called()

            # Verify workflow is paused (not completed)
            state_file = Path(state_dir) / f"{workflow_id}.json"
            assert state_file.exists(), "State file should exist for paused workflow"

            final_state = read_state(workflow_id, state_dir=state_dir)
            assert final_state["agents"][0]["status"] == "paused"

    @pytest.mark.asyncio
    async def test_unparseable_limit_message_falls_back_to_pause(self, tmp_path):
        """Test that unparseable limit message falls back to pause-and-exit."""
        from src.orchestrator import run_all_agents
        from src.state import write_state, read_state

        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        state_dir = str(tmp_path / ".raymond" / "state")
        Path(state_dir).mkdir(parents=True)

        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")

        workflow_id = "test-unparseable"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": None,
                "stack": []
            }]
        }

        write_state(workflow_id, state, state_dir=state_dir)

        async def mock_stream(*args, **kwargs):
            yield {
                "type": "result",
                "subtype": "success",
                "is_error": True,
                "result": "You've hit your limit for today",
                "session_id": "test-session"
            }

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream), \
             patch('asyncio.sleep', new_callable=AsyncMock) as mock_sleep:
            await run_all_agents(workflow_id, state_dir=state_dir, quiet=True)

            # Verify asyncio.sleep was NOT called (fallback to pause-and-exit)
            mock_sleep.assert_not_called()

            # Verify workflow is paused
            final_state = read_state(workflow_id, state_dir=state_dir)
            assert final_state["agents"][0]["status"] == "paused"

    @pytest.mark.asyncio
    async def test_mixed_pause_reasons_falls_back_to_pause(self, tmp_path):
        """Test that when some agents have unparseable limit messages, falls back to pause-and-exit."""
        from src.orchestrator import run_all_agents
        from src.state import write_state, read_state

        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        state_dir = str(tmp_path / ".raymond" / "state")
        Path(state_dir).mkdir(parents=True)

        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")

        workflow_id = "test-mixed-pause"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": scope_dir,
            "agents": [
                {
                    "id": "agent1",
                    "current_state": "START.md",
                    "session_id": None,
                    "stack": []
                },
                {
                    "id": "agent2",
                    "current_state": "START.md",
                    "session_id": None,
                    "stack": []
                }
            ]
        }

        write_state(workflow_id, state, state_dir=state_dir)

        # First agent gets unparseable limit, second gets parseable limit
        call_count = {"count": 0}

        async def mock_stream(*args, **kwargs):
            call_count["count"] += 1
            if call_count["count"] == 1:
                yield {
                    "type": "result",
                    "subtype": "success",
                    "is_error": True,
                    "result": "You've hit your limit for today",
                    "session_id": "test-session-1"
                }
            else:
                yield {
                    "type": "result",
                    "subtype": "success",
                    "is_error": True,
                    "result": "You've hit your limit · resets 3pm (America/Chicago)",
                    "session_id": "test-session-2"
                }

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream), \
             patch('asyncio.sleep', new_callable=AsyncMock) as mock_sleep:
            await run_all_agents(workflow_id, state_dir=state_dir, quiet=True)

            # Verify asyncio.sleep was NOT called (mixed reasons -> pause-and-exit)
            mock_sleep.assert_not_called()

            # Verify workflow is paused
            state_file = Path(state_dir) / f"{workflow_id}.json"
            assert state_file.exists()

    @pytest.mark.asyncio
    async def test_reset_time_in_past_resumes_immediately(self, tmp_path):
        """Test that when reset time is in the past, workflow resumes immediately."""
        from src.orchestrator import run_all_agents
        from src.state import write_state

        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        state_dir = str(tmp_path / ".raymond" / "state")
        Path(state_dir).mkdir(parents=True)

        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")

        workflow_id = "test-past-reset"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": None,
                "stack": []
            }]
        }

        write_state(workflow_id, state, state_dir=state_dir)

        call_count = {"count": 0}

        async def mock_stream(*args, **kwargs):
            call_count["count"] += 1
            if call_count["count"] == 1:
                yield {
                    "type": "result",
                    "subtype": "success",
                    "is_error": True,
                    "result": "You've hit your limit · resets 3pm (America/Chicago)",
                    "session_id": "test-session"
                }
            else:
                yield {
                    "type": "result",
                    "subtype": "success",
                    "result": "<result>Done</result>",
                    "session_id": "test-session-2"
                }

        # Mock parse_limit_reset_time to return a time in the past
        past_time = datetime.now(ZoneInfo("America/Chicago")) - timedelta(hours=1)

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream), \
             patch('src.orchestrator.workflow.parse_limit_reset_time', return_value=past_time), \
             patch('asyncio.sleep', new_callable=AsyncMock) as mock_sleep:
            await run_all_agents(workflow_id, state_dir=state_dir, quiet=True)

            # Sleep should be called with 0 (or not called because wait_seconds is 0)
            if mock_sleep.called:
                sleep_seconds = mock_sleep.call_args[0][0]
                assert sleep_seconds == 0.0, "Should sleep for 0 seconds when reset is past"

            # Workflow should complete
            assert call_count["count"] == 2

    @pytest.mark.asyncio
    async def test_second_limit_hit_after_resume_waits_again(self, tmp_path):
        """Test that hitting limit again after resume triggers another wait."""
        from src.orchestrator import run_all_agents
        from src.state import write_state

        scope_dir = str(tmp_path / "workflows" / "test")
        Path(scope_dir).mkdir(parents=True)

        state_dir = str(tmp_path / ".raymond" / "state")
        Path(state_dir).mkdir(parents=True)

        prompt_file = Path(scope_dir) / "START.md"
        prompt_file.write_text("Test prompt")

        workflow_id = "test-double-limit"
        state = {
            "workflow_id": workflow_id,
            "scope_dir": scope_dir,
            "agents": [{
                "id": "main",
                "current_state": "START.md",
                "session_id": None,
                "stack": []
            }]
        }

        write_state(workflow_id, state, state_dir=state_dir)

        call_count = {"count": 0}

        async def mock_stream(*args, **kwargs):
            call_count["count"] += 1
            if call_count["count"] <= 2:
                # First two calls: limit error
                yield {
                    "type": "result",
                    "subtype": "success",
                    "is_error": True,
                    "result": "You've hit your limit · resets 3pm (America/Chicago)",
                    "session_id": "test-session"
                }
            else:
                # Third call: success
                yield {
                    "type": "result",
                    "subtype": "success",
                    "result": "<result>Done</result>",
                    "session_id": "test-session-3"
                }

        with patch('src.orchestrator.wrap_claude_code_stream', side_effect=mock_stream), \
             patch('asyncio.sleep', new_callable=AsyncMock) as mock_sleep:
            await run_all_agents(workflow_id, state_dir=state_dir, quiet=True)

            # asyncio.sleep should be called twice (one per limit hit)
            assert mock_sleep.call_count == 2

            # Workflow should complete after the third call
            assert call_count["count"] == 3

            state_file = Path(state_dir) / f"{workflow_id}.json"
            assert not state_file.exists()


class TestAutoWaitCLI:
    """Tests for --no-wait CLI flag."""

    def test_no_wait_flag_parsed(self):
        """Test that --no-wait flag is parsed correctly."""
        from src.cli import create_parser

        parser = create_parser()
        args = parser.parse_args(["test.md", "--no-wait"])
        assert args.no_wait is True

    def test_no_wait_default_false(self):
        """Test that no_wait defaults to False."""
        from src.cli import create_parser

        parser = create_parser()
        args = parser.parse_args(["test.md"])
        assert args.no_wait is False


class TestAutoWaitConfig:
    """Tests for no_wait config option."""

    def test_no_wait_config_validation_bool(self, tmp_path):
        """Test that no_wait config value must be boolean."""
        from src.config import validate_config, ConfigError

        config_file = tmp_path / "config.toml"

        # Valid
        result = validate_config({"no_wait": True}, config_file)
        assert result["no_wait"] is True

        # Invalid
        with pytest.raises(ConfigError, match="expected boolean"):
            validate_config({"no_wait": "yes"}, config_file)

    def test_no_wait_config_merge(self):
        """Test that no_wait is merged from config when CLI doesn't set it."""
        import argparse
        from src.config import merge_config_and_args

        args = argparse.Namespace(no_wait=False)
        config = {"no_wait": True}

        result = merge_config_and_args(config, args)
        assert result.no_wait is True

    def test_no_wait_cli_overrides_config(self):
        """Test that CLI --no-wait overrides config."""
        import argparse
        from src.config import merge_config_and_args

        args = argparse.Namespace(no_wait=True)
        config = {"no_wait": False}

        result = merge_config_and_args(config, args)
        assert result.no_wait is True
