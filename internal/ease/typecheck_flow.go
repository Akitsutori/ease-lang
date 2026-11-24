package ease

import (
	"fmt"
	"sort"
	"strings"
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
func (v *typeValidator) checkFuncReturnConsistency(fn *FuncDecl, fnScope *typeScope) error {
	if fn == nil {
		return nil
	}
	if !stmtsHaveReturn(fn.Body) {
		return nil
	}
	if v.stmtAlwaysReturnsExpr(fn.Body) {
		return nil
	}
	if fnScope != nil {
		names := make([]string, 0, len(fnScope.vars))
		for name := range fnScope.vars {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			st := fnScope.vars[name]
			if st.hasRemainingTracking && len(st.remainingVariants) == 0 {
				return nil
			}
		}
		for _, name := range names {
			st := fnScope.vars[name]
			if st.hasRemainingTracking && len(st.remainingVariants) > 0 {
				label := st.unionName
				if label == "" {
					label = st.inlineUnionLabel
				}
				missingLabels := make([]string, len(st.remainingVariants))
				for i, mv := range st.remainingVariants {
					missingLabels[i] = mv.display()
				}
				missingText := strings.Join(missingLabels, ", ")
				caseWord := "Missing case"
				branchWord := "a branch"
				if len(missingLabels) > 1 {
					caseWord = "Missing cases"
					branchWord = "branches"
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
					fmt.Sprintf("Non-exhaustive check on '%s' (%s: %s)", label, caseWord, missingText),
					fmt.Sprintf("Add %s for %s or add a final '-> ...' fallback.", branchWord, missingText),
				)
			}
		}
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
	source                 string
	known                  map[string]typeDefInfo
	defs                   map[string]*DefDecl
	unions                 map[string]*UnionDecl
	consts                 map[string]int
	constExprs             map[string]Expr
	funcNames              map[string]int
	funcDecls              map[string]*FuncDecl
	modules                map[string]modTypeInfo
	resolvedUnions         map[string][]resolvedUnionVariant
	globalScope            *typeScope
	scope                  *typeScope
	allowInternalBuiltins  bool
	allowTrailingValueExpr bool
	isTopLevelBody         bool
	inTrailingReplExpr     bool
	isFuncBodyTop          bool
	inFinalFuncStmt        bool
	spawnFuncCache         map[string]bool
	activeCallSpecs        map[string]bool
	verifiedCallSpecs      map[string]bool
	activeReturnInfer      map[string]bool
	callSpecDepth          int
	inErrorTestExpr        bool
	inErrorBlock           bool
	inTestBlock            bool
	loopDepth              int
	swarmDepth             int
	directInSwarm          bool
	workerAssigned         map[string]bool
	workerOuterScope       *typeScope
}

func (v *typeValidator) checkStmts(stmts []Stmt, inFunction bool) error {
	wasTop := v.isTopLevelBody
	wasFuncTop := v.isFuncBodyTop
	v.isTopLevelBody = false
	v.isFuncBodyTop = false
	for i, s := range stmts {
		prevTrailing := v.inTrailingReplExpr
		prevFinalFunc := v.inFinalFuncStmt
		v.inTrailingReplExpr = wasTop && v.allowTrailingValueExpr && i == len(stmts)-1
		v.inFinalFuncStmt = wasFuncTop && i == len(stmts)-1
		var err error
		switch st := s.(type) {
		case *CheckGuardStmt:
			err = v.checkCheckGuardStmt(stmts, i, st, inFunction)
		default:
			err = v.checkStmt(s, inFunction)
		}
		v.inTrailingReplExpr = prevTrailing
		v.inFinalFuncStmt = prevFinalFunc
		if err != nil {
			return err
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
		valSt := v.inferExprTypeState(st.Value)
		valSt.isParam = false
		v.setVarInCurrentOrExisting(st.Name, valSt)
		return nil

	case *AssignStmt:
		if err := v.checkNameNotCallable(st.Name, st.Line, st.Col, "variable"); err != nil {
			return err
		}
		if !v.inErrorTestExpr {
			if cur, ok := v.lookupVar(st.Name); ok && cur.isParam {
				return newError(
					"TypeError",
					st.Line,
					st.Col,
					v.source,
					fmt.Sprintf("Cannot reassign function parameter %q", st.Name),
					fmt.Sprintf("Parameters are read-only inputs; bind a new local variable name (e.g. 'next_%s = ...') instead.", st.Name),
				)
			}
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
		valSt := v.inferExprTypeState(st.Value)
		valSt.isParam = false
		v.setVarInCurrentOrExisting(st.Name, valSt)
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
		if v.inTestBlock || v.inErrorTestExpr {
			return nil
		}
		if err := v.checkNotStandaloneCondition(st.Expression); err != nil {
			return err
		}
		if v.isEffectfulExprStmt(st.Expression) {
			return nil
		}
		if v.inTrailingReplExpr {
			return nil
		}
		return v.unusedExprStmtError(st.Expression)

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
		if err := v.checkExpr(st.Expected); err != nil {
			return err
		}
		v.applyCondNarrowing(v.scope, &BinaryExpr{
			posInfo: posInfo{Line: st.Line, Col: st.Col},
			Left:    st.Actual,
			Op:      "is",
			Right:   st.Expected,
		}, true)
		return nil

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

func (v *typeValidator) checkMustBeCondition(cond Expr, ctx string) error {
	if v.inErrorTestExpr || cond == nil || isConditionExpr(cond) {
		return nil
	}
	line, col := cond.Pos()
	gotLabel := v.staticConcreteType(v.inferExprTypeState(cond))
	if gotLabel == "" {
		gotLabel = "value"
	}
	hint := "Ease has no truthiness; write an explicit comparison using 'is', 'is not', '<', '>', or 'contains'."
	if _, isCast := cond.(*CastExpr); isCast {
		hint = "Ease has no truthiness and 'as' returns a converted value, not a condition. Write e.g. 'n = raw as number or 0' and check 'n > 0'."
	}
	return newError(
		"TypeError",
		line,
		col,
		v.source,
		fmt.Sprintf("'%s' requires a condition (e.g. 'x > 0' or 'x is ...'), got %s", ctx, gotLabel),
		hint,
	)
}

func (v *typeValidator) checkCheckGuardStmt(blockStmts []Stmt, idx int, st *CheckGuardStmt, inFunction bool) error {
	if st.IsEffectBlock {
		if err := v.checkExpr(st.Cond); err != nil {
			return err
		}
		if err := v.checkMustBeCondition(st.Cond, "check"); err != nil {
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
		} else if v.stmtAlwaysExitsBlock(st.Body, inFunction) {
			v.applyCondNarrowing(v.scope, st.Cond, false)
		}
		return nil
	}

	inCheckScope := v.pushScope()
	for _, r := range st.Rules {
		if err := v.checkExpr(r.Cond); err != nil {
			v.popScope()
			return err
		}
		if err := v.checkMustBeCondition(r.Cond, "check"); err != nil {
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

	// Narrow post-check outer scope when early-exiting guards rule out cases.
	if len(st.ElseBody) == 0 {
		for _, r := range st.Rules {
			if v.stmtAlwaysExitsStmt(r.Action, inFunction) {
				v.applyCondNarrowing(v.scope, r.Cond, false)
			}
		}
	}

	return nil
}

func (v *typeValidator) stmtAlwaysExitsStmt(s Stmt, inFunction bool) bool {
	if s == nil {
		return false
	}
	switch s.(type) {
	case *ReturnStmt:
		return inFunction
	case *LoopEndStmt:
		return v.loopDepth > 0
	default:
		return false
	}
}

func (v *typeValidator) stmtAlwaysExitsBlock(stmts []Stmt, inFunction bool) bool {
	if inFunction && v.stmtAlwaysReturnsExpr(stmts) {
		return true
	}
	if v.loopDepth > 0 {
		for _, s := range stmts {
			if _, isEnd := s.(*LoopEndStmt); isEnd {
				return true
			}
		}
	}
	return false
}

func (v *typeValidator) subtractRemainingVariants(next *varTypeState, removed []resolvedUnionVariant) bool {
	if len(removed) == 0 {
		return false
	}
	if !next.hasRemainingTracking {
		var base []resolvedUnionVariant
		if next.unionName != "" {
			base = v.resolvedUnions[next.unionName]
		} else if len(next.inlineUnionVariants) > 0 {
			base = next.inlineUnionVariants
		}
		if len(base) == 0 {
			return false
		}
		next.remainingVariants = append([]resolvedUnionVariant(nil), base...)
		next.hasRemainingTracking = true
	}

	coveredKeys := make(map[string]bool, len(removed))
	coveredText := false
	coveredNumber := false
	for _, rv := range removed {
		coveredKeys[rv.key()] = true
		if rv.kind == variantBaseType {
			if rv.baseType == "text" {
				coveredText = true
			} else if rv.baseType == "number" {
				coveredNumber = true
			}
		}
	}

	var kept []resolvedUnionVariant
	for _, vr := range next.remainingVariants {
		if coveredKeys[vr.key()] {
			continue
		}
		if vr.kind == variantStringLit && coveredText {
			continue
		}
		if vr.kind == variantNumberLit && coveredNumber {
			continue
		}
		kept = append(kept, vr)
	}
	next.remainingVariants = kept

	// Rest-narrowing when remaining variants uniquely determine a record or base scalar type.
	if len(kept) == 1 && kept[0].kind == variantRecord {
		next.exactRecord = kept[0].recordName
		next.exactNonRecord = false
		next.exactBaseType = ""
	} else if len(kept) > 0 {
		allText := true
		allNumber := true
		for _, vr := range kept {
			if !((vr.kind == variantBaseType && vr.baseType == "text") || vr.kind == variantStringLit) {
				allText = false
			}
			if !((vr.kind == variantBaseType && vr.baseType == "number") || vr.kind == variantNumberLit) {
				allNumber = false
			}
		}
		if allText {
			next.exactRecord = ""
			next.exactNonRecord = true
			next.exactBaseType = "text"
		} else if allNumber {
			next.exactRecord = ""
			next.exactNonRecord = true
			next.exactBaseType = "number"
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
			updated := false

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
					updated = true
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
						updated = true
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
							updated = true
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
								updated = true
							}
						}
					}
				}
			case *StringExpr, *NumberExpr, *UnaryExpr:
				if effective {
					next.exactRecord = ""
					next.exactNonRecord = true
					updated = true
				}
			}

			if !effective {
				if removed, ok := v.exprToPatternVariants(c.Right); ok {
					if v.subtractRemainingVariants(&next, removed) {
						updated = true
					}
				}
			}
			if updated {
				scope.vars[id.Name] = next
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
				next.unionName = ""
				if c.TypeName == "text" || c.TypeName == "number" {
					next.exactBaseType = c.TypeName
					next.listElemType = ""
				} else {
					next.exactBaseType = ""
				}
				scope.vars[id.Name] = next
			} else if !effective && (c.TypeName == "text" || c.TypeName == "number") {
				if v.subtractRemainingVariants(&next, []resolvedUnionVariant{{kind: variantBaseType, baseType: c.TypeName}}) {
					scope.vars[id.Name] = next
				}
			}
		}
	}
}

func (v *typeValidator) isEffectfulExprStmt(e Expr) bool {
	if sl, ok := e.(*SwarmLoopExpr); ok {
		return !stmtsHaveReturn(sl.Body)
	}
	_, isVoid := v.voidBuiltinCallName(e)
	return isVoid
}

func (v *typeValidator) unusedExprStmtError(e Expr) error {
	line, col := e.Pos()
	if ce, ok := e.(*CallExpr); ok {
		callee := ce.Callee
		if ce.Module != "" {
			callee = ce.Module + ":" + ce.Callee
		}
		hint := fmt.Sprintf("Assign the result to a variable (e.g. 'res = %s ...') or return it with '->'.", callee)
		if v.inFinalFuncStmt {
			hint = fmt.Sprintf("To return this value from the function, prefix the final line with '-> %s ...', or assign it to a variable (e.g. 'res = %s ...').", callee, callee)
		}
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Return value of '%s' is unused", callee),
			hint,
		)
	}
	hint := "Use '-> <expr>' to return this value, or assign it to a variable with 'name = <expr>'."
	if v.inFinalFuncStmt {
		hint = "Ease functions have no implicit return; prefix the final expression with '-> <expr>' to return it, or assign it to a variable with 'name = <expr>'."
	}
	return newError(
		"TypeError",
		line,
		col,
		v.source,
		"Unused value expression has no effect",
		hint,
	)
}

func (v *typeValidator) checkUnusedLocals(stmts []Stmt, params []string) error {
	paramSet := make(map[string]bool, len(params))
	for _, p := range params {
		paramSet[p] = true
	}
	reads := make(map[string]bool)
	var collectReadsExpr func(e Expr)
	var collectReadsStmts func(list []Stmt)

	collectReadsExpr = func(e Expr) {
		if e == nil {
			return
		}
		switch ex := e.(type) {
		case *IdentExpr:
			reads[ex.Name] = true
		case *InterpStringExpr:
			for _, part := range ex.Parts {
				if part.IsExpr {
					collectReadsExpr(part.Expr)
				}
			}
		case *FieldExpr:
			collectReadsExpr(ex.Target)
		case *IndexExpr:
			collectReadsExpr(ex.Target)
			collectReadsExpr(ex.Index)
		case *UnaryExpr:
			collectReadsExpr(ex.Right)
		case *BinaryExpr:
			collectReadsExpr(ex.Left)
			collectReadsExpr(ex.Right)
		case *TypeCheckExpr:
			collectReadsExpr(ex.Target)
		case *CastExpr:
			collectReadsExpr(ex.Target)
			collectReadsExpr(ex.Fallback)
		case *CallExpr:
			for _, arg := range ex.Args {
				collectReadsExpr(arg)
			}
		case *ListExpr:
			for _, el := range ex.Elements {
				collectReadsExpr(el)
			}
		case *TypedListExpr:
			for _, row := range ex.Rows {
				for _, u := range row {
					collectReadsExpr(u.Value)
				}
			}
		case *WithExpr:
			collectReadsExpr(ex.Base)
			for _, u := range ex.Updates {
				collectReadsExpr(u.Value)
			}
			for _, iu := range ex.IndexUpdates {
				collectReadsExpr(iu.Index)
				collectReadsExpr(iu.Value)
			}
		case *WhereExpr:
			collectReadsExpr(ex.Target)
			for _, c := range ex.Conditions {
				collectReadsExpr(c)
			}
		case *MatchExpr:
			collectReadsExpr(ex.Target)
			for _, arm := range ex.Arms {
				collectReadsExpr(arm.Value)
			}
			collectReadsStmts(ex.ElseBody)
			collectReadsExpr(ex.ElseValue)
		case *SwarmLoopExpr:
			collectReadsExpr(ex.Iterable)
			collectReadsStmts(ex.Body)
		}
	}

	collectReadsStmts = func(list []Stmt) {
		for _, s := range list {
			switch st := s.(type) {
			case *AssignStmt:
				collectReadsExpr(st.Value)
			case *ConstDeclStmt:
				collectReadsExpr(st.Value)
			case *SayStmt:
				collectReadsExpr(st.Value)
			case *ExprStmt:
				collectReadsExpr(st.Expression)
			case *ReturnStmt:
				collectReadsExpr(st.Value)
			case *SpawnStmt:
				if st.Call != nil {
					collectReadsExpr(st.Call)
				}
			case *LoopStmt:
				collectReadsExpr(st.Iterable)
				collectReadsStmts(st.Body)
			case *TakeStmt:
				collectReadsExpr(st.Inbox)
				collectReadsStmts(st.Body)
			case *CheckGuardStmt:
				collectReadsExpr(st.Cond)
				collectReadsStmts(st.Body)
				for _, r := range st.Rules {
					collectReadsExpr(r.Cond)
					if r.Action != nil {
						collectReadsStmts([]Stmt{r.Action})
					}
				}
				collectReadsStmts(st.ElseBody)
			case *MatchStmt:
				collectReadsExpr(st.Target)
				for _, arm := range st.Arms {
					if arm.Action != nil {
						collectReadsStmts([]Stmt{arm.Action})
					}
					collectReadsStmts(arm.Body)
				}
				collectReadsStmts(st.ElseBody)
			}
		}
	}

	collectReadsStmts(stmts)

	var checkDecls func(list []Stmt) error
	checkDecls = func(list []Stmt) error {
		for _, s := range list {
			switch st := s.(type) {
			case *AssignStmt:
				if !paramSet[st.Name] && !reads[st.Name] {
					return newError(
						"TypeError",
						st.Line,
						st.Col,
						v.source,
						fmt.Sprintf("Local variable %q is assigned but never used", st.Name),
						fmt.Sprintf("Use '%s' in a subsequent expression, return it with '-> %s', or remove the unused assignment.", st.Name, st.Name),
					)
				}
			case *ConstDeclStmt:
				if !paramSet[st.Name] && !reads[st.Name] {
					return newError(
						"TypeError",
						st.Line,
						st.Col,
						v.source,
						fmt.Sprintf("Local variable %q is assigned but never used", st.Name),
						fmt.Sprintf("Use '%s' in a subsequent expression, return it with '-> %s', or remove the unused assignment.", st.Name, st.Name),
					)
				}
			case *LoopStmt:
				if err := checkDecls(st.Body); err != nil {
					return err
				}
			case *TakeStmt:
				if err := checkDecls(st.Body); err != nil {
					return err
				}
			case *CheckGuardStmt:
				if err := checkDecls(st.Body); err != nil {
					return err
				}
				for _, r := range st.Rules {
					if r.Action != nil {
						if err := checkDecls([]Stmt{r.Action}); err != nil {
							return err
						}
					}
				}
				if err := checkDecls(st.ElseBody); err != nil {
					return err
				}
			case *MatchStmt:
				for _, arm := range st.Arms {
					if arm.Action != nil {
						if err := checkDecls([]Stmt{arm.Action}); err != nil {
							return err
						}
					}
					if err := checkDecls(arm.Body); err != nil {
						return err
					}
				}
				if err := checkDecls(st.ElseBody); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return checkDecls(stmts)
}
