package card

import (
	"strconv"
	"strings"
	"time"

	"ataniq/internal/approval/record"
)

// The labels of the three kinds of content a card holds beyond the binding.
// A surface draws the label beside the content on every screen that shows
// it.
const (
	// NoteLabel goes with Card.Note.
	NoteLabel = "lead-stated, not checked"
	// DiffersLabel goes with Card.Differs.
	DiffersLabel = "from an earlier card"
	// AfterLabel goes with Card.After and Card.Verdicts.
	AfterLabel = "after the decision"
	// HeldLabel is the held mark: the words for After.Held.
	HeldLabel = "decided past the limit"
)

// Card is the model of one approval card. Its fields are the card's
// sections in the order a surface draws them, and then the three kinds of
// content that are not under the binding's digest.
//
// A Card is a value made by [New]. The zero Card is no card: nothing may be
// approved on it.
type Card struct {
	// Title is the first line: which request this is, who asked and when.
	Title Title
	// Verb is the second line: what is asked for.
	Verb VerbLine
	// Plan is the plan block: the planner's text, word for word.
	Plan Plan
	// Refs are the ref lines: every ref the approval would move or
	// delete, and nothing else would change.
	Refs []RefLine
	// Merge is the merge commit the host built, or nil for a prune.
	Merge *Merge
	// Diff is the diff summary and its rows, or nil for a prune, which
	// changes no file.
	Diff *Diff
	// Lists are the four lists of the summary in the order of [ListKind],
	// each present even when it is empty. A prune has none.
	Lists []List
	// Flagged are the paths that each need their own sign-off.
	Flagged []FlaggedPath
	// Evidence are the host's records the approval rests on.
	Evidence []Evidence
	// BindingSHA256 is the digest of the binding the card was made from. A
	// decision names it, so that what is approved is what was shown.
	BindingSHA256 string

	// Note is the lead's note, labelled [NoteLabel]. It is outside the
	// binding: the owner reads it and does not approve it.
	Note LeadText
	// Differs is the "differs from" block, labelled [DiffersLabel], or nil
	// when the owner read no earlier card for the same verb and target.
	Differs *Differs
	// After is what came after the card was shown, labelled [AfterLabel].
	After After
	// Verdicts are the verdicts, labelled [AfterLabel] as well. They are
	// nil until the card is decided.
	Verdicts *VerdictBlock
}

// Title is the first line of a card.
type Title struct {
	// ID is the host's id of the pending record. It names the card; it is
	// not under the binding's digest.
	ID string
	// Project is the project the request is for.
	Project string
	// Subject is who asked.
	Subject Subject
	// Asked is when the host accepted the request, and Expires when the
	// request ends unanswered.
	Asked   time.Time
	Expires time.Time
}

// Subject is the seat a request came from, and who holds it.
type Subject struct {
	// Kind is the kind of subject: a seat.
	Kind string
	// Role is the seat's role.
	Role string
	// Epoch and SeatRev are the seat's epoch and the revision of its
	// record. The approval ends when either moves.
	Epoch   int64
	SeatRev int64
	// HolderKind, DisplayID and Runtime describe the session that holds
	// the seat: its kind, its name for display and the program it runs in.
	HolderKind string
	DisplayID  string
	Runtime    string
}

// VerbLine is the second line of a card.
type VerbLine struct {
	// Name is the verb, such as "feature finish".
	Name string
	// Feature is the feature's name for a feature finish, and empty for a
	// prune. It is the one string a lead chose that stands on the model as
	// a plain string, because it passed [record.ValidFeatureName]. It can
	// still be chosen to mislead ("approve", "develop"), so a surface never
	// lets it stand for anything but a name.
	Feature string
	// Target is the branch a feature finish merges into, without
	// "refs/heads/", and empty for a prune.
	Target string
	// Class is the verb's class, such as "ref-move".
	Class string
}

// Plan is the plan block.
type Plan struct {
	// Token is the planner's token for the plan.
	Token string
	// SHA256 is the digest of the plan text, as the binding holds it.
	SHA256 string
	// Lines are the plan text word for word, one value for each line. The
	// text is the host's. Its lines are LeadText all the same: nothing
	// proves that no byte of a lead is in them.
	Lines []LeadText
}

