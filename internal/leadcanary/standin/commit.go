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

// Commit writes token and one LF to TokenFile at the top of the working tree
// that holds dir, and commits the file on Branch with the subject Subject. The
// branch is made from the current commit when it does not exist, and switched
// to when it does. The token is checked against its character set first; no
// other text reaches git or a file. An empty commit is allowed, so a token
// that is already in the file is committed again. home is the HOME of git's
// environment.
func Commit(dir, home, token string) error {
	if !ValidToken(token) {
		return errors.New("standin: not a valid token")
	}
	top, err := git(dir, home, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	top = strings.TrimRight(top, "\n")
	if !filepath.IsAbs(top) {
		return fmt.Errorf("standin: git names the top of the tree as %q", top)
	}
	if _, err := git(top, home, "rev-parse", "--verify", "--quiet", "refs/heads/"+Branch); err == nil {
		_, err = git(top, home, "switch", Branch)
		if err != nil {
			return err
		}
	} else if _, err = git(top, home, "switch", "--create", Branch); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(top, TokenFile), os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return fmt.Errorf("standin: %w", err)
	}
	_, werr := f.WriteString(token + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("standin: %w", werr)
	}
	if _, err := git(top, home, "add", "--", TokenFile); err != nil {
		return err
	}
	_, err = git(top, home, "commit", "--no-verify", "--allow-empty", "--message", Subject, "--", TokenFile)
	return err
}
