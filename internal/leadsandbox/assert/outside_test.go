package assert

import (
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadsandbox/profile"
)

var (
	t0     = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	digest = strings.Repeat("ab", 32)
)

func goodRecord() *QualificationRecord {
	return &QualificationRecord{
		Runtime: profile.Claude, Version: "2.1.0", ProfileDigest: digest,
		Recorded: t0.Add(-24 * time.Hour), Harness: "h-1",
	}
}

func TestCurrent(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(r *QualificationRecord) *QualificationRecord
		now    time.Time
		state  RecordState
		reason string
	}{
		{"current", func(r *QualificationRecord) *QualificationRecord { return r }, t0, RecordCurrent, ""},
		{"current with a future expiry", func(r *QualificationRecord) *QualificationRecord {
			r.Expires = t0.Add(time.Hour)
			return r
		}, t0, RecordCurrent, ""},
		{"missing", func(r *QualificationRecord) *QualificationRecord { return nil }, t0, RecordMissing, "no qualification record"},
		{"other runtime", func(r *QualificationRecord) *QualificationRecord { r.Runtime = profile.Codex; return r }, t0, RecordStale, "runtime codex"},
		{"changed provider version", func(r *QualificationRecord) *QualificationRecord { r.Version = "2.0.9"; return r }, t0, RecordStale, "version 2.0.9"},
		{"other digest", func(r *QualificationRecord) *QualificationRecord {
			r.ProfileDigest = strings.Repeat("cd", 32)
			return r
		}, t0, RecordStale, "another profile digest (cdcdcdcdcdcd)"},
		{"expired", func(r *QualificationRecord) *QualificationRecord { r.Expires = t0.Add(-time.Second); return r }, t0, RecordStale, "expired at 2026-03-01T11:59:59Z"},
		{"expires exactly now", func(r *QualificationRecord) *QualificationRecord { r.Expires = t0; return r }, t0, RecordStale, "expired"},
		{"dated after now", func(r *QualificationRecord) *QualificationRecord { r.Recorded = t0.Add(time.Minute); return r }, t0, RecordStale, "dated after now"},
		{"no recording time", func(r *QualificationRecord) *QualificationRecord { r.Recorded = time.Time{}; return r }, t0, RecordStale, "no recording time"},
		{"no harness", func(r *QualificationRecord) *QualificationRecord { r.Harness = ""; return r }, t0, RecordStale, "harness version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, reason := Current(tc.mutate(goodRecord()), profile.Claude, "2.1.0", digest, tc.now)
			if state != tc.state {
				t.Fatalf("state %s (%s), want %s", state, reason, tc.state)
			}
			if tc.reason == "" && reason != "" || !strings.Contains(reason, tc.reason) {
				t.Errorf("reason %q, want it to contain %q", reason, tc.reason)
			}
			wantConsole := ConsoleUnsupported
			if tc.state == RecordCurrent {
				wantConsole = ""
			}
			if state.Console() != wantConsole {
				t.Errorf("Console() = %q, want %q", state.Console(), wantConsole)
			}
		})
	}
	// Nothing to compare with is never current.
	if st, _ := Current(goodRecord(), profile.Claude, "", digest, t0); st != RecordStale {
		t.Errorf("empty version: %s", st)
	}
	if st, _ := Current(goodRecord(), profile.Claude, "2.1.0", "", t0); st != RecordStale {
		t.Errorf("empty digest: %s", st)
	}
}

type fakeRecords struct {
	rec *QualificationRecord
	err error
}

func (f fakeRecords) Lookup(profile.Runtime, string, string) (*QualificationRecord, error) {
	return f.rec, f.err
}

func TestCheckQualification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		store  QualificationRecords
		status Status
		detail string
	}{
		{"current", fakeRecords{rec: goodRecord()}, StatusOK, "a current record exists"},
		{"missing", fakeRecords{}, StatusFailed, "unsupported: missing"},
		{"stale", fakeRecords{rec: func() *QualificationRecord { r := goodRecord(); r.Version = "1"; return r }()}, StatusFailed, "unsupported: stale"},
		{"lookup fails", fakeRecords{err: errors.New("store gone")}, StatusNotChecked, "store gone"},
		{"no store", nil, StatusNotChecked, "no qualification record store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := CheckQualification(tc.store, profile.Claude, "2.1.0", digest, t0)
			if r.Number != 10 || r.ID != IDQualificationRecord || r.Status != tc.status || !strings.Contains(r.Detail, tc.detail) {
				t.Errorf("%+v", r)
			}
		})
	}
}

type fakeLock struct {
	held bool
	err  error
}

