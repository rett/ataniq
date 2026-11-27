package enrollment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	reservationName = "enrollment.creating"
	preparedName    = "enrollment.prepared"
)

// Build the whole owner-only file before adding its name. The anonymous
// inode disappears on close or crash before link, leaving the name reusable.
func (f *fileLedger) publish(name string, contents func(syscall.Stat_t) ([]byte, error)) (err error) {
	dir := int(f.dir.Fd())
	fd, err := unix.Openat(dir, ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { err = errors.Join(err, file.Close()) }()
	if err = file.Chmod(0600); err != nil {
		return err
	}
	var st syscall.Stat_t
	if err = syscall.Fstat(fd, &st); err != nil {
		return err
	}
	data, err := contents(st)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	// Following our own open descriptor works without CAP_DAC_READ_SEARCH.
	if err = unix.Linkat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", fd), dir, name, unix.AT_SYMLINK_FOLLOW); err != nil {
		return err
	}
	return f.dir.Sync()
}

func (f *fileLedger) createLedger(p Prepared) error {
	if _, err := f.dir.Readdirnames(1); err != io.EOF {
		if err != nil {
			return err
		}
		return refuse(Occupied, errors.New("enrollment requires an empty root"))
	}
	// Reserve the future ledger inode and the signed request in one durable
	// operation. Even a crash before the second file leaves both recoverable.
	err := f.publish(reservationName, func(st syscall.Stat_t) ([]byte, error) {
		pin := f.identity
		pin.LedgerDevice, pin.LedgerInode = uint64(st.Dev), st.Ino
		return json.Marshal(preparedRecord{p, pin})
	})
	if err != nil {
		return err
	}
	if err = after(f.fault, "prepare.reserved"); err != nil {
		return err
	}
	_, err = f.resumePrepared()
	return err
}

func (f *fileLedger) prepared() ([]byte, error) { return f.readOptional(preparedName) }

func (f *fileLedger) resumePrepared() (preparedRecord, error) {
	var rec preparedRecord
	raw, err := f.prepared()
	if err != nil {
		return rec, err
	}
	install := raw == nil
	if install {
		raw, err = f.readOptional(reservationName)
		if err != nil {
			return rec, err
		}
		if raw == nil {
			return rec, refuse(Unknown, errors.New("no prepared enrollment"))
		}
	}
	if err = exactJSON(raw, &rec); err != nil {
		return rec, err
	}
	q, err := rec.Request.request()
	if err != nil {
		return rec, err
	}
	if rec.Identity.RootDevice != f.identity.RootDevice || rec.Identity.RootInode != f.identity.RootInode {
		return rec, refuse(IdentityMismatch, nil)
	}
	scratch, st, err := f.open(reservationName, syscall.O_RDWR)
	if errors.Is(err, os.ErrNotExist) && !install {
		if err = checkGenesis(f, rec.Identity, q.HeaderBytes); err != nil {
			return rec, err
		}
		return rec, f.dir.Sync()
	}
	if err != nil {
		return rec, err
	}
	defer scratch.Close()
	if rec.Identity.LedgerDevice != uint64(st.Dev) || rec.Identity.LedgerInode != st.Ino {
		return rec, refuse(IdentityMismatch, nil)
	}
	if install {
		if err = f.publish(preparedName, func(syscall.Stat_t) ([]byte, error) { return raw, nil }); err != nil {
			return rec, err
		}
		if err = after(f.fault, "prepare.pinned"); err != nil {
			return rec, err
		}
	} else if err = f.dir.Sync(); err != nil {
		return rec, err
	}
	// The durable copy now owns the request. Only the pinned reservation
	// may be rewritten during recovery. Never write to a named ledger.
	if err = scratch.Truncate(0); err != nil {
		return rec, err
	}
	if _, err = scratch.Write(q.HeaderBytes); err != nil {
		return rec, err
	}
	if err = scratch.Sync(); err != nil {
		return rec, err
	}
	if err = after(f.fault, "prepare.genesis-synced"); err != nil {
		return rec, err
	}
	fd := int(f.dir.Fd())
	if err = unix.Renameat2(fd, reservationName, fd, ledgerName, unix.RENAME_NOREPLACE); err != nil {
		return rec, err
	}
	// Atomic publication makes creation and the complete header visible
	// together. Preserve the existing fault points at that named boundary.
	if err = after(f.fault, "header.created"); err != nil {
		return rec, err
	}
	if err = after(f.fault, "header.written"); err != nil {
		return rec, err
	}
	if err = scratch.Sync(); err != nil {
		return rec, err
	}
	if err = after(f.fault, "header.file-synced"); err != nil {
		return rec, err
	}
	if err = f.dir.Sync(); err != nil {
		return rec, err
	}
	if err = after(f.fault, "header.dir-synced"); err != nil {
		return rec, err
	}
	return rec, checkGenesis(f, rec.Identity, q.HeaderBytes)
}

func (f *fileLedger) complete(raw []byte) error {
	err := f.publish(receiptName, func(syscall.Stat_t) ([]byte, error) { return raw, nil })
	if err != nil {
		return err
	}
	return after(f.fault, "enroll.receipt-synced")
}
func (f *fileLedger) clearPrepared() error {
	// A retry may have found the receipt after a failed directory sync.
	if err := f.dir.Sync(); err != nil {
		return err
	}
	if err := syscall.Unlinkat(int(f.dir.Fd()), preparedName); err != nil {
		return err
	}
	if err := f.dir.Sync(); err != nil {
		return err
	}
	return after(f.fault, "enroll.stage-cleared")
}

// WritePrepared exclusively publishes the canonical request and returns the
// SHA-256 of the exact bytes the owner should verify after transfer.
func WritePrepared(path string, p Prepared) (string, error) {
	raw, err := EncodePrepared(p)
	if err != nil {
		return "", err
	}
	if err := publishDocument(path, raw); err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
