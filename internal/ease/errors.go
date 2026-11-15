package ease

import (
	"fmt"
	"strings"
)

// EaseError provides human-friendly diagnostics with source context and an actionable hint.
type EaseError struct {
	Stage   string // e.g. "SyntaxError", "RuntimeError", "TestError"
	Message string
	Hint    string
	Line    int
	Col     int
	Source  string
}

func (e *EaseError) Error() string {
	var b strings.Builder
	stage := e.Stage
	if stage == "" {
		stage = "EaseError"
	}
	if e.Line > 0 {
		fmt.Fprintf(&b, "%s at line %d, col %d: %s", stage, e.Line, e.Col, e.Message)
	} else {
		fmt.Fprintf(&b, "%s: %s", stage, e.Message)
	}

	if e.Source != "" && e.Line > 0 {
		lines := strings.Split(strings.ReplaceAll(e.Source, "\r\n", "\n"), "\n")
		if e.Line <= len(lines) {
			lineText := lines[e.Line-1]
			caretCol := max(1, e.Col)
			fmt.Fprintf(&b, "\n  %4d | %s", e.Line, lineText)
			fmt.Fprintf(&b, "\n       | %s^", strings.Repeat(" ", caretCol-1))
		}
	}

	if e.Hint != "" {
		fmt.Fprintf(&b, "\n  Hint: %s", e.Hint)
	}
	return b.String()
}

func newError(stage string, line, col int, source, msg, hint string) *EaseError {
	return &EaseError{
		Stage:   stage,
		Message: msg,
		Hint:    hint,
		Line:    line,
		Col:     col,
		Source:  source,
	}
}
