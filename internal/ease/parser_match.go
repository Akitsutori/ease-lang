package ease

import (
	"fmt"
	"strconv"
	"strings"
)

func isMatchVarLengthSeg(seg MatchSegment) bool {
	return seg.Kind == MatchSegWildcard || (seg.Kind == MatchSegCapture && seg.FixedWidth == 0)
}

func (p *Parser) parseMatchPattern() (MatchPattern, error) {
	tok := p.current()
	switch tok.Type {
	case TOK_STRING:
		p.advance()
		var segs []MatchSegment
		if tok.Lexeme != "" {
			segs = append(segs, MatchSegment{
				Kind:    MatchSegLiteral,
				Literal: tok.Lexeme,
				Line:    tok.Line,
				Col:     tok.Col,
			})
		}
		return MatchPattern{
			posInfo:  posInfo{Line: tok.Line, Col: tok.Col},
			Raw:      tok.Lexeme,
			Segments: segs,
		}, nil

	case TOK_INTERP_STRING:
		p.advance()
		segments := make([]MatchSegment, 0, len(tok.Segments))
		var rawBuilder strings.Builder
		for _, seg := range tok.Segments {
			if !seg.IsExpr {
				rawBuilder.WriteString(seg.Text)
				if seg.Text == "" {
					continue
				}
				segments = append(segments, MatchSegment{
					Kind:    MatchSegLiteral,
					Literal: seg.Text,
					Line:    seg.Line,
					Col:     seg.Col,
				})
				continue
			}

			rawBuilder.WriteByte('{')
			rawBuilder.WriteString(seg.Text)
			rawBuilder.WriteByte('}')

			subTokens, err := NewLexer(seg.Text).Tokenize()
			if err != nil {
				if eErr, ok := err.(*EaseError); ok {
					errLine, errCol := offsetSourcePos(eErr.Line, eErr.Col, seg.Line, seg.Col)
					return MatchPattern{}, newError("SyntaxError", errLine, errCol, p.source, eErr.Message, eErr.Hint)
				}
				return MatchPattern{}, err
			}
			offsetTokenPositions(subTokens, seg.Line, seg.Col)
			subParser := &Parser{
				tokens:        subTokens,
				source:        p.source,
				depth:         p.depth,
				funcArities:   p.funcArities,
				moduleArities: p.moduleArities,
			}

			if subParser.check(TOK_DOT) {
				subParser.advance()
				if !subParser.match(TOK_DOT) || !subParser.match(TOK_DOT) || !subParser.check(TOK_EOF) {
					cur := subParser.current()
					return MatchPattern{}, newError(
						"SyntaxError",
						cur.Line,
						cur.Col,
						p.source,
						"Invalid wildcard syntax in match pattern",
						"Use '{...}' to discard unmatched text, e.g. \"ERROR: {msg} ({...})\".",
					)
				}
				segments = append(segments, MatchSegment{
					Kind: MatchSegWildcard,
					Line: seg.Line,
					Col:  seg.Col,
				})
				continue
			}

			if !subParser.check(TOK_IDENT) {
				cur := subParser.current()
				return MatchPattern{}, newError(
					"SyntaxError",
					cur.Line,
					cur.Col,
					p.source,
					fmt.Sprintf("Expected capture variable name inside '{...}' in match pattern, got %q", cur.Lexeme),
					"Use '{name}', '{name as number}', '{name: 2}', or '{...}'.",
				)
			}
			nameTok := subParser.advance()
			if nameTok.Lexeme == "it" {
				return MatchPattern{}, newError(
					"SyntaxError",
					nameTok.Line,
					nameTok.Col,
					p.source,
					"Cannot use reserved name 'it' as a capture variable in 'match'",
					"Choose a descriptive variable name such as '{item}' or '{val}'.",
				)
			}

			var fixedWidth int
			if subParser.match(TOK_COLON) {
				numTok := subParser.current()
				if numTok.Type != TOK_NUMBER {
					return MatchPattern{}, newError(
						"SyntaxError",
						numTok.Line,
						numTok.Col,
						p.source,
						fmt.Sprintf("Expected positive integer width after ':' in match capture, got %q", numTok.Lexeme),
						fmt.Sprintf("Write '{%s: N}' with a whole number >= 1, e.g. '{%s: 2}'.", nameTok.Lexeme, nameTok.Lexeme),
					)
				}
				subParser.advance()
				n, convErr := strconv.Atoi(numTok.Lexeme)
				if convErr != nil || n < 1 {
					return MatchPattern{}, newError(
						"SyntaxError",
						numTok.Line,
						numTok.Col,
						p.source,
						fmt.Sprintf("Match capture width after ':' must be a positive integer (>= 1), got %q", numTok.Lexeme),
						fmt.Sprintf("Write '{%s: N}' with a whole number >= 1, e.g. '{%s: 2}'.", nameTok.Lexeme, nameTok.Lexeme),
					)
				}
				fixedWidth = n
			}

			var castType string
			var explicitCast bool
			var typeLine, typeCol int
			if subParser.match(TOK_AS) {
				explicitCast = true
				castType, typeLine, typeCol, err = subParser.parseTypeTarget(
					"Expected type ('number', 'text', or a union) after 'as' in match capture",
					fmt.Sprintf("Write '{%s as number}' or '{%s as <UnionType>}'.", nameTok.Lexeme, nameTok.Lexeme),
				)
				if err != nil {
					return MatchPattern{}, err
				}
			}

			if !subParser.check(TOK_EOF) {
				extra := subParser.current()
				return MatchPattern{}, newError(
					"SyntaxError",
					extra.Line,
					extra.Col,
					p.source,
					fmt.Sprintf("Unexpected %q in match pattern placeholder", extra.Lexeme),
					"Supported forms: '{name}', '{name: N}', '{name as type}', '{name: N as type}', or '{...}'.",
				)
			}

			segments = append(segments, MatchSegment{
				Kind:         MatchSegCapture,
				VarName:      nameTok.Lexeme,
				FixedWidth:   fixedWidth,
				CastType:     castType,
				ExplicitCast: explicitCast,
				TypeLine:     typeLine,
				TypeCol:      typeCol,
				Line:         seg.Line,
				Col:          seg.Col,
			})
		}

		for i := 0; i+1 < len(segments); i++ {
			if isMatchVarLengthSeg(segments[i]) && isMatchVarLengthSeg(segments[i+1]) {
				return MatchPattern{}, newError(
					"SyntaxError",
					segments[i+1].Line,
					segments[i+1].Col,
					p.source,
					"Adjacent variable-length placeholders in match pattern must be separated by literal text or a fixed width ': N'",
					"Add delimiter characters between placeholders (e.g. \"{a}-{b}\") or set a fixed rune width (e.g. \"{a: 2}{b}\").",
				)
			}
		}

		// Propagate the first capture's CastType onto repeated untyped backreference captures.
		firstCast := make(map[string]string)
		for i := range segments {
			if segments[i].Kind != MatchSegCapture {
				continue
			}
			prev, seen := firstCast[segments[i].VarName]
			if !seen {
				firstCast[segments[i].VarName] = segments[i].CastType
			} else if !segments[i].ExplicitCast && prev != "" {
				segments[i].CastType = prev
			}
		}

		return MatchPattern{
			posInfo:  posInfo{Line: tok.Line, Col: tok.Col},
			Raw:      rawBuilder.String(),
			Segments: segsOrSlice(segments),
		}, nil

	default:
		return MatchPattern{}, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			fmt.Sprintf("Expected pattern string in 'match' branch, got %q", tok.Lexeme),
			"Start each 'match' branch with a quoted pattern string, e.g. \"KEY={val}\" -> val.",
		)
	}
}

