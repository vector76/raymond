package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vector76/raymond/internal/lint"
)

func severityLabel(s lint.Severity) string {
	switch s {
	case lint.Error:
		return "error"
	case lint.Warning:
		return "warning"
	case lint.Info:
		return "info"
	default:
		return "unknown"
	}
}

// parseLintLevel maps a string to a lint.Severity value.
func parseLintLevel(s string) (lint.Severity, error) {
	switch s {
	case "error":
		return lint.Error, nil
	case "warning":
		return lint.Warning, nil
	case "info":
		return lint.Info, nil
	default:
		return 0, fmt.Errorf("unknown lint level %q: must be error, warning, or info", s)
	}
}

// formatLintText returns a human-readable string for the diagnostics.
// Only diagnostics with Severity <= level are printed. The summary counts all
// diagnostics regardless of level filter.
func formatLintText(diags []lint.Diagnostic, level lint.Severity) string {
	if len(diags) == 0 {
		return "No issues found.\n"
	}

	var sb strings.Builder

	for _, d := range diags {
		if d.Severity <= level {
			fmt.Fprintf(&sb, "%s: %s\n", severityLabel(d.Severity), d.Message)
		}
	}

	// Summary counts all diagnostics (not filtered).
	counts := [3]int{}
	for _, d := range diags {
		if d.Severity >= 0 && int(d.Severity) < len(counts) {
			counts[d.Severity]++
		}
	}

	labels := []string{"error", "warning", "info"}
	var parts []string
	for i, count := range counts {
		if count == 0 {
			continue
		}
		label := labels[i]
		if i == 0 && count != 1 {
			label = "errors"
		} else if i == 1 && count != 1 {
			label = "warnings"
		}
		// "info" does not pluralize
		parts = append(parts, fmt.Sprintf("%d %s", count, label))
	}
	sb.WriteString(strings.Join(parts, ", ") + "\n")

	return sb.String()
}

// jsonDiagnostic is the JSON representation of a Diagnostic.
type jsonDiagnostic struct {
	Severity string `json:"severity"`
	File     string `json:"file"`
	Message  string `json:"message"`
	Check    string `json:"check"`
}

// formatLintJSON returns a JSON array of diagnostics filtered to those at or above level.
func formatLintJSON(diags []lint.Diagnostic, level lint.Severity) (string, error) {
	filtered := make([]jsonDiagnostic, 0, len(diags))
	for _, d := range diags {
		if d.Severity <= level {
			filtered = append(filtered, jsonDiagnostic{
				Severity: severityLabel(d.Severity),
				File:     d.File,
				Message:  d.Message,
				Check:    d.Check,
			})
		}
	}
	b, err := json.Marshal(filtered)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
