// Package ownerlocal reads the owner's own local settings: small files in the
// owner's configuration folder, under ataniq/owner-local, that no lead, agent
// or seat can set through the daemon's wire. It is the Go standard library
// only, so that the daemon and the console can both import it.
//
// A file here is a claim of the owner's account and nothing more: a process
// that runs as the same user can write it. What the package does is refuse a
// file that is not what an owner writes by hand (a link, a device, a large
// file, a file others may write) and keep the file's content out of every
// error, so that a log line or a refusal never carries it.
package ownerlocal

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SeatOriginName is the file that holds the seat origin, the address of the
// approval app such as "https://app.example", under the owner-local folder.
const SeatOriginName = "seat-origin"

// maxFile is the largest file the package reads, in bytes.
const maxFile = 1024

// The errors of a read. Each text is fixed: neither the path nor anything of
// the file's content is in one.
var (
	// ErrNotRegular says the file is a link or is not a regular file.
	ErrNotRegular = errors.New("ownerlocal: the file is not a regular file")
	// ErrTooBig says the file is larger than 1 KiB.
	ErrTooBig = errors.New("ownerlocal: the file is larger than 1 KiB")
	// ErrWritable says the file can be written by its group or by others.
	ErrWritable = errors.New("ownerlocal: the file can be written by its group or by others")
	// ErrUnreadable says the file could not be read.
	ErrUnreadable = errors.New("ownerlocal: the file cannot be read")
)

// SeatOriginPath returns where the seat origin is read from: the folder named
// by XDG_CONFIG_HOME (when it is an absolute path), or .config in HOME,
// then ataniq/owner-local/seat-origin. It returns "" when neither is set.
func SeatOriginPath(getenv func(string) string) string {
	base := getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
		home := getenv("HOME")
		if !filepath.IsAbs(home) {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ataniq", "owner-local", SeatOriginName)
}

// SeatOrigin returns the seat origin: the first line of the file at
// [SeatOriginPath], with the space around it trimmed. It does not check what
// the value is; the caller does, since each place that uses an origin has its
// own rules.
//
// A file that does not exist, a file whose first line is empty and a
// configuration folder that cannot be found give "" and no error: no origin
// is configured. A file that is a link, is not a regular file, is larger than
// 1 KiB or can be written by its group or by others gives "" and an error of
// this package, and so does one that cannot be read. The value is never in an
// error.
func SeatOrigin(getenv func(string) string) (string, error) {
	path := SeatOriginPath(getenv)
	if path == "" {
		return "", nil
	}
	first, err := lstatChecked(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ErrUnreadable
	}
	defer f.Close()
	// What was opened is what was checked: a file swapped for another
	// between the two calls is refused.
	now, err := f.Stat()
	if err != nil || !os.SameFile(first, now) {
		return "", ErrUnreadable
	}
	if err := check(now); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return "", ErrUnreadable
	}
	if len(b) > maxFile {
		return "", ErrTooBig
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line), nil
}

// lstatChecked looks at the file without following a link and checks it. A
// missing file is returned as the error of fs.ErrNotExist.
func lstatChecked(path string) (fs.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		return nil, ErrUnreadable
	}
	if err := check(fi); err != nil {
		return nil, err
	}
	return fi, nil
}

// check holds the rules a file must meet before it is read.
func check(fi fs.FileInfo) error {
	switch {
	case !fi.Mode().IsRegular():
		return ErrNotRegular
	case fi.Size() > maxFile:
		return ErrTooBig
	case fi.Mode().Perm()&0o022 != 0:
		return ErrWritable
	}
	return nil
}
