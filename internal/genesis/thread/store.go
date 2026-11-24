package thread

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"sync"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
	"ataniq/internal/genesis/plan"
)

// Options are what a store is opened with. The zero value is the daemon's own:
// catalog v1, the system clock, the system's random source and no log.
type Options struct {
	// Catalog is the question catalog threads are started under; nil is
	// [catalog.V1].
	Catalog *catalog.Catalog
	// Clock is the clock of the store; nil is time.Now. A test fixes it.
	Clock func() time.Time
	// Rand is where thread ids come from; nil is crypto/rand.
	Rand io.Reader
	// Log receives the store's lines, which are fixed texts (see the package
	// documentation); nil discards them. It is called with no lock held.
	Log func(format string, a ...any)
}

// Store is the thread store. Make one with [Open]; it is safe for concurrent
// use.
type Store struct {
	cat   catalog.Catalog
	clock func() time.Time
	rand  io.Reader
	log   func(format string, a ...any)

	// mu serialises every call. It guards everything below.
	mu      sync.Mutex
	root    *os.Root
	closed  bool
	threads map[string]record
	// bad holds the names (ids) of files that are not served.
	bad     map[string]bool
	secrets int
	// compiler replaces the plan compiler where a test needs a fault of it;
	// nil is the compiler.
	compiler func(b brief.Brief) (plan.Plan, string, error)
}

// Open opens the store in dir, the folder of the threads: it makes it, mode
// 0700, when it is not there, refuses one that is a link, not the daemon
// user's or writable by group or other, reads every thread file strictly (a
// file that does not pass is logged and not served), expires the threads that
// were untouched for [Expiry] and keeps at most [MaxFiles] files. The error
// is an [*OpenError]: fixed text that never holds the folder's path, and a
// reason word.
func Open(dir string, o Options) (*Store, error) {
	s := &Store{clock: o.Clock, rand: o.Rand, log: o.Log, threads: map[string]record{}, bad: map[string]bool{}}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.rand == nil {
		s.rand = crand.Reader
	}
	if s.log == nil {
		s.log = func(string, ...any) {}
	}
	if o.Catalog != nil {
		s.cat = *o.Catalog
	} else {
		c, err := catalog.V1()
		if err != nil {
			return nil, openErr("catalog", "genesis: the question catalog does not load")
		}
		s.cat = c
	}
	if s.cat.Digest() == "" {
		return nil, openErr("catalog", "genesis: the question catalog was not loaded")
	}
	if dir == "" {
		return nil, openErr("no-folder", "genesis: no folder for the threads")
	}
	root, err := openFolder(dir)
	if err != nil {
		return nil, err
	}
	s.root = root
	t := &tx{s: s, now: stamp(s.clock())}
	if err := s.load(t); err != nil {
		_ = root.Close()
		var oe *OpenError
		if !errors.As(err, &oe) {
			// The write of a thread that expired as the store opened.
			err = openErr("file-write", "genesis: a thread that expired cannot be written")
		}
		return nil, err
	}
	t.flush()
	return s, nil
}

// OpenError is the error of [Open]: a fixed text, which never holds the
// folder's path, and Reason, a short word that names what failed, for a log
// line of fixed words:
//
//	catalog            the question catalog does not load
//	no-folder          no folder was given
//	folder-make        the folder cannot be made
//	folder-read        the folder cannot be read
//	folder-open        the folder cannot be opened
//	folder-replaced    the folder was replaced while it was opened
//	folder-link        the folder is a link
//	folder-not-folder  the path is not a folder
//	folder-owner       the folder is not the daemon user's
//	folder-mode        the folder is writable by group or other
//	too-many-files     the folder holds more than 64 entries
//	file-remove        an old thread file cannot be removed
//	file-write         a thread that expired cannot be written
type OpenError struct {
	Reason string
	text   string
}

func (e *OpenError) Error() string { return e.text }

func openErr(reason, text string) error { return &OpenError{Reason: reason, text: text} }

func stamp(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// openFolder makes the folder if it is not there, checks it, and opens it as a
// root, so that nothing is reached outside it.
func openFolder(dir string) (*os.Root, error) {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, openErr("folder-make", "genesis: the folder of the threads cannot be made")
		}
		fi, err = os.Lstat(dir)
	}
	if err != nil {
		return nil, openErr("folder-read", "genesis: the folder of the threads cannot be read")
	}
	if err := checkFolder(fi); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, openErr("folder-open", "genesis: the folder of the threads cannot be opened")
	}
	// What was opened is the folder looked at, and not what was put at its
	// path between the two.
	held, err := root.Stat(".")
	if err != nil || !os.SameFile(fi, held) || checkFolder(held) != nil {
		_ = root.Close()
		return nil, openErr("folder-replaced", "genesis: the folder of the threads was replaced while it was opened")
	}
	return root, nil
}

