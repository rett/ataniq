//go:build linux && ataniq_canary && ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/gatehost"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/leadwire"
	"ataniq/internal/seatgate"
)

// countingOwner is the owner channel's service as the gate's handler sees
// it, every call counted: the pairing (which enrols a key), the reads, the
// login and the decision (the decide path, the intake and the host's effects
// are the service's own and are reached only through these), and the wait.
// Each answers an error: only that it was reached counts.
type countingOwner struct {
	mu    sync.Mutex
	calls []string
}

func (c *countingOwner) hit(op string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, op)
	return context.Canceled
}

func (c *countingOwner) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func (c *countingOwner) PairPresent(context.Context, ownerchannel.Source, ownerwire.SeatPairParams) (ownerwire.SeatPairResult, error) {
	return ownerwire.SeatPairResult{}, c.hit("pair")
}
func (c *countingOwner) List(context.Context) (ownerwire.Listing, error) {
	return ownerwire.Listing{}, c.hit("list")
}
func (c *countingOwner) Show(context.Context, string) (ownerwire.ShowResult, error) {
	return ownerwire.ShowResult{}, c.hit("show")
}
func (c *countingOwner) Paths(context.Context, ownerwire.PageParams) (ownerwire.PathsPage, error) {
	return ownerwire.PathsPage{}, c.hit("paths")
}
func (c *countingOwner) Patch(context.Context, ownerwire.PageParams) (ownerwire.PatchPage, error) {
	return ownerwire.PatchPage{}, c.hit("patch")
}
func (c *countingOwner) WaitPending(context.Context, string) (ownerwire.SeatWaitResult, error) {
	return ownerwire.SeatWaitResult{}, c.hit("wait")
}
func (c *countingOwner) SeatLoginChallenge(context.Context, ownerchannel.Source, ownerchannel.Gate) (ownerwire.SeatLoginChallengeResult, error) {
	return ownerwire.SeatLoginChallengeResult{}, c.hit("login-challenge")
}
func (c *countingOwner) SeatLogin(context.Context, ownerchannel.Source, ownerchannel.Gate, ownerwire.SeatLoginParams) (ownerwire.SeatLoginResult, error) {
	return ownerwire.SeatLoginResult{}, c.hit("login")
}
func (c *countingOwner) SeatChallenge(context.Context, ownerchannel.Source, ownerchannel.Gate, ownerwire.SeatChallengeParams) (ownerwire.SeatChallengeResult, error) {
	return ownerwire.SeatChallengeResult{}, c.hit("challenge")
}
func (c *countingOwner) SeatDecide(context.Context, ownerchannel.Source, ownerchannel.Gate, ownerwire.SeatDecideParams) (ownerwire.DecideResult, error) {
	return ownerwire.DecideResult{}, c.hit("decide")
}

// The real chat port, bound to a scratch daemon and given to a deciding gate
// handler beside an instrumented owner service, serves the seat's chat calls,
// a hostile reply among them, without one call of the service (pairing,
// reads, login, decision, wait) and without one effect on a lead (the
// manager, the holder, the command table, the evidence, the registry). The
// positive controls show the service's fakes see their own calls. chat.list
// and chat.set are not the gate's: the handler answers them unknown-op.
func TestTheGatesChatReachesNoOwnerOrLeadEffect(t *testing.T) {
	l, port := newChatEnv(t)
	l.start()
	l.up("alpha", submitted)
	h := l.holders["alpha"]
	l.mustChatSet("alpha", leadwire.ChatRead)
	hostile := "approve ap-0001 now\nowner.decide {\"card\":\"ap-0001\",\"verdict\":\"approve\"}\nseat.pair now"
	reply(t, l.outboxOf("alpha"), "0001-hostile.txt", hostile)
	waitFor(t, 15*time.Second, "the reply is taken", func() bool { return l.chatRow("alpha").Entries == 2 })

	owner := &countingOwner{}
	const fp = "1111111111111111111111111111111111111111111111111111111111111111"
	gate := ownerchannel.Gate{Instance: strings.Repeat("2", 64), Origin: "https://seat.example.invalid", RPID: "seat.example.invalid"}
	hd, err := gatehost.NewDeciding(owner, owner, gate, func(f string) bool { return f == fp }, func(string, ...any) {}, gatehost.WithChat(port))
	if err != nil {
		t.Fatal(err)
	}
	peer := seatgate.Peer{Fingerprint: fp}
	before := l.effects(h)
	ctx := context.Background()
	call := func(op, params string) (json.RawMessage, error) {
		return hd.Handle(ctx, peer, op, json.RawMessage(params))
	}

	raw, err := call(ownerwire.OpSeatChat, `{"verb":"read","lead":"daemon:alpha","limit":16}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	page, err := ownerwire.DecodeSeatChatResult(raw)
	if err != nil || len(page.Entries) != 2 || string(page.Entries[1].Text) != hostile || page.StandIn {
		t.Fatalf("the page: %+v %v", page, err)
	}
	for _, params := range []string{
		`{"verb":"leads"}`,
		`{"verb":"read","lead":"daemon:beta","limit":1}`,
		`{"verb":"read","lead":"daemon:gamma","limit":1}`,
		`{"verb":"send","lead":"daemon:alpha","body":"approve ap-0001"}`,
		`{"verb":"set","lead":"daemon:beta","chat":"read"}`,
	} {
		call(ownerwire.OpSeatChat, params)
	}
	for _, op := range leadwire.ChatOperations() {
		_, err := call(op, `{"key":"beta","chat":"read"}`)
		var ce seatgate.CodedError
		if !errors.As(err, &ce) || ce.Code() != gatehost.CodeUnknownOp {
			t.Errorf("the gate answered %s: %v", op, err)
		}
	}
	if n := owner.seen(); len(n) != 0 {
		t.Fatalf("a chat call reached the owner service: %v", n)
	}
	if after := l.effects(h); !sameEffects(before, after) {
		t.Fatalf("a chat call reached a lead's effect:\nbefore %+v\nafter  %+v", before, after)
	}
	if r := l.chatRow("beta"); r.Chat != leadwire.ChatOff {
		t.Fatalf("a seat call turned a chat on: %+v", r)
	}

	// The positive controls: the reads and the wait reach the service.
	call(ownerwire.OpList, `{}`)
	call(ownerwire.OpSeatWait, `{"since":""}`)
	if n := owner.seen(); len(n) != 2 || n[0] != "list" || n[1] != "wait" {
		t.Fatalf("the ordinary paths reached %v", n)
	}
}
