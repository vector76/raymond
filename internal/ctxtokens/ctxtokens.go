// Package ctxtokens defines the single source of truth for "how full is the
// context" — the token count implied by a Claude usage object.
//
// Two callers must agree on this definition or the live and on-disk views of
// the same session would disagree:
//
//   - internal/backend reads the usage object off the live claude stream after
//     each turn (feeds {{context_tokens}} / RAYMOND_CONTEXT_TOKENS).
//   - internal/ccsession reads the usage object off the on-disk transcript
//     (feeds `ray --claude_context_for_session`).
//
// Both call FromUsage so the two numbers are identical for the same turn.
package ctxtokens

// usageKeys are the usage fields that together make up the context size going
// into the next turn: every input-side field (the prompt, including cached
// prefix) PLUS the turn's own output, which becomes part of the conversation
// the next turn must re-read. Omitting output_tokens would undercount the
// context by one assistant message.
var usageKeys = []string{
	"input_tokens",
	"cache_creation_input_tokens",
	"cache_read_input_tokens",
	"output_tokens",
}

// FromUsage returns the context-token total implied by a Claude usage object
// (the value of message.usage on a transcript line, or the top-level usage on
// a stream result object). Missing or non-numeric fields contribute zero.
//
// JSON-decoded numbers arrive as float64; int/int64 are accepted too so the
// helper is robust to callers that build usage maps from native integers.
func FromUsage(usage map[string]any) int64 {
	var sum int64
	for _, key := range usageKeys {
		sum += asInt64(usage[key])
	}
	return sum
}

// asInt64 coerces a JSON-decoded numeric value to int64, returning 0 for nil
// or any non-numeric type.
func asInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	default:
		return 0
	}
}
