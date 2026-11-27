# Arena 06: Holdout — 1-Based Grid Lookup, `where` Re-Indexing & Short-Circuit `any`

Write a single self-contained Ease program (no `test` block needed; hidden tests will be appended).
Output ONLY valid Ease code with no markdown fences and no explanation.

## Requirements

1. Define a closed union `def cell_state = "alive" | "dead"`.
2. Define a record `def cell`:
   - `x = 0`
   - `y = 0`
   - `state = cell_state`
3. Define a record `def cell_rank`:
   - `x = 0`
   - `rank = 0`
4. Write a function `to cell_at grid cols x y as cell`:
   - Uses 1-based row-major indexing (`idx = (y - 1) * cols + x`) to return the cell at `(x, y)` from `grid`.
5. Write a function `to alive_ranks grid as list of cell_rank`:
   - Filters `grid` to cells whose `state` is `"alive"`, then maps each surviving cell to a `cell_rank` record with `x = it.x` and `rank = it.x * 10 + it.index`.
6. Write a function `to has_safe_ratio nums as text`:
   - Returns `"found"` if `any` item in `nums` satisfies `(100 / it) is 25`, otherwise returns `"missing"`.

Use idiomatic Ease: explicit `as <return_type>` contracts on all `to` functions, zero `0`/`1` boolean flags, flat guards (max nesting depth <= 2).
