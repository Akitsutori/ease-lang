package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildTestEaseBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ease-test.exe")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build ease binary: %v\n%s", err, out)
	}
	return bin
}

func TestCLISystemArgumentsAndExitCodes(t *testing.T) {
	bin := buildTestEaseBinary(t)
	tmpDir := t.TempDir()

	argsScript := filepath.Join(tmpDir, "args.ease")
	argsSrc := `import system
say count system.arguments
loop system.arguments
    say "{it.index}:{it}"
end
`
	if err := os.WriteFile(argsScript, []byte(argsSrc), 0o644); err != nil {
		t.Fatalf("write args.ease: %v", err)
	}

	// 1. `ease run <file> -- --check --port 8080` forwards flags after `--` untouched.
	cmd := exec.Command(bin, "run", argsScript, "--", "--check", "--port", "8080")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run with -- failed: %v\n%s", err, out)
	}
	wantOut := "3\n1:--check\n2:--port\n3:8080"
	if got := strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")); got != wantOut {
		t.Fatalf("unexpected args output after --:\ngot:\n%s\nwant:\n%s", got, wantOut)
	}

	// 2. `ease <file> --port 8080 alpha` forwards unknown flags & positional args without `--`.
	cmd2 := exec.Command(bin, argsScript, "--port", "8080", "alpha")
	out2, err2 := cmd2.CombinedOutput()
	if err2 != nil {
		t.Fatalf("direct ease <file> with args failed: %v\n%s", err2, out2)
	}
	wantOut2 := "3\n1:--port\n2:8080\n3:alpha"
	if got := strings.TrimSpace(strings.ReplaceAll(string(out2), "\r\n", "\n")); got != wantOut2 {
		t.Fatalf("unexpected args output for direct file invocation:\ngot:\n%s\nwant:\n%s", got, wantOut2)
	}

	// 3. `system:exit system.ok` exits with 0 and no extra stderr.
	okScript := filepath.Join(tmpDir, "exit_ok.ease")
	if err := os.WriteFile(okScript, []byte("import system\nsystem:exit system.ok\nsay \"unreachable\"\n"), 0o644); err != nil {
		t.Fatalf("write exit_ok.ease: %v", err)
	}
	var stdout, stderr bytes.Buffer
	cmdOk := exec.Command(bin, "run", okScript)
	cmdOk.Stdout = &stdout
	cmdOk.Stderr = &stderr
	if err := cmdOk.Run(); err != nil {
		t.Fatalf("expected exit code 0 for system.ok, got err=%v stderr=%q", err, stderr.String())
	}
	if stdout.String() != "" || stderr.String() != "" {
		t.Fatalf("expected empty stdout/stderr for system.ok, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	// 4. `system:exit 2` exits with exact code 2 and empty stderr.
	codeScript := filepath.Join(tmpDir, "exit_code.ease")
	if err := os.WriteFile(codeScript, []byte("import system\nsystem:exit 2\n"), 0o644); err != nil {
		t.Fatalf("write exit_code.ease: %v", err)
	}
	stderr.Reset()
	cmdCode := exec.Command(bin, "run", codeScript)
	cmdCode.Stderr = &stderr
	errCode := cmdCode.Run()
	exitErr, ok := errCode.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit code 2, got err=%v", errCode)
	}
	if stderr.String() != "" {
		t.Fatalf("expected empty stderr for numeric exit code, got %q", stderr.String())
	}

	// 5. `system:exit "Konfiguration fehlt"` prints message to stderr (no RuntimeError prefix) and exits with 1.
	msgScript := filepath.Join(tmpDir, "exit_msg.ease")
	if err := os.WriteFile(msgScript, []byte("import system\nsystem:exit \"Konfiguration fehlt\"\n"), 0o644); err != nil {
		t.Fatalf("write exit_msg.ease: %v", err)
	}
	stderr.Reset()
	cmdMsg := exec.Command(bin, "run", msgScript)
	cmdMsg.Stderr = &stderr
	errMsg := cmdMsg.Run()
	exitMsgErr, ok := errMsg.(*exec.ExitError)
	if !ok || exitMsgErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got err=%v", errMsg)
	}
	if got := strings.TrimSpace(stderr.String()); got != "Konfiguration fehlt" {
		t.Fatalf("expected stderr %q, got %q", "Konfiguration fehlt", got)
	}
}

