package thread

import (
	"errors"
	"io/fs"
	"sort"
	"time"

	"ataniq/internal/genesis/catalog"
	"ataniq/internal/genesis/plan"
)

// TagLen is the length of a thread's tag, the first digits of its id that the
// desk shows; a tag given to [ReadPlan] has at least as many.
const TagLen = 6

// planShortLen is how many digits of a plan's digest a [Summary] shows.
const planShortLen = 12

// Summary is one finished thread that holds a plan, as the desk lists it. It
// holds nothing of the brief's answers and nothing of the plan's notes.
type Summary struct {
	// ID is the thread's id, 16 lower-case hexadecimal digits.
	ID string
	// Tag is the first [TagLen] digits of the id.
	Tag string
	// Kind is the plan's template id (software, publishing or media): the
	// kind of project, a word of the record's grammar.
	Kind string
	// Revision is the stored plan's revision.
	Revision int
	// PlanDigest is the stored plan's digest, and PlanShort its first 12
	// digits.
	PlanDigest string
	PlanShort  string
	// Finished is when the thread was finished, in UTC to the second.
	Finished time.Time
}

// TagError is the error of [ReadPlan] for a tag that names no finished thread
// with a plan, names more than one, or is not a tag. Its text is fixed and
// holds nothing of the tag.
type TagError struct {
	// Reason is TagBad, TagNone or TagAmbiguous.
	Reason string
	text   string
}

func (e *TagError) Error() string { return e.text }

// The reasons of a [TagError].
const (
	TagBad       = "bad-tag"
	TagNone      = "no-thread"
	TagAmbiguous = "ambiguous"
)

var (
	errTagBad       = &TagError{Reason: TagBad, text: "genesis: a tag is 6 to 16 lower-case hexadecimal digits"}
	errTagNone      = &TagError{Reason: TagNone, text: "genesis: no finished interview with a plan has that tag"}
	errTagAmbiguous = &TagError{Reason: TagAmbiguous, text: "genesis: more than one finished interview with a plan has that tag; give more of its digits"}
)

// finished is a finished thread with its plan, as the reader found it.
type finished struct {
	sum  Summary
	plan plan.Plan
}

// ReadFinished reads the folder of the threads without writing anything and
// returns the finished threads that hold a stored plan, oldest first, and the
// fixed-text warnings of the files it did not serve.
//
// It reads every file as [Open] does, by the same code and the same strict
// rules, under catalog v1, and a file that does not pass is skipped with the
// line Open would log (its name and the check it failed, never a byte of the
// file), never served. Unlike Open it makes no folder (a folder that is not
// there holds no thread: nil, nil, nil), removes no temporary file, expires
// and prunes nothing, takes no lock and writes nothing: it may run beside a
// daemon that serves the store. A folder it refuses is an [*OpenError], as
// Open's is. Open and discarded threads, and finished ones without a plan,
// are not listed.
func ReadFinished(dir string) ([]Summary, []string, error) {
	found, warnings, err := readFinished(dir)
	if err != nil {
		return nil, nil, err
	}
	out := make([]Summary, 0, len(found))
	for _, f := range found {
		out = append(out, f.sum)
	}
	return out, warnings, nil
}

// ReadPlan reads the folder as [ReadFinished] does and returns the stored
// plan of the one finished thread whose id starts with tagOrID (6 to 16
// lower-case hexadecimal digits), and its id. A tag that is not one, that no
// finished thread with a plan has, or that more than one has is a
// [*TagError]. The plan is the one the file holds, checked as Open checks
// it: never compiled again.
func ReadPlan(dir, tagOrID string) (plan.Plan, string, error) {
	if len(tagOrID) < TagLen || len(tagOrID) > 16 || !isHex(tagOrID, len(tagOrID)) {
		return plan.Plan{}, "", errTagBad
	}
	found, _, err := readFinished(dir)
	if err != nil {
		return plan.Plan{}, "", err
	}
	var match []finished
	for _, f := range found {
		if f.sum.ID[:len(tagOrID)] == tagOrID {
			match = append(match, f)
		}
	}
	switch len(match) {
	case 0:
		return plan.Plan{}, "", errTagNone
	case 1:
		return match[0].plan.Clone(), match[0].sum.ID, nil
	}
	return plan.Plan{}, "", errTagAmbiguous
}

func readFinished(dir string) ([]finished, []string, error) {
	cat, err := catalog.V1()
	if err != nil {
		return nil, nil, openErr("catalog", "genesis: the question catalog does not load")
	}
	if dir == "" {
		return nil, nil, openErr("no-folder", "genesis: no folder for the threads")
	}
	root, err := openExisting(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	entries, err := readEntries(root)
	if err != nil {
		return nil, nil, err
	}
	var out []finished
	var warnings []string
	for _, e := range entries {
		name := e.Name()
		if !fileName.MatchString(name) {
			continue
		}
		r, err := readRecord(root, name, cat.Digest())
		if err != nil {
			warnings = append(warnings, notServed(name, err))
			continue
		}
		if r.state != StateFinished || r.plan == nil {
			continue
		}
		out = append(out, finished{
			sum: Summary{
				ID: r.id, Tag: r.id[:TagLen], Kind: r.plan.Template.ID, Revision: r.plan.Revision,
				PlanDigest: r.planDigest, PlanShort: r.planDigest[:planShortLen], Finished: r.updated,
			},
			plan: *r.plan,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].sum, out[j].sum
		if !a.Finished.Equal(b.Finished) {
			return a.Finished.Before(b.Finished)
		}
		return a.ID < b.ID
	})
	return out, warnings, nil
}
