package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"ease-lang/internal/ease"
)

func main() {
	runtime.LockOSThread()

	mainWorkCh := make(chan func())
	exitCh := make(chan int, 1)
	ease.MainThreadDispatch = func(fn func()) {
		mainWorkCh <- fn
	}

	go func() {
		exitCh <- runCLI()
	}()

	for {
		select {
		case fn := <-mainWorkCh:
			if fn != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							fmt.Fprintf(os.Stderr, "InternalError: unexpected panic on main thread: %v\n", r)
						}
					}()
					fn()
				}()
			}
		case code := <-exitCh:
			os.Exit(code)
		}
	}
}

func runCLI() (exitCode int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "InternalError: unexpected panic: %v\n", r)
			exitCode = 1
		}
	}()
	// If this binary carries an embedded `.ease` program, run directly as a standalone app.
	if embeddedSrc, ok, err := ease.ReadEmbeddedSource(""); err == nil && ok {
		return runEmbedded(embeddedSrc, os.Args[1:])
	}

	if len(os.Args) < 2 {
		printUsage()
		return 0
	}

	cmd := os.Args[1]
	switch cmd {
	case "run":
		return cmdRun(os.Args[2:])
	case "check":
		return cmdCheck(os.Args[2:])
	case "test":
		return cmdTest(os.Args[2:])
	case "build":
		return cmdBuild(os.Args[2:])
	case "ast":
		return cmdAst(os.Args[2:])
	case "tokens":
		return cmdTokens(os.Args[2:])
	case "repl":
		return cmdRepl()
	case "lsp":
		return cmdLsp()
	case "version", "--version", "-v":
		fmt.Printf("ease %s\n", ease.Version)
		return 0
	case "help", "--help", "-h":
		printUsage()
		return 0
	case "--check", "-c":
		return cmdRun(os.Args[1:])
	default:
		// Convenience: allow `ease.exe program.ease` directly.
		if strings.HasSuffix(cmd, ".ease") {
			return cmdRun(os.Args[1:])
		}
		fmt.Fprintf(os.Stderr, "Unknown command %q\n\n", cmd)
		printUsage()
		return 2
	}
}

