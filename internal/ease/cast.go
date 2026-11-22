package ease

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func valueMatchesUnionOption(v Value, opt Value) bool {
	if bt, ok := opt.(BaseTypeValue); ok {
		return matchesEaseType(v, string(bt))
	}
	if recOpt, ok := opt.(*RecordValue); ok && recOpt.IsPrototype {
		recVal, isRec := v.(*RecordValue)
		return isRec && !recVal.IsPrototype && recVal.TypeName == recOpt.TypeName
	}
	return ValuesEqual(v, opt)
}

func valueInUnion(v Value, union *UnionValue) bool {
	for _, opt := range union.Values {
		if valueMatchesUnionOption(v, opt) {
			return true
		}
	}
	return false
}

func lookupTypeInEnv(env *Env, name string) (Value, bool) {
	if env == nil || name == "" {
		return nil, false
	}
	if modName, memberName, ok := strings.Cut(name, "."); ok {
		modVal, exists := env.Lookup(modName)
		if !exists {
			return nil, false
		}
		mv, isMod := modVal.(*ModuleValue)
		if !isMod || mv == nil || mv.Env == nil {
			return nil, false
		}
		return mv.Env.Lookup(memberName)
	}
	return env.Lookup(name)
}

func lookupUnion(env *Env, name string) (*UnionValue, bool) {
	v, ok := lookupTypeInEnv(env, name)
	if !ok {
		return nil, false
	}
	u, isUnion := v.(*UnionValue)
	return u, isUnion
}

func matchesListElemType(v Value, elemType string, env *Env) bool {
	switch elemType {
	case "number":
		_, ok := v.(float64)
		return ok
	case "text":
		_, ok := v.(string)
		return ok
	default:
		if union, ok := lookupUnion(env, elemType); ok {
			return valueInUnion(v, union)
		}
		if rec, ok := v.(*RecordValue); ok {
			if rec.TypeName == elemType {
				return true
			}
			if protoVal, found := lookupTypeInEnv(env, elemType); found {
				if proto, isRec := protoVal.(*RecordValue); isRec && proto.IsPrototype {
					return rec.TypeName == proto.TypeName
				}
			}
			return false
		}
		return false
	}
}

