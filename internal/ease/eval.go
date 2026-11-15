package ease

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Value represents any runtime value in Ease (float64, string, bool, []any, or nil).
type Value = any

// RecordValue represents an Ease `def` prototype or instance created with `with`.
type RecordValue struct {
	TypeName    string
	Fields      map[string]Value
	Allowed     map[string][]Value
	Order       []string
	IsPrototype bool
}

// UnionValue represents a union value-space defined with `def <name> = v1 | v2 | ...`.
type UnionValue struct {
	TypeName string
	Values   []Value
}

// ListValue represents a typed homogeneous list in Ease.
type ListValue struct {
	ElemType string
	Elements []Value
}

func asListElements(v Value) ([]Value, string, bool) {
	switch lv := v.(type) {
	case []Value:
		return lv, "", true
	case *ListValue:
		return lv.Elements, lv.ElemType, true
	default:
		return nil, "", false
	}
}

// FormatValue returns a clean, beginner-friendly display string for an Ease value.
func FormatValue(v Value) string {
	switch val := v.(type) {
	case nil:
		return "none"
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return fmt.Sprintf("%v", val)
		}
		if val == math.Trunc(val) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case string:
		return val
	case []Value:
		parts := make([]string, len(val))
		for i, el := range val {
			if s, ok := el.(string); ok {
				parts[i] = strconv.Quote(s)
			} else {
				parts[i] = FormatValue(el)
			}
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *ListValue:
		return FormatValue(val.Elements)
	case *RecordValue:
		parts := make([]string, 0, len(val.Order))
		for _, k := range val.Order {
			fv := val.Fields[k]
			if s, ok := fv.(string); ok {
				parts = append(parts, fmt.Sprintf("%s: %s", k, strconv.Quote(s)))
			} else {
				parts = append(parts, fmt.Sprintf("%s: %s", k, FormatValue(fv)))
			}
		}
		return fmt.Sprintf("%s(%s)", val.TypeName, strings.Join(parts, ", "))
	case *UnionValue:
		parts := make([]string, len(val.Values))
		for i, v := range val.Values {
			if s, ok := v.(string); ok {
				parts[i] = strconv.Quote(s)
			} else {
				parts[i] = FormatValue(v)
			}
		}
		return fmt.Sprintf("%s(%s)", val.TypeName, strings.Join(parts, " | "))
	default:
		return fmt.Sprintf("%v", val)
	}
}

// TypeName returns the human-readable Ease type name of `v`.
func TypeName(v Value) string {
	switch val := v.(type) {
	case nil:
		return "none"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []Value, *ListValue:
		return "list"
	case *RecordValue:
		return val.TypeName
	case *UnionValue:
		return val.TypeName
	default:
		return "unknown"
	}
}

func matchesEaseType(v Value, typeName string) bool {
	switch typeName {
	case "text", "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "bool":
		_, ok := v.(bool)
		return ok
	case "list":
		_, _, ok := asListElements(v)
		return ok
	default:
		if rec, ok := v.(*RecordValue); ok {
			return rec.TypeName == typeName
		}
		return false
	}
}

