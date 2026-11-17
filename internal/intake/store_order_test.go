//go:build linux

package intake

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordTimes returns the time part of every record name in dir, oldest
// first.
func recordTimes(t *testing.T, st *Store) []string {
	t.Helper()
	files, err := st.recordFiles()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		out = append(out, recordName.FindStringSubmatch(f.name)[1])
	}
	return out
}

// TestStoreOrderDoesNotDependOnTheWritersClock: a failure that is written
// later by a Store whose clock is behind the pass still withdraws the pass.
func TestStoreOrderDoesNotDependOnTheWritersClock(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.store.now = func() time.Time { return testNow }
	g.record(nil)
	// A second store on the same folder, with a clock one second behind.
	late := openTestStore(t, g.dir)
	late.now = func() time.Time { return testNow.Add(-time.Second) }
	if _, err := late.PutVerification(testProject, failResult(s.tip), testGates); err != nil {
		t.Fatal(err)
	}
	times := recordTimes(t, g.store)
	if len(times) != 2 || times[0] >= times[1] {
		t.Fatalf("names are not in the order of writing: %v", times)
	}
	all, err := g.store.Verifications(s.tip)
	if err != nil || len(all) != 2 || all[0].passing() == false || all[1].passing() {
		t.Fatalf("order of records: %v, %+v", err, all)
	}
	req := g.req(s.inspect())
	_, ierr := Integrate(context.Background(), s.source(), req)
	if !errors.Is(ierr, ErrNotVerified) {
		t.Errorf("Integrate = %v, want ErrNotVerified", ierr)
	}
	if g.id(upstreamRef) != s.base {
		t.Error("the target moved")
	}
}

// TestStoreIntakeNameIsNotEarlierThanTheNewest: an intake record whose time
// is long before the newest record in the folder is still named after it.
func TestStoreIntakeNameIsNotEarlierThanTheNewest(t *testing.T) {
	st, dir := newTestStore(t)
	other := openTestStore(t, dir)
	other.now = func() time.Time { return testNow }
	ver, err := other.PutVerification(testProject, passResult(idOf('a')), testGates)
	if err != nil {
		t.Fatal(err)
	}
	rec := IntakeRecord{
		Version: recordVersion, Time: testNow.Add(-24 * time.Hour), Project: testProject,
		TargetRef: upstreamRef, TipRef: leadRef, Base: idOf('b'), Tip: idOf('a'),
		ReviewDigest: strings.Repeat("c", 64), Commits: 1, Paths: 1, Verification: ver,
	}
	if err := st.putIntake(rec); err != nil {
		t.Fatal(err)
	}
	files, err := st.recordFiles()
	if err != nil || len(files) != 2 || files[0].kind != "verification" || files[1].kind != "intake" {
		t.Fatalf("%v, files %+v", err, files)
	}
	times := recordTimes(t, st)
	if times[0] >= times[1] {
		t.Errorf("the intake record is named before the record that was there: %v", times)
	}
}

// TestStoreWritersOnOneFolderNeverShareATime: stores on the same folder, with
// the same frozen clock, write at once; every record gets its own time and
// the names of one writer keep their order.
func TestStoreWritersOnOneFolderNeverShareATime(t *testing.T) {
	first, dir := newTestStore(t)
	second := openTestStore(t, dir)
	for _, st := range []*Store{first, second} {
		st.now = func() time.Time { return testNow }
	}
	const each = 25
	var wg sync.WaitGroup
	for _, st := range []*Store{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				if _, err := st.PutVerification(testProject, passResult(idOf('a')), testGates); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	times := recordTimes(t, first)
	if len(times) != 2*each {
		t.Fatalf("%d records", len(times))
	}
	for i := 1; i < len(times); i++ {
		if times[i-1] >= times[i] {
			t.Fatalf("names %d and %d share a time or are out of order: %s %s", i-1, i, times[i-1], times[i])
		}
	}
}
