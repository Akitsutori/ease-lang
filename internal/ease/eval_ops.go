package ease

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

func (ev *Evaluator) evalMatchList(lv *ListValue, arms []MatchArmExpr, hasElse bool, elseBody []Stmt, elseVal Expr, env *Env, line, col int) (*ListValue, error) {
	if lv.ElemType != "" && lv.ElemType != "text" {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("'match' on a list requires text items, got list of %s", lv.ElemType),
			"Convert items to text or pass a list of text values to 'match'.",
		)
	}
	var items []Value
	var firstType string
	for idx, el := range lv.Elements {
		textItem, ok := el.(string)
		if !ok {
			return nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'match' on a list requires text items, got %s at index %d", TypeName(el), idx+1),
				"Ensure every item in the target list is text.",
			)
		}
		itemEnv := NewEnv(env)
		itemEnv.Define("it", textItem, false)
		itemEnv.SetLoopIndex(float64(idx + 1))

		matchedAny := false
		for _, arm := range arms {
			bindings, matched, err := matchPatternRunes(textItem, arm.Pattern, env, ev.source)
			if err != nil {
				return nil, err
			}
			if !matched {
				continue
			}
			matchedAny = true
			armEnv := NewEnv(itemEnv)
			for k, v := range bindings {
				armEnv.Define(k, v, false)
			}
			val, err := ev.EvalExpr(arm.Value, armEnv)
			if err != nil {
				return nil, err
			}
			if err := ev.checkNotConditionValue(val, arm.Line, arm.Col); err != nil {
				return nil, err
			}
			gotType := structuredTypeName(val)
			if len(items) == 0 {
				firstType = gotType
			} else if gotType != firstType {
				return nil, newError(
					"TypeError",
					arm.Line,
					arm.Col,
					ev.source,
					fmt.Sprintf("Mixed result types in list 'match': expected %s, got %s", firstType, gotType),
					"All branches in a list 'match' must return the same type (use 'def' records or convert values with 'as text').",
				)
			}
			items = append(items, val)
			break
		}

		if !matchedAny && hasElse {
			elseEnv := NewEnv(itemEnv)
			val, sig, err := ev.execBlock(elseBody, elseEnv)
			if err != nil {
				return nil, err
			}
			if sig != nil {
				if sig.loopEnd {
					return nil, newError("RuntimeError", line, col, ev.source, "Unexpected '-> end' inside 'match' expression", "")
				}
				val = sig.val
			} else if elseVal != nil {
				val, err = ev.EvalExpr(elseVal, elseEnv)
				if err != nil {
					return nil, err
				}
			}
			if err := ev.checkNotConditionValue(val, line, col); err != nil {
				return nil, err
			}
			gotType := structuredTypeName(val)
			if len(items) == 0 {
				firstType = gotType
			} else if gotType != firstType {
				elLine, elCol := line, col
				if len(elseBody) > 0 {
					elLine, elCol = elseBody[len(elseBody)-1].Pos()
				}
				return nil, newError(
					"TypeError",
					elLine,
					elCol,
					ev.source,
					fmt.Sprintf("Mixed result types in list 'match': expected %s, got %s", firstType, gotType),
					"All branches in a list 'match' (including 'else') must return the same type.",
				)
			}
			items = append(items, val)
		}
	}
	return &ListValue{ElemType: firstType, Elements: items}, nil
}

func (ev *Evaluator) cloneRecordListWithUpdates(lv *ListValue, updates []WithFieldUpdate, env *Env, line, col int) (*ListValue, error) {
	if lv.ElemType == "number" || lv.ElemType == "text" {
		return nil, newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Cannot use 'with' on list of %s", lv.ElemType),
			"'with' on a list only supports '.<index> = <val>'; use 'update <list> with ...' for record lists.",
		)
	}
	return nil, newError(
		"TypeError",
		line,
		col,
		ev.source,
		"Cannot use bare 'with' on a list of records",
		"Write 'update <list> with <field> = <val>' to update all items, or 'update <list> where <cond> with <field> = <val>' to update matching items.",
	)
}

