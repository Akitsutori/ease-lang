package ease

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func (p *Parser) collectSignatures(seen map[string]bool, activeStack []string, extraArities map[string]int) (map[string]int, error) {
	arities := make(map[string]int, len(builtinArities)+len(extraArities))
	for name, arity := range builtinArities {
		arities[name] = arity
	}
	for name, arity := range extraArities {
		arities[name] = arity
	}

	importSeen := make(map[string]bool, len(seen))
	for k, v := range seen {
		importSeen[k] = v
	}

	blockDepth := 0
	for i := 0; i < len(p.tokens); i++ {
		tok := p.tokens[i]
		switch tok.Type {
		case TOK_IMPORT:
			if blockDepth == 0 && i+1 < len(p.tokens) {
				if p.tokens[i+1].Type == TOK_IDENT {
					modName := p.tokens[i+1].Lexeme
					alias := modName
					if i+3 < len(p.tokens) && p.tokens[i+2].Type == TOK_AS && p.tokens[i+3].Type == TOK_IDENT {
						alias = p.tokens[i+3].Lexeme
					}
					modSrc, err := loadStdModuleSource(modName)
					if err != nil {
						return nil, newError(
							"ImportError",
							tok.Line,
							tok.Col,
							p.source,
							err.Error(),
							"Check available standard modules under std/, e.g. 'import network'.",
						)
					}
					modProg, modErr := parseProgramWithContext(modSrc, "", map[string]bool{}, activeStack, nil, true, false)
					if modErr != nil {
						return nil, modErr
					}
					modFns := make(map[string]int, len(modProg.Functions))
					for _, fn := range modProg.Functions {
						modFns[fn.Name] = len(fn.Params)
					}
					p.moduleArities[alias] = modFns
					p.stdModuleOrigNames[alias] = modName
					p.fileImports[alias] = modProg
					p.fileImportSrcs[alias] = modSrc
				} else if p.tokens[i+1].Type == TOK_STRING && i+3 < len(p.tokens) && p.tokens[i+2].Type == TOK_AS && p.tokens[i+3].Type == TOK_IDENT {
					pathStr := p.tokens[i+1].Lexeme
					alias := p.tokens[i+3].Lexeme
					baseDir := "."
					if p.sourcePath != "" {
						baseDir = filepath.Dir(p.sourcePath)
					}
					joined := filepath.Clean(filepath.Join(baseDir, pathStr))
					absPath, err := filepath.Abs(joined)
					if err != nil {
						absPath = joined
					}
					if slices.Contains(activeStack, absPath) {
						return nil, newError(
							"ImportError",
							tok.Line,
							tok.Col,
							p.source,
							fmt.Sprintf("Cyclic import detected: %s -> %s", filepath.Base(p.sourcePath), pathStr),
							"Remove the circular import between these module files.",
						)
					}
					rawBytes, readErr := os.ReadFile(absPath)
					if readErr != nil {
						return nil, newError(
							"ImportError",
							tok.Line,
							tok.Col,
							p.source,
							fmt.Sprintf("Cannot import %q: %v", pathStr, readErr),
							"Check that the imported file path exists relative to the current file.",
						)
					}
					modSrc := string(rawBytes)
					nextStack := append(slices.Clone(activeStack), absPath)
					modProg, modErr := parseProgramWithContext(modSrc, absPath, map[string]bool{}, nextStack, nil, false, false)
					if modErr != nil {
						return nil, modErr
					}
					if modProg.IsEmpty() {
						tok := p.tokens[i]
						return nil, newError(
							"ImportError",
							tok.Line,
							tok.Col,
							p.source,
							fmt.Sprintf("Imported file %q contains no Ease declarations", pathStr),
							"Save the module file and export at least one 'def' or 'to' declaration.",
						)
					}
					modFns := make(map[string]int, len(modProg.Functions))
					for _, fn := range modProg.Functions {
						modFns[fn.Name] = len(fn.Params)
					}
					p.moduleArities[alias] = modFns
					p.fileImports[alias] = modProg
					p.fileImportSrcs[alias] = modSrc
				}
			}
		case TOK_TO:
			hasArrowOnLine := false
			for k := i + 1; k < len(p.tokens); k++ {
				if p.tokens[k].Type == TOK_NEWLINE || p.tokens[k].Type == TOK_EOF {
					break
				}
				if p.tokens[k].Type == TOK_ARROW {
					hasArrowOnLine = true
					break
				}
			}
			if blockDepth == 0 && i+1 < len(p.tokens) && p.tokens[i+1].Type == TOK_IDENT {
				fnName := p.tokens[i+1].Lexeme
				paramCount := 0
				j := i + 2
				for j < len(p.tokens) && p.tokens[j].Type == TOK_IDENT {
					paramCount++
					j++
				}
				if _, isBuiltin := builtinArities[fnName]; !isBuiltin {
					arities[fnName] = paramCount
				}
			}
			if !hasArrowOnLine {
				blockDepth++
			}
		case TOK_LOOP, TOK_MATCH, TOK_TEST, TOK_TAKE:
			blockDepth++
		case TOK_DEF:
			if i+2 < len(p.tokens) && p.tokens[i+1].Type == TOK_IDENT && p.tokens[i+2].Type == TOK_NEWLINE {
				blockDepth++
			}
		case TOK_CHECK:
			hasArrowOnLine := false
			for k := i + 1; k < len(p.tokens); k++ {
				if p.tokens[k].Type == TOK_NEWLINE || p.tokens[k].Type == TOK_EOF {
					break
				}
				if p.tokens[k].Type == TOK_ARROW {
					hasArrowOnLine = true
					break
				}
			}
			if !hasArrowOnLine {
				blockDepth++
			}
		case TOK_WITH, TOK_WHERE:
			if i+1 < len(p.tokens) && p.tokens[i+1].Type == TOK_NEWLINE {
				blockDepth++
			}
		case TOK_END:
			if i == 0 || p.tokens[i-1].Type != TOK_ARROW {
				if blockDepth > 0 {
					blockDepth--
				}
			}
		}
	}
	return arities, nil
}

