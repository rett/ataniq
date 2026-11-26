package enrollment

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"slices"
	"sync"

	"ataniq/internal/assignment"
	"ataniq/internal/assignment/wire"
	"ataniq/internal/checkpoint"
)

// Store owns the local writer lock and the admitted witness session.
type Store struct {
	mu       sync.Mutex
	files    LockedLedger
	b        Binding
	client   checkpoint.Client
	session  ed25519.PrivateKey
	verifier assignment.Verifier
	ledger   assignment.Ledger
	state    checkpoint.State
	fault    Fault
	stopped  bool
}

// Ledger returns the last admitted projection, for preparing a signed event.
func (s *Store) Ledger() (assignment.Ledger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.files == nil {
		return assignment.Ledger{}, refuse(Closed, nil)
	}
	return s.ledger, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	if s.files == nil {
		return nil
	}
	err := s.files.Close()
	s.files = nil
	clear(s.session)
	return err
}

func (s *Store) read(ctx context.Context, prior checkpoint.ID) (checkpoint.View, error) {
	q := checkpoint.ReadRequest{Slot: s.b.Slot, Challenge: id(), ReceiptID: prior}
	v, err := s.client.Read(ctx, q)
	if err == nil {
		err = checkpoint.VerifyView(s.b.WitnessPin, q, v)
	}
	if err != nil {
		return checkpoint.View{}, witnessError(err)
	}
	if v.Current.Slot != s.b.Slot || v.Current.Epoch != s.b.Epoch || v.Current.Header != s.b.Header {
		return checkpoint.View{}, refuse(IdentityMismatch, nil)
	}
	if v.Current.Status == checkpoint.Retired {
		return checkpoint.View{}, refuse(Retired, nil)
	}
	return v, nil
}

func compare(head assignment.Checkpoint, state checkpoint.State) error {
	switch checkpoint.Compare(head, state) {
	case checkpoint.Equal:
		return nil
	case checkpoint.HomeAhead:
		return refuse(HomeAhead, nil)
	case checkpoint.HomeBehind:
		return refuse(HomeBehind, nil)
	case checkpoint.Fork:
		return refuse(Fork, nil)
	case checkpoint.Retired:
		return refuse(Retired, nil)
	default:
		return refuse(Unknown, nil)
	}
}

func (s *Store) confirm(ctx context.Context, op string, q checkpoint.Intent, r checkpoint.Receipt) (checkpoint.State, error) {
	body, err := q.Body("")
	if err != nil {
		return checkpoint.State{}, refuse(Unknown, err)
	}
	if err := checkpoint.MatchReceipt(s.b.WitnessPin, s.b.Epoch, op, body, r); err != nil {
		return checkpoint.State{}, refuse(Unknown, err)
	}
	v, err := s.read(ctx, checkpoint.ID{})
	if err != nil {
		return checkpoint.State{}, err
	}
	ack, err := checkpoint.VerifyReceipt(s.b.WitnessPin, r)
	if err != nil {
		return checkpoint.State{}, refuse(Unknown, err)
	}
	if v.Current != ack.State {
		return checkpoint.State{}, refuse(Occupied, checkpoint.ErrStale)
	}
	return v.Current, nil
}

// Start takes ownership of a locked ledger on success. A fresh client/session
// must claim the witness even after an interrupted advance is completed.
// Only a caller still holding the original session capability can retry its
// outstanding CAS. Loss or release of that key leaves a pending suffix closed.
// A committed advance with a lost reply can be verified by its receipt even
// after the owner releases the old session.
func Start(ctx context.Context, root LedgerStore, b Binding, client checkpoint.Client, session ed25519.PrivateKey, fault Fault) (*Store, error) {
	if len(session) != ed25519.PrivateKeySize {
		return nil, refuse(Unknown, checkpoint.ErrRefused)
	}
	q, err := b.request()
	if err != nil {
		return nil, refuse(Unknown, err)
	}
	body, err := q.Body("")
	if err != nil {
		return nil, refuse(Unknown, err)
	}
	if err := checkpoint.MatchReceipt(b.WitnessPin, b.Epoch, "enroll", body, b.Receipt); err != nil {
		return nil, refuse(Unknown, err)
	}
	verifier, err := wire.NewVerifier(b.Header, b.HomePin)
	if err != nil {
		return nil, refuse(IdentityMismatch, err)
	}
	f, err := root.Lock(fault)
	if err != nil {
		return nil, err
	}
	b.WitnessPin = slices.Clone(b.WitnessPin)
	s := &Store{files: f, b: b, client: client, session: slices.Clone(session), verifier: verifier, fault: fault}
	if err := s.start(ctx, q.HeaderBytes); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}

