package ease

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// CheckOutcome records the result of one inline `is`, `is-not`, or `raises` assertion.
type CheckOutcome struct {
	Scope   string
	Line    int
	Passed  bool
	Message string
}

// CheckReport summarizes all function `test` and top-level `check` assertions in a file.
type CheckReport struct {
	Total   int
	Passed  int
	Failed  int
	Results []CheckOutcome
}

// RunChecks executes all `test` blocks on functions and top-level `check` blocks.
func RunChecks(source string) (*CheckReport, error) {
	return RunChecksWithTrace(source, nil)
}

// RunChecksWithTrace executes all `test` and `check` blocks, writing optional trace logs to `trace`.
func RunChecksWithTrace(source string, trace io.Writer) (*CheckReport, error) {
	prog, err := ParseProgram(source)
	if err != nil {
		return nil, err
	}

	// Evaluate top-level bindings silently with non-blocking empty stdin so `check` blocks
	// can reference module constants without ever hanging on interactive `ask`.
	ev := NewEvaluatorWithInput(source, prog, strings.NewReader(""), io.Discard)
	defer ev.Close()
	ev.SetHeadlessScreen(true)
	ev.SetTrace(trace)
	ev.logTrace("parsed %d def(s), %d function(s), %d check block(s), %d top-level statement(s)",
		len(prog.Defs), len(prog.Functions), len(prog.Checks), len(prog.Body))
	ev.logTrace("running top-level setup (%d statement(s))", len(prog.Body))
	for _, stmt := range prog.Body {
		if _, _, err := ev.execStmt(stmt, ev.global); err != nil {
			return nil, err
		}
	}

	report := &CheckReport{}

	for _, blk := range prog.Checks {
		scopeName := fmt.Sprintf("check %q", blk.Title)
		ev.logTrace("running %s (%d assertion(s))", scopeName, len(blk.Body))
		scopeEnv := NewEnv(ev.global)
		runCheckList(ev, scopeName, blk.Body, scopeEnv, report)
	}

	return report, nil
}

func runCheckList(ev *Evaluator, scope string, items []Stmt, env *Env, report *CheckReport) {
	for _, item := range items {
		switch c := item.(type) {
		case *LetStmt, *AssignStmt, *IfStmt, *RepeatStmt, *ForStmt:
			if _, _, err := ev.execStmt(c, env); err != nil {
				line, _ := c.Pos()
				report.Total++
				report.Failed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    line,
					Passed:  false,
					Message: fmt.Sprintf("Setup error: %v", err),
				})
				return
			}

		case *CheckIsStmt:
			report.Total++
			actual, errA := ev.EvalExpr(c.Actual, env)
			if errA != nil {
				report.Failed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  false,
					Message: fmt.Sprintf("Line %d ('is'): unexpected error: %v", c.Line, errA),
				})
				continue
			}
			expected, errE := ev.EvalExpr(c.Expected, env)
			if errE != nil {
				report.Failed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  false,
					Message: fmt.Sprintf("Line %d ('is'): expected expression errored: %v", c.Line, errE),
				})
				continue
			}
			if ValuesEqual(actual, expected) {
				report.Passed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  true,
					Message: fmt.Sprintf("Line %d: got %s (ok)", c.Line, FormatValue(actual)),
				})
			} else {
				report.Failed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  false,
					Message: fmt.Sprintf("Line %d: expected %s, got %s", c.Line, FormatValue(expected), FormatValue(actual)),
				})
			}

		case *CheckIsNotStmt:
			report.Total++
			actual, errA := ev.EvalExpr(c.Actual, env)
			if errA != nil {
				report.Failed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  false,
					Message: fmt.Sprintf("Line %d ('is-not'): unexpected error: %v", c.Line, errA),
				})
				continue
			}
			unexpected, errU := ev.EvalExpr(c.Unexpected, env)
			if errU != nil {
				report.Failed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  false,
					Message: fmt.Sprintf("Line %d ('is-not'): comparison expression errored: %v", c.Line, errU),
				})
				continue
			}
			if !ValuesEqual(actual, unexpected) {
				report.Passed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  true,
					Message: fmt.Sprintf("Line %d: %s != %s (ok)", c.Line, FormatValue(actual), FormatValue(unexpected)),
				})
			} else {
				report.Failed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  false,
					Message: fmt.Sprintf("Line %d: expected value NOT to equal %s", c.Line, FormatValue(unexpected)),
				})
			}

		case *CheckRaisesStmt:
			report.Total++
			expectedVal, errE := ev.EvalExpr(c.Expected, env)
			if errE != nil {
				report.Failed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  false,
					Message: fmt.Sprintf("Line %d ('raises'): invalid expected pattern: %v", c.Line, errE),
				})
				continue
			}
			expectedSub := FormatValue(expectedVal)
			val, actualErr := ev.EvalExpr(c.Expression, env)
			if actualErr == nil {
				report.Failed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  false,
					Message: fmt.Sprintf("Line %d: expected error containing %q, but succeeded with %s", c.Line, expectedSub, FormatValue(val)),
				})
				continue
			}
			actualMsg := actualErr.Error()
			var easeErr *EaseError
			if errors.As(actualErr, &easeErr) {
				actualMsg = easeErr.Message
			}
			if strings.Contains(strings.ToLower(actualMsg), strings.ToLower(expectedSub)) {
				report.Passed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  true,
					Message: fmt.Sprintf("Line %d: raised error matching %q (ok)", c.Line, expectedSub),
				})
			} else {
				report.Failed++
				report.Results = append(report.Results, CheckOutcome{
					Scope:   scope,
					Line:    c.Line,
					Passed:  false,
					Message: fmt.Sprintf("Line %d: raised %q, expected substring %q", c.Line, actualMsg, expectedSub),
				})
			}
		}
	}
}
