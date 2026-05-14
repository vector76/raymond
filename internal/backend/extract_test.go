package backend

import "testing"

// --------------------------------------------------------------------------
// extractClaudeCost edge cases
// --------------------------------------------------------------------------

func TestExtractClaudeCost_Empty(t *testing.T) {
	if got := extractClaudeCost(nil); got != 0.0 {
		t.Errorf("nil results: got %v, want 0.0", got)
	}
	if got := extractClaudeCost([]map[string]any{}); got != 0.0 {
		t.Errorf("empty slice: got %v, want 0.0", got)
	}
}

func TestExtractClaudeCost_MissingKey(t *testing.T) {
	results := []map[string]any{
		{"type": "assistant", "message": "hello"},
	}
	if got := extractClaudeCost(results); got != 0.0 {
		t.Errorf("missing key: got %v, want 0.0", got)
	}
}

func TestExtractClaudeCost_NonNumericValue(t *testing.T) {
	results := []map[string]any{
		{"total_cost_usd": "not-a-number"},
	}
	if got := extractClaudeCost(results); got != 0.0 {
		t.Errorf("non-numeric value: got %v, want 0.0", got)
	}
}

func TestExtractClaudeCost_UsesLastEntry(t *testing.T) {
	results := []map[string]any{
		{"total_cost_usd": 0.01},
		{"total_cost_usd": 0.05},
	}
	if got := extractClaudeCost(results); got != 0.05 {
		t.Errorf("should use last entry: got %v, want 0.05", got)
	}
}

func TestExtractClaudeCost_IntValue(t *testing.T) {
	results := []map[string]any{
		{"total_cost_usd": int(3)},
	}
	if got := extractClaudeCost(results); got != 3.0 {
		t.Errorf("int value: got %v, want 3.0", got)
	}
}

// --------------------------------------------------------------------------
// extractClaudeTokens edge cases
// --------------------------------------------------------------------------

func TestExtractClaudeTokens(t *testing.T) {
	t.Run("all three fields present", func(t *testing.T) {
		results := []map[string]any{{
			"usage": map[string]any{
				"cache_creation_input_tokens": float64(100),
				"cache_read_input_tokens":     float64(200),
				"input_tokens":                float64(300),
			},
		}}
		got := extractClaudeTokens(results)
		if got == nil {
			t.Fatal("expected non-nil pointer, got nil")
		}
		if *got != 600 {
			t.Errorf("got %d, want 600", *got)
		}
	})

	t.Run("only some fields present", func(t *testing.T) {
		results := []map[string]any{{
			"usage": map[string]any{"input_tokens": float64(50)},
		}}
		got := extractClaudeTokens(results)
		if got == nil {
			t.Fatal("expected non-nil pointer, got nil")
		}
		if *got != 50 {
			t.Errorf("got %d, want 50", *got)
		}
	})

	t.Run("last usage wins among multiple results", func(t *testing.T) {
		results := []map[string]any{
			{"usage": map[string]any{"input_tokens": float64(999)}},
			{"usage": map[string]any{"input_tokens": float64(42)}},
		}
		got := extractClaudeTokens(results)
		if got == nil {
			t.Fatal("expected non-nil pointer, got nil")
		}
		if *got != 42 {
			t.Errorf("got %d, want 42 (last entry should win)", *got)
		}
	})

	t.Run("no usage field returns nil", func(t *testing.T) {
		results := []map[string]any{
			{"total_cost_usd": float64(1.5)},
		}
		if got := extractClaudeTokens(results); got != nil {
			t.Errorf("expected nil, got pointer to %d", *got)
		}
	})

	t.Run("usage is not an object returns nil", func(t *testing.T) {
		if got := extractClaudeTokens([]map[string]any{{"usage": "not-an-object"}}); got != nil {
			t.Errorf("expected nil for string usage, got pointer to %d", *got)
		}
		if got := extractClaudeTokens([]map[string]any{{"usage": float64(42)}}); got != nil {
			t.Errorf("expected nil for numeric usage, got pointer to %d", *got)
		}
	})

	t.Run("all fields zero returns non-nil pointer to 0", func(t *testing.T) {
		results := []map[string]any{{
			"usage": map[string]any{
				"cache_creation_input_tokens": float64(0),
				"cache_read_input_tokens":     float64(0),
				"input_tokens":                float64(0),
			},
		}}
		got := extractClaudeTokens(results)
		if got == nil {
			t.Fatal("expected non-nil pointer to 0, got nil")
		}
		if *got != 0 {
			t.Errorf("got %d, want 0", *got)
		}
	})
}