func (p *Parser) parseMatchElseBlock(matchTok Token, isExpr bool) ([]Stmt, bool, error) {
	if !p.match(TOK_ELSE) {
		return nil, false, nil
	}
	var elseBody []Stmt
	if arrowTok := p.current(); p.match(TOK_ARROW) {
		if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
			cur := p.current()
			if isExpr {
				return nil, true, newError(
					"SyntaxError",
					cur.Line,
					cur.Col,
					p.source,
					"'match' 'else -> <value>' branch cannot be empty",
					"Write 'else -> <expression>' on the same line.",
				)
			}
			return nil, true, newError(
				"SyntaxError",
				cur.Line,
				cur.Col,
				p.source,
				"'match' 'else -> <value | end>' branch cannot be empty",
				"Write 'else -> <value>' on one line or use a multi-line 'else ... end' block.",
			)
		}
		if isExpr {
			elseExpr, err := p.parseExpr()
			if err != nil {
				return nil, true, err
			}
			if err := p.expectStmtEnd("'match' else branch"); err != nil {
				return nil, true, err
			}
			elLine, elCol := elseExpr.Pos()
			elseBody = append(elseBody, &ExprStmt{posInfo: posInfo{Line: elLine, Col: elCol}, Expression: elseExpr})
		} else {
			action, err := p.parseArrowAction(arrowTok)
			if err != nil {
				return nil, true, err
			}
			elseBody = append(elseBody, action)
		}
		p.skipNewlines()
		return elseBody, true, nil
	}

	if !p.match(TOK_NEWLINE) {
		cur := p.current()
		hint := "Write 'else -> <action>' or start a block on the next line."
		if isExpr {
			hint = "Write 'else -> <expression>' or start a block on the next line."
		}
		return nil, true, newError(
			"SyntaxError",
			cur.Line,
			cur.Col,
			p.source,
			fmt.Sprintf("Expected '->' or newline after 'else' in 'match', got %q", cur.Lexeme),
			hint,
		)
	}
	p.skipNewlines()
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, true, newError(
				"SyntaxError",
				matchTok.Line,
				matchTok.Col,
				p.source,
				"'match ... else' block is missing 'end'",
				"Close every 'match ... else' block with 'end'.",
			)
		}
		s, err := p.parseStmt()
		if err != nil {
			return nil, true, err
		}
		elseBody = append(elseBody, s)
		p.skipNewlines()
	}
	if isExpr {
		if len(elseBody) == 0 {
			return nil, true, newError(
				"SyntaxError",
				matchTok.Line,
				matchTok.Col,
				p.source,
				"'match' expression 'else' block must end with an expression",
				"Add a fallback expression before 'end', or write 'else -> <expression>'.",
			)
		}
		if _, ok := elseBody[len(elseBody)-1].(*ExprStmt); !ok {
			elLine, elCol := elseBody[len(elseBody)-1].Pos()
			return nil, true, newError(
				"SyntaxError",
				elLine,
				elCol,
				p.source,
				"'match' expression 'else' block must end with an expression",
				"End the 'else' block with the fallback value expression.",
			)
		}
	}
	return elseBody, true, nil
}

