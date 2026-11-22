package seatgate

import (
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"
)

// This is the one file of the package that touches the file system: the
// helpers that keep an identity in a folder.

const (
	// CertFile and KeyFile are the names of the two files of an identity in
	// its folder.
	CertFile = "cert.pem"
	KeyFile  = "key.pem"

	// maxIdentityFile is the most a certificate or key file may hold.
	maxIdentityFile = 16 << 10
)

// Errors of the file helpers. Each is a fixed text, with no path in it.
var (
	// ErrExists is returned by [WriteIdentityFiles] when a file of the
	// identity is already there: it never overwrites.
	ErrExists = errors.New("seatgate: an identity file already exists")
	// ErrUnsafe is returned when a folder or file is a link, is not what it
	// should be, or is open to others: the key file must not be accessible to
	// group or other, and the folder must not be writable by them.
	ErrUnsafe = errors.New("seatgate: an identity folder or file is not safe to use")
)

// WriteIdentityFiles makes an identity named name and writes it to dir as
// [CertFile] and [KeyFile], both with mode 0600. It creates dir with mode
// 0700 if it is missing; an existing dir must be a real folder (not a link)
// that group and other cannot write. It never overwrites: a file already
// there is [ErrExists] and nothing is written. It returns the new identity,
// so the caller can show its fingerprint.
func WriteIdentityFiles(dir, name string) (Identity, error) {
	certPEM, keyPEM, err := GenerateIdentity(rand.Reader, time.Now(), name)
	if err != nil {
		return Identity{}, err
	}
	id, err := ParseIdentity(certPEM, keyPEM)
	if err != nil {
		return Identity{}, err
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Identity{}, errors.New("seatgate: cannot create the identity folder")
		}
	}
	if err := checkDir(dir); err != nil {
		return Identity{}, err
	}
	// The key first: without it a left certificate is harmless, while a
	// left key without its certificate would be a half identity. On a
	// failure after the first file, the files made here are removed.
	keyPath, certPath := dir+"/"+KeyFile, dir+"/"+CertFile
	if err := createExclusive(keyPath, keyPEM); err != nil {
		return Identity{}, err
	}
	if err := createExclusive(certPath, certPEM); err != nil {
		os.Remove(keyPath)
		return Identity{}, err
	}
	return id, nil
}

// LoadIdentity reads the identity in dir. The folder must be a real folder
// that group and other cannot write, and the two files must be regular files,
// not links, of at most 16 KiB; the key file must not be accessible to group
// or other and the certificate file must not be writable by them. Anything
// else is [ErrUnsafe].
func LoadIdentity(dir string) (Identity, error) {
	if err := checkDir(dir); err != nil {
		return Identity{}, err
	}
	keyPEM, err := readSafe(dir+"/"+KeyFile, 0o077)
	if err != nil {
		return Identity{}, err
	}
	certPEM, err := readSafe(dir+"/"+CertFile, 0o022)
	if err != nil {
		return Identity{}, err
	}
	return ParseIdentity(certPEM, keyPEM)
}

// checkDir holds a folder to being a real folder (not a link) that group and
// other cannot write.
func checkDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return errors.New("seatgate: the identity folder cannot be read")
	}
	if !st.IsDir() || st.Mode()&fs.ModeSymlink != 0 || st.Mode().Perm()&0o022 != 0 {
		return ErrUnsafe
	}
	return nil
}

// createExclusive makes path with mode 0600, failing if it exists (and so
// following no link) and writes data.
func createExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return errors.New("seatgate: cannot create an identity file")
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(path)
		return errors.New("seatgate: cannot write an identity file")
	}
	return nil
}

// readSafe reads a regular file that is not a link and has none of the mode
// bits in forbidden set. It opens first and checks the open file, so the check
// and the read are of the same file. The open does not wait (O_NONBLOCK), so a
// FIFO with no writer in place of the file is refused, not waited on for ever;
// the flag changes nothing for the read of a regular file.
func readSafe(path string, forbidden fs.FileMode) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, ErrUnsafe
		}
		return nil, errors.New("seatgate: cannot open an identity file")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, errors.New("seatgate: cannot read an identity file")
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&forbidden != 0 || st.Size() > maxIdentityFile {
		return nil, ErrUnsafe
	}
	data, err := io.ReadAll(io.LimitReader(f, maxIdentityFile+1))
	if err != nil || len(data) > maxIdentityFile {
		return nil, errors.New("seatgate: cannot read an identity file")
	}
	return data, nil
}
