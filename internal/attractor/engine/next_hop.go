package engine

import (
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/cond"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// ProgressFunc is an optional callback for emitting structured progress events.
// Routing functions accept this to log decisions without depending on the engine.
type ProgressFunc func(map[string]any)

type nextHopSource string

const (
	nextHopSourceEdgeSelection nextHopSource = "edge_selection"
	nextHopSourceConditional   nextHopSource = "conditional"
	nextHopSourceRetryTarget   nextHopSource = "retry_target"
)

type resolvedNextHop struct {
	Edge              *model.Edge
	Source            nextHopSource
	RetryTargetSource string
}

func resolveNextHop(g *model.Graph, from string, out runtime.Outcome, ctx *runtime.Context, failureClass string, progress ProgressFunc) (*resolvedNextHop, error) {
	if g == nil {
		return nil, nil
	}
	from = strings.TrimSpace(from)
	if from == "" {
		return nil, nil
	}

	if isFanInFailureLike(g, from, out.Status) {
		conditional, err := selectMatchingConditionalEdge(g, from, out, ctx, progress)
		if err != nil {
			return nil, err
		}
		if conditional != nil {
			return &resolvedNextHop{
				Edge:   conditional,
				Source: nextHopSourceConditional,
			}, nil
		}

		// Deterministic failures must not follow retry_target — the same
		// branches will fail again, creating an infinite loop.
		if normalizedFailureClassOrDefault(failureClass) == failureClassDeterministic {
			return nil, nil
		}

		target, source := resolveRetryTargetWithSource(g, from)
		if target != "" {
			synthetic := model.NewEdge(from, target)
			synthetic.Attrs = map[string]string{
				"kilroy.synthetic_edge":       string(nextHopSourceRetryTarget),
				"kilroy.retry_target_source":  source,
				"kilroy.retry_target_applies": "fan_in_failure",
			}
			return &resolvedNextHop{
				Edge:              synthetic,
				Source:            nextHopSourceRetryTarget,
				RetryTargetSource: source,
			}, nil
		}
		return nil, nil
	}

	next, err := selectNextEdge(g, from, out, ctx)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return nil, nil
	}
	return &resolvedNextHop{
		Edge:   next,
		Source: nextHopSourceEdgeSelection,
	}, nil
}

func selectMatchingConditionalEdge(g *model.Graph, from string, out runtime.Outcome, ctx *runtime.Context, progress ProgressFunc) (*model.Edge, error) {
	edges := g.Outgoing(from)
	if len(edges) == 0 {
		return nil, nil
	}
	var condMatched []*model.Edge
	for _, e := range edges {
		if e == nil {
			continue
		}
		c := strings.TrimSpace(e.Condition())
		if c == "" {
			continue
		}
		ok, err := cond.Evaluate(c, out, ctx)
		if err != nil {
			return nil, err
		}
		if progress != nil {
			progress(map[string]any{
				"event":     "edge_condition_evaluated",
				"node_id":   from,
				"edge_to":   e.To,
				"condition": c,
				"matched":   ok,
			})
		}
		if ok {
			condMatched = append(condMatched, e)
		}
	}
	if len(condMatched) == 0 {
		return nil, nil
	}
	return bestEdge(condMatched), nil
}

func resolveRetryTargetWithSource(g *model.Graph, nodeID string) (target string, source string) {
	if g == nil {
		return "", ""
	}
	n := g.Nodes[strings.TrimSpace(nodeID)]
	if n == nil {
		return "", ""
	}
	if t := strings.TrimSpace(n.Attr("retry_target", "")); t != "" {
		return t, "node.retry_target"
	}
	if t := strings.TrimSpace(n.Attr("fallback_retry_target", "")); t != "" {
		return t, "node.fallback_retry_target"
	}
	if t := strings.TrimSpace(g.Attrs["retry_target"]); t != "" {
		return t, "graph.retry_target"
	}
	if t := strings.TrimSpace(g.Attrs["fallback_retry_target"]); t != "" {
		return t, "graph.fallback_retry_target"
	}
	return "", ""
}

func resolveRetryTarget(g *model.Graph, nodeID string) string {
	target, _ := resolveRetryTargetWithSource(g, nodeID)
	return target
}

func isFanInFailureLike(g *model.Graph, from string, status runtime.StageStatus) bool {
	if status != runtime.StatusFail && status != runtime.StatusRetry {
		return false
	}
	n := g.Nodes[from]
	if n == nil {
		return false
	}
	t := strings.TrimSpace(n.TypeOverride())
	if t == "" {
		t = shapeToType(n.Shape())
	}
	return t == "parallel.fan_in"
}
