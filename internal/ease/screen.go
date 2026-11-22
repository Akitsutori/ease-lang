package ease

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"golang.org/x/image/font/basicfont"
)

// MainThreadDispatch schedules a blocking UI loop (such as ebiten.RunGame)
// on the locked OS main thread when invoked from main.go.
var MainThreadDispatch func(fn func())

var defaultLabelFace text.Face = text.NewGoXFace(basicfont.Face7x13)

type screenConfig struct {
	width        int
	height       int
	title        string
	fps          int
	unlimitedFPS bool
}

type screenCircle struct {
	X      float64
	Y      float64
	Size   float64
	Color  string
	Filled bool
	Stroke float64
}

type screenBox struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
	Color  string
	Filled bool
	Stroke float64
}

type screenLine struct {
	X1    float64
	Y1    float64
	X2    float64
	Y2    float64
	Width float64
	Color string
}

type screenTriangle struct {
	X1     float64
	Y1     float64
	X2     float64
	Y2     float64
	X3     float64
	Y3     float64
	Color  string
	Filled bool
	Stroke float64
}

type screenLabel struct {
	X       float64
	Y       float64
	Content string
	Size    float64
	Color   string
}

type screenShape struct {
	kind     string
	circle   screenCircle
	box      screenBox
	line     screenLine
	triangle screenTriangle
	label    screenLabel
}

type screenScene struct {
	Background string
	Circles    []screenCircle
	Boxes      []screenBox
	Lines      []screenLine
	Triangles  []screenTriangle
	Labels     []screenLabel
	Shapes     []screenShape
}

type screenFrameState struct {
	Status string `json:"status"`
	Key    string `json:"key"`
	MouseX int    `json:"mouse_x"`
	MouseY int    `json:"mouse_y"`
}

func newScreenEventRecord(kind, key string, x, y int) *RecordValue {
	return &RecordValue{
		TypeName: "event",
		Fields: map[string]Value{
			"kind": kind,
			"key":  key,
			"x":    float64(x),
			"y":    float64(y),
		},
		Order: []string{"kind", "key", "x", "y"},
	}
}

func parseScreenConfig(winRec *RecordValue) screenConfig {
	cfg := screenConfig{
		width:  800,
		height: 600,
		title:  "Ease Window",
		fps:    60,
	}
	if winRec == nil {
		return cfg
	}
	if v, ok := winRec.Fields["width"].(float64); ok && v > 0 {
		cfg.width = int(v)
	}
	if v, ok := winRec.Fields["height"].(float64); ok && v > 0 {
		cfg.height = int(v)
	}
	if v, ok := winRec.Fields["title"].(string); ok && v != "" {
		cfg.title = v
	}
	switch fv := winRec.Fields["fps"].(type) {
	case string:
		if strings.EqualFold(strings.TrimSpace(fv), "unlimited") {
			cfg.unlimitedFPS = true
		}
	case float64:
		if fv > 0 {
			cfg.fps = int(fv)
		}
	}
	return cfg
}

type screenGame struct {
	width     int
	height    int
	sceneCh   chan screenScene
	stateCh   chan string
	doneCh    chan struct{}
	events    *InboxValue
	current   screenScene
	mu        sync.Mutex
	closed    bool
	hasCursor bool
	lastMX    int
	lastMY    int
}

func (g *screenGame) emitEvent(kind, key string, x, y int) {
	if g.events == nil {
		return
	}
	g.events.TryPut(newScreenEventRecord(kind, key, x, y))
}

func ebitenKeyName(k ebiten.Key) string {
	switch k {
	case ebiten.KeyArrowLeft:
		return "left"
	case ebiten.KeyArrowRight:
		return "right"
	case ebiten.KeyArrowUp:
		return "up"
	case ebiten.KeyArrowDown:
		return "down"
	case ebiten.KeyEscape:
		return "escape"
	case ebiten.KeySpace:
		return "space"
	case ebiten.KeyEnter, ebiten.KeyNumpadEnter:
		return "enter"
	case ebiten.KeyTab:
		return "tab"
	case ebiten.KeyBackspace:
		return "backspace"
	}
	if k >= ebiten.KeyA && k <= ebiten.KeyZ {
		return string(rune('a' + (k - ebiten.KeyA)))
	}
	if k >= ebiten.Key0 && k <= ebiten.Key9 {
		return string(rune('0' + (k - ebiten.Key0)))
	}
	return ""
}

