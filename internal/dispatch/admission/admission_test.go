package admission

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/assignment"
	"ataniq/internal/dispatch"
)

const wait = 5 * time.Second

func head() assignment.Head {
	return assignment.Head{
		BindingID: "b1", Generation: 2, Key: assignment.Key{Home: "h", Project: "p", Role: "r"},
		Phase: assignment.PhaseConfirmed, Revision: 7, TransferID: "t1",
	}
}

func holder() assignment.Binding {
	return assignment.Binding{
		Authority: "a", Conversation: "c", ID: "b1", Incarnation: "i",
		Kind: "managed", LaunchIntent: "l", RoleEpoch: 3, Store: "s",
	}
}

func snapshot() Snapshot {
	return Snapshot{
		Row: assignment.Row{Head: head(), Holder: holder(), PublicDigest: "digest"},
		Live: assignment.LiveFacts{
			// BoundSession and RequiredRoutes are the request's to name: the
			// boundary overwrites whatever a source puts there, so set them
			// to values that would admit nothing if they were believed.
			Activation: true, Session: "s1", BoundSession: "ignored", RoleEpoch: 3,
			RequiredRoutes: nil, LiveRoutes: []string{"r1"},
		},
	}
}

func request() Request {
	return Request{
		Expected: head(), Holder: holder(), PublicDigest: "digest",
		Session: "s1", Routes: []string{"r1"}, Authority: "auth", Epoch: "e1",
	}
}

func bound(ctx context.Context) context.Context { return WithRequest(ctx, request()) }

func intent(id string) dispatch.Intent {
	return dispatch.Intent{ID: id, Attempt: "1", Project: "p", Authority: "auth", Epoch: "e1"}
}

type source struct {
	mu   sync.Mutex
	snap Snapshot
	err  error
	// hook runs inside Snapshot, under the boundary's lock.
	hook  func()
	reads atomic.Int32
}

func (s *source) Snapshot(context.Context) (Snapshot, error) {
	s.reads.Add(1)
	s.mu.Lock()
	snap, err, hook := s.snap, s.err, s.hook
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return snap, err
}

func (s *source) set(f func(*Snapshot)) {
	s.mu.Lock()
	f(&s.snap)
	s.mu.Unlock()
}

type inner struct {
	calls   atomic.Int32
	entered chan string   // receives the intent ID on entry when non-nil
	release chan struct{} // entry blocks until closed when non-nil
}

func (i *inner) Start(_ context.Context, in dispatch.Intent) (dispatch.StartStatus, string, error) {
	i.calls.Add(1)
	if i.entered != nil {
		i.entered <- in.ID
	}
	if i.release != nil {
		<-i.release
	}
	return dispatch.StartAcknowledged, "inner started " + in.ID, nil
}

type result struct {
	status dispatch.StartStatus
	detail string
	err    error
}

func start(b *Boundary, ctx context.Context, id string) <-chan result {
	out := make(chan result, 1)
	go func() {
		s, d, e := b.Start(ctx, intent(id))
		out <- result{s, d, e}
	}()
	return out
}

