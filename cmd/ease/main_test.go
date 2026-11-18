package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplMultilineAndDeclarations(t *testing.T) {
	input := strings.Join([]string{
		"def player",
		"    x = 10",
		"    role = \"guest\" | \"admin\"",
		"end",
		"to add a b",
		"    a + b",
		"end",
		"p = player with x = 25, role = \"admin\"",
		"say p.x",
		"add 2 3",
		"total = 0",
		"loop range 1 4",
		"    total = total + it",
		"end",
		"say total",
		"exit",
	}, "\n") + "\n"

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := runRepl(strings.NewReader(input), &out, &errOut)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d; stderr=%s", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("expected empty stderr, got: %s", errOut.String())
	}
	outStr := out.String()
	for _, want := range []string{"...   ", "25", "5", "10"} {
		if !strings.Contains(outStr, want) {
			t.Fatalf("expected %q in REPL output, got:\n%s", want, outStr)
		}
	}
}

func TestCmdCheckAndJSON(t *testing.T) {
	dir := t.TempDir()
	okFile := filepath.Join(dir, "ok.ease")
	okSrc := `def user
  name = "Ada"
end

to greet u
  "Hi {u.name}"
end

# Runtime crash only if called; ` + "`ease check`" + ` must succeed without executing.
say "hello"

test "smoke"
  1 + 1 is 2
end
`
	if err := os.WriteFile(okFile, []byte(okSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := cmdCheckWithWriters([]string{okFile}, &out, &errOut); code != 0 {
		t.Fatalf("expected exit code 0, got %d; stderr=%s", code, errOut.String())
	}
	wantText := "OK: " + okFile + " (1 defs, 1 functions, 1 test blocks)"
	if !strings.Contains(out.String(), wantText) {
		t.Fatalf("expected %q in stdout, got %q", wantText, out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := cmdCheckWithWriters([]string{okFile, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("expected exit code 0, got %d; stderr=%s", code, errOut.String())
	}
	var chk jsonCheckResult
	if err := json.Unmarshal(out.Bytes(), &chk); err != nil {
		t.Fatalf("invalid JSON from ease check --json: %v\n%s", err, out.String())
	}
	if !chk.OK || chk.File != okFile || chk.Defs != 1 || chk.Functions != 1 || chk.TestBlocks != 1 {
		t.Fatalf("unexpected check JSON: %+v", chk)
	}

	badFile := filepath.Join(dir, "bad.ease")
	if err := os.WriteFile(badFile, []byte("x = \"41\" + 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errOut.Reset()
	if code := cmdCheckWithWriters([]string{badFile}, &out, &errOut); code != 1 {
		t.Fatalf("expected exit code 1 for bad file, got %d", code)
	}
	if !strings.Contains(errOut.String(), "TypeError") {
		t.Fatalf("expected TypeError on stderr, got: %q", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := cmdCheckWithWriters([]string{badFile, "--json"}, &out, &errOut); code != 1 {
		t.Fatalf("expected exit code 1 for bad file --json, got %d", code)
	}
	var errResp jsonErrorResponse
	if err := json.Unmarshal(out.Bytes(), &errResp); err != nil {
		t.Fatalf("invalid JSON error: %v\n%s", err, out.String())
	}
	if errResp.OK || errResp.File != badFile || errResp.Error == nil || errResp.Error.Stage != "TypeError" || errResp.Error.Line != 1 {
		t.Fatalf("unexpected JSON error payload: %+v", errResp)
	}
}

func TestCmdTestAndAstJSON(t *testing.T) {
	dir := t.TempDir()
	passFile := filepath.Join(dir, "pass.ease")
	if err := os.WriteFile(passFile, []byte("def p\n  x = 1\nend\ntest \"math\"\n  1 + 2 is 3\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := cmdTestWithWriters([]string{passFile, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("expected exit code 0, got %d; stderr=%s", code, errOut.String())
	}
	var passRes jsonTestResult
	if err := json.Unmarshal(out.Bytes(), &passRes); err != nil {
		t.Fatalf("invalid test JSON: %v\n%s", err, out.String())
	}
	if !passRes.OK || passRes.Passed != 1 || passRes.Failed != 0 || passRes.Total != 1 || len(passRes.Failures) != 0 {
		t.Fatalf("unexpected pass test JSON: %+v", passRes)
	}

	failFile := filepath.Join(dir, "fail.ease")
	if err := os.WriteFile(failFile, []byte("test \"broken\"\n  1 + 2 is 4\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errOut.Reset()
	if code := cmdTestWithWriters([]string{failFile, "--json"}, &out, &errOut); code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	var failRes jsonTestResult
	if err := json.Unmarshal(out.Bytes(), &failRes); err != nil {
		t.Fatalf("invalid fail test JSON: %v\n%s", err, out.String())
	}
	if failRes.OK || failRes.Passed != 0 || failRes.Failed != 1 || len(failRes.Failures) != 1 {
		t.Fatalf("unexpected fail test JSON: %+v", failRes)
	}
	if failRes.Failures[0].Test != "broken" || failRes.Failures[0].Line != 2 {
		t.Fatalf("unexpected failure entry: %+v", failRes.Failures[0])
	}

	// ease ast --json
	out.Reset()
	errOut.Reset()
	if code := cmdAstWithWriters([]string{passFile, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("expected exit code 0 from ease ast --json, got %d; stderr=%s", code, errOut.String())
	}
	var astMap map[string]any
	if err := json.Unmarshal(out.Bytes(), &astMap); err != nil {
		t.Fatalf("invalid AST JSON: %v\n%s", err, out.String())
	}
	if _, hasDefs := astMap["Defs"]; !hasDefs {
		t.Fatalf("expected Defs key in AST JSON, got: %v", astMap)
	}
}

func TestReplRejectsPrintingConditions(t *testing.T) {
	input := "not 2 > 1\n3 > 2\nexit\n"
	var out, errOut bytes.Buffer
	code := runRepl(strings.NewReader(input), &out, &errOut)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if strings.Contains(out.String(), "false") || strings.Contains(out.String(), "true") {
		t.Fatalf("REPL must not print raw boolean condition values, got stdout: %q", out.String())
	}
	if strings.Count(errOut.String(), "Conditions cannot be stored or printed") != 2 {
		t.Fatalf("expected 2 condition TypeErrors on stderr, got: %q", errOut.String())
	}
}