func (g *screenGame) Update() error {
	if ebiten.IsWindowBeingClosed() || (g.events != nil && g.events.IsClosed()) {
		mx, my := ebiten.CursorPosition()
		g.mu.Lock()
		alreadyClosed := g.closed
		g.closed = true
		g.mu.Unlock()
		if !alreadyClosed {
			select {
			case <-g.sceneCh:
			default:
			}
			if g.events != nil {
				g.emitEvent("close", "", mx, my)
				g.events.Close()
			}
			b, _ := json.Marshal(screenFrameState{
				Status: "closed",
				Key:    "",
				MouseX: mx,
				MouseY: my,
			})
			select {
			case g.stateCh <- string(b):
			default:
			}
		}
		return ebiten.Termination
	}

	select {
	case <-g.doneCh:
		g.mu.Lock()
		g.closed = true
		g.mu.Unlock()
		return ebiten.Termination
	default:
	}

	if g.events != nil {
		mx, my := ebiten.CursorPosition()
		for _, k := range inpututil.AppendJustPressedKeys(nil) {
			if name := ebitenKeyName(k); name != "" {
				g.emitEvent("key", name, mx, my)
			}
		}
		if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
			g.emitEvent("click", "left", mx, my)
		}
		if g.hasCursor && (mx != g.lastMX || my != g.lastMY) {
			g.emitEvent("move", "", mx, my)
		}
		g.hasCursor = true
		g.lastMX = mx
		g.lastMY = my
	}

	select {
	case <-g.doneCh:
		g.mu.Lock()
		g.closed = true
		g.mu.Unlock()
		return ebiten.Termination
	case sc, ok := <-g.sceneCh:
		if !ok {
			return ebiten.Termination
		}
		g.mu.Lock()
		g.current = sc
		g.mu.Unlock()

		key := ""
		switch {
		case ebiten.IsKeyPressed(ebiten.KeyArrowRight):
			key = "right"
		case ebiten.IsKeyPressed(ebiten.KeyArrowLeft):
			key = "left"
		case ebiten.IsKeyPressed(ebiten.KeyArrowUp):
			key = "up"
		case ebiten.IsKeyPressed(ebiten.KeyArrowDown):
			key = "down"
		}
		mx, my := ebiten.CursorPosition()
		b, _ := json.Marshal(screenFrameState{
			Status: "open",
			Key:    key,
			MouseX: mx,
			MouseY: my,
		})
		select {
		case g.stateCh <- string(b):
		case <-g.doneCh:
			return ebiten.Termination
		}
	default:
	}
	return nil
}

func drawBox(img *ebiten.Image, b screenBox) {
	clr := parseColor(b.Color)
	if b.Filled {
		vector.FillRect(img, float32(b.X), float32(b.Y), float32(b.Width), float32(b.Height), clr, false)
		return
	}
	sw := float32(b.Stroke)
	if sw <= 0 {
		sw = 2
	}
	vector.StrokeRect(img, float32(b.X), float32(b.Y), float32(b.Width), float32(b.Height), sw, clr, false)
}

func drawCircle(img *ebiten.Image, c screenCircle) {
	clr := parseColor(c.Color)
	if c.Filled {
		vector.FillCircle(img, float32(c.X), float32(c.Y), float32(c.Size), clr, true)
		return
	}
	sw := float32(c.Stroke)
	if sw <= 0 {
		sw = 2
	}
	vector.StrokeCircle(img, float32(c.X), float32(c.Y), float32(c.Size), sw, clr, true)
}

func drawLine(img *ebiten.Image, l screenLine) {
	w := float32(l.Width)
	if w <= 0 {
		w = 2
	}
	vector.StrokeLine(img, float32(l.X1), float32(l.Y1), float32(l.X2), float32(l.Y2), w, parseColor(l.Color), true)
}

