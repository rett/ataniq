package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const retiredMarker = "retired"

var errRetired = errors.New("home retired: keep this ledger as evidence; use a fresh state root, identity-init and enrollment at the next epoch")

// Use the binding directory itself as the cooperating lock so even a
// pre-enrollment listener needs no writable lock file. An exclusive retire
// cannot slip between checking the marker and opening or mutating the home.
func lockInstallation(binding string, exclusive bool) (*os.File, error) {
	if filepath.Base(binding) == retiredMarker {
		return nil, errors.New("binding: retired is reserved for the retirement marker")
	}
	parent, err := directory(filepath.Dir(binding), false)
	if err != nil {
		return nil, fmt.Errorf("binding directory: %w", err)
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	d := os.NewFile(uintptr(fd), parent.Name())
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	if err = unix.Flock(fd, mode|unix.LOCK_NB); err != nil {
		d.Close()
		return nil, fmt.Errorf("home installation busy: stop the active home command and retry: %w", err)
	}
	return d, nil
}

// The external marker survives a rollback. Mark the retired root and seed
// path as well so changing or omitting "binding" cannot reactivate either.
func localRetirementMarkers(c config) []string {
	return []string{filepath.Join(c.State, retiredMarker), c.HomeKey + ".retired"}
}

func isRetired(c config) (bool, error) {
	paths := localRetirementMarkers(c)
	if bindingOutsideState(c) {
		paths = append(paths, filepath.Join(filepath.Dir(c.Binding), retiredMarker))
	}
	for _, path := range paths {
		present, err := exists(path)
		if present || err != nil {
			return present, err
		}
	}
	return false, nil
}

func bindingOutsideState(c config) bool {
	return c.Binding != "" && c.Binding != c.State && !strings.HasPrefix(c.Binding, c.State+"/")
}

func retire(c config, out io.Writer) error {
	if c.Binding == "" {
		return errors.New("binding: retire requires the protected installation directory")
	}
	if !bindingOutsideState(c) {
		return errors.New("binding: retirement marker must be outside the ledger root")
	}
	d, err := lockInstallation(c.Binding, true)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := writeRetiredMarker(d, retiredMarker); err != nil {
		return err
	}
	for _, path := range localRetirementMarkers(c) {
		parent, err := directory(filepath.Dir(path), true)
		if err != nil {
			return err
		}
		err = writeRetiredMarker(parent, filepath.Base(path))
		if err = errors.Join(err, parent.Close()); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(out, "retired=true")
	return err
}

func writeRetiredMarker(d *os.File, name string) error {
	fd := int(d.Fd())
	n, err := unix.Openat(fd, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	var marker *os.File
	switch {
	case err == nil:
		marker = os.NewFile(uintptr(n), name)
		err = marker.Chmod(0600)
	case errors.Is(err, unix.EEXIST):
		// A retry finishes syncing a marker left by an interrupted retire.
		marker, err = openRegular(d, name, true)
	}
	if marker != nil {
		defer marker.Close()
	}
	if err != nil {
		return fmt.Errorf("retired marker: %w", err)
	}
	st, err := marker.Stat()
	if err != nil {
		return err
	}
	if st.Size() != 0 {
		return errors.New("unexpected retired marker contents")
	}
	if err := marker.Sync(); err != nil {
		return err
	}
	return syncDirectory(fd)
}