func (ev *Evaluator) evalUpdateRecordList(lv *ListValue, whereExpr Expr, updates []WithFieldUpdate, env *Env, line, col int) (*ListValue, error) {
	if len(lv.Elements) == 0 {
		if lv.ElemType == "" {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				"Cannot use 'update' on empty untyped list",
				"'update' requires a list of 'def' records (e.g. 'list as <def>').",
			)
		}
		if lv.ElemType == "number" || lv.ElemType == "text" {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'update' requires a list of 'def' records, got list of %s", lv.ElemType),
				"Construct a record list with 'list as <def> with ... end' or 'list rec1, rec2'.",
			)
		}
		protoVal, ok := lookupTypeInEnv(env, lv.ElemType)
		if !ok {
			protoVal, ok = lookupTypeInEnv(ev.global, lv.ElemType)
		}
		recProto, isRec := protoVal.(*RecordValue)
		if !ok || !isRec {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'update' requires a list of 'def' records, got list of %s", lv.ElemType),
				"Construct a record list with 'list as <def> with ... end' or 'list rec1, rec2'.",
			)
		}
		for _, upd := range updates {
			if _, exists := recProto.Fields[upd.Name]; !exists {
				return nil, newError(
					"RuntimeError",
					upd.Line,
					upd.Col,
					ev.source,
					fmt.Sprintf("%s has no field %q", recProto.TypeName, upd.Name),
					missingFieldHint(upd.Name, recProto.Order),
				)
			}
		}
		return &ListValue{ElemType: lv.ElemType}, nil
	}

	out := make([]Value, 0, len(lv.Elements))
	elemType := lv.ElemType
	for idx, item := range lv.Elements {
		rec, ok := item.(*RecordValue)
		if !ok {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'update' requires a list of 'def' records, got list of %s", structuredTypeName(item)),
				"Construct a record list with 'list as <def> with ... end' or 'list rec1, rec2'.",
			)
		}
		if elemType == "" {
			elemType = rec.TypeName
		}
		itemEnv := NewEnv(env)
		itemEnv.Define("it", rec, false)
		itemEnv.SetLoopIndex(float64(idx + 1))

		shouldUpdate := true
		if whereExpr != nil {
			condVal, err := ev.EvalExpr(whereExpr, itemEnv)
			if err != nil {
				return nil, err
			}
			b, isBool := condVal.(bool)
			if !isBool {
				cLine, cCol := whereExpr.Pos()
				return nil, newError(
					"TypeError",
					cLine,
					cCol,
					ev.source,
					fmt.Sprintf("'update ... where' requires a condition (e.g. 'x > 0' or 'x is ...'), got %s", TypeName(condVal)),
					"Write a comparison or membership test after 'where'.",
				)
			}
			shouldUpdate = b
		}
		if !shouldUpdate {
			out = append(out, rec)
			continue
		}
		inst, err := ev.cloneRecordWithUpdates(rec, updates, itemEnv)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return &ListValue{ElemType: elemType, Elements: out}, nil
}

func indexOutOfRangeError(kind, emptyLabel string, idx, length, line, col int, source string) error {
	var msg string
	if length == 0 {
		msg = fmt.Sprintf("%s index %d out of range (%s)", kind, idx, emptyLabel)
	} else {
		msg = fmt.Sprintf("%s index %d out of range (1..%d)", kind, idx, length)
	}
	hint := fmt.Sprintf("Valid indices are 1 to %d.", length)
	if length == 0 {
		hint = fmt.Sprintf("Cannot index into an empty %s.", strings.ToLower(kind))
		if kind == "Column" {
			hint = "The row has no columns."
		}
	}
	if idx == 0 {
		hint = fmt.Sprintf("%s indices in Ease are 1-based; use 1 for the first element.", kind)
	}
	return newError("RuntimeError", line, col, source, msg, hint)
}

