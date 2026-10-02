package ccversion

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		real string
		want Method
		cask string
	}{
		{"/home/u/.local/bin/claude", Native, ""},
		{"/home/u/.local/share/claude/versions/2.1.0/claude", Native, ""},
		{"/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js", NPM, ""},
		{"/home/u/.claude/local/node_modules/.bin/claude", NPM, ""},
		{`C:\Users\u\AppData\Roaming\npm\claude.cmd`, NPM, ""},
		{"/opt/homebrew/Caskroom/claude-code/2.1.0/claude", Homebrew, "claude-code"},
		{"/usr/local/Caskroom/claude-code@latest/2.1.0/claude", Homebrew, "claude-code@latest"},
		{`C:\Users\u\AppData\Local\Microsoft\WinGet\Packages\Anthropic.ClaudeCode\claude.exe`, WinGet, ""},
		{"/usr/bin/claude", Unknown, ""},
	} {
		m, cask := Classify(tc.real)
		if m != tc.want || cask != tc.cask {
			t.Errorf("Classify(%q) = %s %q, want %s %q", tc.real, m, cask, tc.want, tc.cask)
		}
	}
}

func TestParseVersionAndNewer(t *testing.T) {
	if got := ParseVersion("2.1.280 (Claude Code)"); got != "2.1.280" {
		t.Errorf("ParseVersion = %q", got)
	}
	if got := ParseVersion("no version here"); got != "" {
		t.Errorf("ParseVersion = %q, want empty", got)
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"2.1.1", "2.1.0", true}, {"2.1.0", "2.1.0", false}, {"2.0.9", "2.1.0", false},
		{"2.1.0", "2.1.0-rc.1", true}, {"v2.1.1", "2.1.0", true}, {"junk", "2.1.0", false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%q,%q) = %v", tc.a, tc.b, got)
		}
	}
}

func TestCommandString(t *testing.T) {
	if got := (Command{Argv: []string{"/opt/homebrew/bin/brew", "upgrade", "--cask", "claude-code"}}).String(); got != "brew upgrade --cask claude-code" {
		t.Errorf("String = %q", got)
	}
	if got := (Command{Argv: []string{`C:\tools\npm.exe`, "install", "-g", npmPackage}}).String(); got != "npm install -g "+npmPackage {
		t.Errorf("String = %q", got)
	}
	if got := (Command{}).String(); got != "" {
		t.Errorf("empty String = %q", got)
	}
}

