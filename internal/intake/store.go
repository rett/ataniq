package intake

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"ataniq/internal/exchange/safefs"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/intake/verify"
)

const (
	// maxRecordBytes is the largest record file that is read.
	maxRecordBytes = 1 << 20
	// maxStoreEntries is the most names read from the records folder.
	maxStoreEntries = 100000
	// nameTimeFormat is the time in a file name: fixed width, UTC, so that
	// names sort by time.
	nameTimeFormat = "20060102T150405.000000000Z"
)

// recordName matches the name of a record file: time, kind, random part.
var recordName = regexp.MustCompile(`^(\d{8}T\d{6}\.\d{9}Z)-(verification|intake)-([0-9a-f]{16})\.json$`)

// ErrStore means that the records folder or a record in it cannot be used. A
// message names the file of a record and never a lead's text.
var ErrStore = errors.New("intake: records store")

// Store is the owner's records folder: one JSON file for each record.
//
// The folder is the owner's. It must not be inside the exchange mirror or
// inside a state folder that a lead can read or write, and a lead's sandbox
// must not have it mounted. This package cannot know the layout, so the
// command that opens a Store checks it.
//
// Records are never overwritten or deleted by this package. A record is
// written to a temporary name in the folder, synced, and then given its name
// without replacing a file that is there. Reading goes through safefs: only
// regular files are read, a symlink in the folder is not followed, a FIFO does
// not block, a record has at most 1 MiB, and a file whose name is not a
// record's name is ignored. A file with a record's name that cannot be read or
// decoded is an error that names it, and no later step goes on without it:
// a record that cannot be read could be the failure that withdraws a pass.
type Store struct {
	root *safefs.Root
	dir  folder

	mu   sync.Mutex
	last time.Time // the newest name time handed out
	// now is the clock of the names of verification records.
	now func() time.Time
}

// OpenStore opens the records folder at dir. dir must be an absolute, clean
// path of a real folder (not a symlink) that the caller owns, with mode 0700.
// Anything else is refused with an error that matches [ErrStore]. It works on
// Linux only.
func OpenStore(dir string) (*Store, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, fmt.Errorf("%w: the folder is not an absolute, clean path", ErrStore)
	}
	root, err := safefs.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	f, err := openFolder(dir, root)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return &Store{root: root, dir: f, now: time.Now}, nil
}

// Close releases the folder. It does not remove anything.
func (s *Store) Close() error {
	return errors.Join(s.root.Close(), s.dir.close())
}

// nameTime returns t in UTC, moved forward by a nanosecond when needed so that
// every name of this Store is later than the one before it: the order of
// names is the order of records.
func (s *Store) nameTime(t time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	t = t.UTC().Truncate(time.Nanosecond)
	if !t.After(s.last) {
		t = s.last.Add(time.Nanosecond)
	}
	s.last = t
	return t
}

// newBase returns the part of a file name that the record file and its
// output file share: time, kind and a random part.
func (s *Store) newBase(t time.Time, kind string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("%w: no random bytes: %w", ErrStore, err)
	}
	return s.nameTime(t).Format(nameTimeFormat) + "-" + kind + "-" + hex.EncodeToString(b[:]), nil
}

// put writes data under a new name, exclusively and synced.
func (s *Store) put(name string, data []byte) error {
	if err := s.dir.create(name, data); err != nil {
		return fmt.Errorf("%w: write %s: %w", ErrStore, name, err)
	}
	return nil
}

