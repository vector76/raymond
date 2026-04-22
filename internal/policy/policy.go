package policy

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/vector76/raymond/internal/parsing"
)

// stateExtensions lists the file extensions recognized as valid state targets.
var stateExtensions = map[string]bool{
	".md":  true,
	".sh":  true,
	".bat": true,
	".ps1": true,
}

// Frontmatter delimiter patterns anchored to the start of content.
// Go RE2 supports (?s) to make . match newlines.
var (
	frontmatterRe      = regexp.MustCompile(`(?s)^---[ \t]*\n(.+?)\n---[ \t]*\n`)
	emptyFrontmatterRe = regexp.MustCompile(`^---[ \t]*\n---[ \t]*\n`)
)

// Policy represents a workflow state's transition policy parsed from YAML frontmatter.
type Policy struct {
	AllowedTransitions []map[string]string // each entry has at least "tag"
	Model              string              // empty if not specified
	Effort             string              // empty if not specified
}

// PolicyViolationError is returned when a transition violates the state's policy.
type PolicyViolationError struct {
	msg string
}

func (e *PolicyViolationError) Error() string { return e.msg }

// ParseFrontmatter extracts YAML frontmatter from markdown content.
//
// Returns (nil, content, nil) when no frontmatter is present.
// Returns (nil, body, nil) when frontmatter is empty or contains only null/empty YAML.
// Returns (*Policy, body, nil) on success.
// Returns (nil, "", error) when frontmatter contains invalid YAML.
func ParseFrontmatter(content string) (*Policy, string, error) {
	// Normalize Windows line endings so the regex works regardless of how the
	// file was checked out. Python's text-mode file I/O does this automatically;
	// Go's os.ReadFile reads raw bytes, so we do it explicitly here.
	content = strings.ReplaceAll(content, "\r\n", "\n")

	// Try non-empty frontmatter first.
	if m := frontmatterRe.FindStringSubmatchIndex(content); m != nil {
		yamlContent := content[m[2]:m[3]]
		body := content[m[1]:]

		if strings.TrimSpace(yamlContent) == "" {
			return nil, body, nil
		}
		p, err := parseYAML(yamlContent)
		if err != nil {
			return nil, "", err
		}
		return p, body, nil
	}

	// Try empty frontmatter (--- immediately followed by ---).
	if m := emptyFrontmatterRe.FindStringIndex(content); m != nil {
		return nil, content[m[1]:], nil
	}

	// No frontmatter.
	return nil, content, nil
}

// yamlFrontmatter is the intermediate struct for YAML unmarshaling.
type yamlFrontmatter struct {
	AllowedTransitions []map[string]string `yaml:"allowed_transitions"`
	Model              string              `yaml:"model"`
	Effort             string              `yaml:"effort"`
}

