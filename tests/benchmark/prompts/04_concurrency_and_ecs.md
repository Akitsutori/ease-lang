# Arena 04: Concurrency & Flow (`swarm loop`, `inbox`, `spawn`, `take`, `-> stop`, `-> return`)

Write a single self-contained Ease program (no `test` block needed; hidden tests will be appended).
Output ONLY valid Ease code with no markdown fences and no explanation.

## Requirements

1. Define a record `def job`:
   - `id = 0`
   - `value = 0`
2. Write `to double_all nums as list of number`:
   - Use `swarm loop nums -> it * 2` (or multi-line `swarm loop nums ... end`) to double every number in parallel.
3. Write a void procedure `to worker jobs out`:
   - Use `take jobs ... end` to receive `job` values from `jobs`.
   - Inside `take jobs`:
     - If `it.value < 0`: call `close out`, then exit the procedure immediately with `-> return`.
     - If `it.value is 0`: exit the `take` loop early with `-> stop`.
     - Otherwise: `put out (it with value = it.value * 10)`.
   - After `take jobs ... end`, call `close out`.
4. Write `to run_pipeline items as list of job`:
   - Create `jobs = inbox as job` and `out = inbox as job`.
   - `spawn worker jobs out`.
   - Loop over `items` and `put jobs it`, then `close jobs`.
   - Collect all items from `out` into `res = list as job` using `take out` (`res = append res it`), and return `-> res`.

Use idiomatic Ease, explicit `as <return_type>` on value-returning functions, and flat guards (max nesting depth <= 2).
