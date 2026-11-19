package mergebuild

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
)

// slowGit returns a git that never ends one kind of run: a script that
// passes every command line on to the real git, except one that holds the
// argument named, for which it leaves a mark and sleeps. The mark shows
// that the run was reached.
func slowGit(t *testing.T, real gitenv.Git, hangOn string) (g gitenv.Git, mark string) {
	t.Helper()
	dir := t.TempDir()
	mark = filepath.Join(dir, "reached")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = " + shellWord(hangOn) + " ]; then\n" +
		"    : > " + shellWord(mark) + "\n" +
		"    exec sleep 600\n" +
		"  fi\n" +
		"done\n" +
		"exec " + shellWord(real.Path) + " \"$@\"\n"
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := gitenv.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return g, mark
}

func shellWord(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Every git run has a deadline: whichever run never ends, the call returns
// when its time is used up, with the runner's time-limit error, and leaves
// no goroutine and no half-made result behind.
func TestEveryGitRunHasADeadline(t *testing.T) {
	// Counted here, around all the runs below.
	noGoroutineLeft(t)
	const limit = 3 * time.Second
	type outcome struct {
		err     error
		took    time.Duration
		reached bool
	}
	// hang runs do with a git that never ends one kind of run.
	hang := func(l *lab, slow gitenv.Git, mark string, do func(Repo) error) outcome {
		repo := Repo{Git: slow, GitDir: l.repo.GitDir, Timeout: limit}
		start := time.Now()
		err := do(repo)
		took := time.Since(start)
		_, statErr := os.Stat(mark)
		return outcome{err: err, took: took, reached: statErr == nil}
	}
	check := func(t *testing.T, o outcome) {
		t.Helper()
		if !o.reached {
			t.Errorf("the run was never started (the call returned %v)", o.err)
		}
		if !errors.Is(o.err, boundedexec.ErrTimeout) {
			t.Errorf("the call returned %v, want the runner's time-limit error", o.err)
		}
		// The limit, the runner's fixed wait for the pipes, and slack.
		if o.took < limit/2 || o.took > limit+10*time.Second {
			t.Errorf("the call took %v with a limit of %v", o.took, limit)
		}
	}

	// All the runs below wait side by side; the group ends when the last
	// of them has. Each script is written before its run joins the others,
	// while nothing else starts a process: a process started while the
	// script is still open for writing would keep it open, and the script
	// could then not be run ("text file busy").
	real := needGit(t)
	t.Run("calls", func(t *testing.T) {
		for _, hangOn := range []string{"rev-parse", "--batch-check", "for-each-ref", "merge-base", "merge-tree", "hash-object", "update-ref"} {
			t.Run("Build "+hangOn, func(t *testing.T) {
				slow, mark := slowGit(t, real, hangOn)
				t.Parallel()
				l := newLab(t)
				s := newScene(t, l, nil, nil)
				commits := l.count("commit")
				var m Merge
				o := hang(l, slow, mark, func(repo Repo) error {
					var err error
					m, err = Build(ctx, repo, inputs(s.target, s.feature), "k")
					if err == nil {
						t.Errorf("Build returned %+v, %v", m, err)
					}
					return err
				})
				// A pin that may exist comes with the merge, so that the
				// caller can look and unpin; any other error comes with
				// nothing. The merge is the one a normal build gives.
				if hangOn == "update-ref" {
					want, err := Build(ctx, l.repo, inputs(s.target, s.feature), "other")
					if err != nil || m != want || m.Commit == "" {
						t.Errorf("Build returned %+v with %v, want the merge %+v", m, o.err, want)
					}
					// Taken away again, so that the refs below are the
					// slow run's own.
					if err := Unpin(ctx, l.repo, "other", m.Commit); err != nil {
						t.Errorf("Unpin: %v", err)
					}
				} else if m != (Merge{}) {
					t.Errorf("Build returned %+v with %v", m, o.err)
				}
				check(t, o)
				if got := l.refs(); got != "" {
					t.Errorf("refs were written: %q", got)
				}
				// Only the run that stores the pin leaves a commit: it is
				// written, and the pin's state is what the error names.
				wantCommits, unclear := commits, false
				if hangOn == "update-ref" {
					wantCommits, unclear = commits+1, true
				}
				if got := l.count("commit"); got != wantCommits {
					t.Errorf("%d commits were written, want %d", got-commits, wantCommits-commits)
				}
				if errors.Is(o.err, ErrPinUnclear) != unclear {
					t.Errorf("the error %v; ErrPinUnclear expected: %v", o.err, unclear)
				}
			})
		}

		for _, hangOn := range []string{"rev-parse", "--batch-check", "--batch", "merge-base", "merge-tree"} {
			t.Run("Check "+hangOn, func(t *testing.T) {
				slow, mark := slowGit(t, real, hangOn)
				t.Parallel()
				l := newLab(t)
				s := newScene(t, l, nil, nil)
				m, err := Build(ctx, l.repo, inputs(s.target, s.feature), "k")
				if err != nil {
					t.Fatal(err)
				}
				check(t, hang(l, slow, mark, func(repo Repo) error {
					return Check(ctx, repo, m.Commit, inputs(s.target, s.feature))
				}))
			})
		}

		for _, hangOn := range []string{"rev-parse", "for-each-ref", "update-ref"} {
			t.Run("Unpin "+hangOn, func(t *testing.T) {
				slow, mark := slowGit(t, real, hangOn)
				t.Parallel()
				l := newLab(t)
				s := newScene(t, l, nil, nil)
				m, err := Build(ctx, l.repo, inputs(s.target, s.feature), "k")
				if err != nil {
					t.Fatal(err)
				}
				o := hang(l, slow, mark, func(repo Repo) error { return Unpin(ctx, repo, "k", m.Commit) })
				check(t, o)
				if errors.Is(o.err, ErrPinUnclear) != (hangOn == "update-ref") {
					t.Errorf("the error is %v", o.err)
				}
				if got, want := l.refs(), m.Commit+" refs/ataniq/pending/k"; got != want {
					t.Errorf("the refs are %q, want the pin %q", got, want)
				}
			})
		}
	})
}

// The context bounds a call as well: when it ends, the run is stopped and
// the call returns its error.
func TestContextEndsACall(t *testing.T) {
	noGoroutineLeft(t)
	l := newLab(t)
	s := newScene(t, l, nil, nil)
	g, mark := slowGit(t, l.repo.Git, "merge-tree")
	repo := Repo{Git: g, GitDir: l.repo.GitDir}

	short, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Build(short, repo, inputs(s.target, s.feature), "k")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Build returned %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("Build took %v", took)
	}
	if _, statErr := os.Stat(mark); statErr != nil {
		t.Error("the merge was never started")
	}
	if got := l.refs(); got != "" {
		t.Errorf("refs were written: %q", got)
	}

	// A context that has ended starts nothing.
	done, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Build(done, l.repo, inputs(s.target, s.feature), "k"); !errors.Is(err, context.Canceled) {
		t.Errorf("Build with an ended context returned %v", err)
	}
	if err := Check(done, l.repo, s.target, inputs(s.target, s.feature)); !errors.Is(err, context.Canceled) {
		t.Errorf("Check with an ended context returned %v", err)
	}
	if err := Unpin(done, l.repo, "k", s.target); !errors.Is(err, context.Canceled) {
		t.Errorf("Unpin with an ended context returned %v", err)
	}
}

