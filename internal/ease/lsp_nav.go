package ease

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

type lspBuiltinDoc struct {
	sig  string
	desc string
}

var builtinDocs = map[string]lspBuiltinDoc{
	"say":      {sig: "say <value>", desc: "Print a value followed by a newline (`void` statement)."},
	"ask":      {sig: "ask <prompt>", desc: "Read a UTF-8/NFC line from stdin and return it as `text`."},
	"count":    {sig: "count <list_or_text>", desc: "Return the element count of a `list` or rune length of `text`."},
	"range":    {sig: "range <start> <end>", desc: "Build an inclusive integer `list<number>` from `start` to `end`."},
	"append":   {sig: "append <list> <item_or_list>", desc: "Return a new `list` with an item or list appended."},
	"slice":    {sig: "slice <list_or_text> <start> <end>", desc: "Return a 1-based inclusive sub-slice (clamped to `1..len`)."},
	"split":    {sig: "split <text> <sep>", desc: "Split `text` by separator `sep` into a `list<text>`."},
	"join":     {sig: "join <text_list> <sep>", desc: "Join a `list<text>` with separator `sep` into `text`."},
	"trim":     {sig: "trim <text>", desc: "Strip leading and trailing whitespace from `text`."},
	"upper":    {sig: "upper <text>", desc: "Convert `text` to uppercase."},
	"lower":    {sig: "lower <text>", desc: "Convert `text` to lowercase."},
	"replace":  {sig: "replace <text> <old> <new>", desc: "Replace all non-empty occurrences of `old` with `new`."},
	"contains": {sig: "contains <text_or_atomic_list> <item>", desc: "Condition checking whether `text` contains substring `item` or an atomic list (`number`, `text`, or scalar union) contains `item`."},
	"sum":      {sig: "sum <num_list>", desc: "Sum all numbers in a `list<number>`."},
	"avg":      {sig: "avg <num_list>", desc: "Compute the arithmetic mean of a non-empty `list<number>`."},
	"sort":     {sig: "sort <list> [by <key1> [, <key2> ...]]", desc: "Return a stably sorted copy of a list (optionally projected by one or more keys, with closed literal unions sorted in declaration order)."},
	"abs":      {sig: "abs <number>", desc: "Return the absolute value of a `number`."},
	"floor":    {sig: "floor <number>", desc: "Round a `number` down to the nearest integer."},
	"round":    {sig: "round <number>", desc: "Round a `number` to the nearest integer."},
	"min":      {sig: "min <a> <b>", desc: "Return the smaller of two numbers."},
	"max":      {sig: "max <a> <b>", desc: "Return the larger of two numbers."},
	"type":     {sig: "type <value>", desc: "Return the runtime type name of `<value>` as `text`."},
	"fields":   {sig: "fields <record_or_map>", desc: "Return the field/key names of a record or map as `list<text>`."},
	"load":     {sig: "load <path> [as text|json|csv|bytes]", desc: "Load a file (max 50 MB) in the specified format."},
	"save":     {sig: "save <path> <data> [as text|json|csv|bytes]", desc: "Write data to a file (`void` statement; `as <format>` required for structured/byte data)."},
	"put":      {sig: "put <inbox> <value>", desc: "Send a value into a typed `inbox` (`void` statement)."},
	"close":    {sig: "close <inbox>", desc: "Close an `inbox` idempotently (`void` statement)."},
}

var stdModuleDocs = map[string]string{
	"network": "Standard `network` module: HTTP client (`network:fetch`, `network:send`) and `inbox`-driven HTTP/TCP servers (`network:serve_http`, `network:reply_text`, `network:reply_json`, `network:serve_tcp`, `network:tcp_send`).",
	"screen":  "Standard `screen` module: 2D window & event loop (`screen.screen`, `screen.scene`, `screen:open`, `screen:present`).",
	"random":  "Standard `random` module: goroutine-safe randomness (`random:roll`, `random:pick`, `random:shuffle`).",
	"synth":   "Standard `synth` module: retro chiptune waveform synthesizer (`synth.melody`, `synth.wave_kind`, `synth:play`).",
	"system":  "Standard `system` module: CLI arguments (`system.arguments`), success code (`system.ok`), and termination (`system:exit`).",
}

func splitLinesNormalized(src string) []string {
	return strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
}

func normalizeFSPath(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

func pathsEqual(a, b string) bool {
	na := normalizeFSPath(a)
	nb := normalizeFSPath(b)
	if na == "" || nb == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(na, nb)
	}
	return na == nb
}

func pathToFileURI(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	slash := filepath.ToSlash(abs)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	u := url.URL{Scheme: "file", Path: slash}
	return u.String()
}

func resolveImportTargetURI(baseURI, relPath string) string {
	basePath := fileURIToPath(baseURI)
	if basePath == "" || relPath == "" {
		return ""
	}
	joined := filepath.Join(filepath.Dir(basePath), filepath.FromSlash(relPath))
	return pathToFileURI(joined)
}

func (s *LSPServer) findOpenURIByPath(targetPath string) (string, bool) {
	s.docsMu.RLock()
	defer s.docsMu.RUnlock()
	for u := range s.docs {
		if pathsEqual(fileURIToPath(u), targetPath) {
			return u, true
		}
	}
	for u := range s.lastGood {
		if pathsEqual(fileURIToPath(u), targetPath) {
			return u, true
		}
	}
	return "", false
}