func drawTriangle(img *ebiten.Image, tr screenTriangle) {
	var p vector.Path
	p.MoveTo(float32(tr.X1), float32(tr.Y1))
	p.LineTo(float32(tr.X2), float32(tr.Y2))
	p.LineTo(float32(tr.X3), float32(tr.Y3))
	p.Close()

	var dpo vector.DrawPathOptions
	dpo.AntiAlias = true
	dpo.ColorScale.ScaleWithColor(parseColor(tr.Color))

	if tr.Filled {
		vector.FillPath(img, &p, &vector.FillOptions{}, &dpo)
		return
	}
	sw := float32(tr.Stroke)
	if sw <= 0 {
		sw = 2
	}
	vector.StrokePath(img, &p, &vector.StrokeOptions{Width: sw, LineJoin: vector.LineJoinMiter}, &dpo)
}

func drawLabel(img *ebiten.Image, lb screenLabel) {
	if lb.Content == "" {
		return
	}
	op := &text.DrawOptions{}
	if lb.Size > 0 {
		scale := lb.Size / 13.0
		op.GeoM.Scale(scale, scale)
	}
	op.GeoM.Translate(lb.X, lb.Y)
	op.ColorScale.ScaleWithColor(parseColor(lb.Color))
	text.Draw(img, lb.Content, defaultLabelFace, op)
}

func (g *screenGame) Draw(img *ebiten.Image) {
	g.mu.Lock()
	sc := g.current
	g.mu.Unlock()

	img.Fill(parseColor(sc.Background))

	for _, b := range sc.Boxes {
		drawBox(img, b)
	}
	for _, tr := range sc.Triangles {
		drawTriangle(img, tr)
	}
	for _, l := range sc.Lines {
		drawLine(img, l)
	}
	for _, c := range sc.Circles {
		drawCircle(img, c)
	}
	for _, lb := range sc.Labels {
		drawLabel(img, lb)
	}
	for _, sh := range sc.Shapes {
		switch sh.kind {
		case "box":
			drawBox(img, sh.box)
		case "triangle":
			drawTriangle(img, sh.triangle)
		case "line":
			drawLine(img, sh.line)
		case "circle":
			drawCircle(img, sh.circle)
		case "label":
			drawLabel(img, sh.label)
		}
	}
}

func (g *screenGame) Layout(outsideWidth, outsideHeight int) (int, int) {
	return g.width, g.height
}

type screenSession struct {
	game      *screenGame
	exited    chan struct{}
	closeOnce sync.Once
}

func startScreenSession(cfg screenConfig, events *InboxValue) *screenSession {
	g := &screenGame{
		width:   cfg.width,
		height:  cfg.height,
		sceneCh: make(chan screenScene, 1),
		stateCh: make(chan string, 1),
		doneCh:  make(chan struct{}),
		events:  events,
		current: screenScene{Background: "black"},
	}
	sess := &screenSession{
		game:   g,
		exited: make(chan struct{}),
	}
	if events != nil {
		events.AddOnClose(sess.closeAsync)
	}

	run := func() {
		defer close(sess.exited)
		ebiten.SetWindowSize(cfg.width, cfg.height)
		ebiten.SetWindowTitle(cfg.title)
		ebiten.SetWindowIcon([]image.Image{NewLogoImage(48), NewLogoImage(16)})
		ebiten.SetWindowClosingHandled(true)
		if cfg.unlimitedFPS {
			ebiten.SetVsyncEnabled(false)
			ebiten.SetTPS(ebiten.SyncWithFPS)
		} else {
			ebiten.SetVsyncEnabled(true)
			ebiten.SetTPS(cfg.fps)
		}
		_ = ebiten.RunGame(g)
	}

	if MainThreadDispatch != nil {
		MainThreadDispatch(run)
	} else {
		go run()
	}
	return sess
}

func (s *screenSession) present(sc screenScene) string {
	select {
	case <-s.exited:
		b, _ := json.Marshal(screenFrameState{Status: "closed"})
		return string(b)
	case s.game.sceneCh <- sc:
	}
	select {
	case stateJSON := <-s.game.stateCh:
		return stateJSON
	case <-s.exited:
		b, _ := json.Marshal(screenFrameState{Status: "closed"})
		return string(b)
	}
}

func (s *screenSession) closeAsync() {
	s.closeOnce.Do(func() {
		close(s.game.doneCh)
	})
}

func (s *screenSession) close() {
	s.closeAsync()
	select {
	case <-s.exited:
	case <-time.After(2 * time.Second):
	}
}

