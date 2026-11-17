package ease

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type unionVariantKind int

const (
	variantStringLit unionVariantKind = iota
	variantNumberLit
	variantBaseType // "text" or "number"
	variantRecord
)

type resolvedUnionVariant struct {
	kind       unionVariantKind
	strVal     string
	numVal     float64
	rawNum     string
	baseType   string
	recordName string
	line       int
	col        int
}

func (rv resolvedUnionVariant) key() string {
	switch rv.kind {
	case variantStringLit:
		return "str:" + rv.strVal
	case variantNumberLit:
		return fmt.Sprintf("num:%.17g", rv.numVal)
	case variantBaseType:
		return "base:" + rv.baseType
	case variantRecord:
		return "rec:" + rv.recordName
	default:
		return ""
	}
}

func (rv resolvedUnionVariant) display() string {
	switch rv.kind {
	case variantStringLit:
		return strconv.Quote(rv.strVal)
	case variantNumberLit:
		if rv.rawNum != "" {
			return rv.rawNum
		}
		return FormatValue(rv.numVal)
	case variantBaseType:
		return rv.baseType
	case variantRecord:
		return rv.recordName
	default:
		return "unknown"
	}
}

func (rv resolvedUnionVariant) displayPlain() string {
	switch rv.kind {
	case variantStringLit:
		return rv.strVal
	case variantNumberLit:
		if rv.rawNum != "" {
			return rv.rawNum
		}
		return FormatValue(rv.numVal)
	case variantBaseType:
		return rv.baseType
	case variantRecord:
		return rv.recordName
	default:
		return "unknown"
	}
}

func (v *typeValidator) resolveAndValidateUnions(prog *Program) error {
	resolving := make(map[string]bool, len(prog.Unions))
	for _, u := range prog.Unions {
		if _, err := v.resolveUnion(u, resolving); err != nil {
			return err
		}
	}
	return nil
}

func (v *typeValidator) resolveUnion(u *UnionDecl, resolving map[string]bool) ([]resolvedUnionVariant, error) {
	if cached, ok := v.resolvedUnions[u.Name]; ok {
		return cached, nil
	}
	if resolving[u.Name] {
		return nil, newError(
			"TypeError",
			u.Line,
			u.Col,
			v.source,
			fmt.Sprintf("Cyclic reference in union %q", u.Name),
			"Remove recursive union references so each union is finite.",
		)
	}
	resolving[u.Name] = true
	defer delete(resolving, u.Name)

	var flat []resolvedUnionVariant
	for _, ve := range u.Variants {
		if !u.Imported {
			if err := v.checkExpr(ve); err != nil {
				return nil, err
			}
		}
		expanded, err := v.expandUnionVariantExpr(ve, u, resolving)
		if err != nil {
			return nil, err
		}
		flat = append(flat, expanded...)
	}

	if !u.Imported {
		if err := v.validateFlattenedUnion(u, flat); err != nil {
			return nil, err
		}
	}

	v.resolvedUnions[u.Name] = flat
	return flat, nil
}

