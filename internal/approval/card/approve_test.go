package card

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/record"
)

// allLists is a reading's mark for the four lists, each read to its end.
var allLists = [NumLists]bool{true, true, true, true}

// The rule, case by case. Every case starts from a reading that may
// approve and changes one thing, so a "no" has exactly the cause the case
// names.
func TestMayApprove(t *testing.T) {
	makefile := record.SignOff{Kind: record.FlagExecConfig, Path: "Makefile"}
	script := record.SignOff{Kind: record.FlagExecConfig, Path: "scripts/run.sh"}
	link := record.SignOff{Kind: record.FlagSymlink, Path: "link"}

	oneFlag := validPending(t)

	raw := rawPending()
	raw.Binding.Summary.Flagged = []record.Flag{
		{Kind: makefile.Kind, Path: makefile.Path, Pattern: "Makefile"},
		{Kind: script.Kind, Path: script.Path, Pattern: "scripts/"},
		{Kind: link.Kind, Path: link.Path, Pattern: ""},
	}
	threeFlags := seal(t, raw)

	raw = rawPending()
	raw.Binding.Summary.Flagged = []record.Flag{}
	noFlag := seal(t, raw)

	raw = rawPending()
	raw.Binding.Summary.PathsTruncated = true
	raw.Binding.Summary.Files = 5000
	truncated := seal(t, raw)
	digest := truncated.Binding.Summary.ReviewDigest
	otherDigest := record.Digest([]byte("another review"))

	later := seal(t, laterPending())
	earlier := &Earlier{Record: oneFlag, ReadAt: asked}

	opened := asked.Add(time.Hour)
	now := opened.Add(time.Minute)
	// good is a reading that may approve a card with the one flag.
	good := func() Reading {
		return Reading{Opened: opened, SignedOff: []record.SignOff{makefile}, Paths: true, Lists: allLists}
	}
	lastPage := func(r *Reading, digest string) {
		r.ReadPage(Page{Kind: PagePaths, N: 12, Pages: 13, ReviewDigest: digest})
	}
	// allPages reads pages first to last-1 (skipping those in skip) of 13.
	pages := func(r *Reading, digest string, skip ...int) {
	next:
		for n := 0; n < 13; n++ {
			for _, s := range skip {
				if n == s {
					continue next
				}
			}
			r.ReadPage(Page{Kind: PagePaths, N: n, Pages: 13, ReviewDigest: digest})
		}
	}

	cases := []struct {
		name   string
		p      record.Pending
		in     Inputs
		change func(r *Reading)
		now    time.Time // zero: the usual now
		want   string    // the reason; empty: may approve
	}{
		{name: "everything done", p: oneFlag},
		{name: "everything done, no flag", p: noFlag, change: func(r *Reading) { r.SignedOff = nil }},
		{name: "everything done, three flags in another order", p: threeFlags,
			change: func(r *Reading) { r.SignedOff = []record.SignOff{link, makefile, script} }},
		{name: "a sign-off made twice", p: oneFlag,
			change: func(r *Reading) { r.SignedOff = []record.SignOff{makefile, makefile} }},

		// Sign-offs.
		{name: "the flag not signed off", p: oneFlag,
			change: func(r *Reading) { r.SignedOff = nil }, want: "1 flagged path is not signed off"},
		{name: "one of three flags signed off", p: threeFlags,
			change: func(r *Reading) { r.SignedOff = []record.SignOff{script} }, want: "2 flagged paths are not signed off"},
		{name: "two of three flags signed off", p: threeFlags,
			change: func(r *Reading) { r.SignedOff = []record.SignOff{script, link} }, want: "1 flagged path is not signed off"},
		{name: "a surplus sign-off", p: oneFlag,
			change: func(r *Reading) { r.SignedOff = []record.SignOff{makefile, script} }, want: ReasonSurplus},
		{name: "a surplus sign-off on a card without flags", p: noFlag,
			change: func(r *Reading) { r.SignedOff = []record.SignOff{makefile} }, want: ReasonSurplus},
		{name: "the flagged path signed off under another kind", p: oneFlag,
			change: func(r *Reading) { r.SignedOff = []record.SignOff{{Kind: record.FlagSymlink, Path: "Makefile"}} },
			want:   "1 flagged path is not signed off"},
		{name: "the flag signed off and the same path under another kind as well", p: oneFlag,
			change: func(r *Reading) {
				r.SignedOff = []record.SignOff{makefile, {Kind: record.FlagSymlink, Path: "Makefile"}}
			}, want: ReasonSurplus},
		{name: "a sign-off of a path that differs by case", p: oneFlag,
			change: func(r *Reading) { r.SignedOff = []record.SignOff{{Kind: makefile.Kind, Path: "makefile"}} },
			want:   "1 flagged path is not signed off"},

		// Lists read to their end.
		{name: "the path list not read", p: oneFlag,
			change: func(r *Reading) { r.Paths = false }, want: ReasonPaths},
		{name: "the binary list not read", p: oneFlag,
			change: func(r *Reading) { r.Lists[ListBinary] = false }, want: "the list of binary files was not read to its end"},
		{name: "the symlink list not read", p: oneFlag,
			change: func(r *Reading) { r.Lists[ListSymlinks] = false }, want: "the list of symbolic links was not read to its end"},
		{name: "the mode list not read", p: oneFlag,
			change: func(r *Reading) { r.Lists[ListModeChanges] = false }, want: "the list of mode changes was not read to its end"},
		{name: "the submodule list not read", p: oneFlag,
			change: func(r *Reading) { r.Lists[ListSubmodules] = false }, want: "the list of submodules was not read to its end"},
		{name: "no list read", p: oneFlag,
			change: func(r *Reading) { r.Paths = false; r.Lists = [NumLists]bool{} }, want: ReasonPaths},
		{name: "a last page read, on a card whose list is whole and unread", p: oneFlag,
			change: func(r *Reading) { r.Paths = false; lastPage(r, oneFlag.Binding.Summary.ReviewDigest) }, want: ReasonPaths},

		// A truncated path list.
		{name: "truncated, every page read", p: truncated,
			change: func(r *Reading) { pages(r, digest) }},
		{name: "truncated, every page read and the card's rows not", p: truncated,
			change: func(r *Reading) { r.Paths = false; pages(r, digest) }},
		{name: "truncated, every page read, the last first", p: truncated,
			change: func(r *Reading) {
				lastPage(r, digest)
				pages(r, digest, 12)
			}},
		{name: "truncated, a page read twice", p: truncated,
			change: func(r *Reading) { pages(r, digest); pages(r, digest) }},
		{name: "truncated, no page read", p: truncated, want: ReasonPathPages},
		{name: "truncated, only the last page read", p: truncated,
			change: func(r *Reading) { lastPage(r, digest) }, want: ReasonPathPages},
		{name: "truncated, every page but the last read", p: truncated,
			change: func(r *Reading) { pages(r, digest, 12) }, want: ReasonPathPages},
		{name: "truncated, every page but the first read", p: truncated,
			change: func(r *Reading) { pages(r, digest, 0) }, want: ReasonPathPages},
		{name: "truncated, every page but one in the middle read", p: truncated,
			change: func(r *Reading) { pages(r, digest, 6) }, want: ReasonPathPages},
		{name: "truncated, pages of another review read after the others", p: truncated,
			change: func(r *Reading) {
				pages(r, digest, 12)
				r.ReadPage(Page{Kind: PagePaths, N: 0, Pages: 13, ReviewDigest: otherDigest})
				lastPage(r, digest)
			},
			want: ReasonPathPages},
		{name: "truncated, the same review read again with another page count", p: truncated,
			change: func(r *Reading) {
				pages(r, digest, 12)
				r.ReadPage(Page{Kind: PagePaths, N: 0, Pages: 14, ReviewDigest: digest})
				lastPage(r, digest)
			},
			want: ReasonPathPages},
		{name: "truncated, every page of another review read", p: truncated,
			change: func(r *Reading) { pages(r, otherDigest) }, want: ReasonPathPages},
		{name: "truncated, the last page of the patch read", p: truncated,
			change: func(r *Reading) { r.ReadPage(Page{Kind: PagePatch, N: 12, Pages: 13, ReviewDigest: digest}) },
			want:   ReasonPathPages},
		{name: "truncated, the last page of another review read", p: truncated,
			change: func(r *Reading) { lastPage(r, otherDigest) }, want: ReasonPathPages},
		{name: "truncated, every page without a digest", p: truncated,
			change: func(r *Reading) { pages(r, "") }, want: ReasonPathPages},
		{name: "truncated, a page past the end", p: truncated,
			change: func(r *Reading) {
				pages(r, digest, 12)
				r.ReadPage(Page{Kind: PagePaths, N: 13, Pages: 13, ReviewDigest: digest})
			},
			want: ReasonPathPages},
		{name: "truncated, a page of no pages", p: truncated,
			change: func(r *Reading) { r.ReadPage(Page{Kind: PagePaths, N: -1, Pages: 0, ReviewDigest: digest}) },
			want:   ReasonPathPages},
		{name: "truncated, every page read and a list not", p: truncated,
			change: func(r *Reading) { pages(r, digest); r.Lists[ListSubmodules] = false },
			want:   "the list of submodules was not read to its end"},
		{name: "truncated, every page read and the flag not signed off", p: truncated,
			change: func(r *Reading) { pages(r, digest); r.SignedOff = nil }, want: "1 flagged path is not signed off"},

		// The "differs from" block.
		{name: "differs, passed", p: later, in: Inputs{Earlier: earlier},
			change: func(r *Reading) { r.SignedOff = nil; r.Differs = true }},
		{name: "differs, not passed", p: later, in: Inputs{Earlier: earlier},
			change: func(r *Reading) { r.SignedOff = nil }, want: ReasonDiffers},
		{name: "no block to pass", p: later,
			change: func(r *Reading) { r.SignedOff = nil }},

		// 400 ms since the card opened.
		{name: "opened 400 ms ago", p: oneFlag, now: opened.Add(400 * time.Millisecond)},
		{name: "opened 399 ms ago", p: oneFlag, now: opened.Add(399 * time.Millisecond), want: ReasonJustOpened},
		{name: "opened now", p: oneFlag, now: opened, want: ReasonJustOpened},
		{name: "opened after now", p: oneFlag, now: opened.Add(-time.Second), want: ReasonNotOpened},
		{name: "never opened", p: oneFlag, change: func(r *Reading) { r.Opened = time.Time{} }, want: ReasonNotOpened},

		// Ended, and past the expiry.
		{name: "held, and pending again", p: oneFlag, in: Inputs{Held: true}},
		{name: "state pending", p: oneFlag, in: Inputs{State: record.StatePending}},
		{name: "withdrawn", p: oneFlag, in: Inputs{State: record.StateWithdrawn}, want: "this request ended: withdrawn"},
		{name: "expired by its state", p: oneFlag, in: Inputs{State: record.StateExpired}, want: "this request ended: expired"},
		{name: "void", p: oneFlag, in: Inputs{State: record.StateVoid}, want: "this request ended: void"},
		{name: "denied", p: oneFlag, in: Inputs{State: record.StateDenied}, want: "this request ended: denied"},
		{name: "approved", p: oneFlag, in: Inputs{State: record.StateApproved}, want: "this request ended: approved"},
		{name: "approved by a rule", p: oneFlag, in: Inputs{State: record.StateApprovedByRule}, want: "this request ended: approved-by-rule"},
		{name: "approved by a grant", p: oneFlag, in: Inputs{State: record.StateApprovedByGrant}, want: "this request ended: approved-by-grant"},
		{name: "consumed", p: oneFlag, in: Inputs{State: record.StateConsumed}, want: "this request ended: consumed"},
		{name: "applied", p: oneFlag, in: Inputs{State: record.StateApplied}, want: "this request ended: applied"},
		{name: "not applied", p: oneFlag, in: Inputs{State: record.StateNotApplied}, want: "this request ended: not-applied"},
		{name: "unknown", p: oneFlag, in: Inputs{State: record.StateUnknown}, want: "this request ended: unknown"},
		{name: "ended and held", p: oneFlag, in: Inputs{State: record.StateExpired, Held: true}, want: "this request ended: expired"},
		{name: "one second before the expiry", p: oneFlag,
			change: func(r *Reading) { r.Opened = expires.Add(-time.Minute) }, now: expires.Add(-time.Second)},
		{name: "at the expiry", p: oneFlag,
			change: func(r *Reading) { r.Opened = expires.Add(-time.Minute) }, now: expires, want: ReasonExpired},
		{name: "past the expiry", p: oneFlag,
			change: func(r *Reading) { r.Opened = expires.Add(-time.Minute) }, now: expires.Add(time.Hour), want: ReasonExpired},
		{name: "past the expiry, in another time zone", p: oneFlag,
			change: func(r *Reading) { r.Opened = expires.Add(-time.Minute) },
			now:    expires.Add(time.Second).In(time.FixedZone("west", -9*3600)), want: ReasonExpired},

		// Several at once: the first of the rule's order is the reason.
		{name: "ended, and nothing read", p: oneFlag, in: Inputs{State: record.StateWithdrawn},
			change: func(r *Reading) { *r = Reading{} }, want: "this request ended: withdrawn"},
		{name: "past the expiry, and nothing read", p: oneFlag,
			change: func(r *Reading) { *r = Reading{} }, now: expires, want: ReasonExpired},
		{name: "differs not passed, and nothing read", p: later, in: Inputs{Earlier: earlier},
			change: func(r *Reading) { *r = Reading{} }, want: ReasonDiffers},
		{name: "nothing read", p: oneFlag, change: func(r *Reading) { *r = Reading{} }, want: ReasonPaths},
		{name: "lists read, not signed and just opened", p: oneFlag,
			change: func(r *Reading) { r.SignedOff = nil }, now: opened, want: "1 flagged path is not signed off"},
	}
	for _, tc := range cases {
		c := mustCard(t, tc.p, tc.in)
		r := good()
		if tc.change != nil {
			tc.change(&r)
		}
		at := tc.now
		if at.IsZero() {
			at = now
		}
		ok, reason := c.MayApprove(r, at)
		if ok != (tc.want == "") || reason != tc.want {
			t.Errorf("%s: got %v, %q; want %q", tc.name, ok, reason, tc.want)
		}
		if strings.Contains(reason, "\n") || !hostText(reason) {
			t.Errorf("%s: the reason is not one line of host text: %q", tc.name, reason)
		}

		// Approve gives a decision exactly when the rule says yes.
		d, ok2, reason2 := c.Approve(r, at)
		if ok2 != ok || reason2 != reason {
			t.Errorf("%s: Approve says %v, %q and MayApprove %v, %q", tc.name, ok2, reason2, ok, reason)
		}
		if !ok {
			if !reflect.DeepEqual(d, Decision{}) {
				t.Errorf("%s: a decision came with the refusal: %+v", tc.name, d)
			}
			continue
		}
		if d.ID != tc.p.ID || d.BindingSHA256 != tc.p.BindingSHA256 || !d.Approve || d.Reason != "" {
			t.Errorf("%s: the decision is %+v", tc.name, d)
		}
		if !record.SignOffsEqual(tc.p.Binding.Summary.Flagged, d.SignedOff) {
			t.Errorf("%s: the decision's sign-offs %v are not the flagged list", tc.name, d.SignedOff)
		}
	}
}

