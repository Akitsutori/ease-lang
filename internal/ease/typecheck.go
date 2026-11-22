package ease

import (
	"fmt"
)

type typeDefInfo struct {
	line     int
	imported bool
	isRecord bool
}

type modTypeInfo struct {
	alias    string
	origName string
	defs     map[string]*DefDecl
	unions   map[string]*UnionDecl
	funcs    map[string]*FuncDecl
	consts   map[string]Expr
}

type fieldSchema struct {
	anyText   bool
	anyNumber bool
	strLits   map[string]bool
	numLits   map[float64]bool
	records   map[string]bool
	isList    bool
	listElem  string
	isDynamic bool
}

type varTypeState struct {
	unionName           string
	exactRecord         string
	exactNonRecord      bool
	exactBaseType       string
	listElemType        string
	inboxElemType       string
	excludedRecords     map[string]bool
	inlineUnionLabel    string
	inlineUnionVariants []resolvedUnionVariant
}

func (s varTypeState) clone() varTypeState {
	out := s
	if len(s.excludedRecords) > 0 {
		out.excludedRecords = make(map[string]bool, len(s.excludedRecords))
		for k, v := range s.excludedRecords {
			out.excludedRecords[k] = v
		}
	}
	return out
}

type typeScope struct {
	parent       *typeScope
	vars         map[string]varTypeState
	isWorkerRoot bool
}

func newTypeScope(parent *typeScope) *typeScope {
	return &typeScope{
		parent: parent,
		vars:   make(map[string]varTypeState),
	}
}

// validateProgramTypes verifies type targets in `as`, union rules (4 building blocks
// and 3 guardrails), union type narrowing on field accesses, and check exhaustiveness.
func validateProgramTypes(prog *Program, source string) error {
	return validateProgramTypesWithMode(prog, source, false)
}

func validateProgramTypesWithMode(prog *Program, source string, allowInternalBuiltins bool) error {
	known := make(map[string]typeDefInfo, len(prog.Defs)+len(prog.Unions))
	defs := make(map[string]*DefDecl, len(prog.Defs))
	unions := make(map[string]*UnionDecl, len(prog.Unions))

	for _, d := range prog.Defs {
		known[d.Name] = typeDefInfo{
			line:     d.Line,
			imported: d.Imported,
			isRecord: true,
		}
		defs[d.Name] = d
	}
	for _, u := range prog.Unions {
		known[u.Name] = typeDefInfo{
			line:     u.Line,
			imported: u.Imported,
			isRecord: false,
		}
		unions[u.Name] = u
	}

	consts := make(map[string]int)
	constExprs := make(map[string]Expr)
	for _, stmt := range prog.Body {
		if ls, ok := stmt.(*ConstDeclStmt); ok {
			consts[ls.Name] = ls.Line
			constExprs[ls.Name] = ls.Value
		}
	}

	funcNames := make(map[string]int, len(prog.Functions))
	funcDecls := make(map[string]*FuncDecl, len(prog.Functions))
	for _, fn := range prog.Functions {
		if _, isBuiltin := builtinArities[fn.Name]; isBuiltin {
			return newError(
				"TypeError",
				fn.Line,
				fn.Col,
				source,
				fmt.Sprintf("Cannot define function %q: it conflicts with a built-in function", fn.Name),
				"Choose a different function name.",
			)
		}
		if _, isType := known[fn.Name]; isType {
			return newError(
				"TypeError",
				fn.Line,
				fn.Col,
				source,
				fmt.Sprintf("Cannot define function %q: it conflicts with a 'def' type", fn.Name),
				"Choose a different function name.",
			)
		}
		if prevLine, dup := funcNames[fn.Name]; dup {
			return newError(
				"TypeError",
				fn.Line,
				fn.Col,
				source,
				fmt.Sprintf("Duplicate function %q (already defined at line %d)", fn.Name, prevLine),
				"Each function name must be unique across the program and imported modules.",
			)
		}
		funcNames[fn.Name] = fn.Line
		funcDecls[fn.Name] = fn
	}

	modules := make(map[string]modTypeInfo)
	for _, imp := range prog.Imports {
		if imp.ModuleProg == nil {
			continue
		}
		alias := imp.Alias
		if alias == "" {
			alias = imp.Name
		}
		if _, isBuiltin := builtinArities[alias]; isBuiltin {
			return newError(
				"TypeError",
				imp.Line,
				imp.Col,
				source,
				fmt.Sprintf("Cannot import module as %q: it conflicts with a built-in function", alias),
				"Choose a different module alias after 'as'.",
			)
		}
		if _, isType := known[alias]; isType {
			return newError(
				"TypeError",
				imp.Line,
				imp.Col,
				source,
				fmt.Sprintf("Cannot import module as %q: it conflicts with a 'def' type", alias),
				"Choose a different module alias after 'as'.",
			)
		}
		if _, isFunc := funcNames[alias]; isFunc {
			return newError(
				"TypeError",
				imp.Line,
				imp.Col,
				source,
				fmt.Sprintf("Cannot import module as %q: it conflicts with a 'to' function", alias),
				"Choose a different module alias after 'as'.",
			)
		}
		if _, dup := modules[alias]; dup {
			return newError(
				"TypeError",
				imp.Line,
				imp.Col,
				source,
				fmt.Sprintf("Duplicate module alias %q", alias),
				"Each imported module must have a unique alias.",
			)
		}
		if err := validateProgramTypesWithMode(imp.ModuleProg, imp.ModuleSource, !imp.IsFileImport); err != nil {
			return err
		}
		origName := ""
		if !imp.IsFileImport {
			origName = imp.Name
		}
		mi := modTypeInfo{
			alias:    alias,
			origName: origName,
			defs:     make(map[string]*DefDecl, len(imp.ModuleProg.Defs)),
			unions:   make(map[string]*UnionDecl, len(imp.ModuleProg.Unions)),
			funcs:    make(map[string]*FuncDecl, len(imp.ModuleProg.Functions)),
			consts:   make(map[string]Expr),
		}
		for _, d := range imp.ModuleProg.Defs {
			mi.defs[d.Name] = d
			qual := alias + "." + d.Name
			known[qual] = typeDefInfo{line: imp.Line, imported: true, isRecord: true}
			defs[qual] = d
		}
		for _, u := range imp.ModuleProg.Unions {
			mi.unions[u.Name] = u
			qual := alias + "." + u.Name
			known[qual] = typeDefInfo{line: imp.Line, imported: true, isRecord: false}
			unions[qual] = u
		}
		for _, fn := range imp.ModuleProg.Functions {
			mi.funcs[fn.Name] = fn
		}
		for _, s := range imp.ModuleProg.Body {
			if cs, ok := s.(*ConstDeclStmt); ok {
				mi.consts[cs.Name] = cs.Value
			}
		}
		modules[alias] = mi
	}

	v := &typeValidator{
		source:                source,
		known:                 known,
		defs:                  defs,
		unions:                unions,
		consts:                consts,
		constExprs:            constExprs,
		funcNames:             funcNames,
		funcDecls:             funcDecls,
		modules:               modules,
		resolvedUnions:        make(map[string][]resolvedUnionVariant, len(unions)),
		scope:                 newTypeScope(nil),
		allowInternalBuiltins: allowInternalBuiltins,
		spawnFuncCache:        make(map[string]bool),
	}

	if err := v.resolveAndValidateUnions(prog); err != nil {
		return err
	}

	for _, d := range prog.Defs {
		if d.Imported {
			continue
		}
		if err := v.validateDefFields(d); err != nil {
			return err
		}
	}

	if err := v.checkStmts(prog.Body, false); err != nil {
		return err
	}

	for _, fn := range prog.Functions {
		for _, p := range fn.Params {
			if err := v.checkNameNotCallable(p, fn.Line, fn.Col, "parameter"); err != nil {
				return err
			}
		}
		fnScope := v.pushScope()
		v.inferFuncUnionParams(fn, fnScope)
		err := v.checkStmts(fn.Body, true)
		v.popScope()
		if err != nil {
			return err
		}
		if err := v.checkFuncReturnConsistency(fn); err != nil {
			return err
		}
	}

	for _, chk := range prog.Checks {
		v.pushScope()
		err := v.checkStmts(chk.Body, false)
		v.popScope()
		if err != nil {
			return err
		}
	}

	return nil
}