func checkFolder(fi os.FileInfo) error {
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return openErr("folder-link", "genesis: the folder of the threads is a link")
	case !fi.IsDir():
		return openErr("folder-not-folder", "genesis: the folder of the threads is not a folder")
	case !ownedByUs(fi):
		return openErr("folder-owner", "genesis: the folder of the threads is not the daemon user's")
	case fi.Mode().Perm()&0o022 != 0:
		return openErr("folder-mode", "genesis: the folder of the threads is writable by group or other")
	}
	return nil
}

// load reads the folder: the threads in it, then the expiry and the cap on
// the files.
func (s *Store) load(t *tx) error {
	d, err := s.root.Open(".")
	if err != nil {
		return openErr("folder-read", "genesis: the folder of the threads cannot be read")
	}
	entries, err := d.ReadDir(maxEntries + 1)
	_ = d.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return openErr("folder-read", "genesis: the folder of the threads cannot be read")
	}
	if len(entries) > maxEntries {
		return openErr("too-many-files", "genesis: the folder of the threads holds too many files")
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case len(name) == len(tmpOf("0123456789abcdef")) && name[:len(tmpPrefix)] == tmpPrefix && name[len(name)-len(tmpSuffix):] == tmpSuffix:
			// A write that a crash cut short: ours, and of no value.
			_ = s.root.Remove(name)
		case fileName.MatchString(name):
			s.loadFile(t, name)
		}
	}
	if err := t.expireAll(); err != nil {
		return err
	}
	t.prune()
	return t.removeFiles()
}

// loadFile reads one thread file. A file that does not pass is not served and
// is logged, once.
func (s *Store) loadFile(t *tx, name string) {
	id := name[:16]
	data, err := readFile(s.root, name)
	var r record
	if err == nil {
		r, err = decodeRecord(name, data)
	}
	if err == nil && r.catalog != s.cat.Digest() {
		err = bad("the thread was started under another catalog than the store's")
	}
	if err != nil {
		s.bad[id] = true
		why := "it cannot be read"
		var eb *errBad
		if errors.As(err, &eb) {
			why = eb.why
		}
		t.logf("genesis: the thread file %s is not served: %s", name, why)
		return
	}
	s.threads[id] = r
}

// Close ends the store. A call after it is refused [ErrClosed]. It may be
// called more than once.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.root.Close()
}

// tx is one call of the store, run with the mutex held: its clock reading, the
// log lines it will write and the files it will remove, both after it let go.
type tx struct {
	s      *Store
	now    time.Time
	logs   []string
	remove []string
}

func (t *tx) logf(format string, a ...any) { t.logs = append(t.logs, fmt.Sprintf(format, a...)) }

// flush writes the log lines of the call.
func (t *tx) flush() {
	for _, l := range t.logs {
		t.s.log("%s", l)
	}
	t.logs = nil
}

// removeFiles removes the files of the threads pruned by the call. Only the
// store's own opening does it with the mutex held, before any other call.
func (t *tx) removeFiles() error {
	for _, name := range t.remove {
		if err := t.s.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return openErr("file-remove", "genesis: an old thread file cannot be removed")
		}
	}
	t.remove = nil
	return nil
}

