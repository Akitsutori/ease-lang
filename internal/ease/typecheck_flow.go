package ease

import (
	"fmt"
)

func (v *typeValidator) stmtAlwaysReturnsExpr(stmts []Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	for _, s := range stmts {
		switch st := s.(type) {
		case *ReturnStmt:
			return true
		case *LoopStmt:
			if st.Iterable == nil {
				return true
			}
		}
	}
	last := stmts[len(stmts)-1]
	switch st := last.(type) {
	case *MatchStmt:
		if len(st.ElseBody) == 0 || !v.stmtAlwaysReturnsExpr(st.ElseBody) {
			return false
		}
		for _, arm := range st.Arms {
			if arm.Action != nil {
				if !v.stmtAlwaysReturnsExpr([]Stmt{arm.Action}) {
					return false
				}
			} else if !v.stmtAlwaysReturnsExpr(arm.Body) {
				return false
			}
		}
		return true
	case *CheckGuardStmt:
		if st.IsEffectBlock {
			return len(st.ElseBody) > 0 && v.stmtAlwaysReturnsExpr(st.Body) && v.stmtAlwaysReturnsExpr(st.ElseBody)
		}
		if len(st.ElseBody) > 0 {
			if !v.stmtAlwaysReturnsExpr(st.ElseBody) {
				return false
			}
			for _, r := range st.Rules {
				if r.Action == nil || !v.stmtAlwaysReturnsExpr([]Stmt{r.Action}) {
					return false
				}
			}
			return true
		}
		if len(st.Rules) >= 2 {
			for _, r := range st.Rules {
				if r.Action == nil || !v.stmtAlwaysReturnsExpr([]Stmt{r.Action}) {
					return false
				}
			}
			return true
		}
		return false
	default:
		return false
	}
}

func (v *typeValidator) findFirstReturn(stmts []Stmt) (int, int, bool) {
	for _, s := range stmts {
		switch st := s.(type) {
		case *ReturnStmt:
			return st.Line, st.Col, true
		case *CheckGuardStmt:
			if line, col, ok := v.findFirstReturn(st.Body); ok {
				return line, col, true
			}
			for _, r := range st.Rules {
				if r.Action != nil {
					if line, col, ok := v.findFirstReturn([]Stmt{r.Action}); ok {
						return line, col, true
					}
				}
			}
			if line, col, ok := v.findFirstReturn(st.ElseBody); ok {
				return line, col, true
			}
		case *LoopStmt:
			if line, col, ok := v.findFirstReturn(st.Body); ok {
				return line, col, true
			}
		case *TakeStmt:
			if line, col, ok := v.findFirstReturn(st.Body); ok {
				return line, col, true
			}
		case *MatchStmt:
			for _, arm := range st.Arms {
				if arm.Action != nil {
					if line, col, ok := v.findFirstReturn([]Stmt{arm.Action}); ok {
						return line, col, true
					}
				}
				if line, col, ok := v.findFirstReturn(arm.Body); ok {
					return line, col, true
				}
			}
			if line, col, ok := v.findFirstReturn(st.ElseBody); ok {
				return line, col, true
			}
		}
	}
	return 0, 0, false
}

// checkFuncReturnConsistency ensures functions that return a value via `-> <expr>`
// return a value on every execution path (while functions without `->` are `void` procedures).
func (v *typeValidator) checkFuncReturnConsistency(fn *FuncDecl) error {
	if fn == nil {
		return nil
	}
	if !stmtsHaveReturn(fn.Body) {
		return nil
	}
	if v.stmtAlwaysReturnsExpr(fn.Body) {
		return nil
	}
	line, col, hasRet := v.findFirstReturn(fn.Body)
	if !hasRet {
		line, col = fn.Pos()
	}
	return newError(
		"TypeError",
		line,
		col,
		v.source,
		fmt.Sprintf("Function '%s' returns a value on some paths via '->' but can finish without returning a value", fn.Name),
		"Ensure every execution path in the function ends with '-> <value>'.",
	)
}

