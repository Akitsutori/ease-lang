package ease

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const maxParseDepth = 256

// Parser converts Ease tokens into an AST Program using recursive descent.
type Parser struct {
	tokens         []Token
	source         string
	sourcePath     string
	pos            int
	depth          int
	disallowTopIs  bool
	stopChainedIs  bool
	disallowAs     bool
	disallowWhere  bool
	inCallArg      bool
	inTestBlock    bool
	funcArities    map[string]int
	moduleArities  map[string]map[string]int
	fileImports    map[string]*Program
	fileImportSrcs map[string]string
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
	return ParseProgramFromFile(source, "")
}

// ParseProgramFromFile lexes and parses `source` resolving relative file imports against `filePath`.
func ParseProgramFromFile(source, filePath string) (*Program, error) {
	return parseProgramWithContext(source, filePath, map[string]bool{}, nil, nil)
}

// ParseProgramWithArities lexes and parses `source` with pre-populated function arities (e.g. for REPL chunks).
func ParseProgramWithArities(source string, extraArities map[string]int) (*Program, error) {
	return parseProgramWithContext(source, "", map[string]bool{}, nil, extraArities)
}

func parseProgramWithSeen(source string, seen map[string]bool) (*Program, error) {
	return parseProgramWithContext(source, "", seen, nil, nil)
}

func parseProgramWithContext(source, sourcePath string, seen map[string]bool, activeStack []string, extraArities map[string]int) (*Program, error) {
	tokens, err := NewLexer(source).Tokenize()
	if err != nil {
		return nil, err
	}
	if sourcePath != "" {
		if abs, absErr := filepath.Abs(sourcePath); absErr == nil {
			sourcePath = abs
		}
		if len(activeStack) == 0 {
			activeStack = []string{sourcePath}
		}
	}
	p := &Parser{
		tokens:         tokens,
		source:         source,
		sourcePath:     sourcePath,
		moduleArities:  make(map[string]map[string]int),
		fileImports:    make(map[string]*Program),
		fileImportSrcs: make(map[string]string),
	}
	arities, err := p.collectSignatures(seen, activeStack, extraArities)
	if err != nil {
		return nil, err
	}
	p.funcArities = arities
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
					modProg, modErr := parseProgramWithContext(modSrc, "", map[string]bool{}, activeStack, nil)
					if modErr != nil {
						return nil, modErr
					}
					modFns := make(map[string]int, len(modProg.Functions))
					for _, fn := range modProg.Functions {
						modFns[fn.Name] = len(fn.Params)
					}
					p.moduleArities[alias] = modFns
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
					modProg, modErr := parseProgramWithContext(modSrc, absPath, map[string]bool{}, nextStack, nil)
					if modErr != nil {
						return nil, modErr
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
			blockDepth++
		case TOK_LOOP, TOK_MATCH, TOK_TEST:
			blockDepth++
		case TOK_DEF:
			if i+2 < len(p.tokens) && p.tokens[i+1].Type == TOK_IDENT && p.tokens[i+2].Type == TOK_NEWLINE {
				blockDepth++
			}
		case TOK_CHECK:
			if i+1 < len(p.tokens) && (p.tokens[i+1].Type == TOK_NEWLINE ||
				(p.tokens[i+1].Type == TOK_STRING && i+2 < len(p.tokens) && p.tokens[i+2].Type == TOK_NEWLINE)) {
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
	case TOK_LOOP, TOK_SAY:
		return p.parseStmt()
	case TOK_CHECK:
		tok := p.current()
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"'check' guards are not needed inside 'test'",
			"Write conditions directly as assertions inside 'test ... end'.",
		)
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
	return &CheckCondStmt{posInfo: posInfo{Line: line, Col: col}, Condition: expr}, nil
}

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

func (p *Parser) parseStmt() (Stmt, error) {
	if err := p.enterDepth(); err != nil {
		return nil, err
	}
	defer p.exitDepth()

	tok := p.current()
	switch tok.Type {
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
	case TOK_LOOP:
		return p.parseLoopStmt()
	case TOK_CHECK:
		return p.parseCheckGuardStmt()
	case TOK_MATCH:
		return p.parseMatchStmt()
	case TOK_SWARM:
		return p.parseSwarmLoopStmt()
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
		if tok.Type == TOK_IDENT && p.peekNext().Type != TOK_ASSIGN {
			if tok.Lexeme == "for" {
				return nil, newError(
					"SyntaxError",
					tok.Line,
					tok.Col,
					p.source,
					"'for' loops were removed; use 'loop' instead",
					"Write: loop items ... end (access the current item via 'it' and index via 'it.index').",
				)
			}
			if tok.Lexeme == "repeat" {
				return nil, newError(
					"SyntaxError",
					tok.Line,
					tok.Col,
					p.source,
					"'repeat' loops were removed; use 'loop range' instead",
					"Write: loop range 1 N ... end",
				)
			}
		}
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

func (p *Parser) parseLoopStmt() (Stmt, error) {
	loopTok := p.advance()
	if p.check(TOK_IDENT) && p.peekNext().Type == TOK_IDENT && p.peekNext().Lexeme == "in" {
		return nil, newError(
			"SyntaxError",
			p.current().Line,
			p.current().Col,
			p.source,
			fmt.Sprintf("Named loop variables like 'loop %s in ...' are not supported", p.current().Lexeme),
			"Write 'loop <list> ... end' and access the current element via 'it' (or 'it.<field>').",
		)
	}

	var iterExpr Expr
	var err error
	if !p.check(TOK_NEWLINE) && !p.check(TOK_EOF) {
		if p.check(TOK_IDENT) && p.current().Lexeme == "range" {
			rangeTok := p.advance()
			var startExpr, endExpr Expr
			startExpr, err = p.parseUnary()
			if err == nil {
				endExpr, err = p.parseUnary()
			}
			if err != nil {
				return nil, err
			}
			iterExpr = &CallExpr{
				posInfo: posInfo{Line: rangeTok.Line, Col: rangeTok.Col},
				Callee:  "range",
				Args:    []Expr{startExpr, endExpr},
			}
		} else {
			iterExpr, err = p.parseExpr()
			if err != nil {
				return nil, err
			}
		}
	}

	p.skipNewlines()

	var body []Stmt
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				loopTok.Line,
				loopTok.Col,
				p.source,
				"'loop' is missing 'end'",
				"Close every 'loop' block with 'end'.",
			)
		}
		var s Stmt
		var err error
		if p.inTestBlock {
			s, err = p.parseTestItem()
		} else {
			s, err = p.parseStmt()
		}
		if err != nil {
			return nil, err
		}
		body = append(body, s)
		p.skipNewlines()
	}
	p.advance() // consume `end`
	if err := p.expectStmtEnd("'loop' block"); err != nil {
		return nil, err
	}

	return &LoopStmt{
		posInfo:  posInfo{Line: loopTok.Line, Col: loopTok.Col},
		Iterable: iterExpr,
		Body:     body,
	}, nil
}

