package leadctl

import (
	"strconv"
	"testing"
)

func TestDuplicateNamesReorderKeepsMessageTarget(t *testing.T) {
	a, b := aRunning("first"), aRunning("second")
	a.Key, b.Key = "shared", "shared"
	f := newBackend(t)
	f.listing.Leads = []Lead{a, b}
	r := newRig(t, f, 120, 30)
	r.key("j")
	r.key("m")
	r.typ("for the second lead")
	b.Key = "renamed"
	f.listing.Leads = []Lead{b, a}
	r.run(r.m.Refresh())
	r.seen("message to renamed")
	r.key("enter")
	calls := f.callsOf("inject")
	if len(calls) != 1 || calls[0].role != b.Role || calls[0].body != "for the second lead" || r.m.cursor != 0 {
		t.Fatalf("cursor %d, calls %+v", r.m.cursor, calls)
	}
}

func TestSelectionIdentityDisappearanceClearsDraftAndRequiresReselection(t *testing.T) {
	f := twoLeads(t)
	r := newRig(t, f, 120, 30)
	r.key("j")
	r.key("m")
	r.typ("private draft")
	rows := f.listing.Leads
	f.listing.Leads = rows[:1]
	r.run(r.m.Refresh())
	f.listing.Leads = rows
	r.run(r.m.Refresh())
	r.m.SetSize(90, 20)
	if l, ok := r.m.under(); ok || r.m.FieldOpen() {
		t.Fatalf("selection %+v, field open %v", l, r.m.FieldOpen())
	}
	for _, key := range []string{"enter", "m", "s", "v", "y"} {
		r.key(key)
	}
	if f.total() != 0 || r.m.FieldOpen() {
		t.Fatalf("stale selection used: %+v", f.calls)
	}
	r.key("j")
	r.key("j")
	r.key("m")
	if r.m.edit == nil || r.m.edit.text != "" {
		t.Fatal("expected an empty editor for the freshly selected lead")
	}
}

func TestEpochChangeInvalidatesPendingTicket(t *testing.T) {
	for _, when := range []string{"before dispatch", "after dispatch"} {
		t.Run(when, func(t *testing.T) {
			f := twoLeads(t)
			f.answer = func(call) (Receipt, error) {
				return Receipt{Op: OpTicket, Status: Done, Folder: "/run/lead", TicketFile: "/run/lead/ticket"}, nil
			}
			r := newRig(t, f, 120, 30)
			r.key("j")
			cmd, _ := r.m.Update(keyMsg("w"))
			if cmd == nil {
				t.Fatal("missing ticket request")
			}
			var reply any
			if when == "after dispatch" {
				f.inCmd = true
				reply = cmd()
				f.inCmd = false
			}
			f.listing.Leads[1].Epoch++
			r.run(r.m.Refresh())
			// Reappearance of the same role and epoch cannot revive the ticket.
			f.listing.Leads[1].Epoch--
			r.run(r.m.Refresh())
			if when == "before dispatch" {
				r.run(cmd)
				if f.total() != 0 {
					t.Fatal("queued ticket reached the backend")
				}
			} else {
				r.feed(reply)
			}
			if len(r.actions) != 0 {
				t.Fatalf("used a stale ticket: %+v", r.actions)
			}
		})
	}
}

func TestOfflineMessageReceiptSettlesTheOriginalEpoch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		receipt Receipt
		retry   bool
	}{
		{"refused", Receipt{Status: Refused}, true},
		{"not delivered", Receipt{Status: Done, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}, true},
		{"unknown", Receipt{Status: Uncertain}, false},
		{"earlier refusal", Receipt{Status: Refused, Earlier: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackend(t)
			f.listing.Leads = []Lead{aRunning("alpha")}
			f.answer = func(call) (Receipt, error) { return tc.receipt, nil }
			r := newRig(t, f, 120, 30)
			r.key("m")
			r.typ("hello")
			send, _ := r.m.Update(keyMsg("enter"))
			f.inCmd = true
			reply := send()
			f.inCmd = false

			f.listErr = errLost
			r.run(r.m.Refresh())
			r.feed(reply)
			f.listErr = nil
			r.run(r.m.Refresh())
			r.key("j")
			r.key("m")
			if r.m.FieldOpen() != tc.retry {
				t.Errorf("editor open = %v, want %v after receipt", r.m.FieldOpen(), tc.retry)
			}
			if tc.receipt.Outcome == OutcomeNotNow {
				r.seen("free (not delivered)")
			}
		})
	}
}

func TestSelectionIdentityRefusesDuplicateRoutingRoleBeyondDisplayLimit(t *testing.T) {
	f := twoLeads(t)
	r := newRig(t, f, 120, 30)
	r.key("x")
	for len(f.listing.Leads) < maxLeads {
		f.listing.Leads = append(f.listing.Leads, aLead("extra-"+strconv.Itoa(len(f.listing.Leads))))
	}
	duplicate := f.listing.Leads[0]
	duplicate.Epoch++
	f.listing.Leads = append(f.listing.Leads, duplicate)
	r.run(r.m.Refresh())
	r.key("y")
	if r.m.leftOut != 2 || r.m.FieldOpen() || f.total() != 0 {
		t.Fatalf("ambiguous role admitted: %d omitted, calls %+v", r.m.leftOut, f.calls)
	}
	for _, l := range r.m.leads {
		if l.Role == duplicate.Role {
			t.Fatal("ambiguous role still selectable")
		}
	}
}

func TestSelectionIdentityTicketRequiresSameSelectionAndReceiptEpoch(t *testing.T) {
	for _, move := range []bool{false, true} {
		f := twoLeads(t)
		f.answer = func(c call) (Receipt, error) {
			epoch := c.epoch
			if !move {
				epoch++
			}
			return Receipt{Op: OpTicket, Status: Done, Epoch: epoch, Folder: "/run/lead", TicketFile: "/run/lead/ticket"}, nil
		}
		r := newRig(t, f, 120, 30)
		r.key("j")
		cmd, _ := r.m.Update(keyMsg("v"))
		f.inCmd = true
		reply := cmd()
		f.inCmd = false
		if move {
			r.key("k")
			r.key("j")
		}
		r.feed(reply)
		if len(r.actions) != 0 {
			t.Fatalf("move %v: used a stale ticket: %+v", move, r.actions)
		}
	}
}
