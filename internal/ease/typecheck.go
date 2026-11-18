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
	parent *typeScope
	vars   map[string]varTypeState
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
		if err := validateProgramTypes(imp.ModuleProg, imp.ModuleSource); err != nil {
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

type typeValidator struct {
	source          string
	known           map[string]typeDefInfo
	defs            map[string]*DefDecl
	unions          map[string]*UnionDecl
	consts          map[string]int
	constExprs      map[string]Expr
	funcNames       map[string]int
	modules         map[string]modTypeInfo
	resolvedUnions  map[string][]resolvedUnionVariant
	scope           *typeScope
	inErrorTestExpr bool
	inErrorBlock    bool
	loopDepth       int
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
		loopScope := v.pushScope()
		loopScope.vars["it"] = v.inferIterableElemState(st.Iterable)
		v.loopDepth++
		err := v.checkStmts(st.Body, inFunction)
		v.loopDepth--
		v.popScope()
		return err

	case *CheckGuardStmt:
		return v.checkCheckGuardStmt([]Stmt{st}, 0, st, inFunction)

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
		if ex.TypeName == "csv" || ex.TypeName == "bytes" {
			call, isCall := ex.Target.(*CallExpr)
			if !isCall || call.Callee != "load" {
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
			}
			return nil
		}
		return v.validateTypeTarget(ex.TypeName, ex.TypeLine, ex.TypeCol, "as", false)

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

	case *CallExpr:
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
