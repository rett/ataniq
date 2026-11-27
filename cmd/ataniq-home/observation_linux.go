package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ataniq/internal/assignment"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/enrollment"
	"golang.org/x/sys/unix"
)

type observedHead struct {
	Digest   string `json:"head"`
	Sequence uint64 `json:"sequence"`
}

func headOf(h assignment.Checkpoint) observedHead {
	return observedHead{Sequence: h.Sequence, Digest: h.Digest}
}
func (h observedHead) String() string {
	return fmt.Sprintf("sequence=%d head=%s", h.Sequence, h.Digest)
}
func (h observedHead) valid() bool {
	return len(h.Digest) == 64 && strings.Trim(h.Digest, "0123456789abcdef") == ""
}

func readArchive(path string) (*observedHead, error) {
	raw, err := readTransfer(path, 128)
	if err != nil {
		return nil, err
	}
	seq, digest, _ := strings.Cut(strings.TrimSuffix(string(raw), "\n"), " head=")
	n, err := strconv.ParseUint(strings.TrimPrefix(seq, "sequence="), 10, 64)
	h := observedHead{Sequence: n, Digest: digest}
	if err != nil || !h.valid() || string(raw) != h.String()+"\n" {
		return nil, errors.New("expected one line: sequence=N head=HEX")
	}
	return &h, nil
}

// Fixed-schema compact JSON with keys in lexical order.
type observation struct {
	Archive          *observedHead `json:"archive"`
	ArchiveInvalid   bool          `json:"archive_invalid"`
	Fresh            observedHead  `json:"fresh"`
	Home             observedHead  `json:"home"`
	ReceiptConfirmed bool          `json:"receipt_confirmed"`
	Result           string        `json:"result"`
	Witness          observedHead  `json:"witness"`
}

func (o observation) verdict() string {
	var diffs []string
	if o.Home != o.Witness {
		diffs = append(diffs, "home/witness receipt")
	}
	if !o.ReceiptConfirmed || o.Fresh != o.Witness {
		diffs = append(diffs, "witness receipt/challenged read")
	}
	if o.ArchiveInvalid {
		diffs = append(diffs, "archive unreadable or invalid")
	}
	if o.Archive != nil {
		if *o.Archive != o.Home {
			diffs = append(diffs, "home/archive")
		}
		if *o.Archive != o.Witness {
			diffs = append(diffs, "witness receipt/archive")
		}
	}
	if len(diffs) == 0 {
		return "agree"
	}
	return "DISAGREE " + strings.Join(diffs, ", ")
}

func compareObservation(ctx context.Context, archive string, root enrollment.FileRoot, b enrollment.Binding, store *enrollment.Store, client cp.Client, out io.Writer) error {
	_, local, err := enrollment.ReadLocalStatus(root, b.HomePin, &b)
	if err != nil {
		return err
	}
	r, err := store.LastReceipt()
	if err != nil {
		return err
	}
	if r == nil {
		return errors.New("no retained advance receipt; observe at least one event first")
	}
	ack, err := cp.VerifyReceipt(b.WitnessPin, r.Receipt)
	if err != nil {
		return err
	}
	q := cp.ReadRequest{Slot: b.Slot, ReceiptID: r.ID}
	rand.Read(q.Challenge[:])
	view, err := client.Read(ctx, q)
	if err != nil {
		return err
	}
	if err := cp.VerifyView(b.WitnessPin, q, view); err != nil {
		return err
	}
	o := observation{Home: headOf(*local), Witness: headOf(ack.State.Head), Fresh: headOf(view.Current.Head)}
	o.ReceiptConfirmed = view.Current.Header == b.Header && view.Current.Epoch == b.Epoch &&
		cp.Compare(ack.State.Head, view.Current, ack.State.JournalSequence) == cp.Equal &&
		view.Prior != nil && cp.ReceiptDigest(*view.Prior) == cp.ReceiptDigest(r.Receipt)
	archiveText := "absent"
	if archive != "" {
		o.Archive, err = readArchive(archive)
		if err != nil {
			o.ArchiveInvalid = true
			archiveText = "invalid"
		} else {
			archiveText = o.Archive.String()
		}
	}
	o.Result = o.verdict()
	if err := saveObservation(root.Path, o); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "home %s\nwitness %s (challenged %s)\narchive %s\nobservation: %s\nmanaged_starts=0 launches=0\n", o.Home, o.Witness, o.Fresh, archiveText, o.Result); err != nil {
		return err
	}
	if o.Result != "agree" {
		return errors.New("observation evidence differs; preserve the records for owner comparison")
	}
	return nil
}

const observationFile = "observation.json"

// A refused startup has no three-way evidence to publish.
type refusedObservation struct {
	Reason enrollment.Reason `json:"refusal"`
}

func (r refusedObservation) valid() bool {
	switch r.Reason {
	case enrollment.HomeBehind, enrollment.HomeAhead, enrollment.Fork, enrollment.Retired:
		return true
	}
	return false
}

func recordObserveError(state string, err error) error {
	var refusal refusedObservation
	var e *enrollment.Error
	switch {
	case errors.Is(err, errRetired), errors.Is(err, cp.ErrRetired):
		refusal.Reason = enrollment.Retired
	case errors.As(err, &e):
		refusal.Reason = e.Reason
	}
	if refusal.valid() {
		return errors.Join(err, saveObservation(state, refusal))
	}
	return err
}

// Pin the private state directory for creation, publication and directory sync.
func saveObservation(state string, o any) error {
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	dir, err := directory(state, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	fd := int(dir.Fd())
	f, err := os.CreateTemp(fmt.Sprintf("/proc/self/fd/%d", fd), ".observation-*")
	if err != nil {
		return err
	}
	name := filepath.Base(f.Name())
	defer unix.Unlinkat(fd, name, 0)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err := unix.Renameat(fd, name, fd, observationFile); err != nil {
		return err
	}
	return syncDirectory(fd)
}
