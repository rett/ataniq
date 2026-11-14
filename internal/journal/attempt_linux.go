package journal

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"ataniq/internal/dispatch"
)

var (
	ErrConflict = errors.New("journal: immutable binding conflict")
	ErrPolicy   = errors.New("journal: missing or invalid recorded policy")
	ErrInactive = errors.New("journal: inactive attempt capability")
	ErrLimit    = errors.New("journal: bounded value or snapshot exceeded")
)

const maxEvidence = 64 << 10

// Policy is a trusted composition input, not authentication or a reusable
// execution permit. It records full observed permission facts exactly once.
// ValidUntil bounds every permission fact, including grant-free permission.
type Policy struct {
	Intent       dispatch.Intent
	ValidUntil   time.Time
	RequireGrant bool
	Grant        *Grant
}

// Grant records an observed exact-target one-use approval. IDs remain opaque.
type Grant struct {
	ID         string
	Intent     dispatch.Intent
	ValidUntil time.Time
}

// Attempt is one exact writer activation. Pass this view to the coordinator.
// Calls may be concurrent. Do not copy it; Release invalidates the activation.
type Attempt struct {
	store      *Store
	intent     dispatch.Intent
	binding    []byte
	generation []byte
	fenced     atomic.Bool  // errors latch even when cancelled waiting for serialization
	pending    atomic.Int64 // retirement must account for calls still entering
	retiring   atomic.Bool  // no new journal calls once retirement owns the gate
}

var _ dispatch.Journal = (*Attempt)(nil)

func identityFields(i dispatch.Intent) []string {
	return []string{i.Authority, i.Project, i.Command, i.ID, i.Attempt, i.Runtime, i.Epoch, i.Action, i.Kind, i.Policy, i.Qualification, i.Authorization, i.Limits, i.Grant}
}

// The small storage validator mirrors the frozen port's accepted domain. All
// scalars use length-prefixed bytes; JSON would destroy non-UTF8 string bytes.
func encodeIntent(i dispatch.Intent) ([]byte, error) {
	fields := identityFields(i)
	for n, s := range fields {
		if (s == "" && n != 13) || len(s) > 256 || strings.IndexFunc(s, unicode.IsSpace) >= 0 {
			return nil, ErrConflict
		}
	}
	switch i.Kind {
	case "queued", "tool", "child", "iteration", "retry":
	default:
		return nil, ErrConflict
	}
	if len(i.Payload) > 1<<20 {
		return nil, ErrLimit
	}
	var b []byte
	for _, s := range fields {
		b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
		b = append(b, s...)
	}
	b = binary.BigEndian.AppendUint32(b, uint32(len(i.Payload)))
	return append(b, i.Payload...), nil
}

func decodeIntent(b []byte) (dispatch.Intent, error) {
	var i dispatch.Intent
	fields := []*string{&i.Authority, &i.Project, &i.Command, &i.ID, &i.Attempt, &i.Runtime, &i.Epoch, &i.Action, &i.Kind, &i.Policy, &i.Qualification, &i.Authorization, &i.Limits, &i.Grant}
	read := func(max int) ([]byte, error) {
		if len(b) < 4 {
			return nil, ErrConflict
		}
		n := uint64(binary.BigEndian.Uint32(b))
		b = b[4:]
		if n > uint64(max) || n > uint64(len(b)) {
			return nil, ErrLimit
		}
		v := b[:int(n)]
		b = b[int(n):]
		return v, nil
	}
	for _, p := range fields {
		v, err := read(256)
		if err != nil {
			return i, err
		}
		*p = string(v)
	}
	v, err := read(1 << 20)
	if err != nil {
		return i, err
	}
	i.Payload = append([]byte(nil), v...)
	if len(b) != 0 {
		return i, ErrConflict
	}
	_, err = encodeIntent(i)
	return i, err
}

func cloneIntent(i dispatch.Intent) dispatch.Intent {
	i.Payload = append([]byte(nil), i.Payload...)
	return i
}

