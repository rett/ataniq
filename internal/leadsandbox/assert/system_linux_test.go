//go:build linux

package assert

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The real System, on files under t.TempDir() and on the process's own
// environment, for the file and environment checks only.

func TestHostSystemFiles(t *testing.T) {
	dir := t.TempDir()
	h := HostSystem{}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := h.Lstat(file)
	if err != nil || !info.IsRegular() || info.Size != 5 || info.Describe() != "a regular file" {
		t.Errorf("Lstat file: %+v %v", info, err)
	}
	if info, err := h.Lstat(filepath.Join(dir, "sub")); err != nil || !info.IsDir() {
		t.Errorf("Lstat dir: %+v %v", info, err)
	}
	if info, err := h.Lstat(filepath.Join(dir, "link")); err != nil || !info.IsSymlink() || info.IsRegular() {
		t.Errorf("Lstat link: %+v %v", info, err)
	}
	if info, err := h.Lstat(filepath.Join(dir, "fifo")); err != nil || info.IsRegular() || info.Describe() != "a special file" {
		t.Errorf("Lstat fifo: %+v %v", info, err)
	}
	if _, err := h.Lstat(filepath.Join(dir, "none")); !absent(err) {
		t.Errorf("Lstat missing: %v", err)
	}
	if _, err := h.Lstat(filepath.Join(file, "below")); !absent(err) {
		t.Errorf("Lstat below a file: %v", err)
	}

	if data, err := h.ReadFile(file, 5); err != nil || string(data) != "hello" {
		t.Errorf("ReadFile at the limit: %q %v", data, err)
	}
	if _, err := h.ReadFile(file, 4); !errors.Is(err, ErrTooLarge) {
		t.Errorf("ReadFile over the limit: %v", err)
	}
	if _, err := h.ReadFile(file, 0); err == nil {
		t.Error("ReadFile with no limit")
	}
	if _, err := h.ReadFile(filepath.Join(dir, "link"), 100); !errors.Is(err, ErrNotRegular) {
		t.Errorf("ReadFile of a link: %v", err)
	}
	if _, err := h.ReadFile(filepath.Join(dir, "sub"), 100); !errors.Is(err, ErrNotRegular) {
		t.Errorf("ReadFile of a folder: %v", err)
	}
	// A FIFO with no writer must not block.
	done := make(chan error, 1)
	go func() { _, err := h.ReadFile(filepath.Join(dir, "fifo"), 100); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Errorf("ReadFile of a FIFO: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadFile of a FIFO blocked")
	}
	if _, err := h.ReadFile(filepath.Join(dir, "none"), 100); !absent(err) {
		t.Errorf("ReadFile of a missing file: %v", err)
	}
}

