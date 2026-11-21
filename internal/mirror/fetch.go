package mirror

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/safefs"
	"ataniq/internal/exchange/sandboxplan"
)

var (
	// ErrBusy is returned by a fetch when another fetch into the same mirror
	// holds the lock.
	ErrBusy = errors.New("mirror: another fetch into this mirror is running")
	// ErrHostileTree is returned by a fetch when the owner-side check of the
	// lead's clone refuses it: a folder on the way that is not a real folder,
	// a ".git" that is not a real folder, or a file that sends git
	// elsewhere. The error text says which check failed and holds no name
	// from the clone.
	ErrHostileTree = errors.New("mirror: the lead's clone is refused")
	// ErrUpstreamChanged is returned by a fetch when, after the move into
	// the mirror, a ref outside the lead's namespace holds an object that
	// this fetch staged: a tip that the staging repository brought and the
	// mirror did not hold before the fetch. That can only be a bug in the
	// move or an attack, because the move names no ref outside the lead's
	// namespace. The lead's refs are put back as they were before the move
	// and the error text names the refs, at most ten. A ref outside the
	// namespace that changed to anything else is not an error: see
	// [FetchReport.OutsideChanged].
	ErrUpstreamChanged = errors.New("mirror: a ref outside the lead's namespace holds what this fetch staged")
	// ErrRefused is returned by a fetch when what arrived in the staging
	// repository is refused: a lead ref that fails the checks of this
	// package or is not a commit, or a staging repository that is not what
	// this package made. The mirror is unchanged. The error text holds
	// counts and fixed words, and no ref name.
	ErrRefused = errors.New("mirror: the fetched content is refused")
	// ErrFetchTimeout is returned by a fetch that ran past its time limit.
	ErrFetchTimeout = errors.New("mirror: the fetch ran past its time limit")
)

// LimitError is returned when a limit of [FetchLimits] is passed. What is
// "source", "staged", "mirror", "refs" or "entries". Got is what was
// measured, or the count at which the walk or the listing stopped, and Max is
// the limit.
type LimitError struct {
	What     string
	Got, Max int64
}

// Error describes the limit.
func (e *LimitError) Error() string {
	return fmt.Sprintf("mirror: the limit for %s is passed: %d against %d", e.What, e.Got, e.Max)
}

// FetchRequest asks for a fetch of one lead's branches into the mirror.
type FetchRequest struct {
	// StateRoot is the state root. The lead's clone is
	// [LeadTree](StateRoot, Role). The caller vouches that the owner owns
	// StateRoot and that no sandbox can write any folder above the clone.
	StateRoot string
	// Role is the lead's role and Project the project; both pass
	// [ValidName]. Project must be the mirror's own project.
	Role, Project string
	// Planner is how the serving half of the fetch is sandboxed. A fetch
	// without a sandbox does not exist: a planner that has none returns
	// [sandboxplan.ErrNotConfigured] and the fetch stops.
	Planner sandboxplan.Planner
	// Limits bounds the fetch. A zero field means the default of
	// [DefaultFetchLimits].
	Limits FetchLimits
}

// RefUpdate is one change of a lead ref. Old is empty when the ref was
// created, and New is empty when it was pruned. Ref is a full name from the
// lead: escape it before it is shown.
type RefUpdate struct{ Ref, Old, New string }

// FetchReport says what a fetch did.
type FetchReport struct {
	// Source is the size of the lead's object store as measured in the
	// sandbox, in bytes. It is the lead's statement and a first check.
	Source int64
	// MirrorBefore, Staged and MirrorAfter are allocated bytes: the mirror
	// before the fetch, the staging repository after the fetch (it holds
	// a few small files besides what arrived), and the mirror after the
	// move.
	MirrorBefore, Staged, MirrorAfter int64
	// Updates lists the changes of the lead's refs, sorted by name.
	Updates []RefUpdate
	// Duration is the time the fetch took.
	Duration time.Duration
	// OutsideChanged names the refs outside the lead's namespace that
	// differ between the snapshot before the move and the one after it, at
	// most ten, each quoted with strconv.QuoteToASCII. It is a note and not
	// an error. Another owner process, such as an intake or a push, may
	// write upstream refs or the refs of another lead while a fetch runs,
	// and the move names only this lead's namespace, so the fetch cannot
	// have written them. The note is for a caller that wants to know.
	OutsideChanged []string
}

