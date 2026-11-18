//go:build linux && amd64

package harness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/plan"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
)

// CaseS21 is the case of the planted bind paths (the design, section 7.3).
const CaseS21 = "S21"

// PlantAsideSuffix is appended to the name of an entry the case sets aside
// while it plants another in its place, in the same folder. The case refuses
// to plant at a path whose set-aside name is taken: after a crash in the middle
// of a plant, that entry is the original.
const PlantAsideSuffix = ".ataniq-s21-aside"

// The kinds of plant. A symbolic link points outside the scratch folders; a
// wrong type is a regular file where a folder belongs and a folder where a
// file belongs.
const (
	PlantSymlink   = "symbolic-link"
	PlantWrongType = "wrong-type"
)

// The ids of the results of case S21. They are the harness's own facts, not
// the probe's.
const (
	resultS21Refused      = "planted-paths-refused"
	resultS21RefusedOther = "planted-paths-refused-other-runtime"
	resultS21Restored     = "planted-paths-restored"
)

// requests says the configuration lists the case.
func (c *Config) requests(name string) bool {
	for _, n := range c.Cases {
		if n == name {
			return true
		}
	}
	return false
}

// onlyPlanted says case S21 is the only case requested: nothing else needs a
// launch, and S21 never launches.
func (c *Config) onlyPlanted() bool { return len(c.Cases) == 1 && c.requests(CaseS21) }

// plantTarget is one path of the start walk: a bind target that lies in a
// folder the lead writes, or the folder those of the tree's .git lie in.
type plantTarget struct {
	inside, host, rootDir string
	// dir says a folder belongs there; else a regular file.
	dir bool
}

// walkedPaths lists the paths the start walk of rule B1 looks at, for the
// runtime of the inputs: every mount point of mountpoints.List, and before the
// first of the tree's .git rows the .git folder itself, which the walk looks at
// too and which holds no mount point of its own. mountpoints keeps the list of
// the walk to itself (its walkList is not exported), so this is derived from
// List the way walkList derives it; an exported list would be better, because
// a path added to the walk would not be planted here until this is changed.
func walkedPaths(in profile.Inputs) ([]plantTarget, error) {
	pts, err := mountpoints.List(in)
	if err != nil {
		return nil, err
	}
	var out []plantTarget
	seen := map[string]bool{}
	for _, p := range pts {
		if rel := path.Dir(p.Rel); p.Git && rel != "." && !seen[p.RootDir+"/"+rel] {
			seen[p.RootDir+"/"+rel] = true
			out = append(out, plantTarget{inside: path.Dir(p.Inside), host: p.RootDir + "/" + rel, rootDir: p.RootDir, dir: true})
		}
		out = append(out, plantTarget{inside: p.Inside, host: p.HostPath, rootDir: p.RootDir, dir: p.Dir})
	}
	return out, nil
}

// entryPlant plants one entry at a path of a scratch folder and takes it away
// again. The folder the path lies in is opened from the state root's handle
// (one lookup that follows no link), the original is set aside by a rename in
// that same folder, and every later step is beneath that folder's descriptor
// and names one entry of it: nothing is looked up by a path that follows a
// link.
type entryPlant struct {
	host, aside string
	root        *safefile.Root
	dir         *safefile.Handle
	name        string

	hadOrig  bool
	orig     safefile.Info
	asideSet bool
	// planted is what was made at the name, and plantedAt its identity.
	planted   fs.FileMode
	plantMade bool
	plantedAt safefile.Info
}

func openEntry(state *safefile.Root, stateRoot, host string) (*entryPlant, error) {
	parent, name := path.Dir(host), path.Base(host)
	rel, ok := strings.CutPrefix(parent, stateRoot+"/")
	if !ok || rel == "" {
		return nil, fmt.Errorf("%s does not lie below the state root %s", host, stateRoot)
	}
	root, err := state.SubRoot(rel)
	if err != nil {
		return nil, fmt.Errorf("open the folder %s: %w", parent, err)
	}
	dir, err := root.OpenDirHandle(".")
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("open the folder %s: %w", parent, err)
	}
	return &entryPlant{host: host, name: name, aside: name + PlantAsideSuffix, root: root, dir: dir}, nil
}

