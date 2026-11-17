package ease

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLexerAndIsNotTokens(t *testing.T) {
	src := "# comment\nto check_eq a b\n  a is not b\nend\n"
	toks, err := NewLexer(src).Tokenize()
	if err != nil {
		t.Fatalf("unexpected lexer error: %v", err)
	}
	foundIs := false
	foundNot := false
	for _, tok := range toks {
		if tok.Type == TOK_IS {
			foundIs = true
		}
		if tok.Type == TOK_NOT {
			foundNot = true
		}
	}
	if !foundIs || !foundNot {
		t.Fatalf("expected TOK_IS and TOK_NOT tokens in %+v", toks)
	}
}

func TestToWhereAndCheckBlockExecution(t *testing.T) {
	src := `
to double x
  x * 2
end

to divide a b
  a / b
end

test "functions"
  do double 4 is 8
  do double 4 is not 9
  do divide 10 2 is 5
  do divide 1 0 raises "Division by zero"
end

test "loops and mutability"
  factor = 3
  acc = 0
  loop range 1 4
    acc = acc + factor
  end
  acc is 12
end

res = do double 7
say "result={res}"
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Total != 5 || report.Passed != 5 || report.Failed != 0 {
		t.Fatalf("unexpected check report: %+v", report)
	}

	var out bytes.Buffer
	if _, err := EvalProgram(src, &out); err != nil {
		t.Fatalf("EvalProgram failed: %v", err)
	}
	if strings.TrimSpace(out.String()) != "result=14" {
		t.Fatalf("unexpected stdout: %q", out.String())
	}
}

func TestCheckFailureAndImmutabilityHint(t *testing.T) {
	badCheck := `
to square n
  n * n
end

test
  do square 3 is 10
end
`
	report, err := RunChecks(badCheck)
	if err != nil {
		t.Fatalf("RunChecks returned syntax error instead of test report: %v", err)
	}
	if report.Failed != 1 {
		t.Fatalf("expected 1 failed check, got %+v", report)
	}

	immutableAssign := `
def speed = 10
speed = 20
`
	_, err = EvalProgram(immutableAssign, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected immutability error when reassigning def constant")
	}
	if !strings.Contains(err.Error(), "Hint:") || !strings.Contains(err.Error(), "def") {
		t.Fatalf("expected beginner-friendly hint mentioning 'def', got: %v", err)
	}
}

func TestFriendlySyntaxHints(t *testing.T) {
	_, err := ParseProgram("to foo a\n  a\n")
	if err == nil {
		t.Fatal("expected syntax error on unclosed 'to' block")
	}
	if !strings.Contains(err.Error(), "end") || !strings.Contains(err.Error(), "Hint:") {
		t.Fatalf("expected helpful hint, got: %v", err)
	}
}

func TestBareAssignment(t *testing.T) {
	src := `
x = 1
y = 0.5
x = x + 2
say "Sum of {x} and {y} is {x+y}"
test "bare assignment in test block"
  z = 10
  z = z + 5
  z is 15
end
`
	var out bytes.Buffer
	if _, err := EvalProgram(src, &out); err != nil {
		t.Fatalf("EvalProgram failed: %v", err)
	}
	want := "Sum of 3 and 0.5 is 3.5\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\ngot:  %q\nwant: %q", out.String(), want)
	}
	report, err := RunChecks(src)
	if err != nil || report.Passed != 1 || report.Failed != 0 {
		t.Fatalf("RunChecks failed: report=%+v err=%v", report, err)
	}
}

func TestStandaloneBundleRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	hostPath := filepath.Join(tmpDir, "host.exe")
	outPath := filepath.Join(tmpDir, "app.exe")
	if err := os.WriteFile(hostPath, []byte("MZ_FAKE_EXE_HEADER"), 0o755); err != nil {
		t.Fatal(err)
	}

	programSrc := "to greet who\n  \"Hi \" + who\nend\ntest\n  do greet \"Ease\" is \"Hi Ease\"\nend\nsay do greet \"Go\"\n"
	if err := BuildStandaloneExe(hostPath, programSrc, outPath); err != nil {
		t.Fatalf("BuildStandaloneExe failed: %v", err)
	}

	extracted, ok, err := ReadEmbeddedSource(outPath)
	if err != nil || !ok {
		t.Fatalf("ReadEmbeddedSource failed: ok=%v err=%v", ok, err)
	}
	if extracted != programSrc {
		t.Fatalf("extracted source mismatch:\ngot:  %q\nwant: %q", extracted, programSrc)
	}

	// Re-bundling from an already bundled binary must strip the old trailer cleanly.
	outPath2 := filepath.Join(tmpDir, "app2.exe")
	if err := BuildStandaloneExe(outPath, "say 42\n", outPath2); err != nil {
		t.Fatalf("second BuildStandaloneExe failed: %v", err)
	}
	extracted2, ok2, err2 := ReadEmbeddedSource(outPath2)
	if err2 != nil || !ok2 || extracted2 != "say 42\n" {
		t.Fatalf("second bundle extraction failed: ok=%v err=%v src=%q", ok2, err2, extracted2)
	}
}

func TestStringInterpolation(t *testing.T) {
	src := `
def x = 1
def y = 0.5
say "Sum of {x} and {y} is {x+y}"
say "Literal \{braces\} and nested {"hi"}"
test "interpolation in assertions"
  "val={x + 2}" is "val=3"
end
`
	var out bytes.Buffer
	if _, err := EvalProgram(src, &out); err != nil {
		t.Fatalf("EvalProgram failed: %v", err)
	}
	want := "Sum of 1 and 0.5 is 1.5\nLiteral {braces} and nested hi\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\ngot:  %q\nwant: %q", out.String(), want)
	}
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 1 || report.Failed != 0 {
		t.Fatalf("expected 1 passed check, got %d passed / %d failed", report.Passed, report.Failed)
	}

	if _, err := ParseProgram("say \"unclosed {x\"\n"); err == nil {
		t.Fatal("expected error for unclosed interpolation")
	}
	if _, err := ParseProgram("say \"empty {}\"\n"); err == nil {
		t.Fatal("expected error for empty interpolation")
	}
}

func TestAskInput(t *testing.T) {
	src := `
name = ask "Wie heißt du?"
city = do ask "Wo wohnst du?"
say "Hallo {name} aus {city}!"
`
	var out bytes.Buffer
	in := strings.NewReader("Ada\nBerlin\n")
	if _, err := EvalProgramWithInput(src, in, &out); err != nil {
		t.Fatalf("EvalProgramWithInput failed: %v", err)
	}
	want := "Wie heißt du? Wo wohnst du? Hallo Ada aus Berlin!\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\ngot:  %q\nwant: %q", out.String(), want)
	}
}

func TestEntryCheckFeatures(t *testing.T) {
	src := `
to allow_entry name age
    check
        name is not text  -> "Name muss Text sein"
        name is ""        -> "Name darf nicht leer sein"
        age is not number -> "Alter muss eine Zahl sein"
        age < 18          -> "Du bist {age}. Das ist jung"
        age > 120         -> "Ungültiges Alter"
    end

    "Willkommen {name}, du darfst rein!"
end

to is_valid_vip code age
    check
        code is text
        code is "VIP2026"
        age is number
        age >= 18
    else
        "invalid"
    end

    "valid"
end

test
    do allow_entry 123 25      is "Name muss Text sein"
    do allow_entry "" 25       is "Name darf nicht leer sein"
    do allow_entry "Max" "abc" is "Alter muss eine Zahl sein"
    do allow_entry "Max" 16    is "Du bist 16. Das ist jung"
    do allow_entry "Max" 150   is "Ungültiges Alter"
    do allow_entry "Max" 25    is "Willkommen Max, du darfst rein!"

    do is_valid_vip "VIP2026" 20 is "valid"
    do is_valid_vip "VIP2026" 16 is "invalid"
    do is_valid_vip "FALSCH" 25  is "invalid"
    do is_valid_vip "VIP2026" "x" is "invalid"
end

user_name = ask "Wie heißt du?"
user_age  = ask "Wie alt bist du?"
check user_age is not number -> say "Bitte beim nächsten Mal eine Zahl eingeben."
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 10 || report.Failed != 0 {
		t.Fatalf("expected 10 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}

	var out bytes.Buffer
	in := strings.NewReader("Max\nabc\n")
	if _, err := EvalProgramWithInput(src, in, &out); err != nil {
		t.Fatalf("EvalProgramWithInput failed: %v", err)
	}
	if !strings.Contains(out.String(), "Bitte beim nächsten Mal eine Zahl eingeben.") {
		t.Fatalf("expected top-level check message in output, got: %q", out.String())
	}
}

func TestDefAndWithRecords(t *testing.T) {
	src := `
def player
    x = 360
    speed = 400
end

greg = player with x = 10
moved = greg with x = 20

boss = player with
    x = 500
    speed = 150
end

say "{greg.x},{greg.speed}|{moved.x},{moved.speed}|{boss.x},{boss.speed}"

test
    greg.x is 10
    greg.speed is 400
    moved.x is 20
    boss.x is 500
    boss.speed is 150
    greg is player
    greg is not moved
end
`
	var out bytes.Buffer
	if _, err := EvalProgram(src, &out); err != nil {
		t.Fatalf("EvalProgram failed: %v", err)
	}
	want := "10,400|20,400|500,150\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\ngot:  %q\nwant: %q", out.String(), want)
	}

	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 7 || report.Failed != 0 {
		t.Fatalf("expected 7 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}

	typoSrc := `
def player
    x = 360
end
bad = player with speeed = 10
`
	if _, err := EvalProgram(typoSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no field") {
		t.Fatalf("expected unknown field error, got: %v", err)
	}
}

func TestIntrospectionTraceAndFields(t *testing.T) {
	src := `
def player
    x = 360
    speed = 400
end
greg = player with x = 10
say do type greg
say do fields greg
name = ask "Name?"
say name
`
	var out, trace bytes.Buffer
	in := strings.NewReader("Ada\n")
	if _, err := EvalProgramWithTrace(src, in, &out, &trace); err != nil {
		t.Fatalf("EvalProgramWithTrace failed: %v", err)
	}
	wantOut := "player\n[\"x\", \"speed\"]\nName? Ada\n"
	if out.String() != wantOut {
		t.Fatalf("output mismatch:\ngot:  %q\nwant: %q", out.String(), wantOut)
	}
	traceStr := trace.String()
	for _, expected := range []string{"[trace] parsed 1 def(s)", "waiting for stdin", "ask received \"Ada\""} {
		if !strings.Contains(traceStr, expected) {
			t.Fatalf("expected trace to contain %q, got:\n%s", expected, traceStr)
		}
	}
}

func TestUnionTypesAndBareCalls(t *testing.T) {
	src := `
def ticket = "vip" | "standard" | "none"

to check_access age pass
    check
        pass is not ticket -> "Ungültiges Ticket"
        age < 18           -> "Abgelehnt: zu jung"
        pass is "none"     -> "Abgelehnt: kein Ticket"
        pass is "vip"      -> "Willkommen in der Lounge"
    end
    "Willkommen im Hauptbereich"
end

test
    check_access 16 "vip" is "Abgelehnt: zu jung"
    check_access 22 "none" is "Abgelehnt: kein Ticket"
    check_access 22 "standard" is "Willkommen im Hauptbereich"
    check_access 30 "vip" is "Willkommen in der Lounge"
    check_access 30 "gold" is "Ungültiges Ticket"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 5 || report.Failed != 0 {
		t.Fatalf("expected 5 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}
}

func TestInlineUnionFieldInDef(t *testing.T) {
	src := `
def player
    name = "Gast"
    x = 0
    hp = 100
    state = "idle" | "running" | "KO"
end

to take_damage p amount
    next_hp = p.hp - amount
    check next_hp <= 0 -> p with hp = 0, state = "KO"
    p with hp = next_hp
end

test
    hero = player with name = "Aria", x = 10
    injured = take_damage hero 35
    defeated = take_damage injured 80

    hero.hp is 100
    injured.hp is 65
    injured.state is "idle"
    defeated.hp is 0
    defeated.state is "KO"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 5 || report.Failed != 0 {
		t.Fatalf("expected 5 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}

	invalidSrc := `
def player
    state = "idle" | "running" | "KO"
end
bad = player with state = "flying"
`
	if _, err := EvalProgram(invalidSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Invalid value") {
		t.Fatalf("expected invalid union field value error, got: %v", err)
	}
}

func TestDefConstantsAndDoActions(t *testing.T) {
	// Variant 1: flat top-level def constants + runtime assignment + ask
	src1 := `
say "Hello World"
def x = 1
def y = 0.5
say "Sum of {x} and {y} is {x+y}"
name = ask "Wie heißt du?"
say "Hello, {name}"
`
	var out1 bytes.Buffer
	if _, err := EvalProgramWithInput(src1, strings.NewReader("Ada\n"), &out1); err != nil {
		t.Fatalf("variant 1 failed: %v", err)
	}
	want1 := "Hello World\nSum of 1 and 0.5 is 1.5\nWie heißt du? Hello, Ada\n"
	if out1.String() != want1 {
		t.Fatalf("variant 1 mismatch:\ngot:  %q\nwant: %q", out1.String(), want1)
	}

	// Variant 2: structured with `to start`, `do say`, `do add`, `do ask ... as number`, `do start`
	src2 := `
def x = 1
def y = 0.5

to add a b
    a + b
end

to start
    do say "Hello World"
    sum = do add x y
    do say "Sum of {x} and {y} is {sum}"

    age = do ask "Wie alt bist du?" as number
    check age is not number -> do say "Keine gültige Zahl."
    do say "Du bist {age} Jahre alt."
end

do start
`
	var out2 bytes.Buffer
	if _, err := EvalProgramWithInput(src2, strings.NewReader("28\n"), &out2); err != nil {
		t.Fatalf("variant 2 failed: %v", err)
	}
	want2 := "Hello World\nSum of 1 and 0.5 is 1.5\nWie alt bist du? Du bist 28 Jahre alt.\n"
	if out2.String() != want2 {
		t.Fatalf("variant 2 mismatch:\ngot:  %q\nwant: %q", out2.String(), want2)
	}
}

func TestTypedListsAndRecordBuilder(t *testing.T) {
	src := `
def particle
    x = 0
    vx = 10
    life = 100
    state = "active" | "dead"
end

empty_scores = list as number
empty_cloud  = list as particle

to update_particles items dt
    total = do len items
    total + dt
end

stepped = do update_particles list as particle 1

cloud = list as particle with
    x = 0, vx = 5, life = 20
    x = 10, vx = 2, life = 5
end

scores = list 10, 25, 40
names  = list "Aria", "Ben", "Cid"

sum_x = 0
loop cloud
    sum_x = sum_x + it.x
end

test
    empty_scores is list
    do len empty_scores is 0
    do len empty_cloud is 0
    stepped is 1
    do len cloud is 2
    cloud.0.vx is 5
    cloud.0.state is "active"
    cloud.1.x is 10
    cloud.1.life is 5
    scores is list 10, 25, 40
    names.1 is "Ben"
    sum_x is 10
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 12 || report.Failed != 0 {
		t.Fatalf("expected 12 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}

	// Mixed types in `list` must fail
	if _, err := EvalProgram("bad = list 10, \"hi\"\n", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Mixed types") {
		t.Fatalf("expected mixed types error, got: %v", err)
	}

	// Pushing wrong type into `list as number` must fail
	if _, err := EvalProgram("xs = list as number\nxs = do push xs \"bad\"\n", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot push") {
		t.Fatalf("expected push type error, got: %v", err)
	}

	// Strict field type check in `with`
	if _, err := EvalProgram("def p\n    hp = 100\nend\nbad = p with hp = \"high\"\n", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Field 'p.hp' expected number, got text") {
		t.Fatalf("expected strict 'with' field type error, got: %v", err)
	}
}

func TestLegacySyntaxRejected(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"param brackets", "to add [a b]\n  a + b\nend\n"},
		{"param commas", "to add a, b\n  a + b\nend\n"},
		{"call commas", "to add a b\n  a + b\nend\nx = do add 1, 2\n"},
		{"double equals", "if 1 == 1\n  say 1\nend\n"},
		{"not equals", "if 1 != 2\n  say 1\nend\n"},
		{"hyphenated is-not", "if 1 is-not 2\n  say 1\nend\n"},
		{"report keyword", "to add a b\n  report a + b\nend\n"},
		{"test colon", "test:\n  1 is 1\nend\n"},
		{"check colon", "check \"math\":\n  1 is 1\nend\n"},
		{"bracket list literal", "xs = [1, 2, 3]\n"},
		{"bracket indexing", "xs = list 1, 2\ny = xs[0]\n"},
		{"paren function call", "to inc x\n  x + 1\nend\ny = inc(1)\n"},
		{"paren ask call", "x = ask(\"Name?\")\n"},
		{"inline test inside function", "to greet name\n  \"Hi {name}\"\ntest\n  do greet \"World\" is \"Hi World\"\nend\n"},
	}
	for _, tc := range cases {
		if tc.name == "report keyword" {
			if _, err := EvalProgram(tc.src+"x = do add 1 2\n", &bytes.Buffer{}); err == nil {
				t.Fatalf("%s: expected error, got nil", tc.name)
			}
			continue
		}
		if _, err := ParseProgram(tc.src); err == nil {
			t.Fatalf("%s: expected SyntaxError, got nil for:\n%s", tc.name, tc.src)
		}
	}
}

func TestInclusiveRangeAndHeadlessScreen(t *testing.T) {
	src := `
import screen

to move_x x key
    check
        key is "right" -> x + 6
        key is "left"  -> x - 6
    end
    x
end

to run_window
    win = screen with
        width  = 800
        height = 600
        title  = "Go with ease"
    end

    pos_x = 400
    state = frame_state

    loop do range 1 10
        check state.status is "closed" -> "Fenster geschlossen:{pos_x}"

        pos_x = do move_x pos_x state.key
        current = scene with
            background = "slate"
            circles = list as circle with
                x = pos_x, y = 300, size = 35, color = "amber"
            end
            boxes = list as box with
                x = 120, y = 500, width = 560, height = 24, color = "emerald"
            end
        end

        raw_state = do send win current
        state = do decode_json raw_state as frame_state
        check state is not frame_state -> "Fensterfehler"
    end

    "Fertig"
end

test "inclusive range & headless screen"
    do range 1 4 is list 1, 2, 3, 4
    do run_window is "Fenster geschlossen:406"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 2 || report.Failed != 0 {
		t.Fatalf("expected 2 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}
}

func TestSingleLineLoopExpressions(t *testing.T) {
	src := `
loop range 1 10 do say it end
`
	var out bytes.Buffer
	if _, err := EvalProgram(src, &out); err != nil {
		t.Fatalf("EvalProgram failed: %v", err)
	}
	want := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\ngot:  %q\nwant: %q", out.String(), want)
	}

	srcNamed := `
sum = 0
loop range 1 4 do sum = sum + it end

test "single-line loop"
    sum is 10
end
`
	report, err := RunChecks(srcNamed)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 1 || report.Failed != 0 {
		t.Fatalf("expected 1 passed check, got %+v", report)
	}
}

func TestCommaFreeFunctionCalls(t *testing.T) {
	src := `
to greet name suffix
    "Hallo {name}{suffix}"
end

to add_three a b c
    a + b + c
end

sum = 0
loop range 1 3 do sum = sum + it end

test "comma-free parameters and calls"
    do greet "Welt" "!" is "Hallo Welt!"
    do greet "Welt" "" is "Hallo Welt"
    do add_three 2 3 4 is 9
    sum is 6
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 4 || report.Failed != 0 {
		t.Fatalf("expected 4 passed checks, got %+v", report)
	}
}

func TestUnicodeIdentifierLexer(t *testing.T) {
	tokens, err := NewLexer(`response = "ok" as ö`).Tokenize()
	if err != nil {
		t.Fatalf("Tokenize failed: %v", err)
	}
	var lastIdent Token
	for _, tok := range tokens {
		if tok.Type == TOK_IDENT {
			lastIdent = tok
		}
	}
	if lastIdent.Lexeme != "ö" {
		t.Fatalf("expected final identifier 'ö', got %+v", lastIdent)
	}
}

func TestMinusRuleAtomicLiteralVsSubtraction(t *testing.T) {
	src := `
to classify n
    check n > 0 -> "positive"
    "non-positive"
end

to get_count
    10
end

to negate x
    -x
end

a = 10

test "atomic negative literal vs binary subtraction"
    do classify -2 is "non-positive"
    do classify -0.5 is "non-positive"
    do get_count - 2 is 8
    a - 2 is 8
    do negate 5 is -5
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 5 || report.Failed != 0 {
		t.Fatalf("expected 5 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}
}

func TestRawSingleQuotedStrings(t *testing.T) {
	src := `
raw_json = '{"id": 7, "msg": "hi\n{not_interp}"}'
test "raw single quotes preserve braces and backslashes literally"
    raw_json is "\{\"id\": 7, \"msg\": \"hi\\n\{not_interp\}\"\}"
    '{x}' is "\{x\}"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 2 || report.Failed != 0 {
		t.Fatalf("expected 2 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}

	if _, err := ParseProgram("bad = 'unterminated\n"); err == nil || !strings.Contains(err.Error(), "Unterminated raw string literal") {
		t.Fatalf("expected Unterminated raw string literal error, got: %v", err)
	}
}

func TestCheckRaisesMatchesMessageOnlyNotSourceLine(t *testing.T) {
	src := `
test "raises must match error message only"
    10 / 0 raises " bogus_substring_on_same_line "
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 0 || report.Failed != 1 {
		t.Fatalf("expected 0 passed / 1 failed, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}
}

func TestModuloFloatAndZero(t *testing.T) {
	src := `
test "float modulo and modulo by zero"
    5 % 0.5 is 0
    5.5 % 2 is 1.5
    10 % 3 is 1
    5 % 0 raises "Modulo by zero"
    5 % 0.0 raises "Modulo by zero"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 5 || report.Failed != 0 {
		t.Fatalf("expected 5 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}
}

func TestFormatValueLargeIntegersNoInt64Overflow(t *testing.T) {
	two63 := math.Ldexp(1, 63) // 9223372036854775808 (exact in float64, overflows int64)
	if got := FormatValue(two63); got != "9223372036854775808" {
		t.Fatalf("FormatValue(2^63) = %q, want %q", got, "9223372036854775808")
	}
	negZero := math.Copysign(0, -1)
	if got := FormatValue(negZero); got != "0" {
		t.Fatalf("FormatValue(-0.0) = %q, want %q", got, "0")
	}
	if got := FormatValue(-10000000000.0 * 1000000000.0); got != "-10000000000000000000" {
		t.Fatalf("FormatValue(-1e19) = %q, want %q", got, "-10000000000000000000")
	}

	src := `
test "large integer floats format without int64 overflow"
    n = 9223372036854775808
    n as text is "9223372036854775808"
    "{n}" is "9223372036854775808"
    big = 10000000000 * 1000000000
    big as text is "10000000000000000000"
    neg_big = -big
    neg_big as text is "-10000000000000000000"
    (-0.0) as text is "0"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 5 || report.Failed != 0 {
		t.Fatalf("expected 5 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}
}

func TestInterpStringErrorLineAndColOffset(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantSub  string
		wantLine int
		wantCol  int
	}{
		{
			name: "division by zero inside interpolation",
			src: `a = 1
b = 2
c = 3
say "result={10 / 0}"
`,
			wantSub:  "Division by zero",
			wantLine: 4,
			wantCol:  17,
		},
		{
			name: "non-atomic record interpolated on later line",
			src: `def hero
    hp = 100
end
say "hero={hero}"
`,
			wantSub:  "Cannot interpolate hero into text",
			wantLine: 4,
			wantCol:  12,
		},
		{
			name: "unknown variable inside interpolation",
			src: `x = 1
y = 2
say "val={missing_var}"
`,
			wantSub:  `Unknown variable "missing_var"`,
			wantLine: 3,
			wantCol:  11,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := EvalProgram(tc.src, &out)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			var easeErr *EaseError
			if !errors.As(err, &easeErr) {
				t.Fatalf("expected *EaseError, got %T: %v", err, err)
			}
			if easeErr.Line != tc.wantLine || easeErr.Col != tc.wantCol {
				t.Fatalf("position mismatch for %s: got line %d col %d, want line %d col %d\nerr:\n%v",
					tc.name, easeErr.Line, easeErr.Col, tc.wantLine, tc.wantCol, err)
			}
			if !strings.Contains(easeErr.Message, tc.wantSub) {
				t.Fatalf("expected message containing %q, got %q", tc.wantSub, easeErr.Message)
			}
		})
	}
}

func TestForLoopNoImplicitFieldInjectionAndTailoredHint(t *testing.T) {
	// 1. Outer `vx = 100` and function parameter `vx` must NOT be shadowed by record field `vx = 5` inside `for ps do ... end`.
	src := `
def particle
    vx = 0
end

to check_param_and_field vx items
    outer_seen = 0
    field_seen = 0
    loop items
        check vx is not 100 -> do fail "parameter vx was shadowed"
        check it.vx is not 5 -> do fail "unexpected it.vx"
        outer_seen = vx
        field_seen = it.vx
    end
    outer_seen * 10 + field_seen
end

vx = 100
p = particle with vx = 5
ps = list as particle
ps = do push ps p

test "no implicit field injection in loop"
    outer_seen = 0
    field_seen = 0
    loop ps
        check vx is not 100 -> do fail "outer vx was shadowed"
        check it.vx is not 5 -> do fail "unexpected it.vx"
        outer_seen = vx
        field_seen = it.vx
    end
    outer_seen is 100
    field_seen is 5
    do check_param_and_field 100 ps is 1005
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 3 {
		t.Fatalf("expected 3 passed / 0 failed checks, got %d passed / %d failed:\n%+v", report.Passed, report.Failed, report.Results)
	}

	// 2. Accessing an undefined bare field `vx` inside `loop ps` produces a tailored error pointing to `it.vx`.
	missingFieldSrc := `
def particle
    vx = 0
end

p = particle with vx = 5
ps = list as particle
ps = do push ps p

loop ps
    say "{vx}"
end
`
	_, err = EvalProgram(missingFieldSrc, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected RuntimeError for bare record field access inside 'for ... do', got nil")
	}
	full := err.Error()
	for _, want := range []string{"Unknown variable \"vx\"", "it.vx", "particle"} {
		if !strings.Contains(full, want) {
			t.Fatalf("expected %q in tailored loop error, got:\n%s", want, full)
		}
	}
}

func TestLargeInputsAndResourceLimitsIssue13(t *testing.T) {
	// 1. Huge `do range 1 1000000000` must fail with RuntimeError instead of Go OOM.
	_, err := EvalProgram("xs = do range 1 1000000000\nsay do len xs\n", &bytes.Buffer{})
	var easeErr *EaseError
	if !errors.As(err, &easeErr) || easeErr.Stage != "RuntimeError" || !strings.Contains(easeErr.Message, "range too large") {
		t.Fatalf("expected RuntimeError 'range too large', got: %v", err)
	}

	// Huge bounds beyond int64 range must also fail cleanly.
	_, err = EvalProgram("xs = do range 1 100000000000000000000\n", &bytes.Buffer{})
	if !errors.As(err, &easeErr) || !strings.Contains(easeErr.Message, "range too large") {
		t.Fatalf("expected RuntimeError 'range too large' for huge float bound, got: %v", err)
	}

	// 2. 200,000 nested `(` in source must fail with SyntaxError instead of stack overflow.
	nested := "say " + strings.Repeat("(", 200_000) + "1" + strings.Repeat(")", 200_000) + "\n"
	_, err = ParseProgram(nested)
	if !errors.As(err, &easeErr) || easeErr.Stage != "SyntaxError" || !strings.Contains(easeErr.Message, "Nesting deeper than") {
		t.Fatalf("expected SyntaxError 'Nesting deeper than ...', got: %v", err)
	}

	// 3. Unbounded HTTP response body (>10 MB) must be capped cleanly without unbounded memory read.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		chunk := bytes.Repeat([]byte("A"), 1024*1024)
		for i := 0; i < 12; i++ {
			_, _ = w.Write(chunk)
		}
	}))
	defer srv.Close()

	httpSrc := fmt.Sprintf(`
import network
n = network with url = %q, timeout = 5
body = do ask n "/big"
body is ""

req = http_request with method = "GET", url = "%s/big", timeout = 5
resp = do http_call req
`, srv.URL, srv.URL)
	prog, err := ParseProgram(httpSrc)
	if err != nil {
		t.Fatalf("ParseProgram failed: %v", err)
	}
	ev := NewEvaluator("", prog, &bytes.Buffer{})
	defer ev.Close()
	if _, err := ev.ExecStatements(prog.Body, ev.global); err != nil {
		t.Fatalf("ExecStatements failed: %v", err)
	}
	respVal, _ := ev.global.Lookup("resp")
	respRec := respVal.(*RecordValue)
	if respRec.Fields["body"] != "" {
		t.Fatalf("expected empty body for oversized response, got %d bytes", len(FormatValue(respRec.Fields["body"])))
	}
	if !strings.Contains(FormatValue(respRec.Fields["error"]), "exceeds 10 MB limit") {
		t.Fatalf("expected HTTP size limit error, got %q", respRec.Fields["error"])
	}
	bodyVal, _ := ev.global.Lookup("body")
	if bodyVal != "" {
		t.Fatalf("expected do ask to return \"\" on oversized HTTP response, got %q", bodyVal)
	}

	// 4. Unopted-in ./std/<name>.ease in working directory must NOT silently override embedded stdlib.
	tmpDir := t.TempDir()
	stdDir := filepath.Join(tmpDir, "std")
	if err := os.MkdirAll(stdDir, 0755); err != nil {
		t.Fatalf("mkdir std: %v", err)
	}
	customMod := "def poisoned\n    flag = \"override\"\nend\n"
	if err := os.WriteFile(filepath.Join(stdDir, "network.ease"), []byte(customMod), 0644); err != nil {
		t.Fatalf("write override module: %v", err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	// Without EASE_STD, the poisoned disk file must be ignored.
	t.Setenv("EASE_STD", "")
	pEmbedded, err := ParseProgram("import network\nn = network with url = \"https://example.test\"\n")
	if err != nil || len(pEmbedded.Defs) == 0 {
		t.Fatalf("expected embedded network stdlib to load cleanly, got err=%v", err)
	}
	for _, d := range pEmbedded.Defs {
		if d.Name == "poisoned" {
			t.Fatalf("unexpectedly loaded ./std/network.ease without EASE_STD opt-in")
		}
	}

	// With EASE_STD=1, disk override is honored explicitly.
	t.Setenv("EASE_STD", "1")
	pOverride, err := ParseProgram("import network\nx = poisoned with flag = \"ok\"\n")
	if err != nil {
		t.Fatalf("expected EASE_STD=1 to load ./std/network.ease override, got err=%v", err)
	}
	if _, err := EvalProgram("import network\nx = poisoned with flag = \"ok\"\n", &bytes.Buffer{}); err != nil {
		t.Fatalf("expected EvalProgram with EASE_STD=1 override to succeed, got: %v", err)
	}
	_ = pOverride
}

func TestFunctionScopingReadOnlyGlobalsBlockScopeAndNoIf(t *testing.T) {
	// 1. Function local assignment does not overwrite global, and reading global works.
	src := `
tax_rate = 0.2
total = 100

to sum_to n
    total = 0
    loop range 1 n
        total = total + it
    end
    total
end

to with_tax price
    price + (price * tax_rate)
end

say do sum_to 3
say total
say do with_tax 10
`
	var out bytes.Buffer
	if _, err := EvalProgram(src, &out); err != nil {
		t.Fatalf("expected valid scoping program, got error: %v", err)
	}
	if got, want := strings.TrimSpace(out.String()), "6\n100\n12"; got != want {
		t.Fatalf("unexpected output: got %q, want %q", got, want)
	}

	// 2. Attempting to mutate a global (read before local assignment) raises clear RuntimeError.
	mutateGlobalSrc := `
count = 0
to bump
    count = count + 1
end
do bump
`
	if _, err := EvalProgram(mutateGlobalSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot modify global 'count' inside a function; pass it in and return the new value instead.") {
		t.Fatalf("expected global mutation error, got: %v", err)
	}

	// 3. Reading a global earlier in a function when it is assigned later in a block also fails cleanly.
	mixedScopeSrc := `
total = 100
to mixed n
    loop range 1 n
        total = 0
    end
    total
end
say do mixed 2
`
	if _, err := EvalProgram(mixedScopeSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot modify global 'total' inside a function") {
		t.Fatalf("expected mixed global/local error, got: %v", err)
	}

	// 4. Block scope in check (-> and else) keeps newly assigned variables local to the branch.
	checkArrowLeakSrc := `
to describe x
    check x > 0 -> r = "pos"
    "got {r}"
end
say do describe 1
`
	if _, err := EvalProgram(checkArrowLeakSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `Unknown variable "r"`) {
		t.Fatalf("expected block-scoped r inside check -> to be unknown outside, got: %v", err)
	}

	checkElseLeakSrc := `
to describe x
    check
        x > 0 -> r = "pos"
        else
            r = "non-pos"
    end
    "got {r}"
end
say do describe 0
`
	if _, err := EvalProgram(checkElseLeakSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `Unknown variable "r"`) {
		t.Fatalf("expected block-scoped r inside check else to be unknown outside, got: %v", err)
	}

	// Updating a pre-declared local variable inside check works.
	checkUpdateLocalSrc := `
to describe x
    r = "non-pos"
    check x > 0 -> r = "pos"
    "got {r}"
end
say do describe 1
`
	var out2 bytes.Buffer
	if _, err := EvalProgram(checkUpdateLocalSrc, &out2); err != nil {
		t.Fatalf("expected pre-declared local update inside check to succeed, got: %v", err)
	}
	if got := strings.TrimSpace(out2.String()); got != "got pos" {
		t.Fatalf("unexpected output: got %q, want %q", got, "got pos")
	}

	// 5. Reassigning a function parameter gives a parameter-specific error (not 'def' constant).
	paramReassignSrc := `
to inc n
    n = n + 1
    n
end
say do inc 1
`
	if _, err := EvalProgram(paramReassignSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `Cannot reassign parameter "n"; assign to a new local variable instead.`) {
		t.Fatalf("expected parameter reassignment error, got: %v", err)
	}

	// 6. 'if ... end' is removed from Ease.
	ifStmtSrc := `
to f x
    if x > 0
        "pos"
    end
end
`
	if _, err := ParseProgram(ifStmtSrc); err == nil {
		t.Fatalf("expected 'if ... end' to fail parsing after removal")
	}
}

func TestIssue8CheckFormsArrowSemanticsAndTopLevelValueError(t *testing.T) {
	// 1. Parse-time rejection of bare `check` without `->` (single-line and block without `else`).
	bareSingle := "x = 1\ncheck x > 0\n"
	if _, err := ParseProgram(bareSingle); err == nil || !strings.Contains(err.Error(), "Expected '->'") {
		t.Fatalf("expected parse error for single-line check without '->', got: %v", err)
	}

	bareBlock := "x = 1\ncheck\n    x > 0\nend\n"
	if _, err := ParseProgram(bareBlock); err == nil || !strings.Contains(err.Error(), "'check' block requires '->' actions or an 'else' branch") {
		t.Fatalf("expected parse error for check block without '->' or 'else', got: %v", err)
	}

	// 2. Arrow semantics in functions & loops:
	//    - value expression returns from the entire function (even inside loops)
	//    - statement (assignment / say) executes and continues to next iteration/line
	arrowLoopSrc := `
to count_a words
    cnt = 0
    loop words
        check it is "a" -> cnt = cnt + 1
    end
    cnt
end

to first_big xs
    loop xs
        check it > 1 -> it
    end
    0
end

words = list "a", "b", "a"
nums = list 1, 2, 3
say do count_a words
say do first_big nums
`
	var out bytes.Buffer
	if _, err := EvalProgram(arrowLoopSrc, &out); err != nil {
		t.Fatalf("expected arrow loop program to succeed, got: %v", err)
	}
	if got, want := strings.TrimSpace(out.String()), "2\n2"; got != want {
		t.Fatalf("unexpected output: got %q, want %q", got, want)
	}

	// 3. Top-level value after `->` is rejected with clear diagnostic (both when true and when false).
	for _, xVal := range []string{"1", "0"} {
		topValSrc := "x = " + xVal + "\ncheck x > 0 -> \"A\"\n"
		if _, err := EvalProgram(topValSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot return a value from 'check' at top level; use 'say' or 'do fail' instead.") {
			t.Fatalf("expected top-level check value error (x=%s), got: %v", xVal, err)
		}
	}

	// Top-level statement after `->` works cleanly.
	var topOut bytes.Buffer
	if _, err := EvalProgram("x = 1\ncheck x > 0 -> say \"ok\"\n", &topOut); err != nil {
		t.Fatalf("expected top-level check with say to succeed, got: %v", err)
	}
	if got := strings.TrimSpace(topOut.String()); got != "ok" {
		t.Fatalf("unexpected top-level output: got %q, want %q", got, "ok")
	}
}

func TestHardeningEvalRepeatHTTPPoolingScreenCloseAndListDefaults(t *testing.T) {
	// 1. Loop range limit (Finding 2 / maxListSize)
	_, err := EvalProgram("loop range 1 1000001\n    1\nend\n", &bytes.Buffer{})
	var easeErr *EaseError
	if err == nil || !errors.As(err, &easeErr) || easeErr.Stage != "RuntimeError" || !strings.Contains(easeErr.Message, "range too large") {
		t.Fatalf("expected RuntimeError for oversized loop range, got: %v", err)
	}

	// 2. Shared http.Client connection pooling & per-request timeout (Finding 3)
	var newConns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(150 * time.Millisecond)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	httpSrc := fmt.Sprintf(`
import network
n = network with url = %q, timeout = 2
a = do ask n "/fast"
b = do ask n "/fast"
c = do ask n "/fast"
say "{a}-{b}-{c}"
`, srv.URL)
	var httpOut bytes.Buffer
	if _, err := EvalProgram(httpSrc, &httpOut); err != nil {
		t.Fatalf("expected pooled HTTP calls to succeed, got: %v", err)
	}
	if got := strings.TrimSpace(httpOut.String()); got != "ok-ok-ok" {
		t.Fatalf("unexpected HTTP output: got %q, want %q", got, "ok-ok-ok")
	}
	if conns := newConns.Load(); conns != 1 {
		t.Fatalf("expected HTTP keep-alive connection reuse (1 connection), got %d", conns)
	}

	timeoutSrc := fmt.Sprintf(`
import network
req = http_request with url = %q, timeout = 0.03
r = do http_call req
say r.error
`, srv.URL+"/slow")
	var timeoutOut bytes.Buffer
	if _, err := EvalProgram(timeoutSrc, &timeoutOut); err != nil {
		t.Fatalf("expected timeout HTTP call to return error record cleanly, got: %v", err)
	}
	if !strings.Contains(timeoutOut.String(), "deadline exceeded") && !strings.Contains(timeoutOut.String(), "context") {
		t.Fatalf("expected context deadline error in response.error, got: %q", timeoutOut.String())
	}

	// 3. screenSession.close() waits cleanly without deadlocking if exited or non-ticking (Finding 4)
	sessExited := &screenSession{
		game:   &screenGame{doneCh: make(chan struct{})},
		exited: make(chan struct{}),
	}
	go func() {
		<-sessExited.game.doneCh
		close(sessExited.exited)
	}()
	done := make(chan struct{})
	go func() {
		sessExited.close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("screenSession.close() blocked unexpectedly when exited closed")
	}

	// 4. Default lists (and nested records inside lists) are cloned per record instance (Finding 7)
	listDefaultSrc := `
def pt
    x = 0
end
def bag
    items = list 1, 2
    pts = list pt
end
b1 = bag with items = bag.items
b2 = bag with items = bag.items
items1 = do push b1.items 3
say "{do len items1}-{do len b2.items}-{do len bag.items}"
`
	var listOut bytes.Buffer
	if _, err := EvalProgram(listDefaultSrc, &listOut); err != nil {
		t.Fatalf("expected default list isolation program to succeed, got: %v", err)
	}
	if got := strings.TrimSpace(listOut.String()); got != "3-2-2" {
		t.Fatalf("expected isolated list lengths 3-2-2, got %q", got)
	}

	// Also verify nested record inside default list is instantiated (IsPrototype == false).
	prog, err := ParseProgram("def pt\n    x = 0\nend\ndef bag\n    pts = list pt\nend\nb = bag with pts = bag.pts\n")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	ev := NewEvaluator("", prog, &bytes.Buffer{})
	defer ev.Close()
	if _, err := ev.ExecStatements(prog.Body, ev.global); err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	bVal, ok := ev.global.Lookup("b")
	if !ok {
		t.Fatalf("missing variable b")
	}
	bRec := bVal.(*RecordValue)
	ptsList := bRec.Fields["pts"].(*ListValue)
	if len(ptsList.Elements) != 1 || ptsList.Elements[0].(*RecordValue).IsPrototype {
		t.Fatalf("expected nested record inside default list to be instantiated (IsPrototype=false)")
	}
}

func TestUnifiedLoopItIndexBreakEndAndDynamicIndexing(t *testing.T) {
	// 1. it.index (0-based) on range and records, plus dynamic indexing xs.(expr) & Fibonacci with `-> end`
	happySrc := `
def item
    label = ""
end

items = list as item with
    label = "a"
    label = "b"
end

loop items
    say "{it.index}:{items.(it.index).label}"
end

# Fibonacci with loop range 1 100, seq.(it.index - 1), and check -> end
seq = list 0, 1
loop range 1 100
    check
        it.index < 2 -> say "seed:{seq.(it.index)}"
        else
            next = seq.(it.index - 1) + seq.(it.index - 2)
            check next > 25 -> end
            seq = do push seq next
    end
end
say do len seq
say seq.((do len seq) - 1)
`
	var out bytes.Buffer
	if _, err := EvalProgram(happySrc, &out); err != nil {
		t.Fatalf("expected happy loop program to succeed, got: %v", err)
	}
	want := "0:a\n1:b\nseed:0\nseed:1\n9\n21"
	if got := strings.TrimSpace(out.String()); got != want {
		t.Fatalf("unexpected output:\ngot:  %q\nwant: %q", got, want)
	}

	// 2. check -> end exits ONLY innermost loop (not enclosing `to` function or outer loop)
	nestedAndFnSrc := `
to find_first_over limit xs
    found = -1
    loop xs
        check
            it <= limit -> say "small:{it}"
            else
                found = it
                check it > 0 -> end
        end
    end
    found + 100
end

xs_in = list 1, 8, 12
say do find_first_over 5 xs_in

pairs = list as text
loop range 1 2
    outer = it
    loop range 10 12
        check
            it is 11 -> end
        end
        pairs = do push pairs "{outer}:{it}:{it.index}"
    end
end
say do len pairs
say pairs.0
say pairs.1
`
	var out2 bytes.Buffer
	if _, err := EvalProgram(nestedAndFnSrc, &out2); err != nil {
		t.Fatalf("expected nested loop & check -> end in function to succeed, got: %v", err)
	}
	want2 := "small:1\n108\n2\n1:10:0\n2:10:0"
	if got := strings.TrimSpace(out2.String()); got != want2 {
		t.Fatalf("unexpected nested output:\ngot:  %q\nwant: %q", got, want2)
	}

	// 3. Negative tests: loop 3, loop x in xs, for, repeat, -> end outside loop, def field named index
	negCases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "loop scalar number",
			src:  "loop 3\n    say it\nend\n",
			want: "'loop' requires a list or range, got number. Use 'loop range 1 3' instead.",
		},
		{
			name: "loop scalar text",
			src:  "loop \"abc\"\n    say it\nend\n",
			want: "'loop' requires a list or range, got text",
		},
		{
			name: "loop named var",
			src:  "xs = list 1, 2\nloop x in xs\n    say x\nend\n",
			want: "Named loop variables like 'loop x in ...' are not supported",
		},
		{
			name: "legacy for",
			src:  "xs = list 1\nfor xs do\n    say it\nend\n",
			want: "'for' loops were removed; use 'loop' instead",
		},
		{
			name: "legacy repeat",
			src:  "repeat 3\n    say 1\nend\n",
			want: "'repeat' loops were removed; use 'loop range' instead",
		},
		{
			name: "arrow end at top level",
			src:  "x = 1\ncheck x > 0 -> end\n",
			want: "'-> end' can only be used inside a 'loop' block",
		},
		{
			name: "arrow end in function outside loop",
			src:  "to f x\n    check x > 0 -> end\n    x\nend\n",
			want: "'-> end' can only be used inside a 'loop' block",
		},
		{
			name: "def field named index",
			src:  "def bad\n    index = 0\nend\n",
			want: "reserved for 'it.index'",
		},
		{
			name: "reassign it inside loop",
			src:  "loop range 1 3\n    it = 99\nend\n",
			want: "Cannot reassign constant \"it\"",
		},
	}
	for _, tc := range negCases {
		_, err := EvalProgram(tc.src, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected error containing %q, got: %v", tc.name, tc.want, err)
		}
	}
}
