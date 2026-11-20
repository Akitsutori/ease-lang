package ease

import (
	"bufio"
	"context"
	"fmt"
	"net"
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
	onClose  []func()
}

func newInboxValue(elemType string) *InboxValue {
	return &InboxValue{
		ElemType: elemType,
		ch:       make(chan Value, defaultInboxCapacity),
		done:     make(chan struct{}),
	}
}

// AddOnClose registers a callback invoked once when the inbox is closed (or immediately if already closed).
func (ib *InboxValue) AddOnClose(fn func()) {
	if ib == nil || fn == nil {
		return
	}
	ib.mu.Lock()
	if ib.closed {
		ib.mu.Unlock()
		fn()
		return
	}
	ib.onClose = append(ib.onClose, fn)
	ib.mu.Unlock()
}

// IsClosed reports whether the inbox has been closed.
func (ib *InboxValue) IsClosed() bool {
	if ib == nil {
		return true
	}
	ib.mu.Lock()
	c := ib.closed
	ib.mu.Unlock()
	return c
}

// Close closes the inbox idempotently so pending readers drain buffered messages and exit cleanly.
func (ib *InboxValue) Close() {
	if ib == nil {
		return
	}
	ib.mu.Lock()
	var callbacks []func()
	if !ib.closed {
		ib.closed = true
		close(ib.done)
		callbacks = ib.onClose
		ib.onClose = nil
	}
	ib.mu.Unlock()
	for _, fn := range callbacks {
		if fn != nil {
			fn()
		}
	}
}

// TryPut freezes and enqueues `val` without blocking. Returns false if the inbox is closed or full.
func (ib *InboxValue) TryPut(val Value) bool {
	if ib == nil {
		return false
	}
	ib.mu.Lock()
	closed := ib.closed
	ib.mu.Unlock()
	if closed {
		return false
	}
	freezeValueForParallel(val, map[any]bool{})
	select {
	case <-ib.done:
		return false
	case ib.ch <- val:
		return true
	default:
		return false
	}
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

type netListenerHandle struct {
	id     string
	ln     net.Listener
	mu     sync.Mutex
	closed bool
}

func (h *netListenerHandle) isClosed() bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	c := h.closed
	h.mu.Unlock()
	return c
}

func (h *netListenerHandle) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	already := h.closed
	h.closed = true
	ln := h.ln
	h.mu.Unlock()
	if !already && ln != nil {
		_ = ln.Close()
	}
}

type netConnHandle struct {
	id      string
	conn    net.Conn
	reader  *bufio.Reader
	readMu  sync.Mutex
	writeMu sync.Mutex
	mu      sync.Mutex
	closed  bool
}

func (h *netConnHandle) isClosed() bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	c := h.closed
	h.mu.Unlock()
	return c
}

func (h *netConnHandle) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	already := h.closed
	h.closed = true
	c := h.conn
	h.mu.Unlock()
	if !already && c != nil {
		_ = c.Close()
	}
}

type runtimeLifecycle struct {
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	mu               sync.Mutex
	inboxes          []*InboxValue
	listeners        map[string]*netListenerHandle
	conns            map[string]*netConnHandle
	netSeq           uint64
	firstErr         error
	closed           bool
	screenMu         sync.Mutex
	activeScreen     *screenSession
	screenEvents     *InboxValue
	screenFrameCount int
}

func newRuntimeLifecycle() *runtimeLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &runtimeLifecycle{
		ctx:       ctx,
		cancel:    cancel,
		listeners: make(map[string]*netListenerHandle),
		conns:     make(map[string]*netConnHandle),
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

func (rt *runtimeLifecycle) isAborted() bool {
	if rt == nil {
		return false
	}
	rt.mu.Lock()
	c := rt.closed
	rt.mu.Unlock()
	return c
}

func (rt *runtimeLifecycle) registerListener(ln net.Listener) *netListenerHandle {
	if rt == nil || ln == nil {
		return nil
	}
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	rt.netSeq++
	id := fmt.Sprintf("listener-%d", rt.netSeq)
	h := &netListenerHandle{id: id, ln: ln}
	if rt.listeners == nil {
		rt.listeners = make(map[string]*netListenerHandle)
	}
	rt.listeners[id] = h
	rt.mu.Unlock()
	return h
}

func (rt *runtimeLifecycle) getListener(id string) (*netListenerHandle, bool) {
	if rt == nil || id == "" {
		return nil, false
	}
	rt.mu.Lock()
	h, ok := rt.listeners[id]
	rt.mu.Unlock()
	return h, ok
}

func (rt *runtimeLifecycle) closeListener(id string) {
	if rt == nil || id == "" {
		return
	}
	rt.mu.Lock()
	h, ok := rt.listeners[id]
	if ok {
		delete(rt.listeners, id)
	}
	rt.mu.Unlock()
	if ok {
		h.close()
	}
}

func (rt *runtimeLifecycle) registerConn(conn net.Conn) *netConnHandle {
	if rt == nil || conn == nil {
		return nil
	}
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		_ = conn.Close()
		return nil
	}
	rt.netSeq++
	id := fmt.Sprintf("conn-%d", rt.netSeq)
	h := &netConnHandle{
		id:     id,
		conn:   conn,
		reader: bufio.NewReader(conn),
	}
	if rt.conns == nil {
		rt.conns = make(map[string]*netConnHandle)
	}
	rt.conns[id] = h
	rt.mu.Unlock()
	return h
}

func (rt *runtimeLifecycle) getConn(id string) (*netConnHandle, bool) {
	if rt == nil || id == "" {
		return nil, false
	}
	rt.mu.Lock()
	h, ok := rt.conns[id]
	rt.mu.Unlock()
	return h, ok
}

func (rt *runtimeLifecycle) closeConn(id string) {
	if rt == nil || id == "" {
		return
	}
	rt.mu.Lock()
	h, ok := rt.conns[id]
	if ok {
		delete(rt.conns, id)
	}
	rt.mu.Unlock()
	if ok {
		h.close()
	}
}

func (rt *runtimeLifecycle) abort() {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	rt.closed = true
	inboxes := make([]*InboxValue, len(rt.inboxes))
	copy(inboxes, rt.inboxes)
	listeners := make([]*netListenerHandle, 0, len(rt.listeners))
	for _, h := range rt.listeners {
		listeners = append(listeners, h)
	}
	rt.listeners = make(map[string]*netListenerHandle)
	conns := make([]*netConnHandle, 0, len(rt.conns))
	for _, h := range rt.conns {
		conns = append(conns, h)
	}
	rt.conns = make(map[string]*netConnHandle)
	rt.mu.Unlock()

	rt.cancel()
	for _, ib := range inboxes {
		ib.Close()
	}
	for _, h := range listeners {
		h.close()
	}
	for _, h := range conns {
		h.close()
	}

	rt.screenMu.Lock()
	sess := rt.activeScreen
	rt.activeScreen = nil
	rt.screenMu.Unlock()
	if sess != nil {
		sess.close()
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
