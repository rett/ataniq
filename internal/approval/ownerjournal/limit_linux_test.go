package ownerjournal

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/record"
)

func TestTheLimitAndItsChanges(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	if got, err := s.Limit("project-a"); err != nil || got != (Limit{}) {
		t.Fatalf("the limit of a project that never set one: %+v, %v", got, err)
	}
	if got, err := s.LimitChanges("project-a"); err != nil || len(got) != 0 {
		t.Fatalf("the changes of a project that never set one: %+v, %v", got, err)
	}
	bad := map[string]LimitChange{
		"no project":        {At: base, From: 12, To: 10},
		"a hostile project": {Project: "project\x1b[2J", At: base, From: 12, To: 10},
		"no time":           {Project: "project-a", From: 12, To: 10},
		"a limit of zero":   {Project: "project-a", At: base, From: 12, To: 0},
		"a negative limit":  {Project: "project-a", At: base, From: 12, To: -3},
		"from zero":         {Project: "project-a", At: base, From: 0, To: 5},
	}
	for name, c := range bad {
		if err := s.AppendLimit(c); err == nil {
			t.Errorf("AppendLimit took %s", name)
		}
	}
	if got := mustEntries(t, s); len(got) != 0 {
		t.Fatalf("the refused changes wrote %d entries", len(got))
	}
	day := 24 * time.Hour
	changes := []LimitChange{
		{Seq: 1, Project: "project-a", At: base, From: 12, To: 18, Typed: true},
		{Seq: 2, Project: "project-a", At: base.Add(day), From: 18, To: 6},
		{Seq: 3, Project: "project-b", At: base.Add(day), From: 12, To: 9},
		{Seq: 4, Project: "project-a", At: base.Add(2 * day), From: 6, To: 6},
	}
	for _, c := range changes {
		c.Seq = 99 // ignored
		if err := s.AppendLimit(c); err != nil {
			t.Fatal(err)
		}
	}
	// A change that starts from another number than the journal's is
	// refused: another change came between.
	for _, from := range []int{12, 18, 7} {
		if err := s.AppendLimit(LimitChange{Project: "project-a", At: base.Add(3 * day), From: from, To: 5}); !errors.Is(err, ErrLimitMoved) {
			t.Fatalf("a change from %d: %v, want %v", from, err, ErrLimitMoved)
		}
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	got, err := s.LimitChanges("project-a")
	if want := []LimitChange{changes[0], changes[1], changes[3]}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("the changes of project-a:\n %+v, %v\nwant\n %+v", got, err, want)
	}
	limit, err := s.Limit("project-a")
	if want := (Limit{Set: true, Limit: 6, ChangedAt: base.Add(2 * day), LastRaise: base}); err != nil || limit != want {
		t.Fatalf("the limit of project-a: %+v, %v; want %+v", limit, err, want)
	}
	limit, err = s.Limit("project-b")
	if want := (Limit{Set: true, Limit: 9, ChangedAt: base.Add(day)}); err != nil || limit != want {
		t.Fatalf("the limit of project-b: %+v, %v; want %+v", limit, err, want)
	}
	e := mustEntries(t, s)[0]
	if e.Kind != KindLimit || e.Project != "project-a" || e.PendingID != "" || string(e.Body) != `{"from":12,"to":18,"typed":true}` {
		t.Fatalf("the limit entry: %+v", e)
	}
	// A later raise moves the time of the last raise.
	if err := s.AppendLimit(LimitChange{Project: "project-a", At: base.Add(4 * day), From: 6, To: 9, Typed: true}); err != nil {
		t.Fatal(err)
	}
	if limit, _ = s.Limit("project-a"); !limit.LastRaise.Equal(base.Add(4*day)) || limit.Limit != 9 {
		t.Fatalf("the limit after a raise: %+v", limit)
	}
}

// decideAt journals one decision of a fresh card at the given time.
func decideAt(t *testing.T, s *Store, n *int, project string, approve bool, at time.Time) {
	t.Helper()
	*n++
	id := fmt.Sprintf("ap-n%d", *n)
	_, digest := pendingRecord(t, id, project, at.Add(time.Hour))
	if _, err := s.AppendDecision(decisionRecord(t, id, project, digest, approve, at), Seen{}); err != nil {
		t.Fatal(err)
	}
}

