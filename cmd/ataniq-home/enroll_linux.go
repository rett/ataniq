package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/enrollment"
)

// Resume and finalize can be presented with an authentic request from a
// different pairing. Check the current configuration before exporting or
// committing that request.
func matchPrepared(p enrollment.Prepared, c config, keys pairing) error {
	intent, err := cp.ParseIntent("enroll", "", p.Intent)
	if err != nil {
		return err
	}
	q := intent.(cp.Enrollment)
	if q.Slot != c.Slot || p.Epoch != c.Epoch || !bytes.Equal(p.WitnessKey, keys.witness) || !bytes.Equal(q.HomePublicKey, keys.home.key.Public().(ed25519.PublicKey)) {
		return errors.New("prepared request differs from the configured slot, epoch or keys")
	}
	return nil
}

func enroll(ctx context.Context, cmd command, c config, keys pairing, out io.Writer) error {
	root := enrollment.FileRoot{Path: c.State}
	if cmd.op == "finalize" {
		return finalize(ctx, cmd, root, c, keys, out)
	}
	path, err := filepath.Abs(cmd.output)
	if err != nil {
		return err
	}
	var p enrollment.Prepared
	if cmd.op == "prepare" {
		p, err = enrollment.Prepare(ctx, root, c.Slot, c.Epoch, keys.home.key, keys.witness, nil)
	} else {
		p, err = enrollment.ResumePrepared(ctx, root)
	}
	if err != nil {
		return err
	}
	if err := matchPrepared(p, c, keys); err != nil {
		return err
	}
	digest, err := enrollment.WritePrepared(path, p)
	if err != nil {
		return fmt.Errorf("out: %w", err)
	}
	_, err = fmt.Fprintf(out, "prepared_sha256=%s\n", digest)
	return err
}

func readTransfer(path string, limit int64) ([]byte, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return readFile(abs, limit, false)
}

func finalize(ctx context.Context, cmd command, root enrollment.FileRoot, c config, keys pairing, out io.Writer) error {
	if c.Binding == "" {
		return errors.New("binding: configure a separate installation record before finalizing")
	}
	raw, err := readTransfer(cmd.prepared, 192<<10)
	if err != nil {
		return fmt.Errorf("prepared: %w", err)
	}
	p, err := enrollment.DecodePrepared(raw)
	if err != nil {
		return fmt.Errorf("prepared: %w", err)
	}
	if err := matchPrepared(p, c, keys); err != nil {
		return err
	}
	receipt, err := readTransfer(cmd.receipt, cp.MaxFrame+517)
	if err != nil {
		return fmt.Errorf("receipt: %w", err)
	}
	b, err := enrollment.Finalize(ctx, root, p, receipt, keys.witness)
	if err != nil {
		return err
	}
	err = enrollment.WriteBinding(c.Binding, b)
	if errors.Is(err, os.ErrExist) {
		// A prior successful finalize may have lost its return. Only the
		// identical validated binding is a successful retry.
		saved, readErr := enrollment.ReadBinding(c.Binding)
		if readErr == nil && reflect.DeepEqual(saved, b) {
			err = nil
		}
	}
	if err != nil {
		return fmt.Errorf("binding: %w", err)
	}
	_, err = fmt.Fprintf(out, "ledger_id=%s enrollment_id=%x receipt_digest=%s\n", b.Header.LedgerID, b.EnrollmentID, cp.ReceiptDigest(b.Receipt))
	return err
}
