package engine

import (
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

var validFidelityModes = map[string]bool{
	"full":           true,
	"truncate":       true,
	"compact":        true,
	"summary:low":    true,
	"summary:medium": true,
	"summary:high":   true,
}

func resolveFidelityAndThread(g *model.Graph, incoming *model.Edge, node *model.Node) (mode string, threadKey string) {
	mode = resolveFidelityMode(g, incoming, node)
	if mode == "full" {
		threadKey = resolveThreadKey(g, incoming, node)
	}
	return mode, threadKey
}

func resolveFidelityMode(g *model.Graph, incoming *model.Edge, node *model.Node) string {
	candidate := ""
	if incoming != nil {
		candidate = strings.TrimSpace(incoming.Attr("fidelity", ""))
	}
	if candidate == "" && node != nil {
		candidate = strings.TrimSpace(node.Attr("fidelity", ""))
	}
	if candidate == "" && g != nil {
		candidate = strings.TrimSpace(g.Attrs["default_fidelity"])
		if candidate == "" {
			candidate = strings.TrimSpace(g.Attrs["context_fidelity_default"])
		}
	}
	if candidate == "" {
		candidate = "compact"
	}
	candidate = strings.ToLower(candidate)
	if validFidelityModes[candidate] {
		return candidate
	}
	return "compact"
}

func resolveThreadKey(g *model.Graph, incoming *model.Edge, node *model.Node) string {
	// Precedence (attractor-spec):
	// 1) node.thread_id
	// 2) edge.thread_id
	// 3) graph-level default thread
	// 4) derived class from enclosing subgraph
	// 5) fallback: previous node ID
	if node != nil {
		if v := strings.TrimSpace(node.Attr("thread_id", "")); v != "" {
			return v
		}
	}
	if incoming != nil {
		if v := strings.TrimSpace(incoming.Attr("thread_id", "")); v != "" {
			return v
		}
	}
	if g != nil {
		if v := strings.TrimSpace(g.Attrs["thread_id"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(g.Attrs["context_thread_default"]); v != "" {
			return v
		}
	}
	if node != nil {
		if classes := node.ClassList(); len(classes) > 0 && strings.TrimSpace(classes[0]) != "" {
			return strings.TrimSpace(classes[0])
		}
	}
	if incoming != nil && strings.TrimSpace(incoming.From) != "" {
		return strings.TrimSpace(incoming.From)
	}
	if node != nil {
		return node.ID
	}
	return ""
}