var internalStdlibBuiltins = map[string]string{
	"http_call":      "network:fetch / network:send",
	"net_serve_http": "network:serve_http",
	"net_reply_text": "network:reply_text",
	"net_reply_json": "network:reply_json",
	"net_serve_tcp":  "network:serve_tcp",
	"net_send":       "network:tcp_send",
	"screen_open":    "screen:open",
	"screen_present": "screen:present",
	"rand_roll":      "random:roll",
	"rand_pick":      "random:pick",
	"rand_shuffle":   "random:shuffle",
	"synth_play":     "synth:play",
}

func (v *typeValidator) voidBuiltinCallName(e Expr) (string, bool) {
	if cast, ok := e.(*CastExpr); ok {
		if ce, isCall := cast.Target.(*CallExpr); isCall && ce.Module == "" && ce.Callee == "save" {
			return "save", true
		}
		return "", false
	}
	ce, ok := e.(*CallExpr)
	if !ok {
		return "", false
	}
	if ce.Module == "" {
		switch ce.Callee {
		case "close", "put", "save":
			return ce.Callee, true
		case "synth_play":
			return "synth:play", true
		case "screen_open":
			return "screen:open", true
		case "net_reply_text":
			return "network:reply_text", true
		case "net_reply_json":
			return "network:reply_json", true
		case "net_send":
			return "network:tcp_send", true
		default:
			if fn, ok := v.funcDecls[ce.Callee]; ok && !stmtsHaveReturn(fn.Body) {
				return ce.Callee, true
			}
			return "", false
		}
	}
	if mod, ok := v.modules[ce.Module]; ok {
		switch mod.origName {
		case "synth":
			if ce.Callee == "play" {
				return "synth:play", true
			}
		case "screen":
			if ce.Callee == "open" {
				return "screen:open", true
			}
		case "network":
			switch ce.Callee {
			case "reply_text", "reply_json", "tcp_send":
				return "network:" + ce.Callee, true
			}
		}
		if fn, ok := mod.funcs[ce.Callee]; ok && !stmtsHaveReturn(fn.Body) {
			return ce.Module + ":" + ce.Callee, true
		}
	}
	return "", false
}

func (v *typeValidator) checkNotVoidValue(e Expr) error {
	if v.inErrorTestExpr || e == nil {
		return nil
	}
	if sl, isSwarm := e.(*SwarmLoopExpr); isSwarm && !stmtsHaveReturn(sl.Body) {
		line, col := sl.Pos()
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			"'swarm loop' without '-> <value>' does not return a value",
			"Use '-> <value>' inside 'swarm loop' to return an item per iteration, or run it as a statement.",
		)
	}
	name, isVoid := v.voidBuiltinCallName(e)
	if !isVoid {
		return nil
	}
	line, col := e.Pos()
	hint := fmt.Sprintf("Call '%s ...' as a statement instead of using it as a value.", name)
	if name == "synth:play" {
		hint = "Call 'synth:play <melody>' as a statement (or with 'spawn synth:play <melody>')."
	} else if ce, ok := e.(*CallExpr); ok {
		if ce.Module == "" {
			if _, isUser := v.funcDecls[ce.Callee]; isUser {
				return newError(
					"TypeError",
					line,
					col,
					v.source,
					fmt.Sprintf("'%s' is a procedure (void) and does not return a value", name),
					fmt.Sprintf("Call '%s ...' as a statement, or add '-> <value>' inside 'to %s' to return a value.", name, name),
				)
			}
		} else if mod, ok := v.modules[ce.Module]; ok && mod.origName == "" {
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("'%s' is a procedure (void) and does not return a value", name),
				fmt.Sprintf("Call '%s ...' as a statement instead of using it as a value.", name),
			)
		}
	}
	return newError(
		"TypeError",
		line,
		col,
		v.source,
		fmt.Sprintf("'%s' does not return a value", name),
		hint,
	)
}

func (v *typeValidator) isVoidCallStmt(s Stmt) bool {
	es, ok := s.(*ExprStmt)
	if !ok {
		return false
	}
	_, isVoid := v.voidBuiltinCallName(es.Expression)
	return isVoid
}

func (v *typeValidator) checkNameNotCallable(name string, line, col int, kind string) error {
	if _, isBuiltin := builtinArities[name]; isBuiltin {
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Cannot name %s %q the same as a built-in function", kind, name),
			fmt.Sprintf("Choose a different %s name so calls to '%s' remain unambiguous.", kind, name),
		)
	}
	if _, isFunc := v.funcNames[name]; isFunc {
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Cannot name %s %q the same as a 'to' function", kind, name),
			fmt.Sprintf("Choose a different %s name so calls to '%s' remain unambiguous.", kind, name),
		)
	}
	if _, isMod := v.modules[name]; isMod {
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Cannot name %s %q the same as an imported module alias", kind, name),
			fmt.Sprintf("Choose a different %s name.", kind),
		)
	}
	return nil
}

func (v *typeValidator) pushScope() *typeScope {
	v.scope = newTypeScope(v.scope)
	return v.scope
}

func (v *typeValidator) popScope() {
	if v.scope != nil && v.scope.parent != nil {
		v.scope = v.scope.parent
	}
}

func (v *typeValidator) lookupVar(name string) (varTypeState, bool) {
	for cur := v.scope; cur != nil; cur = cur.parent {
		if st, ok := cur.vars[name]; ok {
			return st, true
		}
	}
	return varTypeState{}, false
}

