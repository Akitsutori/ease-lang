package ease

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
)

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
	loopIndex      float64
	hasLoopIndex   bool
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

func (e *Env) SetLoopIndex(idx float64) {
	e.loopIndex = idx
	e.hasLoopIndex = true
}

func (e *Env) LookupLoopIndex() (float64, bool) {
	for cur := e; cur != nil; cur = cur.parent {
		if cur.hasLoopIndex {
			return cur.loopIndex, true
		}
		if cur.isFuncRoot {
			break
		}
	}
	return 0, false
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
			case *ConstDeclStmt:
				assigned[s.Name] = true
			case *LoopStmt:
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
	val     Value
	loopEnd bool
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
	httpClient       *http.Client
}

// SetHeadlessScreen toggles headless screen simulation for `--check` / CI tests.
func (ev *Evaluator) SetHeadlessScreen(headless bool) {
	ev.headlessScreen = headless
}

// Close releases any active OS resources (such as an open screen window and idle HTTP connections).
func (ev *Evaluator) Close() {
	if ev.activeScreen != nil {
		ev.activeScreen.close()
		ev.activeScreen = nil
	}
	if ev.httpClient != nil {
		ev.httpClient.CloseIdleConnections()
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
	ev := &Evaluator{
		source:     source,
		out:        out,
		inReader:   bufio.NewReader(in),
		funcs:      make(map[string]*FuncDecl),
		global:     NewEnv(nil),
		httpClient: &http.Client{},
	}
	if err := ev.RegisterDeclarations(prog); err != nil {
		ev.initErr = err
	}
	return ev
}

// RegisterDeclarations registers functions, record blueprints, and union types from `prog`
// into the evaluator's existing global scope without resetting existing bindings.
func (ev *Evaluator) RegisterDeclarations(prog *Program) error {
	if prog == nil {
		return nil
	}
	for _, fn := range prog.Functions {
		ev.funcs[fn.Name] = fn
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
							return err
						}
						opts = append(opts, optVals...)
					}
					allowed[f.Name] = opts
					fields[f.Name] = defaultValueForUnionOption(opts[0])
					continue
				}
				val, err := ev.EvalExpr(f.Default, ev.global)
				if err != nil {
					return err
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
					return err
				}
				vals = append(vals, vItems...)
			}
			ev.global.Define(u.Name, &UnionValue{
				TypeName: u.Name,
				Values:   vals,
			}, false)
		}
	}
	return nil
}

func instantiateRecordValue(v Value) Value {
	if lv, ok := v.(*ListValue); ok && lv != nil {
		elems := make([]Value, len(lv.Elements))
		for i, el := range lv.Elements {
			elems[i] = instantiateRecordValue(el)
		}
		return &ListValue{
			ElemType: lv.ElemType,
			Elements: elems,
			pushed:   false,
		}
	}
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
	return evalProgramWithOptions(source, os.Stdin, out, nil, false)
}

// ExecTopLevel runs a sequence of top-level statements in the evaluator's global environment.
func (ev *Evaluator) ExecTopLevel(stmts []Stmt) (Value, error) {
	return ev.ExecStatements(stmts, ev.global)
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
				"Unexpected control-flow signal at top level",
				"Use 'say' to print values or 'check <cond> -> end' inside a 'loop'.",
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
	case *LoopStmt:
		ev.logTrace("line %d: loop ...", line)
	case *LoopEndStmt:
		ev.logTrace("line %d: -> end", line)
	}
	switch s := stmt.(type) {
	case *ConstDeclStmt:
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

	case *LoopEndStmt:
		return nil, &reportSignal{loopEnd: true}, nil

	case *LoopStmt:
		iterVal, err := ev.EvalExpr(s.Iterable, env)
		if err != nil {
			return nil, nil, err
		}
		lv, ok := iterVal.(*ListValue)
		if !ok {
			line, col := s.Iterable.Pos()
			if nFloat, isNum := iterVal.(float64); isNum {
				return nil, nil, newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("'loop' requires a list or range, got number. Use 'loop range 1 %s' instead.", FormatValue(nFloat)),
					fmt.Sprintf("Write: loop range 1 %s ... end", FormatValue(nFloat)),
				)
			}
			return nil, nil, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("'loop' requires a list or range, got %s", TypeName(iterVal)),
				"Iterate over a list like 'loop xs ... end' or a range like 'loop range 1 5 ... end'.",
			)
		}
		var last Value
		for idx, item := range lv.Elements {
			loopEnv := NewEnv(env)
			loopEnv.Define("it", item, false)
			loopEnv.SetLoopIndex(float64(idx))
			val, sig, err := ev.execBlock(s.Body, loopEnv)
			if err != nil {
				return val, nil, err
			}
			if sig != nil {
				if sig.loopEnd {
					break
				}
				return val, sig, nil
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
								"Cannot return a value from 'check' at top level; use 'say' or 'fail' instead.",
								"Top-level 'check' rules can run statements like 'say \"...\"', assignments, or 'fail \"...\"'.",
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
		if id, ok := e.Target.(*IdentExpr); ok && id.Name == "it" && e.Field == "index" {
			if idx, hasIdx := env.LookupLoopIndex(); hasIdx {
				return idx, nil
			}
		}
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

	case *WhereExpr:
		targetVal, err := ev.EvalExpr(e.Target, env)
		if err != nil {
			return nil, err
		}
		lv, ok := targetVal.(*ListValue)
		if !ok {
			return nil, newError(
				"TypeError",
				e.Line,
				e.Col,
				ev.source,
				fmt.Sprintf("Cannot use 'where' on %s", TypeName(targetVal)),
				"'where' filters a list, e.g.: orders where it.amount > 50.",
			)
		}
		out := make([]Value, 0)
		for idx, el := range lv.Elements {
			iterEnv := NewEnv(env)
			iterEnv.Define("it", el, false)
			iterEnv.SetLoopIndex(float64(idx))
			keep := true
			for _, cond := range e.Conditions {
				condVal, err := ev.EvalExpr(cond, iterEnv)
				if err != nil {
					return nil, err
				}
				b, isBool := condVal.(bool)
				if !isBool {
					cLine, cCol := cond.Pos()
					return nil, newError(
						"TypeError",
						cLine,
						cCol,
						ev.source,
						fmt.Sprintf("'where' condition must evaluate to a condition, got %s", TypeName(condVal)),
						"Write a comparison like 'it.amount > 50' or 'it.status is \"paid\"'.",
					)
				}
				if !b {
					keep = false
					break
				}
			}
			if keep {
				out = append(out, el)
			}
		}
		return &ListValue{ElemType: lv.ElemType, Elements: out}, nil

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
	for idx, item := range lv.Elements {
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
		itemEnv.Define("it", rec, false)
		itemEnv.SetLoopIndex(float64(idx))
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
		if _, lok := left.(string); lok {
			return FormatValue(left) + FormatValue(right), nil
		}
		if _, rok := right.(string); rok {
			return FormatValue(left) + FormatValue(right), nil
		}
		ln, lok := left.(float64)
		rn, rok := right.(float64)
		if lok && rok {
			return ln + rn, nil
		}
		return nil, newError("TypeError", line, col, ev.source, fmt.Sprintf("Cannot add %s and %s", TypeName(left), TypeName(right)), "Use numbers, numeric lists, or strings with '+'.")

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
