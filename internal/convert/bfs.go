package convert

import "github.com/vector76/raymond/internal/parsing"

// bfsDistances computes minimum BFS distances from the entry point state to all
// reachable states. The entry and map keys are abstract state names (no extension).
func bfsDistances(entry string, transitions map[string][]parsing.Transition) map[string]int {
	// Build the directed graph as an adjacency list.
	graph := make(map[string][]string)

	addEdge := func(from, to string) {
		graph[from] = append(graph[from], to)
	}

	for source, trans := range transitions {
		for _, t := range trans {
			switch {
			case parsing.IsWorkflowTag(t.Tag):
				// Cross-workflow: target is a workflow path specifier, use as-is.
				addEdge(source, t.Target)
				switch t.Tag {
				case "call-workflow", "function-workflow":
					if ret, ok := t.Attributes["return"]; ok && ret != "" {
						addEdge(source, parsing.ExtractStateName(ret))
					}
				case "fork-workflow":
					if next, ok := t.Attributes["next"]; ok && next != "" {
						addEdge(source, parsing.ExtractStateName(next))
					}
				}

			case t.Tag == "goto" || t.Tag == "reset":
				addEdge(source, parsing.ExtractStateName(t.Target))

			case t.Tag == "call" || t.Tag == "function":
				callee := parsing.ExtractStateName(t.Target)
				addEdge(source, callee)

				// Resolve result edges: find all states reachable from callee
				// via goto/reset only, then for each that has a result transition,
				// add an edge to the return state.
				if ret, ok := t.Attributes["return"]; ok && ret != "" {
					retState := parsing.ExtractStateName(ret)
					reachable := gotoResetReachable(callee, transitions)
					for state := range reachable {
						if hasResult(transitions[state]) {
							addEdge(state, retState)
						}
					}
				}

			case t.Tag == "fork":
				addEdge(source, parsing.ExtractStateName(t.Target))
				if next, ok := t.Attributes["next"]; ok && next != "" {
					addEdge(source, parsing.ExtractStateName(next))
				}

			case t.Tag == "result":
				// Handled by call/function resolution above.
			}
		}
	}

	// Standard BFS from entry.
	dist := make(map[string]int)
	dist[entry] = 0
	queue := []string{entry}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		curDist := dist[cur]
		for _, neighbor := range graph[cur] {
			if _, visited := dist[neighbor]; !visited {
				dist[neighbor] = curDist + 1
				queue = append(queue, neighbor)
			}
		}
	}

	return dist
}

// gotoResetReachable returns all states reachable from start following only
// goto and reset transitions (BFS). The start state itself is included.
func gotoResetReachable(start string, transitions map[string][]parsing.Transition) map[string]bool {
	visited := map[string]bool{start: true}
	queue := []string{start}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, t := range transitions[cur] {
			if t.Tag == "goto" || t.Tag == "reset" {
				target := parsing.ExtractStateName(t.Target)
				if !visited[target] {
					visited[target] = true
					queue = append(queue, target)
				}
			}
		}
	}

	return visited
}

// hasResult reports whether any transition in the slice is a result tag.
func hasResult(trans []parsing.Transition) bool {
	for _, t := range trans {
		if t.Tag == "result" {
			return true
		}
	}
	return false
}
