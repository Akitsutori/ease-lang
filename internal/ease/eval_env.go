package ease

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

type binding struct {
	val     Value
	mutable bool
	isParam bool
}

// Env implements lexical scopes with mutability tracking (immutable `def` constants vs mutable bindings/params)
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
	e.vars[name] = binding{val: val, mutable: true, isParam: true}
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
				return newError(
					"RuntimeError",
					line,
					col,
					source,
					fmt.Sprintf("Cannot reassign constant %q defined with 'def'", name),
					fmt.Sprintf("Use a local variable '%s = ...' instead of 'def %s = ...' if it needs to change.", name, name),
				)
			}
			cur.vars[name] = binding{val: val, mutable: true, isParam: b.isParam}
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
				walk(s.Body)
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
					walk(arm.Body)
				}
				walk(s.ElseBody)
			}
		}
	}
	walk(stmts)
	return assigned
}
