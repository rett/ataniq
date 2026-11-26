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
	trust       TrustSources
	slot        cp.Slot
	home        transport
	listenerTLS *tls.Config
}

func NewEndpoint(id seatgate.Identity, witnessCertDER []byte, signingKey ed25519.PublicKey, epoch uint64, slot cp.Slot, trust TrustSources) (*Endpoint, error) {
	leaf, err := x509.ParseCertificate(slices.Clone(witnessCertDER))
	if err != nil || id.Fingerprint() == "" || epoch == 0 || !validSlot(slot) {
		return nil, cp.ErrRefused
	}
	trust, err = trust.normalized()
	if err != nil {
		return nil, err
	}
	wid, err := cp.KeyID(signingKey)
	if err != nil {
		return nil, err
	}
	pin := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	cfg := serverTLS(id, hex.EncodeToString(pin[:]))
	q := &requestQueue{
		calls:         make(chan pendingCall),
		recoveryCalls: make(chan pendingCall),
		closed:        make(chan struct{}),
	}
	t := transport{Remote: Remote{WitnessPin: slices.Clone(signingKey), Epoch: epoch}, queue: q, wid: wid}
	return &Endpoint{trust, slot, t, cfg}, nil
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

// PendingMutation identifies the canonical intent held for reconciliation.
type PendingMutation struct {
	ID     cp.ID
	Digest string
}

// NewEndpointWithPending restores the home journal's intent before admitting calls.
func NewEndpointWithPending(id seatgate.Identity, cert []byte, pin ed25519.PublicKey, epoch uint64, slot cp.Slot, trust TrustSources, pending PendingMutation) (*Endpoint, error) {
	digest, err := hex.DecodeString(pending.Digest)
	if pending.ID == (cp.ID{}) || len(pending.Digest) != 2*sha256.Size || err != nil || hex.EncodeToString(digest) != pending.Digest {
		return nil, &ConfigError{"invalid pending mutation"}
	}
	e, err := NewEndpoint(id, cert, pin, epoch, slot, trust)
	if err == nil {
		e.home.queue.pending = &pending
	}
	return e, err
}

// PendingMutation returns a snapshot of the retained intent, if any.
func (e *Endpoint) PendingMutation() (PendingMutation, bool) {
	return e.home.queue.pendingMutation()
}
func (q *requestQueue) pendingMutation() (PendingMutation, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending == nil {
		return PendingMutation{}, false
	}
	return *q.pending, true
}

type requestQueue struct {
	mu            sync.Mutex
	submitting    bool
	pending       *PendingMutation
	calls         chan pendingCall
	recoveryCalls chan pendingCall
	closed        chan struct{}
	serving       atomic.Bool
	ordinary      atomic.Bool
	recovery      atomic.Bool
}

func (q *requestQueue) exchange(ctx context.Context, k frame.Kind, data []byte, recovery bool) (answer, error) {
	unsent := cp.ErrUnavailable
	if k.Mutation() {
		unsent = ErrNotSent
	}
	calls := q.calls
	lane := &q.ordinary
	if recovery {
		lane = &q.recovery
		calls = q.recoveryCalls
	}
	if !lane.CompareAndSwap(false, true) {
		return answer{}, errors.Join(unsent, cp.ErrBusy)
	}
	defer lane.Store(false)
	call := pendingCall{ctx, k, data, make(chan callResult, 1)}
	select {
	case calls <- call:
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

// Take an already waiting recovery call before considering the ordinary lane.
func (q *requestQueue) nextCall() (pendingCall, bool) {
	select {
	case call := <-q.recoveryCalls:
		return call, true
	default:
	}
	select {
	case call := <-q.calls:
		return call, true
	case call := <-q.recoveryCalls:
		return call, true
	default:
		return pendingCall{}, false
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
	return serveListener(l, e.listenerTLS, 1, requestTimeout, homeAdmission(e.trust), func(ctx context.Context, c *tls.Conn, m frame.Message) {
		if m.Kind != frame.Poll {
			return
		}
		if call, ready := q.nextCall(); ready {
			deadline, _ := ctx.Deadline()
			exchange, cancel := context.WithDeadline(call.ctx, deadline)
			a, err := e.home.exchangeOn(exchange, c, call.kind, call.data, false)
			cancel()
			call.result <- callResult{a, err}
		} else {
			_ = frame.New(frame.Idle).WriteToStream(c)
		}
	})
}
