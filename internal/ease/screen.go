package ease

import (
	"encoding/json"
	"image"
	"image/color"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// MainThreadDispatch schedules a blocking UI loop (such as ebiten.RunGame)
// on the locked OS main thread when invoked from main.go.
var MainThreadDispatch func(fn func())

type screenCircle struct {
	X     float64
	Y     float64
	Size  float64
	Color string
}

type screenBox struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
	Color  string
}

type screenScene struct {
	Background string
	Circles    []screenCircle
	Boxes      []screenBox
}

type screenFrameState struct {
	Status string `json:"status"`
	Key    string `json:"key"`
	MouseX int    `json:"mouse_x"`
	MouseY int    `json:"mouse_y"`
}

type screenGame struct {
	width   int
	height  int
	sceneCh chan screenScene
	stateCh chan string
	doneCh  chan struct{}
	current screenScene
	mu      sync.Mutex
	closed  bool
}

func (g *screenGame) Update() error {
	if ebiten.IsWindowBeingClosed() {
		g.mu.Lock()
		alreadyClosed := g.closed
		g.closed = true
		g.mu.Unlock()
		if !alreadyClosed {
			// Drain one pending scene if evaluator is waiting, then send closed state.
			select {
			case <-g.sceneCh:
			default:
			}
			mx, my := ebiten.CursorPosition()
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
		// No new scene submitted yet; keep window responsive at 60 FPS.
	}
	return nil
}

func (g *screenGame) Draw(img *ebiten.Image) {
	g.mu.Lock()
	sc := g.current
	g.mu.Unlock()

	img.Fill(parseColor(sc.Background))

	for _, b := range sc.Boxes {
		vector.DrawFilledRect(
			img,
			float32(b.X),
			float32(b.Y),
			float32(b.Width),
			float32(b.Height),
			parseColor(b.Color),
			false,
		)
	}
	for _, c := range sc.Circles {
		vector.DrawFilledCircle(
			img,
			float32(c.X),
			float32(c.Y),
			float32(c.Size),
			parseColor(c.Color),
			true,
		)
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

func startScreenSession(width, height int, title string) *screenSession {
	if width <= 0 {
		width = 800
	}
	if height <= 0 {
		height = 600
	}
	if title == "" {
		title = "Ease Window"
	}
	g := &screenGame{
		width:   width,
		height:  height,
		sceneCh: make(chan screenScene, 1),
		stateCh: make(chan string, 1),
		doneCh:  make(chan struct{}),
		current: screenScene{Background: "black"},
	}
	sess := &screenSession{
		game:   g,
		exited: make(chan struct{}),
	}

	run := func() {
		defer close(sess.exited)
		ebiten.SetWindowSize(width, height)
		ebiten.SetWindowTitle(title)
		ebiten.SetWindowIcon([]image.Image{NewLogoImage(48), NewLogoImage(16)})
		ebiten.SetWindowClosingHandled(true)
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

func (s *screenSession) close() {
	s.closeOnce.Do(func() {
		close(s.game.doneCh)
		select {
		case <-s.exited:
		case <-time.After(2 * time.Second):
		}
	})
}

func parseColor(name string) color.RGBA {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "black":
		return color.RGBA{R: 0, G: 0, B: 0, A: 255}
	case "white":
		return color.RGBA{R: 255, G: 255, B: 255, A: 255}
	case "red":
		return color.RGBA{R: 239, G: 68, B: 68, A: 255}
	case "blue":
		return color.RGBA{R: 59, G: 130, B: 246, A: 255}
	case "amber":
		return color.RGBA{R: 245, G: 158, B: 11, A: 255}
	case "emerald":
		return color.RGBA{R: 16, G: 185, B: 129, A: 255}
	case "slate":
		return color.RGBA{R: 30, G: 41, B: 59, A: 255}
	case "green":
		return color.RGBA{R: 34, G: 197, B: 94, A: 255}
	case "yellow":
		return color.RGBA{R: 234, G: 179, B: 8, A: 255}
	case "cyan":
		return color.RGBA{R: 6, G: 182, B: 212, A: 255}
	case "orange":
		return color.RGBA{R: 249, G: 115, B: 22, A: 255}
	case "purple":
		return color.RGBA{R: 168, G: 85, B: 247, A: 255}
	case "gray", "grey":
		return color.RGBA{R: 107, G: 114, B: 128, A: 255}
	default:
		return color.RGBA{R: 0, G: 0, B: 0, A: 255}
	}
}

func recordToScreenScene(rec *RecordValue) screenScene {
	sc := screenScene{Background: "black"}
	if bg, ok := rec.Fields["background"].(string); ok && bg != "" {
		sc.Background = bg
	}
	if circlesVal, ok := rec.Fields["circles"]; ok {
		if lv, isList := circlesVal.(*ListValue); isList {
			for _, item := range lv.Elements {
				if cr, ok := item.(*RecordValue); ok {
					c := screenCircle{Size: 20, Color: "red"}
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
					sc.Circles = append(sc.Circles, c)
				}
			}
		}
	}
	if boxesVal, ok := rec.Fields["boxes"]; ok {
		if lv, isList := boxesVal.(*ListValue); isList {
			for _, item := range lv.Elements {
				if br, ok := item.(*RecordValue); ok {
					b := screenBox{Width: 40, Height: 40, Color: "blue"}
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
					sc.Boxes = append(sc.Boxes, b)
				}
			}
		}
	}
	return sc
}
