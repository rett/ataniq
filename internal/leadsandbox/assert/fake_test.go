package assert

import (
	"fmt"
	"hash/fnv"
	"io/fs"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeNode struct {
	info FileInfo
	data []byte
}

// fakeSys is the fake System: every answer is data the test sets.
type fakeSys struct {
	nodes     map[string]fakeNode
	lstatErr  map[string]error
	readErr   map[string]error
	mounts    []Mount
	mountsErr error
	env       []string
	dialErr   map[string]error // "network address" -> error; nil value means connected
	hosts     map[string][]string
	lookupErr error
	writable  map[string]bool
	accessErr map[string]error
	dirErr    map[string]error
	// namesErr sets the error of DirNames for a path.
	namesErr map[string]error
	// mountIDs sets the answer of MountID for a path; otherwise it is the ID
	// of the mount of the longest point above the path, the last of several on
	// top. noMountID makes every answer ErrNoMountID.
	mountIDs  map[string]int
	noMountID bool
	// identities sets the answer of Identity for a path; any other path has
	// the identity of device 4242 and an inode made of its text, so no two
	// paths share one and none is in the spec's forbidden list.
	identities   map[string]FileIdentity
	identityErr  map[string]error
	identityRead []string

	dialed []string
}

func (f *fakeSys) Lstat(path string) (FileInfo, error) {
	if err := f.lstatErr[path]; err != nil {
		return FileInfo{}, err
	}
	n, ok := f.nodes[path]
	if !ok {
		return FileInfo{}, &fs.PathError{Op: "lstat", Path: path, Err: syscall.ENOENT}
	}
	return n.info, nil
}

func (f *fakeSys) ReadFile(path string, limit int) ([]byte, error) {
	if err := f.readErr[path]; err != nil {
		return nil, err
	}
	n, ok := f.nodes[path]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: path, Err: syscall.ENOENT}
	}
	if !n.info.IsRegular() {
		return nil, fmt.Errorf("read %s: %w", path, ErrNotRegular)
	}
	if len(n.data) > limit {
		return nil, fmt.Errorf("read %s: %w", path, ErrTooLarge)
	}
	return n.data, nil
}

func (f *fakeSys) Mounts() ([]Mount, error) { return f.mounts, f.mountsErr }
func (f *fakeSys) Environ() []string        { return f.env }

func (f *fakeSys) Dial(network, address string, _ time.Duration) error {
	key := network + " " + address
	f.dialed = append(f.dialed, key)
	if err, ok := f.dialErr[key]; ok {
		return err
	}
	return syscall.ECONNREFUSED
}

func (f *fakeSys) LookupHost(host string, _ time.Duration) ([]string, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	return f.hosts[host], nil
}

// DirEmpty is true for a folder that no other node lies below.
func (f *fakeSys) DirEmpty(path string) (bool, error) {
	if err := f.dirErr[path]; err != nil {
		return false, err
	}
	n, ok := f.nodes[path]
	if !ok {
		return false, &fs.PathError{Op: "open", Path: path, Err: syscall.ENOENT}
	}
	if !n.info.IsDir() {
		return false, &fs.PathError{Op: "open", Path: path, Err: syscall.ENOTDIR}
	}
	for p := range f.nodes {
		if strings.HasPrefix(p, path+"/") {
			return false, nil
		}
	}
	return true, nil
}

// DirNames is the names of what lies directly in the folder, by the nodes.
func (f *fakeSys) DirNames(path string, limit int) ([]string, error) {
	if err := f.namesErr[path]; err != nil {
		return nil, err
	}
	n, ok := f.nodes[path]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: path, Err: syscall.ENOENT}
	}
	if !n.info.IsDir() {
		return nil, &fs.PathError{Op: "open", Path: path, Err: syscall.ENOTDIR}
	}
	seen := map[string]bool{}
	var names []string
	for p := range f.nodes {
		rest, ok := strings.CutPrefix(p, path+"/")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, "/")
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) > limit {
		return nil, fmt.Errorf("list %s: %w", path, ErrTooLarge)
	}
	sort.Strings(names)
	return names, nil
}

func (f *fakeSys) MountID(path string) (int, error) {
	if f.noMountID {
		return 0, fmt.Errorf("statx %s: %w", path, ErrNoMountID)
	}
	if id, ok := f.mountIDs[path]; ok {
		return id, nil
	}
	best, found := Mount{}, false
	for _, m := range f.mounts {
		if within(path, m.Point) && (!found || len(m.Point) >= len(best.Point)) {
			best, found = m, true
		}
	}
	if !found {
		return 0, &fs.PathError{Op: "statx", Path: path, Err: syscall.ENOENT}
	}
	return best.ID, nil
}

