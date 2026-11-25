package ownerjournal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// folderState is every name in the folder of a list with its mode, its
// inode, its times and its bytes, and the folder's own mode and times, so
// that any write, creation, rename or repair is seen.
func folderState(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		v := fmt.Sprint(info.Mode(), " ", st.Ino, " ", st.Nlink, " ", st.Mtim.Nano(), " ", st.Ctim.Nano())
		if info.Mode().IsRegular() {
			v += " " + string(fileBytes(t, p))
		}
		out[p] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameFolderState(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// aList makes a list of two credentials and a raised counter, closed.
func aList(t *testing.T) string {
	t.Helper()
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	a := mustAdd(t, k, 1)
	mustAdd(t, k, 2)
	if err := k.RaiseCounter(a.ID, 4, base); err != nil {
		t.Fatal(err)
	}
	mustClose2(t, k)
	return path
}

// A list and its head that the journal opens are what VerifyKeys passes: it
// returns the file's bytes and its number of records, and the folder is as
// it was, to the inode and the time. An empty list with its head passes
// too.
func TestVerifyKeysPassesTheListTheJournalOpens(t *testing.T) {
	path := aList(t)
	before := folderState(t, filepath.Dir(path))
	got, err := VerifyKeys(path)
	if err != nil {
		t.Fatalf("VerifyKeys: %v", err)
	}
	if !bytes.Equal(got.Data, fileBytes(t, path)) || got.Records != 3 {
		t.Errorf("the snapshot: %d bytes, %d records", len(got.Data), got.Records)
	}
	if !bytes.Equal(got.Head, fileBytes(t, headPathOf(path))) {
		t.Errorf("the snapshot's head %q is not the head file's", got.Head)
	}
	if !sameFolderState(before, folderState(t, filepath.Dir(path))) {
		t.Error("VerifyKeys changed the folder")
	}
	empty := newKeyPath(t)
	mustClose2(t, mustOpenKeys(t, empty))
	if got, err := VerifyKeys(empty); err != nil || got.Records != 0 {
		t.Errorf("an empty list with its head: %+v, %v", got, err)
	}
	for _, bad := range []string{"relative/keys.log", path + "/", filepath.Dir(path) + "/./" + KeysFileName} {
		if _, err := VerifyKeys(bad); err == nil {
			t.Errorf("the path %q was taken", bad)
		}
	}
}

// VerifyKeys refuses what OpenKeys refuses, with the same error, and writes
// nothing: a file of mode 0640, a file of another owner (a stand-in for the
// user the files belong to), a file with a second name, a final symbolic
// link to the list, a folder the group can reach and one that is a final
// link (opened without following it: not a folder), a head of mode 0640,
// with a second name or that is a link, a changed byte, and a head that
// counts more than the list. Where OpenKeys would make or
// repair something, VerifyKeys refuses and makes nothing: no list, no head
// of an empty list, and a head that lags the list by records that chain
// from it (the crash OpenKeys repairs). Each case is checked on the same
// files with VerifyKeys first, then OpenKeys.
func TestVerifyKeysRefusesWhatTheOpenerRefuses(t *testing.T) {
	for _, c := range []struct {
		name string
		// change alters a list made by aList and returns the path to
		// check.
		change func(t *testing.T, path string) string
		// want is the error both refuse with; repair, when set, is the
		// error of VerifyKeys alone, for a list OpenKeys takes and
		// writes to.
		want, repair error
	}{
		{"a file of mode 0640", func(t *testing.T, path string) string {
			must(t, os.Chmod(path, 0o640))
			return path
		}, ErrNotPrivate, nil},
		{"files of another owner", func(t *testing.T, path string) string {
			was := euid
			euid = func() int { return was() + 1 }
			t.Cleanup(func() { euid = was })
			return path
		}, ErrNotPrivate, nil},
		{"a file with a second name", func(t *testing.T, path string) string {
			must(t, os.Link(path, path+".again"))
			return path
		}, ErrNotPrivate, nil},
		{"a final symbolic link to the list", func(t *testing.T, path string) string {
			link := filepath.Join(filepath.Dir(path), "link.log")
			must(t, os.Symlink(path, link))
			return link
		}, ErrNotPrivate, nil},
		{"a folder the group can reach", func(t *testing.T, path string) string {
			must(t, os.Chmod(filepath.Dir(path), 0o770))
			t.Cleanup(func() { os.Chmod(filepath.Dir(path), 0o700) })
			return path
		}, ErrNotPrivate, nil},
		{"a folder that is a symbolic link", func(t *testing.T, path string) string {
			link := filepath.Join(t.TempDir(), "dir")
			must(t, os.Symlink(filepath.Dir(path), link))
			return filepath.Join(link, KeysFileName)
		}, syscall.ENOTDIR, nil},
		{"a head of mode 0640", func(t *testing.T, path string) string {
			must(t, os.Chmod(headPathOf(path), 0o640))
			return path
		}, ErrNotPrivate, nil},
		{"a head with a second name", func(t *testing.T, path string) string {
			must(t, os.Link(headPathOf(path), headPathOf(path)+".again"))
			return path
		}, ErrNotPrivate, nil},
		{"a head that is a symbolic link", func(t *testing.T, path string) string {
			real := headPathOf(path) + ".real"
			must(t, os.Rename(headPathOf(path), real))
			must(t, os.Symlink(real, headPathOf(path)))
			return path
		}, ErrNotPrivate, nil},
		{"a changed byte", func(t *testing.T, path string) string {
			b := fileBytes(t, path)
			b[len(b)-3] ^= 1
			must(t, os.WriteFile(path, b, 0o600))
			return path
		}, ErrDamaged, nil},
		{"a head that counts more than the list", func(t *testing.T, path string) string {
			b := fileBytes(t, path)
			cut := bytes.LastIndexByte(b[:len(b)-1], '\n') + 1
			must(t, os.WriteFile(path, b[:cut], 0o600))
			return path
		}, ErrDamaged, nil},
		{"a list ahead of its head", func(t *testing.T, path string) string {
			head := fileBytes(t, headPathOf(path))
			k := mustOpenKeys(t, path)
			mustAdd(t, k, 3)
			mustClose2(t, k)
			must(t, os.WriteFile(headPathOf(path), head, 0o600))
			return path
		}, nil, ErrHeadBehind},
		{"no head of an empty list", func(t *testing.T, path string) string {
			empty := newKeyPath(t)
			mustClose2(t, mustOpenKeys(t, empty))
			must(t, os.Remove(headPathOf(empty)))
			return empty
		}, nil, ErrDamaged},
		{"no list", func(t *testing.T, path string) string {
			return newKeyPath(t)
		}, nil, ErrNoKeys},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := c.change(t, aList(t))
			dir := filepath.Dir(path)
			if real, err := filepath.EvalSymlinks(dir); err == nil {
				dir = real
			}
			before := folderState(t, dir)
			want := c.want
			if c.repair != nil {
				want = c.repair
			}
			if got, err := VerifyKeys(path); !errors.Is(err, want) {
				t.Fatalf("VerifyKeys: %v (%d bytes), want %v", err, len(got.Data), want)
			}
			if !sameFolderState(before, folderState(t, dir)) {
				t.Fatal("VerifyKeys changed the folder")
			}
			k, err := OpenKeys(path)
			if c.repair != nil {
				if err != nil {
					t.Fatalf("OpenKeys refuses what it would make or repair: %v", err)
				}
				k.Close()
				if sameFolderState(before, folderState(t, dir)) {
					t.Fatal("OpenKeys wrote nothing: the case is not one it makes or repairs")
				}
				return
			}
			if !errors.Is(err, c.want) {
				if err == nil {
					k.Close()
				}
				t.Fatalf("OpenKeys: %v, want %v as VerifyKeys", err, c.want)
			}
		})
	}
}

