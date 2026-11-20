package ease

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
)

type debugServer struct {
	svc  *Service
	ln   net.Listener
	addr string
	port int

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

func startDebugServer(svc *Service, port int) (*debugServer, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("debug port %d out of range (0..65535)", port)
	}
	// Strictly bind loopback 127.0.0.1 only, never 0.0.0.0.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	actualPort := port
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		actualPort = tcpAddr.Port
	}
	ds := &debugServer{
		svc:   svc,
		ln:    ln,
		addr:  ln.Addr().String(),
		port:  actualPort,
		conns: make(map[net.Conn]struct{}),
	}
	ds.wg.Add(1)
	go ds.acceptLoop()
	return ds, nil
}

func (ds *debugServer) acceptLoop() {
	defer ds.wg.Done()
	for {
		conn, err := ds.ln.Accept()
		if err != nil {
			return
		}
		ds.mu.Lock()
		if ds.closed {
			ds.mu.Unlock()
			_ = conn.Close()
			return
		}
		ds.conns[conn] = struct{}{}
		ds.mu.Unlock()

		ds.wg.Add(1)
		go ds.handleConn(conn)
	}
}

func (ds *debugServer) handleConn(conn net.Conn) {
	defer ds.wg.Done()
	defer func() {
		ds.mu.Lock()
		delete(ds.conns, conn)
		ds.mu.Unlock()
		_ = conn.Close()
	}()

	_, _ = fmt.Fprintf(conn, "ease debug ready (%s) — type :help\n", ds.svc.entryPath)

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var pending strings.Builder
	for scanner.Scan() {
		rawLine := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(rawLine)

		if pending.Len() == 0 {
			switch trimmed {
			case "":
				continue
			case ":help":
				_, _ = fmt.Fprintln(conn, "Commands:")
				_, _ = fmt.Fprintln(conn, "  :help    - Show available debug commands")
				_, _ = fmt.Fprintln(conn, "  :status  - Show active listeners, workers, and inboxes")
				_, _ = fmt.Fprintln(conn, "  :funcs   - List loaded 'to' functions")
				_, _ = fmt.Fprintln(conn, "  :reload  - Hot-reload the entry file cleanly")
				_, _ = fmt.Fprintln(conn, "  :quit    - Close this debug session")
				_, _ = fmt.Fprintln(conn, "  <ease>   - Evaluate Ease code or 'to name args -> expr'")
				continue
			case ":status":
				ds.writeStatus(conn)
				continue
			case ":funcs":
				ds.writeFuncs(conn)
				continue
			case ":reload":
				if err := ds.svc.HotReloadFile(ds.svc.entryPath); err != nil {
					_, _ = fmt.Fprintf(conn, "error: %v\n", err)
				} else {
					_, _ = fmt.Fprintf(conn, "ok: reloaded %s\n", ds.svc.entryPath)
				}
				continue
			case ":quit", ":exit":
				_, _ = fmt.Fprintln(conn, "bye")
				return
			case ":reset":
				pending.Reset()
				_, _ = fmt.Fprintln(conn, "ok: buffer cleared")
				continue
			}
		}

		if pending.Len() > 0 {
			pending.WriteByte('\n')
		}
		pending.WriteString(rawLine)
		chunk := pending.String()

		res, err := ds.svc.EvalDebugChunk(chunk)
		if err != nil {
			if IsIncompleteBlockError(err) {
				continue
			}
			pending.Reset()
			_, _ = fmt.Fprintf(conn, "error: %v\n", err)
			continue
		}
		pending.Reset()
		if res != "" {
			_, _ = fmt.Fprintln(conn, res)
		} else {
			_, _ = fmt.Fprintln(conn, "ok")
		}
	}
}

func (ds *debugServer) writeStatus(conn net.Conn) {
	listeners := ds.svc.Listeners()
	workers := ds.svc.ActiveWorkers()
	inboxes := ds.svc.Inboxes()

	_, _ = fmt.Fprintf(conn, "listeners: %d, workers: %d, inboxes: %d\n", len(listeners), workers, len(inboxes))
	for _, l := range listeners {
		_, _ = fmt.Fprintf(conn, "  listener %s (%s) %s\n", l.ID, l.Kind, l.Addr)
	}
	for _, ib := range inboxes {
		state := "open"
		if ib.Closed {
			state = "closed"
		}
		_, _ = fmt.Fprintf(conn, "  inbox %s: inbox<%s> (buffered=%d, %s)\n", ib.Name, ib.ElemType, ib.Buffered, state)
	}
}

func (ds *debugServer) writeFuncs(conn net.Conn) {
	funcs := ds.svc.Funcs()
	_, _ = fmt.Fprintf(conn, "functions: %d\n", len(funcs))
	for _, fn := range funcs {
		params := ""
		if len(fn.Params) > 0 {
			params = " " + strings.Join(fn.Params, " ")
		}
		_, _ = fmt.Fprintf(conn, "  to %s%s\n", fn.Name, params)
	}
}

func (ds *debugServer) close() {
	if ds == nil {
		return
	}
	ds.mu.Lock()
	already := ds.closed
	ds.closed = true
	conns := make([]net.Conn, 0, len(ds.conns))
	for c := range ds.conns {
		conns = append(conns, c)
	}
	ds.conns = make(map[net.Conn]struct{})
	ds.mu.Unlock()

	if !already {
		_ = ds.ln.Close()
		for _, c := range conns {
			_ = c.Close()
		}
	}
	ds.wg.Wait()
}
