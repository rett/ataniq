package assignment

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

// walk tracks two routes explicitly, independently of the ledger's indexes.
// Admission and holder are separate so that a committed-closed interval and
// an abort from it cannot accidentally count as permission to start.
type walk struct {
	phase                               Phase
	revision, generation, phaseRevision uint64
	holder, candidate                   Binding
	id                                  string
	startedOpen, admitted               bool
	worker, qargi                       bool
	disposition                         string
}

func (m walk) choices() []Kind {
	switch m.phase {
	case "":
		return []Kind{Seed}
	case PhaseConfirmed, PhaseAborted:
		if m.generation > 1 {
			return []Kind{Freeze, RollbackFreeze}
		}
		return []Kind{Freeze}
	case PhaseAbortedClosed:
		return []Kind{Freeze}
	case PhaseFrozen:
		return []Kind{Inventory, Inventory, AbortPrepared}
	case PhaseInventoried:
		return []Kind{BeliefAccepted, BeliefAccepted, Inventory, AbortPrepared}
	case PhaseBeliefVerified:
		return []Kind{Reserve, Reserve, Inventory}
	case PhaseReserved, PhasePartiallyFenced:
		if m.worker && m.qargi {
			return []Kind{Fenced, Fenced, Inventory, AbortPrepared}
		}
		return []Kind{FenceReceipt, FenceReceipt, FenceReceipt, AbortPrepared}
	case PhaseFenced:
		return []Kind{Commit, Commit, Commit, Inventory, AbortPrepared}
	case PhaseCommittedClosed:
		if m.worker && m.qargi {
			return []Kind{Open}
		}
		if m.generation > 1 {
			return []Kind{ConfirmReceipt, ConfirmReceipt, ConfirmReceipt, RollbackFreeze}
		}
		return []Kind{ConfirmReceipt}
	case PhaseAborting:
		if m.startedOpen {
			return []Kind{Aborted, AbortedClosed}
		}
		return []Kind{AbortedClosed}
	default:
		panic("unknown model phase")
	}
}

func (m *walk) advance(k Kind, route string) {
	before := m.phase
	m.revision++
	switch k {
	case Seed:
		m.generation, m.holder, m.phase, m.disposition = 1, legacy, PhaseCommittedClosed, "pending"
	case Freeze, RollbackFreeze:
		m.startedOpen, m.admitted = m.admitted, false
		m.candidate = managed
		if m.holder == managed {
			m.candidate = legacy
		}
		m.phase, m.disposition = PhaseFrozen, "pending"
	case Inventory:
		m.phase = PhaseInventoried
	case BeliefAccepted:
		m.phase = PhaseBeliefVerified
	case Reserve:
		m.phase, m.worker, m.qargi = PhaseReserved, false, false
	case FenceReceipt, ConfirmReceipt:
		if route == "worker" {
			m.worker = true
		} else {
			m.qargi = true
		}
		if k == FenceReceipt {
			m.phase = PhasePartiallyFenced
		}
	case Fenced:
		m.phase = PhaseFenced
	case Commit:
		m.generation++
		m.phase, m.holder, m.disposition = PhaseCommittedClosed, m.candidate, "committed"
		m.worker, m.qargi = false, false
	case Open:
		m.phase, m.admitted, m.disposition = PhaseConfirmed, true, "confirmed"
	case AbortPrepared:
		m.phase = PhaseAborting
	case Aborted:
		m.phase, m.admitted, m.disposition = PhaseAborted, true, "aborted"
	case AbortedClosed:
		m.phase, m.disposition = PhaseAbortedClosed, "aborted-closed"
	default:
		panic("unknown model command")
	}
	if before != m.phase {
		m.phaseRevision = m.revision
	}
}

func TestAssignmentWalk(t *testing.T) {
	for _, seed := range []int64{1, 11, 27, 51, 83, 107, 193, 229} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			s := fresh(t)
			var m walk
			history := make(map[[2]string]Result)
			latest := make(map[string]Result)
			for n := range 100 {
				options := m.choices()
				k := options[rng.Intn(len(options))]
				if k == Seed || k == Freeze || k == RollbackFreeze {
					m.id = fmt.Sprintf("transfer-%d", n)
				}
				route := "worker"
				if m.worker || (!m.qargi && rng.Intn(2) == 0) {
					route = "qargi"
				}
				c := next(s.ledger, key, k, m.id, route)
				// Inject a stale CAS or a conflicting retry before the valid step.
				if len(s.events) > 0 && rng.Intn(2) == 0 {
					old := s.events[rng.Intn(len(s.events))]
					e, r, err := Decide(s.ledger, old.Command)
					if e != nil || r != old.Result || err != nil {
						t.Fatal(n, r, err)
					}
					wrong := old.Command
					wrong.Authorization += "!"
					refused(t, s.ledger, wrong, ErrConflict)
				} else {
					bad := c
					bad.Expected.Revision++
					refused(t, s.ledger, bad, ErrStale)
				}
				prev := s.ledger
				e := s.accept(t, c)
				m.advance(k, route)
				r, _ := s.ledger.Row(key)
				tr, _ := s.ledger.Transfer(key, m.id)
				if r.Head.Revision != m.revision || r.Head.Generation != m.generation ||
					r.Head.Phase != m.phase || tr.PhaseRevision != m.phaseRevision || tr.Phase != m.phase ||
					r.Holder != m.holder || r.Head.BindingID != m.holder.ID || e.Result.Disposition != m.disposition {
					t.Fatalf("step %d %s: model %+v, row %+v, transfer %+v", n, k, m, r, tr)
				}
				for _, b := range []Binding{legacy, managed} {
					h := r.Head
					h.BindingID = b.ID
					if r.Permits(h) != (m.admitted && b == m.holder) {
						t.Fatalf("step %d admitted %s", n, b.ID)
					}
				}
				if prev.Checkpoint().Digest != e.PreviousHash {
					t.Fatal("prior ledger mutated")
				}
				active := m.id
				if m.admitted || m.phase == PhaseAbortedClosed {
					active = ""
				}
				expected := Head{Key: key, Generation: m.generation, Revision: m.revision, BindingID: m.holder.ID, TransferID: active, Phase: m.phase}
				want := Result{Head: expected, RequestID: c.RequestID, Disposition: m.disposition, EventID: e.Result.EventID}
				history[[2]string{m.id, c.RequestID}], latest[m.id] = want, want
				replayed, err := Replay(header, s.events, testKey{})
				if err != nil || !reflect.DeepEqual(s.ledger, replayed) {
					t.Fatalf("prefix %d: %v", n, err)
				}
				for op, want := range history {
					if got, ok := Lookup(replayed, key, op[0], op[1]); !ok || got != want {
						t.Fatal(n, op, got, want)
					}
				}
				for id, want := range latest {
					if got, ok := Lookup(replayed, key, id, ""); !ok || got != want {
						t.Fatal(n, id, got, want)
					}
				}
			}
		})
	}
}
