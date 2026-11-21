//go:build linux && ataniq_canary

package leadcontrol

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDurabilityAcrossCloseAndReopen(t *testing.T) {
	path := tablePath(t)
	tb := openAt(t, path, Options{})
	a, b := bind(t, "done-1"), bind(t, "refused-1")
	b.Op = "lead.stop"
	admitFresh(t, tb, a)
	admitFresh(t, tb, b)
	done, err := tb.Finish("done-1", StatusDone, "", "", map[string]any{"role": "scratch-alpha/lead", "epoch": 7})
	if err != nil {
		t.Fatal(err)
	}
	refused, err := tb.Finish("refused-1", StatusRefused, "max-running", "At the cap of running leads.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tb.Close(); err != nil {
		t.Fatal(err)
	}

	re := openAt(t, path, Options{})
	for _, c := range []struct {
		b    Binding
		want Receipt
	}{{a, done}, {b, refused}} {
		r, fresh, err := re.Admit(c.b)
		if err != nil || fresh {
			t.Fatalf("%s after reopen: fresh %v, err %v", c.b.ID, fresh, err)
		}
		sameReceipt(t, r, c.want)
	}
	if r, ok := re.Last(a.Role); !ok || r.CommandID != "refused-1" {
		t.Fatalf("Last after reopen = %+v, %v", r, ok)
	}
	// The changed payload still conflicts after a reopen.
	a.Digest = digestOf("other")
	if _, _, err := re.Admit(a); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict after reopen: %v", err)
	}
}

func TestAdmissionIsDurableBeforeItReturns(t *testing.T) {
	path := tablePath(t)
	tb := openAt(t, path, Options{})
	admitFresh(t, tb, bind(t, "a"))
	// The file already holds the line, with the table still open and no Close.
	lines := fileLines(t, path)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], `{"kind":"admit"`) {
		t.Fatalf("file after Admit = %q", lines)
	}
	finish(t, tb, "a", StatusDone)
	if lines = fileLines(t, path); len(lines) != 2 || !strings.HasPrefix(lines[1], `{"kind":"finish"`) {
		t.Fatalf("file after Finish = %q", lines)
	}
}

