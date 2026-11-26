package ease

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestMatchStatementsExpressionsAndPatterns(t *testing.T) {
	t.Run("text statement forms, routing, early return, and loop exit", func(t *testing.T) {
		src := `
to route_cmd cmd
  match cmd
    "PING" -> "PONG"
    "ECHO: {body}" -> "echo={body}"
    "PING" -> "UNREACHABLE"
  end
  -> "IGNORED"
end

to route_with_else cmd
  match cmd
    "A" -> "alpha"
  else
    prefix = "other:"
    -> prefix + cmd
  end
end

to run_side_effects items
  hits = 0
  log = ""
  loop items
    match it
      "quit" -> stop
      "add:{n as number}"
        hits = hits + n
      else
        log = "{log}{it};"
    end
  end
  -> "{hits}|{log}"
end

say route_cmd "PING"
say route_cmd "ECHO: hi:there"
say route_cmd "NOOP"
say route_with_else "Z"
say run_side_effects (list "a", "add:4", "add:6", "quit", "add:99")

line = "PING"
match line
  "PING"
    say "top_ok"
end
`
		var out bytes.Buffer
		if _, err := EvalProgram(src, &out); err != nil {
			t.Fatalf("EvalProgram failed: %v", err)
		}
		want := "PONG\necho=hi:there\nIGNORED\nother:Z\n10|a;\ntop_ok\n"
		if out.String() != want {
			t.Fatalf("unexpected output:\ngot:\n%q\nwant:\n%q", out.String(), want)
		}
	})

	t.Run("captures are immutable inside match arms", func(t *testing.T) {
		src := `
s = "KEY=val"
match s
  "KEY={v}"
    v = "changed"
end
`
		if _, err := EvalProgram(src, &bytes.Buffer{}); err == nil {
			t.Fatalf("expected runtime error when mutating capture variable")
		}
	})

	t.Run("text expressions, multi-line else, error handling, nesting, and parens", func(t *testing.T) {
		src := `
def Role = "admin" | "guest"

to classify msg
  res = match msg
    "ROLE:{r as Role}" -> "role={r}"
    "WRAP:{inner}" -> match inner
      "OK" -> "wrapped-ok"
      else -> "wrapped-other"
    end
  else
    fallback = "unknown"
    -> "{fallback}:{msg}"
  end
  -> res
end

to must_parse msg
  -> match msg
    "ID={id as number}" -> id * 2
    else -> 1 / 0
  end
end

test "text match expressions"
  classify "ROLE:admin" is "role=admin"
  classify "ROLE:root" is "unknown:ROLE:root"
  classify "WRAP:OK" is "wrapped-ok"
  must_parse "ID=21" is 42
  must_parse "ID=nope" is error
  (match "hi"
    "hi" -> "bye"
    else -> "none"
  end) is "bye"
end
`
		report, err := RunChecks(src)
		if err != nil {
			t.Fatalf("RunChecks failed: %v", err)
		}
		if report.Total != 6 || report.Passed != 6 || report.Failed != 0 {
			t.Fatalf("unexpected check report: %+v", report)
		}
	})

	t.Run("list match sieve, else fallback, it/it.index, records, where/with, and explicit return in to", func(t *testing.T) {
		src := `
def Level = "INFO" | "WARN" | "ERR"

def LogEntry
  idx   = 0
  level = "INFO" | "WARN" | "ERR"
  msg   = ""
end

to extract_errors lines
  -> match lines
    "{lvl as Level}: {body}" -> LogEntry with idx = it.index, level = lvl, msg = body
  end
end

raw_lines = list "INFO: booted", "noise line", "ERR: disk full", "WARN: low mem"
entries = extract_errors raw_lines
errs = entries where it.level is "ERR"
bumped = update errs with idx = it.idx + 100

labeled = match raw_lines
  "ERR: {body}" -> "{it.index}:{it}=>{body}"
  else -> "skip:{it.index}"
end

only_missing = match raw_lines
  "NOMATCH:{x}" -> x
end

test "list extraction and transformations"
  count entries is 3
  entries.1.idx is 1
  entries.2.idx is 3
  entries.2.level is "ERR"
  entries.2.msg is "disk full"
  bumped.1.idx is 103
  count errs is 1
  count labeled is 4
  labeled.2 is "skip:2"
  labeled.3 is "3:ERR: disk full=>disk full"
  count only_missing is 0
end
`
		report, err := RunChecks(src)
		if err != nil {
			t.Fatalf("RunChecks failed: %v", err)
		}
		if report.Total != 11 || report.Passed != 11 || report.Failed != 0 {
			t.Fatalf("unexpected check report: %+v", report)
		}
	})

	t.Run("pattern semantics: full-string anchor, 0+ captures, raw strings, rune widths, unions, backreferences", func(t *testing.T) {
		src := `
def HttpCode = 200 | 404 | 500

to check_anchor s
  match s
    "MSG: {body}" -> body
    else -> "NO"
  end
end

to check_nums s
  match s
    "{a as number} - {b as number}" -> a + b
    "{a} - {b} - {...}" -> "{a}&{b}"
    else -> "NONE"
  end
end

to check_fixed_and_raw s
  match s
    "{a: 2}{b}" -> "{a}|{b}"
    else -> "MISS"
  end
end

test "pattern semantics"
  check_anchor "MSG: a: b" is "a: b"
  check_nums "10 - 25" is 35
  check_nums "10 - 20 - 30" is "10&20"
  (match "10 - 20 - 30"
    "{a as number} - {b as number}" -> a + b
    else -> -999
  end) is -999

  # Minimum capture length is 0+ runes
  (match "KEY="
    "KEY={v}" -> "val=[{v}]"
    else -> "no"
  end) is "val=[]"
  (match "<b></b>"
    "<{t}>{body}</{t}>" -> "{t}:{body}"
    else -> "no"
  end) is "b:"

  # Single-quoted raw strings match brace literals literally
  (match '{"ok": true}'
    '{"ok": true}' -> "raw-json"
    else -> "no"
  end) is "raw-json"

  # Fixed rune widths handle multi-byte UTF-8 runes properly
  check_fixed_and_raw "äöü" is "äö|ü"
  (match "2026-04-19"
    "{y: 4}-{m: 2}-{d: 2}" -> "{y}/{m}/{d}"
    else -> "bad"
  end) is "2026/04/19"
  (match "foobarXY"
    "{...}{tag: 2}" -> tag
    else -> "bad"
  end) is "XY"
  (match "1234"
    "{a: 2}{b: 2}" -> "{a}-{b}"
    else -> "bad"
  end) is "12-34"

  # Strict number parsing rejects whitespace, underscores, NaN, Inf
  (match "ID=42"
    "ID={n as number}" -> n
    else -> -1
  end) is 42
  (match "ID=-3.5"
    "ID={n as number}" -> n
    else -> 0
  end) is -3.5
  (match "ID= 42"
    "ID={n as number}" -> n
    else -> -1
  end) is -1
  (match "ID=1_000"
    "ID={n as number}" -> n
    else -> -1
  end) is -1
  (match "ID=NaN"
    "ID={n as number}" -> n
    else -> -1
  end) is -1
  (match "ID=Inf"
    "ID={n as number}" -> n
    else -> -1
  end) is -1

  # Union captures (including numeric unions) fall through on mismatch
  (match "HTTP 404"
    "HTTP {c as HttpCode}" -> c + 1
    else -> 0
  end) is 405
  (match "HTTP 418"
    "HTTP {c as HttpCode}" -> c + 1
    else -> 0
  end) is 0

  # Backreferences enforce structural equality and inherit types
  (match "<a>hello</a>"
    "<{tag}>{body}</{tag}>" -> "{tag}:{body}"
    else -> "mismatch"
  end) is "a:hello"
  (match "<a>hello</b>"
    "<{tag}>{body}</{tag}>" -> "{tag}:{body}"
    else -> "mismatch"
  end) is "mismatch"
  (match "07:7"
    "{n as number}:{n}" -> n + 10
    else -> -1
  end) is 17
  (match "7:8"
    "{n as number}:{n}" -> n + 10
    else -> -1
  end) is -1
  (match "abab"
    "{a: 2}{a: 2}" -> a
    else -> "no"
  end) is "ab"
end
`
		report, err := RunChecks(src)
		if err != nil {
			t.Fatalf("RunChecks failed: %v", err)
		}
		if report.Total != 24 || report.Passed != 24 || report.Failed != 0 {
			t.Fatalf("unexpected check report: %+v", report)
		}
	})

	t.Run("runtime step limit and dynamic runtime errors", func(t *testing.T) {
		pathological := fmt.Sprintf("s = %q\nres = match s\n  \"{a}:{b}:{c}:{d}:{e}:END\" -> a\n  else -> \"ok\"\nend\n", strings.Repeat("x:", 60)+"NOPE")
		if _, err := EvalProgram(pathological, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "exceeded step limit") {
			t.Fatalf("expected step limit error on pathological backtracking, got: %v", err)
		}

		// Missing else on untyped text param caught at runtime in MatchExpr
		missingElseDyn := `
to run_match x
  res = match x
    "A" -> 1
  end
  -> res
end
say run_match "A"
`
		if _, err := EvalProgram(missingElseDyn, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "requires an 'else' branch") {
			t.Fatalf("expected runtime error for text match without else on untyped param, got: %v", err)
		}

		// Non-text target passed dynamically
		badTargetDyn := `
to run_match x
  match x
    "A"
      say "ok"
  end
end
run_match 123
`
		if _, err := EvalProgram(badTargetDyn, &bytes.Buffer{}); err == nil || (!strings.Contains(err.Error(), "'match' requires text target, got number") && !strings.Contains(err.Error(), "'match' requires text or a list of text, got number")) {
			t.Fatalf("expected type error for numeric target, got: %v", err)
		}

		// Mixed list outputs when branches return dynamic/untyped results
		mixedOutDyn := `
to id x
  -> x
end
lines = list "N=1", "T=hi"
res = match lines
  "N={n as number}" -> id n
  "T={t}" -> id t
end
`
		if _, err := EvalProgram(mixedOutDyn, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Mixed result types in list 'match'") {
			t.Fatalf("expected runtime mixed result type error, got: %v", err)
		}
	})
}
