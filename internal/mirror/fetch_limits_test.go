package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/safefs"
	"ataniq/internal/exchange/sandboxplan"
)

// canaries arms a fetch environment: the upload-pack command, the measure
// command and the git of the mirror each write a file in a folder when they
// start. none fails the test when any of them ran.
type canaries struct{ dir string }

func (e *fetchEnv) arm(t *testing.T) *canaries {
	t.Helper()
	c := &canaries{dir: t.TempDir()}
	e.planner.UploadPack = e.wrapper("touch '" + filepath.Join(c.dir, "upload-pack") + "'")
	e.planner.Measure = sandboxplan.Command{
		Argv: []string{"/bin/sh", "-c", "touch '" + filepath.Join(c.dir, "measure") + "'; echo 1"},
		Env:  []string{"PATH=/usr/bin:/bin"},
	}
	// The mirror's own git is a script that leaves a canary and fails. The
	// steps that must not start anything never reach it.
	e.m = &Mirror{
		git:     fakeGit(t, "touch '"+filepath.Join(c.dir, "git")+"'; exit 1"),
		path:    e.m.path,
		project: e.m.project,
	}
	return c
}

func (c *canaries) none(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s started", e.Name())
	}
}

func TestFetchOversizeSourceStartsNothing(t *testing.T) {
	e := newFetchEnv(t)
	c := e.arm(t)
	e.planner.Measure = sandboxplan.Command{
		Argv: []string{"/bin/sh", "-c", "touch '" + filepath.Join(c.dir, "measure") + "'; echo 5001 /objects"},
		Env:  []string{"PATH=/usr/bin:/bin"},
	}
	// Use the real git for the mirror again: only the upload-pack canary and
	// the staging folder prove that the fetch stopped first.
	real := newFetchEnvMirror(t, e)
	req := e.req()
	req.Limits.MaxSource = 5000
	was := real.state0(t)
	_, err := real.m.Fetch(context.Background(), req)
	var le *LimitError
	if !errors.As(err, &le) || le.What != "source" || le.Got != 5001 || le.Max != 5000 {
		t.Fatalf("Fetch = %v, want a source LimitError of 5001 against 5000", err)
	}
	if _, err := os.Lstat(filepath.Join(c.dir, "upload-pack")); err == nil {
		t.Error("upload-pack was started for a clone over the limit")
	}
	if _, err := os.Lstat(filepath.Join(c.dir, "measure")); err != nil {
		t.Errorf("the measure command did not run: %v", err)
	}
	real.unchanged(t, was)

	// Exactly the limit is fine.
	req.Limits.MaxSource = 5001
	if _, err := real.m.Fetch(context.Background(), req); err != nil {
		t.Errorf("a clone of exactly the limit: %v", err)
	}
}

// newFetchEnvMirror returns the environment with the real git again, for the
// mirror that arm replaced.
func newFetchEnvMirror(t *testing.T, e *fetchEnv) *fetchEnv {
	t.Helper()
	m, err := Open(context.Background(), e.g, e.m.path)
	if err != nil {
		t.Fatal(err)
	}
	c := *e
	c.m = m
	return &c
}

func TestFetchMeasureFailures(t *testing.T) {
	for name, argv := range map[string][]string{
		"fails":                {"/bin/sh", "-c", "exit 3"},
		"prints nothing":       {"/bin/sh", "-c", "true"},
		"prints text":          {"/bin/sh", "-c", "echo many"},
		"prints a sign":        {"/bin/sh", "-c", "echo -5"},
		"prints too much":      {"/bin/sh", "-c", "yes | head -c 100000"},
		"prints a huge number": {"/bin/sh", "-c", "echo 99999999999999999999"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newFetchEnv(t)
			c := e.arm(t)
			e.m = newFetchEnvMirror(t, e).m
			e.planner.Measure = sandboxplan.Command{Argv: argv, Env: []string{"PATH=/usr/bin:/bin"}}
			was := e.state0(t)
			_, err := e.m.Fetch(context.Background(), e.req())
			if err == nil || !strings.Contains(err.Error(), "measure command") {
				t.Fatalf("Fetch = %v, want a failure of the measure command", err)
			}
			if _, err := os.Lstat(filepath.Join(c.dir, "upload-pack")); err == nil {
				t.Error("upload-pack was started")
			}
			e.unchanged(t, was)
		})
	}
}

