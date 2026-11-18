# README Syntax Audit & Test Matrix

This overview tracks each syntax rule in [`README.md`](../README.md) with **1 happy path + 10 negative cases** per category.

- **Runtime negative checks (`is error`)**: Directly executable in [`examples/syntax_spec.ease`](syntax_spec.ease) via `ease test examples/syntax_spec.ease`.
- **Lexer / parser / typecheck negative checks**: Syntactically or statically invalid programs caught by `NewLexer(...).Tokenize()` or `ParseProgram(...)` (covered in `internal/ease/ease_test.go`).

---

## 1. Lexical Structure (`README.md`)
- **Happy Path**: Unicode identifier `größe_1 = 42`, comments `# ...`, escapes `\n \t \" \\ \{`, interpolation `"Hi {name}: {1 + 2}"`, raw string `'{"id": 7}'` (1:1 without interpolation or escapes), strict `+` (`"a" + "b"`, `1 + 2`, `(list 1, 2) + 3`)
- **Negative Cases**:
  1. Unterminated string: `"hello`
  2. Multi-line string literal: `"hello\nworld"`
  3. Unterminated raw string `'...'`: `'hello`
  4. Non-atomic interpolation: `"{hero}"` (runtime `TypeError`)
  5. Empty interpolation: `"{}"`
  6. Unterminated interpolation: `"{x + 1"`
  7. Invalid expression inside interpolation: `"{1 + }"`
  8. Illegal character outside string: `@x = 1`
  9. Leading dot number: `.5`
  10. Trailing dot number: `5.`
  11. Mixed `+` coercion rejected: `"41" + 1`, `1 + "41"`, `"user: " + u`, `"items: " + (list 1, 2)` (`TypeError`)

## 2. Variables & Constants (`README.md`)
- **Happy Path**: `x = 10`, `x = x + 5`, `def pi = 3.14`
- **10 Negative Cases**:
  1. Legacy `let x = 1`
  2. Legacy `var x = 1`
  3. Legacy `set x = 1`
  4. Reassigning a constant: `def pi = 3.14` + `pi = 4`
  5. Keyword as variable name: `loop = 5`
  6. Type keyword as variable name: `text = "hi"`
  7. Type keyword as variable name: `number = 5`
  8. `def` constant missing value: `def pi =`
  9. Assignment missing RHS: `x =`
  10. Assignment to literal: `1 = 2`

## 3. Functions (`to ... end`) & Arity-Based Calls (`README.md`)
- **Happy Path**: `to add a b ... end`, `add 2 3`, `to mock_fetch client path` (no commas in parameter lists or calls)
- **10 Negative Cases**:
  1. Missing `end` at EOF: `to f x\n x + 1`
  2. Missing `end` before `test`: `to f x\n x\ntest\n 1 is 1\nend`
  3. Legacy bracketed parameters: `to f [a b]\n a\nend`
  4. Legacy parenthesized call: `add(1, 2)`
  5. Commas in parameters/calls: `to f a, b` / `f 1, 2`
  6. Nested `to` inside `to`
  7. Keyword as parameter name: `to f loop\n 1\nend`
  8. Too few arguments (`add 1` -> `is error` in `.ease`)
  9. Too many arguments (`add 1 2 3` -> `is error` in `.ease`)
  10. Unknown function (`unbekannt 1` -> `is error` in `.ease`)

## 4. Records (`def`), `with` Copies & Unions (`README.md`)
- **Happy Path**: `def ticket = "vip" | "standard"`, `def outcome = status | ErrorMessage`, `def player ... end`, `player with name = "Aria"`, `hero.name`
- **10 Negative Cases**:
  1. `def` record missing `end`
  2. Logic (`to ... end`) inside `def`
  3. Statement (`say 1`) inside `def`
  4. Union with trailing pipe: `def u = "a" |`
  5. Multiline `with` missing `end`
  6. `with` missing `=`: `hero with hp 10`
  7. `with` on non-record (`42 with x = 1` -> `is error` in `.ease`)
  8. `with` with unknown field (`hero with mana = 50` -> `is error` in `.ease`)
  9. `with` with invalid union value (`hero with state = "flying"` -> `is error` in `.ease`)
  10. Missing field access (`hero.mana` -> `is error` in `.ease`)

## 5. Guards (`check`) (`README.md`)
- **Happy Path**: `check ... -> ... end`, `check ... else [-> ...] end`, `check cond -> stmt`, `check cond -> end` (inside `loop`)
- **10 Negative Cases**:
  1. `check` block without `->` or `else`: `check\n x > 0\nend`
  2. `check` missing condition before `->`: `check -> 1`
  3. Orphan `else` without `check`
  4. `check` block missing `end`
  5. Single-line `check cond ->` missing action
  6. Legacy `check:` with colon
  7. Top-level value return in `check`: `check x > 0 -> "A"`
  8. `-> end` outside `loop`: `check x > 0 -> end`
  9. Legacy `if` / `else` (`if x > 0 ... end`)
  10. Non-condition value in `check` (`check "yes" -> "ok"` -> `is error` in `.ease`)