// parseYAML converts a YAML string into a Policy, or nil if the document is empty.
func parseYAML(yamlContent string) (*Policy, error) {
	// First check whether the document is null / empty (matches Python's `if not data`).
	var raw interface{}
	if err := yaml.Unmarshal([]byte(yamlContent), &raw); err != nil {
		return nil, fmt.Errorf("Invalid YAML frontmatter: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	if m, ok := raw.(map[string]interface{}); ok && len(m) == 0 {
		return nil, nil
	}

	var data yamlFrontmatter
	if err := yaml.Unmarshal([]byte(yamlContent), &data); err != nil {
		return nil, fmt.Errorf("Invalid YAML frontmatter: %w", err)
	}

	// Filter: only keep entries that have a "tag" key.
	var valid []map[string]string
	for _, entry := range data.AllowedTransitions {
		if _, ok := entry["tag"]; ok {
			valid = append(valid, entry)
		}
	}

	// Normalize model: lowercase + trim; empty string stays empty.
	model := strings.TrimSpace(strings.ToLower(data.Model))

	// Normalize effort: lowercase + trim.
	effort := strings.TrimSpace(strings.ToLower(data.Effort))

	return &Policy{
		AllowedTransitions: valid,
		Model:              model,
		Effort:             effort,
	}, nil
}

// TargetsMatch reports whether policyTarget (from the policy rule) matches
// transitionTarget (from the parsed transition).
//
// If policyTarget has a file extension, only an exact match is accepted.
// If policyTarget has no extension (abstract), it matches any transitionTarget
// that has the same stem and a recognized state extension (.md, .sh, .bat, .ps1).
func TargetsMatch(policyTarget, transitionTarget string) bool {
	if policyTarget == transitionTarget {
		return true
	}

	policyExt := filepath.Ext(policyTarget)
	if policyExt != "" {
		// Explicit extension — only exact match, which already failed above.
		return false
	}

	// Abstract target: check stem equality and valid extension.
	transitionExt := strings.ToLower(filepath.Ext(transitionTarget))
	transitionStem := strings.TrimSuffix(transitionTarget, filepath.Ext(transitionTarget))
	return transitionStem == policyTarget && stateExtensions[transitionExt]
}

// ShouldUseReminderPrompt reports whether a reminder prompt should be sent to
// the agent on parse failure. Reminders are only useful when the policy
// specifies allowed transitions; without them there is nothing to remind about.
func ShouldUseReminderPrompt(p *Policy) bool {
	return p != nil && len(p.AllowedTransitions) > 0
}

// ValidateTransitionPolicy checks that transition complies with p.
// A nil policy or a policy with an empty AllowedTransitions list imposes no
// restrictions. Returns a *PolicyViolationError on failure.
func ValidateTransitionPolicy(transition parsing.Transition, p *Policy) error {
	if p == nil || len(p.AllowedTransitions) == 0 {
		return nil
	}

	for _, allowed := range p.AllowedTransitions {
		if allowed["tag"] != transition.Tag {
			continue
		}
		// result tags: if the policy specifies a fixed payload, only match
		// when the transition's payload matches; otherwise allow any payload.
		if transition.Tag == "result" {
			if allowedPayload, ok := allowed["payload"]; ok {
				if strings.TrimSpace(transition.Payload) != allowedPayload {
					continue
				}
			}
			return nil
		}
		// Check target (if policy specifies one).
		if pt, ok := allowed["target"]; ok {
			if !TargetsMatch(pt, transition.Target) {
				continue
			}
		}
		// Check return attribute (call/function).
		if pr, ok := allowed["return"]; ok {
			if !TargetsMatch(pr, transition.Attributes["return"]) {
				continue
			}
		}
		// Check next attribute (fork, await).
		if pn, ok := allowed["next"]; ok {
			if !TargetsMatch(pn, transition.Attributes["next"]) {
				continue
			}
		}
		// Check timeout_next attribute (await).
		if ptn, ok := allowed["timeout_next"]; ok {
			if !TargetsMatch(ptn, transition.Attributes["timeout_next"]) {
				continue
			}
		}
		return nil // all checks passed
	}

	// No matching rule found — build a helpful error message.
	var allowedForTag []map[string]string
	for _, allowed := range p.AllowedTransitions {
		if allowed["tag"] == transition.Tag {
			allowedForTag = append(allowedForTag, allowed)
		}
	}
	if len(allowedForTag) > 0 {
		var detail string
		if transition.Tag == "result" {
			detail = fmt.Sprintf(
				"result with payload %q is not allowed. "+
					"Allowed payloads for %q: %v",
				transition.Payload, transition.Tag, allowedForTag,
			)
		} else {
			detail = fmt.Sprintf(
				"transition %q with target %q and attributes %v is not allowed. "+
					"Allowed combinations for %q: %v",
				transition.Tag, transition.Target, transition.Attributes,
				transition.Tag, allowedForTag,
			)
		}
		return &PolicyViolationError{msg: detail}
	}

	seen := map[string]bool{}
	var allowedTags []string
	for _, allowed := range p.AllowedTransitions {
		tag := allowed["tag"]
		if !seen[tag] {
			allowedTags = append(allowedTags, tag)
			seen[tag] = true
		}
	}
	return &PolicyViolationError{msg: fmt.Sprintf(
		"tag %q is not allowed. Allowed tags: %v",
		transition.Tag, allowedTags,
	)}
}

// CanUseImplicitTransition reports whether the policy's single allowed
// transition can be used implicitly (without requiring the agent to emit a tag).
//
// Conditions: policy exists, exactly one allowed transition, and either a
// target is specified (for non-result tags) or a fixed payload is specified
// (for result tags).
func CanUseImplicitTransition(p *Policy) bool {
	if p == nil || len(p.AllowedTransitions) != 1 {
		return false
	}
	allowed := p.AllowedTransitions[0]
	if allowed["tag"] == "result" {
		_, hasPayload := allowed["payload"]
		return hasPayload
	}
	// Await transitions cannot be implicit — the LLM must compose the
	// prompt inside the tag body.
	if allowed["tag"] == "await" {
		return false
	}
	_, hasTarget := allowed["target"]
	return hasTarget
}

// GetImplicitTransition constructs a Transition from the single allowed rule.
// Panics (via error return) when CanUseImplicitTransition would return false.
func GetImplicitTransition(p *Policy) (parsing.Transition, error) {
	if !CanUseImplicitTransition(p) {
		return parsing.Transition{}, fmt.Errorf(
			"cannot get implicit transition: policy must have exactly one " +
				"allowed transition with a target or a result with a fixed payload",
		)
	}
	allowed := p.AllowedTransitions[0]
	if allowed["tag"] == "result" {
		return parsing.Transition{
			Tag:     "result",
			Payload: allowed["payload"],
		}, nil
	}
	attrs := make(map[string]string)
	for k, v := range allowed {
		if k != "tag" && k != "target" {
			attrs[k] = v
		}
	}
	return parsing.Transition{
		Tag:        allowed["tag"],
		Target:     allowed["target"],
		Attributes: attrs,
		Payload:    "",
	}, nil
}

// GenerateReminderPrompt produces a formatted reminder message listing all
// allowed transitions. Used when an agent fails to emit a valid transition tag.
func GenerateReminderPrompt(p *Policy) (string, error) {
	if p == nil {
		return "", fmt.Errorf("cannot generate reminder: policy is nil")
	}
	if len(p.AllowedTransitions) == 0 {
		return "", fmt.Errorf("cannot generate reminder: no allowed_transitions in policy")
	}

	lines := []string{
		"",
		"---",
		"REMINDER: Emit exactly one of these tags (target names are literal, not placeholders):",
		"",
	}

	for i, allowed := range p.AllowedTransitions {
		tag := allowed["tag"]
		target := allowed["target"]

		// Collect and sort attribute keys (excluding tag and target) for deterministic output.
		var attrKeys []string
		for k := range allowed {
			if k != "tag" && k != "target" {
				attrKeys = append(attrKeys, k)
			}
		}
		sort.Strings(attrKeys)

		var attrParts []string
		for _, k := range attrKeys {
			v := allowed[k]
			if strings.Contains(v, `"`) {
				attrParts = append(attrParts, fmt.Sprintf("%s='%s'", k, v))
			} else {
				attrParts = append(attrParts, fmt.Sprintf(`%s="%s"`, k, v))
			}
		}

		var tagStr string
		if tag == "result" {
			if payload, ok := allowed["payload"]; ok {
				tagStr = fmt.Sprintf("<%s>%s</%s>", tag, payload, tag)
			} else {
				tagStr = fmt.Sprintf("<%s>...</%s>", tag, tag)
			}
		} else if tag == "await" {
			// Await: all non-tag keys become attributes (next, timeout,
			// timeout_next); the body is a prompt placeholder.  attrParts
			// already contains these since await entries have no "target" key.
			if len(attrParts) > 0 {
				tagStr = fmt.Sprintf("<await %s>[human-facing prompt here]</await>", strings.Join(attrParts, " "))
			} else {
				tagStr = "<await>[human-facing prompt here]</await>"
			}
		} else {
			if target == "" {
				target = "TARGET"
			}
			if len(attrParts) > 0 {
				tagStr = fmt.Sprintf("<%s %s>%s</%s>", tag, strings.Join(attrParts, " "), target, tag)
			} else {
				tagStr = fmt.Sprintf("<%s>%s</%s>", tag, target, tag)
			}
		}
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, tagStr))
	}

	lines = append(lines, "", "Emit exactly one of the above tags.", "---")
	return strings.Join(lines, "\n"), nil
}
