package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ease-lang/internal/ease"
)

type rubric struct {
	Name                   string   `json:"name"`
	RequireReturnContracts bool     `json:"require_return_contracts"`
	MaxNesting             int      `json:"max_nesting"`
	ForbidLoopAppend       bool     `json:"forbid_loop_append"`
	RequiredConstructs     []string `json:"required_constructs"`
}

type arenaReport struct {
	Name       string
	CheckPass  bool
	TestPass   bool
	IdiomScore int
	MaxNest    int
	Notes      []string
	ErrorMsg   string
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	easeExe := filepath.Join(root, "ease.exe")
	if _, err := os.Stat(easeExe); err != nil {
		easeExe = "ease"
	}

	covReport, missingWords, snippetErr := auditLlmsTxt(root)

	arenasDir := filepath.Join(root, "tests", "benchmark", "arenas")
	entries, err := os.ReadDir(arenasDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "readdir arenas: %v\n", err)
		os.Exit(1)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var reports []arenaReport
	var traceScore, traceTotal int
	var traceNotes []string

	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		dir := filepath.Join(arenasDir, ent.Name())
		if ent.Name() == "05_reasoning_trace" {
			traceScore, traceTotal, traceNotes = gradeReasoningTrace(dir)
			continue
		}
		rep := gradeCodingArena(easeExe, dir, ent.Name())
		reports = append(reports, rep)
	}

	var md strings.Builder
	md.WriteString(fmt.Sprintf("# Ease `llms.txt` Benchmark Scorecard (%s)\n\n", time.Now().Format("2006-01-02 15:04:05")))
	md.WriteString("## 1. `llms.txt` Specification Audit\n\n")
	md.WriteString(covReport + "\n")
	if len(missingWords) > 0 {
		md.WriteString(fmt.Sprintf("- Missing keywords/builtins: `%s`\n", strings.Join(missingWords, "`, `")))
	} else {
		md.WriteString("- Missing keywords/builtins: none (100% covered)\n")
	}
	if snippetErr != nil {
		md.WriteString(fmt.Sprintf("- Embedded `VERIFIED EXAMPLE` check: **FAIL** (`%v`)\n\n", snippetErr))
	} else {
		md.WriteString("- Embedded `VERIFIED EXAMPLE` check: **PASS**\n\n")
	}

	md.WriteString("## 2. Coding Arenas (`01`–`04`)\n\n")
	md.WriteString("| Arena | Check@1 | Pass@1 | IdiomScore | MaxNest | Notes |\n")
	md.WriteString("|---|---|---|---|---|---|\n")

	checksPassed, testsPassed, idiomSum := 0, 0, 0
	for _, r := range reports {
		chk := "FAIL"
		if r.CheckPass {
			chk = "PASS"
			checksPassed++
		}
		tst := "FAIL"
		if r.TestPass {
			tst = "PASS"
			testsPassed++
		}
		idiomSum += r.IdiomScore
		note := strings.Join(r.Notes, "; ")
		if r.ErrorMsg != "" {
			if note != "" {
				note += " | "
			}
			note += strings.ReplaceAll(strings.Split(r.ErrorMsg, "\n")[0], "|", "/")
		}
		if note == "" {
			note = "ok"
		}
		md.WriteString(fmt.Sprintf("| %s | %s | %s | %d%% | %d | %s |\n", r.Name, chk, tst, r.IdiomScore, r.MaxNest, note))
	}

	avgIdiom := 0
	if len(reports) > 0 {
		avgIdiom = idiomSum / len(reports)
	}
	md.WriteString("\n## 3. Reasoning Trace (`05_reasoning_trace`)\n\n")
	md.WriteString(fmt.Sprintf("- Accuracy: **%d / %d**\n", traceScore, traceTotal))
	if len(traceNotes) > 0 {
		md.WriteString("- Details: " + strings.Join(traceNotes, "; ") + "\n")
	}
	md.WriteString("\n## Summary\n\n")
	md.WriteString(fmt.Sprintf("- `Check@1`: %d/%d | `Pass@1`: %d/%d | Avg `IdiomScore`: %d%% | Trace: %d/%d\n",
		checksPassed, len(reports), testsPassed, len(reports), avgIdiom, traceScore, traceTotal))

	out := md.String()
	fmt.Print(out)

	resDir := filepath.Join(root, "tests", "benchmark", "results")
	_ = os.MkdirAll(resDir, 0755)
	_ = os.WriteFile(filepath.Join(resDir, "latest.md"), []byte(out), 0644)
}

