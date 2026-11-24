package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/authorityhost/decisionguard"
	"ataniq/internal/chat/standin"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// genesisPort is a daemon port that runs the interview and notes the calls.
type genesisPort struct {
	unwiredDaemon
	calls atomic.Int64
}

func (p *genesisPort) Genesis(context.Context, seatapp.GenesisCall) (seatapp.GenesisView, error) {
	p.calls.Add(1)
	return seatapp.GenesisView{State: seatapp.GenesisStateThreads, Threads: []seatapp.GenesisThread{}}, nil
}

// The guard passes the interview on to a port that runs it, while decisions
// are paused too: an answer decides nothing. A port that does not run it is
// not offered.
func TestGuardPassesTheInterviewOn(t *testing.T) {
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, decisionguard.GuardFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p := &genesisPort{}
	var refused []string
	g := newGuardedDaemon(p, dir, func(code string) { refused = append(refused, code) })
	var d seatapp.Daemon = g
	port, ok := d.(seatapp.Genesis)
	if !ok {
		t.Fatal("the guarded daemon is no Genesis port")
	}
	v, err := port.Genesis(context.Background(), seatapp.GenesisCall{Verb: seatapp.GenesisVerbList})
	if err != nil || v.State != seatapp.GenesisStateThreads || p.calls.Load() != 1 || len(refused) != 0 {
		t.Fatalf("the list through the guard: %+v, %v, %d calls, refusals %v", v, err, p.calls.Load(), refused)
	}
	unwired := newGuardedDaemon(unwiredDaemon{}, dir, nil)
	if _, err := unwired.Genesis(context.Background(), seatapp.GenesisCall{Verb: seatapp.GenesisVerbList}); !errors.Is(err, seatapp.ErrNotOffered) {
		t.Fatalf("an unwired port: %v", err)
	}
}

// startGenesisGate serves a gate that lists caps and answers a list of
// seat.genesis with no open thread.
func startGenesisGate(t *testing.T, seatPin string, caps []string) (seatgate.Identity, string, *atomic.Int64) {
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
	var calls atomic.Int64
	chat := standin.New(time.Now)
	h := func(ctx context.Context, _ seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
		if op == ownerwire.OpSeatChat && slices.Contains(caps, "chat") {
			call, err := ownerwire.DecodeSeatChatParams(params)
			if err != nil {
				return nil, seatgate.NewCodedError("bad-params")
			}
			view, err := chat.Chat(ctx, call)
			if err != nil {
				return nil, err
			}
			return ownerwire.EncodeSeatChatResult(view)
		}
		if op != ownerwire.OpSeatGenesis {
			return nil, seatgate.NewCodedError("unknown-op")
		}
		if p, err := ownerwire.DecodeSeatGenesisParams(params); err != nil || p.Verb != ownerwire.GenesisVerbList {
			return nil, seatgate.NewCodedError("bad-params")
		}
		calls.Add(1)
		return ownerwire.EncodeSeatGenesisResult(ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateThreads,
			Nodes: []ownerwire.GenesisNode{}, Threads: []ownerwire.GenesisThread{}})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		seatgate.Serve(ctx, ln, seatgate.ServerConfig{TLS: seatgate.ServerTLS(id, []string{seatPin}), Caps: caps,
			ShutdownGrace: 100 * time.Millisecond}, seatgate.HandlerFunc(h))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return id, ln.Addr().String(), &calls
}

// The command needs nothing configured for the interview: the app finds the
// port in the gate link behind the guard, a call reaches the gate, and the
// log names the capabilities each hello listed, genesis among them, with
// whether the phone offers New project.
func TestRunOffersTheInterviewWhenTheGateDoes(t *testing.T) {
	helloEvery = 10 * time.Millisecond
	t.Cleanup(func() { helloEvery = time.Second })
	for _, c := range []struct {
		caps []string
		line string
		port bool
	}{
		{[]string{"pair", "read", "genesis", "chat"}, "ataniq-seat: the seat gate offers pair, read, genesis and chat; the phone offers New project; the phone offers read-only Leads\n", true},
		{[]string{"pair", "read", "genesis"}, "ataniq-seat: the seat gate offers pair, read and genesis; the phone offers New project\n", true},
		{[]string{"pair", "read"}, "ataniq-seat: the seat gate offers pair and read; the phone offers no New project\n", false},
	} {
		dir, fp := seatKey(t)
		gid, addr, calls := startGenesisGate(t, fp, c.caps)
		links := watchLink(t)
		got := make(chan seatapp.Daemon, 1)
		daemonHook = func(d seatapp.Daemon) { got <- d }
		stderr := &lockedBuffer{}
		stop := serveTo(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t),
			"--gate", addr, "--gate-pin-file", pinFile(t, gid.Fingerprint()+"\n", 0o600), "--identity", dir,
			"--decision-guard", stateDir(t)}, stderr)
		waitConnected(t, <-links)
		d := <-got
		daemonHook = nil
		port, ok := d.(seatapp.Genesis)
		if !ok {
			t.Fatalf("%v: the app's daemon %T is no Genesis port", c.caps, d)
		}
		_, err := port.Genesis(context.Background(), seatapp.GenesisCall{Verb: seatapp.GenesisVerbList})
		if c.port && (err != nil || calls.Load() != 1) {
			t.Errorf("%v: a list: %v, %d calls of the gate", c.caps, err, calls.Load())
		}
		if !c.port && (!errors.Is(err, seatapp.ErrNotOffered) || calls.Load() != 0) {
			t.Errorf("%v: a list: %v, %d calls of the gate", c.caps, err, calls.Load())
		}
		chat, ok := d.(seatapp.Chat)
		offered := slices.Contains(c.caps, "chat")
		if !ok || chat.ChatOffered() != offered {
			t.Fatal("chat offer through the running command")
		}
		call := seatapp.ChatCall{Verb: seatapp.ChatRead, Lead: "daemon:alpha"}
		v, err := chat.Chat(context.Background(), call)
		if offered && (err != nil || !v.ValidFor(call) || !v.StandIn || len(v.Entries) == 0) {
			t.Fatalf("trial read through the command: %v", err)
		}
		if !offered && !errors.Is(err, seatapp.ErrNotOffered) {
			t.Fatalf("gate without chat: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(stderr.String(), "the seat gate offers") && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if code := stop(); code != 0 {
			t.Errorf("%v: shutdown code %d", c.caps, code)
		}
		if out := stderr.String(); strings.Count(out, c.line) != 1 {
			t.Errorf("%v: stderr lacks the hello's line %q once: %q", c.caps, c.line, out)
		}
	}
}

