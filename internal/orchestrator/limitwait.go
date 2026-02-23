package orchestrator

import (
	"regexp"
	"strings"
	"time"
)

// resetTimeRe matches "resets 3pm (America/Chicago)" style strings from
// Claude Code usage-limit error messages.
var resetTimeRe = regexp.MustCompile(`(?i)resets\s+(\d{1,2}(?:am|pm))\s+\(([^)]+)\)`)

// parseResetWaitSeconds parses a usage-limit reset time from msg and returns
// the seconds to wait (including bufferMinutes) plus true.
//
// Returns (0, false) when msg doesn't contain a parseable reset time or the
// timezone name is unknown.
func parseResetWaitSeconds(msg string, now time.Time, bufferMinutes int) (float64, bool) {
	m := resetTimeRe.FindStringSubmatch(msg)
	if m == nil {
		return 0, false
	}
	timeStr := strings.ToLower(m[1]) // e.g. "3pm", "12am"
	tzName := m[2]                   // e.g. "America/Chicago"

	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return 0, false
	}

	// Parse hour.
	var hour int
	if strings.HasSuffix(timeStr, "am") {
		h, err := parseHourStr(timeStr[:len(timeStr)-2])
		if err != nil {
			return 0, false
		}
		hour = h
		if hour == 12 {
			hour = 0 // 12am = midnight
		}
	} else if strings.HasSuffix(timeStr, "pm") {
		h, err := parseHourStr(timeStr[:len(timeStr)-2])
		if err != nil {
			return 0, false
		}
		hour = h
		if hour != 12 {
			hour += 12
		}
	} else {
		return 0, false
	}
	if hour < 0 || hour > 23 {
		return 0, false
	}

	// Build reset time in the stated timezone.
	nowInZone := now.In(loc)
	reset := time.Date(nowInZone.Year(), nowInZone.Month(), nowInZone.Day(),
		hour, 0, 0, 0, loc)
	if !reset.After(nowInZone) {
		reset = reset.AddDate(0, 0, 1)
	}

	target := reset.Add(time.Duration(bufferMinutes) * time.Minute)
	secs := target.Sub(now).Seconds()
	if secs < 0 {
		secs = 0
	}
	return secs, true
}

// parseHourStr converts a numeric string to int, returning an error for
// non-numeric input.
func parseHourStr(s string) (int, error) {
	var h int
	_, err := scanInt(s, &h)
	return h, err
}

// scanInt is a minimal integer parser to avoid importing "strconv" twice.
func scanInt(s string, out *int) (int, error) {
	if len(s) == 0 {
		return 0, errBadInt
	}
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errBadInt
		}
		v = v*10 + int(c-'0')
	}
	*out = v
	return v, nil
}

// errBadInt is a sentinel for parse failures.
var errBadInt = &badIntError{}

type badIntError struct{}

func (e *badIntError) Error() string { return "bad integer" }
