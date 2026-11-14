//go:build linux && amd64

package qualification

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// installFD1 is called only in the owned service (or a disposable test child).
// No temporary endpoint escapes publication. In particular, receiving aliases
// sharing O_NONBLOCK must close as well as producers and peers.
func installFD1(a *Attempt) (result *FD1Runtime, err error) {
	if a.Config.DescriptorProfile != FD1TypedProfile || a.DescriptorPolicy != FinalDescriptorPolicy {
		return nil, errors.New("fixed typed injection profile required")
	}
	if err = verifyFD1Static(a); err != nil {
		return nil, err
	}
	var lim syscall.Rlimit
	if err = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return nil, err
	}
	used := map[int]bool{}
	for _, fd := range injectionSlots(a) {
		_, e := fdFlags(fd)
		if e == nil {
			used[fd] = true
		} else if e != syscall.EBADF {
			return nil, e
		}
	}
	if err = vacantSlots(lim.Cur, used, injectionSlots(a)); err != nil {
		return nil, err
	}
	temps := []int{}
	installed := []int{}
	defer func() {
		for _, fd := range temps {
			if e := syscall.Close(fd); e != nil && err == nil {
				err = e
			}
		}
		if err != nil {
			result = nil
			for _, fd := range installed {
				_ = syscall.Close(fd)
			}
		}
	}()
	track := func(fd int) error {
		temps = append(temps, fd)
		if fd < 3 || fd >= FD1DirectoryFD {
			return errors.New("temporary descriptor collides with fixed table")
		}
		flags, e := fdFlags(fd)
		if e != nil || flags != syscall.FD_CLOEXEC {
			return errors.New("temporary descriptor is not CLOEXEC")
		}
		return nil
	}
	open := func(path string, flags int) (int, error) {
		fd, e := syscall.Open(path, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if e != nil {
			return -1, e
		}
		return fd, track(fd)
	}
	dir, err := open(a.FD1Static.Directory.Path, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	regular, err := open(a.Fixture.Path, syscall.O_RDONLY)
	if err != nil {
		return nil, err
	}
	bpf, err := open(a.Snapshots["bpf"].Path, syscall.O_RDONLY)
	if err != nil {
		return nil, err
	}
	p := make([]int, 2)
	if err = syscall.Pipe2(p, syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		return nil, err
	}
	// Track both before any failure can return.
	temps = append(temps, p...)
	for _, fd := range p {
		if fd < 3 || fd >= FD1DirectoryFD {
			return nil, errors.New("pipe temporary collision")
		}
	}
	s, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, &hostFailure{"typed-socket-setup", "socket-admission-failed", err}
	}
	temps = append(temps, s[:]...)
	for _, fd := range s {
		if fd < 3 || fd >= FD1DirectoryFD {
			return nil, errors.New("socket temporary collision")
		}
	}
	for _, fd := range temps {
		flags, e := fdFlags(fd)
		if e != nil || flags != syscall.FD_CLOEXEC {
			return nil, errors.New("temporary CLOEXEC flags changed")
		}
	}
	for _, fd := range []int{p[1], s[1]} {
		access, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
		want := syscall.O_NONBLOCK | syscall.O_WRONLY
		if fd == s[1] {
			want = syscall.O_NONBLOCK | syscall.O_RDWR
		}
		if e != 0 || int(access) != want {
			return nil, errors.New("producer flags changed")
		}
	}
	for _, pair := range []struct {
		fd    int
		frame []byte
	}{{p[1], fd1Frame(CaseFakePipeFD, a.FixtureNonce)}, {s[1], fd1Frame(CaseFakeSocketFD, a.FixtureNonce)}} {
		n, e := syscall.Write(pair.fd, pair.frame)
		if e != nil || n != len(pair.frame) {
			return nil, errors.New("short typed frame write")
		}
	}
	for j, source := range []int{dir, p[0], s[0], regular, bpf} {
		target := injectionSlots(a)[j]
		if err = syscall.Dup3(source, target, 0); err != nil {
			return nil, err
		}
		installed = append(installed, target)
	}
	// Install receiving endpoints BEFORE dropping the last temporary source.
	for len(temps) > 0 {
		fd := temps[0]
		temps = temps[1:]
		if err = syscall.Close(fd); err != nil {
			return nil, err
		}
		if _, e := fdFlags(fd); e != syscall.EBADF {
			return nil, errors.New("temporary alias survived close")
		}
	}
	object := func(fd int, kind, access string, nb bool) (FD1Object, FD1Endpoint, error) {
		var st syscall.Stat_t
		if e := syscall.Fstat(fd, &st); e != nil {
			return FD1Object{}, FD1Endpoint{}, e
		}
		flags, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
		if e != 0 {
			return FD1Object{}, FD1Endpoint{}, e
		}
		return FD1Object{fd, st.Dev, st.Ino, kind, access, &nb}, FD1Endpoint{fd, FileIdentity{st.Dev, st.Ino, st.Uid}, st.Mode, int(flags)}, nil
	}
	r := &FD1Runtime{SourcesClosed: true}
	objects := []FD1Object{}
	for j, fd := range injectionSlots(a)[:4] {
		o, ep, e := object(fd, []string{"directory", "pipe", "unix-stream", "regular"}[j], []string{"read-only", "read-only", "read-write", "read-only"}[j], j == 1 || j == 2)
		if e != nil {
			return nil, e
		}
		objects = append(objects, o)
		r.Endpoints = append(r.Endpoints, ep)
	}
	closed := true
	size := int64(len(fixtureBytes(a.FixtureNonce)))
	pipe, socket := fd1Frame(CaseFakePipeFD, a.FixtureNonce), fd1Frame(CaseFakeSocketFD, a.FixtureNonce)
	r.Bindings = FD1Bindings{Version: 1, RunID: a.Config.RunID, RequestID: a.Config.RequestID, AttemptID: a.ID, Profile: FD1TypedProfile, PlanSHA256: a.PlanSHA256, Nonce: a.FixtureNonce,
		Directory: &FD1Directory{objects[0], &a.FD1Static.Challenge},
		Pipe:      &FD1Channel{objects[1], len(pipe), syntheticDigest(pipe), &closed},
		Socket:    &FD1Channel{objects[2], len(socket), syntheticDigest(socket), &closed},
		Regular:   &FD1Regular{objects[3], &size, a.Fixture.SHA256}}
	// Admission happens before any artifact/receipt is published, including
	// actual socket domain/type. Neither validator reads a channel.
	copy := *a
	copy.FD1Runtime = r
	if err = verifyFD1Endpoints(&copy); err != nil {
		return nil, err
	}
	data, err := json.Marshal(r.Bindings)
	if err != nil || len(data) > MaxFD1BindingBytes {
		return nil, errors.New("typed artifact too large")
	}
	if err = publishOwned(filepath.Join(a.Config.Campaign, a.ID, "fixtures"), FD1BindingName, data); err != nil {
		return nil, err
	}
	r.Artifact, _, err = subject(filepath.Join(a.Config.Campaign, a.ID, "fixtures", FD1BindingName), false, false, true, MaxFD1BindingBytes)
	if err != nil {
		return nil, err
	}
	if err = authenticateFD1Runtime(a, r); err != nil {
		return nil, err
	}
	return r, nil
}