func TestUpgradeCommandPerMethod(t *testing.T) {
	for _, tc := range []struct {
		in   *Installed
		want string
	}{
		{nil, ""},
		{&Installed{Method: Native}, "claude update"},
		{&Installed{Method: Unknown}, "claude update"},
		{&Installed{Method: NPM}, "npm install -g " + npmPackage},
		{&Installed{Method: Homebrew, Cask: "claude-code@latest"}, "brew upgrade --cask claude-code@latest"},
		{&Installed{Method: Homebrew}, "brew upgrade --cask claude-code"},
		{&Installed{Method: WinGet}, "winget upgrade --id Anthropic.ClaudeCode --exact"},
	} {
		if got := UpgradeCommand(tc.in).String(); got != tc.want {
			t.Errorf("UpgradeCommand(%+v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if !strings.Contains(InstallHint("linux"), "install.sh") || !strings.Contains(InstallHint("windows"), "install.ps1") {
		t.Error("InstallHint does not name Claude Code's installers")
	}
}

// fakeEnv is a machine with one claude at path (or none), answering
// --version with versionOut.
func fakeEnv(t *testing.T, path, versionOut string, runErr error) Env {
	t.Helper()
	home := t.TempDir()
	return Env{
		GOOS: "linux", Home: home,
		Getenv:   func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", errors.New("not on PATH") },
		Exists:   func(p string) bool { return path != "" && p == path },
		Realpath: func(p string) (string, error) { return p, nil },
		Run: func(_ context.Context, argv []string) (string, error) {
			if len(argv) == 2 && argv[0] == path && argv[1] == "--version" {
				return versionOut, runErr
			}
			return "ran " + strings.Join(argv, " "), nil
		},
		Endpoints: DefaultEndpoints,
	}
}

func TestLocateAndFind(t *testing.T) {
	e := fakeEnv(t, "", "", nil)
	if p := Locate(e); p != "" {
		t.Fatalf("Locate on an empty machine = %q", p)
	}
	in, err := Find(context.Background(), e)
	if in != nil || err != nil {
		t.Fatalf("Find on an empty machine = %+v, %v", in, err)
	}

	home := e.Home
	path := filepath.Join(home, ".local", "bin", "claude")
	e = fakeEnv(t, path, "2.1.280 (Claude Code)\n", nil)
	e.Home = home // candidates() looks under the home directory
	in, err = Find(context.Background(), e)
	if err != nil || in == nil {
		t.Fatalf("Find: %+v, %v", in, err)
	}
	if in.Version != "2.1.280" || in.Method != Native || in.Path != path {
		t.Errorf("Find = %+v", in)
	}

	// PATH wins over the candidates.
	e.LookPath = func(string) (string, error) { return "/usr/bin/claude", nil }
	if p := Locate(e); p != "/usr/bin/claude" {
		t.Errorf("Locate = %q, want the PATH entry", p)
	}

	// --version fails and prints no version: the install is reported with
	// the error, so the caller can still name it.
	e = fakeEnv(t, path, "", errors.New("exit status 1"))
	e.Home = home
	in, err = Find(context.Background(), e)
	if err == nil || in == nil || in.Version != "" {
		t.Errorf("Find with a failing --version = %+v, %v", in, err)
	}
}

// endpoints serves the three sources from one test server.
func endpoints(t *testing.T, downloads, cask, npm string) Endpoints {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/dl/latest":
			if downloads == "" {
				http.Error(w, "nope", http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(downloads + "\n"))
		case strings.HasPrefix(r.URL.Path, "/cask/"):
			_, _ = w.Write([]byte(cask))
		case r.URL.Path == "/npm":
			_, _ = w.Write([]byte(npm))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return Endpoints{Downloads: srv.URL + "/dl", Homebrew: srv.URL + "/cask", NPM: srv.URL + "/npm"}
}

func TestLatestPerMethod(t *testing.T) {
	e := Env{HTTP: http.DefaultClient, Endpoints: endpoints(t, "2.2.0", `{"version":"2.1.9,123"}`, `{"latest":"2.1.5"}`)}
	ctx := context.Background()
	for _, tc := range []struct {
		in   *Installed
		want string
	}{
		{&Installed{Method: Native}, "2.2.0"},
		{&Installed{Method: Unknown}, "2.2.0"},
		{&Installed{Method: WinGet}, "2.2.0"},
		{nil, "2.2.0"},
		{&Installed{Method: Homebrew, Cask: "claude-code"}, "2.1.9"},
		{&Installed{Method: NPM}, "2.1.5"},
	} {
		got, err := Latest(ctx, e, tc.in)
		if err != nil || got != tc.want {
			t.Errorf("Latest(%+v) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestLatestErrors(t *testing.T) {
	ctx := context.Background()
	e := Env{HTTP: http.DefaultClient, Endpoints: endpoints(t, "", `not json`, `{"latest":"x.y"}`)}
	if _, err := Latest(ctx, e, &Installed{Method: Native}); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("native non-200: %v", err)
	}
	if _, err := Latest(ctx, e, &Installed{Method: Homebrew}); err == nil {
		t.Error("cask bad JSON: want an error")
	}
	if _, err := Latest(ctx, e, &Installed{Method: NPM}); err == nil || !strings.Contains(err.Error(), "unexpected version") {
		t.Errorf("npm odd version: %v", err)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	e = Env{Endpoints: Endpoints{Downloads: url}}
	if _, err := Latest(ctx, e, nil); err == nil {
		t.Error("closed port: want an error")
	}
}

func TestCheckUsesNativeUserChannel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings string
		custom   bool
		channel  string
		latest   string
		state    State
	}{
		{"missing file", "", false, "latest", "2.2.0", UpdateAvailable},
		{"missing setting", `{}`, false, "latest", "2.2.0", UpdateAvailable},
		{"explicit latest", `{"autoUpdatesChannel":"latest"}`, false, "latest", "2.2.0", UpdateAvailable},
		{"stable", `{"autoUpdatesChannel":"stable"}`, false, "stable", "2.1.0", UpToDate},
		{"custom config directory", `{"autoUpdatesChannel":"stable"}`, true, "stable", "2.1.0", UpToDate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fakeEnv(t, "", "", nil)
			path := filepath.Join(e.Home, ".local", "bin", "claude")
			e.LookPath = func(string) (string, error) { return path, nil }
			e.Run = func(context.Context, []string) (string, error) { return "2.1.0 (Claude Code)", nil }
			dir := filepath.Join(e.Home, ".claude")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.custom {
				// The override must replace, not merge with, ~/.claude.
				if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"autoUpdatesChannel":"latest"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				dir = t.TempDir()
				e.Getenv = func(k string) string {
					if k == "CLAUDE_CONFIG_DIR" {
						return dir
					}
					return ""
				}
			}
			if tc.settings != "" {
				if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(tc.settings), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/"+tc.channel {
					t.Errorf("requested %s, want /%s", r.URL.Path, tc.channel)
				}
				if r.URL.Path == "/stable" {
					_, _ = w.Write([]byte("2.1.0\n"))
				} else {
					_, _ = w.Write([]byte("2.2.0\n"))
				}
			}))
			defer srv.Close()
			e.Endpoints.Downloads = srv.URL
			st := Check(context.Background(), e)
			if st.Err != nil || st.State() != tc.state || st.Latest != tc.latest || st.Installed.Channel != tc.channel {
				t.Fatalf("Check = %+v, installed %+v, state %v", st, st.Installed, st.State())
			}
		})
	}
}

