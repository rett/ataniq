package leadctl

import "strconv"

// The view's own words. Nothing a peer sent is drawn but through safeID,
// which keeps a short ASCII subset, and through the tables below, which map a
// closed set of codes to fixed texts.

const (
	title         = "LEAD CONTROL"
	noRowLine     = "there is no lead under the cursor"
	noRandomLine  = "no source of randomness: nothing was sent"
	noListLine    = "the list could not be read"
	readingLine   = "reading the list"
	noneLine      = "no leads in this daemon"
	lostAnswer    = "no answer came back: the command may or may not have run. The list was read again; nothing was sent a second time"
	lostInject    = "no answer came back: the message may or may not have reached the lead. Read the list; it shows whether the epoch's message was sent"
	unusableAns   = "the answer could not be used"
	busyLine      = "a command for this lead is still being sent"
	unconfirmed   = "a message for this epoch has no final answer: read the list before you decide anything"
	notRunning    = "the lead has no epoch: it is not running"
	injectOffLine = "messages are off for this lead"
	spentLine     = "a message was already sent in this epoch"
	blankLine     = "there is nothing to send"
	pasteLine     = "that paste was not taken: it holds text that cannot be sent"
	tooLongLine   = "the message is at the longest the lead takes"
)

// opWords is the name of an operation in the view's words.
func opWords(o Op) string {
	switch o {
	case OpDefine:
		return "define"
	case OpStart:
		return "start"
	case OpStop:
		return "stop"
	case OpTicket:
		return "attach ticket"
	case OpInject:
		return "message"
	}
	return "command"
}

// shortStatus is the status in a few words, for the table.
func shortStatus(s Status) string {
	switch s {
	case InProgress:
		return "accepted (running)"
	case Done:
		return "accepted (done)"
	case Refused:
		return "refused"
	case Unavailable:
		return "unavailable"
	case Uncertain:
		return "uncertain"
	}
	return "unknown status"
}

// reasonWords maps a reason code of a refusal or of an unavailable receipt to
// a fixed text. A code that is not here is drawn as unknownReason.
var reasonWords = map[string]string{
	"invalid-request":    "the daemon did not accept the request",
	"conflict":           "the command id is no longer held; read the list",
	"refused":            "the daemon refused it",
	"unavailable":        "the daemon could not carry it out",
	"max-running":        "the limit of running leads is reached",
	"second-message":     "a message was already sent in this epoch",
	"message-in-flight":  "a message is already in flight for this lead",
	"stale-epoch":        "the epoch changed; read the list and try again",
	"inject-off":         "messages are off for this lead",
	"not-running":        "the lead is not running",
	"bad-message":        "the message was not accepted",
	"holder-unreachable": "the lead's holder could not be reached",
	"foreign-unit":       "a unit with the lead's name is not its own unit",
	"registry":           "the registry refused; check the lead's state",
	"folder":             "a folder of the lead is missing or not private",
	"failed":             "the operation failed; see the daemon's log",
	"stopping":           "the daemon is stopping",
	"table-full":         "the daemon holds too many unfinished commands",
	"not-listed":         "the lead is not one of this daemon's",
}

const unknownReason = "the reason is not known to this console"

// outcomeWords maps the outcome of a done inject to a fixed text.
func outcomeWords(o string) string {
	switch o {
	case OutcomeSubmitted:
		return "the message was submitted"
	case OutcomeNotNow:
		return "the lead could not take it now; nothing was written"
	case OutcomeRefused:
		return "the lead refused it; nothing was written"
	case OutcomeUncertain:
		return "it may have reached the lead; look at the lead's screen before you decide"
	}
	return "the result is not known to this console"
}

// receiptLine is the full line of a receipt, in the view's words.
func receiptLine(r Receipt) string {
	head := opWords(r.Op) + ": "
	switch r.Status {
	case InProgress:
		return head + "accepted, still running"
	case Done:
		if r.Op == OpInject {
			return head + "accepted. " + outcomeWords(r.Outcome)
		}
		return head + "accepted, done"
	case Refused:
		return head + "refused: " + reasonText(r.Code)
	case Unavailable:
		return head + "unavailable: " + reasonText(r.Code)
	case Uncertain:
		return head + "uncertain: the outcome is not known. Read the lead's state, then decide; nothing is sent again for you"
	}
	return head + "the status is not known to this console"
}

func reasonText(code string) string {
	if t, ok := reasonWords[code]; ok {
		return t
	}
	return unknownReason
}

// safeID returns s cut to n bytes with every byte outside a short ASCII set
// replaced by "?". It is how a name a peer sent reaches a cell.
func safeID(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-', c == ':', c == '@', c == '/', c == '+':
		default:
			b[i] = '?'
		}
	}
	return string(b)
}

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }
