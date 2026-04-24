package validate

import (
	"fmt"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

// lintTerminalConditionEdge checks that every inbound edge to a terminal node
// whose predecessor is a fallible node (one that can produce a non-success
// outcome) carries an explicit condition= attribute.
//
// Rule: terminal_condition_edge (ERROR)
//
// Rationale: if a fallible node (e.g., an agent or tool) fails and has an
// unconditional edge to a terminal node, the engine follows the edge, reaches
// the terminal, and reports status=success — even though the predecessor failed.
// Requiring an explicit condition forces the graph author to declare their
// routing intent rather than relying on an implicit "always proceed to done".
//
// Success-only predecessors (start, conditional/diamond, loop.begin/end,
// concurrent.split/join) are exempt: they never execute user code and cannot
// produce a non-success outcome on their own, so an unconditional edge from
// them to a terminal node is safe.
func lintTerminalConditionEdge(g *model.Graph) []Diagnostic {
	exitSet := make(map[string]bool)
	for _, id := range findAllExitNodeIDs(g) {
		exitSet[id] = true
	}

	var diags []Diagnostic
	for _, e := range g.Edges {
		if e == nil {
			continue
		}
		// Only care about edges whose target is a terminal node.
		if !exitSet[e.To] {
			continue
		}
		// Edge already has an explicit condition — graph author was deliberate.
		if strings.TrimSpace(e.Condition()) != "" {
			continue
		}
		// Look up the source node.
		fromNode := g.Nodes[e.From]
		if fromNode == nil {
			// Missing node — caught by edge_target_exists rule.
			continue
		}
		// Success-only predecessors are exempt: they cannot fail.
		if isSuccessOnlyPredecessor(fromNode, e.From) {
			continue
		}
		// Fallible predecessor with an unconditional edge to a terminal node.
		diags = append(diags, Diagnostic{
			Rule:     "terminal_condition_edge",
			Severity: SeverityError,
			Message: fmt.Sprintf(
				"edge %s → %s: unconditional edge to terminal node from fallible predecessor %q; add an explicit condition= so routing intent is clear (e.g. condition=\"outcome=success\" to proceed only on success, or condition=\"outcome!=success\" for a failure fallback)",
				e.From, e.To, e.From,
			),
			EdgeFrom: e.From,
			EdgeTo:   e.To,
			Fix: fmt.Sprintf(
				"add condition=\"outcome=success\" (or another explicit condition) to the %s → %s edge",
				e.From, e.To,
			),
		})
	}
	return diags
}

// isSuccessOnlyPredecessor returns true when n is a node type that cannot
// produce a non-success outcome on its own (i.e., it performs no user-code
// execution and its routing is fully deterministic).  These nodes are exempt
// from the terminal_condition_edge rule.
//
// The set covers:
//   - Start nodes     shape=Mdiamond, shape=circle, or id="start" (by convention)
//   - Conditional     shape=diamond  (pure pass-through router)
//   - Loop sentinels  shape=trapezium (loop.begin), shape=invtrapezium (loop.end)
//   - Concurrent      shape=pentagon (split), shape=cylinder (join)
func isSuccessOnlyPredecessor(n *model.Node, id string) bool {
	switch n.Shape() {
	case "Mdiamond", "circle":
		return true // start node shapes
	case "diamond":
		return true // conditional pass-through — never executes user code
	case "trapezium", "invtrapezium":
		return true // loop.begin / loop.end sentinels
	case "pentagon", "cylinder":
		return true // concurrent.split / concurrent.join
	}
	// Also honour the "start" node ID convention (spec §6.1).
	if strings.EqualFold(id, "start") {
		return true
	}
	return false
}