const (
	// measureTimeout is the time limit of the measure command.
	measureTimeout = 30 * time.Second
	// measureOutput is the output cap of the measure command.
	measureOutput = 4 << 10
	// fetchOutput is the cap of the standard output of the fetch itself.
	fetchOutput = 64 << 10
	// maxReportedRefs is how many ref names the text of
	// [ErrUpstreamChanged] and [FetchReport.OutsideChanged] hold.
	maxReportedRefs = 10
	// moveRuns is how many git runs of the mirror's limit the move of step 8
	// may take in all: the fetch of the objects, the two transactions, the
	// listing after them, and one to spare.
	moveRuns = 5
)

// watchInterval is how often the watchdog of a fetch measures the staging
// repository. Zero means the default of boundedexec. Tests shorten it.
var watchInterval time.Duration

// maxWatchFailures is how many measures of the staging folder in a row may
// fail before the watchdog stops the fetch.
const maxWatchFailures = 3

// watchMeasureHook replaces the measure of the staging folder in the watchdog
// when it is not nil. It is nil outside the tests, which use it to make the
// measure fail.
var watchMeasureHook func() (safefs.Size, error)

// fetchHook is called by a fetch right before each of the steps 3 to 10, with
// the number of the step, once more, with 60, right after the check of the
// repository path of step 6 and before the receiving half is prepared and
// started, and once more, with 90, right after the listing that step 9
// makes. It is nil outside the tests, which use it to stop a fetch at a
// given step, for example with a panic.
var fetchHook func(step int)

func hook(step int) {
	if fetchHook != nil {
		fetchHook(step)
	}
}

// lockPath returns the lock file of the mirror: beside it, not inside.
func (m *Mirror) lockPath() string { return m.path + ".lock" }

// Fetch fetches the branches of one lead from the lead's clone into the
// mirror, under refs/leads/<role>/heads/. It never touches refs/heads,
// refs/tags or the namespace of another lead, and it fetches no tag. A
// failure at any step leaves the refs and the objects of the mirror as they
// were, with the exceptions that the last step names. A crash of the owner
// process is not a failure in that sense: the package comment says what it
// leaves and what the next fetch does.
//
// The steps are, in this order:
//  1. The request is checked and an exclusive, non-blocking lock beside the
//     mirror is taken, so that two fetches into one mirror cannot overlap
//     ([ErrBusy]). A staging folder that a crashed fetch left is removed.
//  2. The owner checks the lead's clone without following anything:
//     [ErrHostileTree]. See the comment of checkLeadTree on what this check
//     is worth.
//  3. The planner gives the sandboxed upload-pack command. A planner error,
//     [sandboxplan.ErrNotConfigured] included, stops the fetch. No command
//     runs before this. The plan's repository path must not exist on the
//     host: [ErrRepositoryPath]. This is a second layer: the fetch names its
//     source as a "file://" URL, which git never reads as a bundle, so the
//     sandboxed command starts whatever is at the path. The clone's own path
//     passes only while it is a real folder: [ErrHostileTree] otherwise.
//  4. The sandbox measures the clone's object store; more than MaxSource is a
//     [*LimitError]. The mirror is measured; a mirror at or over MaxMirror is
//     a [*LimitError].
//  5. A staging repository is made beside the mirror, with the mirror's
//     objects as its alternate, and the mirror's refs are noted.
//  6. The repository path is checked again, and then the receiving half of
//     git runs as the owner, with the source as a "file://" URL, with the sandboxed command
//     as upload-pack, into the staging repository. The fetch has a time limit
//     ([ErrFetchTimeout]) and a watchdog that stops it when the staging
//     repository passes MaxStaged or MaxEntries, or when three measures of
//     the staging folder in a row fail.
//  7. What arrived is checked in the staging repository: the sizes, the count
//     of refs, and every ref, which must be under the lead's namespace, have
//     a name that this package accepts and point at a commit. A name that
//     fails refuses the whole fetch with [ErrRefused] and the count of such
//     refs, never their names. The staging repository must still be what this
//     code made.
//  8. The branches move from the staging repository into the mirror, in two
//     parts. First the objects that the mirror lacks are fetched from the
//     staging repository by object id, with no ref destination, so the fetch
//     writes no ref. Then the lead's namespace, and only it, is changed to
//     what the staging repository holds, in two compare-and-swap
//     transactions of git update-ref --stdin -z: first every delete, then
//     every create and update. The two steps are needed because the files
//     backend refuses a delete of a/b and a create of a in one transaction,
//     which is what a lead's rename of a branch across a "/" is. A failure of
//     the second transaction puts back what the first deleted. A move that
//     fails leaves no ref changed; a crash between the two transactions does
//     not (see the package comment).
//  9. The refs of the mirror are listed again. A ref outside the lead's
//     namespace that holds an object that this fetch staged is a bug or an
//     attack ([ErrUpstreamChanged]), and so is an alternates file in the
//     mirror. In both cases the lead's namespace is put back as it was before
//     the move, in the same two transactions in the other direction (what the
//     move created is deleted first), and then the error is returned; if
//     putting it back fails too, the error says both. The same holds when the
//     listing itself fails: the lead's namespace is then put back against the
//     values that the move wrote, the refs of the staging repository.
//     Any other ref outside the namespace that differs is listed in
//     [FetchReport.OutsideChanged] and left alone.
//  10. The staging repository is deleted, on every path and on a panic, and
//     the mirror is measured again.
//
// From step 8 on, the fetch is not cancelled by ctx or by the time limit: a
// git run killed in the middle of the move would leave refs half moved. Each
// git run there has the mirror's own time limit, and a restore or an undo has
// a time limit of its own that the move cannot have spent.
//
// The objects that a failed fetch brought into the mirror in step 8 stay. They
// are not reachable from any ref and they are not a danger; the owner's
// garbage collection removes them, and this package runs none.
//
// The check of the size after the move is a check, not a quota. A mirror over
// MaxMirror after the move is returned as a [*LimitError] together with the
// report, and the refs have moved.
func (m *Mirror) Fetch(ctx context.Context, req FetchRequest) (FetchReport, error) {
	start := time.Now()
	var rep FetchReport
	err := m.fetch(ctx, req, &rep)
	rep.Duration = time.Since(start)
	return rep, err
}

