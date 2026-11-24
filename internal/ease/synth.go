package ease

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
)

const (
	synthSampleRate     = 44100
	synthChannelCount   = 2
	synthAmplitude      = 0.25
	synthFadeSec        = 0.008
	maxSynthDurationSec = 300.0
)

type synthNote struct {
	IsRest bool
	Freq   float64
	Length float64
}

type synthMelody struct {
	Notes    []synthNote
	BPM      float64
	Wave     string
	TotalSec float64
}

type synthPlayer interface {
	Play()
	Pause()
	IsPlaying() bool
	Close() error
}

var _ synthPlayer = (*oto.Player)(nil)

var newSynthPlayerFn = newOtoSynthPlayer

var (
	synthCtxOnce sync.Once
	synthCtx     *oto.Context
	synthCtxErr  error
)

func getOtoContext() (*oto.Context, error) {
	synthCtxOnce.Do(func() {
		ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
			SampleRate:   synthSampleRate,
			ChannelCount: synthChannelCount,
			Format:       oto.FormatSignedInt16LE,
		})
		if err != nil {
			synthCtxErr = err
			return
		}
		if ready != nil {
			<-ready
		}
		synthCtx = ctx
	})
	return synthCtx, synthCtxErr
}

func newOtoSynthPlayer(pcm []byte) (synthPlayer, error) {
	ctx, err := getOtoContext()
	if err != nil {
		return nil, err
	}
	return ctx.NewPlayer(bytes.NewReader(pcm)), nil
}

func noteBaseSemitone(letter byte) (int, bool) {
	switch letter {
	case 'C':
		return 0, true
	case 'D':
		return 2, true
	case 'E':
		return 4, true
	case 'F':
		return 5, true
	case 'G':
		return 7, true
	case 'A':
		return 9, true
	case 'B':
		return 11, true
	default:
		return 0, false
	}
}

func parseNoteToken(tok string) (synthNote, error) {
	if tok == "" {
		return synthNote{}, errors.New("note token cannot be empty")
	}
	if strings.ContainsAny(tok, " \t\r\n") {
		return synthNote{}, errors.New("note token cannot contain whitespace")
	}
	slash := strings.IndexByte(tok, '/')
	if slash <= 0 || slash >= len(tok)-1 || strings.IndexByte(tok[slash+1:], '/') >= 0 {
		return synthNote{}, errors.New("expected '<pitch>/<length>' format")
	}

	pitch := tok[:slash]
	lenStr := tok[slash+1:]

	noteLen, ok := parseStrictEaseNumber(lenStr)
	if !ok || math.IsNaN(noteLen) || math.IsInf(noteLen, 0) || noteLen <= 0 {
		return synthNote{}, fmt.Errorf("invalid note length %q (must be > 0)", lenStr)
	}

	if pitch == "REST" {
		return synthNote{IsRest: true, Length: noteLen}, nil
	}

	if len(pitch) != 2 && len(pitch) != 3 {
		return synthNote{}, fmt.Errorf("invalid pitch %q (expected note A-G, optional #/b, and octave 0-8)", pitch)
	}

	baseSemitone, ok := noteBaseSemitone(pitch[0])
	if !ok {
		return synthNote{}, fmt.Errorf("invalid pitch letter %q (expected A-G or REST)", pitch[0])
	}

	accidental := 0
	octByte := pitch[1]
	if len(pitch) == 3 {
		switch pitch[1] {
		case '#':
			accidental = 1
		case 'b':
			accidental = -1
		default:
			return synthNote{}, fmt.Errorf("invalid accidental %q in %q (expected # or b)", pitch[1], pitch)
		}
		octByte = pitch[2]
	}

	if octByte < '0' || octByte > '8' {
		return synthNote{}, fmt.Errorf("invalid octave %q in %q (expected 0-8)", octByte, pitch)
	}

	octave := int(octByte - '0')
	midi := (octave+1)*12 + baseSemitone + accidental
	freq := 440.0 * math.Pow(2.0, float64(midi-69)/12.0)
	return synthNote{
		IsRest: false,
		Freq:   freq,
		Length: noteLen,
	}, nil
}

