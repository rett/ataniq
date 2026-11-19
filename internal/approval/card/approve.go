package card

import (
	"strconv"
	"time"
	"unicode/utf8"

	"ataniq/internal/approval/record"
)

// OpenDelay is how long a card must have been open before it may be
// approved, so that a key press aimed at another screen cannot approve a
// card that has just opened.
const OpenDelay = 400 * time.Millisecond

// Reading is what the reader has done with one opening of a card. A surface
// keeps one Reading for each time a card is opened and starts a new one when
// the card is opened again.
type Reading struct {
	// Opened is when the card opened.
	Opened time.Time
	// SignedOff are the sign-offs the reader made, in any order.
	SignedOff []record.SignOff
	// Paths says that the path list on the card was read to its end.
	Paths bool
	// Lists says, for each of the four lists, that it was read to its end.
	// A [ListKind] is the index. An empty list counts as read once the
	// line that shows it empty was on the screen.
	Lists [NumLists]bool
	// Differs says that the "differs from" block was passed.
	Differs bool

	// The pages of the whole path list read so far, for one review digest
	// and one page count. [Reading.ReadPage] keeps them; a card whose
	// path list is cut asks for all of them.
	pathDigest string
	pathCount  int
	pathPages  map[int]struct{}
}

// ReadPage notes that the reader read the page p to its end. Only a page of
// the whole path list changes anything. Every page of that list has to be
// read before a cut list counts, in any order and each once or more. The
// pages read are those of one review digest and one page count: a page of
// another digest, or of the same digest with another number of pages,
// starts the count again with that page alone. A page whose number is not
// between 0 and Pages-1 is ignored.
func (r *Reading) ReadPage(p Page) {
	if p.Kind != PagePaths || p.N < 0 || p.N >= p.Pages {
		return
	}
	// The set is copied before it changes, so that a copy of a Reading
	// does not share it.
	next := make(map[int]struct{}, len(r.pathPages)+1)
	if r.pathDigest == p.ReviewDigest && r.pathCount == p.Pages {
		for n := range r.pathPages {
			next[n] = struct{}{}
		}
	}
	next[p.N] = struct{}{}
	r.pathDigest, r.pathCount, r.pathPages = p.ReviewDigest, p.Pages, next
}

// pathPagesRead reports whether every page of the path list of the review
// digest was read.
func (r Reading) pathPagesRead(digest string) bool {
	return digest != "" && r.pathDigest == digest && r.pathCount >= 1 && len(r.pathPages) == r.pathCount
}

// Signed reports whether the reader signed off s.
func (r Reading) Signed(s record.SignOff) bool {
	for _, have := range r.SignedOff {
		if have == s {
			return true
		}
	}
	return false
}

// Toggle signs off s, or takes the sign-off back when it was made already.
func (r *Reading) Toggle(s record.SignOff) {
	kept := make([]record.SignOff, 0, len(r.SignedOff)+1)
	found := false
	for _, have := range r.SignedOff {
		if have == s {
			found = true
			continue
		}
		kept = append(kept, have)
	}
	if !found {
		kept = append(kept, s)
	}
	r.SignedOff = kept
}

// The reasons MayApprove gives for a "no". Each is one line of the host's
// own words; none holds text of a lead.
const (
	// ReasonNoCard answers the zero Card.
	ReasonNoCard = "there is no card"
	// ReasonEnded answers a card whose request ended. MayApprove adds a
	// colon and the state.
	ReasonEnded = "this request ended"
	// ReasonExpired answers a card past its expiry.
	ReasonExpired = "this request is past its expiry"
	// ReasonDiffers answers a "differs from" block that was not passed.
	ReasonDiffers = "the block that says how this card differs from the earlier one was not read"
	// ReasonPaths answers a path list that was not read to its end.
	ReasonPaths = "the path list was not read to its end"
	// ReasonPathPages answers a cut path list of which a page was not
	// read.
	ReasonPathPages = "the path list is cut on the card, and a page of the whole list was not read"
	// ReasonUnsigned answers flagged paths without a sign-off. MayApprove
	// puts their number before it.
	ReasonUnsigned = "not signed off"
	// ReasonSurplus answers a sign-off of a path that is not flagged.
	ReasonSurplus = "a sign-off names a path that is not flagged"
	// ReasonNotOpened answers a reading with no opening time, or one that
	// is later than now.
	ReasonNotOpened = "the card was not opened"
	// ReasonJustOpened answers a card that opened less than [OpenDelay]
	// ago.
	ReasonJustOpened = "the card opened less than 400 ms ago"
)

// reasonList is the reason for one of the four lists that was not read.
func reasonList(k ListKind) string {
	return "the list of " + k.noun() + " was not read to its end"
}

