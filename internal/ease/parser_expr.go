package ease

import (
	"fmt"
	"strconv"
	"strings"
)

func (p *Parser) parseLogicTail(left Expr) (Expr, error) {
	for p.check(TOK_AND) || p.check(TOK_OR) {
		opTok := p.advance()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		line, col := left.Pos()
		left = &BinaryExpr{posInfo: posInfo{Line: line, Col: col}, Left: left, Op: opTok.Lexeme, Right: right}
	}
	return left, nil
}

func (p *Parser) parseQuantifierExpr() (Expr, error) {
	qTok := p.advance() // consume `any` or `none`
	prevDisallowWhere := p.disallowWhere
	p.disallowWhere = true
	src, err := p.parsePostfix()
	p.disallowWhere = prevDisallowWhere
	if err != nil {
		return nil, err
	}
	if !p.match(TOK_WHERE) {
		cur := p.current()
		return nil, newError(
			"SyntaxError",
			cur.Line,
			cur.Col,
			p.source,
			fmt.Sprintf("Expected 'where <condition>' after '%s <list>', got %q", qTok.Lexeme, cur.Lexeme),
			fmt.Sprintf("Write: %s <list> where <condition>", qTok.Lexeme),
		)
	}
	prevDisallowTopIs := p.disallowTopIs
	prevStopChainedIs := p.stopChainedIs
	prevDisallowArrow := p.disallowArrow
	p.disallowTopIs = false
	p.stopChainedIs = false
	p.disallowArrow = true
	cond, err := p.parseOr()
	p.disallowTopIs = prevDisallowTopIs
	p.stopChainedIs = prevStopChainedIs
	p.disallowArrow = prevDisallowArrow
	if err != nil {
		return nil, err
	}
	return &QuantifierExpr{
		posInfo: posInfo{Line: qTok.Line, Col: qTok.Col},
		Kind:    qTok.Lexeme,
		Source:  src,
		Cond:    cond,
	}, nil
}

func (p *Parser) parseInboxExpr() (Expr, error) {
	inboxTok := p.advance() // consume `inbox`
	if !p.match(TOK_AS) {
		cur := p.current()
		return nil, newError(
			"SyntaxError",
			cur.Line,
			cur.Col,
			p.source,
			fmt.Sprintf("Expected 'as <type>' after 'inbox', got %q", cur.Lexeme),
			"Example: jobs = inbox as text or chat = inbox as ClientEvent",
		)
	}
	typeName, typeLine, typeCol, err := p.parseTypeTarget(
		"Expected element type ('text', 'number', a 'def' record, or union) after 'inbox as'",
		"Example: inbox as number or inbox as ClientEvent",
	)
	if err != nil {
		return nil, err
	}
	return &InboxExpr{
		posInfo:  posInfo{Line: inboxTok.Line, Col: inboxTok.Col},
		ElemType: typeName,
		TypeLine: typeLine,
		TypeCol:  typeCol,
	}, nil
}

func (p *Parser) parseExpr() (Expr, error) {
	if err := p.enterDepth(); err != nil {
		return nil, err
	}
	defer p.exitDepth()

	return p.parseProject()
}

func (p *Parser) parseProject() (Expr, error) {
	left, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	for !p.disallowArrow && p.check(TOK_ARROW) && p.peekNext().Type != TOK_NEWLINE && p.peekNext().Type != TOK_EOF {
		arrowTok := p.advance()
		body, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		left = &ProjectExpr{
			posInfo: posInfo{Line: arrowTok.Line, Col: arrowTok.Col},
			Source:  left,
			Body:    body,
		}
	}
	return left, nil
}

func (p *Parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.match(TOK_OR) {
		line, col := left.Pos()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{posInfo: posInfo{Line: line, Col: col}, Left: left, Op: "or", Right: right}
	}
	return left, nil
}

func (p *Parser) parseAnd() (Expr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.match(TOK_AND) {
		line, col := left.Pos()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{posInfo: posInfo{Line: line, Col: col}, Left: left, Op: "and", Right: right}
	}
	return left, nil
}

func (p *Parser) parseNot() (Expr, error) {
	if p.check(TOK_NOT) {
		tok := p.advance()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Op: "not", Right: right}, nil
	}
	return p.parseComparison()
}

func (p *Parser) isTypeAssertionEnd(t TokenType) bool {
	switch t {
	case TOK_NEWLINE, TOK_EOF, TOK_END, TOK_ARROW, TOK_AND, TOK_OR, TOK_RPAREN:
		return true
	default:
		return false
	}
}

