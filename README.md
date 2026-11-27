<p align="center">
  <img src="assets/logo.svg" alt="Ease Logo" width="96" />
</p>

# Ease

It is five minutes past midnight in the oak-paneled lobby of a hotel at the edge of town. A brass bell sits on the desk, rain taps at the tall panes, and the first handwritten slip of paper just slid under the door.

**Ease** is a compact, race-free language built in Go for moments like that night shift: when data slips have to stay intact, background helpers must never smudge each other's ledgers, and HTTP servers and 2D windows should speak the exact same plain message-passing grammar.

---

## 1. Alone at the Desk

You pick up the slip from the carpet. You never scratch out ink on an original guest note — instead, when a room asks for towels and you stamp it handled, you copy the slip onto a fresh card and leave the original untouched.

```ease
def RequestKind = "towels" | "late_checkout" | "key"
def SlipStatus  = "waiting" | "done" | "escalated"

def guest_note
    room   = 0
    kind   = RequestKind
    status = SlipStatus
end

to stamp_done note
    check note.room <= 0 -> note with status = "escalated"
    -> note with status = "done"
end

to log_line note
    st = note.status as SlipStatus
    check st is "done"      -> "Room {note.room}: fulfilled {note.kind}"
    check st is "escalated" -> "Front desk alert: invalid room number"
    check st is "waiting"   -> "Room {note.room}: still waiting for {note.kind}"
end

slip = guest_note with room = 13, kind = "towels"
handled = stamp_done slip

say log_line slip
say log_line handled
```

Look closely at the counter beside you:
- **`def` describes things; `to` describes verbs.** Records (`guest_note`) and closed unions (`RequestKind`, `SlipStatus`) have no methods inside them, `to` functions take space-separated inputs without outer parentheses or commas, and optional return contracts (`to add a b as number ... end`, `as list of guest_note`) are enforced both statically and at runtime.
- **`with` always makes a fresh copy; `loop ... ->` projects lists.** Both `slip` (still `"waiting"`) and `handled` (`"done"`) stay alive without locking or cloning boilerplate, and declarative list pipelines like `loop users where it.active -> it.name` transform data without imperative `append` loops.
- **No `bool`, no `if`, no `null`.** Choices are explicit words checked with `is`, existential list checks read like English (`any items where it.urgent`, `none tasks where it.status is not "done"`), and in `log_line` the compiler makes sure you never forget a status — or misspell `"done"` as `"doen"`.

---

## 2. When Two Hands Aren't Enough

By 12:20 AM, three guests have dropped notes through the lobby slot at once. Thankfully there is a night porter downstairs, connected to your desk by a pneumatic brass chute: you drop notes into the chute, the porter pulls them out in order down in the cellar, stamps them, and drops the receipts back up in a return tray.

```ease
def guest_note
    room   = 0
    kind   = "towels" | "late_checkout" | "key"
    status = "waiting" | "done"
end

to cellar_porter chute tray
    take chute
        completed = it with status = "done"
        put tray completed
    end
    close tray
end

chute = inbox as guest_note
tray  = inbox as guest_note
spawn cellar_porter chute tray

put chute (guest_note with room = 104, kind = "towels")
put chute (guest_note with room = 208, kind = "key")
close chute

take tray
    say "Receipt #{it.index}: room {it.room} -> {it.status}"
end
```

That pneumatic tube is the whole concurrency model of Ease:
- **`inbox as guest_note`** is a typed tube. Nothing enters unless it matches `guest_note`, and once dropped inside with `put`, the value is frozen so two goroutines can never race over the same record.
- **`spawn` starts any normal `to` function** in the background — no `async`/`await` keywords, no split function ecosystem.
- **`take ... end`** sits at the other end receiving `it` and 1-based `it.index` one slip at a time, then exits the instant `close` shuts the chute and the remaining slips drain out.

---

## 3. When Twenty Bells Ring at Midnight

At 12:30 AM, a tour bus unloads and twenty room service slips hit the blotter together. You don't need twenty separate chutes — you just hand each slip in the stack to a parallel team and gather the bill amounts back in exact room order.

```ease
def guest_note
    room   = 0
    kind   = "towels" | "extra_pillow" | "late_checkout"
    fee    = 0
end

orders = list as guest_note with
    room = 301, kind = "towels"
    room = 302, kind = "late_checkout"
    room = 305, kind = "extra_pillow"
end

charges = swarm loop orders
    check it.kind is "late_checkout" -> 25
    check it.kind is "extra_pillow"  -> 8
    -> 0
end

say "Room fees in arrival order: {charges as json}, total: {sum charges}"
```

`swarm loop` spins up isolated workers over any list or `range`, keeps outer variables strictly read-only, lets any worker return early with `check ... -> <value>`, and guarantees `charges` comes back in `1..N` order so `sum charges` is always deterministic.

---

## 4. When the Outside World Calls

A thunderstorm knocks out the foyer bell, so guests start booking towels and late checkouts straight from their phones over HTTP. Instead of learning a web framework with callbacks and hidden socket structs, Ease hands you another pneumatic chute: an `inbox as network.http_request`.

```ease
import network

def desk_status
    hotel = "Midnight Inn"
    desk  = "open"
end

to handle_call req
    match "{req.method} {req.path}"
        "GET /status"
            network:reply_json req 200 desk_status
        "GET /wake/{room}/{mins}"
            network:reply_text req 200 "Wake-up call set for room {room} in {mins}m"
        else
            network:reply_text req 404 "No one at that extension"
    end
end

to answer_phones requests
    take requests
        spawn handle_call it
    end
end

test "late-night HTTP switchboard handles parallel callers cleanly"
    requests = inbox as network.http_request
    port = network:serve_http 0 requests
    port > 0
    spawn answer_phones requests

    phone = network.network with url = "http://127.0.0.1:{port}"
    calls = list "/status", "/wake/402/15", "/missing"
    replies = swarm loop calls
        -> network:fetch phone it
    end

    replies.1 is (desk_status as json)
    replies.2 is "Wake-up call set for room 402 in 15m"
    replies.3 is ""
    close requests
end
```

