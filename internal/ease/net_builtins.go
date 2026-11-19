package ease

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	maxNetReadBytes = maxHTTPBodyBytes
	maxNetLineBytes = 1024 * 1024
	netReadTimeout  = 10 * time.Second
)

func newServerHTTPRequestRecord(id, method, urlStr, path, body, remote string) *RecordValue {
	return &RecordValue{
		TypeName: "http_request",
		Fields: map[string]Value{
			"id":      id,
			"method":  method,
			"url":     urlStr,
			"path":    path,
			"body":    body,
			"remote":  remote,
			"token":   "",
			"timeout": float64(5),
		},
		Order: []string{"id", "method", "url", "path", "body", "remote", "token", "timeout"},
	}
}

func newTCPClientRecord(id, remote string, lines *InboxValue) *RecordValue {
	return &RecordValue{
		TypeName: "tcp_client",
		Fields: map[string]Value{
			"id":     id,
			"remote": remote,
			"lines":  lines,
		},
		Order: []string{"id", "remote", "lines"},
	}
}

func parsePortNumber(v Value, allowZero bool, fnName string, line, col int, src string) (int, error) {
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n {
		return 0, newError("RuntimeError", line, col, src,
			fmt.Sprintf("%s requires an integer port number, got %s", fnName, TypeName(v)), "")
	}
	port := int(n)
	minPort := 1
	if allowZero {
		minPort = 0
	}
	if port < minPort || port > 65535 {
		return 0, newError("RuntimeError", line, col, src,
			fmt.Sprintf("%s port %d out of range (%d..65535)", fnName, port, minPort), "")
	}
	return port, nil
}

func (ev *Evaluator) execNetServeHTTP(args []Value, line, col int) (Value, error) {
	port, err := parsePortNumber(args[0], true, "net_serve_http", line, col, ev.source)
	if err != nil {
		return nil, err
	}
	requests, ok := args[1].(*InboxValue)
	if !ok {
		return nil, newError("TypeError", line, col, ev.source,
			fmt.Sprintf("net_serve_http requires an inbox as second argument, got %s", TypeName(args[1])),
			"Create an inbox with 'requests = inbox as network.http_request' first.")
	}
	if ev.headlessNet || requests.IsClosed() || ev.rt.isAborted() {
		return float64(0), nil
	}

	lc := net.ListenConfig{}
	ln, err := lc.Listen(ev.rt.ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("network:serve_http failed on 127.0.0.1:%d: %v", port, err),
			"Check that the port is not already in use.")
	}
	actualPort := port
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		actualPort = tcpAddr.Port
	}
	lh := ev.rt.registerListener(ln)
	if lh == nil {
		return float64(0), nil
	}
	requests.AddOnClose(func() {
		ev.rt.closeListener(lh.id)
	})

	ev.rt.wg.Add(1)
	go func() {
		defer ev.rt.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if lh.isClosed() || requests.IsClosed() || ev.rt.isAborted() {
				_ = conn.Close()
				return
			}
			ev.rt.wg.Add(1)
			go func(c net.Conn) {
				defer ev.rt.wg.Done()
				_ = c.SetReadDeadline(time.Now().Add(netReadTimeout))
				br := bufio.NewReader(io.LimitReader(c, maxNetReadBytes))
				httpReq, err := http.ReadRequest(br)
				if err != nil {
					_ = c.Close()
					return
				}
				rawBody, err := io.ReadAll(io.LimitReader(httpReq.Body, maxNetReadBytes+1))
				_ = httpReq.Body.Close()
				if err != nil || len(rawBody) > maxNetReadBytes || !utf8.Valid(rawBody) {
					_, _ = io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
					_ = c.Close()
					return
				}
				_ = c.SetReadDeadline(time.Time{})

				ch := ev.rt.registerConn(c)
				if ch == nil {
					return
				}
				remote := ""
				if c.RemoteAddr() != nil {
					remote = c.RemoteAddr().String()
				}
				rec := newServerHTTPRequestRecord(
					ch.id,
					strings.ToUpper(httpReq.Method),
					httpReq.RequestURI,
					httpReq.URL.Path,
					norm.NFC.String(string(rawBody)),
					remote,
				)
				if err := requests.Put(ev.rt.ctx, rec, line, col, ev.source); err != nil {
					ev.rt.closeConn(ch.id)
				}
			}(conn)
		}
	}()

	return float64(actualPort), nil
}

func (ev *Evaluator) execNetReplyCommon(reqVal, statusVal Value, contentType, body string, line, col int) (Value, error) {
	reqRec, ok := reqVal.(*RecordValue)
	if !ok {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("reply requires an http_request record, got %s", TypeName(reqVal)), "")
	}
	statusNum, ok := statusVal.(float64)
	if !ok || math.IsNaN(statusNum) || math.IsInf(statusNum, 0) || math.Trunc(statusNum) != statusNum {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("reply requires an integer HTTP status code, got %s", TypeName(statusVal)), "")
	}
	status := int(statusNum)
	if status < 100 || status > 599 {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("HTTP status code %d out of range (100..599)", status), "")
	}

	id, _ := reqRec.Fields["id"].(string)
	if id == "" {
		return "", nil
	}
	ch, ok := ev.rt.getConn(id)
	if !ok || ch.isClosed() || ev.rt.isAborted() {
		return "", nil
	}

	statusText := http.StatusText(status)
	if statusText == "" {
		statusText = "Status"
	}
	payload := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, statusText, contentType, len(body), body,
	)

	ch.writeMu.Lock()
	_, writeErr := writeAllBytes(ch.conn, []byte(payload))
	ch.writeMu.Unlock()
	ev.rt.closeConn(id)

	if writeErr != nil {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("failed to write HTTP reply: %v", writeErr), "")
	}
	return "", nil
}

