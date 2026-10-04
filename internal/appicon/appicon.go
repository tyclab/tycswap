// Package appicon draws the tycswap tray mark — an octopus, head, eyes and
// four arms, on a rounded square — at any size, so the tray and the menu need
// no image assets beyond what this package renders itself: macOS and Windows
// get a coloured PNG, Linux a coloured ARGB bitmap, and a monochrome template
// variant exists for menu bars that want one. Stdlib only (image, image/png):
// the head is SVG path data flattened and scan-converted here with 4×4
// supersampling, the arms are stroked centre lines.
//
// Every colour comes from brand.AccentColor: the square is a darker shade of
// it, the octopus the accent itself with a lighter shade as a highlight, the
// eyes white with dark pupils, and the update badge an accent disc in a white
// ring. The sizes the trays use are rendered ahead of time by `go generate`
// (gen/) and embedded (rendered/); a build whose accent differs from the
// default draws at run time instead.
package appicon

//go:generate go run ./gen

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strconv"

	"github.com/tyclab/tycswap/internal/brand"
)

// Palette is the mark's colours, all derived from one accent.
type Palette struct {
	// Tile is the rounded square: the accent mixed 60 % towards black.
	Tile color.NRGBA
	// Body is the octopus: the accent itself.
	Body color.NRGBA
	// Highlight is the shine on the head: the accent mixed halfway to white.
	Highlight color.NRGBA
	// Eye is the white of the eyes, Pupil the accent mixed 85 % towards black.
	Eye   color.NRGBA
	Pupil color.NRGBA
	// Badge fills the update badge (DrawBadge, DrawCounted); Ring is the white
	// ring around it that keeps it apart from the mark.
	Badge color.NRGBA
	Ring  color.NRGBA
}

// DefaultAccent is the accent the embedded renderings were drawn with.
const DefaultAccent = "#5aa2ff"

// PaletteFor derives the palette from a #rrggbb accent; anything else is the
// default accent.
func PaletteFor(accent string) Palette {
	a, ok := parseHex(accent)
	if !ok {
		a, _ = parseHex(DefaultAccent)
	}
	white := color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	return Palette{
		Tile:      mix(a, color.NRGBA{A: 0xff}, 0.6),
		Body:      a,
		Highlight: mix(a, white, 0.5),
		Eye:       white,
		Pupil:     mix(a, color.NRGBA{A: 0xff}, 0.85),
		Badge:     a,
		Ring:      white,
	}
}

// Colors is the palette of this build's accent (brand.AccentColor).
func Colors() Palette { return PaletteFor(brand.Sanitized().AccentColor) }

// parseHex reads #rrggbb.
func parseHex(s string) (color.NRGBA, bool) {
	if len(s) != 7 || s[0] != '#' {
		return color.NRGBA{}, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}, true
}

// mix moves c towards to by f (0..1), opaque.
func mix(c, to color.NRGBA, f float64) color.NRGBA {
	m := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*f + 0.5) }
	return color.NRGBA{R: m(c.R, to.R), G: m(c.G, to.G), B: m(c.B, to.B), A: 0xff}
}

// The octopus in a 64×64 box, y down. The head is a closed path (absolute
// M/C/L/Z), each arm the centre line of a round-capped stroke.
const (
	unit = 64.0

	pathHead = "M32 7C44.7 7 52.5 15.8 52.5 27.5C52.5 34.6 49.6 39.6 45.6 43L18.4 43C14.4 39.6 11.5 34.6 11.5 27.5C11.5 15.8 19.3 7 32 7Z"

	armHalf = 3.3 // half the arms' stroke width

	cornerShare = 0.2 // the square's corner radius relative to its size
	superSample = 4   // 4×4 coverage samples per pixel

	// The update badge (DrawBadge): an accent disc in a white ring, flush
	// with the top-right corner. 36 % of the square with a 6 % ring keeps it
	// clear of the right eye at every tray size from 16 px up.
	badgeShare = 0.36 // the badge's outer diameter (ring included) relative to the square
	ringShare  = 0.06 // the white ring's width relative to the square
)

// The arms: the outer two curl outwards, the inner two hang and turn out.
var pathArms = []string{
	"M19.5 40C16.5 46.5 13 52 7.5 51.5",
	"M27 42C26.5 48.5 25.5 54 21 57",
	"M37 42C37.5 48.5 38.5 54 43 57",
	"M44.5 40C47.5 46.5 51 52 56.5 51.5",
}

