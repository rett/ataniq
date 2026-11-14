//go:build linux && amd64

package qualification

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

type FDRecord struct {
	FD       int          `json:"fd"`
	Role     string       `json:"role"`
	Identity FileIdentity `json:"identity"`
	Cloexec  bool         `json:"cloexec"`
}

type Injection struct {
	Version          int         `json:"version"`
	Attempt          string      `json:"attempt"`
	Invocation       string      `json:"invocation"`
	Process          Process     `json:"process"`
	Boundary         Boundary    `json:"boundary"`
	Fixture          Subject     `json:"fixture"`
	Descriptors      []FDRecord  `json:"descriptors"`
	Profile          string      `json:"profile"`
	RunID            string      `json:"run_id"`
	RequestID        string      `json:"request_id"`
	RoleID           string      `json:"role_id"`
	DescriptorPolicy string      `json:"descriptor_policy,omitempty"`
	FD1Runtime       *FD1Runtime `json:"fd1_runtime,omitempty"`
}

type release struct {
	Version    int    `json:"version"`
	Attempt    string `json:"attempt"`
	Invocation string `json:"invocation"`
}

func fdFlags(fd int) (int, error) {
	n, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
	if e != 0 {
		return 0, e
	}
	return int(n), nil
}

func vacantFDs(limit uint64, occupied map[int]bool) error {
	return vacantSlots(limit, occupied, injectionFDs())
}

func vacantSlots(limit uint64, occupied map[int]bool, slots []int) error {
	for _, fd := range slots {
		if uint64(fd) >= limit || fd <= 2 {
			return errors.New("sparse descriptors exceed rlimit")
		}
		if occupied[fd] {
			return errors.New("sparse descriptor collision")
		}
	}
	return nil
}

func verifyFakeFD(fd int, s Subject) error {
	return verifyFD(fd, s, 4096)
}

func verifyFD(fd int, s Subject, limit int64) error {
	var before, after syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		return err
	}
	if before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink != 1 || before.Dev != s.Identity.Device || before.Ino != s.Identity.Inode || before.Uid != s.Identity.UID || before.Size < 0 || before.Size > limit {
		return errors.New("injected descriptor identity mismatch")
	}
	b := make([]byte, before.Size)
	n, err := syscall.Pread(fd, b, 0)
	if err != nil || n != len(b) {
		return errors.New("fake descriptor read failed")
	}
	if err = syscall.Fstat(fd, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Uid != after.Uid || before.Mode != after.Mode || before.Nlink != after.Nlink || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return errors.New("fake descriptor changed")
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != s.SHA256 {
		return errors.New("fake descriptor digest mismatch")
	}
	return nil
}

func readInjection(path string) (Injection, error) {
	var i Injection
	b, err := readOwned(path, MaxInputBytes)
	if err != nil {
		return i, err
	}
	err = decode(bytes.NewReader(b), MaxInputBytes, &i)
	return i, err
}

func validateInjection(a *Attempt, i Injection, u *Unit) error {
	if a.Config.DescriptorProfile == FD1TypedProfile && i.FD1Runtime == nil {
		return errors.New("missing typed injection binding")
	}
	if err := validateFD1Runtime(a, i.FD1Runtime); err != nil {
		return err
	}
	if i.DescriptorPolicy != a.DescriptorPolicy {
		return errors.New("injection descriptor policy mismatch")
	}
	if u == nil || i.Version != 1 || i.Attempt != a.ID || i.Invocation != u.Properties["InvocationID"] || i.Profile != a.Profile || i.RunID != a.Config.RunID || i.RequestID != a.Config.RequestID || i.RoleID != a.Config.RoleID || i.Process.Boot != a.Boot || i.Fixture != a.Fixture || i.Boundary != u.Boundary || u.Main == nil || !sameProcess(i.Process, *u.Main) || i.Process.Executable != a.Snapshots["driver"].Identity || !reflect.DeepEqual(i.Descriptors, descriptorsForBinding(a, i.FD1Runtime)) {
		return errors.New("invalid injection identity")
	}
	if !strings.Contains(i.Process.Status, "NoNewPrivs:\t1\n") {
		return errors.New("injector no-new-privileges unverified")
	}
	return nil
}

func expectedDescriptors(a *Attempt) []FDRecord {
	return descriptorsForBinding(a, a.FD1Runtime)
}

