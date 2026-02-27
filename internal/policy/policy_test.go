package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/policy"
)

// ----------------------------------------------------------------------------
// ParseFrontmatter
// ----------------------------------------------------------------------------

func TestParseFrontmatterSimple(t *testing.T) {
	content := "---\nallowed_transitions:\n  - { tag: goto, target: REVIEW.md }\n  - { tag: goto, target: DONE.md }\n  - { tag: result }\n---\n# Prompt Content\nThis is the actual prompt."

	p, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Len(t, p.AllowedTransitions, 3)
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "goto", "target": "REVIEW.md"})
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "goto", "target": "DONE.md"})
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "result"})
	assert.Contains(t, body, "# Prompt Content")
	assert.Contains(t, body, "This is the actual prompt.")
}

func TestParseFrontmatterMultilineFormat(t *testing.T) {
	content := "---\nallowed_transitions:\n  - tag: goto\n    target: REVIEW.md\n  - tag: goto\n    target: DONE.md\n  - tag: result\n---\n# Prompt Content\nThis is the actual prompt."

	p, _, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Len(t, p.AllowedTransitions, 3)
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "goto", "target": "REVIEW.md"})
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "goto", "target": "DONE.md"})
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "result"})
}

func TestParseFrontmatterComplexCall(t *testing.T) {
	content := "---\nallowed_transitions:\n  - { tag: goto, target: NEXT.md }\n  - tag: call\n    target: RESEARCH.md\n    return: SUMMARIZE.md\n  - { tag: result }\n---\n# Complex Prompt\nThis prompt allows call transitions."

	p, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Len(t, p.AllowedTransitions, 3)
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "goto", "target": "NEXT.md"})
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "call", "target": "RESEARCH.md", "return": "SUMMARIZE.md"})
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "result"})
	assert.Contains(t, body, "Complex Prompt")
}

func TestParseFrontmatterNoFrontmatter(t *testing.T) {
	content := "# Plain Prompt\n\nThis has no frontmatter."

	p, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	assert.Nil(t, p)
	assert.Equal(t, content, body)
}

func TestParseFrontmatterEmptyFrontmatter(t *testing.T) {
	content := "---\n---\n# Prompt\nContent here."

	p, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	assert.Nil(t, p)
	assert.Contains(t, body, "# Prompt")
	assert.Contains(t, body, "Content here.")
}

func TestParseFrontmatterMixedFormat(t *testing.T) {
	content := "---\nallowed_transitions:\n  - { tag: goto, target: NEXT.md }\n  - tag: call\n    target: RESEARCH.md\n    return: SUMMARIZE.md\n  - { tag: result }\n---\n# Prompt\nContent."

	p, _, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Len(t, p.AllowedTransitions, 3)
}

func TestParseFrontmatterFunctionAndFork(t *testing.T) {
	content := "---\nallowed_transitions:\n  - tag: function\n    target: EVAL.md\n    return: NEXT.md\n  - tag: fork\n    target: WORKER.md\n    next: CONTINUE.md\n---\n# Prompt\nContent."

	p, _, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Len(t, p.AllowedTransitions, 2)
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "function", "target": "EVAL.md", "return": "NEXT.md"})
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "fork", "target": "WORKER.md", "next": "CONTINUE.md"})
}

func TestParseFrontmatterMalformedYAML(t *testing.T) {
	content := "---\nallowed_transitions: [goto\n---\n# Prompt\nContent."

	_, _, err := policy.ParseFrontmatter(content)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid YAML frontmatter")
}

func TestParseFrontmatterInvalidEntrySkipped(t *testing.T) {
	// Entry missing "tag" key should be silently skipped
	content := "---\nallowed_transitions:\n  - { tag: goto, target: NEXT.md }\n  - { target: MISSING_TAG.md }\n  - { tag: result }\n---\n# Prompt\nContent."

	p, _, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Len(t, p.AllowedTransitions, 2)
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "goto", "target": "NEXT.md"})
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "result"})
}

