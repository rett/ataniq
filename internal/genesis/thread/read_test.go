package thread

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/genesis/plan"
)

// entryState is what of a folder entry a write would change.
type entryState struct {
	ino   uint64
	mode  os.FileMode
	size  int64
	mtime time.Time
	ctime syscall.Timespec
	data  string
}

// snapshot takes the state of the folder itself and of every entry in it.
func snapshot(t *testing.T, dir string) map[string]entryState {
	t.Helper()
	out := map[string]entryState{}
	take := func(name, path string) {
		fi, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("no stat of the system")
		}
		e := entryState{ino: st.Ino, mode: fi.Mode(), size: fi.Size(), mtime: fi.ModTime(), ctime: st.Ctim}
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err == nil {
				e.data = string(b)
			}
		}
		out[name] = e
	}
	take(".", dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		take(e.Name(), filepath.Join(dir, e.Name()))
	}
	return out
}

func sameSnapshot(t *testing.T, before, after map[string]entryState) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("the folder had %d entries and has %d", len(before), len(after))
	}
	for name, b := range before {
		a, ok := after[name]
		if !ok {
			t.Fatalf("%s is gone", name)
		}
		if a != b {
			t.Fatalf("%s changed: %+v, then %+v", name, b, a)
		}
	}
}

// finishedThread walks the web app path and finishes it: a thread with a
// stored plan.
func (w *world) finishedThread() string {
	w.t.Helper()
	id := w.ok(w.s.Start(ctx)).Thread
	w.interview(id, 0)
	w.ok(w.s.Finish(ctx, id))
	return id
}

// The reader lists exactly the finished threads that hold a plan, with their
// tag, kind, plan digest and finish time, oldest first; an open, a discarded
// and an early-finished thread are not listed. It finds the plan the store
// stored, byte for byte.
func TestReadFinishedListsFinishedThreadsWithAPlan(t *testing.T) {
	w := newWorld(t)
	first := w.finishedThread()
	finishedAt := w.clock.now()
	w.clock.advance(time.Hour)
	early := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Finish(ctx, early))
	dropped := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Discard(ctx, dropped))
	second := w.finishedThread()
	open := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Answer(ctx, open, 1, webApp[0]))

	got, warnings, err := ReadFinished(w.dir)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("ReadFinished: %v %q", err, warnings)
	}
	if len(got) != 2 || got[0].ID != first || got[1].ID != second {
		t.Fatalf("listed %+v, want %s then %s", got, first, second)
	}
	for _, s := range got {
		r := w.s.threads[s.ID]
		if s.Tag != s.ID[:6] || s.Kind != "software" || s.Revision != 1 || s.PlanDigest != r.planDigest || s.PlanShort != r.planDigest[:12] || !s.Finished.Equal(r.updated) {
			t.Fatalf("summary %+v of %+v", s, r)
		}
	}
	if !got[0].Finished.Equal(finishedAt.UTC().Truncate(time.Second)) {
		t.Fatalf("finished %v, want %v", got[0].Finished, finishedAt)
	}
	p, id, err := ReadPlan(w.dir, first)
	if err != nil || id != first {
		t.Fatalf("ReadPlan: %v %s", err, id)
	}
	want, err := plan.Encode(*w.s.threads[first].plan)
	if err != nil {
		t.Fatal(err)
	}
	if gotBytes, err := plan.Encode(p); err != nil || !bytes.Equal(gotBytes, want) {
		t.Fatalf("the plan read is not the plan stored: %v", err)
	}
	for _, notListed := range []string{early, dropped, open} {
		if _, _, err := ReadPlan(w.dir, notListed); !isTag(err, TagNone) {
			t.Fatalf("%s: %v", notListed, err)
		}
	}
}

func isTag(err error, reason string) bool {
	var te *TagError
	return errors.As(err, &te) && te.Reason == reason
}

// A tag of 6 or more digits names one thread; a tag that two share is
// ambiguous, one that none has is none, and one that is not a tag is refused,
// each in fixed text that holds nothing of the tag.
func TestReadPlanTags(t *testing.T) {
	w := newWorld(t)
	// The ids of this world count up from 0000000000000001: every id shares
	// its first 15 digits.
	a := w.finishedThread()
	b := w.finishedThread()
	if a[:15] != b[:15] {
		t.Fatalf("the ids %s and %s do not share a prefix", a, b)
	}
	for _, id := range []string{a, b} {
		if _, got, err := ReadPlan(w.dir, id); err != nil || got != id {
			t.Fatalf("%s: %v %s", id, err, got)
		}
	}
	for tag, reason := range map[string]string{
		a[:6]:               TagAmbiguous,
		a[:15]:              TagAmbiguous,
		"ffffff":            TagNone,
		"ffffffffffffffff":  TagNone,
		"00000":             TagBad,
		"":                  TagBad,
		"0000000000000000f": TagBad,
		"ABCDEF":            TagBad,
		"00000g":            TagBad,
		"../000000":         TagBad,
		"000000 ":           TagBad,
	} {
		_, _, err := ReadPlan(w.dir, tag)
		if !isTag(err, reason) {
			t.Errorf("%q: %v, want %s", tag, err, reason)
			continue
		}
		if tag != "" && strings.Contains(err.Error(), tag) {
			t.Errorf("%q: the error quotes the tag", tag)
		}
	}
}

