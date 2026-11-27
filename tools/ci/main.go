package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"ease-lang/internal/ease"
)

func main() {
	defaultOut := "ease"
	if runtime.GOOS == "windows" {
		defaultOut = "ease.exe"
	}

	raceFlag := flag.Bool("race", false, "run go test with -race")
	versionFlag := flag.String("version", "dev", "version string embedded via ldflags")
	outFlag := flag.String("out", defaultOut, "output path for built CLI binary")
	vscodeFlag := flag.Bool("vscode", false, "validate and package the VS Code extension (.vsix)")
	flag.Parse()

	root, err := findRepoRoot()
	if err != nil {
		fatalf("locate repo root: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		fatalf("chdir to repo root: %v", err)
	}

	totalSteps := 7
	if *vscodeFlag {
		totalSteps = 8
	}
	step := 0
	banner := func(title string) {
		step++
		fmt.Printf("\n==> [%d/%d] %s\n", step, totalSteps, title)
	}

	// 1. Formatting & Static Analysis
	banner("Formatting (gofmt) & Static Analysis (go vet)")
	unformatted := runCapture("gofmt", "-l", "./cmd", "./internal", "./editors", "./tools", "./tests/benchmark")
	if strings.TrimSpace(unformatted) != "" {
		fatalf("unformatted Go files (run gofmt -w):\n%s", unformatted)
	}
	runStream("go", "vet", "./...")

	// 2. Go Unit & Integration Tests
	banner("Go Unit & Integration Tests")
	testArgs := []string{"test", "-count=1"}
	if *raceFlag {
		testArgs = append(testArgs, "-race")
	}
	testArgs = append(testArgs, "./...")
	runStream("go", testArgs...)

	// 3. Build CLI Binary
	banner("Build Ease CLI (" + *outFlag + ")")
	ldflags := fmt.Sprintf("-s -w -X ease-lang/internal/ease.Version=%s", *versionFlag)
	runStream("go", "build", "-trimpath", "-ldflags", ldflags, "-o", *outFlag, "./cmd/ease")

	absOut, err := filepath.Abs(*outFlag)
	if err != nil {
		fatalf("resolve binary path: %v", err)
	}
	verOut := runCapture(absOut, "version")
	fmt.Print(verOut)
	if !strings.Contains(verOut, *versionFlag) {
		fatalf("expected version output to contain %q, got %q", *versionFlag, verOut)
	}

	// 4. Ease Feature & Conformance Tests
	banner("Ease Feature & Conformance Tests")
	testFiles := trackedEaseFiles("tests")
	if _, err := os.Stat(filepath.Join("tests", "conformance")); err == nil {
		testFiles = append(testFiles, trackedEaseFiles(filepath.Join("tests", "conformance"))...)
	}
	if len(testFiles) == 0 {
		fatalf("no .ease test files found in tests/")
	}
	for _, f := range testFiles {
		fmt.Printf("  ease test %s\n", f)
		runStream(absOut, "test", f)
	}

	// 5. Examples Verification
	banner("Examples Verification")
	onlineOnlyExamples := map[string]bool{
		"examples/051_network.ease": true,
	}
	exampleFiles := trackedEaseFiles("examples")
	if len(exampleFiles) == 0 {
		fatalf("no .ease example files found in examples/")
	}
	for _, f := range exampleFiles {
		if onlineOnlyExamples[f] {
			fmt.Printf("  ease check & ast %s (online example)\n", f)
			runStream(absOut, "check", f)
			_ = runCapture(absOut, "ast", f)
			continue
		}
		fmt.Printf("  ease test %s\n", f)
		runStream(absOut, "test", f)
	}

	headlessRunExamples := []string{
		"examples/00_hello_world.ease",
		"examples/06_window.ease",
		"examples/07_http_server.ease",
		"examples/08_haunted_hotel.ease",
		"examples/09_clinic_simulation.ease",
		"examples/10_game_server.ease",
		"examples/11_chords_and_melodies.ease",
		"examples/12_pacman.ease",
		"examples/13_tetris.ease",
		"examples/fibonacci.ease",
		"examples/showcase.ease",
	}
	for _, f := range headlessRunExamples {
		fmt.Printf("  ease run --ci %s\n", f)
		runStream(absOut, "run", f, "--ci")
	}

	// 6. Packaging Smoke Tests (Standalone & WASM)
	banner("Packaging Smoke Tests (Standalone Binary & WASM Bundle)")
	tmpDir, err := os.MkdirTemp("", "ease-ci-*")
	if err != nil {
		fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	helloName := "hello"
	if runtime.GOOS == "windows" {
		helloName = "hello.exe"
	}
	helloBin := filepath.Join(tmpDir, helloName)
	runStream(absOut, "build", "examples/00_hello_world.ease", "-o", helloBin)
	helloOut := runCapture(helloBin)
	if !strings.Contains(helloOut, "Hallo Welt") {
		fatalf("unexpected standalone executable output: %q", helloOut)
	}

	wasmDir := filepath.Join(tmpDir, "tetris_wasm")
	runStream(absOut, "build", "examples/13_tetris.ease", "--target", "wasm", "-o", wasmDir)
	for _, req := range []string{"index.html", "ease_host.js", "app.wasm"} {
		p := filepath.Join(wasmDir, req)
		if info, err := os.Stat(p); err != nil || info.Size() == 0 {
			fatalf("missing or empty WASM build output: %s", p)
		}
	}
	if _, err := exec.LookPath("node"); err == nil {
		runStream("node", "--check", filepath.Join(wasmDir, "ease_host.js"))
	}

	// 7. llms.txt Integrity Gate
	banner("llms.txt Integrity Gate (100% Keyword Coverage + VERIFIED EXAMPLE)")
	verifyLlmsTxt()

	// 8. Optional VS Code Extension Validation & Packaging
	if *vscodeFlag {
		banner("VS Code Extension Validation & Packaging (.vsix)")
		jsonFiles := []string{
			"editors/vscode/package.json",
			"editors/vscode/language-configuration.json",
			"editors/vscode/syntaxes/ease.tmLanguage.json",
		}
		for _, jf := range jsonFiles {
			data, err := os.ReadFile(jf)
			if err != nil {
				fatalf("read %s: %v", jf, err)
			}
			if !json.Valid(data) {
				fatalf("invalid JSON in %s", jf)
			}
		}
		if _, err := exec.LookPath("node"); err == nil {
			runStream("node", "--check", "editors/vscode/extension.js")
		}
		env := os.Environ()
		if *versionFlag != "dev" {
			env = append(env, "EASE_VERSION="+*versionFlag)
		}
		runStreamWithEnv(env, "go", "run", "./editors/vscode")
	}

	fmt.Println("\nALL CI CHECKS PASSED.")
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "llms.txt")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate go.mod + llms.txt from working directory")
		}
		dir = parent
	}
}

