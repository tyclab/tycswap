// countbadge.go — the menu-bar mark with a count beside it: how many things
// wait, white on an accent badge at the mark's top-right corner, mostly
// outside the square so that the octopus's eyes stay whole. macOS takes a
// status-bar image of any width; Windows and Linux draw a square icon and keep
// DrawBadge's dot.
package appicon

import (
	"image"
	"math"
	"strconv"
)

const (
	// The badge's outer height (ring included) relative to the mark's square,
	// and its white ring. 62 % puts a digit of about 11 px into a 36 px
	// (Retina 18 pt) menu-bar image: readable at a glance, where a dot inside
	// the square is easy to miss.
	countShare     = 0.62
	countRingShare = 0.07
	// countGap is the gap between the badge and the right eye, relative to
	// the square: the badge overlaps the square's corner and the head, never
	// an eye.
	countGap = 0.03
	// glyphShare is a digit's height relative to the accent disc, strokeShare
	// its stroke relative to that height.
	glyphShare  = 0.62
	strokeShare = 0.2
)

// countLabel is what the badge says: 1–9, else "9+".
func countLabel(n int) string {
	switch {
	case n < 1:
		return "1"
	case n > 9:
		return "9+"
	}
	return strconv.Itoa(n)
}

// countGeometry is where DrawCounted puts the badge on a mark of height s:
// the left half-disc's centre (cx, cy), the outer and inner radii, the
// straight part a two-glyph label adds, and the image's width.
func countGeometry(s float64, label string) (cx, cy, outer, inner, straight float64, width int) {
	outer = s * countShare / 2
	inner = outer - s*countRingShare
	glyphH := 2 * inner * glyphShare
	straight = float64(len(label)-1) * glyphH * 0.66
	// The disc's left end may come as close to the right eye as the gap
	// allows: the centre sits on the circle of radius outer+eye+gap around
	// the eye, at the badge's height, or flush right of the square's middle
	// when the eye is lower than that circle reaches.
	k := s / unit
	right := eyes[len(eyes)-1]
	ex, ey := right.cx*k, right.cy*k
	clear := outer + max(right.rx, right.ry)*k + s*countGap
	cy = outer
	cx = s / 2
	if dy := ey - cy; dy < clear {
		cx = max(cx, ex+math.Sqrt(clear*clear-dy*dy))
	}
	width = int(math.Ceil(cx + straight + outer))
	return cx, cy, outer, inner, straight, width
}

// DrawCounted renders the coloured mark at height×height with a count badge
// at its top-right corner. The image is wider than tall: the badge reaches
// past the square, so it is as large as the menu bar allows without covering
// an eye. n below 1 is drawn as 1, above 9 as "9+".
func DrawCounted(height, n int) *image.NRGBA {
	p := Colors()
	mark := drawWith(height, p)
	s := float64(mark.Bounds().Dy()) // Draw clamps tiny sizes
	label := countLabel(n)
	cx, cy, outer, inner, straight, width := countGeometry(s, label)
	glyphH := 2 * inner * glyphShare
	advance := glyphH * 0.66 // one glyph's width with its spacing

	img := image.NewNRGBA(image.Rect(0, 0, width, mark.Bounds().Dy()))
	for y := 0; y < mark.Bounds().Dy(); y++ {
		copy(img.Pix[y*img.Stride:], mark.Pix[y*mark.Stride:y*mark.Stride+mark.Bounds().Dx()*4])
	}
	pill := func(r float64) func(x, y float64) bool {
		return func(x, y float64) bool {
			px := clamp(x, cx, cx+straight)
			ddx, ddy := x-px, y-cy
			return ddx*ddx+ddy*ddy <= r*r
		}
	}
	area := img.SubImage(image.Rect(int(cx-outer)-1, 0, width, int(2*outer)+1).Intersect(img.Bounds())).(*image.NRGBA)
	paint(area, pill(outer), func(x, y int, cov float64) {
		img.SetNRGBA(x, y, over(p.Ring, img.NRGBAAt(x, y), cov))
	})
	paint(area, pill(inner), func(x, y int, cov float64) {
		img.SetNRGBA(x, y, over(p.Badge, img.NRGBAAt(x, y), cov))
	})

	// The label, centred on the badge; glyphs are 0.6 wide and 1 high.
	var strokes [][]point
	left := cx + straight/2 - (float64(len(label)-1)*advance+0.6*glyphH)/2
	for i, r := range label {
		ox, oy := left+float64(i)*advance, cy-glyphH/2
		for _, line := range glyph(r) {
			placed := make([]point, len(line))
			for j, q := range line {
				placed[j] = point{ox + q.x*glyphH, oy + q.y*glyphH}
			}
			strokes = append(strokes, placed)
		}
	}
	half := glyphH * strokeShare / 2
	paint(area, func(x, y float64) bool { return nearStroke(strokes, x, y, half) }, func(x, y int, cov float64) {
		img.SetNRGBA(x, y, over(p.Ring, img.NRGBAAt(x, y), cov))
	})
	return img
}

