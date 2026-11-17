package leadunit

import "testing"

func TestParseActiveState(t *testing.T) {
	for in, want := range map[string]ActiveState{
		"active":       ActiveActive,
		"activating":   ActiveActivating,
		"deactivating": ActiveDeactivating,
		"inactive":     ActiveInactive,
		"failed":       ActiveFailed,
		"reloading":    ActiveUnknown,
		"maintenance":  ActiveUnknown,
		"refreshing":   ActiveUnknown,
		"":             ActiveUnknown,
		"Active":       ActiveUnknown,
		"unknown":      ActiveUnknown,
	} {
		if got := ParseActiveState(in); got != want {
			t.Errorf("ParseActiveState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStateLive(t *testing.T) {
	for _, c := range []struct {
		loaded bool
		active ActiveState
		want   bool
	}{
		{true, ActiveActive, true},
		{true, ActiveActivating, true},
		{true, ActiveDeactivating, true},
		{true, ActiveInactive, false},
		{true, ActiveFailed, false},
		{true, ActiveUnknown, false},
		{true, "", false},
		{false, ActiveActive, false},
		{false, ActiveActivating, false},
		{false, ActiveDeactivating, false},
	} {
		if got := (State{Loaded: c.loaded, Active: c.active}).Live(); got != c.want {
			t.Errorf("Live(loaded=%v, %q) = %v", c.loaded, c.active, got)
		}
	}
}

func TestStateSetDescription(t *testing.T) {
	var st State
	st.SetDescription("ataniq lead epoch=3 intent=abc")
	if !st.Ours || st.Epoch != 3 || st.Intent != "abc" || st.Description != "ataniq lead epoch=3 intent=abc" {
		t.Fatalf("ours description: %+v", st)
	}
	st.SetDescription("something else")
	if st.Ours || st.Epoch != 0 || st.Intent != "" || st.Description != "something else" {
		t.Fatalf("foreign description left ours fields: %+v", st)
	}
}
