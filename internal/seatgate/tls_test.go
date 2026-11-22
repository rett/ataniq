package seatgate

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// handshake runs one TLS connection over loopback with the two configurations
// and reports whether both ends completed it and the client could read what
// the server sent after it. TLS 1.3 completes the client's handshake before
// the server has judged the client, so the client reads one byte: a refusal
// by the server shows there as an alert.
func handshake(t *testing.T, srv, cli *tls.Config) (ok bool, cs tls.ConnectionState, err error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srvErr := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			srvErr <- err
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		tc := tls.Server(c, srv)
		if err := tc.Handshake(); err != nil {
			srvErr <- err
			return
		}
		_, err = tc.Write([]byte{'k'})
		srvErr <- err
	}()
	raw, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(5 * time.Second))
	tc := tls.Client(raw, cli)
	cerr := tc.Handshake()
	if cerr == nil {
		var b [1]byte
		_, cerr = io.ReadFull(tc, b[:])
	}
	serr := <-srvErr
	if cerr == nil && serr == nil {
		return true, tc.ConnectionState(), nil
	}
	if cerr != nil {
		return false, tls.ConnectionState{}, cerr
	}
	return false, tls.ConnectionState{}, serr
}

func TestHandshakeBetweenPinnedPeers(t *testing.T) {
	seat, gate := newID(t, "seat"), newID(t, "gate")
	srv := ServerTLS(gate, []string{seat.Fingerprint()})
	cli := ClientTLS(seat, gate.Fingerprint(), "gate.example.invalid")
	ok, cs, err := handshake(t, srv, cli)
	if !ok {
		t.Fatalf("pinned peers were refused: %v", err)
	}
	if cs.Version != tls.VersionTLS13 || cs.NegotiatedProtocol != ALPN {
		t.Errorf("version %x protocol %q", cs.Version, cs.NegotiatedProtocol)
	}
}

func TestHandshakeAcceptsAnyOfSeveralPins(t *testing.T) {
	a, b, gate := newID(t, "a"), newID(t, "b"), newID(t, "gate")
	other := newID(t, "c")
	srv := ServerTLS(gate, []string{other.Fingerprint(), a.Fingerprint(), b.Fingerprint()})
	for _, id := range []Identity{a, b} {
		if ok, _, err := handshake(t, srv, ClientTLS(id, gate.Fingerprint(), "x.example.invalid")); !ok {
			t.Fatalf("a pinned client was refused: %v", err)
		}
	}
}

