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
to double x -> x * 2

to divide a b -> a / b

test "functions"
  double 4 is 8
  double 4 is not 9
  divide 10 2 is 5
  divide 1 0 is error
end

test "loops and mutability"
  factor = 3
  acc = 0
  loop range 1 4
    acc = acc + factor
  end
  acc is 12
end

res = double 7
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
to square n -> n * n

test
  square 3 is 10
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

    -> "Willkommen {name}, du darfst rein!"
end

to is_valid_vip code age
    check
        code is not text     -> "invalid"
        code is not "VIP2026" -> "invalid"
        age is not number    -> "invalid"
        age < 18             -> "invalid"
    end

    -> "valid"
end

test
    allow_entry 123 25      is "Name muss Text sein"
    allow_entry "" 25       is "Name darf nicht leer sein"
    allow_entry "Max" "abc" is "Alter muss eine Zahl sein"
    allow_entry "Max" 16    is "Du bist 16. Das ist jung"
    allow_entry "Max" 150   is "Ungültiges Alter"
    allow_entry "Max" 25    is "Willkommen Max, du darfst rein!"

    is_valid_vip "VIP2026" 20 is "valid"
    is_valid_vip "VIP2026" 16 is "invalid"
    is_valid_vip "FALSCH" 25  is "invalid"
    is_valid_vip "VIP2026" "x" is "invalid"
end

user_name = ask "Wie heißt du?"
user_age  = ask "Wie alt bist du?"
check user_age is not number
    say "Bitte beim nächsten Mal eine Zahl eingeben."
end
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
say type greg
say fields greg
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
    -> "Willkommen im Hauptbereich"
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
    -> p with hp = next_hp
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

	// Variant 2: structured with `to start`, `say`, `add`, `ask ... as number`, `start`
	src2 := `
def x = 1
def y = 0.5

to add a b -> a + b

to start
    say "Hello World"
    total = add x y
    say "Sum of {x} and {y} is {total}"

    age = ask "Wie alt bist du?" as number
    check age is not number
        say "Keine gültige Zahl."
    end
    say "Du bist {age} Jahre alt."
end

start
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
    total = count items
    -> total + dt
end

stepped = update_particles list as particle 1

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
    count empty_scores is 0
    count empty_cloud is 0
    stepped is 1
    count cloud is 2
    cloud.1.vx is 5
    cloud.1.state is "active"
    cloud.2.x is 10
    cloud.2.life is 5
    scores is list 10, 25, 40
    names.1 is "Aria"
    names.2 is "Ben"
    sum_x is 10
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 13 || report.Failed != 0 {
		t.Fatalf("expected 13 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}

	// Mixed types in `list` must fail
	if _, err := EvalProgram("bad = list 10, \"hi\"\n", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Mixed types") {
		t.Fatalf("expected mixed types error, got: %v", err)
	}

	// Pushing wrong type into `list as number` must fail
	if _, err := EvalProgram("xs = list as number\nxs = append xs \"bad\"\n", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot append") {
		t.Fatalf("expected append type error, got: %v", err)
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
		{"call commas", "to add a b\n  a + b\nend\nx = add 1, 2\n"},
		{"double equals", "if 1 == 1\n  say 1\nend\n"},
		{"not equals", "if 1 != 2\n  say 1\nend\n"},
		{"hyphenated is-not", "if 1 is-not 2\n  say 1\nend\n"},
		{"report keyword", "to add a b\n  report a + b\nend\n"},
		{"test colon", "test:\n  1 is 1\nend\n"},
		{"check colon", "check \"math\":\n  1 is 1\nend\n"},
		{"bracket list literal", "xs = [1, 2, 3]\n"},
		{"bracket indexing", "xs = list 1, 2\ny = xs[0]\n"},
		{"paren function call", "to add a b\n  a + b\nend\ny = add(1, 2)\n"},
		{"paren ask call", "x = ask()\n"},
		{"inline test inside function", "to greet name\n  \"Hi {name}\"\ntest\n  greet \"World\" is \"Hi World\"\nend\n"},
	}
	for _, tc := range cases {
		if tc.name == "report keyword" {
			if _, err := EvalProgram(tc.src+"x = add 1 2\n", &bytes.Buffer{}); err == nil {
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
    -> x
end

to run_window
    win = screen.screen with
        width  = 800
        height = 600
        title  = "Go with ease"
    end

    pos_x = 400
    state = screen.frame_state

    loop range 1 10
        check state.status is "closed" -> "Fenster geschlossen:{pos_x}"

        pos_x = move_x pos_x state.key
        current = screen.scene with
            background = "slate"
            circles = list as screen.circle with
                x = pos_x, y = 300, size = 35, color = "amber"
            end
            boxes = list as screen.box with
                x = 120, y = 500, width = 560, height = 24, color = "emerald"
            end
        end

        raw_state = screen:present win current
        decoded_state = raw_state as json
        state = decoded_state as screen.frame_state
        check state is not screen.frame_state -> "Fensterfehler"
    end

    -> "Fertig"
end

test "inclusive range & headless screen"
    range 1 4 is list 1, 2, 3, 4
    run_window is "Fenster geschlossen:406"
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
loop range 1 10 say it end
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
total = 0
loop range 1 4 total = total + it end

test "single-line loop"
    total is 10
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
to greet name suffix -> "Hallo {name}{suffix}"

to add_three a b c -> a + b + c

total = 0
loop range 1 3 total = total + it end

test "comma-free parameters and calls"
    greet "Welt" "!" is "Hallo Welt!"
    greet "Welt" "" is "Hallo Welt"
    add_three 2 3 4 is 9
    total is 6
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
    -> "non-positive"
end

to get_count -> 10

to negate x -> -x
to add a b -> a + b

a = 10

test "atomic negative literal vs binary subtraction"
    classify -2 is "non-positive"
    classify -0.5 is "non-positive"
    add 10 -4 is 6
    range -2 1 is list -2, -1, 0, 1
    get_count - 2 is 8
    a - 2 is 8
    negate 5 is -5
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 7 || report.Failed != 0 {
		t.Fatalf("expected 7 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}

	wantHint := "Put spaces around '-' for subtraction (e.g. 'x - 2'); '-2' without a space is parsed as a negative number argument."
	missingSpaceCases := []struct {
		name string
		src  string
	}{
		{"literal_sub", "3-2\n"},
		{"var_sub", "x = 10\ny = x-2\n"},
		{"paren_expr_sub", "(1+2)-3\n"},
		{"inside_parens", "x = 10\ny = (x-2)\n"},
		{"inside_interp", "x = 10\nsay \"val={x-2}\"\n"},
		{"inside_check_header", "x = 10\ncheck x-2 > 0 -> 1\n"},
	}
	for _, tc := range missingSpaceCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil || !strings.Contains(err.Error(), wantHint) {
				t.Fatalf("expected hint %q for %s, got: %v", wantHint, tc.name, err)
			}
		})
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

func TestErrorBlockMatchesMessageOnlyNotSourceLine(t *testing.T) {
	src := `