// RefLine is one ref update the approval allows.
type RefLine struct {
	// Kind is the kind of effect: a ref.
	Kind string
	// Ref is the full name of the ref. A branch a prune deletes was named
	// by a lead, so every ref name is LeadText.
	Ref LeadText
	// Old is the object id the ref must be at, and New the id it moves
	// to.
	Old string
	New string
	// Deletes says that the update deletes the ref: New is all zeros.
	Deletes bool
	// MergedInto says where the commits of a deleted branch stay
	// reachable. Only a delete has it.
	MergedInto *MergedInto
}

// MergedInto names the branch, and its tip, that holds the commits of a
// branch a prune deletes.
type MergedInto struct {
	Ref LeadText
	Tip string
}

// Merge is the merge commit the host built for a feature finish.
type Merge struct {
	// Commit is the id of the merge commit.
	Commit string
	// Parents are its parents: the target's tip, then the feature's tip.
	Parents []string
	// BuiltBy says who built it: the host.
	BuiltBy string
}

// TargetTip returns the first parent, the tip of the branch merged into, or
// "" when the merge has none.
func (m Merge) TargetTip() string {
	if len(m.Parents) < 1 {
		return ""
	}
	return m.Parents[0]
}

// FeatureTip returns the second parent, the tip of the feature branch, or ""
// when the merge has none.
func (m Merge) FeatureTip() string {
	if len(m.Parents) < 2 {
		return ""
	}
	return m.Parents[1]
}

// Diff is the diff summary: the totals of the change, and one row for each
// changed path that the record holds.
type Diff struct {
	// ReviewDigest is the digest of the whole review. It covers the full
	// path list even when Rows is cut, and every [Page] names it.
	ReviewDigest string
	// Base and Tip are the two commits the review compares: the target's
	// tip and the merge commit.
	Base string
	Tip  string
	// Files, Added, Removed and NewBytes are the totals: changed paths,
	// lines added and removed, and bytes of new content.
	Files    int64
	Added    int64
	Removed  int64
	NewBytes int64
	// Rows are the changed paths, or the first part of them when Truncated
	// is true.
	Rows []Row
	// Truncated says that the record holds only the first part of the path
	// list. The whole list is then read in pages ([PagePaths]), and the
	// card may be approved only after every page of the whole list was read.
	Truncated bool
	// ExecConfig names the set of rules the paths were flagged with.
	ExecConfig ExecConfig
}

// TruncatedLine returns one line that says the path list is cut and how much
// of it the card holds, or "" when the list is whole.
func (d Diff) TruncatedLine() string {
	if !d.Truncated {
		return ""
	}
	return "the path list is cut: the card holds the first " + strconv.Itoa(len(d.Rows)) +
		" of " + strconv.FormatInt(d.Files, 10) + " paths, and the rest is read in pages"
}

// Row is one changed path.
type Row struct {
	// Path is the path as git stores it.
	Path LeadText
	// Status is "A" (added), "M" (modified), "D" (deleted) or "T" (the
	// type of the entry changed).
	Status string
	// OldMode and NewMode are the git file modes as six octal digits,
	// "000000" where there is no entry.
	OldMode string
	NewMode string
	// Added and Removed count lines, and NewBytes the bytes of new
	// content.
	Added    int64
	Removed  int64
	NewBytes int64
}

// RowOf returns the row of one changed path. A surface uses it for the paths
// of a [Page], so that they are drawn as the card's own rows are.
func RowOf(p record.PathChange) Row {
	return Row{
		Path:     NewLeadText(string(p.Path)),
		Status:   p.Status,
		OldMode:  p.OldMode,
		NewMode:  p.NewMode,
		Added:    p.Added,
		Removed:  p.Removed,
		NewBytes: p.NewBytes,
	}
}

// ExecConfig names the set of execution-config rules of a review.
type ExecConfig struct {
	// Matcher is the matcher's version.
	Matcher string
	// SHA256 is the digest of the rules in matching order.
	SHA256 string
	// Additions counts the rules the project added to the built-in set.
	Additions int64
}

// ListKind names one of the four lists of a summary. The values count from
// zero in the order the lists are drawn, so a ListKind indexes Card.Lists
// and Reading.Lists.
type ListKind int

