# Ease

**Ease** is a compact, cognitively simple programming language implemented in Go, designed around readable bracket-free verbs (`to` / `do`), logic-free data blueprints (`def`), explicit value spaces (union types), declarative guards (`check`), and built-in unit tests (`test`).

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
4. [Standard Modules (`import`)](#standard-modules-import)
5. [Built-in Verbs](#built-in-verbs)
6. [Formal Grammar (BNF / EBNF Specification)](#formal-grammar-bnf--ebnf-specification)
7. [Running Tests & Examples](#running-tests--examples)
8. [Project Layout & Security](#project-layout--security)
9. [Contributing & License](#contributing--license)

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

Ease has five built-in runtime types — `number`, `text`, `bool`, `list`, `nothing` — plus user-defined records and union types declared with `def`.

```ease
# Mutable binding
x = 10
x = x + 5

# Immutable constant
def pi = 3.14

# Strings support {expr} interpolation and \{ escaping
name = "Ease"
msg = "Hello {name}: {x}, \{literal\}"
```

Line comments start with `#`.

---

### Functions (`to ... end`) & Calls (`do`)

Functions are declared with `to <name> <params...>` and implicitly return the value of their **last evaluated expression**. Functions are invoked without parentheses using `do`:

```ease
to add a, b
    a + b
end

result = do add 2, 3
say "Result: {result}"
```

Parentheses `(...)` are reserved for grouping sub-expressions (e.g., `total = total + (n * n)`). For nested expressions involving multiple recursive calls, assign intermediate values to clear local bindings:

```ease
to fib n
    check
        n <= 0 -> 0
        n == 1 -> 1
    end
    left = do fib n - 1
    right = do fib n - 2
    left + right
end
```

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
to allow_entry name, age
    check
        name is not text -> "Name must be text"
        age < 18         -> "Too young"
    end
    "Welcome {name}!"
end

# 2. All-must-hold check block with shared else fallback
to is_valid_vip code, age
    check
        code is "VIP2026"
        age >= 18
    else
        false
    end
    true
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

### Type Checks & Conversions (`is`, `is not`, `as`)

- **Equality, type, and union checks**: `is`, `is-not` / `is not` (plus `==`, `!=`, `<`, `<=`, `>`, `>=`, `and`, `or`, `not`).
- **Explicit casting with `as`**:
  - `text as number` / `as bool` / `as text`
  - `<Record> as text` serializes a `def` record to JSON text.
  - `<text> as <def_type>` unmarshals JSON into a copy of the `<def_type>` blueprint, preserving missing defaults, ignoring extra JSON keys, and enforcing field types and union `Allowed` constraints. If parsing or validation fails, the original non-matching value is preserved so `val is not <def_type>` evaluates to `true`.

```ease
age = "42" as number
check age is not number -> "Invalid number format"

u = "\{\"id\": 42, \"role\": \"admin\"\}" as api_user
check u is not api_user -> "Invalid user JSON"
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
for i in do range 1, 10
    say i
end

# Implicit `it` variable and direct record field access
for cloud do
    say "{it.x} (vx={vx})"
end

# Single-line for loop
for do range 1, 5 do say it end
```

---

### Built-in Tests (`test` & `check`)

Assertions can live inline inside a function (`test ... end`) or at the top level (`test ["label"] ... end` / `check "label" ... end`) using `is`, `is not` / `is-not`, and `raises`:

```ease
to safe_div a, b
    check b == 0 -> do fail "Division by zero"
    a / b
test
    do safe_div 10, 2 is 5
    do safe_div 1, 0 raises "Division by zero"
end
```

---

## Standard Modules (`import`)

Standard modules are embedded directly into the compiled binary (`internal/ease/std/*.ease`), with optional local overrides under `./std/`:

### `import network`

Wraps HTTP networking via the `http_call` host bridge:

- **Blueprints**: `network` (`url`, `timeout = 5`, `token = ""`), `http_request` (`method`, `url`, `body`, `token`, `timeout`), `http_response` (`status`, `body`, `error`).
- **Verbs**:
  - `do ask target, path` — performs HTTP `GET` (`Content-Type: application/json`, optional Bearer token) and returns the response body or `""` on transport errors or HTTP status `>= 400`.
  - `do send target, path, payload` — serializes `payload as text` and performs HTTP `POST` to `{target.url}{path}`.

See [`examples/05_network.ease`](examples/05_network.ease) (offline contract tests) and [`examples/051_network.ease`](examples/051_network.ease) (live REST example).

### `import screen`

Wraps 2D rendering and window input (via Ebitengine and `screen_present`):

- **Blueprints**: `screen` (`width = 800`, `height = 600`, `title = "Ease Window"`), `circle` (`x`, `y`, `size`, `color`), `box` (`x`, `y`, `width`, `height`, `color`), `scene` (`background`, `circles`, `boxes`), `frame_state` (`status = "open" | "closed"`, `key`, `mouse_x`, `mouse_y`).
- **Verb**: `do send target, current_scene` — renders one frame (60 FPS) and returns the current `frame_state` serialized as JSON text (typically consumed via `as frame_state`).
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
| `do num val` | Converts `number`, `text`, or `bool` to `number` (raises `RuntimeError` on invalid input; prefer `as number` for safe checks) |
| `do type val` | Returns the type name or `def` name as `text` |
| `do range start, end` | Returns an inclusive list of numbers `[start .. end]` |
| `do push xs, item` | Returns a new list with `item` appended (enforces element type on `list as <type>`) |
| `do abs n` / `do min a, b` / `do max a, b` | Numeric math helpers |
| `do fields rec` | Returns a list of field names in declaration order for a `def` record |
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

NUMBER        ::= DIGIT+ [ "." DIGIT+ ]
STRING        ::= '"' { CHAR - ('"' | "\n" | "{" | "\\") | ESCAPE } '"'
INTERP_STRING ::= '"' { CHAR - ('"' | "\n" | "\\") | ESCAPE | "{" EXPR_TEXT "}" } '"'
ESCAPE        ::= "\\" ( "n" | "t" | '"' | "\\" | "{" )
```

### Reserved Keywords & Operators

```text
Keywords:
  to  import  def  with  end  test  check  is  is-not  raises
  do  say  ask  as  if  else  repeat  for  in  true  false
  and  or  not  text  number  bool  list

Operators & Punctuation:
  ->  (  )  ,  .  |  =  ==  !=  <  <=  >  >=  +  -  *  /  %
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

func_decl      ::= "to" ( IDENT | "ask" ) { IDENT [ "," ] }
                   ( NL | &( "test" | "end" ) )
                   { stmt [NL] }
                   [ "test" [NL] { check_item [NL] } ]
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
                 | expr "is" ["not"] ( "text" | "number" | "bool" | "list" ) stmt_end
                 | expr ( "is" "not" | "is" | "is-not" | "raises" ) expr stmt_end

stmt_end       ::= NL | EOF | &("end" | "else" | "test")
```

### Expressions (Precedence Low to High)

```ebnf
expr           ::= or_expr

or_expr        ::= and_expr { "or" and_expr }
and_expr       ::= not_expr { "and" not_expr }
not_expr       ::= "not" not_expr | comparison

comparison     ::= term { cmp_op term
                        | is_op ( type_kw | term ) }
cmp_op         ::= "==" | "!=" | "<" | "<=" | ">" | ">="
is_op          ::= "is" ["not"] | "is-not"
type_kw        ::= "text" | "number" | "bool" | "list"

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

cast_type      ::= "text" | "number" | "bool" | IDENT

bare_call_args ::= term { "," term }                     (* 'as' binds outside the full call *)

primary        ::= NUMBER
                 | STRING
                 | INTERP_STRING
                 | "true" | "false"
                 | IDENT
                 | do_expr
                 | ask_expr
                 | "(" expr ")"
                 | list_expr

do_expr        ::= "do" "ask" primary "," bare_call_args (* call to user `to ask ...` *)
                 | "do" "ask" [ primary ]                (* stdin prompt *)
                 | "do" postfix                          (* starting with IDENT *)

ask_expr       ::= "ask" [ primary ]                     (* single-token prompt / input *)

list_expr      ::= "list" "as" list_elem_type [ "with" NL { with_field { "," with_field } stmt_end [NL] } "end" ]
                 | "list" expr { "," expr }

list_elem_type ::= "text" | "number" | IDENT
```

> **Note on `as` binding**: Inside `bare_call_args`, nested `as` postfix casts are suppressed so `do ask client, "/users/1" as api_user` binds `(do ask client, "/users/1") as api_user` rather than casting only the final argument.

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
tests/              Language feature test files (01–07)
.github/workflows/  GitHub Actions CI for Ubuntu Linux and Windows
```

See [`SECURITY.md`](SECURITY.md) for the security model, execution boundaries, and vulnerability reporting instructions.

---

## Contributing & License

Contributions are welcome! Please read [`CONTRIBUTING.md`](CONTRIBUTING.md) before opening a pull request.

Licensed under the terms in [`LICENSE`](LICENSE).
