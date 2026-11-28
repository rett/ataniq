//go:build linux && ataniq_canary

package leadchat

import (
	"fmt"
	"strings"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/brief"
)

// MaxReplyBytes is the largest reply file, in bytes: the longest text of an
// entry on the wire. A larger file is dropped whole, never cut.
const MaxReplyBytes = ownerwire.MaxChatText

// The fixed texts of the system entries. Each is plain text by
// [ownerwire.ValidChatText] (a test holds that), and none holds anything a
// lead wrote. A text that names a bound is made from its constant.
const (
	// TextTurnedOn opens every segment: the first entry after the desk
	// turned chat on for the lead.
	TextTurnedOn = "Chat was turned on. Replies written from now on show here."
	// TextWithheld stands for a reply that looked like a credential: no
	// text of it is stored.
	TextWithheld = "A reply was withheld: it looked like a key, a token or a full commit id."
	// TextStoppedFolder is the one entry of a chat a scan stopped because
	// the outbox is gone, was replaced, is not private any more, or can no
	// longer be read or claimed from.
	TextStoppedFolder = "Chat was stopped: the reply folder is gone, was replaced, is no longer private, or cannot be read. Turn chat on again to go on."
	// TextStoppedOdd is the one entry of a chat a scan stopped because the
	// outbox held a folder, a symbolic link, a FIFO, a socket or a device,
	// or one took a reply's name before it was claimed.
	TextStoppedOdd = "Chat was stopped: the reply folder held a folder, a link or a special file. Turn chat on again to go on."
)

// TextStoppedFull is the one entry of a chat a scan stopped because the
// outbox held more than [MaxWaiting] entries with the replies claimed and not
// yet read.
var TextStoppedFull = fmt.Sprintf("Chat was stopped: more than %d files were waiting in the reply folder. Turn chat on again to go on.", MaxWaiting)

// Reason is why a claimed reply was dropped. The reasons are the keys of the
// counts a scan and a store report.
type Reason string

// The reasons.
const (
	// ReasonRefused: a reply that is not the lead's private, readable plain
	// file: of another user, with more than one link, that group or other
	// may write, that its owner may not read, or that the daemon may not
	// open.
	ReasonRefused Reason = "refused"
	// ReasonTooLong: a reply over [MaxReplyBytes].
	ReasonTooLong Reason = "too-long"
	// ReasonNotPlain: a reply that is not plain text by
	// [ownerwire.ValidChatText] once its trailing line feeds are trimmed
	// (invalid UTF-8, a tab, a carriage return, a control or format
	// character, or nothing but spaces).
	ReasonNotPlain Reason = "not-plain"
	// ReasonUnsettled: a reply that could not be read whole within its
	// bounds: it changed while it was read, a lease held it or its read
	// failed [UnsettledTries] times, or it was still changing
	// [UnsettledAge] after a scan first left it.
	ReasonUnsettled Reason = "unsettled"
)

// dropTexts are the system entries of the drops, at most one per reason a
// minute. None holds a name, a path, a count or a byte of the reply.
var dropTexts = map[Reason]string{
	ReasonRefused:   "Replies were dropped: they were not private, readable plain files of the lead.",
	ReasonTooLong:   fmt.Sprintf("Replies were dropped: they were longer than %d bytes.", MaxReplyBytes),
	ReasonNotPlain:  "Replies were dropped: they were not plain text.",
	ReasonUnsettled: "Replies were dropped: they kept changing after they were published.",
}

// dropOrder is the order the drop entries of one scan are written in.
var dropOrder = []Reason{ReasonRefused, ReasonTooLong, ReasonNotPlain, ReasonUnsettled}

// systemText reports whether s is one of the fixed texts of a system entry:
// a replay refuses any other.
func systemText(s string) bool {
	switch s {
	case TextTurnedOn, TextWithheld, TextStoppedFolder, TextStoppedOdd, TextStoppedFull:
		return true
	}
	for _, t := range dropTexts {
		if s == t {
			return true
		}
	}
	return false
}

// Verdict is what [ReplyText] makes of a reply.
type Verdict int

// The verdicts.
const (
	// VerdictOK: the text is stored as the lead's entry.
	VerdictOK Verdict = iota
	// VerdictWithheld: the text looked like a credential; no text is
	// stored, and the entry is the system's [TextWithheld].
	VerdictWithheld
	// VerdictTooLong: the reply is over [MaxReplyBytes]; it is dropped.
	VerdictTooLong
	// VerdictNotPlain: the reply is not plain text; it is dropped.
	VerdictNotPlain
)

// ReplyText judges the bytes of a reply file. Over [MaxReplyBytes] the reply
// is too long, whatever it holds; it is never cut. Otherwise its trailing
// line feeds are trimmed and the rest must be plain text by
// [ownerwire.ValidChatText]. A plain text that [brief.SecretShaped] finds
// shaped like a credential, weighed on the whole text, is withheld, and so is
// one that holds a full commit id ([fullHexID]) of a single character class,
// which [brief.SecretShaped] lets pass. text is set for VerdictOK only.
func ReplyText(data []byte) (text string, v Verdict) {
	if len(data) > MaxReplyBytes {
		return "", VerdictTooLong
	}
	s := strings.TrimRight(string(data), "\n")
	if !ownerwire.ValidChatText(s) {
		return "", VerdictNotPlain
	}
	if brief.SecretShaped(s) || fullHexID(s) {
		return "", VerdictWithheld
	}
	return s, VerdictOK
}

// fullHexID reports whether s holds a bare token of exactly 40 or 64
// hexadecimal digits: a full commit id of SHA-1 or SHA-256, which the lead's
// instructions say is withheld. A token is a run of ASCII letters and digits;
// one that is wholly hexadecimal and 40 or 64 long is a full id whatever its
// characters (all digits, all letters or both). A short id (7 to 12 digits)
// and a longer or shorter run pass.
func fullHexID(s string) bool {
	start := -1
	for i := 0; i <= len(s); i++ {
		if i < len(s) && isAlnum(s[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if n := i - start; (n == 40 || n == 64) && allHex(s[start:i]) {
				return true
			}
			start = -1
		}
	}
	return false
}

func isAlnum(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func allHex(t string) bool {
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// ValidReplyName reports whether name is the name of a finished reply:
// [a-z0-9][a-z0-9._-]{0,59} followed by ".txt".
func ValidReplyName(name string) bool {
	stem, ok := strings.CutSuffix(name, ".txt")
	if !ok || len(stem) < 1 || len(stem) > 60 {
		return false
	}
	for i := 0; i < len(stem); i++ {
		switch c := stem[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}