func (m *Mirror) fetch(ctx context.Context, req FetchRequest, rep *FetchReport) (err error) {
	// Step 1.
	if m.project == "" {
		return errors.New("mirror: the mirror was not made by Create or Open")
	}
	if !ValidName(req.Role) {
		return badName("role")
	}
	if !ValidName(req.Project) || req.Project != m.project {
		return badName("project")
	}
	if req.Planner == nil {
		return errors.New("mirror: the fetch has no planner")
	}
	lim := req.Limits.withDefaults()
	if err := lim.check(); err != nil {
		return err
	}
	tree, err := LeadTree(req.StateRoot, req.Role)
	if err != nil {
		return err
	}
	prefix, err := LeadPrefix(req.Role)
	if err != nil {
		return err
	}
	refspec, err := LeadRefspec(req.Role)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()
	timedOut := func(err error) error {
		if parent.Err() == nil && ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrFetchTimeout, context.DeadlineExceeded)
		}
		return err
	}

	release, err := lockFile(m.lockPath())
	if err != nil {
		return err
	}
	defer release()
	m.removeStale()

	// Step 2.
	if err := checkLeadTree(req.StateRoot, req.Role); err != nil {
		return err
	}

	// Step 3.
	hook(3)
	plan, err := req.Planner.Serve(sandboxplan.ServeRequest{Role: req.Role, Project: req.Project, Tree: tree})
	if err != nil {
		return fmt.Errorf("mirror: no sandbox plan for the fetch: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("mirror: the sandbox plan is not valid: %w", err)
	}
	if err := checkRepositoryPath(plan.Repository, tree); err != nil {
		return err
	}

	// Step 4.
	hook(4)
	n, err := measureSource(ctx, plan.Measure)
	if err != nil {
		return timedOut(err)
	}
	rep.Source = n
	if n > lim.MaxSource {
		return &LimitError{What: "source", Got: n, Max: lim.MaxSource}
	}
	before, err := m.measure(ctx, lim)
	rep.MirrorBefore = before
	if err != nil {
		return timedOut(err)
	}
	if before >= lim.MaxMirror {
		return &LimitError{What: "mirror", Got: before, Max: lim.MaxMirror}
	}

	// Step 5.
	hook(5)
	st, err := m.newStaging(ctx)
	if err != nil {
		return timedOut(err)
	}
	defer st.remove()
	snapBefore, err := m.snapshotMirror(ctx)
	if err != nil {
		return timedOut(err)
	}

	// Step 6.
	hook(6)
	if err := checkRepositoryPath(plan.Repository, tree); err != nil {
		return err
	}
	hook(60)
	if err := m.fetchStaging(ctx, st, plan, refspec, negotiationTips(snapBefore, prefix), lim); err != nil {
		return timedOut(err)
	}

	// Step 7.
	hook(7)
	staged, err := st.measure(lim)
	rep.Staged = staged
	if err != nil {
		return err
	}
	if staged > lim.MaxStaged {
		return &LimitError{What: "staged", Got: staged, Max: lim.MaxStaged}
	}
	if before+staged > lim.MaxMirror {
		return &LimitError{What: "mirror", Got: before + staged, Max: lim.MaxMirror}
	}
	stagedRecs, err := m.checkArrived(ctx, st, prefix, lim)
	if err != nil {
		return timedOut(err)
	}
	newTips, err := m.newTips(ctx, stagedRecs)
	if err != nil {
		return timedOut(err)
	}
	if err := ctx.Err(); err != nil {
		return timedOut(err)
	}

	// Step 8. The move is not cancelled: see the comment of Fetch.
	hook(8)
	move, cancelMove := context.WithTimeout(context.WithoutCancel(ctx), moveRuns*m.limits.timeout())
	defer cancelMove()
	if err := m.fetchObjects(move, st, newTips); err != nil {
		return err
	}
	if err := m.changeNamespace(move, prefix, snapBefore, stagedRecs); err != nil {
		return err
	}

	// Step 9. A failure from here on puts the lead's namespace back.
	hook(9)
	snapAfter, err := m.snapshotMirror(move)
	if err != nil {
		// Without the listing, the move is still known: the lead's namespace
		// is what the staging repository held. Put it back against that.
		return m.rollback(move, prefix, snapBefore, namespaceAfter(snapBefore, stagedRecs, prefix), err)
	}
	hook(90)
	updates, outside := compareSnapshots(snapBefore, snapAfter, prefix)
	if bad := stagedRefs(outside, newTips); len(bad) > 0 {
		return m.rollback(move, prefix, snapBefore, snapAfter, upstreamChanged(bad))
	}
	if err := m.checkNoAlternates(); err != nil {
		return m.rollback(move, prefix, snapBefore, snapAfter, err)
	}
	rep.Updates = updates
	rep.OutsideChanged = quoteNames(outside)

	// Step 10.
	hook(10)
	st.remove()
	after, err := m.measure(move, lim)
	rep.MirrorAfter = after
	if err != nil {
		return err
	}
	if after > lim.MaxMirror {
		return &LimitError{What: "mirror", Got: after, Max: lim.MaxMirror}
	}
	return nil
}