// ValuesEqual performs structural equality on Ease values.
func ValuesEqual(a, b Value) bool {
	if a == nil && b == nil {
		return true
	}
	if ua, ok := a.(*UnionValue); ok {
		if ub, ok2 := b.(*UnionValue); ok2 {
			return ua.TypeName == ub.TypeName
		}
		for _, v := range ua.Values {
			if ValuesEqual(v, b) {
				return true
			}
		}
		return false
	}
	if ub, ok := b.(*UnionValue); ok {
		for _, v := range ub.Values {
			if ValuesEqual(a, v) {
				return true
			}
		}
		return false
	}
	switch va := a.(type) {
	case bool:
		vb, ok := b.(bool)
		return ok && va == vb
	case float64:
		vb, ok := b.(float64)
		return ok && va == vb
	case string:
		vb, ok := b.(string)
		return ok && va == vb
	case []Value:
		vb, _, ok := asListElements(b)
		if !ok || len(va) != len(vb) {
			return false
		}
		for i := range va {
			if !ValuesEqual(va[i], vb[i]) {
				return false
			}
		}
		return true
	case *ListValue:
		vb, _, ok := asListElements(b)
		if !ok || len(va.Elements) != len(vb) {
			return false
		}
		for i := range va.Elements {
			if !ValuesEqual(va.Elements[i], vb[i]) {
				return false
			}
		}
		return true
	case *RecordValue:
		vb, ok := b.(*RecordValue)
		if !ok || va.TypeName != vb.TypeName {
			return false
		}
		if va.IsPrototype || vb.IsPrototype {
			return true
		}
		if len(va.Fields) != len(vb.Fields) {
			return false
		}
		for k, v := range va.Fields {
			ov, exists := vb.Fields[k]
			if !exists || !ValuesEqual(v, ov) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

type binding struct {
	val     Value
	mutable bool
}

// Env implements lexical scopes with mutability tracking (immutable `def` constants vs mutable bindings).
type Env struct {
	parent *Env
	vars   map[string]binding
}

func NewEnv(parent *Env) *Env {
	return &Env{parent: parent, vars: make(map[string]binding)}
}

func (e *Env) Define(name string, val Value, mutable bool) {
	e.vars[name] = binding{val: val, mutable: mutable}
}

func (e *Env) Lookup(name string) (Value, bool) {
	for cur := e; cur != nil; cur = cur.parent {
		if b, ok := cur.vars[name]; ok {
			return b.val, true
		}
	}
	return nil, false
}

func (e *Env) DefineOrAssign(name string, val Value, line, col int, source string) error {
	for cur := e; cur != nil; cur = cur.parent {
		if b, ok := cur.vars[name]; ok {
			if !b.mutable {
				return newError(
					"RuntimeError",
					line,
					col,
					source,
					fmt.Sprintf("Cannot reassign constant %q defined with 'def'", name),
					fmt.Sprintf("Use a local variable '%s = ...' instead of 'def %s = ...' if it needs to change.", name, name),
				)
			}
			cur.vars[name] = binding{val: val, mutable: true}
			return nil
		}
	}
	e.vars[name] = binding{val: val, mutable: true}
	return nil
}

type reportSignal struct {
	val Value
}

const maxCallDepth = 256

// Evaluator executes parsed Ease programs.
type Evaluator struct {
	source           string
	out              io.Writer
	trace            io.Writer
	inReader         *bufio.Reader
	funcs            map[string]*FuncDecl
	global           *Env
	callDepth        int
	initErr          error
	headlessScreen   bool
	screenFrameCount int
	activeScreen     *screenSession
}

// SetHeadlessScreen toggles headless screen simulation for `--check` / CI tests.
func (ev *Evaluator) SetHeadlessScreen(headless bool) {
	ev.headlessScreen = headless
}

// Close releases any active OS resources (such as an open screen window).
func (ev *Evaluator) Close() {
	if ev.activeScreen != nil {
		ev.activeScreen.close()
		ev.activeScreen = nil
	}
}

// SetTrace enables live execution trace logging to `w` (pass nil to disable).
func (ev *Evaluator) SetTrace(w io.Writer) {
	ev.trace = w
}

func (ev *Evaluator) logTrace(format string, args ...any) {
	if ev.trace != nil {
		fmt.Fprintf(ev.trace, "[trace] "+format+"\n", args...)
	}
}

// NewEvaluator creates an Evaluator for `prog` writing `say` output to `out` and reading `ask` from os.Stdin.
func NewEvaluator(source string, prog *Program, out io.Writer) *Evaluator {
	return NewEvaluatorWithInput(source, prog, os.Stdin, out)
}

// NewEvaluatorWithInput creates an Evaluator reading `ask` input from `in` and writing `say` output to `out`.
func NewEvaluatorWithInput(source string, prog *Program, in io.Reader, out io.Writer) *Evaluator {
	if out == nil {
		out = os.Stdout
	}
	if in == nil {
		in = os.Stdin
	}
	funcs := make(map[string]*FuncDecl, len(prog.Functions))
	for _, fn := range prog.Functions {
		funcs[fn.Name] = fn
	}
	ev := &Evaluator{
		source:   source,
		out:      out,
		inReader: bufio.NewReader(in),
		funcs:    funcs,
		global:   NewEnv(nil),
	}
	for _, def := range prog.Defs {
		fields := make(map[string]Value, len(def.Fields))
		allowed := make(map[string][]Value)
		order := make([]string, 0, len(def.Fields))
		for _, f := range def.Fields {
			val, err := ev.EvalExpr(f.Default, ev.global)
			if err != nil {
				ev.initErr = err
				return ev
			}
			fields[f.Name] = val
			order = append(order, f.Name)
			if len(f.Allowed) > 0 {
				opts := make([]Value, 0, len(f.Allowed))
				for _, optExpr := range f.Allowed {
					optVal, err := ev.EvalExpr(optExpr, ev.global)
					if err != nil {
						ev.initErr = err
						return ev
					}
					opts = append(opts, optVal)
				}
				allowed[f.Name] = opts
			}
		}
		ev.global.Define(def.Name, &RecordValue{
			TypeName:    def.Name,
			Fields:      fields,
			Allowed:     allowed,
			Order:       order,
			IsPrototype: true,
		}, false)
	}
	for _, u := range prog.Unions {
		vals := make([]Value, 0, len(u.Variants))
		for _, ve := range u.Variants {
			val, err := ev.EvalExpr(ve, ev.global)
			if err != nil {
				ev.initErr = err
				return ev
			}
			vals = append(vals, val)
		}
		ev.global.Define(u.Name, &UnionValue{
			TypeName: u.Name,
			Values:   vals,
		}, false)
	}
	return ev
}

// EvalProgramWithInput parses and executes `source` with custom stdin/stdout streams.
func EvalProgramWithInput(source string, in io.Reader, out io.Writer) (Value, error) {
	return EvalProgramWithTrace(source, in, out, nil)
}

// EvalProgramWithTrace parses and executes `source` with custom stdin, stdout, and optional trace output.
func EvalProgramWithTrace(source string, in io.Reader, out io.Writer, trace io.Writer) (Value, error) {
	return evalProgramWithOptions(source, in, out, trace, false)
}

// EvalProgramHeadlessWithTrace parses and executes `source` with headless screen simulation.
func EvalProgramHeadlessWithTrace(source string, in io.Reader, out io.Writer, trace io.Writer) (Value, error) {
	return evalProgramWithOptions(source, in, out, trace, true)
}

func evalProgramWithOptions(source string, in io.Reader, out io.Writer, trace io.Writer, headless bool) (Value, error) {
	prog, err := ParseProgram(source)
	if err != nil {
		return nil, err
	}
	ev := NewEvaluatorWithInput(source, prog, in, out)
	defer ev.Close()
	ev.SetHeadlessScreen(headless)
	ev.SetTrace(trace)
	ev.logTrace("parsed %d def(s), %d function(s), %d check block(s), %d top-level statement(s)",
		len(prog.Defs), len(prog.Functions), len(prog.Checks), len(prog.Body))
	return ev.ExecStatements(prog.Body, ev.global)
}

// EvalProgram parses and executes `source`, writing `say` output to `out`.
func EvalProgram(source string, out io.Writer) (Value, error) {
	prog, err := ParseProgram(source)
	if err != nil {
		return nil, err
	}
	ev := NewEvaluator(source, prog, out)
	return ev.ExecStatements(prog.Body, ev.global)
}

// ExecStatements runs a sequence of statements in `env`.
func (ev *Evaluator) ExecStatements(stmts []Stmt, env *Env) (Value, error) {
	if ev.initErr != nil {
		return nil, ev.initErr
	}
	var last Value
	for _, st := range stmts {
		val, sig, err := ev.execStmt(st, env)
		if err != nil {
			return nil, err
		}
		if sig != nil {
			line, col := st.Pos()
			return nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				"'report' can only be used inside a 'to ... end' function",
				"Move 'report' inside a function, or use 'say' to print at top level.",
			)
		}
		last = val
	}
	return last, nil
}

func (ev *Evaluator) execBlock(stmts []Stmt, env *Env) (Value, *reportSignal, error) {
	var last Value
	for _, st := range stmts {
		val, sig, err := ev.execStmt(st, env)
		if err != nil || sig != nil {
			return val, sig, err
		}
		last = val
	}
	return last, nil, nil
}

func (ev *Evaluator) execStmt(stmt Stmt, env *Env) (Value, *reportSignal, error) {
	line, _ := stmt.Pos()
	switch s := stmt.(type) {
	case *AssignStmt:
		ev.logTrace("line %d: %s = ...", line, s.Name)
	case *SayStmt:
		ev.logTrace("line %d: say ...", line)
	case *CheckGuardStmt:
		ev.logTrace("line %d: check guard (%d rule(s))", line, len(s.Rules))
	case *IfStmt:
		ev.logTrace("line %d: if ...", line)
	case *ForStmt:
		ev.logTrace("line %d: for %s in ...", line, s.ItemName)
	case *RepeatStmt:
		ev.logTrace("line %d: repeat ...", line)
	}
	switch s := stmt.(type) {
	case *LetStmt:
		val, err := ev.EvalExpr(s.Value, env)
		if err != nil {
			return nil, nil, err
		}
		env.Define(s.Name, val, false)
		return val, nil, nil

	case *AssignStmt:
		val, err := ev.EvalExpr(s.Value, env)
		if err != nil {
			return nil, nil, err
		}
		if err := env.DefineOrAssign(s.Name, val, s.Line, s.Col, ev.source); err != nil {
			return nil, nil, err
		}
		return val, nil, nil

	case *SayStmt:
		val, err := ev.EvalExpr(s.Value, env)
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintln(ev.out, FormatValue(val))
		return val, nil, nil

	case *IfStmt:
		condVal, err := ev.EvalExpr(s.Condition, env)
		if err != nil {
			return nil, nil, err
		}
		cond, ok := condVal.(bool)
		if !ok {
			line, col := s.Condition.Pos()
			return nil, nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'if' condition must be a bool, got %s (%s)", TypeName(condVal), FormatValue(condVal)),
				"Use comparisons like 'x > 0', 'name == \"Ada\"', or boolean values 'true'/'false'.",
			)
		}
		branchEnv := NewEnv(env)
		if cond {
			return ev.execBlock(s.ThenBody, branchEnv)
		}
		if len(s.ElseBody) > 0 {
			return ev.execBlock(s.ElseBody, branchEnv)
		}
		return nil, nil, nil

	case *RepeatStmt:
		countVal, err := ev.EvalExpr(s.Count, env)
		if err != nil {
			return nil, nil, err
		}
		nFloat, ok := countVal.(float64)
		if !ok || nFloat < 0 || nFloat != math.Trunc(nFloat) {
			line, col := s.Count.Pos()
			return nil, nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'repeat' count must be a non-negative integer, got %s", FormatValue(countVal)),
				"Example: repeat 5 ... end",
			)
		}
		var last Value
		for range int(nFloat) {
			loopEnv := NewEnv(env)
			val, sig, err := ev.execBlock(s.Body, loopEnv)
			if err != nil || sig != nil {
				return val, sig, err
			}
			last = val
		}
		return last, nil, nil

	case *ForStmt:
		iterVal, err := ev.EvalExpr(s.Iterable, env)
		if err != nil {
			return nil, nil, err
		}
		items, _, ok := asListElements(iterVal)
		if !ok {
			line, col := s.Iterable.Pos()
			return nil, nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'for' requires a list, got %s", TypeName(iterVal)),
				"Iterate over a list like 'list 1, 2, 3' or 'range(0, 5)'.",
			)
		}
		var last Value
		for _, item := range items {
			loopEnv := NewEnv(env)
			if s.ItemName != "" {
				loopEnv.Define(s.ItemName, item, false)
			} else if rec, isRec := item.(*RecordValue); isRec {
				for k, v := range rec.Fields {
					loopEnv.Define(k, v, false)
				}
				loopEnv.Define("it", item, false)
			} else {
				loopEnv.Define("it", item, false)
			}
			val, sig, err := ev.execBlock(s.Body, loopEnv)
			if err != nil || sig != nil {
				return val, sig, err
			}
			last = val
		}
		return last, nil, nil

	case *ExprStmt:
		val, err := ev.EvalExpr(s.Expression, env)
		return val, nil, err

	case *CheckGuardStmt:
		for _, rule := range s.Rules {
			condVal, err := ev.EvalExpr(rule.Cond, env)
			if err != nil {
				return nil, nil, err
			}
			condBool, ok := condVal.(bool)
			if !ok {
				return nil, nil, newError("RuntimeError", rule.Line, rule.Col, ev.source, fmt.Sprintf("Check condition must be a bool, got %s", TypeName(condVal)), "")
			}
			if rule.Action != nil {
				if condBool {
					val, sig, err := ev.execStmt(rule.Action, env)
					if err != nil || sig != nil {
						return val, sig, err
					}
					if _, isExpr := rule.Action.(*ExprStmt); isExpr && ev.callDepth > 0 {
						return val, &reportSignal{val: val}, nil
					}
					return val, nil, nil
				}
				continue
			}
			if !condBool {
				if len(s.ElseBody) > 0 {
					val, sig, err := ev.execBlock(s.ElseBody, NewEnv(env))
					if err != nil || sig != nil {
						return val, sig, err
					}
					lastStmt := s.ElseBody[len(s.ElseBody)-1]
					if _, isExpr := lastStmt.(*ExprStmt); isExpr && ev.callDepth > 0 {
						return val, &reportSignal{val: val}, nil
					}
					return val, nil, nil
				}
				return nil, nil, newError("RuntimeError", rule.Line, rule.Col, ev.source, "Check condition failed", "Add an 'else' block or '-> action' to handle this case.")
			}
		}
		return nil, nil, nil

	default:
		line, col := stmt.Pos()
		return nil, nil, newError("RuntimeError", line, col, ev.source, "Unsupported statement", "")
	}
}

