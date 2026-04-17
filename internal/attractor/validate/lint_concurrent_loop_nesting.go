// Validation rules for the concurrent and loop primitives.
// Reject nested loops inside concurrent regions and nested concurrent regions
// so the execution semantics stay simple (one active loop scope, one
// concurrent scope at a time).
package validate

import (
	"github.com/danshapiro/kilroy/internal/attractor/model"
)

// shape helper functions. Must stay in sync with engine/handlers.go shapeToType.
func isLoopBeginShape(shape string) bool  { return shape == "trapezium" }
func isLoopEndShape(shape string) bool    { return shape == "invtrapezium" }
func isConcurrentSplit(shape string) bool { return shape == "pentagon" }
func isConcurrentJoin(shape string) bool  { return shape == "cylinder" }

// lintNoNestedConcurrentRegions reports when a concurrent.split is reachable
// from another concurrent.split before the outer split's paired join.
func lintNoNestedConcurrentRegions(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil || !isConcurrentSplit(n.Shape()) {
			continue
		}
		joinID := findPairedConcurrentJoinID(g, id)
		if joinID == "" {
			continue
		}
		reachable := nodesBetween(g, id, joinID)
		for rid := range reachable {
			if rid == id || rid == joinID {
				continue
			}
			inner := g.Nodes[rid]
			if inner == nil {
				continue
			}
			if isConcurrentSplit(inner.Shape()) {
				diags = append(diags, Diagnostic{
					Rule:     "no_nested_concurrent_regions",
					Severity: SeverityError,
					Message:  "concurrent region cannot be nested inside another concurrent region",
					NodeID:   rid,
				})
			}
		}
	}
	return diags
}

// lintNoLoopsInConcurrentRegions reports when a loop_begin or loop_end is
// reachable from a concurrent.split before its paired join.
func lintNoLoopsInConcurrentRegions(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil || !isConcurrentSplit(n.Shape()) {
			continue
		}
		joinID := findPairedConcurrentJoinID(g, id)
		if joinID == "" {
			continue
		}
		reachable := nodesBetween(g, id, joinID)
		for rid := range reachable {
			if rid == id || rid == joinID {
				continue
			}
			inner := g.Nodes[rid]
			if inner == nil {
				continue
			}
			if isLoopBeginShape(inner.Shape()) || isLoopEndShape(inner.Shape()) {
				diags = append(diags, Diagnostic{
					Rule:     "no_loops_in_concurrent_regions",
					Severity: SeverityError,
					Message:  "loop cannot be nested inside a concurrent region",
					NodeID:   rid,
				})
			}
		}
	}
	return diags
}

// lintConcurrentSplitMinBranches reports when a concurrent.split has fewer
// than 2 outgoing edges — it's a no-op otherwise.
func lintConcurrentSplitMinBranches(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil || !isConcurrentSplit(n.Shape()) {
			continue
		}
		outgoing := 0
		for _, e := range g.Edges {
			if e.From == id {
				outgoing++
			}
		}
		if outgoing < 2 {
			diags = append(diags, Diagnostic{
				Rule:     "concurrent_split_min_branches",
				Severity: SeverityError,
				Message:  "concurrent_split must have at least 2 outgoing edges",
				NodeID:   id,
			})
		}
	}
	return diags
}

// lintConcurrentSplitHasJoin reports when a concurrent.split has no paired
// concurrent.join (either by concurrent_id match or same node ID).
func lintConcurrentSplitHasJoin(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil || !isConcurrentSplit(n.Shape()) {
			continue
		}
		if findPairedConcurrentJoinID(g, id) == "" {
			diags = append(diags, Diagnostic{
				Rule:     "concurrent_split_requires_join",
				Severity: SeverityError,
				Message:  "concurrent_split has no paired concurrent_join (match by concurrent_id attribute)",
				NodeID:   id,
			})
		}
	}
	return diags
}

// findPairedConcurrentJoinID returns the node ID of the concurrent.join that
// matches the split via concurrent_id (falling back to node ID).
func findPairedConcurrentJoinID(g *model.Graph, splitID string) string {
	splitNode := g.Nodes[splitID]
	if splitNode == nil {
		return ""
	}
	splitKey := splitNode.Attr("concurrent_id", "")
	if splitKey == "" {
		splitKey = splitID
	}
	for id, n := range g.Nodes {
		if n == nil || !isConcurrentJoin(n.Shape()) {
			continue
		}
		joinKey := n.Attr("concurrent_id", "")
		if joinKey == "" {
			joinKey = id
		}
		if joinKey == splitKey {
			return id
		}
	}
	return ""
}

// nodesBetween returns the set of nodes reachable from startID before
// reaching stopID, following all outgoing edges. Used to detect what's
// "inside" a concurrent region for nesting checks.
func nodesBetween(g *model.Graph, startID, stopID string) map[string]bool {
	visited := map[string]bool{}
	var walk func(current string)
	walk = func(current string) {
		if current == stopID || visited[current] {
			return
		}
		visited[current] = true
		for _, e := range g.Edges {
			if e.From == current {
				walk(e.To)
			}
		}
	}
	for _, e := range g.Edges {
		if e.From == startID {
			walk(e.To)
		}
	}
	return visited
}
