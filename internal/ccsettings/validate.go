// validate.go — what an endpoint account may carry: a base URL and a key that
// are safe to write into Claude Code's settings.json and to send as a header
// (DESIGN A46).
package ccsettings

import (
	"fmt"
	"net/url"
	"strings"
)

// MaxBaseURLLen and MaxTokenLen bound what is stored and written. Real values
// are a fraction of either.
const (
	MaxBaseURLLen = 2048
	MaxTokenLen   = 8192
)

// ValidateBaseURL checks a base URL an API-key account is to carry and returns
// it trimmed. It must be an absolute http or https URL with a host, and carry
// nothing that is not an address: no user info (a password in a URL ends up
// in every listing and log line), no query and no fragment (Claude Code
// appends its own paths to the base URL, so either would end up in the middle
// of every request URL). Control characters and whitespace are refused too.
// This is the generic part of the reference's origin rule; which hosts are
// allowed is the user's call, not a list here.
func ValidateBaseURL(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", fmt.Errorf("base URL is empty")
	}
	if len(v) > MaxBaseURLLen {
		return "", fmt.Errorf("base URL is longer than %d bytes", MaxBaseURLLen)
	}
	for _, r := range v {
		if r <= ' ' || r == 0x7f {
			return "", fmt.Errorf("base URL contains whitespace or a control character")
		}
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", fmt.Errorf("base URL does not parse: %v", err)
	}
	switch {
	case u.Scheme != "https" && u.Scheme != "http":
		return "", fmt.Errorf("base URL must start with https:// or http://")
	case u.Opaque != "" || u.Host == "" || u.Hostname() == "":
		return "", fmt.Errorf("base URL must be absolute, with a host (https://host[:port][/path])")
	case u.User != nil:
		return "", fmt.Errorf("base URL must not carry a user name or password")
	case u.RawQuery != "" || u.ForceQuery:
		return "", fmt.Errorf("base URL must not carry a query (?…)")
	case u.Fragment != "" || strings.Contains(v, "#"):
		return "", fmt.Errorf("base URL must not carry a fragment (#…)")
	}
	return v, nil
}

// Host is the host (and port, when one is given) of a base URL, which is what
// the human listings show; "" when it does not parse.
func Host(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return u.Host
}

// ValidateToken checks the key an endpoint account sends and returns it
// trimmed. The key is written as env.ANTHROPIC_AUTH_TOKEN and sent as an HTTP
// header, so it is one run of printable ASCII: no whitespace, no control or
// non-ASCII character. A JSON object is refused because it is an OAuth
// credential, not a key. Its shape is otherwise not checked: a gateway mints
// keys in its own format.
func ValidateToken(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", fmt.Errorf("the key is empty")
	}
	if len(v) > MaxTokenLen {
		return "", fmt.Errorf("the key is longer than %d bytes", MaxTokenLen)
	}
	if strings.HasPrefix(v, "{") {
		return "", fmt.Errorf("the key is a JSON object (an OAuth credential), not a key")
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; c <= ' ' || c >= 0x7f {
			return "", fmt.Errorf("the key contains whitespace, a control character or a non-ASCII character")
		}
	}
	return v, nil
}
