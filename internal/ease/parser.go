package ease

import (
	"fmt"
	"path/filepath"
	"strings"
)

const maxParseDepth = 256

// Parser converts Ease tokens into an AST Program using recursive descent.
type Parser struct {
	tokens             []Token
	source             string
	sourcePath         string
	pos                int
	depth              int
	disallowTopIs      bool
	stopChainedIs      bool
	disallowAs         bool
	disallowWhere      bool
	inCallArg          bool
	inTestBlock        bool
	funcArities        map[string]int
	moduleArities      map[string]map[string]int
	stdModuleOrigNames map[string]string
	fileImports        map[string]*Program
	fileImportSrcs     map[string]string
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
	return parseProgramWithContext(source, filePath, map[string]bool{}, nil, nil, false, false)
}

// ParseProgramWithArities lexes and parses `source` with pre-populated function arities (e.g. for REPL chunks).
func ParseProgramWithArities(source string, extraArities map[string]int) (*Program, error) {
	return parseProgramWithContext(source, "", map[string]bool{}, nil, extraArities, false, false)
}

// ParseProgramForRepl parses and validates an interactive REPL chunk, allowing the final
// top-level non-condition expression to be printed while enforcing all file rules everywhere else.
func ParseProgramForRepl(source string) (*Program, error) {
	return parseProgramWithContext(source, "", map[string]bool{}, nil, nil, false, true)
}

// ParseProgramFromFileForRepl is like ParseProgramForRepl with relative file imports resolved against filePath.
func ParseProgramFromFileForRepl(source, filePath string) (*Program, error) {
	return parseProgramWithContext(source, filePath, map[string]bool{}, nil, nil, false, true)
}

// ParseProgramWithAritiesForRepl parses an interactive debugger/watch expression with pre-populated arities.
func ParseProgramWithAritiesForRepl(source string, extraArities map[string]int) (*Program, error) {
	return parseProgramWithContext(source, "", map[string]bool{}, nil, extraArities, false, true)
}

func parseProgramWithSeen(source string, seen map[string]bool) (*Program, error) {
	return parseProgramWithContext(source, "", seen, nil, nil, false, false)
}

func parseProgramWithContext(source, sourcePath string, seen map[string]bool, activeStack []string, extraArities map[string]int, allowInternalBuiltins bool, allowTrailingValueExpr bool) (*Program, error) {
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
		tokens:             tokens,
		source:             source,
		sourcePath:         sourcePath,
		moduleArities:      make(map[string]map[string]int),
		stdModuleOrigNames: make(map[string]string),
		fileImports:        make(map[string]*Program),
		fileImportSrcs:     make(map[string]string),
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
	if err := validateProgramTypesWithOptions(prog, source, allowInternalBuiltins, allowTrailingValueExpr); err != nil {
		return nil, err
	}
	return prog, nil
}

