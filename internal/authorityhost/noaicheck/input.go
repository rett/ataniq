package noaicheck

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// PathInput is the name of the incomplete hit for a file the program
// opens by name (a list of the release, a firewall listing) that is not a
// regular file: "HIT incomplete input <name>". Such a file is never read,
// so a FIFO without a writer or a device cannot hold the run.
const PathInput = "input"

// InputError is the error of a file opened by name (see OpenInput) that is
// not a regular file: a FIFO, a device, a socket, a folder or a link. Name
// is the name it was opened by.
type InputError struct {
	Name string
	Err  error
}

func (e *InputError) Error() string { return "input " + Field(e.Name) + ": " + e.Err.Error() }

func (e *InputError) Unwrap() error { return e.Err }

// Hit is the hit that reports the input: "incomplete input <name>".
func (e *InputError) Hit() Hit { return Hit{Kind: KindIncomplete, Path: PathInput, Detail: e.Name} }

// OpenInput opens the file name for reading when it is a regular file. Its
// type is looked at first, without following a final link, so that a FIFO
// or a device is never opened; the open neither follows a final link nor
// blocks, and the opened descriptor must be a regular file and the same
// file as the one looked at, so one swapped in between is refused, never
// read or waited on. A file that is not regular (a link included) is an
// *InputError; one that is not there or cannot be opened is the error of
// the lstat or the open.
func OpenInput(name string) (*os.File, error) {
	lfi, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !lfi.Mode().IsRegular() {
		return nil, &InputError{Name: name, Err: errNotRegular}
	}
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_NOCTTY, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, &InputError{Name: name, Err: errNotRegular} // a link put in its place
		}
		return nil, err
	}
	fi, err := f.Stat()
	switch {
	case err != nil:
		f.Close()
		return nil, err
	case !fi.Mode().IsRegular():
		f.Close()
		return nil, &InputError{Name: name, Err: errNotRegular}
	case !os.SameFile(lfi, fi):
		f.Close()
		return nil, &InputError{Name: name, Err: errChanged}
	}
	return f, nil
}

// openRoot opens the folder dir as an [os.Root] without ever blocking on
// it: the folder is first opened with O_DIRECTORY, which the kernel
// refuses for anything else before opening it (a FIFO without a writer
// would hold a plain open), and the root is then opened through that
// descriptor's /proc/self/fd entry, so it is the same folder. Without
// /proc the root is opened by name and must be the same folder as the
// descriptor.
func openRoot(dir string) (*os.Root, error) {
	d, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	r, err := os.OpenRoot("/proc/self/fd/" + strconv.FormatUint(uint64(d.Fd()), 10))
	if err == nil {
		return r, nil
	}
	if r, err = os.OpenRoot(dir); err != nil {
		return nil, err
	}
	want, err1 := d.Stat()
	got, err2 := r.Stat(".")
	if err1 != nil || err2 != nil || !os.SameFile(want, got) {
		r.Close()
		return nil, &fs.PathError{Op: "open", Path: dir, Err: errChanged}
	}
	return r, nil
}