// The zero card and a prune.
func TestMayApproveWithoutASummary(t *testing.T) {
	now := asked.Add(time.Hour)
	full := Reading{Opened: asked, Paths: true, Lists: allLists, Differs: true}
	if ok, reason := (Card{}).MayApprove(full, now); ok || reason != ReasonNoCard {
		t.Errorf("no card: got %v, %q", ok, reason)
	}
	if _, ok, reason := (Card{}).Approve(full, now); ok || reason != ReasonNoCard {
		t.Errorf("no card, Approve: got %v, %q", ok, reason)
	}
	// A prune has no path list and no lists to read, only its ref lines.
	prune := mustCard(t, prunePending(t), Inputs{})
	if ok, reason := prune.MayApprove(Reading{Opened: asked, Refs: true}, now); !ok {
		t.Errorf("a prune: refused, %q", reason)
	}
	if ok, reason := prune.MayApprove(Reading{Opened: asked}, now); ok || reason != ReasonRefs {
		t.Errorf("a prune whose refs were not read: got %v, %q", ok, reason)
	}
	if ok, reason := prune.MayApprove(Reading{Opened: asked, Paths: true, Lists: allLists, Differs: true}, now); ok || reason != ReasonRefs {
		t.Errorf("a prune with everything else read: got %v, %q", ok, reason)
	}
	if _, ok, reason := prune.Approve(Reading{Opened: asked}, now); ok || reason != ReasonRefs {
		t.Errorf("Approve of a prune whose refs were not read: got %v, %q", ok, reason)
	}
	if ok, reason := prune.MayApprove(Reading{Opened: asked, Refs: true, SignedOff: []record.SignOff{{Kind: "symlink", Path: "x"}}}, now); ok || reason != ReasonSurplus {
		t.Errorf("a prune with a sign-off: got %v, %q", ok, reason)
	}
}

