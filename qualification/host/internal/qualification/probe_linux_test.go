//go:build linux && amd64

package qualification

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fact(t *testing.T, o Observation, name string) Fact {
	t.Helper()
	for _, f := range o.Facts {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("missing fact %s: %+v", name, o)
	return Fact{}
}
func requireKnown(t *testing.T, o Observation, name, value string) {
	t.Helper()
	f := fact(t, o, name)
	if f.State != "known" || f.Value == nil || *f.Value != value {
		t.Fatalf("%s: %+v", name, f)
	}
}

type fixtureMetadata struct {
	data        map[string][]byte
	fail        bool
	descriptors []fdMetadata
}

func (m fixtureMetadata) read(key string) ([]byte, error) {
	b, ok := m.data[key]
	if !ok || m.fail {
		return nil, errors.New("fixture read unavailable")
	}
	return b, nil
}
func (m fixtureMetadata) namespace(kind string) (string, error) {
	if m.fail {
		return "", errors.New("fixture namespace unavailable")
	}
	return "1:2", nil
}
func (m fixtureMetadata) fds() ([]fdMetadata, error) {
	if m.fail {
		return nil, errors.New("fixture descriptors unavailable")
	}
	return m.descriptors, nil
}

func inventoryFixture() fixtureMetadata {
	status := ""
	for _, name := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		status += name + ":\t0000000000000000\n"
	}
	return fixtureMetadata{data: map[string][]byte{
		"status":      []byte(status),
		"interfaces":  []byte("Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n lo: 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"),
		"ipv4_routes": []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n"),
		"ipv6_routes": []byte("00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000000 00000000 00000000 lo\n"),
	}, descriptors: []fdMetadata{{Number: 100000, Device: 1, Inode: 2, Mode: syscall.S_IFREG}}}
}

func TestInventoryOwnedMetadata(t *testing.T) {
	m := inventoryFixture()
	lookup := func(name string) (string, bool) {
		if name == "HOME" {
			return "private-value-that-must-not-appear", true
		}
		if name == "ATANIQ_FIXTURE_MARKER" {
			return "", true
		}
		return "", false
	}
	o := inventory(request(CaseInventory), m, lookup)
	if err := o.Validate(); err != nil || o.Status != "observed" {
		t.Fatal(o, err)
	}
	for _, name := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		requireKnown(t, o, name, "0000000000000000")
	}
	requireKnown(t, o, "env.HOME", "true")
	requireKnown(t, o, "env.ATANIQ_FIXTURE_MARKER", "true")
	requireKnown(t, o, "env.SSH_AUTH_SOCK", "false")
	requireKnown(t, o, "interfaces.count", "1")
	requireKnown(t, o, "interfaces.0", "lo")
	requireKnown(t, o, "ipv4_routes.count", "0")
	requireKnown(t, o, "fds.count", "1")
	if !strings.Contains(*fact(t, o, "fds.0").Value, "fd=100000 ") {
		t.Fatal("sparse high FD was lost")
	}
	if strings.Contains(wire(t, o), "private-value") {
		t.Fatal("environment value leaked")
	}
	m.fail = true
	o = inventory(request(CaseInventory), m, lookup)
	if o.Status != "inconclusive" {
		t.Fatal("unavailable data observed")
	}
	for _, name := range []string{"CapEff", "namespace.net", "interfaces", "ipv4_routes", "ipv6_routes", "fds"} {
		f := fact(t, o, name)
		if f.State != "unknown" || f.Value != nil {
			t.Fatal("unavailable defaulted to known", f)
		}
	}
	m = inventoryFixture()
	m.data["status"] = []byte("CapEff:\t0000000000000000\n")
	o = inventory(request(CaseInventory), m, lookup)
	if o.Status != "inconclusive" || fact(t, o, "CapAmb").State != "missing" {
		t.Fatal("missing capability defaults", o)
	}
	for _, data := range []string{"CapEff: 0\n", "CapEff: 0000000000000000\nCapEff: 0000000000000000\n", strings.Repeat("x", MaxLineBytes+1)} {
		if _, state := capability([]byte(data), "CapEff"); state != "unknown" {
			t.Fatal("bad capability state", state)
		}
	}
	for _, kind := range []string{"interfaces", "ipv4_routes", "ipv6_routes"} {
		for _, data := range []string{"", "garbage\n", strings.Repeat("x", MaxLineBytes+1) + "\n"} {
			if _, err := networkRows(kind, []byte(data)); err == nil {
				t.Fatalf("%s accepted malformed metadata", kind)
			}
		}
	}
	m = inventoryFixture()
	m.descriptors = make([]fdMetadata, MaxEntries+1)
	o = inventory(request(CaseInventory), m, lookup)
	if o.Status != "inconclusive" || fact(t, o, "fds").State != "unknown" {
		t.Fatal("excessive descriptors not unknown")
	}
}

