package enrollment

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const ledgerName = "events.v1.jsonl"
const stageName = "advance.pending"
const receiptName = "advance.receipt"

// FileRoot names an existing, owner-protected directory under trusted
// ancestors. Every component is opened without following symlinks. Flock on
// the directory serializes cooperating enrollments, writers and starters.
type FileRoot struct {
	Path string
}

const directoryFlags = syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC

// Linux O_PATH is absent from the frozen syscall package on amd64.
const pathDirectoryFlags = 0x200000 | directoryFlags

func (root FileRoot) lock(fault Fault) (lockedLedger, error) {
	path, err := filepath.Abs(root.Path)
	if err != nil || root.Path == "" {
		return nil, refuse(Insecure, err)
	}
	flags := pathDirectoryFlags
	if path == "/" {
		flags = directoryFlags
	}
	fd, err := syscall.Open("/", flags, 0)
	if err != nil {
		return nil, refuse(Insecure, err)
	}
	l := &fileLedger{dir: os.NewFile(uintptr(fd), "/"), fault: fault}
	fail := func(err error) (lockedLedger, error) {
		return nil, errors.Join(err, l.close())
	}
	names := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	for i, name := range names {
		if i == len(names)-1 {
			flags = directoryFlags
		}
		next, err := syscall.Openat(int(l.dir.Fd()), name, flags, 0)
		if err != nil {
			return fail(refuse(Insecure, err))
		}
		l.parents = append(l.parents, l.dir)
		l.dir = os.NewFile(uintptr(next), name)
	}
	st, err := inspect(l.dir, true)
	if err != nil {
		return fail(err)
	}
	if err := syscall.Flock(int(l.dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fail(refuse(Occupied, err))
		}
		return fail(refuse(Insecure, err))
	}
	l.identity = Identity{RootDevice: uint64(st.Dev), RootInode: st.Ino}
	return l, nil
}

type fileLedger struct {
	parents     []*os.File
	dir, ledger *os.File
	identity    Identity
	size        int64
	changed     syscall.Timespec
	fault       Fault
}

func inspect(f *os.File, directory bool) (syscall.Stat_t, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return st, refuse(Insecure, err)
	}
	want := uint32(syscall.S_IFREG | 0600)
	if directory {
		want = syscall.S_IFDIR | 0700
	}
	if st.Mode != want || int(st.Uid) != os.Geteuid() || (!directory && st.Nlink != 1) {
		return st, refuse(Insecure, fmt.Errorf("unexpected owner, mode or link count on %s", f.Name()))
	}
	return st, nil
}

func (f *fileLedger) open(name string, flags int) (*os.File, syscall.Stat_t, error) {
	var st syscall.Stat_t
	fd, err := syscall.Openat(int(f.dir.Fd()), name, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, st, err
	}
	file := os.NewFile(uintptr(fd), name)
	if flags&(syscall.O_CREAT|syscall.O_EXCL) == syscall.O_CREAT|syscall.O_EXCL {
		if err := file.Chmod(0600); err != nil {
			return nil, st, errors.Join(err, file.Close())
		}
	}
	st, err = inspect(file, false)
	if err != nil {
		_ = file.Close()
		return nil, st, err
	}
	return file, st, nil
}

func (f *fileLedger) persist(file *os.File, data []byte, op string) error {
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := after(f.fault, op+".written"); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := after(f.fault, op+".file-synced"); err != nil {
		return err
	}
	if err := f.dir.Sync(); err != nil {
		return err
	}
	return after(f.fault, op+".dir-synced")
}

