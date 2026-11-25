package clientapi

import (
	"context"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// emfileListener fails its first Accept as a listener out of descriptors
// does, and accepts on the listener it wraps after that.
type emfileListener struct {
	net.Listener
	failed atomic.Int32
}

func (l *emfileListener) Accept() (net.Conn, error) {
	if l.failed.CompareAndSwap(0, 1) {
		return nil, &net.OpError{Op: "accept", Net: "unix", Err: os.NewSyscallError("accept4", syscall.EMFILE)}
	}
	return l.Listener.Accept()
}

// An accept that fails with EMFILE is retried after a backoff, and the server
// goes on serving.
func TestServeRetriesAnAcceptThatFailsWithEMFILE(t *testing.T) {
	inner, path, err := Listen(socketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	l := &emfileListener{Listener: inner}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, l, &stubHandler{version: "v-test"}, Options{}) }()

	callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer callCancel()
	c, err := Dial(callCtx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	hello, err := c.Hello(callCtx)
	if err != nil {
		t.Fatalf("hello after a failed accept: %v", err)
	}
	if hello.Version != "v-test" {
		t.Fatalf("hello = %+v", hello)
	}
	if n := l.failed.Load(); n != 1 {
		t.Fatalf("the listener failed %d times, want 1", n)
	}
	select {
	case err := <-done:
		t.Fatalf("Serve returned %v while it should be serving", err)
	default:
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v after its context ended", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context ended")
	}
}
