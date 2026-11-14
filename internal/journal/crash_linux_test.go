package journal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/dispatch"
)

// This fixture sink records each actual fake handoff independently of SQLite.
// The owned file is only a bounded test counter, not a production executor.
type fileSink struct {
	path  string
	after func()
}

func (b fileSink) Start(context.Context, dispatch.Intent) (dispatch.StartStatus, string, error) {
	f, err := os.OpenFile(b.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return dispatch.StartUnknown, "", err
	}
	_, err = f.Write([]byte("effect\n"))
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return dispatch.StartUnknown, "", err
	}
	if b.after != nil {
		b.after()
	}
	return dispatch.StartAcknowledged, "child\x00\xffstart", nil
}

func TestGuardedCrashHelper(t *testing.T) {
	point := os.Getenv("ATANIQ_JOURNAL_GUARDED_CHILD")
	if point == "" {
		return
	}
	path := os.Getenv("ATANIQ_JOURNAL_TEST_PATH")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pause := func() { fmt.Fprintln(os.Stdout, "ready"); <-ctx.Done() }
	fault := func(p string) error {
		if p == point {
			pause()
			return ctx.Err()
		}
		return nil
	}
	s, err := openWithFault(ctx, path, Options{Authority: "local-authority"}, false, fault)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := testIntent()
	a, err := s.Register(ctx, testPolicy(i))
	if err != nil {
		t.Fatal(err)
	}
	b := fileSink{path: filepath.Join(filepath.Dir(path), "sink-count")}
	if point == "handoff" {
		b.after = pause
	}
	if strings.HasPrefix(point, "late-") {
		s.fault = nil
	}
	if _, err = (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(ctx, i); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(point, "late-") {
		s.fault = func(p string) error {
			if "late-"+p == point {
				pause()
				return ctx.Err()
			}
			return nil
		}
		if err = a.Save(ctx, dispatch.Receipt{Intent: i, ID: "late", State: dispatch.Denied, Evidence: "contradictory"}); err != nil {
			t.Fatal(err)
		}
	}
	if strings.HasPrefix(point, "release-") {
		if err = a.Release(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("child missed requested seam")
}

func guardedChild(t *testing.T, path, point string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGuardedCrashHelper$", "-test.timeout=12s")
	cmd.Env = append(os.Environ(), "ATANIQ_JOURNAL_GUARDED_CHILD="+point, "ATANIQ_JOURNAL_TEST_PATH="+path)
	r, w, err := os.Pipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stdout = w
	cmd.Stderr = w
	if err = cmd.Start(); err != nil {
		r.Close()
		w.Close()
		cancel()
		t.Fatal(err)
	}
	w.Close()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
		r.Close()
		cancel()
	})
	if err = r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("seam %s: %q %v", point, line, err)
	}
	return cmd
}

func TestGuardedSIGKILLReopen(t *testing.T) {
	for _, point := range []string{"activate-before", "activate-after", "prepare-before", "prepare-after", "handoff", "save-before", "save-after", "late-save-before", "late-save-after", "release-before", "release-after", "recover-before", "recover-after"} {
		t.Run(point, func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			identity := mustInfo(t, s)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(point, "recover-") {
				setup := guardedChild(t, path, "activate-after")
				killAndWait(t, setup)
			}
			cmd := guardedChild(t, path, point)
			if _, err := Open(context.Background(), path, o); !errors.Is(err, ErrOwned) {
				t.Fatal("live writer admitted", err)
			}
			killAndWait(t, cmd)
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGKILL {
				t.Fatal("not killed at seam", cmd.ProcessState)
			}
			s, err := Open(context.Background(), path, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			after := mustInfo(t, s)
			if after.StoreID != identity.StoreID || after.IncarnationID != identity.IncarnationID {
				t.Fatal("changed identity")
			}
			sinkPath := filepath.Join(filepath.Dir(path), "sink-count")
			count := func() int {
				b, err := os.ReadFile(sinkPath)
				if errors.Is(err, os.ErrNotExist) {
					return 0
				}
				if err != nil {
					t.Fatal(err)
				}
				return strings.Count(string(b), "effect\n")
			}
			want := 0
			if point == "handoff" || strings.HasPrefix(point, "save-") || strings.HasPrefix(point, "late-") || strings.HasPrefix(point, "release-") {
				want = 1
			}
			if got := count(); got != want {
				t.Fatal("independent actual effects", got, want)
			}
			i := testIntent()
			if point == "activate-before" {
				if _, err := s.Activate(context.Background(), i); err == nil {
					t.Fatal("uncommitted registration exists")
				}
				return
			}
			a := activate(t, s, i)
			r, err := (dispatch.Coordinator{Journal: a, Boundary: fileSink{path: sinkPath}}).Dispatch(context.Background(), i)
			if err != nil || r.State != dispatch.Uncertain || count() != want {
				t.Fatal("replayed original attempt", r, err, count(), want)
			}
			record := inspect(t, s, i.ID)
			evidence := 0
			if point == "save-after" || strings.HasPrefix(point, "late-") || strings.HasPrefix(point, "release-") {
				evidence = 1
			}
			if point == "late-save-after" {
				evidence = 2
			}
			if len(record.Receipts) != evidence {
				t.Fatal("receipt commit lost", len(record.Receipts), evidence)
			}
			if evidence > 0 && record.Receipts[0].Evidence != "child\x00\xffstart" {
				t.Fatal("evidence normalized")
			}
			if err := a.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
			if inspect(t, s, i.ID).Active {
				t.Fatal("guard did not retire")
			}
		})
	}
}
