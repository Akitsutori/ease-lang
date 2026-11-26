package ease

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultWatchInterval = 300 * time.Millisecond

// ServiceOptions configures `ease serve` lifecycle, hot-reloading, and opt-in debugging.
type ServiceOptions struct {
	FilePath      string
	PortOverride  int
	Watch         bool
	WatchInterval time.Duration
	DebugEnabled  bool
	DebugPort     int
	JSONLogs      bool
	Out           io.Writer
	ErrOut        io.Writer
}

type fileStatStamp struct {
	modTime time.Time
	size    int64
}

// Service manages a long-running Ease program with worker isolation, hot-code swapping, and optional loopback debugging.
type Service struct {
	entryPath string
	opts      ServiceOptions
	ev        *Evaluator

	logMu sync.Mutex
	mu    sync.Mutex

	latestSrc     string
	latestProg    *Program
	debugFuncs    map[string]*FuncDecl
	watchedStamps map[string]fileStatStamp

	debugSrv  *debugServer
	stopWatch chan struct{}
	watchWg   sync.WaitGroup
	shutOnce  sync.Once
}

// StartService parses, executes top-level setup, validates active listeners/workers, and starts watch/debug subsystems.
func StartService(opts ServiceOptions) (*Service, error) {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.ErrOut == nil {
		opts.ErrOut = os.Stderr
	}
	if opts.WatchInterval <= 0 {
		opts.WatchInterval = defaultWatchInterval
	}

	absEntry, err := filepath.Abs(opts.FilePath)
	if err != nil {
		absEntry = opts.FilePath
	}
	raw, err := os.ReadFile(absEntry)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", opts.FilePath, err)
	}
	src := string(raw)
	prog, err := ParseProgramFromFile(src, absEntry)
	if err != nil {
		return nil, err
	}

	ev := NewEvaluatorWithInput(src, prog, strings.NewReader(""), opts.Out)
	if ev.initErr != nil {
		ev.Close()
		return nil, ev.initErr
	}
	ev.SetHeadlessScreen(false)
	ev.SetHeadlessNet(false)
	ev.SetHeadlessAudio(false)

	svc := &Service{
		entryPath:     absEntry,
		opts:          opts,
		ev:            ev,
		latestSrc:     src,
		latestProg:    prog,
		debugFuncs:    make(map[string]*FuncDecl),
		watchedStamps: collectWatchedStamps(absEntry, prog),
		stopWatch:     make(chan struct{}),
	}

	ev.rt.mu.Lock()
	ev.rt.serviceMode = true
	ev.rt.portOverride = opts.PortOverride
	ev.rt.onWorkerError = func(workerErr error) {
		svc.logEvent("worker_error", map[string]any{"error": workerErr.Error()},
			fmt.Sprintf("[serve] worker error: %v", workerErr), true)
	}
	ev.rt.mu.Unlock()

	if _, err := ev.ExecStatements(prog.Body, ev.global); err != nil {
		ev.Close()
		return nil, err
	}

	if ev.rt.ActiveListenerCount() == 0 && ev.rt.ActiveWorkerCount() == 0 {
		ev.Close()
		return nil, errors.New("Program exited immediately with no active listeners or background workers (use 'ease run' for one-shot scripts)")
	}

	if opts.DebugEnabled {
		ds, err := startDebugServer(svc, opts.DebugPort)
		if err != nil {
			ev.Close()
			return nil, fmt.Errorf("failed to start debug server: %w", err)
		}
		svc.debugSrv = ds
	}

	if opts.Watch {
		svc.watchWg.Add(1)
		go svc.watchLoop()
	}

	return svc, nil
}

func (s *Service) logEvent(event string, fields map[string]any, humanMsg string, isErr bool) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	w := s.opts.Out
	if isErr {
		w = s.opts.ErrOut
	}
	if s.opts.JSONLogs {
		payload := make(map[string]any, len(fields)+1)
		payload["event"] = event
		for k, v := range fields {
			payload[k] = v
		}
		b, _ := json.Marshal(payload)
		fmt.Fprintln(w, string(b))
		return
	}
	fmt.Fprintln(w, humanMsg)
}

