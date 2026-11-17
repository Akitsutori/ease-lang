package ease

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"slices"
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

// MapValue represents a structured key-value object produced by `do decode_json`.
type MapValue struct {
	Fields map[string]Value
	Order  []string
}

// UnionValue represents a union value-space defined with `def <name> = v1 | v2 | ...`.
type UnionValue struct {
	TypeName string
	Values   []Value
}

// BaseTypeValue represents a base type member (`text` or `number`) inside a union.
type BaseTypeValue string

// ListValue represents a typed homogeneous list in Ease.
type ListValue struct {
	ElemType string
	Elements []Value
	pushed   bool
}

func asListElements(v Value) ([]Value, string, bool) {
	if lv, ok := v.(*ListValue); ok {
		return lv.Elements, lv.ElemType, true
	}
	return nil, "", false
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
		if val == 0 {
			return "0"
		}
		if val == math.Trunc(val) {
			return strconv.FormatFloat(val, 'f', 0, 64)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case string:
		return val
	case *ListValue:
		parts := make([]string, len(val.Elements))
		for i, el := range val.Elements {
			if s, ok := el.(string); ok {
				parts[i] = strconv.Quote(s)
			} else {
				parts[i] = FormatValue(el)
			}
		}
		return "[" + strings.Join(parts, ", ") + "]"
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
	case *MapValue:
		parts := make([]string, 0, len(val.Order))
		for _, k := range val.Order {
			fv := val.Fields[k]
			if s, ok := fv.(string); ok {
				parts = append(parts, fmt.Sprintf("%s: %s", k, strconv.Quote(s)))
			} else {
				parts = append(parts, fmt.Sprintf("%s: %s", k, FormatValue(fv)))
			}
		}
		return fmt.Sprintf("map(%s)", strings.Join(parts, ", "))
	case *UnionValue:
		return fmt.Sprintf("%s(%s)", val.TypeName, strings.Join(formatUnionOptions(val.Values), " | "))
	case BaseTypeValue:
		return string(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func formatUnionOption(opt Value) string {
	switch o := opt.(type) {
	case string:
		return strconv.Quote(o)
	case BaseTypeValue:
		return string(o)
	case *RecordValue:
		if o.IsPrototype {
			return o.TypeName
		}
		return FormatValue(o)
	default:
		return FormatValue(o)
	}
}

func formatUnionOptions(opts []Value) []string {
	out := make([]string, len(opts))
	for i, opt := range opts {
		out[i] = formatUnionOption(opt)
	}
	return out
}

// TypeName returns the human-readable Ease type name of `v`.
func TypeName(v Value) string {
	switch val := v.(type) {
	case nil:
		return "none"
	case bool:
		return "condition"
	case float64:
		return "number"
	case string:
		return "text"
	case *ListValue:
		return "list"
	case *RecordValue:
		return val.TypeName
	case *MapValue:
		return "map"
	case *UnionValue:
		return val.TypeName
	case BaseTypeValue:
		return string(val)
	default:
		return "unknown"
	}
}

func isAtomicEaseValue(v Value) bool {
	switch v.(type) {
	case string, float64:
		return true
	default:
		return false
	}
}

func matchesEaseType(v Value, typeName string) bool {
	switch typeName {
	case "text":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "list":
		_, ok := v.(*ListValue)
		return ok
	default:
		if rec, ok := v.(*RecordValue); ok {
			return rec.TypeName == typeName
		}
		return false
	}
}

// ValuesEqual performs strict structural equality between concrete Ease values.
func ValuesEqual(a, b Value) bool {
	if a == nil && b == nil {
		return true
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
	case *ListValue:
		vb, ok := b.(*ListValue)
		if !ok || len(va.Elements) != len(vb.Elements) {
			return false
		}
		for i := range va.Elements {
			if !ValuesEqual(va.Elements[i], vb.Elements[i]) {
				return false
			}
		}
		return true
	case *RecordValue:
		vb, ok := b.(*RecordValue)
		if !ok || va.IsPrototype || vb.IsPrototype || va.TypeName != vb.TypeName {
			return false
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
	case *MapValue:
		vb, ok := b.(*MapValue)
		if !ok || len(va.Fields) != len(vb.Fields) {
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
	isParam bool
}

// Env implements lexical scopes with mutability tracking (immutable `def` constants/params vs mutable bindings).
type Env struct {
	parent         *Env
	vars           map[string]binding
	isFuncRoot     bool
	assignedLocals map[string]bool
}

func NewEnv(parent *Env) *Env {
	return &Env{parent: parent, vars: make(map[string]binding)}
}

func NewFuncEnv(global *Env, assignedLocals map[string]bool) *Env {
	return &Env{
		parent:         global,
		vars:           make(map[string]binding),
		isFuncRoot:     true,
		assignedLocals: assignedLocals,
	}
}

func (e *Env) Define(name string, val Value, mutable bool) {
	e.vars[name] = binding{val: val, mutable: mutable}
}

func (e *Env) DefineParam(name string, val Value) {
	e.vars[name] = binding{val: val, mutable: false, isParam: true}
}

func (e *Env) Lookup(name string) (Value, bool) {
	val, ok, _ := e.LookupChecked(name)
	return val, ok
}

func (e *Env) LookupChecked(name string) (Value, bool, bool) {
	for cur := e; cur != nil; cur = cur.parent {
		if b, ok := cur.vars[name]; ok {
			return b.val, true, false
		}
		if cur.isFuncRoot && cur.assignedLocals[name] {
			if cur.parent != nil {
				if _, isGlobal := cur.parent.Lookup(name); isGlobal {
					return nil, false, true
				}
			}
			return nil, false, false
		}
	}
	return nil, false, false
}

func (e *Env) DefineOrAssign(name string, val Value, line, col int, source string) error {
	for cur := e; cur != nil; cur = cur.parent {
		if b, ok := cur.vars[name]; ok {
			if !b.mutable {
				if b.isParam {
					return newError(
						"RuntimeError",
						line,
						col,
						source,
						fmt.Sprintf("Cannot reassign parameter %q; assign to a new local variable instead.", name),
						fmt.Sprintf("Use a new local variable like 'next_%s = ...' instead of reassigning '%s'.", name, name),
					)
				}
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
		if cur.isFuncRoot {
			break
		}
	}
	e.vars[name] = binding{val: val, mutable: true}
	return nil
}

func collectAssignedNames(stmts []Stmt) map[string]bool {
	assigned := make(map[string]bool)
	var walk func([]Stmt)
	walk = func(list []Stmt) {
		for _, stmt := range list {
			switch s := stmt.(type) {
			case *AssignStmt:
				assigned[s.Name] = true
			case *LetStmt:
				assigned[s.Name] = true
			case *RepeatStmt:
				walk(s.Body)
			case *ForStmt:
				walk(s.Body)
			case *CheckGuardStmt:
				for _, r := range s.Rules {
					if r.Action != nil {
						walk([]Stmt{r.Action})
					}
				}
				walk(s.ElseBody)
			}
		}
	}
	walk(stmts)
	return assigned
}

type reportSignal struct {
	val Value
}

const (
	maxCallDepth     = 256
	maxListSize      = 1_000_000
	maxHTTPBodyBytes = 10 * 1024 * 1024
)

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
	type declItem struct {
		imported bool
		line     int
		seq      int
		def      *DefDecl
		union    *UnionDecl
	}
	items := make([]declItem, 0, len(prog.Defs)+len(prog.Unions))
	seq := 0
	for _, d := range prog.Defs {
		items = append(items, declItem{imported: d.Imported, line: d.Line, seq: seq, def: d})
		seq++
	}
	for _, u := range prog.Unions {
		items = append(items, declItem{imported: u.Imported, line: u.Line, seq: seq, union: u})
		seq++
	}
	slices.SortFunc(items, func(a, b declItem) int {
		if a.imported != b.imported {
			if a.imported {
				return -1
			}
			return 1
		}
		if a.imported {
			return a.seq - b.seq
		}
		if a.line != b.line {
			return a.line - b.line
		}
		return a.seq - b.seq
	})

	for _, item := range items {
		if item.def != nil {
			def := item.def
			fields := make(map[string]Value, len(def.Fields))
			allowed := make(map[string][]Value)
			order := make([]string, 0, len(def.Fields))
			for _, f := range def.Fields {
				order = append(order, f.Name)
				if len(f.Allowed) > 0 {
					var opts []Value
					for _, optExpr := range f.Allowed {
						optVals, err := ev.evalUnionVariantExpr(optExpr, ev.global)
						if err != nil {
							ev.initErr = err
							return ev
						}
						opts = append(opts, optVals...)
					}
					allowed[f.Name] = opts
					fields[f.Name] = defaultValueForUnionOption(opts[0])
					continue
				}
				val, err := ev.EvalExpr(f.Default, ev.global)
				if err != nil {
					ev.initErr = err
					return ev
				}
				if uVal, isUnion := val.(*UnionValue); isUnion {
					opts := make([]Value, len(uVal.Values))
					copy(opts, uVal.Values)
					allowed[f.Name] = opts
					if len(opts) > 0 {
						fields[f.Name] = defaultValueForUnionOption(opts[0])
					} else {
						fields[f.Name] = nil
					}
					continue
				}
				fields[f.Name] = instantiateRecordValue(val)
			}
			ev.global.Define(def.Name, &RecordValue{
				TypeName:    def.Name,
				Fields:      fields,
				Allowed:     allowed,
				Order:       order,
				IsPrototype: true,
			}, false)
		} else if item.union != nil {
			u := item.union
			vals := make([]Value, 0, len(u.Variants))
			for _, ve := range u.Variants {
				vItems, err := ev.evalUnionVariantExpr(ve, ev.global)
				if err != nil {
					ev.initErr = err
					return ev
				}
				vals = append(vals, vItems...)
			}
			ev.global.Define(u.Name, &UnionValue{
				TypeName: u.Name,
				Values:   vals,
			}, false)
		}
	}
	return ev
}

func instantiateRecordValue(v Value) Value {
	rec, ok := v.(*RecordValue)
	if !ok || rec == nil || !rec.IsPrototype {
		return v
	}
	fieldsCopy := make(map[string]Value, len(rec.Fields))
	for k, fv := range rec.Fields {
		fieldsCopy[k] = instantiateRecordValue(fv)
	}
	orderCopy := make([]string, len(rec.Order))
	copy(orderCopy, rec.Order)
	return &RecordValue{
		TypeName:    rec.TypeName,
		Fields:      fieldsCopy,
		Allowed:     rec.Allowed,
		Order:       orderCopy,
		IsPrototype: false,
	}
}

func (ev *Evaluator) evalUnionVariantExpr(expr Expr, env *Env) ([]Value, error) {
	if id, ok := expr.(*IdentExpr); ok {
		if id.Name == "text" || id.Name == "number" {
			return []Value{BaseTypeValue(id.Name)}, nil
		}
	}
	val, err := ev.EvalExpr(expr, env)
	if err != nil {
		return nil, err
	}
	if subUnion, ok := val.(*UnionValue); ok {
		out := make([]Value, len(subUnion.Values))
		copy(out, subUnion.Values)
		return out, nil
	}
	return []Value{val}, nil
}

func defaultValueForUnionOption(opt Value) Value {
	switch o := opt.(type) {
	case BaseTypeValue:
		if o == "number" {
			return float64(0)
		}
		return ""
	case *RecordValue:
		fieldsCopy := make(map[string]Value, len(o.Fields))
		for k, v := range o.Fields {
			fieldsCopy[k] = instantiateRecordValue(v)
		}
		orderCopy := make([]string, len(o.Order))
		copy(orderCopy, o.Order)
		return &RecordValue{
			TypeName:    o.TypeName,
			Fields:      fieldsCopy,
			Allowed:     o.Allowed,
			Order:       orderCopy,
			IsPrototype: false,
		}
	default:
		return opt
	}
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
		if err := ev.checkNotConditionValue(val, s.Line, s.Col); err != nil {
			return nil, nil, err
		}
		env.Define(s.Name, val, false)
		return val, nil, nil

	case *AssignStmt:
		val, err := ev.EvalExpr(s.Value, env)
		if err != nil {
			return nil, nil, err
		}
		if err := ev.checkNotConditionValue(val, s.Line, s.Col); err != nil {
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
		if err := ev.checkNotConditionValue(val, s.Line, s.Col); err != nil {
			return nil, nil, err
		}
		fmt.Fprintln(ev.out, FormatValue(val))
		return val, nil, nil

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
		lv, ok := iterVal.(*ListValue)
		if !ok {
			line, col := s.Iterable.Pos()
			return nil, nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'for' requires a list, got %s", TypeName(iterVal)),
				"Iterate over a list like 'list 1, 2, 3' or 'range 0 5'.",
			)
		}
		var last Value
		for _, item := range lv.Elements {
			loopEnv := NewEnv(env)
			if s.ItemName != "" {
				loopEnv.Define(s.ItemName, item, false)
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
		hasArrowRule := false
		for _, rule := range s.Rules {
			if rule.Action != nil {
				hasArrowRule = true
			}
			condVal, err := ev.EvalExpr(rule.Cond, env)
			if err != nil {
				return nil, nil, err
			}
			condBool, ok := condVal.(bool)
			if !ok {
				return nil, nil, newError("RuntimeError", rule.Line, rule.Col, ev.source, fmt.Sprintf("Check condition must be a condition, got %s", TypeName(condVal)), "")
			}
			if rule.Action != nil {
				if condBool {
					val, sig, err := ev.execStmt(rule.Action, NewEnv(env))
					if err != nil || sig != nil {
						return val, sig, err
					}
					if _, isExpr := rule.Action.(*ExprStmt); isExpr {
						if ev.callDepth > 0 {
							return val, &reportSignal{val: val}, nil
						}
						if _, isCall := rule.Action.(*ExprStmt).Expression.(*CallExpr); !isCall {
							return nil, nil, newError(
								"RuntimeError",
								rule.Line,
								rule.Col,
								ev.source,
								"Cannot return a value from 'check' at top level; use 'say' or 'do fail' instead.",
								"Top-level 'check' rules can run statements like 'say \"...\"', assignments, or 'do fail \"...\"'.",
							)
						}
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
		if hasArrowRule && len(s.ElseBody) > 0 {
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
			if !isAtomicEaseValue(val) {
				pLine, pCol := part.Expr.Pos()
				if pLine == 0 {
					pLine, pCol = e.Line, e.Col
				}
				return nil, newError(
					"TypeError",
					pLine,
					pCol,
					ev.source,
					fmt.Sprintf("Cannot interpolate %s into text", TypeName(val)),
					"String interpolation '{...}' only allows atomic values (text, number). Interpolate a field like '{hero.hp}' or use 'do encode_json ...'.",
				)
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
		val, ok, blockedGlobal := env.LookupChecked(e.Name)
		if !ok {
			if blockedGlobal {
				return nil, newError(
					"RuntimeError",
					e.Line,
					e.Col,
					ev.source,
					fmt.Sprintf("Cannot modify global '%s' inside a function; pass it in and return the new value instead.", e.Name),
					fmt.Sprintf("If you intended a local variable, assign '%s = ...' before reading it.", e.Name),
				)
			}
			if itVal, hasIt := env.Lookup("it"); hasIt {
				if rec, isRec := itVal.(*RecordValue); isRec && rec != nil {
					if _, hasField := rec.Fields[e.Name]; hasField {
						return nil, newError(
							"RuntimeError",
							e.Line,
							e.Col,
							ev.source,
							fmt.Sprintf("Unknown variable %q (field %q belongs to loop item 'it' of type %s — write 'it.%s')", e.Name, e.Name, rec.TypeName, e.Name),
							fmt.Sprintf("Record fields are not injected into 'for ... do' loops. Use 'it.%s' instead of bare '%s'.", e.Name, e.Name),
						)
					}
				}
			}
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
		if len(items) == 0 {
			return &ListValue{ElemType: "", Elements: items}, nil
		}
		firstType := structuredTypeName(items[0])
		for i := 1; i < len(items); i++ {
			gotType := structuredTypeName(items[i])
			if gotType != firstType {
				line, col := e.Elements[i].Pos()
				return nil, newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Mixed types in 'list': expected %s, got %s", firstType, gotType),
					"All elements in a 'list' must have the same type. Use 'def' records for structured data.",
				)
			}
		}
		return &ListValue{ElemType: firstType, Elements: items}, nil

	case *TypedListExpr:
		if len(e.Rows) == 0 {
			if e.ElemType != "number" && e.ElemType != "text" {
				protoVal, ok := ev.global.Lookup(e.ElemType)
				if !ok {
					return nil, newError("TypeError", e.Line, e.Col, ev.source, fmt.Sprintf("Unknown type %q in 'list as %s'", e.ElemType, e.ElemType), "Define a record with 'def', a union, or use 'number' / 'text'.")
				}
				_, isRec := protoVal.(*RecordValue)
				_, isUnion := protoVal.(*UnionValue)
				if !isRec && !isUnion {
					return nil, newError("TypeError", e.Line, e.Col, ev.source, fmt.Sprintf("%q is not a 'def' record or union type", e.ElemType), "Use 'list as <def_name>' or 'list as number' / 'list as text'.")
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
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("'not' requires a condition, got %s", TypeName(right)), "Example: not (x is 0)")
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
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("'and' requires condition values, got %s", TypeName(lv)), "")
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
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("'and' requires condition values, got %s", TypeName(rv)), "")
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
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("'or' requires condition values, got %s", TypeName(lv)), "")
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
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("'or' requires condition values, got %s", TypeName(rv)), "")
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
		if err := ev.checkNotConditionValue(left, e.Line, e.Col); err != nil {
			return nil, err
		}
		if err := ev.checkNotConditionValue(right, e.Line, e.Col); err != nil {
			return nil, err
		}
		if e.Op == "is" || e.Op == "is not" {
			if fe, isField := e.Left.(*FieldExpr); isField {
				if rightStr, isStr := e.Right.(*StringExpr); isStr {
					if targetVal, tErr := ev.EvalExpr(fe.Target, env); tErr == nil {
						if rec, isRec := targetVal.(*RecordValue); isRec && !rec.IsPrototype {
							if opts := rec.Allowed[fe.Field]; len(opts) > 0 {
								if err := ev.checkRuntimeUnionLiteral(rightStr.Value, opts, fmt.Sprintf("%s.%s", rec.TypeName, fe.Field), e.Line, e.Col); err != nil {
									return nil, err
								}
							}
						}
					}
				}
			}
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
		if err := ev.checkLeftOfIsNotType(targetVal, e.Line, e.Col); err != nil {
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
		return ev.castValue(e, targetVal, env)

	case *FieldExpr:
		targetVal, err := ev.EvalExpr(e.Target, env)
		if err != nil {
			return nil, err
		}
		if mv, ok := targetVal.(*MapValue); ok {
			val, exists := mv.Fields[e.Field]
			if !exists {
				return nil, newError(
					"RuntimeError",
					e.Line,
					e.Col,
					ev.source,
					fmt.Sprintf("map has no field %q", e.Field),
					fmt.Sprintf("Available fields: %s", strings.Join(mv.Order, ", ")),
				)
			}
			return val, nil
		}
		rec, ok := targetVal.(*RecordValue)
		if !ok {
			return nil, newError(
				"RuntimeError",
				e.Line,
				e.Col,
				ev.source,
				fmt.Sprintf("Cannot read field '.%s' on %s", e.Field, TypeName(targetVal)),
				"Field access '.name' only works on 'def' records or maps.",
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
		if lv, isList := baseVal.(*ListValue); isList {
			return ev.cloneRecordListWithUpdates(lv, e.Updates, env, e.Line, e.Col)
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
			if err := ev.checkNotConditionValue(v, e.Line, e.Col); err != nil {
				return nil, err
			}
			args[i] = v
		}
		return ev.callFunction(e.Callee, args, e.Line, e.Col)
	}

	line, col := expr.Pos()
	return nil, newError("RuntimeError", line, col, ev.source, "Unsupported expression", "")
}

func (ev *Evaluator) cloneRecordListWithUpdates(lv *ListValue, updates []WithFieldUpdate, env *Env, line, col int) (*ListValue, error) {
	if len(lv.Elements) == 0 {
		if lv.ElemType == "" {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				"Cannot use 'with' on empty untyped list",
				"'with' on a list only supports lists of 'def' records.",
			)
		}
		if lv.ElemType == "number" || lv.ElemType == "text" {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot use 'with' on list of %s", lv.ElemType),
				"'with' on a list only supports lists of 'def' records.",
			)
		}
		protoVal, ok := env.Lookup(lv.ElemType)
		if !ok {
			protoVal, ok = ev.global.Lookup(lv.ElemType)
		}
		recProto, isRec := protoVal.(*RecordValue)
		if !ok || !isRec {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot use 'with' on list of %s", lv.ElemType),
				"'with' on a list only supports lists of 'def' records.",
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
					fmt.Sprintf("Available fields: %s", strings.Join(recProto.Order, ", ")),
				)
			}
		}
		return &ListValue{ElemType: lv.ElemType}, nil
	}

	out := make([]Value, 0, len(lv.Elements))
	elemType := lv.ElemType
	for _, item := range lv.Elements {
		rec, ok := item.(*RecordValue)
		if !ok {
			return nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot use 'with' on list of %s", structuredTypeName(item)),
				"'with' on a list only supports lists of 'def' records.",
			)
		}
		if elemType == "" {
			elemType = rec.TypeName
		}
		itemEnv := NewEnv(env)
		for k, v := range rec.Fields {
			itemEnv.Define(k, v, false)
		}
		itemEnv.Define("it", rec, false)
		inst, err := ev.cloneRecordWithUpdates(rec, updates, itemEnv)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return &ListValue{ElemType: elemType, Elements: out}, nil
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
				fmt.Sprintf("Available fields: %s", strings.Join(rec.Order, ", ")),
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
	case "==", "is":
		return ev.evalIs(left, right, line, col)
	case "!=", "is-not":
		eq, err := ev.evalIs(left, right, line, col)
		if err != nil {
			return nil, err
		}
		return !eq, nil
	case "+":
		if _, lok := left.(string); lok {
			return FormatValue(left) + FormatValue(right), nil
		}
		if _, rok := right.(string); rok {
			return FormatValue(left) + FormatValue(right), nil
		}
		if la, ok := left.(*ListValue); ok {
			if rb, ok2 := right.(*ListValue); ok2 {
				if la.ElemType != "" && rb.ElemType != "" && la.ElemType != rb.ElemType {
					return nil, newError(
						"TypeError",
						line,
						col,
						ev.source,
						fmt.Sprintf("Cannot concatenate list of %s and list of %s", la.ElemType, rb.ElemType),
						"Both lists in '+' must have the same element type.",
					)
				}
				elemType := la.ElemType
				if elemType == "" {
					elemType = rb.ElemType
				}
				if len(la.Elements)+len(rb.Elements) > maxListSize {
					return nil, newError(
						"RuntimeError",
						line,
						col,
						ev.source,
						fmt.Sprintf("list size exceeds limit (%d elements)", maxListSize),
						fmt.Sprintf("Keep concatenated lists within %d elements.", maxListSize),
					)
				}
				merged := make([]Value, 0, len(la.Elements)+len(rb.Elements))
				merged = append(merged, la.Elements...)
				merged = append(merged, rb.Elements...)
				return &ListValue{ElemType: elemType, Elements: merged}, nil
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
			return math.Mod(ln, rn), nil
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

	fnEnv := NewFuncEnv(ev.global, collectAssignedNames(fn.Body))
	for i, param := range fn.Params {
		fnEnv.DefineParam(param, args[i])
	}

	last, sig, err := ev.execBlock(fn.Body, fnEnv)
	if err != nil {
		return nil, err
	}
	if sig != nil {
		if err := ev.checkNotConditionValue(sig.val, line, col); err != nil {
			return nil, err
		}
		return sig.val, nil
	}
	if err := ev.checkNotConditionValue(last, line, col); err != nil {
		return nil, err
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
		startF, endF := math.Trunc(s), math.Trunc(e)
		if math.Abs(startF) > float64(math.MaxInt64/2) || math.Abs(endF) > float64(math.MaxInt64/2) {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("range too large (limit %d elements)", maxListSize),
				fmt.Sprintf("Keep 'do range start end' within %d elements.", maxListSize),
			)
		}
		if endF >= startF && (endF-startF+1) > float64(maxListSize) {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("range too large (limit %d elements)", maxListSize),
				fmt.Sprintf("Keep 'do range start end' within %d elements.", maxListSize),
			)
		}
		start, end := int(startF), int(endF)
		out := make([]Value, 0, max(0, end-start+1))
		for i := start; i <= end; i++ {
			out = append(out, float64(i))
		}
		return &ListValue{ElemType: "number", Elements: out}, true, nil

	case "push":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "push(list, item) takes 2 arguments", "")
		}
		listVal, ok := args[0].(*ListValue)
		if !ok {
			return nil, true, newError("TypeError", line, col, ev.source, "push() expects a list as first argument", "")
		}
		if len(listVal.Elements) >= maxListSize {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("list size exceeds limit (%d elements)", maxListSize),
				fmt.Sprintf("Keep lists within %d elements.", maxListSize),
			)
		}
		gotType := structuredTypeName(args[1])
		elemType := listVal.ElemType
		if elemType != "" {
			if !matchesListElemType(args[1], elemType, ev.global) {
				return nil, true, newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Cannot push %s into list of %s", gotType, elemType),
					fmt.Sprintf("This list only accepts elements of type %q.", elemType),
				)
			}
		} else {
			elemType = gotType
		}
		var next []Value
		if !listVal.pushed {
			listVal.pushed = true
			next = append(listVal.Elements, args[1])
		} else {
			newLen := len(listVal.Elements) + 1
			newCap := max(4, newLen*2)
			next = make([]Value, newLen, newCap)
			copy(next, listVal.Elements)
			next[len(listVal.Elements)] = args[1]
		}
		return &ListValue{ElemType: elemType, Elements: next}, true, nil

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
		if mv, ok := args[0].(*MapValue); ok {
			out := make([]Value, len(mv.Order))
			for i, k := range mv.Order {
				out[i] = k
			}
			return &ListValue{ElemType: "text", Elements: out}, true, nil
		}
		rec, ok := args[0].(*RecordValue)
		if !ok {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("fields() requires a 'def' record or map, got %s", TypeName(args[0])), "Define a record with 'def ... end'.")
		}
		out := make([]Value, len(rec.Order))
		for i, k := range rec.Order {
			out[i] = k
		}
		return &ListValue{ElemType: "text", Elements: out}, true, nil

	case "decode_json":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "decode_json takes 1 argument (text)", "Example: do decode_json raw_text")
		}
		rawStr, ok := args[0].(string)
		if !ok {
			return nil, true, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("decode_json requires text, got %s", TypeName(args[0])),
				"Pass a JSON string to decode, e.g. do decode_json '{\"id\": 1}'.",
			)
		}
		if strings.TrimSpace(rawStr) == "" {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				"Cannot decode empty text as JSON",
				"Check for empty responses before calling 'do decode_json'.",
			)
		}
		var parsed any
		if err := json.Unmarshal([]byte(rawStr), &parsed); err != nil {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Invalid JSON in decode_json: %v", err),
				"Provide valid JSON text.",
			)
		}
		return jsonAnyToEase(parsed), true, nil

	case "encode_json":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "encode_json takes 1 argument", "Example: do encode_json user")
		}
		if err := validateJSONEncodable(args[0]); err != nil {
			return nil, true, newError("TypeError", line, col, ev.source, err.Error(), "Conditions and union definitions cannot be encoded as JSON.")
		}
		b, err := json.Marshal(easeValueToJSON(args[0]))
		if err != nil {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Cannot encode %s to JSON: %v", TypeName(args[0]), err), "")
		}
		return string(b), true, nil

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

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodyBytes+1))
	if err != nil {
		return ev.newHTTPResponseRecord(float64(resp.StatusCode), "", err.Error())
	}
	if len(rawBody) > maxHTTPBodyBytes {
		return ev.newHTTPResponseRecord(
			float64(resp.StatusCode),
			"",
			fmt.Sprintf("HTTP response body exceeds %d MB limit", maxHTTPBodyBytes/(1024*1024)),
		)
	}
	return ev.newHTTPResponseRecord(float64(resp.StatusCode), string(rawBody), "")
}