func (f *fakeSys) Identity(path string) (FileIdentity, error) {
	f.identityRead = append(f.identityRead, path)
	if err := f.identityErr[path]; err != nil {
		return FileIdentity{}, err
	}
	if id, ok := f.identities[path]; ok {
		return id, nil
	}
	h := fnv.New64a()
	h.Write([]byte(path))
	return FileIdentity{Device: 4242, Inode: h.Sum64() | 1<<40}, nil
}

func (f *fakeSys) Writable(path string) (bool, error) {
	if err := f.accessErr[path]; err != nil {
		return false, err
	}
	return f.writable[path], nil
}

func (f *fakeSys) setFile(path string, data string) {
	f.nodes[path] = fakeNode{info: FileInfo{Mode: 0o644, Size: int64(len(data))}, data: []byte(data)}
	delete(f.lstatErr, path)
}

func (f *fakeSys) setDir(path string) {
	f.nodes[path] = fakeNode{info: FileInfo{Mode: fs.ModeDir | 0o755}}
}

func (f *fakeSys) setMount(point string, ro bool) {
	opts := "rw,nosuid,nodev,relatime"
	if ro {
		opts = "ro,nosuid,nodev,relatime"
	}
	f.mounts = append(f.mounts, Mount{ID: 100 + len(f.mounts), Root: "/", Point: point, Options: opts, FSType: "tmpfs", Source: "tmpfs", SuperOptions: "rw"})
}

// goodSystem is a fake that passes every inside check of the spec.
func goodSystem(s Spec) *fakeSys {
	f := &fakeSys{
		nodes:     map[string]fakeNode{},
		lstatErr:  map[string]error{},
		readErr:   map[string]error{},
		dialErr:   map[string]error{},
		hosts:     map[string][]string{s.ProviderHost: {"192.0.2.10", "2001:db8::10"}},
		writable:  map[string]bool{},
		accessErr: map[string]error{},
		dirErr:    map[string]error{},
		namesErr:  map[string]error{},
		mountIDs:  map[string]int{},

		identities:  map[string]FileIdentity{},
		identityErr: map[string]error{},
	}
	for _, e := range s.Env {
		f.env = append(f.env, e.Name+"="+e.Value)
	}
	f.setFile(tiocstiSysctl, "0\n")
	f.setFile(procStatus, "Name:\tprobe\nSeccomp:\t0\nSeccomp_filters:\t0\n")
	f.setDir(s.Tree)
	f.setDir(s.Tree + "/.git")
	f.setFile(s.GitReadOnly[0], "[core]\n")
	f.setDir(s.GitReadOnly[1])
	f.setDir(s.GitReadOnly[2])
	f.setFile(s.ManagedSettings, `{"hooks":{"PreToolUse":"`+s.ManagedMarker+`"}}`)
	f.setMount("/", false)
	f.setMount("/home/lead", false)
	f.setMount(s.Tree, false)
	for _, p := range s.GitReadOnly {
		f.setMount(p, true)
	}
	f.setMount(s.ManagedSettings, true)
	// The four parent folders, each a bind of its own: the requests and the
	// mail folder read-write, the mailbox and the published folder read-only.
	for _, pf := range parentFolders() {
		f.setDir(pf.path)
		f.setMount(pf.path, !pf.writable)
		f.writable[pf.path] = pf.writable
	}
	return f
}

// find returns the result of assertion n.
func find(t *testing.T, r Report, n int) Result {
	t.Helper()
	for _, res := range r.Results {
		if res.Number == n && !strings.HasSuffix(res.ID, OutsideSuffix) {
			return res
		}
	}
	t.Fatalf("no result for assertion %d in %+v", n, r.Results)
	return Result{}
}

// want checks status and a substring of the detail of assertion n.
func want(t *testing.T, r Report, n int, st Status, detail string) {
	t.Helper()
	res := find(t, r, n)
	if res.Status != st {
		t.Errorf("assertion %d: status %s (%s), want %s", n, res.Status, res.Detail, st)
		return
	}
	if !strings.Contains(res.Detail, detail) {
		t.Errorf("assertion %d: detail %q does not contain %q", n, res.Detail, detail)
	}
}
