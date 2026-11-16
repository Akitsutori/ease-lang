package ease

import (
	"fmt"
	"strconv"
	"strings"
)

const maxParseDepth = 256

// Parser converts Ease tokens into an AST Program using recursive descent.
type Parser struct {
	tokens           []Token
	source           string
	pos              int
	depth            int
	disallowTopIs    bool
	disallowAs       bool
	disallowDoAsArg  bool
	disallowBareCall bool
}

func (p *Parser) enterDepth() error {
	p.depth++
	if p.depth > maxParseDepth {
		tok := p.current()
		return newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			fmt.Sprintf("Nesting deeper than %d levels", maxParseDepth),
			"Simplify deeply nested parentheses, expressions, or blocks.",
		)
	}
	return nil
}

func (p *Parser) exitDepth() {
	p.depth--
}

// ParseProgram lexes and parses `source` into a complete Program AST.
func ParseProgram(source string) (*Program, error) {
	return parseProgramWithSeen(source, map[string]bool{})
}

func parseProgramWithSeen(source string, seen map[string]bool) (*Program, error) {
	tokens, err := NewLexer(source).Tokenize()
	if err != nil {
		return nil, err
	}
	p := &Parser{tokens: tokens, source: source}
	prog, err := p.parseProgram()
	if err != nil {
		return nil, err
	}
	if err := resolveProgramImports(prog, source, seen); err != nil {
		return nil, err
	}
	if err := validateProgramTypes(prog, source); err != nil {
		return nil, err
	}
	return prog, nil
}

func (p *Parser) current() Token {
	if p.pos >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[p.pos]
}

func (p *Parser) peekNext() Token {
	if p.pos+1 >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[p.pos+1]
}

func (p *Parser) advance() Token {
	tok := p.current()
	if p.pos < len(p.tokens)-1 {
		p.pos++
	}
	return tok
}

func (p *Parser) check(t TokenType) bool {
	return p.current().Type == t
}

func (p *Parser) match(types ...TokenType) bool {
	for _, t := range types {
		if p.check(t) {
			p.advance()
			return true
		}
	}
	return false
}

func (p *Parser) skipNewlines() {
	for p.check(TOK_NEWLINE) {
		p.advance()
	}
}

func (p *Parser) expectStmtEnd(context string) error {
	if p.check(TOK_NEWLINE) || p.check(TOK_EOF) || p.check(TOK_END) || p.check(TOK_ELSE) {
		p.skipNewlines()
		return nil
	}
	tok := p.current()
	hint := "Put each statement on its own line."
	if tok.Type == TOK_COMMA {
		hint = "Commas are only used in lists (e.g. list 1, 2, 3), not in function calls or statements."
	}
	return newError(
		"SyntaxError",
		tok.Line,
		tok.Col,
		p.source,
		fmt.Sprintf("Unexpected %q after %s", tok.Lexeme, context),
		hint,
	)
}

func (p *Parser) parseProgram() (*Program, error) {
	prog := &Program{}
	p.skipNewlines()

	for !p.check(TOK_EOF) {
		switch p.current().Type {
		case TOK_IMPORT:
			impTok := p.advance()
			if !p.check(TOK_IDENT) {
				tok := p.current()
				return nil, newError(
					"SyntaxError",
					tok.Line,
					tok.Col,
					p.source,
					fmt.Sprintf("Expected module name after 'import', got %q", tok.Lexeme),
					"Example: import network",
				)
			}
			modTok := p.advance()
			if err := p.expectStmtEnd("'import' statement"); err != nil {
				return nil, err
			}
			prog.Imports = append(prog.Imports, &ImportDecl{
				posInfo: posInfo{Line: impTok.Line, Col: impTok.Col},
				Name:    modTok.Lexeme,
			})
		case TOK_TO:
			fn, err := p.parseFuncDecl()
			if err != nil {
				return nil, err
			}
			prog.Functions = append(prog.Functions, fn)
		case TOK_DEF:
			def, union, constStmt, err := p.parseDefOrUnion()
			if err != nil {
				return nil, err
			}
			if constStmt != nil {
				prog.Body = append(prog.Body, constStmt)
			} else if union != nil {
				prog.Unions = append(prog.Unions, union)
			} else {
				prog.Defs = append(prog.Defs, def)
			}
		case TOK_CHECK:
			if p.peekNext().Type == TOK_STRING {
				blk, err := p.parseCheckBlock()
				if err != nil {
					return nil, err
				}
				prog.Checks = append(prog.Checks, blk)
			} else {
				stmt, err := p.parseStmt()
				if err != nil {
					return nil, err
				}
				prog.Body = append(prog.Body, stmt)
			}
		case TOK_TEST:
			blk, err := p.parseTopTestBlock()
			if err != nil {
				return nil, err
			}
			prog.Checks = append(prog.Checks, blk)
		default:
			stmt, err := p.parseStmt()
			if err != nil {
				return nil, err
			}
			prog.Body = append(prog.Body, stmt)
		}
		p.skipNewlines()
	}

	return prog, nil
}

