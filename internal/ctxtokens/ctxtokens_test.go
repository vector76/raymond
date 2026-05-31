package ctxtokens_test

import (
	"testing"

	"github.com/vector76/raymond/internal/ctxtokens"
)

func TestFromUsageSumsInputSideAndOutput(t *testing.T) {
	usage := map[string]any{
		"input_tokens":                float64(3),
		"cache_creation_input_tokens": float64(6448),
		"cache_read_input_tokens":     float64(12881),
		"output_tokens":               float64(161),
	}
	got := ctxtokens.FromUsage(usage)
	want := int64(3 + 6448 + 12881 + 161)
	if got != want {
		t.Errorf("FromUsage = %d, want %d", got, want)
	}
}

func TestFromUsageOutputIsIncluded(t *testing.T) {
	// Output tokens are part of the context: leaving them out is an error.
	withOutput := ctxtokens.FromUsage(map[string]any{
		"input_tokens":  float64(100),
		"output_tokens": float64(40),
	})
	if withOutput != 140 {
		t.Errorf("FromUsage = %d, want 140 (input+output)", withOutput)
	}
}

func TestFromUsageMissingFieldsAreZero(t *testing.T) {
	got := ctxtokens.FromUsage(map[string]any{"input_tokens": float64(50)})
	if got != 50 {
		t.Errorf("FromUsage = %d, want 50", got)
	}
}

func TestFromUsageEmpty(t *testing.T) {
	if got := ctxtokens.FromUsage(map[string]any{}); got != 0 {
		t.Errorf("FromUsage(empty) = %d, want 0", got)
	}
	if got := ctxtokens.FromUsage(nil); got != 0 {
		t.Errorf("FromUsage(nil) = %d, want 0", got)
	}
}

func TestFromUsageAcceptsIntTypes(t *testing.T) {
	got := ctxtokens.FromUsage(map[string]any{
		"input_tokens":  int(10),
		"output_tokens": int64(5),
	})
	if got != 15 {
		t.Errorf("FromUsage = %d, want 15", got)
	}
}

func TestFromUsageIgnoresNonNumeric(t *testing.T) {
	got := ctxtokens.FromUsage(map[string]any{
		"input_tokens":  float64(10),
		"output_tokens": "not-a-number",
	})
	if got != 10 {
		t.Errorf("FromUsage = %d, want 10 (non-numeric ignored)", got)
	}
}
