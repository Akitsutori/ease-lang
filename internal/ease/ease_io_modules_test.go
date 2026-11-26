package ease

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStandaloneBundleRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	hostPath := filepath.Join(tmpDir, "host.exe")
	outPath := filepath.Join(tmpDir, "app.exe")
	if err := os.WriteFile(hostPath, []byte("MZ_FAKE_EXE_HEADER"), 0o755); err != nil {
		t.Fatal(err)
	}

	programSrc := "to greet who -> \"Hi \" + who\ntest\n  greet \"Ease\" is \"Hi Ease\"\nend\nsay greet \"Go\"\n"
	if err := BuildStandaloneExe(hostPath, programSrc, outPath); err != nil {
		t.Fatalf("BuildStandaloneExe failed: %v", err)
	}

	extracted, ok, err := ReadEmbeddedSource(outPath)
	if err != nil || !ok {
		t.Fatalf("ReadEmbeddedSource failed: ok=%v err=%v", ok, err)
	}
	if extracted != programSrc {
		t.Fatalf("extracted source mismatch:\ngot:  %q\nwant: %q", extracted, programSrc)
	}

	// Re-bundling from an already bundled binary must strip the old trailer cleanly.
	outPath2 := filepath.Join(tmpDir, "app2.exe")
	if err := BuildStandaloneExe(outPath, "say 42\n", outPath2); err != nil {
		t.Fatalf("second BuildStandaloneExe failed: %v", err)
	}
	extracted2, ok2, err2 := ReadEmbeddedSource(outPath2)
	if err2 != nil || !ok2 || extracted2 != "say 42\n" {
		t.Fatalf("second bundle extraction failed: ok=%v err=%v src=%q", ok2, err2, extracted2)
	}
}

func TestAskInput(t *testing.T) {
	src := `
name = ask "Wie heißt du?"
city = ask "Wo wohnst du?"
say "Hallo {name} aus {city}!"
`
	var out bytes.Buffer
	in := strings.NewReader("Ada\nBerlin\n")
	if _, err := EvalProgramWithInput(src, in, &out); err != nil {
		t.Fatalf("EvalProgramWithInput failed: %v", err)
	}
	want := "Wie heißt du? Wo wohnst du? Hallo Ada aus Berlin!\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\ngot:  %q\nwant: %q", out.String(), want)
	}
}