func (s *LSPServer) resolveTargetURI(baseURI, relPath string) string {
	resolved := resolveImportTargetURI(baseURI, relPath)
	if resolved == "" {
		return ""
	}
	targetPath := fileURIToPath(resolved)
	if openURI, ok := s.findOpenURIByPath(targetPath); ok {
		return openURI
	}
	return resolved
}

// utf16PosToLineCol converts a 0-based LSP Position (UTF-16 character offset) to 1-based Ease line and rune column.
func utf16PosToLineCol(lines []string, pos LSPPosition) (int, int) {
	if pos.Line < 0 || pos.Line >= len(lines) {
		return pos.Line + 1, pos.Character + 1
	}
	lineText := lines[pos.Line]
	u16 := 0
	runeCol := 1
	for _, r := range lineText {
		if u16 >= pos.Character {
			break
		}
		step := 1
		if r > 0xFFFF {
			step = 2
		}
		u16 += step
		runeCol++
	}
	return pos.Line + 1, runeCol
}

type lspCursorToken struct {
	word       string
	line       int
	col        int
	runeLen    int
	isString   bool
	qualLeft   string
	qualSep    string // ":" or "."
	isQualBase bool
	qualRight  string
}

func isIdentRune(r rune) bool {
	if r == '_' {
		return true
	}
	if r >= '0' && r <= '9' {
		return true
	}
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
		return true
	}
	return r > 127 && r != ' ' && r != '\t' && r != '\r' && r != '\n' && r != '.' && r != ':' && r != ',' && r != '(' && r != ')' && r != '"' && r != '\''
}

func lspTokenAt(src string, lines []string, pos LSPPosition) (lspCursorToken, bool) {
	line1, col1 := utf16PosToLineCol(lines, pos)
	tokens, err := NewLexer(src).Tokenize()
	if err == nil {
		var lineToks []Token
		for _, t := range tokens {
			if t.Line == line1 && t.Type != TOK_NEWLINE && t.Type != TOK_EOF {
				lineToks = append(lineToks, t)
			}
		}
		for i, t := range lineToks {
			rLen := len([]rune(t.Lexeme))
			if t.Type == TOK_STRING {
				rLen += 2 // include surrounding quotes
			}
			if rLen < 1 {
				rLen = 1
			}
			if col1 >= t.Col && col1 < t.Col+rLen {
				ct := lspCursorToken{
					word:     t.Lexeme,
					line:     t.Line,
					col:      t.Col,
					runeLen:  rLen,
					isString: t.Type == TOK_STRING,
				}
				if i >= 2 && (lineToks[i-1].Type == TOK_COLON || lineToks[i-1].Type == TOK_DOT) {
					ct.qualLeft = lineToks[i-2].Lexeme
					ct.qualSep = lineToks[i-1].Lexeme
				}
				if i+2 < len(lineToks) && (lineToks[i+1].Type == TOK_COLON || lineToks[i+1].Type == TOK_DOT) {
					ct.isQualBase = true
					ct.qualSep = lineToks[i+1].Lexeme
					ct.qualRight = lineToks[i+2].Lexeme
				}
				return ct, true
			}
		}
	}

	// Fallback: simple rune scan on the current line when lexer fails on incomplete input.
	if pos.Line < 0 || pos.Line >= len(lines) {
		return lspCursorToken{}, false
	}
	runes := []rune(lines[pos.Line])
	idx := col1 - 1
	if idx < 0 || idx >= len(runes) {
		if idx == len(runes) && idx > 0 {
			idx--
		} else {
			return lspCursorToken{}, false
		}
	}
	if !isIdentRune(runes[idx]) {
		return lspCursorToken{}, false
	}
	start := idx
	for start > 0 && isIdentRune(runes[start-1]) {
		start--
	}
	end := idx + 1
	for end < len(runes) && isIdentRune(runes[end]) {
		end++
	}
	word := string(runes[start:end])
	ct := lspCursorToken{
		word:    word,
		line:    line1,
		col:     start + 1,
		runeLen: end - start,
	}
	if start >= 2 && (runes[start-1] == ':' || runes[start-1] == '.') && isIdentRune(runes[start-2]) {
		qEnd := start - 1
		qStart := qEnd - 1
		for qStart > 0 && isIdentRune(runes[qStart-1]) {
			qStart--
		}
		ct.qualLeft = string(runes[qStart:qEnd])
		ct.qualSep = string(runes[start-1])
	}
	return ct, true
}

func locationFromPos(uri string, lines []string, line, col, runeLen int) LSPLocation {
	if runeLen < 1 {
		runeLen = 1
	}
	u16Len := runeLen
	if l := line - 1; l >= 0 && l < len(lines) {
		runes := []rune(lines[l])
		start := col - 1
		if start >= 0 && start < len(runes) {
			end := min(len(runes), start+runeLen)
			u16Len = utf16Len(string(runes[start:end]))
		}
	}
	return LSPLocation{
		URI:   uri,
		Range: symbolRange(lines, line, col, u16Len),
	}
}

type lspScopeInfo struct {
	enclosingFn *FuncDecl
	params      map[string]posInfo
	vars        map[string]posInfo
	varTypes    map[string]string
	defs        map[string]*DefDecl
	unions      map[string]*UnionDecl
	funcs       map[string]*FuncDecl
	imports     map[string]*ImportDecl
}

