package enrollment

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	a "ataniq/internal/assignment"
	"ataniq/internal/assignment/wire"
	cp "ataniq/internal/checkpoint"
)

func appendOnly(ctx context.Context, s *Store, e a.Event) error {
	_, err := AppendAndAdvance(ctx, s, e)
	return err
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func want(t *testing.T, err error, reason Reason) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Reason != reason {
		t.Fatalf("got %v; want %q", err, reason)
	}
}

func key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(nil)
	check(t, err)
	return k
}

func pub(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

type fixture struct {
	ctx                    context.Context
	root                   FileRoot
	home, session, witness ed25519.PrivateKey
	w                      *cp.Memory
	slot                   cp.Slot
	client                 cp.Client
	b                      Binding
}

func privateRoot(t *testing.T) FileRoot {
	t.Helper()
	root := FileRoot{t.TempDir()}
	check(t, os.Chmod(root.Path, 0700))
	return root
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{ctx: t.Context(), root: privateRoot(t), home: key(t), session: key(t), witness: key(t), slot: cp.Slot{Witness: "w", Authority: "a", Store: "store"}}
	f.newWitness(t)
	return f
}

func (f *fixture) newWitness(t *testing.T) {
	t.Helper()
	var err error
	f.w, err = cp.NewMemory(f.slot.Witness, 1, f.witness)
	check(t, err)
	f.connect(t)
}

func (f *fixture) connect(t *testing.T) {
	t.Helper()
	check(t, f.w.Grant(f.slot, pub(f.home), true))
	var err error
	f.client, err = f.w.Connect(f.slot, f.home, f.session, pub(f.witness))
	check(t, err)
}

func (f *fixture) enroll(fault Fault) error {
	var err error
	f.b, err = Enroll(f.ctx, f.root, f.w.Administration(), f.slot, 1, f.home, pub(f.witness), fault)
	return err
}

func enrolled(t *testing.T) *fixture { t.Helper(); f := setup(t); check(t, f.enroll(nil)); return f }

func (f *fixture) start(fault Fault) (*Store, error) {
	return Start(f.ctx, f.root, f.b, f.client, f.session, fault)
}

func (f *fixture) open(t *testing.T, fault Fault) *Store {
	t.Helper()
	s, err := f.start(fault)
	check(t, err)
	t.Cleanup(func() { check(t, s.Close()) })
	return s
}

func (f *fixture) refused(t *testing.T, reason Reason) {
	t.Helper()
	s, err := f.start(nil)
	if s != nil {
		check(t, s.Close())
		t.Fatal("returned writable store")
	}
	want(t, err, reason)
}

func (f *fixture) state(t *testing.T) cp.State {
	t.Helper()
	v, err := f.w.Administration().Read(f.ctx, cp.ReadRequest{Slot: f.slot, Challenge: id()})
	check(t, err)
	return v.Current
}

func (f *fixture) release(t *testing.T) {
	t.Helper()
	_, err := f.w.Administration().Release(f.ctx, cp.Mutation{ID: id(), Expected: f.state(t)})
	check(t, err)
	f.session = key(t)
	f.connect(t)
}

func (f *fixture) ledgerPath() string { return filepath.Join(f.root.Path, ledgerName) }

func contents(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	check(t, err)
	return b
}

func put(t *testing.T, path string, b []byte) { t.Helper(); check(t, os.WriteFile(path, b, 0600)) }

func (f *fixture) seed(t *testing.T, s *Store, project string) a.Event {
	t.Helper()
	l, err := s.Ledger()
	check(t, err)
	h := a.Head{Key: a.Key{Home: f.slot.Authority, Project: project, Role: "lead"}}
	c := a.Command{Kind: a.Seed, Expected: h, RequestID: "request", TransferID: "transfer", Actor: "owner", Authorization: "approval",
		Candidate: a.Binding{Kind: "legacy", ID: "holder", Authority: h.Key.Home, Store: "store", Incarnation: "incarnation", Conversation: "conversation"}}
	for _, item := range [][3]string{{"enrollment", "holder", "verified"}, {"route", "qargi", "enabled"}} {
		c.Evidence = append(c.Evidence, a.Evidence{Kind: item[0], Subject: item[1], Outcome: item[2], Issuer: "owner", Revision: "one", Digest: strings.Repeat("a", 64), Scope: h, TransferID: c.TransferID})
	}
	e, _, err := a.Decide(l, c)
	check(t, err)
	signer, err := wire.NewSigner(f.b.Header, f.home)
	check(t, err)
	body, err := e.SigningBytes()
	check(t, err)
	e.Signature, err = signer.Sign(f.b.Header, body)
	check(t, err)
	return *e
}

func TestCompareAndReplay(t *testing.T) {
	for _, kind := range []string{"boundary cut", "restore prefix", "torn", "fork", "bad signature", "missing", "empty", "header removed", "legacy", "registry", "wrong header", "unknown", "partition", "witness restore", "other enrollment"} {
		t.Run(kind, func(t *testing.T) {
			f := enrolled(t)
			s := f.open(t, nil)
			header := contents(t, f.ledgerPath())
			one := f.seed(t, s, "one")
			alternative := f.seed(t, s, "another")
			check(t, appendOnly(f.ctx, s, one))
			prefix := contents(t, f.ledgerPath())
			fork := f.seed(t, s, "fork")
			check(t, appendOnly(f.ctx, s, f.seed(t, s, "two")))
			check(t, s.Close())
			f.release(t)
			path := f.ledgerPath()
			var reason Reason
			switch kind {
			case "boundary cut":
				check(t, os.Truncate(path, int64(len(prefix))))
				reason = HomeBehind
			case "restore prefix":
				put(t, path, header)
				reason = HomeBehind
			case "torn":
				check(t, os.Truncate(path, int64(len(prefix)-1)))
				reason = Truncated
			case "fork":
				raw, err := fork.RecordBytes()
				check(t, err)
				put(t, path, append(prefix, raw...))
				reason = Fork
			case "bad signature":
				alternative.Signature = one.Signature
				b, err := alternative.RecordBytes()
				check(t, err)
				put(t, path, append(header, b...))
				reason = Corrupt
			case "missing":
				check(t, os.Remove(path))
				reason = MissingHeader
			case "header removed":
				put(t, path, prefix[len(header):])
				reason = MissingHeader
			case "empty":
				put(t, path, nil)
				reason = MissingHeader
			case "legacy":
				put(t, path, []byte("{\"format\":\"legacy\"}\n"))
				reason = Legacy
			case "registry":
				check(t, os.Rename(path, filepath.Join(f.root.Path, "registry.jsonl")))
				reason = Legacy
			case "wrong header":
				h := f.b.Header
				h.Incarnation = "other"
				b, err := h.Bytes()
				check(t, err)
				put(t, path, b)
				reason = IdentityMismatch
			case "unknown":
				f.newWitness(t)
				reason = Unknown
			case "partition":
				f.w.Inhibit(true)
				reason = Unreachable
			case "witness restore", "other enrollment":
				// Restore only the enrollment transaction on a fresh independent witness.
				f.newWitness(t)
				q, err := f.b.request()
				check(t, err)
				if kind == "other enrollment" {
					q.ID = id()
				}
				q.ProofOfPossession, err = cp.ProveEnrollment(f.home, q)
				check(t, err)
				_, err = f.w.Administration().Enroll(f.ctx, q)
				check(t, err)
				reason = HomeAhead
				if kind == "other enrollment" {
					reason = Unknown
				}
			}
			before, readErr := os.ReadFile(path)
			f.refused(t, reason)
			if got, err := os.ReadFile(path); !bytes.Equal(got, before) || (err == nil) != (readErr == nil) {
				t.Fatal("failed start changed ledger")
			}
		})
	}
}

func TestProtectedRootAndFile(t *testing.T) {
	for _, kind := range []string{"copy", "replaced file", "link", "hardlink", "root permissions", "file permissions", "symlink root", "symlink ancestor", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			f := enrolled(t)
			p := f.ledgerPath()
			reason := Insecure
			switch kind {
			case "copy":
				body := contents(t, p)
				f.root = privateRoot(t)
				put(t, f.ledgerPath(), body)
				reason = IdentityMismatch
			case "replaced file":
				body := contents(t, p)
				check(t, os.Rename(p, p+".original"))
				put(t, p, body)
				reason = IdentityMismatch
			case "link":
				check(t, os.Rename(p, p+".original"))
				check(t, os.Symlink(p+".original", p))
			case "hardlink":
				check(t, os.Link(p, p+".copy"))
			case "root permissions":
				check(t, os.Chmod(f.root.Path, 0750))
			case "file permissions":
				check(t, os.Chmod(p, 0640))
			case "symlink root":
				dest := filepath.Join(t.TempDir(), "alias")
				check(t, os.Symlink(f.root.Path, dest))
				f.root.Path = dest
			case "symlink ancestor":
				dest := filepath.Join(t.TempDir(), "alias")
				check(t, os.Symlink(filepath.Dir(f.root.Path), dest))
				f.root.Path = filepath.Join(dest, filepath.Base(f.root.Path))
			case "fifo":
				check(t, os.Remove(p))
				check(t, syscall.Mkfifo(p, 0600))
			}
			f.refused(t, reason)
			if f.state(t).Revision != 1 {
				t.Fatal("claimed before validation")
			}
		})
	}
}