func (p *Parser) parseComparison() (Expr, error) {
	left, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	hasComparison := false
	for {
		if p.check(TOK_LT) || p.check(TOK_LTE) || p.check(TOK_GT) || p.check(TOK_GTE) {
			opTok := p.advance()
			right, err := p.parseTerm()
			if err != nil {
				return nil, err
			}
			left = &BinaryExpr{posInfo: posInfo{Line: opTok.Line, Col: opTok.Col}, Left: left, Op: opTok.Lexeme, Right: right}
			hasComparison = true
			continue
		}
		if !p.disallowTopIs && !(hasComparison && p.stopChainedIs) && p.check(TOK_IS) {
			opTok := p.advance()
			negated := p.match(TOK_NOT)
			isBareListType := p.check(TOK_LIST_KW) && p.isTypeAssertionEnd(p.peekNext().Type)
			if p.check(TOK_TEXT) || p.check(TOK_NUMBER_KW) || isBareListType {
				typeTok := p.advance()
				left = &TypeCheckExpr{
					posInfo:  posInfo{Line: opTok.Line, Col: opTok.Col},
					Target:   left,
					TypeName: typeTok.Lexeme,
					Negated:  negated,
				}
				hasComparison = true
				continue
			}
			right, err := p.parseTerm()
			if err != nil {
				return nil, err
			}
			op := "is"
			if negated {
				op = "is-not"
			}
			left = &BinaryExpr{posInfo: posInfo{Line: opTok.Line, Col: opTok.Col}, Left: left, Op: op, Right: right}
			hasComparison = true
			continue
		}
		break
	}
	return left, nil
}

func (p *Parser) parseTerm() (Expr, error) {
	left, err := p.parseFactor()
	if err != nil {
		return nil, err
	}
	for p.check(TOK_PLUS) || p.check(TOK_MINUS) {
		opTok := p.advance()
		right, err := p.parseFactor()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{posInfo: posInfo{Line: opTok.Line, Col: opTok.Col}, Left: left, Op: opTok.Lexeme, Right: right}
	}
	return left, nil
}

func (p *Parser) parseFactor() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.check(TOK_STAR) || p.check(TOK_SLASH) || p.check(TOK_MOD) {
		opTok := p.advance()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{posInfo: posInfo{Line: opTok.Line, Col: opTok.Col}, Left: left, Op: opTok.Lexeme, Right: right}
	}
	return left, nil
}

func (p *Parser) parseUnary() (Expr, error) {
	if p.check(TOK_MINUS) {
		tok := p.advance()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Op: "-", Right: right}, nil
	}
	return p.parsePostfix()
}

