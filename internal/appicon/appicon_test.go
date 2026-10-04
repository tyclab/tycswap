package appicon

import (
	"bytes"
	"image/color"
	"image/png"
	"testing"

	"github.com/tyclab/tycswap/internal/brand"
)

var white = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}

// at64 maps a box unit to a pixel of the 64 px mark (the box is 64 units).
func at64(u float64) int { return int(u) }

func TestDrawIsAnOctopusOnTheTile(t *testing.T) {
	p := Colors()
	img := Draw(64)
	for _, c := range [][2]int{{0, 0}, {63, 0}, {0, 63}, {63, 63}} {
		if a := img.NRGBAAt(c[0], c[1]).A; a != 0 {
			t.Errorf("corner %v should be transparent (rounded), alpha %d", c, a)
		}
	}
	if c := img.NRGBAAt(3, 32); c != p.Tile {
		t.Errorf("left edge mid-height should be the tile, got %+v", c)
	}
	if c := img.NRGBAAt(at64(40), at64(14)); c != p.Body {
		t.Errorf("the head should be the accent, got %+v", c)
	}
	if c := img.NRGBAAt(at64(eyes[0].cx), at64(eyes[0].cy-eyes[0].ry+1.5)); c != p.Eye {
		t.Errorf("the top of the left eye should be white, got %+v", c)
	}
	if c := img.NRGBAAt(at64(pupils[1].cx), at64(pupils[1].cy)); c != p.Pupil {
		t.Errorf("the right pupil should be dark, got %+v", c)
	}
	if c := img.NRGBAAt(at64(highlight.cx), at64(highlight.cy)); c != p.Highlight {
		t.Errorf("the shine should be the lighter shade, got %+v", c)
	}
	// an arm, on its centre line below the head
	if c := img.NRGBAAt(at64(26.6), at64(48)); c != p.Body {
		t.Errorf("an arm should be the accent, got %+v", c)
	}
	// between the inner arms the tile shows through
	if c := img.NRGBAAt(32, 52); c != p.Tile {
		t.Errorf("between the arms the tile should show, got %+v", c)
	}
}

func TestPaletteIsDerivedFromTheAccent(t *testing.T) {
	p := PaletteFor("#5aa2ff")
	want := Palette{
		Tile:      color.NRGBA{R: 0x24, G: 0x41, B: 0x66, A: 0xff},
		Body:      color.NRGBA{R: 0x5a, G: 0xa2, B: 0xff, A: 0xff},
		Highlight: color.NRGBA{R: 0xad, G: 0xd1, B: 0xff, A: 0xff},
		Eye:       white,
		Pupil:     color.NRGBA{R: 0x0e, G: 0x18, B: 0x26, A: 0xff},
		Badge:     color.NRGBA{R: 0x5a, G: 0xa2, B: 0xff, A: 0xff},
		Ring:      white,
	}
	if p != want {
		t.Errorf("palette = %+v, want %+v", p, want)
	}
	if PaletteFor("red") != PaletteFor(DefaultAccent) {
		t.Error("an accent that is not #rrggbb must fall back to the default")
	}
	red := PaletteFor("#ff0000")
	if red.Body != (color.NRGBA{R: 0xff, A: 0xff}) || red.Tile != (color.NRGBA{R: 0x66, A: 0xff}) || red.Badge != red.Body {
		t.Errorf("red palette = %+v", red)
	}
}

// A build with another accent draws its icons in that accent's palette.
func TestAnotherAccentIsDrawnInItsPalette(t *testing.T) {
	saved := brand.AccentColor
	t.Cleanup(func() { brand.AccentColor = saved })
	plain := PNG(32)
	brand.AccentColor = "#ff0000"
	got := PNG(32)
	if bytes.Equal(got, plain) {
		t.Fatal("the default-accent icon was drawn for another accent")
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := img.At(20, 7).RGBA(); r>>8 != 0xff || g != 0 || b != 0 {
		t.Errorf("the head should be the red accent, got %x %x %x", r>>8, g>>8, b>>8)
	}
}

func TestEdgesAreAntialiased(t *testing.T) {
	img := Draw(64)
	partial := 0
	for x := 0; x < 64; x++ {
		if a := img.NRGBAAt(x, 2).A; a > 0 && a < 0xff {
			partial++
		}
	}
	if partial == 0 {
		t.Error("expected partially covered pixels along the rounded corners")
	}
	// the octopus blends into the tile, never into transparency, inside the square
	if c := img.NRGBAAt(at64(11.5), at64(27)); c.A != 0xff {
		t.Errorf("inside the square alpha stays opaque, got %+v", c)
	}
}

func TestPNGDecodes(t *testing.T) {
	for _, size := range []int{16, 22, 32, 64, 128} {
		img, err := png.Decode(bytes.NewReader(PNG(size)))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			t.Errorf("size %d: bounds %v", size, img.Bounds())
		}
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Errorf("size %d: corner alpha %d, want transparent", size, a)
		}
	}
}

func TestARGB32Layout(t *testing.T) {
	for _, size := range []int{16, 22} {
		b := ARGB32(size)
		if len(b) != size*size*4 {
			t.Fatalf("len = %d", len(b))
		}
		i := (size/2*size + size/2) * 4
		if b[i] != 0xff {
			t.Errorf("size %d: alpha at centre = %x", size, b[i])
		}
		if b[0] != 0 {
			t.Errorf("size %d: corner alpha = %x, want transparent", size, b[0])
		}
	}
}

