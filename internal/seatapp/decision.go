package seatapp

import (
	"errors"
	"time"
)

// Reading is what one session did with one card: when the Card page last
// showed it, which parts of the card that page showed whole, and which pages
// of its whole path list the session read. The Card page holds the card on
// one page (its rows, its lists, its refs, its flagged paths and the
// "differs from" block), so a part it showed whole counts as read to its end
// once the page was served; a part with a path or a ref too long to be shown
// whole is not read, and the whole path list of a card whose rows are cut
// counts only page by page, by the pages that showed every path whole.
type Reading struct {
	// Opened is when the Card page last showed the card to the session.
	Opened time.Time
	// Whole are the parts of the card the Card page showed whole.
	Whole Whole
	// PagesRead are the numbers of the pages of the whole path list the
	// session read, each once, Pages how many pages that list had, and
	// Review the review digest those pages were of, as the port answered
	// them ([PathsPage].Review).
	PagesRead []int
	Pages     int
	Review    string
}

// Whole says, for each part of a card, that the Card page showed every
// name, line, path and ref of it whole: none was cut by [Text]. A part the
// card does not have is whole. The zero Whole says that nothing was shown
// whole.
type Whole struct {
	// Names are the names of the card's header and of the confirm box (the
	// project, the role, the verb, the feature, the target and the class),
	// Plan the plan's lines, Rows the diff's rows on the card, Lists the
	// four lists, Refs the ref lines with the branch each deleted one was
	// merged into, Flagged the flagged paths with their rules, Note the
	// lines of the lead's note, and Differs the paths and refs of the
	// "differs from" block.
	Names, Plan, Rows, Lists, Refs, Flagged, Note, Differs bool
}

// AllWhole is the Whole of a page that showed every part whole.
var AllWhole = Whole{Names: true, Plan: true, Rows: true, Lists: true, Refs: true, Flagged: true, Note: true, Differs: true}

// Offer is what the Card page may offer for a card as a session read it,
// as the port's card model answers: approve and deny, each with the host's
// line when it is not offered.
type Offer struct {
	// Approve says that an approve may be offered once every flagged path
	// is signed off; ApproveWhy is the host's line when it may not.
	Approve    bool
	ApproveWhy string
	// Deny says that a deny may be offered; DenyWhy is the host's line when
	// it may not.
	Deny    bool
	DenyWhy string
	// Inhibited says that the host the seat runs on takes no decision now
	// (the port would answer [ErrDecisionsInhibited] to a challenge): the
	// page then shows a notice and no decide form, and the card stays
	// readable. It is the port's answer for the page alone; the refusal of
	// DecideChallenge and Decide stays the enforcement.
	Inhibited bool
}

// SignOff is one sign-off of a decision: the flag's kind and the flagged
// path's raw bytes. It is for the port, never drawn on a page.
type SignOff struct {
	Kind string
	Path string
}

// DecisionFrame is the decision the port froze for a challenge, as the app
// keeps it with the session until the phone's answer comes: the card, the
// binding the card showed, approve or deny, the sign-offs in the order of
// the card's flagged paths, and the reason. The app takes it from the port
// and gives it back unchanged; it never builds one from what a browser sent.
type DecisionFrame struct {
	ID            string
	BindingSHA256 string
	Approve       bool
	SignedOff     []SignOff
	Reason        string
}

// DecideChallengeRequest is what the app hands the port when the owner
// tapped approve or deny on a card: the card and the binding the Card page
// showed this session, the action, the flagged paths signed off by their
// index in [Card].Flagged, the reason, the session's credential, the
// session's reading of the card, and the app's clock.
type DecideChallengeRequest struct {
	ID            string
	BindingSHA256 string
	Approve       bool
	SignedOff     []int
	Reason        string
	// Credential is the display id of the session's credential.
	Credential string
	Reading    Reading
	Now        time.Time
}

// DecideChallenge is the daemon's challenge for one decision, as the port
// checked it: the port recomputed Challenge from the gate's key, the frame
// and the daemon's nonce before it returned it.
type DecideChallenge struct {
	// ID is the daemon's id of the challenge.
	ID string
	// Challenge is the 32 bytes the passkey signs.
	Challenge []byte
	// Expires is when the daemon drops the challenge.
	Expires time.Time
	// Frame is the decision the challenge was drawn for.
	Frame DecisionFrame
}

// DecideRequest is the phone's answer to a decision's challenge: the
// challenge's id and the frame the app kept for it, and the four fields of
// the WebAuthn assertion.
type DecideRequest struct {
	ChallengeID       string
	Frame             DecisionFrame
	CredentialID      []byte
	AuthenticatorData []byte
	ClientDataJSON    []byte
	Signature         []byte
}

// DecideResult is the daemon's answer to a decision: the record's state
// after it, the host's line, and the held mark of an approve that came past
// the day's limit. State and Line are the host's; the app shows them with
// [Text].
type DecideResult struct {
	State, Line string
	Held        bool
}

// The errors of the decision calls of a Daemon that the app tells apart,
// beside [ErrChallengeStale], [ErrCardChanged], [ErrBusy], [ErrNotOffered]
// and [ErrDaemonUnavailable]. Every one but [ErrOutcomeUnknown] says for
// certain that nothing was decided.
var (
	// ErrOutcomeUnknown says that the decision was sent and its answer was
	// lost: the decision may have been taken, or not.
	ErrOutcomeUnknown = errors.New("seatapp: the decision was sent and its outcome is unknown")
	// ErrChallengeDiffers says that the daemon's challenge is not the one
	// the port recomputed from the card it showed; nothing was asked of the
	// phone.
	ErrChallengeDiffers = errors.New("seatapp: the daemon's challenge is not the card's")
	// ErrCardExpired says that the card's time ran out.
	ErrCardExpired = errors.New("seatapp: the card's time ran out")
	// ErrCannotDecide says that the card cannot be decided now: it was
	// decided or ended, or the decision is not one the daemon takes.
	ErrCannotDecide = errors.New("seatapp: the card cannot be decided now")
	// ErrAssertionRefused says that the daemon did not accept the phone's
	// assertion.
	ErrAssertionRefused = errors.New("seatapp: the assertion was not accepted")
	// ErrKeyRevoked says that the session's credential is revoked or no
	// longer on the daemon's list.
	ErrKeyRevoked = errors.New("seatapp: the passkey is no longer paired")
	// ErrKindRefused says that the card's project does not take decisions
	// from the credential's kind.
	ErrKindRefused = errors.New("seatapp: the project does not take this kind of passkey")
	// ErrDecisionsInhibited says that the host the seat runs on takes no
	// decision now, and that nothing was sent to the daemon: on the
	// authority host, the release switch's decision inhibit is there, or
	// cannot be looked up (package decisionguard). The port returns it
	// before it relays a challenge or a decision.
	ErrDecisionsInhibited = errors.New("seatapp: decisions are paused on this host")
)

// NotAllowedError says that the port's card model does not allow the
// decision asked: Reason is the host's line, such as a flagged path that is
// not signed off or a card past its expiry. Nothing was sent.
type NotAllowedError struct {
	Reason string
}

func (e *NotAllowedError) Error() string {
	return "seatapp: the card model does not allow it: " + e.Reason
}
