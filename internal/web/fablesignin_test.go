// Tests for the accounts that need a Fable sign-in on the dashboard (DESIGN
// A115).
package web

import (
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The state's fable object carries the set the snapshot names.
func TestStateCarriesTheFableSignInSet(t *testing.T) {
	h := newHarness(t, withFable(&fakeFable{}), func(h *harness, d *Deps) {
		h.fa.snap.Fable["withoutLogin"] = []int{3, 5}
	})
	fable, _ := stateOf(t, h)["fable"].(map[string]any)
	if got := fable["withoutLogin"]; !reflect.DeepEqual(got, []any{3.0, 5.0}) {
		t.Fatalf("fable.withoutLogin = %v, want [3 5]", got)
	}
}

// The header chip, the notice above the accounts and the row chip, over the
// node harness of the Fable page; the notice's command is markup beside the
// auth-overrides notice, and its warn level comes from the colour variables.
func TestAppJSFableSignIn(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; Fable sign-in page test skipped")
	}
	if out, err := exec.Command(node, "testdata/fable.cjs", "static/app.js", "signin").CombinedOutput(); err != nil {
		t.Errorf("Fable sign-in page: %v\n%s", err, out)
	}
	html := staticFile(t, "index.html")
	auth, updates, notice := strings.Index(html, `id="auth-overrides"`), strings.Index(html, `id="updates"`), strings.Index(html, `id="fable-signin"`)
	if auth < 0 || notice < auth || notice > updates {
		t.Fatalf("the sign-in notice is not beside the auth-overrides notice (auth-overrides %d, notice %d, updates %d)", auth, notice, updates)
	}
	start := strings.LastIndex(html[:notice], "<")
	element := html[start : start+strings.Index(html[start:], "</div>")]
	for _, want := range []string{`class="notice notice-warn"`, ` hidden`, `id="fable-signin-text"`, `<code>{{.Name}} fable --login</code>`} {
		if !strings.Contains(element, want) {
			t.Errorf("the sign-in notice lacks %s: %s", want, element)
		}
	}
	guide := html[strings.Index(html, `id="g-trouble"`):]
	if c := regexp.MustCompile(`<li class="g-case">[^\n]*need a Fable sign-in[^\n]*</li>`).FindString(guide); !strings.Contains(c, "{{.Name}} fable --login") {
		t.Errorf("the Guide's Something looks wrong has no case for the sign-in: %q", c)
	}
	rule := regexp.MustCompile(`(?m)^\.notice-warn \{[^}]*\}`).FindString(staticFile(t, "style.css"))
	if !strings.Contains(rule, "var(--warn)") || regexp.MustCompile(`\dpx`).MatchString(rule) {
		t.Errorf(".notice-warn = %q, want the warn colour variable and no pixel size", rule)
	}
}
