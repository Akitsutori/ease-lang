package main

import (
	"bufio"
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

func cmdRun(args []string) int {
	file := ""
	runCheckFirst := false
	traceMode := false
	for _, a := range args {
		switch a {
		case "--check", "-c":
			runCheckFirst = true
		case "--trace", "-v":
			traceMode = true
		default:
			if file == "" && !strings.HasPrefix(a, "-") {
				file = a
			}
		}
	}
	if file == "" {
		fmt.Fprintln(os.Stderr, "Usage: ease run <file.ease> [--check] [--trace]")
		return 2
	}

	src, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot read %q: %v\n", file, err)
		return 1
	}
	if runCheckFirst {
		if code := executeChecks(string(src), file, traceMode); code != 0 {
			return code
		}
		fmt.Println("---")
	}
	var traceOut *os.File
	if traceMode {
		traceOut = os.Stderr
	}
	var errEval error
	if runCheckFirst {
		_, errEval = ease.EvalProgramHeadlessWithTrace(string(src), os.Stdin, os.Stdout, traceOut)
	} else {
		_, errEval = ease.EvalProgramWithTrace(string(src), os.Stdin, os.Stdout, traceOut)
	}
	if errEval != nil {
		fmt.Fprintln(os.Stderr, errEval)
		return 1
	}
	return 0
}

func cmdTest(args []string) int {
	file := ""
	traceMode := false
	for _, a := range args {
		switch a {
		case "--trace", "-v":
			traceMode = true
		default:
			if file == "" && !strings.HasPrefix(a, "-") {
				file = a
			}
		}
	}
	if file == "" {
		fmt.Fprintln(os.Stderr, "Usage: ease test <file.ease> [--trace]")
		return 2
	}
	src, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot read %q: %v\n", file, err)
		return 1
	}
	return executeChecks(string(src), file, traceMode)
}

func executeChecks(source, label string, traceMode bool) int {
	var report *ease.CheckReport
	var err error
	if traceMode {
		report, err = ease.RunChecksWithTrace(source, os.Stderr)
	} else {
		report, err = ease.RunChecks(source)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, r := range report.Results {
		tag := "PASS"
		if !r.Passed {
			tag = "FAIL"
		}
		fmt.Printf("[%s] (%s) %s\n", tag, r.Scope, r.Message)
	}
	fmt.Printf("Ease Check Summary (%s): %d/%d passed, %d failed\n", label, report.Passed, report.Total, report.Failed)
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
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: ease ast <file.ease>")
		return 2
	}
	src, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot read %q: %v\n", args[0], err)
		return 1
	}
	prog, err := ease.ParseProgram(string(src))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Program AST (%d defs, %d functions, %d check blocks, %d top-level statements)\n",
		len(prog.Defs), len(prog.Functions), len(prog.Checks), len(prog.Body))
	for _, d := range prog.Defs {
		names := make([]string, len(d.Fields))
		for i, f := range d.Fields {
			names[i] = f.Name
		}
		fmt.Printf("  - def %s (%s)\n", d.Name, strings.Join(names, ", "))
	}
	for _, fn := range prog.Functions {
		fmt.Printf("  - to %s %s (body=%d stmts)\n",
			fn.Name, strings.Join(fn.Params, ", "), len(fn.Body))
	}
	for _, blk := range prog.Checks {
		fmt.Printf("  - check %q (%d items)\n", blk.Title, len(blk.Body))
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
			chunkOnly, chunkErr := ease.ParseProgram(chunk)
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
	fmt.Println("  ease run <file.ease> [--check] [--trace]  Run an Ease program (with optional tests/trace)")
	fmt.Println("  ease test <file.ease> [--trace]           Run all 'test' and 'check' assertions")
	fmt.Println("  ease build <file.ease> [-o <out>]         Compile an Ease program into a standalone binary")
	fmt.Println("  ease ast <file.ease>                      Inspect parsed defs, functions, checks, and statements")
	fmt.Println("  ease tokens <file.ease>                   Inspect lexer tokens with line/column positions")
	fmt.Println("  ease repl                                 Start interactive Ease REPL")
	fmt.Println("  ease lsp                                  Start Language Server (stdio JSON-RPC for editors)")
	fmt.Println("  ease version                              Print Ease CLI version")
}