func collectLSPScope(prog *Program, cursorLine int) *lspScopeInfo {
	sc := &lspScopeInfo{
		params:   make(map[string]posInfo),
		vars:     make(map[string]posInfo),
		varTypes: make(map[string]string),
		defs:     make(map[string]*DefDecl),
		unions:   make(map[string]*UnionDecl),
		funcs:    make(map[string]*FuncDecl),
		imports:  make(map[string]*ImportDecl),
	}
	if prog == nil {
		return sc
	}
	for _, imp := range prog.Imports {
		alias := imp.Alias
		if alias == "" {
			alias = imp.Name
		}
		sc.imports[alias] = imp
	}
	for _, d := range prog.Defs {
		if !d.Imported {
			sc.defs[d.Name] = d
		}
	}
	for _, u := range prog.Unions {
		if !u.Imported {
			sc.unions[u.Name] = u
		}
	}
	for _, fn := range prog.Functions {
		sc.funcs[fn.Name] = fn
	}

	// Collect top-level constants and variables.
	collectVarsFromStmts(prog.Body, sc, cursorLine, false)

	// Find enclosing function (highest fn.Line <= cursorLine whose body spans cursorLine).
	var bestFn *FuncDecl
	for _, fn := range prog.Functions {
		if fn.Line <= cursorLine && (bestFn == nil || fn.Line > bestFn.Line) {
			if cursorLine <= funcEndLine(fn) {
				bestFn = fn
			}
		}
	}
	if bestFn != nil {
		sc.enclosingFn = bestFn
		for _, p := range bestFn.Params {
			sc.params[p] = posInfo{Line: bestFn.Line, Col: bestFn.Col}
		}
		collectVarsFromStmts(bestFn.Body, sc, cursorLine, true)
	}
	for _, chk := range prog.Checks {
		if chk.Line <= cursorLine && cursorLine <= stmtsMaxLine(chk.Body, chk.Line) {
			collectVarsFromStmts(chk.Body, sc, cursorLine, true)
		}
	}
	return sc
}

func funcEndLine(fn *FuncDecl) int {
	return stmtsMaxLine(fn.Body, fn.Line) + 1
}

func stmtsMaxLine(stmts []Stmt, base int) int {
	m := base
	for _, st := range stmts {
		l, _ := st.Pos()
		if l > m {
			m = l
		}
		switch s := st.(type) {
		case *LoopStmt:
			if sub := stmtsMaxLine(s.Body, m); sub > m {
				m = sub
			}
		case *TakeStmt:
			if sub := stmtsMaxLine(s.Body, m); sub > m {
				m = sub
			}
		case *CheckGuardStmt:
			if sub := stmtsMaxLine(s.Body, m); sub > m {
				m = sub
			}
			if sub := stmtsMaxLine(s.ElseBody, m); sub > m {
				m = sub
			}
		case *MatchStmt:
			for _, arm := range s.Arms {
				if sub := stmtsMaxLine(arm.Body, arm.Line); sub > m {
					m = sub
				}
			}
			if sub := stmtsMaxLine(s.ElseBody, m); sub > m {
				m = sub
			}
		}
	}
	return m
}

func collectVarsFromStmts(stmts []Stmt, sc *lspScopeInfo, cursorLine int, allowShadow bool) {
	for _, st := range stmts {
		switch s := st.(type) {
		case *ConstDeclStmt:
			if allowShadow || s.Line <= cursorLine {
				sc.vars[s.Name] = posInfo{Line: s.Line, Col: s.Col}
				sc.varTypes[s.Name] = lspInferExprType(s.Value, sc)
			}
		case *AssignStmt:
			if _, exists := sc.vars[s.Name]; !exists || allowShadow {
				if s.Line <= cursorLine {
					if !exists {
						sc.vars[s.Name] = posInfo{Line: s.Line, Col: s.Col}
					}
					if t := lspInferExprType(s.Value, sc); t != "" {
						sc.varTypes[s.Name] = t
					}
				}
			}
		case *LoopStmt:
			collectVarsFromStmts(s.Body, sc, cursorLine, allowShadow)
		case *TakeStmt:
			collectVarsFromStmts(s.Body, sc, cursorLine, allowShadow)
		case *CheckGuardStmt:
			collectVarsFromStmts(s.Body, sc, cursorLine, allowShadow)
			collectVarsFromStmts(s.ElseBody, sc, cursorLine, allowShadow)
		case *MatchStmt:
			for _, arm := range s.Arms {
				collectVarsFromStmts(arm.Body, sc, cursorLine, allowShadow)
			}
			collectVarsFromStmts(s.ElseBody, sc, cursorLine, allowShadow)
		}
	}
}

func lspInferExprType(e Expr, sc *lspScopeInfo) string {
	if e == nil {
		return ""
	}
	switch ex := e.(type) {
	case *NumberExpr:
		return "number"
	case *StringExpr, *InterpStringExpr:
		return "text"
	case *IdentExpr:
		if sc.defs[ex.Name] != nil || sc.unions[ex.Name] != nil {
			return ex.Name
		}
		if t, ok := sc.varTypes[ex.Name]; ok {
			return t
		}
	case *WithExpr:
		return lspInferExprType(ex.Base, sc)
	case *UpdateExpr:
		return lspInferExprType(ex.Target, sc)
	case *SortByExpr:
		return lspInferExprType(ex.Target, sc)
	case *CastExpr:
		if ex.TypeName == "json" {
			return "json"
		}
		if ex.TypeName == "csv" {
			return "list<map>"
		}
		if ex.TypeName == "bytes" {
			return "list<number>"
		}
		return ex.TypeName
	case *TypedListExpr:
		return "list<" + ex.ElemType + ">"
	case *InboxExpr:
		return "inbox<" + ex.ElemType + ">"
	case *FieldExpr:
		if id, ok := ex.Target.(*IdentExpr); ok {
			if imp, isImp := sc.imports[id.Name]; isImp && imp.ModuleProg != nil {
				for _, d := range imp.ModuleProg.Defs {
					if d.Name == ex.Field {
						return id.Name + "." + ex.Field
					}
				}
			}
		}
	case *CallExpr:
		if ex.Module == "" {
			switch ex.Callee {
			case "ask", "trim", "upper", "lower", "replace", "join", "type":
				return "text"
			case "count", "sum", "avg", "abs", "floor", "round", "min", "max":
				return "number"
			case "range":
				return "list<number>"
			case "split", "fields":
				return "list<text>"
			}
		}
	}
	return ""
}

