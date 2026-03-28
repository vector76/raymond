package convert

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/parsing"
)

func TestBfsDistances_LinearChain(t *testing.T) {
	// A→B→C via goto
	transitions := map[string][]parsing.Transition{
		"A": {{Tag: "goto", Target: "B.md"}},
		"B": {{Tag: "goto", Target: "C.md"}},
		"C": {},
	}
	dist := bfsDistances("A", transitions)
	assert.Equal(t, 0, dist["A"])
	assert.Equal(t, 1, dist["B"])
	assert.Equal(t, 2, dist["C"])
	assert.Len(t, dist, 3)
}

func TestBfsDistances_Diamond(t *testing.T) {
	// A→B, A→C, B→D, C→D (all goto) → D=2
	transitions := map[string][]parsing.Transition{
		"A": {
			{Tag: "goto", Target: "B.md"},
			{Tag: "goto", Target: "C.md"},
		},
		"B": {{Tag: "goto", Target: "D.md"}},
		"C": {{Tag: "goto", Target: "D.md"}},
		"D": {},
	}
	dist := bfsDistances("A", transitions)
	assert.Equal(t, 0, dist["A"])
	assert.Equal(t, 1, dist["B"])
	assert.Equal(t, 1, dist["C"])
	assert.Equal(t, 2, dist["D"])
}

func TestBfsDistances_CallResult(t *testing.T) {
	// S1 calls S2 (return S3), S2→S4 (goto), S4 has result
	// → {S1:0, S2:1, S4:2, S3:3}
	transitions := map[string][]parsing.Transition{
		"S1": {{Tag: "call", Target: "S2.md", Attributes: map[string]string{"return": "S3.md"}}},
		"S2": {{Tag: "goto", Target: "S4.md"}},
		"S4": {{Tag: "result", Payload: "done"}},
		"S3": {},
	}
	dist := bfsDistances("S1", transitions)
	require.Contains(t, dist, "S1")
	require.Contains(t, dist, "S2")
	require.Contains(t, dist, "S4")
	require.Contains(t, dist, "S3")
	assert.Equal(t, 0, dist["S1"])
	assert.Equal(t, 1, dist["S2"])
	assert.Equal(t, 2, dist["S4"])
	assert.Equal(t, 3, dist["S3"])
}

func TestBfsDistances_DisconnectedState(t *testing.T) {
	// X has transitions but is not reachable from A
	transitions := map[string][]parsing.Transition{
		"A": {{Tag: "goto", Target: "B.md"}},
		"B": {},
		"X": {{Tag: "goto", Target: "B.md"}},
	}
	dist := bfsDistances("A", transitions)
	assert.Contains(t, dist, "A")
	assert.Contains(t, dist, "B")
	assert.NotContains(t, dist, "X")
}

func TestBfsDistances_Cycle(t *testing.T) {
	// A→B→A (goto)
	transitions := map[string][]parsing.Transition{
		"A": {{Tag: "goto", Target: "B.md"}},
		"B": {{Tag: "goto", Target: "A.md"}},
	}
	dist := bfsDistances("A", transitions)
	assert.Equal(t, 0, dist["A"])
	assert.Equal(t, 1, dist["B"])
	assert.Len(t, dist, 2)
}

func TestBfsDistances_ForkWithNext(t *testing.T) {
	// A forks to B with next=C
	transitions := map[string][]parsing.Transition{
		"A": {{Tag: "fork", Target: "B.md", Attributes: map[string]string{"next": "C.md"}}},
		"B": {},
		"C": {},
	}
	dist := bfsDistances("A", transitions)
	assert.Equal(t, 0, dist["A"])
	assert.Equal(t, 1, dist["B"])
	assert.Equal(t, 1, dist["C"])
}

func TestBfsDistances_NestedCalls(t *testing.T) {
	// S1 calls S2 (return S5), S2 calls S3 (return S4), S3 has result, S4 has result.
	// Inner call: goto/reset reachable from S3 = {S3}. S3 has result → edge S3→S4.
	// Outer call: goto/reset reachable from S2 = {S2}. S2 has no result → no edge to S5.
	// Nested calls don't descend into further call targets, so S5 is unreachable.
	transitions := map[string][]parsing.Transition{
		"S1": {{Tag: "call", Target: "S2.md", Attributes: map[string]string{"return": "S5.md"}}},
		"S2": {{Tag: "call", Target: "S3.md", Attributes: map[string]string{"return": "S4.md"}}},
		"S3": {{Tag: "result", Payload: "inner result"}},
		"S4": {{Tag: "result", Payload: "outer result"}},
		"S5": {},
	}
	dist := bfsDistances("S1", transitions)
	assert.Equal(t, 0, dist["S1"])
	assert.Equal(t, 1, dist["S2"])
	assert.Equal(t, 2, dist["S3"])
	assert.Equal(t, 3, dist["S4"])
	assert.NotContains(t, dist, "S5", "S5 is not reachable: outer call's goto/reset subgraph has no result emitter")
}

func TestBfsDistances_CrossWorkflowWithReturn(t *testing.T) {
	// source has call-workflow with return=LOCAL
	transitions := map[string][]parsing.Transition{
		"SOURCE": {{
			Tag:        "call-workflow",
			Target:     "workflows/sub.zip",
			Attributes: map[string]string{"return": "LOCAL.md"},
		}},
		"LOCAL": {},
	}
	dist := bfsDistances("SOURCE", transitions)
	assert.Equal(t, 0, dist["SOURCE"])
	assert.Contains(t, dist, "LOCAL")
	assert.Contains(t, dist, "workflows/sub.zip")
	assert.Equal(t, 1, dist["LOCAL"])
	assert.Equal(t, 1, dist["workflows/sub.zip"])
}
