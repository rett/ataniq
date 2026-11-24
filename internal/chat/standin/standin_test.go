package standin

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
)

// clock is the fixed clock of the tests.
func clock() time.Time { return time.Date(2026, 11, 23, 10, 30, 42, 0, time.FixedZone("x", -9*3600)) }

func read(lead string, after int64, limit int) ownerwire.SeatChatParams {
	return ownerwire.SeatChatParams{Verb: ownerwire.ChatVerbRead, Lead: lead, After: after, Limit: limit}
}

// call asks the port and holds its result to the wire: it encodes, and decodes
// to itself.
func call(t *testing.T, p *Port, params ownerwire.SeatChatParams) ownerwire.SeatChatResult {
	t.Helper()
	if _, err := ownerwire.EncodeSeatChatParams(params); err != nil {
		t.Fatalf("the test's params do not encode: %v", err)
	}
	r, err := p.Chat(context.Background(), params)
	if err != nil {
		t.Fatalf("%+v: %v", params, err)
	}
	out, err := ownerwire.EncodeSeatChatResult(r)
	if err != nil {
		t.Fatalf("%+v: the result does not encode: %v", params, err)
	}
	back, err := ownerwire.DecodeSeatChatResult(out)
	if err != nil || !reflect.DeepEqual(back, r) {
		t.Fatalf("%+v: the result does not come back: %v", params, err)
	}
	if !r.StandIn {
		t.Fatalf("%+v: the result is not marked stand_in", params)
	}
	return r
}

// gateCode is the word a refusal carries, as the gate's handler finds it.
func gateCode(err error) string {
	var g interface{ GateCode() string }
	if errors.As(err, &g) {
		return g.GateCode()
	}
	return ""
}

// The list holds the three sample leads, in order, and the off one carries
// its identifier and its switch and nothing else.
func TestTheListHoldsTheThreeSampleLeads(t *testing.T) {
	r := call(t, New(clock), ownerwire.SeatChatParams{Verb: ownerwire.ChatVerbLeads})
	three, ten, one, five := int64(3), int64(10), int64(1), int64(5)
	want := []ownerwire.ChatLeadRow{
		{Lead: LeadAlpha, Chat: ownerwire.ChatRead, State: ownerwire.ChatLeadRunning, Epoch: &three, Head: &ten},
		{Lead: LeadBravo, Chat: ownerwire.ChatRead, State: ownerwire.ChatLeadStopped, Epoch: &one, Head: &five},
		{Lead: LeadCharlie, Chat: ownerwire.ChatOff},
	}
	if r.State != ownerwire.ChatStateLeads || !reflect.DeepEqual(r.Leads, want) {
		t.Fatalf("the list: %+v", r)
	}
}

// A read of all of a conversation gives every retained entry, in order, with
// the lead's epoch and head and no more.
func TestAReadGivesTheWholeRetainedConversation(t *testing.T) {
	p := New(clock)
	for _, c := range []struct {
		lead         string
		first, head  int64
		epoch        int64
		entries      int
		firstWho     string
		lastWho      string
		someCut      bool
		someLines    bool
		someMarkup   bool
		retainedFrom int64
	}{
		{lead: LeadAlpha, first: 4, head: 10, epoch: 3, entries: 7, firstWho: ownerwire.ChatWhoSystem, lastWho: ownerwire.ChatWhoLead, someCut: true, someLines: true, someMarkup: true},
		{lead: LeadBravo, first: 1, head: 5, epoch: 1, entries: 5, firstWho: ownerwire.ChatWhoSystem, lastWho: ownerwire.ChatWhoSystem, someLines: true},
	} {
		r := call(t, p, read(c.lead, 0, ownerwire.MaxChatReadLimit))
		if r.State != ownerwire.ChatStateRead || r.Lead != c.lead || r.Epoch != c.epoch || r.Head != c.head || r.More || r.Reset || len(r.Entries) != c.entries {
			t.Fatalf("%s: %+v", c.lead, r)
		}
		if r.Entries[0].Seq != c.first || r.Entries[0].Who != c.firstWho || r.Entries[len(r.Entries)-1].Who != c.lastWho {
			t.Errorf("%s: the ends are %+v and %+v", c.lead, r.Entries[0], r.Entries[len(r.Entries)-1])
		}
		cut, lines, markup := false, false, false
		for i, e := range r.Entries {
			if e.Seq != c.first+int64(i) {
				t.Errorf("%s: entry %d has the seq %d", c.lead, i, e.Seq)
			}
			cut = cut || e.Cut
			lines = lines || strings.Contains(string(e.Text), "\n")
			markup = markup || strings.Contains(string(e.Text), "<b>bold</b> & \"quotes\"")
			if !strings.HasPrefix(string(e.Text), "Sample:") {
				t.Errorf("%s: the %s entry %d does not say it is a sample: %q", c.lead, e.Who, e.Seq, e.Text)
			}
		}
		if cut != c.someCut || lines != c.someLines || markup != c.someMarkup {
			t.Errorf("%s: cut %v, lines %v, markup %v", c.lead, cut, lines, markup)
		}
	}
}

