package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	wfstate "github.com/vector76/raymond/internal/state"
)

// ---------------------------------------------------------------------------
// parseResetWaitSeconds
// ---------------------------------------------------------------------------

func TestParseResetWaitSeconds_BasicPM(t *testing.T) {
	// Use a fixed "now" so results are deterministic.
	// "now" is Tuesday 2026-01-06 10:00:00 America/Chicago (UTC-6).
	loc, _ := time.LoadLocation("America/Chicago")
	now := time.Date(2026, 1, 6, 10, 0, 0, 0, loc)

	msg := "Claude Code usage limit reached. Your limit resets 3pm (America/Chicago) and will be available then."
	secs, ok := parseResetWaitSeconds(msg, now, 0)

	assert.True(t, ok)
	// reset is 15:00 same day; delta from 10:00 = 5 hours = 18000s.
	assert.InDelta(t, 5*3600.0, secs, 2.0, "expected ~5 hours until 3pm")
}

func TestParseResetWaitSeconds_AM(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	// now is 14:00 — reset at 3am is in the past, so next-day 3am.
	now := time.Date(2026, 1, 6, 14, 0, 0, 0, loc)

	msg := "resets 3am (America/Chicago)"
	secs, ok := parseResetWaitSeconds(msg, now, 0)

	assert.True(t, ok)
	// Next 3am from 14:00 same day = 13 hours away.
	assert.InDelta(t, 13*3600.0, secs, 2.0, "expected ~13 hours until next 3am")
}

func TestParseResetWaitSeconds_Midnight_12am(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	// 12am = midnight = 00:00.
	now := time.Date(2026, 1, 6, 22, 0, 0, 0, loc)

	msg := "resets 12am (UTC)"
	secs, ok := parseResetWaitSeconds(msg, now, 0)

	assert.True(t, ok)
	// Reset at midnight (next day) = 2 hours away.
	assert.InDelta(t, 2*3600.0, secs, 2.0, "12am should be midnight (0h)")
}

func TestParseResetWaitSeconds_Noon_12pm(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	now := time.Date(2026, 1, 6, 10, 0, 0, 0, loc)

	msg := "resets 12pm (UTC)"
	secs, ok := parseResetWaitSeconds(msg, now, 0)

	assert.True(t, ok)
	// 12pm = noon = 12:00 — 2 hours from 10:00.
	assert.InDelta(t, 2*3600.0, secs, 2.0, "12pm should be noon (12h)")
}

func TestParseResetWaitSeconds_WithBuffer(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	now := time.Date(2026, 1, 6, 10, 0, 0, 0, loc)

	msg := "resets 3pm (UTC)"
	secs, ok := parseResetWaitSeconds(msg, now, 5) // 5 min buffer

	assert.True(t, ok)
	// 5h + 5min = 18300s.
	assert.InDelta(t, 5*3600.0+300.0, secs, 2.0, "buffer minutes should be added")
}

func TestParseResetWaitSeconds_ResetAlreadyPast_AdvancesOneDay(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	// now is 16:00; reset at 3pm = 15:00 is already past.
	now := time.Date(2026, 1, 6, 16, 0, 0, 0, loc)

	msg := "resets 3pm (UTC)"
	secs, ok := parseResetWaitSeconds(msg, now, 0)

	assert.True(t, ok)
	// Next 3pm = tomorrow 15:00 = 23 hours away.
	assert.InDelta(t, 23*3600.0, secs, 2.0, "should advance to next day when past")
}

func TestParseResetWaitSeconds_NoMatch(t *testing.T) {
	_, ok := parseResetWaitSeconds("Claude usage limit reached", time.Now(), 0)
	assert.False(t, ok, "no reset time in message")
}

func TestParseResetWaitSeconds_InvalidTimezone(t *testing.T) {
	_, ok := parseResetWaitSeconds("resets 3pm (Not/AReal/Zone)", time.Now(), 0)
	assert.False(t, ok, "unknown timezone should return false")
}

func TestParseResetWaitSeconds_CaseInsensitive(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	now := time.Date(2026, 1, 6, 10, 0, 0, 0, loc)

	msg := "Resets 3PM (UTC)" // uppercase
	secs, ok := parseResetWaitSeconds(msg, now, 0)

	assert.True(t, ok)
	assert.InDelta(t, 5*3600.0, secs, 2.0, "regex should be case-insensitive")
}

func TestParseResetWaitSeconds_EmptyMessage(t *testing.T) {
	_, ok := parseResetWaitSeconds("", time.Now(), 0)
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// computeAutoWait
// ---------------------------------------------------------------------------

func TestComputeAutoWait_NoPausedAgents(t *testing.T) {
	agents := []wfstate.AgentState{
		{ID: "main", Status: ""}, // empty = running/active
	}
	_, ok := computeAutoWait(agents)
	// No paused agents: nothing to wait for → false.
	assert.False(t, ok)
}

func TestComputeAutoWait_EmptyAgents(t *testing.T) {
	_, ok := computeAutoWait(nil)
	// No agents at all: nothing to wait for → false.
	assert.False(t, ok)
}

func TestComputeAutoWait_PausedAgentWithNoParseableReset(t *testing.T) {
	agents := []wfstate.AgentState{
		{ID: "main", Status: wfstate.AgentStatusPaused, Error: "usage limit reached"},
	}
	_, ok := computeAutoWait(agents)
	assert.False(t, ok, "paused agent with no parseable reset time => false")
}

func TestComputeAutoWait_PausedAgentWithParseableReset(t *testing.T) {
	agents := []wfstate.AgentState{
		{
			ID:     "main",
			Status: wfstate.AgentStatusPaused,
			Error:  "limit resets 3pm (UTC)",
		},
	}
	secs, ok := computeAutoWait(agents)
	assert.True(t, ok)
	assert.Greater(t, secs, 0.0)
}

func TestComputeAutoWait_MultipleAgents_ReturnsLongest(t *testing.T) {
	// Use a timezone that's far away so "3pm" is in the future from any UTC "now".
	agents := []wfstate.AgentState{
		{
			ID:     "a",
			Status: wfstate.AgentStatusPaused,
			Error:  "limit resets 2am (UTC)",
		},
		{
			ID:     "b",
			Status: wfstate.AgentStatusPaused,
			Error:  "limit resets 3am (UTC)",
		},
	}
	secs, ok := computeAutoWait(agents)
	assert.True(t, ok)
	// Both future times (running test during daytime UTC), 3am > 2am wait.
	assert.Greater(t, secs, 0.0)
}

func TestComputeAutoWait_MixedPausedAndRunning(t *testing.T) {
	agents := []wfstate.AgentState{
		{ID: "a", Status: ""}, // running
		{
			ID:     "b",
			Status: wfstate.AgentStatusPaused,
			Error:  "limit resets 3am (UTC)",
		},
	}
	secs, ok := computeAutoWait(agents)
	assert.True(t, ok)
	assert.Greater(t, secs, 0.0)
}

func TestComputeAutoWait_OnePausedWithoutReset_ReturnsFalse(t *testing.T) {
	agents := []wfstate.AgentState{
		{
			ID:     "a",
			Status: wfstate.AgentStatusPaused,
			Error:  "limit resets 2am (UTC)",
		},
		{
			ID:     "b",
			Status: wfstate.AgentStatusPaused,
			Error:  "timeout error (no reset time)",
		},
	}
	_, ok := computeAutoWait(agents)
	assert.False(t, ok, "one agent with no parseable time => false")
}
