package appmodel

import (
	"math"
	"strconv"
	"strings"
)

// MessageID is the stable identity of one conversation entry: the authority,
// the stable lead role, the conversation's ID, the generation of the readable
// segment and the entry's sequence within that segment. An attach, a page
// refresh or a change of viewer keeps it; a fresh readable segment has a fresh
// generation. Desk and seat compare whole values. The value is data, not a
// proof of disclosure: a reader may see an entry only under current authority.
//
// The canonical text form is
//
//	m1/<authority>/<lead>/<conversation>/<generation>/<seq>
//
// where the first three parts use the application ID alphabet (which has no
// slash) and the last two are decimal numbers from 1 through MaxInt64 without
// a sign or leading zero. It carries no secret, so any surface can reproduce
// it from the tuple alone; the shared vectors are in
// testdata/message-ids-v1.json.
type MessageID struct {
	Authority, Lead, Conversation string
	Generation, Seq               uint64
}

const messageIDPrefix = "m1"

// Validate reports whether every part is in its domain.
func (m MessageID) Validate() error {
	for _, s := range []string{m.Authority, m.Lead, m.Conversation} {
		if !validID(s) {
			return invalid()
		}
	}
	if m.Generation == 0 || m.Generation > math.MaxInt64 || m.Seq == 0 || m.Seq > math.MaxInt64 {
		return invalid()
	}
	return nil
}

// String returns the canonical text form of a valid ID, and "" for an invalid
// one, so that an invalid value never looks like an identity.
func (m MessageID) String() string {
	if m.Validate() != nil {
		return ""
	}
	return strings.Join([]string{messageIDPrefix, m.Authority, m.Lead, m.Conversation,
		strconv.FormatUint(m.Generation, 10), strconv.FormatUint(m.Seq, 10)}, "/")
}

// ParseMessageID parses exactly the canonical text form.
func ParseMessageID(s string) (MessageID, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 6 || parts[0] != messageIDPrefix {
		return MessageID{}, invalid()
	}
	gen, err1 := strconv.ParseUint(parts[4], 10, 63)
	seq, err2 := strconv.ParseUint(parts[5], 10, 63)
	m := MessageID{Authority: parts[1], Lead: parts[2], Conversation: parts[3], Generation: gen, Seq: seq}
	if err1 != nil || err2 != nil || m.String() != s {
		return MessageID{}, invalid()
	}
	return m, nil
}
