package leadctl

import (
	"strings"
	"testing"
)

var capabilityKeys = []struct {
	key, label, reason string
	control            Control
}{
	{"d", "define", "define-not-offered", Control{Op: OpDefine}},
	{"s", "start", "start-not-offered", Control{Op: OpStart}},
	{"x", "stop", "stop-not-offered", Control{Op: OpStop}},
	{"m", "message", "inject-not-offered", Control{Op: OpInject}},
	{"w", "attach as writer", "writer-ticket-not-offered", Control{Op: OpTicket, Writer: true}},
	{"v", "attach as viewer", "viewer-ticket-not-offered", Control{Op: OpTicket}},
}

func restrictedLead() Lead {
	l := aRunning("alpha")
	l.Actions = make(map[Control]Availability)
	for _, k := range capabilityKeys {
		l.Actions[k.control] = Availability{Reason: k.reason}
	}
	return l
}

func TestCFCapsHintMarksUnavailable(t *testing.T) {
	b := newBackend(t)
	l := restrictedLead()
	l.Actions[Control{Op: OpTicket}] = Availability{Offered: true}
	b.listing.Leads = []Lead{l}
	r := newRig(t, b, 80, 24)
	for _, k := range capabilityKeys {
		label := "[" + k.key + "] " + k.label
		if k.key == "v" {
			r.seen(label)
			r.notSeen(label + " (")
		} else {
			r.seen(label + " (" + reasonText(k.reason) + ")")
		}
	}
}

func TestCFCapsPressShowsReasonNoCall(t *testing.T) {
	b := newBackend(t)
	b.listing.Leads = []Lead{restrictedLead()}
	r := newRig(t, b, 80, 24)
	check := func(key, reason string) {
		t.Helper()
		lists := b.lists
		r.key(key)
		if got := strings.TrimSpace(r.rows()[r.h-2]); got != reasonText(reason) {
			t.Fatalf("%s displayed %q, want %q", key, got, reasonText(reason))
		}
		if b.total() != 0 || b.lists != lists || r.m.FieldOpen() || len(r.actions) != 0 {
			t.Fatalf("%s opened a control or called the backend: %+v", key, b.calls)
		}
	}
	for _, k := range capabilityKeys {
		check(k.key, k.reason)
	}
	b.listing.Leads[0].Inject = false
	r.run(r.m.Refresh())
	check("m", "messages-off")
	r.m.MarkOffline()
	check("s", "offline")
}

func TestCFCapsDegradedOffersNothing(t *testing.T) {
	b := newBackend(t)
	l := aRunning("alpha")
	l.State, l.StateReason = "degraded", "client/invalid-row"
	l.Actions = make(map[Control]Availability)
	for _, k := range capabilityKeys {
		l.Actions[k.control] = Availability{Offered: true}
	}
	b.listing.Leads = []Lead{l}
	r := newRig(t, b, 80, 24)
	r.seen("invalid lead data")
	for _, k := range capabilityKeys {
		r.notSeen("[" + k.key + "]")
		r.key(k.key)
		r.seen(reasonText("offline"))
		if b.total() != 0 || b.lists != 1 || r.m.FieldOpen() || len(r.actions) != 0 {
			t.Fatalf("degraded row admitted %s", k.key)
		}
	}
}

func TestCFCapsRefreshBeforeConfirmation(t *testing.T) {
	for _, tc := range []struct {
		open, confirm string
		op            Op
	}{
		{"x", "y", OpStop}, {"m", "enter", OpInject},
	} {
		b := newBackend(t)
		b.listing.Leads = []Lead{aRunning("alpha")}
		r := newRig(t, b, 80, 24)
		r.key(tc.open)
		if !r.m.FieldOpen() {
			t.Fatalf("%s did not open a field", tc.open)
		}
		if tc.op == OpInject {
			r.typ("hello")
		}
		b.listing.Leads[0] = restrictedLead()
		r.run(r.m.Refresh())
		r.key(tc.confirm)
		reason := Control{Op: tc.op}.NotOfferedReason()
		if r.m.note != reasonText(reason) || b.total() != 0 || r.m.FieldOpen() {
			t.Fatalf("after %s: note %q, calls %+v", tc.confirm, r.m.note, b.calls)
		}
	}
}

func TestCFCapsRefreshBeforeTicketDispatch(t *testing.T) {
	b := newBackend(t)
	b.listing.Leads = []Lead{aRunning("alpha")}
	r := newRig(t, b, 80, 24)
	cmd, _ := r.m.Update(keyMsg("w"))
	if cmd == nil {
		t.Fatal("missing queued writer ticket")
	}
	l := restrictedLead()
	l.Actions[Control{Op: OpTicket}] = Availability{Offered: true}
	b.listing.Leads[0] = l
	r.run(r.m.Refresh())
	r.run(cmd)
	if b.total() != 0 || len(r.actions) != 0 {
		t.Fatal("viewer offer admitted a queued writer ticket")
	}
}

// A lead with no offer map offers nothing: a backend that forgot the map must
// not make every control, the writer ticket included, available.
func TestCFCapsNoOfferMapOffersNothing(t *testing.T) {
	b := newBackend(t)
	l := aRunning("alpha")
	l.Actions = nil
	b.listing.Leads = []Lead{l}
	r := newRig(t, b, 100, 24)
	for _, k := range capabilityKeys {
		if a := l.availability(k.control); a.Offered || a.Reason != k.reason {
			t.Errorf("%s: availability %+v, want not offered for %q", k.key, a, k.reason)
		}
		r.key(k.key)
		if b.total() != 0 || r.m.FieldOpen() || len(r.actions) != 0 {
			t.Fatalf("%s acted with no offer map: %+v", k.key, b.calls)
		}
		r.seen("[" + k.key + "] " + k.label + " (" + reasonText(k.reason) + ")")
	}
}

// OfferAll is every control, and each is a fresh map.
func TestOfferAll(t *testing.T) {
	a, b := OfferAll(), OfferAll()
	if len(a) != len(capabilityKeys) {
		t.Fatalf("%d controls", len(a))
	}
	for _, k := range capabilityKeys {
		if !a[k.control].Offered {
			t.Errorf("%s not offered", k.key)
		}
	}
	delete(a, Control{Op: OpDefine})
	if !b[Control{Op: OpDefine}].Offered {
		t.Error("OfferAll shares its map")
	}
}
