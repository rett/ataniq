//go:build linux && ataniq_canary

package leadcontrol

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fill admits and finishes n commands named c0, c1, ... and closes the table.
func fill(t *testing.T, path string, o Options, n int) []Receipt {
	t.Helper()
	tb := openAt(t, path, o)
	var out []Receipt
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		admitFresh(t, tb, bind(t, id))
		r, err := tb.Finish(id, StatusDone, "", "", map[string]int{"i": i})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := tb.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

func kinds(lines []string) map[string]int {
	m := map[string]int{}
	for _, l := range lines {
		k, _, _ := strings.Cut(strings.TrimPrefix(l, `{"kind":"`), `"`)
		m[k]++
	}
	return m
}

func TestOpenCompactsAnOverfullFile(t *testing.T) {
	path := tablePath(t)
	// With the default bound nothing is dropped.
	all := fill(t, path, Options{}, 6)
	if got := kinds(fileLines(t, path)); got["admit"] != 6 || got["finish"] != 6 || got["tombstone"] != 0 {
		t.Fatalf("file before = %v", got)
	}

	tb := openAt(t, path, Options{MaxReceipts: 3, MaxTombstones: 10})
	lines := fileLines(t, path)
	if got := kinds(lines); got["admit"] != 3 || got["finish"] != 3 || got["tombstone"] != 3 || len(lines) != 9 {
		t.Fatalf("file after compaction = %v (%d lines)", got, len(lines))
	}
	// The newest three are kept as they were, the others are expired.
	for i := 3; i < 6; i++ {
		r, fresh, err := tb.Admit(bind(t, fmt.Sprintf("c%d", i)))
		if err != nil || fresh {
			t.Fatalf("c%d: fresh %v, err %v", i, fresh, err)
		}
		sameReceipt(t, r, all[i])
	}
	for i := 0; i < 3; i++ {
		if _, fresh, err := tb.Admit(bind(t, fmt.Sprintf("c%d", i))); !errors.Is(err, ErrExpired) || fresh {
			t.Fatalf("c%d: fresh %v, err %v; want ErrExpired", i, fresh, err)
		}
	}
	// A command admitted after the compaction lands in the compacted file.
	admitFresh(t, tb, bind(t, "new"))
	_ = tb.Close()

	// The compacted file replays, and the next Open writes nothing more.
	re := openAt(t, path, Options{MaxReceipts: 4, MaxTombstones: 10})
	if _, _, err := re.Admit(bind(t, "c0")); !errors.Is(err, ErrExpired) {
		t.Fatalf("c0 after the second Open: %v, want ErrExpired", err)
	}
	if r, fresh, err := re.Admit(bind(t, "c5")); err != nil || fresh {
		t.Fatalf("c5 after the second Open: %+v, %v, %v", r, fresh, err)
	}
	if r, fresh, err := re.Admit(bind(t, "new")); err != nil || fresh || r.Status != StatusUncertain {
		t.Fatalf("new after the second Open: %+v, %v, %v", r, fresh, err)
	}
}

func TestCompactionKeepsInterruptedReceiptsAsTheyWere(t *testing.T) {
	path := tablePath(t)
	tb := openAt(t, path, Options{})
	tk := bind(t, "ticket")
	tk.Op = OpTicket
	admitFresh(t, tb, tk)
	admitFresh(t, tb, bind(t, "start"))
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("d%d", i)
		admitFresh(t, tb, bind(t, id))
		finish(t, tb, id, StatusDone)
	}
	_ = tb.Close()

	// The Open that ends ticket and start also compacts: the two oldest are
	// interrupted and are the first dropped; the survivors are the finished.
	re := openAt(t, path, Options{MaxReceipts: 3})
	if _, _, err := re.Admit(tk); !errors.Is(err, ErrExpired) {
		t.Fatalf("ticket: %v, want ErrExpired", err)
	}
	lines := fileLines(t, path)
	if got := kinds(lines); got["interrupted"] != 0 || got["tombstone"] != 2 || got["admit"] != 3 {
		t.Fatalf("file = %v", got)
	}

	// Interrupted receipts that are kept are written as interrupted lines.
	path2 := tablePath(t)
	tb = openAt(t, path2, Options{})
	admitFresh(t, tb, tk)
	admitFresh(t, tb, bind(t, "start"))
	_ = tb.Close()
	re = openAt(t, path2, Options{MaxReceipts: 1})
	// "start" is the newest and is kept.
	r, fresh, err := re.Admit(bind(t, "start"))
	if err != nil || fresh || r.Status != StatusUncertain || r.Code != CodeInterrupted {
		t.Fatalf("start = %+v, %v, %v", r, fresh, err)
	}
	if got := kinds(fileLines(t, path2)); got["interrupted"] != 1 || got["finish"] != 0 {
		t.Fatalf("file = %v", got)
	}
	_ = re.Close()
	// And it still reads the same after another restart.
	again := openAt(t, path2, Options{MaxReceipts: 1})
	r2, fresh, err := again.Admit(bind(t, "start"))
	if err != nil || fresh {
		t.Fatal(err)
	}
	sameReceipt(t, r2, r)
}