func parseHexColor(s string) (color.RGBA, bool) {
	hex := strings.TrimPrefix(strings.TrimSpace(s), "#")
	switch len(hex) {
	case 3:
		r, err1 := strconv.ParseUint(strings.Repeat(hex[0:1], 2), 16, 8)
		g, err2 := strconv.ParseUint(strings.Repeat(hex[1:2], 2), 16, 8)
		b, err3 := strconv.ParseUint(strings.Repeat(hex[2:3], 2), 16, 8)
		if err1 == nil && err2 == nil && err3 == nil {
			return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}, true
		}
	case 6:
		v, err := strconv.ParseUint(hex, 16, 32)
		if err == nil {
			return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}, true
		}
	case 8:
		v, err := strconv.ParseUint(hex, 16, 32)
		if err == nil {
			return color.RGBA{R: uint8(v >> 24), G: uint8(v >> 16), B: uint8(v >> 8), A: uint8(v)}, true
		}
	}
	return color.RGBA{}, false
}

func parseColor(name string) color.RGBA {
	trimmed := strings.TrimSpace(name)
	if strings.HasPrefix(trimmed, "#") {
		if c, ok := parseHexColor(trimmed); ok {
			return c
		}
	}
	switch strings.ToLower(trimmed) {
	case "black":
		return color.RGBA{R: 0, G: 0, B: 0, A: 255}
	case "white", "cream":
		if strings.ToLower(trimmed) == "cream" {
			return color.RGBA{R: 254, G: 243, B: 199, A: 255}
		}
		return color.RGBA{R: 255, G: 255, B: 255, A: 255}
	case "red", "crimson":
		return color.RGBA{R: 239, G: 68, B: 68, A: 255}
	case "blue":
		return color.RGBA{R: 59, G: 130, B: 246, A: 255}
	case "amber", "gold":
		return color.RGBA{R: 245, G: 158, B: 11, A: 255}
	case "emerald":
		return color.RGBA{R: 16, G: 185, B: 129, A: 255}
	case "slate":
		return color.RGBA{R: 30, G: 41, B: 59, A: 255}
	case "midnight", "navy":
		return color.RGBA{R: 15, G: 23, B: 42, A: 255}
	case "indigo":
		return color.RGBA{R: 49, G: 46, B: 129, A: 255}
	case "violet", "purple":
		return color.RGBA{R: 168, G: 85, B: 247, A: 255}
	case "green":
		return color.RGBA{R: 34, G: 197, B: 94, A: 255}
	case "yellow":
		return color.RGBA{R: 234, G: 179, B: 8, A: 255}
	case "cyan":
		return color.RGBA{R: 6, G: 182, B: 212, A: 255}
	case "orange":
		return color.RGBA{R: 249, G: 115, B: 22, A: 255}
	case "gray", "grey":
		return color.RGBA{R: 107, G: 114, B: 128, A: 255}
	case "silver":
		return color.RGBA{R: 203, G: 213, B: 225, A: 255}
	default:
		return color.RGBA{R: 0, G: 0, B: 0, A: 255}
	}
}

func isFilledField(fields map[string]Value) bool {
	if s, ok := fields["filled"].(string); ok && strings.EqualFold(strings.TrimSpace(s), "no") {
		return false
	}
	return true
}

func recordToCircle(cr *RecordValue) screenCircle {
	c := screenCircle{Size: 20, Color: "red", Filled: isFilledField(cr.Fields), Stroke: 2}
	if v, ok := cr.Fields["x"].(float64); ok {
		c.X = v
	}
	if v, ok := cr.Fields["y"].(float64); ok {
		c.Y = v
	}
	if v, ok := cr.Fields["size"].(float64); ok {
		c.Size = v
	}
	if v, ok := cr.Fields["color"].(string); ok && v != "" {
		c.Color = v
	}
	if v, ok := cr.Fields["stroke"].(float64); ok && v > 0 {
		c.Stroke = v
	}
	return c
}

func recordToBox(br *RecordValue) screenBox {
	b := screenBox{Width: 40, Height: 40, Color: "blue", Filled: isFilledField(br.Fields), Stroke: 2}
	if v, ok := br.Fields["x"].(float64); ok {
		b.X = v
	}
	if v, ok := br.Fields["y"].(float64); ok {
		b.Y = v
	}
	if v, ok := br.Fields["width"].(float64); ok {
		b.Width = v
	}
	if v, ok := br.Fields["height"].(float64); ok {
		b.Height = v
	}
	if v, ok := br.Fields["color"].(string); ok && v != "" {
		b.Color = v
	}
	if v, ok := br.Fields["stroke"].(float64); ok && v > 0 {
		b.Stroke = v
	}
	return b
}