func (e *entryPlant) close() {
	e.dir.Close()
	e.root.Close()
}

// plantContent is what a planted regular file holds.
const plantContent = "planted by the harness, case S21\n"

// plant sets the original aside (when there is one) and makes the entry. A
// failure leaves nothing behind that restore cannot undo.
func (e *entryPlant) plant(kind string, dir bool, linkTarget string) error {
	switch _, err := e.root.Lstat(e.aside); {
	case err == nil:
		return fmt.Errorf("an entry named %s%s already exists beside %s: it is the original entry, which a case that did not finish set aside; if what is at %s is a plant, remove it, and rename %s%s back to %s", e.name, PlantAsideSuffix, e.host, e.host, e.name, PlantAsideSuffix, e.name)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("look at %s%s: %w", e.name, PlantAsideSuffix, err)
	}
	switch info, err := e.root.Lstat(e.name); {
	case err == nil:
		e.hadOrig, e.orig = true, info
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("look at %s: %w", e.host, err)
	}
	if e.hadOrig {
		if err := e.root.RenameNoReplace(e.name, e.aside); err != nil {
			return fmt.Errorf("set %s aside: %w", e.host, err)
		}
		e.asideSet = true
	}
	fd, err := e.dir.Fd()
	if err != nil {
		return err
	}
	switch {
	case kind == PlantSymlink:
		e.planted, err = fs.ModeSymlink, unix.Symlinkat(linkTarget, fd, e.name)
	case dir:
		// A regular file where a folder belongs.
		e.planted, err = 0, e.root.WriteFileExclusive(e.name, []byte(plantContent), 0o600)
	default:
		// A folder where a file belongs.
		e.planted, err = fs.ModeDir, unix.Mkdirat(fd, e.name, 0o700)
	}
	if err != nil {
		return fmt.Errorf("plant at %s: %w", e.host, err)
	}
	e.plantMade = true
	info, err := e.root.Lstat(e.name)
	if err != nil {
		return fmt.Errorf("look at what was planted at %s: %w", e.host, err)
	}
	e.plantedAt = info
	return nil
}

// restore takes the planted entry away and puts the original back, and then
// looks at the result: the original has its type, mode, owner, device and inode
// again (a rename keeps them), or the name is absent as it was, and the
// set-aside name is free. The planted entry is removed only when it is still
// what was planted. The error names the path that is left planted or not
// restored.
func (e *entryPlant) restore() error {
	if e.plantMade {
		info, err := e.root.Lstat(e.name)
		switch {
		case err != nil:
			return fmt.Errorf("%s is left planted: cannot look at it: %w", e.host, err)
		case info.Type() != e.planted || info.Ino != e.plantedAt.Ino || info.Dev != e.plantedAt.Dev:
			return fmt.Errorf("%s is left planted: what is there is not what the harness planted, so it is not removed", e.host)
		}
		fd, err := e.dir.Fd()
		if err != nil {
			return fmt.Errorf("%s is left planted: %w", e.host, err)
		}
		flags := 0
		if e.planted == fs.ModeDir {
			flags = unix.AT_REMOVEDIR
		}
		if err := unix.Unlinkat(fd, e.name, flags); err != nil {
			return fmt.Errorf("%s is left planted: cannot remove it: %w", e.host, err)
		}
		e.plantMade = false
	}
	if e.asideSet {
		if err := e.root.RenameNoReplace(e.aside, e.name); err != nil {
			return fmt.Errorf("%s is not restored: the original is at %s%s: %w", e.host, e.name, PlantAsideSuffix, err)
		}
		e.asideSet = false
	}
	if _, err := e.root.Lstat(e.aside); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s is not restored: %s%s is still there (%v)", e.host, e.name, PlantAsideSuffix, err)
	}
	info, err := e.root.Lstat(e.name)
	switch {
	case !e.hadOrig && errors.Is(err, fs.ErrNotExist):
		return nil
	case !e.hadOrig:
		return fmt.Errorf("%s is not restored: it was absent and something is there (%v)", e.host, err)
	case err != nil:
		return fmt.Errorf("%s is not restored: cannot look at it: %w", e.host, err)
	case info.Mode != e.orig.Mode || info.Ino != e.orig.Ino || info.Dev != e.orig.Dev || info.UID != e.orig.UID || info.GID != e.orig.GID:
		return fmt.Errorf("%s is not restored: it is not the entry that was set aside", e.host)
	}
	return nil
}

