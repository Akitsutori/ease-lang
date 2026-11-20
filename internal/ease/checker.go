package ease

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// CheckOutcome records the result of one soft assertion inside a `test` block.
type CheckOutcome struct {
	Scope        string
	TestTitle    string
	Line         int
	ContextLines []int
	VarsDetail   string
	Passed       bool
	Message      string
}

// CheckReport summarizes all `test` blocks and assertions in a file.
type CheckReport struct {
	Total      int
	Passed     int
	Failed     int
	TestBlocks int
	Results    []CheckOutcome
}

// RunChecks executes all `test` blocks in `source`.
func RunChecks(source string) (*CheckReport, error) {
	return runChecksWithOptions(source, "", nil)
}

// RunChecksFromFile executes all `test` blocks in `source` resolved relative to `filePath`.
func RunChecksFromFile(source string, filePath string) (*CheckReport, error) {
	return runChecksWithOptions(source, filePath, nil)
}

// RunChecksWithTrace executes all `test` blocks, writing optional trace logs to `trace`.
func RunChecksWithTrace(source string, trace io.Writer) (*CheckReport, error) {
	return runChecksWithOptions(source, "", trace)
}

// RunChecksWithTraceFromFile executes all `test` blocks resolved relative to `filePath`, writing optional trace logs to `trace`.
func RunChecksWithTraceFromFile(source string, filePath string, trace io.Writer) (*CheckReport, error) {
	return runChecksWithOptions(source, filePath, trace)
}

func runChecksWithOptions(source string, filePath string, trace io.Writer) (*CheckReport, error) {
	var prog *Program
	var err error
	if filePath != "" {
		prog, err = ParseProgramFromFile(source, filePath)
	} else {
		prog, err = ParseProgram(source)
	}
	if err != nil {
		return nil, err
	}

	// Evaluate top-level bindings silently with non-blocking empty stdin so `test` blocks
	// can reference module constants without hanging on interactive `ask`.
	ev := NewEvaluatorWithInput(source, prog, strings.NewReader(""), io.Discard)
	defer ev.Close()
	ev.SetHeadlessScreen(true)
	ev.SetHeadlessNet(true)
	ev.SetTrace(trace)
	ev.logTrace("parsed %d def(s), %d function(s), %d test block(s), %d top-level statement(s)",
		len(prog.Defs), len(prog.Functions), len(prog.Checks), len(prog.Body))
	ev.logTrace("running top-level setup (%d statement(s))", len(prog.Body))
	for _, stmt := range prog.Body {
		if _, _, err := ev.execStmt(stmt, ev.global); err != nil {
			return nil, err
		}
	}
	if err := ev.WaitWorkers(); err != nil {
		return nil, err
	}
	ev.SetHeadlessNet(false)

	report := &CheckReport{TestBlocks: len(prog.Checks)}
	srcLines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")

	for _, blk := range prog.Checks {
		scopeName := fmt.Sprintf("test %q", blk.Title)
		ev.logTrace("running %s (%d statement(s))", scopeName, len(blk.Body))
		runner := &testRunner{
			ev:           ev,
			srcLines:     srcLines,
			report:       report,
			testTitle:    blk.Title,
			scope:        scopeName,
			contextLines: []int{blk.Line},
		}
		scopeEnv := NewEnv(ev.global)
		runner.runStmts(blk.Body, scopeEnv)
	}

	if err := ev.WaitWorkers(); err != nil {
		return nil, err
	}

	return report, nil
}

type testRunner struct {
	ev           *Evaluator
	srcLines     []string
	report       *CheckReport
	testTitle    string
	scope        string
	contextLines []int
}

func (r *testRunner) recordPass(line int, msg string) {
	r.report.Total++
	r.report.Passed++
	r.report.Results = append(r.report.Results, CheckOutcome{
		Scope:     r.scope,
		TestTitle: r.testTitle,
		Line:      line,
		Passed:    true,
		Message:   msg,
	})
}

func (r *testRunner) recordFail(line int, varsDetail, msg string) {
	r.report.Total++
	r.report.Failed++
	ctx := make([]int, len(r.contextLines)+1)
	copy(ctx, r.contextLines)
	ctx[len(r.contextLines)] = line
	r.report.Results = append(r.report.Results, CheckOutcome{
		Scope:        r.scope,
		TestTitle:    r.testTitle,
		Line:         line,
		ContextLines: ctx,
		VarsDetail:   varsDetail,
		Passed:       false,
		Message:      msg,
	})
}