// EvalExpr evaluates a single expression node inside `env`.
func (ev *Evaluator) EvalExpr(expr Expr, env *Env) (Value, error) {
	switch e := expr.(type) {
	case *NumberExpr:
		return e.Value, nil
	case *StringExpr:
		return e.Value, nil
	case *InterpStringExpr:
		var b strings.Builder
		for _, part := range e.Parts {
			if !part.IsExpr {
				b.WriteString(part.Text)
				continue
			}
			val, err := ev.EvalExpr(part.Expr, env)
			if err != nil {
				return nil, err
			}
			b.WriteString(FormatValue(val))
		}
		return b.String(), nil
	case *BoolExpr:
		return e.Value, nil
	case *AskExpr:
		if e.Prompt != nil {
			promptVal, err := ev.EvalExpr(e.Prompt, env)
			if err != nil {
				return nil, err
			}
			promptText := FormatValue(promptVal)
			if promptText != "" && !strings.HasSuffix(promptText, " ") {
				promptText += " "
			}
			fmt.Fprint(ev.out, promptText)
		}
		ev.logTrace("line %d: ask (waiting for stdin...)", e.Line)
		line, err := ev.inReader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, newError("RuntimeError", e.Line, e.Col, ev.source, "Failed to read input: "+err.Error(), "")
		}
		res := strings.TrimRight(line, "\r\n")
		ev.logTrace("line %d: ask received %q", e.Line, res)
		return res, nil
	case *IdentExpr:
		val, ok := env.Lookup(e.Name)
		if !ok {
			return nil, newError(
				"RuntimeError",
				e.Line,
				e.Col,
				ev.source,
				fmt.Sprintf("Unknown variable %q", e.Name),
				fmt.Sprintf("Define %q first with '%s = ...' or 'def %s = ...'.", e.Name, e.Name, e.Name),
			)
		}
		return val, nil

	case *ListExpr:
		items := make([]Value, len(e.Elements))
		for i, el := range e.Elements {
			v, err := ev.EvalExpr(el, env)
			if err != nil {
				return nil, err
			}
			items[i] = v
		}
		if e.Homogeneous && len(items) > 0 {
			firstType := TypeName(items[0])
			for i := 1; i < len(items); i++ {
				if TypeName(items[i]) != firstType {
					line, col := e.Elements[i].Pos()
					return nil, newError(
						"RuntimeError",
						line,
						col,
						ev.source,
						fmt.Sprintf("Mixed types in 'list': expected %s, got %s", firstType, TypeName(items[i])),
						"All elements in a 'list' must have the same type. Use 'def' records for structured data.",
					)
				}
			}
			return &ListValue{ElemType: firstType, Elements: items}, nil
		}
		return items, nil

	case *TypedListExpr:
		if len(e.Rows) == 0 {
			if e.ElemType != "number" && e.ElemType != "text" && e.ElemType != "string" {
				protoVal, ok := ev.global.Lookup(e.ElemType)
				if !ok {
					return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("Unknown type %q in 'list as %s'", e.ElemType, e.ElemType), "Define a record with 'def' or use 'number' / 'text'.")
				}
				if _, isRec := protoVal.(*RecordValue); !isRec {
					return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("%q is not a 'def' record type", e.ElemType), "Use 'list as <def_name>' or 'list as number' / 'list as text'.")
				}
			}
			return &ListValue{ElemType: e.ElemType}, nil
		}
		protoVal, ok := ev.global.Lookup(e.ElemType)
		if !ok {
			return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("Unknown record type %q in 'list as %s with'", e.ElemType, e.ElemType), "Define it first with 'def "+e.ElemType+" ... end'.")
		}
		rec, isRec := protoVal.(*RecordValue)
		if !isRec {
			return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("%q is not a 'def' record", e.ElemType), "Only 'def' records can be used with 'list as ... with'.")
		}
		out := make([]Value, 0, len(e.Rows))
		for _, row := range e.Rows {
			inst, err := ev.cloneRecordWithUpdates(rec, row, env)
			if err != nil {
				return nil, err
			}
			out = append(out, inst)
		}
		return &ListValue{ElemType: e.ElemType, Elements: out}, nil

	case *UnaryExpr:
		right, err := ev.EvalExpr(e.Right, env)
		if err != nil {
			return nil, err
		}
		switch e.Op {
		case "-":
			num, ok := right.(float64)
			if !ok {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("Cannot negate %s", TypeName(right)), "Unary '-' only works on numbers.")
			}
			return -num, nil
		case "not":
			b, ok := right.(bool)
			if !ok {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("'not' requires a bool, got %s", TypeName(right)), "Example: not (x == 0)")
			}
			return !b, nil
		}

	case *BinaryExpr:
		if e.Op == "and" {
			lv, err := ev.EvalExpr(e.Left, env)
			if err != nil {
				return nil, err
			}
			lb, ok := lv.(bool)
			if !ok {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("'and' requires bool values, got %s", TypeName(lv)), "")
			}
			if !lb {
				return false, nil
			}
			rv, err := ev.EvalExpr(e.Right, env)
			if err != nil {
				return nil, err
			}
			rb, ok := rv.(bool)
			if !ok {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("'and' requires bool values, got %s", TypeName(rv)), "")
			}
			return rb, nil
		}
		if e.Op == "or" {
			lv, err := ev.EvalExpr(e.Left, env)
			if err != nil {
				return nil, err
			}
			lb, ok := lv.(bool)
			if !ok {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("'or' requires bool values, got %s", TypeName(lv)), "")
			}
			if lb {
				return true, nil
			}
			rv, err := ev.EvalExpr(e.Right, env)
			if err != nil {
				return nil, err
			}
			rb, ok := rv.(bool)
			if !ok {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("'or' requires bool values, got %s", TypeName(rv)), "")
			}
			return rb, nil
		}

		left, err := ev.EvalExpr(e.Left, env)
		if err != nil {
			return nil, err
		}
		right, err := ev.EvalExpr(e.Right, env)
		if err != nil {
			return nil, err
		}
		return ev.evalBinaryOp(e.Op, left, right, e.Line, e.Col)

	case *IndexExpr:
		targetVal, err := ev.EvalExpr(e.Target, env)
		if err != nil {
			return nil, err
		}
		idxVal, err := ev.EvalExpr(e.Index, env)
		if err != nil {
			return nil, err
		}
		idxFloat, ok := idxVal.(float64)
		if !ok || idxFloat != math.Trunc(idxFloat) {
			return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("Index must be an integer, got %s", FormatValue(idxVal)), "Use integer indices starting at 0, e.g. xs.0.")
		}
		idx := int(idxFloat)
		switch container := targetVal.(type) {
		case []Value:
			if idx < 0 || idx >= len(container) {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("List index %d out of range (length %d)", idx, len(container)), "Valid indices are 0 to len(list) - 1.")
			}
			return container[idx], nil
		case *ListValue:
			if idx < 0 || idx >= len(container.Elements) {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("List index %d out of range (length %d)", idx, len(container.Elements)), "Valid indices are 0 to len(list) - 1.")
			}
			return container.Elements[idx], nil
		case string:
			runes := []rune(container)
			if idx < 0 || idx >= len(runes) {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("String index %d out of range (length %d)", idx, len(runes)), "")
			}
			return string(runes[idx]), nil
		default:
			return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("Cannot index into %s", TypeName(targetVal)), "Only lists and strings support '.<index>' access.")
		}

	case *TypeCheckExpr:
		targetVal, err := ev.EvalExpr(e.Target, env)
		if err != nil {
			return nil, err
		}
		matched := matchesEaseType(targetVal, e.TypeName)
		if e.Negated {
			return !matched, nil
		}
		return matched, nil

	case *CastExpr:
		targetVal, err := ev.EvalExpr(e.Target, env)
		if err != nil {
			return nil, err
		}
		switch e.TypeName {
		case "text", "string":
			if rec, ok := targetVal.(*RecordValue); ok {
				b, err := json.Marshal(easeValueToJSON(rec))
				if err != nil {
					return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("Cannot serialize %s to JSON: %v", rec.TypeName, err), "")
				}
				return string(b), nil
			}
			return FormatValue(targetVal), nil
		case "number":
			switch v := targetVal.(type) {
			case float64:
				return v, nil
			case string:
				n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if err != nil {
					// Keep original non-numeric value so `check x is not number` can handle it cleanly.
					return targetVal, nil
				}
				return n, nil
			case bool:
				if v {
					return float64(1), nil
				}
				return float64(0), nil
			default:
				return targetVal, nil
			}
		case "bool":
			switch v := targetVal.(type) {
			case bool:
				return v, nil
			case string:
				low := strings.ToLower(strings.TrimSpace(v))
				if low == "true" {
					return true, nil
				}
				if low == "false" {
					return false, nil
				}
				return targetVal, nil
			case float64:
				return v != 0, nil
			default:
				return targetVal, nil
			}
		default:
			if protoVal, ok := env.Lookup(e.TypeName); ok {
				if proto, isRec := protoVal.(*RecordValue); isRec && proto.IsPrototype {
					if rec, already := targetVal.(*RecordValue); already && rec.TypeName == e.TypeName {
						return rec, nil
					}
					rawStr, isStr := targetVal.(string)
					if !isStr || strings.TrimSpace(rawStr) == "" {
						return targetVal, nil
					}
					var rawMap map[string]any
					if err := json.Unmarshal([]byte(rawStr), &rawMap); err != nil || rawMap == nil {
						return targetVal, nil
					}
					mapped, ok := mapJSONToRecord(rawMap, proto, env)
					if !ok {
						return targetVal, nil
					}
					return mapped, nil
				}
			}
			return targetVal, nil
		}

	case *FieldExpr:
		targetVal, err := ev.EvalExpr(e.Target, env)
		if err != nil {
			return nil, err
		}
		rec, ok := targetVal.(*RecordValue)
		if !ok {
			return nil, newError(
				"RuntimeError",
				e.Line,
				e.Col,
				ev.source,
				fmt.Sprintf("Cannot read field '.%s' on %s", e.Field, TypeName(targetVal)),
				"Field access '.name' only works on 'def' records.",
			)
		}
		val, exists := rec.Fields[e.Field]
		if !exists {
			return nil, newError(
				"RuntimeError",
				e.Line,
				e.Col,
				ev.source,
				fmt.Sprintf("%s has no field %q", rec.TypeName, e.Field),
				fmt.Sprintf("Available fields: %s", strings.Join(rec.Order, ", ")),
			)
		}
		return val, nil

	case *WithExpr:
		baseVal, err := ev.EvalExpr(e.Base, env)
		if err != nil {
			return nil, err
		}
		rec, ok := baseVal.(*RecordValue)
		if !ok {
			return nil, newError(
				"RuntimeError",
				e.Line,
				e.Col,
				ev.source,
				fmt.Sprintf("Cannot use 'with' on %s", TypeName(baseVal)),
				"'with' clones a 'def' prototype or record instance, e.g.: player with x = 10",
			)
		}
		return ev.cloneRecordWithUpdates(rec, e.Updates, env)

	case *CallExpr:
		args := make([]Value, len(e.Args))
		for i, a := range e.Args {
			v, err := ev.EvalExpr(a, env)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		return ev.callFunction(e.Callee, args, e.Line, e.Col)
	}

	line, col := expr.Pos()
	return nil, newError("RuntimeError", line, col, ev.source, "Unsupported expression", "")
}