func (p *Parser) parseProgram() (*Program, error) {
	prog := &Program{}
	p.skipNewlines()

	for !p.check(TOK_EOF) {
		switch p.current().Type {
		case TOK_IMPORT:
			impTok := p.advance()
			if p.check(TOK_STRING) {
				pathTok := p.advance()
				if !p.match(TOK_AS) || !p.check(TOK_IDENT) {
					tok := p.current()
					return nil, newError(
						"SyntaxError",
						tok.Line,
						tok.Col,
						p.source,
						"Expected 'as <alias>' after imported file path",
						"Example: import \"./billing.ease\" as billing",
					)
				}
				aliasTok := p.advance()
				if err := p.expectStmtEnd("'import' statement"); err != nil {
					return nil, err
				}
				prog.Imports = append(prog.Imports, &ImportDecl{
					posInfo:      posInfo{Line: impTok.Line, Col: impTok.Col},
					Name:         aliasTok.Lexeme,
					Path:         pathTok.Lexeme,
					Alias:        aliasTok.Lexeme,
					IsFileImport: true,
					ModuleProg:   p.fileImports[aliasTok.Lexeme],
					ModuleSource: p.fileImportSrcs[aliasTok.Lexeme],
				})
				continue
			}
			if !p.check(TOK_IDENT) {
				tok := p.current()
				return nil, newError(
					"SyntaxError",
					tok.Line,
					tok.Col,
					p.source,
					fmt.Sprintf("Expected module name or file path after 'import', got %q", tok.Lexeme),
					"Example: import network or import \"./billing.ease\" as billing",
				)
			}
			modTok := p.advance()
			alias := modTok.Lexeme
			if p.match(TOK_AS) {
				if !p.check(TOK_IDENT) {
					tok := p.current()
					return nil, newError(
						"SyntaxError",
						tok.Line,
						tok.Col,
						p.source,
						"Expected module alias after 'as'",
						fmt.Sprintf("Example: import %s as my_%s", modTok.Lexeme, modTok.Lexeme),
					)
				}
				alias = p.advance().Lexeme
			}
			if err := p.expectStmtEnd("'import' statement"); err != nil {
				return nil, err
			}
			prog.Imports = append(prog.Imports, &ImportDecl{
				posInfo:      posInfo{Line: impTok.Line, Col: impTok.Col},
				Name:         modTok.Lexeme,
				Alias:        alias,
				IsFileImport: false,
				ModuleProg:   p.fileImports[alias],
				ModuleSource: p.fileImportSrcs[alias],
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
				tok := p.current()
				return nil, newError(
					"SyntaxError",
					tok.Line,
					tok.Col,
					p.source,
					"Top-level 'check \"...\"' test blocks were removed; use 'test \"...\"' instead",
					"'check' is reserved for runtime guards (check cond -> action); write tests in 'test \"label\" ... end'.",
				)
			}
			stmt, err := p.parseStmt()
			if err != nil {
				return nil, err
			}
			prog.Body = append(prog.Body, stmt)
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
	if !p.check(TOK_IDENT) {
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

	if arrowTok := p.current(); p.match(TOK_ARROW) {
		if p.check(TOK_NEWLINE) || p.check(TOK_EOF) || p.check(TOK_END) {
			tok := p.current()
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				fmt.Sprintf("Expected return expression after '->' in 'to %s'", nameTok.Lexeme),
				fmt.Sprintf("Example: to %s %s -> <expr>", nameTok.Lexeme, strings.Join(params, " ")),
			)
		}
		retStmt, err := p.parseArrowReturnValueOnly(arrowTok)
		if err != nil {
			return nil, err
		}
		return &FuncDecl{
			posInfo: posInfo{Line: toTok.Line, Col: toTok.Col},
			Name:    nameTok.Lexeme,
			Params:  params,
			Body:    []Stmt{retStmt},
		}, nil
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
	p.pushBlock("to", nameTok.Lexeme, toTok.Line)
	defer p.popBlock()

	var body []Stmt
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				toTok.Line,
				toTok.Col,
				p.source,
				fmt.Sprintf("Function 'to %s' is missing a closing 'end'", nameTok.Lexeme),
				p.eofBlockHint("Every 'to ...' block must end with 'end' on its own line."),
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
		return nil, nil, &ConstDeclStmt{
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
		fieldTok, ok := p.matchFieldIdent()
		if !ok {
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
		if fieldTok.Lexeme == "index" {
			return nil, nil, nil, newError(
				"SyntaxError",
				fieldTok.Line,
				fieldTok.Col,
				p.source,
				fmt.Sprintf("Field name %q in 'def %s' is reserved for 'it.index' inside 'loop'", fieldTok.Lexeme, nameTok.Lexeme),
				"Choose another field name such as 'idx' or 'pos'.",
			)
		}
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

func (p *Parser) parseTopTestBlock() (*CheckBlock, error) {
	tTok := p.advance() // consume `test`
	title := "test"
	if p.check(TOK_STRING) {
		title = p.advance().Lexeme
	}
	p.skipNewlines()

	prevInTest := p.inTestBlock
	p.inTestBlock = true
	defer func() { p.inTestBlock = prevInTest }()
	p.pushBlock("test", title, tTok.Line)
	defer p.popBlock()

	var body []Stmt
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				tTok.Line,
				tTok.Col,
				p.source,
				"'test' block is missing 'end'",
				p.eofBlockHint("Close every 'test' block with 'end'."),
			)
		}
		item, err := p.parseTestItem()
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

func (p *Parser) parseTestItem() (Stmt, error) {
	switch p.current().Type {
	case TOK_LOOP, TOK_SAY, TOK_SPAWN, TOK_TAKE:
		return p.parseStmt()
	case TOK_CHECK:
		tok := p.current()
		if p.peekNext().Type == TOK_NEWLINE {
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				"Single-line 'check' is not used in 'test'; write assertions directly without 'check' (e.g. 'x is 1'), or use a multi-line 'check <cond> ... end' block.",
				"Write assertions directly inside 'test ... end', or put a condition on the 'check' header line for a multi-line effect block.",
			)
		}
		stmt, err := p.parseCheckGuardStmt()
		if err != nil {
			return nil, err
		}
		if chk, ok := stmt.(*CheckGuardStmt); ok && !chk.IsEffectBlock {
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				"Single-line 'check' is not used in 'test'; write assertions directly without 'check' (e.g. 'x is 1'), or use a multi-line 'check <cond> ... end' block.",
				"Write assertions directly inside 'test ... end', or use a multi-line 'check <cond> ... end' block.",
			)
		}
		return stmt, nil
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
	if err := p.checkNoFieldOrIndexAssign(expr); err != nil {
		return nil, err
	}
	line, col := expr.Pos()

	if p.match(TOK_ARROW) {
		p.skipNewlines()
		var body []Stmt
		for !p.check(TOK_END) {
			if p.check(TOK_EOF) {
				return nil, newError(
					"SyntaxError",
					line,
					col,
					p.source,
					"Error assertion block '->' is missing 'end'",
					"Close every 'expr -> ... end' block with 'end'.",
				)
			}
			item, err := p.parseTestItem()
			if err != nil {
				return nil, err
			}
			body = append(body, item)
			p.skipNewlines()
		}
		p.advance() // consume `end`
		if err := p.expectStmtEnd("error assertion block"); err != nil {
			return nil, err
		}
		return &CheckErrorBlockStmt{
			posInfo:    posInfo{Line: line, Col: col},
			Expression: expr,
			Body:       body,
		}, nil
	}

	if p.match(TOK_IS) {
		negated := p.match(TOK_NOT)
		if p.check(TOK_IDENT) && p.current().Lexeme == "error" {
			p.advance()
			if err := p.expectStmtEnd("'is error' assertion"); err != nil {
				return nil, err
			}
			return &CheckIsErrorStmt{
				posInfo:    posInfo{Line: line, Col: col},
				Expression: expr,
				Negated:    negated,
			}, nil
		}
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
		rhs, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		if p.check(TOK_AND) || p.check(TOK_OR) {
			op := "is"
			if negated {
				op = "is-not"
			}
			cond := Expr(&BinaryExpr{posInfo: posInfo{Line: line, Col: col}, Left: expr, Op: op, Right: rhs})
			cond, err = p.parseLogicTail(cond)
			if err != nil {
				return nil, err
			}
			if err := p.expectStmtEnd("test condition"); err != nil {
				return nil, err
			}
			return &CheckCondStmt{posInfo: posInfo{Line: line, Col: col}, Condition: cond}, nil
		}
		if negated {
			if err := p.expectStmtEnd("'is not' assertion"); err != nil {
				return nil, err
			}
			return &CheckIsNotStmt{posInfo: posInfo{Line: line, Col: col}, Actual: expr, Unexpected: rhs}, nil
		}
		if err := p.expectStmtEnd("'is' assertion"); err != nil {
			return nil, err
		}
		return &CheckIsStmt{posInfo: posInfo{Line: line, Col: col}, Actual: expr, Expected: rhs}, nil
	}

	if err := p.expectStmtEnd("test assertion"); err != nil {
		return nil, err
	}
	if _, isSwarm := expr.(*SwarmLoopExpr); isSwarm {
		return &ExprStmt{posInfo: posInfo{Line: line, Col: col}, Expression: expr}, nil
	}
	if call, isCall := expr.(*CallExpr); isCall && ((call.Module == "" && (call.Callee == "put" || call.Callee == "close" || call.Callee == "save" || call.Callee == "synth_play" || call.Callee == "sys_exit")) ||
		(call.Module != "" && p.stdModuleOrigNames[call.Module] == "synth" && call.Callee == "play") ||
		(call.Module != "" && p.stdModuleOrigNames[call.Module] == "system" && call.Callee == "exit")) {
		return &ExprStmt{posInfo: posInfo{Line: line, Col: col}, Expression: expr}, nil
	}
	if castExpr, isCast := expr.(*CastExpr); isCast {
		if call, isCall := castExpr.Target.(*CallExpr); isCall && call.Module == "" && call.Callee == "save" {
			return &ExprStmt{posInfo: posInfo{Line: line, Col: col}, Expression: expr}, nil
		}
	}
	return &CheckCondStmt{posInfo: posInfo{Line: line, Col: col}, Condition: expr}, nil
}
