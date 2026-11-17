package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/intake"
	"ataniq/internal/intake/verify"
)

// ready is a world with a lead branch "feature" that is fetched, verified
// with a passing gate and reviewed, so that integrate can be called.
type ready struct {
	*world
	base, tip string
	rev       reviewOut
}

func newReady(t *testing.T) ready {
	t.Helper()
	w := newWorld(t)
	w.runner = &fakeRunner{res: boundedexec.Result{Stdout: []byte("fine\n")}}
	w.initMirror()
	base := w.lead(testRole)
	w.branch(testRole, "feature")
	w.write(testRole, map[string]string{"feature.txt": "feature\n"})
	tip := w.commit(testRole, "add a feature")
	w.fetch(testRole)
	w.mustOK(w.sub("verify", w.verifyArgs(testRole, "feature", "make check")...))
	return ready{world: w, base: base, tip: tip, rev: w.review(testRole, "feature", "main", "--records", w.records)}
}

// integrate runs integrate with the whole sign-off.
func (r ready) integrateNow() (int, string, string) {
	return r.sub("integrate", r.integrateArgs(testRole, "feature", "main", r.rev)...)
}

// recorded returns the intake records of the records folder.
func (r ready) recorded() []intake.IntakeRecord {
	r.t.Helper()
	recs, err := r.store().Intakes()
	if err != nil {
		r.t.Fatal(err)
	}
	return recs
}

// TestIntegrateWarningsAreSuccess: an update that ended unclearly but left the
// branch at the tip with the record written, and a record whose folder could
// not be synced, are exit 0 with the record printed and a warning on standard
// error. The wrapper runs the real integration and adds the error that the
// real call returns when those things happen.
func TestIntegrateWarningsAreSuccess(t *testing.T) {
	cases := map[string]struct {
		extra error
		warns []string
	}{
		"recovered": {
			extra: fmt.Errorf("%w (the update ended with: signal: killed)", intake.ErrRecovered),
			warns: []string{"warning: the update of the upstream branch ended unclearly", "intake is recorded"},
		},
		"not synced": {
			extra: intake.ErrNotSynced,
			warns: []string{"warning: the record was written, but the records folder could not be synced", "Do not repeat"},
		},
		"both": {
			extra: errors.Join(fmt.Errorf("%w (the update ended with: signal: killed)", intake.ErrRecovered), intake.ErrNotSynced),
			warns: []string{"ended unclearly", "could not be synced"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newReady(t)
			r.integrate = func(ctx context.Context, src intake.Source, req intake.IntegrateRequest) (intake.IntakeRecord, error) {
				rec, err := intake.Integrate(ctx, src, req)
				if err != nil {
					return rec, err
				}
				return rec, c.extra
			}
			code, out, errb := r.integrateNow()
			if code != exitOK {
				t.Fatalf("exit %d\n%s\n%s", code, out, errb)
			}
			for _, want := range []string{"Integrated: the upstream branch moved", "Old: " + r.base, "New: " + r.tip} {
				if !strings.Contains(out, want) {
					t.Errorf("stdout lacks %q:\n%s", want, out)
				}
			}
			for _, want := range c.warns {
				if !strings.Contains(errb, want) {
					t.Errorf("stderr lacks %q:\n%s", want, errb)
				}
			}
			noControl(t, "stderr", errb)
			if got := refLines(r.refs())["refs/heads/main"]; got != r.tip {
				t.Errorf("main is at %s, want %s", got, r.tip)
			}
			if len(r.recorded()) != 1 {
				t.Errorf("intake records: %d", len(r.recorded()))
			}
		})
	}
}

// TestIntegrateOutcomeUnknownIsExitThree: the update ended unclearly and the
// branch could not be read. The owner is told to look at the branch with
// branches before trying again.
func TestIntegrateOutcomeUnknownIsExitThree(t *testing.T) {
	r := newReady(t)
	r.integrate = func(context.Context, intake.Source, intake.IntegrateRequest) (intake.IntakeRecord, error) {
		return intake.IntakeRecord{}, fmt.Errorf("%w: the branch could not be read: signal: killed", intake.ErrOutcomeUnknown)
	}
	code, out, errb := r.integrateNow()
	if code != exitFailed || out != "" {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	for _, want := range []string{"ataniq-intake branches", "before you try again", "may have moved", "no intake record was written"} {
		if !strings.Contains(errb, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errb)
		}
	}
	noControl(t, "stderr", errb)
}