func segsOrSlice(s []MatchSegment) []MatchSegment {
	if s == nil {
		return []MatchSegment{}
	}
	return s
}

func (p *Parser) parseMatchStmt() (Stmt, error) {
	if err := p.enterDepth(); err != nil {
		return nil, err
	}
	defer p.exitDepth()

	matchTok := p.advance() // consume `match`
	target, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if !p.match(TOK_NEWLINE) {
		tok := p.current()
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Expected newline after 'match' target",
			"Write 'match <expression>' followed by pattern branches on new lines, closing with 'end'.",
		)
	}
	p.skipNewlines()

	var arms []MatchArmStmt
	for !p.check(TOK_END) && !p.check(TOK_ELSE) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				matchTok.Line,
				matchTok.Col,
				p.source,
				"'match' block is missing 'end'",
				"Close every 'match' block with 'end'.",
			)
		}
		armTok := p.current()
		pat, err := p.parseMatchPattern()
		if err != nil {
			return nil, err
		}
		if arrowTok := p.current(); p.match(TOK_ARROW) {
			if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
				cur := p.current()
				return nil, newError(
					"SyntaxError",
					cur.Line,
					cur.Col,
					p.source,
					"'match' branch '-> <value | end>' cannot be empty",
					"Put a value expression or 'end' on the same line as '->', or start an effect block on the next line without '->'.",
				)
			}
			action, err := p.parseArrowAction(arrowTok)
			if err != nil {
				return nil, err
			}
			arms = append(arms, MatchArmStmt{
				Line:    armTok.Line,
				Col:     armTok.Col,
				Pattern: pat,
				Action:  action,
			})
			p.skipNewlines()
		} else if p.match(TOK_NEWLINE) {
			p.skipNewlines()
			var armBody []Stmt
			for !p.check(TOK_STRING) && !p.check(TOK_INTERP_STRING) && !p.check(TOK_ELSE) && !p.check(TOK_END) {
				if p.check(TOK_EOF) {
					return nil, newError(
						"SyntaxError",
						matchTok.Line,
						matchTok.Col,
						p.source,
						"'match' block is missing 'end'",
						"Close every 'match' block with 'end'.",
					)
				}
				s, err := p.parseStmt()
				if err != nil {
					return nil, err
				}
				armBody = append(armBody, s)
				p.skipNewlines()
			}
			if len(armBody) == 0 {
				cur := p.current()
				return nil, newError(
					"SyntaxError",
					cur.Line,
					cur.Col,
					p.source,
					"Effect arm in 'match' cannot be empty",
					"Add one or more statements below the pattern, or use '-> <value | end>'.",
				)
			}
			arms = append(arms, MatchArmStmt{
				Line:    armTok.Line,
				Col:     armTok.Col,
				Pattern: pat,
				Body:    armBody,
			})
		} else {
			cur := p.current()
			return nil, newError(
				"SyntaxError",
				cur.Line,
				cur.Col,
				p.source,
				fmt.Sprintf("Expected '->' or newline after match pattern, got %q", cur.Lexeme),
				"Write '\"pattern\" -> <value | end>' or put statements on the next line without '->'.",
			)
		}
	}

	if len(arms) == 0 {
		return nil, newError(
			"SyntaxError",
			matchTok.Line,
			matchTok.Col,
			p.source,
			"'match' block requires at least one pattern branch",
			"Add at least one pattern branch before 'end'.",
		)
	}

	elseBody, _, err := p.parseMatchElseBlock(matchTok, false)
	if err != nil {
		return nil, err
	}

	if !p.match(TOK_END) {
		tok := p.current()
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Expected 'end' to close 'match' block",
			"Close every 'match' block with 'end'.",
		)
	}
	if err := p.expectStmtEnd("'match' block"); err != nil {
		return nil, err
	}

	return &MatchStmt{
		posInfo:  posInfo{Line: matchTok.Line, Col: matchTok.Col},
		Target:   target,
		Arms:     arms,
		ElseBody: elseBody,
	}, nil
}