func TestUTF8ValidationNFCAndTextBuiltins(t *testing.T) {
	// 1. Happy paths: split, join, trim, replace, NFC normalization (NFD vs NFC), contains in where/check/test.
	happySrc := "def user\n" +
		"    email = \"\"\n" +
		"end\n\n" +
		"to classify_msg s\n" +
		"    check contains s \"ok\" -> \"accepted\"\n" +
		"    -> \"rejected\"\n" +
		"end\n\n" +
		"test \"UTF-8, NFC, and text built-ins\"\n" +
		"    split \"äöü👍\" \"\" is list \"ä\", \"ö\", \"ü\", \"👍\"\n" +
		"    split \"a,b,c\" \",\" is list \"a\", \"b\", \"c\"\n" +
		"    join (list \"a\", \"b\") \"-\" is \"a-b\"\n" +
		"    trim \"  hi \\t\\n\" is \"hi\"\n" +
		"    replace \"foo bar foo\" \"foo\" \"baz\" is \"baz bar baz\"\n\n" +
		"    # NFD vs NFC canonical equivalence\n" +
		"    nfd = \"a\u0308\"\n" +
		"    nfc = \"ä\"\n" +
		"    nfd is nfc\n" +
		"    count nfd is 1\n" +
		"    nfd.1 is \"ä\"\n\n" +
		"    # contains as condition in check, where, and test (including is error)\n" +
		"    classify_msg \"status: ok\" is \"accepted\"\n" +
		"    classify_msg \"status: fail\" is \"rejected\"\n" +
		"    contains \"hello world\" \"world\"\n" +
		"    (contains 123 \"a\") is error\n" +
		"    contains 123 \"a\" is error\n\n" +
		"    users = list as user with\n" +
		"        email = \"ada@example.com\"\n" +
		"        email = \"bob@other.org\"\n" +
		"    end\n" +
		"    matched = users where contains it.email \"@example.com\"\n" +
		"    count matched is 1\n" +
		"    matched.1.email is \"ada@example.com\"\n\n" +
		"    replace \"abc\" \"\" \"x\" is error\n" +
		"    join (list 1, 2) \",\" is error\n" +
		"end\n"

	report, err := RunChecks(happySrc)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 17 {
		t.Fatalf("expected 17 passed / 0 failed checks, got %d passed / %d failed:\n%+v", report.Passed, report.Failed, report.Results)
	}

	// 2. Invalid UTF-8 in source code (lexer).
	badSrc := "x = \"\xff\xfe\"\n"
	if _, err := ParseProgram(badSrc); err == nil || !strings.Contains(err.Error(), "Invalid UTF-8 byte sequence (0xFF)") {
		t.Fatalf("expected Invalid UTF-8 syntax error from lexer, got: %v", err)
	}

	// 3. Invalid UTF-8 from stdin (`ask`).
	if _, err := EvalProgramWithInput("x = ask \"\"\n", bytes.NewReader([]byte{0xff, '\n'}), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Input from stdin contains invalid UTF-8 bytes") {
		t.Fatalf("expected RuntimeError on invalid UTF-8 from ask, got: %v", err)
	}

	// 4. Valid NFD over stdin (`ask`) is normalized to NFC.
	nfdVal, err := EvalProgramWithInput("res = ask \"\"\n", strings.NewReader("a\u0308\n"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("ask NFD failed: %v", err)
	}
	if nfdVal != "ä" {
		t.Fatalf("expected NFC %q from ask, got %q", "ä", nfdVal)
	}

	// 5. Invalid UTF-8 and NFC normalization in `http_call`.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte{0xff, 0xfe})
		case "/nfd":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("a\u0308"))
		}
	}))
	defer srv.Close()

	badHTTPSrc := fmt.Sprintf("import network\nn = network.network with url = %q\nres = network:fetch n \"/bad\"\n", srv.URL)
	if _, err := EvalProgram(badHTTPSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "HTTP response body contains invalid UTF-8 bytes") {
		t.Fatalf("expected RuntimeError on invalid UTF-8 HTTP body, got: %v", err)
	}

	nfdHTTPSrc := fmt.Sprintf("import network\nn = network.network with url = %q\nsay (network:fetch n \"/nfd\")\n", srv.URL)
	var httpOut bytes.Buffer
	if _, err := EvalProgram(nfdHTTPSrc, &httpOut); err != nil {
		t.Fatalf("expected valid NFD HTTP body to succeed, got: %v", err)
	}
	if httpOut.String() != "ä\n" {
		t.Fatalf("expected NFC HTTP body %q, got %q", "ä\n", httpOut.String())
	}

	// 6. Storing or printing `contains` is forbidden (condition guardrail).
	for _, badCondSrc := range []string{
		"ok = contains \"abc\" \"a\"\n",
		"say contains \"abc\" \"a\"\n",
	} {
		if _, err := ParseProgram(badCondSrc); err == nil || !strings.Contains(err.Error(), "Conditions cannot be stored or printed") {
			t.Fatalf("expected condition storage/print TypeError for %q, got: %v", badCondSrc, err)
		}
	}
}

