//go:build linux && amd64

package qualification

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"ataniq/internal/systemd"
)

// HostConfig is an explicit qualification subject selection, never discovery of
// installed developer tools. Runtime commands require a separate CLI opt-in.
type HostConfig struct {
	Version           int    `json:"version"`
	Campaign          string `json:"campaign"`
	Driver            string `json:"driver"`
	Probe             string `json:"probe"`
	Bwrap             string `json:"bwrap"`
	BPF               string `json:"bpf"`
	Systemctl         string `json:"systemctl"`
	SystemdRun        string `json:"systemd_run"`
	Role              string `json:"role"`
	RoleID            string `json:"role_id"`
	RunID             string `json:"run_id"`
	RequestID         string `json:"request_id"`
	Case              string `json:"case"`
	DirectControl     string `json:"direct_control,omitempty"`
	ReviewedBPF       bool   `json:"reviewed_bpf"`
	DescriptorProfile string `json:"descriptor_profile,omitempty"`
	Busctl            string `json:"busctl,omitempty"`
}

const HostProfile = "bwrap-static-probe-v1"
const FakeFD = 512
const BPFFD = 513

// Fixed stage/code fields identify the rejected subject without echoing bytes.
type hostFailure struct {
	Stage, Code string
	Cause       error
}

func (e *hostFailure) Error() string { return e.Stage + ": " + hostReason(e.Cause) }
func (e *hostFailure) Unwrap() error { return e.Cause }

// FileIdentity is the generic file identity; the alias keeps the JSON shape of
// state files and reports.
type FileIdentity = systemd.FileIdentity

type Subject struct {
	Path     string       `json:"path"`
	Identity FileIdentity `json:"identity"`
	SHA256   string       `json:"sha256"`
	ELF      ELFMetadata  `json:"elf"`
}

type ELFMetadata struct {
	Interpreter               string `json:"interpreter"`
	Needed                    string `json:"needed"`
	RPath                     string `json:"rpath"`
	RunPath                   string `json:"runpath"`
	DependencyClosureComplete bool   `json:"dependency_closure_complete"`
}

func identity(fi os.FileInfo) FileIdentity { return systemd.IdentityOf(fi) }

func absolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && validText(path, 511) && !strings.ContainsAny(path, " %\\\"'")
}

