package durable

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"ataniq/internal/assignment"
	cp "ataniq/internal/checkpoint"
)

func fixtureEnrollment(t *testing.T, w *Witness) cp.Enrollment {
	t.Helper()
	home := testKey("fixture home")
	keyID, err := cp.KeyID(public(home))
	must(t, err)
	s := cp.Slot{Witness: "witness", Authority: "home", Store: "ledger"}
	header := assignment.Header{Authority: s.Authority, Store: s.Store, Incarnation: "one", KeyID: keyID, LedgerID: "first", Schema: 1}
	raw, err := header.Bytes()
	must(t, err)
	q := cp.Enrollment{Slot: s, ID: cp.ID{1}, HeaderBytes: raw, HomePublicKey: public(home)}
	q.ProofOfPossession, err = cp.ProveEnrollment(home, q)
	must(t, err)
	must(t, w.Grant(s, public(home), true))
	w.Inhibit(false)
	return q
}
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}
func create(t *testing.T, c Config) *Witness {
	t.Helper()
	w, err := Create(c)
	must(t, err)
	t.Cleanup(func() { must(t, w.Close()) })
	return w
}
func reopen(t *testing.T, c Config) *Witness {
	t.Helper()
	w, err := Open(c)
	must(t, err)
	t.Cleanup(func() { must(t, w.Close()) })
	return w
}

const childStage = "ATANIQ_CHECKPOINT_TEST_STAGE"
const childDirectory = "ATANIQ_CHECKPOINT_TEST_DIRECTORY"

func TestWitnessChild(t *testing.T) {
	stage := os.Getenv(childStage)
	if stage == "" {
		return
	}
	root := os.Getenv(childDirectory)
	c := Config{Directory: root, KeyPath: filepath.Join(filepath.Dir(root), "key"), Pin: public(testKey("process")), Identity: Identity{"witness", "store", "incarnation", 1}}
	w, err := Open(c)
	if stage == "compete" {
		if !errors.Is(err, ErrLocked) {
			t.Fatalf("second process opened store: %v", err)
		}
		return
	}
	must(t, err)
	q := fixtureEnrollment(t, w)
	w.fault = func(s string) error {
		if s == stage {
			os.Exit(79)
		}
		return nil
	}
	_, err = w.Administration().Enroll(t.Context(), q)
	t.Fatalf("process missed boundary %s: %v", stage, err)
}
func child(t *testing.T, c Config, stage string, want int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWitnessChild$")
	cmd.Env = append(os.Environ(), childStage+"="+stage, childDirectory+"="+c.Directory)
	out, err := cmd.CombinedOutput()
	if want == 0 && err == nil {
		return
	}
	var e *exec.ExitError
	if !errors.As(err, &e) || e.ExitCode() != want {
		t.Fatalf("child %s: %v %s", stage, err, out)
	}
}
func TestLockAcrossProcesses(t *testing.T) {
	c := config(t, testKey("process"))
	w := create(t, c)
	child(t, c, "compete", 0)
	must(t, w.Close())
	reopen(t, c)
}
func TestProcessCrashBoundaries(t *testing.T) {
	for _, stage := range []string{"before-append", "appended", "journal-synced", "directory-synced", "ack-signed", "receipt-appended", "receipt-synced", "receipt-directory-synced"} {
		t.Run(stage, func(t *testing.T) {
			c := config(t, testKey("process"))
			w := create(t, c)
			must(t, w.Close())
			child(t, c, stage, 79)
			recovered := reopen(t, c)
			q := fixtureEnrollment(t, recovered)
			found, err := recovered.HasReceipt(t.Context(), q.Slot, q.ID)
			must(t, err)
			if found != (stage != "before-append") {
				t.Fatalf("reopened receipt present=%t", found)
			}
			r, err := recovered.Administration().Enroll(t.Context(), q)
			must(t, err)
			a, err := cp.VerifyReceipt(c.Pin, r)
			must(t, err)
			if a.State.Revision != 1 {
				t.Fatal("retry applied twice")
			}
			must(t, recovered.Close())
			again := reopen(t, c)
			settled, err := again.Administration().Settle(t.Context(), 1, q.Slot, q.ID)
			must(t, err)
			if cp.ReceiptDigest(settled) != cp.ReceiptDigest(r) {
				t.Fatal("recovery changed original receipt")
			}
		})
	}
}