func collectWatchedStamps(entryPath string, prog *Program) map[string]fileStatStamp {
	stamps := make(map[string]fileStatStamp)
	addFile := func(p string) {
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err == nil {
			p = abs
		}
		if info, err := os.Stat(p); err == nil {
			stamps[p] = fileStatStamp{modTime: info.ModTime(), size: info.Size()}
		}
	}
	addFile(entryPath)
	var walkImports func(string, []*ImportDecl)
	walkImports = func(baseFile string, imps []*ImportDecl) {
		baseDir := filepath.Dir(baseFile)
		for _, imp := range imps {
			if imp != nil && imp.IsFileImport {
				target := imp.Path
				if !filepath.IsAbs(target) {
					target = filepath.Join(baseDir, target)
				}
				addFile(target)
				if imp.ModuleProg != nil {
					walkImports(target, imp.ModuleProg.Imports)
				}
			}
		}
	}
	if prog != nil {
		walkImports(entryPath, prog.Imports)
	}
	return stamps
}

func (s *Service) watchLoop() {
	defer s.watchWg.Done()
	ticker := time.NewTicker(s.opts.WatchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopWatch:
			return
		case <-ticker.C:
			s.mu.Lock()
			stamps := s.watchedStamps
			s.mu.Unlock()

			changedPath := ""
			for p, prev := range stamps {
				info, err := os.Stat(p)
				if err != nil {
					continue
				}
				if !info.ModTime().Equal(prev.modTime) || info.Size() != prev.size {
					changedPath = p
					break
				}
			}
			if changedPath != "" {
				if err := s.HotReloadFile(s.entryPath); err != nil {
					s.logEvent("reload_error", map[string]any{"file": changedPath, "error": err.Error()},
						fmt.Sprintf("[serve] reload failed (%s): %v", changedPath, err), true)
					// Update stamp on the edited file so we don't spam the same parse error every 300ms.
					s.mu.Lock()
					if info, statErr := os.Stat(changedPath); statErr == nil {
						s.watchedStamps[changedPath] = fileStatStamp{modTime: info.ModTime(), size: info.Size()}
					}
					s.mu.Unlock()
				} else {
					s.logEvent("reloaded", map[string]any{"file": changedPath},
						fmt.Sprintf("reloaded %s", changedPath), false)
				}
			}
		}
	}
}

// HotReloadFile reparses and typechecks `path` in isolation and atomically swaps `to` functions and `def`/union
// blueprints on the live root Env without interrupting active inboxes, listeners, or worker local state.
func (s *Service) HotReloadFile(path string) error {
	if path == "" {
		path = s.entryPath
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", absPath, err)
	}
	newSrc := string(raw)
	newProg, err := ParseProgramFromFile(newSrc, absPath)
	if err != nil {
		return err
	}

	newFuncs, newDecls, err := buildDeclValues(newProg, newSrc)
	if err != nil {
		return err
	}

	// Stage any imported local file modules that already exist on the running evaluator.
	type modSwap struct {
		ev    *Evaluator
		funcs map[string]*FuncDecl
		decls map[string]Value
	}
	var modSwaps []modSwap
	for _, imp := range newProg.Imports {
		if !imp.IsFileImport || imp.ModuleProg == nil {
			continue
		}
		alias := imp.Alias
		if alias == "" {
			alias = imp.Name
		}
		if modEv, ok := s.ev.modules[alias]; ok && modEv != nil {
			mf, md, err := buildDeclValues(imp.ModuleProg, imp.ModuleSource)
			if err != nil {
				return err
			}
			modSwaps = append(modSwaps, modSwap{ev: modEv, funcs: mf, decls: md})
		}
	}

	// Atomically commit new declarations after all parsing, typechecking, and validation succeed.
	s.ev.global.SwapDecls(newFuncs, newDecls)
	for _, ms := range modSwaps {
		ms.ev.global.SwapDecls(ms.funcs, ms.decls)
	}

	s.mu.Lock()
	s.latestSrc = newSrc
	s.latestProg = newProg
	s.debugFuncs = make(map[string]*FuncDecl)
	s.watchedStamps = collectWatchedStamps(absPath, newProg)
	s.mu.Unlock()
	return nil
}

