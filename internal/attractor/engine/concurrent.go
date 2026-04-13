// Concurrent execution primitive: process-flow-level split and join.
// Separate from the existing parallel handler (shape=component) which is
// worktree-isolated, branch-based, and winner-takes-all for LLM code gen.
// This primitive runs independent node chains concurrently in the same
// workspace, context, and git repo — semantically similar to running
// multiple shell scripts with "&" and waiting on all of them.
package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// concurrentSpec describes a split/join scope parsed from the split node.
type concurrentSpec struct {
	ConcurrentID string
	SplitNode    string
	JoinNode     string
	AllowPartial bool // if true, sibling branches are allowed to fail
}

// parseConcurrentSpecFromNode reads concurrent_* attributes from a split node.
func parseConcurrentSpecFromNode(n *model.Node) *concurrentSpec {
	if n == nil {
		return nil
	}
	id := strings.TrimSpace(n.Attr("concurrent_id", ""))
	if id == "" {
		id = n.ID
	}
	allowPartial := strings.EqualFold(strings.TrimSpace(n.Attr("allow_partial", "")), "true")
	return &concurrentSpec{
		ConcurrentID: id,
		SplitNode:    n.ID,
		AllowPartial: allowPartial,
	}
}

// findConcurrentJoinForSplit walks forward from the split node to find the
// paired concurrent_join node. Pairs are matched by concurrent_id attribute,
// defaulting to node ID. Returns nil if no matching join exists in the graph.
func findConcurrentJoinForSplit(g *model.Graph, splitNode *model.Node) *model.Node {
	if g == nil || splitNode == nil {
		return nil
	}
	splitID := strings.TrimSpace(splitNode.Attr("concurrent_id", ""))
	if splitID == "" {
		splitID = splitNode.ID
	}
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		if shapeToType(n.Shape()) != "concurrent.join" {
			continue
		}
		joinID := strings.TrimSpace(n.Attr("concurrent_id", ""))
		if joinID == "" {
			joinID = id
		}
		if joinID == splitID {
			return n
		}
	}
	return nil
}

// branchResult captures the outcome of a single concurrent branch.
type branchResult struct {
	StartNode    string
	LastNode     string
	Completed    []string
	Err          error
	FailedNode   string
	FailedReason string
}

// runConcurrentRegion dispatches concurrent branches from a split node, waits
// for all of them to reach the paired join, and returns the join node ID (so
// the main runLoop can resume from there) along with any branch failure.
//
// Each branch runs runBranchUntilJoin in a goroutine, sharing the same
// engine state (context, DB, git worktree). Git commits are suppressed
// inside the concurrent region via e.concurrentDepth; one consolidated
// commit is made when the region exits.
//
// Fail-fast: the first branch to return an error cancels sibling goroutines
// via the provided context. Unless allow_partial is set on the split node.
func (e *Engine) runConcurrentRegion(ctx context.Context, splitNode *model.Node, completed *[]string, nodeRetries map[string]int, nodeOutcomes map[string]runtime.Outcome) (string, error) {
	spec := parseConcurrentSpecFromNode(splitNode)
	joinNode := findConcurrentJoinForSplit(e.Graph, splitNode)
	if joinNode == nil {
		return "", fmt.Errorf("concurrent_split %q has no paired concurrent_join", splitNode.ID)
	}
	spec.JoinNode = joinNode.ID

	// Gather outgoing edges from the split. Each becomes a branch.
	var branchEdges []*model.Edge
	for _, edge := range e.Graph.Edges {
		if edge.From == splitNode.ID {
			branchEdges = append(branchEdges, edge)
		}
	}
	if len(branchEdges) < 2 {
		return "", fmt.Errorf("concurrent_split %q needs ≥2 outgoing edges, got %d", splitNode.ID, len(branchEdges))
	}

	e.appendProgress(map[string]any{
		"event":         "concurrent_split_started",
		"concurrent_id": spec.ConcurrentID,
		"split_node":    spec.SplitNode,
		"join_node":     spec.JoinNode,
		"branches":      len(branchEdges),
	})

	// Track the concurrent region depth so per-node commits are skipped
	// inside. Incremented here; decremented after all branches return.
	e.concurrentDepth++
	defer func() { e.concurrentDepth-- }()

	// Cancelable sub-context for fail-fast sibling cancellation.
	branchCtx, cancelBranches := context.WithCancel(ctx)
	defer cancelBranches()

	var wg sync.WaitGroup
	results := make([]branchResult, len(branchEdges))
	for i, edge := range branchEdges {
		wg.Add(1)
		go func(idx int, startNodeID string) {
			defer wg.Done()
			res := e.runBranchUntilJoin(branchCtx, startNodeID, spec.JoinNode, nodeRetries, nodeOutcomes)
			results[idx] = res
			if res.Err != nil && !spec.AllowPartial {
				cancelBranches()
			}
		}(i, edge.To)
	}
	wg.Wait()

	// Aggregate completed nodes from all branches into the shared list.
	for _, r := range results {
		*completed = append(*completed, r.Completed...)
	}

	// Determine aggregate outcome.
	var firstErr error
	var failedNodes []string
	for _, r := range results {
		if r.Err != nil {
			if firstErr == nil {
				firstErr = r.Err
			}
			if r.FailedNode != "" {
				failedNodes = append(failedNodes, r.FailedNode)
			}
		}
	}

	e.appendProgress(map[string]any{
		"event":         "concurrent_split_completed",
		"concurrent_id": spec.ConcurrentID,
		"split_node":    spec.SplitNode,
		"join_node":     spec.JoinNode,
		"branches":      len(branchEdges),
		"failed_nodes":  failedNodes,
	})

	if firstErr != nil && !spec.AllowPartial {
		return spec.JoinNode, fmt.Errorf("concurrent region %q failed: %w", spec.ConcurrentID, firstErr)
	}
	return spec.JoinNode, nil
}