func parseMelodyRecord(val Value, line, col int, src string) (*synthMelody, error) {
	rec, ok := val.(*RecordValue)
	if !ok || rec.TypeName != "melody" {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			src,
			fmt.Sprintf("synth:play requires a synth.melody record, got %s", TypeName(val)),
			"Build one with 'synth.melody with notes = ..., bpm = 120, wave = \"square\"'.",
		)
	}

	bpmVal, ok := rec.Fields["bpm"].(float64)
	if !ok || math.IsNaN(bpmVal) || math.IsInf(bpmVal, 0) || bpmVal <= 0 {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			src,
			fmt.Sprintf("synth:play requires bpm > 0, got %v", rec.Fields["bpm"]),
			"Set 'bpm' to a positive number such as 120 or 160.",
		)
	}

	waveVal, ok := rec.Fields["wave"].(string)
	if !ok || (waveVal != "triangle" && waveVal != "square" && waveVal != "sine") {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			src,
			fmt.Sprintf("synth:play requires wave \"triangle\", \"square\", or \"sine\", got %v", rec.Fields["wave"]),
			"Set 'wave' to \"triangle\", \"square\", or \"sine\".",
		)
	}

	notesList, ok := rec.Fields["notes"].(*ListValue)
	if !ok {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			src,
			fmt.Sprintf("synth:play requires melody.notes to be a list as text, got %s", TypeName(rec.Fields["notes"])),
			"Provide 'notes = split \"C4/4 E4/4 G4/4\" \" \"' on synth.melody.",
		)
	}

	notes := make([]synthNote, len(notesList.Elements))
	var totalSec float64
	for i, elem := range notesList.Elements {
		tok, ok := elem.(string)
		if !ok {
			return nil, newError(
				"RuntimeError",
				line,
				col,
				src,
				fmt.Sprintf("synth:play requires text note tokens, got %s at index %d", TypeName(elem), i+1),
				"Use note tokens like 'E4/8', 'C#5/4', 'Bb3/4', or 'REST/8'.",
			)
		}
		note, err := parseNoteToken(tok)
		if err != nil {
			return nil, newError(
				"RuntimeError",
				line,
				col,
				src,
				fmt.Sprintf("synth:play invalid note token %q at index %d: %v", tok, i+1, err),
				"Use '<pitch>/<length>' tokens like 'E4/8', 'F#4/4', 'Bb3/8', or 'REST/8' with length > 0.",
			)
		}
		notes[i] = note
		totalSec += (60.0 / bpmVal) * (4.0 / note.Length)
		if totalSec > maxSynthDurationSec {
			return nil, newError(
				"RuntimeError",
				line,
				col,
				src,
				fmt.Sprintf("synth:play melody duration %.1fs exceeds maximum of %.0fs", totalSec, maxSynthDurationSec),
				"Split very long sequences into shorter melodies.",
			)
		}
	}

	return &synthMelody{
		Notes:    notes,
		BPM:      bpmVal,
		Wave:     waveVal,
		TotalSec: totalSec,
	}, nil
}

func sampleWaveform(wave string, phi float64) float64 {
	switch wave {
	case "sine":
		return math.Sin(2.0 * math.Pi * phi)
	case "square":
		if phi < 0.5 {
			return 1.0
		}
		return -1.0
	default: // "triangle"
		switch {
		case phi < 0.25:
			return 4.0 * phi
		case phi < 0.75:
			return 2.0 - 4.0*phi
		default:
			return 4.0*phi - 4.0
		}
	}
}

func renderMelodyPCM(m *synthMelody) []byte {
	if m == nil || len(m.Notes) == 0 {
		return nil
	}
	noteSampleCounts := make([]int, len(m.Notes))
	totalSamples := 0
	for i, n := range m.Notes {
		noteSec := (60.0 / m.BPM) * (4.0 / n.Length)
		count := int(math.Round(noteSec * float64(synthSampleRate)))
		if count < 1 {
			count = 1
		}
		noteSampleCounts[i] = count
		totalSamples += count
	}

	pcm := make([]byte, totalSamples*synthChannelCount*2)
	maxFade := int(math.Round(synthFadeSec * float64(synthSampleRate)))
	cursor := 0

	for idx, n := range m.Notes {
		count := noteSampleCounts[idx]
		if n.IsRest {
			cursor += count * synthChannelCount * 2
			continue
		}
		fade := maxFade
		if half := count / 2; fade > half {
			fade = half
		}

		for i := 0; i < count; i++ {
			phi := math.Mod(float64(i)*n.Freq/float64(synthSampleRate), 1.0)
			w := sampleWaveform(m.Wave, phi)

			env := 1.0
			if fade > 0 {
				if i < fade {
					att := float64(i) / float64(fade)
					if att < env {
						env = att
					}
				}
				rem := count - 1 - i
				if rem < fade {
					rel := float64(rem) / float64(fade)
					if rel < env {
						env = rel
					}
				}
			}

			sample := int16(math.Round(w * env * synthAmplitude * 32767.0))
			u := uint16(sample)
			binary.LittleEndian.PutUint16(pcm[cursor:cursor+2], u)
			binary.LittleEndian.PutUint16(pcm[cursor+2:cursor+4], u)
			cursor += 4
		}
	}

	return pcm
}

func (ev *Evaluator) execSynthPlay(args []Value, line, col int) (Value, error) {
	if len(args) != 1 {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			"synth_play takes 1 argument (melody)",
			"Pass a synth.melody record to synth:play.",
		)
	}

	melody, err := parseMelodyRecord(args[0], line, col, ev.source)
	if err != nil {
		return nil, err
	}
	if len(melody.Notes) == 0 {
		return nil, nil
	}

	pcm := renderMelodyPCM(melody)

	if ev.headlessAudio || ev.rt.isAborted() {
		return nil, nil
	}

	player, err := newSynthPlayerFn(pcm)
	if err != nil {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("synth:play audio device initialization failed: %v", err),
			"Check audio output device availability or run headless with '--ci'.",
		)
	}
	defer func() {
		_ = player.Close()
		runtime.KeepAlive(player)
	}()

	player.Play()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	ctx := ev.ctx()

	for {
		if !player.IsPlaying() {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			player.Pause()
			return nil, nil
		case <-ticker.C:
		}
	}
}
