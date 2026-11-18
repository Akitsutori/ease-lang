package ease

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
    api = network.network with
        url     = %q
        timeout = 2
        token   = "secret-123"
    end

    # GET via network:fetch
    raw_ok  = network:fetch api "/age/ok"
    raw_neg = network:fetch api "/age/negative"
    raw_bad = network:fetch api "/age/bad"
    raw_err = network:fetch api "/fail"

    raw_ok is "25"
    raw_err is ""

    # Typ-Konformität (as) vs. Inhaltsvalidität (to + check)
    parse_age raw_ok is 25
    parse_age raw_neg is -5
    parse_age raw_bad is error
    validate_age parse_age raw_ok is 25
    validate_age parse_age raw_neg is "Alter kann nicht negativ sein"

    # POST via network:send
    posted = network:send api "/submit" 42
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
    raw_map = decode_json '{"id": 7, "name": "Aria", "role": "admin", "extra": true}'
    raw_map is not text
    type raw_map is "map"
    raw_map.id is 7

    u = raw_map as api_user
    u is api_user
    u.id is 7
    u.name is "Aria"
    u.role is "admin"

    # Explicit JSON encoding & roundtrip
    encoded = encode_json u
    decoded = decode_json encoded as api_user
    decoded is api_user
    decoded.id is 7
    decoded.name is "Aria"
    decoded.role is "admin"

    # Defaults remain exclusively for 'with' construction
    with_defaults = api_user with id = 9
    with_defaults.name is "Gast"
    with_defaults.role is "member"

    # Raw text directly passed to 'as <def>' is rejected immediately
    '{"id": 7, "name": "Aria", "role": "admin"}' as api_user is error
    "" as api_user is error

    # Missing required fields in 'as <def>' (no silent default filling!)
    (decode_json '{"id": 7, "role": "admin"}') as api_user is error

    # Invalid JSON / non-map decoded values / wrong types / invalid union -> immediate error
    decode_json "" is error
    decode_json '{broken' is error
    decode_json 42 is error
    (decode_json 'null') as api_user is error
    (decode_json '[1, 2]') as api_user is error
    (decode_json '{"id": "not-a-num", "name": "Aria", "role": "admin"}') as api_user is error
    (decode_json '{"id": 7, "name": "Aria", "role": "superuser"}') as api_user is error
    42 as api_user is error

    # Symmetry: no JSON magic in 'as text'
    u as text is error
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
    decode_json "\{\"first\": \"{a}\", \"second\": \"{b}\"\}"
end