// run takes the mutex and runs one call. The log lines and the removal of
// pruned files come after the mutex is let go.
func (s *Store) run(ctx context.Context, fn func(t *tx) (ownerwire.SeatGenesisResult, error)) (ownerwire.SeatGenesisResult, error) {
	if err := ctx.Err(); err != nil {
		return ownerwire.SeatGenesisResult{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ownerwire.SeatGenesisResult{}, ErrClosed
	}
	t := &tx{s: s, now: stamp(s.clock())}
	res, err := fn(t)
	root, remove := s.root, t.remove
	s.mu.Unlock()
	t.flush()
	for _, name := range remove {
		// Not served already: a file that stays is read again, and pruned
		// again, when the store is next opened.
		_ = root.Remove(name)
	}
	return res, err
}

// get returns the live thread of id: one that is not there, is discarded, or
// expires now, is unknown.
func (t *tx) get(id string) (record, error) {
	r, ok := t.s.threads[id]
	if !ok || !isHex(id, 16) {
		return record{}, refuse(ownerwire.CodeGenesisUnknownThread)
	}
	if err := t.expire(&r); err != nil {
		return record{}, err
	}
	if r.state == StateDiscarded {
		return record{}, refuse(ownerwire.CodeGenesisUnknownThread)
	}
	return r, nil
}

// expire discards an open thread that was untouched for [Expiry].
func (t *tx) expire(r *record) error {
	if r.state != StateOpen || t.now.Sub(r.updated) < Expiry {
		return nil
	}
	d := t.discarded(*r)
	if err := t.put(d); err != nil {
		return err
	}
	*r = d
	t.logf("genesis: a thread expired")
	return nil
}

func (t *tx) expireAll() error {
	for id, r := range t.s.threads {
		if err := t.expire(&r); err != nil {
			return err
		}
		t.s.threads[id] = r
	}
	return nil
}

// discarded is the thread as it is once it is dropped: no answer is kept.
func (t *tx) discarded(r record) record {
	r.state, r.brief, r.frozen, r.updated = StateDiscarded, brief.New(r.catalog), "", t.now
	r.plan, r.planDigest = nil, ""
	return r
}

// put writes the thread, and only when that worked makes it the store's.
func (t *tx) put(r record) error {
	data, err := encodeRecord(r)
	if err != nil {
		return fmt.Errorf("genesis: the thread does not encode: %w", err)
	}
	if err := writeFile(t.s.root, r.id, data); err != nil {
		return errors.New("genesis: the thread cannot be written")
	}
	t.s.threads[r.id] = r
	return nil
}

// prune drops from the store the oldest finished or discarded threads while
// the files are more than [MaxFiles], and names their files for removal.
func (t *tx) prune() {
	for len(t.s.threads)+len(t.s.bad) > MaxFiles {
		var old []record
		for _, r := range t.s.threads {
			if r.state != StateOpen {
				old = append(old, r)
			}
		}
		if len(old) == 0 {
			return
		}
		sort.Slice(old, func(i, j int) bool {
			if !old[i].updated.Equal(old[j].updated) {
				return old[i].updated.Before(old[j].updated)
			}
			return old[i].id < old[j].id
		})
		delete(t.s.threads, old[0].id)
		t.remove = append(t.remove, fileOf(old[0].id))
	}
}

// newID chooses an id: 16 lower-case hexadecimal digits from the random
// source, none that a file of the folder already has.
func (t *tx) newID() (string, error) {
	for i := 0; i < 8; i++ {
		var b [8]byte
		if _, err := io.ReadFull(t.s.rand, b[:]); err != nil {
			return "", errors.New("genesis: no random bytes for a thread id")
		}
		id := hex.EncodeToString(b[:])
		if _, used := t.s.threads[id]; !used && !t.s.bad[id] {
			return id, nil
		}
	}
	return "", errors.New("genesis: no unused thread id was found")
}

// Start opens a new thread and answers its first round. It refuses a third
// open thread genesis-busy.
func (s *Store) Start(ctx context.Context) (ownerwire.SeatGenesisResult, error) {
	return s.run(ctx, func(t *tx) (ownerwire.SeatGenesisResult, error) {
		if err := t.expireAll(); err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		open := 0
		for _, r := range s.threads {
			if r.state == StateOpen {
				open++
			}
		}
		if open >= MaxOpen {
			return ownerwire.SeatGenesisResult{}, refuse(ownerwire.CodeGenesisBusy)
		}
		id, err := t.newID()
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		r := record{id: id, state: StateOpen, brief: brief.New(s.cat.Digest()), catalog: s.cat.Digest(), created: t.now, updated: t.now}
		res, err := view(s.cat, r, 0)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		if err := t.put(r); err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		t.prune()
		t.logf("genesis: a thread was started")
		return res, nil
	})
}

// Round answers where the thread stands: the question round it is at, or the
// summary when there is no question left; a finished thread answers its
// summary and digest.
func (s *Store) Round(ctx context.Context, id string) (ownerwire.SeatGenesisResult, error) {
	return s.run(ctx, func(t *tx) (ownerwire.SeatGenesisResult, error) {
		r, err := t.get(id)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		return view(s.cat, r, 0)
	})
}

// Summary answers the summary of the thread's brief as it stands, whether or
// not the walk is done.
func (s *Store) Summary(ctx context.Context, id string) (ownerwire.SeatGenesisResult, error) {
	return s.run(ctx, func(t *tx) (ownerwire.SeatGenesisResult, error) {
		r, err := t.get(id)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		return summaryView(s.cat, r, base(s.cat, r, 0))
	})
}

// Answer gives the answers of a round, which must be the round the thread is
// at, each to a question of it. A round the thread has already taken, or any
// round once the walk is done, is refused genesis-stale-round; a round it has
// not reached genesis-bad-answer. Every answer goes through the walker; one
// that fails fails the call and changes nothing. It answers the next round or
// the summary.
func (s *Store) Answer(ctx context.Context, id string, round int, answers []ownerwire.GenesisAnswer) (ownerwire.SeatGenesisResult, error) {
	return s.run(ctx, func(t *tx) (ownerwire.SeatGenesisResult, error) {
		r, err := t.get(id)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		if r.state == StateFinished {
			return ownerwire.SeatGenesisResult{}, refuse(ownerwire.CodeGenesisDone)
		}
		cur, done, err := catalog.Next(s.cat, r.brief, catalog.Limits{})
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		if len(answers) == 0 || len(answers) > ownerwire.MaxGenesisAnswers || round < 1 || round > ownerwire.MaxGenesisRounds {
			return ownerwire.SeatGenesisResult{}, refuse(ownerwire.CodeGenesisBadAnswer)
		}
		// A round already taken, or any round once the walk is done, is
		// the thread's state (a second tab, a post sent twice), not
		// misuse; a round the thread has not reached is.
		if done || round < cur.Number {
			return ownerwire.SeatGenesisResult{}, refuse(ownerwire.CodeGenesisStaleRound)
		}
		if round != cur.Number {
			return ownerwire.SeatGenesisResult{}, refuse(ownerwire.CodeGenesisBadAnswer)
		}
		b, reset := r.brief, 0
		seen := map[string]bool{}
		for _, a := range answers {
			if seen[a.Node] || !inRound(cur, a.Node) {
				return ownerwire.SeatGenesisResult{}, refuse(ownerwire.CodeGenesisBadAnswer)
			}
			seen[a.Node] = true
			wa, ok := walkerAnswer(a.Kind, a.IDs, a.Text, round)
			if !ok {
				return ownerwire.SeatGenesisResult{}, refuse(ownerwire.CodeGenesisBadAnswer)
			}
			nb, res, err := catalog.Apply(s.cat, b, a.Node, wa, t.now)
			if err != nil {
				return ownerwire.SeatGenesisResult{}, t.answerFailed(err)
			}
			b, reset = nb, reset+res.Reset
		}
		r.brief, r.updated = b, t.now
		return t.commit(r, reset)
	})
}

func inRound(r catalog.Round, node string) bool {
	for _, n := range r.Nodes {
		if n.ID == node {
			return true
		}
	}
	return false
}

// answerFailed is the error of an answer the walker refused. Text that looks
// like a secret is counted and logged, by its number only.
func (t *tx) answerFailed(err error) error {
	if secretShaped(err) {
		t.s.secrets++
		t.logf("genesis: an answer was refused as secret-shaped (%d since the store was opened)", t.s.secrets)
	}
	return mapAnswerError(err)
}

// commit writes the changed thread and answers its view, which is made first
// so that a thread that cannot be shown is not written.
func (t *tx) commit(r record, reset int) (ownerwire.SeatGenesisResult, error) {
	res, err := view(t.s.cat, r, reset)
	if err != nil {
		return ownerwire.SeatGenesisResult{}, err
	}
	if err := t.put(r); err != nil {
		return ownerwire.SeatGenesisResult{}, err
	}
	return res, nil
}

// Change replaces the answer to a question that has one, which clears the
// answers that depended on it; the result says how many in Reset.
func (s *Store) Change(ctx context.Context, id, node string, pick ownerwire.GenesisPick) (ownerwire.SeatGenesisResult, error) {
	return s.run(ctx, func(t *tx) (ownerwire.SeatGenesisResult, error) {
		r, err := t.get(id)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		if r.state == StateFinished {
			return ownerwire.SeatGenesisResult{}, refuse(ownerwire.CodeGenesisDone)
		}
		n, ok := s.cat.Node(node)
		f, set := r.brief.Get(n.Field)
		if !ok || !set || f.Node != node {
			return ownerwire.SeatGenesisResult{}, refuse(ownerwire.CodeGenesisBadAnswer)
		}
		wa, ok := walkerAnswer(pick.Kind, pick.IDs, pick.Text, r.brief.Rounds)
		if !ok {
			return ownerwire.SeatGenesisResult{}, refuse(ownerwire.CodeGenesisBadAnswer)
		}
		b, res, err := catalog.Apply(s.cat, r.brief, node, wa, t.now)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, t.answerFailed(err)
		}
		r.brief, r.updated = b, t.now
		return t.commit(r, res.Reset)
	})
}