// startStep is what one check of a start found.
type startStep struct {
	name     string
	refused  bool
	problems []*mountpoints.Problem
	text     string
}

func reportStep(name string, rep mountpoints.Report, err error) startStep {
	s := startStep{name: name}
	switch {
	case err != nil:
		s.refused, s.text = true, err.Error()
	case !rep.OK():
		s.refused, s.problems, s.text = true, rep.Problems, rep.Problems[0].Error()
	}
	return s
}

// startChecks makes the checks a start makes before a launch, for the inputs
// given and as the real steps do: the own-clone check, the walk of the mount
// points (the start walk, through Options.CheckMountPoints), the resolution of
// the bind sources, the plan's own check and the check of every bind source
// the plan binds. It launches nothing and writes nothing. With full false it
// makes the walk alone, which is all that is known about the start of another
// runtime (see plantedPaths).
func (a *attempt) startChecks(in profile.Inputs, full bool) []startStep {
	var out []startStep
	if full {
		rep, err := mountpoints.CheckOwnClone(in, a.opt.UID)
		out = append(out, reportStep(StepOwnClone, rep, err))
	}
	rep, err := a.opt.CheckMountPoints(in)
	out = append(out, reportStep(StepCheck, rep, err))
	if !full {
		return out
	}
	res, err := mountpoints.ResolveSources(in, a.opt.Resolver)
	out = append(out, reportStep(StepResolve, res.Report, err))
	if probs := plan.Verify(a.pl, in, a.planOpt); len(probs) > 0 {
		out = append(out, startStep{name: StepVerify, refused: true, text: probs[0].String()})
	} else {
		out = append(out, startStep{name: StepVerify})
	}
	if err != nil || !res.Report.OK() {
		return out // there are no rows to check
	}
	rows, err := a.planBindRowsOf(res)
	if err != nil {
		return append(out, startStep{name: StepBindSources, refused: true, text: err.Error()})
	}
	return append(out, reportStep(StepBindSources, a.opt.CheckBindSources(rows), nil))
}

// plantItem is one plant to make: a path, the kind, and the start it is
// checked against.
type plantItem struct {
	target plantTarget
	kind   string
	other  bool
	in     profile.Inputs
	// baseline is what the start walk found before any plant, for this
	// runtime.
	baseline mountpoints.Report
}

func (k plantItem) expected() string {
	if k.target.dir {
		return WalkFolder
	}
	return WalkFile
}

// kindName names the kind of a problem for the evidence.
func kindName(p *mountpoints.Problem) string {
	switch {
	case errors.Is(p, mountpoints.ErrSymlink):
		return "symbolic-link"
	case errors.Is(p, mountpoints.ErrWrongType):
		return "wrong-type"
	case errors.Is(p, mountpoints.ErrMissing):
		return "missing"
	}
	return "other"
}

// wantKind is the kind of problem the walk must report for a plant.
func wantKind(kind string) error {
	if kind == PlantSymlink {
		return mountpoints.ErrSymlink
	}
	return mountpoints.ErrWrongType
}