// PutVerification stores the result of a verification run as a record in the
// folder and returns it. gates are the gates that were run; res.PlanDigest
// must be the digest of them. The record holds the SHA-256 and the length of
// the output, not the output.
//
// The raw output is also written next to the record as a separate file with
// the same name and the suffix ".out", with the same rules (a new file,
// synced, never replaced). It is text that a lead influenced. This package
// never reads it back and never parses it; a person who looks at it shows it
// only through leadtext. It is written first, so that a record never names an
// output that is missing. A run with no output has no such file.
//
// PutVerification records what it is given, also a failure. Whether a record
// allows an integration is decided by [Integrate].
func (s *Store) PutVerification(project string, res verify.Result, gates []verify.Gate) (VerificationRecord, error) {
	if !sandboxplan.ValidName(project) {
		return VerificationRecord{}, fmt.Errorf("%w: the project name is not valid", ErrStore)
	}
	if err := checkRefName(res.TipRef, "refs/leads/"); err != nil {
		return VerificationRecord{}, err
	}
	sum := sha256.Sum256(res.Output)
	rec := VerificationRecord{
		Version: recordVersion, Project: project, TipRef: res.TipRef, Tip: res.Tip,
		PlanDigest: res.PlanDigest, Gates: slices.Clone(gates),
		Passed: res.Passed, Exit: res.Exit, Stopped: res.Stopped,
		OutputSHA256: hex.EncodeToString(sum[:]), OutputBytes: int64(len(res.Output)), OutputCut: res.OutputCut,
		Started: res.Started, DurationMS: res.Duration.Milliseconds(),
	}
	if err := rec.check(); err != nil {
		return VerificationRecord{}, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if verify.PlanDigest(gates) != res.PlanDigest {
		return VerificationRecord{}, fmt.Errorf("%w: the plan digest of the result is not the digest of the gates", ErrStore)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return VerificationRecord{}, fmt.Errorf("%w: %w", ErrStore, err)
	}
	base, err := s.newBase(s.now(), "verification")
	if err != nil {
		return VerificationRecord{}, err
	}
	if len(res.Output) > 0 {
		if err := s.put(base+".out", res.Output); err != nil {
			return VerificationRecord{}, err
		}
	}
	if err := s.put(base+".json", append(data, '\n')); err != nil {
		return VerificationRecord{}, err
	}
	return rec, nil
}

// putIntake stores an intake record. The name takes its time from the record.
func (s *Store) putIntake(rec IntakeRecord) error {
	if err := rec.check(); err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	base, err := s.newBase(rec.Time, "intake")
	if err != nil {
		return err
	}
	return s.put(base+".json", append(data, '\n'))
}

// recordFile is one record file of the folder.
type recordFile struct {
	name string
	kind string
}

// recordFiles lists the files whose names are record names, oldest first.
// Other names are ignored without being opened.
func (s *Store) recordFiles() ([]recordFile, error) {
	names, err := s.dir.names(maxStoreEntries)
	if err != nil {
		return nil, fmt.Errorf("%w: list: %w", ErrStore, err)
	}
	var files []recordFile
	for _, n := range names {
		if m := recordName.FindStringSubmatch(n); m != nil {
			files = append(files, recordFile{name: n, kind: m[2]})
		}
	}
	slices.SortFunc(files, func(a, b recordFile) int {
		switch {
		case a.name < b.name:
			return -1
		case a.name > b.name:
			return 1
		}
		return 0
	})
	return files, nil
}

// read reads and decodes one record file into v. The name is one that
// matched recordName, so it is safe to show.
func (s *Store) read(f recordFile, v any) error {
	data, err := s.root.ReadFile(f.name, maxRecordBytes)
	if err != nil {
		return fmt.Errorf("%w: record %s: %w", ErrStore, f.name, err)
	}
	if err := strictDecode(data, v); err != nil {
		return fmt.Errorf("%w: record %s is malformed: %w", ErrStore, f.name, err)
	}
	return nil
}

// Verifications returns every verification record for the tip, oldest first.
// The tip is a full commit id. Every record file in the folder is read, also
// those of other tips: one that cannot be read or decoded is an error that
// names it.
func (s *Store) Verifications(tip string) ([]VerificationRecord, error) {
	files, err := s.recordFiles()
	if err != nil {
		return nil, err
	}
	var out []VerificationRecord
	for _, f := range files {
		if f.kind != "verification" {
			continue
		}
		var rec VerificationRecord
		if err := s.read(f, &rec); err != nil {
			return nil, err
		}
		if rec.Tip == tip {
			out = append(out, rec)
		}
	}
	return out, nil
}

// Intakes returns every intake record, oldest first. A record file that
// cannot be read or decoded is an error that names it.
func (s *Store) Intakes() ([]IntakeRecord, error) {
	files, err := s.recordFiles()
	if err != nil {
		return nil, err
	}
	var out []IntakeRecord
	for _, f := range files {
		if f.kind != "intake" {
			continue
		}
		var rec IntakeRecord
		if err := s.read(f, &rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}
