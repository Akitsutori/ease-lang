package ease

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWasmRejectsUnsupportedHostFeatures(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "import_network",
			src: `import network
to main
  say "hi"
end`,
			want: "network",
		},
		{
			name: "import_system",
			src: `import system
to main
  say "hi"
end`,
			want: "system",
		},
		{
			name: "load_file",
			src: `to main
  def data = load "foo.txt" or ""
  print data
end`,
			want: "load",
		},
		{
			name: "save_file",
			src: `to main
  save "foo.txt" "hello"
end`,
			want: "save",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompileProgramToWasm(tc.src, filepath.Join(t.TempDir(), "app.ease"))
			if err == nil {
				t.Fatalf("expected CompileError for %s, got nil", tc.name)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("expected error mentioning %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestWasmBinaryFormatAndExports(t *testing.T) {
	src := `to main
  say "hello wasm"
end`
	wasmBytes, err := CompileProgramToWasm(src, filepath.Join(t.TempDir(), "hello.ease"))
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed: %v", err)
	}
	if len(wasmBytes) < 8 {
		t.Fatalf("wasm output too short: %d bytes", len(wasmBytes))
	}
	wantMagic := []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}
	if !bytes.Equal(wasmBytes[:8], wantMagic) {
		t.Fatalf("invalid wasm magic/version: %x", wasmBytes[:8])
	}
	for _, exp := range []string{"memory", "ease_alloc", "ease_init", "ease_step"} {
		if !bytes.Contains(wasmBytes, []byte(exp)) {
			t.Fatalf("expected export %q in wasm binary", exp)
		}
	}
}

type nodeWasmMelody struct {
	BPM       float64 `json:"bpm"`
	Wave      string  `json:"wave"`
	Volume    float64 `json:"volume"`
	IsForever bool    `json:"isForever"`
	Stopped   bool    `json:"stopped"`
}

type nodeWasmResult struct {
	InitRet  int               `json:"initRet"`
	Steps    []int             `json:"steps"`
	Prints   []string          `json:"prints"`
	Scenes   []json.RawMessage `json:"scenes"`
	Draws    []json.RawMessage `json:"draws"`
	Melodies []nodeWasmMelody  `json:"melodies"`
}