// TestIntegrateNothingToIntegrateIsExitOne: a lead branch at the target is a
// refusal with a plain sentence.
func TestIntegrateNothingToIntegrateIsExitOne(t *testing.T) {
	r := newReady(t)
	r.mustOK(r.integrateNow())
	code, out, errb := r.integrateNow()
	if code != exitRefused || out != "" {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	want := "ataniq-intake integrate: nothing to integrate: the lead branch lead/feature is at the upstream branch main already. Nothing was moved.\n"
	if errb != want {
		t.Fatalf("stderr:\n%q\nwant\n%q", errb, want)
	}
}

// TestVerifyNotSyncedIsSuccessWithAWarning: a verification record that is
// written but whose folder could not be synced is stored, so verify goes on.
func TestVerifyNotSyncedIsSuccessWithAWarning(t *testing.T) {
	w := newWorld(t)
	w.runner = &fakeRunner{res: boundedexec.Result{Stdout: []byte("fine\n")}}
	w.putVer = func(st *intake.Store, project string, res verify.Result, gates []verify.Gate) (intake.VerificationRecord, error) {
		rec, err := st.PutVerification(project, res, gates)
		if err != nil {
			return rec, err
		}
		return rec, intake.ErrNotSynced
	}
	w.initMirror()
	base := w.lead(testRole)
	w.fetch(testRole)
	code, out, errb := w.sub("verify", w.verifyArgs(testRole, "main", "true")...)
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	if !strings.Contains(out, "Outcome: pass") || !strings.Contains(out, "Stored: a verification record") {
		t.Errorf("stdout:\n%s", out)
	}
	if !strings.Contains(errb, "warning: the record was written, but the records folder could not be synced") {
		t.Errorf("stderr:\n%s", errb)
	}
	if recs, err := w.store().Verifications(base); err != nil || len(recs) != 1 {
		t.Errorf("records: %v %v", recs, err)
	}
}

// TestReviewShowsTheGatesOfTheVerification: with --records, review lists the
// gates of the verification records of the tip before the sign-off, escaped,
// and says when there is none. Without --records it says nothing about it.
func TestReviewShowsTheGatesOfTheVerification(t *testing.T) {
	w := newWorld(t)
	w.runner = &fakeRunner{res: boundedexec.Result{Stdout: []byte("fine\n")}}
	w.initMirror()
	w.lead(testRole)
	w.branch(testRole, "feature")
	w.write(testRole, map[string]string{"feature.txt": "feature\n"})
	w.commit(testRole, "add a feature")
	w.fetch(testRole)

	plain := w.review(testRole, "feature", "main")
	if strings.Contains(plain.text, "erification") {
		t.Errorf("review without --records says something about verification:\n%s", plain.text)
	}
	none := w.review(testRole, "feature", "main", "--records", w.records)
	if !strings.Contains(none.text, "\nVerification: no verification recorded for this tip\n") {
		t.Errorf("no verification recorded:\n%s", none.text)
	}
	checkOwnLines(t, "review", none.text)

	args := append(w.verifyArgs(testRole, "feature", "go build ./... && echo \x1b[2Jdone"), "--gate", "second=go test ./...")
	w.mustOK(w.sub("verify", args...))
	rev := w.review(testRole, "feature", "main", "--records", w.records)
	checkOwnLines(t, "review", rev.text)
	for _, want := range []string{
		"\nVerification of this tip (your records, shown escaped):\n",
		"  pass of 2026-",
		", the newest, which decides\n",
		"    gate check: go build ./... && echo \\x1b[2Jdone\n",
		"    gate second: go test ./...\n",
	} {
		if !strings.Contains(rev.text, want) {
			t.Errorf("review lacks %q:\n%s", want, rev.text)
		}
	}
	if v, s := strings.Index(rev.text, "Verification of this tip"), strings.Index(rev.text, "Needs your sign-off"); v < 0 || s < 0 || v > s {
		t.Errorf("the verification is not before the sign-off (%d, %d):\n%s", v, s, rev.text)
	}
	// A later failure is the newest record and says so.
	w.runner = &fakeRunner{res: boundedexec.Result{Exit: 1}, err: &boundedexec.ExitError{Exit: 1}}
	w.sub("verify", w.verifyArgs(testRole, "feature", "false")...)
	rev = w.review(testRole, "feature", "main", "--records", w.records)
	if !strings.Contains(rev.text, "  fail of 2026-") || !strings.Contains(rev.text, "  pass of 2026-") {
		t.Errorf("review does not show both records:\n%s", rev.text)
	}
	if strings.Index(rev.text, "  fail of") > strings.Index(rev.text, "  pass of") {
		t.Errorf("the newest record is not first:\n%s", rev.text)
	}
	// The folder must be one of the owner's.
	code, out, errb := w.sub("review", "--role", testRole, "--branch", "feature", "--target", "main", "--records", w.state)
	if code != exitUsage || out != "" || errb == "" {
		t.Errorf("records in the state: exit %d\n%s\n%s", code, out, errb)
	}
}

// TestIntegratePrintsTheGatesOfThePass: after an integration the gates of the
// verification that allowed it are shown.
func TestIntegratePrintsTheGatesOfThePass(t *testing.T) {
	r := newReady(t)
	out := r.mustOK(r.integrateNow())
	checkOwnLines(t, "integrate", out)
	for _, want := range []string{
		"\nGates that the pass stands for (your records, shown escaped):\n",
		"  gate check: make check\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("integrate output lacks %q:\n%s", want, out)
		}
	}
}