func (v *typeValidator) expandUnionVariantExpr(expr Expr, owner *UnionDecl, resolving map[string]bool) ([]resolvedUnionVariant, error) {
	line, col := expr.Pos()
	switch e := expr.(type) {
	case *StringExpr:
		return []resolvedUnionVariant{{
			kind:   variantStringLit,
			strVal: e.Value,
			line:   line,
			col:    col,
		}}, nil

	case *NumberExpr:
		return []resolvedUnionVariant{{
			kind:   variantNumberLit,
			numVal: e.Value,
			rawNum: e.Raw,
			line:   line,
			col:    col,
		}}, nil

	case *UnaryExpr:
		if e.Op == "-" {
			if num, ok := e.Right.(*NumberExpr); ok {
				return []resolvedUnionVariant{{
					kind:   variantNumberLit,
					numVal: -num.Value,
					rawNum: "-" + num.Raw,
					line:   line,
					col:    col,
				}}, nil
			}
		}

	case *IdentExpr:
		switch e.Name {
		case "text", "number":
			return []resolvedUnionVariant{{
				kind:     variantBaseType,
				baseType: e.Name,
				line:     line,
				col:      col,
			}}, nil
		case "bool", "boolean":
			return nil, newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Boolean (%q) is not a built-in type in Ease", e.Name),
				"Use string/number literals, 'text', 'number', 'def' records, or other named unions.",
			)
		case "string":
			return nil, newError(
				"TypeError",
				line,
				col,
				v.source,
				"Unknown type 'string' in union definition",
				"Did you mean 'text'? Ease uses 'text' instead of 'string'.",
			)
		}

		if subUnion, ok := v.unions[e.Name]; ok {
			if !owner.Imported && !subUnion.Imported && line > 0 && subUnion.Line > line {
				return nil, newError(
					"TypeError",
					line,
					col,
					v.source,
					fmt.Sprintf("Union %q is used in %q before it is defined (defined on line %d)", e.Name, owner.Name, subUnion.Line),
					fmt.Sprintf("Move 'def %s' above line %d.", e.Name, line),
				)
			}
			subVariants, err := v.resolveUnion(subUnion, resolving)
			if err != nil {
				return nil, err
			}
			out := make([]resolvedUnionVariant, len(subVariants))
			for i, sv := range subVariants {
				sv.line = line
				sv.col = col
				out[i] = sv
			}
			return out, nil
		}

		if defDecl, ok := v.defs[e.Name]; ok {
			if !owner.Imported && !defDecl.Imported && line > 0 && defDecl.Line > line {
				return nil, newError(
					"TypeError",
					line,
					col,
					v.source,
					fmt.Sprintf("Record %q is used in %q before it is defined (defined on line %d)", e.Name, owner.Name, defDecl.Line),
					fmt.Sprintf("Move 'def %s' above line %d.", e.Name, line),
				)
			}
			return []resolvedUnionVariant{{
				kind:       variantRecord,
				recordName: e.Name,
				line:       line,
				col:        col,
			}}, nil
		}

		if constLine, isConst := v.consts[e.Name]; isConst {
			return nil, newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Constant %q (defined on line %d) is not a valid union member", e.Name, constLine),
				"Union members must be literals (\"OK\", 404), base types (text, number), 'def' records, or named unions.",
			)
		}

		if !owner.Imported {
			return nil, newError(
				"TypeError",
				line,
				col,
				v.source,
				fmt.Sprintf("Unknown union member %q in %q", e.Name, owner.Name),
				"Union members must be literals, 'text', 'number', a previously defined 'def' record, or another union.",
			)
		}
	}

	return nil, newError(
		"TypeError",
		line,
		col,
		v.source,
		fmt.Sprintf("Invalid union member in %q", owner.Name),
		"Allowed union members: literals (\"OK\", 404), 'text', 'number', 'def' records, or named unions.",
	)
}

func (v *typeValidator) validateFlattenedUnion(u *UnionDecl, variants []resolvedUnionVariant) error {
	// Guardrail 2: No duplicate members (also catches duplicates after flattening).
	seen := make(map[string]resolvedUnionVariant, len(variants))
	hasText := false
	hasNumber := false
	var recordVariants []resolvedUnionVariant

	for _, vr := range variants {
		k := vr.key()
		if _, dup := seen[k]; dup {
			return newError(
				"TypeError",
				vr.line,
				vr.col,
				v.source,
				fmt.Sprintf("Duplicate union member %s in union %q", vr.display(), u.Name),
				fmt.Sprintf("Remove the duplicate %s member from union %q.", vr.display(), u.Name),
			)
		}
		seen[k] = vr
		if vr.kind == variantBaseType {
			if vr.baseType == "text" {
				hasText = true
			} else if vr.baseType == "number" {
				hasNumber = true
			}
		} else if vr.kind == variantRecord {
			recordVariants = append(recordVariants, vr)
		}
	}

	// Guardrail 1: No redundancy ('OK' is already covered by 'text', numbers by 'number').
	if hasText || hasNumber {
		for _, vr := range variants {
			if hasText && vr.kind == variantStringLit {
				return newError(
					"TypeError",
					vr.line,
					vr.col,
					v.source,
					fmt.Sprintf("'%s' is already covered by 'text'", vr.displayPlain()),
					fmt.Sprintf("Remove %s or remove 'text' from union %q.", vr.display(), u.Name),
				)
			}
			if hasNumber && vr.kind == variantNumberLit {
				return newError(
					"TypeError",
					vr.line,
					vr.col,
					v.source,
					fmt.Sprintf("'%s' is already covered by 'number'", vr.displayPlain()),
					fmt.Sprintf("Remove %s or remove 'number' from union %q.", vr.display(), u.Name),
				)
			}
		}
	}

	// Guardrail 3: Records inside the same union must be distinguishable for `as`.
	for i := 0; i < len(recordVariants); i++ {
		for j := i + 1; j < len(recordVariants); j++ {
			r1 := recordVariants[i]
			r2 := recordVariants[j]
			if !v.recordsDistinguishable(r1.recordName, r2.recordName, make(map[string]bool)) {
				return newError(
					"TypeError",
					r2.line,
					r2.col,
					v.source,
					fmt.Sprintf("Records %q and %q in union %q are indistinguishable", r1.recordName, r2.recordName, u.Name),
					fmt.Sprintf("Ensure %s and %s differ in at least one mandatory field or field type so 'as %s' can tell them apart.", r1.recordName, r2.recordName, u.Name),
				)
			}
		}
	}

	return nil
}

