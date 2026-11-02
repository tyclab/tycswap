package tray

import (
	"reflect"
	"testing"

	native "github.com/tyclab/tycswap/internal/tray"
)

// OnPanelAction stays internal: the panel it answers (SetPanel, Panel) is not public.
func TestOptionsCoverTheNativeOptions(t *testing.T) {
	public := reflect.TypeOf(Options{})
	nat := reflect.TypeOf(native.Options{})
	for i := 0; i < nat.NumField(); i++ {
		f := nat.Field(i)
		if f.Name == "OnPanelAction" {
			continue
		}
		if p, ok := public.FieldByName(f.Name); !ok || p.Type != f.Type {
			t.Errorf("public Options lacks %s %s", f.Name, f.Type)
		}
	}
}

func TestOptionsReachTheNativeTray(t *testing.T) {
	o := Options{Name: "Party Visuals", Identifier: "io.tyclab.partyvisuals.controller", Tooltip: "Party Visuals",
		OnClick: func(string) {}, OnSystemEvent: func(string) {}, OnActivate: func() {}}
	got := reflect.ValueOf(o.native())
	want := reflect.ValueOf(o)
	for i := 0; i < want.NumField(); i++ {
		name := want.Type().Field(i).Name
		w, g := want.Field(i), got.FieldByName(name)
		if w.Kind() == reflect.Func {
			if g.Pointer() != w.Pointer() {
				t.Errorf("%s did not reach the native tray", name)
			}
		} else if g.Interface() != w.Interface() {
			t.Errorf("%s = %v, want %v", name, g.Interface(), w.Interface())
		}
	}
}