// The four lists, and their number.
const (
	ListBinary ListKind = iota
	ListSymlinks
	ListModeChanges
	ListSubmodules
	NumLists int = iota
)

// Label returns the list's heading, or "" for a value that names no list.
func (k ListKind) Label() string {
	switch k {
	case ListBinary:
		return "Binary"
	case ListSymlinks:
		return "Symlinks"
	case ListModeChanges:
		return "Mode changes"
	case ListSubmodules:
		return "Submodules"
	}
	return ""
}

// noun is the list as it reads inside a sentence.
func (k ListKind) noun() string {
	switch k {
	case ListBinary:
		return "binary files"
	case ListSymlinks:
		return "symbolic links"
	case ListModeChanges:
		return "mode changes"
	case ListSubmodules:
		return "submodules"
	}
	return "paths"
}

// List is one of the four lists: the paths of one kind.
type List struct {
	Kind  ListKind
	Paths []LeadText
}

// FlaggedPath is one path that needs its own sign-off.
type FlaggedPath struct {
	// Kind is the kind of flag, such as "exec-config".
	Kind string
	// Path is the flagged path.
	Path LeadText
	// Pattern is the rule that matched, in the rule's own text, and empty
	// when the flag comes from no rule. A project can add rules, so the
	// text is escaped like a lead's.
	Pattern LeadText
	// Reason says in the host's words why a path of this kind is flagged.
	Reason string
}

// SignOff returns the sign-off of this path, as a [Reading] and a decision
// hold it. Its path is the raw bytes: it is for comparing and for the
// record, never for drawing.
func (f FlaggedPath) SignOff() record.SignOff {
	return record.SignOff{Kind: f.Kind, Path: record.Text(f.Path.bytes())}
}

// flagReason returns the host's words for a kind of flag.
func flagReason(kind string) string {
	switch kind {
	case record.FlagExecConfig:
		return "matches an execution-config rule"
	case record.FlagSymlink:
		return "a symbolic link"
	case record.FlagSubmodule:
		return "a submodule"
	case record.FlagAliased:
		return "lies in a folder that an execution-config link stands for"
	case record.FlagHistoryOnly:
		return "was flagged in a commit of the branch and is not in the net change"
	}
	return "flagged"
}

// Evidence names one record of the host that the approval rests on.
type Evidence struct {
	// Kind is the kind of record, such as "suite".
	Kind string
	// RecordID and RecordSHA256 are the record's id and digest.
	RecordID     string
	RecordSHA256 string
}

// After is what came after a card was shown. A surface draws it labelled
// [AfterLabel]. None of it is under the binding's digest.
type After struct {
	// State is the state of the pending record. Empty means pending.
	State record.State
	// Line is the outcome line, such as "applied: develop 93c8d68 ->
	// 0194daf". It is written by the host and may be empty.
	Line string
	// Held says that an approve of this card was made past the daily
	// limit, so it was recorded and not applied, and the card is in front
	// of the owner again. A surface draws [HeldLabel].
	Held bool
}

// Inputs carries what a card holds that is not in its record.
type Inputs struct {
	// Earlier is the earlier card of the "differs from" block: a card for
	// the same verb and target that the owner opened before. The caller
	// decides whether there is one (the design says: within the last 24
	// hours); nil means none.
	Earlier *Earlier
	// Held is the held mark; see After.Held.
	Held bool
	// State is the state of the pending record. Empty means pending.
	State record.State
	// Line is the outcome line, written by the host.
	Line string
	// Verdicts are the verdicts. New copies them to the card only when
	// State says the card is decided.
	Verdicts *VerdictBlock
}

// Earlier is an earlier card the owner read.
type Earlier struct {
	// Record is the earlier card's pending record.
	Record record.Pending
	// ReadAt is when the owner read it.
	ReadAt time.Time
}

