# README Syntax-Audit & Testmatrix (Review-Vorlage)

Diese Übersicht prüft jede Syntax-Behauptung in [`README.md`](../README.md) Zeile für Zeile mit **1 Happy Path + 10 Negativtests** pro Kategorie.

- **Laufzeit-Negativtests (`raises`)**: Direkt ausführbar in [`examples/syntax_spec.ease`](syntax_spec.ease) via `ease test examples/syntax_spec.ease`.
- **Lexer-/Parser-/Typecheck-Negativtests**: Syntaktisch oder statisch ungültige Programme (müssen bereits bei `NewLexer(...).Tokenize()` bzw. `ParseProgram(...)` abbrechen). Nach deinem Review überführe ich alle Fälle direkt in `internal/ease/readme_syntax_test.go`.

---

## 1. Lexical Structure (`README.md`)
- **Happy Path**: Unicode-Ident `größe_1 = 42`, Kommentare `# ...`, Escapes `\n \t \" \\ \{`, Interpolation `"Hi {name}: {1 + 2}"`, Rohtext `'{"id": 7}'` (1:1 ohne Interpolation/Escapes)
- **10 Negativtests**:
  1. Unterminierter String: `"hello`
  2. Mehrzeiliger Rohstring: `"hello\nworld"`
  3. Unterminierter Rohtext in `'...'`: `'hello`
  4. Interpolation auf Nicht-Atomwerten: `"{hero}"` (Laufzeit-`TypeError`)
  5. Leere Interpolation: `"{}"`
  6. Unterminierte Interpolation: `"{x + 1"`
  7. Ungültiger Ausdruck in Interpolation: `"{1 + }"`
  8. Unerlaubtes Zeichen außerhalb String: `@x = 1`
  9. Führender Punkt bei Zahl: `.5`
  10. Abschließender Punkt bei Zahl: `5.`

## 2. Variables & Constants (`README.md` L84–L99, L391, L413)
- **Happy Path**: `x = 10`, `x = x + 5`, `def pi = 3.14`
- **10 Negativtests**:
  1. Legacy `let x = 1`
  2. Legacy `var x = 1`
  3. Legacy `set x = 1`
  4. Konstante überschreiben: `def pi = 3.14` + `pi = 4`
  5. Keyword als Variable: `if = 5`
  6. Typ-Keyword als Variable: `text = "hi"`
  7. Typ-Keyword als Variable: `number = 5`
  8. `def`-Konstante ohne Ausdruck: `def pi =`
  9. Zuweisung ohne rechte Seite: `x =`
  10. Zuweisung an Literal: `1 = 2`

## 3. Functions (`to ... end`) & Calls (`do`) (`README.md`)
- **Happy Path**: `to add a b ... end`, `do add 2 3`, `to ask client path` (keine Kommas in Parameterlisten und Aufrufen)
- **10 Negativtests**:
  1. Fehlendes `end` am Dateiende: `to f x\n x + 1`
  2. Fehlendes `end` vor `test`: `to f x\n x\ntest\n 1 is 1\nend`
  3. Legacy-Parameterklammern: `to f [a b]\n a\nend`
  4. Legacy-Funktionsaufruf mit Klammern: `add(1, 2)`
  5. Kommas in Parametern/Aufrufen: `to f a, b` / `do f 1, 2`
  6. Verschachteltes `to` in `to`
  7. Keyword als Parametername: `to f if\n 1\nend`
  8. Zu wenige Argumente (`do add 1` -> `raises` in `.ease`)
  9. Zu viele Argumente (`do add 1 2 3` -> `raises` in `.ease`)
  10. Unbekannte Funktion (`do unbekannt 1` -> `raises` in `.ease`)

## 4. Records (`def`), `with` Copies & Unions (`README.md` L134–L153, L390–L394, L459–L467)
- **Happy Path**: `def ticket = "vip" | "standard"`, `def player ... end`, `player with name = "Aria"`, `hero.name`
- **10 Negativtests**:
  1. `def`-Record ohne `end`
  2. Logik (`to ... end`) innerhalb von `def`
  3. Statement (`say 1`) innerhalb von `def`
  4. Union mit hängendem Pipe: `def u = "a" |`
  5. Mehrzeiliges `with` ohne `end`
  6. `with` ohne `=`: `hero with hp 10`
  7. `with` auf Nicht-Record (`42 with x = 1` -> `raises` in `.ease`)
  8. `with` mit unbekanntem Feld (`hero with mana = 50` -> `raises` in `.ease`)
  9. `with` mit ungültigem Union-Wert (`hero with state = "flying"` -> `raises` in `.ease`)
  10. Fehlendes Feld lesen (`hero.mana` -> `raises` in `.ease`)

