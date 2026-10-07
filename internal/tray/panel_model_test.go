package tray

import "testing"

func panelFixture() Panel {
	lo, hi := 0.0, 100.0
	return Panel{Columns: []Column{{ID: "account", Label: "Account", Width: 180}, {ID: "weekly", Label: "Weekly", Width: 70}},
		Rows: []PanelRow{
			{ID: "claude:1", Title: "Account 1", Active: true, Cells: []string{"Account 1", "42%"}, Targets: []Target{{ID: "group:fable:1", Label: "Fable", Disabled: true, Reason: "already active"}}},
			{ID: "claude:2", Title: "Account 2", Cells: []string{"Account 2", "15%"}, Targets: []Target{{ID: "group:fable:2", Label: "Fable"}}},
		}, Settings: []SettingScope{
			{ID: "", Label: "Shared defaults", Settings: []PanelSetting{{Key: "threshold", Label: "Threshold", Kind: "float", Value: "85", Min: &lo, Max: &hi}}},
			{ID: "fable", Label: "Fable", Settings: []PanelSetting{
				{Key: "threshold", Label: "Threshold", Kind: "float", Value: "95", Min: &lo, Max: &hi},
				{Key: "model", Label: "Model", Kind: "string", Value: "fable", ReadOnly: "Fable is fixed for this group"},
			}},
		}, Actions: []Item{{ID: "open", Title: "Open dashboard"}, {ID: "quit", Title: "Quit"}}}
}

func TestPanelLiveUpdatePreservesDraftAndIdentity(t *testing.T) {
	m := &panelModel{}
	p := panelFixture()
	m.update(p)
	m.rowID, m.scopeID, m.fieldKey = "claude:2", "fable", "threshold"
	m.selectTarget()
	m.edit("93")
	p.Rows[0], p.Rows[1] = p.Rows[1], p.Rows[0]
	p.Rows[0].Cells[1] = "16%"
	m.update(p)
	if m.rowID != "claude:2" || m.targetID != "group:fable:2" || m.value() != "93" {
		t.Fatal("live update changed selection or unsaved setting")
	}
	action, err := m.mutation("setting-set")
	if err != nil || action.Scope != "fable" || action.Key != "threshold" || action.Value != "93" {
		t.Fatalf("wrong setting scope or draft: %+v, %v", action, err)
	}
	p.Settings[1].Settings[0].Value = "93"
	m.update(p)
	if len(m.drafts) != 0 {
		t.Fatal("confirmed setting still shown as unsaved")
	}
}

func TestPanelReadonlyOfflineAndInvalidValueHoldMutations(t *testing.T) {
	m := &panelModel{}
	p := panelFixture()
	m.update(p)
	m.scopeID, m.fieldKey = "fable", "model"
	m.edit("opus")
	for _, kind := range []string{"setting-set", "setting-reset"} {
		if _, err := m.mutation(kind); err == nil {
			t.Fatal("read-only group model accepted mutation")
		}
	}
	m.fieldKey = "threshold"
	for _, value := range []string{"NaN", "Inf", "101", "-1", "unknown"} {
		m.edit(value)
		if _, err := m.mutation("setting-set"); err == nil {
			t.Fatalf("invalid threshold %q accepted", value)
		}
	}
	m.edit("92")
	p.Offline = true
	m.update(p)
	if _, err := m.mutation("setting-set"); err == nil {
		t.Fatal("offline setting accepted mutation")
	}
	if m.value() != "92" {
		t.Fatal("offline state erased draft")
	}
}

func TestPanelCopyIsIndependentOfProducer(t *testing.T) {
	p := panelFixture()
	copy := clonePanel(p)
	p.Rows[0].Cells[1] = "100%"
	p.Rows[0].Targets[0].Reason = "changed"
	p.Settings[0].Settings[0].Value = "100"
	*p.Settings[0].Settings[0].Max = 200
	if copy.Rows[0].Cells[1] != "42%" || copy.Rows[0].Targets[0].Reason != "already active" ||
		copy.Settings[0].Settings[0].Value != "85" || *copy.Settings[0].Settings[0].Max != 100 {
		t.Fatal("panel retained mutable producer data")
	}
}

func TestPanelTargetScopeDoesNotChangeOnRowNavigation(t *testing.T) {
	p := panelFixture()
	p.Rows[0].Targets = append([]Target{{ID: "switch:claude:1", Label: "Default"}}, p.Rows[0].Targets...)
	p.Rows[1].Targets = append([]Target{{ID: "switch:claude:2", Label: "Default"}}, p.Rows[1].Targets...)
	p.Rows[1].Targets[1].Disabled, p.Rows[1].Targets[1].Reason = true, "Fable start unavailable"
	m := &panelModel{}
	m.update(p)
	m.chooseTarget("group:fable:1")
	m.rowID = "claude:2"
	m.selectTarget()
	if m.targetID != "group:fable:2" || m.target() == nil || !m.target().Disabled {
		t.Fatal("navigation silently changed destination scope")
	}
	p.Rows[1].Targets = p.Rows[1].Targets[:1]
	m.update(p)
	if m.target() != nil || m.targetScope != "group:fable" {
		t.Fatal("missing destination silently fell back to default")
	}
}

func TestPanelAcknowledgesCanonicalSaveAndInheritedReset(t *testing.T) {
	p := panelFixture()
	m := &panelModel{}
	m.update(p)
	m.edit("90.0")
	action, err := m.mutation("setting-set")
	if err != nil {
		t.Fatal(err)
	}
	m.awaitSetting(action)
	p.Settings[0].Settings[0].Value = "90"
	m.update(p)
	if len(m.drafts) != 0 || m.value() != "90" {
		t.Fatal("canonical server value did not acknowledge save")
	}
	m.edit("94")
	action, err = m.mutation("setting-reset")
	if err != nil {
		t.Fatal(err)
	}
	m.awaitSetting(action)
	m.update(p)
	if m.value() != "94" {
		t.Fatal("reset discarded draft before state acknowledgment")
	}
	p.Settings[0].Settings[0].Value = "85"
	p.Settings[0].Settings[0].IsDefault = true
	m.update(p)
	if m.value() != "85" || len(m.drafts) != 0 {
		t.Fatal("inherited reset state did not acknowledge reset")
	}
}

func TestPanelAcknowledgesCanonicalIntAndStringSaves(t *testing.T) {
	for _, check := range []struct{ kind, before, input, ack string }{{"int", "31", "030", "30"}, {"int", "31", "+030", "30"}, {"string", "Fable", " Opus ", "Opus"}} {
		p := panelFixture()
		p.Settings[0].Settings[0].Kind, p.Settings[0].Settings[0].Value = check.kind, check.before
		m := &panelModel{}
		m.update(p)
		m.edit(check.input)
		action, err := m.mutation("setting-set")
		if err != nil {
			t.Fatal(err)
		}
		m.awaitSetting(action)
		p.Settings[0].Settings[0].Value = check.ack
		m.update(p)
		if len(m.drafts) != 0 || len(m.saves) != 0 || m.value() != check.ack {
			t.Fatalf("%s save not acknowledged", check.kind)
		}
	}
}
