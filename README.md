<p align="center">
  <img src="assets/logo.svg" alt="Ease Logo" width="96" />
</p>

# Ease

**Ease** is a compact, strongly boundary-checked programming language implemented in Go. It is built around bracket-free prefix calls (`to`), logic-free data blueprints (`def`), explicit union value spaces (no `bool`), declarative guards (`check`), and built-in tests (`test`).

> **VS Code Support:** Syntax highlighting, live diagnostics (`ease lsp`), outline symbols, and autocomplete are documented in [README_VSCODE.md](README_VSCODE.md).  
> **1-Page LLM Reference:** See [`llms.txt`](llms.txt).

---

## Table of Contents

1. [Installation & CLI](#installation--cli)
2. [Language Tour](#language-tour)
   - [1. Variables, Constants & Text](#1-variables-constants--text)
   - [2. Functions (`to ... end`) & Arity Composition](#2-functions-to--end--arity-composition)
   - [3. Data Blueprints (`def`), `with` & Unions (No `bool`)](#3-data-blueprints-def-with--unions-no-bool)
   - [4. Guards (`check`), Type Narrowing & Exhaustiveness](#4-guards-check-type-narrowing--exhaustiveness)
   - [5. Lists, `where`, Batch-`with` & `loop`](#5-lists-where-batch-with--loop)
   - [6. Boundaries (`as`, JSON & File I/O / CSV)](#6-boundaries-as-json--file-io--csv)
   - [7. Modules (`import` with `.` and `:`)](#7-modules-import-with--and-)
   - [8. Built-in Tests (`test`)](#8-built-in-tests-test)
3. [Why Ease Works Well for AI Agents](#why-ease-works-well-for-ai-agents)
4. [Built-in Verbs](#built-in-verbs)
5. [Formal Grammar (EBNF)](#formal-grammar-ebnf)
6. [Running Tests & Project Layout](#running-tests--project-layout)

---

## Installation & CLI

### Prerequisites & Build

- **Go** matching [`go.mod`](go.mod) (`go 1.27.4`)
- **Linux system dependencies** (required by [Ebitengine](https://ebitengine.org/) for `screen`):
  ```bash
  sudo apt-get update
  sudo apt-get install -y libc6-dev libgl1-mesa-dev libxcursor-dev libxi-dev \
    libxinerama-dev libxrandr-dev libxxf86vm-dev libasound2-dev pkg-config
  ```

```bash
# Linux / macOS
go build -o ease ./cmd/ease

# Windows (PowerShell)
go build -o ease.exe .\cmd\ease
```

### CLI Reference

```text
ease run <file.ease> [--json] [--check] [--trace]    Run an Ease program (optionally running tests first, tracing, or emitting JSON)
ease check <file.ease> [--json]                      Parse and type-check without executing
ease test <file.ease> [--json] [--verbose] [--trace] Run all 'test' assertions
ease build <file.ease> [-o <out>]                    Compile an Ease program into a standalone binary
ease ast <file.ease> [--json]                        Inspect parsed defs, functions, checks, and statements (or full AST JSON)
ease tokens <file.ease>                              Inspect lexer tokens with line/column positions
ease repl                                            Start interactive Ease REPL
ease lsp                                             Start Language Server (stdio JSON-RPC for editors)
ease version                                         Print Ease CLI version
```

Shorthand: `ease <file.ease>` is equivalent to `ease run <file.ease>`.

---

## Language Tour

Each section builds strictly on the concepts introduced before it.

### 1. Variables, Constants & Text

Variables use plain assignment (`=`), while immutable constants use `def <name> = <value>`. Numbers are 64-bit floats (`/` is real division: `7 / 2` is `3.5`, and `-2` directly before digits is an atomic negative literal; write `a - 2` with spaces for subtraction).

```ease
def tax_rate = 0.19

subtotal = 100
total = subtotal * (1 + tax_rate)

name = "Ada"
greeting = "Hello {name}, total is {total}"
raw_json = '{"currency": "EUR"}'

say greeting
say raw_json
```

- **Interpolated vs. raw text:** `"..."` interpolates atomic `text`/`number` values inside `{expr}` and normalizes all text to Unicode NFC. `'...'` reads 1:1 with zero interpolation and zero backslash escapes.
- **Strict `+`:** `+` accepts only `number + number` or `text + text` (never implicit `"41" + 1`). Combine text and numbers with interpolation (`"{label}: {count}"`) or explicit `as text`.
- **Console input:** `answer = ask "Your name?"` reads one UTF-8-validated line from `stdin`.

---

### 2. Functions (`to ... end`) & Arity Composition

Functions are declared with `to <name> <params...>` (space-separated, no commas) and return the value of their last evaluated expression. Calls use prefix syntax with fixed arity — no parentheses around arguments, though `(...)` can group any compound argument.

```ease
to add a b
    a + b
end

to square x
    x * x
end

total_sum = add (1 + 2) 3
hypotenuse_sq = add (square 3) (square 4)

say "sum={total_sum}, c^2={hypotenuse_sq}"
```

- **`Assigned-in-Function = Local`:** Top-level variables and constants are readable inside `to` functions, while assignments inside a function always create or update local variables without mutating globals. Function parameters are immutable.

---

### 3. Data Blueprints (`def`), `with` & Unions (No `bool`)

Ease separates **noun blueprints** (`def`) from **action verbs** (`to`). Records are immutable; `with` returns an updated copy.

Instead of `bool`, `true`/`false`, or `null`, Ease models outcomes explicitly with **union types** (`|`) built from literals, `text`, `number`, record blueprints, or other unions:

```ease
def status = "OK" | "FAILED"

def ErrorMessage
    code    = 500
    message = "Unknown error"
end

def outcome = status | ErrorMessage

def player
    name  = "Guest"
    score = 0
    role  = "member" | "admin"
end

hero = player with name = "Aria", role = "admin"
leveled_up = hero with score = hero.score + 50
err = ErrorMessage with code = 404, message = "Not found"

say "{leveled_up.name} ({leveled_up.role}): {leveled_up.score}"
say "Error {err.code}: {err.message}"
```

- **Compile-time union guardrails:** Ease rejects redundant literals alongside `text`/`number`, duplicate union variants, and ambiguous record unions (records in the same union must differ structurally so casts stay unambiguous).

---

### 4. Guards (`check`), Type Narrowing & Exhaustiveness

Ease has no `if` keyword. Branching and early returns use `check`:

- **Single-line guard:** `check <cond> -> <action>`. Inside `to`, `-> <expr>` returns immediately from the function, while `-> <stmt>` (`say`, assignment) executes and continues.
- **Exhaustive `check ... end`:** Matches every variant of a closed union (or provides `else -> ...` / a post-`end` fallback). Inside `res is ErrorMessage -> ...`, `res` is statically narrowed to `ErrorMessage`.

```ease
def ErrorMessage
    code    = 500
    message = "Unknown error"
end

def outcome = "OK" | ErrorMessage

to describe_code code
    check code < 400 -> "healthy"
    "failing"
end

to process_response res
    check
        res is "OK"         -> "Request succeeded"
        res is ErrorMessage -> "Error {res.code} ({describe_code res.code}): {res.message}"
    end
end

say process_response "OK"
say process_response (ErrorMessage with code = 503, message = "Unavailable")
```

- **Conditions stay out of values:** Conditions (`>`, `<`, `>=`, `<=`, `is`, `is not`, `and`, `or`, `not`, `contains`) can only be evaluated inside `check`, `where`, and `test` — storing or printing a condition raises `TypeError`.

---

### 5. Lists, `where`, Batch-`with` & `loop`

Lists are homogeneous (`list 1, 2, 3` or typed `list as <type>`). Elements are accessed with `.0` or computed `.(i)`:

```ease
prices = list 10, 20, 50
shipping = 5
gross = prices * 1.19 + shipping

say "First gross: {gross.0}, total: {sum gross}, avg: {avg gross}"
```

#### Declarative `where` filters and batch `with` (`it` & `it.index`)

Both `where` and batch `with` bind `it` to each element and `it.index` to its 0-based index:

```ease
def order
    id     = ""
    amount = 0
    status = "open" | "paid" | "refunded"
end

orders = list as order with
    id = "A1", amount = 40,  status = "paid"
    id = "A2", amount = 120, status = "paid"
    id = "A3", amount = 80,  status = "open"
end

big_paid = orders where
    it.status is "paid"
    it.amount >= 50
end

# Batch update across every record in a list
discounted = big_paid with amount = it.amount * 0.9
say "Selected: {count discounted}, first discounted: {discounted.0.amount}"
```

#### Imperative `loop ... end` and text processing

`loop ... end` is a statement (not an expression) and binds `it` and `it.index`. Use `check <cond> -> end` to break out of the innermost loop, and `append` to build new lists:

```ease
raw = "  alpha, beta, gamma  "
parts = split (trim raw) ", "
say "Joined: {join parts " | "}"
say "Upper slice: {upper (slice (parts.0) 0 3)}"

kept = list as text
loop parts
    check it is "gamma" -> end
    check not (contains it "a") -> say "skip"
    kept = append kept "{it.index}:{it}"
end
say "Kept count: {count kept}"
```

---

### 6. Boundaries (`as`, JSON & File I/O / CSV)

External data enters as untrusted `text`, `bytes`, or `map`s and is explicitly validated at the boundary with `as`:

- `decode_json txt` parses JSON (`true`/`false` become `"true"`/`"false"` text); `encode_json val` serializes records/maps/lists back to JSON.
- `map as <def>` checks every declared field strictly (all `def` fields are mandatory; extra keys are tolerated).
- `load path [as text|csv|bytes]` reads UTF-8/NFC text (50 MB limit, BOM stripped), uniform CSV rows (`list` of `map` with positional `it.0` and header `it.("Header")` access), or raw `0..255` byte lists. `save path data` writes text or bytes and returns written byte count.

```ease
def sale_status = "paid" | "pending"

def asian_sale
    product = ""
    price   = 0
    status  = sale_status
end

rows = load "./datafiles/export_asia.csv" as csv
entries = list as asian_sale

loop rows
    entry = asian_sale with
        product = it.("Product Name")
        price   = it.1 as number
        status  = it.2 as sale_status
    end
    entries = append entries entry
end

paid = entries where it.status is "paid"
say "Paid entries: {count paid}"
```

---

### 7. Modules (`import` with `.` and `:`)

All modules — both file modules (`import "./file.ease" as mod`) and embedded standard modules (`import network`, `import screen`, optionally `as <alias>`) — are encapsulated namespaces:

- Call module verbs with **`:`** (`mod:verb arg1 arg2`).
- Access module blueprints, unions, and constants with **`.`** (`mod.blueprint`, `mod.const`).

```ease
# billing.ease
def default_tax = 0.19

def invoice
    id  = ""
    net = 0
end

to total inv
    inv.net * (1 + default_tax)
end
```

```ease
# main.ease
import "./billing.ease" as billing
import network

inv = billing.invoice with id = "INV-1", net = 100
say "Gross: {billing:total inv}"

api = network.network with url = "https://jsonplaceholder.typicode.com", timeout = 5
raw = network:fetch api "/users/1"
```

- **Standard modules:** `network` (`network.network`, `network.http_request`, `network.http_response`, `network:fetch`, `network:send`) and `screen` (`screen.screen`, `screen.circle`, `screen.box`, `screen.scene`, `screen.frame_state`, `screen:present` — run headless with `ease run <file> --check`). Local `./std/` overrides can be enabled with `EASE_STD=1`.

---

### 8. Built-in Tests (`test`)

Standalone `test ["label"] ... end` blocks run via `ease test <file.ease>` as soft assertions with full enclosing block breadcrumbs on failure.

```ease
to safe_div a b
    check b is 0 -> fail "Division by zero"
    a / b
end

def div_case
    a = 0
    b = 0
    want = 0
end

test "division table & error inspection"
    cases = list as div_case with
        a = 10, b = 2, want = 5
        a = 9,  b = 3, want = 3
    end

    loop cases
        safe_div it.a it.b is it.want
    end

    (safe_div 10 0) is error
    (safe_div 10 0) ->
        error.type is "RuntimeError"
        error.message is "Division by zero"
    end
end
```

---

## Why Ease Works Well for AI Agents

Ease pairs a tiny, unambiguous grammar with [`llms.txt`](llms.txt) so models can hold the entire language specification in context:

- **−54.6 % generated code vs. Python** across 36 benchmark tasks.
- **100 % `pass@1` accuracy** when prompted with `llms.txt`.
- **Single-command machine feedback:** `ease check --json`, `ease test --json`, `ease ast --json`, and `ease lsp` catch syntax, type, exhaustiveness, and assertion failures deterministically.

---

## Built-in Verbs

| Built-in | Behavior |
| :--- | :--- |
| `say expr` | Prints formatted value to `stdout` |
| `ask prompt` | Reads one UTF-8 line from `stdin` (use `ask ""` for empty prompt) |
| `count xs` | Returns rune length of `text` or element count of `list` |
| `type val` | Returns runtime type or `def` blueprint name as `text` |
| `range start end` | Returns inclusive `number` list `[start .. end]` (empty if `start > end`) |
| `append xs item_or_list` | Returns a new list with an item or compatible list appended |
| `slice xs start end` | Returns 0-based `[start:end)` clamped slice of `list` or UTF-8 `text` (by runes) |
| `sum xs` / `avg xs` / `sort xs` | Numeric aggregations (`sum`, `avg`) and ascending sort (`number`/`text` lists) |
| `split txt sep` / `join items sep` | Splits `text` into `list` of `text` (or runes for `""`) / joins `text` list |
| `trim txt` / `upper txt` / `lower txt` | Unicode whitespace trimming and case conversion |
| `replace txt old new` | Replaces all non-empty `old` occurrences with `new` in `text` |
| `contains txt sub` | Condition verb (for `check`, `where`, `test`) checking substring membership |
| `abs n` / `floor n` / `round n` / `min a b` / `max a b` | Numeric math helpers |
| `fields rec` | Returns field names in declaration order for a `def` record or `map` |
| `decode_json txt` / `encode_json val` | Parses JSON `text` / encodes values to JSON `text` |
| `load path [as text\|csv\|bytes]` | Reads UTF-8/NFC `text`, uniform `csv` `map` list, or `0..255` byte list |
| `save path data` | Writes UTF-8 `text` or `0..255` byte list, returning written byte count |
| `fail msg` | Raises an explicit `RuntimeError` |

---

## Formal Grammar (EBNF)

The grammar below matches [`internal/ease/lexer.go`](internal/ease/lexer.go) and [`internal/ease/parser.go`](internal/ease/parser.go).

### Lexical Structure

```ebnf
WHITESPACE    ::= ( " " | "\t" | "\r" )+            (* skipped *)
COMMENT       ::= "#" { ANY_CHAR - "\n" }           (* skipped *)
NL            ::= "\n"+

LETTER        ::= UNICODE_LETTER | "_"
DIGIT         ::= [0-9]
IDENT         ::= LETTER { LETTER | DIGIT }         (* excluding reserved keywords *)

NUMBER        ::= [ "-" ] DIGIT+ [ "." DIGIT+ ]    (* "-" directly before a digit is an atomic negative literal; use "- " for subtraction *)
STRING        ::= '"' { CHAR - ('"' | "\n" | "{" | "\\") | ESCAPE } '"'
RAW_STRING    ::= "'" { CHAR - ("'" | "\n") } "'"   (* 1:1 literal: no interpolation and no escapes *)
INTERP_STRING ::= '"' { CHAR - ('"' | "\n" | "\\") | ESCAPE | "{" EXPR_TEXT "}" } '"'
ESCAPE        ::= "\\" ( "n" | "t" | '"' | "\\" | "{" )
```

### Reserved Keywords & Operators

```text
Keywords:
  to  import  def  with  where  end  test  check  is
  say  as  else  loop
  and  or  not  text  number  list

Operators & Punctuation:
  ->  (  )  ,  .  :  |  =  <  <=  >  >=  +  -  *  /  %
```

### Program, Declarations & Statements

```ebnf
program        ::= [NL] { top_item [NL] } EOF

top_item       ::= import_decl
                 | func_decl
                 | def_decl
                 | top_test_blk
                 | stmt

import_decl    ::= "import" ( IDENT [ "as" IDENT ] | STRING "as" IDENT ) stmt_end

func_decl      ::= "to" IDENT { IDENT } ( NL | &"end" )
                   { stmt [NL] } "end" stmt_end

def_decl       ::= "def" IDENT "=" expr { "|" expr } stmt_end
                 | "def" IDENT NL { field_decl [NL] } "end" stmt_end
field_decl     ::= IDENT "=" expr { "|" expr } stmt_end

top_test_blk   ::= "test" [STRING] ( NL | &"end" )
                   { test_item [NL] } "end" stmt_end

stmt           ::= "say" expr stmt_end
                 | "end" stmt_end
                 | loop_stmt
                 | check_guard
                 | assign_stmt
                 | expr stmt_end

assign_stmt    ::= IDENT "=" expr stmt_end
loop_stmt      ::= "loop" ( "range" unary unary | expr ) [NL] { stmt [NL] } "end" stmt_end

check_action   ::= "end" stmt_end | stmt
check_guard    ::= "check" expr "->" check_action
                 | "check" NL { check_rule [NL] } [ "else" ( "->" check_action | NL { stmt [NL] } ) ] "end" stmt_end
check_rule     ::= expr ( "->" check_action | stmt_end )

test_item      ::= loop_stmt
                 | "say" expr stmt_end
                 | assign_stmt
                 | expr "->" [NL] { test_item [NL] } "end" stmt_end
                 | expr "is" ["not"] "error" stmt_end
                 | expr "is" ["not"] ( "text" | "number" | "list" ) stmt_end
                 | expr "is" ["not"] expr stmt_end
                 | expr stmt_end

stmt_end       ::= NL | EOF | &("end" | "else")
```

### Expressions (Precedence Low to High)

```ebnf
expr           ::= or_expr
or_expr        ::= and_expr { "or" and_expr }
and_expr       ::= not_expr { "and" not_expr }
not_expr       ::= "not" not_expr | comparison

comparison     ::= term { cmp_op term | is_op ( type_kw | term ) }
cmp_op         ::= "<" | "<=" | ">" | ">="
is_op          ::= "is" ["not"]
type_kw        ::= "text" | "number" | "list"

term           ::= factor { ( "+" | "-" ) factor }
factor         ::= unary  { ( "*" | "/" | "%" ) unary }
unary          ::= "-" unary | postfix

postfix        ::= primary { "." IDENT
                           | "." INT_PATH
                           | "." "(" expr ")"
                           | ":" IDENT call_args
                           | with_clause
                           | where_clause
                           | "as" cast_type
                           | call_args }

with_clause    ::= "with" NL { with_field stmt_end [NL] } "end"
                 | "with" with_field { "," with_field }
with_field     ::= IDENT "=" expr

where_clause   ::= "where" NL { expr stmt_end [NL] } "end"
                 | "where" expr

cast_type      ::= "text" | "number" | "csv" | "bytes" | IDENT [ "." IDENT ]
call_args      ::= unary{k}

primary        ::= NUMBER | STRING | RAW_STRING | INTERP_STRING | IDENT | "(" expr ")" | list_expr
list_expr      ::= "list" "as" list_elem_type [ "with" NL { with_field { "," with_field } stmt_end [NL] } "end" ]
                 | "list" expr { "," expr }
list_elem_type ::= "text" | "number" | IDENT [ "." IDENT ]
```

---

## Running Tests & Project Layout

```bash
go vet ./...
go test -v ./...
for f in tests/*.ease; do ./ease test "$f"; done
./ease test examples/05_network.ease
./ease run examples/06_window.ease --check
```

```text
cmd/ease/           CLI entrypoint
internal/ease/      Lexer, Parser, AST, Typechecker, Evaluator, Screen, Bundle & Stdlib
examples/           Executable examples and reference specifications
tests/              Language feature test files (01–08)
.github/workflows/  GitHub Actions CI for Ubuntu Linux and Windows
```

See [`SECURITY.md`](SECURITY.md), [`CONTRIBUTING.md`](CONTRIBUTING.md), and [`LICENSE`](LICENSE).