func coerceToUnionOptions(val Value, opts []Value, env *Env, candidatePrefix string) (Value, error) {
	if mv, isMap := val.(*MapValue); isMap && mv != nil {
		var matches []*RecordValue
		var recErrs []string
		for _, opt := range opts {
			recOpt, ok := opt.(*RecordValue)
			if !ok || !recOpt.IsPrototype {
				continue
			}
			prefix := recOpt.TypeName
			if candidatePrefix != "" {
				prefix = candidatePrefix + "(" + recOpt.TypeName + ")"
			}
			mapped, err := mapToRecord(mv.Fields, recOpt, env, prefix, mv.FromCSV)
			if err == nil {
				matches = append(matches, mapped)
			} else {
				recErrs = append(recErrs, recOpt.TypeName+": "+err.Error())
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			matchedNames := make([]string, len(matches))
			for i, m := range matches {
				matchedNames[i] = m.TypeName
			}
			return nil, fmt.Errorf("ambiguous map matching multiple records (%s)", strings.Join(matchedNames, ", "))
		}
		if len(recErrs) > 0 {
			return nil, fmt.Errorf("map did not match (%s)", strings.Join(recErrs, "; "))
		}
		return nil, fmt.Errorf("expected allowed value, got map")
	}

	for _, opt := range opts {
		if valueMatchesUnionOption(val, opt) {
			return val, nil
		}
	}
	return nil, fmt.Errorf("no matching union variant")
}

func coerceTypedListElements(elems []Value, elemType string, env *Env, pathPrefix string) ([]Value, error) {
	out := make([]Value, 0, len(elems))
	switch elemType {
	case "number", "text":
		for idx, item := range elems {
			pos := idx + 1
			if !matchesEaseType(item, elemType) {
				if pathPrefix == "" {
					return nil, fmt.Errorf("element %d expected %s, got %s (%s)", pos, elemType, structuredTypeName(item), FormatValue(item))
				}
				return nil, fmt.Errorf("Element '%s.%d' expected %s, got %s", pathPrefix, pos, elemType, structuredTypeName(item))
			}
			out = append(out, item)
		}
		return out, nil
	default:
		protoVal, found := lookupTypeInEnv(env, elemType)
		if !found {
			return nil, fmt.Errorf("Unknown type %q for '%s'", elemType, pathPrefix)
		}
		if union, isUnion := protoVal.(*UnionValue); isUnion {
			for idx, item := range elems {
				pos := idx + 1
				coerced, cErr := coerceToUnionOptions(item, union.Values, env, fmt.Sprintf("%s.%d", pathPrefix, pos))
				if cErr != nil {
					return nil, fmt.Errorf("Invalid value %s at element %d of union %q: %s", FormatValue(item), pos, elemType, cErr.Error())
				}
				out = append(out, coerced)
			}
			return out, nil
		}
		proto, isRec := protoVal.(*RecordValue)
		if !isRec || !proto.IsPrototype {
			return nil, fmt.Errorf("%q is not a 'def' blueprint or built-in type", elemType)
		}
		for idx, item := range elems {
			pos := idx + 1
			elemPath := fmt.Sprintf("%s.%d", pathPrefix, pos)
			if rec, ok := item.(*RecordValue); ok {
				if rec.TypeName != proto.TypeName {
					return nil, fmt.Errorf("expected structured map or %s record (element %d is %s)", elemType, pos, rec.TypeName)
				}
				out = append(out, rec)
				continue
			}
			mv, isMap := item.(*MapValue)
			if !isMap || mv == nil {
				return nil, fmt.Errorf("expected structured map or %s record (element %d is %s)", elemType, pos, structuredTypeName(item))
			}
			mapped, mapErr := mapToRecord(mv.Fields, proto, env, elemPath, mv.FromCSV)
			if mapErr != nil {
				return nil, mapErr
			}
			out = append(out, mapped)
		}
		return out, nil
	}
}

func (ev *Evaluator) castListValue(lv *ListValue, targetType string, line, col int, env *Env) (Value, error) {
	switch targetType {
	case "number", "text":
		if lv.ElemType != "" && lv.ElemType != targetType {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot convert list to %s: expected %s elements, got %s", targetType, targetType, lv.ElemType),
				fmt.Sprintf("Every element of a list cast 'as %s' must be %s.", targetType, targetType),
			)
		}
		elems, err := coerceTypedListElements(lv.Elements, targetType, env, "")
		if err != nil {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot convert list to %s: %s", targetType, err.Error()),
				fmt.Sprintf("Every element of a list cast 'as %s' must be %s.", targetType, targetType),
			)
		}
		return &ListValue{ElemType: targetType, Elements: elems}, nil

	default:
		protoVal, ok := lookupTypeInEnv(env, targetType)
		if !ok {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Unknown type %q after 'as'", targetType),
				"Only built-in types (text, number) or a previously defined/imported 'def' blueprint are allowed after 'as'.",
			)
		}

		if union, isUnion := protoVal.(*UnionValue); isUnion {
			if len(lv.Elements) == 0 && lv.ElemType != "" && lv.ElemType != targetType {
				compatible := false
				if subUnion, isSub := lookupUnion(env, lv.ElemType); isSub {
					compatible = true
					for _, sv := range subUnion.Values {
						if !valueInUnion(sv, union) {
							compatible = false
							break
						}
					}
				} else {
					for _, opt := range union.Values {
						if structuredTypeName(opt) == lv.ElemType {
							compatible = true
							break
						}
					}
				}
				if !compatible {
					return nil, newError(
						"TypeError",
						line,
						col,
						ev.source,
						fmt.Sprintf("Cannot validate list of %s as union %q", lv.ElemType, targetType),
						fmt.Sprintf("List element type %q does not match any variant of %s.", lv.ElemType, targetType),
					)
				}
			}
			elems, err := coerceTypedListElements(lv.Elements, targetType, env, targetType)
			if err != nil {
				allowed := formatUnionOptions(union.Values)
				return nil, newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Value not valid for union %q: %s", targetType, err.Error()),
					fmt.Sprintf("Allowed values for %s: %s.", targetType, strings.Join(allowed, ", ")),
				)
			}
			return &ListValue{ElemType: targetType, Elements: elems}, nil
		}

		proto, isRec := protoVal.(*RecordValue)
		if !isRec || !proto.IsPrototype {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("%q is not a 'def' blueprint or built-in type", targetType),
				"Only built-in types (text, number) or a 'def' blueprint can be used after 'as'.",
			)
		}

		if len(lv.Elements) == 0 && lv.ElemType != "" && lv.ElemType != targetType {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot validate list as %s: expected structured map or %s record, got list of %s", targetType, targetType, lv.ElemType),
				fmt.Sprintf("Expected a list of maps from 'as json'/'as csv' or %s records.", targetType),
			)
		}

		elems, err := coerceTypedListElements(lv.Elements, targetType, env, targetType)
		if err != nil {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot validate list as %s: %s", targetType, err.Error()),
				fmt.Sprintf("Ensure all elements match 'def %s' with matching types and valid union values.", targetType),
			)
		}
		return &ListValue{ElemType: targetType, Elements: elems}, nil
	}
}

