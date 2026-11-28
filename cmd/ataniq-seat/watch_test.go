package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
	"ataniq/internal/seatlink"
)

// goroutinesIn counts the goroutines whose stack holds name.
func goroutinesIn(name string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), name)
		}
		buf = make([]byte, 2*len(buf))
	}
}

// TestRunWatchesThroughTheGuard: with a gate that lists "watch" the command
// starts the link's watcher, the app's daemon port (behind the guard) passes
// its state and its channel on, the gate gets the seat's one wait, and when
// the command stops the watcher is gone.
func TestRunWatchesThroughTheGuard(t *testing.T) {
	dir, fp := seatKey(t)
	g := startGate(t, fp, "")
	// A gate of its own, which lists "watch" and holds the seat's wait.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var waits atomic.Int64
	h := func(ctx context.Context, _ seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
		if op != ownerwire.OpSeatWait {
			return nil, seatgate.NewCodedError("unknown-op")
		}
		p, err := ownerwire.DecodeSeatWaitParams(params)
		if err != nil {
			return nil, seatgate.NewCodedError("bad-params")
		}
		waits.Add(1)
		cards := []ownerwire.SeatWaitCard{}
		version := ownerwire.PendingVersion(cards)
		if p.Since == version {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return ownerwire.EncodeSeatWaitResult(ownerwire.SeatWaitResult{Version: version, Cards: cards})
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

	got := make(chan seatapp.Daemon, 1)
	daemonHook = func(d seatapp.Daemon) { got <- d }
	t.Cleanup(func() { daemonHook = nil })
	links := watchLink(t)
	var stderr *bytes.Buffer
	var stop func() int
	_, stderr, stop = serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t),
		"--gate", ln.Addr().String(), "--gate-pin-file", pinFile(t, g.id.Fingerprint()+"\n", 0o600), "--identity", dir})
	waitConnected(t, <-links)
	port, ok := (<-got).(seatlink.WatchPort)
	if !ok {
		t.Fatal("the app's daemon port does not pass the watched state on")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		ch := port.WatchChanged()
		if s := port.Watch(); s.Watching && !s.Offline && s.Version != "" {
			break
		}
		select {
		case <-ch:
		case <-time.After(time.Until(deadline)):
			t.Fatalf("no watched state: %+v", port.Watch())
		}
	}
	if n := waits.Load(); n < 1 {
		t.Errorf("the gate got %d waits", n)
	}
	if code := stop(); code != 0 {
		t.Errorf("shutdown code %d: %s", code, stderr.String())
	}
	if n := goroutinesIn("seatlink.(*watcher).run"); n != 0 {
		t.Errorf("%d watcher goroutines after the command stopped", n)
	}
}

// TestGuardPassesTheWatchOn: the guard passes the state and the channel of a
// port that watches on, and for one that does not says nothing is watched,
// with a channel that never closes.
func TestGuardPassesTheWatchOn(t *testing.T) {
	g, _, _ := guarded(stateDir(t))
	if s := g.Watch(); s.Watching || s.Version != "" {
		t.Errorf("state %+v of a port that does not watch", s)
	}
	select {
	case <-g.WatchChanged():
		t.Error("the channel of a port that does not watch closed")
	default:
	}
	w := &watchingDaemon{ch: make(chan struct{})}
	w.state = seatlink.WatchState{Watching: true, Version: "v"}
	g = newGuardedDaemon(w, stateDir(t), nil)
	if s := g.Watch(); !s.Watching || s.Version != "v" {
		t.Errorf("state %+v", s)
	}
	close(w.ch)
	select {
	case <-g.WatchChanged():
	default:
		t.Error("the port's channel was not passed on")
	}
}

// watchingDaemon is a port that watches.
type watchingDaemon struct {
	unwiredDaemon
	state seatlink.WatchState
	ch    chan struct{}
}

func (w *watchingDaemon) Watch() seatlink.WatchState    { return w.state }
func (w *watchingDaemon) WatchChanged() <-chan struct{} { return w.ch }

// TestRunServesTheHintStream: the command's server has the card list's hint
// stream, of both views, with its admission: a request without a session is
// refused with the fixed text and no redirect, before any header of a
// stream; and the command still shuts down at once.
func TestRunServesTheHintStream(t *testing.T) {
	addr, _, stop := serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t)})
	for _, opening := range []string{`{"version":""}`, `{"leads":"0"}`} {
		req, err := http.NewRequest("POST", "http://"+addr.String()+"/api/watch/stream", strings.NewReader(opening))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = seatRPID
		req.Header.Set("Origin", seatOrigin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Content-Type", "application/json")
		tr := &http.Transport{DisableKeepAlives: true}
		resp, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		tr.CloseIdleConnections()
		if resp.StatusCode != http.StatusForbidden || string(body) != "session ended\n" ||
			resp.Header.Get("Location") != "" || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") ||
			resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("stream %s without a session: %d %q %v", opening, resp.StatusCode, body, resp.Header)
		}
	}
	if code := stop(); code != 0 {
		t.Errorf("shutdown code %d", code)
	}
}
