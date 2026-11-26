package durable

import (
	"crypto/ed25519"
	"errors"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const maxFrameBytes = 4 + maxRecord + ed25519.SignatureSize
const maxEntryBytes = 34 + 512

func keepSize(file *os.File, offset, length int64) error {
	return unix.Fallocate(int(file.Fd()), unix.FALLOC_FL_KEEP_SIZE, offset, length)
}

func (w *Witness) closingDelta(r record, c change) int64 {
	switch r.Op {
	case "enroll", "claim":
		return 1
	case "release":
		return -1
	case "retire":
		if w.slots[c.target.slot].state.SessionKeyID != "" {
			return -2
		}
		return -1
	}
	return 0
}

// Ordinary work must leave room for every outstanding Release and Retire.
// Closing operations may consume the last reserved pair. Replay rebuilds the
// obligation count, including for stores created before preallocation.
func (w *Witness) reserve(r record, c change, frameBytes int64) error {
	remaining := w.closingRecords + w.closingDelta(r, c)
	if remaining == 0 && r.Op != "retire" && r.Op != "release" {
		remaining = 1
	}
	for _, part := range []struct {
		file  *os.File
		end   *int64
		bytes int64
	}{
		{w.journal, &w.journalSpace, frameBytes + remaining*maxFrameBytes},
		{w.lockFile, &w.indexSpace, (1 + remaining) * maxEntryBytes},
	} {
		info, err := part.file.Stat()
		if err != nil {
			return err
		}
		end := info.Size() + part.bytes
		start := max(info.Size(), *part.end)
		if end <= start {
			continue
		}
		if err = w.allocate(part.file, start, end-start); err != nil {
			if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) || errors.Is(err, syscall.EFBIG) {
				return errors.Join(&CapacityError{}, err)
			}
			return w.poison(err)
		}
		*part.end = end
	}
	return nil
}

// Only the writer that observed the failed append knows the pre-write EOF.
// Replay never truncates evidence. A failed index append leaves a complete
// pending journal frame that can be proved and signed during owner recovery.
func (w *Witness) appendBytes(file *os.File, write func([]byte) (int, error), data []byte) error {
	before, err := file.Stat()
	if err != nil {
		return err
	}
	n, err := write(data)
	if n == len(data) && err == nil {
		return nil
	}
	if err == nil {
		err = io.ErrShortWrite
	}
	repair, openErr := openFile(w.dir, file.Name(), syscall.O_RDWR)
	if openErr != nil {
		return errors.Join(err, openErr)
	}
	truncateErr := repair.Truncate(before.Size())
	return errors.Join(err, truncateErr, repair.Sync(), repair.Close(), w.dir.Sync())
}
