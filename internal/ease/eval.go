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
	"sync"

	"golang.org/x/text/unicode/norm"
)

type binding struct {
	val     Value
	mutable bool
	isParam bool
}

// Env implements lexical scopes with mutability tracking (immutable `def` constants/params vs mutable bindings)
// and a mutex-protected function table on the root scope for atomic hot-code-swapping.
type Env struct {
	mu             sync.RWMutex
	parent         *Env
	vars           map[string]binding
	funcs          map[string]*FuncDecl
	isFuncRoot     bool
	isWorkerRoot   bool
	assignedLocals map[string]bool
	loopIndex      float64
	hasLoopIndex   bool
}

// InboxStatus describes a named top-level inbox in a running service.
type InboxStatus struct {
	Name     string `json:"name"`
	ElemType string `json:"elem_type"`
	Buffered int    `json:"buffered"`
	Closed   bool   `json:"closed"`
}

func (e *Env) SetFunc(name string, fn *FuncDecl) {
	if e == nil || fn == nil {
		return
	}
	e.mu.Lock()
	if e.funcs == nil {
		e.funcs = make(map[string]*FuncDecl)
	}
	e.funcs[name] = fn
	e.mu.Unlock()
}

func (e *Env) LookupFunc(name string) (*FuncDecl, bool) {
	for cur := e; cur != nil; cur = cur.parent {
		cur.mu.RLock()
		fn, ok := cur.funcs[name]
		cur.mu.RUnlock()
		if ok {
			return fn, true
		}
	}
	return nil, false
}