test "error block must match error message only"
    10 / 0 ->
        error.message is "bogus_substring_on_same_line"
    end
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
    5 % 0 is error
    5 % 0.0 is error
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
	// 1. Outer `vx = 100` and function parameter `vx` must NOT be shadowed by record field `vx = 5` inside `for ps ... end`.
	src := `
def particle
    vx = 0
end

to check_param_and_field vx items
    outer_seen = 0
    field_seen = 0
    loop items
        check vx is not 100 -> -1
        check it.vx is not 5 -> -2
        outer_seen = vx
        field_seen = it.vx
    end
    -> outer_seen * 10 + field_seen
end

vx = 100
p = particle with vx = 5
ps = list as particle
ps = append ps p

test "no implicit field injection in loop"
    outer_seen = 0
    field_seen = 0
    loop ps
        vx is 100
        it.vx is 5
        outer_seen = vx
        field_seen = it.vx
    end
    outer_seen is 100
    field_seen is 5
    check_param_and_field 100 ps is 1005
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 5 {
		t.Fatalf("expected 5 passed / 0 failed checks, got %d passed / %d failed:\n%+v", report.Passed, report.Failed, report.Results)
	}

	// 2. Accessing an undefined bare field `vx` inside `loop ps` produces a tailored error pointing to `it.vx`.
	missingFieldSrc := `
def particle
    vx = 0
end

p = particle with vx = 5
ps = list as particle
ps = append ps p

loop ps
    say "{vx}"
end
`
	_, err = EvalProgram(missingFieldSrc, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected RuntimeError for bare record field access inside 'loop', got nil")
	}
	full := err.Error()
	for _, want := range []string{"Unknown variable \"vx\"", "it.vx", "particle"} {
		if !strings.Contains(full, want) {
			t.Fatalf("expected %q in tailored loop error, got:\n%s", want, full)
		}
	}
}

func TestLargeInputsAndResourceLimitsIssue13(t *testing.T) {
	// 1. Huge `range 1 1000000000` must fail with RuntimeError instead of Go OOM.
	_, err := EvalProgram("xs = range 1 1000000000\nsay count xs\n", &bytes.Buffer{})
	var easeErr *EaseError
	if !errors.As(err, &easeErr) || easeErr.Stage != "RuntimeError" || !strings.Contains(easeErr.Message, "range too large") {
		t.Fatalf("expected RuntimeError 'range too large', got: %v", err)
	}

	// Huge bounds beyond int64 range must also fail cleanly.
	_, err = EvalProgram("xs = range 1 100000000000000000000\n", &bytes.Buffer{})
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
n = network.network with url = %q, timeout = 5
body = network:fetch n "/big"
body is ""
req = network.http_request with method = "GET", url = "%s/big", timeout = 5
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
	bodyVal, _ := ev.global.Lookup("body")
	if bodyVal != "" {
		t.Fatalf("expected empty body for oversized response, got %d bytes", len(FormatValue(bodyVal)))
	}
	reqBigVal, _ := ev.global.Lookup("req")
	respRec, err := ev.execHTTPCall(reqBigVal.(*RecordValue), 1, 1)
	if err != nil {
		t.Fatalf("execHTTPCall(/big) failed: %v", err)
	}
	if respRec.Fields["body"] != "" {
		t.Fatalf("expected empty body for oversized response, got %d bytes", len(FormatValue(respRec.Fields["body"])))
	}
	if !strings.Contains(FormatValue(respRec.Fields["error"]), "exceeds 10 MB limit") {
		t.Fatalf("expected HTTP size limit error, got %q", respRec.Fields["error"])
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
	pEmbedded, err := ParseProgram("import network\nn = network.network with url = \"https://example.test\"\n")
	if err != nil || len(pEmbedded.Imports) == 0 || pEmbedded.Imports[0].ModuleProg == nil || len(pEmbedded.Imports[0].ModuleProg.Defs) == 0 {
		t.Fatalf("expected embedded network stdlib to load cleanly, got err=%v", err)
	}
	for _, d := range pEmbedded.Imports[0].ModuleProg.Defs {
		if d.Name == "poisoned" {
			t.Fatalf("unexpectedly loaded ./std/network.ease without EASE_STD opt-in")
		}
	}

	// With EASE_STD=1, disk override is honored explicitly.
	t.Setenv("EASE_STD", "1")
	pOverride, err := ParseProgram("import network\nx = network.poisoned with flag = \"ok\"\n")
	if err != nil {
		t.Fatalf("expected EASE_STD=1 to load ./std/network.ease override, got err=%v", err)
	}
	if _, err := EvalProgram("import network\nx = network.poisoned with flag = \"ok\"\n", &bytes.Buffer{}); err != nil {
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
    -> total
end

to with_tax price -> price + (price * tax_rate)

say sum_to 3
say total
say with_tax 10
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
counter = 0
to bump
    counter = counter + 1
end
bump
`
	if _, err := EvalProgram(mutateGlobalSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot modify global 'counter' inside a function; pass it in and return the new value instead.") {
		t.Fatalf("expected global mutation error, got: %v", err)
	}

	// 3. Reading a global earlier in a function when it is assigned later in a block also fails cleanly.
	mixedScopeSrc := `
total = 100
to mixed n
    loop range 1 n
        total = 0
    end
    -> total
end
say mixed 2
`
	if _, err := EvalProgram(mixedScopeSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot modify global 'total' inside a function") {
		t.Fatalf("expected mixed global/local error, got: %v", err)
	}

	// 4. Block scope in check effect block keeps newly assigned variables local to the branch.
	checkArrowLeakSrc := `
to describe x
    check x > 0
        r = "pos"
    end
    -> "got {r}"
end
say describe 1
`
	if _, err := EvalProgram(checkArrowLeakSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `Unknown variable "r"`) {
		t.Fatalf("expected block-scoped r inside check effect block to be unknown outside, got: %v", err)
	}

	checkElseLeakSrc := `
to describe x
    check x > 0
        r = "pos"
    else
        r = "non-pos"
    end
    -> "got {r}"
end
say describe 0
`
	if _, err := EvalProgram(checkElseLeakSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `Unknown variable "r"`) {
		t.Fatalf("expected block-scoped r inside check else to be unknown outside, got: %v", err)
	}

	// Updating a pre-declared local variable inside check effect block works.
	checkUpdateLocalSrc := `
to describe x
    r = "non-pos"
    check x > 0
        r = "pos"
    end
    -> "got {r}"
end
say describe 1
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
    -> n
end
say inc 1
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
	// 1. Parse-time rejection of unclosed effect check and bare rules inside verdict table.
	bareSingle := "x = 1\ncheck x > 0\n"
	if _, err := ParseProgram(bareSingle); err == nil || !strings.Contains(err.Error(), "'check' block is missing 'end'") {
		t.Fatalf("expected parse error for unclosed check effect block, got: %v", err)
	}

	bareBlock := "x = 1\ncheck\n    x > 0\nend\n"
	if _, err := ParseProgram(bareBlock); err == nil || !strings.Contains(err.Error(), "Every rule inside a 'check ... end' verdict table must have '-> <value | end>'") {
		t.Fatalf("expected parse error for check verdict block without '->', got: %v", err)
	}

	// 2. Arrow semantics in functions & loops vs effect blocks:
	//    - `-> <expr>` returns from the entire function (even inside loops)
	//    - `check <cond> ... end` effect block runs statements and continues to next iteration/line
	arrowLoopSrc := `
