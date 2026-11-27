package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link"
	"ataniq/internal/enrollment"
)

type pairing struct {
	home    homeIdentity
	witness ed25519.PublicKey
	cert    []byte
}

func loadPairing(c config) (pairing, error) {
	home, err := loadIdentity(c)
	if err != nil {
		return pairing{}, err
	}
	witness, cert, err := loadWitness(c, home)
	if err != nil {
		clear(home.key)
		return pairing{}, err
	}
	return pairing{home, witness, cert}, nil
}

func loadWitness(c config, home homeIdentity) (ed25519.PublicKey, []byte, error) {
	if c.WitnessTLSPin == home.tls.Fingerprint() {
		return nil, nil, errors.New("witness_tls_pin: must differ from the home TLS pin")
	}
	key, err := hex.DecodeString(c.WitnessPublicKey)
	if err != nil || bytes.Equal(key, home.key.Public().(ed25519.PublicKey)) {
		return nil, nil, errors.New("witness_public_key: expected a witness key distinct from the home ledger key")
	}
	der, err := readFile(c.WitnessCert, 64<<10, false)
	if err != nil {
		return nil, nil, fmt.Errorf("witness_cert: %w", err)
	}
	const invalidCert = "witness_cert: expected one DER X.509 certificate or one PEM CERTIFICATE block"
	if bytes.HasPrefix(der, []byte("-----BEGIN ")) {
		// Decode can skip malformed text before a later valid block.
		end := bytes.TrimSuffix(bytes.TrimSuffix(der, []byte("\r\n")), []byte("\n"))
		if bytes.Count(der, []byte("-----BEGIN ")) != 1 || !bytes.HasSuffix(end, []byte("-----END CERTIFICATE-----")) {
			return nil, nil, errors.New(invalidCert)
		}
		block, rest := pem.Decode(der)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(rest) != 0 {
			return nil, nil, errors.New(invalidCert)
		}
		der = block.Bytes
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, errors.New(invalidCert)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	if hex.EncodeToString(sum[:]) != c.WitnessTLSPin {
		return nil, nil, errors.New("witness_cert: SPKI fingerprint differs from witness_tls_pin")
	}
	return key, der, nil
}

func homeEndpoint(c config, p pairing) (*link.Endpoint, error) {
	advance, err := enrollment.PendingForRestart(enrollment.FileRoot{Path: c.State}, c.Binding, c.Slot, c.Epoch, p.home.key.Public().(ed25519.PublicKey), p.witness)
	if err != nil {
		return nil, fmt.Errorf("pending advance: %w", err)
	}
	if advance == nil {
		return link.NewEndpoint(p.home.tls, p.cert, p.witness, c.Epoch, c.Slot, c.TrustSources)
	}
	body, err := cp.AdvanceBody(*advance)
	if err != nil {
		return nil, err
	}
	pending := link.PendingMutation{ID: advance.ID, Digest: cp.RequestDigest(body)}
	return link.NewEndpointWithPending(p.home.tls, p.cert, p.witness, c.Epoch, c.Slot, c.TrustSources, pending)
}
