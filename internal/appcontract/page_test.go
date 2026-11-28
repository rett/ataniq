package appcontract

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"ataniq/internal/appmodel"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

var (
	pageInstance = Instance{Authority: "auth-a", StoreID: [16]byte{1}, IncarnationID: [16]byte{2}}
	pageProject  = appmodel.ProjectRef{Authority: "auth-a", ID: "project-a"}
	pageConv     = appmodel.ConversationRef{Authority: "auth-a", ID: "conv-a"}
	pageMaster   = [32]byte{7}
)

type pageFixture struct {
	clock  *fakeClock
	source *MemorySource
	reader *PageReader
}

func newPageFixture(t *testing.T, retention Retention) *pageFixture {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	src := NewMemorySource("lead-a", pageConv, clock.Now, retention)
	src.Grant("viewer-a", nil)
	reader, err := NewPageReader(pageInstance, pageMaster, src)
	if err != nil {
		t.Fatal(err)
	}
	return &pageFixture{clock, src, reader}
}

func (f *pageFixture) appendReplies(n int) {
	for i := 0; i < n; i++ {
		f.clock.Advance(time.Second)
		f.source.Append(Entry{Kind: KindReply, Text: "reply"})
	}
}

func pageReq(viewer string, dir Direction, cursor string, limit int) PageRequest {
	return PageRequest{Viewer: viewer, Project: pageProject, Conversation: pageConv, Lead: "lead-a", Direction: dir, Cursor: cursor, Limit: limit}
}

func mustRead(t *testing.T, r *PageReader, req PageRequest) Page {
	t.Helper()
	p, err := r.Read(req)
	if err != nil {
		t.Fatalf("Read(%+v): %v", req, err)
	}
	return p
}

func seqs(p Page) []uint64 {
	out := []uint64{}
	for _, e := range p.Entries {
		out = append(out, e.ID.Seq)
	}
	return out
}

