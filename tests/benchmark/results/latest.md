# Ease `llms.txt` Benchmark Scorecard (2026-11-26 22:03:32)

## 1. `llms.txt` Specification Audit

- Size: 206 lines, ~9897 chars | Keyword & builtin coverage: **54/54**
- Missing keywords/builtins: none (100% covered)
- Embedded `VERIFIED EXAMPLE` check: **PASS**

## 2. Coding Arenas (`01`–`04`)

| Arena | Check@1 | Pass@1 | IdiomScore | MaxNest | Notes |
|---|---|---|---|---|---|
| 01_unions_and_contracts | PASS | FAIL | 100% | 2 | FAIL  test "arena 01 hidden verification" (C:\Users\alexe\AppData\Local\Temp\ease_bench_01_unions_and_contracts_1795727012226202800.ease:52) |
| 02_relational_pipelines | FAIL | FAIL | 0% | 0 | SyntaxError at line 22, col 1: Nested 'to' function definitions are not allowed inside blocks |
| 03_text_match_and_json | PASS | PASS | 100% | 1 | ok |
| 04_concurrency_and_ecs | FAIL | FAIL | 0% | 0 | SyntaxError at line 7, col 34: Expected newline after 'swarm loop' iterable |

## 3. Reasoning Trace (`05_reasoning_trace`)

- Accuracy: **4 / 5**
- Details: q3: want "96" got "99"

## Summary

- `Check@1`: 2/4 | `Pass@1`: 1/4 | Avg `IdiomScore`: 50% | Trace: 4/5
