package dot

import (
	"fmt"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

// Parse parses a constrained DOT digraph into the Attractor graph model.
// It strips comments, flattens subgraphs, applies scoped node/edge defaults,
// expands chained edges, and derives CSS-like classes from subgraph labels.
func Parse(dotSource []byte) (*model.Graph, error) {
	clean, err := stripComments(dotSource)
	if err != nil {
		return nil, err
	}
	p := &parser{
		lx: newLexer(clean),
	}
	if err := p.read(); err != nil {
		return nil, err
	}
	return p.parseGraph()
}

type parser struct {
	lx   *lexer
	peek token
	has  bool
}

func (p *parser) read() error {
	if p.has {
		return nil
	}
	tok, err := p.lx.next()
	if err != nil {
		return err
	}
	p.peek = tok
	p.has = true
	return nil
}

func (p *parser) next() (token, error) {
	if err := p.read(); err != nil {
		return token{}, err
	}
	tok := p.peek
	p.has = false
	return tok, nil
}

func (p *parser) expectSymbol(sym string) error {
	tok, err := p.next()
	if err != nil {
		return err
	}
	if tok.typ != tokenSymbol || tok.lit != sym {
		return fmt.Errorf("dot parse: expected %q, got %q at %d", sym, tok.lit, tok.pos)
	}
	return nil
}

func (p *parser) expectIdent(lit string) error {
	tok, err := p.next()
	if err != nil {
		return err
	}
	if tok.typ != tokenIdent || tok.lit != lit {
		return fmt.Errorf("dot parse: expected %q, got %q at %d", lit, tok.lit, tok.pos)
	}
	return nil
}

func (p *parser) parseGraph() (*model.Graph, error) {
	// digraph <Identifier> { ... }
	if err := p.expectIdent("digraph"); err != nil {
		return nil, err
	}
	nameTok, err := p.next()
	if err != nil {
		return nil, err
	}
	if nameTok.typ != tokenIdent {
		return nil, fmt.Errorf("dot parse: expected graph identifier, got %q at %d", nameTok.lit, nameTok.pos)
	}
	g := model.NewGraph(nameTok.lit)
	if err := p.expectSymbol("{"); err != nil {
		return nil, err
	}

	root := newScope(nil)
	if err := p.parseStatements(g, root); err != nil {
		return nil, err
	}
	if err := p.expectSymbol("}"); err != nil {
		return nil, err
	}
	// Spec constraint: one digraph per file. Allow an optional trailing semicolon,
	// then require EOF.
	_ = p.consumeOptionalSemicolon()
	if err := p.read(); err != nil {
		return nil, err
	}
	if p.peek.typ != tokenEOF {
		return nil, fmt.Errorf("dot parse: trailing tokens after graph end at %d", p.peek.pos)
	}
	return g, nil
}

type scope struct {
	parent       *scope
	nodeDefaults map[string]string
	edgeDefaults map[string]string

	subgraphLabel string
	nodeIDs       map[string]struct{} // nodes declared within this subgraph (including nested)
}

func newScope(parent *scope) *scope {
	s := &scope{
		parent:       parent,
		nodeDefaults: map[string]string{},
		edgeDefaults: map[string]string{},
		nodeIDs:      map[string]struct{}{},
	}
	if parent != nil {
		for k, v := range parent.nodeDefaults {
			s.nodeDefaults[k] = v
		}
		for k, v := range parent.edgeDefaults {
			s.edgeDefaults[k] = v
		}
	}
	return s
}

func (s *scope) recordNode(id string) {
	for cur := s; cur != nil; cur = cur.parent {
		cur.nodeIDs[id] = struct{}{}
	}
}

func (p *parser) parseStatements(g *model.Graph, sc *scope) error {
	for {
		if err := p.read(); err != nil {
			return err
		}
		if p.peek.typ == tokenEOF {
			return fmt.Errorf("dot parse: unexpected EOF (missing '}')")
		}
		if p.peek.typ == tokenSymbol && p.peek.lit == "}" {
			// end of this scope
			if sc.parent != nil {
				p.applySubgraphLabelClass(g, sc)
			}
			return nil
		}

		tok, err := p.next()
		if err != nil {
			return err
		}

		if tok.typ != tokenIdent {
			return fmt.Errorf("dot parse: expected identifier, got %q at %d", tok.lit, tok.pos)
		}

		switch tok.lit {
		case "graph":
			attrs, err := p.parseAttrBlock()
			if err != nil {
				return err
			}
			for k, v := range attrs {
				g.Attrs[k] = v
			}
			_ = p.consumeOptionalSemicolon()
			continue
		case "node":
			attrs, err := p.parseAttrBlock()
			if err != nil {
				return err
			}
			for k, v := range attrs {
				sc.nodeDefaults[k] = v
			}
			_ = p.consumeOptionalSemicolon()
			continue
		case "edge":
			attrs, err := p.parseAttrBlock()
			if err != nil {
				return err
			}
			for k, v := range attrs {
				sc.edgeDefaults[k] = v
			}
			_ = p.consumeOptionalSemicolon()
			continue
		case "subgraph":
			// subgraph <Identifier>? { ... }
			if err := p.read(); err != nil {
				return err
			}
			if p.peek.typ == tokenIdent {
				// subgraph id (ignored, optional)
				if _, err := p.next(); err != nil {
					return err
				}
			}
			if err := p.expectSymbol("{"); err != nil {
				return err
			}
			sub := newScope(sc)
			if err := p.parseStatements(g, sub); err != nil {
				return err
			}
			if err := p.expectSymbol("}"); err != nil {
				return err
			}
			p.applySubgraphLabelClass(g, sub)
			continue
		default:
			// Could be:
			// - Graph attr decl: key = value
			// - Node stmt: id [attrs]
			// - Edge stmt: id -> id (-> id)* [attrs]
			if err := p.read(); err != nil {
				return err
			}
			if p.peek.typ == tokenSymbol && p.peek.lit == "=" {
				// graph attr decl
				if _, err := p.next(); err != nil {
					return err
				}
				val, err := p.parseTopLevelValue()
				if err != nil {
					return err
				}
				// Special case: label inside subgraph scope becomes a derived class source.
				if sc.parent != nil && tok.lit == "label" {
					sc.subgraphLabel = val
				} else {
					g.Attrs[tok.lit] = val
				}
				_ = p.consumeOptionalSemicolon()
				continue
			}

			if p.peek.typ == tokenSymbol && p.peek.lit == "->" {
				// Edge statement.
				from := tok.lit
				chain := []string{from}
				for {
					// consume ->
					if _, err := p.next(); err != nil {
						return err
					}
					toTok, err := p.next()
					if err != nil {
						return err
					}
					if toTok.typ != tokenIdent {
						return fmt.Errorf("dot parse: expected edge target identifier, got %q at %d", toTok.lit, toTok.pos)
					}
					chain = append(chain, toTok.lit)

					if err := p.read(); err != nil {
						return err
					}
					if !(p.peek.typ == tokenSymbol && p.peek.lit == "->") {
						break
					}
				}

				attrs := map[string]string{}
				if err := p.read(); err != nil {
					return err
				}
				if p.peek.typ == tokenSymbol && p.peek.lit == "[" {
					var err error
					attrs, err = p.parseAttrBlock()
					if err != nil {
						return err
					}
				}

				for i := 0; i+1 < len(chain); i++ {
					e := model.NewEdge(chain[i], chain[i+1])
					// Defaults first, then explicit attrs.
					for k, v := range sc.edgeDefaults {
						e.Attrs[k] = v
					}
					for k, v := range attrs {
						e.Attrs[k] = v
					}
					if err := g.AddEdge(e); err != nil {
						return err
					}
				}

				_ = p.consumeOptionalSemicolon()
				continue
			}

			// Node statement.
			nodeAttrs := map[string]string{}
			if p.peek.typ == tokenSymbol && p.peek.lit == "[" {
				var err error
				nodeAttrs, err = p.parseAttrBlock()
				if err != nil {
					return err
				}
			}

			n := model.NewNode(tok.lit)
			n.Order = len(g.Nodes)
			for k, v := range sc.nodeDefaults {
				n.Attrs[k] = v
			}
			for k, v := range nodeAttrs {
				n.Attrs[k] = v
			}
			if err := g.AddNode(n); err != nil {
				return err
			}
			sc.recordNode(n.ID)
			_ = p.consumeOptionalSemicolon()
		}
	}
}

func (p *parser) consumeOptionalSemicolon() error {
	if err := p.read(); err != nil {
		return err
	}
	if p.peek.typ == tokenSymbol && p.peek.lit == ";" {
		_, err := p.next()
		return err
	}
	return nil
}

func (p *parser) parseAttrBlock() (map[string]string, error) {
	if err := p.expectSymbol("["); err != nil {
		return nil, err
	}
	attrs := map[string]string{}
	for {
		if err := p.read(); err != nil {
			return nil, err
		}
		if p.peek.typ == tokenSymbol && p.peek.lit == "]" {
			_, _ = p.next()
			return attrs, nil
		}

		key, err := p.parseQualifiedKey()
		if err != nil {
			return nil, err
		}
		if err := p.expectSymbol("="); err != nil {
			return nil, err
		}
		val, err := p.parseAttrValue()
		if err != nil {
			return nil, err
		}
		attrs[key] = val

		// Next: ',' or ']'
		if err := p.read(); err != nil {
			return nil, err
		}
		if p.peek.typ == tokenSymbol && p.peek.lit == "," {
			_, _ = p.next()
			continue
		}
		// Allow optional trailing comma before ']'.
		if p.peek.typ == tokenSymbol && p.peek.lit == "]" {
			continue
		}
		// Anything else is a syntax error.
		return nil, fmt.Errorf("dot parse: expected ',' or ']', got %q at %d", p.peek.lit, p.peek.pos)
	}
}

func (p *parser) parseAttrValue() (string, error) {
	// Values inside attr blocks can be unquoted and may contain punctuation (DoD: accept quoted/unquoted).
	if err := p.read(); err != nil {
		return "", err
	}
	if p.peek.typ == tokenString {
		tok, err := p.next()
		if err != nil {
			return "", err
		}
		return tok.lit, nil
	}
	var parts []string
	for {
		if err := p.read(); err != nil {
			return "", err
		}
		if p.peek.typ == tokenSymbol && (p.peek.lit == "," || p.peek.lit == "]") {
			break
		}
		tok, err := p.next()
		if err != nil {
			return "", err
		}
		switch tok.typ {
		case tokenIdent:
			parts = append(parts, tok.lit)
		case tokenSymbol:
			switch tok.lit {
			case "-", ".", ":", "/":
				parts = append(parts, tok.lit)
			default:
				return "", fmt.Errorf("dot parse: unexpected token in value: %q at %d", tok.lit, tok.pos)
			}
		default:
			return "", fmt.Errorf("dot parse: unexpected token in value: %q at %d", tok.lit, tok.pos)
		}
	}
	val := strings.TrimSpace(strings.Join(parts, ""))
	if val == "" {
		return "", fmt.Errorf("dot parse: empty attr value")
	}
	return val, nil
}

func (p *parser) parseTopLevelValue() (string, error) {
	// Parse a value for a top-level graph attr decl (key = value).
	// Handles quoted strings, identifiers, and negative numbers/floats/durations
	// (e.g. -1, -3.5, -900s) where the lexer emits '-' as a separate symbol token.
	if err := p.read(); err != nil {
		return "", err
	}
	if p.peek.typ == tokenString {
		tok, err := p.next()
		if err != nil {
			return "", err
		}
		return tok.lit, nil
	}
	if p.peek.typ == tokenSymbol && p.peek.lit == "-" {
		// Negative number: consume '-' then the numeric ident.
		neg, err := p.next()
		if err != nil {
			return "", err
		}
		numTok, err := p.next()
		if err != nil {
			return "", err
		}
		if numTok.typ != tokenIdent {
			return "", fmt.Errorf("dot parse: expected number after '-', got %q at %d", numTok.lit, numTok.pos)
		}
		return neg.lit + numTok.lit, nil
	}
	if p.peek.typ == tokenIdent {
		tok, err := p.next()
		if err != nil {
			return "", err
		}
		return tok.lit, nil
	}
	return "", fmt.Errorf("dot parse: expected value after '=', got %q at %d", p.peek.lit, p.peek.pos)
}

func (p *parser) parseQualifiedKey() (string, error) {
	// Key is Identifier or QualifiedId (Identifier '.' Identifier)+.
	first, err := p.next()
	if err != nil {
		return "", err
	}
	if first.typ != tokenIdent {
		return "", fmt.Errorf("dot parse: expected identifier key, got %q at %d", first.lit, first.pos)
	}
	key := first.lit
	for {
		if err := p.read(); err != nil {
			return "", err
		}
		if p.peek.typ == tokenSymbol && p.peek.lit == "." {
			// The lexer currently doesn't emit '.' as a symbol; keep for future.
			_, _ = p.next()
			part, err := p.next()
			if err != nil {
				return "", err
			}
			if part.typ != tokenIdent {
				return "", fmt.Errorf("dot parse: expected identifier after '.', got %q at %d", part.lit, part.pos)
			}
			key += "." + part.lit
			continue
		}
		break
	}
	return key, nil
}

func (p *parser) applySubgraphLabelClass(g *model.Graph, sc *scope) {
	if sc == nil {
		return
	}
	lbl := strings.TrimSpace(sc.subgraphLabel)
	if lbl == "" {
		return
	}
	class := deriveClassFromLabel(lbl)
	if class == "" {
		return
	}
	for id := range sc.nodeIDs {
		n := g.Nodes[id]
		if n == nil {
			continue
		}
		n.Classes = append(n.Classes, class)
	}
}

func deriveClassFromLabel(label string) string {
	label = strings.ToLower(label)
	label = strings.ReplaceAll(label, " ", "-")
	var b strings.Builder
	for _, r := range label {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}
