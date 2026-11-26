# Ease `llms.txt` Benchmark Arenas

This folder benchmarks whether `llms.txt` alone is sufficient for a small LLM to write **idiomatic, human-readable, compiler-verified Ease code** — and to reason accurately about existing Ease code without executing it.

Everything here is isolated under `tests/benchmark/` so normal `ease check tests/*.ease` runs are unaffected.

## Layout

```text
tests/benchmark/
├── README.md
├── grade.go                         # Deterministic compiler + AST idiom + coverage grader
├── results/                         # Generated Markdown scorecards (latest.md)
└── arenas/
    ├── 01_unions_and_contracts/     # Closed unions, exhaustive check, pure def constants, as <type>
    ├── 02_relational_pipelines/     # where, -> projection, update ... with, sort ... by, any/none
    ├── 03_text_match_and_json/      # match extraction, two-step as json -> Record, or fallbacks
    ├── 04_concurrency_and_ecs/      # swarm loop, inbox/spawn/take, -> stop / -> return, pure ECS
    └── 05_reasoning_trace/          # Reverse test: trace outputs & static errors without running
```

Each coding arena (`01`–`04`) contains:
- `prompt.md` — the exact task prompt given to the model.
- `hidden_test.ease` — hidden assertions appended during grading to verify functional correctness.
- `rubric.json` — AST idiom expectations (required constructs, max nesting depth, return contracts).
- `submission.ease` — the model's generated code (overwritten on each run).

Arena `05_reasoning_trace` contains:
- `prompt.md` — 5 short Ease snippets with questions.
- `expected.json` — exact ground-truth answers.
- `submission.json` — the model's JSON answer object.

## How to Run & Rerun Manually (Without an Agent)

### Step 1: Generate Submissions with Any Small LLM
For each arena (`01` through `05`):
1. Start a **fresh** chat session with your target model (e.g. Gemini Flash Lite, Qwen-2.5-Coder-7B, Llama-3.1-8B) so there is zero prior context.
2. Paste the **entire raw contents** of `llms.txt` as the system prompt (or first message).
3. Paste the arena's `prompt.md` as the user message.
4. Save the model's raw output (code only, no markdown fences) to:
   - `tests/benchmark/arenas/<arena>/submission.ease` (for Arenas `01`–`04`)
   - `tests/benchmark/arenas/05_reasoning_trace/submission.json` (for Arena `05`)

### Step 2: Run the Grader
From the repository root:

```powershell
go build -o ease.exe ./cmd/ease
go run ./tests/benchmark/grade.go
```

The grader automatically:
1. Audits `llms.txt` for **100% keyword & built-in coverage** against `internal/ease/token.go` and `internal/ease/builtins.go`.
2. Runs `ease.exe check` on each `submission.ease` (`Check@1`).
3. Appends `hidden_test.ease` and runs `ease.exe test` (`Pass@1`).
4. Parses each `submission.ease` AST to compute the **AST Idiom Score** (penalizing `0`/`1` pseudo-booleans, imperative `loop` + `append` where `where`/`->`/`any`/`none` applies, missing `as <type>` return contracts, and nesting depth > 2).
5. Grades `05_reasoning_trace/submission.json` against `expected.json`.
6. Prints the full scorecard and saves it to `tests/benchmark/results/latest.md`.

### Optional: Testing Self-Repair (`Pass@2`)
If an arena fails `Check@1` or `Pass@1`:
1. Copy **only** the compiler error output from `grade.go` back into the same LLM session with: `"Fix the Ease code using this compiler error and output only the corrected code."`
2. Overwrite `submission.ease` and re-run `go run ./tests/benchmark/grade.go`.

### Optional: Rerun Automatically in Antigravity
Ask Antigravity: *"Rerun all 5 benchmark arenas in `tests/benchmark/arenas/` in parallel with `flash_lite` subagents using only `llms.txt`, then run `go run ./tests/benchmark/grade.go`."*
