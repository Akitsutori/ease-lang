package ease

import (
	"fmt"
	"strings"
	"unicode"
)

// Lexer transforms Ease source text into a slice of Tokens.
type Lexer struct {
	source string
	runes  []rune
	pos    int
	line   int
	col    int
}

// NewLexer creates a Lexer for `source`.
func NewLexer(source string) *Lexer {
	return &Lexer{
		source: source,
		runes:  []rune(source),
		pos:    0,
		line:   1,
		col:    1,
	}
}

func (l *Lexer) eof() bool {
	return l.pos >= len(l.runes)
}

func (l *Lexer) peek() rune {
	if l.eof() {
		return 0
	}
	return l.runes[l.pos]
}

func (l *Lexer) peekNext() rune {
	if l.pos+1 >= len(l.runes) {
		return 0
	}
	return l.runes[l.pos+1]
}

func (l *Lexer) advance() rune {
	if l.eof() {
		return 0
	}
	r := l.runes[l.pos]
	l.pos++
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

func isIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isIdentPart(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// Tokenize scans the complete input into tokens, ending with TOK_EOF.
func (l *Lexer) Tokenize() ([]Token, error) {
	var tokens []Token

	for !l.eof() {
		r := l.peek()

		if r == ' ' || r == '\t' || r == '\r' {
			l.advance()
			continue
		}

		if r == '\n' {
			line, col := l.line, l.col
			l.advance()
			tokens = append(tokens, Token{Type: TOK_NEWLINE, Lexeme: "\\n", Line: line, Col: col})
			continue
		}

		if r == '#' {
			for !l.eof() && l.peek() != '\n' {
				l.advance()
			}
			continue
		}

		if r == '"' {
			tok, err := l.scanString()
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, tok)
			continue
		}

		if unicode.IsDigit(r) {
			tok, err := l.scanNumber()
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, tok)
			continue
		}

		if isIdentStart(r) {
			tokens = append(tokens, l.scanIdentOrKeyword())
			continue
		}

		line, col := l.line, l.col
		switch r {
		case '(':
			l.advance()
			tokens = append(tokens, Token{Type: TOK_LPAREN, Lexeme: "(", Line: line, Col: col})
		case ')':
			l.advance()
			tokens = append(tokens, Token{Type: TOK_RPAREN, Lexeme: ")", Line: line, Col: col})
		case ',':
			l.advance()
			tokens = append(tokens, Token{Type: TOK_COMMA, Lexeme: ",", Line: line, Col: col})
		case '.':
			l.advance()
			tokens = append(tokens, Token{Type: TOK_DOT, Lexeme: ".", Line: line, Col: col})
		case '|':
			l.advance()
			tokens = append(tokens, Token{Type: TOK_PIPE, Lexeme: "|", Line: line, Col: col})
		case '+':
			l.advance()
			tokens = append(tokens, Token{Type: TOK_PLUS, Lexeme: "+", Line: line, Col: col})
		case '-':
			l.advance()
			if l.peek() == '>' {
				l.advance()
				tokens = append(tokens, Token{Type: TOK_ARROW, Lexeme: "->", Line: line, Col: col})
			} else {
				tokens = append(tokens, Token{Type: TOK_MINUS, Lexeme: "-", Line: line, Col: col})
			}
		case '*':
			l.advance()
			tokens = append(tokens, Token{Type: TOK_STAR, Lexeme: "*", Line: line, Col: col})
		case '/':
			l.advance()
			tokens = append(tokens, Token{Type: TOK_SLASH, Lexeme: "/", Line: line, Col: col})
		case '%':
			l.advance()
			tokens = append(tokens, Token{Type: TOK_MOD, Lexeme: "%", Line: line, Col: col})
		case '=':
			l.advance()
			if l.peek() == '=' {
				l.advance()
				tokens = append(tokens, Token{Type: TOK_EQ, Lexeme: "==", Line: line, Col: col})
			} else {
				tokens = append(tokens, Token{Type: TOK_ASSIGN, Lexeme: "=", Line: line, Col: col})
			}
		case '!':
			l.advance()
			if l.peek() == '=' {
				l.advance()
				tokens = append(tokens, Token{Type: TOK_NEQ, Lexeme: "!=", Line: line, Col: col})
			} else {
				return nil, newError("SyntaxError", line, col, l.source, "Unexpected '!'", "Use 'not <expr>' for boolean negation, or '!=' for inequality.")
			}
		case '<':
			l.advance()
			if l.peek() == '=' {
				l.advance()
				tokens = append(tokens, Token{Type: TOK_LTE, Lexeme: "<=", Line: line, Col: col})
			} else {
				tokens = append(tokens, Token{Type: TOK_LT, Lexeme: "<", Line: line, Col: col})
			}
		case '>':
			l.advance()
			if l.peek() == '=' {
				l.advance()
				tokens = append(tokens, Token{Type: TOK_GTE, Lexeme: ">=", Line: line, Col: col})
			} else {
				tokens = append(tokens, Token{Type: TOK_GT, Lexeme: ">", Line: line, Col: col})
			}
		default:
			return nil, newError("SyntaxError", line, col, l.source, fmt.Sprintf("Unexpected character %q", r), "Remove or quote unknown characters.")
		}
	}

	tokens = append(tokens, Token{Type: TOK_EOF, Lexeme: "", Line: l.line, Col: l.col})
	return tokens, nil
}

