package leadregistry

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadrole"
)

// Options tunes a Registry. The zero value is what production uses; the fields
// exist so tests can fix the clock and the randomness.
type Options struct {
	// Now returns the time stamped on a record. Default: time.Now.
	Now func() time.Time
	// Rand supplies the random bits of intent ids. Default: crypto/rand.
	Rand io.Reader
}

// Recovery reports the cuts Open has made that nobody has acknowledged. It is
// what the daemon reads after Open to decide whether anything was lost: when
// Cut is true, every role may have lost its newest records, so it should show
// every role as unknown until it has looked at the unit.
//
// When Open cuts an unfinished tail it appends a recovered record to the
// journal saying what was cut and where. Recovery reports the cuts of the
// recovered records that no later recovery-acknowledged record follows, so
// the report survives a reopen and goes away only with AcknowledgeRecovery.
// With more than one unacknowledged cut Lines and Bytes are the sums and
// Offset is that of the latest.
//
// Open reads a cut anywhere in the file as an append that never returned, not
// only a cut inside the last line: a file truncated from outside inside an
// earlier line, even in the middle of it, opens with the older state, the
// records after the cut are lost, and their epochs may be given out again.
// A cut exactly at a line boundary leaves a valid shorter journal and reports
// nothing, and so does a cut that was made but whose recovered record never
// reached the disk (the cut and the record are written before one sync, so
// a crash in between can lose the record).
type Recovery struct {
	// Lines is the number of unfinished final lines that were cut off.
	Lines int
	// Bytes is the number of bytes cut off.
	Bytes int64
	// Offset is the file size after the cut, where the cut was made.
	Offset int64
}

// CorruptError is the error Open returns for a damaged journal. It names the
// 1-based line of the file and matches ErrCorrupt and nothing else: the error
// of a refused record (a stale epoch, a refused launch, an undefined role) is
// kept as text in Reason and its identity is not passed through, so a caller
// that tests for one of those errors around Open does not take a corrupt
// journal for it.
type CorruptError struct {
	// Line is the 1-based line number in the file.
	Line int
	// Reason says what is wrong with it. For a record the state does not
	// admit it is the text of the refusal.
	Reason string
}

// Error names the line and the damage.
func (e *CorruptError) Error() string {
	return fmt.Sprintf("%v: line %d: %s", ErrCorrupt, e.Line, e.Reason)
}

// Is makes errors.Is(err, ErrCorrupt) true.
func (e *CorruptError) Is(target error) bool { return target == ErrCorrupt }

// Registry is the open, locked registry. Its methods are safe for concurrent
// use. It is not safe to use after Close.
type Registry struct {
	mu       sync.Mutex
	f        *os.File
	roles    map[leadrole.Role]Entry
	seq      uint64
	prev     [sha256.Size]byte
	recovery Recovery
	now      func() time.Time
	rand     io.Reader
	closed   bool
	broken   error
}

// Open opens the registry in dir, creating dir (mode 0700) and the journal
// file (mode 0600) when missing, takes the exclusive file lock and replays the
// journal. It fails with ErrLocked when another holder has the lock, with
// ErrInsecure for a symlink, a foreign owner or too-wide permission bits, and
// with ErrCorrupt (a *CorruptError naming the line) for any damage other than
// an unfinished final line. An unfinished final line is cut off and counted
// in Recovery.
func Open(dir string, opts Options) (*Registry, error) {
	r := &Registry{roles: map[leadrole.Role]Entry{}, now: opts.Now, rand: opts.Rand}
	if r.now == nil {
		r.now = time.Now
	}
	if r.rand == nil {
		r.rand = rand.Reader
	}
	dirFD, err := openDir(dir)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirFD)
	f, err := openFile(dirFD, filepath.Join(dir, FileName))
	if err != nil {
		return nil, err
	}
	r.f = f
	if err := r.start(dirFD); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

func syncFD(fd int) error {
	for {
		err := unix.Fsync(fd)
		if err != unix.EINTR {
			return err
		}
	}
}