func (ev *Evaluator) cloneRecordWithUpdates(rec *RecordValue, updates []WithFieldUpdate, env *Env) (*RecordValue, error) {
	cloned := make(map[string]Value, len(rec.Fields))
	for k, v := range rec.Fields {
		cloned[k] = v
	}
	for _, upd := range updates {
		if _, exists := cloned[upd.Name]; !exists {
			return nil, newError(
				"RuntimeError",
				upd.Line,
				upd.Col,
				ev.source,
				fmt.Sprintf("%s has no field %q", rec.TypeName, upd.Name),
				fmt.Sprintf("Available fields: %s", strings.Join(rec.Order, ", ")),
			)
		}
		newVal, err := ev.EvalExpr(upd.Value, env)
		if err != nil {
			return nil, err
		}
		if opts, constrained := rec.Allowed[upd.Name]; constrained {
			matched := false
			optStrs := make([]string, len(opts))
			for i, opt := range opts {
				if s, ok := opt.(string); ok {
					optStrs[i] = strconv.Quote(s)
				} else {
					optStrs[i] = FormatValue(opt)
				}
				if ValuesEqual(newVal, opt) {
					matched = true
				}
			}
			if !matched {
				return nil, newError(
					"RuntimeError",
					upd.Line,
					upd.Col,
					ev.source,
					fmt.Sprintf("Invalid value %s for %s.%s", FormatValue(newVal), rec.TypeName, upd.Name),
					fmt.Sprintf("Allowed values: %s", strings.Join(optStrs, " | ")),
				)
			}
		}
		cloned[upd.Name] = newVal
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

func (ev *Evaluator) evalBinaryOp(op string, left, right Value, line, col int) (Value, error) {
	switch op {
	case "==", "is":
		return ValuesEqual(left, right), nil
	case "!=", "is-not":
		return !ValuesEqual(left, right), nil
	case "+":
		if _, lok := left.(string); lok {
			return FormatValue(left) + FormatValue(right), nil
		}
		if _, rok := right.(string); rok {
			return FormatValue(left) + FormatValue(right), nil
		}
		if la, ok := left.([]Value); ok {
			if rb, ok2 := right.([]Value); ok2 {
				merged := make([]Value, 0, len(la)+len(rb))
				merged = append(merged, la...)
				merged = append(merged, rb...)
				return merged, nil
			}
		}
		ln, lok := left.(float64)
		rn, rok := right.(float64)
		if lok && rok {
			return ln + rn, nil
		}
		return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Cannot add %s and %s", TypeName(left), TypeName(right)), "Use numbers, strings, or two lists with '+'.")

	case "-", "*", "/", "%", "<", "<=", ">", ">=":
		ln, lok := left.(float64)
		rn, rok := right.(float64)
		if !lok || !rok {
			return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Operator %q requires numbers, got %s and %s", op, TypeName(left), TypeName(right)), "Convert strings to numbers with num(...) if needed.")
		}
		switch op {
		case "-":
			return ln - rn, nil
		case "*":
			return ln * rn, nil
		case "/":
			if rn == 0 {
				return nil, newError("RuntimeError", line, col, ev.source, "Division by zero", "Check that the divisor is not 0 before dividing.")
			}
			return ln / rn, nil
		case "%":
			if rn == 0 {
				return nil, newError("RuntimeError", line, col, ev.source, "Modulo by zero", "The right-hand divisor of '%' cannot be 0.")
			}
			return float64(int64(ln) % int64(rn)), nil
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

func (ev *Evaluator) callFunction(name string, args []Value, line, col int) (Value, error) {
	if val, handled, err := ev.callBuiltin(name, args, line, col); handled {
		return val, err
	}

	fn, ok := ev.funcs[name]
	if !ok {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Unknown function %q", name),
			fmt.Sprintf("Define it with 'to %s [...] ... end' or check the spelling.", name),
		)
	}
	if len(args) != len(fn.Params) {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Function %q expects %d argument(s), got %d", name, len(fn.Params), len(args)),
			fmt.Sprintf("Signature: to %s [%s]", name, strings.Join(fn.Params, " ")),
		)
	}
	if ev.callDepth >= maxCallDepth {
		return nil, newError("RuntimeError", line, col, ev.source, "Maximum recursion depth exceeded", "Add a base case to stop recursion.")
	}

	ev.callDepth++
	defer func() { ev.callDepth-- }()

	fnEnv := NewEnv(ev.global)
	for i, param := range fn.Params {
		fnEnv.Define(param, args[i], false)
	}

	last, sig, err := ev.execBlock(fn.Body, fnEnv)
	if err != nil {
		return nil, err
	}
	if sig != nil {
		return sig.val, nil
	}
	return last, nil
}

func (ev *Evaluator) callBuiltin(name string, args []Value, line, col int) (Value, bool, error) {
	switch name {
	case "len":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "len() takes 1 argument", "")
		}
		switch v := args[0].(type) {
		case []Value:
			return float64(len(v)), true, nil
		case *ListValue:
			return float64(len(v.Elements)), true, nil
		case string:
			return float64(len([]rune(v))), true, nil
		default:
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("len() requires a list or string, got %s", TypeName(args[0])), "")
		}

	case "str":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "str() takes 1 argument", "")
		}
		return FormatValue(args[0]), true, nil

	case "num":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "num() takes 1 argument", "")
		}
		switch v := args[0].(type) {
		case float64:
			return v, true, nil
		case string:
			n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Cannot convert %q to number", v), "")
			}
			return n, true, nil
		default:
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Cannot convert %s to number", TypeName(args[0])), "")
		}

	case "type":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "type() takes 1 argument", "")
		}
		return TypeName(args[0]), true, nil

	case "range":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "range(start, end) takes 2 arguments", "")
		}
		s, ok1 := args[0].(float64)
		e, ok2 := args[1].(float64)
		if !ok1 || !ok2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "range() requires two numbers", "")
		}
		start, end := int(s), int(e)
		out := make([]Value, 0, max(0, end-start+1))
		for i := start; i <= end; i++ {
			out = append(out, float64(i))
		}
		return out, true, nil

	case "push":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "push(list, item) takes 2 arguments", "")
		}
		xs, elemType, ok := asListElements(args[0])
		if !ok {
			return nil, true, newError("RuntimeError", line, col, ev.source, "push() expects a list as first argument", "")
		}
		if elemType != "" && !matchesEaseType(args[1], elemType) {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Cannot push %s into list of %s", TypeName(args[1]), elemType), fmt.Sprintf("This list only accepts elements of type %q.", elemType))
		}
		next := make([]Value, len(xs)+1)
		copy(next, xs)
		next[len(xs)] = args[1]
		if elemType != "" {
			return &ListValue{ElemType: elemType, Elements: next}, true, nil
		}
		return next, true, nil

	case "abs":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "abs() takes 1 argument", "")
		}
		n, ok := args[0].(float64)
		if !ok {
			return nil, true, newError("RuntimeError", line, col, ev.source, "abs() requires a number", "")
		}
		return math.Abs(n), true, nil

	case "min", "max":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("%s() takes 2 arguments", name), "")
		}
		a, ok1 := args[0].(float64)
		b, ok2 := args[1].(float64)
		if !ok1 || !ok2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("%s() requires numbers", name), "")
		}
		if name == "min" {
			return math.Min(a, b), true, nil
		}
		return math.Max(a, b), true, nil

	case "fail":
		msg := "Explicit failure"
		if len(args) > 0 {
			msg = FormatValue(args[0])
		}
		return nil, true, newError("RuntimeError", line, col, ev.source, msg, "")

	case "fields":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "fields takes 1 argument", "Example: do fields player")
		}
		rec, ok := args[0].(*RecordValue)
		if !ok {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("fields() requires a 'def' record, got %s", TypeName(args[0])), "Define a record with 'def ... end'.")
		}
		out := make([]Value, len(rec.Order))
		for i, k := range rec.Order {
			out[i] = k
		}
		return out, true, nil

	case "http_call":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "http_call takes 1 argument (http_request)", "")
		}
		reqRec, ok := args[0].(*RecordValue)
		if !ok {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("http_call requires an http_request record, got %s", TypeName(args[0])), "")
		}
		return ev.execHTTPCall(reqRec), true, nil

	case "screen_present":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "screen_present takes 2 arguments (screen, scene)", "")
		}
		winRec, ok1 := args[0].(*RecordValue)
		sceneRec, ok2 := args[1].(*RecordValue)
		if !ok1 || !ok2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "screen_present requires (screen, scene) records", "")
		}
		if ev.headlessScreen {
			ev.screenFrameCount++
			st := screenFrameState{Status: "open"}
			if ev.screenFrameCount == 1 {
				st.Key = "right"
			} else {
				st.Status = "closed"
			}
			b, _ := json.Marshal(st)
			return string(b), true, nil
		}
		if ev.activeScreen == nil {
			w := 800
			h := 600
			title := "Ease Window"
			if v, ok := winRec.Fields["width"].(float64); ok && v > 0 {
				w = int(v)
			}
			if v, ok := winRec.Fields["height"].(float64); ok && v > 0 {
				h = int(v)
			}
			if v, ok := winRec.Fields["title"].(string); ok && v != "" {
				title = v
			}
			ev.activeScreen = startScreenSession(w, h, title)
		}
		return ev.activeScreen.present(recordToScreenScene(sceneRec)), true, nil
	}

	return nil, false, nil
}