func (s *LSPServer) lookupDefDecl(sc *lspScopeInfo, typeName string) (*DefDecl, string, []string) {
	if dot := strings.IndexByte(typeName, '.'); dot > 0 {
		modAlias := typeName[:dot]
		defName := typeName[dot+1:]
		if imp, ok := sc.imports[modAlias]; ok && imp.IsFileImport {
			return s.lookupImportedDef(imp, defName)
		}
		if imp, ok := sc.imports[modAlias]; ok && imp.ModuleProg != nil {
			for _, d := range imp.ModuleProg.Defs {
				if d.Name == defName {
					return d, "", splitLinesNormalized(imp.ModuleSource)
				}
			}
		}
		return nil, "", nil
	}
	if d := sc.defs[typeName]; d != nil {
		return d, "", nil
	}
	return nil, "", nil
}

func (s *LSPServer) lookupImportedDef(imp *ImportDecl, defName string) (*DefDecl, string, []string) {
	if imp.ModuleProg != nil {
		for _, d := range imp.ModuleProg.Defs {
			if d.Name == defName {
				return d, imp.Path, splitLinesNormalized(imp.ModuleSource)
			}
		}
	}
	return nil, "", nil
}

// DefinitionForURI resolves Go-to-Definition locations for a single source string (exported for tests).
func DefinitionForURI(uri, source string, pos LSPPosition) []LSPLocation {
	s := NewLSPServer(nil, nil)
	s.setDoc(uri, source)
	return s.definitionForURI(uri, pos)
}

func (s *LSPServer) definitionForURI(uri string, pos LSPPosition) []LSPLocation {
	prog, src, lines, ok := s.resolveProgramWithFallback(uri)
	if !ok || prog == nil {
		return []LSPLocation{}
	}
	tok, found := lspTokenAt(src, lines, pos)
	if !found {
		return []LSPLocation{}
	}
	sc := collectLSPScope(prog, tok.line)

	// 1. Clicking on an import path string `"./other.ease"`: jump to top of target file.
	if tok.isString {
		for _, imp := range prog.Imports {
			if imp.IsFileImport && imp.Line == tok.line && imp.Path == tok.word {
				if targetURI := s.resolveTargetURI(uri, imp.Path); targetURI != "" {
					return []LSPLocation{{
						URI:   targetURI,
						Range: LSPRange{Start: LSPPosition{0, 0}, End: LSPPosition{0, 0}},
					}}
				}
			}
		}
		return []LSPLocation{}
	}

	// 2. Qualified symbol: `mod:fn` or `mod.Rec` or `rec.field`.
	if tok.qualLeft != "" {
		if imp, isImp := sc.imports[tok.qualLeft]; isImp {
			if imp.IsFileImport {
				targetURI := s.resolveTargetURI(uri, imp.Path)
				tProg, _, tLines, tOk := s.resolveProgramWithFallback(targetURI)
				if !tOk && imp.ModuleProg != nil {
					tProg = imp.ModuleProg
					tLines = splitLinesNormalized(imp.ModuleSource)
				}
				if tProg != nil {
					if loc, hit := findDeclLocation(targetURI, tProg, tLines, tok.word); hit {
						return []LSPLocation{loc}
					}
				}
			}
			return []LSPLocation{}
		}
		if tok.qualSep == "." {
			if recType, ok := sc.varTypes[tok.qualLeft]; ok && recType != "" {
				if loc, hit := s.findFieldLocation(uri, lines, sc, recType, tok.word); hit {
					return []LSPLocation{loc}
				}
			}
			// Fallback: search matching field name in local defs if variable type was dynamic.
			for _, d := range prog.Defs {
				if d.Imported {
					continue
				}
				for _, f := range d.Fields {
					if f.Name == tok.word {
						return []LSPLocation{locationFromPos(uri, lines, f.Line, f.Col, len([]rune(f.Name)))}
					}
				}
			}
		}
	}

	// 3. Field inside `with` expression (`rec with field = ...`).
	if loc, hit := s.findWithFieldLocation(uri, lines, prog, sc, tok); hit {
		return []LSPLocation{loc}
	}

	// 4. Cursor on module alias `mod` in `mod:fn` or `mod.Rec`.
	if imp, isImp := sc.imports[tok.word]; isImp {
		return []LSPLocation{locationFromPos(uri, lines, imp.Line, imp.Col, len([]rune(tok.word)))}
	}

	// 5. Enclosing function parameter (shadows global variable of same name).
	if pPos, isParam := sc.params[tok.word]; isParam {
		return []LSPLocation{locationFromPos(uri, lines, pPos.Line, pPos.Col, len([]rune(tok.word)))}
	}

	// 6. Local/global variable or constant.
	if vPos, isVar := sc.vars[tok.word]; isVar {
		return []LSPLocation{locationFromPos(uri, lines, vPos.Line, vPos.Col, len([]rune(tok.word)))}
	}

	// 7. Top-level function, def record, or union.
	if loc, hit := findDeclLocation(uri, prog, lines, tok.word); hit {
		return []LSPLocation{loc}
	}
	return []LSPLocation{}
}

