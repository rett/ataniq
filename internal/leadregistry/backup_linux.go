package leadregistry

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LegacyFileName holds the exact pre-migration bytes, including a torn tail.
const LegacyFileName = FileName + ".v1"

// The caller holds the original's exclusive lock. Confirm a durable backup
// before binding the original name. Never overwrite an earlier backup.
func saveLegacyJournal(dir int, original *os.File) error {
	if _, err := original.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// A crash during copying may have left only this private staging file.
	const stage = "." + LegacyFileName + ".tmp"
	if err := unix.Unlinkat(dir, stage, 0); err != nil && err != unix.ENOENT {
		return err
	}
	fd, err := unix.Openat(dir, LegacyFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	switch err {
	case nil:
		saved := os.NewFile(uintptr(fd), filepath.Join(filepath.Dir(original.Name()), LegacyFileName))
		defer saved.Close()
		if err := checkJournalFile(dir, fd, saved.Name()); err != nil {
			return err
		}
		return confirmLegacyCopy(dir, original, saved)
	case unix.ENOENT:
		// Publish a complete copy under the backup name in one rename.
	default:
		return err
	}
	fd, err = unix.Openat(dir, stage, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(dir, stage, 0)
	saved := os.NewFile(uintptr(fd), stage)
	defer saved.Close()
	if err := saved.Chmod(0600); err != nil {
		return err
	}
	if _, err := io.Copy(saved, original); err != nil {
		return err
	}
	if err := saved.Sync(); err != nil {
		return err
	}
	if err := unix.Renameat2(dir, stage, dir, LegacyFileName, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	return syncFD(dir)
}

func confirmLegacyCopy(dir int, original, saved *os.File) error {
	st, err := saved.Stat()
	if err != nil {
		return err
	}
	if st.Mode().Perm() != 0600 {
		return fmt.Errorf("%w: expected a 0600 backup", ErrInsecure)
	}
	want, got := sha256.New(), sha256.New()
	if _, err := io.Copy(want, original); err != nil {
		return err
	}
	if _, err := io.Copy(got, saved); err != nil {
		return err
	}
	if !bytes.Equal(want.Sum(nil), got.Sum(nil)) {
		return errors.New("existing backup differs from the journal; move the backup offline before retrying init")
	}
	if err := saved.Sync(); err != nil {
		return err
	}
	return syncFD(dir)
}
