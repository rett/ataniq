package dispatch

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeJournal struct {
	mu             sync.Mutex
	rows           map[string]Receipt
	grants         map[string]Intent
	used           map[string]bool
	contradictions []Receipt
	evidence       map[string]map[string]Receipt
	fenced         map[string]Intent
	uncertain      map[string]bool
	now, expiry    time.Time
	revoked        bool
	prepareErr     string
	saveErr        bool
}

func journal() *fakeJournal {
	return &fakeJournal{rows: map[string]Receipt{}, grants: map[string]Intent{"grant": intent()}, used: map[string]bool{}, evidence: map[string]map[string]Receipt{}, fenced: map[string]Intent{}, uncertain: map[string]bool{}, now: time.Unix(1, 0), expiry: time.Unix(2, 0)}
}
func (j *fakeJournal) Prepare(_ context.Context, i Intent) (PrepareStatus, Receipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if r, ok := j.rows[i.ID]; ok {
		if !sameIntent(r.Intent, i) {
			j.contradictions = append(j.contradictions, Receipt{Intent: cloneIntent(i), State: Uncertain, Evidence: "intent contradiction"})
			return PrepareConflict, r, nil
		}
		r.Intent = cloneIntent(r.Intent)
		if j.uncertain[i.ID] {
			r.State = Uncertain
		}
		return PrepareDuplicate, r, nil
	}
	if f, ok := j.fenced[i.ID]; ok {
		if !sameIntent(f, i) {
			return PrepareConflict, Receipt{}, nil
		}
		return PrepareDuplicate, Receipt{Intent: cloneIntent(f), State: Uncertain}, nil
	}
	if j.prepareErr == "before" {
		j.fenced[i.ID] = cloneIntent(i)
		return PrepareUnknown, Receipt{}, errors.New("before")
	}
	if i.Grant != "" {
		if target, ok := j.grants[i.Grant]; !ok || !sameIntent(target, i) || j.used[i.Grant] {
			j.contradictions = append(j.contradictions, Receipt{Intent: cloneIntent(i), State: Uncertain, Evidence: "grant contradiction"})
			return PrepareConflict, Receipt{}, nil
		}
		if j.revoked || !j.now.Before(j.expiry) {
			return PrepareRejected, Receipt{}, nil
		}
		j.used[i.Grant] = true
	}
	r := Receipt{Intent: cloneIntent(i), State: Prepared}
	j.rows[i.ID] = r
	if j.prepareErr == "after" {
		return PrepareUnknown, Receipt{}, errors.New("after")
	}
	r.Intent = cloneIntent(r.Intent)
	return PrepareNew, r, nil
}
func (j *fakeJournal) Save(_ context.Context, r Receipt) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.saveErr {
		return errors.New("save")
	}
	old, ok := j.rows[r.Intent.ID]
	if !ok || !sameIntent(old.Intent, r.Intent) {
		r.Intent = cloneIntent(r.Intent)
		j.contradictions = append(j.contradictions, r)
		return errors.New("receipt contradiction")
	}
	if j.evidence[r.Intent.ID] == nil {
		j.evidence[r.Intent.ID] = map[string]Receipt{}
	}
	evidence := j.evidence[r.Intent.ID]
	if prior, exists := evidence[r.ID]; exists && sameIntent(prior.Intent, r.Intent) && prior.State == r.State && prior.Evidence == r.Evidence {
		return nil
	}
	conflict := false
	for id, prior := range evidence {
		if id == r.ID || (prior.State != r.State && prior.State != Uncertain && r.State != Uncertain) {
			conflict = true
		}
	}
	r.Intent = cloneIntent(r.Intent)
	if conflict {
		j.contradictions = append(j.contradictions, r)
		j.uncertain[r.Intent.ID] = true
		return errors.New("receipt contradiction")
	}
	evidence[r.ID] = r
	j.rows[r.Intent.ID] = r
	return nil
}

type fakeBoundary struct {
	mu                                   sync.Mutex
	available, qualified                 bool
	epoch, policy, qualification, limits string
	authority, runtime, authorization    string
	starts                               int
	lost                                 bool
	entered, release                     chan struct{}
	work, completed                      chan struct{}
	completionEvidence                   []string
	grantRequired, grantRevoked          bool
	now, expiry                          time.Time
	grantTarget                          Intent
}