func parseCastEaseNumber(s string) (float64, bool) {
	return parseStrictEaseNumber(strings.TrimSpace(s))
}

func castScalarToUnion(targetVal Value, union *UnionValue, env *Env, typeName string) (Value, error) {
	coerced, cErr := coerceToUnionOptions(targetVal, union.Values, env, typeName)
	if cErr == nil {
		return coerced, nil
	}
	if raw, isStr := targetVal.(string); isStr {
		if n, ok := parseCastEaseNumber(raw); ok {
			for _, opt := range union.Values {
				if numOpt, isNum := opt.(float64); isNum && ValuesEqual(n, numOpt) {
					return n, nil
				}
				if bt, isBT := opt.(BaseTypeValue); isBT && string(bt) == "number" {
					return n, nil
				}
			}
		}
	}
	return nil, cErr
}

func unionHasScalarOption(union *UnionValue) bool {
	if union == nil {
		return false
	}
	for _, opt := range union.Values {
		switch o := opt.(type) {
		case string, float64:
			return true
		case BaseTypeValue:
			if string(o) == "text" || string(o) == "number" {
				return true
			}
		}
	}
	return false
}

func (ev *Evaluator) validateCastFallbackValue(fbVal Value, targetType string, env *Env, line, col int) error {
	switch targetType {
	case "number":
		if _, ok := fbVal.(float64); !ok {
			return newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Fallback for 'as number' must be number, got %s", TypeName(fbVal)),
				"Provide a numeric fallback value after 'or', e.g. 'raw as number or 0'.",
			)
		}
		return nil
	case "text":
		if _, ok := fbVal.(string); !ok {
			return newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Fallback for 'as text' must be text, got %s", TypeName(fbVal)),
				"Provide a text fallback value after 'or', e.g. 'val as text or \"\"'.",
			)
		}
		return nil
	default:
		union, ok := lookupUnion(env, targetType)
		if !ok || union == nil {
			return newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'or' fallback is not supported for target type %q", targetType),
				"Use 'or' fallbacks with 'number', 'text', or a scalar union type.",
			)
		}
		if !valueInUnion(fbVal, union) {
			allowed := formatUnionOptions(union.Values)
			return newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Fallback value %s is not valid for union %q", FormatValue(fbVal), targetType),
				fmt.Sprintf("Allowed values for %s: %s.", targetType, strings.Join(allowed, ", ")),
			)
		}
		return nil
	}
}

func (ev *Evaluator) castScalarForFallback(item Value, targetType string, union *UnionValue, env *Env, line, col int) (Value, bool, error) {
	switch targetType {
	case "number":
		switch v := item.(type) {
		case float64:
			return v, true, nil
		case string:
			n, ok := parseCastEaseNumber(v)
			return n, ok, nil
		case *RecordValue:
			return nil, false, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot convert %s to number", TypeName(item)),
				"Only number or numeric text can be converted with 'as number'.",
			)
		default:
			return nil, false, nil
		}
	case "text":
		switch v := item.(type) {
		case string:
			return v, true, nil
		case float64:
			return FormatValue(v), true, nil
		case *RecordValue:
			return nil, false, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot convert %s to text", TypeName(item)),
				"Only atomic values (text, number) can be cast with 'as text'. Use '<value> as json' to serialize structured data to JSON.",
			)
		default:
			return nil, false, nil
		}
	default:
		coerced, cErr := castScalarToUnion(item, union, env, targetType)
		if cErr == nil {
			return coerced, true, nil
		}
		return nil, false, nil
	}
}