// Every reason is one line of the host's own words.
func TestReasonsAreHostLines(t *testing.T) {
	reasons := []string{
		ReasonNoCard, ReasonEnded, ReasonExpired, ReasonDiffers, ReasonPaths, ReasonPathPages, ReasonRefs, ReasonUnsigned,
		ReasonSurplus, ReasonNotOpened, ReasonJustOpened, ReasonTooLong, ReasonNotText,
		NoteLabel, DiffersLabel, AfterLabel, HeldLabel,
	}
	for k := ListKind(0); int(k) < NumLists; k++ {
		reasons = append(reasons, reasonList(k), k.Label())
	}
	seen := map[string]bool{}
	for _, r := range reasons {
		if r == "" || strings.Contains(r, "\n") || !hostText(r) || len(r) > 100 || seen[r] {
			t.Errorf("the reason %q is empty, not one short line, or given twice", r)
		}
		seen[r] = true
	}
	if OpenDelay != 400*time.Millisecond {
		t.Errorf("the delay is %v, and the reason says 400 ms", OpenDelay)
	}
	if !strings.Contains(ReasonTooLong, "500") || record.MaxReasonBytes != 500 {
		t.Error("the reason for a long deny does not name the cap")
	}
}

func TestReadingToggleAndSigned(t *testing.T) {
	a := record.SignOff{Kind: record.FlagExecConfig, Path: "Makefile"}
	b := record.SignOff{Kind: record.FlagSymlink, Path: "Makefile"}
	var r Reading
	if r.Signed(a) {
		t.Error("an empty reading has a sign-off")
	}
	r.Toggle(a)
	r.Toggle(b)
	if !r.Signed(a) || !r.Signed(b) || len(r.SignedOff) != 2 {
		t.Errorf("after two sign-offs: %v", r.SignedOff)
	}
	r.Toggle(a)
	if r.Signed(a) || !r.Signed(b) || len(r.SignedOff) != 1 {
		t.Errorf("after taking one back: %v", r.SignedOff)
	}
	// Taking a sign-off back removes every copy of it.
	r.SignedOff = []record.SignOff{a, b, a}
	r.Toggle(a)
	if r.Signed(a) || len(r.SignedOff) != 1 {
		t.Errorf("after taking a doubled one back: %v", r.SignedOff)
	}
}