func (p *Parser) parseFuncDecl() (*FuncDecl, error) {
	toTok := p.advance() // consume `to`
	if !p.check(TOK_IDENT) && !p.check(TOK_ASK) {
		tok := p.current()
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			fmt.Sprintf("Expected function name after 'to', got %q", tok.Lexeme),
			"Example: to greet name ... end",
		)
	}
	nameTok := p.advance()

	var params []string
	for p.check(TOK_IDENT) {
		params = append(params, p.advance().Lexeme)
		if p.check(TOK_COMMA) {
			tok := p.current()
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				"Commas are not allowed in function parameter lists",
				fmt.Sprintf("Write parameters separated by spaces: to %s param1 param2", nameTok.Lexeme),
			)
		}
	}

	if !p.check(TOK_NEWLINE) && !p.check(TOK_EOF) && !p.check(TOK_END) {
		tok := p.current()
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			fmt.Sprintf("Unexpected %q in function header", tok.Lexeme),
			fmt.Sprintf("Write 'to %s param1 param2' followed by a new line.", nameTok.Lexeme),
		)
	}
	p.skipNewlines()

	var body []Stmt
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				toTok.Line,
				toTok.Col,
				p.source,
				fmt.Sprintf("Function 'to %s' is missing a closing 'end'", nameTok.Lexeme),
				"Every 'to ...' block must end with 'end' on its own line.",
			)
		}
		stmt, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		body = append(body, stmt)
		p.skipNewlines()
	}

	if !p.match(TOK_END) {
		tok := p.current()
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			fmt.Sprintf("Expected 'end' to close function %q", nameTok.Lexeme),
			"Add 'end' after the function body.",
		)
	}

	return &FuncDecl{
		posInfo: posInfo{Line: toTok.Line, Col: toTok.Col},
		Name:    nameTok.Lexeme,
		Params:  params,
		Body:    body,
	}, nil
}

func (p *Parser) isUnionDelimiter(t TokenType) bool {
	switch t {
	case TOK_PIPE, TOK_NEWLINE, TOK_EOF, TOK_END, TOK_ELSE:
		return true
	default:
		return false
	}
}

func (p *Parser) parseUnionOrFieldExpr() (Expr, bool, error) {
	if (p.check(TOK_TEXT) || p.check(TOK_NUMBER_KW)) && p.isUnionDelimiter(p.peekNext().Type) {
		tok := p.advance()
		return &IdentExpr{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Name: tok.Lexeme}, true, nil
	}
	expr, err := p.parseExpr()
	return expr, false, err
}

