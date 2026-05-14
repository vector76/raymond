package backend

import (
	"errors"
	"strings"
	"testing"
)

// TestTimeoutError_Idle verifies the idle-timeout message variant.
func TestTimeoutError_Idle(t *testing.T) {
	e := &TimeoutError{Timeout: 1.5, Idle: true}
	if !strings.Contains(e.Error(), "idle timeout") {
		t.Errorf("want idle message, got %q", e.Error())
	}
}

// TestTimeoutError_Total verifies the total-timeout message variant.
func TestTimeoutError_Total(t *testing.T) {
	e := &TimeoutError{Timeout: 30, Idle: false}
	if !strings.Contains(e.Error(), "timed out after") {
		t.Errorf("want total-timeout message, got %q", e.Error())
	}
	if strings.Contains(e.Error(), "idle") {
		t.Errorf("total timeout message should not mention 'idle': %q", e.Error())
	}
}

// TestLimitError_AsTarget confirms errors.As recognises *LimitError so
// the executor can map it back to ClaudeCodeLimitError without losing
// type information.
func TestLimitError_AsTarget(t *testing.T) {
	var le *LimitError
	if !errors.As(error(&LimitError{Msg: "limit hit"}), &le) {
		t.Fatal("direct *LimitError should match")
	}
	if le.Msg != "limit hit" {
		t.Errorf("captured msg %q, want %q", le.Msg, "limit hit")
	}
}

// TestRunError_Message is a basic sanity check on the RunError shape.
func TestRunError_Message(t *testing.T) {
	e := &RunError{Msg: "boom"}
	if e.Error() != "boom" {
		t.Errorf("got %q, want %q", e.Error(), "boom")
	}
}
