package seatgate

import (
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
)

// ALPN is the application protocol both ends must negotiate. The major
// version of the protocol is in it.
const ALPN = "ataniq-gate/1"

// pins is a list of fingerprints, decoded once. A fingerprint that is not
// exactly 64 lower-case hex digits is dropped: it can match nothing, so a
// list that holds only such entries refuses every peer.
type pins [][32]byte

func parsePins(list []string) pins {
	var out pins
	for _, s := range list {
		if !ValidFingerprint(s) {
			continue
		}
		var p [32]byte
		if _, err := hex.Decode(p[:], []byte(s)); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// has reports whether the fingerprint (raw bytes) is in the list. It compares
// every entry, in constant time each, and does not stop at a match.
func (p pins) has(sum [32]byte) bool {
	found := 0
	for i := range p {
		found |= subtle.ConstantTimeCompare(p[i][:], sum[:])
	}
	return found == 1
}

// verifier is the VerifyPeerCertificate of both ends. The peer must send
// exactly one certificate; it must carry the digital signature usage and the
// extended usage the peer's role needs (usage); its key must be ECDSA P-256;
// and the fingerprint of its key must be one of the pins. Validity dates,
// names and issuers are not looked at: the key is the identity.
func verifier(p pins, usage x509.ExtKeyUsage) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) != 1 {
			return errors.New("seatgate: the peer must send exactly one certificate")
		}
		c, err := x509.ParseCertificate(raw[0])
		if err != nil {
			return errors.New("seatgate: the peer's certificate cannot be read")
		}
		if err := checkLeaf(c, usage); err != nil {
			return err
		}
		if !p.has(sha256Sum(c.RawSubjectPublicKeyInfo)) {
			return errors.New("seatgate: the peer's key is not pinned")
		}
		return nil
	}
}

// connectionCheck is the VerifyConnection of both ends: TLS 1.3 and the
// protocol's ALPN, whatever else the peer offered.
func connectionCheck(cs tls.ConnectionState) error {
	if cs.Version != tls.VersionTLS13 {
		return errors.New("seatgate: TLS 1.3 is required")
	}
	if cs.NegotiatedProtocol != ALPN {
		return errors.New("seatgate: the peer did not negotiate " + ALPN)
	}
	return nil
}

// ServerTLS is the TLS configuration of the end that listens (the gate). It
// accepts TLS 1.3 only, asks for a client certificate and requires one, and
// lets a client in only if the fingerprint of its key is one of
// pinnedClients. With an empty list nobody gets in. It has no session tickets
// and never renegotiates, and it requires the ALPN [ALPN].
func ServerTLS(id Identity, pinnedClients []string) *tls.Config {
	return &tls.Config{
		Certificates:           []tls.Certificate{id.cert},
		MinVersion:             tls.VersionTLS13,
		MaxVersion:             tls.VersionTLS13,
		ClientAuth:             tls.RequireAnyClientCert,
		VerifyPeerCertificate:  verifier(parsePins(pinnedClients), x509.ExtKeyUsageClientAuth),
		VerifyConnection:       connectionCheck,
		SessionTicketsDisabled: true,
		Renegotiation:          tls.RenegotiateNever,
		NextProtos:             []string{ALPN},
	}
}

// ClientTLS is the TLS configuration of the end that dials (the seat). It
// accepts TLS 1.3 only and a server whose key has the fingerprint
// pinnedServer, and nobody else. serverName is sent as the server name
// indication only; it is not checked against the certificate, because the
// pinned key is the identity. Go's own verification is switched off
// (InsecureSkipVerify) and replaced by the pin, which is the one documented way
// to pin a raw key.
func ClientTLS(id Identity, pinnedServer string, serverName string) *tls.Config {
	return &tls.Config{
		Certificates:           []tls.Certificate{id.cert},
		MinVersion:             tls.VersionTLS13,
		MaxVersion:             tls.VersionTLS13,
		InsecureSkipVerify:     true, //nolint:gosec // replaced by VerifyPeerCertificate: the key is pinned
		VerifyPeerCertificate:  verifier(parsePins([]string{pinnedServer}), x509.ExtKeyUsageServerAuth),
		VerifyConnection:       connectionCheck,
		SessionTicketsDisabled: true,
		Renegotiation:          tls.RenegotiateNever,
		NextProtos:             []string{ALPN},
		ServerName:             serverName,
	}
}
