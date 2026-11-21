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
	leftOutSuffix = " rows left out: the list is not all of the daemon's leads"
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
	// The client protocol's, and the ones the console's adapter makes itself.
	"invalid-request": "the daemon did not accept the request",
	"conflict":        "the command id is no longer held; read the list",
	"refused":         "the daemon refused it",
	"unavailable":     "the daemon could not carry it out",
	"not-listed":      "the lead is not one of this daemon's",

	// The control plane's own (leadwire).
	"max-running":       "the limit of running leads is reached",
	"second-message":    "a message was already sent in this epoch",
	"message-in-flight": "a message is already in flight for this lead",
	"stale-epoch":       "the epoch changed; read the list and try again",

	// Any command.
	"role-busy":            "another command for this lead is running; try again",
	"role-not-defined":     "the lead is not defined",
	"not-managed":          "the lead is not run by this daemon",
	"failed":               "the operation failed; see the daemon's log",
	"panic":                "the operation failed unexpectedly; see the daemon's log",
	"daemon-broken":        "the daemon cannot write its files; it must be restarted",
	"receipt-not-recorded": "the command ran but its receipt was not recorded; read the list",
	"interrupted":          "the daemon restarted before the command finished; read the list",
	"daemon-stopping":      "the daemon was stopping; read the list",
	"registry-refused":     "the registry refused the change; read the list",
	"registry-check":       "the registry may or may not hold the change; read the list",
	"registry-broken":      "the registry cannot be written; the daemon must be restarted",

	// Define.
	"provision-incomplete": "an earlier define was cut short; the owner clears the lead's folders",
	"provision-failed":     "provisioning failed; see the daemon's log",
	"provision-refused":    "provisioning refused the lead; see the daemon's log",
	"record-invalid":       "the lead's record is not valid; the owner checks its folders",
	"digest-pin":           "the profile digest is not the one pinned for the lead",
	"holder-command":       "the lead's holder command could not be built; see the daemon's log",

	// Start and stop.
	"not-provisioned":        "the lead is not provisioned; define it first",
	"folder-missing":         "a folder of the lead is missing or not private",
	"preflight":              "the preflight refused the start",
	"foreign-unit":           "a unit with the lead's name is not its own unit",
	"unit-loaded":            "a unit with the lead's name is already loaded",
	"limits":                 "the unit's limits are outside the bounds",
	"launch-refused":         "the registry does not admit a launch in the lead's state",
	"epoch-exhausted":        "the lead has no epoch left to start",
	"previous-unit-not-gone": "the previous unit is not proven gone",
	"start-failed":           "the start failed and nothing was started",
	"launch-uncertain":       "the start's outcome is not known; read the list",
	"intent-pending":         unsettledWords,
	"stop-unproven":          "the stop is not proven; read the list",

	// Ticket and message.
	"not-running":        "the lead is not running",
	"holder-unreachable": "the lead's holder could not be reached",
	"ticket-failed":      "no ticket could be made",
	"ticket-refused":     "the holder refused the ticket; a writer may already be attached",
	"ticket-lost":        "the holder did not answer the ticket request",
	"ticket-file":        "the ticket was taken but its file could not be written",
	"holder-disabled":    "the lead's holder does not take messages",
	"holder-wrong-epoch": "the holder is at another epoch; read the list and try again",
	"holder-busy":        "the holder is busy with an unrecorded delivery; the epoch's message is spent",
	"holder-bad-request": "the holder did not accept the request; nothing was written",
	"inject-off":         "messages are off for this lead",
	"bad-message":        "the message was not accepted",
	"bad-id":             "the message id was not accepted",
	"bad-target":         "the message target was not accepted",
	"body-too-long":      "the message is longer than the daemon takes",
	"invalid-utf8":       "the message is not valid text",
	"bad-character":      "the message holds a character that cannot be sent",
	"empty-body":         "there is nothing to send",
	"forged-header":      "the message holds text that looks like a message header",
	"bad-body":           "the message body was not accepted",
	"bad-text":           "the message text was not accepted",
}