func (p *Parser) parseDefOrUnion() (*DefDecl, *UnionDecl, Stmt, error) {
	defTok := p.advance() // consume `def`
	if !p.check(TOK_IDENT) {
		tok := p.current()
		return nil, nil, nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			fmt.Sprintf("Expected constant, record, or union name after 'def', got %q", tok.Lexeme),
			"Example: def x = 1, def player ... end, or def ticket = \"vip\" | \"standard\"",
		)
	}
	nameTok := p.advance()
	if p.match(TOK_ASSIGN) {
		first, isBaseKw, err := p.parseUnionOrFieldExpr()
		if err != nil {
			return nil, nil, nil, err
		}
		if p.match(TOK_PIPE) {
			variants := []Expr{first}
			for {
				v, _, err := p.parseUnionOrFieldExpr()
				if err != nil {
					return nil, nil, nil, err
				}
				variants = append(variants, v)
				if !p.match(TOK_PIPE) {
					break
				}
			}
			if err := p.expectStmtEnd("union definition"); err != nil {
				return nil, nil, nil, err
			}
			return nil, &UnionDecl{
				posInfo:  posInfo{Line: defTok.Line, Col: defTok.Col},
				Name:     nameTok.Lexeme,
				Variants: variants,
			}, nil, nil
		}
		if isBaseKw {
			id := first.(*IdentExpr)
			return nil, nil, nil, newError(
				"SyntaxError",
				id.Line,
				id.Col,
				p.source,
				fmt.Sprintf("Constant 'def %s' cannot have bare type %q as value", nameTok.Lexeme, id.Name),
				fmt.Sprintf("Use a concrete literal value or combine with '|' into a union (e.g. def %s = %s | ErrorRecord).", nameTok.Lexeme, id.Name),
			)
		}
		if err := p.expectStmtEnd("constant definition"); err != nil {
			return nil, nil, nil, err
		}
		return nil, nil, &LetStmt{
			posInfo: posInfo{Line: defTok.Line, Col: defTok.Col},
			Name:    nameTok.Lexeme,
			Value:   first,
		}, nil
	}
	if err := p.expectStmtEnd("def header"); err != nil {
		return nil, nil, nil, err
	}
	p.skipNewlines()

	var fields []DefField
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, nil, nil, newError(
				"SyntaxError",
				defTok.Line,
				defTok.Col,
				p.source,
				fmt.Sprintf("Record 'def %s' is missing 'end'", nameTok.Lexeme),
				"Close every 'def' block with 'end'.",
			)
		}
		if !p.check(TOK_IDENT) {
			tok := p.current()
			return nil, nil, nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				fmt.Sprintf("Expected field name in 'def %s', got %q", nameTok.Lexeme, tok.Lexeme),
				"Write one 'field = default_value' per line.",
			)
		}
		fieldTok := p.advance()
		if !p.match(TOK_ASSIGN) {
			tok := p.current()
			return nil, nil, nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				fmt.Sprintf("Expected '=' after field '%s' in 'def %s'", fieldTok.Lexeme, nameTok.Lexeme),
				fmt.Sprintf("Write: %s = <default value>", fieldTok.Lexeme),
			)
		}
		valExpr, isFieldBaseKw, err := p.parseUnionOrFieldExpr()
		if err != nil {
			return nil, nil, nil, err
		}
		var allowed []Expr
		if p.match(TOK_PIPE) {
			allowed = append(allowed, valExpr)
			for {
				optExpr, _, err := p.parseUnionOrFieldExpr()
				if err != nil {
					return nil, nil, nil, err
				}
				allowed = append(allowed, optExpr)
				if !p.match(TOK_PIPE) {
					break
				}
			}
		} else if isFieldBaseKw {
			id := valExpr.(*IdentExpr)
			return nil, nil, nil, newError(
				"SyntaxError",
				id.Line,
				id.Col,
				p.source,
				fmt.Sprintf("Field '%s.%s' needs a default value or union, got bare type %q", nameTok.Lexeme, fieldTok.Lexeme, id.Name),
				"Provide a concrete default value (e.g. name = \"\" or code = 0) or a union (e.g. val = text | ErrorRecord).",
			)
		}
		if err := p.expectStmtEnd("def field"); err != nil {
			return nil, nil, nil, err
		}
		fields = append(fields, DefField{
			Line:    fieldTok.Line,
			Col:     fieldTok.Col,
			Name:    fieldTok.Lexeme,
			Default: valExpr,
			Allowed: allowed,
		})
		p.skipNewlines()
	}
	p.advance() // consume `end`

	return &DefDecl{
		posInfo: posInfo{Line: defTok.Line, Col: defTok.Col},
		Name:    nameTok.Lexeme,
		Fields:  fields,
	}, nil, nil, nil
}

func (p *Parser) parseCheckBlock() (*CheckBlock, error) {
	chkTok := p.advance() // consume `check`
	title := fmt.Sprintf("check@line-%d", chkTok.Line)
	if p.check(TOK_STRING) {
		title = p.advance().Lexeme
	}
	p.skipNewlines()

	var body []Stmt
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				chkTok.Line,
				chkTok.Col,
				p.source,
				fmt.Sprintf("Check block %q is missing 'end'", title),
				"Close every 'check' block with 'end'.",
			)
		}
		item, err := p.parseCheckItem()
		if err != nil {
			return nil, err
		}
		body = append(body, item)
		p.skipNewlines()
	}
	p.advance() // consume `end`

	return &CheckBlock{
		posInfo: posInfo{Line: chkTok.Line, Col: chkTok.Col},
		Title:   title,
		Body:    body,
	}, nil
}

