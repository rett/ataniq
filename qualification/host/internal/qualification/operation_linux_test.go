//go:build linux && amd64

package qualification

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

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
	for _, mode := range []string{"eof", "timeout", "mismatch"} {
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
			requireKnown(t, o, "stage", stage)
			if err := o.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
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
