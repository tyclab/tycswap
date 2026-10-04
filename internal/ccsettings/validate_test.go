package ccsettings

import (
	"strings"
	"testing"
)

func TestValidateBaseURL(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"https://gw.example.com", "https://gw.example.com", true},
		{"  https://gw.example.com/anthropic/  ", "https://gw.example.com/anthropic/", true},
		{"http://localhost:4000", "http://localhost:4000", true},
		{"https://10.0.0.5:8443/v1", "https://10.0.0.5:8443/v1", true},
		{"https://[::1]:4000", "https://[::1]:4000", true},
		{"", "", false},
		{"gw.example.com", "", false},
		{"//gw.example.com", "", false},
		{"ftp://gw.example.com", "", false},
		{"https:gw.example.com", "", false},
		{"https://", "", false},
		{"https://:443", "", false},
		{"https://user:pass@gw.example.com", "", false},
		{"https://user@gw.example.com", "", false},
		{"https://gw.example.com/?key=x", "", false},
		{"https://gw.example.com/?", "", false},
		{"https://gw.example.com/#x", "", false},
		{"https://gw.example.com/#", "", false},
		{"https://gw.example.com/a b", "", false},
		{"https://gw.example.com/\x1b[31m", "", false},
		{"https://gw.example.com/" + strings.Repeat("a", MaxBaseURLLen), "", false},
	} {
		got, err := ValidateBaseURL(tc.in)
		if tc.ok != (err == nil) || got != tc.want {
			t.Errorf("ValidateBaseURL(%q) = %q, %v; want %q, ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://gw.example.com/anthropic": "gw.example.com",
		"http://localhost:4000":            "localhost:4000",
		"https://[::1]:4000/x":             "[::1]:4000",
		"%zz":                              "",
	} {
		if got := Host(in); got != want {
			t.Errorf("Host(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateToken(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"sk-ant-api03-abc", "sk-ant-api03-abc", true},
		{"  sk-gw-0123456789  \n", "sk-gw-0123456789", true},
		{"eyJhbGciOi.payload.sig", "eyJhbGciOi.payload.sig", true},
		{"", "", false},
		{"   ", "", false},
		{`{"claudeAiOauth":{}}`, "", false},
		{"two words", "", false},
		{"tab\tinside", "", false},
		{"line\nbreak", "", false},
		{"esc\x1b", "", false},
		{"del\x7f", "", false},
		{"ключ", "", false},
		{strings.Repeat("k", MaxTokenLen+1), "", false},
	} {
		got, err := ValidateToken(tc.in)
		if tc.ok != (err == nil) || got != tc.want {
			t.Errorf("ValidateToken(%q) = %q, %v; want %q, ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}