func TestFakeFileOwnedFixtures(t *testing.T) {
	_, dir := ownedRoot(t)
	put(t, filepath.Join(dir, "fake"), []byte("private synthetic contents"))
	r := request(CaseFakeFile)
	r.Payload = &Payload{File: &NamePayload{Name: "fake"}}
	o := Probe(r, Config{FixtureRoot: dir})
	if o.Status != "observed" {
		t.Fatal(o)
	}
	requireKnown(t, o, "opened", "true")
	if strings.Contains(wire(t, o), "private synthetic contents") {
		t.Fatal("file contents leaked")
	}
	r.Payload.File.Name = "missing"
	o = Probe(r, Config{FixtureRoot: dir})
	if o.Status != "observed" || o.Errno == nil || *o.Errno != int(syscall.ENOENT) {
		t.Fatal("missing errno", o)
	}
	requireKnown(t, o, "stage", "lstat")
	if len(o.Facts) != 1 {
		t.Fatal("absent path implied a later operation", o)
	}
	r.Payload.File.Name = "fake"
	if Probe(r, Config{}).Status != "error" {
		t.Fatal("implicit fixture root accepted")
	}
	if err := os.Symlink("fake", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	r.Payload.File.Name = "alias"
	if Probe(r, Config{FixtureRoot: dir}).Status != "inconclusive" {
		t.Fatal("symlink fixture accepted")
	}
}

func TestFakeFDIdentityBeforeRead(t *testing.T) {
	_, dir := ownedRoot(t)
	path := filepath.Join(dir, "fake")
	contents := []byte("synthetic marker")
	put(t, path, contents)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	r := request(CaseFakeFD)
	p := &FDPayload{Name: "fake", FD: pointer(int(f.Fd())), Device: pointer(uint64(st.Dev)), Inode: pointer(st.Ino), Regular: pointer(true), SHA256: hex.EncodeToString(digest[:])}
	r.Payload = &Payload{FD: p}
	if _, err = f.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	o := Probe(r, Config{})
	if o.Status != "observed" {
		t.Fatal(o)
	}
	requireKnown(t, o, "digest_match", "true")
	if offset, _ := f.Seek(0, io.SeekCurrent); offset != 3 {
		t.Fatal("Pread changed descriptor offset")
	}
	p.SHA256 = strings.Repeat("0", 64)
	requireKnown(t, Probe(r, Config{}), "digest_match", "false")
	p.Inode = pointer(st.Ino + 1)
	o = Probe(r, Config{})
	if o.Status != "inconclusive" {
		t.Fatal(o)
	}
	requireKnown(t, o, "identity_match", "false")
	p.Inode = pointer(st.Ino)
	r.MaxBytes = 1
	if Probe(r, Config{}).Status != "inconclusive" {
		t.Fatal("read bound not enforced")
	}
	r.MaxBytes = 4096
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var dst syscall.Stat_t
	syscall.Fstat(int(d.Fd()), &dst)
	p.FD = pointer(int(d.Fd()))
	p.Device = pointer(uint64(dst.Dev))
	p.Inode = pointer(dst.Ino)
	if Probe(r, Config{}).Status != "inconclusive" {
		t.Fatal("directory FD read")
	}
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(pair[0])
	defer syscall.Close(pair[1])
	for _, fd := range []int{int(readEnd.Fd()), pair[0]} {
		var nonregular syscall.Stat_t
		if err := syscall.Fstat(fd, &nonregular); err != nil {
			t.Fatal(err)
		}
		p.FD = pointer(fd)
		p.Device = pointer(uint64(nonregular.Dev))
		p.Inode = pointer(nonregular.Ino)
		o = Probe(r, Config{})
		if o.Status != "inconclusive" {
			t.Fatal("nonregular descriptor accepted", o)
		}
		requireKnown(t, o, "identity_match", "false")
	}
	for _, fd := range []int{0, 1, 2, MaxFakeFD + 1} {
		p.FD = pointer(fd)
		if Probe(r, Config{}).Status != "error" {
			t.Fatal("invalid FD reached syscall")
		}
	}
	// A closed owned descriptor yields EBADF; no arbitrary host FD is inspected.
	closed, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fd := int(closed.Fd())
	closed.Close()
	p.FD = &fd
	o = Probe(r, Config{})
	if o.Errno == nil || *o.Errno != int(syscall.EBADF) {
		t.Fatal(o)
	}
}

func TestFakeUnixOwnedFixture(t *testing.T) {
	_, dir := ownedRoot(t)
	path := filepath.Join(dir, "socket")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Skipf("owned Unix fixture unavailable: %v", err)
	}
	defer l.Close()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	r := request(CaseFakeUnix)
	r.MaxDurationMS = 1000
	r.Payload = &Payload{Unix: &UnixPayload{Name: "socket", Device: pointer(uint64(st.Dev)), Inode: pointer(st.Ino), UID: pointer(st.Uid), Nonce: "0123456789abcdef"}}
	done := make(chan error, 1)
	go func() {
		c, err := l.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		b := make([]byte, len("ataniq-fixture-v1 "+r.Payload.Unix.Nonce+"\n"))
		_, err = io.ReadFull(c, b)
		if err == nil {
			_, err = c.Write(b)
		}
		done <- err
	}()
	o := Probe(r, Config{FixtureRoot: dir})
	if o.Status != "observed" {
		t.Fatal(o)
	}
	requireKnown(t, o, "nonce_match", "true")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r.Payload.Unix.Inode = pointer(st.Ino + 1)
	if Probe(r, Config{FixtureRoot: dir}).Status != "inconclusive" {
		t.Fatal("wrong endpoint accepted")
	}
	r.Payload.Unix.Name = "absent"
	o = Probe(r, Config{FixtureRoot: dir})
	if o.Errno == nil || *o.Errno != int(syscall.ENOENT) {
		t.Fatal("missing socket lost errno", o)
	}
}

