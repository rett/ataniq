package switcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// The phases of the no-AI check the switch runs.
const (
	// CheckPhasePre is the check before activation: the candidate's own
	// files (the units under its etc/systemd, the environment names they
	// set) and the host's AI binaries and credentials, without the
	// listening ports and the firewall, which are still the old release's.
	CheckPhasePre = "pre"
	// CheckPhasePost is the check after the reload and the health check:
	// everything, the live listeners and the loaded firewall included,
	// before the switch is recorded as done.
	CheckPhasePost = "post"
)

// checkProgram is the no-AI check of rel, or the --noaicheck program.
func (s *Switcher) checkProgram(rel *release) (string, error) {
	check := s.cfg.NoAICheck
	if check == "" {
		check = filepath.Join(rel.dir, NoAICheckProgram)
		ok, err := rel.exists(NoAICheckProgram)
		if err == nil && !ok {
			err = failf(CodeNoAICheckMissing, "%s missing in the release", NoAICheckProgram)
		}
		if err != nil {
			return "", err
		}
		return check, nil
	}
	if info, err := os.Stat(check); err != nil || !info.Mode().IsRegular() {
		return "", failf(CodeNoAICheckMissing, "no-AI check %s is not a regular file", q(check))
	}
	return check, nil
}

// preCheck is step 4: the no-AI check of the release before it is
// activated, which must exit 0:
//
//	CHECK --phase pre --root ROOT --release DIR --require-policy
//
// It returns the check program, which the post-activation check runs too.
func (s *Switcher) preCheck(ctx context.Context, a *attempt, rel *release) (string, error) {
	check, err := s.checkProgram(rel)
	if err != nil {
		return "", err
	}
	argv := []string{check, "--phase", CheckPhasePre, "--root", s.cfg.Root, "--release", rel.dir, "--require-policy"}
	if s.cfg.Dry {
		s.step(4, "would run %s", qargv(argv))
		return check, nil
	}
	if _, err := s.cfg.Runner.Run(ctx, Command{Argv: argv, Timeout: NoAICheckTimeout}); err != nil {
		a.rec.Check = CheckFailed
		return "", failf(CodeNoAICheckFailed, "%s (run it by hand to see its hits)", summary(err))
	}
	a.rec.Check = CheckPassed
	s.step(4, "passed (phase pre)")
	return check, nil
}

// postCheck is step 8: the no-AI check of the activated release, on the
// host as the reload left it, with the firewall listing it compares against
// the release's pinned digest:
//
//	CHECK --phase post --root ROOT --release DIR --require-policy --nft-listing FILE
//
// FILE is what "nft list ruleset" printed, written to a private file in a
// private temporary folder that is removed afterwards. Any exit status but
// 0 fails, including the check's "HIT incomplete" when it could not finish.
func (s *Switcher) postCheck(ctx context.Context, check, relDir string) error {
	if check == "" {
		return failf(CodeNoAICheckMissing, "the transaction names no no-AI check")
	}
	if info, err := os.Stat(check); err != nil || !info.Mode().IsRegular() {
		return failf(CodeNoAICheckMissing, "no-AI check %s is not a regular file", q(check))
	}
	listArgv := []string{"nft", "list", "ruleset"}
	out, err := s.cfg.Runner.Run(ctx, Command{Argv: listArgv, Timeout: NFTListTimeout, MaxOutput: maxNFTListing})
	if err == nil && out.Truncated {
		err = errors.New("larger than the limit")
	}
	if err != nil {
		return failf(CodeNFTListingFailed, "%s: %s", qargv(listArgv), summary(err))
	}
	tmp, err := os.MkdirTemp("", "ah-switch-nft-")
	if err != nil {
		return failf(CodeNFTListingFailed, "temporary folder: %s", q(err.Error()))
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o700); err != nil {
		return failf(CodeNFTListingFailed, "temporary folder: %s", q(err.Error()))
	}
	listing := filepath.Join(tmp, "nft-ruleset")
	if err := os.WriteFile(listing, out.Stdout, 0o600); err != nil {
		return failf(CodeNFTListingFailed, "listing: %s", q(err.Error()))
	}
	argv := []string{check, "--phase", CheckPhasePost, "--root", s.cfg.Root, "--release", relDir, "--require-policy", "--nft-listing", listing}
	if _, err := s.cfg.Runner.Run(ctx, Command{Argv: argv, Timeout: NoAICheckTimeout}); err != nil {
		return failf(CodeNoAICheckFailed, "phase post: %s (run it by hand to see its hits)", summary(err))
	}
	s.step(8, "passed (phase post)")
	return nil
}
