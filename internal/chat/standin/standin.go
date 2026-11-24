// Package standin is the STAND-IN chat port of the seat gate: fixed sample
// leads and sample conversations, held in memory, which the daemon serves as
// seat.chat in the phone trial only (ataniqd run --owner-channel --trial), so
// the owner can try the seat's lead pages with no risk.
//
// Nothing in it is a lead. It reads no conversation, reaches no lead, holder,
// unit or sandbox, opens no file and no socket, and asks no other port of the
// daemon: the samples are made once, by [New], from fixed text and the clock it
// is given, and every call answers from them. Every result it gives is marked
// stand_in, so the seat shows a banner that says the leads are samples.
//
// It serves the two verbs of seat.chat as the wire has them (package
// internal/approval/ownerwire): leads lists three sample leads, one of them
// with its chat switched off, and read reads a sample conversation in pages,
// paged by encoded bytes with [ownerwire.FillSeatChatRead]. A read of the off
// lead is refused chat-off, and a read of a lead it does not hold
// chat-unknown-lead. It never answers chat-busy.
//
// The samples show what the seat must handle: a long reply the host shortened
// (cut), a reply of several lines, a reply that looks like markup (which the
// seat must show as text), lines the host writes about a lead (a restart), a
// conversation whose oldest entries are no longer retained (a cursor before
// them reads from the start, with reset), and a lead whose chat is off.
//
// Every entry's text, a lead's or a system line, begins "Sample:".
//
// The package imports the standard library and package ownerwire only.
package standin

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"ataniq/internal/approval/ownerwire"
)

// The sample leads.
const (
	// LeadAlpha is a running lead at epoch 3 whose chat is read. Its three
	// oldest entries are no longer retained: its conversation starts at
	// sequence number 4.
	LeadAlpha = "daemon:alpha"
	// LeadBravo is a stopped lead at epoch 1 whose chat is read.
	LeadBravo = "daemon:bravo"
	// LeadCharlie is a lead whose chat is off: its row carries nothing but
	// its identifier and its switch, and a read of it is refused chat-off.
	LeadCharlie = "daemon:charlie"
)

// timeLayout is the wire's time: RFC 3339 in UTC to the second, ending in Z.
const timeLayout = "2006-01-02T15:04:05Z"

// refusal is a refusal of the stand-in, which carries the gate's word.
type refusal string

func (r refusal) Error() string { return "chat stand-in: " + string(r) }

// GateCode is the gate's word of the refusal, which the gate's handler passes
// on to the seat.
func (r refusal) GateCode() string { return string(r) }

// sampleLead is one lead and its retained conversation, oldest first.
type sampleLead struct {
	id, chat, state string
	epoch           int64
	// first is the sequence number of the first retained entry; the
	// entries are numbered from it, one by one.
	first   int64
	entries []ownerwire.ChatEntry
}

// head is the sequence number of the lead's last entry, or the one before
// its first retained entry when it holds none.
func (l *sampleLead) head() int64 { return l.first - 1 + int64(len(l.entries)) }

// Port is the stand-in chat port. Its samples are made once and never
// changed, so it is safe for concurrent use. Make one with [New].
type Port struct {
	leads []sampleLead
}

// New returns the stand-in with its samples. now is the clock the sample
// times are drawn from, once: each entry is a fixed number of minutes before
// the minute now() falls in, so the same clock gives the same samples. nil is
// time.Now.
func New(now func() time.Time) *Port {
	if now == nil {
		now = time.Now
	}
	return newWith(samples(now().UTC().Truncate(time.Minute)))
}

// newWith returns a stand-in that serves the given leads.
func newWith(leads []sampleLead) *Port {
	return &Port{leads: leads}
}

// line is one entry of a sample conversation before it is numbered: who
// wrote it, how many minutes before the base time, its text and whether it
// was cut.
type line struct {
	who  string
	ago  int
	text string
	cut  bool
}

// conversation numbers lines from first and gives them their times.
func conversation(base time.Time, first int64, lines []line) []ownerwire.ChatEntry {
	out := make([]ownerwire.ChatEntry, len(lines))
	for i, l := range lines {
		out[i] = ownerwire.ChatEntry{
			Seq: first + int64(i), Who: l.who, At: base.Add(-time.Duration(l.ago) * time.Minute).Format(timeLayout), Cut: l.cut,
		}
		setText(&out[i].Text, l.text)
	}
	return out
}

// setText sets an entry's text. The text's type is the record package's,
// which this package does not import: its type is inferred here.
func setText[T ~string](dst *T, s string) { *dst = T(s) }

// longReply is the sample of a reply the host shortened: steps of a plan, one
// a line, cut to fit the wire's cap with an ellipsis at the end.
func longReply() string {
	var b strings.Builder
	b.WriteString("Sample: here is the whole plan I am following for the sample project, one step a line.\n")
	const ellipsis = "\u2026"
	for n := 1; ; n++ {
		step := "Step " + strconv.Itoa(n) + ": read the files of part " + strconv.Itoa(n) +
			", write the change, run the tests of that part and note what changed.\n"
		if b.Len()+len(step)+len(ellipsis) > ownerwire.MaxChatText {
			break
		}
		b.WriteString(step)
	}
	b.WriteString(ellipsis)
	return b.String()
}

