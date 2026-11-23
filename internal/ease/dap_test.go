package ease

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testDAPClient struct {
	t      *testing.T
	w      io.Writer
	r      *bufio.Reader
	nextID int
}

func startTestDAPSession(t *testing.T) (*testDAPClient, func()) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := NewDAPServer(inR, outW)
	done := make(chan error, 1)
	go func() {
		done <- srv.Run()
		_ = outW.Close()
	}()
	c := &testDAPClient{
		t:      t,
		w:      inW,
		r:      bufio.NewReader(outR),
		nextID: 1,
	}
	cleanup := func() {
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("DAP server did not exit cleanly")
		}
	}
	return c, cleanup
}

func (c *testDAPClient) sendReq(cmd string, args any) int {
	c.t.Helper()
	seq := c.nextID
	c.nextID++
	msg := map[string]any{
		"seq":     seq,
		"type":    "request",
		"command": cmd,
	}
	if args != nil {
		msg["arguments"] = args
	}
	body, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatal(err)
	}
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	if _, err := io.WriteString(c.w, frame); err != nil {
		c.t.Fatal(err)
	}
	return seq
}

func (c *testDAPClient) readRaw() map[string]any {
	c.t.Helper()
	payload, err := readLSPFrame(c.r)
	if err != nil {
		c.t.Fatalf("readLSPFrame failed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		c.t.Fatalf("unmarshal DAP message failed: %v", err)
	}
	return m
}

func (c *testDAPClient) waitResponse(cmd string) (map[string]any, []map[string]any) {
	c.t.Helper()
	var events []map[string]any
	for {
		m := c.readRaw()
		if m["type"] == "event" {
			events = append(events, m)
			continue
		}
		if m["type"] == "response" && m["command"] == cmd {
			if ok, _ := m["success"].(bool); !ok {
				c.t.Fatalf("DAP %s failed: %+v", cmd, m)
			}
			return m, events
		}
	}
}

func (c *testDAPClient) waitEvent(eventName string) (map[string]any, []map[string]any) {
	c.t.Helper()
	var other []map[string]any
	for {
		m := c.readRaw()
		if m["type"] == "event" && m["event"] == eventName {
			return m, other
		}
		other = append(other, m)
	}
}

func TestDAPBreakpointContinueAndArgs(t *testing.T) {
	dir := t.TempDir()
	progPath := filepath.Join(dir, "app.ease")
	src := `import system
count_args = count system.arguments
first = system.arguments.1
say "args={count_args}:{first}"
`
	if err := os.WriteFile(progPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	c, cleanup := startTestDAPSession(t)
	defer cleanup()

	c.sendReq("initialize", map[string]any{"adapterID": "ease"})
	c.waitResponse("initialize")

	c.sendReq("launch", map[string]any{
		"program": progPath,
		"args":    []string{"--port", "8080"},
	})
	c.waitResponse("launch")
	c.waitEvent("initialized")

	// Breakpoint on line 4 (`say "args=..."`)
	c.sendReq("setBreakpoints", map[string]any{
		"source":      map[string]any{"path": progPath},
		"breakpoints": []map[string]any{{"line": 4}},
	})
	c.waitResponse("setBreakpoints")

	c.sendReq("configurationDone", nil)
	c.waitResponse("configurationDone")

	stopped, _ := c.waitEvent("stopped")
	body := stopped["body"].(map[string]any)
	if body["reason"] != "breakpoint" {
		t.Fatalf("expected stopped reason 'breakpoint', got %+v", body)
	}

	c.sendReq("continue", map[string]any{"threadId": 1})
	c.waitResponse("continue")

	_, preTerm := c.waitEvent("terminated")
	var stdout strings.Builder
	for _, ev := range preTerm {
		if ev["event"] == "output" {
			b := ev["body"].(map[string]any)
			if b["category"] == "stdout" {
				stdout.WriteString(b["output"].(string))
			}
		}
	}
	if got := strings.TrimSpace(stdout.String()); got != "args=2:--port" {
		t.Fatalf("expected stdout 'args=2:--port', got %q", got)
	}
}

func TestDAPSteppingOverIntoAndOut(t *testing.T) {
	dir := t.TempDir()
	progPath := filepath.Join(dir, "step.ease")
	src := `to helper x
    y = x + 1
    -> y * 2
end

a = 5
b = helper a
say b
`
	if err := os.WriteFile(progPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	c, cleanup := startTestDAPSession(t)
	defer cleanup()

	c.sendReq("initialize", map[string]any{"adapterID": "ease"})
	c.waitResponse("initialize")
	c.sendReq("launch", map[string]any{"program": progPath, "stopOnEntry": true})
	c.waitResponse("launch")
	c.waitEvent("initialized")
	c.sendReq("configurationDone", nil)
	c.waitResponse("configurationDone")

	// Stop on entry at line 6 (`a = 5`)
	ev1, _ := c.waitEvent("stopped")
	if ev1["body"].(map[string]any)["reason"] != "entry" {
		t.Fatalf("expected entry stop, got %+v", ev1)
	}

	// Step Over to line 7 (`b = helper a`)
	c.sendReq("next", map[string]any{"threadId": 1})
	c.waitResponse("next")
	c.waitEvent("stopped")

	// Step Into `helper` -> lands at line 2 (`y = x + 1`)
	c.sendReq("stepIn", map[string]any{"threadId": 1})
	c.waitResponse("stepIn")
	c.waitEvent("stopped")

	c.sendReq("stackTrace", map[string]any{"threadId": 1})
	stResp, _ := c.waitResponse("stackTrace")
	frames := stResp["body"].(map[string]any)["stackFrames"].([]any)
	if len(frames) != 2 {
		t.Fatalf("expected 2 stack frames inside helper, got %+v", frames)
	}
	top := frames[0].(map[string]any)
	if top["name"] != "helper" || int(top["line"].(float64)) != 2 {
		t.Fatalf("expected top frame helper:2, got %+v", top)
	}

	// Step Out -> back to line 8 (`say b`)
	c.sendReq("stepOut", map[string]any{"threadId": 1})
	c.waitResponse("stepOut")
	c.waitEvent("stopped")

	c.sendReq("stackTrace", map[string]any{"threadId": 1})
	stResp2, _ := c.waitResponse("stackTrace")
	frames2 := stResp2["body"].(map[string]any)["stackFrames"].([]any)
	if len(frames2) != 1 || int(frames2[0].(map[string]any)["line"].(float64)) != 8 {
		t.Fatalf("expected top frame <main>:8 after stepOut, got %+v", frames2)
	}

	c.sendReq("continue", map[string]any{"threadId": 1})
	c.waitResponse("continue")
	c.waitEvent("terminated")
}

func TestDAPVariablesTreeAndEvaluate(t *testing.T) {
	dir := t.TempDir()
	progPath := filepath.Join(dir, "vars.ease")
	src := `def User
    name = "Ada"
end

to inspect u nums
    total = sum nums
    -> "{u.name}:{total}"
end

u = User with name = "Grace"
xs = list 10, 20
say inspect u xs
`
	if err := os.WriteFile(progPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	c, cleanup := startTestDAPSession(t)
	defer cleanup()

	c.sendReq("initialize", map[string]any{"adapterID": "ease"})
	c.waitResponse("initialize")
	c.sendReq("launch", map[string]any{"program": progPath})
	c.waitResponse("launch")
	c.waitEvent("initialized")

	// Breakpoint on line 7 (`-> "{u.name}:{total}"`)
	c.sendReq("setBreakpoints", map[string]any{
		"source":      map[string]any{"path": progPath},
		"breakpoints": []map[string]any{{"line": 7}},
	})
	c.waitResponse("setBreakpoints")
	c.sendReq("configurationDone", nil)
	c.waitResponse("configurationDone")
	c.waitEvent("stopped")

	c.sendReq("stackTrace", map[string]any{"threadId": 1})
	stResp, _ := c.waitResponse("stackTrace")
	frameID := int(stResp["body"].(map[string]any)["stackFrames"].([]any)[0].(map[string]any)["id"].(float64))

	c.sendReq("scopes", map[string]any{"frameId": frameID})
	scResp, _ := c.waitResponse("scopes")
	localsRef := int(scResp["body"].(map[string]any)["scopes"].([]any)[0].(map[string]any)["variablesReference"].(float64))

	c.sendReq("variables", map[string]any{"variablesReference": localsRef})
	vResp, _ := c.waitResponse("variables")
	vars := vResp["body"].(map[string]any)["variables"].([]any)
	var uRef int
	for _, rawV := range vars {
		v := rawV.(map[string]any)
		if v["name"] == "u" {
			uRef = int(v["variablesReference"].(float64))
		}
	}
	if uRef == 0 {
		t.Fatalf("expected expandable User record 'u' in locals, got %+v", vars)
	}

	// Expand `u` record children
	c.sendReq("variables", map[string]any{"variablesReference": uRef})
	uResp, _ := c.waitResponse("variables")
	uFields := uResp["body"].(map[string]any)["variables"].([]any)
	if len(uFields) != 1 || uFields[0].(map[string]any)["name"] != "name" || uFields[0].(map[string]any)["value"] != "Grace" {
		t.Fatalf("unexpected expanded User fields: %+v", uFields)
	}

	// Evaluate watch expression in sandboxed child scope (without mutating live total)
	c.sendReq("evaluate", map[string]any{"frameId": frameID, "expression": "total + 5"})
	evResp, _ := c.waitResponse("evaluate")
	if evResp["body"].(map[string]any)["result"] != "35" {
		t.Fatalf("expected evaluate 'total + 5' -> '35', got %+v", evResp)
	}

	c.sendReq("continue", map[string]any{"threadId": 1})
	c.waitResponse("continue")
	c.waitEvent("terminated")
}

func TestDAPCrossFileBreakpointAndDisconnect(t *testing.T) {
	dir := t.TempDir()
	modPath := filepath.Join(dir, "mod.ease")
	modSrc := `to compute n
    res = n * 10
    -> res
end
`
	if err := os.WriteFile(modPath, []byte(modSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "main.ease")
	mainSrc := `import "./mod.ease" as m
x = m:compute 4
loop
    check x > 1000000 -> end
    x = x + 1
end
`
	if err := os.WriteFile(mainPath, []byte(mainSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	c, cleanup := startTestDAPSession(t)
	defer cleanup()

	c.sendReq("initialize", map[string]any{"adapterID": "ease"})
	c.waitResponse("initialize")
	c.sendReq("launch", map[string]any{"program": mainPath})
	c.waitResponse("launch")
	c.waitEvent("initialized")

	// Breakpoint inside imported mod.ease at line 2 (`res = n * 10`)
	c.sendReq("setBreakpoints", map[string]any{
		"source":      map[string]any{"path": modPath},
		"breakpoints": []map[string]any{{"line": 2}},
	})
	c.waitResponse("setBreakpoints")
	c.sendReq("configurationDone", nil)
	c.waitResponse("configurationDone")

	stopped, _ := c.waitEvent("stopped")
	if stopped["body"].(map[string]any)["reason"] != "breakpoint" {
		t.Fatalf("expected breakpoint stop in mod.ease, got %+v", stopped)
	}

	// Disconnect while paused / before infinite loop cleanly aborts without deadlock.
	c.sendReq("disconnect", nil)
	c.waitResponse("disconnect")
}