func (v *typeValidator) validateDefFields(d *DefDecl) error {
	for _, f := range d.Fields {
		if err := v.checkExpr(f.Default); err != nil {
			return err
		}
		if id, ok := f.Default.(*IdentExpr); ok {
			if info, exists := v.known[id.Name]; exists && !info.imported && f.Line > 0 && info.line > f.Line {
				return newError(
					"TypeError",
					f.Line,
					f.Col,
					v.source,
					fmt.Sprintf("Type %q is used in 'def %s' before it is defined (defined on line %d)", id.Name, d.Name, info.line),
					fmt.Sprintf("Move 'def %s' above line %d.", id.Name, f.Line),
				)
			}
		}
		if len(f.Allowed) > 0 {
			tmpUnion := &UnionDecl{
				posInfo:  posInfo{Line: f.Line, Col: f.Col},
				Name:     fmt.Sprintf("%s.%s", d.Name, f.Name),
				Variants: f.Allowed,
			}
			resolving := make(map[string]bool)
			var flat []resolvedUnionVariant
			for _, opt := range f.Allowed {
				if err := v.checkExpr(opt); err != nil {
					return err
				}
				expanded, err := v.expandUnionVariantExpr(opt, tmpUnion, resolving)
				if err != nil {
					return err
				}
				flat = append(flat, expanded...)
			}
			if err := v.validateFlattenedUnion(tmpUnion, flat); err != nil {
				return err
			}
		}
	}
	return nil
}

func addNumberSchema(s *fieldSchema, val float64, defaultWildcard bool) {
	if defaultWildcard {
		s.anyNumber = true
		return
	}
	if s.numLits == nil {
		s.numLits = make(map[float64]bool)
	}
	s.numLits[val] = true
}

func (v *typeValidator) recordFieldSchemas(recName string) map[string]fieldSchema {
	d, ok := v.defs[recName]
	if !ok {
		return nil
	}
	out := make(map[string]fieldSchema, len(d.Fields))
	for _, f := range d.Fields {
		out[f.Name] = v.fieldToSchema(f)
	}
	return out
}

func (v *typeValidator) fieldToSchema(f DefField) fieldSchema {
	if len(f.Allowed) > 0 {
		var s fieldSchema
		for _, opt := range f.Allowed {
			v.mergeExprIntoSchema(&s, opt, false)
		}
		return s
	}
	var s fieldSchema
	v.mergeExprIntoSchema(&s, f.Default, true)
	return s
}

