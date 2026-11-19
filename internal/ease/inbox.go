package ease

import (
	"context"
	"fmt"
	"sync"
)

const defaultInboxCapacity = 64

// InboxValue is a typed, concurrent FIFO message channel created by `inbox as <type>`.
type InboxValue struct {
	ElemType string
	ch       chan Value
	done     chan struct{}
	mu       sync.Mutex
	closed   bool
}

func newInboxValue(elemType string) *InboxValue {
	return &InboxValue{
		ElemType: elemType,
		ch:       make(chan Value, defaultInboxCapacity),
		done:     make(chan struct{}),
	}
}

// Close closes the inbox idempotently so pending readers drain buffered messages and exit cleanly.
func (ib *InboxValue) Close() {
	if ib == nil {
		return
	}
	ib.mu.Lock()
	if !ib.closed {
		ib.closed = true
		close(ib.done)
	}
	ib.mu.Unlock()
}

// Put sends `val` into the inbox, returning a RuntimeError if the inbox is already closed.
func (ib *InboxValue) Put(ctx context.Context, val Value, line, col int, source string) error {
	ib.mu.Lock()
	if ib.closed {
		ib.mu.Unlock()
		return newError(
			"RuntimeError",
			line,
			col,
			source,
			"Cannot 'put' into a closed inbox",
			"Ensure 'close' is only called after all producers finish sending.",
		)
	}
	ib.mu.Unlock()

	select {
	case <-ib.done:
		return newError(
			"RuntimeError",
			line,
			col,
			source,
			"Cannot 'put' into a closed inbox",
			"Ensure 'close' is only called after all producers finish sending.",
		)
	default:
	}

	select {
	case ib.ch <- val:
		return nil
	case <-ib.done:
		return newError(
			"RuntimeError",
			line,
			col,
			source,
			"Cannot 'put' into a closed inbox",
			"Ensure 'close' is only called after all producers finish sending.",
		)
	case <-ctx.Done():
		return newError(
			"RuntimeError",
			line,
			col,
			source,
			"Inbox 'put' cancelled during shutdown",
			"",
		)
	}
}