func TestInvalidChannelSettingsDoNotRequestLatest(t *testing.T) {
	for _, raw := range []string{`{`, `null`, `[]`, `{"autoUpdatesChannel":null}`, `{"autoUpdatesChannel":false}`, `{"autoUpdatesChannel":"preview"}`} {
		t.Run(raw, func(t *testing.T) {
			e := Env{Home: t.TempDir(), ReadFile: func(string) ([]byte, error) { return []byte(raw), nil }}
			// No endpoint is configured: an invalid setting must fail before HTTP.
			_, err := Latest(context.Background(), e, &Installed{Method: Native})
			if err == nil || !strings.Contains(err.Error(), "update channel") {
				t.Fatalf("Latest with %q: %v", raw, err)
			}
		})
	}
	e := Env{Home: t.TempDir(), ReadFile: func(string) ([]byte, error) { return nil, os.ErrPermission }}
	if _, err := Latest(context.Background(), e, &Installed{Method: Native}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unreadable channel: %v", err)
	}
}

func TestCheckStates(t *testing.T) {
	ctx := context.Background()
	if st := (Status{}); st.State() != Checking {
		t.Errorf("zero Status = %v, want Checking", st.State())
	}

	// No Claude Code: Missing, no network asked.
	e := fakeEnv(t, "", "", nil)
	e.Endpoints = Endpoints{Downloads: "http://127.0.0.1:1/never"}
	st := Check(ctx, e)
	if st.State() != Missing || st.Err != nil || st.Latest != "" {
		t.Errorf("missing: %+v state %v", st, st.State())
	}

	home := t.TempDir()
	path := filepath.Join(home, ".local", "bin", "claude")
	mk := func(versionOut string, runErr error, downloads string) Env {
		e := fakeEnv(t, path, versionOut, runErr)
		e.Home = home // candidates() looks under the home directory
		e.HTTP = http.DefaultClient
		e.Endpoints = endpoints(t, downloads, "{}", "{}")
		return e
	}
	st = Check(ctx, mk("2.1.0 (Claude Code)", nil, "2.2.0"))
	if st.State() != UpdateAvailable || st.Latest != "2.2.0" || st.Err != nil {
		t.Errorf("update: %+v state %v", st, st.State())
	}
	st = Check(ctx, mk("2.2.0 (Claude Code)", nil, "2.2.0"))
	if st.State() != UpToDate {
		t.Errorf("latest: state %v", st.State())
	}
	st = Check(ctx, mk("2.1.0 (Claude Code)", nil, ""))
	if st.State() != CannotCheck || st.Err == nil {
		t.Errorf("source down: %+v state %v", st, st.State())
	}
	st = Check(ctx, mk("", errors.New("boom"), "2.2.0"))
	if st.State() != CannotCheck || st.Err == nil || !strings.Contains(st.Err.Error(), "boom") {
		t.Errorf("version unknown: %+v state %v", st, st.State())
	}
}

func TestRunSubstitutesTheBinary(t *testing.T) {
	var got []string
	e := Env{Run: func(_ context.Context, argv []string) (string, error) { got = argv; return "ok", nil }}
	out, err := Run(context.Background(), e, Command{Argv: []string{"claude", "update"}}, &Installed{Path: "/x/claude"})
	if err != nil || out != "ok" || !reflect.DeepEqual(got, []string{"/x/claude", "update"}) {
		t.Errorf("Run = %q, %v, argv %v", out, err, got)
	}
	_, _ = Run(context.Background(), e, Command{Argv: []string{"npm", "install"}}, &Installed{Path: "/x/claude"})
	if !reflect.DeepEqual(got, []string{"npm", "install"}) {
		t.Errorf("another program was substituted: %v", got)
	}
	if _, err := Run(context.Background(), e, Command{}, nil); err == nil {
		t.Error("empty command: want an error")
	}
}