func wantDomain(t *testing.T, err error, code appmodel.Code) {
	t.Helper()
	var d appmodel.DomainError
	if !errors.As(err, &d) || d.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

func wantSeqs(t *testing.T, what string, p Page, want ...uint64) {
	t.Helper()
	if got := seqs(p); !reflect.DeepEqual(got, append([]uint64{}, want...)) {
		t.Fatalf("%s: sequences %v, want %v", what, got, want)
	}
}

func TestCF04Page(t *testing.T) {
	t.Run("StableIDsAcrossAppend", func(t *testing.T) {
		f := newPageFixture(t, Retention{})
		f.appendReplies(5)
		first := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 3))
		wantSeqs(t, "first read", first, 3, 4, 5)
		want := appmodel.MessageID{Authority: "auth-a", Lead: "lead-a", Conversation: "conv-a", Generation: 1, Seq: 5}
		if got := first.Entries[2].ID; got != want || got.String() != "m1/auth-a/lead-a/conv-a/1/5" {
			t.Fatalf("newest ID %+v (%s), want %+v", got, got, want)
		}

		f.appendReplies(2)
		f.source.Grant("viewer-b", nil)
		again := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 5))
		wantSeqs(t, "refresh after append", again, 3, 4, 5, 6, 7)
		other := mustRead(t, f.reader, pageReq("viewer-b", Backward, "", 5))
		for i, e := range again.Entries {
			if e.ID != other.Entries[i].ID {
				t.Errorf("entry %d: viewers see %+v and %+v", i, e.ID, other.Entries[i].ID)
			}
			if i < 3 && e.ID != first.Entries[i].ID {
				t.Errorf("entry %d changed ID across append: %+v then %+v", i, first.Entries[i].ID, e.ID)
			}
		}
		for _, e := range again.Entries {
			back, err := appmodel.ParseMessageID(e.ID.String())
			if err != nil || back != e.ID {
				t.Errorf("ID %+v does not round-trip: %+v %v", e.ID, back, err)
			}
		}

		// A fresh segment keeps the tuple's other parts and changes only the
		// generation, and a cursor of the old segment resets.
		f.source.NewSegment()
		f.appendReplies(1)
		fresh := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 5))
		wantSeqs(t, "fresh segment", fresh, 1)
		if got := fresh.Entries[0].ID; got.Generation != 2 || got.Seq != 1 || got.Authority != "auth-a" || got.Lead != "lead-a" || got.Conversation != "conv-a" {
			t.Errorf("fresh segment ID %+v", got)
		}
		if p := mustRead(t, f.reader, pageReq("viewer-a", Backward, first.Older, 5)); p.State != PageReset {
			t.Errorf("cursor of the old segment: state %s", p.State)
		}
	})

	t.Run("NoDuplicateOrSkip", func(t *testing.T) {
		f := newPageFixture(t, Retention{})
		f.appendReplies(10)
		first := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 4))
		wantSeqs(t, "first", first, 7, 8, 9, 10)
		if first.State != PageMore || first.Older == "" || first.Newer == "" {
			t.Fatalf("first page %+v", first)
		}
		// Entries arrive between the two reads; they must not move the boundary.
		f.appendReplies(3)
		seen := map[uint64]int{}
		count := func(p Page) {
			for _, s := range seqs(p) {
				seen[s]++
			}
		}
		count(first)
		older := mustRead(t, f.reader, pageReq("viewer-a", Backward, first.Older, 4))
		wantSeqs(t, "older", older, 3, 4, 5, 6)
		count(older)
		oldest := mustRead(t, f.reader, pageReq("viewer-a", Backward, older.Older, 4))
		wantSeqs(t, "oldest", oldest, 1, 2)
		if oldest.State != PageEnd || oldest.Older != "" {
			t.Errorf("oldest page %+v", oldest)
		}
		count(oldest)

		newer := mustRead(t, f.reader, pageReq("viewer-a", Forward, first.Newer, 2))
		wantSeqs(t, "newer", newer, 11, 12)
		count(newer)
		more := mustRead(t, f.reader, pageReq("viewer-a", Forward, newer.Newer, 2))
		wantSeqs(t, "newer, second", more, 13)
		count(more)
		caught := mustRead(t, f.reader, pageReq("viewer-a", Forward, more.Newer, 2))
		wantSeqs(t, "caught up", caught)
		if caught.Newer == "" || caught.State != PageMore {
			t.Errorf("a caught-up forward read needs a cursor to wait on: %+v", caught)
		}
		f.appendReplies(1)
		late := mustRead(t, f.reader, pageReq("viewer-a", Forward, caught.Newer, 2))
		wantSeqs(t, "after waiting", late, 14)
		count(late)

		for s := uint64(1); s <= 14; s++ {
			if seen[s] != 1 {
				t.Errorf("sequence %d was returned %d times", s, seen[s])
			}
		}
		if len(seen) != 14 {
			t.Errorf("returned %d distinct sequences, want 14", len(seen))
		}
	})

	t.Run("FourStates", func(t *testing.T) {
		// end: an empty new segment, and a backward read to the first entry.
		f := newPageFixture(t, Retention{})
		empty := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 0))
		if empty.State != PageEnd || len(empty.Entries) != 0 || empty.Older != "" || empty.Newer == "" {
			t.Errorf("empty segment: %+v", empty)
		}
		f.appendReplies(3)
		whole := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 3))
		if whole.State != PageEnd || whole.Older != "" {
			t.Errorf("a page holding the whole segment: state %s, older %q", whole.State, whole.Older)
		}

		// gap: retention by count, then by age, then seen from a forward read.
		g := newPageFixture(t, Retention{MaxEntries: 3})
		g.appendReplies(5)
		gap := mustRead(t, g.reader, pageReq("viewer-a", Backward, "", 10))
		wantSeqs(t, "count gap", gap, 3, 4, 5)
		if gap.State != PageGap || gap.Older != "" {
			t.Errorf("count gap: state %s, older %q", gap.State, gap.Older)
		}
		partial := mustRead(t, g.reader, pageReq("viewer-a", Backward, "", 2))
		if partial.State != PageMore || partial.Older == "" {
			t.Fatalf("partial page %+v", partial)
		}
		last := mustRead(t, g.reader, pageReq("viewer-a", Backward, partial.Older, 2))
		wantSeqs(t, "last retained", last, 3)
		if last.State != PageGap {
			t.Errorf("reaching the oldest retained entry above pruned ones: state %s", last.State)
		}
		g.appendReplies(1)
		if pruned := mustRead(t, g.reader, pageReq("viewer-a", Backward, partial.Older, 2)); pruned.State != PageGap || len(pruned.Entries) != 0 {
			t.Errorf("a cursor whose older entries were since pruned: %+v", pruned)
		}
		behind := mustRead(t, g.reader, pageReq("viewer-a", Forward, gap.Newer, 10))
		wantSeqs(t, "forward after one more append", behind, 6)
		g.appendReplies(5)
		missed := mustRead(t, g.reader, pageReq("viewer-a", Forward, behind.Newer, 10))
		wantSeqs(t, "forward past pruned entries", missed, 9, 10, 11)
		if missed.State != PageGap {
			t.Errorf("a forward read that missed pruned entries: state %s", missed.State)
		}

		age := newPageFixture(t, Retention{MaxAge: time.Hour})
		age.appendReplies(2)
		age.clock.Advance(30 * time.Minute)
		age.appendReplies(1)
		age.clock.Advance(40 * time.Minute)
		aged := mustRead(t, age.reader, pageReq("viewer-a", Backward, "", 10))
		wantSeqs(t, "age gap", aged, 3)
		if aged.State != PageGap {
			t.Errorf("age gap: state %s", aged.State)
		}
		age.clock.Advance(2 * time.Hour)
		none := mustRead(t, age.reader, pageReq("viewer-a", Backward, "", 10))
		if none.State != PageGap || len(none.Entries) != 0 || none.Newer == "" {
			t.Errorf("all entries aged out: %+v", none)
		}

		// reset: an authorized reader with a continuation that cannot be used.
		for name, cursor := range map[string]string{
			"garbage":   "not-a-cursor",
			"truncated": whole.Newer[:len(whole.Newer)-4],
			"spaces":    strings.Repeat(" ", 146),
		} {
			p, err := f.reader.Read(pageReq("viewer-a", Backward, cursor, 3))
			if err != nil || p.State != PageReset || len(p.Entries) != 0 || p.Older != "" || p.Newer != "" {
				t.Errorf("%s: %+v, %v", name, p, err)
			}
		}

		// withheld: a marker with no content, paged past; hidden: nothing.
		w := newPageFixture(t, Retention{})
		w.source.Grant("viewer-b", func(e Entry) Disclosure {
			switch e.Seq {
			case 2:
				return Withheld
			case 4:
				return Hidden
			}
			return Disclosed
		})
		key := appmodel.CommandKey{Authority: "auth-a", EnrolledClientID: "client-a", CommandID: "cmd-a"}
		w.source.Append(Entry{Kind: KindReply, Text: "one"})
		w.source.Append(Entry{Kind: KindReceipt, Text: "secret", Command: key, Receipt: &appmodel.AdmissionReceipt{Command: key, AdmittedRevision: "r1", Cursor: "c1"}})
		w.source.Append(Entry{Kind: KindReply, Text: "three"})
		w.source.Append(Entry{Kind: KindSystem, Text: "hidden four"})
		w.source.Append(Entry{Kind: KindReply, Text: "five"})
		w.source.SetStatus(appmodel.CurrentStatus{Command: key, Revision: "s1", State: appmodel.Queued})
		var order []uint64
		var marker PageEntry
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			p := mustRead(t, w.reader, pageReq("viewer-b", Backward, cursor, 1))
			for _, e := range p.Entries {
				order = append([]uint64{e.ID.Seq}, order...)
				if e.Kind == KindWithheld {
					marker = e
				}
			}
			if p.State == PageEnd {
				break
			}
			if p.State != PageMore || p.Older == "" {
				t.Fatalf("paging past a withheld entry stopped: %+v", p)
			}
			cursor = p.Older
		}
		if !reflect.DeepEqual(order, []uint64{1, 2, 3, 5}) {
			t.Errorf("viewer-b saw sequences %v, want [1 2 3 5]: withheld stays, hidden vanishes", order)
		}
		wantMarker := PageEntry{ID: appmodel.MessageID{Authority: "auth-a", Lead: "lead-a", Conversation: "conv-a", Generation: 1, Seq: 2}, Kind: KindWithheld}
		if !reflect.DeepEqual(marker, wantMarker) {
			t.Errorf("withheld marker %+v, want only %+v", marker, wantMarker)
		}
		if gotAll := mustRead(t, w.reader, pageReq("viewer-b", Backward, "", 10)); gotAll.State != PageEnd {
			t.Errorf("a hidden entry made a %s page; visible IDs may skip without a gap", gotAll.State)
		}
		full := mustRead(t, w.reader, pageReq("viewer-a", Backward, "", 10))
		if len(full.Entries) != 5 {
			t.Errorf("a viewer with full disclosure sees %d entries", len(full.Entries))
		}
	})

	t.Run("ReceiptUnchangedAsStatusAdvances", func(t *testing.T) {
		f := newPageFixture(t, Retention{})
		key := appmodel.CommandKey{Authority: "auth-a", EnrolledClientID: "client-a", CommandID: "cmd-a"}
		receipt := appmodel.AdmissionReceipt{Command: key, Digest: [32]byte{9, 8, 7}, AdmittedRevision: "r7", Cursor: "hist-1"}
		f.appendReplies(1)
		f.source.Append(Entry{Kind: KindInstruction, Text: "do it", Command: key})
		f.source.Append(Entry{Kind: KindReceipt, Command: key, Receipt: &receipt})
		f.source.SetStatus(appmodel.CurrentStatus{Command: key, Revision: "s1", State: appmodel.Queued})
		before := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 10))
		wantSeqs(t, "before", before, 1, 2, 3)
		if before.Entries[2].Status == nil || before.Entries[2].Status.State != appmodel.Queued || before.Entries[1].Status == nil {
			t.Fatalf("status not shown: %+v", before.Entries)
		}

		f.source.SetStatus(appmodel.CurrentStatus{Command: key, Revision: "s2", State: appmodel.Applied})
		f.appendReplies(2)
		after := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 10))
		wantSeqs(t, "after", after, 1, 2, 3, 4, 5)
		for i := 1; i <= 2; i++ {
			b, a := before.Entries[i], after.Entries[i]
			if a.ID != b.ID || a.Kind != b.Kind || !reflect.DeepEqual(a.Command, b.Command) {
				t.Errorf("entry %d changed: %+v then %+v", i, b, a)
			}
			if a.Status == nil || a.Status.Revision != "s2" || a.Status.State != appmodel.Applied || a.Status.Command != key {
				t.Errorf("entry %d status %+v", i, a.Status)
			}
		}
		b, a := before.Entries[2], after.Entries[2]
		if b.Receipt == nil || a.Receipt == nil || *a.Receipt != *b.Receipt || *a.Receipt != receipt {
			t.Errorf("receipt changed: %+v then %+v", b.Receipt, a.Receipt)
		}
		if before.Entries[1].Receipt != nil {
			t.Error("an instruction entry carries a receipt")
		}
		if b.Status.Revision != "s1" {
			t.Errorf("an earlier page's status was rewritten: %+v", b.Status)
		}

		// A returned receipt is a copy: changing it cannot alter the record.
		a.Receipt.AdmittedRevision = "forged"
		receipt.Cursor = "forged"
		again := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 10))
		if got := again.Entries[2].Receipt; got.AdmittedRevision != "r7" || got.Cursor != "hist-1" {
			t.Errorf("receipt after caller edits: %+v", got)
		}

		// Display retention does not delete the command's status.
		g := newPageFixture(t, Retention{MaxEntries: 1})
		g.source.Append(Entry{Kind: KindReceipt, Command: key, Receipt: &receipt})
		g.source.SetStatus(appmodel.CurrentStatus{Command: key, Revision: "s1", State: appmodel.Queued})
		g.appendReplies(2)
		if p := mustRead(t, g.reader, pageReq("viewer-a", Backward, "", 10)); p.State != PageGap || len(p.Entries) != 1 {
			t.Errorf("after eviction: %+v", p)
		}
		if st := g.source.statuses[key]; st.Revision != "s1" {
			t.Errorf("eviction touched the status: %+v", st)
		}
	})

	t.Run("ResetForForeignInstanceViewerDirection", func(t *testing.T) {
		f := newPageFixture(t, Retention{})
		f.appendReplies(6)
		f.source.Grant("viewer-b", nil)
		first := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 2))
		if first.Older == "" || first.Newer == "" {
			t.Fatalf("first page %+v", first)
		}
		// The control: the same cursor decodes for its own reader.
		if p := mustRead(t, f.reader, pageReq("viewer-a", Backward, first.Older, 2)); p.State != PageMore || len(p.Entries) != 2 {
			t.Fatalf("control read: %+v", p)
		}
		if p := mustRead(t, f.reader, pageReq("viewer-a", Forward, first.Newer, 2)); p.State != PageMore {
			t.Fatalf("forward control: %+v", p)
		}

		reset := func(name string, r *PageReader, req PageRequest) {
			t.Helper()
			p, err := r.Read(req)
			if err != nil || p.State != PageReset || len(p.Entries) != 0 || p.Older != "" || p.Newer != "" {
				t.Errorf("%s: %+v, %v", name, p, err)
			}
		}
		for name, other := range map[string]Instance{
			"other store":       {Authority: "auth-a", StoreID: [16]byte{9}, IncarnationID: [16]byte{2}},
			"other incarnation": {Authority: "auth-a", StoreID: [16]byte{1}, IncarnationID: [16]byte{9}},
		} {
			r, err := NewPageReader(other, pageMaster, f.source)
			if err != nil {
				t.Fatal(err)
			}
			reset(name+", backward", r, pageReq("viewer-a", Backward, first.Older, 2))
			reset(name+", forward", r, pageReq("viewer-a", Forward, first.Newer, 2))
		}
		otherMaster, err := NewPageReader(pageInstance, [32]byte{8}, f.source)
		if err != nil {
			t.Fatal(err)
		}
		reset("other master", otherMaster, pageReq("viewer-a", Backward, first.Older, 2))

		reset("foreign viewer, backward", f.reader, pageReq("viewer-b", Backward, first.Older, 2))
		reset("foreign viewer, forward", f.reader, pageReq("viewer-b", Forward, first.Newer, 2))
		reset("backward token read forward", f.reader, pageReq("viewer-a", Forward, first.Older, 2))
		reset("forward token read backward", f.reader, pageReq("viewer-a", Backward, first.Newer, 2))
		foreignProject := pageReq("viewer-a", Backward, first.Older, 2)
		foreignProject.Project.ID = "project-b"
		reset("foreign project", f.reader, foreignProject)

		// A reader who may not read gets an error, never a reset that would
		// say a cursor was once valid, and never a decode of the token.
		_, err = f.reader.Read(pageReq("viewer-c", Backward, first.Older, 2))
		wantDomain(t, err, appmodel.HiddenOrAbsent)
		_, err = f.reader.Read(pageReq("viewer-c", Forward, "garbage", 2))
		wantDomain(t, err, appmodel.HiddenOrAbsent)

		// A change of disclosure projection resets an authorized reader.
		f.source.Grant("viewer-a", func(e Entry) Disclosure {
			if e.Seq == 1 {
				return Withheld
			}
			return Disclosed
		})
		reset("changed projection, backward", f.reader, pageReq("viewer-a", Backward, first.Older, 2))
		reset("changed projection, forward", f.reader, pageReq("viewer-a", Forward, first.Newer, 2))
		fresh := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 10))
		if fresh.State != PageEnd || len(fresh.Entries) != 6 || fresh.Entries[0].Kind != KindWithheld {
			t.Errorf("fresh snapshot after the reset: %+v", fresh)
		}

		// Revocation ends reads before any cursor is looked at.
		f.source.Revoke("viewer-a")
		_, err = f.reader.Read(pageReq("viewer-a", Backward, fresh.Newer, 2))
		wantDomain(t, err, appmodel.HiddenOrAbsent)
	})
}