// eye is an ellipse in box units; the pupils are discs inside the eyes.
type eye struct{ cx, cy, rx, ry float64 }

var (
	eyes = []eye{{25, 30, 4.4, 5.4}, {39, 30, 4.4, 5.4}}
	// pupils sit low and a little inwards, looking at the person.
	pupils = []eye{{25.6, 31.4, 2.5, 2.5}, {38.4, 31.4, 2.5, 2.5}}
	// highlight is the shine on the head's upper left.
	highlight = eye{22.5, 16.5, 4.6, 2.6}
)

var (
	head    = flatten(pathHead)
	headBox = boxOf(head[0])
	arms    = func() [][]point {
		var out [][]point
		for _, d := range pathArms {
			out = append(out, flatten(d)...)
		}
		return out
	}()
	// bodyBox holds the head and the arms' strokes.
	bodyBox = func() box {
		b := headBox
		for _, a := range arms {
			o := boxOf(a)
			b.x0, b.y0 = min(b.x0, o.x0-armHalf), min(b.y0, o.y0-armHalf)
			b.x1, b.y1 = max(b.x1, o.x1+armHalf), max(b.y1, o.y1+armHalf)
		}
		return b
	}()
)

// Draw renders the mark at size×size pixels in this build's palette.
// template=false is the coloured icon. template=true is the monochrome form
// for menu bars that recolour icons: an opaque black square with the octopus
// cut out and its eyes left standing in the cut, so the bar shows through the
// silhouette.
func Draw(size int, template bool) *image.NRGBA {
	return drawWith(size, template, Colors())
}

func drawWith(size int, template bool, p Palette) *image.NRGBA {
	if size < 8 {
		size = 8
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	k := s / unit // pixels per box unit
	radius := s * cornerShare
	black := color.NRGBA{A: 0xff}
	tile := p.Tile
	if template {
		tile = black
	}
	paint(img, func(x, y float64) bool { return inRoundedRect(x, y, s, radius) }, func(x, y int, cov float64) {
		img.SetNRGBA(x, y, withAlpha(tile, cov))
	})
	body := func(x, y float64) bool {
		px, py := x/k, y/k
		if headBox.has(px, py) && winding(head, px, py) != 0 {
			return true
		}
		return nearStroke(arms, px, py, armHalf)
	}
	in := func(e eye) func(x, y float64) bool {
		return func(x, y float64) bool { return e.has(x/k, y/k) }
	}
	if template {
		cut := func(x, y int, cov float64) {
			c := img.NRGBAAt(x, y)
			c.A = uint8(float64(c.A)*(1-cov) + 0.5)
			img.SetNRGBA(x, y, c)
		}
		fill := func(x, y int, cov float64) {
			img.SetNRGBA(x, y, over(black, img.NRGBAAt(x, y), cov))
		}
		paintIn(img, bodyBox, k, body, cut)
		for _, e := range eyes {
			paintIn(img, e.box(), k, in(e), fill)
		}
		for _, e := range pupils {
			paintIn(img, e.box(), k, in(e), cut)
		}
		return img
	}
	layer := func(c color.NRGBA) func(x, y int, cov float64) {
		return func(x, y int, cov float64) { img.SetNRGBA(x, y, over(c, img.NRGBAAt(x, y), cov)) }
	}
	paintIn(img, bodyBox, k, body, layer(p.Body))
	// The shine reads as noise below 24 px.
	if size >= 24 {
		paintIn(img, highlight.box(), k, in(highlight), layer(p.Highlight))
	}
	for _, e := range eyes {
		paintIn(img, e.box(), k, in(e), layer(p.Eye))
	}
	for _, e := range pupils {
		paintIn(img, e.box(), k, in(e), layer(p.Pupil))
	}
	return img
}

func (e eye) box() box { return box{e.cx - e.rx, e.cy - e.ry, e.cx + e.rx, e.cy + e.ry} }

func (e eye) has(x, y float64) bool {
	dx, dy := (x-e.cx)/e.rx, (y-e.cy)/e.ry
	return dx*dx+dy*dy <= 1
}

// PNG encodes Draw(size, template); the sizes the trays use come from the
// embedded renderings.
func PNG(size int, template bool) []byte {
	name := "mark-" + strconv.Itoa(size) + ".png"
	if template {
		name = "template-" + strconv.Itoa(size) + ".png"
	}
	if b, ok := prerendered(name); ok {
		return b
	}
	return encode(Draw(size, template))
}

// ARGB32 returns Draw(size, false) as network-order ARGB rows, the pixmap
// format the StatusNotifierItem D-Bus interface expects: size²·4 bytes (sizes
// below 8 are drawn at 8, as in Draw).
func ARGB32(size int) []byte {
	if img, ok := prerenderedImage("mark-" + strconv.Itoa(size) + ".png"); ok {
		return argb(img)
	}
	return argb(Draw(size, false))
}

// DrawBadge renders the coloured mark with the update badge: an accent disc
// in a white ring, flush with the top-right corner. The badge covers the
// square's corner and the top of the head, never an eye, and the ring keeps
// the disc apart from the octopus of the same colour at 16 px as well as on a
// dark menu bar, where the corner outside the rounded square is the bar.
func DrawBadge(size int) *image.NRGBA { return drawBadgeWith(size, Colors()) }

func drawBadgeWith(size int, p Palette) *image.NRGBA {
	img := drawWith(size, false, p)
	s := float64(img.Bounds().Dx()) // Draw clamps tiny sizes
	outer := s * badgeShare / 2
	inner := outer - s*ringShare
	cx, cy := s-outer, outer
	disc := func(r float64) func(x, y float64) bool {
		return func(x, y float64) bool {
			dx, dy := x-cx, y-cy
			return dx*dx+dy*dy <= r*r
		}
	}
	area := box{cx - outer, cy - outer, cx + outer, cy + outer}
	paintIn(img, area, 1, disc(outer), func(x, y int, cov float64) {
		img.SetNRGBA(x, y, over(p.Ring, img.NRGBAAt(x, y), cov))
	})
	paintIn(img, area, 1, disc(inner), func(x, y int, cov float64) {
		img.SetNRGBA(x, y, over(p.Badge, img.NRGBAAt(x, y), cov))
	})
	return img
}

// PNGBadge encodes DrawBadge(size).
func PNGBadge(size int) []byte {
	if b, ok := prerendered("badge-" + strconv.Itoa(size) + ".png"); ok {
		return b
	}
	return encode(DrawBadge(size))
}

// ARGB32Badge is DrawBadge(size) in the StatusNotifierItem pixmap format.
func ARGB32Badge(size int) []byte {
	if img, ok := prerenderedImage("badge-" + strconv.Itoa(size) + ".png"); ok {
		return argb(img)
	}
	return argb(DrawBadge(size))
}

func encode(img image.Image) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// argb flattens img into network-order ARGB rows.
func argb(img *image.NRGBA) []byte {
	b := img.Bounds()
	out := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			out = append(out, c.A, c.R, c.G, c.B)
		}
	}
	return out
}

