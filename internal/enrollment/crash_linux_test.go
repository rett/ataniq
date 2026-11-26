package enrollment

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	cp "ataniq/internal/checkpoint"
)

type cut string

var errCut = errors.New("simulated crash")

func (c cut) After(point string) error {
	if point == string(c) {
		return errCut
	}
	return nil
}

func TestEnrollmentCuts(t *testing.T) {
	for _, point := range []string{"header.created", "header.written", "header.file-synced", "header.dir-synced", "enroll.registered", "lost reply"} {
		t.Run(point, func(t *testing.T) {
			f := setup(t)
			if point == "lost reply" {
				f.w.LoseNextReply()
			}
			err := f.enroll(cut(point))
			want(t, err, EnrollmentFailed)
			if point != "lost reply" && !errors.Is(err, errCut) {
				t.Fatal("missed boundary", err)
			}
			if f.b.Receipt.Body != nil {
				t.Fatal("issued binding on failure")
			}
			f.refused(t, Unknown)
			old := contents(t, f.ledgerPath())
			want(t, f.enroll(nil), EnrollmentFailed)
			if !bytes.Equal(old, contents(t, f.ledgerPath())) {
				t.Fatal("reenrollment replaced header")
			}
			_, err = f.w.Administration().Read(f.ctx, cp.ReadRequest{Slot: f.slot, Challenge: id()})
			if point == "enroll.registered" || point == "lost reply" {
				check(t, err)
			} else {
				var r *cp.Reason
				if !errors.As(err, &r) || r.Kind != cp.NotEnrolled {
					t.Fatal("registered before header was durable", err)
				}
			}
		})
	}
}

func TestNonemptyEnrollment(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.root.Path, "unrelated"), nil)
	want(t, f.enroll(nil), EnrollmentFailed)
	if _, err := os.Stat(f.ledgerPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestAdvanceCuts(t *testing.T) {
	for _, tc := range []struct {
		point                        string
		appended, committed, removed bool
	}{
		{"stage.created", false, false, false}, {"stage.written", false, false, false}, {"stage.file-synced", false, false, false}, {"stage.dir-synced", false, false, false},
		{"append.written", true, false, false}, {"append.file-synced", true, false, false}, {"append.dir-synced", true, false, false},
		{"advance.committed", true, true, false}, {"lost reply", true, true, false}, {"stage.removed", true, true, true}, {"stage.removal-synced", true, true, true},
	} {
		t.Run(tc.point, func(t *testing.T) {
			f := enrolled(t)
			s := f.open(t, cut(tc.point))
			e := f.seed(t, s, "project")
			if tc.point == "lost reply" {
				f.w.LoseNextReply()
			}
			err := appendOnly(f.ctx, s, e)
			want(t, err, Interrupted)
			if tc.point != "lost reply" && !errors.Is(err, errCut) {
				t.Fatal("missed boundary", err)
			}
			_, err = s.Ledger()
			want(t, err, Closed)
			want(t, appendOnly(f.ctx, s, e), Closed)
			check(t, s.Close())
			state := f.state(t)
			if (state.Head.Sequence == 1) != tc.committed {
				t.Fatal("witness at cut", state.Head)
			}
			p := filepath.Join(f.root.Path, stageName)
			_, err = os.Stat(p)
			if errors.Is(err, os.ErrNotExist) != tc.removed {
				t.Fatal("stage at cut", err)
			}

			if !tc.appended {
				before := contents(t, p)
				f.refused(t, Occupied)
				if f.state(t) != state || !bytes.Equal(before, contents(t, p)) {
					t.Fatal("changed unappended intent before claim")
				}
				f.release(t)
				f.open(t, nil)
				if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) || f.state(t).Head != state.Head {
					t.Fatal("unappended stage survived admission or changed head", err)
				}
				return
			}
			if !tc.committed {
				// A different session cannot rebase an interrupted advance.
				saved := f.session
				f.session = key(t)
				f.connect(t)
				f.refused(t, Interrupted)
				if f.state(t) != state {
					t.Fatal("re-signed a foreign session's intent")
				}
				f.session = saved
				f.connect(t)
			}
			// A refused claim cannot commit even the original session's CAS.
			stageBefore, stageErr := os.ReadFile(p)
			f.refused(t, Occupied)
			current := f.state(t)
			if current != state {
				t.Fatal("occupied start mutated witness", current)
			}
			stageAfter, err := os.ReadFile(p)
			if !bytes.Equal(stageBefore, stageAfter) || (err == nil) != (stageErr == nil) {
				t.Fatal("occupied start mutated stage")
			}
			f.release(t)
			if !tc.committed {
				f.refused(t, Interrupted)
				return
			}
			s = f.open(t, nil)
			l, err := s.Ledger()
			check(t, err)
			if l.Checkpoint() != current.Head {
				t.Fatal("admitted an unwitnessed projection")
			}
			if _, err = os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("stage after admission", err)
			}
		})
	}
}