func findDeclLocation(uri string, prog *Program, lines []string, name string) (LSPLocation, bool) {
	for _, fn := range prog.Functions {
		if fn.Name == name {
			return locationFromPos(uri, lines, fn.Line, fn.Col, len([]rune(fn.Name))+3), true
		}
	}
	for _, d := range prog.Defs {
		if !d.Imported && d.Name == name {
			return locationFromPos(uri, lines, d.Line, d.Col, len([]rune(d.Name))+4), true
		}
	}
	for _, u := range prog.Unions {
		if !u.Imported && u.Name == name {
			return locationFromPos(uri, lines, u.Line, u.Col, len([]rune(u.Name))+4), true
		}
	}
	for _, st := range prog.Body {
		if cs, ok := st.(*ConstDeclStmt); ok && cs.Name == name {
			return locationFromPos(uri, lines, cs.Line, cs.Col, len([]rune(cs.Name))+4), true
		}
	}
	return LSPLocation{}, false
}

func (s *LSPServer) findFieldLocation(uri string, lines []string, sc *lspScopeInfo, recType, fieldName string) (LSPLocation, bool) {
	if dot := strings.IndexByte(recType, '.'); dot > 0 {
		modAlias := recType[:dot]
		defName := recType[dot+1:]
		if imp, ok := sc.imports[modAlias]; ok && imp.IsFileImport {
			targetURI := s.resolveTargetURI(uri, imp.Path)
			tProg, _, tLines, tOk := s.resolveProgramWithFallback(targetURI)
			if !tOk && imp.ModuleProg != nil {
				tProg = imp.ModuleProg
				tLines = splitLinesNormalized(imp.ModuleSource)
			}
			if tProg != nil {
				for _, d := range tProg.Defs {
					if d.Name == defName {
						for _, f := range d.Fields {
							if f.Name == fieldName {
								return locationFromPos(targetURI, tLines, f.Line, f.Col, len([]rune(f.Name))), true
							}
						}
					}
				}
			}
		}
		return LSPLocation{}, false
	}
	if d := sc.defs[recType]; d != nil {
		for _, f := range d.Fields {
			if f.Name == fieldName {
				return locationFromPos(uri, lines, f.Line, f.Col, len([]rune(f.Name))), true
			}
		}
	}
	return LSPLocation{}, false
}

func (s *LSPServer) findWithFieldLocation(uri string, lines []string, prog *Program, sc *lspScopeInfo, tok lspCursorToken) (LSPLocation, bool) {
	var foundBase Expr
	walkAllExprs(prog, func(e Expr) {
		if foundBase != nil {
			return
		}
		if we, ok := e.(*WithExpr); ok {
			for _, u := range we.Updates {
				if u.Name == tok.word && u.Line == tok.line && u.Col == tok.col {
					foundBase = we.Base
					return
				}
			}
		}
	})
	if foundBase == nil {
		return LSPLocation{}, false
	}
	recType := lspInferExprType(foundBase, sc)
	if recType == "" {
		return LSPLocation{}, false
	}
	return s.findFieldLocation(uri, lines, sc, recType, tok.word)
}

// HoverForURI returns Markdown hover info for a single source string (exported for tests).
func HoverForURI(uri, source string, pos LSPPosition) *LSPHover {
	s := NewLSPServer(nil, nil)
	s.setDoc(uri, source)
	return s.hoverForURI(uri, pos)
}

func (s *LSPServer) hoverForURI(uri string, pos LSPPosition) *LSPHover {
	prog, src, lines, ok := s.resolveProgramWithFallback(uri)
	if !ok || prog == nil {
		return nil
	}
	tok, found := lspTokenAt(src, lines, pos)
	if !found || tok.isString {
		return nil
	}
	sc := collectLSPScope(prog, tok.line)
	tokRange := symbolRange(lines, tok.line, tok.col, utf16Len(tok.word))

	makeHover := func(md string) *LSPHover {
		return &LSPHover{
			Contents: LSPMarkupContent{Kind: "markdown", Value: md},
			Range:    &tokRange,
		}
	}

	// 1. Qualified `mod:fn` or `mod.Rec`.
	if tok.qualLeft != "" {
		if imp, isImp := sc.imports[tok.qualLeft]; isImp {
			if imp.ModuleProg != nil {
				if md := hoverFromProgram(imp.ModuleProg, tok.word, tok.qualLeft); md != "" {
					return makeHover(md)
				}
			}
		}
	}

	// 2. Stdlib module alias.
	if imp, isImp := sc.imports[tok.word]; isImp {
		if !imp.IsFileImport {
			if doc, ok := stdModuleDocs[imp.Name]; ok {
				return makeHover(fmt.Sprintf("**module `%s`**\n\n%s", imp.Name, doc))
			}
		} else {
			return makeHover(fmt.Sprintf("**module `%s`** (`%s`)", tok.word, imp.Path))
		}
	}

	// 3. Built-in verb.
	if doc, isBuiltin := builtinDocs[tok.word]; isBuiltin {
		return makeHover(fmt.Sprintf("```ease\n%s\n```\n%s", doc.sig, doc.desc))
	}

	// 4. Local/imported declaration (`to`, `def`, `union`).
	if md := hoverFromProgram(prog, tok.word, ""); md != "" {
		return makeHover(md)
	}

	// 5. Function parameter or variable.
	if _, isParam := sc.params[tok.word]; isParam {
		return makeHover(fmt.Sprintf("**parameter `%s`**", tok.word))
	}
	if _, isVar := sc.vars[tok.word]; isVar {
		t := sc.varTypes[tok.word]
		if t != "" {
			return makeHover(fmt.Sprintf("**variable `%s`**: `%s`", tok.word, t))
		}
		return makeHover(fmt.Sprintf("**variable `%s`**", tok.word))
	}

	return nil
}

