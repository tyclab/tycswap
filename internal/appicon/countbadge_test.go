package appicon

import (
	"bytes"
	"image/png"
	"testing"
)

func TestCountLabel(t *testing.T) {
	for n, want := range map[int]string{-3: "1", 0: "1", 1: "1", 7: "7", 9: "9", 10: "9+", 42: "9+"} {
		if got := countLabel(n); got != want {
			t.Errorf("countLabel(%d) = %q, want %q", n, got, want)
		}
	}
	for r := range "123456789+" {
		if len(glyph(rune("123456789+"[r]))) == 0 {
			t.Errorf("no glyph for %q", "123456789+"[r])
		}
	}
}

// The count sits beside the mark: the image is wider than tall, the square
// keeps every eye pixel, and the badge is the accent with a white number.
func TestCountedBadgeBesideTheMark(t *testing.T) {
	p := Colors()
	for _, h := range []int{18, 36, 128} {
		plain := Draw(h, false)
		one, many := DrawCounted(h, 3), DrawCounted(h, 12)
		if one.Bounds().Dy() != h || one.Bounds().Dx() <= h || many.Bounds().Dx() <= one.Bounds().Dx() {
			t.Fatalf("height %d: bounds %v and %v (the 9+ pill must be wider)", h, one.Bounds(), many.Bounds())
		}
		for y := 0; y < h; y++ {
			for x := 0; x < h; x++ {
				if plain.NRGBAAt(x, y) != one.NRGBAAt(x, y) && eyeCovered(h, x, y) {
					t.Fatalf("height %d: the badge covers an eye at (%d,%d)", h, x, y)
				}
			}
		}
		var accent, digit int
		for y := 0; y < h; y++ {
			for x := h; x < one.Bounds().Dx(); x++ {
				switch one.NRGBAAt(x, y) {
				case p.Badge:
					accent++
				case white:
					digit++ // the ring and the number
				}
			}
		}
		if accent == 0 || digit == 0 {
			t.Errorf("height %d: %d accent and %d white pixels beside the mark", h, accent, digit)
		}
	}
	if bytes.Equal(PNGCounted(36, 1), PNGCounted(36, 2)) {
		t.Error("1 and 2 are drawn alike")
	}
	if img, err := png.Decode(bytes.NewReader(PNGCounted(128, 4))); err != nil || img.Bounds().Dy() != 128 {
		t.Errorf("PNGCounted: %v, %v", img, err)
	}
}
