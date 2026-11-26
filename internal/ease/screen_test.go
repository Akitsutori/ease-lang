package ease

import (
	"bytes"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScreenParseColorHexAndNamed(t *testing.T) {
	cases := []struct {
		in   string
		want color.RGBA
	}{
		{"#fff", color.RGBA{R: 255, G: 255, B: 255, A: 255}},
		{"#0f172a", color.RGBA{R: 15, G: 23, B: 42, A: 255}},
		{"#ff00aa80", color.RGBA{R: 255, G: 0, B: 170, A: 128}},
		{"amber", color.RGBA{R: 245, G: 158, B: 11, A: 255}},
		{"midnight", color.RGBA{R: 15, G: 23, B: 42, A: 255}},
	}
	for _, tc := range cases {
		if got := parseColor(tc.in); got != tc.want {
			t.Errorf("parseColor(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestScreenParseConfigFPSModes(t *testing.T) {
	defaultCfg := parseScreenConfig(&RecordValue{Fields: map[string]Value{
		"width":  float64(640),
		"height": float64(360),
		"title":  "Lobby",
		"fps":    float64(0),
	}})
	if defaultCfg.fps != 60 || defaultCfg.unlimitedFPS {
		t.Fatalf("expected default fps=60 finite, got %+v", defaultCfg)
	}

	customCfg := parseScreenConfig(&RecordValue{Fields: map[string]Value{
		"fps": float64(30),
	}})
	if customCfg.fps != 30 || customCfg.unlimitedFPS {
		t.Fatalf("expected fps=30 finite, got %+v", customCfg)
	}

	unlimCfg := parseScreenConfig(&RecordValue{Fields: map[string]Value{
		"fps": "unlimited",
	}})
	if !unlimCfg.unlimitedFPS {
		t.Fatalf("expected unlimitedFPS=true, got %+v", unlimCfg)
	}
}

func TestScreenSceneConversionAllShapesAndPainterOrder(t *testing.T) {
	src := `import screen

c = screen.circle with x = 10, y = 20, size = 15, color = "#ffaa00", style = "outline", stroke = 3
b = screen.box with x = 5, y = 6, width = 80, height = 40, color = "slate", style = "outline", stroke = 4
ln = screen.line with x1 = 1, y1 = 2, x2 = 30, y2 = 40, width = 5, color = "gold"
tr = screen.triangle with x1 = 10, y1 = 10, x2 = 20, y2 = 30, x3 = 30, y3 = 10, color = "crimson", style = "solid"
lb = screen.label with x = 12, y = 14, content = "Room 13", size = 18, color = "cream"

mixed = list as screen.shape
mixed = append mixed c
mixed = append mixed tr
mixed = append mixed lb

sc = screen.scene with
    background = "#0f172a"
    circles    = list c
    boxes      = list b
    lines      = list ln
    triangles  = list tr
    labels     = list lb
    shapes     = mixed
end
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
	scVal, ok := ev.global.Lookup("sc")
	if !ok {
		t.Fatalf("missing 'sc' variable")
	}
	sc := recordToScreenScene(scVal.(*RecordValue))
	if sc.Background != "#0f172a" {
		t.Fatalf("unexpected background %q", sc.Background)
	}
	if len(sc.Circles) != 1 || sc.Circles[0].Filled || sc.Circles[0].Stroke != 3 {
		t.Fatalf("unexpected circles: %+v", sc.Circles)
	}
	if len(sc.Boxes) != 1 || sc.Boxes[0].Filled || sc.Boxes[0].Stroke != 4 {
		t.Fatalf("unexpected boxes: %+v", sc.Boxes)
	}
	if len(sc.Lines) != 1 || sc.Lines[0].Width != 5 || sc.Lines[0].Color != "gold" {
		t.Fatalf("unexpected lines: %+v", sc.Lines)
	}
	if len(sc.Triangles) != 1 || !sc.Triangles[0].Filled || sc.Triangles[0].X2 != 20 {
		t.Fatalf("unexpected triangles: %+v", sc.Triangles)
	}
	if len(sc.Labels) != 1 || sc.Labels[0].Content != "Room 13" || sc.Labels[0].Size != 18 {
		t.Fatalf("unexpected labels: %+v", sc.Labels)
	}
	if len(sc.Shapes) != 3 || sc.Shapes[0].kind != "circle" || sc.Shapes[1].kind != "triangle" || sc.Shapes[2].kind != "label" {
		t.Fatalf("unexpected mixed shapes order: %+v", sc.Shapes)
	}
}

func TestScreenHeadlessEventsInboxAndEarlyClose(t *testing.T) {
	src := `import screen

win = screen.screen with width = 640, height = 480, title = "Event Test", fps = 60
events = inbox as screen.event
screen:open win events

to render_loop win
    loop
        sc = screen.scene with background = "midnight"
        raw = screen:present win sc
        decoded_st = raw as json
        st = decoded_st as screen.frame_state
        check st.status is "closed" -> end
    end
end

spawn render_loop win

seen = list as text
take events
    seen = append seen "{it.kind}:{it.key}:{it.x}:{it.y}"
    check it.kind is "close" -> end
end

say (seen as json)
`
	var out bytes.Buffer
	if _, err := EvalProgramHeadlessWithTrace(src, strings.NewReader(""), &out, nil); err != nil {
		t.Fatalf("EvalProgramHeadlessWithTrace failed: %v", err)
	}
	want := `["key:right:0:0","click:left:320:240","close::0:0"]`
	if strings.TrimSpace(out.String()) != want {
		t.Fatalf("expected events %s, got %q", want, strings.TrimSpace(out.String()))
	}

	// Closing `events` from Ease stops screen:present immediately with status = "closed".
	earlyCloseSrc := `import screen

win = screen.screen with fps = "unlimited"
events = inbox as screen.event
screen:open win events
close events

raw = screen:present win (screen.scene)
decoded_st = raw as json
st = decoded_st as screen.frame_state
say st.status
`
	var out2 bytes.Buffer
	if _, err := EvalProgramHeadlessWithTrace(earlyCloseSrc, strings.NewReader(""), &out2, nil); err != nil {
		t.Fatalf("early close headless failed: %v", err)
	}
	if strings.TrimSpace(out2.String()) != "closed" {
		t.Fatalf("expected 'closed' after close events, got %q", out2.String())
	}
}

func TestScreenLegacyExample06HeadlessStillWorks(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "06_window.ease"))
	if err != nil {
		t.Fatalf("read 06_window.ease failed: %v", err)
	}
	var out bytes.Buffer
	if _, err := EvalProgramHeadlessWithTrace(string(raw), strings.NewReader(""), &out, nil); err != nil {
		t.Fatalf("06_window.ease headless failed: %v", err)
	}
	if !strings.Contains(out.String(), "Fenster geschlossen") {
		t.Fatalf("expected legacy close message, got %q", out.String())
	}
}

func TestScreenFrameStateKeysSerialization(t *testing.T) {
	src := `import screen

win = screen.screen with fps = "unlimited"
events = inbox as screen.event
screen:open win events

raw = screen:present win (screen.scene)
decoded = raw as json
st = decoded as screen.frame_state
say "{st.status}:{st.key}:{count st.keys}:{st.keys.1}"
`
	var out bytes.Buffer
	if _, err := EvalProgramHeadlessWithTrace(src, strings.NewReader(""), &out, nil); err != nil {
		t.Fatalf("headless frame_state keys failed: %v", err)
	}
	if strings.TrimSpace(out.String()) != "open:right:1:right" {
		t.Fatalf("expected 'open:right:1:right', got %q", out.String())
	}
}

func TestScreenImmediateClearDrawShowHeadless(t *testing.T) {
	src := `import screen

win = screen.screen with width = 320, height = 240, fps = 60
boxes = list as screen.box with
    x = 10, y = 20, width = 30, height = 30, color = "#06b6d4"
end

frames = 0
loop
    screen:clear win "#0f172a"
    screen:draw win (screen.line with x1 = 0, y1 = 0, x2 = 100, y2 = 100, color = "#1e293b")
    screen:draw win boxes
    screen:draw win (screen.label with x = 5, y = 5, content = "OK", size = 14)
    st = screen:show win
    frames = frames + 1
    check st.status is "closed" -> end
    say "{frames}:{st.status}:{st.key}:{count st.keys}:{st.mouse_x}"
end
say "done:{frames}"
`
	var out bytes.Buffer
	if _, err := EvalProgramHeadlessWithTrace(src, strings.NewReader(""), &out, nil); err != nil {
		t.Fatalf("headless immediate screen failed: %v", err)
	}
	want := "1:open:right:1:0\n2:open::0:320\ndone:3"
	if strings.TrimSpace(out.String()) != want {
		t.Fatalf("expected %q, got %q", want, strings.TrimSpace(out.String()))
	}
}
