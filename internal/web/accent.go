package web

import (
	"fmt"
	"net/http"

	"github.com/tyclab/tycswap/internal/brand"
)

// accentCSS shares the light-surface accent with native menus.
func accentCSS(accent string) string {
	return fmt.Sprintf(":root { --brand: %s; --brand-deep: %s; }\n", accent, brand.LightAccent(accent))
}

func (s *Server) handleAccent(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(accentCSS(brand.Sanitized().AccentColor)))
}
