// Package link carries checkpoint requests from a home to its configured
// authority over connections polled by the witness. A lost
// mutation reply is reconciled by a challenged Read with the saved ReceiptID
// and, when appropriate, a retry of the identical intent.
package link

import (
	"crypto/tls"
	"crypto/x509"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/seatgate"
)

const ALPN = "ataniq-checkpoint/2"

// Keep the seat's exact P-256 SPKI, role usage and TLS 1.3 checks. Give the
// checkpoint service its own ALPN and provision it with separate identities.
func checkpointTLS(cfg *tls.Config) *tls.Config {
	cfg.NextProtos = []string{ALPN}
	cfg.VerifyConnection = func(s tls.ConnectionState) error {
		if s.Version != tls.VersionTLS13 || s.NegotiatedProtocol != ALPN {
			return cp.ErrRefused
		}
		return nil
	}
	return cfg
}

func serverTLS(id seatgate.Identity, peerPin string) *tls.Config {
	cfg := seatgate.ServerTLS(id, []string{peerPin})
	return checkpointTLS(cfg)
}

func clientTLS(pin string, owner seatgate.Identity) *tls.Config {
	cfg := seatgate.ClientTLS(owner, pin, "")
	if owner.Fingerprint() == "" {
		cfg.Certificates = nil
	}
	// Return a typed refusal for a certificate that fails the pin or role checks.
	verify := cfg.VerifyPeerCertificate
	cfg.VerifyPeerCertificate = func(raw [][]byte, chains [][]*x509.Certificate) error {
		if err := verify(raw, chains); err != nil {
			return &cp.Reason{Kind: cp.Unauthorized}
		}
		return nil
	}
	return checkpointTLS(cfg)
}
