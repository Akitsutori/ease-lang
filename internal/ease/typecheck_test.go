package ease

import (
	"bytes"
	"errors"
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
e = (decode_json '{"name": "Ada"}') as custom_user
f = (decode_json '{"status": 200, "body": "ok", "error": ""}') as network.http_response
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
    raw_err = (decode_json '{"code": 503, "message": "Unavailable"}') as outcome
    raw_err is ErrorMessage
    process_response raw_err is "Fehler 503: Unavailable"
    process_response "OK" is "Erfolgreich verarbeitet"
    process_response "FEHLER" is "Unbekannter Fehler"

    # Record field typed by union accepts literal and ErrorMessage from JSON
    env1 = (decode_json '{"res": "OK"}') as envelope
    env1.res is "OK"
    env2 = (decode_json '{"res": {"code": 400, "message": "Bad"}}') as envelope
    env2.res is ErrorMessage
    inspect_check env2.res is "check:400:Bad"
    inspect_check "OK" is "other"

    # Heterogeneous union list + JSON array coercion
    items = list as outcome
    items = append items "OK"
    items = append items err_rec
    count items is 2
    items.2 is ErrorMessage

    json_items = (decode_json '["OK", {"code": 404, "message": "Missing"}]') as outcome
    json_items.1 is "OK"
    json_items.2 is ErrorMessage

    # Early-exit guard narrowing & compound 'and' narrowing
    unwrap_err_guard err_rec is "guard:404"
    unwrap_err_guard "OK" is "not-error"
    server_error_only raw_err is "server:503:Unavailable"
    server_error_only err_rec is "handled"

    # Reject incomplete map when decoding into union
    (decode_json '{"code": 1}') as outcome is error
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
    a1 = (decode_json '{"name": "Ada", "level": 9}') as actor1
    a1 is AdminDiffField
    g1 = (decode_json '{"name": "Bob", "ticket": "day"}') as actor1
    g1 is GuestDiffField

    p1 = (decode_json '{"val": 42}') as actor2
    p1 is NumPayload
    p2 = (decode_json '{"val": "hi"}') as actor2
    p2 is TextPayload

    r1 = (decode_json '{"kind": "admin", "name": "Ada"}') as actor3
    r1 is RoleAdmin
    r2 = (decode_json '{"kind": "guest", "name": "Bob"}') as actor3
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
items = append items "OK"
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

m = decode_json '{"ok": true, "label": "true"}'
r = m as reply
say r.ok
say encode_json r

free = decode_json '{"ok": true}'
say encode_json free
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
say inspect_decision d
`
	if _, err := EvalProgram(dynamicTypoSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `"yse" is not a value of decision.answer ("yes" | "no")`) {
		t.Fatalf("expected runtime union literal typo error, got: %v", err)
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
			wantSub: "Cannot return a value from 'match' at top level",
		},
		{
			name: "top level list match statement without assignment rejected",
			src: `
xs = list "a", "b"
match xs
  "{v}" -> v
end
`,
			wantSub: "Cannot use list 'match' without assigning its result at top level",
		},
		{
			name: "list match statement with non-expression action rejected",
			src: `
to f
  xs = list "a", "b"
  match xs
    "{v}" -> say v
  end
end
`,
			wantSub: "transforms items with expressions and cannot execute statements",
		},
		{
			name: "numeric target rejected statically",
			src: `
n = 10
match n
  "10" -> say "ten"
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
  "{v as string}" -> say v
end
`,
			wantSub: "Did you mean 'text'?",
		},
		{
			name: "bool capture type rejected",
			src: `
s = "true"
match s
  "{v as bool}" -> say v
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
  "{v as Item}" -> say "ok"
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
  "{v as RecOnly}" -> say "ok"
end
`,
			wantSub: `Union "RecOnly" contains only record variants`,
		},
		{
			name: "conflicting repeated capture number vs text",
			src: `
s = "1-1"
match s
  "{n as number}-{n as text}" -> say n
end
`,
			wantSub: `Conflicting types for repeated capture "n"`,
		},
		{
			name: "conflicting repeated capture implicit text vs number",
			src: `
s = "1-1"
match s
  "{n}-{n as number}" -> say n
end
`,
			wantSub: `Conflicting types for repeated capture "n"`,
		},
		{
			name: "capture variable shadowing function rejected",
			src: `
to helper x
  x
end
s = "abc"
match s
  "{helper}" -> say "ok"
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
      else -> i = i + 1
    end
  end
  i - 1
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
  else -> fail "bad"
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
			src:     "raw = decode_json 'null'\nx = raw as text or 42\n",
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
raw = decode_json '{"id": 1}'
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
raw = decode_json '{"x": 1}'
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
l2 = (decode_json 'null') as Flex or fb_plain
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