func (ev *Evaluator) castWithFallback(e *CastExpr, targetVal Value, env *Env, line, col int) (Value, error) {
	if e.TypeName == "csv" || e.TypeName == "bytes" {
		return nil, newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("'or' fallback cannot be used with 'as %s'", e.TypeName),
			"Target types 'csv' and 'bytes' only work with 'load <path> as ...' without 'or'.",
		)
	}
	var union *UnionValue
	if e.TypeName != "number" && e.TypeName != "text" {
		protoVal, ok := lookupTypeInEnv(env, e.TypeName)
		if !ok {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Unknown type %q after 'as'", e.TypeName),
				"Only built-in types (text, number) or a previously defined/imported 'def' blueprint are allowed after 'as'.",
			)
		}
		if rec, isRec := protoVal.(*RecordValue); isRec && rec.IsPrototype {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'or' fallback cannot be used with record type %q", e.TypeName),
				"Record contracts ('as <def>') must remain strict; use '<def> with ...' for default instances.",
			)
		}
		u, isUnion := protoVal.(*UnionValue)
		if !isUnion {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("%q is not a 'def' blueprint or built-in type", e.TypeName),
				"Use 'number', 'text', or a scalar union type with 'or' fallbacks.",
			)
		}
		if !unionHasScalarOption(u) {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'or' fallback cannot be used with record-only union %q", e.TypeName),
				"Unions containing only 'def' records must remain strict; 'or' fallbacks support number, text, or scalar unions.",
			)
		}
		union = u
	}

	var fbCached Value
	var fbLoaded bool
	getFallback := func() (Value, error) {
		if fbLoaded {
			return fbCached, nil
		}
		fbVal, err := ev.EvalExpr(e.Fallback, env)
		if err != nil {
			return nil, err
		}
		fbLine, fbCol := e.Fallback.Pos()
		if err := ev.checkNotConditionValue(fbVal, fbLine, fbCol); err != nil {
			return nil, err
		}
		if err := ev.validateCastFallbackValue(fbVal, e.TypeName, env, fbLine, fbCol); err != nil {
			return nil, err
		}
		fbCached = fbVal
		fbLoaded = true
		return fbCached, nil
	}

	if lv, isList := targetVal.(*ListValue); isList {
		if lv.ElemType != "" {
			if recVal, ok := lookupTypeInEnv(env, lv.ElemType); ok {
				if rec, isRec := recVal.(*RecordValue); isRec && rec.IsPrototype {
					return nil, newError(
						"TypeError",
						line,
						col,
						ev.source,
						fmt.Sprintf("Cannot convert list of %s with 'as %s or ...' fallback", lv.ElemType, e.TypeName),
						"Lists of records cannot be cast to scalar types.",
					)
				}
			}
		}
		out := make([]Value, 0, len(lv.Elements))
		for _, item := range lv.Elements {
			converted, ok, err := ev.castScalarForFallback(item, e.TypeName, union, env, line, col)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, converted)
				continue
			}
			fb, fbErr := getFallback()
			if fbErr != nil {
				return nil, fbErr
			}
			out = append(out, fb)
		}
		return &ListValue{ElemType: e.TypeName, Elements: out}, nil
	}

	converted, ok, err := ev.castScalarForFallback(targetVal, e.TypeName, union, env, line, col)
	if err != nil {
		return nil, err
	}
	if ok {
		return converted, nil
	}
	return getFallback()
}

