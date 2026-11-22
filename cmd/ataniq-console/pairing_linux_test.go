package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
	"ataniq/internal/consoleclient"
	"ataniq/internal/terminalconsole"
)

// pairDaemon is a fake daemon with an owner channel whose pairing the test
// sets. It counts the connections that ask who the decider is and keeps every
// operation it was sent, so that a test sees how many calls reached it.
type pairDaemon struct {
	t   *testing.T
	dir string

	mu       sync.Mutex
	ops      []string
	deciders int
	status   ownerwire.PairStatus
	start    ownerwire.PairStartResult
	refuse   string
	ids      []string
}

func (d *pairDaemon) Hello(context.Context) clientapi.HelloResult {
	return clientapi.HelloResult{Version: "0.0.0-test"}
}

func (d *pairDaemon) Status(context.Context) (clientapi.StatusResult, error) {
	return clientapi.StatusResult{}, nil
}

func (d *pairDaemon) Operations() []string { return ownerwire.Operations() }
func (d *pairDaemon) Mutating() bool       { return true }

func (d *pairDaemon) Handle(_ context.Context, op string, params json.RawMessage) (any, *clientapi.ErrorBody) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, op)
	enc := func(b []byte, err error) (any, *clientapi.ErrorBody) {
		if err != nil {
			d.t.Error(err)
			return nil, &clientapi.ErrorBody{Code: "internal", Message: "test"}
		}
		return json.RawMessage(b), nil
	}
	switch op {
	case ownerwire.OpDecider:
		d.deciders++
		return enc(ownerwire.EncodeDeciderResult(ownerwire.DeciderResult{Decider: true}))
	case ownerwire.OpList:
		return enc(ownerwire.EncodeListing(ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}}))
	case ownerwire.OpBeat, ownerwire.OpLooked:
		return json.RawMessage(ownerwire.EncodeNothing()), nil
	case ownerwire.OpPairStart, ownerwire.OpPairStatus, ownerwire.OpPairConfirm, ownerwire.OpPairReject:
		if d.refuse != "" {
			return nil, &clientapi.ErrorBody{Code: clientapi.ErrorCode(d.refuse), Message: "refused"}
		}
	}
	switch op {
	case ownerwire.OpPairStart:
		return enc(ownerwire.EncodePairStartResult(d.start))
	case ownerwire.OpPairStatus:
		return enc(ownerwire.EncodePairStatus(d.status))
	case ownerwire.OpPairConfirm:
		p, err := ownerwire.DecodePairPendingParams(params)
		if err != nil {
			return nil, &clientapi.ErrorBody{Code: "internal", Message: "test"}
		}
		d.ids = append(d.ids, p.PendingID)
		return enc(ownerwire.EncodeKeyItem(ownerwire.KeyItem{ID: "00112233aabbccdd", Kind: "unverified", PairedAt: "2026-11-22T09:00:00Z"}))
	case ownerwire.OpPairReject:
		p, err := ownerwire.DecodePairPendingParams(params)
		if err != nil {
			return nil, &clientapi.ErrorBody{Code: "internal", Message: "test"}
		}
		d.ids = append(d.ids, p.PendingID)
		return json.RawMessage(ownerwire.EncodeNothing()), nil
	}
	return nil, &clientapi.ErrorBody{Code: "internal", Message: "no such operation in the test"}
}

func (d *pairDaemon) sent() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ops...)
}

func (d *pairDaemon) count(op string) int {
	n := 0
	for _, o := range d.sent() {
		if o == op {
			n++
		}
	}
	return n
}

func (d *pairDaemon) connections() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.deciders
}

func (d *pairDaemon) set(f func(d *pairDaemon)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(d)
}

func startPairDaemon(t *testing.T) *pairDaemon {
	t.Helper()
	base, err := os.MkdirTemp("", "pd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	d := &pairDaemon{t: t, dir: filepath.Join(base, "run")}
	d.start = ownerwire.PairStartResult{Code: "0123456789ABCDEFGHJK", ExpiresAt: time.Now().UTC().Add(5 * time.Minute).Format("2006-01-02T15:04:05Z")}
	d.status = ownerwire.PairStatus{Phase: ownerwire.PairNone}
	l, _, err := clientapi.Listen(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- clientapi.Serve(ctx, l, d, clientapi.Options{Extra: []clientapi.Extension{d}}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return")
		}
	})
	return d
}

