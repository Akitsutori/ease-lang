package main

import (
	"bytes"
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