func (l *Lexer) scanString() (Token, error) {
	line, col := l.line, l.col
	l.advance() // opening quote
	var b strings.Builder
	var segments []StringSegment
	hasInterp := false

	for !l.eof() {
		r := l.peek()
		if r == '"' {
			l.advance()
			if !hasInterp {
				return Token{Type: TOK_STRING, Lexeme: b.String(), Line: line, Col: col}, nil
			}
			if b.Len() > 0 {
				segments = append(segments, StringSegment{IsExpr: false, Text: b.String(), Line: line, Col: col})
			}
			return Token{Type: TOK_INTERP_STRING, Lexeme: "", Line: line, Col: col, Segments: segments}, nil
		}
		if r == '\n' {
			return Token{}, newError("SyntaxError", line, col, l.source, "Unterminated string literal", "Close strings with a matching '\"' before the end of the line.")
		}
		if r == '{' {
			hasInterp = true
			if b.Len() > 0 {
				segments = append(segments, StringSegment{IsExpr: false, Text: b.String(), Line: line, Col: col})
				b.Reset()
			}
			exprLine, exprCol := l.line, l.col
			l.advance() // '{'
			exprText, err := l.scanInterpExpr(exprLine, exprCol)
			if err != nil {
				return Token{}, err
			}
			segments = append(segments, StringSegment{IsExpr: true, Text: exprText, Line: exprLine, Col: exprCol + 1})
			continue
		}
		if r == '\\' {
			l.advance()
			if l.eof() {
				break
			}
			esc := l.advance()
			switch esc {
			case 'n':
				b.WriteRune('\n')
			case 't':
				b.WriteRune('\t')
			case '"':
				b.WriteRune('"')
			case '\\':
				b.WriteRune('\\')
			case '{':
				b.WriteRune('{')
			case '}':
				b.WriteRune('}')
			default:
				b.WriteRune(esc)
			}
			continue
		}
		b.WriteRune(l.advance())
	}

	return Token{}, newError("SyntaxError", line, col, l.source, "Unterminated string literal", "Add a closing '\"' at the end of your text.")
}

func (l *Lexer) scanInterpExpr(openLine, openCol int) (string, error) {
	var expr strings.Builder
	depth := 1
	inString := false

	for !l.eof() {
		r := l.peek()
		if r == '\n' {
			return "", newError("SyntaxError", openLine, openCol, l.source, "Unterminated '{...}' in string interpolation", "Close the interpolation with '}' or escape a literal brace with '\\{'.")
		}
		if inString {
			if r == '\\' {
				expr.WriteRune(l.advance())
				if !l.eof() && l.peek() != '\n' {
					expr.WriteRune(l.advance())
				}
				continue
			}
			if r == '"' {
				inString = false
			}
			expr.WriteRune(l.advance())
			continue
		}
		if r == '"' {
			inString = true
			expr.WriteRune(l.advance())
			continue
		}
		if r == '{' {
			depth++
			expr.WriteRune(l.advance())
			continue
		}
		if r == '}' {
			depth--
			l.advance()
			if depth == 0 {
				trimmed := strings.TrimSpace(expr.String())
				if trimmed == "" {
					return "", newError("SyntaxError", openLine, openCol, l.source, "Empty '{}' in string interpolation", "Put an expression inside '{...}', e.g. \"Hello {name}\", or escape with '\\{'.")
				}
				return expr.String(), nil
			}
			expr.WriteRune('}')
			continue
		}
		expr.WriteRune(l.advance())
	}

	return "", newError("SyntaxError", openLine, openCol, l.source, "Unterminated '{...}' in string interpolation", "Close the interpolation with '}' or escape a literal brace with '\\{'.")
}

func (l *Lexer) scanNumber() (Token, error) {
	line, col := l.line, l.col
	var b strings.Builder
	hasDot := false

	for !l.eof() {
		r := l.peek()
		if unicode.IsDigit(r) {
			b.WriteRune(l.advance())
			continue
		}
		if r == '.' && !hasDot && unicode.IsDigit(l.peekNext()) {
			hasDot = true
			b.WriteRune(l.advance())
			continue
		}
		break
	}

	return Token{Type: TOK_NUMBER, Lexeme: b.String(), Line: line, Col: col}, nil
}

func (l *Lexer) scanIdentOrKeyword() Token {
	line, col := l.line, l.col
	var b strings.Builder

	for !l.eof() && isIdentPart(l.peek()) {
		b.WriteRune(l.advance())
	}
	word := b.String()

	// Special cognitive-ease hyphenated keyword: `is-not`
	if word == "is" && l.pos+3 < len(l.runes) && string(l.runes[l.pos:l.pos+4]) == "-not" {
		afterIdx := l.pos + 4
		if afterIdx >= len(l.runes) || !isIdentPart(l.runes[afterIdx]) {
			for range 4 {
				l.advance()
			}
			return Token{Type: TOK_IS_NOT, Lexeme: "is-not", Line: line, Col: col}
		}
	}

	if kwType, ok := keywords[word]; ok {
		return Token{Type: kwType, Lexeme: word, Line: line, Col: col}
	}
	return Token{Type: TOK_IDENT, Lexeme: word, Line: line, Col: col}
}