func TestCLIRunCIFlagAndSystemRunSubprocess(t *testing.T) {
	bin := buildTestEaseBinary(t)
	tmpDir := t.TempDir()

	// 1. `ease run <file> --ci` runs tests then headless program (ask EOF exits cleanly).
	ciScript := filepath.Join(tmpDir, "ci_demo.ease")
	ciSrc := `to dbl x -> x * 2

test "dbl"
    dbl 4 is 8
end

say "before-ask"
name = ask "Name?"
say "hi:{name}"
`
	if err := os.WriteFile(ciScript, []byte(ciSrc), 0o644); err != nil {
		t.Fatalf("write ci_demo.ease: %v", err)
	}
	outCI, errCI := exec.Command(bin, "run", ciScript, "--ci").CombinedOutput()
	if errCI != nil {
		t.Fatalf("ease run --ci failed: %v\n%s", errCI, outCI)
	}
	gotCI := string(outCI)
	if !strings.Contains(gotCI, "1 passed, 0 failed") || !strings.Contains(gotCI, "before-ask") {
		t.Fatalf("unexpected --ci output:\n%s", gotCI)
	}

	// 2. Removed top-level `--check` flag is rejected cleanly.
	errOld := exec.Command(bin, "--check", ciScript).Run()
	if exitErr, ok := errOld.(*exec.ExitError); !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit code 2 for removed --check flag, got: %v", errOld)
	}

	// 3. `system:run` invoking the built ease binary captures non-zero exit code 2 as process_report.
	childScript := filepath.Join(tmpDir, "child_exit2.ease")
	if err := os.WriteFile(childScript, []byte("import system\nsay \"child-out\"\nsystem:exit 2\n"), 0o644); err != nil {
		t.Fatalf("write child_exit2.ease: %v", err)
	}
	parentScript := filepath.Join(tmpDir, "parent_run.ease")
	parentSrc := "import system\ncmd = system.command with\n    program = '" + bin + "'\n    args    = list 'run', '" + childScript + "'\nend\nrep = system:run cmd\nsay \"code={rep.code}|out={trim rep.stdout}\"\n"
	if err := os.WriteFile(parentScript, []byte(parentSrc), 0o644); err != nil {
		t.Fatalf("write parent_run.ease: %v", err)
	}
	outRun, errRun := exec.Command(bin, "run", parentScript).CombinedOutput()
	if errRun != nil {
		t.Fatalf("parent system:run failed: %v\n%s", errRun, outRun)
	}
	if got := strings.TrimSpace(string(outRun)); got != "code=2|out=child-out" {
		t.Fatalf("unexpected system:run subprocess output: got %q, want %q", got, "code=2|out=child-out")
	}
}

func TestReplParityAndTestBlocks(t *testing.T) {
	input := strings.Join([]string{
		"k = list 12, 13, 14",
		"k",
		"test \"t\"",
		"  contains k 13",
		"end",
		"test \"fail\"",
		"  1 is 2",
		"end",
		"10 > 5",
		"contains k 13",
		"xs = list \"a\", \"b\"",
		"check contains xs 32",
		"  say \"no\"",
		"end",
		"check 1 is 1",
		":reset",
		"40 + 2",
		"exit",
	}, "\n") + "\n"

	var out, errOut bytes.Buffer
	code := runRepl(strings.NewReader(input), &out, &errOut)
	if code != 0 {
		t.Fatalf("runRepl returned %d, errOut=%s", code, errOut.String())
	}

	outStr := out.String()
	errStr := errOut.String()

	if !strings.Contains(outStr, "[12, 13, 14]") {
		t.Fatalf("expected REPL to print list k, got out:\n%s", outStr)
	}
	if !strings.Contains(outStr, "Summary: 1 passed, 0 failed (1 assertions in 1 test blocks)") {
		t.Fatalf("expected passing test block summary in REPL, got out:\n%s", outStr)
	}
	if !strings.Contains(outStr, "FAIL") || !strings.Contains(outStr, "Summary: 0 passed, 1 failed (1 assertions in 1 test blocks)") {
		t.Fatalf("expected failing test block report and continued session, got out:\n%s", outStr)
	}
	if !strings.Contains(outStr, "42") {
		t.Fatalf("expected :reset + 40 + 2 to print 42, got out:\n%s", outStr)
	}
	if strings.Count(errStr, "Standalone condition has no effect") != 2 {
		t.Fatalf("expected 2 standalone condition errors (10 > 5 and contains k 13), got errOut:\n%s", errStr)
	}
	if !strings.Contains(errStr, "Cannot check 'contains' for number in list of text") {
		t.Fatalf("expected cross-line static TypeError for contains xs 32, got errOut:\n%s", errStr)
	}
}