// Uses and marks are placed on calendar days in the location of the time
// that is handed in, not on UTC dates.
func TestDaysCountCardUsesAndMarksByLocalDay(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	loc := time.FixedZone("west", -9*3600)
	local := func(day, hour, minute int) time.Time { return time.Date(2026, 11, day, hour, minute, 0, 0, loc) }
	now := local(20, 10, 0)
	n := 0
	// The 19th, local: two approves, the second at 23:59 (the 20th in UTC).
	decideAt(t, s, &n, "project-a", true, local(19, 9, 0))
	decideAt(t, s, &n, "project-a", true, local(19, 23, 59))
	// Today: one approve at 00:00, one deny, one held, one in another project.
	decideAt(t, s, &n, "project-a", true, local(20, 0, 0))
	decideAt(t, s, &n, "project-a", false, local(20, 1, 0))
	held, pending := approveOf(t, "ap-held", "project-a", local(20, 2, 0))
	if _, err := s.AppendHeld(held, pending, Seen{}); err != nil {
		t.Fatal(err)
	}
	decideAt(t, s, &n, "project-b", true, local(20, 3, 0))
	// The first day of the view (the 6th) at its first second, and the day
	// before it at its last, which is outside.
	decideAt(t, s, &n, "project-a", true, local(6, 0, 0))
	decideAt(t, s, &n, "project-a", true, local(5, 23, 59))
	// A use dated tomorrow (the clock was set back since) is on no day of
	// the view.
	decideAt(t, s, &n, "project-a", true, local(21, 0, 30))
	// Marks: the 10th reached the limit, the 12th had an alert, the 19th
	// both; an alert of another project and one before the view are not
	// project-a's days.
	alerts := []Alert{
		{Project: "project-a", At: local(10, 15, 0), LimitReached: true, Note: "12 of 12"},
		{Project: "project-a", At: local(12, 23, 59), PendingID: "ap-n1", Note: "the door holds a decision the journal lacks"},
		{Project: "project-a", At: local(19, 12, 0), LimitReached: true},
		{Project: "project-a", At: local(19, 13, 0)},
		{Project: "project-b", At: local(15, 12, 0)},
		{Project: "project-a", At: local(5, 12, 0)},
	}
	for _, a := range alerts {
		if err := s.AppendAlert(a); err != nil {
			t.Fatal(err)
		}
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	days, err := s.Days("project-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != ViewDays+1 {
		t.Fatalf("%d days, want %d", len(days), ViewDays+1)
	}
	want := make([]Day, ViewDays+1)
	for i := range want {
		want[i].Date = fmt.Sprintf("2026-11-%02d", 6+i)
	}
	want[0].Uses = 1            // the 6th
	want[4].ReachedLimit = true // the 10th
	want[6].Alert = true        // the 12th
	want[13] = Day{Date: "2026-11-19", Uses: 2, ReachedLimit: true, Alert: true}
	want[14].Uses = 1 // today
	if !reflect.DeepEqual(days, want) {
		t.Fatalf("the days of project-a:\n %+v\nwant\n %+v", days, want)
	}
	// The same journal read in UTC puts the 23:59 use on the 20th.
	utc, err := s.Days("project-a", now.UTC())
	if err != nil {
		t.Fatal(err)
	}
	if utc[13].Date != "2026-11-19" || utc[13].Uses != 1 || utc[14].Date != "2026-11-20" || utc[14].Uses != 2 {
		t.Fatalf("the last two days read in UTC: %+v", utc[13:])
	}
	other, err := s.Days("project-b", now)
	if err != nil || other[14].Uses != 1 || !other[9].Alert || other[13].Uses != 0 {
		t.Fatalf("the days of project-b: %+v, %v", other, err)
	}
	// The uses since the start of today, for the limit's judgement: the
	// approve of today and the one dated tomorrow, not the deny, not the
	// held one, not the other project's.
	uses, err := s.CardUses("project-a", local(20, 0, 0))
	if want := []time.Time{local(20, 0, 0).UTC(), local(21, 0, 30).UTC()}; err != nil || !reflect.DeepEqual(uses, want) {
		t.Fatalf("the card uses since the start of today: %v, %v; want %v", uses, err, want)
	}
	if uses, err = s.CardUses("project-a", local(1, 0, 0)); err != nil || len(uses) != 6 {
		t.Fatalf("all card uses: %d, %v; want 6", len(uses), err)
	}
	for i := 1; i < len(uses); i++ {
		if uses[i].Before(uses[i-1]) {
			t.Fatalf("the card uses are not oldest first: %v", uses)
		}
	}
	if _, err := s.Days("project-a", time.Time{}); err == nil {
		t.Fatal("Days took no time")
	}
	if _, err := s.CardUses("project-a", time.Time{}); err == nil {
		t.Fatal("CardUses took no time")
	}
}

// Across a change of the clocks every day of the view still has its own
// date, and there are no two days with one.
func TestDaysAcrossAChangeOfTheClocks(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no time zone data: %v", err)
	}
	s := mustOpen(t, newPath(t))
	for _, now := range []time.Time{
		time.Date(2026, 10, 30, 0, 30, 0, 0, loc), // the clocks went back on the 25th
		time.Date(2026, 4, 5, 23, 30, 0, 0, loc),  // and forward on the 29th of March
	} {
		days, err := s.Days("project-a", now)
		if err != nil {
			t.Fatal(err)
		}
		for i, d := range days {
			want := now.AddDate(0, 0, i-ViewDays).Format(dateLayout)
			if d.Date != want {
				t.Fatalf("day %d of the view at %v is %s, want %s", i, now, d.Date, want)
			}
		}
	}
}