// otherRuntimeInputs are the inputs of a start of the other runtime, derived
// from the configured ones and from what the profile gives for a runtime: the
// walk looks at the bind table, which the sources do not change, so the
// sources are the configured ones. The managed-settings path is the profile's
// default for the runtime, and where the profile has none it is the configured
// one, which the walk does not read.
func (a *attempt) otherRuntimeInputs() (profile.Inputs, error) {
	in := a.in
	in.Runtime = a.in.Runtime.Other()
	if !in.Runtime.Valid() {
		return in, errors.New("the configured runtime has no other runtime")
	}
	if in.ManagedSettingsInside = profile.ManagedSettingsDefaultInside(in.Runtime); in.ManagedSettingsInside == "" {
		in.ManagedSettingsInside = a.in.ManagedInside()
	}
	if err := in.Validate(); err != nil {
		return in, err
	}
	return in, nil
}

// plantedPaths is case S21 (the design, section 7.3): at every path the start
// walk looks at, plant a symbolic link and then an entry of the wrong type, and
// run the checks of a start; record whether it was refused, the problem and
// the path it names, and that nothing was launched; put the original back and
// look at the result before the next plant. It never launches, and it is made
// by the harness from outside as a stand-in for a lead that plants from inside.
// The folders are as found when it returns, or the error says which path is
// left planted. It runs after the plan is built and before the spec, so the
// steps that follow see the folders as they were.
//
// The start of the configured runtime is checked as a whole; the start of the
// other runtime (Q24(a): S21 holds across a runtime switch) by the walk alone,
// at the paths in the folders the walk of the configured runtime names: the
// .git paths and the config folder's place in the home, which both runtimes
// have. The places in the other runtime's own config folder are not covered:
// this run neither prepares nor writes in that folder (the layout derives it,
// and the case plants only in the folders the configured runtime's walk names),
// so they need a second run with the other runtime's configuration.
func (a *attempt) plantedPaths(ctx context.Context) error {
	ev := &PlantedPathsEvidence{Plants: []PlantFact{}, NotCovered: []PlantNotCovered{}}
	a.planted = ev
	rep, err := a.opt.CheckMountPoints(a.in)
	switch {
	case err != nil:
		ev.NotRun = "the start walk refused the inputs before any plant: " + assert.EscapeDetail(err.Error())
		return nil
	case !rep.OK():
		ev.NotRun = "the start walk found problems before any plant, so a refusal could not be told from them: " + assert.EscapeDetail(rep.Problems[0].Error())
		return nil
	}
	targets, err := walkedPaths(a.in)
	if err != nil {
		ev.NotRun = "the paths of the start walk could not be listed: " + assert.EscapeDetail(err.Error())
		return nil
	}
	state, err := safefile.OpenRoot(a.cfg.State.Root)
	if err != nil {
		ev.NotRun = "the state root could not be opened: " + assert.EscapeDetail(err.Error())
		return nil
	}
	defer state.Close()

	var items []plantItem
	roots := map[string]bool{}
	for _, t := range targets {
		roots[t.rootDir] = true
		for _, k := range []string{PlantSymlink, PlantWrongType} {
			items = append(items, plantItem{target: t, kind: k, in: a.in, baseline: rep})
		}
	}
	items = append(items, a.otherRuntimeItems(ev, roots)...)

	var fatal error
	for _, it := range items {
		var f PlantFact
		switch {
		case fatal != nil:
			f = a.plantFact(it)
			f.Detail = "not planted: the case ended because " + assert.EscapeDetail(fatal.Error())
		case ctx.Err() != nil:
			f = a.plantFact(it)
			f.Detail = "not planted: the run was interrupted"
		default:
			f, fatal = a.plantAndCheck(state, it)
		}
		ev.Plants = append(ev.Plants, f)
	}
	ev.Launched = a.launched
	if fatal != nil {
		return fatal
	}
	if ctx.Err() != nil {
		return errors.New("the run was interrupted")
	}
	// The folders are as found: the start walk that was clean before the first
	// plant is clean again.
	rep, err = a.opt.CheckMountPoints(a.in)
	clean := err == nil && rep.OK()
	ev.FoldersAsFound = &clean
	if !clean {
		return fmt.Errorf("the start walk is not clean after the case restored every path: %v %v", err, rep.Err())
	}
	ev.Ran = true
	return nil
}