func runWasmInNode(t *testing.T, wasmBytes []byte, eventsJSON string, maxSteps int) nodeWasmResult {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping WebAssembly execution test")
	}

	dir := t.TempDir()
	wasmPath := filepath.Join(dir, "app.wasm")
	if err := os.WriteFile(wasmPath, wasmBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	harness := `
const fs = require("fs");
const wasmPath = process.argv[2];
const events = JSON.parse(process.argv[3]);
const maxSteps = parseInt(process.argv[4], 10);

(async () => {
  const bytes = fs.readFileSync(wasmPath);
  const prints = [];
  const scenes = [];
  const draws = [];
  const melodies = [];
  let memory = null;
  let exports = null;

  const dec = new TextDecoder("utf-8");
  const enc = new TextEncoder();

  function readStr(ptr, len) {
    if (!ptr || len <= 0) return "";
    return dec.decode(new Uint8Array(memory.buffer, ptr, len));
  }

  function decodeScene(ptr, len) {
    const view = new DataView(memory.buffer, ptr >>> 0, len >>> 0);
    let off = 0;
    const bgPtr = view.getUint32(off, true); off += 4;
    const bgLen = view.getUint32(off, true); off += 4;
    const count = view.getUint32(off, true); off += 4;
    const bg = readStr(bgPtr, bgLen);
    const shapes = [];
    for (let i = 0; i < count; i++) {
      const op = view.getUint8(off++);
      if (op === 1) {
        const x = view.getFloat64(off, true); off += 8;
        const y = view.getFloat64(off, true); off += 8;
        const size = view.getFloat64(off, true); off += 8;
        const stroke = view.getFloat64(off, true); off += 8;
        const filled = view.getUint8(off++);
        const cp = view.getUint32(off, true); off += 4;
        const cl = view.getUint32(off, true); off += 4;
        shapes.push({ op: "circle", x, y, size, stroke, filled, color: readStr(cp, cl) });
      } else if (op === 2) {
        const x = view.getFloat64(off, true); off += 8;
        const y = view.getFloat64(off, true); off += 8;
        const w = view.getFloat64(off, true); off += 8;
        const h = view.getFloat64(off, true); off += 8;
        const stroke = view.getFloat64(off, true); off += 8;
        const filled = view.getUint8(off++);
        const cp = view.getUint32(off, true); off += 4;
        const cl = view.getUint32(off, true); off += 4;
        shapes.push({ op: "box", x, y, w, h, stroke, filled, color: readStr(cp, cl) });
      } else if (op === 3) {
        const x1 = view.getFloat64(off, true); off += 8;
        const y1 = view.getFloat64(off, true); off += 8;
        const x2 = view.getFloat64(off, true); off += 8;
        const y2 = view.getFloat64(off, true); off += 8;
        const w = view.getFloat64(off, true); off += 8;
        const cp = view.getUint32(off, true); off += 4;
        const cl = view.getUint32(off, true); off += 4;
        shapes.push({ op: "line", x1, y1, x2, y2, w, color: readStr(cp, cl) });
      } else if (op === 4) {
        const x1 = view.getFloat64(off, true); off += 8;
        const y1 = view.getFloat64(off, true); off += 8;
        const x2 = view.getFloat64(off, true); off += 8;
        const y2 = view.getFloat64(off, true); off += 8;
        const x3 = view.getFloat64(off, true); off += 8;
        const y3 = view.getFloat64(off, true); off += 8;
        const stroke = view.getFloat64(off, true); off += 8;
        const filled = view.getUint8(off++);
        const cp = view.getUint32(off, true); off += 4;
        const cl = view.getUint32(off, true); off += 4;
        shapes.push({ op: "triangle", x1, y1, x2, y2, x3, y3, stroke, filled, color: readStr(cp, cl) });
      } else if (op === 5) {
        const x = view.getFloat64(off, true); off += 8;
        const y = view.getFloat64(off, true); off += 8;
        const size = view.getFloat64(off, true); off += 8;
        const tp = view.getUint32(off, true); off += 4;
        const tl = view.getUint32(off, true); off += 4;
        const cp = view.getUint32(off, true); off += 4;
        const cl = view.getUint32(off, true); off += 4;
        shapes.push({ op: "label", x, y, size, text: readStr(tp, tl), color: readStr(cp, cl) });
      }
    }
    return { bg, count, shapes };
  }

  let evIdx = 0;
  const imports = {
    env: {
      log(ptr, len) {
        prints.push(readStr(ptr, len));
      },
      print_line(ptr, len) {
        prints.push(readStr(ptr, len));
      },
      panic(ptr, len) {
        throw new Error("WASM panic: " + readStr(ptr, len));
      },
      open_screen(w, h, tptr, tlen, fpsMode) {},
      present_scene(ptr, len) {
        scenes.push(decodeScene(ptr, len));
      },
      clear_screen(bgPtr, bgLen) {
        draws.push({ clear: readStr(bgPtr, bgLen) });
      },
      draw_shape(ptr, len) {
        const dv = new DataView(memory.buffer, ptr >>> 0, len >>> 0);
        draws.push({ op: dv.getUint8(0) });
      },
      poll_event(bufPtr) {
        if (evIdx >= events.length) return 0;
        const ev = events[evIdx++];
        const view = new DataView(memory.buffer, bufPtr >>> 0, 32);
        view.setUint8(0, ev.kind | 0);
        view.setFloat64(8, ev.x || 0, true);
        view.setFloat64(16, ev.y || 0, true);
        const keyStr = ev.key || "";
        let kPtr = 0, kLen = 0;
        if (keyStr.length > 0 && exports.ease_alloc) {
          const kb = enc.encode(keyStr);
          kPtr = exports.ease_alloc(kb.length);
          new Uint8Array(memory.buffer, kPtr >>> 0, kb.length).set(kb);
          kLen = kb.length;
        }
        view.setUint32(24, kPtr, true);
        view.setUint32(28, kLen, true);
        return 1;
      },
      poll_keys(outPtr, maxKeys) {
        if (!memory || !exports.ease_alloc) return 0;
        const held = globalThis.__easeHeldKeys || [];
        const dv = new DataView(memory.buffer);
        let count = 0;
        for (const k of held) {
          if (count >= maxKeys) break;
          const kb = enc.encode(k);
          const kPtr = exports.ease_alloc(kb.length);
          new Uint8Array(memory.buffer, kPtr >>> 0, kb.length).set(kb);
          const base = (outPtr >>> 0) + count * 8;
          dv.setUint32(base, kPtr, true);
          dv.setUint32(base + 4, kb.length, true);
          count++;
        }
        return count;
      },
      random_int(min, max) {
        const lo = Number(min), hi = Number(max);
        const val = Math.floor((lo + hi) / 2);
        return typeof min === "bigint" ? BigInt(val) : val;
      },
      play_melody(notesListPtr, bpm, wavePtr, waveLen, volume, isForever) {
        melodies.push({
          bpm,
          wave: readStr(wavePtr, waveLen),
          volume,
          isForever: Boolean(isForever),
        });
      },
      stop_melody() {
        melodies.push({ stopped: true });
      },
      sin(x) { return Math.sin(x); },
      cos(x) { return Math.cos(x); },
      sqrt(x) { return Math.sqrt(x); },
      pow(a, b) { return Math.pow(a, b); }
    }
  };

  const inst = await WebAssembly.instantiate(bytes, imports);
  exports = inst.instance.exports;
  memory = exports.memory;

  const initRet = exports.ease_init ? exports.ease_init() : 0;
  const steps = [];
  if (exports.ease_step) {
    for (let i = 0; i < maxSteps; i++) {
      const s = exports.ease_step();
      steps.push(s);
      if (s === 0) break;
    }
  }
  console.log(JSON.stringify({ initRet, steps, prints, scenes, draws, melodies }));
})().catch(err => {
  console.error(err);
  process.exit(1);
});
`
	harnessPath := filepath.Join(dir, "run.js")
	if err := os.WriteFile(harnessPath, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(nodePath, harnessPath, wasmPath, eventsJSON, string(rune('0'+maxSteps)))
	// Use proper integer formatting for maxSteps:
	cmd = exec.Command(nodePath, harnessPath, wasmPath, eventsJSON, jsonInt(maxSteps))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node execution failed: %v\noutput:\n%s", err, string(out))
	}

	var res nodeWasmResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("failed to parse node JSON output: %v\nraw:\n%s", err, string(out))
	}
	return res
}

func jsonInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestWasmSimpleSay(t *testing.T) {
	src := `say "hello world"`
	wasmBytes, err := CompileProgramToWasm(src, filepath.Join(t.TempDir(), "say.ease"))
	if err != nil {
		t.Fatal(err)
	}
	res := runWasmInNode(t, wasmBytes, "[]", 1)
	if len(res.Prints) != 1 || res.Prints[0] != "hello world" {
		t.Fatalf("got prints=%v (initRet=%d, steps=%v), want [hello world]", res.Prints, res.InitRet, res.Steps)
	}
}

func TestWasmNodeExecutionCore(t *testing.T) {
	src := `import random

def point
  x = 0
  y = 0
end

to fib n
  check n <= 1 -> n
  -> (fib (n - 1)) + (fib (n - 2))
end

to rebind_add n
  res = n + 10
  -> res
end

to main
  p = point with x = 10, y = 20
  p2 = p with x = 42
  nums = list 1, 2, 3, 4, 5
  evens = nums where it % 2 is 0
  r = random:roll 1 10
  v = 5
  words = list "alpha", "beta"
  has_num = "no"
  check contains nums 4
    has_num = "yes"
  end
  has_word = "no"
  check contains words "beta"
    has_word = "yes"
  end
  say "fib={fib 7} rebind={rebind_add v},{v}"
  say "mod={17 % 5} abs={abs -7} floor={floor 3.9} min={min 8 3} max={max 8 3}"
  say "point={p2.x},{p2.y}"
  say "sum={sum evens} roll={r} has={has_num},{has_word}"
end

main`

	wasmBytes, err := CompileProgramToWasm(src, filepath.Join(t.TempDir(), "core.ease"))
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed: %v", err)
	}
	res := runWasmInNode(t, wasmBytes, "[]", 5)
	want := []string{
		"fib=13 rebind=15,5",
		"mod=2 abs=7 floor=3 min=3 max=8",
		"point=42,20",
		"sum=6 roll=5 has=yes,yes",
	}
	if len(res.Prints) != len(want) {
		t.Fatalf("got %d print lines %v, want %v", len(res.Prints), res.Prints, want)
	}
	for i, w := range want {
		if res.Prints[i] != w {
			t.Errorf("line %d: got %q, want %q", i, res.Prints[i], w)
		}
	}
}

