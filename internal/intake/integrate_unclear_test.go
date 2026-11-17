//go:build linux

package intake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/gitenv"
)

// wrapGit writes a git that runs the real git, and for update-ref then runs
// the shell text after, with $rc the status of the real run and $flag a path
// that the text may create. When afterFlag is not empty, a for-each-ref that
// starts once $flag exists runs that text instead of the real git.
func wrapGit(t *testing.T, real gitenv.Git, after, afterFlag string) (g gitenv.Git, flag string) {
	t.Helper()
	dir := t.TempDir()
	flag = filepath.Join(dir, "flag")
	script := "#!/bin/sh\n" +
		"flag='" + flag + "'\n" +
		"sub=\n" +
		"for a in \"$@\"; do case \"$a\" in update-ref|for-each-ref) sub=$a;; esac; done\n"
	if afterFlag != "" {
		script += "if [ \"$sub\" = for-each-ref ] && [ -e \"$flag\" ]; then " + afterFlag + "; fi\n"
	}
	script += "'" + real.Path + "' \"$@\"\nrc=$?\n" +
		"if [ \"$sub\" = update-ref ]; then " + after + "; fi\n" +
		"exit $rc\n"
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	g, err := gitenv.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return g, flag
}

// wrapOnlyFailing is a git whose update-ref fails with status 1 without
// running the real one.
func wrapOnlyFailing(t *testing.T, real gitenv.Git) gitenv.Git {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do if [ \"$a\" = update-ref ]; then exit 1; fi; done\n" +
		"exec '" + real.Path + "' \"$@\"\n"
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	g, err := gitenv.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// requireMovedAndRecorded checks the outcome of a move that happened: the
// target is at the tip, and one intake record of it exists.
func (g *rig) requireMovedAndRecorded(rec IntakeRecord) {
	g.t.Helper()
	if g.id(upstreamRef) != g.tip {
		g.t.Errorf("the target is at %s, not at the tip", g.id(upstreamRef))
	}
	got := g.intakes()
	if len(got) != 1 {
		g.t.Fatalf("%d intake records, want 1", len(got))
	}
	if got[0].Tip != g.tip || got[0].Base != g.base || got[0].TargetRef != upstreamRef || rec.Tip != g.tip {
		g.t.Errorf("record: %+v (returned %+v)", got[0], rec)
	}
}

// TestIntegrateUpdateEndsUnclearAfterTheMove: git runs the real update and
// then the call does not end cleanly. The target is at the tip, so the move
// happened and must be recorded. The record comes back with an error that
// matches ErrRecovered and keeps the error of the update.
func TestIntegrateUpdateEndsUnclearAfterTheMove(t *testing.T) {
	cases := map[string]struct {
		after   string
		timeout time.Duration
		cancel  bool
		check   func(err error) bool
	}{
		"status 1 after the update": {after: "exit 1", check: func(err error) bool {
			var ge *gitExit
			return errors.As(err, &ge) && ge.sub == "update-ref" && ge.exit == 1
		}},
		"killed by the time limit": {after: "sleep 20", timeout: 1500 * time.Millisecond, check: func(err error) bool {
			return errors.Is(err, ErrLimit)
		}},
		"cancelled context": {after: ": > \"$flag\"; sleep 20", cancel: true, check: func(err error) bool {
			return errors.Is(err, context.Canceled)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := flaggedScene(t)
			g := newRig(t, s)
			g.record(nil)
			wrapped, flag := wrapGit(t, s.git, tc.after, "")
			src := Source{Git: wrapped, GitDir: s.bare}
			req := g.req(s.inspect())
			req.Review.Limits.Timeout = tc.timeout
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				go func() {
					for {
						if _, err := os.Stat(flag); err == nil {
							cancel()
							return
						}
						select {
						case <-ctx.Done():
							return
						case <-time.After(5 * time.Millisecond):
						}
					}
				}()
			}
			start := time.Now()
			rec, err := Integrate(ctx, src, req)
			if !errors.Is(err, ErrRecovered) {
				t.Fatalf("Integrate: %v, want ErrRecovered", err)
			}
			if errors.Is(err, ErrMoved) || errors.Is(err, ErrRecordMissing) || errors.Is(err, ErrOutcomeUnknown) {
				t.Errorf("a recovered move has the wrong kind: %v", err)
			}
			if tc.check != nil && !tc.check(err) {
				t.Errorf("the error of the update is not kept: %v", err)
			}
			if time.Since(start) > 15*time.Second {
				t.Errorf("the call took %v", time.Since(start))
			}
			g.requireMovedAndRecorded(rec)
		})
	}
}

// TestIntegrateUpdateEndsUnclearAndTheRecordFails: the branch moved and the
// record cannot be written, so the error is a *RecordError that carries the
// record and names the failed update.
func TestIntegrateUpdateEndsUnclearAndTheRecordFails(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	wrapped, _ := wrapGit(t, s.git, "exit 1", "")
	src := Source{Git: wrapped, GitDir: s.bare}
	req := g.req(s.inspect())
	req.afterChecks = func() { g.store.dir.close() }
	rec, err := Integrate(context.Background(), src, req)
	var re *RecordError
	if !errors.As(err, &re) || !errors.Is(err, ErrRecordMissing) {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, ErrMoved) {
		t.Errorf("a moved branch is not ErrMoved: %v", err)
	}
	for _, want := range []string{"moved", "record is missing", "update", s.base, s.tip} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
	if re.Record.Tip != s.tip || re.Record.Base != s.base || rec.Tip != s.tip {
		t.Errorf("record: %+v", re.Record)
	}
	if g.id(upstreamRef) != s.tip {
		t.Error("the target did not move")
	}
}

// TestIntegrateUpdateFailsAndNothingMoved: the update ends with status 1 and
// the target is still at the base: the original error, no record.
func TestIntegrateUpdateFailsAndNothingMoved(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	src := Source{Git: wrapOnlyFailing(t, s.git), GitDir: s.bare}
	_, err := Integrate(context.Background(), src, g.req(s.inspect()))
	var ge *gitExit
	if !errors.As(err, &ge) || ge.sub != "update-ref" {
		t.Errorf("error = %v", err)
	}
	if errors.Is(err, ErrMoved) || errors.Is(err, ErrRecordMissing) {
		t.Errorf("error = %v", err)
	}
	if g.id(upstreamRef) != s.base || len(g.intakes()) != 0 {
		t.Error("something changed")
	}
}

// TestIntegrateUpdateEndsUnclearAndTheTargetCannotBeRead: the outcome is
// unknown, and the error says so and does not say that nothing changed.
func TestIntegrateUpdateEndsUnclearAndTheTargetCannotBeRead(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	wrapped, _ := wrapGit(t, s.git, ": > \"$flag\"; exit 1", "exit 3")
	src := Source{Git: wrapped, GitDir: s.bare}
	_, err := Integrate(context.Background(), src, g.req(s.inspect()))
	if err == nil {
		t.Fatal("no error")
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Errorf("the error is not ErrOutcomeUnknown: %v", err)
	}
	if errors.Is(err, ErrMoved) || errors.Is(err, ErrRecovered) || strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("the error says that nothing changed: %v", err)
	}
	for _, want := range []string{"outcome is unknown", "may have moved", "update-ref"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
}

// TestIntegrateTargetAtTheTipAfterTheChecks: the target is put at the tip by
// someone else after the fresh inspection and before the update. The update
// is refused at its prepare step: git exits with a status and prints no
// "prepare: ok". This call moved nothing, so it writes no record and the
// error is ErrMoved, although the target is at the tip. A target that was at
// the tip before the call is refused earlier, as an empty review
// (TestIntegrateRefusesWhatIsNotFastForward).
func TestIntegrateTargetAtTheTipAfterTheChecks(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	req := g.req(s.inspect())
	req.afterChecks = func() { g.publish(upstreamRef, s.tip) }
	rec, err := Integrate(context.Background(), s.source(), req)
	if !errors.Is(err, ErrMoved) || errors.Is(err, ErrRecovered) {
		t.Fatalf("Integrate = %v, want ErrMoved", err)
	}
	if rec.Tip != "" {
		t.Errorf("a record came back: %+v", rec)
	}
	for _, want := range []string{s.tip, "moved nothing", "no record"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
	if g.id(upstreamRef) != s.tip {
		t.Error("the target is not at the tip")
	}
	if n := len(g.intakes()); n != 0 {
		t.Errorf("%d intake records, want none", n)
	}
}

// TestIntegrateTwoIntegrationsOfOneSignOff: a whole second Integrate of the
// same sign-off runs in the hook of the first, between its checks and its
// update, as a double submit would. The inner one moves the branch and
// records it. The outer update is refused, so the outer call returns ErrMoved
// and writes nothing: one move, one record.
func TestIntegrateTwoIntegrationsOfOneSignOff(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	outer := g.req(s.inspect())
	var innerRec IntakeRecord
	var innerErr error
	outer.afterChecks = func() {
		inner := g.req(s.inspect())
		innerRec, innerErr = Integrate(context.Background(), s.source(), inner)
	}
	rec, err := Integrate(context.Background(), s.source(), outer)
	if innerErr != nil {
		t.Fatalf("the inner call: %v", innerErr)
	}
	if !errors.Is(err, ErrMoved) {
		t.Fatalf("the outer call = %v, want ErrMoved", err)
	}
	if rec.Tip != "" {
		t.Errorf("the outer call returned a record: %+v", rec)
	}
	g.requireMovedAndRecorded(innerRec)
}

// wrapPrintsPrepared is a git whose update-ref prints what a transaction that
// was prepared prints, "start: ok" and "prepare: ok", and then fails with
// status 1 without running the real one.
func wrapPrintsPrepared(t *testing.T, real gitenv.Git) gitenv.Git {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do if [ \"$a\" = update-ref ]; then echo 'start: ok'; echo 'prepare: ok'; exit 1; fi; done\n" +
		"exec '" + real.Path + "' \"$@\"\n"
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	g, err := gitenv.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestIntegrateStatusAfterPrepareIsUnclear: git prints "prepare: ok" and then
// exits with a status, so the commit may have happened. The target is read
// again. At the base: the original error, no record. At the tip, put there
// meanwhile: the record and ErrRecovered.
func TestIntegrateStatusAfterPrepareIsUnclear(t *testing.T) {
	t.Run("target at the base", func(t *testing.T) {
		s := flaggedScene(t)
		g := newRig(t, s)
		g.record(nil)
		src := Source{Git: wrapPrintsPrepared(t, s.git), GitDir: s.bare}
		_, err := Integrate(context.Background(), src, g.req(s.inspect()))
		var ge *gitExit
		if !errors.As(err, &ge) || errors.Is(err, ErrMoved) || errors.Is(err, ErrRecovered) {
			t.Errorf("error = %v", err)
		}
		if g.id(upstreamRef) != s.base || len(g.intakes()) != 0 {
			t.Error("something changed")
		}
	})
	t.Run("target at the tip", func(t *testing.T) {
		s := flaggedScene(t)
		g := newRig(t, s)
		g.record(nil)
		src := Source{Git: wrapPrintsPrepared(t, s.git), GitDir: s.bare}
		req := g.req(s.inspect())
		req.afterChecks = func() { g.publish(upstreamRef, s.tip) }
		rec, err := Integrate(context.Background(), src, req)
		if !errors.Is(err, ErrRecovered) {
			t.Fatalf("error = %v, want ErrRecovered", err)
		}
		g.requireMovedAndRecorded(rec)
	})
}