to count_a words
    cnt = 0
    loop words
        check it is "a"
            cnt = cnt + 1
        end
    end
    -> cnt
end

to first_big xs
    loop xs
        check it > 1 -> it
    end
    -> 0
end

words = list "a", "b", "a"
nums = list 1, 2, 3
say count_a words
say first_big nums
`
	var out bytes.Buffer
	if _, err := EvalProgram(arrowLoopSrc, &out); err != nil {
		t.Fatalf("expected arrow loop program to succeed, got: %v", err)
	}
	if got, want := strings.TrimSpace(out.String()), "2\n2"; got != want {
		t.Fatalf("unexpected output: got %q, want %q", got, want)
	}

	// 3. Top-level value after `->` is rejected statically (both when true and when false).
	for _, xVal := range []string{"1", "0"} {
		topValSrc := "x = " + xVal + "\ncheck x > 0 -> \"A\"\n"
		if _, err := EvalProgram(topValSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot use '-> <value>' at top level") {
			t.Fatalf("expected top-level check value error (x=%s), got: %v", xVal, err)
		}
	}

	// Top-level effect block works cleanly.
	var topOut bytes.Buffer
	if _, err := EvalProgram("x = 1\ncheck x > 0\n    say \"ok\"\nend\n", &topOut); err != nil {
		t.Fatalf("expected top-level check effect block to succeed, got: %v", err)
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
n = network.network with url = %q, timeout = 2
a = network:fetch n "/fast"
b = network:fetch n "/fast"
c = network:fetch n "/fast"
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
n = network.network with url = %q, timeout = 0.03
body = network:fetch n "/slow"
req = network.http_request with url = %q, timeout = 0.03
`, srv.URL, srv.URL+"/slow")
	timeoutProg, err := ParseProgram(timeoutSrc)
	if err != nil {
		t.Fatalf("ParseProgram(timeoutSrc) failed: %v", err)
	}
	timeoutEv := NewEvaluator(timeoutSrc, timeoutProg, &bytes.Buffer{})
	defer timeoutEv.Close()
	if _, err := timeoutEv.ExecStatements(timeoutProg.Body, timeoutEv.global); err != nil {
		t.Fatalf("expected timeout network:fetch to return empty string cleanly, got: %v", err)
	}
	reqVal, _ := timeoutEv.global.Lookup("req")
	rRec, err := timeoutEv.execHTTPCall(reqVal.(*RecordValue), 1, 1)
	if err != nil {
		t.Fatalf("expected timeout execHTTPCall to return error record cleanly, got: %v", err)
	}
	errStr := FormatValue(rRec.Fields["error"])
	if !strings.Contains(errStr, "deadline exceeded") && !strings.Contains(errStr, "context") {
		t.Fatalf("expected context deadline error in response.error, got: %q", errStr)
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
items1 = append b1.items 3
say "{count items1}-{count b2.items}-{count bag.items}"
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
	// 1. it.index (1-based) on range and records, plus dynamic indexing xs.(expr) & Fibonacci with `-> end`
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
    check it.index <= 2
        say "seed:{seq.(it.index)}"
    else
        next = seq.(it.index - 1) + seq.(it.index - 2)
        check next > 25 -> end
        seq = append seq next
    end
end
say count seq
say seq.(count seq)
`
	var out bytes.Buffer
	if _, err := EvalProgram(happySrc, &out); err != nil {
		t.Fatalf("expected happy loop program to succeed, got: %v", err)
	}
	want := "1:a\n2:b\nseed:0\nseed:1\n9\n21"
	if got := strings.TrimSpace(out.String()); got != want {
		t.Fatalf("unexpected output:\ngot:  %q\nwant: %q", got, want)
	}

	// 2. check -> end exits ONLY innermost loop (not enclosing `to` function or outer loop)
	nestedAndFnSrc := `
to find_first_over limit xs
    found = -1
    loop xs
        check it <= limit
            say "small:{it}"
        else
            found = it
            check it > 0 -> end
        end
    end
    -> found + 100
end

xs_in = list 1, 8, 12
say find_first_over 5 xs_in

pairs = list as text
loop range 1 2
    outer = it
    loop range 10 12
        check
            it is 11 -> end
        end
        pairs = append pairs "{outer}:{it}:{it.index}"
    end
end
say count pairs
say pairs.1
say pairs.2
`
	var out2 bytes.Buffer
	if _, err := EvalProgram(nestedAndFnSrc, &out2); err != nil {
		t.Fatalf("expected nested loop & check -> end in function to succeed, got: %v", err)
	}
	want2 := "small:1\n108\n2\n1:10:1\n2:10:1"
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
			src:  "to f x\n    check x > 0 -> end\n    -> x\nend\n",
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

func TestArityPrefixCallsAndNoDo(t *testing.T) {
	happySrc := `
def user
    id   = 0
    name = "Guest"
end

say greet "Ada"

to greet name -> "Hi {name}"

to ping -> "pong"

to add a b -> a + b

to sum_squares xs
    total = 0
    loop xs
        total = total + (it * it)
    end
    -> total
end

to fib n
    check n <= 1 -> n
    -> fib (n - 1) + fib (n - 2)
end

say ping
say add (1 + 2) 3
say sum_squares range 1 5
say fib 7
raw = '{"id": 1, "name": "Ada"}'
decoded_u = raw as json
u = decoded_u as user
say u.name
loop range 1 3 say it end
`
	var out bytes.Buffer
	if _, err := EvalProgram(happySrc, &out); err != nil {
		t.Fatalf("expected arity prefix program to succeed, got: %v", err)
	}
	want := "Hi Ada\npong\n6\n55\n13\nAda\n1\n2\n3\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\ngot:\n%s\nwant:\n%s", out.String(), want)
	}

	// Fixed arity 1 for ask ""
	var askOut bytes.Buffer
	val, err := EvalProgramWithInput("ans = ask \"\"\nsay ans\n", strings.NewReader("hello\n"), &askOut)
	if err != nil {
		t.Fatalf("ask \"\" failed: %v", err)
	}
	_ = val
	if askOut.String() != "hello\n" {
		t.Fatalf("expected 'hello\\n', got %q", askOut.String())
	}

	negCases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "unknown prefix identifier do",
			src:  "to add a b\n  a + b\nend\nx = do add 2 3\n",
			want: "SyntaxError",
		},
		{
			name: "too few arguments",
			src:  "to add a b\n  a + b\nend\nx = add 2\n",
			want: "Function 'add' expects 2 argument(s), got 1",
		},
		{
			name: "ask without argument",
			src:  "x = ask\n",
			want: "Function 'ask' expects 1 argument(s), got 0",
		},
		{
			name: "var collides with function",
			src:  "to add a b\n  a + b\nend\nadd = 5\n",
			want: "Cannot name variable \"add\" the same as a 'to' function",
		},
		{
			name: "var collides with builtin",
			src:  "count = 0\n",
			want: "Cannot name variable \"count\" the same as a built-in function",
		},
		{
			name: "func collides with builtin",
			src:  "to count x\n  x\nend\n",
			want: "Cannot define function \"count\": it conflicts with a built-in function",
		},
		{
			name: "param collides with function",
			src:  "to add a b\n  a + b\nend\nto f add\n  1\nend\n",
			want: "Cannot name parameter \"add\" the same as a 'to' function",
		},
	}
	for _, tc := range negCases {
		if _, err := ParseProgram(tc.src); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected error containing %q, got: %v", tc.name, tc.want, err)
		}
	}
}

func TestVectorArithmeticAppendAndAggregations(t *testing.T) {
	happySrc := `
prices = list 10, 20, 30
shipping = list 5, 5, 10
totals = prices * 2 + shipping
say totals
say 100 - (list 10, 25)
say (list 10, 20) / (list 2, 5)
say (list 10, 7) % (list 3, 4)

seq = list 1, 1
loop range 1 3
    next = seq.(count seq) + seq.((count seq) - 1)
    seq = append seq next
end
say seq
say append (list 1, 2) (list 3, 4)

say sum prices
say sum (list as number)
say avg prices

unsorted_nums = list 3, 1, 2
sorted_nums = sort unsorted_nums
say sorted_nums
say unsorted_nums

unsorted_words = list "c", "a", "b"
say sort unsorted_words

# Typecheck inference for vectorized arithmetic and loop over result
scaled = (list 1, 2) * 3
loop scaled
    say it
end
`
	var out bytes.Buffer
	if _, err := EvalProgram(happySrc, &out); err != nil {
		t.Fatalf("expected vector arithmetic program to succeed, got: %v", err)
	}
	want := "[25, 45, 70]\n[90, 75]\n[5, 4]\n[1, 3]\n[1, 1, 2, 3, 5]\n[1, 2, 3, 4]\n60\n0\n20\n[1, 2, 3]\n[3, 1, 2]\n[\"a\", \"b\", \"c\"]\n3\n6\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\ngot:\n%s\nwant:\n%s", out.String(), want)
	}

	negCases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "length mismatch",
			src:  "say (list 1, 2) + (list 3, 4, 5)\n",
			want: "List length mismatch",
		},
		{
			name: "division by zero in vector",
			src:  "say (list 10, 20) / (list 2, 0)\n",
			want: "Division by zero",
		},
		{
			name: "modulo by zero in broadcast",
			src:  "say (list 10, 20) % 0\n",
			want: "Modulo by zero",
		},
		{
			name: "plus on text lists",
			src:  "say (list \"a\") + (list \"b\")\n",
			want: "Use 'append xs ys'",
		},
		{
			name: "plus on list and text",
			src:  "say (list \"a\") + \"b\"\n",
			want: "Cannot use '+'",
		},
		{
			name: "append element type mismatch",
			src:  "say append (list 1, 2) \"bad\"\n",
			want: "Cannot append text into list of number",
		},
		{
			name: "append list type mismatch",
			src:  "say append (list 1, 2) (list \"a\", \"b\")\n",
			want: "Cannot append list of text into list of number",
		},
		{
			name: "avg on empty list",
			src:  "say avg (list as number)\n",
			want: "Cannot compute average of an empty list",
		},
		{
			name: "sum on text list",
			src:  "say sum (list \"a\", \"b\")\n",
			want: "sum requires a list of numbers",
		},
		{
			name: "sort on record list",
			src:  "def p\n  x = 0\nend\nxs = list as p with\n  x = 1\nend\nsay sort xs\n",
			want: "sort requires a homogeneous list of numbers or text",
		},
		{
			name: "unknown function push",
			src:  "say push (list 1, 2) 3\n",
			want: "SyntaxError",
		},
	}
	for _, tc := range negCases {
		if _, err := EvalProgram(tc.src, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected error containing %q, got: %v", tc.name, tc.want, err)
		}
	}
}

func TestDataDrivenTestsErrorBlocksAndFailureFormat(t *testing.T) {
	happySrc := `
def div_case
    a = 0
    b = 0
end

to safe_div a b -> a / b

test "data-driven rules and error blocks"
    a = 10
    b = 3
    a - b >= 0
    a + b <= 20
    safe_div 10 2 is not error
    safe_div 1 0 is error

    cases = list as div_case with
        a = 10, b = 2
        a = 1, b = 0
    end

    loop cases
        it.a >= 1
        it.b >= 0
    end

    safe_div 1 0 ->
        error.type is "RuntimeError"
        error.message is "Division by zero"
    end
end
`
	report, err := RunChecks(happySrc)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.TestBlocks != 1 || report.Failed != 0 || report.Passed != 10 {
		t.Fatalf("expected 1 test block, 10 passed / 0 failed, got blocks=%d passed=%d failed=%d:\n%+v",
			report.TestBlocks, report.Passed, report.Failed, report.Results)
	}

	// Verify exact FAIL formatting with block context chain, variables, `it`, and soft assertions.
	failSrc := `def div_case
    a = 0
    b = 0
end

to safe_div a b
    # native division by zero
    -> a / b
end

test "Calculator rules"
    a = 150
    b = 170
    a + b <= 299
    a + b is 320
end

test "Error handling"
    cases = list as div_case with
        a = 1, b = 0
    end

    loop cases
        safe_div it.a it.b ->
            error.type is "RuntimeError"
            error.message is "Zero division"
        end
    end
end
`
	failReport, err := RunChecks(failSrc)
	if err != nil {
		t.Fatalf("RunChecks(failSrc) failed: %v", err)
	}
	if failReport.TestBlocks != 2 || failReport.Passed != 2 || failReport.Failed != 2 {
		t.Fatalf("expected 2 blocks, 2 passed / 2 failed (soft asserts), got blocks=%d passed=%d failed=%d",
			failReport.TestBlocks, failReport.Passed, failReport.Failed)
	}

	var failedOutputs []string
	for _, r := range failReport.Results {
		if !r.Passed {
			failedOutputs = append(failedOutputs, FormatFailure(r, "tests/discount.ease", failSrc))
		}
	}
	if len(failedOutputs) != 2 {
		t.Fatalf("expected 2 formatted failures, got %d", len(failedOutputs))
	}

	wantFirst := "FAIL  test \"Calculator rules\" (tests/discount.ease:14)\n" +
		"  11 | test \"Calculator rules\"\n" +
		"   . |     ...\n" +
		"  14 |     a + b <= 299\n" +
		"     |     -> a = 150, b = 170 (320 <= 299)\n"
	if failedOutputs[0] != wantFirst {
		t.Fatalf("first failure format mismatch:\ngot:\n%s\nwant:\n%s", failedOutputs[0], wantFirst)
	}

	wantSecond := "FAIL  test \"Error handling\" (tests/discount.ease:26)\n" +
		"  18 | test \"Error handling\"\n" +
		"   . |     ...\n" +
		"  23 |     loop cases\n" +
		"  24 |         safe_div it.a it.b ->\n" +
		"   . |             ...\n" +
		"  26 |             error.message is \"Zero division\"\n" +
		"     |             -> error.message = \"Division by zero\", it = div_case(a: 1, b: 0)\n"
	if failedOutputs[1] != wantSecond {
		t.Fatalf("second failure format mismatch:\ngot:\n%s\nwant:\n%s", failedOutputs[1], wantSecond)
	}

	// Negative syntax & typecheck cases
	negCases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "trailing raises expression",
			src:  "test \"x\"\n    1 / 0 raises \"Division by zero\"\nend\n",
			want: "SyntaxError",
		},
		{
			name: "legacy top-level check label",
			src:  "check \"math\"\n    1 is 1\nend\n",
			want: "Top-level 'check \"...\"' test blocks were removed",
		},
		{
			name: "error outside error block",
			src:  "test \"x\"\n    say error.type\nend\n",
			want: "'error' can only be used inside an 'expr -> ... end' error block",
		},
	}
	for _, tc := range negCases {
		if _, err := ParseProgram(tc.src); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected error containing %q, got: %v", tc.name, tc.want, err)
		}
	}

	// Error block when expression succeeds must fail.
	noErrSrc := "test \"no err\"\n    10 / 2 ->\n        error.type is \"RuntimeError\"\n    end\nend\n"
	noErrRep, err := RunChecks(noErrSrc)
	if err != nil || noErrRep.Failed != 1 {
		t.Fatalf("expected 1 failed check when error block succeeds, got rep=%+v err=%v", noErrRep, err)
	}
}

func TestWhereFilterAndBatchWithIt(t *testing.T) {
	src := `
def order
    id     = 0
    status = "open" | "paid"
    amount = 0
end

def ErrorMessage
    code    = 500
    message = "Unknown"
end

def outcome = "OK" | ErrorMessage

def particle
    x  = 0
    vx = 0
end

test "where filters and batch-with using it"
    orders = list as order with
        id = 1, status = "open", amount = 120
        id = 2, status = "paid", amount = 40
        id = 3, status = "paid", amount = 80
        id = 4, status = "paid", amount = 150
    end

    expected_orders = list as order with
        id = 3, status = "paid", amount = 80
        id = 4, status = "paid", amount = 150
    end

    # Single-line where inside test assertion (no parens needed around where-clause!)
    orders where it.status is "paid" and it.amount > 50 is expected_orders

    # Multi-line where block with it.index
    tail_paid = orders where
        it.status is "paid"
        it.index >= 4
    end
    count tail_paid is 1
    tail_paid.1.id is 4

    # Union narrowing inside where
    e1 = ErrorMessage with code = 404, message = "Not Found"
    e2 = ErrorMessage with code = 503, message = "Unavailable"
    items = list as outcome
    items = append items "OK"
    items = append items e1
    items = append items e2

    server_errs = items where it is ErrorMessage and it.code >= 500
    count server_errs is 1
    server_errs.1 is e2

    # Batch-with using it and it.index
    cloud = list as particle with
        x = 10, vx = 5
        x = 20, vx = -3
    end
    moved = cloud with x = it.x + it.vx
    moved.1.x is 15
    moved.2.x is 17
    cloud.1.x is 10

    indexed = cloud with x = it.index * 100
    indexed.1.x is 100
    indexed.2.x is 200
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 10 {
		t.Fatalf("expected 10 passed / 0 failed checks, got %d passed / %d failed:\n%+v", report.Passed, report.Failed, report.Results)
	}

	negCases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "where condition not boolean",
			src:  "say (list 1, 2) where it + 1\n",
			want: "'where' requires a condition (e.g. 'x > 0' or 'x is ...'), got number",
		},
		{
			name: "where on non-list",
			src:  "say 42 where it > 0\n",
			want: "Cannot use 'where' on",
		},
		{
			name: "batch-with without it prefix fails",
			src:  "def particle\n  x = 0\n  vx = 0\nend\ncloud = list as particle with\n  x = 1, vx = 2\nend\nmoved = cloud with x = x + vx\n",
			want: "Unknown variable \"x\"",
		},
	}
	for _, tc := range negCases {
		if _, err := EvalProgram(tc.src, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected error containing %q, got: %v", tc.name, tc.want, err)
		}
	}
}