// New builds the card of the pending record p. It checks the record again,
// whoever decoded it:
//
//   - a feature finish whose argv is not exactly one name that passes
//     [record.ValidFeatureName] is refused feature-name;
//   - a record whose binding_sha256 is not the digest of its binding, or
//     whose plan_sha256 is not the digest of its plan text, is refused
//     binding-mismatch;
//   - every other rule of [record.DecodePending] holds as well, with that
//     function's codes;
//   - a record in which a string the host wrote holds something the
//     lead-text escaping would change is refused record-invalid, and so are
//     inputs that do not fit the record.
//
// Every refusal is a [*record.Error], and then there is no card.
func New(p record.Pending, in Inputs) (Card, error) {
	if err := check(p); err != nil {
		return Card{}, err
	}
	if in.State != "" && !in.State.Valid() {
		return Card{}, invalid("inputs.state is not a state of a pending record")
	}
	if !hostText(in.Line) {
		return Card{}, invalid("inputs.line holds text that would need escaping")
	}
	c := build(p)
	if in.Earlier != nil {
		d, err := DiffersFrom(in.Earlier.Record, p, in.Earlier.ReadAt)
		if err != nil {
			return Card{}, err
		}
		c.Differs = &d
	}
	c.After = After{State: in.State, Line: in.Line, Held: in.Held}
	if c.Decided() {
		c.Verdicts = in.Verdicts
	}
	return c, nil
}

// invalid returns the refusal of a record or of inputs that are not well
// formed. The detail is this package's own text.
func invalid(detail string) *record.Error {
	return &record.Error{Code: record.CodeRecordInvalid, Detail: detail}
}

// check makes the checks of New on the record alone.
func check(p record.Pending) error {
	b := &p.Binding
	// The feature name first, so that a name outside the grammar always
	// has its own code, whatever else is wrong with the record.
	if b.Verb == record.VerbFeatureFinish && (len(b.Argv) != 1 || !record.ValidFeatureName(b.Argv[0])) {
		return &record.Error{
			Code:   record.CodeFeatureName,
			Detail: "pending.binding.argv is not exactly one feature name of 1 to 48 lower-case letters, digits and dashes",
		}
	}
	// The encoder makes every check of the decoder, the size cap included.
	if _, err := record.EncodePending(p); err != nil {
		return err
	}
	// The two digests again, here, so that the card's promise does not
	// rest on what the encoder happens to check.
	if b.PlanSHA256 != record.PlanDigest(string(p.PlanText)) {
		return &record.Error{Code: record.CodeBindingMismatch, Detail: "pending.binding.plan_sha256 is not the digest of pending.plan_text"}
	}
	digest, err := record.BindingDigest(*b)
	if err != nil {
		return err
	}
	if digest != p.BindingSHA256 {
		return &record.Error{Code: record.CodeBindingMismatch, Detail: "pending.binding_sha256 is not the digest of pending.binding"}
	}

	// The strings that stand on the model as plain strings.
	type hostString struct{ s, where string }
	host := []hostString{
		{b.Project, "project"},
		{b.Subject.RoleName, "subject.role_name"},
		{b.PlanToken, "plan_token"},
	}
	if h := b.Subject.Holder; h != nil {
		host = append(host,
			hostString{h.Kind, "subject.holder.kind"},
			hostString{h.DisplayID, "subject.holder.display_id"},
			hostString{h.Runtime, "subject.holder.runtime"})
	}
	if b.Summary != nil {
		host = append(host, hostString{b.Summary.ExecConfigSet.Matcher, "summary.exec_config_set.matcher"})
	}
	for _, e := range b.Evidence {
		host = append(host, hostString{e.RecordID, "evidence.record_id"})
	}
	if b.Verb == record.VerbFeatureFinish && len(b.Effects) == 1 {
		// The target of the verb line is made from this ref.
		host = append(host, hostString{b.Effects[0].Ref, "effects[0].ref"})
	}
	for _, h := range host {
		if !hostText(h.s) {
			return invalid("pending.binding." + h.where + " holds text that would need escaping")
		}
	}
	return nil
}

// branchPrefix starts the full name of every branch.
const branchPrefix = "refs/heads/"

