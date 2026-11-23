package ease

import (
	"fmt"
	"sort"
	"strings"
)

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
			if v.callSpecDepth > 0 {
				hasMatchingLit := false
				for _, uv := range uVariants {
					if (fbSt.exactBaseType == "text" && uv.kind == variantStringLit) ||
						(fbSt.exactBaseType == "number" && uv.kind == variantNumberLit) {
						hasMatchingLit = true
						break
					}
				}
				if hasMatchingLit {
					return nil
				}
			}
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

func (v *typeValidator) checkSaveCastExpr(ex *CastExpr, call *CallExpr) error {
	for _, arg := range call.Args {
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
	if ex.Fallback != nil {
		if err := v.checkExpr(ex.Fallback); err != nil {
			return err
		}
		if !v.inErrorTestExpr {
			return newError(
				"TypeError",
				ex.OrLine,
				ex.OrCol,
				v.source,
				"'or' fallback cannot be used with 'save'",
				"Write: save \"./out.json\" data as json",
			)
		}
		return nil
	}
	if v.inErrorTestExpr || len(call.Args) != 2 {
		return nil
	}
	dataExpr := call.Args[1]
	dataSt := v.inferExprTypeState(dataExpr)
	switch ex.TypeName {
	case "text":
		if dataSt.exactRecord != "" || dataSt.listElemType != "" || (dataSt.exactBaseType != "" && dataSt.exactBaseType != "text") {
			return newError(
				"TypeError",
				ex.TypeLine,
				ex.TypeCol,
				v.source,
				"save ... as text requires a text value",
				"Use 'as json', 'as csv', or 'as bytes' for structured data or byte lists.",
			)
		}
		return nil
	case "json":
		if dataSt.exactBaseType == "text" {
			return newError(
				"TypeError",
				ex.TypeLine,
				ex.TypeCol,
				v.source,
				"Cannot save raw text with 'as json'; pass a record, list, or map instead",
				"To save a text string directly, omit 'as json': save \"./out.txt\" txt",
			)
		}
		if dataSt.exactBaseType == "number" {
			return newError(
				"TypeError",
				ex.TypeLine,
				ex.TypeCol,
				v.source,
				"Cannot save number with 'as json'",
				"Convert the number with 'as text' and save as text, or pass a record, list, or map with 'as json'.",
			)
		}
		return nil
	case "csv":
		return v.checkCSVFormatArg(dataExpr, ex.TypeLine, ex.TypeCol)
	case "bytes":
		if dataSt.listElemType != "" && dataSt.listElemType != "number" && dataSt.listElemType != "any" {
			return newError(
				"TypeError",
				ex.TypeLine,
				ex.TypeCol,
				v.source,
				fmt.Sprintf("save ... as bytes requires a list of byte numbers (0..255), got list of %s", dataSt.listElemType),
				"Pass a list of integer byte values (0..255) to 'save ... as bytes'.",
			)
		}
		if dataSt.exactRecord != "" || dataSt.exactBaseType != "" || dataSt.unionName != "" || dataSt.inboxElemType != "" {
			return newError(
				"TypeError",
				ex.TypeLine,
				ex.TypeCol,
				v.source,
				"save ... as bytes requires a list of byte numbers (0..255)",
				"Example: save \"./out.bin\" byte_list as bytes",
			)
		}
		return nil
	default:
		return newError(
			"TypeError",
			ex.TypeLine,
			ex.TypeCol,
			v.source,
			fmt.Sprintf("Unsupported format %q for 'save'", ex.TypeName),
			"Supported formats for 'save' are: text, json, csv, bytes.",
		)
	}
}

func (v *typeValidator) checkCSVFormatArg(target Expr, line, col int) error {
	if isConditionExpr(target) {
		return newError("TypeError", line, col, v.source, "Conditions cannot be formatted as csv", "Pass CSV text or a list of flat records/maps to 'as csv'.")
	}
	st := v.inferExprTypeState(target)
	if st.exactBaseType == "text" {
		return nil
	}
	if st.listElemType != "" {
		if st.listElemType == "map" || st.listElemType == "any" {
			return nil
		}
		if defDecl, isDef := v.defs[st.listElemType]; isDef {
			for _, f := range defDecl.Fields {
				if fieldType, nonFlat := v.nonFlatCSVFieldType(f); nonFlat {
					return newError(
						"TypeError",
						line,
						col,
						v.source,
						fmt.Sprintf("Cannot format record %q as csv: field '%s.%s' is %s (CSV fields must be text or number)", defDecl.Name, defDecl.Name, f.Name, fieldType),
						"Flatten nested records/lists into atomic text or number fields before converting to CSV.",
					)
				}
			}
			return nil
		}
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			fmt.Sprintf("Cannot format list of %s as csv; 'as csv' requires a list of flat records or maps", st.listElemType),
			"Use a list of flat 'def' records or maps with text/number fields.",
		)
	}
	if st.exactRecord != "" || st.exactBaseType != "" || st.unionName != "" || st.inboxElemType != "" {
		return newError(
			"TypeError",
			line,
			col,
			v.source,
			"Cannot format non-table value as csv; 'as csv' requires CSV text or a list of flat records/maps",
			"Wrap records in a list (e.g. 'rows as csv') or pass CSV text ('raw as csv').",
		)
	}
	return nil
}

func (v *typeValidator) nonFlatCSVFieldType(f DefField) (string, bool) {
	if _, isList := f.Default.(*TypedListExpr); isList {
		return "list", true
	}
	if _, isList := f.Default.(*ListExpr); isList {
		return "list", true
	}
	st := v.inferExprTypeState(f.Default)
	if st.listElemType != "" {
		return "list", true
	}
	if st.exactRecord != "" {
		return st.exactRecord, true
	}
	for _, opt := range f.Allowed {
		optSt := v.inferExprTypeState(opt)
		if optSt.exactRecord != "" {
			return optSt.exactRecord, true
		}
		if optSt.unionName != "" {
			for _, uv := range v.resolvedUnions[optSt.unionName] {
				if uv.kind == variantRecord {
					return uv.recordName, true
				}
			}
		}
	}
	return "", false
}