func descriptorsForBinding(a *Attempt, binding *FD1Runtime) []FDRecord {
	r := []FDRecord{{0, "request", a.Streams["request"], false}, {1, "output", a.Streams["output"], false}, {2, "stderr", a.Streams["stderr"], false}}
	if a.Config.DescriptorProfile == FD1TypedProfile {
		for j, fd := range []int{FD1DirectoryFD, FD1PipeFD, FD1SocketFD, FD1RegularFD} {
			id := FileIdentity{}
			if binding != nil && len(binding.Endpoints) == 4 {
				id = binding.Endpoints[j].Identity
			}
			r = append(r, FDRecord{fd, []string{"synthetic-directory", "synthetic-pipe", "synthetic-unix-stream", "synthetic-regular"}[j], id, false})
		}
		return append(r, FDRecord{BPFFD, "reviewed-bpf", a.Snapshots["bpf"].Identity, false})
	}
	for _, fd := range fakeFDs(a) {
		role := "synthetic-alias"
		if fd == FakeFD {
			role = "synthetic-regular"
		}
		r = append(r, FDRecord{fd, role, a.Fixture.Identity, false})
	}
	return append(r, FDRecord{BPFFD, "reviewed-bpf", a.Snapshots["bpf"].Identity, false})
}

// Deliberate sparse aliases belong only to the owned synthetic regular file.
// All fit the selected service's 1024 limit; no RLIMIT raising or slot fallback.
func injectionFDs() []int { return []int{64, FakeFD, 768, 1000, BPFFD} }
func injectionSlots(a *Attempt) []int {
	if a.Config.DescriptorProfile == FD1TypedProfile {
		return []int{FD1DirectoryFD, FD1PipeFD, FD1SocketFD, FD1RegularFD, BPFFD}
	}
	return injectionFDs()
}
func fakeFDs(a *Attempt) []int {
	if a.DescriptorPolicy == "" || a.Config.DescriptorProfile == FD1TypedProfile {
		return []int{FakeFD}
	}
	return []int{64, FakeFD, 768, 1000}
}