func recv[T any](t *testing.T, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(wait):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func notYet[T any](t *testing.T, c <-chan T, what string) {
	t.Helper()
	select {
	case <-c:
		t.Fatalf("%s happened too early", what)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestAdmissionBindsTheIntentAndTheFencesToTheRequest: what the boundary
// compares the live read against comes from the request, never from the
// source, and an intent that is not the request's never reaches the snapshot.
func TestAdmissionBindsTheIntentAndTheFencesToTheRequest(t *testing.T) {
	refused := func(t *testing.T, name string, req func(*Request), in func(*dispatch.Intent), live func(*Snapshot), want string, wantReads int32) {
		t.Run(name, func(t *testing.T) {
			inn, src := &inner{}, &source{snap: snapshot()}
			if live != nil {
				live(&src.snap)
			}
			r := request()
			if req != nil {
				req(&r)
			}
			it := intent("x")
			if in != nil {
				in(&it)
			}
			s, d, err := New(inn, src).Start(WithRequest(context.Background(), r), it)
			if s != dispatch.StartDenied || err != nil || !strings.Contains(d, want) {
				t.Fatalf("Start = %v %q %v, want denied containing %q", s, d, err, want)
			}
			if c := inn.calls.Load(); c != 0 {
				t.Fatalf("inner started %d times", c)
			}
			if n := src.reads.Load(); n != wantReads {
				t.Fatalf("snapshot read %d times, want %d", n, wantReads)
			}
		})
	}
	const foreign = "intent is not the bound request's"
	refused(t, "intent of another project", nil, func(i *dispatch.Intent) { i.Project = "other-project" }, nil, foreign, 0)
	refused(t, "intent of another authority", nil, func(i *dispatch.Intent) { i.Authority = "other-authority" }, nil, foreign, 0)
	refused(t, "intent of another epoch", nil, func(i *dispatch.Intent) { i.Epoch = "99" }, nil, foreign, 0)
	refused(t, "all three foreign", nil, func(i *dispatch.Intent) {
		i.Project, i.Authority, i.Epoch = "other-project", "other-authority", "99"
	}, nil, foreign, 0)
	refused(t, "request names no authority", func(r *Request) { r.Authority = "" }, func(i *dispatch.Intent) { i.Authority = "" }, nil, foreign, 0)
	refused(t, "request names no epoch", func(r *Request) { r.Epoch = "" }, func(i *dispatch.Intent) { i.Epoch = "" }, nil, foreign, 0)
	// The ledger holder's authority and role epoch are not the intent's: an
	// intent spelled from them is not the request's.
	refused(t, "holder identity is not the intent identity", nil, func(i *dispatch.Intent) {
		i.Authority, i.Epoch = holder().Authority, "3"
	}, nil, foreign, 0)

	// A release replaced session s1 with s2 at the same head, and the source
	// reports the new session as the bound one too: only the request knows s1.
	refused(t, "session replaced, source vouches for the new one", nil, nil, func(s *Snapshot) {
		s.Live.Session, s.Live.BoundSession = "s2", "s2"
	}, assignment.ReasonSessionChanged.Error(), 1)
	refused(t, "request bound to no session", func(r *Request) { r.Session = "" }, nil, nil, assignment.ReasonSessionChanged.Error(), 1)
	refused(t, "no live session", nil, nil, func(s *Snapshot) {
		s.Live.Session, s.Live.BoundSession = "", ""
	}, assignment.ReasonSessionLost.Error(), 1)

	// An empty route list names no route: it is refused, not vacuously met,
	// whatever the source lists as required.
	refused(t, "no route named", func(r *Request) { r.Routes = nil }, nil, func(s *Snapshot) {
		s.Live.RequiredRoutes = []string{"r1"}
	}, "no required route named", 0)
	refused(t, "empty route list", func(r *Request) { r.Routes = []string{} }, nil, nil, "no required route named", 0)
	// A route only the request needs is checked, though the source lists
	// none as required.
	refused(t, "required route not live", func(r *Request) { r.Routes = []string{"r1", "r2"} }, nil, nil, assignment.ReasonRouteMissing.Error(), 1)

	t.Run("a source cannot widen what the request needs", func(t *testing.T) {
		inn, src := &inner{}, &source{snap: snapshot()}
		src.snap.Live.RequiredRoutes = []string{"elsewhere"}
		s, d, err := New(inn, src).Start(bound(context.Background()), intent("ok"))
		if s != dispatch.StartAcknowledged || err != nil || inn.calls.Load() != 1 {
			t.Fatalf("Start = %v %q %v, inner calls %d", s, d, err, inn.calls.Load())
		}
	})

	t.Run("the request's routes are copied at binding", func(t *testing.T) {
		inn, src := &inner{}, &source{snap: snapshot()}
		r := request()
		ctx := WithRequest(context.Background(), r)
		r.Routes[0] = "r2" // after binding: must not change the bound request
		if s, d, err := New(inn, src).Start(ctx, intent("ok")); s != dispatch.StartAcknowledged || err != nil {
			t.Fatalf("Start = %v %q %v, want the bound routes to hold", s, d, err)
		}
	})
}

func TestAdmissionBoundary(t *testing.T) {
	t.Run("SealBeforeStartDenies", func(t *testing.T) {
		in, src := &inner{}, &source{snap: snapshot()}
		b := New(in, src)
		gate := make(chan struct{})
		sealed := make(chan []Entry, 1)
		got := make(chan result, 1)
		go func() {
			<-gate // the start begins only once the seal is complete
			s, d, e := b.Start(bound(context.Background()), intent("x"))
			got <- result{s, d, e}
		}()
		go func() { sealed <- b.Seal(); close(gate) }()
		if entries := recv(t, sealed, "seal"); len(entries) != 0 {
			t.Fatalf("Seal inventoried %v, want none", entries)
		}
		r := recv(t, got, "start")
		if r.status != dispatch.StartDenied || r.err != nil || !strings.Contains(r.detail, "sealed") {
			t.Fatalf("Start = %+v, want denied as sealed", r)
		}
		if n := in.calls.Load(); n != 0 {
			t.Fatalf("inner started %d times after Seal", n)
		}
		if again := b.Seal(); len(again) != 0 {
			t.Fatalf("second Seal = %v", again)
		}
	})

	t.Run("StartBeforeSealIsInventoried", func(t *testing.T) {
		in := &inner{entered: make(chan string), release: make(chan struct{})}
		b := New(in, &source{snap: snapshot()})
		first := start(b, bound(context.Background()), "first")
		if id := recv(t, in.entered, "inner entry"); id != "first" {
			t.Fatalf("inner entered with %q", id)
		}
		entries := b.Seal() // the inner start is still running
		if len(entries) != 1 || entries[0].ID != "first" || entries[0].Expected != head() || entries[0].Project != "p" {
			t.Fatalf("Seal = %+v, want the one in-flight start", entries)
		}
		late := start(b, bound(context.Background()), "late")
		if r := recv(t, late, "late start"); r.status != dispatch.StartDenied {
			t.Fatalf("start after Seal = %+v, want denied", r)
		}
		close(in.release)
		if r := recv(t, first, "first start"); r.status != dispatch.StartAcknowledged || r.err != nil {
			t.Fatalf("in-flight start = %+v, want the inner result unchanged", r)
		}
		if n := in.calls.Load(); n != 1 {
			t.Fatalf("inner started %d times, want 1", n)
		}
		if again := b.Seal(); len(again) != 1 || again[0].ID != "first" {
			t.Fatalf("repeated Seal = %+v, want the captured set", again)
		}
	})

	t.Run("SealWaitsForAdmissionUnderTheLock", func(t *testing.T) {
		in := &inner{}
		src := &source{snap: snapshot()}
		inSnapshot, releaseSnapshot := make(chan struct{}), make(chan struct{})
		var once sync.Once
		src.hook = func() { once.Do(func() { close(inSnapshot); <-releaseSnapshot }) }
		b := New(in, src)
		first := start(b, bound(context.Background()), "first")
		recv(t, inSnapshot, "snapshot entry")
		sealed := make(chan []Entry, 1)
		go func() { sealed <- b.Seal() }()
		notYet(t, sealed, "Seal while a start holds the lock")
		close(releaseSnapshot)
		entries := recv(t, sealed, "seal")
		r := recv(t, first, "first start")
		// The start was admitted before the seal took the lock, so it
		// reached the inner boundary: either still in flight at Seal (and
		// inventoried) or already returned. Never started and unseen.
		if r.status != dispatch.StartAcknowledged || in.calls.Load() != 1 {
			t.Fatalf("start = %+v, inner calls %d", r, in.calls.Load())
		}
		if len(entries) > 1 || (len(entries) == 1 && entries[0].ID != "first") {
			t.Fatalf("Seal = %+v", entries)
		}
	})

	t.Run("ChangedHeadZeroInnerStarts", func(t *testing.T) {
		in, src := &inner{}, &source{snap: snapshot()}
		b := New(in, src)
		// The ledger moved on after submission: its own head still permits
		// admission, but the head the request was bound to no longer holds.
		src.set(func(s *Snapshot) { s.Row.Head.Revision++ })
		const n = 8
		barrier := make(chan struct{})
		results := make(chan result, n)
		for i := 0; i < n; i++ {
			go func() {
				<-barrier
				s, d, e := b.Start(bound(context.Background()), intent("x"))
				results <- result{s, d, e}
			}()
		}
		close(barrier)
		for i := 0; i < n; i++ {
			r := recv(t, results, "denied start")
			if r.status != dispatch.StartDenied || r.err != nil || !strings.Contains(r.detail, assignment.ReasonHeadChanged.Error()) {
				t.Fatalf("Start = %+v, want denied for the changed head", r)
			}
		}
		if c := in.calls.Load(); c != 0 {
			t.Fatalf("inner started %d times for a changed head", c)
		}
		if entries := b.Seal(); len(entries) != 0 {
			t.Fatalf("Seal = %v, want none", entries)
		}
	})

	t.Run("RefusalsNeverReachTheInnerBoundary", func(t *testing.T) {
		refusals := []struct {
			name string
			ctx  func() (context.Context, context.CancelFunc)
			mut  func(*source)
			want string
		}{
			{"no bound request", func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			}, nil, "no bound request"},
			{"canceled", func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(bound(context.Background()))
				cancel()
				return ctx, cancel
			}, nil, "context done"},
			{"snapshot error", nil, func(s *source) { s.err = errors.New("boom") }, "snapshot unavailable"},
			{"session replaced", nil, func(s *source) {
				s.snap.Live.Session = "s2"
			}, assignment.ReasonSessionChanged.Error()},
			{"activation off", nil, func(s *source) { s.snap.Live.Activation = false }, assignment.ReasonActivationOff.Error()},
		}
		for _, tc := range refusals {
			t.Run(tc.name, func(t *testing.T) {
				in, src := &inner{}, &source{snap: snapshot()}
				if tc.mut != nil {
					tc.mut(src)
				}
				ctx, cancel := context.WithCancel(bound(context.Background()))
				defer cancel()
				if tc.ctx != nil {
					ctx, cancel = tc.ctx()
					defer cancel()
				}
				s, d, err := New(in, src).Start(ctx, intent("x"))
				if s != dispatch.StartDenied || err != nil || !strings.Contains(d, tc.want) {
					t.Fatalf("Start = %v %q %v, want denied containing %q", s, d, err, tc.want)
				}
				if c := in.calls.Load(); c != 0 {
					t.Fatalf("inner started %d times", c)
				}
			})
		}
	})

	t.Run("AdmittedStartPassesThrough", func(t *testing.T) {
		in := &inner{}
		s, d, err := New(in, &source{snap: snapshot()}).Start(bound(context.Background()), intent("ok"))
		if s != dispatch.StartAcknowledged || err != nil || d != "inner started ok" || in.calls.Load() != 1 {
			t.Fatalf("Start = %v %q %v, inner calls %d", s, d, err, in.calls.Load())
		}
	})
}
