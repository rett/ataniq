package ownerjournal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The steps of one write, in order, and the steps of making a new store.
// The fault hook is called with each of these names.
var (
	writePoints  = []string{"before:begin", "after:begin", "before:insert", "after:insert", "before:commit", "after:commit"}
	createPoints = []string{"before:create", "after:create", "before:schema", "after:schema", "before:rename", "after:rename"}
)

// copyStore copies the store's file and the files SQLite keeps beside it,
// as they are at this moment, into a new private folder. It is what a
// process that died at this moment would have left on the disk.
func copyStore(t *testing.T, path string) string {
	t.Helper()
	to := newPath(t)
	for _, suffix := range append([]string{""}, sidecars...) {
		b, err := os.ReadFile(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to+suffix, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return to
}

// checkWhole opens the store at path and checks that it holds exactly
// want, that it takes a further entry with the next sequence number, and
// that it opens once more after that.
func checkWhole(t *testing.T, path string, want []Entry) {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("the store does not open: %v", err)
	}
	got := mustEntries(t, s)
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		s.Close()
		t.Fatalf("the store holds\n %+v\nwant\n %+v", got, want)
	}
	next, err := addLooked(s, base.Add(time.Hour))
	if err != nil || next.Seq != int64(len(want)+1) {
		s.Close()
		t.Fatalf("the next entry: sequence number %d, %v; want %d", next.Seq, err, len(want)+1)
	}
	mustClose(t, s)
	s, err = Open(path)
	if err != nil {
		t.Fatalf("the store does not open a second time: %v", err)
	}
	if got := mustEntries(t, s); len(got) != len(want)+1 {
		t.Fatalf("%d entries at the second opening, want %d", len(got), len(want)+1)
	}
	mustClose(t, s)
}

