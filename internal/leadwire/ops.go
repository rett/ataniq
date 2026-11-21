package leadwire

// The six operations of the lead control plane, in the order the daemon
// lists them in its hello.
const (
	OpList   = "lead.list"
	OpDefine = "lead.define"
	OpStart  = "lead.start"
	OpStop   = "lead.stop"
	OpTicket = "lead.ticket"
	OpInject = "lead.inject"
)

// Operations returns the six operation names in the order the daemon lists
// them: lead.list, lead.define, lead.start, lead.stop, lead.ticket,
// lead.inject. A client takes "all six" as present, "none" as absent and
// "some" as a version error. The slice is a new one on every call.
func Operations() []string {
	return []string{OpList, OpDefine, OpStart, OpStop, OpTicket, OpInject}
}

// Mutating reports whether op is one of the five operations that change
// something, which take a client and a command id and answer with a
// [Receipt]. Only [OpList] does not.
func Mutating(op string) bool {
	switch op {
	case OpDefine, OpStart, OpStop, OpTicket, OpInject:
		return true
	}
	return false
}

// Status is where a command stands, the "status" of a [Receipt].
type Status string

// The statuses. [StatusDone], [StatusRefused], [StatusUnavailable] and
// [StatusUncertain] are final; [StatusInProgress] is not.
const (
	// StatusInProgress: the command is admitted and not finished.
	StatusInProgress Status = "in-progress"
	// StatusDone: the command was carried out; the receipt's result says
	// what it did.
	StatusDone Status = "done"
	// StatusRefused: the daemon refused the command and changed nothing that
	// matters; the receipt's code says why.
	StatusRefused Status = "refused"
	// StatusUnavailable: the command could not be carried out; the receipt's
	// code says why.
	StatusUnavailable Status = "unavailable"
	// StatusUncertain: the daemon stopped between admitting the command and
	// finishing it, so what it did is not known. The client reads lead.list
	// and never resends.
	StatusUncertain Status = "uncertain"
)

// Final reports whether s is a status a command does not leave.
func (s Status) Final() bool {
	switch s {
	case StatusDone, StatusRefused, StatusUnavailable, StatusUncertain:
		return true
	}
	return false
}

// The error codes of an answer that is an error, which happen only before a
// command is admitted. After admission the answer is a result holding a
// [Receipt]. "invalid-request" belongs to the client protocol; "conflict" is
// the one the lead extension adds; "refused" and "unavailable" are the
// canary extension's and also appear as a [Receipt]'s status words.
const (
	// ErrCodeInvalidRequest: the params are not valid for the operation.
	ErrCodeInvalidRequest = "invalid-request"
	// ErrCodeConflict: the command id was used before with another operation,
	// role or payload, or it is no longer held.
	ErrCodeConflict = "conflict"
	// ErrCodeUnavailable: the table of commands is full or could not be
	// written, so nothing was admitted.
	ErrCodeUnavailable = "unavailable"
	// ErrCodeRefused: the role is not one of the daemon's leads.
	ErrCodeRefused = "refused"
)

// The reason codes of a refused receipt that the control plane adds to the
// canary's. A receipt's code is one short token of lower-case letters, digits
// and dashes.
const (
	// CodeMaxRunning: starting would pass the daemon's max_running.
	CodeMaxRunning = "max-running"
	// CodeSecondMessage: a message was already sent in this epoch.
	CodeSecondMessage = "second-message"
	// CodeMessageInFlight: another message of the role is being delivered.
	CodeMessageInFlight = "message-in-flight"
	// CodeStaleEpoch: the command names an epoch that is not the role's.
	CodeStaleEpoch = "stale-epoch"
)

// The attach roles of a ticket.
const (
	AttachWriter = "writer"
	AttachViewer = "viewer"
)

// The values of [Lead.Provision]: whether the lead's folders and clone are
// all made, partly made, or not started.
const (
	ProvisionDone       = "done"
	ProvisionIncomplete = "incomplete"
	ProvisionNone       = "none"
)

// Caps. They bound what the daemon admits and what it answers, and the tests
// of this package measure the largest answers against [MaxRelayBytes].
const (
	// MaxLeads is the most leads one daemon is configured with (and so the
	// most rows of lead.list).
	MaxLeads = 8
	// MaxKey is the longest lead key, in bytes. The project is "scratch-"
	// and the key, and the layout name that follows is at most 63 bytes.
	MaxKey = 50
	// MaxClient is the longest client string, in bytes.
	MaxClient = 64
	// MaxCommandID is the longest command id, in bytes.
	MaxCommandID = 64
	// MaxMessageID is the longest message id of lead.inject.
	MaxMessageID = 64
	// MaxBody is the longest body of lead.inject, in bytes.
	MaxBody = 4096
	// MaxInjectDetail is the longest detail of a [Delivery], in bytes.
	MaxInjectDetail = 512
	// MaxMessage is the longest fixed text of a receipt, in bytes.
	MaxMessage = 256
	// MaxReceiptLine is the most a stored receipt occupies in the command
	// table, in bytes.
	MaxReceiptLine = 4096
	// MaxRelayBytes is the size under which the largest lead.list answer and
	// the largest inject receipt stay, so that an approval frame (8 KiB) can
	// carry them in a later increment.
	MaxRelayBytes = 8192
)