func (p *Parser) parsePostfix() (Expr, error) {
	expr, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}

	for {
		if p.inSingleLineWith && p.check(TOK_DOT) && p.pos+2 < len(p.tokens) && p.tokens[p.pos+2].Type == TOK_ASSIGN {
			break
		}
		if p.match(TOK_DOT) {
			if fieldTok, ok := p.matchFieldIdent(); ok {
				if id, ok := expr.(*IdentExpr); ok {
					if modFns, isMod := p.moduleArities[id.Name]; isMod {
						if _, isFn := modFns[fieldTok.Lexeme]; isFn {
							return nil, newError(
								"TypeError",
								fieldTok.Line,
								fieldTok.Col,
								p.source,
								fmt.Sprintf("Cannot access module function %q on '%s' with '.'", fieldTok.Lexeme, id.Name),
								fmt.Sprintf("Use ':' to call module functions, e.g. '%s:%s ...'.", id.Name, fieldTok.Lexeme),
							)
						}
					}
				}
				line, col := expr.Pos()
				expr = &FieldExpr{posInfo: posInfo{Line: line, Col: col}, Target: expr, Field: fieldTok.Lexeme}
				continue
			}
			if p.check(TOK_NUMBER) {
				numTok := p.advance()
				parts := strings.Split(numTok.Lexeme, ".")
				for _, part := range parts {
					idx, err := strconv.Atoi(part)
					if err != nil || idx < 1 {
						if err == nil && idx == 0 {
							return nil, newError(
								"SyntaxError",
								numTok.Line,
								numTok.Col,
								p.source,
								"List indices in Ease are 1-based; use '.1' for the first element",
								"Replace '.0' with '.1' (or use '.(expr)' for computed indices).",
							)
						}
						return nil, newError(
							"SyntaxError",
							numTok.Line,
							numTok.Col,
							p.source,
							fmt.Sprintf("Invalid list index %q after '.'", numTok.Lexeme),
							"Use 1-based integer indices after '.', e.g. cloud.1.vx",
						)
					}
					line, col := expr.Pos()
					expr = &IndexExpr{
						posInfo: posInfo{Line: line, Col: col},
						Target:  expr,
						Index:   &NumberExpr{posInfo: posInfo{Line: numTok.Line, Col: numTok.Col}, Value: float64(idx), Raw: part},
					}
				}
				continue
			}
			if p.match(TOK_LPAREN) {
				idxExpr, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				if !p.match(TOK_RPAREN) {
					tok := p.current()
					return nil, newError(
						"SyntaxError",
						tok.Line,
						tok.Col,
						p.source,
						"Expected ')' after index expression",
						"Example: xs.(it.index)",
					)
				}
				line, col := expr.Pos()
				expr = &IndexExpr{
					posInfo: posInfo{Line: line, Col: col},
					Target:  expr,
					Index:   idxExpr,
				}
				continue
			}
			tok := p.current()
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				fmt.Sprintf("Expected field name or list index after '.', got %q", tok.Lexeme),
				"Example: player.x or cloud.1.vx",
			)
		}

		if !p.disallowWith && p.match(TOK_WITH) {
			var updates []WithFieldUpdate
			var indexUpdates []WithIndexUpdate
			line, col := expr.Pos()
			appendWithEntry := func() error {
				isIdx, fUpd, iUpd, err := p.parseWithEntry()
				if err != nil {
					return err
				}
				if isIdx {
					if len(updates) > 0 {
						return newError(
							"SyntaxError",
							iUpd.Line,
							iUpd.Col,
							p.source,
							"Cannot mix record field updates and list index updates in the same 'with' expression",
							"Use either 'rec with field = val' or 'list with .index = val'.",
						)
					}
					indexUpdates = append(indexUpdates, iUpd)
				} else {
					if len(indexUpdates) > 0 {
						return newError(
							"SyntaxError",
							fUpd.Line,
							fUpd.Col,
							p.source,
							"Cannot mix record field updates and list index updates in the same 'with' expression",
							"Use either 'rec with field = val' or 'list with .index = val'.",
						)
					}
					updates = append(updates, fUpd)
				}
				return nil
			}
			if p.match(TOK_NEWLINE) {
				p.skipNewlines()
				for !p.check(TOK_END) {
					if p.check(TOK_EOF) {
						return nil, newError(
							"SyntaxError",
							line,
							col,
							p.source,
							"Multiline 'with' block is missing 'end'",
							"Close every multiline 'with' block with 'end'.",
						)
					}
					if err := appendWithEntry(); err != nil {
						return nil, err
					}
					if err := p.expectStmtEnd("'with' field"); err != nil {
						return nil, err
					}
					p.skipNewlines()
				}
				p.advance() // consume `end`
			} else {
				prevSingleWith := p.inSingleLineWith
				p.inSingleLineWith = true
				for {
					if err := appendWithEntry(); err != nil {
						p.inSingleLineWith = prevSingleWith
						return nil, err
					}
					if !p.match(TOK_COMMA) {
						break
					}
				}
				p.inSingleLineWith = prevSingleWith
				if err := p.checkMissingWithComma(); err != nil {
					return nil, err
				}
			}
			expr = &WithExpr{posInfo: posInfo{Line: line, Col: col}, Base: expr, Updates: updates, IndexUpdates: indexUpdates}
			continue
		}

		if !p.disallowWhere && p.match(TOK_WHERE) {
			line, col := expr.Pos()
			prevDisallowTopIs := p.disallowTopIs
			prevStopChainedIs := p.stopChainedIs
			p.disallowTopIs = false

			var conditions []Expr
			isBlock := false
			if p.match(TOK_NEWLINE) {
				isBlock = true
				p.stopChainedIs = false
				p.skipNewlines()
				for !p.check(TOK_END) {
					if p.check(TOK_EOF) {
						p.disallowTopIs = prevDisallowTopIs
						p.stopChainedIs = prevStopChainedIs
						return nil, newError(
							"SyntaxError",
							line,
							col,
							p.source,
							"Multiline 'where' block is missing 'end'",
							"Close every multiline 'where' block with 'end'.",
						)
					}
					cond, err := p.parseExpr()
					if err != nil {
						p.disallowTopIs = prevDisallowTopIs
						p.stopChainedIs = prevStopChainedIs
						return nil, err
					}
					conditions = append(conditions, cond)
					if err := p.expectStmtEnd("'where' condition"); err != nil {
						p.disallowTopIs = prevDisallowTopIs
						p.stopChainedIs = prevStopChainedIs
						return nil, err
					}
					p.skipNewlines()
				}
				p.advance() // consume `end`
				if len(conditions) == 0 {
					p.disallowTopIs = prevDisallowTopIs
					p.stopChainedIs = prevStopChainedIs
					return nil, newError(
						"SyntaxError",
						line,
						col,
						p.source,
						"'where' block requires at least one condition",
						"Write one condition per line inside 'where ... end'.",
					)
				}
			} else {
				p.stopChainedIs = prevDisallowTopIs
				prevDisallowArrow := p.disallowArrow
				p.disallowArrow = true
				cond, err := p.parseExpr()
				p.disallowArrow = prevDisallowArrow
				if err != nil {
					p.disallowTopIs = prevDisallowTopIs
					p.stopChainedIs = prevStopChainedIs
					return nil, err
				}
				conditions = []Expr{cond}
			}

			p.disallowTopIs = prevDisallowTopIs
			p.stopChainedIs = prevStopChainedIs
			expr = &WhereExpr{posInfo: posInfo{Line: line, Col: col}, Target: expr, Conditions: conditions, IsBlock: isBlock}
			continue
		}

		if !p.disallowAs && p.match(TOK_AS) {
			typeName, typeLine, typeCol, err := p.parseTypeTarget(
				"Expected target type ('number', 'text', or a 'def' type) after 'as'",
				"Example: ask \"Age?\" as number or raw as billing.invoice",
			)
			if err != nil {
				return nil, err
			}
			var fallback Expr
			var orLine, orCol int
			if p.match(TOK_OR) {
				orTok := p.tokens[p.pos-1]
				orLine, orCol = orTok.Line, orTok.Col
				prevDisallowWhere := p.disallowWhere
				p.disallowWhere = true
				fallback, err = p.parseUnary()
				p.disallowWhere = prevDisallowWhere
				if err != nil {
					return nil, err
				}
			}
			line, col := expr.Pos()
			expr = &CastExpr{
				posInfo:  posInfo{Line: line, Col: col},
				Target:   expr,
				TypeName: typeName,
				TypeLine: typeLine,
				TypeCol:  typeCol,
				Fallback: fallback,
				OrLine:   orLine,
				OrCol:    orCol,
			}
			continue
		}

		if id, ok := expr.(*IdentExpr); ok {
			if p.match(TOK_COLON) {
				modFns, isMod := p.moduleArities[id.Name]
				if !isMod {
					return nil, newError(
						"SyntaxError",
						id.Line,
						id.Col,
						p.source,
						fmt.Sprintf("Unknown module %q", id.Name),
						fmt.Sprintf("Import the module first, e.g.: import \"./%s.ease\" as %s", id.Name, id.Name),
					)
				}
				if !p.check(TOK_IDENT) {
					cur := p.current()
					return nil, newError(
						"SyntaxError",
						cur.Line,
						cur.Col,
						p.source,
						fmt.Sprintf("Expected function name after '%s:', got %q", id.Name, cur.Lexeme),
						fmt.Sprintf("Example: %s:fn arg1", id.Name),
					)
				}
				fnTok := p.advance()
				arity, exists := modFns[fnTok.Lexeme]
				if !exists {
					return nil, newError(
						"SyntaxError",
						fnTok.Line,
						fnTok.Col,
						p.source,
						fmt.Sprintf("Module %q has no function %q", id.Name, fnTok.Lexeme),
						fmt.Sprintf("Check the 'to' functions exported by module %q.", id.Name),
					)
				}
				args, err := p.parseCallArgs(fmt.Sprintf("%s:%s", id.Name, fnTok.Lexeme), arity)
				if err != nil {
					return nil, err
				}
				line, col := id.Pos()
				expr = &CallExpr{posInfo: posInfo{Line: line, Col: col}, Module: id.Name, Callee: fnTok.Lexeme, Args: args}
				continue
			}
			if arity, isFunc := p.funcArities[id.Name]; isFunc {
				args, err := p.parseCallArgs(id.Name, arity)
				if err != nil {
					return nil, err
				}
				line, col := id.Pos()
				if id.Name == "sort" && p.check(TOK_IDENT) && p.current().Lexeme == "by" {
					p.advance() // consume `by`
					var keys []Expr
					prevAs := p.disallowAs
					prevWhere := p.disallowWhere
					prevInCallArg := p.inCallArg
					p.disallowAs = false
					p.disallowWhere = true
					p.inCallArg = false
					for {
						if p.isCallArgBoundary() {
							p.disallowAs = prevAs
							p.disallowWhere = prevWhere
							p.inCallArg = prevInCallArg
							cur := p.current()
							return nil, newError(
								"SyntaxError",
								cur.Line,
								cur.Col,
								p.source,
								"Expected sort key expression after 'by'",
								"Write: sort <list> by <key1> [, <key2> ...]",
							)
						}
						keyExpr, err := p.parseExpr()
						if err != nil {
							p.disallowAs = prevAs
							p.disallowWhere = prevWhere
							p.inCallArg = prevInCallArg
							return nil, err
						}
						keys = append(keys, keyExpr)
						if !p.match(TOK_COMMA) {
							break
						}
					}
					p.disallowAs = prevAs
					p.disallowWhere = prevWhere
					p.inCallArg = prevInCallArg
					expr = &SortByExpr{posInfo: posInfo{Line: line, Col: col}, Target: args[0], Keys: keys}
					continue
				}
				expr = &CallExpr{posInfo: posInfo{Line: line, Col: col}, Callee: id.Name, Args: args}
				continue
			}
		}

		break
	}

	return expr, nil
}