func newTestOwner(t *testing.T, dir string) *consoleclient.Owner {
	t.Helper()
	o, err := consoleclient.NewOwner(consoleclient.OwnerConfig{Dir: dir, Client: clientName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	return o
}

func TestTheAdapterCarriesThePairingOverTheOwnerClient(t *testing.T) {
	d := startPairDaemon(t)
	a := newPairAdapter(newTestOwner(t, d.dir))
	ctx := context.Background()

	offer, err := a.PairStart(ctx, true)
	if err != nil || offer.Code != d.start.Code || offer.ExpiresAt.IsZero() {
		t.Fatalf("offer = %+v, %v", offer, err)
	}
	// The code is not in what the offer prints.
	if s := d.start.String(); s == "" {
		t.Fatal("empty")
	}

	st, err := a.PairStatus(ctx)
	if err != nil || st.Phase != terminalconsole.PairNone {
		t.Fatalf("none: %+v, %v", st, err)
	}
	d.set(func(d *pairDaemon) {
		d.status = ownerwire.PairStatus{Phase: ownerwire.PairWaiting, ExpiresAt: "2026-11-22T10:00:00Z"}
	})
	if st, err = a.PairStatus(ctx); err != nil || st.Phase != terminalconsole.PairWaiting {
		t.Fatalf("waiting: %+v, %v", st, err)
	}
	d.set(func(d *pairDaemon) {
		d.status = ownerwire.PairStatus{Phase: ownerwire.PairPending, Words: eightWords(), PendingID: pendingID, ExpiresAt: "2026-11-22T10:00:00Z"}
	})
	if st, err = a.PairStatus(ctx); err != nil || st.Phase != terminalconsole.PairPending || st.PendingID != pendingID || st.Words[0] != "amber" || st.Words[7] != "haven" {
		t.Fatalf("pending: %+v, %v", st, err)
	}
	d.set(func(d *pairDaemon) {
		d.status = ownerwire.PairStatus{Phase: ownerwire.PairExpired, ExpiresAt: "2026-11-22T10:00:00Z"}
	})
	if st, err = a.PairStatus(ctx); err != nil || st.Phase != terminalconsole.PairExpired {
		t.Fatalf("expired: %+v, %v", st, err)
	}

	paired, err := a.PairConfirm(ctx, pendingID)
	if err != nil || paired.ID != "00112233aabbccdd" || paired.Kind != "unverified" {
		t.Fatalf("paired = %+v, %v", paired, err)
	}
	if err := a.PairReject(ctx, pendingID); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	ids := append([]string(nil), d.ids...)
	d.mu.Unlock()
	if len(ids) != 2 || ids[0] != pendingID || ids[1] != pendingID {
		t.Fatalf("ids = %v", ids)
	}
	if d.connections() != 1 {
		t.Fatalf("%d connections for one adapter", d.connections())
	}
}

// TestADoneContextMakesNoCall holds that the adapter looks at the context
// before it asks the daemon: with the context over, no call is made, and not
// even a connection (the daemon was sent nothing, not owner.decider either).
func TestADoneContextMakesNoCall(t *testing.T) {
	d := startPairDaemon(t)
	a := newPairAdapter(newTestOwner(t, d.dir))
	done, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	for name, ctx := range map[string]context.Context{"canceled": done, "past its deadline": expired} {
		if _, err := a.PairStart(ctx, true); !errors.Is(err, ctx.Err()) {
			t.Errorf("%s: PairStart error = %v", name, err)
		}
		if _, err := a.PairStatus(ctx); !errors.Is(err, ctx.Err()) {
			t.Errorf("%s: PairStatus error = %v", name, err)
		}
		if _, err := a.PairConfirm(ctx, pendingID); !errors.Is(err, ctx.Err()) {
			t.Errorf("%s: PairConfirm error = %v", name, err)
		}
		if err := a.PairReject(ctx, pendingID); !errors.Is(err, ctx.Err()) {
			t.Errorf("%s: PairReject error = %v", name, err)
		}
	}
	if ops := d.sent(); len(ops) != 0 {
		t.Fatalf("the daemon was sent %v", ops)
	}
}

// TestTheAdapterNeverSendsACallAgain holds that a refused or failed call is
// made once: a second PairStart would make a second code and lose the first.
func TestTheAdapterNeverSendsACallAgain(t *testing.T) {
	d := startPairDaemon(t)
	a := newPairAdapter(newTestOwner(t, d.dir))
	d.set(func(d *pairDaemon) { d.refuse = consoleclient.PairPairingOpen })
	_, err := a.PairStart(context.Background(), false)
	var pe *consoleclient.PairError
	if !errors.As(err, &pe) || pe.Code != consoleclient.PairPairingOpen {
		t.Fatalf("error = %v", err)
	}
	if n := d.count(ownerwire.OpPairStart); n != 1 {
		t.Fatalf("%d pair-start calls reached the daemon, want 1", n)
	}
	if _, err := a.PairStatus(context.Background()); err == nil {
		t.Fatal("no error for a refused status")
	}
	if n := d.count(ownerwire.OpPairStatus); n != 1 {
		t.Fatalf("%d pair-status calls, want 1", n)
	}
	if _, err := a.PairConfirm(context.Background(), pendingID); err == nil || d.count(ownerwire.OpPairConfirm) != 1 {
		t.Fatalf("confirm: %v, %d calls", err, d.count(ownerwire.OpPairConfirm))
	}
	if err := a.PairReject(context.Background(), pendingID); err == nil || d.count(ownerwire.OpPairReject) != 1 {
		t.Fatalf("reject: %v, %d calls", err, d.count(ownerwire.OpPairReject))
	}
}
