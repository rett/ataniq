package mergebuild

import (
	"context"
	"errors"
	"fmt"

	"ataniq/internal/approval/record"
)

// PinPrefix is where the pins live. A pin is the ref PinPrefix and a key.
const PinPrefix = "refs/ataniq/pending/"

var (
	// ErrValue means a value was refused before anything was written: an
	// object id, a name, the identity, the time, the key or the
	// repository's description. The error text says which, in the host's
	// words, and never repeats the value.
	ErrValue = errors.New("mergebuild: a value is refused")
	// ErrAttributes means the repository's "info/attributes" is not absent
	// or empty, or the repository's folder could not be looked at, so no
	// merge is computed in it. It wraps the error of
	// [gitenv.CheckInfoAttributes].
	ErrAttributes = errors.New("mergebuild: the repository's attribute file is refused")
	// ErrObjectFormat means the repository's object format is not SHA-1,
	// which [Build] and [Check] do not yet serve: the pinned environment
	// pins the attribute source to the SHA-1 empty tree, and in a SHA-256
	// repository git reads that 40-digit id as a name, so a ref of that
	// name would become the attribute source. [Unpin] needs no merge and
	// works in either format.
	ErrObjectFormat = errors.New("mergebuild: the repository's object format is not served")
	// ErrNoCommit means an id that should name a commit of the repository
	// names another kind of object, or none.
	ErrNoCommit = errors.New("mergebuild: an id does not name a commit of the repository")
	// ErrUnrelated means the two tips share no history, so git has no base
	// to merge them from.
	ErrUnrelated = errors.New("mergebuild: the two tips share no history")
	// ErrPinExists means the key already has a pin. [Build] never
	// overwrites one.
	ErrPinExists = errors.New("mergebuild: the key already has a pin")
	// ErrNoPin means [Unpin] found no pin for the key.
	ErrNoPin = errors.New("mergebuild: the key has no pin")
	// ErrPinMoved means [Unpin] found the pin at another commit than the
	// one it was told, and left it.
	ErrPinMoved = errors.New("mergebuild: the pin holds another commit")
	// ErrPinUnclear means the git run that writes or removes the pin did
	// not end by itself (the time ran out, or the context ended), so the
	// pin may or may not have changed. The caller looks before it goes on.
	ErrPinUnclear = errors.New("mergebuild: the pin may or may not have changed")
	// ErrMismatch means [Check] found that a commit is not the one [Build]
	// writes for the values given. The error text says what differs, in
	// the host's words.
	ErrMismatch = errors.New("mergebuild: the commit is not the host's merge")
)

// Merge is a merge commit the host built.
type Merge struct {
	// Commit is the id of the merge commit.
	Commit string
	// Tree is the id of its tree, the result of merging the two parents.
	Tree string
	// Parents are its two parents in order: the target's tip, then the
	// feature's tip.
	Parents [2]string
}

// PinRef returns the name of the ref that pins the merge built under key.
// It refuses a key outside [ValidKey].
func PinRef(key string) (string, error) {
	if err := checkKey(key); err != nil {
		return "", err
	}
	return pinRef(key), nil
}

// pinRef is [PinRef] for a key that has been checked.
func pinRef(key string) string { return PinPrefix + key }

// prepare does what [Build] and [Check] share before any merge: it checks
// the values, then "info/attributes", then, with git, that the repository's
// object format is SHA-1 ([ErrObjectFormat]) and that every id has its
// length. ids are further object ids
// under that last rule.
func prepare(ctx context.Context, repo Repo, in Inputs, ids ...string) (*call, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	c, err := newCall(repo)
	if err != nil {
		return nil, err
	}
	if err := checkAttributes(repo.GitDir); err != nil {
		return nil, err
	}
	if err := c.format(ctx); err != nil {
		return nil, err
	}
	// Until the pinned environment pins the empty tree of the repository's
	// own format, a merge in a SHA-256 repository could read its attributes
	// from a ref a name-lookup finds (see [ErrObjectFormat]), so none is
	// computed there.
	if c.idLen != 40 {
		return nil, ErrObjectFormat
	}
	for _, id := range append([]string{in.TargetTip, in.FeatureTip}, ids...) {
		if !c.isID(id) {
			return nil, refuse("an object id does not have the length of the repository's object format")
		}
	}
	return c, nil
}

