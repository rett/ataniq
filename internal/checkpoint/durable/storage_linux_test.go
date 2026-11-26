package durable

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/iotest"
	"time"

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
	c := Config{HomePin: public(testKey("fixture home")), Directory: root, KeyPath: filepath.Join(filepath.Dir(root), "key"), Pin: public(testKey("process")), Identity: Identity{"witness", "store", "incarnation", 1}}
	if stage == "published-"+lockName {
		_, err := start(c, createStore, func(s string) error {
			if s == stage {
				os.Exit(79)
			}
			return nil
		})
		t.Fatalf("missed publication boundary: %v", err)
	}
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
		if s == strings.TrimPrefix(stage, "settle-") {
			os.Exit(79)
		}
		return nil
	}
	if strings.HasPrefix(stage, "settle-") {
		_, err = w.Administration().Settle(t.Context(), 1, q.Slot, q.ID)
	} else {
		_, err = w.Administration().Enroll(t.Context(), q)
	}
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
			must(t, recovered.Administration().AcknowledgeStorageError(t.Context()))
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
	requirePoison(t, c)
	again := reopen(t, c)
	original, err := again.Administration().Settle(t.Context(), 1, q.Slot, q.ID)
	must(t, err)
	for i := 0; i < 2; i++ {
		again.Inhibit(false)
		_, err = again.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{9})
		if !errors.Is(err, cp.ErrUnavailable) {
			t.Fatalf("ordinary guard cleared storage inhibition: %v", err)
		}
		saved, err := again.Administration().Settle(t.Context(), 1, q.Slot, q.ID)
		must(t, err)
		if !bytes.Equal(saved.Signature, original.Signature) || !bytes.Equal(saved.Body, original.Body) {
			t.Fatal("inhibition changed receipt")
		}
		must(t, again.Close())
		again = reopen(t, c)
	}
	// Acknowledgement clears only storage inhibition. The startup guard remains.
	must(t, again.Administration().AcknowledgeStorageError(t.Context()))
	_, err = again.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{9})
	if !errors.Is(err, cp.ErrUnavailable) {
		t.Fatal("acknowledgement bypassed service guard", err)
	}
	again.Inhibit(false)
	_, err = again.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{9})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatal("owner acknowledgement did not restore admission", err)
	}
	must(t, again.Close())
	again = reopen(t, c)
	again.Inhibit(false)
	_, err = again.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{10})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatal("clean restart lost acknowledgement", err)
	}
}