func checkOwner(st *unix.Stat_t, path string) error {
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: %s is not owned by the current user", ErrInsecure, path)
	}
	return nil
}

func openDir(dir string) (int, error) {
	dir = filepath.Clean(dir)
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open(dir, flags, 0)
	if err == unix.ENOENT {
		parent := filepath.Dir(dir)
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return -1, fmt.Errorf("leadregistry: create folder: %w", err)
		}
		if err := unix.Mkdir(dir, 0o700); err != nil && err != unix.EEXIST {
			return -1, fmt.Errorf("leadregistry: create folder: %w", err)
		}
		fd, err = unix.Open(dir, flags, 0)
		if err != nil {
			return -1, fmt.Errorf("leadregistry: open folder: %w", err)
		}
		if err := unix.Fchmod(fd, 0o700); err != nil {
			unix.Close(fd)
			return -1, fmt.Errorf("leadregistry: chmod folder: %w", err)
		}
		if pfd, perr := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0); perr == nil {
			serr := syncFD(pfd)
			unix.Close(pfd)
			if serr != nil {
				unix.Close(fd)
				return -1, fmt.Errorf("leadregistry: sync parent folder: %w", serr)
			}
		}
	} else if err == unix.ELOOP || err == unix.ENOTDIR {
		return -1, fmt.Errorf("%w: %s is a symlink or not a folder", ErrInsecure, dir)
	} else if err != nil {
		return -1, fmt.Errorf("leadregistry: open folder: %w", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("leadregistry: stat folder: %w", err)
	}
	if err := checkOwner(&st, dir); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if st.Mode&0o077 != 0 {
		unix.Close(fd)
		return -1, fmt.Errorf("%w: folder %s has group or other permission bits (want 0700)", ErrInsecure, dir)
	}
	return fd, nil
}

