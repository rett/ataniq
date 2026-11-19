package card

import (
	"time"

	"ataniq/internal/approval/record"
)

// Differs is the "differs from" block: how a card differs from an earlier
// card for the same verb and target that the owner read. A surface draws it
// first, labelled [DiffersLabel], and the card may be approved only after
// the block was passed. It comes from another record, so nothing in it is
// under this card's binding digest.
type Differs struct {
	// EarlierID is the id of the earlier card.
	EarlierID string
	// EarlierSHA256 is the digest of the earlier record's canonical bytes.
	// It is part of [Card.ShownDigest].
	EarlierSHA256 string
	// ReadAt is when the owner read the earlier card.
	ReadAt time.Time
	// Feature, Target and Merge are the feature's tip, the target's tip
	// and the merge commit, then and now. All three are empty for a prune,
	// which has no merge.
	Feature Change
	Target  Change
	Merge   Change
	// PathsAdded are the paths in this card's summary that the earlier
	// summary did not have, and PathsRemoved those the earlier one had and
	// this one has not, each in its summary's order.
	PathsAdded   []LeadText
	PathsRemoved []LeadText
	// PathsPartial says that one of the two summaries holds only the first
	// part of its path list, so the two lists above may miss paths.
	PathsPartial bool
	// RefsAdded and RefsRemoved are the refs this card would move or
	// delete and the earlier one would not, and the other way round. They
	// matter for a prune; a feature finish has one ref, the same in both.
	RefsAdded   []LeadText
	RefsRemoved []LeadText
}

// Change is one object id as an earlier card had it and as this card has
// it.
type Change struct {
	Old string
	New string
}

// Changed reports whether the two ids differ.
func (c Change) Changed() bool {
	return c.Old != c.New
}

// DiffersFrom builds the "differs from" block of the record now against the
// record earlier, which the owner read at readAt: the feature's tip, the
// target's tip and the merge then and now, and the paths added to and
// removed from the summary.
//
// The earlier record is checked by [record.EncodePending], the encoder that
// makes every check of [record.DecodePending]: its form, its digests and its
// size. New's own further checks (the feature name first, and the host
// strings that would need escaping) are not made on it, since nothing of
// the earlier record is drawn except its id, which has the grammar of the
// encoder's check. It must be another record for the same project, verb and target as now: the
// same argv, and for a feature finish the same ref. Anything else is refused
// record-invalid, since such a pair is no "differs from". Whether the owner
// read the earlier card, and how long ago, is for the caller to know. The
// record now is not checked here; [New] checks it.
func DiffersFrom(earlier, now record.Pending, readAt time.Time) (Differs, error) {
	data, err := record.EncodePending(earlier)
	if err != nil {
		return Differs{}, err
	}
	eb, nb := &earlier.Binding, &now.Binding
	switch {
	case earlier.ID == now.ID:
		return Differs{}, invalid("the earlier record is the record itself")
	case eb.Project != nb.Project:
		return Differs{}, invalid("the earlier record is for another project")
	case eb.Verb != nb.Verb:
		return Differs{}, invalid("the earlier record is for another verb")
	case !equalStrings(eb.Argv, nb.Argv):
		return Differs{}, invalid("the earlier record is for another target")
	}
	if nb.Verb == record.VerbFeatureFinish &&
		(len(eb.Effects) != 1 || len(nb.Effects) != 1 || eb.Effects[0].Ref != nb.Effects[0].Ref) {
		return Differs{}, invalid("the earlier record is for another target")
	}

	d := Differs{
		EarlierID:     earlier.ID,
		EarlierSHA256: record.Digest(data),
		ReadAt:        readAt,
	}
	if em, nm := eb.Merge, nb.Merge; em != nil && nm != nil {
		d.Merge = Change{Old: em.Commit, New: nm.Commit}
		if len(em.Parents) == 2 && len(nm.Parents) == 2 {
			d.Target = Change{Old: em.Parents[0], New: nm.Parents[0]}
			d.Feature = Change{Old: em.Parents[1], New: nm.Parents[1]}
		}
	}
	var oldPaths, newPaths []string
	if s := eb.Summary; s != nil {
		d.PathsPartial = d.PathsPartial || s.PathsTruncated
		for _, p := range s.Paths {
			oldPaths = append(oldPaths, string(p.Path))
		}
	}
	if s := nb.Summary; s != nil {
		d.PathsPartial = d.PathsPartial || s.PathsTruncated
		for _, p := range s.Paths {
			newPaths = append(newPaths, string(p.Path))
		}
	}
	d.PathsAdded = without(newPaths, oldPaths)
	d.PathsRemoved = without(oldPaths, newPaths)

	var oldRefs, newRefs []string
	for _, e := range eb.Effects {
		oldRefs = append(oldRefs, e.Ref)
	}
	for _, e := range nb.Effects {
		newRefs = append(newRefs, e.Ref)
	}
	d.RefsAdded = without(newRefs, oldRefs)
	d.RefsRemoved = without(oldRefs, newRefs)
	return d, nil
}

// without returns the strings of list that are not in other, in list's
// order and each once, as lead text. Strings are compared by their bytes.
func without(list, other []string) []LeadText {
	skip := make(map[string]bool, len(other))
	for _, s := range other {
		skip[s] = true
	}
	var out []LeadText
	for _, s := range list {
		if !skip[s] {
			skip[s] = true
			out = append(out, NewLeadText(s))
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