// enter bounds waiting as well as SQL. The lifetime read lock lets Close wait
// for all calls before closing SQL and then releasing the inode ownership.
func (s *Store) enter(ctx context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, s.options.OperationTimeout)
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		cancel()
		return nil, nil, os.ErrClosed
	}
	select {
	case s.gate <- struct{}{}:
		if err := s.owner.check(); err != nil {
			<-s.gate
			s.mu.RUnlock()
			cancel()
			return nil, nil, err
		}
		return ctx, func() { <-s.gate; s.mu.RUnlock(); cancel() }, nil
	case <-ctx.Done():
		s.mu.RUnlock()
		cancel()
		return nil, nil, ctx.Err()
	}
}

func (s *Store) hit(point string) error {
	if s.fault != nil {
		return s.fault(point)
	}
	return nil
}

func (s *Store) observeAt(point string) {
	if s.observe != nil {
		s.observe(point)
	}
}

func (a *Attempt) fail() {
	a.store.observeAt("failure-before")
	a.store.failureMu.Lock()
	defer a.store.failureMu.Unlock()
	a.fenced.Store(true)
}

// Release is not an admitted effect operation. An enter failure can fence only
// before retirement begins. After that point it cannot change an old activation
// or race a settled retirement's decision. Retrying a failed Release while its
// view remains current is still allowed under the gate.
func (a *Attempt) releaseEntryFailure(err error) error {
	a.store.failureMu.Lock()
	defer a.store.failureMu.Unlock()
	if a.retiring.Load() {
		return ErrInactive
	}
	a.fenced.Store(true)
	return err
}

func (a *Attempt) beginRetirement() {
	a.store.failureMu.Lock()
	defer a.store.failureMu.Unlock()
	a.retiring.Store(true)
}

