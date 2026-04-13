// Loop primitive: single-node and multi-node iteration with explicit termination.
// Separate from loop_restart (which handles whole-run restarts on transient_infra failures).
package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// LoopSpec describes an iteration scope parsed from DOT node attributes.
// Single-node loops set BeginNode == EndNode. Multi-node loops identify the
// begin/end pair via loop_id.
type LoopSpec struct {
	LoopID            string
	BeginNode         string
	EndNode           string
	Count             int    // loop_count: exact number of iterations
	Max               int    // loop_max: hard ceiling (always enforced)
	UntilFile         string // loop_until_file: stop when this file exists in workspace
	UntilFileContains string // loop_until_file_contains: stop when file contains pattern
	WhileOutcomeFail  bool   // loop_while_outcome=fail: stop when outcome becomes success
}

// hasTerminationCondition returns true if at least one termination condition is set.
// Graphs with no termination condition are rejected at validation.
func (s *LoopSpec) hasTerminationCondition() bool {
	return s.Count > 0 || s.UntilFile != "" || s.UntilFileContains != "" || s.WhileOutcomeFail
}

// parseLoopSpecFromNode reads loop_* attributes from a node. Returns nil if the
// node has no loop attributes at all. For single-node loops, BeginNode and
// EndNode are both set to the node's own ID and LoopID defaults to the node ID.
func parseLoopSpecFromNode(n *model.Node) *LoopSpec {
	if n == nil {
		return nil
	}
	count := parseInt(n.Attr("loop_count", ""), 0)
	max := parseInt(n.Attr("loop_max", ""), 0)
	untilFile := strings.TrimSpace(n.Attr("loop_until_file", ""))
	untilFileContains := strings.TrimSpace(n.Attr("loop_until_file_contains", ""))
	whileFail := strings.EqualFold(strings.TrimSpace(n.Attr("loop_while_outcome", "")), "fail")
	loopID := strings.TrimSpace(n.Attr("loop_id", ""))

	if count == 0 && max == 0 && untilFile == "" && untilFileContains == "" && !whileFail && loopID == "" {
		return nil
	}
	if loopID == "" {
		loopID = n.ID
	}
	return &LoopSpec{
		LoopID:            loopID,
		BeginNode:         n.ID,
		EndNode:           n.ID,
		Count:             count,
		Max:               max,
		UntilFile:         untilFile,
		UntilFileContains: untilFileContains,
		WhileOutcomeFail:  whileFail,
	}
}

// shouldContinueLoop evaluates termination conditions after an iteration completes.
// Returns (continue, reason) where continue=true means run another iteration and
// reason is a human-readable explanation (empty when continuing without issue, or
// a failure reason when loop_max is exceeded).
func (e *Engine) shouldContinueLoop(spec *LoopSpec, iteration int, lastOutcomeStatus string) (bool, string, bool) {
	// loop_max is always enforced as a hard ceiling. Exceeding it is a failure.
	if spec.Max > 0 && iteration >= spec.Max {
		return false, fmt.Sprintf("loop_max exceeded: %d iterations without meeting termination condition", spec.Max), true
	}

	// loop_count: exact iteration count.
	if spec.Count > 0 && iteration >= spec.Count {
		return false, "", false
	}

	// loop_until_file: stop when file exists in workspace.
	if spec.UntilFile != "" {
		path := e.resolveWorkspaceRelative(spec.UntilFile)
		if _, err := os.Stat(path); err == nil {
			return false, "", false
		}
	}

	// loop_until_file_contains: stop when file contains pattern.
	if spec.UntilFileContains != "" {
		// Convention: loop_until_file_contains="path:pattern" — split on the first colon.
		parts := strings.SplitN(spec.UntilFileContains, ":", 2)
		if len(parts) == 2 {
			path := e.resolveWorkspaceRelative(strings.TrimSpace(parts[0]))
			pattern := strings.TrimSpace(parts[1])
			if data, err := os.ReadFile(path); err == nil {
				if strings.Contains(string(data), pattern) {
					return false, "", false
				}
			}
		}
	}

	// loop_while_outcome=fail: stop when outcome becomes success.
	if spec.WhileOutcomeFail {
		if lastOutcomeStatus == "success" || lastOutcomeStatus == "degraded_success" || lastOutcomeStatus == "partial_success" {
			return false, "", false
		}
	}

	return true, "", false
}