func TestStrictPlusOperator(t *testing.T) {
	// Happy path: number + number, text + text (NFC normalized), numeric_list + numeric_list / number
	happySrc := `say "hello " + "world"
say 10 + 32
say "a" + "\u0308" is "ä"
`
	// Note: conditions cannot be passed to `say`, so test `"a" + "\u0308" is "ä"` in a `test` block.
	_ = happySrc
	src := `def user
  name = "Ada"
end

to bad_add a b
  a + b
end

say "hello " + "world"
say 10 + 32
say "a" + "b"

test "strict + operator happy and negative cases"
  ("hello " + "world") is "hello world"
  (10 + 32) is 42
  ((list 1, 2) + (list 3, 4)) is (list 4, 6)
  ("41" + 1) is error
  (1 + "41") is error
  ("user: " + user) is error
  ("items: " + (list 1, 2)) is error
  ((list "a") + "b") is error
  (bad_add "41" 1) is error
  ("41" + 1) ->
    error.type is "TypeError"
    error.message is "Cannot add text and number with '+'"
  end
  (1 + "41") ->
    error.type is "TypeError"
    error.message is "Cannot add number and text with '+'"
  end
end
`
	var out bytes.Buffer
	if _, err := EvalProgram(src, &out); err != nil {
		t.Fatalf("EvalProgram failed: %v", err)
	}
	if out.String() != "hello world\n42\nab\n" {
		t.Fatalf("unexpected output: %q", out.String())
	}

	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 13 {
		t.Fatalf("expected 13 passed / 0 failed checks, got %d passed / %d failed:\n%+v", report.Passed, report.Failed, report.Results)
	}

	// Static TypeError checks outside `is error`
	for _, bad := range []struct {
		code string
		want string
	}{
		{`x = "41" + 1`, `Cannot add text and number with '+'`},
		{`x = 1 + "41"`, `Cannot add number and text with '+'`},
		{"def user\n  name = \"Ada\"\nend\nx = \"user: \" + user\n", `Cannot add text and user with '+'`},
		{`x = "items: " + (list 1, 2)`, `Cannot add text and list with '+'`},
	} {
		if _, err := ParseProgram(bad.code); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Fatalf("expected static TypeError %q for %q, got: %v", bad.want, bad.code, err)
		}
	}

	// Runtime TypeError for dynamically typed parameters
	if _, err := EvalProgram("to f a b -> a + b\nx = f \"41\" 1\n", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot add text and number with '+'") {
		t.Fatalf("expected runtime TypeError for dynamic mixed '+', got: %v", err)
	}
}