func TestTinySizeClamped(t *testing.T) {
	if got := Draw(1).Bounds().Dx(); got != 8 {
		t.Errorf("min size = %d, want 8", got)
	}
}

func TestFlattenParsesThePaths(t *testing.T) {
	if n := len(head); n != 1 {
		t.Errorf("the head is one contour, got %d", n)
	}
	if n := len(arms); n != len(pathArms) {
		t.Errorf("one centre line per arm: got %d for %d arms", n, len(pathArms))
	}
	if w := winding(head, 32, 25); w == 0 {
		t.Error("winding inside the head must be non-zero")
	}
	if w := winding(head, 32, 50); w != 0 {
		t.Errorf("winding below the head = %d, want 0", w)
	}
	if !nearStroke(arms, 7.5, 51.5, armHalf) || nearStroke(arms, 32, 55, armHalf) {
		t.Error("an arm's tip is on its stroke; the gap between the arms is not")
	}
}

// eyeCovered reports whether any coverage sample of pixel (x, y) of a size
// px mark falls inside an eye: the badge must leave such a pixel alone.
func eyeCovered(size, x, y int) bool {
	k := float64(size) / unit
	for sy := 0; sy < superSample; sy++ {
		for sx := 0; sx < superSample; sx++ {
			px := (float64(x) + (float64(sx)+0.5)/superSample) / k
			py := (float64(y) + (float64(sy)+0.5)/superSample) / k
			for _, e := range eyes {
				if e.has(px, py) {
					return true
				}
			}
		}
	}
	return false
}

// badgeGeometry mirrors DrawBadge: the badge's centre and its two radii.
func badgeGeometry(size int) (cx, cy, outer, inner float64) {
	s := float64(size)
	outer = s * badgeShare / 2
	return s - outer, outer, outer, outer - s*ringShare
}

func TestBadgeIsAnAccentDiscInAWhiteRing(t *testing.T) {
	const size = 64
	p := Colors()
	img := DrawBadge(size)
	cx, cy, outer, inner := badgeGeometry(size)
	if c := img.NRGBAAt(int(cx), int(cy)); c != p.Badge {
		t.Errorf("badge centre should be %+v, got %+v", p.Badge, c)
	}
	if c := img.NRGBAAt(int(cx-(inner+outer)/2), int(cy)); c != white {
		t.Errorf("the ring should be opaque white, got %+v", c)
	}
	if c := img.NRGBAAt(int(cx+inner/2), int(cy-inner/2)); c != p.Badge {
		t.Errorf("badge fill off-centre should be the accent, got %+v", c)
	}
	blends := 0
	for y := 0; y < int(2*outer); y++ {
		for x := size - int(2*outer); x < size; x++ {
			if c := img.NRGBAAt(x, y); c.A != 0 && c != p.Tile && c != p.Body && c != white && c != p.Badge {
				blends++
			}
		}
	}
	if blends == 0 {
		t.Error("expected blended pixels along the badge's edges")
	}
}

// The badge changes the square's corner only, and never a pixel an eye
// touches: the octopus must still look out of a badged icon.
func TestBadgeNeverCoversAnEye(t *testing.T) {
	for _, size := range []int{16, 22, 32, 36, 64, 128} {
		plain, badged := Draw(size), DrawBadge(size)
		cx, cy, outer, _ := badgeGeometry(size)
		changed := 0
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				if plain.NRGBAAt(x, y) == badged.NRGBAAt(x, y) {
					continue
				}
				changed++
				dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
				if dx*dx+dy*dy > (outer+0.75)*(outer+0.75) {
					t.Fatalf("size %d: pixel (%d,%d) outside the badge changed", size, x, y)
				}
				if eyeCovered(size, x, y) {
					t.Fatalf("size %d: the badge covers an eye at (%d,%d)", size, x, y)
				}
			}
		}
		if changed == 0 {
			t.Errorf("size %d: DrawBadge drew no badge", size)
		}
	}
}

func TestBadgePNGAndARGB32(t *testing.T) {
	p := Colors()
	for _, size := range []int{16, 22, 32, 128} {
		img, err := png.Decode(bytes.NewReader(PNGBadge(size)))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			t.Errorf("size %d: bounds %v", size, img.Bounds())
		}
	}
	const size = 22
	b := ARGB32Badge(size)
	if len(b) != size*size*4 {
		t.Fatalf("len = %d", len(b))
	}
	cx, cy, _, _ := badgeGeometry(size)
	i := (int(cy)*size + int(cx)) * 4
	if got := [4]byte{b[i], b[i+1], b[i+2], b[i+3]}; got != [4]byte{0xff, p.Badge.R, p.Badge.G, p.Badge.B} {
		t.Errorf("ARGB at the badge centre = % x, want ff %02x %02x %02x", got, p.Badge.R, p.Badge.G, p.Badge.B)
	}
	if b[0] != 0 {
		t.Errorf("top-left corner alpha = %x, want transparent", b[0])
	}
	i = ((size/2)*size + 1) * 4
	if got := [4]byte{b[i], b[i+1], b[i+2], b[i+3]}; got != [4]byte{0xff, p.Tile.R, p.Tile.G, p.Tile.B} {
		t.Errorf("ARGB at the left edge = % x, want the tile", got)
	}
	if got := DrawBadge(1).Bounds().Dx(); got != 8 {
		t.Errorf("min badge size = %d, want 8", got)
	}
}
