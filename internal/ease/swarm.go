package ease

import (
	"bytes"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
)

func maxSwarmWorkers() int {
	return max(4, min(64, runtime.NumCPU()*2))
}

func freezeValueForParallel(v Value, seen map[any]bool) {
	switch val := v.(type) {
	case *ListValue:
		if val == nil || seen[val] {
			return
		}
		seen[val] = true
		val.pushed = true
		for _, el := range val.Elements {
			freezeValueForParallel(el, seen)
		}
	case *RecordValue:
		if val == nil || seen[val] {
			return
		}
		seen[val] = true
		for _, fv := range val.Fields {
			freezeValueForParallel(fv, seen)
		}
	case *MapValue:
		if val == nil || seen[val] {
			return
		}
		seen[val] = true
		for _, fv := range val.Fields {
			freezeValueForParallel(fv, seen)
		}
	case *ModuleValue:
		if val == nil || seen[val] || val.Env == nil {
			return
		}
		seen[val] = true
		freezeEnvForParallel(val.Env, seen)
	}
}

func freezeEnvForParallel(env *Env, seen map[any]bool) {
	for cur := env; cur != nil; cur = cur.parent {
		cur.mu.RLock()
		vals := make([]Value, 0, len(cur.vars))
		for _, b := range cur.vars {
			vals = append(vals, b.val)
		}
		cur.mu.RUnlock()
		for _, v := range vals {
			freezeValueForParallel(v, seen)
		}
	}
}

func (ev *Evaluator) forkWorker(outBuf io.Writer) *Evaluator {
	w := &Evaluator{
		source:         ev.source,
		out:            outBuf,
		trace:          ev.trace,
		ioMu:           ev.ioMu,
		rt:             ev.rt,
		isChild:        true,
		inReader:       nil,
		modules:        make(map[string]*Evaluator, len(ev.modules)),
		global:         ev.global,
		callDepth:      0,
		inSwarmWorker:  true,
		headlessScreen: ev.headlessScreen,
		headlessNet:    ev.headlessNet,
		headlessAudio:  ev.headlessAudio,
		httpClient:     ev.httpClient,
	}
	for alias, modEv := range ev.modules {
		w.modules[alias] = modEv.forkWorker(outBuf)
	}
	return w
}

func (ev *Evaluator) evalSwarmLoop(e *SwarmLoopExpr, env *Env) (Value, error) {
	iterVal, err := ev.EvalExpr(e.Iterable, env)
	if err != nil {
		return nil, err
	}
	if err := ev.checkNotConditionValue(iterVal, e.Line, e.Col); err != nil {
		return nil, err
	}
	lv, ok := iterVal.(*ListValue)
	if !ok {
		return nil, newError(
			"RuntimeError",
			e.Line,
			e.Col,
			ev.source,
			fmt.Sprintf("'swarm loop' requires a list or range, got %s", TypeName(iterVal)),
			"Iterate over a list like 'swarm loop xs ... end' or a range like 'swarm loop range 1 5 ... end'.",
		)
	}

	n := len(lv.Elements)
	if n == 0 {
		return &ListValue{ElemType: "", Elements: []Value{}}, nil
	}

	// Freeze outer/shared lists before launching workers so `append` never mutates shared backing arrays.
	seen := make(map[any]bool)
	freezeValueForParallel(lv, seen)
	freezeEnvForParallel(env, seen)

	assigned := collectAssignedNames(e.Body)
	results := make([]Value, n)
	outs := make([]bytes.Buffer, n)

	hasReturn := stmtsHaveReturn(e.Body)
	runItem := func(idx int) error {
		worker := ev.forkWorker(&outs[idx])
		workerEnv := NewWorkerEnv(env, assigned)
		workerEnv.Define("it", lv.Elements[idx], false)
		workerEnv.SetLoopIndex(float64(idx + 1))

		_, sig, execErr := worker.execBlock(e.Body, workerEnv)
		if execErr != nil {
			return execErr
		}
		var val Value
		if sig != nil {
			if sig.loopStop {
				return newError(
					"RuntimeError",
					e.Line,
					e.Col,
					ev.source,
					"'-> stop' cannot be used inside 'swarm loop'",
					"Use '-> <value>' or 'check <cond> -> <value>' to return from a 'swarm loop' iteration.",
				)
			}
			val = sig.val
		} else if hasReturn {
			return newError(
				"RuntimeError",
				e.Line,
				e.Col,
				ev.source,
				"'swarm loop' iteration finished without returning a value via '->'",
				"End each path in 'swarm loop' with '-> <value>'.",
			)
		}
		if hasReturn {
			if err := worker.checkNotConditionValue(val, e.Line, e.Col); err != nil {
				return err
			}
		}
		results[idx] = val
		return nil
	}

	if n == 1 {
		if err := runItem(0); err != nil {
			return nil, err
		}
	} else {
		errs := make([]error, n)
		workers := min(n, maxSwarmWorkers())
		var nextIdx atomic.Int64
		var firstErrIdx atomic.Int64
		firstErrIdx.Store(-1)

		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				for {
					idx := int(nextIdx.Add(1) - 1)
					if idx >= n {
						return
					}
					if errIdx := firstErrIdx.Load(); errIdx >= 0 && int64(idx) > errIdx {
						return
					}
					if runErr := runItem(idx); runErr != nil {
						errs[idx] = runErr
						for {
							cur := firstErrIdx.Load()
							if cur >= 0 && cur <= int64(idx) {
								break
							}
							if firstErrIdx.CompareAndSwap(cur, int64(idx)) {
								break
							}
						}
						return
					}
				}
			}()
		}
		wg.Wait()

		for i := 0; i < n; i++ {
			if errs[i] != nil {
				return nil, errs[i]
			}
		}
	}

	for i := 0; i < n; i++ {
		if outs[i].Len() > 0 {
			ev.writeOutString(outs[i].String())
		}
	}

	if !hasReturn {
		return nil, nil
	}

	firstType := structuredTypeName(results[0])
	for i := 1; i < n; i++ {
		gotType := structuredTypeName(results[i])
		if gotType != firstType {
			return nil, newError(
				"TypeError",
				e.Line,
				e.Col,
				ev.source,
				fmt.Sprintf("Mixed result types in 'swarm loop': expected %s, got %s", firstType, gotType),
				"All iterations of 'swarm loop' must return the same type.",
			)
		}
	}
	elemType := firstType
	if elemType == "none" {
		elemType = ""
	}
	return &ListValue{ElemType: elemType, Elements: results}, nil
}