func TestJournalStorageFaults(t *testing.T) {
	for site, name := range []string{"partial write", "journal sync", "journal directory sync", "index sync", "index directory sync"} {
		t.Run(name, func(t *testing.T) {
			c := config(t, testKey("storage faults"))
			w := create(t, c)
			q := fixtureEnrollment(t, w)
			expected := syscall.EIO
			var partial []byte
			path := filepath.Join(c.Directory, journalName)
			before := readFile(t, path)
			if site == 0 {
				expected = syscall.ENOSPC
				w.writeFrame = func(frame []byte) (int, error) {
					partial = bytes.Clone(frame[:len(frame)/2])
					n, err := w.journal.Write(partial)
					must(t, err)
					return n, syscall.ENOSPC
				}
			} else {
				files := []*os.File{w.journal, w.dir, w.lockFile, w.dir}
				calls := 0
				w.syncFile = func(f *os.File) error {
					if f != files[calls] {
						t.Fatalf("unexpected sync at site %d", calls+1)
					}
					calls++
					if calls == site {
						return syscall.EIO
					}
					return f.Sync()
				}
			}
			r, err := w.Administration().Enroll(t.Context(), q)
			if !errors.Is(err, expected) || !errors.Is(err, cp.ErrUnknownOutcome) || r.Body != nil || r.Signature != nil {
				t.Fatalf("storage failure returned receipt or wrong outcome: %+v, %v", r, err)
			}
			if _, err := w.SignDisposition(t.Context(), [32]byte{}, "granted"); !errors.Is(err, cp.ErrUnavailable) {
				t.Fatal("poisoned signer available", err)
			}
			if err := w.Administration().AcknowledgeStorageError(t.Context()); !errors.Is(err, cp.ErrUnavailable) {
				t.Fatal("poisoned handle acknowledged", err)
			}
			must(t, w.Close())
			requirePoison(t, c)
			if site == 0 {
				if !bytes.Equal(readFile(t, path), before) {
					t.Fatal("short write left a partial journal frame")
				}
				again := reopen(t, c)
				found, err := again.HasReceipt(t.Context(), q.Slot, q.ID)
				must(t, err)
				if found {
					t.Fatal("rolled back write became a receipt")
				}
				return
			}
			next := reopen(t, c)
			next.Inhibit(false)
			historical, err := next.Administration().Settle(t.Context(), 1, q.Slot, q.ID)
			must(t, err)
			ack, err := cp.VerifyReceipt(c.Pin, historical)
			must(t, err)
			if ack.State.JournalSequence != 2 {
				t.Fatal("replay changed receipt position")
			}
			_, err = next.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{3})
			if !errors.Is(err, cp.ErrUnavailable) {
				t.Fatal("new transition after storage error", err)
			}
		})
	}
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
	middleLength := bytes.Clone(original)
	binary.BigEndian.PutUint32(middleLength[genesis:], maxRecord)
	damagedSig := bytes.Clone(original)
	damagedSig[len(damagedSig)-1] ^= 0x80
	wrongReceipt := bytes.Clone(anchors)
	wrongReceipt[len(wrongReceipt)-35] ^= 0x80
	for _, tc := range []struct {
		name            string
		frames, entries []byte
		want            error
	}{
		{"empty", nil, anchors, ErrCorrupt}, {"short prefix", original[:2], anchors, ErrCorrupt},
		{"middle length", middleLength, anchors, ErrCorrupt},
		{"zero tail", append(bytes.Clone(original), make([]byte, 8192)...), anchors, ErrTorn},
		{"nonzero after zeros", append(append(bytes.Clone(original), make([]byte, 8192)...), 1), anchors, ErrCorrupt},
		{"unindexed partial frame", original[:len(original)-1], anchors[:len(anchors)-34], ErrTorn},
		{"partial frame", original[:len(original)-1], anchors, ErrCorrupt}, {"whole frame removed", original[:enrolled], anchors, ErrCorrupt},
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
		func(c *Config) { c.HomePin = public(testKey("foreign")) }, func(c *Config) { c.Identity.Epoch++ }, func(c *Config) { c.Pin = public(testKey("other")) },
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
	must(t, w.Grant(q.Slot, q.HomePublicKey, true))
	_, err = w.SettleHome(t.Context(), 1, q.Slot, cp.ID{3}, q.HomePublicKey)
	var capErr *CapacityError
	if !errors.As(err, &capErr) || capErr.Limit != 2 || !errors.Is(err, cp.ErrCapacity) {
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

func TestHomePinAndOwnerAllowance(t *testing.T) {
	c := config(t, testKey("home isolation"))
	c.ReceiptLimit = 1
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	foreignKey := testKey("foreign")
	foreign := public(foreignKey)
	other := q
	other.HomePublicKey = foreign
	other.ProofOfPossession, _ = cp.ProveEnrollment(foreignKey, other)
	for name, request := range map[string]func() error{
		"grant":   func() error { return w.Grant(q.Slot, foreign, true) },
		"connect": func() error { _, err := w.Connect(q.Slot, foreignKey, testKey("session"), c.Pin); return err },
		"check":   func() error { return w.CheckHome(t.Context(), q.Slot, foreign) },
		"read": func() error {
			_, err := w.ReadHome(t.Context(), cp.ReadRequest{Slot: q.Slot, Challenge: cp.ID{2}}, foreign)
			return err
		},
		"settle": func() error { _, err := w.SettleHome(t.Context(), 1, q.Slot, q.ID, foreign); return err },
		"enroll": func() error { _, err := w.Administration().Enroll(t.Context(), other); return err },
		"submit": func() error { _, err := w.RawSubmit(t.Context(), q.Slot, foreign, "claim", nil, nil, nil); return err },
	} {
		t.Run(name, func(t *testing.T) {
			var reason *cp.Reason
			if err := request(); !errors.As(err, &reason) || reason.Kind != cp.HomeMismatch {
				t.Fatalf("foreign key: %v", err)
			}
		})
	}

	must(t, w.Grant(q.Slot, q.HomePublicKey, true))
	_, err := w.Administration().Enroll(t.Context(), q)
	must(t, err)
	_, err = w.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{8})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatalf("owner could not settle at normal limit: %v", err)
	}
	must(t, w.Close())
	w = reopen(t, c)
	w.Inhibit(false)
	_, err = w.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{8})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatal("lost owner fence", err)
	}
	_, err = w.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{9})
	if !errors.Is(err, cp.ErrCapacity) {
		t.Fatal("reopen lost owner reserve accounting", err)
	}
	v, err := w.Administration().Read(t.Context(), cp.ReadRequest{Slot: q.Slot, Challenge: cp.ID{2}})
	must(t, err)
	_, err = w.Administration().Retire(t.Context(), cp.Retirement{Mutation: cp.Mutation{Expected: v.Current, ID: cp.ID{10}}, DispositionDigest: v.Current.Head.Digest})
	must(t, err)
}