func TestBuiltinsFloorRoundUpperLowerSliceAndDidYouMean(t *testing.T) {
	src := `nums = list 10, 20, 30, 40
txt = "äöü€!"

test "math, text, and slice built-ins"
  (floor 3.9) is 3
  (floor -1.2) is -2
  (round 3.5) is 4
  (round 3.4) is 3
  (upper "hello äöü") is "HELLO ÄÖÜ"
  (lower "HELLO ÄÖÜ") is "hello äöü"
  (slice nums 1 3) is (list 10, 20, 30)
  (slice nums 1 1) is (list 10)
  (slice nums -5 2) is (list 10, 20)
  (slice nums 2 99) is (list 20, 30, 40)
  (slice nums 3 1) is (list 0) where it > 0
  (slice nums 0 0) is (list 0) where it > 0
  (slice nums 5 9) is (list 0) where it > 0
  (slice txt 1 4) is "äöü€"
  (slice txt 1 1) is "ä"
  (slice txt -1 2) is "äö"
  (slice txt 3 10) is "ü€!"
  (slice "" 1 3) is ""
  (slice "abc" 1.5 2) is error
  (floor "x") is error
  (upper 123) is error
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 21 {
		t.Fatalf("expected 21 passed / 0 failed checks, got %d passed / %d failed:\n%+v", report.Passed, report.Failed, report.Results)
	}

	// Did you mean hints for variables, functions, and record fields
	if _, err := EvalProgram("user = \"Ada\"\nsay usr\n", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `Did you mean "user"?`) {
		t.Fatalf("expected Did you mean hint for variable, got: %v", err)
	}
	if _, err := EvalProgram("say cunt\n", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `Did you mean "count"?`) {
		t.Fatalf("expected Did you mean hint for function, got: %v", err)
	}
	if _, err := EvalProgram("def player\n  speed = 10\nend\np = player with speed = 5\nsay p.speeed\n", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `Did you mean "speed"?`) {
		t.Fatalf("expected Did you mean hint for field, got: %v", err)
	}

	// Recursion limit message and hint
	recSrc := "to down n\n  check n <= 0 -> 0\n  -> down (n - 1)\nend\nsay down 300\n"
	if _, err := EvalProgram(recSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Maximum recursion depth (256) exceeded") || !strings.Contains(err.Error(), "use a 'loop' over 'range'") {
		t.Fatalf("expected recursion limit (256) message and loop hint, got: %v", err)
	}

	// Standard module encapsulation, `as <alias>`, and migration hints
	aliasSrc := "import network as net\napi = net.network with url = \"http://127.0.0.1:1\", timeout = 1\nsay net:fetch api \"/x\"\n"
	var aliasOut bytes.Buffer
	if _, err := EvalProgram(aliasSrc, &aliasOut); err != nil {
		t.Fatalf("expected import network as net to succeed, got: %v", err)
	}
	if _, err := ParseProgram("import network\nx = network with url = \"http://127.0.0.1:1\"\n"); err == nil || !strings.Contains(err.Error(), "Use 'network.network with ...'") {
		t.Fatalf("expected migration hint for 'network with ...', got: %v", err)
	}
	if _, err := EvalProgram("import network\nsay fetch\n", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Did you mean 'network:fetch'?") {
		t.Fatalf("expected migration hint for bare 'fetch', got: %v", err)
	}
}

func TestErgonomicParserHintsForMultilineArrowCheckAndLoopExpr(t *testing.T) {
	multilineCheck := "x = 1\ncheck x > 0 ->\n  say x\nend\n"
	if _, err := ParseProgram(multilineCheck); err == nil ||
		!strings.Contains(err.Error(), "Single-line 'check <cond> -> <value | end>' cannot span multiple lines") ||
		!strings.Contains(err.Error(), "Start the value expression on the same line right after '->'") {
		t.Fatalf("expected multiline single-line check hint, got: %v", err)
	}

	loopExpr := "xs = loop range 1 3\n  it * 2\nend\n"
	if _, err := ParseProgram(loopExpr); err == nil ||
		!strings.Contains(err.Error(), "'loop' is a statement, not an expression") ||
		!strings.Contains(err.Error(), "xs = append xs val") {
		t.Fatalf("expected loop-as-expression hint, got: %v", err)
	}
}

func TestNotTypecheckAndCanonicalAsConversions(t *testing.T) {
	// Static TypeError for `not <non-condition>`
	for _, bad := range []string{
		"check not 42\n  say 1\nend\n",
		"check not \"hi\"\n  say 1\nend\n",
		"x = 1\ncheck not x\n  say 1\nend\n",
		"check not (count (list 1, 2))\n  say 1\nend\n",
	} {
		if _, err := ParseProgram(bad); err == nil || !strings.Contains(err.Error(), "'not' requires a condition") {
			t.Fatalf("expected static TypeError for %q, got: %v", bad, err)
		}
	}

	// Runtime error test inside `test` block + canonical `as text` / `as number`
	src := `test "not condition check and canonical conversions"
  (42 as text) is "42"
  ("42" as number) is 42
  ("abc" as number) is error
  (not 42) is error
  (not 42) ->
    error.type is "TypeError"
    error.message is "'not' requires a condition, got number"
  end
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 6 {
		t.Fatalf("expected 6 passed / 0 failed checks, got %d passed / %d failed:\n%+v", report.Passed, report.Failed, report.Results)
	}

	// `str` and `num` built-ins are removed
	for _, oldBuiltin := range []string{"say str 42\n", "say num \"42\"\n"} {
		if _, err := EvalProgram(oldBuiltin, &bytes.Buffer{}); err == nil {
			t.Fatalf("expected error for removed built-in in %q", oldBuiltin)
		}
	}
}