// unsettledWords is what a launch that is not settled needs: the daemon in
// scratch mode settles it itself once the late-start window of 30 s and its
// margin of 10 s have passed since the launch was recorded (reading the list
// does not settle it). A daemon that does not (one that cannot ask the unit
// manager, or has no timer) settles it at its reconcile when it is restarted
// after that. The words name the two ways out when it is not settled: stopping
// the lead (the way out when the launch's own unit was adopted, which the
// timer and a restart both leave unknown) and the restart.
const unsettledWords = "launch settles by itself 40 s after the start; if not, stop the lead or restart"

// maxRunningUnknownTail is added to the refusal for the limit of running
// leads while the list shows a lead that is unknown: an unknown lead counts
// as running, and it stays so until its launch is settled, which the daemon
// does itself 40 s after the start (stopping the lead or a restart is the
// fallback).
const maxRunningUnknownTail = ". An unknown lead holds a slot until its launch settles by itself, 40 s after the start; if it is still unknown after that, stop the lead or restart"

// maxRunningPlainTail is added instead while the list shows a lead that is
// unknown for another reason (a unit that is not ours, a registry that was
// recovered, a reboot): the daemon has no timer for it, so the words promise
// no self-settle and name what the owner can do.
const maxRunningPlainTail = ". An unknown lead holds a slot until it is settled; stop the lead or restart"

// Reasons of an unknown lead that the daemon settles by itself (its settle
// timer in internal/leaddaemon/lead_settle_linux.go, settleWanted): a launch
// that was not settled or whose reply was lost, and a control group that was
// not proven empty when the timer tried (the timer tries again).
const (
	reasonLaunchUnsettled = "launch-unsettled"
	reasonLaunchReplyLost = "launch-reply-lost"
	reasonGroupNotProven  = "control-group-not-proven-empty"
)

// launchReason says whether the lead is unknown for the reason of a launch
// that was not settled: nothing is known of whether its unit runs.
func launchReason(l Lead) bool {
	return l.State == "unknown" && (l.StateReason == reasonLaunchUnsettled || l.StateReason == reasonLaunchReplyLost)
}

// settlesItself says whether the daemon's settle timer is for the lead: a
// launch reason, or a control group that is not proven empty (which only a
// timer that is trying sets).
func settlesItself(l Lead) bool {
	return launchReason(l) || l.State == "unknown" && l.StateReason == reasonGroupNotProven
}

const unknownReason = "the reason is not known to this console"

// fate is what became of a message, as far as the view can say.
type fate int

const (
	// fateUnknown: the result is not one the view has words for.
	fateUnknown fate = iota
	// fateNotDelivered: nothing was written to the lead.
	fateNotDelivered
	// fateDelivered: the whole message was written and submitted.
	fateDelivered
	// fateUnsure: something may have reached the lead; only a person can
	// settle it.
	fateUnsure
)

type deliveryKey struct{ outcome, reason, written string }

type deliveryText struct {
	fate fate
	text string
}