func (r *testRunner) runStmts(items []Stmt, env *Env) bool {
	for _, item := range items {
		switch c := item.(type) {
		case *ConstDeclStmt, *AssignStmt, *SayStmt, *ExprStmt, *SpawnStmt, *TakeStmt:
			if _, _, err := r.ev.execStmt(c, env); err != nil {
				line, _ := c.Pos()
				r.recordFail(line, errMessage(err), fmt.Sprintf("Setup error: %v", err))
				return false
			}

		case *LoopStmt:
			if c.Iterable == nil {
				r.recordFail(c.Line, "", "Infinite 'loop' cannot run directly inside a 'test' block; call a 'to' function from 'test' instead.")
				return false
			}
			iterVal, err := r.ev.EvalExpr(c.Iterable, env)
			if err != nil {
				r.recordFail(c.Line, errMessage(err), fmt.Sprintf("Loop iterable error: %v", err))
				return false
			}
			lv, ok := iterVal.(*ListValue)
			if !ok {
				r.recordFail(c.Line, fmt.Sprintf("iterable = %s", FormatValue(iterVal)), fmt.Sprintf("'loop' requires a list, got %s", TypeName(iterVal)))
				return false
			}
			r.contextLines = append(r.contextLines, c.Line)
			for idx, el := range lv.Elements {
				loopEnv := NewEnv(env)
				loopEnv.Define("it", el, false)
				loopEnv.SetLoopIndex(float64(idx))
				r.runStmts(c.Body, loopEnv)
			}
			r.contextLines = r.contextLines[:len(r.contextLines)-1]

		case *CheckIsStmt:
			actual, errA := r.ev.EvalExpr(c.Actual, env)
			if errA != nil {
				r.recordFail(c.Line, r.buildVarsDetail([]Expr{c.Actual, c.Expected}, errMessage(errA), env), fmt.Sprintf("Line %d ('is'): %v", c.Line, errA))
				continue
			}
			expected, errE := r.ev.EvalExpr(c.Expected, env)
			if errE != nil {
				r.recordFail(c.Line, r.buildVarsDetail([]Expr{c.Actual, c.Expected}, errMessage(errE), env), fmt.Sprintf("Line %d ('is'): %v", c.Line, errE))
				continue
			}
			matched, errM := r.ev.evalIs(actual, expected, c.Line, c.Col)
			if errM != nil {
				r.recordFail(c.Line, r.buildVarsDetail([]Expr{c.Actual, c.Expected}, errMessage(errM), env), fmt.Sprintf("Line %d ('is'): %v", c.Line, errM))
				continue
			}
			if matched {
				r.recordPass(c.Line, fmt.Sprintf("Line %d: %s (ok)", c.Line, FormatValue(actual)))
			} else {
				detail := r.buildVarsDetail([]Expr{c.Actual, c.Expected}, "", env)
				if detail == "" {
					detail = fmt.Sprintf("%s is not %s", FormatValue(actual), FormatValue(expected))
				}
				r.recordFail(c.Line, detail, fmt.Sprintf("Line %d: %s", c.Line, detail))
			}

		case *CheckIsNotStmt:
			actual, errA := r.ev.EvalExpr(c.Actual, env)
			if errA != nil {
				r.recordFail(c.Line, r.buildVarsDetail([]Expr{c.Actual, c.Unexpected}, errMessage(errA), env), fmt.Sprintf("Line %d ('is not'): %v", c.Line, errA))
				continue
			}
			unexpected, errU := r.ev.EvalExpr(c.Unexpected, env)
			if errU != nil {
				r.recordFail(c.Line, r.buildVarsDetail([]Expr{c.Actual, c.Unexpected}, errMessage(errU), env), fmt.Sprintf("Line %d ('is not'): %v", c.Line, errU))
				continue
			}
			matched, errM := r.ev.evalIs(actual, unexpected, c.Line, c.Col)
			if errM != nil {
				r.recordFail(c.Line, r.buildVarsDetail([]Expr{c.Actual, c.Unexpected}, errMessage(errM), env), fmt.Sprintf("Line %d ('is not'): %v", c.Line, errM))
				continue
			}
			if !matched {
				r.recordPass(c.Line, fmt.Sprintf("Line %d: %s != %s (ok)", c.Line, FormatValue(actual), FormatValue(unexpected)))
			} else {
				detail := r.buildVarsDetail([]Expr{c.Actual, c.Unexpected}, "", env)
				if detail == "" {
					detail = fmt.Sprintf("both sides evaluated to %s", FormatValue(actual))
				}
				r.recordFail(c.Line, detail, fmt.Sprintf("Line %d: %s", c.Line, detail))
			}

		case *CheckCondStmt:
			val, err := r.ev.EvalExpr(c.Condition, env)
			if err != nil {
				r.recordFail(c.Line, r.buildVarsDetail([]Expr{c.Condition}, errMessage(err), env), fmt.Sprintf("Line %d: %v", c.Line, err))
				continue
			}
			b, ok := val.(bool)
			if !ok {
				r.recordFail(c.Line, r.buildVarsDetail([]Expr{c.Condition}, fmt.Sprintf("got %s instead of condition", TypeName(val)), env), fmt.Sprintf("Line %d: assertion must be a condition, got %s", c.Line, TypeName(val)))
				continue
			}
			if b {
				r.recordPass(c.Line, fmt.Sprintf("Line %d: condition holds (ok)", c.Line))
			} else {
				suffix := r.condComparisonSuffix(c.Condition, env)
				detail := r.buildVarsDetail([]Expr{c.Condition}, suffix, env)
				r.recordFail(c.Line, detail, fmt.Sprintf("Line %d: %s", c.Line, detail))
			}

		case *CheckIsErrorStmt:
			val, err := r.ev.EvalExpr(c.Expression, env)
			if c.Negated {
				if err == nil {
					r.recordPass(c.Line, fmt.Sprintf("Line %d: succeeded with %s (ok)", c.Line, FormatValue(val)))
				} else {
					detail := r.buildVarsDetail([]Expr{c.Expression}, fmt.Sprintf("raised %s: %s", errStage(err), errMessage(err)), env)
					r.recordFail(c.Line, detail, fmt.Sprintf("Line %d: %s", c.Line, detail))
				}
			} else {
				if err != nil {
					r.recordPass(c.Line, fmt.Sprintf("Line %d: raised %s (ok)", c.Line, errStage(err)))
				} else {
					detail := r.buildVarsDetail([]Expr{c.Expression}, fmt.Sprintf("succeeded with %s", FormatValue(val)), env)
					r.recordFail(c.Line, detail, fmt.Sprintf("Line %d: %s", c.Line, detail))
				}
			}

		case *CheckErrorBlockStmt:
			val, err := r.ev.EvalExpr(c.Expression, env)
			if err == nil {
				detail := r.buildVarsDetail([]Expr{c.Expression}, fmt.Sprintf("succeeded with %s", FormatValue(val)), env)
				r.recordFail(c.Line, detail, fmt.Sprintf("Line %d: expected error, succeeded with %s", c.Line, FormatValue(val)))
				continue
			}
			errRec := &RecordValue{
				TypeName: "error",
				Fields: map[string]Value{
					"type":    errStage(err),
					"message": errMessage(err),
				},
				Order: []string{"type", "message"},
			}
			errEnv := NewEnv(env)
			errEnv.Define("error", errRec, false)
			r.contextLines = append(r.contextLines, c.Line)
			r.runStmts(c.Body, errEnv)
			r.contextLines = r.contextLines[:len(r.contextLines)-1]
		}
	}
	return true
}

