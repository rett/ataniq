package sandboxnet

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fakeFS is an in-memory host. Every access is recorded so a test can check
// that only the two named paths are read.
type fakeFS struct {
	files  map[string]string
	errs   map[string]error
	links  map[string]string
	limits map[string]int
	reads  []string
}

func (f *fakeFS) ReadFile(name string, limit int) ([]byte, error) {
	f.reads = append(f.reads, name)
	if f.limits == nil {
		f.limits = map[string]int{}
	}
	f.limits[name] = limit
	if err := f.errs[name]; err != nil {
		return nil, err
	}
	c, ok := f.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if len(c) > limit {
		c = c[:limit]
	}
	return []byte(c), nil
}

func (f *fakeFS) RealPath(name string) (string, error) {
	f.reads = append(f.reads, "realpath "+name)
	if l, ok := f.links[name]; ok {
		return l, nil
	}
	return name, nil
}

func TestDeriveFromFSStubWithUpstream(t *testing.T) {
	f := &fakeFS{
		files: map[string]string{HostResolvConfPath: stubFile, SystemdUpstreamPath: upstreamFile},
		links: map[string]string{HostResolvConfPath: SystemdStubPath},
	}
	res, err := DeriveFromFS(f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceSystemdUpstream || len(res.Nameservers) != 2 {
		t.Errorf("%+v", res)
	}
	if got, want := strings.Join(f.reads, ","), "/etc/resolv.conf,realpath /etc/resolv.conf,/run/systemd/resolve/resolv.conf"; got != want {
		t.Errorf("reads %s", got)
	}
	for name, l := range f.limits {
		if l != MaxResolvConfBytes+1 {
			t.Errorf("%s read with limit %d", name, l)
		}
	}
}

func TestDeriveFromFSStubWithoutUpstream(t *testing.T) {
	f := &fakeFS{
		files: map[string]string{HostResolvConfPath: stubFile},
		links: map[string]string{HostResolvConfPath: SystemdStubPath},
	}
	_, err := DeriveFromFS(f)
	mustContain(t, refusalReason(t, err), "does not exist")
}

func TestDeriveFromFSNoSystemdDirectory(t *testing.T) {
	f := &fakeFS{
		files: map[string]string{HostResolvConfPath: "nameserver 192.0.2.1\n"},
		errs:  map[string]error{SystemdUpstreamPath: &fs.PathError{Op: "open", Path: SystemdUpstreamPath, Err: syscall.ENOTDIR}},
	}
	res, err := DeriveFromFS(f)
	if err != nil || res.Source != SourceHostFile {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestDeriveFromFSOversizedHostIsRefusedNotTruncated(t *testing.T) {
	f := &fakeFS{files: map[string]string{
		HostResolvConfPath: "nameserver 192.0.2.1\n" + strings.Repeat("#", 2*MaxResolvConfBytes),
	}}
	_, err := DeriveFromFS(f)
	mustContain(t, refusalReason(t, err), "larger than")
}

func TestDeriveFromFSReadErrorsAreNotRefusals(t *testing.T) {
	boom := errors.New("boom")
	for _, f := range []*fakeFS{
		{errs: map[string]error{HostResolvConfPath: boom}},
		{files: map[string]string{HostResolvConfPath: "nameserver 192.0.2.1\n"}, errs: map[string]error{SystemdUpstreamPath: boom}},
	} {
		_, err := DeriveFromFS(f)
		if err == nil || !errors.Is(err, boom) || errors.Is(err, ErrRefused) {
			t.Errorf("error %v", err)
		}
	}
	_, err := DeriveFromFS(&fakeFS{files: map[string]string{HostResolvConfPath: "nameserver 192.0.2.1\n"}, errs: map[string]error{SystemdUpstreamPath: fs.ErrPermission}})
	if err == nil || errors.Is(err, ErrRefused) {
		t.Errorf("permission error %v", err)
	}
}

// TestHostFS exercises the real implementation on files under a temporary
// directory only; it never reads a host file.
func TestHostFS(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	link := filepath.Join(dir, "link.conf")
	if err := os.WriteFile(target, []byte("nameserver 192.0.2.1\n"+strings.Repeat("#", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	var h HostFS
	got, err := h.ReadFile(link, 10)
	if err != nil || string(got) != "nameserver" {
		t.Errorf("%q %v", got, err)
	}
	real, err := h.RealPath(link)
	if err != nil {
		t.Fatal(err)
	}
	wantReal, _ := filepath.EvalSymlinks(target)
	if real != wantReal {
		t.Errorf("real path %s want %s", real, wantReal)
	}
	if _, err := h.ReadFile(filepath.Join(dir, "absent"), 10); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("absent: %v", err)
	}
	if _, err := h.ReadFile(dir, 10); err == nil {
		t.Error("a directory was read")
	}
}
