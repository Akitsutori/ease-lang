# Arena 01: Unions, Exhaustive Checks, Pure `def` Constants & Return Contracts

Write a single self-contained Ease program (no `test` block needed; hidden tests will be appended).
Output ONLY valid Ease code with no markdown fences and no explanation.

## Requirements

1. Define a closed union `def severity = "critical" | "high" | "low"`.
2. Define a closed union `def action_kind = "page_oncall" | "open_ticket" | "log_only"`.
3. Define a record `def alert`:
   - `id = 0`
   - `sev = severity`
   - `retries = 0`
4. Define a pure immutable constant `def max_retries = 3`.
5. Write a function `to route_alert a as action_kind`:
   - If `a.sev` is `"critical"` -> `"page_oncall"`
   - If `a.sev` is `"high"` and `a.retries >= max_retries` -> `"page_oncall"`
   - If `a.sev` is `"high"` -> `"open_ticket"`
   - If `a.sev` is `"low"` -> `"log_only"`
6. Write a function `to bump_retry a as alert`:
   - Returns a new `alert` with `retries = a.retries + 1`.
7. Write a function `to label_alert a as text`:
   - Returns `"#{a.id}:{a.sev}"`.

Use idiomatic Ease: explicit `as <return_type>` contracts on all `to` functions, zero `0`/`1` boolean flags, flat guards (max nesting depth <= 2).