// Finish freezes the brief and answers its digest. A thread that is finished
// answers the same again.
//
// When the brief is complete and a template serves its kind, the plan of the
// brief, revision 1, is compiled and stored with the frozen brief, in the
// same write. A brief that is not complete, or of a kind no template serves,
// is frozen as it is, with no plan: the owner may stop early. A plan the host
// fails to compile for a fault of its own does not stop the finish either: it
// is logged, and drawn when it is next asked for.
func (s *Store) Finish(ctx context.Context, id string) (ownerwire.SeatGenesisResult, error) {
	return s.run(ctx, func(t *tx) (ownerwire.SeatGenesisResult, error) {
		r, err := t.get(id)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		if r.state == StateFinished {
			return view(s.cat, r, 0)
		}
		d, err := brief.Digest(r.brief)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		r.state, r.frozen, r.updated = StateFinished, d, t.now
		p, pd, perr := s.compile(r.brief)
		if perr == nil {
			r.plan, r.planDigest = &p, pd
		}
		res, err := t.commit(r, 0)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		t.logf("genesis: a thread was finished")
		if _, refused := RefusalCode(perr); perr == nil {
			t.logf("genesis: a plan was drawn")
		} else if !refused {
			t.logf("genesis: a plan could not be drawn at finish")
		}
		return res, nil
	})
}

