//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"ataniq/internal/approval/mergebuild"
	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/intake"
	"ataniq/internal/intake/execconfig"
	"ataniq/internal/intake/verify"
)

// Project is what the host knows of one project: its mirror, the lead's role
// whose branches live there, the one branch a feature finish moves, the rules
// that flag a path, the identity of the merge commit and the gates of the
// suite.
type Project struct {
	// Name is the project's name, as the service is told it with each
	// collected request: [sandboxplan.ValidName].
	Name string
	// Mirror is the absolute, clean path of the bare mirror. Only the host
	// can write it: the lead's sandbox reads it and cannot change it.
	Mirror string
	// Role is the lead's role. The feature's branch is
	// refs/leads/<role>/heads/feature/<name> in the mirror.
	Role string
	// Target is the full name of the branch a feature finish moves, such
	// as refs/heads/develop.
	Target string
	// Rules is the execution-config set the review flags paths with. The
	// zero Set matches nothing; [execconfig.Default] is the built-in one.
	Rules execconfig.Set
	// Identity is the author and committer of the merge commit.
	Identity mergebuild.Identity
	// Gates are the gates of the suite, in order. At least one. They are
	// named in the verification record; the suite runs them.
	Gates []verify.Gate
}

// Defaults of the adapter's limits.
const (
	// DefaultGitTimeout is the time of one git call of the adapter.
	DefaultGitTimeout = 60 * time.Second
	// maxSummaryPathBudget is how many bytes of the pending record the
	// path list of a summary may use, as the adapter estimates them. A
	// longer list is cut and marked, and the card reads the rest in pages.
	maxSummaryPathBudget = 32 << 10
	// maxListBudget is how many bytes the four lists and the flagged list
	// may use together. They are never cut: a change with more is refused
	// record-too-large.
	maxListBudget = 24 << 10
)

// check reports why p cannot be used, in words that name no value.
func (p Project) check() error {
	switch {
	case !sandboxplan.ValidName(p.Name):
		return errors.New("hostadapter: the project's name is not valid")
	case !sandboxplan.ValidName(p.Role):
		return errors.New("hostadapter: the project's role is not a valid name")
	case p.Mirror == "" || !filepath.IsAbs(p.Mirror) || filepath.Clean(p.Mirror) != p.Mirror || strings.ContainsAny(p.Mirror, "\x00\n"):
		return errors.New("hostadapter: the mirror is not a clean absolute path")
	case !strings.HasPrefix(p.Target, "refs/heads/") || len(p.Target) == len("refs/heads/") || len(p.Target) > 200 ||
		strings.ContainsAny(p.Target, " ~^:?*[\\\x00\n\t") || strings.Contains(p.Target, ".."):
		return errors.New("hostadapter: the target is not a branch under refs/heads/")
	case len(p.Gates) == 0:
		return errors.New("hostadapter: the project has no gate")
	}
	// The gates are checked by the one function that knows the rules for
	// them; the other arguments are placeholders that pass its own checks.
	if _, err := verify.Script("refs/leads/a/heads/b", strings.Repeat("0", 40), "/m", "/s", p.Gates); err != nil {
		return errors.New("hostadapter: a gate of the project is not valid")
	}
	if p.Identity.Name == "" || p.Identity.Email == "" {
		return errors.New("hostadapter: the project has no identity for the merge commit")
	}
	return nil
}

// featureRef is the lead's branch for a feature, in the mirror.
func (p Project) featureRef(name string) string {
	return "refs/leads/" + p.Role + "/heads/feature/" + name
}

// Config is what a [Host] is made from.
type Config struct {
	// Git is the git executable, from gitenv.New or gitenv.Find.
	Git gitenv.Git
	// Projects are the projects the host serves. At least one.
	Projects []Project
	// Suite runs the project's gates on a merge. Required.
	Suite Suite
	// Now is the clock of the verification records and of the collector.
	// Default time.Now.
	Now func() time.Time
	// GitTimeout is the time of one git call; default [DefaultGitTimeout].
	GitTimeout time.Duration
	// Limits bound the intake's reads; zero fields take its defaults.
	Limits intake.Limits
	// Notice, when set, is called with a host-written line for something
	// the operator should see.
	Notice func(line string)
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Config) notice(format string, a ...any) {
	if c.Notice != nil {
		c.Notice(fmt.Sprintf(format, a...))
	}
}
