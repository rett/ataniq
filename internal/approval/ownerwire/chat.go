package ownerwire

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"ataniq/internal/approval/record"
)

// This file holds the words of seat.chat, the seat's read-only view of the
// leads' conversations: the seat lists the leads it may look at (the verb
// leads) and reads one lead's retained conversation in pages (the verb read).
// Nothing in it sends: there is no verb that writes to a lead, and no member
// of a result is, or leads to, a decision, a grant or an approval.
//
// The text of an entry is a lead's text, or a line the host writes about the
// lead ("Lead restarted, epoch 3"). A lead's text is untrusted: it is shown to
// the owner only, escaped, and never obeyed by anything. The wire holds it to
// plain text: valid UTF-8, no control character but the line feed, and no
// bidirectional or other format control, so a page can show it as it is,
// escaped, without a byte that would reorder or hide what is around it.
//
// See docs/design/seat-gate.md, "Chat".

// The bounds of the forms of seat.chat.
const (
	// MaxSeatChatParamsBytes caps the params of seat.chat.
	MaxSeatChatParamsBytes = MaxParamsBytes
	// MaxSeatChatResultBytes caps the result of seat.chat: 16 KiB. A read
	// is paged by the encoded bytes of its result, so one page is at most
	// this; [FillSeatChatRead] fills a page up to it.
	MaxSeatChatResultBytes = 16 << 10

	// MaxChatLeads is the most leads a list holds.
	MaxChatLeads = 8
	// MaxChatReadLimit is the most entries one read asks for and one page
	// holds.
	MaxChatReadLimit = 16
	// MaxChatText is the longest text of an entry, in bytes. A host that
	// has more to show shortens it to this and marks the entry cut.
	MaxChatText = 2000
	// MaxChatKeyBytes is the longest key of a lead identifier.
	MaxChatKeyBytes = 32
	// MaxChatSeq is the highest sequence number, epoch and head: the
	// largest integer a JavaScript number holds exactly, so a page's script
	// can carry the cursor without losing a digit.
	MaxChatSeq = 1<<53 - 1
)

// The verbs of seat.chat, the discriminator of its params.
const (
	// ChatVerbLeads lists the leads: no other member. It answers the state
	// leads.
	ChatVerbLeads = "leads"
	// ChatVerbRead reads one page of a lead's retained conversation: the
	// lead, the sequence number after which to read (after; 0 or absent
	// reads from the start of what is retained) and the most entries to
	// return (limit, 1 to [MaxChatReadLimit]). It answers the state read.
	ChatVerbRead = "read"
)

// The states of a result, its discriminator.
const (
	// ChatStateLeads is the answer to leads: the leads, and nothing of one
	// conversation.
	ChatStateLeads = "leads"
	// ChatStateRead is the answer to read: one page of one lead's
	// conversation.
	ChatStateRead = "read"
)

// ChatKindDaemon is the one kind of lead identifier: a lead the daemon runs.
// The identifier is "kind:key"; other kinds are reserved for later, so a
// later kind adds rows and not operations.
const ChatKindDaemon = "daemon"

// The chat switch of a lead, as a row of a list says it.
const (
	// ChatOff is a lead whose conversation the seat may not read. Its row
	// carries only the lead and the switch: no state, epoch or head.
	ChatOff = "off"
	// ChatRead is a lead whose conversation the seat may read.
	ChatRead = "read"
	// ChatSend is a lead whose conversation the seat may read and, in a
	// later slice, write to. Nothing of this slice sends.
	ChatSend = "send"
)

// The state of a lead, in one word, as a row of a list says it.
const (
	ChatLeadRunning  = "running"
	ChatLeadStarting = "starting"
	ChatLeadStopped  = "stopped"
	ChatLeadUnknown  = "unknown"
)

// Who wrote an entry. Only the two exist in this slice.
const (
	// ChatWhoLead is the lead's own text: untrusted, for the owner's eyes
	// only.
	ChatWhoLead = "lead"
	// ChatWhoSystem is a line the host writes about the lead, such as a
	// restart.
	ChatWhoSystem = "system"
)

// The gate's words for a refusal of seat.chat. Each is of the grammar of a
// gate code, [a-z0-9-]{1,40}. None of them is counted by the gate's refusal
// bound: each answers the state of a lead or of the host, which a seat in
// good order meets.
const (
	// CodeChatOff answers a read of a lead whose chat switch is off.
	CodeChatOff = "chat-off"
	// CodeChatUnknownLead answers a read of a lead the host does not know.
	CodeChatUnknownLead = "chat-unknown-lead"
	// CodeChatBusy answers a call the host cannot serve now; the seat may
	// ask again later.
	CodeChatBusy = "chat-busy"
)