func (v *typeValidator) setVarInCurrentOrExisting(name string, st varTypeState) {
	for cur := v.scope; cur != nil; cur = cur.parent {
		if _, ok := cur.vars[name]; ok {
			cur.vars[name] = st
			return
		}
		if cur.isWorkerRoot {
			break
		}
	}
	v.scope.vars[name] = st
}

func (v *typeValidator) inferFuncUnionParams(fn *FuncDecl, fnScope *typeScope) {
	if len(fn.Params) == 0 || len(v.resolvedUnions) == 0 {
		return
	}
	paramSet := make(map[string]bool, len(fn.Params))
	for _, p := range fn.Params {
		paramSet[p] = true
	}

	testedByVar := make(map[string][]resolvedUnionVariant)
	var collectFromExpr func(e Expr)
	var collectFromStmts func(stmts []Stmt)

	collectFromExpr = func(e Expr) {
		if e == nil {
			return
		}
		switch ex := e.(type) {
		case *BinaryExpr:
			if ex.Op == "is" || ex.Op == "is-not" {
				if id, ok := ex.Left.(*IdentExpr); ok && paramSet[id.Name] {
					if variants, ok := v.exprToPatternVariants(ex.Right); ok {
						testedByVar[id.Name] = append(testedByVar[id.Name], variants...)
					}
				}
			}
			collectFromExpr(ex.Left)
			collectFromExpr(ex.Right)
		case *TypeCheckExpr:
			if id, ok := ex.Target.(*IdentExpr); ok && paramSet[id.Name] {
				if ex.TypeName == "text" || ex.TypeName == "number" {
					testedByVar[id.Name] = append(testedByVar[id.Name], resolvedUnionVariant{
						kind:     variantBaseType,
						baseType: ex.TypeName,
					})
				}
			}
			collectFromExpr(ex.Target)
		case *UnaryExpr:
			collectFromExpr(ex.Right)
		}
	}

	collectFromStmts = func(stmts []Stmt) {
		for _, s := range stmts {
			switch st := s.(type) {
			case *CheckGuardStmt:
				if st.Cond != nil {
					collectFromExpr(st.Cond)
				}
				collectFromStmts(st.Body)
				for _, r := range st.Rules {
					collectFromExpr(r.Cond)
					if r.Action != nil {
						collectFromStmts([]Stmt{r.Action})
					}
				}
				collectFromStmts(st.ElseBody)
			case *ReturnStmt:
				collectFromExpr(st.Value)
			case *LoopStmt:
				collectFromStmts(st.Body)
			case *TakeStmt:
				collectFromStmts(st.Body)
			case *MatchStmt:
				for _, arm := range st.Arms {
					if arm.Action != nil {
						collectFromStmts([]Stmt{arm.Action})
					}
					collectFromStmts(arm.Body)
				}
				collectFromStmts(st.ElseBody)
			}
		}
	}
	collectFromStmts(fn.Body)

	for _, p := range fn.Params {
		tested := testedByVar[p]
		if len(tested) == 0 {
			continue
		}
		distinctKeys := make(map[string]bool, len(tested))
		hasRecord := false
		for _, tv := range tested {
			distinctKeys[tv.key()] = true
			if tv.kind == variantRecord {
				hasRecord = true
			}
		}
		if len(distinctKeys) < 2 && !hasRecord {
			continue
		}
		if uName, ok := v.findMatchingUnionForVariants(tested, hasRecord); ok {
			fnScope.vars[p] = varTypeState{unionName: uName}
		} else if len(distinctKeys) == 1 && hasRecord {
			fnScope.vars[p] = varTypeState{exactRecord: ""}
		}
	}
}

func (v *typeValidator) exprToPatternVariants(e Expr) ([]resolvedUnionVariant, bool) {
	switch ex := e.(type) {
	case *StringExpr:
		return []resolvedUnionVariant{{kind: variantStringLit, strVal: ex.Value}}, true
	case *NumberExpr:
		return []resolvedUnionVariant{{kind: variantNumberLit, numVal: ex.Value, rawNum: ex.Raw}}, true
	case *UnaryExpr:
		if ex.Op == "-" {
			if n, ok := ex.Right.(*NumberExpr); ok {
				return []resolvedUnionVariant{{kind: variantNumberLit, numVal: -n.Value, rawNum: "-" + n.Raw}}, true
			}
		}
	case *IdentExpr:
		if ex.Name == "text" || ex.Name == "number" {
			return []resolvedUnionVariant{{kind: variantBaseType, baseType: ex.Name}}, true
		}
		if v.defs[ex.Name] != nil {
			return []resolvedUnionVariant{{kind: variantRecord, recordName: ex.Name}}, true
		}
		if sub, ok := v.resolvedUnions[ex.Name]; ok {
			return sub, true
		}
	case *FieldExpr:
		if id, ok := ex.Target.(*IdentExpr); ok {
			if _, isMod := v.modules[id.Name]; isMod {
				qual := id.Name + "." + ex.Field
				if v.defs[qual] != nil {
					return []resolvedUnionVariant{{kind: variantRecord, recordName: qual}}, true
				}
				if sub, ok := v.resolvedUnions[qual]; ok {
					return sub, true
				}
			}
		}
	}
	return nil, false
}

func (v *typeValidator) typeStateFromName(name string) varTypeState {
	if v.unions[name] != nil {
		return varTypeState{unionName: name}
	}
	if v.defs[name] != nil {
		return varTypeState{exactRecord: name}
	}
	if name == "text" || name == "number" {
		return varTypeState{exactNonRecord: true, exactBaseType: name}
	}
	return varTypeState{}
}

func (v *typeValidator) staticAddOperandType(st varTypeState) string {
	return v.staticTypeKind(st, true)
}

func (v *typeValidator) staticTypeKind(st varTypeState, genericRecordLabel bool) string {
	if st.exactBaseType != "" {
		return st.exactBaseType
	}
	if st.exactRecord != "" {
		return st.exactRecord
	}
	if st.listElemType != "" {
		return "list"
	}
	if st.inboxElemType != "" {
		return "inbox"
	}
	var variants []resolvedUnionVariant
	if st.inlineUnionLabel != "" && len(st.inlineUnionVariants) > 0 {
		variants = st.inlineUnionVariants
	} else if st.unionName != "" {
		variants = v.resolvedUnions[st.unionName]
	}
	if len(variants) == 0 {
		return ""
	}
	kind := ""
	for _, vr := range variants {
		cur := ""
		switch vr.kind {
		case variantStringLit:
			cur = "text"
		case variantNumberLit:
			cur = "number"
		case variantBaseType:
			cur = vr.baseType
		case variantRecord:
			if genericRecordLabel {
				cur = "record"
			} else {
				cur = vr.recordName
			}
		}
		if cur == "" {
			return ""
		}
		if kind == "" {
			kind = cur
		} else if kind != cur {
			return ""
		}
	}
	return kind
}

