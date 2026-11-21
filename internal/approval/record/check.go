package record

import (
	"strconv"
	"strings"
	"time"
)

// This file holds the checks of a record's content that the decoders and
// the encoders share: the closed words, the grammars of ids and names, the
// digests, and which fields go with which verb. What the codec already
// holds (the types, the keys, the control characters) is not repeated here.

// timeLayout is the one form of a time in a record: RFC 3339 in UTC to the
// second, ending in Z.
const timeLayout = "2006-01-02T15:04:05Z"

// FormatTime writes t as a record's time: in UTC, to the second.
func FormatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// ParseTime reads a record's time. It refuses every other spelling of a
// time: an offset, a fraction of a second, lower-case letters. The decoders
// have checked the times of a record they return, so a caller that parses
// one of those can count on success; for any other string the refusal is
// record-invalid.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil || t.Format(timeLayout) != s {
		return time.Time{}, &Error{Code: CodeRecordInvalid, Detail: "a time is not RFC 3339 in UTC to the second"}
	}
	return t, nil
}

// checker collects the first refusal of a record. invalid is the code of
// the record's kind for a broken form.
type checker struct {
	invalid Code
	err     *Error
}

// fail records a refusal with its own code, unless one is recorded already.
func (c *checker) fail(code Code, detail string) {
	if c.err == nil {
		c.err = &Error{Code: code, Detail: detail}
	}
}

// need records a refusal with the kind's code when ok is false.
func (c *checker) need(ok bool, detail string) {
	if !ok {
		c.fail(c.invalid, detail)
	}
}

// result returns the first refusal as an error, or nil. It returns the
// untyped nil, so that a caller's "err != nil" is right.
func (c *checker) result() error {
	if c.err == nil {
		return nil
	}
	return c.err
}

func (c *checker) time(s, where string) {
	_, err := ParseTime(s)
	c.need(err == nil, where+" is not a time in UTC to the second, ending in Z")
}

func (c *checker) sha256(s, where string) {
	c.need(len(s) == 64 && all(s, isHexByte), where+" is not 64 lower-case hexadecimal digits")
}

func (c *checker) objectID(s, where string) {
	c.need(ValidObjectID(s), where+" is not a full object id")
}

func (c *checker) filled(s, where string) {
	c.need(s != "", where+" is empty")
}

func (c *checker) count(n int64, where string) {
	c.need(n >= 0, where+" is negative")
}

// verb checks a verb against the closed list and its argv against the
// verb's grammar.
func (c *checker) verb(verb string, argv []string, where string) {
	switch verb {
	case VerbFeatureFinish:
		if len(argv) != 1 || !ValidFeatureName(argv[0]) {
			c.fail(CodeFeatureName, where+".argv is not exactly one feature name of 1 to 48 lower-case letters, digits and dashes")
		}
	case VerbBranchPrune:
		c.need(len(argv) == 0, where+".argv is not empty for a prune")
	default:
		c.fail(CodeVerbNotRequestable, where+".verb is not a verb that can be asked for")
	}
}

func checkRequest(r *Request) error {
	c := &checker{invalid: CodeRequestInvalid}
	c.need(r.Schema == SchemaRequest, "request.schema is not "+SchemaRequest)
	c.need(ValidRequestID(r.RequestID), "request.request_id is not rq- and 1 to 48 lower-case letters, digits and dashes")
	c.count(r.Epoch, "request.epoch")
	c.verb(r.Verb, r.Argv, "request")
	if r.Verb == VerbFeatureFinish {
		c.objectID(r.FeatureSHA, "request.feature_sha")
	} else {
		c.need(r.FeatureSHA == "", "request.feature_sha is given for a prune")
	}
	c.sha256(r.NoteSHA256, "request.note_sha256")
	c.time(r.At, "request.at")
	return c.result()
}

