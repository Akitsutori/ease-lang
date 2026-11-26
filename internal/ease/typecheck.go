package ease

import (
	"fmt"
	"sort"
	"strings"
)

type typeDefInfo struct {
	line     int
	imported bool
	isRecord bool
}

type modTypeInfo struct {
	alias    string
	origName string
	source   string
	prog     *Program
	defs     map[string]*DefDecl
	unions   map[string]*UnionDecl
	funcs    map[string]*FuncDecl
	consts   map[string]Expr
	modules  map[string]modTypeInfo
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
	unionName            string
	exactRecord          string
	exactNonRecord       bool
	exactBaseType        string
	listElemType         string
	inboxElemType        string
	isParam              bool
	fromParam            bool
	excludedRecords      map[string]bool
	inlineUnionLabel     string
	inlineUnionVariants  []resolvedUnionVariant
	hasRemainingTracking bool
	remainingVariants    []resolvedUnionVariant
	knownVariants        []resolvedUnionVariant
}

func (s varTypeState) clone() varTypeState {
	out := s
	if len(s.excludedRecords) > 0 {
		out.excludedRecords = make(map[string]bool, len(s.excludedRecords))
		for k, v := range s.excludedRecords {
			out.excludedRecords[k] = v
		}
	}
	if len(s.remainingVariants) > 0 {
		out.remainingVariants = append([]resolvedUnionVariant(nil), s.remainingVariants...)
	}
	if len(s.knownVariants) > 0 {
		out.knownVariants = append([]resolvedUnionVariant(nil), s.knownVariants...)
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
	return validateProgramTypesWithOptions(prog, source, allowInternalBuiltins, false)
}

func validateProgramTypesWithOptions(prog *Program, source string, allowInternalBuiltins bool, allowTrailingValueExpr bool) error {
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
			source:   imp.ModuleSource,
			prog:     imp.ModuleProg,
			defs:     make(map[string]*DefDecl, len(imp.ModuleProg.Defs)),
			unions:   make(map[string]*UnionDecl, len(imp.ModuleProg.Unions)),
			funcs:    make(map[string]*FuncDecl, len(imp.ModuleProg.Functions)),
			consts:   make(map[string]Expr),
			modules:  collectSubModules(imp.ModuleProg),
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

	rootScope := newTypeScope(nil)
	v := &typeValidator{
		source:                 source,
		known:                  known,
		defs:                   defs,
		unions:                 unions,
		consts:                 consts,
		constExprs:             constExprs,
		funcNames:              funcNames,
		funcDecls:              funcDecls,
		modules:                modules,
		resolvedUnions:         make(map[string][]resolvedUnionVariant, len(unions)),
		globalScope:            rootScope,
		scope:                  rootScope,
		allowInternalBuiltins:  allowInternalBuiltins,
		allowTrailingValueExpr: allowTrailingValueExpr,
		spawnFuncCache:         make(map[string]bool),
		activeCallSpecs:        make(map[string]bool),
		verifiedCallSpecs:      make(map[string]bool),
		activeReturnInfer:      make(map[string]bool),
	}

	if err := v.resolveAndValidateUnions(prog); err != nil {
		return err
	}

	if err := v.checkRecordCycles(prog); err != nil {
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

	sortedConsts, err := toposortConsts(prog.Body, source)
	if err != nil {
		return err
	}
	for _, cs := range sortedConsts {
		if err := v.checkNameNotCallable(cs.Name, cs.Line, cs.Col, "constant"); err != nil {
			return err
		}
		if _, isType := v.known[cs.Name]; isType {
			return newError(
				"TypeError",
				cs.Line,
				cs.Col,
				source,
				fmt.Sprintf("Cannot define constant %q: it conflicts with a 'def' type", cs.Name),
				"Choose a different constant name.",
			)
		}
		if err := v.checkExpr(cs.Value); err != nil {
			return err
		}
		if err := v.checkNotCondition(cs.Value); err != nil {
			return err
		}
		if err := v.checkNotVoidValue(cs.Value); err != nil {
			return err
		}
		if err := v.checkPureConstExpr(cs.Value); err != nil {
			return err
		}
		valSt := v.inferExprTypeState(cs.Value)
		valSt.isParam = false
		v.globalScope.vars[cs.Name] = valSt
	}

	v.isTopLevelBody = true
	if err := v.checkStmts(prog.Body, false); err != nil {
		return err
	}
	v.isTopLevelBody = false

	for _, fn := range prog.Functions {
		for _, p := range fn.Params {
			if err := v.checkNameNotCallable(p, fn.Line, fn.Col, "parameter"); err != nil {
				return err
			}
		}
		if fn.ReturnType != "" {
			if fn.ReturnElemType != "" {
				if err := v.validateTypeTarget(fn.ReturnElemType, fn.ReturnTypeLine, fn.ReturnTypeCol, "as list of", false); err != nil {
					return err
				}
			} else {
				if err := v.validateTypeTarget(fn.ReturnType, fn.ReturnTypeLine, fn.ReturnTypeCol, "as", false); err != nil {
					return err
				}
			}
		}
		if !stmtsHaveReturn(fn.Body) && len(fn.Body) == 0 {
			if fn.ReturnType != "" {
				return newError(
					"TypeError",
					fn.Line,
					fn.Col,
					v.source,
					fmt.Sprintf("Function '%s' declares return type '%s' but does not return a value", fn.Name, formatReturnTypeDisplay(fn.ReturnType, fn.ReturnElemType)),
					fmt.Sprintf("Return a value with '-> <expr>' or remove 'as %s' from the function header.", formatReturnTypeDisplay(fn.ReturnType, fn.ReturnElemType)),
				)
			}
			return newError(
				"TypeError",
				fn.Line,
				fn.Col,
				v.source,
				fmt.Sprintf("Procedure '%s' has an empty body and no '->' return", fn.Name),
				fmt.Sprintf("Add statements with side effects (e.g. 'say', 'put', 'close') or return a value with '-> <expr>' in 'to %s'.", fn.Name),
			)
		}
		fnScope := v.pushScope()
		v.inferFuncUnionParams(fn, fnScope)
		for _, p := range fn.Params {
			st := fnScope.vars[p]
			st.isParam = true
			st.fromParam = !hasConcreteTypeInfo(st)
			fnScope.vars[p] = st
		}
		prevRetFn := v.currentReturnFunc
		v.currentReturnFunc = fn
		v.isFuncBodyTop = true
		err := v.checkStmts(fn.Body, true)
		v.isFuncBodyTop = false
		v.currentReturnFunc = prevRetFn
		if err != nil {
			v.popScope()
			return err
		}
		if err := v.checkUnusedLocals(fn.Body, fn.Params); err != nil {
			v.popScope()
			return err
		}
		if err := v.checkFuncReturnConsistency(fn, fnScope); err != nil {
			v.popScope()
			return err
		}
		v.popScope()
	}

	for _, chk := range prog.Checks {
		v.pushScope()
		v.inTestBlock = true
		err := v.checkStmts(chk.Body, false)
		v.inTestBlock = false
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
	"screen_clear":   "screen:clear",
	"screen_draw":    "screen:draw",
	"screen_show":    "screen:show",
	"rand_roll":      "random:roll",
	"rand_pick":      "random:pick",
	"rand_shuffle":   "random:shuffle",
	"synth_play":     "synth:play",
	"synth_stop":     "synth:stop",
	"sys_args":       "system.arguments",
	"sys_exit":       "system:exit",
	"sys_run":        "system:run",
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
		case "synth_stop":
			return "synth:stop", true
		case "screen_open":
			return "screen:open", true
		case "screen_clear":
			return "screen:clear", true
		case "screen_draw":
			return "screen:draw", true
		case "net_reply_text":
			return "network:reply_text", true
		case "net_reply_json":
			return "network:reply_json", true
		case "net_send":
			return "network:tcp_send", true
		case "sys_exit":
			return "system:exit", true
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
			if ce.Callee == "play" || ce.Callee == "stop" {
				return "synth:" + ce.Callee, true
			}
		case "screen":
			if ce.Callee == "open" || ce.Callee == "clear" || ce.Callee == "draw" {
				return "screen:" + ce.Callee, true
			}
		case "network":
			switch ce.Callee {
			case "reply_text", "reply_json", "tcp_send":
				return "network:" + ce.Callee, true
			}
		case "system":
			if ce.Callee == "exit" {
				return "system:exit", true
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
	if name == "stop" || name == "return" {
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Cannot name %s %q: reserved for '-> %s' control flow", kind, name, name),
			fmt.Sprintf("Choose a different %s name so '-> %s' remains unambiguous.", kind, name),
		)
	}
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
	if kind != "constant" {
		if _, isConst := v.consts[name]; isConst {
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Cannot reassign or shadow immutable 'def' constant %q", name),
				fmt.Sprintf("Use a different %s name; 'def %s = ...' constants are immutable.", kind, name),
			)
		}
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

func (v *typeValidator) finiteVariantsOf(st varTypeState) []resolvedUnionVariant {
	if len(st.knownVariants) > 0 {
		return st.knownVariants
	}
	if st.unionName != "" {
		if vars, ok := v.resolveUnionVariantsByName(st.unionName); ok && len(vars) > 0 {
			return vars
		}
	}
	if len(st.inlineUnionVariants) > 0 {
		return st.inlineUnionVariants
	}
	if st.exactRecord != "" {
		return []resolvedUnionVariant{{kind: variantRecord, recordName: st.exactRecord}}
	}
	return nil
}

func mergeVariantSlices(a, b []resolvedUnionVariant) []resolvedUnionVariant {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]resolvedUnionVariant, 0, len(a)+len(b))
	for _, vr := range a {
		k := vr.key()
		if !seen[k] {
			seen[k] = true
			out = append(out, vr)
		}
	}
	for _, vr := range b {
		k := vr.key()
		if !seen[k] {
			seen[k] = true
			out = append(out, vr)
		}
	}
	return out
}

func (v *typeValidator) setVarInCurrentOrExisting(name string, st varTypeState) {
	for cur := v.scope; cur != nil; cur = cur.parent {
		if prev, ok := cur.vars[name]; ok {
			if cur != v.scope {
				prevVars := v.finiteVariantsOf(prev)
				stVars := v.finiteVariantsOf(st)
				merged := st.clone()
				if len(prevVars) > 0 && len(stVars) > 0 {
					merged.knownVariants = mergeVariantSlices(prevVars, stVars)
					if prev.unionName != "" && prev.unionName == st.unionName {
						merged.unionName = prev.unionName
					} else {
						merged.unionName = ""
					}
				} else {
					merged.knownVariants = nil
					if prev.unionName != st.unionName {
						merged.unionName = ""
					}
				}
				cur.vars[name] = merged
				return
			}
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
	if strings.HasPrefix(name, "list<") && strings.HasSuffix(name, ">") {
		return varTypeState{listElemType: name[5 : len(name)-1]}
	}
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
	case *ProjectExpr:
		projScope := v.pushScope()
		projScope.vars["it"] = v.inferIterableElemState(ex.Source)
		v.loopDepth++
		bodySt := v.inferExprTypeState(ex.Body)
		v.loopDepth--
		v.popScope()
		if bodySt.exactRecord != "" {
			return varTypeState{listElemType: bodySt.exactRecord}
		}
		if bodySt.unionName != "" {
			return varTypeState{listElemType: bodySt.unionName}
		}
		if bodySt.exactBaseType != "" {
			return varTypeState{listElemType: bodySt.exactBaseType}
		}
		if bodySt.listElemType != "" {
			return varTypeState{listElemType: "list<" + bodySt.listElemType + ">"}
		}
		if kind := v.staticTypeKind(bodySt, false); kind != "" {
			return varTypeState{listElemType: kind}
		}
		return varTypeState{listElemType: "any"}
	case *UpdateExpr:
		return v.inferExprTypeState(ex.Target)
	case *SortByExpr:
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
					unionName:           v.fieldNamedUnion(targetSt.exactRecord, ex.Field),
					inlineUnionLabel:    label,
					inlineUnionVariants: variants,
				}
			}
			if d := v.defs[targetSt.exactRecord]; d != nil {
				for _, f := range d.Fields {
					if f.Name == ex.Field && len(f.Allowed) == 0 && f.Default != nil {
						st := v.inferExprTypeState(f.Default)
						if dot := strings.IndexByte(targetSt.exactRecord, '.'); dot >= 0 {
							alias := targetSt.exactRecord[:dot]
							if mod, ok := v.modules[alias]; ok {
								st = prefixVarTypeState(st, alias, mod.defs, mod.unions)
							}
						}
						return st
					}
				}
			}
		}
	case *IndexExpr:
		targetSt := v.inferExprTypeState(ex.Target)
		if targetSt.listElemType != "" {
			return v.typeStateFromName(targetSt.listElemType)
		}
	case *StringExpr:
		return varTypeState{
			exactNonRecord: true,
			exactBaseType:  "text",
			knownVariants:  []resolvedUnionVariant{{kind: variantStringLit, strVal: ex.Value}},
		}
	case *InterpStringExpr:
		return varTypeState{exactNonRecord: true, exactBaseType: "text"}
	case *NumberExpr:
		return varTypeState{
			exactNonRecord: true,
			exactBaseType:  "number",
			knownVariants:  []resolvedUnionVariant{{kind: variantNumberLit, numVal: ex.Value, rawNum: ex.Raw}},
		}
	case *UnaryExpr:
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
			if mod, ok := v.modules[ex.Module]; ok && mod.origName == "system" && ex.Callee == "run" {
				return varTypeState{exactRecord: ex.Module + ".process_report"}
			}
			if mod, ok := v.modules[ex.Module]; ok && mod.origName == "screen" && ex.Callee == "show" {
				return varTypeState{exactRecord: ex.Module + ".frame_state"}
			}
			if mod, ok := v.modules[ex.Module]; ok && mod.origName == "random" {
				switch ex.Callee {
				case "sys_args":
					return varTypeState{listElemType: "text"}
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
			if mod, ok := v.modules[ex.Module]; ok && mod.origName == "" {
				if fn := mod.funcs[ex.Callee]; fn != nil && len(ex.Args) == len(fn.Params) {
					argStates := make([]varTypeState, len(ex.Args))
					for i, arg := range ex.Args {
						argStates[i] = v.inferExprTypeState(arg)
					}
					return v.inferFuncReturnState(ex.Module, fn, argStates)
				}
			}
			return varTypeState{}
		}
		switch ex.Callee {
		case "sys_args":
			return varTypeState{listElemType: "text"}
		case "sys_run":
			return varTypeState{exactRecord: "process_report"}
		case "screen_show":
			return varTypeState{exactRecord: "frame_state"}
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
		default:
			if fn := v.funcDecls[ex.Callee]; fn != nil && len(ex.Args) == len(fn.Params) {
				argStates := make([]varTypeState, len(ex.Args))
				for i, arg := range ex.Args {
					argStates[i] = v.inferExprTypeState(arg)
				}
				return v.inferFuncReturnState("", fn, argStates)
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

func (v *typeValidator) checkContainsCall(ex *CallExpr) error {
	cSt := v.inferExprTypeState(ex.Args[0])
	iSt := v.inferExprTypeState(ex.Args[1])

	if cSt.exactRecord != "" || cSt.exactBaseType == "number" || cSt.inboxElemType != "" {
		got := v.staticConcreteType(cSt)
		if got == "" {
			got = "non-container"
		}
		return newError(
			"TypeError",
			ex.Line,
			ex.Col,
			v.source,
			fmt.Sprintf("contains requires text or a list of atomic values, got %s", got),
			"Example: check contains roles \"admin\" -> ...",
		)
	}
	if cSt.listElemType == "" && cSt.unionName != "" {
		if uVars, ok := v.resolvedUnions[cSt.unionName]; ok && !v.unionContainsOnlyBaseType(uVars, "text") {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				fmt.Sprintf("contains requires text or a list of atomic values, got %s", cSt.unionName),
				"Example: check contains roles \"admin\" -> ...",
			)
		}
	}

	if cSt.exactBaseType == "text" {
		if iSt.exactRecord != "" || iSt.listElemType != "" || iSt.inboxElemType != "" ||
			(iSt.exactBaseType != "" && iSt.exactBaseType != "text") {
			got := v.staticConcreteType(iSt)
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				fmt.Sprintf("contains on text requires a text argument, got %s", got),
				"Example: check contains email \"@example.com\" -> ...",
			)
		}
		if uVars := v.unionVariantsForTypeState(iSt); len(uVars) > 0 && !v.unionContainsOnlyBaseType(uVars, "text") {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				"contains on text requires a text argument",
				"Example: check contains email \"@example.com\" -> ...",
			)
		}
		return nil
	}

	if cSt.listElemType != "" {
		if !v.isAtomicElemType(cSt.listElemType) {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				fmt.Sprintf("Cannot use 'contains' on list of %s; 'contains' only supports text or lists of atomic values", cSt.listElemType),
				"Use 'count (xs where it is ...) > 0' to query record lists.",
			)
		}
		if (cSt.listElemType == "number" || cSt.listElemType == "text") && iSt.exactBaseType != "" && iSt.exactBaseType != cSt.listElemType {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				fmt.Sprintf("Cannot check 'contains' for %s in list of %s", iSt.exactBaseType, cSt.listElemType),
				fmt.Sprintf("Provide a value of type %q.", cSt.listElemType),
			)
		}
		return v.checkTypedElementValue(cSt.listElemType, ex.Args[1], "check 'contains'", "list")
	}

	// Empty untyped list (e.g. '[]' as json) or unknown list: still reject known non-atomic items.
	if iSt.exactRecord != "" || iSt.listElemType != "" || iSt.inboxElemType != "" {
		got := v.staticConcreteType(iSt)
		return newError(
			"TypeError",
			ex.Line,
			ex.Col,
			v.source,
			fmt.Sprintf("Cannot use 'contains' with non-atomic %s", got),
			"Use 'count (xs where it is ...) > 0' to query record lists.",
		)
	}
	if uVars := v.unionVariantsForTypeState(iSt); len(uVars) > 0 {
		for _, vr := range uVars {
			if vr.kind == variantRecord {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("Cannot use 'contains' with non-atomic %s", vr.display()),
					"Use 'count (xs where it is ...) > 0' to query record lists.",
				)
			}
		}
	}
	return nil
}