func (f fakeLock) Held(string) (bool, error) { return f.held, f.err }

type fakeLinger struct {
	on  bool
	err error
}

func (f fakeLinger) Enabled(string) (bool, error) { return f.on, f.err }

func TestCheckRoleLockAndLingering(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lock   RoleLock
		ling   Lingering
		status Status
		detail string
	}{
		{"both", fakeLock{held: true}, fakeLinger{on: true}, StatusOK, "is held and lingering is on"},
		{"lock not held", fakeLock{}, fakeLinger{on: true}, StatusFailed, "does not hold the lock"},
		{"lingering off", fakeLock{held: true}, fakeLinger{}, StatusFailed, "lingering is off"},
		{"both wrong", fakeLock{}, fakeLinger{}, StatusFailed, "lingering is off"},
		{"lock error", fakeLock{err: errors.New("fd gone")}, fakeLinger{on: true}, StatusNotChecked, "fd gone"},
		{"lingering error", fakeLock{held: true}, fakeLinger{err: errors.New("no loginctl")}, StatusNotChecked, "no loginctl"},
		{"failed beats an error", fakeLock{}, fakeLinger{err: errors.New("no loginctl")}, StatusFailed, "does not hold"},
		{"no lock", nil, fakeLinger{on: true}, StatusNotChecked, "no role lock"},
		{"no lingering", fakeLock{held: true}, nil, StatusNotChecked, "no lingering"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := CheckRoleLockAndLingering(tc.lock, tc.ling, "arc-lead", "someone")
			if r.Number != 9 || r.ID != IDRoleLockLingering || r.Status != tc.status || !strings.Contains(r.Detail, tc.detail) {
				t.Errorf("%+v", r)
			}
		})
	}
}

func TestCheckHolderBinds(t *testing.T) {
	binds := []profile.Bind{
		{Kind: profile.KindTmpfs, Inside: "/"},
		{Kind: profile.KindRoBind, Inside: "/usr", Source: "/usr"},
		{Kind: profile.KindRwBind, Inside: "/home/lead", Source: "/srv/state/leads/a/home"},
	}
	holder := []string{"/run/example/holder.sock", "/var/lib/example/holder"}
	r := CheckHolderBinds(binds, holder)
	if r.Status != StatusOK || r.ID != IDHolderHidden+OutsideSuffix || r.Number != 1 {
		t.Errorf("%+v", r)
	}
	for _, src := range []string{"/var/lib/example/holder", "/var/lib/example/holder/db", "/var/lib/example", "/run/example/holder.sock"} {
		bad := append(append([]profile.Bind(nil), binds...), profile.Bind{Kind: profile.KindRoBind, Inside: "/leak", Source: src})
		r := CheckHolderBinds(bad, holder)
		if r.Status != StatusFailed || !strings.Contains(r.Detail, `"/leak"`) {
			t.Errorf("source %s: %+v", src, r)
		}
	}
	if r := CheckHolderBinds(binds, nil); r.Status != StatusNotChecked {
		t.Errorf("no holder paths: %+v", r)
	}
	rel := append(append([]profile.Bind(nil), binds...), profile.Bind{Kind: profile.KindRoBind, Inside: "/x", Source: "relative"})
	if r := CheckHolderBinds(rel, holder); r.Status != StatusFailed {
		t.Errorf("relative source: %+v", r)
	}
}

func TestCheckTIOCSTIOutside(t *testing.T) {
	s := testSpec(profile.Claude)
	for _, tc := range []struct {
		name    string
		sysctl  string
		readErr error
		filter  bool
		status  Status
	}{
		{"zero", "0\n", nil, false, StatusOK},
		{"one, no filter", "1\n", nil, false, StatusFailed},
		{"one, filter named", "1\n", nil, true, StatusOK},
		{"unreadable, no filter", "", syscall.EACCES, false, StatusNotChecked},
		{"unreadable, filter named", "", syscall.EACCES, true, StatusOK},
		{"absent, no filter", "ABSENT", nil, false, StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := goodSystem(s)
			switch {
			case tc.sysctl == "ABSENT":
				delete(f.nodes, tiocstiSysctl)
			case tc.readErr != nil:
				f.readErr[tiocstiSysctl] = tc.readErr
			default:
				f.setFile(tiocstiSysctl, tc.sysctl)
			}
			r := CheckTIOCSTIOutside(f, tc.filter)
			if r.Status != tc.status || r.Number != 5 || r.ID != IDTIOCSTIBlocked+OutsideSuffix {
				t.Errorf("%+v", r)
			}
		})
	}
	if r := CheckTIOCSTIOutside(nil, false); r.Status != StatusNotChecked {
		t.Errorf("nil system: %+v", r)
	}
}