type typeValidator struct {
	source                string
	known                 map[string]typeDefInfo
	defs                  map[string]*DefDecl
	unions                map[string]*UnionDecl
	consts                map[string]int
	constExprs            map[string]Expr
	funcNames             map[string]int
	funcDecls             map[string]*FuncDecl
	modules               map[string]modTypeInfo
	resolvedUnions        map[string][]resolvedUnionVariant
	scope                 *typeScope
	allowInternalBuiltins bool
	spawnFuncCache        map[string]bool
	inErrorTestExpr       bool
	inErrorBlock          bool
	loopDepth             int
	swarmDepth            int
	directInSwarm         bool
	workerAssigned        map[string]bool
	workerOuterScope      *typeScope
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
		case *ReturnStmt:
			if inFunction {
				return true
			}
		case *CheckGuardStmt:
			if hasLoopExit(st.Body, inFunction, directLoop) {
				return true
			}
			for _, r := range st.Rules {
				if r.Action == nil {
					continue
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
		if err := v.checkNotVoidValue(st.Value); err != nil {
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
		if err := v.checkNotVoidValue(st.Value); err != nil {
			return err
		}
		v.setVarInCurrentOrExisting(st.Name, v.inferExprTypeState(st.Value))
		return nil

	case *SayStmt:
		if err := v.checkExpr(st.Value); err != nil {
			return err
		}
		if err := v.checkNotCondition(st.Value); err != nil {
			return err
		}
		return v.checkNotVoidValue(st.Value)

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

	case *ReturnStmt:
		if !inFunction && !v.inErrorTestExpr {
			return newError(
				"TypeError",
				st.Line,
				st.Col,
				v.source,
				"Cannot use '-> <value>' at top level",
				"Return values with '-> <value>' only inside 'to' functions or 'swarm loop'.",
			)
		}
		if err := v.checkExpr(st.Value); err != nil {
			return err
		}
		if err := v.checkNotCondition(st.Value); err != nil {
			return err
		}
		return v.checkNotVoidValue(st.Value)

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
		if v.swarmDepth > 0 && !v.inErrorTestExpr {
			return newError(
				"TypeError",
				st.Line,
				st.Col,
				v.source,
				"'spawn' cannot be used inside 'swarm loop'; 'swarm loop' already runs iterations in parallel",
				"Move 'spawn' outside 'swarm loop' or use top-level inbox pipelines.",
			)
		}
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
	if st.IsEffectBlock {
		if err := v.checkExpr(st.Cond); err != nil {
			return err
		}
		bodyScope := v.pushScope()
		v.applyCondNarrowing(bodyScope, st.Cond, true)
		err := v.checkStmts(st.Body, inFunction)
		v.popScope()
		if err != nil {
			return err
		}
		if len(st.ElseBody) > 0 {
			elseScope := v.pushScope()
			v.applyCondNarrowing(elseScope, st.Cond, false)
			err := v.checkStmts(st.ElseBody, inFunction)
			v.popScope()
			if err != nil {
				return err
			}
		}
		return nil
	}

	inCheckScope := v.pushScope()
	for _, r := range st.Rules {
		if err := v.checkExpr(r.Cond); err != nil {
			v.popScope()
			return err
		}
		if r.Action != nil {
			actionScope := v.pushScope()
			v.applyCondNarrowing(actionScope, r.Cond, true)
			err := v.checkStmt(r.Action, inFunction)
			v.popScope()
			if err != nil {
				v.popScope()
				return err
			}
			v.applyCondNarrowing(inCheckScope, r.Cond, false)
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
			if stmtAlwaysExits(r.Action, inFunction) {
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
	_, ok := s.(*ReturnStmt)
	return ok && inFunction
}

func allRulesReturnExpr(rules []CheckRule) bool {
	if len(rules) == 0 {
		return false
	}
	for _, r := range rules {
		if _, isRet := r.Action.(*ReturnStmt); !isRet {
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