func trackedEaseFiles(dir string) []string {
	wantDir := filepath.ToSlash(filepath.Clean(dir))
	var out []string

	cmd := exec.Command("git", "ls-files", wantDir+"/*.ease")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err == nil {
		for _, line := range strings.Split(strings.ReplaceAll(stdout.String(), "\r\n", "\n"), "\n") {
			p := strings.TrimSpace(line)
			if p == "" {
				continue
			}
			p = filepath.ToSlash(p)
			if filepath.ToSlash(filepath.Dir(p)) == wantDir {
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		matches, err := filepath.Glob(filepath.Join(dir, "*.ease"))
		if err != nil {
			fatalf("glob %s: %v", dir, err)
		}
		for _, m := range matches {
			out = append(out, filepath.ToSlash(m))
		}
	}
	sort.Strings(out)
	return out
}

func verifyLlmsTxt() {
	const maxLlmsChars = 16000
	llmsBytes, err := os.ReadFile("llms.txt")
	if err != nil {
		fatalf("read llms.txt: %v", err)
	}
	llmsText := string(llmsBytes)
	if len(llmsText) >= maxLlmsChars {
		fatalf("llms.txt exceeds character budget: %d chars (must be < %d)", len(llmsText), maxLlmsChars)
	}

	words := make(map[string]bool)
	reWord := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	for _, w := range reWord.FindAllString(llmsText, -1) {
		words[w] = true
	}

	kwKeys := extractMapStringKeys(filepath.Join("internal", "ease", "token.go"), "keywords")
	biKeys := extractMapStringKeys(filepath.Join("internal", "ease", "builtins.go"), "builtinArities")
	internalBuiltins := map[string]bool{
		"http_call": true, "net_serve_http": true, "net_reply_text": true, "net_reply_json": true,
		"net_serve_tcp": true, "net_send": true, "screen_open": true, "screen_present": true,
		"screen_clear": true, "screen_draw": true, "screen_show": true, "rand_roll": true,
		"rand_pick": true, "rand_shuffle": true, "synth_play": true, "synth_stop": true,
		"sys_args": true, "sys_exit": true, "sys_run": true,
	}

	var required []string
	required = append(required, kwKeys...)
	for _, b := range biKeys {
		if !internalBuiltins[b] {
			required = append(required, b)
		}
	}
	sort.Strings(required)

	var missing []string
	for _, r := range required {
		if !words[r] {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		fatalf("llms.txt missing keywords/builtins: %s", strings.Join(missing, ", "))
	}

	exampleSrc, err := extractVerifiedExample(llmsText)
	if err != nil {
		fatalf("llms.txt %v", err)
	}
	rep, err := ease.RunChecks(exampleSrc)
	if err != nil {
		fatalf("llms.txt VERIFIED EXAMPLE failed: %v", err)
	}
	if rep.Failed > 0 {
		fatalf("llms.txt VERIFIED EXAMPLE had %d failed checks", rep.Failed)
	}
	fmt.Printf("  %d/%d chars (< %d); %d/%d keywords/builtins covered; VERIFIED EXAMPLE passed (%d assertions)\n",
		len(llmsText), maxLlmsChars, maxLlmsChars, len(required), len(required), rep.Passed)
}

func extractVerifiedExample(llmsText string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(llmsText, "\r\n", "\n"), "\n")
	reSectionHdr := regexp.MustCompile(`^(#{1,4}\s|[0-9]+\.\s+[A-Z]|\[[A-Z][A-Z0-9 _&/-]+\]|===\s)`)
	startIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(strings.ToUpper(trimmed), "VERIFIED EXAMPLE") &&
			(strings.EqualFold(trimmed, "VERIFIED EXAMPLE") || reSectionHdr.MatchString(trimmed)) {
			startIdx = i + 1
			break
		}
	}
	if startIdx < 0 {
		return "", fmt.Errorf("missing VERIFIED EXAMPLE section")
	}
	for startIdx < len(lines) && strings.TrimSpace(lines[startIdx]) == "" {
		startIdx++
	}
	if startIdx >= len(lines) {
		return "", fmt.Errorf("empty VERIFIED EXAMPLE section")
	}
	var collected []string
	if strings.HasPrefix(strings.TrimSpace(lines[startIdx]), "```") {
		closed := false
		for i := startIdx + 1; i < len(lines); i++ {
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				closed = true
				break
			}
			collected = append(collected, lines[i])
		}
		if !closed {
			return "", fmt.Errorf("unclosed code fence in VERIFIED EXAMPLE section")
		}
	} else {
		for i := startIdx; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if reSectionHdr.MatchString(trimmed) {
				break
			}
			collected = append(collected, lines[i])
		}
	}
	src := strings.TrimSpace(strings.Join(collected, "\n"))
	if src == "" {
		return "", fmt.Errorf("empty VERIFIED EXAMPLE section")
	}
	return src, nil
}

