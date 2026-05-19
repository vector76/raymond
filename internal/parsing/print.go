package parsing

import "strings"

const printOpenTag = "<print>"
const printCloseTag = "</print>"

// ExtractPrintTags scans input for complete <print>...</print> tags and
// returns their text contents in payloads. The remainder is the unconsumed
// suffix — starting from the beginning of an incomplete <print> open tag if
// one is present without a matching close tag, or "" otherwise. Callers can
// append new chunks to remainder and call again to process streaming input.
func ExtractPrintTags(input string) (payloads []string, remainder string) {
	pos := 0
	for {
		openIdx := strings.Index(input[pos:], printOpenTag)
		if openIdx < 0 {
			break
		}
		openIdx += pos

		afterOpen := openIdx + len(printOpenTag)
		closeIdx := strings.Index(input[afterOpen:], printCloseTag)
		if closeIdx < 0 {
			remainder = input[openIdx:]
			return
		}

		payloads = append(payloads, input[afterOpen:afterOpen+closeIdx])
		pos = afterOpen + closeIdx + len(printCloseTag)
	}

	remainder = partialTagPrefix(input[pos:], printOpenTag)
	return
}

// partialTagPrefix returns the longest suffix of s that is a non-empty prefix
// of tag, or "" if no such suffix exists. This detects mid-tag splits at the
// end of a streaming chunk so the partial opener can be prepended to the next.
func partialTagPrefix(s, tag string) string {
	for i := 0; i < len(s); i++ {
		if strings.HasPrefix(tag, s[i:]) {
			return s[i:]
		}
	}
	return ""
}