func requirePoison(t *testing.T, c Config) {
	t.Helper()
	if w, err := Open(c); err == nil {
		w.Close()
		t.Fatal("Open served after reported I/O error")
	} else if !errors.Is(err, ErrPoisoned) {
		t.Fatal("missing typed storage refusal", err)
	}
	must(t, os.Remove(filepath.Join(c.Directory, "poisoned")))
}

func TestOpenRewritesTails(t *testing.T) {
	c := config(t, testKey("rewrite"))
	w := create(t, c)
	must(t, w.Close())
	stale := time.Unix(946684800, 0)
	for _, name := range []string{journalName, lockName} {
		path := filepath.Join(c.Directory, name)
		must(t, os.Chtimes(path, stale, stale))
	}
	reopen(t, c)
	for _, name := range []string{journalName, lockName} {
		info, err := os.Stat(filepath.Join(c.Directory, name))
		must(t, err)
		if info.ModTime().Equal(stale) {
			t.Errorf("Open did not rewrite %s", name)
		}
	}
}

func TestReserveBeforeAppend(t *testing.T) {
	c := config(t, testKey("blocks"))
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	receipt, err := w.Administration().Enroll(t.Context(), q)
	must(t, err)
	ack, err := cp.VerifyReceipt(c.Pin, receipt)
	must(t, err)
	before := readFile(t, filepath.Join(c.Directory, journalName))
	w.allocate = func(*os.File, int64, int64) error { return syscall.EDQUOT }
	_, err = w.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{7})
	var capacity *CapacityError
	if !errors.As(err, &capacity) || !errors.Is(err, syscall.EDQUOT) || !bytes.Equal(before, readFile(t, filepath.Join(c.Directory, journalName))) {
		t.Fatal("quota did not refuse before append", err)
	}
	_, err = w.Administration().Retire(t.Context(), cp.Retirement{Mutation: cp.Mutation{ID: cp.ID{8}, Expected: ack.State}, DispositionDigest: ack.State.Head.Digest})
	must(t, err)
}

