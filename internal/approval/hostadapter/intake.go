//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/record"
	"ataniq/internal/intake"
	"ataniq/internal/intake/execconfig"
)

// Resolve implements [ownerchannel.Intake]: it reads the lead's feature
// branch and the target in the mirror, each by its exact name and once. It
// does not refresh the lead's branch: the mirror's own sync does.
func (h *Host) Resolve(ctx context.Context, in ownerchannel.ResolveInput) (ownerchannel.Resolved, error) {
	p, err := h.project(in.Project)
	if err != nil {
		return ownerchannel.Resolved{}, err
	}
	if len(in.Request.Argv) != 1 || !record.ValidFeatureName(in.Request.Argv[0]) {
		return ownerchannel.Resolved{}, refusal(record.CodeFeatureName, "the feature name is not 1 to 48 lower-case letters, digits and dashes")
	}
	feature := p.featureRef(in.Request.Argv[0])
	got, err := h.gitCall(p).refs(ctx, p.Target, feature)
	if err != nil {
		return ownerchannel.Resolved{}, err
	}
	target, ok := got[p.Target]
	if !ok {
		return ownerchannel.Resolved{}, errors.New("hostadapter: the target branch is not in the mirror")
	}
	f, ok := got[feature]
	if !ok {
		return ownerchannel.Resolved{}, refusal(record.CodeFeatureMoved, "the feature branch is not in the mirror")
	}
	if target.typ != "commit" || f.typ != "commit" {
		return ownerchannel.Resolved{}, errors.New("hostadapter: the target or the feature is not a commit")
	}
	return ownerchannel.Resolved{TargetRef: p.Target, TargetTip: target.id, FeatureTip: f.id}, nil
}

// TargetTip implements [ownerchannel.Intake]: the present tip of the project's
// target branch. A ref that is not the project's target is refused.
func (h *Host) TargetTip(ctx context.Context, project, ref string) (string, error) {
	p, err := h.project(project)
	if err != nil {
		return "", err
	}
	if ref != p.Target {
		return "", errors.New("hostadapter: the ref is not the project's target")
	}
	got, err := h.gitCall(p).refs(ctx, ref)
	if err != nil {
		return "", err
	}
	info, ok := got[ref]
	if !ok || info.typ != "commit" {
		return "", errors.New("hostadapter: the target branch is not a commit in the mirror")
	}
	return info.id, nil
}

// pinOf returns the pin that holds the merge: a name under the pending
// prefix. The service made the pin's key from the request, which this call
// is not told, so the pin is found by the commit it holds. The merge has
// both the target's tip and the feature's tip as parents, so no two requests
// share one; of several pins to the one commit the first by name is taken.
func (h *Host) pinOf(ctx context.Context, p Project, merge string) (string, error) {
	pins, err := h.gitCall(p).pinsAt(ctx, intake.PendingPrefix, merge)
	if err != nil {
		return "", err
	}
	if len(pins) == 0 {
		return "", errNoPin
	}
	return pins[0], nil
}

var errNoPin = errors.New("hostadapter: the merge has no pin")

// changedError turns what the intake says of a review whose base or tip is no
// longer what it was into a refusal with the given code, which says that the
// review is not the one that was asked for. It returns false for any other
// error.
func changedError(err error, c record.Code) (*record.Error, bool) {
	var mb *intake.MergeBaseError
	var nf *intake.NotFastForwardError
	switch {
	case errors.As(err, &mb), errors.As(err, &nf), errors.Is(err, intake.ErrNoRef), errors.Is(err, intake.ErrNotMerge), errors.Is(err, errNoPin):
		return refusal(c, "the target or the pin of the merge is not what the review was made from"), true
	}
	return nil, false
}

// mapReviewError maps the errors of the intake's reads to the refusal the
// owner connection speaks in: a review that is no longer the one asked for
// has the code changed, and a change larger than the host reads is
// record-too-large (the 64 MiB limit of the summary's scan, a patch past its
// limit, too many paths or commits). Any other error is the host's failure
// and passes as it is.
func mapReviewError(err error, changed record.Code) error {
	if err == nil {
		return nil
	}
	if e, ok := changedError(err, changed); ok {
		return e
	}
	if errors.Is(err, intake.ErrLimit) {
		return refusal(record.CodeRecordTooLarge, "the change is larger than the host reads for a card")
	}
	return err
}