func auditLlmsTxt(root string) (string, []string, error) {
	llmsBytes, err := os.ReadFile(filepath.Join(root, "llms.txt"))
	if err != nil {
		return fmt.Sprintf("- Read error: %v", err), nil, err
	}
	llmsText := string(llmsBytes)
	words := make(map[string]bool)
	reWord := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	for _, w := range reWord.FindAllString(llmsText, -1) {
		words[w] = true
	}

	kwKeys := extractMapStringKeys(filepath.Join(root, "internal", "ease", "token.go"), "keywords")
	biKeys := extractMapStringKeys(filepath.Join(root, "internal", "ease", "builtins.go"), "builtinArities")

	internalBuiltins := map[string]bool{
		"http_call": true, "net_serve_http": true, "net_reply_text": true, "net_reply_json": true,
		"net_serve_tcp": true, "net_send": true, "screen_open": true, "screen_present": true,
		"screen_clear": true, "screen_draw": true, "screen_show": true, "rand_roll": true,
		"rand_pick": true, "rand_shuffle": true, "synth_play": true, "synth_stop": true,
		"sys_args": true, "sys_exit": true, "sys_run": true,
	}

	var required []string
	for _, k := range kwKeys {
		required = append(required, k)
	}
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
	covered := len(required) - len(missing)
	linesCount := strings.Count(llmsText, "\n") + 1
	summary := fmt.Sprintf("- Size: %d lines, ~%d chars | Keyword & builtin coverage: **%d/%d**", linesCount, len(llmsText), covered, len(required))

	// Run embedded VERIFIED EXAMPLE block.
	var snippetErr error
	const marker = "3. VERIFIED EXAMPLE\n"
	const endMarker = "\n4. BUILT-IN VERBS"
	if idx := strings.Index(llmsText, marker); idx >= 0 {
		rest := llmsText[idx+len(marker):]
		if endIdx := strings.Index(rest, endMarker); endIdx >= 0 {
			exampleSrc := strings.TrimSpace(rest[:endIdx])
			rep, rErr := ease.RunChecks(exampleSrc)
			if rErr != nil {
				snippetErr = rErr
			} else if rep.Failed > 0 {
				snippetErr = fmt.Errorf("%d failed checks in VERIFIED EXAMPLE", rep.Failed)
			}
		}
	}
	return summary, missing, snippetErr
}