func (p *Parser) parseSwarmLoopStmt() (Stmt, error) {
	ex, err := p.parseSwarmLoopExpr()
	if err != nil {
		return nil, err
	}
	if err := p.expectStmtEnd("'swarm loop' block"); err != nil {
		return nil, err
	}
	line, col := ex.Pos()
	return &ExprStmt{posInfo: posInfo{Line: line, Col: col}, Expression: ex}, nil
}

func (p *Parser) parseSwarmLoopExpr() (Expr, error) {
	if err := p.enterDepth(); err != nil {
		return nil, err
	}
	defer p.exitDepth()

	swarmTok := p.advance() // consume `swarm`
	if !p.match(TOK_LOOP) {
		cur := p.current()
		return nil, newError(
			"SyntaxError",
			cur.Line,
			cur.Col,
			p.source,
			fmt.Sprintf("Expected 'loop' after 'swarm', got %q", cur.Lexeme),
			"Write: swarm loop <list_or_range> ... end",
		)
	}

	if p.check(TOK_IDENT) && p.peekNext().Type == TOK_IDENT && p.peekNext().Lexeme == "in" {
		return nil, newError(
			"SyntaxError",
			p.current().Line,
			p.current().Col,
			p.source,
			fmt.Sprintf("Named loop variables like 'swarm loop %s in ...' are not supported", p.current().Lexeme),
			"Write 'swarm loop <list> ... end' and access the current element via 'it' (or 'it.index').",
		)
	}

	if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
		return nil, newError(
			"SyntaxError",
			swarmTok.Line,
			swarmTok.Col,
			p.source,
			"'swarm loop' requires a list or 'range start end'",
			"Unbounded 'swarm loop' is not allowed. Write: swarm loop items ... end or swarm loop range 1 10 ... end.",
		)
	}

	var iterExpr Expr
	var err error
	if p.check(TOK_IDENT) && p.current().Lexeme == "range" {
		rangeTok := p.advance()
		var startExpr, endExpr Expr
		startExpr, err = p.parseUnary()
		if err == nil {
			endExpr, err = p.parseUnary()
		}
		if err != nil {
			return nil, err
		}
		iterExpr = &CallExpr{
			posInfo: posInfo{Line: rangeTok.Line, Col: rangeTok.Col},
			Callee:  "range",
			Args:    []Expr{startExpr, endExpr},
		}
	} else {
		iterExpr, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
	}

	if !p.match(TOK_NEWLINE) {
		tok := p.current()
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Expected newline after 'swarm loop' iterable",
			"Write 'swarm loop <list_or_range>' followed by body statements on new lines, closing with 'end'.",
		)
	}
	p.skipNewlines()

	prevDisallowAs := p.disallowAs
	prevDisallowWhere := p.disallowWhere
	prevDisallowTopIs := p.disallowTopIs
	prevStopChainedIs := p.stopChainedIs
	prevInCallArg := p.inCallArg
	prevInTestBlock := p.inTestBlock
	p.disallowAs = false
	p.disallowWhere = false
	p.disallowTopIs = false
	p.stopChainedIs = false
	p.inCallArg = false
	p.inTestBlock = false
	defer func() {
		p.disallowAs = prevDisallowAs
		p.disallowWhere = prevDisallowWhere
		p.disallowTopIs = prevDisallowTopIs
		p.stopChainedIs = prevStopChainedIs
		p.inCallArg = prevInCallArg
		p.inTestBlock = prevInTestBlock
	}()

	var body []Stmt
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				swarmTok.Line,
				swarmTok.Col,
				p.source,
				"'swarm loop' is missing 'end'",
				"Close every 'swarm loop' block with 'end'.",
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

	return &SwarmLoopExpr{
		posInfo:  posInfo{Line: swarmTok.Line, Col: swarmTok.Col},
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
		if p.check(TOK_NEWLINE) {
			tok := p.current()
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				"Single-line 'check <cond> -> <action>' cannot span multiple lines",
				"Put the action on the same line without 'end' (e.g. 'check cond -> x = x + 1'), or use a 'check ... end' block.",
			)
		}
		action, err := p.parseCheckArrowAction()
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
			action, err = p.parseCheckArrowAction()
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
			action, err := p.parseCheckArrowAction()
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

	hasArrow := false
	for _, r := range rules {
		if r.Action != nil {
			hasArrow = true
			break
		}
	}
	if !hasArrow && len(elseBody) == 0 {
		return nil, newError(
			"SyntaxError",
			chkTok.Line,
			chkTok.Col,
			p.source,
			"'check' block requires '->' actions or an 'else' branch",
			"Add '-> <action>' to each rule, or add an 'else' branch (e.g. 'check x <= 0 -> fail \"...\"').",
		)
	}

	return &CheckGuardStmt{
		posInfo:  posInfo{Line: chkTok.Line, Col: chkTok.Col},
		IsBlock:  true,
		Rules:    rules,
		ElseBody: elseBody,
	}, nil
}

