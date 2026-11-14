//go:build linux && amd64

package qualification

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTypedFDChildren(t *testing.T) {
	common := []string{"valid", "closed", "device", "inode", "type", "slot", "run", "request", "attempt", "nonce", "profile", "missing-binding", "nonregular-binding", "linked-binding", "oversized-binding", "malformed-binding", "symlink-binding", "nonblock", "access"}
	for _, c := range []Case{CaseFakeDirFD, CaseFakePipeFD, CaseFakeSocketFD} {
		scenarios := append([]string{}, common...)
		if c == CaseFakeDirFD {
			scenarios = append(scenarios, "symlink-leaf", "wrong-leaf", "swapped-leaf", "linked-leaf", "oversized-leaf", "wrong-content", "leaf-context")
		} else {
			scenarios = append(scenarios, "empty", "short", "wrong-frame", "wrong-frame-type", "trailing", "kept-open", "empty-open")
			if c == CaseFakeSocketFD {
				scenarios = append(scenarios, "socket-type", "raw-channel")
			}
		}
		for _, scenario := range scenarios {
			t.Run(string(c)+"/"+scenario, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTypedFDChild$", "--", "typed-fd-child", string(c), scenario)
				out, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatal("typed child exceeded watchdog")
				}
				if e, ok := err.(*exec.ExitError); ok && e.ExitCode() == 77 {
					t.Skipf("owned socketpair or required socket metadata query unavailable: %s", out)
				}
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
			})
		}
	}
}