func TestInfiniteLoopWithExitCondition(t *testing.T) {
	// Happy path: command loop reading stdin with `it.index` and exiting on `"quit"`
	cmdSrc := `loop
  cmd = ask "> "
  check cmd is "quit" -> end
  say "{it.index}:{cmd}"
end
say "bye"
`
	var out bytes.Buffer
	if _, err := EvalProgramWithInput(cmdSrc, strings.NewReader("look\nfight\nquit\n"), &out); err != nil {
		t.Fatalf("EvalProgramWithInput failed: %v", err)
	}
	if out.String() != "> 1:look\n> 2:fight\n> bye\n" {
		t.Fatalf("unexpected output: %q", out.String())
	}

	// Infinite loop inside a `to` function tested from a `test` block (early return exit)
	fnSrc := `to find_first_over limit
  n = 1
  loop
    check n > limit -> n
    n = n + it.index
  end
end

test "infinite loop inside function"
  find_first_over 5 is 7
end
`
	report, err := RunChecks(fnSrc)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 1 {
		t.Fatalf("expected 1 passed / 0 failed checks, got %d passed / %d failed", report.Passed, report.Failed)
	}

	// Negative test: infinite loop with no exit condition
	if _, err := ParseProgram("loop\n  say it.index\nend\n"); err == nil ||
		!strings.Contains(err.Error(), "Infinite 'loop' has no exit condition") ||
		!strings.Contains(err.Error(), "check <condition> -> end") {
		t.Fatalf("expected infinite loop exit condition TypeError, got: %v", err)
	}
}

