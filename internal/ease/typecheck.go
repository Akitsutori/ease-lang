package ease

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type typeDefInfo struct {
	line     int
	imported bool
	isRecord bool
}

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

	v := &typeValidator{
		source:         source,
		known:          known,
		defs:           defs,
		unions:         unions,
		consts:         consts,
		constExprs:     constExprs,
		resolvedUnions: make(map[string][]resolvedUnionVariant, len(prog.Unions)),
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
	source         string
	known          map[string]typeDefInfo
	defs           map[string]*DefDecl
	unions         map[string]*UnionDecl
	consts         map[string]int
	constExprs     map[string]Expr
	resolvedUnions map[string][]resolvedUnionVariant
	scope          *typeScope
	inRaisesExpr   bool
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
		if defaultWildcard {
			s.anyNumber = true
		} else {
			if s.numLits == nil {
				s.numLits = make(map[float64]bool)
			}
			s.numLits[e.Value] = true
		}
	case *UnaryExpr:
		if e.Op == "-" {
			if num, ok := e.Right.(*NumberExpr); ok {
				if defaultWildcard {
					s.anyNumber = true
				} else {
					if s.numLits == nil {
						s.numLits = make(map[float64]bool)
					}
					s.numLits[-num.Value] = true
				}
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
			case *ForStmt:
				collectFromStmts(st.Body)
			case *RepeatStmt:
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
	}
	return nil, false
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
		if err := v.checkExpr(st.Value); err != nil {
			return err
		}
		if err := v.checkNotCondition(st.Value); err != nil {
			return err
		}
		v.setVarInCurrentOrExisting(st.Name, v.inferExprTypeState(st.Value))
		return nil

	case *AssignStmt:
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

	case *RepeatStmt:
		if err := v.checkExpr(st.Count); err != nil {
			return err
		}
		if err := v.checkNotCondition(st.Count); err != nil {
			return err
		}
		v.pushScope()
		err := v.checkStmts(st.Body, inFunction)
		v.popScope()
		return err

	case *ForStmt:
		if err := v.checkExpr(st.Iterable); err != nil {
			return err
		}
		if err := v.checkNotCondition(st.Iterable); err != nil {
			return err
		}
		loopScope := v.pushScope()
		itemName := st.ItemName
		if itemName == "" {
			itemName = "it"
		}
		elemState := v.inferIterableElemState(st.Iterable)
		loopScope.vars[itemName] = elemState
		err := v.checkStmts(st.Body, inFunction)
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

	case *CheckRaisesStmt:
		prevRaises := v.inRaisesExpr
		v.inRaisesExpr = true
		err := v.checkExpr(st.Expression)
		v.inRaisesExpr = prevRaises
		if err != nil {
			return err
		}
		return v.checkExpr(st.Expected)
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
							"Cannot return a value from 'check' at top level; use 'say' or 'do fail' instead.",
							"Top-level 'check' rules can run statements like 'say \"...\"', assignments, or 'do fail \"...\"'.",
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

func (v *typeValidator) inferExprTypeState(e Expr) varTypeState {
	switch ex := e.(type) {
	case *CastExpr:
		if v.unions[ex.TypeName] != nil {
			return varTypeState{unionName: ex.TypeName}
		}
		if v.defs[ex.TypeName] != nil {
			return varTypeState{exactRecord: ex.TypeName}
		}
		if ex.TypeName == "text" || ex.TypeName == "number" {
			return varTypeState{exactNonRecord: true}
		}
	case *TypedListExpr:
		return varTypeState{listElemType: ex.ElemType}
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
	case *FieldExpr:
		targetSt := v.inferExprTypeState(ex.Target)
		if targetSt.exactRecord != "" {
			if variants, label, ok := v.fieldUnionVariants(targetSt.exactRecord, ex.Field); ok {
				return varTypeState{
					inlineUnionLabel:    label,
					inlineUnionVariants: variants,
				}
			}
		}
	case *StringExpr, *InterpStringExpr, *NumberExpr, *UnaryExpr:
		return varTypeState{exactNonRecord: true}
	case *CallExpr:
		if ex.Callee == "push" && len(ex.Args) >= 1 {
			argSt := v.inferExprTypeState(ex.Args[0])
			if argSt.listElemType != "" {
				return varTypeState{listElemType: argSt.listElemType}
			}
			if argSt.unionName != "" {
				return varTypeState{listElemType: argSt.unionName}
			}
			if argSt.exactRecord != "" {
				return varTypeState{listElemType: argSt.exactRecord}
			}
		} else if ex.Callee == "range" {
			return varTypeState{listElemType: "number"}
		}
	}
	return varTypeState{}
}

func (v *typeValidator) inferIterableElemState(iter Expr) varTypeState {
	elemType := ""
	switch ex := iter.(type) {
	case *TypedListExpr:
		elemType = ex.ElemType
	case *CastExpr:
		elemType = ex.TypeName
	case *IdentExpr:
		if st, ok := v.lookupVar(ex.Name); ok {
			if st.listElemType != "" {
				elemType = st.listElemType
			} else if st.unionName != "" {
				elemType = st.unionName
			} else if st.exactRecord != "" {
				elemType = st.exactRecord
			}
		}
	}
	if elemType != "" {
		if v.unions[elemType] != nil {
			return varTypeState{unionName: elemType}
		}
		if v.defs[elemType] != nil {
			return varTypeState{exactRecord: elemType}
		}
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

	case *AskExpr:
		if err := v.checkExpr(ex.Prompt); err != nil {
			return err
		}
		return v.checkNotCondition(ex.Prompt)

	case *UnaryExpr:
		if err := v.checkExpr(ex.Right); err != nil {
			return err
		}
		if ex.Op != "not" {
			return v.checkNotCondition(ex.Right)
		}
		return nil

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
		if (ex.Op == "is" || ex.Op == "is not") && !v.inRaisesExpr {
			return v.checkUnionLiteralComparison(ex)
		}
		return nil

	case *TypeCheckExpr:
		return v.checkExpr(ex.Target)

	case *FieldExpr:
		if err := v.checkExpr(ex.Target); err != nil {
			return err
		}
		if v.inRaisesExpr {
			return nil
		}
		if id, ok := ex.Target.(*IdentExpr); ok {
			if st, tracked := v.lookupVar(id.Name); tracked {
				if st.exactRecord != "" {
					if defDecl, ok := v.defs[st.exactRecord]; ok && !hasDefField(defDecl, ex.Field) {
						return newError(
							"TypeError",
							ex.Line,
							ex.Col,
							v.source,
							fmt.Sprintf("%s has no field %q", st.exactRecord, ex.Field),
							fmt.Sprintf("Available fields on %s: %s.", st.exactRecord, defFieldNames(defDecl)),
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
		return v.checkExpr(ex.Index)

	case *WithExpr:
		if err := v.checkExpr(ex.Base); err != nil {
			return err
		}
		for _, upd := range ex.Updates {
			if err := v.checkExpr(upd.Value); err != nil {
				return err
			}
			if err := v.checkNotCondition(upd.Value); err != nil {
				return err
			}
		}

	case *CallExpr:
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
	if v.inRaisesExpr || e == nil || !isConditionExpr(e) {
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
