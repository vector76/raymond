package parsing

import (
	"fmt"
	"regexp"
	"strings"
)

// openTagRe matches opening tags for any recognized transition type.
// Group 1: tag name, Group 2: attributes string (may be empty).
//
// Go's RE2 does not support backreferences, so we cannot enforce matching
// close tags in a single regex. Instead we find the opening tag here, then
// locate the corresponding closing tag with a plain string search.
var openTagRe = regexp.MustCompile(`<(call-workflow|function-workflow|fork-workflow|reset-workflow|goto|reset|function|call|fork|result)([^>]*)>`)

// attrRe matches key="value" or key='value' attribute pairs.
// Two alternatives enforce matching quote characters so key="val' is rejected.
var attrRe = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|'([^']*)')`)

// Transition represents a single transition tag parsed from agent output.
type Transition struct {
	Tag        string
	Target     string            // filename; empty for result tags
	Attributes map[string]string // e.g. {"return": "NEXT.md"}
	Payload    string            // content between tags; non-empty for result tags only
}

// ParseTransitions extracts all recognized transition tags from output.
//
// Tags may appear anywhere in the text. Unknown XML-like tags are silently
// ignored. Returns an error if a non-result tag has an empty target or a
// target containing a path separator (/ or \).
func ParseTransitions(output string) ([]Transition, error) {
	var transitions []Transition

	for _, match := range openTagRe.FindAllStringSubmatchIndex(output, -1) {
		tagName := output[match[2]:match[3]]
		attrsStr := strings.TrimSpace(output[match[4]:match[5]])
		afterOpen := match[1] // byte position immediately after '>'

		closeTag := "</" + tagName + ">"
		closeIdx := strings.Index(output[afterOpen:], closeTag)
		if closeIdx < 0 {
			continue // no matching close tag; skip
		}
		content := output[afterOpen : afterOpen+closeIdx]

		attrs := parseAttributes(attrsStr)

		var target, payload string
		if tagName == "result" {
			payload = content
		} else {
			target = strings.TrimSpace(content)
			if target == "" {
				return nil, fmt.Errorf(
					"tag <%s> has empty target. Non-result tags must specify a target filename.",
					tagName,
				)
			}
			if !IsWorkflowTag(tagName) && strings.ContainsAny(target, "/\\") {
				return nil, fmt.Errorf(
					"path %q contains path separator. Tag targets must be filenames only, not paths.",
					target,
				)
			}
		}

		transitions = append(transitions, Transition{
			Tag:        tagName,
			Target:     target,
			Attributes: attrs,
			Payload:    payload,
		})
	}

	return transitions, nil
}

// parseAttributes parses HTML-style key="value" or key='value' pairs.
// The regex uses two alternatives to enforce matching quotes:
// group 2 captures the value for double-quoted attributes,
// group 3 captures the value for single-quoted attributes.
func parseAttributes(attrsStr string) map[string]string {
	attrs := make(map[string]string)
	for _, m := range attrRe.FindAllStringSubmatch(attrsStr, -1) {
		// m[2] is non-empty for double-quoted values; m[3] for single-quoted.
		value := m[2]
		if value == "" && m[3] != "" {
			value = m[3]
		}
		attrs[m[1]] = value
	}
	return attrs
}

// ValidateSingleTransition returns an error if transitions does not contain
// exactly one element. The caller is responsible for deciding how to respond
// (e.g. send a reminder prompt to the agent).
func ValidateSingleTransition(transitions []Transition) error {
	if len(transitions) != 1 {
		return fmt.Errorf("expected exactly one transition, found %d", len(transitions))
	}
	return nil
}

// IsWorkflowTag reports whether tag is a cross-workflow transition whose target
// is a workflow specifier (directory path or zip path) rather than a local
// state name. Workflow-tag targets are passed through to specifier.Resolve
// without being validated as filenames.
func IsWorkflowTag(tag string) bool {
	return tag == "call-workflow" || tag == "function-workflow" || tag == "fork-workflow" || tag == "reset-workflow"
}

// ExtractStateName strips the recognized state file extension (.md, .sh, .bat, .ps1)
// from filename, case-insensitively. If no recognized extension is present the
// filename is returned unchanged. Case of the base name is preserved.
func ExtractStateName(filename string) string {
	lower := strings.ToLower(filename)
	for _, ext := range []string{".md", ".sh", ".bat", ".ps1"} {
		if strings.HasSuffix(lower, ext) {
			return filename[:len(filename)-len(ext)]
		}
	}
	return filename
}
