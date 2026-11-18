<p align="center">
  <img src="assets/logo.svg" alt="Ease Logo" width="96" />
</p>

# Ease

**Ease** is a compact, cognitively simple programming language implemented in Go, designed around readable bracket-free prefix calls (`to`), logic-free data blueprints (`def`), explicit value spaces (union types), declarative guards (`check`), and built-in unit tests (`test`).

> **VS Code Support:** Syntax highlighting, live diagnostics (`ease lsp`), outline symbols, and autocomplete are documented in [README_VSCODE.md](README_VSCODE.md).

---

## Table of Contents

1. [Installation & Building](#installation--building)
2. [CLI Reference](#cli-reference)
3. [Language Tour](#language-tour)
   - [Variables, Constants & Types](#variables-constants--types)
   - [Functions (`to ... end`) & Arity-Based Calls](#functions-to--end--arity-based-calls)
   - [Records (`def`), `with` Copies & Union Types](#records-def-with-copies--union-types)
   - [Guards (`check`), Type Narrowing & Exhaustiveness](#guards-check-type-narrowing--exhaustiveness)
   - [Type Checks & Conversions (`is`, `is not`, `as`)](#type-checks--conversions-is-is-not-as)
   - [Lists & Loops (`list`, `loop`)](#lists--loops-list-loop)
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

Shorthand: `ease <file.ease> [--check] [--trace]` is equivalent to `ease run <file.ease> ...`.

---

## Language Tour

### Variables, Constants & Types

Ease has four built-in runtime types — `number`, `text`, `list`, `nothing` — plus user-defined records and union types declared with `def`.

By design, **Boolean (`bool`) and `true`/`false` literals do not exist in Ease** — omitting `bool` eliminates *boolean blindness* and makes code simpler to reason about:
1. **Conditions stay inside control flow (not storable/printable values)**: Comparisons (`amount >= 50`, `status is "open"`) and combinators (`and`, `or`, `not`) evaluate directly in `check` and `test`. Attempting to store, print, pass, or return a condition (`ok = 3 > 2`, `say 3 > 2`) raises `TypeError` (`Conditions cannot be stored or printed. Use a union type instead, e.g.: def result = "yes" | "no"`).
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
raw_json = '{"id": 7, "name": "Guest", "role": "admin"}'
```

Line comments start with `#`.

**Scoping Rules** (zero implicit global mutation):
- **Read-only globals in functions**: Top-level variables and `def` constants can be read inside `to` functions, never mutated.
- **Assigned-in-Function = Local**: Any assignment `x = ...` inside a function creates or updates a local variable without touching a global of the same name. Reading a global name before assigning it locally in the same function raises `RuntimeError` (`Cannot modify global '<name>' inside a function; pass it in and return the new value instead.`).
- **Immutable parameters**: Function parameters cannot be reassigned; bind a new local variable instead.
- **Block scope**: `check` (both `->` and `else` branches) and `loop` create their own block scope; `it` and `it.index` stay strictly loop-local.

---

### Functions (`to ... end`) & Arity-Based Calls

Functions are declared with `to <name> <params...>` (space-separated, **no commas**) and implicitly return the value of their **last evaluated expression**. Functions are invoked **directly by name** without `do`, without parentheses, and without commas:

```ease
to add a b
    a + b
end

result = add 2 3
say "Result: {result}"
```

Calls resolve **by fixed arity** (right-associatively, like Lisp/Clojure without parentheses): a function of arity `k` consumes the next `k` argument expressions, so calls compose naturally without nesting brackets:

```ease
to square x
    x * x
end

to sum_squares xs
    total = 0
    loop xs
        total = total + (square it)
    end
    total
end

# `sum_squares` (arity 1) consumes `range 1 5` (arity 2) as its argument:
say sum_squares range 1 5        # prints 55
```

Parentheses `(...)` are reserved for grouping sub-expressions and compound arguments (e.g., `add (1 + 2) 3`, `fib (n - 1)`):

```ease
to fib n
    check
        n <= 0 -> 0
        n is 1 -> 1
    end
    left = fib (n - 1)
    right = fib (n - 2)
    left + right
end
```

#### Why `to`? The Grammatical Guardrail (`def` / `to`)

In languages with neutral declaration keywords like `def` (Python) or `func` (Go), sloppy naming sneaks in unnoticed: developers write `def user_data()` (a noun) or `def can_ship()` (a yes/no question without an action).

In Ease, `to` acts as a **grammatical guardrail** (the English **infinitive**):
- Reading `to user_data` ("to user data") or `to can_ship` ("to can ship") aloud immediately sounds wrong.
- `to` naturally pushes you toward a genuine **action verb**: `to decide_if_to_ship`, `to calculate_discount`, `to validate_order`.

Together with `def`, Ease enforces a seamless linguistic role split — **Noun (`def`) / Infinitive (`to`)** — and replaces anonymous `true`/`false` booleans with explicit decision blueprints:

```ease
# Awkward with `to`:  "to can_ship" ("to can ship") -> boolean question smell
# Clear with `to`:    def = Noun, to = Infinitive

def decision                         # Noun: the passive data blueprint
    answer = "yes" | "no"
end

to decide_if_to_ship amount          # Infinitive: "to decide if to ship"
    check amount >= 50 -> decision with answer = "yes"
    decision with answer = "no"
end

test "def / to without booleans"
    d1 = decide_if_to_ship 80
    d1.answer is "yes"

    d2 = decide_if_to_ship 20
    d2.answer is "no"

    # Unlike an unguarded string, `def decision` rejects invalid variants:
    (decision with answer = "maybe") is error
end
```

And whenever `"yes" | "no"` grows into richer domain logic, switching to `status = "ship" | "below_minimum" | "manual_review"` works seamlessly without breaking callers on raw `true`/`false` assumptions.

---

### Records (`def`), `with` Copies & Union Types

Record blueprints (`def`) are **logic-free**: they define fields, default values, and optional inline union constraints (or reference named unions directly). `with` creates a modified copy without mutating the original record.

Union definitions (`def <Name> = A | B | ...`) are closed value spaces built orthogonally from **4 building blocks**:
1. **Literals**: `"OK"`, `"FAILED"`, `0`, `404`
2. **Base types**: `text`, `number`
3. **Record blueprints**: `ErrorMessage`, `User`
4. **Other named unions (automatically flattened)**: e.g. `def outcome = status | ErrorMessage` flattens `status` into `outcome`

To keep unions unambiguous and clean, Ease enforces **3 compile-time guardrails**:
- **No redundancy**: A base type cannot be paired with literals of that type (`"OK" | text` -> `'OK' is already covered by 'text'`).
- **No duplicates**: Duplicate literals, base types, or record members (including across flattened unions) are rejected immediately.
- **Distinguishable records for `as`**: Two records in the same union (e.g. `Admin | Guest`) must differ in at least one mandatory field name, field type, or disjoint inline union constraint so `raw_map as outcome` can decode unambiguously.

```ease
# 1. Record blueprint
def ErrorMessage
    code    = 500
    message = "Unknown error"
end

# 2. Composed union (literals + flattened sub-union + record blueprint)
def status  = "OK" | "FAILED"
def outcome = status | ErrorMessage

# Record blueprint with defaults, union field reference, and inline union
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

### Guards (`check`), Type Narrowing & Exhaustiveness

`check` guards validate preconditions and dispatch over closed union types cleanly without deeply nested conditionals (Ease has no `if` statement — all conditional branching uses `check`):

- **Two `check` forms**:
  - **Single-line guard**: `check <cond> -> <expr_or_stmt>` (no `end`, ideal for flat guards and filtering inside `loop` blocks).
  - **Block form**: `check ... end` (for exhaustive union matching and `else` fallbacks). Incomplete `check` blocks without `->` or `else` are rejected at parse time — use `check <cond> -> fail "<msg>"` for explicit aborts.
- **Arrow Semantics (`->`)**: `->` indicates execution direction. Inside a `to` function, if the right side is a **value expression** (e.g. `res is "OK" -> "Processed"`), the entire function returns that value immediately (**early return**, even out of `loop` blocks); if the right side is a **statement** (`say ...`, assignment `cnt = cnt + 1`), it executes and continues on the next line or loop iteration. Inside a `loop`, `check <cond> -> end` exits only the innermost `loop` early. At top level (outside a `to` function), returning a value after `->` (e.g. `check x > 0 -> "A"`) is an error (`Cannot return a value from 'check' at top level; use 'say' or 'fail' instead.`).
- **Type Narrowing**: Inside a `res is ErrorMessage -> ...` branch of `check` (or after an early exit `check res is not ErrorMessage -> ...`), Ease narrows `res` to `ErrorMessage` and allows `res.code` and `res.message`. Accessing record fields outside a narrowed branch fails statically with `TypeError`.
- **Exhaustiveness Check**: A `check ... end` block matching a closed union without a fallback (`else -> ...` or a post-`end` expression) must handle every flattened variant — otherwise compilation stops with `Non-exhaustive check on '<union>' (Missing case: ...)`.
- **Explicit fallback (`else -> ...`)**: When only specific variants matter, add `else -> ...` to handle everything else explicitly.

```ease
# 1. Exhaustive union matching + Type Narrowing (no 'else' needed)
to process_response res
    check
        res is "OK"         -> "Processed successfully"
        res is "FAILED"     -> "Unknown failure"
        res is ErrorMessage -> "Error {res.code}: {res.message}"
    end
end

# 2. Explicit catch-all fallback with `else -> ...`
to log_only_success res
    check
        res is "OK" -> say "Succeeded"
        else        -> say "Something else happened"
    end
end

# 3. Precondition arrow guard checklist
to allow_entry name age
    check
        name is not text -> "Name must be text"
        age < 18         -> "Too young"
    end
    "Welcome {name}!"
end

# 4. All-must-hold check block with shared multi-line else fallback
to validate_vip code age
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

# 5. Single-line guard
check status is "error" -> fail "Aborted"
```

Conditional branching with `else` uses `check`:

```ease
to classify n
    check
        n > 0 -> "positive"
        else  -> "non-positive"
    end
end
```

---

### Type Checks, Explicit JSON (`decode_json` / `encode_json`) & Contracts (`is`, `as`)

Ease strictly separates **format parsing**, **boundary contract validation**, and **internal construction**:

- **Asymmetric type & value checks (`is`, `is not`)**:
  - `<value> is <type>` checks type or union membership (e.g., `a is user`, `"OK" is status`, `x is text`).
  - `<value> is <value>` checks structural value equality between concrete values (e.g., `a is b`, `x is 5`).
  - **Types are never allowed on the left side of `is`** (`user is a` or `user is user` immediately raises `TypeError`). Nested record defaults inside a `def` (e.g., `a = pt`) are automatically instantiated so value equality remains strictly transitive.
  - **Strict union-literal checking (typo protection)**: Comparing a union-typed value against a string literal that is not a valid variant of that union (e.g. `d.answer is "yse"` when `answer = "yes" | "no"`) immediately raises `TypeError` (`"yse" is not a value of decision.answer ("yes" | "no")`).
  - Ordering and logic operators: `<`, `<=`, `>`, `>=`, `and`, `or`, `not`.
- **Explicit JSON parsing & encoding**: `decode_json <text>` parses JSON text into structured data (`map`, `list`, primitives) and fails immediately on empty or invalid JSON (JSON `true`/`false` become `"true"` / `"false"` text). `encode_json <val>` serializes structured values (`def` records, `map`, `list`, atomic values) into JSON text; any `def` record field whose schema is `"true" | "false"` serializes back to an unquoted JSON boolean (`true`/`false`), while regular `text` fields and free maps stay JSON strings.
- **Strict structural contract with `as` (zero implicit JSON magic)**:
  - **Allowed targets**: After `as` (and `list as`), Ease **only** accepts the built-in base types (`text`, `number`) or a previously declared (`def`) or imported blueprint. Any unknown identifier (`as ö`, `as bool`, `as api_usre`), forward-referenced `def`, or `def` constant fails **immediately** at parse time.
  - **Atomic-only `as text` & interpolation**: `val as text` and `"{expr}"` interpolation work **exclusively on atomic base values (`text`, `number`)** — passing a structured `def` record, `map`, or `list` aborts immediately with `TypeError`. Use `encode_json` for JSON serialization or interpolate atomic fields like `"{user.name}"`.
  - **`val as number`**: Converts numbers or valid numeric strings strictly.
  - **`<text> as <def_union>`**: Validates that the value matches one of the union's allowed literals (`"guest" | "admin"`).
  - **`<map_or_record> as <def_record>`**: Validates **already-structured** data (`map` from `decode_json`, or a matching record) against the `<def_record>` blueprint. Passing unstructured `text` directly to `as <def_record>` fails immediately (`TypeError`).
  - **Mandatory fields at the boundary (defaults belong exclusively to `with`)**: Every field declared in `def` **must be explicitly present** with matching type and valid union value — `as <def_record>` never fills missing fields with defaults (`Missing field '<type>.<field>'`). Extra unknown keys are tolerated for forward compatibility. Internal default construction remains exclusively `<def> with ...`.

```ease
def api_user
    id   = 0
    name = "Guest"
    role = "guest" | "admin"
end

age = "42" as number

# 1. Explicit JSON parsing (single-quoted raw text avoids backslash escapes)
# 2. Strict contract validation against `def api_user` (all fields required)
raw_map = decode_json '{"id": 42, "name": "Ada", "role": "admin"}'
u = raw_map as api_user

# Defaults are strictly for internal `with` construction:
local_guest = api_user with id = 1

test "strict as contract vs decode_json vs with defaults"
    u.name is "Ada"
    local_guest.name is "Guest"

    '{"id": 42, "name": "Ada", "role": "admin"}' as api_user is error
    (decode_json '{"id": 42, "role": "admin"}') as api_user ->
        error.type is "TypeError"
        error.message is "Missing field 'api_user.name'"
    end
    (decode_json '{"id": 42, "name": "Ada", "role": "hacker"}') as api_user is error
    u as text is error
end
```

---

### Lists & Loops (`list`, `loop`)

Lists are homogeneous and use **0-based dot indexing** — either with a literal index (`cloud.0.vx`) or a computed index in parentheses (`scores.(i)`):

```ease
scores = list 10, 25, 40
first = scores.0
pos = 1
second = scores.(pos)

# Empty typed list
empty_nums = list as number

# Multiline record list builder
cloud = list as particle with
    x = 0, vx = 5, life = 20
    x = 10, vx = 2, life = 5
end
vx0 = cloud.0.vx
```

Ease has one unified loop construct (`loop <iterable> ... end`), which binds `it` to the current element and `it.index` to the 0-based iteration index (`0, 1, 2, ...`):

```ease
# Inclusive range shorthand (1 through 5 inclusive)
loop range 1 5
    say "{it.index}: {it}"
end

# Iterate over a record list (access fields via `it.<field>`)
loop cloud
    say "#{it.index}: x={it.x}, vx={it.vx}"
end

# Early loop exit with `check <cond> -> end`
loop range 1 100
    check it > 3 -> end
    say it
end

# Computed indexing `seq.(it.index - 1)` inside a loop (Fibonacci example)
seq = list 0, 1
loop range 1 10
    check
        it.index < 2 -> say "seed: {seq.(it.index)}"
        else
            next = seq.(it.index - 1) + seq.(it.index - 2)
            check next > 25 -> end
            seq = append seq next
    end
end

# Single-line loop
loop range 1 5 say it end

# Vectorized arithmetic, broadcasting, append, and aggregations
prices = list 10, 20, 30
shipping = list 5, 5, 10
totals = prices * 1.19 + shipping       # element-wise multiply + add -> [16.9, 28.8, 45.7]
combined = append (list 1, 2) (list 3, 4)
total_sum = sum totals
mean_price = avg prices
ordered = sort (list 30, 10, 20)        # returns a sorted copy [10, 20, 30]

# Declarative filtering with `where` (uses `it` and `it.index`)
paid = orders where it.status is "paid" and it.amount > 50

vip = orders where
    it.status is "paid"
    it.amount >= 100
end

# Union narrowing works inside `where` as well
server_errors = items where it is ErrorMessage and it.code >= 500

# Batch `with` on record lists (uses `it` and `it.index`, never implicit field injection)
moved = cloud with x = it.x + it.vx

# UTF-8 text processing (all text inputs are validated as UTF-8 and NFC-normalized)
clean = trim "  ada@example.com  "
parts = split clean "@"                  # -> ["ada", "example.com"]
rebuilt = join parts " at "              # -> "ada at example.com"
updated = replace clean ".com" ".org"    # -> "ada@example.org"
org_users = users where contains it.email "@example.com"
```

> **Note:** `def` records cannot declare a field named `index` (reserved for `it.index` inside `loop`), and `loop` requires a list or `range` (e.g. `loop range 1 3`, not `loop 3`).

---

### Built-in Tests (`test`)

Every `to ... end` function closes with its own `end`. Assertions live in standalone `test ["label"] ... end` blocks executed by `ease test <file.ease> [--verbose]`. All assertions run as **soft asserts** and support variable setup, data-driven `loop` blocks, direct boolean conditions, `is` / `is not`, `is error` / `is not error`, and `expr -> ... end` error inspection blocks (`error.type`, `error.message`):

```ease
def div_case
    a = 0
    b = 0
end

to safe_div a b
    check b is 0 -> fail "Division by zero"
    a / b
end

test "data-driven calculator & error checks"
    a = 10
    b = 3
    a - b >= 0
    a + b <= 20

    safe_div 10 2 is 5
    safe_div 10 2 is not error
    safe_div 1 0 is error

    cases = list as div_case with
        a = 1, b = 0
    end

    loop cases
        safe_div it.a it.b ->
            error.type is "RuntimeError"
            error.message is "Division by zero"
        end
    end
end
```

By default, `ease test` prints **only failures** with their full enclosing block context (`test`, `loop`, `expr ->`) and resolved variable values (use `--verbose` to include `[PASS]` lines):

```text
FAIL  test "Error handling" (tests/math.ease:26)
  18 | test "Error handling"
   . |     ...
  23 |     loop cases
  24 |         safe_div it.a it.b ->
   . |             ...
  26 |             error.message is "Zero division"
     |             -> error.message = "Division by zero", it = div_case(a: 1, b: 0)
Summary: 2 passed, 1 failed (3 assertions in 1 test blocks)
```

---

## Why Ease Is Efficient for AI Agents

Ease is explicitly designed to minimize token ambiguity, context overhead, and repair loops when AI models generate, inspect, and verify code:

1. **One-page learnable grammar ([`llms.txt`](llms.txt))**: A model with zero prior Ease training data can load the entire formal grammar and standard library in a single small prompt without conflicting dialects.
2. **One canonical syntax per construct**: Function declarations (`to add a b`) and arity-based calls (`add 2 3`, `sum_squares range 1 5`) never use brackets or commas; equality and type assertions both use `is` / `is not` (no `==`/`!=`/`is-not` aliases); blocks always close with `end`. Commas appear only in `list` literals and single-line `with` updates.
3. **Self-documenting states instead of `bool`, enforced by `def`/`to`**: Removing `bool` eliminates boolean blindness — conditions (`amount >= 50`) live inside `check` and `test`, while returned states use explicit `def` union types (`"yes" | "no"`, `"open" | "locked"`). At the same time, the `def` (noun) / `to` (infinitive verb) split exposes sloppy names like `to can_ship` immediately when read aloud.
4. **Zero implicit magic + repairable diagnostics**: Parsing (`decode_json`) is strictly separated from schema validation (`as <def>`, where every declared field is mandatory without silent default-filling; defaults stay exclusively in `with`). `+` never coerces mixed types (`number + number`, `text + text`, and numeric-list arithmetic only; `"41" + 1` or `"x: " + rec` raises `TypeError` — use `"{a}{b}"` or `as text`). Unknown types (`as ö`, `as bool`) and mismatched fields abort immediately with exact `Line`/`Col`, source caret, `"Did you mean ...?"` suggestions, and actionable hints.
5. **Built-in self-verification & machine interfaces**: Agents can verify their own output in one command with `ease test` (including `is error` / `-> error.type` assertions and block-context diagnostics) and inspect structure deterministically via `ease ast`, `ease tokens`, and `ease lsp`.

---

## Standard Modules (`import`)

Standard modules are embedded directly into the compiled binary (`internal/ease/std/*.ease`) and behave identically to file modules: they bind an encapsulated module namespace (defaulting to the module name, or a custom alias via `import network as api`). Call module verbs with `:` and access module blueprints with `.`. Optional local overrides under `./std/` can be enabled explicitly via `EASE_STD=1` (or `EASE_STD=<dir>`):

### `import network`

Wraps HTTP networking via the `http_call` host bridge:

- **Blueprints**: `network.network` (`url`, `timeout = 5`, `token = ""`), `network.http_request` (`method`, `url`, `body`, `token`, `timeout`), `network.http_response` (`status`, `body`, `error`).
- **Verbs**:
  - `network:fetch target path` — performs HTTP `GET` (`Content-Type: application/json`, optional Bearer token) and returns the response body or `""` on transport errors or HTTP status `>= 400`.
  - `network:send target path payload` — serializes `payload` via `encode_json` and performs HTTP `POST` to `{target.url}{path}`.

See [`examples/05_network.ease`](examples/05_network.ease) (offline contract tests) and [`examples/051_network.ease`](examples/051_network.ease) (live REST example).

### `import screen`

Wraps 2D rendering and window input (via Ebitengine and `screen_present`):

- **Blueprints**: `screen.screen` (`width = 800`, `height = 600`, `title = "Ease Window"`), `screen.circle` (`x`, `y`, `size`, `color`), `screen.box` (`x`, `y`, `width`, `height`, `color`), `screen.scene` (`background`, `circles`, `boxes`), `screen.frame_state` (`status = "open" | "closed"`, `key`, `mouse_x`, `mouse_y`).
- **Verb**: `screen:present target current_scene` — renders one frame (60 FPS) and returns the current `screen.frame_state` serialized as JSON text (typically decoded via `decode_json raw as screen.frame_state`).
- **Headless test mode (`--check`)**: Simulates deterministic frames without opening a graphical window so UI loops can run cleanly in CI/CD environments.

See [`examples/06_window.ease`](examples/06_window.ease).

---

## Built-in Verbs

| Built-in / Construct | Behavior |
| :--- | :--- |
| `say expr` | Prints formatted value to `stdout` |
| `ask prompt` | Reads a line of input from `stdin` (fixed arity 1; use `ask ""` for an empty prompt) |
| `count xs` | Returns length of `text` (runes) or `list` |
| `str val` | Converts a value to `text` |
| `num val` | Converts `number` or `text` to `number` (raises `RuntimeError` on invalid input) |
| `type val` | Returns the type name or `def` name as `text` |
| `range start end` | Returns an inclusive list of numbers `[start .. end]` |
| `append xs item_or_list` | Returns a new list with an item or another compatible list appended (enforces element type) |
| `sum xs` | Sums all elements of a numeric `list` (`0` for empty numeric list) |
| `avg xs` | Computes the arithmetic mean of a non-empty numeric `list` |
| `sort xs` | Returns a new ascending sorted copy of a `number` or `text` `list` |
| `split txt sep` | Splits UTF-8 `text` by `sep` into a `list` of `text` (`split "äöü" ""` splits into runes) |
| `join items sep` | Joins a `list` of `text` with `sep` into `text` |
| `trim txt` | Strips leading and trailing Unicode whitespace |
| `upper txt` / `lower txt` | Converts UTF-8 `text` to uppercase or lowercase |
| `slice xs start end` | Returns 0-based `[start:end)` slice (clamped) of a `list` or UTF-8 `text` (by runes) |
| `replace txt old new` | Replaces all occurrences of non-empty `old` with `new` in `text` |
| `contains txt sub` | Condition verb (usable in `check`, `where`, `test`; not storable) checking if `txt` contains `sub` |
| `abs n` / `floor n` / `round n` / `min a b` / `max a b` | Numeric math helpers |
| `fields rec` | Returns a list of field names in declaration order for a `def` record or decoded `map` |
| `decode_json txt` | Parses valid JSON `text` into structured Ease data (`map`, `list`, or primitives); aborts on empty/invalid JSON |
| `encode_json val` | Encodes a `def` record, `map`, `list`, or primitive into JSON `text` |
| `load path [as text\|csv\|bytes]` | Reads a file (50 MB max) as UTF-8/NFC `text`, structured `csv` rows (`map` list), or raw byte list (`0..255`) |
| `save path data` | Writes UTF-8 `text` or a `list` of byte numbers (`0..255`) to `path`, returning written byte count |
| `fail msg` | Raises an explicit `RuntimeError` (testable with `is error` / `-> ... end`) |

---

### File I/O (`load` & `save`)

`load` and `save` are directly available built-in verbs (no `import` required):

- `load "./notes.txt"` (or `load "./notes.txt" as text`) validates UTF-8, strips an optional UTF-8 BOM, and normalizes to Unicode NFC.
- `load "./image.png" as bytes` reads raw bytes (`0..255`) as a `list` of `number` without UTF-8 checks or normalization.
- `load "./data.csv" as csv` parses uniform CSV rows into a list of maps (`FromCSV`), supporting both positional column access (`it.0`, `it.1`, `it.(i)`) and dynamic header lookup (`it.("Product Name")`, `it.("单价 (EUR)")`).
- `save path data` writes `text` or byte lists (`0..255`) and returns the number of bytes written.

```ease
# CSV with mixed/non-ASCII headers:
# Product Name,单价 (EUR),Order Status
# Mechanical Keyboard,89.50,paid
# Wireless Mouse,25.00,refunded

def sale_status = "paid" | "refunded"

def sales_entry
    product = ""
    price   = 0
    status  = "paid" | "refunded"
end

# Step 1: Parse CSV structure (UTF-8 + NFC + uniform columns), no domain assumptions yet
raw_rows = load "./datafiles/export_asia.csv" as csv

# Step 2: Translate explicitly at the boundary by column index (`it.0`, `it.1`, `it.2`)
# or by header string (`it.("Product Name")`, `it.("单价 (EUR)")`)
entries = list as sales_entry
loop raw_rows
    entry = sales_entry with
        product = it.0
        price   = it.1 as number
        status  = it.2 as sale_status
    end
    entries = append entries entry
end

paid = entries where it.status is "paid"
say "Paid count: {count paid}"
```

### File Modules (`import "./file.ease" as mod`)

Split larger Ease programs into reusable `.ease` files without polluting the global namespace:

```ease
# billing.ease
def default_tax = 0.19

def invoice
    id    = ""
    net   = 0
    state = "open"
end

def outcome = invoice | "empty"

to total inv
    inv.net * (1 + default_tax)
end
```

```ease
# main.ease
import "./billing.ease" as billing

inv = billing.invoice with id = "INV-1", net = 100
say billing:total inv          # 119
say billing.default_tax        # 0.19

res = inv as billing.outcome
check
    res is billing.invoice -> say res.id
    res is "empty"         -> say "nothing"
end
```

- **Paths**: File imports must be relative to the importing file (`./...` or `../...`) and end in `.ease`; cyclic imports are rejected with `ImportError`.
- **Encapsulation**: Functions are called with `:` (`mod:verb arg1 arg2`), while `def` blueprints, unions, and constants are accessed with `.` (`mod.invoice`, `mod.default_tax`). Two modules can define identically named functions or `def` types without colliding.
- **Isolation**: Imported modules run in their own isolated environment; module-local `test` blocks are not executed on import.

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
  to  import  def  with  where  end  test  check  is
  say  as  else  loop
  and  or  not  text  number  list

Operators & Punctuation:
  ->  (  )  ,  .  :  |  =  <  <=  >  >=  +  -  *  /  %
```

### Program & Top-Level Declarations

```ebnf
program        ::= [NL] { top_item [NL] } EOF

top_item       ::= import_decl
                 | func_decl
                 | def_decl
                 | top_test_blk
                 | stmt

import_decl    ::= "import" ( IDENT [ "as" IDENT ] | STRING "as" IDENT ) stmt_end

func_decl      ::= "to" IDENT { IDENT }
                   ( NL | &"end" )
                   { stmt [NL] }
                   "end"

def_decl       ::= "def" IDENT "=" union_member "|" union_member { "|" union_member } stmt_end  (* union decl *)
                 | "def" IDENT "=" expr stmt_end                                             (* constant decl *)
                 | "def" IDENT stmt_end [NL] { def_field [NL] } "end"                        (* record blueprint *)

union_member   ::= "text" | "number" | expr
def_field      ::= IDENT "=" union_member { "|" union_member } stmt_end

top_test_blk   ::= "test" [STRING] [NL] { test_item [NL] } "end"
```

### Statements & Assertions

```ebnf
stmt           ::= "say" expr stmt_end
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
                 | expr stmt_end                                  (* direct condition assertion *)

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
                           | "." "(" expr ")"            (* computed list/string index *)
                           | with_clause
                           | where_clause
                           | "as" cast_type
                           | call_args }                 (* when base IDENT is a known function of arity k *)

with_clause    ::= "with" NL { with_field stmt_end [NL] } "end"
                 | "with" with_field { "," with_field }
with_field     ::= IDENT "=" expr

where_clause   ::= "where" NL { expr stmt_end [NL] } "end"
                 | "where" expr

cast_type      ::= "text" | "number" | "csv" | "bytes" | IDENT  (* "csv" and "bytes" are valid only directly on `load <path>` *)

call_args      ::= unary{k}                              (* exactly k space-separated arguments for arity k; 'as' binds outside the full call *)

primary        ::= NUMBER
                 | STRING
                 | RAW_STRING
                 | INTERP_STRING
                 | IDENT
                 | "(" expr ")"
                 | list_expr

list_expr      ::= "list" "as" list_elem_type [ "with" NL { with_field { "," with_field } stmt_end [NL] } "end" ]
                 | "list" expr { "," expr }

list_elem_type ::= "text" | "number" | IDENT    (* must name a built-in type or previously defined/imported `def` blueprint *)
```

> **Note on `as` binding**: Inside `call_args`, nested `as` postfix casts are suppressed so `decode_json raw_text as api_user` binds `(decode_json raw_text) as api_user` rather than casting only the final argument.
>
> **Note on `contains`:** `contains txt sub` is a condition-producing built-in (like `is` or `>`): it can be used directly in `check`, `where`, `test`, and with `and`/`or`/`not`, and cannot be stored in variables or printed.

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