func (v *typeValidator) isAtomicElemType(elemType string) bool {
	switch elemType {
	case "", "number", "text":
		return true
	case "list", "map", "inbox":
		return false
	default:
		if v.defs[elemType] != nil {
			return false
		}
		if uVars, isU := v.resolvedUnions[elemType]; isU {
			for _, vr := range uVars {
				if vr.kind == variantRecord {
					return false
				}
			}
			return true
		}
		return false
	}
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
		if err := v.validateTypeTarget(ex.ElemType, ex.TypeLine, ex.TypeCol, "list as", requireRecord); err != nil {
			return err
		}
		if !v.inErrorTestExpr && len(ex.Rows) > 0 && v.defs[ex.ElemType] != nil {
			for _, row := range ex.Rows {
				if err := v.checkRecordWithUpdates(ex.ElemType, row, ex.Line, ex.Col); err != nil {
					return err
				}
			}
		}
		return nil

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
			if err := v.checkMustBeCondition(ex.Right, "not"); err != nil {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					"'not' requires a condition",
					"Example: not (x is 0) or not (contains txt \"a\")",
				)
			}
			return nil
		}
		return v.checkNotCondition(ex.Right)

	case *BinaryExpr:
		if err := v.checkExpr(ex.Left); err != nil {
			return err
		}
		if ex.Op == "and" || ex.Op == "or" {
			if err := v.checkMustBeCondition(ex.Left, ex.Op); err != nil {
				return err
			}
			rhsScope := v.pushScope()
			v.applyCondNarrowing(rhsScope, ex.Left, ex.Op == "and")
			err := v.checkExpr(ex.Right)
			v.popScope()
			if err != nil {
				return err
			}
			return v.checkMustBeCondition(ex.Right, ex.Op)
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
				if err := v.checkFieldAccessOnState(st, id.Name, ex); err != nil {
					return err
				}
			}
		} else {
			targetSt := v.inferExprTypeState(ex.Target)
			if err := v.checkFieldAccessOnState(targetSt, "", ex); err != nil {
				return err
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
			if !v.inErrorTestExpr {
				if baseSt.listElemType == "number" || baseSt.listElemType == "text" {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						fmt.Sprintf("Cannot use 'with' on list of %s", baseSt.listElemType),
						"'with' on a list only supports '.<index> = <val>'; use 'update <list> with ...' for record lists.",
					)
				}
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					"Cannot use bare 'with' on a list of records",
					"Write 'update <list> with <field> = <val>' to update all items, or 'update <list> where <cond> with <field> = <val>' to update matching items.",
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
		if !v.inErrorTestExpr && baseSt.exactRecord != "" && v.defs[baseSt.exactRecord] != nil {
			return v.checkRecordWithUpdates(baseSt.exactRecord, ex.Updates, ex.Line, ex.Col)
		}

	case *UpdateExpr:
		if err := v.checkExpr(ex.Target); err != nil {
			return err
		}
		if err := v.checkNotCondition(ex.Target); err != nil {
			return err
		}
		if err := v.checkNotVoidValue(ex.Target); err != nil {
			return err
		}
		targetSt := v.inferExprTypeState(ex.Target)
		if !v.inErrorTestExpr {
			if targetSt.exactRecord != "" || targetSt.exactNonRecord || targetSt.unionName != "" || targetSt.inboxElemType != "" {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					"Cannot use 'update' on non-list value",
					"'update' requires a list of 'def' records, e.g.: update users where it.role is \"guest\" with role = \"member\".",
				)
			}
			if targetSt.listElemType != "" && v.defs[targetSt.listElemType] == nil {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("'update' requires a list of 'def' records, got list of %s", targetSt.listElemType),
					"Construct a record list with 'list as <def> with ... end' or 'list rec1, rec2'.",
				)
			}
		}
		updateScope := v.pushScope()
		if targetSt.listElemType != "" {
			updateScope.vars["it"] = v.typeStateFromName(targetSt.listElemType)
		} else {
			updateScope.vars["it"] = varTypeState{}
		}
		v.loopDepth++
		var updErr error
		if ex.Where != nil {
			if err := v.checkExpr(ex.Where); err != nil {
				updErr = err
			} else if err := v.checkMustBeCondition(ex.Where, "update ... where"); err != nil {
				updErr = err
			} else {
				v.applyCondNarrowing(updateScope, ex.Where, true)
			}
		}
		if updErr == nil {
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
		}
		if updErr == nil && !v.inErrorTestExpr && targetSt.listElemType != "" && v.defs[targetSt.listElemType] != nil {
			updErr = v.checkRecordWithUpdates(targetSt.listElemType, ex.Updates, ex.Line, ex.Col)
		}
		v.loopDepth--
		v.popScope()
		return updErr

	case *SortByExpr:
		if err := v.checkExpr(ex.Target); err != nil {
			return err
		}
		if err := v.checkNotCondition(ex.Target); err != nil {
			return err
		}
		if err := v.checkNotVoidValue(ex.Target); err != nil {
			return err
		}
		targetSt := v.inferExprTypeState(ex.Target)
		if !v.inErrorTestExpr {
			if targetSt.exactRecord != "" || targetSt.exactNonRecord || targetSt.unionName != "" || targetSt.inboxElemType != "" {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					"'sort ... by' requires a list",
					"Write: sort <list> by <key1> [, <key2> ...]",
				)
			}
			if targetSt.listElemType == "list" {
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					"Cannot sort a list of lists",
					"Sort a list of records, maps, numbers, or text.",
				)
			}
		}
		sortScope := v.pushScope()
		if targetSt.listElemType != "" {
			sortScope.vars["it"] = v.typeStateFromName(targetSt.listElemType)
		} else {
			sortScope.vars["it"] = varTypeState{}
		}
		v.loopDepth++
		var sortErr error
		for _, k := range ex.Keys {
			if err := v.checkExpr(k); err != nil {
				sortErr = err
				break
			}
			if err := v.checkNotCondition(k); err != nil {
				sortErr = err
				break
			}
			if err := v.checkNotVoidValue(k); err != nil {
				sortErr = err
				break
			}
			if !v.inErrorTestExpr {
				kSt := v.inferExprTypeState(k)
				kLine, kCol := k.Pos()
				if kSt.exactRecord != "" || kSt.listElemType != "" || kSt.inboxElemType != "" {
					sortErr = newError(
						"TypeError",
						kLine,
						kCol,
						v.source,
						fmt.Sprintf("'sort ... by' key must be number, text, or a scalar union, got %s", v.staticConcreteType(kSt)),
						"Project a scalar field or expression (e.g. 'it.score', 'it.name', or 'it.priority').",
					)
					break
				}
				var uVars []resolvedUnionVariant
				if len(kSt.inlineUnionVariants) > 0 {
					uVars = kSt.inlineUnionVariants
				} else if kSt.unionName != "" {
					uVars = v.resolvedUnions[kSt.unionName]
				}
				if len(uVars) > 0 {
					hasNum, hasText, hasRec := false, false, false
					for _, uv := range uVars {
						switch uv.kind {
						case variantRecord:
							hasRec = true
						case variantNumberLit:
							hasNum = true
						case variantStringLit:
							hasText = true
						case variantBaseType:
							if uv.baseType == "number" {
								hasNum = true
							} else if uv.baseType == "text" {
								hasText = true
							}
						}
					}
					if hasRec || (hasNum && hasText) {
						sortErr = newError(
							"TypeError",
							kLine,
							kCol,
							v.source,
							"Cannot sort by mixed or record-containing union key",
							"Project a pure number, text, or single-kind scalar union key.",
						)
						break
					}
				}
			}
		}
		v.loopDepth--
		v.popScope()
		return sortErr

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
			if err := v.checkMustBeCondition(cond, "where"); err != nil {
				condErr = err
				break
			}
			v.applyCondNarrowing(whereScope, cond, true)
		}
		v.loopDepth--
		v.popScope()
		return condErr

	case *ProjectExpr:
		if err := v.checkExpr(ex.Source); err != nil {
			return err
		}
		sourceSt := v.inferExprTypeState(ex.Source)
		if !v.inErrorTestExpr && (sourceSt.exactRecord != "" || sourceSt.exactNonRecord || sourceSt.unionName != "" || sourceSt.inboxElemType != "") {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				"Cannot project non-list value with '->'",
				"List projection '<list> -> <expr>' requires a list on the left.",
			)
		}
		projScope := v.pushScope()
		projScope.vars["it"] = v.inferIterableElemState(ex.Source)
		v.loopDepth++
		var bodyErr error
		if err := v.checkExpr(ex.Body); err != nil {
			bodyErr = err
		} else if err := v.checkNotCondition(ex.Body); err != nil {
			bodyErr = err
		} else if err := v.checkNotVoidValue(ex.Body); err != nil {
			bodyErr = err
		}
		v.loopDepth--
		v.popScope()
		return bodyErr

	case *QuantifierExpr:
		if err := v.checkExpr(ex.Source); err != nil {
			return err
		}
		sourceSt := v.inferExprTypeState(ex.Source)
		if !v.inErrorTestExpr && (sourceSt.exactRecord != "" || sourceSt.exactNonRecord || sourceSt.unionName != "" || sourceSt.inboxElemType != "") {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				fmt.Sprintf("Cannot use '%s' on non-list value", ex.Kind),
				fmt.Sprintf("'%s <list> where <cond>' requires a list.", ex.Kind),
			)
		}
		qScope := v.pushScope()
		qScope.vars["it"] = v.inferIterableElemState(ex.Source)
		v.loopDepth++
		var condErr error
		if err := v.checkExpr(ex.Cond); err != nil {
			condErr = err
		} else if err := v.checkMustBeCondition(ex.Cond, ex.Kind); err != nil {
			condErr = err
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
			if ex.Name == "continue" {
				if _, ok := v.lookupVar("continue"); !ok {
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						`Unknown variable "continue"`,
						"Ease has no 'continue' keyword; wrap the remaining loop body in 'check <inverted_cond> ... end' instead.",
					)
				}
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
			if !v.inErrorTestExpr && mod.origName == "screen" {
				if ex.Callee == "clear" && len(ex.Args) >= 2 {
					bgSt := v.inferExprTypeState(ex.Args[1])
					if bgSt.exactBaseType != "" && bgSt.exactBaseType != "text" {
						return newError(
							"TypeError",
							ex.Line,
							ex.Col,
							v.source,
							fmt.Sprintf("screen:clear requires a text color, got %s", bgSt.exactBaseType),
							"Pass a color name or hex string, e.g. 'screen:clear win \"#0f172a\"'.",
						)
					}
				}
				if ex.Callee == "draw" && len(ex.Args) >= 2 {
					isDrawableName := func(n string) bool {
						base := n
						if dot := strings.LastIndexByte(base, '.'); dot >= 0 {
							base = base[dot+1:]
						}
						switch base {
						case "box", "circle", "line", "triangle", "label", "shape", "drawable":
							return true
						}
						return false
					}
					dSt := v.inferExprTypeState(ex.Args[1])
					if dSt.exactBaseType != "" {
						return newError(
							"TypeError",
							ex.Line,
							ex.Col,
							v.source,
							fmt.Sprintf("screen:draw requires a screen drawable or list of drawables, got %s", dSt.exactBaseType),
							"Draw a screen.box, circle, line, triangle, label, or a list of them.",
						)
					}
					if dSt.exactRecord != "" && !isDrawableName(dSt.exactRecord) {
						return newError(
							"TypeError",
							ex.Line,
							ex.Col,
							v.source,
							fmt.Sprintf("screen:draw cannot draw record %q", dSt.exactRecord),
							"Draw a screen.box, circle, line, triangle, or label.",
						)
					}
					if dSt.listElemType != "" && !isDrawableName(dSt.listElemType) {
						return newError(
							"TypeError",
							ex.Line,
							ex.Col,
							v.source,
							fmt.Sprintf("screen:draw requires a list of drawables, got list of %s", dSt.listElemType),
							"Pass a list of screen.box, circle, line, triangle, label, or screen.drawable.",
						)
					}
				}
			}
		}
		isSystemExitCall := (ex.Module == "" && ex.Callee == "sys_exit")
		if !isSystemExitCall && ex.Module != "" {
			if mod, isMod := v.modules[ex.Module]; isMod && mod.origName == "system" && ex.Callee == "exit" {
				isSystemExitCall = true
			}
		}
		if v.swarmDepth > 0 && !v.inErrorTestExpr && isSystemExitCall {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				"'system:exit' cannot be used inside 'swarm loop'",
				"Return an outcome union/record from 'swarm loop' and call 'system:exit' at the top level.",
			)
		}
		if !v.inErrorTestExpr && len(ex.Args) == 1 && isSystemExitCall {
			argSt := v.inferExprTypeState(ex.Args[0])
			if argSt.exactRecord != "" || argSt.listElemType != "" || argSt.inboxElemType != "" || (argSt.exactBaseType != "" && argSt.exactBaseType != "number" && argSt.exactBaseType != "text") {
				gotType := v.staticConcreteType(argSt)
				if gotType == "" {
					gotType = "value"
				}
				return newError(
					"TypeError",
					ex.Line,
					ex.Col,
					v.source,
					fmt.Sprintf("'system:exit' expects a number (0..255, e.g. 'system.ok') or a non-empty text message, got %s", gotType),
					"Example: system:exit system.ok or system:exit \"Missing config\"",
				)
			}
		}
		if !v.inErrorTestExpr && len(ex.Args) == 1 && ex.Module != "" {
			if mod, isMod := v.modules[ex.Module]; isMod && mod.origName == "system" && ex.Callee == "run" {
				argSt := v.inferExprTypeState(ex.Args[0])
				wantRec := ex.Module + ".command"
				if argSt.exactNonRecord || argSt.exactBaseType != "" || argSt.listElemType != "" || argSt.inboxElemType != "" || (argSt.exactRecord != "" && argSt.exactRecord != wantRec) {
					gotType := v.staticConcreteType(argSt)
					if gotType == "" {
						gotType = "value"
					}
					return newError(
						"TypeError",
						ex.Line,
						ex.Col,
						v.source,
						fmt.Sprintf("'system:run' expects a '%s' record, got %s", wantRec, gotType),
						fmt.Sprintf("Pass '%s with program = \"...\"' to 'system:run'.", wantRec),
					)
				}
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
		if !v.inErrorTestExpr && ex.Module == "" && ex.Callee == "contains" && len(ex.Args) == 2 {
			if err := v.checkContainsCall(ex); err != nil {
				return err
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
		if !v.inErrorTestExpr {
			if err := v.checkCallSiteSpecialization(ex); err != nil {
				return err
			}
		}
	}
	return nil
}

func collectSubModules(prog *Program) map[string]modTypeInfo {
	if prog == nil || len(prog.Imports) == 0 {
		return nil
	}
	out := make(map[string]modTypeInfo, len(prog.Imports))
	for _, imp := range prog.Imports {
		if imp.ModuleProg == nil {
			continue
		}
		alias := imp.Alias
		if alias == "" {
			alias = imp.Name
		}
		origName := ""
		if !imp.IsFileImport {
			origName = imp.Name
		}
		sub := modTypeInfo{
			alias:    alias,
			origName: origName,
			source:   imp.ModuleSource,
			prog:     imp.ModuleProg,
			defs:     make(map[string]*DefDecl, len(imp.ModuleProg.Defs)),
			unions:   make(map[string]*UnionDecl, len(imp.ModuleProg.Unions)),
			funcs:    make(map[string]*FuncDecl, len(imp.ModuleProg.Functions)),
			consts:   make(map[string]Expr),
			modules:  collectSubModules(imp.ModuleProg),
		}
		for _, d := range imp.ModuleProg.Defs {
			sub.defs[d.Name] = d
		}
		for _, u := range imp.ModuleProg.Unions {
			sub.unions[u.Name] = u
		}
		for _, fn := range imp.ModuleProg.Functions {
			sub.funcs[fn.Name] = fn
		}
		for _, s := range imp.ModuleProg.Body {
			if cs, ok := s.(*ConstDeclStmt); ok {
				sub.consts[cs.Name] = cs.Value
			}
		}
		out[alias] = sub
	}
	return out
}

func hasConcreteTypeInfo(st varTypeState) bool {
	return st.exactBaseType != "" ||
		st.exactRecord != "" ||
		st.unionName != "" ||
		st.listElemType != "" ||
		st.inboxElemType != "" ||
		st.inlineUnionLabel != ""
}

func varTypeStateSig(st varTypeState) string {
	return st.exactBaseType + "|" + st.exactRecord + "|" + st.unionName + "|" + st.listElemType + "|" + st.inboxElemType + "|" + st.inlineUnionLabel
}

func (v *typeValidator) buildModuleValidator(mod modTypeInfo) *typeValidator {
	known := make(map[string]typeDefInfo, len(mod.defs)+len(mod.unions))
	defs := make(map[string]*DefDecl, len(mod.defs))
	unions := make(map[string]*UnionDecl, len(mod.unions))
	consts := make(map[string]int, len(mod.consts))
	constExprs := make(map[string]Expr, len(mod.consts))
	funcNames := make(map[string]int, len(mod.funcs))
	funcDecls := make(map[string]*FuncDecl, len(mod.funcs))
	modules := make(map[string]modTypeInfo, len(mod.modules))

	for name, d := range mod.defs {
		known[name] = typeDefInfo{line: d.Line, imported: d.Imported, isRecord: true}
		defs[name] = d
	}
	for name, u := range mod.unions {
		known[name] = typeDefInfo{line: u.Line, imported: u.Imported, isRecord: false}
		unions[name] = u
	}
	for name, fn := range mod.funcs {
		funcNames[name] = fn.Line
		funcDecls[name] = fn
	}
	for name, expr := range mod.consts {
		consts[name] = 0
		constExprs[name] = expr
	}
	for alias, sub := range mod.modules {
		modules[alias] = sub
		for dName, d := range sub.defs {
			qual := alias + "." + dName
			known[qual] = typeDefInfo{line: d.Line, imported: true, isRecord: true}
			defs[qual] = d
		}
		for uName, u := range sub.unions {
			qual := alias + "." + uName
			known[qual] = typeDefInfo{line: u.Line, imported: true, isRecord: false}
			unions[qual] = u
		}
	}

	rootScope := newTypeScope(nil)
	mv := &typeValidator{
		source:                mod.source,
		known:                 known,
		defs:                  defs,
		unions:                unions,
		consts:                consts,
		constExprs:            constExprs,
		funcNames:             funcNames,
		funcDecls:             funcDecls,
		modules:               modules,
		resolvedUnions:        make(map[string][]resolvedUnionVariant, len(unions)),
		globalScope:           rootScope,
		scope:                 rootScope,
		allowInternalBuiltins: mod.origName != "",
		spawnFuncCache:        make(map[string]bool),
		activeCallSpecs:       v.activeCallSpecs,
		verifiedCallSpecs:     v.verifiedCallSpecs,
		activeReturnInfer:     v.activeReturnInfer,
		callSpecDepth:         v.callSpecDepth,
	}
	for name, u := range unions {
		_, _ = mv.resolveUnion(name, u, make(map[string]bool))
	}
	if mod.prog != nil {
		for _, s := range mod.prog.Body {
			switch st := s.(type) {
			case *ConstDeclStmt:
				mv.consts[st.Name] = st.Line
				mv.constExprs[st.Name] = st.Value
				mv.globalScope.vars[st.Name] = mv.inferExprTypeState(st.Value)
			case *AssignStmt:
				mv.globalScope.vars[st.Name] = mv.inferExprTypeState(st.Value)
			}
		}
	}
	return mv
}

func unprefixVarTypeState(st varTypeState, modAlias string) varTypeState {
	out := st.clone()
	prefix := modAlias + "."
	strip := func(s string) string {
		return strings.TrimPrefix(s, prefix)
	}
	out.exactRecord = strip(out.exactRecord)
	out.unionName = strip(out.unionName)
	out.listElemType = strip(out.listElemType)
	out.inboxElemType = strip(out.inboxElemType)
	out.inlineUnionLabel = strip(out.inlineUnionLabel)
	if len(out.excludedRecords) > 0 {
		nextExcl := make(map[string]bool, len(out.excludedRecords))
		for k, val := range out.excludedRecords {
			nextExcl[strip(k)] = val
		}
		out.excludedRecords = nextExcl
	}
	if len(out.inlineUnionVariants) > 0 {
		nextVars := make([]resolvedUnionVariant, len(out.inlineUnionVariants))
		for i, vr := range out.inlineUnionVariants {
			if vr.kind == variantRecord {
				vr.recordName = strip(vr.recordName)
			}
			nextVars[i] = vr
		}
		out.inlineUnionVariants = nextVars
	}
	if len(out.remainingVariants) > 0 {
		nextVars := make([]resolvedUnionVariant, len(out.remainingVariants))
		for i, vr := range out.remainingVariants {
			if vr.kind == variantRecord {
				vr.recordName = strip(vr.recordName)
			}
			nextVars[i] = vr
		}
		out.remainingVariants = nextVars
	}
	return out
}

func prefixVarTypeState(st varTypeState, modAlias string, modDefs map[string]*DefDecl, modUnions map[string]*UnionDecl) varTypeState {
	out := st.clone()
	prefix := modAlias + "."
	qualify := func(name string) string {
		if name == "" || strings.Contains(name, ".") {
			return name
		}
		if modDefs[name] != nil || modUnions[name] != nil {
			return prefix + name
		}
		return name
	}
	out.exactRecord = qualify(out.exactRecord)
	out.unionName = qualify(out.unionName)
	out.listElemType = qualify(out.listElemType)
	out.inboxElemType = qualify(out.inboxElemType)
	if out.inlineUnionLabel != "" && !strings.HasPrefix(out.inlineUnionLabel, prefix) {
		out.inlineUnionLabel = prefix + out.inlineUnionLabel
	}
	return out
}

func (v *typeValidator) checkCallSiteSpecialization(ex *CallExpr) error {
	var fn *FuncDecl
	var callName string
	var modInfo modTypeInfo
	isModCall := false
	if ex.Module == "" {
		fn = v.funcDecls[ex.Callee]
		callName = ex.Callee
	} else {
		mod, ok := v.modules[ex.Module]
		if !ok || mod.origName != "" {
			return nil
		}
		modInfo = mod
		isModCall = true
		fn = mod.funcs[ex.Callee]
		callName = ex.Module + ":" + ex.Callee
	}
	if fn == nil || len(fn.Params) == 0 || len(ex.Args) != len(fn.Params) {
		return nil
	}

	argStates := make([]varTypeState, len(ex.Args))
	hasAnyConcrete := false
	sig := callName + "("
	for i, arg := range ex.Args {
		argStates[i] = v.inferExprTypeState(arg)
		if hasConcreteTypeInfo(argStates[i]) {
			hasAnyConcrete = true
		}
		if i > 0 {
			sig += ","
		}
		sig += varTypeStateSig(argStates[i])
	}
	sig += ")"
	if !hasAnyConcrete {
		return nil
	}
	if v.activeCallSpecs[sig] || v.verifiedCallSpecs[sig] || v.callSpecDepth >= 16 {
		return nil
	}

	v.activeCallSpecs[sig] = true
	v.callSpecDepth++

	var err error
	if isModCall {
		targetV := v.buildModuleValidator(modInfo)
		fnScope := targetV.pushScope()
		targetV.inferFuncUnionParams(fn, fnScope)
		for i, p := range fn.Params {
			st := fnScope.vars[p]
			if hasConcreteTypeInfo(argStates[i]) {
				st = unprefixVarTypeState(argStates[i], ex.Module)
			}
			st.isParam = true
			st.fromParam = !hasConcreteTypeInfo(st)
			fnScope.vars[p] = st
		}
		targetV.currentReturnFunc = fn
		targetV.inSpecializedCall = true
		err = targetV.checkStmts(fn.Body, true)
	} else {
		// Save validator state so specialized check runs in an isolated scope.
		savedScope := v.scope
		savedLoopDepth := v.loopDepth
		savedSwarmDepth := v.swarmDepth
		savedDirectInSwarm := v.directInSwarm
		savedWorkerAssigned := v.workerAssigned
		savedWorkerOuter := v.workerOuterScope
		savedInErrorBlock := v.inErrorBlock
		savedRetFn := v.currentReturnFunc
		savedInSpec := v.inSpecializedCall

		v.scope = newTypeScope(v.globalScope)
		v.loopDepth = 0
		v.swarmDepth = 0
		v.directInSwarm = false
		v.workerAssigned = nil
		v.workerOuterScope = nil
		v.inErrorBlock = false
		v.currentReturnFunc = fn
		v.inSpecializedCall = true

		fnScope := v.pushScope()
		v.inferFuncUnionParams(fn, fnScope)
		for i, p := range fn.Params {
			st := fnScope.vars[p]
			if hasConcreteTypeInfo(argStates[i]) {
				st = argStates[i].clone()
			}
			st.isParam = true
			st.fromParam = !hasConcreteTypeInfo(st)
			fnScope.vars[p] = st
		}

		err = v.checkStmts(fn.Body, true)

		v.scope = savedScope
		v.loopDepth = savedLoopDepth
		v.swarmDepth = savedSwarmDepth
		v.directInSwarm = savedDirectInSwarm
		v.workerAssigned = savedWorkerAssigned
		v.workerOuterScope = savedWorkerOuter
		v.inErrorBlock = savedInErrorBlock
		v.currentReturnFunc = savedRetFn
		v.inSpecializedCall = savedInSpec
	}

	delete(v.activeCallSpecs, sig)
	v.callSpecDepth--

	if err != nil {
		if ee, ok := err.(*EaseError); ok {
			return newError(
				ee.Stage,
				ex.Line,
				ex.Col,
				v.source,
				fmt.Sprintf("In call to '%s': %s (in '%s' at line %d)", callName, ee.Message, fn.Name, ee.Line),
				ee.Hint,
			)
		}
		return err
	}
	v.verifiedCallSpecs[sig] = true
	return nil
}

func (v *typeValidator) inferFuncReturnState(modName string, fn *FuncDecl, argStates []varTypeState) varTypeState {
	if fn == nil {
		return varTypeState{}
	}
	if fn.ReturnType != "" {
		targetV := v
		var modInfo modTypeInfo
		isMod := false
		if modName != "" {
			if mod, ok := v.modules[modName]; ok && mod.origName == "" {
				modInfo = mod
				isMod = true
				targetV = v.buildModuleValidator(mod)
			}
		}
		out := targetV.returnTypeState(fn)
		if isMod {
			return prefixVarTypeState(out, modName, modInfo.defs, modInfo.unions)
		}
		return out
	}
	if !stmtsHaveReturn(fn.Body) {
		return varTypeState{}
	}
	key := modName + ":" + fn.Name + "("
	for i, st := range argStates {
		if i > 0 {
			key += ","
		}
		key += varTypeStateSig(st)
	}
	key += ")"
	if v.activeReturnInfer[key] || len(v.activeReturnInfer) >= 16 {
		return varTypeState{}
	}
	v.activeReturnInfer[key] = true
	defer delete(v.activeReturnInfer, key)

	targetV := v
	var modInfo modTypeInfo
	isMod := false
	if modName != "" {
		if mod, ok := v.modules[modName]; ok && mod.origName == "" {
			modInfo = mod
			isMod = true
			targetV = v.buildModuleValidator(mod)
		}
	}

	savedScope := targetV.scope
	targetV.scope = newTypeScope(targetV.globalScope)
	fnScope := targetV.pushScope()
	targetV.inferFuncUnionParams(fn, fnScope)
	for i, p := range fn.Params {
		if i < len(argStates) && hasConcreteTypeInfo(argStates[i]) {
			if isMod {
				fnScope.vars[p] = unprefixVarTypeState(argStates[i], modName)
			} else {
				fnScope.vars[p] = argStates[i].clone()
			}
		}
	}

	var retStates []varTypeState
	var walkStmts func(stmts []Stmt)
	walkStmts = func(stmts []Stmt) {
		for _, s := range stmts {
			switch st := s.(type) {
			case *AssignStmt:
				targetV.setVarInCurrentOrExisting(st.Name, targetV.inferExprTypeState(st.Value))
			case *ConstDeclStmt:
				targetV.scope.vars[st.Name] = targetV.inferExprTypeState(st.Value)
			case *ReturnStmt:
				retStates = append(retStates, targetV.inferExprTypeState(st.Value))
			case *CheckGuardStmt:
				if st.IsEffectBlock {
					bScope := targetV.pushScope()
					targetV.applyCondNarrowing(bScope, st.Cond, true)
					walkStmts(st.Body)
					targetV.popScope()
					if len(st.ElseBody) > 0 {
						eScope := targetV.pushScope()
						targetV.applyCondNarrowing(eScope, st.Cond, false)
						walkStmts(st.ElseBody)
						targetV.popScope()
					} else if targetV.stmtAlwaysReturnsExpr(st.Body) {
						targetV.applyCondNarrowing(targetV.scope, st.Cond, false)
					}
				} else {
					for _, r := range st.Rules {
						if r.Action != nil {
							aScope := targetV.pushScope()
							targetV.applyCondNarrowing(aScope, r.Cond, true)
							walkStmts([]Stmt{r.Action})
							targetV.popScope()
						}
						targetV.applyCondNarrowing(targetV.scope, r.Cond, false)
					}
					if len(st.ElseBody) > 0 {
						walkStmts(st.ElseBody)
					}
				}
			case *MatchStmt:
				for _, arm := range st.Arms {
					targetV.pushScope()
					if arm.Action != nil {
						walkStmts([]Stmt{arm.Action})
					} else {
						walkStmts(arm.Body)
					}
					targetV.popScope()
				}
				if len(st.ElseBody) > 0 {
					targetV.pushScope()
					walkStmts(st.ElseBody)
					targetV.popScope()
				}
			}
		}
	}
	walkStmts(fn.Body)
	targetV.scope = savedScope

	if len(retStates) == 0 {
		return varTypeState{}
	}
	var out varTypeState
	first := retStates[0]
	if first.listElemType != "" {
		allMatch := true
		for _, rs := range retStates[1:] {
			if rs.listElemType != first.listElemType {
				allMatch = false
				break
			}
		}
		if allMatch {
			out = varTypeState{listElemType: first.listElemType}
		}
	}
	if !hasConcreteTypeInfo(out) && first.exactRecord != "" {
		allMatch := true
		for _, rs := range retStates[1:] {
			if rs.exactRecord != first.exactRecord {
				allMatch = false
				break
			}
		}
		if allMatch {
			out = varTypeState{exactRecord: first.exactRecord}
		}
	}
	if !hasConcreteTypeInfo(out) && first.unionName != "" {
		allMatch := true
		for _, rs := range retStates[1:] {
			if rs.unionName != first.unionName {
				allMatch = false
				break
			}
		}
		if allMatch {
			out = varTypeState{unionName: first.unionName}
		}
	}
	if !hasConcreteTypeInfo(out) && first.inboxElemType != "" {
		allMatch := true
		for _, rs := range retStates[1:] {
			if rs.inboxElemType != first.inboxElemType {
				allMatch = false
				break
			}
		}
		if allMatch {
			out = varTypeState{inboxElemType: first.inboxElemType}
		}
	}
	if !hasConcreteTypeInfo(out) {
		out = targetV.unifyScalarTypeStates(retStates)
	}
	if isMod {
		return prefixVarTypeState(out, modName, modInfo.defs, modInfo.unions)
	}
	return out
}

func isConditionExpr(e Expr) bool {
	switch ex := e.(type) {
	case *BoolExpr:
		return true
	case *QuantifierExpr:
		return true
	case *TypeCheckExpr:
		return true
	case *CallExpr:
		return ex.Callee == "contains"
	case *UnaryExpr:
		return ex.Op == "not"
	case *BinaryExpr:
		switch ex.Op {
		case ">", "<", ">=", "<=", "is", "is not", "is-not", "and", "or":
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
		"Conditions cannot be stored or printed. Use a domain union instead, e.g.: def state = \"free\" | \"occupied\"",
		"Evaluate conditions directly inside 'check' or 'test', and return/store explicit union variants instead.",
	)
}

func (v *typeValidator) checkNotStandaloneCondition(e Expr) error {
	if v.inErrorTestExpr || e == nil || !isConditionExpr(e) {
		return nil
	}
	line, col := e.Pos()
	return newError(
		"TypeError",
		line,
		col,
		v.source,
		"Standalone condition has no effect; conditions can only be used in 'check', 'where', or 'test'",
		"Write 'check <cond> ... end' to branch, 'xs where <cond>' to filter, or wrap assertions in 'test ... end'.",
	)
}

func (v *typeValidator) staticConcreteType(st varTypeState) string {
	return v.staticTypeKind(st, false)
}

func (v *typeValidator) checkFieldAccessOnState(st varTypeState, varName string, ex *FieldExpr) error {
	if st.listElemType != "" {
		if ex.Field == "index" && v.loopDepth > 0 {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				"Cannot access field '.index' on a list",
				"Lists have no '.index' field; use 'it.index' for the 1-based loop position or 'count <list>' for length.",
			)
		}
		if idxSt, ok := v.lookupVar(ex.Field); ok && (idxSt.exactBaseType == "number" || (idxSt.isParam && idxSt.exactBaseType == "" && idxSt.exactRecord == "" && idxSt.listElemType == "" && idxSt.unionName == "")) {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				fmt.Sprintf("Cannot access field '.%s' on a list", ex.Field),
				fmt.Sprintf("To index a list by variable %q, use 1-based dynamic indexing: '.(%s)' (e.g. 'xs.(%s)').", ex.Field, ex.Field, ex.Field),
			)
		}
		return newError(
			"TypeError",
			ex.Line,
			ex.Col,
			v.source,
			fmt.Sprintf("Cannot access field '.%s' on a list", ex.Field),
			"Lists only support 1-based positional indexing ('.1' or '.(expr)').",
		)
	}
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
			if ex.Field == "index" && v.loopDepth > 0 {
				fieldHint = "Note: '.index' is only available directly on the loop variable 'it' ('it.index'), not on copied variables. " + fieldHint
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
		msg := fmt.Sprintf("Cannot read field '.%s' on non-record value", ex.Field)
		hint := fmt.Sprintf("Narrow the value with '<var> is <Record>' before accessing '.%s'.", ex.Field)
		if varName != "" {
			msg = fmt.Sprintf("Cannot read field '.%s' on non-record variable %q", ex.Field, varName)
			hint = fmt.Sprintf("Narrow %q with '%s is <Record>' before accessing '.%s'.", varName, varName, ex.Field)
		}
		if ex.Field == "index" && v.loopDepth > 0 {
			hint = "Note: '.index' is only available directly on the loop variable 'it' ('it.index'), not on copied variables. " + hint
		}
		return newError("TypeError", ex.Line, ex.Col, v.source, msg, hint)
	}
	if st.unionName != "" && varName != "" {
		recOpts := v.unionRecordNames(st.unionName)
		hint := fmt.Sprintf("Union %q has no record variants.", st.unionName)
		if len(recOpts) > 0 {
			hint = fmt.Sprintf("Narrow %q first (e.g. 'check %s is %s -> ...').", varName, varName, recOpts[0])
		}
		return newError(
			"TypeError",
			ex.Line,
			ex.Col,
			v.source,
			fmt.Sprintf("Cannot read field '.%s' on union variable %q (%s) before narrowing", ex.Field, varName, st.unionName),
			hint,
		)
	}
	if len(st.excludedRecords) > 0 && varName != "" {
		return newError(
			"TypeError",
			ex.Line,
			ex.Col,
			v.source,
			fmt.Sprintf("Cannot read field '.%s' on un-narrowed variable %q", ex.Field, varName),
			fmt.Sprintf("Narrow %q with '%s is <Record>' before accessing '.%s'.", varName, varName, ex.Field),
		)
	}
	return nil
}

func (v *typeValidator) checkRecordWithUpdates(recName string, updates []WithFieldUpdate, fallbackLine, fallbackCol int) error {
	d := v.defs[recName]
	if d == nil {
		return nil
	}
	fieldByName := make(map[string]DefField, len(d.Fields))
	for _, f := range d.Fields {
		fieldByName[f.Name] = f
	}
	modPrefix := ""
	if dot := strings.IndexByte(recName, '.'); dot >= 0 {
		modPrefix = recName[:dot+1]
	}

	for _, upd := range updates {
		line, col := upd.Line, upd.Col
		if line == 0 {
			line, col = fallbackLine, fallbackCol
		}
		f, ok := fieldByName[upd.Name]
		if !ok {
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("%s has no field %q", recName, upd.Name),
				fmt.Sprintf("Available fields on %s: %s.", recName, defFieldNames(d)),
			)
		}

		valSt := v.inferExprTypeState(upd.Value)
		if !hasConcreteTypeInfo(valSt) {
			continue
		}

		// 1. Inline or named union field constraint.
		if len(f.Allowed) > 0 {
			if variants, label, ok := v.fieldUnionVariants(recName, f.Name); ok && len(variants) > 0 {
				if err := v.checkValueAgainstUnionVariants(upd.Value, valSt, variants, label, line, col); err != nil {
					return err
				}
				if err := v.checkNonLiteralMatchesUnion(upd.Value, valSt, variants, label, line, col); err != nil {
					return err
				}
			}
			continue
		}
		if id, isId := f.Default.(*IdentExpr); isId {
			uName := ""
			if modPrefix != "" && len(v.resolvedUnions[modPrefix+id.Name]) > 0 {
				uName = modPrefix + id.Name
			} else if len(v.resolvedUnions[id.Name]) > 0 {
				uName = id.Name
			}
			if uName != "" {
				if err := v.checkValueAgainstUnionVariants(upd.Value, valSt, v.resolvedUnions[uName], uName, line, col); err != nil {
					return err
				}
				if err := v.checkNonLiteralMatchesUnion(upd.Value, valSt, v.resolvedUnions[uName], uName, line, col); err != nil {
					return err
				}
				continue
			}
			wantRec := ""
			if modPrefix != "" && v.defs[modPrefix+id.Name] != nil {
				wantRec = modPrefix + id.Name
			} else if v.defs[id.Name] != nil {
				wantRec = id.Name
			}
			if wantRec != "" {
				if valSt.exactBaseType != "" || valSt.listElemType != "" || valSt.inboxElemType != "" || (valSt.exactRecord != "" && valSt.exactRecord != wantRec) {
					return newError(
						"TypeError",
						line,
						col,
						v.source,
						fmt.Sprintf("Field '%s.%s' expected %s, got %s", recName, f.Name, wantRec, v.staticConcreteType(valSt)),
						fmt.Sprintf("Assign a '%s' value to field %q.", wantRec, f.Name),
					)
				}
				continue
			}
		}

		// 2. Plain default-typed fields (number, text, list).
		switch defEx := f.Default.(type) {
		case *NumberExpr:
			if (valSt.exactBaseType != "" && valSt.exactBaseType != "number") || valSt.exactRecord != "" || valSt.listElemType != "" || valSt.inboxElemType != "" {
				return newError(
					"TypeError",
					line,
					col,
					v.source,
					fmt.Sprintf("Field '%s.%s' expected number, got %s", recName, f.Name, v.staticConcreteType(valSt)),
					fmt.Sprintf("Assign a number to field %q.", f.Name),
				)
			}
		case *UnaryExpr:
			if defEx.Op == "-" {
				if (valSt.exactBaseType != "" && valSt.exactBaseType != "number") || valSt.exactRecord != "" || valSt.listElemType != "" || valSt.inboxElemType != "" {
					return newError(
						"TypeError",
						line,
						col,
						v.source,
						fmt.Sprintf("Field '%s.%s' expected number, got %s", recName, f.Name, v.staticConcreteType(valSt)),
						fmt.Sprintf("Assign a number to field %q.", f.Name),
					)
				}
			}
		case *StringExpr, *InterpStringExpr:
			if (valSt.exactBaseType != "" && valSt.exactBaseType != "text") || valSt.exactRecord != "" || valSt.listElemType != "" || valSt.inboxElemType != "" {
				return newError(
					"TypeError",
					line,
					col,
					v.source,
					fmt.Sprintf("Field '%s.%s' expected text, got %s", recName, f.Name, v.staticConcreteType(valSt)),
					fmt.Sprintf("Assign text to field %q.", f.Name),
				)
			}
		case *TypedListExpr, *ListExpr:
			if valSt.exactBaseType != "" || valSt.exactRecord != "" || valSt.inboxElemType != "" {
				return newError(
					"TypeError",
					line,
					col,
					v.source,
					fmt.Sprintf("Field '%s.%s' expected list, got %s", recName, f.Name, v.staticConcreteType(valSt)),
					fmt.Sprintf("Assign a list to field %q.", f.Name),
				)
			}
		}
	}
	return nil
}