func TestHandshakeRefusals(t *testing.T) {
	seat, gate := newID(t, "seat"), newID(t, "gate")
	stranger := newID(t, "seat") // the same name, another key
	srv := ServerTLS(gate, []string{seat.Fingerprint()})

	t.Run("unpinned client", func(t *testing.T) {
		cli := ClientTLS(stranger, gate.Fingerprint(), "x.example.invalid")
		if ok, _, _ := handshake(t, srv, cli); ok {
			t.Fatal("an unpinned client got in")
		}
	})
	t.Run("same name and another key", func(t *testing.T) {
		// The client is the stranger and is pinned under no name at all.
		srv2 := ServerTLS(gate, []string{stranger.Fingerprint()})
		cli := ClientTLS(seat, gate.Fingerprint(), "x.example.invalid")
		if ok, _, _ := handshake(t, srv2, cli); ok {
			t.Fatal("a certificate with the same name and another key got in")
		}
	})
	t.Run("unpinned server", func(t *testing.T) {
		cli := ClientTLS(seat, stranger.Fingerprint(), "x.example.invalid")
		if ok, _, _ := handshake(t, srv, cli); ok {
			t.Fatal("the client accepted an unpinned server")
		}
	})
	t.Run("no pins on the server", func(t *testing.T) {
		cli := ClientTLS(seat, gate.Fingerprint(), "x.example.invalid")
		if ok, _, _ := handshake(t, ServerTLS(gate, nil), cli); ok {
			t.Fatal("a server with no pins let a client in")
		}
	})
	t.Run("TLS 1.2 client", func(t *testing.T) {
		cli := ClientTLS(seat, gate.Fingerprint(), "x.example.invalid")
		cli.MinVersion, cli.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
		if ok, _, _ := handshake(t, srv, cli); ok {
			t.Fatal("a TLS 1.2 client got in")
		}
	})
	t.Run("TLS 1.2 server", func(t *testing.T) {
		s := ServerTLS(gate, []string{seat.Fingerprint()})
		s.MinVersion, s.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
		cli := ClientTLS(seat, gate.Fingerprint(), "x.example.invalid")
		if ok, _, _ := handshake(t, s, cli); ok {
			t.Fatal("the client accepted a TLS 1.2 server")
		}
	})
	t.Run("no client certificate", func(t *testing.T) {
		cli := ClientTLS(seat, gate.Fingerprint(), "x.example.invalid")
		cli.Certificates = nil
		if ok, _, _ := handshake(t, srv, cli); ok {
			t.Fatal("a client with no certificate got in")
		}
	})
	t.Run("no ALPN from the client", func(t *testing.T) {
		cli := ClientTLS(seat, gate.Fingerprint(), "x.example.invalid")
		cli.NextProtos = nil
		if ok, _, _ := handshake(t, srv, cli); ok {
			t.Fatal("a client with no ALPN got in")
		}
	})
	t.Run("another ALPN from the client", func(t *testing.T) {
		cli := ClientTLS(seat, gate.Fingerprint(), "x.example.invalid")
		cli.NextProtos = []string{"http/1.1"}
		if ok, _, _ := handshake(t, srv, cli); ok {
			t.Fatal("a client with another ALPN got in")
		}
	})
	t.Run("no ALPN from the server", func(t *testing.T) {
		s := ServerTLS(gate, []string{seat.Fingerprint()})
		s.NextProtos = nil
		cli := ClientTLS(seat, gate.Fingerprint(), "x.example.invalid")
		if ok, _, _ := handshake(t, s, cli); ok {
			t.Fatal("the client accepted a server with no ALPN")
		}
	})
}

// A fingerprint that is not exactly 64 lower-case hex digits matches nothing,
// whichever way it was changed.
func TestTamperedFingerprintFormatIsRefused(t *testing.T) {
	seat, gate := newID(t, "seat"), newID(t, "gate")
	fp := gate.Fingerprint()
	pinForms := map[string]string{
		"upper":    strings.ToUpper(fp),
		"short":    fp[:63],
		"long":     fp + "0",
		"colons":   fp[:2] + ":" + fp[2:],
		"prefixed": "sha256:" + fp,
		"padded":   " " + fp,
		"empty":    "",
	}
	for name, pin := range pinForms {
		cli := ClientTLS(seat, pin, "x.example.invalid")
		if ok, _, _ := handshake(t, ServerTLS(gate, []string{seat.Fingerprint()}), cli); ok {
			t.Errorf("client pin %s was accepted", name)
		}
	}
	sfp := seat.Fingerprint()
	for name, pin := range map[string]string{"upper": strings.ToUpper(sfp), "short": sfp[:63], "colons": sfp[:2] + ":" + sfp[2:]} {
		srv := ServerTLS(gate, []string{pin})
		if ok, _, _ := handshake(t, srv, ClientTLS(seat, fp, "x.example.invalid")); ok {
			t.Errorf("server pin %s was accepted", name)
		}
	}
	// A list with one bad entry still honours the good ones.
	srv := ServerTLS(gate, []string{"zz", sfp})
	if ok, _, err := handshake(t, srv, ClientTLS(seat, fp, "x.example.invalid")); !ok {
		t.Errorf("a good pin beside a bad one was refused: %v", err)
	}
}

// customID makes an identity from a template, to test certificates this
// package would never make itself.
func customID(t *testing.T, tmpl *x509.Certificate) Identity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.SerialNumber = big.NewInt(7)
	tmpl.Subject = pkix.Name{CommonName: "custom"}
	if tmpl.NotBefore.IsZero() {
		tmpl.NotBefore = time.Now().Add(-time.Hour)
		tmpl.NotAfter = time.Now().Add(time.Hour)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return Identity{cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, fp: spkiFingerprint(leaf)}
}

