package ease

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServeFailFastNoListenersOrWorkers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oneshot.ease")
	if err := os.WriteFile(path, []byte("x = 2 + 3\nsay x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	_, err := StartService(ServiceOptions{
		FilePath: path,
		Out:      &out,
		ErrOut:   &errOut,
	})
	if err == nil {
		t.Fatalf("expected fail-fast error for one-shot script without listeners or workers")
	}
	want := "Program exited immediately with no active listeners or background workers (use 'ease run' for one-shot scripts)"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error %q, got: %v", want, err)
	}
}

func TestServeWorkerCrashIsolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "isolated.ease")
	src := `import network

to crash_when_told req
    network:reply_text req 500 "crashing"
    bad = 1 / 0
end

to handle_req req
    match "{req.method} {req.path}"
        "GET /boom"
            crash_when_told req
        else
            network:reply_text req 200 "still alive"
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
say "PORT={port}"
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	var outMu sync.Mutex
	svc, err := StartService(ServiceOptions{
		FilePath: path,
		Out:      &syncWriter{mu: &outMu, w: &out},
		ErrOut:   &syncWriter{mu: &outMu, w: &errOut},
		JSONLogs: true,
	})
	if err != nil {
		t.Fatalf("StartService failed: %v", err)
	}
	defer svc.ShutdownService(2 * time.Second)

	listeners := svc.Listeners()
	if len(listeners) != 1 {
		t.Fatalf("expected 1 listener, got %d", len(listeners))
	}
	baseURL := "http://" + listeners[0].Addr

	resp1, err := http.Get(baseURL + "/boom")
	if err != nil {
		t.Fatalf("GET /boom failed: %v", err)
	}
	_ = resp1.Body.Close()

	// Wait briefly for crash log to flush.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		outMu.Lock()
		logged := errOut.String()
		outMu.Unlock()
		if strings.Contains(logged, "worker_error") && strings.Contains(logged, "Division by zero") {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	outMu.Lock()
	logged := errOut.String()
	outMu.Unlock()
	if !strings.Contains(logged, `"event":"worker_error"`) || !strings.Contains(logged, "Division by zero") {
		t.Fatalf("expected isolated JSON worker_error log, got %q", logged)
	}

	// Subsequent request must still succeed with 200 OK.
	resp2, err := http.Get(baseURL + "/ok")
	if err != nil {
		t.Fatalf("GET /ok after worker crash failed: %v", err)
	}
	body, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != 200 || string(body) != "still alive" {
		t.Fatalf("expected 200 'still alive', got %d %q", resp2.StatusCode, string(body))
	}
}

func TestServeHotReloadAndDebugPortLiveSwap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live.ease")
	v1Src := `import network

def world_event
    kind = "hit" | "status"
    req  = network.http_request
end

to calc_label count_val -> "v1:{count_val}"

to run_world world
    hits = 0
    take world
        hits = hits + 1
        label = calc_label hits
        network:reply_text it.req 200 label
    end
end

to serve_loop requests world
    take requests
        put world (world_event with kind = "hit", req = it)
    end
end

world = inbox as world_event
requests = inbox as network.http_request
port = network:serve_http 0 requests
spawn run_world world
spawn serve_loop requests world
`
	if err := os.WriteFile(path, []byte(v1Src), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	var logMu sync.Mutex
	svc, err := StartService(ServiceOptions{
		FilePath:     path,
		DebugEnabled: true,
		DebugPort:    0,
		Out:          &syncWriter{mu: &logMu, w: &out},
		ErrOut:       &syncWriter{mu: &logMu, w: &errOut},
	})
	if err != nil {
		t.Fatalf("StartService failed: %v", err)
	}
	defer svc.ShutdownService(2 * time.Second)

	dbgAddr := svc.DebugAddr()
	if !strings.HasPrefix(dbgAddr, "127.0.0.1:") {
		t.Fatalf("expected loopback debug address 127.0.0.1:..., got %q", dbgAddr)
	}

	listeners := svc.Listeners()
	if len(listeners) != 1 {
		t.Fatalf("expected 1 listener, got %d", len(listeners))
	}
	httpURL := "http://" + listeners[0].Addr

	// 1. Initial v1 request increments `hits` inside run_world from 0 -> 1.
	if got := mustHTTPGetText(t, httpURL); got != "v1:1" {
		t.Fatalf("expected v1:1, got %q", got)
	}

	// 2. Connect to debug socket, verify :status, :funcs, and live expression evaluation.
	conn, err := net.DialTimeout("tcp", dbgAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial debug port failed: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	banner, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(banner, "ease debug ready") {
		t.Fatalf("unexpected debug banner: %q (%v)", banner, err)
	}

	_, _ = fmt.Fprintln(conn, ":status")
	statusOut := readDebugUntilContains(t, reader, "inbox world: inbox<world_event>")
	if !strings.Contains(statusOut, "listeners: 1") {
		t.Fatalf("expected active listener in :status, got %q", statusOut)
	}

	_, _ = fmt.Fprintln(conn, ":funcs")
	funcsOut := readDebugUntilContains(t, reader, "to serve_loop requests world")
	if !strings.Contains(funcsOut, "to calc_label count_val") || !strings.Contains(funcsOut, "to run_world world") {
		t.Fatalf("expected functions list, got %q", funcsOut)
	}

	// 3. Broken edit fails :reload without touching live v1 state.
	brokenSrc := strings.ReplaceAll(v1Src, "to calc_label count_val", "to calc_label count_val ->")
	if err := os.WriteFile(path, []byte(brokenSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(conn, ":reload")
	reloadErrLine, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(reloadErrLine, "error:") {
		t.Fatalf("expected :reload error on broken source, got %q (%v)", reloadErrLine, err)
	}
	// Drain rest of multi-line EaseError on debug socket by issuing :status cleanly next?
	// Let's write valid v2 now and synchronize via a new connection or drain error tail.
	conn2, err := net.DialTimeout("tcp", dbgAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial debug port #2 failed: %v", err)
	}
	defer conn2.Close()
	r2 := bufio.NewReader(conn2)
	_, _ = r2.ReadString('\n')

	// Still runs v1 after failed reload, state preserved (hits 1 -> 2)!
	if got := mustHTTPGetText(t, httpURL); got != "v1:2" {
		t.Fatalf("expected v1:2 after failed reload, got %q", got)
	}

	// 4. Write valid v2 source on disk and trigger :reload -> state (`hits`) preserved (2 -> 3) with NEW code!
	v2Src := strings.ReplaceAll(v1Src, `"v1:{count_val}"`, `"v2:{count_val * 10}"`)
	if err := os.WriteFile(path, []byte(v2Src), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(conn2, ":reload")
	reloadOK, err := r2.ReadString('\n')
	if err != nil || !strings.Contains(reloadOK, "ok: reloaded") {
		t.Fatalf("expected 'ok: reloaded', got %q (%v)", reloadOK, err)
	}

	if got := mustHTTPGetText(t, httpURL); got != "v2:30" {
		t.Fatalf("expected live hot-swapped response v2:30 (preserving hits=3), got %q", got)
	}

	// 5. Live single-line `to` redefinition + live evaluation over debug socket!
	_, _ = fmt.Fprintln(conn2, `to calc_label count_val -> "live:{count_val + 100}"`)
	liveSwapLine, err := r2.ReadString('\n')
	if err != nil || !strings.Contains(liveSwapLine, "ok: updated to calc_label") {
		t.Fatalf("expected live swap ack, got %q (%v)", liveSwapLine, err)
	}
	if got := mustHTTPGetText(t, httpURL); got != "live:104" {
		t.Fatalf("expected debug-swapped response live:104, got %q", got)
	}

	_, _ = fmt.Fprintln(conn2, `calc_label 5`)
	evalLine, err := r2.ReadString('\n')
	if err != nil || strings.TrimSpace(evalLine) != "live:105" {
		t.Fatalf("expected eval result live:105, got %q (%v)", evalLine, err)
	}
}

func TestServePortOverrideWatchAndGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watch.ease")
	v1 := `import network