func (v *typeValidator) checkValueAgainstUnionVariants(valExpr Expr, valSt varTypeState, variants []resolvedUnionVariant, label string, line, col int) error {
	if s, ok := valExpr.(*StringExpr); ok {
		for _, vr := range variants {
			if (vr.kind == variantBaseType && vr.baseType == "text") || (vr.kind == variantStringLit && vr.strVal == s.Value) {
				return nil
			}
		}
		allowed := make([]string, len(variants))
		for i, vr := range variants {
			allowed[i] = vr.display()
		}
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Invalid value %q for %s (%s)", s.Value, label, strings.Join(allowed, " | ")),
			fmt.Sprintf("Valid values for %s: %s.", label, strings.Join(allowed, " | ")),
		)
	}
	if n, ok := valExpr.(*NumberExpr); ok {
		for _, vr := range variants {
			if (vr.kind == variantBaseType && vr.baseType == "number") || (vr.kind == variantNumberLit && vr.numVal == n.Value) {
				return nil
			}
		}
		allowed := make([]string, len(variants))
		for i, vr := range variants {
			allowed[i] = vr.display()
		}
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Invalid value %s for %s (%s)", n.Raw, label, strings.Join(allowed, " | ")),
			fmt.Sprintf("Valid values for %s: %s.", label, strings.Join(allowed, " | ")),
		)
	}
	if valSt.exactRecord != "" {
		for _, vr := range variants {
			if vr.kind == variantRecord && vr.recordName == valSt.exactRecord {
				return nil
			}
		}
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Record %q is not a member of %s", valSt.exactRecord, label),
			fmt.Sprintf("Pass a valid member of %s.", label),
		)
	}
	return nil
}