func (v *typeValidator) mergeExprIntoSchema(s *fieldSchema, expr Expr, defaultWildcard bool) {
	switch e := expr.(type) {
	case *StringExpr, *InterpStringExpr:
		if defaultWildcard {
			s.anyText = true
		} else if str, ok := e.(*StringExpr); ok {
			if s.strLits == nil {
				s.strLits = make(map[string]bool)
			}
			s.strLits[str.Value] = true
		} else {
			s.anyText = true
		}
	case *NumberExpr:
		addNumberSchema(s, e.Value, defaultWildcard)
	case *UnaryExpr:
		if e.Op == "-" {
			if num, ok := e.Right.(*NumberExpr); ok {
				addNumberSchema(s, -num.Value, defaultWildcard)
				return
			}
		}
		s.isDynamic = true
	case *ListExpr:
		s.isList = true
		if len(e.Elements) == 0 {
			s.listElem = "any"
		} else {
			var elSchema fieldSchema
			v.mergeExprIntoSchema(&elSchema, e.Elements[0], true)
			switch {
			case elSchema.anyText:
				s.listElem = "text"
			case elSchema.anyNumber:
				s.listElem = "number"
			case len(elSchema.records) == 1:
				for r := range elSchema.records {
					s.listElem = r
				}
			default:
				s.listElem = "any"
			}
		}
	case *TypedListExpr:
		s.isList = true
		s.listElem = e.ElemType
	case *CallExpr:
		if e.Callee == "range" {
			s.isList = true
			s.listElem = "number"
		} else {
			s.isDynamic = true
		}
	case *WithExpr:
		if id, ok := e.Base.(*IdentExpr); ok && v.defs[id.Name] != nil {
			if s.records == nil {
				s.records = make(map[string]bool)
			}
			s.records[id.Name] = true
		} else {
			s.isDynamic = true
		}
	case *IdentExpr:
		switch e.Name {
		case "text":
			s.anyText = true
			return
		case "number":
			s.anyNumber = true
			return
		}
		if v.defs[e.Name] != nil {
			if s.records == nil {
				s.records = make(map[string]bool)
			}
			s.records[e.Name] = true
			return
		}
		if variants, ok := v.resolvedUnions[e.Name]; ok {
			for _, vr := range variants {
				switch vr.kind {
				case variantBaseType:
					if vr.baseType == "text" {
						s.anyText = true
					} else if vr.baseType == "number" {
						s.anyNumber = true
					}
				case variantStringLit:
					if s.strLits == nil {
						s.strLits = make(map[string]bool)
					}
					s.strLits[vr.strVal] = true
				case variantNumberLit:
					if s.numLits == nil {
						s.numLits = make(map[float64]bool)
					}
					s.numLits[vr.numVal] = true
				case variantRecord:
					if s.records == nil {
						s.records = make(map[string]bool)
					}
					s.records[vr.recordName] = true
				}
			}
			return
		}
		if cExpr, ok := v.constExprs[e.Name]; ok {
			v.mergeExprIntoSchema(s, cExpr, true)
			return
		}
		s.isDynamic = true
	default:
		s.isDynamic = true
	}
}

func (v *typeValidator) recordsDistinguishable(r1, r2 string, visiting map[string]bool) bool {
	if r1 == r2 {
		return false
	}
	pairKey := r1 + "|" + r2
	if r2 < r1 {
		pairKey = r2 + "|" + r1
	}
	if visiting[pairKey] {
		return false
	}
	visiting[pairKey] = true
	defer delete(visiting, pairKey)

	f1 := v.recordFieldSchemas(r1)
	f2 := v.recordFieldSchemas(r2)
	for name := range f1 {
		if _, ok := f2[name]; !ok {
			return true
		}
	}
	for name := range f2 {
		if _, ok := f1[name]; !ok {
			return true
		}
	}
	for name, s1 := range f1 {
		s2 := f2[name]
		if !v.schemasOverlap(s1, s2, visiting) {
			return true
		}
	}
	return false
}

func (v *typeValidator) schemasOverlap(s1, s2 fieldSchema, visiting map[string]bool) bool {
	if s1.isDynamic || s2.isDynamic {
		return true
	}
	if s1.isList || s2.isList {
		if !(s1.isList && s2.isList) {
			return false
		}
		if s1.listElem == "" || s2.listElem == "" || s1.listElem == "any" || s2.listElem == "any" {
			return true
		}
		if s1.listElem == s2.listElem {
			return true
		}
		if v.defs[s1.listElem] != nil && v.defs[s2.listElem] != nil {
			return !v.recordsDistinguishable(s1.listElem, s2.listElem, visiting)
		}
		return false
	}
	if (s1.anyText && s2.anyText) || (s1.anyText && len(s2.strLits) > 0) || (s2.anyText && len(s1.strLits) > 0) {
		return true
	}
	for k := range s1.strLits {
		if s2.strLits[k] {
			return true
		}
	}
	if (s1.anyNumber && s2.anyNumber) || (s1.anyNumber && len(s2.numLits) > 0) || (s2.anyNumber && len(s1.numLits) > 0) {
		return true
	}
	for k := range s1.numLits {
		if s2.numLits[k] {
			return true
		}
	}
	for r1 := range s1.records {
		for r2 := range s2.records {
			if !v.recordsDistinguishable(r1, r2, visiting) {
				return true
			}
		}
	}
	return false
}