func TestWasmGolden06Window(t *testing.T) {
	exPath := filepath.Join("..", "..", "examples", "06_window.ease")
	srcBytes, err := os.ReadFile(exPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", exPath, err)
	}

	outDir := t.TempDir()
	if err := BuildWasmBundleFromFile(string(srcBytes), exPath, outDir); err != nil {
		t.Fatalf("BuildWasmBundleFromFile failed: %v", err)
	}
	for _, fname := range []string{"index.html", "ease_host.js", "app.wasm"} {
		if _, err := os.Stat(filepath.Join(outDir, fname)); err != nil {
			t.Fatalf("expected %s in bundle dir: %v", fname, err)
		}
	}

	wasmBytes, err := os.ReadFile(filepath.Join(outDir, "app.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	// Verify deterministic compilation (second compile must produce identical bytes).
	wasmBytes2, err := CompileProgramToWasm(string(srcBytes), exPath)
	if err != nil {
		t.Fatalf("second CompileProgramToWasm failed: %v", err)
	}
	if !bytes.Equal(wasmBytes, wasmBytes2) {
		t.Fatalf("WASM compilation of 12_pacman.ease is not deterministic (%d vs %d bytes)", len(wasmBytes), len(wasmBytes2))
	}

	// Simulate: key "left" (kind=2), then window close (kind=1 -> status "closed" exits).
	events := `[
		{"kind": 2, "key": "left", "x": 0, "y": 0},
		{"kind": 1, "key": "", "x": 0, "y": 0}
	]`
	res := runWasmInNode(t, wasmBytes, events, 10)
	if len(res.Scenes) < 2 {
		t.Fatalf("expected at least 2 rendered scenes from 06_window.ease, got %d (prints: %v)", len(res.Scenes), res.Prints)
	}
	var firstScene struct {
		Bg     string            `json:"bg"`
		Count  int               `json:"count"`
		Shapes []json.RawMessage `json:"shapes"`
	}
	if err := json.Unmarshal(res.Scenes[0], &firstScene); err != nil {
		t.Fatalf("failed to decode 06_window scene: %v", err)
	}
	if firstScene.Count == 0 || len(firstScene.Shapes) != firstScene.Count {
		t.Fatalf("expected non-empty shapes in 06_window scene, got count=%d shapes=%d", firstScene.Count, len(firstScene.Shapes))
	}
	if len(res.Steps) == 0 || res.Steps[len(res.Steps)-1] != 0 {
		t.Fatalf("expected 06_window.ease to terminate on escape event; steps=%v", res.Steps)
	}
}

func TestWasmGolden08HauntedHotel(t *testing.T) {
	exPath := filepath.Join("..", "..", "examples", "08_haunted_hotel.ease")
	srcBytes, err := os.ReadFile(exPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", exPath, err)
	}

	wasmBytes, err := CompileProgramToWasm(string(srcBytes), exPath)
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed on 08_haunted_hotel.ease: %v", err)
	}

	// Simulate a key event (kind=2) followed by close (kind=1); verify multiple frames & scenes render.
	events := `[
		{"kind": 2, "key": "space", "x": 0, "y": 0},
		{"kind": 1, "key": "", "x": 0, "y": 0}
	]`
	res := runWasmInNode(t, wasmBytes, events, 10)
	if len(res.Scenes) == 0 {
		t.Fatalf("expected at least 1 rendered scene from 08_haunted_hotel.ease, got 0 (prints: %v)", res.Prints)
	}
	var hotelScene struct {
		Bg     string            `json:"bg"`
		Count  int               `json:"count"`
		Shapes []json.RawMessage `json:"shapes"`
	}
	if err := json.Unmarshal(res.Scenes[0], &hotelScene); err != nil {
		t.Fatalf("failed to decode 08_haunted_hotel scene: %v", err)
	}
	if hotelScene.Bg != "midnight" || hotelScene.Count != 16 {
		t.Fatalf("expected midnight bg and 16 display items (14 shapes + 2 labels), got bg=%q count=%d", hotelScene.Bg, hotelScene.Count)
	}
	if len(res.Steps) == 0 || res.Steps[len(res.Steps)-1] != 0 {
		t.Fatalf("expected 08_haunted_hotel.ease to terminate on close event; steps=%v", res.Steps)
	}
}

func TestWasmGolden12Pacman(t *testing.T) {
	exPath := filepath.Join("..", "..", "examples", "12_pacman.ease")
	srcBytes, err := os.ReadFile(exPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", exPath, err)
	}

	outDir := t.TempDir()
	if err := BuildWasmBundleFromFile(string(srcBytes), exPath, outDir); err != nil {
		t.Fatalf("BuildWasmBundleFromFile failed on 12_pacman.ease: %v", err)
	}
	wasmBytes, err := os.ReadFile(filepath.Join(outDir, "app.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	// Verify deterministic compilation (second compile must produce identical bytes).
	wasmBytes2, err := CompileProgramToWasm(string(srcBytes), exPath)
	if err != nil {
		t.Fatalf("second CompileProgramToWasm failed: %v", err)
	}
	if !bytes.Equal(wasmBytes, wasmBytes2) {
		t.Fatalf("WASM compilation of 12_pacman.ease is not deterministic (%d vs %d bytes)", len(wasmBytes), len(wasmBytes2))
	}

	// Simulate steering inputs over 25 frames, then exit via escape.
	events := `[
		{"kind": 2, "key": "right", "x": 0, "y": 0},
		{"kind": 2, "key": "right", "x": 0, "y": 0},
		{"kind": 2, "key": "up", "x": 0, "y": 0},
		{"kind": 2, "key": "left", "x": 0, "y": 0},
		{"kind": 2, "key": "down", "x": 0, "y": 0}
	]`
	res := runWasmInNode(t, wasmBytes, events, 25)
	if len(res.Scenes) != 25 {
		t.Fatalf("expected 25 rendered frames from 12_pacman.ease, got %d", len(res.Scenes))
	}
	var scene struct {
		Bg     string `json:"bg"`
		Count  int    `json:"count"`
		Shapes []struct {
			Op   string `json:"op"`
			Text string `json:"text"`
		} `json:"shapes"`
	}
	if err := json.Unmarshal(res.Scenes[24], &scene); err != nil {
		t.Fatalf("failed to decode 12_pacman scene: %v", err)
	}
	if scene.Bg != "midnight" || scene.Count < 100 {
		t.Fatalf("expected midnight bg and rich Pac-Man scene (>100 items), got bg=%q count=%d", scene.Bg, scene.Count)
	}
	labelCount := 0
	hasTitle := false
	for _, sh := range scene.Shapes {
		if sh.Op == "label" {
			labelCount++
			if sh.Text == "PAC-MAN" {
				hasTitle = true
			}
		}
	}
	if labelCount < 4 || !hasTitle {
		t.Fatalf("expected >=4 0x05 label opcodes including 'PAC-MAN', got labelCount=%d hasTitle=%v", labelCount, hasTitle)
	}
	// Ensure labels are serialized AFTER shapes so HUD text renders on top.
	if scene.Shapes[len(scene.Shapes)-1].Op != "label" {
		t.Fatalf("expected labels to be serialized after shapes, last op was %q", scene.Shapes[len(scene.Shapes)-1].Op)
	}
}

func TestWasmUpdateRecordList(t *testing.T) {
	src := `
def particle
    x = 0
    vx = 5
    state = "active" | "dead"
end

cloud = list as particle with
    x = 10, vx = 5, state = "active"
    x = 20, vx = 3, state = "dead"
end

all_shifted = update cloud with x = it.x + it.index
active_only = update cloud where it.state is "active" with
    x = it.x + it.vx
    state = "dead"
end

say "{cloud.1.x}:{all_shifted.1.x}:{all_shifted.2.x}:{active_only.1.x}:{active_only.1.state}:{active_only.2.x}:{active_only.2.state}"
`
	wasmBytes, err := CompileProgramToWasm(src, "")
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed: %v", err)
	}
	res := runWasmInNode(t, wasmBytes, "[]", 1)
	if len(res.Prints) != 1 || res.Prints[0] != "10:11:22:15:dead:20:dead" {
		t.Fatalf("unexpected WASM update output: %+v", res.Prints)
	}
}

func TestWasmSortLexicographicalAndSortBy(t *testing.T) {
	src := `
def Priority = "urgent" | "routine" | "low"

def task
    name     = ""
    priority = Priority
    score    = 0
end

# 1. Lexicographical text sort (not length sort)
words = sort (list "b", "aa", "a")

# 2. Multi-key sort with named union declaration order, negative numeric tie-breaker, and stability
tasks = list as task with
    name = "Zoe", priority = "low", score = 10
    name = "Bob", priority = "urgent", score = 50
    name = "Ada", priority = "urgent", score = 50
    name = "Max", priority = "routine", score = 30
end

ordered = sort tasks by it.priority, -it.score
say "{words.1},{words.2},{words.3}|{ordered.1.name},{ordered.2.name},{ordered.3.name},{ordered.4.name}"
`
	wasmBytes, err := CompileProgramToWasm(src, "")
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed: %v", err)
	}
	res := runWasmInNode(t, wasmBytes, "[]", 1)
	if len(res.Prints) != 1 || res.Prints[0] != "a,aa,b|Bob,Ada,Max,Zoe" {
		t.Fatalf("unexpected WASM sort output: %+v", res.Prints)
	}
}

func TestWasmSynthPlayStopAndScreenFrameStateKeys(t *testing.T) {
	// 1. Verify embedded wasmHostJS passes `node --check`
	if nodePath, err := exec.LookPath("node"); err == nil {
		hostFile := filepath.Join(t.TempDir(), "ease_host.js")
		if err := os.WriteFile(hostFile, []byte(wasmHostJS), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(nodePath, "--check", hostFile).CombinedOutput(); err != nil {
			t.Fatalf("wasmHostJS syntax check failed: %v\n%s", err, string(out))
		}
	}

	// 2. Compile and run synth:play (forever) + spawn synth:play (sfx) + synth:stop + screen.frame_state.keys
	src := `import screen
import synth

bgm = synth.melody with
    notes  = list "E4/8", "B3/8"
    bpm    = 140
    wave   = "square"
    volume = 20
    repeat = "forever"
end

sfx = synth.melody with
    notes  = list "C5/16"
    bpm    = 180
    wave   = "triangle"
    volume = 75
    repeat = "once"
end

win = screen.screen with width = 320, height = 240, title = "Tetris", fps = 60
synth:play bgm

loop
    sc = screen.scene with background = "#0f172a"
    raw = screen:present win sc
    parsed = raw as json
    st = parsed as screen.frame_state
    check st.status is "closed" -> stop
    spawn synth:play sfx
    has_left = "no"
    check contains st.keys "left"
        has_left = "yes"
    end
    say "{st.status}:{st.key}:{count st.keys}:{has_left}"
    synth:stop
    -> stop
end
`
	wasmBytes, err := CompileProgramToWasm(src, "")
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed: %v", err)
	}
	res := runWasmInNode(t, wasmBytes, `[{"kind":2,"key":"left"}]`, 2)
	if len(res.Prints) != 1 || res.Prints[0] != "open:left:0:no" {
		t.Fatalf("unexpected WASM frame_state output: %+v", res.Prints)
	}
	if len(res.Melodies) != 3 {
		t.Fatalf("expected 3 synth host calls (bgm, sfx, stop), got %+v", res.Melodies)
	}
	if !res.Melodies[0].IsForever || res.Melodies[0].Volume != 20 || res.Melodies[0].Wave != "square" {
		t.Fatalf("unexpected bgm call: %+v", res.Melodies[0])
	}
	if res.Melodies[1].IsForever || res.Melodies[1].Volume != 75 || res.Melodies[1].Wave != "triangle" {
		t.Fatalf("unexpected sfx call: %+v", res.Melodies[1])
	}
	if !res.Melodies[2].Stopped {
		t.Fatalf("expected stop_melody call, got %+v", res.Melodies[2])
	}
}

func TestWasmImmediateScreenClearDrawShowInHelpers(t *testing.T) {
	if nodePath, err := exec.LookPath("node"); err == nil {
		hostFile := filepath.Join(t.TempDir(), "ease_host.js")
		if err := os.WriteFile(hostFile, []byte(wasmHostJS), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(nodePath, "--check", hostFile).CombinedOutput(); err != nil {
			t.Fatalf("wasmHostJS syntax check failed: %v\n%s", err, string(out))
		}
	}

	src := `import screen

to draw_helper win items
    screen:draw win (screen.line with x1 = 0, y1 = 0, x2 = 50, y2 = 50, color = "#334155")
    screen:draw win items
    screen:draw win (screen.label with x = 10, y = 10, content = "HI", size = 14)
end

win = screen.screen with width = 320, height = 240, title = "Immediate", fps = 60
boxes = list as screen.box with
    x = 10, y = 10, width = 20, height = 20, color = "#06b6d4"
    x = 40, y = 10, width = 20, height = 20, color = "#eab308"
end

loop
    screen:clear win "#0f172a"
    draw_helper win boxes
    st = screen:show win
    check st.status is "closed" -> stop
    say "{st.status}:{st.key}"
    -> stop
end
`
	wasmBytes, err := CompileProgramToWasm(src, "")
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed: %v", err)
	}
	res := runWasmInNode(t, wasmBytes, `[{"kind":2,"key":"space"}]`, 2)
	if len(res.Prints) != 1 || res.Prints[0] != "open:space" {
		t.Fatalf("unexpected immediate WASM output: %+v", res.Prints)
	}
	// Expect: clear, line(3), box(2), box(2), label(5) = 5 draw calls
	if len(res.Draws) != 5 {
		t.Fatalf("expected 5 immediate draw calls (clear + line + 2 boxes + label), got %d: %s", len(res.Draws), string(res.Draws[0]))
	}
}

func TestWasmReturnTypeContracts(t *testing.T) {
	src := `
def item
    val = 0
end

to make_item n as item
    -> item with val = n
end
to make_items a b as list of item
    -> list as item with
        val = a
        val = b
    end
end

one = make_item 7
xs = make_items 10 20
say one.val + xs.2.val
`
	wasmBytes, err := CompileProgramToWasm(src, "")
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed: %v", err)
	}
	res := runWasmInNode(t, wasmBytes, `[]`, 1)
	if len(res.Prints) != 1 || res.Prints[0] != "27" {
		t.Fatalf("expected ['27'], got: %+v", res.Prints)
	}
}

