package transfer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportValidatesEveryRowFirst: a bad email, alias or account key in any
// row refuses the whole import before anything is written, as the Claude
// import does.
func TestImportValidatesEveryRowFirst(t *testing.T) {
	good := `{"accountKey":"` + keyA + `","email":"a@example.com","auth":{"x":1}}`
	for _, c := range []struct{ name, bad, want string }{
		{"traversal email", `{"accountKey":"k2","email":"a/../../x","auth":{"x":1}}`, "invalid email"},
		{"newline email", `{"accountKey":"k2","email":"b@example.com\n","auth":{"x":1}}`, "invalid email"},
		{"alias with CR", `{"accountKey":"k2","alias":"work\r","auth":{"x":1}}`, "control character"},
		{"long alias", `{"accountKey":"k2","alias":"` + strings.Repeat("a", 65) + `","auth":{"x":1}}`, "longer than 64"},
		{"key with ESC", `{"accountKey":"k\u001b2","auth":{"x":1}}`, "control character"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			p := filepath.Join(e.dir, "in.json")
			if err := os.WriteFile(p, []byte(`{"accounts":[`+good+`,`+c.bad+`]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Import(e.open(), p, false, nil)
			wantTransferErr(t, err, c.want)
			if n := len(e.open().Slots()); n != 0 {
				t.Fatalf("slots = %d, want nothing written", n)
			}
		})
	}
}

func TestImportStripsControlCharactersFromDisplayFields(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(e.dir, "in.json")
	body := `{"accounts":[{"accountKey":"` + keyA + `","plan":"pro\u001b[2J","workspaceName":"W\u009b31m","auth":{"x":1}}]}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(e.open(), p, false, nil); err != nil {
		t.Fatal(err)
	}
	s := e.open().Slots()[0]
	if s.Plan != "pro[2J" || s.WorkspaceName != "W31m" {
		t.Fatalf("plan=%q workspace=%q", s.Plan, s.WorkspaceName)
	}
}