func (f *fileLedger) createFile(name string, data []byte, op string) (*os.File, error) {
	file, _, err := f.open(name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL)
	if err != nil {
		return nil, err
	}
	if err = after(f.fault, op+".created"); err == nil {
		err = f.persist(file, data, op)
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func (f *fileLedger) createLedger(header []byte) (Identity, error) {
	if names, err := f.dir.Readdirnames(1); err != io.EOF || len(names) != 0 {
		if err != nil {
			return Identity{}, err
		}
		return Identity{}, errors.New("enrollment requires an empty root")
	}
	file, err := f.createFile(ledgerName, header, "header")
	if err != nil {
		return Identity{}, err
	}
	f.ledger = file
	st, err := inspect(file, false)
	if err != nil {
		return Identity{}, err
	}
	f.size, f.changed = int64(len(header)), st.Ctim
	f.identity.LedgerDevice, f.identity.LedgerInode = uint64(st.Dev), st.Ino
	return f.identity, nil
}

func (f *fileLedger) readLedger() (io.Reader, Identity, error) {
	file, st, err := f.open(ledgerName, syscall.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		// A scratch registry is never a candidate for automatic conversion.
		legacy, _, e := f.open("registry.jsonl", syscall.O_RDONLY)
		if e == nil {
			_ = legacy.Close()
			return nil, Identity{}, refuse(Legacy, nil)
		}
		return nil, Identity{}, refuse(MissingHeader, err)
	}
	if err != nil {
		return nil, Identity{}, refuse(Insecure, err)
	}
	if f.ledger != nil {
		_ = f.ledger.Close()
	}
	f.ledger = file
	f.size, f.changed = st.Size, st.Ctim
	f.identity.LedgerDevice, f.identity.LedgerInode = uint64(st.Dev), st.Ino
	return file, f.identity, nil
}

// Check the name as well as the open descriptor across calls to the witness.
func (f *fileLedger) check() error {
	if _, err := inspect(f.dir, true); err != nil {
		return err
	}
	// Verify each named edge relative to its pinned parent. This also catches
	// replacement of an ancestor with a symlink back to the original tree.
	child := f.dir
	for i := len(f.parents) - 1; i >= 0; i-- {
		parent := f.parents[i]
		fd, err := syscall.Openat(int(parent.Fd()), child.Name(), pathDirectoryFlags, 0)
		if err != nil {
			return refuse(IdentityMismatch, err)
		}
		var held, named syscall.Stat_t
		err = syscall.Fstat(fd, &named)
		_ = syscall.Close(fd)
		if err != nil {
			return refuse(Insecure, err)
		}
		if err := syscall.Fstat(int(child.Fd()), &held); err != nil {
			return refuse(Insecure, err)
		}
		if held.Dev != named.Dev || held.Ino != named.Ino {
			return refuse(IdentityMismatch, nil)
		}
		child = parent
	}
	held, err := inspect(f.ledger, false)
	if err != nil {
		return err
	}
	if err := f.checkLedger(held); err != nil {
		return err
	}
	file, st, err := f.open(ledgerName, syscall.O_RDONLY)
	if err != nil {
		return refuse(Insecure, err)
	}
	defer func() { _ = file.Close() }()
	return f.checkLedger(st)
}

func (f *fileLedger) checkLedger(st syscall.Stat_t) error {
	if uint64(st.Dev) != f.identity.LedgerDevice || st.Ino != f.identity.LedgerInode || st.Size != f.size || st.Ctim != f.changed {
		return refuse(IdentityMismatch, nil)
	}
	return nil
}

// The base64-encoded record and checkpoint intent each have a 64 KiB limit.
// Include both encodings, the signature, key and JSON framing.
const stageLimit = 192 << 10

func (f *fileLedger) receipt() ([]byte, error) { return f.readOptional(receiptName) }
func (f *fileLedger) stage() ([]byte, error)   { return f.readOptional(stageName) }
func (f *fileLedger) readOptional(name string) ([]byte, error) {
	file, st, err := f.open(name, syscall.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, refuse(Insecure, err)
	}
	defer func() { _ = file.Close() }()
	if st.Size > stageLimit {
		return nil, refuse(Interrupted, fmt.Errorf("oversize %s", name))
	}
	b, err := io.ReadAll(io.LimitReader(file, stageLimit+1))
	if err != nil || int64(len(b)) != st.Size {
		return nil, refuse(Interrupted, err)
	}
	return b, nil
}

func (f *fileLedger) saveStage(b []byte) error {
	file, err := f.createFile(stageName, b, "stage")
	if err != nil {
		return err
	}
	return file.Close()
}

func (f *fileLedger) append(b []byte) error {
	file, st, err := f.open(ledgerName, syscall.O_APPEND|syscall.O_WRONLY)
	if err != nil {
		return err
	}
	if err := f.checkLedger(st); err != nil {
		_ = file.Close()
		return err
	}
	err = f.persist(file, b, "append")
	if err == nil {
		st, err = inspect(file, false)
		f.size += int64(len(b))
		f.changed = st.Ctim
		if err == nil {
			err = f.checkLedger(st)
		}
	}
	return errors.Join(err, file.Close())
}

// Keep one replacement slot. The intact stage can reconstruct the receipt
// after any interruption up to the rename and directory sync.
func (f *fileLedger) saveReceipt(raw []byte) error {
	const temp = receiptName + ".tmp"
	fd := int(f.dir.Fd())
	if err := syscall.Unlinkat(fd, temp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := f.createFile(temp, raw, "receipt")
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := syscall.Renameat(fd, temp, fd, receiptName); err != nil {
		return err
	}
	if err := after(f.fault, "receipt.renamed"); err != nil {
		return err
	}
	if err := f.dir.Sync(); err != nil {
		return err
	}
	return after(f.fault, "receipt.rename-synced")
}

func (f *fileLedger) clearStage() error {
	if err := syscall.Unlinkat(int(f.dir.Fd()), stageName); err != nil {
		return err
	}
	if err := after(f.fault, "stage.removed"); err != nil {
		return err
	}
	if err := f.dir.Sync(); err != nil {
		return err
	}
	return after(f.fault, "stage.removal-synced")
}

func (f *fileLedger) close() error {
	var err error
	if f.ledger != nil {
		err = f.ledger.Close()
	}
	for _, parent := range f.parents {
		err = errors.Join(err, parent.Close())
	}
	return errors.Join(err, f.dir.Close())
}