x = make_pair "a" "b" as my_record
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
			src:     "def user\n    id = 0\nend\nx = (decode_json '[1, 2]') as user\n",
			wantSub: `Cannot validate list as user: expected structured map or user record`,
		},
		{
			name:    "missing required field rejected for record def",
			src:     "def user\n    id = 0\n    name = \"Gast\"\nend\nx = (decode_json '{\"id\": 1}') as user\n",
			wantSub: `Missing field 'user.name'`,
		},
		{
			name:    "wrong nested field type",
			src:     "def user\n    id = 0\nend\nx = (decode_json '{\"id\": true}') as user\n",
			wantSub: `Field 'user.id' expected number, got text (true)`,
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

func TestUnifiedHomogeneousListValueSemantics(t *testing.T) {
	// Rule 1: Every runtime list is *ListValue with inferred or explicit ElemType.
	val, err := EvalProgram("range 1 3\n", &bytes.Buffer{})
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

	val, err = EvalProgram("def p\n  x = 1\nend\ninst = p with x = 2\nfields inst\n", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("fields eval failed: %v", err)
	}
	fieldsLV, ok := val.(*ListValue)
	if !ok || fieldsLV.ElemType != "text" || len(fieldsLV.Elements) != 1 {
		t.Fatalf("expected *ListValue{ElemType: text, len: 1}, got %T %+v", val, val)
	}

	val, err = EvalProgram(`decode_json '[1, 2]'`+"\n", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("decode_json array failed: %v", err)
	}
	jsonLV, ok := val.(*ListValue)
	if !ok || jsonLV.ElemType != "number" || len(jsonLV.Elements) != 2 {
		t.Fatalf("expected *ListValue{ElemType: number, len: 2} from decode_json, got %T %+v", val, val)
	}

	val, err = EvalProgram(`decode_json '[]'`+"\n", &bytes.Buffer{})
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
    (decode_json '[1, 2, 3]') as number is list 1, 2, 3
    (decode_json '["a", "b"]') as text is list "a", "b"
    users = (decode_json '[{"id": 1, "name": "Ada"}, {"id": 2, "name": "Bob"}]') as user
    count users is 2
    users.0 is user
    users.0.name is "Ada"
    users.1.id is 2

    t = (decode_json '{"title": "Red", "scores": [10, 20]}') as team
    t is team
    t.scores is list 10, 20

    (list "a", "b") as number is error
    (list 1, 2) as text is error
    (list "vip", "gold") as ticket is error
    (decode_json '[1, "bad"]') as number is error
    (decode_json '[{"id": 1, "name": "Ada"}, {"id": 2}]') as user is error
    (decode_json '{"title": "Red", "scores": [10, "bad"]}') as team is error
end

test "Rule 3: homogeneous list concatenation with append and vector +"
    combined = append (list 1, 2) (list 3, 4)
    combined is list 1, 2, 3, 4
    combined2 = append (list as number) (list 5, 6)
    combined2 is list 5, 6
    combined3 = append (list 7, 8) (decode_json '[]')
    combined3 is list 7, 8
    ((list 1, 2) + (list 3, 4)) is list 4, 6
    append (list 1, 2) (list "a", "b") is error
    append (list as number) (list as text) is error
    ((list 1, 2) + (list "a", "b")) is error
end

test "Rule 4: batch-with on record lists"
    cloud = list as particle with
        x = 0, vx = 5, life = 20
        x = 10, vx = 2, life = 5
    end
    moved = cloud with x = it.x + it.vx
    count moved is 2
    moved.0 is particle
    moved.0.x is 5
    moved.1.x is 12
    cloud.0.x is 0

    p1 = particle with x = 1, vx = 3
    p2 = particle with x = 4, vx = 6
    moved2 = (list p1, p2) with x = it.x + it.vx
    moved2.0.x is 4
    moved2.1.x is 10
    (append moved2 (list p1)).2.x is 1

    ((list 1, 2) with x = 1) is error
end

test "Rule 5: append type lock and value semantics under aliasing"
    r = range 1 1
    a = append r 2
    b = append a 3
    c = append a 4
    a is list 1, 2
    b is list 1, 2, 3
    c is list 1, 2, 4

    dyn = decode_json '[]'
    dyn_num = append dyn 42
    dyn_num is list 42
    count dyn is 0
    append dyn_num "bad" is error
    bad_empty = list as number
    append bad_empty "bad" is error
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed != 39 {
		t.Fatalf("expected 39 passed / 0 failed checks, got %d passed / %d failed:\n%+v", report.Passed, report.Failed, report.Results)
	}

	// Verify TypeError stage explicitly for cast, +, and append failures.
	typeErrCases := []struct {
		name string
		src  string
	}{
		{"cast list mismatch", `x = (list "a", "b") as number`},
		{"cast mixed json array", `x = (decode_json '[1, "bad"]') as number`},
		{"non-numeric list +", `x = (list 1, 2) + (list "a", "b")`},
		{"append list mismatch", `x = append (list 1, 2) (list "a", "b")`},
		{"append mismatch", "xs = list 1, 2\nys = append xs \"x\""},
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
moved = cloud with x = it.x + it.vx
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

	// Verify amortized O(1) append: linear chain reuses spare capacity; branching copies without aliasing corruption.
	prealloc := make([]Value, 2, 8)
	prealloc[0] = 1.0
	prealloc[1] = 2.0
	base := &ListValue{ElemType: "number", Elements: prealloc}
	b1Val, handled, err := ev.callBuiltin("append", []Value{base, 3.0}, 1, 1)
	if !handled || err != nil {
		t.Fatalf("first append failed: handled=%v err=%v", handled, err)
	}
	b1 := b1Val.(*ListValue)
	if &b1.Elements[0] != &base.Elements[0] {
		t.Fatalf("expected linear first append to reuse spare slice capacity in O(1)")
	}
	b2Val, handled, err := ev.callBuiltin("append", []Value{base, 4.0}, 1, 1)
	if !handled || err != nil {
		t.Fatalf("second branched append failed: handled=%v err=%v", handled, err)
	}
	b2 := b2Val.(*ListValue)
	if &b2.Elements[0] == &b1.Elements[0] {
		t.Fatalf("expected branched append on already-appended list to allocate a fresh backing array")
	}
	if b1.Elements[2] != 3.0 || b2.Elements[2] != 4.0 {
		t.Fatalf("value semantics corrupted: b1=%v b2=%v", b1.Elements, b2.Elements)
	}
}
