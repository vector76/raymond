package diagram

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// nodeJSON is the JSON shape for each entry in the embedded nodeData map.
type nodeJSON struct {
	Content string `json:"content"`
	Type    string `json:"type"`
}

// GenerateHTML produces a self-contained HTML page for the given diagram Result.
// It embeds the Mermaid diagram with click directives and a side-panel for file
// content rendering.  The function has no I/O side effects.
func GenerateHTML(result Result) string {
	// Build nodeData map and click directives in one pass.
	// Use sanitizeID on the key so that the JS nodeData lookup and the Mermaid
	// click directive always reference the same ID.
	nodeDataMap := make(map[string]nodeJSON, len(result.FileContents))
	clickLines := make([]string, 0, len(result.FileContents))
	for id, nc := range result.FileContents {
		safeID := sanitizeID(id)
		t := "script"
		if nc.IsMarkdown {
			t = "markdown"
		}
		nodeDataMap[safeID] = nodeJSON{Content: nc.Content, Type: t}
		clickLines = append(clickLines, fmt.Sprintf("click %s handleNodeClick", safeID))
	}
	nodeDataJSON, _ := json.Marshal(nodeDataMap)
	sort.Strings(clickLines)

	mermaidText := result.Mermaid
	if len(clickLines) > 0 {
		mermaidText = mermaidText + "\n" + strings.Join(clickLines, "\n")
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Workflow Diagram</title>
<script src="https://cdn.jsdelivr.net/npm/mermaid/dist/mermaid.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/marked/marked.min.js"></script>
<style>
body { display: flex; margin: 0; font-family: sans-serif; height: 100vh; overflow: hidden; }
.diagram { flex: 1; overflow: auto; padding: 16px; border-right: 1px solid #ccc; }
.content { flex: 1; overflow: auto; padding: 16px; }
</style>
</head>
<body>
<div class="diagram">
<pre class="mermaid">
%s
</pre>
</div>
<div class="content"><p>Click a node to view its content.</p></div>
<script>
const nodeData = %s;
mermaid.initialize({startOnLoad: true});
function handleNodeClick(nodeId) {
  const data = nodeData[nodeId];
  if (!data) return;
  const panel = document.querySelector('.content');
  if (data.type === 'markdown') {
    panel.innerHTML = marked.parse(data.content);
  } else {
    const pre = document.createElement('pre');
    pre.textContent = data.content;
    panel.innerHTML = '';
    panel.appendChild(pre);
  }
}
</script>
</body>
</html>`, mermaidText, string(nodeDataJSON))
}