func (v *typeValidator) inferExprTypeState(e Expr) varTypeState {
	switch ex := e.(type) {
	case *CastExpr:
		switch ex.TypeName {
		case "bytes":
			return varTypeState{listElemType: "number"}
		case "csv":
			if call, isCall := ex.Target.(*CallExpr); isCall && call.Module == "" && call.Callee == "load" {
				return varTypeState{listElemType: "map"}
			}
			targetSt := v.inferExprTypeState(ex.Target)
			if targetSt.exactBaseType == "text" {
				return varTypeState{listElemType: "map"}
			}
			if targetSt.listElemType != "" {
				return varTypeState{exactNonRecord: true, exactBaseType: "text"}
			}
			return varTypeState{}
		case "json":
			if call, isCall := ex.Target.(*CallExpr); isCall && call.Module == "" && call.Callee == "load" {
				return varTypeState{}
			}
			targetSt := v.inferExprTypeState(ex.Target)
			if targetSt.exactRecord != "" || targetSt.listElemType != "" {
				return varTypeState{exactNonRecord: true, exactBaseType: "text"}
			}
			return varTypeState{}
		default:
			targetSt := v.inferExprTypeState(ex.Target)
			if targetSt.listElemType != "" {
				return varTypeState{listElemType: ex.TypeName}
			}
			return v.typeStateFromName(ex.TypeName)
		}
	case *TypedListExpr:
		return varTypeState{listElemType: ex.ElemType}
	case *ListExpr:
		if len(ex.Elements) == 0 {
			return varTypeState{listElemType: "any"}
		}
		firstSt := v.inferExprTypeState(ex.Elements[0])
		if firstSt.exactBaseType != "" {
			return varTypeState{listElemType: firstSt.exactBaseType}
		}
		if firstSt.exactRecord != "" {
			return varTypeState{listElemType: firstSt.exactRecord}
		}
		return varTypeState{listElemType: "any"}
	case *IdentExpr:
		if v.defs[ex.Name] != nil {
			return varTypeState{exactRecord: ex.Name}
		}
		if st, ok := v.lookupVar(ex.Name); ok {
			return st.clone()
		}
	case *WithExpr:
		baseSt := v.inferExprTypeState(ex.Base)
		if baseSt.exactRecord != "" {
			return varTypeState{exactRecord: baseSt.exactRecord}
		}
		if baseSt.listElemType != "" {
			return varTypeState{listElemType: baseSt.listElemType}
		}
	case *WhereExpr:
		return v.inferExprTypeState(ex.Target)
	case *MatchExpr:
		return v.inferMatchExprTypeState(ex)
	case *SwarmLoopExpr:
		return v.inferSwarmLoopTypeState(ex)
	case *InboxExpr:
		return varTypeState{inboxElemType: ex.ElemType}
	case *FieldExpr:
		if id, ok := ex.Target.(*IdentExpr); ok && id.Name == "it" && ex.Field == "index" && v.loopDepth > 0 {
			return varTypeState{exactNonRecord: true}
		}
		if id, ok := ex.Target.(*IdentExpr); ok && id.Name == "error" && (ex.Field == "type" || ex.Field == "message") && v.inErrorBlock {
			return varTypeState{exactNonRecord: true}
		}
		if id, ok := ex.Target.(*IdentExpr); ok {
			if mod, isMod := v.modules[id.Name]; isMod {
				if mod.defs[ex.Field] != nil {
					return varTypeState{exactRecord: id.Name + "." + ex.Field}
				}
				if mod.unions[ex.Field] != nil {
					return varTypeState{unionName: id.Name + "." + ex.Field}
				}
				if cExpr, isConst := mod.consts[ex.Field]; isConst {
					return v.inferExprTypeState(cExpr)
				}
			}
		}
		targetSt := v.inferExprTypeState(ex.Target)
		if targetSt.exactRecord != "" {
			if variants, label, ok := v.fieldUnionVariants(targetSt.exactRecord, ex.Field); ok {
				return varTypeState{
					inlineUnionLabel:    label,
					inlineUnionVariants: variants,
				}
			}
		}
	case *IndexExpr:
		targetSt := v.inferExprTypeState(ex.Target)
		if targetSt.listElemType != "" {
			return v.typeStateFromName(targetSt.listElemType)
		}
	case *StringExpr, *InterpStringExpr:
		return varTypeState{exactNonRecord: true, exactBaseType: "text"}
	case *NumberExpr, *UnaryExpr:
		return varTypeState{exactNonRecord: true, exactBaseType: "number"}
	case *BinaryExpr:
		switch ex.Op {
		case "+", "-", "*", "/", "%":
			leftSt := v.inferExprTypeState(ex.Left)
			rightSt := v.inferExprTypeState(ex.Right)
			if leftSt.listElemType == "number" || rightSt.listElemType == "number" {
				return varTypeState{listElemType: "number"}
			}
			if ex.Op == "+" {
				if leftSt.exactBaseType == "text" && rightSt.exactBaseType == "text" {
					return varTypeState{exactNonRecord: true, exactBaseType: "text"}
				}
				if leftSt.exactBaseType == "number" && rightSt.exactBaseType == "number" {
					return varTypeState{exactNonRecord: true, exactBaseType: "number"}
				}
				return varTypeState{exactNonRecord: true}
			}
			return varTypeState{exactNonRecord: true, exactBaseType: "number"}
		}
	case *CallExpr:
		if ex.Module != "" {
			if mod, ok := v.modules[ex.Module]; ok && mod.origName == "random" {
				switch ex.Callee {
				case "roll":
					return varTypeState{exactNonRecord: true, exactBaseType: "number"}
				case "pick":
					if len(ex.Args) >= 1 {
						return v.inferIterableElemState(ex.Args[0])
					}
				case "shuffle":
					if len(ex.Args) >= 1 {
						return v.inferExprTypeState(ex.Args[0])
					}
				}
			}
			return varTypeState{}
		}
		switch ex.Callee {
		case "append":
			if len(ex.Args) >= 1 {
				argSt := v.inferExprTypeState(ex.Args[0])
				if argSt.listElemType != "" {
					return varTypeState{listElemType: argSt.listElemType}
				}
				if len(ex.Args) >= 2 {
					arg2St := v.inferExprTypeState(ex.Args[1])
					if arg2St.listElemType != "" {
						return varTypeState{listElemType: arg2St.listElemType}
					}
					if arg2St.unionName != "" {
						return varTypeState{listElemType: arg2St.unionName}
					}
					if arg2St.exactRecord != "" {
						return varTypeState{listElemType: arg2St.exactRecord}
					}
					switch ex.Args[1].(type) {
					case *NumberExpr:
						return varTypeState{listElemType: "number"}
					case *StringExpr, *InterpStringExpr:
						return varTypeState{listElemType: "text"}
					}
				}
			}
		case "floor", "round", "sum", "avg":
			return varTypeState{exactNonRecord: true, exactBaseType: "number"}
		case "sort", "slice":
			if len(ex.Args) >= 1 {
				return v.inferExprTypeState(ex.Args[0])
			}
		case "split":
			return varTypeState{listElemType: "text"}
		case "upper", "lower", "join", "trim", "replace":
			return varTypeState{exactNonRecord: true, exactBaseType: "text"}
		case "load":
			return varTypeState{exactNonRecord: true, exactBaseType: "text"}
		case "save":
			return varTypeState{}
		case "range":
			return varTypeState{listElemType: "number"}
		case "rand_roll":
			return varTypeState{exactNonRecord: true, exactBaseType: "number"}
		case "rand_pick":
			if len(ex.Args) >= 1 {
				return v.inferIterableElemState(ex.Args[0])
			}
		case "rand_shuffle":
			if len(ex.Args) >= 1 {
				return v.inferExprTypeState(ex.Args[0])
			}
		}
	}
	return varTypeState{}
}