// build turns a record into the model. It checks nothing: New has checked
// the record before, and build must not fail on any record.
func build(p record.Pending) Card {
	b := p.Binding
	c := Card{
		Title: Title{
			ID:      p.ID,
			Project: b.Project,
			Subject: Subject{
				Kind:    b.Subject.Kind,
				Role:    b.Subject.RoleName,
				Epoch:   b.Subject.Epoch,
				SeatRev: b.Subject.SeatRev,
			},
			Asked:   recordTime(b.RequestedAt),
			Expires: recordTime(b.PendingExpiresAt),
		},
		Verb: VerbLine{Name: b.Verb, Class: b.Class},
		Plan: Plan{
			Token:  b.PlanToken,
			SHA256: b.PlanSHA256,
			Lines:  NewLeadText(string(p.PlanText)).Lines(),
		},
		BindingSHA256: p.BindingSHA256,
		Note:          NewLeadText(string(p.Note)),
	}
	if h := b.Subject.Holder; h != nil {
		c.Title.Subject.HolderKind = h.Kind
		c.Title.Subject.DisplayID = h.DisplayID
		c.Title.Subject.Runtime = h.Runtime
	}
	if b.Verb == record.VerbFeatureFinish {
		// Every name of the argv, so that none is left out if a record
		// ever had more than the one New allows.
		c.Verb.Feature = strings.Join(b.Argv, " ")
		if len(b.Effects) > 0 {
			c.Verb.Target = strings.TrimPrefix(b.Effects[0].Ref, branchPrefix)
		}
	}
	for _, e := range b.Effects {
		line := RefLine{
			Kind:    e.Kind,
			Ref:     NewLeadText(e.Ref),
			Old:     e.Old,
			New:     e.New,
			Deletes: e.New != "" && strings.Trim(e.New, "0") == "",
		}
		if e.MergedInto != nil {
			line.MergedInto = &MergedInto{Ref: NewLeadText(e.MergedInto.Ref), Tip: e.MergedInto.Tip}
		}
		c.Refs = append(c.Refs, line)
	}
	if m := b.Merge; m != nil {
		c.Merge = &Merge{Commit: m.Commit, Parents: append([]string(nil), m.Parents...), BuiltBy: m.BuiltBy}
	}
	if s := b.Summary; s != nil {
		d := &Diff{
			ReviewDigest: s.ReviewDigest,
			Base:         s.Base,
			Tip:          s.Tip,
			Files:        s.Files,
			Added:        s.Added,
			Removed:      s.Removed,
			NewBytes:     s.NewBytes,
			Truncated:    s.PathsTruncated,
			ExecConfig: ExecConfig{
				Matcher:   s.ExecConfigSet.Matcher,
				SHA256:    s.ExecConfigSet.SHA256,
				Additions: s.ExecConfigSet.Additions,
			},
		}
		for _, path := range s.Paths {
			d.Rows = append(d.Rows, RowOf(path))
		}
		c.Diff = d
		c.Lists = []List{
			{Kind: ListBinary, Paths: leadTexts(s.Binary)},
			{Kind: ListSymlinks, Paths: leadTexts(s.Symlinks)},
			{Kind: ListModeChanges, Paths: leadTexts(s.ModeChanges)},
			{Kind: ListSubmodules, Paths: leadTexts(s.Submodules)},
		}
		for _, f := range s.Flagged {
			c.Flagged = append(c.Flagged, FlaggedPath{
				Kind:    f.Kind,
				Path:    NewLeadText(string(f.Path)),
				Pattern: NewLeadText(f.Pattern),
				Reason:  flagReason(f.Kind),
			})
		}
	}
	for _, e := range b.Evidence {
		c.Evidence = append(c.Evidence, Evidence{Kind: e.Kind, RecordID: e.RecordID, RecordSHA256: e.RecordSHA256})
	}
	return c
}

// recordTime reads a record's time, and gives the zero time for a string
// that is none.
func recordTime(s string) time.Time {
	t, err := record.ParseTime(s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Ended reports whether the request has ended: its record is in any state
// but pending. An ended card can be read and can no longer be decided.
func (c Card) Ended() bool {
	return c.After.State != "" && c.After.State != record.StatePending
}

// Decided reports whether a decision was made on the request, by the owner
// or under a rule or a grant: it was approved or denied, whatever came of
// the approval. A request that was withdrawn, expired or voided is ended
// and not decided, since the record's state does not say that anyone
// decided it.
func (c Card) Decided() bool {
	return decided(c.After.State)
}

func decided(s record.State) bool {
	switch s {
	case record.StateApproved, record.StateApprovedByRule, record.StateApprovedByGrant,
		record.StateDenied, record.StateConsumed, record.StateApplied,
		record.StateNotApplied, record.StateUnknown:
		return true
	}
	return false
}