func (p *Parser) isCallArgBoundary() bool {
	switch p.current().Type {
	case TOK_NEWLINE, TOK_EOF, TOK_END, TOK_ELSE, TOK_ARROW, TOK_RPAREN, TOK_COMMA,
		TOK_IS, TOK_AND, TOK_OR, TOK_LT, TOK_LTE, TOK_GT, TOK_GTE,
		TOK_PLUS, TOK_STAR, TOK_SLASH, TOK_MOD, TOK_ASSIGN, TOK_AS, TOK_WITH:
		return true
	default:
		return false
	}
}

func (p *Parser) parseWithEntry() (bool, WithFieldUpdate, WithIndexUpdate, error) {
	if !p.match(TOK_DOT) {
		upd, err := p.parseWithFieldUpdate()
		return false, upd, WithIndexUpdate{}, err
	}
	dotTok := p.tokens[p.pos-1]
	var idxExpr Expr
	switch {
	case p.check(TOK_NUMBER):
		numTok := p.advance()
		if strings.Contains(numTok.Lexeme, ".") {
			return false, WithFieldUpdate{}, WithIndexUpdate{}, newError(
				"SyntaxError",
				numTok.Line,
				numTok.Col,
				p.source,
				fmt.Sprintf("Invalid list index %q after '.' in 'with'", numTok.Lexeme),
				"Use 1-based positive whole-number indices, e.g. xs with .1 = val.",
			)
		}
		idx, err := strconv.Atoi(numTok.Lexeme)
		if err != nil || idx < 1 {
			if err == nil && idx == 0 {
				return false, WithFieldUpdate{}, WithIndexUpdate{}, newError(
					"SyntaxError",
					numTok.Line,
					numTok.Col,
					p.source,
					"List indices in Ease are 1-based; use '.1' for the first element",
					"Replace '.0' with '.1' (or use '.(expr)' for computed indices).",
				)
			}
			return false, WithFieldUpdate{}, WithIndexUpdate{}, newError(
				"SyntaxError",
				numTok.Line,
				numTok.Col,
				p.source,
				fmt.Sprintf("Invalid list index %q after '.' in 'with'", numTok.Lexeme),
				"Use 1-based integer indices, e.g. xs with .1 = val.",
			)
		}
		idxExpr = &NumberExpr{posInfo: posInfo{Line: numTok.Line, Col: numTok.Col}, Value: float64(idx), Raw: numTok.Lexeme}
	case p.check(TOK_IDENT):
		idTok := p.advance()
		idxExpr = &IdentExpr{posInfo: posInfo{Line: idTok.Line, Col: idTok.Col}, Name: idTok.Lexeme}
	case p.match(TOK_LPAREN):
		var err error
		idxExpr, err = p.parseExpr()
		if err != nil {
			return false, WithFieldUpdate{}, WithIndexUpdate{}, err
		}
		if !p.match(TOK_RPAREN) {
			tok := p.current()
			return false, WithFieldUpdate{}, WithIndexUpdate{}, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				"Expected ')' after index expression in 'with'",
				"Example: xs with .(row * 8 + seat) = \"taken\"",
			)
		}
	default:
		tok := p.current()
		return false, WithFieldUpdate{}, WithIndexUpdate{}, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			fmt.Sprintf("Expected list index after '.' in 'with', got %q", tok.Lexeme),
			"Example: xs with .1 = val, xs with .pos = val, or xs with .(i + 1) = val.",
		)
	}
	if !p.match(TOK_ASSIGN) {
		tok := p.current()
		return false, WithFieldUpdate{}, WithIndexUpdate{}, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Expected '=' after list index in 'with'",
			"Example: xs with .1 = <value>",
		)
	}
	valExpr, err := p.parseExpr()
	if err != nil {
		return false, WithFieldUpdate{}, WithIndexUpdate{}, err
	}
	return true, WithFieldUpdate{}, WithIndexUpdate{Line: dotTok.Line, Col: dotTok.Col, Index: idxExpr, Value: valExpr}, nil
}

