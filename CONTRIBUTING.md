# Contributing to Ease

Thank you for your interest in **Ease**! This guide explains the repository architecture, where you can make meaningful contributions, local setup requirements, and the language design invariants that every change must preserve.

---

## 1. Architecture & Where to Contribute

The entire Ease toolchain lives in a single, self-contained Go module:

```text
.
├── cmd/ease/main.go             # CLI entrypoint (run, test, build, ast, tokens, repl, version)
├── internal/ease/
│   ├── token.go                 # Token types and reserved keywords
│   ├── lexer.go                 # Scanner (numbers, strings with {interpolation}, # comments, operators)
│   ├── ast.go                   # AST node definitions for declarations, statements, and expressions
│   ├── parser.go                # Recursive-descent parser and stdlib import resolution
│   ├── typecheck.go             # Strict `as` / `list as` target-type validation (built-ins & `def` blueprints)
│   ├── checker.go               # Runner for `test` and `check "..."` assertion blocks
│   ├── eval.go                  # Tree-walking evaluator, runtime values, JSON casts, and built-ins
│   ├── screen.go                # Ebitengine main-thread bridge and headless `--check` frame simulation
│   ├── stdlib.go                # Embedded `std/*.ease` loader (`//go:embed`)
│   ├── bundle.go                # Standalone executable packer (`EASE_BUNDLE_V1`)
│   ├── errors.go                # Structured `EaseError` diagnostics with source snippets and hints
│   ├── ease_test.go             # Go test suite covering lexer, parser, evaluator, modules, and CLI behavior
│   └── std/
│       ├── network.ease         # `network` standard module (`def` schemas + `ask`/`send` via `http_call`)
│       └── screen.ease          # `screen` standard module (`def` schemas + `send` via `screen_present`)
├── examples/                    # Runnable examples and end-to-end reference specifications
├── tests/                       # Language feature test files (01–07) executed via `ease test`
└── .github/workflows/ci.yml     # Cross-platform CI pipeline (Ubuntu Linux & Windows)
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

### Building and Running Checks Locally

**Linux / macOS:**

```bash
go vet ./...
go test -race -v ./...
go build -o ease ./cmd/ease

for f in tests/*.ease; do ./ease test "$f"; done
./ease run examples/06_window.ease --check
```

**Windows (PowerShell):**

```powershell
go vet ./...
go test -v ./...
go build -o ease.exe .\cmd\ease

Get-ChildItem tests\*.ease | ForEach-Object { .\ease.exe test $_.FullName }
.\ease.exe run examples\06_window.ease --check
```

---

## 3. Language Design Invariants (Read Before Opening a PR)

Ease intentionally optimizes for cognitive simplicity and syntactic uniformity. Please uphold these invariants across all contributions:

1. **Uniform Bracket-Free, Comma-Free Calls**: Call verbs with `do <verb> arg1 arg2` and declare parameters with `to <verb> param1 param2` — never introduce `fn(...)` call syntax, commas between arguments/parameters, or `[params]` parameter lists. Parentheses `(...)` are strictly for expression grouping (e.g., `total = total + (n * n)`). Commas are reserved for `list` literals (`list 1, 2, 3`) and single-line `with` updates.
2. **Dot Notation Only for Lists and Records**: Index lists and access fields consistently with `.` (`cloud.0.vx`, `player.hp`). Build lists with `list 1, 2, 3` or `list as <type>`.
3. **Logic-Free Data Blueprints (`def`)**: Keep `def` declarations pure data structures with defaults and union constraints (`"a" | "b"`). Behavior belongs exclusively in verbs (`to ... end`).
4. **Strict Boundary Between Parsing, Shape & Meaning (Zero Implicit Magic)**:
   - Explicit JSON parsing/serialization (`do decode_json` / `do encode_json`) is strictly separated from `as <type>` validation.
   - `as <type>` enforces **shape / type conformity** only on structured `map`/`record` values (raw `text` -> `TypeError`), requires **every field declared in `def`** without default-filling (`Missing field '<type>.<field>'`), and restricts `as text` + `{...}` interpolation to atomic (`text`, `number`) values. Default filling belongs exclusively to `<def> with ...`.
   - `to ...` + `check` handles **domain validity** (business rules, ranges, invariants).
5. **No Legacy Syntax Regressions**: Do not reintroduce `let`, `var`, `set`, `report`, `test:`, `check:`, `[a b]` parameter blocks, or `[]` list literals/indexing.
6. **Tests & Docs Ship Together**: Every change to lexing, parsing, evaluation, or standard modules must include Go unit tests in `internal/ease/ease_test.go`, corresponding `.ease` coverage in `tests/` or `examples/`, and an exact update to the BNF grammar and docs in `README.md` if syntax changed.

---

## 4. Pull Request Checklist

- [ ] `go vet ./...` passes with zero warnings.
- [ ] `go test -v ./...` passes completely (with `-race` on Linux).
- [ ] All `tests/*.ease` files and offline `examples/*.ease` files pass `ease test`.
- [ ] Any syntax or built-in changes are accurately reflected in `README.md` (including the BNF grammar).
- [ ] Cross-platform cleanliness verified (use `filepath`, append `.exe` only when `runtime.GOOS == "windows"`).