func TestSyncFailureStopsServing(t *testing.T) {
	c := config(t, testKey("io"))
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	// A real fsync error after append has an uncertain outcome.
	w.fault = func(stage string) error {
		if stage == "appended" {
			return w.journal.Close()
		}
		return nil
	}
	r, err := w.Administration().Enroll(t.Context(), q)
	if !errors.Is(err, cp.ErrUnknownOutcome) || r.Body != nil {
		t.Fatalf("sync failure: %+v %v", r, err)
	}
	if _, err = w.Administration().Read(t.Context(), cp.ReadRequest{Slot: q.Slot, Challenge: cp.ID{2}}); !errors.Is(err, cp.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = w.SignDisposition(t.Context(), [32]byte{}, "granted"); !errors.Is(err, cp.ErrUnavailable) {
		t.Fatal(err)
	}
	w.journal = nil // closed by fault injection
	must(t, w.Close())
	again := reopen(t, c)
	_, err = again.Administration().Settle(t.Context(), 1, q.Slot, q.ID)
	must(t, err)
}

func TestRefuseDamagedFiles(t *testing.T) {
	// Save three separate transactions, two of them valid even if replayed
	// against the same slot in another order. Mutations below leave signatures
	// intact except where the case explicitly attacks one.
	c := config(t, testKey("damage"))
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	journal := filepath.Join(c.Directory, journalName)
	lock := filepath.Join(c.Directory, lockName)
	genesis := len(readFile(t, journal))
	_, err := w.Administration().Enroll(t.Context(), q)
	must(t, err)
	enrolled := len(readFile(t, journal))
	_, err = w.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{2})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatal(err)
	}
	must(t, w.Close())
	original, anchors := readFile(t, journal), readFile(t, lock)
	swapped := bytes.Join([][]byte{original[:genesis], original[enrolled:], original[genesis:enrolled]}, nil)
	damagedSig := bytes.Clone(original)
	damagedSig[len(damagedSig)-1] ^= 0x80
	wrongReceipt := bytes.Clone(anchors)
	wrongReceipt[len(wrongReceipt)-35] ^= 0x80
	for _, tc := range []struct {
		name            string
		frames, entries []byte
		want            error
	}{
		{"empty", nil, anchors, ErrTorn}, {"short prefix", original[:2], anchors, ErrTorn},
		{"partial frame", original[:len(original)-1], anchors, ErrTorn}, {"whole frame removed", original[:enrolled], anchors, ErrTorn},
		{"changed signature", damagedSig, anchors, ErrCorrupt}, {"receipt signature", original, wrongReceipt, ErrCorrupt},
		{"duplicated frame", append(bytes.Clone(original), original[enrolled:]...), anchors, ErrCorrupt},
		{"reordered frames", swapped, anchors, ErrCorrupt}, {"partial entry", original, anchors[:len(anchors)-1], ErrTorn},
		{"missing multiple entries", original, nil, ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := c
			c.Directory = filepath.Join(t.TempDir(), "state")
			must(t, os.Mkdir(c.Directory, 0700))
			j, k := filepath.Join(c.Directory, journalName), filepath.Join(c.Directory, lockName)
			must(t, os.WriteFile(j, tc.frames, 0600))
			must(t, os.WriteFile(k, tc.entries, 0600))
			if _, err := Open(c); !errors.Is(err, tc.want) {
				t.Fatalf("open: %v", err)
			}
			if !bytes.Equal(tc.frames, readFile(t, j)) || !bytes.Equal(tc.entries, readFile(t, k)) {
				t.Fatal("modified damaged evidence")
			}
		})
	}
}

func TestIdentityAndCreation(t *testing.T) {
	c := config(t, testKey("identity"))
	if _, err := Open(c); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	w := create(t, c)
	must(t, w.Close())
	if _, err := Create(c); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	for _, edit := range []func(*Config){
		func(c *Config) { c.Identity.Witness += "x" }, func(c *Config) { c.Identity.Store += "x" }, func(c *Config) { c.Identity.Incarnation += "x" },
		func(c *Config) { c.Identity.Epoch++ }, func(c *Config) { c.Pin = public(testKey("other")) },
	} {
		given := c
		edit(&given)
		if _, err := Open(given); !errors.Is(err, ErrIdentity) {
			t.Fatal(err)
		}
	}
	c.Pin = public(testKey("other"))
	must(t, os.WriteFile(c.KeyPath, testKey("other").Seed(), 0600))
	if _, err := Open(c); !errors.Is(err, ErrCorrupt) {
		t.Fatal("opened journal signed by another key", err)
	}
}

