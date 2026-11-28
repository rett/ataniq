package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ataniq/internal/assignment"
	"ataniq/internal/enrollment"
	"ataniq/internal/seatgate"
)

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func status(c config, out io.Writer) error {
	seed, err := exists(c.HomeKey)
	if err != nil {
		return err
	}
	tlsKey, err := exists(filepath.Join(c.TLSDir, seatgate.KeyFile))
	if err != nil {
		return err
	}
	cert, err := exists(filepath.Join(c.TLSDir, seatgate.CertFile))
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "home_key=%t tls_key=%t tls_cert=%t\n", seed, tlsKey, cert); err != nil {
		return err
	}
	var home ed25519.PublicKey
	if seed {
		key, err := loadHomeKey(c)
		if err != nil {
			return err
		}
		home = key.Public().(ed25519.PublicKey)
		clear(key)
	}
	state, binding, bindingErr := bindingStatus(c, home)
	prepared, head, replayErr := enrollment.ReadLocalStatus(enrollment.FileRoot{Path: c.State}, home, binding)
	if _, err := fmt.Fprintf(out, "prepared=%t binding=%s\n", prepared, state); err != nil {
		return err
	}
	if replayErr == nil {
		if head == nil {
			_, err = fmt.Fprintln(out, "ledger=absent")
		} else {
			_, err = fmt.Fprintf(out, "ledger_head_sequence=%d\n", head.Sequence)
		}
	}
	return errors.Join(err, bindingErr, replayErr, pilotStatus(c, head, out))
}

func bindingStatus(c config, home ed25519.PublicKey) (string, *enrollment.Binding, error) {
	if c.Binding == "" {
		return "unconfigured", nil, nil
	}
	present, err := exists(c.Binding)
	if err != nil {
		return "unknown", nil, err
	}
	if !present {
		return "absent", nil, nil
	}
	b, err := enrollment.ReadBinding(c.Binding)
	if err != nil {
		return "invalid", nil, fmt.Errorf("binding: %w", err)
	}
	if b.RootPath != c.State || b.Slot != c.Slot || b.Epoch != c.Epoch || !bytes.Equal(b.HomePin, home) || hex.EncodeToString(b.WitnessPin) != c.WitnessPublicKey {
		return "invalid", nil, errors.New("binding differs from the configured ledger root, slot, epoch or keys")
	}
	return "valid", &b, nil
}

func pilotStatus(c config, head *assignment.Checkpoint, out io.Writer) error {
	retired, err := isRetired(c)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "retired=%t\n", retired); err != nil {
		return err
	}
	path := filepath.Join(c.State, observationFile)
	present, err := exists(path)
	if err != nil {
		return err
	}
	if !present {
		_, err := fmt.Fprintln(out, "last_observation=none")
		return err
	}
	raw, err := readFile(path, 4096, true)
	if err != nil {
		return fmt.Errorf("observation.json: %w", err)
	}
	var refusal refusedObservation
	if err := json.Unmarshal(raw, &refusal); err == nil && refusal.valid() {
		canonical, err := json.Marshal(refusal)
		if err != nil || !bytes.Equal(raw, canonical) {
			return errors.New("invalid observation.json")
		}
		_, err = fmt.Fprintln(out, "last_observation=refused:", refusal.Reason)
		return err
	}
	var o observation
	if err := json.Unmarshal(raw, &o); err != nil {
		return fmt.Errorf("observation.json: %w", err)
	}
	canonical, err := json.Marshal(o)
	if err != nil || !bytes.Equal(raw, canonical) || !o.Home.valid() || !o.Witness.valid() || !o.Fresh.valid() ||
		(o.Archive != nil && (!o.Archive.valid() || o.ArchiveInvalid)) || o.Result != o.verdict() {
		return errors.New("invalid observation.json")
	}
	result := o.Result
	switch {
	case head == nil:
		result += " (current ledger head unavailable)"
	case o.Home != headOf(*head):
		result += " (stale: ledger head moved)"
	}
	_, err = fmt.Fprintln(out, "last_observation="+result)
	return err
}