// A fault at any step of a write stops the write. The entry is there only
// when the fault came after the commit, and the store goes on working.
func TestAFaultStopsAWrite(t *testing.T) {
	boom := errors.New("fault")
	for _, point := range writePoints {
		t.Run(point, func(t *testing.T) {
			path := newPath(t)
			armed := false
			s, err := openWithFault(path, func(p string) error {
				if armed && p == point {
					return boom
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			want := fill(t, s, 2)
			armed = true
			target := Entry{Seq: 3, Kind: KindLooked, At: base.Add(time.Minute), Body: []byte("{}")}
			if _, err := addLooked(s, target.At); !errors.Is(err, boom) {
				t.Fatalf("the write: %v, want the fault", err)
			}
			armed = false
			if point == "after:commit" {
				want = append(want, target)
			}
			if got := mustEntries(t, s); !reflect.DeepEqual(got, want) {
				t.Fatalf("after the fault:\n got %+v\nwant %+v", got, want)
			}
			mustClose(t, s)
			checkWhole(t, path, want)
		})
	}
}

// At each step of a write the files are copied as they are at that moment.
// The copy opens as a whole store, and the entry being written is in it
// only when the copy was taken after the commit.
func TestACopyTakenAtEachStepOfAWriteIsWhole(t *testing.T) {
	for _, point := range writePoints {
		t.Run(point, func(t *testing.T) {
			path := newPath(t)
			var copied string
			armed := false
			s, err := openWithFault(path, func(p string) error {
				if armed && p == point {
					copied = copyStore(t, path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			want := fill(t, s, 3)
			armed = true
			target, err := addLooked(s, base.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			armed = false
			if copied == "" {
				t.Fatal("the step was never reached")
			}
			if point == "after:commit" {
				want = append(want, target)
			}
			checkWhole(t, copied, want)
			// The store the copy was taken from was not disturbed.
			if got := mustEntries(t, s); len(got) != 4 {
				t.Fatalf("the store itself holds %d entries, want 4", len(got))
			}
		})
	}
}

// The child of the kill tests. It opens the store named in the
// environment with a fault hook that, at the step named there, says
// "ready" and then waits to be killed.
func TestKillChild(t *testing.T) {
	point := os.Getenv("ATANIQ_OWNERJOURNAL_KILL_POINT")
	if point == "" {
		return
	}
	path := os.Getenv("ATANIQ_OWNERJOURNAL_KILL_PATH")
	armed := os.Getenv("ATANIQ_OWNERJOURNAL_KILL_AT_OPEN") != ""
	var once sync.Once
	s, err := openWithFault(path, func(p string) error {
		if armed && p == point {
			once.Do(func() { fmt.Fprintln(os.Stdout, "ready") })
			time.Sleep(time.Minute)
			return errors.New("the child was not killed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	armed = true
	if _, err := addLooked(s, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	t.Fatal("the child did not reach its step")
}

// killAt runs the child until it reaches the step, lets during run while
// the child waits there, and kills it.
func killAt(t *testing.T, path, point string, atOpen bool, during func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKillChild$", "-test.timeout=45s")
	cmd.Env = append(os.Environ(), "ATANIQ_OWNERJOURNAL_KILL_POINT="+point, "ATANIQ_OWNERJOURNAL_KILL_PATH="+path)
	if atOpen {
		cmd.Env = append(cmd.Env, "ATANIQ_OWNERJOURNAL_KILL_AT_OPEN=1")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		w.Close()
		t.Fatal(err)
	}
	w.Close()
	killed := false
	defer func() {
		if !killed {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	if err := r.SetReadDeadline(time.Now().Add(40 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("the child at %s said %q, %v", point, line, err)
	}
	if during != nil {
		during()
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	killed = true
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGKILL {
		t.Fatalf("the child was not killed at its step: %v", cmd.ProcessState)
	}
}

// A process is killed at each step of a write, without closing the store.
// While it lives no one else gets the store; after it the store opens
// whole, with the entry only when the commit had been made.
func TestAProcessKilledAtEachStepOfAWriteLeavesAWholeStore(t *testing.T) {
	for _, point := range writePoints {
		t.Run(point, func(t *testing.T) {
			path := newPath(t)
			s := mustOpen(t, path)
			want := fill(t, s, 3)
			mustClose(t, s)
			killAt(t, path, point, false, func() {
				if got, err := Open(path); !errors.Is(err, ErrLocked) {
					if err == nil {
						got.Close()
					}
					t.Errorf("Open while the writer lives: %v, want %v", err, ErrLocked)
				}
			})
			if point == "after:commit" {
				want = append(want, Entry{Seq: 4, Kind: KindLooked, At: base.Add(time.Minute), Body: []byte("{}")})
			}
			checkWhole(t, path, want)
		})
	}
}

// A process is killed at each step of making a new store. The next Open
// finds either no store, and makes one, or a whole empty one, and nothing
// half-made stays behind.
func TestAProcessKilledWhileMakingAStoreLeavesNoHalfStore(t *testing.T) {
	for _, point := range createPoints {
		t.Run(point, func(t *testing.T) {
			path := newPath(t)
			killAt(t, path, point, true, nil)
			_, err := os.Lstat(path)
			there := err == nil
			if wantThere := point == "after:rename"; there != wantThere {
				t.Fatalf("the store's file is there: %v, want %v", there, wantThere)
			}
			checkWhole(t, path, nil)
			if got := leftovers(t, path); len(got) != 0 {
				t.Fatalf("left beside the store: %v", got)
			}
		})
	}
}

// A fault while a new store is made leaves nothing under the store's name
// or beside it, and the next Open makes the store.
func TestAFaultWhileMakingAStoreLeavesNothing(t *testing.T) {
	boom := errors.New("fault")
	for _, point := range createPoints {
		t.Run(point, func(t *testing.T) {
			path := newPath(t)
			s, err := openWithFault(path, func(p string) error {
				if p == point {
					return boom
				}
				return nil
			})
			if !errors.Is(err, boom) {
				if err == nil {
					s.Close()
				}
				t.Fatalf("Open: %v, want the fault", err)
			}
			if point == "after:rename" {
				// The store was whole and named before the fault.
				checkWhole(t, path, nil)
				return
			}
			if got, _ := os.ReadDir(filepath.Dir(path)); len(got) != 0 {
				t.Fatalf("the failed Open left %d files", len(got))
			}
			checkWhole(t, path, nil)
		})
	}
}

// What a crashed Open left under the new name is not taken for a store:
// it is thrown away, whatever it holds.
func TestLeftoversOfACrashedOpenAreThrownAway(t *testing.T) {
	path := newPath(t)
	if err := os.WriteFile(path+newSuffix, []byte("half a store"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+newSuffix+"-journal", []byte("half a journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkWhole(t, path, nil)
	if got := leftovers(t, path); len(got) != 0 {
		t.Fatalf("left beside the store: %v", got)
	}
}

// While one Open is making a store, a second one is refused and does not
// disturb it.
func TestAnOpenWhileAStoreIsBeingMadeIsRefused(t *testing.T) {
	path := newPath(t)
	var second error
	s, err := openWithFault(path, func(p string) error {
		if p == "after:schema" {
			got, err := Open(path)
			if err == nil {
				got.Close()
			}
			second = err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(second, ErrLocked) {
		t.Fatalf("the second Open: %v, want %v", second, ErrLocked)
	}
	fill(t, s, 1)
	mustClose(t, s)
	checkWhole(t, path, []Entry{{Seq: 1, Kind: KindLooked, At: base, Body: []byte("{}")}})
}

// A commit that returned an error leaves it open whether the entry is in the
// file. The store then refuses every call, reads and writes, with
// ErrUncertain, until it is closed and opened again; the reopened store
// answers what is there.
func TestACommitThatFailedRefusesEveryLaterCall(t *testing.T) {
	boom := errors.New("fault")
	path := newPath(t)
	armed := false
	s, err := openWithFault(path, func(p string) error {
		if armed && p == "commit" {
			return boom
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := fill(t, s, 2)
	armed = true
	at := base.Add(time.Minute)
	if _, err := addLooked(s, at); !errors.Is(err, ErrUncertain) {
		t.Fatalf("the write: %v, want %v", err, ErrUncertain)
	}
	armed = false
	if _, err := addLooked(s, at.Add(time.Second)); !errors.Is(err, ErrUncertain) {
		t.Fatalf("a write after it: %v, want %v", err, ErrUncertain)
	}
	if _, err := s.Entries(0, 0); !errors.Is(err, ErrUncertain) {
		t.Fatalf("Entries after it: %v, want %v", err, ErrUncertain)
	}
	if _, err := s.Undelivered(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("Undelivered after it: %v, want %v", err, ErrUncertain)
	}
	if _, err := s.LookedAt(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("LookedAt after it: %v, want %v", err, ErrUncertain)
	}
	mustClose(t, s)
	// The commit did reach the file here, as it may; the reopened store
	// says so.
	checkWhole(t, path, append(want, Entry{Seq: 3, Kind: KindLooked, At: at, Body: []byte("{}")}))
}
