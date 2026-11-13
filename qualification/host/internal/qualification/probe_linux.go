//go:build linux && amd64

package qualification

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Config is supplied by a trusted launcher, never decoded from the wire.
// FixtureRoot must be an explicit frozen owned directory containing only fake
// fixtures. Descriptor lifetime/ownership must remain stable throughout Probe.
// For Unix Dial the trusted caller must freeze the complete fixture pathname,
// its ancestors, mount mapping and all writable aliases for the entire call.
// Root pins metadata access, but Dial resolves the original absolute pathname.
// Nonce validation happens after connecting and cannot undo that exposure.
type Config struct{ FixtureRoot string }

func known(name, value string) Fact { return Fact{Name: name, State: "known", Value: &value} }
func unknown(name string) Fact      { return Fact{Name: name, State: "unknown"} }

func baseObservation(r Request) Observation {
	return Observation{Version: Version1, RunID: r.RunID, RequestID: r.RequestID, Case: r.Case, Status: "observed", Facts: []Fact{}}
}

func ErrorObservation() Observation {
	return Observation{Version: Version1, Status: "error", Facts: []Fact{}, Reason: "invalid probe input or configuration"}
}

// Probe performs only the four core cases. It does not compute qualification.
func Probe(r Request, cfg Config) Observation {
	if err := r.Validate(); err != nil {
		return ErrorObservation()
	}
	o := baseObservation(r)
	if futureCase(r.Case) {
		o.Status = "unsupported"
		o.Reason = "case requires a separately implemented driver and controls"
		return o
	}
	switch r.Case {
	case CaseInventory:
		o = inventory(r, kernelMetadata{}, os.LookupEnv)
	case CaseFakeFD:
		fakeFD(&o, r)
	case CaseFakeFile, CaseFakeUnix:
		if !filepath.IsAbs(cfg.FixtureRoot) {
			o.Status = "error"
			o.Reason = "explicit absolute fixture root required"
			return o
		}
		root, err := os.OpenRoot(cfg.FixtureRoot)
		if err != nil {
			incompleteOperation(&o, "fixture-root", err)
			return o
		}
		defer root.Close()
		if r.Case == CaseFakeFile {
			fakeFile(&o, r, root)
		} else {
			fakeUnix(&o, r, root, cfg.FixtureRoot)
		}
	}
	// A pathological but bounded inventory may still exceed the wire budget.
	// Refuse the whole inventory explicitly rather than emitting truncated facts.
	if err := EncodeObservation(io.Discard, o); err != nil {
		o.Facts = []Fact{unknown("inventory")}
		o.Status = "inconclusive"
		o.Reason = "observation exceeds protocol bounds"
		o.Errno = nil
	}
	return o
}

// Record only the attempted stage. A known syscall termination is a raw
// observation; the driver needs fixture/applied-state/paired-control evidence
// to interpret it. Never infer that a later open/connect/read was attempted.
func operationError(o *Observation, stage string, err error) {
	o.Status = "inconclusive"
	o.Reason = "operation did not complete"
	o.Errno = nil
	var errno syscall.Errno
	if errors.As(err, &errno) && errno > 0 && errno <= 4095 {
		n := int(errno)
		o.Errno = &n
		o.Status = "observed"
		o.Reason = "syscall returned errno"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		o.Status = "inconclusive"
		o.Reason = "operation timed out"
	}
	o.Facts = append(o.Facts, known("stage", stage))
}

// Setup/validation failures and incomplete exchanges remain uncertain even if
// a constituent syscall returned an errno. Earlier successful effects survive.
func incompleteOperation(o *Observation, stage string, err error) {
	operationError(o, stage, err)
	o.Status = "inconclusive"
	o.Reason = "fixture validation or exchange incomplete"
}

func fakeFile(o *Observation, r Request, root *os.Root) {
	name := r.Payload.File.Name
	before, err := root.Lstat(name)
	if err != nil {
		operationError(o, "lstat", err)
		return
	}
	if err := checkRegular(before, int64(r.MaxBytes)); err != nil {
		incompleteOperation(o, "validate-file", err)
		return
	}
	f, err := openFlat(root, name)
	if err != nil {
		operationError(o, "open", err)
		return
	}
	inspectOpenedFile(o, before, f)
}