func TestUnsafeStorage(t *testing.T) {
	for _, name := range []string{"key mode", "state mode", "journal mode", "lock mode", "key symlink", "ancestor symlink", "state symlink", "key hard link", "key fifo", "short seed", "long seed", "traversal"} {
		t.Run(name, func(t *testing.T) {
			c := config(t, testKey("permissions"))
			w := create(t, c)
			must(t, w.Close())
			switch name {
			case "key mode":
				must(t, os.Chmod(c.KeyPath, 0644))
			case "state mode":
				must(t, os.Chmod(c.Directory, 0710))
			case "journal mode":
				must(t, os.Chmod(filepath.Join(c.Directory, journalName), 0640))
			case "lock mode":
				must(t, os.Chmod(filepath.Join(c.Directory, lockName), 0640))
			case "key symlink", "key hard link":
				newPath := filepath.Join(t.TempDir(), "alias")
				if name == "key symlink" {
					must(t, os.Symlink(c.KeyPath, newPath))
				} else {
					must(t, os.Link(c.KeyPath, newPath))
				}
				c.KeyPath = newPath
			case "ancestor symlink", "state symlink":
				path := filepath.Join(t.TempDir(), "alias")
				if name == "state symlink" {
					must(t, os.Symlink(c.Directory, path))
					c.Directory = path
				} else {
					must(t, os.Symlink(filepath.Dir(c.KeyPath), path))
					c.KeyPath = filepath.Join(path, "key")
				}
			case "key fifo":
				c.KeyPath = filepath.Join(t.TempDir(), "fifo")
				must(t, syscall.Mkfifo(c.KeyPath, 0600))
			case "short seed":
				must(t, os.WriteFile(c.KeyPath, make([]byte, ed25519.SeedSize-1), 0600))
			case "long seed":
				must(t, os.WriteFile(c.KeyPath, make([]byte, ed25519.SeedSize+1), 0600))
			case "traversal":
				c.Directory += "/../state"
			}
			if _, err := Open(c); err == nil {
				t.Fatal("opened unsafe store")
			}
		})
	}
}

func TestTypedCapacityAfterRestart(t *testing.T) {
	c := config(t, testKey("capacity"))
	c.ReceiptLimit = 2
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	r, err := w.Administration().Enroll(t.Context(), q)
	must(t, err)
	a, err := cp.VerifyReceipt(c.Pin, r)
	must(t, err)
	_, err = w.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{2})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatal(err)
	}
	must(t, w.Close())
	w = reopen(t, c)
	w.Inhibit(false)
	_, err = w.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{3})
	var capErr *CapacityError
	if !errors.As(err, &capErr) || capErr.Limit != 2 || !errors.Is(err, cp.ErrUnavailable) {
		t.Fatal(err)
	}
	_, err = w.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{2})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatal(err)
	}
	retired, err := w.Administration().Retire(t.Context(), cp.Retirement{Mutation: cp.Mutation{ID: cp.ID{4}, Expected: a.State}, DispositionDigest: a.State.Head.Digest})
	must(t, err)
	must(t, w.Close())
	w = reopen(t, c)
	prior, err := w.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{4})
	must(t, err)
	if cp.ReceiptDigest(prior) != cp.ReceiptDigest(retired) {
		t.Fatal("lost reserved retirement receipt")
	}
	v, err := w.Administration().Read(context.Background(), cp.ReadRequest{Slot: q.Slot, Challenge: cp.ID{5}})
	if !errors.Is(err, cp.ErrUnavailable) || v.Current.Status != cp.Retired {
		t.Fatalf("restored guard/retirement: %v %+v", err, v.Current)
	}
}

func TestServiceOwnership(t *testing.T) {
	uid := uint32(os.Geteuid())
	for _, isDir := range []bool{true, false} {
		mode := uint32(syscall.S_IFREG | 0600)
		if isDir {
			mode = syscall.S_IFDIR | 0700
		}
		st := syscall.Stat_t{Mode: mode, Nlink: 1, Uid: uid}
		must(t, checkStat(st, isDir, uid))
		st.Uid ^= 1
		if err := checkStat(st, isDir, uid); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("foreign uid accepted, directory=%v", isDir)
		}
	}
}

func TestTraverseOnlyAncestor(t *testing.T) {
	c := config(t, testKey("traversal"))
	parent := filepath.Dir(c.Directory)
	must(t, os.Chmod(parent, 0100))
	defer func() { must(t, os.Chmod(parent, 0700)) }()
	w := create(t, c)
	must(t, w.Close())
	reopen(t, c)
}

