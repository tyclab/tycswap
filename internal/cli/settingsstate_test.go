// The settings facade through the dashboard's state (DESIGN A27): with the
// facade wired and no settings.json at all, /api/state carries every key the
// settings package defines, each at its default and marked so. The Settings
// tab renders from this list alone, so an empty store must not make it
// empty.
package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/web"
)

func TestDashboardStateCarriesEverySettingDefault(t *testing.T) {
	sw := fixtureSwitcher(t)
	root := t.TempDir() // no settings.json here
	srv, err := web.New(web.Deps{
		Facade:        sw,
		Settings:      settingsFacade{root: root},
		Sessions:      func() web.SessionsView { return web.SessionsView{} },
		AuthOverrides: func() web.AuthOverridesView { return web.AuthOverridesView{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx) }()
	defer func() { cancel(); <-served }()

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Get(launch) // redeems the token: cookie + the CSRF fragment
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	base := launch[:strings.Index(launch, "/?token=")]
	req, _ := http.NewRequest(http.MethodGet, base+"/api/state", nil)
	req.Header.Set("X-CSRF-Token", srv.Token())
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st web.State
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("state %d: %v", resp.StatusCode, err)
	}
	if st.Settings == nil {
		t.Fatal("state.settings is null with a facade wired")
	}
	if len(st.Settings) != len(settings.SettingSpecs) {
		t.Fatalf("state carries %d settings, the package defines %d", len(st.Settings), len(settings.SettingSpecs))
	}
	for i, spec := range settings.SettingSpecs {
		got := st.Settings[i]
		if got.Key != spec.Dotted() {
			t.Errorf("settings[%d] = %q, want %q (registry order)", i, got.Key, spec.Dotted())
			continue
		}
		if !got.IsDefault {
			t.Errorf("%s is marked custom with no settings.json", got.Key)
		}
		if got.Applies == "" {
			t.Errorf("%s does not say when a saved value takes effect", got.Key)
		}
		if string(spec.Kind) != got.Kind {
			t.Errorf("%s kind %q, want %q", got.Key, got.Kind, spec.Kind)
		}
		// JSON numbers come back as float64; compare through fmt-free JSON.
		w, _ := json.Marshal(spec.Default)
		v, _ := json.Marshal(got.Value)
		d, _ := json.Marshal(got.Default)
		if string(w) != string(v) || string(w) != string(d) {
			t.Errorf("%s value %s default %s, want %s", got.Key, v, d, w)
		}
		if (spec.Kind == settings.KindFloat || spec.Kind == settings.KindInt) && (got.Min == nil || got.Max == nil) {
			t.Errorf("%s lacks its range", got.Key)
		}
		if spec.Kind == settings.KindChoice && len(got.Choices) != len(spec.Choices) {
			t.Errorf("%s choices %v", got.Key, got.Choices)
		}
	}
}