// measureSource runs the measure command of the plan and returns the number
// it prints. The command runs sandboxed; its output is the lead side's
// statement.
func measureSource(ctx context.Context, c sandboxplan.Command) (int64, error) {
	d := measureTimeout
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		if left <= 0 {
			return 0, context.DeadlineExceeded
		}
		d = min(d, left)
	}
	res, err := boundedexec.Run(ctx, boundedexec.Cmd{
		Argv:      c.Argv,
		Env:       c.Env,
		Timeout:   d,
		MaxStdout: measureOutput,
		MaxStderr: measureOutput,
	})
	if err != nil {
		return 0, fmt.Errorf("mirror: the measure command: %w", err)
	}
	n, err := sandboxplan.ParseMeasure(res.Stdout)
	if err != nil {
		return 0, fmt.Errorf("mirror: the measure command: %w", err)
	}
	return n, nil
}

// measure counts the whole mirror folder and returns its allocated bytes. The
// entry limit is a [*LimitError]. It does not compare the bytes with
// MaxMirror.
func (m *Mirror) measure(ctx context.Context, lim FetchLimits) (int64, error) {
	size, err := m.Size(ctx, safefs.SizeLimits{MaxBytes: math.MaxInt64, MaxEntries: lim.MaxEntries})
	if errors.Is(err, safefs.ErrLimit) && size.Entries > lim.MaxEntries {
		return size.Bytes, &LimitError{What: "entries", Got: size.Entries, Max: lim.MaxEntries}
	}
	return size.Bytes, err
}

