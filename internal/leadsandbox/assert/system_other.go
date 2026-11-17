//go:build !linux

package assert

import (
	"errors"
	"time"
)

// DefaultMountInfoPath is the mount table of the calling process on Linux.
const DefaultMountInfoPath = "/proc/self/mountinfo"

// HostSystem is the real System. It is implemented on Linux only: on another
// system every method fails with errors.ErrUnsupported, so every check is
// not-checked.
type HostSystem struct {
	MountInfoPath string
}

var _ System = HostSystem{}

func (HostSystem) Lstat(string) (FileInfo, error)           { return FileInfo{}, errors.ErrUnsupported }
func (HostSystem) ReadFile(string, int) ([]byte, error)     { return nil, errors.ErrUnsupported }
func (HostSystem) Mounts() ([]Mount, error)                 { return nil, errors.ErrUnsupported }
func (HostSystem) Environ() []string                        { return nil }
func (HostSystem) Dial(string, string, time.Duration) error { return errors.ErrUnsupported }
func (HostSystem) LookupHost(string, time.Duration) ([]string, error) {
	return nil, errors.ErrUnsupported
}
func (HostSystem) Writable(string) (bool, error) { return false, errors.ErrUnsupported }
