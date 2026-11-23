package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatgate"
	"ataniq/internal/seatnotice"
)

// TestRunTellsTheNotifierHowManyWait: with a gate that has two cards pending
// and a notifier on --notify-socket, the command tells the notifier one
// integer, 2, once, and when it stops its goroutine is gone. The notifier is
// the package's own server on a socket of this test, allowed for this user.
func TestRunTellsTheNotifierHowManyWait(t *testing.T) {
	dir, fp := seatKey(t)
	g := startGate(t, fp, "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cards := []ownerwire.SeatWaitCard{
		{ID: "ap-20261121-0001", Kind: ownerwire.CardKindApproval, BindingSHA256: strings.Repeat("ab", 32)},
		{ID: "ap-20261121-0002", Kind: ownerwire.CardKindApproval, BindingSHA256: strings.Repeat("cd", 32)},
	}
	h := func(ctx context.Context, _ seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
		if op != ownerwire.OpSeatWait {
			return nil, seatgate.NewCodedError("unknown-op")
		}
		p, err := ownerwire.DecodeSeatWaitParams(params)
		if err != nil {
			return nil, seatgate.NewCodedError("bad-params")
		}
		version := ownerwire.PendingVersion(cards)
		if p.Since == version {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return ownerwire.EncodeSeatWaitResult(ownerwire.SeatWaitResult{Version: version, Waiting: len(cards), Cards: cards})
	}
	sctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		seatgate.Serve(sctx, ln, seatgate.ServerConfig{
			TLS:           seatgate.ServerTLS(g.id, []string{fp}),
			Caps:          []string{"pair", "read", "watch"},
			ShutdownGrace: 100 * time.Millisecond,
		}, seatgate.HandlerFunc(h))
	}()
	t.Cleanup(func() { cancel(); <-done })

	// The notifier, on a socket in a short private folder.
	sdir, err := os.MkdirTemp("", "sn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sdir) })
	sock := filepath.Join(sdir, "n.sock")
	nl, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("no Unix sockets: %v", err)
	}
	fake := &seatnotice.FakeHandler{}
	nctx, ncancel := context.WithCancel(context.Background())
	ndone := make(chan error, 1)
	go func() {
		ndone <- seatnotice.Serve(nctx, nl, fake, seatnotice.ServeConfig{AllowedUID: uint32(os.Getuid())})
	}()
	t.Cleanup(func() { ncancel(); <-ndone })

	links := watchLink(t)
	var stderr *bytes.Buffer
	var stop func() int
	var addr net.Addr
	addr, stderr, stop = serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t),
		"--gate", ln.Addr().String(), "--gate-pin-file", pinFile(t, g.id.Fingerprint()+"\n", 0o600), "--identity", dir,
		"--notify-socket", sock})
	waitConnected(t, <-links)
	deadline := time.Now().Add(10 * time.Second)
	for len(fake.Notified()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the notifier was told nothing: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if got := fake.Notified(); len(got) != 1 || got[0] != 2 {
		t.Errorf("the notifier was told %v, want [2]", got)
	}
	// With the notifier's socket the seat also serves the phone's side of
	// the notices: the service worker's script is there, and no session is
	// needed for it.
	resp, err := http.Get("http://" + addr.String() + "/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Service-Worker-Allowed") != "/" {
		t.Errorf("sw.js: %d %v", resp.StatusCode, resp.Header)
	}
	if code := stop(); code != 0 {
		t.Errorf("shutdown code %d: %s", code, stderr.String())
	}
	if n := goroutinesIn("seatapp.(*app).noticeLoop"); n != 0 {
		t.Errorf("%d notice goroutines after the command stopped", n)
	}
}

// TestRunWithoutNotifySocketStartsNoNotices: no flag, no goroutine.
func TestRunWithoutNotifySocketStartsNoNotices(t *testing.T) {
	addr, stderr, stop := serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t)})
	if n := goroutinesIn("noticeLoop"); n != 0 {
		t.Errorf("%d notice goroutines without the flag", n)
	}
	// Nor is there a phone's side of the notices.
	for _, p := range []string{"/sw.js", "/manifest.webmanifest", "/static/push.js"} {
		resp, err := http.Get("http://" + addr.String() + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s without the flag: %d", p, resp.StatusCode)
		}
	}
	if code := stop(); code != 0 {
		t.Errorf("shutdown %d: %s", code, stderr.String())
	}
}