// Build merges the feature's tip into the target's tip in the bare
// repository, writes the merge commit and pins it under key.
//
// In order: every value of in and the key are checked ([ErrValue]); the
// repository's "info/attributes" must be absent or empty ([ErrAttributes]);
// the repository must use SHA-1 ([ErrObjectFormat]); the two tips must be full ids of the repository's object format and name
// commits ([ErrValue], [ErrNoCommit]); the key must have no pin yet
// ([ErrPinExists]); the tips must share history ([ErrUnrelated]). Then git
// merges the two commits, reading no attribute from any tree. A conflict is
// refused with a [*record.Error] whose code is [record.CodeMergeConflict],
// and then no commit and no ref is written (git leaves the trees and blobs
// of its attempt in the object store, where nothing names them). Otherwise
// Build writes the commit from a text it wrote in full (see the package
// comment), checks that git stored exactly that text, and creates the ref
// [PinPrefix]+key at the commit. It never moves an existing ref.
//
// The feature's tip is merged as it is, whatever it is: when it is a merge
// commit a lead made, that commit is ordinary history under the second
// parent, and the merge commit is still the host's own.
//
// The same values give the same commit id. When the run that creates the
// pin does not end by itself, the error wraps [ErrPinUnclear].
func Build(ctx context.Context, repo Repo, in Inputs, key string) (Merge, error) {
	if err := checkKey(key); err != nil {
		return Merge{}, err
	}
	c, err := prepare(ctx, repo, in)
	if err != nil {
		return Merge{}, err
	}
	if err := c.commits(ctx, []string{in.TargetTip, in.FeatureTip}, []string{"the target's tip", "the feature's tip"}); err != nil {
		return Merge{}, err
	}
	// Looked at before anything is written, so that a key in use costs no
	// object. The creation below is what holds against a second writer.
	if at, err := c.pinned(ctx, key); err != nil {
		return Merge{}, err
	} else if at != "" {
		return Merge{}, ErrPinExists
	}

	if err := c.related(ctx, in.TargetTip, in.FeatureTip); err != nil {
		return Merge{}, err
	}
	tree, clean, err := c.mergeTree(ctx, in.TargetTip, in.FeatureTip)
	if err != nil {
		return Merge{}, err
	}
	if !clean {
		return Merge{}, &record.Error{Code: record.CodeMergeConflict, Detail: "the feature does not merge into the target without a conflict"}
	}

	text := commitText(tree, in)
	out, err := c.git(ctx, []string{"hash-object", "-tcommit", "-w", "--stdin"}, text, smallOutput, nil)
	if err != nil {
		return Merge{}, err
	}
	commit := commitID(c.idLen, text)
	if string(out) != commit+"\n" {
		// The id is the digest of the text, so an equal id means git
		// stored these bytes and no others.
		return Merge{}, errors.New("mergebuild: git hash-object did not print the id of the text it was given")
	}

	if err := c.updateRef(ctx, "create "+pinRef(key)+" "+commit); err != nil {
		var exit *gitExit
		if !errors.As(err, &exit) {
			return Merge{}, fmt.Errorf("%w: %w", ErrPinUnclear, err)
		}
		// git refused, so it wrote nothing. Say so plainly when the reason
		// is a pin that another writer made in the meantime.
		if at, lookErr := c.pinned(ctx, key); lookErr == nil && at != "" {
			return Merge{}, ErrPinExists
		}
		return Merge{}, err
	}
	return Merge{Commit: commit, Tree: tree, Parents: [2]string{in.TargetTip, in.FeatureTip}}, nil
}

// Unpin removes the pin of key, which must hold commit: git deletes the ref
// only while it still points there. It refuses a key with no pin
// ([ErrNoPin]) and a pin at another commit ([ErrPinMoved]), and changes
// nothing then. The commit itself is not removed; once nothing names it,
// the repository's maintenance may.
//
// When the run that deletes the ref does not end by itself, the error
// wraps [ErrPinUnclear].
func Unpin(ctx context.Context, repo Repo, key, commit string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if !record.ValidObjectID(commit) {
		return refuse("the pinned commit is not a full object id")
	}
	c, err := newCall(repo)
	if err != nil {
		return err
	}
	if err := c.format(ctx); err != nil {
		return err
	}
	if !c.isID(commit) {
		return refuse("an object id does not have the length of the repository's object format")
	}
	at, err := c.pinned(ctx, key)
	if err != nil {
		return err
	}
	if at == "" {
		return ErrNoPin
	}
	if at != commit {
		return ErrPinMoved
	}
	if err := c.updateRef(ctx, "delete "+pinRef(key)+" "+commit); err != nil {
		var exit *gitExit
		if !errors.As(err, &exit) {
			return fmt.Errorf("%w: %w", ErrPinUnclear, err)
		}
		return err
	}
	return nil
}