func extractMapStringKeys(goFile, varName string) []string {
	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, goFile, nil, 0)
	if err != nil {
		return nil
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

func gradeCodingArena(easeExe, dir, name string) arenaReport {
	rep := arenaReport{Name: name}
	subPath := filepath.Join(dir, "submission.ease")
	subBytes, err := os.ReadFile(subPath)
	if err != nil {
		rep.Notes = append(rep.Notes, "missing submission.ease")
		return rep
	}
	subSrc := strings.TrimSpace(string(subBytes))

	var rb rubric
	if rbBytes, err := os.ReadFile(filepath.Join(dir, "rubric.json")); err == nil {
		_ = json.Unmarshal(rbBytes, &rb)
	}

	// 1. Check@1
	cmdChk := exec.Command(easeExe, "check", subPath)
	var chkOut bytes.Buffer
	cmdChk.Stdout = &chkOut
	cmdChk.Stderr = &chkOut
	if err := cmdChk.Run(); err != nil {
		rep.ErrorMsg = strings.TrimSpace(chkOut.String())
	} else {
		rep.CheckPass = true
	}

	// 2. Pass@1 with hidden_test.ease
	hiddenBytes, err := os.ReadFile(filepath.Join(dir, "hidden_test.ease"))
	if err == nil && rep.CheckPass {
		tmpPath := filepath.Join(os.TempDir(), fmt.Sprintf("ease_bench_%s_%d.ease", name, time.Now().UnixNano()))
		combined := subSrc + "\n\n" + string(hiddenBytes) + "\n"
		if wErr := os.WriteFile(tmpPath, []byte(combined), 0644); wErr == nil {
			defer os.Remove(tmpPath)
			cmdTst := exec.Command(easeExe, "test", tmpPath)
			var tstOut bytes.Buffer
			cmdTst.Stdout = &tstOut
			cmdTst.Stderr = &tstOut
			if err := cmdTst.Run(); err != nil {
				rep.ErrorMsg = strings.TrimSpace(tstOut.String())
			} else {
				rep.TestPass = true
			}
		}
	}

	// 3. AST Idiom Score
	prog, parseErr := ease.ParseProgram(subSrc)
	if parseErr == nil && prog != nil {
		score, maxNest, notes := scoreProgramAST(prog, rb)
		rep.IdiomScore = score
		rep.MaxNest = maxNest
		rep.Notes = append(rep.Notes, notes...)
	}
	return rep
}

func scoreProgramAST(prog *ease.Program, rb rubric) (int, int, []string) {
	score := 100
	var notes []string
	found := make(map[string]bool)
	if len(prog.Unions) > 0 {
		found["union"] = true
	}
	for _, st := range prog.Body {
		if _, ok := st.(*ease.ConstDeclStmt); ok {
			found["const"] = true
		}
	}

	missingRetContracts := 0
	maxNest := 0
	loopAppend := false
	pseudoBool := false

	var walkStmts func(stmts []ease.Stmt, depth int)
	var walkExpr func(e ease.Expr)

	walkExpr = func(e ease.Expr) {
		if e == nil {
			return
		}
		switch ex := e.(type) {
		case *ease.WhereExpr:
			found["where"] = true
			walkExpr(ex.Target)
			for _, c := range ex.Conditions {
				walkExpr(c)
			}
		case *ease.ProjectExpr:
			found["project"] = true
			walkExpr(ex.Source)
			walkExpr(ex.Body)
		case *ease.UpdateExpr:
			found["update"] = true
			walkExpr(ex.Target)
			walkExpr(ex.Where)
			for _, u := range ex.Updates {
				walkExpr(u.Value)
			}
		case *ease.SortByExpr:
			found["sort_by"] = true
			walkExpr(ex.Target)
			for _, k := range ex.Keys {
				walkExpr(k)
			}
		case *ease.QuantifierExpr:
			found["quantifier"] = true
			walkExpr(ex.Source)
			walkExpr(ex.Cond)
		case *ease.MatchExpr:
			found["match"] = true
			walkExpr(ex.Target)
			for _, arm := range ex.Arms {
				walkExpr(arm.Value)
			}
			walkExpr(ex.ElseValue)
			walkStmts(ex.ElseBody, 1)
		case *ease.CastExpr:
			if ex.Fallback != nil {
				found["cast_fallback"] = true
				walkExpr(ex.Fallback)
			}
			walkExpr(ex.Target)
		case *ease.SwarmLoopExpr:
			found["swarm"] = true
			walkExpr(ex.Iterable)
			walkStmts(ex.Body, 1)
		case *ease.InboxExpr:
			found["inbox"] = true
		case *ease.BinaryExpr:
			if ex.Op == "is" || ex.Op == "is not" {
				if num, ok := ex.Right.(*ease.NumberExpr); ok && (num.Value == 0 || num.Value == 1) {
					if id, isId := ex.Left.(*ease.IdentExpr); isId && (strings.HasPrefix(id.Name, "is_") || strings.HasPrefix(id.Name, "has_") || strings.HasSuffix(id.Name, "_flag")) {
						pseudoBool = true
					}
				}
			}
			walkExpr(ex.Left)
			walkExpr(ex.Right)
		case *ease.UnaryExpr:
			walkExpr(ex.Right)
		case *ease.CallExpr:
			for _, a := range ex.Args {
				walkExpr(a)
			}
		case *ease.WithExpr:
			walkExpr(ex.Base)
			for _, u := range ex.Updates {
				walkExpr(u.Value)
			}
		case *ease.TypedListExpr:
			for _, row := range ex.Rows {
				for _, u := range row {
					walkExpr(u.Value)
				}
			}
		case *ease.ListExpr:
			for _, el := range ex.Elements {
				walkExpr(el)
			}
		case *ease.FieldExpr:
			walkExpr(ex.Target)
		case *ease.IndexExpr:
			walkExpr(ex.Target)
			walkExpr(ex.Index)
		}
	}

	walkStmts = func(stmts []ease.Stmt, depth int) {
		if depth > maxNest {
			maxNest = depth
		}
		for _, s := range stmts {
			switch st := s.(type) {
			case *ease.AssignStmt:
				walkExpr(st.Value)
				if ce, ok := st.Value.(*ease.CallExpr); ok && ce.Callee == "append" && depth > 0 {
					loopAppend = true
				}
			case *ease.ConstDeclStmt:
				walkExpr(st.Value)
			case *ease.ReturnStmt:
				walkExpr(st.Value)
			case *ease.LoopStopStmt, *ease.VoidReturnStmt:
				found["loop_stop_or_void_return"] = true
			case *ease.SpawnStmt:
				found["spawn"] = true
				walkExpr(st.Call)
			case *ease.TakeStmt:
				found["take"] = true
				walkExpr(st.Inbox)
				walkStmts(st.Body, depth+1)
			case *ease.LoopStmt:
				walkExpr(st.Iterable)
				walkStmts(st.Body, depth+1)
			case *ease.CheckGuardStmt:
				walkExpr(st.Cond)
				for _, r := range st.Rules {
					walkExpr(r.Cond)
					if r.Action != nil {
						walkStmts([]ease.Stmt{r.Action}, depth)
					}
				}
				if st.IsEffectBlock {
					walkStmts(st.Body, depth+1)
					walkStmts(st.ElseBody, depth+1)
				}
			case *ease.MatchStmt:
				found["match"] = true
				walkExpr(st.Target)
				for _, arm := range st.Arms {
					if arm.Action != nil {
						walkStmts([]ease.Stmt{arm.Action}, depth+1)
					}
					walkStmts(arm.Body, depth+1)
				}
				walkStmts(st.ElseBody, depth+1)
			case *ease.SayStmt:
				walkExpr(st.Value)
			case *ease.ExprStmt:
				walkExpr(st.Expression)
			}
		}
	}

	walkStmts(prog.Body, 0)
	for _, fn := range prog.Functions {
		if fn.ReturnType != "" {
			found["return_type"] = true
		} else if fnHasValueReturn(fn.Body) {
			missingRetContracts++
		}
		walkStmts(fn.Body, 0)
	}

	if rb.RequireReturnContracts && missingRetContracts > 0 {
		score -= 20
		notes = append(notes, fmt.Sprintf("%d value fn missing 'as <type>'", missingRetContracts))
	}
	if rb.MaxNesting > 0 && maxNest > rb.MaxNesting {
		score -= 15
		notes = append(notes, fmt.Sprintf("nesting %d > %d", maxNest, rb.MaxNesting))
	}
	if rb.ForbidLoopAppend && loopAppend {
		score -= 20
		notes = append(notes, "imperative loop+append")
	}
	if pseudoBool {
		score -= 15
		notes = append(notes, "pseudo-boolean 0/1 flag")
	}
	for _, req := range rb.RequiredConstructs {
		if !found[req] {
			score -= 15
			notes = append(notes, "missing "+req)
		}
	}
	if score < 0 {
		score = 0
	}
	return score, maxNest, notes
}

func fnHasValueReturn(stmts []ease.Stmt) bool {
	for _, s := range stmts {
		switch st := s.(type) {
		case *ease.ReturnStmt:
			return true
		case *ease.CheckGuardStmt:
			for _, r := range st.Rules {
				if r.Action != nil && fnHasValueReturn([]ease.Stmt{r.Action}) {
					return true
				}
			}
			if fnHasValueReturn(st.Body) || fnHasValueReturn(st.ElseBody) {
				return true
			}
		case *ease.LoopStmt:
			if fnHasValueReturn(st.Body) {
				return true
			}
		case *ease.TakeStmt:
			if fnHasValueReturn(st.Body) {
				return true
			}
		case *ease.MatchStmt:
			for _, arm := range st.Arms {
				if (arm.Action != nil && fnHasValueReturn([]ease.Stmt{arm.Action})) || fnHasValueReturn(arm.Body) {
					return true
				}
			}
			if fnHasValueReturn(st.ElseBody) {
				return true
			}
		}
	}
	return false
}

func gradeReasoningTrace(dir string) (int, int, []string) {
	expBytes, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		return 0, 5, []string{"missing expected.json"}
	}
	subBytes, err := os.ReadFile(filepath.Join(dir, "submission.json"))
	if err != nil {
		return 0, 5, []string{"missing submission.json"}
	}
	var exp, sub map[string]any
	if err := json.Unmarshal(expBytes, &exp); err != nil {
		return 0, 5, []string{"bad expected.json"}
	}
	if err := json.Unmarshal(subBytes, &sub); err != nil {
		return 0, len(exp), []string{fmt.Sprintf("invalid submission.json: %v", err)}
	}
	keys := make([]string, 0, len(exp))
	for k := range exp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	passed := 0
	var notes []string
	for _, k := range keys {
		ev := fmt.Sprintf("%v", exp[k])
		sv := fmt.Sprintf("%v", sub[k])
		if ev == sv {
			passed++
		} else {
			notes = append(notes, fmt.Sprintf("%s: want %q got %q", k, ev, sv))
		}
	}
	return passed, len(keys), notes
}
