//go:build linux && amd64

package qualification

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFileDirectoryPinFailure(t *testing.T) {
	root, dir := ownedRoot(t)
	put(t, filepath.Join(dir, "fake"), []byte("synthetic"))
	r := request(CaseFakeFile)
	r.Payload = &Payload{File: &NamePayload{Name: "fake"}}
	calls := 0
	open := func(root *os.Root, name string) (*os.File, error) {
		return openFlatWithPin(root, name, func(*os.Root) (*os.File, error) {
			calls++
			return nil, &os.PathError{Op: "open", Path: ".", Err: syscall.EMFILE}
		})
	}
	f, err := open(root, "fake")
	var pinErr *openDirError
	if f != nil || !errors.As(err, &pinErr) || !errors.Is(err, syscall.EMFILE) {
		t.Fatal("pin error lost or target opened", f, err)
	}
	o := baseObservation(r)
	fakeFile(&o, r, root, open)
	if calls != 2 || o.Status != "inconclusive" || o.Errno == nil || *o.Errno != int(syscall.EMFILE) || len(o.Facts) != 1 {
		t.Fatal(o, calls)
	}
	requireKnown(t, o, "stage", "open-dir")
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUnixDialErrorStages(t *testing.T) {
	for _, tc := range []struct {
		name, call, stage, status string
		err                       error
	}{
		{"socket", "socket", "socket", "inconclusive", syscall.EPERM},
		{"connect", "connect", "connect", "observed", syscall.EACCES},
		{"backlog", "connect", "connect", "observed", syscall.EAGAIN},
		{"setup", "setsockopt", "setsockopt", "inconclusive", syscall.EPERM},
		{"unattributed errno", "", "dial-setup", "inconclusive", syscall.EINVAL},
		{"deadline", "", "dial-setup", "inconclusive", os.ErrDeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.err
			if tc.call != "" {
				err = &os.SyscallError{Syscall: tc.call, Err: err}
			}
			err = fmt.Errorf("wrapped: %w", &net.OpError{Op: "dial", Net: "unix", Err: err})
			o := baseObservation(request(CaseFakeUnix))
			unixDialError(&o, err)
			if o.Status != tc.status || len(o.Facts) != 1 {
				t.Fatal(o)
			}
			requireKnown(t, o, "stage", tc.stage)
			var errno syscall.Errno
			if errors.As(tc.err, &errno) && (o.Errno == nil || *o.Errno != int(errno)) {
				t.Fatal("lost errno", o)
			}
			if tc.name == "backlog" && o.Reason != "syscall returned errno" {
				t.Fatal("EAGAIN mislabelled timeout", o)
			}
			if err := o.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
	o := baseObservation(request(CaseFakeUnix))
	operationError(&o, "nonce-read", &net.OpError{Op: "read", Net: "unix", Err: os.ErrDeadlineExceeded})
	if o.Status != "inconclusive" || o.Reason != "operation timed out" {
		t.Fatal(o)
	}
}

func TestUnixPreflightBeforeEffects(t *testing.T) {
	r := request(CaseFakeUnix)
	r.Payload = &Payload{Unix: &UnixPayload{Name: "fake", Device: pointer(uint64(0)), Inode: pointer(uint64(0)), UID: pointer(uint32(0)), Nonce: "01234567"}}
	for _, tc := range []struct {
		path, stage string
		max         int
	}{
		{"/absent-owned-fixture", "nonce-size", 1},
		{"/" + strings.Repeat("a", 103), "socket-path", 4096}, // Joined pathname: 109 bytes.
		{"/" + strings.Repeat("λ", 52), "socket-path", 4096},  // Bound bytes, not runes.
		{"/bad\x00root", "socket-path", 4096},
	} {
		r.MaxBytes = tc.max
		o := Probe(r, Config{FixtureRoot: tc.path})
		if o.Status != "error" || o.Errno != nil || len(o.Facts) != 1 {
			t.Fatal(o)
		}
		requireKnown(t, o, "stage", tc.stage)
	}
	r.MaxBytes = 4096
	for _, size := range []int{107, 108} {
		o := baseObservation(r)
		path, _, ok := unixInput(&o, r, "/"+strings.Repeat("a", size-6))
		if ok != (size == 107) || ok && len(path) != size {
			t.Fatal("incorrect Linux pathname bound", size, path, o)
		}
	}
}

func TestNonceReplyFraming(t *testing.T) {
	msg := []byte("ataniq-fixture-v1 01234567\n")
	for _, tc := range []struct {
		name, stage string
		reply       io.Reader
		match       *string
	}{
		{"exact", "", bytes.NewReader(msg), pointer("true")},
		{"short", "nonce-read", bytes.NewReader(msg[:len(msg)-1]), nil},
		{"trailing", "nonce-frame", strings.NewReader(string(msg) + "x"), pointer("false")},
		{"mismatch", "nonce-compare", strings.NewReader("?" + string(msg[1:])), pointer("false")},
		{"no write EOF", "nonce-eof", io.MultiReader(bytes.NewReader(msg), deadlineReader{}), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := baseObservation(request(CaseFakeUnix))
			o.Facts = append(o.Facts, known("connected", "true"))
			ok := readNonceReply(&o, tc.reply, msg)
			if ok != (tc.stage == "") || !ok && o.Status != "inconclusive" {
				t.Fatal(o)
			}
			requireKnown(t, o, "connected", "true")
			if tc.stage != "" {
				requireKnown(t, o, "stage", tc.stage)
			}
			if tc.match != nil {
				requireKnown(t, o, "nonce_match", *tc.match)
			} else {
				for _, f := range o.Facts {
					if f.Name == "nonce_match" {
						t.Fatal("incomplete reply has known match", o)
					}
				}
			}
			if err := o.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type deadlineReader struct{}

func (deadlineReader) Read([]byte) (int, error) { return 0, os.ErrDeadlineExceeded }

func TestOperationStagesAndErrno(t *testing.T) {
	for _, stage := range []string{"lstat", "open", "connect", "fstat", "pread"} {
		t.Run(stage, func(t *testing.T) {
			o := baseObservation(request(CaseFakeFile))
			operationError(&o, stage, &os.PathError{Op: stage, Path: "private fake path", Err: syscall.EACCES})
			if o.Status != "observed" || o.Errno == nil || *o.Errno != int(syscall.EACCES) || len(o.Facts) != 1 {
				t.Fatal(o)
			}
			requireKnown(t, o, "stage", stage)
			if err := o.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, io.ErrShortWrite, errors.New("fixture changed"), &net.OpError{Op: "read", Net: "unix", Err: syscall.ETIMEDOUT}} {
		o := baseObservation(request(CaseFakeUnix))
		o.Facts = append(o.Facts, known("connected", "true"))
		incompleteOperation(&o, "nonce-read", err)
		if o.Status != "inconclusive" {
			t.Fatal(o)
		}
		requireKnown(t, o, "stage", "nonce-read")
		requireKnown(t, o, "connected", "true")
		if err := o.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

// Fail metadata/close deterministically while operating only on an owned file.
type openedFixture struct {
	*os.File
	statErr, closeErr error
}

func (f openedFixture) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.File.Stat()
}
func (f openedFixture) Close() error {
	err := f.File.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return err
}

func TestOpenedSurvivesLaterFailure(t *testing.T) {
	_, dir := ownedRoot(t)
	path := filepath.Join(dir, "fake")
	put(t, path, []byte("synthetic"))
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"fstat", "close", "file-identity"} {
		t.Run(stage, func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			fixture := openedFixture{File: f}
			initial := before
			switch stage {
			case "fstat":
				fixture.statErr = syscall.EIO
			case "close":
				fixture.closeErr = syscall.EIO
			case "file-identity":
				initial = fakeInfo{} // Deliberately mismatched synthetic identity.
			}
			o := baseObservation(request(CaseFakeFile))
			inspectOpenedFile(&o, initial, fixture)
			if o.Status != "inconclusive" {
				t.Fatal(o)
			}
			requireKnown(t, o, "opened", "true")
			requireKnown(t, o, "stage", stage)
			if stage != "file-identity" && (o.Errno == nil || *o.Errno != int(syscall.EIO)) {
				t.Fatal("lost post-open errno", o)
			}
			if err := o.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFixtureRootFailureStage(t *testing.T) {
	_, dir := ownedRoot(t)
	for _, c := range []Case{CaseFakeFile, CaseFakeUnix} {
		r := request(c)
		if c == CaseFakeFile {
			r.Payload = &Payload{File: &NamePayload{Name: "absent"}}
		} else {
			r.Payload = &Payload{Unix: &UnixPayload{Name: "absent", Device: pointer(uint64(0)), Inode: pointer(uint64(0)), UID: pointer(uint32(0)), Nonce: "01234567"}}
		}
		o := Probe(r, Config{FixtureRoot: filepath.Join(dir, "absent-root")})
		if o.Status != "inconclusive" || o.Errno == nil || *o.Errno != int(syscall.ENOENT) || len(o.Facts) != 1 {
			t.Fatal(o)
		}
		requireKnown(t, o, "stage", "fixture-root")
	}
}

func ownedUnixListener(t *testing.T) (*net.UnixListener, Request, string) {
	t.Helper()
	_, dir := ownedRoot(t)
	path := filepath.Join(dir, "socket")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAFNOSUPPORT) {
			t.Skipf("owned Unix listener fixture unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	if err := l.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	r := request(CaseFakeUnix)
	r.MaxDurationMS = 1000
	// This owned, same-user fixture uses the current projection only. No actual
	// user namespace mapping or backend qualification is inferred by this test.
	r.Payload = &Payload{Unix: &UnixPayload{Name: "socket", Device: pointer(uint64(st.Dev)), Inode: pointer(st.Ino), UID: pointer(st.Uid), Nonce: "0123456789abcdef"}}
	return l, r, dir
}

func TestFakeUnixConnectedSurvivesExchangeFailure(t *testing.T) {
	for _, mode := range []string{"eof", "timeout", "mismatch", "trailing", "no-eof"} {
		t.Run(mode, func(t *testing.T) {
			l, r, dir := ownedUnixListener(t)
			done := make(chan error, 1)
			release := make(chan struct{})
			go func() {
				c, err := l.AcceptUnix()
				if err != nil {
					done <- err
					return
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(2 * time.Second))
				b := make([]byte, len("ataniq-fixture-v1 "+r.Payload.Unix.Nonce+"\n"))
				_, err = io.ReadFull(c, b)
				if err == nil {
					switch mode {
					case "timeout":
						<-release
					case "mismatch":
						b[0] = '?'
						_, err = c.Write(b)
					case "trailing":
						_, err = c.Write(append(b, '?'))
					case "no-eof":
						_, err = c.Write(b)
						if err == nil {
							<-release
						}
					}
				}
				done <- err
			}()
			o := Probe(r, Config{FixtureRoot: dir})
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if o.Status != "inconclusive" {
				t.Fatal(o)
			}
			requireKnown(t, o, "connected", "true")
			stage := "nonce-read"
			if mode == "mismatch" {
				stage = "nonce-compare"
				requireKnown(t, o, "nonce_match", "false")
			}
			if mode == "trailing" {
				stage = "nonce-frame"
				requireKnown(t, o, "nonce_match", "false")
			}
			if mode == "no-eof" {
				stage = "nonce-eof"
			}
			requireKnown(t, o, "stage", stage)
			if err := o.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFakeUnixWriteEOFAccepted(t *testing.T) {
	l, r, dir := ownedUnixListener(t)
	done, release := make(chan error, 1), make(chan struct{})
	go func() {
		c, err := l.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(2 * time.Second))
		msg := make([]byte, len("ataniq-fixture-v1 "+r.Payload.Unix.Nonce+"\n"))
		_, err = io.ReadFull(c, msg)
		if err == nil {
			_, err = c.Write(msg)
		}
		if err == nil {
			err = c.CloseWrite()
		}
		done <- err
		<-release // The read half stays open until after the probe returns.
	}()
	o := Probe(r, Config{FixtureRoot: dir})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if o.Status != "observed" {
		t.Fatal(o)
	}
	requireKnown(t, o, "connected", "true")
	requireKnown(t, o, "nonce_match", "true")
}

func TestFakeUnixConnectRefusedStage(t *testing.T) {
	l, r, dir := ownedUnixListener(t)
	l.SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	o := Probe(r, Config{FixtureRoot: dir})
	if o.Status != "observed" || o.Errno == nil || *o.Errno != int(syscall.ECONNREFUSED) || len(o.Facts) != 1 {
		t.Fatal(o)
	}
	requireKnown(t, o, "stage", "connect")
}

type projectedSocketInfo struct {
	fakeInfo
	uid uint32
}

func (f projectedSocketInfo) Sys() any {
	return &syscall.Stat_t{Nlink: 1, Uid: f.uid, Dev: 12, Ino: 34}
}

func TestUnixProjectedInodeUID(t *testing.T) {
	// Synthetic projection values test comparison, not real namespace mappings.
	for _, uid := range []uint32{0, 12345} {
		fi := projectedSocketInfo{fakeInfo: fakeInfo{mode: os.ModeSocket}, uid: uid}
		p := &UnixPayload{Device: pointer(uint64(12)), Inode: pointer(uint64(34)), UID: pointer(uid)}
		if !socketIdentity(fi, p) {
			t.Fatal("matching projected UID rejected")
		}
		p.UID = pointer(uid + 1)
		if socketIdentity(fi, p) {
			t.Fatal("mismatched projected UID accepted")
		}
	}
}