func (p *Parser) checkMissingWithComma() error {
	cur := p.current()
	if isFieldIdentToken(cur.Type) && p.peekNext().Type == TOK_ASSIGN {
		return newError(
			"SyntaxError",
			cur.Line,
			cur.Col,
			p.source,
			fmt.Sprintf("Missing ',' before '%s =' in single-line 'with'", cur.Lexeme),
			"Separate multiple fields on one line with commas (e.g. 'with a = 1, b = 2'), or use a multi-line 'with\\n  ...\\nend' block.",
		)
	}
	if cur.Type == TOK_DOT && p.pos+2 < len(p.tokens) && p.tokens[p.pos+2].Type == TOK_ASSIGN {
		idxTok := p.tokens[p.pos+1]
		return newError(
			"SyntaxError",
			cur.Line,
			cur.Col,
			p.source,
			fmt.Sprintf("Missing ',' before '.%s =' in single-line 'with'", idxTok.Lexeme),
			"Separate multiple index updates on one line with commas (e.g. 'with .1 = 10, .2 = 20'), or use a multi-line 'with\\n  ...\\nend' block.",
		)
	}
	return nil
}

func (p *Parser) parseWithFieldUpdate() (WithFieldUpdate, error) {
	fieldTok, ok := p.matchFieldIdent()
	if !ok {
		tok := p.current()
		return WithFieldUpdate{}, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			fmt.Sprintf("Expected field name after 'with', got %q", tok.Lexeme),
			"Example: player with x = 10",
		)
	}
	if !p.match(TOK_ASSIGN) {
		tok := p.current()
		if fieldTok.Lexeme == "append" {
			return WithFieldUpdate{}, newError(
				"SyntaxError",
				fieldTok.Line,
				fieldTok.Col,
				p.source,
				"'append' cannot be used after 'with'",
				"Use 'xs = append xs <item>' to append to a list ('with' only updates record fields or 1-based indices like '.1 = val').",
			)
		}
		return WithFieldUpdate{}, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			fmt.Sprintf("Expected '=' after field '%s' in 'with'", fieldTok.Lexeme),
			fmt.Sprintf("Write: with %s = <value>", fieldTok.Lexeme),
		)
	}
	valExpr, err := p.parseExpr()
	if err != nil {
		return WithFieldUpdate{}, err
	}
	return WithFieldUpdate{Line: fieldTok.Line, Col: fieldTok.Col, Name: fieldTok.Lexeme, Value: valExpr}, nil
}

