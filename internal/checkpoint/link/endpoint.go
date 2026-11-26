package link

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"slices"
	"sync/atomic"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
	"ataniq/internal/seatgate"
)

// Endpoint listens at the home. Its clients share one bounded handoff queue.
type Endpoint struct {
	slot        cp.Slot
	home        transport
	listenerTLS *tls.Config
}

func NewEndpoint(id seatgate.Identity, witnessCertDER []byte, signingKey ed25519.PublicKey, epoch uint64, slot cp.Slot) (*Endpoint, error) {
	leaf, err := x509.ParseCertificate(slices.Clone(witnessCertDER))
	if err != nil || id.Fingerprint() == "" || epoch == 0 || !validSlot(slot) {
		return nil, cp.ErrRefused
	}
	wid, err := cp.KeyID(signingKey)
	if err != nil {
		return nil, err
	}
	pin := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	cfg := serverTLS(id, hex.EncodeToString(pin[:]))
	q := &requestQueue{
		calls:  make(chan pendingCall),
		closed: make(chan struct{}),
	}
	t := transport{Remote: Remote{WitnessPin: slices.Clone(signingKey), Epoch: epoch}, queue: q, wid: wid}
	return &Endpoint{slot, t, cfg}, nil
}

type pendingCall struct {
	ctx    context.Context
	kind   frame.Kind
	data   []byte
	result chan callResult
}
type callResult struct {
	answer answer
	err    error
}
type requestQueue struct {
	calls    chan pendingCall
	closed   chan struct{}
	serving  atomic.Bool
	ordinary atomic.Bool
	recovery atomic.Bool
}

func (q *requestQueue) exchange(ctx context.Context, k frame.Kind, data []byte, recovery bool) (answer, error) {
	unsent := cp.ErrUnavailable
	if k == frame.Claim || k == frame.Advance {
		unsent = ErrNotSent
	}
	lane := &q.ordinary
	if recovery {
		lane = &q.recovery
	}
	if !lane.CompareAndSwap(false, true) {
		return answer{}, errors.Join(unsent, cp.ErrBusy)
	}
	defer lane.Store(false)
	call := pendingCall{ctx, k, data, make(chan callResult, 1)}
	select {
	case q.calls <- call:
		// The worker observes cancellation on the socket and classifies how
		// much of the request was written before releasing this reservation.
		r := <-call.result
		return r.answer, r.err
	case <-ctx.Done():
		return answer{}, errors.Join(unsent, ctx.Err())
	case <-q.closed:
		return answer{}, unsent
	}
}

// Serve accepts witness polls until the supplied listener is closed. An
// endpoint serves once. Shutdown wakes waiting clients and joins its workers.
func (e *Endpoint) Serve(l net.Listener) error {
	q := e.home.queue
	if q == nil || !q.serving.CompareAndSwap(false, true) {
		return cp.ErrRefused
	}
	defer close(q.closed)
	return serveListener(l, e.listenerTLS, 1, requestTimeout, func(ctx context.Context, c *tls.Conn, m frame.Message) {
		if m.Kind != frame.Poll {
			return
		}
		select {
		case call := <-q.calls:
			deadline, _ := ctx.Deadline()
			exchange, cancel := context.WithDeadline(call.ctx, deadline)
			a, err := exchangeOn(exchange, c, call.kind, call.data, false)
			cancel()
			call.result <- callResult{a, err}
		default:
			_ = frame.New(frame.Idle).WriteToStream(c)
		}
	})
}