func TestPageRequestShape(t *testing.T) {
	f := newPageFixture(t, Retention{})
	f.appendReplies(2)
	good := pageReq("viewer-a", Backward, "", 1)
	for name, mod := range map[string]func(*PageRequest){
		"no viewer":             func(r *PageRequest) { r.Viewer = "" },
		"bad viewer":            func(r *PageRequest) { r.Viewer = "a/b" },
		"no lead":               func(r *PageRequest) { r.Lead = "" },
		"foreign project":       func(r *PageRequest) { r.Project.Authority = "auth-b" },
		"foreign conversation":  func(r *PageRequest) { r.Conversation.Authority = "auth-b" },
		"no direction":          func(r *PageRequest) { r.Direction = "" },
		"unknown direction":     func(r *PageRequest) { r.Direction = "sideways" },
		"forward without token": func(r *PageRequest) { r.Direction = Forward },
		"negative limit":        func(r *PageRequest) { r.Limit = -1 },
	} {
		req := good
		mod(&req)
		if _, err := f.reader.Read(req); err == nil {
			t.Errorf("%s: no error", name)
		} else {
			wantDomain(t, err, appmodel.InvalidInput)
		}
	}
	// An unknown conversation or lead is hidden, like an unauthorized viewer.
	other := good
	other.Lead = "lead-b"
	_, err := f.reader.Read(other)
	wantDomain(t, err, appmodel.HiddenOrAbsent)
	other = good
	other.Conversation.ID = "conv-b"
	_, err = f.reader.Read(other)
	wantDomain(t, err, appmodel.HiddenOrAbsent)

	if _, err := NewPageReader(Instance{}, pageMaster, f.source); err == nil {
		t.Error("a reader without an authority")
	}
	if _, err := NewPageReader(pageInstance, pageMaster, nil); err == nil {
		t.Error("a reader without a source")
	}
}

