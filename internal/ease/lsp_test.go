package ease

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDiagnoseSourceValidAndInvalid(t *testing.T) {
	valid := `
to greet name
    "Hello {name}"
end

test
    do greet "World" is "Hello World"
end
`
	if diags := DiagnoseSource(valid); len(diags) != 0 {
		t.Fatalf("expected 0 diagnostics for valid Ease code, got %+v", diags)
	}

	nestedTest := `
to greet name
    "Hello {name}"
test
    do greet "World" is "Hello World"
end
`
	if diags := DiagnoseSource(nestedTest); len(diags) == 0 {
		t.Fatalf("expected diagnostic when 'to' is not closed with 'end' before 'test'")
	}

	invalid := `
to broken x
    say x
`
	diags := DiagnoseSource(invalid)
	if len(diags) == 0 {
		t.Fatalf("expected diagnostics for unclosed function, got 0")
	}
	if diags[0].Severity != 1 {
		t.Fatalf("expected error severity 1, got %d", diags[0].Severity)
	}
	if diags[0].Message == "" {
		t.Fatalf("expected non-empty diagnostic message")
	}
}

func TestDocumentSymbolsForSource(t *testing.T) {
	src := `
def Player
    name = "Hero"
    hp = 100
end

to heal amount
    amount + 10
end
`
	syms := DocumentSymbolsForSource(src)
	if len(syms) != 2 {
		t.Fatalf("expected 2 top-level symbols, got %d", len(syms))
	}
	if syms[0].Name != "Player" || len(syms[0].Children) != 2 {
		t.Fatalf("expected Player def with 2 fields, got %+v", syms[0])
	}
	if syms[1].Name != "heal" {
		t.Fatalf("expected heal function symbol, got %+v", syms[1])
	}
}

func TestLSPServerRoundTrip(t *testing.T) {
	var inBuf bytes.Buffer
	writeFrame := func(payload any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(&inBuf, "Content-Length: %d\r\n\r\n%s", len(b), b)
	}

	writeFrame(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{},
	})
	writeFrame(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":  "file:///test.ease",
				"text": "to broken\n  say 1\n",
			},
		},
	})
	writeFrame(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "shutdown",
	})
	writeFrame(map[string]any{
		"jsonrpc": "2.0",
		"method":  "exit",
	})

	var outBuf bytes.Buffer
	srv := NewLSPServer(&inBuf, &outBuf)
	if err := srv.Run(); err != nil {
		t.Fatalf("LSPServer.Run() error: %v", err)
	}

	r := bufio.NewReader(&outBuf)
	// 1. initialize response
	initFrame, err := readLSPFrame(r)
	if err != nil {
		t.Fatalf("failed reading initialize response: %v", err)
	}
	if !strings.Contains(string(initFrame), "ease-lsp") {
		t.Fatalf("expected ease-lsp in initialize response, got %s", initFrame)
	}

	// 2. publishDiagnostics notification
	diagFrame, err := readLSPFrame(r)
	if err != nil {
		t.Fatalf("failed reading diagnostics notification: %v", err)
	}
	if !strings.Contains(string(diagFrame), "textDocument/publishDiagnostics") {
		t.Fatalf("expected publishDiagnostics, got %s", diagFrame)
	}
	if !strings.Contains(string(diagFrame), `"severity":1`) {
		t.Fatalf("expected error diagnostic, got %s", diagFrame)
	}

	// 3. shutdown response
	shutdownFrame, err := readLSPFrame(r)
	if err != nil {
		t.Fatalf("failed reading shutdown response: %v", err)
	}
	if !strings.Contains(string(shutdownFrame), `"id":2`) {
		t.Fatalf("expected shutdown response id 2, got %s", shutdownFrame)
	}
}
