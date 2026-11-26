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
		{"Before fresh header sync / after header sync but before enrollment CAS / after enrollment CAS before reply", func(t *testing.T) {
			raw, err := header.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewLedgerFromHeader(raw[:len(raw)/2]); err == nil {
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
			other := header
			other.LedgerID = "orphan"
			orphan, err := NewLedger(other)
			if err != nil {
				t.Fatal(err)
			}
			if VerifyCheckpoint(orphan.Checkpoint(), s.ledger.Checkpoint()) == nil {
				t.Fatal("adopted orphan")
			}
			if _, ok := l.Row(key); ok {
				t.Fatal("enrollment assigned a holder")
			}
			s.steps(t, "seed", seedSteps[:1])
			assertHolder(t, s.ledger, legacy, 1, false)
		}},
		{"Before append for any table row, including receipt, abort, hold or rollback", func(t *testing.T) {
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
					if e.Command.Kind == Freeze {
						row, _ := before.Row(key)
						gate := startGate{projection: row.PublicDigest, qargiAvailable: true}
						if _, ok := Lookup(before, key, e.Command.TransferID, ""); ok || gate.permits(before, row.Head) {
							t.Fatal("absent begin reopened locally closed route")
						}
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
			raw, err := s.events[len(s.events)-1].RecordBytes()
			if err != nil {
				t.Fatal(err)
			}
			for _, cut := range []int{1, len(raw) / 2, len(raw) - 1} {
				if _, err := DecodeRecord(raw[:cut]); err == nil {
					t.Fatal("accepted torn frame", cut)
				}
			}
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
			old, err := Replay(header, s.events[:len(s.events)-1], testKey{})
			if err != nil {
				t.Fatal(err)
			}
			if _, sent := recoverPublication(s.events, old.Checkpoint(), true); sent {
				t.Fatal("broadcast staged commit")
			}
			if _, sent := recoverPublication(s.events, pin, false); sent {
				t.Fatal("published after session loss")
			}
			if published, ok := recoverPublication(s.events, pin, true); !ok || published.Result != last.Result {
				t.Fatal("lost acknowledged publication")
			}
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
		{"Before/after writing an inventory, belief, decision or other evidence object", func(t *testing.T) {
			s := forward(t, 1)
			objects := objectStore{}
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
				ref := c.Evidence[0]
				if objects.resolves(ref) {
					t.Fatal("evidence present before write")
				}
				content := []byte(ref.Kind + ref.Subject + ref.Outcome)
				objects[ref.Digest] = content
				if !objects.resolves(ref) {
					t.Fatal("bad fixture digest")
				}
				s.accept(t, c)
				saved, _ := s.ledger.Transfer(key, "T")
				var ack Evidence
				switch step.kind {
				case Inventory:
					ack = saved.Inventory
				case BeliefAccepted:
					ack = saved.Belief
				case Reserve:
					ack = saved.Decision
				}
				if !objects.resolves(ack) {
					t.Fatal("lost acknowledged reference")
				}
				delete(objects, ack.Digest)
				dependentKind := map[Kind]Kind{Inventory: BeliefAccepted, BeliefAccepted: Reserve, Reserve: FenceReceipt}[step.kind]
				dependent := next(s.ledger, key, dependentKind, "T", "worker")
				decideDependent := func() error {
					if !objects.resolves(ack) {
						return ErrEvidence
					}
					_, _, err := Decide(s.ledger, dependent)
					return err
				}
				if err := decideDependent(); err != ErrEvidence {
					t.Fatal("gate accepted missing evidence", err)
				}
				objects[ack.Digest] = []byte("substituted")
				if objects.resolves(ack) {
					t.Fatal("substituted object verified")
				}
				objects[ack.Digest] = content
				if err := decideDependent(); err != nil {
					t.Fatal("restoring exact object failed", err)
				}
			}
		}},
		{"Before/after route intent, actual effect start, or outcome write", func(t *testing.T) {
			s := forward(t, 4)
			c := next(s.ledger, key, FenceReceipt, "T", "worker")
			child := Effect{"child", "worker"}
			broker := routeBroker{}
			if broker.start(child) || !broker.begin(child) {
				t.Fatal("intent ordering")
			}
			restart := routeBroker{journal: broker.journal}
			if restart.start(child) || !broker.start(child) || broker.start(child) {
				t.Fatal("effect started without fresh intent")
			}
			restart = routeBroker{journal: broker.journal}
			if restart.begin(child) || restart.start(child) {
				t.Fatal("retried unresolved effect")
			}
			restart.journal.sealed = true
			if _, ok := restart.journal.fence(c); ok {
				t.Fatal("sealed with an uncertain outcome")
			}
			unsettled := c
			unsettled.Evidence = c.Evidence[:1]
			refused(t, s.ledger, unsettled, ErrEvidence)
			wrong := c
			wrong.Evidence = slices.Clone(c.Evidence)
			wrong.Evidence[1].Route = "qargi"
			refused(t, s.ledger, wrong, ErrEvidence)
			assertHolder(t, s.ledger, legacy, 1, false)
			restart.journal.outcome = child
			durable, ok := restart.journal.fence(c)
			if !ok {
				t.Fatal("missing settled receipt")
			}
			s.accept(t, durable)
			tr, _ := s.ledger.Transfer(key, "T")
			if len(tr.Outstanding) != 0 {
				t.Fatal("receipt did not settle the child")
			}
		}},
		{"Before/after sealing a route, writing its receipt, or recording the receipt at home", func(t *testing.T) {
			s := forward(t, 4)
			c := next(s.ledger, key, FenceReceipt, "T", "worker")
			child := Effect{"child", "worker"}
			route := routeJournal{intent: child, outcome: child}
			if _, ok := route.fence(c); ok {
				t.Fatal("receipt without seal")
			}
			route.sealed = true
			reopened := routeBroker{journal: route}
			if reopened.begin(Effect{"other", "worker"}) {
				t.Fatal("restart unsealed route")
			}
			first, ok := route.fence(c)
			if !ok {
				t.Fatal("missing receipt")
			}
			recoveredRoute := route
			retry := c
			retry.RequestID = "replacement"
			second, ok := recoveredRoute.fence(retry)
			if !ok || !reflect.DeepEqual(first, second) {
				t.Fatal("lost reply changed the sealed receipt")
			}
			s.accept(t, second)
			refused(t, s.ledger, next(s.ledger, key, Fenced, "T", ""), ErrEvidence)
			refused(t, s.ledger, next(s.ledger, key, FenceReceipt, "T", "worker"), ErrEvidence)
			s.steps(t, "T", forwardSteps[5:7])
			assertHolder(t, s.ledger, legacy, 1, false)
		}},
		{"Before/after Qargi applies the signed event and records its receipt", func(t *testing.T) {
			s := forward(t, 8)
			c := next(s.ledger, key, ConfirmReceipt, "T", "qargi")
			refused(t, s.ledger, next(s.ledger, key, Open, "T", ""), ErrEvidence)
			commit := s.events[len(s.events)-1]
			prior := projectionTxn{head: commit.Command.Expected, digest: commit.Public.PreviousPublicDigest}
			leases := [2]bool{true, false}
			if partial, err := prior.confirm(commit, c, leases); err != ErrEvidence || !reflect.DeepEqual(partial, prior) {
				t.Fatal("published partial feature release")
			}
			leases[1] = true
			staged, err := prior.confirm(commit, c, leases)
			if err != nil {
				t.Fatal(err)
			}
			if prior.receipt.RequestID != "" || prior.head == commit.Result.Head {
				t.Fatal("staged projection visible")
			}
			installed := staged
			if installed.head != commit.Result.Head || !reflect.DeepEqual(installed.receipt, c) {
				t.Fatal("projection and receipt separated")
			}
			recoveredTxn, err := installed.install(commit, Command{})
			if err != nil || !reflect.DeepEqual(installed, recoveredTxn) {
				t.Fatal("redelivery changed projection receipt")
			}
			e := s.accept(t, recoveredTxn.receipt)
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
		{"Before/after opening locally on either Open or Aborted", func(t *testing.T) {
			for _, abort := range []bool{false, true} {
				s := forward(t, 2)
				if abort {
					s.steps(t, "T", []step{{kind: AbortPrepared}, {kind: Aborted}})
				} else {
					s.steps(t, "T", forwardSteps[2:])
				}
				opened := s.events[len(s.events)-1].Result.Head
				r, _ := s.ledger.Row(key)
				gate := startGate{projection: r.PublicDigest, qargiAvailable: true}
				if gate.permits(s.ledger, opened) {
					t.Fatal("local failure admitted effect")
				}
				gate.local = opened
				if !gate.permits(s.ledger, opened) || !r.Permits(opened) {
					t.Fatal("missing fresh admission")
				}
				s.accept(t, next(s.ledger, key, Freeze, "U", ""))
				r, _ = s.ledger.Row(key)
				if gate.permits(s.ledger, opened) || r.Permits(opened) {
					t.Fatal("delayed local open")
				}
			}
		}},
		{"Lost begin, commit, abort, confirmation or rollback reply; duplicate or reordered request", func(t *testing.T) {
			s := forward(t, 10)
			s.steps(t, "U", []step{{kind: RollbackFreeze}, {kind: AbortPrepared}, {kind: AbortedClosed}})
			recovered, err := recoverAt(s.events, s.ledger.Checkpoint())
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range s.events {
				if got, ok := Lookup(recovered, key, e.Command.TransferID, e.Command.RequestID); !ok || got != e.Result {
					t.Fatal("lost operation after restart")
				}
				dup, res, err := Decide(s.ledger, e.Command)
				if err != nil || dup != nil || res != e.Result {
					t.Fatal(e.Command.Kind, res, err)
				}
				bad := e.Command
				bad.Expected.Generation++
				refused(t, s.ledger, bad, ErrConflict)
			}
			found := false
			for _, event := range s.events {
				if event.Command.TransferID == "T" && event.Command.Kind == ConfirmReceipt {
					found = true
				}
			}
			if !found {
				t.Fatal("trace lost no forward confirmation reply")
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
			baseRow, _ := s.ledger.Row(key)
			cursor := projectionTxn{head: baseRow.Head, digest: baseRow.PublicDigest}
			if _, err := cursor.install(a.events[1], Command{}); err != ErrChain {
				t.Fatal("projection skipped a revision")
			}
			first, err := cursor.install(a.events[0], Command{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.install(b.events[0], Command{}); err != ErrChain {
				t.Fatal("conflicting signed revision")
			}
			stale := startGate{baseRow.Head, s.events[len(s.events)-1].Public.PreviousPublicDigest, true}
			if !(startGate{baseRow.Head, cursor.digest, true}).permits(s.ledger, baseRow.Head) || stale.permits(s.ledger, baseRow.Head) {
				t.Fatal("stale projection admitted")
			}
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
			s := forward(t, 4)
			s.steps(t, "T", []step{{kind: FenceReceipt, route: "qargi"}})
			open := seeded(t)
			row, _ := open.ledger.Row(key)
			gate := startGate{row.Head, row.PublicDigest, true}
			if !gate.permits(open.ledger, row.Head) || gate.permits(Ledger{}, row.Head) {
				t.Fatal("start without home")
			}
			restored, err := recoverAt(s.events, s.ledger.Checkpoint())
			if err != nil {
				t.Fatal(err)
			}
			s.ledger = restored
			tr, _ := s.ledger.Transfer(key, "T")
			if !slices.Contains(tr.Outstanding, Effect{"child", "worker"}) {
				t.Fatal("peer partition has no outstanding child")
			}
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
			lease := featureLease{"feature-A", legacy.ID, "lease-r7"}
			request := releaseRequest{"release-feature-A", lease}
			journal := releaseJournal{lease: lease, results: make(map[string]releaseResult)}
			pending := Effect{request.id, "qargi"}
			route := routeJournal{intent: pending}
			if err := journal.release(request); err != nil {
				t.Fatal(err)
			}
			// The durable mutation succeeds, then the reply is lost.
			seal := next(s.ledger, key, FenceReceipt, "T", "qargi")
			route.sealed = true
			if _, ok := route.fence(seal); ok {
				t.Fatal("sealed over uncertain release")
			}
			if _, err := journal.query(request.id, false); err != errUnavailable {
				t.Fatal("partition resolved release")
			}
			if _, err := journal.query("new-attempt", true); err != ErrEvidence {
				t.Fatal("wrong release request")
			}
			got, err := journal.query(request.id, true)
			if err != nil || got.request != request || got.disposition != "released" || journal.lease.holder != "" {
				t.Fatal("original release unavailable", got, err)
			}
			if err := journal.release(request); err != nil {
				t.Fatal("release retry", err)
			}
			changed := request
			changed.expected.revision = "lease-r8"
			if err := journal.release(changed); err != ErrConflict {
				t.Fatal("release ID reused", err)
			}
			route.outcome = Effect{got.request.id, "qargi"}
			receipt, ok := route.fence(seal)
			if !ok || receipt.RequestID == got.request.id {
				t.Fatal("missing distinct seal")
			}
			s.accept(t, receipt)
			s.steps(t, "T", forwardSteps[6:8])
			s.steps(t, "T", forwardSteps[9:10])
			refused(t, s.ledger, next(s.ledger, key, Open, "T", ""), ErrEvidence)
			s.steps(t, "T", forwardSteps[8:9])
			s.steps(t, "T", forwardSteps[10:])
			home, err := recoverAt(s.events, s.ledger.Checkpoint())
			if err != nil {
				t.Fatal(err)
			}
			row, _ := home.Row(key)
			gate := startGate{row.Head, row.PublicDigest, true}
			if !gate.permits(home, row.Head) {
				t.Fatal("matching authority unavailable")
			}
			gate.qargiAvailable = false
			if gate.permits(home, row.Head) {
				t.Fatal("dependent effect started while Qargi unavailable")
			}
			assertHolder(t, home, managed, 2, true)
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
			dispute := next(s.ledger, key, ConfirmReceipt, "T", "worker")
			dispute.Evidence[0].Outcome = "legacy-still-running"
			dispute.Evidence[0].Digest = sha256Hex([]byte("two routes observed"))
			s.accept(t, dispute)
			home, err := recoverAt(s.events, s.ledger.Checkpoint())
			if err != nil {
				t.Fatal(err)
			}
			closed, _ := home.Row(key)
			if closed.Permits(closed.Head) || closed.Permits(old) || closed.Permits(r.Head) {
				t.Fatal("claim elected during conflict")
			}
			tr, _ := home.Transfer(key, "T")
			if !slices.Contains(tr.Disputes, dispute.Evidence[0]) {
				t.Fatal("lost live-route evidence")
			}
			resolve := next(home, key, ReceiptReconciled, "T", "")
			resolve.Evidence = []Evidence{{Kind: "reconciliation", Issuer: dispute.Evidence[0].Issuer, Subject: dispute.Evidence[0].Digest, Scope: closed.Head, TransferID: "T", Digest: sha256Hex([]byte("old executor stopped and work accounted for")), Revision: "review", Outcome: "reconciled"}}
			s.accept(t, resolve)
			s.steps(t, "T", forwardSteps[4:6])
			refused(t, s.ledger, next(s.ledger, key, Open, "T", ""), ErrEvidence)
			s.steps(t, "T", forwardSteps[8:])
			assertHolder(t, s.ledger, managed, 2, true)
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
			row, _ := l.Row(key)
			if row.Head.TransferID != "U" {
				t.Fatal("lost reverse transfer")
			}
			s.ledger = l
			s.accept(t, command(row, after, HoldReviewed, "T", ""))
			s.steps(t, "U", forwardSteps[8:])
			assertHolder(t, s.ledger, legacy, 3, true)
			reviewed, _ := s.ledger.Transfer(key, "T")
			if reviewed.Hold.Review.Subject != prior.Hold.Preservation.Digest {
				t.Fatal("hold review failed after crash")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.check)
	}
}