func TestRestoreWholeDirectory(t *testing.T) {
	c := config(t, testKey("restore"))
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	enroll, err := w.Administration().Enroll(t.Context(), q)
	must(t, err)
	old, err := cp.VerifyReceipt(c.Pin, enroll)
	must(t, err)
	// Copy a quiescent, complete pair at enrollment. Keep the later receipt
	// outside this backup, as a surviving home would.
	backup := c
	backup.Directory = filepath.Join(t.TempDir(), "snapshot")
	must(t, os.Mkdir(backup.Directory, 0700))
	for _, name := range []string{journalName, lockName} {
		must(t, os.WriteFile(filepath.Join(backup.Directory, name), readFile(t, filepath.Join(c.Directory, name)), 0600))
	}
	retired, err := w.Administration().Retire(t.Context(), cp.Retirement{Mutation: cp.Mutation{ID: cp.ID{3}, Expected: old.State}, DispositionDigest: old.State.Head.Digest})
	must(t, err)
	external, err := cp.VerifyReceipt(c.Pin, retired)
	must(t, err)
	must(t, w.Close())
	restored := reopen(t, backup)
	read := cp.ReadRequest{Slot: q.Slot, Challenge: cp.ID{4}, ReceiptID: external.ID}
	v, err := restored.Administration().Read(t.Context(), read)
	if !errors.Is(err, cp.ErrUnavailable) || !errors.Is(cp.VerifyView(c.Pin, read, v), cp.ErrUnavailable) {
		t.Fatal("restore must start inhibited", err)
	}
	if v.Current != old.State || v.Prior != nil || external.State.Epoch != v.Current.Epoch || external.State.Revision <= v.Current.Revision {
		t.Fatal("expected independent receipt ahead of authentic older state")
	}
}

func TestAckSigningBarrier(t *testing.T) {
	c := config(t, testKey("signing order"))
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	var stage string
	w.fault = func(s string) error { stage = s; return nil }
	sign := w.sign
	var signed bool
	w.sign = func(b []byte) ([]byte, error) {
		if stage != "directory-synced" {
			t.Fatalf("signed at %q", stage)
		}
		signed = true
		return sign(b)
	}
	_, err := w.Administration().Enroll(t.Context(), q)
	must(t, err)
	if !signed || stage != "receipt-directory-synced" {
		t.Fatalf("signed=%v, returned at %q", signed, stage)
	}
}

func TestCancelledWaitForCommit(t *testing.T) {
	c := config(t, testKey("wait"))
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	reached, release := make(chan struct{}), make(chan struct{})
	w.fault = func(stage string) error {
		if stage == "appended" {
			close(reached)
			<-release
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := w.Administration().Enroll(t.Context(), q); done <- err }()
	<-reached
	defer func() { close(release); must(t, <-done) }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := w.Administration().Settle(ctx, 1, q.Slot, q.ID); !errors.Is(err, cp.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := w.SignDisposition(ctx, [32]byte{}, "granted"); !errors.Is(err, cp.ErrUnavailable) {
		t.Fatal(err)
	}
}

// Both fixtures use exactly the same key, identity and enrollment. Only the
// number of earlier, independently valid journal transitions differs.
func TestReviewReceiptPosition(t *testing.T) {
	enrollAt := func(extraFence bool) (uint64, cp.Receipt) {
		c := config(t, testKey("review position"))
		w := create(t, c)
		q := fixtureEnrollment(t, w)
		if extraFence {
			_, err := w.Administration().Settle(t.Context(), c.Identity.Epoch, q.Slot, cp.ID{9})
			if !errors.Is(err, cp.ErrNotApplied) {
				t.Fatal(err)
			}
		}
		r, err := w.Administration().Enroll(t.Context(), q)
		must(t, err)
		a, err := cp.VerifyReceipt(c.Pin, r)
		must(t, err)
		if a.State.JournalSequence != w.seq {
			t.Fatalf("signed position %d, journal position %d", a.State.JournalSequence, w.seq)
		}
		return w.seq, r
	}
	p, a := enrollAt(false)
	q, b := enrollAt(true)
	if p == q {
		t.Fatal("fixture did not distinguish journal positions")
	}
	if bytes.Equal(a.Body, b.Body) {
		t.Fatalf("journal positions %d and %d have identical signed receipt bodies (signatures identical=%t)", p, q, bytes.Equal(a.Signature, b.Signature))
	}
}