// resolveWorkspaceRelative joins a path to the engine's worktree dir (if
// relative), or returns it unchanged if absolute.
func (e *Engine) resolveWorkspaceRelative(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if e.WorktreeDir != "" {
		return filepath.Join(e.WorktreeDir, path)
	}
	return path
}

// handleLoopIteration runs after a node completes and decides whether the
// engine should loop back or proceed to normal edge selection. Returns:
//   - shouldLoop=true with nextNodeID to jump back
//   - shouldLoop=false, failReason="" to let normal routing continue
//   - shouldLoop=false, failReason set when loop_max has been exceeded
//
// The current iteration count is stored in engine context under
// loop.<loop_id>.iteration and is incremented on each call. When a loop
// terminates normally, the counter is reset so the same loop can be re-entered
// later without stale state.
func (e *Engine) handleLoopIteration(node *model.Node, out runtime.Outcome) (bool, string, string) {
	if node == nil {
		return false, "", ""
	}
	handlerType := shapeToType(node.Shape())

	var spec *LoopSpec
	var jumpTo string

	switch handlerType {
	case "loop.end":
		spec = parseLoopSpecFromNode(node)
		if spec == nil || !spec.hasTerminationCondition() {
			return false, "", ""
		}
		begin := findLoopBeginForEnd(e.Graph, node)
		if begin == nil {
			return false, "", ""
		}
		spec.BeginNode = begin.ID
		spec.EndNode = node.ID
		jumpTo = begin.ID
	case "loop.begin":
		// Pass-through: iteration check happens at loop.end, not here.
		return false, "", ""
	default:
		spec = parseLoopSpecFromNode(node)
		if spec == nil || !spec.hasTerminationCondition() {
			return false, "", ""
		}
		jumpTo = node.ID
	}

	contextKey := fmt.Sprintf("loop.%s.iteration", spec.LoopID)
	iteration := 0
	if raw, ok := e.Context.Get(contextKey); ok && raw != nil {
		if n, ok := raw.(int); ok {
			iteration = n
		}
	}
	iteration++
	e.Context.Set(contextKey, iteration)

	shouldContinue, reason, isFailure := e.shouldContinueLoop(spec, iteration, string(out.Status))
	if isFailure {
		return false, "", reason
	}
	if !shouldContinue {
		// Normal termination: reset counters so this loop scope is fresh on
		// re-entry (e.g., when an outer loop revisits this inner loop).
		e.Context.Set(contextKey, 0)
		if e.loopIterations != nil {
			delete(e.loopIterations, jumpTo)
			delete(e.loopIterations, spec.EndNode)
		}
		e.appendProgress(map[string]any{
			"event":      "loop_terminated",
			"loop_id":    spec.LoopID,
			"iterations": iteration,
			"end_node":   spec.EndNode,
		})
		return false, "", ""
	}

	// Track iteration on the jump target so the next pass through the main
	// loop can use it as the DB attempt number — giving each loop iteration
	// its own node_executions row and captured artifacts.
	if e.loopIterations == nil {
		e.loopIterations = map[string]int{}
	}
	e.loopIterations[jumpTo] = iteration

	e.appendProgress(map[string]any{
		"event":      "loop_iteration",
		"loop_id":    spec.LoopID,
		"iteration":  iteration,
		"begin_node": jumpTo,
		"end_node":   spec.EndNode,
	})
	return true, jumpTo, ""
}

// findLoopBeginForEnd locates the paired loop_begin node (shape=trapezium) for
// a loop_end node (shape=invtrapezium) by matching loop_id. Single-node loops
// return the node itself.
func findLoopBeginForEnd(g *model.Graph, endNode *model.Node) *model.Node {
	if g == nil || endNode == nil {
		return nil
	}
	loopID := strings.TrimSpace(endNode.Attr("loop_id", ""))
	if loopID == "" {
		loopID = endNode.ID
	}
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		if shapeToType(n.Shape()) != "loop.begin" {
			continue
		}
		nLoopID := strings.TrimSpace(n.Attr("loop_id", ""))
		if nLoopID == "" {
			nLoopID = id
		}
		if nLoopID == loopID {
			return n
		}
	}
	return nil
}
