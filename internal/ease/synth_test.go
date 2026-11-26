package ease

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseNoteTokenValidAndInvalid(t *testing.T) {
	a4, err := parseNoteToken("A4/4")
	if err != nil {
		t.Fatalf("parseNoteToken(A4/4) failed: %v", err)
	}
	if a4.IsRest || math.Abs(a4.Freq-440.0) > 1e-9 || a4.Length != 4 {
		t.Fatalf("unexpected A4/4 parse: %+v", a4)
	}

	a3, err := parseNoteToken("A3/8")
	if err != nil {
		t.Fatalf("parseNoteToken(A3/8) failed: %v", err)
	}
	if math.Abs(a3.Freq-220.0) > 1e-9 || a3.Length != 8 {
		t.Fatalf("unexpected A3/8 parse: %+v", a3)
	}

	c4, err := parseNoteToken("C4/1.5")
	if err != nil {
		t.Fatalf("parseNoteToken(C4/1.5) failed: %v", err)
	}
	if math.Abs(c4.Freq-261.6255653005986) > 1e-6 || c4.Length != 1.5 {
		t.Fatalf("unexpected C4/1.5 parse: %+v", c4)
	}

	cSharp4, err := parseNoteToken("C#4/4")
	if err != nil {
		t.Fatalf("parseNoteToken(C#4/4) failed: %v", err)
	}
	dFlat4, err := parseNoteToken("Db4/4")
	if err != nil {
		t.Fatalf("parseNoteToken(Db4/4) failed: %v", err)
	}
	if math.Abs(cSharp4.Freq-dFlat4.Freq) > 1e-9 {
		t.Fatalf("expected enharmonic equivalence C#4 == Db4, got %v vs %v", cSharp4.Freq, dFlat4.Freq)
	}

	if bb3, err := parseNoteToken("Bb3/4"); err != nil || bb3.Freq <= 0 {
		t.Fatalf("expected valid Bb3/4, got (%+v, %v)", bb3, err)
	}
	if c0, err := parseNoteToken("C0/4"); err != nil || c0.Freq <= 0 {
		t.Fatalf("expected valid C0/4, got (%+v, %v)", c0, err)
	}
	if b8, err := parseNoteToken("B8/16"); err != nil || b8.Freq <= 0 {
		t.Fatalf("expected valid B8/16, got (%+v, %v)", b8, err)
	}

	rest, err := parseNoteToken("REST/4")
	if err != nil {
		t.Fatalf("parseNoteToken(REST/4) failed: %v", err)
	}
	if !rest.IsRest || rest.Freq != 0 || rest.Length != 4 {
		t.Fatalf("unexpected REST/4 parse: %+v", rest)
	}

	invalid := []string{
		"",
		"H99/4",
		"C4",
		"C4/0",
		"C4/-2",
		"C9/4",
		"c4/4",
		"C4/abc",
		"REST/0",
		"REST/4/8",
		"E4/4 ",
		"C##4/4",
	}
	for _, tok := range invalid {
		if _, err := parseNoteToken(tok); err == nil {
			t.Fatalf("expected parseNoteToken(%q) to fail", tok)
		}
	}
}

