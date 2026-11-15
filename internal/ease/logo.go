package ease

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

type logoRect struct {
	x, y, w, h, r float64
	col           color.NRGBA
}

var logoShapes = []logoRect{
	// Rounded dark-blue background (#0A192F)
	{x: 0, y: 0, w: 128, h: 128, r: 28, col: color.NRGBA{R: 0x0A, G: 0x19, B: 0x2F, A: 0xFF}},
	// Top light-blue bar (#38BDF8)
	{x: 28, y: 30, w: 72, h: 16, r: 8, col: color.NRGBA{R: 0x38, G: 0xBD, B: 0xF8, A: 0xFF}},
	// Middle green bar (#34D399)
	{x: 28, y: 56, w: 50, h: 16, r: 8, col: color.NRGBA{R: 0x34, G: 0xD3, B: 0x99, A: 0xFF}},
	// Bottom light-blue bar (#38BDF8)
	{x: 28, y: 82, w: 72, h: 16, r: 8, col: color.NRGBA{R: 0x38, G: 0xBD, B: 0xF8, A: 0xFF}},
}

// NewLogoImage renders the Ease logo into a square NRGBA image with 4x4 supersampling.
func NewLogoImage(size int) *image.NRGBA {
	if size <= 0 {
		size = 128
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := 128.0 / float64(size)
	const samples = 4
	const invSamplesSq = 1.0 / float64(samples*samples)

	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var rSum, gSum, bSum, aSum float64
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					cx := (float64(px) + (float64(sx)+0.5)/float64(samples)) * scale
					cy := (float64(py) + (float64(sy)+0.5)/float64(samples)) * scale
					c := sampleLogoColor(cx, cy)
					a := float64(c.A) / 255.0
					rSum += float64(c.R) * a
					gSum += float64(c.G) * a
					bSum += float64(c.B) * a
					aSum += a
				}
			}
			aAvg := aSum * invSamplesSq
			if aAvg <= 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(math.Round((rSum * invSamplesSq) / aAvg)),
				G: uint8(math.Round((gSum * invSamplesSq) / aAvg)),
				B: uint8(math.Round((bSum * invSamplesSq) / aAvg)),
				A: uint8(math.Round(aAvg * 255.0)),
			})
		}
	}
	return img
}

// EncodeLogoPNG renders the Ease logo at size x size pixels and encodes it as PNG.
func EncodeLogoPNG(size int) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, NewLogoImage(size)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sampleLogoColor(x, y float64) color.NRGBA {
	bg := logoShapes[0]
	if !inRoundedRect(x, y, bg) {
		return color.NRGBA{}
	}
	for _, bar := range logoShapes[1:] {
		if inRoundedRect(x, y, bar) {
			return bar.col
		}
	}
	return bg.col
}

func inRoundedRect(x, y float64, r logoRect) bool {
	if x < r.x || x > r.x+r.w || y < r.y || y > r.y+r.h {
		return false
	}
	// Check corner circles when outside the inner cross.
	cx := math.Max(r.x+r.r, math.Min(x, r.x+r.w-r.r))
	cy := math.Max(r.y+r.r, math.Min(y, r.y+r.h-r.r))
	dx := x - cx
	dy := y - cy
	return dx*dx+dy*dy <= r.r*r.r
}
