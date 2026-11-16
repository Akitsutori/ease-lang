package ease

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
  repeat 4
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

func TestExplicitAsCast(t *testing.T) {
	src := `
user_age = ask "Age?" as number
say "next={user_age + 1}"
say 42 as text + "!"
`
	var out1 bytes.Buffer
	if _, err := EvalProgramWithInput(src, strings.NewReader("25\n"), &out1); err != nil {
		t.Fatalf("numeric input failed: %v", err)
	}
	if out1.String() != "Age? next=26\n42!\n" {
		t.Fatalf("unexpected numeric output: %q", out1.String())
	}

	var out2 bytes.Buffer
	_, err := EvalProgramWithInput(src, strings.NewReader("abc\n"), &out2)
	if err == nil {
		t.Fatalf("expected error on non-numeric input with strict 'as number', got output: %q", out2.String())
	}
	if !strings.Contains(err.Error(), `Cannot convert text "abc" to number`) {
		t.Fatalf("unexpected error message for 'as number': %v", err)
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
for cloud do
    sum_x = sum_x + x
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

func TestReferenceParticlesAndNetwork(t *testing.T) {
	// Based on 04_particles.ease (unified ease syntax)
	particlesSrc := `
def particle
    x = 0
    vx = 10
    life = 100
    state = "active" | "dead"
end

to step_particle p dt
    next_life = p.life - 10 * dt
    next_x = p.x + p.vx * dt
    check next_life <= 0 -> p with x = next_x, life = 0, state = "dead"
    p with x = next_x, life = next_life
end

to update_particles items dt
    out = list as particle
    for items do
        check state is "dead" -> out = do push out it
        check state is "active" -> out = do push out do step_particle it dt
    end
    out
end

test "04_particles reference"
    cloud = list as particle with
        x = 0, vx = 5, life = 20
        x = 10, vx = 2, life = 5
    end

    stepped = do update_particles cloud 1

    do len cloud is 2
    do len stepped is 2
    stepped.0.x is 5
    stepped.0.vx is 5
    stepped.0.life is 10
    stepped.0.state is "active"
    stepped.1.x is 12
    stepped.1.life is 0
    stepped.1.state is "dead"
end
`
	pReport, err := RunChecks(particlesSrc)
	if err != nil {
		t.Fatalf("04_particles RunChecks failed: %v", err)
	}
	if pReport.Passed != 9 || pReport.Failed != 0 {
		t.Fatalf("04_particles checks mismatch: %+v", pReport)
	}

	// Based on 05_network.ease (Parse, don't validate + check guards)
	networkSrc := `
def event
    kind = "click" | "key" | "unknown"
    x    = 0
    y    = 0
    key  = ""
end

to describe_event ev
    check
        ev.kind is "click" -> "click@{ev.x},{ev.y}"
        ev.kind is "key"   -> "key:{ev.key}"
    end
    "unknown"
end

test "05_network reference"
    events = list as event with
        kind = "click", x = 120, y = 240
        kind = "key", key = "Enter"
        kind = "unknown"
    end

    do len events is 3
    events.0.kind is "click"
    events.1.key is "Enter"
    do describe_event events.0 is "click@120,240"
    do describe_event events.1 is "key:Enter"
    do describe_event events.2 is "unknown"
end
`
	nReport, err := RunChecks(networkSrc)
	if err != nil {
		t.Fatalf("05_network RunChecks failed: %v", err)
	}
	if nReport.Passed != 6 || nReport.Failed != 0 {
		t.Fatalf("05_network checks mismatch: %+v", nReport)
	}
}

func TestStdNetworkImportAndHTTPCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/age/ok":
			_, _ = w.Write([]byte("25"))
		case r.Method == http.MethodGet && r.URL.Path == "/age/negative":
			_, _ = w.Write([]byte("-5"))
		case r.Method == http.MethodGet && r.URL.Path == "/age/bad":
			_, _ = w.Write([]byte("abc"))
		case r.Method == http.MethodGet && r.URL.Path == "/fail":
			http.Error(w, "server error", http.StatusInternalServerError)
		case r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			_, _ = w.Write([]byte("echo:" + string(body)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := fmt.Sprintf(`
import network

to parse_age raw
    raw as number
end

to validate_age age
    check
        age is not number -> "Ungültiges Zahlenformat"
        age < 0           -> "Alter kann nicht negativ sein"
        age > 130         -> "Alter ist unrealistisch"
    end
    age
end

test "std/network real HTTP GET/POST & Form/Inhalt"
    api = network with
        url     = %q
        timeout = 2
        token   = "secret-123"
    end

    # GET via do ask
    raw_ok  = do ask api "/age/ok"
    raw_neg = do ask api "/age/negative"
    raw_bad = do ask api "/age/bad"
    raw_err = do ask api "/fail"

    raw_ok is "25"
    raw_err is ""

    # Typ-Konformität (as) vs. Inhaltsvalidität (to + check)
    do parse_age raw_ok is 25
    do parse_age raw_neg is -5
    do parse_age raw_bad raises "Cannot convert text"
    do validate_age do parse_age raw_ok is 25
    do validate_age do parse_age raw_neg is "Alter kann nicht negativ sein"

    # POST via do send
    posted = do send api "/submit" 42
    posted is "echo:42"
end
`, srv.URL)

	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 8 || report.Failed != 0 {
		t.Fatalf("expected 8 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}
}

func TestRecordJSONCastAndValidation(t *testing.T) {
	src := `
def api_user
    id   = 0
    name = "Gast"
    role = "member" | "admin"
end

test "Record <-> JSON via decode_json / encode_json and strict as"
    # Valid JSON with single-quoted raw string, unknown field tolerated, all def fields present
    raw_map = do decode_json '{"id": 7, "name": "Aria", "role": "admin", "extra": true}'
    raw_map is not text
    do type raw_map is "map"
    raw_map.id is 7

    u = raw_map as api_user
    u is api_user
    u.id is 7
    u.name is "Aria"
    u.role is "admin"

    # Explicit JSON encoding & roundtrip
    encoded = do encode_json u
    decoded = do decode_json encoded as api_user
    decoded is api_user
    decoded.id is 7
    decoded.name is "Aria"
    decoded.role is "admin"

    # Defaults remain exclusively for 'with' construction
    with_defaults = api_user with id = 9
    with_defaults.name is "Gast"
    with_defaults.role is "member"

    # Raw text directly passed to 'as <def>' is rejected immediately
    '{"id": 7, "name": "Aria", "role": "admin"}' as api_user raises "Cannot validate text as api_user"
    "" as api_user raises "Cannot validate text as api_user"

    # Missing required fields in 'as <def>' (no silent default filling!)
    (do decode_json '{"id": 7, "role": "admin"}') as api_user raises "Missing field 'api_user.name'"

    # Invalid JSON / non-map decoded values / wrong types / invalid union -> immediate error
    do decode_json "" raises "empty text"
    do decode_json '{broken' raises "Invalid JSON"
    do decode_json 42 raises "requires text"
    (do decode_json 'null') as api_user raises "Cannot validate none as api_user"
    (do decode_json '[1, 2]') as api_user raises "Cannot validate list as api_user"
    (do decode_json '{"id": "not-a-num", "name": "Aria", "role": "admin"}') as api_user raises "api_user.id"
    (do decode_json '{"id": 7, "name": "Aria", "role": "superuser"}') as api_user raises "api_user.role"
    42 as api_user raises "expected structured map or api_user record"

    # Symmetry: no JSON magic in 'as text'
    u as text raises "Cannot convert api_user to text"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 25 || report.Failed != 0 {
		t.Fatalf("expected 25 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}
}

func TestAsCastBindsToWholeDoCall(t *testing.T) {
	src := `
def my_record
    first  = ""
    second = ""
end

to make_pair a b
    do decode_json "\{\"first\": \"{a}\", \"second\": \"{b}\"\}"
end

x = do make_pair "a" "b" as my_record
`
	prog, err := ParseProgram(src)
	if err != nil {
		t.Fatalf("ParseProgram failed: %v", err)
	}
	assign, ok := prog.Body[0].(*AssignStmt)
	if !ok {
		t.Fatalf("expected AssignStmt, got %T", prog.Body[0])
	}
	cast, ok := assign.Value.(*CastExpr)
	if !ok {
		t.Fatalf("expected outer CastExpr wrapping do-call, got %T", assign.Value)
	}
	call, ok := cast.Target.(*CallExpr)
	if !ok || call.Callee != "make_pair" || len(call.Args) != 2 {
		t.Fatalf("expected CastExpr.Target to be CallExpr(make_pair, 2 args), got %#v", cast.Target)
	}

	evalSrc := src + `
test "as binds to whole do-call"
    x is my_record
    x.first is "a"
    x.second is "b"
end
`
	report, err := RunChecks(evalSrc)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 3 || report.Failed != 0 {
		t.Fatalf("expected 3 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
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

    for do range 1 10 do
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

func TestSingleLineForExpressions(t *testing.T) {
	src := `
for do range 1 10 do say it end
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
for x in do range 1 4 do sum = sum + x end

test "single-line for"
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
for x in range 1 3 do sum = sum + x end

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

func TestStrictAsRejectsUnknownTypes(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantSub  string
		wantLine int
	}{
		{
			name:     "unknown unicode ident",
			src:      "import network\nx = \"ok\" as ö\n",
			wantSub:  `Unknown type "ö" after 'as'`,
			wantLine: 2,
		},
		{
			name:     "typo suggestion",
			src:      "def api_user\n    id = 0\nend\nx = \"\\{\\}\" as api_usre\n",
			wantSub:  `Did you mean "api_user"?`,
			wantLine: 4,
		},
		{
			name:     "string alias hint",
			src:      "x = 42 as string\n",
			wantSub:  "Did you mean 'text'?",
			wantLine: 1,
		},
		{
			name:     "constant is not a blueprint",
			src:      "def pi = 3.14\nx = \"3.14\" as pi\n",
			wantSub:  `Constant "pi" (defined on line 1) is not a type or 'def' blueprint`,
			wantLine: 2,
		},
		{
			name:     "forward declared def rejected",
			src:      "x = \"\\{\\}\" as late_user\ndef late_user\n    id = 0\nend\n",
			wantSub:  `Type "late_user" is used after 'as' before it is defined (defined on line 2)`,
			wantLine: 1,
		},
		{
			name:     "bool is not a built-in type",
			src:      "x = \"true\" as bool\n",
			wantSub:  `Boolean ("bool") is not a built-in type in Ease`,
			wantLine: 1,
		},
		{
			name:     "boolean is not a built-in type",
			src:      "x = \"true\" as boolean\n",
			wantSub:  `Boolean ("boolean") is not a built-in type in Ease`,
			wantLine: 1,
		},
		{
			name:     "unknown type in list as",
			src:      "xs = list as unknown_item\n",
			wantSub:  `Unknown type "unknown_item" after 'list as'`,
			wantLine: 1,
		},
		{
			name:     "bool not allowed in list as",
			src:      "xs = list as bool\n",
			wantSub:  `Boolean ("bool") is not a built-in type in Ease`,
			wantLine: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil {
				t.Fatalf("expected parse error for %s, got nil", tc.name)
			}
			var easeErr *EaseError
			if !errors.As(err, &easeErr) {
				t.Fatalf("expected *EaseError, got %T: %v", err, err)
			}
			if easeErr.Line != tc.wantLine {
				t.Fatalf("expected line %d, got %d (%v)", tc.wantLine, easeErr.Line, err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected %q in error:\n%v", tc.wantSub, err)
			}
		})
	}

	// Valid cases: built-in types (text, number), prior def, imported std module blueprint, and union def.
	validSrc := `
import network

def custom_user
    name = ""
end

def ticket = "open" | "closed"

a = 42 as text
b = "12" as number
d = "open" as ticket
e = (do decode_json '{"name": "Ada"}') as custom_user
f = (do decode_json '{"status": 200, "body": "ok", "error": ""}') as http_response
g = list as number
`
	if _, err := ParseProgram(validSrc); err != nil {
		t.Fatalf("expected valid strict 'as' usages to parse cleanly, got: %v", err)
	}
}

func TestStrictAsRuntimeErrors(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "invalid number text",
			src:     `x = "not-a-num" as number`,
			wantSub: `Cannot convert text "not-a-num" to number`,
		},
		{
			name:    "disallowed union value",
			src:     "def ticket = \"open\" | \"closed\"\nx = \"pending\" as ticket\n",
			wantSub: `Value pending is not valid for union "ticket"`,
		},
		{
			name:    "raw text rejected for record def",
			src:     "def user\n    id = 0\nend\nx = '{\"id\": 1}' as user\n",
			wantSub: `Cannot validate text as user: expected structured map or user record`,
		},
		{
			name:    "json array rejected for record def",
			src:     "def user\n    id = 0\nend\nx = (do decode_json '[1, 2]') as user\n",
			wantSub: `Cannot validate list as user: expected structured map or user record`,
		},
		{
			name:    "missing required field rejected for record def",
			src:     "def user\n    id = 0\n    name = \"Gast\"\nend\nx = (do decode_json '{\"id\": 1}') as user\n",
			wantSub: `Missing field 'user.name'`,
		},
		{
			name:    "wrong nested field type",
			src:     "def user\n    id = 0\nend\nx = (do decode_json '{\"id\": true}') as user\n",
			wantSub: `Field 'user.id' expected number, got condition`,
		},
		{
			name:    "record as text rejected",
			src:     "def hero\n    hp = 100\nend\nx = hero as text\n",
			wantSub: `Cannot convert hero to text`,
		},
		{
			name:    "record string interpolation rejected",
			src:     "def hero\n    hp = 100\nend\nsay \"{hero}\"\n",
			wantSub: `Cannot interpolate hero into text`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := EvalProgram(tc.src, &out)
			if err == nil {
				t.Fatalf("expected runtime error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected %q in error:\n%v", tc.wantSub, err)
			}
		})
	}
}

func TestMinusRuleAtomicLiteralVsSubtraction(t *testing.T) {
	src := `
to classify n
    if n > 0
        "positive"
    else
        "non-positive"
    end
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

func TestUnifiedHomogeneousListValueSemantics(t *testing.T) {
	// Rule 1: Every runtime list is *ListValue with inferred or explicit ElemType.
	val, err := EvalProgram("do range 1 3\n", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("range eval failed: %v", err)
	}
	rangeLV, ok := val.(*ListValue)
	if !ok || rangeLV.ElemType != "number" || len(rangeLV.Elements) != 3 {
		t.Fatalf("expected *ListValue{ElemType: number, len: 3}, got %T %+v", val, val)
	}

	val, err = EvalProgram("list 10, 20\n", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("list literal eval failed: %v", err)
	}
	litLV, ok := val.(*ListValue)
	if !ok || litLV.ElemType != "number" || len(litLV.Elements) != 2 {
		t.Fatalf("expected *ListValue{ElemType: number, len: 2}, got %T %+v", val, val)
	}

	val, err = EvalProgram("def p\n  x = 1\nend\ninst = p with x = 2\ndo fields inst\n", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("fields eval failed: %v", err)
	}
	fieldsLV, ok := val.(*ListValue)
	if !ok || fieldsLV.ElemType != "text" || len(fieldsLV.Elements) != 1 {
		t.Fatalf("expected *ListValue{ElemType: text, len: 1}, got %T %+v", val, val)
	}

	val, err = EvalProgram(`do decode_json '[1, 2]'`+"\n", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("decode_json array failed: %v", err)
	}
	jsonLV, ok := val.(*ListValue)
	if !ok || jsonLV.ElemType != "number" || len(jsonLV.Elements) != 2 {
		t.Fatalf("expected *ListValue{ElemType: number, len: 2} from decode_json, got %T %+v", val, val)
	}

	val, err = EvalProgram(`do decode_json '[]'`+"\n", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("decode_json empty array failed: %v", err)
	}
	emptyJSONLV, ok := val.(*ListValue)
	if !ok || emptyJSONLV.ElemType != "" || len(emptyJSONLV.Elements) != 0 {
		t.Fatalf("expected *ListValue{ElemType: \"\", len: 0} from empty decode_json, got %T %+v", val, val)
	}

	// Rule 1 & Rule 2 & Rule 3 & Rule 4 & Rule 5 via Ease source + checks.
	src := `
def user
    id   = 0
    name = "Gast"
end

def team
    title  = ""
    scores = list as number
end

def particle
    x     = 0
    vx    = 10
    life  = 100
    state = "active" | "dead"
end

def ticket = "vip" | "standard"

test "Rule 1 & 2: strict list types and 'as' validation"
    (list 1, 2) as number is list 1, 2
    (list "vip", "standard") as ticket is list "vip", "standard"
    (do decode_json '[1, 2, 3]') as number is list 1, 2, 3
    (do decode_json '["a", "b"]') as text is list "a", "b"
    users = (do decode_json '[{"id": 1, "name": "Ada"}, {"id": 2, "name": "Bob"}]') as user
    do len users is 2
    users.0 is user
    users.0.name is "Ada"
    users.1.id is 2

    t = (do decode_json '{"title": "Red", "scores": [10, 20]}') as team
    t is team
    t.scores is list 10, 20

    (list "a", "b") as number raises "Cannot convert list to number"
    (list 1, 2) as text raises "Cannot convert list to text"
    (list "vip", "gold") as ticket raises "not valid for union"
    (do decode_json '[1, "bad"]') as number raises "element 1 expected number, got text"
    (do decode_json '[{"id": 1, "name": "Ada"}, {"id": 2}]') as user raises "Missing field 'user.1.name'"
    (do decode_json '{"title": "Red", "scores": [10, "bad"]}') as team raises "Element 'team.scores.1' expected number"
end

test "Rule 3: homogeneous list concatenation with +"
    combined = (list 1, 2) + (list 3, 4)
    combined is list 1, 2, 3, 4
    combined2 = (list as number) + (list 5, 6)
    combined2 is list 5, 6
    combined3 = (list 7, 8) + (do decode_json '[]')
    combined3 is list 7, 8
    ((list 1, 2) + (list "a", "b")) raises "Cannot concatenate list of number and list of text"
    ((list as number) + (list as text)) raises "Cannot concatenate list of number and list of text"
end

test "Rule 4: batch-with on record lists"
    cloud = list as particle with
        x = 0, vx = 5, life = 20
        x = 10, vx = 2, life = 5
    end
    moved = cloud with x = x + vx
    do len moved is 2
    moved.0 is particle
    moved.0.x is 5
    moved.1.x is 12
    cloud.0.x is 0

    p1 = particle with x = 1, vx = 3
    p2 = particle with x = 4, vx = 6
    moved2 = (list p1, p2) with x = x + vx
    moved2.0.x is 4
    moved2.1.x is 10
    (moved2 + (list p1)).2.x is 1

    ((list 1, 2) with x = 1) raises "Cannot use 'with'"
end

test "Rule 5: push type lock and value semantics under aliasing"
    r = do range 1 1
    a = do push r 2
    b = do push a 3
    c = do push a 4
    a is list 1, 2
    b is list 1, 2, 3
    c is list 1, 2, 4

    dyn = do decode_json '[]'
    dyn_num = do push dyn 42
    dyn_num is list 42
    do len dyn is 0
    do push dyn_num "bad" raises "Cannot push text into list of number"
    bad_empty = list as number
    do push bad_empty "bad" raises "Cannot push text into list of number"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 37 {
		t.Fatalf("expected 37 passed / 0 failed checks, got %d passed / %d failed:\n%+v", report.Passed, report.Failed, report.Results)
	}

	// Verify TypeError stage explicitly for cast, +, and push failures.
	typeErrCases := []struct {
		name string
		src  string
	}{
		{"cast list mismatch", `x = (list "a", "b") as number`},
		{"cast mixed json array", `x = (do decode_json '[1, "bad"]') as number`},
		{"concat mismatch", `x = (list 1, 2) + (list "a", "b")`},
		{"push mismatch", "xs = list 1, 2\nys = do push xs \"x\""},
	}
	for _, tc := range typeErrCases {
		_, err := EvalProgram(tc.src+"\n", &bytes.Buffer{})
		var easeErr *EaseError
		if !errors.As(err, &easeErr) || easeErr.Stage != "TypeError" {
			t.Fatalf("%s: expected TypeError, got %v", tc.name, err)
		}
	}

	// Verify batch-with preserves ElemType ("particle") and O(1) push backing-array reuse vs fork copy.
	prog, err := ParseProgram(`
def particle
    x = 0
    vx = 10
end
cloud = list as particle with
    x = 1, vx = 2
end
moved = cloud with x = x + vx
`)
	if err != nil {
		t.Fatalf("ParseProgram failed: %v", err)
	}
	ev := NewEvaluator("", prog, &bytes.Buffer{})
	defer ev.Close()
	if _, err := ev.ExecStatements(prog.Body, ev.global); err != nil {
		t.Fatalf("ExecStatements failed: %v", err)
	}
	movedVal, ok := ev.global.Lookup("moved")
	movedLV, isLV := movedVal.(*ListValue)
	if !ok || !isLV || movedLV.ElemType != "particle" {
		t.Fatalf("expected moved to be *ListValue{ElemType: particle}, got %#v", movedVal)
	}

	// Verify amortized O(1) push: linear chain reuses spare capacity; branching copies without aliasing corruption.
	prealloc := make([]Value, 2, 8)
	prealloc[0] = 1.0
	prealloc[1] = 2.0
	base := &ListValue{ElemType: "number", Elements: prealloc}
	b1Val, handled, err := ev.callBuiltin("push", []Value{base, 3.0}, 1, 1)
	if !handled || err != nil {
		t.Fatalf("first push failed: handled=%v err=%v", handled, err)
	}
	b1 := b1Val.(*ListValue)
	if &b1.Elements[0] != &base.Elements[0] {
		t.Fatalf("expected linear first push to reuse spare slice capacity in O(1)")
	}
	b2Val, handled, err := ev.callBuiltin("push", []Value{base, 4.0}, 1, 1)
	if !handled || err != nil {
		t.Fatalf("second branched push failed: handled=%v err=%v", handled, err)
	}
	b2 := b2Val.(*ListValue)
	if &b2.Elements[0] == &b1.Elements[0] {
		t.Fatalf("expected branched push on already-pushed list to allocate a fresh backing array")
	}
	if b1.Elements[2] != 3.0 || b2.Elements[2] != 4.0 {
		t.Fatalf("value semantics corrupted: b1=%v b2=%v", b1.Elements, b2.Elements)
	}
}

func TestUnionTypesBuildingBlocksGuardrailsNarrowingAndExhaustiveness(t *testing.T) {
	// 1. User's canonical outcome example: ErrorMessage, 4 building blocks, narrowing, exhaustive check, else -> ...
	src := `
def ErrorMessage
    code    = 500
    message = "Unknown error"
end

def status = "OK" | "FEHLER"
def outcome = status | ErrorMessage
def tagged = number | text | ErrorMessage

def envelope
    res = outcome
end

to handle res
    check
        res is "OK"         -> say "Alles super!"
        res is "FEHLER"     -> say "Allgemeiner Fehler"
        res is ErrorMessage -> say "Fehler {res.code}: {res.message}"
    end
end

to process_response res
    check
        res is "OK"         -> "Erfolgreich verarbeitet"
        res is "FEHLER"     -> "Unbekannter Fehler"
        res is ErrorMessage -> "Fehler {res.code}: {res.message}"
    end
end

to log_only_success res
    check
        res is "OK" -> say "Hat geklappt"
        else        -> say "Irgendetwas anderes ist passiert"
    end
end

to inspect_if res
    if res is ErrorMessage
        "if:{res.code}:{res.message}"
    else
        "other"
    end
end

to unwrap_err_guard res
    check res is not ErrorMessage -> "not-error"
    "guard:{res.code}"
end

to server_error_only res
    check
        res is ErrorMessage and res.code >= 500 -> "server:{res.code}:{res.message}"
        else                                    -> "handled"
    end
end

err_rec = ErrorMessage with code = 404, message = "Not Found"
do handle "OK"
do handle "FEHLER"
do handle err_rec
do log_only_success "OK"
do log_only_success "FEHLER"
do log_only_success err_rec

test "orthogonal union building blocks, flattening, JSON as, narrowing, and else ->"
    "OK" is outcome
    "FEHLER" is outcome
    err_rec is outcome
    42 is tagged
    "hello" is tagged
    err_rec is tagged

    # Explicit JSON -> union conversion picks ErrorMessage
    raw_err = (do decode_json '{"code": 503, "message": "Unavailable"}') as outcome
    raw_err is ErrorMessage
    do process_response raw_err is "Fehler 503: Unavailable"
    do process_response "OK" is "Erfolgreich verarbeitet"
    do process_response "FEHLER" is "Unbekannter Fehler"

    # Record field typed by union accepts literal and ErrorMessage from JSON
    env1 = (do decode_json '{"res": "OK"}') as envelope
    env1.res is "OK"
    env2 = (do decode_json '{"res": {"code": 400, "message": "Bad"}}') as envelope
    env2.res is ErrorMessage
    do inspect_if env2.res is "if:400:Bad"
    do inspect_if "OK" is "other"

    # Heterogeneous union list + JSON array coercion
    items = list as outcome
    items = do push items "OK"
    items = do push items err_rec
    do len items is 2
    items.1 is ErrorMessage

    json_items = (do decode_json '["OK", {"code": 404, "message": "Missing"}]') as outcome
    json_items.0 is "OK"
    json_items.1 is ErrorMessage

    # Early-exit guard narrowing & compound 'and' narrowing
    do unwrap_err_guard err_rec is "guard:404"
    do unwrap_err_guard "OK" is "not-error"
    do server_error_only raw_err is "server:503:Unavailable"
    do server_error_only err_rec is "handled"

    # Reject incomplete map when decoding into union
    (do decode_json '{"code": 1}') as outcome raises "Missing field"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed == 0 {
		t.Fatalf("unexpected union check report: passed=%d failed=%d results=%+v", report.Passed, report.Failed, report.Results)
	}
	var out bytes.Buffer
	if _, err := EvalProgram(src, &out); err != nil {
		t.Fatalf("EvalProgram failed: %v", err)
	}
	wantOut := strings.Join([]string{
		"Alles super!",
		"Allgemeiner Fehler",
		"Fehler 404: Not Found",
		"Hat geklappt",
		"Irgendetwas anderes ist passiert",
		"Irgendetwas anderes ist passiert",
	}, "\n") + "\n"
	if out.String() != wantOut {
		t.Fatalf("stdout mismatch:\ngot:\n%s\nwant:\n%s", out.String(), wantOut)
	}

	// 2. Test sub-union coverage in check (`res is status` covers both "OK" and "FEHLER")
	subUnionCheck := `
def ErrorMessage
    code = 500
    message = "err"
end
def status = "OK" | "FEHLER"
def outcome = status | ErrorMessage

to process res
    check
        res is status       -> "status-ok"
        res is ErrorMessage -> "err:{res.code}"
    end
end
`
	if _, err := ParseProgram(subUnionCheck); err != nil {
		t.Fatalf("expected sub-union exhaustive check to pass, got: %v", err)
	}

	// 3. Test distinguishable records (different field vs different type vs disjoint inline union)
	distinguishable := `
def AdminDiffField
    name  = "Admin"
    level = 1
end
def GuestDiffField
    name   = "Guest"
    ticket = "vip"
end
def actor1 = AdminDiffField | GuestDiffField

def NumPayload
    val = 0
end
def TextPayload
    val = ""
end
def actor2 = NumPayload | TextPayload

def RoleAdmin
    kind = "admin" | "owner"
    name = ""
end
def RoleGuest
    kind = "guest" | "anon"
    name = ""
end
def actor3 = RoleAdmin | RoleGuest

test "distinguishable record unions decode cleanly"
    a1 = (do decode_json '{"name": "Ada", "level": 9}') as actor1
    a1 is AdminDiffField
    g1 = (do decode_json '{"name": "Bob", "ticket": "day"}') as actor1
    g1 is GuestDiffField

    p1 = (do decode_json '{"val": 42}') as actor2
    p1 is NumPayload
    p2 = (do decode_json '{"val": "hi"}') as actor2
    p2 is TextPayload

    r1 = (do decode_json '{"kind": "admin", "name": "Ada"}') as actor3
    r1 is RoleAdmin
    r2 = (do decode_json '{"kind": "guest", "name": "Bob"}') as actor3
    r2 is RoleGuest
end
`
	if rep, err := RunChecks(distinguishable); err != nil || rep.Failed != 0 || rep.Passed != 6 {
		t.Fatalf("distinguishable record unions failed: rep=%+v err=%v", rep, err)
	}

	// 4. Test the 3 Guardrails, Exhaustiveness Check, and Type Narrowing compile-time errors
	errCases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "guardrail 1 text redundant literal",
			src:  "def bad = \"OK\" | text\n",
			want: []string{"'OK' is already covered by 'text'"},
		},
		{
			name: "guardrail 1 number redundant literal",
			src:  "def bad = number | 404\n",
			want: []string{"'404' is already covered by 'number'"},
		},
		{
			name: "guardrail 1 redundancy via flattening",
			src:  "def s = \"OK\" | \"FEHLER\"\ndef bad = s | text\n",
			want: []string{"'OK' is already covered by 'text'"},
		},
		{
			name: "guardrail 2 duplicate string literal",
			src:  "def bad = \"OK\" | \"OK\"\n",
			want: []string{"Duplicate union member", "\"OK\""},
		},
		{
			name: "guardrail 2 duplicate record",
			src:  "def User\n    id = 0\nend\ndef bad = User | User\n",
			want: []string{"Duplicate union member", "User"},
		},
		{
			name: "guardrail 2 duplicate via flattening",
			src:  "def User\n    id = 0\nend\ndef a = \"OK\" | User\ndef b = \"TIMEOUT\" | User\ndef c = a | b\n",
			want: []string{"Duplicate union member", "User"},
		},
		{
			name: "guardrail 3 indistinguishable records",
			src:  "def Admin\n    name = \"a\"\n    id = 0\nend\ndef Guest\n    name = \"g\"\n    id = 0\nend\ndef bad = Admin | Guest\n",
			want: []string{"indistinguishable", "Admin", "Guest"},
		},
		{
			name: "guardrail 3 overlapping inline union field",
			src:  "def Admin\n    role = \"admin\" | \"shared\"\nend\ndef Guest\n    role = \"guest\" | \"shared\"\nend\ndef bad = Admin | Guest\n",
			want: []string{"indistinguishable"},
		},
		{
			name: "narrowing field outside ErrorMessage branch (in OK branch)",
			src: `
def ErrorMessage
    code = 500
    message = "err"
end
def outcome = "OK" | "FEHLER" | ErrorMessage
to bad res
    check
        res is "OK"         -> say "{res.code}"
        res is "FEHLER"     -> say "err"
        res is ErrorMessage -> say "{res.code}"
    end
end
`,
			want: []string{"Cannot read field '.code'", "non-record"},
		},
		{
			name: "narrowing field before check on union param",
			src: `
def ErrorMessage
    code = 500
    message = "err"
end
def outcome = "OK" | "FEHLER" | ErrorMessage
to bad res
    say "{res.code}"
    check
        res is "OK"         -> say "ok"
        res is "FEHLER"     -> say "err"
        res is ErrorMessage -> say "{res.code}"
    end
end
`,
			want: []string{"Cannot read field '.code'", "outcome", "before narrowing"},
		},
		{
			name: "narrowing field in else branch",
			src: `
def ErrorMessage
    code = 500
    message = "err"
end
def outcome = "OK" | "FEHLER" | ErrorMessage
to bad res
    check
        res is ErrorMessage -> say "{res.code}"
        else                -> say "{res.code}"
    end
end
`,
			want: []string{"Cannot read field '.code'"},
		},
		{
			name: "narrowing catches wrong field name on ErrorMessage",
			src: `
def ErrorMessage
    code = 500
    message = "err"
end
def outcome = "OK" | "FEHLER" | ErrorMessage
to bad res
    check
        res is "OK"         -> "ok"
        res is "FEHLER"     -> "err"
        res is ErrorMessage -> "{res.nonexistent}"
    end
end
`,
			want: []string{"ErrorMessage has no field \"nonexistent\""},
		},
		{
			name: "exhaustiveness check missing TIMEOUT",
			src: `
def ErrorMessage
    code    = 500
    message = "Unknown error"
end

def outcome = "OK" | "FEHLER" | "TIMEOUT" | ErrorMessage

to process_response res
    check
        res is "OK"         -> "Erfolgreich verarbeitet"
        res is "FEHLER"     -> "Unbekannter Fehler"
        res is ErrorMessage -> "Fehler {res.code}: {res.message}"
    end
end
`,
			want: []string{"Non-exhaustive check on 'outcome'", "Missing case: \"TIMEOUT\"", "Add a branch for \"TIMEOUT\" or add an 'else -> ...' fallback."},
		},
		{
			name: "narrowing field outside if branch on union variable",
			src: `
def ErrorMessage
    code    = 500
    message = "Unknown error"
end
def outcome = "OK" | "FEHLER" | ErrorMessage
to inspect res
    if res is ErrorMessage
        say "{res.code}"
    end
    say "{res.code}"
end
`,
			want: []string{"Cannot read field '.code'", "outcome", "before narrowing"},
		},
		{
			name: "exhaustiveness and narrowing inside for-loop over pushed union list",
			src: `
def ErrorMessage
    code    = 500
    message = "Unknown error"
end
def outcome = "OK" | "FEHLER" | ErrorMessage
items = list as outcome
items = do push items "OK"
for item in items
    check
        item is "OK"         -> say "ok"
        item is ErrorMessage -> say "{item.code}"
    end
end
`,
			want: []string{"Non-exhaustive check on 'outcome'", "Missing case: \"FEHLER\""},
		},
	}

	for _, tc := range errCases {
		_, err := ParseProgram(tc.src)
		if err == nil {
			t.Fatalf("%s: expected TypeError, got nil", tc.name)
		}
		var easeErr *EaseError
		if !errors.As(err, &easeErr) || easeErr.Stage != "TypeError" {
			t.Fatalf("%s: expected EaseError(TypeError), got %v", tc.name, err)
		}
		full := err.Error()
		for _, sub := range tc.want {
			if !strings.Contains(full, sub) {
				t.Fatalf("%s: expected substring %q in error:\n%s", tc.name, sub, full)
			}
		}
	}
}
