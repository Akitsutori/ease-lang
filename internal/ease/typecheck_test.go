package ease

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
m1 = '{"name": "Ada"}' as json
e = m1 as custom_user
m2 = '{"status": 200, "body": "ok", "error": ""}' as json
f = m2 as network.http_response
g = list as number
`
	if _, err := ParseProgram(validSrc); err != nil {
		t.Fatalf("expected valid strict 'as' usages to parse cleanly, got: %v", err)
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
    check res is "OK"
        say "Alles super!"
    end
    check res is "FEHLER"
        say "Allgemeiner Fehler"
    end
    check res is ErrorMessage
        say "Fehler {res.code}: {res.message}"
    end
end

to process_response res
    check res is "OK"         -> "Erfolgreich verarbeitet"
    check res is "FEHLER"     -> "Unbekannter Fehler"
    check res is ErrorMessage -> "Fehler {res.code}: {res.message}"
end

to log_only_success res
    check res is "OK"
        say "Hat geklappt"
    else
        say "Irgendetwas anderes ist passiert"
    end
end

to inspect_check res
    check res is ErrorMessage -> "check:{res.code}:{res.message}"
    -> "other"
end

to unwrap_err_guard res
    check res is not ErrorMessage -> "not-error"
    -> "guard:{res.code}"
end

to server_error_only res
    check res is ErrorMessage and res.code >= 500 -> "server:{res.code}:{res.message}"
    -> "handled"
end

err_rec = ErrorMessage with code = 404, message = "Not Found"
handle "OK"
handle "FEHLER"
handle err_rec
log_only_success "OK"
log_only_success "FEHLER"
log_only_success err_rec

test "orthogonal union building blocks, flattening, JSON as, narrowing, and else ->"
    "OK" is outcome
    "FEHLER" is outcome
    err_rec is outcome
    42 is tagged
    "hello" is tagged
    err_rec is tagged

    # Explicit JSON -> union conversion picks ErrorMessage
    raw_err_map = '{"code": 503, "message": "Unavailable"}' as json
    raw_err = raw_err_map as outcome
    raw_err is ErrorMessage
    process_response raw_err is "Fehler 503: Unavailable"
    process_response "OK" is "Erfolgreich verarbeitet"
    process_response "FEHLER" is "Unbekannter Fehler"

    # Record field typed by union accepts literal and ErrorMessage from JSON
    env1_map = '{"res": "OK"}' as json
    env1 = env1_map as envelope
    env1.res is "OK"
    env2_map = '{"res": {"code": 400, "message": "Bad"}}' as json
    env2 = env2_map as envelope
    env2.res is ErrorMessage
    inspect_check env2.res is "check:400:Bad"
    inspect_check "OK" is "other"

    # Heterogeneous union list + JSON array coercion
    items = list as outcome
    items = append items "OK"
    items = append items err_rec
    count items is 2
    items.2 is ErrorMessage

    json_items_raw = '["OK", {"code": 404, "message": "Missing"}]' as json
    json_items = json_items_raw as outcome
    json_items.1 is "OK"
    json_items.2 is ErrorMessage

    # Early-exit guard narrowing & compound 'and' narrowing
    unwrap_err_guard err_rec is "guard:404"
    unwrap_err_guard "OK" is "not-error"
    server_error_only raw_err is "server:503:Unavailable"
    server_error_only err_rec is "handled"

    # Reject incomplete map when decoding into union
    incomplete_map = '{"code": 1}' as json
    incomplete_map as outcome is error
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
    check res is status       -> "status-ok"
    check res is ErrorMessage -> "err:{res.code}"
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
    ma1 = '{"name": "Ada", "level": 9}' as json
    a1 = ma1 as actor1
    a1 is AdminDiffField
    mg1 = '{"name": "Bob", "ticket": "day"}' as json
    g1 = mg1 as actor1
    g1 is GuestDiffField

    mp1 = '{"val": 42}' as json
    p1 = mp1 as actor2
    p1 is NumPayload
    mp2 = '{"val": "hi"}' as json
    p2 = mp2 as actor2
    p2 is TextPayload

    mr1 = '{"kind": "admin", "name": "Ada"}' as json
    r1 = mr1 as actor3
    r1 is RoleAdmin
    mr2 = '{"kind": "guest", "name": "Bob"}' as json
    r2 = mr2 as actor3
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
    check res is "OK"         -> "{res.code}"
    check res is "FEHLER"     -> "err"
    check res is ErrorMessage -> "{res.code}"
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
    check res is "OK"         -> "ok"
    check res is "FEHLER"     -> "err"
    check res is ErrorMessage -> "{res.code}"
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
    check res is ErrorMessage
        -> "{res.code}"
    else
        -> "{res.code}"
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
    check res is "OK"         -> "ok"
    check res is "FEHLER"     -> "err"
    check res is ErrorMessage -> "{res.nonexistent}"
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
    check res is "OK"         -> "Erfolgreich verarbeitet"
    check res is "FEHLER"     -> "Unbekannter Fehler"
    check res is ErrorMessage -> "Fehler {res.code}: {res.message}"
end
`,
			want: []string{"Non-exhaustive check on 'outcome'", "Missing case: \"TIMEOUT\"", "Add a branch for \"TIMEOUT\" or add a final '-> ...' fallback."},
		},
		{
			name: "narrowing field outside check branch on union variable",
			src: `
def ErrorMessage
    code    = 500
    message = "Unknown error"
end
def outcome = "OK" | "FEHLER" | ErrorMessage
to inspect res
    check res is ErrorMessage
        say "{res.code}"
    else
        say "other"
    end
    say "{res.code}"
end
`,
			want: []string{"Cannot read field '.code'", "outcome", "before narrowing"},
		},
		{
			name: "exhaustiveness on sequential check guards missing FEHLER",
			src: `
def ErrorMessage
    code    = 500
    message = "Unknown error"
end
def outcome = "OK" | "FEHLER" | ErrorMessage
to check_item x
    check x is "OK"         -> "ok"
    check x is ErrorMessage -> "{x.code}"
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

func TestStrictIsTypeInstanceSeparationAndNestedRecordDefaults(t *testing.T) {
	// Acceptance criteria:
	// 1. a = user with id = 1 -> a is user == true
	// 2. b = user with id = 2 -> b is user == true
	// 3. a is b == false (value equality, transitive)
	// 4. user is a -> error (TypeError, not false) and user is user -> error
	// 5. (line with a = pt) is (line with a = (pt with x = 9)) -> false (nested record defaults are instantiated)
	src := `
def user
    id = 0
end

def pt
    x = 0
end

def line
    a = pt
end

a = user with id = 1
b = user with id = 2
c = user with id = 1

l_default = line with a = pt
l_x0      = line with a = (pt with x = 0)
l_x9      = line with a = (pt with x = 9)

test "strict asymmetric is and instantiated nested record defaults"
    a is user
    b is user
    a is not b
    a is c
    l_default.a is pt
    l_default is l_x0
    l_default is not l_x9
    (user is a) is error
    (user is user) is error
    (user is not a) is error
    (user is text) is error
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 11 {
		t.Fatalf("expected 11 passed / 0 failed checks, got %d passed / %d failed:\n%+v", report.Passed, report.Failed, report.Results)
	}

	// Verify runtime failures when a prototype or union appears on the left of `is` (must error, never return false).
	leftTypeErrCases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "user is a",
			src:  "def user\n    id = 0\nend\na = user with id = 1\ncheck user is a\n    say \"ok\"\nend\n",
			want: "cannot use record type 'user' as a value on the left side of 'is'",
		},
		{
			name: "user is user",
			src:  "def user\n    id = 0\nend\ncheck user is user\n    say \"ok\"\nend\n",
			want: "cannot use record type 'user' as a value on the left side of 'is'",
		},
		{
			name: "union on left of is",
			src:  "def role = \"guest\" | \"admin\"\ncheck role is \"guest\"\n    say \"ok\"\nend\n",
			want: "cannot use union type 'role' as a value on the left side of 'is'",
		},
	}
	for _, tc := range leftTypeErrCases {
		_, err := EvalProgram(tc.src, &bytes.Buffer{})
		if err == nil {
			t.Fatalf("%s: expected TypeError when type is on left of 'is', got nil", tc.name)
		}
		var easeErr *EaseError
		if !errors.As(err, &easeErr) || easeErr.Stage != "TypeError" {
			t.Fatalf("%s: expected TypeError, got %v", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected %q in error, got:\n%s", tc.name, tc.want, err.Error())
		}
	}

	// Verify pure ValuesEqual directly (no prototype bypass).
	proto := &RecordValue{TypeName: "user", Fields: map[string]Value{"id": 0.0}, IsPrototype: true}
	inst := &RecordValue{TypeName: "user", Fields: map[string]Value{"id": 1.0}, IsPrototype: false}
	if ValuesEqual(inst, proto) || ValuesEqual(proto, inst) || ValuesEqual(proto, proto) {
		t.Fatalf("ValuesEqual must never return true for record prototypes")
	}

	// Verify nested record default on prototype `line` is already instantiated (IsPrototype == false).
	prog, err := ParseProgram("def pt\n    x = 0\nend\ndef line\n    a = pt\n    b = 0\nend\nl1 = line with b = 1\nl2 = line with a = (pt with x = 9), b = 1\n")
	if err != nil {
		t.Fatalf("ParseProgram failed: %v", err)
	}
	ev := NewEvaluator("", prog, &bytes.Buffer{})
	defer ev.Close()
	if _, err := ev.ExecStatements(prog.Body, ev.global); err != nil {
		t.Fatalf("ExecStatements failed: %v", err)
	}
	l1Val, _ := ev.global.Lookup("l1")
	l2Val, _ := ev.global.Lookup("l2")
	l1A := l1Val.(*RecordValue).Fields["a"].(*RecordValue)
	if l1A.IsPrototype {
		t.Fatalf("expected nested record default l1.a to be instantiated (IsPrototype=false), got IsPrototype=true")
	}
	if ValuesEqual(l1Val, l2Val) {
		t.Fatalf("expected l1 (default a=pt{x:0}) != l2 (a=pt{x:9}), but ValuesEqual returned true")
	}
}

