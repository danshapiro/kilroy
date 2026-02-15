package dot

import (
	"fmt"
	"strings"
)

type tokenType int

const (
	tokenEOF tokenType = iota
	tokenIdent
	tokenString
	tokenSymbol
)

type token struct {
	typ tokenType
	lit string
	pos int // byte offset in source (for diagnostics)
}

type lexer struct {
	src []byte
	i   int
}

func newLexer(src []byte) *lexer {
	return &lexer{src: src}
}

func (l *lexer) next() (token, error) {
	l.skipSpace()
	if l.i >= len(l.src) {
		return token{typ: tokenEOF, pos: l.i}, nil
	}

	ch := l.src[l.i]

	// Symbols and operators.
	switch ch {
	case '{', '}', '[', ']', ',', ';', '=', '.', ':', '/':
		l.i++
		return token{typ: tokenSymbol, lit: string(ch), pos: l.i - 1}, nil
	case '-':
		// Could be "->" or a negative number/duration.
		if l.i+1 < len(l.src) && l.src[l.i+1] == '>' {
			l.i += 2
			return token{typ: tokenSymbol, lit: "->", pos: l.i - 2}, nil
		}
		// Treat '-' as a symbol so we can accept unquoted values like "claude-opus-4-6"
		// and also negative numbers (assembled by the parser).
		l.i++
		return token{typ: tokenSymbol, lit: "-", pos: l.i - 1}, nil
	case '"':
		return l.lexString()
	}

	// Identifier or number/duration (unquoted value).
	if isIdentStart(rune(ch)) {
		return l.lexIdent()
	}
	if isDigit(ch) {
		return l.lexBareNumberish()
	}

	return token{}, fmt.Errorf("dot lexer: unexpected character %q at %d", ch, l.i)
}

func (l *lexer) skipSpace() {
	for l.i < len(l.src) {
		r := rune(l.src[l.i])
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			l.i++
			continue
		}
		break
	}
}

func (l *lexer) lexIdent() (token, error) {
	start := l.i
	l.i++
	for l.i < len(l.src) {
		r := rune(l.src[l.i])
		if isIdentContinue(r) {
			l.i++
			continue
		}
		break
	}
	return token{typ: tokenIdent, lit: string(l.src[start:l.i]), pos: start}, nil
}

func (l *lexer) lexBareNumberish() (token, error) {
	// Parses: [-]?\d+(\.\d+)?[A-Za-z]*  (covers int, float, duration like 900s, 250ms)
	start := l.i
	for l.i < len(l.src) && isDigit(l.src[l.i]) {
		l.i++
	}
	if l.i < len(l.src) && l.src[l.i] == '.' {
		l.i++
		if l.i >= len(l.src) || !isDigit(l.src[l.i]) {
			return token{}, fmt.Errorf("dot lexer: malformed float at %d", start)
		}
		for l.i < len(l.src) && isDigit(l.src[l.i]) {
			l.i++
		}
	}
	for l.i < len(l.src) && isAlpha(l.src[l.i]) {
		l.i++
	}
	return token{typ: tokenIdent, lit: string(l.src[start:l.i]), pos: start}, nil
}

func (l *lexer) lexString() (token, error) {
	start := l.i
	// Consume opening quote.
	l.i++
	var sb strings.Builder
	for l.i < len(l.src) {
		ch := l.src[l.i]
		l.i++
		if ch == '"' {
			return token{typ: tokenString, lit: sb.String(), pos: start}, nil
		}
		if ch == '\\' {
			if l.i >= len(l.src) {
				return token{}, fmt.Errorf("dot lexer: unterminated escape at %d", l.i)
			}
			esc := l.src[l.i]
			l.i++
			switch esc {
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			default:
				// Preserve unknown escape sequences verbatim.
				sb.WriteByte('\\')
				sb.WriteByte(esc)
			}
			continue
		}
		sb.WriteByte(ch)
	}
	return token{}, fmt.Errorf("dot lexer: unterminated string starting at %d", start)
}

func isIdentStart(r rune) bool {
	return r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func isIdentContinue(r rune) bool {
	return isIdentStart(r) || (r >= '0' && r <= '9')
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
func isAlpha(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