func hoverFromProgram(prog *Program, name, modPrefix string) string {
	qual := name
	if modPrefix != "" {
		qual = modPrefix + ":" + name
	}
	for _, fn := range prog.Functions {
		if fn.Name == name {
			sig := "to " + qual
			if len(fn.Params) > 0 {
				sig += " " + strings.Join(fn.Params, " ")
			}
			kind := "void procedure"
			if stmtsHaveReturn(fn.Body) {
				kind = "returns value (`->`)"
			}
			return fmt.Sprintf("```ease\n%s\n```\nFunction (%s)", sig, kind)
		}
	}
	for _, d := range prog.Defs {
		if d.Imported || d.Name != name {
			continue
		}
		var b strings.Builder
		b.WriteString("```ease\ndef ")
		if modPrefix != "" {
			b.WriteString(modPrefix + "." + d.Name)
		} else {
			b.WriteString(d.Name)
		}
		b.WriteString("\n")
		for _, f := range d.Fields {
			b.WriteString("    " + f.Name + "\n")
		}
		b.WriteString("end\n```")
		return b.String()
	}
	for _, u := range prog.Unions {
		if u.Imported || u.Name != name {
			continue
		}
		return fmt.Sprintf("```ease\ndef %s = ... (%d variants)\n```", u.Name, len(u.Variants))
	}
	for _, st := range prog.Body {
		if cs, ok := st.(*ConstDeclStmt); ok && cs.Name == name {
			return fmt.Sprintf("**constant `%s`**", cs.Name)
		}
	}
	return ""
}

// ReferencesForURI finds all references in a single source string (exported for tests).
func ReferencesForURI(uri, source string, pos LSPPosition, includeDecl bool) []LSPLocation {
	s := NewLSPServer(nil, nil)
	s.setDoc(uri, source)
	return s.referencesForURI(uri, pos, includeDecl)
}

func (s *LSPServer) referencesForURI(uri string, pos LSPPosition, includeDecl bool) []LSPLocation {
	prog, src, lines, ok := s.resolveProgramWithFallback(uri)
	if !ok || prog == nil {
		return []LSPLocation{}
	}
	tok, found := lspTokenAt(src, lines, pos)
	if !found || tok.isString {
		return []LSPLocation{}
	}
	sc := collectLSPScope(prog, tok.line)
	target := tok.word

	var locs []LSPLocation
	// Cross-file reference case 1: `mod:sym` or `mod.sym` -> find references in target file and all open importers.
	if tok.qualLeft != "" {
		if imp, isImp := sc.imports[tok.qualLeft]; isImp && imp.IsFileImport {
			targetURI := s.resolveTargetURI(uri, imp.Path)
			targetPath := fileURIToPath(targetURI)
			if tProg, _, tLines, tOk := s.resolveProgramWithFallback(targetURI); tOk {
				locs = append(locs, collectLocalSymbolRefs(targetURI, tProg, tLines, target, nil, includeDecl)...)
			}
			locs = append(locs, s.collectImporterRefs(targetPath, target)...)
			return locs
		}
	}

	// Scope-aware local references in current file.
	var fnScope *FuncDecl
	if _, isParam := sc.params[target]; isParam {
		fnScope = sc.enclosingFn
	} else if vPos, isVar := sc.vars[target]; isVar && sc.enclosingFn != nil && vPos.Line >= sc.enclosingFn.Line {
		fnScope = sc.enclosingFn
	}

	locs = append(locs, collectLocalSymbolRefs(uri, prog, lines, target, fnScope, includeDecl)...)

	// Cross-file reference case 2: cursor on top-level `to fn` or `def Rec` -> also check open importers.
	if fnScope == nil {
		if _, isDecl := findDeclLocation(uri, prog, lines, target); isDecl {
			if curPath := fileURIToPath(uri); curPath != "" {
				locs = append(locs, s.collectImporterRefs(curPath, target)...)
			}
		}
	}
	return locs
}

