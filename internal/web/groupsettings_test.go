package web

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
)

type groupSettingsFixture struct {
	mu     sync.Mutex
	values map[string]string
	views  []SettingView
}

func (f *groupSettingsFixture) Views() []GroupView {
	return []GroupView{{ID: "fable", SettingViews: f.views}}
}

func (f *groupSettingsFixture) Switch(string, string) (map[string]any, error) {
	return nil, nil
}

func (f *groupSettingsFixture) Settings(group string) ([]SettingView, error) {
	if group != "fable" && group != "opus" {
		return nil, cerr.Validation("unknown group")
	}
	return f.views, nil
}

func (f *groupSettingsFixture) SetSetting(group, key, raw string) (any, error) {
	if key == modelSettingKey {
		return nil, cerr.Validation("group model is read-only")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[group+":"+key] = raw
	return raw, nil
}

func (f *groupSettingsFixture) UnsetSetting(group, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	identity := group + ":" + key
	_, exists := f.values[identity]
	delete(f.values, identity)
	return exists, nil
}

func TestGroupSettingRoutesAndLocalSaveShareMutationPath(t *testing.T) {
	fixture := &groupSettingsFixture{values: map[string]string{}, views: []SettingView{{Key: "autoswitch.sevenDayThreshold", Source: "default", Value: 80.0}}}
	h := newHarness(t, func(_ *harness, deps *Deps) { deps.Groups = fixture })
	key := "autoswitch.sevenDayThreshold"
	local := "84"
	result, err := h.s.SaveSetting("fable", key, &local)
	if err != nil || result["value"] != "84" || result["stateSequence"] == nil {
		t.Fatalf("local save %#v %v", result, err)
	}
	response := h.postJSON("/api/groups/fable/settings/"+key, map[string]any{"value": 86})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP save %d: %s", response.StatusCode, readBody(t, response))
	}
	decodeJSON(t, response)
	fixture.mu.Lock()
	written := fixture.values["fable:"+key]
	fixture.mu.Unlock()
	if written != "86" || len(h.set.Calls()) != 0 || len(h.auto.Calls()) != 0 {
		t.Fatalf("group save touched shared settings or engine: %q %v %v", written, h.set.Calls(), h.auto.Calls())
	}
	for _, path := range []string{"/api/groups/fable/settings/" + key, "/api/groups/fable/settings/" + key + "/unset"} {
		method := http.MethodDelete
		if strings.HasSuffix(path, "/unset") {
			method = http.MethodPost
		}
		response = h.send(method, path, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("reset %d: %s", response.StatusCode, readBody(t, response))
		}
		decodeJSON(t, response)
	}
	if response = h.postJSON("/api/groups/fable/settings/"+modelSettingKey, map[string]any{"value": "all"}); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("model save %d", response.StatusCode)
	}
	response.Body.Close()
	if len(h.auto.Calls()) != 0 {
		t.Fatal("group model mutation retargeted Default engine")
	}
	if response = h.get("/api/groups/unknown/settings"); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown group %d", response.StatusCode)
	}
	response.Body.Close()
}

func TestLocalSharedModelSaveAndResetRetargetHostedEngine(t *testing.T) {
	h := newHarness(t)
	model := "Fable"
	if _, err := h.s.SaveSetting("", modelSettingKey, &model); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.SaveSetting("", modelSettingKey, nil); err != nil {
		t.Fatal(err)
	}
	if calls := h.auto.Calls(); !reflect.DeepEqual(calls, []string{`ApplyModels("Fable")`, `ApplyModels("")`}) {
		t.Fatalf("local model path %v", calls)
	}
}

func TestGroupSettingRoutesCannotMutateSharedDefaults(t *testing.T) {
	fixture := &groupSettingsFixture{values: map[string]string{}}
	h := newHarness(t, func(_ *harness, deps *Deps) { deps.Groups = fixture })
	key := "autoswitch.sevenDayThreshold"
	for _, group := range []string{"default", "DEFAULT", "%20default%20", "unknown", "%20"} {
		path := "/api/groups/" + group + "/settings/" + key
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			var body any
			if method == http.MethodPost {
				body = map[string]any{"value": 84}
			}
			response := h.send(method, path, body)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("%s %s returned %d: %s", method, group, response.StatusCode, readBody(t, response))
			}
			response.Body.Close()
		}
	}
	if calls := h.set.Calls(); len(calls) != 0 {
		t.Fatalf("group routes mutated shared settings: %v", calls)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.values) != 0 {
		t.Fatalf("invalid scopes reached group writes: %v", fixture.values)
	}
}

func TestManagedSettingMetadataUsesNextCheckAndPreservesFacadeViews(t *testing.T) {
	fixture := &groupSettingsFixture{values: map[string]string{}, views: []SettingView{{Key: "autoswitch.sevenDayThreshold", Source: "default", Applies: "original"}}}
	h := newHarness(t, func(h *harness, deps *Deps) {
		deps.Groups = fixture
		h.auto.view.ManagedBy = "Flakelab"
		h.auto.view.Running = false
		h.set.views = []SettingView{{Key: "autoswitch.strategy"}, {Key: "autoswitch.intervalSeconds"}, {Key: "autoswitch.handoverWaitMinutes"}}
	})
	var shared struct {
		Settings []SettingView `json:"settings"`
	}
	if err := json.Unmarshal(readBody(t, h.get("/api/settings")), &shared); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shared.Settings[0].Applies, "next rotation check") || !strings.Contains(shared.Settings[1].Applies, "external scheduler controls") || !strings.Contains(shared.Settings[2].Applies, "next usage update") {
		t.Fatalf("managed applicability %+v", shared.Settings)
	}
	var group struct {
		Settings []SettingView `json:"settings"`
	}
	if err := json.Unmarshal(readBody(t, h.get("/api/groups/fable/settings")), &group); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(group.Settings[0].Applies, "next group rotation check") || fixture.views[0].Applies != "original" {
		t.Fatalf("inherited applicability %+v original %+v", group.Settings, fixture.views)
	}
	if response := newHarness(t).get("/api/groups/fable/settings"); response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unavailable group settings %d", response.StatusCode)
	} else {
		response.Body.Close()
	}
}
