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
	alias  string
	defs   map[string]*DefDecl
	unions map[string]*UnionDecl
	funcs  map[string]*FuncDecl
	consts map[string]Expr
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

func validateProgramTypesWithMode(prog *Program, source string, isModule bool) error {
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
	for _, fn := range prog.Functions {
		if !isModule {
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
		if err := validateProgramTypesWithMode(imp.ModuleProg, imp.ModuleSource, true); err != nil {
			return err
		}
		mi := modTypeInfo{
			alias:  alias,
			defs:   make(map[string]*DefDecl, len(imp.ModuleProg.Defs)),
			unions: make(map[string]*UnionDecl, len(imp.ModuleProg.Unions)),
			funcs:  make(map[string]*FuncDecl, len(imp.ModuleProg.Functions)),
			consts: make(map[string]Expr),
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
		source:         source,
		known:          known,
		defs:           defs,
		unions:         unions,
		consts:         consts,
		constExprs:     constExprs,
		funcNames:      funcNames,
		modules:        modules,
		resolvedUnions: make(map[string][]resolvedUnionVariant, len(unions)),
		scope:          newTypeScope(nil),
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

func isVoidBuiltinCallStmt(s Stmt) bool {
	es, ok := s.(*ExprStmt)
	if !ok {
		return false
	}
	ce, ok := es.Expression.(*CallExpr)
	if !ok || ce.Module != "" {
		return false
	}
	switch ce.Callee {
	case "close", "put":
		return true
	default:
		return false
	}
}

func stmtEndsWithExpr(stmts []Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	last := stmts[len(stmts)-1]
	switch st := last.(type) {
	case *ExprStmt:
		return !isVoidBuiltinCallStmt(st)
	case *LoopStmt:
		// An infinite `loop ... end` (no iterable) cannot fall through normally.
		return st.Iterable == nil
	case *MatchStmt:
		if len(st.ElseBody) > 0 && stmtEndsWithExpr(st.ElseBody) {
			return true
		}
		for _, arm := range st.Arms {
			if arm.Action != nil && stmtEndsWithExpr([]Stmt{arm.Action}) {
				return true
			}
		}
		return false
	case *CheckGuardStmt:
		if len(st.ElseBody) > 0 {
			return stmtEndsWithExpr(st.ElseBody)
		}
		// Exhaustive multi-branch `check ... end` where every rule returns an expression.
		if len(st.Rules) >= 2 {
			allExpr := true
			for _, r := range st.Rules {
				if r.Action == nil || !stmtEndsWithExpr([]Stmt{r.Action}) {
					allExpr = false
					break
				}
			}
			return allExpr
		}
		return false
	default:
		return false
	}
}

func findGuardReturn(stmts []Stmt) (int, int, bool) {
	for _, s := range stmts {
		switch st := s.(type) {
		case *CheckGuardStmt:
			for _, r := range st.Rules {
				if r.Action != nil && !isVoidBuiltinCallStmt(r.Action) {
					if _, isExpr := r.Action.(*ExprStmt); isExpr {
						return r.Line, r.Col, true
					}
				}
			}
			if line, col, ok := findGuardReturn(st.ElseBody); ok {
				return line, col, true
			}
		case *LoopStmt:
			if line, col, ok := findGuardReturn(st.Body); ok {
				return line, col, true
			}
		case *TakeStmt:
			if line, col, ok := findGuardReturn(st.Body); ok {
				return line, col, true
			}
		case *MatchStmt:
			for _, arm := range st.Arms {
				if arm.Action != nil {
					if line, col, ok := findGuardReturn([]Stmt{arm.Action}); ok {
						return line, col, true
					}
				}
			}
			if line, col, ok := findGuardReturn(st.ElseBody); ok {
				return line, col, true
			}
		}
	}
	return 0, 0, false
}

// checkFuncReturnConsistency rejects functions that return a value on a guard branch
// (`check ... -> <expr>`) but fall through without a final return expression.
func (v *typeValidator) checkFuncReturnConsistency(fn *FuncDecl) error {
	if fn == nil {
		return nil
	}
	if stmtEndsWithExpr(fn.Body) {
		return nil
	}
	if line, col, hasGuard := findGuardReturn(fn.Body); hasGuard {
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Function '%s' returns a value on a 'check' branch but falls through without a return value at the end", fn.Name),
			"Use '-> end' to exit a loop or take block, or return a consistent value at the end of the function.",
		)
	}
	return nil
}

type typeValidator struct {
	source           string
	known            map[string]typeDefInfo
	defs             map[string]*DefDecl
	unions           map[string]*UnionDecl
	consts           map[string]int
	constExprs       map[string]Expr
	funcNames        map[string]int
	modules          map[string]modTypeInfo
	resolvedUnions   map[string][]resolvedUnionVariant
	scope            *typeScope
	inErrorTestExpr  bool
	inErrorBlock     bool
	loopDepth        int
	swarmDepth       int
	directInSwarm    bool
	workerAssigned   map[string]bool
	workerOuterScope *typeScope
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
				for _, r := range st.Rules {
					collectFromExpr(r.Cond)
					if r.Action != nil {
						collectFromStmts([]Stmt{r.Action})
					}
				}
				collectFromStmts(st.ElseBody)
			case *LoopStmt:
				collectFromStmts(st.Body)
			case *TakeStmt:
				collectFromStmts(st.Body)
			case *MatchStmt:
				for _, arm := range st.Arms {
					if arm.Action != nil {
						collectFromStmts([]Stmt{arm.Action})
					}
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
func (v *typeValidator) checkStmts(stmts []Stmt, inFunction bool) error {
	for i, s := range stmts {
		switch st := s.(type) {
		case *CheckGuardStmt:
			if err := v.checkCheckGuardStmt(stmts, i, st, inFunction); err != nil {
				return err
			}
		default:
			if err := v.checkStmt(s, inFunction); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasLoopExit(stmts []Stmt, inFunction, directLoop bool) bool {
	for _, s := range stmts {
		switch st := s.(type) {
		case *LoopEndStmt:
			if directLoop {
				return true
			}
		case *CheckGuardStmt:
			for _, r := range st.Rules {
				if r.Action == nil {
					continue
				}
				if _, isEnd := r.Action.(*LoopEndStmt); isEnd && directLoop {
					return true
				}
				if _, isExpr := r.Action.(*ExprStmt); isExpr && inFunction {
					return true
				}
				if hasLoopExit([]Stmt{r.Action}, inFunction, directLoop) {
					return true
				}
			}
			if hasLoopExit(st.ElseBody, inFunction, directLoop) {
				return true
			}
		case *LoopStmt:
			if inFunction && hasLoopExit(st.Body, inFunction, false) {
				return true
			}
		case *TakeStmt:
			if inFunction && hasLoopExit(st.Body, inFunction, false) {
				return true
			}
		case *MatchStmt:
			for _, arm := range st.Arms {
				if arm.Action == nil {
					continue
				}
				if _, isEnd := arm.Action.(*LoopEndStmt); isEnd && directLoop {
					return true
				}
				if _, isExpr := arm.Action.(*ExprStmt); isExpr && inFunction {
					return true
				}
				if hasLoopExit([]Stmt{arm.Action}, inFunction, directLoop) {
					return true
				}
			}
			if hasLoopExit(st.ElseBody, inFunction, directLoop) {
				return true
			}
		}
	}
	return false
}

func (v *typeValidator) checkStmt(s Stmt, inFunction bool) error {
	switch st := s.(type) {
	case *ConstDeclStmt:
		if err := v.checkNameNotCallable(st.Name, st.Line, st.Col, "constant"); err != nil {
			return err
		}
		if err := v.checkExpr(st.Value); err != nil {
			return err
		}
		if err := v.checkNotCondition(st.Value); err != nil {
			return err
		}
		v.setVarInCurrentOrExisting(st.Name, v.inferExprTypeState(st.Value))
		return nil

	case *AssignStmt:
		if err := v.checkNameNotCallable(st.Name, st.Line, st.Col, "variable"); err != nil {
			return err
		}
		if err := v.checkExpr(st.Value); err != nil {
			return err
		}
		if err := v.checkNotCondition(st.Value); err != nil {
			return err
		}
		v.setVarInCurrentOrExisting(st.Name, v.inferExprTypeState(st.Value))
		return nil

	case *SayStmt:
		if err := v.checkExpr(st.Value); err != nil {
			return err
		}
		return v.checkNotCondition(st.Value)

	case *ExprStmt:
		if err := v.checkExpr(st.Expression); err != nil {
			return err
		}
		if inFunction {
			return v.checkNotCondition(st.Expression)
		}
		return nil

	case *LoopEndStmt:
		if v.directInSwarm {
			return newError(
				"TypeError",
				st.Line,
				st.Col,
				v.source,
				"'-> end' cannot be used inside 'swarm loop'",
				"Use 'check <cond> -> <value>' to return early from a 'swarm loop' iteration.",
			)
		}
		if v.loopDepth == 0 {
			return newError(
				"TypeError",
				st.Line,
				st.Col,
				v.source,
				"'-> end' can only be used inside a 'loop' block",
				"Use 'check <cond> -> end' inside 'loop ... end' to exit the loop early.",
			)
		}
		return nil

	case *LoopStmt:
		if st.Iterable != nil {
			if err := v.checkExpr(st.Iterable); err != nil {
				return err
			}
			if err := v.checkNotCondition(st.Iterable); err != nil {
				return err
			}
			if !v.inErrorTestExpr {
				switch lit := st.Iterable.(type) {
				case *NumberExpr:
					return newError(
						"TypeError",
						lit.Line,
						lit.Col,
						v.source,
						fmt.Sprintf("'loop' requires a list or range, got number. Use 'loop range 1 %s' instead.", FormatValue(lit.Value)),
						fmt.Sprintf("Write: loop range 1 %s ... end", FormatValue(lit.Value)),
					)
				case *StringExpr, *InterpStringExpr:
					line, col := st.Iterable.Pos()
					return newError(
						"TypeError",
						line,
						col,
						v.source,
						"'loop' requires a list or range, got text",
						"Iterate over a list like 'loop xs ... end' or a range like 'loop range 1 5 ... end'.",
					)
				}
			}
		} else if !v.inErrorTestExpr && !hasLoopExit(st.Body, inFunction, true) {
			return newError(
				"TypeError",
				st.Line,
				st.Col,
				v.source,
				"Infinite 'loop' has no exit condition",
				"Add a 'check <condition> -> end' guard inside the loop to exit cleanly.",
			)
		}
		loopScope := v.pushScope()
		if st.Iterable != nil {
			loopScope.vars["it"] = v.inferIterableElemState(st.Iterable)
		}
		prevDirectInSwarm := v.directInSwarm
		v.directInSwarm = false
		v.loopDepth++
		err := v.checkStmts(st.Body, inFunction)
		v.loopDepth--
		v.directInSwarm = prevDirectInSwarm
		v.popScope()
		return err

	case *CheckGuardStmt:
		return v.checkCheckGuardStmt([]Stmt{st}, 0, st, inFunction)

	case *MatchStmt:
		return v.checkMatchStmt(st, inFunction)

	case *SpawnStmt:
		return v.checkExpr(st.Call)

	case *TakeStmt:
		if err := v.checkExpr(st.Inbox); err != nil {
			return err
		}
		if err := v.checkNotCondition(st.Inbox); err != nil {
			return err
		}
		inSt := v.inferExprTypeState(st.Inbox)
		if !v.inErrorTestExpr && inSt.inboxElemType == "" {
			if inSt.exactRecord != "" || inSt.exactNonRecord || inSt.unionName != "" || inSt.listElemType != "" || inSt.exactBaseType != "" {
				return newError(
					"TypeError",
					st.Line,
					st.Col,
					v.source,
					"'take' requires an inbox",
					"Create an inbox with 'ch = inbox as <type>' and pass it to 'take ch ... end'.",
				)
			}
		}
		takeScope := v.pushScope()
		if inSt.inboxElemType != "" {
			takeScope.vars["it"] = v.typeStateFromName(inSt.inboxElemType)
		}
		prevDirectInSwarm := v.directInSwarm
		v.directInSwarm = false
		v.loopDepth++
		err := v.checkStmts(st.Body, inFunction)
		v.loopDepth--
		v.directInSwarm = prevDirectInSwarm
		v.popScope()
		return err

	case *CheckIsStmt:
		if err := v.checkExpr(st.Actual); err != nil {
			return err
		}
		return v.checkExpr(st.Expected)

	case *CheckIsNotStmt:
		if err := v.checkExpr(st.Actual); err != nil {
			return err
		}
		return v.checkExpr(st.Unexpected)

	case *CheckCondStmt:
		return v.checkExpr(st.Condition)

	case *CheckIsErrorStmt:
		prevErrTest := v.inErrorTestExpr
		v.inErrorTestExpr = true
		err := v.checkExpr(st.Expression)
		v.inErrorTestExpr = prevErrTest
		return err

	case *CheckErrorBlockStmt:
		prevErrTest := v.inErrorTestExpr
		v.inErrorTestExpr = true
		err := v.checkExpr(st.Expression)
		v.inErrorTestExpr = prevErrTest
		if err != nil {
			return err
		}
		prevErrBlk := v.inErrorBlock
		v.inErrorBlock = true
		v.pushScope()
		bodyErr := v.checkStmts(st.Body, inFunction)
		v.popScope()
		v.inErrorBlock = prevErrBlk
		return bodyErr
	}
	return nil
}

func (v *typeValidator) checkCheckGuardStmt(blockStmts []Stmt, idx int, st *CheckGuardStmt, inFunction bool) error {
	inCheckScope := v.pushScope()
	for _, r := range st.Rules {
		if err := v.checkExpr(r.Cond); err != nil {
			v.popScope()
			return err
		}
		if r.Action != nil {
			if !inFunction {
				if es, isExpr := r.Action.(*ExprStmt); isExpr {
					if _, isCall := es.Expression.(*CallExpr); !isCall {
						v.popScope()
						line, col := es.Pos()
						return newError(
							"TypeError",
							line,
							col,
							v.source,
							"Cannot return a value from 'check' at top level; use 'say' or 'fail' instead.",
							"Top-level 'check' rules can run statements like 'say \"...\"', assignments, or 'fail \"...\"'.",
						)
					}
				}
			}
			actionScope := v.pushScope()
			v.applyCondNarrowing(actionScope, r.Cond, true)
			err := v.checkStmt(r.Action, inFunction)
			v.popScope()
			if err != nil {
				v.popScope()
				return err
			}
			v.applyCondNarrowing(inCheckScope, r.Cond, false)
		} else {
			// Assertion precondition rule: if control reaches subsequent rules/after-check, Cond was true.
			v.applyCondNarrowing(inCheckScope, r.Cond, true)
		}
	}

	if len(st.ElseBody) > 0 {
		if err := v.checkStmts(st.ElseBody, inFunction); err != nil {
			v.popScope()
			return err
		}
	}
	v.popScope()

	// Narrow post-check outer scope when early-exiting guards rule out non-record cases.
	if len(st.ElseBody) == 0 {
		for _, r := range st.Rules {
			if r.Action == nil {
				v.applyCondNarrowing(v.scope, r.Cond, true)
			} else if stmtAlwaysExits(r.Action, inFunction) {
				v.applyCondNarrowing(v.scope, r.Cond, false)
			}
		}
	}

	hasFallback := inFunction && idx < len(blockStmts)-1 && allRulesReturnExpr(st.Rules)
	return v.checkCheckExhaustiveness(st, hasFallback)
}

func stmtAlwaysExits(s Stmt, inFunction bool) bool {
	if s == nil {
		return false
	}
	if es, ok := s.(*ExprStmt); ok {
		if call, isCall := es.Expression.(*CallExpr); isCall && call.Callee == "fail" {
			return true
		}
		return inFunction
	}
	return false
}

func allRulesReturnExpr(rules []CheckRule) bool {
	if len(rules) == 0 {
		return false
	}
	for _, r := range rules {
		if r.Action == nil {
			return false
		}
		if _, isExpr := r.Action.(*ExprStmt); !isExpr {
			return false
		}
	}
	return true
}

func (v *typeValidator) applyCondNarrowing(scope *typeScope, cond Expr, truthy bool) {
	if cond == nil {
		return
	}
	switch c := cond.(type) {
	case *UnaryExpr:
		if c.Op == "not" {
			v.applyCondNarrowing(scope, c.Right, !truthy)
		}
	case *BinaryExpr:
		switch c.Op {
		case "and":
			if truthy {
				v.applyCondNarrowing(scope, c.Left, true)
				v.applyCondNarrowing(scope, c.Right, true)
			}
		case "or":
			if !truthy {
				v.applyCondNarrowing(scope, c.Left, false)
				v.applyCondNarrowing(scope, c.Right, false)
			}
		case "is", "is-not":
			effective := truthy
			if c.Op == "is-not" {
				effective = !truthy
			}
			id, ok := c.Left.(*IdentExpr)
			if !ok {
				return
			}
			cur, _ := v.lookupVar(id.Name)
			next := cur.clone()

			switch rhs := c.Right.(type) {
			case *IdentExpr:
				if v.defs[rhs.Name] != nil {
					if effective {
						next.exactRecord = rhs.Name
						next.exactNonRecord = false
					} else {
						if next.exactRecord == rhs.Name {
							next.exactRecord = ""
						}
						if next.excludedRecords == nil {
							next.excludedRecords = make(map[string]bool)
						}
						next.excludedRecords[rhs.Name] = true
					}
					scope.vars[id.Name] = next
				} else if uVars, isU := v.resolvedUnions[rhs.Name]; isU {
					if effective {
						next.unionName = rhs.Name
						hasRec := false
						for _, uv := range uVars {
							if uv.kind == variantRecord {
								hasRec = true
								break
							}
						}
						if !hasRec {
							next.exactRecord = ""
							next.exactNonRecord = true
						}
						scope.vars[id.Name] = next
					}
				}
			case *FieldExpr:
				if modId, ok := rhs.Target.(*IdentExpr); ok {
					if _, isMod := v.modules[modId.Name]; isMod {
						qual := modId.Name + "." + rhs.Field
						if v.defs[qual] != nil {
							if effective {
								next.exactRecord = qual
								next.exactNonRecord = false
							} else {
								if next.exactRecord == qual {
									next.exactRecord = ""
								}
								if next.excludedRecords == nil {
									next.excludedRecords = make(map[string]bool)
								}
								next.excludedRecords[qual] = true
							}
							scope.vars[id.Name] = next
						} else if uVars, isU := v.resolvedUnions[qual]; isU {
							if effective {
								next.unionName = qual
								hasRec := false
								for _, uv := range uVars {
									if uv.kind == variantRecord {
										hasRec = true
										break
									}
								}
								if !hasRec {
									next.exactRecord = ""
									next.exactNonRecord = true
								}
								scope.vars[id.Name] = next
							}
						}
					}
				}
			case *StringExpr, *NumberExpr, *UnaryExpr:
				if effective {
					next.exactRecord = ""
					next.exactNonRecord = true
					scope.vars[id.Name] = next
				}
			}
		}
	case *TypeCheckExpr:
		effective := truthy
		if c.Negated {
			effective = !truthy
		}
		if id, ok := c.Target.(*IdentExpr); ok {
			cur, _ := v.lookupVar(id.Name)
			next := cur.clone()
			if effective && (c.TypeName == "text" || c.TypeName == "number" || c.TypeName == "list") {
				next.exactRecord = ""
				next.exactNonRecord = true
				scope.vars[id.Name] = next
			}
		}
	}
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
			cur = "record"
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
		case "load", "save":
			return varTypeState{exactNonRecord: true}
		case "range":
			return varTypeState{listElemType: "number"}
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
		if err := v.checkExpr(ex.Target); err != nil {
			return err
		}
		if err := v.checkNotCondition(ex.Target); err != nil {
			return err
		}
		isLoadCall := false
		if call, isCall := ex.Target.(*CallExpr); isCall && call.Module == "" && call.Callee == "load" {
			isLoadCall = true
		}
		if ex.TypeName == "csv" || ex.TypeName == "bytes" {
			if !isLoadCall {
				if !v.inErrorTestExpr {
					return newError(
						"TypeError",
						ex.TypeLine,
						ex.TypeCol,
						v.source,
						fmt.Sprintf("Target type %q can only be used with 'load <path> as %s'", ex.TypeName, ex.TypeName),
						fmt.Sprintf("Write: load \"./path\" as %s", ex.TypeName),
					)
				}
			} else if ex.Fallback != nil && !v.inErrorTestExpr {
				return newError(
					"TypeError",
					ex.OrLine,
					ex.OrCol,
					v.source,
					fmt.Sprintf("'or' fallback cannot be used with 'load ... as %s'", ex.TypeName),
					"Handle file loading errors separately; 'or' fallbacks are for value conversions.",
				)
			}
			if ex.Fallback != nil {
				if err := v.checkExpr(ex.Fallback); err != nil {
					return err
				}
				if err := v.checkNotCondition(ex.Fallback); err != nil {
					return err
				}
			}
			return nil
		}
		if isLoadCall && ex.Fallback != nil {
			if err := v.checkExpr(ex.Fallback); err != nil {
				return err
			}
			if err := v.checkNotCondition(ex.Fallback); err != nil {
				return err
			}
			if !v.inErrorTestExpr {
				return newError(
					"TypeError",
					ex.OrLine,
					ex.OrCol,
					v.source,
					"'or' fallback cannot be used with 'load ... as text'",
					"Handle file loading errors separately; 'or' fallbacks are for value conversions.",
				)
			}
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
		if err := v.checkExpr(ex.Right); err != nil {
			return err
		}
		if err := v.checkNotCondition(ex.Right); err != nil {
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
		if v.swarmDepth > 0 && ex.Module == "" && ex.Callee == "ask" && !v.inErrorTestExpr {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				"'ask' cannot be used inside 'swarm loop'",
				"Read interactive input before 'swarm loop', not inside parallel workers.",
			)
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

func isFailCallExpr(e Expr) bool {
	call, ok := e.(*CallExpr)
	return ok && call.Module == "" && call.Callee == "fail"
}

func (v *typeValidator) staticConcreteType(st varTypeState) string {
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
			cur = vr.recordName
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

func (v *typeValidator) classifyMatchTarget(target Expr) (isList bool, isText bool, badType string) {
	st := v.inferExprTypeState(target)
	if st.listElemType != "" {
		if st.listElemType == "text" || st.listElemType == "any" {
			return true, false, ""
		}
		return false, false, "list of " + st.listElemType
	}
	if st.exactRecord != "" {
		return false, false, st.exactRecord
	}
	if st.exactBaseType == "number" {
		return false, false, "number"
	}
	if st.exactBaseType == "text" {
		return false, true, ""
	}
	if st.unionName != "" || (st.inlineUnionLabel != "" && len(st.inlineUnionVariants) > 0) {
		ct := v.staticConcreteType(st)
		switch ct {
		case "text":
			return false, true, ""
		case "":
			return false, false, ""
		default:
			return false, false, ct
		}
	}
	return false, false, ""
}

func effectiveMatchCaptureType(c string) string {
	if c == "" {
		return "text"
	}
	return c
}

func (v *typeValidator) captureTypeState(castType string) varTypeState {
	if castType == "" {
		return varTypeState{exactNonRecord: true, exactBaseType: "text"}
	}
	return v.typeStateFromName(castType)
}

func (v *typeValidator) bindMatchPatternTypes(pat MatchPattern, scope *typeScope) {
	for _, seg := range pat.Segments {
		if seg.Kind != MatchSegCapture {
			continue
		}
		if _, exists := scope.vars[seg.VarName]; exists {
			continue
		}
		scope.vars[seg.VarName] = v.captureTypeState(seg.CastType)
	}
}

func (v *typeValidator) validateAndBindMatchPattern(pat MatchPattern, scope *typeScope) error {
	firstSeg := make(map[string]MatchSegment)
	for _, seg := range pat.Segments {
		if seg.Kind != MatchSegCapture {
			continue
		}
		if err := v.checkNameNotCallable(seg.VarName, seg.Line, seg.Col, "capture variable"); err != nil {
			return err
		}
		if seg.ExplicitCast {
			tLine, tCol := seg.TypeLine, seg.TypeCol
			if tLine == 0 {
				tLine, tCol = seg.Line, seg.Col
			}
			if seg.CastType == "csv" || seg.CastType == "bytes" {
				return newError(
					"TypeError",
					tLine,
					tCol,
					v.source,
					fmt.Sprintf("Type %q cannot be used in match capture", seg.CastType),
					"Match captures support 'text', 'number', or a scalar union type.",
				)
			}
			if err := v.validateTypeTarget(seg.CastType, tLine, tCol, "as", false); err != nil {
				return err
			}
			if info, ok := v.known[seg.CastType]; ok && info.isRecord {
				return newError(
					"TypeError",
					tLine,
					tCol,
					v.source,
					fmt.Sprintf("Record type %q cannot be used directly in match capture", seg.CastType),
					"Capture atomic fields first (text, number, or union), then construct the record with 'with' after '->'.",
				)
			}
			if variants, ok := v.resolvedUnions[seg.CastType]; ok {
				hasScalar := false
				for _, vr := range variants {
					if vr.kind == variantStringLit || vr.kind == variantNumberLit || (vr.kind == variantBaseType && (vr.baseType == "text" || vr.baseType == "number")) {
						hasScalar = true
						break
					}
				}
				if !hasScalar {
					return newError(
						"TypeError",
						tLine,
						tCol,
						v.source,
						fmt.Sprintf("Union %q contains only record variants and cannot match text captures", seg.CastType),
						"Match captures can only cast to 'text', 'number', or unions with text/number variants.",
					)
				}
			}
		}

		if prev, seen := firstSeg[seg.VarName]; seen {
			if seg.ExplicitCast && effectiveMatchCaptureType(seg.CastType) != effectiveMatchCaptureType(prev.CastType) {
				tLine, tCol := seg.TypeLine, seg.TypeCol
				if tLine == 0 {
					tLine, tCol = seg.Line, seg.Col
				}
				return newError(
					"TypeError",
					tLine,
					tCol,
					v.source,
					fmt.Sprintf("Conflicting types for repeated capture %q in match pattern (%s vs %s)", seg.VarName, effectiveMatchCaptureType(prev.CastType), effectiveMatchCaptureType(seg.CastType)),
					fmt.Sprintf("Use consistent types for repeated '{%s}', or write '{%s as %s}' once and repeat it as '{%s}'.", seg.VarName, seg.VarName, effectiveMatchCaptureType(prev.CastType), seg.VarName),
				)
			}
			continue
		}
		firstSeg[seg.VarName] = seg
		scope.vars[seg.VarName] = v.captureTypeState(seg.CastType)
	}
	return nil
}

func (v *typeValidator) checkMatchListResultExpr(expectedType *string, expr Expr, inElse bool) error {
	if v.inErrorTestExpr || expr == nil || isFailCallExpr(expr) {
		return nil
	}
	ct := v.staticConcreteType(v.inferExprTypeState(expr))
	if ct == "" {
		return nil
	}
	if *expectedType == "" {
		*expectedType = ct
		return nil
	}
	if ct != *expectedType {
		bLine, bCol := expr.Pos()
		hint := "All branches in a list 'match' must return compatible types (use 'def' records or 'as text')."
		if inElse {
			hint = "All branches in a list 'match' (including 'else') must return compatible types."
		}
		return newError(
			"TypeError",
			bLine,
			bCol,
			v.source,
			fmt.Sprintf("Mixed result types in list 'match': expected %s, got %s", *expectedType, ct),
			hint,
		)
	}
	return nil
}

func (v *typeValidator) checkMatchStmt(st *MatchStmt, inFunction bool) error {
	if err := v.checkExpr(st.Target); err != nil {
		return err
	}
	if err := v.checkNotCondition(st.Target); err != nil {
		return err
	}
	isList, _, badType := v.classifyMatchTarget(st.Target)
	if !v.inErrorTestExpr && badType != "" {
		return newError(
			"TypeError",
			st.Line,
			st.Col,
			v.source,
			fmt.Sprintf("'match' requires text or a list of text, got %s", badType),
			"Pass a text string or a list of text items to 'match'.",
		)
	}

	if isList {
		if !inFunction && !v.inErrorTestExpr {
			return newError(
				"TypeError",
				st.Line,
				st.Col,
				v.source,
				"Cannot use list 'match' without assigning its result at top level",
				"Assign the result (e.g. 'result = match items ... end') or return it from a 'to' function.",
			)
		}
		matchScope := v.pushScope()
		matchScope.vars["it"] = varTypeState{exactNonRecord: true, exactBaseType: "text"}
		v.loopDepth++
		defer func() {
			v.loopDepth--
			v.popScope()
		}()

		var expectedType string
		for _, arm := range st.Arms {
			es, isExpr := arm.Action.(*ExprStmt)
			if !isExpr {
				return newError(
					"TypeError",
					arm.Line,
					arm.Col,
					v.source,
					"List 'match' transforms items with expressions and cannot execute statements",
					"Use 'loop items ... end' with 'match it ... end' to run statements for each item.",
				)
			}
			armScope := v.pushScope()
			if err := v.validateAndBindMatchPattern(arm.Pattern, armScope); err != nil {
				v.popScope()
				return err
			}
			err := v.checkExpr(es.Expression)
			if err == nil {
				err = v.checkNotCondition(es.Expression)
			}
			if err == nil {
				err = v.checkMatchListResultExpr(&expectedType, es.Expression, false)
			}
			v.popScope()
			if err != nil {
				return err
			}
		}

		if len(st.ElseBody) > 0 {
			lastStmt := st.ElseBody[len(st.ElseBody)-1]
			lastEs, isExpr := lastStmt.(*ExprStmt)
			if !isExpr {
				elLine, elCol := lastStmt.Pos()
				return newError(
					"TypeError",
					elLine,
					elCol,
					v.source,
					"List 'match' 'else' branch must end with an expression",
					"End 'else' with a result expression (or write 'else -> <expr>').",
				)
			}
			v.pushScope()
			err := v.checkStmts(st.ElseBody, false)
			if err == nil {
				err = v.checkNotCondition(lastEs.Expression)
			}
			if err == nil {
				err = v.checkMatchListResultExpr(&expectedType, lastEs.Expression, true)
			}
			v.popScope()
			if err != nil {
				return err
			}
		}
		return nil
	}

	for _, arm := range st.Arms {
		if !inFunction && !v.inErrorTestExpr {
			if es, isExpr := arm.Action.(*ExprStmt); isExpr {
				if _, isCall := es.Expression.(*CallExpr); !isCall {
					line, col := es.Pos()
					return newError(
						"TypeError",
						line,
						col,
						v.source,
						"Cannot return a value from 'match' at top level; use 'say' or 'fail' instead.",
						"Top-level 'match' branches can run statements like 'say \"...\"', assignments, or 'fail \"...\"'.",
					)
				}
			}
		}
		armScope := v.pushScope()
		err := v.validateAndBindMatchPattern(arm.Pattern, armScope)
		if err == nil {
			err = v.checkStmt(arm.Action, inFunction)
		}
		v.popScope()
		if err != nil {
			return err
		}
	}

	if len(st.ElseBody) > 0 {
		if !inFunction && !v.inErrorTestExpr {
			lastStmt := st.ElseBody[len(st.ElseBody)-1]
			if es, isExpr := lastStmt.(*ExprStmt); isExpr {
				if _, isCall := es.Expression.(*CallExpr); !isCall {
					line, col := es.Pos()
					return newError(
						"TypeError",
						line,
						col,
						v.source,
						"Cannot return a value from 'match' at top level; use 'say' or 'fail' instead.",
						"Top-level 'match' branches can run statements like 'say \"...\"', assignments, or 'fail \"...\"'.",
					)
				}
			}
		}
		v.pushScope()
		err := v.checkStmts(st.ElseBody, inFunction)
		v.popScope()
		if err != nil {
			return err
		}
	}
	return nil
}

func (v *typeValidator) checkMatchExpr(ex *MatchExpr) error {
	if err := v.checkExpr(ex.Target); err != nil {
		return err
	}
	if err := v.checkNotCondition(ex.Target); err != nil {
		return err
	}
	isList, isText, badType := v.classifyMatchTarget(ex.Target)
	if !v.inErrorTestExpr && badType != "" {
		return newError(
			"TypeError",
			ex.Line,
			ex.Col,
			v.source,
			fmt.Sprintf("'match' requires text or a list of text, got %s", badType),
			"Pass a text string or a list of text items to 'match'.",
		)
	}
	if !v.inErrorTestExpr && isText && !ex.HasElse {
		return newError(
			"TypeError",
			ex.Line,
			ex.Col,
			v.source,
			"'match' expression on text requires an 'else' branch",
			"Add 'else -> <fallback>' before 'end', or use statement form without assignment.",
		)
	}

	treatAsList := isList || (!isText && !ex.HasElse)
	bindIt := isList || !isText
	if bindIt {
		matchScope := v.pushScope()
		matchScope.vars["it"] = varTypeState{exactNonRecord: true, exactBaseType: "text"}
		v.loopDepth++
		defer func() {
			v.loopDepth--
			v.popScope()
		}()
	}

	expectedType := ""
	for _, arm := range ex.Arms {
		armScope := v.pushScope()
		err := v.validateAndBindMatchPattern(arm.Pattern, armScope)
		if err == nil {
			err = v.checkExpr(arm.Value)
		}
		if err == nil {
			err = v.checkNotCondition(arm.Value)
		}
		if err == nil && treatAsList {
			err = v.checkMatchListResultExpr(&expectedType, arm.Value, false)
		}
		v.popScope()
		if err != nil {
			return err
		}
	}

	if ex.HasElse && len(ex.ElseBody) > 0 {
		v.pushScope()
		err := v.checkStmts(ex.ElseBody, false)
		if err == nil {
			if lastEs, ok := ex.ElseBody[len(ex.ElseBody)-1].(*ExprStmt); ok {
				err = v.checkNotCondition(lastEs.Expression)
				if err == nil && treatAsList {
					err = v.checkMatchListResultExpr(&expectedType, lastEs.Expression, true)
				}
			}
		}
		v.popScope()
		if err != nil {
			return err
		}
	}

	return nil
}

func (v *typeValidator) unifyScalarTypeStates(states []varTypeState) varTypeState {
	if len(states) == 0 {
		return varTypeState{}
	}
	first := states[0]
	sameRec := first.exactRecord != ""
	sameBase := first.exactBaseType != ""
	sameUnion := first.unionName != ""
	sameList := first.listElemType != ""
	allNonRec := first.exactNonRecord
	commonConcrete := v.staticConcreteType(first)

	for _, st := range states[1:] {
		if st.exactRecord == "" || st.exactRecord != first.exactRecord {
			sameRec = false
		}
		if st.exactBaseType == "" || st.exactBaseType != first.exactBaseType {
			sameBase = false
		}
		if st.unionName == "" || st.unionName != first.unionName {
			sameUnion = false
		}
		if st.listElemType == "" || st.listElemType != first.listElemType {
			sameList = false
		}
		if !st.exactNonRecord {
			allNonRec = false
		}
		ct := v.staticConcreteType(st)
		if ct == "" || ct != commonConcrete {
			commonConcrete = ""
		}
	}
	if sameRec {
		return varTypeState{exactRecord: first.exactRecord}
	}
	if sameUnion {
		return varTypeState{unionName: first.unionName}
	}
	if sameBase {
		return varTypeState{exactNonRecord: true, exactBaseType: first.exactBaseType}
	}
	if sameList {
		return varTypeState{listElemType: first.listElemType}
	}
	if commonConcrete == "text" || commonConcrete == "number" {
		return varTypeState{exactNonRecord: true, exactBaseType: commonConcrete}
	}
	if allNonRec {
		return varTypeState{exactNonRecord: true}
	}
	return varTypeState{}
}

func (v *typeValidator) inferMatchExprTypeState(ex *MatchExpr) varTypeState {
	isList, isText, _ := v.classifyMatchTarget(ex.Target)
	if !isList && !isText && !ex.HasElse {
		isList = true
	}
	if !isList && !isText {
		return varTypeState{}
	}

	if isList {
		matchScope := v.pushScope()
		matchScope.vars["it"] = varTypeState{exactNonRecord: true, exactBaseType: "text"}
		v.loopDepth++
		defer func() {
			v.loopDepth--
			v.popScope()
		}()
	}

	var states []varTypeState
	for _, arm := range ex.Arms {
		if isFailCallExpr(arm.Value) {
			continue
		}
		armScope := v.pushScope()
		v.bindMatchPatternTypes(arm.Pattern, armScope)
		states = append(states, v.inferExprTypeState(arm.Value))
		v.popScope()
	}

	if ex.HasElse && len(ex.ElseBody) > 0 {
		elseScope := v.pushScope()
		for i, s := range ex.ElseBody {
			if i < len(ex.ElseBody)-1 {
				if as, ok := s.(*AssignStmt); ok {
					elseScope.vars[as.Name] = v.inferExprTypeState(as.Value)
				}
			} else if es, ok := s.(*ExprStmt); ok && !isFailCallExpr(es.Expression) {
				states = append(states, v.inferExprTypeState(es.Expression))
			}
		}
		v.popScope()
	}

	if isList {
		if len(states) == 0 {
			return varTypeState{listElemType: "any"}
		}
		unified := v.unifyScalarTypeStates(states)
		switch {
		case unified.exactRecord != "":
			return varTypeState{listElemType: unified.exactRecord}
		case unified.unionName != "":
			return varTypeState{listElemType: unified.unionName}
		case unified.exactBaseType != "":
			return varTypeState{listElemType: unified.exactBaseType}
		default:
			return varTypeState{listElemType: "any"}
		}
	}

	return v.unifyScalarTypeStates(states)
}

func (v *typeValidator) collectSwarmReturnExprs(stmts []Stmt) []Expr {
	if len(stmts) == 0 {
		return nil
	}
	var out []Expr
	for i, s := range stmts {
		isLast := i == len(stmts)-1
		switch st := s.(type) {
		case *CheckGuardStmt:
			for _, r := range st.Rules {
				if r.Action != nil {
					if es, ok := r.Action.(*ExprStmt); ok {
						out = append(out, es.Expression)
					}
				}
			}
			if len(st.ElseBody) > 0 {
				out = append(out, v.collectSwarmReturnExprs(st.ElseBody)...)
			}
		case *MatchStmt:
			for _, arm := range st.Arms {
				if arm.Action != nil {
					if es, ok := arm.Action.(*ExprStmt); ok {
						out = append(out, es.Expression)
					}
				}
			}
			if len(st.ElseBody) > 0 {
				out = append(out, v.collectSwarmReturnExprs(st.ElseBody)...)
			}
		case *ExprStmt:
			if isLast {
				out = append(out, st.Expression)
			}
		}
	}
	return out
}

func (v *typeValidator) checkSwarmLoopExpr(ex *SwarmLoopExpr) error {
	if err := v.checkExpr(ex.Iterable); err != nil {
		return err
	}
	if err := v.checkNotCondition(ex.Iterable); err != nil {
		return err
	}
	if !v.inErrorTestExpr {
		switch lit := ex.Iterable.(type) {
		case *NumberExpr:
			return newError(
				"TypeError",
				lit.Line,
				lit.Col,
				v.source,
				fmt.Sprintf("'swarm loop' requires a list or range, got number. Use 'swarm loop range 1 %s' instead.", FormatValue(lit.Value)),
				fmt.Sprintf("Write: swarm loop range 1 %s ... end", FormatValue(lit.Value)),
			)
		case *StringExpr, *InterpStringExpr:
			line, col := ex.Iterable.Pos()
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				"'swarm loop' requires a list or range, got text",
				"Iterate over a list like 'swarm loop xs ... end' or a range like 'swarm loop range 1 5 ... end'.",
			)
		}
	}

	outerScope := v.scope
	workerScope := v.pushScope()
	workerScope.isWorkerRoot = true
	workerScope.vars["it"] = v.inferIterableElemState(ex.Iterable)

	prevLoopDepth := v.loopDepth
	prevSwarmDepth := v.swarmDepth
	prevDirectInSwarm := v.directInSwarm
	prevWorkerAssigned := v.workerAssigned
	prevWorkerOuterScope := v.workerOuterScope

	v.loopDepth++
	v.swarmDepth++
	v.directInSwarm = true
	v.workerAssigned = collectAssignedNames(ex.Body)
	v.workerOuterScope = outerScope

	defer func() {
		v.loopDepth = prevLoopDepth
		v.swarmDepth = prevSwarmDepth
		v.directInSwarm = prevDirectInSwarm
		v.workerAssigned = prevWorkerAssigned
		v.workerOuterScope = prevWorkerOuterScope
		v.popScope()
	}()

	if err := v.checkStmts(ex.Body, true); err != nil {
		return err
	}

	if !v.inErrorTestExpr {
		retExprs := v.collectSwarmReturnExprs(ex.Body)
		expectedType := ""
		for _, re := range retExprs {
			if re == nil || isFailCallExpr(re) {
				continue
			}
			if err := v.checkNotCondition(re); err != nil {
				return err
			}
			ct := v.staticConcreteType(v.inferExprTypeState(re))
			if ct == "" {
				continue
			}
			if expectedType == "" {
				expectedType = ct
			} else if ct != expectedType {
				bLine, bCol := re.Pos()
				return newError(
					"TypeError",
					bLine,
					bCol,
					v.source,
					fmt.Sprintf("Mixed result types in 'swarm loop': expected %s, got %s", expectedType, ct),
					"All iterations of 'swarm loop' must return compatible types.",
				)
			}
		}
	}
	return nil
}

func (v *typeValidator) inferSwarmLoopTypeState(ex *SwarmLoopExpr) varTypeState {
	outerScope := v.scope
	workerScope := v.pushScope()
	workerScope.isWorkerRoot = true
	workerScope.vars["it"] = v.inferIterableElemState(ex.Iterable)
	prevLoopDepth := v.loopDepth
	v.loopDepth++
	defer func() {
		v.loopDepth = prevLoopDepth
		v.popScope()
	}()

	var states []varTypeState
	var walkBody func(stmts []Stmt, inScope *typeScope)
	walkBody = func(stmts []Stmt, inScope *typeScope) {
		for i, s := range stmts {
			isLast := i == len(stmts)-1
			switch st := s.(type) {
			case *AssignStmt:
				inScope.vars[st.Name] = v.inferExprTypeState(st.Value)
			case *CheckGuardStmt:
				for _, r := range st.Rules {
					if r.Action != nil {
						if es, ok := r.Action.(*ExprStmt); ok && !isFailCallExpr(es.Expression) {
							states = append(states, v.inferExprTypeState(es.Expression))
						}
					}
				}
				if len(st.ElseBody) > 0 {
					walkBody(st.ElseBody, inScope)
				}
			case *MatchStmt:
				for _, arm := range st.Arms {
					if arm.Action != nil {
						if es, ok := arm.Action.(*ExprStmt); ok && !isFailCallExpr(es.Expression) {
							states = append(states, v.inferExprTypeState(es.Expression))
						}
					}
				}
				if len(st.ElseBody) > 0 {
					walkBody(st.ElseBody, inScope)
				}
			case *ExprStmt:
				if isLast && !isFailCallExpr(st.Expression) {
					states = append(states, v.inferExprTypeState(st.Expression))
				}
			}
		}
	}
	_ = outerScope
	walkBody(ex.Body, workerScope)

	if len(states) == 0 {
		return varTypeState{listElemType: "any"}
	}
	unified := v.unifyScalarTypeStates(states)
	switch {
	case unified.exactRecord != "":
		return varTypeState{listElemType: unified.exactRecord}
	case unified.unionName != "":
		return varTypeState{listElemType: unified.unionName}
	case unified.exactBaseType != "":
		return varTypeState{listElemType: unified.exactBaseType}
	default:
		return varTypeState{listElemType: "any"}
	}
}

func (v *typeValidator) resolveFallbackLiteralVariant(e Expr) (resolvedUnionVariant, bool) {
	switch ex := e.(type) {
	case *IdentExpr:
		if cExpr, isConst := v.constExprs[ex.Name]; isConst {
			return v.resolveFallbackLiteralVariant(cExpr)
		}
		return resolvedUnionVariant{}, false
	case *FieldExpr:
		if id, ok := ex.Target.(*IdentExpr); ok {
			if mod, isMod := v.modules[id.Name]; isMod {
				if cExpr, isConst := mod.consts[ex.Field]; isConst {
					return v.resolveFallbackLiteralVariant(cExpr)
				}
			}
		}
		return resolvedUnionVariant{}, false
	}
	variants, ok := v.exprToPatternVariants(e)
	if !ok || len(variants) != 1 || variants[0].kind == variantBaseType || variants[0].kind == variantRecord {
		return resolvedUnionVariant{}, false
	}
	return variants[0], true
}

func (v *typeValidator) unionVariantsForTypeState(st varTypeState) []resolvedUnionVariant {
	if st.inlineUnionLabel != "" && len(st.inlineUnionVariants) > 0 {
		return st.inlineUnionVariants
	}
	if st.unionName != "" {
		return v.resolvedUnions[st.unionName]
	}
	return nil
}

func (v *typeValidator) unionContainsOnlyBaseType(variants []resolvedUnionVariant, base string) bool {
	if len(variants) == 0 {
		return false
	}
	for _, vr := range variants {
		switch base {
		case "number":
			if vr.kind != variantNumberLit && !(vr.kind == variantBaseType && vr.baseType == "number") {
				return false
			}
		case "text":
			if vr.kind != variantStringLit && !(vr.kind == variantBaseType && vr.baseType == "text") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (v *typeValidator) checkCastFallback(ex *CastExpr) error {
	if err := v.checkExpr(ex.Fallback); err != nil {
		return err
	}
	if err := v.checkNotCondition(ex.Fallback); err != nil {
		return err
	}
	if v.inErrorTestExpr {
		return nil
	}

	if info, ok := v.known[ex.TypeName]; ok && info.isRecord {
		return newError(
			"TypeError",
			ex.TypeLine,
			ex.TypeCol,
			v.source,
			fmt.Sprintf("'or' fallback cannot be used with record type %q", ex.TypeName),
			"Record contracts ('as <def>') must remain strict; use '<def> with ...' for default instances.",
		)
	}
	uVariants, isUnion := v.resolvedUnions[ex.TypeName]
	if isUnion {
		hasScalar := false
		for _, vr := range uVariants {
			if vr.kind == variantStringLit || vr.kind == variantNumberLit || vr.kind == variantBaseType {
				hasScalar = true
				break
			}
		}
		if !hasScalar {
			return newError(
				"TypeError",
				ex.TypeLine,
				ex.TypeCol,
				v.source,
				fmt.Sprintf("'or' fallback cannot be used with record-only union %q", ex.TypeName),
				"Unions containing only 'def' records must remain strict; 'or' fallbacks support number, text, or scalar unions.",
			)
		}
	}

	targetSt := v.inferExprTypeState(ex.Target)
	if targetSt.exactRecord != "" || (targetSt.listElemType != "" && v.known[targetSt.listElemType].isRecord) {
		bad := targetSt.exactRecord
		if bad == "" {
			bad = "list of " + targetSt.listElemType
		}
		return newError(
			"TypeError",
			ex.OrLine,
			ex.OrCol,
			v.source,
			fmt.Sprintf("Cannot convert %s with 'as %s or ...' fallback", bad, ex.TypeName),
			"Records cannot be cast with 'or' fallbacks; access an atomic field (e.g. 'rec.field as ...') instead.",
		)
	}

	fbSt := v.inferExprTypeState(ex.Fallback)
	fbLine, fbCol := ex.Fallback.Pos()
	if fbSt.listElemType != "" {
		return newError(
			"TypeError",
			fbLine,
			fbCol,
			v.source,
			fmt.Sprintf("Fallback for 'as %s' must be %s, got list of %s", ex.TypeName, ex.TypeName, fbSt.listElemType),
			"Provide a single element value after 'or' (even when casting a list like 'lines as number or 0').",
		)
	}

	if ex.TypeName == "number" || ex.TypeName == "text" {
		if fbSt.exactRecord != "" {
			return newError(
				"TypeError",
				fbLine,
				fbCol,
				v.source,
				fmt.Sprintf("Fallback for 'as %s' must be %s, got %s", ex.TypeName, ex.TypeName, fbSt.exactRecord),
				fmt.Sprintf("Provide a %s value after 'or'.", ex.TypeName),
			)
		}
		if fbSt.exactBaseType != "" && fbSt.exactBaseType != ex.TypeName {
			return newError(
				"TypeError",
				fbLine,
				fbCol,
				v.source,
				fmt.Sprintf("Fallback for 'as %s' must be %s, got %s", ex.TypeName, ex.TypeName, fbSt.exactBaseType),
				fmt.Sprintf("Provide a %s value after 'or'.", ex.TypeName),
			)
		}
		if fbUnion := v.unionVariantsForTypeState(fbSt); len(fbUnion) > 0 && !v.unionContainsOnlyBaseType(fbUnion, ex.TypeName) {
			return newError(
				"TypeError",
				fbLine,
				fbCol,
				v.source,
				fmt.Sprintf("Fallback union for 'as %s' is not guaranteed to be %s", ex.TypeName, ex.TypeName),
				fmt.Sprintf("Provide a %s value or a pure-%s union after 'or'.", ex.TypeName, ex.TypeName),
			)
		}
		return nil
	}

	// Union target: strict AOT verification (Decision 5.4).
	if litVar, isLit := v.resolveFallbackLiteralVariant(ex.Fallback); isLit {
		if !v.variantCoveredByUnion(litVar, uVariants) {
			var allowedDisplay []string
			var strCandidates []string
			for _, uv := range uVariants {
				allowedDisplay = append(allowedDisplay, uv.display())
				if uv.kind == variantStringLit {
					strCandidates = append(strCandidates, uv.strVal)
				}
			}
			hint := fmt.Sprintf("Valid values for %s: %s.", ex.TypeName, strings.Join(allowedDisplay, " | "))
			if litVar.kind == variantStringLit {
				if suggestion := closestTypeName(litVar.strVal, strCandidates); suggestion != "" {
					hint = fmt.Sprintf("Did you mean %q? %s", suggestion, hint)
				}
			}
			return newError(
				"TypeError",
				fbLine,
				fbCol,
				v.source,
				fmt.Sprintf("Fallback %s is not a valid value of union %q (%s)", litVar.display(), ex.TypeName, strings.Join(allowedDisplay, " | ")),
				hint,
			)
		}
		return nil
	}

	if fbUnion := v.unionVariantsForTypeState(fbSt); len(fbUnion) > 0 {
		for _, vr := range fbUnion {
			if !v.variantCoveredByUnion(vr, uVariants) {
				var allowedDisplay []string
				for _, uv := range uVariants {
					allowedDisplay = append(allowedDisplay, uv.display())
				}
				return newError(
					"TypeError",
					fbLine,
					fbCol,
					v.source,
					fmt.Sprintf("Fallback variant %s is not covered by union %q (%s)", vr.display(), ex.TypeName, strings.Join(allowedDisplay, " | ")),
					fmt.Sprintf("Valid values for %s: %s.", ex.TypeName, strings.Join(allowedDisplay, " | ")),
				)
			}
		}
		return nil
	}

	if fbSt.exactRecord != "" {
		recVar := resolvedUnionVariant{kind: variantRecord, recordName: fbSt.exactRecord}
		if !v.variantCoveredByUnion(recVar, uVariants) {
			return newError(
				"TypeError",
				fbLine,
				fbCol,
				v.source,
				fmt.Sprintf("Fallback record %q is not a member of union %q", fbSt.exactRecord, ex.TypeName),
				fmt.Sprintf("Use a valid member of union %s after 'or'.", ex.TypeName),
			)
		}
		if id, isIdent := ex.Fallback.(*IdentExpr); isIdent && v.defs[id.Name] != nil {
			return newError(
				"TypeError",
				fbLine,
				fbCol,
				v.source,
				fmt.Sprintf("Cannot use 'def' blueprint %q as fallback without 'with'", id.Name),
				fmt.Sprintf("Instantiate a concrete record, e.g. '%s with ...'.", id.Name),
			)
		}
		return nil
	}

	if fbSt.exactBaseType != "" {
		baseVar := resolvedUnionVariant{kind: variantBaseType, baseType: fbSt.exactBaseType}
		if !v.variantCoveredByUnion(baseVar, uVariants) {
			var allowedDisplay []string
			for _, uv := range uVariants {
				allowedDisplay = append(allowedDisplay, uv.display())
			}
			return newError(
				"TypeError",
				fbLine,
				fbCol,
				v.source,
				fmt.Sprintf("Fallback of type %s is not statically guaranteed to belong to union %q (%s)", fbSt.exactBaseType, ex.TypeName, strings.Join(allowedDisplay, " | ")),
				fmt.Sprintf("Use an allowed literal, a 'def' constant, or a value already typed as '%s'.", ex.TypeName),
			)
		}
		return nil
	}

	return nil
}
func (v *typeValidator) checkInboxElementType(elemType string, valExpr Expr) error {
	valSt := v.inferExprTypeState(valExpr)
	vLine, vCol := valExpr.Pos()
	if valSt.inboxElemType != "" {
		return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot 'put' inbox into inbox of %s", elemType), fmt.Sprintf("Provide a value of type %q.", elemType))
	}
	if valSt.listElemType != "" {
		return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot 'put' list of %s into inbox of %s", valSt.listElemType, elemType), fmt.Sprintf("Provide a value of type %q.", elemType))
	}
	if elemType == "number" || elemType == "text" {
		if valSt.exactRecord != "" {
			return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot 'put' %s into inbox of %s", valSt.exactRecord, elemType), fmt.Sprintf("Provide a %s value.", elemType))
		}
		if valSt.exactBaseType != "" && valSt.exactBaseType != elemType {
			return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot 'put' %s into inbox of %s", valSt.exactBaseType, elemType), fmt.Sprintf("Provide a %s value.", elemType))
		}
		if uVars := v.unionVariantsForTypeState(valSt); len(uVars) > 0 && !v.unionContainsOnlyBaseType(uVars, elemType) {
			return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot 'put' union value into inbox of %s", elemType), fmt.Sprintf("Provide a %s value.", elemType))
		}
		return nil
	}
	if v.defs[elemType] != nil {
		if valSt.exactBaseType != "" || valSt.exactNonRecord || valSt.unionName != "" || (valSt.exactRecord != "" && valSt.exactRecord != elemType) {
			got := v.staticConcreteType(valSt)
			if got == "" {
				got = "incompatible value"
			}
			return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot 'put' %s into inbox of %s", got, elemType), fmt.Sprintf("Pass a %s record instance created with '%s with ...'.", elemType, elemType))
		}
		return nil
	}
	if uVariants, isUnion := v.resolvedUnions[elemType]; isUnion {
		if litVar, isLit := v.resolveFallbackLiteralVariant(valExpr); isLit {
			if !v.variantCoveredByUnion(litVar, uVariants) {
				return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Value %s is not valid for inbox union %q", litVar.display(), elemType), fmt.Sprintf("Provide a valid member of union %s.", elemType))
			}
			return nil
		}
		if valSt.exactRecord != "" {
			recVar := resolvedUnionVariant{kind: variantRecord, recordName: valSt.exactRecord}
			if !v.variantCoveredByUnion(recVar, uVariants) {
				return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Record %q is not a member of inbox union %q", valSt.exactRecord, elemType), fmt.Sprintf("Provide a valid member of union %s.", elemType))
			}
			return nil
		}
		if valUnion := v.unionVariantsForTypeState(valSt); len(valUnion) > 0 {
			for _, vr := range valUnion {
				if !v.variantCoveredByUnion(vr, uVariants) {
					return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Variant %s is not covered by inbox union %q", vr.display(), elemType), fmt.Sprintf("Provide a valid member of union %s.", elemType))
				}
			}
			return nil
		}
		if valSt.exactBaseType != "" {
			baseVar := resolvedUnionVariant{kind: variantBaseType, baseType: valSt.exactBaseType}
			if !v.variantCoveredByUnion(baseVar, uVariants) {
				return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Value of type %s is not guaranteed to belong to inbox union %q", valSt.exactBaseType, elemType), fmt.Sprintf("Provide a valid member of union %s.", elemType))
			}
			return nil
		}
	}
	return nil
}

func (v *typeValidator) validateTypeTarget(typeName string, line, col int, ctx string, requireRecord bool) error {
	switch typeName {
	case "text", "number":
		if requireRecord {
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Built-in type %q cannot be used with 'list as ... with'", typeName),
				"Only a 'def <name> ... end' record blueprint can be used with 'list as <name> with'.",
			)
		}
		return nil
	case "bool", "boolean":
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Boolean (%q) is not a built-in type in Ease", typeName),
			"Ease models states with explicit domain union types instead of booleans, e.g.: def status = \"active\" | \"inactive\".",
		)
	case "string":
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Unknown type 'string' after '%s'", ctx),
			"Did you mean 'text'? Ease uses 'text' instead of 'string'.",
		)
	}

	if info, ok := v.known[typeName]; ok {
		if requireRecord && !info.isRecord {
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Union %q is not a 'def' record blueprint for 'list as ... with'", typeName),
				fmt.Sprintf("Define a record with 'def %s ... end' to instantiate rows.", typeName),
			)
		}
		if !info.imported && line > 0 && info.line > line {
			return newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Type %q is used after '%s' before it is defined (defined on line %d)", typeName, ctx, info.line),
				fmt.Sprintf("Move 'def %s' above line %d.", typeName, line),
			)
		}
		return nil
	}

	if constLine, isConst := v.consts[typeName]; isConst {
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Constant %q (defined on line %d) is not a type or 'def' blueprint", typeName, constLine),
			fmt.Sprintf("After '%s', only built-in types (text, number) or a 'def' blueprint are allowed.", ctx),
		)
	}

	candidates := v.availableTypeNames(requireRecord)
	hint := fmt.Sprintf("After '%s', only built-in types (text, number) or a previously defined/imported 'def' blueprint are allowed.", ctx)
	if suggestion := closestTypeName(typeName, candidates); suggestion != "" {
		hint = fmt.Sprintf("Did you mean %q? %s", suggestion, hint)
	} else if len(candidates) > 0 {
		hint = fmt.Sprintf("%s Available types: %s.", hint, strings.Join(candidates, ", "))
	}

	return newError(
		"TypeError",
		line,
		col,
		v.source,
		fmt.Sprintf("Unknown type %q after '%s'", typeName, ctx),
		hint,
	)
}

func (v *typeValidator) availableTypeNames(recordsOnly bool) []string {
	var out []string
	if !recordsOnly {
		out = append(out, "text", "number")
	}
	var defs []string
	for name, info := range v.known {
		if recordsOnly && !info.isRecord {
			continue
		}
		defs = append(defs, name)
	}
	sort.Strings(defs)
	return append(out, defs...)
}

func closestTypeName(target string, candidates []string) string {
	best := ""
	bestDist := 3 // require edit distance <= 2
	tLower := strings.ToLower(target)
	for _, c := range candidates {
		d := levenshteinDistance(tLower, strings.ToLower(c))
		if d > 0 && d < bestDist {
			bestDist = d
			best = c
		}
	}
	return best
}

func levenshteinDistance(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}