func (p *Parser) parseCheckArrowAction() (Stmt, error) {
	if p.check(TOK_END) {
		endTok := p.advance()
		if err := p.expectStmtEnd("'-> end'"); err != nil {
			return nil, err
		}
		return &LoopEndStmt{posInfo: posInfo{Line: endTok.Line, Col: endTok.Col}}, nil
	}
	return p.parseStmt()
}

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
		if !p.match(TOK_ARROW) {
			cur := p.current()
			return nil, newError(
				"SyntaxError",
				cur.Line,
				cur.Col,
				p.source,
				fmt.Sprintf("Expected '->' after match pattern, got %q", cur.Lexeme),
				"Write '\"pattern\" -> action' on each branch.",
			)
		}
		if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
			cur := p.current()
			return nil, newError(
				"SyntaxError",
				cur.Line,
				cur.Col,
				p.source,
				"'match' branch '-> <action>' cannot be empty",
				"Put the action on the same line as '->' (e.g. '\"PING\" -> \"PONG\"').",
			)
		}
		action, err := p.parseCheckArrowAction()
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
	}

	if len(arms) == 0 {
		return nil, newError(
			"SyntaxError",
			matchTok.Line,
			matchTok.Col,
			p.source,
			"'match' block requires at least one pattern branch",
			"Add at least one '\"pattern\" -> action' branch before 'end'.",
		)
	}

	var elseBody []Stmt
	if p.match(TOK_ELSE) {
		if p.match(TOK_ARROW) {
			if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
				cur := p.current()
				return nil, newError(
					"SyntaxError",
					cur.Line,
					cur.Col,
					p.source,
					"'match' 'else -> <action>' branch cannot be empty",
					"Write 'else -> <action>' on one line or use a multi-line 'else ... end' block.",
				)
			}
			action, err := p.parseCheckArrowAction()
			if err != nil {
				return nil, err
			}
			elseBody = append(elseBody, action)
			p.skipNewlines()
		} else {
			if !p.match(TOK_NEWLINE) {
				cur := p.current()
				return nil, newError(
					"SyntaxError",
					cur.Line,
					cur.Col,
					p.source,
					fmt.Sprintf("Expected '->' or newline after 'else' in 'match', got %q", cur.Lexeme),
					"Write 'else -> <action>' or start a block on the next line.",
				)
			}
			p.skipNewlines()
			for !p.check(TOK_END) {
				if p.check(TOK_EOF) {
					return nil, newError(
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
					return nil, err
				}
				elseBody = append(elseBody, s)
				p.skipNewlines()
			}
		}
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

	var hasElse bool
	var elseBody []Stmt
	if p.match(TOK_ELSE) {
		hasElse = true
		if p.match(TOK_ARROW) {
			if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
				cur := p.current()
				return nil, newError(
					"SyntaxError",
					cur.Line,
					cur.Col,
					p.source,
					"'match' 'else -> <value>' branch cannot be empty",
					"Write 'else -> <expression>' on the same line.",
				)
			}
			elseExpr, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if err := p.expectStmtEnd("'match' else branch"); err != nil {
				return nil, err
			}
			elLine, elCol := elseExpr.Pos()
			elseBody = append(elseBody, &ExprStmt{posInfo: posInfo{Line: elLine, Col: elCol}, Expression: elseExpr})
			p.skipNewlines()
		} else {
			if !p.match(TOK_NEWLINE) {
				cur := p.current()
				return nil, newError(
					"SyntaxError",
					cur.Line,
					cur.Col,
					p.source,
					fmt.Sprintf("Expected '->' or newline after 'else' in 'match', got %q", cur.Lexeme),
					"Write 'else -> <expression>' or start a block on the next line.",
				)
			}
			p.skipNewlines()
			for !p.check(TOK_END) {
				if p.check(TOK_EOF) {
					return nil, newError(
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
					return nil, err
				}
				elseBody = append(elseBody, s)
				p.skipNewlines()
			}
			if len(elseBody) == 0 {
				return nil, newError(
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
				return nil, newError(
					"SyntaxError",
					elLine,
					elCol,
					p.source,
					"'match' expression 'else' block must end with an expression",
					"End the 'else' block with the fallback value expression.",
				)
			}
		}
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
						"Example: xs.(it.index - 1)",
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
				cond, err := p.parseExpr()
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
		p.advance()
		return &IdentExpr{posInfo: posInfo{Line: tok.Line, Col: tok.Col}, Name: tok.Lexeme}, nil
	case TOK_LPAREN:
		p.advance()
		prevDisallowAs := p.disallowAs
		prevDisallowWhere := p.disallowWhere
		prevDisallowTopIs := p.disallowTopIs
		prevInCallArg := p.inCallArg
		p.disallowAs = false
		p.disallowWhere = false
		p.disallowTopIs = false
		p.inCallArg = false
		inner, err := p.parseExpr()
		p.disallowAs = prevDisallowAs
		p.disallowWhere = prevDisallowWhere
		p.disallowTopIs = prevDisallowTopIs
		p.inCallArg = prevInCallArg
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
