package ease

import (
	"fmt"
	"sort"
	"strings"
)

func (wc *wasmCompiler) allocLocal(t byte) uint32 {
	base := uint32(0)
	if wc.curFn != nil {
		base = uint32(len(wc.curFn.paramTypes))
	}
	idx := base + uint32(len(wc.localTypes))
	wc.localTypes = append(wc.localTypes, t)
	return idx
}

func (wc *wasmCompiler) resolveVar(name string) (wasmVarSlot, bool) {
	if wc.locals != nil {
		if s, ok := wc.locals[name]; ok {
			return s, true
		}
	}
	if s, ok := wc.globals[name]; ok {
		return s, true
	}
	return wasmVarSlot{}, false
}

func (wc *wasmCompiler) ensureVar(name string, wt wasmTypeInfo) wasmVarSlot {
	if wc.curFn != nil {
		if s, ok := wc.locals[name]; ok {
			return s
		}
		idx := wc.allocLocal(wt.wasmStorageType())
		s := wasmVarSlot{isGlobal: false, idx: idx, typ: wt}
		wc.locals[name] = s
		return s
	}
	if s, ok := wc.globals[name]; ok {
		return s
	}
	initOp := opI32Const
	if wt.wasmStorageType() == wasmF64 {
		initOp = opF64Const
	} else if wt.wasmStorageType() == wasmI64 {
		initOp = opI64Const
	}
	gIdx := wc.mod.addGlobal(wt.wasmStorageType(), true, initOp, 0)
	s := wasmVarSlot{isGlobal: true, idx: gIdx, typ: wt}
	wc.globals[name] = s
	return s
}

func (wc *wasmCompiler) emitGetVar(c *wasmCodeBuf, s wasmVarSlot) {
	if s.isGlobal {
		c.globalGet(s.idx)
	} else {
		c.localGet(s.idx)
	}
}

func (wc *wasmCompiler) emitSetVar(c *wasmCodeBuf, s wasmVarSlot) {
	if s.isGlobal {
		c.globalSet(s.idx)
	} else {
		c.localSet(s.idx)
	}
}

func (wc *wasmCompiler) compileFunctions() error {
	keys := make([]string, 0, len(wc.funcs))
	for k := range wc.funcs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fi := wc.funcs[k]
		wc.curFn = fi
		wc.locals = make(map[string]wasmVarSlot)
		wc.localTypes = nil
		for i, p := range fi.decl.Params {
			wc.locals[p] = wasmVarSlot{isGlobal: false, idx: uint32(i), typ: fi.paramTypes[i]}
		}
		var c wasmCodeBuf
		if err := wc.emitStmts(&c, fi.decl.Body); err != nil {
			return err
		}
		if fi.retType.kind != wkVoid {
			switch fi.retType.wasmStorageType() {
			case wasmF64:
				c.f64Const(0)
			case wasmI64:
				c.i64Const(0)
			default:
				c.i32Const(0)
			}
		}
		wc.mod.setFuncBody(fi.fnIdx, wc.localTypes, c.b)
	}
	wc.curFn = nil
	wc.locals = nil
	wc.localTypes = nil
	return nil
}

func (wc *wasmCompiler) compileEntryPoints() error {
	initFn := wc.mod.reserveFunc(nil, []byte{wasmI32})
	stepFn := wc.mod.reserveFunc(nil, []byte{wasmI32})
	wc.mod.addExport("ease_init", wasmKindFunc, initFn)
	wc.mod.addExport("ease_step", wasmKindFunc, stepFn)

	// Detect GUI loop pattern in top-level or called entry function (`06_window.ease` / `08_haunted_hotel.ease`).
	handled, err := wc.tryCompileScreenApp(initFn, stepFn)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}

	// Non-GUI program: run all top-level statements in ease_init(), return 0 (done).
	wc.curFn = nil
	wc.locals = nil
	wc.localTypes = nil
	var ic wasmCodeBuf
	if err := wc.emitStmts(&ic, wc.prog.Body); err != nil {
		return err
	}
	ic.i32Const(0)
	wc.mod.setFuncBody(initFn, wc.localTypes, ic.b)

	var sc wasmCodeBuf
	sc.i32Const(0)
	wc.mod.setFuncBody(stepFn, nil, sc.b)
	return nil
}