func TestAPartialLastLineIsCut(t *testing.T) {
	good := `{"kind":"admit","at":"2026-11-20T12:00:01Z","client":"c1","id":"whole","op":"lead.start","role":"scratch-alpha/lead","digest":"` +
		hex.EncodeToString(make([]byte, 32)) + `"}`
	for name, tail := range map[string]string{
		"half a line":      good[:40],
		"one byte":         `{`,
		"whole but no LF":  strings.Replace(good, "whole", "tail", 1),
		"garbage":          "\x00\x01 not json",
		"nothing readable": `}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := tablePath(t)
			writeFile(t, path, good+"\n"+tail)
			tb := openAt(t, path, Options{})
			// The first line is kept, as interrupted (it was never finished), and
			// the zero digest of the stand-in binding is the one in the file ...
			r, fresh, err := tb.Admit(Binding{Client: "c1", ID: "whole", Op: "lead.start", Role: mustRole(t, "scratch-alpha/lead")})
			if err != nil || fresh || r.Status != StatusUncertain {
				t.Fatalf("whole line: %+v, %v, %v", r, fresh, err)
			}
			// ... and the unfinished tail is gone: its key is new.
			if _, fresh, err := tb.Admit(bind(t, "tail")); err != nil || !fresh {
				t.Fatalf("the cut tail: fresh %v, err %v", fresh, err)
			}
			_ = tb.Close()
			for _, l := range fileLines(t, path) {
				if strings.Contains(l, "not json") || l == "}}}" || l == "{" {
					t.Fatalf("the file still holds the tail: %q", l)
				}
			}
			// The file replays cleanly again.
			openAt(t, path, Options{})
		})
	}
}

func TestAPartialLastLineOfOnlyThatLineLeavesAnEmptyTable(t *testing.T) {
	path := tablePath(t)
	writeFile(t, path, `{"kind":"admit","at"`)
	tb := openAt(t, path, Options{})
	if _, fresh, err := tb.Admit(bind(t, "a")); err != nil || !fresh {
		t.Fatalf("Admit: %v, %v", fresh, err)
	}
	if lines := fileLines(t, path); len(lines) != 1 {
		t.Fatalf("lines = %q", lines)
	}
}

func TestInterruptedCommandsBecomeUncertain(t *testing.T) {
	path := tablePath(t)
	tb := openAt(t, path, Options{})
	ops := map[string]Status{
		"lead.define": StatusUncertain,
		"lead.start":  StatusUncertain,
		"lead.stop":   StatusUncertain,
		"lead.inject": StatusUncertain,
		"lead.ticket": StatusUnavailable,
	}
	var order []string
	for op := range ops {
		order = append(order, op)
	}
	for i, op := range order {
		b := bind(t, fmt.Sprintf("id-%d", i))
		b.Op = op
		admitFresh(t, tb, b)
	}
	// One command is finished before the "crash" and must stay as it was.
	admitFresh(t, tb, bind(t, "finished"))
	was := finish(t, tb, "finished", StatusDone)
	if err := tb.Close(); err != nil {
		t.Fatal(err)
	}

	re := openAt(t, path, Options{})
	for i, op := range order {
		b := bind(t, fmt.Sprintf("id-%d", i))
		b.Op = op
		r, fresh, err := re.Admit(b)
		if err != nil || fresh {
			t.Fatalf("%s after the restart: fresh %v, err %v", op, fresh, err)
		}
		if r.Status != ops[op] || r.Code != CodeInterrupted || r.Message != MessageInterrupted || r.FinishedAt == nil {
			t.Fatalf("%s after the restart = %+v, want %s and the interrupted words", op, r, ops[op])
		}
	}
	r, _, _ := re.Admit(bind(t, "finished"))
	sameReceipt(t, r, was)

	// Interrupted is final: Finish no longer finds the command, and the
	// receipt is the same after any number of further restarts.
	if _, err := re.Finish("id-0", StatusDone, "", "", nil); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("Finish of an interrupted command: %v, want ErrNotOpen", err)
	}
	size := fileSize(t, path)
	first, _, _ := re.Admit(bindOp(t, "id-0", order[0]))
	_ = re.Close()
	again := openAt(t, path, Options{})
	if fileSize(t, path) != size {
		t.Fatal("a second Open changed the file")
	}
	r, _, _ = again.Admit(bindOp(t, "id-0", order[0]))
	sameReceipt(t, r, first)
	// A command admitted after the restart is a new one; the old key never runs.
	if _, fresh, err := again.Admit(bind(t, "after")); err != nil || !fresh {
		t.Fatalf("a new command: %v, %v", fresh, err)
	}
}

func bindOp(t testing.TB, id, op string) Binding {
	b := bind(t, id)
	b.Op = op
	return b
}

func fileSize(t testing.TB, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

func TestAnInterruptedTicketFreesNothingElse(t *testing.T) {
	// An interrupted command is final, so its slot may be dropped and its id
	// reused by another client, as for every final receipt.
	path := tablePath(t)
	tb := openAt(t, path, Options{})
	admitFresh(t, tb, bind(t, "x"))
	_ = tb.Close()
	re := openAt(t, path, Options{})
	other := bind(t, "x")
	other.Client = "c2"
	admitFresh(t, re, other)
}

func TestEveryKindOfDamageRefusesTheStart(t *testing.T) {
	zero := hex.EncodeToString(make([]byte, 32))
	one := strings.Repeat("1", 64)
	admit := func(id string) string {
		return `{"kind":"admit","at":"2026-11-20T12:00:01Z","client":"c1","id":"` + id + `","op":"lead.start","role":"scratch-alpha/lead","digest":"` + zero + `"}`
	}
	fin := func(id string) string {
		return `{"kind":"finish","at":"2026-11-20T12:00:02Z","client":"c1","id":"` + id + `","status":"done"}`
	}
	for name, content := range map[string]string{
		"not json":                  "hello\n",
		"unknown member":            strings.Replace(admit("a"), `"op"`, `"extra":1,"op"`, 1) + "\n",
		"unknown kind":              `{"kind":"other"}` + "\n",
		"empty object":              "{}\n",
		"data after the record":     admit("a") + " {}\n",
		"two records":               admit("a") + admit("b") + "\n",
		"blank line":                admit("a") + "\n\n",
		"damage before a good line": "garbage\n" + admit("a") + "\n",
		"finish without admit":      fin("a") + "\n",
		"admitted twice":            admit("a") + "\n" + admit("a") + "\n",
		"finished twice":            admit("a") + "\n" + fin("a") + "\n" + fin("a") + "\n",
		"open id admitted again":    admit("a") + "\n" + strings.Replace(admit("a"), `"c1"`, `"c2"`, 1) + "\n",
		"bad time":                  strings.Replace(admit("a"), "2026-11-20T12:00:01Z", "yesterday", 1) + "\n",
		"bad role":                  strings.Replace(admit("a"), "scratch-alpha/lead", "Scratch Alpha", 1) + "\n",
		"bad op":                    strings.Replace(admit("a"), "lead.start", "Lead Start", 1) + "\n",
		"bad id":                    admit("a b") + "\n",
		"bad client":                strings.Replace(admit("a"), `"c1"`, `"c 1"`, 1) + "\n",
		"short digest":              strings.Replace(admit("a"), zero, "ab", 1) + "\n",
		"upper-case digest":         strings.Replace(admit("a"), zero, strings.Repeat("A", 64), 1) + "\n",
		"admit with a status":       strings.Replace(admit("a"), `"op"`, `"status":"done","op"`, 1) + "\n",
		"admit lacking a digest":    strings.Replace(admit("a"), `,"digest":"`+zero+`"`, "", 1) + "\n",
		"finish in progress":        admit("a") + "\n" + strings.Replace(fin("a"), `"done"`, `"in-progress"`, 1) + "\n",
		"finish with a bad code":    admit("a") + "\n" + strings.Replace(fin("a"), `"done"`, `"done","code":"Bad Code"`, 1) + "\n",
		"finish with a null result": admit("a") + "\n" + strings.Replace(fin("a"), `"done"`, `"done","result":null`, 1) + "\n",
		"finish without a status": admit("a") + "\n" +
			`{"kind":"finish","at":"2026-11-20T12:00:02Z","client":"c1","id":"a"}` + "\n",
		"interrupted as done": admit("a") + "\n" +
			`{"kind":"interrupted","at":"2026-11-20T12:00:02Z","client":"c1","id":"a","status":"done"}` + "\n",
		"interrupted with a code": admit("a") + "\n" +
			`{"kind":"interrupted","at":"2026-11-20T12:00:02Z","client":"c1","id":"a","status":"uncertain","code":"x"}` + "\n",
		"interrupted without admit": `{"kind":"interrupted","at":"2026-11-20T12:00:02Z","client":"c1","id":"a","status":"uncertain"}` + "\n",
		"tombstone short key":       `{"kind":"tombstone","key":"ab"}` + "\n",
		"tombstone with an id":      `{"kind":"tombstone","key":"` + one + `","id":"a"}` + "\n",
		"tombstone twice":           `{"kind":"tombstone","key":"` + one + `"}` + "\n" + `{"kind":"tombstone","key":"` + one + `"}` + "\n",
		"line over the cap":         admit(strings.Repeat("a", 64)) + strings.Repeat(" ", MaxLineBytes) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := tablePath(t)
			writeFile(t, path, content)
			before, _ := os.ReadFile(path)
			_, err := Open(path, Options{})
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Open = %v, want ErrCorrupt", err)
			}
			// Nothing is repaired.
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatalf("Open changed a corrupt file:\n%q\n%q", before, after)
			}
			// And the lock is released: the failed Open left no handle behind.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			openAt(t, path, Options{})
		})
	}
}

func TestAKeyAfterItsTombstoneIsCorrupt(t *testing.T) {
	path := tablePath(t)
	h := key{"c1", "a"}.hash()
	writeFile(t, path, `{"kind":"tombstone","key":"`+hex.EncodeToString(h[:])+`"}`+"\n"+
		`{"kind":"admit","at":"2026-11-20T12:00:01Z","client":"c1","id":"a","op":"lead.start","role":"scratch-alpha/lead","digest":"`+
		hex.EncodeToString(make([]byte, 32))+`"}`+"\n")
	if _, err := Open(path, Options{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open = %v, want ErrCorrupt", err)
	}
}

func TestAClockThatWentBackIsNotDamage(t *testing.T) {
	path := tablePath(t)
	tb := openAt(t, path, Options{Now: newClock().now})
	admitFresh(t, tb, bind(t, "a"))
	admitFresh(t, tb, bind(t, "b"))
	finish(t, tb, "a", StatusDone)
	_ = tb.Close()
	// The second run's clock starts before the first run's last reading.
	re := openAt(t, path, Options{Now: newClock().now})
	if r, _, err := re.Admit(bind(t, "b")); err != nil || r.Status != StatusUncertain {
		t.Fatalf("b after the restart: %+v, %v", r, err)
	}
}

func TestFileRules(t *testing.T) {
	t.Run("a new file is private", func(t *testing.T) {
		path := tablePath(t)
		// A umask that clears the owner's bits must not leave 0400.
		old := syscall.Umask(0o277)
		openAt(t, path, Options{})
		syscall.Umask(old)
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v", st.Mode(), err)
		}
	})
	t.Run("a symbolic link", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		writeFile(t, target, "")
		path := filepath.Join(dir, "lead-commands.jsonl")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, Options{}); !errors.Is(err, ErrInsecure) {
			t.Fatalf("Open = %v, want ErrInsecure", err)
		}
	})
	t.Run("a folder that is a symbolic link", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(filepath.Join(link, "lead-commands.jsonl"), Options{}); !errors.Is(err, ErrInsecure) {
			t.Fatalf("Open = %v, want ErrInsecure", err)
		}
	})
	t.Run("a missing folder", func(t *testing.T) {
		if _, err := Open(filepath.Join(t.TempDir(), "absent", "lead-commands.jsonl"), Options{}); err == nil {
			t.Fatal("Open succeeded")
		}
	})
	t.Run("a wider mode", func(t *testing.T) {
		path := tablePath(t)
		writeFile(t, path, "")
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, Options{}); !errors.Is(err, ErrInsecure) {
			t.Fatalf("Open = %v, want ErrInsecure", err)
		}
	})
	t.Run("a narrower mode", func(t *testing.T) {
		path := tablePath(t)
		writeFile(t, path, "")
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, Options{}); err == nil {
			t.Fatal("Open succeeded")
		}
	})
	t.Run("a second link", func(t *testing.T) {
		path := tablePath(t)
		writeFile(t, path, "")
		if err := os.Link(path, path+".other"); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, Options{}); !errors.Is(err, ErrInsecure) {
			t.Fatalf("Open = %v, want ErrInsecure", err)
		}
	})
	t.Run("a folder in its place", func(t *testing.T) {
		path := tablePath(t)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, Options{}); err == nil {
			t.Fatal("Open succeeded")
		}
	})
}

func TestOneProcessOwnsTheFile(t *testing.T) {
	path := tablePath(t)
	tb := openAt(t, path, Options{})
	if _, err := Open(path, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open = %v, want ErrLocked", err)
	}
	if err := tb.Close(); err != nil {
		t.Fatal(err)
	}
	openAt(t, path, Options{})
}