func openFile(dirFD int, path string) (*os.File, error) {
	name := FileName
	base := unix.O_RDWR | unix.O_APPEND | unix.O_NOFOLLOW | unix.O_CLOEXEC
	created := true
	fd, err := unix.Openat(dirFD, name, base|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err == unix.EEXIST {
		created = false
		fd, err = unix.Openat(dirFD, name, base, 0)
	}
	if err == unix.ELOOP {
		return nil, fmt.Errorf("%w: %s is a symlink", ErrInsecure, path)
	}
	if err != nil {
		return nil, fmt.Errorf("leadregistry: open journal: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	fail := func(err error) (*os.File, error) {
		f.Close()
		return nil, err
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err != unix.EINTR {
			break
		}
	}
	if err == unix.EWOULDBLOCK {
		return fail(fmt.Errorf("%w: %s", ErrLocked, path))
	}
	if err != nil {
		return fail(fmt.Errorf("leadregistry: lock journal: %w", err))
	}
	if created {
		if err := unix.Fchmod(fd, 0o600); err != nil {
			return fail(fmt.Errorf("leadregistry: chmod journal: %w", err))
		}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fail(fmt.Errorf("leadregistry: stat journal: %w", err))
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fail(fmt.Errorf("%w: %s is not a regular file", ErrInsecure, path))
	}
	if err := checkOwner(&st, path); err != nil {
		return fail(err)
	}
	if st.Mode&0o177 != 0 {
		return fail(fmt.Errorf("%w: %s has permission bits wider than 0600", ErrInsecure, path))
	}
	return f, nil
}

// start replays the journal, cuts an unfinished tail and writes the header of
// an empty journal.
func (r *Registry) start(dirFD int) error {
	rd := bufio.NewReaderSize(r.f, MaxLineBytes+1)
	var offset int64
	var tail int
	lines := 0
	for {
		line, err := rd.ReadSlice('\n')
		if err == nil {
			lines++
			if err := r.replayLine(lines, line[:len(line)-1]); err != nil {
				return err
			}
			offset += int64(len(line))
			continue
		}
		if err == io.EOF {
			tail = len(line)
			break
		}
		if err == bufio.ErrBufferFull {
			return &CorruptError{Line: lines + 1, Reason: fmt.Sprintf("line longer than %d bytes", MaxLineBytes)}
		}
		return fmt.Errorf("leadregistry: read journal: %w", err)
	}
	if tail > 0 {
		if err := r.f.Truncate(offset); err != nil {
			return fmt.Errorf("leadregistry: cut unfinished line: %w", err)
		}
	}
	if lines == 0 {
		line, sum, err := encodeLine(genesis, 0, r.now(), kindHeader, leadrole.Role{},
			&headerPayload{Format: formatName, Version: formatVersion})
		if err != nil {
			return err
		}
		if _, err := r.f.Write(line); err != nil {
			return fmt.Errorf("leadregistry: write header: %w", err)
		}
		r.prev = sum
	}
	if tail > 0 {
		// The record of the cut is written before the one sync. A crash before
		// that sync may keep the cut and lose the record; the cut is then not
		// reported (see the package documentation).
		if _, _, err := r.commitRecovered(&recoveredPayload{Lines: 1, Bytes: int64(tail), Offset: offset}); err != nil {
			return err
		}
	}
	if tail > 0 || lines == 0 {
		if err := r.f.Sync(); err != nil {
			return fmt.Errorf("leadregistry: sync after recovery: %w", err)
		}
	}
	if lines == 0 {
		if err := syncFD(dirFD); err != nil {
			return fmt.Errorf("leadregistry: sync folder: %w", err)
		}
	}
	return nil
}

// commitRecovered appends the record of a cut to the file, without a sync,
// and applies it. Called by Open only.
func (r *Registry) commitRecovered(p *recoveredPayload) (record, Recovery, error) {
	rec, line, sum, err := r.prepare(kindRecovered, leadrole.Role{}, p)
	if err != nil {
		return record{}, Recovery{}, err
	}
	next, err := applyRecovery(r.recovery, rec)
	if err != nil {
		return record{}, Recovery{}, err
	}
	if _, err := r.f.Write(line); err != nil {
		return record{}, Recovery{}, fmt.Errorf("leadregistry: write recovery record: %w", err)
	}
	r.recovery, r.seq, r.prev = next, rec.seq, sum
	return rec, next, nil
}

// applyRecovery applies a recovered or recovery-acknowledged record to the
// unacknowledged cuts.
func applyRecovery(cur Recovery, rec record) (Recovery, error) {
	switch p := rec.payload.(type) {
	case *recoveredPayload:
		return Recovery{Lines: cur.Lines + p.Lines, Bytes: cur.Bytes + p.Bytes, Offset: p.Offset}, nil
	case *ackPayload:
		if !cur.Cut() {
			return Recovery{}, fmt.Errorf("a recovery acknowledgement with no recovery to acknowledge")
		}
		return Recovery{}, nil
	}
	return Recovery{}, invalid("record kind %q", rec.kind)
}

// replayLine applies the n-th line (1-based, newline removed) of the file.
func (r *Registry) replayLine(n int, line []byte) error {
	rec, sum, err := decodeLine(r.prev, uint64(n-1), line)
	if err != nil {
		return &CorruptError{Line: n, Reason: err.Error()}
	}
	if (n == 1) != (rec.kind == kindHeader) {
		return &CorruptError{Line: n, Reason: "header line missing or repeated"}
	}
	switch {
	case rec.kind == kindRecovered || rec.kind == kindRecoveryAck:
		next, err := applyRecovery(r.recovery, rec)
		if err != nil {
			return &CorruptError{Line: n, Reason: err.Error()}
		}
		r.recovery = next
	case rec.kind != kindHeader:
		cur, ok := r.roles[rec.role]
		next, err := reduce(cur, ok, r.roles, rec)
		if err != nil {
			return &CorruptError{Line: n, Reason: err.Error()}
		}
		r.roles[rec.role] = next
	}
	r.seq = rec.seq
	r.prev = sum
	return nil
}

// Cut reports whether Open cut anything off the journal.
func (r Recovery) Cut() bool { return r.Lines > 0 || r.Bytes > 0 }

// Recovery returns the cuts of the journal that no one has acknowledged: what
// Open cut off it just now, and what an earlier Open cut that was not
// acknowledged since. It is fixed when Open returns and changes only by
// AcknowledgeRecovery, whatever else is written, and it stays readable after
// Close.
func (r *Registry) Recovery() Recovery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recovery
}

// Close releases the file lock and closes the journal. It is safe to call
// more than once.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.f.Close()
}