func errStage(err error) string {
	var easeErr *EaseError
	if errors.As(err, &easeErr) && easeErr.Stage != "" {
		return easeErr.Stage
	}
	return "RuntimeError"
}

func errMessage(err error) string {
	var easeErr *EaseError
	if errors.As(err, &easeErr) {
		return easeErr.Message
	}
	return err.Error()
}

func (r *testRunner) condComparisonSuffix(cond Expr, env *Env) string {
	be, ok := cond.(*BinaryExpr)
	if !ok {
		return ""
	}
	switch be.Op {
	case "<", "<=", ">", ">=", "is", "is-not":
		lv, errL := r.ev.EvalExpr(be.Left, env)
		rv, errR := r.ev.EvalExpr(be.Right, env)
		if errL != nil || errR != nil {
			return ""
		}
		op := be.Op
		if op == "is-not" {
			op = "is not"
		}
		return fmt.Sprintf("(%s %s %s)", FormatValue(lv), op, FormatValue(rv))
	}
	return ""
}

func (r *testRunner) buildVarsDetail(exprs []Expr, suffix string, env *Env) string {
	seen := make(map[string]bool)
	var parts []string

	addVar := func(label string, val Value) {
		if seen[label] {
			return
		}
		seen[label] = true
		parts = append(parts, fmt.Sprintf("%s = %s", label, formatDetailValue(val)))
	}

	var walk func(e Expr)
	walk = func(e Expr) {
		if e == nil {
			return
		}
		switch ex := e.(type) {
		case *IdentExpr:
			if r.ev.global != nil {
				if _, isFunc := r.ev.global.LookupFunc(ex.Name); isFunc {
					return
				}
			}
			if _, isBuiltin := builtinArities[ex.Name]; isBuiltin {
				return
			}
			if val, ok := env.Lookup(ex.Name); ok {
				if rv, isRec := val.(*RecordValue); isRec && rv.IsPrototype {
					return
				}
				if _, isUnion := val.(*UnionValue); isUnion {
					return
				}
				addVar(ex.Name, val)
			}
		case *FieldExpr:
			if path, ok := fieldExprPath(ex); ok {
				if val, err := r.ev.EvalExpr(ex, env); err == nil {
					addVar(path, val)
					return
				}
			}
			walk(ex.Target)
		case *IndexExpr:
			walk(ex.Target)
			walk(ex.Index)
		case *UnaryExpr:
			walk(ex.Right)
		case *BinaryExpr:
			walk(ex.Left)
			walk(ex.Right)
		case *CallExpr:
			for _, arg := range ex.Args {
				walk(arg)
			}
		case *WithExpr:
			walk(ex.Base)
			for _, upd := range ex.Updates {
				walk(upd.Value)
			}
		case *WhereExpr:
			walk(ex.Target)
		case *CastExpr:
			walk(ex.Target)
		case *TypeCheckExpr:
			walk(ex.Target)
		case *ListExpr:
			for _, el := range ex.Elements {
				walk(el)
			}
		case *InterpStringExpr:
			for _, p := range ex.Parts {
				if p.IsExpr {
					walk(p.Expr)
				}
			}
		}
	}

	for _, e := range exprs {
		walk(e)
	}

	if itVal, hasIt := env.Lookup("it"); hasIt && !seen["it"] {
		addVar("it", itVal)
	}

	joined := strings.Join(parts, ", ")
	if suffix != "" {
		if joined != "" {
			if strings.HasPrefix(suffix, "(") {
				return joined + " " + suffix
			}
			return joined + " (" + suffix + ")"
		}
		return suffix
	}
	return joined
}

