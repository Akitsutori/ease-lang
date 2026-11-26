package ease

import (
	"fmt"
)

func (v *typeValidator) funcUsesSpawn(fnName string) bool {
	if cached, ok := v.spawnFuncCache[fnName]; ok {
		return cached
	}
	visited := make(map[string]bool)
	res := v.funcUsesSpawnVisited(fnName, visited)
	v.spawnFuncCache[fnName] = res
	return res
}

func (v *typeValidator) funcUsesSpawnVisited(fnName string, visited map[string]bool) bool {
	if visited[fnName] {
		return false
	}
	visited[fnName] = true
	fn := v.funcDecls[fnName]
	if fn == nil {
		return false
	}
	return v.stmtsContainSpawn(fn.Body, visited)
}

func (v *typeValidator) stmtsContainSpawn(stmts []Stmt, visited map[string]bool) bool {
	for _, s := range stmts {
		switch st := s.(type) {
		case *SpawnStmt:
			return true
		case *ConstDeclStmt:
			if v.exprContainsSpawn(st.Value, visited) {
				return true
			}
		case *AssignStmt:
			if v.exprContainsSpawn(st.Value, visited) {
				return true
			}
		case *SayStmt:
			if v.exprContainsSpawn(st.Value, visited) {
				return true
			}
		case *ExprStmt:
			if v.exprContainsSpawn(st.Expression, visited) {
				return true
			}
		case *ReturnStmt:
			if v.exprContainsSpawn(st.Value, visited) {
				return true
			}
		case *LoopStmt:
			if v.exprContainsSpawn(st.Iterable, visited) || v.stmtsContainSpawn(st.Body, visited) {
				return true
			}
		case *TakeStmt:
			if v.exprContainsSpawn(st.Inbox, visited) || v.stmtsContainSpawn(st.Body, visited) {
				return true
			}
		case *CheckGuardStmt:
			if v.exprContainsSpawn(st.Cond, visited) || v.stmtsContainSpawn(st.Body, visited) {
				return true
			}
			for _, r := range st.Rules {
				if v.exprContainsSpawn(r.Cond, visited) || (r.Action != nil && v.stmtsContainSpawn([]Stmt{r.Action}, visited)) {
					return true
				}
			}
			if v.stmtsContainSpawn(st.ElseBody, visited) {
				return true
			}
		case *MatchStmt:
			if v.exprContainsSpawn(st.Target, visited) {
				return true
			}
			for _, arm := range st.Arms {
				if (arm.Action != nil && v.stmtsContainSpawn([]Stmt{arm.Action}, visited)) || v.stmtsContainSpawn(arm.Body, visited) {
					return true
				}
			}
			if v.stmtsContainSpawn(st.ElseBody, visited) {
				return true
			}
		}
	}
	return false
}

