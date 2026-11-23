package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"ataniq/internal/approval/record"
	"ataniq/internal/seatgate"
	"ataniq/internal/seatlink"
)

// startLoginGate serves a gate that pins seatPin, lists the capability
// "login", and answers seat.login-challenge with a challenge made from its
// own key, as the daemon does. It stops when the test ends.
func startLoginGate(t *testing.T, seatPin string) (seatgate.Identity, string) {
	t.Helper()
	certPEM, keyPEM, err := seatgate.GenerateIdentity(rand.Reader, time.Now(), "gate")
	if err != nil {
		t.Fatal(err)
	}
	id, err := seatgate.ParseIdentity(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := func(_ context.Context, _ seatgate.Peer, op string, _ json.RawMessage) (json.RawMessage, error) {
		if op != seatlink.OpLoginChallenge {
			return nil, seatgate.NewCodedError("unknown-op")
		}
		nonce := bytes.Repeat([]byte{0x42}, 32)
		ch := seatlink.LoginChallengeOf(id.Fingerprint(), nonce)
		return json.Marshal(map[string]any{
			"challenge_id": "0123456789abcdef", "challenge": ch[:], "nonce": nonce,
			"expires_at": record.FormatTime(time.Now().Add(5 * time.Minute)), "allow": [][]byte{bytes.Repeat([]byte{7}, 32)},
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		seatgate.Serve(ctx, ln, seatgate.ServerConfig{
			TLS:           seatgate.ServerTLS(id, []string{seatPin}),
			Caps:          []string{"pair", "read", "login"},
			ShutdownGrace: 100 * time.Millisecond,
		}, seatgate.HandlerFunc(h))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return id, ln.Addr().String()
}

// TestSeatCommandLoginChecksTheChallengeWithThePin: the command gives the
// link the gate's pin, so a login challenge made from the gate's key reaches
// the phone. Without the pin the link would ask nothing.
func TestSeatCommandLoginChecksTheChallengeWithThePin(t *testing.T) {
	dir, fp := seatKey(t)
	gid, gaddr := startLoginGate(t, fp)
	links := watchLink(t)
	addr, _, stop := serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t),
		"--gate", gaddr, "--gate-pin-file", pinFile(t, gid.Fingerprint()+"\n", 0o600), "--identity", dir})
	defer stop()
	waitConnected(t, <-links)
	status, m := post(t, addr.String(), "/api/login-challenge", map[string]any{})
	want := seatlink.LoginChallengeOf(gid.Fingerprint(), bytes.Repeat([]byte{0x42}, 32))
	if status != http.StatusOK || m["challenge"] != base64.RawURLEncoding.EncodeToString(want[:]) {
		t.Errorf("login challenge: %d %v", status, m)
	}
}

// TestSeatCommandUnwiredLogin: without a gate the login answers
// daemon_unavailable.
func TestSeatCommandUnwiredLogin(t *testing.T) {
	addr, _, stop := serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t)})
	defer stop()
	status, m := post(t, addr.String(), "/api/login-challenge", map[string]any{})
	if status != http.StatusServiceUnavailable || m["error"] != "daemon_unavailable" {
		t.Errorf("login challenge: %d %v", status, m)
	}
}
