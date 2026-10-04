package brand

import "testing"

func TestDefaults(t *testing.T) {
	v := Sanitized()
	want := Values{
		Name:               "tycswap",
		DisplayName:        "tycswap",
		SessionCookie:      "tycswap_session",
		RedirectFilePrefix: "tycswap-dashboard-",
		AccentColor:        "#5aa2ff",
		Identifier:         "io.github.tyclab.tycswap",
		EnvPrefix:          "TYCSWAP",
	}
	if v != want {
		t.Fatalf("Sanitized() = %+v, want %+v", v, want)
	}
}

func TestSanitizedFallsBackOnBadOverrides(t *testing.T) {
	saved := []string{Name, DisplayName, SessionCookie, RedirectFilePrefix, AccentColor, Identifier, EnvPrefix}
	t.Cleanup(func() {
		Name, DisplayName, SessionCookie, RedirectFilePrefix, AccentColor = saved[0], saved[1], saved[2], saved[3], saved[4]
		Identifier, EnvPrefix = saved[5], saved[6]
	})
	Name, DisplayName, SessionCookie = "Bad Name", "<b>x</b>", "a;b=c"
	RedirectFilePrefix, AccentColor = "../x", "red;}"
	Identifier, EnvPrefix = "../../x.y", "A B"
	v := Sanitized()
	if v.Name != "tycswap" || v.DisplayName != "tycswap" || v.SessionCookie != "tycswap_session" ||
		v.RedirectFilePrefix != "tycswap-dashboard-" || v.AccentColor != "#5aa2ff" ||
		v.Identifier != "io.github.tyclab.tycswap" || v.EnvPrefix != "TYCSWAP" {
		t.Fatalf("bad overrides leaked through: %+v", v)
	}
	Name, DisplayName, SessionCookie = "example", "Example App", "example_session"
	RedirectFilePrefix, AccentColor = "example-", "#12356F"
	Identifier, EnvPrefix = "org.example.app", "EXAMPLE_APP"
	v = Sanitized()
	if v.Name != "example" || v.DisplayName != "Example App" || v.SessionCookie != "example_session" ||
		v.RedirectFilePrefix != "example-" || v.AccentColor != "#12356F" ||
		v.Identifier != "org.example.app" || v.EnvPrefix != "EXAMPLE_APP" {
		t.Fatalf("good overrides were replaced: %+v", v)
	}
}