func TestAlertsAreChecked(t *testing.T) {
	s := mustOpen(t, newPath(t))
	bad := map[string]Alert{
		"no project":      {At: base},
		"no time":         {Project: "project-a"},
		"a hostile note":  {Project: "project-a", At: base, Note: "alert\x07"},
		"a long note":     {Project: "project-a", At: base, Note: strings.Repeat("n", 501)},
		"a malformed id":  {Project: "project-a", At: base, PendingID: "rq-one"},
		"a note not UTF8": {Project: "project-a", At: base, Note: "\xff"},
	}
	for name, a := range bad {
		if err := s.AppendAlert(a); err == nil {
			t.Errorf("AppendAlert took %s", name)
		}
	}
	if err := s.AppendAlert(Alert{Project: "project-a", At: base, PendingID: "ap-one", LimitReached: true, Note: strings.Repeat("n", 500)}); err != nil {
		t.Fatal(err)
	}
	entries := mustEntries(t, s)
	if len(entries) != 1 || entries[0].Kind != KindAlert || entries[0].PendingID != "ap-one" ||
		string(entries[0].Body) != `{"limit_reached":true,"note":"`+strings.Repeat("n", 500)+`"}` {
		t.Fatalf("the entries: %+v", entries)
	}
}

func TestTheCountPerRequest(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	bad := map[string]Count{
		"no project":                      {At: base, RequestID: "rq-one"},
		"no time":                         {Project: "project-a", RequestID: "rq-one"},
		"no request id":                   {Project: "project-a", At: base},
		"a pending id for the request id": {Project: "project-a", At: base, RequestID: "ap-one"},
		"a hostile request id":            {Project: "project-a", At: base, RequestID: "rq-\x1b[2J"},
		"a malformed pending id":          {Project: "project-a", At: base, RequestID: "rq-one", PendingID: "one"},
		"a hostile reason":                {Project: "project-a", At: base, RequestID: "rq-one", Reason: "a\nb"},
		"a long reason":                   {Project: "project-a", At: base, RequestID: "rq-one", Reason: strings.Repeat("r", 501)},
	}
	for name, c := range bad {
		if err := s.AppendCount(c); err == nil {
			t.Errorf("AppendCount took %s", name)
		}
	}
	if requests, met, err := s.CountTotals("project-a"); err != nil || requests != 0 || met != 0 {
		t.Fatalf("the totals of nothing: %d, %d, %v", requests, met, err)
	}
	counts := []Count{
		{Project: "project-a", At: base, RequestID: "rq-one", PendingID: "ap-one", Met: true},
		{Project: "project-a", At: base.Add(time.Second), RequestID: "rq-two", PendingID: "ap-two", Reason: "23 files, and the terms allow 20"},
		// Counted again after a restart, with another answer: the last holds.
		{Project: "project-a", At: base.Add(2 * time.Second), RequestID: "rq-one", PendingID: "ap-one", Reason: "the suite record is not clean"},
		// No pending record was made; the lead used the same label twice.
		{Project: "project-a", At: base.Add(3 * time.Second), RequestID: "rq-same", Met: true},
		{Project: "project-a", At: base.Add(4 * time.Second), RequestID: "rq-same", Met: true},
		{Project: "project-b", At: base.Add(5 * time.Second), RequestID: "rq-one", PendingID: "ap-one", Met: true},
	}
	for _, c := range counts {
		if err := s.AppendCount(c); err != nil {
			t.Fatal(err)
		}
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	got, err := s.Counts("project-a")
	want := []Count{counts[1], counts[2], counts[3], counts[4]}
	for i, seq := range []int64{2, 3, 4, 5} {
		want[i].Seq = seq
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("the counts of project-a:\n %+v, %v\nwant\n %+v", got, err, want)
	}
	if requests, met, err := s.CountTotals("project-a"); err != nil || requests != 4 || met != 2 {
		t.Fatalf("the totals of project-a: %d requests, %d met, %v; want 4 and 2", requests, met, err)
	}
	if requests, met, err := s.CountTotals("project-b"); err != nil || requests != 1 || met != 1 {
		t.Fatalf("the totals of project-b: %d requests, %d met, %v; want 1 and 1", requests, met, err)
	}
	e := mustEntries(t, s)[1]
	if e.Kind != KindCount || e.PendingID != "ap-two" || e.Project != "project-a" ||
		string(e.Body) != `{"met":false,"reason":"23 files, and the terms allow 20","request_id":"rq-two"}` {
		t.Fatalf("the count entry: %+v", e)
	}
}

// Every kind of the fixed list can be written, and the list in the file's
// layout is the same list.
func TestEveryKindIsWrittenAndTheListIsClosed(t *testing.T) {
	s := mustOpen(t, newPath(t))
	approve, pending := approveOf(t, "ap-held", "project-a", base)
	if _, err := s.AppendHeld(approve, pending, Seen{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExpireHeld(base.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	approve, _ = approveOf(t, "ap-one", "project-a", base)
	d, err := s.AppendDecision(approve, Seen{})
	if err != nil {
		t.Fatal(err)
	}
	u, err := d.MarkDelivered(base)
	if err != nil {
		t.Fatal(err)
	}
	steps := []error{
		u.AppendUse(base, ""),
		u.AppendOutcome(base, record.StateApplied, ""),
		s.AppendRefused(Refused{At: base, Code: record.CodeOwnerAway, Surface: "console", Session: "0123456789abcdef"}),
		s.AppendLimit(LimitChange{Project: "project-a", At: base, From: 12, To: 6}),
		s.AppendAlert(Alert{Project: "project-a", At: base}),
		s.AppendCount(Count{Project: "project-a", At: base, RequestID: "rq-one"}),
		s.AppendLooked(base),
	}
	for i, err := range steps {
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	written := map[Kind]bool{}
	for _, e := range mustEntries(t, s) {
		written[e.Kind] = true
	}
	all := []Kind{KindDecision, KindHeld, KindRefused, KindDelivered, KindOutcome, KindExpired, KindLimit, KindUse, KindAlert, KindCount, KindActivity, KindLooked}
	for _, k := range all {
		if !written[k] {
			t.Errorf("no entry of kind %s was written", k)
		}
		if !strings.Contains(schema[0].ddl, "'"+string(k)+"'") {
			t.Errorf("the layout does not list the kind %s", k)
		}
	}
	if len(written) != len(all) || strings.Count(schema[0].ddl, "','") != len(all)-1 {
		t.Fatalf("%d kinds were written, the code has %d, and the layout lists another number", len(written), len(all))
	}
}