// runBranchUntilJoin runs nodes along a single concurrent branch starting
// from startNode and terminating when it reaches stopAtNode (the join) or
// its context is canceled. Each node is executed with the same
// rundbRecordNodeStart/executeWithRetry/rundbRecordNodeComplete/
// rundbCaptureNodeArtifacts sequence as the main loop. Edge selection uses
// resolveNextHop just like the main loop.
//
// Branches do NOT execute the join node itself — they hand control back to
// the caller, which processes the join normally after all branches complete.
func (e *Engine) runBranchUntilJoin(ctx context.Context, startNode, stopAtNode string, nodeRetries map[string]int, nodeOutcomes map[string]runtime.Outcome) branchResult {
	res := branchResult{StartNode: startNode}
	current := startNode
	for {
		// Cancellation check (fail-fast from a sibling).
		if ctxErr := ctx.Err(); ctxErr != nil {
			res.Err = ctxErr
			return res
		}

		if current == stopAtNode {
			res.LastNode = current
			return res
		}

		node := e.Graph.Nodes[current]
		if node == nil {
			res.Err = fmt.Errorf("branch: missing node %q", current)
			res.FailedNode = current
			return res
		}

		// Reject nested concurrent regions and loops inside a concurrent
		// region to keep the semantics simple for now. These are graph
		// validation errors but we also guard at runtime.
		handlerType := shapeToType(node.Shape())
		if handlerType == "concurrent.split" || handlerType == "concurrent.join" {
			res.Err = fmt.Errorf("branch: nested concurrent regions not supported (node %q)", current)
			res.FailedNode = current
			return res
		}
		if handlerType == "loop.begin" || handlerType == "loop.end" {
			res.Err = fmt.Errorf("branch: loops inside concurrent regions not supported (node %q)", current)
			res.FailedNode = current
			return res
		}

		// Execute this node using the same path as the main runLoop.
		e.cxdbStageStarted(ctx, node)
		attempt := nodeRetries[node.ID] + 1
		nodeDBID := e.rundbRecordNodeStart(node.ID, attempt, resolvedHandlerTypeName(e, node.ID))
		out, err := e.executeWithRetry(ctx, node, nodeRetries)
		if err != nil {
			res.Err = err
			res.FailedNode = node.ID
			return res
		}
		e.rundbRecordProviderIfAgent(node.ID, attempt)
		e.cxdbStageFinished(ctx, node, out)
		e.rundbRecordNodeComplete(nodeDBID, out)
		e.rundbCaptureNodeArtifacts(nodeDBID, node.ID)

		nodeOutcomes[node.ID] = out
		res.Completed = append(res.Completed, node.ID)
		res.LastNode = node.ID

		if out.Status == runtime.StatusFail {
			res.Err = fmt.Errorf("branch node %s failed: %s", node.ID, out.FailureReason)
			res.FailedNode = node.ID
			res.FailedReason = out.FailureReason
			return res
		}

		// Edge selection. Branches use the same next-hop logic as the main
		// loop but only follow a single edge at a time — no nested splits.
		failureClass := classifyFailureClass(out)
		nextHop, edgeErr := resolveNextHop(e.Graph, node.ID, out, e.Context, failureClass, e.appendProgress)
		if edgeErr != nil {
			res.Err = edgeErr
			res.FailedNode = node.ID
			return res
		}
		if nextHop == nil || nextHop.Edge == nil {
			// No further edges — branch terminates without reaching the join.
			res.Err = fmt.Errorf("branch from %q did not reach join %q", startNode, stopAtNode)
			res.FailedNode = node.ID
			return res
		}
		e.rundbRecordEdgeDecision(node.ID, nextHop.Edge.To, nextHop.Edge.Label(), nextHop.Edge.Condition(), nextHop.SelectionMeta.Method)
		current = nextHop.Edge.To
	}
}