func (e *Env) ListFuncs() []*FuncDecl {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	out := make([]*FuncDecl, 0, len(e.funcs))
	for _, fn := range e.funcs {
		out = append(out, fn)
	}
	e.mu.RUnlock()
	slices.SortFunc(out, func(a, b *FuncDecl) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// SwapDecls atomically replaces all function declarations and immutable def/union blueprints
// on this Env without disturbing active variables, listeners, or inboxes.
func (e *Env) SwapDecls(newFuncs map[string]*FuncDecl, newDecls map[string]Value) {
	if e == nil {
		return
	}
	copiedFuncs := make(map[string]*FuncDecl, len(newFuncs))
	for k, fn := range newFuncs {
		copiedFuncs[k] = fn
	}
	e.mu.Lock()
	e.funcs = copiedFuncs
	for k, v := range newDecls {
		e.vars[k] = binding{val: v, mutable: false}
	}
	e.mu.Unlock()
}

// SnapshotInboxes returns status snapshots for all top-level InboxValue bindings in sorted name order.
func (e *Env) SnapshotInboxes() []InboxStatus {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	var out []InboxStatus
	for name, b := range e.vars {
		if ib, ok := b.val.(*InboxValue); ok && ib != nil {
			out = append(out, InboxStatus{
				Name:     name,
				ElemType: ib.ElemType,
				Buffered: ib.Len(),
				Closed:   ib.IsClosed(),
			})
		}
	}
	e.mu.RUnlock()
	slices.SortFunc(out, func(a, b InboxStatus) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// SnapshotBindings returns a shallow snapshot of current top-level variable/def bindings.
func (e *Env) SnapshotBindings() map[string]Value {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	out := make(map[string]Value, len(e.vars))
	for k, b := range e.vars {
		out[k] = b.val
	}
	e.mu.RUnlock()
	return out
}

func (env *Env) collectVisibleNames() []string {
	seen := make(map[string]bool)
	var names []string
	for cur := env; cur != nil; cur = cur.parent {
		cur.mu.RLock()
		for k := range cur.vars {
			if !seen[k] {
				seen[k] = true
				names = append(names, k)
			}
		}
		cur.mu.RUnlock()
	}
	return names
}

func (ev *Evaluator) collectCallableNames() []string {
	seen := make(map[string]bool)
	var names []string
	if ev.global != nil {
		for _, fn := range ev.global.ListFuncs() {
			if !seen[fn.Name] {
				seen[fn.Name] = true
				names = append(names, fn.Name)
			}
		}
	}
	for k := range builtinArities {
		if !seen[k] {
			seen[k] = true
			names = append(names, k)
		}
	}
	return names
}

func missingFieldHint(field string, available []string) string {
	hint := fmt.Sprintf("Available fields: %s", strings.Join(available, ", "))
	if suggestion := closestTypeName(field, available); suggestion != "" {
		return fmt.Sprintf("Did you mean %q? %s", suggestion, hint)
	}
	return hint
}

func NewEnv(parent *Env) *Env {
	return &Env{parent: parent, vars: make(map[string]binding), funcs: make(map[string]*FuncDecl)}
}

func NewFuncEnv(global *Env, assignedLocals map[string]bool) *Env {
	return &Env{
		parent:         global,
		vars:           make(map[string]binding),
		isFuncRoot:     true,
		assignedLocals: assignedLocals,
	}
}

func NewWorkerEnv(parent *Env, assignedLocals map[string]bool) *Env {
	return &Env{
		parent:         parent,
		vars:           make(map[string]binding),
		isWorkerRoot:   true,
		assignedLocals: assignedLocals,
	}
}

func (e *Env) Define(name string, val Value, mutable bool) {
	e.mu.Lock()
	e.vars[name] = binding{val: val, mutable: mutable}
	e.mu.Unlock()
}

func (e *Env) DefineParam(name string, val Value) {
	e.mu.Lock()
	e.vars[name] = binding{val: val, mutable: false, isParam: true}
	e.mu.Unlock()
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
	val, ok, blockedKind := e.lookupCheckedKind(name)
	return val, ok, blockedKind != 0
}

func (e *Env) lookupCheckedKind(name string) (Value, bool, int) {
	for cur := e; cur != nil; cur = cur.parent {
		cur.mu.RLock()
		b, ok := cur.vars[name]
		cur.mu.RUnlock()
		if ok {
			return b.val, true, 0
		}
		if (cur.isFuncRoot || cur.isWorkerRoot) && cur.assignedLocals[name] {
			if cur.parent != nil {
				if _, isOuter := cur.parent.Lookup(name); isOuter {
					if cur.isWorkerRoot {
						return nil, false, 2
					}
					return nil, false, 1
				}
			}
			return nil, false, 0
		}
	}
	return nil, false, 0
}

func (e *Env) DefineOrAssign(name string, val Value, line, col int, source string) error {
	for cur := e; cur != nil; cur = cur.parent {
		cur.mu.Lock()
		b, ok := cur.vars[name]
		if ok {
			if !b.mutable {
				cur.mu.Unlock()
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
			cur.mu.Unlock()
			return nil
		}
		cur.mu.Unlock()
		if cur.isFuncRoot || cur.isWorkerRoot {
			break
		}
	}
	e.mu.Lock()
	e.vars[name] = binding{val: val, mutable: true}
	e.mu.Unlock()
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
			case *TakeStmt:
				walk(s.Body)
			case *CheckGuardStmt:
				for _, r := range s.Rules {
					if r.Action != nil {
						walk([]Stmt{r.Action})
					}
				}
				walk(s.ElseBody)
			case *MatchStmt:
				for _, arm := range s.Arms {
					if arm.Action != nil {
						walk([]Stmt{arm.Action})
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
	source         string
	out            io.Writer
	trace          io.Writer
	ioMu           *sync.Mutex
	rt             *runtimeLifecycle
	isChild        bool
	inReader       *bufio.Reader
	modules        map[string]*Evaluator
	global         *Env
	callDepth      int
	inSwarmWorker  bool
	initErr        error
	headlessScreen bool
	headlessNet    bool
	headlessAudio  bool
	httpClient     *http.Client
}

// SetHeadlessScreen toggles headless screen simulation for `--check` / CI tests.
func (ev *Evaluator) SetHeadlessScreen(headless bool) {
	ev.headlessScreen = headless
	for _, modEv := range ev.modules {
		modEv.SetHeadlessScreen(headless)
	}
}

// SetHeadlessNet toggles headless TCP listener simulation for `--check` mode.
func (ev *Evaluator) SetHeadlessNet(headless bool) {
	ev.headlessNet = headless
	for _, modEv := range ev.modules {
		modEv.SetHeadlessNet(headless)
	}
}

// SetHeadlessAudio toggles silent headless audio simulation for `--check` / CI tests.
func (ev *Evaluator) SetHeadlessAudio(headless bool) {
	ev.headlessAudio = headless
	for _, modEv := range ev.modules {
		modEv.SetHeadlessAudio(headless)
	}
}

// Close releases any active OS resources (such as an open screen window, idle HTTP connections, and background workers).
func (ev *Evaluator) Close() {
	if !ev.isChild && ev.rt != nil {
		ev.rt.abort()
		ev.rt.wg.Wait()
	}
	if ev.httpClient != nil {
		ev.httpClient.CloseIdleConnections()
	}
	for _, modEv := range ev.modules {
		modEv.Close()
	}
}

// SetTrace enables live execution trace logging to `w` (pass nil to disable).
func (ev *Evaluator) SetTrace(w io.Writer) {
	ev.trace = w
	for _, modEv := range ev.modules {
		modEv.SetTrace(w)
	}
}

func (ev *Evaluator) logTrace(format string, args ...any) {
	if ev.trace != nil {
		if ev.ioMu != nil {
			ev.ioMu.Lock()
			defer ev.ioMu.Unlock()
		}
		fmt.Fprintf(ev.trace, "[trace] "+format+"\n", args...)
	}
}

func (ev *Evaluator) writeOutString(s string) {
	if s == "" {
		return
	}
	if ev.ioMu != nil && !ev.inSwarmWorker {
		ev.ioMu.Lock()
		defer ev.ioMu.Unlock()
	}
	fmt.Fprint(ev.out, s)
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
		ioMu:       &sync.Mutex{},
		rt:         newRuntimeLifecycle(),
		inReader:   bufio.NewReader(in),
		modules:    make(map[string]*Evaluator),
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
	for _, imp := range prog.Imports {
		if imp.ModuleProg == nil {
			continue
		}
		alias := imp.Alias
		if alias == "" {
			alias = imp.Name
		}
		if _, exists := ev.modules[alias]; exists {
			continue
		}
		modEv := NewEvaluatorWithInput(imp.ModuleSource, imp.ModuleProg, ev.inReader, ev.out)
		modEv.ioMu = ev.ioMu
		modEv.rt = ev.rt
		modEv.isChild = true
		modEv.httpClient = ev.httpClient
		modEv.SetHeadlessScreen(ev.headlessScreen)
		modEv.SetHeadlessNet(ev.headlessNet)
		modEv.SetHeadlessAudio(ev.headlessAudio)
		modEv.SetTrace(ev.trace)
		if modEv.initErr != nil {
			return modEv.initErr
		}
		if _, err := modEv.ExecStatements(imp.ModuleProg.Body, modEv.global); err != nil {
			return err
		}
		modVal := &ModuleValue{
			Name:   alias,
			Env:    modEv.global,
			Source: imp.ModuleSource,
		}
		ev.modules[alias] = modEv
		ev.global.Define(alias, modVal, false)
	}
	for _, fn := range prog.Functions {
		ev.global.SetFunc(fn.Name, fn)
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
	return evalProgramWithOptions(source, "", in, out, trace, false)
}

// EvalProgramWithTraceFromFile parses and executes `source` resolved relative to `filePath`.
func EvalProgramWithTraceFromFile(source string, filePath string, in io.Reader, out io.Writer, trace io.Writer) (Value, error) {
	return evalProgramWithOptions(source, filePath, in, out, trace, false)
}

// EvalProgramHeadlessWithTrace parses and executes `source` with headless screen simulation.
func EvalProgramHeadlessWithTrace(source string, in io.Reader, out io.Writer, trace io.Writer) (Value, error) {
	return evalProgramWithOptions(source, "", in, out, trace, true)
}

// EvalProgramHeadlessWithTraceFromFile parses and executes `source` resolved relative to `filePath` with headless screen simulation.
func EvalProgramHeadlessWithTraceFromFile(source string, filePath string, in io.Reader, out io.Writer, trace io.Writer) (Value, error) {
	return evalProgramWithOptions(source, filePath, in, out, trace, true)
}

func evalProgramWithOptions(source string, filePath string, in io.Reader, out io.Writer, trace io.Writer, headless bool) (Value, error) {
	var prog *Program
	var err error
	if filePath != "" {
		prog, err = ParseProgramFromFile(source, filePath)
	} else {
		prog, err = ParseProgram(source)
	}
	if err != nil {
		return nil, err
	}
	ev := NewEvaluatorWithInput(source, prog, in, out)
	defer ev.Close()
	ev.SetHeadlessScreen(headless)
	ev.SetHeadlessNet(headless)
	ev.SetHeadlessAudio(headless)
	ev.SetTrace(trace)
	ev.logTrace("parsed %d def(s), %d function(s), %d check block(s), %d top-level statement(s)",
		len(prog.Defs), len(prog.Functions), len(prog.Checks), len(prog.Body))
	res, execErr := ev.ExecStatements(prog.Body, ev.global)
	if execErr != nil {
		ev.Close()
		return nil, execErr
	}
	if wErr := ev.WaitWorkers(); wErr != nil {
		return nil, wErr
	}
	return res, nil
}

// EvalProgram parses and executes `source`, writing `say` output to `out`.
func EvalProgram(source string, out io.Writer) (Value, error) {
	return evalProgramWithOptions(source, "", os.Stdin, out, nil, false)
}

// EvalProgramFromFile parses and executes `source` resolved relative to `filePath`, writing `say` output to `out`.
func EvalProgramFromFile(source string, filePath string, out io.Writer) (Value, error) {
	return evalProgramWithOptions(source, filePath, os.Stdin, out, nil, false)
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
	case *MatchStmt:
		ev.logTrace("line %d: match (%d arm(s))", line, len(s.Arms))
	case *LoopStmt:
		ev.logTrace("line %d: loop ...", line)
	case *TakeStmt:
		ev.logTrace("line %d: take ...", line)
	case *SpawnStmt:
		ev.logTrace("line %d: spawn ...", line)
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
		ev.writeOutString(FormatValue(val) + "\n")
		return val, nil, nil

	case *LoopEndStmt:
		return nil, &reportSignal{loopEnd: true}, nil

	case *SpawnStmt:
		return ev.execSpawnStmt(s, env)

	case *TakeStmt:
		return ev.execTakeStmt(s, env)

	case *LoopStmt:
		if s.Iterable == nil {
			var last Value
			for idx := 0; ; idx++ {
				loopEnv := NewEnv(env)
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
		}
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
						if ev.callDepth > 0 || ev.inSwarmWorker {
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
					if _, isExpr := lastStmt.(*ExprStmt); isExpr && (ev.callDepth > 0 || ev.inSwarmWorker) {
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
			if _, isExpr := lastStmt.(*ExprStmt); isExpr && (ev.callDepth > 0 || ev.inSwarmWorker) {
				return val, &reportSignal{val: val}, nil
			}
			return val, nil, nil
		}
		return nil, nil, nil

	case *MatchStmt:
		targetVal, err := ev.EvalExpr(s.Target, env)
		if err != nil {
			return nil, nil, err
		}
		if err := ev.checkNotConditionValue(targetVal, s.Line, s.Col); err != nil {
			return nil, nil, err
		}
		switch tv := targetVal.(type) {
		case string:
			for _, arm := range s.Arms {
				bindings, matched, err := matchPatternRunes(tv, arm.Pattern, env, ev.source)
				if err != nil {
					return nil, nil, err
				}
				if !matched {
					continue
				}
				armEnv := NewEnv(env)
				for k, v := range bindings {
					armEnv.Define(k, v, false)
				}
				val, sig, err := ev.execStmt(arm.Action, armEnv)
				if err != nil || sig != nil {
					return val, sig, err
				}
				if es, isExpr := arm.Action.(*ExprStmt); isExpr {
					if ev.callDepth > 0 || ev.inSwarmWorker {
						return val, &reportSignal{val: val}, nil
					}
					if _, isCall := es.Expression.(*CallExpr); !isCall {
						return nil, nil, newError(
							"RuntimeError",
							arm.Line,
							arm.Col,
							ev.source,
							"Cannot return a value from 'match' at top level; use 'say' or 'fail' instead.",
							"Top-level 'match' branches can run statements like 'say \"...\"', assignments, or 'fail \"...\"'.",
						)
					}
				}
				return val, nil, nil
			}
			if len(s.ElseBody) > 0 {
				val, sig, err := ev.execBlock(s.ElseBody, NewEnv(env))
				if err != nil || sig != nil {
					return val, sig, err
				}
				lastStmt := s.ElseBody[len(s.ElseBody)-1]
				if es, isExpr := lastStmt.(*ExprStmt); isExpr {
					if ev.callDepth > 0 || ev.inSwarmWorker {
						return val, &reportSignal{val: val}, nil
					}
					if _, isCall := es.Expression.(*CallExpr); !isCall {
						elLine, elCol := lastStmt.Pos()
						return nil, nil, newError(
							"RuntimeError",
							elLine,
							elCol,
							ev.source,
							"Cannot return a value from 'match' at top level; use 'say' or 'fail' instead.",
							"Top-level 'match' branches can run statements like 'say \"...\"', assignments, or 'fail \"...\"'.",
						)
					}
				}
				return val, nil, nil
			}
			return nil, nil, nil

		case *ListValue:
			if ev.callDepth == 0 && !ev.inSwarmWorker {
				return nil, nil, newError(
					"RuntimeError",
					s.Line,
					s.Col,
					ev.source,
					"Cannot use list 'match' without assigning its result at top level",
					"Assign the result (e.g. 'result = match items ... end') or return it from a 'to' function.",
				)
			}
			exprArms := make([]MatchArmExpr, 0, len(s.Arms))
			for _, arm := range s.Arms {
				es, isExpr := arm.Action.(*ExprStmt)
				if !isExpr {
					return nil, nil, newError(
						"RuntimeError",
						arm.Line,
						arm.Col,
						ev.source,
						"List 'match' transforms items with expressions and cannot execute statements",
						"Use 'loop items ... end' with 'match it ... end' to run statements for each item.",
					)
				}
				exprArms = append(exprArms, MatchArmExpr{
					Line:    arm.Line,
					Col:     arm.Col,
					Pattern: arm.Pattern,
					Value:   es.Expression,
				})
			}
			hasElse := len(s.ElseBody) > 0
			if hasElse {
				if _, isExpr := s.ElseBody[len(s.ElseBody)-1].(*ExprStmt); !isExpr {
					elLine, elCol := s.ElseBody[len(s.ElseBody)-1].Pos()
					return nil, nil, newError(
						"RuntimeError",
						elLine,
						elCol,
						ev.source,
						"List 'match' 'else' branch must end with an expression",
						"End 'else' with a result expression (or write 'else -> <expr>').",
					)
				}
			}
			listVal, err := ev.evalMatchList(tv, exprArms, hasElse, s.ElseBody, env, s.Line, s.Col)
			if err != nil {
				return nil, nil, err
			}
			return listVal, nil, nil

		default:
			return nil, nil, newError(
				"RuntimeError",
				s.Line,
				s.Col,
				ev.source,
				fmt.Sprintf("'match' requires text or a list of text, got %s", TypeName(targetVal)),
				"Pass a text string or a list of text items to 'match'.",
			)
		}

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
					"String interpolation '{...}' only allows atomic values (text, number). Interpolate a field like '{hero.hp}' or use 'encode_json ...'.",
				)
			}
			b.WriteString(FormatValue(val))
		}
		return b.String(), nil
	case *BoolExpr:
		return e.Value, nil
	case *IdentExpr:
		val, ok, blockedKind := env.lookupCheckedKind(e.Name)
		if !ok {
			if blockedKind == 2 {
				return nil, newError(
					"RuntimeError",
					e.Line,
					e.Col,
					ev.source,
					fmt.Sprintf("Cannot modify outer variable %q inside 'swarm loop'", e.Name),
					fmt.Sprintf("Assign to a different local variable name instead of reusing outer %q.", e.Name),
				)
			}
			if blockedKind == 1 {
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
							fmt.Sprintf("Record fields are not injected into 'loop' blocks. Use 'it.%s' instead of bare '%s'.", e.Name, e.Name),
						)
					}
				}
			}
			varHint := fmt.Sprintf("Define %q first with '%s = ...' or 'def %s = ...'.", e.Name, e.Name, e.Name)
			var modMatches []string
			for modAlias, modEv := range ev.modules {
				if _, ok := modEv.global.LookupFunc(e.Name); ok {
					modMatches = append(modMatches, fmt.Sprintf("%s:%s", modAlias, e.Name))
				}
			}
			slices.Sort(modMatches)
			if len(modMatches) > 0 {
				varHint = fmt.Sprintf("Did you mean '%s'? Module functions must be called with ':'.", modMatches[0])
			} else {
				candidates := append(env.collectVisibleNames(), ev.collectCallableNames()...)
				if suggestion := closestTypeName(e.Name, candidates); suggestion != "" {
					varHint = fmt.Sprintf("Did you mean %q? %s", suggestion, varHint)
				}
			}
			return nil, newError(
				"RuntimeError",
				e.Line,
				e.Col,
				ev.source,
				fmt.Sprintf("Unknown variable %q", e.Name),
				varHint,
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
				protoVal, ok := lookupTypeInEnv(ev.global, e.ElemType)
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
		protoVal, ok := lookupTypeInEnv(ev.global, e.ElemType)
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
				return nil, newError("TypeError", e.Line, e.Col, ev.source, fmt.Sprintf("'not' requires a condition, got %s", TypeName(right)), "Example: not (x is 0)")
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
		if mv, isMap := targetVal.(*MapValue); isMap {
			if keyStr, isStr := idxVal.(string); isStr {
				val, exists := mv.Fields[keyStr]
				if !exists {
					return nil, newError(
						"RuntimeError",
						e.Line,
						e.Col,
						ev.source,
						fmt.Sprintf("Map has no key %q", keyStr),
						fmt.Sprintf("Available keys: %s.", strings.Join(mv.Order, ", ")),
					)
				}
				return val, nil
			}
			idxFloat, ok := idxVal.(float64)
			if !ok || idxFloat != math.Trunc(idxFloat) {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("Map index must be an integer column index or text key, got %s", FormatValue(idxVal)), "Example: row.0 or row.(\"Product Name\")")
			}
			idx := int(idxFloat)
			if idx < 0 || idx >= len(mv.Order) {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("Column index %d out of range (columns: %d)", idx, len(mv.Order)), fmt.Sprintf("Valid column indices are 0 to %d.", len(mv.Order)-1))
			}
			return mv.Fields[mv.Order[idx]], nil
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
			return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("Cannot index into %s", TypeName(targetVal)), "Only lists, maps, and strings support '.<index>' access.")
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
		if call, isCall := e.Target.(*CallExpr); isCall && call.Module == "" && call.Callee == "load" && (e.TypeName == "csv" || e.TypeName == "bytes" || e.TypeName == "text") {
			if e.Fallback != nil {
				return nil, newError(
					"TypeError",
					e.OrLine,
					e.OrCol,
					ev.source,
					fmt.Sprintf("'or' fallback cannot be used with 'load ... as %s'", e.TypeName),
					"Handle file loading errors separately; 'or' fallbacks are for value conversions.",
				)
			}
			if len(call.Args) == 1 {
				pathVal, err := ev.EvalExpr(call.Args[0], env)
				if err != nil {
					return nil, err
				}
				pathStr, ok := pathVal.(string)
				if !ok {
					return nil, newError("TypeError", e.Line, e.Col, ev.source, fmt.Sprintf("load requires a text file path, got %s", TypeName(pathVal)), "Example: load \"./data.csv\" as csv")
				}
				return ev.evalLoad(pathStr, e.TypeName, e.Line, e.Col)
			}
		}
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
		if mv, ok := targetVal.(*ModuleValue); ok {
			if _, isFunc := mv.Env.LookupFunc(e.Field); isFunc {
				return nil, newError(
					"RuntimeError",
					e.Line,
					e.Col,
					ev.source,
					fmt.Sprintf("Use ':' to call module functions, e.g. '%s:%s ...'", mv.Name, e.Field),
					"Access module constants and blueprints with '.', and call module functions with ':'.",
				)
			}
			val, exists := mv.Env.Lookup(e.Field)
			if !exists {
				return nil, newError(
					"RuntimeError",
					e.Line,
					e.Col,
					ev.source,
					fmt.Sprintf("Module %q has no member %q", mv.Name, e.Field),
					fmt.Sprintf("Check exported 'def' constants, records, or functions in module %q.", mv.Name),
				)
			}
			return val, nil
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
					missingFieldHint(e.Field, mv.Order),
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
				missingFieldHint(e.Field, rec.Order),
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
		if modVal, isMod := baseVal.(*ModuleValue); isMod {
			withHint := fmt.Sprintf("Clone a blueprint inside the module, e.g.: %s.invoice with net = 100", modVal.Name)
			if _, hasSameDef := modVal.Env.Lookup(modVal.Name); hasSameDef {
				withHint = fmt.Sprintf("Use '%s.%s with ...' to instantiate the module's blueprint.", modVal.Name, modVal.Name)
			}
			return nil, newError(
				"RuntimeError",
				e.Line,
				e.Col,
				ev.source,
				fmt.Sprintf("Cannot use 'with' on module %q", modVal.Name),
				withHint,
			)
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
		if e.Module != "" {
			modEv, ok := ev.modules[e.Module]
			if !ok {
				return nil, newError("RuntimeError", e.Line, e.Col, ev.source, fmt.Sprintf("Unknown module %q", e.Module), "")
			}
			return modEv.callModuleFunction(e.Callee, args, e.Line, e.Col)
		}
		return ev.callFunction(e.Callee, args, e.Line, e.Col)

	case *MatchExpr:
		targetVal, err := ev.EvalExpr(e.Target, env)
		if err != nil {
			return nil, err
		}
		if err := ev.checkNotConditionValue(targetVal, e.Line, e.Col); err != nil {
			return nil, err
		}
		switch tv := targetVal.(type) {
		case string:
			if !e.HasElse {
				return nil, newError(
					"RuntimeError",
					e.Line,
					e.Col,
					ev.source,
					"'match' expression on text requires an 'else' branch",
					"Add 'else -> <fallback>' before 'end', or use statement form without assignment.",
				)
			}
			for _, arm := range e.Arms {
				bindings, matched, err := matchPatternRunes(tv, arm.Pattern, env, ev.source)
				if err != nil {
					return nil, err
				}
				if !matched {
					continue
				}
				armEnv := NewEnv(env)
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
				return val, nil
			}
			val, sig, err := ev.execBlock(e.ElseBody, NewEnv(env))
			if err != nil {
				return nil, err
			}
			if sig != nil {
				if sig.loopEnd {
					return nil, newError("RuntimeError", e.Line, e.Col, ev.source, "Unexpected '-> end' inside 'match' expression", "")
				}
				val = sig.val
			}
			if err := ev.checkNotConditionValue(val, e.Line, e.Col); err != nil {
				return nil, err
			}
			return val, nil

		case *ListValue:
			return ev.evalMatchList(tv, e.Arms, e.HasElse, e.ElseBody, env, e.Line, e.Col)

		default:
			return nil, newError(
				"RuntimeError",
				e.Line,
				e.Col,
				ev.source,
				fmt.Sprintf("'match' requires text or a list of text, got %s", TypeName(targetVal)),
				"Pass a text string or a list of text items to 'match'.",
			)
		}

	case *SwarmLoopExpr:
		return ev.evalSwarmLoop(e, env)
	case *InboxExpr:
		return ev.evalInboxExpr(e)
	}

	line, col := expr.Pos()
	return nil, newError("RuntimeError", line, col, ev.source, "Unsupported expression", "")
}

func (ev *Evaluator) evalMatchList(lv *ListValue, arms []MatchArmExpr, hasElse bool, elseBody []Stmt, env *Env, line, col int) (*ListValue, error) {
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
				fmt.Sprintf("'match' on a list requires text items, got %s at index %d", TypeName(el), idx),
				"Ensure every item in the target list is text.",
			)
		}
		itemEnv := NewEnv(env)
		itemEnv.Define("it", textItem, false)
		itemEnv.SetLoopIndex(float64(idx))

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
			val, sig, err := ev.execBlock(elseBody, NewEnv(itemEnv))
			if err != nil {
				return nil, err
			}
			if sig != nil {
				if sig.loopEnd {
					return nil, newError("RuntimeError", line, col, ev.source, "Unexpected '-> end' inside 'match' expression", "")
				}
				val = sig.val
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

func (ev *Evaluator) callModuleFunction(name string, args []Value, line, col int) (Value, error) {
	fn, ok := ev.global.LookupFunc(name)
	if !ok {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Unknown module function %q", name),
			fmt.Sprintf("Define it with 'to %s [...] ... end' in the module.", name),
		)
	}
	return ev.callUserFunction(fn, args, line, col)
}

func (ev *Evaluator) callFunction(name string, args []Value, line, col int) (Value, error) {
	if val, handled, err := ev.callBuiltin(name, args, line, col); handled {
		return val, err
	}

	fn, ok := ev.global.LookupFunc(name)
	if !ok {
		fnHint := fmt.Sprintf("Define it with 'to %s [...] ... end' or check the spelling.", name)
		if suggestion := closestTypeName(name, ev.collectCallableNames()); suggestion != "" {
			fnHint = fmt.Sprintf("Did you mean %q? %s", suggestion, fnHint)
		}
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Unknown function %q", name),
			fnHint,
		)
	}
	return ev.callUserFunction(fn, args, line, col)
}

func (ev *Evaluator) callUserFunction(fn *FuncDecl, args []Value, line, col int) (Value, error) {
	name := fn.Name
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
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("Maximum recursion depth (%d) exceeded", maxCallDepth),
			"Add a base case to stop recursion, or use a 'loop' over 'range' for deep iterations.",
		)
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

// buildDeclValues evaluates `prog`'s function, def blueprint, and union declarations
// in an isolated staging evaluator so hot-reload never mutates live state on error.
func buildDeclValues(prog *Program, source string) (map[string]*FuncDecl, map[string]Value, error) {
	staging := NewEvaluatorWithInput(source, prog, strings.NewReader(""), io.Discard)
	defer staging.Close()
	if staging.initErr != nil {
		return nil, nil, staging.initErr
	}
	fnList := staging.global.ListFuncs()
	funcsMap := make(map[string]*FuncDecl, len(fnList))
	for _, fn := range fnList {
		funcsMap[fn.Name] = fn
	}
	declsMap := make(map[string]Value)
	for name, val := range staging.global.SnapshotBindings() {
		switch tv := val.(type) {
		case *RecordValue:
			if tv != nil && tv.IsPrototype {
				declsMap[name] = tv
			}
		case *UnionValue:
			if tv != nil {
				declsMap[name] = tv
			}
		}
	}
	return funcsMap, declsMap, nil
}
