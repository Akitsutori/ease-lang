package ease

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var errDebugAborted = errors.New("debug session aborted")

type debugStepMode int

const (
	stepRun debugStepMode = iota
	stepOver
	stepInto
	stepOut
	stepPause
)

type debugFrame struct {
	id   int
	name string
	file string
	line int
	col  int
	env  *Env
}

// DebugStackFrame describes one call-stack frame for DAP stackTrace responses.
type DebugStackFrame struct {
	ID     int
	Name   string
	Source string
	Line   int
	Col    int
}

// DebugScope describes a Locals or Globals scope in a paused frame.
type DebugScope struct {
	Name               string
	VariablesReference int
	Expensive          bool
}

// DebugVariable describes one variable or expandable container entry in DAP.
type DebugVariable struct {
	Name               string
	Value              string
	Type               string
	VariablesReference int
}

type debugScopeRef struct {
	frameID  int
	isGlobal bool
}

// DebugController coordinates breakpoints, stepping, stack inspection, and watch evaluation
// for a single Evaluator execution without global state.
type DebugController struct {
	mu              sync.Mutex
	cond            *sync.Cond
	breakpoints     map[string]map[int]bool
	mode            debugStepMode
	stepTargetDepth int
	paused          bool
	aborted         bool
	stopOnEntry     bool
	frames          []*debugFrame
	nextFrameID     int
	varRefs         map[int]any
	nextVarRef      int
	lastStopFile    string
	lastStopLine    int
	onStopped       func(reason string, threadID int)
	rootProg        *Program
	rootSource      string
	rootEval        *Evaluator
}

// NewDebugController creates a new thread-safe DebugController.
func NewDebugController(stopOnEntry bool, onStopped func(reason string, threadID int)) *DebugController {
	dc := &DebugController{
		breakpoints: make(map[string]map[int]bool),
		varRefs:     make(map[int]any),
		nextFrameID: 1,
		nextVarRef:  1,
		stopOnEntry: stopOnEntry,
		onStopped:   onStopped,
	}
	if stopOnEntry {
		dc.mode = stepPause
	}
	dc.cond = sync.NewCond(&dc.mu)
	return dc
}

func normalizeDebugPath(p string) string {
	if p == "" {
		return ""
	}
	c := filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(c)
	}
	return c
}

// SetBreakpoints replaces the active breakpoints for `file` and returns the verified lines.
func (dc *DebugController) SetBreakpoints(file string, lines []int) []int {
	key := normalizeDebugPath(file)
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if len(lines) == 0 {
		delete(dc.breakpoints, key)
		return nil
	}
	set := make(map[int]bool, len(lines))
	verified := make([]int, 0, len(lines))
	for _, l := range lines {
		if l > 0 && !set[l] {
			set[l] = true
			verified = append(verified, l)
		}
	}
	dc.breakpoints[key] = set
	return verified
}

func (dc *DebugController) setProgramContext(prog *Program, source string, ev *Evaluator) {
	dc.mu.Lock()
	dc.rootProg = prog
	dc.rootSource = source
	dc.rootEval = ev
	dc.mu.Unlock()
}

// PushFrame pushes a new call-stack frame when entering top-level or a `to` function.
func (dc *DebugController) PushFrame(name, file string, line, col int, env *Env) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	id := dc.nextFrameID
	dc.nextFrameID++
	dc.frames = append(dc.frames, &debugFrame{
		id:   id,
		name: name,
		file: file,
		line: line,
		col:  col,
		env:  env,
	})
}

// PopFrame removes the top call-stack frame when returning from a `to` function or top level.
func (dc *DebugController) PopFrame() {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if len(dc.frames) > 0 {
		dc.frames = dc.frames[:len(dc.frames)-1]
	}
}

// BeforeStmt is called by Evaluator before executing each statement.
func (dc *DebugController) BeforeStmt(file string, line, col int, env *Env) error {
	dc.mu.Lock()
	if dc.aborted {
		dc.mu.Unlock()
		return errDebugAborted
	}

	if n := len(dc.frames); n > 0 {
		dc.frames[n-1].file = file
		dc.frames[n-1].line = line
		dc.frames[n-1].col = col
		dc.frames[n-1].env = env
	}

	reason := ""
	key := normalizeDebugPath(file)
	if lines := dc.breakpoints[key]; lines != nil && lines[line] {
		if !(dc.lastStopFile == key && dc.lastStopLine == line) {
			reason = "breakpoint"
		}
	}
	if reason == "" {
		switch dc.mode {
		case stepPause:
			if dc.stopOnEntry {
				reason = "entry"
				dc.stopOnEntry = false
			} else {
				reason = "pause"
			}
		case stepInto:
			reason = "step"
		case stepOver:
			if len(dc.frames) <= dc.stepTargetDepth {
				reason = "step"
			}
		case stepOut:
			if len(dc.frames) < dc.stepTargetDepth {
				reason = "step"
			}
		}
	}

	if reason == "" {
		dc.mu.Unlock()
		return nil
	}

	dc.paused = true
	dc.mode = stepRun
	dc.lastStopFile = key
	dc.lastStopLine = line
	dc.varRefs = make(map[int]any)
	dc.nextVarRef = 1
	cb := dc.onStopped
	dc.mu.Unlock()

	if cb != nil {
		cb(reason, 1)
	}

	dc.mu.Lock()
	for dc.paused && !dc.aborted {
		dc.cond.Wait()
	}
	aborted := dc.aborted
	dc.mu.Unlock()

	if aborted {
		return errDebugAborted
	}
	return nil
}

func (dc *DebugController) resume(mode debugStepMode) {
	dc.mu.Lock()
	dc.mode = mode
	switch mode {
	case stepOver, stepOut:
		dc.stepTargetDepth = len(dc.frames)
	default:
		dc.stepTargetDepth = 0
	}
	if mode != stepRun {
		dc.lastStopFile = ""
		dc.lastStopLine = 0
	}
	dc.paused = false
	dc.varRefs = make(map[int]any)
	dc.cond.Broadcast()
	dc.mu.Unlock()
}

// Continue resumes execution until the next breakpoint or program exit.
func (dc *DebugController) Continue() { dc.resume(stepRun) }

// StepOver steps to the next statement at the same or shallower call depth.
func (dc *DebugController) StepOver() { dc.resume(stepOver) }

// StepInto steps into the next function call or next statement.
func (dc *DebugController) StepInto() { dc.resume(stepInto) }

// StepOut continues until the current function returns.
func (dc *DebugController) StepOut() { dc.resume(stepOut) }

// Pause requests an immediate pause before the next statement.
func (dc *DebugController) Pause() {
	dc.mu.Lock()
	if !dc.paused {
		dc.mode = stepPause
	}
	dc.mu.Unlock()
}

// Abort terminates a paused or running debugged program cleanly.
func (dc *DebugController) Abort() {
	dc.mu.Lock()
	dc.aborted = true
	dc.paused = false
	dc.cond.Broadcast()
	dc.mu.Unlock()
}

// StackTrace returns the current call stack (top-most frame first).
func (dc *DebugController) StackTrace() []DebugStackFrame {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	out := make([]DebugStackFrame, 0, len(dc.frames))
	for i := len(dc.frames) - 1; i >= 0; i-- {
		f := dc.frames[i]
		out = append(out, DebugStackFrame{
			ID:     f.id,
			Name:   f.name,
			Source: f.file,
			Line:   max(1, f.line),
			Col:    max(1, f.col),
		})
	}
	return out
}

// Scopes returns Locals and Globals scopes for `frameID`.
func (dc *DebugController) Scopes(frameID int) []DebugScope {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	localsRef := dc.nextVarRef
	dc.nextVarRef++
	dc.varRefs[localsRef] = debugScopeRef{frameID: frameID, isGlobal: false}

	globalsRef := dc.nextVarRef
	dc.nextVarRef++
	dc.varRefs[globalsRef] = debugScopeRef{frameID: frameID, isGlobal: true}

	return []DebugScope{
		{Name: "Locals", VariablesReference: localsRef, Expensive: false},
		{Name: "Globals", VariablesReference: globalsRef, Expensive: false},
	}
}

func (dc *DebugController) findFrameLocked(frameID int) *debugFrame {
	for _, f := range dc.frames {
		if f.id == frameID {
			return f
		}
	}
	if len(dc.frames) > 0 {
		return dc.frames[len(dc.frames)-1]
	}
	return nil
}

func (dc *DebugController) allocVarRefLocked(val Value) int {
	switch v := val.(type) {
	case *RecordValue:
		if v == nil || len(v.Fields) == 0 {
			return 0
		}
	case *ListValue:
		if v == nil || len(v.Elements) == 0 {
			return 0
		}
	case *MapValue:
		if v == nil || len(v.Fields) == 0 {
			return 0
		}
	case *InboxValue:
		if v == nil {
			return 0
		}
	default:
		return 0
	}
	ref := dc.nextVarRef
	dc.nextVarRef++
	dc.varRefs[ref] = val
	return ref
}

func (dc *DebugController) toDebugVarLocked(name string, val Value) DebugVariable {
	return DebugVariable{
		Name:               name,
		Value:              FormatValue(val),
		Type:               TypeName(val),
		VariablesReference: dc.allocVarRefLocked(val),
	}
}