// inspect reviews the merge in merge mode against the target as it is now.
func (h *Host) inspect(ctx context.Context, p Project, merge string) (*intake.Review, error) {
	pin, err := h.pinOf(ctx, p, merge)
	if err != nil {
		return nil, err
	}
	return intake.Inspect(ctx, h.source(p), intake.Request{
		BaseRef: p.Target, TipRef: pin, Rules: p.Rules, Merge: true, Limits: h.cfg.Limits,
	})
}

// review is the intake's review of base to merge and its summary. It is of
// the target's tip now: a base that is not the first parent of the merge is a
// moved target, and is answered with the code changed.
func (h *Host) review(ctx context.Context, p Project, base, merge string, changed record.Code) (*intake.Review, *intake.Summary, error) {
	rev, err := h.inspect(ctx, p, merge)
	if err != nil {
		return nil, nil, mapReviewError(err, changed)
	}
	if rev.Base != base || rev.Tip != merge {
		return nil, nil, refusal(changed, "the target or the pin of the merge is not what the review was made from")
	}
	sum, err := intake.Summarize(ctx, h.source(p), rev, h.cfg.Limits)
	if err != nil {
		return nil, nil, mapReviewError(err, changed)
	}
	return rev, sum, nil
}

// Review implements [ownerchannel.Intake]: the changed paths (as many as the
// record holds), the flags, the totals, the review digest and the digest of
// the rule set, for the merge against the target's tip.
func (h *Host) Review(ctx context.Context, in ownerchannel.ReviewInput) (record.Summary, error) {
	p, err := h.project(in.Project)
	if err != nil {
		return record.Summary{}, err
	}
	rev, sum, err := h.review(ctx, p, in.Base, in.Merge, record.CodeBindingMismatch)
	if err != nil {
		return record.Summary{}, err
	}
	return buildSummary(p, rev, sum)
}

// Paths implements [ownerchannel.Intake]: the whole list of changed paths,
// with the review digest it was made under.
func (h *Host) Paths(ctx context.Context, ref ownerchannel.ReviewRef) (ownerchannel.ReviewPaths, error) {
	p, err := h.project(ref.Project)
	if err != nil {
		return ownerchannel.ReviewPaths{}, err
	}
	rev, sum, err := h.review(ctx, p, ref.Base, ref.Merge, record.CodeReviewChanged)
	if err != nil {
		return ownerchannel.ReviewPaths{}, err
	}
	return ownerchannel.ReviewPaths{ReviewDigest: rev.Digest(), Paths: pathChanges(rev, sum)}, nil
}

// Patch implements [ownerchannel.Intake]: the patch of the review as the
// intake writes it, one entry for each line, without the header line the
// intake puts first. The lines are escaped, as the intake writes them; the
// daemon turns them back (leaddaemon.UnescapingIntake) before the service
// pages them.
func (h *Host) Patch(ctx context.Context, ref ownerchannel.ReviewRef) (ownerchannel.ReviewPatch, error) {
	p, err := h.project(ref.Project)
	if err != nil {
		return ownerchannel.ReviewPatch{}, err
	}
	rev, err := h.inspect(ctx, p, ref.Merge)
	if err != nil {
		return ownerchannel.ReviewPatch{}, mapReviewError(err, record.CodeReviewChanged)
	}
	if rev.Base != ref.Base || rev.Tip != ref.Merge {
		return ownerchannel.ReviewPatch{}, refusal(record.CodeReviewChanged, "the target or the pin of the merge is not what the review was made from")
	}
	var buf bytes.Buffer
	if err := intake.Diff(ctx, h.source(p), rev, &buf, h.cfg.Limits); err != nil {
		return ownerchannel.ReviewPatch{}, mapReviewError(err, record.CodeReviewChanged)
	}
	header, body, _ := strings.Cut(buf.String(), "\n")
	if header != fmt.Sprintf("ataniq intake diff base=%s tip=%s", rev.Base, rev.Tip) {
		return ownerchannel.ReviewPatch{}, errors.New("hostadapter: the patch does not start with the intake's header")
	}
	lines := []string{}
	if body != "" {
		lines = strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	}
	return ownerchannel.ReviewPatch{ReviewDigest: rev.Digest(), Lines: lines}, nil
}