func recordToLine(lr *RecordValue) screenLine {
	l := screenLine{X2: 100, Y2: 100, Width: 2, Color: "#ffffff"}
	if v, ok := lr.Fields["x1"].(float64); ok {
		l.X1 = v
	}
	if v, ok := lr.Fields["y1"].(float64); ok {
		l.Y1 = v
	}
	if v, ok := lr.Fields["x2"].(float64); ok {
		l.X2 = v
	}
	if v, ok := lr.Fields["y2"].(float64); ok {
		l.Y2 = v
	}
	if v, ok := lr.Fields["width"].(float64); ok && v > 0 {
		l.Width = v
	}
	if v, ok := lr.Fields["color"].(string); ok && v != "" {
		l.Color = v
	}
	return l
}

func recordToTriangle(tr *RecordValue) screenTriangle {
	t := screenTriangle{X2: 50, Y2: 100, X3: 100, Color: "#ffffff", Filled: isFilledField(tr.Fields), Stroke: 2}
	if v, ok := tr.Fields["x1"].(float64); ok {
		t.X1 = v
	}
	if v, ok := tr.Fields["y1"].(float64); ok {
		t.Y1 = v
	}
	if v, ok := tr.Fields["x2"].(float64); ok {
		t.X2 = v
	}
	if v, ok := tr.Fields["y2"].(float64); ok {
		t.Y2 = v
	}
	if v, ok := tr.Fields["x3"].(float64); ok {
		t.X3 = v
	}
	if v, ok := tr.Fields["y3"].(float64); ok {
		t.Y3 = v
	}
	if v, ok := tr.Fields["color"].(string); ok && v != "" {
		t.Color = v
	}
	if v, ok := tr.Fields["stroke"].(float64); ok && v > 0 {
		t.Stroke = v
	}
	return t
}

func recordToLabel(lb *RecordValue) screenLabel {
	out := screenLabel{Size: 16, Color: "#ffffff"}
	if v, ok := lb.Fields["x"].(float64); ok {
		out.X = v
	}
	if v, ok := lb.Fields["y"].(float64); ok {
		out.Y = v
	}
	if v, ok := lb.Fields["content"].(string); ok {
		out.Content = v
	}
	if v, ok := lb.Fields["size"].(float64); ok && v > 0 {
		out.Size = v
	}
	if v, ok := lb.Fields["color"].(string); ok && v != "" {
		out.Color = v
	}
	return out
}

func recordToScreenScene(rec *RecordValue) screenScene {
	sc := screenScene{Background: "black"}
	if rec == nil {
		return sc
	}
	if bg, ok := rec.Fields["background"].(string); ok && bg != "" {
		sc.Background = bg
	}
	if lv, isList := rec.Fields["circles"].(*ListValue); isList {
		for _, item := range lv.Elements {
			if cr, ok := item.(*RecordValue); ok {
				sc.Circles = append(sc.Circles, recordToCircle(cr))
			}
		}
	}
	if lv, isList := rec.Fields["boxes"].(*ListValue); isList {
		for _, item := range lv.Elements {
			if br, ok := item.(*RecordValue); ok {
				sc.Boxes = append(sc.Boxes, recordToBox(br))
			}
		}
	}
	if lv, isList := rec.Fields["lines"].(*ListValue); isList {
		for _, item := range lv.Elements {
			if lr, ok := item.(*RecordValue); ok {
				sc.Lines = append(sc.Lines, recordToLine(lr))
			}
		}
	}
	if lv, isList := rec.Fields["triangles"].(*ListValue); isList {
		for _, item := range lv.Elements {
			if tr, ok := item.(*RecordValue); ok {
				sc.Triangles = append(sc.Triangles, recordToTriangle(tr))
			}
		}
	}
	if lv, isList := rec.Fields["labels"].(*ListValue); isList {
		for _, item := range lv.Elements {
			if lb, ok := item.(*RecordValue); ok {
				sc.Labels = append(sc.Labels, recordToLabel(lb))
			}
		}
	}
	if lv, isList := rec.Fields["shapes"].(*ListValue); isList {
		for _, item := range lv.Elements {
			sr, ok := item.(*RecordValue)
			if !ok {
				continue
			}
			switch sr.TypeName {
			case "circle":
				sc.Shapes = append(sc.Shapes, screenShape{kind: "circle", circle: recordToCircle(sr)})
			case "box":
				sc.Shapes = append(sc.Shapes, screenShape{kind: "box", box: recordToBox(sr)})
			case "line":
				sc.Shapes = append(sc.Shapes, screenShape{kind: "line", line: recordToLine(sr)})
			case "triangle":
				sc.Shapes = append(sc.Shapes, screenShape{kind: "triangle", triangle: recordToTriangle(sr)})
			case "label":
				sc.Shapes = append(sc.Shapes, screenShape{kind: "label", label: recordToLabel(sr)})
			}
		}
	}
	return sc
}

