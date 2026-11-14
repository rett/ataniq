package journal

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"modernc.org/sqlite"
)

func TestOwnedProcessHelper(t *testing.T) {
	mode := os.Getenv("ATANIQ_JOURNAL_TEST_CHILD")
	if mode == "" {
		return
	}
	path := os.Getenv("ATANIQ_JOURNAL_TEST_PATH")
	o := Options{Authority: "local-authority"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var info Info
	if mode == "hold" {
		s, err := Open(ctx, path, o)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		info, err = s.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		owner, err := acquire(path, true)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.close()
		o, _ = o.normalized()
		db := pool(&connector{driver: &sqlite.Driver{}, owner: owner, options: o})
		defer db.Close()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := initializeIdentity(ctx, tx, o.Authority); err != nil {
			t.Fatal(err)
		}
		if mode == "after-commit" {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := owner.dir.Sync(); err != nil {
				t.Fatal(err)
			}
			info, err = readIdentity(ctx, db, o.Authority)
			if err != nil {
				t.Fatal(err)
			}
		}
		// Keep the actual uncommitted transaction/SQL handles alive at the seam.
		if err := json.NewEncoder(os.Stdout).Encode(info); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		return
	}
	// Hold mode needs ownership to outlive the readiness marker as well.
	// The defer above belongs to this function, not a helper scope.
	if err := json.NewEncoder(os.Stdout).Encode(info); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
}

func childAtSeam(t *testing.T, path, mode string) (*exec.Cmd, Info) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOwnedProcessHelper$", "-test.timeout=12s")
	cmd.Env = append(os.Environ(), "ATANIQ_JOURNAL_TEST_CHILD="+mode, "ATANIQ_JOURNAL_TEST_PATH="+path)
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
	if err := r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(r).ReadBytes('\n')
	if err != nil {
		t.Fatalf("child readiness: %v", err)
	}
	var info Info
	if err := json.Unmarshal(line, &info); err != nil {
		t.Fatalf("child readiness %q: %v", line, err)
	}
	return cmd, info
}

func killAndWait(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected killed child")
	}
}

func TestOwnedSubprocessAndCrashReopen(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	want := mustInfo(t, s)
	s.Close()
	cmd, got := childAtSeam(t, path, "hold")
	if got.StoreID != want.StoreID {
		t.Fatal("child opened wrong identity")
	}
	if _, err := Open(context.Background(), path, o); !errors.Is(err, ErrOwned) {
		t.Fatalf("live process ownership: %v", err)
	}
	killAndWait(t, cmd)
	s, err := Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := mustInfo(t, s); got != want {
		t.Fatalf("crash reopen changed identity: %+v", got)
	}
}

func TestInitializationCrash(t *testing.T) {
	for _, mode := range []string{"before-commit", "after-commit"} {
		t.Run(mode, func(t *testing.T) {
			path, o := fixture(t)
			cmd, want := childAtSeam(t, path, mode)
			killAndWait(t, cmd)
			s, err := Open(context.Background(), path, o)
			if mode == "before-commit" {
				if err == nil {
					s.Close()
					t.Fatal("adopted interrupted initialization")
				}
				if _, err := Create(context.Background(), path, o); !errors.Is(err, os.ErrExist) {
					t.Fatalf("overwrote interrupted state: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			got := mustInfo(t, s)
			if got.StoreID != want.StoreID || got.IncarnationID != want.IncarnationID {
				t.Fatal(fmt.Sprintf("lost committed identity: %+v != %+v", got, want))
			}
		})
	}
}
