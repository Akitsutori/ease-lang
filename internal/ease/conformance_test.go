package ease

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestSyntaxConformancePositive(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "tests", "conformance", "*.ease"))
	if err != nil {
		t.Fatalf("glob positive conformance files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected positive conformance .ease files")
	}
	sort.Strings(files)

	for _, relPath := range files {
		absPath, err := filepath.Abs(relPath)
		if err != nil {
			t.Fatalf("abs path %s: %v", relPath, err)
		}
		rawBytes, err := os.ReadFile(absPath)
		if err != nil {
			t.Fatalf("read %s: %v", relPath, err)
		}
		src := string(rawBytes)
		name := filepath.Base(relPath)

		t.Run(name, func(t *testing.T) {
			rep, err := RunChecksFromFile(src, absPath)
			if err != nil {
				t.Fatalf("RunChecksFromFile failed: %v", err)
			}
			if rep.Failed > 0 || rep.Passed == 0 {
				t.Fatalf("expected all tests passing (>0 passed, 0 failed), got: %+v", rep)
			}

			if strings.HasPrefix(src, "# wasm-parity") {
				var evalOut bytes.Buffer
				if _, err := EvalProgramFromFile(src, absPath, &evalOut); err != nil {
					t.Fatalf("EvalProgramFromFile failed: %v", err)
				}
				wasmBytes, err := CompileProgramToWasm(src, absPath)
				if err != nil {
					t.Fatalf("CompileProgramToWasm failed: %v", err)
				}
				if _, err := exec.LookPath("node"); err == nil {
					res := runWasmInNode(t, wasmBytes, `[]`, 1)
					wantOut := strings.TrimSpace(strings.ReplaceAll(evalOut.String(), "\r\n", "\n"))
					gotOut := strings.TrimSpace(strings.Join(res.Prints, "\n"))
					if gotOut != wantOut {
						t.Fatalf("Interpreter/WASM stdout mismatch:\nwant:\n%s\ngot:\n%s", wantOut, gotOut)
					}
				}
			}
		})
	}
}

func TestSyntaxConformanceNegative(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "tests", "conformance", "negative", "*.ease"))
	if err != nil {
		t.Fatalf("glob negative conformance files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected negative conformance .ease files")
	}
	sort.Strings(files)

	for _, relPath := range files {
		absPath, err := filepath.Abs(relPath)
		if err != nil {
			t.Fatalf("abs path %s: %v", relPath, err)
		}
		rawBytes, err := os.ReadFile(absPath)
		if err != nil {
			t.Fatalf("read %s: %v", relPath, err)
		}
		src := string(rawBytes)
		firstLine := strings.SplitN(strings.ReplaceAll(src, "\r\n", "\n"), "\n", 2)[0]
		spec := strings.TrimSpace(strings.TrimPrefix(firstLine, "# expect:"))
		parts := strings.SplitN(spec, ":", 2)
		if !strings.HasPrefix(firstLine, "# expect:") || len(parts) != 2 {
			t.Fatalf("%s line 1 must be `# expect: <ErrorKind>: <substring>`, got %q", relPath, firstLine)
		}
		wantKind := strings.TrimSpace(parts[0])
		wantSub := strings.TrimSpace(parts[1])
		name := filepath.Base(relPath)

		t.Run(name, func(t *testing.T) {
			_, err := ParseProgramFromFile(src, absPath)
			if err == nil {
				t.Fatalf("expected %s containing %q, got nil error", wantKind, wantSub)
			}
			full := err.Error()
			if !strings.Contains(full, wantKind) || !strings.Contains(full, wantSub) {
				t.Fatalf("expected error containing %q and %q, got:\n%s", wantKind, wantSub, full)
			}
		})
	}
}