// paint calls set for every pixel the shape covers, with its coverage in
// (0, 1], sampled on a superSample×superSample grid.
func paint(img *image.NRGBA, inside func(x, y float64) bool, set func(x, y int, cov float64)) {
	paintRect(img, img.Bounds(), inside, set)
}

// paintIn is paint over the pixels of a box in box units only (k pixels per
// unit), which a shape outside it cannot cover: the eyes and the arms are a
// small part of the square.
func paintIn(img *image.NRGBA, b box, k float64, inside func(x, y float64) bool, set func(x, y int, cov float64)) {
	r := image.Rect(int(b.x0*k)-1, int(b.y0*k)-1, int(b.x1*k)+2, int(b.y1*k)+2)
	paintRect(img, r.Intersect(img.Bounds()), inside, set)
}

func paintRect(img *image.NRGBA, b image.Rectangle, inside func(x, y float64) bool, set func(x, y int, cov float64)) {
	n := float64(superSample)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			hits := 0
			for sy := 0; sy < superSample; sy++ {
				for sx := 0; sx < superSample; sx++ {
					if inside(float64(x)+(float64(sx)+0.5)/n, float64(y)+(float64(sy)+0.5)/n) {
						hits++
					}
				}
			}
			if hits > 0 {
				set(x, y, float64(hits)/(n*n))
			}
		}
	}
}

func withAlpha(c color.NRGBA, cov float64) color.NRGBA {
	c.A = uint8(float64(c.A)*cov + 0.5)
	return c
}

// over composites fg at opacity cov onto bg (non-premultiplied).
func over(fg, bg color.NRGBA, cov float64) color.NRGBA {
	fa := cov * float64(fg.A) / 255
	ba := float64(bg.A) / 255 * (1 - fa)
	a := fa + ba
	if a == 0 {
		return color.NRGBA{}
	}
	mix := func(f, b uint8) uint8 {
		return uint8((float64(f)*fa+float64(b)*ba)/a + 0.5)
	}
	return color.NRGBA{R: mix(fg.R, bg.R), G: mix(fg.G, bg.G), B: mix(fg.B, bg.B), A: uint8(a*255 + 0.5)}
}