// usable reports why the registry cannot be used, with r.mu held.
func (r *Registry) usable() error {
	if r.closed {
		return ErrClosed
	}
	if r.broken != nil {
		return fmt.Errorf("%w: %v", ErrBroken, r.broken)
	}
	return nil
}

// prepare encodes a record on top of the journal, reads it back through the
// replay path and returns it with its line and chain value, writing nothing.
// Called with r.mu held, or by Open.
func (r *Registry) prepare(kind string, role leadrole.Role, body payload) (record, []byte, [sha256.Size]byte, error) {
	seq := r.seq + 1
	line, sum, err := encodeLine(r.prev, seq, r.now(), kind, role, body)
	if err != nil {
		return record{}, nil, sum, err
	}
	rec, _, err := decodeLine(r.prev, seq, line[:len(line)-1])
	if err != nil {
		return record{}, nil, sum, fmt.Errorf("leadregistry: internal: record does not read back: %w", err)
	}
	return rec, line, sum, nil
}

// commit appends a prepared line and syncs it. If the write or the sync
// fails the registry is broken: the record may or may not be on disk, since
// the bytes of a completed write stay in the file when only the sync failed,
// and a reopen reads them. Called with r.mu held.
func (r *Registry) commit(rec record, line []byte, sum [sha256.Size]byte) error {
	if _, err := r.f.Write(line); err != nil {
		r.broken = err
		return fmt.Errorf("leadregistry: append (the record may or may not be on disk): %w", err)
	}
	if err := r.f.Sync(); err != nil {
		r.broken = err
		return fmt.Errorf("leadregistry: sync (the record may or may not be on disk): %w", err)
	}
	r.seq = rec.seq
	r.prev = sum
	return nil
}

// mutate journals one record of a role and installs the resulting entry. The
// record is validated, encoded, re-read through the replay path and reduced
// against the current state before a byte is written, so a refused call
// writes nothing. Failures of the write or the sync are those of commit.
// Called with r.mu held.
func (r *Registry) mutate(kind string, role leadrole.Role, body payload) (Entry, error) {
	if err := r.usable(); err != nil {
		return Entry{}, err
	}
	if role.IsZero() {
		return Entry{}, invalid("zero role")
	}
	if err := body.validate(); err != nil {
		return Entry{}, err
	}
	if err := checkRole(body, role); err != nil {
		return Entry{}, err
	}
	rec, line, sum, err := r.prepare(kind, role, body)
	if err != nil {
		return Entry{}, err
	}
	cur, ok := r.roles[role]
	next, err := reduce(cur, ok, r.roles, rec)
	if err != nil {
		return Entry{}, err
	}
	if err := r.commit(rec, line, sum); err != nil {
		return Entry{}, err
	}
	r.roles[role] = next
	return next.clone(), nil
}

// AcknowledgeRecovery journals that the cuts Recovery reports have been
// dealt with, with a note saying how (required, at most MaxProofBytes bytes,
// no control character), and returns the cuts it acknowledged. From then on
// Recovery reports no cut, also after a reopen, until Open cuts again. It is
// refused with ErrNoRecovery, writing nothing, when no cut is unacknowledged,
// and with ErrInvalid for a missing or malformed note.
func (r *Registry) AcknowledgeRecovery(note string) (Recovery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usable(); err != nil {
		return Recovery{}, err
	}
	body := &ackPayload{Note: note}
	if err := body.validate(); err != nil {
		return Recovery{}, err
	}
	if !r.recovery.Cut() {
		return Recovery{}, ErrNoRecovery
	}
	rec, line, sum, err := r.prepare(kindRecoveryAck, leadrole.Role{}, body)
	if err != nil {
		return Recovery{}, err
	}
	next, err := applyRecovery(r.recovery, rec)
	if err != nil {
		return Recovery{}, err
	}
	if err := r.commit(rec, line, sum); err != nil {
		return Recovery{}, err
	}
	acked := r.recovery
	r.recovery = next
	return acked, nil
}