func fieldExprPath(fe *FieldExpr) (string, bool) {
	switch t := fe.Target.(type) {
	case *IdentExpr:
		return t.Name + "." + fe.Field, true
	case *FieldExpr:
		if prefix, ok := fieldExprPath(t); ok {
			return prefix + "." + fe.Field, true
		}
	}
	return "", false
}

func formatDetailValue(v Value) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return FormatValue(v)
}

// FormatFailure renders a failed CheckOutcome with its full enclosing block context chain.
func FormatFailure(r CheckOutcome, fileLabel, source string) string {
	var b strings.Builder
	loc := fmt.Sprintf("line %d", r.Line)
	if fileLabel != "" {
		loc = fmt.Sprintf("%s:%d", fileLabel, r.Line)
	}
	fmt.Fprintf(&b, "FAIL  test %q (%s)\n", r.TestTitle, loc)

	srcLines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	lines := r.ContextLines
	if len(lines) == 0 && r.Line > 0 {
		lines = []int{r.Line}
	}
	if len(lines) == 0 {
		if r.VarsDetail != "" {
			fmt.Fprintf(&b, "      -> %s\n", r.VarsDetail)
		}
		return b.String()
	}

	maxLine := slices.Max(lines)
	width := max(2, len(strconv.Itoa(maxLine)))
	lastIndent := ""

	for i, lineNo := range lines {
		lineText := ""
		if lineNo >= 1 && lineNo <= len(srcLines) {
			lineText = srcLines[lineNo-1]
		}
		indent := leadingWhitespace(lineText)
		if i > 0 && lineNo-lines[i-1] > 1 {
			fmt.Fprintf(&b, "  %*s | %s...\n", width, ".", indent)
		}
		fmt.Fprintf(&b, "  %*d | %s\n", width, lineNo, lineText)
		lastIndent = indent
	}

	if r.VarsDetail != "" {
		fmt.Fprintf(&b, "  %*s | %s-> %s\n", width, "", lastIndent, r.VarsDetail)
	}
	return b.String()
}

func leadingWhitespace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}