func TestIssue7NoBoolConditionJsonBoolAndStrictUnionLiteral(t *testing.T) {
	// 1. Option A: conditions cannot be stored, printed, passed, or returned.
	condCases := []struct {
		name string
		src  string
	}{
		{"store comparison", "ok = 3 > 2\n"},
		{"print comparison", "say 3 > 2\n"},
		{"list element", "xs = list 3 > 2\n"},
		{"record field", "def box\n    v = 0\nend\nb = box with v = 3 > 2\n"},
		{"function return", "to f\n    -> 3 > 2\nend\n"},
	}
	for _, tc := range condCases {
		_, err := EvalProgram(tc.src, &bytes.Buffer{})
		if err == nil {
			t.Fatalf("%s: expected TypeError for condition value, got nil", tc.name)
		}
		var easeErr *EaseError
		if !errors.As(err, &easeErr) || easeErr.Stage != "TypeError" {
			t.Fatalf("%s: expected TypeError, got %v", tc.name, err)
		}
		if !strings.Contains(err.Error(), `Conditions cannot be stored or printed. Use a domain union instead, e.g.: def state = "free" | "occupied"`) {
			t.Fatalf("%s: unexpected error message: %v", tc.name, err)
		}
	}

	// 1b. Tier 2: Standalone conditions outside 'check'/'where'/'test' are rejected everywhere.
	standaloneCases := []struct {
		name string
		src  string
	}{
		{"top-level comparison", "10 > 5\n"},
		{"top-level contains", "contains \"abc\" \"a\"\n"},
		{"function body standalone", "to f\n    10 > 5\nend\n"},
		{"function body contains", "to f\n    contains \"abc\" \"a\"\nend\n"},
	}
	for _, tc := range standaloneCases {
		_, err := ParseProgram(tc.src)
		if err == nil {
			t.Fatalf("%s: expected standalone condition TypeError, got nil", tc.name)
		}
		var easeErr *EaseError
		if !errors.As(err, &easeErr) || easeErr.Stage != "TypeError" {
			t.Fatalf("%s: expected TypeError, got %v", tc.name, err)
		}
		if !strings.Contains(err.Error(), "Standalone condition has no effect; conditions can only be used in 'check', 'where', or 'test'") {
			t.Fatalf("%s: unexpected error message: %v", tc.name, err)
		}
	}

	// Union-based domain outcome works cleanly.
	unionOutcomeSrc := `
def ergebnis = "👍" | "👎"
to check_ready score
    check score >= 100 -> "👍" as ergebnis
    -> "👎" as ergebnis
end
say check_ready 120
say check_ready 50
`
	var out bytes.Buffer
	if _, err := EvalProgram(unionOutcomeSrc, &out); err != nil {
		t.Fatalf("expected union outcome program to succeed, got: %v", err)
	}
	if got, want := strings.TrimSpace(out.String()), "👍\n👎"; got != want {
		t.Fatalf("unexpected output: got %q, want %q", got, want)
	}

	// 2. JSON booleans map to "true" | "false" unions and roundtrip for schema fields,
	// while normal text fields and free maps stay JSON strings.
	jsonBoolSrc := `
def reply
    ok = "true" | "false"
    label = "true"
end

m = '{"ok": true, "label": "true"}' as json
r = m as reply
say r.ok
say (r as json)

free = '{"ok": true}' as json
say (free as json)
`
	var jsonOut bytes.Buffer
	if _, err := EvalProgram(jsonBoolSrc, &jsonOut); err != nil {
		t.Fatalf("expected JSON bool roundtrip to succeed, got: %v", err)
	}
	if got, want := strings.TrimSpace(jsonOut.String()), "true\n{\"label\":\"true\",\"ok\":true}\n{\"ok\":\"true\"}"; got != want {
		t.Fatalf("unexpected JSON bool output: got %q, want %q", got, want)
	}

	// 3. Strict union-literal checking at `is` (typo protection) — static & dynamic.
	staticTypoSrc := `
def gate
    access = "granted" | "denied"
end
g = gate with access = "granted"
check g.access is "denid"
    say "ok"
end
`
	if _, err := EvalProgram(staticTypoSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `"denid" is not a value of gate.access ("granted" | "denied")`) {
		t.Fatalf("expected static union literal typo error, got: %v", err)
	}

	dynamicTypoSrc := `
def gate
    access = "granted" | "denied"
end
to inspect_gate g
    check g.access is "denid" -> "ok"
    -> "denied"
end
g = gate with access = "granted"
say inspect_gate g
`
	if _, err := EvalProgram(dynamicTypoSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `"denid" is not a value of gate.access ("granted" | "denied")`) {
		t.Fatalf("expected runtime union literal typo error, got: %v", err)
	}

	// 4. Static condition enforcement (checkMustBeCondition) rejects non-condition values at compile time.
	condErrCases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "check raw as number",
			src:  "raw = \"42\"\ncheck raw as number\n    say \"ok\"\nend\n",
			want: "Ease has no truthiness and 'as' returns a converted value, not a condition",
		},
		{
			name: "check param",
			src:  "to f x\n    check x -> 1\n    -> 0\nend\n",
			want: "'check' requires a condition (e.g. 'x > 0' or 'x is ...'), got value",
		},
		{
			name: "where non-condition",
			src:  "xs = (list 1, 2) where it + 1\n",
			want: "'where' requires a condition (e.g. 'x > 0' or 'x is ...'), got number",
		},
	}
	for _, tc := range condErrCases {
		if _, err := ParseProgram(tc.src); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected static TypeError containing %q, got: %v", tc.name, tc.want, err)
		}
	}
}

func TestWhereUnionNarrowingAndIndexTypecheck(t *testing.T) {
	valid := `
def ErrorMessage
    code    = 500
    message = "Error"
end

def outcome = "OK" | ErrorMessage

items = list as outcome
items = append items "OK"
items = append items ErrorMessage

errs = items where
    it is ErrorMessage
    it.code >= 500
    it.index >= 1
end
say count errs
`
	if _, err := ParseProgram(valid); err != nil {
		t.Fatalf("expected valid where union narrowing to typecheck cleanly, got: %v", err)
	}

	unnarrowed := `
def ErrorMessage
    code    = 500
    message = "Error"
end

def outcome = "OK" | ErrorMessage

items = list as outcome
bad = items where it.code >= 500
`
	if _, err := ParseProgram(unnarrowed); err == nil || !strings.Contains(err.Error(), "Cannot read field '.code' on union variable \"it\"") {
		t.Fatalf("expected union narrowing error in where, got: %v", err)
	}
}

func TestMatchStaticTypecheckingAndInference(t *testing.T) {
	errorCases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name: "adjacent variable captures",
			src: `
s = "ab"
match s
  "{a}{b}" -> say a
end
`,
			wantSub: "Adjacent variable-length placeholders",
		},
		{
			name: "adjacent wildcard and capture",
			src: `
s = "ab"
match s
  "{...}{b}" -> say b
end
`,
			wantSub: "Adjacent variable-length placeholders",
		},
		{
			name: "adjacent capture and wildcard",
			src: `
s = "ab"
match s
  "{a}{...}" -> say a
end
`,
			wantSub: "Adjacent variable-length placeholders",
		},
		{
			name: "adjacent wildcards",
			src: `
s = "ab"
match s
  "{...}{...}" -> say "x"
end
`,
			wantSub: "Adjacent variable-length placeholders",
		},
		{
			name: "zero fixed width rejected",
			src: `
s = "ab"
match s
  "{a: 0}{b}" -> say a
end
`,
			wantSub: "positive integer (>= 1)",
		},
		{
			name: "fractional fixed width rejected",
			src: `
s = "ab"
match s
  "{a: 1.5}{b}" -> say a
end
`,
			wantSub: "positive integer (>= 1)",
		},
		{
			name: "reserved capture name it rejected",
			src: `
s = "ab"
match s
  "{it}" -> say it
end
`,
			wantSub: "reserved name 'it'",
		},
		{
			name: "empty match rejected",
			src: `
s = "ab"
match s
end
`,
			wantSub: "requires at least one pattern branch",
		},
		{
			name: "missing else on text match expression",
			src: `
s = "hello"
r = match s
  "hello" -> "hi"
end
`,
			wantSub: "'match' expression on text requires an 'else' branch",
		},
		{
			name: "top level match returning value rejected",
			src: `
s = "hello"
match s
  "hello" -> 42
end
`,
			wantSub: "Cannot use '-> <value>' at top level",
		},
		{
			name: "top level list match statement without assignment rejected",
			src: `
xs = list "a", "b"
match xs
  "{v}" -> v
end
`,
			wantSub: "List 'match' is a transformation expression and cannot be used as a standalone statement",
		},
		{
			name: "list match statement with non-expression action rejected",
			src: `
to f
  xs = list "a", "b"
  match xs
    "{v}"
      say v
  end
end
`,
			wantSub: "List 'match' is a transformation expression and cannot be used as a standalone statement",
		},
		{
			name: "numeric target rejected statically",
			src: `
n = 10
match n
  "10"
    say "ten"
end
`,
			wantSub: "'match' requires text or a list of text, got number",
		},
		{
			name: "list of number target rejected statically",
			src: `
nums = list 1, 2
res = match nums
  "{v}" -> v
end
`,
			wantSub: "'match' requires text or a list of text, got list of number",
		},
		{
			name: "unknown type string suggests text",
			src: `
s = "hi"
match s
  "{v as string}"
    say v
end
`,
			wantSub: "Did you mean 'text'?",
		},
		{
			name: "bool capture type rejected",
			src: `
s = "true"
match s
  "{v as bool}"
    say v
end
`,
			wantSub: `Boolean ("bool") is not a built-in type`,
		},
		{
			name: "record capture type rejected",
			src: `
def Item
  name = ""
end
s = "hi"
match s
  "{v as Item}"
    say "ok"
end
`,
			wantSub: `Record type "Item" cannot be used directly in match capture`,
		},
		{
			name: "record-only union capture rejected",
			src: `
def A
  x = 0
end
def B
  y = 0
end
def RecOnly = A | B
s = "hi"
match s
  "{v as RecOnly}"
    say "ok"
end
`,
			wantSub: `Union "RecOnly" contains only record variants`,
		},
		{
			name: "conflicting repeated capture number vs text",
			src: `
s = "1-1"
match s
  "{n as number}-{n as text}"
    say n
end
`,
			wantSub: `Conflicting types for repeated capture "n"`,
		},
		{
			name: "conflicting repeated capture implicit text vs number",
			src: `
s = "1-1"
match s
  "{n}-{n as number}"
    say n
end
`,
			wantSub: `Conflicting types for repeated capture "n"`,
		},
		{
			name: "capture variable shadowing function rejected",
			src: `
to helper x -> x
s = "abc"
match s
  "{helper}"
    say "ok"
end
`,
			wantSub: `Cannot name capture variable "helper" the same as a 'to' function`,
		},
		{
			name: "condition returned in match expression rejected",
			src: `
s = "abc"
r = match s
  "abc" -> 10 > 5
  else -> 10 < 5
end
`,
			wantSub: "Conditions cannot be stored or printed",
		},
		{
			name: "statically mixed list branch types rejected",
			src: `
xs = list "1", "a"
res = match xs
  "{n as number}" -> n
  "{t}" -> t
end
`,
			wantSub: "Mixed result types in list 'match': expected number, got text",
		},
		{
			name: "statically incompatible list downstream field error",
			src: `
def Entry
  name = ""
end
lines = list "NAME:ada"
entries = match lines
  "NAME:{n}" -> Entry with name = n
end
bad = entries where it.missing is "x"
`,
			wantSub: `Entry has no field "missing"`,
		},
		{
			name: "statically inferred list of text fails sum",
			src: `
lines = list "a", "b"
texts = match lines
  "{m}" -> m
end
total = sum texts
`,
			wantSub: "sum requires a list of numbers",
		},
	}

	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got:\n%v", tc.wantSub, err)
			}
		})
	}

	validSrc := `
def Level = "INFO" | "WARN"
def Entry
  lvl = "INFO" | "WARN"
  msg = ""
end

to loop_with_match_exit cmds
  i = 1
  loop
    check i > count cmds -> end
    match cmds.(i)
      "quit" -> end
      else
        i = i + 1
    end
  end
  -> i - 1
end

lines = list "INFO: ok", "10"
nums = match lines
  "{n as number}" -> n
end
total = sum nums

entries = match lines
  "{l as Level}: {m}" -> Entry with lvl = l, msg = m
  else -> Entry with lvl = "WARN", msg = it
end
filtered = entries where it.lvl is "INFO"

strict_entries = match (list "INFO: ok")
  "{l as Level}: {m}" -> Entry with lvl = l, msg = m
  else -> Entry with lvl = "WARN", msg = "fallback"
end

labels = match lines
  "{l as Level}: {m}" -> l
  else -> "UNKNOWN"
end

args_loop = loop_with_match_exit (list "go", "quit")
say "{total}|{count filtered}|{count strict_entries}|{count labels}|{args_loop}"
`
	if _, err := ParseProgram(validSrc); err != nil {
		t.Fatalf("expected valid typed match program to pass typecheck, got:\n%v", err)
	}
	var out bytes.Buffer
	if _, err := EvalProgram(validSrc, &out); err != nil {
		t.Fatalf("expected EvalProgram on validSrc to succeed, got: %v", err)
	}
	if out.String() != "10|1|1|2|1\n" {
		t.Fatalf("unexpected output: got %q, want %q", out.String(), "10|1|1|2|1\n")
	}
}

