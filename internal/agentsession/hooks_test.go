package agentsession

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func newView(t *testing.T, limit time.Duration) HookView {
	t.Helper()
	h, err := NewHookView(limit, "printable")
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func bind(t *testing.T, h HookView, kind HookKind, s string, age time.Duration) HookView {
	t.Helper()
	next, err := Bind(h, HookReport{kind, s, age})
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func ages(t *testing.T, h HookView, a ...time.Duration) HookView {
	t.Helper()
	next, err := Reage(h, a)
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func checkView(t *testing.T, h HookView, obs Obs, why DenyReason) {
	t.Helper()
	i := passing()
	r, err := Readiness(i, h)
	if err != nil || r.Obs != obs || Observation(h) != obs || r.Deny != (DenyResult{why == "", why}) || DenyInForce(i, h) != r.Deny || !r.Delivery.Ready {
		t.Fatal(h, r, err)
	}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeHookView(b)
	if err != nil || !reflect.DeepEqual(got, h) {
		t.Fatal(string(b), got, err)
	}
}
func TestTR23ToTR26AndTR31History(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		h := bind(t, newView(t, 1), "session-start", "s", 0)
		a, b := HookKind("drift"), HookKind("config-change")
		if reverse {
			a, b = b, a
		}
		h = bind(t, bind(t, h, a, "s", 1), b, "s", 1)
		obs := Obs{"s", 0}
		checkView(t, h, obs, "pack-mode-drift")
		if reverse {
			checkView(t, ages(t, h, 0, 1, 2), obs, "pack-hooks-unverified")
		} else {
			checkView(t, ages(t, h, 0, 2, 1), obs, "pack-hooks-unverified")
		}
		checkView(t, ages(t, h, 0, 2, 2), obs, "")
		checkView(t, h, obs, "pack-mode-drift")
	}
	h := bind(t, newView(t, 2), "drift", "s", 0)
	checkView(t, h, Obs{"", 1}, "")
	h = bind(t, h, "session-start", "s", 0)
	checkView(t, h, Obs{"s", 1}, "")
	h = bind(t, h, "drift", "s", 0)
	checkView(t, h, Obs{"s", 1}, "pack-mode-drift")
	checkView(t, ages(t, h, 0, 0, 3), Obs{"s", 1}, "")
	h = bind(t, newView(t, 2), "session-start", "s", 0)
	h = bind(t, h, "session-start", "t", 0)
	checkView(t, h, Obs{"s", 1}, "")
	h = bind(t, h, "drift", "s", 2)
	h = Drop(h, 0)
	checkView(t, h, Obs{"s", 1}, "pack-mode-drift")
	checkView(t, Drop(h, 1), Obs{"s", 1}, "")
	checkView(t, ages(t, h, 0, 3), Obs{"s", 1}, "")
	checkView(t, bind(t, Drop(h, 1), "drift", "t", 0), Obs{"s", 2}, "")
	checkView(t, bind(t, bind(t, newView(t, 0), "session-start", "s", 0), "drift", "s", 0), Obs{"s", 0}, "")
}
func TestTR21aTR21bAndTR35Total(t *testing.T) {
	i := passing()
	i.Launch = Launch{}
	h := newView(t, 1)
	bad := bind(t, h, "other", "s", 0)
	bad.reports[0].Age = -1
	for _, v := range []HookView{h, bad, h} {
		r, err := Readiness(i, v)
		if !reflect.DeepEqual(r.Delivery, DeliveryReady(i)) || !r.Delivery.Ready {
			t.Fatal(r)
		}
		if len(v.reports) > 0 {
			requireError(t, err, "HookView.Reports[0].Age", "negative")
			if r.Deny != (DenyResult{Reason: "invalid-input"}) || r.Obs != (Obs{}) || Observation(v) != (Obs{}) || DenyInForce(i, v) != r.Deny {
				t.Fatal(r)
			}
			next, err := Bind(v, HookReport{"session-start", "s", 0})
			if err == nil || !reflect.DeepEqual(next, v) {
				t.Fatal(next, err)
			}
			next, err = Reage(v, nil)
			if err == nil || !reflect.DeepEqual(next, v) {
				t.Fatal(next, err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	i.Build.DeclaresDenyRow = false
	bound := bind(t, h, "session-start", "s", 0)
	for _, v := range []HookView{bound, bad} {
		r, err := Readiness(i, v)
		requireError(t, err, "Independent.Build.DenyMeant", "declaration-omits-required-row")
		if !reflect.DeepEqual(r.Delivery, DeliveryResult{Reasons: []DeliveryReason{"invalid-input"}}) || r.Deny.Reason != "invalid-input" || r.Obs != Observation(v) {
			t.Fatal(r)
		}
	}
}
func TestTR34TR35HookTransitions(t *testing.T) {
	h, err := NewHookView(-1, "uuid")
	requireError(t, err, "HookView.AgeLimit", "negative")
	if !reflect.DeepEqual(h, HookView{}) {
		t.Fatal(h)
	}
	_, err = NewHookView(0, "other")
	requireError(t, err, "HookView.Form", "unknown-value")
	h, err = NewHookView(1, "uuid")
	if err != nil {
		t.Fatal(err)
	}
	id := "00112233-4455-6677-8899-aabbccddeeff"
	for _, row := range []struct {
		r     HookReport
		field string
		why   InputReason
	}{
		{HookReport{Kind: ""}, "Kind", "empty-value"}, {HookReport{Kind: "x"}, "Kind", "unknown-value"}, {HookReport{Kind: "other"}, "Session", "empty-value"},
		{HookReport{"other", strings.Repeat("a", 129), 0}, "Session", "too-long"}, {HookReport{"session-start", "s", 0}, "Session", "inconsistent"},
		{HookReport{"other", "bad\n", 0}, "Session", "not-printable"}, {HookReport{"other", id, -1}, "Age", "negative"},
	} {
		next, err := Bind(h, row.r)
		path := "HookView.Reports[0]." + row.field
		requireError(t, err, path, row.why)
		if !reflect.DeepEqual(next, h) {
			t.Fatal(next)
		}
		bad := h
		bad.reports = []classifiedReport{{row.r, "foreign"}}
		bad.foreign = 1
		b, _ := json.Marshal(bad)
		decoded, err := DecodeHookView(b)
		requireError(t, err, path, row.why)
		if !reflect.DeepEqual(decoded, HookView{}) {
			t.Fatal(decoded)
		}
		checkView(t, bind(t, next, "session-start", id, 0), Obs{id, 0}, "")
	}
	h = bind(t, h, "session-start", id, 0)
	for _, a := range [][]time.Duration{nil, {-1}} {
		next, err := Reage(h, a)
		if !reflect.DeepEqual(next, h) {
			t.Fatal(next)
		}
		if a == nil {
			requireError(t, err, "HookView.Reports", "inconsistent")
		} else {
			requireError(t, err, "HookView.Reports[0].Age", "negative")
		}
	}
	b, _ := json.Marshal(h)
	for _, row := range []struct {
		old, new, path string
		why            InputReason
	}{
		{`"Age":0`, `"Age":0,"Age":1`, "HookView.Reports[0].Age", "duplicate"}, {`"Bound"`, `"bound"`, "HookView.bound", "unknown-value"},
		{`"bound"`, `"x"`, "HookView.Reports[0].Class", "unknown-value"}, {`"AgeLimit":1`, `"AgeLimit":-1`, "HookView.AgeLimit", "negative"},
	} {
		bad := bytes.Replace(b, []byte(row.old), []byte(row.new), 1)
		_, err := DecodeHookView(bad)
		requireError(t, err, row.path, row.why)
	}
	for _, suffix := range []byte{'!', ' ', '\n'} {
		_, err = DecodeHookView(append(b, suffix))
		requireError(t, err, "HookView", "inconsistent")
	}
}
func TestUUIDSpelling(t *testing.T) {
	const id = "00112233-4455-4677-8899-aabbccddeeff"
	upper := strings.ToUpper(id)
	h, err := NewHookView(1, "uuid")
	if err != nil {
		t.Fatal(err)
	}
	next, err := Bind(h, HookReport{"session-start", upper, 0})
	requireError(t, err, "HookView.Reports[0].Session", "inconsistent")
	if !reflect.DeepEqual(next, h) {
		t.Fatal(next)
	}
	h = bind(t, h, "session-start", id, 0)
	checkView(t, bind(t, h, "drift", id, 0), Obs{id, 0}, "pack-mode-drift")

	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Bound", "Session"} {
		raw := bytes.Replace(b, []byte(`"`+field+`":"`+id+`"`), []byte(`"`+field+`":"`+upper+`"`), 1)
		_, err := DecodeHookView(raw)
		path := "HookView.Bound"
		if field == "Session" {
			path = "HookView.Reports[0].Session"
		}
		requireError(t, err, path, "inconsistent")
	}
	printable := bind(t, newView(t, 1), "session-start", upper, 0)
	checkView(t, bind(t, printable, "drift", id, 0), Obs{upper, 1}, "")
}

func TestHookViewFirstMember(t *testing.T) {
	foreign := classifiedReport{HookReport{"other", "t", 0}, "foreign"}
	later := classifiedReport{HookReport{"drift", "s", -1}, "bound"}
	h := HookView{bound: "s", form: "printable", reports: []classifiedReport{foreign, later}}
	requireError(t, ValidateHooks(h), "HookView.Foreign", "inconsistent")
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{b, bytes.Replace(b, []byte(`"Age":-1`), []byte(`"Age":"bad"`), 1)} {
		_, err := DecodeHookView(raw)
		requireError(t, err, "HookView.Foreign", "inconsistent")
	}
	for _, row := range []struct {
		raw, path string
		why       InputReason
	}{
		{`{"AgeLimit":-1,"Form":7}`, "HookView.AgeLimit", "negative"},
		{`{"Form":7,"AgeLimit":-1}`, "HookView.AgeLimit", "negative"},
		{`{"Reports":[{"Age":"bad","Kind":"typo"}],"Form":"printable"}`, "HookView.Reports[0].Kind", "unknown-value"},
		{`{"Form":"printable","Reports":[{"Kind":"other","Session":"s","Class":"bound"},{"Age":"bad"}]}`, "HookView.Reports[0].Class", "inconsistent"},
	} {
		_, err := DecodeHookView([]byte(row.raw))
		requireError(t, err, row.path, row.why)
	}
}

func TestDecodeRejectsImpossibleHistory(t *testing.T) {
	for _, h := range []HookView{
		{form: "printable", reports: []classifiedReport{{HookReport{"session-start", "s", 0}, "foreign"}}, foreign: 1},
		{form: "printable", bound: "s", reports: []classifiedReport{{HookReport{"drift", "t", 0}, "bound"}}},
		{form: "printable", bound: "s", foreign: 1, reports: []classifiedReport{{HookReport{"other", "s", 0}, "bound"}, {HookReport{"other", "s", 0}, "foreign"}}},
		{form: "printable", bound: "s", reports: []classifiedReport{{HookReport{"other", "t", 0}, "foreign"}}},
	} {
		b, _ := json.Marshal(h)
		if _, err := DecodeHookView(b); err == nil {
			t.Fatal(string(b))
		}
	}
}
