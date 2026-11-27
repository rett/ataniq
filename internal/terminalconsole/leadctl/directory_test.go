package leadctl

import "testing"

func TestCF01(t *testing.T) {
	a := aRunning("shared")
	a.HomeID, a.ProjectID, a.ID, a.ConversationID = "east", "project", "lead", "conversation"
	b := a
	b.HomeID = "west"
	t.Run("EqualNamesTwoHomes", func(t *testing.T) {
		f := newBackend(t)
		f.listing.Leads = []Lead{a, b}
		r := newRig(t, f, 150, 24)
		r.key("j")
		renamed := b
		renamed.Key = "renamed"
		f.listing.Leads = []Lead{renamed, a}
		r.key("r")
		if l, ok := r.m.under(); !ok || l.HomeID != b.HomeID || l.Key != renamed.Key {
			t.Fatalf("selected %+v", l)
		}
		r.seen("east/shared", "west/renamed")
		// No role-only scratch call may act on either home's row.
		for _, key := range []string{"s", "m", "d", "x", "y", "w", "v"} {
			r.key(key)
		}
		if r.m.FieldOpen() || f.total() != 0 {
			t.Fatal("qualified selection opened a scratch control")
		}
	})
	t.Run("EpochChange", func(t *testing.T) {
		for _, change := range []func(*Lead){
			func(l *Lead) { l.Epoch++ },
			func(l *Lead) { l.ConversationID = "replacement" },
			func(l *Lead) { l.ProjectID = "replacement" },
			func(l *Lead) { l.ID = "replacement" },
			func(l *Lead) { l.HomeID = b.HomeID },
		} {
			local := a
			local.HomeID = ""
			f := newBackend(t)
			f.listing.Leads = []Lead{local}
			r := newRig(t, f, 140, 24)
			r.key("m")
			r.typ("local draft")
			r.seen("local draft")
			change(&f.listing.Leads[0])
			r.run(r.m.Refresh())
			r.key("enter")
			if _, ok := r.m.under(); ok || r.m.FieldOpen() || f.total() != 0 {
				t.Fatalf("draft survived replacement %+v", f.listing.Leads[0])
			}
		}
	})
	t.Run("Offline", func(t *testing.T) {
		f := newBackend(t)
		f.listing.Leads = []Lead{a, b}
		r := newRig(t, f, 150, 24)
		f.listing = Listing{Leads: []Lead{b}, OfflineHomes: []string{a.HomeID}}
		r.key("r")
		r.seen("stale", "east/shared", "west/shared")
		if _, ok := r.m.under(); ok {
			t.Fatal("offline selection survived")
		}
		r.key("j")
		if l, ok := r.m.under(); !ok || l.HomeID != b.HomeID {
			t.Fatal("online home could not be selected")
		}
		// A whole-backend outage also invalidates queued scratch commands.
		local := aRunning("local")
		f.listing = Listing{Leads: []Lead{local}}
		r.key("r")
		r.key("j")
		cmd, _ := r.m.Update(keyMsg("v"))
		if cmd == nil {
			t.Fatal("missing queued ticket")
		}
		f.listErr = errLost
		r.key("r")
		r.run(cmd)
		for _, k := range []string{"j", "d", "s", "v", "w", "m", "x", "y"} {
			r.key(k)
		}
		r.seen("stale")
		r.notSeen("[w] attach", "[s] start")
		if f.total() != 0 || len(r.actions) != 0 || r.m.FieldOpen() {
			t.Fatal("acted on a stale row")
		}
	})
}
