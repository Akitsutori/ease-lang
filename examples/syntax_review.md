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
- **Happy Path**: `check ... -> <value|end> ... end`, `check <cond>\n  stmts...\n[else\n  stmts...]\nend`, `check cond -> <value|end>`
- **Negative Cases**:
  1. Verdict `check` block rule without `->`: `check\n x > 0\nend`
  2. `check` missing condition before `->`: `check -> 1`
  3. Orphan `else` without `check`
  4. `check` block missing `end`
  5. Single-line `check cond ->` missing value/`end` or statement after `->` (`check x > 0 -> say x`)
  6. Legacy `check:` with colon or bare precondition checklist without `->`
  7. Top-level value return in `check`: `check x > 0 -> "A"`
  8. `-> end` outside `loop`/`take`: `check x > 0 -> end`
  9. Legacy `if` / `else` (`if x > 0 ... end`) or `fail` (`fail "..."`)
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
- **Happy Path**: `list 10, 25, 40`, `scores.1`, `scores.(it.index)`, `scores with .1 = 99`, `list as number`, `list as player with ... end`, `orders where it.status is "paid" and it.amount > 50`, `orders where ... end`, `users where contains it.email "@example.com"`, `split`/`join`/`trim`/`replace`, `load path [as text|csv|bytes]`, `save path data`, `row.1`, `row.("Header")`, `cloud with x = it.x + it.vx`, `loop ... end` (`it.index` 1-based, `check ... -> end`), `loop range 1 10`, `loop xs ... end` (`it`, `it.index`, `check ... -> end`)
- **10 Negative Cases**:
  1. Legacy list literal `[1, 2, 3]`
  2. Legacy indexing `scores[0]` or 0-based `.0` (`scores.0`, `scores with .0 = 1` -> `SyntaxError`)
  3. `list as bool`
  4. `list as unknown_type`
  5. `list as number with x = 1 end`
  6. `list as ticket with x = 1 end`
  7. `loop` missing `end`
  8. Legacy `for` / `repeat` (`for xs ... end`, `repeat 3 ... end`)
  9. Named loop variable (`loop x in xs ... end`) or `def` field named `index`
  10. Runtime errors (`xs.99`, `"hi".99`, `xs.(0)`, `list 10, "hi"`, `append typed "bad"`, `loop 42`, `loop "3"`, `loop\n  say it.index\nend` without exit `TypeError`, `xs.(1.5)`, `x as csv`, `save p (list 256)`, malformed/duplicate CSV -> `is error` in `.ease`)

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

## 10. Pattern Matching & Structured Extraction (`match ... end`)
- **Happy Path**: `match cmd\n "PING" -> "PONG"\nend`, `match cmd\n "PING"\n say "pong"\nend`, `res = match s\n "PORT={p as number}" -> p\n else -> 0\nend`, `entries = match lines\n "[{lvl as LogLevel}] {msg} ({...})" -> LogRecord with level = lvl, msg = msg\nend`, fixed rune widths `"{y: 4}-{m: 2}-{d: 2}"`, raw literal `'{"ok": true}'`, backreferences `"<{tag}>{body}</{tag}>"` / `"{n as number}:{n}"`, loop exit `match cmd\n "quit" -> end\nend`
- **Negative Cases**:
  1. Adjacent variable-length placeholders (`"{a}{b}"`, `"{...}{b}"`, `"{a}{...}"`, `"{...}{...}"`)
  2. Invalid fixed width (`"{a: 0}{b}"`, `"{a: 1.5}{b}"`) or reserved `{it}` capture
  3. Empty `match` block without pattern arms or missing `end`
  4. `match` expression on `text` without `else`
  5. Top-level value return in `match` (`"A" -> 1`) or unassigned top-level list `match`
  6. Statement/side-effect after `->` (`"PING" -> say "ok"`, `"KEY={v}" -> v = "new"`)
  7. Non-text / `list as number` target in `match`
  8. Direct `def` record or `bool`/`csv`/`bytes`/`string` target in `{x as Type}`
  9. Conflicting repeated capture types (`"{n as number}-{n as text}"`, `"{n}-{n as number}"`)
  10. Mixed incompatible list branch return types (`number` vs `text`, `RecA` vs `RecB`) or mutating immutable capture variable in effect arm (`"KEY={v}"\n v = "new"`)

