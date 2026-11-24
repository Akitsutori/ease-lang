package ease

import (
	"errors"
	"fmt"
	"strings"
)

// ExitError signals an explicit program termination requested via `system:exit`.
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("exit code %d", e.Code)
}

// EaseError provides human-friendly diagnostics with source context and an actionable hint.
type EaseError struct {
	Stage    string // e.g. "SyntaxError", "RuntimeError", "TestError"
	Message  string
	Hint     string
	Line     int
	Col      int
	Source   string
	isAskEOF bool
}

func isAskEOFError(err error) bool {
	var easeErr *EaseError
	return errors.As(err, &easeErr) && easeErr.isAskEOF
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

// NewEmptyFileError returns a SyntaxError diagnostic for empty or comment-only Ease files.
func NewEmptyFileError(source string) *EaseError {
	return &EaseError{
		Stage:   "SyntaxError",
		Line:    1,
		Col:     1,
		Source:  source,
		Message: "File contains no Ease code or declarations",
		Hint:    "Add statements, functions, or definitions (or check whether you saved the file in your editor).",
	}
}

// IsIncompleteBlockError reports whether a parse error indicates an unclosed multi-line block waiting for `end`.
func IsIncompleteBlockError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "missing 'end'") ||
		strings.Contains(msg, "missing a closing 'end'") ||
		strings.Contains(msg, "Expected 'end'")
}