func checkPending(p *Pending) error {
	c := &checker{invalid: CodeRecordInvalid}
	c.need(p.Schema == SchemaPending, "pending.schema is not "+SchemaPending)
	c.need(ValidPendingID(p.ID), "pending.id is not ap- and 1 to 48 lower-case letters, digits and dashes")
	c.filled(p.Planner, "pending.planner")
	c.need(ValidRequestID(p.RequestID), "pending.request_id is not rq- and 1 to 48 lower-case letters, digits and dashes")
	c.sha256(p.RequestSHA256, "pending.request_sha256")
	if len(p.Note) > MaxNoteBytes {
		c.fail(CodeRecordTooLarge, "the note is larger than "+strconv.Itoa(MaxNoteBytes)+" bytes")
	}
	c.binding(&p.Binding)
	c.sha256(p.BindingSHA256, "pending.binding_sha256")
	if c.err != nil {
		return c.err
	}
	// The digests are compared last, on a record that is otherwise whole,
	// so that a broken form is never reported as a mismatch.
	if p.Binding.PlanSHA256 != PlanDigest(string(p.PlanText)) {
		c.fail(CodeBindingMismatch, "pending.binding.plan_sha256 is not the digest of pending.plan_text")
	}
	if got, err := BindingDigest(p.Binding); err != nil {
		c.need(false, "pending.binding cannot be written in the canonical form")
	} else if got != p.BindingSHA256 {
		c.fail(CodeBindingMismatch, "pending.binding_sha256 is not the digest of pending.binding")
	}
	return c.result()
}

func (c *checker) binding(b *Binding) {
	const at = "pending.binding"
	c.filled(b.Project, at+".project")
	c.subject(&b.Subject, at+".subject")
	if b.Subject.Holder == nil {
		c.need(false, at+".subject.holder is missing")
	} else {
		c.filled(b.Subject.Holder.Kind, at+".subject.holder.kind")
		c.filled(b.Subject.Holder.DisplayID, at+".subject.holder.display_id")
		c.filled(b.Subject.Holder.Runtime, at+".subject.holder.runtime")
	}
	c.verb(b.Verb, b.Argv, at)
	c.need(b.Class == ClassRefMove, at+".class is not "+ClassRefMove)
	c.filled(b.PlanToken, at+".plan_token")
	c.sha256(b.PlanSHA256, at+".plan_sha256")

	c.need(len(b.Effects) >= 1 && len(b.Effects) <= MaxEffects, at+".effects is not 1 to "+strconv.Itoa(MaxEffects)+" effects")
	for i := range b.Effects {
		c.effect(&b.Effects[i], at+".effects["+strconv.Itoa(i)+"]")
	}

	finish := b.Verb == VerbFeatureFinish
	switch {
	case finish && b.Merge == nil:
		c.need(false, at+".merge is missing for a feature finish")
	case !finish && b.Merge != nil:
		c.need(false, at+".merge is given for a prune")
	case b.Merge != nil:
		c.objectID(b.Merge.Commit, at+".merge.commit")
		c.need(len(b.Merge.Parents) == 2, at+".merge.parents is not exactly two parents")
		for i, parent := range b.Merge.Parents {
			c.objectID(parent, at+".merge.parents["+strconv.Itoa(i)+"]")
		}
		c.need(b.Merge.BuiltBy == BuiltByHost, at+".merge.built_by is not "+BuiltByHost)
	}
	switch {
	case finish && b.Summary == nil:
		c.need(false, at+".summary is missing for a feature finish")
	case !finish && b.Summary != nil:
		c.need(false, at+".summary is given for a prune")
	case b.Summary != nil:
		c.summary(b.Summary, at+".summary")
	}

	suite := false
	for i, e := range b.Evidence {
		where := at + ".evidence[" + strconv.Itoa(i) + "]"
		c.need(e.Kind == EvidenceSuite, where+".kind is not "+EvidenceSuite)
		c.filled(e.RecordID, where+".record_id")
		c.sha256(e.RecordSHA256, where+".record_sha256")
		suite = true
	}
	c.need(suite || !finish, at+".evidence has no suite record for a feature finish")
	c.time(b.RequestedAt, at+".requested_at")
	c.time(b.PendingExpiresAt, at+".pending_expires_at")
	c.agree(b, at)
}

