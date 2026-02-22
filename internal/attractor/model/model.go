package model

import (
	"fmt"
	"sort"
	"strings"
)

// Graph is the parsed, flattened representation of a DOT digraph pipeline.
// Attributes are stored as raw strings; callers can parse types as needed.
type Graph struct {
	Name  string
	Attrs map[string]string

	Nodes map[string]*Node
	Edges []*Edge // declaration order (expanded for chained edges)

	outgoing map[string][]*Edge
	incoming map[string][]*Edge
}

func NewGraph(name string) *Graph {
	return &Graph{
		Name:     name,
		Attrs:    map[string]string{},
		Nodes:    map[string]*Node{},
		Edges:    []*Edge{},
		outgoing: map[string][]*Edge{},
		incoming: map[string][]*Edge{},
	}
}

func (g *Graph) AddNode(n *Node) error {
	if n == nil {
		return fmt.Errorf("node is nil")
	}
	if n.ID == "" {
		return fmt.Errorf("node id is empty")
	}
	if _, exists := g.Nodes[n.ID]; exists {
		// Graphviz allows multiple node statements; we treat as merge-latest-wins.
		// Keep original order/index stable for tie-breaks by preserving the original Node.Order.
		existing := g.Nodes[n.ID]
		for k, v := range n.Attrs {
			existing.Attrs[k] = v
		}
		existing.Classes = mergeClasses(existing.Classes, n.Classes)
		return nil
	}
	g.Nodes[n.ID] = n
	return nil
}

func (g *Graph) AddEdge(e *Edge) error {
	if e == nil {
		return fmt.Errorf("edge is nil")
	}
	if e.From == "" || e.To == "" {
		return fmt.Errorf("edge endpoints must be non-empty")
	}
	e.Order = len(g.Edges)
	g.Edges = append(g.Edges, e)
	g.outgoing[e.From] = append(g.outgoing[e.From], e)
	g.incoming[e.To] = append(g.incoming[e.To], e)
	return nil
}

func (g *Graph) Outgoing(nodeID string) []*Edge {
	return g.outgoing[nodeID]
}

func (g *Graph) Incoming(nodeID string) []*Edge {
	return g.incoming[nodeID]
}

// AllNodeIDs returns node IDs in lexical order (for stable output).
func (g *Graph) AllNodeIDs() []string {
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

type Node struct {
	ID      string
	Attrs   map[string]string
	Classes []string
	Order   int // first-seen declaration order (stable)
}

func NewNode(id string) *Node {
	return &Node{
		ID:    id,
		Attrs: map[string]string{},
	}
}

func (n *Node) Attr(key, def string) string {
	if n == nil {
		return def
	}
	if v, ok := n.Attrs[key]; ok {
		return v
	}
	return def
}

func (n *Node) Shape() string {
	shape := n.Attr("shape", "")
	if shape == "" {
		return "box"
	}
	return shape
}

func (n *Node) TypeOverride() string {
	return n.Attr("type", "")
}

func (n *Node) Label() string {
	lbl := n.Attr("label", "")
	if lbl == "" {
		return n.ID
	}
	return lbl
}

func (n *Node) Prompt() string {
	if p := n.Attr("prompt", ""); p != "" {
		return p
	}
	return n.Attr("llm_prompt", "")
}

func (n *Node) ClassList() []string {
	// classes may come from:
	// - node attr "class" (comma separated)
	// - derived subgraph label classes applied by the parser
	out := append([]string{}, n.Classes...)
	if raw := n.Attr("class", ""); raw != "" {
		for _, c := range strings.Split(raw, ",") {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			out = append(out, c)
		}
	}
	return dedupeStable(out)
}

type Edge struct {
	From  string
	To    string
	Attrs map[string]string
	Order int // declaration order (stable)
}

func NewEdge(from, to string) *Edge {
	return &Edge{
		From:  from,
		To:    to,
		Attrs: map[string]string{},
		Order: -1,
	}
}

func (e *Edge) Attr(key, def string) string {
	if e == nil {
		return def
	}
	if v, ok := e.Attrs[key]; ok {
		return v
	}
	return def
}

func (e *Edge) Label() string {
	return e.Attr("label", "")
}

func (e *Edge) Condition() string {
	return e.Attr("condition", "")
}

func mergeClasses(a, b []string) []string {
	out := append([]string{}, a...)
	out = append(out, b...)
	return dedupeStable(out)
}

func dedupeStable(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