// otherRuntimeItems makes the items of the other runtime's start: the paths its
// walk looks at that lie in a folder the configured runtime's walk also looks
// at. Any other is recorded as not covered, with the reason: this run neither
// prepares nor writes in that folder, so nothing is planted there.
func (a *attempt) otherRuntimeItems(ev *PlantedPathsEvidence, roots map[string]bool) []plantItem {
	other := a.in.Runtime.Other()
	ev.OtherRuntime = string(other)
	const needs = "needs a second run with the other runtime's configuration"
	in, err := a.otherRuntimeInputs()
	if err != nil {
		ev.NotCovered = append(ev.NotCovered, PlantNotCovered{Runtime: string(other), Reason: "the inputs of a start of the other runtime cannot be made from this configuration (" + assert.EscapeDetail(err.Error()) + "): it " + needs})
		return nil
	}
	targets, err := walkedPaths(in)
	if err != nil {
		ev.NotCovered = append(ev.NotCovered, PlantNotCovered{Runtime: string(other), Reason: "the paths of the other runtime's walk cannot be listed (" + assert.EscapeDetail(err.Error()) + "): it " + needs})
		return nil
	}
	base, err := a.opt.CheckMountPoints(in)
	if err != nil {
		ev.NotCovered = append(ev.NotCovered, PlantNotCovered{Runtime: string(other), Reason: "the other runtime's walk refused its inputs (" + assert.EscapeDetail(err.Error()) + "): it " + needs})
		return nil
	}
	var items []plantItem
	for _, t := range targets {
		if !roots[t.rootDir] {
			ev.NotCovered = append(ev.NotCovered, PlantNotCovered{Runtime: string(other), Inside: t.inside, Path: t.host, Reason: "lies in " + t.rootDir + ", a folder this run neither prepares nor writes in (it plants only in the folders the walk of the configured runtime names): it " + needs})
			continue
		}
		for _, k := range []string{PlantSymlink, PlantWrongType} {
			items = append(items, plantItem{target: t, kind: k, other: true, in: in, baseline: base})
		}
	}
	return items
}

// plantFact is the record of an item before anything is known: not checked.
func (a *attempt) plantFact(it plantItem) PlantFact {
	return PlantFact{
		Runtime: string(it.in.Runtime), OtherRuntime: it.other, Inside: it.target.inside, Path: it.target.host,
		Expected: it.expected(), Plant: it.kind, Status: assert.StatusNotChecked,
	}
}

// plantAndCheck makes one plant, runs the checks of a start against it, and
// takes it away. The error is set when the path could not be restored: the
// case ends there.
func (a *attempt) plantAndCheck(state *safefile.Root, it plantItem) (PlantFact, error) {
	f := a.plantFact(it)
	for _, p := range it.baseline.Problems {
		if p.Path == it.target.host && errors.Is(p, wantKind(it.kind)) {
			f.Detail = "not planted: the start walk already reports this problem at the path before the plant"
			return f, nil
		}
	}
	e, err := openEntry(state, a.cfg.State.Root, it.target.host)
	if err != nil {
		f.Detail = "not planted: " + assert.EscapeDetail(err.Error())
		return f, nil
	}
	defer e.close()
	// The link points outside the scratch folders: at the attempt folder.
	made := e.plant(it.kind, it.target.dir, a.dir)
	if made != nil {
		f.Detail = "not planted: " + assert.EscapeDetail(made.Error())
		if e.asideSet || e.plantMade {
			if rerr := e.restore(); rerr != nil {
				f.Detail += "; " + assert.EscapeDetail(rerr.Error())
				return f, rerr
			}
		}
		return f, nil
	}
	f.Planted = true
	switch {
	case it.kind == PlantSymlink:
		f.PlantedAs = "a symbolic link to " + a.dir
	case it.target.dir:
		f.PlantedAs = "a regular file"
	default:
		f.PlantedAs = "a folder"
	}
	steps := a.startChecks(it.in, !it.other)
	f.Launched = a.launched
	a.judgePlant(&f, it, steps)
	if rerr := e.restore(); rerr != nil {
		restored := false
		f.Restored = &restored
		f.Detail += "; not restored: " + assert.EscapeDetail(rerr.Error())
		f.Status = assert.StatusNotChecked
		return f, rerr
	}
	restored := true
	f.Restored = &restored
	return f, nil
}

