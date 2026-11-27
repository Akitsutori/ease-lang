package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	"sort"
	"strconv"
	"strings"
	"time"

	"ease-lang/internal/ease"
)

const maxLlmsChars = 16000

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
	labelFlag := flag.String("label", "", "optional run label recorded in history.md")
	trialsFlag := flag.Int("trials", 1, "number of independent trials per assignment (1 reads submissions/<arena>.*, >1 reads submissions/trial_<t>/<arena>.*)")
	auditOnlyFlag := flag.Bool("audit-only", false, "audit llms.txt only (char budget, keyword coverage, VERIFIED EXAMPLE) and exit")
	flag.Parse()

	trials := *trialsFlag
	if trials < 1 {
		trials = 1
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	easeExe := filepath.Join(root, "ease.exe")
	if _, err := os.Stat(easeExe); err != nil {
		easeExe = "ease"
	}

	covReport, missingWords, snippetErr, llmsSHA, llmsLines, llmsChars := auditLlmsTxt(root)
	if *auditOnlyFlag {
		budgetStatus := "PASS"
		if llmsChars >= maxLlmsChars {
			budgetStatus = "FAIL"
		}
		fmt.Printf("# Ease `llms.txt` Audit (`llms_sha=%s` | `lines=%d` | `chars=%d`)\n\n", llmsSHA, llmsLines, llmsChars)
		fmt.Printf("- Character budget (< %d): **%s** (%d / %d chars)\n", maxLlmsChars, budgetStatus, llmsChars, maxLlmsChars)
		fmt.Println(covReport)
		if len(missingWords) > 0 {
			fmt.Printf("- Missing keywords/builtins: `%s`\n", strings.Join(missingWords, "`, `"))
		} else {
			fmt.Println("- Missing keywords/builtins: none (100% covered)")
		}
		if snippetErr != nil {
			fmt.Printf("- Embedded `VERIFIED EXAMPLE` check: **FAIL** (`%v`)\n", snippetErr)
		} else {
			fmt.Println("- Embedded `VERIFIED EXAMPLE` check: **PASS**")
		}
		if llmsChars >= maxLlmsChars || len(missingWords) > 0 || snippetErr != nil {
			os.Exit(1)
		}
		return
	}

	hiddenRoot := filepath.Join(root, "tests", "benchmark", "hidden")
	subRoot := filepath.Join(root, "tests", "benchmark", "submissions")
	entries, err := os.ReadDir(hiddenRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "readdir hidden: %v\n", err)
		os.Exit(1)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	trialDirs := make([]string, trials)
	if trials == 1 {
		trialDirs[0] = subRoot
	} else {
		for t := 0; t < trials; t++ {
			trialDirs[t] = filepath.Join(subRoot, fmt.Sprintf("trial_%d", t+1))
		}
	}

	type arenaSummary struct {
		Name           string
		CheckPassCount int
		TestPassCount  int
		Stability      string
		AvgIdiom       int
		MaxNest        int
		Notes          string
	}

	var arenaSummaries []arenaSummary
	totalArenaRuns := 0
	checksPassed := 0
	testsPassed := 0
	idiomSumAll := 0
	passAllK := 0
	flakyCount := 0

	traceScore, traceTotal := 0, 0
	var tracePerTrial []string
	var traceNotes []string
	qPass := make(map[string]int)
	qSeen := make(map[string]bool)

	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		hiddenDir := filepath.Join(hiddenRoot, ent.Name())
		if ent.Name() == "05_reasoning_trace" {
			for t, dir := range trialDirs {
				sc, tot, qMap, notes := gradeReasoningTrace(hiddenDir, filepath.Join(dir, ent.Name()+".json"))
				traceScore += sc
				traceTotal += tot
				tracePerTrial = append(tracePerTrial, fmt.Sprintf("T%d=%d/%d", t+1, sc, tot))
				for qk, ok := range qMap {
					qSeen[qk] = true
					if ok {
						qPass[qk]++
					}
				}
				if len(notes) > 0 {
					if trials == 1 {
						traceNotes = append(traceNotes, notes...)
					} else {
						traceNotes = append(traceNotes, fmt.Sprintf("T%d(%s)", t+1, strings.Join(notes, "; ")))
					}
				}
			}
			continue
		}

		chkCount, tstCount, idiomSum, maxNest := 0, 0, 0, 0
		trialNotes := make([]string, 0, trials)
		allCleanOK := true
		for t, dir := range trialDirs {
			rep := gradeCodingArena(easeExe, hiddenDir, filepath.Join(dir, ent.Name()+".ease"), ent.Name())
			totalArenaRuns++
			idiomSumAll += rep.IdiomScore
			idiomSum += rep.IdiomScore
			if rep.MaxNest > maxNest {
				maxNest = rep.MaxNest
			}
			if rep.CheckPass {
				chkCount++
				checksPassed++
			}
			if rep.TestPass {
				tstCount++
				testsPassed++
			}
			tNote := strings.Join(rep.Notes, "; ")
			if rep.ErrorMsg != "" {
				if tNote != "" {
					tNote += " | "
				}
				tNote += strings.ReplaceAll(strings.Split(rep.ErrorMsg, "\n")[0], "|", "/")
			}
			if trials == 1 {
				if tNote == "" {
					tNote = "ok"
				}
				trialNotes = append(trialNotes, tNote)
			} else {
				if !rep.TestPass || tNote != "" {
					allCleanOK = false
					if tNote == "" {
						tNote = "failed"
					}
					trialNotes = append(trialNotes, fmt.Sprintf("T%d: %s", t+1, tNote))
				} else {
					trialNotes = append(trialNotes, fmt.Sprintf("T%d: ok", t+1))
				}
			}
		}

		stability := "FLAKY"
		if tstCount == trials {
			stability = "STABLE PASS"
			passAllK++
		} else if tstCount == 0 {
			stability = "STABLE FAIL"
		} else {
			flakyCount++
		}

		notesStr := strings.Join(trialNotes, "; ")
		if trials > 1 && allCleanOK {
			notesStr = "all ok"
		}

		arenaSummaries = append(arenaSummaries, arenaSummary{
			Name:           ent.Name(),
			CheckPassCount: chkCount,
			TestPassCount:  tstCount,
			Stability:      stability,
			AvgIdiom:       idiomSum / trials,
			MaxNest:        maxNest,
			Notes:          notesStr,
		})
	}

	qKeys := make([]string, 0, len(qSeen))
	for k := range qSeen {
		qKeys = append(qKeys, k)
	}
	sort.Strings(qKeys)
	traceAllK := 0
	var tracePerQ []string
	for _, k := range qKeys {
		p := qPass[k]
		if p == trials {
			traceAllK++
			tracePerQ = append(tracePerQ, fmt.Sprintf("%s=%d/%d", k, p, trials))
		} else if p == 0 {
			tracePerQ = append(tracePerQ, fmt.Sprintf("%s=0/%d (FAIL)", k, trials))
		} else {
			tracePerQ = append(tracePerQ, fmt.Sprintf("%s=%d/%d (FLAKY)", k, p, trials))
		}
	}

	now := time.Now()
	labelVal := strings.TrimSpace(*labelFlag)
	if labelVal == "" {
		labelVal = "-"
	}

	var md strings.Builder
	md.WriteString(fmt.Sprintf("# Ease `llms.txt` Benchmark Scorecard (%s)\n\n", now.Format("2006-01-02 15:04:05")))
	md.WriteString(fmt.Sprintf("- Run metadata: `llms_sha=%s` | `label=%s` | `lines=%d` | `chars=%d` | `trials=%d`\n\n", llmsSHA, labelVal, llmsLines, llmsChars, trials))
	md.WriteString("## 1. `llms.txt` Specification Audit\n\n")
	budgetStatus := "PASS"
	if llmsChars >= maxLlmsChars {
		budgetStatus = "FAIL"
	}
	md.WriteString(fmt.Sprintf("- Character budget (< %d): **%s** (%d / %d chars)\n", maxLlmsChars, budgetStatus, llmsChars, maxLlmsChars))
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
	md.WriteString("| Arena | Check@1 | Pass@1 | Stability | IdiomScore | MaxNest | Notes |\n")
	md.WriteString("|---|---|---|---|---|---|---|\n")
	for _, r := range arenaSummaries {
		md.WriteString(fmt.Sprintf("| %s | %d/%d | %d/%d | %s | %d%% | %d | %s |\n",
			r.Name, r.CheckPassCount, trials, r.TestPassCount, trials, r.Stability, r.AvgIdiom, r.MaxNest, r.Notes))
	}

	avgIdiom := 0
	if totalArenaRuns > 0 {
		avgIdiom = idiomSumAll / totalArenaRuns
	}
	numQ := len(qKeys)
	if numQ == 0 {
		numQ = 5
	}
	md.WriteString("\n## 3. Reasoning Trace (`05_reasoning_trace`)\n\n")
	md.WriteString(fmt.Sprintf("- Accuracy: **%d / %d** (Pass^%d: %d/%d)\n", traceScore, traceTotal, trials, traceAllK, numQ))
	if len(tracePerTrial) > 0 {
		md.WriteString("- Per-trial: " + strings.Join(tracePerTrial, ", ") + "\n")
	}
	if len(tracePerQ) > 0 {
		md.WriteString("- Per-question: " + strings.Join(tracePerQ, ", ") + "\n")
	}
	if len(traceNotes) > 0 {
		md.WriteString("- Failure details: " + strings.Join(traceNotes, "; ") + "\n")
	}
	md.WriteString("\n## Summary\n\n")
	md.WriteString(fmt.Sprintf("- `Check@1`: %d/%d | `Pass@1`: %d/%d | `Pass^%d`: %d/%d | Flaky: %d/%d | Avg `IdiomScore`: %d%% | Trace: %d/%d (`Pass^%d`: %d/%d)\n",
		checksPassed, totalArenaRuns, testsPassed, totalArenaRuns, trials, passAllK, len(arenaSummaries), flakyCount, len(arenaSummaries), avgIdiom, traceScore, traceTotal, trials, traceAllK, numQ))

	out := md.String()
	fmt.Print(out)

	resDir := filepath.Join(root, "tests", "benchmark", "results")
	runsDir := filepath.Join(resDir, "runs")
	candsDir := filepath.Join(resDir, "candidates")
	_ = os.MkdirAll(runsDir, 0755)
	_ = os.MkdirAll(candsDir, 0755)
	_ = os.WriteFile(filepath.Join(resDir, "latest.md"), []byte(out), 0644)

	if llmsSHA != "error" {
		candPath := filepath.Join(candsDir, llmsSHA+".txt")
		if _, statErr := os.Stat(candPath); os.IsNotExist(statErr) {
			if rawLlms, rErr := os.ReadFile(filepath.Join(root, "llms.txt")); rErr == nil {
				_ = os.WriteFile(candPath, rawLlms, 0644)
			}
		}
	}

	runFile := fmt.Sprintf("%s_%s_k%d", now.Format("20060102-150405"), llmsSHA, trials)
	if labelVal != "-" {
		safeLabel := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(labelVal, "-")
		safeLabel = strings.Trim(safeLabel, "-")
		if safeLabel != "" {
			runFile += "_" + safeLabel
		}
	}
	_ = os.WriteFile(filepath.Join(runsDir, runFile+".md"), []byte(out), 0644)

	histPath := filepath.Join(resDir, "history.md")
	histBytes, hErr := os.ReadFile(histPath)
	var hb strings.Builder
	if hErr != nil || len(bytes.TrimSpace(histBytes)) == 0 {
		hb.WriteString("# Ease `llms.txt` Benchmark History\n\n")
		hb.WriteString("| Date | llms_sha | Chars | K | Label | Check@1 | Pass@1 | Pass^K | Flaky | Idiom | Trace |\n")
		hb.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
	} else {
		hb.Write(histBytes)
		if !strings.HasSuffix(string(histBytes), "\n") {
			hb.WriteString("\n")
		}
	}
	hb.WriteString(fmt.Sprintf("| %s | `%s` | %d | %d | %s | %d/%d | %d/%d | %d/%d | %d/%d | %d%% | %d/%d |\n",
		now.Format("2006-01-02 15:04"), llmsSHA, llmsChars, trials, strings.ReplaceAll(labelVal, "|", "/"),
		checksPassed, totalArenaRuns, testsPassed, totalArenaRuns, passAllK, len(arenaSummaries), flakyCount, len(arenaSummaries), avgIdiom, traceScore, traceTotal))
	_ = os.WriteFile(histPath, []byte(hb.String()), 0644)

	if llmsChars >= maxLlmsChars {
		fmt.Fprintf(os.Stderr, "benchmark error: llms.txt exceeds character budget: %d chars (must be < %d)\n", llmsChars, maxLlmsChars)
		os.Exit(1)
	}
}

