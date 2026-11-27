package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"

	"ataniq/internal/checkpoint/link"
)

func check(c config) error {
	home, err := loadIdentity(c)
	if err != nil {
		return err
	}
	if c.WitnessTLSPin == home.tls.Fingerprint() {
		return errors.New("witness_tls_pin: must differ from the home TLS pin")
	}
	witnessKey, err := hex.DecodeString(c.WitnessPublicKey)
	if err != nil || bytes.Equal(witnessKey, home.publicKey) {
		return errors.New("witness_public_key: expected a witness key distinct from the home ledger key")
	}
	der, err := readFile(c.WitnessCert, 64<<10, false)
	if err != nil {
		return fmt.Errorf("witness_cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return errors.New("witness_cert: expected one DER X.509 certificate")
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	if hex.EncodeToString(sum[:]) != c.WitnessTLSPin {
		return errors.New("witness_cert: SPKI fingerprint differs from witness_tls_pin")
	}
	// Exercise the same endpoint checks that precede serving, including
	// TrustSources normalization. Construction is entirely in memory.
	_, err = link.NewEndpoint(home.tls, der, witnessKey, c.Epoch, c.Slot, c.TrustSources)
	if err != nil {
		return fmt.Errorf("witness_cert, witness_public_key, epoch, slot or trust_sources: %w", err)
	}
	return nil
}