func boundary() *fakeBoundary {
	return &fakeBoundary{available: true, qualified: true, epoch: "e", policy: "p", qualification: "q", limits: "l", authority: "a", runtime: "runtime", authorization: "auth", now: time.Unix(1, 0), expiry: time.Unix(2, 0), grantTarget: intent()}
}
func (b *fakeBoundary) Start(ctx context.Context, i Intent) (StartStatus, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entered != nil {
		close(b.entered)
		<-b.release
	}
	if ctx.Err() != nil || !b.available || !b.qualified || i.Authority != b.authority || i.Runtime != b.runtime || i.Authorization != b.authorization || i.Epoch != b.epoch || i.Policy != b.policy || i.Qualification != b.qualification || i.Limits != b.limits || (b.grantRequired && i.Grant == "") || (i.Grant != "" && (b.grantRevoked || !b.now.Before(b.expiry) || !sameIntent(i, b.grantTarget))) {
		return StartDenied, "no start", nil
	}
	b.starts++ // independently counted actual sink invocation
	if b.work != nil {
		go func() {
			<-b.work
			b.mu.Lock()
			b.completionEvidence = append(b.completionEvidence, "completed")
			b.mu.Unlock()
			close(b.completed)
		}()
	}
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
			j.prepareErr = ""
			(Coordinator{j, b}).Dispatch(context.Background(), i)
			if b.starts != count {
				t.Fatal("replayed")
			}
		})
	}
}
func TestOutageAndCompletion(t *testing.T) {
	j, b := journal(), boundary()
	c := Coordinator{j, b}
	i := intent()
	b.work, b.completed = make(chan struct{}), make(chan struct{})
	r, _ := c.Dispatch(context.Background(), i)
	if r.State != Started {
		t.Fatal(r)
	}
	// Completion belongs to the already-started sink, independent of admission.

	b.mu.Lock()
	b.available = false
	b.mu.Unlock()
	close(b.work)
	<-b.completed
	if len(b.completionEvidence) != 1 || b.completionEvidence[0] != "completed" {
		t.Fatal("completion evidence missing")
	}
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
	if b.starts != 1 {
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

// A port can accidentally reuse buffers or return a receipt for another scope.
type journalPort struct {
	prepare func(Intent) (PrepareStatus, Receipt, error)
	save    func(Receipt) error
}

func (j journalPort) Prepare(_ context.Context, i Intent) (PrepareStatus, Receipt, error) {
	return j.prepare(i)
}
func (j journalPort) Save(_ context.Context, r Receipt) error { return j.save(r) }

type boundaryPort func(context.Context, Intent) (StartStatus, string, error)

func (b boundaryPort) Start(c context.Context, i Intent) (StartStatus, string, error) { return b(c, i) }

func TestPortIsolationAndReceiptValidation(t *testing.T) {
	for _, status := range []PrepareStatus{PrepareNew, PrepareDuplicate, PrepareConflict} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			in := intent()
			foreign := cloneIntent(in)
			foreign.Project = "foreign"
			j := journalPort{prepare: func(i Intent) (PrepareStatus, Receipt, error) {
				i.Payload[0] = 'X'
				return status, Receipt{Intent: foreign, State: Prepared}, nil
			}, save: func(Receipt) error { t.Fatal("save"); return nil }}
			b := boundaryPort(func(context.Context, Intent) (StartStatus, string, error) {
				t.Fatal("start")
				return StartDenied, "", nil
			})
			r, err := (Coordinator{j, b}).Dispatch(context.Background(), in)
			if err == nil || r.Intent.Project != in.Project || string(r.Intent.Payload) != "data" || string(in.Payload) != "data" {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
	for _, state := range []State{Started, Denied, Uncertain, "invalid"} {
		j := journalPort{prepare: func(i Intent) (PrepareStatus, Receipt, error) {
			return PrepareNew, Receipt{Intent: i, State: state}, nil
		}, save: func(Receipt) error { t.Fatal("save"); return nil }}
		b := boundaryPort(func(context.Context, Intent) (StartStatus, string, error) {
			t.Fatal("start")
			return StartDenied, "", nil
		})
		if _, err := (Coordinator{j, b}).Dispatch(context.Background(), intent()); err == nil {
			t.Fatal(state)
		}
	}
	j := journal()
	b := boundaryPort(func(_ context.Context, i Intent) (StartStatus, string, error) {
		i.Payload[0] = 'X'
		return StartAcknowledged, "accepted", nil
	})
	in := intent()
	r, err := (Coordinator{j, b}).Dispatch(context.Background(), in)
	if err != nil || string(r.Intent.Payload) != "data" || string(j.rows[in.ID].Intent.Payload) != "data" {
		t.Fatalf("%+v %v", r, err)
	}
	r.Intent.Payload[0] = 'Y'
	if string(j.rows[in.ID].Intent.Payload) != "data" || string(in.Payload) != "data" {
		t.Fatal("aliased receipt")
	}
}

func TestGrantTargetAndContradictoryReceipts(t *testing.T) {
	j, b := journal(), boundary()
	c := Coordinator{j, b}
	original := intent()
	wrong := cloneIntent(original)
	wrong.Project = "other"
	if _, err := c.Dispatch(context.Background(), wrong); err == nil || b.starts != 0 || j.used[original.Grant] {
		t.Fatal("wrong target consumed grant")
	}
	if _, err := c.Dispatch(context.Background(), original); err != nil || b.starts != 1 {
		t.Fatal(err)
	}
	second := cloneIntent(original)
	second.ID = "second"
	if _, err := c.Dispatch(context.Background(), second); err == nil || b.starts != 1 {
		t.Fatal("grant reused")
	}
	before := j.rows[original.ID]
	altered := before
	altered.Intent = cloneIntent(before.Intent)
	altered.Intent.Payload = []byte("contradiction")
	if err := j.Save(context.Background(), altered); err == nil {
		t.Fatal("accepted altered receipt")
	}
	foreign := altered
	foreign.Intent.ID = "another"
	if err := j.Save(context.Background(), foreign); err == nil {
		t.Fatal("accepted foreign receipt")
	}
	if !reflect.DeepEqual(j.rows[original.ID], before) || len(j.contradictions) != 4 || string(j.contradictions[2].Intent.Payload) != "contradiction" || j.contradictions[3].Intent.ID != "another" {
		t.Fatalf("lost evidence: %+v", j.contradictions)
	}
}

func TestCanceledOrUnknownNeverStartsLater(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint(lost), func(t *testing.T) {
			j, b := journal(), boundary()
			b.entered, b.release = make(chan struct{}), make(chan struct{})
			b.lost = lost
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan Receipt, 1)
			go func() { r, _ := (Coordinator{j, b}).Dispatch(ctx, intent()); done <- r }()
			<-b.entered
			if !lost {
				cancel()
			}
			close(b.release)
			r := <-done
			cancel()
			expected := 0
			if lost {
				expected = 1
				if r.State != Uncertain {
					t.Fatal(r)
				}
			} else if r.State != Denied {
				t.Fatal(r)
			}
			for n := 0; n < 2; n++ {
				(Coordinator{j, b}).Dispatch(context.Background(), intent())
			}
			if b.starts != expected {
				t.Fatalf("late start: %d", b.starts)
			}
		})
	}
}