func (ev *Evaluator) castValue(e *CastExpr, targetVal Value, env *Env) (Value, error) {
	line, col := e.TypeLine, e.TypeCol
	if line == 0 {
		line, col = e.Line, e.Col
	}

	if e.Fallback != nil {
		return ev.castWithFallback(e, targetVal, env, line, col)
	}

	switch e.TypeName {
	case "json":
		if s, ok := targetVal.(string); ok {
			return ev.parseJSONValue(s, line, col)
		}
		return ev.formatJSONValue(targetVal, line, col)
	case "csv":
		if s, ok := targetVal.(string); ok {
			return ev.parseCSVText(s, "CSV text", line, col)
		}
		return ev.formatCSVValue(targetVal, env, line, col)
	}

	if lv, isList := targetVal.(*ListValue); isList {
		return ev.castListValue(lv, e.TypeName, line, col, env)
	}

	switch e.TypeName {
	case "text":
		switch v := targetVal.(type) {
		case string:
			return v, nil
		case float64:
			return FormatValue(v), nil
		case *RecordValue, *MapValue:
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot convert %s to text", TypeName(targetVal)),
				"Only atomic values (text, number) can be cast with 'as text'. Use '<value> as json' to serialize structured data to JSON.",
			)
		default:
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot convert %s to text", TypeName(targetVal)),
				"Only atomic values (text, number) can be cast with 'as text'.",
			)
		}

	case "number":
		switch v := targetVal.(type) {
		case float64:
			return v, nil
		case string:
			n, ok := parseCastEaseNumber(v)
			if !ok {
				return nil, newError(
					"RuntimeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Cannot convert text %q to number", v),
					"Provide a valid numeric string (for example \"42\" or \"3.14\").",
				)
			}
			return n, nil
		default:
			return nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot convert %s to number", TypeName(targetVal)),
				"Only number or numeric text can be converted with 'as number'.",
			)
		}

	default:
		protoVal, ok := lookupTypeInEnv(env, e.TypeName)
		if !ok {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Unknown type %q after 'as'", e.TypeName),
				"Only built-in types (text, number) or a previously defined/imported 'def' blueprint are allowed after 'as'.",
			)
		}

		if union, isUnion := protoVal.(*UnionValue); isUnion {
			coerced, cErr := castScalarToUnion(targetVal, union, env, e.TypeName)
			if cErr != nil {
				allowed := formatUnionOptions(union.Values)
				msg := fmt.Sprintf("Value %s is not valid for union %q", FormatValue(targetVal), e.TypeName)
				if _, isMap := targetVal.(*MapValue); isMap {
					msg = fmt.Sprintf("Value %s is not valid for union %q (%s)", FormatValue(targetVal), e.TypeName, cErr.Error())
				}
				return nil, newError(
					"TypeError",
					line,
					col,
					ev.source,
					msg,
					fmt.Sprintf("Allowed values for %s: %s.", e.TypeName, strings.Join(allowed, ", ")),
				)
			}
			return coerced, nil
		}

		proto, isRec := protoVal.(*RecordValue)
		if !isRec || !proto.IsPrototype {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("%q is not a 'def' blueprint or built-in type", e.TypeName),
				"Only built-in types (text, number) or a 'def' blueprint can be used after 'as'.",
			)
		}

		if rec, already := targetVal.(*RecordValue); already {
			if rec.TypeName == e.TypeName {
				return rec, nil
			}
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot validate record %q as %q", rec.TypeName, e.TypeName),
				fmt.Sprintf("Expected a map from '<text> as json' or a %s record matching 'def %s'.", e.TypeName, e.TypeName),
			)
		}

		mv, isMap := targetVal.(*MapValue)
		if !isMap {
			if _, isStr := targetVal.(string); isStr {
				return nil, newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Cannot validate text as %s: expected structured map or %s record", e.TypeName, e.TypeName),
					fmt.Sprintf("Parse JSON text first ('data = raw as json'), then validate with 'data as %s'.", e.TypeName),
				)
			}
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot validate %s as %s: expected structured map or %s record", TypeName(targetVal), e.TypeName, e.TypeName),
				fmt.Sprintf("Expected a map from '<text> as json' or a %s record matching 'def %s'.", e.TypeName, e.TypeName),
			)
		}

		mapped, mapErr := mapToRecord(mv.Fields, proto, env, e.TypeName, mv.FromCSV)
		if mapErr != nil {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				mapErr.Error(),
				fmt.Sprintf("Ensure all fields declared in 'def %s' are present with matching types and valid union values.", e.TypeName),
			)
		}
		return mapped, nil
	}
}