func TestCertificateShapeIsChecked(t *testing.T) {
	gate := newID(t, "gate")
	both := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}

	t.Run("expired and not yet valid certificates are fine: the key is the identity", func(t *testing.T) {
		old := customID(t, &x509.Certificate{
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: both,
			NotBefore: time.Now().AddDate(-20, 0, 0), NotAfter: time.Now().AddDate(-10, 0, 0),
		})
		srv := ServerTLS(gate, []string{old.Fingerprint()})
		if ok, _, err := handshake(t, srv, ClientTLS(old, gate.Fingerprint(), "x.example.invalid")); !ok {
			t.Fatalf("an expired certificate with a pinned key was refused: %v", err)
		}
	})
	t.Run("a client certificate with server usage only", func(t *testing.T) {
		c := customID(t, &x509.Certificate{KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		srv := ServerTLS(gate, []string{c.Fingerprint()})
		if ok, _, _ := handshake(t, srv, ClientTLS(c, gate.Fingerprint(), "x.example.invalid")); ok {
			t.Fatal("a pinned certificate without client usage got in")
		}
	})
	t.Run("a server certificate with client usage only", func(t *testing.T) {
		seat := newID(t, "seat")
		c := customID(t, &x509.Certificate{KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
		srv := ServerTLS(c, []string{seat.Fingerprint()})
		if ok, _, _ := handshake(t, srv, ClientTLS(seat, c.Fingerprint(), "x.example.invalid")); ok {
			t.Fatal("the client accepted a pinned server without server usage")
		}
	})
	t.Run("no digital signature usage", func(t *testing.T) {
		c := customID(t, &x509.Certificate{KeyUsage: x509.KeyUsageKeyEncipherment, ExtKeyUsage: both})
		srv := ServerTLS(gate, []string{c.Fingerprint()})
		if ok, _, _ := handshake(t, srv, ClientTLS(c, gate.Fingerprint(), "x.example.invalid")); ok {
			t.Fatal("a certificate without the signature usage got in")
		}
	})
	t.Run("a chain of two", func(t *testing.T) {
		seat := newID(t, "seat")
		extra := newID(t, "extra")
		chained := seat
		chained.cert.Certificate = [][]byte{seat.cert.Certificate[0], extra.cert.Certificate[0]}
		srv := ServerTLS(gate, []string{seat.Fingerprint()})
		if ok, _, _ := handshake(t, srv, ClientTLS(chained, gate.Fingerprint(), "x.example.invalid")); ok {
			t.Fatal("a chain of two certificates got in")
		}
	})
}

func TestTLSConfigIsHardened(t *testing.T) {
	id := newID(t, "x")
	for name, c := range map[string]*tls.Config{
		"server": ServerTLS(id, nil),
		"client": ClientTLS(id, id.Fingerprint(), "x.example.invalid"),
	} {
		if c.MinVersion != tls.VersionTLS13 || c.MaxVersion != tls.VersionTLS13 {
			t.Errorf("%s: versions %x to %x", name, c.MinVersion, c.MaxVersion)
		}
		if !c.SessionTicketsDisabled || c.Renegotiation != tls.RenegotiateNever {
			t.Errorf("%s: tickets or renegotiation", name)
		}
		if len(c.NextProtos) != 1 || c.NextProtos[0] != "ataniq-gate/1" {
			t.Errorf("%s: ALPN %v", name, c.NextProtos)
		}
		if c.VerifyPeerCertificate == nil || c.VerifyConnection == nil {
			t.Errorf("%s: no verification", name)
		}
	}
	if ServerTLS(id, nil).ClientAuth != tls.RequireAnyClientCert {
		t.Error("the server does not require a client certificate")
	}
}

func TestNoSessionResumption(t *testing.T) {
	seat, gate := newID(t, "seat"), newID(t, "gate")
	srv := ServerTLS(gate, []string{seat.Fingerprint()})
	cli := ClientTLS(seat, gate.Fingerprint(), "x.example.invalid")
	cli.ClientSessionCache = tls.NewLRUClientSessionCache(4)
	for i := 0; i < 2; i++ {
		ok, cs, err := handshake(t, srv, cli)
		if !ok {
			t.Fatal(err)
		}
		if cs.DidResume {
			t.Fatal("a session was resumed")
		}
	}
}
