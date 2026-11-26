package assignment

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// These are canonical bytes for the in-memory model, independent of JSON
// framing. Every field has a fixed position. Integers and byte lengths are
// unsigned big-endian uint64s. Evidence order is part of request identity.
const recordLimit = 64 << 10

type encoding struct {
	b   []byte
	err error
}

func (w *encoding) number(n uint64) {
	if len(w.b)+8 > recordLimit {
		w.err = ErrLimit
		return
	}
	w.b = binary.BigEndian.AppendUint64(w.b, n)
}

func (w *encoding) strings(values ...string) {
	for _, s := range values {
		w.number(uint64(len(s)))
		if len(s) > recordLimit-len(w.b) {
			w.err = ErrLimit
			return
		}
		w.b = append(w.b, s...)
	}
}

func (w *encoding) head(h Head) {
	w.strings(h.Key.Home, h.Key.Project, h.Key.Role)
	w.number(h.Generation)
	w.number(h.Revision)
	w.strings(h.BindingID, h.TransferID, string(h.Phase))
}

func (w *encoding) command(c Command) {
	w.head(c.Expected)
	w.strings(string(c.Kind), c.TransferID, c.RequestID, c.Actor, c.Authorization)
	b := c.Candidate
	w.strings(b.Kind, b.ID, b.Authority, b.Store, b.Incarnation, b.Conversation, b.LaunchIntent)
	w.number(b.RoleEpoch)
	w.number(c.HoldStart)
	w.number(c.EarliestRetirement)
	w.number(uint64(len(c.Evidence)))
	for _, e := range c.Evidence {
		w.strings(e.Kind, e.Issuer, e.Subject, e.Revision, e.Digest, e.Outcome, e.Route)
		w.head(e.Scope)
		w.strings(e.TransferID, e.ScopeEventID)
		if w.err != nil {
			return
		}
	}
}

// public binds the predecessor, resulting row and private request commitment.
// The wire codec supplies its own signed envelope and schema.
func (w *encoding) public(e Event) {
	w.number(e.Schema)
	w.number(e.Sequence)
	w.strings(e.PreviousHash, e.RequestDigest, string(e.Command.Kind), e.Command.TransferID)
	w.head(e.Command.Expected)
	w.head(e.Result.Head)
	w.strings(e.Result.RequestID, e.Result.Disposition)
}

func (w *encoding) digest(domain string) (string, error) {
	if w.err != nil {
		return "", w.err
	}
	h := sha256.New()
	h.Write([]byte(domain + "\x00"))
	h.Write(w.b)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func requestDigest(c Command) (string, error) {
	var w encoding
	w.command(c)
	return w.digest("ataniq.assignment.request.v1")
}

func publicID(e Event) (string, error) {
	var w encoding
	w.public(e)
	return w.digest("ataniq.assignment.public.v1")
}

func ledgerDigest(e Event) (string, error) {
	var w encoding
	w.command(e.Command)
	w.public(e)
	w.strings(e.Result.EventID, e.Signature)
	return w.digest("ataniq.assignment.ledger.v1")
}