func (p *Parser) matchFieldIdent() (Token, bool) {
	switch p.current().Type {
	case TOK_IDENT, TOK_NUMBER_KW, TOK_TEXT, TOK_LIST_KW:
		return p.advance(), true
	default:
		return Token{}, false
	}
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

func minusSpaceHint(tok Token, defaultHint string) string {
	if tok.Type == TOK_NUMBER && strings.HasPrefix(tok.Lexeme, "-") {
		return "Put spaces around '-' for subtraction (e.g. 'x - 2'); '-2' without a space is parsed as a negative number argument."
	}
	return defaultHint
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
	hint = minusSpaceHint(tok, hint)
	return newError(
		"SyntaxError",
		tok.Line,
		tok.Col,
		p.source,
		fmt.Sprintf("Unexpected %q after %s", tok.Lexeme, context),
		hint,
	)
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
	case TOK_SPAWN:
		return p.parseSpawnStmt()
	case TOK_ARROW:
		arrowTok := p.advance()
		return p.parseArrowAction(arrowTok)
	case TOK_TAKE:
		return p.parseTakeStmt()
	case TOK_ELSE:
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Unexpected 'else' outside a multi-line 'check' or 'match' block",
			"Single-line 'check <cond> -> <val>' cannot be followed by 'else'. Put 'check' on its own line for a verdict table, or omit '->' for a 'check <cond> ... else ... end' block.",
		)
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
			if tok.Lexeme == "fail" {
				return nil, newError(
					"SyntaxError",
					tok.Line,
					tok.Col,
					p.source,
					"'fail' has been removed from Ease",
					"'fail' was removed; return an explicit error union/record with '-> ...' instead.",
				)
			}
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
			if tok.Lexeme == "return" {
				return nil, newError(
					"SyntaxError",
					tok.Line,
					tok.Col,
					p.source,
					"'return' is not a keyword in Ease",
					"Use '-> <value>' to return values in Ease.",
				)
			}
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

func (p *Parser) parseSpawnStmt() (Stmt, error) {
	spawnTok := p.advance() // consume `spawn`
	if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
		return nil, newError(
			"SyntaxError",
			spawnTok.Line,
			spawnTok.Col,
			p.source,
			"'spawn' requires a function call, e.g. 'spawn handle_client conn'",
			"Write: spawn <fn> [arg1 arg2 ...] or spawn <mod>:<fn> [arg1 ...]",
		)
	}
	expr, err := p.parseExpr()
	if err != nil {
		return nil, newError(
			"SyntaxError",
			spawnTok.Line,
			spawnTok.Col,
			p.source,
			"'spawn' requires a function call, e.g. 'spawn handle_client conn'",
			err.Error(),
		)
	}
	call, ok := expr.(*CallExpr)
	if !ok {
		return nil, newError(
			"SyntaxError",
			spawnTok.Line,
			spawnTok.Col,
			p.source,
			"'spawn' requires a function call, e.g. 'spawn handle_client conn'",
			"Define a 'to' function and launch it with 'spawn fn arg1 arg2'.",
		)
	}
	if err := p.expectStmtEnd("'spawn' statement"); err != nil {
		return nil, err
	}
	return &SpawnStmt{
		posInfo: posInfo{Line: spawnTok.Line, Col: spawnTok.Col},
		Call:    call,
	}, nil
}

func (p *Parser) parseTakeStmt() (Stmt, error) {
	takeTok := p.advance() // consume `take`
	if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
		return nil, newError(
			"SyntaxError",
			takeTok.Line,
			takeTok.Col,
			p.source,
			"'take' requires an inbox expression",
			"Write: take <inbox> ... end",
		)
	}
	inboxExpr, err := p.parseExpr()
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
			"Expected newline after 'take' inbox expression",
			"Write 'take <inbox>' followed by body statements on new lines, closing with 'end'.",
		)
	}
	p.skipNewlines()

	prevInTestBlock := p.inTestBlock
	p.inTestBlock = false
	defer func() { p.inTestBlock = prevInTestBlock }()

	var body []Stmt
	for !p.check(TOK_END) {
		if p.check(TOK_EOF) {
			return nil, newError(
				"SyntaxError",
				takeTok.Line,
				takeTok.Col,
				p.source,
				"'take' block is missing 'end'",
				"Close every 'take' block with 'end'.",
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
	if err := p.expectStmtEnd("'take' block"); err != nil {
		return nil, err
	}
	return &TakeStmt{
		posInfo: posInfo{Line: takeTok.Line, Col: takeTok.Col},
		Inbox:   inboxExpr,
		Body:    body,
	}, nil
}

func (p *Parser) parseCheckGuardStmt() (Stmt, error) {
	chkTok := p.advance() // consume `check`

	if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
		tok := p.current()
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Expected condition expression after 'check'",
			"Write 'check <cond> -> <value | end>' on one line, or 'check <cond>' followed by a statement block and 'end'.",
		)
	}

	// Form 1 (single-line) or Form 2 (multi-line block): `check <cond> ...`
	cond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if arrowTok := p.current(); p.match(TOK_ARROW) {
		if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
			tok := p.current()
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				"Single-line 'check <cond> -> <value | end>' cannot span multiple lines",
				"Start the value expression on the same line right after '->', or omit '->' ('check <cond>\\n  -> <value>\\nend') for multi-line guards.",
			)
		}
		action, err := p.parseArrowAction(arrowTok)
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

	// Form 2: Multi-line block: `check <cond>\n  stmts...\n  [else\n  stmts...]\nend`
	if err := p.expectStmtEnd("'check <condition>'"); err != nil {
		return nil, err
	}
	p.skipNewlines()
	var body []Stmt
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

	var elseBody []Stmt
	if p.match(TOK_ELSE) {
		if p.match(TOK_ARROW) {
			tok := p.current()
			return nil, newError(
				"SyntaxError",
				tok.Line,
				tok.Col,
				p.source,
				"'else ->' is not valid in a 'check' block",
				"Put 'else' on its own line followed by statements (and '-> <value>' if returning).",
			)
		}
		if err := p.expectStmtEnd("'else'"); err != nil {
			return nil, err
		}
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
			elseBody = append(elseBody, s)
			p.skipNewlines()
		}
	}

	if !p.match(TOK_END) {
		tok := p.current()
		return nil, newError("SyntaxError", tok.Line, tok.Col, p.source, "Expected 'end' to close 'check' block", "Add 'end' after your 'check' block.")
	}
	if err := p.expectStmtEnd("'check' block"); err != nil {
		return nil, err
	}
	return &CheckGuardStmt{
		posInfo:       posInfo{Line: chkTok.Line, Col: chkTok.Col},
		IsBlock:       true,
		IsEffectBlock: true,
		Cond:          cond,
		Body:          body,
		ElseBody:      elseBody,
	}, nil
}

