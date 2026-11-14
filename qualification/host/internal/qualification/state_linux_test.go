//go:build linux && amd64

package qualification

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func emptyFixture(t *testing.T) *hostFixture {
	t.Helper()
	x := testHost(t)
	bindFixture(t, x)
	x.a.Empty, x.a.TargetExited, x.a.SliceStopped = true, true, true
	if err := x.c.Save(x.a, "empty", "prior reason"); err != nil {
		t.Fatal(err)
	}
	return x
}

func TestFailedCompletionKeepsPriorPhaseAndStickyRestartFence(t *testing.T) {
	for _, kind := range []string{"size", "random", "attempt-identity", "campaign-identity", "missing-attempt", "linked-attempt", "invalid-id", "invalid-phase", "nil", "file-sync", "rename", "parent-sync"} {
		t.Run(kind, func(t *testing.T) {
			x := emptyFixture(t)
			path := filepath.Join(x.c.Path, x.a.ID)
			before, err := os.ReadFile(filepath.Join(path, "intent.json"))
			if err != nil {
				t.Fatal(err)
			}
			oldID, oldRoot := x.a.ID, x.a.Root
			phase, reason := "complete", "new reason"
			a := x.a
			repair := func() {}
			safeMarker := false
			switch kind {
			case "size":
				reason = strings.Repeat("x", MaxInputBytes+1)
				safeMarker = true
			case "random":
				x.c.updateID = func() (string, error) { return "", errors.New("entropy unavailable") }
				safeMarker = true
			case "attempt-identity":
				x.a.Root.Inode++
			case "campaign-identity", "missing-attempt", "linked-attempt":
				original := path
				if kind == "campaign-identity" {
					original = x.c.Path
				}
				held := filepath.Join(t.TempDir(), "held")
				if err := os.Rename(original, held); err != nil {
					t.Fatal(err)
				}
				if kind == "campaign-identity" {
					err = os.Mkdir(original, 0700)
				} else if kind == "linked-attempt" {
					err = os.Symlink(held, original)
				}
				if err != nil {
					t.Fatal(err)
				}
				repair = func() {
					// Neither a replacement root nor a link may receive a marker.
					if _, err := os.Lstat(filepath.Join(original, "sync-failed")); !os.IsNotExist(err) {
						t.Fatal("marker written under rejected root", err)
					}
					if kind != "missing-attempt" {
						if err := os.Remove(original); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Rename(held, original); err != nil {
						t.Fatal(err)
					}
				}
			case "invalid-id":
				foreign := filepath.Join(filepath.Dir(x.c.Path), "foreign")
				if err := os.Mkdir(foreign, 0700); err != nil {
					t.Fatal(err)
				}
				x.a.ID = "../foreign"
				repair = func() {
					if _, err := os.Lstat(filepath.Join(foreign, "sync-failed")); !os.IsNotExist(err) {
						t.Fatal("traversing ID wrote a foreign marker", err)
					}
				}
			case "invalid-phase":
				phase = "invented"
			case "nil":
				a = nil
			case "file-sync":
				x.c.syncFile = func(*os.File) error { return errors.New("file sync failed") }
				safeMarker = true
			case "rename":
				x.c.rename = func(int, string, int, string) error { return errors.New("rename failed") }
				safeMarker = true
			case "parent-sync":
				x.c.syncDir = func(*os.File) error { return errors.New("parent sync failed") }
				safeMarker = true
			}
			if x.c.Save(a, phase, reason) == nil {
				t.Fatal("completion failure hidden")
			}
			if x.a.Phase != "empty" || x.a.Reason != "prior reason" || !x.c.Blocked || !x.c.fenced {
				t.Fatal("failed update published phase/reason or lost sticky fence")
			}
			x.a.ID, x.a.Root = oldID, oldRoot
			repair()
			after, err := os.ReadFile(filepath.Join(path, "intent.json"))
			if err != nil || kind != "parent-sync" && !bytes.Equal(before, after) {
				t.Fatal("prior durable unresolved intent replaced on early failure", err)
			}
			_, err = os.Lstat(filepath.Join(path, "sync-failed"))
			if safeMarker && err != nil || !safeMarker && !os.IsNotExist(err) {
				t.Fatal("marker did not respect validated ownership", err)
			}
			// Prove the shared pointer did not make reconcile skip cleanup as complete.
			if x.c.Records[oldID] != x.a || x.c.Records[oldID].Phase == "complete" {
				t.Fatal("shared record completed before durability")
			}
			x.c.syncFile, x.c.syncDir = (*os.File).Sync, (*os.File).Sync
			x.c.rename, x.c.updateID = syscall.Renameat, randomID
			// Even a later successful completion cannot clear the controller fence or
			// erase it on restart, including when the original error had no safe root.
			if err := x.c.Save(x.a, "complete", "later completion"); err != nil {
				t.Fatal(err)
			}
			if !x.c.Blocked || !x.c.fenced {
				t.Fatal("later completion cleared the sticky fence")
			}
			if _, err := x.c.Prepare(x.a.Config); err == nil {
				t.Fatal("new start accepted after failed durability")
			}
			x.c.Close()
			c, err := OpenCampaign(x.c.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if !c.Blocked || !c.fenced {
				t.Fatal("later completion erased restart fence")
			}
			if len(x.calls) != 0 {
				t.Fatal("state tests dispatched a supervisor command")
			}
		})
	}
}

func TestFailedCreateCannotBeClearedByExistingCompletion(t *testing.T) {
	x := emptyFixture(t)
	if err := x.c.Save(x.a, "complete", ""); err != nil {
		t.Fatal(err)
	}
	a, err := x.c.fresh(x.a.Config)
	if err != nil {
		t.Fatal(err)
	}
	x.c.syncDir = func(*os.File) error { return errors.New("create parent sync failed") }
	if x.c.create(a) == nil || !x.c.fenced {
		t.Fatal("failed create did not poison controller")
	}
	x.c.syncDir = (*os.File).Sync
	if err := x.c.Save(x.a, "complete", "later completion"); err != nil || !x.c.Blocked {
		t.Fatal("existing completion cleared create failure", err)
	}
	x.c.Close()
	c, err := OpenCampaign(x.c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Blocked {
		t.Fatal("orphaned create lost restart fence")
	}
}