func (p *Parser) parseTopTestBlock() (*CheckBlock, error) {
	tTok := p.advance() // consume `test`
	title := "test"
	if p.check(TOK_STRING) {
		title = p.advance().Lexeme
	}
	p.skipNewlines()

	var body []Stmt
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				tTok.Line,
				tTok.Col,
				p.source,
				"'test' block is missing 'end'",
				"Close every 'test' block with 'end'.",
			)
		}
		item, err := p.parseCheckItem()
		if err != nil {
			return nil, err
		}
		body = append(body, item)
		p.skipNewlines()
	}
	p.advance() // consume `end`

	return &CheckBlock{
		posInfo: posInfo{Line: tTok.Line, Col: tTok.Col},
		Title:   title,
		Body:    body,
	}, nil
}

func (p *Parser) parseCheckItem() (Stmt, error) {
	switch p.current().Type {
	case TOK_REPEAT, TOK_FOR:
		return p.parseStmt()
	case TOK_DO:
		if p.peekNext().Type == TOK_SAY {
			return p.parseStmt()
		}
	}
	if p.current().Type == TOK_IDENT && p.peekNext().Type == TOK_ASSIGN {
		return p.parseStmt()
	}

	p.disallowTopIs = true
	expr, err := p.parseExpr()
	p.disallowTopIs = false
	if err != nil {
		return nil, err
	}
	line, col := expr.Pos()

	if p.match(TOK_IS) {
		negated := p.match(TOK_NOT)
		isBareListType := p.check(TOK_LIST_KW) && p.isTypeAssertionEnd(p.peekNext().Type)
		if p.check(TOK_TEXT) || p.check(TOK_NUMBER_KW) || isBareListType {
			typeTok := p.advance()
			if err := p.expectStmtEnd("'is' type assertion"); err != nil {
				return nil, err
			}
			tc := &TypeCheckExpr{
				posInfo:  posInfo{Line: line, Col: col},
				Target:   expr,
				TypeName: typeTok.Lexeme,
				Negated:  negated,
			}
			return &CheckIsStmt{
				posInfo:  posInfo{Line: line, Col: col},
				Actual:   tc,
				Expected: &BoolExpr{posInfo: posInfo{Line: line, Col: col}, Value: true},
			}, nil
		}
		if negated {
			unexpected, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if err := p.expectStmtEnd("'is not' assertion"); err != nil {
				return nil, err
			}
			return &CheckIsNotStmt{posInfo: posInfo{Line: line, Col: col}, Actual: expr, Unexpected: unexpected}, nil
		}
		expected, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if err := p.expectStmtEnd("'is' assertion"); err != nil {
			return nil, err
		}
		return &CheckIsStmt{posInfo: posInfo{Line: line, Col: col}, Actual: expr, Expected: expected}, nil
	}

	if p.match(TOK_RAISES) {
		expected, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if err := p.expectStmtEnd("'raises' assertion"); err != nil {
			return nil, err
		}
		return &CheckRaisesStmt{posInfo: posInfo{Line: line, Col: col}, Expression: expr, Expected: expected}, nil
	}

	tok := p.current()
	return nil, newError(
		"SyntaxError",
		tok.Line,
		tok.Col,
		p.source,
		fmt.Sprintf("Expected 'is', 'is-not', or 'raises' in test assertion, got %q", tok.Lexeme),
		"Example: do add 2, 3 is 5",
	)
}

