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
	n, err := w.writeFrame(frame)
	if n != len(frame) && err == nil {
		err = io.ErrShortWrite
	}
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
		w.stopped = true
		return cp.Receipt{}, errors.Join(cp.ErrUnknownOutcome, err)
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
	if _, err := w.lockFile.Write(entry); err != nil {
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

func (w *Witness) replay(homeID string) error {
	frames, entries := bufio.NewReader(w.journal), bufio.NewReader(w.lockFile)
	for {
		r, digest, err := decode(frames, w.pin)
		if err == io.EOF {
			if w.seq == 0 {
				return ErrTorn
			}
			if _, err := entries.Peek(1); err != io.EOF {
				if err != nil {
					return err
				}
				return ErrTorn
			}
			// A process crash can leave complete unsynced entries in the page cache.
			if err := w.arm(); err != nil {
				return err
			}
			if err := w.syncFile(w.journal); err != nil {
				return err
			}
			if err := w.syncFile(w.lockFile); err != nil {
				return err
			}
			return w.syncFile(w.dir)
		}
		if err != nil {
			return fmt.Errorf("frame %d: %w", w.seq+1, err)
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
		sig, err := readEntry(entries, digest)
		if err == io.EOF {
			// Only one complete, unacknowledged transition may be pending. Inspect
			// the entire suffix before writing anything, including its first byte.
			if _, err := frames.Peek(1); err != io.EOF {
				if err != nil {
					return err
				}
				return ErrCorrupt
			}
			if err = w.arm(); err != nil {
				return err
			}
			if err = w.syncJournal(); err != nil {
				return err
			}
			sig, err = w.finish(r, c, digest)
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