func (ev *Evaluator) execNetReplyText(args []Value, line, col int) (Value, error) {
	body, ok := args[2].(string)
	if !ok {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("net_reply_text requires a text body, got %s", TypeName(args[2])), "")
	}
	return ev.execNetReplyCommon(args[0], args[1], "text/plain; charset=utf-8", body, line, col)
}

func (ev *Evaluator) execNetReplyJSON(args []Value, line, col int) (Value, error) {
	if err := validateJSONEncodable(args[2]); err != nil {
		return nil, newError("TypeError", line, col, ev.source, err.Error(),
			"Conditions and union definitions cannot be encoded as JSON.")
	}
	b, err := json.Marshal(easeValueToJSON(args[2]))
	if err != nil {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("Cannot encode %s to JSON: %v", TypeName(args[2]), err), "")
	}
	return ev.execNetReplyCommon(args[0], args[1], "application/json; charset=utf-8", string(b), line, col)
}

func (ev *Evaluator) execNetServeTCP(args []Value, line, col int) (Value, error) {
	port, err := parsePortNumber(args[0], true, "net_serve_tcp", line, col, ev.source)
	if err != nil {
		return nil, err
	}
	clients, ok := args[1].(*InboxValue)
	if !ok {
		return nil, newError("TypeError", line, col, ev.source,
			fmt.Sprintf("net_serve_tcp requires an inbox as second argument, got %s", TypeName(args[1])),
			"Create an inbox with 'clients = inbox as network.tcp_client' first.")
	}
	if ev.headlessNet || clients.IsClosed() || ev.rt.isAborted() {
		return float64(0), nil
	}

	lc := net.ListenConfig{}
	ln, err := lc.Listen(ev.rt.ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("network:serve_tcp failed on 127.0.0.1:%d: %v", port, err),
			"Check that the port is not already in use.")
	}
	actualPort := port
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		actualPort = tcpAddr.Port
	}
	lh := ev.rt.registerListener(ln)
	if lh == nil {
		return float64(0), nil
	}
	clients.AddOnClose(func() {
		ev.rt.closeListener(lh.id)
	})

	ev.rt.wg.Add(1)
	go func() {
		defer ev.rt.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if lh.isClosed() || clients.IsClosed() || ev.rt.isAborted() {
				_ = conn.Close()
				return
			}
			ch := ev.rt.registerConn(conn)
			if ch == nil {
				continue
			}
			lines := newInboxValue("text")
			ev.rt.registerInbox(lines)
			lines.AddOnClose(func() {
				ev.rt.closeConn(ch.id)
			})
			remote := ""
			if conn.RemoteAddr() != nil {
				remote = conn.RemoteAddr().String()
			}
			clientRec := newTCPClientRecord(ch.id, remote, lines)
			if err := clients.Put(ev.rt.ctx, clientRec, line, col, ev.source); err != nil {
				lines.Close()
				continue
			}

			ev.rt.wg.Add(1)
			go func(cHandle *netConnHandle, lineInbox *InboxValue) {
				defer ev.rt.wg.Done()
				defer lineInbox.Close()
				for {
					textLine, ok := readNetLineFromConn(cHandle, ev.rt)
					if !ok {
						return
					}
					if err := lineInbox.Put(ev.rt.ctx, textLine, line, col, ev.source); err != nil {
						return
					}
				}
			}(ch, lines)
		}
	}()

	return float64(actualPort), nil
}

func readNetLineFromConn(ch *netConnHandle, rt *runtimeLifecycle) (string, bool) {
	ch.readMu.Lock()
	defer ch.readMu.Unlock()

	var lineBuf []byte
	for {
		frag, err := ch.reader.ReadSlice('\n')
		if len(lineBuf)+len(frag) > maxNetLineBytes {
			return "", false
		}
		lineBuf = append(lineBuf, frag...)
		if err == nil {
			break
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if errors.Is(err, io.EOF) || ch.isClosed() || rt.isAborted() || errors.Is(err, net.ErrClosed) {
			if len(lineBuf) == 0 {
				return "", false
			}
			break
		}
		return "", false
	}

	if len(lineBuf) > 0 && lineBuf[len(lineBuf)-1] == '\n' {
		lineBuf = lineBuf[:len(lineBuf)-1]
	}
	if len(lineBuf) > 0 && lineBuf[len(lineBuf)-1] == '\r' {
		lineBuf = lineBuf[:len(lineBuf)-1]
	}
	if !utf8.Valid(lineBuf) {
		return "", false
	}
	return norm.NFC.String(string(lineBuf)), true
}

func (ev *Evaluator) execNetSend(args []Value, line, col int) (Value, error) {
	clientRec, ok := args[0].(*RecordValue)
	if !ok {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("net_send requires a tcp_client record, got %s", TypeName(args[0])), "")
	}
	msg, ok := args[1].(string)
	if !ok {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("net_send requires a text message, got %s", TypeName(args[1])), "")
	}
	id, _ := clientRec.Fields["id"].(string)
	if id == "" {
		return nil, newError("RuntimeError", line, col, ev.source,
			"cannot send to closed tcp_client", "")
	}
	ch, ok := ev.rt.getConn(id)
	if !ok || ch.isClosed() || ev.rt.isAborted() {
		return nil, newError("RuntimeError", line, col, ev.source,
			"cannot send to closed tcp_client", "")
	}

	ch.writeMu.Lock()
	_, err := writeAllBytes(ch.conn, []byte(msg))
	ch.writeMu.Unlock()
	if err != nil {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("network:tcp_send failed: %v", err), "")
	}
	return "", nil
}

func writeAllBytes(conn net.Conn, payload []byte) (int, error) {
	written := 0
	for written < len(payload) {
		n, err := conn.Write(payload[written:])
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}