to version_tag -> "alpha"

to serve_loop requests
    take requests
        network:reply_json it 200 (version_tag)
    end
end

requests = inbox as network.http_request
port = network:serve_http 18080 requests
spawn serve_loop requests
`
	if err := os.WriteFile(path, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pick a free port, close it, and pass as PortOverride to test --port override on fixed port 18080.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	overridePort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	var out, errOut bytes.Buffer
	var logMu sync.Mutex
	svc, err := StartService(ServiceOptions{
		FilePath:      path,
		PortOverride:  overridePort,
		Watch:         true,
		WatchInterval: 40 * time.Millisecond,
		Out:           &syncWriter{mu: &logMu, w: &out},
		ErrOut:        &syncWriter{mu: &logMu, w: &errOut},
	})
	if err != nil {
		t.Fatalf("StartService failed: %v", err)
	}

	listeners := svc.Listeners()
	if len(listeners) != 1 || listeners[0].Addr != fmt.Sprintf("127.0.0.1:%d", overridePort) {
		_ = svc.ShutdownService(2 * time.Second)
		t.Fatalf("expected port override %d, got listeners %+v", overridePort, listeners)
	}

	if svc.DebugAddr() != "" {
		_ = svc.ShutdownService(2 * time.Second)
		t.Fatalf("expected debug server disabled by default, got %q", svc.DebugAddr())
	}

	url := "http://" + listeners[0].Addr
	if got := mustHTTPGetText(t, url); got != `"alpha"` {
		_ = svc.ShutdownService(2 * time.Second)
		t.Fatalf("expected '\"alpha\"', got %q", got)
	}

	// Touch file with new content and future modtime so --watch picks it up automatically.
	v2 := strings.ReplaceAll(v1, `"alpha"`, `"beta"`)
	if err := os.WriteFile(path, []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)

	deadline := time.Now().Add(2 * time.Second)
	swapped := false
	for time.Now().Before(deadline) {
		var raw string
		resp, err := http.Get(url)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			raw = string(b)
		}
		if raw == `"beta"` {
			swapped = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !swapped {
		_ = svc.ShutdownService(2 * time.Second)
		t.Fatalf("expected --watch to auto-reload 'beta'")
	}

	done := make(chan error, 1)
	go func() {
		done <- svc.ShutdownService(2 * time.Second)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ShutdownService failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("ShutdownService timed out")
	}

	if _, err := http.Get(url); err == nil {
		t.Fatalf("expected listener closed after ShutdownService")
	}
}

type syncWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (sw *syncWriter) Write(p []byte) (int, error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Write(p)
}

func mustHTTPGetText(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s failed: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	return string(b)
}

func readDebugUntilContains(t *testing.T, r *bufio.Reader, needle string) string {
	t.Helper()
	var b strings.Builder
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		b.WriteString(line)
		if strings.Contains(line, needle) {
			return b.String()
		}
		if err != nil {
			t.Fatalf("debug read error (%v) after %q", err, b.String())
		}
	}
	t.Fatalf("timed out waiting for %q, got %q", needle, b.String())
	return ""
}

var _ = json.Marshal
