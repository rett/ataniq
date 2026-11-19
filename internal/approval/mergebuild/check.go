package mergebuild

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"ataniq/internal/approval/record"
)

// differs returns the refusal of [Check]. The text is the host's: it says
// what differs and never repeats a byte of the commit, which a lead may
// have written.
func differs(what string) error {
	return fmt.Errorf("%w: %s", ErrMismatch, what)
}

// Check recomputes a merge commit and refuses one that [Build] would not
// write for the same values. commit is the id to check; in holds the values,
// of which TargetTip and FeatureTip are the parents expected, in that order.
//
// It returns nil only when all of this holds, and an error that wraps
// [ErrMismatch] otherwise:
//
//   - the object is a commit with exactly two parents, the target's tip
//     first and the feature's tip second: a third parent, one parent and
//     swapped parents are refused;
//   - its tree is the tree git gives now for merging those two parents, with
//     no attribute read from any tree, and that merge has no conflict;
//   - its whole text, every header and the message, is byte for byte the
//     text Build writes for these values and that tree: another author,
//     committer, date or message, a further header and a further line are
//     refused.
//
// The values are checked first, as Build checks them ([ErrValue],
// [ErrAttributes], [ErrObjectFormat], [ErrNoCommit] for a tip that names
// no commit, [ErrUnrelated], [ErrNothingToMerge] for a target that already
// holds the feature). Check reads and computes; it writes no commit and no
// ref, and it does not look at the pin (git leaves the tree of the merge it
// recomputes in the object store, which for a commit that passes is the tree
// the commit names).
func Check(ctx context.Context, repo Repo, commit string, in Inputs) error {
	if !record.ValidObjectID(commit) {
		return refuse("the commit to check is not a full object id")
	}
	c, err := prepare(ctx, repo, in, commit)
	if err != nil {
		return err
	}
	found, err := c.objects(ctx, in.TargetTip, in.FeatureTip, commit)
	if err != nil {
		return err
	}
	if found[0].kind != "commit" {
		return fmt.Errorf("%w: the target's tip", ErrNoCommit)
	}
	if found[1].kind != "commit" {
		return fmt.Errorf("%w: the feature's tip", ErrNoCommit)
	}
	if found[2].kind != "commit" {
		return differs("the object is not a commit of the repository")
	}
	// The text Build writes has one length for these values, whatever the
	// tree is, since every tree id of a repository is as long as the next.
	want := len(commitText(c.zeroID(), in))
	if found[2].size > int64(want)+maxExtraRead {
		return differs("the commit is longer than any the host writes for these values")
	}

	raw, err := c.rawCommit(ctx, commit, found[2].size)
	if err != nil {
		return err
	}
	tree, parents := commitHead(raw)
	switch {
	case len(parents) != 2:
		return differs("the commit has " + strconv.Itoa(len(parents)) + " parents, not two")
	case parents[0] != in.TargetTip:
		return differs("the first parent is not the target's tip")
	case parents[1] != in.FeatureTip:
		return differs("the second parent is not the feature's tip")
	}

	if err := c.related(ctx, in.TargetTip, in.FeatureTip); err != nil {
		return err
	}
	if held, err := c.holds(ctx, in.TargetTip, in.FeatureTip); err != nil {
		return err
	} else if held {
		return ErrNothingToMerge
	}
	merged, clean, err := c.mergeTree(ctx, in.TargetTip, in.FeatureTip)
	if err != nil {
		return err
	}
	if !clean {
		return differs("the two parents do not merge without a conflict")
	}
	if tree != merged {
		return differs("the tree is not the tree of merging the two parents")
	}
	if !bytes.Equal(raw, commitText(merged, in)) {
		return differs("the headers and the message are not the text the host writes for these values")
	}
	return nil
}

// maxExtraRead is how many bytes longer than the host's own text a commit
// may be and still be read, so that [Check] can say which part differs.
const maxExtraRead = 4 << 10

// rawCommit returns the whole text of a commit, which has size bytes, as
// the object store holds it.
func (c *call) rawCommit(ctx context.Context, id string, size int64) ([]byte, error) {
	out, err := c.git(ctx, []string{"cat-file", "--batch"}, []byte(id+"\n"), size+smallOutput, nil)
	if err != nil {
		return nil, err
	}
	// The answer is one line with the id, the type and the size, then the
	// text, then a newline.
	header := id + " commit " + strconv.FormatInt(size, 10) + "\n"
	if !strings.HasPrefix(string(out), header) || int64(len(out)) != int64(len(header))+size+1 || out[len(out)-1] != '\n' {
		return nil, fmt.Errorf("mergebuild: git cat-file printed an answer this package does not know")
	}
	return out[len(header) : len(out)-1], nil
}

// commitHead reads the first lines of a commit's text as git does: one
// "tree" line, then the "parent" lines that follow it directly. It returns
// what stands after each word, unchecked; tree is "" when the text does not
// start with a tree line.
func commitHead(raw []byte) (tree string, parents []string) {
	lines := strings.Split(string(raw), "\n")
	rest, ok := strings.CutPrefix(lines[0], "tree ")
	if !ok {
		return "", nil
	}
	for _, line := range lines[1:] {
		parent, ok := strings.CutPrefix(line, "parent ")
		if !ok {
			break
		}
		parents = append(parents, parent)
	}
	return rest, parents
}