func TestOwnerReconcilesUncommittedSuffix(t *testing.T) {
	f := enrolled(t)
	inhibit := false
	s := f.open(t, boundaryHook(func(point string) error {
		if inhibit && point == "append.dir-synced" {
			f.w.Inhibit(true)
		}
		return nil
	}))
	check(t, appendOnly(f.ctx, s, f.seed(t, s, "one")))
	prefix := contents(t, f.ledgerPath())
	head := f.state(t).Head
	inhibit = true
	want(t, appendOnly(f.ctx, s, f.seed(t, s, "two")), Interrupted)
	check(t, s.Close())
	f.w.Inhibit(false)
	f.release(t)
	f.refused(t, Interrupted)

	// The owner cuts only the unwitnessed suffix, retaining the signed stage.
	check(t, os.Truncate(f.ledgerPath(), int64(len(prefix))))
	s = f.open(t, nil)
	ledger, err := s.Ledger()
	check(t, err)
	if ledger.Checkpoint() != head || f.state(t).Head != head {
		t.Fatal("did not admit the witnessed prefix")
	}
	if _, err := os.Stat(filepath.Join(f.root.Path, stageName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stage after reconciliation", err)
	}
	check(t, appendOnly(f.ctx, s, f.seed(t, s, "three")))
}

func TestInvalidStageNeverAdvances(t *testing.T) {
	for _, kind := range []string{"missing", "record", "intent id", "session proof", "released fence", "foreign predecessor"} {
		t.Run(kind, func(t *testing.T) {
			f := enrolled(t)
			s := f.open(t, cut("append.dir-synced"))
			want(t, appendOnly(f.ctx, s, f.seed(t, s, "project")), Interrupted)
			check(t, s.Close())
			path := filepath.Join(f.root.Path, stageName)
			var p staged
			check(t, json.Unmarshal(contents(t, path), &p))
			reason := Interrupted
			switch kind {
			case "missing":
				check(t, os.Remove(path))
				reason = HomeAhead
			case "record":
				p.Record = append(p.Record, ' ')
			case "session proof":
				p.Proof = nil
			case "released fence":
				f.release(t)
			case "intent id", "foreign predecessor":
				q, err := p.advance()
				check(t, err)
				if kind == "intent id" {
					q.ID = id()
				} else {
					q.Expected.LastReceiptDigest = f.state(t).Head.Digest
				}
				p.Intent, err = cp.AdvanceBody(q)
				check(t, err)
				if kind == "foreign predecessor" {
					p.Proof, err = cp.Sign(cp.SessionNamespace, f.session, p.Intent)
					check(t, err)
				}
			}
			if kind != "missing" {
				b, err := json.Marshal(p)
				check(t, err)
				put(t, path, b)
			}
			f.refused(t, reason)
			if f.state(t).Head.Sequence != 0 {
				t.Fatal("invalid stage advanced witness")
			}
		})
	}
}

type boundaryHook func(string) error

func (fn boundaryHook) After(point string) error { return fn(point) }

func swapAt(t *testing.T, f *fixture, boundary string) Fault {
	t.Helper()
	hit := false
	t.Cleanup(func() {
		if !hit {
			t.Errorf("missed %s", boundary)
		}
	})
	return boundaryHook(func(point string) error {
		if point == boundary {
			hit = true
			check(t, os.Rename(f.root.Path, f.root.Path+".old"))
			check(t, os.Mkdir(f.root.Path, 0700))
		}
		return nil
	})
}

func TestSwappedDirectoryNeverAdmitted(t *testing.T) {
	for _, point := range []string{"header.created", "header.written", "header.file-synced", "header.dir-synced", "enroll.registered"} {
		t.Run(point, func(t *testing.T) {
			f := setup(t)
			want(t, f.enroll(swapAt(t, f, point)), EnrollmentFailed)
		})
	}
	for _, point := range []string{"stage.created", "stage.written", "stage.file-synced", "stage.dir-synced", "append.written", "append.file-synced", "append.dir-synced", "advance.committed", "receipt.created", "receipt.written", "receipt.file-synced", "receipt.dir-synced", "receipt.renamed", "receipt.rename-synced", "stage.removed", "stage.removal-synced"} {
		t.Run(point, func(t *testing.T) {
			f := enrolled(t)
			s := f.open(t, swapAt(t, f, point))
			if err := appendOnly(f.ctx, s, f.seed(t, s, "row")); err == nil {
				t.Fatal("published after directory replacement")
			}
		})
	}
	for _, point := range []string{"start.claimed", "receipt.created", "receipt.written", "receipt.file-synced", "receipt.dir-synced", "receipt.renamed", "receipt.rename-synced", "stage.removed", "stage.removal-synced"} {
		t.Run("start-"+point, func(t *testing.T) {
			f := enrolled(t)
			s := f.open(t, cut("advance.committed"))
			want(t, appendOnly(f.ctx, s, f.seed(t, s, "row")), Interrupted)
			check(t, s.Close())
			f.release(t)
			s, err := f.start(swapAt(t, f, point))
			if s != nil {
				check(t, s.Close())
			}
			want(t, err, IdentityMismatch)
		})
	}
}

func TestTornStageNeedsCleanEqualLedger(t *testing.T) {
	for _, kind := range []string{"equal", "ahead", "torn ledger"} {
		t.Run(kind, func(t *testing.T) {
			f := enrolled(t)
			s := f.open(t, cut("append.dir-synced"))
			header := contents(t, f.ledgerPath())
			want(t, appendOnly(f.ctx, s, f.seed(t, s, "row")), Interrupted)
			check(t, s.Close())
			f.release(t)
			path := filepath.Join(f.root.Path, stageName)
			put(t, path, []byte("{"))
			head := f.state(t).Head
			switch kind {
			case "equal":
				put(t, f.ledgerPath(), header)
				f.open(t, nil)
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("stage remains", err)
				}
			case "ahead":
				f.refused(t, Interrupted)
			case "torn ledger":
				put(t, f.ledgerPath(), append(header, '{'))
				f.refused(t, Truncated)
			}
			if f.state(t).Head != head {
				t.Fatal("changed witness head")
			}
		})
	}
}