// SeatChatParams is the params of seat.chat, a union tagged by Verb. Which
// other members a verb takes is closed, and a member of another verb is
// refused:
//
//	verb   members
//	leads  none
//	read   lead, after (optional), limit
type SeatChatParams struct {
	// Verb is leads or read. There is no verb that sends.
	Verb string `json:"verb"`
	// Lead is the lead's identifier, "kind:key" ([ValidChatLead]). Only a
	// read names one.
	Lead string `json:"lead,omitempty"`
	// After is the sequence number after which a read starts, 0 to
	// [MaxChatSeq]: 0 (left out) reads from the start of the retained
	// conversation, and the last sequence number of a page reads the next
	// page. Only a read has one.
	After int64 `json:"after,omitempty"`
	// Limit is the most entries a read returns, 1 to [MaxChatReadLimit].
	// Only a read has one. A page may hold fewer, to stay within
	// [MaxSeatChatResultBytes].
	Limit int `json:"limit,omitempty"`
}

// ChatLeadRow is one lead in the answer to leads. A row whose chat is off
// carries only Lead and Chat; every other row carries State, Epoch and Head
// as well. Epoch and Head are pointers so that the wire can refuse them on
// an off row even when they are written as 0.
type ChatLeadRow struct {
	// Lead is the lead's identifier, "kind:key".
	Lead string `json:"lead"`
	// Chat is the lead's chat switch: off, read or send.
	Chat string `json:"chat"`
	// State is the lead's state in one word: running, starting, stopped or
	// unknown. Absent on an off row.
	State string `json:"state,omitempty"`
	// Epoch is the lead's epoch, 0 to [MaxChatSeq]. Absent on an off row.
	Epoch *int64 `json:"epoch,omitempty"`
	// Head is the sequence number of the lead's last entry, 0 (no entry)
	// to [MaxChatSeq]. Absent on an off row.
	Head *int64 `json:"head,omitempty"`
}

// ChatEntry is one entry of a conversation.
type ChatEntry struct {
	// Seq is the entry's sequence number, 1 to [MaxChatSeq]; the entries of
	// a conversation are numbered in order, and a page holds them in that
	// order.
	Seq int64 `json:"seq"`
	// Who is lead or system.
	Who string `json:"who"`
	// At is when the host recorded the entry, a record time (UTC, to the
	// second, ending in Z).
	At string `json:"at"`
	// Text is the entry's text: 1 to [MaxChatText] bytes of plain text (see
	// [ValidChatText]). It is a [record.Text], so a text of several lines
	// travels as b64, since the codec writes no line feed in a string. A
	// lead's text is untrusted: shown to the owner only, escaped, never
	// obeyed.
	Text record.Text `json:"text"`
	// Cut is true when the host shortened the entry to fit [MaxChatText].
	Cut bool `json:"cut"`
}

// SeatChatResult is the result of seat.chat, a union tagged by State:
//
//	state  members
//	leads  leads (0 to 8 rows), stand_in
//	read   lead, epoch, entries (0 to 16), more, head, reset, stand_in
//
// StandIn is true on every result of a stand-in host, whose leads and
// conversations are samples: the seat shows a banner that says so.
type SeatChatResult struct {
	// State is leads or read.
	State string `json:"state"`
	// StandIn is true when the host is a stand-in that serves samples, not
	// leads. It is left out when false.
	StandIn bool `json:"stand_in,omitempty"`
	// Leads are the leads, at most [MaxChatLeads], none twice; given, and
	// possibly empty, for leads only.
	Leads []ChatLeadRow `json:"leads,omitempty"`
	// Lead is the lead the page is of; given for read only.
	Lead string `json:"lead,omitempty"`
	// Epoch is the lead's epoch, 0 to [MaxChatSeq]; read only.
	Epoch int64 `json:"epoch,omitempty"`
	// Entries are the page, in order of their sequence numbers, each above
	// the last, at most [MaxChatReadLimit]; given, and possibly empty, for
	// read only.
	Entries []ChatEntry `json:"entries,omitempty"`
	// More is true when the retained conversation holds entries after the
	// page's last: the seat reads on with after set to that entry's
	// sequence number. A page with more holds at least one entry, so the
	// cursor always moves on. Read only.
	More bool `json:"more,omitempty"`
	// Head is the sequence number of the lead's last entry, at least that
	// of the page's last; read only.
	Head int64 `json:"head,omitempty"`
	// Reset is true when the read's after was before the start of what the
	// host retains (or past the head, a cursor of a conversation the host
	// no longer holds): the page then starts at the start of what is
	// retained, and the seat drops what it held of the lead. Read only.
	Reset bool `json:"reset,omitempty"`
}

var (
	seatChatParamsForm = payload[SeatChatParams]{"seat chat", MaxSeatChatParamsBytes, record.CodeRecordInvalid, checkSeatChatParams}
	seatChatResultForm = payload[SeatChatResult]{"seat chatted", MaxSeatChatResultBytes, record.CodeRecordInvalid, checkSeatChatResult}
)