func (v *typeValidator) returnTypeState(fn *FuncDecl) varTypeState {
	if fn == nil || fn.ReturnType == "" {
		return varTypeState{}
	}
	if fn.ReturnElemType != "" {
		return varTypeState{listElemType: fn.ReturnElemType}
	}
	if st := v.typeStateFromName(fn.ReturnType); hasConcreteTypeInfo(st) {
		return st
	}
	if modName, memberName, ok := strings.Cut(fn.ReturnType, "."); ok {
		if mod, exists := v.modules[modName]; exists {
			if mod.defs[memberName] != nil {
				return varTypeState{exactRecord: fn.ReturnType}
			}
			if mod.unions[memberName] != nil {
				return varTypeState{unionName: fn.ReturnType}
			}
		}
	}
	return varTypeState{}
}

func (v *typeValidator) isKnownRecordName(name string) bool {
	if v.defs[name] != nil {
		return true
	}
	if modName, memberName, ok := strings.Cut(name, "."); ok {
		if mod, exists := v.modules[modName]; exists {
			return mod.defs[memberName] != nil
		}
	}
	return false
}

func (v *typeValidator) resolveUnionVariantsByName(name string) ([]resolvedUnionVariant, bool) {
	if variants, ok := v.resolvedUnions[name]; ok {
		return variants, true
	}
	if modName, memberName, ok := strings.Cut(name, "."); ok {
		if mod, exists := v.modules[modName]; exists && mod.unions[memberName] != nil {
			modV := v.buildModuleValidator(mod)
			if variants, ok := modV.resolvedUnions[memberName]; ok {
				prefixed := make([]resolvedUnionVariant, len(variants))
				for i, vr := range variants {
					prefixed[i] = vr
					if vr.kind == variantRecord && !strings.Contains(vr.recordName, ".") {
						prefixed[i].recordName = modName + "." + vr.recordName
					}
				}
				return prefixed, true
			}
		}
	}
	return nil, false
}

