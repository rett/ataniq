package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"ataniq/internal/exchange/safefs"
)

// absClean checks that a path given as a flag is absolute and clean.
func absClean(flagName, p string) error {
	if p == "" {
		return usagef("--%s is required", flagName)
	}
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return usagef("--%s must be an absolute, clean path", flagName)
	}
	if strings.ContainsRune(p, 0) {
		return usagef("--%s must not hold a NUL byte", flagName)
	}
	return nil
}

// ownerUID returns the owner of a file from its Sys value. It reads the
// field by name so that this file needs no system package: the value is a
// *syscall.Stat_t on Linux.
func ownerUID(fi fs.FileInfo) (uint32, bool) {
	v := reflect.ValueOf(fi.Sys())
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0, false
	}
	f := v.FieldByName("Uid")
	if !f.IsValid() || !f.CanUint() {
		return 0, false
	}
	return uint32(f.Uint()), true
}

// openFolder opens the folder at p without following a symlink in the last
// name and returns what the open handle says about it.
func openFolder(flagName, p string) (fs.FileInfo, error) {
	root, err := safefs.OpenRoot(p)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, usagef("--%s: the folder does not exist", flagName)
		case errors.Is(err, safefs.ErrEscape):
			return nil, usagef("--%s: not a real folder (a symlink is refused)", flagName)
		}
		return nil, usagef("--%s: cannot open the folder: %v", flagName, err)
	}
	defer root.Close()
	fi, err := root.Lstat(".")
	if err != nil {
		return nil, usagef("--%s: cannot read the folder: %v", flagName, err)
	}
	if !fi.IsDir() {
		return nil, usagef("--%s: not a folder", flagName)
	}
	return fi, nil
}

// checkState checks the state root: a real folder that the caller owns and
// that nobody else can write.
func checkState(state string) error {
	if err := absClean("state", state); err != nil {
		return err
	}
	fi, err := openFolder("state", state)
	if err != nil {
		return err
	}
	if uid, ok := ownerUID(fi); !ok || int(uid) != os.Geteuid() {
		return usagef("--state: the folder must belong to the calling user")
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return usagef("--state: the folder must not be writable by its group or by others")
	}
	return nil
}

// owned checks a folder of the owner that leads must not reach, such as the
// records folder or the work folder: absolute, clean, existing, a real
// folder, and neither the state root nor inside it. The check is made on the
// names and on the names with their symlinks resolved.
func owned(flagName, p, state string) error {
	if err := absClean(flagName, p); err != nil {
		return err
	}
	if _, err := openFolder(flagName, p); err != nil {
		return err
	}
	return notInState(flagName, p, state)
}

// separate refuses two owner folders that are the same or one inside the
// other. The sandbox of a verify works in the work folder, and the records
// folder must not be one that a lead's sandbox can reach. The check is made
// on the names as given and on the names with their symlinks resolved. Both
// paths must exist.
func separate(recordsFlag, records, workFlag, work string) error {
	realRecords, err := filepath.EvalSymlinks(records)
	if err != nil {
		return usagef("--%s: cannot resolve the path: %v", recordsFlag, err)
	}
	realWork, err := filepath.EvalSymlinks(work)
	if err != nil {
		return usagef("--%s: cannot resolve the path: %v", workFlag, err)
	}
	for _, r := range []string{records, realRecords} {
		for _, w := range []string{work, realWork} {
			if within(r, w) || within(w, r) {
				return usagef("--%s and --%s must be separate folders, neither inside the other: the sandbox works in --%s", recordsFlag, workFlag, workFlag)
			}
		}
	}
	return nil
}

// notInState refuses a path that is the state root or inside it. The path
// must exist.
func notInState(flagName, p, state string) error {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return usagef("--%s: cannot resolve the path: %v", flagName, err)
	}
	realState, err := filepath.EvalSymlinks(state)
	if err != nil {
		return usagef("--state: cannot resolve the path: %v", err)
	}
	for _, a := range []string{p, real} {
		for _, b := range []string{state, realState} {
			if within(b, a) {
				return usagef("--%s must not be the state root or inside it: the state holds folders that leads can read or write", flagName)
			}
		}
	}
	return nil
}

// within reports whether child is parent or lies below it.
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