func TestWasmDefListConstantInFunc(t *testing.T) {
	src := `
def item
    val = 0
end

def catalog = list as item with
    val = 11
    val = 31
end

to total as number
    -> catalog.1.val + catalog.2.val
end

say total
`
	wasmBytes, err := CompileProgramToWasm(src, "")
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed: %v", err)
	}
	res := runWasmInNode(t, wasmBytes, `[]`, 1)
	if len(res.Prints) != 1 || res.Prints[0] != "42" {
		t.Fatalf("expected ['42'], got: %+v", res.Prints)
	}
}

func TestWasmVoidProcArrowReturn(t *testing.T) {
	src := `
to print_nonzero x
    check x is 0 -> return
    say x
end

to find_first limit
    loop range 1 5
        check it > limit
            say it
            -> return
        end
    end
    say 99
end

loop range 1 4
    check it is 2
        print_nonzero 0
    else
        print_nonzero it
    end
end
find_first 2
`
	wasmBytes, err := CompileProgramToWasm(src, "")
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed: %v", err)
	}
	res := runWasmInNode(t, wasmBytes, `[]`, 1)
	want := []string{"1", "3", "4", "3"}
	if len(res.Prints) != len(want) || res.Prints[0] != want[0] || res.Prints[1] != want[1] || res.Prints[2] != want[2] || res.Prints[3] != want[3] {
		t.Fatalf("expected %v, got: %+v", want, res.Prints)
	}
}