func (r *Registry) existing(role leadrole.Role) (Entry, error) {
	if role.IsZero() {
		return Entry{}, invalid("zero role")
	}
	e, ok := r.roles[role]
	if !ok {
		return Entry{}, fmt.Errorf("%w: %s", ErrNoRole, role)
	}
	return e, nil
}

// Define adds a role, or updates the manager and requested profile of an
// existing one. A new role starts with epoch 0, desired stopped, and shown
// state stopped with reason code "defined". An update leaves the epoch, the
// desired state, the shown state, the intent and the evidence alone.
//
// For a coordinator the runtime (Provider) and the model of requested are
// DefaultCoordinatorProvider and DefaultCoordinatorModel when empty, and the
// version, effort, permission mode and access mode are optional; the profile
// is journaled with the defaults filled in.
//
// A profile that sets Pack is refused with ErrInvalid, writing nothing:
// provider packs are not wired in this build. (A define record that a later
// build wrote with a pack replays; see BeginLaunch.)
func (r *Registry) Define(role leadrole.Role, manager Manager, requested Requested) (Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !requested.Pack.IsZero() {
		return Entry{}, invalid("a pack reference in the requested profile: provider packs are not wired in this build")
	}
	if role.Kind() == leadrole.KindCoordinator {
		requested = requested.withCoordinatorDefaults()
	}
	return r.mutate(kindDefine, role, &definePayload{Manager: manager, Requested: requested})
}

// SetDesired sets the desired state of a role. It does not change the shown
// state.
func (r *Registry) SetDesired(role leadrole.Role, desired Desired) (Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mutate(kindDesired, role, &desiredPayload{Desired: desired})
}

// BeginLaunch journals a launch intent with the next epoch and sets the shown
// state to starting. It returns the intent. The caller must not touch systemd
// before it returns. It is refused, matching ErrLaunchRefused, with
// ErrLaunchPending when an intent is pending, ErrLaunchManager unless the
// manager is ataniq, and ErrLaunchState unless the shown state is desired,
// stopped or ended; ErrEpochExhausted when the epoch cannot grow. The epoch
// is consumed even if the launch later fails: an epoch is never reused. It
// records no boot identity; BeginLaunchWith does.
//
// A coordinator is admitted only under its parent lead: the parent lead of
// the project must be defined, run by ataniq and shown starting, ready or
// degraded and its own launch not failed or abandoned, else the launch is
// refused with ErrLaunchNoParent, ErrLaunchParentManager or
// ErrLaunchParentState. The intent then records the parent's epoch
// (Intent.ParentEpoch) and the identity of the instance (Intent.Identity); the
// instance number is the coordinator's epoch. A lead is refused with
// ErrLaunchCoordinator while a coordinator of it that was launched under one
// of its earlier epochs is not stopped, ended or desired.
//
// The intent's profile digest is the digest of the requested profile as the
// role's define record stores it. A role whose define record a newer build
// wrote with a member this build does not know is refused with
// ErrLaunchProfile: it replays, but this build does not launch a profile it
// cannot read whole. So is a role whose recorded profile names a pack
// (Requested.Pack): this build does not launch packs.
func (r *Registry) BeginLaunch(role leadrole.Role) (Intent, error) {
	return r.BeginLaunchWith(role, LaunchOptions{})
}

// LaunchOptions are the optional inputs of BeginLaunchWith. The zero value
// is what BeginLaunch uses.
type LaunchOptions struct {
	// BootIdentity is the identity of the boot the launch is made in, as the
	// caller knows it: at most MaxBootIdentityBytes bytes of valid UTF-8
	// without a control character. It is journaled in the launch intent and
	// stored as given, so a later reader can tell whether the host has
	// restarted since. Empty records none.
	BootIdentity string
}

