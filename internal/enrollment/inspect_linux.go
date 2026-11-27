package enrollment

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"

	"ataniq/internal/assignment"
	"ataniq/internal/assignment/wire"
	"ataniq/internal/checkpoint"
)

// PendingForRestart restores an advance only if its record was appended and
// the request could have reached the witness. It leaves the signed stage in
// place for Start to reconcile after a challenged read.
func PendingForRestart(root FileRoot, bindingPath string, slot checkpoint.Slot, epoch uint64, home, witness ed25519.PublicKey) (*checkpoint.Advance, error) {
	f, err := root.lock(nil)
	if err != nil {
		return nil, err
	}
	defer f.close()
	raw, err := f.stage()
	if err != nil || raw == nil {
		return nil, err
	}
	r, identity, err := f.readLedger()
	if err != nil {
		return nil, err
	}
	var receipt checkpoint.Receipt
	if bindingPath != "" {
		b, err := ReadBinding(bindingPath)
		if err != nil {
			return nil, err
		}
		if b.RootPath != root.Path || b.Identity != identity {
			return nil, refuse(IdentityMismatch, nil)
		}
		receipt = b.Receipt
	} else {
		raw, err := f.receipt()
		if err != nil {
			return nil, err
		}
		var genesis completedRecord
		if exactJSON(raw, &genesis) == nil {
			receipt, err = unpackReceipt(genesis.Receipt)
		} else {
			err = exactJSON(raw, &receipt)
		}
		if err != nil {
			return nil, refuse(Interrupted, err)
		}
	}
	ack, err := checkpoint.VerifyReceipt(witness, receipt)
	if err != nil || ack.State.Slot != slot || ack.State.Epoch != epoch {
		return nil, refuse(IdentityMismatch, err)
	}
	homeID, err := checkpoint.KeyID(home)
	if err != nil || ack.State.HomeKeyID != homeID {
		return nil, refuse(IdentityMismatch, err)
	}
	header, err := ack.State.Header.Bytes()
	if err != nil {
		return nil, refuse(IdentityMismatch, err)
	}
	verifier, err := wire.NewVerifier(ack.State.Header, home)
	if err != nil {
		return nil, refuse(IdentityMismatch, err)
	}
	ledger, last, err := replay(r, header, verifier)
	if err != nil {
		return nil, err
	}
	if err := f.check(); err != nil {
		return nil, err
	}
	var p staged
	return p.classifyStage(f, raw, last, ledger.Checkpoint(), ack.State, receipt, identity)
}

// ReadLocalStatus takes a read-only snapshot. A reservation also counts as a
// prepared request after an interrupted Prepare. Only ResumePrepared repairs it.
// A nil head denotes a root in which no ledger has been published.
func ReadLocalStatus(root FileRoot, home ed25519.PublicKey, binding *Binding) (prepared bool, head *assignment.Checkpoint, err error) {
	f, err := root.openRoot()
	if err != nil {
		return false, nil, err
	}
	defer f.close()
	for _, name := range []string{preparedName, reservationName} {
		raw, err := f.readOptional(name)
		if err != nil {
			return false, nil, err
		}
		if raw != nil {
			prepared = true
			break
		}
	}
	r, identity, err := f.readLedger()
	if binding == nil && errors.Is(err, os.ErrNotExist) {
		return prepared, nil, f.checkPath()
	}
	if err != nil {
		return prepared, nil, err
	}
	if binding != nil && binding.Identity != identity {
		return prepared, nil, refuse(IdentityMismatch, nil)
	}
	reader := bufio.NewReaderSize(r, 64<<10)
	raw, err := readFrame(reader)
	if err != nil {
		return prepared, nil, err
	}
	var header struct{ Header assignment.Header }
	if err := json.Unmarshal(raw, &header); err != nil {
		return prepared, nil, refuse(Corrupt, err)
	}
	if binding != nil && binding.Header != header.Header {
		return prepared, nil, refuse(IdentityMismatch, nil)
	}
	verifier, err := wire.NewVerifier(header.Header, home)
	if err != nil {
		return prepared, nil, refuse(IdentityMismatch, err)
	}
	// Keep the header while the buffered reader reuses its frame buffer.
	hdr := bytes.Clone(raw)
	ledger, _, err := replayEvents(reader, hdr, verifier)
	if err != nil {
		return prepared, nil, err
	}
	if err := f.check(); err != nil {
		return prepared, nil, err
	}
	checkpoint := ledger.Checkpoint()
	return prepared, &checkpoint, nil
}