func (ev *Evaluator) cloneListWithIndexUpdates(lv *ListValue, updates []WithIndexUpdate, env *Env) (*ListValue, error) {
	elems := slices.Clone(lv.Elements)
	if elems == nil {
		elems = []Value{}
	}
	for _, upd := range updates {
		idxVal, err := ev.EvalExpr(upd.Index, env)
		if err != nil {
			return nil, err
		}
		idxFloat, ok := idxVal.(float64)
		if !ok || math.IsNaN(idxFloat) || math.IsInf(idxFloat, 0) || math.Trunc(idxFloat) != idxFloat {
			return nil, newError(
				"RuntimeError",
				upd.Line,
				upd.Col,
				ev.source,
				fmt.Sprintf("Index must be an integer, got %s", FormatValue(idxVal)),
				"Use 1-based integer indices, e.g. xs with .1 = val.",
			)
		}
		if idxFloat < 1 || idxFloat > float64(len(elems)) {
			idxInt := int(idxFloat)
			if idxFloat < float64(math.MinInt) || idxFloat > float64(math.MaxInt) {
				idxInt = math.MaxInt
			}
			return nil, indexOutOfRangeError("List", "list is empty", idxInt, len(elems), upd.Line, upd.Col, ev.source)
		}
		idx := int(idxFloat)
		newVal, err := ev.EvalExpr(upd.Value, env)
		if err != nil {
			return nil, err
		}
		if err := ev.checkNotConditionValue(newVal, upd.Line, upd.Col); err != nil {
			return nil, err
		}
		if lv.ElemType != "" && !matchesListElemType(newVal, lv.ElemType, ev.global) {
			return nil, newError(
				"TypeError",
				upd.Line,
				upd.Col,
				ev.source,
				fmt.Sprintf("Cannot assign %s to element %d of list of %s", structuredTypeName(newVal), idx, lv.ElemType),
				fmt.Sprintf("This list requires elements of type %q.", lv.ElemType),
			)
		}
		elems[idx-1] = newVal
	}
	return &ListValue{ElemType: lv.ElemType, Elements: elems}, nil
}

func (ev *Evaluator) cloneRecordWithUpdates(rec *RecordValue, updates []WithFieldUpdate, env *Env) (*RecordValue, error) {
	cloned := make(map[string]Value, len(rec.Fields))
	for k, v := range rec.Fields {
		cloned[k] = instantiateRecordValue(v)
	}
	for _, upd := range updates {
		if _, exists := cloned[upd.Name]; !exists {
			return nil, newError(
				"RuntimeError",
				upd.Line,
				upd.Col,
				ev.source,
				fmt.Sprintf("%s has no field %q", rec.TypeName, upd.Name),
				missingFieldHint(upd.Name, rec.Order),
			)
		}
		newVal, err := ev.EvalExpr(upd.Value, env)
		if err != nil {
			return nil, err
		}
		if opts, constrained := rec.Allowed[upd.Name]; constrained {
			matched := false
			for _, opt := range opts {
				if valueMatchesUnionOption(newVal, opt) {
					matched = true
					break
				}
			}
			if !matched {
				return nil, newError(
					"RuntimeError",
					upd.Line,
					upd.Col,
					ev.source,
					fmt.Sprintf("Invalid value %s for %s.%s", FormatValue(newVal), rec.TypeName, upd.Name),
					fmt.Sprintf("Allowed values: %s", strings.Join(formatUnionOptions(opts), " | ")),
				)
			}
		} else if origVal := cloned[upd.Name]; origVal != nil && TypeName(origVal) != TypeName(newVal) {
			return nil, newError(
				"TypeError",
				upd.Line,
				upd.Col,
				ev.source,
				fmt.Sprintf("Field '%s.%s' expected %s, got %s (%s)", rec.TypeName, upd.Name, TypeName(origVal), TypeName(newVal), FormatValue(newVal)),
				fmt.Sprintf("Keep the field type of '%s.%s' (%s).", rec.TypeName, upd.Name, TypeName(origVal)),
			)
		}
		cloned[upd.Name] = instantiateRecordValue(newVal)
	}
	orderCopy := make([]string, len(rec.Order))
	copy(orderCopy, rec.Order)
	return &RecordValue{
		TypeName:    rec.TypeName,
		Fields:      cloned,
		Allowed:     rec.Allowed,
		Order:       orderCopy,
		IsPrototype: false,
	}, nil
}