// templateKinds are the kinds a plan template serves.
var templateKinds = sync.OnceValues(func() (map[string]bool, error) {
	ts, err := plan.Templates()
	if err != nil {
		return nil, err
	}
	kinds := map[string]bool{}
	for _, t := range ts {
		kinds[t.Kind] = true
	}
	return kinds, nil
})

// hasTemplate reports whether a plan template serves the kind.
func hasTemplate(kind string) (bool, error) {
	kinds, err := templateKinds()
	if err != nil {
		return false, err
	}
	return kinds[kind], nil
}

// compile compiles the plan of a brief, revision 1, and its digest. A kind no
// template serves is refused genesis-no-template, whether or not the brief is
// complete (completing it would not help); a brief that is not complete
// genesis-not-ready. Any other error is the host's own fault.
func (s *Store) compile(b brief.Brief) (plan.Plan, string, error) {
	if s.compiler != nil {
		return s.compiler(b)
	}
	if b.Kind != "" {
		ok, err := hasTemplate(b.Kind)
		if err != nil {
			return plan.Plan{}, "", err
		}
		if !ok {
			return plan.Plan{}, "", refuse(ownerwire.CodeGenesisNoTemplate)
		}
	}
	p, err := plan.Compile(b, s.cat)
	if err != nil {
		return plan.Plan{}, "", mapAnswerError(err)
	}
	d, err := plan.Digest(p)
	if err != nil {
		return plan.Plan{}, "", err
	}
	return p, d, nil
}

// Plan answers the thread's plan. On an open thread whose brief is complete it
// is a preview: the plan is compiled for the read, revision 1, answered with
// stored false and kept nowhere, and the same brief gives the same plan and
// digest each time. On a finished thread it is the plan stored at finish,
// answered with stored true; a finished thread that has none and whose brief
// is complete (one finished before plans were stored) is given one now, which
// is stored, once, as finish would have. A brief that is not complete is
// refused genesis-not-ready, a kind no template serves genesis-no-template. A
// stored plan is never compiled again. It touches no thread: it writes only to
// the store, once, the plan of a finished thread that has none, and only when
// the plan is of the thread's frozen brief (else [ErrPlanBrief], and nothing
// is kept).
func (s *Store) Plan(ctx context.Context, id string) (ownerwire.SeatGenesisResult, error) {
	return s.run(ctx, func(t *tx) (ownerwire.SeatGenesisResult, error) {
		r, err := t.get(id)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		if r.plan != nil {
			return planView(s.cat, r, *r.plan, r.planDigest, true)
		}
		p, d, err := s.compile(r.brief)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		if r.state != StateFinished {
			return planView(s.cat, r, p, d, false)
		}
		if p.BriefDigest != r.frozen {
			t.logf("genesis: a plan drawn late did not match the thread's brief and was not kept")
			return ownerwire.SeatGenesisResult{}, ErrPlanBrief
		}
		r.plan, r.planDigest = &p, d
		res, err := planView(s.cat, r, p, d, true)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		if err := t.put(r); err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		t.logf("genesis: a plan was drawn")
		return res, nil
	})
}

