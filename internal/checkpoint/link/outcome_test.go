package link

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
	"ataniq/internal/seatgate"
)

type rejectApplication struct {
	net.Conn
	partial bool
}

func (c rejectApplication) Write(b []byte) (int, error) {
	if len(b) > 5 && b[0] == 23 && len(b) > 3000 { // The test sends a single large application record.
		if c.partial {
			n, _ := c.Conn.Write(b[:1])
			return n, net.ErrClosed
		}
		return 0, net.ErrClosed
	}
	return c.Conn.Write(b)
}
func TestMutationWriteProgress(t *testing.T) {
	for _, partial := range []bool{false, true} {
		f := newFixture(t, "witness")
		owner := identity(t, "owner")
		e := f.endpoint(testOwner(t, f.s, f.w, f.w.Administration(), owner.Fingerprint()))
		dial := e.DialContext
		e.DialContext = func(ctx context.Context, n, a string) (net.Conn, error) {
			c, err := dial(ctx, n, a)
			return rejectApplication{c, partial}, err
		}
		tr, err := e.transport(owner)
		must(t, err)
		tr.tls.DynamicRecordSizingDisabled = true
		_, err = tr.exchange(t.Context(), frame.New(frame.Release, make([]byte, 4000)))
		want := ErrNotSent
		if partial {
			want = cp.ErrUnknownOutcome
		}
		if !errors.Is(err, want) {
			t.Fatalf("partial %v: %v", partial, err)
		}
	}
}

func TestPermanentHandshakeRefusal(t *testing.T) {
	f := newFixture(t, "witness")
	_, e := f.listener(t)
	tr, err := e.transport(f.id)
	must(t, err)
	tr.tls.NextProtos = []string{"different"}
	var calls atomic.Int32
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, n, a string) (net.Conn, error) { calls.Add(1); return dial(ctx, n, a) }
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = tr.connect(ctx)
	if calls.Load() != 1 || !errors.Is(err, cp.ErrRefused) {
		t.Fatalf("%d attempts: %v", calls.Load(), err)
	}
}

func TestReconnectDelay(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 32 {
		d := retryDelay(time.Second)
		if d < time.Second/2 || d > time.Second {
			t.Fatalf("delay %v", d)
		}
		seen[d] = true
	}
	if len(seen) == 1 {
		t.Fatal("constant reconnect delay")
	}
}

func TestFirstReadAlert(t *testing.T) {
	for _, tc := range []struct {
		code tls.AlertError
		want error
	}{{42, cp.ErrRefused}, {116, cp.ErrRefused}, {20, cp.ErrUnknownOutcome}} {
		err := &net.OpError{Op: "remote error", Err: tc.code}
		if got := replyReadError(err, 0, cp.ErrUnknownOutcome); !errors.Is(got, tc.want) {
			t.Fatalf("alert %v: %v", tc.code, got)
		}
		if got := replyReadError(err, 1, cp.ErrUnknownOutcome); !errors.Is(got, cp.ErrUnknownOutcome) {
			t.Fatal("partial reply lost uncertainty")
		}
	}
}

// Installed after the client handshake. Let the request writer finish
// handling its error before the reader picks up the server's TLS record.
type failedRequestWrite struct{ net.Conn }

func (failedRequestWrite) Write([]byte) (int, error) { return 0, syscall.EPIPE }
func (c failedRequestWrite) Read(p []byte) (int, error) {
	synctest.Wait()
	return c.Conn.Read(p)
}

func TestOwnerReadWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name        string
		provideCert bool
		want        error
	}{
		{"pending certificate alert", false, cp.ErrRefused},
		{"silent peer", true, cp.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				witness, owner := identity(t, "witness"), identity(t, "owner")
				clientSide, serverSide := net.Pipe()
				defer clientSide.Close()
				go func() {
					defer serverSide.Close()
					server := tls.Server(serverSide, serverTLS(witness, owner.Fingerprint()))
					if server.Handshake() == nil {
						// An authenticated peer waits for the request until
						// the client's exchange deadline closes the pipe.
						server.Read(make([]byte, 1))
					}
				}()
				var id seatgate.Identity
				if tc.provideCert {
					id = owner
				}
				raw := &countedConn{Conn: clientSide}
				conn := tls.Client(raw, clientTLS(witness.Fingerprint(), id))
				must(t, conn.Handshake())
				raw.Conn = failedRequestWrite{clientSide}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				request := frame.New(frame.OwnerRead, nil)
				data, err := request.Marshal()
				must(t, err)
				_, err = (transport{}).exchangeOn(ctx, conn, request.Kind, data, true)
				if !errors.Is(err, tc.want) {
					t.Fatalf("owner read: want %v, got %v", tc.want, err)
				}
			})
		})
	}
}

func tcpListener(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("loopback bind denied: %v", err)
	}
	must(t, err)
	return l
}
func TestOwnerRefusesTCP(t *testing.T) {
	l := tcpListener(t)
	defer l.Close()
	f := newFixture(t, "witness")
	if err := f.s.ServeOwner(l, f.w.Administration(), f.w, f.id.Fingerprint(), 0); !errors.Is(err, cp.ErrRefused) {
		t.Fatal(err)
	}
	if _, err := f.endpoint(Remote{Network: "tcp", Address: l.Addr().String()}).Administration(f.id); !errors.Is(err, cp.ErrRefused) {
		t.Fatal(err)
	}
}
