package ease

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLexerAndIsNotKeyword(t *testing.T) {
	src := "# comment\nto check_eq a, b\n  a is-not b\nend\n"
	toks, err := NewLexer(src).Tokenize()
	if err != nil {
		t.Fatalf("unexpected lexer error: %v", err)
	}
	foundIsNot := false
	for _, tok := range toks {
		if tok.Type == TOK_IS_NOT {
			foundIsNot = true
		}
	}
	if !foundIsNot {
		t.Fatalf("expected TOK_IS_NOT token in %+v", toks)
	}
}

func TestToWhereAndCheckBlockExecution(t *testing.T) {
	src := `
to double x
  x * 2
end

to divide a, b
  a / b
end

test "functions"
  do double 4 is 8
  do double 4 is-not 9
  do divide 10, 2 is 5
  do divide 1, 0 raises "Division by zero"
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
to allow_entry name, age
    check
        name is not text  -> "Name muss Text sein"
        name is ""        -> "Name darf nicht leer sein"
        age is not number -> "Alter muss eine Zahl sein"
        age < 18          -> "Du bist {age}. Das ist jung"
        age > 120         -> "Ungültiges Alter"
    end

    "Willkommen {name}, du darfst rein!"
end

to is_valid_vip code, age
    check
        code is text
        code is "VIP2026"
        age is number
        age >= 18
    else
        false
    end

    true
end

test
    do allow_entry 123, 25      is "Name muss Text sein"
    do allow_entry "", 25       is "Name darf nicht leer sein"
    do allow_entry "Max", "abc" is "Alter muss eine Zahl sein"
    do allow_entry "Max", 16    is "Du bist 16. Das ist jung"
    do allow_entry "Max", 150   is "Ungültiges Alter"
    do allow_entry "Max", 25    is "Willkommen Max, du darfst rein!"

    do is_valid_vip "VIP2026", 20 is true
    do is_valid_vip "VIP2026", 16 is false
    do is_valid_vip "FALSCH", 25  is false
    do is_valid_vip "VIP2026", "x" is false
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

to check_access age, pass
    check
        pass is not ticket -> "Ungültiges Ticket"
        age < 18           -> "Abgelehnt: zu jung"
        pass is "none"     -> "Abgelehnt: kein Ticket"
        pass is "vip"      -> "Willkommen in der Lounge"
    end
    "Willkommen im Hauptbereich"
end

test
    check_access 16, "vip" is "Abgelehnt: zu jung"
    check_access 22, "none" is "Abgelehnt: kein Ticket"
    check_access 22, "standard" is "Willkommen im Hauptbereich"
    check_access 30, "vip" is "Willkommen in der Lounge"
    check_access 30, "gold" is "Ungültiges Ticket"
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

to take_damage p, amount
    next_hp = p.hp - amount
    check next_hp <= 0 -> p with hp = 0, state = "KO"
    p with hp = next_hp
end

test
    hero = player with name = "Aria", x = 10
    injured = take_damage hero, 35
    defeated = take_damage injured, 80

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

to add a, b
    a + b
end

to start
    do say "Hello World"
    sum = do add x, y
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

to update_particles items, dt
    total = do len items
    total + dt
end

stepped = do update_particles list as particle, 1

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
	if _, err := EvalProgram("xs = list as number\nxs = do push xs, \"bad\"\n", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot push") {
		t.Fatalf("expected push type error, got: %v", err)
	}
}

func TestLegacySyntaxRejected(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"param brackets", "to add [a b]\n  a + b\nend\n"},
		{"report keyword", "to add a, b\n  report a + b\nend\n"},
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
			if _, err := EvalProgram(tc.src+"x = do add 1, 2\n", &bytes.Buffer{}); err == nil {
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

to step_particle p, dt
    next_life = p.life - 10 * dt
    next_x = p.x + p.vx * dt
    check next_life <= 0 -> p with x = next_x, life = 0, state = "dead"
    p with x = next_x, life = next_life
end

to update_particles items, dt
    out = list as particle
    for items do
        check state is "dead" -> out = do push out, it
        check state is "active" -> out = do push out, do step_particle it, dt
    end
    out
end

test "04_particles reference"
    cloud = list as particle with
        x = 0, vx = 5, life = 20
        x = 10, vx = 2, life = 5
    end

    stepped = do update_particles cloud, 1

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
    raw_ok  = do ask api, "/age/ok"
    raw_neg = do ask api, "/age/negative"
    raw_bad = do ask api, "/age/bad"
    raw_err = do ask api, "/fail"

    raw_ok is "25"
    raw_err is ""

    # Typ-Konformität (as) vs. Inhaltsvalidität (to + check)
    do parse_age raw_ok is 25
    do parse_age raw_neg is -5
    do parse_age raw_bad raises "Cannot convert text"
    do validate_age do parse_age raw_ok is 25
    do validate_age do parse_age raw_neg is "Alter kann nicht negativ sein"

    # POST via do send
    posted = do send api, "/submit", 42
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

test "Record <-> JSON via as"
    # Valid JSON with unknown field and omitted default field
    u = "\{\"id\": 7, \"role\": \"admin\", \"extra\": true\}" as api_user
    u is api_user
    u.id is 7
    u.name is "Gast"
    u.role is "admin"

    # Record -> JSON text
    encoded = u as text
    decoded = encoded as api_user
    decoded is api_user
    decoded.id is 7
    decoded.role is "admin"

    # Non-match cases -> immediate error (Outcome B, zero silent fallbacks)
    "" as api_user raises "empty text"
    "\{broken" as api_user raises "invalid JSON"
    "null" as api_user raises "JSON null"
    "\{\"id\": \"not-a-num\"\}" as api_user raises "api_user.id"
    "\{\"role\": \"superuser\"\}" as api_user raises "api_user.role"
    42 as api_user raises "expected JSON text"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 13 || report.Failed != 0 {
		t.Fatalf("expected 13 passed checks, got %d passed / %d failed: %+v", report.Passed, report.Failed, report.Results)
	}
}

func TestAsCastBindsToWholeDoCall(t *testing.T) {
	src := `
def my_record
    first  = ""
    second = ""
end

to some_func a, b
    "\{\"first\": \"{a}\", \"second\": \"{b}\"\}"
end

x = do some_func "a", "b" as my_record
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
	if !ok || call.Callee != "some_func" || len(call.Args) != 2 {
		t.Fatalf("expected CastExpr.Target to be CallExpr(some_func, 2 args), got %#v", cast.Target)
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

to move_x x, key
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

    for range 1, 10 do
        check state.status is "closed" -> "Fenster geschlossen:{pos_x}"

        pos_x = do move_x pos_x, state.key
        current = scene with
            background = "slate"
            circles = list as circle with
                x = pos_x, y = 300, size = 35, color = "amber"
            end
            boxes = list as box with
                x = 120, y = 500, width = 560, height = 24, color = "emerald"
            end
        end

        state = do send win, current as frame_state
        check state is not frame_state -> "Fensterfehler"
    end

    "Fertig"
end

test "inclusive range & headless screen"
    do range 1, 4 is list 1, 2, 3, 4
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
for range 1, 10 do say it end
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
for x in range 1, 4 do sum = sum + x end

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
			name:     "unknown type in list as",
			src:      "xs = list as unknown_item\n",
			wantSub:  `Unknown type "unknown_item" after 'list as'`,
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

	// Valid cases: built-in types, prior def, imported std module blueprint, and union def.
	validSrc := `
import network

def custom_user
    name = ""
end

def ticket = "open" | "closed"

a = 42 as text
b = "12" as number
c = "true" as bool
d = "open" as ticket
e = "\{\"name\": \"Ada\"\}" as custom_user
f = "\{\"status\": 200, \"body\": \"ok\"\}" as http_response
g = list as bool
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
			name:    "invalid bool text",
			src:     `x = "maybe" as bool`,
			wantSub: `Cannot convert text "maybe" to bool`,
		},
		{
			name:    "disallowed union value",
			src:     "def ticket = \"open\" | \"closed\"\nx = \"pending\" as ticket\n",
			wantSub: `Value pending is not valid for union "ticket"`,
		},
		{
			name:    "json array rejected for record def",
			src:     "def user\n    id = 0\nend\nx = \"[1, 2]\" as user\n",
			wantSub: `Cannot convert text to user: invalid JSON`,
		},
		{
			name:    "wrong nested field type",
			src:     "def user\n    id = 0\nend\nx = \"\\{\\\"id\\\": true\\}\" as user\n",
			wantSub: `Field 'user.id' expected number, got bool`,
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
