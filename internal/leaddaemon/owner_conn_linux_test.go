//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"net"
	"sync"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
)

// TestOwnerGoroutineIDs: the id is the calling goroutine's, the same on every
// call and different in another goroutine.
func TestOwnerGoroutineIDs(t *testing.T) {
	mine := goroutineID()
	if mine == 0 || goroutineID() != mine {
		t.Fatalf("id %d then %d", mine, goroutineID())
	}
	other := make(chan uint64)
	go func() { other <- goroutineID() }()
	if got := <-other; got == 0 || got == mine {
		t.Fatalf("another goroutine has id %d, this one %d", got, mine)
	}
}

// ownerPipeConn makes a connection of the registry over a pipe.
func ownerPipeConn(t *testing.T, reg *ownerConns) (*ownerConn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return reg.wrap(a, testNow), b
}

var testNow = t0

// TestOwnerAConnectionIsFoundByTheGoroutineThatReadsIt: after the first Read a
// goroutine finds its connection; another goroutine, and a goroutine that never
// read, find none; after Close nothing finds it and the decider's place is free.
func TestOwnerAConnectionIsFoundByTheGoroutineThatReadsIt(t *testing.T) {
	reg := newOwnerConns()
	c1, peer1 := ownerPipeConn(t, reg)
	c2, peer2 := ownerPipeConn(t, reg)

	var wg sync.WaitGroup
	found := make(chan [2]*ownerConn, 2)
	read := func(c *ownerConn, peer net.Conn) {
		defer wg.Done()
		go peer.Write([]byte("x"))
		buf := make([]byte, 1)
		if _, err := c.Read(buf); err != nil {
			t.Error(err)
		}
		found <- [2]*ownerConn{c, reg.current()}
		<-make(chan struct{}) // keep the goroutine, and so its id, until the test ends
	}
	wg.Add(2)
	go read(c1, peer1)
	go read(c2, peer2)
	for i := 0; i < 2; i++ {
		got := <-found
		if got[1] != got[0] {
			t.Fatalf("a goroutine found %p, want its own connection %p", got[1], got[0])
		}
	}
	if reg.current() != nil {
		t.Fatal("a goroutine that read no connection found one")
	}

	if st := reg.grant(c1, ownerTTY); st != seatGranted {
		t.Fatalf("grant: %v", st)
	}
	if st := reg.grant(c2, ownerTTY); st != seatTaken {
		t.Fatalf("a second grant: %v", st)
	}
	if !reg.isDecider(c1) || reg.isDecider(c2) {
		t.Fatal("the place is not c1's alone")
	}
	c1.Close()
	c1.Close() // twice is safe
	if reg.isDecider(c1) {
		t.Fatal("a closed connection is the decider")
	}
	if st := reg.grant(c1, ownerTTY); st != seatClosed {
		t.Fatalf("a closed connection was granted the place: %v", st)
	}
	if st := reg.grant(c2, ownerTTY); st != seatGranted {
		t.Fatalf("the freed place was not given: %v", st)
	}
	reg.mu.Lock()
	n := len(reg.byGID)
	reg.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d connections are still bound after one closed", n)
	}
}

// TestOwnerARequestThatCannotBeTiedToAConnectionIsRefused: Handle called from a
// goroutine that no connection reads in (which the server never does) fails
// closed for every operation that needs the connection: nothing is granted,
// the inspector is not asked, the service is not called, and it is counted. The
// reads, which need no connection, are served.
func TestOwnerARequestThatCannotBeTiedToAConnectionIsRefused(t *testing.T) {
	r := newUnstartedOwnerRig(t)
	ext := &ownerExt{oc: r.oc, svc: r.svc, reg: newOwnerConns()}
	deciderParams := []byte(`{"client":"ataniq-console 1","session":"0123456789abcdef"}`)
	frame := []byte(`{"binding_sha256":"` + string(make([]byte, 0)) + `abababababababababababababababababababababababababababababababab","decision":"approve","id":"ap-one","signed_off":[]}`)
	for _, tt := range []struct {
		op     string
		params []byte
	}{
		{ownerwire.OpDecider, deciderParams},
		{ownerwire.OpDecide, frame},
		{ownerwire.OpBeat, nil},
		{ownerwire.OpLimitSet, []byte(`{"project":"p","to":3,"typed":false}`)},
	} {
		res, fail := ext.Handle(context.Background(), tt.op, tt.params)
		if res != nil || fail == nil || fail.Code != ownerCodeUnavailable || fail.Message != msgOwnerNoConn {
			t.Errorf("%s: %v %+v", tt.op, res, fail)
		}
	}
	if len(r.insp.inspected()) != 0 || len(r.svc.called()) != 0 {
		t.Fatalf("something was done: %v %v", r.insp.inspected(), r.svc.called())
	}
	if got := r.oc.Counts().NoConnection.Load(); got != 4 {
		t.Fatalf("%d refusals counted", got)
	}
	if _, fail := ext.Handle(context.Background(), ownerwire.OpList, nil); fail != nil {
		t.Fatalf("list: %+v", fail)
	}
}

// TestOwnerTheServersOwnPeerCheckStillHolds: the wrapper does not take the
// server's check for the peer's user away. A peer check set in the options
// (here one that says another user) is the one called, with the socket and not
// with the wrapper, and a connection it refuses is answered unauthorized and
// reaches no operation.
func TestOwnerTheServersOwnPeerCheckStillHolds(t *testing.T) {
	r := newUnstartedOwnerRig(t)
	var mu sync.Mutex
	var types []string
	other := false
	cfg := r.config()
	cfg.Server.PeerUID = func(c net.Conn) (uint32, error) {
		mu.Lock()
		types = append(types, typeName(c))
		refuse := other
		mu.Unlock()
		uid, err := clientapi.PeerUID(c)
		if refuse {
			uid++
		}
		return uid, err
	}
	r.run = r.e.startDaemon(cfg)
	h := ownerHello(t, r.run.socket)
	if len(h.Operations) != 12 {
		t.Fatalf("%v", h.Operations)
	}
	mu.Lock()
	if len(types) == 0 || types[0] != "*net.UnixConn" {
		t.Fatalf("the peer check was given %v, want the socket", types)
	}
	other = true
	mu.Unlock()

	c, err := clientapi.Dial(ownerCtx(t), r.run.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call(ownerCtx(t), ownerwire.OpList, nil, nil)
	if err == nil {
		t.Fatal("a peer of another user was served")
	}
	wantOwnerCode(t, err, clientapi.CodeUnauthorized)
	if got := r.svc.called(); len(got) != 0 {
		t.Fatalf("the service was called: %v", got)
	}
}

func typeName(v any) string {
	switch v.(type) {
	case *net.UnixConn:
		return "*net.UnixConn"
	case *ownerConn:
		return "*ownerConn"
	}
	return "other"
}