// judgePlant turns what the checks found into the fact. The start walk (the
// check of the design, section 3.6) decides: it must refuse with a problem of
// the kind of the plant that names the planted path. A start that another
// check refuses and the walk does not is a plant the walk missed.
func (a *attempt) judgePlant(f *PlantFact, it plantItem, steps []startStep) {
	refused := false
	var walk *startStep
	for i, s := range steps {
		f.ChecksRun = append(f.ChecksRun, s.name)
		if s.refused {
			refused = true
			f.RefusedBy = append(f.RefusedBy, s.name)
		}
		if s.name == StepCheck {
			walk = &steps[i]
		}
	}
	f.StartRefused = &refused
	var hit *mountpoints.Problem
	if walk != nil {
		for _, p := range walk.problems {
			if p.Path == it.target.host {
				hit = p
				break
			}
		}
	}
	if hit != nil {
		f.ProblemKind, f.ProblemPath = kindName(hit), hit.Path
	}
	switch {
	case !refused:
		f.Status, f.Detail = assert.StatusFailed, "the start was not refused"
	case hit == nil:
		f.Status = assert.StatusFailed
		f.Detail = "the start walk did not refuse with a problem that names the planted path"
		if walk != nil && walk.text != "" {
			f.Detail += "; it found: " + assert.EscapeDetail(walk.text)
		}
	case !errors.Is(hit, wantKind(it.kind)):
		f.Status = assert.StatusFailed
		f.Detail = "the start walk named the planted path with the kind " + f.ProblemKind + ", not " + it.kind
	default:
		f.Status = assert.StatusOK
		f.Detail = "refused: " + assert.EscapeDetail(hit.Error())
	}
}

// s21Results turns the facts of the case into its results. A leg none of whose
// plants was made is not-checked, never ok; one plant that was not refused with
// its path makes the leg failed.
func s21Results(ev *PlantedPathsEvidence) []CaseResult {
	if ev == nil {
		return nil
	}
	leg := func(id string, other bool) CaseResult {
		st, n := assert.StatusOK, 0
		for _, p := range ev.Plants {
			if p.OtherRuntime != other {
				continue
			}
			n++
			switch p.Status {
			case assert.StatusFailed:
				st = assert.StatusFailed
			case assert.StatusNotChecked:
				if st == assert.StatusOK {
					st = assert.StatusNotChecked
				}
			}
		}
		if n == 0 || (!ev.Ran && st == assert.StatusOK) {
			st = assert.StatusNotChecked
		}
		return CaseResult{ID: id, Status: st}
	}
	restored, made := assert.StatusOK, 0
	for _, p := range ev.Plants {
		if p.Restored == nil {
			continue
		}
		made++
		if !*p.Restored {
			restored = assert.StatusFailed
		}
	}
	if made == 0 {
		restored = assert.StatusNotChecked
	} else if ev.FoldersAsFound != nil && !*ev.FoldersAsFound {
		restored = assert.StatusFailed
	}
	return []CaseResult{leg(resultS21Refused, false), leg(resultS21RefusedOther, true), {ID: resultS21Restored, Status: restored}}
}
