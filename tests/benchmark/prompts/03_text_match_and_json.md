# Arena 03: Text `match` Extraction, Two-Step JSON Casts & `or` Fallbacks

Write a single self-contained Ease program (no `test` block needed; hidden tests will be appended).
Output ONLY valid Ease code with no markdown fences and no explanation.

## Requirements

1. Define a closed union `def log_level = "INFO" | "WARN" | "ERROR"`.
2. Define a record `def log_entry`:
   - `level = log_level`
   - `code = 0`
   - `msg = ""`
3. Define a record `def api_user`:
   - `name = ""`
   - `age = 0`
4. Write `to parse_log_line line as log_entry`:
   - Use a `match` expression on `line`:
     - Match `"{lvl as log_level} [{code as number}] {msg}"` -> `log_entry with level = lvl, code = code, msg = msg`
     - `else` -> `log_entry with level = "ERROR", code = 0, msg = "invalid"`
5. Write `to parse_port raw as number`:
   - Convert `raw` to `number` using `as number or 8080`.
6. Write `to parse_user raw_json as api_user`:
   - Perform Ease's canonical two-step JSON cast (`m = raw_json as json`, then `-> m as api_user`).

Use explicit `as <return_type>` contracts and idiomatic Ease.