func auditLlmsTxt(root string) (string, []string, error, string, int, int) {
	llmsBytes, err := os.ReadFile(filepath.Join(root, "llms.txt"))
	if err != nil {
		return fmt.Sprintf("- Read error: %v", err), nil, err, "error", 0, 0
	}
	sum := sha256.Sum256(llmsBytes)
	shaShort := hex.EncodeToString(sum[:])[:8]
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
	exampleSrc, snippetErr := extractVerifiedExample(llmsText)
	if snippetErr == nil {
		rep, rErr := ease.RunChecks(exampleSrc)
		if rErr != nil {
			snippetErr = rErr
		} else if rep.Failed > 0 {
			snippetErr = fmt.Errorf("%d failed checks in VERIFIED EXAMPLE", rep.Failed)
		}
	}
	return summary, missing, snippetErr, shaShort, linesCount, len(llmsText)
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

func gradeCodingArena(easeExe, hiddenDir, subPath, name string) arenaReport {
	rep := arenaReport{Name: name}
	subBytes, err := os.ReadFile(subPath)
	if err != nil {
		rep.Notes = append(rep.Notes, "missing "+filepath.Base(subPath))
		return rep
	}
	subSrc := strings.TrimSpace(string(subBytes))

	var rb rubric
	if rbBytes, err := os.ReadFile(filepath.Join(hiddenDir, "rubric.json")); err == nil {
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
	hiddenBytes, err := os.ReadFile(filepath.Join(hiddenDir, "hidden_test.ease"))
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

func gradeReasoningTrace(hiddenDir, subPath string) (int, int, map[string]bool, []string) {
	qPass := make(map[string]bool)
	expBytes, err := os.ReadFile(filepath.Join(hiddenDir, "expected.json"))
	if err != nil {
		return 0, 5, qPass, []string{"missing expected.json"}
	}
	var exp map[string]any
	if err := json.Unmarshal(expBytes, &exp); err != nil {
		return 0, 5, qPass, []string{"bad expected.json"}
	}
	keys := make([]string, 0, len(exp))
	for k := range exp {
		keys = append(keys, k)
		qPass[k] = false
	}
	sort.Strings(keys)

	subBytes, err := os.ReadFile(subPath)
	if err != nil {
		return 0, len(keys), qPass, []string{"missing " + filepath.Base(subPath)}
	}
	var sub map[string]any
	if err := json.Unmarshal(subBytes, &sub); err != nil {
		return 0, len(keys), qPass, []string{fmt.Sprintf("invalid submission.json: %v", err)}
	}
	passed := 0
	var notes []string
	for _, k := range keys {
		ev := fmt.Sprintf("%v", exp[k])
		sv := fmt.Sprintf("%v", sub[k])
		if ev == sv {
			passed++
			qPass[k] = true
		} else {
			notes = append(notes, fmt.Sprintf("%s: want %q got %q", k, ev, sv))
		}
	}
	return passed, len(keys), qPass, notes
}