// lockedBuffer is a buffer the command writes to while the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// serveTo is serve with the command's errors written to w.
func serveTo(t *testing.T, args []string, w io.Writer) (stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan net.Addr, 1)
	done := make(chan int, 1)
	go func() { done <- run(ctx, args, w, func(a net.Addr) { got <- a }) }()
	select {
	case <-got:
	case c := <-done:
		cancel()
		t.Fatalf("run ended with %d", c)
	case <-time.After(waitLong):
		cancel()
		t.Fatal("no listener")
	}
	return func() int {
		cancel()
		select {
		case c := <-done:
			return c
		case <-time.After(waitLong):
			t.Fatalf("no shutdown after %v", waitLong)
			return -1
		}
	}
}

func TestHelloLineWords(t *testing.T) {
	for _, c := range []struct {
		caps []string
		want string
	}{
		{[]string{"chat"}, "the seat gate offers chat; the phone offers no New project; the phone offers read-only Leads"},
		{[]string{"genesis", "chat"}, "the seat gate offers genesis and chat; the phone offers New project; the phone offers read-only Leads"},
		{nil, "the seat gate offers nothing; the phone offers no New project"},
		{[]string{"genesis"}, "the seat gate offers genesis; the phone offers New project"},
		{[]string{"pair", "read", "login", "decide", "watch", "genesis"}, "the seat gate offers pair, read, login, decide, watch and genesis; the phone offers New project"},
	} {
		if got := helloLine(c.caps); got != c.want {
			t.Errorf("%v: %q", c.caps, got)
		}
	}
}

// The link's refusals for room reach the command's log: with the room of
// the interview spent, the next call is busy, and the journal has the one
// line, which names the kind and holds nothing of the call.
func TestRunLogsTheLinksRefusalForRoom(t *testing.T) {
	dir, fp := seatKey(t)
	gid, addr, _ := startGenesisGate(t, fp, []string{"pair", "read", "genesis"})
	links := watchLink(t)
	got := make(chan seatapp.Daemon, 1)
	daemonHook = func(d seatapp.Daemon) { got <- d }
	t.Cleanup(func() { daemonHook = nil })
	stderr := &lockedBuffer{}
	stop := serveTo(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t),
		"--gate", addr, "--gate-pin-file", pinFile(t, gid.Fingerprint()+"\n", 0o600), "--identity", dir,
		"--decision-guard", stateDir(t)}, stderr)
	waitConnected(t, <-links)
	port, ok := (<-got).(seatapp.Genesis)
	if !ok {
		t.Fatal("the app's daemon is no Genesis port")
	}
	const thread = "0123456789abcdef"
	busy := 0
	for i := 0; i < 20; i++ {
		_, err := port.Genesis(context.Background(), seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: thread})
		if errors.Is(err, seatapp.ErrBusy) {
			busy++
		}
	}
	if code := stop(); code != 0 {
		t.Errorf("shutdown code %d", code)
	}
	if busy == 0 {
		t.Fatal("no call was refused for room")
	}
	out := stderr.String()
	const line = "ataniq-seat: the link has no room for a call of kind genesis (10 of 10 in the last 80 seconds); the page says the desk is busy\n"
	if strings.Count(out, line) != 1 || strings.Count(out, "the link has no room") != 1 {
		t.Errorf("stderr lacks the one line: %q", out)
	}
	if strings.Contains(out, thread) {
		t.Errorf("stderr holds the thread: %q", out)
	}
}