// measure counts the whole staging folder, as [Mirror.measure] does for the
// mirror.
func (s *staging) measure(lim FetchLimits) (int64, error) {
	root, err := safefs.OpenRoot(s.path)
	if err != nil {
		return 0, fmt.Errorf("mirror: the staging repository: %w", err)
	}
	defer root.Close()
	size, err := root.TreeSize(".", safefs.SizeLimits{MaxBytes: math.MaxInt64, MaxEntries: lim.MaxEntries})
	if errors.Is(err, safefs.ErrLimit) && size.Entries > lim.MaxEntries {
		return size.Bytes, &LimitError{What: "entries", Got: size.Entries, Max: lim.MaxEntries}
	}
	return size.Bytes, err
}

// fetchStaging runs the receiving half of the fetch into the staging
// repository, with the sandboxed upload-pack command named by git's option.
func (m *Mirror) fetchStaging(ctx context.Context, st *staging, plan sandboxplan.ServePlan, refspec string, tips []string, lim FetchLimits) error {
	root, err := safefs.OpenRoot(st.path)
	if err != nil {
		return fmt.Errorf("mirror: the staging repository: %w", err)
	}
	defer root.Close()
	failures := 0
	watch := func() error {
		var size safefs.Size
		var err error
		if watchMeasureHook != nil {
			size, err = watchMeasureHook()
		} else {
			size, err = root.TreeSize(".", safefs.SizeLimits{MaxBytes: lim.MaxStaged, MaxEntries: lim.MaxEntries})
		}
		if errors.Is(err, safefs.ErrLimit) {
			if size.Entries > lim.MaxEntries {
				return &LimitError{What: "entries", Got: size.Entries, Max: lim.MaxEntries}
			}
			return &LimitError{What: "staged", Got: size.Bytes, Max: lim.MaxStaged}
		}
		if err != nil {
			// git renames and removes files while the walk runs, which makes
			// the walk fail now and then. A few failures in a row are
			// fine and the measure after the fetch decides. A walk that
			// keeps failing would turn the limit off, so it stops the fetch.
			if failures++; failures >= maxWatchFailures {
				// The path of a failed walk is a ref file of the lead, whose
				// name is the lead's text: keep the cause and drop the path.
				var pe *fs.PathError
				if errors.As(err, &pe) {
					err = pe.Err
				}
				return fmt.Errorf("mirror: the staging folder could not be measured: %w", err)
			}
			return nil
		}
		failures = 0
		return nil
	}
	left := lim.Timeout
	if dl, ok := ctx.Deadline(); ok {
		if left = time.Until(dl); left <= 0 {
			return context.DeadlineExceeded
		}
	}
	_, err = runGitWatch(ctx, m.git, gitenv.Cmd{
		GitDir:     st.path,
		Protocols:  []string{"file"},
		UploadPack: plan.UploadPack,
		// A "file://" URL, so that git never reads a bundle at the path.
		SourceFileURL: true,
		Args: slices.Concat(
			[]string{"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance", "--prune"},
			tips,
			[]string{"--end-of-options", plan.Repository, refspec},
		),
	}, nil, left, fetchOutput, watch, watchInterval)
	var le *LimitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &le):
		return le
	case errors.Is(err, boundedexec.ErrTimeout):
		return fmt.Errorf("%w: %w", ErrFetchTimeout, err)
	}
	return err
}

// maxTips is the most commits that a fetch names as the starting point of
// its negotiation.
const maxTips = 1000

// negotiationTips returns the options "--negotiation-tip=<id>" for a fetch
// into a new staging repository. A staging repository has no refs, so without
// them git would tell the lead's side nothing about what the mirror has, and
// every fetch would copy the whole history again. The tips are the commits of
// the lead's own refs in the mirror, then the commits of the upstream
// branches, without repeats and at most [maxTips]. The objects are in the
// mirror, which the staging repository has as its alternate. Both sets are
// the mirror's own data, written by an earlier fetch or by the owner.
func negotiationTips(snap []record, prefix string) []string {
	var tips []string
	seen := map[string]bool{}
	add := func(pfx string) {
		for _, r := range snap {
			if len(tips) >= maxTips {
				return
			}
			if r.Type == "commit" && r.Symref == "" && strings.HasPrefix(r.Name, pfx) && !seen[r.ID] {
				seen[r.ID] = true
				tips = append(tips, "--negotiation-tip="+r.ID)
			}
		}
	}
	add(prefix)
	add(headsPrefix)
	return tips
}