func (p *Parser) parseStmt() (Stmt, error) {
	if err := p.enterDepth(); err != nil {
		return nil, err
	}
	defer p.exitDepth()

	tok := p.current()
	switch tok.Type {
	case TOK_DO:
		doTok := p.advance()
		if p.check(TOK_SAY) {
			return p.parseStmt()
		}
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if id, ok := expr.(*IdentExpr); ok {
			line, col := id.Pos()
			expr = &CallExpr{posInfo: posInfo{Line: line, Col: col}, Callee: id.Name}
		}
		if err := p.expectStmtEnd("'do' statement"); err != nil {
			return nil, err
		}
		return &ExprStmt{posInfo: posInfo{Line: doTok.Line, Col: doTok.Col}, Expression: expr}, nil
	case TOK_SAY:
		p.advance()
		val, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if err := p.expectStmtEnd("'say' statement"); err != nil {
			return nil, err
		}
		return &SayStmt{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Value: val}, nil
	case TOK_REPEAT:
		return p.parseRepeatStmt()
	case TOK_FOR:
		return p.parseForStmt()
	case TOK_CHECK:
		return p.parseCheckGuardStmt()
	case TOK_TO:
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Nested 'to' function definitions are not allowed inside blocks",
			"Define functions with 'to ... end' at the top level of the file.",
		)
	case TOK_DEF:
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Nested 'def' record definitions are not allowed inside blocks",
			"Define records with 'def ... end' at the top level of the file.",
		)
	case TOK_TEST:
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Nested 'test' blocks are not allowed inside functions or statements",
			"Close the enclosing block with 'end' before starting a 'test ... end' block.",
		)
	default:
		if tok.Type == TOK_IDENT && p.peekNext().Type == TOK_ASSIGN {
			return p.parseBareAssignStmt()
		}
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if err := p.expectStmtEnd("expression"); err != nil {
			return nil, err
		}
		return &ExprStmt{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Expression: expr}, nil
	}
}

func (p *Parser) parseBareAssignStmt() (Stmt, error) {
	nameTok := p.advance()
	p.advance() // '='
	val, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if err := p.expectStmtEnd("assignment"); err != nil {
		return nil, err
	}
	return &AssignStmt{posInfo: posInfo{Line: nameTok.Line, Col: nameTok.Col}, Name: nameTok.Lexeme, Value: val}, nil
}

func (p *Parser) parseRepeatStmt() (Stmt, error) {
	repTok := p.advance()
	countExpr, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	p.skipNewlines()

	var body []Stmt
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				repTok.Line,
				repTok.Col,
				p.source,
				"'repeat' loop is missing 'end'",
				"Close every 'repeat <n> ...' loop with 'end'.",
			)
		}
		s, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		body = append(body, s)
		p.skipNewlines()
	}
	p.advance() // consume `end`
	if err := p.expectStmtEnd("'repeat' block"); err != nil {
		return nil, err
	}

	return &RepeatStmt{posInfo: posInfo{Line: repTok.Line, Col: repTok.Col}, Count: countExpr, Body: body}, nil
}

func (p *Parser) parseForStmt() (Stmt, error) {
	forTok := p.advance()
	itemName := ""
	var iterExpr Expr
	var err error
	if p.check(TOK_IDENT) && p.peekNext().Type == TOK_IN {
		itemName = p.advance().Lexeme
		p.advance() // consume `in`
		prevDisallowDo := p.disallowDoAsArg
		p.disallowDoAsArg = true
		iterExpr, err = p.parseExpr()
		p.disallowDoAsArg = prevDisallowDo
		if err != nil {
			return nil, err
		}
		p.match(TOK_DO) // optional `do` for single-line `for x in list do ... end`
	} else {
		prevDisallowDo := p.disallowDoAsArg
		p.disallowDoAsArg = true
		iterExpr, err = p.parseExpr()
		p.disallowDoAsArg = prevDisallowDo
		if err != nil {
			return nil, err
		}
		if !p.match(TOK_DO) {
			tok := p.current()
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				fmt.Sprintf("Expected 'do' after 'for <list>', got %q", tok.Lexeme),
				"Write: for items do ... end, or: for item in items ... end",
			)
		}
	}
	p.skipNewlines()

	var body []Stmt
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				forTok.Line,
				forTok.Col,
				p.source,
				"'for' loop is missing 'end'",
				"Close every 'for' loop with 'end'.",
			)
		}
		s, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		body = append(body, s)
		p.skipNewlines()
	}
	p.advance() // consume `end`
	if err := p.expectStmtEnd("'for' block"); err != nil {
		return nil, err
	}

	return &ForStmt{
		posInfo:  posInfo{Line: forTok.Line, Col: forTok.Col},
		ItemName: itemName,
		Iterable: iterExpr,
		Body:     body,
	}, nil
}

