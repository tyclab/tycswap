// prerendered.go — the tray sizes, rendered ahead of time. `go generate`
// (gen/) writes Renderings into rendered/, and the build embeds them, so a
// tray shows its icon without rasterizing the mark at start: the 128 px mark
// and its counted variants take the bulk of the drawing time, more so under
// the race detector. A test renders them again and compares the bytes, so the
// files cannot drift from the drawing code.
package appicon

import (
	"bytes"
	"embed"
	"image"
	"image/draw"
	"image/png"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/brand"
)

//go:embed rendered/*.png
var renderedFS embed.FS

// The sizes the trays take: the coloured mark at 22 px (Linux pixmap), 32 px
// (Windows) and 128 px (the macOS menu bar and the menu's brand row), the
// dotted badge at 22 and 32 px, and the counted badge at 128 px for 1–9 and
// "9+".
var (
	markSizes  = []int{22, 32, 128}
	badgeSizes = []int{22, 32}
	countSize  = 128
)

// Renderings draws every embedded file in the default palette, keyed by its
// name under rendered/. gen/ writes them; the test compares them.
func Renderings() map[string][]byte {
	p := PaletteFor(DefaultAccent)
	out := map[string][]byte{}
	for _, s := range markSizes {
		out["mark-"+strconv.Itoa(s)+".png"] = encode(drawWith(s, false, p))
	}
	for _, s := range badgeSizes {
		out["badge-"+strconv.Itoa(s)+".png"] = encode(drawBadgeWith(s, p))
	}
	for n := 1; n <= 10; n++ {
		out["count-"+strconv.Itoa(countSize)+"-"+countLabelName(n)+".png"] = encode(drawCountedWith(countSize, n, p))
	}
	return out
}

// prerendered returns an embedded file, but only for a build with the
// default accent: the files are drawn in its palette.
func prerendered(name string) ([]byte, bool) {
	if !strings.EqualFold(brand.Sanitized().AccentColor, DefaultAccent) {
		return nil, false
	}
	b, err := renderedFS.ReadFile("rendered/" + name)
	if err != nil {
		return nil, false
	}
	return b, true
}

// prerenderedImage decodes an embedded file, for the ARGB pixmaps.
func prerenderedImage(name string) (*image.NRGBA, bool) {
	b, ok := prerendered(name)
	if !ok {
		return nil, false
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, false
	}
	if n, ok := img.(*image.NRGBA); ok {
		return n, true
	}
	n := image.NewNRGBA(img.Bounds())
	draw.Draw(n, n.Bounds(), img, img.Bounds().Min, draw.Src)
	return n, true
}