// PrivateInject runs ONLY in the separate transient service. It never changes
// the orchestrator's FD table and never uses exec.Command's final FD remapping.
func PrivateInject(path string) error {
	if !absolute(path) || !attemptID(filepath.Base(path)) {
		return errors.New("invalid injector root")
	}
	root := filepath.Dir(path)
	// Do not acquire the controller flock from its child. Read only its exact
	// durable record, including directory identity, before any injection.
	d, id, err := ownedDir(root)
	if err != nil {
		return err
	}
	d.Close()
	c := &Campaign{Path: root, Identity: id}
	a, err := c.read(filepath.Base(path))
	if err != nil || a.Phase != "launching-service" || a.DescriptorPolicy != FinalDescriptorPolicy {
		return errors.New("injector intent not invoking")
	}
	if time.Now().Unix() > a.NotAfter {
		return errors.New("injector safety deadline elapsed")
	}
	self, err := process(os.Getpid())
	if err != nil || self.Executable != a.Snapshots["driver"].Identity {
		return errors.New("wrong injector executable")
	}
	if a.Parent == nil || self.Cgroup != a.Parent.Boundary.Path+"/"+a.Service {
		return errors.New("injector outside exact service")
	}
	inv := os.Getenv("INVOCATION_ID")
	if !bounded(inv, 32, 32, isHex) {
		return errors.New("missing service invocation")
	}
	for _, name := range []string{"driver", "probe", "bwrap", "bpf"} {
		s, _, err := chainSubject(name, a.Snapshots[name].Path, true)
		if err != nil || s != a.Snapshots[name] {
			return errors.New("frozen subject changed")
		}
	}
	fake, _, err := subject(a.Fixture.Path, false, false, true, 4096)
	if err != nil || fake != a.Fixture {
		return errors.New("fake fixture changed")
	}
	var lim syscall.Rlimit
	if err = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return err
	}
	used := map[int]bool{0: true, 1: true, 2: true}
	for _, fd := range injectionSlots(a) {
		_, err := fdFlags(fd)
		if err == nil {
			used[fd] = true
		} else if err != syscall.EBADF {
			return err
		}
	}
	if err = vacantSlots(lim.Cur, used, injectionSlots(a)); err != nil {
		return err
	}
	if a.Config.DescriptorProfile == FD1TypedProfile {
		a.FD1Runtime, err = installFD1(a)
		if err != nil {
			return err
		}
	} else {
		for _, fd := range injectionSlots(a) {
			source := a.Fixture.Path
			if fd == BPFFD {
				source = a.Snapshots["bpf"].Path
			}
			f, err := openAbsolute(source, syscall.O_RDONLY)
			if err != nil {
				return err
			}
			if int(f.Fd()) >= 64 {
				f.Close()
				return errors.New("source descriptor collision")
			}
			err = syscall.Dup3(int(f.Fd()), fd, 0)
			ce := f.Close()
			if err != nil {
				return err
			}
			if ce != nil {
				return ce
			}
		}
	}
	for _, fd := range fakeFDs(a) {
		if err = verifyFakeFD(fd, a.Fixture); err != nil {
			return err
		}
	}
	for _, fd := range injectionSlots(a) {
		flags, err := fdFlags(fd)
		if err != nil || flags&syscall.FD_CLOEXEC != 0 {
			return errors.New("injection CLOEXEC mismatch")
		}
	}
	bnd, err := boundary(self.Cgroup)
	if err != nil {
		return err
	}
	descriptors := expectedDescriptors(a)
	for _, want := range descriptors {
		var st syscall.Stat_t
		flags, e := fdFlags(want.FD)
		kind := uint32(syscall.S_IFREG)
		if a.Config.DescriptorProfile == FD1TypedProfile && want.FD > 2 && want.FD != BPFFD {
			kind = fd1Kind(want.FD)
		}
		if syscall.Fstat(want.FD, &st) != nil || e != nil || st.Mode&syscall.S_IFMT != kind || kind == syscall.S_IFREG && st.Nlink != 1 || (FileIdentity{st.Dev, st.Ino, st.Uid}) != want.Identity || flags != 0 {
			return errors.New("descriptor role identity mismatch")
		}
	}
	i := Injection{1, a.ID, inv, self, bnd, a.Fixture, descriptors, a.Profile, a.Config.RunID, a.Config.RequestID, a.Config.RoleID, a.DescriptorPolicy, a.FD1Runtime}
	b, err := json.Marshal(i)
	if err != nil || len(b) > MaxOutputBytes {
		return errors.New("injection evidence excessive")
	}
	if err = verifyFD(BPFFD, a.Snapshots["bpf"], 32768); err != nil {
		return err
	}
	if err = publishOwned(path, "injection.json", b); err != nil {
		return err
	}
	// A finite gate prevents fast cases disappearing before binding. The marker
	// is not mounted in the workload and its read descriptor closes before exec.
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := readOwned(filepath.Join(path, "release.json"), 4096)
		if err == nil {
			var r release
			if decode(bytes.NewReader(b), 4096, &r) != nil || r != (release{1, a.ID, inv}) {
				return errors.New("wrong release identity")
			}
			break
		}
		if !os.IsNotExist(err) || time.Now().After(deadline) {
			return errors.New("injector handshake failed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = verifyFakeFD(FakeFD, a.Fixture); err != nil {
		return err
	}
	// Keep every injected role through the identical self-reexec path. Only the
	// final mode applies the compiled role policy at the terminal exec boundary.
	if err = verifyFD(BPFFD, a.Snapshots["bpf"], 32768); err != nil {
		return err
	}
	latest, err := c.read(a.ID)
	if err != nil || latest.Phase != "released" || latest.Injector == nil || latest.Injector.Invocation != inv {
		return errors.New("release not durably bound")
	}
	if a.Config.DescriptorProfile == FD1TypedProfile {
		if !sameJSON(latest.FD1Runtime, a.FD1Runtime) || authenticateFD1Runtime(latest, latest.FD1Runtime) != nil || verifyFD1Endpoints(latest) != nil {
			return errors.New("released typed binding changed")
		}
	}
	for _, name := range []string{"probe", "bwrap"} {
		s, _, err := chainSubject(name, a.Snapshots[name].Path, true)
		if err != nil || s != a.Snapshots[name] {
			return errors.New("exec subject changed")
		}
	}
	// Environment is deliberately empty, including manager/bus/credential paths.
	return syscall.Exec(a.Command[0], a.Command, []string{})
}
