package web

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboardHeaderLayout(t *testing.T) {
	chrome := os.Getenv("CHROME_BIN")
	if chrome == "" {
		for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
			if path, err := exec.LookPath(name); err == nil {
				chrome = path
				break
			}
		}
	}
	if chrome == "" {
		t.Skip("set CHROME_BIN or install Chromium to measure dashboard layout")
	}

	page := strings.NewReplacer("{{.DisplayName}}", "TycSwap", "{{.Name}}", "tycswap",
		"</head>", `<script>window.EventSource = class extends EventTarget { constructor() { super(); window.events = this; } };</script></head>`).Replace(staticFile(t, "index.html"))
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(staticFS)))
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, page)
	})
	mux.Handle("/", http.FileServer(http.Dir("testdata")))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	type measurement struct {
		Font, Viewport, Zoom, WrapRem float64
		Type                          map[string]float64
		Failures                      []string
	}
	var standard measurement
	for _, tc := range []struct{ font, zoom int }{{16, 1}, {20, 1}, {32, 1}, {16, 2}} {
		t.Run(fmt.Sprintf("font%d-zoom%d", tc.font, tc.zoom), func(t *testing.T) {
			profile := t.TempDir()
			if err := os.Mkdir(filepath.Join(profile, "Default"), 0700); err != nil {
				t.Fatal(err)
			}
			// Chromium stores page zoom as powers of 1.2 in the default storage partition.
			prefs := fmt.Sprintf(`{"webkit":{"webprefs":{"default_font_size":%d}},"partition":{"default_zoom_level":{"x":%g}}}`,
				tc.font, math.Log(float64(tc.zoom))/math.Log(1.2))
			if err := os.WriteFile(filepath.Join(profile, "Default", "Preferences"), []byte(prefs), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), chrome, "--headless", "--no-sandbox", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-component-update", "--no-first-run",
				"--user-data-dir="+profile, "--force-device-scale-factor=1", "--window-size=1280,900", "--dump-dom", server.URL+"/header-layout.html")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("Chrome: %v", err)
			}
			_, result, _ := strings.Cut(string(out), `<pre id="result" hidden="">`)
			result, _, _ = strings.Cut(result, "</pre>")
			var got measurement
			if err := json.Unmarshal([]byte(html.UnescapeString(result)), &got); err != nil {
				t.Fatalf("browser measurement: %v: %s", err, result)
			}
			if got.Font != float64(tc.font) || got.Zoom != float64(tc.zoom) || got.Viewport != float64(1280/tc.zoom) {
				t.Fatalf("browser settings were not applied: font %g, devicePixelRatio %g, viewport %g", got.Font, got.Zoom, got.Viewport)
			}
			for _, problem := range got.Failures {
				t.Error(problem)
			}
			if tc.font == 16 && tc.zoom == 1 {
				standard = got
				return
			}
			// The search resolves the transition to one rem.
			if math.Abs(got.WrapRem-standard.WrapRem) > 1 {
				t.Errorf("accounts fit beside the brand at %g rem, default %g rem", got.WrapRem, standard.WrapRem)
			}
			for label, ratio := range standard.Type {
				if math.Abs(got.Type[label]-ratio) > 0.01 {
					t.Errorf("%s type is %.3g rem, default %.3g rem", label, got.Type[label], ratio)
				}
			}
		})
	}
}