// Recv reads the next message from the inbox, draining buffered messages after close before returning ok=false.
func (ib *InboxValue) Recv(ctx context.Context) (Value, bool, error) {
	select {
	case v := <-ib.ch:
		return v, true, nil
	default:
	}

	select {
	case v := <-ib.ch:
		return v, true, nil
	case <-ib.done:
		select {
		case v := <-ib.ch:
			return v, true, nil
		default:
			return nil, false, nil
		}
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

type runtimeLifecycle struct {
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	inboxes  []*InboxValue
	firstErr error
	closed   bool
}

func newRuntimeLifecycle() *runtimeLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &runtimeLifecycle{
		ctx:    ctx,
		cancel: cancel,
	}
}

func (rt *runtimeLifecycle) registerInbox(ib *InboxValue) {
	if rt == nil || ib == nil {
		return
	}
	rt.mu.Lock()
	closed := rt.closed
	if !closed {
		rt.inboxes = append(rt.inboxes, ib)
	}
	rt.mu.Unlock()
	if closed {
		ib.Close()
	}
}

func (rt *runtimeLifecycle) getErr() error {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	err := rt.firstErr
	rt.mu.Unlock()
	return err
}

func (rt *runtimeLifecycle) recordError(err error) {
	if rt == nil || err == nil {
		return
	}
	rt.mu.Lock()
	if rt.firstErr == nil {
		rt.firstErr = err
	}
	rt.mu.Unlock()
	rt.abort()
}

func (rt *runtimeLifecycle) abort() {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	rt.closed = true
	inboxes := make([]*InboxValue, len(rt.inboxes))
	copy(inboxes, rt.inboxes)
	rt.mu.Unlock()

	rt.cancel()
	for _, ib := range inboxes {
		ib.Close()
	}
}

func (rt *runtimeLifecycle) waitWorkers() error {
	if rt == nil {
		return nil
	}
	rt.wg.Wait()
	return rt.getErr()
}

func (ev *Evaluator) ctx() context.Context {
	if ev != nil && ev.rt != nil && ev.rt.ctx != nil {
		return ev.rt.ctx
	}
	return context.Background()
}

// WaitWorkers blocks until all spawned background workers complete, returning the first worker error if any.
func (ev *Evaluator) WaitWorkers() error {
	if ev == nil || ev.rt == nil {
		return nil
	}
	return ev.rt.waitWorkers()
}

func (ev *Evaluator) evalInboxExpr(e *InboxExpr) (*InboxValue, error) {
	if e.ElemType == "csv" || e.ElemType == "bytes" {
		return nil, newError(
			"TypeError",
			e.TypeLine,
			e.TypeCol,
			ev.source,
			fmt.Sprintf("Type %q cannot be used with 'inbox as'", e.ElemType),
			"Use 'text', 'number', a 'def' record, or a union type.",
		)
	}
	if e.ElemType != "number" && e.ElemType != "text" {
		protoVal, ok := lookupTypeInEnv(ev.global, e.ElemType)
		if !ok {
			return nil, newError(
				"TypeError",
				e.TypeLine,
				e.TypeCol,
				ev.source,
				fmt.Sprintf("Unknown type %q in 'inbox as %s'", e.ElemType, e.ElemType),
				"Use 'text', 'number', a defined 'def' record, or a union type.",
			)
		}
		_, isRec := protoVal.(*RecordValue)
		_, isUnion := protoVal.(*UnionValue)
		if !isRec && !isUnion {
			return nil, newError(
				"TypeError",
				e.TypeLine,
				e.TypeCol,
				ev.source,
				fmt.Sprintf("%q is not a 'def' record or union type", e.ElemType),
				"Use 'text', 'number', a defined 'def' record, or a union type.",
			)
		}
	}
	ib := newInboxValue(e.ElemType)
	if ev.rt != nil {
		ev.rt.registerInbox(ib)
	}
	return ib, nil
}

func (ev *Evaluator) execSpawnStmt(s *SpawnStmt, env *Env) (Value, *reportSignal, error) {
	args := make([]Value, len(s.Call.Args))
	seen := make(map[any]bool)
	for i, a := range s.Call.Args {
		v, err := ev.EvalExpr(a, env)
		if err != nil {
			return nil, nil, err
		}
		if err := ev.checkNotConditionValue(v, s.Line, s.Col); err != nil {
			return nil, nil, err
		}
		freezeValueForParallel(v, seen)
		args[i] = v
	}
	freezeEnvForParallel(env, seen)

	worker := ev.forkWorker(ev.out)
	worker.inSwarmWorker = false

	target := worker
	if s.Call.Module != "" {
		modEv, ok := worker.modules[s.Call.Module]
		if !ok {
			return nil, nil, newError("RuntimeError", s.Line, s.Col, ev.source, fmt.Sprintf("Unknown module %q", s.Call.Module), "")
		}
		target = modEv
	}

	if ev.rt == nil {
		ev.rt = newRuntimeLifecycle()
	}
	ev.rt.wg.Add(1)
	go func() {
		defer ev.rt.wg.Done()
		if _, err := target.callFunction(s.Call.Callee, args, s.Line, s.Col); err != nil {
			ev.rt.recordError(err)
		}
	}()
	return nil, nil, nil
}

func (ev *Evaluator) execTakeStmt(s *TakeStmt, env *Env) (Value, *reportSignal, error) {
	inboxVal, err := ev.EvalExpr(s.Inbox, env)
	if err != nil {
		return nil, nil, err
	}
	if err := ev.checkNotConditionValue(inboxVal, s.Line, s.Col); err != nil {
		return nil, nil, err
	}
	ib, ok := inboxVal.(*InboxValue)
	if !ok {
		return nil, nil, newError(
			"TypeError",
			s.Line,
			s.Col,
			ev.source,
			fmt.Sprintf("'take' requires an inbox, got %s", TypeName(inboxVal)),
			"Create an inbox with 'ch = inbox as <type>' first.",
		)
	}

	var last Value
	idx := 0
	for {
		item, hasItem, recvErr := ib.Recv(ev.ctx())
		if recvErr != nil {
			if workerErr := ev.rt.getErr(); workerErr != nil {
				return nil, nil, workerErr
			}
			return nil, nil, newError("RuntimeError", s.Line, s.Col, ev.source, "Inbox 'take' cancelled", "")
		}
		if !hasItem {
			break
		}
		takeEnv := NewEnv(env)
		takeEnv.Define("it", item, false)
		takeEnv.SetLoopIndex(float64(idx))
		val, sig, execErr := ev.execBlock(s.Body, takeEnv)
		if execErr != nil {
			return nil, nil, execErr
		}
		if sig != nil {
			if sig.loopEnd {
				break
			}
			return val, sig, nil
		}
		last = val
		idx++
	}
	if workerErr := ev.rt.getErr(); workerErr != nil {
		return nil, nil, workerErr
	}
	return last, nil, nil
}
