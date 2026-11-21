package web

import (
	"fmt"
	"html"
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
	cmd := exec.CommandContext(t.Context(), chrome, "--headless", "--no-sandbox", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-component-update", "--no-first-run",
		"--user-data-dir="+filepath.Join(t.TempDir(), "profile"), "--window-size=1800,900", "--dump-dom", server.URL+"/header-layout.html")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Chrome: %v", err)
	}
	_, result, _ := strings.Cut(string(out), `<pre id="result">`)
	result, _, _ = strings.Cut(result, "</pre>")
	if !strings.HasPrefix(result, "PASS") {
		t.Fatalf("dashboard header geometry:\n%s", html.UnescapeString(result))
	}
}
