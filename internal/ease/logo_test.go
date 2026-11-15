package ease

import (
	"bytes"
	"image/png"
	"testing"
)

func TestNewLogoImageAndPNG(t *testing.T) {
	img := NewLogoImage(128)
	if img.Bounds().Dx() != 128 || img.Bounds().Dy() != 128 {
		t.Fatalf("expected 128x128 image, got %v", img.Bounds())
	}

	// Outer corner (0,0) should be transparent because of rx=28.
	if c := img.NRGBAAt(0, 0); c.A != 0 {
		t.Fatalf("expected transparent corner at (0,0), got %+v", c)
	}

	// Background pixel should be #0A192F.
	if c := img.NRGBAAt(16, 64); c.R != 0x0A || c.G != 0x19 || c.B != 0x2F || c.A != 0xFF {
		t.Fatalf("expected #0A192F background at (16,64), got %+v", c)
	}

	// Top bar pixel should be #38BDF8.
	if c := img.NRGBAAt(50, 38); c.R != 0x38 || c.G != 0xBD || c.B != 0xF8 || c.A != 0xFF {
		t.Fatalf("expected #38BDF8 top bar at (50,38), got %+v", c)
	}

	// Middle bar pixel should be #34D399.
	if c := img.NRGBAAt(50, 64); c.R != 0x34 || c.G != 0xD3 || c.B != 0x99 || c.A != 0xFF {
		t.Fatalf("expected #34D399 middle bar at (50,64), got %+v", c)
	}

	// PNG round-trip check.
	pngBytes, err := EncodeLogoPNG(128)
	if err != nil {
		t.Fatalf("EncodeLogoPNG error: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png.Decode error: %v", err)
	}
	if decoded.Bounds().Dx() != 128 || decoded.Bounds().Dy() != 128 {
		t.Fatalf("unexpected decoded PNG bounds: %v", decoded.Bounds())
	}
}
