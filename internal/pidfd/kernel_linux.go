package pidfd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

var errUnexpectedPoll = errors.New("unexpected pidfd poll result")

// absolute reports whether path is a clean absolute path of printable text
// without characters that the qualification subjects forbid.
func absolute(path string) bool {
	if !utf8.ValidString(path) || len(path) > 511 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	if strings.ContainsAny(path, " %\\\"'") {
		return false
	}
	return strings.IndexFunc(path, func(r rune) bool { return unicode.IsControl(r) || r == unicode.ReplacementChar }) < 0
}

// openAbsolute opens each component without following links.
func openAbsolute(path string) (*os.File, error) {
	if !absolute(path) {
		return nil, errors.New("invalid absolute path")
	}
	d, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, p := range parts {
		flags := unix.O_RDONLY | unix.O_DIRECTORY
		if i == len(parts)-1 {
			flags = unix.O_RDONLY
		}
		n, e := unix.Openat(d, p, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		unix.Close(d)
		if e != nil {
			return nil, e
		}
		d = n
	}
	return os.NewFile(uintptr(d), path), nil
}

// readKernel reads a small kernel file and fails when it is longer than cap.
func readKernel(path string, cap int64) ([]byte, error) {
	f, err := openAbsolute(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, cap+1))
	if err != nil || int64(len(b)) > cap {
		return nil, errors.New("kernel read failed or excessive")
	}
	return b, nil
}
