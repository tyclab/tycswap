// hardening_test.go — the importer's identity check and alias normalisation.

package registryimport

import "testing"

func TestARowWhoseSnapshotBelongsToAnotherAccountIsSkipped(t *testing.T) {
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro"), seed(keyB, "b@x", "pro")}, 4)
	e.snapshot(keyA, makeAuthJSONFor(keyB, "b@x")) // keyA's file holds keyB's tokens
	e.snapshot(keyB, nil)
	r := e.run(false)
	if r.Imported != 1 || r.Skipped != 1 {
		t.Fatalf("result = %+v, want 1 imported, 1 skipped", r)
	}
	st := e.open()
	if st.SlotForKey(keyA) != nil || st.ReadSnapshot(keyA) != nil {
		t.Fatal("mismatched row was imported")
	}
	if st.SlotForKey(keyB) == nil {
		t.Fatalf("slots = %+v", st.Slots())
	}
}

func TestImportedAliasesAreNormalisedAndInvalidOnesDropped(t *testing.T) {
	e := newEnv(t)
	a, b := seed(keyA, "a@x", "pro"), seed(keyB, "b@x", "pro")
	a["alias"], b["alias"] = "Team.X", "2"
	e.writeRegistry([]map[string]any{a, b}, 4)
	e.snapshot(keyA, nil)
	e.snapshot(keyB, nil)
	if r := e.run(false); r.Imported != 2 {
		t.Fatalf("result = %+v", r)
	}
	st := e.open()
	if got := st.SlotForKey(keyA).Alias; got != "team.x" {
		t.Fatalf("alias A = %q", got)
	}
	if got := st.SlotForKey(keyB).Alias; got != "" {
		t.Fatalf("alias B = %q, want dropped", got)
	}
}