func TestShortIndexWrite(t *testing.T) {
	c := config(t, testKey("index"))
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	path := filepath.Join(c.Directory, lockName)
	before := readFile(t, path)
	w.writeEntry = func(entry []byte) (int, error) { return w.lockFile.Write(entry[:7]) }
	_, err := w.Administration().Enroll(t.Context(), q)
	if !errors.Is(err, cp.ErrUnknownOutcome) || !bytes.Equal(before, readFile(t, path)) {
		t.Fatal("partial index after short write", err)
	}
	must(t, w.Close())
	requirePoison(t, c)
	again := reopen(t, c)
	_, err = again.Administration().Settle(t.Context(), 1, q.Slot, q.ID)
	must(t, err)
}

func TestReadSignsDirectoryPositionAfterFence(t *testing.T) {
	c := config(t, testKey("read head"))
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	_, err := w.Administration().Enroll(t.Context(), q)
	must(t, err)
	request := cp.ReadRequest{Slot: q.Slot, Challenge: cp.ID{11}}
	before, err := w.Administration().Read(t.Context(), request)
	must(t, err)
	_, err = w.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{12})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatal(err)
	}
	after, err := w.ReadHome(t.Context(), request, q.HomePublicKey)
	must(t, err)
	must(t, cp.VerifyView(c.Pin, request, after))
	var head struct {
		Seq    uint64 `json:"journal_sequence,string"`
		Digest string `json:"previous_digest"`
	}
	must(t, json.Unmarshal(after.Fresh.Body, &head))
	if before.Current != after.Current || bytes.Equal(before.Fresh.Body, after.Fresh.Body) || head.Seq != w.seq || head.Digest != hex.EncodeToString(w.previous[:]) {
		t.Fatalf("missing signed directory position after fence: %+v", head)
	}
}

func TestCopiedStoreWithSameKeyFile(t *testing.T) {
	c := config(t, testKey("shared file"))
	original := create(t, c)
	copyConfig := c
	copyConfig.Directory = t.TempDir()
	must(t, os.Chmod(copyConfig.Directory, 0700))
	for _, name := range []string{journalName, lockName} {
		must(t, os.WriteFile(filepath.Join(copyConfig.Directory, name), readFile(t, filepath.Join(c.Directory, name)), 0600))
	}
	if other, err := Open(copyConfig); !errors.Is(err, ErrLocked) {
		if other != nil {
			other.Close()
		}
		t.Fatal("copy served concurrently with same key file", err)
	}
	must(t, original.Close())
	reopen(t, copyConfig)
}

func TestSettleCrashBoundaries(t *testing.T) {
	for _, stage := range []string{"before-append", "appended", "journal-synced", "directory-synced", "receipt-appended", "receipt-synced", "receipt-directory-synced"} {
		t.Run(stage, func(t *testing.T) {
			c := config(t, testKey("process"))
			w := create(t, c)
			must(t, w.Close())
			child(t, c, "settle-"+stage, 79)
			w = reopen(t, c)
			q := fixtureEnrollment(t, w)
			must(t, w.Administration().AcknowledgeStorageError(t.Context()))
			_, err := w.Administration().Enroll(t.Context(), q)
			if stage == "before-append" {
				must(t, err)
			} else if !errors.Is(err, cp.ErrNotApplied) {
				t.Fatal("lost fence after restart", err)
			}
		})
	}
}
func TestSettleSyncBarrier(t *testing.T) {
	w := create(t, config(t, testKey("settle sync")))
	q := fixtureEnrollment(t, w)
	var last string
	w.fault = func(s string) error { last = s; return nil }
	_, err := w.Administration().Settle(t.Context(), 1, q.Slot, q.ID)
	if !errors.Is(err, cp.ErrNotApplied) || last != "receipt-directory-synced" {
		t.Fatalf("settlement returned at %q: %v", last, err)
	}
}

func TestCreateOwnerUmask(t *testing.T) {
	c := config(t, testKey("umask"))
	old := syscall.Umask(0777)
	defer syscall.Umask(old)
	w := create(t, c)
	must(t, w.Close())
	reopen(t, c)
}

