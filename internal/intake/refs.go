package intake

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/leadtext"
)

// maxRefName is the longest ref name accepted.
const maxRefName = 1024

// checkRefName refuses a name that is not a full ref name under prefix. The
// rules are those of git check-ref-format, written strictly: the name must
// also hold no byte that for-each-ref would read as a pattern. It never
// searches for a short name.
func checkRefName(name, prefix string) error {
	bad := func(why string) error {
		return fmt.Errorf("%w: %s: %s", ErrRefName, why, leadtext.Escape(name))
	}
	if !strings.HasPrefix(name, prefix) {
		return bad("it does not start with " + prefix)
	}
	if len(name) == len(prefix) {
		return bad("it names no branch")
	}
	if len(name) > maxRefName {
		return bad("it is too long")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c == 0x7f || strings.IndexByte(" ~^:?*[\\", c) >= 0 {
			return bad("it holds a byte that is not allowed")
		}
	}
	if strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.Contains(name, "//") {
		return bad("it holds a sequence that is not allowed")
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") {
		return bad("it ends in a character that is not allowed")
	}
	for _, e := range strings.Split(name, "/") {
		if strings.HasPrefix(e, ".") || strings.HasSuffix(e, ".lock") {
			return bad("an element starts with a dot or ends in .lock")
		}
	}
	return nil
}

// resolve reads the two refs by their exact names in one for-each-ref run,
// so both ids come from one listing, and checks that each names a commit.
// A pattern of for-each-ref matches a name and what lies below it; only the
// line whose name is exactly the one asked for counts.
func (r *runner) resolve(ctx context.Context, baseRef, tipRef string) (base, tip string, err error) {
	out, err := r.git(ctx, []string{
		"for-each-ref",
		"--format=%(refname) %(objectname) %(objecttype) %(symref)",
		"--end-of-options", baseRef, tipRef,
	}, nil, smallOutput, nil)
	if err != nil {
		return "", "", err
	}
	type found struct{ id, typ string }
	got := map[string]found{}
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, " ")
		if len(f) != 4 || !gitenv.IsObjectID(f[1]) {
			return "", "", errors.New("intake: unexpected output of git for-each-ref")
		}
		if f[3] != "" {
			return "", "", fmt.Errorf("%w: a symbolic ref is refused: %s", ErrNotCommit, leadtext.Escape(f[0]))
		}
		if _, dup := got[f[0]]; dup {
			return "", "", errors.New("intake: unexpected output of git for-each-ref")
		}
		got[f[0]] = found{id: f[1], typ: f[2]}
	}
	ids := [2]string{}
	for i, name := range []string{baseRef, tipRef} {
		f, ok := got[name]
		if !ok {
			return "", "", fmt.Errorf("%w: %s", ErrNoRef, leadtext.Escape(name))
		}
		if f.typ != "commit" {
			return "", "", fmt.Errorf("%w: %s is a %s", ErrNotCommit, leadtext.Escape(name), leadtext.Escape(f.typ))
		}
		if ids[i], err = r.verifyCommit(ctx, f.id); err != nil {
			return "", "", err
		}
	}
	return ids[0], ids[1], nil
}

// verifyCommit checks with rev-parse that id names a readable commit and
// returns the full id git prints.
func (r *runner) verifyCommit(ctx context.Context, id string) (string, error) {
	out, err := r.git(ctx, []string{"rev-parse", "--verify", "--end-of-options", id + "^{commit}"}, nil, smallOutput, nil)
	if err != nil {
		return "", err
	}
	got := strings.TrimSuffix(string(out), "\n")
	if got != id {
		return "", errors.New("intake: unexpected output of git rev-parse")
	}
	return got, nil
}

// requireAncestor returns a *NotFastForwardError unless base is an ancestor
// of tip.
func (r *runner) requireAncestor(ctx context.Context, base, tip string) error {
	_, err := r.git(ctx, []string{"merge-base", "--is-ancestor", "--end-of-options", base, tip}, nil, smallOutput, nil)
	if err == nil {
		return nil
	}
	var ge *gitExit
	if !errors.As(err, &ge) || ge.exit != 1 {
		return err
	}
	nf := &NotFastForwardError{Base: base, Tip: tip}
	out, err := r.git(ctx, []string{"merge-base", "--end-of-options", base, tip}, nil, smallOutput, nil)
	switch {
	case err == nil:
		nf.MergeBase = strings.TrimSuffix(string(out), "\n")
		if !gitenv.IsObjectID(nf.MergeBase) {
			return errors.New("intake: unexpected output of git merge-base")
		}
	case errors.As(err, &ge) && ge.exit == 1:
		// No common ancestor.
	default:
		return err
	}
	return nf
}

// count runs rev-list --count for the range and returns the number.
func (r *runner) count(ctx context.Context, base, tip string) (int, error) {
	out, err := r.git(ctx, []string{"rev-list", "--count", "--end-of-options", base + ".." + tip}, nil, smallOutput, nil)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSuffix(string(out), "\n"))
	if err != nil || n < 0 {
		return 0, errors.New("intake: unexpected output of git rev-list")
	}
	return n, nil
}