func TestTwoHomesAndRetirement(t *testing.T) {
	f := enrolled(t)
	s := f.open(t, nil)
	f.refused(t, Occupied)
	other := setup(t)
	other.witness, other.w = f.witness, f.w
	other.slot.Authority = "second"
	other.connect(t)
	check(t, other.enroll(nil))
	s2 := other.open(t, nil)
	check(t, appendOnly(other.ctx, s2, other.seed(t, s2, "same")))
	check(t, appendOnly(f.ctx, s, f.seed(t, s, "same")))
	check(t, s.Close())
	f.refused(t, Occupied)
	m := cp.Mutation{ID: id(), Expected: f.state(t)}
	_, err := f.w.Administration().Retire(f.ctx, cp.Retirement{Mutation: m, DispositionDigest: strings.Repeat("b", 64)})
	check(t, err)
	f.refused(t, Retired)
	if other.state(t).Revision != 3 {
		t.Fatal("other home changed")
	}
	old := f.root
	f.root = privateRoot(t)
	want(t, f.enroll(nil), EnrollmentFailed)
	f.root = privateRoot(t)
	f.slot.Store = "fresh"
	f.home, f.session = key(t), key(t)
	f.connect(t)
	check(t, f.enroll(nil))
	f.open(t, nil)
	f.root = old
	f.refused(t, IdentityMismatch)
}

