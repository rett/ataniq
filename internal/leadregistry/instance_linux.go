package leadregistry

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"ataniq/internal/instanceid"
	"ataniq/internal/leadrole"
	"golang.org/x/sys/unix"
)

// InstanceRefusal identifies a journal whose binding does not admit this root.
type InstanceRefusal string

func (r InstanceRefusal) Error() string { return string(r) }

const (
	ErrForeignInstance InstanceRefusal = "registry belongs to another authority or store incarnation"
	ErrUnbound         InstanceRefusal = "registry has no instance binding; run ataniqd init --state-dir DIR"
)

func journalHeader(b instanceid.Binding) *headerPayload {
	if b.Authority == "" {
		return &headerPayload{Format: formatName, Version: formatVersion}
	}
	return &headerPayload{Format: formatName, Version: boundFormatVersion, Authority: b.Authority, Incarnation: b.Incarnation}
}

func (h *headerPayload) match(b instanceid.Binding) error {
	if h.Version == boundFormatVersion && b.Authority == "" {
		return instanceid.ErrMissing
	}
	if h.Version == formatVersion && b.Authority != "" {
		return ErrUnbound
	}
	if h.Authority != b.Authority || h.Incarnation != b.Incarnation {
		return ErrForeignInstance
	}
	return nil
}

// ReadHeaderAt reads the existing registry header without locking or repairing
// the journal. A missing or empty journal has no header. V1 has a zero binding.
func ReadHeaderAt(root *os.File) (*instanceid.Binding, error) {
	dir, err := unix.Openat(int(root.Fd()), DirName, unix.O_RDONLY|dirFlags|unix.O_NOFOLLOW, 0)
	if err == unix.ENOENT {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	path := filepath.Join(root.Name(), DirName)
	if err := checkDir(dir, path); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(dir, FileName, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(path, FileName))
	defer f.Close()
	if err := checkJournalFile(dir, fd, f.Name()); err != nil {
		return nil, err
	}
	header, _, size, err := readInitHeader(bufio.NewReaderSize(f, MaxLineBytes+1))
	if err != nil || size == 0 {
		return nil, err
	}
	h := header.payload.(*headerPayload)
	return &instanceid.Binding{Authority: h.Authority, Incarnation: h.Incarnation}, nil
}

// A zero record denotes an empty file. The length includes the newline so
// migration can revisit the records after validating them.
func readInitHeader(rd *bufio.Reader) (record, [sha256.Size]byte, int64, error) {
	line, err := rd.ReadSlice('\n')
	if err == io.EOF && len(line) == 0 {
		return record{}, genesis, 0, nil
	}
	if err != nil {
		return record{}, genesis, 0, &CorruptError{Line: 1, Reason: "cannot read complete header"}
	}
	header, prev, err := decodeLine(genesis, 0, line[:len(line)-1])
	if err != nil || header.kind != kindHeader {
		return record{}, genesis, 0, &CorruptError{Line: 1, Reason: "invalid header"}
	}
	return header, prev, int64(len(line)), nil
}

// InitInstance initializes a private state root and binds its registry.
// The old journal stays locked throughout migration. A retry after a crash
// sees either the old journal or the fully written replacement.
func InitInstance(stateDir, registryName, label string) (instanceid.Identity, error) {
	var zero instanceid.Identity
	root, err := instanceid.OpenRoot(stateDir, true)
	if err != nil {
		return zero, err
	}
	defer root.Close()
	result, err := InitInstanceAt(root, registryName, label)
	return result.Identity, err
}

// InitResult describes the journal that ordinary init left in place.
type InitResult struct {
	instanceid.Identity
	Journal string // "created", "migrated" or "existing"
	Records uint64 // complete records migrated after the v1 header
}

// InitInstanceAt uses the state directory already pinned by the initializer.
func InitInstanceAt(root *os.File, registryName, label string) (InitResult, error) {
	var zero InitResult
	if !filepath.IsLocal(registryName) || filepath.Base(registryName) != registryName || registryName == "." {
		return zero, errors.New("expected a registry directory name")
	}
	fd, err := openSyncedChild(int(root.Fd()), registryName, FileName, unix.O_RDONLY|dirFlags|unix.O_NOFOLLOW, syncFD)
	if err != nil {
		return zero, err
	}
	defer unix.Close(fd)
	dir := filepath.Join(root.Name(), registryName)
	if err := checkDir(fd, dir); err != nil {
		return zero, err
	}
	f, err := openFile(fd, filepath.Join(dir, FileName))
	if err != nil {
		return zero, err
	}
	defer f.Close()
	rd := bufio.NewReaderSize(f, MaxLineBytes+1)
	header, prev, offset, err := readInitHeader(rd)
	if err != nil {
		return zero, err
	}
	result := InitResult{Journal: "created"}
	empty := offset == 0
	if !empty {
		h := header.payload.(*headerPayload)
		if h.Version == boundFormatVersion {
			id, err := instanceid.LoadAt(int(root.Fd()))
			if err != nil {
				return zero, err
			}
			if err := h.match(id.Binding); err != nil {
				return zero, err
			}
			return InitResult{Identity: id, Journal: "existing"}, syncFD(fd)
		}
		// Validate before publishing either identity file. Keep this same
		// descriptor locked through replacement, including the second read.
		r := Registry{prev: prev, roles: make(map[leadrole.Role]Entry)}
		for n := 2; ; n++ {
			recordLine, err := rd.ReadSlice('\n')
			if err == io.EOF {
				break // Normal Open owns recovery of an unfinished tail.
			}
			if err != nil {
				return zero, fmt.Errorf("read registry line %d: %w", n, err)
			}
			if err := r.replayLine(n, recordLine[:len(recordLine)-1]); err != nil {
				return zero, err
			}
		}
		result.Journal, result.Records = "migrated", r.seq
		if err := saveLegacyJournal(fd, f); err != nil {
			return zero, fmt.Errorf("retain %s: %w", LegacyFileName, err)
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return zero, err
		}
		rd.Reset(f)
	}
	id, err := instanceid.InitAt(root, label)
	if err != nil {
		return zero, err
	}
	at := header.at
	if empty {
		at = time.Now()
	}
	newHeader, sum, err := encodeLine(genesis, 0, at, kindHeader, leadrole.Role{}, journalHeader(id.Binding))
	if err != nil {
		return zero, err
	}
	if err := replaceJournal(fd, rd, newHeader, sum); err != nil {
		return zero, err
	}
	result.Identity = id
	return result, nil
}

// Rechain the original record bytes, including fields unknown to this build.
// Replay validates the original chain and transitions before publication.
func replaceJournal(fd int, rd *bufio.Reader, header []byte, sum [sha256.Size]byte) error {
	name := ".registry-" + rand.Text()
	tmp, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(tmp), name)
	defer f.Close()
	defer unix.Unlinkat(fd, name, 0)
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if err := unix.Flock(tmp, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	if _, err := f.Write(header); err != nil {
		return err
	}
	for n := 2; ; n++ {
		line, err := rd.ReadSlice('\n')
		if err == io.EOF {
			// Keep the unfinished tail for normal Open recovery.
			if _, err := f.Write(line); err != nil {
				return err
			}
			break
		}
		if err != nil {
			return fmt.Errorf("read registry line %d: %w", n, err)
		}
		end := len(line) - 1 - hashSuffixLen
		content := append(line[:end], '}')
		line, sum, err = sealLine(sum, content)
		if err != nil {
			return err
		}
		if _, err := f.Write(line); err != nil {
			return err
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := unix.Renameat(fd, name, fd, FileName); err != nil {
		return err
	}
	return syncFD(fd)
}