func TestRunOutsideAndFailedAll(t *testing.T) {
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	in := OutsideInput{
		Spec: s, Role: "arc-lead", User: "someone", Version: "2.1.0", Now: t0,
		Binds:       []profile.Bind{{Kind: profile.KindRoBind, Inside: "/usr", Source: "/usr"}},
		HolderPaths: s.HolderPaths,
		Canary:      testCanary(t),
		Sys:         f, Lock: fakeLock{held: true}, Linger: fakeLinger{on: true},
		Records: fakeRecords{rec: func() *QualificationRecord { r := goodRecord(); r.ProfileDigest = s.Digest; return r }()},
	}
	out := RunOutside(in)
	if len(out) != 5 {
		t.Fatalf("%d results", len(out))
	}
	for i, id := range []string{IDHolderHidden + OutsideSuffix, IDLoopbackUnreach + OutsideSuffix, IDTIOCSTIBlocked + OutsideSuffix, IDRoleLockLingering, IDQualificationRecord} {
		if out[i].ID != id {
			t.Errorf("result %d is %s, want %s", i, out[i].ID, id)
		}
	}
	for _, r := range out {
		if r.Status != StatusOK {
			t.Errorf("%+v", r)
		}
	}
	rep := RunInside(s, f).With(out...)
	if FailedAll(rep, s) {
		t.Fatalf("FailedAll: %v", Problems(rep, s, RequireAll))
	}
	// The whole report survives the line format.
	back, err := ParseLine(EncodeLine(rep))
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if FailedAll(back, s) {
		t.Error("FailedAll after a round trip")
	}
	// A stale record makes the whole fail although every inside check passed.
	in.Records = fakeRecords{}
	rep = RunInside(s, f).With(RunOutside(in)...)
	if Failed(rep, s) != true {
		t.Log("inside part alone:", Problems(rep, s, RequireInside))
	}
	if !FailedAll(rep, s) {
		t.Error("a missing record did not fail the start")
	}
	var found bool
	for _, p := range Problems(rep, s, RequireAll) {
		found = found || strings.Contains(p, "unsupported")
	}
	if !found {
		t.Errorf("problems do not say unsupported: %v", Problems(rep, s, RequireAll))
	}
}

func TestStopRuleData(t *testing.T) {
	if StopSignalTerm != "SIGTERM" || StopSignalKill != "SIGKILL" || StopStateUnknown != "unknown" {
		t.Error("stop rule names")
	}
	if StopTermWait != 2*time.Second || StopKillWait != 2*time.Second {
		t.Error("stop rule times")
	}
}

// HolderPaths of the input default to the spec's: leaving them out must not
// turn the outside half of assertion 1 into not-checked, or hide a leak.
func TestRunOutsideDefaultsHolderPaths(t *testing.T) {
	s := testSpec(profile.Claude)
	in := OutsideInput{
		Spec: s, Role: "arc-lead", User: "someone", Version: "2.1.0", Now: t0,
		Binds: []profile.Bind{{Kind: profile.KindRoBind, Inside: "/leak", Source: s.HolderPaths[2]}},
		Sys:   goodSystem(s), Lock: fakeLock{held: true}, Linger: fakeLinger{on: true},
	}
	in.Canary = testCanary(t)
	out := RunOutside(in)
	if out[0].ID != IDHolderHidden+OutsideSuffix || out[0].Status != StatusFailed {
		t.Errorf("a bind of a holder path with no HolderPaths given: %+v", out[0])
	}
	in.Binds = nil
	if out := RunOutside(in); out[0].Status != StatusOK {
		t.Errorf("no binds: %+v", out[0])
	}
}

// An outside half is never excused by the spec's optional list: the list may
// name assertion 4 only, and a not-checked outside result of 1 or 5 still
// fails the start (the spec that lists 1 is refused as a whole).
func TestOptionalNeverExcusesAnOutsideHalf(t *testing.T) {
	s := testSpec(profile.Claude)
	rep := insideReport(s).With(
		CheckHolderBinds(nil, nil), // not-checked: no holder path
		CheckCanary(nil),
		CheckTIOCSTIOutside(nil, false),
		Result{9, IDRoleLockLingering, StatusOK, ""}, Result{10, IDQualificationRecord, StatusOK, ""},
	)
	for _, optional := range [][]int{nil, {4}, {1}, {1, 5}} {
		s.Optional = optional
		if !FailedAll(rep, s) {
			t.Errorf("optional %v: a not-checked outside half passed: %v", optional, Problems(rep, s, RequireAll))
		}
	}
}