func (v *typeValidator) variantCoveredByUnion(tv resolvedUnionVariant, uVariants []resolvedUnionVariant) bool {
	for _, uv := range uVariants {
		if uv.key() == tv.key() {
			return true
		}
		if uv.kind == variantBaseType {
			if uv.baseType == "text" && tv.kind == variantStringLit {
				return true
			}
			if uv.baseType == "number" && tv.kind == variantNumberLit {
				return true
			}
		}
	}
	return false
}

func (v *typeValidator) findMatchingUnionForVariants(tested []resolvedUnionVariant, requireRecordInUnion bool) (string, bool) {
	if len(tested) == 0 {
		return "", false
	}
	var names []string
	for name := range v.resolvedUnions {
		names = append(names, name)
	}
	sort.Strings(names)

	type candidate struct {
		name  string
		count int
	}
	var candidates []candidate
	for _, name := range names {
		uVars := v.resolvedUnions[name]
		allIn := true
		hasRec := false
		for _, uv := range uVars {
			if uv.kind == variantRecord {
				hasRec = true
				break
			}
		}
		if requireRecordInUnion && !hasRec {
			continue
		}
		for _, tv := range tested {
			if !v.variantCoveredByUnion(tv, uVars) {
				allIn = false
				break
			}
		}
		if allIn {
			candidates = append(candidates, candidate{name: name, count: len(uVars)})
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].count != candidates[j].count {
			return candidates[i].count < candidates[j].count
		}
		return candidates[i].name < candidates[j].name
	})
	if len(candidates) == 1 || candidates[0].count < candidates[1].count {
		return candidates[0].name, true
	}
	return "", false
}

func (v *typeValidator) checkCheckExhaustiveness(st *CheckGuardStmt, hasFallback bool) error {
	if !st.IsBlock || len(st.ElseBody) > 0 || hasFallback || len(st.Rules) == 0 {
		return nil
	}
	for _, r := range st.Rules {
		if r.Action == nil {
			return nil
		}
	}

	var targetVar string
	var testedVariants []resolvedUnionVariant
	for _, r := range st.Rules {
		tVar, vars, ok := v.collectRuleIsCases(r.Cond)
		if !ok {
			return nil
		}
		if targetVar == "" {
			targetVar = tVar
		} else if tVar != targetVar {
			return nil
		}
		testedVariants = append(testedVariants, vars...)
	}
	if targetVar == "" || len(testedVariants) == 0 {
		return nil
	}

	var unionName string
	if varSt, ok := v.lookupVar(targetVar); ok {
		if varSt.unionName != "" {
			unionName = varSt.unionName
		} else if varSt.exactRecord != "" || varSt.exactNonRecord {
			return nil
		}
	}
	if unionName == "" {
		distinct := make(map[string]bool, len(testedVariants))
		for _, tv := range testedVariants {
			distinct[tv.key()] = true
		}
		if len(distinct) < 2 {
			return nil
		}
		matched, ok := v.findMatchingUnionForVariants(testedVariants, false)
		if !ok {
			return nil
		}
		unionName = matched
	}

	uVariants, exists := v.resolvedUnions[unionName]
	if !exists || len(uVariants) == 0 {
		return nil
	}

	covered := make(map[string]bool, len(testedVariants))
	coveredText := false
	coveredNumber := false
	for _, tv := range testedVariants {
		covered[tv.key()] = true
		if tv.kind == variantBaseType {
			if tv.baseType == "text" {
				coveredText = true
			} else if tv.baseType == "number" {
				coveredNumber = true
			}
		}
	}

	var missing []resolvedUnionVariant
	for _, uv := range uVariants {
		if covered[uv.key()] {
			continue
		}
		if uv.kind == variantStringLit && coveredText {
			continue
		}
		if uv.kind == variantNumberLit && coveredNumber {
			continue
		}
		missing = append(missing, uv)
	}

	if len(missing) == 0 {
		return nil
	}

	missingLabels := make([]string, len(missing))
	for i, mv := range missing {
		missingLabels[i] = mv.display()
	}
	missingText := strings.Join(missingLabels, ", ")
	caseWord := "Missing case"
	branchWord := "a branch"
	if len(missingLabels) > 1 {
		caseWord = "Missing cases"
		branchWord = "branches"
	}

	return newError(
		"TypeError",
		st.Line,
		st.Col,
		v.source,
		fmt.Sprintf("Non-exhaustive check on '%s' (%s: %s)", unionName, caseWord, missingText),
		fmt.Sprintf("Add %s for %s or add an 'else -> ...' fallback.", branchWord, missingText),
	)
}

