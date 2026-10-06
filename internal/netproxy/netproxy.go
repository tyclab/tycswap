// Package netproxy falls back to the operating system's proxy when the
// environment names none (DESIGN A58).
package netproxy

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
)

// settings is one system proxy configuration.
type settings struct {
	https, http string   // proxy per target scheme ("host:port" or a URL); "" is direct
	bypass      []string // hosts that go direct
}

// Seams for tests.
var (
	fromEnv = http.ProxyFromEnvironment
	system  = sync.OnceValue(readSystem)
)

// envKeys are the variables http.ProxyFromEnvironment reads a proxy from.
var envKeys = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"}

// Install points http.DefaultTransport's proxy at resolve.
func Install() {
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.Proxy = resolve
	}
}

// resolve is an http.Transport Proxy func: the environment when it names a
// proxy, the system settings otherwise.
func resolve(req *http.Request) (*url.URL, error) {
	for _, k := range envKeys {
		if os.Getenv(k) == "" {
			continue
		}
		u, err := fromEnv(req)
		if err != nil {
			// Go's message quotes the raw value, password included, and
			// request errors end up in the log.
			return nil, errors.New("the proxy in HTTPS_PROXY or HTTP_PROXY is not a valid URL")
		}
		return u, nil
	}
	noProxy := os.Getenv("NO_PROXY")
	if noProxy == "" {
		noProxy = os.Getenv("no_proxy")
	}
	return system().proxyFor(req.URL, strings.Split(noProxy, ",")), nil
}

// proxyFor picks the proxy for target, or nil for a direct connection.
// Loopback is never proxied, as in Go's own resolver, and NO_PROXY applies on
// top of the system's bypass list.
func (s settings) proxyFor(target *url.URL, noProxy []string) *url.URL {
	host := strings.ToLower(strings.TrimSuffix(target.Hostname(), "."))
	if host == "localhost" || net.ParseIP(host).IsLoopback() {
		return nil
	}
	raw := s.https
	if target.Scheme == "http" {
		raw = s.http
	}
	if raw == "" || bypassed(host, s.bypass) || bypassed(host, noProxy) {
		return nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil // a broken setting is a direct connection, never an error quoting it
	}
	return u
}

// bypassed matches host against a bypass list. An entry is "<local>" (a name
// without a dot), a CIDR block, a glob ("*", "*.example.com", "10.*"), or a
// host, with or without a leading dot, that covers its subdomains too.
func bypassed(host string, list []string) bool {
	ip := net.ParseIP(host)
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
		case e == "<local>":
			if ip == nil && !strings.Contains(host, ".") {
				return true
			}
		case strings.Contains(e, "/"):
			if _, n, err := net.ParseCIDR(e); err == nil && ip != nil && n.Contains(ip) {
				return true
			}
		case strings.Contains(e, "*"):
			if ok, _ := path.Match(e, host); ok {
				return true
			}
		default:
			e = strings.TrimPrefix(e, ".")
			if host == e || strings.HasSuffix(host, "."+e) {
				return true
			}
		}
	}
	return false
}

// parseScutil reads `scutil --proxy` output:
//
//	<dictionary> {
//	  ExceptionsList : <array> {
//	    0 : *.local
//	  }
//	  HTTPSEnable : 1
//	  HTTPSPort : 3128
//	  HTTPSProxy : proxy.example.com
//	}
func parseScutil(out string) settings {
	var s settings
	f := map[string]string{}
	// Only the top-level dictionary counts: the per-interface copies under
	// __SCOPED__ repeat every key and would overwrite the primary service's.
	depth, inList := 0, false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, " : ")
		switch {
		case line == "}":
			depth--
			inList = false
		case !ok:
		case inList:
			s.bypass = append(s.bypass, v)
		case depth != 1:
		case k == "ExceptionsList":
			inList = true
		default:
			f[k] = v
		}
		if strings.HasSuffix(line, "{") {
			depth++
		}
	}
	proxy := func(p string) string {
		if f[p+"Enable"] != "1" || f[p+"Proxy"] == "" {
			return ""
		}
		if f[p+"Port"] == "" {
			return f[p+"Proxy"]
		}
		return net.JoinHostPort(f[p+"Proxy"], f[p+"Port"])
	}
	s.https, s.http = proxy("HTTPS"), proxy("HTTP")
	if f["ExcludeSimpleHostnames"] == "1" {
		s.bypass = append(s.bypass, "<local>")
	}
	return s
}

// parseProxyServer reads Windows' ProxyServer value: one "host:port" for
// every scheme, or a per-scheme list ("http=a:8080;https=b:3128").
func parseProxyServer(v string) (httpsProxy, httpProxy string) {
	if !strings.Contains(v, "=") {
		v = strings.TrimSpace(v)
		return v, v
	}
	for _, part := range strings.Split(v, ";") {
		scheme, addr, _ := strings.Cut(part, "=")
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "https":
			httpsProxy = strings.TrimSpace(addr)
		case "http":
			httpProxy = strings.TrimSpace(addr)
		}
	}
	return httpsProxy, httpProxy
}
