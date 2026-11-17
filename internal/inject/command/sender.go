package command

import (
	"errors"
	"fmt"
	"strings"
)

// ErrOwnerSender is returned by ParseSender for "owner". The owner speaks as
// the writer of the terminal, never through this channel, and no constructor
// yields an owner sender.
var ErrOwnerSender = errors.New("command: the owner is not a sender on this channel")

// ErrBadSender is returned by ParseSender and ParseTarget for text that is not
// a sender or a target.
var ErrBadSender = errors.New("command: not a valid sender or target")

type principalKind uint8

const (
	principalNone principalKind = iota
	principalDaemon
	principalSupervisor
	principalLead
)

const leadPrefix = "lead:"

// Sender is who a message is from. It is a typed value, not a string from the
// caller: the only constructors are Daemon, Supervisor and Lead, so a sender
// can never be the owner. The zero Sender is invalid and is refused at
// admission. Sender values are comparable.
type Sender struct {
	kind    principalKind
	project string
}

// Daemon is the sender of Ataniq's own messages, rendered "ataniq".
func Daemon() Sender { return Sender{kind: principalDaemon} }

// Supervisor is the sender of the supervisor's messages, rendered
// "supervisor".
func Supervisor() Sender { return Sender{kind: principalSupervisor} }

// Lead is the sender of a project lead's messages, rendered "lead:<project>".
// The project name is checked at admission, not here.
func Lead(project string) Sender { return Sender{kind: principalLead, project: project} }

// String renders the sender as it appears in the header and the journal. The
// zero Sender renders as the empty string.
func (s Sender) String() string {
	switch s.kind {
	case principalDaemon:
		return "ataniq"
	case principalSupervisor:
		return "supervisor"
	case principalLead:
		return leadPrefix + s.project
	}
	return ""
}

// Project is the project of a lead sender, and empty for the others.
func (s Sender) Project() string { return s.project }

// Valid reports whether the sender is one of the three kinds and, for a lead,
// has a project name that matches the grammar.
func (s Sender) Valid() bool {
	switch s.kind {
	case principalDaemon, principalSupervisor:
		return true
	case principalLead:
		return ValidName(s.project)
	}
	return false
}

// ParseSender restores a sender from its rendered form, as written to the
// journal. It accepts exactly "ataniq", "supervisor" and "lead:<project>" with
// a valid project name. It refuses "owner" with ErrOwnerSender and anything
// else with ErrBadSender.
func ParseSender(s string) (Sender, error) {
	switch {
	case s == "ataniq":
		return Daemon(), nil
	case s == "supervisor":
		return Supervisor(), nil
	case s == "owner":
		return Sender{}, ErrOwnerSender
	case strings.HasPrefix(s, leadPrefix):
		project := s[len(leadPrefix):]
		if ValidName(project) {
			return Lead(project), nil
		}
	}
	return Sender{}, fmt.Errorf("%w: %q", ErrBadSender, clipForError(s))
}

// Target is who a message is for: the supervisor or a project lead. The zero
// Target is invalid. Target values are comparable.
type Target struct {
	kind    principalKind
	project string
}

// SupervisorTarget is the supervisor, rendered "supervisor".
func SupervisorTarget() Target { return Target{kind: principalSupervisor} }

// LeadTarget is a project lead, rendered "lead:<project>". The project name is
// checked at admission, not here.
func LeadTarget(project string) Target { return Target{kind: principalLead, project: project} }

// String renders the target as it appears in the journal. The zero Target
// renders as the empty string.
func (t Target) String() string {
	switch t.kind {
	case principalSupervisor:
		return "supervisor"
	case principalLead:
		return leadPrefix + t.project
	}
	return ""
}

// Project is the project of a lead target, and empty for the supervisor.
func (t Target) Project() string { return t.project }

// Valid reports whether the target is the supervisor or a lead with a project
// name that matches the grammar.
func (t Target) Valid() bool {
	switch t.kind {
	case principalSupervisor:
		return true
	case principalLead:
		return ValidName(t.project)
	}
	return false
}

// ParseTarget restores a target from its rendered form.
func ParseTarget(s string) (Target, error) {
	switch {
	case s == "supervisor":
		return SupervisorTarget(), nil
	case strings.HasPrefix(s, leadPrefix):
		project := s[len(leadPrefix):]
		if ValidName(project) {
			return LeadTarget(project), nil
		}
	}
	return Target{}, fmt.Errorf("%w: %q", ErrBadSender, clipForError(s))
}

// EdgeAllowed is the sender policy. The daemon may send to any target; the
// supervisor may send to a project lead; a project lead may send to the
// supervisor. Everything else is refused: lead to lead (a lead to itself
// included), supervisor to supervisor, and any zero value. It does not check
// project names; admission does.
func EdgeAllowed(from Sender, to Target) bool {
	switch from.kind {
	case principalDaemon:
		return to.kind == principalSupervisor || to.kind == principalLead
	case principalSupervisor:
		return to.kind == principalLead
	case principalLead:
		return to.kind == principalSupervisor
	}
	return false
}

// clipForError shortens text that may come from a hostile source before it is
// put in an error message.
func clipForError(s string) string { return safeField(s, 64) }