func (v *typeValidator) exprDependsOnUnspecializedParam(e Expr) bool {
	if e == nil {
		return false
	}
	switch ex := e.(type) {
	case *IdentExpr:
		for sc := v.scope; sc != nil; sc = sc.parent {
			if st, ok := sc.vars[ex.Name]; ok {
				return (st.isParam || st.fromParam) && !hasConcreteTypeInfo(st)
			}
		}
		return false
	case *WithExpr:
		return v.exprDependsOnUnspecializedParam(ex.Base)
	case *UpdateExpr:
		return v.exprDependsOnUnspecializedParam(ex.Target)
	case *WhereExpr:
		return v.exprDependsOnUnspecializedParam(ex.Target)
	case *ProjectExpr:
		return v.exprDependsOnUnspecializedParam(ex.Source) || v.exprDependsOnUnspecializedParam(ex.Body)
	case *QuantifierExpr:
		return v.exprDependsOnUnspecializedParam(ex.Source) || v.exprDependsOnUnspecializedParam(ex.Cond)
	case *SortByExpr:
		return v.exprDependsOnUnspecializedParam(ex.Target)
	case *FieldExpr:
		return v.exprDependsOnUnspecializedParam(ex.Target)
	case *IndexExpr:
		return v.exprDependsOnUnspecializedParam(ex.Target)
	case *BinaryExpr:
		return v.exprDependsOnUnspecializedParam(ex.Left) || v.exprDependsOnUnspecializedParam(ex.Right)
	case *UnaryExpr:
		return v.exprDependsOnUnspecializedParam(ex.Right)
	case *CallExpr:
		for _, a := range ex.Args {
			if v.exprDependsOnUnspecializedParam(a) {
				return true
			}
		}
	}
	return false
}