// Discard drops the thread, and with it every answer it held. A finished
// thread is frozen and is refused genesis-done: its brief stays as it was
// finished.
func (s *Store) Discard(ctx context.Context, id string) (ownerwire.SeatGenesisResult, error) {
	return s.run(ctx, func(t *tx) (ownerwire.SeatGenesisResult, error) {
		r, err := t.get(id)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		if r.state == StateFinished {
			return ownerwire.SeatGenesisResult{}, refuse(ownerwire.CodeGenesisDone)
		}
		res, err := t.commit(t.discarded(r), 0)
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		t.logf("genesis: a thread was discarded")
		return res, nil
	})
}

// List answers the open threads, oldest first: where each stands (its state,
// round and progress) and none of its answers. A finished or discarded thread
// is not listed, and a thread that expires now is discarded first. It is a
// read: it touches no thread.
//
// It lists at most [ownerwire.MaxGenesisThreads] threads, the oldest, and
// never fails for more: the store never opens more than [MaxOpen] itself, but
// a folder may hold more open threads than that (files restored by hand from
// a copy), and the wire's list holds no more. The newer ones are still served
// by id, and Start is refused genesis-busy while [MaxOpen] or more are open,
// until the owner finishes or discards enough of them, or they expire.
func (s *Store) List(ctx context.Context) (ownerwire.SeatGenesisResult, error) {
	return s.run(ctx, func(t *tx) (ownerwire.SeatGenesisResult, error) {
		if err := t.expireAll(); err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		var open []record
		for _, r := range s.threads {
			if r.state == StateOpen {
				open = append(open, r)
			}
		}
		sort.Slice(open, func(i, j int) bool {
			if !open[i].created.Equal(open[j].created) {
				return open[i].created.Before(open[j].created)
			}
			return open[i].id < open[j].id
		})
		if len(open) > ownerwire.MaxGenesisThreads {
			open = open[:ownerwire.MaxGenesisThreads]
		}
		res := ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateThreads, Nodes: []ownerwire.GenesisNode{}, Threads: []ownerwire.GenesisThread{}}
		for _, r := range open {
			v, err := view(s.cat, r, 0)
			if err != nil {
				return ownerwire.SeatGenesisResult{}, err
			}
			res.Threads = append(res.Threads, ownerwire.GenesisThread{
				Thread: r.id, State: v.State, Round: v.Round, Of: v.Of, Filled: v.Progress.Filled, Total: v.Progress.Total,
			})
		}
		return res, nil
	})
}

// Genesis serves seat.genesis: it checks the params once more by the wire's
// own form and hands them to their verb. A refusal is a [*Refusal] with the
// gate's word; any other error is the host's own fault.
func (s *Store) Genesis(ctx context.Context, p ownerwire.SeatGenesisParams) (ownerwire.SeatGenesisResult, error) {
	if _, err := ownerwire.EncodeSeatGenesisParams(p); err != nil {
		return ownerwire.SeatGenesisResult{}, refuse(CodeBadParams)
	}
	switch p.Verb {
	case ownerwire.GenesisVerbStart:
		return s.Start(ctx)
	case ownerwire.GenesisVerbList:
		return s.List(ctx)
	case ownerwire.GenesisVerbRound:
		return s.Round(ctx, p.Thread)
	case ownerwire.GenesisVerbAnswer:
		return s.Answer(ctx, p.Thread, p.Round, p.Answers)
	case ownerwire.GenesisVerbChange:
		return s.Change(ctx, p.Thread, p.Node, *p.Answer)
	case ownerwire.GenesisVerbSummary:
		return s.Summary(ctx, p.Thread)
	case ownerwire.GenesisVerbFinish:
		return s.Finish(ctx, p.Thread)
	case ownerwire.GenesisVerbDiscard:
		return s.Discard(ctx, p.Thread)
	case ownerwire.GenesisVerbPlan:
		return s.Plan(ctx, p.Thread)
	}
	return ownerwire.SeatGenesisResult{}, refuse(CodeBadParams)
}
