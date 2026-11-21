package consoleclient

import "time"

// The bounds of a view. A value over a bound is cut or left out, and the view
// says so; it is never an error.
const (
	// MaxText is the most bytes a string of a view holds. A longer one is cut
	// and ends in "…", and is at most MaxText bytes with that mark.
	MaxText = 256
	// MaxProjects is the most projects a view holds.
	MaxProjects = 32
	// MaxCoordinators is the most coordinators a project of a view holds.
	MaxCoordinators = 64
)

// State says what a [Result] is: a view, or the reason there is none.
type State string

// The states of a result.
const (
	// StateOK: the daemon answered and the result holds a view.
	StateOK State = "ok"
	// StateNoDaemon: there is no socket in the folder, or nothing listens on
	// the one that is there.
	StateNoDaemon State = "no-daemon"
	// StateSlow: the socket did not answer before the deadline.
	StateSlow State = "slow"
	// StateOtherProtocol: the peer says it does not speak this protocol, or
	// does not offer the two operations.
	StateOtherProtocol State = "other-protocol"
	// StateRefused: the daemon answered "unauthorized": it serves another
	// user.
	StateRefused State = "refused"
	// StateBadAnswer: an answer over the size limit, or one that is not a
	// valid answer, or a connection closed in place of an answer.
	// A daemon of another version can be the cause: one that writes another
	// protocol's name on its answers is seen here, not as
	// StateOtherProtocol, so the next step names the version too.
	StateBadAnswer State = "bad-answer"
	// StateUnavailable: everything else. The socket could not be opened for a
	// reason other than its absence, the daemon was at its connection limit
	// or could not serve the request, or the caller cancelled.
	StateUnavailable State = "unavailable"
)

// stateTexts holds the fixed text and next step of every state without a
// view. They name the daemon's commands as docs/lead-daemon.md does.
var stateTexts = map[State][2]string{
	StateNoDaemon:      {"no daemon answers at the socket", "start the daemon with: ataniqd run"},
	StateSlow:          {"the daemon did not answer in time", "check the daemon with: ataniqd status, then try again"},
	StateOtherProtocol: {"the daemon speaks another protocol than this console", "use a console and a daemon of the same version"},
	StateRefused:       {"the daemon refused this user", "run the console as the user that runs the daemon"},
	StateBadAnswer:     {"the daemon's answer was not valid or was over the size limit", "check that the console and daemon are the same version, then: ataniqd status"},
	StateUnavailable:   {"the daemon could not be asked", "check the socket's folder and the daemon with: ataniqd status"},
}

// Result is what [Read] returns: a view when State is [StateOK], and
// otherwise a state with its text and next step.
type Result struct {
	// State tells which of the two the result is.
	State State
	// Text is one line saying what is the matter, written by this package.
	// It is empty with StateOK.
	Text string
	// NextStep is one line saying what to do about it, written by this
	// package. It is empty with StateOK.
	NextStep string
	// View is the daemon's view. It is the zero value unless State is
	// StateOK.
	View View
}

// View is the daemon's answer as the console shows it. Every string in it is
// cleaned and bounded.
type View struct {
	// OwnerChannel tells whether the daemon's hello lists every operation of
	// the owner channel: the console can then show and decide approvals
	// through it. It is false for a daemon that lists none or only some.
	OwnerChannel bool
	// Daemon is the daemon itself.
	Daemon Daemon
	// HasSupervisor tells whether the daemon listed a supervisor.
	HasSupervisor bool
	// Supervisor is the supervisor's entry; meaningful only when
	// HasSupervisor is true.
	Supervisor Entry
	// Projects are the projects, in the order the daemon first named each.
	// Two projects whose names differ only in what cleaning drops are two
	// projects here, with the same Name.
	Projects []Project
	// LeftOut counts the entries of the daemon's answer that are not in the
	// view: those over a bound, those of a kind this package does not know,
	// a lead or coordinator without a project, and a second supervisor or a
	// second lead of one project.
	LeftOut int
}

// Daemon is the daemon block of a view.
type Daemon struct {
	// Version is the daemon's version string.
	Version string
	// PID is the daemon's process ID, or 0 when it sent none that can be one.
	PID int
	// StartedAt is when the daemon started.
	StartedAt time.Time
	// ReconcileCompleted tells whether the daemon's last reconcile ran to its
	// end.
	ReconcileCompleted bool
}

// Project is one project of a view: its lead and its coordinators.
type Project struct {
	// Name is the project's name.
	Name string
	// HasLead tells whether the daemon listed a lead for the project. A
	// project can be here for its coordinators alone.
	HasLead bool
	// Lead is the lead's entry; meaningful only when HasLead is true.
	Lead Entry
	// Coordinators are the project's coordinators, in the daemon's order.
	Coordinators []Entry
}

// Kind is the kind of an entry.
type Kind string

// The kinds of an entry. Their texts are the client protocol's.
const (
	// KindLead is a project's lead.
	KindLead Kind = "lead"
	// KindSupervisor is the supervisor.
	KindSupervisor Kind = "supervisor"
	// KindCoordinator is a feature's coordinator. It is a record, not a lead.
	KindCoordinator Kind = "coordinator"
)

// EntryState is the shown state of an entry. Its values are the client
// protocol's shown states; a state this package does not know is shown as
// [EntryUnknown].
type EntryState string

// The shown states of an entry.
const (
	// EntryDesired: wanted, and nothing launched yet.
	EntryDesired EntryState = "desired"
	// EntryStarting: a launch is under way.
	EntryStarting EntryState = "starting"
	// EntryReady: the entry is up.
	EntryReady EntryState = "ready"
	// EntryDegraded: the entry is up but not fully well.
	EntryDegraded EntryState = "degraded"
	// EntryStalled: a coordinator that is not making progress.
	EntryStalled EntryState = "stalled"
	// EntryStopping: a stop is under way.
	EntryStopping EntryState = "stopping"
	// EntryStopped: proven stopped.
	EntryStopped EntryState = "stopped"
	// EntryEnded: ended, proven.
	EntryEnded EntryState = "ended"
	// EntryUnknown: the state is not proven, or is not one of the above.
	EntryUnknown EntryState = "unknown"
)

// Entry is one lead, supervisor or coordinator, with the few fields a cockpit
// shows.
type Entry struct {
	// Kind is the kind of the entry.
	Kind Kind
	// Name is the entry's role string, such as "<project>/lead".
	Name string
	// Project is the entry's project; empty for the supervisor.
	Project string
	// Feature is a coordinator's feature; empty otherwise.
	Feature string
	// Parent is the role string of a coordinator's parent lead; empty
	// otherwise.
	Parent string
	// State is the shown state.
	State EntryState
	// Desired is the desired state, as the daemon words it.
	Desired string
	// Reason is the machine-readable reason of the state.
	Reason string
	// Detail is the reason's detail text.
	Detail string
	// Since is when the entry entered the state.
	Since time.Time
	// Epoch is the entry's epoch; for a coordinator, its instance number.
	Epoch uint64
	// Manager is the manager of the entry's unit.
	Manager string
	// Provider, Model and Effort are the requested profile: what the entry
	// was asked to run with, not what was observed.
	Provider, Model, Effort string
}