// The long reply is cut: shortened to the wire's cap, marked so, and ending
// with an ellipsis.
func TestTheLongReplyIsCut(t *testing.T) {
	r := call(t, New(clock), read(LeadAlpha, 0, ownerwire.MaxChatReadLimit))
	var long ownerwire.ChatEntry
	for _, e := range r.Entries {
		if e.Cut {
			long = e
		}
	}
	if len(long.Text) < ownerwire.MaxChatText-120 || len(long.Text) > ownerwire.MaxChatText || !strings.HasSuffix(string(long.Text), string(rune(0x2026))) {
		t.Fatalf("the cut entry is %d bytes: %q", len(long.Text), long.Text)
	}
}

// No sample text carries a byte the wire refuses: a bidirectional control
// put in the markup sample is refused by the wire, so the sample carries
// none, and the seat is left to escape the markup.
func TestTheSamplesCarryNoBidiControl(t *testing.T) {
	p := New(clock)
	for _, lead := range []string{LeadAlpha, LeadBravo} {
		r := call(t, p, read(lead, 0, ownerwire.MaxChatReadLimit))
		for _, e := range r.Entries {
			if !ownerwire.ValidChatText(string(e.Text)) {
				t.Errorf("%s %d is not plain text", lead, e.Seq)
			}
			for _, ch := range string(e.Text) {
				if ch == 0x202e || ch == 0x200e || ch == 0x200f || (ch >= 0x2066 && ch <= 0x2069) || ch == 0x061c {
					t.Errorf("%s %d carries U+%04X", lead, e.Seq, ch)
				}
			}
			if strings.Contains(string(e.Text), "<b>") {
				bad := r
				bad.Entries = append([]ownerwire.ChatEntry(nil), r.Entries...)
				for i := range bad.Entries {
					if bad.Entries[i].Seq == e.Seq {
						setText(&bad.Entries[i].Text, string(e.Text)+string(rune(0x202e)))
					}
				}
				if _, err := ownerwire.EncodeSeatChatResult(bad); err == nil {
					t.Error("the wire takes the markup sample with a right-to-left override")
				}
			}
		}
	}
}

// Read in pages of one or two entries, the cursor (the last seq of a page)
// moves on until more is false, and the pages hold the whole conversation.
func TestPagesFollowTheCursor(t *testing.T) {
	p := New(clock)
	for _, lead := range []string{LeadAlpha, LeadBravo} {
		whole := call(t, p, read(lead, 0, ownerwire.MaxChatReadLimit)).Entries
		for _, limit := range []int{1, 2, 3} {
			var got []ownerwire.ChatEntry
			after := int64(0)
			for pages := 0; ; pages++ {
				if pages > 20 {
					t.Fatalf("%s by %d: the cursor does not move on", lead, limit)
				}
				r := call(t, p, read(lead, after, limit))
				if len(r.Entries) > limit || r.Reset {
					t.Fatalf("%s by %d after %d: %+v", lead, limit, after, r)
				}
				got = append(got, r.Entries...)
				if !r.More {
					break
				}
				after = r.Entries[len(r.Entries)-1].Seq
			}
			if !reflect.DeepEqual(got, whole) {
				t.Errorf("%s by %d: the pages are not the conversation", lead, limit)
			}
		}
	}
}

// A cursor before the retained start reads from the start with reset; the
// one just before the start, 0, and every cursor inside do not; the head
// gives an empty page; a cursor past the head is one of a conversation the
// host does not hold, and reads from the start with reset.
func TestACursorOutsideWhatIsRetainedResets(t *testing.T) {
	p := New(clock)
	for _, c := range []struct {
		after     int64
		reset     bool
		firstSeq  int64
		entries   int
		more      bool
		emptyPage bool
	}{
		{after: 0, firstSeq: 4, entries: 7},
		{after: 1, reset: true, firstSeq: 4, entries: 7},
		{after: 2, reset: true, firstSeq: 4, entries: 7},
		{after: 3, firstSeq: 4, entries: 7},
		{after: 4, firstSeq: 5, entries: 6},
		{after: 9, firstSeq: 10, entries: 1},
		{after: 10, emptyPage: true},
		{after: 11, reset: true, firstSeq: 4, entries: 7},
		{after: ownerwire.MaxChatSeq, reset: true, firstSeq: 4, entries: 7},
	} {
		r := call(t, p, read(LeadAlpha, c.after, ownerwire.MaxChatReadLimit))
		if r.Reset != c.reset || r.More || r.Head != 10 {
			t.Errorf("after %d: %+v", c.after, r)
			continue
		}
		if c.emptyPage {
			if len(r.Entries) != 0 {
				t.Errorf("after the head: %d entries", len(r.Entries))
			}
			continue
		}
		if len(r.Entries) != c.entries || r.Entries[0].Seq != c.firstSeq {
			t.Errorf("after %d: %d entries from %d", c.after, len(r.Entries), r.Entries[0].Seq)
		}
	}
}