func TestWasmListProjection(t *testing.T) {
	src := `
def item
    val = 0
end

to bump x as item
    -> item with val = x.val + 10
end

xs = list as item with
    val = 1
    val = 5
    val = 2
end

doubled = loop range 1 3 -> it * 2
bumped = loop xs where it.val >= 2 -> bump it
say (sum doubled) + bumped.1.val + bumped.2.val
`
	wasmBytes, err := CompileProgramToWasm(src, "")
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed: %v", err)
	}
	res := runWasmInNode(t, wasmBytes, `[]`, 1)
	if len(res.Prints) != 1 || res.Prints[0] != "39" {
		t.Fatalf("expected ['39'], got: %+v", res.Prints)
	}
}

func TestWasmQuantifiersAnyAndNone(t *testing.T) {
	src := `
nums = list 3, 7, 11
empty = list as number
check any nums where it is 7
    say "any_hit"
end
check none nums where it > 20
    say "none_hit"
end
check any empty where it > 0
    say "bad"
else
    say "empty_any"
end
check none empty where it > 0
    say "empty_none"
end
`
	wasmBytes, err := CompileProgramToWasm(src, "")
	if err != nil {
		t.Fatalf("CompileProgramToWasm failed: %v", err)
	}
	res := runWasmInNode(t, wasmBytes, `[]`, 1)
	want := []string{"any_hit", "none_hit", "empty_any", "empty_none"}
	if len(res.Prints) != len(want) || res.Prints[0] != want[0] || res.Prints[1] != want[1] || res.Prints[2] != want[2] || res.Prints[3] != want[3] {
		t.Fatalf("expected %v, got: %+v", want, res.Prints)
	}
}
