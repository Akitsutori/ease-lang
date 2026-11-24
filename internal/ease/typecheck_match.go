package ease

import (
	"fmt"
)

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
	if v.inErrorTestExpr || expr == nil {
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
		return newError(
			"TypeError",
			st.Line,
			st.Col,
			v.source,
			"List 'match' is a transformation expression and cannot be used as a standalone statement",
			"Return or assign the result (e.g. '-> match items ... end' or 'result = match items ... end').",
		)
	}

	for _, arm := range st.Arms {
		armScope := v.pushScope()
		err := v.validateAndBindMatchPattern(arm.Pattern, armScope)
		if err == nil {
			if len(arm.Body) > 0 {
				err = v.checkStmts(arm.Body, inFunction)
			} else {
				err = v.checkStmt(arm.Action, inFunction)
			}
		}
		v.popScope()
		if err != nil {
			return err
		}
	}

	if len(st.ElseBody) > 0 {
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

	if ex.HasElse {
		v.pushScope()
		err := v.checkStmts(ex.ElseBody, false)
		if err == nil && ex.ElseValue != nil {
			err = v.checkExpr(ex.ElseValue)
			if err == nil {
				err = v.checkNotCondition(ex.ElseValue)
			}
			if err == nil {
				err = v.checkNotVoidValue(ex.ElseValue)
			}
			if err == nil && treatAsList {
				err = v.checkMatchListResultExpr(&expectedType, ex.ElseValue, true)
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
		armScope := v.pushScope()
		v.bindMatchPatternTypes(arm.Pattern, armScope)
		states = append(states, v.inferExprTypeState(arm.Value))
		v.popScope()
	}

	if ex.HasElse {
		elseScope := v.pushScope()
		for _, s := range ex.ElseBody {
			if as, ok := s.(*AssignStmt); ok {
				elseScope.vars[as.Name] = v.inferExprTypeState(as.Value)
			}
		}
		if ex.ElseValue != nil {
			states = append(states, v.inferExprTypeState(ex.ElseValue))
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