func (v *typeValidator) checkReturnValueConforms(fn *FuncDecl, valExpr Expr, line, col int) error {
	valSt := v.inferExprTypeState(valExpr)
	retDisplay := formatReturnTypeDisplay(fn.ReturnType, fn.ReturnElemType)

	if fn.ReturnElemType != "" {
		if le, isList := valExpr.(*ListExpr); isList && len(le.Elements) == 0 || valSt.listElemType == "any" {
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Function '%s' declares return type '%s', but returned an untyped empty list", fn.Name, retDisplay),
				fmt.Sprintf("Write 'list as %s' for an empty typed list.", fn.ReturnElemType),
			)
		}
		if valSt.listElemType != "" && valSt.listElemType != "any" {
			if valSt.listElemType == fn.ReturnElemType {
				return nil
			}
			if variants, isUnion := v.resolveUnionVariantsByName(fn.ReturnElemType); isUnion {
				if le, isList := valExpr.(*ListExpr); isList {
					for _, el := range le.Elements {
						elSt := v.inferExprTypeState(el)
						if err := v.checkValueAgainstUnionVariants(el, elSt, variants, fn.ReturnElemType, line, col); err != nil {
							return err
						}
						if err := v.checkNonLiteralMatchesUnion(el, elSt, variants, fn.ReturnElemType, line, col); err != nil {
							return err
						}
					}
					return nil
				}
				if v.isKnownRecordName(valSt.listElemType) {
					for _, vr := range variants {
						if vr.kind == variantRecord && vr.recordName == valSt.listElemType {
							return nil
						}
					}
				}
			}
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Function '%s' declares return type '%s', got 'list of %s'", fn.Name, retDisplay, valSt.listElemType),
				fmt.Sprintf("Return a list of %s.", fn.ReturnElemType),
			)
		}
		if !hasConcreteTypeInfo(valSt) {
			if v.exprDependsOnUnspecializedParam(valExpr) {
				return nil
			}
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Cannot verify return value matches declared type '%s' in function '%s'", retDisplay, fn.Name),
				fmt.Sprintf("Return a typed 'list as %s' or pass typed arguments.", fn.ReturnElemType),
			)
		}
		gotLabel := v.staticConcreteType(valSt)
		if gotLabel == "" {
			gotLabel = "incompatible value"
		}
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Function '%s' declares return type '%s', got %s", fn.Name, retDisplay, gotLabel),
			fmt.Sprintf("Return a value of type '%s'.", retDisplay),
		)
	}

	if fn.ReturnType == "number" || fn.ReturnType == "text" {
		if !hasConcreteTypeInfo(valSt) {
			if v.exprDependsOnUnspecializedParam(valExpr) {
				return nil
			}
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Cannot verify return value matches declared type '%s' in function '%s'", fn.ReturnType, fn.Name),
				fmt.Sprintf("Add an explicit 'as %s' cast or return a statically typed %s.", fn.ReturnType, fn.ReturnType),
			)
		}
		gotKind := v.staticTypeKind(valSt, false)
		if gotKind == fn.ReturnType {
			return nil
		}
		if gotKind == "" {
			gotKind = v.staticConcreteType(valSt)
		}
		if gotKind == "" {
			gotKind = "incompatible value"
		}
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Function '%s' declares return type '%s', got %s", fn.Name, fn.ReturnType, gotKind),
			fmt.Sprintf("Return a %s value from '%s'.", fn.ReturnType, fn.Name),
		)
	}

	if v.isKnownRecordName(fn.ReturnType) {
		if !hasConcreteTypeInfo(valSt) {
			if v.exprDependsOnUnspecializedParam(valExpr) {
				return nil
			}
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Cannot verify return value matches declared record type '%s' in function '%s'", fn.ReturnType, fn.Name),
				fmt.Sprintf("Add an explicit 'as %s' cast or return a '%s' record.", fn.ReturnType, fn.ReturnType),
			)
		}
		if valSt.exactRecord == fn.ReturnType {
			return nil
		}
		gotLabel := v.staticConcreteType(valSt)
		if gotLabel == "" {
			gotLabel = "incompatible value"
		}
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Function '%s' declares return type '%s', got %s", fn.Name, fn.ReturnType, gotLabel),
			fmt.Sprintf("Return a '%s' record from '%s'.", fn.ReturnType, fn.Name),
		)
	}

	if variants, isUnion := v.resolveUnionVariantsByName(fn.ReturnType); isUnion {
		if valSt.unionName == fn.ReturnType {
			return nil
		}
		if !hasConcreteTypeInfo(valSt) {
			if v.exprDependsOnUnspecializedParam(valExpr) {
				return nil
			}
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Cannot verify return value matches declared union '%s' in function '%s'", fn.ReturnType, fn.Name),
				fmt.Sprintf("Add an explicit 'as %s' cast or return a valid '%s' variant.", fn.ReturnType, fn.ReturnType),
			)
		}
		if err := v.checkValueAgainstUnionVariants(valExpr, valSt, variants, fn.ReturnType, line, col); err != nil {
			return err
		}
		return v.checkNonLiteralMatchesUnion(valExpr, valSt, variants, fn.ReturnType, line, col)
	}

	return nil
}