// ShutdownService closes listeners and debug port, waits up to `timeout` for workers, and frees resources.
func (s *Service) ShutdownService(timeout time.Duration) error {
	if s == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	s.shutOnce.Do(func() {
		close(s.stopWatch)
		s.watchWg.Wait()
		if s.debugSrv != nil {
			s.debugSrv.close()
		}
		if s.ev != nil && s.ev.rt != nil {
			s.ev.rt.abort()
			done := make(chan struct{})
			go func() {
				s.ev.rt.wg.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(timeout):
			}
		}
		if s.ev != nil && s.ev.httpClient != nil {
			s.ev.httpClient.CloseIdleConnections()
		}
	})
	return nil
}

// Listeners returns active HTTP/TCP listeners registered in this service.
func (s *Service) Listeners() []ListenerInfo {
	if s == nil || s.ev == nil || s.ev.rt == nil {
		return nil
	}
	return s.ev.rt.ListListeners()
}

// ActiveWorkers returns the number of currently running background `spawn` workers.
func (s *Service) ActiveWorkers() int64 {
	if s == nil || s.ev == nil || s.ev.rt == nil {
		return 0
	}
	return s.ev.rt.ActiveWorkerCount()
}

// Inboxes returns snapshots of all top-level inbox variables.
func (s *Service) Inboxes() []InboxStatus {
	if s == nil || s.ev == nil || s.ev.global == nil {
		return nil
	}
	return s.ev.global.SnapshotInboxes()
}

// Funcs returns sorted function declarations currently active on the root Env.
func (s *Service) Funcs() []*FuncDecl {
	if s == nil || s.ev == nil || s.ev.global == nil {
		return nil
	}
	return s.ev.global.ListFuncs()
}

// DebugAddr returns the bound loopback address of the opt-in debug server, or "" if disabled.
func (s *Service) DebugAddr() string {
	if s == nil || s.debugSrv == nil {
		return ""
	}
	return s.debugSrv.addr
}

// DebugPort returns the bound TCP port of the opt-in debug server, or 0 if disabled.
func (s *Service) DebugPort() int {
	if s == nil || s.debugSrv == nil {
		return 0
	}
	return s.debugSrv.port
}

func normalizeDebugChunk(chunk string) string {
	return strings.ReplaceAll(chunk, "\r\n", "\n")
}

func chunkFuncHeaderNames(chunk string) map[string]bool {
	names := make(map[string]bool)
	for _, line := range strings.Split(chunk, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "to ") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				names[fields[1]] = true
			}
		}
	}
	return names
}

func formatExprStub(e Expr) string {
	switch ex := e.(type) {
	case *StringExpr:
		return strconv.Quote(ex.Value)
	case *NumberExpr:
		return strconv.FormatFloat(ex.Value, 'f', -1, 64)
	case *IdentExpr:
		return ex.Name
	case *FieldExpr:
		return formatExprStub(ex.Target) + "." + ex.Field
	default:
		return `""`
	}
}

func valueToTypedStub(val Value) string {
	switch v := val.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return strconv.Quote(v)
	case *InboxValue:
		return "inbox as " + v.ElemType
	case *ListValue:
		if v.ElemType != "" && v.ElemType != "any" {
			return "list as " + v.ElemType
		}
		if len(v.Elements) > 0 {
			switch el := v.Elements[0].(type) {
			case float64:
				return "list as number"
			case string:
				return "list as text"
			case *RecordValue:
				if el.TypeName != "" {
					return "list as " + el.TypeName
				}
			}
		}
		return `'[]' as json`
	case *MapValue:
		return `'{}' as json`
	case *RecordValue:
		return v.TypeName
	case *UnionValue:
		if len(v.Values) > 0 {
			return valueToTypedStub(v.Values[0])
		}
		return `""`
	default:
		return `""`
	}
}

