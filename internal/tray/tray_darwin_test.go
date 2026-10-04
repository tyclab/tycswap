//go:build darwin && cgo

package tray

import (
	"bytes"
	"testing"
)

// barImage is the one place Run and SetIcon pick the status-bar rendition
// (DESIGN A44: the badge swaps it with the same sizing rules).
func TestBarImagePicksLargeThenPNG(t *testing.T) {
	large, small := []byte("large"), []byte("png")
	cases := []struct {
		name string
		icon Icon
		want []byte
	}{
		{"large coloured mark", Icon{LargePNG: large, PNG: small}, large},
		{"only the small PNG", Icon{PNG: small}, small},
		{"nothing to draw", Icon{ARGB32: []byte{1, 2, 3, 4}, Size: 1}, nil},
	}
	for _, c := range cases {
		if png := barImage(c.icon); !bytes.Equal(png, c.want) {
			t.Errorf("%s: barImage = %q want %q", c.name, png, c.want)
		}
	}
}

// Before Run there is no status item: SetIcon records the icon Run will show
// and leaves the brand row's mark alone. An icon macOS cannot draw is ignored.
func TestSetIconBeforeRunChangesOnlyTheBarIcon(t *testing.T) {
	plain := Icon{PNG: []byte("plain32"), LargePNG: []byte("plain128")}
	tr, err := newTray(plain, Options{})
	if err != nil {
		t.Fatal(err)
	}
	d := tr.(*darwinTrayImpl)
	badge := Icon{PNG: []byte("badge32"), LargePNG: []byte("badge128")}
	d.SetIcon(badge)
	if png := barImage(d.bar); string(png) != "badge128" {
		t.Errorf("status-bar icon = %q, want the badge", png)
	}
	if string(d.icon.LargePNG) != "plain128" {
		t.Errorf("brand row's mark = %q, want the plain mark", d.icon.LargePNG)
	}
	d.SetIcon(Icon{ARGB32: []byte{0, 0, 0, 0}, Size: 1})
	if png := barImage(d.bar); string(png) != "badge128" {
		t.Errorf("an icon without a PNG replaced the bar icon: %q", png)
	}
}