func TestParseFrontmatterWithModel(t *testing.T) {
	content := "---\nmodel: haiku\nallowed_transitions:\n  - { tag: goto, target: NEXT.md }\n---\n# Prompt Content\nThis is the prompt."

	p, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "haiku", p.Model)
	assert.Contains(t, body, "# Prompt Content")
}

func TestParseFrontmatterModelNormalizedToLowercase(t *testing.T) {
	content := "---\nmodel: OpUs\nallowed_transitions:\n  - { tag: goto, target: NEXT.md }\n---\n# Prompt"

	p, _, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "opus", p.Model)
}

func TestParseFrontmatterModelStripsWhitespace(t *testing.T) {
	content := "---\nmodel: \" sonnet \"\nallowed_transitions:\n  - { tag: goto, target: NEXT.md }\n---\n# Prompt"

	p, _, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "sonnet", p.Model)
}

func TestParseFrontmatterEmptyModelTreatedAsEmpty(t *testing.T) {
	content := "---\nmodel: \"\"\nallowed_transitions:\n  - { tag: goto, target: NEXT.md }\n---\n# Prompt"

	p, _, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "", p.Model)
}

func TestParseFrontmatterCRLFLineEndings(t *testing.T) {
	// Files checked out on Windows have \r\n line endings. The parser must
	// normalize them so the frontmatter regex matches correctly.
	content := "---\r\nallowed_transitions:\r\n  - { tag: goto, target: NEXT.md }\r\n---\r\n# Prompt\r\nContent."

	p, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Len(t, p.AllowedTransitions, 1)
	assert.Contains(t, p.AllowedTransitions, map[string]string{"tag": "goto", "target": "NEXT.md"})
	assert.Contains(t, body, "# Prompt")
	assert.True(t, policy.CanUseImplicitTransition(p))
}

// ----------------------------------------------------------------------------
// ValidateTransitionPolicy
// ----------------------------------------------------------------------------

func TestValidateAllowedTransitionPasses(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "goto", "target": "NEXT.md"},
			{"tag": "result"},
		},
	}
	tr := parsing.Transition{Tag: "goto", Target: "NEXT.md"}
	err := policy.ValidateTransitionPolicy(tr, p)
	assert.NoError(t, err)
}

func TestValidateDisallowedTagErrors(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "goto", "target": "NEXT.md"},
			{"tag": "result"},
		},
	}
	tr := parsing.Transition{Tag: "fork", Target: "WORKER.md"}
	err := policy.ValidateTransitionPolicy(tr, p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fork")
	assert.Contains(t, err.Error(), "not allowed")
}

func TestValidateDisallowedTargetErrors(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "goto", "target": "NEXT.md"},
			{"tag": "goto", "target": "DONE.md"},
		},
	}
	tr := parsing.Transition{Tag: "goto", Target: "OTHER.md"}
	err := policy.ValidateTransitionPolicy(tr, p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
}

func TestValidateResultTagNoTargetRestriction(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "result"},
		},
	}
	tr := parsing.Transition{Tag: "result", Payload: "Done"}
	err := policy.ValidateTransitionPolicy(tr, p)
	assert.NoError(t, err)
}

func TestValidateCallWithStructuredAttributes(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "call", "target": "RESEARCH.md", "return": "SUMMARIZE.md"},
		},
	}
	tr := parsing.Transition{
		Tag:        "call",
		Target:     "RESEARCH.md",
		Attributes: map[string]string{"return": "SUMMARIZE.md"},
	}
	err := policy.ValidateTransitionPolicy(tr, p)
	assert.NoError(t, err)
}

func TestValidateCallWithWrongTargetErrors(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "call", "target": "RESEARCH.md", "return": "SUMMARIZE.md"},
		},
	}
	tr := parsing.Transition{
		Tag:        "call",
		Target:     "WRONG.md",
		Attributes: map[string]string{"return": "SUMMARIZE.md"},
	}
	err := policy.ValidateTransitionPolicy(tr, p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
}

func TestValidateCallWithWrongReturnErrors(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "call", "target": "RESEARCH.md", "return": "SUMMARIZE.md"},
		},
	}
	tr := parsing.Transition{
		Tag:        "call",
		Target:     "RESEARCH.md",
		Attributes: map[string]string{"return": "WRONG.md"},
	}
	err := policy.ValidateTransitionPolicy(tr, p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
}

