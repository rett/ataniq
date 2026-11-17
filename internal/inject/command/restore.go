package command

import (
	"errors"
	"fmt"
	"time"
)

// Restore rebuilds a Machine from the records of its journal, in order, and
// writes any further records to journal. It refuses a journal that does not
// follow the state machine with a *RestoreError carrying the sequence number of
// the record that does not: a sequence that does not start at 1 and rise by
// one, a transition the machine does not have, a sender that is not parsable
// (so "owner" is refused), a text that does not match its header or breaks the
// body rules, an id used twice, a dispatch that is not for the head of a queue.
//
// After the last record it makes two kinds of completion, each as a new record
// stamped now. A command left in dispatching, with or without Enter recorded,
// becomes uncertain (holder or agent died): it is never sent again by itself.
// A command left accepted, because its queued record was never written, is
// queued. The rate windows are rebuilt from the times of the accepted records.
//
// Holds are rebuilt from their records, with the hold counter and the last
// pieces of late information, and Restore never writes or ends one: only an
// operator's release does. A hold or release record, or a withdrawal, that the
// live machine could not have written is refused: a hold about an unknown
// command or about a command in a state that holds no queue; a hold whose
// counter is not the last one plus one, whose digest is not valid, that repeats
// the last piece of a queue that is held or that has no bounded, safe rendering
// of the call (a hold for a command the queue is already held for is not
// refused: the live machine writes one for every new piece); a release of a
// queue that is not held, with no operator or with a counter that is not the
// current one; a withdrawal from a queue that is not held, a dispatch while
// the queue is held, and a note for a decided command with no hold behind it.
//
// The text cap of limits applies to every journaled text. A journal that holds
// a text over the configured MaxTextBytes is refused as a whole, with a
// *RestoreError naming the record: Restore fails closed and does not truncate
// or skip it. So the cap must not be lowered under a live journal; to lower it,
// start a new journal.
//
// now is a parameter, not a clock, for the completion records and for the
// times they carry. If appending a completion record fails, Restore returns
// the *JournalError and no machine.
func Restore(limits Limits, journal Journal, records []Record, now time.Time) (*Machine, error) {
	m, err := New(limits, journal)
	if err != nil {
		return nil, err
	}
	for i, r := range records {
		if want := uint64(i + 1); r.Seq != want {
			return nil, &RestoreError{Seq: r.Seq, Err: fmt.Errorf("sequence number %d, expected %d", r.Seq, want)}
		}
		if err := m.check(r); err != nil {
			return nil, &RestoreError{Seq: r.Seq, Err: err}
		}
		m.apply(r)
		m.seq = r.Seq
	}
	for _, key := range m.targetKeys() {
		for _, c := range append([]*command(nil), m.queues[key]...) {
			var r Record
			switch c.state {
			case StateAccepted:
				r = queuedRecord(c, now)
			case StateDispatching:
				r = base(c, now)
				r.From, r.To, r.Reason = StateDispatching, StateUncertain, ReasonHolderDied
				r.EvidenceKind, r.Evidence = EvidenceObservation, string(WrittenUnknown)
				r.Detail = "found dispatching at restore; Enter recorded: " + yesNo(c.hasEnter)
			default:
				continue
			}
			if err := m.commit(r); err != nil {
				var je *JournalError
				if errors.As(err, &je) {
					return nil, je
				}
				return nil, err
			}
		}
	}
	return m, nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
