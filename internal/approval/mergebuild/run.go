package mergebuild

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
)

const (
	// DefaultTimeout is the time one call of this package may take, all its
	// git runs together, when [Repo.Timeout] is zero.
	DefaultTimeout = 60 * time.Second

	// maxStderr is how much standard error of git is read. It is dropped:
	// no error of this package holds it, since it can name lead data.
	maxStderr = 64 << 10
	// smallOutput is the output cap of a git run that prints a few lines.
	smallOutput = 4 << 10
	// maxMergeOutput is the output cap of "merge-tree". A clean merge prints
	// one object id. A conflict also prints the name of every conflicting
	// path, of which only the exit status is used; the cap is wide so that a
	// conflict over very many paths is still reported as a conflict.
	maxMergeOutput = 256 << 20
)

// Repo names the bare repository a call works in, and the git that runs.
type Repo struct {
	// Git is the git executable, from [gitenv.New] or [gitenv.Find].
	Git gitenv.Git
	// GitDir is the absolute path of the bare repository. It is one that
	// only the host can write: this package relies on that, as
	// [gitenv.CheckInfoAttributes] does.
	GitDir string
	// Timeout is the time one call ([Build], [Check], [Unpin]) may take,
	// all its git runs together. Zero means [DefaultTimeout]; a negative
	// value is refused. Each git run gets the time that is left, and the
	// runner may add its fixed two-second wait for the pipes to a run.
	Timeout time.Duration
}

// gitExit is the error of a git run that ended with a non-zero status. It
// carries the subcommand and the status, and nothing git wrote.
type gitExit struct {
	sub  string
	exit int
}

func (e *gitExit) Error() string {
	return fmt.Sprintf("mergebuild: git %s failed with status %d", e.sub, e.exit)
}

// call is one call of the package: the repository, the length of its object
// ids, and the one deadline all git runs of the call share.
type call struct {
	repo     Repo
	deadline time.Time
	// idLen is the number of hexadecimal digits of an object id in this
	// repository: 40 (SHA-1) or 64 (SHA-256). It is set by [call.format].
	idLen int
}

// newCall checks the repository's description and starts the clock.
func newCall(repo Repo) (*call, error) {
	if repo.GitDir == "" || strings.ContainsRune(repo.GitDir, 0) || !filepath.IsAbs(repo.GitDir) {
		return nil, fmt.Errorf("%w: the repository is not named by an absolute path", ErrValue)
	}
	if repo.Timeout < 0 {
		return nil, fmt.Errorf("%w: the time limit is negative", ErrValue)
	}
	d := repo.Timeout
	if d == 0 {
		d = DefaultTimeout
	}
	return &call{repo: repo, deadline: time.Now().Add(d)}, nil
}

// git runs one git command through gitenv and the bounded runner, with the
// time that is left of the call. args is the subcommand and its arguments.
// The standard output is returned, up to maxOut bytes, unless sink is set,
// which then receives it. A non-zero exit is a [*gitExit].
func (c *call) git(ctx context.Context, args []string, stdin []byte, maxOut int64, sink io.Writer) ([]byte, error) {
	cmd := gitenv.Cmd{GitDir: c.repo.GitDir, Args: args}
	argv, err := c.repo.Git.Argv(cmd)
	if err != nil {
		return nil, fmt.Errorf("mergebuild: %w", err)
	}
	left := time.Until(c.deadline)
	if left <= 0 {
		return nil, fmt.Errorf("mergebuild: git %s: %w", args[0], boundedexec.ErrTimeout)
	}
	run := boundedexec.Cmd{
		Argv:      argv,
		Env:       c.repo.Git.Env(cmd),
		Stdout:    sink,
		Timeout:   left,
		MaxStdout: maxOut,
		MaxStderr: maxStderr,
	}
	if stdin != nil {
		// A reader over bytes never blocks, so the runner's copying
		// goroutine ends with the run.
		run.Stdin = bytes.NewReader(stdin)
	}
	res, err := boundedexec.Run(ctx, run)
	if err != nil {
		var exit *boundedexec.ExitError
		if errors.As(err, &exit) {
			return nil, &gitExit{sub: args[0], exit: exit.Exit}
		}
		return nil, fmt.Errorf("mergebuild: git %s: %w", args[0], err)
	}
	return res.Stdout, nil
}

// format asks the repository for its object format, once per call, and sets
// c.idLen. The form of the question is the one the pinned environment
// accepts: the option with its value joined by "=".
func (c *call) format(ctx context.Context) error {
	out, err := c.git(ctx, []string{"rev-parse", "--show-object-format=storage"}, nil, smallOutput, nil)
	if err != nil {
		return err
	}
	switch string(out) {
	case "sha1\n":
		c.idLen = 40
	case "sha256\n":
		c.idLen = 64
	default:
		return errors.New("mergebuild: git names an object format that is neither sha1 nor sha256")
	}
	return nil
}

// isID reports whether s is a full object id of this repository: lower-case
// hexadecimal digits of the length of its object format. An id of the other
// length is refused, because git would read it as the name of a ref.
func (c *call) isID(s string) bool {
	return len(s) == c.idLen && gitenv.IsObjectID(s)
}

// zeroID is the id of no object, which "update-ref" reads as "the ref does
// not exist".
func (c *call) zeroID() string { return strings.Repeat("0", c.idLen) }

// object is what "cat-file --batch-check" says about one id.
type object struct {
	// kind is "commit", "tree", "blob" or "tag", and "" when the repository
	// has no such object.
	kind string
	size int64
}

