package session

import (
	"reflect"
	"testing"
)

func TestSelection_Policy(t *testing.T) {
	parts := []Participant{
		{ID: "a", Name: "Alice", Pinned: true},
		{ID: "b", Name: "Bob"},
		{ID: "c", Name: "Carol", Pinned: true},
		{ID: "d", Name: "Dan", Pinned: true}, // No video source
	}
	src := func(id string) string {
		if id == "d" {
			return ""
		}
		return id + "-v0"
	}
	sel := Selection{Limit: 1, Height: 360, PinHeight: 720}
	p, dropped := sel.policy(parts, src)
	if p.LastN != 1 || p.DefaultMaxHeight != 360 {
		t.Errorf("policy %+v", p)
	}
	if !reflect.DeepEqual(p.Selected, []string{"a-v0"}) || !reflect.DeepEqual(p.OnStage, []string{"a-v0"}) {
		t.Errorf("selected %v on stage %v", p.Selected, p.OnStage)
	}
	if !reflect.DeepEqual(p.MaxHeights, map[string]int{"a-v0": 720}) {
		t.Errorf("heights %v", p.MaxHeights)
	}
	if !reflect.DeepEqual(dropped, []string{"Carol"}) {
		t.Errorf("dropped %v, want Carol (over the limit)", dropped)
	}
	// Same height for pins: no per-source constraint.
	p, _ = Selection{Limit: 9, Height: 180, PinHeight: 180}.policy(parts, src)
	if p.MaxHeights != nil || len(p.Selected) != 2 {
		t.Errorf("policy %+v", p)
	}
}

func TestSelection_Validate(t *testing.T) {
	ok := Selection{}.withDefaults()
	if !reflect.DeepEqual(ok, Selection{Limit: 9, Height: 180, PinHeight: 720}) || ok.validate() != nil {
		t.Errorf("defaults %+v", ok)
	}
	for _, bad := range []Selection{{Limit: 26, Height: 180, PinHeight: 720}, {Limit: 9, Height: 240, PinHeight: 720}, {Limit: 9, Height: 180, PinHeight: 1080}} {
		if bad.validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