func (s *Store) start(ctx context.Context, header []byte) error {
	r, identity, err := s.files.Ledger()
	if err != nil {
		return err
	}
	if identity != s.b.Identity {
		return refuse(IdentityMismatch, nil)
	}
	ledger, last, err := replay(r, header, s.verifier)
	if err != nil {
		return err
	}
	stage, err := s.files.Stage()
	if err != nil {
		return err
	}
	v, err := s.read(ctx, s.b.EnrollmentID)
	if err != nil {
		return err
	}
	if v.Prior == nil || checkpoint.ReceiptDigest(*v.Prior) != checkpoint.ReceiptDigest(s.b.Receipt) {
		return refuse(Unknown, errors.New("original enrollment receipt missing or changed"))
	}
	if err := s.files.Check(); err != nil {
		return err
	}
	if stage != nil {
		v, err = s.finishStage(ctx, stage, last, ledger.Checkpoint(), v)
		if err != nil {
			return err
		}
	}
	if err := compare(ledger.Checkpoint(), v.Current); err != nil {
		return err
	}
	q := checkpoint.Claim{Mutation: checkpoint.Mutation{ID: id(), Expected: v.Current}, NewSessionPublicKey: s.session.Public().(ed25519.PublicKey)}
	rct, err := s.client.Claim(ctx, q)
	if err != nil {
		return witnessError(err)
	}
	s.state, err = s.confirm(ctx, "claim", q, rct)
	if err != nil {
		return err
	}
	if err = compare(ledger.Checkpoint(), s.state); err != nil {
		return err
	}
	if err := s.files.Check(); err != nil {
		return err
	}
	if stage != nil {
		if err := s.files.ClearStage(); err != nil {
			return refuse(Interrupted, err)
		}
	}
	s.ledger = ledger
	return nil
}

func equalHeader(raw, expected []byte) error {
	if _, err := assignment.NewLedgerFromHeader(raw); err != nil {
		if _, eventErr := assignment.DecodeRecord(raw); eventErr == nil {
			return refuse(MissingHeader, nil)
		}
		return ledgerError(err)
	}
	if !bytes.Equal(raw, expected) {
		return refuse(IdentityMismatch, nil)
	}
	return nil
}

func ledgerError(err error) error {
	if errors.Is(err, assignment.ErrFormat) {
		return refuse(Legacy, err)
	}
	return refuse(Corrupt, err)
}

// Use the assignment frame bound and retain the LF. An unfinished tail is
// evidence; the managed opener must never trim or complete it.
func replay(r io.Reader, hdr []byte, verifier assignment.Verifier) (assignment.Ledger, []byte, error) {
	reader := bufio.NewReaderSize(r, 64<<10)
	frame := func() ([]byte, error) {
		raw, err := reader.ReadSlice('\n')
		if errors.Is(err, io.EOF) && len(raw) > 0 {
			return nil, refuse(Truncated, err)
		}
		if err != nil && err != io.EOF {
			return nil, ledgerError(err)
		}
		return raw, err
	}
	raw, err := frame()
	if err == io.EOF {
		return assignment.Ledger{}, nil, refuse(MissingHeader, nil)
	}
	if err != nil {
		return assignment.Ledger{}, nil, err
	}
	if err := equalHeader(raw, hdr); err != nil {
		return assignment.Ledger{}, nil, err
	}
	var events []assignment.Event
	var last []byte
	for {
		raw, err = frame()
		if err == io.EOF {
			break
		}
		if err != nil {
			return assignment.Ledger{}, nil, err
		}
		e, err := assignment.DecodeRecord(raw)
		if err != nil {
			return assignment.Ledger{}, nil, ledgerError(err)
		}
		events = append(events, e)
		last = append(last[:0], raw...)
	}
	l, err := assignment.ReplayFromHeader(hdr, events, verifier)
	if err != nil {
		return assignment.Ledger{}, nil, ledgerError(err)
	}
	return l, last, nil
}