// checkArrived is the check of step 7 on the refs and on the staging
// repository itself. It returns the refs of the staging repository, sorted by
// name: what the move writes into the mirror.
func (m *Mirror) checkArrived(ctx context.Context, st *staging, prefix string, lim FetchLimits) ([]record, error) {
	reason, err := st.check(ctx)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return nil, fmt.Errorf("%w: %s", ErrRefused, reason)
	}
	recs, err := m.snapshot(ctx, st.path, lim.MaxRefs, int64(lim.MaxRefs+1)*snapshotRecord)
	if errors.Is(err, ErrTooManyRefs) {
		return nil, &LimitError{What: "refs", Got: int64(lim.MaxRefs) + 1, Max: int64(lim.MaxRefs)}
	}
	if err != nil {
		return nil, err
	}
	bad := 0
	for _, r := range recs {
		branch, ok := strings.CutPrefix(r.Name, prefix)
		if !ok || !ValidBranch(branch) || r.Symref != "" || r.Type != "commit" {
			bad++
		}
	}
	if bad > 0 {
		return nil, fmt.Errorf("%w: %d of %d refs fail the checks of names and types", ErrRefused, bad, len(recs))
	}
	return recs, nil
}

// outsideChange is a ref outside the lead's namespace that differs between two
// snapshots. New is its value in the second snapshot, and empty when the ref is
// gone.
type outsideChange struct{ Name, New string }

// compareSnapshots returns the updates of the refs under prefix, sorted by
// name, and the refs outside prefix that differ, in name order. Both snapshots
// are sorted by name. It decides nothing about the refs outside prefix: see
// [stagedRefs].
func compareSnapshots(before, after []record, prefix string) ([]RefUpdate, []outsideChange) {
	var outside []outsideChange
	var updates []RefUpdate
	i, j := 0, 0
	for i < len(before) || j < len(after) {
		var b, a *record
		switch {
		case j == len(after) || (i < len(before) && before[i].Name < after[j].Name):
			b = &before[i]
			i++
		case i == len(before) || after[j].Name < before[i].Name:
			a = &after[j]
			j++
		default:
			b, a = &before[i], &after[j]
			i++
			j++
		}
		name := ""
		if b != nil {
			name = b.Name
		} else {
			name = a.Name
		}
		if !strings.HasPrefix(name, prefix) {
			if b == nil || a == nil || *a != *b {
				c := outsideChange{Name: name}
				if a != nil {
					c.New = a.ID
				}
				outside = append(outside, c)
			}
			continue
		}
		if b != nil && a != nil && *a == *b {
			continue
		}
		u := RefUpdate{Ref: name}
		if b != nil {
			u.Old = b.ID
		}
		if a != nil {
			u.New = a.ID
		}
		updates = append(updates, u)
	}
	slices.SortFunc(updates, func(x, y RefUpdate) int { return strings.Compare(x.Ref, y.Ref) })
	return updates, outside
}

// newTips returns the ids of the refs of the staging repository, recs, that
// the mirror does not hold: the tips that this fetch staged. The mirror is
// asked before the move, when it still has only what it had before the fetch. A
// staging repository holds, in its own object store, exactly what the lead
// sent that the mirror lacked, and the staging repository's alternate is the
// mirror; so a tip that the mirror lacks is in the staging repository's own
// store.
//
// This is the proof that a fetch can give: the move writes the values of the
// staging repository's refs under the lead's namespace and no others, and
// those values are these tips. A ref outside the namespace that holds one of
// them was written by the move or by a bug; a ref that holds any other value
// was not written by this fetch.
func (m *Mirror) newTips(ctx context.Context, recs []record) (map[string]bool, error) {
	var ids []string
	seen := map[string]bool{}
	for _, r := range recs {
		if !seen[r.ID] {
			seen[r.ID] = true
			ids = append(ids, r.ID)
		}
	}
	tips := map[string]bool{}
	if len(ids) == 0 {
		return tips, nil
	}
	out, err := m.run(ctx, gitenv.Cmd{
		GitDir: m.path,
		Args:   []string{"cat-file", "--batch-check"},
	}, strings.NewReader(strings.Join(ids, "\n")+"\n"), int64(len(ids)+1)*128)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if id, rest, ok := strings.Cut(line, " "); ok && rest == "missing" && seen[id] {
			tips[id] = true
		}
	}
	return tips, nil
}

