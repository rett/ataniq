//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/sandboxnet"
)

// DefaultLingerDir is the login manager's folder of lingering marks: one empty
// file named after each user whose systemd instance lingers.
const DefaultLingerDir = "/var/lib/systemd/linger"

// SystemConfig is what the real System is given. The zero value of every field
// but RoleLock means the real thing.
type SystemConfig struct {
	// RoleLock is the lock the holder holds on its role, which assertion 9 asks
	// about. It is required: the sequence never takes it. Run asks it with the
	// layout role (Inputs.LayoutRole, for example "throwaway-one-lead"), not
	// with the role text of the daemon
	// ("throwaway-one/lead"): an adapter that compares the argument with its
	// own role text must compare it with the layout role, or assertion 9 fails.
	RoleLock assert.RoleLock
	// LingerDir overrides DefaultLingerDir (tests give a folder).
	LingerDir string
	// RunVersion asks a tool for its version line; nil means run the tool by its
	// absolute path with "--version", an empty environment, no input and a time
	// limit (tests replace it, so that no tool is started).
	RunVersion func(ctx context.Context, path string) (string, error)
}

// realSystem is the System over the packages of the lead sandbox.
type realSystem struct {
	cfg SystemConfig
}

var _ System = (*realSystem)(nil)

// NewSystem returns the real System. It reads and writes the host as the
// methods of System say, each by handle where the design asks for it, and
// starts nothing but, in ToolVersions, the two tools' version questions.
func NewSystem(cfg SystemConfig) (System, error) {
	if cfg.RoleLock == nil {
		return nil, errors.New("sandboxstart: the real system needs the role lock the holder holds")
	}
	if cfg.LingerDir == "" {
		cfg.LingerDir = DefaultLingerDir
	}
	return &realSystem{cfg: cfg}, nil
}

func (*realSystem) Getenv(name string) string { return os.Getenv(name) }

func (*realSystem) Now() time.Time { return time.Now() }

func (*realSystem) IDs() (uid, gid uint32) { return uint32(unix.Geteuid()), uint32(unix.Getegid()) }

func (*realSystem) Username() (string, error) {
	u, err := user.LookupId(strconv.Itoa(unix.Geteuid()))
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

func (*realSystem) CheckOwnClone(in profile.Inputs, uid uint32) (mountpoints.Report, error) {
	return mountpoints.CheckOwnClone(in, uid)
}

func (*realSystem) PrepareMountPoints(in profile.Inputs) error {
	return mountpoints.Prepare(in, mountpoints.PrepareOptions{NoLeadHasRun: true})
}

func (*realSystem) CheckMountPoints(in profile.Inputs) (mountpoints.Report, error) {
	return mountpoints.Check(in)
}

func (*realSystem) DeriveResolv() (sandboxnet.Result, error) {
	return sandboxnet.DeriveFromFS(sandboxnet.HostFS{})
}

func (*realSystem) ResolveSources(in profile.Inputs) (mountpoints.Resolution, error) {
	return mountpoints.ResolveSources(in, mountpoints.RealResolver)
}

func (*realSystem) HostHas(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (*realSystem) NewCanary() (*assert.Canary, error) { return assert.NewCanary() }

func (*realSystem) CheckBindSources(rows []mountpoints.ResolvedSource) mountpoints.Report {
	return mountpoints.CheckBindSources(rows)
}

func (*realSystem) Assert() assert.System { return assert.HostSystem{} }

func (s *realSystem) RoleLock() assert.RoleLock { return s.cfg.RoleLock }

func (s *realSystem) Lingering() assert.Lingering { return lingering{dir: s.cfg.LingerDir} }

// lingering reads the login manager's state: the user lingers when the folder
// holds a file of the user's name. A folder that does not exist is a question
// that cannot be answered (the host has no such login manager, or not that
// layout), not an answer of "no".
type lingering struct{ dir string }

// Enabled implements assert.Lingering.
func (l lingering) Enabled(name string) (bool, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return false, errors.New("no user name to ask about")
	}
	var st unix.Stat_t
	if err := unix.Stat(l.dir, &st); err != nil {
		return false, err
	}
	if err := unix.Lstat(filepath.Join(l.dir, name), &st); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, err
	}
	return st.Mode&unix.S_IFMT == unix.S_IFREG, nil
}
