package assert

import (
	"fmt"
	"time"

	"ataniq/internal/leadsandbox/profile"
)

// RoleLock is the holder's exclusive lock on a role (the lead-hosting design,
// section 1.6). The holder implements it. No store is built here.
type RoleLock interface {
	// Held says whether this process holds the lock for the role.
	Held(role string) (bool, error)
}

// Lingering says whether the user's systemd instance lingers, so the lead's
// unit survives a logout. The holder or the daemon implements it.
type Lingering interface {
	// Enabled says whether lingering is on for the user.
	Enabled(user string) (bool, error)
}

// QualificationRecords looks up the record of a qualification run. The daemon
// implements it; no store is built in this package.
type QualificationRecords interface {
	// Lookup returns the record for the runtime, version and profile digest,
	// or nil when there is none. A store may return the closest record it
	// has instead (for another version, say): Current compares every field.
	Lookup(runtime profile.Runtime, version, digest string) (*QualificationRecord, error)
}

// QualificationRecord says that a qualification run was made for a runtime
// version and a profile digest. It is a fact about a run, not a statement that
// the sandbox is safe: the run's own report says what was checked.
type QualificationRecord struct {
	Runtime       profile.Runtime
	Version       string
	ProfileDigest string
	// Recorded is when the run was recorded.
	Recorded time.Time
	// Harness is the version of the harness that made the run.
	Harness string
	// Expires is when the record stops counting; the zero time means it
	// does not expire.
	Expires time.Time
}

// RecordState says how a record stands against a start.
type RecordState string

const (
	// RecordCurrent: a record exists for this runtime, version and digest,
	// recorded and not expired.
	RecordCurrent RecordState = "current"
	// RecordMissing: there is no record.
	RecordMissing RecordState = "missing"
	// RecordStale: there is a record that does not match or no longer counts.
	RecordStale RecordState = "stale"
)

// ConsoleUnsupported is the status the console shows for a lead whose record
// is missing or stale.
const ConsoleUnsupported = "unsupported"

// Console returns what the console shows: ConsoleUnsupported for a missing or
// stale record, "" for a current one (the record does not say more).
func (s RecordState) Console() string {
	if s == RecordCurrent {
		return ""
	}
	return ConsoleUnsupported
}

// Current says whether rec is a current record for the runtime, version and
// digest at the time now, and when it is not, why. A nil record is missing. A
// record is stale when its runtime, version or digest differ, when it has no
// recording time or harness version, when it is recorded after now, or when
// it expired at or before now. The reason is empty for a current record.
func Current(rec *QualificationRecord, runtime profile.Runtime, version, digest string, now time.Time) (RecordState, string) {
	short := clipDigest(digest)
	switch {
	case rec == nil:
		return RecordMissing, fmt.Sprintf("no qualification record for %s %s with profile %s", runtime, version, short)
	case version == "" || digest == "":
		return RecordStale, "no version or profile digest to compare the record with"
	case rec.Runtime != runtime:
		return RecordStale, fmt.Sprintf("the record is for runtime %s, not %s", rec.Runtime, runtime)
	case rec.Version != version:
		return RecordStale, fmt.Sprintf("the record is for version %s, not %s", rec.Version, version)
	case rec.ProfileDigest != digest:
		return RecordStale, fmt.Sprintf("the record is for another profile digest (%s), not %s", clipDigest(rec.ProfileDigest), short)
	case rec.Recorded.IsZero() || rec.Harness == "":
		return RecordStale, "the record has no recording time or harness version"
	case rec.Recorded.After(now):
		return RecordStale, "the record is dated after now"
	case !rec.Expires.IsZero() && !now.Before(rec.Expires):
		return RecordStale, fmt.Sprintf("the record expired at %s", rec.Expires.UTC().Format(time.RFC3339))
	}
	return RecordCurrent, ""
}