func TestFetchMirrorLimits(t *testing.T) {
	e := newFetchEnv(t)
	ctx := context.Background()
	size, err := e.m.Size(ctx, safefs.SizeLimits{MaxBytes: 1 << 40, MaxEntries: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	c := e.arm(t)
	e.m = newFetchEnvMirror(t, e).m
	was := e.state0(t)

	// A mirror at the limit takes nothing more, and nothing starts.
	req := e.req()
	req.Limits.MaxMirror = size.Bytes
	var le *LimitError
	if _, err := e.m.Fetch(ctx, req); !errors.As(err, &le) || le.What != "mirror" || le.Got != size.Bytes {
		t.Fatalf("Fetch with a full mirror = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(c.dir, "upload-pack")); err == nil {
		t.Error("upload-pack was started")
	}
	e.unchanged(t, was)

	// Room for the mirror, not for what the fetch stages.
	req.Limits.MaxMirror = size.Bytes + 4096
	if _, err := e.m.Fetch(ctx, req); !errors.As(err, &le) || le.What != "mirror" || le.Got <= le.Max {
		t.Fatalf("Fetch with no room = %v", err)
	}
	e.unchanged(t, was)

	// Too many entries in any measure.
	req.Limits.MaxMirror = 0
	req.Limits.MaxEntries = size.Entries - 1
	if _, err := e.m.Fetch(ctx, req); !errors.As(err, &le) || le.What != "entries" {
		t.Fatalf("Fetch with too many entries in the mirror = %v", err)
	}
	e.unchanged(t, was)
}

func TestFetchStagedEntriesLimit(t *testing.T) {
	e := newFetchEnv(t)
	ctx := context.Background()
	size, err := e.m.Size(ctx, safefs.SizeLimits{MaxBytes: 1 << 40, MaxEntries: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	// Each branch is a file in the staging repository.
	for i := 0; i < 80; i++ {
		e.lead.git(t, "branch", "b"+strconv.Itoa(i))
	}
	was := e.state0(t)
	// The mirror fits; the staging repository, with its skeleton and 81 ref
	// files, does not.
	req := e.req()
	req.Limits.MaxEntries = size.Entries + 40
	var le *LimitError
	if _, err := e.m.Fetch(ctx, req); !errors.As(err, &le) || le.What != "entries" {
		t.Fatalf("Fetch = %v, want an entries LimitError", err)
	}
	e.unchanged(t, was)
}

func TestFetchRefCap(t *testing.T) {
	e := newFetchEnv(t)
	for i := 0; i < 3; i++ {
		e.lead.git(t, "branch", "b"+strconv.Itoa(i))
	}
	// main and three branches are four refs.
	was := e.state0(t)
	req := e.req()
	req.Limits.MaxRefs = 3
	var le *LimitError
	if _, err := e.m.Fetch(context.Background(), req); !errors.As(err, &le) || le.What != "refs" || le.Max != 3 {
		t.Fatalf("Fetch = %v, want a refs LimitError", err)
	}
	e.unchanged(t, was)
	req.Limits.MaxRefs = 4
	if rep, err := e.m.Fetch(context.Background(), req); err != nil || len(rep.Updates) != 4 {
		t.Errorf("Fetch with a cap of exactly four refs = %+v, %v", rep, err)
	}
}

func TestFetchStagedLimit(t *testing.T) {
	setup := func(t *testing.T) *fetchEnv {
		e := newFetchEnv(t)
		for i := 0; i < 4; i++ {
			e.lead.commit(t, "big"+strconv.Itoa(i)+".bin", randomText(int64(i), 100<<10), "big")
		}
		return e
	}
	quick := func(t *testing.T) {
		old := watchInterval
		watchInterval = 20 * time.Millisecond
		t.Cleanup(func() { watchInterval = old })
	}

	t.Run("the watchdog stops the fetch", func(t *testing.T) {
		quick(t)
		e := setup(t)
		// The real upload-pack sends everything, and then the command
		// waits, so that the fetch is still running with the objects in the
		// staging repository. Only the watchdog ends it early.
		e.planner.UploadPack = []string{"/bin/sh", "-c", "'" + e.gup + "' \"$@\"; sleep 60", "sh"}
		was := e.state0(t)
		req := e.req()
		req.Limits.MaxStaged = 150 << 10
		start := time.Now()
		var le *LimitError
		_, err := e.m.Fetch(context.Background(), req)
		if !errors.As(err, &le) || le.What != "staged" || le.Max != 150<<10 || le.Got <= le.Max {
			t.Fatalf("Fetch = %v, want a staged LimitError", err)
		}
		if d := time.Since(start); d > 20*time.Second {
			t.Errorf("the fetch ran %v: the watchdog did not stop it", d)
		}
		e.unchanged(t, was)
	})

	t.Run("the check after the fetch", func(t *testing.T) {
		// The watchdog never looks: only the check after the fetch can
		// refuse.
		old := watchInterval
		watchInterval = time.Hour
		t.Cleanup(func() { watchInterval = old })
		e := setup(t)
		was := e.state0(t)
		req := e.req()
		req.Limits.MaxStaged = 150 << 10
		var le *LimitError
		rep, err := e.m.Fetch(context.Background(), req)
		if !errors.As(err, &le) || le.What != "staged" || le.Got != rep.Staged || rep.Staged <= 150<<10 {
			t.Fatalf("Fetch = %+v, %v, want a staged LimitError", rep, err)
		}
		e.unchanged(t, was)
	})

	t.Run("under the limit", func(t *testing.T) {
		e := setup(t)
		req := e.req()
		req.Limits.MaxStaged = 1 << 20
		if _, err := e.m.Fetch(context.Background(), req); err != nil {
			t.Errorf("Fetch = %v", err)
		}
	})
}

func TestFetchWatchdogWhenTheMeasureFails(t *testing.T) {
	// The measure hook stands for a walk that fails because git renames
	// files under it.
	errWalk := errors.New("test: the walk failed")
	slowly := func(e *fetchEnv) {
		// The fetch runs for a second, which is many watchdog intervals.
		e.planner.UploadPack = e.wrapper("sleep 1")
	}
	hook := func(t *testing.T, f func(n int) (safefs.Size, error)) *int {
		old, oldInterval := watchMeasureHook, watchInterval
		calls := 0
		watchMeasureHook = func() (safefs.Size, error) { calls++; return f(calls) }
		watchInterval = 20 * time.Millisecond
		t.Cleanup(func() { watchMeasureHook, watchInterval = old, oldInterval })
		return &calls
	}

	t.Run("failures in a row stop the fetch", func(t *testing.T) {
		e := newFetchEnv(t)
		slowly(e)
		calls := hook(t, func(int) (safefs.Size, error) { return safefs.Size{}, errWalk })
		was := e.state0(t)
		start := time.Now()
		_, err := e.m.Fetch(context.Background(), e.req())
		if err == nil || !strings.Contains(err.Error(), "staging folder could not be measured") || !errors.Is(err, errWalk) {
			t.Fatalf("Fetch = %v, want an error that says the staging folder could not be measured", err)
		}
		var le *LimitError
		if errors.As(err, &le) {
			t.Errorf("the error is a LimitError: %v", err)
		}
		if *calls != maxWatchFailures {
			t.Errorf("the measure ran %d times, want %d", *calls, maxWatchFailures)
		}
		if d := time.Since(start); d > 900*time.Millisecond {
			t.Errorf("the fetch ran %v: the watchdog did not stop it", d)
		}
		e.unchanged(t, was)
	})

	t.Run("failures with a success between them do not", func(t *testing.T) {
		e := newFetchEnv(t)
		slowly(e)
		calls := hook(t, func(n int) (safefs.Size, error) {
			if n%maxWatchFailures == 0 {
				return safefs.Size{Bytes: 1, Entries: 1}, nil
			}
			return safefs.Size{}, errWalk
		})
		if _, err := e.m.Fetch(context.Background(), e.req()); err != nil {
			t.Fatalf("Fetch = %v", err)
		}
		if *calls < 2*maxWatchFailures {
			t.Errorf("the measure ran only %d times, so the test proves little", *calls)
		}
	})
}

func TestFetchMirrorOverTheLimitAfterTheMove(t *testing.T) {
	// The limit after the move is a check, not a quota. The upload-pack
	// command grows the mirror while the fetch runs, as a second writer
	// would; the refs have moved when the fetch reports it.
	e := newFetchEnv(t)
	ctx := context.Background()
	size, err := e.m.Size(ctx, safefs.SizeLimits{MaxBytes: 1 << 40, MaxEntries: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(e.m.Path(), "objects", "junk")
	e.planner.UploadPack = e.wrapper("dd if=/dev/zero of='" + junk + "' bs=1k count=512 2>/dev/null")
	req := e.req()
	req.Limits.MaxMirror = size.Bytes + 300<<10
	rep, err := e.m.Fetch(ctx, req)
	var le *LimitError
	if !errors.As(err, &le) || le.What != "mirror" || le.Got != rep.MirrorAfter || rep.MirrorAfter <= req.Limits.MaxMirror {
		t.Fatalf("Fetch = %+v, %v, want a mirror LimitError with the report", rep, err)
	}
	if len(rep.Updates) != 1 || rep.Source != 1000 || rep.Staged <= 0 || rep.MirrorBefore != size.Bytes {
		t.Errorf("report = %+v", rep)
	}
	if refs, err := e.m.LeadBranches(ctx, "coder"); err != nil || len(refs) != 1 {
		t.Errorf("the refs did not move: %v, %v", refs, err)
	}
	noLeftovers(t, e.m)
}

func TestFetchTimeLimit(t *testing.T) {
	e := newFetchEnv(t)
	e.planner.UploadPack = []string{"/bin/sh", "-c", "sleep 60", "sh"}
	was := e.state0(t)
	req := e.req()
	req.Limits.Timeout = time.Second
	start := time.Now()
	_, err := e.m.Fetch(context.Background(), req)
	if !errors.Is(err, ErrFetchTimeout) {
		t.Fatalf("Fetch = %v, want ErrFetchTimeout", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("Fetch took %v for a limit of one second", d)
	}
	e.unchanged(t, was)

	// A cancelled context stops the fetch too, and is not a timeout.
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start = time.Now()
	_, err = e.m.Fetch(ctx, e.req())
	if err == nil || errors.Is(err, ErrFetchTimeout) {
		t.Errorf("a cancelled Fetch = %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("a cancelled Fetch took %v", d)
	}
	e.unchanged(t, was)

	// An expired context never starts anything.
	expired, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := e.m.Fetch(expired, e.req()); !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch with a cancelled context = %v", err)
	}
}