// deliveryWords is a fixed text for every combination of the holder's
// outcome, reason and written that its wire carries (the table in
// internal/leadholder/wire/inject.go; a test reads it from the source). The
// text says what happened to the message: submitted, NOT delivered, written
// but not submitted, or a result that is not known.
var deliveryWords = map[deliveryKey]deliveryText{
	{OutcomeSubmitted, "", WrittenWhole}: {fateDelivered,
		"the message was submitted: the whole of it was written and sent"},
	{OutcomeNotNow, ReasonNotReady, WrittenNothing}: {fateNotDelivered,
		"NOT delivered: nothing was written, the lead's screen is not idle"},
	{OutcomeNotNow, ReasonBlocked, WrittenNothing}: {fateNotDelivered,
		"NOT delivered: nothing was written, the lead's screen is blocked"},
	{OutcomeRefused, ReasonUntested, WrittenNothing}: {fateNotDelivered,
		"NOT delivered: nothing was written, the lead's program version is not one the holder is tested with"},
	{OutcomeRefused, ReasonText, WrittenNothing}: {fateNotDelivered,
		"NOT delivered: nothing was written, the holder refused the text"},
	{OutcomeUncertain, ReasonNotSubmitted, WrittenPartial}: {fateUnsure,
		"written but not submitted: the text reached the lead's input and Enter was not sent. Look at the lead's screen"},
	{OutcomeUncertain, ReasonDied, WrittenNothing}: {fateUnsure,
		"result unknown: the lead ended during the send; the holder reports nothing written. Look at the lead's screen"},
	{OutcomeUncertain, ReasonDied, WrittenPartial}: {fateUnsure,
		"result unknown: the lead ended during the send; part of the message was written. Look at the lead's screen"},
	{OutcomeUncertain, ReasonDied, WrittenWhole}: {fateUnsure,
		"result unknown: the lead ended during the send; the whole message was written. Look at the lead's screen"},
	{OutcomeUncertain, ReasonDied, WrittenUnknown}: {fateUnsure,
		"result unknown: the lead ended or no answer came back; how much was written is not known. Look at the lead's screen"},
}

// outcomeOnlyWords is the text for an outcome whose reason and written the
// receipt does not carry (an adapter that does not pass them). What each says
// about the lead's input is what the wire says of the outcome.
var outcomeOnlyWords = map[string]deliveryText{
	OutcomeSubmitted: {fateDelivered, "the message was submitted"},
	OutcomeNotNow:    {fateNotDelivered, "NOT delivered: nothing was written, the lead could not take it now"},
	OutcomeRefused:   {fateNotDelivered, "NOT delivered: nothing was written, the lead refused it"},
	OutcomeUncertain: {fateUnsure, "result unknown: it may have reached the lead. Look at the lead's screen"},
}

const unknownResult = "the result is not known to this console"

// What the daemon does with the epoch's message once a request was written to
// the holder (internal/leaddaemon/canary_ext_linux.go injectFor): it is spent,
// unless the holder answered that nothing was written (not-now or refused), when
// it is free again and the lead may send another. The receipt does not say
// which: the daemon holds the epoch spent all the same when it could not write
// the result down, and only the list's row (message_spent) knows. So the tail
// of a message that was not delivered is drawn from the row when the view has
// read it since the answer, and says what it does not know otherwise.
const (
	spentTail = ". This epoch's one message is spent"
	freeTail  = ". The epoch's message is free"
	// heldTail: the row says spent after a message that was not delivered.
	heldTail = ". The daemon holds this epoch's message spent all the same"
	// maybeTail: no row has been read since the answer.
	maybeTail = ". The epoch's message is free unless the list says it is spent"
	// movedTail: the row is at another epoch than the answer's (the lead was
	// stopped or started since, perhaps by another client), so nothing is
	// known here of that epoch's message and the row's flag is not its.
	movedTail = ". The lead is at another epoch now; that epoch's message is not known"
)

// held is what the list's row says of the epoch's message for the receipt
// that is drawn.
type held int

const (
	// heldUnknown: the view has no row of the receipt's lead and epoch that it
	// read after the answer.
	heldUnknown held = iota
	// heldFree and heldSpent: the row's message_spent.
	heldFree
	heldSpent
	// heldMoved: the row is at another epoch than the receipt's.
	heldMoved
)

// deliveryOf looks up the words of a done inject's three words. A result
// whose reason or written is set and is not a combination the holder sends,
// or whose outcome is not one it sends, is not known.
func deliveryOf(outcome, reason, written string) deliveryText {
	if t, ok := deliveryWords[deliveryKey{outcome, reason, written}]; ok {
		return t
	}
	if reason == "" && written == "" {
		if t, ok := outcomeOnlyWords[outcome]; ok {
			return t
		}
	}
	return deliveryText{fateUnknown, unknownResult}
}