func (ev *Evaluator) BuildReplPreamble(declsSrc string) string {
	var b strings.Builder
	if declsSrc != "" {
		b.WriteString(declsSrc)
		b.WriteByte('\n')
	}
	if ev == nil || ev.global == nil {
		return strings.TrimSpace(b.String())
	}

	ev.global.mu.RLock()
	names := make([]string, 0, len(ev.global.vars))
	snapshot := make(map[string]binding, len(ev.global.vars))
	for name, bnd := range ev.global.vars {
		names = append(names, name)
		snapshot[name] = bnd
	}
	ev.global.mu.RUnlock()

	sort.Strings(names)
	for _, name := range names {
		if name == "it" || strings.Contains(name, ".") {
			continue
		}
		bnd := snapshot[name]
		switch v := bnd.val.(type) {
		case *RecordValue:
			if v.IsPrototype {
				continue
			}
		case *UnionValue, *ModuleValue:
			continue
		}
		stub := valueToTypedStub(bnd.val)
		if !bnd.mutable {
			fmt.Fprintf(&b, "def %s = %s\n", name, stub)
		} else {
			fmt.Fprintf(&b, "%s = %s\n", name, stub)
		}
	}
	return strings.TrimSpace(b.String())
}

func (s *Service) buildDebugPreamble(chunkFuncs map[string]bool) string {
	s.mu.Lock()
	prog := s.latestProg
	dbgFuncs := make(map[string]*FuncDecl, len(s.debugFuncs))
	for k, v := range s.debugFuncs {
		dbgFuncs[k] = v
	}
	s.mu.Unlock()

	var b strings.Builder
	definedNames := make(map[string]bool)
	modNames := make(map[string]bool)

	if prog != nil {
		for _, imp := range prog.Imports {
			if imp.IsFileImport {
				alias := imp.Alias
				if alias == "" {
					alias = imp.Name
				}
				modNames[alias] = true
				fmt.Fprintf(&b, "import %q as %s\n", imp.Path, alias)
			} else if imp.Alias != "" && imp.Alias != imp.Name {
				modNames[imp.Alias] = true
				fmt.Fprintf(&b, "import %s as %s\n", imp.Name, imp.Alias)
			} else {
				modNames[imp.Name] = true
				fmt.Fprintf(&b, "import %s\n", imp.Name)
			}
		}

		for _, u := range prog.Unions {
			if u.Imported {
				continue
			}
			definedNames[u.Name] = true
			parts := make([]string, len(u.Variants))
			for i, vr := range u.Variants {
				parts[i] = formatExprStub(vr)
			}
			fmt.Fprintf(&b, "def %s = %s\n", u.Name, strings.Join(parts, " | "))
		}

		for _, d := range prog.Defs {
			if d.Imported {
				continue
			}
			definedNames[d.Name] = true
			fmt.Fprintf(&b, "def %s\n", d.Name)
			for _, f := range d.Fields {
				if len(f.Allowed) > 0 {
					opts := make([]string, len(f.Allowed))
					for i, opt := range f.Allowed {
						opts[i] = formatExprStub(opt)
					}
					fmt.Fprintf(&b, "    %s = %s\n", f.Name, strings.Join(opts, " | "))
				} else {
					fmt.Fprintf(&b, "    %s = %s\n", f.Name, formatExprStub(f.Default))
				}
			}
			b.WriteString("end\n")
		}

		for _, fn := range prog.Functions {
			if chunkFuncs[fn.Name] || dbgFuncs[fn.Name] != nil {
				continue
			}
			params := ""
			if len(fn.Params) > 0 {
				params = " " + strings.Join(fn.Params, " ")
			}
			if stmtsHaveReturn(fn.Body) {
				fmt.Fprintf(&b, "to %s%s\n  -> 0\nend\n", fn.Name, params)
			} else {
				fmt.Fprintf(&b, "to %s%s\n  say \"\"\nend\n", fn.Name, params)
			}
		}
	}

	var dbgNames []string
	for name := range dbgFuncs {
		if !chunkFuncs[name] {
			dbgNames = append(dbgNames, name)
		}
	}
	sort.Strings(dbgNames)
	for _, name := range dbgNames {
		fn := dbgFuncs[name]
		params := ""
		if len(fn.Params) > 0 {
			params = " " + strings.Join(fn.Params, " ")
		}
		if stmtsHaveReturn(fn.Body) {
			fmt.Fprintf(&b, "to %s%s\n  -> 0\nend\n", fn.Name, params)
		} else {
			fmt.Fprintf(&b, "to %s%s\n  say \"\"\nend\n", fn.Name, params)
		}
	}

	bindings := s.ev.global.SnapshotBindings()
	var varNames []string
	for name, val := range bindings {
		if definedNames[name] || modNames[name] {
			continue
		}
		if _, isMod := val.(*ModuleValue); isMod {
			continue
		}
		if rv, isRec := val.(*RecordValue); isRec && rv != nil && rv.IsPrototype {
			continue
		}
		if _, isUnion := val.(*UnionValue); isUnion {
			continue
		}
		if _, isFunc := s.ev.global.LookupFunc(name); isFunc {
			continue
		}
		varNames = append(varNames, name)
	}
	sort.Strings(varNames)
	for _, name := range varNames {
		fmt.Fprintf(&b, "%s = %s\n", name, valueToTypedStub(bindings[name]))
	}

	return b.String()
}

