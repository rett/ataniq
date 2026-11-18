//go:build linux && ataniq_canary

package standin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// What the commit is called and where it goes.
const (
	// TokenFile is the file the token is written to, at the top of the
	// working tree.
	TokenFile = "canary-token.txt"
	// Branch is the branch the commit is made on.
	Branch = "canary/one"
	// Subject is the commit's subject. The commit has no body.
	Subject = "canary: token"

	gitTimeout   = 30 * time.Second
	maxGitOutput = 4096
)

// GitPath is the git executable. It is a variable so that a test can replace
// it; nothing else does.
var GitPath = "/usr/bin/git"

// gitEnv is the whole environment of every git process: nothing is inherited.
// HOME is passed on, when there is one, so that git finds the identity that
// provisioning configured; the system configuration is not read.
func gitEnv(home string) []string {
	env := []string{
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	}
	if home != "" {
		env = append(env, "HOME="+home)
	}
	return env
}

// gitConfig is passed with -c to every git call: no hook of the tree runs, no
// monitor command is started, no commit is signed.
var gitConfig = []string{
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "commit.gpgsign=false",
}

// git runs GitPath in dir with the fixed configuration, the explicit
// environment and no input. It returns what git wrote to standard output.
func git(dir, home string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, GitPath, append(append([]string(nil), gitConfig...), args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(home)
	var out, errOut limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

// limitedBuffer keeps the first maxGitOutput bytes written to it and drops
// the rest.
type limitedBuffer struct{ b bytes.Buffer }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxGitOutput - l.b.Len(); room > 0 {
		l.b.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return l.b.String() }

// stageError is a failure of one stage of Commit. Reason is a fixed text
// that holds nothing of the message, the tree or git's own output, so that it
// can be written to a file; the error's own text may.
type stageError struct {
	reason string
	err    error
}

func (e *stageError) Error() string { return e.err.Error() }
func (e *stageError) Unwrap() error { return e.err }

// The fixed reasons of a failed commit, which is all result.txt says of it.
const (
	reasonToken   = "not a valid token"
	reasonTree    = "no working tree"
	reasonBranch  = "branch"
	reasonFile    = "token file"
	reasonAdd     = "git add"
	reasonCommit  = "git commit"
	reasonUnknown = "commit"
)

// failReason returns the fixed reason of a Commit error.
func failReason(err error) string {
	var se *stageError
	if errors.As(err, &se) {
		return se.reason
	}
	return reasonUnknown
}

func stage(reason string, err error) error { return &stageError{reason, err} }

// Commit writes token and one LF to TokenFile at the top of the working tree
// that holds dir, and commits the file on Branch with the subject Subject. The
// branch is made from the current commit when it does not exist, and switched
// to when it does. The token is checked against its character set first; no
// other text reaches git or a file. TokenFile must be a regular file or not
// exist: a link, a pipe or any other kind of file is refused, without waiting
// for it. An empty commit is allowed, so a token that is already in the file
// is committed again. home is the HOME of git's environment.
func Commit(dir, home, token string) error {
	_, err := commitTree(dir, home, token)
	return err
}

// commitTree is Commit; it also returns the short name of the new commit.
func commitTree(dir, home, token string) (string, error) {
	if !ValidToken(token) {
		return "", stage(reasonToken, errors.New("standin: not a valid token"))
	}
	top, err := git(dir, home, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", stage(reasonTree, err)
	}
	top = strings.TrimRight(top, "\n")
	if !filepath.IsAbs(top) {
		return "", stage(reasonTree, fmt.Errorf("standin: git names the top of the tree as %q", top))
	}
	if _, err := git(top, home, "rev-parse", "--verify", "--quiet", "refs/heads/"+Branch); err == nil {
		_, err = git(top, home, "switch", Branch)
		if err != nil {
			return "", stage(reasonBranch, err)
		}
	} else if _, err = git(top, home, "switch", "--create", Branch); err != nil {
		return "", stage(reasonBranch, err)
	}
	if err := writeRegular(filepath.Join(top, TokenFile), token+"\n", 0o644); err != nil {
		return "", stage(reasonFile, err)
	}
	if _, err := git(top, home, "add", "--", TokenFile); err != nil {
		return "", stage(reasonAdd, err)
	}
	if _, err := git(top, home, "commit", "--no-verify", "--allow-empty", "--message", Subject, "--", TokenFile); err != nil {
		return "", stage(reasonCommit, err)
	}
	short, err := git(top, home, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", stage(reasonCommit, err)
	}
	return strings.TrimSpace(short), nil
}

// writeRegular replaces the content of the file at path with s, making it
// when it does not exist. It refuses a link, and any file that is not a
// regular file, such as a pipe, which it does not wait for: the open does not
// block and the file is not truncated before it is known to be regular.
func writeRegular(path, s string, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, perm)
	if err != nil {
		return fmt.Errorf("standin: %w", err)
	}
	st, err := f.Stat()
	if err == nil && !st.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if err == nil {
		err = f.Truncate(0)
	}
	if err == nil {
		_, err = f.WriteString(s)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("standin: %w", err)
	}
	return nil
}