func (s *LSPServer) collectImporterRefs(targetPath, symName string) []LSPLocation {
	if targetPath == "" {
		return nil
	}
	s.docsMu.RLock()
	openURIs := make([]string, 0, len(s.docs))
	for u := range s.docs {
		openURIs = append(openURIs, u)
	}
	s.docsMu.RUnlock()

	var locs []LSPLocation
	for _, openURI := range openURIs {
		if pathsEqual(fileURIToPath(openURI), targetPath) {
			continue
		}
		oProg, _, oLines, oOk := s.resolveProgramWithFallback(openURI)
		if !oOk || oProg == nil {
			continue
		}
		var matchingAliases []string
		for _, imp := range oProg.Imports {
			if !imp.IsFileImport {
				continue
			}
			if pathsEqual(fileURIToPath(resolveImportTargetURI(openURI, imp.Path)), targetPath) {
				matchingAliases = append(matchingAliases, imp.Alias)
			}
		}
		if len(matchingAliases) == 0 {
			continue
		}
		aliasSet := make(map[string]bool, len(matchingAliases))
		for _, a := range matchingAliases {
			aliasSet[a] = true
		}
		walkAllExprs(oProg, func(e Expr) {
			switch ex := e.(type) {
			case *CallExpr:
				if aliasSet[ex.Module] && ex.Callee == symName {
					locs = append(locs, locationFromPos(openURI, oLines, ex.Line, ex.Col, len([]rune(ex.Module))+1+len([]rune(symName))))
				}
			case *FieldExpr:
				if id, ok := ex.Target.(*IdentExpr); ok && aliasSet[id.Name] && ex.Field == symName {
					locs = append(locs, locationFromPos(openURI, oLines, ex.Line, ex.Col, len([]rune(id.Name))+1+len([]rune(symName))))
				}
			}
		})
	}
	return locs
}

func collectLocalSymbolRefs(uri string, prog *Program, lines []string, name string, onlyInFn *FuncDecl, includeDecl bool) []LSPLocation {
	var locs []LSPLocation
	seen := make(map[string]bool)
	add := func(line, col, rLen int) {
		if line <= 0 || col <= 0 {
			return
		}
		key := fmt.Sprintf("%d:%d", line, col)
		if seen[key] {
			return
		}
		seen[key] = true
		locs = append(locs, locationFromPos(uri, lines, line, col, rLen))
	}
	nameLen := len([]rune(name))

	if includeDecl && onlyInFn == nil {
		if loc, ok := findDeclLocation(uri, prog, lines, name); ok {
			add(loc.Range.Start.Line+1, loc.Range.Start.Character+1, nameLen)
		}
	}
	if includeDecl && onlyInFn != nil {
		add(onlyInFn.Line, onlyInFn.Col, len([]rune(onlyInFn.Name))+3)
	}

	visitExpr := func(e Expr) {
		switch ex := e.(type) {
		case *IdentExpr:
			if ex.Name == name {
				add(ex.Line, ex.Col, nameLen)
			}
		case *CallExpr:
			if ex.Module == "" && ex.Callee == name {
				add(ex.Line, ex.Col, nameLen)
			}
		case *FieldExpr:
			if ex.Field == name {
				add(ex.Line, ex.Col, nameLen)
			}
		case *WithExpr:
			for _, u := range ex.Updates {
				if u.Name == name {
					add(u.Line, u.Col, nameLen)
				}
			}
		case *UpdateExpr:
			for _, u := range ex.Updates {
				if u.Name == name {
					add(u.Line, u.Col, nameLen)
				}
			}
		case *CastExpr:
			if ex.TypeName == name && ex.TypeLine > 0 {
				add(ex.TypeLine, ex.TypeCol, nameLen)
			}
		case *TypeCheckExpr:
			if ex.TypeName == name {
				add(ex.Line, ex.Col, nameLen)
			}
		case *TypedListExpr:
			if ex.ElemType == name && ex.TypeLine > 0 {
				add(ex.TypeLine, ex.TypeCol, nameLen)
			}
		case *InboxExpr:
			if ex.ElemType == name && ex.TypeLine > 0 {
				add(ex.TypeLine, ex.TypeCol, nameLen)
			}
		}
	}

	visitStmt := func(s Stmt) {
		switch st := s.(type) {
		case *AssignStmt:
			if st.Name == name && (includeDecl || onlyInFn == nil) {
				add(st.Line, st.Col, nameLen)
			}
		case *ConstDeclStmt:
			if st.Name == name && includeDecl {
				add(st.Line, st.Col, nameLen)
			}
		}
	}

	if onlyInFn != nil {
		walkStmtsAndExprs(onlyInFn.Body, visitStmt, visitExpr)
		return locs
	}
	for _, d := range prog.Defs {
		if d.Imported {
			continue
		}
		for _, f := range d.Fields {
			if f.Name == name && includeDecl {
				add(f.Line, f.Col, nameLen)
			}
			walkExprTree(f.Default, visitExpr)
			for _, opt := range f.Allowed {
				walkExprTree(opt, visitExpr)
			}
		}
	}
	for _, u := range prog.Unions {
		if u.Imported {
			continue
		}
		for _, v := range u.Variants {
			walkExprTree(v, visitExpr)
		}
	}
	walkStmtsAndExprs(prog.Body, visitStmt, visitExpr)
	for _, fn := range prog.Functions {
		walkStmtsAndExprs(fn.Body, visitStmt, visitExpr)
	}
	for _, chk := range prog.Checks {
		walkStmtsAndExprs(chk.Body, visitStmt, visitExpr)
	}
	return locs
}

func walkAllExprs(prog *Program, fn func(Expr)) {
	for _, d := range prog.Defs {
		for _, f := range d.Fields {
			walkExprTree(f.Default, fn)
			for _, opt := range f.Allowed {
				walkExprTree(opt, fn)
			}
		}
	}
	for _, u := range prog.Unions {
		for _, v := range u.Variants {
			walkExprTree(v, fn)
		}
	}
	walkStmtsAndExprs(prog.Body, nil, fn)
	for _, f := range prog.Functions {
		walkStmtsAndExprs(f.Body, nil, fn)
	}
	for _, c := range prog.Checks {
		walkStmtsAndExprs(c.Body, nil, fn)
	}
}

