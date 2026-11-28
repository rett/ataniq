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
			Activation: true, Session: "s1", BoundSession: "s1", RoleEpoch: 3,
			RequiredRoutes: []string{"r1"}, LiveRoutes: []string{"r1"},
		},
	}
}

func bound(ctx context.Context) context.Context {
	return WithRequest(ctx, Request{Expected: head(), Holder: holder(), PublicDigest: "digest"})
}

func intent(id string) dispatch.Intent { return dispatch.Intent{ID: id, Attempt: "1", Project: "p"} }

type source struct {
	mu   sync.Mutex
	snap Snapshot
	err  error
	// hook runs inside Snapshot, under the boundary's lock.
	hook func()
}

func (s *source) Snapshot(context.Context) (Snapshot, error) {
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
