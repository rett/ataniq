//go:build linux && ataniq_canary

package provision

import (
	"errors"
	"fmt"
	"io/fs"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/safefile"
)

// dirMode and the file modes provisioning gives. Every folder is 0700; a file
// the lead reads is 0600, the guard hook's executable 0500 and the
// managed-settings copy 0400.
const (
	dirMode = 0o700
)

// area is a folder opened by handle, with the absolute path it is known by in
// messages. Every lookup is beneath the handle and follows no symbolic link;
// nothing is looked up by its name from outside.
type area struct {
	root *safefile.Root
	base string // absolute path, for messages only
	log  func(string)
}

// at returns the absolute path of rel, for messages.
func (a *area) at(rel string) string {
	if rel == "." {
		return a.base
	}
	return a.base + "/" + rel
}

// owned reports whether the caller owns the entry.
func owned(info safefile.Info) bool { return info.UID == uint32(unix.Geteuid()) }

// checkFolder reports why info is not a folder provisioning accepts: a real
// folder of the caller with mode 0700 exactly. It returns "" for one that is.
func checkFolder(info safefile.Info) string {
	switch {
	case info.IsSymlink():
		return "is a symbolic link"
	case !info.IsDir():
		return "is not a folder"
	case !owned(info):
		return "is not owned by the caller"
	case info.Mode&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != dirMode:
		return fmt.Sprintf("has mode %04o, must be 0700", info.Mode&fs.ModePerm)
	}
	return ""
}

// lookFolder is the look before a folder is made: it returns whether rel
// exists, and an error when something is there that is not a folder
// provisioning accepts (a symbolic link in any component included).
func (a *area) lookFolder(rel string) (bool, error) {
	info, err := a.root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("%s: %w", a.at(rel), err)
	}
	if why := checkFolder(info); why != "" {
		return true, fmt.Errorf("%s: %s", a.at(rel), why)
	}
	return true, nil
}

// lookAbsent is the look before a file is made: nothing may be there, a
// symbolic link included.
func (a *area) lookAbsent(rel string) error {
	info, err := a.root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%s: %w", a.at(rel), err)
	}
	if info.IsSymlink() {
		return fmt.Errorf("%s: is a symbolic link", a.at(rel))
	}
	return fmt.Errorf("%s: exists already", a.at(rel))
}

// dir makes the folder rel with mode 0700, or keeps one that is there and is
// as provisioning would make it. Its parent must exist or be made by an
// earlier call: callers go top down so that each folder is checked and
// reported.
func (a *area) dir(rel string) error {
	exists, err := a.lookFolder(rel)
	if err != nil {
		return err
	}
	if exists {
		a.say("kept folder " + a.at(rel))
		return nil
	}
	if err := a.root.MkdirAll(rel, dirMode); err != nil {
		return fmt.Errorf("%s: %w", a.at(rel), err)
	}
	// MkdirAll gives the mode before the umask, and an existing folder keeps
	// its own: look at the result.
	info, err := a.root.Lstat(rel)
	if err != nil {
		return fmt.Errorf("%s: %w", a.at(rel), err)
	}
	if why := checkFolder(info); why != "" {
		return fmt.Errorf("%s: %s", a.at(rel), why)
	}
	a.say("made folder " + a.at(rel))
	return nil
}

// file makes the regular file rel with data and the mode, exclusively: it
// never replaces anything and never follows a link. The mode is checked on the
// result, because the umask may have cleared bits.
func (a *area) file(rel string, data []byte, mode fs.FileMode) error {
	if err := a.root.WriteFileExclusive(rel, data, mode); err != nil {
		return fmt.Errorf("%s: %w", a.at(rel), err)
	}
	info, err := a.root.Lstat(rel)
	if err != nil {
		return fmt.Errorf("%s: %w", a.at(rel), err)
	}
	switch {
	case !info.IsRegular() || info.Mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0:
		return fmt.Errorf("%s: is not a plain regular file", a.at(rel))
	case !owned(info):
		return fmt.Errorf("%s: is not owned by the caller", a.at(rel))
	case info.Mode&fs.ModePerm != mode:
		return fmt.Errorf("%s: has mode %04o, wanted %04o", a.at(rel), info.Mode&fs.ModePerm, mode)
	case info.Size != int64(len(data)):
		return fmt.Errorf("%s: holds %d bytes, wanted %d", a.at(rel), info.Size, len(data))
	}
	if err := a.root.Sync(rel); err != nil {
		return fmt.Errorf("%s: %w", a.at(rel), err)
	}
	a.say("made file " + a.at(rel))
	return nil
}

// empty reports that the folder rel holds nothing; a folder that is not there
// is empty. A name is returned for one that is not.
func (a *area) empty(rel string) (string, error) {
	sub, err := a.root.SubRoot(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("%s: %w", a.at(rel), err)
	}
	defer sub.Close()
	names, err := sub.ReadDirNames(".", 1024)
	if err != nil {
		return "", fmt.Errorf("%s: %w", a.at(rel), err)
	}
	if len(names) > 0 {
		return names[0], nil
	}
	return "", nil
}

func (a *area) say(line string) {
	if a.log != nil {
		a.log(line)
	}
}