func TestConcurrentAppends(t *testing.T) {
	f := enrolled(t)
	s := f.open(t, nil)
	e := f.seed(t, s, "row")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { results <- appendOnly(f.ctx, s, e) })
	}
	wg.Wait()
	close(results)
	n := 0
	for err := range results {
		if err == nil {
			n++
		} else {
			want(t, err, Corrupt)
		}
	}
	if n != 1 || f.state(t).Head.Sequence != 1 {
		t.Fatal(n)
	}
}

func TestFileReplacementDuringClaim(t *testing.T) {
	f := enrolled(t)
	path := f.ledgerPath()
	f.client = claimHook{f.client, func() {
		check(t, os.Rename(path, path+".previous"))
		put(t, path, contents(t, path+".previous"))
	}}
	f.refused(t, IdentityMismatch)
}

func TestFencedWriter(t *testing.T) {
	f := enrolled(t)
	store := f.open(t, nil)
	event := f.seed(t, store, "project")
	f.release(t)
	before := contents(t, f.ledgerPath())
	want(t, appendOnly(f.ctx, store, event), Occupied)
	if !bytes.Equal(before, contents(t, f.ledgerPath())) || f.state(t).Head.Sequence != 0 {
		t.Fatal("old writer appended after release")
	}
	_, err := store.Ledger()
	want(t, err, Closed)
}

func TestPrewriteFailureCanRetry(t *testing.T) {
	for _, failure := range []string{"unreachable", "cancel", "signing"} {
		t.Run(failure, func(t *testing.T) {
			f := enrolled(t)
			s := f.open(t, nil)
			e := f.seed(t, s, "row")
			ctx := f.ctx
			session := s.session
			reason := Unreachable
			switch failure {
			case "unreachable":
				f.w.Inhibit(true)
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "signing":
				s.session = nil
				reason = Interrupted
			}
			want(t, appendOnly(ctx, s, e), reason)
			f.w.Inhibit(false)
			s.session = session
			if _, err := os.Stat(filepath.Join(f.root.Path, stageName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stage after pre-write failure: %v", err)
			}
			check(t, appendOnly(f.ctx, s, e))
		})
	}
}

func TestAncestorSubstitutionReturningToHeldRoot(t *testing.T) {
	f := setup(t)
	parent := filepath.Join(t.TempDir(), "parent")
	f.root.Path = filepath.Join(parent, "store")
	check(t, os.MkdirAll(f.root.Path, 0700))
	check(t, f.enroll(nil))
	f.client = claimHook{f.client, func() {
		check(t, os.Rename(parent, parent+".original"))
		check(t, os.Symlink(parent+".original", parent))
	}}
	f.refused(t, IdentityMismatch)
}

func TestTraverseOnlyAncestor(t *testing.T) {
	f := setup(t)
	parent := filepath.Join(t.TempDir(), "traverse")
	f.root.Path = filepath.Join(parent, "private")
	check(t, os.MkdirAll(f.root.Path, 0700))
	check(t, os.Chmod(parent, 0100))
	t.Cleanup(func() { check(t, os.Chmod(parent, 0700)) })
	check(t, f.enroll(nil))
	s := f.open(t, nil)
	check(t, appendOnly(f.ctx, s, f.seed(t, s, "row")))
}

func TestRestrictiveUmask(t *testing.T) {
	f := setup(t)
	old := syscall.Umask(0777)
	defer syscall.Umask(old)
	check(t, f.enroll(nil))
	s := f.open(t, nil)
	check(t, appendOnly(f.ctx, s, f.seed(t, s, "row")))
}

func TestStartWithoutClient(t *testing.T) {
	for _, c := range []cp.Client{nil, (*claimHook)(nil)} {
		f := enrolled(t)
		f.client = c
		f.refused(t, Unreachable)
		f.connect(t)
		f.open(t, nil)
	}
}

func TestStageRefusalReasons(t *testing.T) {
	f := enrolled(t)
	path := filepath.Join(f.root.Path, stageName)
	check(t, os.Symlink(ledgerName, path))
	f.refused(t, Insecure)
	check(t, os.Remove(path))
	put(t, path, []byte("{"))
	f.w.Inhibit(true)
	f.refused(t, Unreachable)
}