func childStat(t *testing.T, fd int) syscall.Stat_t {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestTypedFDChild(t *testing.T) {
	args := descriptorChildArgs("typed-fd-child")
	if args == nil {
		return
	}
	c, scenario := Case(args[0]), args[1]
	r := typedRequest(c)
	b := typedBindings(r)
	dir := t.TempDir()
	leafDir := t.TempDir()
	leafPath := filepath.Join(leafDir, "leaf")
	leafData := []byte("leaf")
	if err := os.WriteFile(leafPath, leafData, 0600); err != nil {
		t.Fatal(err)
	}
	leafInfo, err := os.Stat(leafPath)
	if err != nil {
		t.Fatal(err)
	}
	leafStat := leafInfo.Sys().(*syscall.Stat_t)
	b.Directory.Leaf.Device, b.Directory.Leaf.Inode = uint64(leafStat.Dev), leafStat.Ino
	object := &b.Directory.FD1Object
	if c == CaseFakePipeFD {
		object = &b.Pipe.FD1Object
	}
	if c == CaseFakeSocketFD {
		object = &b.Socket.FD1Object
	}
	frame := fd1Frame(c, r.Payload.TypedFD.Nonce)
	actualFrame := append([]byte{}, frame...)
	switch scenario {
	case "empty", "empty-open":
		actualFrame = nil
	case "short":
		actualFrame = actualFrame[:len(actualFrame)-1]
	case "wrong-frame":
		actualFrame[0] = '?'
	case "wrong-frame-type":
		if c == CaseFakePipeFD {
			actualFrame = fd1Frame(CaseFakeSocketFD, b.Nonce)
		} else {
			actualFrame = fd1Frame(CaseFakePipeFD, b.Nonce)
		}
	case "trailing":
		actualFrame = append(actualFrame, '?')
	}
	source, producer := -1, -1
	if c == CaseFakeDirFD {
		source, err = syscall.Open(leafDir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	} else if c == CaseFakePipeFD {
		var fds [2]int
		err = syscall.Pipe2(fds[:], syscall.O_NONBLOCK|syscall.O_CLOEXEC)
		source, producer = fds[0], fds[1]
	} else {
		kind := syscall.SOCK_STREAM
		if scenario == "socket-type" {
			kind = syscall.SOCK_DGRAM
		}
		var fds [2]int
		fds, err = syscall.Socketpair(syscall.AF_UNIX, kind|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAFNOSUPPORT) {
			os.Exit(77)
		}
		source, producer = fds[0], fds[1]
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(source) })
	if producer >= 0 {
		if len(actualFrame) > 0 {
			if n, e := syscall.Write(producer, actualFrame); e != nil || n != len(actualFrame) {
				t.Fatal(n, e)
			}
		}
		if scenario == "kept-open" || scenario == "empty-open" || scenario == "access" && c == CaseFakePipeFD {
			t.Cleanup(func() { syscall.Close(producer) })
		} else if err := syscall.Close(producer); err != nil {
			t.Fatal(err)
		}
	}
	st := childStat(t, source)
	object.Device, object.Inode = uint64(st.Dev), st.Ino
	installed := source
	if scenario == "type" {
		installed, err = syscall.Open(leafPath, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { syscall.Close(installed) })
		other := childStat(t, installed)
		object.Device, object.Inode = uint64(other.Dev), other.Ino
	}
	if scenario == "access" {
		if c == CaseFakePipeFD {
			installed = producer
		} else if c == CaseFakeDirFD {
			// O_PATH for directories also has O_RDONLY access bits. It must fail
			// admission before leaf lookup/open despite the matching inode/type.
			flags := syscall.O_RDONLY | syscall.O_CLOEXEC
			flags |= 0x200000
			installed, err = syscall.Open(leafDir, flags, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { syscall.Close(installed) })
		} else {
			// Socketpair endpoints are always O_RDWR; exercise actual socket
			// access validation independently below with a fixed expectation.
			if fd1Flags(FD1Object{FD: source, Access: "read-only", Nonblock: pointer(true)}) == nil {
				t.Fatal("socket wrong access accepted")
			}
		}
	}
	if scenario == "nonblock" {
		if err := syscall.SetNonblock(source, c == CaseFakeDirFD); err != nil {
			t.Fatal(err)
		}
	}
	fd := typedFDSlot(c)
	if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0); e != syscall.EBADF {
		t.Fatal("fixed test slot occupied", fd, e)
	}
	if err := syscall.Dup3(installed, fd, 0); err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	stage := ""
	switch scenario {
	case "closed":
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		stage = "fstat"
	case "device":
		object.Device++
		stage = "fd-identity"
	case "inode":
		object.Inode++
		stage = "fd-identity"
	case "type":
		stage = "fd-identity"
	case "slot":
		object.FD++
		stage = "binding-validate"
	case "run":
		b.RunID = "bbbbbbbb"
		stage = "binding-context"
	case "request":
		b.RequestID = "bbbbbbbb"
		stage = "binding-context"
	case "attempt":
		b.AttemptID = "bbbbbbbb"
		stage = "binding-context"
	case "nonce":
		b.Nonce = "bbbbbbbb"
		stage = "binding-context"
		b.Pipe.SHA256 = syntheticDigest(fd1Frame(CaseFakePipeFD, b.Nonce))
		b.Socket.SHA256 = syntheticDigest(fd1Frame(CaseFakeSocketFD, b.Nonce))
	case "profile":
		b.Profile = "unknown"
		stage = "binding-validate"
	case "nonblock":
		stage = "fd-flags"
	case "access":
		if c != CaseFakeSocketFD {
			stage = "fd-flags"
		}
	case "socket-type":
		stage = "socket-type"
	case "kept-open", "empty-open":
		stage = "channel-read"
	case "empty", "short", "wrong-frame", "wrong-frame-type", "trailing":
		stage = "channel-frame"
	}
	if c == CaseFakeDirFD {
		// Deep-copy expected leaf so artifact and request never share mutable
		// test state for the context-mismatch negative.
		copyLeaf := *b.Directory.Leaf
		r.Payload.TypedFD.Leaf = &copyLeaf
		switch scenario {
		case "symlink-leaf":
			if err := os.Rename(leafPath, filepath.Join(leafDir, "original")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("original", leafPath); err != nil {
				t.Fatal(err)
			}
			stage = "leaf-identity"
		case "wrong-leaf":
			if err := os.Remove(leafPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(leafPath, 0700); err != nil {
				t.Fatal(err)
			}
			stage = "leaf-identity"
		case "swapped-leaf":
			if err := os.Rename(leafPath, filepath.Join(leafDir, "old")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(leafPath, leafData, 0600); err != nil {
				t.Fatal(err)
			}
			stage = "leaf-identity"
		case "linked-leaf":
			if err := os.Link(leafPath, filepath.Join(leafDir, "alias")); err != nil {
				t.Fatal(err)
			}
			stage = "leaf-identity"
		case "oversized-leaf":
			if err := os.WriteFile(leafPath, bytes.Repeat([]byte("x"), 4097), 0600); err != nil {
				t.Fatal(err)
			}
			stage = "leaf-identity"
		case "wrong-content":
			if err := os.WriteFile(leafPath, []byte("nope"), 0600); err != nil {
				t.Fatal(err)
			}
			stage = "leaf-compare"
		case "leaf-context":
			b.Directory.Leaf.Inode++
			stage = "binding-context"
		}
	}
	bindingPath := filepath.Join(dir, FD1BindingName)
	if err := os.WriteFile(bindingPath, []byte(wire(t, b)), 0600); err != nil {
		t.Fatal(err)
	}
	switch scenario {
	case "missing-binding":
		if err := os.Remove(bindingPath); err != nil {
			t.Fatal(err)
		}
		stage = "binding-read"
	case "nonregular-binding":
		if err := os.Remove(bindingPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(bindingPath, 0700); err != nil {
			t.Fatal(err)
		}
		stage = "binding-read"
	case "linked-binding":
		if err := os.Link(bindingPath, filepath.Join(dir, "alias")); err != nil {
			t.Fatal(err)
		}
		stage = "binding-read"
	case "oversized-binding":
		if err := os.WriteFile(bindingPath, bytes.Repeat([]byte(" "), MaxFD1BindingBytes+1), 0400); err != nil {
			t.Fatal(err)
		}
		stage = "binding-read"
	case "malformed-binding":
		if err := os.WriteFile(bindingPath, []byte(`{"version":1,"version":1}`), 0400); err != nil {
			t.Fatal(err)
		}
		stage = "binding-validate"
	case "symlink-binding":
		if err := os.Rename(bindingPath, filepath.Join(dir, "real")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("real", bindingPath); err != nil {
			t.Fatal(err)
		}
		stage = "binding-read"
	}
	o := baseObservation(r)
	if c == CaseFakeSocketFD && scenario == "raw-channel" {
		// Exercise actual nonblocking socket reception even on hosts whose
		// syscall policy denies SO_DOMAIN/SO_TYPE. This fixture belongs only
		// to the raw reader: Probe would consume its frame on admitted hosts.
		// The separate valid/socket-type cases exercise full socket admission.
		readFD1Channel(&o, r, b.Socket, childStat(t, fd), syscall.Read, syscall.Fstat, time.Now)
	} else {
		o = Probe(r, Config{FixtureRoot: dir})
	}
	t.Log(wire(t, o))
	if c == CaseFakeSocketFD && o.Errno != nil && (*o.Errno == int(syscall.EPERM) || *o.Errno == int(syscall.EACCES)) {
		for _, f := range o.Facts {
			if f.Name == "stage" && *f.Value == "socket-type" {
				buf := make([]byte, len(frame)+1)
				n, err := syscall.Read(source, buf)
				if scenario != "empty-open" && (err != nil || !bytes.Equal(buf[:n], actualFrame)) {
					t.Fatal("metadata rejection consumed socket data", n, err)
				}
				os.Exit(77)
			}
		}
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err, o)
	}
	if stage == "" {
		if o.Status != "observed" || o.Errno != nil {
			t.Fatal(o)
		}
		if c == CaseFakeDirFD {
			requireKnown(t, o, "opened", "true")
			requireKnown(t, o, "digest_match", "true")
		} else {
			requireKnown(t, o, "read_success", "true")
			requireKnown(t, o, "eof", "true")
			requireKnown(t, o, "frame_match", "true")
		}
		return
	}
	requireKnown(t, o, "stage", stage)
	if scenario == "closed" {
		if o.Status != "observed" || o.Errno == nil || *o.Errno != int(syscall.EBADF) {
			t.Fatal(o)
		}
	} else if o.Status != "inconclusive" {
		t.Fatal(o)
	}
	if scenario == "kept-open" || scenario == "empty-open" {
		if o.Errno == nil || *o.Errno != int(syscall.EAGAIN) {
			t.Fatal(o)
		}
	}
	if scenario == "wrong-content" {
		requireKnown(t, o, "opened", "true")
		requireKnown(t, o, "read_success", "true")
		requireKnown(t, o, "digest_match", "false")
	}
	if c != CaseFakeDirFD {
		consumed := stage == "channel-read" || stage == "channel-frame"
		if consumed && len(actualFrame) > 0 {
			requireKnown(t, o, "read_success", "true")
		}
		if !consumed {
			// The test-only alias shares the same receive queue. Every identity,
			// type, flag and context rejection must leave all bytes unconsumed.
			if err := syscall.SetNonblock(source, true); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, len(frame)+1)
			n, err := syscall.Read(source, buf)
			if err != nil || !bytes.Equal(buf[:n], actualFrame) {
				t.Fatalf("rejected endpoint was consumed: n=%d err=%v", n, err)
			}
		}
	} else if scenario != "wrong-content" {
		for _, f := range o.Facts {
			if f.Name == "opened" || f.Name == "read_success" {
				t.Fatal("rejected directory accessed leaf", o)
			}
		}
	}
}

func TestFD1FiniteReadsAndRetainedEffects(t *testing.T) {
	r := typedRequest(CaseFakePipeFD)
	c := typedBindings(r).Pipe
	frame := fd1Frame(r.Case, r.Payload.TypedFD.Nonce)
	for _, scenario := range []string{"eintr", "eintr-after-read", "deadline", "postread-fstat", "metadata", "one-byte"} {
		t.Run(scenario, func(t *testing.T) {
			o := baseObservation(r)
			calls := 0
			read := func(_ int, p []byte) (int, error) {
				calls++
				if scenario == "eintr" || scenario == "eintr-after-read" && calls > 1 {
					return 0, syscall.EINTR
				}
				if scenario == "one-byte" {
					if calls <= len(frame) {
						p[0] = frame[calls-1]
						return 1, nil
					}
					return 0, nil
				}
				if calls == 1 {
					return copy(p, frame), nil
				}
				return 0, nil
			}
			stat := func(_ int, s *syscall.Stat_t) error {
				if scenario == "postread-fstat" {
					return syscall.EIO
				}
				if scenario == "metadata" {
					s.Ino = 1
				}
				return nil
			}
			nowCalls := 0
			epoch := time.Now()
			now := func() time.Time {
				nowCalls++
				if scenario == "deadline" && nowCalls > 2 {
					return epoch.Add(time.Second)
				}
				return epoch
			}
			readFD1Channel(&o, r, c, syscall.Stat_t{}, read, stat, now)
			if calls > maxFD1ReadCalls {
				t.Fatal("unbounded read loop", calls)
			}
			if scenario == "eintr" || scenario == "eintr-after-read" {
				requireKnown(t, o, "stage", "channel-budget")
				if calls != maxFD1ReadCalls {
					t.Fatal(calls)
				}
			}
			if scenario != "eintr" {
				requireKnown(t, o, "read_success", "true")
			}
			if scenario == "deadline" {
				requireKnown(t, o, "stage", "channel-deadline")
			}
			if scenario == "postread-fstat" || scenario == "metadata" {
				requireKnown(t, o, "frame_match", "true")
				stage := "postread-fstat"
				if scenario == "metadata" {
					stage = "postread-identity"
				}
				requireKnown(t, o, "stage", stage)
			}
			if scenario == "one-byte" {
				if o.Status != "observed" || calls != len(frame)+1 {
					t.Fatal(o, calls)
				}
			} else if o.Status != "inconclusive" {
				t.Fatal(o)
			}
			if err := o.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFD1LeafValidationAndDrift(t *testing.T) {
	r := typedRequest(CaseFakeDirFD)
	l := r.Payload.TypedFD.Leaf
	before := syscall.Stat_t{Dev: l.Device, Ino: l.Inode, Mode: syscall.S_IFREG | 0600, Nlink: 1, Size: *l.Size}
	for _, scenario := range []string{"opened-identity", "opened-type", "opened-links", "opened-size", "postread-fstat", "postread-inode", "postread-mode", "postread-size", "postread-links", "postread-mtime", "postread-ctime", "short"} {
		t.Run(scenario, func(t *testing.T) {
			o := baseObservation(r)
			o.Facts = append(o.Facts, known("opened", "true"))
			statCalls, reads := 0, 0
			stat := func(_ int, s *syscall.Stat_t) error {
				statCalls++
				*s = before
				if statCalls == 1 {
					switch scenario {
					case "opened-identity":
						s.Ino++
					case "opened-type":
						s.Mode = syscall.S_IFIFO
					case "opened-links":
						s.Nlink++
					case "opened-size":
						s.Size++
					}
				}
				if statCalls == 2 {
					switch scenario {
					case "postread-fstat":
						return syscall.EIO
					case "postread-inode":
						s.Ino++
					case "postread-mode":
						s.Mode++
					case "postread-size":
						s.Size++
					case "postread-links":
						s.Nlink++
					case "postread-mtime":
						s.Mtim.Nsec++
					case "postread-ctime":
						s.Ctim.Nsec++
					}
				}
				return nil
			}
			pread := func(_ int, p []byte, off int64) (int, error) {
				reads++
				if off != 0 || len(p) > int(*l.Size)+1 {
					t.Fatal("bad pread bounds")
				}
				if scenario == "short" {
					return copy(p, []byte("lea")), nil
				}
				return copy(p, []byte("leaf")), nil
			}
			readFD1Leaf(&o, r, l, 123, before, stat, pread)
			if o.Status != "inconclusive" {
				t.Fatal(o)
			}
			requireKnown(t, o, "opened", "true")
			if strings.HasPrefix(scenario, "opened-") {
				if reads != 0 {
					t.Fatal("rejected leaf read")
				}
				requireKnown(t, o, "stage", "leaf-identity")
			} else {
				requireKnown(t, o, "read_success", "true")
			}
			if err := o.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFD1SocketDomainAndType(t *testing.T) {
	for _, scenario := range []string{"domain", "type", "domain-error", "type-error", "valid"} {
		get := func(fd, level, opt int) (int, error) {
			if fd != FD1SocketFD || level != syscall.SOL_SOCKET {
				t.Fatal("wrong socket query")
			}
			if opt == syscall.SO_DOMAIN {
				if scenario == "domain-error" {
					return 0, syscall.EIO
				}
				if scenario == "domain" {
					return syscall.AF_INET, nil
				}
				return syscall.AF_UNIX, nil
			}
			if opt != syscall.SO_TYPE {
				t.Fatal("unexpected socket query")
			}
			if scenario == "type-error" {
				return 0, syscall.EIO
			}
			if scenario == "type" {
				return syscall.SOCK_DGRAM, nil
			}
			return syscall.SOCK_STREAM, nil
		}
		if err := fd1Socket(FD1SocketFD, get); (err == nil) != (scenario == "valid") {
			t.Fatal(scenario, err)
		}
	}
}

func TestFD1FrozenReaderDoesNotAssertWorkloadStop(t *testing.T) {
	root, dir := ownedRoot(t)
	r := typedRequest(CaseFakePipeFD)
	data := []byte(wire(t, typedBindings(r)))
	put(t, filepath.Join(dir, FD1BindingName), data)
	got, err := readFrozenFlat(root, FD1BindingName, MaxFD1BindingBytes)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("frozen metadata read", err)
	}
	// Shared I/O does not weaken the collector's independent stop requirement.
	if _, err := CollectFlat(root, FD1BindingName, MaxFD1BindingBytes, false); err == nil {
		t.Fatal("collector stop proof bypassed")
	}
}