// A FIFO in place of the list or of its head is refused at once, as a file
// that is not a plain file, and never blocks the check: both are opened
// nonblocking and checked before they are read.
func TestVerifyKeysRefusesAFIFOWithoutBlocking(t *testing.T) {
	for _, which := range []string{"the list", "the head"} {
		t.Run(which, func(t *testing.T) {
			path := aList(t)
			target := path
			if which == "the head" {
				target = headPathOf(path)
			}
			must(t, os.Remove(target))
			must(t, syscall.Mkfifo(target, 0o600))
			done := make(chan error, 1)
			go func() {
				_, err := VerifyKeys(path)
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, ErrNotPrivate) {
					t.Fatalf("VerifyKeys with a FIFO as %s: %v, want %v", which, err, ErrNotPrivate)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("VerifyKeys blocked on a FIFO as %s", which)
			}
		})
	}
}

// A record appended while the check reads is never passed as the list's: the
// head that lags is refused, and a check run again afterwards passes the
// whole list.
func TestVerifyKeysAfterAnAppendReadsTheWholeList(t *testing.T) {
	path := aList(t)
	first, err := VerifyKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	k := mustOpenKeys(t, path)
	mustAdd(t, k, 3)
	mustClose2(t, k)
	again, err := VerifyKeys(path)
	if err != nil || again.Records != first.Records+1 || !bytes.HasPrefix(again.Data, first.Data) {
		t.Fatalf("after the append: %d records, %v", again.Records, err)
	}
	if !strings.HasSuffix(string(again.Data), "\n") {
		t.Fatal("the snapshot does not end with a whole line")
	}
}

