package ownerchannel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

func (e *env) limitSet(to int, typed bool) (ownerwire.LimitView, error) {
	e.t.Helper()
	e.beat()
	return e.svc.LimitSet(context.Background(), src, ownerwire.LimitSetParams{Project: "project-a", To: to, Typed: typed})
}

// wantRefusedLimit checks a refusal with the code limit-refused and a
// reason that contains the words.
func wantRefusedLimit(t *testing.T, err error, words string) {
	t.Helper()
	wantCode(t, err, record.CodeLimitRefused)
	var e *record.Error
	if !errors.As(err, &e) || !strings.Contains(e.Detail, words) {
		t.Errorf("the reason is %q, want it to say %q", err, words)
	}
}

func TestTheLimitStartsAtTwelveWithAnEmptyHistory(t *testing.T) {
	e := newEnv(t)
	e.start()
	v, err := e.svc.Limit(context.Background(), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if v.Project != "project-a" || v.Limit != limits.Default || v.Suggestion != limits.SuggestionMin || v.RaisedToday || len(v.Days) != limits.WindowDays {
		t.Fatalf("%+v", v)
	}
	for _, d := range v.Days {
		if d.Uses != 0 || d.ReachedLimit || d.Alert {
			t.Errorf("%+v", d)
		}
	}
	if _, err := ownerwire.EncodeLimitView(v); err != nil {
		t.Error(err)
	}
	// Every rule is per project.
	other, err := e.svc.Limit(context.Background(), "project-b")
	if err != nil || other.Project != "project-b" || other.Limit != limits.Default {
		t.Errorf("%+v %v", other, err)
	}
}

func TestTheLimitIsChangedUnderItsRules(t *testing.T) {
	e := newEnv(t)
	e.start()

	// Raising is typed.
	_, err := e.limitSet(14, false)
	wantRefusedLimit(t, err, "typed")
	// By half at most: 12 rises to 18.
	_, err = e.limitSet(19, true)
	wantRefusedLimit(t, err, "18")
	_, err = e.limitSet(41, true)
	wantRefusedLimit(t, err, "above")
	_, err = e.limitSet(0, false)
	wantRefusedLimit(t, err, "under")
	if n := len(e.entries(ownerjournal.KindLimit)); n != 0 {
		t.Fatalf("%d limit entries after refusals", n)
	}

	// Lowering is one key.
	v, err := e.limitSet(8, false)
	if err != nil || v.Limit != 8 || v.RaisedToday {
		t.Fatalf("%+v %v", v, err)
	}
	// A raise, typed and by half: 8 rises to 12. One raise a day.
	v, err = e.limitSet(12, true)
	if err != nil || v.Limit != 12 || !v.RaisedToday {
		t.Fatalf("%+v %v", v, err)
	}
	_, err = e.limitSet(18, true)
	wantRefusedLimit(t, err, "one raise a day")
	// Lowering is still allowed the same day.
	if v, err := e.limitSet(10, false); err != nil || v.Limit != 10 {
		t.Fatalf("%+v %v", v, err)
	}
	// The next day, a raise is allowed again.
	e.advance(24 * time.Hour)
	if v, err := e.limitSet(15, true); err != nil || v.Limit != 15 {
		t.Fatalf("%+v %v", v, err)
	}
	changes, err := e.svc.journal().LimitChanges("project-a")
	if err != nil || len(changes) != 4 {
		t.Fatalf("%v %v", changes, err)
	}
	if changes[0].From != 12 || changes[0].To != 8 || changes[0].Typed || !changes[1].Typed || changes[1].From != 8 {
		t.Errorf("the changes are %+v", changes)
	}
	// A number that is the present one changes nothing.
	n := len(e.entries(ownerjournal.KindLimit))
	if v, err := e.limitSet(15, false); err != nil || v.Limit != 15 || len(e.entries(ownerjournal.KindLimit)) != n {
		t.Errorf("%+v %v", v, err)
	}

	// The limit survives a restart.
	e.restart()
	if v, err := e.svc.Limit(context.Background(), "project-a"); err != nil || v.Limit != 15 || !v.RaisedToday {
		t.Errorf("%+v %v", v, err)
	}
}

func TestALimitOfOneCannotBeRaised(t *testing.T) {
	e := newEnv(t)
	e.start()
	if _, err := e.limitSet(1, false); err != nil {
		t.Fatal(err)
	}
	e.advance(24 * time.Hour)
	_, err := e.limitSet(2, true)
	wantRefusedLimit(t, err, "cannot be raised")
}

func TestTheLimitIsTheOwnersToChangeAsTheDeciderAndPresent(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()
	p := ownerwire.LimitSetParams{Project: "project-a", To: 8}

	_, err := e.svc.LimitSet(ctx, src, p)
	wantCode(t, err, record.CodeOwnerAway)
	e.beat()
	other := src
	other.Decider = false
	_, err = e.svc.LimitSet(ctx, other, p)
	wantCode(t, err, record.CodeNotDecider)
	bad := src
	bad.Session = "x"
	_, err = e.svc.LimitSet(ctx, bad, p)
	wantCode(t, err, record.CodeRecordInvalid)
	if v, err := e.svc.Limit(ctx, "project-a"); err != nil || v.Limit != limits.Default {
		t.Errorf("a refused change moved the limit: %+v %v", v, err)
	}
	if _, err := e.svc.LimitSet(ctx, src, p); err != nil {
		t.Fatal(err)
	}
}

func TestTheViewShowsTheDaysAndTheSuggestion(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()
	e.setLimit(5)
	e.useUp(5, "feature-a", "feature-b", "feature-c", "feature-d", "feature-e")
	// The day reached its limit, so the suggestion leaves it out: twice 5
	// would be 10.
	e.advance(24 * time.Hour)
	v, err := e.svc.Limit(ctx, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if v.Limit != 5 || v.Suggestion != limits.SuggestionMin {
		t.Errorf("the limit is %d and the suggestion %d", v.Limit, v.Suggestion)
	}
	yesterday := v.Days[len(v.Days)-1]
	if yesterday.Uses != 5 || !yesterday.ReachedLimit {
		t.Errorf("yesterday is %+v", yesterday)
	}

	// A busy day that did not reach its limit counts: 8 uses under a
	// limit of 12 suggest 16.
	e.setLimit(12)
	names := []string{"feature-f", "feature-g", "feature-h", "feature-i", "feature-j", "feature-k", "feature-l", "feature-m"}
	e.useUp(8, names...)
	e.advance(24 * time.Hour)
	v, err = e.svc.Limit(ctx, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if v.Suggestion != 16 {
		t.Errorf("the suggestion is %d, want 16", v.Suggestion)
	}
	if d := v.Days[len(v.Days)-1]; d.Uses != 8 || d.ReachedLimit {
		t.Errorf("the busy day is %+v", d)
	}
}

func TestADayWithAnAlertIsMarkedAndLeftOutOfTheSuggestion(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.beat()
	// A decision at the door the journal lacks raises an alert for the day.
	if _, err := e.stand.Decide(context.Background(), forgedFor(t, e, p)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.advance(24 * time.Hour)
	v, err := e.svc.Limit(context.Background(), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if d := v.Days[len(v.Days)-1]; !d.Alert || d.ReachedLimit {
		t.Errorf("yesterday is %+v", d)
	}
}