func TestTheTombstonesAreBounded(t *testing.T) {
	path := tablePath(t)
	fill(t, path, Options{}, 8)
	tb := openAt(t, path, Options{MaxReceipts: 2, MaxTombstones: 2})
	// Six keys were dropped; only the two newest dropped ones are remembered.
	for i := 4; i < 8; i++ {
		_, fresh, err := tb.Admit(bind(t, fmt.Sprintf("c%d", i)))
		switch {
		case i >= 6: // held
			if err != nil || fresh {
				t.Errorf("c%d: fresh %v, err %v; want the held receipt", i, fresh, err)
			}
		case i >= 4: // tombstoned
			if !errors.Is(err, ErrExpired) {
				t.Errorf("c%d: %v, want ErrExpired", i, err)
			}
		}
	}
	if got := kinds(fileLines(t, path)); got["tombstone"] != 2 {
		t.Fatalf("file = %v", got)
	}
	// A key older than the horizon looks new: the residual risk the design names.
	_ = tb.Close()
	re := openAt(t, path, Options{MaxReceipts: 2, MaxTombstones: 2})
	if _, fresh, err := re.Admit(bind(t, "c0")); err != nil || !fresh {
		t.Fatalf("c0 beyond the horizon: fresh %v, err %v", fresh, err)
	}
}

func TestLoweringTheBoundOnReopenTrimsTheFile(t *testing.T) {
	path := tablePath(t)
	fill(t, path, Options{MaxReceipts: 2, MaxTombstones: 10}, 6)
	// 6 commands in a 2 slot table: Admit dropped the old ones in memory only,
	// the file still holds them all.
	if got := kinds(fileLines(t, path)); got["admit"] != 6 {
		t.Fatalf("file = %v", got)
	}
	tb := openAt(t, path, Options{MaxReceipts: 2, MaxTombstones: 10})
	if got := kinds(fileLines(t, path)); got["admit"] != 2 || got["tombstone"] != 4 {
		t.Fatalf("file = %v", got)
	}
	tb.Close()
	// Fewer tombstones than the file holds: the file is written again.
	openAt(t, path, Options{MaxReceipts: 2, MaxTombstones: 1})
	if got := kinds(fileLines(t, path)); got["tombstone"] != 1 || got["admit"] != 2 {
		t.Fatalf("file = %v", got)
	}
}

func TestACompactedFileIsPrivateAndLeavesNoTemporaryFile(t *testing.T) {
	path := tablePath(t)
	fill(t, path, Options{}, 5)
	// A temporary file that an earlier run left is replaced.
	writeFile(t, path+".tmp", "stale")
	old := syscall.Umask(0o277)
	tb := openAt(t, path, Options{MaxReceipts: 2})
	syscall.Umask(old)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", st.Mode(), err)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file: %v", err)
	}
	matches, _ := filepath.Glob(path + "*")
	if len(matches) != 1 {
		t.Fatalf("files = %v", matches)
	}
	// The table writes to the new file, which is the one at the path.
	admitFresh(t, tb, bind(t, "after"))
	if lines := fileLines(t, path); !strings.Contains(lines[len(lines)-1], `"id":"after"`) {
		t.Fatalf("last line = %q", lines[len(lines)-1])
	}
	// It holds the lock: another Open is refused.
	if _, err := Open(path, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("Open = %v, want ErrLocked", err)
	}
}

func TestAFailedCompactionLeavesTheTableAsItWas(t *testing.T) {
	path := tablePath(t)
	fill(t, path, Options{}, 5)
	before, _ := os.ReadFile(path)
	// A folder at the temporary file's name makes the unlink and the create fail.
	if err := os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path+".tmp", "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, Options{MaxReceipts: 2}); err == nil {
		t.Fatal("Open succeeded")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("a failed compaction changed the table")
	}
	// The lock was released with the failure.
	if err := os.RemoveAll(path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	openAt(t, path, Options{MaxReceipts: 2})
}
