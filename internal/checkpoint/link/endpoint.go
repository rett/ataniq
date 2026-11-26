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
	"sync"
	"sync/atomic"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
	"ataniq/internal/seatgate"
)

// Endpoint listens at the home. Its clients share one bounded handoff queue.
type Endpoint struct {
	home        transport
	listenerTLS *tls.Config
}

func NewEndpoint(id seatgate.Identity, witnessCertDER []byte, signingKey ed25519.PublicKey, epoch uint64) (*Endpoint, error) {
	leaf, err := x509.ParseCertificate(slices.Clone(witnessCertDER))
	if err != nil || id.Fingerprint() == "" || epoch == 0 {
		return nil, cp.ErrRefused
	}
	wid, err := cp.KeyID(signingKey)
	if err != nil {
		return nil, err
	}
	pin := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	cfg := serverTLS(id, hex.EncodeToString(pin[:]))
	q := &requestQueue{
		calls:     make(chan pendingCall),
		closed:    make(chan struct{}),
		capacity:  make(chan struct{}, 8),
		mutations: make(map[cp.Slot]chan struct{}),
	}
	t := transport{Remote: Remote{WitnessPin: slices.Clone(signingKey), Epoch: epoch}, queue: q, wid: wid}
	return &Endpoint{t, cfg}, nil
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
	calls     chan pendingCall
	closed    chan struct{}
	serving   atomic.Bool
	capacity  chan struct{}
	mu        sync.Mutex
	mutations map[cp.Slot]chan struct{}
}

// Serialize callers before handing a mutation to a poll. At most the fixed
// caller capacity can wait, including other callers for this enrollment.
func (q *requestQueue) reserve(ctx context.Context, slot cp.Slot) error {
	for {
		q.mu.Lock()
		active := q.mutations[slot]
		if active == nil {
			q.mutations[slot] = make(chan struct{})
			q.mu.Unlock()
			return nil
		}
		q.mu.Unlock()
		select {
		case <-active:
		case <-ctx.Done():
			return errors.Join(ErrNotSent, ctx.Err())
		case <-q.closed:
			return ErrNotSent
		}
	}
}
func (q *requestQueue) release(slot cp.Slot) {
	q.mu.Lock()
	defer q.mu.Unlock()
	close(q.mutations[slot])
	delete(q.mutations, slot)
}
func (q *requestQueue) exchange(ctx context.Context, k frame.Kind, data []byte, slot cp.Slot) (answer, error) {
	unsent := cp.ErrUnavailable
	if k == frame.Claim || k == frame.Advance {
		unsent = ErrNotSent
	}
	select {
	case q.capacity <- struct{}{}:
		defer func() { <-q.capacity }()
	default:
		return answer{}, unsent
	}
	if k == frame.Claim || k == frame.Advance {
		if err := q.reserve(ctx, slot); err != nil {
			return answer{}, err
		}
		defer q.release(slot)
	}
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
	return serveListener(l, e.listenerTLS, make(chan struct{}, 8), requestTimeout, func(ctx context.Context, c *tls.Conn, m frame.Message) {
		if m.Kind != frame.Poll {
			return
		}
		select {
		case call := <-q.calls:
			a, err := exchangeOn(call.ctx, c, call.kind, call.data, false)
			call.result <- callResult{a, err}
		default:
			_ = frame.New(frame.Idle).WriteToStream(c)
		}
	})
}
