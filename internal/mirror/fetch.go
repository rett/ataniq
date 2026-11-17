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
	// the mirror, a ref outside the lead's namespace is not what it was. It
	// means a bug or an attack. The error text names the refs.
	ErrUpstreamChanged = errors.New("mirror: a ref outside the lead's namespace changed")
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
}

const (
	// measureTimeout is the time limit of the measure command.
	measureTimeout = 30 * time.Second
	// measureOutput is the output cap of the measure command.
	measureOutput = 4 << 10
	// fetchOutput is the cap of the standard output of the fetch itself.
	fetchOutput = 64 << 10
	// maxReportedRefs is how many ref names the text of
	// [ErrUpstreamChanged] holds.
	maxReportedRefs = 10
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
// the number of the step. It is nil outside the tests, which use it to stop
// a fetch at a given step, for example with a panic.
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
// were, with the exceptions that the last step names.
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
//     runs before this.
//  4. The sandbox measures the clone's object store; more than MaxSource is a
//     [*LimitError]. The mirror is measured; a mirror at or over MaxMirror is
//     a [*LimitError].
//  5. A staging repository is made beside the mirror, with the mirror's
//     objects as its alternate, and the mirror's refs are noted.
//  6. The receiving half of git runs as the owner, with the sandboxed command
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
//  8. The branches move from the staging repository into the mirror, with
//     refs of the lead's namespace only and pruning.
//  9. Every ref outside the lead's namespace must be what it was before
//     ([ErrUpstreamChanged]), and the mirror has no alternates file.
//  10. The staging repository is deleted, on every path and on a panic, and
//     the mirror is measured again.
//
// From step 8 on, the fetch is not cancelled by ctx or by the time limit: a
// git run killed in the middle of the move would leave refs half moved. Each
// git run there has the mirror's own time limit.
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
	if err := m.checkArrived(ctx, st, prefix, lim); err != nil {
		return timedOut(err)
	}
	if err := ctx.Err(); err != nil {
		return timedOut(err)
	}

	// Step 8. The move is not cancelled: see the comment of Fetch.
	hook(8)
	move, cancelMove := context.WithTimeout(context.WithoutCancel(ctx), 3*m.limits.timeout())
	defer cancelMove()
	if _, err := m.run(move, gitenv.Cmd{
		GitDir:    m.path,
		Protocols: []string{"file"},
		Args: []string{
			"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
			"--no-auto-maintenance", "--prune", "--end-of-options",
			st.path, "+" + prefix + "*:" + prefix + "*",
		},
	}, nil, fetchOutput); err != nil {
		return err
	}

	// Step 9.
	hook(9)
	snapAfter, err := m.snapshotMirror(move)
	if err != nil {
		return err
	}
	updates, err := compareSnapshots(snapBefore, snapAfter, prefix)
	if err != nil {
		return err
	}
	rep.Updates = updates
	if err := m.checkNoAlternates(); err != nil {
		return err
	}

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
// repository itself.
func (m *Mirror) checkArrived(ctx context.Context, st *staging, prefix string, lim FetchLimits) error {
	reason, err := st.check(ctx)
	if err != nil {
		return err
	}
	if reason != "" {
		return fmt.Errorf("%w: %s", ErrRefused, reason)
	}
	recs, err := m.snapshot(ctx, st.path, lim.MaxRefs, int64(lim.MaxRefs+1)*snapshotRecord)
	if errors.Is(err, ErrTooManyRefs) {
		return &LimitError{What: "refs", Got: int64(lim.MaxRefs) + 1, Max: int64(lim.MaxRefs)}
	}
	if err != nil {
		return err
	}
	bad := 0
	for _, r := range recs {
		branch, ok := strings.CutPrefix(r.Name, prefix)
		if !ok || !ValidBranch(branch) || r.Symref != "" || r.Type != "commit" {
			bad++
		}
	}
	if bad > 0 {
		return fmt.Errorf("%w: %d of %d refs fail the checks of names and types", ErrRefused, bad, len(recs))
	}
	return nil
}

// compareSnapshots checks that every ref outside prefix is the same in both
// snapshots, and returns the updates of the refs under prefix. Both
// snapshots are sorted by name.
func compareSnapshots(before, after []record, prefix string) ([]RefUpdate, error) {
	var changed []string
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
				changed = append(changed, name)
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
	if len(changed) > 0 {
		shown := make([]string, 0, maxReportedRefs)
		for _, n := range changed[:min(len(changed), maxReportedRefs)] {
			shown = append(shown, strconv.QuoteToASCII(n))
		}
		more := ""
		if len(changed) > maxReportedRefs {
			more = ", and more"
		}
		return nil, fmt.Errorf("%w: %d ref(s) differ: %s%s", ErrUpstreamChanged, len(changed), strings.Join(shown, ", "), more)
	}
	slices.SortFunc(updates, func(x, y RefUpdate) int { return strings.Compare(x.Ref, y.Ref) })
	return updates, nil
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