// A read of the lead whose chat is off is chat-off, and of a lead the
// stand-in does not hold chat-unknown-lead; neither gives a result.
func TestOffAndUnknownLeadsAreRefused(t *testing.T) {
	p := New(clock)
	for lead, code := range map[string]string{
		LeadCharlie: ownerwire.CodeChatOff, "daemon:delta": ownerwire.CodeChatUnknownLead, "daemon:alpha-2": ownerwire.CodeChatUnknownLead,
	} {
		r, err := p.Chat(context.Background(), read(lead, 0, 4))
		if gateCode(err) != code || !reflect.DeepEqual(r, ownerwire.SeatChatResult{}) {
			t.Errorf("%s: %+v %v", lead, r, err)
		}
		if err != nil && strings.Contains(err.Error(), lead) {
			t.Errorf("%s: the error names the lead: %v", lead, err)
		}
	}
	if _, err := p.Chat(context.Background(), ownerwire.SeatChatParams{Verb: "send"}); gateCode(err) != "bad-params" {
		t.Errorf("a send: %v", err)
	}
}

// A call whose context is done answers its error, and nothing else.
func TestADoneContextIsAnswered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(clock).Chat(ctx, ownerwire.SeatChatParams{Verb: ownerwire.ChatVerbLeads}); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

// The samples are drawn from the clock once: the same clock gives the same
// samples, the times are minutes before the clock's minute, in UTC, and
// later calls do not move them.
func TestTheTimesComeFromTheClock(t *testing.T) {
	a, b := New(clock), New(clock)
	ra := call(t, a, read(LeadAlpha, 0, 16))
	if rb := call(t, b, read(LeadAlpha, 0, 16)); !reflect.DeepEqual(ra, rb) {
		t.Fatal("the same clock gives other samples")
	}
	base := clock().UTC().Truncate(time.Minute)
	for _, e := range ra.Entries {
		at, err := time.Parse(time.RFC3339, e.At)
		if err != nil || !strings.HasSuffix(e.At, "Z") || !at.Before(base) || at.Sub(base)%time.Minute != 0 {
			t.Errorf("entry %d at %s", e.Seq, e.At)
		}
	}
	if ra.Entries[len(ra.Entries)-1].At != "2026-11-23T19:25:00Z" {
		t.Errorf("the last entry is at %s", ra.Entries[len(ra.Entries)-1].At)
	}
	later := New(func() time.Time { return clock().Add(time.Hour) })
	if rl := call(t, later, read(LeadAlpha, 0, 16)); rl.Entries[0].At == ra.Entries[0].At {
		t.Error("another clock gives the same times")
	}
	if again := call(t, a, read(LeadAlpha, 0, 16)); !reflect.DeepEqual(again, ra) {
		t.Error("a second call moved the samples")
	}
	if New(nil) == nil {
		t.Error("New(nil) gives no port")
	}
}

// A page is paged by its encoded bytes: a conversation of sixteen entries of
// the text that encodes longest is read in several pages, each within the
// wire's cap and each holding at least one entry.
func TestPagesAreCutByEncodedBytes(t *testing.T) {
	es := make([]ownerwire.ChatEntry, ownerwire.MaxChatReadLimit)
	for i := range es {
		es[i] = ownerwire.ChatEntry{Seq: int64(i + 1), Who: ownerwire.ChatWhoLead, At: "2026-11-23T10:00:00Z"}
		setText(&es[i].Text, strings.Repeat(`"`, ownerwire.MaxChatText))
	}
	p := newWith([]sampleLead{{id: "daemon:big", chat: ownerwire.ChatSend, state: ownerwire.ChatLeadRunning, epoch: 1, first: 1, entries: es}})
	var got []ownerwire.ChatEntry
	after, pages := int64(0), 0
	for {
		r := call(t, p, read("daemon:big", after, ownerwire.MaxChatReadLimit))
		out, _ := ownerwire.EncodeSeatChatResult(r)
		if len(r.Entries) == 0 || len(r.Entries) >= ownerwire.MaxChatReadLimit || len(out) > ownerwire.MaxSeatChatResultBytes {
			t.Fatalf("a page of %d entries in %d bytes", len(r.Entries), len(out))
		}
		got = append(got, r.Entries...)
		pages++
		if !r.More {
			break
		}
		after = r.Entries[len(r.Entries)-1].Seq
	}
	if pages < 4 || !reflect.DeepEqual(got, es) {
		t.Fatalf("%d pages, %d entries", pages, len(got))
	}
}

// The port is safe for concurrent use: its samples are never written.
func TestConcurrentCalls(t *testing.T) {
	p := New(clock)
	want := call(t, p, read(LeadBravo, 2, 2))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if r, err := p.Chat(context.Background(), read(LeadBravo, 2, 2)); err != nil || !reflect.DeepEqual(r, want) {
					t.Errorf("goroutine %d call %d: %v", i, j, err)
					return
				}
				if _, err := p.Chat(context.Background(), ownerwire.SeatChatParams{Verb: ownerwire.ChatVerbLeads}); err != nil {
					t.Errorf("goroutine %d: %v", i, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