// withStat is a file's FileInfo with another stat in place of its own.
type withStat struct {
	os.FileInfo
	st *syscall.Stat_t
}

func (w withStat) Sys() any { return w.st }

// The owner predicate of the list and of the head, on its own (review
// evidence item): with the folder the user's own, a list or a head whose
// stat names another owner is refused, each by the check VerifyKeys makes of
// it (keysFileProblem for the list, privateFile for the head), while the
// same stat with the user's own UID passes. The UID is changed in the stat
// alone, so the folder's owner stays the right one and only the file's is
// wrong; the test of "files of another owner" above changes the user for
// every object and is refused at the folder.
func TestVerifyKeysRefusesAListOrAHeadOfAnotherOwner(t *testing.T) {
	path := aList(t)
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || !private(dir) {
		t.Fatalf("the folder is not the user's own: %v", err)
	}
	for _, c := range []struct {
		name, file string
		passes     func(os.FileInfo) bool
	}{
		{"the list", path, func(info os.FileInfo) bool {
			err := keysFileProblem(info)
			if err != nil && !errors.Is(err, ErrNotPrivate) {
				t.Errorf("the list's check: %v, want %v", err, ErrNotPrivate)
			}
			return err == nil
		}},
		{"the head", headPathOf(path), privateFile},
	} {
		info, err := os.Lstat(c.file)
		if err != nil {
			t.Fatal(err)
		}
		if !c.passes(info) {
			t.Fatalf("%s of the user's own is refused", c.name)
		}
		st := *info.Sys().(*syscall.Stat_t)
		st.Uid = uint32(euid()) + 1
		if c.passes(withStat{info, &st}) {
			t.Errorf("%s of another owner passes, in a folder of the user's own", c.name)
		}
	}
}

// A record appended between the read of the head and the last look at the
// list (review evidence item): the hook appends one through the journal's
// own opener at that point, and VerifyKeys refuses the list as one that
// changed while it was checked, returning nothing of it; run again, it
// passes the whole list.
func TestVerifyKeysRefusesAnAppendBetweenTheHeadAndTheLastLook(t *testing.T) {
	path := aList(t)
	calls := 0
	verifyKeysAfterHead = func() {
		calls++
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 3)
		mustClose2(t, k)
	}
	t.Cleanup(func() { verifyKeysAfterHead = nil })
	snap, err := VerifyKeys(path)
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "changed while it was checked") || snap.Data != nil || snap.Head != nil {
		t.Fatalf("an append after the head was read: %d call(s), %d bytes, %v", calls, len(snap.Data), err)
	}
	verifyKeysAfterHead = nil
	again, err := VerifyKeys(path)
	if err != nil || again.Records != 4 || !bytes.Equal(again.Head, fileBytes(t, headPathOf(path))) {
		t.Fatalf("after the append: %d records, %v", again.Records, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
