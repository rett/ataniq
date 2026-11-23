package seatlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// hostileNote is a lead's note made to break a page or a terminal: markup, a
// link, terminal controls and a direction override.
const hostileNote = "<script>alert(1)</script><a href=\"https://evil.example/\">tap</a>\n\x1b[2J\u202eevil"

// vector reads the pending record of the record package's test vectors: a
// feature finish whose paths include one with an escape byte and one with
// markup characters. Its note is set to the hostile one; the note is not
// under the binding's digest.
func vector(t *testing.T) (record.Pending, []byte) {
	t.Helper()
	b, err := os.ReadFile("../approval/record/testdata/pending-valid.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := record.DecodePending(b)
	if err != nil {
		t.Fatal(err)
	}
	p.Note = record.Text(hostileNote)
	out, err := record.EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, out
}

// fakeReads is the daemon's side of the reads on the gate: owner.list,
// owner.show and owner.paths over one pending record, with what each answers
// replaceable by a test.
type fakeReads struct {
	mu      sync.Mutex
	pending record.Pending
	bytes   []byte
	// show, when set, is the result of owner.show as raw JSON.
	show json.RawMessage
	// pageID, when set, is the id a page names.
	pageID string
	// ops are the operations asked, and pageReviews the review digests the
	// pages were asked for.
	ops         []string
	pageReviews []string
}

func (f *fakeReads) handle(_ context.Context, _ seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, op)
	switch op {
	case ownerwire.OpList:
		return ownerwire.EncodeListing(ownerwire.Listing{
			Pending: []ownerwire.PendingItem{{
				ID: f.pending.ID, SHA256: record.Digest(f.bytes), Project: f.pending.Binding.Project,
				Verb: f.pending.Binding.Verb, Feature: f.pending.Binding.Argv[0],
				RequestedAt: f.pending.Binding.RequestedAt, PendingExpiresAt: f.pending.Binding.PendingExpiresAt, Held: true,
			}},
			Recent: []ownerwire.RecentItem{{ID: "ap-20261119-0001", Project: "project-a", Verb: record.VerbBranchPrune, State: record.StateDenied,
				Line: "denied <b>at</b> the desk", At: f.pending.Binding.RequestedAt}},
			Rules:     []ownerwire.Rule{},
			NotListed: 3,
		})
	case ownerwire.OpShow:
		q, err := ownerwire.DecodeShowParams(params)
		if err != nil {
			return nil, seatgate.NewCodedError("bad-params")
		}
		if q.ID != f.pending.ID {
			return nil, seatgate.NewCodedError("approval-void")
		}
		if f.show != nil {
			return f.show, nil
		}
		return ownerwire.EncodeShowResult(ownerwire.ShowResult{
			Record:  ownerwire.NewRecord(f.bytes),
			Outcome: &ownerwire.Outcome{State: record.StatePending, Line: "the door refused <i>the last one</i>"},
		})
	case ownerwire.OpPaths:
		q, err := ownerwire.DecodePageParams(params)
		if err != nil {
			return nil, seatgate.NewCodedError("bad-params")
		}
		f.pageReviews = append(f.pageReviews, q.ReviewDigest)
		if q.ReviewDigest != f.pending.Binding.Summary.ReviewDigest {
			return nil, seatgate.NewCodedError("review-changed")
		}
		id := q.ID
		if f.pageID != "" {
			id = f.pageID
		}
		return ownerwire.EncodePathsPage(ownerwire.PathsPage{ID: id, ReviewDigest: q.ReviewDigest, Page: q.Page, Pages: 2,
			Paths: f.pending.Binding.Summary.Paths[:2]})
	}
	return nil, seatgate.NewCodedError("unknown-op")
}