func (p *Parser) parseUpdateExpr() (Expr, error) {
	if err := p.enterDepth(); err != nil {
		return nil, err
	}
	defer p.exitDepth()

	updTok := p.advance() // consume `update`
	if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
		return nil, newError(
			"SyntaxError",
			updTok.Line,
			updTok.Col,
			p.source,
			"Expected list expression after 'update'",
			"Write: update <list> [where <cond>] with <field> = <val>",
		)
	}

	prevWhere := p.disallowWhere
	prevWith := p.disallowWith
	p.disallowWhere = true
	p.disallowWith = true
	target, err := p.parsePostfix()
	p.disallowWhere = prevWhere
	p.disallowWith = prevWith
	if err != nil {
		return nil, err
	}

	var whereCond Expr
	if p.match(TOK_WHERE) {
		if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
			cur := p.current()
			return nil, newError(
				"SyntaxError",
				cur.Line,
				cur.Col,
				p.source,
				"Expected condition expression after 'where' in 'update'",
				"Write: update <list> where <cond> with <field> = <val>",
			)
		}
		prevW := p.disallowWith
		prevTopIs := p.disallowTopIs
		prevStopIs := p.stopChainedIs
		p.disallowWith = true
		p.disallowTopIs = false
		p.stopChainedIs = false
		whereCond, err = p.parseExpr()
		p.disallowWith = prevW
		p.disallowTopIs = prevTopIs
		p.stopChainedIs = prevStopIs
		if err != nil {
			return nil, err
		}
	}

	if !p.match(TOK_WITH) {
		cur := p.current()
		return nil, newError(
			"SyntaxError",
			cur.Line,
			cur.Col,
			p.source,
			"Expected 'with' after 'update' target/condition",
			"Write: update <list> [where <cond>] with <field> = <val>",
		)
	}

	var updates []WithFieldUpdate
	parseFieldInto := func() error {
		if p.check(TOK_DOT) {
			dotTok := p.current()
			return newError(
				"SyntaxError",
				dotTok.Line,
				dotTok.Col,
				p.source,
				"'update' only updates named record fields; use '<list> with .1 = <val>' for positional list updates",
				"Write: update <list> with <field> = <val>",
			)
		}
		prevW := p.disallowWith
		p.disallowWith = false
		upd, err := p.parseWithFieldUpdate()
		p.disallowWith = prevW
		if err != nil {
			return err
		}
		updates = append(updates, upd)
		return nil
	}

	if p.match(TOK_NEWLINE) {
		p.skipNewlines()
		for !p.check(TOK_END) {
			if p.check(TOK_EOF) {
				return nil, newError(
					"SyntaxError",
					updTok.Line,
					updTok.Col,
					p.source,
					"Multiline 'update ... with' block is missing 'end'",
					"Close every multiline 'update ... with' block with 'end'.",
				)
			}
			if err := parseFieldInto(); err != nil {
				return nil, err
			}
			if err := p.expectStmtEnd("'update' field"); err != nil {
				return nil, err
			}
			p.skipNewlines()
		}
		p.advance() // consume `end`
	} else {
		prevSingleWith := p.inSingleLineWith
		p.inSingleLineWith = true
		for {
			if err := parseFieldInto(); err != nil {
				p.inSingleLineWith = prevSingleWith
				return nil, err
			}
			if !p.match(TOK_COMMA) {
				break
			}
		}
		p.inSingleLineWith = prevSingleWith
		if err := p.checkMissingWithComma(); err != nil {
			return nil, err
		}
	}

	if len(updates) == 0 {
		return nil, newError(
			"SyntaxError",
			updTok.Line,
			updTok.Col,
			p.source,
			"'update ... with' requires at least one field update",
			"Write: update <list> [where <cond>] with <field> = <val>",
		)
	}

	return &UpdateExpr{
		posInfo: posInfo{Line: updTok.Line, Col: updTok.Col},
		Target:  target,
		Where:   whereCond,
		Updates: updates,
	}, nil
}