// DecodeSeatChatParams reads the params of seat.chat: a verb that is leads or
// read, and exactly the members that verb takes, each within its grammar and
// bounds. Params over [MaxSeatChatParamsBytes] are refused record-too-large;
// anything else that is wrong, such as an unknown verb (there is no verb that
// sends), a member of another verb, a lead identifier of another form or a
// limit out of its bounds, record-invalid. Whether the lead exists and whether
// its chat is on are for the host.
func DecodeSeatChatParams(data []byte) (SeatChatParams, error) {
	return seatChatParamsForm.decode(data)
}

// EncodeSeatChatParams writes the params of seat.chat. It makes the checks of
// [DecodeSeatChatParams].
func EncodeSeatChatParams(p SeatChatParams) ([]byte, error) {
	return seatChatParamsForm.encode(p)
}

// DecodeSeatChatResult reads the result of seat.chat. A result over
// [MaxSeatChatResultBytes] is refused record-too-large; anything else that is
// wrong, such as a member the state does not take, an off row with a state,
// an epoch or a head, a text that is not plain text, entries out of order or
// a page with more and no entry, record-invalid.
func DecodeSeatChatResult(data []byte) (SeatChatResult, error) {
	return seatChatResultForm.decode(data)
}

// EncodeSeatChatResult writes the result of seat.chat.
func EncodeSeatChatResult(r SeatChatResult) ([]byte, error) {
	return seatChatResultForm.encode(r)
}

// FillSeatChatRead makes one page of a read: r is the read result without its
// entries (state read, the lead, the epoch, the head, reset and stand_in),
// from are the entries after the read's cursor, in order, and limit is the
// read's limit (taken as 1 to [MaxChatReadLimit]). It takes entries from the
// front of from, at most limit of them, while the encoded result stays within
// [MaxSeatChatResultBytes], and sets More when it left any of from out. The
// page holds at least one entry when from holds one, since the largest entry
// fits a page on its own (a test holds that): an entry that does not fit even
// alone is refused with the encoder's error, and so is a page the encoder
// refuses for any other reason.
func FillSeatChatRead(r SeatChatResult, from []ChatEntry, limit int) (SeatChatResult, error) {
	limit = min(max(limit, 1), MaxChatReadLimit)
	r.Entries, r.More = []ChatEntry{}, false
	if _, err := EncodeSeatChatResult(r); err != nil {
		return SeatChatResult{}, err
	}
	for n := 0; n < len(from) && n < limit; n++ {
		next := r
		next.Entries = append(append(make([]ChatEntry, 0, n+1), r.Entries...), from[n])
		next.More = n+1 < len(from)
		if _, err := EncodeSeatChatResult(next); err != nil {
			var re *record.Error
			if n > 0 && errors.As(err, &re) && re.Code == record.CodeRecordTooLarge {
				break
			}
			return SeatChatResult{}, err
		}
		r = next
	}
	r.More = len(r.Entries) < len(from)
	return r, nil
}

// ValidChatLead reports whether s is a lead identifier: "kind:key", kind
// [ChatKindDaemon] (the one kind; others are reserved for later), and key 1 to
// [MaxChatKeyBytes] lower-case letters, digits and dashes, starting with a
// letter or a digit.
func ValidChatLead(s string) bool {
	kind, key, ok := strings.Cut(s, ":")
	if !ok || kind != ChatKindDaemon || len(key) < 1 || len(key) > MaxChatKeyBytes {
		return false
	}
	for i := 0; i < len(key); i++ {
		switch c := key[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && c == '-':
		default:
			return false
		}
	}
	return true
}

// ValidChatText reports whether s is the text of an entry: 1 to
// [MaxChatText] bytes of valid UTF-8, every rune a line feed or printable
// ([unicode.IsPrint]: letters, marks, numbers, punctuation, symbols and the
// ASCII space), and something besides spaces and line feeds. So every other
// control character (a tab, a carriage return, ESC, DEL, the C1 controls),
// the line and paragraph separators, the non-ASCII spaces, and the
// bidirectional and other format controls are refused: the rule is the
// character rule of the injection's message bodies (package
// internal/inject/command, CheckBody), which this package cannot import.
func ValidChatText(s string) bool {
	if len(s) < 1 || len(s) > MaxChatText || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r != '\n' && !unicode.IsPrint(r) {
			return false
		}
	}
	return strings.Trim(s, " \n") != ""
}

// chatLead checks a lead identifier.
func (c *checker) chatLead(s, where string) {
	c.need(ValidChatLead(s), where+" is not a lead identifier, daemon: and 1 to "+strconv.Itoa(MaxChatKeyBytes)+" lower-case letters, digits and dashes")
}