// readGate serves fakeReads on a gate with the capabilities given and
// returns the link to it.
func readGate(t *testing.T, caps []string) (*fakeReads, *Daemon, *gate) {
	t.Helper()
	p, b := vector(t)
	f := &fakeReads{pending: p, bytes: b}
	seat := newIdentity(t, "seat")
	g := serveGateCaps(t, []string{seat.Fingerprint()}, caps, f.handle)
	return f, dialPinned(t, g, seat), g
}

// TestSeatLinkCardFromTheRecordBytes: the card the phone gets is built from
// the record's bytes with the card model: the host's fields as the record
// holds them, and the lead's text in the escaped form the desk shows.
func TestSeatLinkCardFromTheRecordBytes(t *testing.T) {
	f, d, _ := readGate(t, []string{"read"})
	c, err := d.Show(context.Background(), f.pending.ID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	b := f.pending.Binding
	if c.ID != f.pending.ID || c.Project != b.Project || c.Verb != b.Verb || c.Feature != "sample-feature" ||
		c.Target != "develop" || c.Class != b.Class || c.Role != b.Subject.RoleName || c.BindingSHA256 != f.pending.BindingSHA256 {
		t.Errorf("card %+v", c)
	}
	if c.State != "pending" || c.Line != "the door refused <i>the last one</i>" || c.Ended {
		t.Errorf("after: %q %q %v", c.State, c.Line, c.Ended)
	}
	if c.Diff == nil || len(c.Diff.Rows) != len(b.Summary.Paths) || c.Diff.Files != b.Summary.Files {
		t.Fatalf("diff %+v", c.Diff)
	}
	shown := map[string]bool{}
	for _, r := range c.Diff.Rows {
		shown[fmtLead(r.Path)] = true
	}
	// The path with an escape byte shows it escaped, as the desk does; the
	// app's Text then shows that text as it is.
	if !shown[`tools/\x1b[2Jclear`] || !shown["lib/seat_read.py"] {
		t.Errorf("rows %v", shown)
	}
	if len(c.Note) != 2 || strings.ContainsAny(fmtLead(c.Note[1]), "\x1b\u202e") || !strings.Contains(fmtLead(c.Note[0]), "<script>") {
		t.Errorf("note %v", c.Note)
	}
	if len(c.Plan) != 3 || len(c.Flagged) == 0 || len(c.Lists) != 4 || len(c.Refs) != 1 {
		t.Errorf("plan %d, flagged %d, lists %d, refs %d", len(c.Plan), len(c.Flagged), len(c.Lists), len(c.Refs))
	}
}

// fmtLead is how the app shows a Lead: through its Text.
func fmtLead(l seatapp.Lead) string {
	var b strings.Builder
	l.Format(&fmtState{&b}, 'v')
	return b.String()
}

// fmtState is the least fmt.State that Format needs.
type fmtState struct{ b *strings.Builder }

func (s *fmtState) Write(p []byte) (int, error) { return s.b.Write(p) }
func (s *fmtState) Width() (int, bool)          { return 0, false }
func (s *fmtState) Precision() (int, bool)      { return 0, false }
func (s *fmtState) Flag(int) bool               { return false }

// TestSeatLinkCardIsNeverRenderedElsewhere: a show whose bytes are not their
// digest's, whose record is another card's, whose binding is not its
// digest's, or which carries anything beside the record, is no card.
func TestSeatLinkCardIsNeverRenderedElsewhere(t *testing.T) {
	_, good := vector(t)
	other, _ := vector(t)
	other.ID = "ap-20261120-0008"
	otherBytes, err := record.EncodePending(other)
	if err != nil {
		t.Fatal(err)
	}
	badBinding, err := os.ReadFile("../approval/record/testdata/pending-invalid-binding-digest.json")
	if err != nil {
		t.Fatal(err)
	}
	// A record that decodes, with its digests right, and that the card model
	// refuses: a host string it would have to escape.
	refused, _ := vector(t)
	refused.Binding.Project = "project\u202ea"
	if refused.BindingSHA256, err = record.BindingDigest(refused.Binding); err != nil {
		t.Fatal(err)
	}
	refusedBytes, err := record.EncodePending(refused)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := record.DecodePending(refusedBytes); err != nil {
		t.Fatalf("the refused record does not decode: %v", err)
	}
	enc := func(r ownerwire.ShowResult) json.RawMessage {
		b, err := ownerwire.EncodeShowResult(r)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	withDigest := func(b []byte, digest string) json.RawMessage {
		out, _ := json.Marshal(map[string]any{"record": map[string]any{"bytes": b, "sha256": digest}, "held": false})
		return out
	}
	for name, show := range map[string]json.RawMessage{
		"another digest":      withDigest(good, record.Digest([]byte("other"))),
		"another card":        enc(ownerwire.ShowResult{Record: ownerwire.NewRecord(otherBytes)}),
		"binding not digest":  withDigest(badBinding, record.Digest(badBinding)),
		"a rendered card":     json.RawMessage(strings.Replace(string(enc(ownerwire.ShowResult{Record: ownerwire.NewRecord(good)})), `"held":false`, `"held":false,"html":"<p>approve</p>"`, 1)),
		"no record":           json.RawMessage(`{"held":false}`),
		"refused by the card": enc(ownerwire.ShowResult{Record: ownerwire.NewRecord(refusedBytes)}),
	} {
		f, d, _ := readGate(t, []string{"read"})
		f.show = show
		c, err := d.Show(context.Background(), f.pending.ID)
		if err == nil || c.ID != "" {
			t.Errorf("%s: a card was made: %v", name, err)
		}
		if want := errReadAnswer; name == "refused by the card" && !errors.Is(err, errCard) || name != "refused by the card" && !errors.Is(err, want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestSeatLinkPagesOfTheCardsReview: a page of the path list is asked for the
// review the card was shown with, learnt by showing the card when the link
// has not; a page of another card, or of another review, is not taken.
func TestSeatLinkPagesOfTheCardsReview(t *testing.T) {
	f, d, _ := readGate(t, []string{"read"})
	p, err := d.Pages(context.Background(), f.pending.ID, 1)
	if err != nil {
		t.Fatalf("pages: %v", err)
	}
	if p.N != 1 || p.Pages != 2 || len(p.Rows) != 2 || fmtLead(p.Rows[0].Path) != "lib/seat_read.py" {
		t.Errorf("page %+v", p)
	}
	if strings.Join(f.ops, ",") != "owner.show,owner.paths" || f.pageReviews[0] != f.pending.Binding.Summary.ReviewDigest {
		t.Errorf("ops %v, reviews %v", f.ops, f.pageReviews)
	}
	// The review is remembered: the next page asks for the page only.
	if _, err := d.Pages(context.Background(), f.pending.ID, 0); err != nil || len(f.ops) != 3 {
		t.Errorf("second page: %v, ops %v", err, f.ops)
	}
	f.pageID = "ap-20261120-0008"
	if _, err := d.Pages(context.Background(), f.pending.ID, 0); !errors.Is(err, seatapp.ErrCardChanged) {
		t.Errorf("a page of another card: %v", err)
	}
	f.pageID = ""
	f.pending.Binding.Summary.ReviewDigest = strings.Repeat("0", 64)
	if _, err := d.Pages(context.Background(), f.pending.ID, 0); !errors.Is(err, seatapp.ErrCardChanged) {
		t.Errorf("another review: %v", err)
	}
	for _, n := range []int{-1, ownerwire.MaxPages} {
		if _, err := d.Pages(context.Background(), f.pending.ID, n); !errors.Is(err, seatapp.ErrNoCard) {
			t.Errorf("page %d: %v", n, err)
		}
	}
}

// TestSeatLinkList: the list maps row by row; a row whose id is not a card's
// is a bad answer.
func TestSeatLinkList(t *testing.T) {
	f, d, _ := readGate(t, []string{"read"})
	l, err := d.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(l.Pending) != 1 || l.Pending[0].ID != f.pending.ID || !l.Pending[0].Held || l.Pending[0].Feature != "sample-feature" ||
		l.Pending[0].Asked.IsZero() || l.NotListed != 3 {
		t.Errorf("pending %+v", l)
	}
	if len(l.Recent) != 1 || l.Recent[0].State != "denied" || l.Recent[0].Line != "denied <b>at</b> the desk" {
		t.Errorf("recent %+v", l.Recent)
	}
}

// TestSeatLinkReadCodes: the gate's codes map to the app's errors; an id
// that is not a card's, or a gate that does not list "read", is asked
// nothing.
func TestSeatLinkReadCodes(t *testing.T) {
	seat := newIdentity(t, "seat")
	for code, want := range map[string]error{
		"approval-void":     seatapp.ErrNoCard,
		"review-changed":    seatapp.ErrCardChanged,
		"too-large":         seatapp.ErrTooLarge,
		"unknown-op":        seatapp.ErrNotOffered,
		"rate-limited":      seatapp.ErrBusy,
		"busy":              seatapp.ErrBusy,
		"too-many-refusals": seatapp.ErrBusy,
	} {
		g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"read"}, refuse(code))
		d := dialPinned(t, g, seat)
		if _, err := d.List(context.Background()); !errors.Is(err, want) {
			t.Errorf("%s: list %v", code, err)
		}
		if _, err := d.Show(context.Background(), "ap-20261120-0007"); !errors.Is(err, want) {
			t.Errorf("%s: show %v", code, err)
		}
	}
	g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"read"}, refuse("journal-reopened"))
	var re *RefusedError
	if _, err := dialPinned(t, g, seat).Show(context.Background(), "ap-20261120-0007"); !errors.As(err, &re) || re.Op != ownerwire.OpShow {
		t.Errorf("another code: %v", err)
	}

	g = serveGateCaps(t, []string{seat.Fingerprint()}, []string{"pair"}, refuse("failed"))
	d := dialPinned(t, g, seat)
	if _, err := d.List(context.Background()); !errors.Is(err, seatapp.ErrNotOffered) {
		t.Errorf("no read capability: %v", err)
	}
	if _, err := d.Show(context.Background(), "../etc"); !errors.Is(err, seatapp.ErrNoCard) {
		t.Errorf("a bad id: %v", err)
	}
	if n := g.calls.Load(); n != 0 {
		t.Errorf("the gate was asked %d times", n)
	}
}

// TestSeatLinkCardsOverTheSeatApp: the whole read-only path on loopback: a
// phone logs in with a software authenticator, reads the list, a card and a
// page of its paths through the seat app, the link and the gate, and sees
// the lead's text only as escaped text, with no decide control anywhere.
func TestSeatLinkCardsOverTheSeatApp(t *testing.T) {
	seat := newIdentity(t, "seat")
	p, b := vector(t)
	reads := &fakeReads{pending: p, bytes: b}
	var desk *fakeDesk
	g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"pair", "read", "login"},
		func(ctx context.Context, peer seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
			if op == OpLoginChallenge || op == OpLogin {
				return desk.handle(ctx, peer, op, params)
			}
			return reads.handle(ctx, peer, op, params)
		})
	desk = newFakeDesk(g.id.Fingerprint())
	phone, err := webauthntest.New(seatRPID, seatOrigin)
	if err != nil {
		t.Fatal(err)
	}
	desk.pair(t, phone)
	br := newBrowser(seatApp(t, dialPinned(t, g, seat)))

	// Without a session, no card page asks the gate anything.
	before := g.calls.Load()
	if w := br.do("GET", "/cards/"+p.ID, ""); w.Code != 303 {
		t.Errorf("no session: %d", w.Code)
	}
	if g.calls.Load() != before {
		t.Error("a page without a session asked the gate")
	}
	if w := br.loginWith(t, phone); w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/cards/", "/cards/" + p.ID, "/cards/" + p.ID + "/paths/1"} {
		w := br.do("GET", path, "")
		body := w.Body.String()
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %q", path, w.Code, w.Header().Get("Cache-Control"))
		}
		for _, bad := range []string{"\x1b", "\u202e", "<script>alert", `<a href="https://evil`, "<form", "<input", "/api/decide"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s holds %q", path, bad)
			}
		}
		// The one control of a card page is the logout.
		if strings.Count(body, "<button") != 1 || !strings.Contains(body, `<button id="logout"`) {
			t.Errorf("%s: a control other than the logout", path)
		}
	}
	card := br.do("GET", "/cards/"+p.ID, "").Body.String()
	for _, want := range []string{
		"project-a", "sample-feature", "&lt;script&gt;alert(1)&lt;/script&gt;", `\x1b[2J\u{202E}evil`,
		`tools/\x1b[2Jclear`, "Lead-stated, not checked.", "STAND-IN", "the door refused &lt;i&gt;the last one&lt;/i&gt;",
	} {
		if !strings.Contains(card, want) {
			t.Errorf("the card page lacks %q", want)
		}
	}
	list := br.do("GET", "/cards/", "").Body.String()
	if !strings.Contains(list, `href="/cards/`+p.ID+`"`) || !strings.Contains(list, "denied &lt;b&gt;at&lt;/b&gt; the desk") {
		t.Errorf("the list page:\n%s", list)
	}
	// After a logout the pages are closed again.
	br.do("POST", "/api/logout", "{}")
	if w := br.do("GET", "/cards/", ""); w.Code != 303 {
		t.Errorf("after the logout: %d", w.Code)
	}
}