func (ev *Evaluator) newHTTPResponseRecord(status float64, body, errMsg string) *RecordValue {
	return &RecordValue{
		TypeName: "http_response",
		Fields: map[string]Value{
			"status": status,
			"body":   body,
			"error":  errMsg,
		},
		Order: []string{"status", "body", "error"},
	}
}

func (ev *Evaluator) execHTTPCall(reqRec *RecordValue) *RecordValue {
	method := "GET"
	if v, ok := reqRec.Fields["method"].(string); ok && v != "" {
		method = strings.ToUpper(v)
	}
	targetURL, _ := reqRec.Fields["url"].(string)
	bodyText, _ := reqRec.Fields["body"].(string)
	token, _ := reqRec.Fields["token"].(string)
	timeoutSec := 5.0
	if t, ok := reqRec.Fields["timeout"].(float64); ok && t > 0 {
		timeoutSec = t
	}

	var bodyReader io.Reader
	if bodyText != "" {
		bodyReader = strings.NewReader(bodyText)
	}
	httpReq, err := http.NewRequest(method, targetURL, bodyReader)
	if err != nil {
		return ev.newHTTPResponseRecord(0, "", err.Error())
	}
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	if bodyText != "" && (method == "POST" || method == "PUT") {
		trimmed := strings.TrimSpace(bodyText)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			httpReq.Header.Set("Content-Type", "application/json")
		} else {
			httpReq.Header.Set("Content-Type", "text/plain; charset=utf-8")
		}
	}

	client := &http.Client{Timeout: time.Duration(timeoutSec * float64(time.Second))}
	resp, err := client.Do(httpReq)
	if err != nil {
		return ev.newHTTPResponseRecord(0, "", err.Error())
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ev.newHTTPResponseRecord(float64(resp.StatusCode), "", err.Error())
	}
	return ev.newHTTPResponseRecord(float64(resp.StatusCode), string(rawBody), "")
}