func (ev *Evaluator) execScreenOpen(args []Value, line, col int) (Value, error) {
	winRec, ok := args[0].(*RecordValue)
	if !ok {
		return nil, newError("RuntimeError", line, col, ev.source,
			fmt.Sprintf("screen_open requires a screen record as first argument, got %s", TypeName(args[0])), "")
	}
	events, ok := args[1].(*InboxValue)
	if !ok {
		return nil, newError("TypeError", line, col, ev.source,
			fmt.Sprintf("screen_open requires an inbox as second argument, got %s", TypeName(args[1])),
			"Create an inbox with 'events = inbox as screen.event' first.")
	}

	cfg := parseScreenConfig(winRec)

	ev.rt.screenMu.Lock()
	oldSess := ev.rt.activeScreen
	ev.rt.activeScreen = nil
	ev.rt.screenEvents = events
	ev.rt.screenFrameCount = 0
	ev.rt.screenMu.Unlock()

	if oldSess != nil {
		oldSess.close()
	}

	if events.IsClosed() || ev.rt.isAborted() {
		return nil, nil
	}

	if ev.headlessScreen {
		return nil, nil
	}

	sess := startScreenSession(cfg, events)
	ev.rt.screenMu.Lock()
	ev.rt.activeScreen = sess
	ev.rt.screenMu.Unlock()
	return nil, nil
}

func (ev *Evaluator) execScreenPresent(args []Value, line, col int) (Value, error) {
	winRec, ok1 := args[0].(*RecordValue)
	sceneRec, ok2 := args[1].(*RecordValue)
	if !ok1 || !ok2 {
		return nil, newError("RuntimeError", line, col, ev.source, "screen_present requires (screen, scene) records", "")
	}

	if ev.headlessScreen {
		ev.rt.screenMu.Lock()
		ev.rt.screenFrameCount++
		frame := ev.rt.screenFrameCount
		events := ev.rt.screenEvents
		ev.rt.screenMu.Unlock()

		if events == nil {
			// Legacy headless simulation for scenes without an event inbox.
			st := screenFrameState{Status: "open"}
			if frame == 1 {
				st.Key = "right"
			} else {
				st.Status = "closed"
			}
			b, _ := json.Marshal(st)
			return string(b), nil
		}

		if events.IsClosed() || ev.rt.isAborted() {
			b, _ := json.Marshal(screenFrameState{Status: "closed"})
			return string(b), nil
		}

		st := screenFrameState{Status: "open"}
		switch frame {
		case 1:
			st.Key = "right"
			events.TryPut(newScreenEventRecord("key", "right", 0, 0))
		case 2:
			st.MouseX = 320
			st.MouseY = 240
			events.TryPut(newScreenEventRecord("click", "left", 320, 240))
		default:
			st.Status = "closed"
			events.TryPut(newScreenEventRecord("close", "", 0, 0))
			events.Close()
		}
		b, _ := json.Marshal(st)
		return string(b), nil
	}

	cfg := parseScreenConfig(winRec)
	ev.rt.screenMu.Lock()
	sess := ev.rt.activeScreen
	if sess == nil {
		sess = startScreenSession(cfg, ev.rt.screenEvents)
		ev.rt.activeScreen = sess
	}
	ev.rt.screenMu.Unlock()

	return sess.present(recordToScreenScene(sceneRec)), nil
}