func verifyFD1Endpoints(a *Attempt) error {
	r := a.FD1Runtime
	if r == nil || r.Bindings.Validate() != nil || !r.SourcesClosed || len(r.Endpoints) != 4 {
		return errors.New("missing typed endpoint evidence")
	}
	if err := verifyFD1Static(a); err != nil {
		return err
	}
	for j, o := range fd1Objects(r.Bindings) {
		e := r.Endpoints[j]
		var st syscall.Stat_t
		flags, err := fdFlags(o.FD)
		access, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(o.FD), syscall.F_GETFL, 0)
		if syscall.Fstat(o.FD, &st) != nil || err != nil || flags != 0 || errno != 0 || e.FD != o.FD ||
			(FileIdentity{st.Dev, st.Ino, st.Uid}) != e.Identity || st.Dev != o.Device || st.Ino != o.Inode || st.Uid != uint32(os.Getuid()) || st.Mode != e.Mode || st.Mode&syscall.S_IFMT != fd1Kind(o.FD) ||
			int(access) != e.Flags || e.Flags != fd1ExpectedFlags(o.FD) || st.Mode&syscall.S_IFMT == syscall.S_IFREG && st.Nlink != 1 {
			return errors.New("typed endpoint identity/type/access/flags changed")
		}
		if o.FD == FD1SocketFD {
			if err := fd1Socket(o.FD, syscall.GetsockoptInt); err != nil {
				return &hostFailure{"typed-socket-metadata", "socket-admission-failed", err}
			}
		}
		if o.FD == FD1DirectoryFD {
			leaf, err := fd1LeafStat(o.FD, "leaf")
			if err != nil || !fd1LeafMatches(leaf, &a.FD1Static.Challenge, 4096) || leaf.Uid != uint32(os.Getuid()) {
				return errors.New("typed directory leaf changed")
			}
		}
	}
	if err := verifyFakeFD(FakeFD, a.Fixture); err != nil {
		return err
	}
	if err := verifyFD(BPFFD, a.Snapshots["bpf"], 32768); err != nil {
		return err
	}
	if off, err := syscall.Seek(BPFFD, 0, 1); err != nil || off != 0 {
		return errors.New("typed BPF offset changed")
	}
	return nil
}