func (p *Parser) parsePrimary() (Expr, error) {
	tok := p.current()
	switch tok.Type {
	case TOK_NUMBER:
		p.advance()
		val, err := strconv.ParseFloat(tok.Lexeme, 64)
		if err != nil {
			return nil, newError("SyntaxError", tok.Line, tok.Col, p.source, "Invalid number "+tok.Lexeme, "Use standard digits like 42 or 3.14.")
		}
		return &NumberExpr{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Value: val, Raw: tok.Lexeme}, nil
	case TOK_STRING:
		p.advance()
		return &StringExpr{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Value: tok.Lexeme}, nil
	case TOK_INTERP_STRING:
		p.advance()
		return p.parseInterpString(tok)
	case TOK_LOOP:
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"'loop' is a statement, not an expression",
			"Build lists imperatively with 'xs = list as <type>' and 'xs = append xs val' inside 'loop ... end'.",
		)
	case TOK_IDENT:
		if tok.Lexeme == "decode_json" {
			return nil, newError(
				"TypeError",
				tok.Line,
				tok.Col,
				p.source,
				"'decode_json' was removed from Ease",
				"Use '<text> as json' or 'load <path> as json' instead.",
			)
		}
		if tok.Lexeme == "encode_json" {
			return nil, newError(
				"TypeError",
				tok.Line,
				tok.Col,
				p.source,
				"'encode_json' was removed from Ease",
				"Use '<value> as json' or 'save <path> <value> as json' instead.",
			)
		}
		p.advance()
		return &IdentExpr{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Name: tok.Lexeme}, nil
	case TOK_LPAREN:
		p.advance()
		prevDisallowAs := p.disallowAs
		prevDisallowWhere := p.disallowWhere
		prevDisallowWith := p.disallowWith
		prevDisallowTopIs := p.disallowTopIs
		prevDisallowArrow := p.disallowArrow
		prevInCallArg := p.inCallArg
		p.disallowAs = false
		p.disallowWhere = false
		p.disallowWith = false
		p.disallowTopIs = false
		p.disallowArrow = false
		p.inCallArg = false
		inner, err := p.parseExpr()
		p.disallowAs = prevDisallowAs
		p.disallowWhere = prevDisallowWhere
		p.disallowWith = prevDisallowWith
		p.disallowTopIs = prevDisallowTopIs
		p.disallowArrow = prevDisallowArrow
		p.inCallArg = prevInCallArg
		if err != nil {
			return nil, err
		}
		if !p.match(TOK_RPAREN) {
			cur := p.current()
			return nil, newError("SyntaxError", cur.Line, cur.Col, p.source, "Missing closing ')'", minusSpaceHint(cur, "Match every '(' with a closing ')'."))
		}
		return inner, nil
	case TOK_LIST_KW:
		return p.parseTypedOrInlineList()
	case TOK_UPDATE:
		return p.parseUpdateExpr()
	case TOK_ANY, TOK_NONE:
		return p.parseQuantifierExpr()
	case TOK_INBOX:
		return p.parseInboxExpr()
	case TOK_SPAWN:
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"'spawn' is a statement, not an expression",
			"Launch background workers on their own line: spawn fn arg1 arg2",
		)
	case TOK_TAKE:
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"'take' is a statement, not an expression",
			"Read messages with: take <inbox> ... end",
		)
	case TOK_MATCH:
		return p.parseMatchExpr()
	case TOK_SWARM:
		if p.inCallArg {
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				"Wrap 'swarm loop ... end' in parentheses or assign it to a variable first",
				"Write e.g. 'sum (swarm loop ... end)' or 'items = swarm loop ... end'.",
			)
		}
		return p.parseSwarmLoopExpr()
	default:
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			fmt.Sprintf("Expected an expression, got %q", tok.Lexeme),
			"Provide a value, variable name, function call, or parenthesized expression.",
		)
	}
}

func offsetSourcePos(line, col, baseLine, baseCol int) (int, int) {
	if line <= 0 {
		line = 1
	}
	if col <= 0 {
		col = 1
	}
	if line == 1 {
		col += baseCol - 1
	}
	line += baseLine - 1
	return line, col
}

func offsetTokenPositions(tokens []Token, baseLine, baseCol int) {
	for i := range tokens {
		tokens[i].Line, tokens[i].Col = offsetSourcePos(tokens[i].Line, tokens[i].Col, baseLine, baseCol)
		for j := range tokens[i].Segments {
			tokens[i].Segments[j].Line, tokens[i].Segments[j].Col = offsetSourcePos(
				tokens[i].Segments[j].Line,
				tokens[i].Segments[j].Col,
				baseLine,
				baseCol,
			)
		}
	}
}

func (p *Parser) parseInterpString(tok Token) (Expr, error) {
	parts := make([]InterpPart, 0, len(tok.Segments))
	for _, seg := range tok.Segments {
		if !seg.IsExpr {
			parts = append(parts, InterpPart{IsExpr: false, Text: seg.Text})
			continue
		}
		subTokens, err := NewLexer(seg.Text).Tokenize()
		if err != nil {
			if eErr, ok := err.(*EaseError); ok {
				errLine, errCol := offsetSourcePos(eErr.Line, eErr.Col, seg.Line, seg.Col)
				return nil, newError("SyntaxError", errLine, errCol, p.source, eErr.Message, eErr.Hint)
			}
			return nil, err
		}
		offsetTokenPositions(subTokens, seg.Line, seg.Col)
		subParser := &Parser{tokens: subTokens, source: p.source, depth: p.depth, funcArities: p.funcArities, moduleArities: p.moduleArities}
		expr, err := subParser.parseExpr()
		if err != nil {
			return nil, err
		}
		if !subParser.check(TOK_EOF) {
			extra := subParser.current()
			return nil, newError(
				"SyntaxError",
				extra.Line,
				extra.Col,
				p.source,
				fmt.Sprintf("Unexpected %q inside string interpolation", extra.Lexeme),
				minusSpaceHint(extra, "Use a single expression inside '{...}', e.g. \"Sum is {x + y}\"."),
			)
		}
		parts = append(parts, InterpPart{IsExpr: true, Expr: expr})
	}
	return &InterpStringExpr{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Parts: parts}, nil
}