func clipDigest(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// outside builds the result of an outside check. The ID is the assertion's, or
// the outside half's when half is true.
func outside(n int, half bool, st Status, format string, args ...any) Result {
	id := idOf(n)
	if half {
		id += OutsideSuffix
	}
	return Result{Number: n, ID: id, Status: st, Detail: EscapeDetail(fmt.Sprintf(format, args...))}
}

// CheckHolderBinds is the outside half of assertion 1: no bind of the plan
// takes its source from, below or above a path the holder owns (its sockets
// and state). The comparison is lexical, as profile.CheckNeverMounted is: the
// caller has resolved symbolic links. With no holder path it is not-checked.
func CheckHolderBinds(binds []profile.Bind, holderPaths []string) Result {
	if len(holderPaths) == 0 {
		return outside(1, true, StatusNotChecked, "no holder path was given")
	}
	var t tally
	n := 0
	for _, b := range binds {
		if b.Source == "" {
			continue
		}
		n++
		if err := profile.CheckNeverMounted(b.Source, holderPaths); err != nil {
			t.fail("bind at %q: %s", b.Inside, reasonOf(err))
		}
	}
	r := t.result(1, fmt.Sprintf("none of %d bind sources touches a holder path", n))
	r.ID = idOf(1) + OutsideSuffix
	return r
}

// CheckTIOCSTIOutside is the outside half of assertion 5: before the start,
// the host's dev.tty.legacy_tiocsti is 0, or the profile names a seccomp
// filter that the probe will then confirm is loaded. It reads the sysctl; it
// never issues TIOCSTI.
func CheckTIOCSTIOutside(sys System, seccompExpected bool) Result {
	if sys == nil {
		return outside(5, true, StatusNotChecked, "no system to read the sysctl with")
	}
	st, detail := readSysctl(sys)
	switch {
	case st == sysctlZero:
		return outside(5, true, StatusOK, "%s", detail)
	case seccompExpected:
		return outside(5, true, StatusOK, "%s; the profile names a seccomp filter, which the probe must find loaded", detail)
	case st == sysctlUnreadable:
		return outside(5, true, StatusNotChecked, "%s", detail)
	}
	return outside(5, true, StatusFailed, "%s, and the profile names no seccomp filter", detail)
}

// CheckRoleLockAndLingering is assertion 9: this process holds the lock for the
// role, and lingering is on for the user. A question that cannot be asked is
// not-checked.
func CheckRoleLockAndLingering(lock RoleLock, ling Lingering, role, user string) Result {
	var t tally
	if lock == nil {
		t.skip("no role lock to ask")
	} else if held, err := lock.Held(role); err != nil {
		t.skip("the role lock for %q: %v", role, err)
	} else if !held {
		t.fail("this process does not hold the lock for role %q", role)
	}
	if ling == nil {
		t.skip("no lingering source to ask")
	} else if on, err := ling.Enabled(user); err != nil {
		t.skip("lingering for %q: %v", user, err)
	} else if !on {
		t.fail("lingering is off for user %q", user)
	}
	return t.result(9, fmt.Sprintf("the lock for role %q is held and lingering is on for %q", role, user))
}

// CheckQualification is assertion 10: a current qualification record exists
// for this runtime, version and profile digest. A missing or stale record is
// failed with the reason, and shows in the console as unsupported
// ([RecordState.Console]). A lookup that fails is not-checked.
func CheckQualification(store QualificationRecords, runtime profile.Runtime, version, digest string, now time.Time) Result {
	if store == nil {
		return outside(10, false, StatusNotChecked, "no qualification record store to ask")
	}
	rec, err := store.Lookup(runtime, version, digest)
	if err != nil {
		return outside(10, false, StatusNotChecked, "the record lookup failed: %v", err)
	}
	state, reason := Current(rec, runtime, version, digest, now)
	if state != RecordCurrent {
		return outside(10, false, StatusFailed, "%s: %s: %s", ConsoleUnsupported, state, reason)
	}
	return outside(10, false, StatusOK, "a current record exists for %s %s", runtime, version)
}

// OutsideInput is everything the outside checks need.
type OutsideInput struct {
	Spec    Spec
	Role    string
	User    string
	Version string // the pinned runtime version
	Now     time.Time
	// Binds is the plan's bind table, for the outside half of assertion 1.
	// HolderPaths are the paths of the holder's sockets and state; they are
	// normally the spec's.
	Binds       []profile.Bind
	HolderPaths []string

	Sys     System
	Lock    RoleLock
	Linger  Lingering
	Records QualificationRecords
}

// RunOutside runs the outside checks and returns their results: the outside
// half of 1, the outside half of 5, then 9 and 10. The holder adds them to the
// probe's report with Report.With and judges the whole with FailedAll.
func RunOutside(in OutsideInput) []Result {
	return []Result{
		CheckHolderBinds(in.Binds, in.HolderPaths),
		CheckTIOCSTIOutside(in.Sys, in.Spec.SeccompExpected),
		CheckRoleLockAndLingering(in.Lock, in.Linger, in.Role, in.User),
		CheckQualification(in.Records, in.Spec.Runtime, in.Version, in.Spec.Digest, in.Now),
	}
}