// A call whose time is used up starts no further run.
func TestNoRunAfterTheDeadline(t *testing.T) {
	l := newLab(t)
	c, err := newCall(l.repo)
	if err != nil {
		t.Fatal(err)
	}
	c.deadline = time.Now().Add(-time.Second)
	if err := c.format(ctx); !errors.Is(err, boundedexec.ErrTimeout) {
		t.Errorf("a run after the deadline returned %v", err)
	}
}

// git is started in one place only, through the bounded runner: no file of
// the package but its tests imports os/exec, and the package imports
// nothing of this repository but the three packages the seams note names.
func TestGitRunsThroughTheRunnerOnly(t *testing.T) {
	allowed := map[string]bool{
		"ataniq/internal/approval/record":      true,
		"ataniq/internal/exchange/gitenv":      true,
		"ataniq/internal/exchange/boundedexec": true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files, runs := 0, 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		files++
		f, err := parser.ParseFile(token.NewFileSet(), e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == "os/exec" || path == "syscall" {
				t.Errorf("%s imports %s", e.Name(), path)
			}
			if first, _, _ := strings.Cut(path, "/"); (first == "ataniq" || strings.Contains(first, ".")) && !allowed[path] {
				t.Errorf("%s imports %s", e.Name(), path)
			}
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		runs += strings.Count(string(src), "boundedexec.Run(")
		if strings.Contains(string(src), "go func") {
			t.Errorf("%s starts a goroutine", e.Name())
		}
	}
	if files < 8 {
		t.Fatalf("read %d Go files, want the whole package", files)
	}
	if runs != 1 {
		t.Errorf("the package calls the runner in %d places, want the one in run.go", runs)
	}
}