func jsonAnyToEase(v any) Value {
	switch x := v.(type) {
	case map[string]any:
		fields := make(map[string]Value, len(x))
		order := make([]string, 0, len(x))
		for k := range x {
			order = append(order, k)
		}
		slices.Sort(order)
		for _, k := range order {
			fields[k] = jsonAnyToEase(x[k])
		}
		return &MapValue{Fields: fields, Order: order}
	case []any:
		out := make([]Value, len(x))
		elemType := ""
		for i, el := range x {
			conv := jsonAnyToEase(el)
			out[i] = conv
			et := structuredTypeName(conv)
			if i == 0 {
				elemType = et
			} else if elemType != "" && et != elemType {
				elemType = ""
			}
		}
		return &ListValue{ElemType: elemType, Elements: out}
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return x
	}
}

func isTrueFalseUnion(opts []Value) bool {
	if len(opts) != 2 {
		return false
	}
	s0, ok0 := opts[0].(string)
	s1, ok1 := opts[1].(string)
	if !ok0 || !ok1 {
		return false
	}
	return (s0 == "true" && s1 == "false") || (s0 == "false" && s1 == "true")
}

func (ev *Evaluator) checkNotConditionValue(v Value, line, col int) error {
	if _, isCond := v.(bool); isCond {
		return newError(
			"TypeError",
			line,
			col,
			ev.source,
			"Conditions cannot be stored or printed. Use a union type instead, e.g.: def result = \"yes\" | \"no\"",
			"Evaluate conditions directly inside 'check' or 'test', and return/store explicit union variants instead.",
		)
	}
	return nil
}