## 11. Safe Cast Fallbacks (`<expr> as <type> or <fallback>`)
- **Happy Path**: `raw as number or 0`, `ask "Port?" as number or -1`, `"404" as HttpCode or 200`, `raw as Level or "INFO"`, `(list "10", "bad", "20") as number or 0`, `(lines as number or 0) where it > 0`, `raw as number or 8080 + 1`
- **10 Negative Cases**:
  1. Wrong fallback literal type: `raw as number or "abc"` (`TypeError`)
  2. List passed as fallback: `lines as number or (list 0)` (`TypeError`)
  3. Union fallback typo / invalid literal: `raw as Level or "TYPO"` (`TypeError` with hint)
  4. Plain unannotated `text` var for closed union: `fb = "INFO"; raw as Level or fb` (`TypeError`)
  5. Condition expression as fallback: `raw as number or (1 > 0)` (`TypeError`)
  6. `def` record contract with `or`: `raw_map as api_user or default_user` (`TypeError`)
  7. Record-only union with `or`: `raw_map as RecUnion or rec` (`TypeError`)
  8. `load` with `or`: `load "./data.csv" as csv or 0`, `load "./a.txt" as text or ""` (`TypeError`)
  9. Forbidden target types: `raw as bool or 0`, `raw as string or ""`, `raw as unknown or 0` (`TypeError`)
  10. Runtime non-suppression: bare `"abc" as number`, bare `(list "a", "b") as number`, `(1 / 0) as number or 0` -> `is error` in `.ease`

## 12. Data-Parallel Workers (`swarm loop`)
- **Happy Path**: `xs = swarm loop range 1 5\n -> it * 2\nend`, `to f xs\n -> swarm loop xs\n check it < 0 -> 0\n -> it\n end\nend`, `(swarm loop range 1 5\n -> it * it\nend) where it > 10`, nested `swarm loop`s, statement `swarm loop xs\n say it\nend`
- **10 Negative Cases**:
  1. Unbounded `swarm loop\n 1\nend` (`SyntaxError`)
  2. Named variable `swarm loop x in xs ... end` (`SyntaxError`)
  3. Bare function argument `sum swarm loop range 1 5\n it\nend` (`SyntaxError`)
  4. `-> end` directly inside `swarm loop` (`TypeError`)
  5. Mutating outer variable `total = total + it` inside `swarm loop` (`TypeError` / `RuntimeError`)
  6. `ask "..."` inside `swarm loop` (`TypeError` / `RuntimeError`)
  7. Non-list/non-range iterable `swarm loop 42 ... end` or `swarm loop "abc" ... end` (`TypeError`)
  8. Condition returned from worker `swarm loop xs\n it > 1\nend` (`TypeError`)
  9. Mixed result types across worker branches (`number` vs `text`, `TypeError`)
  10. Downstream field typo `res where it.missing is 1` or `sum text_res` (`TypeError`)

## 13. Background Workers & Typed Inboxes (`spawn`, `inbox`, `put`, `take`, `close`)
- **Happy Path**: `ch = inbox as ClientEvent`, `spawn produce ch`, `spawn mod:emit ch 10`, `put ch val`, `take ch\n say it.kind\n check it.val > 10 -> end\nend`, `close ch` (idempotent)
- **10 Negative Cases**:
  1. `spawn 1 + 2` / `spawn say "hi"` (non-call `SyntaxError`)
  2. `ch = inbox number` (missing `as`, `SyntaxError`)
  3. `ch = inbox as bool` / `inbox as csv` / `inbox as unknown` (`TypeError`)
  4. `put ch "bad"` on `inbox as number` (`TypeError`)
  5. `put ch "TYPO"` on union inbox (`TypeError`)
  6. `put xs 1` on non-inbox (`TypeError`)
  7. `close x` / `take xs ... end` on non-inbox (`TypeError`)
  8. `take ch\n say it.missing\nend` (`TypeError` via inferred `ElemType`)
  9. `take ch` missing `end` (`SyntaxError`)
  10. `put closed_ch "late"` -> `RuntimeError` (`Cannot 'put' into a closed inbox`)

## 14. `inbox`-Driven TCP & HTTP Servers (`std/network`, `tests/13_net_sockets.ease`, `examples/07_http_server.ease`)
- **Happy Path**: `requests = inbox as network.http_request`, `port = network:serve_http 0 requests`, `network:reply_text req 200 "ok"`, `network:reply_json req 200 rec`, `clients = inbox as network.tcp_client`, `network:serve_tcp 0 clients`, `network:tcp_send client "msg\n"`, `close requests` / `close clients` / `close client.lines`
- **Negative / Safety Cases**:
  1. Port out of range (`-1`, `65536`) or non-integer port (`RuntimeError`)
  2. Non-inbox passed to `serve_http` / `serve_tcp` (`TypeError`)
  3. Invalid HTTP status code (`< 100` or `> 599`) in `reply_text` / `reply_json` (`RuntimeError`)
  4. Sending to a closed `tcp_client` via `tcp_send` (`RuntimeError`)
  5. `InboxValue.AddOnClose` runs callbacks once on `close` (and immediately if already closed)
  6. Headless `--check` mode returns `0` from `serve_http` / `serve_tcp` without blocking
  7. Runtime shutdown (`ev.Close()`) closes open listeners/connections and unblocks servers cleanly