func runEmbedded(source string, args []string) int {
	for _, a := range args {
		if a == "--test" || a == "-t" {
			return executeChecks(source, "<embedded>", false)
		}
	}
	if _, err := ease.EvalProgram(source, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type jsonErrorInfo struct {
	Stage   string `json:"stage"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type jsonErrorResponse struct {
	OK    bool           `json:"ok"`
	File  string         `json:"file"`
	Error *jsonErrorInfo `json:"error"`
}

type jsonCheckResult struct {
	OK         bool   `json:"ok"`
	File       string `json:"file"`
	Defs       int    `json:"defs"`
	Functions  int    `json:"functions"`
	TestBlocks int    `json:"test_blocks"`
}

type jsonTestFailure struct {
	Test         string `json:"test"`
	Line         int    `json:"line"`
	ContextLines []int  `json:"context_lines,omitempty"`
	Vars         string `json:"vars,omitempty"`
	Message      string `json:"message"`
}

type jsonTestResult struct {
	OK         bool              `json:"ok"`
	File       string            `json:"file"`
	Passed     int               `json:"passed"`
	Failed     int               `json:"failed"`
	Total      int               `json:"total"`
	TestBlocks int               `json:"test_blocks"`
	Failures   []jsonTestFailure `json:"failures"`
}

type jsonRunResult struct {
	OK   bool   `json:"ok"`
	File string `json:"file"`
}

func toJSONError(err error) *jsonErrorInfo {
	if err == nil {
		return nil
	}
	var ee *ease.EaseError
	if errors.As(err, &ee) {
		stage := ee.Stage
		if stage == "" {
			stage = "EaseError"
		}
		return &jsonErrorInfo{
			Stage:   stage,
			Line:    ee.Line,
			Col:     ee.Col,
			Message: ee.Message,
			Hint:    ee.Hint,
		}
	}
	return &jsonErrorInfo{
		Stage:   "IOError",
		Message: err.Error(),
	}
}

func writeJSON(out io.Writer, v any) {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func cmdCheck(args []string) int {
	return cmdCheckWithWriters(args, os.Stdout, os.Stderr)
}

func cmdCheckWithWriters(args []string, out, errOut io.Writer) int {
	file := ""
	jsonMode := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonMode = true
		default:
			if file == "" && !strings.HasPrefix(a, "-") {
				file = a
			}
		}
	}
	if file == "" {
		fmt.Fprintln(errOut, "Usage: ease check <file.ease> [--json]")
		return 2
	}

	src, err := os.ReadFile(file)
	if err != nil {
		if jsonMode {
			writeJSON(out, jsonErrorResponse{OK: false, File: file, Error: toJSONError(err)})
		} else {
			fmt.Fprintf(errOut, "Cannot read %q: %v\n", file, err)
		}
		return 1
	}

	prog, err := ease.ParseProgramFromFile(string(src), file)
	if err != nil {
		if jsonMode {
			writeJSON(out, jsonErrorResponse{OK: false, File: file, Error: toJSONError(err)})
		} else {
			fmt.Fprintln(errOut, err)
		}
		return 1
	}

	if jsonMode {
		writeJSON(out, jsonCheckResult{
			OK:         true,
			File:       file,
			Defs:       len(prog.Defs),
			Functions:  len(prog.Functions),
			TestBlocks: len(prog.Checks),
		})
	} else {
		fmt.Fprintf(out, "OK: %s (%d defs, %d functions, %d test blocks)\n",
			file, len(prog.Defs), len(prog.Functions), len(prog.Checks))
	}
	return 0
}

func cmdRun(args []string) int {
	return cmdRunWithWriters(args, os.Stdout, os.Stderr)
}

func cmdRunWithWriters(args []string, out, errOut io.Writer) int {
	file := ""
	runCheckFirst := false
	traceMode := false
	jsonMode := false
	for _, a := range args {
		switch a {
		case "--check", "-c":
			runCheckFirst = true
		case "--trace", "-v":
			traceMode = true
		case "--json":
			jsonMode = true
		default:
			if file == "" && !strings.HasPrefix(a, "-") {
				file = a
			}
		}
	}
	if file == "" {
		fmt.Fprintln(errOut, "Usage: ease run <file.ease> [--json] [--check] [--trace]")
		return 2
	}

	src, err := os.ReadFile(file)
	if err != nil {
		if jsonMode {
			writeJSON(out, jsonErrorResponse{OK: false, File: file, Error: toJSONError(err)})
		} else {
			fmt.Fprintf(errOut, "Cannot read %q: %v\n", file, err)
		}
		return 1
	}
	if runCheckFirst {
		if code := executeChecksWithWriters(string(src), file, traceMode, false, jsonMode, out, errOut); code != 0 {
			return code
		}
		if !jsonMode {
			fmt.Fprintln(out, "---")
		}
	}
	var traceOut io.Writer
	if traceMode {
		traceOut = errOut
	}
	var errEval error
	if runCheckFirst {
		_, errEval = ease.EvalProgramHeadlessWithTraceFromFile(string(src), file, os.Stdin, out, traceOut)
	} else {
		_, errEval = ease.EvalProgramWithTraceFromFile(string(src), file, os.Stdin, out, traceOut)
	}
	if errEval != nil {
		if jsonMode {
			writeJSON(out, jsonErrorResponse{OK: false, File: file, Error: toJSONError(errEval)})
		} else {
			fmt.Fprintln(errOut, errEval)
		}
		return 1
	}
	if jsonMode {
		writeJSON(out, jsonRunResult{OK: true, File: file})
	}
	return 0
}

func cmdTest(args []string) int {
	return cmdTestWithWriters(args, os.Stdout, os.Stderr)
}

func cmdTestWithWriters(args []string, out, errOut io.Writer) int {
	file := ""
	traceMode := false
	verbose := false
	jsonMode := false
	for _, a := range args {
		switch a {
		case "--trace":
			traceMode = true
		case "--verbose", "-v":
			verbose = true
		case "--json":
			jsonMode = true
		default:
			if file == "" && !strings.HasPrefix(a, "-") {
				file = a
			}
		}
	}
	if file == "" {
		fmt.Fprintln(errOut, "Usage: ease test <file.ease> [--json] [--verbose] [--trace]")
		return 2
	}
	src, err := os.ReadFile(file)
	if err != nil {
		if jsonMode {
			writeJSON(out, jsonErrorResponse{OK: false, File: file, Error: toJSONError(err)})
		} else {
			fmt.Fprintf(errOut, "Cannot read %q: %v\n", file, err)
		}
		return 1
	}
	return executeChecksWithWriters(string(src), file, traceMode, verbose, jsonMode, out, errOut)
}

func executeChecks(source, label string, traceMode bool) int {
	return executeChecksWithWriters(source, label, traceMode, false, false, os.Stdout, os.Stderr)
}

func executeChecksWithWriters(source, label string, traceMode, verbose, jsonMode bool, out, errOut io.Writer) int {
	var report *ease.CheckReport
	var err error
	if traceMode {
		report, err = ease.RunChecksWithTraceFromFile(source, label, errOut)
	} else {
		report, err = ease.RunChecksFromFile(source, label)
	}
	if err != nil {
		if jsonMode {
			writeJSON(out, jsonErrorResponse{OK: false, File: label, Error: toJSONError(err)})
		} else {
			fmt.Fprintln(errOut, err)
		}
		return 1
	}
	if jsonMode {
		failures := make([]jsonTestFailure, 0, report.Failed)
		for _, r := range report.Results {
			if !r.Passed {
				failures = append(failures, jsonTestFailure{
					Test:         r.TestTitle,
					Line:         r.Line,
					ContextLines: r.ContextLines,
					Vars:         r.VarsDetail,
					Message:      r.Message,
				})
			}
		}
		writeJSON(out, jsonTestResult{
			OK:         report.Failed == 0,
			File:       label,
			Passed:     report.Passed,
			Failed:     report.Failed,
			Total:      report.Total,
			TestBlocks: report.TestBlocks,
			Failures:   failures,
		})
		if report.Failed > 0 {
			return 1
		}
		return 0
	}
	for _, r := range report.Results {
		if !r.Passed {
			fmt.Fprint(out, ease.FormatFailure(r, label, source))
		} else if verbose {
			fmt.Fprintf(out, "[PASS] (%s) %s\n", r.Scope, r.Message)
		}
	}
	fmt.Fprintf(out, "Summary: %d passed, %d failed (%d assertions in %d test blocks)\n", report.Passed, report.Failed, report.Total, report.TestBlocks)
	if report.Failed > 0 {
		return 1
	}
	return 0
}

func cmdBuild(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: ease build <file.ease> [-o <out>]")
		return 2
	}
	inputFile := args[0]
	outFile := ""
	for i := 1; i < len(args); i++ {
		if (args[i] == "-o" || args[i] == "--out") && i+1 < len(args) {
			outFile = args[i+1]
			i++
		}
	}
	if outFile == "" {
		base := strings.TrimSuffix(filepath.Base(inputFile), filepath.Ext(inputFile))
		if runtime.GOOS == "windows" {
			outFile = base + ".exe"
		} else {
			outFile = base
		}
	}

	src, err := os.ReadFile(inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot read %q: %v\n", inputFile, err)
		return 1
	}
	if err := ease.BuildStandaloneExe("", string(src), outFile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Built standalone executable: %s\n", outFile)
	return 0
}

func cmdAst(args []string) int {
	return cmdAstWithWriters(args, os.Stdout, os.Stderr)
}

func cmdAstWithWriters(args []string, out, errOut io.Writer) int {
	file := ""
	jsonMode := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonMode = true
		default:
			if file == "" && !strings.HasPrefix(a, "-") {
				file = a
			}
		}
	}
	if file == "" {
		fmt.Fprintln(errOut, "Usage: ease ast <file.ease> [--json]")
		return 2
	}
	src, err := os.ReadFile(file)
	if err != nil {
		if jsonMode {
			writeJSON(out, jsonErrorResponse{OK: false, File: file, Error: toJSONError(err)})
		} else {
			fmt.Fprintf(errOut, "Cannot read %q: %v\n", file, err)
		}
		return 1
	}
	prog, err := ease.ParseProgramFromFile(string(src), file)
	if err != nil {
		if jsonMode {
			writeJSON(out, jsonErrorResponse{OK: false, File: file, Error: toJSONError(err)})
		} else {
			fmt.Fprintln(errOut, err)
		}
		return 1
	}
	if jsonMode {
		raw, mErr := json.MarshalIndent(prog, "", "  ")
		if mErr != nil {
			writeJSON(out, jsonErrorResponse{OK: false, File: file, Error: toJSONError(mErr)})
			return 1
		}
		fmt.Fprintln(out, string(raw))
		return 0
	}
	fmt.Fprintf(out, "Program AST (%d defs, %d functions, %d check blocks, %d top-level statements)\n",
		len(prog.Defs), len(prog.Functions), len(prog.Checks), len(prog.Body))
	for _, d := range prog.Defs {
		names := make([]string, len(d.Fields))
		for i, f := range d.Fields {
			names[i] = f.Name
		}
		fmt.Fprintf(out, "  - def %s (%s)\n", d.Name, strings.Join(names, ", "))
	}
	for _, fn := range prog.Functions {
		fmt.Fprintf(out, "  - to %s %s (body=%d stmts)\n",
			fn.Name, strings.Join(fn.Params, ", "), len(fn.Body))
	}
	for _, blk := range prog.Checks {
		fmt.Fprintf(out, "  - check %q (%d items)\n", blk.Title, len(blk.Body))
	}
	return 0
}

func cmdTokens(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: ease tokens <file.ease>")
		return 2
	}
	src, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot read %q: %v\n", args[0], err)
		return 1
	}
	tokens, err := ease.NewLexer(string(src)).Tokenize()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, tok := range tokens {
		fmt.Printf("%3d:%-3d %-15s %q\n", tok.Line, tok.Col, tok.Type, tok.Lexeme)
	}
	return 0
}

func cmdRepl() int {
	return runRepl(os.Stdin, os.Stdout, os.Stderr)
}

func runRepl(in io.Reader, out, errOut io.Writer) int {
	fmt.Fprintf(out, "Ease %s REPL (type 'exit' to quit, ':reset' to cancel block)\n", ease.Version)
	scanner := bufio.NewScanner(in)
	prog := &ease.Program{}
	ev := ease.NewEvaluator("", prog, out)
	defer ev.Close()

	var declsSrc strings.Builder
	var pending strings.Builder

	for {
		if pending.Len() == 0 {
			fmt.Fprint(out, "ease> ")
		} else {
			fmt.Fprint(out, "...   ")
		}
		if !scanner.Scan() {
			break
		}
		rawLine := scanner.Text()
		trimmed := strings.TrimSpace(rawLine)
		if pending.Len() == 0 {
			if trimmed == "" {
				continue
			}
			if trimmed == "exit" || trimmed == "quit" {
				break
			}
		} else if trimmed == ":reset" {
			pending.Reset()
			continue
		}

		if pending.Len() > 0 {
			pending.WriteByte('\n')
		}
		pending.WriteString(rawLine)
		chunk := pending.String()

		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(errOut, "InternalError: unexpected panic: %v\n", r)
					pending.Reset()
				}
			}()
			fullSrc := declsSrc.String()
			if fullSrc != "" {
				fullSrc += "\n" + chunk
			} else {
				fullSrc = chunk
			}
			parsed, err := ease.ParseProgram(fullSrc)
			if err != nil {
				if isIncompleteBlockError(err) {
					return
				}
				fmt.Fprintln(errOut, err)
				pending.Reset()
				return
			}
			pending.Reset()

			if err := ev.RegisterDeclarations(parsed); err != nil {
				fmt.Fprintln(errOut, err)
				return
			}
			knownArities := make(map[string]int, len(parsed.Functions))
			for _, fn := range parsed.Functions {
				knownArities[fn.Name] = len(fn.Params)
			}
			chunkOnly, chunkErr := ease.ParseProgramWithArities(chunk, knownArities)
			if chunkErr == nil && len(chunkOnly.Body) == 0 &&
				(len(chunkOnly.Defs) > 0 || len(chunkOnly.Unions) > 0 || len(chunkOnly.Functions) > 0 || len(chunkOnly.Imports) > 0) {
				if declsSrc.Len() > 0 {
					declsSrc.WriteByte('\n')
				}
				declsSrc.WriteString(chunk)
			}

			if len(parsed.Body) > 0 {
				val, err := ev.ExecTopLevel(parsed.Body)
				if err != nil {
					fmt.Fprintln(errOut, err)
					return
				}
				if val != nil {
					if _, isSay := parsed.Body[len(parsed.Body)-1].(*ease.SayStmt); !isSay {
						fmt.Fprintln(out, ease.FormatValue(val))
					}
				}
			}
		}()
	}
	return 0
}

func isIncompleteBlockError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "missing 'end'") ||
		strings.Contains(msg, "missing a closing 'end'") ||
		strings.Contains(msg, "Expected 'end'")
}

func cmdLsp() int {
	srv := ease.NewLSPServer(os.Stdin, os.Stdout)
	if err := srv.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "LSP server error: %v\n", err)
		return 1
	}
	return 0
}

func printUsage() {
	fmt.Println("Ease Programming Language CLI")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  ease run <file.ease> [--json] [--check] [--trace]    Run an Ease program (with optional tests/trace/JSON)")
	fmt.Println("  ease check <file.ease> [--json]                      Parse and type-check without executing")
	fmt.Println("  ease test <file.ease> [--json] [--verbose] [--trace] Run all 'test' and 'check' assertions")
	fmt.Println("  ease build <file.ease> [-o <out>]                    Compile an Ease program into a standalone binary")
	fmt.Println("  ease ast <file.ease> [--json]                        Inspect parsed defs, functions, checks, and statements")
	fmt.Println("  ease tokens <file.ease>                              Inspect lexer tokens with line/column positions")
	fmt.Println("  ease repl                                            Start interactive Ease REPL")
	fmt.Println("  ease lsp                                             Start Language Server (stdio JSON-RPC for editors)")
	fmt.Println("  ease version                                         Print Ease CLI version")
}
