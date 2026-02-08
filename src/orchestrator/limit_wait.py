"""Utility for parsing and waiting on Claude Code usage limit reset times.

This module provides functions to parse the reset time from Claude Code's
usage limit error messages and calculate how long to wait before resuming.
"""

import re
from datetime import datetime, timedelta
from typing import Optional
from zoneinfo import ZoneInfo


# Regex to extract reset time and timezone from limit message
# Matches: "resets 3pm (America/Chicago)" or "resets 12am (America/New_York)"
_RESET_TIME_PATTERN = re.compile(
    r'resets\s+(\d{1,2}(?:am|pm))\s+\(([^)]+)\)',
    re.IGNORECASE
)


def parse_limit_reset_time(error_message: str, now: Optional[datetime] = None) -> Optional[datetime]:
    """Parse reset time from Claude Code limit message.

    Extracts the reset hour and timezone from messages like:
        "You've hit your limit · resets 3pm (America/Chicago)"

    The message only contains an hour, not a date. To determine the target datetime:
    - Construct today's date at the stated hour in the stated timezone.
    - If that time is already in the past, use tomorrow instead.

    Args:
        error_message: The error message string from Claude Code.
        now: Optional current time for testing. If None, uses datetime.now().

    Returns:
        Timezone-aware datetime of the reset time, or None if parsing fails.
    """
    match = _RESET_TIME_PATTERN.search(error_message)
    if not match:
        return None

    time_str = match.group(1).lower()  # e.g., "3pm", "12am"
    tz_str = match.group(2)  # e.g., "America/Chicago"

    try:
        tz = ZoneInfo(tz_str)
    except (KeyError, ValueError):
        return None

    # Parse hour from time string (e.g., "3pm" -> 15, "12am" -> 0, "12pm" -> 12)
    try:
        # Extract numeric part and am/pm suffix
        if time_str.endswith('am'):
            hour = int(time_str[:-2])
            if hour == 12:
                hour = 0  # 12am = midnight
        elif time_str.endswith('pm'):
            hour = int(time_str[:-2])
            if hour != 12:
                hour += 12  # 12pm stays as 12 (noon)
        else:
            return None
    except (ValueError, IndexError):
        return None

    if not (0 <= hour <= 23):
        return None

    # Get current time in the stated timezone
    if now is None:
        now = datetime.now(tz)
    elif now.tzinfo is None:
        now = now.replace(tzinfo=tz)
    else:
        now = now.astimezone(tz)

    # Construct today's date at the stated hour
    reset_time = now.replace(hour=hour, minute=0, second=0, microsecond=0)

    # If the reset time is in the past (or exactly now), use tomorrow
    if reset_time <= now:
        reset_time += timedelta(days=1)

    return reset_time


def calculate_wait_seconds(reset_time: datetime, buffer_minutes: int = 5,
                           now: Optional[datetime] = None) -> float:
    """Calculate seconds to wait until reset time plus buffer.

    Args:
        reset_time: Timezone-aware datetime when the limit resets.
        buffer_minutes: Extra minutes to wait after reset (default: 5).
        now: Optional current time for testing.

    Returns:
        Seconds to wait. Returns 0.0 if the time has already passed.
    """
    target = reset_time + timedelta(minutes=buffer_minutes)

    if now is None:
        now = datetime.now(reset_time.tzinfo)
    elif now.tzinfo is None:
        now = now.replace(tzinfo=reset_time.tzinfo)

    delta = (target - now).total_seconds()
    return max(0.0, delta)


def format_wait_message(reset_time: datetime, buffer_minutes: int = 5,
                        now: Optional[datetime] = None) -> str:
    """Format a human-readable wait message.

    Args:
        reset_time: Timezone-aware datetime when the limit resets.
        buffer_minutes: Extra minutes added to the reset time.
        now: Optional current time for testing.

    Returns:
        Formatted message like "Waiting for usage limit reset at 3:05pm America/Chicago (42 minutes)..."
    """
    target = reset_time + timedelta(minutes=buffer_minutes)
    tz_name = str(reset_time.tzinfo)

    if now is None:
        now = datetime.now(reset_time.tzinfo)
    elif now.tzinfo is None:
        now = now.replace(tzinfo=reset_time.tzinfo)

    wait_seconds = max(0.0, (target - now).total_seconds())
    total_minutes = int(wait_seconds / 60)

    if total_minutes >= 60:
        hours = total_minutes // 60
        minutes = total_minutes % 60
        duration_str = f"{hours}h {minutes}m"
    else:
        duration_str = f"{total_minutes} minutes"

    # Format the target time (e.g., "3:05pm")
    target_str = target.strftime("%-I:%M%p").lower()

    return f"Waiting for usage limit reset at {target_str} {tz_name} ({duration_str})..."