A few things happen right on the switchboard:
- **`:` vs `.`:** `network:serve_http` calls a module verb with `:` while `network.http_request` reads a blueprint from the module with `.`. Passing `0` lets the OS assign a free port for the inline `test` block.
- **`match` replaces regexes:** `"GET /wake/{room}/{mins}"` extracts both `{room}` and `{mins}` cleanly into variables on the left side of `->` (and `{name as number}` or `{z as MyUnion}` skips invalid inputs safely).
- **`close requests` tears down the listener:** Closing your inbox closes the underlying TCP socket cleanly with zero dangling handles. (`import network` also ships `network:serve_tcp` and `network:tcp_send` following the same `clients = inbox as network.tcp_client` pattern.)

---

## 5. Giving the Hotel a Window

Before the sun comes up, you paint the Midnight Inn itself — a pointed roof against a dark sky, glowing upstairs windows, a porch lantern swaying gently at 60 frames a second, and a window `events = inbox as screen.event` so closing the pane or pressing `Escape` sends the shift home cleanly.

```ease
import screen

to lantern_x tick
    phase = (tick % 120) - 60
    400 + ((abs phase) - 30)
end

to paint_hotel win
    loop
        lx = lantern_x it.index
        shapes = list as screen.shape
        shapes = append shapes (screen.triangle with x1 = 190, y1 = 220, x2 = 400, y2 = 110, x3 = 610, y3 = 220, color = "indigo")
        shapes = append shapes (screen.box with x = 220, y = 220, width = 360, height = 270, color = "slate")
        shapes = append shapes (screen.box with x = 265, y = 265, width = 70, height = 75, color = "amber")
        shapes = append shapes (screen.box with x = 265, y = 265, width = 70, height = 75, color = "black", style = "outline", stroke = 3)
        shapes = append shapes (screen.line with x1 = 400, y1 = 370, x2 = lx, y2 = 435, width = 3, color = "silver")
        shapes = append shapes (screen.circle with x = lx, y = 435, size = 12, color = "orange")
        shapes = append shapes (screen.label with x = 250, y = 55, content = "MIDNIGHT INN (ESC OR X TO LEAVE)", size = 16, color = "cream")

        sc = screen.scene with background = "midnight", shapes = shapes
        raw = screen:present win sc
        parsed_st = raw as json
        st = parsed_st as screen.frame_state
        check st.status is "closed" -> stop
    end
end

to watch_window win events
    screen:open win events
    spawn paint_hotel win
    take events
        check it.kind is "key" and it.key is "escape"
            close events
        end
        check it.kind is "close" -> stop
    end
    -> "Shift ended"
end

test "window shuts down cleanly on simulated close event"
    win = screen.screen with width = 800, height = 560, title = "Midnight Inn", fps = 60
    events = inbox as screen.event
    watch_window win events is "Shift ended"
end
```

Notice how nothing new had to be invented for graphics:
- **`fps = 60` (or `"unlimited"`):** The painter sets `fps = 60` once on `screen.screen`, then advances the lantern directly with `it.index` — no delta-time math, and `"unlimited"` unlocks VSync/FPS whenever needed.
- **`events = inbox as screen.event`:** Clicking the window close button (`X`) puts `"close"` into `events` and closes it; pressing `Escape` calls `close events`, which closes the OS window on the next tick.
- **Headless `--ci` & `ease test`:** In CI or headless tests, `screen:open` fires deterministic sample events (`"key"`, `"click"`, `"close"`) and closes `events` after 3 frames so graphics programs never hang automated test suites.

---

## Try It in 30 Seconds

Build the CLI with Go (version in [`go.mod`](go.mod)) and run the full animated Midnight Hotel example:

```bash
go build -trimpath -ldflags="-s -w" -o ease ./cmd/ease
./ease test examples/08_haunted_hotel.ease
./ease run examples/08_haunted_hotel.ease
./ease serve examples/10_game_server.ease --watch --debug
./ease build examples/07_http_server.ease -o hotel-api
```

Use `ease serve` for long-running servers with crash-isolated workers, `--watch` live `to`-function hot-code-swapping (keeping in-flight `inbox` queues intact), and an opt-in loopback debug socket (`127.0.0.1:7888`). On Windows PowerShell, build `.\ease.exe .\cmd\ease`. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for Linux graphics library prerequisites (required by the bundled Ebitengine window backend), the full CLI command reference, and local stdlib overrides.

---

## Where to Go Next

- [`llms.txt`](llms.txt) — Complete single-page Ease language specification (ideal for humans & AI coding agents)
- [`examples/`](examples/) — Runnable programs, including [`07_http_server.ease`](examples/07_http_server.ease), [`08_haunted_hotel.ease`](examples/08_haunted_hotel.ease), [`10_game_server.ease`](examples/10_game_server.ease), and [`11_chords_and_melodies.ease`](examples/11_chords_and_melodies.ease)
- [`README_VSCODE.md`](README_VSCODE.md) — VS Code extension, syntax highlighting, and built-in `ease lsp` language server
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — Repository layout, build prerequisites, full CLI & test commands, and language design invariants
- [`LICENSE`](LICENSE) — Project license