func (ev *Evaluator) checkRuntimeUnionLiteral(lit string, opts []Value, label string, line, col int) error {
	var allowedDisplay []string
	for _, opt := range opts {
		if bt, ok := opt.(BaseTypeValue); ok && string(bt) == "text" {
			return nil
		}
		if s, ok := opt.(string); ok && s == lit {
			return nil
		}
		allowedDisplay = append(allowedDisplay, formatUnionOption(opt))
	}
	return newError(
		"TypeError",
		line,
		col,
		ev.source,
		fmt.Sprintf("%q is not a value of %s (%s)", lit, label, strings.Join(allowedDisplay, " | ")),
		fmt.Sprintf("Valid values for %s: %s.", label, strings.Join(allowedDisplay, " | ")),
	)
}

func validateJSONEncodable(v Value) error {
	switch x := v.(type) {
	case bool:
		return fmt.Errorf("Cannot encode condition to JSON")
	case BaseTypeValue:
		return fmt.Errorf("Cannot encode base type %q to JSON", string(x))
	case *UnionValue:
		return fmt.Errorf("Cannot encode union definition %q to JSON", x.TypeName)
	case *RecordValue:
		for _, k := range x.Order {
			if err := validateJSONEncodable(x.Fields[k]); err != nil {
				return err
			}
		}
	case *MapValue:
		for _, k := range x.Order {
			if err := validateJSONEncodable(x.Fields[k]); err != nil {
				return err
			}
		}
	case *ListValue:
		for _, el := range x.Elements {
			if err := validateJSONEncodable(el); err != nil {
				return err
			}
		}
	}
	return nil
}