func easeValueToJSON(v Value) any {
	switch x := v.(type) {
	case *RecordValue:
		out := make(map[string]any, len(x.Fields))
		for k, val := range x.Fields {
			out[k] = easeValueToJSON(val)
		}
		return out
	case *ListValue:
		out := make([]any, len(x.Elements))
		for i, el := range x.Elements {
			out[i] = easeValueToJSON(el)
		}
		return out
	case []Value:
		out := make([]any, len(x))
		for i, el := range x {
			out[i] = easeValueToJSON(el)
		}
		return out
	default:
		return x
	}
}

func mapJSONToRecord(rawMap map[string]any, proto *RecordValue, env *Env) (*RecordValue, bool) {
	fields := make(map[string]Value, len(proto.Fields))
	for k, v := range proto.Fields {
		fields[k] = v
	}
	order := make([]string, len(proto.Order))
	copy(order, proto.Order)

	for fieldName, defaultVal := range proto.Fields {
		jsonVal, exists := rawMap[fieldName]
		if !exists {
			continue
		}
		converted, ok := coerceJSONValue(jsonVal, defaultVal, env)
		if !ok {
			return nil, false
		}
		if opts, hasAllowed := proto.Allowed[fieldName]; hasAllowed && len(opts) > 0 {
			matched := false
			for _, opt := range opts {
				if ValuesEqual(converted, opt) {
					matched = true
					break
				}
			}
			if !matched {
				return nil, false
			}
		}
		fields[fieldName] = converted
	}

	return &RecordValue{
		TypeName:    proto.TypeName,
		Fields:      fields,
		Allowed:     proto.Allowed,
		Order:       order,
		IsPrototype: false,
	}, true
}

