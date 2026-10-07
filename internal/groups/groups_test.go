package groups

import (
	"testing"
)

func TestFableQuotaDoesNotGrantEntitlement(t *testing.T) {
	allow := true
	for _, plan := range []string{"pro", "free"} {
		if decision := Entitlement(Fable, Continue, plan, &allow, &allow); !decision.Known || decision.Allowed {
			t.Fatalf("%s gained forbidden Fable access", plan)
		}
	}
	if decision := Entitlement(Fable, Start, "team", nil, &allow); decision.Known || decision.Allowed {
		t.Fatal("continuation evidence granted startup")
	}
	if decision := Entitlement(Fable, Continue, "team", nil, &allow); !decision.Known || !decision.Allowed {
		t.Fatal("explicit continuation evidence was ignored")
	}
	if decision := Entitlement(Fable, Continue, "", nil, nil); decision.Known || decision.Allowed {
		t.Fatal("unknown entitlement was treated as compatible")
	}
}

func TestModelGuardRefusesUnknownAndCrossGroupModels(t *testing.T) {
	for _, model := range []string{"opus", "sonnet", "haiku", "default", "", "fable; command", "claude-fable-5-1\ncommand"} {
		if err := CheckModel(Fable, model); err == nil {
			t.Fatalf("Fable group accepted %q", model)
		}
	}
	for _, model := range []string{"opus", "claude-opus-5-5", "sonnet", "haiku"} {
		if err := CheckModel(Opus, model); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckModel(Opus, "claude-fable-5-1"); err == nil {
		t.Fatal("Opus group accepted Fable")
	}
}
