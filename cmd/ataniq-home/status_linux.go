package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	return errors.Join(err, bindingErr, replayErr)
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