func (ev *Evaluator) checkLeftOfIsNotType(left Value, line, col int) error {
	if rec, ok := left.(*RecordValue); ok && rec.IsPrototype {
		return newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("cannot use record type '%s' as a value on the left side of 'is'", rec.TypeName),
			fmt.Sprintf("Put the value on the left and the type on the right (e.g. 'item is %s'), or create an instance with '%s with ...'.", rec.TypeName, rec.TypeName),
		)
	}
	if u, ok := left.(*UnionValue); ok {
		return newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("cannot use union type '%s' as a value on the left side of 'is'", u.TypeName),
			fmt.Sprintf("Put a concrete value on the left side of 'is' (e.g. 'val is %s').", u.TypeName),
		)
	}
	return nil
}

func (ev *Evaluator) evalIs(left, right Value, line, col int) (bool, error) {
	if err := ev.checkLeftOfIsNotType(left, line, col); err != nil {
		return false, err
	}
	if recRight, ok := right.(*RecordValue); ok && recRight.IsPrototype {
		recLeft, isRec := left.(*RecordValue)
		return isRec && !recLeft.IsPrototype && recLeft.TypeName == recRight.TypeName, nil
	}
	if uRight, ok := right.(*UnionValue); ok {
		return valueInUnion(left, uRight), nil
	}
	if btRight, ok := right.(BaseTypeValue); ok {
		return matchesEaseType(left, string(btRight)), nil
	}
	return ValuesEqual(left, right), nil
}

func (ev *Evaluator) evalBinaryOp(op string, left, right Value, line, col int) (Value, error) {
	switch op {
	case "is":
		return ev.evalIs(left, right, line, col)
	case "is-not":
		eq, err := ev.evalIs(left, right, line, col)
		if err != nil {
			return nil, err
		}
		return !eq, nil
	case "+":
		if _, isLeftList := left.(*ListValue); isLeftList {
			return ev.evalVectorArithmetic("+", left, right, line, col)
		}
		if _, isRightList := right.(*ListValue); isRightList {
			return ev.evalVectorArithmetic("+", left, right, line, col)
		}
		if ls, lok := left.(string); lok {
			if rs, rok := right.(string); rok {
				return norm.NFC.String(ls + rs), nil
			}
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot add %s and %s with '+'", TypeName(left), TypeName(right)),
				"Use string interpolation '\"{a}{b}\"' or explicit 'as text' conversion to combine text with other values.",
			)
		}
		if _, rok := right.(string); rok {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot add %s and %s with '+'", TypeName(left), TypeName(right)),
				"Use string interpolation '\"{a}{b}\"' or explicit 'as text' conversion to combine text with other values.",
			)
		}
		ln, lok := left.(float64)
		rn, rok := right.(float64)
		if lok && rok {
			return ln + rn, nil
		}
		return nil, newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Cannot add %s and %s with '+'", TypeName(left), TypeName(right)),
			"Use string interpolation '\"{a}{b}\"' or explicit 'as text' conversion to combine text with other values.",
		)

	case "-", "*", "/", "%":
		if _, isLeftList := left.(*ListValue); isLeftList {
			return ev.evalVectorArithmetic(op, left, right, line, col)
		}
		if _, isRightList := right.(*ListValue); isRightList {
			return ev.evalVectorArithmetic(op, left, right, line, col)
		}
		ln, lok := left.(float64)
		rn, rok := right.(float64)
		if !lok || !rok {
			return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Operator %q requires numbers or numeric lists, got %s and %s", op, TypeName(left), TypeName(right)), "Convert strings to numbers with num if needed.")
		}
		return ev.evalNumericScalarOp(op, ln, rn, line, col)

	case "<", "<=", ">", ">=":
		ln, lok := left.(float64)
		rn, rok := right.(float64)
		if !lok || !rok {
			return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Operator %q requires numbers, got %s and %s", op, TypeName(left), TypeName(right)), "Convert strings to numbers with num if needed.")
		}
		switch op {
		case "<":
			return ln < rn, nil
		case "<=":
			return ln <= rn, nil
		case ">":
			return ln > rn, nil
		case ">=":
			return ln >= rn, nil
		}
	}
	return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Unknown operator %q", op), "")
}

