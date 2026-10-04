//go:build darwin && cgo

package tray

import (
	"bytes"
	"testing"
)

// barImage is the one place Run and SetIcon pick the status-bar rendition
// (DESIGN A44: the badge swaps it with the same sizing and template rules).
func TestBarImagePicksTemplateThenLargeThenPNG(t *testing.T) {
	tmpl, large, small := []byte("template"), []byte("large"), []byte("png")
	cases := []struct {
		name     string
		icon     Icon
		want     []byte
		template int
	}{
		{"template wins", Icon{TemplatePNG: tmpl, LargePNG: large, PNG: small}, tmpl, 1},
		{"large coloured mark", Icon{LargePNG: large, PNG: small}, large, 0},
		{"only the small PNG", Icon{PNG: small}, small, 0},
		{"nothing to draw", Icon{ARGB32: []byte{1, 2, 3, 4}, Size: 1}, nil, 0},
	}
	for _, c := range cases {
		png, template := barImage(c.icon)
		if !bytes.Equal(png, c.want) || template != c.template {
			t.Errorf("%s: barImage = %q,%d want %q,%d", c.name, png, template, c.want, c.template)
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
	if png, _ := barImage(d.bar); string(png) != "badge128" {
		t.Errorf("status-bar icon = %q, want the badge", png)
	}
	if string(d.icon.LargePNG) != "plain128" {
		t.Errorf("brand row's mark = %q, want the plain mark", d.icon.LargePNG)
	}
	d.SetIcon(Icon{ARGB32: []byte{0, 0, 0, 0}, Size: 1})
	if png, _ := barImage(d.bar); string(png) != "badge128" {
		t.Errorf("an icon without a PNG replaced the bar icon: %q", png)
	}
}
