// accent.go — GET /static/accent.css: the build's brand accent as CSS custom
// properties. The page's CSP forbids inline style, so the one value a build
// may override (brand.AccentColor) is served as a tiny stylesheet of its own
// instead of being templated into the page.
package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/brand"
)

// defaultAccentDeep is the deeper TycStation blue the light theme uses where
// the bright default accent has too little contrast on white.
const defaultAccentDeep = "#12356f"

// accentCSS renders --brand (on dark surfaces) and --brand-deep (on light
// ones) for a validated #rrggbb accent. The default accent keeps its designed
// deep partner; any other accent gets a deep variant mixed towards black.
func accentCSS(accent string) string {
	deep := defaultAccentDeep
	if !strings.EqualFold(accent, "#5aa2ff") {
		deep = darken(accent, 0.6)
	}
	return fmt.Sprintf(":root { --brand: %s; --brand-deep: %s; }\n", accent, deep)
}

// darken mixes a #rrggbb colour towards black by f (0..1).
func darken(hex string, f float64) string {
	if len(hex) != 7 || hex[0] != '#' {
		return defaultAccentDeep
	}
	out := "#"
	for i := 1; i < 7; i += 2 {
		v, err := strconv.ParseUint(hex[i:i+2], 16, 8)
		if err != nil {
			return defaultAccentDeep
		}
		out += fmt.Sprintf("%02x", int(float64(v)*(1-f)+0.5))
	}
	return out
}

func (s *Server) handleAccent(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(accentCSS(brand.Sanitized().AccentColor)))
}