func mapToRecord(rawMap map[string]Value, proto *RecordValue, env *Env, path string, fromCSV bool) (*RecordValue, error) {
	fields := make(map[string]Value, len(proto.Fields))
	order := make([]string, len(proto.Order))
	copy(order, proto.Order)

	for _, fieldName := range proto.Order {
		defaultVal := proto.Fields[fieldName]
		fieldPath := path + "." + fieldName
		rawVal, exists := rawMap[fieldName]
		if !exists {
			return nil, fmt.Errorf("Missing field '%s'", fieldPath)
		}
		if opts, hasAllowed := proto.Allowed[fieldName]; hasAllowed && len(opts) > 0 {
			converted, cErr := coerceCSVOrUnionOptions(rawVal, opts, env, fieldPath, fromCSV)
			if cErr != nil {
				allowed := formatUnionOptions(opts)
				return nil, fmt.Errorf("Invalid value %s for field '%s': allowed values are %s (%s)", FormatValue(rawVal), fieldPath, strings.Join(allowed, ", "), cErr.Error())
			}
			fields[fieldName] = converted
			continue
		}
		converted, err := coerceToTemplate(rawVal, defaultVal, env, fieldPath, fromCSV)
		if err != nil {
			return nil, err
		}
		fields[fieldName] = converted
	}

	return &RecordValue{
		TypeName:    proto.TypeName,
		Fields:      fields,
		Allowed:     proto.Allowed,
		Order:       order,
		IsPrototype: false,
	}, nil
}

func coerceCSVOrUnionOptions(rawVal Value, opts []Value, env *Env, fieldPath string, fromCSV bool) (Value, error) {
	if fromCSV {
		if s, isStr := rawVal.(string); isStr {
			if n, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
				for _, opt := range opts {
					if _, isNumOpt := opt.(float64); isNumOpt && opt == n {
						return n, nil
					}
					if bt, isBT := opt.(BaseTypeValue); isBT && string(bt) == "number" {
						return n, nil
					}
				}
			}
		}
	}
	return coerceToUnionOptions(rawVal, opts, env, fieldPath)
}

func coerceToTemplate(val Value, template Value, env *Env, path string, fromCSV bool) (Value, error) {
	switch tmpl := template.(type) {
	case float64:
		if fromCSV {
			if s, isStr := val.(string); isStr {
				n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
					return nil, fmt.Errorf("Field '%s' expected number, got %s (%s)", path, structuredTypeName(val), FormatValue(val))
				}
				return n, nil
			}
		}
		n, ok := val.(float64)
		if !ok {
			return nil, fmt.Errorf("Field '%s' expected number, got %s (%s)", path, structuredTypeName(val), FormatValue(val))
		}
		return n, nil
	case string:
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("Field '%s' expected text, got %s (%s)", path, structuredTypeName(val), FormatValue(val))
		}
		return s, nil
	case *RecordValue:
		if subRec, ok := val.(*RecordValue); ok && subRec.TypeName == tmpl.TypeName {
			return subRec, nil
		}
		subMap, ok := val.(*MapValue)
		if !ok || subMap == nil {
			return nil, fmt.Errorf("Field '%s' expected %s map/record, got %s", path, tmpl.TypeName, structuredTypeName(val))
		}
		return mapToRecord(subMap.Fields, tmpl, env, path, subMap.FromCSV)
	case *ListValue:
		lv, ok := val.(*ListValue)
		if !ok {
			return nil, fmt.Errorf("Field '%s' expected list, got %s", path, structuredTypeName(val))
		}
		if (tmpl.ElemType == "number" || tmpl.ElemType == "text") && lv.ElemType != "" && lv.ElemType != tmpl.ElemType {
			return nil, fmt.Errorf("Field '%s' expected list of %s, got list of %s", path, tmpl.ElemType, lv.ElemType)
		}
		if tmpl.ElemType != "" && len(lv.Elements) == 0 && lv.ElemType != "" && lv.ElemType != tmpl.ElemType {
			if _, isUnion := lookupUnion(env, tmpl.ElemType); !isUnion {
				return nil, fmt.Errorf("Field '%s' expected list of %s, got list of %s", path, tmpl.ElemType, lv.ElemType)
			}
		}
		if tmpl.ElemType != "" {
			elems, err := coerceTypedListElements(lv.Elements, tmpl.ElemType, env, path)
			if err != nil {
				return nil, err
			}
			return &ListValue{ElemType: tmpl.ElemType, Elements: elems}, nil
		}
		elems := make([]Value, 0, len(lv.Elements))
		outElemType := tmpl.ElemType
		for idx, item := range lv.Elements {
			elemPath := fmt.Sprintf("%s.%d", path, idx+1)
			et := structuredTypeName(item)
			if idx == 0 {
				outElemType = et
			} else if et != outElemType {
				return nil, fmt.Errorf("Element '%s' expected %s, got %s", elemPath, outElemType, et)
			}
			elems = append(elems, item)
		}
		return &ListValue{ElemType: outElemType, Elements: elems}, nil
	default:
		return val, nil
	}
}