func (p *Parser) parseCheckGuardStmt() (Stmt, error) {
	chkTok := p.advance() // consume `check`

	// Single-line guard: `check <cond> -> <stmt>`
	if !p.check(TOK_NEWLINE) && !p.check(TOK_EOF) {
		cond, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if !p.match(TOK_ARROW) {
			tok := p.current()
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				"Expected '->' after condition in single-line 'check'",
				"Write: check <condition> -> <action>, or put conditions on new lines inside 'check ... end'.",
			)
		}
		action, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		return &CheckGuardStmt{
			posInfo: posInfo{Line: chkTok.Line, Col: chkTok.Col},
			Rules: []CheckRule{
				{Line: chkTok.Line, Col: chkTok.Col, Cond: cond, Action: action},
			},
		}, nil
	}

	// Multi-line guard block: `check\n ...\n [else\n ...]\n end`
	p.skipNewlines()
	var rules []CheckRule
	for !p.check(TOK_END) && !p.check(TOK_ELSE) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				chkTok.Line,
				chkTok.Col,
				p.source,
				"'check' block is missing 'end'",
				"Close every 'check' block with 'end'.",
			)
		}
		ruleTok := p.current()
		cond, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		var action Stmt
		if p.match(TOK_ARROW) {
			action, err = p.parseStmt()
			if err != nil {
				return nil, err
			}
		} else {
			if err := p.expectStmtEnd("check condition"); err != nil {
				return nil, err
			}
		}
		rules = append(rules, CheckRule{Line: ruleTok.Line, Col: ruleTok.Col, Cond: cond, Action: action})
		p.skipNewlines()
	}

	var elseBody []Stmt
	if p.match(TOK_ELSE) {
		if p.match(TOK_ARROW) {
			action, err := p.parseStmt()
			if err != nil {
				return nil, err
			}
			elseBody = append(elseBody, action)
			p.skipNewlines()
		} else {
			p.skipNewlines()
			for !p.check(TOK_END) {
				if p.check(TOK_EOF) {
					return nil, newError(
						"SyntaxError",
						chkTok.Line,
						chkTok.Col,
						p.source,
						"'check ... else' block is missing 'end'",
						"Close every 'check ... else' block with 'end'.",
					)
				}
				s, err := p.parseStmt()
				if err != nil {
					return nil, err
				}
				elseBody = append(elseBody, s)
				p.skipNewlines()
			}
		}
	}

	if !p.match(TOK_END) {
		tok := p.current()
		return nil, newError("SyntaxError", tok.Line, tok.Col, p.source, "Expected 'end' to close 'check' block", "Add 'end' after your check rules.")
	}
	if err := p.expectStmtEnd("'check' block"); err != nil {
		return nil, err
	}

	return &CheckGuardStmt{
		posInfo:  posInfo{Line: chkTok.Line, Col: chkTok.Col},
		IsBlock:  true,
		Rules:    rules,
		ElseBody: elseBody,
	}, nil
}

// Expressions (precedence climbing)