// BeginLaunchWith is BeginLaunch with options: it records the boot identity
// of the options in the intent. It is refused with ErrInvalid, writing
// nothing, for a boot identity over the bound or with a control character;
// every other rule is BeginLaunch's.
func (r *Registry) BeginLaunchWith(role leadrole.Role, opts LaunchOptions) (Intent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usable(); err != nil {
		return Intent{}, err
	}
	if err := validText("boot identity", opts.BootIdentity, MaxBootIdentityBytes, false); err != nil {
		return Intent{}, err
	}
	e, err := r.existing(role)
	if err != nil {
		return Intent{}, err
	}
	if e.Epoch == ^uint64(0) {
		return Intent{}, ErrEpochExhausted
	}
	if err := launchGate(e, r.roles); err != nil {
		return Intent{}, err
	}
	// A rule of writing only: a launch a newer build journaled for such a
	// profile replays, but this build does not launch what it cannot read.
	if err := e.ProfileLaunchable(); err != nil {
		return Intent{}, err
	}
	var parentEpoch uint64
	var identity string
	if role.Kind() == leadrole.KindCoordinator {
		parentEpoch = r.roles[role.Parent()].Epoch
		identity = role.Identity(parentEpoch, e.Epoch+1)
	}
	var id [16]byte
	if _, err := io.ReadFull(r.rand, id[:]); err != nil {
		return Intent{}, fmt.Errorf("leadregistry: random intent id: %w", err)
	}
	next, err := r.mutate(kindLaunchBegin, role, &beginPayload{
		IntentID:      hex.EncodeToString(id[:]),
		Epoch:         e.Epoch + 1,
		UnitName:      role.UnitName(),
		ProfileDigest: e.profileDigest,
		ParentEpoch:   parentEpoch,
		Identity:      identity,
		BootID:        opts.BootIdentity,
	})
	if err != nil {
		return Intent{}, err
	}
	return *next.Intent, nil
}

// RecordOutcome records how the launch intent ended, with the unit evidence
// of the epoch. The outcome must not be pending. It is refused with
// ErrStaleEpoch when epoch is not the current one, ErrIntentMismatch when
// intentID is not the current intent, and ErrIntentResolved when the intent
// already has an outcome. It does not change the shown state.
func (r *Registry) RecordOutcome(role leadrole.Role, intentID string, epoch uint64, outcome Outcome, unit UnitEvidence) (Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mutate(kindLaunchOutcome, role, &outcomePayload{IntentID: intentID, Epoch: epoch, Outcome: outcome, Unit: unit})
}

// StateChange is one change of the shown state.
type StateChange struct {
	// Epoch must be the role's current epoch.
	Epoch uint64
	// State is the new shown state. starting is set by BeginLaunch only.
	State State
	// Evidence is the record kind of the evidence behind the state.
	Evidence Evidence
	// Reason is the machine code and detail.
	Reason Reason
	// Proof is required, non-empty, for stopped and ended, and journaled.
	Proof string
}

// SetState sets the shown state of a role for an epoch. The transition table
// in the package documentation says what is admitted. It is refused with
// ErrStaleEpoch unless the epoch is the current one, with ErrProofRequired
// for stopped or ended without a proof text, and with ErrStateTransition for
// a change the table refuses: desired from any state but desired, stopped or
// ended; ready, degraded or stopping from unknown; stopped or ended with
// owner-decision evidence from every state (an owner decision is recorded
// through ResolveUnknown, from unknown only); stalled for a role that is not
// a coordinator and for a coordinator that is not starting, ready or
// degraded; and, as ErrOutcomePending, stopped or ended while the launch
// intent is pending. unknown is accepted for the current epoch from every
// state.
func (r *Registry) SetState(role leadrole.Role, c StateChange) (Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mutate(kindState, role, &statePayload{
		Epoch: c.Epoch, State: c.State, Evidence: c.Evidence, Reason: c.Reason, Proof: c.Proof,
	})
}