// objects asks for the type and the size of each id, in one run. Every id
// has passed [call.isID], so git reads none of them as a name.
func (c *call) objects(ctx context.Context, ids ...string) ([]object, error) {
	out, err := c.git(ctx, []string{"cat-file", "--batch-check"}, []byte(strings.Join(ids, "\n")+"\n"), smallOutput, nil)
	if err != nil {
		return nil, err
	}
	bad := errors.New("mergebuild: git cat-file printed a listing this package does not know")
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(ids) {
		return nil, bad
	}
	found := make([]object, len(ids))
	for i, line := range lines {
		rest, ok := strings.CutPrefix(line, ids[i]+" ")
		if !ok {
			return nil, bad
		}
		if rest == "missing" {
			continue
		}
		var o object
		if n, err := fmt.Sscanf(rest, "%s %d", &o.kind, &o.size); err != nil || n != 2 || o.size < 0 {
			return nil, bad
		}
		switch o.kind {
		case "commit", "tree", "blob", "tag":
		default:
			return nil, bad
		}
		found[i] = o
	}
	return found, nil
}

// commits refuses unless every id names a commit of the repository. what
// says, in the host's words, which value each id is.
func (c *call) commits(ctx context.Context, ids []string, what []string) error {
	found, err := c.objects(ctx, ids...)
	if err != nil {
		return err
	}
	for i, o := range found {
		if o.kind != "commit" {
			return fmt.Errorf("%w: %s", ErrNoCommit, what[i])
		}
	}
	return nil
}

// head keeps the first bytes of a stream and drops the rest.
type head struct {
	buf  []byte
	max  int
	more bool
}

func (h *head) Write(p []byte) (int, error) {
	room := h.max - len(h.buf)
	if len(p) > room {
		h.buf = append(h.buf, p[:room]...)
		h.more = true
	} else {
		h.buf = append(h.buf, p...)
	}
	return len(p), nil
}

// mergeTree merges the two commits with "merge-tree --write-tree", in the
// one form gitenv allows, and returns the id of the merged tree. clean is
// false when the merge has a conflict; the tree is then not to be used.
//
// It checks "info/attributes" itself, right before git runs, because that
// file is the one attribute source the pinned environment cannot switch
// off.
func (c *call) mergeTree(ctx context.Context, ours, theirs string) (tree string, clean bool, err error) {
	if err := checkAttributes(c.repo.GitDir); err != nil {
		return "", false, err
	}
	// The tree's id and the NUL after it; anything further is the list of
	// conflicting paths, which is lead data and is dropped unread.
	out := &head{max: c.idLen + 1}
	_, err = c.git(ctx, []string{
		"merge-tree", "--write-tree", "--name-only", "--no-messages", "-z",
		"--end-of-options", ours, theirs,
	}, nil, maxMergeOutput, out)
	var exit *gitExit
	conflict := errors.As(err, &exit) && exit.exit == 1
	if err != nil && !conflict {
		return "", false, err
	}
	if len(out.buf) != c.idLen+1 || out.buf[c.idLen] != 0 || !c.isID(string(out.buf[:c.idLen])) {
		return "", false, errors.New("mergebuild: git merge-tree did not print the id of a tree first")
	}
	if conflict {
		return "", false, nil
	}
	if out.more {
		return "", false, errors.New("mergebuild: git merge-tree printed more than the id of a tree for a clean merge")
	}
	return string(out.buf[:c.idLen]), true, nil
}

// related refuses two commits that share no history, which git would not
// merge: "merge-base" finds no commit then and ends with status 1.
func (c *call) related(ctx context.Context, ours, theirs string) error {
	_, err := c.git(ctx, []string{"merge-base", ours, theirs}, nil, smallOutput, nil)
	var exit *gitExit
	if errors.As(err, &exit) && exit.exit == 1 {
		return ErrUnrelated
	}
	return err
}

// holds reports whether the commit ours has the commit theirs among its
// ancestors, or is it: "merge-base --is-ancestor" ends with status 0 then
// and with status 1 when it is not.
func (c *call) holds(ctx context.Context, ours, theirs string) (bool, error) {
	_, err := c.git(ctx, []string{"merge-base", "--is-ancestor", theirs, ours}, nil, smallOutput, nil)
	var exit *gitExit
	if errors.As(err, &exit) && exit.exit == 1 {
		return false, nil
	}
	return err == nil, err
}

// checkAttributes is [gitenv.CheckInfoAttributes] with this package's error.
func checkAttributes(gitDir string) error {
	if err := gitenv.CheckInfoAttributes(gitDir); err != nil {
		return fmt.Errorf("%w: %w", ErrAttributes, err)
	}
	return nil
}

// pinned returns the id the pin of key holds, or "" when there is no such
// ref.
func (c *call) pinned(ctx context.Context, key string) (string, error) {
	out, err := c.git(ctx, []string{"for-each-ref", "--format=%(objectname)", "--end-of-options", pinRef(key)}, nil, smallOutput, nil)
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", nil
	}
	id := strings.TrimSuffix(string(out), "\n")
	if !c.isID(id) {
		return "", errors.New("mergebuild: git for-each-ref printed something other than one object id")
	}
	return id, nil
}

// updateRef hands one instruction to "update-ref --stdin". The ref is not
// followed when it is a symbolic one.
func (c *call) updateRef(ctx context.Context, instruction string) error {
	_, err := c.git(ctx, []string{"update-ref", "--no-deref", "--stdin"}, []byte(instruction+"\n"), smallOutput, nil)
	return err
}