func structuredTypeName(v any) string {
	if v == nil {
		return "none"
	}
	return TypeName(v)
}

func (ev *Evaluator) parseJSONValue(rawStr string, line, col int) (Value, error) {
	if strings.TrimSpace(rawStr) == "" {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			"Cannot parse empty text as json",
			"Provide non-empty JSON text before 'as json'.",
		)
	}
	var parsed any
	if err := json.Unmarshal([]byte(rawStr), &parsed); err != nil {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Invalid JSON in 'as json': %v", err),
			"Provide valid JSON text.",
		)
	}
	return jsonAnyToEase(parsed), nil
}

func (ev *Evaluator) formatJSONValue(val Value, line, col int) (string, error) {
	switch v := val.(type) {
	case *RecordValue, *ListValue, *MapValue:
		if err := validateJSONEncodable(v); err != nil {
			return "", newError("TypeError", line, col, ev.source, err.Error(), "Conditions and union definitions cannot be serialized as JSON.")
		}
		b, err := json.Marshal(easeValueToJSON(v))
		if err != nil {
			return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Cannot format %s as json: %v", TypeName(val), err), "")
		}
		return string(b), nil
	case float64:
		return "", newError(
			"TypeError",
			line,
			col,
			ev.source,
			"Cannot format number with 'as json'",
			fmt.Sprintf("Use '%s as text' for numbers, or pass a record, list, map, or JSON text to 'as json'.", FormatValue(v)),
		)
	default:
		return "", newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Cannot format %s as json", TypeName(val)),
			"Pass JSON text to parse, or a record, list, or map to serialize with 'as json'.",
		)
	}
}

func (ev *Evaluator) formatCSVValue(val Value, env *Env, line, col int) (string, error) {
	lv, ok := val.(*ListValue)
	if !ok {
		return "", newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Cannot format %s as csv; 'as csv' requires CSV text or a list of flat records/maps", TypeName(val)),
			"Wrap records in a list (e.g. 'rows as csv') or pass CSV text ('raw as csv').",
		)
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	if lv.ElemType != "" {
		protoVal, ok := lookupTypeInEnv(env, lv.ElemType)
		if !ok {
			if lv.ElemType == "map" {
				goto formatMapRows
			}
			return "", newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot format list of %s as csv; 'as csv' requires a list of flat records or maps", lv.ElemType),
				"Use a list of flat 'def' records or maps with text/number fields.",
			)
		}
		proto, isRec := protoVal.(*RecordValue)
		if !isRec {
			return "", newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot format list of %s as csv; 'as csv' requires a list of flat records or maps", lv.ElemType),
				"Use a list of flat 'def' records or maps with text/number fields.",
			)
		}
		for _, fieldName := range proto.Order {
			if opts, hasUnion := proto.Allowed[fieldName]; hasUnion {
				for _, opt := range opts {
					if optRec, isOptRec := opt.(*RecordValue); isOptRec && optRec.IsPrototype {
						return "", newError(
							"TypeError",
							line,
							col,
							ev.source,
							fmt.Sprintf("Cannot format record %q as csv: field '%s.%s' is %s (CSV fields must be text or number)", proto.TypeName, proto.TypeName, fieldName, optRec.TypeName),
							"Flatten nested records/lists into atomic text or number fields before converting to CSV.",
						)
					}
				}
			}
			fVal := proto.Fields[fieldName]
			if _, isList := fVal.(*ListValue); isList {
				return "", newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Cannot format record %q as csv: field '%s.%s' is list (CSV fields must be text or number)", proto.TypeName, proto.TypeName, fieldName),
					"Flatten nested records/lists into atomic text or number fields before converting to CSV.",
				)
			}
			if _, isSubRec := fVal.(*RecordValue); isSubRec {
				return "", newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Cannot format record %q as csv: field '%s.%s' is %s (CSV fields must be text or number)", proto.TypeName, proto.TypeName, fieldName, TypeName(fVal)),
					"Flatten nested records/lists into atomic text or number fields before converting to CSV.",
				)
			}
		}
		headers := proto.Order
		if err := w.Write(headers); err != nil {
			return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to write CSV header: %v", err), "")
		}
		for idx, el := range lv.Elements {
			rec, ok := el.(*RecordValue)
			if !ok {
				return "", newError("TypeError", line, col, ev.source, fmt.Sprintf("CSV row at index %d must be %s, got %s", idx+1, proto.TypeName, TypeName(el)), "")
			}
			row := make([]string, len(headers))
			for i, h := range headers {
				fv := rec.Fields[h]
				if _, isList := fv.(*ListValue); isList {
					return "", newError("TypeError", line, col, ev.source, fmt.Sprintf("Cannot format record %q as csv: field '%s.%s' is list (CSV fields must be text or number)", rec.TypeName, rec.TypeName, h), "")
				}
				if _, isSubRec := fv.(*RecordValue); isSubRec {
					return "", newError("TypeError", line, col, ev.source, fmt.Sprintf("Cannot format record %q as csv: field '%s.%s' is %s (CSV fields must be text or number)", rec.TypeName, rec.TypeName, h, TypeName(fv)), "")
				}
				row[i] = FormatValue(fv)
			}
			if err := w.Write(row); err != nil {
				return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to write CSV row %d: %v", idx+1, err), "")
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to flush CSV output: %v", err), "")
		}
		return buf.String(), nil
	}