// The sign-offs of a decision are the card's flagged paths in the card's
// order, whatever order the reader signed in.
func TestSignedOffIsInTheCardsOrder(t *testing.T) {
	raw := rawPending()
	raw.Binding.Summary.Flagged = []record.Flag{
		{Kind: record.FlagExecConfig, Path: "b"},
		{Kind: record.FlagSymlink, Path: "a"},
		{Kind: record.FlagExecConfig, Path: "a\xff"},
	}
	p := seal(t, raw)
	c := mustCard(t, p, Inputs{})
	r := Reading{SignedOff: []record.SignOff{
		{Kind: record.FlagExecConfig, Path: "a\xff"},
		{Kind: record.FlagExecConfig, Path: "not flagged"},
		{Kind: record.FlagSymlink, Path: "a"},
		{Kind: record.FlagExecConfig, Path: "b"},
		{Kind: record.FlagSymlink, Path: "a"},
	}}
	if got := c.SignedOff(r); !record.SignOffsEqual(p.Binding.Summary.Flagged, got) {
		t.Errorf("got %v", got)
	}
	r.SignedOff = r.SignedOff[:2]
	want := []record.SignOff{{Kind: record.FlagExecConfig, Path: "a\xff"}}
	if got := c.SignedOff(r); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := c.SignedOff(Reading{}); got == nil || len(got) != 0 {
		t.Errorf("no sign-off: got %v, want an empty list that is not nil", got)
	}
}

