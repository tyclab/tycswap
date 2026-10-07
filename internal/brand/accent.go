package brand

import (
	"fmt"
	"strconv"
	"strings"
)

// LightAccent is the dashboard and native-menu accent on light surfaces.
// The default blue has a designed deep partner; other brand colors are
// mixed 60 percent toward black, matching the dashboard's light theme.
func LightAccent(accent string) string {
	const deep = "#12356f"
	if strings.EqualFold(accent, defaultAccentColor) || !colorRe.MatchString(accent) {
		return deep
	}
	out := "#"
	for i := 1; i < 7; i += 2 {
		v, _ := strconv.ParseUint(accent[i:i+2], 16, 8)
		out += fmt.Sprintf("%02x", int(float64(v)*0.4+0.5))
	}
	return out
}