func TestValidateNilPolicyAllowsAll(t *testing.T) {
	tr := parsing.Transition{Tag: "goto", Target: "ANY.md"}
	err := policy.ValidateTransitionPolicy(tr, nil)
	assert.NoError(t, err)
}

func TestValidateEmptyPolicyAllowsAll(t *testing.T) {
	p := &policy.Policy{AllowedTransitions: []map[string]string{}}
	tr := parsing.Transition{Tag: "goto", Target: "ANY.md"}
	err := policy.ValidateTransitionPolicy(tr, p)
	assert.NoError(t, err)
}

func TestValidateFunctionWithReturnAttribute(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "function", "target": "EVAL.md", "return": "NEXT.md"},
		},
	}
	tr := parsing.Transition{
		Tag:        "function",
		Target:     "EVAL.md",
		Attributes: map[string]string{"return": "NEXT.md"},
	}
	err := policy.ValidateTransitionPolicy(tr, p)
	assert.NoError(t, err)
}

func TestValidateForkWithNextAttribute(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "fork", "target": "WORKER.md", "next": "CONTINUE.md"},
		},
	}
	// Extra attribute "item" is metadata — not validated by policy
	tr := parsing.Transition{
		Tag:        "fork",
		Target:     "WORKER.md",
		Attributes: map[string]string{"next": "CONTINUE.md", "item": "data"},
	}
	err := policy.ValidateTransitionPolicy(tr, p)
	assert.NoError(t, err)
}

func TestValidateForkWithWrongNextErrors(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "fork", "target": "WORKER.md", "next": "CONTINUE.md"},
		},
	}
	tr := parsing.Transition{
		Tag:        "fork",
		Target:     "WORKER.md",
		Attributes: map[string]string{"next": "WRONG.md"},
	}
	err := policy.ValidateTransitionPolicy(tr, p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
}

func TestValidateMultipleAllowedCombinations(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "goto", "target": "NEXT.md"},
			{"tag": "goto", "target": "DONE.md"},
			{"tag": "reset", "target": "START.md"},
			{"tag": "result"},
		},
	}
	cases := []parsing.Transition{
		{Tag: "goto", Target: "NEXT.md"},
		{Tag: "goto", Target: "DONE.md"},
		{Tag: "reset", Target: "START.md"},
		{Tag: "result"},
	}
	for _, tr := range cases {
		assert.NoError(t, policy.ValidateTransitionPolicy(tr, p), "transition %+v should be allowed", tr)
	}
}

func TestPolicyViolationErrorType(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "goto", "target": "NEXT.md"},
		},
	}
	tr := parsing.Transition{Tag: "fork", Target: "WORKER.md"}
	err := policy.ValidateTransitionPolicy(tr, p)
	require.Error(t, err)
	var pve *policy.PolicyViolationError
	assert.ErrorAs(t, err, &pve)
}

// ----------------------------------------------------------------------------
// TargetsMatch (abstract target matching)
// ----------------------------------------------------------------------------

func TestTargetsMatchAbstractMatchesMDExtension(t *testing.T) {
	assert.True(t, policy.TargetsMatch("COUNT", "COUNT.md"))
}

func TestTargetsMatchAbstractMatchesSHExtension(t *testing.T) {
	assert.True(t, policy.TargetsMatch("COUNT", "COUNT.sh"))
}

func TestTargetsMatchAbstractMatchesBATExtension(t *testing.T) {
	assert.True(t, policy.TargetsMatch("COUNT", "COUNT.bat"))
}

func TestTargetsMatchExplicitRequiresExactMatch(t *testing.T) {
	assert.False(t, policy.TargetsMatch("COUNT.md", "COUNT.sh"))
}

func TestTargetsMatchAbstractDoesNotMatchDifferentStem(t *testing.T) {
	assert.False(t, policy.TargetsMatch("COUNT", "OTHER.md"))
}