func TestDeny(t *testing.T) {
	p := validPending(t)
	now := asked.Add(time.Hour)
	c := mustCard(t, p, Inputs{})

	// A deny needs no reading at all.
	for _, why := range []string{"", "not today", "naïve: 名前", strings.Repeat("r", 500)} {
		d, ok, reason := c.Deny(why, now)
		want := Decision{ID: p.ID, BindingSHA256: p.BindingSHA256, Reason: why}
		if !ok || reason != "" || !reflect.DeepEqual(d, want) {
			t.Errorf("deny %.10q: got %+v, %v, %q", why, d, ok, reason)
		}
	}
	held := mustCard(t, p, Inputs{Held: true})
	if _, ok, reason := held.Deny("", now); !ok {
		t.Errorf("a held card cannot be denied: %q", reason)
	}

	refused := []struct {
		name string
		c    Card
		why  string
		now  time.Time
		want string
	}{
		{"no card", Card{}, "", now, ReasonNoCard},
		{"ended", mustCard(t, p, Inputs{State: record.StateWithdrawn}), "", now, "this request ended: withdrawn"},
		{"at the expiry", c, "", expires, ReasonExpired},
		{"a reason of 501 bytes", c, strings.Repeat("r", 501), now, ReasonTooLong},
		{"a line feed in the reason", c, "one\ntwo", now, ReasonNotText},
		{"an escape in the reason", c, "\x1b[2J", now, ReasonNotText},
		{"a C1 control in the reason", c, "a\u009bb", now, ReasonNotText},
		{"a delete in the reason", c, "a\x7fb", now, ReasonNotText},
		{"bytes that are not UTF-8 in the reason", c, "a\xffb", now, ReasonNotText},
	}
	for _, tc := range refused {
		d, ok, reason := tc.c.Deny(tc.why, tc.now)
		if ok || reason != tc.want || !reflect.DeepEqual(d, Decision{}) {
			t.Errorf("%s: got %+v, %v, %q; want %q", tc.name, d, ok, reason, tc.want)
		}
	}
}

