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
