package gatehost

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatgate"
)

// newIdentity makes a seat gate identity for a test.
func newIdentity(t *testing.T, name string) seatgate.Identity {
	t.Helper()
	certPEM, keyPEM, err := seatgate.GenerateIdentity(rand.Reader, time.Now(), name)
	if err != nil {
		t.Fatal(err)
	}
	id, err := seatgate.ParseIdentity(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The handler behind the real gate, on the loopback address, with pinned
// keys at both ends: the hello lists its capabilities, a pairing call with
// no pairing open is refused pairing-refused on the wire, a write of the
// owner connection is unknown-op, and a read is answered.
func TestOverTheGate(t *testing.T) {
	gateID, seatID := newIdentity(t, "gate"), newIdentity(t, "seat")
	f := &fakePort{code: pairCode}
	h, err := New(f, func(fp string) bool { return fp == seatID.Fingerprint() }, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- seatgate.Serve(ctx, ln, seatgate.ServerConfig{TLS: seatgate.ServerTLS(gateID, []string{seatID.Fingerprint()}), Caps: Caps()}, h)
	}()
	defer func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve did not end")
		}
	}()

	dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dcancel()
	c, err := seatgate.Dial(dctx, ln.Addr().String(), seatgate.ClientConfig{TLS: seatgate.ClientTLS(seatID, gateID.Fingerprint(), "gate"), Caps: []string{"pair", "read"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !slices.Equal(c.Caps(), Caps()) {
		t.Errorf("the hello listed %q", c.Caps())
	}

	var re *seatgate.RemoteError
	err = c.Call(dctx, ownerwire.OpSeatPair, json.RawMessage(validParams(t, ownerwire.OpSeatPair)), nil)
	if !errors.As(err, &re) || re.Code != CodePairingRefused {
		t.Errorf("seat.pair with no pairing open: %v", err)
	}
	err = c.Call(dctx, ownerwire.OpDecide, json.RawMessage(`{}`), nil)
	if !errors.As(err, &re) || re.Code != CodeUnknownOp {
		t.Errorf("owner.decide: %v", err)
	}
	var l ownerwire.Listing
	if err := c.Call(dctx, ownerwire.OpList, nil, &l); err != nil || l.Count.WouldMeet != 2 {
		t.Errorf("owner.list: %+v %v", l, err)
	}

	calls := f.seen()
	if len(calls) != 2 || calls[0].src != (ownerchannel.Source{Surface: "seat", Session: seatID.Fingerprint()[:16]}) {
		t.Errorf("calls %+v", calls)
	}
}