formatMapRows:
	if len(lv.Elements) == 0 {
		return "", newError(
			"TypeError",
			line,
			col,
			ev.source,
			"Cannot format empty untyped list as csv without column headers",
			"Create a typed record list (e.g. 'list as MyRow') or provide at least one map row.",
		)
	}
	if firstRec, isRec := lv.Elements[0].(*RecordValue); isRec {
		headers := firstRec.Order
		if err := w.Write(headers); err != nil {
			return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to write CSV header: %v", err), "")
		}
		for idx, el := range lv.Elements {
			rec, ok := el.(*RecordValue)
			if !ok || rec.TypeName != firstRec.TypeName {
				return "", newError("TypeError", line, col, ev.source, fmt.Sprintf("CSV row at index %d must be %s, got %s", idx+1, firstRec.TypeName, TypeName(el)), "")
			}
			row := make([]string, len(headers))
			for i, h := range headers {
				fv := rec.Fields[h]
				switch fv.(type) {
				case string, float64:
					row[i] = FormatValue(fv)
				default:
					return "", newError("TypeError", line, col, ev.source, fmt.Sprintf("Cannot format record %q as csv: field '%s.%s' is %s (CSV fields must be text or number)", rec.TypeName, rec.TypeName, h, TypeName(fv)), "")
				}
			}
			if err := w.Write(row); err != nil {
				return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to write CSV row %d: %v", idx+1, err), "")
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to flush CSV output: %v", err), "")
		}
		return buf.String(), nil
	}

	firstMap, isMap := lv.Elements[0].(*MapValue)
	if !isMap {
		return "", newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Cannot format list of %s as csv; 'as csv' requires a list of flat records or maps", TypeName(lv.Elements[0])),
			"Use a list of flat 'def' records or maps with text/number fields.",
		)
	}
	headers := firstMap.Order
	if len(headers) == 0 {
		return "", newError("TypeError", line, col, ev.source, "Cannot format empty map row as csv", "")
	}
	if err := w.Write(headers); err != nil {
		return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to write CSV header: %v", err), "")
	}
	for idx, el := range lv.Elements {
		mv, ok := el.(*MapValue)
		if !ok {
			return "", newError("TypeError", line, col, ev.source, fmt.Sprintf("CSV row at index %d must be a map, got %s", idx+1, TypeName(el)), "")
		}
		if len(mv.Fields) != len(headers) {
			return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("CSV row at index %d has %d columns, expected %d", idx+1, len(mv.Fields), len(headers)), "")
		}
		row := make([]string, len(headers))
		for i, h := range headers {
			fv, exists := mv.Fields[h]
			if !exists {
				return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("CSV row at index %d is missing column %q", idx+1, h), "")
			}
			switch fv.(type) {
			case string, float64:
				row[i] = FormatValue(fv)
			default:
				return "", newError("TypeError", line, col, ev.source, fmt.Sprintf("CSV field %q at row %d must be text or number, got %s", h, idx+1, TypeName(fv)), "")
			}
		}
		if err := w.Write(row); err != nil {
			return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to write CSV row %d: %v", idx+1, err), "")
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to flush CSV output: %v", err), "")
	}
	return buf.String(), nil
}