// PNGCounted encodes DrawCounted(height, n).
func PNGCounted(height, n int) []byte { return encode(DrawCounted(height, n)) }

// nearStroke reports whether (x, y) lies within half of any polyline.
func nearStroke(lines [][]point, x, y, half float64) bool {
	for _, l := range lines {
		if b := boxOf(l); x < b.x0-half || x > b.x1+half || y < b.y0-half || y > b.y1+half {
			continue
		}
		for i := 0; i+1 < len(l); i++ {
			if segmentDist2(l[i], l[i+1], x, y) <= half*half {
				return true
			}
		}
	}
	return false
}

func segmentDist2(a, b point, x, y float64) float64 {
	vx, vy := b.x-a.x, b.y-a.y
	t := 0.0
	if l := vx*vx + vy*vy; l > 0 {
		t = clamp(((x-a.x)*vx+(y-a.y)*vy)/l, 0, 1)
	}
	dx, dy := x-(a.x+t*vx), y-(a.y+t*vy)
	return dx*dx + dy*dy
}

// arc samples an ellipse around (cx, cy) from a0 to a1 degrees (0° points
// right, 90° down, as y grows downwards).
func arc(cx, cy, rx, ry, a0, a1 float64) []point {
	const steps = 24
	pts := make([]point, 0, steps+1)
	for i := 0; i <= steps; i++ {
		a := (a0 + (a1-a0)*float64(i)/steps) * math.Pi / 180
		pts = append(pts, point{cx + rx*math.Cos(a), cy + ry*math.Sin(a)})
	}
	return pts
}

func join(parts ...[]point) []point {
	var out []point
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// glyph is a digit or "+" as centre lines in a 0.6×1 box, y down; the stroke
// is drawn around them. Only what countLabel can say.
func glyph(r rune) [][]point {
	switch r {
	case '1':
		return [][]point{{{0.12, 0.25}, {0.34, 0.09}, {0.34, 0.91}}}
	case '2':
		return [][]point{join(arc(0.3, 0.31, 0.2, 0.22, 180, 400), []point{{0.1, 0.91}, {0.52, 0.91}})}
	case '3':
		return [][]point{join(arc(0.29, 0.29, 0.19, 0.2, 200, 450), arc(0.29, 0.7, 0.215, 0.21, 270, 520))}
	case '4':
		return [][]point{{{0.41, 0.91}, {0.41, 0.09}, {0.08, 0.66}, {0.54, 0.66}}}
	case '5':
		return [][]point{join([]point{{0.48, 0.09}, {0.14, 0.09}, {0.11, 0.47}}, arc(0.29, 0.66, 0.215, 0.25, 220, 520))}
	case '6':
		return [][]point{arc(0.3, 0.665, 0.205, 0.245, 0, 360), arc(0.52, 0.66, 0.42, 0.57, 180, 247)}
	case '7':
		return [][]point{{{0.08, 0.09}, {0.52, 0.09}, {0.22, 0.91}}}
	case '8':
		return [][]point{arc(0.3, 0.285, 0.175, 0.195, 0, 360), arc(0.3, 0.695, 0.21, 0.215, 0, 360)}
	case '9':
		var out [][]point
		for _, l := range glyph('6') {
			turned := make([]point, len(l))
			for i, p := range l {
				turned[i] = point{0.6 - p.x, 1 - p.y}
			}
			out = append(out, turned)
		}
		return out
	case '+':
		return [][]point{{{0.3, 0.24}, {0.3, 0.76}}, {{0.04, 0.5}, {0.56, 0.5}}}
	}
	return nil
}