func coerceJSONValue(jsonVal any, template Value, env *Env) (Value, bool) {
	switch tmpl := template.(type) {
	case float64:
		n, ok := jsonVal.(float64)
		return n, ok
	case string:
		s, ok := jsonVal.(string)
		return s, ok
	case bool:
		b, ok := jsonVal.(bool)
		return b, ok
	case *RecordValue:
		subMap, ok := jsonVal.(map[string]any)
		if !ok || subMap == nil {
			return nil, false
		}
		return mapJSONToRecord(subMap, tmpl, env)
	case *ListValue:
		arr, ok := jsonVal.([]any)
		if !ok {
			return nil, false
		}
		elems := make([]Value, 0, len(arr))
		for _, item := range arr {
			if tmpl.ElemType != "" {
				switch tmpl.ElemType {
				case "number":
					n, ok := item.(float64)
					if !ok {
						return nil, false
					}
					elems = append(elems, n)
				case "text", "string":
					s, ok := item.(string)
					if !ok {
						return nil, false
					}
					elems = append(elems, s)
				case "bool":
					b, ok := item.(bool)
					if !ok {
						return nil, false
					}
					elems = append(elems, b)
				default:
					subMap, ok := item.(map[string]any)
					if !ok || subMap == nil {
						return nil, false
					}
					if protoVal, found := env.Lookup(tmpl.ElemType); found {
						if proto, isRec := protoVal.(*RecordValue); isRec && proto.IsPrototype {
							rec, ok := mapJSONToRecord(subMap, proto, env)
							if !ok {
								return nil, false
							}
							elems = append(elems, rec)
							continue
						}
					}
					return nil, false
				}
			} else {
				elems = append(elems, item)
			}
		}
		return &ListValue{ElemType: tmpl.ElemType, Elements: elems}, true
	default:
		return jsonVal, true
	}
}
