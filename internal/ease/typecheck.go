package ease

import (
	"fmt"
	"sort"
	"strings"
)

type typeDefInfo struct {
	line     int
	imported bool
	isRecord bool
}

// validateProgramTypes verifies that every `as <type>` cast and `list as <type>`
// expression targets either a built-in Ease type or a previously defined / imported `def` blueprint.
func validateProgramTypes(prog *Program, source string) error {
	known := make(map[string]typeDefInfo, len(prog.Defs)+len(prog.Unions))
	for _, d := range prog.Defs {
		known[d.Name] = typeDefInfo{
			line:     d.Line,
			imported: d.Imported,
			isRecord: true,
		}
	}
	for _, u := range prog.Unions {
		known[u.Name] = typeDefInfo{
			line:     u.Line,
			imported: u.Imported,
			isRecord: false,
		}
	}

	consts := make(map[string]int)
	for _, stmt := range prog.Body {
		if ls, ok := stmt.(*LetStmt); ok {
			consts[ls.Name] = ls.Line
		}
	}

	v := &typeValidator{
		source: source,
		known:  known,
		consts: consts,
	}

	for _, d := range prog.Defs {
		if d.Imported {
			continue
		}
		for _, f := range d.Fields {
			if err := v.checkExpr(f.Default); err != nil {
				return err
			}
			for _, opt := range f.Allowed {
				if err := v.checkExpr(opt); err != nil {
					return err
				}
			}
		}
	}
	for _, u := range prog.Unions {
		if u.Imported {
			continue
		}
		for _, variant := range u.Variants {
			if err := v.checkExpr(variant); err != nil {
				return err
			}
		}
	}
	for _, fn := range prog.Functions {
		if err := v.checkStmts(fn.Body); err != nil {
			return err
		}
	}
	for _, chk := range prog.Checks {
		if err := v.checkStmts(chk.Body); err != nil {
			return err
		}
	}
	return v.checkStmts(prog.Body)
}

type typeValidator struct {
	source string
	known  map[string]typeDefInfo
	consts map[string]int
}

func (v *typeValidator) checkStmts(stmts []Stmt) error {
	for _, s := range stmts {
		if err := v.checkStmt(s); err != nil {
			return err
		}
	}
	return nil
}

func (v *typeValidator) checkStmt(s Stmt) error {
	switch st := s.(type) {
	case *LetStmt:
		return v.checkExpr(st.Value)
	case *AssignStmt:
		return v.checkExpr(st.Value)
	case *SayStmt:
		return v.checkExpr(st.Value)
	case *ExprStmt:
		return v.checkExpr(st.Expression)
	case *IfStmt:
		if err := v.checkExpr(st.Condition); err != nil {
			return err
		}
		if err := v.checkStmts(st.ThenBody); err != nil {
			return err
		}
		return v.checkStmts(st.ElseBody)
	case *RepeatStmt:
		if err := v.checkExpr(st.Count); err != nil {
			return err
		}
		return v.checkStmts(st.Body)
	case *ForStmt:
		if err := v.checkExpr(st.Iterable); err != nil {
			return err
		}
		return v.checkStmts(st.Body)
	case *CheckGuardStmt:
		for _, r := range st.Rules {
			if err := v.checkExpr(r.Cond); err != nil {
				return err
			}
			if r.Action != nil {
				if err := v.checkStmt(r.Action); err != nil {
					return err
				}
			}
		}
		return v.checkStmts(st.ElseBody)
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
		if err := v.checkExpr(st.Expression); err != nil {
			return err
		}
		return v.checkExpr(st.Expected)
	}
	return nil
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
		return v.validateTypeTarget(ex.TypeName, ex.TypeLine, ex.TypeCol, "as", false)
	case *TypedListExpr:
		for _, row := range ex.Rows {
			for _, upd := range row {
				if err := v.checkExpr(upd.Value); err != nil {
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
		}
	case *InterpStringExpr:
		for _, p := range ex.Parts {
			if p.IsExpr {
				if err := v.checkExpr(p.Expr); err != nil {
					return err
				}
			}
		}
	case *AskExpr:
		return v.checkExpr(ex.Prompt)
	case *UnaryExpr:
		return v.checkExpr(ex.Right)
	case *BinaryExpr:
		if err := v.checkExpr(ex.Left); err != nil {
			return err
		}
		return v.checkExpr(ex.Right)
	case *TypeCheckExpr:
		return v.checkExpr(ex.Target)
	case *FieldExpr:
		return v.checkExpr(ex.Target)
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
		}
	case *CallExpr:
		for _, arg := range ex.Args {
			if err := v.checkExpr(arg); err != nil {
				return err
			}
		}
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
