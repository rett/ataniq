//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/gatehost"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/chat/standin"
	"ataniq/internal/seatgate"
)

// startChatGate starts a gate over a bare service, with no thread store and
// the given chat port and note, and returns a dialed seat.
func startChatGate(t *testing.T, chat gatehost.ChatPort, note string, lg *gateLog) *seatgate.Client {
	t.Helper()
	gateID, seatID := newGateIdentity(t, "gate"), newGateIdentity(t, "seat")
	var ln net.Listener
	g := &SeatGate{
		Addr: netip.MustParseAddrPort("127.0.0.1:7443"), Identity: gateID, Pins: []string{seatID.Fingerprint()},
		Chat: chat, ChatNote: note, Log: lg.logf,
		Listen: func(network, address string) (net.Listener, error) {
			var err error
			ln, err = net.Listen("tcp", "127.0.0.1:0")
			return ln, err
		},
	}
	stop, err := g.Start(bareService{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := seatgate.Dial(ctx, ln.Addr().String(), seatgate.ClientConfig{TLS: seatgate.ClientTLS(seatID, gateID.Fingerprint(), "gate"), Caps: []string{"pair", "read", "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// A gate given a chat port serves the capability chat, last, names it in its
// start line and writes its note after it; a gate given none lists no chat,
// answers seat.chat unknown-op, and writes no note even when one is set.
func TestTheGateServesChatOnlyWithAChatPort(t *testing.T) {
	const note = "chat: STAND-IN marker note"
	lg := &gateLog{}
	c := startChatGate(t, standin.New(nil), note, lg)
	if got := strings.Join(c.Caps(), " "); got != "pair read chat" {
		t.Fatalf("capabilities %q", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var res ownerwire.SeatChatResult
	if err := c.Call(ctx, ownerwire.OpSeatChat, json.RawMessage(`{"verb":"leads"}`), &res); err != nil || !res.StandIn || len(res.Leads) != 3 {
		t.Fatalf("leads: %+v %v", res, err)
	}
	lines := lg.all()
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "seat gate: listening on") {
			start = i
		}
	}
	if start < 0 || !strings.HasSuffix(lines[start], "; it serves pair, read and chat only: no seat origin is configured") ||
		start+1 >= len(lines) || lines[start+1] != "seat gate: "+note {
		t.Fatalf("the start lines %q", lines)
	}

	lg2 := &gateLog{}
	c2 := startChatGate(t, nil, note, lg2)
	if got := strings.Join(c2.Caps(), " "); got != "pair read" {
		t.Fatalf("capabilities without a chat port: %q", got)
	}
	err := c2.Call(ctx, ownerwire.OpSeatChat, json.RawMessage(`{"verb":"leads"}`), nil)
	var re *seatgate.RemoteError
	if !errors.As(err, &re) || re.Code != gatehost.CodeUnknownOp {
		t.Fatalf("seat.chat without a chat port: %v", err)
	}
	for _, l := range lg2.all() {
		if strings.Contains(l, "marker note") {
			t.Fatalf("a gate without chat wrote the note: %q", l)
		}
	}
}