func TestHalfMadeStore(t *testing.T) {
	for _, present := range []string{"", journalName, lockName} {
		t.Run("only "+present, func(t *testing.T) {
			c := config(t, testKey("half made"))
			if present != "" {
				must(t, os.WriteFile(filepath.Join(c.Directory, present), nil, 0600))
			}
			want := ErrNotInitialized
			if present != "" {
				want = ErrMissingJournal
				if present == journalName {
					want = ErrMissingIndex
				}
			}
			if _, err := Open(c); !errors.Is(err, want) {
				t.Fatalf("Open: %v", err)
			}
		})
	}
}

func TestConcurrentOpenDuringCreate(t *testing.T) {
	c := config(t, testKey("provisioning"))
	var wg sync.WaitGroup
	ready := make(chan struct{}, 8)
	stop := make(chan struct{})
	for range cap(ready) {
		wg.Go(func() {
			ready <- struct{}{}
			for {
				select {
				case <-stop:
					return
				default:
				}
				other, err := Open(c)
				if other != nil {
					other.Close()
					t.Error("opened unpublished store")
					return
				}
				if !errors.Is(err, ErrNotInitialized) && !errors.Is(err, ErrLocked) {
					t.Errorf("Open: %v", err)
					return
				}
			}
		})
	}
	for range cap(ready) {
		<-ready
	}
	w, err := Create(c)
	close(stop)
	wg.Wait()
	must(t, err)
	must(t, w.Close())
	reopen(t, c)
}

func TestReplayReadErrorBetweenFrames(t *testing.T) {
	c := config(t, testKey("read error"))
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	path := filepath.Join(c.Directory, journalName)
	genesis := len(readFile(t, path))
	_, err := w.Administration().Enroll(t.Context(), q)
	must(t, err)
	frames := readFile(t, path)
	// The first underlying read supplies exactly genesis. Lookahead then
	// sees one transient error before the reader can continue with Enroll.
	input := io.MultiReader(iotest.TimeoutReader(bytes.NewReader(frames[:genesis])), bytes.NewReader(frames[genesis:]))
	entries := bytes.NewReader(readFile(t, filepath.Join(c.Directory, lockName)))
	fresh := &Witness{dir: w.dir, journal: w.journal, lockFile: w.lockFile, pin: w.pin, homePin: w.homePin, keyID: w.keyID, identity: w.identity,
		slots: make(map[cp.Slot]*enrollment), history: make(map[item]accepted), syncFile: (*os.File).Sync}
	homeID, err := cp.KeyID(c.HomePin)
	must(t, err)
	if err = fresh.replay(homeID, input, entries); !errors.Is(err, iotest.ErrTimeout) {
		t.Fatalf("replay lost read error: %v", err)
	}
}

func TestCompleteInterruptedPublication(t *testing.T) {
	c := config(t, testKey("process"))
	child(t, c, "published-"+lockName, 79)
	for _, open := range []func(Config) (*Witness, error){Open, Create} {
		if _, err := open(c); !errors.Is(err, ErrPublication) {
			t.Fatal(err)
		}
	}
	matches, err := filepath.Glob(filepath.Join(c.Directory, ".create-*-"+journalName))
	must(t, err)
	if len(matches) != 1 {
		t.Fatal(matches)
	}
	before := readFile(t, matches[0])
	index := readFile(t, filepath.Join(c.Directory, lockName))
	dir, err := os.Open(c.Directory)
	must(t, err)
	must(t, exclusive(dir))
	if w, err := CompletePublication(c); w != nil || !errors.Is(err, ErrLocked) {
		t.Fatalf("completion while directory locked: %v", err)
	}
	must(t, dir.Close())
	w, err := CompletePublication(c)
	must(t, err)
	checkStatus := func(w *Witness, challenge cp.ID, want cp.Inhibition) {
		t.Helper()
		r, err := w.Administration().Status(t.Context(), challenge)
		must(t, err)
		s, err := cp.VerifyDirectoryStatus(c.Pin, c.Identity.Epoch, challenge, r)
		must(t, err)
		if s.Inhibition != want || s.Sequence != 1 || s.Digest != hex.EncodeToString(index[:32]) {
			t.Fatalf("status after completion: %+v, want %q at genesis", s, want)
		}
	}
	checkStatus(w, cp.ID{1}, cp.ServiceInhibited)
	w.Inhibit(false)
	checkStatus(w, cp.ID{2}, cp.Ready)
	must(t, w.Close())
	checkStatus(reopen(t, c), cp.ID{3}, cp.ServiceInhibited)
	if !bytes.Equal(before, readFile(t, filepath.Join(c.Directory, journalName))) ||
		!bytes.Equal(index, readFile(t, filepath.Join(c.Directory, lockName))) {
		t.Fatal("recovery changed genesis")
	}
}