func TestHalfValueReturningFunctionRejected(t *testing.T) {
	badLoop := `
to find_even xs
  loop xs
    check it % 2 is 0 -> it
    say it
  end
end
`
	_, err := ParseProgram(badLoop)
	if err == nil {
		t.Fatalf("expected TypeError for half-value-returning function, got nil")
	}
	if !strings.Contains(err.Error(), "returns a value on some paths via '->' but can finish without returning a value") {
		t.Fatalf("unexpected error message: %v", err)
	}
	if !strings.Contains(err.Error(), "Ensure every execution path in the function ends with '-> <value>'") {
		t.Fatalf("expected hint about '-> <value>', got: %v", err)
	}

	badTake := `
to drain_until_zero ch
  take ch
    check it is 0 -> it
    say it
  end
end
`
	if _, err := ParseProgram(badTake); err == nil || !strings.Contains(err.Error(), "can finish without returning a value") {
		t.Fatalf("expected TypeError for take fallthrough, got: %v", err)
	}

	okEndExit := `
to print_until_zero xs
  loop xs
    check it is 0 -> end
    say it
  end
end
`
	if _, err := ParseProgram(okEndExit); err != nil {
		t.Fatalf("expected '-> end' loop exit to pass, got: %v", err)
	}

	okConsistentReturn := `
to find_even_or_zero xs
  loop xs
    check it % 2 is 0 -> it
  end
  -> 0
end
`
	if _, err := ParseProgram(okConsistentReturn); err != nil {
		t.Fatalf("expected consistent return at end to pass, got: %v", err)
	}
}

