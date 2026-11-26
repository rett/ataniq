package durable

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	cp "ataniq/internal/checkpoint"
)

// Each frame is a big-endian uint32 length, canonical JSON and a detached
// Ed25519 signature. Genesis binds the configured identity. Later records
// link its chain by hashing the previous frame signature.
const journalDomain = "ataniq.checkpoint.durable.v1\x00"
const maxRecord = 2*cp.MaxFrame + 4096

type record struct {
	Seq                uint64    `json:"seq,string"`
	HomeKeyID          string    `json:"home_key_id,omitempty"`
	ReservedSettlement bool      `json:"reserved_settlement,omitempty"`
	Previous           string    `json:"previous"`
	Op                 string    `json:"op"`
	Identity           *Identity `json:"identity,omitempty"`
	Slot               *cp.Slot  `json:"slot,omitempty"`
	ID                 *cp.ID    `json:"id,omitempty"`
	Body               []byte    `json:"body,omitempty"`
	HomeProof          []byte    `json:"home_proof,omitempty"`
	SessionProof       []byte    `json:"session_proof,omitempty"`
}

func readPart(in io.Reader, b []byte, atBoundary bool) error {
	_, err := io.ReadFull(in, b)
	if errors.Is(err, io.ErrUnexpectedEOF) || (!atBoundary && errors.Is(err, io.EOF)) {
		return ErrTorn
	}
	return err
}
func decode(in io.Reader, pin ed25519.PublicKey) (record, [32]byte, error) {
	var r record
	var digest [32]byte
	var prefix [4]byte
	if err := readPart(in, prefix[:], true); err != nil {
		return r, digest, err
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n == 0 || n > maxRecord {
		return r, digest, ErrCorrupt
	}
	data := make([]byte, int(n)+ed25519.SignatureSize)
	if err := readPart(in, data, false); err != nil {
		return r, digest, err
	}
	b, sig := data[:n], data[n:]
	if !ed25519.Verify(pin, append([]byte(journalDomain), b...), sig) || json.Unmarshal(b, &r) != nil {
		return r, digest, ErrCorrupt
	}
	canonicalBody, err := canonical(r)
	if err != nil || !bytes.Equal(b, canonicalBody) {
		return r, digest, ErrCorrupt
	}
	return r, sha256.Sum256(sig), nil
}

func (w *Witness) commit(r record, c change) (cp.Receipt, error) {
	r.Previous = hex.EncodeToString(w.previous[:])
	body, err := canonical(r)
	if err != nil || len(body) > maxRecord {
		return cp.Receipt{}, &cp.Reason{Kind: cp.InvalidRequest}
	}
	sig := w.signRecord(append([]byte(journalDomain), body...))
	digest := sha256.Sum256(sig)
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	frame = append(frame, body...)
	frame = append(frame, sig...)
	if err = w.after("before-append"); err != nil {
		return cp.Receipt{}, errors.Join(cp.ErrUnavailable, err)
	}
	if err := w.reserve(r, c, int64(len(frame))); err != nil {
		return cp.Receipt{}, err
	}
	err = w.appendBytes(w.journal, w.writeFrame, frame)
	if err == nil {
		err = w.after("appended")
	}
	if err == nil {
		err = w.syncJournal()
	}
	var ack []byte
	if err == nil {
		ack, err = w.finish(r, c, digest)
	}
	if err != nil {
		// A short write, sync error or lost acknowledgement can leave a complete
		// transition on disk. No subsequent call may attest to the old index.
		return cp.Receipt{}, errors.Join(cp.ErrUnknownOutcome, w.poison(err))
	}
	w.seq, w.previous = r.Seq, digest
	reply := w.apply(r, c, ack)
	if r.Op == "settle" {
		return cp.Receipt{}, cp.ErrNotApplied
	}
	return reply, nil
}

func (w *Witness) syncJournal() error {
	if err := w.syncFile(w.journal); err != nil {
		return err
	}
	if err := w.after("journal-synced"); err != nil {
		return err
	}
	if err := w.syncFile(w.dir); err != nil {
		return err
	}
	return w.after("directory-synced")
}

// Only called after the transition and its directory have been synced. The
// companion entry binds the exact receipt to the frame. Its own sync makes
// the signature recoverable and detects a later clean cut of the journal.
func (w *Witness) finish(r record, c change, digest [32]byte) ([]byte, error) {
	var sig []byte
	if c.ack != nil {
		var err error
		sig, err = w.sign(c.ack)
		if err != nil {
			return nil, err
		}
		if err = verifyAck(w, r, c, sig); err != nil {
			return nil, err
		}
		if err = w.after("ack-signed"); err != nil {
			return nil, err
		}
	}
	entry := binary.BigEndian.AppendUint16(digest[:], uint16(len(sig)))
	entry = append(entry, sig...)
	if err := w.appendBytes(w.lockFile, w.writeEntry, entry); err != nil {
		return nil, err
	}
	if err := w.after("receipt-appended"); err != nil {
		return nil, err
	}
	if err := w.syncFile(w.lockFile); err != nil {
		return nil, err
	}
	if err := w.after("receipt-synced"); err != nil {
		return nil, err
	}
	if err := w.syncFile(w.dir); err != nil {
		return nil, err
	}
	return sig, w.after("receipt-directory-synced")
}

func verifyAck(w *Witness, r record, c change, sig []byte) error {
	if c.ack == nil {
		if len(sig) != 0 {
			return ErrCorrupt
		}
		return nil
	}
	return cp.MatchReceipt(w.pin, w.identity.Epoch, r.Op, r.Body, cp.Receipt{Body: c.ack, Signature: sig})
}

func readEntry(in io.Reader, digest [32]byte) ([]byte, error) {
	var header [34]byte
	if err := readPart(in, header[:], true); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(header[32:])
	if !bytes.Equal(header[:32], digest[:]) || n > 512 {
		return nil, ErrCorrupt
	}
	sig := make([]byte, n)
	return sig, readPart(in, sig, false)
}

// Rewrite the exact bytes replay validated. O_APPEND would defeat pwrite's
// offset on Linux. Keep the original index descriptor and its flock open.
func (w *Witness) rewriteTail(name string, tail []byte) error {
	f, err := openFile(w.dir, name, syscall.O_RDWR)
	if err != nil {
		return err
	}
	size, err := f.Stat()
	if err == nil && len(tail) > 0 {
		_, err = f.WriteAt(tail, size.Size()-int64(len(tail)))
	}
	return errors.Join(err, f.Close())
}

func (w *Witness) proveTail(frame, entry []byte) error {
	if err := w.arm(); err != nil {
		return err
	}
	if err := w.rewriteTail(journalName, frame); err != nil {
		return w.poison(err)
	}
	if err := w.rewriteTail(lockName, entry); err != nil {
		return w.poison(err)
	}
	for _, f := range []*os.File{w.journal, w.lockFile, w.dir} {
		if err := w.syncFile(f); err != nil {
			return w.poison(err)
		}
	}
	return nil
}

// A zero length followed solely by zeros can be the unwritten end of a
// newly extended file. Scan it in bounded chunks, only at an unindexed tail.
func classifyFrameError(err error, prefix []byte, frames, entries *bufio.Reader) error {
	zeroLength := errors.Is(err, ErrCorrupt) && len(prefix) == 4 && binary.BigEndian.Uint32(prefix) == 0
	if !errors.Is(err, ErrTorn) && !zeroLength {
		return err
	}
	if _, e := entries.Peek(1); e != io.EOF {
		if e != nil {
			return e
		}
		return ErrCorrupt
	}
	if zeroLength {
		var b [4096]byte
		for {
			n, e := frames.Read(b[:])
			if len(bytes.Trim(b[:n], "\x00")) != 0 {
				return ErrCorrupt
			}
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
		}
	}
	return ErrTorn
}

func (w *Witness) replay(homeID string) error {
	frames, entries := bufio.NewReader(w.journal), bufio.NewReader(w.lockFile)
	var frame, entry bytes.Buffer
	for {
		if _, err := frames.Peek(1); err == io.EOF {
			if _, err := entries.Peek(1); err != io.EOF {
				if err != nil {
					return err
				}
				return ErrCorrupt
			}
			if w.seq == 0 {
				return ErrTorn
			}
			return w.proveTail(frame.Bytes(), entry.Bytes())
		}
		frame.Reset()
		r, digest, err := decode(io.TeeReader(frames, &frame), w.pin)
		if err != nil {
			return fmt.Errorf("frame %d: %w", w.seq+1, classifyFrameError(err, frame.Bytes(), frames, entries))
		}
		if r.Seq != w.seq+1 || r.Previous != hex.EncodeToString(w.previous[:]) {
			return ErrCorrupt
		}
		var c change
		if w.seq == 0 {
			if r.Op != "create" || r.ReservedSettlement || r.Identity == nil || r.Body != nil || r.HomeProof != nil || r.SessionProof != nil || r.ID != nil || r.Slot != nil {
				return ErrCorrupt
			}
			if *r.Identity != w.identity || r.HomeKeyID != homeID {
				return ErrIdentity
			}
		} else {
			if r.Op == "create" || r.HomeKeyID != "" || r.Identity != nil {
				return ErrCorrupt
			}
			c, _, err = w.prepare(r, true)
			if err != nil {
				return errors.Join(ErrCorrupt, err)
			}
		}
		var sig []byte
		if _, err = entries.Peek(1); err == nil {
			entry.Reset()
			sig, err = readEntry(io.TeeReader(entries, &entry), digest)
		}
		if err == io.EOF {
			// Only one complete, unacknowledged transition may be pending. Inspect
			// the entire suffix before writing anything, including its first byte.
			if _, err := frames.Peek(1); err != io.EOF {
				if err != nil {
					return err
				}
				return ErrCorrupt
			}
			if err = w.proveTail(frame.Bytes(), entry.Bytes()); err != nil {
				return err
			}
			sig, err = w.finish(r, c, digest)
			if err != nil {
				return w.poison(err)
			}
			w.apply(r, c, sig)
			w.seq, w.previous = r.Seq, digest
			return nil
		}
		if errors.Is(err, ErrTorn) {
			if _, e := frames.Peek(1); e == nil {
				return ErrCorrupt
			} else if e != io.EOF {
				return e
			}
		}
		if err != nil {
			return err
		}
		if err := verifyAck(w, r, c, sig); err != nil {
			return errors.Join(ErrCorrupt, err)
		}
		w.apply(r, c, sig)
		w.seq, w.previous = r.Seq, digest
	}
}