// agree checks the parts of a binding against each other, after each part
// was checked alone. An approval allows the effects and nothing else, and
// what the owner is shown is the merge and the summary, so the three must
// say the same thing.
//
// A feature finish moves one branch from the target's tip to the merge the
// host built, and nothing else: it has exactly one effect, whose old is the
// merge's first parent and whose new is the merge commit, and its summary
// compares those same two commits. A prune only deletes branches: every
// effect of it has a new of all zeros, and so says where the branch was
// merged. No effect of either verb is on a ref that is not a branch.
//
// Every consumer of a pending record reads it through the decoder, so the
// rules are held here once and not by each of them.
func (c *checker) agree(b *Binding, at string) {
	finish := b.Verb == VerbFeatureFinish
	for i := range b.Effects {
		e, where := &b.Effects[i], at+".effects["+strconv.Itoa(i)+"]"
		c.need(isBranch(e.Ref), where+".ref is not a branch under refs/heads/")
		if finish {
			c.need(!deletes(e), where+" deletes a ref, which a feature finish does not do")
		} else {
			c.need(deletes(e), where+" does not delete its ref, which is all a prune does")
		}
	}
	if !finish {
		return
	}
	c.need(len(b.Effects) == 1, at+".effects is not exactly one effect for a feature finish")
	if len(b.Effects) != 1 || b.Merge == nil || len(b.Merge.Parents) != 2 || b.Summary == nil {
		// One of the parts is missing or malformed, and that is
		// refused already.
		return
	}
	e := &b.Effects[0]
	c.need(e.Old == b.Merge.Parents[0], at+".effects[0].old is not the first parent of the merge")
	c.need(e.New == b.Merge.Commit, at+".effects[0].new is not the merge commit")
	c.need(b.Summary.Base == b.Merge.Parents[0], at+".summary.base is not the first parent of the merge")
	c.need(b.Summary.Tip == b.Merge.Commit, at+".summary.tip is not the merge commit")
}

// branchPrefix starts the full name of every branch.
const branchPrefix = "refs/heads/"

// isBranch reports whether ref is the full name of a branch: refs/heads/
// and at least one more byte. It does not check the rest of the name
// against git's rules for a ref name.
func isBranch(ref string) bool {
	return len(ref) > len(branchPrefix) && strings.HasPrefix(ref, branchPrefix)
}

// deletes reports whether e deletes its ref: its new is an id of all zeros.
func deletes(e *Effect) bool {
	return e.New != "" && strings.Trim(e.New, "0") == ""
}

// subject checks the fields a pending record and a decision share. The
// holder is checked by each of them.
func (c *checker) subject(s *Subject, where string) {
	c.need(s.Kind == SubjectSeat, where+".kind is not "+SubjectSeat)
	c.filled(s.RoleName, where+".role_name")
	c.count(s.Epoch, where+".epoch")
	c.count(s.SeatRev, where+".seat_rev")
}

func (c *checker) effect(e *Effect, where string) {
	c.need(e.Kind == EffectRef, where+".kind is not "+EffectRef)
	c.filled(e.Ref, where+".ref")
	c.objectID(e.Old, where+".old")
	c.objectID(e.New, where+".new")
	c.need(len(e.Old) == len(e.New), where+".old and .new are ids of different lengths")
	gone := deletes(e)
	switch {
	case gone && e.MergedInto == nil:
		c.need(false, where+".merged_into is missing for a delete")
	case !gone && e.MergedInto != nil:
		c.need(false, where+".merged_into is given for an effect that deletes nothing")
	case e.MergedInto != nil:
		c.filled(e.MergedInto.Ref, where+".merged_into.ref")
		c.objectID(e.MergedInto.Tip, where+".merged_into.tip")
	}
}

