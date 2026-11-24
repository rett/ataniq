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
	// the outbox is gone, was replaced, or is not private any more.
	TextStoppedFolder = "Chat was stopped: the reply folder is gone, was replaced, or is no longer private. Turn chat on again to go on."
)

// TextStoppedFull is the one entry of a chat a scan stopped because the
// outbox held more than [MaxDirEntries] entries or more than [MaxWaiting]
// replies and folders.
var TextStoppedFull = fmt.Sprintf("Chat was stopped: the reply folder held more than %d entries, or more than %d replies and folders. Turn chat on again to go on.", MaxDirEntries, MaxWaiting)

// Reason is why an entry of the outbox was removed, or why it is counted.
// The reasons are the keys of the counts a scan and a store report.
type Reason string

// The reasons.
const (
	// ReasonRefused: a symbolic link, a FIFO, a socket, a device, a folder
	// that took a reply's place, a file of another user, a file with more
	// than one link, or a file that group or other may write. It is
	// removed.
	ReasonRefused Reason = "refused"
	// ReasonName: an entry whose name is not a reply name and does not
	// start with a dot. It is removed.
	ReasonName Reason = "name"
	// ReasonTooLong: a reply over [MaxReplyBytes]. It is removed.
	ReasonTooLong Reason = "too-long"
	// ReasonNotPlain: a reply that is not plain text by
	// [ownerwire.ValidChatText] once its trailing line feeds are trimmed
	// (invalid UTF-8, a tab, a carriage return, a control or format
	// character, or nothing but spaces). It is removed.
	ReasonNotPlain Reason = "not-plain"
	// ReasonStale: a file whose name starts with a dot (a reply being
	// written) that is older than [StaleAge]. It is removed.
	ReasonStale Reason = "stale"
	// ReasonBefore: an entry whose change time is before the turn-on
	// found the outbox empty: written before chat was on. It is removed,
	// never read.
	ReasonBefore Reason = "before"
	// ReasonFolder: a folder in the outbox. It is left as it is, and counts
	// toward [MaxWaiting].
	ReasonFolder Reason = "folder"
)

// dropTexts are the system entries of the drops that the owner is told
// about, at most one per reason a minute. Stale in-progress files, entries
// from before the turn-on and folders are only counted.
var dropTexts = map[Reason]string{
	ReasonRefused:  "Files were removed from the reply folder: they were not private plain files of the lead.",
	ReasonName:     "Files were removed from the reply folder: their names were not reply names.",
	ReasonTooLong:  fmt.Sprintf("Replies were dropped: they were longer than %d bytes.", MaxReplyBytes),
	ReasonNotPlain: "Replies were dropped: they were not plain text.",
}

// dropOrder is the order the drop entries of one scan are written in.
var dropOrder = []Reason{ReasonRefused, ReasonName, ReasonTooLong, ReasonNotPlain}

// systemText reports whether s is one of the fixed texts of a system entry:
// a replay refuses any other.
func systemText(s string) bool {
	switch s {
	case TextTurnedOn, TextWithheld, TextStoppedFolder, TextStoppedFull:
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
// shaped like a credential, weighed on the whole text, is withheld. text is
// set for VerdictOK only.
func ReplyText(data []byte) (text string, v Verdict) {
	if len(data) > MaxReplyBytes {
		return "", VerdictTooLong
	}
	s := strings.TrimRight(string(data), "\n")
	if !ownerwire.ValidChatText(s) {
		return "", VerdictNotPlain
	}
	if brief.SecretShaped(s) {
		return "", VerdictWithheld
	}
	return s, VerdictOK
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