func TestCastFallbackStaticTypecheckAndInference(t *testing.T) {
	errorCases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "number fallback given text literal",
			src:     "raw = \"42\"\nx = raw as number or \"abc\"\n",
			wantSub: "Fallback for 'as number' must be number, got text",
		},
		{
			name:    "number fallback given list",
			src:     "lines = list \"1\", \"2\"\nx = lines as number or (list 0)\n",
			wantSub: "Fallback for 'as number' must be number, got list of number",
		},
		{
			name:    "scalar number fallback given list",
			src:     "raw = \"1\"\nx = raw as number or (list 0)\n",
			wantSub: "Fallback for 'as number' must be number, got list of number",
		},
		{
			name:    "text fallback given number",
			src:     "raw = 'null' as json\nx = raw as text or 42\n",
			wantSub: "Fallback for 'as text' must be text, got number",
		},
		{
			name: "union fallback given invalid literal with typo hint",
			src: `
def Level = "INFO" | "WARN" | "ERROR"
raw = "x"
lvl = raw as Level or "EROR"
`,
			wantSub: `Did you mean "ERROR"?`,
		},
		{
			name: "union fallback given wrong primitive literal",
			src: `
def Level = "INFO" | "WARN"
raw = "x"
lvl = raw as Level or 123
`,
			wantSub: `Fallback 123 is not a valid value of union "Level"`,
		},
		{
			name: "numeric union fallback given unlisted number",
			src: `
def HttpCode = 200 | 404
raw = "x"
c = raw as HttpCode or 999
`,
			wantSub: `Fallback 999 is not a valid value of union "HttpCode"`,
		},
		{
			name: "union fallback given invalid def constant",
			src: `
def Level = "INFO" | "WARN"
def bad_lvl = "TYPO"
raw = "x"
lvl = raw as Level or bad_lvl
`,
			wantSub: `Fallback "TYPO" is not a valid value of union "Level"`,
		},
		{
			name: "strict AOT rejects plain text variable as closed union fallback",
			src: `
def Level = "INFO" | "WARN"
fb = "INFO"
raw = "x"
lvl = raw as Level or fb
`,
			wantSub: `Fallback of type text is not statically guaranteed to belong to union "Level"`,
		},
		{
			name: "union fallback given incompatible sub-union",
			src: `
def Level = "INFO" | "WARN"
def Other = "INFO" | "DEBUG"
sub = "DEBUG" as Other
raw = "x"
lvl = raw as Level or sub
`,
			wantSub: `Fallback variant "DEBUG" is not covered by union "Level"`,
		},
		{
			name:    "condition rejected as cast fallback",
			src:     "raw = \"1\"\nx = raw as number or (1 > 0)\n",
			wantSub: "Conditions cannot be stored or printed",
		},
		{
			name:    "bool target still rejected with or",
			src:     "raw = \"true\"\nx = raw as bool or 0\n",
			wantSub: `Boolean ("bool") is not a built-in type`,
		},
		{
			name:    "string alias target still rejected with or",
			src:     "raw = \"hi\"\nx = raw as string or \"\"\n",
			wantSub: "Unknown type 'string' after 'as'",
		},
		{
			name:    "unknown target still rejected with or",
			src:     "raw = \"x\"\nx = raw as nope or 0\n",
			wantSub: `Unknown type "nope" after 'as'`,
		},
		{
			name: "record contract cannot use or fallback",
			src: `
def user
  id = 0
end
raw = '{"id": 1}' as json
d = user with id = 2
u = raw as user or d
`,
			wantSub: `'or' fallback cannot be used with record type "user"`,
		},
		{
			name: "record-only union cannot use or fallback",
			src: `
def A
  x = 0
end
def B
  y = 1
end
def Both = A | B
raw = '{"x": 1}' as json
res = raw as Both or (A with x = 2)
`,
			wantSub: `'or' fallback cannot be used with record-only union "Both"`,
		},
		{
			name:    "load csv with or fallback rejected",
			src:     "rows = load \"./f.csv\" as csv or 0\n",
			wantSub: "'or' fallback cannot be used with 'load ... as csv'",
		},
		{
			name:    "load text with or fallback rejected",
			src:     "txt = load \"./f.txt\" as text or \"\"\n",
			wantSub: "'or' fallback cannot be used with 'load ... as text'",
		},
		{
			name: "record target cannot cast to scalar with or fallback",
			src: `
def Item
  x = 0
end
it_rec = Item with x = 1
n = it_rec as number or 0
`,
			wantSub: "Cannot convert Item with 'as number or ...' fallback",
		},
		{
			name: "downstream union list inferred from list cast fallback rejects sum",
			src: `
def Level = "INFO" | "WARN"
lines = list "INFO", "bad"
lvls = lines as Level or "INFO"
total = sum lvls
`,
			wantSub: "sum requires a list of numbers",
		},
		{
			name: "downstream where on inferred union list rejects invalid literal",
			src: `
def Level = "INFO" | "WARN"
lines = list "INFO", "bad"
lvls = lines as Level or "INFO"
bad = lvls where it is "TYPO"
`,
			wantSub: `"TYPO" is not a value of Level`,
		},
	}

	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got:\n%v", tc.wantSub, err)
			}
		})
	}

	validSrc := `
def Level = "INFO" | "WARN" | "ERROR"
def DefaultLevel = "INFO"
def Flex = number | text

lines = list "10", "bad", "20"
nums = lines as number or 0
total = sum nums
filtered = (lines as number or 0) where it > 5

raw = "BAD"
fb_plain = "ok"
l1 = raw as Level or DefaultLevel
l2_raw = 'null' as json
l2 = l2_raw as Flex or fb_plain
l3 = lines as Level or "WARN"
say "{total}|{count filtered}|{l1}|{l2}|{count l3}"
`
	if _, err := ParseProgram(validSrc); err != nil {
		t.Fatalf("expected valid cast fallback program to pass typecheck, got:\n%v", err)
	}
	var out bytes.Buffer
	if _, err := EvalProgram(validSrc, &out); err != nil {
		t.Fatalf("EvalProgram failed: %v", err)
	}
	if out.String() != "30|2|INFO|ok|3\n" {
		t.Fatalf("unexpected output: got %q, want %q", out.String(), "30|2|INFO|ok|3\n")
	}
}