func extractMapStringKeys(goFile, varName string) []string {
	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, goFile, nil, 0)
	if err != nil {
		fatalf("parse %s: %v", goFile, err)
	}
	var keys []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*goast.GenDecl)
		if !ok {
			continue
		}
		for _, sp := range gd.Specs {
			vs, ok := sp.(*goast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name.Name != varName || i >= len(vs.Values) {
					continue
				}
				cl, ok := vs.Values[i].(*goast.CompositeLit)
				if !ok {
					continue
				}
				for _, elt := range cl.Elts {
					kv, ok := elt.(*goast.KeyValueExpr)
					if !ok {
						continue
					}
					bl, ok := kv.Key.(*goast.BasicLit)
					if ok && bl.Kind == token.STRING {
						if unq, err := strconv.Unquote(bl.Value); err == nil {
							keys = append(keys, unq)
						}
					}
				}
			}
		}
	}
	return keys
}

func runStream(name string, args ...string) {
	runStreamWithEnv(nil, name, args...)
}

func runStreamWithEnv(env []string, name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if env != nil {
		cmd.Env = env
	}
	if err := cmd.Run(); err != nil {
		fatalf("command failed (%s %s): %v", name, strings.Join(args, " "), err)
	}
}

func runCapture(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		fatalf("command failed (%s %s): %v\n%s", name, strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ci error: "+format+"\n", args...)
	os.Exit(1)
}