// Open each component without following links. Callers additionally require a
// frozen, trusted owned campaign; no path API makes hostile concurrent mutation safe.
func openAbsolute(path string, flags int) (*os.File, error) {
	if !absolute(path) {
		return nil, errors.New("invalid absolute path")
	}
	d, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, p := range parts {
		f := syscall.O_RDONLY | syscall.O_DIRECTORY
		if i == len(parts)-1 {
			f = flags
		}
		n, e := syscall.Openat(d, p, f|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		syscall.Close(d)
		if e != nil {
			return nil, e
		}
		d = n
	}
	return os.NewFile(uintptr(d), path), nil
}

func ownedDir(path string) (*os.File, FileIdentity, error) {
	f, err := openAbsolute(path, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return nil, FileIdentity{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, FileIdentity{}, err
	}
	id := identity(fi)
	if id.UID != uint32(os.Getuid()) || fi.Mode().Perm() != 0700 {
		f.Close()
		return nil, FileIdentity{}, errors.New("directory must be owned mode 0700")
	}
	return f, id, nil
}

func subject(path string, executable, static, owned bool, limit int64) (Subject, []byte, error) {
	f, err := openAbsolute(path, syscall.O_RDONLY)
	if err != nil {
		return Subject{}, nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Subject{}, nil, err
	}
	if err = checkRegular(fi, limit); err != nil {
		return Subject{}, nil, err
	}
	id := identity(fi)
	if fi.Mode().Perm()&0022 != 0 || owned && id.UID != uint32(os.Getuid()) || executable && fi.Mode().Perm()&0111 == 0 {
		return Subject{}, nil, errors.New("untrusted subject permissions")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) != fi.Size() {
		return Subject{}, nil, errors.New("subject read failed")
	}
	after, err := f.Stat()
	if err != nil || !same(fi, after) {
		return Subject{}, nil, errors.New("subject changed")
	}
	if static {
		e, err := elf.NewFile(bytes.NewReader(b))
		if err != nil {
			return Subject{}, nil, errors.New("static ELF subject required")
		}
		meta, err := launcherELF(e)
		if err != nil || !meta.DependencyClosureComplete {
			return Subject{}, nil, errors.New("unsupported: static Linux amd64 workload required")
		}
	}
	h := sha256.Sum256(b)
	return Subject{Path: path, Identity: id, SHA256: hex.EncodeToString(h[:])}, b, nil
}

func chainSubject(name, path string, owned bool) (Subject, []byte, error) {
	s, b, err := subject(path, name != "bpf", name == "driver" || name == "probe", owned, 64<<20)
	if err != nil || name != "bwrap" {
		return s, b, err
	}
	e, err := elf.NewFile(bytes.NewReader(b))
	if err != nil || e.Machine != elf.EM_X86_64 || e.Class != elf.ELFCLASS64 {
		return Subject{}, nil, errors.New("unsupported: ELF64 amd64 bwrap required")
	}
	meta, err := launcherELF(e)
	if err != nil {
		return Subject{}, nil, err
	}
	s.ELF = meta
	return s, b, nil
}

func DecodeHostConfig(r io.Reader) (HostConfig, error) {
	var c HostConfig
	if err := decode(r, MaxInputBytes, &c); err != nil {
		return c, err
	}
	return c, c.validate()
}

func ReadHostConfig(path string) (HostConfig, error) {
	f, err := openAbsolute(path, syscall.O_RDONLY)
	if err != nil {
		return HostConfig{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return HostConfig{}, err
	}
	if checkRegular(fi, MaxInputBytes) != nil || identity(fi).UID != uint32(os.Getuid()) || fi.Mode().Perm() != 0600 {
		return HostConfig{}, errors.New("owned mode 0600 configuration required")
	}
	return DecodeHostConfig(f)
}

// Fixed host subject/property keys are the only extensions to the probe's
// canonical structural vocabulary. No case-folding or arbitrary map keys.
func decodeHostRecord(r io.Reader, a *Attempt) error {
	return decodeKeys(r, MaxInputBytes, a, func(s string) bool {
		return canonicalKey(s) || s == "systemd-run" || strings.Contains(","+entryServiceProperties+","+entryParentProperties+","+entryPolicyProperties+",", ","+s+",")
	})
}

func (c HostConfig) validate() error {
	if c.Case == R1EntryCase {
		if c.Role != "candidate" || c.DescriptorProfile != "" || c.DirectControl != "" || !absolute(c.Busctl) {
			return errors.New("fixed candidate r1-entry requires pinned busctl and no control/profile selectors")
		}
	} else if c.Busctl != "" {
		return errors.New("busctl is only an r1-entry subject")
	}
	if c.DescriptorProfile != "" && c.DescriptorProfile != FD1TypedProfile {
		return errors.New("unknown descriptor profile")
	}
	typed := c.DescriptorProfile == FD1TypedProfile
	if c.Version != 1 || !validHex(c.RunID) || !validHex(c.RequestID) || !validHex(c.RoleID) || c.Role != "direct" && c.Role != "candidate" && c.Role != "weakened-synthetic" || !c.ReviewedBPF {
		return errors.New("invalid host configuration")
	}
	seen := map[string]bool{}
	paths := []string{c.Campaign, c.Driver, c.Probe, c.Bwrap, c.BPF, c.Systemctl, c.SystemdRun}
	if c.Case == R1EntryCase {
		paths = append(paths, c.Busctl)
	}
	for _, p := range paths {
		if !absolute(p) {
			return errors.New("exact absolute subjects required")
		}
		if seen[p] {
			return errors.New("subject paths must be distinct")
		}
		seen[p] = true
		if p != c.Campaign && strings.HasPrefix(p, c.Campaign+"/") {
			return errors.New("subjects must be outside campaign")
		}
	}
	switch c.Case {
	case "inventory", "fake-file", "fake-fd", "hold", R1EntryCase:
	case "fake-dir-fd", "fake-pipe-fd", "fake-socket-fd":
		if !typed {
			return errors.New("typed descriptor case requires fd1-typed profile")
		}
	default:
		return errors.New("unsupported host case")
	}
	if typed && (c.Case == "hold" || c.Case == "fake-file") {
		return errors.New("unsupported typed descriptor case")
	}
	if c.Role == "weakened-synthetic" && (c.Case != "fake-fd" && c.Case != "inventory" && !typedFDCase(Case(c.Case)) || c.DirectControl != "") {
		return errors.New("weakened synthetic control requires owned FD or inventory fixture")
	}
	if c.DirectControl != "" && !attemptID(c.DirectControl) {
		return errors.New("invalid direct control")
	}
	if c.Role == "candidate" && (c.Case == "fake-fd" || typed) && c.DirectControl == "" {
		return errors.New("direct control required")
	}
	return nil
}

func attemptID(id string) bool { return bounded(id, 32, 32, isHex) }

func BubblewrapArgs(probe, fixture string) []string {
	return []string{"--unshare-all", "--unshare-user", "--uid", "0", "--gid", "0", "--die-with-parent", "--new-session", "--clearenv", "--cap-drop", "ALL",
		"--disable-userns", "--assert-userns-disabled", "--seccomp", fmt.Sprint(BPFFD),
		"--tmpfs", "/", "--dir", "/fixtures", "--dir", "/dev", "--dir", "/tmp",
		"--ro-bind", probe, "/probe", "--ro-bind", fixture, "/fixtures/fake-file",
		"--proc", "/proc", "--remount-ro", "/", "--chdir", "/", "--", "/probe", "--fixture-root", "/fixtures"}
}