// OwnerDecision is an owner decision that resolves an unknown state.
type OwnerDecision struct {
	// Epoch must be the role's current epoch.
	Epoch uint64
	// State is the state the owner decided on: stopped or ended.
	State State
	// Reason is the machine code and detail.
	Reason Reason
	// Decision is the owner's decision text, required, non-empty, journaled.
	Decision string
}

// ResolveUnknown records an owner decision that resolves the shown state
// unknown. It is the only way besides SetState with proof out of unknown. It
// moves the role to stopped or ended only, with the evidence kind
// owner-decision, and stores the decision text as the proof of the shown
// state. It is journaled as its own record kind, owner-decision. It is
// refused with ErrDecisionRequired for an empty decision text, ErrInvalid
// unless the state is stopped or ended, ErrStaleEpoch unless the epoch is the
// current one, and ErrStateTransition when the shown state is not unknown or,
// as ErrOutcomePending, when the launch intent is pending.
func (r *Registry) ResolveUnknown(role leadrole.Role, d OwnerDecision) (Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mutate(kindOwnerDecision, role, &decisionPayload{
		Epoch: d.Epoch, State: d.State, Reason: d.Reason, Decision: d.Decision,
	})
}

// SetObserved records observed profile fields for a role and epoch: every
// field of obs that is marked observed overwrites the stored one, the others
// are left alone. At least one field must be observed. The observed profile
// starts empty in every epoch.
func (r *Registry) SetObserved(role leadrole.Role, epoch uint64, obs Observed) (Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mutate(kindObserved, role, &observedPayload{Epoch: epoch, Observed: obs})
}

// Get returns a deep copy of one role, or ErrNoRole.
func (r *Registry) Get(role leadrole.Role) (Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Entry{}, ErrClosed
	}
	e, err := r.existing(role)
	if err != nil {
		return Entry{}, err
	}
	return e.clone(), nil
}

// List returns deep copies of all roles, sorted by the canonical role string.
func (r *Registry) List() ([]Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	out := make([]Entry, 0, len(r.roles))
	for _, e := range r.roles {
		out = append(out, e.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role.String() < out[j].Role.String() })
	return out, nil
}

// Coordinators returns deep copies of the coordinators of a lead, sorted by
// the canonical role string. lead must be a project lead role; a lead with no
// coordinator gives an empty list.
func (r *Registry) Coordinators(lead leadrole.Role) ([]Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	if lead.Kind() != leadrole.KindLead {
		return nil, invalid("%q is not a project lead", lead)
	}
	var out []Entry
	for role, e := range r.roles {
		if role.Kind() == leadrole.KindCoordinator && role.Parent() == lead {
			out = append(out, e.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role.String() < out[j].Role.String() })
	return out, nil
}

// CoordinatorStopped reports whether instance n of a coordinator is proven
// stopped: true when the coordinator is at instance n and shown stopped or
// ended with proof, or when a later instance exists (a later instance is
// launched only from a proven stop). It is false for an instance that was
// never launched. role must be a coordinator and n at least 1; ErrNoRole when
// the coordinator is not defined. The registry stores the proof text as given
// and does not check it.
func (r *Registry) CoordinatorStopped(role leadrole.Role, n uint64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false, ErrClosed
	}
	if role.Kind() != leadrole.KindCoordinator {
		return false, invalid("%q is not a coordinator", role)
	}
	if n == 0 {
		return false, invalid("instance numbers start at 1")
	}
	e, err := r.existing(role)
	if err != nil {
		return false, err
	}
	switch {
	case e.Epoch > n:
		return true, nil
	case e.Epoch < n:
		return false, nil
	}
	return (e.Shown.State == StateStopped || e.Shown.State == StateEnded) && e.Shown.Proof != "", nil
}

// Revision returns the sequence number of the last journal line: 0 for a
// registry with no mutation yet.
func (r *Registry) Revision() (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, ErrClosed
	}
	return r.seq, nil
}