func TestHostSystemEnvironAndWritable(t *testing.T) {
	t.Setenv("ASSERT_TEST_VAR", "value")
	found := false
	for _, e := range (HostSystem{}).Environ() {
		found = found || e == "ASSERT_TEST_VAR=value"
	}
	if !found {
		t.Error("Environ does not hold a variable the test set")
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if w, err := (HostSystem{}).Writable(file); err != nil || !w {
		t.Errorf("Writable of an own file: %v %v", w, err)
	}
	if err := os.Chmod(file, 0o400); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 { // root writes to a mode 0400 file
		if w, err := (HostSystem{}).Writable(file); err != nil || w {
			t.Errorf("Writable of a read-only file: %v %v", w, err)
		}
	}
	if _, err := (HostSystem{}).Writable(filepath.Join(dir, "none")); !absent(err) {
		t.Errorf("Writable of a missing file: %v", err)
	}
	// The check did not write.
	if fi, err := os.Stat(file); err != nil || fi.Size() != 0 {
		t.Errorf("the file changed: %v %v", fi, err)
	}
}

func TestHostSystemMounts(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "mountinfo")
	if err := os.WriteFile(fixture, []byte(sampleMountInfo), 0o600); err != nil {
		t.Fatal(err)
	}
	ms, err := (HostSystem{MountInfoPath: fixture}).Mounts()
	if err != nil || len(ms) != 5 {
		t.Errorf("fixture: %d mounts, %v", len(ms), err)
	}
	if err := os.WriteFile(fixture, []byte(strings.Repeat("x", MaxMountTableBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (HostSystem{MountInfoPath: fixture}).Mounts(); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized table: %v", err)
	}
	// The process's own table parses and has a root mount.
	ms, err = (HostSystem{}).Mounts()
	if err != nil {
		t.Skipf("no readable mount table here: %v", err)
	}
	if id, err := (HostSystem{}).MountID("/"); err != nil {
		t.Logf("no mount ID of /: %v", err)
	} else if _, ok := mountByID(ms, id); !ok {
		t.Errorf("mount ID %d of / is not in the %d mounts", id, len(ms))
	}
}

// statx and the mount table agree: the mount ID of a path under the process's
// temporary folder is the ID of a mount of the table whose point is a parent.
func TestHostSystemMountID(t *testing.T) {
	h := HostSystem{}
	dir := t.TempDir()
	id, err := h.MountID(dir)
	if errors.Is(err, ErrNoMountID) {
		t.Skipf("the kernel gives no mount ID: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	ms, err := h.Mounts()
	if err != nil {
		t.Skipf("no readable mount table here: %v", err)
	}
	m, ok := mountByID(ms, id)
	if !ok {
		t.Fatalf("mount ID %d of %s is not in the table", id, dir)
	}
	if !within(dir, m.Point) {
		t.Errorf("%s is on mount %d at %s, which is not above it", dir, id, m.Point)
	}
	// A file of the same folder is on the same mount.
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if id2, err := h.MountID(f); err != nil || id2 != id {
		t.Errorf("file: %d %v, want %d", id2, err, id)
	}
	if _, err := h.MountID(filepath.Join(dir, "none")); !absent(err) {
		t.Errorf("a missing path: %v", err)
	}
}

func TestHostSystemDial(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := HostSystem{}
	if err := h.Dial("tcp", l.Addr().String(), time.Second); err != nil {
		t.Errorf("dial a listener: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	err = h.Dial("tcp", addr, time.Second)
	if _, proves := dialEvidence(err); err == nil || !proves {
		t.Errorf("dial a closed port: %v", err)
	}
	if err := h.Dial("udp", addr, time.Second); err == nil {
		t.Error("dial of udp allowed")
	}
	if err := h.Dial("unix", "@ataniq-no-such-socket-for-test", time.Second); err == nil {
		t.Error("dial a missing abstract socket connected")
	} else if _, proves := dialEvidence(err); !proves {
		t.Errorf("dial a missing abstract socket: %v", err)
	}
}

// A listener whose backlog is full fails a unix connect with EAGAIN, which
// net.Error calls a timeout. A listener is there: that is not evidence that
// the socket is unreachable, and assertion 3 is not-checked, not ok.
func TestFullBacklogIsNotUnreachable(t *testing.T) {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Skipf("no unix socket here: %v", err)
	}
	defer unix.Close(fd)
	name := "ataniq-test-backlog-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: "@" + name}); err != nil {
		t.Skipf("no abstract socket here: %v", err)
	}
	if err := unix.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}
	h := HostSystem{}
	var last error
	for try := 0; try < 8 && last == nil; try++ {
		last = h.Dial("unix", "@"+name, time.Second)
	}
	if last == nil {
		t.Skip("the backlog never filled up on this kernel")
	}
	if !errors.Is(last, syscall.EAGAIN) {
		t.Skipf("a full backlog gave %v, not EAGAIN", last)
	}
	if how, proves := dialEvidence(last); proves {
		t.Errorf("EAGAIN counted as evidence of unreachability: %q", how)
	}
	s := testSpec("claude")
	s.AbstractSocket = name
	s.LoopbackTCP = "127.0.0.1:1"
	r := (&checker{spec: s, sys: HostSystem{}}).loopback()
	if r.Status == StatusOK {
		t.Errorf("a reachable socket with a full backlog passed assertion 3: %+v", r)
	}
	if r.Status != StatusNotChecked || !strings.Contains(r.Detail, "does not show it is unreachable") {
		t.Errorf("want not-checked for the full backlog: %+v", r)
	}
}

func TestHostSystemLookupHost(t *testing.T) {
	addrs, err := (HostSystem{}).LookupHost("localhost", 5*time.Second)
	if err != nil {
		t.Skipf("localhost does not resolve here: %v", err)
	}
	if len(addrs) == 0 {
		t.Error("no address")
	}
}

// The inside checks against real files: the environment and a managed
// settings file in a temporary folder.
func TestHostSystemWithChecks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed.json")
	if err := os.WriteFile(path, []byte(`{"hook":"example-guard-hook"}`), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASSERT_ONLY", "1")
	s := testSpec("claude")
	s.ManagedSettings = path
	// The managed path of a claude spec is fixed, so check with the checker.
	c := &checker{spec: s, sys: HostSystem{}}
	var tl tally
	c.mustBeAbsent(&tl, "x", filepath.Join(dir, "none"))
	if len(tl.failed)+len(tl.unchecked) != 0 {
		t.Errorf("mustBeAbsent of a missing path: %+v", tl)
	}
	c.mustBeAbsent(&tl, "x", path)
	if len(tl.failed) != 1 {
		t.Errorf("mustBeAbsent of a present path: %+v", tl)
	}
	// The environment check, with the real environment as the allowlist.
	s.Env = nil
	for _, e := range os.Environ() {
		name, value, _ := strings.Cut(e, "=")
		if validEnvName(name) && len(value) <= MaxEnvValueBytes && !hasControl(value) && len(s.Env) < MaxEnvEntries {
			s.Env = append(s.Env, EnvEntry{name, value})
		}
	}
	c = &checker{spec: s, sys: HostSystem{}}
	if r := c.environment(); r.Status != StatusOK && len(os.Environ()) == len(s.Env) {
		t.Errorf("the real environment against itself: %+v", r)
	}
	s.Env = append(s.Env, EnvEntry{"ASSERT_NOT_SET", "x"})
	c = &checker{spec: s, sys: HostSystem{}}
	if r := c.environment(); r.Status != StatusFailed || !strings.Contains(r.Detail, "ASSERT_NOT_SET") {
		t.Errorf("a missing variable: %+v", r)
	}
}

func TestHostSystemDirEmpty(t *testing.T) {
	dir := t.TempDir()
	h := HostSystem{}
	if empty, err := h.DirEmpty(dir); err != nil || !empty {
		t.Errorf("an empty folder: %v %v", empty, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if empty, err := h.DirEmpty(dir); err != nil || empty {
		t.Errorf("a folder with a file: %v %v", empty, err)
	}
	if _, err := h.DirEmpty(filepath.Join(dir, "f")); err == nil {
		t.Error("a file was read as a folder")
	}
	if _, err := h.DirEmpty(filepath.Join(dir, "none")); !absent(err) {
		t.Errorf("a missing folder: %v", err)
	}
	// A link to a folder is not followed.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DirEmpty(link); err == nil {
		t.Error("DirEmpty followed a link")
	}
}

func TestHostSystemDirNames(t *testing.T) {
	dir := t.TempDir()
	h := HostSystem{}
	if names, err := h.DirNames(dir, 4); err != nil || len(names) != 0 {
		t.Errorf("an empty folder: %v %v", names, err)
	}
	for _, name := range []string{"b", "a", "c"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Every entry, sorted, a hidden one and a file included.
	names, err := h.DirNames(dir, 4)
	if err != nil || strings.Join(names, ",") != ".hidden,a,b,c" {
		t.Errorf("four entries with the limit 4: %v %v", names, err)
	}
	// One more than the limit is refused whole, not cut short.
	names, err = h.DirNames(dir, 3)
	if !errors.Is(err, ErrTooLarge) || len(names) != 0 {
		t.Errorf("four entries with the limit 3: %v %v", names, err)
	}
	if _, err := h.DirNames(dir, 0); err == nil {
		t.Error("a limit of 0 was accepted")
	}
	if _, err := h.DirNames(filepath.Join(dir, ".hidden"), 4); !absent(err) {
		t.Errorf("a file read as a folder: %v", err)
	}
	if _, err := h.DirNames(filepath.Join(dir, "none"), 4); !absent(err) {
		t.Errorf("a missing folder: %v", err)
	}
	// A link to a folder is not followed.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if names, err := h.DirNames(link, 4); err == nil {
		t.Errorf("DirNames followed a link: %v", names)
	}
}

// The holder's list of the coordinators' folders, and their identities, on
// real folders.
func TestCoordinatorFoldersOnTheHost(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := profileInputs("claude")
	in.StateRoot = state
	l := in.Layout()
	for _, d := range []string{
		l.CoordsDir() + "/ataniq/feature-a/tree", l.CoordsDir() + "/ataniq/feature-a/home",
		l.CoordsDir() + "/ataniq/feature-b/tree",
		l.CoordLoginDir() + "/ataniq/claude",
	} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	h := HostSystem{}
	folders, err := CoordinatorFolders(in, h.DirNames)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		l.CoordsDir() + "/ataniq/feature-a", l.CoordsDir() + "/ataniq/feature-a/tree", l.CoordsDir() + "/ataniq/feature-a/home",
		l.CoordsDir() + "/ataniq/feature-b", l.CoordsDir() + "/ataniq/feature-b/tree", l.CoordsDir() + "/ataniq/feature-b/home",
		l.CoordLoginDir() + "/ataniq/claude",
	}
	if strings.Join(folders, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %v, want %v", folders, want)
	}
	ids, err := CollectIdentities(append(CoordinatorIdentityPaths(in, HostFacts{}), folders...), h.Identity)
	if err != nil {
		t.Fatal(err)
	}
	have := map[FileIdentity]bool{}
	for _, id := range ids {
		have[id] = true
	}
	for _, p := range []string{l.CoordsDir(), l.CoordLoginDir(), state, want[1], want[2], want[4], want[6]} {
		id, err := h.Identity(p)
		if err != nil {
			t.Fatal(err)
		}
		if !have[id] {
			t.Errorf("the identity of %s is not in the list", p)
		}
	}
	// A link in the place of a project folder is not a folder and is not
	// followed: what it points to is not listed a second time under its name.
	if err := os.Symlink(l.CoordsDir()+"/ataniq", l.CoordsDir()+"/linked"); err != nil {
		t.Fatal(err)
	}
	again, err := CoordinatorFolders(in, h.DirNames)
	if err != nil || strings.Join(again, "\n") != strings.Join(want, "\n") {
		t.Errorf("with a link beside the project folder: %v %v", again, err)
	}
}

func TestHostSystemIdentity(t *testing.T) {
	dir := t.TempDir()
	h := HostSystem{}
	file, other, link, hard := dir+"/file", dir+"/other", dir+"/link", dir+"/hard"
	for _, p := range []string{file, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, hard); err != nil {
		t.Fatal(err)
	}
	a, err := h.Identity(file)
	if err != nil || a.Inode == 0 {
		t.Fatalf("Identity: %v %v", a, err)
	}
	var st unix.Stat_t
	if err := unix.Stat(file, &st); err != nil || a != (FileIdentity{Device: uint64(st.Dev), Inode: st.Ino}) {
		t.Errorf("identity %v is not the device and inode of stat: %+v %v", a, st, err)
	}
	if b, err := h.Identity(hard); err != nil || b != a {
		t.Errorf("one file under two names: %v %v, want %v", b, err, a)
	}
	if b, err := h.Identity(other); err != nil || b == a {
		t.Errorf("another file: %v %v", b, err)
	}
	// A link at the end is not followed.
	if b, err := h.Identity(link); err != nil || b == a {
		t.Errorf("the link has the identity of its target: %v %v", b, err)
	}
	if d1, err := h.Identity(dir); err != nil || d1 == a || d1.Device != a.Device {
		t.Errorf("the folder: %v %v", d1, err)
	}
	if _, err := h.Identity(dir + "/none"); !absent(err) {
		t.Errorf("a missing path: %v", err)
	}
	if _, err := h.Identity(file + "/below"); !absent(err) {
		t.Errorf("below a file: %v", err)
	}
	if s := a.String(); !strings.Contains(s, ":") {
		t.Errorf("String %q", s)
	}
}

// A stat that reads the real file system, through the System the probe uses:
// the same pair for one file under two names, and not for a link or a sibling.
func TestCollectIdentitiesAndHostIdentity(t *testing.T) {
	dir := t.TempDir()
	h := HostSystem{}
	got, err := CollectIdentities([]string{dir + "/none", dir}, h.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("the folder and its parents: %v", got)
	}
	self, err := h.Identity(dir)
	if err != nil || got[0] != self {
		t.Errorf("first identity %v, want the folder's %v (%v)", got[0], self, err)
	}
	if root, err := h.Identity("/"); err == nil {
		for _, id := range got {
			if id == root && self != root {
				t.Error("the root's identity was collected")
			}
		}
	}
}