// Variables returns child variables for a scope or expandable container reference.
func (dc *DebugController) Variables(varRef int) []DebugVariable {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	target, ok := dc.varRefs[varRef]
	if !ok {
		return nil
	}

	switch t := target.(type) {
	case debugScopeRef:
		frame := dc.findFrameLocked(t.frameID)
		if frame == nil || frame.env == nil {
			return nil
		}
		if t.isGlobal {
			root := frame.env
			for root.parent != nil {
				root = root.parent
			}
			return dc.collectEnvVarsLocked(root, false)
		}
		return dc.collectLocalVarsLocked(frame.env)
	case *RecordValue:
		keys := t.Order
		if len(keys) == 0 {
			keys = make([]string, 0, len(t.Fields))
			for k := range t.Fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
		}
		out := make([]DebugVariable, 0, len(keys))
		for _, k := range keys {
			out = append(out, dc.toDebugVarLocked(k, t.Fields[k]))
		}
		return out
	case *ListValue:
		out := make([]DebugVariable, 0, len(t.Elements))
		for i, el := range t.Elements {
			out = append(out, dc.toDebugVarLocked(strconv.Itoa(i+1), el))
		}
		return out
	case *MapValue:
		keys := t.Order
		if len(keys) == 0 {
			keys = make([]string, 0, len(t.Fields))
			for k := range t.Fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
		}
		out := make([]DebugVariable, 0, len(keys))
		for _, k := range keys {
			out = append(out, dc.toDebugVarLocked(k, t.Fields[k]))
		}
		return out
	case *InboxValue:
		return []DebugVariable{
			{Name: "element_type", Value: t.ElemType, Type: "text"},
			{Name: "buffered", Value: strconv.Itoa(t.Len()), Type: "number"},
			{Name: "closed", Value: fmt.Sprintf("%t", t.IsClosed()), Type: "status"},
		}
	}
	return nil
}

func (dc *DebugController) collectLocalVarsLocked(env *Env) []DebugVariable {
	if env == nil || env.parent == nil {
		// Top-level <main>: show top-level user variables in Locals too for convenience.
		return dc.collectEnvVarsLocked(env, false)
	}
	seen := make(map[string]bool)
	var out []DebugVariable
	for cur := env; cur != nil && cur.parent != nil; cur = cur.parent {
		cur.mu.RLock()
		if cur.hasLoopIndex && !seen["it.index"] {
			seen["it.index"] = true
			out = append(out, DebugVariable{Name: "it.index", Value: FormatValue(cur.loopIndex), Type: "number"})
		}
		names := make([]string, 0, len(cur.vars))
		for k := range cur.vars {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if seen[k] {
				continue
			}
			seen[k] = true
			b := cur.vars[k]
			out = append(out, dc.toDebugVarLocked(k, b.val))
		}
		cur.mu.RUnlock()
		if cur.isFuncRoot {
			break
		}
	}
	return out
}

func (dc *DebugController) collectEnvVarsLocked(env *Env, includePrototypes bool) []DebugVariable {
	if env == nil {
		return nil
	}
	env.mu.RLock()
	names := make([]string, 0, len(env.vars))
	for k, b := range env.vars {
		if !includePrototypes {
			if rec, isRec := b.val.(*RecordValue); isRec && rec.IsPrototype {
				continue
			}
			if _, isUnion := b.val.(*UnionValue); isUnion {
				continue
			}
			if _, isMod := b.val.(*ModuleValue); isMod {
				continue
			}
		}
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]DebugVariable, 0, len(names))
	for _, k := range names {
		out = append(out, dc.toDebugVarLocked(k, env.vars[k].val))
	}
	env.mu.RUnlock()
	return out
}

// EvaluateInFrame evaluates a watch/console expression inside a child scope of `frameID`
// with debugging disabled so watch expressions cannot mutate live state or trigger breakpoints.
func (dc *DebugController) EvaluateInFrame(frameID int, exprText string) (DebugVariable, error) {
	dc.mu.Lock()
	frame := dc.findFrameLocked(frameID)
	prog := dc.rootProg
	baseEv := dc.rootEval
	dc.mu.Unlock()

	if frame == nil || frame.env == nil || baseEv == nil {
		return DebugVariable{}, fmt.Errorf("no active paused frame")
	}

	arities := make(map[string]int)
	if prog != nil {
		for _, fn := range prog.Functions {
			arities[fn.Name] = len(fn.Params)
		}
	}
	trimmed := strings.TrimSpace(exprText)
	if trimmed == "" {
		return DebugVariable{}, fmt.Errorf("empty expression")
	}

	// Parse inside a dummy `to __watch` or top-level chunk using known arities.
	parsed, err := ParseProgramWithAritiesForRepl(trimmed+"\n", arities)
	if err != nil {
		return DebugVariable{}, err
	}

	// Clone evaluator settings WITHOUT debug controller to prevent recursive breakpoint hits.
	watchEv := &Evaluator{
		source:         trimmed,
		out:            baseEv.out,
		ioMu:           baseEv.ioMu,
		rt:             baseEv.rt,
		isChild:        true,
		inReader:       baseEv.inReader,
		modules:        baseEv.modules,
		global:         baseEv.global,
		headlessScreen: baseEv.headlessScreen,
		headlessNet:    baseEv.headlessNet,
		headlessAudio:  baseEv.headlessAudio,
		httpClient:     baseEv.httpClient,
		programArgs:    baseEv.programArgs,
		debug:          nil,
	}
	childEnv := NewEnv(frame.env)
	val, _, execErr := watchEv.execBlock(parsed.Body, childEnv)
	if execErr != nil {
		return DebugVariable{}, execErr
	}

	dc.mu.Lock()
	defer dc.mu.Unlock()
	return dc.toDebugVarLocked(trimmed, val), nil
}
