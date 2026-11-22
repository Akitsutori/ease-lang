package ease

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnoseSourceValidAndInvalid(t *testing.T) {
	valid := `
to greet name -> "Hello {name}"

test
    greet "World" is "Hello World"
end
`
	if diags := DiagnoseSource(valid); len(diags) != 0 {
		t.Fatalf("expected 0 diagnostics for valid Ease code, got %+v", diags)
	}

	nestedTest := `
to greet name
    "Hello {name}"
test
    greet "World" is "Hello World"
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

func TestLSPFileURIToPath(t *testing.T) {
	if got, want := fileURIToPath("file:///d:/work/src/main.ease"), filepath.FromSlash("d:/work/src/main.ease"); got != want {
		t.Fatalf("fileURIToPath Windows URI: got %q, want %q", got, want)
	}
	if got, want := fileURIToPath("file:///tmp/my%20dir/main.ease"), filepath.FromSlash("/tmp/my dir/main.ease"); got != want {
		t.Fatalf("fileURIToPath POSIX URI: got %q, want %q", got, want)
	}
	if got := fileURIToPath("untitled:Untitled-1"); got != "" {
		t.Fatalf("expected empty path for non-file URI, got %q", got)
	}
	if got := fileURIToPath(""); got != "" {
		t.Fatalf("expected empty path for empty URI, got %q", got)
	}
}

func TestLSPDiagnoseRelativeModuleImportInSubdir(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	billingPath := filepath.Join(srcDir, "billing.ease")
	billingSrc := "def invoice\n  net = 0\nend\nto total inv -> inv.net * 1.19\n"
	if err := os.WriteFile(billingPath, []byte(billingSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(srcDir, "main.ease")
	mainSrc := "import \"./billing.ease\" as billing\n\ndef order\n  id = 1\nend\n\ninv = billing.invoice with net = 100\nsay billing:total inv\n"
	if err := os.WriteFile(mainPath, []byte(mainSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	slash := filepath.ToSlash(mainPath)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	uri := "file://" + slash

	diags := DiagnoseSourceForURI(uri, mainSrc)
	if len(diags) != 0 {
		t.Fatalf("expected 0 diagnostics for valid relative module import, got: %+v", diags)
	}
	syms := DocumentSymbolsForURI(uri, mainSrc)
	if len(syms) != 1 || syms[0].Name != "order" {
		t.Fatalf("expected 1 symbol 'order', got: %+v", syms)
	}
	items := CompletionItemsForURI(uri, mainSrc)
	if len(items) == 0 {
		t.Fatalf("expected non-empty completion items")
	}
}

func TestLSPDiagnoseStdSynthModule(t *testing.T) {
	validSrc := `import synth

mel = synth.melody with
    notes = split "E4/8 G4/8 C5/4 REST/8 B4/8 C5/2" " "
    bpm   = 160
    wave  = "square"
end

synth:play mel
spawn synth:play mel
`
	if diags := DiagnoseSource(validSrc); len(diags) != 0 {
		t.Fatalf("expected 0 diagnostics for valid synth usage, got: %+v", diags)
	}

	badWaveSrc := `import synth
w = "sine" as synth.wave_kind or "sawtooth"
`
	if diags := DiagnoseSource(badWaveSrc); len(diags) == 0 || !strings.Contains(diags[0].Message, `"sawtooth" is not a valid value`) {
		t.Fatalf("expected diagnostic for invalid synth.wave_kind fallback, got: %+v", diags)
	}

	voidAssignSrc := `import synth
m = synth.melody with notes = list "C4/4"
x = synth:play m
`
	diags := DiagnoseSource(voidAssignSrc)
	if len(diags) == 0 || !strings.Contains(diags[0].Message, "does not return a value") {
		t.Fatalf("expected void assignment diagnostic for synth:play, got: %+v", diags)
	}
}