func TestTargetsMatchExactWithExtension(t *testing.T) {
	assert.True(t, policy.TargetsMatch("COUNT.md", "COUNT.md"))
	assert.False(t, policy.TargetsMatch("COUNT.md", "COUNT.sh"))
	assert.False(t, policy.TargetsMatch("COUNT.md", "OTHER.md"))
}

func TestTargetsMatchAbstractDoesNotMatchInvalidExtension(t *testing.T) {
	assert.False(t, policy.TargetsMatch("COUNT", "COUNT.py"))
	assert.False(t, policy.TargetsMatch("COUNT", "COUNT.txt"))
}

// Case sensitivity: the extension check is case-insensitive (.MD matches .md),
// but the stem comparison is case-sensitive. Policy targets and resolved
// filenames must use the same stem case to match.
func TestTargetsMatchMixedCaseExtension(t *testing.T) {
	// Extension case is normalized — ".MD", ".SH", ".BAT" all match.
	assert.True(t, policy.TargetsMatch("COUNT", "COUNT.MD"))
	assert.True(t, policy.TargetsMatch("COUNT", "COUNT.SH"))
	assert.True(t, policy.TargetsMatch("COUNT", "COUNT.BAT"))
}

func TestTargetsMatchStemCaseSensitive(t *testing.T) {
	// Stem comparison is case-sensitive; different cases do not match.
	// On case-insensitive filesystems (macOS, Windows), file resolution
	// will normalize the stem before it reaches policy validation, but
	// the policy itself does not perform case folding.
	assert.False(t, policy.TargetsMatch("count", "COUNT.md"))
	assert.True(t, policy.TargetsMatch("count", "count.MD"))
}

func TestAbstractReturnAttributeMatchesResolved(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "call", "target": "RESEARCH", "return": "SUMMARIZE"},
		},
	}
	tr := parsing.Transition{
		Tag:        "call",
		Target:     "RESEARCH.md",
		Attributes: map[string]string{"return": "SUMMARIZE.sh"},
	}
	err := policy.ValidateTransitionPolicy(tr, p)
	assert.NoError(t, err)
}

func TestAbstractNextAttributeMatchesResolved(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "fork", "target": "WORKER", "next": "CONTINUE"},
		},
	}
	tr := parsing.Transition{
		Tag:        "fork",
		Target:     "WORKER.bat",
		Attributes: map[string]string{"next": "CONTINUE.md"},
	}
	err := policy.ValidateTransitionPolicy(tr, p)
	assert.NoError(t, err)
}

// ----------------------------------------------------------------------------
// ShouldUseReminderPrompt
// ----------------------------------------------------------------------------

func TestShouldUseReminderNilPolicy(t *testing.T) {
	assert.False(t, policy.ShouldUseReminderPrompt(nil))
}

func TestShouldUseReminderEmptyTransitions(t *testing.T) {
	p := &policy.Policy{AllowedTransitions: []map[string]string{}}
	assert.False(t, policy.ShouldUseReminderPrompt(p))
}

func TestShouldUseReminderNonEmptyTransitions(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{{"tag": "goto", "target": "NEXT.md"}},
	}
	assert.True(t, policy.ShouldUseReminderPrompt(p))
}

// ----------------------------------------------------------------------------
// CanUseImplicitTransition / GetImplicitTransition
// ----------------------------------------------------------------------------

func TestCanUseImplicitSingleGoto(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "goto", "target": "NEXT.md"},
		},
	}
	assert.True(t, policy.CanUseImplicitTransition(p))

	tr, err := policy.GetImplicitTransition(p)
	require.NoError(t, err)
	assert.Equal(t, "goto", tr.Tag)
	assert.Equal(t, "NEXT.md", tr.Target)
	assert.Empty(t, tr.Attributes)
	assert.Equal(t, "", tr.Payload)
}

func TestCanUseImplicitSingleCall(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "call", "target": "RESEARCH.md", "return": "SUMMARIZE.md"},
		},
	}
	assert.True(t, policy.CanUseImplicitTransition(p))

	tr, err := policy.GetImplicitTransition(p)
	require.NoError(t, err)
	assert.Equal(t, "call", tr.Tag)
	assert.Equal(t, "RESEARCH.md", tr.Target)
	assert.Equal(t, map[string]string{"return": "SUMMARIZE.md"}, tr.Attributes)
}

