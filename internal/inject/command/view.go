package command

import (
	"sort"
	"time"
)

// CommandView is a read-only copy of one command.
type CommandView struct {
	ID     string
	Kind   string
	Sender string
	State  State
	// Reason and Detail are the last reason the command was given, for
	// example the readings of the last guard that found the lead not idle.
	Reason Reason
	Detail string
	// EvidenceKind and Evidence are the last evidence recorded.
	EvidenceKind EvidenceKind
	Evidence     string
	Operator     string
	// Late are the late fence results and late delivery evidence of a command
	// that was uncertain, or that an operator resolved as "send again", in the
	// order they were journaled. They are for the operator who decides; they
	// never end the uncertain state. They stay with the command after it ends.
	Late []LateNote
	// Admitted is when the command was admitted; Updated when it last moved.
	// Submitted is when Enter was written, and the zero time before that.
	Admitted, Updated, Submitted time.Time
	// Of is the id of the command this one replaces; Replacement the id of
	// the command that replaced this one.
	Of          string
	Replacement string
}

// LateNote is a late fence result or late delivery evidence, kept on a command
// that was already uncertain, or already resolved as sent again, when it
// arrived.
type LateNote struct {
	// Seq and At are the sequence number and the time of the record.
	Seq uint64
	At  time.Time
	// Reason is ReasonLateFenceResult or ReasonLateEvidence.
	Reason Reason
	// Detail is, for a late fence result, its outcome and readings.
	Detail string
	// EvidenceKind and Evidence are the observation of what the fence wrote
	// (a fence result), or the provider's report (delivery evidence).
	EvidenceKind EvidenceKind
	Evidence     string
}

// TargetView is a read-only copy of one target's queue.
type TargetView struct {
	Target string
	// Commands are the commands still in the queue, head first: accepted,
	// queued, dispatching and uncertain ones.
	Commands []CommandView
	// QueuedBytes is the bytes of text the queue holds.
	QueuedBytes int
	// Frozen is true when an uncertain command holds the queue, and FrozenBy
	// names it.
	Frozen   bool
	FrozenBy string
	// BlockedNotified is true during a blocked spell: the owner was already
	// told that the lead is waiting for an approval.
	BlockedNotified bool
}

// View is a read-only copy of the queues, for the daemon to show.
type View struct {
	// Targets lists the targets that have commands in their queue, by name.
	Targets []TargetView
}

// View returns a copy of the queues. Ended commands are not in it; Command
// finds them.
func (m *Machine) View() View {
	var v View
	for _, key := range m.targetKeys() {
		q := m.queues[key]
		tv := TargetView{Target: key, BlockedNotified: m.blocked[key]}
		for _, c := range q {
			tv.Commands = append(tv.Commands, c.view())
			tv.QueuedBytes += len(c.text)
		}
		if f := frozenBy(q); f != nil {
			tv.Frozen, tv.FrozenBy = true, f.id
		}
		v.Targets = append(v.Targets, tv)
	}
	return v
}

// Command returns a copy of any command the machine knows, ended or not.
func (m *Machine) Command(id string) (CommandView, bool) {
	c := m.cmds[id]
	if c == nil {
		return CommandView{}, false
	}
	return c.view(), true
}

func (c *command) view() CommandView {
	v := CommandView{
		ID: c.id, Kind: c.kind, Sender: c.sender.String(), State: c.state,
		Reason: c.reason, Detail: c.detail,
		EvidenceKind: c.evidenceKind, Evidence: c.evidence, Operator: c.operator,
		Admitted: c.admitted, Updated: c.updated, Of: c.of, Replacement: c.replacement,
	}
	if c.hasEnter {
		v.Submitted = c.enterAt
	}
	v.Late = append([]LateNote(nil), c.late...)
	return v
}

// targetKeys returns the rendered names of the targets that have a queue,
// sorted.
func (m *Machine) targetKeys() []string {
	keys := make([]string, 0, len(m.queues))
	for k := range m.queues {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