func (v *typeValidator) checkNonLiteralMatchesUnion(valExpr Expr, valSt varTypeState, variants []resolvedUnionVariant, unionName string, line, col int) error {
	switch valExpr.(type) {
	case *StringExpr, *NumberExpr:
		return nil
	}
	if valSt.exactRecord != "" {
		return nil
	}
	if len(valSt.knownVariants) > 0 {
		if unionVariantsSubset(valSt.knownVariants, variants) {
			return nil
		}
		allowed := make([]string, len(variants))
		for i, vr := range variants {
			allowed[i] = vr.display()
		}
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Value is not statically guaranteed to belong to union '%s'", unionName),
			fmt.Sprintf("Valid values for %s: %s.", unionName, strings.Join(allowed, " | ")),
		)
	}
	if valSt.exactBaseType != "" {
		for _, vr := range variants {
			if vr.kind == variantBaseType && vr.baseType == valSt.exactBaseType {
				return nil
			}
		}
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Value of type %s is not statically guaranteed to belong to union '%s'", valSt.exactBaseType, unionName),
			fmt.Sprintf("Use 'as %s' or return a concrete %s variant.", unionName, unionName),
		)
	}
	if valSt.unionName != "" && valSt.unionName != unionName {
		if srcVariants, ok := v.resolveUnionVariantsByName(valSt.unionName); ok && len(srcVariants) > 0 && unionVariantsSubset(srcVariants, variants) {
			return nil
		}
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Union '%s' does not match declared union '%s'", valSt.unionName, unionName),
			fmt.Sprintf("Return a valid '%s' variant.", unionName),
		)
	}
	return nil
}

