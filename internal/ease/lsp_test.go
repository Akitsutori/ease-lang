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
	if len(syms) != 2 || syms[0].Name != "billing" || syms[0].Kind != 2 || syms[1].Name != "order" || syms[1].Kind != 23 {
		t.Fatalf("expected [billing (Module), order (Struct)], got: %+v", syms)
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

func TestDocumentSymbolsImportsConstsAndFilter(t *testing.T) {
	dir := t.TempDir()
	helperPath := filepath.Join(dir, "helper.ease")
	if err := os.WriteFile(helperPath, []byte("def Remote\n    id = 0\nend\nto ping -> \"pong\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "main.ease")
	mainSrc := `import "./helper.ease" as h
import system

def max_items = 10

def Local
    title = ""
end

def Status = "ok" | "err"

to run -> "ok"
`
	mainURI := pathToFileURI(mainPath)
	syms := DocumentSymbolsForURI(mainURI, mainSrc)
	kinds := make(map[string]int)
	for _, sym := range syms {
		kinds[sym.Name] = sym.Kind
	}
	if kinds["h"] != 2 || kinds["system"] != 2 {
		t.Fatalf("expected Module (Kind 2) symbols for imports, got: %+v", kinds)
	}
	if kinds["max_items"] != 14 {
		t.Fatalf("expected Constant (Kind 14) for max_items, got: %+v", kinds)
	}
	if kinds["Local"] != 23 || kinds["Status"] != 10 || kinds["run"] != 12 {
		t.Fatalf("expected Local(23), Status(10), run(12), got: %+v", kinds)
	}
	if _, leaked := kinds["Remote"]; leaked {
		t.Fatalf("did not expect imported Remote def in DocumentSymbolsForURI, got: %+v", kinds)
	}
}

func TestLSPDefinitionLocalCrossFileAndFields(t *testing.T) {
	dir := t.TempDir()
	helperPath := filepath.Join(dir, "helper.ease")
	helperSrc := `def User
    name = "Ada"
    role = "admin"
end

to greet u -> "Hi {u.name}"
`
	if err := os.WriteFile(helperPath, []byte(helperSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "main.ease")
	mainSrc := `import "./helper.ease" as h

def Local
    label = "äöü"
end

x = 10
to shadow x
    -> x + 1
end

u = h.User with name = "Grace"
say u.name
say h:greet u
say shadow 5
loc = Local with label = "ok"
say loc.label
`
	s := NewLSPServer(nil, nil)
	mainURI := pathToFileURI(mainPath)
	helperURI := pathToFileURI(helperPath)
	s.setDoc(mainURI, mainSrc)
	s.setDoc(helperURI, helperSrc)

	// 1. Click on `h:greet` -> jumps to `to greet` in helper.ease (line 5, 0-based).
	locs := s.definitionForURI(mainURI, LSPPosition{Line: 13, Character: 7})
	if len(locs) != 1 || !pathsEqual(fileURIToPath(locs[0].URI), helperPath) || locs[0].Range.Start.Line != 5 {
		t.Fatalf("expected cross-file def for h:greet at helper.ease:5, got: %+v", locs)
	}

	// 2. Click on `u.name` -> jumps to `name = "Ada"` in helper.ease (line 1, 0-based).
	fieldLocs := s.definitionForURI(mainURI, LSPPosition{Line: 12, Character: 7})
	if len(fieldLocs) != 1 || !pathsEqual(fileURIToPath(fieldLocs[0].URI), helperPath) || fieldLocs[0].Range.Start.Line != 1 {
		t.Fatalf("expected field def for u.name at helper.ease:1, got: %+v", fieldLocs)
	}

	// 3. Click on `name` inside `h.User with name = "Grace"` -> jumps to `name` in helper.ease (line 1).
	withLocs := s.definitionForURI(mainURI, LSPPosition{Line: 11, Character: 17})
	if len(withLocs) != 1 || !pathsEqual(fileURIToPath(withLocs[0].URI), helperPath) || withLocs[0].Range.Start.Line != 1 {
		t.Fatalf("expected field def for with name at helper.ease:1, got: %+v", withLocs)
	}

	// 4. Click on `"./helper.ease"` import string -> jumps to helper.ease line 0.
	impLocs := s.definitionForURI(mainURI, LSPPosition{Line: 0, Character: 10})
	if len(impLocs) != 1 || !pathsEqual(fileURIToPath(impLocs[0].URI), helperPath) || impLocs[0].Range.Start.Line != 0 {
		t.Fatalf("expected import path def at helper.ease:0, got: %+v", impLocs)
	}

	// 5. Parameter `x` inside `to shadow x` jumps to `to shadow x` (line 7), not global `x = 10` (line 6).
	paramLocs := s.definitionForURI(mainURI, LSPPosition{Line: 8, Character: 7})
	if len(paramLocs) != 1 || paramLocs[0].Range.Start.Line != 7 {
		t.Fatalf("expected param x def at line 7, got: %+v", paramLocs)
	}

	// 6. Click on `loc.label` -> jumps to `label` in Local (line 3).
	localFieldLocs := s.definitionForURI(mainURI, LSPPosition{Line: 16, Character: 8})
	if len(localFieldLocs) != 1 || localFieldLocs[0].Range.Start.Line != 3 {
		t.Fatalf("expected local field def at line 3, got: %+v", localFieldLocs)
	}
}

func TestLSPHoverAndReferences(t *testing.T) {
	dir := t.TempDir()
	modPath := filepath.Join(dir, "math_mod.ease")
	modSrc := `def Point
    x = 0
end

to double n -> n * 2

to log_msg msg
    say msg
end
`
	if err := os.WriteFile(modPath, []byte(modSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(dir, "app.ease")
	appSrc := `import "./math_mod.ease" as m
import system

p = m.Point with x = 3
v = m:double p.x
say count system.arguments
`
	s := NewLSPServer(nil, nil)
	modURI := pathToFileURI(modPath)
	appURI := pathToFileURI(appPath)
	s.setDoc(modURI, modSrc)
	s.setDoc(appURI, appSrc)

	// Hover on `m:double` -> shows `to m:double n` and `returns value`.
	hDouble := s.hoverForURI(appURI, LSPPosition{Line: 4, Character: 7})
	if hDouble == nil || !strings.Contains(hDouble.Contents.Value, "to m:double n") || !strings.Contains(hDouble.Contents.Value, "returns value") {
		t.Fatalf("unexpected hover for m:double: %+v", hDouble)
	}

	// Hover on `log_msg` -> shows `void procedure`.
	hVoid := s.hoverForURI(modURI, LSPPosition{Line: 6, Character: 4})
	if hVoid == nil || !strings.Contains(hVoid.Contents.Value, "void procedure") {
		t.Fatalf("unexpected hover for void function: %+v", hVoid)
	}

	// Hover on built-in `count` and module `system`.
	hCount := s.hoverForURI(appURI, LSPPosition{Line: 5, Character: 5})
	if hCount == nil || !strings.Contains(hCount.Contents.Value, "count <list_or_text>") {
		t.Fatalf("unexpected hover for builtin count: %+v", hCount)
	}
	hSys := s.hoverForURI(appURI, LSPPosition{Line: 5, Character: 11})
	if hSys == nil || !strings.Contains(hSys.Contents.Value, "system.arguments") {
		t.Fatalf("unexpected hover for stdlib system: %+v", hSys)
	}

	// Cross-file references for `double`: declaration in math_mod.ease + call in app.ease.
	refsWithDecl := s.referencesForURI(appURI, LSPPosition{Line: 4, Character: 7}, true)
	if len(refsWithDecl) != 2 {
		t.Fatalf("expected 2 references for double (decl + call), got: %+v", refsWithDecl)
	}
	refsNoDecl := s.referencesForURI(appURI, LSPPosition{Line: 4, Character: 7}, false)
	if len(refsNoDecl) != 1 || !pathsEqual(fileURIToPath(refsNoDecl[0].URI), appPath) {
		t.Fatalf("expected 1 call-site reference for double in app.ease, got: %+v", refsNoDecl)
	}
}

func TestLSPLastGoodASTFallbackOnSyntaxError(t *testing.T) {
	s := NewLSPServer(nil, nil)
	uri := "file:///tmp/typing_test.ease"
	goodSrc := `def User
    name = "Ada"
end

to greet u -> "Hi {u.name}"

u = User with name = "Grace"
say greet u
`
	s.setDoc(uri, goodSrc)

	// Simulate user mid-line with a syntax error:
	brokenSrc := goodSrc + "\nsay (\n"
	s.setDoc(uri, brokenSrc)

	syms := s.documentSymbolsForURI(uri)
	if len(syms) == 0 {
		t.Fatalf("expected lastGoodAST fallback symbols during syntax error, got 0")
	}
	locs := s.definitionForURI(uri, LSPPosition{Line: 7, Character: 5})
	if len(locs) != 1 || locs[0].Range.Start.Line != 4 {
		t.Fatalf("expected lastGoodAST fallback definition for 'greet' at line 4, got: %+v", locs)
	}
	hov := s.hoverForURI(uri, LSPPosition{Line: 7, Character: 5})
	if hov == nil || !strings.Contains(hov.Contents.Value, "to greet u") {
		t.Fatalf("expected lastGoodAST fallback hover for 'greet', got: %+v", hov)
	}
	refs := s.referencesForURI(uri, LSPPosition{Line: 7, Character: 5}, true)
	if len(refs) != 2 {
		t.Fatalf("expected lastGoodAST fallback references for 'greet', got: %+v", refs)
	}
}
