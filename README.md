<p align="center">
  <img src="assets/logo.svg" alt="Ease Logo" width="96" />
</p>

# Ease

**Ease** is a compact, cognitively simple programming language implemented in Go, designed around readable bracket-free verbs (`to` / `do`), logic-free data blueprints (`def`), explicit value spaces (union types), declarative guards (`check`), and built-in unit tests (`test`).

> **VS Code Support:** Syntax highlighting, live diagnostics (`ease lsp`), outline symbols, and autocomplete are documented in [README_VSCODE.md](README_VSCODE.md).

---

## Table of Contents

1. [Installation & Building](#installation--building)
2. [CLI Reference](#cli-reference)
3. [Language Tour](#language-tour)
   - [Variables, Constants & Types](#variables-constants--types)
   - [Functions (`to ... end`) & Calls (`do`)](#functions-to--end--calls-do)
   - [Records (`def`), `with` Copies & Union Types](#records-def-with-copies--union-types)
   - [Guards (`check`) & Branching (`if`)](#guards-check--branching-if)
   - [Type Checks & Conversions (`is`, `is not`, `as`)](#type-checks--conversions-is-is-not-as)
   - [Lists & Loops (`list`, `repeat`, `for`)](#lists--loops-list-repeat-for)
   - [Built-in Tests (`test` & `check`)](#built-in-tests-test--check)
4. [Why Ease Is Efficient for AI Agents](#why-ease-is-efficient-for-ai-agents)
5. [Standard Modules (`import`)](#standard-modules-import)
6. [Built-in Verbs](#built-in-verbs)
7. [Formal Grammar (BNF / EBNF Specification)](#formal-grammar-bnf--ebnf-specification)
8. [Running Tests & Examples](#running-tests--examples)
9. [Project Layout & Security](#project-layout--security)
10. [Contributing & License](#contributing--license)

---

## Installation & Building

### Prerequisites

- **Go** matching [`go.mod`](go.mod) (`go 1.27.4`)
- **Linux system dependencies** (required by [Ebitengine](https://ebitengine.org/) for `internal/ease/screen.go`):
  ```bash
  sudo apt-get update
  sudo apt-get install -y libc6-dev libgl1-mesa-dev libxcursor-dev libxi-dev \
    libxinerama-dev libxrandr-dev libxxf86vm-dev libasound2-dev pkg-config
  ```

### Build from Source

**Linux / macOS:**

```bash
go build -o ease ./cmd/ease
./ease version
```

**Windows (PowerShell):**

```powershell
go build -o ease.exe .\cmd\ease
.\ease.exe version
```

---

## CLI Reference

```text
ease run <file.ease> [--check] [--trace]  Run an Ease program (optionally running tests first and tracing)
ease test <file.ease> [--trace]           Run all 'test' and 'check' assertions
ease build <file.ease> [-o <out>]         Compile an Ease program into a standalone binary
ease ast <file.ease>                      Inspect parsed defs, functions, checks, and statements
ease tokens <file.ease>                   Inspect lexer tokens with line/column positions
ease repl                                 Start interactive Ease REPL
ease version                              Print Ease CLI version
```

Shorthand: `ease <file.ease> [--check] [--trace]` is equivalent to `ease run <file.ease> ...`.

---

## Language Tour

### Variables, Constants & Types

Ease has four built-in runtime types — `number`, `text`, `list`, `nothing` — plus user-defined records and union types declared with `def`.

By design, **Boolean (`bool`) and `true`/`false` literals do not exist in Ease** — omitting `bool` eliminates *boolean blindness* and makes code simpler to reason about:
1. **Conditions stay inside control flow**: Comparisons (`amount >= 50`, `status is "open"`) and combinators (`and`, `or`, `not`) evaluate directly in `check`, `if`, and `test` instead of being passed around as raw flags.
2. **Stored or returned outcomes use explicit `def` states**: Instead of returning anonymous `true`/`false`, you name what was decided (`def decision` with `answer = "yes" | "no"`, or domain variants like `def door_state = "open" | "locked"`). Every state documents itself at the call site and rejects invalid values automatically.

```ease
# Mutable binding
x = 10
x = x + 5

# Immutable constant
def pi = 3.14

# Strings in "..." support {expr} interpolation (atomic text/number only) and \{ escaping
name = "Ease"
msg = "Hello {name}: {x}, \{literal\}"

# Raw text in '...' is read 1:1 with zero interpolation and zero backslash escapes
raw_json = '{"id": 7, "name": "Gast", "role": "admin"}'
```

Line comments start with `#`.

---

### Functions (`to ... end`) & Calls (`do`)

Functions are declared with `to <name> <params...>` (space-separated, **no commas**) and implicitly return the value of their **last evaluated expression**. Functions are invoked without parentheses and without commas using `do`:

```ease
to add a b
    a + b
end

result = do add 2 3
say "Result: {result}"
```

Parentheses `(...)` are reserved for grouping sub-expressions (e.g., `total = total + (n * n)`). For nested expressions involving multiple recursive calls, assign intermediate values to clear local bindings:

```ease
to fib n
    check
        n <= 0 -> 0
        n is 1 -> 1
    end
    left = do fib n - 1
    right = do fib n - 2
    left + right
end
```

#### Why `to`? The Grammatical Guardrail (`def` / `to` / `do`)

In languages with neutral declaration keywords like `def` (Python) or `func` (Go), sloppy naming sneaks in unnoticed: developers write `def user_data()` (a noun) or `def can_ship()` (a yes/no question without an action).

In Ease, `to` acts as a **grammatical guardrail** (the English **infinitive**):
- Reading `to user_data` ("to user data") or `to can_ship` ("to can ship") aloud immediately sounds wrong.
- `to` naturally pushes you toward a genuine **action verb**: `to decide_if_to_ship`, `to calculate_discount`, `to validate_order`.

Together with `def` and `do`, Ease enforces a seamless linguistic role split — **Noun / Infinitive / Imperative** — and replaces anonymous `true`/`false` booleans with explicit decision blueprints:

```ease
# Awkward with `to`:  "to can_ship" ("to can ship") -> boolean question smell
# Clear with `to`:    def = Noun, to = Infinitive, do = Imperative

def decision                         # Noun: the passive data blueprint
    answer = "yes" | "no"
end

to decide_if_to_ship amount          # Infinitive: "to decide if to ship"
    check amount >= 50 -> decision with answer = "yes"
    decision with answer = "no"
end

test "def / to / do triad without booleans"
    d1 = do decide_if_to_ship 80     # Imperative: concrete execution
    d1.answer is "yes"

    d2 = do decide_if_to_ship 20
    d2.answer is "no"

    # Unlike an unguarded string, `def decision` rejects invalid variants:
    (decision with answer = "maybe") raises "Invalid value"
end
```

And whenever `"yes" | "no"` grows into richer domain logic, switching to `status = "ship" | "below_minimum" | "manual_review"` works seamlessly without breaking callers on raw `true`/`false` assumptions.

---

### Records (`def`), `with` Copies & Union Types

Record blueprints (`def`) are **logic-free**: they define fields, default values, and optional inline union constraints. `with` creates a modified copy without mutating the original record.

```ease
# Named union type (closed value space)
def ticket = "vip" | "standard" | "none"

# Record blueprint with defaults and inline union field
def player
    name  = "Guest"
    hp    = 100
    state = "idle" | "running" | "KO"
end

hero = player with name = "Aria", hp = 80
ko   = hero with hp = 0, state = "KO"
# hero.hp remains 80
```

Fields are read via dot notation (`hero.name`, `ko.state`).

---

### Guards (`check`) & Branching (`if`)

`check` guards validate preconditions cleanly without deeply nested conditionals:

```ease
# 1. Arrow guard checklist: first matching condition returns or executes its action
to allow_entry name age
    check
        name is not text -> "Name must be text"
        age < 18         -> "Too young"
    end
    "Welcome {name}!"
end

# 2. All-must-hold check block with shared else fallback
to validate_vip code age
    check
        code is "VIP2026"
        age >= 18
    else
        "invalid"
    end
    "valid"
end

# 3. Single-line guard
check status is "error" -> do fail "Aborted"
```

Standard conditional branching uses `if ... [else ...] end`:

```ease
to classify n
    if n > 0
        "positive"
    else
        "non-positive"
    end
end
```

---

### Type Checks, Explicit JSON (`decode_json` / `encode_json`) & Contracts (`is`, `as`)

Ease strictly separates **format parsing**, **boundary contract validation**, and **internal construction**:

- **Equality, type, and union checks**: `is`, `is not` (plus `<`, `<=`, `>`, `>=`, `and`, `or`, `not`).
- **Explicit JSON parsing & encoding**: `do decode_json <text>` parses JSON text into structured data (`map`, `list`, primitives) and fails immediately on empty or invalid JSON; `do encode_json <val>` serializes structured values (`def` records, `map`, `list`, atomic values) into JSON text.
- **Strict structural contract with `as` (zero implicit JSON magic)**:
  - **Allowed targets**: After `as` (and `list as`), Ease **only** accepts the built-in base types (`text`, `number`) or a previously declared (`def`) or imported blueprint. Any unknown identifier (`as ö`, `as bool`, `as api_usre`), forward-referenced `def`, or `def` constant fails **immediately** at parse time.
  - **Atomic-only `as text` & interpolation**: `val as text` and `"{expr}"` interpolation work **exclusively on atomic base values (`text`, `number`)** — passing a structured `def` record, `map`, or `list` aborts immediately with `TypeError`. Use `do encode_json` for JSON serialization or interpolate atomic fields like `"{user.name}"`.
  - **`val as number`**: Converts numbers or valid numeric strings strictly.
  - **`<text> as <def_union>`**: Validates that the value matches one of the union's allowed literals (`"guest" | "admin"`).
  - **`<map_or_record> as <def_record>`**: Validates **already-structured** data (`map` from `do decode_json`, or a matching record) against the `<def_record>` blueprint. Passing unstructured `text` directly to `as <def_record>` fails immediately (`TypeError`).
  - **Mandatory fields at the boundary (defaults belong exclusively to `with`)**: Every field declared in `def` **must be explicitly present** with matching type and valid union value — `as <def_record>` never fills missing fields with defaults (`Missing field '<type>.<field>'`). Extra unknown keys are tolerated for forward compatibility. Internal default construction remains exclusively `<def> with ...`.

```ease
def api_user
    id   = 0
    name = "Gast"
    role = "guest" | "admin"
end

age = "42" as number

# 1. Explicit JSON parsing (single-quoted raw text avoids backslash escapes)
# 2. Strict contract validation against `def api_user` (all fields required)
raw_map = do decode_json '{"id": 42, "name": "Ada", "role": "admin"}'
u = raw_map as api_user

# Defaults are strictly for internal `with` construction:
local_guest = api_user with id = 1

test "strict as contract vs decode_json vs with defaults"
    u.name is "Ada"
    local_guest.name is "Gast"

    '{"id": 42, "name": "Ada", "role": "admin"}' as api_user raises "Cannot validate text"
    (do decode_json '{"id": 42, "role": "admin"}') as api_user raises "Missing field 'api_user.name'"
    (do decode_json '{"id": 42, "name": "Ada", "role": "hacker"}') as api_user raises "api_user.role"
    u as text raises "Cannot convert api_user to text"
end
```

---

### Lists & Loops (`list`, `repeat`, `for`)

Lists are homogeneous and use **0-based dot indexing** (`cloud.0.vx`):

```ease
scores = list 10, 25, 40
first = scores.0

# Empty typed list
empty_nums = list as number

# Multiline record list builder
cloud = list as particle with
    x = 0, vx = 5, life = 20
    x = 10, vx = 2, life = 5
end
vx0 = cloud.0.vx
```

Loops:

```ease
# Fixed-count loop
repeat 3
    say "Tick"
end

# Inclusive range (1 through 10 inclusive)
for i in do range 1 10
    say i
end

# Implicit `it` variable and direct record field access
for cloud do
    say "{it.x} (vx={vx})"
end

# Single-line for loop
for do range 1 5 do say it end
```

---

### Built-in Tests (`test` & `check`)

Every `to ... end` function closes with its own `end`. Assertions live in standalone `test ["label"] ... end` (or `check "label" ... end`) blocks executed by `ease test <file.ease>` using `is`, `is not`, and `raises`:

```ease
to safe_div a b
    check b is 0 -> do fail "Division by zero"
    a / b
end

test
    do safe_div 10 2 is 5
    do safe_div 1 0 raises "Division by zero"
end
```

---

## Why Ease Is Efficient for AI Agents

Ease is explicitly designed to minimize token ambiguity, context overhead, and repair loops when AI models generate, inspect, and verify code:

1. **One-page learnable grammar ([`llms.txt`](llms.txt))**: A model with zero prior Ease training data can load the entire formal grammar and standard library in a single small prompt without conflicting dialects.
2. **One canonical syntax per construct**: Function declarations (`to add a b`) and calls (`do add 2 3`) never use brackets or commas; equality and type assertions both use `is` / `is not` (no `==`/`!=`/`is-not` aliases); blocks always close with `end`. Commas appear only in `list` literals and single-line `with` updates.
3. **Self-documenting states instead of `bool`, enforced by `def`/`to`/`do`**: Removing `bool` eliminates boolean blindness — conditions (`amount >= 50`) live inside `check`/`if`/`test`, while returned states use explicit `def` union types (`"yes" | "no"`, `"open" | "locked"`). At the same time, the `def` (noun) / `to` (infinitive verb) / `do` (imperative call) triad exposes sloppy names like `to can_ship` immediately when read aloud.
4. **Zero implicit magic + repairable diagnostics**: Parsing (`do decode_json`) is strictly separated from schema validation (`as <def>`, where every declared field is mandatory without silent default-filling; defaults stay exclusively in `with`). Unknown types (`as ö`, `as bool`) and mismatched fields abort immediately with exact `Line`/`Col`, source caret, `"Did you mean ...?"` suggestions, and actionable hints.
5. **Built-in self-verification & machine interfaces**: Agents can verify their own output in one command with `ease test` (including `raises` error assertions) and inspect structure deterministically via `ease ast`, `ease tokens`, and `ease lsp`.

---

## Standard Modules (`import`)

Standard modules are embedded directly into the compiled binary (`internal/ease/std/*.ease`), with optional local overrides under `./std/`:

### `import network`

Wraps HTTP networking via the `http_call` host bridge:

- **Blueprints**: `network` (`url`, `timeout = 5`, `token = ""`), `http_request` (`method`, `url`, `body`, `token`, `timeout`), `http_response` (`status`, `body`, `error`).
- **Verbs**:
  - `do ask target path` — performs HTTP `GET` (`Content-Type: application/json`, optional Bearer token) and returns the response body or `""` on transport errors or HTTP status `>= 400`.
  - `do send target path payload` — serializes `payload` via `do encode_json` and performs HTTP `POST` to `{target.url}{path}`.

See [`examples/05_network.ease`](examples/05_network.ease) (offline contract tests) and [`examples/051_network.ease`](examples/051_network.ease) (live REST example).

### `import screen`

Wraps 2D rendering and window input (via Ebitengine and `screen_present`):

- **Blueprints**: `screen` (`width = 800`, `height = 600`, `title = "Ease Window"`), `circle` (`x`, `y`, `size`, `color`), `box` (`x`, `y`, `width`, `height`, `color`), `scene` (`background`, `circles`, `boxes`), `frame_state` (`status = "open" | "closed"`, `key`, `mouse_x`, `mouse_y`).
- **Verb**: `do send target current_scene` — renders one frame (60 FPS) and returns the current `frame_state` serialized as JSON text (typically decoded via `do decode_json ... as frame_state`).
- **Headless test mode (`--check`)**: Simulates deterministic frames without opening a graphical window so UI loops can run cleanly in CI/CD environments.

See [`examples/06_window.ease`](examples/06_window.ease).

---

## Built-in Verbs

| Built-in / Construct | Behavior |
| :--- | :--- |
| `say expr` | Prints formatted value to `stdout` |
| `ask` / `ask "Prompt"` | Reads a line of input from `stdin` (optional prompt) |
| `do len xs` | Returns length of `text` (runes) or `list` |
| `do str val` | Converts a value to `text` |
| `do num val` | Converts `number` or `text` to `number` (raises `RuntimeError` on invalid input) |
| `do type val` | Returns the type name or `def` name as `text` |
| `do range start end` | Returns an inclusive list of numbers `[start .. end]` |
| `do push xs item` | Returns a new list with `item` appended (enforces element type on `list as <type>`) |
| `do abs n` / `do min a b` / `do max a b` | Numeric math helpers |
| `do fields rec` | Returns a list of field names in declaration order for a `def` record or decoded `map` |
| `do decode_json txt` | Parses valid JSON `text` into structured Ease data (`map`, `list`, or primitives); aborts on empty/invalid JSON |
| `do encode_json val` | Encodes a `def` record, `map`, `list`, or primitive into JSON `text` |
| `do fail msg` | Raises an explicit `RuntimeError` (testable with `raises`) |

---

## Formal Grammar (BNF / EBNF Specification)

The grammar below matches `internal/ease/lexer.go` and `internal/ease/parser.go` directly.

### Lexical Structure

```ebnf
WHITESPACE    ::= ( " " | "\t" | "\r" )+            (* skipped *)
COMMENT       ::= "#" { ANY_CHAR - "\n" }           (* skipped *)
NL            ::= "\n"+

LETTER        ::= UNICODE_LETTER | "_"
DIGIT         ::= [0-9]
IDENT         ::= LETTER { LETTER | DIGIT }         (* excluding reserved keywords *)

NUMBER        ::= [ "-" ] DIGIT+ [ "." DIGIT+ ]    (* "-" directly before a digit without whitespace is an atomic negative literal; use "- " for subtraction *)
STRING        ::= '"' { CHAR - ('"' | "\n" | "{" | "\\") | ESCAPE } '"'
RAW_STRING    ::= "'" { CHAR - ("'" | "\n") } "'"   (* 1:1 literal: no interpolation and no escapes *)
INTERP_STRING ::= '"' { CHAR - ('"' | "\n" | "\\") | ESCAPE | "{" EXPR_TEXT "}" } '"'
ESCAPE        ::= "\\" ( "n" | "t" | '"' | "\\" | "{" )
```

### Reserved Keywords & Operators

```text
Keywords:
  to  import  def  with  end  test  check  is  raises
  do  say  ask  as  if  else  repeat  for  in
  and  or  not  text  number  list

Operators & Punctuation:
  ->  (  )  ,  .  |  =  <  <=  >  >=  +  -  *  /  %
```

### Program & Top-Level Declarations

```ebnf
program        ::= [NL] { top_item [NL] } EOF

top_item       ::= import_decl
                 | func_decl
                 | def_decl
                 | top_check_blk
                 | top_test_blk
                 | stmt

import_decl    ::= "import" IDENT stmt_end

func_decl      ::= "to" ( IDENT | "ask" ) { IDENT }
                   ( NL | &"end" )
                   { stmt [NL] }
                   "end"

def_decl       ::= "def" IDENT "=" expr "|" term { "|" term } stmt_end    (* union decl *)
                 | "def" IDENT "=" expr stmt_end                           (* constant decl *)
                 | "def" IDENT stmt_end [NL] { def_field [NL] } "end"      (* record blueprint *)

def_field      ::= IDENT "=" expr { "|" expr } stmt_end

top_check_blk  ::= "check" STRING [NL] { check_item [NL] } "end"
top_test_blk   ::= "test" [STRING] [NL] { check_item [NL] } "end"
```

### Statements & Assertions

```ebnf
stmt           ::= "do" "say" expr stmt_end
                 | "do" expr stmt_end
                 | "say" expr stmt_end
                 | if_stmt
                 | repeat_stmt
                 | for_stmt
                 | check_guard
                 | assign_stmt
                 | expr stmt_end

assign_stmt    ::= IDENT "=" expr stmt_end

if_stmt        ::= "if" expr [NL] { stmt [NL] }
                   [ "else" [NL] { stmt [NL] } ]
                   "end" stmt_end

repeat_stmt    ::= "repeat" expr [NL] { stmt [NL] } "end" stmt_end

for_stmt       ::= "for" IDENT "in" expr ["do"] [NL] { stmt [NL] } "end" stmt_end
                 | "for" expr "do" [NL] { stmt [NL] } "end" stmt_end

check_guard    ::= "check" expr "->" stmt
                 | "check" NL { check_rule [NL] } [ "else" [NL] { stmt [NL] } ] "end" stmt_end

check_rule     ::= expr ( "->" stmt | stmt_end )

check_item     ::= if_stmt
                 | repeat_stmt
                 | for_stmt
                 | "do" "say" expr stmt_end
                 | assign_stmt
                 | expr "is" ["not"] ( "text" | "number" | "list" ) stmt_end
                 | expr ( "is" "not" | "is" | "raises" ) expr stmt_end

stmt_end       ::= NL | EOF | &("end" | "else")
```

### Expressions (Precedence Low to High)

```ebnf
expr           ::= or_expr

or_expr        ::= and_expr { "or" and_expr }
and_expr       ::= not_expr { "and" not_expr }
not_expr       ::= "not" not_expr | comparison

comparison     ::= term { cmp_op term
                        | is_op ( type_kw | term ) }
cmp_op         ::= "<" | "<=" | ">" | ">="
is_op          ::= "is" ["not"]
type_kw        ::= "text" | "number" | "list"

term           ::= factor { ( "+" | "-" ) factor }
factor         ::= unary  { ( "*" | "/" | "%" ) unary }
unary          ::= "-" unary | postfix

postfix        ::= primary { "." IDENT
                           | "." INT_PATH                (* e.g. .0 or .0.1 *)
                           | with_clause
                           | "as" cast_type
                           | bare_call_args }            (* when base is IDENT *)

with_clause    ::= "with" NL { with_field stmt_end [NL] } "end"
                 | "with" with_field { "," with_field }
with_field     ::= IDENT "=" expr

cast_type      ::= "text" | "number" | IDENT       (* must name a built-in type or previously defined/imported `def` blueprint *)

bare_call_args ::= term { term }                         (* space-separated; 'as' binds outside the full call *)

primary        ::= NUMBER
                 | STRING
                 | RAW_STRING
                 | INTERP_STRING
                 | IDENT
                 | do_expr
                 | ask_expr
                 | "(" expr ")"
                 | list_expr

do_expr        ::= "do" "ask" primary bare_call_args     (* call to user `to ask ...` *)
                 | "do" "ask" [ primary ]                (* stdin prompt *)
                 | "do" postfix                          (* starting with IDENT *)

ask_expr       ::= "ask" [ primary ]                     (* single-token prompt / input *)

list_expr      ::= "list" "as" list_elem_type [ "with" NL { with_field { "," with_field } stmt_end [NL] } "end" ]
                 | "list" expr { "," expr }

list_elem_type ::= "text" | "number" | IDENT    (* must name a built-in type or previously defined/imported `def` blueprint *)
```

> **Note on `as` binding**: Inside `bare_call_args`, nested `as` postfix casts are suppressed so `do decode_json raw_text as api_user` binds `(do decode_json raw_text) as api_user` rather than casting only the final argument.

---

## Running Tests & Examples

```bash
# Static analysis & Go test suite
go vet ./...
go test -v ./...

# Language feature test suite
for f in tests/*.ease; do ./ease test "$f"; done

# Offline examples & headless window check
./ease test examples/04_particles.ease
./ease test examples/05_network.ease
./ease run examples/06_window.ease --check
```

---

## Project Layout & Security

```text
cmd/ease/           CLI entrypoint
internal/ease/      Lexer, Parser, AST, Checker, Evaluator, Screen, Bundle & Stdlib
examples/           Executable examples and reference specifications
tests/              Language feature test files (01–08)
.github/workflows/  GitHub Actions CI for Ubuntu Linux and Windows
```

See [`SECURITY.md`](SECURITY.md) for the security model, execution boundaries, and vulnerability reporting instructions.

---

## Contributing & License

Contributions are welcome! Please read [`CONTRIBUTING.md`](CONTRIBUTING.md) before opening a pull request.

Licensed under the terms in [`LICENSE`](LICENSE).