// samples are the three sample leads, their times taken back from base.
func samples(base time.Time) []sampleLead {
	return []sampleLead{
		{
			id: LeadAlpha, chat: ownerwire.ChatRead, state: ownerwire.ChatLeadRunning, epoch: 3, first: 4,
			entries: conversation(base, 4, []line{
				{ownerwire.ChatWhoSystem, 95, "Sample: lead restarted, epoch 2", false},
				{ownerwire.ChatWhoLead, 90, "Sample: I am back on the sample project and reading where I stopped.", false},
				{ownerwire.ChatWhoLead, 70, longReply(), true},
				{ownerwire.ChatWhoLead, 52, `Sample: I wrote <b>bold</b> & "quotes" and <a href="https://example.com">a link</a> in the notes. A page shows them as text, never as markup.`, false},
				{ownerwire.ChatWhoSystem, 40, "Sample: lead restarted, epoch 3", false},
				{ownerwire.ChatWhoLead, 35, "Sample: back at work on the second task.\nNothing needs your approval now; an approval is only ever a card.", false},
				{ownerwire.ChatWhoLead, 5, "Sample: the second task is done and its tests pass. I am starting the third.", false},
			}),
		},
		{
			id: LeadBravo, chat: ownerwire.ChatRead, state: ownerwire.ChatLeadStopped, epoch: 1, first: 1,
			entries: conversation(base, 1, []line{
				{ownerwire.ChatWhoSystem, 300, "Sample: lead started, epoch 1", false},
				{ownerwire.ChatWhoLead, 290, "Sample: I am reviewing the sample branch.", false},
				{ownerwire.ChatWhoLead, 250, "Sample: the review is done.\n- two files changed\n- the tests pass\n- one note for you on the card", false},
				{ownerwire.ChatWhoLead, 245, "Sample: I am stopping now. Start me again from the desk when you need me.", false},
				{ownerwire.ChatWhoSystem, 244, "Sample: lead stopped", false},
			}),
		},
		{id: LeadCharlie, chat: ownerwire.ChatOff},
	}
}

// Chat answers one call of seat.chat from the samples: leads lists the three
// sample leads, and read reads one page of a sample conversation. A read of
// the lead whose chat is off is refused chat-off, a read of any other lead
// chat-unknown-lead. A cursor before the retained start, or past the head,
// reads from the start with reset. Every result is marked stand_in. A call
// whose context is done answers its error.
func (p *Port) Chat(ctx context.Context, params ownerwire.SeatChatParams) (ownerwire.SeatChatResult, error) {
	if err := ctx.Err(); err != nil {
		return ownerwire.SeatChatResult{}, err
	}
	switch params.Verb {
	case ownerwire.ChatVerbLeads:
		return p.list(), nil
	case ownerwire.ChatVerbRead:
		return p.read(params)
	}
	return ownerwire.SeatChatResult{}, refusal("bad-params")
}

// list is the answer to leads: a row for each sample lead, the off one bare.
func (p *Port) list() ownerwire.SeatChatResult {
	rows := make([]ownerwire.ChatLeadRow, 0, len(p.leads))
	for i := range p.leads {
		l := &p.leads[i]
		row := ownerwire.ChatLeadRow{Lead: l.id, Chat: l.chat}
		if l.chat != ownerwire.ChatOff {
			epoch, head := l.epoch, l.head()
			row.State, row.Epoch, row.Head = l.state, &epoch, &head
		}
		rows = append(rows, row)
	}
	return ownerwire.SeatChatResult{State: ownerwire.ChatStateLeads, StandIn: true, Leads: rows}
}

// read is one page of a lead's conversation.
func (p *Port) read(params ownerwire.SeatChatParams) (ownerwire.SeatChatResult, error) {
	var l *sampleLead
	for i := range p.leads {
		if p.leads[i].id == params.Lead {
			l = &p.leads[i]
		}
	}
	switch {
	case l == nil:
		return ownerwire.SeatChatResult{}, refusal(ownerwire.CodeChatUnknownLead)
	case l.chat == ownerwire.ChatOff:
		return ownerwire.SeatChatResult{}, refusal(ownerwire.CodeChatOff)
	}
	after, head, reset := params.After, l.head(), false
	if after != 0 && (after < l.first-1 || after > head) {
		after, reset = 0, true
	}
	skip := 0
	if after >= l.first {
		skip = int(after - l.first + 1)
	}
	page := ownerwire.SeatChatResult{State: ownerwire.ChatStateRead, StandIn: true, Lead: l.id, Epoch: l.epoch, Head: head, Reset: reset}
	r, err := ownerwire.FillSeatChatRead(page, l.entries[skip:], params.Limit)
	if err != nil {
		return ownerwire.SeatChatResult{}, errors.New("chat stand-in: a sample page does not encode")
	}
	return r, nil
}

// ChatCursor answers the chat cursor of the extended seat.wait over the
// samples: [ownerwire.ChatCursor] of the sample leads whose chat is read,
// each of switch generation 1 and its head. The samples never change, so the
// cursor is the same for every call and every stand-in: a held wait is never
// woken for the chat. The lead whose chat is off is not in it. A call whose
// context is done answers its error.
func (p *Port) ChatCursor(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var leads []ownerwire.ChatCursorLead
	for i := range p.leads {
		if l := &p.leads[i]; l.chat != ownerwire.ChatOff {
			leads = append(leads, ownerwire.ChatCursorLead{Lead: l.id, Gen: 1, Head: l.head()})
		}
	}
	return ownerwire.ChatCursor(leads), nil
}

// ChatCursorChanged answers nil: the samples never change, so the cursor
// never moves.
func (p *Port) ChatCursorChanged() <-chan struct{} { return nil }