## 6. Type Checks (`is`, `is not`) & Logic (`README.md`)
- **Happy Path**: `is`, `is not`, `<`, `<=`, `>`, `>=`, `and`, `or`, `not`, `is text`, `is number`, `is list`
- **10 Negative Cases**:
  1. `x is bool` (`bool` does not exist)
  2. `x is not bool`
  3. `x == 1` / `x != 1` / `x is-not 1` (removed legacy operators)
  4. `x is` missing RHS
  5. `and (1 > 0)` missing LHS
  6. `not 42` (`is error` in `.ease`)
  7. `1 and (1 > 0)` (`is error` in `.ease`)
  8. `(1 > 0) or 1` (`is error` in `.ease`)
  9. `"a" < 1` (`is error` in `.ease`)
  10. `(1 > 0) < (1 < 0)` (`is error` in `.ease`)

## 7. Strict `as` Contract & Explicit JSON (`decode_json` / `encode_json`) (`README.md`)
- **Happy Path**: `"42" as number`, `42 as text`, `"vip" as ticket`, `(decode_json '{"id": 7, "name": "Guest", "role": "admin"}') as api_user`, `encode_json u`
- **10 Negative Cases**:
  1. Unknown Unicode type: `x as ö` (Parse/TypeError)
  2. Typo suggestion: `x as api_usre` (Parse/TypeError with suggestion)
  3. Alias `as string` (Parse/TypeError hinting `text`)
  4. `as bool` / `as boolean` (Parse/TypeError: boolean is not a built-in type)
  5. Constant / forward reference after `as`: `x as pi` / `x as late_user`
  6. Raw text passed directly to `as <def>`: `'{"id": 7, ...}' as api_user` (`is error` in `.ease`)
  7. Missing mandatory field on `as <def>` (no silent defaults): `Missing field 'api_user.name'` (`is error` in `.ease`)
  8. Record/list passed to `as text` or `{...}` interpolation (`is error` in `.ease`)
  9. Invalid number / union / JSON in `decode_json` (`"abc" as number`, `"pending" as ticket`, `decode_json '{broken'` -> `is error` in `.ease`)
  10. Wrong JSON shape / field type (`decode_json '[1, 2]' as api_user`, `decode_json '{"id": "abc", ...}' as api_user` -> `is error` in `.ease`)

## 8. Lists, Dot Indexing, `where` Filters, Batch `with` & Loops (`README.md`)
- **Happy Path**: `list 10, 25, 40`, `scores.0`, `scores.(it.index)`, `list as number`, `list as player with ... end`, `orders where it.status is "paid" and it.amount > 50`, `orders where ... end`, `users where contains it.email "@example.com"`, `split`/`join`/`trim`/`replace`, `load path [as text|csv|bytes]`, `save path data`, `row.0`, `row.("Header")`, `cloud with x = it.x + it.vx`, `loop ... end` (`it.index`, `check ... -> end`), `loop range 1 10`, `loop xs ... end` (`it`, `it.index`, `check ... -> end`)
- **10 Negative Cases**:
  1. Legacy list literal `[1, 2, 3]`
  2. Legacy indexing `scores[0]`
  3. `list as bool`
  4. `list as unknown_type`
  5. `list as number with x = 1 end`
  6. `list as ticket with x = 1 end`
  7. `loop` missing `end`
  8. Legacy `for` / `repeat` (`for xs ... end`, `repeat 3 ... end`)
  9. Named loop variable (`loop x in xs ... end`) or `def` field named `index`
  10. Runtime errors (`xs.99`, `"hi".0`, `list 10, "hi"`, `append typed "bad"`, `loop 42`, `loop "3"`, `loop\n  say it.index\nend` without exit `TypeError`, `xs.(1.5)`, `x as csv`, `save p (list 256)`, malformed/duplicate CSV -> `is error` in `.ease`)

## 9. Built-in Tests (`test`) & Imports (`import`) (`README.md`)
- **Happy Path**: `test ... end`, `test "label" ... end`, `is error`, `-> error.type / error.message end`, `import network` (`network:fetch`, `network:send`, `network.network`), `import screen` (`screen:present`, `screen.screen`, `screen.frame_state`), `import "./file.ease" as mod` (`mod:verb`, `mod.def`, `mod.const`, `as mod.def`, `is mod.def`)
- **Negative Cases**:
  1. `test` missing `end`
  2. Nested `test` inside `to`
  3. Nested `test` inside `loop`
  4. Legacy `test:` with colon
  5. `check "label"` missing `end`
  6. Error assertions require `is error` (`1 / 0 is error`, not unknown trailing expressions)
  7. `import nonexistent_mod` / missing `.ease` file (`ImportError`)
  8. `import "network"` (string without `as alias`) or non-relative / cyclic `.ease` import
  9. `import network` inside `to`
  10. `import` missing module name (`import\n`), `mod.fn` dot call (`TypeError` hinting `mod:fn`), `mod = 1`, `mod with ...`