## 5. Guards (`check`) & Branching (`if`) (`README.md` L158–L195, L415–L427)
- **Happy Path**: `check ... -> ... end`, `check ... else ... end`, `check cond -> stmt`, `if ... else ... end`
- **10 Negativtests**:
  1. `if` ohne `end`
  2. `if` ohne Bedingung: `if\n say 1\nend`
  3. Verwaistes `else` ohne `if`/`check`
  4. `check`-Block ohne `end`
  5. Einzeiliges `check cond ->` ohne Aktion
  6. Legacy `check:` mit Doppelpunkt
  7. `check` ohne Bedingung vor `->`: `check -> 1`
  8. `else` doppelt im selben `if`
  9. Nicht-wahrheitswertige `if`-Bedingung (`if 42` -> `raises` in `.ease`)
  10. Nicht-wahrheitswertige `check`-Bedingung (`check "yes" -> "ok"` -> `raises` in `.ease`)

## 6. Type Checks (`is`, `is not`) & Logic (`README.md`)
- **Happy Path**: `is`, `is not`, `<`, `<=`, `>`, `>=`, `and`, `or`, `not`, `is text`, `is number`, `is list`
- **10 Negativtests**:
  1. `x is bool` (`bool` ist kein Typ)
  2. `x is not bool`
  3. `x == 1` / `x != 1` / `x is-not 1` (entfernte Legacy-Operatoren)
  4. `x is` ohne rechte Seite
  5. `and (1 > 0)` ohne linke Seite
  6. `not 42` (`raises` in `.ease`)
  7. `1 and (1 > 0)` (`raises` in `.ease`)
  8. `(1 > 0) or 1` (`raises` in `.ease`)
  9. `"a" < 1` (`raises` in `.ease`)
  10. `(1 > 0) < (1 < 0)` (`raises` in `.ease`)

## 7. Strict `as` Contract & Explicit JSON (`decode_json` / `encode_json`) (`README.md`)
- **Happy Path**: `"42" as number`, `42 as text`, `"vip" as ticket`, `(do decode_json '{"id": 7, "name": "Gast", "role": "admin"}') as api_user`, `do encode_json u`
- **10 Negativtests**:
  1. Unbekannter Unicode-Typ: `x as ö` (Parse/TypeError)
  2. Tippfehler: `x as api_usre` (Parse/TypeError mit Vorschlag)
  3. Alias `as string` (Parse/TypeError mit Hinweis auf `text`)
  4. `as bool` / `as boolean` (Parse/TypeError: Boolean ist kein eingebauter Typ)
  5. Konstante / Vorwärtsreferenz nach `as`: `x as pi` / `x as late_user`
  6. Roher Text direkt an `as <def>`: `'{"id": 7, ...}' as api_user` (`raises` in `.ease`)
  7. Fehlendes Pflichtfeld bei `as <def>` (kein Default-Fallback): `Missing field 'api_user.name'` (`raises` in `.ease`)
  8. Record/Liste an `as text` oder `{...}`-Interpolation (`raises` in `.ease`)
  9. Ungültige Zahl / Union / JSON in `decode_json` (`"abc" as number`, `"pending" as ticket`, `do decode_json '{broken'` -> `raises` in `.ease`)
  10. Falsches JSON-Format / Feldtyp (`do decode_json '[1, 2]' as api_user`, `do decode_json '{"id": "abc", ...}' as api_user` -> `raises` in `.ease`)

## 8. Lists, Dot Indexing & Loops (`list`, `.0`, `repeat`, `for`) (`README.md`)
- **Happy Path**: `list 10, 25, 40`, `scores.0`, `list as number`, `list as player with ... end`, `repeat 3`, `for i in do range 1 10`, `for xs do say it end`
- **10 Negativtests**:
  1. Legacy-Listenliteral `[1, 2, 3]`
  2. Legacy-Indexierung `scores[0]`
  3. `list as bool`
  4. `list as unknown_type`
  5. `list as number with x = 1 end`
  6. `list as ticket with x = 1 end`
  7. `repeat` ohne `end`
  8. `for` ohne `end`
  9. `for if in list 1 end` (Keyword als Schleifenvariable)
  10. Laufzeitfehler (`xs.99`, `"hi".0`, `list 10, "hi"`, `do push typed, "bad"`, `repeat -1`, `repeat 1.5`, `for item in 42` -> `raises` in `.ease`)

## 9. Built-in Tests (`test`, `check "label"`) & Imports (`import`) (`README.md` L277–L316, L383, L396–L397)
- **Happy Path**: `test ... end`, `test "label" ... end`, `check "label" ... end`, `import network`, `import screen`
- **10 Negativtests**:
  1. `test` ohne `end`
  2. Verschachteltes `test` in `to`
  3. Verschachteltes `test` in `if`
  4. Legacy `test:` mit Doppelpunkt
  5. `check "label"` ohne `end`
  6. `raises` mit Nicht-String (`1 / 0 raises 42`)
  7. `import nonexistent_mod`
  8. `import "network"` (String statt Ident)
  9. `import network` innerhalb von `to`
  10. `import` ohne Modulnamen (`import\n`)