func (p *Parser) parseArrowAction(arrowTok Token) (Stmt, error) {
	if p.check(TOK_END) {
		endTok := p.advance()
		if err := p.expectStmtEnd("'-> end'"); err != nil {
			return nil, err
		}
		return &LoopEndStmt{posInfo: posInfo{Line: endTok.Line, Col: endTok.Col}}, nil
	}
	return p.parseArrowReturnValueOnly(arrowTok)
}

func (p *Parser) parseArrowReturnValueOnly(arrowTok Token) (*ReturnStmt, error) {
	tok := p.current()
	if p.check(TOK_NEWLINE) || p.check(TOK_EOF) {
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Expected value expression or 'end' after '->'",
			"Start the value expression on the same line right after '->', or omit '->' ('check <cond>\\n  -> <value>\\nend') for multi-line guards.",
		)
	}
	switch tok.Type {
	case TOK_SAY, TOK_LOOP, TOK_CHECK, TOK_SPAWN, TOK_TAKE, TOK_TO, TOK_DEF, TOK_TEST, TOK_END:
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Statements and side effects are not allowed after '->'",
			"Put only a value expression after '->' (or use a multi-line block without '->' for side effects).",
		)
	}
	if tok.Type == TOK_IDENT && p.peekNext().Type == TOK_ASSIGN {
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"Assignments are not allowed after '->'",
			"Put only a value expression after '->' (or use a multi-line block without '->' for assignments).",
		)
	}
	if tok.Type == TOK_IDENT && tok.Lexeme == "fail" {
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"'fail' has been removed from Ease",
			"'fail' was removed; return an explicit error union/record with '-> ...' instead.",
		)
	}
	if tok.Type == TOK_IDENT && tok.Lexeme == "decode_json" {
		return nil, newError(
			"TypeError",
			tok.Line,
			tok.Col,
			p.source,
			"'decode_json' was removed from Ease",
			"Use '<text> as json' or 'load <path> as json' instead.",
		)
	}
	if tok.Type == TOK_IDENT && tok.Lexeme == "encode_json" {
		return nil, newError(
			"TypeError",
			tok.Line,
			tok.Col,
			p.source,
			"'encode_json' was removed from Ease",
			"Use '<value> as json' or 'save <path> <value> as json' instead.",
		)
	}
	if tok.Type == TOK_IDENT && tok.Lexeme == "return" {
		return nil, newError(
			"SyntaxError",
			tok.Line,
			tok.Col,
			p.source,
			"'return' is not a keyword in Ease",
			"Put the return expression directly after '->'.",
		)
	}
	val, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if err := p.expectStmtEnd("'->' return value"); err != nil {
		return nil, err
	}
	return &ReturnStmt{posInfo: posInfo{Line: arrowTok.Line, Col: arrowTok.Col}, Value: val}, nil
}