func TestKeywordFieldNamesNumberTextList(t *testing.T) {
	src := `
def Seat
    number = 1
    state  = "free" | "occupied"
end

def Message
    text = ""
end

def Todo
    list = list as number
end

s = Seat with number = 4, state = "occupied"
m = Message with text = "hello"
td = Todo with list = list 10, 20

raw_seat = '{"number": 7, "state": "free"}' as json
decoded_seat = raw_seat as Seat
raw_msg = '{"text": "from json"}' as json
decoded_msg = raw_msg as Message

test "number, text, list work as record field names"
    s.number is 4
    s.state is "occupied"
    m.text is "hello"
    td.list is list 10, 20
    decoded_seat.number is 7
    decoded_seat.state is "free"
    decoded_msg.text is "from json"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 7 || report.Failed != 0 {
		t.Fatalf("expected 7 passed / 0 failed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}

	// Variables named `number` or `text` must remain rejected.
	for _, bad := range []string{"number = 5\n", "text = \"hi\"\n"} {
		if _, err := ParseProgram(bad); err == nil {
			t.Fatalf("expected parse error for reserved type name as variable %q, got nil", bad)
		}
	}
}

func TestCheckEffectBlockAndUnionNarrowingInTestBlocks(t *testing.T) {
	src := `
def CinemaHall
    rows = list as number
end

def BookingRejected
    reason = ""
end

def BookingResult = CinemaHall | BookingRejected

to book_seat ok
    check ok is 1 -> CinemaHall with rows = list 10, 20
    -> BookingRejected with reason = "full"
end

to first_row hall
    -> hall.rows.1
end

test "check effect blocks and union narrowing in test blocks"
    r1 = book_seat 1
    r1 is CinemaHall
    first_row r1 is 10
    r1.rows.2 is 20

    r2 = book_seat 0
    check r2 is BookingRejected
        r2.reason is "full"
    else
        1 is 0
    end
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 4 || report.Failed != 0 {
		t.Fatalf("expected 4 passed / 0 failed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}

	// Single-line `check` or verdict table inside `test` must report a clear SyntaxError.
	wantMsg := "Single-line 'check' is not used in 'test'"
	for _, bad := range []string{
		"test \"bad\"\n  check 1 is 1 -> 2\nend\n",
		"test \"bad\"\n  check\n    1 is 1 -> 2\n  end\nend\n",
	} {
		if _, err := ParseProgram(bad); err == nil || !strings.Contains(err.Error(), wantMsg) {
			t.Fatalf("expected error containing %q for %q, got: %v", wantMsg, bad, err)
		}
	}
}

func TestElseAfterSingleLineCheckAndContinueHints(t *testing.T) {
	elseSrc := "to f x\n  check x > 0 -> 1\n  else\n    -> 0\n  end\nend\n"
	wantElseHint := "Single-line 'check <cond> -> <val>' cannot be followed by 'else'"
	if _, err := ParseProgram(elseSrc); err == nil || !strings.Contains(err.Error(), wantElseHint) {
		t.Fatalf("expected hint %q, got: %v", wantElseHint, err)
	}

	contSrc := "loop range 1 3\n  check it is 2\n    continue\n  end\n  say it\nend\n"
	wantContHint := "Ease has no 'continue' keyword; wrap the remaining loop body in 'check <inverted_cond> ... end' instead."
	if _, err := ParseProgram(contSrc); err == nil || !strings.Contains(err.Error(), wantContHint) {
		t.Fatalf("expected hint %q, got: %v", wantContHint, err)
	}
}