func (v *typeValidator) exprContainsSpawn(e Expr, visited map[string]bool) bool {
	if e == nil {
		return false
	}
	switch ex := e.(type) {
	case *CallExpr:
		if ex.Module == "" && v.funcUsesSpawnVisited(ex.Callee, visited) {
			return true
		}
		for _, arg := range ex.Args {
			if v.exprContainsSpawn(arg, visited) {
				return true
			}
		}
	case *BinaryExpr:
		return v.exprContainsSpawn(ex.Left, visited) || v.exprContainsSpawn(ex.Right, visited)
	case *UnaryExpr:
		return v.exprContainsSpawn(ex.Right, visited)
	case *CastExpr:
		return v.exprContainsSpawn(ex.Target, visited) || v.exprContainsSpawn(ex.Fallback, visited)
	case *FieldExpr:
		return v.exprContainsSpawn(ex.Target, visited)
	case *IndexExpr:
		return v.exprContainsSpawn(ex.Target, visited) || v.exprContainsSpawn(ex.Index, visited)
	case *ListExpr:
		for _, el := range ex.Elements {
			if v.exprContainsSpawn(el, visited) {
				return true
			}
		}
	case *WithExpr:
		if v.exprContainsSpawn(ex.Base, visited) {
			return true
		}
		for _, u := range ex.Updates {
			if v.exprContainsSpawn(u.Value, visited) {
				return true
			}
		}
		for _, u := range ex.IndexUpdates {
			if v.exprContainsSpawn(u.Index, visited) || v.exprContainsSpawn(u.Value, visited) {
				return true
			}
		}
	case *WhereExpr:
		if v.exprContainsSpawn(ex.Target, visited) {
			return true
		}
		for _, c := range ex.Conditions {
			if v.exprContainsSpawn(c, visited) {
				return true
			}
		}
	case *UpdateExpr:
		if v.exprContainsSpawn(ex.Target, visited) || v.exprContainsSpawn(ex.Where, visited) {
			return true
		}
		for _, u := range ex.Updates {
			if v.exprContainsSpawn(u.Value, visited) {
				return true
			}
		}
	case *SortByExpr:
		if v.exprContainsSpawn(ex.Target, visited) {
			return true
		}
		for _, k := range ex.Keys {
			if v.exprContainsSpawn(k, visited) {
				return true
			}
		}
	case *MatchExpr:
		if v.exprContainsSpawn(ex.Target, visited) || v.stmtsContainSpawn(ex.ElseBody, visited) {
			return true
		}
		for _, arm := range ex.Arms {
			if v.exprContainsSpawn(arm.Value, visited) {
				return true
			}
		}
	}
	return false
}