// EvalDebugChunk parses, typechecks, and runs an Ease snippet or live `to` definition against the running service.
func (s *Service) EvalDebugChunk(chunk string) (string, error) {
	norm := normalizeDebugChunk(chunk)
	chunkFuncs := chunkFuncHeaderNames(norm)
	preamble := s.buildDebugPreamble(chunkFuncs)

	preProg, err := ParseProgramFromFile(preamble, s.entryPath)
	if err != nil {
		return "", err
	}

	fullSrc := preamble + norm + "\n"
	fullProg, err := ParseProgramFromFileForRepl(fullSrc, s.entryPath)
	if err != nil {
		return "", err
	}

	newDecls := make(map[string]Value)
	if len(fullProg.Defs) > len(preProg.Defs) || len(fullProg.Unions) > len(preProg.Unions) {
		_, stagedDecls, err := buildDeclValues(fullProg, fullSrc)
		if err != nil {
			return "", err
		}
		for _, d := range fullProg.Defs[len(preProg.Defs):] {
			if v, ok := stagedDecls[d.Name]; ok {
				newDecls[d.Name] = v
			}
		}
		for _, u := range fullProg.Unions[len(preProg.Unions):] {
			if v, ok := stagedDecls[u.Name]; ok {
				newDecls[u.Name] = v
			}
		}
	}

	newFuncs := fullProg.Functions[len(preProg.Functions):]
	if len(newFuncs) > 0 {
		s.mu.Lock()
		for _, fn := range newFuncs {
			s.ev.global.SetFunc(fn.Name, fn)
			s.debugFuncs[fn.Name] = fn
		}
		s.mu.Unlock()
	}
	if len(newDecls) > 0 {
		s.ev.global.mu.Lock()
		for k, v := range newDecls {
			s.ev.global.vars[k] = binding{val: v, mutable: false}
		}
		s.ev.global.mu.Unlock()
	}

	newStmts := fullProg.Body[len(preProg.Body):]
	var outBuf bytes.Buffer
	runner := &Evaluator{
		source:         fullSrc,
		out:            &outBuf,
		ioMu:           &sync.Mutex{},
		rt:             s.ev.rt,
		isChild:        true,
		inReader:       s.ev.inReader,
		modules:        s.ev.modules,
		global:         s.ev.global,
		headlessScreen: s.ev.headlessScreen,
		headlessNet:    s.ev.headlessNet,
		headlessAudio:  s.ev.headlessAudio,
		httpClient:     s.ev.httpClient,
	}

	var lastVal Value
	if len(newStmts) > 0 {
		lastVal, err = runner.ExecTopLevel(newStmts)
		if err != nil {
			return "", err
		}
		if lastVal != nil {
			if _, isSay := newStmts[len(newStmts)-1].(*SayStmt); !isSay {
				lastLine, lastCol := newStmts[len(newStmts)-1].Pos()
				if condErr := CheckNotConditionValue(lastVal, lastLine, lastCol, fullSrc); condErr != nil {
					return "", condErr
				}
				fmt.Fprintln(&outBuf, FormatValue(lastVal))
			}
		}
	}

	resText := strings.TrimRight(outBuf.String(), "\r\n")
	if resText == "" && (len(newFuncs) > 0 || len(newDecls) > 0) {
		var names []string
		for _, fn := range newFuncs {
			names = append(names, "to "+fn.Name)
		}
		for k := range newDecls {
			names = append(names, "def "+k)
		}
		resText = "ok: updated " + strings.Join(names, ", ")
	}
	return resText, nil
}