func walkStmtsAndExprs(stmts []Stmt, onStmt func(Stmt), onExpr func(Expr)) {
	for _, st := range stmts {
		if onStmt != nil {
			onStmt(st)
		}
		switch s := st.(type) {
		case *ConstDeclStmt:
			walkExprTree(s.Value, onExpr)
		case *AssignStmt:
			walkExprTree(s.Value, onExpr)
		case *SayStmt:
			walkExprTree(s.Value, onExpr)
		case *ExprStmt:
			walkExprTree(s.Expression, onExpr)
		case *ReturnStmt:
			walkExprTree(s.Value, onExpr)
		case *SpawnStmt:
			if s.Call != nil {
				walkExprTree(s.Call, onExpr)
			}
		case *LoopStmt:
			walkExprTree(s.Iterable, onExpr)
			walkStmtsAndExprs(s.Body, onStmt, onExpr)
		case *TakeStmt:
			walkExprTree(s.Inbox, onExpr)
			walkStmtsAndExprs(s.Body, onStmt, onExpr)
		case *CheckGuardStmt:
			walkExprTree(s.Cond, onExpr)
			for _, r := range s.Rules {
				walkExprTree(r.Cond, onExpr)
				if r.Action != nil {
					walkStmtsAndExprs([]Stmt{r.Action}, onStmt, onExpr)
				}
			}
			walkStmtsAndExprs(s.Body, onStmt, onExpr)
			walkStmtsAndExprs(s.ElseBody, onStmt, onExpr)
		case *MatchStmt:
			walkExprTree(s.Target, onExpr)
			for _, arm := range s.Arms {
				if arm.Action != nil {
					walkStmtsAndExprs([]Stmt{arm.Action}, onStmt, onExpr)
				}
				walkStmtsAndExprs(arm.Body, onStmt, onExpr)
			}
			walkStmtsAndExprs(s.ElseBody, onStmt, onExpr)
		case *CheckIsStmt:
			walkExprTree(s.Actual, onExpr)
			walkExprTree(s.Expected, onExpr)
		case *CheckIsNotStmt:
			walkExprTree(s.Actual, onExpr)
			walkExprTree(s.Unexpected, onExpr)
		case *CheckCondStmt:
			walkExprTree(s.Condition, onExpr)
		case *CheckIsErrorStmt:
			walkExprTree(s.Expression, onExpr)
		case *CheckErrorBlockStmt:
			walkExprTree(s.Expression, onExpr)
			walkStmtsAndExprs(s.Body, onStmt, onExpr)
		}
	}
}

func walkExprTree(e Expr, onExpr func(Expr)) {
	if e == nil || onExpr == nil {
		return
	}
	onExpr(e)
	switch ex := e.(type) {
	case *UnaryExpr:
		walkExprTree(ex.Right, onExpr)
	case *BinaryExpr:
		walkExprTree(ex.Left, onExpr)
		walkExprTree(ex.Right, onExpr)
	case *CallExpr:
		for _, a := range ex.Args {
			walkExprTree(a, onExpr)
		}
	case *FieldExpr:
		walkExprTree(ex.Target, onExpr)
	case *IndexExpr:
		walkExprTree(ex.Target, onExpr)
		walkExprTree(ex.Index, onExpr)
	case *ListExpr:
		for _, el := range ex.Elements {
			walkExprTree(el, onExpr)
		}
	case *TypedListExpr:
		for _, row := range ex.Rows {
			for _, u := range row {
				walkExprTree(u.Value, onExpr)
			}
		}
	case *WithExpr:
		walkExprTree(ex.Base, onExpr)
		for _, u := range ex.Updates {
			walkExprTree(u.Value, onExpr)
		}
		for _, iu := range ex.IndexUpdates {
			walkExprTree(iu.Index, onExpr)
			walkExprTree(iu.Value, onExpr)
		}
	case *WhereExpr:
		walkExprTree(ex.Target, onExpr)
		for _, c := range ex.Conditions {
			walkExprTree(c, onExpr)
		}
	case *UpdateExpr:
		walkExprTree(ex.Target, onExpr)
		walkExprTree(ex.Where, onExpr)
		for _, u := range ex.Updates {
			walkExprTree(u.Value, onExpr)
		}
	case *SortByExpr:
		walkExprTree(ex.Target, onExpr)
		for _, k := range ex.Keys {
			walkExprTree(k, onExpr)
		}
	case *CastExpr:
		walkExprTree(ex.Target, onExpr)
		walkExprTree(ex.Fallback, onExpr)
	case *TypeCheckExpr:
		walkExprTree(ex.Target, onExpr)
	case *InterpStringExpr:
		for _, p := range ex.Parts {
			if p.IsExpr {
				walkExprTree(p.Expr, onExpr)
			}
		}
	case *MatchExpr:
		walkExprTree(ex.Target, onExpr)
		for _, arm := range ex.Arms {
			walkExprTree(arm.Value, onExpr)
		}
		walkStmtsAndExprs(ex.ElseBody, nil, onExpr)
	case *SwarmLoopExpr:
		walkExprTree(ex.Iterable, onExpr)
		walkStmtsAndExprs(ex.Body, nil, onExpr)
	}
}