func (p *Parser) parseTypedOrInlineList() (Expr, error) {
	listTok := p.advance() // consume `list`
	if p.match(TOK_AS) {
		typeName, typeLine, typeCol, err := p.parseTypeTarget(
			"Expected element type ('number', 'text', or a 'def' record) after 'list as'",
			"Example: list as number, or list as billing.invoice",
		)
		if err != nil {
			return nil, err
		}
		if !p.match(TOK_WITH) {
			return &TypedListExpr{
				posInfo:  posInfo{Line: listTok.Line, Col: listTok.Col},
				ElemType: typeName,
				TypeLine: typeLine,
				TypeCol:  typeCol,
			}, nil
		}
		if typeName == "number" || typeName == "text" {
			return nil, newError(
				"TypeError",
				typeLine,
				typeCol,
				p.source,
				fmt.Sprintf("Built-in type %q cannot be used with 'list as ... with'", typeName),
				"Only a 'def <name> ... end' record blueprint can be used with 'list as <name> with'. To create a populated list of numbers/text, use 'list 1, 2, 3' or 'list as number'.",
			)
		}
		if !p.match(TOK_NEWLINE) {
			tok := p.current()
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				"Expected newline after 'list as ... with'",
				"Put each record item on its own line and close with 'end'.",
			)
		}
		p.skipNewlines()
		var rows [][]WithFieldUpdate
		for !p.check(TOK_END) {
			if p.check(TOK_EOF) {
				return nil, newError(
					"SyntaxError",
					listTok.Line,
					listTok.Col,
					p.source,
					"Record list 'list as ... with' is missing 'end'",
					"Close the list block with 'end'.",
				)
			}
			var row []WithFieldUpdate
			for {
				upd, err := p.parseWithFieldUpdate()
				if err != nil {
					return nil, err
				}
				row = append(row, upd)
				if !p.match(TOK_COMMA) {
					break
				}
			}
			if err := p.checkMissingWithComma(); err != nil {
				return nil, err
			}
			if err := p.expectStmtEnd("list record row"); err != nil {
				return nil, err
			}
			rows = append(rows, row)
			p.skipNewlines()
		}
		p.advance() // consume `end`
		return &TypedListExpr{
			posInfo:  posInfo{Line: listTok.Line, Col: listTok.Col},
			ElemType: typeName,
			TypeLine: typeLine,
			TypeCol:  typeCol,
			Rows:     rows,
		}, nil
	}

	var elems []Expr
	for {
		el, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		elems = append(elems, el)
		if !p.match(TOK_COMMA) {
			break
		}
	}
	return &ListExpr{
		posInfo:     posInfo{Line: listTok.Line, Col: listTok.Col},
		Elements:    elems,
		Homogeneous: true,
	}, nil
}

func (p *Parser) parseTypeTarget(errMsg, hint string) (string, int, int, error) {
	if !p.check(TOK_TEXT) && !p.check(TOK_NUMBER_KW) && !p.check(TOK_IDENT) {
		tok := p.current()
		return "", 0, 0, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			fmt.Sprintf("%s, got %q", errMsg, tok.Lexeme),
			hint,
		)
	}
	typeTok := p.advance()
	name := typeTok.Lexeme
	if typeTok.Type == TOK_IDENT && p.match(TOK_DOT) {
		if !p.check(TOK_IDENT) {
			cur := p.current()
			return "", 0, 0, newError(
				"SyntaxError",
				cur.Line,
				cur.Col,
				p.source,
				fmt.Sprintf("Expected type name after '%s.', got %q", name, cur.Lexeme),
				hint,
			)
		}
		subTok := p.advance()
		name = name + "." + subTok.Lexeme
	}
	return name, typeTok.Line, typeTok.Col, nil
}

func (p *Parser) parseCallArgs(callee string, arity int) ([]Expr, error) {
	args := make([]Expr, 0, arity)
	if arity == 0 {
		return args, nil
	}
	prevDisallowAs := p.disallowAs
	prevInCallArg := p.inCallArg
	p.disallowAs = true
	p.inCallArg = true
	defer func() {
		p.disallowAs = prevDisallowAs
		p.inCallArg = prevInCallArg
	}()

	for i := 0; i < arity; i++ {
		if p.isCallArgBoundary() {
			cur := p.current()
			hint := fmt.Sprintf("Provide %d argument(s) for '%s', or wrap compound arguments in (...).", arity, callee)
			if callee == "ask" {
				hint = "Write: ask \"Prompt?\" or ask \"\""
			}
			return nil, newError(
				"SyntaxError",
				cur.Line,
				cur.Col,
				p.source,
				fmt.Sprintf("Function '%s' expects %d argument(s), got %d", callee, arity, i),
				hint,
			)
		}
		arg, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
	}
	return args, nil
}