// flagKinds maps the intake's kinds of flag to the record's. The intake's
// odd-path flag has no kind in the record, so it is not here.
var flagKinds = map[intake.FlagKind]string{
	intake.FlagExecConfig: record.FlagExecConfig,
	intake.FlagSymlink:    record.FlagSymlink,
	intake.FlagSubmodule:  record.FlagSubmodule,
	intake.FlagAliased:    record.FlagAliased,
	intake.FlagHistory:    record.FlagHistoryOnly,
}

// mode writes a git file mode as six octal digits.
func mode(m uint32) string { return fmt.Sprintf("%06o", m) }

// pathChanges lists every changed path of the review as the record has it.
func pathChanges(rev *intake.Review, sum *intake.Summary) []record.PathChange {
	out := make([]record.PathChange, 0, len(sum.Paths))
	for i, ps := range sum.Paths {
		ch := rev.Changes[i]
		var added int64
		// A blob that is the same on both sides brings no new bytes: a
		// change of mode alone.
		if ch.NewMode != 0 && ch.NewMode != 0o160000 && ch.NewID != ch.OldID {
			added = ps.NewSize
		}
		out = append(out, record.PathChange{
			Path: record.Text(ps.Path), Status: string(ps.Status),
			OldMode: mode(ps.OldMode), NewMode: mode(ps.NewMode),
			Added: ps.Added, Removed: ps.Removed, NewBytes: added,
		})
	}
	return out
}

// texts turns a list of raw paths into the record's lists, never nil.
func texts(paths []string) []record.Text {
	out := make([]record.Text, 0, len(paths))
	for _, p := range paths {
		out = append(out, record.Text(p))
	}
	return out
}

// estimate is the bytes a path may take in a pending record: its text in the
// worse form (base 64 and escapes) and the fields around it.
func estimate(path string) int { return 2*len(path) + 160 }

// buildSummary makes the record's summary of a review. The flagged paths and
// the four lists are never cut, and a change whose lists do not fit is
// refused record-too-large; the path list is cut when it is long, and marked,
// and the card reads the rest in pages (owner.paths). A flag the record has
// no kind for (an odd path) is refused: a path that needs a sign-off cannot
// be left out of the list that carries them.
func buildSummary(p Project, rev *intake.Review, sum *intake.Summary) (record.Summary, error) {
	flagged := make([]record.Flag, 0, len(rev.Flagged))
	used := 0
	for _, f := range rev.Flagged {
		kind, ok := flagKinds[f.Kind]
		if !ok {
			return record.Summary{}, refusal(record.CodeRequestInvalid, "a path of the change has a flag the record cannot carry, so no card is made")
		}
		flagged = append(flagged, record.Flag{Kind: kind, Path: record.Text(f.Path), Pattern: f.Pattern})
		used += estimate(f.Path) + len(f.Pattern)
	}
	for _, list := range [][]string{sum.Binary, sum.Symlinks, sum.ModeChanges, sum.Submodules} {
		for _, path := range list {
			used += estimate(path)
		}
	}
	if used > maxListBudget {
		return record.Summary{}, refusal(record.CodeRecordTooLarge, "the flagged paths and the lists of the change do not fit in a card")
	}
	all := pathChanges(rev, sum)
	cut, spent := len(all), 0
	for i, pc := range all {
		spent += estimate(string(pc.Path))
		if spent > maxSummaryPathBudget {
			cut = i
			break
		}
	}
	return record.Summary{
		ReviewDigest: rev.Digest(), Base: rev.Base, Tip: rev.Tip,
		Files: int64(sum.Files), Added: sum.Added, Removed: sum.Removed, NewBytes: sum.NewBytes,
		Paths: all[:cut:cut], PathsTruncated: cut < len(all),
		Flagged: flagged,
		Binary:  texts(sum.Binary), Symlinks: texts(sum.Symlinks), ModeChanges: texts(sum.ModeChanges), Submodules: texts(sum.Submodules),
		ExecConfigSet: record.ExecConfigSet{
			Matcher: execconfig.MatcherVersion, SHA256: execconfig.Digest(p.Rules), Additions: int64(execconfig.Additions(p.Rules)),
		},
	}, nil
}
