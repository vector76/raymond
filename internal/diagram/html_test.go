package diagram

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateHTMLContainsMermaidText(t *testing.T) {
	mermaidText := "flowchart TD\n    A --> B"
	result := Result{
		Mermaid:      mermaidText,
		FileContents: map[string]NodeContent{},
	}
	html := GenerateHTML(result)
	assert.Contains(t, html, mermaidText)
}

func TestGenerateHTMLClickableNodes(t *testing.T) {
	result := Result{
		Mermaid: "flowchart TD\n    nodeA --> nodeB",
		FileContents: map[string]NodeContent{
			"nodeA": {Content: "content A", IsMarkdown: true},
			"nodeB": {Content: "content B", IsMarkdown: false},
		},
	}
	html := GenerateHTML(result)
	assert.Contains(t, html, "click nodeA handleNodeClick")
	assert.Contains(t, html, "click nodeB handleNodeClick")
}

func TestGenerateHTMLNonClickableNodes(t *testing.T) {
	result := Result{
		Mermaid: "flowchart TD\n    nodeA --> nodeC",
		FileContents: map[string]NodeContent{
			"nodeA": {Content: "content A", IsMarkdown: true},
		},
	}
	html := GenerateHTML(result)
	assert.Contains(t, html, "click nodeA handleNodeClick")
	assert.NotContains(t, html, "click nodeC handleNodeClick")
	// nodeC has no backing file, so it must not appear in the content map.
	assert.NotContains(t, html, `"nodeC":`)
}

func TestGenerateHTMLContentMapJSON(t *testing.T) {
	result := Result{
		Mermaid: "flowchart TD",
		FileContents: map[string]NodeContent{
			"myNode":    {Content: "hello", IsMarkdown: true},
			"otherNode": {Content: "world", IsMarkdown: false},
		},
	}
	html := GenerateHTML(result)
	assert.Contains(t, html, "myNode")
	assert.Contains(t, html, "otherNode")
	assert.Contains(t, html, "nodeData")
}

func TestGenerateHTMLMarkdownVsScript(t *testing.T) {
	result := Result{
		Mermaid: "flowchart TD",
		FileContents: map[string]NodeContent{
			"mdNode":     {Content: "# heading", IsMarkdown: true},
			"scriptNode": {Content: "echo hello", IsMarkdown: false},
		},
	}
	html := GenerateHTML(result)
	// Both type values must appear in the JSON
	assert.Contains(t, html, `"type":"markdown"`)
	assert.Contains(t, html, `"type":"script"`)
}

func TestGenerateHTMLCDNTag(t *testing.T) {
	result := Result{
		Mermaid:      "flowchart TD",
		FileContents: map[string]NodeContent{},
	}
	html := GenerateHTML(result)
	assert.Contains(t, html, "https://cdn.jsdelivr.net/npm/mermaid/dist/mermaid.min.js")
}

func TestGenerateHTMLScriptContentInJSON(t *testing.T) {
	// Script content with HTML-special chars must survive round-trip through
	// the embedded JSON so the client-side JS can handle escaping correctly.
	result := Result{
		Mermaid: "flowchart TD",
		FileContents: map[string]NodeContent{
			"myScript": {Content: "echo \"<hello>\" && exit", IsMarkdown: false},
		},
	}
	html := GenerateHTML(result)
	// The raw chars must be JSON-encoded in the nodeData block, not raw HTML.
	assert.Contains(t, html, `\u003c`) // JSON encoding of <
	assert.Contains(t, html, `\u003e`) // JSON encoding of >
}

func TestGenerateHTMLStructure(t *testing.T) {
	result := Result{
		Mermaid:      "flowchart TD",
		FileContents: map[string]NodeContent{},
	}
	html := GenerateHTML(result)
	require.Contains(t, html, "<html")
	require.Contains(t, html, "<head")
	require.Contains(t, html, "<body")
}