func (p *Parser) parseMatchExpr() (Expr, error) {
	if err := p.enterDepth(); err != nil {
		return nil, err
	}
	defer p.exitDepth()

	prevDisallowAs := p.disallowAs
	prevDisallowWhere := p.disallowWhere
	prevDisallowTopIs := p.disallowTopIs
	prevStopChainedIs := p.stopChainedIs
	prevInCallArg := p.inCallArg
	p.disallowAs = false
	p.disallowWhere = false
	p.disallowTopIs = false
	p.stopChainedIs = false
	p.inCallArg = false
	defer func() {
		p.disallowAs = prevDisallowAs
		p.disallowWhere = prevDisallowWhere
		p.disallowTopIs = prevDisallowTopIs
		p.stopChainedIs = prevStopChainedIs
		p.inCallArg = prevInCallArg
	}()

	matchTok := p.advance() // consume `match`
	target, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if !p.match(TOK_NEWLINE) {
		tok := p.current()
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Expected newline after 'match' target",
			"Write 'match <expression>' followed by pattern branches on new lines, closing with 'end'.",
		)
	}
	p.skipNewlines()

	var arms []MatchArmExpr
	for !p.check(TOK_END) && !p.check(TOK_ELSE) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				matchTok.Line,
				matchTok.Col,
				p.source,
				"'match' block is missing 'end'",
				"Close every 'match' block with 'end'.",
			)
		}
		armTok := p.current()
		pat, err := p.parseMatchPattern()
		if err != nil {
			return nil, err
		}
		if !p.match(TOK_ARROW) {
			cur := p.current()
			return nil, newError(
				"SyntaxError",
				cur.Line,
				cur.Col,
				p.source,
				fmt.Sprintf("Expected '->' after match pattern, got %q", cur.Lexeme),
				"Write '\"pattern\" -> <expression>' on each branch.",
			)
		}
		if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
			cur := p.current()
			return nil, newError(
				"SyntaxError",
				cur.Line,
				cur.Col,
				p.source,
				"'match' expression branch '-> <value>' cannot be empty",
				"Write '\"pattern\" -> <expression>' on the same line.",
			)
		}
		valExpr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if err := p.expectStmtEnd("'match' branch"); err != nil {
			return nil, err
		}
		arms = append(arms, MatchArmExpr{
			Line:    armTok.Line,
			Col:     armTok.Col,
			Pattern: pat,
			Value:   valExpr,
		})
		p.skipNewlines()
	}

	if len(arms) == 0 {
		return nil, newError(
			"SyntaxError",
			matchTok.Line,
			matchTok.Col,
			p.source,
			"'match' block requires at least one pattern branch",
			"Add at least one '\"pattern\" -> <expression>' branch before 'end'.",
		)
	}

	elseBody, hasElse, err := p.parseMatchElseBlock(matchTok, true)
	if err != nil {
		return nil, err
	}

	if !p.match(TOK_END) {
		tok := p.current()
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Expected 'end' to close 'match' block",
			"Close every 'match' block with 'end'.",
		)
	}

	return &MatchExpr{
		posInfo:  posInfo{Line: matchTok.Line, Col: matchTok.Col},
		Target:   target,
		Arms:     arms,
		HasElse:  hasElse,
		ElseBody: elseBody,
	}, nil
}

// Expressions (precedence climbing)