func (wc *wasmCompiler) tryCompileScreenApp(initFn, stepFn uint32) (bool, error) {
	// Case A: `08_haunted_hotel.ease`-style: `screen:open` + `spawn renderFn` + `take events`
	// Case B: `06_window.ease`-style: `loop ... screen:present ... end`
	var hasScreen bool
	walkAllExprs(wc.prog, func(e Expr) {
		if ce, ok := e.(*CallExpr); ok && ce.Module == "screen" && (ce.Callee == "present" || ce.Callee == "open") {
			hasScreen = true
		}
	})
	if !hasScreen {
		return false, nil
	}

	// Check if there is a `spawn renderFn` + `take events` inside `prog.Body` or a top-level called function.
	var renderFn *FuncDecl
	var takeStmt *TakeStmt
	var preTakeStmts []Stmt
	var openCall *CallExpr
	var preOpenStmts []Stmt
	var postBody []Stmt
	visitedFuncs := make(map[string]bool)

	var collectFromStmts func(stmts []Stmt, topLevel bool)
	collectFromStmts = func(stmts []Stmt, topLevel bool) {
		for idx, st := range stmts {
			switch s := st.(type) {
			case *SpawnStmt:
				if s.Call != nil {
					if fi := wc.funcs[s.Call.Callee]; fi != nil {
						renderFn = fi.decl
					}
				}
			case *TakeStmt:
				takeStmt = s
				preTakeStmts = stmts[:idx]
			case *ExprStmt:
				if ce, ok := s.Expression.(*CallExpr); ok {
					if ce.Module == "screen" && ce.Callee == "open" {
						openCall = ce
						preOpenStmts = stmts[:idx]
					} else if ce.Module == "" {
						if fi := wc.funcs[ce.Callee]; fi != nil && !visitedFuncs[ce.Callee] {
							visitedFuncs[ce.Callee] = true
							if topLevel {
								postBody = stmts[idx+1:]
							}
							collectFromStmts(fi.decl.Body, false)
						}
					}
				} else if id, ok := s.Expression.(*IdentExpr); ok {
					if fi := wc.funcs[id.Name]; fi != nil && !visitedFuncs[id.Name] {
						visitedFuncs[id.Name] = true
						if topLevel {
							postBody = stmts[idx+1:]
						}
						collectFromStmts(fi.decl.Body, false)
					}
				}
			case *ReturnStmt:
				if ce, ok := s.Value.(*CallExpr); ok && ce.Module == "" {
					if fi := wc.funcs[ce.Callee]; fi != nil && !visitedFuncs[ce.Callee] {
						visitedFuncs[ce.Callee] = true
						collectFromStmts(fi.decl.Body, false)
					}
				}
			case *AssignStmt:
				if ce, ok := s.Value.(*CallExpr); ok && ce.Module == "" {
					if fi := wc.funcs[ce.Callee]; fi != nil && !visitedFuncs[ce.Callee] {
						visitedFuncs[ce.Callee] = true
						if topLevel {
							postBody = stmts[idx+1:]
						}
						collectFromStmts(fi.decl.Body, false)
					}
				} else if id, ok := s.Value.(*IdentExpr); ok {
					if fi := wc.funcs[id.Name]; fi != nil && !visitedFuncs[id.Name] {
						visitedFuncs[id.Name] = true
						if topLevel {
							postBody = stmts[idx+1:]
						}
						collectFromStmts(fi.decl.Body, false)
					}
				}
			}
		}
	}
	collectFromStmts(wc.prog.Body, true)

	if openCall != nil && renderFn != nil && takeStmt != nil {
		if err := wc.emitSpawnTakeScreenApp(initFn, stepFn, preOpenStmts, preTakeStmts, openCall, renderFn, takeStmt, postBody); err != nil {
			return false, err
		}
		return true, nil
	}

	// Case B: Direct `loop ... screen:present ... end` inside `prog.Body` or an entry function.
	var loopStmt *LoopStmt
	var preLoop []Stmt
	targetStmts := wc.prog.Body
	for _, st := range wc.prog.Body {
		var fnName string
		switch s := st.(type) {
		case *ExprStmt:
			if ce, ok := s.Expression.(*CallExpr); ok && ce.Module == "" {
				fnName = ce.Callee
			} else if id, ok := s.Expression.(*IdentExpr); ok {
				fnName = id.Name
			}
		case *AssignStmt:
			if ce, ok := s.Value.(*CallExpr); ok && ce.Module == "" {
				fnName = ce.Callee
			} else if id, ok := s.Value.(*IdentExpr); ok {
				fnName = id.Name
			}
		}
		if fnName != "" {
			if fi := wc.funcs[fnName]; fi != nil && len(fi.decl.Body) > 0 {
				targetStmts = fi.decl.Body
				break
			}
		}
	}
	for i, st := range targetStmts {
		if lp, ok := st.(*LoopStmt); ok {
			hasPres := false
			walkStmtsAndExprs(lp.Body, nil, func(e Expr) {
				if ce, ok := e.(*CallExpr); ok && ce.Module == "screen" && ce.Callee == "present" {
					hasPres = true
				}
			})
			if hasPres {
				loopStmt = lp
				preLoop = targetStmts[:i]
				break
			}
		}
	}
	if loopStmt != nil {
		if err := wc.emitDirectLoopScreenApp(initFn, stepFn, preLoop, loopStmt); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (wc *wasmCompiler) emitDirectLoopScreenApp(initFn, stepFn uint32, preLoop []Stmt, lp *LoopStmt) error {
	wc.activeGlobal = wc.mod.addGlobal(wasmI32, true, opI32Const, 0)
	wc.frameIdxGlob = wc.mod.addGlobal(wasmF64, true, opF64Const, 0)
	wc.hasScreenLoop = true

	wc.curFn = nil
	wc.locals = nil
	wc.localTypes = nil
	var ic wasmCodeBuf
	if err := wc.emitStmts(&ic, preLoop); err != nil {
		return err
	}
	ic.i32Const(1)
	ic.globalSet(wc.activeGlobal)
	ic.f64Const(1)
	ic.globalSet(wc.frameIdxGlob)
	ic.i32Const(1)
	wc.mod.setFuncBody(initFn, wc.localTypes, ic.b)

	// stepFn: runs 1 iteration of lp.Body, increments frameIdxGlob, returns activeGlobal.
	wc.curFn = nil
	wc.locals = nil
	wc.localTypes = nil
	var sc wasmCodeBuf
	sc.globalGet(wc.activeGlobal)
	sc.op(opI32Eqz)
	sc.ifOp(wasmBlockVoid)
	sc.i32Const(0)
	sc.op(opReturn)
	sc.op(opEnd)

	// Bind `it` (and `it.index`) to frameIdxGlob
	itSlot := wc.ensureVar("it", wasmTypeInfo{kind: wkNum})
	idxSlot := wc.ensureVar("it.index", wasmTypeInfo{kind: wkNum})
	sc.globalGet(wc.frameIdxGlob)
	wc.emitSetVar(&sc, itSlot)
	sc.globalGet(wc.frameIdxGlob)
	wc.emitSetVar(&sc, idxSlot)

	sc.block(wasmBlockVoid) // break target for `-> end` / `-> "..."` inside step
	wc.loopDepth++
	wc.breakDepth = 0
	for _, st := range lp.Body {
		if err := wc.emitStepStmt(&sc, st); err != nil {
			return err
		}
	}
	wc.loopDepth--
	sc.op(opEnd)

	sc.globalGet(wc.frameIdxGlob)
	sc.f64Const(1)
	sc.op(opF64Add)
	sc.globalSet(wc.frameIdxGlob)

	sc.globalGet(wc.activeGlobal)
	wc.mod.setFuncBody(stepFn, wc.localTypes, sc.b)
	wc.hasScreenLoop = false
	return nil
}

func (wc *wasmCompiler) emitSpawnTakeScreenApp(initFn, stepFn uint32, preOpenStmts, preTakeStmts []Stmt, openCall *CallExpr, renderFn *FuncDecl, takeStmt *TakeStmt, postBody []Stmt) error {
	wc.activeGlobal = wc.mod.addGlobal(wasmI32, true, opI32Const, 0)
	wc.frameIdxGlob = wc.mod.addGlobal(wasmF64, true, opF64Const, 0)
	wc.hasScreenLoop = true

	// ease_init: run pre-open and pre-take setup (e.g. win = screen.screen with ..., seen = list as text), open screen, set active = 1
	wc.curFn = nil
	wc.locals = nil
	wc.localTypes = nil
	var ic wasmCodeBuf
	if err := wc.emitStmts(&ic, preOpenStmts); err != nil {
		return err
	}
	if err := wc.emitStmts(&ic, preTakeStmts); err != nil {
		return err
	}
	if len(openCall.Args) >= 1 {
		if err := wc.emitCallExpr(&ic, &CallExpr{Module: "screen", Callee: "open", Args: openCall.Args}); err != nil {
			return err
		}
	}
	ic.i32Const(1)
	ic.globalSet(wc.activeGlobal)
	ic.f64Const(1)
	ic.globalSet(wc.frameIdxGlob)
	ic.i32Const(1)
	wc.mod.setFuncBody(initFn, wc.localTypes, ic.b)

	// ease_step: drain poll_event into takeStmt.Body, then run 1 iteration of renderFn's loop
	wc.curFn = nil
	wc.locals = nil
	wc.localTypes = nil
	var sc wasmCodeBuf
	sc.globalGet(wc.activeGlobal)
	sc.op(opI32Eqz)
	sc.ifOp(wasmBlockVoid)
	sc.i32Const(0)
	sc.op(opReturn)
	sc.op(opEnd)

	// Poll queued DOM events
	evRecSlot := wc.ensureVar("it", wasmTypeInfo{kind: wkRec, name: "screen.event"})
	sc.block(wasmBlockVoid)
	sc.loop(wasmBlockVoid)
	sc.i32Const(int32(wasmEventBufAddr))
	sc.call(wc.rt.impPollEvent)
	sc.op(opI32Eqz)
	sc.brIf(1)
	wc.emitBuildEventRecordFromBuf(&sc)
	wc.emitSetVar(&sc, evRecSlot)
	// Execute takeStmt body (check close/escape -> activeGlobal = 0)
	for _, st := range takeStmt.Body {
		if cg, ok := st.(*CheckGuardStmt); ok && len(cg.Rules) == 1 {
			if _, isEnd := cg.Rules[0].Action.(*LoopEndStmt); isEnd {
				if err := wc.emitExpr(&sc, cg.Rules[0].Cond, wasmTypeInfo{kind: wkCond}); err != nil {
					return err
				}
				sc.ifOp(wasmBlockVoid)
				sc.i32Const(0)
				sc.globalSet(wc.activeGlobal)
				sc.br(2)
				sc.op(opEnd)
				continue
			}
		}
		if err := wc.emitStmt(&sc, st); err != nil {
			return err
		}
	}
	sc.br(0)
	sc.op(opEnd)
	sc.op(opEnd)

	// Run 1 tick of renderFn's loop (or body)
	idxSlot := wc.ensureVar("it.index", wasmTypeInfo{kind: wkNum})
	sc.globalGet(wc.frameIdxGlob)
	wc.emitSetVar(&sc, idxSlot)
	sc.block(wasmBlockVoid)
	wc.loopDepth++
	wc.breakDepth = 0
	for _, st := range renderFn.Body {
		if lp, ok := st.(*LoopStmt); ok {
			for _, lst := range lp.Body {
				if err := wc.emitStepStmt(&sc, lst); err != nil {
					return err
				}
			}
		} else {
			if err := wc.emitStmt(&sc, st); err != nil {
				return err
			}
		}
	}
	wc.loopDepth--
	sc.op(opEnd)
	sc.globalGet(wc.frameIdxGlob)
	sc.f64Const(1)
	sc.op(opF64Add)
	sc.globalSet(wc.frameIdxGlob)

	sc.globalGet(wc.activeGlobal)
	sc.op(opI32Eqz)
	sc.ifOp(wasmBlockVoid)
	if err := wc.emitStmts(&sc, postBody); err != nil {
		return err
	}
	sc.op(opEnd)

	sc.globalGet(wc.activeGlobal)
	wc.mod.setFuncBody(stepFn, wc.localTypes, sc.b)
	wc.hasScreenLoop = false
	return nil
}

func (wc *wasmCompiler) emitStepStmt(c *wasmCodeBuf, st Stmt) error {
	if cg, ok := st.(*CheckGuardStmt); ok && len(cg.Rules) == 1 {
		if cg.Rules[0].Action != nil {
			_, isEnd := cg.Rules[0].Action.(*LoopEndStmt)
			_, isRet := cg.Rules[0].Action.(*ReturnStmt)
			if isEnd || isRet {
				_ = wc.emitExpr(c, cg.Rules[0].Cond, wasmTypeInfo{kind: wkCond})
				c.ifOp(wasmBlockVoid)
				c.i32Const(0)
				c.globalSet(wc.activeGlobal)
				c.br(1)
				c.op(opEnd)
				return nil
			}
		}
	}
	return wc.emitStmt(c, st)
}

func (wc *wasmCompiler) emitBuildEventRecordFromBuf(c *wasmCodeBuf) {
	lay := wc.records["screen.event"]
	c.i32Const(int32(lay.size))
	c.call(wc.rt.fnAlloc)
	recTmp := wc.allocLocal(wasmI32)
	c.localSet(recTmp)

	// kind @0: 1=close, 2=key, 3=click, 4=move
	kindSlot := lay.byName["kind"]
	c.i32Const(int32(wasmEventBufAddr))
	c.memOp(opI32Load8U, 0, 0)
	evKindTmp := wc.allocLocal(wasmI32)
	c.localSet(evKindTmp)

	c.localGet(recTmp)
	c.localGet(evKindTmp)
	c.i32Const(1)
	c.op(opI32Eq)
	c.ifOp(wasmI64)
	c.i64Const(int64(wc.mod.internString("close")))
	c.op(opElse)
	c.localGet(evKindTmp)
	c.i32Const(2)
	c.op(opI32Eq)
	c.ifOp(wasmI64)
	c.i64Const(int64(wc.mod.internString("key")))
	c.op(opElse)
	c.localGet(evKindTmp)
	c.i32Const(3)
	c.op(opI32Eq)
	c.ifOp(wasmI64)
	c.i64Const(int64(wc.mod.internString("click")))
	c.op(opElse)
	c.i64Const(int64(wc.mod.internString("move")))
	c.op(opEnd)
	c.op(opEnd)
	c.op(opEnd)
	c.memOp(opI64Store, 3, kindSlot.offset)

	// x @8, y @16
	c.localGet(recTmp)
	c.i32Const(int32(wasmEventBufAddr))
	c.memOp(opF64Load, 3, 8)
	c.memOp(opF64Store, 3, lay.byName["x"].offset)
	c.localGet(recTmp)
	c.i32Const(int32(wasmEventBufAddr))
	c.memOp(opF64Load, 3, 16)
	c.memOp(opF64Store, 3, lay.byName["y"].offset)

	// key_ptr @24, key_len @28 -> packed i64
	c.localGet(recTmp)
	c.i32Const(int32(wasmEventBufAddr))
	c.memOp(opI32Load, 2, 24)
	c.op(opI64ExtendI32U)
	c.i64Const(32)
	c.op(opI64Shl)
	c.i32Const(int32(wasmEventBufAddr))
	c.memOp(opI32Load, 2, 28)
	c.op(opI64ExtendI32U)
	c.op(opI64Or)
	c.memOp(opI64Store, 3, lay.byName["key"].offset)

	c.localGet(recTmp)
}

func (wc *wasmCompiler) emitBuildFrameStateFromBuf(c *wasmCodeBuf) {
	// Polls 1 event from env.poll_event(wasmEventBufAddr) and returns a `screen.frame_state` record ptr.
	lay := wc.records["screen.frame_state"]
	c.i32Const(int32(wasmEventBufAddr))
	c.call(wc.rt.impPollEvent)
	hasEv := wc.allocLocal(wasmI32)
	c.localSet(hasEv)

	c.i32Const(int32(lay.size))
	c.call(wc.rt.fnAlloc)
	recTmp := wc.allocLocal(wasmI32)
	c.localSet(recTmp)

	evKind := wc.allocLocal(wasmI32)
	c.localGet(hasEv)
	c.ifOp(wasmI32)
	c.i32Const(int32(wasmEventBufAddr))
	c.memOp(opI32Load8U, 0, 0)
	c.op(opElse)
	c.i32Const(0)
	c.op(opEnd)
	c.localSet(evKind)

	// status: "closed" if evKind == 1, else "open"
	c.localGet(recTmp)
	c.localGet(evKind)
	c.i32Const(1)
	c.op(opI32Eq)
	c.ifOp(wasmI64)
	c.i64Const(int64(wc.mod.internString("closed")))
	c.op(opElse)
	c.i64Const(int64(wc.mod.internString("open")))
	c.op(opEnd)
	c.memOp(opI64Store, 3, lay.byName["status"].offset)

	// key
	c.localGet(recTmp)
	c.localGet(hasEv)
	c.ifOp(wasmI64)
	c.i32Const(int32(wasmEventBufAddr))
	c.memOp(opI32Load, 2, 24)
	c.op(opI64ExtendI32U)
	c.i64Const(32)
	c.op(opI64Shl)
	c.i32Const(int32(wasmEventBufAddr))
	c.memOp(opI32Load, 2, 28)
	c.op(opI64ExtendI32U)
	c.op(opI64Or)
	c.op(opElse)
	c.i64Const(int64(wc.mod.internString("")))
	c.op(opEnd)
	c.memOp(opI64Store, 3, lay.byName["key"].offset)

	// mouse_x, mouse_y
	c.localGet(recTmp)
	c.localGet(hasEv)
	c.ifOp(wasmF64)
	c.i32Const(int32(wasmEventBufAddr))
	c.memOp(opF64Load, 3, 8)
	c.op(opElse)
	c.f64Const(0)
	c.op(opEnd)
	c.memOp(opF64Store, 3, lay.byName["mouse_x"].offset)

	c.localGet(recTmp)
	c.localGet(hasEv)
	c.ifOp(wasmF64)
	c.i32Const(int32(wasmEventBufAddr))
	c.memOp(opF64Load, 3, 16)
	c.op(opElse)
	c.f64Const(0)
	c.op(opEnd)
	c.memOp(opF64Store, 3, lay.byName["mouse_y"].offset)

	c.localGet(recTmp)
}

func (wc *wasmCompiler) emitStmts(c *wasmCodeBuf, stmts []Stmt) error {
	for _, st := range stmts {
		if err := wc.emitStmt(c, st); err != nil {
			return err
		}
	}
	return nil
}

func (wc *wasmCompiler) emitStmt(c *wasmCodeBuf, st Stmt) error {
	switch s := st.(type) {
	case *ConstDeclStmt:
		wt := wc.inferExprTypeWithEnv(s.Value, wc.curEnvTypes(), "")
		if wt.kind == wkVoid {
			wt = wasmTypeInfo{kind: wkNum}
		}
		slot := wc.ensureVar(s.Name, wt)
		before := len(c.b)
		if err := wc.emitExpr(c, s.Value, wt); err != nil {
			return err
		}
		if len(c.b) == before {
			return fmt.Errorf("wasm: AssignStmt %q emitted 0 bytes for %T", s.Name, s.Value)
		}
		wc.emitSetVar(c, slot)
		return nil

	case *AssignStmt:
		// Special case: `raw_state = screen:present ...` followed by `as json` + `as screen.frame_state`
		wt := wc.inferExprTypeWithEnv(s.Value, wc.curEnvTypes(), "")
		if wt.kind == wkVoid {
			wt = wasmTypeInfo{kind: wkNum}
		}
		if ce, ok := s.Value.(*CastExpr); ok && ce.TypeName == "screen.frame_state" {
			wt = wasmTypeInfo{kind: wkRec, name: "screen.frame_state"}
			slot := wc.ensureVar(s.Name, wt)
			wc.emitBuildFrameStateFromBuf(c)
			wc.emitSetVar(c, slot)
			return nil
		}
		slot := wc.ensureVar(s.Name, wt)
		before := len(c.b)
		if err := wc.emitExpr(c, s.Value, wt); err != nil {
			return err
		}
		if len(c.b) == before {
			return fmt.Errorf("wasm: AssignStmt %q emitted 0 bytes for %T", s.Name, s.Value)
		}
		wc.emitSetVar(c, slot)
		return nil

	case *SayStmt:
		vt := wc.inferExprTypeWithEnv(s.Value, wc.curEnvTypes(), "")
		if vt.kind == wkNum {
			if err := wc.emitExpr(c, s.Value, vt); err != nil {
				return err
			}
			c.call(wc.rt.fnF64ToText)
		} else {
			if err := wc.emitExpr(c, s.Value, wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
		}
		tmp := wc.allocLocal(wasmI64)
		c.localTee(tmp)
		emitUnpackPtr(c)
		c.localGet(tmp)
		emitUnpackLen(c)
		c.call(wc.rt.impLog)
		return nil

	case *ExprStmt:
		vt := wc.inferExprTypeWithEnv(s.Expression, wc.curEnvTypes(), "")
		if err := wc.emitExpr(c, s.Expression, vt); err != nil {
			return err
		}
		if vt.kind != wkVoid {
			c.op(opDrop)
		}
		return nil

	case *ReturnStmt:
		if wc.curFn == nil {
			return nil
		}
		if err := wc.emitExpr(c, s.Value, wc.curFn.retType); err != nil {
			return err
		}
		c.op(opReturn)
		return nil

	case *LoopEndStmt:
		if wc.loopDepth > 0 {
			c.br(uint32(wc.breakDepth + 1))
		}
		return nil

	case *CheckGuardStmt:
		if s.IsEffectBlock {
			if err := wc.emitExpr(c, s.Cond, wasmTypeInfo{kind: wkCond}); err != nil {
				return err
			}
			c.ifOp(wasmBlockVoid)
			wc.breakDepth++
			if err := wc.emitStmts(c, s.Body); err != nil {
				return err
			}
			if len(s.ElseBody) > 0 {
				c.op(opElse)
				if err := wc.emitStmts(c, s.ElseBody); err != nil {
					return err
				}
			}
			wc.breakDepth--
			c.op(opEnd)
			return nil
		}
		for _, r := range s.Rules {
			if err := wc.emitExpr(c, r.Cond, wasmTypeInfo{kind: wkCond}); err != nil {
				return err
			}
			c.ifOp(wasmBlockVoid)
			wc.breakDepth++
			if r.Action != nil {
				if err := wc.emitStmt(c, r.Action); err != nil {
					return err
				}
			}
			wc.breakDepth--
			c.op(opEnd)
		}
		return wc.emitStmts(c, s.ElseBody)

	case *LoopStmt:
		return wc.emitLoopStmt(c, s)
	}
	return nil
}

func (wc *wasmCompiler) emitLoopStmt(c *wasmCodeBuf, s *LoopStmt) error {
	idxSlot := wc.ensureVar("it.index", wasmTypeInfo{kind: wkNum})
	if s.Iterable == nil {
		// Infinite loop with `-> end`
		cnt := wc.allocLocal(wasmF64)
		c.f64Const(1)
		c.localSet(cnt)
		c.block(wasmBlockVoid)
		c.loop(wasmBlockVoid)
		c.localGet(cnt)
		wc.emitSetVar(c, idxSlot)
		wc.loopDepth++
		prevBD := wc.breakDepth
		wc.breakDepth = 0
		if err := wc.emitStmts(c, s.Body); err != nil {
			return err
		}
		wc.breakDepth = prevBD
		wc.loopDepth--
		c.localGet(cnt)
		c.f64Const(1)
		c.op(opF64Add)
		c.localSet(cnt)
		c.br(0)
		c.op(opEnd)
		c.op(opEnd)
		return nil
	}

	lt := wc.inferExprTypeWithEnv(s.Iterable, wc.curEnvTypes(), "")
	listLocal := wc.allocLocal(wasmI32)
	if err := wc.emitExpr(c, s.Iterable, lt); err != nil {
		return err
	}
	c.localTee(listLocal)
	c.memOp(opI32Load, 2, 4)
	nLocal := wc.allocLocal(wasmI32)
	c.localSet(nLocal)
	dataLocal := wc.allocLocal(wasmI32)
	c.localGet(listLocal)
	c.memOp(opI32Load, 2, 0)
	c.localSet(dataLocal)

	elemType := wasmTypeInfo{kind: lt.elemKind, name: lt.name}
	if elemType.kind == wkVoid {
		elemType.kind = wkNum
	}
	itSlot := wc.ensureVar("it", elemType)

	iLocal := wc.allocLocal(wasmI32)
	c.i32Const(0)
	c.localSet(iLocal)

	c.block(wasmBlockVoid)
	c.loop(wasmBlockVoid)
	c.localGet(iLocal)
	c.localGet(nLocal)
	c.op(opI32GeS)
	c.brIf(1)

	c.localGet(iLocal)
	c.i32Const(1)
	c.op(opI32Add)
	c.op(opF64ConvertI32S)
	wc.emitSetVar(c, idxSlot)

	c.localGet(dataLocal)
	c.localGet(iLocal)
	c.i32Const(8)
	c.op(opI32Mul)
	c.op(opI32Add)
	switch elemType.wasmStorageType() {
	case wasmF64:
		c.memOp(opF64Load, 3, 0)
	case wasmI64:
		c.memOp(opI64Load, 3, 0)
	default:
		c.memOp(opI32Load, 2, 0)
	}
	wc.emitSetVar(c, itSlot)

	wc.loopDepth++
	prevBD := wc.breakDepth
	wc.breakDepth = 0
	if err := wc.emitStmts(c, s.Body); err != nil {
		return err
	}
	wc.breakDepth = prevBD
	wc.loopDepth--

	c.localGet(iLocal)
	c.i32Const(1)
	c.op(opI32Add)
	c.localSet(iLocal)
	c.br(0)
	c.op(opEnd)
	c.op(opEnd)
	return nil
}

func (wc *wasmCompiler) curEnvTypes() map[string]wasmTypeInfo {
	out := make(map[string]wasmTypeInfo)
	for k, s := range wc.globals {
		out[k] = s.typ
	}
	for k, s := range wc.locals {
		out[k] = s.typ
	}
	return out
}

func (wc *wasmCompiler) emitExpr(c *wasmCodeBuf, e Expr, targetType wasmTypeInfo) error {
	if e == nil {
		return nil
	}
	switch ex := e.(type) {
	case *NumberExpr:
		c.f64Const(ex.Value)
		return nil

	case *StringExpr:
		c.i64Const(int64(wc.mod.internString(ex.Value)))
		return nil

	case *InterpStringExpr:
		if len(ex.Parts) == 0 {
			c.i64Const(int64(wc.mod.internString("")))
			return nil
		}
		for i, p := range ex.Parts {
			if !p.IsExpr {
				c.i64Const(int64(wc.mod.internString(p.Text)))
			} else {
				pt := wc.inferExprTypeWithEnv(p.Expr, wc.curEnvTypes(), "")
				if pt.kind == wkNum {
					if err := wc.emitExpr(c, p.Expr, pt); err != nil {
						return err
					}
					c.call(wc.rt.fnF64ToText)
				} else {
					if err := wc.emitExpr(c, p.Expr, wasmTypeInfo{kind: wkText}); err != nil {
						return err
					}
				}
			}
			if i > 0 {
				c.call(wc.rt.fnStrConcat)
			}
		}
		return nil

	case *IdentExpr:
		if ex.Name == "it.index" {
			if s, ok := wc.resolveVar("it.index"); ok {
				wc.emitGetVar(c, s)
				return nil
			}
		}
		if s, ok := wc.resolveVar(ex.Name); ok {
			wc.emitGetVar(c, s)
			return nil
		}
		if lay := wc.records[ex.Name]; lay != nil {
			return wc.emitDefaultRecord(c, lay)
		}
		if fi := wc.funcs[ex.Name]; fi != nil && len(fi.decl.Params) == 0 {
			c.call(fi.fnIdx)
			return nil
		}
		if targetType.kind == wkNum {
			c.f64Const(0)
		} else {
			c.i64Const(0)
		}
		return nil

	case *FieldExpr:
		if id, ok := ex.Target.(*IdentExpr); ok && id.Name == "it" && ex.Field == "index" {
			if s, ok := wc.resolveVar("it.index"); ok {
				wc.emitGetVar(c, s)
				return nil
			}
		}
		if id, ok := ex.Target.(*IdentExpr); ok && wc.modules[id.Name] != nil {
			qual := id.Name + "." + ex.Field
			if lay := wc.records[qual]; lay != nil {
				return wc.emitDefaultRecord(c, lay)
			}
		}
		recT := wc.inferExprTypeWithEnv(ex.Target, wc.curEnvTypes(), "")
		lay := wc.records[recT.name]
		if lay == nil {
			return fmt.Errorf("wasm: unknown record type %q for field '.%s'", recT.name, ex.Field)
		}
		rf, ok := lay.byName[ex.Field]
		if !ok {
			return fmt.Errorf("wasm: unknown field '%s.%s'", recT.name, ex.Field)
		}
		if err := wc.emitExpr(c, ex.Target, recT); err != nil {
			return err
		}
		switch rf.typ.wasmStorageType() {
		case wasmF64:
			c.memOp(opF64Load, 3, rf.offset)
		case wasmI64:
			c.memOp(opI64Load, 3, rf.offset)
		default:
			c.memOp(opI32Load, 2, rf.offset)
		}
		return nil

	case *WithExpr:
		baseT := wc.inferExprTypeWithEnv(ex.Base, wc.curEnvTypes(), "")
		if len(ex.IndexUpdates) > 0 {
			if err := wc.emitExpr(c, ex.Base, baseT); err != nil {
				return err
			}
			for _, iu := range ex.IndexUpdates {
				if err := wc.emitExpr(c, iu.Index, wasmTypeInfo{kind: wkNum}); err != nil {
					return err
				}
				if err := wc.emitExprAsI64(c, iu.Value, wasmTypeInfo{kind: baseT.elemKind, name: baseT.name}); err != nil {
					return err
				}
				c.call(wc.rt.fnListSet)
			}
			return nil
		}
		lay := wc.records[baseT.name]
		if lay == nil {
			return fmt.Errorf("wasm: unknown record %q in 'with'", baseT.name)
		}
		srcTmp := wc.allocLocal(wasmI32)
		dstTmp := wc.allocLocal(wasmI32)
		if err := wc.emitExpr(c, ex.Base, baseT); err != nil {
			return err
		}
		c.localSet(srcTmp)
		c.i32Const(int32(lay.size))
		c.call(wc.rt.fnAlloc)
		c.localTee(dstTmp)
		c.localGet(srcTmp)
		c.i32Const(int32(lay.size))
		c.call(wc.rt.fnMemcpy)
		for _, u := range ex.Updates {
			rf, ok := lay.byName[u.Name]
			if !ok {
				continue
			}
			c.localGet(dstTmp)
			vt := wc.inferExprTypeWithEnv(u.Value, wc.curEnvTypes(), "")
			if err := wc.emitExpr(c, u.Value, rf.typ); err != nil {
				return err
			}
			if rf.typ.wasmStorageType() == wasmI64 && vt.kind == wkNum {
				c.call(wc.rt.fnF64ToText)
			}
			switch rf.typ.wasmStorageType() {
			case wasmF64:
				c.memOp(opF64Store, 3, rf.offset)
			case wasmI64:
				c.memOp(opI64Store, 3, rf.offset)
			default:
				c.memOp(opI32Store, 2, rf.offset)
			}
		}
		c.localGet(dstTmp)
		return nil

	case *ListExpr:
		capN := max(4, len(ex.Elements))
		c.i32Const(int32(capN))
		c.call(wc.rt.fnListNew)
		listTmp := wc.allocLocal(wasmI32)
		c.localSet(listTmp)
		var elemT wasmTypeInfo
		if len(ex.Elements) > 0 {
			elemT = wc.inferExprTypeWithEnv(ex.Elements[0], wc.curEnvTypes(), "")
		}
		for _, el := range ex.Elements {
			c.localGet(listTmp)
			if err := wc.emitExprAsI64(c, el, elemT); err != nil {
				return err
			}
			c.call(wc.rt.fnListPush)
			c.localSet(listTmp)
		}
		c.localGet(listTmp)
		return nil

	case *TypedListExpr:
		capN := max(4, len(ex.Rows))
		c.i32Const(int32(capN))
		c.call(wc.rt.fnListNew)
		listTmp := wc.allocLocal(wasmI32)
		c.localSet(listTmp)
		lay := wc.records[ex.ElemType]
		for _, row := range ex.Rows {
			c.localGet(listTmp)
			if lay != nil {
				if err := wc.emitDefaultRecord(c, lay); err != nil {
					return err
				}
				recTmp := wc.allocLocal(wasmI32)
				c.localSet(recTmp)
				for _, u := range row {
					if rf, ok := lay.byName[u.Name]; ok {
						c.localGet(recTmp)
						vt := wc.inferExprTypeWithEnv(u.Value, wc.curEnvTypes(), "")
						if err := wc.emitExpr(c, u.Value, rf.typ); err != nil {
							return err
						}
						if rf.typ.wasmStorageType() == wasmI64 && vt.kind == wkNum {
							c.call(wc.rt.fnF64ToText)
						}
						switch rf.typ.wasmStorageType() {
						case wasmF64:
							c.memOp(opF64Store, 3, rf.offset)
						case wasmI64:
							c.memOp(opI64Store, 3, rf.offset)
						default:
							c.memOp(opI32Store, 2, rf.offset)
						}
					}
				}
				c.localGet(recTmp)
				c.op(opI64ExtendI32U)
			} else {
				c.i64Const(0)
			}
			c.call(wc.rt.fnListPush)
			c.localSet(listTmp)
		}
		c.localGet(listTmp)
		return nil

	case *IndexExpr:
		lt := wc.inferExprTypeWithEnv(ex.Target, wc.curEnvTypes(), "")
		if err := wc.emitExpr(c, ex.Target, lt); err != nil {
			return err
		}
		if err := wc.emitExpr(c, ex.Index, wasmTypeInfo{kind: wkNum}); err != nil {
			return err
		}
		c.call(wc.rt.fnListGet)
		switch targetType.wasmStorageType() {
		case wasmF64:
			c.op(opF64ReinterpretI64)
		case wasmI32:
			c.op(opI32WrapI64)
		}
		return nil

	case *WhereExpr:
		lt := wc.inferExprTypeWithEnv(ex.Target, wc.curEnvTypes(), "")
		srcList := wc.allocLocal(wasmI32)
		outList := wc.allocLocal(wasmI32)
		if err := wc.emitExpr(c, ex.Target, lt); err != nil {
			return err
		}
		c.localTee(srcList)
		c.memOp(opI32Load, 2, 4)
		nLocal := wc.allocLocal(wasmI32)
		c.localSet(nLocal)
		c.i32Const(4)
		c.call(wc.rt.fnListNew)
		c.localSet(outList)

		dataLocal := wc.allocLocal(wasmI32)
		c.localGet(srcList)
		c.memOp(opI32Load, 2, 0)
		c.localSet(dataLocal)

		elemType := wasmTypeInfo{kind: lt.elemKind, name: lt.name}
		if elemType.kind == wkVoid {
			elemType.kind = wkNum
		}
		itSlot := wc.ensureVar("it", elemType)
		idxSlot := wc.ensureVar("it.index", wasmTypeInfo{kind: wkNum})

		iLocal := wc.allocLocal(wasmI32)
		elI64 := wc.allocLocal(wasmI64)
		c.i32Const(0)
		c.localSet(iLocal)
		c.block(wasmBlockVoid)
		c.loop(wasmBlockVoid)
		c.localGet(iLocal)
		c.localGet(nLocal)
		c.op(opI32GeS)
		c.brIf(1)

		c.localGet(iLocal)
		c.i32Const(1)
		c.op(opI32Add)
		c.op(opF64ConvertI32S)
		wc.emitSetVar(c, idxSlot)

		c.localGet(dataLocal)
		c.localGet(iLocal)
		c.i32Const(8)
		c.op(opI32Mul)
		c.op(opI32Add)
		c.memOp(opI64Load, 3, 0)
		c.localTee(elI64)
		switch elemType.wasmStorageType() {
		case wasmF64:
			c.op(opF64ReinterpretI64)
		case wasmI32:
			c.op(opI32WrapI64)
		}
		wc.emitSetVar(c, itSlot)

		c.i32Const(1)
		for _, cond := range ex.Conditions {
			if err := wc.emitExpr(c, cond, wasmTypeInfo{kind: wkCond}); err != nil {
				return err
			}
			c.op(opI32And)
		}
		c.ifOp(wasmBlockVoid)
		c.localGet(outList)
		c.localGet(elI64)
		c.call(wc.rt.fnListPush)
		c.localSet(outList)
		c.op(opEnd)

		c.localGet(iLocal)
		c.i32Const(1)
		c.op(opI32Add)
		c.localSet(iLocal)
		c.br(0)
		c.op(opEnd)
		c.op(opEnd)
		c.localGet(outList)
		return nil

	case *UnaryExpr:
		if ex.Op == "not" {
			if err := wc.emitExpr(c, ex.Right, wasmTypeInfo{kind: wkCond}); err != nil {
				return err
			}
			c.op(opI32Eqz)
			return nil
		}
		if err := wc.emitExpr(c, ex.Right, wasmTypeInfo{kind: wkNum}); err != nil {
			return err
		}
		c.op(opF64Neg)
		return nil

	case *BinaryExpr:
		return wc.emitBinaryExpr(c, ex)

	case *CastExpr:
		return wc.emitCastExpr(c, ex)

	case *BoolExpr:
		if ex.Value {
			c.i32Const(1)
		} else {
			c.i32Const(0)
		}
		return nil

	case *TypeCheckExpr:
		vt := wc.inferExprTypeWithEnv(ex.Target, wc.curEnvTypes(), "")
		if err := wc.emitExpr(c, ex.Target, vt); err != nil {
			return err
		}
		match := false
		switch ex.TypeName {
		case "number":
			match = vt.kind == wkNum
		case "text":
			match = vt.kind == wkText
		case "bool":
			match = vt.kind == wkCond
		default:
			match = vt.name == ex.TypeName
		}
		if vt.wasmStorageType() == wasmI32 && match {
			c.i32Const(0)
			c.op(opI32Ne)
		} else {
			c.op(opDrop)
			if match {
				c.i32Const(1)
			} else {
				c.i32Const(0)
			}
		}
		if ex.Negated {
			c.op(opI32Eqz)
		}
		return nil

	case *InboxExpr:
		c.i32Const(1)
		return nil

	case *CallExpr:
		return wc.emitCallExpr(c, ex)
	}
	return nil
}

func (wc *wasmCompiler) emitExprAsI64(c *wasmCodeBuf, e Expr, wt wasmTypeInfo) error {
	if wt.kind == wkVoid {
		wt = wc.inferExprTypeWithEnv(e, wc.curEnvTypes(), "")
	}
	if err := wc.emitExpr(c, e, wt); err != nil {
		return err
	}
	switch wt.wasmStorageType() {
	case wasmF64:
		c.op(opI64ReinterpretF64)
	case wasmI32:
		c.op(opI64ExtendI32U)
	}
	return nil
}

func (wc *wasmCompiler) emitDefaultRecord(c *wasmCodeBuf, lay *wasmRecordLayout) error {
	c.i32Const(int32(lay.size))
	c.call(wc.rt.fnAlloc)
	tmp := wc.allocLocal(wasmI32)
	c.localSet(tmp)
	for _, rf := range lay.fields {
		c.localGet(tmp)
		var valExpr Expr
		if len(rf.def.Allowed) > 0 {
			valExpr = rf.def.Allowed[0]
		} else {
			valExpr = rf.def.Default
		}
		vt := wc.inferExprTypeWithEnv(valExpr, wc.curEnvTypes(), "")
		if err := wc.emitExpr(c, valExpr, rf.typ); err != nil {
			return err
		}
		if rf.typ.wasmStorageType() == wasmI64 && vt.kind == wkNum {
			c.call(wc.rt.fnF64ToText)
		}
		switch rf.typ.wasmStorageType() {
		case wasmF64:
			c.memOp(opF64Store, 3, rf.offset)
		case wasmI64:
			c.memOp(opI64Store, 3, rf.offset)
		default:
			c.memOp(opI32Store, 2, rf.offset)
		}
	}
	c.localGet(tmp)
	return nil
}

func (wc *wasmCompiler) exprAsTypeName(e Expr) (string, bool) {
	switch ex := e.(type) {
	case *IdentExpr:
		if _, isVar := wc.resolveVar(ex.Name); isVar {
			return "", false
		}
		if wc.records[ex.Name] != nil || wc.unions[ex.Name] != nil {
			return ex.Name, true
		}
	case *FieldExpr:
		if id, ok := ex.Target.(*IdentExpr); ok && wc.modules[id.Name] != nil {
			qual := id.Name + "." + ex.Field
			if wc.records[qual] != nil || wc.unions[qual] != nil {
				return qual, true
			}
		}
	}
	return "", false
}

func (wc *wasmCompiler) emitBinaryExpr(c *wasmCodeBuf, ex *BinaryExpr) error {
	switch ex.Op {
	case "and":
		if err := wc.emitExpr(c, ex.Left, wasmTypeInfo{kind: wkCond}); err != nil {
			return err
		}
		c.ifOp(wasmI32)
		if err := wc.emitExpr(c, ex.Right, wasmTypeInfo{kind: wkCond}); err != nil {
			return err
		}
		c.op(opElse)
		c.i32Const(0)
		c.op(opEnd)
		return nil
	case "or":
		if err := wc.emitExpr(c, ex.Left, wasmTypeInfo{kind: wkCond}); err != nil {
			return err
		}
		c.ifOp(wasmI32)
		c.i32Const(1)
		c.op(opElse)
		if err := wc.emitExpr(c, ex.Right, wasmTypeInfo{kind: wkCond}); err != nil {
			return err
		}
		c.op(opEnd)
		return nil
	case "is", "is not", "is-not", "==", "!=":
		// In Ease, `x is MyRecord` / `x is not screen.frame_state` is parsed as BinaryExpr
		// where Right is a record/union type name rather than a runtime value.
		if typeName, isType := wc.exprAsTypeName(ex.Right); isType && (ex.Op == "is" || ex.Op == "is not" || ex.Op == "is-not") {
			lt := wc.inferExprTypeWithEnv(ex.Left, wc.curEnvTypes(), "")
			if err := wc.emitExpr(c, ex.Left, lt); err != nil {
				return err
			}
			match := lt.name == typeName
			if lt.wasmStorageType() == wasmI32 && match {
				c.i32Const(0)
				c.op(opI32Ne)
			} else {
				c.op(opDrop)
				if match {
					c.i32Const(1)
				} else {
					c.i32Const(0)
				}
			}
			if ex.Op != "is" {
				c.op(opI32Eqz)
			}
			return nil
		}
		lt := wc.inferExprTypeWithEnv(ex.Left, wc.curEnvTypes(), "")
		if lt.kind == wkText || lt.wasmStorageType() == wasmI64 {
			if err := wc.emitExpr(c, ex.Left, wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Right, wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
			c.call(wc.rt.fnStrEq)
		} else if lt.wasmStorageType() == wasmI32 {
			if err := wc.emitExpr(c, ex.Left, lt); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Right, lt); err != nil {
				return err
			}
			c.op(opI32Eq)
		} else {
			if err := wc.emitExpr(c, ex.Left, wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Right, wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
			c.op(opF64Eq)
		}
		if ex.Op != "is" && ex.Op != "==" {
			c.op(opI32Eqz)
		}
		return nil
	}

	lt := wc.inferExprTypeWithEnv(ex.Left, wc.curEnvTypes(), "")
	if ex.Op == "+" && lt.kind == wkText {
		if err := wc.emitExpr(c, ex.Left, lt); err != nil {
			return err
		}
		if err := wc.emitExpr(c, ex.Right, lt); err != nil {
			return err
		}
		c.call(wc.rt.fnStrConcat)
		return nil
	}

	if err := wc.emitExpr(c, ex.Left, wasmTypeInfo{kind: wkNum}); err != nil {
		return err
	}
	if err := wc.emitExpr(c, ex.Right, wasmTypeInfo{kind: wkNum}); err != nil {
		return err
	}
	switch ex.Op {
	case "+":
		c.op(opF64Add)
	case "-":
		c.op(opF64Sub)
	case "*":
		c.op(opF64Mul)
	case "/":
		c.op(opF64Div)
	case "%":
		// WASM 1.0 has no f64.rem: a - f64.trunc(a / b) * b
		bTmp := wc.allocLocal(wasmF64)
		aTmp := wc.allocLocal(wasmF64)
		c.localSet(bTmp)
		c.localSet(aTmp)
		c.localGet(aTmp)
		c.localGet(aTmp)
		c.localGet(bTmp)
		c.op(opF64Div)
		c.op(opF64Trunc)
		c.localGet(bTmp)
		c.op(opF64Mul)
		c.op(opF64Sub)
	case "<":
		c.op(opF64Lt)
	case "<=":
		c.op(opF64Le)
	case ">":
		c.op(opF64Gt)
	case ">=":
		c.op(opF64Ge)
	}
	return nil
}

func (wc *wasmCompiler) emitCastExpr(c *wasmCodeBuf, ex *CastExpr) error {
	if ex.TypeName == "text" {
		vt := wc.inferExprTypeWithEnv(ex.Target, wc.curEnvTypes(), "")
		if err := wc.emitExpr(c, ex.Target, vt); err != nil {
			return err
		}
		if vt.kind == wkNum {
			c.call(wc.rt.fnF64ToText)
		}
		return nil
	}
	if ex.TypeName == "number" {
		if err := wc.emitExpr(c, ex.Target, wasmTypeInfo{kind: wkText}); err != nil {
			return err
		}
		okPtr := wc.allocLocal(wasmI32)
		c.i32Const(8)
		c.call(wc.rt.fnAlloc)
		c.localTee(okPtr)
		c.call(wc.rt.fnTextToF64)
		resTmp := wc.allocLocal(wasmF64)
		c.localSet(resTmp)
		c.localGet(okPtr)
		c.memOp(opI32Load, 2, 0)
		c.op(opI32Eqz)
		c.ifOp(wasmF64)
		if ex.Fallback != nil {
			if err := wc.emitExpr(c, ex.Fallback, wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
		} else {
			wc.rt.emitPanicConst(c, "Cannot convert text to number")
			c.f64Const(0)
		}
		c.op(opElse)
		c.localGet(resTmp)
		c.op(opEnd)
		return nil
	}
	if ex.TypeName == "json" {
		vt := wc.inferExprTypeWithEnv(ex.Target, wc.curEnvTypes(), "")
		if err := wc.emitExpr(c, ex.Target, vt); err != nil {
			return err
		}
		if vt.kind == wkText {
			return nil
		}
		if vt.kind == wkNum {
			c.call(wc.rt.fnF64ToText)
			return nil
		}
		c.op(opDrop)
		c.i64Const(int64(wc.mod.internString("[]")))
		return nil
	}
	if ex.TypeName == "screen.frame_state" {
		wc.emitBuildFrameStateFromBuf(c)
		return nil
	}
	return wc.emitExpr(c, ex.Target, wasmTypeInfo{kind: wkText})
}

func (wc *wasmCompiler) emitOpenScreenFromExpr(c *wasmCodeBuf, scrExpr Expr) error {
	scrT := wasmTypeInfo{kind: wkRec, name: "screen.screen"}
	scrPtr := wc.allocLocal(wasmI32)
	if err := wc.emitExpr(c, scrExpr, scrT); err != nil {
		return err
	}
	c.localSet(scrPtr)
	lay := wc.records["screen.screen"]
	// Parse win.fps (text like "60" or "unlimited") -> f64 (default 60.0 if <= 0 or non-numeric)
	okPtr := wc.allocLocal(wasmI32)
	fpsVal := wc.allocLocal(wasmF64)
	c.i32Const(8)
	c.call(wc.rt.fnAlloc)
	c.localSet(okPtr)
	c.localGet(scrPtr)
	c.memOp(opI64Load, 3, lay.byName["fps"].offset)
	c.localGet(okPtr)
	c.call(wc.rt.fnTextToF64)
	c.localSet(fpsVal)
	c.localGet(okPtr)
	c.memOp(opI32Load, 2, 0)
	c.op(opI32Eqz)
	c.ifOp(wasmBlockVoid)
	c.f64Const(60)
	c.localSet(fpsVal)
	c.op(opEnd)
	c.localGet(fpsVal)
	c.f64Const(0)
	c.op(opF64Le)
	c.ifOp(wasmBlockVoid)
	c.f64Const(60)
	c.localSet(fpsVal)
	c.op(opEnd)

	c.localGet(scrPtr)
	c.memOp(opF64Load, 3, lay.byName["width"].offset)
	c.localGet(scrPtr)
	c.memOp(opF64Load, 3, lay.byName["height"].offset)
	c.localGet(scrPtr)
	c.memOp(opI64Load, 3, lay.byName["title"].offset)
	titleTmp := wc.allocLocal(wasmI64)
	c.localTee(titleTmp)
	emitUnpackPtr(c)
	c.localGet(titleTmp)
	emitUnpackLen(c)
	c.localGet(fpsVal)
	c.call(wc.rt.impOpenScreen)
	c.i32Const(1)
	c.globalSet(wc.screenOpenedGlob)
	return nil
}

func (wc *wasmCompiler) emitCallExpr(c *wasmCodeBuf, ex *CallExpr) error {
	if ex.Module == "screen" {
		switch ex.Callee {
		case "open":
			if len(ex.Args) >= 1 {
				if err := wc.emitOpenScreenFromExpr(c, ex.Args[0]); err != nil {
					return err
				}
			}
			return nil
		case "present":
			if len(ex.Args) >= 1 {
				c.globalGet(wc.screenOpenedGlob)
				c.op(opI32Eqz)
				c.ifOp(wasmBlockVoid)
				if err := wc.emitOpenScreenFromExpr(c, ex.Args[0]); err != nil {
					return err
				}
				c.op(opEnd)
			}
			if len(ex.Args) >= 2 {
				if err := wc.emitExpr(c, ex.Args[1], wasmTypeInfo{kind: wkRec, name: "screen.scene"}); err != nil {
					return err
				}
				c.call(wc.rt.fnPresentScene)
			}
			c.i64Const(int64(wc.mod.internString("{}")))
			return nil
		}
	}
	if ex.Module == "random" {
		switch ex.Callee {
		case "roll":
			if err := wc.emitExpr(c, ex.Args[0], wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
			c.op(opI64TruncF64S)
			if err := wc.emitExpr(c, ex.Args[1], wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
			c.op(opI64TruncF64S)
			c.call(wc.rt.impRandomInt)
			c.op(opF64ConvertI64S)
			return nil
		case "pick":
			lt := wc.inferExprTypeWithEnv(ex.Args[0], wc.curEnvTypes(), "")
			listTmp := wc.allocLocal(wasmI32)
			if err := wc.emitExpr(c, ex.Args[0], lt); err != nil {
				return err
			}
			c.localTee(listTmp)
			c.i64Const(1)
			c.localGet(listTmp)
			c.memOp(opI32Load, 2, 4)
			c.op(opI64ExtendI32U)
			c.call(wc.rt.impRandomInt)
			c.op(opF64ConvertI64S)
			c.call(wc.rt.fnListGet)
			if lt.elemKind == wkNum {
				c.op(opF64ReinterpretI64)
			} else if lt.elemKind != wkText {
				c.op(opI32WrapI64)
			}
			return nil
		case "shuffle":
			lt := wc.inferExprTypeWithEnv(ex.Args[0], wc.curEnvTypes(), "")
			return wc.emitExpr(c, ex.Args[0], lt)
		}
	}
	if ex.Module == "synth" && ex.Callee == "play" {
		return nil
	}

	if ex.Module == "" {
		switch ex.Callee {
		case "abs", "floor", "round":
			if err := wc.emitExpr(c, ex.Args[0], wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
			switch ex.Callee {
			case "abs":
				c.op(opF64Abs)
			case "floor":
				c.op(opF64Floor)
			case "round":
				c.op(opF64Nearest)
			}
			return nil
		case "min", "max":
			if err := wc.emitExpr(c, ex.Args[0], wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Args[1], wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
			if ex.Callee == "min" {
				c.op(opF64Min)
			} else {
				c.op(opF64Max)
			}
			return nil
		case "count":
			at := wc.inferExprTypeWithEnv(ex.Args[0], wc.curEnvTypes(), "")
			if err := wc.emitExpr(c, ex.Args[0], at); err != nil {
				return err
			}
			if at.kind == wkText {
				emitUnpackLen(c)
				c.op(opF64ConvertI32U)
			} else {
				c.call(wc.rt.fnListLen)
			}
			return nil
		case "range":
			if err := wc.emitExpr(c, ex.Args[0], wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Args[1], wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
			c.call(wc.rt.fnListRange)
			return nil
		case "append":
			lt := wc.inferExprTypeWithEnv(ex.Args[0], wc.curEnvTypes(), "")
			if err := wc.emitExpr(c, ex.Args[0], lt); err != nil {
				return err
			}
			argT := wc.inferExprTypeWithEnv(ex.Args[1], wc.curEnvTypes(), "")
			// Handle union element wrapping when appending a concrete shape record into list<screen.shape>
			if lt.name == "screen.shape" && argT.kind == wkRec {
				if ul := wc.unions["screen.shape"]; ul != nil {
					var tag int32 = 1
					for _, vr := range ul.variants {
						if vr.name == argT.name {
							tag = vr.tag
							break
						}
					}
					recTmp := wc.allocLocal(wasmI32)
					if err := wc.emitExpr(c, ex.Args[1], argT); err != nil {
						return err
					}
					c.localSet(recTmp)
					uTmp := wc.allocLocal(wasmI32)
					c.i32Const(16)
					c.call(wc.rt.fnAlloc)
					c.localTee(uTmp)
					c.i32Const(tag)
					c.memOp(opI32Store, 2, 0)
					c.localGet(uTmp)
					c.localGet(recTmp)
					c.memOp(opI32Store, 2, 8)
					c.localGet(uTmp)
					c.op(opI64ExtendI32U)
					c.call(wc.rt.fnListPush)
					return nil
				}
			}
			if err := wc.emitExprAsI64(c, ex.Args[1], argT); err != nil {
				return err
			}
			c.call(wc.rt.fnListPush)
			return nil
		case "sum":
			if err := wc.emitExpr(c, ex.Args[0], wasmTypeInfo{kind: wkList, elemKind: wkNum}); err != nil {
				return err
			}
			c.call(wc.rt.fnListSum)
			return nil
		case "avg":
			if err := wc.emitExpr(c, ex.Args[0], wasmTypeInfo{kind: wkList, elemKind: wkNum}); err != nil {
				return err
			}
			c.call(wc.rt.fnListAvg)
			return nil
		case "sort":
			lt := wc.inferExprTypeWithEnv(ex.Args[0], wc.curEnvTypes(), "")
			if err := wc.emitExpr(c, ex.Args[0], lt); err != nil {
				return err
			}
			if lt.elemKind == wkText {
				c.call(wc.rt.fnListSortText)
			} else {
				c.call(wc.rt.fnListSortNum)
			}
			return nil
		case "slice":
			at := wc.inferExprTypeWithEnv(ex.Args[0], wc.curEnvTypes(), "")
			if err := wc.emitExpr(c, ex.Args[0], at); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Args[1], wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Args[2], wasmTypeInfo{kind: wkNum}); err != nil {
				return err
			}
			if at.kind == wkText {
				c.call(wc.rt.fnStrSlice)
			} else {
				c.call(wc.rt.fnListSlice)
			}
			return nil
		case "trim", "upper", "lower":
			if err := wc.emitExpr(c, ex.Args[0], wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
			switch ex.Callee {
			case "trim":
				c.call(wc.rt.fnStrTrim)
			case "upper":
				c.call(wc.rt.fnStrUpper)
			case "lower":
				c.call(wc.rt.fnStrLower)
			}
			return nil
		case "contains":
			cType := wc.inferExprTypeWithEnv(ex.Args[0], wc.curEnvTypes(), "")
			if cType.kind == wkList {
				if err := wc.emitExpr(c, ex.Args[0], cType); err != nil {
					return err
				}
				if cType.elemKind == wkText {
					if err := wc.emitExpr(c, ex.Args[1], wasmTypeInfo{kind: wkText}); err != nil {
						return err
					}
					c.call(wc.rt.fnListContainsText)
				} else {
					if err := wc.emitExpr(c, ex.Args[1], wasmTypeInfo{kind: wkNum}); err != nil {
						return err
					}
					c.call(wc.rt.fnListContainsNum)
				}
				return nil
			}
			if err := wc.emitExpr(c, ex.Args[0], wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Args[1], wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
			c.call(wc.rt.fnStrContains)
			return nil
		case "split":
			if err := wc.emitExpr(c, ex.Args[0], wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Args[1], wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
			c.call(wc.rt.fnStrSplit)
			return nil
		case "join":
			if err := wc.emitExpr(c, ex.Args[0], wasmTypeInfo{kind: wkList, elemKind: wkText}); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Args[1], wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
			c.call(wc.rt.fnStrJoin)
			return nil
		case "replace":
			if err := wc.emitExpr(c, ex.Args[0], wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Args[1], wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
			if err := wc.emitExpr(c, ex.Args[2], wasmTypeInfo{kind: wkText}); err != nil {
				return err
			}
			c.call(wc.rt.fnStrReplace)
			return nil
		case "close":
			if wc.hasScreenLoop {
				c.i32Const(0)
				c.globalSet(wc.activeGlobal)
			}
			return nil
		}
	}

	key := ex.Callee
	if ex.Module != "" {
		key = ex.Module + ":" + ex.Callee
	}
	fi := wc.funcs[key]
	if fi == nil && wc.curFn != nil && wc.curFn.modAlias != "" && ex.Module == "" {
		fi = wc.funcs[wc.curFn.modAlias+":"+ex.Callee]
	}
	if fi == nil {
		return fmt.Errorf("wasm: unsupported or unknown function %q", key)
	}
	for i, arg := range ex.Args {
		pt := wasmTypeInfo{kind: wkNum}
		if i < len(fi.paramTypes) {
			pt = fi.paramTypes[i]
		}
		if err := wc.emitExpr(c, arg, pt); err != nil {
			return err
		}
	}
	c.call(fi.fnIdx)
	return nil
}

// Helper for unused strings import check
var _ = strings.TrimSpace
