package webpush

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// This is the one file of the package that touches the file system: the
// helpers that keep a VAPID key in a file. They follow the checks of the
// gate's identity files (internal/seatgate/files.go).

// maxKeyFile is the most a key file may hold.
const maxKeyFile = 16 << 10

// Errors of the key file helpers. Each is a fixed text, with no path in it.
var (
	// ErrExists is returned by [WriteVAPIDKeyFile] when the file is already
	// there: it never overwrites.
	ErrExists = errors.New("webpush: the VAPID key file already exists")
	// ErrUnsafe is returned when the folder or the file is a link, is not
	// what it should be, or is open to others: the key file must not be
	// accessible to group or other, and its folder must not be writable by
	// them.
	ErrUnsafe = errors.New("webpush: the VAPID key folder or file is not safe to use")
)

// WriteVAPIDKeyFile writes k to path, a new file of mode 0600. The folder of
// path must exist and be a real folder (not a link) that group and other
// cannot write. It never overwrites: a file already there, or a link in its
// place, is [ErrExists] and nothing is written.
func WriteVAPIDKeyFile(path string, k *VAPIDKey) error {
	data, err := k.MarshalPEM()
	if err != nil {
		return err
	}
	if err := checkDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return errors.New("webpush: cannot create the VAPID key file")
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(path)
		return errors.New("webpush: cannot write the VAPID key file")
	}
	return nil
}

// LoadVAPIDKeyFile reads the key at path. The folder must be a real folder
// that group and other cannot write, and the file a regular file, not a link
// (symbolic or hard), of at most 16 KiB, that group and other cannot read or
// write. Anything else is [ErrUnsafe]; a file that is not a key is [ErrKey].
func LoadVAPIDKeyFile(path string) (*VAPIDKey, error) {
	if err := checkDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	data, err := readSafe(path, 0o077)
	if err != nil {
		return nil, err
	}
	return ParseVAPIDKey(data)
}

// checkDir holds a folder to being a real folder (not a link) that group and
// other cannot write.
func checkDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return errors.New("webpush: the VAPID key folder cannot be read")
	}
	if !st.IsDir() || st.Mode()&fs.ModeSymlink != 0 || st.Mode().Perm()&0o022 != 0 {
		return ErrUnsafe
	}
	return nil
}

// readSafe reads a regular file that is not a link, symbolic or hard (its link
// count is 1), and has none of the mode bits in forbidden set. It opens first
// and checks the open file, so the check and the read are of the same file.
// The open does not wait (O_NONBLOCK), so a FIFO with no writer in place of
// the file is refused, not waited on for ever.
func readSafe(path string, forbidden fs.FileMode) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, ErrUnsafe
		}
		return nil, errors.New("webpush: cannot open the VAPID key file")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, errors.New("webpush: cannot read the VAPID key file")
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&forbidden != 0 || st.Size() > maxKeyFile {
		return nil, ErrUnsafe
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Nlink != 1 { // a second hard link
		return nil, ErrUnsafe
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil || len(data) > maxKeyFile {
		return nil, errors.New("webpush: cannot read the VAPID key file")
	}
	return data, nil
}