// A file that does not pass the store's checks is skipped with the fixed line
// the store would log, never served, and nothing of its content is in the
// line; a file that is not a thread's is not read at all.
func TestReadFinishedSkipsFilesThatDoNotPass(t *testing.T) {
	w := newWorld(t)
	good := w.finishedThread()
	edited := w.finishedThread()
	w.s.Close()
	secret := "HOSTILE-CONTENT-" + strings.Repeat("x", 8)
	write := func(name string, data []byte, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(w.dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	// The plan of one finished thread was edited by hand.
	data := w.bytesOf(edited)
	changed := bytes.Replace(data, []byte(`"enforcement":"advisory"`), []byte(`"enforcement":"enforced"`), 1)
	if bytes.Equal(changed, data) {
		t.Fatal("the file holds no budget band")
	}
	write(fileOf(edited), changed, 0o600)
	write("1111111111111111.json", []byte(`{"secret":"`+secret+`"}`), 0o600)
	write("2222222222222222.json", w.bytesOf(good), 0o600)
	write("3333333333333333.json", w.bytesOf(good), 0o644)
	if err := os.Symlink(filepath.Join(w.dir, fileOf(good)), filepath.Join(w.dir, "4444444444444444.json")); err != nil {
		t.Fatal(err)
	}
	write(tmpOf("5555555555555555"), []byte(secret), 0o600)
	write("notes.txt", []byte(secret), 0o600)

	before := snapshot(t, w.dir)
	got, warnings, err := ReadFinished(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != good {
		t.Fatalf("listed %+v, want %s only", got, good)
	}
	sort.Strings(warnings)
	want := []string{
		"genesis: the thread file 1111111111111111.json is not served: thread has a field the form does not name",
		"genesis: the thread file 2222222222222222.json is not served: the id is not the file's name",
		"genesis: the thread file 3333333333333333.json is not served: the file is open to group or other",
		"genesis: the thread file 4444444444444444.json is not served: the file is not a regular file",
		"genesis: the thread file " + fileOf(edited) + " is not served: the plan does not pass its own checks",
	}
	sort.Strings(want)
	if len(warnings) != len(want) {
		t.Fatalf("warnings %q", warnings)
	}
	for i := range want {
		if !strings.HasPrefix(warnings[i], want[i]) {
			t.Errorf("warning %q, want %q", warnings[i], want[i])
		}
		if strings.Contains(warnings[i], secret) || strings.Contains(warnings[i], w.dir) {
			t.Errorf("the warning %q holds content or the path", warnings[i])
		}
	}
	for _, tag := range []string{edited, "1111111111111111", "2222222222222222", "3333333333333333", "4444444444444444", "5555555555555555"} {
		if _, _, err := ReadPlan(w.dir, tag); !isTag(err, TagNone) {
			t.Errorf("%s: %v, want none", tag, err)
		}
	}
	sameSnapshot(t, before, snapshot(t, w.dir))
}

// The reader writes nothing: no file and the folder itself keep their inode,
// mode, size, times and content, and the folder gains and loses no entry,
// whatever it is asked; it may run while a store is open on the folder, and
// the store goes on after it. A folder that is not there is not made.
func TestTheReaderWritesNothing(t *testing.T) {
	w := newWorld(t)
	id := w.finishedThread()
	w.finishedThread()
	open := w.ok(w.s.Start(ctx)).Thread
	// An open thread that is past its expiry is not discarded by the reader,
	// as Open would.
	w.clock.advance(Expiry + time.Hour)
	before := snapshot(t, w.dir)
	for i := 0; i < 3; i++ {
		if _, _, err := ReadFinished(w.dir); err != nil {
			t.Fatal(err)
		}
		for _, tag := range []string{id, id[:6], "ffffff", "bad", open} {
			_, _, _ = ReadPlan(w.dir, tag)
		}
	}
	sameSnapshot(t, before, snapshot(t, w.dir))
	// The store, open all along, still serves.
	w.ok(w.s.Plan(ctx, id))

	missing := filepath.Join(t.TempDir(), "no-such-folder")
	got, warnings, err := ReadFinished(missing)
	if err != nil || len(got) != 0 || len(warnings) != 0 {
		t.Fatalf("a missing folder: %v %v %q", got, err, warnings)
	}
	if _, _, err := ReadPlan(missing, "000000"); !isTag(err, TagNone) {
		t.Fatalf("a missing folder: %v", err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the folder was made: %v", err)
	}
}

// A folder the store would refuse, the reader refuses too, with the store's
// error and reason word, and reads nothing in it.
func TestTheReaderRefusesAFolderTheStoreRefuses(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(base, "full")
	if err := os.Mkdir(full, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxEntries; i++ {
		if err := os.WriteFile(filepath.Join(full, strings.Repeat("a", 3)+string(rune('a'+i%26))+string(rune('a'+i/26))), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for dir, reason := range map[string]string{link: "folder-link", open: "folder-mode", file: "folder-not-folder", full: "too-many-files", "": "no-folder"} {
		_, _, err := ReadFinished(dir)
		var oe *OpenError
		if !errors.As(err, &oe) || oe.Reason != reason {
			t.Errorf("%s: %v, want %s", filepath.Base(dir), err, reason)
			continue
		}
		if dir != "" && strings.Contains(err.Error(), dir) {
			t.Errorf("the error holds the path: %v", err)
		}
		if _, _, err := ReadPlan(dir, "000000"); !errors.As(err, &oe) || oe.Reason != reason {
			t.Errorf("ReadPlan %s: %v", filepath.Base(dir), err)
		}
	}
}
