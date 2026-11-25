package web

import (
	"regexp"
	"strings"
	"testing"
)

// The header bar is dark in both themes, so a status chip in it (at limit,
// a Fable sign-in) takes the dark surface's text colours, never the light
// theme's dark inks.
func TestHeaderStatusChipsUseDarkSurfaceInks(t *testing.T) {
	css := staticFile(t, "style.css")
	block := func(open string) string {
		t.Helper()
		_, rest, ok := strings.Cut(css, open)
		if !ok {
			t.Fatalf("style.css has no %q", open)
		}
		body, _, _ := strings.Cut(rest, "\n}")
		return body
	}
	value := func(body, token string) string {
		m := regexp.MustCompile(regexp.QuoteMeta(token) + `:\s*([^;]+);`).FindStringSubmatch(body)
		if m == nil {
			return ""
		}
		return strings.TrimSpace(m[1])
	}
	light, dark, bar := block("\n:root {"), block("\n:root[data-theme=\"dark\"] {"), block("\n.top {")
	for _, token := range []string{"--good-ink", "--warn-ink", "--serious-ink", "--crit-ink"} {
		want := value(dark, token)
		if want == "" || want == value(light, token) {
			t.Fatalf("%s: dark theme %q, light theme %q", token, want, value(light, token))
		}
		if got := value(bar, token); got != want {
			t.Errorf("header %s = %q, want the dark surface's %q", token, got, want)
		}
	}
}
