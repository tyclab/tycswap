package lifecycle

import (
	"strings"
	"testing"
)

// TestAddTokenWarnsAboutAPositionalToken: a token given as an argument draws
// the ps/history warning; "-" and the prompt do not.
func TestAddTokenWarnsAboutAPositionalToken(t *testing.T) {
	s := newStore(t)
	out := captureOut(t)
	if err := AddAccountFromToken(s, "sk-ant-oat01-ARG", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "visible to other local users") {
		t.Errorf("no warning for a positional token:\n%s", out.String())
	}

	s2 := newStore(t)
	out2 := captureOut(t)
	withPrompter(t, &fakePrompter{stdin: "sk-ant-oat01-STDIN", secret: "sk-ant-oat01-PROMPT"})
	if err := AddAccountFromToken(s2, "-", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := AddAccountFromToken(s2, "", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out2.String(), "visible to other local users") {
		t.Errorf("warned for stdin/prompt:\n%s", out2.String())
	}
}
