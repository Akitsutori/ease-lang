package ease

import (
	"bytes"
	"errors"
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

to inspect_check res
    check
        res is ErrorMessage -> "check:{res.code}:{res.message}"
        else                -> "other"
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
    do inspect_check env2.res is "check:400:Bad"
    do inspect_check "OK" is "other"

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
			name: "narrowing field outside check branch on union variable",
			src: `
def ErrorMessage
    code    = 500
    message = "Unknown error"
end
def outcome = "OK" | "FEHLER" | ErrorMessage
to inspect res
    check
        res is ErrorMessage -> say "{res.code}"
        else                -> say "other"
    end
    say "{res.code}"
end
`,
			want: []string{"Cannot read field '.code'", "outcome", "before narrowing"},
		},
		{
			name: "exhaustiveness and narrowing inside loop over pushed union list",
			src: `
def ErrorMessage
    code    = 500
    message = "Unknown error"
end
def outcome = "OK" | "FEHLER" | ErrorMessage
items = list as outcome
items = do push items "OK"
loop items
    check
        it is "OK"         -> say "ok"
        it is ErrorMessage -> say "{it.code}"
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
    (user is a) raises "cannot use record type 'user' as a value on the left side of 'is'"
    (user is user) raises "cannot use record type 'user' as a value on the left side of 'is'"
    (user is not a) raises "cannot use record type 'user' as a value on the left side of 'is'"
    (user is text) raises "cannot use record type 'user' as a value on the left side of 'is'"
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
			src:  "def user\n    id = 0\nend\na = user with id = 1\ncheck user is a -> say \"ok\"\n",
			want: "cannot use record type 'user' as a value on the left side of 'is'",
		},
		{
			name: "user is user",
			src:  "def user\n    id = 0\nend\ncheck user is user -> say \"ok\"\n",
			want: "cannot use record type 'user' as a value on the left side of 'is'",
		},
		{
			name: "union on left of is",
			src:  "def role = \"guest\" | \"admin\"\ncheck role is \"guest\" -> say \"ok\"\n",
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
		{"function return", "to f\n    3 > 2\nend\n"},
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
		if !strings.Contains(err.Error(), `Conditions cannot be stored or printed. Use a union type instead, e.g.: def result = "yes" | "no"`) {
			t.Fatalf("%s: unexpected error message: %v", tc.name, err)
		}
	}

	// Union-based domain outcome works cleanly.
	unionOutcomeSrc := `
def ergebnis = "👍" | "👎"
to check_ready score
    check score >= 100 -> "👍" as ergebnis
    "👎" as ergebnis
end
say do check_ready 120
say do check_ready 50
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

m = do decode_json '{"ok": true, "label": "true"}'
r = m as reply
say r.ok
say do encode_json r

free = do decode_json '{"ok": true}'
say do encode_json free
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
def decision
    answer = "yes" | "no"
end
d = decision with answer = "no"
check d.answer is "yse" -> say "ok"
`
	if _, err := EvalProgram(staticTypoSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `"yse" is not a value of decision.answer ("yes" | "no")`) {
		t.Fatalf("expected static union literal typo error, got: %v", err)
	}

	dynamicTypoSrc := `
def decision
    answer = "yes" | "no"
end
to inspect_decision dec
    check dec.answer is "yse" -> "ok"
    "no"
end
d = decision with answer = "no"
say do inspect_decision d
`
	if _, err := EvalProgram(dynamicTypoSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `"yse" is not a value of decision.answer ("yes" | "no")`) {
		t.Fatalf("expected runtime union literal typo error, got: %v", err)
	}
}