func TestCanUseImplicitSingleFunction(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "function", "target": "EVAL.md", "return": "NEXT.md"},
		},
	}
	assert.True(t, policy.CanUseImplicitTransition(p))

	tr, err := policy.GetImplicitTransition(p)
	require.NoError(t, err)
	assert.Equal(t, "function", tr.Tag)
	assert.Equal(t, "EVAL.md", tr.Target)
	assert.Equal(t, map[string]string{"return": "NEXT.md"}, tr.Attributes)
}

func TestCanUseImplicitSingleFork(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "fork", "target": "WORKER.md", "next": "CONTINUE.md"},
		},
	}
	assert.True(t, policy.CanUseImplicitTransition(p))

	tr, err := policy.GetImplicitTransition(p)
	require.NoError(t, err)
	assert.Equal(t, "fork", tr.Tag)
	assert.Equal(t, "WORKER.md", tr.Target)
	assert.Equal(t, map[string]string{"next": "CONTINUE.md"}, tr.Attributes)
}

func TestCannotUseImplicitResultTag(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{{"tag": "result"}},
	}
	assert.False(t, policy.CanUseImplicitTransition(p))
}

func TestCannotUseImplicitMultipleAllowed(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "goto", "target": "NEXT.md"},
			{"tag": "goto", "target": "DONE.md"},
		},
	}
	assert.False(t, policy.CanUseImplicitTransition(p))
}

func TestCannotUseImplicitNilPolicy(t *testing.T) {
	assert.False(t, policy.CanUseImplicitTransition(nil))
}

func TestCannotUseImplicitEmptyPolicy(t *testing.T) {
	p := &policy.Policy{AllowedTransitions: []map[string]string{}}
	assert.False(t, policy.CanUseImplicitTransition(p))
}

func TestGetImplicitTransitionErrorsWhenNotApplicable(t *testing.T) {
	// Multiple transitions
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "goto", "target": "NEXT.md"},
			{"tag": "goto", "target": "DONE.md"},
		},
	}
	_, err := policy.GetImplicitTransition(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot get implicit transition")

	// Result tag
	p2 := &policy.Policy{AllowedTransitions: []map[string]string{{"tag": "result"}}}
	_, err = policy.GetImplicitTransition(p2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot get implicit transition")

	// Nil policy
	_, err = policy.GetImplicitTransition(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot get implicit transition")
}

// ----------------------------------------------------------------------------
// GenerateReminderPrompt
// ----------------------------------------------------------------------------

func TestGenerateReminderNilPolicyErrors(t *testing.T) {
	_, err := policy.GenerateReminderPrompt(nil)
	require.Error(t, err)
}

func TestGenerateReminderEmptyPolicyErrors(t *testing.T) {
	p := &policy.Policy{AllowedTransitions: []map[string]string{}}
	_, err := policy.GenerateReminderPrompt(p)
	require.Error(t, err)
}

func TestGenerateReminderContainsExpectedElements(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "goto", "target": "NEXT.md"},
			{"tag": "result"},
		},
	}
	out, err := policy.GenerateReminderPrompt(p)
	require.NoError(t, err)
	assert.Contains(t, out, "REMINDER")
	assert.Contains(t, out, "<goto>NEXT.md</goto>")
	assert.Contains(t, out, "<result>...</result>")
	assert.Contains(t, out, "---")
}

func TestGenerateReminderWithAttributes(t *testing.T) {
	p := &policy.Policy{
		AllowedTransitions: []map[string]string{
			{"tag": "call", "target": "RESEARCH.md", "return": "SUMMARIZE.md"},
		},
	}
	out, err := policy.GenerateReminderPrompt(p)
	require.NoError(t, err)
	assert.Contains(t, out, "call")
	assert.Contains(t, out, "RESEARCH.md")
	assert.Contains(t, out, "return=")
	assert.Contains(t, out, "SUMMARIZE.md")
}
