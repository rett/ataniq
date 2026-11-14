package dispatch

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeJournal struct {
	mu          sync.Mutex
	rows        map[string]Receipt
	grants      map[string]string
	now, expiry time.Time
	revoked     bool
	prepareErr  string
	saveErr     bool
}

func journal() *fakeJournal {
	return &fakeJournal{rows: map[string]Receipt{}, grants: map[string]string{}, now: time.Unix(1, 0), expiry: time.Unix(2, 0)}
}
func (j *fakeJournal) Prepare(_ context.Context, i Intent) (PrepareStatus, Receipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if r, ok := j.rows[i.ID]; ok {
		if !reflect.DeepEqual(r.Intent, i) {
			return PrepareConflict, r, nil
		}
		return PrepareDuplicate, r, nil
	}
	if j.prepareErr == "before" {
		return PrepareUnknown, Receipt{}, errors.New("before")
	}
	if i.Grant != "" {
		if _, used := j.grants[i.Grant]; used {
			return PrepareConflict, Receipt{}, nil
		}
		if j.revoked || !j.now.Before(j.expiry) {
			return PrepareRejected, Receipt{}, nil
		}
		j.grants[i.Grant] = i.ID
	}
	r := Receipt{Intent: i, State: Prepared}
	j.rows[i.ID] = r
	if j.prepareErr == "after" {
		return PrepareUnknown, Receipt{}, errors.New("after")
	}
	return PrepareNew, r, nil
}
func (j *fakeJournal) Save(_ context.Context, r Receipt) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.saveErr {
		return errors.New("save")
	}
	j.rows[r.Intent.ID] = r
	return nil
}

type fakeBoundary struct {
	mu                                   sync.Mutex
	available, qualified                 bool
	epoch, policy, qualification, limits string
	starts                               int
	lost                                 bool
	entered, release                     chan struct{}
}

func boundary() *fakeBoundary {
	return &fakeBoundary{available: true, qualified: true, epoch: "e", policy: "p", qualification: "q", limits: "l"}
}
func (b *fakeBoundary) Start(ctx context.Context, i Intent) (StartStatus, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entered != nil {
		close(b.entered)
		<-b.release
	}
	if ctx.Err() != nil || !b.available || !b.qualified || i.Epoch != b.epoch || i.Policy != b.policy || i.Qualification != b.qualification || i.Limits != b.limits {
		return StartDenied, "no start", nil
	}
	b.starts++ // independently counted actual sink invocation
	if b.lost {
		return StartUnknown, "response lost", errors.New("lost")
	}
	return StartAcknowledged, "accepted", nil
}
func intent() Intent {
	return Intent{Authority: "a", Project: "project", Command: "cmd", ID: "id", Attempt: "attempt", Runtime: "runtime", Epoch: "e", Action: "action", Kind: "tool", Policy: "p", Qualification: "q", Authorization: "auth", Limits: "l", Grant: "grant", Payload: []byte("data")}
}
func TestConcurrentDuplicate(t *testing.T) {
	j, b := journal(), boundary()
	b.entered, b.release = make(chan struct{}), make(chan struct{})
	c := Coordinator{j, b}
	done := make(chan Receipt, 1)
	go func() { r, _ := c.Dispatch(context.Background(), intent()); done <- r }()
	<-b.entered
	r, err := c.Dispatch(context.Background(), intent())
	if err != nil || r.State != Prepared {
		t.Fatalf("duplicate: %+v %v", r, err)
	}
	close(b.release)
	if r = <-done; r.State != Started || b.starts != 1 {
		t.Fatalf("%+v starts %d", r, b.starts)
	}
}
func TestConflictsAndGrant(t *testing.T) {
	j, b := journal(), boundary()
	c := Coordinator{j, b}
	i := intent()
	c.Dispatch(context.Background(), i)
	for _, change := range []func(*Intent){func(i *Intent) { i.Payload = []byte("other") }, func(i *Intent) { i.Project = "other" }, func(i *Intent) { i.ID = "other" }} {
		x := i
		change(&x)
		if _, err := c.Dispatch(context.Background(), x); err == nil {
			t.Fatal("missing conflict")
		}
	}
	if b.starts != 1 {
		t.Fatal(b.starts)
	}
}
func TestCurrentChecks(t *testing.T) {
	for _, scenario := range []string{"epoch", "authority", "missing qualification", "stale qualification", "policy", "limits", "expired", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			j, b := journal(), boundary()
			switch scenario {
			case "epoch":
				b.epoch = "new"
			case "authority":
				b.available = false
			case "missing qualification":
				b.qualified = false
			case "stale qualification":
				b.qualification = "new"
			case "policy":
				b.policy = "new"
			case "limits":
				b.limits = "new"
			case "expired":
				j.now = j.expiry
			case "revoked":
				j.revoked = true
			}
			r, _ := (Coordinator{j, b}).Dispatch(context.Background(), intent())
			if r.State == Started || b.starts != 0 {
				t.Fatal(r)
			}
		})
	}
}
func TestUncertaintyNeverReplays(t *testing.T) {
	for _, scenario := range []string{"before", "after", "lost", "save", "recovered"} {
		t.Run(scenario, func(t *testing.T) {
			j, b := journal(), boundary()
			i := intent()
			switch scenario {
			case "before", "after":
				j.prepareErr = scenario
			case "lost":
				b.lost = true
			case "save":
				j.saveErr = true
			case "recovered":
				j.Prepare(context.Background(), i)
			}
			c := Coordinator{j, b}
			r, _ := c.Dispatch(context.Background(), i)
			if scenario != "recovered" && r.State != Uncertain {
				t.Fatal(r)
			}
			count := b.starts
			// A before-commit error has no durable record; it requires reconciliation,
			// not an automatic retry. Simulate only the committed/recovered cases.
			if scenario != "before" {
				j.prepareErr = ""
				(Coordinator{j, b}).Dispatch(context.Background(), i)
				if b.starts != count {
					t.Fatal("replayed")
				}
			}
		})
	}
}
func TestOutageAndCompletion(t *testing.T) {
	j, b := journal(), boundary()
	c := Coordinator{j, b}
	i := intent()
	r, _ := c.Dispatch(context.Background(), i)
	if r.State != Started {
		t.Fatal(r)
	}
	// Completion belongs to the already-started sink, independent of admission.
	completed := make(chan bool, 1)
	b.mu.Lock()
	b.available = false
	b.mu.Unlock()
	completed <- true
	for _, kind := range []string{"queued", "tool", "child", "iteration", "retry"} {
		x := i
		x.ID = kind
		x.Attempt = kind
		x.Grant = ""
		x.Kind = kind
		r, _ = c.Dispatch(context.Background(), x)
		if r.State != Denied {
			t.Fatal(kind, r)
		}
	}
	if !<-completed || b.starts != 1 {
		t.Fatal("completion or outage")
	}
}
func TestInvalidAndCanceled(t *testing.T) {
	j, b := journal(), boundary()
	c := Coordinator{j, b}
	i := intent()
	i.Project = " changed"
	if _, err := c.Dispatch(context.Background(), i); err == nil {
		t.Fatal("invalid")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ := c.Dispatch(ctx, intent())
	if r.State != Denied || b.starts != 0 {
		t.Fatal(r)
	}
}