func (v *typeValidator) collectSwarmReturnExprs(stmts []Stmt) []Expr {
	if len(stmts) == 0 {
		return nil
	}
	var out []Expr
	for _, s := range stmts {
		switch st := s.(type) {
		case *ReturnStmt:
			out = append(out, st.Value)
		case *CheckGuardStmt:
			out = append(out, v.collectSwarmReturnExprs(st.Body)...)
			for _, r := range st.Rules {
				if r.Action != nil {
					if rs, ok := r.Action.(*ReturnStmt); ok {
						out = append(out, rs.Value)
					}
				}
			}
			out = append(out, v.collectSwarmReturnExprs(st.ElseBody)...)
		case *MatchStmt:
			for _, arm := range st.Arms {
				if arm.Action != nil {
					if rs, ok := arm.Action.(*ReturnStmt); ok {
						out = append(out, rs.Value)
					}
				}
				out = append(out, v.collectSwarmReturnExprs(arm.Body)...)
			}
			out = append(out, v.collectSwarmReturnExprs(st.ElseBody)...)
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
		if err := v.checkUnusedLocals(ex.Body, nil); err != nil {
			return err
		}
		if stmtsHaveReturn(ex.Body) && !v.stmtAlwaysReturnsExpr(ex.Body) {
			return newError(
				"TypeError",
				ex.Line,
				ex.Col,
				v.source,
				"'swarm loop' returns a value on some paths via '->' but can finish without returning a value",
				"Ensure every execution path in 'swarm loop' ends with '-> <value>'.",
			)
		}
		retExprs := v.collectSwarmReturnExprs(ex.Body)
		expectedType := ""
		for _, re := range retExprs {
			if re == nil {
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
		for _, s := range stmts {
			switch st := s.(type) {
			case *AssignStmt:
				inScope.vars[st.Name] = v.inferExprTypeState(st.Value)
			case *ReturnStmt:
				states = append(states, v.inferExprTypeState(st.Value))
			case *CheckGuardStmt:
				if len(st.Body) > 0 {
					walkBody(st.Body, inScope)
				}
				for _, r := range st.Rules {
					if r.Action != nil {
						if rs, ok := r.Action.(*ReturnStmt); ok {
							states = append(states, v.inferExprTypeState(rs.Value))
						}
					}
				}
				if len(st.ElseBody) > 0 {
					walkBody(st.ElseBody, inScope)
				}
			case *MatchStmt:
				for _, arm := range st.Arms {
					if arm.Action != nil {
						if rs, ok := arm.Action.(*ReturnStmt); ok {
							states = append(states, v.inferExprTypeState(rs.Value))
						}
					}
					if len(arm.Body) > 0 {
						walkBody(arm.Body, inScope)
					}
				}
				if len(st.ElseBody) > 0 {
					walkBody(st.ElseBody, inScope)
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

func (v *typeValidator) checkInboxElementType(elemType string, valExpr Expr) error {
	return v.checkTypedElementValue(elemType, valExpr, "'put'", "inbox")
}

func (v *typeValidator) checkTypedElementValue(elemType string, valExpr Expr, actionLabel, containerLabel string) error {
	valSt := v.inferExprTypeState(valExpr)
	vLine, vCol := valExpr.Pos()
	if valSt.inboxElemType != "" {
		return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot %s inbox into %s of %s", actionLabel, containerLabel, elemType), fmt.Sprintf("Provide a value of type %q.", elemType))
	}
	if valSt.listElemType != "" && elemType != "list" {
		return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot %s list of %s into %s of %s", actionLabel, valSt.listElemType, containerLabel, elemType), fmt.Sprintf("Provide a value of type %q.", elemType))
	}
	if elemType == "number" || elemType == "text" {
		if valSt.exactRecord != "" {
			return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot %s %s into %s of %s", actionLabel, valSt.exactRecord, containerLabel, elemType), fmt.Sprintf("Provide a %s value.", elemType))
		}
		if valSt.exactBaseType != "" && valSt.exactBaseType != elemType {
			return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot %s %s into %s of %s", actionLabel, valSt.exactBaseType, containerLabel, elemType), fmt.Sprintf("Provide a %s value.", elemType))
		}
		if uVars := v.unionVariantsForTypeState(valSt); len(uVars) > 0 && !v.unionContainsOnlyBaseType(uVars, elemType) {
			return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot %s union value into %s of %s", actionLabel, containerLabel, elemType), fmt.Sprintf("Provide a %s value.", elemType))
		}
		return nil
	}
	if v.defs[elemType] != nil {
		if valSt.exactBaseType != "" || valSt.exactNonRecord || valSt.unionName != "" || (valSt.exactRecord != "" && valSt.exactRecord != elemType) {
			got := v.staticConcreteType(valSt)
			if got == "" {
				got = "incompatible value"
			}
			return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Cannot %s %s into %s of %s", actionLabel, got, containerLabel, elemType), fmt.Sprintf("Pass a %s record instance created with '%s with ...'.", elemType, elemType))
		}
		return nil
	}
	if uVariants, isUnion := v.resolvedUnions[elemType]; isUnion {
		if litVar, isLit := v.resolveFallbackLiteralVariant(valExpr); isLit {
			if !v.variantCoveredByUnion(litVar, uVariants) {
				return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Value %s is not valid for %s union %q", litVar.display(), containerLabel, elemType), fmt.Sprintf("Provide a valid member of union %s.", elemType))
			}
			return nil
		}
		if valSt.exactRecord != "" {
			recVar := resolvedUnionVariant{kind: variantRecord, recordName: valSt.exactRecord}
			if !v.variantCoveredByUnion(recVar, uVariants) {
				return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Record %q is not a member of %s union %q", valSt.exactRecord, containerLabel, elemType), fmt.Sprintf("Provide a valid member of union %s.", elemType))
			}
			return nil
		}
		if valUnion := v.unionVariantsForTypeState(valSt); len(valUnion) > 0 {
			for _, vr := range valUnion {
				if !v.variantCoveredByUnion(vr, uVariants) {
					return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Variant %s is not covered by %s union %q", vr.display(), containerLabel, elemType), fmt.Sprintf("Provide a valid member of union %s.", elemType))
				}
			}
			return nil
		}
		if valSt.exactBaseType != "" {
			baseVar := resolvedUnionVariant{kind: variantBaseType, baseType: valSt.exactBaseType}
			if !v.variantCoveredByUnion(baseVar, uVariants) {
				return newError("TypeError", vLine, vCol, v.source, fmt.Sprintf("Value of type %s is not guaranteed to belong to %s union %q", valSt.exactBaseType, containerLabel, elemType), fmt.Sprintf("Provide a valid member of union %s.", elemType))
			}
			return nil
		}
	}
	return nil
}