// stagedRefs returns the names of the changed refs outside the lead's
// namespace whose new value is a tip that the fetch staged.
func stagedRefs(outside []outsideChange, newTips map[string]bool) []string {
	var bad []string
	for _, c := range outside {
		if c.New != "" && newTips[c.New] {
			bad = append(bad, c.Name)
		}
	}
	return bad
}

// quoteNames returns the names of the changes, at most [maxReportedRefs],
// quoted with strconv.QuoteToASCII.
func quoteNames(outside []outsideChange) []string {
	var out []string
	for _, c := range outside[:min(len(outside), maxReportedRefs)] {
		out = append(out, strconv.QuoteToASCII(c.Name))
	}
	return out
}

// upstreamChanged returns [ErrUpstreamChanged] with the names of the refs, at
// most ten, quoted.
func upstreamChanged(names []string) error {
	shown := make([]string, 0, maxReportedRefs)
	for _, n := range names[:min(len(names), maxReportedRefs)] {
		shown = append(shown, strconv.QuoteToASCII(n))
	}
	more := ""
	if len(names) > maxReportedRefs {
		more = ", and more"
	}
	return fmt.Errorf("%w: %d ref(s): %s%s", ErrUpstreamChanged, len(names), strings.Join(shown, ", "), more)
}

// namespaceAfter returns the snapshot that the mirror has after a move that
// changed the namespace under prefix from its value in before to staged, the
// refs of the staging repository, and nothing else: the refs of before outside
// prefix, and staged, sorted by name. It is what the mirror holds if the move
// did what it was asked, and it is known without a second listing.
func namespaceAfter(before, staged []record, prefix string) []record {
	after := make([]record, 0, len(before)+len(staged))
	for _, r := range before {
		if !strings.HasPrefix(r.Name, prefix) {
			after = append(after, r)
		}
	}
	after = append(after, staged...)
	slices.SortFunc(after, func(a, b record) int { return strings.Compare(a.Name, b.Name) })
	return after
}

// rollback puts the lead's namespace back as it was in before, after a check
// of step 9 failed with cause, and returns cause. If that fails too, the
// returned error holds both. The restore does not share the time of the move,
// which may be spent: ctx gives it its values and not its deadline or its
// cancellation, and each git run of the restore has the mirror's own limit.
func (m *Mirror) rollback(ctx context.Context, prefix string, before, after []record, cause error) error {
	ctx = context.WithoutCancel(ctx)
	if err := m.restoreNamespace(ctx, prefix, before, after); err != nil {
		return errors.Join(cause, fmt.Errorf("mirror: the lead's refs could not be put back as they were: %w", err))
	}
	return cause
}

// restoreNamespace puts the lead's namespace back to its value in before, after
// the move changed it to its value in after: see [Mirror.changeNamespace]. A
// ref that someone else moved in the meantime makes it fail.
func (m *Mirror) restoreNamespace(ctx context.Context, prefix string, before, after []record) error {
	return m.changeNamespace(ctx, prefix, after, before)
}

// namespaceHook is called by changeNamespace with 1 right before its first
// transaction (the deletes) and with 2 right before its second (the creates and
// updates, after the first is done). It is nil outside the tests, which use it
// to act between the two.
var namespaceHook func(phase int)