func (v *typeValidator) collectRuleIsCases(cond Expr) (string, []resolvedUnionVariant, bool) {
	switch c := cond.(type) {
	case *BinaryExpr:
		if c.Op == "is" {
			id, ok := c.Left.(*IdentExpr)
			if !ok {
				return "", nil, false
			}
			vars, ok := v.exprToPatternVariants(c.Right)
			if !ok {
				return "", nil, false
			}
			return id.Name, vars, true
		}
		if c.Op == "or" {
			t1, v1, ok1 := v.collectRuleIsCases(c.Left)
			t2, v2, ok2 := v.collectRuleIsCases(c.Right)
			if !ok1 || !ok2 || t1 != t2 {
				return "", nil, false
			}
			return t1, append(v1, v2...), true
		}
		if c.Op == "and" {
			// Guarded patterns like `res is ErrorMessage and res.code >= 500` do not cover the whole variant.
			t1, _, ok1 := v.collectRuleIsCases(c.Left)
			if ok1 {
				return t1, nil, true
			}
		}
	case *TypeCheckExpr:
		if !c.Negated && (c.TypeName == "text" || c.TypeName == "number") {
			if id, ok := c.Target.(*IdentExpr); ok {
				return id.Name, []resolvedUnionVariant{{kind: variantBaseType, baseType: c.TypeName}}, true
			}
		}
	}
	return "", nil, false
}

func (v *typeValidator) fieldUnionVariants(recName, fieldName string) ([]resolvedUnionVariant, string, bool) {
	d, ok := v.defs[recName]
	if !ok {
		return nil, "", false
	}
	for _, f := range d.Fields {
		if f.Name != fieldName {
			continue
		}
		if len(f.Allowed) > 0 {
			tmpUnion := &UnionDecl{
				posInfo:  posInfo{Line: f.Line, Col: f.Col},
				Name:     fmt.Sprintf("%s.%s", recName, fieldName),
				Variants: f.Allowed,
			}
			resolving := make(map[string]bool)
			var flat []resolvedUnionVariant
			for _, opt := range f.Allowed {
				expanded, err := v.expandUnionVariantExpr(opt, tmpUnion, resolving)
				if err != nil {
					return nil, "", false
				}
				flat = append(flat, expanded...)
			}
			return flat, tmpUnion.Name, true
		}
		if id, ok := f.Default.(*IdentExpr); ok {
			if variants, isUnion := v.resolvedUnions[id.Name]; isUnion {
				return variants, fmt.Sprintf("%s.%s", recName, fieldName), true
			}
		}
	}
	return nil, "", false
}

func (v *typeValidator) checkUnionLiteralComparison(ex *BinaryExpr) error {
	rightStr, ok := ex.Right.(*StringExpr)
	if !ok {
		return nil
	}
	st := v.inferExprTypeState(ex.Left)
	var variants []resolvedUnionVariant
	var label string
	if st.inlineUnionLabel != "" && len(st.inlineUnionVariants) > 0 {
		variants = st.inlineUnionVariants
		label = st.inlineUnionLabel
	} else if st.unionName != "" {
		variants = v.resolvedUnions[st.unionName]
		label = st.unionName
	} else {
		return nil
	}
	if len(variants) == 0 {
		return nil
	}
	var allowedDisplay []string
	for _, vr := range variants {
		if vr.kind == variantBaseType && vr.baseType == "text" {
			return nil
		}
		if vr.kind == variantStringLit && vr.strVal == rightStr.Value {
			return nil
		}
		allowedDisplay = append(allowedDisplay, vr.display())
	}
	return newError(
		"TypeError",
		ex.Line,
		ex.Col,
		v.source,
		fmt.Sprintf("%q is not a value of %s (%s)", rightStr.Value, label, strings.Join(allowedDisplay, " | ")),
		fmt.Sprintf("Valid values for %s: %s.", label, strings.Join(allowedDisplay, " | ")),
	)
}

func (v *typeValidator) unionRecordNames(unionName string) []string {
	var out []string
	for _, vr := range v.resolvedUnions[unionName] {
		if vr.kind == variantRecord {
			out = append(out, vr.recordName)
		}
	}
	return out
}

func hasDefField(d *DefDecl, fieldName string) bool {
	for _, f := range d.Fields {
		if f.Name == fieldName {
			return true
		}
	}
	return false
}

func defFieldNames(d *DefDecl) string {
	names := make([]string, len(d.Fields))
	for i, f := range d.Fields {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}
