package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/testutil"
)

func writeSettingsJSON(t *testing.T, root, content string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SettingsPath(root), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func strp(s string) *string { return &s }

// --- Load: missing/corrupt/non-object handling ---

func TestLoad_MissingFileGivesDefaults(t *testing.T) {
	got := Load(t.TempDir())
	if !reflect.DeepEqual(got, Default()) {
		t.Errorf("Load(missing) = %+v, want defaults %+v", got, Default())
	}
}

func TestLoad_CorruptFileGivesDefaults(t *testing.T) {
	root := t.TempDir()
	writeSettingsJSON(t, root, "{not json")
	got := Load(root)
	if !reflect.DeepEqual(got, Default()) {
		t.Errorf("Load(corrupt) = %+v, want defaults", got)
	}
}

func TestLoad_NonObjectRootGivesDefaults(t *testing.T) {
	root := t.TempDir()
	writeSettingsJSON(t, root, "[1, 2]")
	got := Load(root)
	if !reflect.DeepEqual(got, Default()) {
		t.Errorf("Load(non-object) = %+v, want defaults", got)
	}
}

func TestLoad_PartialSectionFillsDefaults(t *testing.T) {
	root := t.TempDir()
	writeSettingsJSON(t, root, `{"schemaVersion":1,"autoswitch":{"threshold":80}}`)
	got := Load(root)
	if got.Threshold != 80.0 {
		t.Errorf("Threshold = %v, want 80", got.Threshold)
	}
	if got.IntervalSeconds != Default().IntervalSeconds {
		t.Errorf("IntervalSeconds = %v, want default %v", got.IntervalSeconds, Default().IntervalSeconds)
	}
}

// --- Clamp table (spec 08§8.3 / DESIGN §5 WP4) ---

func TestLoad_ClampTable(t *testing.T) {
	cases := []struct {
		name string
		json string
		want func(AutoSwitchSettings) bool
		desc string
	}{
		{"threshold_200_clamps_to_99_9", `{"autoswitch":{"threshold":200}}`,
			func(s AutoSwitchSettings) bool { return s.Threshold == 99.9 }, "threshold=99.9"},
		{"intervalSeconds_1_clamps_to_15", `{"autoswitch":{"intervalSeconds":1}}`,
			func(s AutoSwitchSettings) bool { return s.IntervalSeconds == 15.0 }, "intervalSeconds=15.0"},
		{"hysteresisPct_neg5_clamps_to_0", `{"autoswitch":{"hysteresisPct":-5}}`,
			func(s AutoSwitchSettings) bool { return s.HysteresisPct == 0.0 }, "hysteresisPct=0.0"},
		{"unhealthyTicks_0_clamps_to_1", `{"autoswitch":{"unhealthyTicks":0}}`,
			func(s AutoSwitchSettings) bool { return s.UnhealthyTicks == 1 }, "unhealthyTicks=1"},
		{"threshold_bad_type_falls_back_to_default", `{"autoswitch":{"threshold":"high"}}`,
			func(s AutoSwitchSettings) bool { return s.Threshold == 90.0 }, "threshold=90.0 (default)"},
		{"strategy_chaos_falls_back_to_soonest_reset", `{"autoswitch":{"strategy":"chaos"}}`,
			func(s AutoSwitchSettings) bool { return s.Strategy == "soonest-reset" }, "strategy=soonest-reset"},
		{"model_123_falls_back_to_none", `{"autoswitch":{"model":123}}`,
			func(s AutoSwitchSettings) bool { return s.Model == nil }, "model=nil"},
		{"codexThreshold_200_clamps_to_99_9", `{"autoswitch":{"codexThreshold":200}}`,
			func(s AutoSwitchSettings) bool { return s.CodexThreshold == 99.9 }, "codexThreshold=99.9"},
		{"codexThreshold_neg5_clamps_to_0", `{"autoswitch":{"codexThreshold":-5}}`,
			func(s AutoSwitchSettings) bool { return s.CodexThreshold == 0.0 }, "codexThreshold=0.0"},
		{"codexThreshold_bad_type_falls_back_to_default", `{"autoswitch":{"codexThreshold":"high"}}`,
			func(s AutoSwitchSettings) bool { return s.CodexThreshold == 0.0 }, "codexThreshold=0.0 (default)"},
		{"codexEnabled_false_is_false", `{"autoswitch":{"codexEnabled":false}}`,
			func(s AutoSwitchSettings) bool { return !s.CodexEnabled }, "codexEnabled=false"},
		{"codexEnabled_0_is_false", `{"autoswitch":{"codexEnabled":0}}`,
			func(s AutoSwitchSettings) bool { return !s.CodexEnabled }, "codexEnabled=false"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeSettingsJSON(t, root, c.json)
			got := Load(root)
			if !c.want(got) {
				t.Errorf("Load(%s) = %+v, want %s", c.json, got, c.desc)
			}
		})
	}
}