func TestParseMelodyRecordAndDurations(t *testing.T) {
	src := `import synth
mel = synth.melody with
    notes = split "E4/8 G4/8 C5/4 REST/8 B4/8 C5/2" " "
    bpm   = 160
    wave  = "square"
end
empty = synth.melody
`
	prog, err := ParseProgram(src)
	if err != nil {
		t.Fatalf("ParseProgram failed: %v", err)
	}
	ev := NewEvaluator(src, prog, &bytes.Buffer{})
	defer ev.Close()
	if _, err := ev.ExecStatements(prog.Body, ev.global); err != nil {
		t.Fatalf("ExecStatements failed: %v", err)
	}

	melVal, _ := ev.global.Lookup("mel")
	melParsed, err := parseMelodyRecord(melVal, 1, 1, src)
	if err != nil {
		t.Fatalf("parseMelodyRecord(mel) failed: %v", err)
	}
	// At 160 bpm, quarter note = 0.375s; total = (1/8 + 1/8 + 1/4 + 1/8 + 1/8 + 1/2)*4 * 0.375 = 5 * 0.375 = 1.875s.
	if math.Abs(melParsed.TotalSec-1.875) > 1e-9 || melParsed.Wave != "square" || len(melParsed.Notes) != 6 {
		t.Fatalf("unexpected parsed melody: %+v", melParsed)
	}

	emptyVal, _ := ev.global.Lookup("empty")
	emptyParsed, err := parseMelodyRecord(emptyVal, 1, 1, src)
	if err != nil {
		t.Fatalf("parseMelodyRecord(empty) failed: %v", err)
	}
	if emptyParsed.TotalSec != 0 || len(emptyParsed.Notes) != 0 || emptyParsed.Wave != "triangle" || emptyParsed.BPM != 120 {
		t.Fatalf("unexpected default empty melody: %+v", emptyParsed)
	}

	tooLong := &RecordValue{
		TypeName: "melody",
		Fields: map[string]Value{
			"notes": &ListValue{ElemType: "text", Elements: []Value{"A4/0.01"}},
			"bpm":   float64(1),
			"wave":  "sine",
		},
	}
	if _, err := parseMelodyRecord(tooLong, 1, 1, src); err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("expected duration limit error, got: %v", err)
	}
}

func TestRenderMelodyPCMWaveformsAndAntiClickEnvelope(t *testing.T) {
	waves := []string{"triangle", "square", "sine"}
	for _, wave := range waves {
		mel := &synthMelody{
			BPM:    120,
			Wave:   wave,
			Volume: 100,
			Notes: []synthNote{
				{IsRest: false, Freq: 440.0, Length: 8}, // 0.25s = 11025 samples
				{IsRest: true, Freq: 0, Length: 8},      // 0.25s = 11025 samples
				{IsRest: false, Freq: 523.25, Length: 8},
			},
			TotalSec: 0.75,
		}
		pcm := renderMelodyPCM(mel)
		perNoteFrames := 11025
		wantBytes := 3 * perNoteFrames * 4
		if len(pcm) != wantBytes {
			t.Fatalf("wave %s: expected %d PCM bytes, got %d", wave, wantBytes, len(pcm))
		}

		readFrame := func(frameIdx int) (int16, int16) {
			off := frameIdx * 4
			l := int16(binary.LittleEndian.Uint16(pcm[off : off+2]))
			r := int16(binary.LittleEndian.Uint16(pcm[off+2 : off+4]))
			return l, r
		}

		// Note 0 boundary frames must start and end at 0 (8ms fade envelope).
		if l, r := readFrame(0); l != 0 || r != 0 {
			t.Fatalf("wave %s: expected first sample == 0, got L=%d R=%d", wave, l, r)
		}
		if l, r := readFrame(perNoteFrames - 1); l != 0 || r != 0 {
			t.Fatalf("wave %s: expected last sample of note 0 == 0, got L=%d R=%d", wave, l, r)
		}

		// Middle note is REST -> strictly silent.
		for f := perNoteFrames; f < 2*perNoteFrames; f++ {
			if l, r := readFrame(f); l != 0 || r != 0 {
				t.Fatalf("wave %s: expected silence during REST at frame %d, got L=%d R=%d", wave, f, l, r)
			}
		}

		// Note 2 boundaries must start and end at 0, and peak must respect headroom.
		if l, r := readFrame(2 * perNoteFrames); l != 0 || r != 0 {
			t.Fatalf("wave %s: expected first sample of note 2 == 0, got L=%d R=%d", wave, l, r)
		}
		if l, r := readFrame(3*perNoteFrames - 1); l != 0 || r != 0 {
			t.Fatalf("wave %s: expected last sample of note 2 == 0, got L=%d R=%d", wave, l, r)
		}

		maxAllowed := int16(math.Ceil(synthAmplitude * 32767.0))
		peak := int16(0)
		for f := 0; f < perNoteFrames; f++ {
			l, r := readFrame(f)
			if l != r {
				t.Fatalf("wave %s: L/R channel mismatch at frame %d (%d != %d)", wave, f, l, r)
			}
			if l < 0 {
				l = -l
			}
			if l > peak {
				peak = l
			}
		}
		if peak == 0 || peak > maxAllowed {
			t.Fatalf("wave %s: unexpected peak %d (want 1..%d)", wave, peak, maxAllowed)
		}
	}
}