// Keep open success independent of the subsequent metadata and close result.
func inspectOpenedFile(o *Observation, before os.FileInfo, f interface {
	Stat() (os.FileInfo, error)
	Close() error
}) {
	o.Facts = append(o.Facts, known("opened", "true"))
	opened, statErr := f.Stat()
	closeErr := f.Close()
	if statErr != nil {
		incompleteOperation(o, "fstat", statErr)
		return
	}
	if closeErr != nil {
		incompleteOperation(o, "close", closeErr)
		return
	}
	if !same(before, opened) {
		incompleteOperation(o, "file-identity", errors.New("fixture changed"))
		return
	}
	st := opened.Sys().(*syscall.Stat_t)
	o.Facts = append(o.Facts, known("device", strconv.FormatUint(uint64(st.Dev), 10)), known("inode", strconv.FormatUint(st.Ino, 10)), known("size", strconv.FormatInt(opened.Size(), 10)))
}

func fakeFD(o *Observation, r Request) {
	p := r.Payload.FD
	// Validate ran before any descriptor operation. Never read an unknown FD,
	// stdio, directory or socket; fstat identity and regular type must match.
	var before syscall.Stat_t
	if err := syscall.Fstat(*p.FD, &before); err != nil {
		operationError(o, "fstat", err)
		return
	}
	match := uint64(before.Dev) == *p.Device && before.Ino == *p.Inode && before.Mode&syscall.S_IFMT == syscall.S_IFREG
	o.Facts = append(o.Facts, known("identity_match", strconv.FormatBool(match)))
	if !match {
		incompleteOperation(o, "fd-identity", errors.New("unexpected descriptor"))
		return
	}
	if before.Size < 0 || before.Size > int64(r.MaxBytes) {
		incompleteOperation(o, "fd-size", errors.New("descriptor exceeds bound"))
		return
	}
	buf := make([]byte, r.MaxBytes+1)
	n, err := syscall.Pread(*p.FD, buf, 0)
	if err != nil {
		operationError(o, "pread", err)
		return
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(*p.FD, &after); err != nil {
		incompleteOperation(o, "postread-fstat", err)
		return
	}
	if !stableFD(before, after) || int64(n) != before.Size {
		incompleteOperation(o, "postread-identity", errors.New("descriptor changed or read incomplete"))
		return
	}
	digest := sha256.Sum256(buf[:n])
	o.Facts = append(o.Facts, known("digest_match", strconv.FormatBool(hex.EncodeToString(digest[:]) == p.SHA256)))
}

func stableFD(a, b syscall.Stat_t) bool {
	// Reading may update atime. It must not change content/identity metadata.
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func socketIdentity(fi os.FileInfo, p *UnixPayload) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && fi.Mode()&os.ModeSocket != 0 && uint64(st.Dev) == *p.Device && st.Ino == *p.Inode && st.Uid == *p.UID
}

func fakeUnix(o *Observation, r Request, root *os.Root, path string) {
	p := r.Payload.Unix
	before, err := root.Lstat(p.Name)
	if err != nil {
		operationError(o, "lstat", err)
		return
	}
	if !socketIdentity(before, p) {
		incompleteOperation(o, "socket-identity", errors.New("unexpected socket"))
		return
	}
	deadline := time.Now().Add(time.Duration(r.MaxDurationMS) * time.Millisecond)
	d := net.Dialer{Deadline: deadline}
	c, err := d.Dial("unix", filepath.Join(path, p.Name))
	if err != nil {
		operationError(o, "connect", err)
		return
	}
	o.Facts = append(o.Facts, known("connected", "true"))
	defer c.Close()
	if err = c.SetDeadline(deadline); err != nil {
		incompleteOperation(o, "deadline", err)
		return
	}
	u := c.(*net.UnixConn)
	raw, err := u.SyscallConn()
	if err != nil {
		incompleteOperation(o, "syscall-conn", err)
		return
	}
	var cred *syscall.Ucred
	var credErr error
	err = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil {
		incompleteOperation(o, "peer-control", err)
		return
	}
	if credErr != nil {
		incompleteOperation(o, "peer-credentials", credErr)
		return
	}
	if cred == nil || cred.Uid != *p.UID {
		incompleteOperation(o, "peer-identity", errors.New("unexpected peer"))
		return
	}
	after, err := root.Lstat(p.Name)
	if err != nil {
		incompleteOperation(o, "endpoint-lstat", err)
		return
	}
	if !same(before, after) {
		incompleteOperation(o, "endpoint-identity", errors.New("fixture changed"))
		return
	}
	// Fixed synthetic protocol. No commands, arbitrary service requests or
	// ancillary capabilities. Only the exact typed nonce reply is accepted.
	msg := []byte("ataniq-fixture-v1 " + p.Nonce + "\n")
	if len(msg) > r.MaxBytes {
		o.Status = "error"
		o.Reason = "nonce frame exceeds max_bytes"
		o.Facts = append(o.Facts, known("stage", "nonce-size"))
		return
	}
	n, err := c.Write(msg)
	if err == nil && n != len(msg) {
		err = io.ErrShortWrite
	}
	if err != nil {
		incompleteOperation(o, "nonce-write", err)
		return
	}
	response := make([]byte, len(msg))
	if _, err = io.ReadFull(c, response); err != nil {
		incompleteOperation(o, "nonce-read", err)
		return
	}
	match := bytes.Equal(response, msg)
	o.Facts = append(o.Facts, known("nonce_match", strconv.FormatBool(match)))
	if !match {
		incompleteOperation(o, "nonce-compare", errors.New("unexpected nonce"))
		return
	}
	if err := c.Close(); err != nil {
		incompleteOperation(o, "close", err)
	}
}

// Only these kernel metadata paths are read, plus the three own namespace
// handles and bounded /proc/self/fd metadata. No proc/sys recursion occurs.
var metadataPaths = map[string]string{
	"status": "/proc/self/status", "interfaces": "/proc/self/net/dev",
	"ipv4_routes": "/proc/self/net/route", "ipv6_routes": "/proc/self/net/ipv6_route",
}

type fdMetadata struct {
	Number int
	Device uint64
	Inode  uint64
	Mode   uint32
}
type metadata interface {
	read(string) ([]byte, error)
	namespace(string) (string, error)
	fds() ([]fdMetadata, error)
}
type kernelMetadata struct{}

func (kernelMetadata) read(key string) ([]byte, error) {
	path, ok := metadataPaths[key]
	if !ok {
		return nil, errors.New("unexpected metadata source")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 32769))
	if len(b) > 32768 {
		return nil, errors.New("metadata too large")
	}
	return b, err
}
func (kernelMetadata) namespace(kind string) (string, error) {
	if kind != "pid" && kind != "mnt" && kind != "net" {
		return "", errors.New("unexpected namespace")
	}
	fi, err := os.Stat("/proc/self/ns/" + kind)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("missing namespace identity")
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), nil
}
func (kernelMetadata) fds() ([]fdMetadata, error) {
	d, err := os.Open("/proc/self/fd")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return enumerateFDs(d, syscall.Fstat)
}

