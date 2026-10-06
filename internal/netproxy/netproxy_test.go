package netproxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// useSystem clears the proxy environment and makes s the system settings.
func useSystem(t *testing.T, s settings) {
	t.Helper()
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	prev := system
	system = func() settings { return s }
	t.Cleanup(func() { system = prev })
}

func resolveURL(t *testing.T, target string) *url.URL {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := resolve(req)
	if err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	return u
}

// With no proxy in the environment, a request through http.DefaultClient goes
// through the system proxy.
func TestInstallSendsDefaultClientThroughSystemProxy(t *testing.T) {
	var proxied string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = r.URL.String()
	}))
	defer proxy.Close()
	useSystem(t, settings{http: proxy.Listener.Addr().String()})

	tr := http.DefaultTransport.(*http.Transport)
	prev := tr.Proxy
	t.Cleanup(func() { tr.Proxy = prev })
	Install()

	resp, err := http.Get("http://api.tycswap.invalid/usage")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if proxied != "http://api.tycswap.invalid/usage" {
		t.Errorf("proxy saw %q, want the request for api.tycswap.invalid", proxied)
	}
}

// A proxy in the environment wins over the system's, and an invalid one fails
// the request without quoting the value, which may carry a password.
func TestEnvironmentErrorHidesCredentials(t *testing.T) {
	useSystem(t, settings{https: "system.example:3128"})
	const raw = "http://user:s3cret@bad host:3128"
	prev := fromEnv
	fromEnv = func(*http.Request) (*url.URL, error) { return nil, errors.New("invalid proxy address " + raw) }
	t.Cleanup(func() { fromEnv = prev })

	t.Setenv("HTTPS_PROXY", raw)
	req, _ := http.NewRequest(http.MethodGet, "https://api.example/", nil)
	if _, err := resolve(req); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("err = %v, want an error without the password", err)
	}
}

// The system proxy honours its bypass list and NO_PROXY, never proxies
// loopback, and treats a broken setting as direct.
func TestSystemProxyBypass(t *testing.T) {
	useSystem(t, settings{
		https:  "secure.example:3128",
		http:   "http://plain.example:8080",
		bypass: []string{"*.corp.example", "<local>", "10.0.0.0/8", "192.168.*", ".lan.example", "exact.example"},
	})
	t.Setenv("NO_PROXY", "skip.example, 172.16.0.0/12")
	cases := []struct{ target, want string }{
		{"https://api.example/", "http://secure.example:3128"},
		{"http://api.example/", "http://plain.example:8080"},
		{"https://a.corp.example/", ""},
		{"https://buildbox/", ""},
		{"https://10.1.2.3/", ""},
		{"https://11.1.2.3/", "http://secure.example:3128"},
		{"https://192.168.1.4/", ""},
		{"https://lan.example/", ""},
		{"https://x.lan.example/", ""},
		{"https://exact.example/", ""},
		{"https://sub.exact.example/", ""},
		{"https://notexact.example/", "http://secure.example:3128"},
		{"https://skip.example/", ""},
		{"https://172.20.0.1/", ""},
		{"https://localhost:7337/", ""},
		{"https://127.0.0.1/", ""},
		{"https://[::1]:8080/", ""},
	}
	for _, tc := range cases {
		u := resolveURL(t, tc.target)
		got := ""
		if u != nil {
			got = u.String()
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.target, got, tc.want)
		}
	}

	useSystem(t, settings{https: "://:::"})
	if u := resolveURL(t, "https://api.example/"); u != nil {
		t.Errorf("a broken system setting must mean direct, got %v", u)
	}
}

func TestParseScutil(t *testing.T) {
	s := parseScutil(`<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 0
  HTTPProxy : leftover.example
  HTTPSEnable : 1
  HTTPSPort : 3128
  HTTPSProxy : secure.example
  ProxyAutoConfigEnable : 0
  __SCOPED__ : <dictionary> {
    en7 : <dictionary> {
      ExceptionsList : <array> {
        0 : *.scoped
      }
      HTTPSEnable : 0
      HTTPSProxy : scoped.example
    }
  }
}`)
	if s.https != "secure.example:3128" || s.http != "" {
		t.Errorf("https=%q http=%q, want secure.example:3128 and a disabled http proxy", s.https, s.http)
	}
	if got := strings.Join(s.bypass, ","); got != "*.local,169.254/16,<local>" {
		t.Errorf("bypass = %q", got)
	}
	if s := parseScutil("<dictionary> {\n}"); s.https != "" || s.http != "" || len(s.bypass) != 0 {
		t.Errorf("empty dictionary = %+v", s)
	}
}

func TestParseProxyServer(t *testing.T) {
	cases := []struct{ in, https, http string }{
		{"proxy.example:8080", "proxy.example:8080", "proxy.example:8080"},
		{"http=plain.example:8080;https=secure.example:3128", "secure.example:3128", "plain.example:8080"},
		{"http=plain.example:8080", "", "plain.example:8080"},
		{"ftp=ftp.example:21", "", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		if https, http := parseProxyServer(tc.in); https != tc.https || http != tc.http {
			t.Errorf("%q: got (%q, %q), want (%q, %q)", tc.in, https, http, tc.https, tc.http)
		}
	}
}
