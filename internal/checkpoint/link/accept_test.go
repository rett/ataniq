package link

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
)

type failingAccept struct {
	net.Listener
	failures []error
}

func (l *failingAccept) Accept() (net.Conn, error) {
	err := l.failures[0]
	l.failures = l.failures[1:]
	return nil, &net.OpError{Op: "accept", Err: err}
}
func TestAcceptRetries(t *testing.T) {
	for _, end := range []error{net.ErrClosed, syscall.EINVAL} {
		f := newFixture(t, "witness")
		l, _ := loopback(t)
		defer l.Close()
		scripted := &failingAccept{l, []error{syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM, os.ErrDeadlineExceeded, end}}
		err := f.s.ServeOwner(scripted, f.w.Administration(), f.id.Fingerprint())
		if !errors.Is(err, end) {
			t.Fatalf("accept returned %v, want %v", err, end)
		}
	}
}