func (v *typeValidator) inferIterableElemState(iter Expr) varTypeState {
	st := v.inferExprTypeState(iter)
	if st.listElemType != "" {
		return v.typeStateFromName(st.listElemType)
	}
	if st.unionName != "" {
		return varTypeState{unionName: st.unionName}
	}
	if st.exactRecord != "" {
		return varTypeState{exactRecord: st.exactRecord}
	}
	return varTypeState{}
}

func (v *typeValidator) checkExpr(e Expr) error {
	if e == nil {
		return nil
	}
	switch ex := e.(type) {
	case *CastExpr:
		if _, isTargetCast := ex.Target.(*CastExpr); isTargetCast {
			return newError(
				"TypeError",
				ex.TypeLine,
				ex.TypeCol,
				v.source,
				"Cannot chain multiple 'as' conversions in one expression",
				"Assign the intermediate value to a named variable first (e.g. 'json_data = raw as json', then 'user = json_data as api_user').",
			)
		}
		if _, isFallbackCast := ex.Fallback.(*CastExpr); isFallbackCast {
			return newError(
				"TypeError",
				ex.OrLine,
				ex.OrCol,
				v.source,
				"Cannot chain multiple 'as' conversions in one expression",
				"Assign the intermediate value to a named variable first (e.g. 'json_data = raw as json', then 'user = json_data as api_user').",
			)
		}
		if call, isCall := ex.Target.(*CallExpr); isCall && call.Module == "" && call.Callee == "save" {
			return v.checkSaveCastExpr(ex, call)
		}
		if err := v.checkExpr(ex.Target); err != nil {
			return err
		}
		if err := v.checkNotCondition(ex.Target); err != nil {
			return err
		}
		if err := v.checkNotVoidValue(ex.Target); err != nil {
			return err
		}
		isLoadCall := false
		if call, isCall := ex.Target.(*CallExpr); isCall && call.Module == "" && call.Callee == "load" {
			isLoadCall = true
		}
		if ex.Fallback != nil && (ex.TypeName == "json" || ex.TypeName == "csv" || ex.TypeName == "bytes" || isLoadCall) {
			if err := v.checkExpr(ex.Fallback); err != nil {
				return err
			}
			if err := v.checkNotCondition(ex.Fallback); err != nil {
				return err
			}
			if !v.inErrorTestExpr {
				if isLoadCall {
					return newError(
						"TypeError",
						ex.OrLine,
						ex.OrCol,
						v.source,
						fmt.Sprintf("'or' fallback cannot be used with 'load ... as %s'", ex.TypeName),
						"Handle file loading errors separately; 'or' fallbacks are for value conversions.",
					)
				}
				return newError(
					"TypeError",
					ex.OrLine,
					ex.OrCol,
					v.source,
					fmt.Sprintf("'or' fallback cannot be used with 'as %s'", ex.TypeName),
					fmt.Sprintf("'as %s' does not support 'or' fallbacks.", ex.TypeName),
				)
			}
			return nil
		}
		switch ex.TypeName {
		case "bytes":
			if !isLoadCall && !v.inErrorTestExpr {
				return newError(
					"TypeError",
					ex.TypeLine,
					ex.TypeCol,
					v.source,
					"Target type \"bytes\" can only be used with 'load <path> as bytes' or 'save <path> <data> as bytes'",
					"Write: load \"./path\" as bytes",
				)
			}
			return nil
		case "json":
			if isLoadCall {
				return nil
			}
			if !v.inErrorTestExpr {
				if isConditionExpr(ex.Target) {
					return newError("TypeError", ex.TypeLine, ex.TypeCol, v.source, "Conditions cannot be formatted with 'as json'", "Model states with explicit union types or records.")
				}
				targetSt := v.inferExprTypeState(ex.Target)
				if targetSt.exactBaseType == "number" {
					return newError(
						"TypeError",
						ex.TypeLine,
						ex.TypeCol,
						v.source,
						"Cannot format number with 'as json'",
						"Use '<num> as text' for numbers, or pass a record, list, map, or JSON text to 'as json'.",
					)
				}
				if targetSt.inboxElemType != "" {
					return newError(
						"TypeError",
						ex.TypeLine,
						ex.TypeCol,
						v.source,
						"Cannot format inbox with 'as json'",
						"Pass JSON text to parse, or a record, list, or map to serialize with 'as json'.",
					)
				}
			}
			return nil
		case "csv":
			if isLoadCall {
				return nil
			}
			if !v.inErrorTestExpr {
				return v.checkCSVFormatArg(ex.Target, ex.TypeLine, ex.TypeCol)
			}
			return nil
		}
		if isLoadCall && ex.TypeName == "text" {
			return nil
		}
		if err := v.validateTypeTarget(ex.TypeName, ex.TypeLine, ex.TypeCol, "as", false); err != nil {
			return err
		}
		if ex.Fallback != nil {
			return v.checkCastFallback(ex)
		}
		return nil

	case *TypedListExpr:
		for _, row := range ex.Rows {
			for _, upd := range row {
				if err := v.checkExpr(upd.Value); err != nil {
					return err
				}
				if err := v.checkNotCondition(upd.Value); err != nil {
					return err
				}
			}
		}
		requireRecord := len(ex.Rows) > 0
		return v.validateTypeTarget(ex.ElemType, ex.TypeLine, ex.TypeCol, "list as", requireRecord)

	case *ListExpr:
		for _, el := range ex.Elements {
			if err := v.checkExpr(el); err != nil {
				return err
			}
			if err := v.checkNotCondition(el); err != nil {
				return err
			}
			if err := v.checkNotVoidValue(el); err != nil {
				return err
			}
		}

	case *InterpStringExpr:
		for _, p := range ex.Parts {
			if p.IsExpr {
				if err := v.checkExpr(p.Expr); err != nil {
					return err
				}
				if err := v.checkNotCondition(p.Expr); err != nil {
					return err
				}
				if err := v.checkNotVoidValue(p.Expr); err != nil {
					return err
				}
			}
		}

	case *UnaryExpr:
		if err := v.checkExpr(ex.Right); err != nil {
			return err
		}
		if ex.Op == "not" {
			if !v.inErrorTestExpr && !isConditionExpr(ex.Right) {
				rightSt := v.inferExprTypeState(ex.Right)
				knownValue := rightSt.exactRecord != "" || rightSt.exactNonRecord || rightSt.unionName != "" || rightSt.listElemType != ""
				switch rt := ex.Right.(type) {
				case *NumberExpr, *StringExpr, *InterpStringExpr, *ListExpr, *TypedListExpr, *WithExpr:
					knownValue = true
				case *CallExpr:
					if rt.Callee != "contains" {
						knownValue = true
					}
				}
				if knownValue {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						"'not' requires a condition",
						"Example: not (x is 0) or not (contains txt \"a\")",
					)
				}
			}
			return nil
		}
		return v.checkNotCondition(ex.Right)

	case *BinaryExpr:
		if err := v.checkExpr(ex.Left); err != nil {
			return err
		}
		if ex.Op == "and" || ex.Op == "or" {
			rhsScope := v.pushScope()
			v.applyCondNarrowing(rhsScope, ex.Left, ex.Op == "and")
			err := v.checkExpr(ex.Right)
			v.popScope()
			return err
		}
		if err := v.checkNotCondition(ex.Left); err != nil {
			return err
		}
		if err := v.checkNotVoidValue(ex.Left); err != nil {
			return err
		}
		if err := v.checkExpr(ex.Right); err != nil {
			return err
		}
		if err := v.checkNotCondition(ex.Right); err != nil {
			return err
		}
		if err := v.checkNotVoidValue(ex.Right); err != nil {
			return err
		}
		if (ex.Op == "is" || ex.Op == "is not") && !v.inErrorTestExpr {
			return v.checkUnionLiteralComparison(ex)
		}
		if !v.inErrorTestExpr && (ex.Op == "+" || ex.Op == "-" || ex.Op == "*" || ex.Op == "/" || ex.Op == "%") {
			leftSt := v.inferExprTypeState(ex.Left)
			rightSt := v.inferExprTypeState(ex.Right)
			if (leftSt.listElemType != "" && leftSt.listElemType != "number") || (rightSt.listElemType != "" && rightSt.listElemType != "number") {
				if ex.Op == "+" {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						"Cannot use '+' on non-numeric lists",
						"Use 'append xs ys' to combine lists; '+' on lists performs element-wise numeric addition.",
					)
				}
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("Operator %q only supports numbers or numeric lists", ex.Op),
					"Vector arithmetic requires numeric lists.",
				)
			}
			if ex.Op == "+" {
				lt := v.staticAddOperandType(leftSt)
				rt := v.staticAddOperandType(rightSt)
				if lt != "" && rt != "" {
					allowed := (lt == "text" && rt == "text") ||
						(lt == "number" && rt == "number") ||
						(lt == "list" && rt == "list") ||
						(lt == "list" && rt == "number") ||
						(lt == "number" && rt == "list")
					if !allowed {
						return newError(
							"TypeError",
							ex.Line,
							ex.Col,
							v.source,
							fmt.Sprintf("Cannot add %s and %s with '+'", lt, rt),
							"Use string interpolation '\"{a}{b}\"' or explicit 'as text' conversion to combine text with other values.",
						)
					}
				}
			}
		}
		return nil

	case *TypeCheckExpr:
		return v.checkExpr(ex.Target)

	case *FieldExpr:
		if err := v.checkExpr(ex.Target); err != nil {
			return err
		}
		if v.inErrorTestExpr {
			return nil
		}
		if id, ok := ex.Target.(*IdentExpr); ok {
			if id.Name == "error" {
				if !v.inErrorBlock {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						"'error' can only be used inside an 'expr -> ... end' error block",
						"Wrap error assertions in 'expr -> error.type is \"...\" end'.",
					)
				}
				if ex.Field != "type" && ex.Field != "message" {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						fmt.Sprintf("error has no field %q", ex.Field),
						"Available fields on error: type, message.",
					)
				}
				return nil
			}
			if id.Name == "it" && ex.Field == "index" && v.loopDepth > 0 {
				return nil
			}
			if mod, isMod := v.modules[id.Name]; isMod {
				if mod.funcs[ex.Field] != nil {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						fmt.Sprintf("Cannot access module function %q on '%s' with '.'", ex.Field, id.Name),
						fmt.Sprintf("Use ':' to call module functions, e.g. '%s:%s ...'.", id.Name, ex.Field),
					)
				}
				if mod.defs[ex.Field] == nil && mod.unions[ex.Field] == nil && mod.consts[ex.Field] == nil {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						fmt.Sprintf("Module %q has no member %q", id.Name, ex.Field),
						fmt.Sprintf("Check the definitions exported by module %q.", id.Name),
					)
				}
				return nil
			}
			if st, tracked := v.lookupVar(id.Name); tracked {
				if st.exactRecord != "" {
					if defDecl, ok := v.defs[st.exactRecord]; ok && !hasDefField(defDecl, ex.Field) {
						fieldNames := make([]string, len(defDecl.Fields))
						for i, f := range defDecl.Fields {
							fieldNames[i] = f.Name
						}
						fieldHint := fmt.Sprintf("Available fields on %s: %s.", st.exactRecord, defFieldNames(defDecl))
						if suggestion := closestTypeName(ex.Field, fieldNames); suggestion != "" {
							fieldHint = fmt.Sprintf("Did you mean %q? %s", suggestion, fieldHint)
						}
						return newError(
							"TypeError",
							ex.Line,
							ex.Col,
							v.source,
							fmt.Sprintf("%s has no field %q", st.exactRecord, ex.Field),
							fieldHint,
						)
					}
					return nil
				}
				if st.exactNonRecord {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						fmt.Sprintf("Cannot read field '.%s' on non-record variable %q", ex.Field, id.Name),
						fmt.Sprintf("Narrow %q with '%s is <Record>' before accessing '.%s'.", id.Name, id.Name, ex.Field),
					)
				}
				if st.unionName != "" {
					recOpts := v.unionRecordNames(st.unionName)
					hint := fmt.Sprintf("Union %q has no record variants.", st.unionName)
					if len(recOpts) > 0 {
						hint = fmt.Sprintf("Narrow %q first (e.g. 'check %s is %s -> ...').", id.Name, id.Name, recOpts[0])
					}
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						fmt.Sprintf("Cannot read field '.%s' on union variable %q (%s) before narrowing", ex.Field, id.Name, st.unionName),
						hint,
					)
				}
				if len(st.excludedRecords) > 0 {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						fmt.Sprintf("Cannot read field '.%s' on un-narrowed variable %q", ex.Field, id.Name),
						fmt.Sprintf("Narrow %q with '%s is <Record>' before accessing '.%s'.", id.Name, id.Name, ex.Field),
					)
				}
			}
		} else {
			targetSt := v.inferExprTypeState(ex.Target)
			if targetSt.exactRecord != "" {
				if defDecl, ok := v.defs[targetSt.exactRecord]; ok && !hasDefField(defDecl, ex.Field) {
					fieldNames := make([]string, len(defDecl.Fields))
					for i, f := range defDecl.Fields {
						fieldNames[i] = f.Name
					}
					fieldHint := fmt.Sprintf("Available fields on %s: %s.", targetSt.exactRecord, defFieldNames(defDecl))
					if suggestion := closestTypeName(ex.Field, fieldNames); suggestion != "" {
						fieldHint = fmt.Sprintf("Did you mean %q? %s", suggestion, fieldHint)
					}
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						fmt.Sprintf("%s has no field %q", targetSt.exactRecord, ex.Field),
						fieldHint,
					)
				}
			}
		}
		return nil

	case *IndexExpr:
		if err := v.checkExpr(ex.Target); err != nil {
			return err
		}
		if err := v.checkExpr(ex.Index); err != nil {
			return err
		}
		return v.checkNotCondition(ex.Index)

	case *WithExpr:
		if err := v.checkExpr(ex.Base); err != nil {
			return err
		}
		if id, ok := ex.Base.(*IdentExpr); ok {
			if mod, isMod := v.modules[id.Name]; isMod {
				hint := "'with' only updates 'def' blueprints, records, or lists of records."
				if mod.defs[id.Name] != nil {
					hint = fmt.Sprintf("Use '%s.%s with ...' to instantiate the module's blueprint.", id.Name, id.Name)
				}
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("Cannot use 'with' on module %q", id.Name),
					hint,
				)
			}
		}
		baseSt := v.inferExprTypeState(ex.Base)
		if len(ex.IndexUpdates) > 0 {
			if !v.inErrorTestExpr {
				if baseSt.exactRecord != "" || baseSt.exactNonRecord || baseSt.unionName != "" || baseSt.inboxElemType != "" {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						"'with .<index> = <val>' requires a list",
						"Use 'rec with field = val' for records, or pass a list to 'with .<index> = <val>'.",
					)
				}
			}
			for _, upd := range ex.IndexUpdates {
				if err := v.checkExpr(upd.Index); err != nil {
					return err
				}
				if err := v.checkNotCondition(upd.Index); err != nil {
					return err
				}
				if err := v.checkNotVoidValue(upd.Index); err != nil {
					return err
				}
				if err := v.checkExpr(upd.Value); err != nil {
					return err
				}
				if err := v.checkNotCondition(upd.Value); err != nil {
					return err
				}
				if err := v.checkNotVoidValue(upd.Value); err != nil {
					return err
				}
				if !v.inErrorTestExpr {
					idxSt := v.inferExprTypeState(upd.Index)
					badIdx := (idxSt.exactBaseType != "" && idxSt.exactBaseType != "number") ||
						idxSt.exactRecord != "" || idxSt.listElemType != "" || idxSt.inboxElemType != ""
					if !badIdx && (idxSt.unionName != "" || idxSt.inlineUnionLabel != "") {
						if ct := v.staticConcreteType(idxSt); ct != "" && ct != "number" {
							badIdx = true
						}
					}
					if badIdx {
						iLine, iCol := upd.Index.Pos()
						return newError(
							"TypeError",
							iLine,
							iCol,
							v.source,
							"List index in 'with' must be a number",
							"Use a 1-based integer number, e.g. xs with .1 = val or xs with .pos = val.",
						)
					}
					if baseSt.listElemType != "" && baseSt.listElemType != "any" && baseSt.listElemType != "list" {
						if err := v.checkTypedElementValue(baseSt.listElemType, upd.Value, "assign", "list"); err != nil {
							return err
						}
					}
				}
			}
			return nil
		}
		if baseSt.listElemType != "" {
			if !v.inErrorTestExpr && (baseSt.listElemType == "number" || baseSt.listElemType == "text") {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("Cannot use 'with' on list of %s", baseSt.listElemType),
					"'with' on a list only supports lists of 'def' records.",
				)
			}
			withScope := v.pushScope()
			withScope.vars["it"] = v.inferIterableElemState(ex.Base)
			v.loopDepth++
			var updErr error
			for _, upd := range ex.Updates {
				if err := v.checkExpr(upd.Value); err != nil {
					updErr = err
					break
				}
				if err := v.checkNotCondition(upd.Value); err != nil {
					updErr = err
					break
				}
				if err := v.checkNotVoidValue(upd.Value); err != nil {
					updErr = err
					break
				}
			}
			v.loopDepth--
			v.popScope()
			return updErr
		}
		for _, upd := range ex.Updates {
			if err := v.checkExpr(upd.Value); err != nil {
				return err
			}
			if err := v.checkNotCondition(upd.Value); err != nil {
				return err
			}
			if err := v.checkNotVoidValue(upd.Value); err != nil {
				return err
			}
		}

	case *WhereExpr:
		if err := v.checkExpr(ex.Target); err != nil {
			return err
		}
		targetSt := v.inferExprTypeState(ex.Target)
		if !v.inErrorTestExpr && (targetSt.exactRecord != "" || targetSt.exactNonRecord || targetSt.unionName != "") {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				"Cannot use 'where' on non-list value",
				"'where' filters a list, e.g.: orders where it.amount > 50.",
			)
		}
		whereScope := v.pushScope()
		whereScope.vars["it"] = v.inferIterableElemState(ex.Target)
		v.loopDepth++
		var condErr error
		for _, cond := range ex.Conditions {
			if err := v.checkExpr(cond); err != nil {
				condErr = err
				break
			}
			v.applyCondNarrowing(whereScope, cond, true)
		}
		v.loopDepth--
		v.popScope()
		return condErr

	case *MatchExpr:
		return v.checkMatchExpr(ex)

	case *SwarmLoopExpr:
		return v.checkSwarmLoopExpr(ex)

	case *InboxExpr:
		if ex.ElemType == "csv" || ex.ElemType == "bytes" {
			return newError(
				"TypeError",
				ex.TypeLine,
				ex.TypeCol,
				v.source,
				fmt.Sprintf("Type %q cannot be used with 'inbox as'", ex.ElemType),
				"Use 'text', 'number', a 'def' record, or a union type.",
			)
		}
		return v.validateTypeTarget(ex.ElemType, ex.TypeLine, ex.TypeCol, "inbox as", false)

	case *IdentExpr:
		if !v.inErrorTestExpr {
			if ex.Name == "decode_json" {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					"'decode_json' was removed from Ease",
					"Use '<text> as json' instead (e.g. 'data = raw as json').",
				)
			}
			if ex.Name == "encode_json" {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					"'encode_json' was removed from Ease",
					"Use '<value> as json' instead (e.g. 'raw = user as json').",
				)
			}
		}
		if v.swarmDepth > 0 && v.workerAssigned != nil && v.workerAssigned[ex.Name] {
			assignedLocal := false
			for cur := v.scope; cur != nil && cur != v.workerOuterScope; cur = cur.parent {
				if _, ok := cur.vars[ex.Name]; ok {
					assignedLocal = true
					break
				}
			}
			if !assignedLocal && v.workerOuterScope != nil {
				for cur := v.workerOuterScope; cur != nil; cur = cur.parent {
					if _, inOuter := cur.vars[ex.Name]; inOuter {
						return newError(
							"TypeError",
							ex.Line,
							ex.Col,
							v.source,
							fmt.Sprintf("Cannot modify outer variable %q inside 'swarm loop'", ex.Name),
							fmt.Sprintf("Assign to a different local variable name instead of reusing outer %q.", ex.Name),
						)
					}
				}
			}
		}
		return nil

	case *CallExpr:
		if !v.allowInternalBuiltins && !v.inErrorTestExpr && ex.Module == "" {
			if publicAlt, isInternal := internalStdlibBuiltins[ex.Callee]; isInternal {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("Internal built-in %q cannot be called directly; use %s instead", ex.Callee, publicAlt),
					fmt.Sprintf("Import the standard module and call %s.", publicAlt),
				)
			}
		}
		if v.swarmDepth > 0 && !v.inErrorTestExpr && ex.Module == "" {
			if ex.Callee == "ask" {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					"'ask' cannot be used inside 'swarm loop'",
					"Read interactive input before 'swarm loop', not inside parallel workers.",
				)
			}
			if v.funcUsesSpawn(ex.Callee) {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("Cannot call %q inside 'swarm loop' because it uses 'spawn'; 'swarm loop' already runs iterations in parallel", ex.Callee),
					"Move 'spawn' outside 'swarm loop' or use top-level inbox pipelines.",
				)
			}
		}
		if ex.Module != "" {
			mod, isMod := v.modules[ex.Module]
			if !isMod {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("Unknown module %q", ex.Module),
					"Import the file module with 'import \"./file.ease\" as <alias>' first.",
				)
			}
			if mod.funcs[ex.Callee] == nil {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("Module %q has no function %q", ex.Module, ex.Callee),
					fmt.Sprintf("Define 'to %s ...' in module %q.", ex.Callee, ex.Module),
				)
			}
		}
		if !v.inErrorTestExpr && ex.Module == "" && ex.Callee == "save" && len(ex.Args) == 2 {
			dataSt := v.inferExprTypeState(ex.Args[1])
			if dataSt.exactRecord != "" || dataSt.listElemType != "" || dataSt.unionName != "" || dataSt.inboxElemType != "" || (dataSt.exactBaseType != "" && dataSt.exactBaseType != "text") {
				gotType := "value"
				switch {
				case dataSt.exactRecord != "":
					gotType = dataSt.exactRecord
				case dataSt.listElemType != "":
					gotType = "list of " + dataSt.listElemType
				case dataSt.unionName != "":
					gotType = dataSt.unionName
				case dataSt.inboxElemType != "":
					gotType = "inbox"
				case dataSt.exactBaseType != "":
					gotType = dataSt.exactBaseType
				}
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("Cannot save %s without an explicit format; write 'save <path> <data> as json|csv|bytes'", gotType),
					"Add 'as json', 'as csv', or 'as bytes' after the data argument.",
				)
			}
		}
		if !v.inErrorTestExpr && ex.Module == "" && (ex.Callee == "sum" || ex.Callee == "avg") && len(ex.Args) >= 1 {
			argSt := v.inferExprTypeState(ex.Args[0])
			if (argSt.listElemType != "" && argSt.listElemType != "number") || argSt.exactRecord != "" {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("%s requires a list of numbers", ex.Callee),
					fmt.Sprintf("Pass a numeric list to '%s'.", ex.Callee),
				)
			}
		}
		if !v.inErrorTestExpr && ex.Module == "" && ex.Callee == "put" && len(ex.Args) == 2 {
			inSt := v.inferExprTypeState(ex.Args[0])
			if inSt.inboxElemType == "" {
				if inSt.exactRecord != "" || inSt.exactNonRecord || inSt.unionName != "" || inSt.listElemType != "" || inSt.exactBaseType != "" {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						"'put' requires an inbox as first argument",
						"Create an inbox with 'ch = inbox as <type>' and write 'put ch val'.",
					)
				}
			} else if err := v.checkInboxElementType(inSt.inboxElemType, ex.Args[1]); err != nil {
				return err
			}
		}
		if !v.inErrorTestExpr && ex.Module == "" && ex.Callee == "close" && len(ex.Args) == 1 {
			inSt := v.inferExprTypeState(ex.Args[0])
			if inSt.inboxElemType == "" {
				if inSt.exactRecord != "" || inSt.exactNonRecord || inSt.unionName != "" || inSt.listElemType != "" || inSt.exactBaseType != "" {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						"'close' requires an inbox",
						"Pass an inbox created with 'inbox as <type>' to 'close'.",
					)
				}
			}
		}
		for _, arg := range ex.Args {
			if err := v.checkExpr(arg); err != nil {
				return err
			}
			if err := v.checkNotCondition(arg); err != nil {
				return err
			}
			if err := v.checkNotVoidValue(arg); err != nil {
				return err
			}
		}
	}
	return nil
}

func isConditionExpr(e Expr) bool {
	switch ex := e.(type) {
	case *TypeCheckExpr:
		return true
	case *CallExpr:
		return ex.Callee == "contains"
	case *UnaryExpr:
		return ex.Op == "not"
	case *BinaryExpr:
		switch ex.Op {
		case ">", "<", ">=", "<=", "is", "is not", "and", "or":
			return true
		}
	}
	return false
}

func (v *typeValidator) checkNotCondition(e Expr) error {
	if v.inErrorTestExpr || e == nil || !isConditionExpr(e) {
		return nil
	}
	line, col := e.Pos()
	return newError(
		"TypeError",
		line,
		col,
		v.source,
		"Conditions cannot be stored or printed. Use a union type instead, e.g.: def result = \"yes\" | \"no\"",
		"Evaluate conditions directly inside 'check' or 'test', and return/store explicit union variants instead.",
	)
}

func (v *typeValidator) staticConcreteType(st varTypeState) string {
	return v.staticTypeKind(st, false)
}
