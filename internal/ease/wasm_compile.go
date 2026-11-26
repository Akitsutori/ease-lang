package ease

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type wasmValKind int

const (
	wkVoid  wasmValKind = iota
	wkNum               // f64
	wkText              // i64 (packed ptr<<32 | len)
	wkCond              // i32 (0 or 1)
	wkRec               // i32 ptr to struct
	wkList              // i32 ptr to list header
	wkUnion             // i32 ptr to [tag: i32, pad: i32, payload: i64]
)

type wasmTypeInfo struct {
	kind     wasmValKind
	name     string // record, union, or list elem type
	elemKind wasmValKind
}

func (wt wasmTypeInfo) wasmStorageType() byte {
	switch wt.kind {
	case wkNum:
		return wasmF64
	case wkText:
		return wasmI64
	case wkCond, wkRec, wkList, wkUnion:
		return wasmI32
	default:
		return wasmI64
	}
}

type wasmRecordField struct {
	name   string
	offset uint32
	typ    wasmTypeInfo
	def    *DefField
}

type wasmRecordLayout struct {
	name   string
	size   uint32
	fields []wasmRecordField
	byName map[string]wasmRecordField
	decl   *DefDecl
}

type wasmUnionVariant struct {
	tag  int32
	kind wasmValKind
	name string // record name or base type
	lit  string
	num  float64
}

type wasmUnionLayout struct {
	name     string
	variants []wasmUnionVariant
}

type wasmFuncInfo struct {
	key        string // "fn" or "mod:fn"
	modAlias   string
	decl       *FuncDecl
	fnIdx      uint32
	paramTypes []wasmTypeInfo
	retType    wasmTypeInfo
}

type wasmVarSlot struct {
	isGlobal bool
	idx      uint32
	typ      wasmTypeInfo
}

type wasmCompiler struct {
	prog          *Program
	mod           *wasmModule
	rt            *wasmRuntime
	records       map[string]*wasmRecordLayout
	unions        map[string]*wasmUnionLayout
	unionDecls    map[string]*UnionDecl
	consts        map[string]*ConstDeclStmt
	sortedConsts  []*ConstDeclStmt
	emittedConsts map[string]bool
	funcs         map[string]*wasmFuncInfo
	globals       map[string]wasmVarSlot
	modules       map[string]*Program
	loopDepth     int
	breakDepth    int
	curFn         *wasmFuncInfo
	locals        map[string]wasmVarSlot
	localTypes    []byte
	nextLocal     uint32

	// Browser screen-loop state machine
	hasScreenLoop    bool
	stepBodyFn       uint32
	activeGlobal     uint32
	frameIdxGlob     uint32
	screenOpenedGlob uint32
}

