package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/enrollment"
	"ataniq/internal/seatgate"
)

// Only fixed operator diagnoses cross the command's diagnostic boundary.
type enrollmentRefusal string

func (e enrollmentRefusal) Error() string { return string(e) }

const errReceiptExists enrollmentRefusal = "receipt-out already exists; choose a new file"

func transferPath(name string, c config) (string, error) {
	path, err := filepath.Abs(name)
	if err != nil {
		return "", errFiles
	}
	if path == c.Seed || path == filepath.Join(c.ServiceTLS, seatgate.KeyFile) || path == filepath.Join(c.OwnerTLS, seatgate.KeyFile) ||
		path == c.State || strings.HasPrefix(path, c.State+"/") {
		return "", enrollmentRefusal("transfer file overlaps witness state or a private key")
	}
	return path, nil
}

func checkPrepared(cmd command, c config, store durable.Config, out io.Writer) (enrollment.Prepared, error) {
	var p enrollment.Prepared
	path, err := transferPath(cmd.prepared, c)
	if err != nil {
		return p, err
	}
	raw, err := readFile(path, 192<<10, false)
	if err != nil {
		return p, enrollmentRefusal("cannot read Prepared as a protected regular file of at most 192 KiB")
	}
	p, err = enrollment.DecodePrepared(raw)
	if err != nil {
		return p, enrollmentRefusal("invalid signed Prepared document")
	}
	sum := sha256.Sum256(raw)
	if _, err = fmt.Fprintf(out, "prepared_digest=%x\n", sum); err != nil {
		return p, err
	}
	if cmd.expectDigest != "" {
		want, err := hex.DecodeString(cmd.expectDigest)
		if err != nil || !bytes.Equal(sum[:], want) {
			return p, enrollmentRefusal("Prepared digest mismatch: compare --expect-digest with the digest read on the home device")
		}
	}
	intent, err := cp.ParseIntent("enroll", "", p.Intent)
	if err != nil {
		return p, err
	}
	q := intent.(cp.Enrollment)
	switch {
	case q.Slot.Witness != c.Slot.Witness:
		return p, enrollmentRefusal("Prepared slot.witness mismatch")
	case q.Slot.Authority != c.Slot.Authority:
		return p, enrollmentRefusal("Prepared slot.authority mismatch")
	case q.Slot.Store != c.Slot.Store:
		return p, enrollmentRefusal("Prepared slot.store mismatch")
	case p.Epoch != c.Identity.Epoch:
		return p, enrollmentRefusal("Prepared epoch mismatch")
	case !bytes.Equal(p.WitnessKey, store.Pin):
		return p, enrollmentRefusal("Prepared witness_key mismatch")
	case !bytes.Equal(q.HomePublicKey, store.HomePin):
		return p, enrollmentRefusal("Prepared home_public_key mismatch")
	}
	return p, nil
}

func enrollSubmit(ctx context.Context, c config, store durable.Config, cmd command, out io.Writer, connect func(config, durable.Config) (cp.RecoveryAdministration, error)) error {
	p, err := checkPrepared(cmd, c, store, out)
	if err != nil {
		return err
	}
	path, err := transferPath(cmd.receiptOut, c)
	if err != nil {
		return err
	}
	// Refuse an occupied name before mutation. WriteReceipt enforces
	// exclusive creation even if another call wins the race after this check.
	if _, err := os.Lstat(path); err == nil {
		return errReceiptExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return errFiles
	}
	admin, err := connect(c, store)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, ownerTimeout)
	defer cancel()
	raw, err := enrollment.Submit(ctx, admin, p)
	if err != nil {
		return err
	}
	digest, err := enrollment.WriteReceipt(path, raw)
	if errors.Is(err, os.ErrExist) {
		return errReceiptExists
	}
	if err != nil {
		return enrollmentRefusal("could not publish signed receipt. Replay the same Prepared to a new receipt-out file")
	}
	if _, err = fmt.Fprintf(out, "receipt_digest=%s\n", digest); err != nil {
		return err
	}
	return printStatus(ctx, admin, store.Pin, c.Identity.Epoch, out)
}