func TestFakeUnixMissingFixture(t *testing.T) {
	_, dir := ownedRoot(t)
	r := request(CaseFakeUnix)
	r.Payload = &Payload{Unix: &UnixPayload{Name: "absent", Device: pointer(uint64(0)), Inode: pointer(uint64(0)), UID: pointer(uint32(0)), Nonce: "01234567"}}
	o := Probe(r, Config{FixtureRoot: dir})
	if o.Status != "observed" || o.Errno == nil || *o.Errno != int(syscall.ENOENT) {
		t.Fatal("missing socket lost errno", o)
	}
	requireKnown(t, o, "stage", "lstat")
	if len(o.Facts) != 1 {
		t.Fatal("absent socket implied connect", o)
	}
	r.Payload.Unix.Inode = pointer(uint64(1))
	if socketIdentity(fakeInfo{mode: os.ModeSocket}, r.Payload.Unix) {
		t.Fatal("unexpected synthetic endpoint identity accepted")
	}
}

func TestFutureCasesUnsupported(t *testing.T) {
	for _, c := range []Case{"FS1", "FS2", "FS3", "FS4", "US1", "US2", "US3", "FD1", "FD2", "PR1", "PR2", "PR3", "NS1", "KR1", "N1", "N2", "N3", "R1", "R2", "R3", "R4", "L1", "L2"} {
		o := Probe(request(c), Config{})
		if o.Status != "unsupported" || o.Reason == "" || len(o.Facts) != 0 {
			t.Fatal(c, o)
		}
	}
	if request(Case("typo")).Validate() == nil {
		t.Fatal("unknown case accepted")
	}
	// Counts are entry counts, never a maximum descriptor-number scan.
	m := inventoryFixture()
	m.descriptors = []fdMetadata{{Number: MaxFakeFD + 100}}
	o := inventory(request(CaseInventory), m, os.LookupEnv)
	if !strings.Contains(*fact(t, o, "fds.0").Value, strconv.Itoa(MaxFakeFD+100)) {
		t.Fatal("high sparse descriptor mislabelled absent")
	}
}

func TestEnumerateOwnedSparseFDNames(t *testing.T) {
	_, dir := ownedRoot(t)
	for _, name := range []string{"3", "1000000"} {
		put(t, filepath.Join(dir, name), nil)
	}
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// A fixture directory may coincidentally use fd 3. These are fake names,
	// so put the low entry above its descriptor number before enumeration.
	if d.Fd() == 3 {
		if err := os.Rename(filepath.Join(dir, "3"), filepath.Join(dir, "4")); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int]bool{}
	rows, err := enumerateFDs(d, func(fd int, st *syscall.Stat_t) error {
		seen[fd] = true
		st.Ino = uint64(fd)
		st.Mode = syscall.S_IFREG
		return nil
	})
	if err != nil || len(rows) != 2 || !seen[1000000] {
		t.Fatal("sparse enumeration", rows, seen, err)
	}
	for i := 0; i < MaxEntries+1; i++ {
		put(t, filepath.Join(dir, strconv.Itoa(2000000+i)), nil)
	}
	d2, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	called := false
	if _, err := enumerateFDs(d2, func(int, *syscall.Stat_t) error { called = true; return nil }); err == nil || called {
		t.Fatal("entry cap not applied before metadata operations")
	}
}
