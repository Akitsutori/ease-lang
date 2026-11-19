package ease

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInboxAddOnCloseRunsOnceAndOnAlreadyClosed(t *testing.T) {
	ib := newInboxValue("number")
	var count atomic.Int32
	ib.AddOnClose(func() { count.Add(1) })
	ib.AddOnClose(func() { count.Add(1) })
	ib.Close()
	ib.Close()
	if got := count.Load(); got != 2 {
		t.Fatalf("expected 2 onClose invocations, got %d", got)
	}
	ib.AddOnClose(func() { count.Add(1) })
	if got := count.Load(); got != 3 {
		t.Fatalf("expected immediate invocation on already-closed inbox, got %d", got)
	}
}

func TestNetServeHTTPConcurrentAndUTF8ContentLength(t *testing.T) {
	src := `import network

def api_info
    service = ""
    status  = "ok"
end

to handle_req req
    match req.path
        "/hello" -> network:reply_text req 200 "Grüß Gott!"
        "/json"  -> network:reply_json req 200 (api_info with service = "ease-net")
        else     -> network:reply_text req 404 "not found"
    end
end

to serve_loop requests
    take requests
        spawn handle_req it
    end
end

requests = inbox as network.http_request
port = network:serve_http 0 requests
spawn serve_loop requests

api = network.network with url = "http://127.0.0.1:{port}"
paths = list "/hello", "/json", "/hello", "/missing"
bodies = swarm loop paths
    network:fetch api it
end

say bodies
close requests
`
	var out bytes.Buffer
	if _, err := EvalProgram(src, &out); err != nil {
		t.Fatalf("EvalProgram failed: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Grüß Gott!") {
		t.Fatalf("expected UTF-8 greeting in output, got %q", got)
	}
	if !strings.Contains(got, "ease-net") {
		t.Fatalf("expected JSON response in output, got %q", got)
	}
}

func TestNetServeTCPInboxEcho(t *testing.T) {
	src := `import network

to handle_client client
    take client.lines
        network:tcp_send client "echo:{it}\n"
        check it is "bye" -> close client.lines
    end
end

to accept_clients clients
    take clients
        spawn handle_client it
    end
end

clients = inbox as network.tcp_client
port = network:serve_tcp 0 clients
spawn accept_clients clients
say "PORT={port}"
`
	prog, err := ParseProgram(src)
	if err != nil {
		t.Fatalf("ParseProgram failed: %v", err)
	}
	var out bytes.Buffer
	ev := NewEvaluatorWithInput(src, prog, strings.NewReader(""), &out)
	defer ev.Close()
	if ev.initErr != nil {
		t.Fatalf("initErr: %v", ev.initErr)
	}

	// Execute top-level statements (`clients`, `port`, `spawn accept_clients`).
	if _, err := ev.ExecStatements(prog.Body, ev.global); err != nil {
		t.Fatalf("ExecStatements failed: %v", err)
	}

	outStr := strings.TrimSpace(out.String())
	var port int
	if _, err := fmt.Sscanf(outStr, "PORT=%d", &port); err != nil || port <= 0 {
		t.Fatalf("expected valid bound port, got %q", outStr)
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatalf("net.DialTimeout failed: %v", err)
	}
	defer conn.Close()

	_, _ = fmt.Fprint(conn, "ping\nbye\n")
	br := bufio.NewReader(conn)
	l1, err := br.ReadString('\n')
	if err != nil || l1 != "echo:ping\n" {
		t.Fatalf("expected 'echo:ping\\n', got %q (err=%v)", l1, err)
	}
	l2, err := br.ReadString('\n')
	if err != nil || l2 != "echo:bye\n" {
		t.Fatalf("expected 'echo:bye\\n', got %q (err=%v)", l2, err)
	}
}

func TestNetHeadlessCheckReturnsZeroPort(t *testing.T) {
	src := `import network

requests = inbox as network.http_request
port = network:serve_http 8080 requests
say port
close requests
`
	var out bytes.Buffer
	if _, err := EvalProgramHeadlessWithTrace(src, strings.NewReader(""), &out, nil); err != nil {
		t.Fatalf("EvalProgramHeadlessWithTrace failed: %v", err)
	}
	if strings.TrimSpace(out.String()) != "0" {
		t.Fatalf("expected port 0 in headless --check mode, got %q", out.String())
	}
}

func TestNetCloseUnblocksBlockedServersNoLeak(t *testing.T) {
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	before := runtime.NumGoroutine()

	src := `import network

to serve_loop requests
    take requests
        network:reply_text it 200 "ok"
    end
end

requests = inbox as network.http_request
port = network:serve_http 0 requests
spawn serve_loop requests
`
	prog, err := ParseProgram(src)
	if err != nil {
		t.Fatalf("ParseProgram failed: %v", err)
	}
	var out bytes.Buffer
	ev := NewEvaluatorWithInput(src, prog, strings.NewReader(""), &out)
	if ev.initErr != nil {
		t.Fatalf("initErr: %v", ev.initErr)
	}
	if _, err := ev.ExecStatements(prog.Body, ev.global); err != nil {
		t.Fatalf("ExecStatements failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		ev.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("ev.Close() deadlocked instead of shutting down serve_http")
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Fatalf("goroutine leak suspected: before=%d, after=%d", before, after)
	}
}
