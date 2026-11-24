package ownerwire

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"

	"ataniq/internal/approval/record"
)

// This file holds the extended form of seat.wait: the chat cursor, a hint
// that the leads' chat the seat may read moved, carried beside the Card
// version on the one long wait the seat already holds. The gate offers it only
// when its hello lists the capability wait-chat, and a seat asks for it only
// then. See docs/design/seat-gate.md, "Chat hints on the wait".
//
// The two forms are distinct. The plain form is the wait as it always was:
// [DecodeSeatWaitParams] and [DecodeSeatWaitResult] refuse a chat member, as a
// gate or a seat that does not know it does, so a seat that never asked for
// the extension never meets it, and a seat that sends it to a gate that does
// not offer it is refused bad-params. The extended form is
// [DecodeSeatWaitChatParams] (params that carry chat, 0 to [MaxChatSeq]) and
// [DecodeSeatWaitChatResult] (a result that may carry chat, 1 to
// [MaxChatSeq], and nothing else beside the plain result's members). An
// extended result without chat says the daemon had no cursor to give: the
// seat drops what it held of the chat.
//
// The cursor is a hint, never content: an integer that a page's script can
// carry, which changes when what the seat may read of the chat changes, and
// for nothing else. It is computed by [ChatCursor] from the leads whose chat
// the desk switched on (read, not suspended) alone, so a lead whose chat is
// off moves nothing.

// ChatCursorLead is one desk-enabled lead as a chat cursor covers it: the
// lead's identifier, the generation of its switch (the segment its turn-on
// started) and the head of its chat. A lead whose chat is off, or stopped by a
// scan, is not given to [ChatCursor] at all.
type ChatCursorLead struct {
	// Lead is the lead's identifier, "kind:key".
	Lead string
	// Gen is the generation of the lead's switch: a new segment, a reset,
	// is a new generation.
	Gen int64
	// Head is the sequence number of the lead's last entry, 0 for none.
	Head int64
}

// chatCursorDomain starts the bytes [ChatCursor] hashes, so that no other
// digest of this package can be taken for a cursor.
const chatCursorDomain = "ataniq.seat-wait.chat/1"

// ChatCursor returns the chat cursor of a set of desk-enabled leads: an
// integer of 1 to [MaxChatSeq], from the first 8 bytes of the SHA-256 of a
// canonical encoding of the set. Like [PendingVersion] it is a digest, not a
// counter, so a daemon that restarts with the same leads, generations and
// heads gives the same cursor; it is never 0, which a seat sends when it has
// none.
//
// The encoding is the domain string, then the number of leads, then each lead
// in the order of its identifier: the identifier as a 4-byte big-endian length
// and its bytes, and the generation and the head, each 8 bytes big-endian.
// The order of the items does not matter and the argument is not changed. An
// empty set has a cursor too. So the cursor changes when an enabled lead's
// head moves, when its segment is reset (a new generation), and when a lead
// is enabled or disabled; nothing else is in it.
func ChatCursor(leads []ChatCursorLead) int64 {
	sorted := append([]ChatCursorLead(nil), leads...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		switch {
		case a.Lead != b.Lead:
			return a.Lead < b.Lead
		case a.Gen != b.Gen:
			return a.Gen < b.Gen
		}
		return a.Head < b.Head
	})
	h := sha256.New()
	var n4 [4]byte
	var n8 [8]byte
	field := func(s string) {
		binary.BigEndian.PutUint32(n4[:], uint32(len(s)))
		h.Write(n4[:])
		h.Write([]byte(s))
	}
	number := func(v int64) {
		binary.BigEndian.PutUint64(n8[:], uint64(v))
		h.Write(n8[:])
	}
	field(chatCursorDomain)
	binary.BigEndian.PutUint32(n4[:], uint32(len(sorted)))
	h.Write(n4[:])
	for _, l := range sorted {
		field(l.Lead)
		number(l.Gen)
		number(l.Head)
	}
	v := int64(binary.BigEndian.Uint64(h.Sum(nil)[:8]) & MaxChatSeq)
	if v == 0 {
		v = 1
	}
	return v
}

var (
	seatWaitChatParamsForm = payload[SeatWaitParams]{"seat wait", MaxParamsBytes, record.CodeRecordInvalid, checkSeatWaitChatParams}
	seatWaitChatResultForm = payload[SeatWaitResult]{"seat waited", MaxSeatWaitResultBytes, record.CodeRecordInvalid, checkSeatWaitChatResult}
)

// DecodeSeatWaitChatParams reads the params of the extended seat.wait: the
// since of the plain form and chat, which must be given, 0 to [MaxChatSeq].
// The plain form's params, without chat, are refused: a gate that offers the
// extension reads params with [DecodeSeatWaitParams] first and with this
// only when that refused them. Params over [MaxParamsBytes] are refused
// record-too-large; anything else that is wrong record-invalid.
func DecodeSeatWaitChatParams(data []byte) (SeatWaitParams, error) {
	return seatWaitChatParamsForm.decode(data)
}

// EncodeSeatWaitChatParams writes the params of the extended seat.wait. It
// makes the checks of [DecodeSeatWaitChatParams].
func EncodeSeatWaitChatParams(p SeatWaitParams) ([]byte, error) {
	return seatWaitChatParamsForm.encode(p)
}

// DecodeSeatWaitChatResult reads the result of the extended seat.wait: the
// plain result's members, held to the same rules, and chat, 1 to
// [MaxChatSeq], or absent when the daemon had no cursor to give. A result
// over [MaxSeatWaitResultBytes] is refused record-too-large; anything else
// that is wrong, a member that is neither the plain result's nor chat among
// it, record-invalid.
func DecodeSeatWaitChatResult(data []byte) (SeatWaitResult, error) {
	return seatWaitChatResultForm.decode(data)
}

// EncodeSeatWaitChatResult writes the result of the extended seat.wait. It
// makes the checks of [DecodeSeatWaitChatResult].
func EncodeSeatWaitChatResult(r SeatWaitResult) ([]byte, error) {
	return seatWaitChatResultForm.encode(r)
}

func checkSeatWaitChatParams(p *SeatWaitParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.need(p.Since == "" || record.IsLowerHex(p.Since, SeatWaitVersionLength), "seat.since is neither empty nor 32 lower-case hexadecimal digits")
	c.need(p.Chat != nil, "seat.chat is missing in the extended wait")
	if p.Chat != nil {
		c.chatSeq(*p.Chat, false, "seat.chat")
	}
	return c.result()
}

func checkSeatWaitChatResult(r *SeatWaitResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	if r.Chat != nil {
		c.chatSeq(*r.Chat, true, "seat.chat")
	}
	checkWaitCards(c, r)
	return c.result()
}
