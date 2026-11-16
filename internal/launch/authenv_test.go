package launch

import (
	"reflect"
	"testing"
)

func TestAuthOverrideEnvVarsExactList(t *testing.T) {
	want := []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
		"CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR",
		"CLAUDE_SECURESTORAGE_CONFIG_DIR",
	}
	if !reflect.DeepEqual(AuthOverrideEnvVars, want) {
		t.Fatalf("AuthOverrideEnvVars = %v, want %v", AuthOverrideEnvVars, want)
	}
}

func TestScrubAuthOverrides(t *testing.T) {
	in := []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=secret", "FOO=bar", "CLAUDE_CODE_OAUTH_TOKEN=tok", "CLAUDE_SECURESTORAGE_CONFIG_DIR=", "BARE"}
	got := ScrubAuthOverrides(in)
	if want := []string{"PATH=/usr/bin", "FOO=bar", "BARE"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ScrubAuthOverrides = %v, want %v", got, want)
	}
	if in[1] != "ANTHROPIC_API_KEY=secret" {
		t.Fatal("ScrubAuthOverrides changed its input")
	}
}