// outcomeWords is the line of a done inject's result: what happened to the
// message and, when something is known, whether the epoch's message is spent
// (it is not after a message that was not delivered, unless the daemon holds it
// spent, which the row says: h).
func outcomeWords(r Receipt, h held) string {
	t := deliveryOf(r.Outcome, r.Reason, r.Written)
	switch t.fate {
	case fateUnknown:
		return t.text
	case fateNotDelivered:
		switch h {
		case heldFree:
			return t.text + freeTail
		case heldSpent:
			return t.text + heldTail
		case heldMoved:
			return t.text + movedTail
		}
		return t.text + maybeTail
	}
	return t.text + spentTail
}

// running says whether the lead has an epoch that is running, as far as the
// row says: it has an epoch and its state is not one of a lead that is not
// running (stopped, ended, desired, unknown). A lead that is not running has no
// message to speak of, and the epoch the row still carries after a stop or an
// unknown start is the last one's.
func running(l Lead) bool {
	if l.Epoch == 0 {
		return false
	}
	switch l.State {
	case "", "stopped", "ended", "desired", "unknown":
		return false
	}
	return true
}

// msgWord is the MSG column: whether the lead takes a message ("off" when it
// does not), "-" for a lead that is not running, and what became of the epoch's
// message for one that is. It agrees with the receipt line of a done message: a
// message that was NOT delivered leaves the epoch's message free, and says so
// while it is free. The same result of a message that the daemon holds spent
// (it could not write down the result) reads as spent and not delivered.
//
// A spent message is "sent" only when the words say it was submitted. When
// something may have reached the lead, when the words are not ones this console
// knows, and when the daemon holds the epoch spent and there are no words at
// all (a message whose answer was lost, one sent before the console started or
// by another client, a daemon that was restarted since, a holder answer the
// daemon could not use), the word is "sent-unsure": the frame was written, and
// nothing says the lead has the message.
func msgWord(l Lead, d Delivery) string {
	f := deliveryOf(d.Outcome, d.Reason, d.Written).fate
	switch {
	case !l.Inject:
		return "off"
	case !running(l):
		return "-"
	case !l.MessageSpent && f == fateNotDelivered:
		return "free (not delivered)"
	case !l.MessageSpent:
		return "free"
	}
	switch f {
	case fateNotDelivered:
		return "spent, not delivered"
	case fateDelivered:
		return "sent"
	}
	return "sent-unsure"
}

// stateWords are the words for a registry state and the reason shown with it,
// where the state alone says too little. The pair ready and holder-ready is
// what the daemon shows once the holder is up and the agent started.
var stateWords = map[[2]string]string{
	{"ready", "holder-ready"}: "the holder is up and the agent started; its login is not observed",
	// A launch whose reply was lost, or that a restarted daemon found inside
	// its window: the daemon settles it when the window ends.
	{"unknown", reasonLaunchUnsettled}: unsettledWords,
	{"unknown", reasonLaunchReplyLost}: unsettledWords,
}

// stateLine is the line about the row under the cursor: its state in words,
// where the table's cell has no room for them. It is empty when there is
// nothing to add.
func stateLine(l Lead) string {
	w, ok := stateWords[[2]string{l.State, l.StateReason}]
	if !ok {
		return ""
	}
	return l.Key + ": " + l.State + " (" + l.StateReason + "): " + w
}

// receiptLine is the full line of a receipt, in the view's words; h is what the
// list's row says of the epoch's message.
func receiptLine(r Receipt, h held) string {
	head := opWords(r.Op) + ": "
	if r.Earlier {
		head = "an earlier " + opWords(r.Op) + " command: "
	}
	switch r.Status {
	case InProgress:
		return head + "accepted, still running"
	case Done:
		if r.Op == OpInject {
			return head + "accepted. " + outcomeWords(r, h)
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
