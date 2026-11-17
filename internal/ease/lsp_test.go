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

func TestLSPHandleMessagePanicRecovery(t *testing.T) {
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
		"id":      2,
		"method":  "shutdown",
	})
	writeFrame(map[string]any{
		"jsonrpc": "2.0",
		"method":  "exit",
	})

	var outBuf bytes.Buffer
	srv := NewLSPServer(&inBuf, &outBuf)

	// Simulate an unexpected panic inside handleMessage via a nil message.
	if exit := srv.handleMessage(nil); exit {
		t.Fatalf("expected recovered panic in handleMessage not to trigger server exit")
	}

	// Subsequent LSP requests on the same server instance must still succeed.
	if err := srv.Run(); err != nil {
		t.Fatalf("LSPServer.Run() after recovered panic failed: %v", err)
	}

	r := bufio.NewReader(&outBuf)
	initFrame, err := readLSPFrame(r)
	if err != nil {
		t.Fatalf("failed reading initialize response after recovery: %v", err)
	}
	if !strings.Contains(string(initFrame), "ease-lsp") {
		t.Fatalf("expected ease-lsp in initialize response, got %s", initFrame)
	}
}

func TestLSPRejectsOversizedContentLength(t *testing.T) {
	in := strings.NewReader("Content-Length: 3000000000\r\n\r\n")
	var out bytes.Buffer
	srv := NewLSPServer(in, &out)
	err := srv.Run()
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected error mentioning Content-Length limit, got: %v", err)
	}
}

func TestLSPUnicodeUTF16DiagnosticsAndSymbols(t *testing.T) {
	// Surrogate-pair emoji "👍" = 2 UTF-16 code units, 4 UTF-8 bytes, 1 rune.
	// Umlaut "ä" / "ö" = 1 UTF-16 code unit, 2 UTF-8 bytes, 1 rune.
	diagSrc := "msg = \"👍\" as unknown_type\n"
	diags := DiagnoseSource(diagSrc)
	if len(diags) == 0 {
		t.Fatalf("expected diagnostic for unknown_type")
	}
	// Line: `msg = "👍" as unknown_type`
	// Runes before `unknown_type`: m(0) s(1) g(2) ' '(3) =(4) ' '(5) "(6) 👍(7) "(8) ' '(9) a(10) s(11) ' '(12) u(13)
	// UTF-16 before `unknown_type`: 13 + 1 extra unit for 👍 = 14; `unknown_type` is 12 ASCII chars -> End = 26.
	if diags[0].Range.Start.Character != 14 || diags[0].Range.End.Character != 26 {
		t.Fatalf("expected UTF-16 diagnostic span [14, 26), got [%d, %d)",
			diags[0].Range.Start.Character, diags[0].Range.End.Character)
	}

	symSrc := "def größe\n    länge = 1\nend\nto prüfe ö\n    ö\nend\n"
	syms := DocumentSymbolsForSource(symSrc)
	if len(syms) != 2 {
		t.Fatalf("expected 2 symbols, got %d", len(syms))
	}
	// `def größe`: "def " (4) + "größe" (5 UTF-16 units) = 9
	if syms[0].Name != "größe" || syms[0].Range.Start.Character != 0 || syms[0].Range.End.Character != 9 {
		t.Fatalf("unexpected UTF-16 range for def größe: %+v", syms[0].Range)
	}
	// `    länge = 1`: 4 spaces + "länge" (5 UTF-16 units) -> [4, 9)
	if len(syms[0].Children) != 1 || syms[0].Children[0].Range.Start.Character != 4 || syms[0].Children[0].Range.End.Character != 9 {
		t.Fatalf("unexpected UTF-16 range for field länge: %+v", syms[0].Children)
	}
	// `to prüfe ö`: "to " (3) + "prüfe" (5 UTF-16 units) = 8
	if syms[1].Name != "prüfe" || syms[1].Range.Start.Character != 0 || syms[1].Range.End.Character != 8 {
		t.Fatalf("unexpected UTF-16 range for to prüfe: %+v", syms[1].Range)
	}
}