func (ev *Evaluator) evalNumericScalarOp(op string, ln, rn float64, line, col int) (float64, error) {
	switch op {
	case "-":
		return ln - rn, nil
	case "+":
		return ln + rn, nil
	case "*":
		return ln * rn, nil
	case "/":
		if rn == 0 {
			return 0, newError("RuntimeError", line, col, ev.source, "Division by zero", "Check that the divisor is not 0 before dividing.")
		}
		return ln / rn, nil
	case "%":
		if rn == 0 {
			return 0, newError("RuntimeError", line, col, ev.source, "Modulo by zero", "The right-hand divisor of '%' cannot be 0.")
		}
		return math.Mod(ln, rn), nil
	default:
		return 0, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Unknown operator %q", op), "")
	}
}

func (ev *Evaluator) evalVectorArithmetic(op string, left, right Value, line, col int) (Value, error) {
	la, leftIsList := left.(*ListValue)
	rb, rightIsList := right.(*ListValue)

	if leftIsList && !isNumericList(la) {
		if op == "+" {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot use '+' on %s", describeListOrType(la)),
				"Use 'append xs ys' to combine lists; '+' on lists performs element-wise numeric addition.",
			)
		}
		return nil, newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Operator %q requires numbers or numeric lists, got %s and %s", op, describeListOrType(left), describeListOrType(right)),
			"Vector arithmetic only supports numeric lists.",
		)
	}
	if rightIsList && !isNumericList(rb) {
		if op == "+" {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot use '+' on %s", describeListOrType(rb)),
				"Use 'append xs ys' to combine lists; '+' on lists performs element-wise numeric addition.",
			)
		}
		return nil, newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Operator %q requires numbers or numeric lists, got %s and %s", op, describeListOrType(left), describeListOrType(right)),
			"Vector arithmetic only supports numeric lists.",
		)
	}

	if leftIsList && rightIsList {
		if len(la.Elements) != len(rb.Elements) {
			return nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("List length mismatch for operator %q: left has %d element(s), right has %d element(s)", op, len(la.Elements), len(rb.Elements)),
				"Both lists in element-wise arithmetic must have the same length.",
			)
		}
		out := make([]Value, len(la.Elements))
		for i := range la.Elements {
			res, err := ev.evalNumericScalarOp(op, la.Elements[i].(float64), rb.Elements[i].(float64), line, col)
			if err != nil {
				return nil, err
			}
			out[i] = res
		}
		return &ListValue{ElemType: "number", Elements: out}, nil
	}

	if leftIsList {
		scalar, ok := right.(float64)
		if !ok {
			if op == "+" {
				return nil, newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Cannot use '+' on %s and %s", describeListOrType(left), TypeName(right)),
					"Use 'append xs item' to add elements or pass a number for broadcasting.",
				)
			}
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Operator %q requires numbers or numeric lists, got %s and %s", op, describeListOrType(left), TypeName(right)),
				"Broadcast arithmetic requires a numeric scalar.",
			)
		}
		out := make([]Value, len(la.Elements))
		for i, el := range la.Elements {
			res, err := ev.evalNumericScalarOp(op, el.(float64), scalar, line, col)
			if err != nil {
				return nil, err
			}
			out[i] = res
		}
		return &ListValue{ElemType: "number", Elements: out}, nil
	}

	scalar, ok := left.(float64)
	if !ok {
		if op == "+" {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot use '+' on %s and %s", TypeName(left), describeListOrType(right)),
				"Use 'append xs item' to add elements or pass a number for broadcasting.",
			)
		}
		return nil, newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Operator %q requires numbers or numeric lists, got %s and %s", op, TypeName(left), describeListOrType(right)),
			"Broadcast arithmetic requires a numeric scalar.",
		)
	}
	out := make([]Value, len(rb.Elements))
	for i, el := range rb.Elements {
		res, err := ev.evalNumericScalarOp(op, scalar, el.(float64), line, col)
		if err != nil {
			return nil, err
		}
		out[i] = res
	}
	return &ListValue{ElemType: "number", Elements: out}, nil
}