func enumerateFDs(d *os.File, stat func(int, *syscall.Stat_t) error) ([]fdMetadata, error) {
	names, err := d.Readdirnames(MaxEntries + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(names) > MaxEntries {
		return nil, errors.New("too many descriptors")
	}
	result := make([]fdMetadata, 0, len(names))
	for _, name := range names {
		n, err := strconv.Atoi(name)
		if err != nil || n < 0 {
			return nil, errors.New("bad fd entry")
		}
		if uintptr(n) == d.Fd() {
			continue
		}
		var st syscall.Stat_t
		if err = stat(n, &st); err != nil {
			return nil, err
		}
		result = append(result, fdMetadata{n, uint64(st.Dev), st.Ino, st.Mode})
	}
	return result, nil
}

func incomplete(o *Observation, name, state string) {
	o.Status = "inconclusive"
	o.Reason = "inventory incomplete or unavailable"
	o.Facts = append(o.Facts, Fact{Name: name, State: state})
}

var environmentNames = []string{"HOME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "SSH_AUTH_SOCK", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ATANIQ_FIXTURE_MARKER"}

func inventory(r Request, m metadata, lookup func(string) (string, bool)) Observation {
	o := baseObservation(r)
	status, err := m.read("status")
	capNames := []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"}
	for _, name := range capNames {
		value, state := capability(status, name)
		if err != nil {
			state = "unknown"
		}
		if state != "known" {
			incomplete(&o, name, state)
		} else {
			o.Facts = append(o.Facts, known(name, value))
		}
	}
	for _, name := range environmentNames {
		_, present := lookup(name)
		o.Facts = append(o.Facts, known("env."+name, strconv.FormatBool(present)))
	}
	for _, kind := range []string{"pid", "mnt", "net"} {
		value, err := m.namespace(kind)
		if err != nil || value == "" {
			incomplete(&o, "namespace."+kind, "unknown")
		} else {
			o.Facts = append(o.Facts, known("namespace."+kind, value))
		}
	}
	for _, key := range []string{"interfaces", "ipv4_routes", "ipv6_routes"} {
		data, err := m.read(key)
		rows, parseErr := networkRows(key, data)
		if err != nil || parseErr != nil {
			incomplete(&o, key, "unknown")
			continue
		}
		o.Facts = append(o.Facts, known(key+".count", strconv.Itoa(len(rows))))
		for i, row := range rows {
			o.Facts = append(o.Facts, known(fmt.Sprintf("%s.%d", key, i), row))
		}
	}
	fds, err := m.fds()
	if err != nil || len(fds) > MaxEntries {
		incomplete(&o, "fds", "unknown")
	} else {
		o.Facts = append(o.Facts, known("fds.count", strconv.Itoa(len(fds))))
		for i, fd := range fds {
			o.Facts = append(o.Facts, known(fmt.Sprintf("fds.%d", i), fmt.Sprintf("fd=%d dev=%d ino=%d mode=%o", fd.Number, fd.Device, fd.Inode, fd.Mode)))
		}
	}
	return o
}

func capability(data []byte, name string) (string, string) {
	if len(data) > 32768 {
		return "", "unknown"
	}
	value := ""
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if len(line) > MaxLineBytes {
			return "", "unknown"
		}
		if strings.HasPrefix(line, name+":") {
			count++
			value = strings.TrimSpace(strings.TrimPrefix(line, name+":"))
		}
	}
	if count == 0 {
		return "", "missing"
	}
	if count != 1 || len(value) != 16 {
		return "", "unknown"
	}
	if _, err := strconv.ParseUint(value, 16, 64); err != nil {
		return "", "unknown"
	}
	return value, "known"
}