func TestAuditConsistencyGuardrails(t *testing.T) {
	// 1. Direct and transitive `spawn` inside `swarm loop` rejected statically + at runtime.
	directSpawnSwarm := `
to worker n
    say n
end
res = swarm loop range 1 2
    spawn worker it
    it
end
`
	if _, err := ParseProgram(directSpawnSwarm); err == nil || !strings.Contains(err.Error(), "'spawn' cannot be used inside 'swarm loop'") {
		t.Fatalf("expected TypeError for direct spawn in swarm loop, got: %v", err)
	}

	transitiveSpawnSwarm := `
to leaf n
    say n
end
to step_b n
    spawn leaf n
    n
end
to step_a n
    step_b n
end
res = swarm loop range 1 2
    step_a it
end
`
	if _, err := ParseProgram(transitiveSpawnSwarm); err == nil || !strings.Contains(err.Error(), `Cannot call "step_a" inside 'swarm loop' because it uses 'spawn'`) {
		t.Fatalf("expected TypeError for transitive spawn in swarm loop, got: %v", err)
	}

	// Runtime defense-in-depth check on execSpawnStmt when inSwarmWorker == true.
	ev := NewEvaluator("", &Program{}, &bytes.Buffer{})
	defer ev.Close()
	ev.inSwarmWorker = true
	_, _, rtSpawnErr := ev.execSpawnStmt(&SpawnStmt{
		posInfo: posInfo{Line: 1, Col: 1},
		Call:    &CallExpr{Callee: "leaf"},
	}, ev.global)
	if rtSpawnErr == nil || !strings.Contains(rtSpawnErr.Error(), "'spawn' cannot be used inside 'swarm loop'") {
		t.Fatalf("expected RuntimeError from execSpawnStmt in swarm worker, got: %v", rtSpawnErr)
	}

	// 2. String escapes: \r allowed, unknown escapes rejected with SyntaxError.
	var crOut bytes.Buffer
	if _, err := EvalProgram(`say "a\rb"`, &crOut); err != nil {
		t.Fatalf("expected \\r escape to succeed, got: %v", err)
	}
	if crOut.String() != "a\rb\n" {
		t.Fatalf("unexpected \\r output: %q", crOut.String())
	}
	if _, err := ParseProgram(`x = "bad\sescape"`); err == nil || !strings.Contains(err.Error(), `Unknown escape sequence '\s'`) {
		t.Fatalf("expected SyntaxError for unknown escape \\s, got: %v", err)
	}

	// 3. `put` and `close` are statically void (forbidden in value expressions).
	for _, voidSrc := range []string{
		"ch = inbox as number\nx = put ch 1\n",
		"ch = inbox as number\nx = close ch\n",
		"ch = inbox as number\nsay (put ch 1)\n",
		"ch = inbox as number\nsay (close ch)\n",
	} {
		if _, err := ParseProgram(voidSrc); err == nil || !strings.Contains(err.Error(), "does not return a value") {
			t.Fatalf("expected void TypeError for %q, got: %v", voidSrc, err)
		}
	}

	// 4. Imported file modules cannot collide with built-in function names.
	modDir := t.TempDir()
	modPath := filepath.Join(modDir, "helpers.ease")
	if err := os.WriteFile(modPath, []byte("to count xs\n    0\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(modDir, "main.ease")
	mainSrc := "import \"./helpers.ease\" as h\nsay h:count (list 1, 2)\n"
	if _, err := ParseProgramFromFile(mainSrc, mainPath); err == nil || !strings.Contains(err.Error(), `Cannot define function "count": it conflicts with a built-in function`) {
		t.Fatalf("expected built-in collision TypeError in imported module, got: %v", err)
	}

	// 5. Internal stdlib primitives cannot be called directly from user code.
	for _, privSrc := range []string{
		"import network\nreq = network.http_request with url = \"http://127.0.0.1:1\"\nr = http_call req\n",
		"x = rand_roll 1 6\n",
		"import screen\nev = inbox as screen.event\nscreen_open screen.screen ev\n",
		"import synth\nsynth_play synth.melody\n",
	} {
		if _, err := ParseProgram(privSrc); err == nil || !strings.Contains(err.Error(), "cannot be called directly") {
			t.Fatalf("expected internal built-in TypeError for %q, got: %v", privSrc, err)
		}
	}
}

func TestOneBasedIndexingAndListWithUpdates(t *testing.T) {
	// 1. Static SyntaxError on literal `.0` in index access and `with .0 = ...`, plus mixed `with` and `list as ... with .1`
	badSyntax := []struct {
		src     string
		wantSub string
	}{
		{"xs = list 10, 20\nsay xs.0\n", "List indices in Ease are 1-based; use '.1' for the first element"},
		{"xs = list 10, 20\nys = xs with .0 = 99\n", "List indices in Ease are 1-based; use '.1' for the first element"},
		{"xs = list 10, 20\nys = xs with .1 = 99, x = 2\n", "Cannot mix record field updates and list index updates"},
		{"def p\n  x = 0\nend\nxs = list as p with\n  .1 = 5\nend\n", "Expected field name after 'with'"},
	}
	for _, tc := range badSyntax {
		if _, err := ParseProgram(tc.src); err == nil || !strings.Contains(err.Error(), tc.wantSub) {
			t.Fatalf("expected SyntaxError %q for %q, got: %v", tc.wantSub, tc.src, err)
		}
	}

	// 2. Static TypeError checks for `with .<index> = <val>`
	badTypes := []struct {
		src     string
		wantSub string
	}{
		{"x = 42\ny = x with .1 = 5\n", "'with .<index> = <val>' requires a list"},
		{"xs = list 10, 20\npos = \"1\"\ny = xs with .pos = 99\n", "List index in 'with' must be a number"},
		{"xs = list 10, 20\ny = xs with .1 = \"bad\"\n", "Cannot assign text into list of number"},
		{"def Seat = \"free\" | \"taken\"\nseats = list as Seat\ny = seats with .1 = \"typo\"\n", "Value \"typo\" is not valid for list union \"Seat\""},
	}
	for _, tc := range badTypes {
		if _, err := ParseProgram(tc.src); err == nil || !strings.Contains(err.Error(), tc.wantSub) {
			t.Fatalf("expected TypeError %q for %q, got: %v", tc.wantSub, tc.src, err)
		}
	}

	// 3. Runtime checks: 1-based indexing, out-of-range hints, immutable `with .<pos>`, computed expr, vector arithmetic
	src := `def Seat = "free" | "taken"

seats = list "free", "free", "free", "free"
pos = 2
row = 0
seat = 4
updated = seats with .1 = "taken", .pos = "taken"
computed = updated with .(row * 8 + seat) = "taken"

typed_seats = seats as Seat
typed_next = typed_seats with .3 = "taken"

multi = seats with
    .1 = "taken"
    .4 = "taken"
end

listA = list 1, 2, 3
listB = list 4, 0, 6
avg_vec = (listA + listB with .2 = 8) / 2

empty_nums = list as number
zero_idx = 0

test "1-based indexing and immutable list with updates"
    seats.1 is "free"
    updated.1 is "taken"
    updated.2 is "taken"
    updated.3 is "free"
    computed.4 is "taken"
    typed_next.3 is "taken"
    multi.1 is "taken"
    multi.4 is "taken"
    avg_vec is list 2.5, 5, 4.5

    "äöü".1 is "ä"
    "äöü".3 is "ü"

    seats.(zero_idx) ->
        error.type is "RuntimeError"
        error.message is "List index 0 out of range (1..4)"
    end
    empty_nums.1 ->
        error.type is "RuntimeError"
        error.message is "List index 1 out of range (list is empty)"
    end
    "ä".(zero_idx) ->
        error.type is "RuntimeError"
        error.message is "String index 0 out of range (1..1)"
    end
    (seats with .(zero_idx) = "taken") ->
        error.type is "RuntimeError"
        error.message is "List index 0 out of range (1..4)"
    end
    (seats with .5 = "taken") is error
    (empty_nums with .1 = 10) is error
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 21 {
		t.Fatalf("expected 21 passed / 0 failed, got %+v", report)
	}
}

func TestArrowLawVoidProceduresEffectBlocksAndNoFail(t *testing.T) {
	// 1. Happy path: one-liner `to`, void procedure, check effect block + early `->`, verdict table, match effect arms, swarm loop `->`
	happySrc := `
to add a b -> a + b

to log_step msg
    say "LOG:{msg}"
end

to shipping weight
    porto = 5
    check weight > 10
        porto = porto + 4
        log_step "heavy"
    else
        log_step "light"
    end
    -> porto
end

to classify n
    check n < 0  -> "neg"
    check n is 0 -> "zero"
    -> "pos"
end

to dispatch cmd
    out = ""
    match cmd
        "PING"
            log_step "pong"
            out = "pong"
        "STOP" -> "stopped"
        else
            out = "other:{cmd}"
    end
    -> out
end

doubled = swarm loop range 1 3
    -> it * 2
end

swarm loop (list "a", "b")
    say "item:{it}"
end

test "arrow law and effect blocks"
    add 2 3 is 5
    shipping 12 is 9
    shipping 4 is 5
    classify -1 is "neg"
    classify 0 is "zero"
    classify 7 is "pos"
    dispatch "PING" is "pong"
    dispatch "STOP" is "stopped"
    dispatch "GO" is "other:GO"
    doubled is list 2, 4, 6
end
`
	report, err := RunChecks(happySrc)
	if err != nil {
		t.Fatalf("RunChecks failed on happy arrow-law program: %v", err)
	}
	if report.Failed != 0 || report.Passed != 10 {
		t.Fatalf("expected 10 passed / 0 failed, got %+v", report)
	}

	// 2. Negative cases
	negCases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "one-liner to with say rejected",
			src:     "to f x -> say x\n",
			wantSub: "Statements and side effects are not allowed after '->'",
		},
		{
			name:    "one-liner to with assignment rejected",
			src:     "to f x -> a = 1\n",
			wantSub: "Assignments are not allowed after '->'",
		},
		{
			name:    "multiline to without -> is void (calling as value rejected)",
			src:     "to log_it msg\n  say msg\nend\nx = log_it \"hi\"\n",
			wantSub: "'log_it' is a procedure (void) and does not return a value",
		},
		{
			name:    "partial return function rejected",
			src:     "to maybe n\n  check n > 0 -> 1\nend\n",
			wantSub: "returns a value on some paths via '->' but can finish without returning a value",
		},
		{
			name:    "-> with put/close/void call rejected",
			src:     "to f ch\n  -> close ch\nend\n",
			wantSub: "'close' does not return a value",
		},
		{
			name:    "-> with void user function rejected",
			src:     "to proc x\n  say x\nend\nto f x -> proc x\n",
			wantSub: "'proc' is a procedure (void) and does not return a value",
		},
		{
			name:    "legacy bare check verdict table rejected",
			src:     "to f x\n  check\n    x > 0\n  else\n    \"bad\"\n  end\n  -> \"ok\"\nend\n",
			wantSub: "Expected condition expression after 'check'",
		},
		{
			name:    "network:reply_text is void",
			src:     "import network\nto h req\n  x = network:reply_text req 200 \"ok\"\nend\n",
			wantSub: "'network:reply_text' does not return a value",
		},
		{
			name:    "screen:open is void",
			src:     "import screen\nw = screen.screen\nev = inbox as screen.event\nx = screen:open w ev\n",
			wantSub: "'screen:open' does not return a value",
		},
		{
			name:    "fail removed with migration hint",
			src:     "fail \"boom\"\n",
			wantSub: "'fail' was removed; return an explicit error union/record with '-> ...' instead.",
		},
		{
			name:    "swarm loop without -> cannot be assigned",
			src:     "xs = swarm loop range 1 3\n  say it\nend\n",
			wantSub: "'swarm loop' without '-> <value>' does not return a value",
		},
	}

	for _, tc := range negCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantSub, err)
			}
		})
	}
}

func TestUnifiedFormatModelSaveVoidAndCastChaining(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "user.json")
	csvPath := filepath.Join(dir, "sales.csv")
	emptyCSVPath := filepath.Join(dir, "empty_sales.csv")
	txtPath := filepath.Join(dir, "note.txt")
	binPath := filepath.Join(dir, "raw.bin")

	happySrc := fmt.Sprintf(`
def user
    id   = 0
    name = "Gast"
    ok   = "true" | "false"
end

def sale
    item  = ""
    price = 0
end

test "as json, in-memory as csv, and void save"
    # 1. Default def prototype serializes via 'as json' (matching net_reply_json)
    proto_json = user as json
    parsed_proto = proto_json as json
    parsed_proto.name is "Gast"

    # 2. Record <-> JSON file roundtrip via save ... as json and load ... as json
    ada = user with id = 7, name = "Ada", ok = "true"
    save %q ada as json
    loaded_map = load %q as json
    loaded_ada = loaded_map as user
    loaded_ada is ada

    # 3. In-memory CSV parsing & serialization (typed list, empty typed list, and map list)
    csv_raw = "item,price\nTea,4.5\nCake,6\n"
    rows = csv_raw as csv
    count rows is 2
    sales = rows as sale
    sales.1.item is "Tea"
    sales.2.price is 6

    csv_back = sales as csv
    contains csv_back "item,price"
    contains csv_back "Tea,4.5"

    empty_sales = list as sale
    (empty_sales as csv) is "item,price\n"

    map_csv = rows as csv
    contains map_csv "Tea,4.5"

    # 4. save ... as csv / text / bytes
    save %q sales as csv
    reloaded_csv = load %q as csv
    reloaded_sales = reloaded_csv as sale
    count reloaded_sales is 2

    save %q empty_sales as csv
    (load %q) is "item,price\n"

    save %q "hi" as text
    (load %q) is "hi"

    save %q (list 0, 255) as bytes
    (load %q as bytes) is list 0, 255
end
`, jsonPath, jsonPath, csvPath, csvPath, emptyCSVPath, emptyCSVPath, txtPath, txtPath, binPath, binPath)

	report, err := RunChecks(happySrc)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 13 {
		t.Fatalf("expected 13 passed / 0 failed, got %+v", report)
	}

	negCases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "decode_json removed migration hint",
			src:     "x = decode_json '{}'\n",
			wantSub: "'decode_json' was removed from Ease",
		},
		{
			name:    "encode_json removed migration hint",
			src:     "def u\n  id = 1\nend\nx = encode_json u\n",
			wantSub: "'encode_json' was removed from Ease",
		},
		{
			name:    "bare number rejected by as json",
			src:     "x = 42 as json\n",
			wantSub: "Cannot format number with 'as json'",
		},
		{
			name:    "or fallback rejected on as json",
			src:     "x = '{}' as json or \"\"\n",
			wantSub: "'or' fallback cannot be used with 'as json'",
		},
		{
			name:    "or fallback rejected on as csv",
			src:     "x = \"a,b\\n1,2\" as csv or \"\"\n",
			wantSub: "'or' fallback cannot be used with 'as csv'",
		},
		{
			name: "nested list field in record rejected by as csv",
			src: `
def order
  id = 0
  tags = list as text
end
rows = list as order
x = rows as csv
`,
			wantSub: "Cannot format record \"order\" as csv: field 'order.tags' is list",
		},
		{
			name: "nested record field in record rejected by as csv",
			src: `
def addr
  city = ""
end
def person
  name = ""
  home = addr
end
rows = list as person
x = rows as csv
`,
			wantSub: "Cannot format record \"person\" as csv: field 'person.home' is addr",
		},
		{
			name:    "single record rejected by as csv",
			src:     "def u\n  id = 1\nend\nx = u as csv\n",
			wantSub: "Cannot format non-table value as csv",
		},
		{
			name:    "scalar list rejected by as csv",
			src:     "xs = list 1, 2\nx = xs as csv\n",
			wantSub: "Cannot format list of number as csv",
		},
		{
			name:    "save is void (cannot assign)",
			src:     "x = save \"./out.txt\" \"hi\"\n",
			wantSub: "'save' does not return a value",
		},
		{
			name:    "save ... as json is void (cannot assign)",
			src:     "def u\n  id = 1\nend\nx = save \"./u.json\" u as json\n",
			wantSub: "'save' does not return a value",
		},
		{
			name:    "bare save on record without as format rejected",
			src:     "def u\n  id = 1\nend\nsave \"./u.json\" u\n",
			wantSub: "Cannot save u without an explicit format",
		},
		{
			name:    "bare save on list without as format rejected",
			src:     "xs = list 1, 2\nsave \"./raw.bin\" xs\n",
			wantSub: "Cannot save list of number without an explicit format",
		},
		{
			name:    "save raw text as json rejected",
			src:     "txt = \"hi\"\nsave \"./out.json\" txt as json\n",
			wantSub: "Cannot save raw text with 'as json'",
		},
		{
			name:    "chained as without parens rejected statically",
			src:     "def u\n  id = 1\nend\nx = '{}' as json as u\n",
			wantSub: "Cannot chain multiple 'as' conversions in one expression",
		},
		{
			name:    "chained as with parens rejected statically",
			src:     "def u\n  id = 1\nend\nx = ('{}' as json) as u\n",
			wantSub: "Cannot chain multiple 'as' conversions in one expression",
		},
		{
			name:    "chained as in or fallback rejected statically",
			src:     "x = \"bad\" as number or \"12\" as number\n",
			wantSub: "Cannot chain multiple 'as' conversions in one expression",
		},
	}

	for _, tc := range negCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantSub, err)
			}
		})
	}
}

func TestTopDownOrderIndependentDefsAndUnions(t *testing.T) {
	topDownSrc := `
def BookingOutcome = Hall | BookingRejected

def Hall
    rows = list as Row
end

def Row
    chairs = list as Chair
end

def Chair
    state = "free" | "occupied" | "reserved"
end

def BookingRejected
    reason = "out_of_bounds" | "already_occupied"
end

def Parent
    child = Child
end

def Child
    label = "nested"
end

to book_hall
    c1 = Chair with state = "free"
    c2 = Chair with state = "occupied"
    c3 = Chair with state = "reserved"
    r1 = Row with chairs = list c1, c2
    r2 = Row with chairs = list c1, c2, c3
    -> Hall with rows = list r1, r2
end

to describe_outcome out
    check out is Hall            -> out.rows.2.chairs.3.state
    check out is BookingRejected -> out.reason
end

p = Parent
say p.child.label
h = book_hall
say describe_outcome h

test "top-down defs and unions work in any declaration order"
    p.child.label is "nested"
    h is Hall
    h.rows.2.chairs.3.state is "reserved"
    describe_outcome h is "reserved"
    describe_outcome (BookingRejected with reason = "out_of_bounds") is "out_of_bounds"
end
`
	var out bytes.Buffer
	if _, err := EvalProgram(topDownSrc, &out); err != nil {
		t.Fatalf("top-down EvalProgram failed: %v", err)
	}
	if got, want := strings.TrimSpace(out.String()), "nested\nreserved"; got != want {
		t.Fatalf("unexpected output: got %q, want %q", got, want)
	}
	report, err := RunChecks(topDownSrc)
	if err != nil {
		t.Fatalf("top-down RunChecks failed: %v", err)
	}
	if report.Passed != 5 || report.Failed != 0 {
		t.Fatalf("expected 5 passed / 0 failed checks, got %d passed / %d failed", report.Passed, report.Failed)
	}

	// Direct record cycles must fail statically without stack overflow.
	cycleCases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "mutual direct record cycle",
			src:  "def A\n    b = B\nend\ndef B\n    a = A\nend\n",
			want: "Cyclic record definition",
		},
		{
			name: "self direct record cycle",
			src:  "def A\n    a = A\nend\n",
			want: `Cyclic record definition involving "A"`,
		},
	}
	for _, tc := range cycleCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected static TypeError containing %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestContainsOnAtomicListsStaticAndRuntime(t *testing.T) {
	// Valid static & runtime cases
	validSrc := `
def Role = "admin" | "guest"
def Scalar = number | text

nums = list 13, 16
words = list "alpha", "beta"
roles = (list "admin", "guest") as Role
mixed = list as Scalar
mixed = append mixed 13
mixed = append mixed "b"
empty_json = '[]' as json

test "contains on atomic lists"
  contains nums 16
  not (contains nums 0)
  contains words "beta"
  contains roles "admin"
  contains mixed 13
  contains mixed "b"
  not (contains empty_json "x")
  not (contains empty_json 42)
end
`
	if _, err := ParseProgram(validSrc); err != nil {
		t.Fatalf("ParseProgram failed on valid atomic list contains: %v", err)
	}
	report, err := RunChecks(validSrc)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 8 || report.Failed != 0 {
		t.Fatalf("expected 8 passed / 0 failed, got %d/%d: %+v", report.Passed, report.Failed, report.Results)
	}

	// Static TypeError cases
	badCases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "number_in_text_list",
			src:  "xs = list \"a\", \"b\"\ncheck contains xs 32\n  say \"ok\"\nend\n",
			want: "Cannot check 'contains' for number in list of text",
		},
		{
			name: "union_typo_literal",
			src:  "def Role = \"admin\" | \"guest\"\nroles = (list \"admin\", \"guest\") as Role\ncheck contains roles \"admn\"\n  say \"ok\"\nend\n",
			want: `Value "admn" is not valid for list union "Role"`,
		},
		{
			name: "record_list_rejected_with_where_hint",
			src:  "def User\n  name = \"\"\nend\nusers = list as User\nu = User with name = \"Aria\"\ncheck contains users u\n  say \"ok\"\nend\n",
			want: "Use 'count (xs where it is ...) > 0' to query record lists",
		},
		{
			name: "record_in_mixed_scalar_union_rejected",
			src:  "def User\n  name = \"\"\nend\ndef Scalar = number | text\nxs = list as Scalar\nu = User with name = \"Aria\"\ncheck contains xs u\n  say \"ok\"\nend\n",
			want: `Record "User" is not a member of list union "Scalar"`,
		},
		{
			name: "record_in_empty_json_rejected",
			src:  "def User\n  name = \"\"\nend\nxs = '[]' as json\nu = User with name = \"Aria\"\ncheck contains xs u\n  say \"ok\"\nend\n",
			want: "Cannot use 'contains' with non-atomic User",
		},
		{
			name: "condition_cannot_be_stored",
			src:  "xs = list 1, 2\nflag = contains xs 1\n",
			want: "Conditions cannot be stored or printed",
		},
	}

	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected TypeError containing %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestCallSiteParameterTypecheckAndReturnInference(t *testing.T) {
	// 1. Wrong list element type passed to function using `sum`
	badSumList := `
to total prices
  -> sum prices
end
bad = list "a", "b"
say total bad
`
	if _, err := ParseProgram(badSumList); err == nil || !strings.Contains(err.Error(), "In call to 'total'") || !strings.Contains(err.Error(), "sum requires a list of numbers") {
		t.Fatalf("expected call-site TypeError for total(list of text), got: %v", err)
	}

	// 2. Wrong record field accessed inside specialized function
	badFieldInFn := `
def User
  name = ""
end
to sum_orders orders
  matched = orders where it.amount > 0
  -> count matched
end
users = list as User
say sum_orders users
`
	if _, err := ParseProgram(badFieldInFn); err == nil || !strings.Contains(err.Error(), "In call to 'sum_orders'") || !strings.Contains(err.Error(), `has no field "amount"`) {
		t.Fatalf("expected call-site TypeError for missing field 'amount', got: %v", err)
	}

	// 3. Wrong scalar type passed to arithmetic function
	badScalar := `
to add_ten x
  -> x + 10
end
say add_ten "hello"
`
	if _, err := ParseProgram(badScalar); err == nil || !strings.Contains(err.Error(), "In call to 'add_ten'") {
		t.Fatalf("expected call-site TypeError for add_ten \"hello\", got: %v", err)
	}

	// 4. Return type propagation enables downstream field typo checks
	badReturnField := `
def Order
  amount = 0
end
to make_orders n
  xs = list as Order
  xs = append xs (Order with amount = n)
  -> xs
end
orders = make_orders 42
say orders.1.amnt
`
	if _, err := ParseProgram(badReturnField); err == nil || !strings.Contains(err.Error(), `Order has no field "amnt"`) {
		t.Fatalf("expected downstream field typo TypeError via return inference, got: %v", err)
	}

	// 5. Recursive function (fib) terminates cleanly and infers number return
	recSrc := `
to fib n
  check n <= 1 -> n
  -> (fib (n - 1)) + (fib (n - 2))
end
res = fib 6
say res + 1
`
	if _, err := ParseProgram(recSrc); err != nil {
		t.Fatalf("expected recursive fib to typecheck cleanly, got: %v", err)
	}
}

func TestStrictValueDisciplineImmutableParamsAndTwoFormCheck(t *testing.T) {
	// 1. Happy path covering immutable params (with local copies), World B void procedures,
	//    explicit match-else `->`, sequential union rest-narrowing (record + scalar + Form 2),
	//    and exhaustive sequential union guards without dummy trailing `->`.
	happySrc := `
def Ok
    value = 0
end
def Err
    msg = ""
end
def Result = Ok | Err
def MaybeNum = number | "none"
def Light = "red" | "yellow" | "green"

to bump_local x
    cur = x
    cur = cur + 1
    -> cur
end

to log_note msg
    say "note:{msg}"
end

to unwrap_form1 r
    check r is Err -> 0
    -> r.value
end

to unwrap_form2 r
    check r is Err
        log_note r.msg
        -> 0
    end
    -> r.value
end

to plus_one v
    check v is "none" -> 0
    -> v + 1
end

to light_action l
    check l is "red"    -> "stop"
    check l is "yellow" -> "slow"
    check l is "green"  -> "go"
end

to parse_cmd raw
    res = match raw
        "ping" -> "pong"
        else
            log_note "fallback:{raw}"
            -> "unknown"
    end
    -> res
end

test "strict value discipline, rest-narrowing, and exhaustive sequential check"
    bump_local 4 is 5
    ok_res = (Ok with value = 42) as Result
    err_res = (Err with msg = "boom") as Result
    unwrap_form1 ok_res is 42
    unwrap_form1 err_res is 0
    unwrap_form2 ok_res is 42
    unwrap_form2 err_res is 0

    m1 = 9 as MaybeNum
    m2 = "none" as MaybeNum
    plus_one m1 is 10
    plus_one m2 is 0

    light_action "red" is "stop"
    light_action "yellow" is "slow"
    light_action "green" is "go"

    parse_cmd "ping" is "pong"
    parse_cmd "xyz" is "unknown"
end
`
	report, err := RunChecks(happySrc)
	if err != nil {
		t.Fatalf("RunChecks failed on happy strict-discipline program: %v", err)
	}
	if report.Failed != 0 || report.Passed != 12 {
		t.Fatalf("expected 12 passed / 0 failed, got %+v", report)
	}

	// REPL mode still permits a trailing pure value expression at top-level.
	if _, err := ParseProgramForRepl("x = 10\nx + 5\n"); err != nil {
		t.Fatalf("expected ParseProgramForRepl to allow trailing value expr, got: %v", err)
	}

	// 2. Negative cases
	negCases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "immutable param direct reassignment",
			src:     "to f x\n  x = x + 1\n  -> x\nend\n",
			wantSub: `Cannot reassign function parameter "x"`,
		},
		{
			name:    "immutable param nested reassignment",
			src:     "to f x\n  check x > 0\n    x = 0\n  end\n  -> x\nend\n",
			wantSub: `Cannot reassign function parameter "x"`,
		},
		{
			name:    "unused standalone pure expression at top-level",
			src:     "x = 1\nx + 2\n",
			wantSub: "Unused value expression has no effect",
		},
		{
			name:    "unused standalone pure expression in function",
			src:     "to f x\n  x + 1\n  -> x\nend\n",
			wantSub: "Unused value expression has no effect",
		},
		{
			name:    "discarded return value of function call",
			src:     "to add a b -> a + b\nadd 1 2\n",
			wantSub: "Return value of 'add' is unused",
		},
		{
			name:    "unused local variable inside function",
			src:     "to f x\n  tmp = x + 1\n  -> x\nend\n",
			wantSub: `Local variable "tmp" is assigned but never used`,
		},
		{
			name:    "unused local variable inside swarm loop",
			src:     "xs = swarm loop range 1 3\n  dead = it * 2\n  -> it\nend\nsay count xs\n",
			wantSub: `Local variable "dead" is assigned but never used`,
		},
		{
			name:    "unused local variable inside check block gets block-local hint",
			src:     "to f cond\n  check cond is 1\n    coffee = \"yes\"\n  end\n  -> \"no\"\nend\n",
			wantSub: "Variables first assigned inside a 'check'/'loop' block stay local to that block; initialize 'coffee' before the block if you want to read it afterwards.",
		},
		{
			name:    "match expr else without explicit arrow rejected",
			src:     "to f raw\n  res = match raw\n    \"a\" -> 1\n    else\n      say raw\n      2\n  end\n  -> res\nend\n",
			wantSub: "must end with '-> <expression>'",
		},
		{
			name:    "empty void procedure rejected",
			src:     "to noop\nend\n",
			wantSub: "Procedure 'noop' has an empty body and no '->' return",
		},
		{
			name:    "bare check verdict table rejected",
			src:     "to f x\n  check\n    x > 0 -> 1\n  end\n  -> 0\nend\n",
			wantSub: "Expected condition expression after 'check'",
		},
		{
			name:    "else arrow in check block rejected",
			src:     "to f x\n  check x > 0\n    -> 1\n  else -> 0\n  end\nend\n",
			wantSub: "'else ->' is not valid in a 'check' block",
		},
		{
			name:    "non-exhaustive sequential union check",
			src:     "def RGB = \"r\" | \"g\" | \"b\"\nto pick c\n  check c is \"r\" -> 1\n  check c is \"g\" -> 2\nend\n",
			wantSub: `Non-exhaustive check on 'RGB' (Missing case: "b")`,
		},
	}

	for _, tc := range negCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantSub, err)
			}
		})
	}
}

func TestCrossModuleCallSiteAsTypeAndUnion(t *testing.T) {
	dir := t.TempDir()
	modPath := filepath.Join(dir, "mod.ease")
	modSrc := `
def Config
    port = 0
end

def Err
    msg = ""
end

def Resp = Config | Err

to load_cfg raw
    m = raw as json
    -> m as Config
end

to load_resp raw
    m = raw as json
    -> m as Resp
end

to unwrap_port resp
    check resp is Err -> 0
    -> resp.port
end
`
	if err := os.WriteFile(modPath, []byte(modSrc), 0o644); err != nil {
		t.Fatalf("write mod.ease: %v", err)
	}

	mainPath := filepath.Join(dir, "main.ease")
	mainSrc := `
import "./mod.ease" as sol

cfg = sol:load_cfg '{"port": 80}'
r1  = sol:load_resp '{"port": 443}'
r2  = sol:load_resp '{"msg": "offline"}'

say "{cfg.port}:{sol:unwrap_port r1}:{sol:unwrap_port r2}"

test "cross-module call-site as type and union"
    c = sol:load_cfg '{"port": 80}'
    c is sol.Config
    c.port is 80

    resp_ok = sol:load_resp '{"port": 443}'
    resp_ok is sol.Config
    sol:unwrap_port resp_ok is 443

    resp_err = sol:load_resp '{"msg": "boom"}'
    resp_err is sol.Err
    sol:unwrap_port resp_err is 0
end
`
	var out bytes.Buffer
	if _, err := EvalProgramFromFile(mainSrc, mainPath, &out); err != nil {
		t.Fatalf("EvalProgramFromFile failed: %v", err)
	}
	if got, want := strings.TrimSpace(out.String()), "80:443:0"; got != want {
		t.Fatalf("unexpected output: got %q, want %q", got, want)
	}
	report, err := RunChecksFromFile(mainSrc, mainPath)
	if err != nil {
		t.Fatalf("RunChecksFromFile failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 6 {
		t.Fatalf("expected 6 passed / 0 failed, got %+v", report)
	}

	// Returned record type is mapped back to sol.Config so downstream field typos are caught statically.
	badTypoSrc := `
import "./mod.ease" as sol
cfg = sol:load_cfg '{"port": 80}'
say cfg.prot
`
	if _, err := ParseProgramFromFile(badTypoSrc, mainPath); err == nil || !strings.Contains(err.Error(), `Config has no field "prot"`) {
		t.Fatalf("expected static field typo error on returned sol.Config, got: %v", err)
	}

	// Returned union type is mapped back to sol.Resp so unnarrowed field access is caught statically.
	badUnionFieldSrc := `
import "./mod.ease" as sol
r = sol:load_resp '{"port": 443}'
say r.port
`
	if _, err := ParseProgramFromFile(badUnionFieldSrc, mainPath); err == nil || !strings.Contains(err.Error(), "Cannot read field '.port'") {
		t.Fatalf("expected static unnarrowed union field error on returned sol.Resp, got: %v", err)
	}
}

func TestQualifiedStdlibAndModuleRecordWithFieldCheck(t *testing.T) {
	dir := t.TempDir()
	modPath := filepath.Join(dir, "mod.ease")
	modSrc := `
def Config
    port = 80
    mode = "dev" | "prod"
end
`
	if err := os.WriteFile(modPath, []byte(modSrc), 0o644); err != nil {
		t.Fatalf("write mod.ease: %v", err)
	}
	mainPath := filepath.Join(dir, "main.ease")

	// 1. Valid qualified stdlib & module `with` and `list as ... with` pass cleanly.
	happySrc := `
import screen
import "./mod.ease" as sol

lbl = screen.label with x = 10, y = 20, content = "SCORE", size = 18, color = "white"
lbl2 = lbl with content = "SCORE: 10"
labels = list as screen.label with
    x = 0, y = 0, content = "HUD"
end
labels2 = update labels with content = "HUD2"
cfg = sol.Config with port = 443, mode = "prod"
say "{lbl2.content}:{labels2.1.content}:{cfg.port}"
`
	if _, err := ParseProgramFromFile(happySrc, mainPath); err != nil {
		t.Fatalf("expected valid qualified with/list-with to pass, got: %v", err)
	}

	// 2. Invalid field names & types on qualified stdlib and module records fail statically at ParseProgram time.
	negCases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "screen.label unknown field text",
			src:     "import screen\nlbl = screen.label with text = \"SCORE\"\n",
			wantSub: `screen.label has no field "text"`,
		},
		{
			name:    "variable of screen.label unknown field text",
			src:     "import screen\nlbl = screen.label with content = \"A\"\nlbl2 = lbl with text = \"B\"\n",
			wantSub: `screen.label has no field "text"`,
		},
		{
			name:    "list as screen.label with unknown field text",
			src:     "import screen\nxs = list as screen.label with\n    text = \"SCORE\"\nend\n",
			wantSub: `screen.label has no field "text"`,
		},
		{
			name:    "update on list of screen.label unknown field text",
			src:     "import screen\nxs = list as screen.label\nys = update xs with text = \"SCORE\"\n",
			wantSub: `screen.label has no field "text"`,
		},
		{
			name:    "bare with on list of screen.label rejected",
			src:     "import screen\nxs = list as screen.label\nys = xs with content = \"SCORE\"\n",
			wantSub: `Cannot use bare 'with' on a list of records`,
		},
		{
			name:    "screen.label wrong field value type",
			src:     "import screen\nlbl = screen.label with size = \"big\"\n",
			wantSub: `Field 'screen.label.size' expected number, got text`,
		},
		{
			name:    "module sol.Config unknown field typo",
			src:     "import \"./mod.ease\" as sol\ncfg = sol.Config with typo = 1\n",
			wantSub: `sol.Config has no field "typo"`,
		},
		{
			name:    "module sol.Config invalid inline union literal",
			src:     "import \"./mod.ease\" as sol\ncfg = sol.Config with mode = \"staging\"\n",
			wantSub: `Invalid value "staging" for sol.Config.mode`,
		},
	}

	for _, tc := range negCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgramFromFile(tc.src, mainPath)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected TypeError containing %q, got: %v", tc.wantSub, err)
			}
		})
	}
}

func TestUnclosedCheckInsideFunctionDiagnosticHint(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantSubs []string
	}{
		{
			name: "bare check absorbs to end before test block",
			src:  "to f x\n  check x > 0\n  -> x\nend\n\ntest \"t\"\n  f 1 is 1\nend\n",
			wantSubs: []string{
				"SyntaxError",
				"Nested 'test' blocks are not allowed",
				"Function 'to f' (started at line 1) is still open",
				"'check' at line 2",
			},
		},
		{
			name: "unclosed check directly enclosing nested test",
			src:  "to f x\n  check x > 0\n  test \"t\"\n    f 1 is 1\n  end\nend\n",
			wantSubs: []string{
				"SyntaxError",
				"Unclosed 'check' block started at line 2",
				"add '-> <value | end>' on line 2",
			},
		},
		{
			name: "bare check absorbs to end before EOF",
			src:  "to f x\n  check x > 0\n  -> x\nend\n",
			wantSubs: []string{
				"SyntaxError",
				"Function 'to f' is missing a closing 'end'",
				"'check' at line 2",
			},
		},
		{
			name: "unclosed loop directly enclosing nested to",
			src:  "to f x\n  loop range 1 3\n    say it\n  to g y -> y\nend\n",
			wantSubs: []string{
				"SyntaxError",
				"Nested 'to' function definitions are not allowed",
				"Unclosed 'loop' block started at line 2",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil {
				t.Fatalf("expected SyntaxError, got nil")
			}
			full := err.Error()
			for _, sub := range tc.wantSubs {
				if !strings.Contains(full, sub) {
					t.Fatalf("expected error to contain %q, got:\n%s", sub, full)
				}
			}
		})
	}

	// Empty / comment-only imported module must fail with ImportError.
	dir := t.TempDir()
	emptyMod := filepath.Join(dir, "empty_mod.ease")
	if err := os.WriteFile(emptyMod, []byte("# just a comment\n"), 0o644); err != nil {
		t.Fatalf("write empty_mod.ease: %v", err)
	}
	mainPath := filepath.Join(dir, "main.ease")
	if _, err := ParseProgramFromFile("import \"./empty_mod.ease\" as em\nsay 1\n", mainPath); err == nil ||
		!strings.Contains(err.Error(), "ImportError") ||
		!strings.Contains(err.Error(), "contains no Ease declarations") {
		t.Fatalf("expected ImportError for empty imported module, got: %v", err)
	}
}

func TestErgonomicDiagnosticsImprovements1To7(t *testing.T) {
	// 1. Positive edge cases that MUST continue to work cleanly:
	//    - `rec with items = append rec.items 4`
	//    - `(xs with .1 = 99).1` postfix chaining
	happySrc := `
def bag
    items = list 1, 2
end

b = bag with items = append bag.items 4
xs = list 10, 20

test "append in with value and postfix chaining on with"
    b.items is list 1, 2, 4
    (xs with .1 = 99).1 is 99
    (b with items = list 7, 8).items.2 is 8
end
`
	report, err := RunChecks(happySrc)
	if err != nil {
		t.Fatalf("RunChecks failed on happy edge cases: %v", err)
	}
	if report.Failed != 0 || report.Passed != 3 {
		t.Fatalf("expected 3 passed / 0 failed, got %+v", report)
	}

	// 2. Diagnostic cases across items 1..7
	cases := []struct {
		name     string
		src      string
		wantSubs []string
	}{
		{
			name:     "item1 direct record field mutation",
			src:      "def p\n  x = 0\nend\na = p\na.x = 1\n",
			wantSubs: []string{"SyntaxError", "Cannot mutate field '.x' in place; values in Ease are immutable", "with x = <value>"},
		},
		{
			name:     "item1 direct list index mutation",
			src:      "xs = list 1, 2\nxs.1 = 9\n",
			wantSubs: []string{"SyntaxError", "Cannot mutate list element in place; lists in Ease are immutable", "xs with .<index> = <value>"},
		},
		{
			name:     "item1 direct field mutation inside test block",
			src:      "def p\n  x = 0\nend\ntest \"t\"\n  a = p\n  a.x = 1\nend\n",
			wantSubs: []string{"SyntaxError", "Cannot mutate field '.x' in place; values in Ease are immutable"},
		},
		{
			name:     "item1 module member reassignment",
			src:      "import system\nsystem.arguments = list \"x\"\n",
			wantSubs: []string{"SyntaxError", "Cannot reassign module member 'system.arguments'", "read-only"},
		},
		{
			name:     "item2 trailing arrow on check header",
			src:      "x = 1\ncheck x > 0 ->\n  say x\nend\n",
			wantSubs: []string{"SyntaxError", "Single-line 'check <cond> -> <value | end>' cannot span multiple lines", "omit '->' on the 'check <cond>' line"},
		},
		{
			name:     "item2 assignment after arrow",
			src:      "to f x\n  check x > 0 -> a = 1\n  -> x\nend\n",
			wantSubs: []string{"SyntaxError", "Assignments are not allowed after '->'", "omit '->' on the 'check <cond>' header"},
		},
		{
			name:     "item3 with append confusion",
			src:      "xs = list 1, 2\nys = xs with append 3\n",
			wantSubs: []string{"SyntaxError", "'append' cannot be used after 'with'", "xs = append xs <item>"},
		},
		{
			name:     "item4 list as number with .1 = 0",
			src:      "xs = list as number with\n  .1 = 0\nend\n",
			wantSubs: []string{"TypeError", `Built-in type "number" cannot be used with 'list as ... with'`},
		},
		{
			name:     "item4 list as text with",
			src:      "xs = list as text with\n  .1 = \"a\"\nend\n",
			wantSubs: []string{"TypeError", `Built-in type "text" cannot be used with 'list as ... with'`},
		},
		{
			name:     "item5 list field access with numeric var in scope",
			src:      "slots = list 10, 20\nslot_idx = 1\nsay slots.slot_idx\n",
			wantSubs: []string{"TypeError", "Cannot access field '.slot_idx' on a list", ".(slot_idx)"},
		},
		{
			name:     "item5 list field access on chained field target",
			src:      "def box\n  slots = list 10, 20\nend\nslot_idx = 1\nsay box.slots.slot_idx\n",
			wantSubs: []string{"TypeError", "Cannot access field '.slot_idx' on a list", ".(slot_idx)"},
		},
		{
			name:     "item5 list field access with untyped function param",
			src:      "to bump slots slot_idx -> slots.slot_idx\nxs = list 10, 20\nsay bump xs 1\n",
			wantSubs: []string{"TypeError", "Cannot access field '.slot_idx' on a list", ".(slot_idx)"},
		},
		{
			name:     "item5 list .index inside loop",
			src:      "xs = list 10, 20\nloop xs\n  say xs.index\nend\n",
			wantSubs: []string{"TypeError", "Cannot access field '.index' on a list", "it.index"},
		},
		{
			name:     "item5 copied loop variable .index",
			src:      "def cust\n  name = \"a\"\nend\ncs = list as cust\nloop cs\n  c = it\n  say c.index\nend\n",
			wantSubs: []string{"TypeError", `cust has no field "index"`, "it.index"},
		},
		{
			name:     "item6 missing comma in single-line record with",
			src:      "import screen\nwin = screen.screen with title = \"A\" width = 800\n",
			wantSubs: []string{"SyntaxError", "Missing ',' before 'width =' in single-line 'with'"},
		},
		{
			name:     "item6 missing comma in single-line list index with",
			src:      "xs = list 1, 2\nys = xs with .1 = 10 .2 = 20\n",
			wantSubs: []string{"SyntaxError", "Missing ',' before '.2 =' in single-line 'with'"},
		},
		{
			name:     "item6 missing comma in list as record with row",
			src:      "def p\n  x = 0\n  y = 0\nend\nxs = list as p with\n  x = 1 y = 2\nend\n",
			wantSubs: []string{"SyntaxError", "Missing ',' before 'y =' in single-line 'with'"},
		},
		{
			name:     "item7 final pure expression in function missing arrow",
			src:      "to f x\n  x + 1\nend\n",
			wantSubs: []string{"TypeError", "Unused value expression has no effect", "Ease functions have no implicit return; prefix the final expression with '-> <expr>'"},
		},
		{
			name:     "item7 final call in function missing arrow",
			src:      "to add a b -> a + b\nto f x\n  add x 1\nend\n",
			wantSubs: []string{"TypeError", "Return value of 'add' is unused", "prefix the final line with '-> add ...'"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			full := err.Error()
			for _, sub := range tc.wantSubs {
				if !strings.Contains(full, sub) {
					t.Fatalf("expected error to contain %q, got:\n%s", sub, full)
				}
			}
		})
	}

	// Empty / comment-only imported module must fail with ImportError.
	dir := t.TempDir()
	emptyMod := filepath.Join(dir, "empty_mod.ease")
	if err := os.WriteFile(emptyMod, []byte("# just a comment\n"), 0o644); err != nil {
		t.Fatalf("write empty_mod.ease: %v", err)
	}
	mainPath := filepath.Join(dir, "main.ease")
	if _, err := ParseProgramFromFile("import \"./empty_mod.ease\" as em\nsay 1\n", mainPath); err == nil ||
		!strings.Contains(err.Error(), "ImportError") ||
		!strings.Contains(err.Error(), "contains no Ease declarations") {
		t.Fatalf("expected ImportError for empty imported module, got: %v", err)
	}
}

func TestUpdateRecordListExprAndMigration(t *testing.T) {
	// 1. Happy paths: single-line, multi-line, optional where, it / it.index, untyped param,
	//    parenthesized call target, nested `with` in update value, and empty typed record list.
	happySrc := `
def tag
    label = "new"
end

def item
    name  = ""
    score = 0
    role  = "guest" | "member" | "admin"
    badge = tag
end

to make_items
    -> list as item with
        name = "Ada", score = 10, role = "guest"
        name = "Bob", score = 20, role = "admin"
    end
end

to bump_guests xs delta
    -> update xs where it.role is "guest" with
        score = it.score + delta + it.index
        role  = "member"
        badge = tag with label = "promoted"
    end
end

test "update record list happy paths"
    base = make_items
    all_bumped = update base with score = it.score + 5
    all_bumped.1.score is 15
    all_bumped.2.score is 25
    base.1.score is 10

    res = bump_guests (make_items) 100
    res.1.score is 111
    res.1.role is "member"
    res.1.badge.label is "promoted"
    res.2.score is 20
    res.2.role is "admin"

    empty = list as item
    empty2 = update empty with score = 1
    count empty2 is 0
end
`
	report, err := RunChecks(happySrc)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 9 {
		t.Fatalf("expected 9 passed / 0 failed, got %+v", report)
	}

	// 2. Negative cases: bare list with, .1 inside update, non-list, scalar list, field typo, union typo, missing with.
	negCases := []struct {
		name     string
		src      string
		wantSubs []string
	}{
		{
			name:     "bare record list with rejected",
			src:      "def p\n  x = 0\nend\nxs = list as p\nys = xs with x = 1\n",
			wantSubs: []string{"TypeError", "Cannot use bare 'with' on a list of records", "update <list> with"},
		},
		{
			name:     "positional .1 rejected inside update",
			src:      "def p\n  x = 0\nend\nxs = list as p\nys = update xs with .1 = p\n",
			wantSubs: []string{"SyntaxError", "'update' only updates named record fields"},
		},
		{
			name:     "update on non-list rejected",
			src:      "def p\n  x = 0\nend\na = p\nb = update a with x = 1\n",
			wantSubs: []string{"TypeError", "Cannot use 'update' on non-list value"},
		},
		{
			name:     "update on scalar list rejected",
			src:      "xs = list 1, 2\nys = update xs with x = 1\n",
			wantSubs: []string{"TypeError", "'update' requires a list of 'def' records, got list of number"},
		},
		{
			name:     "update unknown field rejected statically",
			src:      "def p\n  x = 0\nend\nxs = list as p\nys = update xs with typo = 1\n",
			wantSubs: []string{"TypeError", `p has no field "typo"`},
		},
		{
			name:     "update invalid union literal rejected statically",
			src:      "def p\n  role = \"a\" | \"b\"\nend\nxs = list as p\nys = update xs with role = \"c\"\n",
			wantSubs: []string{"TypeError", `Invalid value "c" for p.role`},
		},
		{
			name:     "update missing with keyword",
			src:      "def p\n  x = 0\nend\nxs = list as p\nys = update xs where it.x is 0\n",
			wantSubs: []string{"SyntaxError", "Expected 'with' after 'update' target/condition"},
		},
	}

	for _, tc := range negCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			full := err.Error()
			for _, sub := range tc.wantSubs {
				if !strings.Contains(full, sub) {
					t.Fatalf("expected error to contain %q, got:\n%s", sub, full)
				}
			}
		})
	}
}

func TestSortByMultiKeyAndUnionOrder(t *testing.T) {
	src := `
def Priority = "urgent" | "routine" | "low"

def patient
    name     = ""
    priority = Priority
    hat      = "orange" | "blue"
    age      = 0
end

to order_queue items -> sort items by it.priority, -it.age, it.name

test "sort by multi-key, stability, union declaration order, and CSV map"
    q = list as patient with
        name = "Zoe", priority = "low", hat = "blue", age = 30
        name = "Bob", priority = "urgent", hat = "orange", age = 40
        name = "Ada", priority = "urgent", hat = "blue", age = 40
        name = "Max", priority = "routine", hat = "orange", age = 25
    end

    # 1. Union declaration order (urgent < routine < low), then -age, then name
    ordered = order_queue q
    ordered.1.name is "Ada"
    ordered.2.name is "Bob"
    ordered.3.name is "Max"
    ordered.4.name is "Zoe"
    # Input list remains unmodified
    q.1.name is "Zoe"

    # 2. Inline union declaration order ("orange" before "blue") vs explicit 'as text' (lexicographical "blue" before "orange")
    by_hat = sort q by it.hat
    by_hat.1.hat is "orange"
    by_hat.3.hat is "blue"
    by_hat_az = sort q by (it.hat as text)
    by_hat_az.1.hat is "blue"

    # 3. Stability on full tie (preserves original relative order)
    ties = list as patient with
        name = "First", priority = "urgent", age = 50
        name = "Second", priority = "urgent", age = 50
    end
    stable = sort ties by it.priority
    stable.1.name is "First"
    stable.2.name is "Second"

    # 4. Works on list<map> (CSV) and with it.index / plain variable named 'by'
    rows = "item,price\npen,3\nbook,12\npad,1\n" as csv
    cheap = sort rows by (it.("price") as number)
    cheap.1.item is "pad"
    cheap.3.item is "book"

    by = 10
    by is 10
    (sort (q where it.priority is "urgent") by it.name).1.name is "Ada"
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 14 {
		t.Fatalf("expected 14 passed / 0 failed, got %+v", report)
	}

	negCases := []struct {
		name     string
		src      string
		wantSubs []string
	}{
		{
			name:     "sort by non-list rejected",
			src:      "x = sort 42 by it\n",
			wantSubs: []string{"TypeError", "'sort ... by' requires a list"},
		},
		{
			name:     "sort by condition key rejected",
			src:      "def p\n  x = 0\nend\nxs = list as p\ny = sort xs by it.x > 0\n",
			wantSubs: []string{"TypeError", "Conditions cannot be stored or printed"},
		},
		{
			name:     "sort by record key rejected",
			src:      "def p\n  x = 0\nend\nxs = list as p\ny = sort xs by it\n",
			wantSubs: []string{"TypeError", "'sort ... by' key must be number, text, or a scalar union"},
		},
		{
			name:     "sort by mixed number|text union rejected",
			src:      "def M = number | text\ndef p\n  v = M\nend\nxs = list as p\ny = sort xs by it.v\n",
			wantSubs: []string{"TypeError", "Cannot sort by mixed or record-containing union key"},
		},
		{
			name:     "sort by missing key expression",
			src:      "xs = list 1, 2\ny = sort xs by\n",
			wantSubs: []string{"SyntaxError", "Expected sort key expression after 'by'"},
		},
		{
			name:     "sort = 1 still rejected as built-in collision",
			src:      "sort = 1\n",
			wantSubs: []string{"TypeError", "built-in function"},
		},
		{
			name:     "private synth_stop intrinsic rejected in user code",
			src:      "synth_stop\n",
			wantSubs: []string{"TypeError", "synth:stop"},
		},
		{
			name:     "synth:stop cannot be assigned as a value",
			src:      "import synth\nx = synth:stop\n",
			wantSubs: []string{"TypeError", "synth:stop"},
		},
	}

	for _, tc := range negCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			full := err.Error()
			for _, sub := range tc.wantSubs {
				if !strings.Contains(full, sub) {
					t.Fatalf("expected error to contain %q, got:\n%s", sub, full)
				}
			}
		})
	}
}