func TestSynthHeadlessEvalChecksAndVoidRules(t *testing.T) {
	src := `import synth

notes = split "E4/8 G4/8 C5/4 REST/8 B4/8 C5/2" " "
mel   = synth.melody with notes = notes, bpm = 160, wave = "square"
synth:play mel
spawn synth:play mel
say "done"

test "headless synth checks"
    synth:play (synth.melody)
    (synth:play mel) is not error
    (synth:play (synth.melody with notes = list "H99/4")) is error
    (synth:play (synth.melody with bpm = 0, notes = list "C4/4")) is error
end
`
	var out bytes.Buffer
	res, err := EvalProgramHeadlessWithTrace(src, strings.NewReader(""), &out, nil)
	if err != nil {
		t.Fatalf("EvalProgramHeadlessWithTrace failed: %v", err)
	}
	if res != "done" {
		t.Fatalf("expected output statement value \"done\", got %#v", res)
	}
	if out.String() != "done\n" {
		t.Fatalf("expected output 'done\\n', got %q", out.String())
	}

	nilResSrc := "import synth\nmel = synth.melody with bpm = 160, notes = list \"E4/8\"\nsynth:play mel\n"
	nilRes, err := EvalProgramHeadlessWithTrace(nilResSrc, strings.NewReader(""), &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatalf("headless standalone synth:play failed: %v", err)
	}
	if nilRes != nil {
		t.Fatalf("expected nil value from standalone synth:play, got %#v", nilRes)
	}

	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Passed != 3 || report.Failed != 0 {
		t.Fatalf("expected 3 passed checks, got %+v", report)
	}

	// Standalone call inside a helper returning nil, plus compile-time rejection of value usage.
	voidCases := []string{
		"import synth\nm = synth.melody with notes = list \"C4/4\"\nx = synth:play m\n",
		"import synth\nm = synth.melody with notes = list \"C4/4\"\nsay synth:play m\n",
		"import synth\nm = synth.melody with notes = list \"C4/4\"\nsay (synth:play m)\n",
		"import synth\nm = synth.melody with notes = list \"C4/4\"\nxs = list (synth:play m)\n",
		"import synth\nm = synth.melody with notes = list \"C4/4\"\ny = (synth:play m) + 1\n",
	}
	for _, bad := range voidCases {
		if _, err := ParseProgram(bad); err == nil || !strings.Contains(err.Error(), "does not return a value") {
			t.Fatalf("expected void TypeError for %q, got: %v", bad, err)
		}
	}

	// Calling synth:play as final statement in a user `to` procedure is allowed cleanly.
	procSrc := `import synth
to chime
    m = synth.melody with notes = list "C5/8", "E5/8"
    synth:play m
end
chime
`
	if _, err := EvalProgramHeadlessWithTrace(procSrc, strings.NewReader(""), &bytes.Buffer{}, nil); err != nil {
		t.Fatalf("expected void procedure wrapping synth:play to pass, got: %v", err)
	}
}

type mockSynthPlayer struct {
	mu      sync.Mutex
	pcm     []byte
	playing bool
	paused  bool
	closed  bool
	played  bool
}

func (p *mockSynthPlayer) Play() {
	p.mu.Lock()
	p.playing = true
	p.played = true
	p.mu.Unlock()
}

