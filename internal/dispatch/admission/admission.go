// Package admission adapts a dispatch.Boundary so that every actual start is
// admitted against the ledger and the live facts at that moment, and so that
// the owner of the boundary can close it and learn which starts are in
// flight. It is a thin layer: it changes neither dispatch.Intent nor the
// journal encoding, and it hands out no permit. The head the request was
// submitted at travels in the adapter's own Request, carried by the context
// of the Start call.
package admission

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"ataniq/internal/assignment"
	"ataniq/internal/dispatch"
)

// Request is the admission a start is bound to: the head it was submitted
// against, the submitting holder, the public digest it relied on, the session
// it was bound to, the routes its effect needs and the identity of the one
// dispatch.Intent it may start. Everything the boundary compares a live read
// against comes from the Request, never from the LiveSource, so a source
// cannot vouch for a start it cannot see.
type Request struct {
	Expected     assignment.Head
	Holder       assignment.Binding
	PublicDigest string
	// Session is the session and witness fence the request was bound to. A
	// live session that differs from it, even at the same head, refuses the
	// start.
	Session string
	// Routes are the routes the effect needs; every one must be live. An
	// empty list names no route and refuses the start.
	Routes []string
	// Authority and Epoch are the Authority and Epoch of the one
	// dispatch.Intent this request admits. The dispatch package treats both
	// as opaque, nonempty identity strings that the boundary compares with
	// the authority and epoch it observes, so neither is the ledger
	// holder's authority or its role epoch, and both are carried here as
	// the intent spells them. The intent's Project must be the project of
	// Expected.Key.
	Authority, Epoch string
}

type requestKey struct{}

// WithRequest returns a context whose Start calls are bound to r.
func WithRequest(ctx context.Context, r Request) context.Context {
	r.Routes = slices.Clone(r.Routes)
	return context.WithValue(ctx, requestKey{}, r)
}

// Snapshot is one consistent read of the ledger row and the live facts.
type Snapshot struct {
	Row  assignment.Row
	Live assignment.LiveFacts
}

// LiveSource reads a fresh snapshot. It is called under the boundary's lock
// at every start and must not call back into the boundary. It reports the
// live facts only: the boundary overwrites Live.BoundSession and
// Live.RequiredRoutes with the Request's, so a source sets neither.
type LiveSource interface {
	Snapshot(context.Context) (Snapshot, error)
}

// Entry identifies one admitted start that had not returned from the inner
// boundary when Seal captured it.
type Entry struct {
	Authority, Project, Command, ID, Attempt string
	Expected                                 assignment.Head
}

// Boundary admits each Start with assignment.Admit before it calls the inner
// boundary. One mutex serializes the snapshot, the admission, the entry into
// the in-flight set and Seal, so a start is either entered before a Seal
// (and appears in its result) or refused after it. The inner call runs
// outside the lock, so a slow start never blocks Seal or other starts.
type Boundary struct {
	inner dispatch.Boundary
	src   LiveSource

	mu       sync.Mutex
	sealed   bool
	next     uint64
	inflight map[uint64]Entry
	captured []Entry
}

var _ dispatch.Boundary = (*Boundary)(nil)

// New returns a Boundary over inner that reads src at each start.
func New(inner dispatch.Boundary, src LiveSource) *Boundary {
	return &Boundary{inner: inner, src: src, inflight: map[uint64]Entry{}}
}

func denied(format string, args ...any) (dispatch.StartStatus, string, error) {
	return dispatch.StartDenied, "admission refused: " + fmt.Sprintf(format, args...), nil
}

// Start returns StartDenied, without any call to the inner boundary, when the
// boundary is sealed, the context is done or carries no Request, the intent is
// not the one the Request admits (its project, authority or epoch differs, or
// the Request names none), the Request names no route, the snapshot cannot be
// read, or assignment.Admit refuses. Otherwise it returns
// the inner boundary's result unchanged.
func (b *Boundary) Start(ctx context.Context, in dispatch.Intent) (dispatch.StartStatus, string, error) {
	if b == nil || b.inner == nil || b.src == nil {
		return denied("boundary not configured")
	}
	req, ok := ctx.Value(requestKey{}).(Request)
	b.mu.Lock()
	if b.sealed {
		b.mu.Unlock()
		return denied("sealed")
	}
	if err := ctx.Err(); err != nil {
		b.mu.Unlock()
		return denied("context done")
	}
	if !ok {
		b.mu.Unlock()
		return denied("no bound request")
	}
	if req.Authority == "" || req.Epoch == "" || in.Project != req.Expected.Key.Project ||
		in.Authority != req.Authority || in.Epoch != req.Epoch {
		b.mu.Unlock()
		return denied("intent is not the bound request's")
	}
	if len(req.Routes) == 0 {
		b.mu.Unlock()
		return denied("no required route named")
	}
	snap, err := b.src.Snapshot(ctx)
	if err != nil {
		b.mu.Unlock()
		return denied("live snapshot unavailable")
	}
	live := snap.Live
	live.BoundSession, live.RequiredRoutes = req.Session, req.Routes
	if err := assignment.Admit(snap.Row, req.Expected, req.Holder, req.PublicDigest, live); err != nil {
		b.mu.Unlock()
		return denied("%v", err)
	}
	id := b.next
	b.next++
	b.inflight[id] = Entry{
		Authority: in.Authority, Project: in.Project, Command: in.Command, ID: in.ID,
		Attempt: in.Attempt, Expected: req.Expected,
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.inflight, id)
		b.mu.Unlock()
	}()
	return b.inner.Start(ctx, in)
}

// Seal closes the boundary to new entries and returns the starts that had
// entered the inner boundary and not yet returned, oldest first. Every later
// Start is denied. Seal is idempotent: a repeated call returns the set the
// first call captured. A returned entry may still be starting: the caller
// must reconcile it, not assume it failed.
func (b *Boundary) Seal() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.sealed {
		b.sealed = true
		ids := make([]uint64, 0, len(b.inflight))
		for id := range b.inflight {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			b.captured = append(b.captured, b.inflight[id])
		}
	}
	return slices.Clone(b.captured)
}
