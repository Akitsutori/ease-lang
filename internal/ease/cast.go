package ease

import (
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

func lookupUnion(env *Env, name string) (*UnionValue, bool) {
	if env == nil {
		return nil, false
	}
	v, ok := env.Lookup(name)
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
			return rec.TypeName == elemType
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
			mapped, err := mapToRecord(mv.Fields, recOpt, env, prefix)
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
			if !matchesEaseType(item, elemType) {
				if pathPrefix == "" {
					return nil, fmt.Errorf("element %d expected %s, got %s (%s)", idx, elemType, structuredTypeName(item), FormatValue(item))
				}
				return nil, fmt.Errorf("Element '%s.%d' expected %s, got %s", pathPrefix, idx, elemType, structuredTypeName(item))
			}
			out = append(out, item)
		}
		return out, nil
	default:
		protoVal, found := env.Lookup(elemType)
		if !found {
			return nil, fmt.Errorf("Unknown type %q for '%s'", elemType, pathPrefix)
		}
		if union, isUnion := protoVal.(*UnionValue); isUnion {
			for idx, item := range elems {
				coerced, cErr := coerceToUnionOptions(item, union.Values, env, fmt.Sprintf("%s.%d", pathPrefix, idx))
				if cErr != nil {
					return nil, fmt.Errorf("Invalid value %s at element %d of union %q: %s", FormatValue(item), idx, elemType, cErr.Error())
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
			elemPath := fmt.Sprintf("%s.%d", pathPrefix, idx)
			if rec, ok := item.(*RecordValue); ok {
				if rec.TypeName != elemType {
					return nil, fmt.Errorf("expected structured map or %s record (element %d is %s)", elemType, idx, rec.TypeName)
				}
				out = append(out, rec)
				continue
			}
			mv, isMap := item.(*MapValue)
			if !isMap || mv == nil {
				return nil, fmt.Errorf("expected structured map or %s record (element %d is %s)", elemType, idx, structuredTypeName(item))
			}
			mapped, mapErr := mapToRecord(mv.Fields, proto, env, elemPath)
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
		protoVal, ok := env.Lookup(targetType)
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
				fmt.Sprintf("Expected a list of maps from 'do decode_json' or %s records.", targetType),
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

func (ev *Evaluator) castValue(e *CastExpr, targetVal Value, env *Env) (Value, error) {
	line, col := e.TypeLine, e.TypeCol
	if line == 0 {
		line, col = e.Line, e.Col
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
				"Only atomic values (text, number) can be cast with 'as text'. Use 'do encode_json ...' to serialize structured data to JSON.",
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
			n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
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
		protoVal, ok := env.Lookup(e.TypeName)
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
			coerced, cErr := coerceToUnionOptions(targetVal, union.Values, env, e.TypeName)
			if cErr != nil {
				allowed := formatUnionOptions(union.Values)
				msg := fmt.Sprintf("Value %s is not valid for union %q", FormatValue(targetVal), e.TypeName)
				if _, isMap := targetVal.(*MapValue); isMap {
					msg = fmt.Sprintf("Value %s is not valid for union %q (%s)", FormatValue(targetVal), e.TypeName, cErr.Error())
				}
				return nil, newError(
					"RuntimeError",
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
				fmt.Sprintf("Expected a map from 'do decode_json' or a %s record matching 'def %s'.", e.TypeName, e.TypeName),
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
					fmt.Sprintf("Parse JSON text explicitly first with 'do decode_json ...', then validate with 'as %s'.", e.TypeName),
				)
			}
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot validate %s as %s: expected structured map or %s record", TypeName(targetVal), e.TypeName, e.TypeName),
				fmt.Sprintf("Expected a map from 'do decode_json' or a %s record matching 'def %s'.", e.TypeName, e.TypeName),
			)
		}

		mapped, mapErr := mapToRecord(mv.Fields, proto, env, e.TypeName)
		if mapErr != nil {
			return nil, newError(
				"RuntimeError",
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

func mapToRecord(rawMap map[string]Value, proto *RecordValue, env *Env, path string) (*RecordValue, error) {
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
			converted, cErr := coerceToUnionOptions(rawVal, opts, env, fieldPath)
			if cErr != nil {
				allowed := formatUnionOptions(opts)
				return nil, fmt.Errorf("Invalid value %s for field '%s': allowed values are %s (%s)", FormatValue(rawVal), fieldPath, strings.Join(allowed, ", "), cErr.Error())
			}
			fields[fieldName] = converted
			continue
		}
		converted, err := coerceToTemplate(rawVal, defaultVal, env, fieldPath)
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

func coerceToTemplate(val Value, template Value, env *Env, path string) (Value, error) {
	switch tmpl := template.(type) {
	case float64:
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
		return mapToRecord(subMap.Fields, tmpl, env, path)
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
			elemPath := fmt.Sprintf("%s.%d", path, idx)
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
