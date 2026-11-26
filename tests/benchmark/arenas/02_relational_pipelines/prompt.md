# Arena 02: Relational Pipelines (`where`, `->`, `update`, `sort`, `any`/`none`)

Write a single self-contained Ease program (no `test` block needed; hidden tests will be appended).
Output ONLY valid Ease code with no markdown fences and no explanation.

## Requirements

1. Define a closed union `def tier = "gold" | "silver" | "bronze"`.
2. Define a record `def order`:
   - `id = 0`
   - `customer = ""`
   - `total = 0`
   - `vip = tier`
3. Define a record `def receipt`:
   - `id = 0`
   - `summary = ""`
4. Write `to apply_vip_discount orders as list of order`:
   - Using `update ... where ... with ...` (NOT an imperative loop), reduce `total` by 10% (`it.total * 0.9`) for orders where `it.vip is "gold"`.
5. Write `to build_receipts orders min_total as list of receipt`:
   - Filter `orders` where `it.total >= min_total`, sort ascending by `it.vip` (closed union order: gold, silver, bronze) then by `it.id`, and project with `->` into `receipt` records where `id = it.id` and `summary = "{it.customer}:{it.total}"`.
6. Write `to queue_status orders as text`:
   - Using `none` / `any` existential predicates (NOT `count (... where ...)`):
     - If `none orders where it.total > 0` -> `"empty"`
     - If `any orders where it.vip is "gold" and it.total >= 100` -> `"priority"`
     - Otherwise -> `"standard"`

Use idiomatic declarative pipelines (`where`, `->`, `update`, `sort ... by`, `any`/`none`) with zero imperative `loop` + `append` boilerplate and explicit `as <return_type>` contracts.