func TestPageBounds(t *testing.T) {
	f := newPageFixture(t, Retention{})
	f.appendReplies(MaxPageEntries + 20)
	p := mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 0))
	if len(p.Entries) != DefaultPageEntries {
		t.Errorf("default limit: %d entries", len(p.Entries))
	}
	p = mustRead(t, f.reader, pageReq("viewer-a", Backward, "", 100000))
	if len(p.Entries) != MaxPageEntries || p.State != PageMore {
		t.Errorf("the maximum: %d entries, state %s", len(p.Entries), p.State)
	}

	// Large texts stop a page by bytes, and a page is never empty for it.
	b := newPageFixture(t, Retention{})
	text := strings.Repeat("x", 60<<10)
	for i := 0; i < 9; i++ {
		b.source.Append(Entry{Kind: KindReply, Text: text})
	}
	p = mustRead(t, b.reader, pageReq("viewer-a", Backward, "", 50))
	wantSeqs(t, "byte bound", p, 6, 7, 8, 9)
	if p.State != PageMore {
		t.Errorf("state %s", p.State)
	}
	p = mustRead(t, b.reader, pageReq("viewer-a", Backward, p.Older, 50))
	wantSeqs(t, "byte bound, next", p, 2, 3, 4, 5)
	huge := newPageFixture(t, Retention{})
	huge.source.Append(Entry{Kind: KindReply, Text: strings.Repeat("y", appmodel.MaxTextBytes)})
	if p = mustRead(t, huge.reader, pageReq("viewer-a", Backward, "", 50)); len(p.Entries) != 1 {
		t.Errorf("a largest text: %d entries", len(p.Entries))
	}
}