func TestCompletePublicationRechecksPublishedPair(t *testing.T) {
	for _, name := range []string{journalName, lockName} {
		for _, change := range []string{"replace", "truncate", "extend"} {
			t.Run(name+"/"+change, func(t *testing.T) {
				c := config(t, testKey("process"))
				child(t, c, "published-"+lockName, 79)
				w, err := start(c, completeStore, func(stage string) error {
					if stage != "published-"+journalName {
						return nil
					}
					path := filepath.Join(c.Directory, name)
					switch change {
					case "replace":
						other := filepath.Join(c.Directory, "replacement")
						must(t, os.WriteFile(other, readFile(t, path), 0600))
						must(t, os.Rename(other, path))
					case "truncate":
						must(t, os.Truncate(path, 0))
					case "extend":
						must(t, os.WriteFile(path, append(readFile(t, path), 0), 0600))
					}
					return nil
				})
				if w != nil || !errors.Is(err, ErrPublication) {
					t.Fatalf("completed after %s of %s: %v", change, name, err)
				}
			})
		}
	}
}

func TestPreserveAcknowledgedPublication(t *testing.T) {
	for _, missing := range []string{lockName, journalName} {
		t.Run(missing, func(t *testing.T) {
			c := config(t, testKey("acknowledged"))
			w := create(t, c)
			q := fixtureEnrollment(t, w)
			_, err := w.Administration().Enroll(t.Context(), q)
			must(t, err)
			must(t, w.Close())
			survivor, want := journalName, ErrMissingIndex
			if missing == journalName {
				survivor, want = lockName, ErrMissingJournal
			}
			// Save the missing file outside the directory, then present it under
			// a staging name. Neither shape proves a genesis-only publication.
			path := filepath.Join(c.Directory, missing)
			saved := filepath.Join(filepath.Dir(c.Directory), "saved")
			must(t, os.Rename(path, saved))
			before := readFile(t, filepath.Join(c.Directory, survivor))
			for _, open := range []func(Config) (*Witness, error){Open, Create, CompletePublication} {
				if _, err := open(c); !errors.Is(err, want) {
					t.Fatal(err)
				}
			}
			must(t, os.Rename(saved, filepath.Join(c.Directory, ".create-test-"+missing)))
			if _, err := CompletePublication(c); !errors.Is(err, ErrPublication) {
				t.Fatal(err)
			}
			if !bytes.Equal(before, readFile(t, filepath.Join(c.Directory, survivor))) {
				t.Fatal("changed surviving file")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("published unproven file", err)
			}
		})
	}
}

