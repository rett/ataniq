package switcher

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
)

// maxReleaseEntries bounds the entries of releases/ the evidence reads.
const maxReleaseEntries = 10000

// maxManifestEvidence bounds a MANIFEST read as evidence.
const maxManifestEvidence = 64 << 10

// evidence is the highest counter this host shows it used or holds, apart
// from highest-counter, and where that was seen.
type evidence struct {
	counter uint64
	source  string
}

func (e *evidence) see(n uint64, source string) {
	if n > e.counter {
		e.counter, e.source = n, source
	}
}

// counterEvidence gathers what the host shows of the counters it used or
// holds, besides highest-counter: the releases current and previous point
// at and the counter of the last forward switch deploy.json records as
// having used its counter (the counters used), and, with releases, every
// release folder in releases/ but skip (the counter in its name and in its
// MANIFEST, when that can be read). A forward switch must be above all of
// it. What cannot be read (the links, deploy.json, the folder of releases)
// is an error, never taken as no evidence; a single MANIFEST that cannot be
// read still counts by its folder's name.
func (s *Switcher) counterEvidence(skip string, releases bool) (evidence, error) {
	var ev evidence
	for _, name := range []string{"current", "previous"} {
		t, ok, err := readLink(s.baseRoot, name)
		if err != nil {
			return ev, err
		}
		if ok {
			id, err := s.targetID(t)
			if err != nil {
				return ev, err
			}
			ev.see(id.Counter, name)
		}
	}
	if s.stateRoot != nil {
		rec, _, err := s.readRecord()
		if err != nil {
			return ev, err
		}
		if rec.CounterUsed && !rec.Rollback {
			ev.see(rec.Counter, DeployFile)
		}
	}
	if !releases {
		return ev, nil
	}
	d, err := s.relRoot.Open(".")
	if err != nil {
		return ev, err
	}
	defer d.Close()
	seen := 0
	for {
		ents, err := d.ReadDir(readDirBatch)
		for _, ent := range ents {
			if seen++; seen > maxReleaseEntries {
				return ev, failf(CodeTooManyEntries, "releases/ holds more than %d entries", maxReleaseEntries)
			}
			id, perr := ParseID(ent.Name())
			if perr != nil || ent.Name() == skip {
				continue
			}
			ev.see(id.Counter, "releases/"+id.Name)
			if data, err := readFileIn(s.relRoot, path.Join(id.Name, ManifestFile), maxManifestEvidence); err == nil {
				if m, err := ParseManifest(data); err == nil {
					ev.see(m.Counter, "releases/"+id.Name+"/"+ManifestFile)
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return ev, nil
		}
		if err != nil {
			return ev, err
		}
	}
}

// counterState checks highest-counter against the counters the host shows
// it used, before a switch or rollback records anything: a counter that
// cannot be read, one that is missing on a host that used counters, or one
// below a counter used (restored from an older state, or written wrong) is
// refused until recover writes it, with the evidence kept as it is.
func (s *Switcher) counterState(a *attempt) error {
	if s.stateRoot == nil {
		return nil
	}
	highest, present, err := s.readHighest()
	if err != nil {
		return err
	}
	used, err := s.counterEvidence("", false)
	if err != nil {
		return evidenceError(err)
	}
	switch {
	case !present && (a.oldExists || used.counter > 0):
		return failf(CodeCounterMissing, "%s is missing although this host used counter %d (%s); run ah-switch recover --counter N, N the highest counter this host ever switched to (at least %d)", HighestFile, used.counter, used.source, used.counter)
	case present && highest < used.counter:
		return failf(CodeCounterInvalid, "%s holds %d, below %d, the counter of %s; run ah-switch recover --counter N, N the highest counter this host ever switched to (at least %d)", HighestFile, highest, used.counter, used.source, used.counter)
	}
	return nil
}

// evidenceError is the refusal when the evidence cannot be read.
func evidenceError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		err = errors.New("missing")
	}
	return failf(CodeStateInvalid, "what this host shows of its counters cannot be read (%s); look at the state, then run ah-switch recover --counter N", describe(err))
}

// counterLine is how status shows the counter against its evidence.
func counterLine(highest uint64, ev evidence) string {
	if ev.counter > highest {
		return fmt.Sprintf("highest counter: %d, below %d (%s): run ah-switch recover --counter N, N at least %d", highest, ev.counter, ev.source, ev.counter)
	}
	return fmt.Sprintf("highest counter: %d", highest)
}
