package style

import (
	"fmt"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

type SelectorKind int

const (
	SelectorUniversal SelectorKind = iota
	SelectorShape
	SelectorClass
	SelectorID
)

type Rule struct {
	Kind        SelectorKind
	Value       string // id/class/shape; empty for universal
	Specificity int    // universal(0) < shape(1) < class(2) < id(3)
	Order       int    // source order (0..n-1)
	Decls       map[string]string
}

func ParseStylesheet(src string) ([]Rule, error) {
	p := &ssParser{s: src}
	return p.parse()
}

func ApplyStylesheet(g *model.Graph, rules []Rule) error {
	if g == nil {
		return fmt.Errorf("graph is nil")
	}
	if len(rules) == 0 {
		return nil
	}

	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		applyToNode(g, n, rules)
	}
	return nil
}

func applyToNode(g *model.Graph, n *model.Node, rules []Rule) {
	// Only set properties that are missing.
	props := []string{"llm_model", "llm_provider", "reasoning_effort"}
	for _, prop := range props {
		if _, ok := n.Attrs[prop]; ok {
			continue
		}
		bestSpec := -1
		bestOrder := -1
		bestVal := ""
		for _, r := range rules {
			if !ruleMatchesNode(r, n) {
				continue
			}
			v, ok := r.Decls[prop]
			if !ok {
				continue
			}
			if r.Specificity > bestSpec || (r.Specificity == bestSpec && r.Order > bestOrder) {
				bestSpec = r.Specificity
				bestOrder = r.Order
				bestVal = v
			}
		}
		if bestSpec >= 0 {
			n.Attrs[prop] = bestVal
			continue
		}
		// Graph-level defaults (optional / best-effort).
		if g != nil {
			if v, ok := g.Attrs[prop]; ok && strings.TrimSpace(v) != "" {
				n.Attrs[prop] = v
			}
		}
	}
}

func ruleMatchesNode(r Rule, n *model.Node) bool {
	switch r.Kind {
	case SelectorUniversal:
		return true
	case SelectorID:
		return n.ID == r.Value
	case SelectorClass:
		for _, c := range n.ClassList() {
			if c == r.Value {
				return true
			}
		}
		return false
	case SelectorShape:
		return n.Shape() == r.Value
	default:
		return false
	}
}

type ssParser struct {
	s    string
	i    int
	rule int
}

func (p *ssParser) parse() ([]Rule, error) {
	var rules []Rule
	for {
		p.skipSpace()
		if p.eof() {
			return rules, nil
		}
		r, err := p.parseRule()
		if err != nil {
			return nil, err
		}
		r.Order = p.rule
		p.rule++
		rules = append(rules, r)
	}
}

func (p *ssParser) parseRule() (Rule, error) {
	kind, val, spec, err := p.parseSelector()
	if err != nil {
		return Rule{}, err
	}
	p.skipSpace()
	if !p.consume("{") {
		return Rule{}, p.errf("expected '{' after selector")
	}
	decls := map[string]string{}
	for {
		p.skipSpace()
		if p.consume("}") {
			break
		}
		prop, err := p.parseIdent()
		if err != nil {
			return Rule{}, err
		}
		if prop != "llm_model" && prop != "llm_provider" && prop != "reasoning_effort" {
			return Rule{}, p.errf("unknown property %q", prop)
		}
		p.skipSpace()
		if !p.consume(":") {
			return Rule{}, p.errf("expected ':' after property")
		}
		p.skipSpace()
		val, err := p.parseValue()
		if err != nil {
			return Rule{}, err
		}
		decls[prop] = val
		p.skipSpace()
		_ = p.consume(";") // optional (including trailing before '}')
	}
	return Rule{Kind: kind, Value: val, Specificity: spec, Decls: decls}, nil
}

func (p *ssParser) parseSelector() (SelectorKind, string, int, error) {
	if p.consume("*") {
		return SelectorUniversal, "", 0, nil
	}
	if p.consume("#") {
		id, err := p.parseIdent()
		if err != nil {
			return 0, "", 0, err
		}
		return SelectorID, id, 3, nil
	}
	if p.consume(".") {
		class, err := p.parseClassName()
		if err != nil {
			return 0, "", 0, err
		}
		return SelectorClass, class, 2, nil
	}
	shape, err := p.parseIdentLike()
	if err != nil {
		return 0, "", 0, err
	}
	return SelectorShape, shape, 1, nil
}

func (p *ssParser) parseIdent() (string, error) {
	p.skipSpace()
	start := p.i
	if p.eof() || !isIdentStart(rune(p.s[p.i])) {
		return "", p.errf("expected identifier")
	}
	p.i++
	for !p.eof() && isIdentContinue(rune(p.s[p.i])) {
		p.i++
	}
	return p.s[start:p.i], nil
}

func (p *ssParser) parseClassName() (string, error) {
	p.skipSpace()
	start := p.i
	if p.eof() {
		return "", p.errf("expected class name")
	}
	for !p.eof() {
		r := rune(p.s[p.i])
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			p.i++
			continue
		}
		break
	}
	if start == p.i {
		return "", p.errf("expected class name")
	}
	return p.s[start:p.i], nil
}

func (p *ssParser) parseIdentLike() (string, error) {
	// For shape selectors and bare values, accept [A-Za-z0-9_-.]+
	p.skipSpace()
	start := p.i
	if p.eof() {
		return "", p.errf("expected identifier")
	}
	for !p.eof() {
		r := rune(p.s[p.i])
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			p.i++
			continue
		}
		break
	}
	if start == p.i {
		return "", p.errf("expected identifier")
	}
	return strings.TrimSpace(p.s[start:p.i]), nil
}

func (p *ssParser) parseValue() (string, error) {
	if p.eof() {
		return "", p.errf("expected value")
	}
	if p.s[p.i] == '"' {
		return p.parseString()
	}
	// Read until ';' or '}'.
	start := p.i
	for !p.eof() {
		if p.s[p.i] == ';' || p.s[p.i] == '}' {
			break
		}
		p.i++
	}
	return strings.TrimSpace(p.s[start:p.i]), nil
}

func (p *ssParser) parseString() (string, error) {
	if !p.consume(`"`) {
		return "", p.errf("expected string")
	}
	var b strings.Builder
	for !p.eof() {
		ch := p.s[p.i]
		p.i++
		if ch == '"' {
			return b.String(), nil
		}
		if ch == '\\' {
			if p.eof() {
				return "", p.errf("unterminated escape")
			}
			esc := p.s[p.i]
			p.i++
			switch esc {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			default:
				b.WriteByte('\\')
				b.WriteByte(esc)
			}
			continue
		}
		b.WriteByte(ch)
	}
	return "", p.errf("unterminated string")
}

func (p *ssParser) skipSpace() {
	for !p.eof() {
		switch p.s[p.i] {
		case ' ', '\n', '\r', '\t':
			p.i++
		default:
			return
		}
	}
}

func (p *ssParser) consume(lit string) bool {
	if strings.HasPrefix(p.s[p.i:], lit) {
		p.i += len(lit)
		return true
	}
	return false
}

func (p *ssParser) eof() bool { return p.i >= len(p.s) }

func (p *ssParser) errf(format string, args ...any) error {
	return fmt.Errorf("stylesheet parse: "+format+" (at %d)", append(args, p.i)...)
}

func isIdentStart(r rune) bool {
	return r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func isIdentContinue(r rune) bool {
	return isIdentStart(r) || (r >= '0' && r <= '9')
}
