package assignment

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

func forward(t *testing.T, n int) trace {
	t.Helper()
	s := seeded(t)
	s.steps(t, "T", forwardSteps[:n])
	return s
}

// recoverAt models the adapter's decision after framing and signature checks.
// A missing or incompatible independent head yields no usable ledger.
func recoverAt(events []Event, pin Checkpoint) (Ledger, error) {
	l, err := Replay(header, events, testKey{})
	if err != nil {
		return Ledger{}, err
	}
	if err := VerifyCheckpoint(l.Checkpoint(), pin); err != nil {
		return Ledger{}, err
	}
	return l, nil
}

func assertHolder(t *testing.T, l Ledger, holder Binding, g uint64, open bool) {
	t.Helper()
	r, ok := l.Row(key)
	if !ok || r.Holder != holder || r.Head.BindingID != holder.ID || r.Head.Generation != g || r.Permits(r.Head) != open {
		t.Fatalf("holder after recovery: %+v, want %s at %d, open=%v", r, holder.ID, g, open)
	}
}

func TestCrashAndPartitionTable(t *testing.T) {
	cases := []struct {
		name  string
		check func(*testing.T)
	}{
		{"Before fresh header sync, after header sync before enrollment CAS, after enrollment CAS before reply", func(t *testing.T) {
			if _, err := NewLedger(Header{}); err == nil {
				t.Fatal("incomplete header")
			}
			s := fresh(t)
			if _, err := recoverAt(nil, Checkpoint{}); err == nil {
				t.Fatal("unacknowledged header")
			}
			l, err := recoverAt(nil, s.ledger.Checkpoint())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := l.Row(key); ok {
				t.Fatal("enrollment assigned a holder")
			}
			s.steps(t, "seed", seedSteps[:1])
			assertHolder(t, s.ledger, legacy, 1, false)
		}},
		{"Before append for any table row including receipt, abort, hold or rollback", func(t *testing.T) {
			for _, end := range []Kind{HoldReviewed, RollbackFreeze, Aborted, AbortedClosed} {
				s := seeded(t)
				switch end {
				case HoldReviewed:
					s.steps(t, "T", appendSteps(forwardSteps, step{kind: HoldStarted}, step{kind: HoldReviewed}))
				case RollbackFreeze:
					s.steps(t, "T", forwardSteps[:8])
					s.steps(t, "U", []step{{kind: RollbackFreeze}})
				default:
					s.steps(t, "T", appendSteps(forwardSteps[:4], step{kind: AbortPrepared}, step{kind: end}))
				}
				for i, e := range s.events {
					before, err := Replay(header, s.events[:i], testKey{})
					if err != nil {
						t.Fatal(err)
					}
					proposed, _, err := Decide(before, e.Command)
					if proposed == nil || err != nil {
						t.Fatal(err)
					}
					if _, ok := Lookup(before, key, e.Command.TransferID, e.Command.RequestID); ok {
						t.Fatal("staged result visible")
					}
					r, ok := before.Row(key)
					if ok && r.Head != e.Command.Expected {
						t.Fatal("wrong predecessor")
					}
					if before.Checkpoint().Digest != e.PreviousHash {
						t.Fatal("decision advanced checkpoint")
					}
				}
			}
		}},
		{"During append or sync, or after file sync before witness CAS", func(t *testing.T) {
			s := forward(t, 7)
			pin := s.ledger.Checkpoint()
			s.steps(t, "T", forwardSteps[7:8])
			if _, err := recoverAt(s.events, pin); !errors.Is(err, ErrCheckpoint) {
				t.Fatal(err)
			}
			l, err := recoverAt(s.events[:len(s.events)-1], pin)
			if err != nil {
				t.Fatal(err)
			}
			assertHolder(t, l, legacy, 1, false)
		}},
		{"During witness CAS, or after CAS before publication, reply or caller save", func(t *testing.T) {
			s := forward(t, 8)
			last := s.events[len(s.events)-1]
			pin := s.ledger.Checkpoint()
			l, err := recoverAt(s.events, pin)
			if err != nil {
				t.Fatal(err)
			}
			assertHolder(t, l, managed, 2, false)
			if got, ok := Lookup(l, key, "T", last.Command.RequestID); !ok || got != last.Result {
				t.Fatal(got)
			}
			if _, err := recoverAt(s.events[:len(s.events)-1], pin); !errors.Is(err, ErrCheckpoint) {
				t.Fatal(err)
			}
		}},
		{"Before or after writing inventory, belief, decision or other evidence", func(t *testing.T) {
			s := forward(t, 1)
			for _, step := range forwardSteps[1:4] {
				c := next(s.ledger, key, step.kind, "T", "")
				missing := c
				missing.Evidence = nil
				refused(t, s.ledger, missing, ErrEvidence)
				before, _ := s.ledger.Row(key)
				if before.Permits(before.Head) {
					t.Fatal("evidence itself opened admission")
				}
				bad := c
				bad.Evidence = slices.Clone(c.Evidence)
				bad.Evidence[0].Digest = ""
				refused(t, s.ledger, bad, ErrEvidence)
				s.accept(t, c)
			}
		}},
		{"Before or after route intent, actual effect start, or outcome write", func(t *testing.T) {
			s := forward(t, 4)
			c := next(s.ledger, key, FenceReceipt, "T", "worker")
			unsettled := c
			unsettled.Evidence = c.Evidence[:1]
			refused(t, s.ledger, unsettled, ErrEvidence)
			wrong := c
			wrong.Evidence = slices.Clone(c.Evidence)
			wrong.Evidence[1].Route = "qargi"
			refused(t, s.ledger, wrong, ErrEvidence)
			assertHolder(t, s.ledger, legacy, 1, false)
			s.accept(t, c)
			tr, _ := s.ledger.Transfer(key, "T")
			if len(tr.Outstanding) != 0 {
				t.Fatal("receipt did not settle the child")
			}
		}},
		{"Before or after sealing a route, writing its receipt, or recording the receipt at home", func(t *testing.T) {
			s := forward(t, 5)
			refused(t, s.ledger, next(s.ledger, key, Fenced, "T", ""), ErrEvidence)
			refused(t, s.ledger, next(s.ledger, key, FenceReceipt, "T", "worker"), ErrEvidence)
			s.steps(t, "T", forwardSteps[5:7])
			assertHolder(t, s.ledger, legacy, 1, false)
		}},
		{"Before or after Qargi applies the signed event and records its receipt", func(t *testing.T) {
			s := forward(t, 8)
			c := next(s.ledger, key, ConfirmReceipt, "T", "qargi")
			refused(t, s.ledger, next(s.ledger, key, Open, "T", ""), ErrEvidence)
			e := s.accept(t, c)
			recovered, err := recoverAt(s.events, s.ledger.Checkpoint())
			if err != nil {
				t.Fatal(err)
			}
			got, ok := Lookup(recovered, key, "T", c.RequestID)
			if !ok || got != e.Result {
				t.Fatal("missing receipt disposition")
			}
			assertHolder(t, recovered, managed, 2, false)
		}},
		{"Before or after opening locally on either Open or Aborted", func(t *testing.T) {
			for _, abort := range []bool{false, true} {
				s := forward(t, 2)
				if abort {
					s.steps(t, "T", []step{{kind: AbortPrepared}, {kind: Aborted}})
				} else {
					s.steps(t, "T", forwardSteps[2:])
				}
				opened := s.events[len(s.events)-1].Result.Head
				r, _ := s.ledger.Row(key)
				if !r.Permits(opened) {
					t.Fatal("missing fresh admission")
				}
				s.accept(t, next(s.ledger, key, Freeze, "U", ""))
				r, _ = s.ledger.Row(key)
				if r.Permits(opened) {
					t.Fatal("delayed local open")
				}
			}
		}},
		{"Lost begin, commit, abort, confirmation or rollback reply, duplicate or reordered request", func(t *testing.T) {
			s := forward(t, 8)
			s.steps(t, "U", []step{{kind: RollbackFreeze}, {kind: AbortPrepared}, {kind: AbortedClosed}})
			for _, e := range s.events {
				dup, res, err := Decide(s.ledger, e.Command)
				if err != nil || dup != nil || res != e.Result {
					t.Fatal(e.Command.Kind, res, err)
				}
				bad := e.Command
				bad.Expected.Generation++
				refused(t, s.ledger, bad, ErrConflict)
			}
			got, _ := Lookup(s.ledger, key, "T", "")
			if got.Disposition != "committed" {
				t.Fatal(got)
			}
			assertHolder(t, s.ledger, managed, 2, false)
		}},
		{"Stale restored Qargi projection, gap, old incarnation, or two signed events claiming the same row revision", func(t *testing.T) {
			s := seeded(t)
			a, b := trace{ledger: s.ledger}, trace{ledger: s.ledger}
			a.steps(t, "A", forwardSteps[:2])
			b.steps(t, "B", forwardSteps[:1])
			if _, err := Apply(s.ledger, a.events[1]); !errors.Is(err, ErrChain) {
				t.Fatal(err)
			}
			if _, err := Apply(b.ledger, a.events[1]); !errors.Is(err, ErrChain) {
				t.Fatal(err)
			}
			pin := a.ledger.Checkpoint()
			pin.Incarnation = "old"
			if VerifyCheckpoint(a.ledger.Checkpoint(), pin) == nil {
				t.Fatal("old incarnation")
			}
			root := header
			root.Incarnation = "old"
			if _, err := Replay(root, s.events, testKey{}); !errors.Is(err, ErrChain) {
				t.Fatal(err)
			}
		}},
		{"Home, witness or potentially executing peer unreachable", func(t *testing.T) {
			s := forward(t, 5)
			if _, err := recoverAt(s.events, Checkpoint{}); !errors.Is(err, ErrCheckpoint) {
				t.Fatal(err)
			}
			refused(t, s.ledger, next(s.ledger, key, Fenced, "T", ""), ErrEvidence)
			refused(t, s.ledger, next(s.ledger, key, Commit, "T", ""), ErrPhase)
			assertHolder(t, s.ledger, legacy, 1, false)
		}},
		{"Qargi unreachable before freeze acknowledgement, during release, after commit or after open", func(t *testing.T) {
			s := forward(t, 1)
			c := next(s.ledger, key, Inventory, "T", "")
			c.Evidence = slices.DeleteFunc(c.Evidence, func(e Evidence) bool { return e.Subject == "qargi" })
			refused(t, s.ledger, c, ErrEvidence)
			s.steps(t, "T", forwardSteps[1:5])
			refused(t, s.ledger, next(s.ledger, key, Fenced, "T", ""), ErrEvidence)
			s.steps(t, "T", forwardSteps[5:8])
			s.steps(t, "T", forwardSteps[9:10])
			refused(t, s.ledger, next(s.ledger, key, Open, "T", ""), ErrEvidence)
			s.steps(t, "T", forwardSteps[8:9])
			s.steps(t, "T", forwardSteps[10:])
			if _, err := recoverAt(s.events, Checkpoint{}); err == nil {
				t.Fatal("unverified current authority")
			}
			assertHolder(t, s.ledger, managed, 2, true)
		}},
		{"Both sides claim ownership, a delayed legacy restart appears, or evidence shows two actual routes", func(t *testing.T) {
			s := seeded(t)
			old := s.events[len(s.events)-1].Result.Head
			s.steps(t, "T", forwardSteps)
			r, _ := s.ledger.Row(key)
			if r.Permits(old) {
				t.Fatal("old head admitted")
			}
			for _, bad := range []Head{old, r.Head} {
				bad.BindingID = legacy.ID
				if r.Permits(bad) {
					t.Fatal("legacy claim")
				}
			}
			s.steps(t, "U", []step{{kind: RollbackFreeze}})
			assertHolder(t, s.ledger, managed, 2, false)
		}},
		{"Crash during the 14-day hold or reverse transfer", func(t *testing.T) {
			s := forward(t, len(forwardSteps))
			s.steps(t, "T", []step{{kind: HoldStarted}})
			prior, _ := s.ledger.Transfer(key, "T")
			s.steps(t, "U", appendSteps([]step{{kind: RollbackFreeze}}, forwardSteps[1:8]...))
			l, err := recoverAt(s.events, s.ledger.Checkpoint())
			if err != nil {
				t.Fatal(err)
			}
			assertHolder(t, l, legacy, 3, false)
			after, _ := l.Transfer(key, "T")
			if prior.Hold.Preservation == (Evidence{}) || !reflect.DeepEqual(prior, after) {
				t.Fatal("hold history lost")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.check)
	}
}
