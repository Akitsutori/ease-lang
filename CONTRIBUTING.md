# Contributing to Ease

Thank you for your interest in **Ease**! This guide explains the repository architecture, where you can make meaningful contributions, local setup requirements, and the language design invariants that every change must preserve.

---

## 1. Architecture & Where to Contribute

The entire Ease toolchain lives in a single, self-contained Go module:

```text
.
├── cmd/ease/main.go             # CLI entrypoint (run, check, test, build, ast, tokens, repl, lsp, version)
├── internal/ease/
│   ├── token.go                 # Token types and reserved keywords
│   ├── lexer.go                 # Scanner (numbers, strings with {interpolation}, raw '...', # comments, operators)
│   ├── ast.go                   # AST node definitions for declarations, statements, and expressions
│   ├── parser.go                # Two-pass arity-based recursive-descent parser and stdlib/file import resolution
│   ├── typecheck.go             # Static type/arity checks, safe `or` fallbacks, return consistency & flow checking
│   ├── typecheck_unions.go      # Union type resolution, 3 compile-time guardrails, narrowing & exhaustiveness
│   ├── checker.go               # Data-driven soft-assert test runner (`test` blocks, `is error`, context reports)
│   ├── eval.go                  # Tree-walking evaluator, thread-safe hot-swappable `Env`, and statement execution
│   ├── values.go                # Runtime value representations, formatting, freezing & structural equality
│   ├── match.go                 # Full-string pattern matching and typed extraction engine (`match`)
│   ├── swarm.go                 # Bounded data-parallel `swarm loop` worker pool and deterministic ordering
│   ├── inbox.go                 # Typed concurrent FIFO `inbox`, `spawn` workers, service isolation & lifecycle cleanup
│   ├── builtins.go              # Fixed-arity built-in verbs (`ask`, `count`, `range`, JSON, file I/O, HTTP/screen/random dispatch)
│   ├── net_builtins.go          # `inbox`-driven HTTP & TCP server host bridges (`serve_http`, `reply_*`, `serve_tcp`, `tcp_send`)
│   ├── random_builtins.go       # Goroutine-safe randomness bridges via `math/rand/v2` (`roll`, `pick`, `shuffle`)
│   ├── service.go               # `ease serve` supervisor, fail-fast checks, `--watch` hot-code-swap & graceful shutdown
│   ├── debug_server.go          # Opt-in `127.0.0.1` loopback debug socket (`:status`, `:funcs`, `:reload`, live eval)
│   ├── cast.go                  # Strict `as` casting, `or` fallback coercion, and JSON-to-Ease validation
│   ├── screen.go                # Ebitengine main-thread renderer (60/"unlimited" TPS, shapes, `screen.event` inbox, `--check` simulation)
│   ├── stdlib.go                # Embedded `std/*.ease` loader (`//go:embed`) and `EASE_STD` disk override
│   ├── bundle.go                # Standalone executable packer (`EASE_BUNDLE_V1`)
│   ├── errors.go                # Structured `EaseError` diagnostics with source snippets and hints
│   ├── ease_test.go             # Core Go test suite (lexer, parser, arity calls, evaluator, match, CLI)
│   ├── typecheck_test.go        # Static typecheck, union narrowing, and exhaustiveness Go tests
│   ├── cast_json_test.go        # JSON decode/encode, strict `as`, `or` fallbacks & list coercion Go tests
│   ├── net_test.go              # HTTP/TCP inbox server concurrency and lifecycle Go tests
│   ├── screen_test.go           # Screen config, color/shape parsing, and headless `screen.event` inbox Go tests
│   └── std/
│       ├── network.ease         # `network` stdlib module (HTTP client + `inbox`-driven HTTP & TCP servers)
│       └── screen.ease          # `screen` stdlib module (`fps_mode`, `event`, 2D shapes, `open` & `present`)
├── examples/                    # Runnable examples (00–08, fibonacci, showcase) and reference specs
├── tests/                       # Language feature test files (01–13) executed via `ease test`
└── .github/workflows/ci.yml     # Cross-platform CI pipeline (Ubuntu Linux with -race, and Windows)
```

### High-Value Contribution Areas

| Area | Key Files | Typical Tasks |
| :--- | :--- | :--- |
| **Standard Library (`std/`)** | `internal/ease/std/*.ease`, `internal/ease/stdlib.go`, `eval.go` | Add clean Ease record blueprints (`def`) backed by minimal Go host bridges (e.g., `file`, `time`, `math`) |
| **Parser & Diagnostics** | `internal/ease/parser.go`, `errors.go` | Improve beginner-friendly error messages, source spans, and actionable recovery hints |
| **Evaluator & Built-ins** | `internal/ease/eval.go` | Add well-tested built-in verbs, tighten edge-case handling, and keep runtime limits safe |
| **Graphics & Input (`screen`)** | `internal/ease/screen.go`, `std/screen.ease` | Extend drawing primitives or input events while preserving deterministic `--check` headless behavior |
| **Examples & Feature Tests** | `examples/*.ease`, `tests/*.ease`, `internal/ease/ease_test.go` | Expand regression coverage, edge-case specs, and clear real-world examples |
| **Docs & Developer Tooling** | `README.md`, `CONTRIBUTING.md`, `.github/workflows/` | Keep the BNF grammar and docs aligned with the parser, improve CI, or add editor syntax support |

---

## 2. Prerequisites & Local Setup

- **Go**: Version declared in [`go.mod`](go.mod) (`go 1.27.4`).
- **Linux additional system packages** (required by Ebitengine for `go build` and `go test`):
  ```bash
  sudo apt-get update
  sudo apt-get install -y libc6-dev libgl1-mesa-dev libxcursor-dev libxi-dev \
    libxinerama-dev libxrandr-dev libxxf86vm-dev libasound2-dev pkg-config
  ```
- **Windows**: No extra OS system libraries are needed beyond Go.

### Building, CLI Reference & Running Checks Locally

**Linux / macOS:**

```bash
go vet ./...
go test -race -v ./...
go build -o ease ./cmd/ease

for f in tests/*.ease; do ./ease test "$f"; done
./ease run examples/06_window.ease --check
./ease run examples/07_http_server.ease --check
./ease run examples/08_haunted_hotel.ease --check
./ease test examples/08_haunted_hotel.ease
```

**Windows (PowerShell):**

```powershell
go vet ./...
go test -v ./...
go build -o ease.exe .\cmd\ease

Get-ChildItem tests\*.ease | ForEach-Object { .\ease.exe test $_.FullName }
.\ease.exe run examples\06_window.ease --check
.\ease.exe run examples\07_http_server.ease --check
.\ease.exe run examples\08_haunted_hotel.ease --check
.\ease.exe test examples\08_haunted_hotel.ease
```

**Local stdlib override:** Pass `EASE_STD=internal/ease/std` from the repository root (or `EASE_STD=1` when running inside a directory with `./std/`) to test changes to `std/network.ease` or `std/screen.ease` without rebuilding embedded files.

**CLI Reference:**

```text
ease run <file.ease> [--json] [--check] [--trace]    Run an Ease program (optionally running tests first, tracing, or emitting JSON)
ease serve <file.ease> [--port <n>] [--watch] [--debug] [--debug-port <n>] [--json]
                                                     Run a persistent service with crash-isolated workers, live hot-reload & opt-in loopback debug port
ease check <file.ease> [--json]                      Parse and type-check without executing
ease test <file.ease> [--json] [--verbose] [--trace] Run all 'test' assertions
ease build <file.ease> [-o <out>]                    Package an Ease program into a standalone executable
ease ast <file.ease> [--json]                        Inspect parsed defs, functions, checks, and statements (or full AST JSON)
ease tokens <file.ease>                              Inspect lexer tokens with line/column positions
ease repl                                            Start interactive Ease REPL
ease lsp                                             Start Language Server (stdio JSON-RPC for editors)
ease version                                         Print Ease CLI version
```

Shorthand: `ease <file.ease>` is equivalent to `ease run <file.ease>`.

---

## 3. Language Design Invariants (Read Before Opening a PR)

Ease intentionally optimizes for cognitive simplicity and syntactic uniformity. Please uphold these invariants across all contributions:

1. **Uniform Bracket-Free, Comma-Free Arity-Based Calls**: Call verbs directly by name (`add 2 3`, `sum_squares range 1 5`) and declare parameters with `to <verb> param1 param2` — never introduce `fn(...)` call syntax, commas between arguments/parameters, or `[params]` parameter lists. Parentheses `(...)` are strictly for expression grouping and compound arguments (e.g., `add (1 + 2) 3`, `fib (n - 1)`). Commas are reserved for `list` literals (`list 1, 2, 3`) and single-line `with` updates (both record fields and list `.index` updates).
2. **1-Based Dot Notation Only for Lists and Records**: Index lists 1-based (`1..N`) and access fields consistently with `.` (`cloud.1.vx`, `player.hp`, `xs with .1 = 99`). Build lists with `list 1, 2, 3` or `list as <type>`.
3. **Logic-Free Data Blueprints (`def`)**: Keep `def` declarations pure data structures with defaults and union constraints (`"a" | "b"`). Behavior belongs exclusively in verbs (`to ... end`).
4. **Strict Boundary Between Parsing, Shape & Meaning (Zero Implicit Magic)**:
   - Explicit format conversion (`as json`, `as csv`) is strictly separated from `as <type>` shape validation (and chained `as` is forbidden).
   - `as <type>` enforces **shape / type conformity** only on structured `map`/`record` values (raw `text` -> `TypeError`), requires **every field declared in `def`** without default-filling (`Missing field '<type>.<field>'`), and restricts `as text` + `{...}` interpolation to atomic (`text`, `number`) values. Default filling belongs exclusively to `<def> with ...`.
   - `to ...` + `check` handles **domain validity** (business rules, ranges, invariants).
5. **No Legacy Syntax Regressions & Arrow Law**: Do not reintroduce `do`, `fail`, `decode_json`, `encode_json`, `push`, `raises`, `let`, `var`, `set`, `report`, `test:`, `check "..."`, `check:`, `[a b]` parameter blocks, or `[]` list literals/indexing. Preserve the Arrow Law (`-> <value>` or `-> end` only — never statements/side effects after `->`; `to` functions without `->` are `void` procedures).
6. **Tests & Docs Ship Together**: Every change to lexing, parsing, evaluation, or standard modules must include Go unit tests in `internal/ease/ease_test.go`, corresponding `.ease` coverage in `tests/` or `examples/`, and an exact update to the BNF grammar and docs in `README.md` if syntax changed.

---

## 4. Pull Request Checklist

- [ ] `go vet ./...` passes with zero warnings.
- [ ] `go test -v ./...` passes completely (with `-race` on Linux).
- [ ] All `tests/*.ease` files and offline `examples/*.ease` files pass `ease test`.
- [ ] Any syntax or built-in changes are accurately reflected in `README.md` (including the BNF grammar).
- [ ] Cross-platform cleanliness verified (use `filepath`, append `.exe` only when `runtime.GOOS == "windows"`).