// BuildWasmBundleFromFile compiles an Ease entry file and writes index.html, ease_host.js, and app.wasm into outDir.
func BuildWasmBundleFromFile(source, filePath, outDir string) error {
	wasmBytes, err := CompileProgramToWasm(source, filePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory %q: %w", outDir, err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "app.wasm"), wasmBytes, 0o644); err != nil {
		return fmt.Errorf("failed to write app.wasm: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "ease_host.js"), []byte(wasmHostJS), 0o644); err != nil {
		return fmt.Errorf("failed to write ease_host.js: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "index.html"), []byte(wasmIndexHTML), 0o644); err != nil {
		return fmt.Errorf("failed to write index.html: %w", err)
	}
	return nil
}

// CompileProgramToWasm parses, validates, and compiles an Ease program directly to a WASM 1.0 binary.
func CompileProgramToWasm(source, filePath string) ([]byte, error) {
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
	if err := validateWasmTargetSupported(prog, source); err != nil {
		return nil, err
	}

	mod := newWasmModule(wasmStaticBase, 4) // 256 KB initial memory
	rt := newWasmRuntime(mod)
	wc := &wasmCompiler{
		prog:          prog,
		mod:           mod,
		rt:            rt,
		records:       make(map[string]*wasmRecordLayout),
		unions:        make(map[string]*wasmUnionLayout),
		unionDecls:    make(map[string]*UnionDecl),
		consts:        make(map[string]*ConstDeclStmt),
		emittedConsts: make(map[string]bool),
		funcs:         make(map[string]*wasmFuncInfo),
		globals:       make(map[string]wasmVarSlot),
		modules:       make(map[string]*Program),
	}

	if err := wc.compile(); err != nil {
		return nil, err
	}
	return mod.emit(), nil
}

func validateWasmTargetSupported(prog *Program, source string) error {
	for _, imp := range prog.Imports {
		if !imp.IsFileImport {
			switch imp.Name {
			case "network", "system":
				return newError(
					"CompileError",
					imp.Line,
					imp.Col,
					source,
					fmt.Sprintf("Module %q is not supported for '--target wasm'", imp.Name),
					"Browser WASM builds support 'screen', 'random', 'synth', and local './file.ease' modules.",
				)
			}
		} else if imp.ModuleProg != nil {
			if err := validateWasmTargetSupported(imp.ModuleProg, imp.ModuleSource); err != nil {
				return err
			}
		}
	}
	var unsupportedErr error
	walkAllExprs(prog, func(e Expr) {
		if unsupportedErr != nil {
			return
		}
		if ce, ok := e.(*CallExpr); ok && ce.Module == "" && (ce.Callee == "load" || ce.Callee == "save") {
			unsupportedErr = newError(
				"CompileError",
				ce.Line,
				ce.Col,
				source,
				fmt.Sprintf("'%s' file I/O is not supported for '--target wasm'", ce.Callee),
				"Remove local filesystem 'load'/'save' calls when targeting browser WASM.",
			)
		}
	})
	return unsupportedErr
}

func (wc *wasmCompiler) compile() error {
	wc.screenOpenedGlob = wc.mod.addGlobal(wasmI32, true, opI32Const, 0)
	wc.collectDecls()
	wc.rt.emitAllBodies()
	wc.emitPresentSceneHelper()

	if err := wc.compileFunctions(); err != nil {
		return err
	}
	if err := wc.compileEntryPoints(); err != nil {
		return err
	}

	// Set initial heapPtr right after static data.
	heapBase := wc.mod.heapBase()
	wc.mod.globals[wc.rt.heapPtrGlobal].initVal = int64(heapBase)
	wc.mod.globals[wc.rt.frameMarkGlobal].initVal = int64(heapBase)
	return nil
}

func (wc *wasmCompiler) collectDecls() {
	for _, imp := range wc.prog.Imports {
		alias := imp.Alias
		if alias == "" {
			alias = imp.Name
		}
		if imp.ModuleProg != nil {
			wc.modules[alias] = imp.ModuleProg
			for _, u := range imp.ModuleProg.Unions {
				wc.registerUnion(alias+"."+u.Name, u, alias+".")
			}
			for _, d := range imp.ModuleProg.Defs {
				wc.registerDef(alias+"."+d.Name, d)
			}
			for _, fn := range imp.ModuleProg.Functions {
				if fn.Name == "open" || fn.Name == "present" || fn.Name == "clear" || fn.Name == "draw" || fn.Name == "show" || fn.Name == "roll" || fn.Name == "pick" || fn.Name == "shuffle" || fn.Name == "play" || fn.Name == "stop" {
					if !imp.IsFileImport {
						continue
					}
				}
				key := alias + ":" + fn.Name
				wc.funcs[key] = &wasmFuncInfo{key: key, modAlias: alias, decl: fn}
			}
		}
	}
	for _, u := range wc.prog.Unions {
		if !u.Imported {
			wc.registerUnion(u.Name, u, "")
		}
	}
	for _, d := range wc.prog.Defs {
		if !d.Imported {
			wc.registerDef(d.Name, d)
		}
	}
	for _, st := range wc.prog.Body {
		if cs, ok := st.(*ConstDeclStmt); ok {
			wc.consts[cs.Name] = cs
		}
	}
	if sorted, err := toposortConsts(wc.prog.Body, ""); err == nil {
		wc.sortedConsts = sorted
		for _, cs := range sorted {
			wt := wc.inferExprTypeWithEnv(cs.Value, wc.curEnvTypes(), "")
			if wt.kind == wkVoid {
				wt = wasmTypeInfo{kind: wkNum}
			}
			wc.ensureVar(cs.Name, wt)
		}
	}
	for _, fn := range wc.prog.Functions {
		wc.funcs[fn.Name] = &wasmFuncInfo{key: fn.Name, decl: fn}
	}

	// Sort function keys so signature inference and WASM function indices are 100% deterministic.
	funcKeys := make([]string, 0, len(wc.funcs))
	for k := range wc.funcs {
		funcKeys = append(funcKeys, k)
	}
	sort.Strings(funcKeys)

	// Multi-pass type inference without prematurely defaulting unknown params to f64,
	// so transitive callers (e.g. run_pacman -> ghost_step_x -> is_wall(maze)) propagate wkList cleanly.
	for pass := 0; pass < 4; pass++ {
		for _, k := range funcKeys {
			wc.inferFuncSig(wc.funcs[k], false)
		}
	}
	for _, k := range funcKeys {
		fi := wc.funcs[k]
		wc.inferFuncSig(fi, true)
		var pBytes []byte
		for _, pt := range fi.paramTypes {
			pBytes = append(pBytes, pt.wasmStorageType())
		}
		var rBytes []byte
		if fi.retType.kind != wkVoid {
			rBytes = []byte{fi.retType.wasmStorageType()}
		}
		fi.fnIdx = wc.mod.reserveFunc(pBytes, rBytes)
	}
}

func (wc *wasmCompiler) registerDef(name string, d *DefDecl) {
	if _, exists := wc.records[name]; exists {
		return
	}
	lay := &wasmRecordLayout{
		name:   name,
		byName: make(map[string]wasmRecordField, len(d.Fields)),
		decl:   d,
	}
	var off uint32
	for i := range d.Fields {
		f := &d.Fields[i]
		ft := wc.inferStaticExprType(f.Default, "")
		if ft.kind == wkUnion {
			if ul := wc.unions[ft.name]; ul != nil && len(ul.variants) > 0 {
				allText, allNum := true, true
				for _, uv := range ul.variants {
					if uv.kind != wkText {
						allText = false
					}
					if uv.kind != wkNum {
						allNum = false
					}
				}
				if allText {
					ft = wasmTypeInfo{kind: wkText}
				} else if allNum {
					ft = wasmTypeInfo{kind: wkNum}
				}
			}
		} else if ft.kind == wkVoid {
			if id, ok := f.Default.(*IdentExpr); ok {
				if dot := strings.LastIndex(name, "."); dot > 0 {
					qualU := name[:dot+1] + id.Name
					if wc.unions[qualU] != nil {
						ft = wasmTypeInfo{kind: wkText}
					}
				} else if wc.unions[id.Name] != nil {
					ft = wasmTypeInfo{kind: wkText}
				}
			}
		}
		if len(f.Allowed) > 0 {
			ft = wasmTypeInfo{kind: wkText}
		}
		rf := wasmRecordField{name: f.Name, offset: off, typ: ft, def: f}
		lay.fields = append(lay.fields, rf)
		lay.byName[f.Name] = rf
		off += 8
	}
	if off < 8 {
		off = 8
	}
	lay.size = off
	wc.records[name] = lay
}

func (wc *wasmCompiler) registerUnion(name string, u *UnionDecl, prefix string) {
	if _, exists := wc.unions[name]; exists {
		return
	}
	wc.unionDecls[name] = u
	ul := &wasmUnionLayout{name: name}
	for i, ve := range u.Variants {
		tag := int32(i + 1)
		switch v := ve.(type) {
		case *StringExpr:
			ul.variants = append(ul.variants, wasmUnionVariant{tag: tag, kind: wkText, lit: v.Value})
		case *NumberExpr:
			ul.variants = append(ul.variants, wasmUnionVariant{tag: tag, kind: wkNum, num: v.Value})
		case *IdentExpr:
			if v.Name == "text" {
				ul.variants = append(ul.variants, wasmUnionVariant{tag: tag, kind: wkText, name: "text"})
			} else if v.Name == "number" {
				ul.variants = append(ul.variants, wasmUnionVariant{tag: tag, kind: wkNum, name: "number"})
			} else {
				recName := prefix + v.Name
				if prefix != "" && wc.records[recName] == nil && wc.records[v.Name] != nil {
					recName = v.Name
				}
				ul.variants = append(ul.variants, wasmUnionVariant{tag: tag, kind: wkRec, name: recName})
			}
		case *FieldExpr:
			if id, ok := v.Target.(*IdentExpr); ok {
				ul.variants = append(ul.variants, wasmUnionVariant{tag: tag, kind: wkRec, name: id.Name + "." + v.Field})
			}
		}
	}
	wc.unions[name] = ul
}

func (wc *wasmCompiler) inferFuncSig(fi *wasmFuncInfo, finalize bool) {
	// Infer parameter types from call sites; keep unknown params as wkVoid until finalize
	// so early passes never poison transitive callers with a premature wkNum fallback.
	if len(fi.paramTypes) != len(fi.decl.Params) {
		fi.paramTypes = make([]wasmTypeInfo, len(fi.decl.Params))
	}
	for i, p := range fi.decl.Params {
		if t := wc.guessParamType(fi.decl, p); t.kind != wkVoid {
			fi.paramTypes[i] = t
		} else if finalize && fi.paramTypes[i].kind == wkVoid {
			fi.paramTypes[i] = wasmTypeInfo{kind: wkNum}
		}
	}
	if fi.decl.ReturnType != "" {
		qualify := func(name string) string {
			if fi.modAlias != "" && name != "number" && name != "text" && !strings.Contains(name, ".") {
				q := fi.modAlias + "." + name
				if wc.records[q] != nil || wc.unions[q] != nil {
					return q
				}
			}
			return name
		}
		if fi.decl.ReturnElemType != "" {
			elemName := qualify(fi.decl.ReturnElemType)
			fi.retType = wasmTypeInfo{kind: wkList, name: elemName, elemKind: wc.typeKindForName(elemName)}
			return
		}
		retName := qualify(fi.decl.ReturnType)
		switch retName {
		case "number":
			fi.retType = wasmTypeInfo{kind: wkNum}
		case "text":
			fi.retType = wasmTypeInfo{kind: wkText}
		default:
			if wc.records[retName] != nil {
				fi.retType = wasmTypeInfo{kind: wkRec, name: retName}
			} else if wc.unions[retName] != nil {
				fi.retType = wc.unionWasmType(retName)
			} else {
				fi.retType = wasmTypeInfo{kind: wc.typeKindForName(retName), name: retName}
			}
		}
		return
	}
	if !stmtsHaveReturn(fi.decl.Body) {
		fi.retType = wasmTypeInfo{kind: wkVoid}
		return
	}
	// Infer return type from first ReturnStmt in body.
	retExpr := findFirstReturnExpr(fi.decl.Body)
	if retExpr == nil {
		fi.retType = wasmTypeInfo{kind: wkVoid}
		return
	}
	envTypes := make(map[string]wasmTypeInfo)
	for i, p := range fi.decl.Params {
		if fi.paramTypes[i].kind != wkVoid {
			envTypes[p] = fi.paramTypes[i]
		}
	}
	wc.collectLocalTypes(fi.decl.Body, envTypes, fi.modAlias)
	fi.retType = wc.inferExprTypeWithEnv(retExpr, envTypes, fi.modAlias)
	if finalize && fi.retType.kind == wkVoid {
		fi.retType = wasmTypeInfo{kind: wkNum}
	}
}

func (wc *wasmCompiler) collectLocalTypes(stmts []Stmt, env map[string]wasmTypeInfo, modPrefix string) {
	for _, st := range stmts {
		switch s := st.(type) {
		case *AssignStmt:
			if t := wc.inferExprTypeWithEnv(s.Value, env, modPrefix); t.kind != wkVoid {
				env[s.Name] = t
			}
		case *ConstDeclStmt:
			if t := wc.inferExprTypeWithEnv(s.Value, env, modPrefix); t.kind != wkVoid {
				env[s.Name] = t
			}
		case *LoopStmt:
			wc.collectLocalTypes(s.Body, env, modPrefix)
		case *TakeStmt:
			wc.collectLocalTypes(s.Body, env, modPrefix)
		case *CheckGuardStmt:
			wc.collectLocalTypes(s.Body, env, modPrefix)
			wc.collectLocalTypes(s.ElseBody, env, modPrefix)
		}
	}
}

func findFirstReturnExpr(stmts []Stmt) Expr {
	for _, st := range stmts {
		switch s := st.(type) {
		case *ReturnStmt:
			return s.Value
		case *CheckGuardStmt:
			for _, r := range s.Rules {
				if r.Action != nil {
					if e := findFirstReturnExpr([]Stmt{r.Action}); e != nil {
						return e
					}
				}
			}
			if e := findFirstReturnExpr(s.Body); e != nil {
				return e
			}
			if e := findFirstReturnExpr(s.ElseBody); e != nil {
				return e
			}
		}
	}
	return nil
}

func (wc *wasmCompiler) guessParamType(fn *FuncDecl, param string) wasmTypeInfo {
	// Check call sites across program (including caller local variable types) for argument types first.
	var guessed wasmTypeInfo
	checkStmts := func(stmts []Stmt, env map[string]wasmTypeInfo) {
		var checkExpr func(e Expr, curEnv map[string]wasmTypeInfo)
		checkExpr = func(e Expr, curEnv map[string]wasmTypeInfo) {
			if guessed.kind != wkVoid || e == nil {
				return
			}
			if pe, ok := e.(*ProjectExpr); ok {
				lt := wc.inferExprTypeWithEnv(pe.Source, curEnv, "")
				srcElem := wasmTypeInfo{kind: lt.elemKind, name: lt.name}
				if srcElem.kind != wkVoid {
					pEnv := make(map[string]wasmTypeInfo, len(curEnv)+2)
					for k, v := range curEnv {
						pEnv[k] = v
					}
					pEnv["it"] = srcElem
					pEnv["it.index"] = wasmTypeInfo{kind: wkNum}
					walkExprTree(pe.Body, func(sub Expr) {
						if guessed.kind == wkVoid {
							checkExpr(sub, pEnv)
						}
					})
				}
				return
			}
			if ce, ok := e.(*CallExpr); ok && ce.Callee == fn.Name {
				for i, p := range fn.Params {
					if p == param && i < len(ce.Args) {
						t := wc.inferExprTypeWithEnv(ce.Args[i], curEnv, "")
						if t.kind != wkVoid {
							guessed = t
						}
					}
				}
			}
		}
		walkStmtsAndExprs(stmts, nil, func(e Expr) {
			checkExpr(e, env)
		})
	}
	topEnv := make(map[string]wasmTypeInfo)
	wc.collectLocalTypes(wc.prog.Body, topEnv, "")
	checkStmts(wc.prog.Body, topEnv)
	for _, caller := range wc.prog.Functions {
		if guessed.kind != wkVoid {
			break
		}
		cEnv := make(map[string]wasmTypeInfo)
		if cfi := wc.funcs[caller.Name]; cfi != nil {
			for i, cp := range caller.Params {
				if i < len(cfi.paramTypes) && cfi.paramTypes[i].kind != wkVoid {
					cEnv[cp] = cfi.paramTypes[i]
				}
			}
		}
		wc.collectLocalTypes(caller.Body, cEnv, "")
		checkStmts(caller.Body, cEnv)
	}
	if guessed.kind != wkVoid {
		return guessed
	}
	return wasmTypeInfo{kind: wkVoid}
}

func (wc *wasmCompiler) inferStaticExprType(e Expr, modPrefix string) wasmTypeInfo {
	return wc.inferExprTypeWithEnv(e, nil, modPrefix)
}

func (wc *wasmCompiler) closedLiteralUnionVariants(recName, fieldName string) ([]wasmUnionVariant, bool) {
	lay := wc.records[recName]
	if lay == nil {
		return nil, false
	}
	rf, ok := lay.byName[fieldName]
	if !ok || rf.def == nil {
		return nil, false
	}

	modPrefix := ""
	if dot := strings.LastIndex(recName, "."); dot > 0 {
		modPrefix = recName[:dot+1]
	}

	visiting := make(map[string]bool)
	var expandExprs func(exprs []Expr, pfx string) ([]wasmUnionVariant, bool)
	expandExprs = func(exprs []Expr, pfx string) ([]wasmUnionVariant, bool) {
		var out []wasmUnionVariant
		for _, ve := range exprs {
			switch ex := ve.(type) {
			case *StringExpr:
				out = append(out, wasmUnionVariant{kind: wkText, lit: ex.Value})
			case *NumberExpr:
				out = append(out, wasmUnionVariant{kind: wkNum, num: ex.Value})
			case *UnaryExpr:
				if ex.Op == "-" {
					if n, isNum := ex.Right.(*NumberExpr); isNum {
						out = append(out, wasmUnionVariant{kind: wkNum, num: -n.Value})
						continue
					}
				}
				return nil, false
			case *IdentExpr:
				if ex.Name == "text" || ex.Name == "number" {
					return nil, false
				}
				uName := pfx + ex.Name
				ud := wc.unionDecls[uName]
				if ud == nil {
					uName = ex.Name
					ud = wc.unionDecls[uName]
				}
				if ud == nil || visiting[uName] {
					return nil, false
				}
				visiting[uName] = true
				subPfx := ""
				if dot := strings.LastIndex(uName, "."); dot > 0 {
					subPfx = uName[:dot+1]
				}
				sub, ok := expandExprs(ud.Variants, subPfx)
				delete(visiting, uName)
				if !ok {
					return nil, false
				}
				out = append(out, sub...)
			case *FieldExpr:
				id, ok := ex.Target.(*IdentExpr)
				if !ok {
					return nil, false
				}
				uName := id.Name + "." + ex.Field
				ud := wc.unionDecls[uName]
				if ud == nil || visiting[uName] {
					return nil, false
				}
				visiting[uName] = true
				sub, ok := expandExprs(ud.Variants, id.Name+".")
				delete(visiting, uName)
				if !ok {
					return nil, false
				}
				out = append(out, sub...)
			default:
				return nil, false
			}
		}
		return out, len(out) > 0
	}

	if len(rf.def.Allowed) > 0 {
		return expandExprs(rf.def.Allowed, modPrefix)
	}
	if rf.def.Default != nil {
		switch rf.def.Default.(type) {
		case *IdentExpr, *FieldExpr:
			return expandExprs([]Expr{rf.def.Default}, modPrefix)
		}
	}
	return nil, false
}

func (wc *wasmCompiler) inferExprTypeWithEnv(e Expr, env map[string]wasmTypeInfo, modPrefix string) wasmTypeInfo {
	if e == nil {
		return wasmTypeInfo{kind: wkVoid}
	}
	switch ex := e.(type) {
	case *NumberExpr:
		return wasmTypeInfo{kind: wkNum}
	case *StringExpr, *InterpStringExpr:
		return wasmTypeInfo{kind: wkText}
	case *IdentExpr:
		if env != nil {
			if t, ok := env[ex.Name]; ok {
				return t
			}
		}
		if g, ok := wc.globals[ex.Name]; ok {
			return g.typ
		}
		if modPrefix != "" && wc.records[modPrefix+"."+ex.Name] != nil {
			return wasmTypeInfo{kind: wkRec, name: modPrefix + "." + ex.Name}
		}
		if wc.records[ex.Name] != nil {
			return wasmTypeInfo{kind: wkRec, name: ex.Name}
		}
		if modPrefix != "" && wc.unions[modPrefix+"."+ex.Name] != nil {
			return wasmTypeInfo{kind: wkUnion, name: modPrefix + "." + ex.Name}
		}
		if wc.unions[ex.Name] != nil {
			return wasmTypeInfo{kind: wkUnion, name: ex.Name}
		}
		if fi := wc.funcs[ex.Name]; fi != nil && len(fi.decl.Params) == 0 {
			return fi.retType
		}
	case *FieldExpr:
		if id, ok := ex.Target.(*IdentExpr); ok {
			if wc.modules[id.Name] != nil {
				qual := id.Name + "." + ex.Field
				if wc.records[qual] != nil {
					return wasmTypeInfo{kind: wkRec, name: qual}
				}
				if wc.unions[qual] != nil {
					return wasmTypeInfo{kind: wkUnion, name: qual}
				}
			}
		}
		t := wc.inferExprTypeWithEnv(ex.Target, env, modPrefix)
		if t.kind == wkRec {
			if lay := wc.records[t.name]; lay != nil {
				if rf, ok := lay.byName[ex.Field]; ok {
					return rf.typ
				}
			}
		}
	case *WithExpr:
		return wc.inferExprTypeWithEnv(ex.Base, env, modPrefix)
	case *TypedListExpr:
		return wasmTypeInfo{kind: wkList, name: ex.ElemType, elemKind: wc.typeKindForName(ex.ElemType)}
	case *ListExpr:
		if len(ex.Elements) > 0 {
			et := wc.inferExprTypeWithEnv(ex.Elements[0], env, modPrefix)
			return wasmTypeInfo{kind: wkList, name: et.name, elemKind: et.kind}
		}
		return wasmTypeInfo{kind: wkList, elemKind: wkNum}
	case *IndexExpr:
		lt := wc.inferExprTypeWithEnv(ex.Target, env, modPrefix)
		if lt.kind == wkList {
			return wasmTypeInfo{kind: lt.elemKind, name: lt.name}
		}
		if lt.kind == wkText {
			return wasmTypeInfo{kind: wkText}
		}
	case *CastExpr:
		if ex.TypeName == "number" {
			return wasmTypeInfo{kind: wkNum}
		}
		if ex.TypeName == "text" || ex.TypeName == "json" {
			return wasmTypeInfo{kind: wkText}
		}
		if wc.records[ex.TypeName] != nil {
			return wasmTypeInfo{kind: wkRec, name: ex.TypeName}
		}
		if wc.unions[ex.TypeName] != nil {
			return wc.unionWasmType(ex.TypeName)
		}
	case *BinaryExpr:
		switch ex.Op {
		case ">", "<", ">=", "<=", "is", "is not", "is-not", "and", "or":
			return wasmTypeInfo{kind: wkCond}
		case "+":
			lt := wc.inferExprTypeWithEnv(ex.Left, env, modPrefix)
			if lt.kind == wkText {
				return wasmTypeInfo{kind: wkText}
			}
			return wasmTypeInfo{kind: wkNum}
		default:
			return wasmTypeInfo{kind: wkNum}
		}
	case *UnaryExpr:
		if ex.Op == "not" {
			return wasmTypeInfo{kind: wkCond}
		}
		return wasmTypeInfo{kind: wkNum}
	case *TypeCheckExpr:
		return wasmTypeInfo{kind: wkCond}
	case *InboxExpr:
		return wasmTypeInfo{kind: wkList, name: ex.ElemType}
	case *BoolExpr:
		return wasmTypeInfo{kind: wkCond}
	case *QuantifierExpr:
		return wasmTypeInfo{kind: wkCond}
	case *WhereExpr:
		return wc.inferExprTypeWithEnv(ex.Target, env, modPrefix)
	case *ProjectExpr:
		lt := wc.inferExprTypeWithEnv(ex.Source, env, modPrefix)
		srcElem := wasmTypeInfo{kind: lt.elemKind, name: lt.name}
		if srcElem.kind == wkList && strings.HasPrefix(lt.name, "list<") && strings.HasSuffix(lt.name, ">") {
			inner := lt.name[5 : len(lt.name)-1]
			srcElem = wasmTypeInfo{kind: wkList, name: inner, elemKind: wc.typeKindForName(inner)}
		}
		if srcElem.kind == wkVoid {
			srcElem.kind = wkNum
		}
		projEnv := make(map[string]wasmTypeInfo, len(env)+2)
		for k, v := range env {
			projEnv[k] = v
		}
		projEnv["it"] = srcElem
		projEnv["it.index"] = wasmTypeInfo{kind: wkNum}
		bt := wc.inferExprTypeWithEnv(ex.Body, projEnv, modPrefix)
		if bt.kind == wkVoid {
			bt.kind = wkNum
		}
		outName := bt.name
		if bt.kind == wkList {
			outName = "list<" + bt.name + ">"
		}
		return wasmTypeInfo{kind: wkList, name: outName, elemKind: bt.kind}
	case *UpdateExpr:
		return wc.inferExprTypeWithEnv(ex.Target, env, modPrefix)
	case *SortByExpr:
		return wc.inferExprTypeWithEnv(ex.Target, env, modPrefix)
	case *CallExpr:
		if ex.Module != "" {
			if fi := wc.funcs[ex.Module+":"+ex.Callee]; fi != nil {
				return fi.retType
			}
			if ex.Module == "screen" && (ex.Callee == "open" || ex.Callee == "clear" || ex.Callee == "draw") {
				return wasmTypeInfo{kind: wkVoid}
			}
			if ex.Module == "screen" && ex.Callee == "present" {
				return wasmTypeInfo{kind: wkText}
			}
			if ex.Module == "screen" && ex.Callee == "show" {
				return wasmTypeInfo{kind: wkRec, name: "screen.frame_state"}
			}
			if ex.Module == "synth" && (ex.Callee == "play" || ex.Callee == "stop") {
				return wasmTypeInfo{kind: wkVoid}
			}
			if ex.Module == "random" {
				if ex.Callee == "roll" {
					return wasmTypeInfo{kind: wkNum}
				}
				if ex.Callee == "pick" && len(ex.Args) >= 1 {
					lt := wc.inferExprTypeWithEnv(ex.Args[0], env, modPrefix)
					return wasmTypeInfo{kind: lt.elemKind, name: lt.name}
				}
				if ex.Callee == "shuffle" && len(ex.Args) >= 1 {
					return wc.inferExprTypeWithEnv(ex.Args[0], env, modPrefix)
				}
			}
			return wasmTypeInfo{kind: wkNum}
		}
		switch ex.Callee {
		case "trim", "upper", "lower", "replace", "join", "slice":
			if ex.Callee == "slice" && len(ex.Args) >= 1 {
				return wc.inferExprTypeWithEnv(ex.Args[0], env, modPrefix)
			}
			return wasmTypeInfo{kind: wkText}
		case "split":
			return wasmTypeInfo{kind: wkList, name: "text", elemKind: wkText}
		case "range":
			return wasmTypeInfo{kind: wkList, name: "number", elemKind: wkNum}
		case "append", "sort":
			if len(ex.Args) >= 1 {
				if t := wc.inferExprTypeWithEnv(ex.Args[0], env, modPrefix); t.kind != wkVoid {
					return t
				}
				return wasmTypeInfo{kind: wkList, elemKind: wkText}
			}
		case "count", "sum", "avg", "abs", "floor", "round", "min", "max":
			return wasmTypeInfo{kind: wkNum}
		case "contains":
			return wasmTypeInfo{kind: wkCond}
		case "close":
			return wasmTypeInfo{kind: wkVoid}
		default:
			if fi := wc.funcs[ex.Callee]; fi != nil {
				return fi.retType
			}
		}
	}
	return wasmTypeInfo{kind: wkVoid}
}

func (wc *wasmCompiler) typeKindForName(name string) wasmValKind {
	switch name {
	case "number":
		return wkNum
	case "text":
		return wkText
	default:
		if wc.records[name] != nil {
			return wkRec
		}
		if wc.unions[name] != nil {
			return wc.unionWasmType(name).kind
		}
		return wkNum
	}
}

func (wc *wasmCompiler) unionWasmType(name string) wasmTypeInfo {
	ul := wc.unions[name]
	if ul == nil || len(ul.variants) == 0 {
		return wasmTypeInfo{kind: wkUnion, name: name}
	}
	allText := true
	allNum := true
	for _, vr := range ul.variants {
		if vr.kind != wkText {
			allText = false
		}
		if vr.kind != wkNum {
			allNum = false
		}
	}
	if allText {
		return wasmTypeInfo{kind: wkText}
	}
	if allNum {
		return wasmTypeInfo{kind: wkNum}
	}
	return wasmTypeInfo{kind: wkUnion, name: name}
}

func (wc *wasmCompiler) emitPresentSceneHelper() {
	// Serializes `screen.scene` (param 0: i32 ptr) into binary Display-List format,
	// preserving heapPtr via frameMarkGlobal around env.present_scene!
	sceneLay := wc.records["screen.scene"]
	if sceneLay == nil {
		wc.mod.setFuncBody(wc.rt.fnPresentScene, nil, nil)
		return
	}
	var c wasmCodeBuf
	// locals: 1=bg(i64), 2=circles(i32), 3=boxes(i32), 4=lines(i32), 5=tris(i32), 6=labels(i32), 7=shapes(i32),
	// 8=total(i32), 9=buf(i32), 10=pos(i32), 11=i(i32), 12=n(i32), 13=item(i32), 14=data(i32), 15=tag(i32), 16=payload(i32)
	locals := []byte{
		wasmI64, wasmI32, wasmI32, wasmI32, wasmI32, wasmI32, wasmI32,
		wasmI32, wasmI32, wasmI32, wasmI32, wasmI32, wasmI32, wasmI32, wasmI32, wasmI32,
	}

	// Save frameMark = heapPtr BEFORE temporary display-list allocation
	c.globalGet(wc.rt.heapPtrGlobal)
	c.globalSet(wc.rt.frameMarkGlobal)

	loadFieldPtr := func(fieldName string, dstLocal uint32) {
		rf := sceneLay.byName[fieldName]
		c.localGet(0)
		c.memOp(opI32Load, 2, rf.offset)
		c.localSet(dstLocal)
	}
	// background: i64
	bgF := sceneLay.byName["background"]
	c.localGet(0)
	c.memOp(opI64Load, 3, bgF.offset)
	c.localSet(1)

	loadFieldPtr("circles", 2)
	loadFieldPtr("boxes", 3)
	loadFieldPtr("lines", 4)
	loadFieldPtr("triangles", 5)
	loadFieldPtr("labels", 6)
	loadFieldPtr("shapes", 7)

	// total = len(circles)+len(boxes)+len(lines)+len(triangles)+len(labels)+len(shapes)
	c.i32Const(0)
	for _, lIdx := range []uint32{2, 3, 4, 5, 6, 7} {
		c.localGet(lIdx)
		c.ifOp(wasmI32)
		c.localGet(lIdx)
		c.memOp(opI32Load, 2, 4)
		c.op(opElse)
		c.i32Const(0)
		c.op(opEnd)
		c.op(opI32Add)
	}
	c.localTee(8)

	// buf = alloc(12 + total * 64)
	c.i32Const(64)
	c.op(opI32Mul)
	c.i32Const(12)
	c.op(opI32Add)
	c.call(wc.rt.fnAlloc)
	c.localTee(9)

	// Header: bgPtr @0, bgLen @4, count @8
	c.localGet(1)
	emitUnpackPtr(&c)
	c.memOp(opI32Store, 2, 0)
	c.localGet(9)
	c.localGet(1)
	emitUnpackLen(&c)
	c.memOp(opI32Store, 2, 4)
	c.localGet(9)
	c.localGet(8)
	c.memOp(opI32Store, 2, 8)

	c.localGet(9)
	c.i32Const(12)
	c.op(opI32Add)
	c.localSet(10) // pos

	emitStoreF64Field := func(recName, fieldName string) {
		rf := wc.records[recName].byName[fieldName]
		c.localGet(10)
		c.localGet(13)
		c.memOp(opF64Load, 3, rf.offset)
		c.memOp(opF64Store, 3, 0)
		c.localGet(10)
		c.i32Const(8)
		c.op(opI32Add)
		c.localSet(10)
	}
	emitStoreStyle := func(recName string) {
		rf := wc.records[recName].byName["style"]
		solidStr := wc.mod.internString("solid")
		c.localGet(10)
		c.localGet(13)
		c.memOp(opI64Load, 3, rf.offset)
		c.i64Const(int64(solidStr))
		c.call(wc.rt.fnStrEq)
		c.memOp(opI32Store8, 0, 0)
		c.localGet(10)
		c.i32Const(1)
		c.op(opI32Add)
		c.localSet(10)
	}
	emitStoreTextField := func(recName, fieldName string) {
		rf := wc.records[recName].byName[fieldName]
		// ptr
		c.localGet(10)
		c.localGet(13)
		c.memOp(opI64Load, 3, rf.offset)
		emitUnpackPtr(&c)
		c.memOp(opI32Store, 2, 0)
		// len
		c.localGet(10)
		c.localGet(13)
		c.memOp(opI64Load, 3, rf.offset)
		emitUnpackLen(&c)
		c.memOp(opI32Store, 2, 4)
		c.localGet(10)
		c.i32Const(8)
		c.op(opI32Add)
		c.localSet(10)
	}
	emitShapeSerialize := func(opcode byte, recName string) {
		c.localGet(10)
		c.i32Const(int32(opcode))
		c.memOp(opI32Store8, 0, 0)
		c.localGet(10)
		c.i32Const(1)
		c.op(opI32Add)
		c.localSet(10)
		switch opcode {
		case 1: // circle
			emitStoreF64Field(recName, "x")
			emitStoreF64Field(recName, "y")
			emitStoreF64Field(recName, "size")
			emitStoreF64Field(recName, "stroke")
			emitStoreStyle(recName)
			emitStoreTextField(recName, "color")
		case 2: // box
			emitStoreF64Field(recName, "x")
			emitStoreF64Field(recName, "y")
			emitStoreF64Field(recName, "width")
			emitStoreF64Field(recName, "height")
			emitStoreF64Field(recName, "stroke")
			emitStoreStyle(recName)
			emitStoreTextField(recName, "color")
		case 3: // line
			emitStoreF64Field(recName, "x1")
			emitStoreF64Field(recName, "y1")
			emitStoreF64Field(recName, "x2")
			emitStoreF64Field(recName, "y2")
			emitStoreF64Field(recName, "width")
			emitStoreTextField(recName, "color")
		case 4: // triangle
			emitStoreF64Field(recName, "x1")
			emitStoreF64Field(recName, "y1")
			emitStoreF64Field(recName, "x2")
			emitStoreF64Field(recName, "y2")
			emitStoreF64Field(recName, "x3")
			emitStoreF64Field(recName, "y3")
			emitStoreF64Field(recName, "stroke")
			emitStoreStyle(recName)
			emitStoreTextField(recName, "color")
		case 5: // label
			emitStoreF64Field(recName, "x")
			emitStoreF64Field(recName, "y")
			emitStoreF64Field(recName, "size")
			emitStoreTextField(recName, "content")
			emitStoreTextField(recName, "color")
		}
	}

	emitListLoop := func(listLocal uint32, opcode byte, recName string) {
		c.localGet(listLocal)
		c.ifOp(wasmBlockVoid)
		c.localGet(listLocal)
		c.memOp(opI32Load, 2, 4)
		c.localSet(12)
		c.localGet(listLocal)
		c.memOp(opI32Load, 2, 0)
		c.localSet(14)
		c.i32Const(0)
		c.localSet(11)
		c.block(wasmBlockVoid)
		c.loop(wasmBlockVoid)
		c.localGet(11)
		c.localGet(12)
		c.op(opI32GeS)
		c.brIf(1)
		c.localGet(14)
		c.localGet(11)
		c.i32Const(8)
		c.op(opI32Mul)
		c.op(opI32Add)
		c.memOp(opI32Load, 2, 0)
		c.localSet(13)
		emitShapeSerialize(opcode, recName)
		c.localGet(11)
		c.i32Const(1)
		c.op(opI32Add)
		c.localSet(11)
		c.br(0)
		c.op(opEnd)
		c.op(opEnd)
		c.op(opEnd)
	}

	emitListLoop(2, 1, "screen.circle")
	emitListLoop(3, 2, "screen.box")
	emitListLoop(4, 3, "screen.line")
	emitListLoop(5, 4, "screen.triangle")

	// shapes: list<screen.shape> (tagged union [tag: i32 @0, payload: i32 @8])
	c.localGet(7)
	c.ifOp(wasmBlockVoid)
	c.localGet(7)
	c.memOp(opI32Load, 2, 4)
	c.localSet(12)
	c.localGet(7)
	c.memOp(opI32Load, 2, 0)
	c.localSet(14)
	c.i32Const(0)
	c.localSet(11)
	c.block(wasmBlockVoid)
	c.loop(wasmBlockVoid)
	c.localGet(11)
	c.localGet(12)
	c.op(opI32GeS)
	c.brIf(1)
	c.localGet(14)
	c.localGet(11)
	c.i32Const(8)
	c.op(opI32Mul)
	c.op(opI32Add)
	c.memOp(opI32Load, 2, 0)
	c.localTee(16)
	c.memOp(opI32Load, 2, 0)
	c.localSet(15) // tag 1..5
	c.localGet(16)
	c.memOp(opI32Load, 2, 8)
	c.localSet(13) // payload record ptr

	for idx, recName := range []string{"screen.circle", "screen.box", "screen.line", "screen.triangle", "screen.label"} {
		c.localGet(15)
		c.i32Const(int32(idx + 1))
		c.op(opI32Eq)
		c.ifOp(wasmBlockVoid)
		emitShapeSerialize(byte(idx+1), recName)
		c.op(opEnd)
	}

	c.localGet(11)
	c.i32Const(1)
	c.op(opI32Add)
	c.localSet(11)
	c.br(0)
	c.op(opEnd)
	c.op(opEnd)
	c.op(opEnd)

	// Render sc.labels AFTER sc.shapes so HUD and overlay text always appear on top.
	emitListLoop(6, 5, "screen.label")

	// Call env.present_scene(buf, pos - buf)
	c.localGet(9)
	c.localGet(10)
	c.localGet(9)
	c.op(opI32Sub)
	c.call(wc.rt.impPresentScene)

	// Restore heapPtr = frameMark immediately after JS has read the buffer!
	c.globalGet(wc.rt.frameMarkGlobal)
	c.globalSet(wc.rt.heapPtrGlobal)

	wc.mod.setFuncBody(wc.rt.fnPresentScene, locals, c.b)
}
