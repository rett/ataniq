package assignment

import "errors"

// These independent in-memory fixtures stand in for the witness, broker and
// Qargi. Dropping a reply or restoring a participant never rewinds the home.
type startGate struct {
	local          Head
	projection     string
	qargiAvailable bool
}

func (g startGate) permits(home Ledger, claim Head) bool {
	row, ok := home.Row(claim.Key)
	return ok && g.qargiAvailable && g.local == claim && g.projection == row.PublicDigest && row.Permits(claim)
}

func recoverPublication(events []Event, pin Checkpoint, session bool) (Event, bool) {
	if !session || len(events) == 0 {
		return Event{}, false
	}
	l, err := recoverAt(events, pin)
	if err != nil {
		return Event{}, false
	}
	last := events[len(events)-1]
	result, ok := Lookup(l, last.Command.Expected.Key, last.Command.TransferID, last.Command.RequestID)
	return last, ok && result == last.Result
}

type objectStore map[string][]byte

func (s objectStore) resolves(e Evidence) bool {
	b, ok := s[e.Digest]
	return ok && sha256Hex(b) == e.Digest
}

type routeJournal struct {
	intent  Effect
	outcome Effect
	sealed  bool
	receipt Command
}

type routeBroker struct {
	journal routeJournal
	armed   Effect // ephemeral permission for the attempt that wrote the intent
}

func (b *routeBroker) begin(id Effect) bool {
	if b.journal.sealed || b.journal.intent != (Effect{}) {
		return false
	}
	b.journal.intent, b.armed = id, id
	return true
}
func (b *routeBroker) start(id Effect) bool {
	if b.journal.sealed || id.ID == "" || b.armed != id || b.journal.intent != id {
		return false
	}
	b.armed = Effect{}
	return true
}
func (r *routeJournal) fence(c Command) (Command, bool) {
	if !r.sealed || r.intent != r.outcome {
		return Command{}, false
	}
	if r.receipt.RequestID != "" {
		return r.receipt, true
	}
	r.receipt = c
	return r.receipt, true
}

// All members of a projection transaction are installed by assigning one
// value. Feature dispositions can be independently durable before that cut.
type projectionTxn struct {
	head    Head
	digest  string
	eventID string
	receipt Command
}

func (p projectionTxn) install(e Event, receipt Command) (projectionTxn, error) {
	signing, err := e.SigningBytes()
	if err != nil {
		return p, err
	}
	if err := (testKey{}).Verify(header, signing, e.Signature); err != nil {
		return p, err
	}
	signed, err := e.SignedPublicBytes()
	if err != nil {
		return p, err
	}
	digest := sha256Hex(signed)
	if p.eventID == e.Public.EventID && p.digest == digest {
		return p, nil
	}
	if p.head != e.Command.Expected || p.digest != e.Public.PreviousPublicDigest {
		return p, ErrChain
	}
	return projectionTxn{e.Result.Head, digest, e.Public.EventID, receipt}, nil
}

type releaseJournal map[string]Evidence

var errUnavailable = errors.New("fixture unavailable")

func (j releaseJournal) query(id string, available bool) (Evidence, error) {
	if !available {
		return Evidence{}, errUnavailable
	}
	e, ok := j[id]
	if !ok {
		return Evidence{}, ErrEvidence
	}
	return e, nil
}

func (p projectionTxn) confirm(e Event, c Command, released [2]bool) (projectionTxn, error) {
	if !released[0] || !released[1] {
		return p, ErrEvidence
	}
	return p.install(e, c)
}