// --- Save / roundtrip ---

func TestSave_Roundtrip(t *testing.T) {
	root := t.TempDir()
	custom := AutoSwitchSettings{
		Threshold: 85.0, IntervalSeconds: 60.0, CodexEnabled: false,
		CodexThreshold: 75.0, CooldownSeconds: 60.0, HysteresisPct: 10.0, Strategy: "best",
		UnhealthyTicks: 3, Model: nil,
	}
	if err := Save(root, custom); err != nil {
		t.Fatal(err)
	}
	got := Load(root)
	if !reflect.DeepEqual(got, custom) {
		t.Errorf("Load(Save(custom)) = %+v, want %+v", got, custom)
	}
}

func TestSave_UnknownKeysSurvive(t *testing.T) {
	root := t.TempDir()
	writeSettingsJSON(t, root, `{
		"schemaVersion": 1,
		"futureSection": {"x": 1},
		"autoswitch": {"threshold": 80, "futureKnob": true}
	}`)
	custom := Default()
	custom.Threshold = 70.0
	if err := Save(root, custom); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(SettingsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	future, ok := raw["futureSection"].(map[string]any)
	if !ok || future["x"] != 1.0 {
		t.Errorf("futureSection = %v, want {x: 1}", raw["futureSection"])
	}
	autoswitch := raw["autoswitch"].(map[string]any)
	if autoswitch["futureKnob"] != true {
		t.Errorf("futureKnob = %v, want true", autoswitch["futureKnob"])
	}
	if autoswitch["threshold"] != 70.0 {
		t.Errorf("threshold = %v, want 70.0", autoswitch["threshold"])
	}
}

func TestSave_FileMode0600(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("POSIX file modes")
	}
	root := t.TempDir()
	if err := Save(root, Default()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(SettingsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
}

// --- SettingSpecs registry invariants (spec 08§8.2) ---

func TestSettingSpecs_CoversEveryField(t *testing.T) {
	want := map[string]bool{
		"Threshold": true, "IntervalSeconds": true, "CodexEnabled": true,
		"CodexThreshold": true, "CooldownSeconds": true,
		"HysteresisPct": true, "Strategy": true,
		"UnhealthyTicks": true, "Model": true,
	}
	got := map[string]bool{}
	for _, s := range SettingSpecs {
		got[s.Field] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SettingSpecs fields = %v, want %v", got, want)
	}
}

func TestSettingSpecs_DefaultsMatchDataclass(t *testing.T) {
	fields := fieldsOf(Default())
	for _, spec := range SettingSpecs {
		got := fields[spec.Field]
		if !reflect.DeepEqual(got, spec.Default) {
			t.Errorf("spec %s: Default=%#v, want dataclass default %#v", spec.Dotted(), spec.Default, got)
		}
	}
}

// --- SetSetting / UnsetSetting ---

func TestSetSetting_WritesMinimalFile(t *testing.T) {
	root := t.TempDir()
	value, err := SetSetting(root, "autoswitch.threshold", "80")
	if err != nil {
		t.Fatal(err)
	}
	if value != 80.0 {
		t.Errorf("value = %v, want 80.0", value)
	}
	data, err := os.ReadFile(SettingsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"schemaVersion": 1.0, "autoswitch": map[string]any{"threshold": 80.0}}
	if !reflect.DeepEqual(raw, want) {
		t.Errorf("raw = %#v, want %#v", raw, want)
	}
}

// TestSetSetting_FloatKeepsTrailingDecimal locks byte-parity with Python's
// json.dumps: a whole-number float setting must serialize as "80.0", not "80"
// (spec 08§8 result file {"threshold": 80.0}; fixture settings.json; DESIGN A1
// permits only key-order/indent to differ). A pre-existing float key that a
// single-key write merely preserves must also keep its trailing decimal.
func TestSetSetting_FloatKeepsTrailingDecimal(t *testing.T) {
	root := t.TempDir()
	if _, err := SetSetting(root, "autoswitch.threshold", "80"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(SettingsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, `"threshold": 80.0`) {
		t.Errorf("settings.json = %s, want it to contain \"threshold\": 80.0", got)
	}

	// A subsequent write of a different key must preserve threshold's ".0".
	if _, err := SetSetting(root, "autoswitch.model", "Fable"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(SettingsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, `"threshold": 80.0`) {
		t.Errorf("after second set, settings.json = %s, want threshold still 80.0", got)
	}
}

func TestSetSetting_IntKindCoercesAndRejectsFloats(t *testing.T) {
	root := t.TempDir()
	v, err := SetSetting(root, "autoswitch.unhealthyTicks", "5")
	if err != nil || v != 5 {
		t.Fatalf("v=%v err=%v, want 5, nil", v, err)
	}
	_, err = SetSetting(root, "autoswitch.unhealthyTicks", "3.5")
	if err == nil {
		t.Fatal("expected an error for a float value on an int-kind setting")
	}
	if got := err.Error(); !strings.Contains(got, "integer") {
		t.Errorf("error = %q, want it to mention 'integer'", got)
	}
}

func TestSetSetting_RejectsOutOfRangeWithoutWriting(t *testing.T) {
	root := t.TempDir()
	_, err := SetSetting(root, "autoswitch.threshold", "200")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, "between 50 and 99.9") {
		t.Errorf("error = %q, want it to contain 'between 50 and 99.9'", got)
	}
	if _, statErr := os.Stat(SettingsPath(root)); !os.IsNotExist(statErr) {
		t.Error("settings.json should not have been created")
	}
}

// TestSetSetting_RejectsNaNAsOutOfRange: strconv.ParseFloat accepts "NaN",
// which compares false against both bounds; it must be refused with the
// range message, not reach the JSON encoder.
func TestSetSetting_RejectsNaNAsOutOfRange(t *testing.T) {
	root := t.TempDir()
	for _, raw := range []string{"NaN", "nan"} {
		_, err := SetSetting(root, "autoswitch.threshold", raw)
		if err == nil || !strings.Contains(err.Error(), "must be between 50 and 99.9") {
			t.Errorf("SetSetting(%q) err = %v, want the range message", raw, err)
		}
	}
	if _, statErr := os.Stat(SettingsPath(root)); !os.IsNotExist(statErr) {
		t.Error("settings.json should not have been created")
	}
}

// TestStrategyDefaultIsSoonestReset pins the auto-switch ordering default and
// what it means for a settings.json that already exists: a file without the
// key (or with a value Load does not know) now orders by earliest weekly
// renewal, and a file that names `best` keeps it. Nothing is rewritten on
// load, so an explicit choice survives the change of default.
func TestStrategyDefaultIsSoonestReset(t *testing.T) {
	if got := Default().Strategy; got != "soonest-reset" {
		t.Fatalf("Default().Strategy = %q, want soonest-reset", got)
	}
	spec, err := SpecFor("autoswitch.strategy")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Default != "soonest-reset" {
		t.Errorf("spec default = %v, want soonest-reset", spec.Default)
	}

	absent := t.TempDir()
	writeSettingsJSON(t, absent, `{"schemaVersion":1,"autoswitch":{"intervalSeconds":60}}`)
	if got := Load(absent).Strategy; got != "soonest-reset" {
		t.Errorf("a file without autoswitch.strategy loads %q, want soonest-reset", got)
	}

	explicit := t.TempDir()
	writeSettingsJSON(t, explicit, `{"schemaVersion":1,"autoswitch":{"strategy":"best"}}`)
	if got := Load(explicit).Strategy; got != "best" {
		t.Errorf("a file that sets best loads %q, want best", got)
	}
	before, err := os.ReadFile(SettingsPath(explicit))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range EffectiveSettings(explicit) {
		if e.Spec.Dotted() == "autoswitch.strategy" && (!e.IsSet || e.Value != "best") {
			t.Errorf("effective strategy = %+v, want best and set", e)
		}
	}
	after, err := os.ReadFile(SettingsPath(explicit))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("reading the settings rewrote the file:\n%s\n->\n%s", before, after)
	}
}

// TestSetSetting_StrategyChoices locks the two accepted autoswitch.strategy
// values ("best" and the added "soonest-reset") and that an off-list value is
// still strictly rejected with the choice list (the lenient-load fallback for a
// bad value is covered by TestLoad_ClampTable's strategy_chaos case).
func TestSetSetting_StrategyChoices(t *testing.T) {
	root := t.TempDir()
	v, err := SetSetting(root, "autoswitch.strategy", "soonest-reset")
	if err != nil || v != "soonest-reset" {
		t.Fatalf("v=%v err=%v, want soonest-reset, nil", v, err)
	}
	if got := Load(root).Strategy; got != "soonest-reset" {
		t.Errorf("Load().Strategy = %q, want soonest-reset", got)
	}
	if _, err := SetSetting(root, "autoswitch.strategy", "best"); err != nil {
		t.Errorf("best rejected: %v", err)
	}
	_, err = SetSetting(root, "autoswitch.strategy", "chaos")
	if err == nil || !strings.Contains(err.Error(), "soonest-reset") {
		t.Errorf("err = %v, want it to list the valid choices including soonest-reset", err)
	}
}

// TestDefault_CodexKnobs pins the claude-swap PR #252 defaults: Codex rides in
// the auto loop unless turned off, and its threshold inherits
// autoswitch.threshold (0) until set.
func TestDefault_CodexKnobs(t *testing.T) {
	d := Default()
	if !d.CodexEnabled {
		t.Error("Default().CodexEnabled = false, want true")
	}
	if d.CodexThreshold != 0.0 {
		t.Errorf("Default().CodexThreshold = %v, want 0", d.CodexThreshold)
	}
	for _, tc := range []struct {
		key, help string
		kind      Kind
	}{
		{"autoswitch.codexEnabled", "Also auto-switch Codex accounts in the tycswap auto loop", KindBool},
		{"autoswitch.codexThreshold", "Codex-only switch threshold (0 = use autoswitch.threshold)", KindFloat},
	} {
		spec, err := SpecFor(tc.key)
		if err != nil {
			t.Fatalf("SpecFor(%s): %v", tc.key, err)
		}
		if spec.Kind != tc.kind || spec.Help != tc.help {
			t.Errorf("%s: Kind=%q Help=%q, want %q %q", tc.key, spec.Kind, spec.Help, tc.kind, tc.help)
		}
	}
	if spec, _ := SpecFor("autoswitch.codexThreshold"); spec.Lo != 0.0 || spec.Hi != 99.9 {
		t.Errorf("codexThreshold range = [%v, %v], want [0, 99.9]", spec.Lo, spec.Hi)
	}
}

// TestSetSetting_CodexThresholdRange: the strict config-set parser accepts the
// whole 0–99.9 range — 0 included, since it is the "inherit" sentinel — and
// rejects anything above 99.9 without writing.
func TestSetSetting_CodexThresholdRange(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		raw  string
		want float64
	}{
		{"0", 0.0},
		{"99.9", 99.9},
		{"85", 85.0},
	} {
		v, err := SetSetting(root, "autoswitch.codexThreshold", tc.raw)
		if err != nil || v != tc.want {
			t.Fatalf("set %s: v=%v err=%v, want %v, nil", tc.raw, v, err, tc.want)
		}
		if got := Load(root).CodexThreshold; got != tc.want {
			t.Errorf("after set %s: Load().CodexThreshold = %v, want %v", tc.raw, got, tc.want)
		}
	}
	data, err := os.ReadFile(SettingsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, `"codexThreshold": 85.0`) {
		t.Errorf("settings.json = %s, want it to contain \"codexThreshold\": 85.0", got)
	}

	fresh := t.TempDir()
	_, err = SetSetting(fresh, "autoswitch.codexThreshold", "100")
	if err == nil || !strings.Contains(err.Error(), "between 0 and 99.9") {
		t.Errorf("err = %v, want it to contain 'between 0 and 99.9'", err)
	}
	if _, statErr := os.Stat(SettingsPath(fresh)); !os.IsNotExist(statErr) {
		t.Error("settings.json should not have been created")
	}
}

// TestSetSetting_CodexEnabledRoundTrips: set false persists and loads, get's
// effective row reports it set, and unset restores the true default.
func TestSetSetting_CodexEnabledRoundTrips(t *testing.T) {
	root := t.TempDir()
	v, err := SetSetting(root, "autoswitch.codexEnabled", "no")
	if err != nil || v != false {
		t.Fatalf("v=%v err=%v, want false, nil", v, err)
	}
	if Load(root).CodexEnabled {
		t.Error("Load().CodexEnabled = true after set false")
	}
	for _, r := range EffectiveSettings(root) {
		if r.Spec.Dotted() == "autoswitch.codexEnabled" {
			if !r.IsSet || r.Value != false || FormatSettingValue(r.Value) != "false" {
				t.Errorf("effective row = %+v, want IsSet with value false", r)
			}
		}
	}
	removed, err := UnsetSetting(root, "autoswitch.codexEnabled")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if !Load(root).CodexEnabled {
		t.Error("Load().CodexEnabled = false after unset, want default true")
	}
}

// TestRemovedIncludeApiKeyAccountsIsAnUnknownKey: autoswitch.includeApiKeyAccounts
// is gone (DESIGN A33). A settings.json that still carries it loads exactly as
// one without it, whatever its value; a whole-file save keeps it as it keeps
// any key it does not know; and config set/get/unset refuse it as unknown,
// naming the valid keys.
func TestRemovedIncludeApiKeyAccountsIsAnUnknownKey(t *testing.T) {
	for _, v := range []string{"true", "false", "1", `"yes"`} {
		root := t.TempDir()
		writeSettingsJSON(t, root, `{"schemaVersion":1,"autoswitch":{"includeApiKeyAccounts":`+v+`,"intervalSeconds":30}}`)
		want := Default()
		want.IntervalSeconds = 30
		if got := Load(root); !reflect.DeepEqual(got, want) {
			t.Errorf("includeApiKeyAccounts=%s: Load = %+v, want %+v", v, got, want)
		}
		for _, e := range EffectiveSettings(root) {
			if e.Spec.JSONKey == "includeApiKeyAccounts" {
				t.Errorf("EffectiveSettings still lists %s", e.Spec.Dotted())
			}
		}
	}

	root := t.TempDir()
	writeSettingsJSON(t, root, `{"schemaVersion":1,"autoswitch":{"includeApiKeyAccounts":true}}`)
	if err := Save(root, Default()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(SettingsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if section, _ := raw["autoswitch"].(map[string]any); section["includeApiKeyAccounts"] != true {
		t.Errorf("Save dropped the unknown key: %s", data)
	}

	if _, err := SetSetting(root, "autoswitch.includeApiKeyAccounts", "true"); err == nil || !strings.Contains(err.Error(), "unknown setting") || !strings.Contains(err.Error(), "autoswitch.strategy") {
		t.Errorf("SetSetting err = %v, want unknown setting with the valid keys", err)
	}
	if _, err := SpecFor("autoswitch.includeApiKeyAccounts"); err == nil {
		t.Error("SpecFor found the removed key")
	}
	if _, err := UnsetSetting(root, "autoswitch.includeApiKeyAccounts"); err == nil || !strings.Contains(err.Error(), "unknown setting") {
		t.Errorf("UnsetSetting err = %v, want unknown setting", err)
	}
}

func TestSetSetting_RejectsUnknownKey(t *testing.T) {
	root := t.TempDir()
	_, err := SetSetting(root, "autoswitch.bogus", "1")
	if err == nil || !strings.Contains(err.Error(), "unknown setting") {
		t.Errorf("err = %v, want 'unknown setting'", err)
	}
}

func TestSetSetting_StringKindRoundTrips(t *testing.T) {
	root := t.TempDir()
	v, err := SetSetting(root, "autoswitch.model", "Fable")
	if err != nil || v != "Fable" {
		t.Fatalf("v=%v err=%v", v, err)
	}
	if got := Load(root).Model; got == nil || *got != "Fable" {
		t.Errorf("Load().Model = %v, want Fable", got)
	}
}

func TestSetSetting_StringKindRejectsEmpty(t *testing.T) {
	root := t.TempDir()
	_, err := SetSetting(root, "autoswitch.model", "   ")
	if err == nil || !strings.Contains(err.Error(), "unset") {
		t.Errorf("err = %v, want it to mention 'unset'", err)
	}
	if _, statErr := os.Stat(SettingsPath(root)); !os.IsNotExist(statErr) {
		t.Error("settings.json should not have been created")
	}
}

func TestSetSetting_RejectsBoolWordsStrictly(t *testing.T) {
	root := t.TempDir()
	v, err := SetSetting(root, "autoswitch.codexEnabled", "FALSE")
	if err != nil || v != false {
		t.Fatalf("v=%v err=%v, want false, nil", v, err)
	}
	_, err = SetSetting(root, "autoswitch.codexEnabled", "falsy")
	if err == nil || !strings.Contains(err.Error(), "true or false") {
		t.Errorf("err = %v, want it to mention 'true or false'", err)
	}
}

func TestSetSetting_OnCorruptFileRaisesAndPreservesIt(t *testing.T) {
	root := t.TempDir()
	writeSettingsJSON(t, root, "{not json")
	_, err := SetSetting(root, "autoswitch.threshold", "80")
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("err = %v, want it to mention 'not valid JSON'", err)
	}
	data, rerr := os.ReadFile(SettingsPath(root))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(data) != "{not json" {
		t.Errorf("file contents changed: %q", data)
	}
}

func TestUnsetSetting_RemovesKeyAndEmptySection(t *testing.T) {
	root := t.TempDir()
	if _, err := SetSetting(root, "autoswitch.threshold", "80"); err != nil {
		t.Fatal(err)
	}
	removed, err := UnsetSetting(root, "autoswitch.threshold")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	data, _ := os.ReadFile(SettingsPath(root))
	var raw map[string]any
	json.Unmarshal(data, &raw)
	if _, ok := raw["autoswitch"]; ok {
		t.Error("autoswitch section should have been removed once empty")
	}
}

func TestUnsetSetting_StampsSchemaVersionOnUnversionedFile(t *testing.T) {
	root := t.TempDir()
	writeSettingsJSON(t, root, `{"autoswitch":{"threshold":80}}`)
	removed, err := UnsetSetting(root, "autoswitch.threshold")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	data, _ := os.ReadFile(SettingsPath(root))
	var raw map[string]any
	json.Unmarshal(data, &raw)
	if raw["schemaVersion"] != 1.0 {
		t.Errorf("schemaVersion = %v, want 1", raw["schemaVersion"])
	}
}

func TestUnsetSetting_AbsentKeyIsNoop(t *testing.T) {
	root := t.TempDir()
	removed, err := UnsetSetting(root, "autoswitch.threshold")
	if err != nil || removed {
		t.Fatalf("removed=%v err=%v, want false, nil", removed, err)
	}
	if _, statErr := os.Stat(SettingsPath(root)); !os.IsNotExist(statErr) {
		t.Error("settings.json should not have been created")
	}
}

// --- EffectiveSettings ---

func TestEffectiveSettings_MissingFileReportsAllDefaults(t *testing.T) {
	rows := EffectiveSettings(t.TempDir())
	if len(rows) != len(SettingSpecs) {
		t.Fatalf("got %d rows, want %d", len(rows), len(SettingSpecs))
	}
	for _, r := range rows {
		if r.IsSet {
			t.Errorf("%s: IsSet = true, want false", r.Spec.Dotted())
		}
	}
}

func TestEffectiveSettings_PresenceNotValueEqualityMarksSet(t *testing.T) {
	root := t.TempDir()
	if _, err := SetSetting(root, "autoswitch.threshold", "90"); err != nil { // equals default
		t.Fatal(err)
	}
	byKey := map[string]bool{}
	for _, r := range EffectiveSettings(root) {
		byKey[r.Spec.Dotted()] = r.IsSet
	}
	if !byKey["autoswitch.threshold"] {
		t.Error("autoswitch.threshold should be marked set even though it equals the default")
	}
	if byKey["autoswitch.intervalSeconds"] {
		t.Error("autoswitch.intervalSeconds should not be marked set")
	}
}

// --- MergedWithCLI ---

func TestMergedWithCLI_NoFlagsReturnsUnchanged(t *testing.T) {
	base := Default()
	base.Threshold = 80.0
	got := MergedWithCLI(base, CLIOverrides{})
	if !reflect.DeepEqual(got, base) {
		t.Errorf("MergedWithCLI(no overrides) = %+v, want unchanged %+v", got, base)
	}
}

func TestMergedWithCLI_CLIBeatsSettings(t *testing.T) {
	base := Default()
	base.Threshold = 80.0
	base.CooldownSeconds = 10.0
	threshold := 60.0
	interval := 30.0
	merged := MergedWithCLI(base, CLIOverrides{Threshold: &threshold, IntervalSeconds: &interval})
	if merged.Threshold != 60.0 {
		t.Errorf("Threshold = %v, want 60", merged.Threshold)
	}
	if merged.IntervalSeconds != 30.0 {
		t.Errorf("IntervalSeconds = %v, want 30", merged.IntervalSeconds)
	}
	if merged.CooldownSeconds != 10.0 {
		t.Errorf("CooldownSeconds = %v, want untouched 10", merged.CooldownSeconds)
	}
}

// TestMergedWithCLI_PreservesCodexKnobs: the codex knobs have no `tycswap auto`
// flag, so a merge carrying other overrides must pass them through unchanged.
func TestMergedWithCLI_PreservesCodexKnobs(t *testing.T) {
	base := Default()
	base.CodexEnabled = false
	base.CodexThreshold = 70.0
	threshold := 60.0
	merged := MergedWithCLI(base, CLIOverrides{Threshold: &threshold})
	if merged.CodexEnabled || merged.CodexThreshold != 70.0 {
		t.Errorf("CodexEnabled=%v CodexThreshold=%v, want false/70", merged.CodexEnabled, merged.CodexThreshold)
	}
}

func TestMergedWithCLI_ValuesAreClamped(t *testing.T) {
	interval := 1.0
	merged := MergedWithCLI(Default(), CLIOverrides{IntervalSeconds: &interval})
	if merged.IntervalSeconds != 15.0 {
		t.Errorf("IntervalSeconds = %v, want clamped 15", merged.IntervalSeconds)
	}
}

func TestMergedWithCLI_ModelOverride(t *testing.T) {
	merged := MergedWithCLI(Default(), CLIOverrides{Model: strp("Fable")})
	if merged.Model == nil || *merged.Model != "Fable" {
		t.Errorf("Model = %v, want Fable", merged.Model)
	}
}

// --- ParseModelNames ---

func TestParseModelNames(t *testing.T) {
	cases := []struct {
		name string
		in   *string
		want []string
	}{
		{"nil", nil, []string{}},
		{"empty", strp(""), []string{}},
		{"single", strp("Fable"), []string{"Fable"}},
		{"dedupe_first_spelling_wins", strp("Opus, opus,Fable"), []string{"Opus", "Fable"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseModelNames(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParseModelNames(%v) = %v, want %v", derefOrNil(c.in), got, c.want)
			}
		})
	}
}

func derefOrNil(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// --- FormatSettingValue ---

func TestFormatSettingValue(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "(none)"},
		{true, "true"},
		{false, "false"},
		{90.0, "90"},
		{99.9, "99.9"},
		{"Fable", "Fable"},
	}
	for _, c := range cases {
		if got := FormatSettingValue(c.in); got != c.want {
			t.Errorf("FormatSettingValue(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- Parse a genuine Python-produced settings.json fixture (DESIGN §5 WP4) ---

func TestLoad_PythonFixture(t *testing.T) {
	fixturesDir := testutil.FixturesDir(t)
	data, err := os.ReadFile(filepath.Join(fixturesDir, "claude-swap-data", "settings.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	root := t.TempDir()
	writeSettingsJSON(t, root, string(data))

	got := Load(root)
	if got.Threshold != 80.0 {
		t.Errorf("Threshold = %v, want 80", got.Threshold)
	}
	if got.Model == nil || *got.Model != "Fable" {
		t.Errorf("Model = %v, want Fable", got.Model)
	}
	// Everything else the fixture didn't set stays default.
	if got.IntervalSeconds != Default().IntervalSeconds {
		t.Errorf("IntervalSeconds = %v, want default", got.IntervalSeconds)
	}
}

// ValuesOf names every spec by its dotted key and carries the struct's value
// for it; an unset Model is nil and a set one its string.
func TestValuesOf_CoversEverySpec(t *testing.T) {
	got := ValuesOf(Default())
	if len(got) != len(SettingSpecs) {
		t.Fatalf("ValuesOf has %d keys, want one per spec (%d)", len(got), len(SettingSpecs))
	}
	for _, spec := range SettingSpecs {
		v, ok := got[spec.Dotted()]
		if !ok {
			t.Errorf("ValuesOf lacks %s", spec.Dotted())
			continue
		}
		if !reflect.DeepEqual(v, spec.Default) {
			t.Errorf("ValuesOf(Default())[%s] = %#v, want the spec default %#v", spec.Dotted(), v, spec.Default)
		}
	}
	s := Default()
	m := "Fable, Opus"
	s.Model, s.Threshold = &m, 77
	got = ValuesOf(s)
	if got["autoswitch.model"] != "Fable, Opus" || got["autoswitch.threshold"] != 77.0 {
		t.Fatalf("ValuesOf(set) model %#v threshold %#v", got["autoswitch.model"], got["autoswitch.threshold"])
	}
}