type fixedSource struct{ s Snapshot }

func (f fixedSource) Snapshot(string, appmodel.ConversationRef, string) (Snapshot, error) {
	return f.s, nil
}

type failingSource struct{ err error }

func (f failingSource) Snapshot(string, appmodel.ConversationRef, string) (Snapshot, error) {
	return Snapshot{}, f.err
}

func TestPageRefusesInvalidSnapshots(t *testing.T) {
	key := appmodel.CommandKey{Authority: "auth-a", EnrolledClientID: "client-a", CommandID: "cmd-a"}
	rc := &appmodel.AdmissionReceipt{Command: key}
	ok := Snapshot{Segment: 1, Projection: 1, Start: 1, Floor: 1, Head: 2, Entries: []Entry{{Seq: 1, Kind: KindReply}, {Seq: 2, Kind: KindSystem}}}
	read := func(s Snapshot) error {
		r, err := NewPageReader(pageInstance, pageMaster, fixedSource{s})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.Read(pageReq("viewer-a", Backward, "", 5))
		return err
	}
	if err := read(ok); err != nil {
		t.Fatalf("control: %v", err)
	}
	edit := func(f func(*Snapshot)) Snapshot {
		s := ok
		s.Entries = append([]Entry(nil), ok.Entries...)
		f(&s)
		return s
	}
	for name, s := range map[string]Snapshot{
		"no segment":         edit(func(s *Snapshot) { s.Segment = 0 }),
		"no start":           edit(func(s *Snapshot) { s.Start = 0 }),
		"floor below start":  edit(func(s *Snapshot) { s.Start = 2 }),
		"floor above head":   edit(func(s *Snapshot) { s.Floor = 4 }),
		"entry below floor":  edit(func(s *Snapshot) { s.Floor = 2 }),
		"entry above head":   edit(func(s *Snapshot) { s.Head = 1 }),
		"repeated sequence":  edit(func(s *Snapshot) { s.Entries[1].Seq = 1 }),
		"unordered":          edit(func(s *Snapshot) { s.Entries[0], s.Entries[1] = s.Entries[1], s.Entries[0] }),
		"withheld kind":      edit(func(s *Snapshot) { s.Entries[0].Kind = KindWithheld }),
		"unknown kind":       edit(func(s *Snapshot) { s.Entries[0].Kind = "x" }),
		"unknown disclosure": edit(func(s *Snapshot) { s.Entries[0].Disclosure = 9 }),
		"reply with a key":   edit(func(s *Snapshot) { s.Entries[0].Command = key }),
		"instruction no key": edit(func(s *Snapshot) { s.Entries[0].Kind = KindInstruction }),
		"receipt no receipt": edit(func(s *Snapshot) { s.Entries[0].Kind, s.Entries[0].Command = KindReceipt, key }),
		"receipt wrong key": edit(func(s *Snapshot) {
			s.Entries[0].Kind, s.Entries[0].Command, s.Entries[0].Receipt = KindReceipt, appmodel.CommandKey{Authority: "auth-a", EnrolledClientID: "client-b", CommandID: "cmd-a"}, rc
		}),
		"foreign authority": edit(func(s *Snapshot) {
			s.Entries[0].Kind, s.Entries[0].Command = KindInstruction, appmodel.CommandKey{Authority: "auth-b", EnrolledClientID: "c", CommandID: "d"}
		}),
		"invalid text": edit(func(s *Snapshot) { s.Entries[0].Text = "\xff" }),
		"status of other key": edit(func(s *Snapshot) {
			s.Statuses = map[appmodel.CommandKey]appmodel.CurrentStatus{key: {Command: appmodel.CommandKey{}}}
		}),
	} {
		if err := read(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// A source error that is not a domain code is hidden, and never leaks its text.
	r, err := NewPageReader(pageInstance, pageMaster, failingSource{errors.New("secret detail")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Read(pageReq("viewer-a", Backward, "", 5))
	wantDomain(t, err, appmodel.HiddenOrAbsent)
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error text %q", err)
	}
	r, _ = NewPageReader(pageInstance, pageMaster, failingSource{appmodel.DomainError{Code: appmodel.PermissionDenied}})
	_, err = r.Read(pageReq("viewer-a", Backward, "", 5))
	wantDomain(t, err, appmodel.PermissionDenied)
}

// TestPageCursorContexts holds that every part of the page context changes the
// digest, so a token of one read cannot open another.
func TestPageCursorContexts(t *testing.T) {
	base := pageCursor{viewer: "v", project: "p", conversation: "c", lead: "l", projection: 3}
	digest := func(c pageCursor) [32]byte {
		d, err := cursorDigest(pageInstance, c)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	seen := map[[32]byte]string{digest(base): "base"}
	for name, c := range map[string]pageCursor{
		"viewer":       {viewer: "w", project: "p", conversation: "c", lead: "l", projection: 3},
		"project":      {viewer: "v", project: "q", conversation: "c", lead: "l", projection: 3},
		"conversation": {viewer: "v", project: "p", conversation: "d", lead: "l", projection: 3},
		"lead":         {viewer: "v", project: "p", conversation: "c", lead: "m", projection: 3},
		"direction":    {viewer: "v", project: "p", conversation: "c", lead: "l", forward: true, projection: 3},
		"projection":   {viewer: "v", project: "p", conversation: "c", lead: "l", projection: 4},
	} {
		d := digest(c)
		if other, dup := seen[d]; dup {
			t.Errorf("%s has the digest of %s", name, other)
		}
		seen[d] = name
	}
	// Not the digest of an events cursor over the same names.
	if e, _ := cursorDigest(pageInstance, eventsCursor{"v", "p", "c"}); e == digest(base) {
		t.Error("a page cursor shares an events cursor's digest")
	}
	for _, bad := range []pageCursor{{}, {viewer: "v", project: "p", conversation: "c"}, {viewer: "v", project: "p", conversation: "c", lead: "l", projection: 1 << 63}} {
		if _, err := cursorDigest(pageInstance, bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	codec, err := newCursorCodec(pageMaster)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.mint(pageInstance, base, 0, 1, 5); err == nil {
		t.Error("a backward cursor at position 0")
	}
	if _, err := codec.mint(pageInstance, base, 1, 0, 5); err == nil {
		t.Error("a cursor with no segment generation")
	}
	fwd := base
	fwd.forward = true
	tok, err := codec.mint(pageInstance, fwd, 0, 1, 0)
	if err != nil {
		t.Fatalf("a forward cursor of an empty segment: %v", err)
	}
	if pos, err := codec.decode(pageInstance, fwd, tok, 1, 0); err != nil || pos != 0 {
		t.Errorf("decode: %d, %v", pos, err)
	}
	if _, err := codec.decode(pageInstance, fwd, tok, 2, 0); err == nil {
		t.Error("another segment generation decoded")
	}
}