func easeValueToJSON(v Value) any {
	switch x := v.(type) {
	case *RecordValue:
		out := make(map[string]any, len(x.Fields))
		for k, val := range x.Fields {
			if isTrueFalseUnion(x.Allowed[k]) {
				if s, ok := val.(string); ok {
					if s == "true" {
						out[k] = true
						continue
					}
					if s == "false" {
						out[k] = false
						continue
					}
				}
			}
			out[k] = easeValueToJSON(val)
		}
		return out
	case *MapValue:
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
	default:
		return x
	}
}

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
		var recNames []string
		var recErrs []string
		for _, opt := range opts {
			recOpt, ok := opt.(*RecordValue)
			if !ok || !recOpt.IsPrototype {
				continue
			}
			recNames = append(recNames, recOpt.TypeName)
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
		elems := make([]Value, len(lv.Elements))
		for idx, item := range lv.Elements {
			if !matchesEaseType(item, targetType) {
				return nil, newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Cannot convert list to %s: element %d expected %s, got %s (%s)", targetType, idx, targetType, structuredTypeName(item), FormatValue(item)),
					fmt.Sprintf("Every element of a list cast 'as %s' must be %s.", targetType, targetType),
				)
			}
			elems[idx] = item
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
			allowed := formatUnionOptions(union.Values)
			elems := make([]Value, len(lv.Elements))
			for idx, item := range lv.Elements {
				coerced, cErr := coerceToUnionOptions(item, union.Values, env, fmt.Sprintf("%s.%d", targetType, idx))
				if cErr != nil {
					return nil, newError(
						"TypeError",
						line,
						col,
						ev.source,
						fmt.Sprintf("Value %s at element %d is not valid for union %q: %s", FormatValue(item), idx, targetType, cErr.Error()),
						fmt.Sprintf("Allowed values for %s: %s.", targetType, strings.Join(allowed, ", ")),
					)
				}
				elems[idx] = coerced
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

		elems := make([]Value, 0, len(lv.Elements))
		for idx, item := range lv.Elements {
			if rec, ok := item.(*RecordValue); ok {
				if rec.TypeName != targetType {
					return nil, newError(
						"TypeError",
						line,
						col,
						ev.source,
						fmt.Sprintf("Cannot validate list as %s: expected structured map or %s record (element %d is %s)", targetType, targetType, idx, rec.TypeName),
						fmt.Sprintf("Expected a map from 'do decode_json' or a %s record matching 'def %s'.", targetType, targetType),
					)
				}
				elems = append(elems, rec)
				continue
			}
			mv, isMap := item.(*MapValue)
			if !isMap || mv == nil {
				return nil, newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Cannot validate list as %s: expected structured map or %s record (element %d is %s)", targetType, targetType, idx, structuredTypeName(item)),
					fmt.Sprintf("Expected a map from 'do decode_json' or a %s record matching 'def %s'.", targetType, targetType),
				)
			}
			mapped, mapErr := mapToRecord(mv.Fields, proto, env, fmt.Sprintf("%s.%d", targetType, idx))
			if mapErr != nil {
				return nil, newError(
					"TypeError",
					line,
					col,
					ev.source,
					mapErr.Error(),
					fmt.Sprintf("Ensure all fields declared in 'def %s' are present with matching types and valid union values.", targetType),
				)
			}
			elems = append(elems, mapped)
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
		elems := make([]Value, 0, len(lv.Elements))
		outElemType := tmpl.ElemType
		for idx, item := range lv.Elements {
			elemPath := fmt.Sprintf("%s.%d", path, idx)
			if tmpl.ElemType != "" {
				switch tmpl.ElemType {
				case "number":
					n, ok := item.(float64)
					if !ok {
						return nil, fmt.Errorf("Element '%s' expected number, got %s", elemPath, structuredTypeName(item))
					}
					elems = append(elems, n)
				case "text":
					s, ok := item.(string)
					if !ok {
						return nil, fmt.Errorf("Element '%s' expected text, got %s", elemPath, structuredTypeName(item))
					}
					elems = append(elems, s)
				default:
					protoVal, found := env.Lookup(tmpl.ElemType)
					if !found {
						return nil, fmt.Errorf("Unknown list element blueprint %q for '%s'", tmpl.ElemType, elemPath)
					}
					if union, isUnion := protoVal.(*UnionValue); isUnion {
						coerced, cErr := coerceToUnionOptions(item, union.Values, env, elemPath)
						if cErr != nil {
							return nil, fmt.Errorf("Invalid value %s for element '%s' of union %q: %s", FormatValue(item), elemPath, tmpl.ElemType, cErr.Error())
						}
						elems = append(elems, coerced)
						continue
					}
					if subRec, ok := item.(*RecordValue); ok && subRec.TypeName == tmpl.ElemType {
						elems = append(elems, subRec)
						continue
					}
					subMap, ok := item.(*MapValue)
					if !ok || subMap == nil {
						return nil, fmt.Errorf("Element '%s' expected %s map/record, got %s", elemPath, tmpl.ElemType, structuredTypeName(item))
					}
					proto, isRec := protoVal.(*RecordValue)
					if !isRec || !proto.IsPrototype {
						return nil, fmt.Errorf("%q is not a 'def' record blueprint for '%s'", tmpl.ElemType, elemPath)
					}
					rec, err := mapToRecord(subMap.Fields, proto, env, elemPath)
					if err != nil {
						return nil, err
					}
					elems = append(elems, rec)
				}
			} else {
				et := structuredTypeName(item)
				if idx == 0 {
					outElemType = et
				} else if et != outElemType {
					return nil, fmt.Errorf("Element '%s' expected %s, got %s", elemPath, outElemType, et)
				}
				elems = append(elems, item)
			}
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