// changeNamespace changes the refs under prefix from their values in from to
// their values in to, which are two snapshots sorted by name. Refs outside
// prefix are not looked at. It runs in two compare-and-swap transactions of
// git update-ref --no-deref --stdin -z: the first deletes every ref that to
// lacks, the second creates and updates the others. Every command names the
// value that the ref has in from (nothing, for a create), so a ref that
// someone else moved in the meantime makes that transaction fail and change
// nothing.
//
// The deletes come first because the files backend refuses to delete a/b and
// create a in one transaction, and a lead's rename of a branch across a "/" is
// that. If the second transaction fails, the refs that the first deleted are
// created again, and the error from the second is returned; if that fails too,
// the returned error holds both. In between, the namespace holds a subset of
// its refs, under the lock of the fetch, which a reader of the namespace does
// not hold, and a crash here leaves it so. A failure of the first transaction
// leaves nothing changed. A symbolic ref under prefix, in either snapshot, is
// refused before anything changes: the fetch makes none.
//
// A restore or an undo runs on a context with the values of ctx and none of
// its deadline, so each git run has the mirror's own limit.
func (m *Mirror) changeNamespace(ctx context.Context, prefix string, from, to []record) error {
	updates, _ := compareSnapshots(from, to, prefix)
	if len(updates) == 0 {
		return nil
	}
	symbolic := func(snap []record, name string) bool {
		i, ok := slices.BinarySearchFunc(snap, name, func(r record, n string) int { return strings.Compare(r.Name, n) })
		return ok && snap[i].Symref != ""
	}
	var deletes, writes, undo strings.Builder
	for _, u := range updates {
		if symbolic(from, u.Ref) || symbolic(to, u.Ref) {
			return errors.New("a symbolic ref is in the lead's namespace")
		}
		switch {
		case u.New == "":
			deletes.WriteString("delete " + u.Ref + "\x00" + u.Old + "\x00")
			undo.WriteString("create " + u.Ref + "\x00" + u.Old + "\x00")
		case u.Old == "":
			writes.WriteString("create " + u.Ref + "\x00" + u.New + "\x00")
		default:
			writes.WriteString("update " + u.Ref + "\x00" + u.New + "\x00" + u.Old + "\x00")
		}
	}
	if namespaceHook != nil {
		namespaceHook(1)
	}
	if deletes.Len() > 0 {
		if err := m.updateRefs(ctx, deletes.String()); err != nil {
			return err
		}
	}
	if namespaceHook != nil {
		namespaceHook(2)
	}
	if writes.Len() > 0 {
		err := m.updateRefs(ctx, writes.String())
		if err == nil {
			return nil
		}
		if undo.Len() == 0 {
			return err
		}
		if uerr := m.updateRefs(context.WithoutCancel(ctx), undo.String()); uerr != nil {
			return errors.Join(err, fmt.Errorf("mirror: the refs that were deleted could not be created again: %w", uerr))
		}
		return err
	}
	return nil
}

// updateRefs runs one transaction of git update-ref --no-deref --stdin -z. The
// text is the commands, each ending in NUL: all of them apply or none.
func (m *Mirror) updateRefs(ctx context.Context, tx string) error {
	_, err := m.run(ctx, gitenv.Cmd{
		GitDir: m.path,
		Args:   []string{"update-ref", "--no-deref", "--stdin", "-z"},
	}, strings.NewReader(tx), smallOutput)
	return err
}

// fetchObjects brings the objects of the tips from the staging repository into
// the mirror, with no ref destination: git fetch of the object ids writes no
// ref and no FETCH_HEAD. The refs are then changed by [Mirror.changeNamespace].
// A tip that the mirror holds is not named, since the staging repository has
// the mirror's objects as its alternate; with none to fetch, nothing runs.
//
// The other way, a refspec into a temporary namespace that this code deletes
// again, was not chosen: it writes refs that a crash would leave behind, and
// it needs a second change of refs. The way chosen has a window of its own,
// between its two transactions, which the package comment describes. A want of an object id needs no ref and
// works here because gitenv selects protocol version 2, where upload-pack
// serves any object that it has; the staging repository has every tip.
func (m *Mirror) fetchObjects(ctx context.Context, st *staging, tips map[string]bool) error {
	if len(tips) == 0 {
		return nil
	}
	ids := make([]string, 0, len(tips))
	for id := range tips {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	_, err := m.run(ctx, gitenv.Cmd{
		GitDir:    m.path,
		Protocols: []string{"file"},
		Args: slices.Concat(
			[]string{"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance", "--end-of-options", st.path},
			ids,
		),
	}, nil, fetchOutput)
	return err
}

// checkNoAlternates checks that the mirror has no alternates file after the
// move: its objects must stand on their own.
func (m *Mirror) checkNoAlternates() error {
	root, err := safefs.OpenRoot(m.path)
	if err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	defer root.Close()
	for _, rel := range []string{"objects/info/alternates", "objects/info/http-alternates"} {
		_, err := root.Lstat(rel)
		if err == nil {
			return errors.New("mirror: the mirror has an alternates file after the fetch")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return errors.New("mirror: the mirror's alternates file cannot be checked after the fetch")
		}
	}
	return nil
}