func (c *checker) summary(s *Summary, where string) {
	c.sha256(s.ReviewDigest, where+".review_digest")
	c.objectID(s.Base, where+".base")
	c.objectID(s.Tip, where+".tip")
	c.count(s.Files, where+".files")
	c.count(s.Added, where+".added")
	c.count(s.Removed, where+".removed")
	c.count(s.NewBytes, where+".new_bytes")
	for i := range s.Paths {
		p, at := &s.Paths[i], where+".paths["+strconv.Itoa(i)+"]"
		c.need(p.Status == "A" || p.Status == "M" || p.Status == "D" || p.Status == "T", at+".status is not A, M, D or T")
		c.need(validMode(p.OldMode), at+".old_mode is not six octal digits")
		c.need(validMode(p.NewMode), at+".new_mode is not six octal digits")
		c.count(p.Added, at+".added")
		c.count(p.Removed, at+".removed")
		c.count(p.NewBytes, at+".new_bytes")
	}
	seen := make(map[SignOff]struct{}, len(s.Flagged))
	for i, f := range s.Flagged {
		at := where + ".flagged[" + strconv.Itoa(i) + "]"
		c.need(validFlagKind(f.Kind), at+".kind is not a kind of flag")
		pair := SignOff{Kind: f.Kind, Path: f.Path}
		_, dup := seen[pair]
		c.need(!dup, at+" repeats an earlier flag")
		seen[pair] = struct{}{}
	}
	c.filled(s.ExecConfigSet.Matcher, where+".exec_config_set.matcher")
	c.sha256(s.ExecConfigSet.SHA256, where+".exec_config_set.sha256")
	c.count(s.ExecConfigSet.Additions, where+".exec_config_set.additions")
}

func checkDecision(d *Decision) error {
	c := &checker{invalid: CodeDecisionInvalid}
	c.need(d.Schema == SchemaDecision, "decision.schema is not "+SchemaDecision)
	c.need(ValidPendingID(d.ID), "decision.id is not ap- and 1 to 48 lower-case letters, digits and dashes")
	c.filled(d.Planner, "decision.planner")
	c.sha256(d.BindingSHA256, "decision.binding_sha256")
	c.filled(d.Project, "decision.project")
	c.subject(&d.Subject, "decision.subject")
	c.need(d.Subject.Holder == nil, "decision.subject.holder is given; a decision repeats the subject without it")
	c.filled(d.PlanToken, "decision.plan_token")
	seen := make(map[SignOff]struct{}, len(d.SignedOff))
	for i, s := range d.SignedOff {
		at := "decision.signed_off[" + strconv.Itoa(i) + "]"
		c.need(validFlagKind(s.Kind), at+".kind is not a kind of flag")
		_, dup := seen[s]
		c.need(!dup, at+" repeats an earlier sign-off")
		seen[s] = struct{}{}
	}
	switch d.Decision {
	case DecisionApprove:
		c.need(d.Uses == 1, "decision.uses is not 1 for an approve")
		c.need(d.Reason == "", "decision.reason is given for an approve")
	case DecisionDeny:
		c.need(d.Uses == 0, "decision.uses is not 0 for a deny")
		c.need(len(d.Reason) <= MaxReasonBytes, "decision.reason is larger than "+strconv.Itoa(MaxReasonBytes)+" bytes")
	default:
		c.need(false, "decision.decision is not approve or deny")
	}
	c.time(d.DecidedAt, "decision.decided_at")
	c.time(d.ExpiresAt, "decision.expires_at")
	c.need(d.Confirmed == ConfirmedCode, "decision.confirmed is not "+ConfirmedCode)
	c.need(d.Console.PID >= 1, "decision.console.pid is not a process id")
	c.filled(d.Console.Client, "decision.console.client")
	c.filled(d.Console.Surface, "decision.console.surface")
	c.need(len(d.Console.Session) == 16 && all(d.Console.Session, isHexByte), "decision.console.session is not 16 lower-case hexadecimal digits")
	return c.result()
}

func validFlagKind(k string) bool {
	switch k {
	case FlagExecConfig, FlagSymlink, FlagSubmodule, FlagAliased, FlagHistoryOnly, FlagOddPath:
		return true
	}
	return false
}

// validMode reports whether s is a git file mode as six octal digits.
func validMode(s string) bool {
	return len(s) == 6 && all(s, func(c byte) bool { return c >= '0' && c <= '7' })
}