func TestAdvanceReceiptSurvivesClose(t *testing.T) {
	f := enrolled(t)
	s := f.open(t, nil)
	first, err := AppendAndAdvance(f.ctx, s, f.seed(t, s, "one"))
	check(t, err)
	ack, err := cp.VerifyReceipt(f.b.WitnessPin, first.Receipt)
	check(t, err)
	if ack.ID != first.ID || ack.State != f.state(t) {
		t.Fatal("returned wrong evidence")
	}
	prior := contents(t, filepath.Join(f.root.Path, receiptName))
	second, err := AppendAndAdvance(f.ctx, s, f.seed(t, s, "two"))
	check(t, err)
	check(t, s.Close())
	f.release(t)
	s = f.open(t, nil)
	got, err := s.LastReceipt()
	check(t, err)
	if got.ID != second.ID || cp.ReceiptDigest(got.Receipt) != cp.ReceiptDigest(second.Receipt) {
		t.Fatal("lost last verified advance")
	}
	files, err := os.ReadDir(f.root.Path)
	check(t, err)
	if len(files) != 2 {
		t.Fatal("receipt history grew", files)
	}
	check(t, s.Close())
	f.release(t)
	state := f.state(t)
	for _, raw := range [][]byte{prior, []byte("{")} {
		put(t, filepath.Join(f.root.Path, receiptName), raw)
		f.refused(t, Interrupted)
		if f.state(t) != state {
			t.Fatal("claimed before checking receipt")
		}
	}
}

func TestReceiptReplacementCuts(t *testing.T) {
	for _, point := range []string{"receipt.created", "receipt.written", "receipt.file-synced", "receipt.dir-synced", "receipt.renamed", "receipt.rename-synced"} {
		t.Run(point, func(t *testing.T) {
			f := enrolled(t)
			var fault cut
			s := f.open(t, boundaryHook(func(boundary string) error { return fault.After(boundary) }))
			check(t, appendOnly(f.ctx, s, f.seed(t, s, "one")))
			fault = cut(point)
			r, err := AppendAndAdvance(f.ctx, s, f.seed(t, s, "two"))
			want(t, err, Interrupted)
			if r != nil || !errors.Is(err, errCut) {
				t.Fatal("published or missed boundary", err)
			}
			check(t, s.Close())
			f.release(t)
			s = f.open(t, nil)
			last, err := s.LastReceipt()
			check(t, err)
			ack, err := cp.VerifyReceipt(f.b.WitnessPin, last.Receipt)
			check(t, err)
			if ack.State.Head.Sequence != 2 || ack.ID != last.ID {
				t.Fatal("failed to recover receipt")
			}
			files, err := os.ReadDir(f.root.Path)
			check(t, err)
			if len(files) != 2 {
				t.Fatal("replacement debris", files)
			}
		})
	}
}