// TestSeatLinkPruneSaysWhereTheBranchStays: a prune card's ref line keeps
// the branch its deleted branch was merged into, and that branch's tip, as
// the desk shows them.
func TestSeatLinkPruneSaysWhereTheBranchStays(t *testing.T) {
	f, d, _ := readGate(t, []string{"read"})
	b, err := os.ReadFile("../approval/record/testdata/pending-valid-prune.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := record.DecodePending(b)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.pending, f.bytes = p, b
	f.mu.Unlock()
	c, err := d.Show(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Refs) != 1 || c.Refs[0].MergedInto == nil || fmtLead(c.Refs[0].MergedInto.Ref) != "refs/heads/develop" ||
		c.Refs[0].MergedInto.Tip != "93c8d68b93c8d68b93c8d68b93c8d68b93c8d68b" || !c.Refs[0].Deletes {
		t.Errorf("the prune's ref line: %+v", c.Refs)
	}
}

// TestSeatLinkHeldAndEarlierCard: the held mark and the earlier card of a
// show reach the phone; an earlier record that is not its digest's is a bad
// answer.
func TestSeatLinkHeldAndEarlierCard(t *testing.T) {
	f, d, _ := readGate(t, []string{"read"})
	earlier, _ := vector(t)
	earlier.ID = "ap-20261119-0002"
	eb, err := record.EncodePending(earlier)
	if err != nil {
		t.Fatal(err)
	}
	readAt := record.FormatTime(time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC))
	show, err := ownerwire.EncodeShowResult(ownerwire.ShowResult{Record: ownerwire.NewRecord(f.bytes), Held: true,
		Earlier: &ownerwire.Earlier{Record: ownerwire.NewRecord(eb), ReadAt: readAt}})
	if err != nil {
		t.Fatal(err)
	}
	f.show = show
	c, err := d.Show(context.Background(), f.pending.ID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !c.Held || (c.Differs == nil && c.DiffersRefused == "") {
		t.Errorf("held %v, differs %+v, refused %q", c.Held, c.Differs, c.DiffersRefused)
	}
	bad := bytes.Replace(show, []byte(record.Digest(eb)), []byte(strings.Repeat("0", 64)), 1)
	f.show = bad
	if _, err := d.Show(context.Background(), f.pending.ID); err == nil {
		t.Error("an earlier record that is not its digest's was taken")
	}
}