func TestEvidenceIdentityAndContradiction(t *testing.T) {
	j, b := journal(), boundary()
	r, err := (Coordinator{j, b}).Dispatch(context.Background(), intent())
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Save(context.Background(), r); err != nil || len(j.contradictions) != 0 {
		t.Fatal("identical evidence", err)
	}
	contrary := r
	contrary.ID, contrary.State, contrary.Evidence = "other", Denied, "contrary no-start proof"
	if err := j.Save(context.Background(), contrary); err == nil {
		t.Fatal("accepted contradiction")
	}
	got, _ := (Coordinator{j, b}).Dispatch(context.Background(), intent())
	if got.State != Uncertain || j.rows[r.Intent.ID].State != Started || len(j.contradictions) != 1 {
		t.Fatal(got)
	}
	changed := r
	changed.Evidence = "different content"
	if err := j.Save(context.Background(), changed); err == nil || len(j.contradictions) != 2 {
		t.Fatal("same ID conflict")
	}
}

func TestAdmissionChangesAfterPrepare(t *testing.T) {
	for _, name := range []string{"availability", "authorization", "expiry", "revocation", "epoch", "policy", "missing qualification", "stale qualification", "limits", "missing grant"} {
		t.Run(name, func(t *testing.T) {
			j, b := journal(), boundary()
			i := intent()
			if name == "missing grant" {
				i.Grant = ""
				b.grantRequired = true
			}
			prepared := false
			port := journalPort{prepare: func(in Intent) (PrepareStatus, Receipt, error) {
				s, r, err := j.Prepare(context.Background(), in)
				if s != PrepareNew || err != nil {
					t.Fatalf("prepare: %v %v", s, err)
				}
				prepared = true
				switch name {
				case "availability":
					b.available = false
				case "authorization":
					b.authorization = "revoked"
				case "expiry":
					b.now = b.expiry
				case "revocation":
					b.grantRevoked = true
				case "epoch":
					b.epoch = "new"
				case "policy":
					b.policy = "new"
				case "missing qualification":
					b.qualified = false
				case "stale qualification":
					b.qualification = "new"
				case "limits":
					b.limits = "new"
				}
				return s, r, err
			}, save: func(r Receipt) error { return j.Save(context.Background(), r) }}
			r, _ := (Coordinator{port, b}).Dispatch(context.Background(), i)
			if !prepared || r.State != Denied || b.starts != 0 {
				t.Fatalf("%+v starts=%d", r, b.starts)
			}
		})
	}
}

func TestNilEmptyPayloadEquality(t *testing.T) {
	j := journal()
	i := intent()
	i.Grant = ""
	i.Payload = nil
	if s, _, _ := j.Prepare(context.Background(), i); s != PrepareNew {
		t.Fatal(s)
	}
	i.Payload = []byte{}
	if s, _, _ := j.Prepare(context.Background(), i); s != PrepareDuplicate {
		t.Fatal(s)
	}
}

func TestGrantRejectsChangedBinding(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*Intent)
	}{
		{"target", func(i *Intent) { i.Project = "other" }},
		{"payload", func(i *Intent) { i.Payload = []byte("other") }},
		{"epoch", func(i *Intent) { i.Epoch = "other" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			j, b := journal(), boundary()
			i := intent()
			change.apply(&i)
			if _, err := (Coordinator{j, b}).Dispatch(context.Background(), i); err == nil || b.starts != 0 || j.used["grant"] {
				t.Fatal("changed grant binding admitted or consumed")
			}
		})
	}
}