// Network rows retain topology facts only: interface names and raw route fields.
// Known empty route tables require valid framing, not an empty/missing read.
func networkRows(kind string, data []byte) ([]string, error) {
	bad := errors.New("invalid or excessive network metadata")
	if len(data) == 0 || len(data) > 32768 || data[len(data)-1] != '\n' {
		return nil, bad
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for _, line := range lines {
		if !bounded(line, 0, MaxLineBytes, func(r rune) bool { return r == '\t' || r >= 0x20 && r < 0x7f }) {
			return nil, bad
		}
	}
	start := 0
	switch kind {
	case "interfaces":
		if len(lines) < 2 || !strings.Contains(lines[0], "Inter-") || !strings.Contains(lines[1], "face") {
			return nil, bad
		}
		start = 2
	case "ipv4_routes":
		if len(lines) < 1 || !strings.HasPrefix(lines[0], "Iface\tDestination\tGateway") {
			return nil, bad
		}
		start = 1
	case "ipv6_routes":
	default:
		return nil, bad
	}
	if len(lines)-start > 24 {
		return nil, bad
	}
	rows := []string{}
	for _, line := range lines[start:] {
		f := strings.Fields(line)
		switch kind {
		case "interfaces":
			name, stats, ok := strings.Cut(line, ":")
			name = strings.TrimSpace(name)
			if !ok || !validName(name) || len(strings.Fields(stats)) != 16 {
				return nil, bad
			}
			for _, v := range strings.Fields(stats) {
				if _, err := strconv.ParseUint(v, 10, 64); err != nil {
					return nil, bad
				}
			}
			rows = append(rows, name)
		case "ipv4_routes":
			if len(f) != 11 || !validName(f[0]) {
				return nil, bad
			}
			for i, v := range f[1:] {
				base := 10
				if i == 0 || i == 1 || i == 2 || i == 6 {
					base = 16
				}
				if _, err := strconv.ParseUint(v, base, 64); err != nil {
					return nil, bad
				}
			}
			rows = append(rows, strings.Join(f, " "))
		case "ipv6_routes":
			if len(f) != 10 || !validName(f[9]) {
				return nil, bad
			}
			for i, v := range f[:9] {
				if i == 0 || i == 2 || i == 4 {
					if len(v) != 32 {
						return nil, bad
					}
					if _, err := hex.DecodeString(v); err != nil {
						return nil, bad
					}
				} else if _, err := strconv.ParseUint(v, 16, 32); err != nil {
					return nil, bad
				}
			}
			rows = append(rows, strings.Join(f, " "))
		}
	}
	return rows, nil
}