func TestStatusBeforeEnrollment(t *testing.T) {
	c := config(t, testKey("status"))
	w := create(t, c)
	check := func(ch cp.ID, reason cp.Inhibition, slots, receipts, fences uint64) cp.DirectoryStatus {
		t.Helper()
		r, err := w.Administration().Status(t.Context(), ch)
		must(t, err)
		status, err := cp.VerifyDirectoryStatus(c.Pin, c.Identity.Epoch, ch, r)
		must(t, err)
		if status.Inhibition != reason || status.Sequence != 1+receipts+fences || status.Slots != slots ||
			status.Receipts != receipts || status.Fences != fences || status.Digest != hex.EncodeToString(w.previous[:]) {
			t.Fatal(status)
		}
		ch[31] ^= 1
		if _, err := cp.VerifyDirectoryStatus(c.Pin, c.Identity.Epoch, ch, r); err == nil {
			t.Fatal("accepted stale challenge")
		}
		return status
	}
	before := check(cp.ID{1}, cp.ServiceInhibited, 0, 0, 0)
	q := fixtureEnrollment(t, w)
	_, err := w.Administration().Settle(t.Context(), 1, q.Slot, cp.ID{9})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatal(err)
	}
	after := check(cp.ID{2}, cp.Ready, 0, 0, 1)
	if before.Digest == after.Digest {
		t.Fatal("fence did not advance directory head")
	}
	_, err = w.Administration().Enroll(t.Context(), q)
	must(t, err)
	check(cp.ID{3}, cp.Ready, 1, 1, 1)
	must(t, w.Close())
	must(t, os.WriteFile(filepath.Join(c.Directory, recoveryName), nil, 0600))
	w = reopen(t, c)
	check(cp.ID{4}, cp.UncleanEnd, 1, 1, 1)
}

func TestInspectInterruptedPublication(t *testing.T) {
	c := config(t, testKey("process"))
	child(t, c, "published-"+lockName, 79)
	w, err := OpenForRecovery(c)
	must(t, err)
	defer w.Close()
	ch := cp.ID{1}
	r, err := w.Administration().Status(t.Context(), ch)
	must(t, err)
	s, err := cp.VerifyDirectoryStatus(c.Pin, 1, ch, r)
	must(t, err)
	if s.Inhibition != cp.InterruptedPublication || s.Sequence != 1 {
		t.Fatal(s)
	}
	if err := w.Administration().AcknowledgeStorageError(t.Context()); !errors.Is(err, ErrPublication) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.Directory, journalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

// Genesis and its index are the unmodified output of Create at 4580bd22.
func TestLegacyGenesisFormat(t *testing.T) {
	c := config(t, testKey("legacy fixture"))
	for name, fixture := range map[string]string{journalName: "legacy-genesis", lockName: "legacy-index"} {
		must(t, os.WriteFile(filepath.Join(c.Directory, name), readFile(t, filepath.Join("testdata", fixture)), 0600))
	}
	_, err := Open(c)
	if !errors.Is(err, ErrLegacyFormat) || errors.Is(err, ErrIdentity) {
		t.Fatal("legacy genesis with matching pins", err)
	}
}

func TestDecodeErrorWithFinalBytes(t *testing.T) {
	c := config(t, testKey("reader"))
	w := create(t, c)
	must(t, w.Close())
	frame := readFile(t, filepath.Join(c.Directory, journalName))
	for _, end := range []error{io.EOF, syscall.EIO, io.ErrUnexpectedEOF} {
		in := iotest.DataErrReader(io.MultiReader(bytes.NewReader(frame), iotest.ErrReader(end)))
		_, _, err := decode(in, c.Pin)
		if end == io.EOF {
			must(t, err)
		} else if !errors.Is(err, end) || errors.Is(err, ErrTorn) {
			t.Errorf("final bytes with %v: got %v", end, err)
		}
	}
}

func TestReplayUnexpectedEOFInFrame(t *testing.T) {
	// The last read supplies three bytes of the four-byte length and its error.
	partial := func() io.Reader {
		return iotest.DataErrReader(io.MultiReader(bytes.NewReader([]byte{0, 0, 0}), iotest.ErrReader(io.ErrUnexpectedEOF)))
	}
	w := new(Witness)
	err := w.replay("", partial(), bytes.NewReader(nil))
	if !errors.Is(err, ErrTorn) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("unindexed partial frame: %v", err)
	}
	err = w.replay("", partial(), bytes.NewReader([]byte{1}))
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("index outlives partial frame: %v", err)
	}
}