func (p *mockSynthPlayer) Pause() {
	p.mu.Lock()
	p.playing = false
	p.paused = true
	p.mu.Unlock()
}

func (p *mockSynthPlayer) IsPlaying() bool {
	p.mu.Lock()
	v := p.playing
	p.mu.Unlock()
	return v
}

func (p *mockSynthPlayer) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

func (p *mockSynthPlayer) setPlaying(v bool) {
	p.mu.Lock()
	p.playing = v
	p.mu.Unlock()
}

func TestSynthSyncPlaybackAndAbortCancellation(t *testing.T) {
	origFactory := newSynthPlayerFn
	defer func() {
		newSynthPlayerFn = origFactory
	}()

	src := `import synth
m = synth.melody with bpm = 120, wave = "sine", notes = list "A4/8"
synth:play m
`
	prog, err := ParseProgram(src)
	if err != nil {
		t.Fatalf("ParseProgram failed: %v", err)
	}

	// Case A: normal synchronous wait until IsPlaying() becomes false.
	var mockA *mockSynthPlayer
	newSynthPlayerFn = func(pcm []byte) (synthPlayer, error) {
		mockA = &mockSynthPlayer{pcm: pcm}
		go func(m *mockSynthPlayer) {
			time.Sleep(35 * time.Millisecond)
			m.setPlaying(false)
		}(mockA)
		return mockA, nil
	}
	evA := NewEvaluator(src, prog, &bytes.Buffer{})
	evA.SetHeadlessAudio(false)
	start := time.Now()
	val, err := evA.ExecStatements(prog.Body, evA.global)
	elapsed := time.Since(start)
	evA.Close()
	if err != nil || val != nil {
		t.Fatalf("expected (nil, nil), got (%#v, %v)", val, err)
	}
	if mockA == nil || !mockA.played || !mockA.closed || mockA.paused || len(mockA.pcm) == 0 {
		t.Fatalf("unexpected mockA state: %+v", mockA)
	}
	if elapsed < 25*time.Millisecond {
		t.Fatalf("expected synchronous wait >= 25ms, finished in %v", elapsed)
	}

	// Case B: rt.abort() while playing stops playback cleanly and pauses player.
	startedCh := make(chan *mockSynthPlayer, 1)
	newSynthPlayerFn = func(pcm []byte) (synthPlayer, error) {
		m := &mockSynthPlayer{pcm: pcm}
		startedCh <- m
		return m, nil
	}
	evB := NewEvaluator(src, prog, &bytes.Buffer{})
	evB.SetHeadlessAudio(false)
	doneCh := make(chan error, 1)
	go func() {
		_, runErr := evB.ExecStatements(prog.Body, evB.global)
		doneCh <- runErr
	}()

	var mockB *mockSynthPlayer
	select {
	case mockB = <-startedCh:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for mock player start")
	}
	evB.rt.abort()
	select {
	case runErr := <-doneCh:
		if runErr != nil {
			t.Fatalf("unexpected error on abort: %v", runErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for synth:play abort cancellation")
	}
	evB.Close()
	if !mockB.paused || !mockB.closed {
		t.Fatalf("expected aborted player to be paused and closed, got %+v", mockB)
	}

	// Case C: audio device failure returns RuntimeError instead of panicking.
	newSynthPlayerFn = func(pcm []byte) (synthPlayer, error) {
		return nil, errors.New("no audio hardware")
	}
	evC := NewEvaluator(src, prog, &bytes.Buffer{})
	evC.SetHeadlessAudio(false)
	_, errC := evC.ExecStatements(prog.Body, evC.global)
	evC.Close()
	if errC == nil || !strings.Contains(errC.Error(), "audio device initialization failed") {
		t.Fatalf("expected audio init RuntimeError, got: %v", errC)
	}
}

func TestSynthVolumeRepeatAndStop(t *testing.T) {
	// 1. Validation of volume (0..100) and repeat ("once" | "forever")
	for _, badSrc := range []string{
		"import synth\nm = synth.melody with\n  notes = list \"A4/4\"\n  volume = -1\nend\nsynth:play m\n",
		"import synth\nm = synth.melody with\n  notes = list \"A4/4\"\n  volume = 101\nend\nsynth:play m\n",
		"import synth\nm = synth.melody with\n  notes = list \"A4/4\"\n  repeat = \"twice\"\nend\nsynth:play m\n",
	} {
		var out bytes.Buffer
		_, err := ParseProgram(badSrc)
		if err == nil {
			_, err = EvalProgramHeadlessWithTrace(badSrc, strings.NewReader(""), &out, nil)
		}
		if err == nil {
			t.Fatalf("expected error for %q, got nil", badSrc)
		}
	}

	// 2. Volume scaling: volume=0 produces all-zero PCM, volume=100 > volume=25
	m0 := &synthMelody{Notes: []synthNote{{Freq: 440, Length: 16}}, BPM: 120, Wave: "square", Volume: 0}
	pcm0 := renderMelodyPCM(m0)
	for i := 0; i < len(pcm0); i++ {
		if pcm0[i] != 0 {
			t.Fatalf("expected zero PCM for volume=0, got %d at byte %d", pcm0[i], i)
		}
	}
	m25 := &synthMelody{Notes: []synthNote{{Freq: 440, Length: 16}}, BPM: 120, Wave: "square", Volume: 25}
	m100 := &synthMelody{Notes: []synthNote{{Freq: 440, Length: 16}}, BPM: 120, Wave: "square", Volume: 100}
	pcm25 := renderMelodyPCM(m25)
	pcm100 := renderMelodyPCM(m100)
	peak := func(b []byte) int16 {
		var mx int16
		for i := 0; i+1 < len(b); i += 2 {
			v := int16(binary.LittleEndian.Uint16(b[i : i+2]))
			if v < 0 {
				v = -v
			}
			if v > mx {
				mx = v
			}
		}
		return mx
	}
	if peak(pcm100) <= peak(pcm25) || peak(pcm25) == 0 {
		t.Fatalf("expected peak(vol=100) > peak(vol=25) > 0, got %d vs %d", peak(pcm100), peak(pcm25))
	}

	// 3. repeat = "forever" returns immediately (non-blocking) and synth:stop cancels it cleanly
	orig := newSynthPlayerFn
	defer func() { newSynthPlayerFn = orig }()

	var mu sync.Mutex
	var players []*mockSynthPlayer
	newSynthPlayerFn = func(pcm []byte) (synthPlayer, error) {
		m := &mockSynthPlayer{pcm: pcm}
		mu.Lock()
		players = append(players, m)
		mu.Unlock()
		return m, nil
	}

	bgmSrc := `import synth
bgm = synth.melody with
    notes  = list "E4/8", "B3/8"
    bpm    = 140
    wave   = "square"
    volume = 20
    repeat = "forever"
end
synth:play bgm
synth:stop
`
	prog, err := ParseProgram(bgmSrc)
	if err != nil {
		t.Fatalf("ParseProgram failed: %v", err)
	}
	ev := NewEvaluator(bgmSrc, prog, &bytes.Buffer{})
	ev.SetHeadlessAudio(false)
	start := time.Now()
	_, err = ev.ExecStatements(prog.Body, ev.global)
	ev.Close()
	if err != nil {
		t.Fatalf("bgm ExecStatements failed: %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("expected non-blocking repeat=\"forever\" + synth:stop, took %v", time.Since(start))
	}
	mu.Lock()
	count := len(players)
	mu.Unlock()
	if count == 0 {
		t.Fatalf("expected at least 1 mock player created for repeat=\"forever\"")
	}

	// 4. Headless --ci mode returns immediately on repeat = "forever"
	var ciOut bytes.Buffer
	if _, err := EvalProgramHeadlessWithTrace(bgmSrc, strings.NewReader(""), &ciOut, nil); err != nil {
		t.Fatalf("headless bgm failed: %v", err)
	}
}