// chatSeq checks a sequence number, an epoch or a head: 0 to [MaxChatSeq],
// or 1 to it when positive is set.
func (c *checker) chatSeq(n int64, positive bool, where string) {
	low := int64(0)
	if positive {
		low = 1
	}
	c.need(n >= low && n <= MaxChatSeq, where+" is not "+strconv.FormatInt(low, 10)+" to 2^53-1")
}

func checkSeatChatParams(p *SeatChatParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	switch p.Verb {
	case ChatVerbLeads:
		c.need(p.Lead == "", "chat.lead is given for a leads")
		c.need(p.After == 0, "chat.after is given for a leads")
		c.need(p.Limit == 0, "chat.limit is given for a leads")
	case ChatVerbRead:
		c.chatLead(p.Lead, "chat.lead")
		c.chatSeq(p.After, false, "chat.after")
		c.need(p.Limit >= 1 && p.Limit <= MaxChatReadLimit, "chat.limit is not 1 to "+strconv.Itoa(MaxChatReadLimit))
	default:
		c.need(false, "chat.verb is not leads or read")
	}
	return c.result()
}

// chatRows checks the rows of a list: at most [MaxChatLeads], none twice, an
// off row with nothing but its lead and switch, and every other row with a
// state word, an epoch and a head.
func (c *checker) chatRows(rows []ChatLeadRow) {
	c.need(rows != nil, "chat.leads is missing for a leads")
	c.need(len(rows) <= MaxChatLeads, "chat.leads is more than "+strconv.Itoa(MaxChatLeads)+" leads")
	seen := make(map[string]struct{}, len(rows))
	for i := range rows {
		r, at := &rows[i], "chat.leads["+strconv.Itoa(i)+"]"
		c.chatLead(r.Lead, at+".lead")
		_, dup := seen[r.Lead]
		c.need(!dup, at+" repeats an earlier lead")
		seen[r.Lead] = struct{}{}
		switch r.Chat {
		case ChatOff:
			c.need(r.State == "" && r.Epoch == nil && r.Head == nil, at+" is off and carries a state, an epoch or a head")
			continue
		case ChatRead, ChatSend:
		default:
			c.need(false, at+".chat is not off, read or send")
			continue
		}
		switch r.State {
		case ChatLeadRunning, ChatLeadStarting, ChatLeadStopped, ChatLeadUnknown:
		default:
			c.need(false, at+".state is not running, starting, stopped or unknown")
		}
		c.need(r.Epoch != nil, at+".epoch is missing")
		c.need(r.Head != nil, at+".head is missing")
		if r.Epoch != nil {
			c.chatSeq(*r.Epoch, false, at+".epoch")
		}
		if r.Head != nil {
			c.chatSeq(*r.Head, false, at+".head")
		}
	}
}

// chatEntries checks a page: at most [MaxChatReadLimit] entries, each of its
// grammar, in order of their sequence numbers, none above the head.
func (c *checker) chatEntries(es []ChatEntry, head int64) {
	c.need(es != nil, "chat.entries is missing for a read")
	c.need(len(es) <= MaxChatReadLimit, "chat.entries is more than "+strconv.Itoa(MaxChatReadLimit)+" entries")
	last := int64(0)
	for i := range es {
		e, at := &es[i], "chat.entries["+strconv.Itoa(i)+"]"
		c.chatSeq(e.Seq, true, at+".seq")
		c.need(e.Seq > last, at+".seq is not above the one before it")
		c.need(e.Seq <= head, at+".seq is above chat.head")
		last = e.Seq
		c.need(e.Who == ChatWhoLead || e.Who == ChatWhoSystem, at+".who is not lead or system")
		c.time(e.At, at+".at")
		c.need(ValidChatText(string(e.Text)), at+".text is not 1 to "+strconv.Itoa(MaxChatText)+" bytes of plain text")
	}
}

func checkSeatChatResult(r *SeatChatResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	switch r.State {
	case ChatStateLeads:
		c.need(r.Lead == "", "chat.lead is given for a leads")
		c.need(r.Epoch == 0 && r.Head == 0, "chat.epoch or chat.head is given for a leads")
		c.need(r.Entries == nil, "chat.entries is given for a leads")
		c.need(!r.More && !r.Reset, "chat.more or chat.reset is given for a leads")
		c.chatRows(r.Leads)
	case ChatStateRead:
		c.need(r.Leads == nil, "chat.leads is given for a read")
		c.chatLead(r.Lead, "chat.lead")
		c.chatSeq(r.Epoch, false, "chat.epoch")
		c.chatSeq(r.Head, false, "chat.head")
		c.chatEntries(r.Entries, r.Head)
		c.need(!r.More || len(r.Entries) > 0, "chat.more is given for a page with no entry")
	default:
		c.need(false, "chat.state is not leads or read")
	}
	return c.result()
}