// A card whose path list is cut may be approved only after every page of
// the whole list was read; the card's own rows are not enough.
func TestATruncatedPathListNeedsEveryPage(t *testing.T) {
	p, _ := truncatedPending(t)
	c := mustCard(t, p, Inputs{})
	now := asked.Add(time5min)
	r := Reading{Opened: asked, Paths: true, Lists: allLists}
	if ok, reason := c.MayApprove(r, now); ok || reason != ReasonPathPages {
		t.Errorf("with the card's rows read: got %v, %q", ok, reason)
	}
	pages := 13
	for n := 0; n < pages; n++ {
		if ok, _ := c.MayApprove(r, now); ok {
			t.Fatalf("approve is offered before page %d of %d was read", n, pages)
		}
		r.ReadPage(Page{Kind: PagePaths, N: n, Pages: pages, ReviewDigest: c.Diff.ReviewDigest})
	}
	if ok, reason := c.MayApprove(r, now); !ok {
		t.Errorf("with every page read: refused, %q", reason)
	}
}

// A copy of a reading does not share the pages with the reading it came
// from.
func TestReadPageDoesNotShareWithACopy(t *testing.T) {
	var r Reading
	r.ReadPage(Page{Kind: PagePaths, N: 0, Pages: 2, ReviewDigest: "d"})
	cp := r
	cp.ReadPage(Page{Kind: PagePaths, N: 1, Pages: 2, ReviewDigest: "d"})
	if !cp.pathPagesRead("d") || r.pathPagesRead("d") {
		t.Errorf("copy read %v, original read %v", cp.pathPagesRead("d"), r.pathPagesRead("d"))
	}
}

// The keys are the last section: which ones a card offers.
func TestKeys(t *testing.T) {
	names := func(c Card) string {
		var out []string
		for _, k := range c.Keys() {
			out = append(out, k.Key+"="+k.Label)
		}
		return strings.Join(out, " ")
	}
	p := validPending(t)
	if got, want := names(mustCard(t, p, Inputs{})), "space=sign off a=approve x=deny d=diff l=open the lead esc=back"; got != want {
		t.Errorf("a live card with a flag: got %q, want %q", got, want)
	}
	if got, want := names(mustCard(t, seal(t, laterPending()), Inputs{})), "a=approve x=deny d=diff l=open the lead esc=back"; got != want {
		t.Errorf("a live card without a flag: got %q, want %q", got, want)
	}
	if got, want := names(mustCard(t, p, Inputs{Held: true})), "space=sign off a=approve x=deny d=diff l=open the lead esc=back"; got != want {
		t.Errorf("a held card: got %q, want %q", got, want)
	}
	for _, state := range []record.State{record.StateApplied, record.StateDenied, record.StateWithdrawn, record.StateExpired} {
		if got, want := names(mustCard(t, p, Inputs{State: state})), "d=diff l=open the lead esc=back"; got != want {
			t.Errorf("a card that ended (%s): got %q, want %q", state, got, want)
		}
	}
	if got, want := names(mustCard(t, prunePending(t), Inputs{})), "a=approve x=deny l=open the lead esc=back"; got != want {
		t.Errorf("a prune, which has no diff and no flag: got %q, want %q", got, want)
	}
	if got := (Card{}).Keys(); got != nil {
		t.Errorf("no card has keys: %v", got)
	}
	actions := map[Action]bool{}
	for _, k := range mustCard(t, p, Inputs{}).Keys() {
		if k.Action < ActionSignOff || k.Action > ActionBack || actions[k.Action] {
			t.Errorf("the key %q has the action %d", k.Key, k.Action)
		}
		actions[k.Action] = true
	}
}