func (a *Attempt) retirementUncertain(r attemptRow) bool {
	// Retirement already forbids new admission. An admitted call either
	// remains pending here, or publishes its failure before decrementing to
	// zero. SC loads in this order cannot miss both sides of that handoff.
	pending := a.pending.Load()
	a.store.observeAt("retirement-sampled")
	return r.uncertain || pending != 0 || a.fenced.Load() || !r.prepared || r.state != dispatch.Denied
}
func (s *Store) commit(tx *sql.Tx, point string) error {
	if err := s.hit(point + "-before"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.hit(point + "-after")
}

func event(ctx context.Context, tx *sql.Tx, id, kind string, detail []byte) error {
	if detail == nil {
		detail = []byte{}
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO events(attempt_id,kind,detail) VALUES(?,?,?)", []byte(id), kind, detail)
	return err
}

func validExpiry(t time.Time) bool { return !t.IsZero() && t.Year() >= 1970 && t.Year() <= 9999 }

// Register atomically records immutable trusted policy and activates the dirty
// guard BEFORE returning a Journal capability. Existing IDs cannot be refreshed.
func (s *Store) Register(ctx context.Context, p Policy) (*Attempt, error) {
	b, err := encodeIntent(p.Intent)
	if err != nil {
		return nil, err
	}
	if p.Intent.Authority != s.options.Authority || !validExpiry(p.ValidUntil) {
		return nil, ErrPolicy
	}
	grantExpiry := int64(0)
	if p.RequireGrant {
		if p.Grant == nil || p.Intent.Grant == "" || p.Grant.ID != p.Intent.Grant || !validExpiry(p.Grant.ValidUntil) {
			return nil, ErrPolicy
		}
		gb, e := encodeIntent(p.Grant.Intent)
		if e != nil || !bytes.Equal(gb, b) {
			return nil, ErrPolicy
		}
		grantExpiry = p.Grant.ValidUntil.UnixMilli()
	} else if p.Grant != nil || p.Intent.Grant != "" {
		return nil, ErrPolicy
	}
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g := make([]byte, 16)
	if _, err := rand.Read(g); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM attempts WHERE id=?", []byte(p.Intent.ID)).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 0 {
		return nil, ErrConflict
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO attempts VALUES(?,?,?,?,0,0,0,1,?,'prepared',?,?)", []byte(p.Intent.ID), b, p.ValidUntil.UnixMilli(), grantExpiry, g, []byte{}, []byte{})
	if err != nil {
		return nil, err
	}
	if err = event(ctx, tx, p.Intent.ID, "registered-active", nil); err != nil {
		return nil, err
	}
	if err = s.commit(tx, "activate"); err != nil {
		return nil, err
	}
	a := &Attempt{store: s, intent: cloneIntent(p.Intent), binding: b, generation: g}
	s.views[p.Intent.ID] = a
	return a, nil
}

// Activate exposes an already known exact binding for duplicate/evidence
// handling. It confers no new execution approval, even if Prepare never ran.
func (s *Store) Activate(ctx context.Context, in dispatch.Intent) (*Attempt, error) {
	b, err := encodeIntent(in)
	if err != nil {
		return nil, err
	}
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	if s.views[in.ID] != nil {
		return nil, ErrInactive
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := readAttempt(ctx, tx, in.ID)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(b, r.binding) {
		return nil, ErrConflict
	}
	// A guard whose activation response was lost also needs recovery. Never
	// overwrite it with an apparently fresh grant of execution permission.
	g := make([]byte, 16)
	if _, err = rand.Read(g); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE attempts SET active=1,generation=?,uncertain=CASE WHEN active=1 OR prepared=0 THEN 1 ELSE uncertain END WHERE id=?", g, []byte(in.ID))
	if err != nil {
		return nil, err
	}
	if err = event(ctx, tx, in.ID, "activated-existing", nil); err != nil {
		return nil, err
	}
	if err = s.commit(tx, "activate"); err != nil {
		return nil, err
	}
	a := &Attempt{store: s, intent: cloneIntent(in), binding: b, generation: g}
	s.views[in.ID] = a
	return a, nil
}

type attemptRow struct {
	binding, generation                  []byte
	expires, grantExpires                int64
	revoked, prepared, uncertain, active bool
	state                                dispatch.State
	id, evidence                         []byte
}

func readAttempt(ctx context.Context, tx *sql.Tx, id string) (r attemptRow, err error) {
	err = tx.QueryRowContext(ctx, "SELECT binding,expires,grant_expires,revoked,prepared,uncertain,active,generation,state,receipt_id,evidence FROM attempts WHERE id=?", []byte(id)).Scan(&r.binding, &r.expires, &r.grantExpires, &r.revoked, &r.prepared, &r.uncertain, &r.active, &r.generation, &r.state, &r.id, &r.evidence)
	return
}

func (a *Attempt) current() bool { return a.store.views[a.intent.ID] == a }
func (a *Attempt) row(ctx context.Context, tx *sql.Tx) (attemptRow, error) {
	r, err := readAttempt(ctx, tx, a.intent.ID)
	if err != nil {
		return r, err
	}
	if !r.active || !bytes.Equal(r.generation, a.generation) || !bytes.Equal(r.binding, a.binding) {
		return r, ErrInactive
	}
	return r, nil
}

func (a *Attempt) receipt(r attemptRow) dispatch.Receipt {
	state := r.state
	if r.uncertain || a.fenced.Load() {
		state = dispatch.Uncertain
	}
	return dispatch.Receipt{Intent: cloneIntent(a.intent), ID: string(r.id), State: state, Evidence: string(r.evidence)}
}

// Prepare checks existing state before checking stale permission facts. Only
// this invocation's confirmed new commit can return PrepareNew.
func (a *Attempt) Prepare(ctx context.Context, in dispatch.Intent) (status dispatch.PrepareStatus, receipt dispatch.Receipt, err error) {
	b, e := encodeIntent(in)
	if e != nil || !bytes.Equal(b, a.binding) {
		return dispatch.PrepareConflict, receipt, nil
	}
	if a.retiring.Load() {
		return dispatch.PrepareUnknown, receipt, ErrInactive
	}
	a.pending.Add(1)
	defer a.pending.Add(-1)
	if a.retiring.Load() {
		return dispatch.PrepareUnknown, receipt, ErrInactive
	}
	s := a.store
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		a.fail()
		return dispatch.PrepareUnknown, receipt, err
	}
	defer leave()
	if !a.current() || a.retiring.Load() {
		return dispatch.PrepareUnknown, receipt, ErrInactive
	}
	defer func() {
		if err != nil {
			a.fail()
		}
	}()
	if err = ctx.Err(); err != nil {
		return dispatch.PrepareUnknown, receipt, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dispatch.PrepareUnknown, receipt, err
	}
	defer tx.Rollback()
	r, err := a.row(ctx, tx)
	if err != nil {
		return dispatch.PrepareUnknown, receipt, err
	}
	receipt = a.receipt(r)
	if r.prepared || r.uncertain || a.fenced.Load() {
		return dispatch.PrepareDuplicate, receipt, nil
	}
	now := s.now().UnixMilli()
	if r.revoked || now >= r.expires || (in.Grant != "" && now >= r.grantExpires) {
		return dispatch.PrepareRejected, receipt, nil
	}
	if in.Grant != "" {
		var binding []byte
		err = tx.QueryRowContext(ctx, "SELECT binding FROM grants WHERE id=?", []byte(in.Grant)).Scan(&binding)
		if err == nil {
			err = event(ctx, tx, in.ID, "grant-conflict", binding)
			if err == nil {
				err = s.commit(tx, "conflict")
			}
			return dispatch.PrepareConflict, receipt, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return dispatch.PrepareUnknown, receipt, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO grants VALUES(?,?)", []byte(in.Grant), b)
		if err != nil {
			return dispatch.PrepareUnknown, receipt, err
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE attempts SET prepared=1 WHERE id=?", []byte(in.ID))
	if err != nil {
		return dispatch.PrepareUnknown, receipt, err
	}
	if err = event(ctx, tx, in.ID, "prepared", nil); err != nil {
		return dispatch.PrepareUnknown, receipt, err
	}
	if err = s.commit(tx, "prepare"); err != nil {
		return dispatch.PrepareUnknown, receipt, err
	}
	return dispatch.PrepareNew, receipt, nil
}

// Save preserves exact, bounded receipts. Neither new evidence nor duplicate
// receipt identity can clear uncertainty or restore a consumed grant.
func (a *Attempt) Save(ctx context.Context, receipt dispatch.Receipt) (err error) {
	// Check foreign bindings before entering; these cannot poison this attempt.
	b, e := encodeIntent(receipt.Intent)
	if e != nil || !bytes.Equal(b, a.binding) {
		return ErrConflict
	}
	if a.retiring.Load() {
		return ErrInactive
	}
	a.pending.Add(1)
	defer a.pending.Add(-1)
	if a.retiring.Load() {
		return ErrInactive
	}
	s := a.store
	s.observeAt("save-admitted")
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		a.fail()
		return err
	}
	defer leave()
	if !a.current() || a.retiring.Load() {
		return ErrInactive
	}
	defer func() {
		if err != nil {
			a.fail()
		}
	}()
	if len(receipt.ID) == 0 || len(receipt.ID) > 256 || len(receipt.Evidence) > maxEvidence {
		return ErrLimit
	}
	switch receipt.State {
	case dispatch.Prepared, dispatch.Started, dispatch.Denied, dispatch.Uncertain:
	default:
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := a.row(ctx, tx)
	if err != nil {
		return err
	}
	var identical, contradictory int
	// Same-ID/different-content and differing outcome states remain distinct
	// evidence rows. Prepared is an observation, not an outcome contradiction.
	err = tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM evidence WHERE attempt_id=? AND receipt_id=? AND state=? AND evidence=?),
 EXISTS(SELECT 1 FROM evidence WHERE attempt_id=? AND
 ((receipt_id=? AND (state<>? OR evidence<>?)) OR
 (state<>? AND state<>'prepared' AND ?<>'prepared')))`, []byte(a.intent.ID), []byte(receipt.ID), string(receipt.State), []byte(receipt.Evidence), []byte(a.intent.ID), []byte(receipt.ID), string(receipt.State), []byte(receipt.Evidence), string(receipt.State), string(receipt.State)).Scan(&identical, &contradictory)
	if err != nil {
		return err
	}
	if identical != 0 {
		return nil
	}
	uncertain := r.uncertain || a.fenced.Load() || !r.prepared || contradictory != 0 || receipt.State == dispatch.Uncertain
	_, err = tx.ExecContext(ctx, "INSERT INTO evidence(attempt_id,receipt_id,state,evidence) VALUES(?,?,?,?)", []byte(a.intent.ID), []byte(receipt.ID), string(receipt.State), []byte(receipt.Evidence))
	if err != nil {
		return err
	}
	state, id, detail := receipt.State, []byte(receipt.ID), []byte(receipt.Evidence)
	if receipt.State == dispatch.Prepared && r.state != dispatch.Prepared {
		state, id, detail = r.state, r.id, r.evidence
	}
	_, err = tx.ExecContext(ctx, "UPDATE attempts SET uncertain=?,state=?,receipt_id=?,evidence=? WHERE id=?", uncertain, string(state), id, detail, []byte(a.intent.ID))
	if err != nil {
		return err
	}
	if err = event(ctx, tx, a.intent.ID, "receipt", encodeReceipt(receipt)); err != nil {
		return err
	}
	return s.commit(tx, "save")
}

func encodeReceipt(r dispatch.Receipt) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(r.ID)))
	b = append(b, r.ID...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(r.State)))
	b = append(b, string(r.State)...)
	return append(b, r.Evidence...)
}

// Revoke orders monotonic permission revocation with Prepare under this exact
// durable guard. An ambiguous result fences the activation; it is never success.
func (a *Attempt) Revoke(ctx context.Context) (err error) {
	if a.retiring.Load() {
		return ErrInactive
	}
	a.pending.Add(1)
	defer a.pending.Add(-1)
	if a.retiring.Load() {
		return ErrInactive
	}
	s := a.store
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		a.fail()
		return err
	}
	defer leave()
	if !a.current() || a.retiring.Load() {
		return ErrInactive
	}
	defer func() {
		if err != nil {
			a.fail()
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = a.row(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE attempts SET revoked=1 WHERE id=?", []byte(a.intent.ID)); err != nil {
		return err
	}
	if err = event(ctx, tx, a.intent.ID, "revoked", nil); err != nil {
		return err
	}
	return s.commit(tx, "revoke")
}

// Release follows Dispatch returning in trusted composition. Started is start
// evidence, not proof of completion or executor quiescence. Unresolved work
// becomes durable uncertainty in the SAME transaction that removes the guard.
func (a *Attempt) Release(ctx context.Context) (err error) {
	s := a.store
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return a.releaseEntryFailure(err)
	}
	defer leave()
	if !a.current() {
		return ErrInactive
	}
	a.beginRetirement()
	defer func() {
		if err != nil {
			a.fail()
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := a.row(ctx, tx)
	if err != nil {
		return err
	}
	uncertain := a.retirementUncertain(r)
	if _, err = tx.ExecContext(ctx, "UPDATE attempts SET active=0,uncertain=? WHERE id=? AND generation=?", uncertain, []byte(a.intent.ID), a.generation); err != nil {
		return err
	}
	if err = event(ctx, tx, a.intent.ID, "released", nil); err != nil {
		return err
	}
	if err = s.commit(tx, "release"); err != nil {
		return err
	}
	delete(s.views, a.intent.ID)
	return nil
}

func (s *Store) recover(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO events(attempt_id,kind,detail) SELECT id,'recovered-uncertain',x'' FROM attempts WHERE active=1"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE attempts SET uncertain=1,active=0 WHERE active=1"); err != nil {
		return err
	}
	return s.commit(tx, "recover")
}

func (s *Store) retireAll(ctx context.Context) error {
	// Close owns the lifetime write lock. Calls that passed admission but
	// have not acquired a read lock are still counted; do not drain them here.
	// Publish even when beginning/committing retirement subsequently fails.
	for _, a := range s.views {
		a.beginRetirement()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, a := range s.views {
		r, err := readAttempt(ctx, tx, id)
		if err != nil {
			return err
		}
		if !bytes.Equal(r.generation, a.generation) || !bytes.Equal(r.binding, a.binding) {
			return ErrInactive
		}
		uncertain := a.retirementUncertain(r)
		if _, err = tx.ExecContext(ctx, "UPDATE attempts SET uncertain=?,active=0 WHERE id=? AND generation=?", uncertain, []byte(id), a.generation); err != nil {
			return err
		}
		if err = event(ctx, tx, id, "closed", nil); err != nil {
			return err
		}
	}
	return s.commit(tx, "close")
}