func TestLoadSaveAndCSVMapping(t *testing.T) {
	tmpDir := t.TempDir()
	txtPath := filepath.Join(tmpDir, "notes.txt")
	binPath := filepath.Join(tmpDir, "image.bin")
	asiaCSVPath := filepath.Join(tmpDir, "export_asia.csv")
	cleanCSVPath := filepath.Join(tmpDir, "sales_clean.csv")
	badUTF8Path := filepath.Join(tmpDir, "bad.bin")
	badCSVPath := filepath.Join(tmpDir, "bad.csv")
	emptyCSVPath := filepath.Join(tmpDir, "empty.csv")
	dupCSVPath := filepath.Join(tmpDir, "dup.csv")
	nanCSVPath := filepath.Join(tmpDir, "nan.csv")

	// Write BOM + NFD text file to verify BOM stripping & NFC normalization on load.
	if err := os.WriteFile(txtPath, []byte("\uFEFFa\u0308"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write BOM + mixed/non-ASCII CSV matching the README boundary-mapping example.
	asiaCSV := "\uFEFFProduct Name,单价 (EUR),Order Status\n" +
		"Mechanical Keyboard,89.50,paid\n" +
		"Wireless Mouse,25.00,refunded\n"
	if err := os.WriteFile(asiaCSVPath, []byte(asiaCSV), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanCSV := "product,price,status\nKeyboard,89.50,paid\nMouse,25.00,refunded\n"
	if err := os.WriteFile(cleanCSVPath, []byte(cleanCSV), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badUTF8Path, []byte{0xff, 0xfe, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badCSVPath, []byte("a,b\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(emptyCSVPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dupCSVPath, []byte("id,id\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nanCSVPath, []byte("product,price,status\nKeyboard,NaN,paid\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	happySrc := fmt.Sprintf(`
def sale_status = "paid" | "refunded"

def sales_entry
    product = ""
    price   = 0
    status  = "paid" | "refunded"
end

test "load, save, bytes, and CSV boundary mapping"
    # 1. Text load (with BOM strip + NFC) & save round-trip (save is void)
    loaded_txt = load %q
    loaded_txt is "ä"
    (load %q as text) is "ä"
    save %q "hello ä"
    load %q is "hello ä"

    # 2. Binary save & load as bytes (1:1 raw, including 0xFF/0xFE)
    raw_bytes = list 0, 137, 80, 78, 71, 255, 254
    save %q raw_bytes as bytes
    loaded_bytes = load %q as bytes
    loaded_bytes is raw_bytes

    # 3. CSV boundary mapping with mixed/non-ASCII headers (index + dynamic key)
    raw_rows = load %q as csv
    count raw_rows is 2
    raw_rows.1.("Product Name") is "Mechanical Keyboard"
    raw_rows.1.("单价 (EUR)") is "89.50"

    entries = list as sales_entry
    loop raw_rows
        entry = sales_entry with
            product = it.1
            price   = it.2 as number
            status  = it.3 as sale_status
        end
        entries = append entries entry
    end

    paid = entries where it.status is "paid"
    count paid is 1
    paid.1.product is "Mechanical Keyboard"
    paid.1.price is 89.5

    # 4. Direct CSV cast (`+"`as sales_entry`"+`) converts CSV numeric cells
    clean_rows = load %q as csv
    direct_entries = clean_rows as sales_entry
    count direct_entries is 2
    direct_entries.1.price is 89.5

    # 5. Negative runtime assertions
    load %q is error
    load %q as csv is error
    load %q as csv is error
    load %q as csv is error
    load %q as csv is error
    nan_rows = load %q as csv
    nan_rows as sales_entry is error
    raw_rows.1.99 is error
    raw_rows.1.("missing") is error
    save %q (list -1) as bytes is error
    save %q (list 256) as bytes is error
    save %q (list 1.5) as bytes is error
end
`, txtPath, txtPath, txtPath, txtPath, binPath, binPath, asiaCSVPath, cleanCSVPath,
		badUTF8Path, badUTF8Path, badCSVPath, emptyCSVPath, dupCSVPath, nanCSVPath,
		binPath, binPath, binPath)

	report, err := RunChecks(happySrc)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 23 {
		t.Fatalf("expected 23 passed / 0 failed checks, got %d passed / %d failed:\n%+v", report.Passed, report.Failed, report.Results)
	}

	// Verify static TypeError when `as bytes` is used outside `load`/`save`.
	if _, err := ParseProgram("x = (list 1, 2) as bytes\n"); err == nil || !strings.Contains(err.Error(), "can only be used with 'load <path> as bytes' or 'save <path> <data> as bytes'") {
		t.Fatalf("expected static TypeError for bare 'as bytes', got: %v", err)
	}

	// Verify specific English error messages for duplicate CSV header & NaN CSV cell.
	if _, err := EvalProgram(fmt.Sprintf("x = load %q as csv\n", dupCSVPath), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Duplicate CSV header") {
		t.Fatalf("expected Duplicate CSV header error, got: %v", err)
	}
	if _, err := EvalProgram(fmt.Sprintf("def s\n  product = \"\"\n  price = 0\n  status = \"paid\"\nend\nrows = load %q as csv\nx = rows as s\n", nanCSVPath), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "expected number") {
		t.Fatalf("expected NaN CSV number error, got: %v", err)
	}
}

func TestFileModules(t *testing.T) {
	dir := t.TempDir()

	// 1. Write billing.ease with constants, def, union, 0/1/2-arg verbs, and a module-local test block.
	billingPath := filepath.Join(dir, "billing.ease")
	billingSrc := `def default_tax = 0.19

def status = "open" | "paid"

def invoice
  id = ""
  net = 0
  state = "open"
end

def outcome = invoice | "empty"

to version -> "v1"

to total inv -> inv.net * (1 + default_tax)

to apply_tax amount rate -> amount * (1 + rate)

test "module internal check should be ignored on import"
  1 is 2
end
`
	if err := os.WriteFile(billingPath, []byte(billingSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2. Write two sibling modules with colliding names to verify encapsulation.
	aPath := filepath.Join(dir, "mod_a.ease")
	bPath := filepath.Join(dir, "mod_b.ease")
	if err := os.WriteFile(aPath, []byte("def rate = 10\ndef item\n  label = \"A\"\nend\nto tag x -> \"A:\" + x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("def rate = 20\ndef item\n  label = \"B\"\nend\nto tag x -> \"B:\" + x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. Nested relative import test: sub/helper.ease imports ../mod_a.ease.
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	helperPath := filepath.Join(subDir, "helper.ease")
	if err := os.WriteFile(helperPath, []byte("import \"../mod_a.ease\" as ma\nto double_rate -> ma.rate * 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mainPath := filepath.Join(dir, "main.ease")
	mainSrc := `import "./billing.ease" as billing
import "./mod_a.ease" as a
import "./mod_b.ease" as b
import "./sub/helper.ease" as helper

def item
  label = "local"
end

to tag x -> "L:" + x

inv = billing.invoice with id = "INV-1", net = 100
inv2 = billing.invoice with id = "INV-1", net = 100

raw = '[{"id": "INV-2", "net": 200, "state": "paid"}]' as json
typed_list = raw as billing.invoice
first = typed_list.1

res = (billing.invoice with id = "INV-3", net = 50) as billing.outcome
res_label = ""
check res is billing.invoice
  res_label = res.id
else
  res_label = "none"
end

say billing:version
say billing.default_tax
say billing:total inv
say billing:apply_tax 100 0.05
check inv is billing.invoice
  say "inv_type_ok"
end
check inv is inv2
  say "inv_eq_ok"
end
check first.state is "paid"
  say "first_paid_ok"
end
say res_label
say (a:tag "x") + "|" + (b:tag "y") + "|" + (tag "z")
say a.rate + b.rate
say helper:double_rate

test "main checks pass without running billing's failing test"
  inv is billing.invoice
  inv is inv2
  first.state is "paid"
  billing:total inv is 119
  billing:apply_tax 100 0.25 is 125
  helper:double_rate is 20
end
`
	if err := os.WriteFile(mainPath, []byte(mainSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if _, err := EvalProgramFromFile(mainSrc, mainPath, &out); err != nil {
		t.Fatalf("EvalProgramFromFile failed: %v", err)
	}
	expected := "v1\n0.19\n119\n105\ninv_type_ok\ninv_eq_ok\nfirst_paid_ok\nINV-3\nA:x|B:y|L:z\n30\n20\n"
	if out.String() != expected {
		t.Fatalf("unexpected output:\ngot:\n%s\nwant:\n%s", out.String(), expected)
	}

	report, err := RunChecksFromFile(mainSrc, mainPath)
	if err != nil {
		t.Fatalf("RunChecksFromFile failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 6 {
		t.Fatalf("expected 6 passed / 0 failed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}

	// Negative tests:
	// a) Missing file
	if _, err := ParseProgramFromFile("import \"./missing.ease\" as m\n", mainPath); err == nil || !strings.Contains(err.Error(), "ImportError") {
		t.Fatalf("expected ImportError for missing file, got: %v", err)
	}

	// b) Cyclic import
	cycA := filepath.Join(dir, "cyc_a.ease")
	cycB := filepath.Join(dir, "cyc_b.ease")
	_ = os.WriteFile(cycA, []byte("import \"./cyc_b.ease\" as b\n"), 0o644)
	_ = os.WriteFile(cycB, []byte("import \"./cyc_a.ease\" as a\n"), 0o644)
	if _, err := ParseProgramFromFile("import \"./cyc_b.ease\" as b\n", cycA); err == nil || !strings.Contains(err.Error(), "Cyclic import detected") {
		t.Fatalf("expected cyclic import error, got: %v", err)
	}

	// c) Dot call on module function (both without and with arguments)
	if _, err := ParseProgramFromFile("import \"./billing.ease\" as billing\nx = billing.total\n", mainPath); err == nil || !strings.Contains(err.Error(), "Use ':' to call module functions") {
		t.Fatalf("expected ':' hint for billing.total, got: %v", err)
	}
	if _, err := ParseProgram("import system\ncmd = system.command\nsystem.run cmd\n"); err == nil || !strings.Contains(err.Error(), "Use ':' to call module functions, e.g. 'system:run ...'") {
		t.Fatalf("expected ':' hint for system.run cmd, got: %v", err)
	}

	// d) Unknown module function
	if _, err := ParseProgramFromFile("import \"./billing.ease\" as billing\nx = billing:nope\n", mainPath); err == nil || !strings.Contains(err.Error(), "has no function \"nope\"") {
		t.Fatalf("expected unknown module function error, got: %v", err)
	}

	// e) Non-exhaustive check on module union
	nonExh := "import \"./billing.ease\" as billing\nr = billing.invoice with id = \"1\", net = 1\ncheck r\n  is billing.invoice -> say r.id\nend\n"
	if _, err := ParseProgramFromFile(nonExh, mainPath); err != nil {
		// Note: r is exactRecord billing.invoice here unless inferred as union, so test an inline/casted union value:
	}
	nonExhUnion := "import \"./billing.ease\" as billing\nr = (billing.invoice with id = \"1\", net = 1) as billing.outcome\nto check_r x\n  check x is billing.invoice -> x.id\nend\n"
	if _, err := ParseProgramFromFile(nonExhUnion, mainPath); err == nil || !strings.Contains(err.Error(), "Non-exhaustive check") {
		t.Fatalf("expected non-exhaustive check error for billing.outcome, got: %v", err)
	}

	// f) Reassigning module alias
	if _, err := EvalProgramFromFile("import \"./billing.ease\" as billing\nbilling = 1\n", mainPath, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "imported module alias") {
		t.Fatalf("expected module alias reassignment error, got: %v", err)
	}

	// g) `with` on module alias
	if _, err := ParseProgramFromFile("import \"./billing.ease\" as billing\nx = billing with net = 1\n", mainPath); err == nil || !strings.Contains(err.Error(), "Cannot use 'with' on module") {
		t.Fatalf("expected with-on-module error, got: %v", err)
	}
}

func TestAskEOFLoopPreventionAndTestChecks(t *testing.T) {
	interactiveSrc := `def prompt_label = "> "

to format_cmd c -> "{prompt_label}{c}"

loop
  cmd = ask prompt_label
  check cmd is "quit" -> end
  say format_cmd cmd
end

test "interactive loop does not hang ease test and ask EOF is RuntimeError"
  format_cmd "help" is "> help"
  (ask "?") ->
    error.type is "RuntimeError"
    error.message is "Unexpected end of input (EOF) in 'ask'"
  end
end
`
	// 1. `ease test` stops top-level setup cleanly at `ask` EOF and runs all test blocks.
	report, err := RunChecks(interactiveSrc)
	if err != nil {
		t.Fatalf("RunChecks failed on interactive ask loop: %v", err)
	}
	if report.Passed != 3 || report.Failed != 0 {
		t.Fatalf("expected 3 passed / 0 failed, got %+v", report)
	}

	// 2. `ease run --ci` (headless) exits cleanly at `ask` EOF without hanging or failing.
	var headlessOut bytes.Buffer
	if _, err := EvalProgramHeadlessWithTrace(interactiveSrc, strings.NewReader(""), &headlessOut, nil); err != nil {
		t.Fatalf("EvalProgramHeadlessWithTrace failed on interactive ask loop: %v", err)
	}

	// 3. Regular `ease run` aborts cleanly with RuntimeError when stdin reaches EOF inside a loop.
	var runOut bytes.Buffer
	_, runErr := EvalProgramWithInput(interactiveSrc, strings.NewReader("look\n"), &runOut)
	if runErr == nil || !strings.Contains(runErr.Error(), "Unexpected end of input (EOF) in 'ask'") {
		t.Fatalf("expected RuntimeError on EOF in regular run, got: %v", runErr)
	}
	if runOut.String() != "> > look\n> " {
		t.Fatalf("expected single iteration output before EOF, got %q", runOut.String())
	}

	// 4. Trailing `def` constants after an `ask` EOF and pre-`ask` spawned workers do not break or hang `RunChecks` / headless `--ci`.
	spawnAndTrailingConstSrc := `to drain box
  take box
    say it
  end
end

box = inbox as text
spawn drain box

loop
  cmd = ask "> "
  check cmd is "q" -> end
  put box cmd
end
close box

def cap = 40

test "trailing const visible and worker does not hang"
  cap is 40
  (ask "?") ->
    error.type is "RuntimeError"
  end
end
`
	report2, err := RunChecks(spawnAndTrailingConstSrc)
	if err != nil {
		t.Fatalf("RunChecks failed with spawned worker + trailing const: %v", err)
	}
	if report2.Passed != 2 || report2.Failed != 0 {
		t.Fatalf("expected 2 passed / 0 failed, got %+v", report2)
	}
	if _, err := EvalProgramHeadlessWithTrace(spawnAndTrailingConstSrc, strings.NewReader(""), &bytes.Buffer{}, nil); err != nil {
		t.Fatalf("EvalProgramHeadlessWithTrace failed with spawned worker: %v", err)
	}

	// 5. Final line without trailing newline is read normally by `ask`; only the subsequent `ask` triggers EOF.
	noNewlineSrc := `first = ask ""
say first
second = ask ""
`
	var noNLOut bytes.Buffer
	_, noNLErr := EvalProgramWithInput(noNewlineSrc, strings.NewReader("Ada"), &noNLOut)
	if noNLErr == nil || !strings.Contains(noNLErr.Error(), "Unexpected end of input (EOF) in 'ask'") {
		t.Fatalf("expected RuntimeError on second ask after non-newline EOF line, got: %v", noNLErr)
	}
	if noNLOut.String() != "Ada\n" {
		t.Fatalf("expected first ask without trailing newline to yield Ada, got %q", noNLOut.String())
	}
}

func TestStandaloneBundleMultiFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	sharedDir := filepath.Join(dir, "shared")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}

	utilPath := filepath.Join(sharedDir, "util.ease")
	if err := os.WriteFile(utilPath, []byte("to bonus x -> x + 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	combatPath := filepath.Join(subDir, "combat.ease")
	combatSrc := "import \"../shared/util.ease\" as util\ndef crit = 1.5\ndef fighter\n  hp = 100\nend\nto strike f power -> f with hp = f.hp - util:bonus (power * crit)\n"
	if err := os.WriteFile(combatPath, []byte(combatSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	mainPath := filepath.Join(subDir, "main.ease")
	mainSrc := "import \"./combat.ease\" as combat\ng = combat.fighter with hp = 30\nhit = combat:strike g 10\nsay hit.hp\ntest \"bundled\"\n  hit.hp is 10\nend\n"
	if err := os.WriteFile(mainPath, []byte(mainSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	hostBin := filepath.Join(dir, "host.bin")
	outBin := filepath.Join(dir, "app.bin")
	if err := os.WriteFile(hostBin, []byte("DUMMYHOST"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := BuildStandaloneExeFromFile(hostBin, mainSrc, mainPath, outBin); err != nil {
		t.Fatalf("BuildStandaloneExeFromFile failed: %v", err)
	}

	bundle, ok, err := ReadEmbeddedBundle(outBin)
	if err != nil || !ok {
		t.Fatalf("ReadEmbeddedBundle failed: ok=%v err=%v", ok, err)
	}
	if bundle.Entry != "sub/main.ease" || len(bundle.Files) != 3 {
		t.Fatalf("unexpected bundle contents: entry=%q files=%d", bundle.Entry, len(bundle.Files))
	}

	// Unpack and verify the bundled files execute and pass `test` cleanly from a fresh directory
	unpackDir := filepath.Join(dir, "unpacked")
	for rel, content := range bundle.Files {
		target := filepath.Join(unpackDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	unpackedEntry := filepath.Join(unpackDir, filepath.FromSlash(bundle.Entry))
	var out bytes.Buffer
	if _, err := EvalProgramFromFile(bundle.Files[bundle.Entry], unpackedEntry, &out); err != nil {
		t.Fatalf("EvalProgramFromFile on unpacked bundle failed: %v", err)
	}
	if out.String() != "10\n" {
		t.Fatalf("unexpected unpacked output: got %q, want %q", out.String(), "10\n")
	}
	report, err := RunChecksFromFile(bundle.Files[bundle.Entry], unpackedEntry)
	if err != nil || report.Failed != 0 || report.Passed != 1 {
		t.Fatalf("RunChecksFromFile on unpacked bundle failed: report=%+v err=%v", report, err)
	}
}

func TestSystemModuleArgsExitAndImmutability(t *testing.T) {
	// 1. system.arguments is populated in EvalProgramWithOptions (including inside imported file modules).
	dir := t.TempDir()
	modPath := filepath.Join(dir, "cli_mod.ease")
	modSrc := `import system
to first_arg
    check count system.arguments is 0 -> ""
    -> system.arguments.1
end
`
	if err := os.WriteFile(modPath, []byte(modSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "main.ease")
	mainSrc := `import system
import "./cli_mod.ease" as cli
say count system.arguments
say system.arguments.2
say cli:first_arg
`
	var out bytes.Buffer
	_, err := EvalProgramWithOptions(mainSrc, mainPath, EvalOptions{
		Out:  &out,
		Args: []string{"--port", "8080"},
	})
	if err != nil {
		t.Fatalf("EvalProgramWithOptions failed: %v", err)
	}
	if got, want := strings.TrimSpace(strings.ReplaceAll(out.String(), "\r\n", "\n")), "2\n8080\n--port"; got != want {
		t.Fatalf("unexpected system.arguments output: got %q, want %q", got, want)
	}

	// 2. system:exit returns ExitError with exact Code / Message.
	_, errOk := EvalProgramWithOptions("import system\nsystem:exit system.ok\n", "", EvalOptions{Out: &bytes.Buffer{}})
	var exitErr *ExitError
	if !errors.As(errOk, &exitErr) || exitErr.Code != 0 || exitErr.Message != "" {
		t.Fatalf("expected ExitError{Code: 0}, got: %v", errOk)
	}

	_, errMsg := EvalProgramWithOptions("import system\nsystem:exit \"Konfiguration fehlt\"\n", "", EvalOptions{Out: &bytes.Buffer{}})
	if !errors.As(errMsg, &exitErr) || exitErr.Code != 1 || exitErr.Message != "Konfiguration fehlt" {
		t.Fatalf("expected ExitError{Code: 1, Message: 'Konfiguration fehlt'}, got: %v", errMsg)
	}

	// 3. Static rejections: reassigning system.arguments, system:exit in swarm loop, wrong type, direct sys_args/sys_exit.
	negCases := []struct {
		name string
		src  string
	}{
		{"reassign system.arguments", "import system\nsystem.arguments = list \"x\"\n"},
		{"system:exit in swarm loop", "import system\nxs = swarm loop range 1 2\n  system:exit 1\n  -> it\nend\n"},
		{"system:exit wrong static type", "import system\nsystem:exit (list 1, 2)\n"},
		{"direct sys_args", "say sys_args\n"},
		{"direct sys_exit", "sys_exit 0\n"},
		{"direct sys_run", "say sys_run 1\n"},
		{"system:run wrong arg type", "import system\nrep = system:run \"echo\"\nsay rep.code\n"},
		{"system:run result field typo", "import system\ncmd = system.command with program = \"go\"\nrep = system:run cmd\nsay rep.typo\n"},
	}
	for _, tc := range negCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseProgram(tc.src); err == nil {
				t.Fatalf("expected static parse/typecheck error for %s, got nil", tc.name)
			}
		})
	}
}

func TestRollingBufferWrapAndUTF8Healing(t *testing.T) {
	// 1. Exact fit without wrap
	rb := newRollingBuffer(64)
	msg := strings.Repeat("a", 64)
	if _, err := rb.Write([]byte(msg)); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if got := string(rb.Bytes()); got != msg {
		t.Fatalf("exact fit mismatch: got %d bytes, want 64", len(got))
	}

	// 2. Wrap-around retains the tail
	rb2 := newRollingBuffer(64)
	_, _ = rb2.Write([]byte(strings.Repeat("x", 50)))
	_, _ = rb2.Write([]byte(strings.Repeat("y", 40)))
	got2 := string(rb2.Bytes())
	want2 := strings.Repeat("x", 24) + strings.Repeat("y", 40)
	if got2 != want2 {
		t.Fatalf("wrap tail mismatch:\ngot:  %q\nwant: %q", got2, want2)
	}

	// 3. Multi-byte UTF-8 rune split across truncation boundary heals cleanly
	rb3 := newRollingBuffer(64)
	// Prefix with 63 'a's + 3-byte euro sign '€' (total 66 bytes -> drops first 2 bytes, wait:
	// let's put 1 'a' + '€' (3 bytes) + 63 'b's = 67 bytes -> drops first 3 bytes (a + 2 bytes of €),
	// leaving 1 trailing continuation byte of € followed by 63 'b's!)
	payload := "a€" + strings.Repeat("b", 63)
	_, _ = rb3.Write([]byte(payload))
	healed := rb3.Bytes()
	if !utf8.Valid(healed) {
		t.Fatalf("expected healed rolling buffer to be valid UTF-8, got %x", healed)
	}
	if got := string(healed); got != strings.Repeat("b", 63) {
		t.Fatalf("expected 63 'b's after UTF-8 boundary healing, got %q", got)
	}

	// 4. 4-byte emoji split across incremental writes heals cleanly
	rb4 := newRollingBuffer(64)
	_, _ = rb4.Write([]byte("😀" + strings.Repeat("z", 63))) // 67 bytes -> drops 3 bytes of 😀, 1 continuation byte remains
	healed4 := rb4.Bytes()
	if !utf8.Valid(healed4) || string(healed4) != strings.Repeat("z", 63) {
		t.Fatalf("expected 4-byte emoji healing to yield 63 'z's, got valid=%v str=%q", utf8.Valid(healed4), string(healed4))
	}
}

func TestSystemRunExecutionAndValidation(t *testing.T) {
	src := `import system

test "system:run captures exit 0, non-zero process_report, and rejects invalid config"
    ok_cmd = system.command with
        program = "go"
        args    = list "version"
    end
    ok_cmd.timeout is 30
    ok_cmd.max_mb is 10
    rep = system:run ok_cmd
    rep is system.process_report
    rep.code is 0
    contains rep.stdout "go version"

    # Non-zero exit code returns process_report (NOT RuntimeError)
    bad_sub = system.command with
        program = "go"
        args    = list "no_such_subcommand_ease_test"
    end
    bad_rep = system:run bad_sub
    bad_rep is system.process_report
    bad_rep.code > 0
    count bad_rep.stderr > 0

    # Programmer/OS errors raise RuntimeError
    system:run system.command is error
    system:run (system.command with program = "") is error
    system:run (system.command with program = "go", timeout = 0) is error
    system:run (system.command with program = "go", max_mb = 0) is error
    system:run (system.command with program = "go", max_mb = 600) is error
    system:run (system.command with program = "definitely_missing_binary_ease_999") is error
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 14 {
		t.Fatalf("expected 14 passed / 0 failed, got passed=%d failed=%d results=%+v", report.Passed, report.Failed, report.Results)
	}
}