// MayApprove is the one rule for when a card may be approved. The answer is
// yes only when all of these hold:
//
//   - the card has not ended, and now is before its expiry;
//   - the "differs from" block, when the card has one, was passed;
//   - the path list and each of the four lists were read to their end; when
//     the record holds only the first part of the path list, the path list
//     counts only when every page of the whole list was read, from the
//     review the card was made from (see [Reading.ReadPage]);
//   - every flagged path is signed off, and no sign-off names a path that
//     is not flagged;
//   - at least [OpenDelay] has gone by since the card opened.
//
// When the answer is no, reason is one line that says which of them failed
// first, in the order above. A held card is pending, and may be approved
// again like any other.
func (c Card) MayApprove(r Reading, now time.Time) (ok bool, reason string) {
	if c.BindingSHA256 == "" {
		return false, ReasonNoCard
	}
	if c.Ended() {
		return false, ReasonEnded + ": " + string(c.After.State)
	}
	if !now.Before(c.Title.Expires) {
		return false, ReasonExpired
	}
	if c.Differs != nil && !r.Differs {
		return false, ReasonDiffers
	}
	if d := c.Diff; d != nil {
		if d.Truncated {
			if !r.pathPagesRead(d.ReviewDigest) {
				return false, ReasonPathPages
			}
		} else if !r.Paths {
			return false, ReasonPaths
		}
	}
	if c.Diff != nil {
		for k := ListKind(0); int(k) < NumLists; k++ {
			if !r.Lists[k] {
				return false, reasonList(k)
			}
		}
	}
	flagged := make(map[record.SignOff]bool, len(c.Flagged))
	for _, f := range c.Flagged {
		flagged[f.SignOff()] = true
	}
	signed := make(map[record.SignOff]bool, len(r.SignedOff))
	for _, s := range r.SignedOff {
		signed[s] = true
	}
	missing := 0
	for s := range flagged {
		if !signed[s] {
			missing++
		}
	}
	switch {
	case missing == 1:
		return false, "1 flagged path is " + ReasonUnsigned
	case missing > 1:
		return false, strconv.Itoa(missing) + " flagged paths are " + ReasonUnsigned
	}
	for s := range signed {
		if !flagged[s] {
			return false, ReasonSurplus
		}
	}
	if r.Opened.IsZero() || now.Before(r.Opened) {
		return false, ReasonNotOpened
	}
	if now.Sub(r.Opened) < OpenDelay {
		return false, ReasonJustOpened
	}
	return true, ""
}

// SignedOff returns the sign-offs of r as a decision carries them: those of
// the card's flagged paths, in the card's order, each once. A sign-off of r
// that names no flagged path is left out; MayApprove refuses such a reading.
func (c Card) SignedOff(r Reading) []record.SignOff {
	out := make([]record.SignOff, 0, len(c.Flagged))
	for _, f := range c.Flagged {
		if s := f.SignOff(); r.Signed(s) {
			out = append(out, s)
		}
	}
	return out
}

// Approve returns the decision that approves the card, when MayApprove
// allows it. Otherwise ok is false and reason is MayApprove's. The decision
// names the digest the card was made from. The typed code is not part of
// it: checking the code is the surface's own step, before it calls Approve.
func (c Card) Approve(r Reading, now time.Time) (d Decision, ok bool, reason string) {
	if ok, reason := c.MayApprove(r, now); !ok {
		return Decision{}, false, reason
	}
	return Decision{
		ID:            c.Title.ID,
		BindingSHA256: c.BindingSHA256,
		Approve:       true,
		SignedOff:     c.SignedOff(r),
	}, true, ""
}

// The reasons Deny gives for a "no".
const (
	// ReasonTooLong answers a deny whose reason is over
	// [record.MaxReasonBytes].
	ReasonTooLong = "the reason is longer than 500 bytes"
	// ReasonNotText answers a deny whose reason holds a control character
	// or bytes that are not UTF-8, which no record can carry.
	ReasonNotText = "the reason holds a control character"
)

// Deny returns the decision that denies the card with the owner's reason,
// which may be empty. A deny needs no reading: it is refused only when there
// is no card, when the request ended or is past its expiry, or when the
// reason is too long or cannot stand in a record.
func (c Card) Deny(why string, now time.Time) (d Decision, ok bool, reason string) {
	switch {
	case c.BindingSHA256 == "":
		return Decision{}, false, ReasonNoCard
	case c.Ended():
		return Decision{}, false, ReasonEnded + ": " + string(c.After.State)
	case !now.Before(c.Title.Expires):
		return Decision{}, false, ReasonExpired
	case len(why) > record.MaxReasonBytes:
		return Decision{}, false, ReasonTooLong
	case !recordString(why):
		return Decision{}, false, ReasonNotText
	}
	return Decision{ID: c.Title.ID, BindingSHA256: c.BindingSHA256, Reason: why}, true, ""
}

// recordString reports whether s can be a plain string of a record: valid
// UTF-8 with no control character (U+0000 to U+001F, U+007F, U+0080 to
// U+009F).
func recordString(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}

// Action is what a key of a card does.
type Action int

// The actions of a card.
const (
	// ActionSignOff signs off the flagged path under the cursor.
	ActionSignOff Action = iota + 1
	// ActionDiff opens the full patch.
	ActionDiff
	// ActionApprove starts the confirm step.
	ActionApprove
	// ActionDeny denies the request.
	ActionDeny
	// ActionLead opens the lead that asked.
	ActionLead
	// ActionBack leaves the card.
	ActionBack
)

// Key is one key of a card: the key's name, what it does, and the word for
// it in the key hints.
type Key struct {
	Key    string
	Action Action
	Label  string
}

// Keys returns the last section of a card: the keys it offers, in the order
// of the key hints. A card that ended offers no sign-off, no approve and no
// deny; a card without flagged paths no sign-off; a card without a diff no
// diff. Whether approve may be pressed now is MayApprove's answer, not this
// list's.
func (c Card) Keys() []Key {
	if c.BindingSHA256 == "" {
		return nil
	}
	live := !c.Ended()
	var keys []Key
	if live && len(c.Flagged) > 0 {
		keys = append(keys, Key{Key: "space", Action: ActionSignOff, Label: "sign off"})
	}
	if live {
		keys = append(keys,
			Key{Key: "a", Action: ActionApprove, Label: "approve"},
			Key{Key: "x", Action: ActionDeny, Label: "deny"})
	}
	if c.Diff != nil {
		keys = append(keys, Key{Key: "d", Action: ActionDiff, Label: "diff"})
	}
	return append(keys,
		Key{Key: "l", Action: ActionLead, Label: "open the lead"},
		Key{Key: "esc", Action: ActionBack, Label: "back"})
}