func inRoundedRect(x, y, s, r float64) bool {
	if x < 0 || y < 0 || x > s || y > s {
		return false
	}
	cx := clamp(x, r, s-r)
	cy := clamp(y, r, s-r)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---- path geometry -------------------------------------------------------

type point struct{ x, y float64 }

// winding is the non-zero winding number of (x, y) against the polygons,
// the SVG default fill rule.
func winding(polys [][]point, x, y float64) int {
	wn := 0
	for _, poly := range polys {
		wn += polyWinding(poly, x, y)
	}
	return wn
}

// polyWinding is one closed contour's contribution to the winding number.
func polyWinding(poly []point, x, y float64) int {
	wn := 0
	n := len(poly)
	for i := 0; i < n; i++ {
		a, b := poly[i], poly[(i+1)%n]
		if a.y <= y {
			if b.y > y && cross(a, b, x, y) > 0 {
				wn++
			}
		} else if b.y <= y && cross(a, b, x, y) < 0 {
			wn--
		}
	}
	return wn
}

// box is an axis-aligned bounding rectangle in path units.
type box struct{ x0, y0, x1, y1 float64 }

func boxOf(poly []point) box {
	b := box{poly[0].x, poly[0].y, poly[0].x, poly[0].y}
	for _, p := range poly[1:] {
		b.x0, b.y0 = min(b.x0, p.x), min(b.y0, p.y)
		b.x1, b.y1 = max(b.x1, p.x), max(b.y1, p.y)
	}
	return b
}

func (b box) has(x, y float64) bool { return x >= b.x0 && x <= b.x1 && y >= b.y0 && y <= b.y1 }

// cross is > 0 when (x, y) lies left of the directed edge a→b.
func cross(a, b point, x, y float64) float64 {
	return (b.x-a.x)*(y-a.y) - (x-a.x)*(b.y-a.y)
}

// flatten turns an absolute M/L/H/V/C/Z path into polylines; each cubic
// becomes 16 chords, plenty at icon sizes. A closed subpath is a polygon for
// winding; an open one is a centre line for nearStroke.
func flatten(d string) [][]point {
	var polys [][]point
	var cur []point
	var pen, start point
	end := func() {
		if len(cur) > 1 {
			polys = append(polys, cur)
		}
		cur = nil
	}
	i := 0
	for i < len(d) {
		c := d[i]
		i++
		switch c {
		case 'M':
			end()
			pen.x, i = num(d, i)
			pen.y, i = num(d, i)
			start = pen
			cur = append(cur, pen)
		case 'L':
			pen.x, i = num(d, i)
			pen.y, i = num(d, i)
			cur = append(cur, pen)
		case 'H':
			pen.x, i = num(d, i)
			cur = append(cur, pen)
		case 'V':
			pen.y, i = num(d, i)
			cur = append(cur, pen)
		case 'C':
			var p1, p2, p3 point
			p1.x, i = num(d, i)
			p1.y, i = num(d, i)
			p2.x, i = num(d, i)
			p2.y, i = num(d, i)
			p3.x, i = num(d, i)
			p3.y, i = num(d, i)
			const steps = 16
			for k := 1; k <= steps; k++ {
				t := float64(k) / steps
				u := 1 - t
				cur = append(cur, point{
					x: u*u*u*pen.x + 3*u*u*t*p1.x + 3*u*t*t*p2.x + t*t*t*p3.x,
					y: u*u*u*pen.y + 3*u*u*t*p1.y + 3*u*t*t*p2.y + t*t*t*p3.y,
				})
			}
			pen = p3
		case 'Z', 'z':
			pen = start
			end()
		}
	}
	end()
	return polys
}

// num parses the next number in d starting at i (skipping separators) and
// returns it with the index after it.
func num(d string, i int) (float64, int) {
	for i < len(d) && (d[i] == ' ' || d[i] == ',') {
		i++
	}
	j := i
	for j < len(d) {
		ch := d[j]
		if (ch >= '0' && ch <= '9') || ch == '.' || (ch == '-' && j == i) {
			j++
			continue
		}
		break
	}
	v, _ := strconv.ParseFloat(d[i:j], 64)
	return v, j
}
