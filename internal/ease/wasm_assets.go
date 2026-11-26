package ease

const wasmIndexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Ease App</title>
  <style>
    body { margin: 0; background: #0f172a; color: #e2e8f0; font-family: system-ui, sans-serif; display: flex; flex-direction: column; align-items: center; padding: 16px; }
    canvas { border: 1px solid #334155; border-radius: 6px; background: #000; max-width: 100%; }
    #ease-debug-panel { display: none; width: 100%; max-width: 820px; margin-top: 12px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; padding: 10px; font-family: monospace; font-size: 12px; white-space: pre-wrap; }
    #ease-error { color: #f87171; margin-top: 8px; font-family: monospace; white-space: pre-wrap; }
  </style>
</head>
<body>
  <canvas id="ease-canvas" width="800" height="600"></canvas>
  <div id="ease-error"></div>
  <div id="ease-debug-panel">
    <strong>Ease Debug (?debug=1)</strong>
    <pre id="ease-frame-state">{}</pre>
    <pre id="ease-console"></pre>
  </div>
  <script src="ease_host.js"></script>
</body>
</html>
`

const wasmHostJS = `(function () {
  "use strict";
  const debugMode = new URLSearchParams(window.location.search).get("debug") === "1";
  const canvas = document.getElementById("ease-canvas");
  const ctx = canvas ? canvas.getContext("2d") : null;
  const errEl = document.getElementById("ease-error");
  const dbgPanel = document.getElementById("ease-debug-panel");
  const frameStateEl = document.getElementById("ease-frame-state");
  const consoleEl = document.getElementById("ease-console");
  if (debugMode && dbgPanel) dbgPanel.style.display = "block";

  const decoder = new TextDecoder("utf-8");
  const encoder = new TextEncoder();
  let wasmMemory = null;
  let wasmExports = null;
  let stopped = false;
  let frameCount = 0;
  let targetFps = 60;
  let lastTickMs = 0;
  const eventQueue = [];
  const heldKeys = new Set();
  let audioCtx = null;
  let activeBGM = null;
  let bgmTimer = null;
  let bgmNodes = [];

  function readStr(ptr, len) {
    if (!wasmMemory || len <= 0) return "";
    return decoder.decode(new Uint8Array(wasmMemory.buffer, ptr >>> 0, len >>> 0));
  }

  function mapKey(k) {
    if (k === "ArrowLeft") return "left";
    if (k === "ArrowRight") return "right";
    if (k === "ArrowUp") return "up";
    if (k === "ArrowDown") return "down";
    if (k === "Escape") return "escape";
    if (k === " ") return "space";
    return k.toLowerCase();
  }

  function ensureAudioCtx() {
    if (audioCtx) return audioCtx;
    const AudioCtx = window.AudioContext || window.webkitAudioContext;
    if (!AudioCtx) return null;
    audioCtx = new AudioCtx();
    return audioCtx;
  }

  function unlockAudio() {
    const actx = ensureAudioCtx();
    if (!actx) return;
    if (actx.state === "suspended") {
      actx.resume().then(() => {
        if (activeBGM && !bgmTimer && !stopped) {
          startBGMLoop(activeBGM);
        }
      }).catch(() => {});
    } else if (activeBGM && !bgmTimer && !stopped) {
      startBGMLoop(activeBGM);
    }
  }

  window.addEventListener("keydown", (e) => {
    if (canvas && (e.key === "ArrowLeft" || e.key === "ArrowRight" || e.key === "ArrowUp" || e.key === "ArrowDown" || e.key === " ")) {
      e.preventDefault();
    }
    const mapped = mapKey(e.key);
    if (mapped) heldKeys.add(mapped);
    eventQueue.push({ kind: 2, key: mapped, x: 0, y: 0 });
    unlockAudio();
  });
  window.addEventListener("keyup", (e) => {
    const mapped = mapKey(e.key);
    if (mapped) heldKeys.delete(mapped);
  });
  window.addEventListener("blur", () => {
    heldKeys.clear();
  });
  window.addEventListener("pointerdown", unlockAudio);
  if (canvas) {
    canvas.addEventListener("mousedown", (e) => {
      unlockAudio();
      const r = canvas.getBoundingClientRect();
      eventQueue.push({ kind: 3, key: "", x: e.clientX - r.left, y: e.clientY - r.top });
    });
    canvas.addEventListener("mousemove", (e) => {
      const r = canvas.getBoundingClientRect();
      eventQueue.push({ kind: 4, key: "", x: e.clientX - r.left, y: e.clientY - r.top });
    });
  }
  window.addEventListener("beforeunload", () => {
    stopBGM();
    eventQueue.push({ kind: 1, key: "", x: 0, y: 0 });
  });

  function resolveColor(c) {
    const table = {
      slate: "#1e293b", midnight: "#0f172a", navy: "#0f172a", amber: "#f59e0b",
      emerald: "#10b981", crimson: "#ef4444", red: "#ef4444", blue: "#3b82f6",
      green: "#22c55e", cyan: "#06b6d4", purple: "#a855f7", violet: "#a855f7",
      silver: "#cbd5e1", gray: "#6b7280", grey: "#6b7280", cream: "#fef3c7",
      indigo: "#312e81", gold: "#f59e0b", orange: "#f97316", yellow: "#eab308",
      black: "#000000", white: "#ffffff"
    };
    return table[(c || "").toLowerCase()] || c || "#ffffff";
  }

  const NOTE_SEMITONES = { C: 0, D: 2, E: 4, F: 5, G: 7, A: 9, B: 11 };

  function parseNoteToken(tok) {
    const slash = tok.indexOf("/");
    if (slash <= 0 || slash >= tok.length - 1) return null;
    const pitch = tok.slice(0, slash);
    const len = parseFloat(tok.slice(slash + 1));
    if (!Number.isFinite(len) || len <= 0) return null;
    if (pitch === "REST") return { rest: true, freq: 0, len };
    if (pitch.length !== 2 && pitch.length !== 3) return null;
    const base = NOTE_SEMITONES[pitch[0]];
    if (base === undefined) return null;
    let acc = 0;
    let octCh = pitch[1];
    if (pitch.length === 3) {
      if (pitch[1] === "#") acc = 1;
      else if (pitch[1] === "b") acc = -1;
      else return null;
      octCh = pitch[2];
    }
    const oct = octCh.charCodeAt(0) - 48;
    if (oct < 0 || oct > 8) return null;
    const midi = (oct + 1) * 12 + base + acc;
    const freq = 440 * Math.pow(2, (midi - 69) / 12);
    return { rest: false, freq, len };
  }

  function scheduleMelody(actx, spec, startTime, trackBgm) {
    const gainVal = Math.max(0, Math.min(100, spec.volume)) / 100 * 0.25;
    let t = startTime;
    const beatSec = 60 / (spec.bpm > 0 ? spec.bpm : 120);
    for (const n of spec.notes) {
      const dur = beatSec * (4 / n.len);
      if (!n.rest && gainVal > 0) {
        const osc = actx.createOscillator();
        const gain = actx.createGain();
        osc.type = spec.wave || "triangle";
        osc.frequency.setValueAtTime(n.freq, t);
        const fade = Math.min(0.008, dur * 0.4);
        gain.gain.setValueAtTime(0.0001, t);
        gain.gain.linearRampToValueAtTime(gainVal, t + fade);
        gain.gain.setValueAtTime(gainVal, Math.max(t + fade, t + dur - fade));
        gain.gain.linearRampToValueAtTime(0.0001, t + dur);
        osc.connect(gain);
        gain.connect(actx.destination);
        osc.start(t);
        osc.stop(t + dur + 0.01);
        if (trackBgm) bgmNodes.push(osc);
      }
      t += dur;
    }
    return t - startTime;
  }

  function stopBGM() {
    activeBGM = null;
    if (bgmTimer !== null) {
      clearTimeout(bgmTimer);
      bgmTimer = null;
    }
    for (const osc of bgmNodes) {
      try { osc.stop(); } catch (_) {}
      try { osc.disconnect(); } catch (_) {}
    }
    bgmNodes = [];
  }

  function startBGMLoop(spec) {
    const actx = ensureAudioCtx();
    if (!actx || actx.state === "suspended" || stopped) return;
    if (bgmTimer !== null) {
      clearTimeout(bgmTimer);
      bgmTimer = null;
    }
    const scheduleCycle = (startAt) => {
      if (activeBGM !== spec || stopped) return;
      bgmNodes = bgmNodes.filter((n) => n.playbackState !== "finished");
      const totalDur = scheduleMelody(actx, spec, startAt, true);
      if (totalDur <= 0) return;
      const delayMs = Math.max(20, (startAt + totalDur - actx.currentTime - 0.05) * 1000);
      bgmTimer = setTimeout(() => scheduleCycle(startAt + totalDur), delayMs);
    };
    scheduleCycle(actx.currentTime + 0.02);
  }

  const env = {
    log(ptr, len) {
      const msg = readStr(ptr, len);
      console.log(msg);
      if (debugMode && consoleEl) consoleEl.textContent += msg + "\n";
    },
    panic(ptr, len) {
      stopped = true;
      const msg = readStr(ptr, len);
      console.error("Ease Panic:", msg);
      if (errEl) errEl.textContent = "RuntimeError: " + msg;
    },
    open_screen(w, h, titlePtr, titleLen, fps) {
      if (canvas) {
        canvas.width = w;
        canvas.height = h;
      }
      document.title = readStr(titlePtr, titleLen) || "Ease App";
      targetFps = (typeof fps === "number" && fps > 0) ? fps : 60;
    },
    present_scene(ptr, len) {
      if (!wasmMemory || !ctx) return;
      const dv = new DataView(wasmMemory.buffer, ptr >>> 0, len >>> 0);
      let off = 0;
      const bgPtr = dv.getUint32(off, true); off += 4;
      const bgLen = dv.getUint32(off, true); off += 4;
      const count = dv.getUint32(off, true); off += 4;
      const bg = resolveColor(readStr(bgPtr, bgLen));
      ctx.fillStyle = bg;
      ctx.fillRect(0, 0, canvas.width, canvas.height);

      const dbgShapes = [];
      for (let i = 0; i < count; i++) {
        const op = dv.getUint8(off); off += 1;
        if (op === 1) { // circle
          const x = dv.getFloat64(off, true); off += 8;
          const y = dv.getFloat64(off, true); off += 8;
          const size = dv.getFloat64(off, true); off += 8;
          const stroke = dv.getFloat64(off, true); off += 8;
          const filled = dv.getUint8(off); off += 1;
          const cp = dv.getUint32(off, true); off += 4;
          const cl = dv.getUint32(off, true); off += 4;
          const col = resolveColor(readStr(cp, cl));
          ctx.beginPath();
          ctx.arc(x, y, size, 0, Math.PI * 2);
          if (filled) { ctx.fillStyle = col; ctx.fill(); }
          else { ctx.strokeStyle = col; ctx.lineWidth = stroke; ctx.stroke(); }
          if (debugMode) dbgShapes.push({ type: "circle", x, y, size, color: col, style: filled ? "solid" : "outline" });
        } else if (op === 2) { // box
          const x = dv.getFloat64(off, true); off += 8;
          const y = dv.getFloat64(off, true); off += 8;
          const w = dv.getFloat64(off, true); off += 8;
          const h = dv.getFloat64(off, true); off += 8;
          const stroke = dv.getFloat64(off, true); off += 8;
          const filled = dv.getUint8(off); off += 1;
          const cp = dv.getUint32(off, true); off += 4;
          const cl = dv.getUint32(off, true); off += 4;
          const col = resolveColor(readStr(cp, cl));
          if (filled) { ctx.fillStyle = col; ctx.fillRect(x, y, w, h); }
          else { ctx.strokeStyle = col; ctx.lineWidth = stroke; ctx.strokeRect(x, y, w, h); }
          if (debugMode) dbgShapes.push({ type: "box", x, y, w, h, color: col, style: filled ? "solid" : "outline" });
        } else if (op === 3) { // line
          const x1 = dv.getFloat64(off, true); off += 8;
          const y1 = dv.getFloat64(off, true); off += 8;
          const x2 = dv.getFloat64(off, true); off += 8;
          const y2 = dv.getFloat64(off, true); off += 8;
          const w = dv.getFloat64(off, true); off += 8;
          const cp = dv.getUint32(off, true); off += 4;
          const cl = dv.getUint32(off, true); off += 4;
          const col = resolveColor(readStr(cp, cl));
          ctx.beginPath(); ctx.moveTo(x1, y1); ctx.lineTo(x2, y2);
          ctx.strokeStyle = col; ctx.lineWidth = w; ctx.stroke();
          if (debugMode) dbgShapes.push({ type: "line", x1, y1, x2, y2, color: col });
        } else if (op === 4) { // triangle
          const x1 = dv.getFloat64(off, true); off += 8;
          const y1 = dv.getFloat64(off, true); off += 8;
          const x2 = dv.getFloat64(off, true); off += 8;
          const y2 = dv.getFloat64(off, true); off += 8;
          const x3 = dv.getFloat64(off, true); off += 8;
          const y3 = dv.getFloat64(off, true); off += 8;
          const stroke = dv.getFloat64(off, true); off += 8;
          const filled = dv.getUint8(off); off += 1;
          const cp = dv.getUint32(off, true); off += 4;
          const cl = dv.getUint32(off, true); off += 4;
          const col = resolveColor(readStr(cp, cl));
          ctx.beginPath(); ctx.moveTo(x1, y1); ctx.lineTo(x2, y2); ctx.lineTo(x3, y3); ctx.closePath();
          if (filled) { ctx.fillStyle = col; ctx.fill(); }
          else { ctx.strokeStyle = col; ctx.lineWidth = stroke; ctx.stroke(); }
          if (debugMode) dbgShapes.push({ type: "triangle", x1, y1, x2, y2, x3, y3, color: col });
        } else if (op === 5) { // label
          const x = dv.getFloat64(off, true); off += 8;
          const y = dv.getFloat64(off, true); off += 8;
          const size = dv.getFloat64(off, true); off += 8;
          const tp = dv.getUint32(off, true); off += 4;
          const tl = dv.getUint32(off, true); off += 4;
          const cp = dv.getUint32(off, true); off += 4;
          const cl = dv.getUint32(off, true); off += 4;
          const text = readStr(tp, tl);
          const col = resolveColor(readStr(cp, cl));
          ctx.fillStyle = col;
          ctx.font = size + "px monospace";
          ctx.textBaseline = "top";
          ctx.fillText(text, x, y);
          if (debugMode) dbgShapes.push({ type: "label", x, y, size, content: text, color: col });
        }
      }
      frameCount++;
      if (debugMode && frameStateEl) {
        frameStateEl.textContent = JSON.stringify({ frame: frameCount, background: bg, shapes: dbgShapes, queuedEvents: eventQueue.length }, null, 2);
      }
    },
    poll_event(outPtr) {
      if (!wasmMemory) return 0;
      const dv = new DataView(wasmMemory.buffer, outPtr >>> 0, 32);
      if (eventQueue.length === 0) {
        dv.setUint8(0, 0);
        return 0;
      }
      const ev = eventQueue.shift();
      dv.setUint8(0, ev.kind);
      dv.setFloat64(8, ev.x || 0, true);
      dv.setFloat64(16, ev.y || 0, true);
      if (ev.key && wasmExports && wasmExports.ease_alloc) {
        const bytes = encoder.encode(ev.key);
        const kPtr = wasmExports.ease_alloc(bytes.length);
        new Uint8Array(wasmMemory.buffer, kPtr >>> 0, bytes.length).set(bytes);
        dv.setUint32(24, kPtr, true);
        dv.setUint32(28, bytes.length, true);
      } else {
        dv.setUint32(24, 0, true);
        dv.setUint32(28, 0, true);
      }
      return 1;
    },
    poll_keys(outPtr, maxKeys) {
      if (!wasmMemory || !wasmExports || !wasmExports.ease_alloc) return 0;
      const dv = new DataView(wasmMemory.buffer);
      let count = 0;
      for (const k of heldKeys) {
        if (count >= maxKeys) break;
        const bytes = encoder.encode(k);
        const kPtr = wasmExports.ease_alloc(bytes.length);
        new Uint8Array(wasmMemory.buffer, kPtr >>> 0, bytes.length).set(bytes);
        const base = (outPtr >>> 0) + count * 8;
        dv.setUint32(base, kPtr, true);
        dv.setUint32(base + 4, bytes.length, true);
        count++;
      }
      return count;
    },
    random_int(low, high) {
      const lo = Number(low);
      const hi = Number(high);
      return BigInt(Math.floor(Math.random() * (hi - lo + 1)) + lo);
    },
    play_melody(notesListPtr, bpm, wavePtr, waveLen, volume, isForever) {
      try {
        if (!wasmMemory || !notesListPtr) return;
        const dv = new DataView(wasmMemory.buffer);
        const dataPtr = dv.getUint32(notesListPtr >>> 0, true);
        const nLen = dv.getUint32((notesListPtr >>> 0) + 4, true);
        const notes = [];
        for (let i = 0; i < nLen; i++) {
          const elOff = (dataPtr >>> 0) + i * 8;
          const strLen = dv.getUint32(elOff, true);
          const strPtr = dv.getUint32(elOff + 4, true);
          const tok = parseNoteToken(readStr(strPtr, strLen));
          if (tok) notes.push(tok);
        }
        if (notes.length === 0) return;
        const wave = readStr(wavePtr, waveLen) || "triangle";
        const spec = { notes, bpm, wave, volume };
        const actx = ensureAudioCtx();
        if (!actx) return;
        if (isForever) {
          stopBGM();
          activeBGM = spec;
          if (actx.state !== "suspended") {
            startBGMLoop(spec);
          }
          return;
        }
        if (actx.state === "suspended") {
          actx.resume().then(() => scheduleMelody(actx, spec, actx.currentTime + 0.01, false)).catch(() => {});
        } else {
          scheduleMelody(actx, spec, actx.currentTime + 0.01, false);
        }
      } catch (_) {}
    },
    stop_melody() {
      stopBGM();
    }
  };

  async function boot() {
    let instance;
    if (WebAssembly.instantiateStreaming) {
      try {
        const res = await WebAssembly.instantiateStreaming(fetch("app.wasm"), { env });
        instance = res.instance;
      } catch (_) {
        const buf = await (await fetch("app.wasm")).arrayBuffer();
        instance = (await WebAssembly.instantiate(buf, { env })).instance;
      }
    } else {
      const buf = await (await fetch("app.wasm")).arrayBuffer();
      instance = (await WebAssembly.instantiate(buf, { env })).instance;
    }
    wasmExports = instance.exports;
    wasmMemory = wasmExports.memory;
    const cont = wasmExports.ease_init();
    if (cont && !stopped) {
      const tick = (nowMs) => {
        if (stopped) return;
        const interval = (1000 / (targetFps > 0 ? targetFps : 60)) - 0.5;
        if (lastTickMs !== 0 && (nowMs - lastTickMs) < interval) {
          requestAnimationFrame(tick);
          return;
        }
        lastTickMs = nowMs;
        if (wasmExports.ease_step()) {
          requestAnimationFrame(tick);
        }
      };
      requestAnimationFrame(tick);
    }
  }
  boot().catch((e) => {
    console.error(e);
    if (errEl) errEl.textContent = e.message;
  });
})();
`