func unionVariantsSubset(sub, sup []resolvedUnionVariant) bool {
	for _, sv := range sub {
		matched := false
		for _, pv := range sup {
			if pv.kind == sv.kind {
				switch sv.kind {
				case variantStringLit:
					matched = sv.strVal == pv.strVal
				case variantNumberLit:
					matched = sv.numVal == pv.numVal
				case variantBaseType:
					matched = sv.baseType == pv.baseType
				case variantRecord:
					matched = sv.recordName == pv.recordName
				}
			} else if pv.kind == variantBaseType {
				if (sv.kind == variantStringLit && pv.baseType == "text") || (sv.kind == variantNumberLit && pv.baseType == "number") {
					matched = true
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func toposortConsts(stmts []Stmt, source string) ([]*ConstDeclStmt, error) {
	byName := make(map[string]*ConstDeclStmt)
	var order []string
	for _, s := range stmts {
		cs, ok := s.(*ConstDeclStmt)
		if !ok {
			continue
		}
		if prev, dup := byName[cs.Name]; dup {
			return nil, newError(
				"TypeError",
				cs.Line,
				cs.Col,
				source,
				fmt.Sprintf("Duplicate 'def' constant %q (already defined at line %d)", cs.Name, prev.Line),
				"Each 'def' constant must have a unique name.",
			)
		}
		byName[cs.Name] = cs
		order = append(order, cs.Name)
	}
	if len(order) == 0 {
		return nil, nil
	}

	var collectDeps func(e Expr, deps map[string]bool)
	collectDeps = func(e Expr, deps map[string]bool) {
		if e == nil {
			return
		}
		switch ex := e.(type) {
		case *IdentExpr:
			if byName[ex.Name] != nil {
				deps[ex.Name] = true
			}
		case *UnaryExpr:
			collectDeps(ex.Right, deps)
		case *BinaryExpr:
			collectDeps(ex.Left, deps)
			collectDeps(ex.Right, deps)
		case *ListExpr:
			for _, el := range ex.Elements {
				collectDeps(el, deps)
			}
		case *TypedListExpr:
			for _, row := range ex.Rows {
				for _, upd := range row {
					collectDeps(upd.Value, deps)
				}
			}
		case *WithExpr:
			collectDeps(ex.Base, deps)
			for _, upd := range ex.Updates {
				collectDeps(upd.Value, deps)
			}
		case *CallExpr:
			for _, a := range ex.Args {
				collectDeps(a, deps)
			}
		case *IndexExpr:
			collectDeps(ex.Target, deps)
			collectDeps(ex.Index, deps)
		case *FieldExpr:
			collectDeps(ex.Target, deps)
		case *InterpStringExpr:
			for _, seg := range ex.Parts {
				if seg.IsExpr && seg.Expr != nil {
					collectDeps(seg.Expr, deps)
				}
			}
		}
	}

	state := make(map[string]int, len(order)) // 0=unvisited, 1=visiting, 2=done
	var sorted []*ConstDeclStmt
	var stack []string

	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case 2:
			return nil
		case 1:
			cs := byName[name]
			cycle := append(append([]string(nil), stack...), name)
			return newError(
				"TypeError",
				cs.Line,
				cs.Col,
				source,
				fmt.Sprintf("Cyclic 'def' constant dependency: %s", strings.Join(cycle, " -> ")),
				"Remove the circular reference between 'def' constants.",
			)
		}
		state[name] = 1
		stack = append(stack, name)
		cs := byName[name]
		deps := make(map[string]bool)
		collectDeps(cs.Value, deps)
		depNames := make([]string, 0, len(deps))
		for d := range deps {
			depNames = append(depNames, d)
		}
		sort.Strings(depNames)
		for _, d := range depNames {
			if err := visit(d); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = 2
		sorted = append(sorted, cs)
		return nil
	}

	for _, name := range order {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return sorted, nil
}

func (v *typeValidator) checkPureConstExpr(e Expr) error {
	if e == nil {
		return nil
	}
	switch ex := e.(type) {
	case *NumberExpr, *StringExpr:
		return nil
	case *InterpStringExpr:
		for _, seg := range ex.Parts {
			if seg.IsExpr && seg.Expr != nil {
				if err := v.checkPureConstExpr(seg.Expr); err != nil {
					return err
				}
			}
		}
		return nil
	case *IdentExpr:
		if v.consts[ex.Name] != 0 || v.defs[ex.Name] != nil || v.unions[ex.Name] != nil {
			return nil
		}
		return newError(
			"TypeError",
			ex.Line,
			ex.Col,
			v.source,
			fmt.Sprintf("'def' constant cannot reference mutable variable %q", ex.Name),
			"Use only literals, other 'def' constants, or pure data constructors inside 'def name = ...'.",
		)
	case *UnaryExpr:
		return v.checkPureConstExpr(ex.Right)
	case *BinaryExpr:
		if err := v.checkPureConstExpr(ex.Left); err != nil {
			return err
		}
		return v.checkPureConstExpr(ex.Right)
	case *ListExpr:
		for _, el := range ex.Elements {
			if err := v.checkPureConstExpr(el); err != nil {
				return err
			}
		}
		return nil
	case *TypedListExpr:
		for _, row := range ex.Rows {
			for _, upd := range row {
				if err := v.checkPureConstExpr(upd.Value); err != nil {
					return err
				}
			}
		}
		return nil
	case *WithExpr:
		if err := v.checkPureConstExpr(ex.Base); err != nil {
			return err
		}
		for _, upd := range ex.Updates {
			if err := v.checkPureConstExpr(upd.Value); err != nil {
				return err
			}
		}
		return nil
	case *FieldExpr:
		if id, ok := ex.Target.(*IdentExpr); ok {
			if mod, isMod := v.modules[id.Name]; isMod {
				if mod.consts[ex.Field] != nil || mod.defs[ex.Field] != nil || mod.unions[ex.Field] != nil {
					return nil
				}
			}
		}
		return v.checkPureConstExpr(ex.Target)
	case *IndexExpr:
		if err := v.checkPureConstExpr(ex.Target); err != nil {
			return err
		}
		return v.checkPureConstExpr(ex.Index)
	case *CallExpr:
		if ex.Module == "" {
			switch ex.Callee {
			case "range", "split", "join", "trim", "upper", "lower", "replace", "floor", "round", "abs", "min", "max", "slice", "count", "append":
				for _, a := range ex.Args {
					if err := v.checkPureConstExpr(a); err != nil {
						return err
					}
				}
				return nil
			case "sys_args":
				if v.allowInternalBuiltins {
					return nil
				}
			}
		}
		return newError(
			"TypeError",
			ex.Line,
			ex.Col,
			v.source,
			fmt.Sprintf("'def' constant cannot call impure or user function %q", ex.Callee),
			"Keep 'def' constants 100% pure (literals, records, typed lists, or pure built-in transforms like 'split' and 'range').",
		)
	default:
		line, col := e.Pos()
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			"Unsupported expression in 'def' constant",
			"Use literals, other 'def' constants, records ('with'), or typed lists ('list as ...').",
		)
	}
}
