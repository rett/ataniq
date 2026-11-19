//go:build linux && ataniq_canary

package provision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
)

// GitPath is the git executable provisioning runs. It is a variable so that a
// test can put a program of its own there; nothing else changes it.
var GitPath = "/usr/bin/git"

// gitTimeout bounds one git run.
const gitTimeout = 2 * time.Minute

// maxGitOutput bounds what is kept of a git run's output.
const maxGitOutput = 1 << 20

// GitEnvironment is the whole environment of every git run: nothing is
// inherited. HOME is a device, so no per-user configuration or credential
// file can be found under it; the system and global configuration are off;
// a terminal prompt is off; PATH holds the system folders only.
func GitEnvironment() []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=/dev/null",
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	}
}

// gitCommand builds one git run in the folder dir (the current folder of the
// child), with the explicit environment and the settings that keep it to a
// local clone: no protocol but file, no credential helper, no hooks path.
// Standard input is the null device.
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := append([]string{
		"-c", "protocol.allow=never",
		"-c", "protocol.file.allow=always",
		"-c", "credential.helper=",
		"-c", "core.hooksPath=/dev/null",
	}, args...)
	cmd := exec.CommandContext(ctx, GitPath, full...)
	cmd.Dir = dir
	cmd.Env = GitEnvironment()
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// limitedBuffer keeps the first maxGitOutput bytes written to it.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxGitOutput - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// runGit runs git in dir and returns its standard output. A failure carries
// the first line of git's own message.
func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := gitCommand(ctx, dir, args...)
	var out, errOut limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return out.Bytes(), nil
}

// fdDir is a path that names the folder behind a handle, for the working
// folder of a child: the child changes into the folder the handle names, not
// into whatever a name holds at that moment.
func fdDir(h *safefile.Handle) (string, error) {
	fd, err := h.Fd()
	if err != nil {
		return "", err
	}
	return "/proc/" + strconv.Itoa(os.Getpid()) + "/fd/" + strconv.Itoa(fd), nil
}

// cloneInto clones the mirror's branch into the folder behind the handle,
// which must be empty. The clone copies the objects (no hard link to the
// mirror's files, which the sandbox binds), takes no template (so no hook and
// no sample), and names the mirror "origin". Afterwards the remote's URL is
// the mirror's place inside the sandbox, and the configuration is checked
// against an allow-list: see checkConfig.
func cloneInto(ctx context.Context, tree *safefile.Handle, mirrorPath, branch, insideURL string) error {
	dir, err := fdDir(tree)
	if err != nil {
		return err
	}
	if _, err := runGit(ctx, dir, "clone", "--quiet", "--no-hardlinks", "--template=",
		"--origin=origin", "--branch="+branch, "--", mirrorPath, "."); err != nil {
		return err
	}
	if _, err := runGit(ctx, dir, "remote", "set-url", "origin", insideURL); err != nil {
		return err
	}
	out, err := runGit(ctx, dir, "config", "--local", "--list", "-z")
	if err != nil {
		return err
	}
	return checkConfig(out, insideURL)
}

// coreKeys are the core settings a clone writes and nothing else.
var coreKeys = map[string]bool{
	"core.repositoryformatversion": true,
	"core.filemode":                true,
	"core.bare":                    true,
	"core.logallrefupdates":        true,
	"core.symlinks":                true,
	"core.ignorecase":              true,
	"core.precomposeunicode":       true,
}

// ErrBadCloneConfig is wrapped when the clone's own configuration holds
// anything but what a plain clone of the mirror writes.
var ErrBadCloneConfig = errors.New("provision: the clone's configuration holds an entry that provisioning did not expect")

// checkConfig takes the output of "git config --local --list -z" and accepts
// only what a plain clone of the mirror writes: a few core settings, the one
// remote named origin with the mirror's URL and the default fetch refspec,
// and the tracking entries of branches. Anything else (a credential helper, a
// URL rewrite, an include, a push URL, an extra header, a second remote) is
// refused, so that no credential or credential helper is anywhere in the
// configuration the lead's sandbox will read. The error never holds a value.
func checkConfig(listing []byte, insideURL string) error {
	var sawURL bool
	for _, rec := range bytes.Split(bytes.TrimSuffix(listing, []byte{0}), []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		key, value, _ := strings.Cut(string(rec), "\n")
		switch {
		case coreKeys[key]:
		case key == "remote.origin.url":
			if value != insideURL {
				return fmt.Errorf("%w: remote.origin.url", ErrBadCloneConfig)
			}
			sawURL = true
		case key == "remote.origin.fetch":
			if value != "+refs/heads/*:refs/remotes/origin/*" {
				return fmt.Errorf("%w: remote.origin.fetch", ErrBadCloneConfig)
			}
		case strings.HasPrefix(key, "branch.") && strings.HasSuffix(key, ".remote"):
			if value != "origin" {
				return fmt.Errorf("%w: a branch's remote", ErrBadCloneConfig)
			}
		case strings.HasPrefix(key, "branch.") && strings.HasSuffix(key, ".merge"):
			if !strings.HasPrefix(value, "refs/heads/") {
				return fmt.Errorf("%w: a branch's merge ref", ErrBadCloneConfig)
			}
		default:
			return fmt.Errorf("%w: section %s", ErrBadCloneConfig, quoteSection(key))
		}
	}
	if !sawURL {
		return fmt.Errorf("%w: no remote.origin.url", ErrBadCloneConfig)
	}
	return nil
}

// quoteSection makes the section of a configuration key safe to print: the
// text before the first dot, cut short. A key can carry a URL in its middle
// (credential.<url>.helper), and a URL can carry a secret, so no more of the
// key is printed.
func quoteSection(key string) string {
	section, _, _ := strings.Cut(key, ".")
	if len(section) > 40 {
		section = section[:40]
	}
	return strconv.Quote(section)
}

// insideMirror is where the sandbox sees the mirror.
func insideMirror(project string) string { return profile.MirrorInside(project) }

// identityConfig is the content of the git identity file: the name and the
// address from the config, as quoted values, so that no character of either
// can end the value or start a section. The config has refused control
// characters already.
func identityConfig(name, email string) []byte {
	return []byte("[user]\n\tname = " + gitQuote(name) + "\n\temail = " + gitQuote(email) + "\n")
}

// gitQuote writes s as a double-quoted git configuration value.
func gitQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
