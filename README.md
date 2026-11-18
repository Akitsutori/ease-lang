<p align="center">
  <img src="assets/logo.svg" alt="Ease Logo" width="96" />
</p>

# Ease

**Ease** is a compact, strongly boundary-checked programming language implemented in Go. It is built around bracket-free prefix calls (`to`), logic-free data blueprints (`def`), explicit union value spaces (no `bool`), declarative guards (`check`), and built-in tests (`test`).

> **VS Code Support:** Syntax highlighting, live diagnostics (`ease lsp`), outline symbols, and autocomplete are documented in [README_VSCODE.md](README_VSCODE.md).  
> **1-Page LLM Reference:** See [`llms.txt`](llms.txt).

---

## Table of Contents

1. [Installation & CLI](#installation--cli)
2. [Tutorial: Build a Text Adventure in Ease](#tutorial-build-a-text-adventure-in-ease)
   - [1. Entering the Tavern](#1-entering-the-tavern)
   - [2. The Command Loop](#2-the-command-loop)
   - [3. Choosing Your Move](#3-choosing-your-move)
   - [4. Hero Stats, Damage & Combat Outcomes](#4-hero-stats-damage--combat-outcomes)
   - [5. Cleaning Up the Game Loop](#5-cleaning-up-the-game-loop)
   - [6. Ambush! Fighting a Pack of Monsters](#6-ambush-fighting-a-pack-of-monsters)
   - [7. Loot Tables & Savegames](#7-loot-tables--savegames)
   - [8. Expanding the World & Testing Combat Rules](#8-expanding-the-world--testing-combat-rules)
3. [Why Ease Works Well for AI Agents](#why-ease-works-well-for-ai-agents)
4. [Built-in Verbs](#built-in-verbs)
5. [Formal Grammar (EBNF)](#formal-grammar-ebnf)
6. [Running Tests & Project Layout](#running-tests--project-layout)

---

## Installation & CLI

### Prerequisites & Build

- **Go** matching [`go.mod`](go.mod) (`go 1.27.4`)
- **Linux system dependencies** (required by [Ebitengine](https://ebitengine.org/) for `screen`):
  ```bash
  sudo apt-get update
  sudo apt-get install -y libc6-dev libgl1-mesa-dev libxcursor-dev libxi-dev \
    libxinerama-dev libxrandr-dev libxxf86vm-dev libasound2-dev pkg-config
  ```

```bash
# Linux / macOS
go build -o ease ./cmd/ease

# Windows (PowerShell)
go build -o ease.exe .\cmd\ease
```

### CLI Reference

```text
ease run <file.ease> [--json] [--check] [--trace]    Run an Ease program (optionally running tests first, tracing, or emitting JSON)
ease check <file.ease> [--json]                      Parse and type-check without executing
ease test <file.ease> [--json] [--verbose] [--trace] Run all 'test' assertions
ease build <file.ease> [-o <out>]                    Compile an Ease program into a standalone binary
ease ast <file.ease> [--json]                        Inspect parsed defs, functions, checks, and statements (or full AST JSON)
ease tokens <file.ease>                              Inspect lexer tokens with line/column positions
ease repl                                            Start interactive Ease REPL
ease lsp                                             Start Language Server (stdio JSON-RPC for editors)
ease version                                         Print Ease CLI version
```

Shorthand: `ease <file.ease>` is equivalent to `ease run <file.ease>`.

---

## Tutorial: Build a Text Adventure in Ease

In this tutorial, we build a playable **text adventure** from scratch. Each chapter advances the game and introduces only the Ease ideas needed for that scene.

### 1. Entering the Tavern

Our adventure begins in a tavern. Before the hero sets off, the game asks for their name, buys a few supplies, calculates their starting gold, and prints a welcome banner.

In Ease, variables are plain assignments (`=`) and constants use `def`. Text in double quotes (`"..."`) interpolates `{expr}` values and normalizes to Unicode NFC, while single-quoted text (`'...'`) is raw 1:1 literal text (handy for JSON strings). Numbers are 64-bit floats (`/` is real division, and `-2` directly before digits is a negative literal; write `a - 2` with spaces for subtraction). Parentheses `(...)` group arithmetic sub-expressions, `+` combines only `number + number` or `text + text`, and `ask` / `say` handle console input and output:

```ease
def starting_gold = 50

hero_name = ask "What is your name, traveler?"
gold = starting_gold - (2 * 5)
sign_json = '{"tavern": "The Rusty Tankard"}'

title = "Traveler " + hero_name
say "Welcome, {title}! You start with {gold} gold."
say "Tavern sign: {sign_json}"
```

---

### 2. The Command Loop

Now the hero steps into the dungeon and starts typing commands. The game needs a real command loop that keeps asking for input until the player types `"quit"`.

Ease has a single loop construct: `loop ... end` (or `loop <list|range> ... end` when iterating over values). An unbounded `loop ... end` binds `it.index` (0, 1, 2, ...) and requires at least one exit guard: a single-line `check <cond> -> end` breaks out of the innermost loop immediately:

```ease
loop
    cmd = ask "> "
    check cmd is "quit" -> end
    say "Turn {it.index}: you typed '{cmd}'."
end

say "Farewell, adventurer!"
```

---

### 3. Choosing Your Move

Typing commands is only fun if the dungeon reacts. The player should be able to `"look"`, `"fight"`, or `"flee"` — and get a helpful message if they type anything else.

Ease has no `if` keyword and no `bool` (`true`/`false`) type. Instead, you model choices explicitly (for example as a closed union type `def move = "look" | "fight" | "flee"`) and dispatch with a multi-line `check ... else -> ... end` block:

```ease
def move = "look" | "fight" | "flee"

loop
    cmd = ask "Choose (look, fight, flee, quit)> "
    check cmd is "quit" -> end

    check
        cmd is "look"  -> say "Torchlight flickers along damp stone walls."
        cmd is "fight" -> say "You raise your sword!"
        cmd is "flee"  -> say "You slip back into the shadows."
        else -> say "Unknown move '{cmd}'. Try look, fight, or flee."
    end
end
```

---

### 4. Hero Stats, Damage & Combat Outcomes

A real fight needs hero stats — `name`, `hp`, and `stamina` — and an attack outcome that is richer than a boolean: a swing might update the hero, end in `"fled"`, or fail with a `CombatError` when stamina is too low.

Ease separates logic-free noun blueprints (`def`) from actions. Records are immutable: `with` creates an updated copy. Combining a record, a text literal, and an error record with `|` creates an explicit union (`turn_outcome`), and `check` narrows `res` inside `res is CombatError -> ...` so `res.code` and `res.message` are safe to read:

```ease
def CombatError
    code    = 400
    message = "Invalid action"
end

def hero
    name    = "Aria"
    hp      = 100
    stamina = 10
end

def turn_outcome = hero | "fled" | CombatError

aria = hero with name = "Aria", stamina = 4
aria_after_hit = aria with hp = aria.hp - 15, stamina = aria.stamina - 2

res = CombatError with code = 409, message = "Not enough stamina"
check
    res is hero        -> say "{res.name} now has {res.hp} HP."
    res is "fled"      -> say "Hero escaped safely."
    res is CombatError -> say "Blocked ({res.code}): {res.message}"
end
say "After skirmish: {aria_after_hit.name} HP={aria_after_hit.hp}"
```

---

### 5. Cleaning Up the Game Loop

As the dungeon grows, inline damage math and `check` blocks make the main loop noisy. Let's extract reusable combat rules like `calc_damage`, `heal`, and `attempt_strike`.

Functions are declared with `to <name> <params...>` (space-separated, no commas) and return their last evaluated expression. Calls use prefix syntax without parentheses; `(...)` groups compound arguments (`heal aria.hp (calc_damage 14 3)`). Inside a `to` function, `check <cond> -> <value>` returns immediately from the function, and every assignment stays local (`Assigned-in-Function = Local`):

```ease
def base_crit = 1.5

def CombatError
    code    = 400
    message = "Invalid action"
end

def hero
    name    = "Aria"
    hp      = 100
    stamina = 10
end

def turn_outcome = hero | "fled" | CombatError

to calc_damage attack armor
    (attack - armor) * base_crit
end

to heal current bonus
    next_hp = current + bonus
    next_hp
end

to attempt_strike h cost
    check h.hp <= 0 -> CombatError with code = 410, message = "Hero has fallen"
    check h.stamina < cost -> CombatError with code = 409, message = "Not enough stamina"
    h with stamina = h.stamina - cost
end

aria = hero with hp = 60, stamina = 5
repaired_hp = heal aria.hp (calc_damage 14 3)
res = attempt_strike (aria with hp = repaired_hp) 2

check
    res is hero        -> say "{res.name}: HP={res.hp}, stamina={res.stamina}"
    res is "fled"      -> say "Retreated"
    res is CombatError -> say "Blocked: {res.message}"
end
```

---

### 6. Ambush! Fighting a Pack of Monsters

Around the next corner, a whole pack of monsters ambushes our hero! We need to filter out defeated enemies, apply end-of-turn poison to every survivor at once, scale a list of damage rolls, and clean up the loot names dropped in the room.

Lists are homogeneous (`list 1, 2, 3` or `list as <type>`) with `.0` / `.(i)` indexing and element-wise numeric arithmetic. Both `where` filters and batch `with` updates bind `it` and `it.index`; `append` builds new lists without mutating in place, and built-in text verbs (`trim`, `split`, `join`, `upper`, `slice`, `contains`) clean up text safely:

```ease
def monster
    name = ""
    hp   = 0
end

pack = list as monster with
    name = "Goblin", hp = 12
    name = "Slime",  hp = 0
    name = "Orc",    hp = 35
end

alive = pack where
    it.hp > 0
end
poisoned = alive with hp = it.hp - 3
say "Surviving monsters: {count poisoned}, first HP: {poisoned.0.hp}"

rolls = list 4, 6, 8
scaled = rolls * 1.5 + 2
say "Scaled rolls total: {sum scaled}, avg: {avg scaled}, second: {scaled.(1)}"

raw_loot = "  rusty key, iron shield, broken horn  "
drops = split (trim raw_loot) ", "
kept = list as text

loop drops
    check it is "broken horn" -> end
    check not (contains it "iron") -> say "Non-iron drop: {it}"
    tag = upper (slice it 0 4)
    kept = append kept "{it.index}:{tag}"
end
say "Pack loot: {join kept " | "}"
```

---

### 7. Loot Tables & Savegames

A full adventure needs external data: a CSV loot table for treasure chests and a JSON save file so the hero can resume later.

Ease keeps external data untrusted until you explicitly validate it with `as`. `load path [as text|csv|bytes]` reads UTF-8/NFC text (50 MB max, BOM stripped), uniform CSV rows (`list` of `map` with positional `it.0` and header `it.("Header")` access), or raw `0..255` bytes; `save path data` writes text or bytes and returns the byte count; `decode_json` / `encode_json` handle JSON conversion:

```ease
def rarity = "common" | "rare"

def loot_item
    name  = ""
    value = 0
    tier  = rarity
end

def hero
    name = "Aria"
    hp   = 100
end

rows = load "./datafiles/loot.csv" as csv
chest = list as loot_item
loop rows
    item = loot_item with
        name  = it.("Item Name")
        value = it.1 as number
        tier  = it.2 as rarity
    end
    chest = append chest item
end
rare_chest = chest where it.tier is "rare"
say "Rare items loaded: {count rare_chest}"

saved = hero with name = "Aria", hp = 85
bytes_written = save "./save.json" (encode_json saved)
restored = (decode_json (load "./save.json" as text)) as hero
raw_bytes = load "./save.json" as bytes
say "Saved {bytes_written} bytes ({count raw_bytes} raw), restored HP: {restored.hp}"
```

---

### 8. Expanding the World & Testing Combat Rules

As our world grows, we split combat mechanics into `./combat.ease`, connect optional online highscores (`import network`) or a 2D dungeon map (`import screen`), and lock down our rules with built-in tests (`test ... end`).

All modules — file modules (`import "./file.ease" as mod`) and embedded standard modules (`import network`, `import screen`, optionally `as <alias>`) — are encapsulated namespaces: call module verbs with **`:`** (`mod:verb`) and access module blueprints, unions, and constants with **`.`** (`mod.blueprint`, `mod.const`).

```ease
# combat.ease
def crit_multiplier = 1.5

def fighter
    name = ""
    hp   = 100
end

to strike target power
    target with hp = target.hp - (power * crit_multiplier)
end
```

```ease
# main.ease
import "./combat.ease" as combat
import network
import screen

goblin = combat.fighter with name = "Goblin", hp = 30
hit_goblin = combat:strike goblin 10
say "{hit_goblin.name} HP left: {hit_goblin.hp} (crit x{combat.crit_multiplier})"

api = network.network with url = "https://scores.example.test", timeout = 5
resp = network:send api "/runs" hit_goblin

win = screen.screen with width = 640, height = 480, title = "Ease Dungeon"
hud = screen.scene with background = "slate"
raw_frame = screen:present win hud
frame = decode_json raw_frame as screen.frame_state
```

Finally, `test ["label"] ... end` blocks run via `ease test <file.ease>` as soft assertions with `is`, `is not`, `is error`, `-> error.type / error.message end`, and `loop` over test cases:

```ease
to safe_strike hp power
    check power <= 0 -> fail "Power must be positive"
    hp - power
end

def strike_case
    hp    = 0
    power = 0
    want  = 0
end

test "dungeon combat assertions"
    cases = list as strike_case with
        hp = 50, power = 15, want = 35
        hp = 20, power = 20, want = 0
    end

    loop cases
        safe_strike it.hp it.power is it.want
    end

    (safe_strike 50 0) is error
    (safe_strike 50 0) ->
        error.type is "RuntimeError"
        error.message is "Power must be positive"
    end
end
```

- **Headless screen testing:** `ease run <file.ease> --check` runs `screen` loops deterministically without opening an OS window. Local `./std/` overrides can be enabled via `EASE_STD=1`.

---

## Why Ease Works Well for AI Agents

Ease pairs a tiny, unambiguous grammar with [`llms.txt`](llms.txt) so models can hold the entire language specification in context:

- **−54.6 % generated code vs. Python** across 36 benchmark tasks.
- **100 % `pass@1` accuracy** when prompted with `llms.txt`.
- **Single-command machine feedback:** `ease check --json`, `ease test --json`, `ease ast --json`, and `ease lsp` catch syntax, type, exhaustiveness, and assertion failures deterministically.

---

## Built-in Verbs

| Built-in | Behavior |
| :--- | :--- |
| `say expr` | Prints formatted value to `stdout` |
| `ask prompt` | Reads one UTF-8 line from `stdin` (use `ask ""` for empty prompt) |
| `count xs` | Returns rune length of `text` or element count of `list` |
| `type val` | Returns runtime type or `def` blueprint name as `text` |
| `range start end` | Returns inclusive `number` list `[start .. end]` (empty if `start > end`) |
| `append xs item_or_list` | Returns a new list with an item or compatible list appended |
| `slice xs start end` | Returns 0-based `[start:end)` clamped slice of `list` or UTF-8 `text` (by runes) |
| `sum xs` / `avg xs` / `sort xs` | Numeric aggregations (`sum`, `avg`) and ascending sort (`number`/`text` lists) |
| `split txt sep` / `join items sep` | Splits `text` into `list` of `text` (or runes for `""`) / joins `text` list |
| `trim txt` / `upper txt` / `lower txt` | Unicode whitespace trimming and case conversion |
| `replace txt old new` | Replaces all non-empty `old` occurrences with `new` in `text` |
| `contains txt sub` | Condition verb (for `check`, `where`, `test`) checking substring membership |
| `abs n` / `floor n` / `round n` / `min a b` / `max a b` | Numeric math helpers |
| `fields rec` | Returns field names in declaration order for a `def` record or `map` |
| `decode_json txt` / `encode_json val` | Parses JSON `text` / encodes values to JSON `text` |
| `load path [as text\|csv\|bytes]` | Reads UTF-8/NFC `text`, uniform `csv` `map` list, or `0..255` byte list |
| `save path data` | Writes UTF-8 `text` or `0..255` byte list, returning written byte count |
| `fail msg` | Raises an explicit `RuntimeError` |

---

## Formal Grammar (EBNF)

The grammar below matches [`internal/ease/lexer.go`](internal/ease/lexer.go) and [`internal/ease/parser.go`](internal/ease/parser.go).

### Lexical Structure

```ebnf
WHITESPACE    ::= ( " " | "\t" | "\r" )+            (* skipped *)
COMMENT       ::= "#" { ANY_CHAR - "\n" }           (* skipped *)
NL            ::= "\n"+

LETTER        ::= UNICODE_LETTER | "_"
DIGIT         ::= [0-9]
IDENT         ::= LETTER { LETTER | DIGIT }         (* excluding reserved keywords *)

NUMBER        ::= [ "-" ] DIGIT+ [ "." DIGIT+ ]    (* "-" directly before a digit is an atomic negative literal; use "- " for subtraction *)
STRING        ::= '"' { CHAR - ('"' | "\n" | "{" | "\\") | ESCAPE } '"'
RAW_STRING    ::= "'" { CHAR - ("'" | "\n") } "'"   (* 1:1 literal: no interpolation and no escapes *)
INTERP_STRING ::= '"' { CHAR - ('"' | "\n" | "\\") | ESCAPE | "{" EXPR_TEXT "}" } '"'
ESCAPE        ::= "\\" ( "n" | "t" | '"' | "\\" | "{" )
```

### Reserved Keywords & Operators

```text
Keywords:
  to  import  def  with  where  end  test  check  is
  say  as  else  loop
  and  or  not  text  number  list

Operators & Punctuation:
  ->  (  )  ,  .  :  |  =  <  <=  >  >=  +  -  *  /  %
```

### Program, Declarations & Statements

```ebnf
program        ::= [NL] { top_item [NL] } EOF

top_item       ::= import_decl
                 | func_decl
                 | def_decl
                 | top_test_blk
                 | stmt

import_decl    ::= "import" ( IDENT [ "as" IDENT ] | STRING "as" IDENT ) stmt_end

func_decl      ::= "to" IDENT { IDENT } ( NL | &"end" )
                   { stmt [NL] } "end" stmt_end

def_decl       ::= "def" IDENT "=" expr { "|" expr } stmt_end
                 | "def" IDENT NL { field_decl [NL] } "end" stmt_end
field_decl     ::= IDENT "=" expr { "|" expr } stmt_end

top_test_blk   ::= "test" [STRING] ( NL | &"end" )
                   { test_item [NL] } "end" stmt_end

stmt           ::= "say" expr stmt_end
                 | "end" stmt_end
                 | loop_stmt
                 | check_guard
                 | assign_stmt
                 | expr stmt_end

assign_stmt    ::= IDENT "=" expr stmt_end
loop_stmt      ::= "loop" [ "range" unary unary | expr ] NL { stmt [NL] } "end" stmt_end

check_action   ::= "end" stmt_end | stmt
check_guard    ::= "check" expr "->" check_action
                 | "check" NL { check_rule [NL] } [ "else" ( "->" check_action | NL { stmt [NL] } ) ] "end" stmt_end
check_rule     ::= expr ( "->" check_action | stmt_end )

test_item      ::= loop_stmt
                 | "say" expr stmt_end
                 | assign_stmt
                 | expr "->" [NL] { test_item [NL] } "end" stmt_end
                 | expr "is" ["not"] "error" stmt_end
                 | expr "is" ["not"] ( "text" | "number" | "list" ) stmt_end
                 | expr "is" ["not"] expr stmt_end
                 | expr stmt_end

stmt_end       ::= NL | EOF | &("end" | "else")
```

### Expressions (Precedence Low to High)

```ebnf
expr           ::= or_expr
or_expr        ::= and_expr { "or" and_expr }
and_expr       ::= not_expr { "and" not_expr }
not_expr       ::= "not" not_expr | comparison

comparison     ::= term { cmp_op term | is_op ( type_kw | term ) }
cmp_op         ::= "<" | "<=" | ">" | ">="
is_op          ::= "is" ["not"]
type_kw        ::= "text" | "number" | "list"

term           ::= factor { ( "+" | "-" ) factor }
factor         ::= unary  { ( "*" | "/" | "%" ) unary }
unary          ::= "-" unary | postfix

postfix        ::= primary { "." IDENT
                           | "." INT_PATH
                           | "." "(" expr ")"
                           | ":" IDENT call_args
                           | with_clause
                           | where_clause
                           | "as" cast_type
                           | call_args }

with_clause    ::= "with" NL { with_field stmt_end [NL] } "end"
                 | "with" with_field { "," with_field }
with_field     ::= IDENT "=" expr

where_clause   ::= "where" NL { expr stmt_end [NL] } "end"
                 | "where" expr

cast_type      ::= "text" | "number" | "csv" | "bytes" | IDENT [ "." IDENT ]
call_args      ::= unary{k}

primary        ::= NUMBER | STRING | RAW_STRING | INTERP_STRING | IDENT | "(" expr ")" | list_expr
list_expr      ::= "list" "as" list_elem_type [ "with" NL { with_field { "," with_field } stmt_end [NL] } "end" ]
                 | "list" expr { "," expr }
list_elem_type ::= "text" | "number" | IDENT [ "." IDENT ]
```

---

## Running Tests & Project Layout

```bash
go vet ./...
go test -v ./...
for f in tests/*.ease; do ./ease test "$f"; done
./ease test examples/05_network.ease
./ease run examples/06_window.ease --check
```

```text
cmd/ease/           CLI entrypoint
internal/ease/      Lexer, Parser, AST, Typechecker, Evaluator, Screen, Bundle & Stdlib
examples/           Executable examples and reference specifications
tests/              Language feature test files (01–08)
.github/workflows/  GitHub Actions CI for Ubuntu Linux and Windows
```

See [`SECURITY.md`](SECURITY.md), [`CONTRIBUTING.md`](CONTRIBUTING.md), and [`LICENSE`](LICENSE).