func (p *Parser) parseExpr() (Expr, error) {
	if err := p.enterDepth(); err != nil {
		return nil, err
	}
	defer p.exitDepth()

	return p.parseOr()
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
	for {
		if p.check(TOK_LT) || p.check(TOK_LTE) || p.check(TOK_GT) || p.check(TOK_GTE) {
			opTok := p.advance()
			right, err := p.parseTerm()
			if err != nil {
				return nil, err
			}
			left = &BinaryExpr{posInfo: posInfo{Line: opTok.Line, Col: opTok.Col}, Left: left, Op: opTok.Lexeme, Right: right}
			continue
		}
		if !p.disallowTopIs && p.check(TOK_IS) {
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
		if p.match(TOK_DOT) {
			if p.check(TOK_IDENT) {
				fieldTok := p.advance()
				line, col := expr.Pos()
				expr = &FieldExpr{posInfo: posInfo{Line: line, Col: col}, Target: expr, Field: fieldTok.Lexeme}
				continue
			}
			if p.check(TOK_NUMBER) {
				numTok := p.advance()
				parts := strings.Split(numTok.Lexeme, ".")
				for _, part := range parts {
					idx, err := strconv.Atoi(part)
					if err != nil || idx < 0 {
						return nil, newError(
							"SyntaxError",
							numTok.Line,
							numTok.Col,
							p.source,
							fmt.Sprintf("Invalid list index %q after '.'", numTok.Lexeme),
							"Use non-negative integer indices after '.', e.g. cloud.0.vx",
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
			tok := p.current()
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				fmt.Sprintf("Expected field name or list index after '.', got %q", tok.Lexeme),
				"Example: player.x or cloud.0.vx",
			)
		}

		if p.match(TOK_WITH) {
			var updates []WithFieldUpdate
			line, col := expr.Pos()
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
					upd, err := p.parseWithFieldUpdate()
					if err != nil {
						return nil, err
					}
					updates = append(updates, upd)
					if err := p.expectStmtEnd("'with' field"); err != nil {
						return nil, err
					}
					p.skipNewlines()
				}
				p.advance() // consume `end`
			} else {
				for {
					upd, err := p.parseWithFieldUpdate()
					if err != nil {
						return nil, err
					}
					updates = append(updates, upd)
					if !p.match(TOK_COMMA) {
						break
					}
				}
			}
			expr = &WithExpr{posInfo: posInfo{Line: line, Col: col}, Base: expr, Updates: updates}
			continue
		}

		if !p.disallowAs && p.match(TOK_AS) {
			if !p.check(TOK_TEXT) && !p.check(TOK_NUMBER_KW) && !p.check(TOK_IDENT) {
				tok := p.current()
				return nil, newError(
					"SyntaxError",
					tok.Line,
					tok.Col,
					p.source,
					fmt.Sprintf("Expected target type ('number', 'text', or a 'def' type) after 'as', got %q", tok.Lexeme),
					"Example: ask \"Age?\" as number or raw as api_user",
				)
			}
			typeTok := p.advance()
			line, col := expr.Pos()
			expr = &CastExpr{
				posInfo:  posInfo{Line: line, Col: col},
				Target:   expr,
				TypeName: typeTok.Lexeme,
				TypeLine: typeTok.Line,
				TypeCol:  typeTok.Col,
			}
			continue
		}

		if id, ok := expr.(*IdentExpr); ok && !p.disallowBareCall && p.startsBareCallArg() {
			args, err := p.parseBareCallArgs(nil)
			if err != nil {
				return nil, err
			}
			line, col := id.Pos()
			expr = &CallExpr{posInfo: posInfo{Line: line, Col: col}, Callee: id.Name, Args: args}
			continue
		}

		break
	}

	return expr, nil
}

func (p *Parser) parseBareCallArgs(initial []Expr) ([]Expr, error) {
	prevDisallowAs := p.disallowAs
	prevDisallowBareCall := p.disallowBareCall
	p.disallowAs = true
	p.disallowBareCall = true
	defer func() {
		p.disallowAs = prevDisallowAs
		p.disallowBareCall = prevDisallowBareCall
	}()

	args := initial
	for {
		arg, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if p.startsBareCallArg() {
			continue
		}
		break
	}
	return args, nil
}

func (p *Parser) startsBareCallArg() bool {
	switch p.current().Type {
	case TOK_NUMBER, TOK_STRING, TOK_INTERP_STRING, TOK_IDENT, TOK_LIST_KW, TOK_ASK:
		return true
	case TOK_DO:
		if p.disallowDoAsArg {
			return false
		}
		return p.peekNext().Type != TOK_NEWLINE && p.peekNext().Type != TOK_EOF
	default:
		return false
	}
}

func (p *Parser) parseWithFieldUpdate() (WithFieldUpdate, error) {
	if !p.check(TOK_IDENT) {
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
	fieldTok := p.advance()
	if !p.match(TOK_ASSIGN) {
		tok := p.current()
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
	case TOK_IDENT:
		p.advance()
		return &IdentExpr{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Name: tok.Lexeme}, nil
	case TOK_DO:
		doTok := p.advance()
		if p.check(TOK_ASK) {
			askTok := p.advance()
			if p.check(TOK_LPAREN) {
				cur := p.current()
				return nil, newError("SyntaxError", cur.Line, cur.Col, p.source, "Parenthesized 'ask(...)' is not allowed", "Write: ask \"Prompt?\" or do ask net \"/path\"")
			}
			if !p.check(TOK_NEWLINE) && !p.check(TOK_EOF) && !p.check(TOK_RPAREN) && !p.check(TOK_COMMA) && !p.check(TOK_AS) {
				firstArg, err := p.parsePrimary()
				if err != nil {
					return nil, err
				}
				if p.startsBareCallArg() {
					args, err := p.parseBareCallArgs([]Expr{firstArg})
					if err != nil {
						return nil, err
					}
					return &CallExpr{posInfo: posInfo{Line: askTok.Line, Col: askTok.Col}, Callee: "ask", Args: args}, nil
				}
				return &AskExpr{posInfo: posInfo{Line: askTok.Line, Col: askTok.Col}, Prompt: firstArg}, nil
			}
			return &AskExpr{posInfo: posInfo{Line: askTok.Line, Col: askTok.Col}}, nil
		}
		if p.check(TOK_IDENT) {
			prevDisallowBareCall := p.disallowBareCall
			p.disallowBareCall = false
			expr, err := p.parsePostfix()
			p.disallowBareCall = prevDisallowBareCall
			if err != nil {
				return nil, err
			}
			if id, ok := expr.(*IdentExpr); ok {
				line, col := id.Pos()
				return &CallExpr{posInfo: posInfo{Line: line, Col: col}, Callee: id.Name}, nil
			}
			return expr, nil
		}
		return nil, newError(
			"SyntaxError",
			doTok.Line,
			doTok.Col,
			p.source,
			"Expected action or function call after 'do'",
			"Example: do say \"Hello\", sum = do add x, y, or age = do ask \"Age?\" as number",
		)
	case TOK_ASK:
		p.advance()
		if p.check(TOK_LPAREN) {
			cur := p.current()
			return nil, newError("SyntaxError", cur.Line, cur.Col, p.source, "Parenthesized 'ask(...)' is not allowed", "Write: ask \"Prompt?\"")
		}
		var prompt Expr
		if !p.check(TOK_NEWLINE) && !p.check(TOK_EOF) && !p.check(TOK_RPAREN) && !p.check(TOK_COMMA) && !p.check(TOK_AS) {
			var err error
			prompt, err = p.parsePrimary()
			if err != nil {
				return nil, err
			}
		}
		return &AskExpr{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Prompt: prompt}, nil
	case TOK_LPAREN:
		p.advance()
		prevDisallowAs := p.disallowAs
		prevDisallowTopIs := p.disallowTopIs
		p.disallowAs = false
		p.disallowTopIs = false
		inner, err := p.parseExpr()
		p.disallowAs = prevDisallowAs
		p.disallowTopIs = prevDisallowTopIs
		if err != nil {
			return nil, err
		}
		if !p.match(TOK_RPAREN) {
			cur := p.current()
			return nil, newError("SyntaxError", cur.Line, cur.Col, p.source, "Missing closing ')'", "Match every '(' with a closing ')'.")
		}
		return inner, nil
	case TOK_LIST_KW:
		return p.parseTypedOrInlineList()
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
		subParser := &Parser{tokens: subTokens, source: p.source, depth: p.depth}
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
				"Use a single expression inside '{...}', e.g. \"Sum is {x + y}\".",
			)
		}
		parts = append(parts, InterpPart{IsExpr: true, Expr: expr})
	}
	return &InterpStringExpr{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Parts: parts}, nil
}

func (p *Parser) parseTypedOrInlineList() (Expr, error) {
	listTok := p.advance() // consume `list`
	if p.match(TOK_AS) {
		if !p.check(TOK_TEXT) && !p.check(TOK_NUMBER_KW) && !p.check(TOK_IDENT) {
			tok := p.current()
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				fmt.Sprintf("Expected element type ('number', 'text', or a 'def' record) after 'list as', got %q", tok.Lexeme),
				"Example: list as number, or list as particle",
			)
		}
		typeTok := p.advance()
		if !p.match(TOK_WITH) {
			return &TypedListExpr{
				posInfo:  posInfo{Line: listTok.Line, Col: listTok.Col},
				ElemType: typeTok.Lexeme,
				TypeLine: typeTok.Line,
				TypeCol:  typeTok.Col,
			}, nil
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
			if err := p.expectStmtEnd("list record row"); err != nil {
				return nil, err
			}
			rows = append(rows, row)
			p.skipNewlines()
		}
		p.advance() // consume `end`
		return &TypedListExpr{
			posInfo:  posInfo{Line: listTok.Line, Col: listTok.Col},
			ElemType: typeTok.Lexeme,
			TypeLine: typeTok.Line,
			TypeCol:  typeTok.Col,
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
