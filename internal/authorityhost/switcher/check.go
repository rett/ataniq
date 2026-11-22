package switcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
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

// maxCheckProgram bounds the --noaicheck program the switch hashes.
const maxCheckProgram = 256 << 20

// checkRef is the no-AI check a transaction runs: the release's own
// bin/ah-noaicheck (prog empty), or the --noaicheck program prog, a path on
// the host, bound by its SHA-256, sum. The journal keeps both.
type checkRef struct{ prog, sum string }

// newCheckRef is the check of a new switch: the --noaicheck program, which
// must be protected code (see overrideSum), or the release's own.
func (s *Switcher) newCheckRef() (checkRef, error) {
	if s.cfg.NoAICheck == "" {
		return checkRef{}, nil
	}
	sum, err := s.overrideSum(s.cfg.NoAICheck)
	if err != nil {
		return checkRef{}, err
	}
	return checkRef{prog: s.cfg.NoAICheck, sum: sum}, nil
}

// overrideSum checks the --noaicheck program hostPath (a path on the host,
// below the root) as protected code and returns its SHA-256: the root and
// every folder down to it real folders, owned as the owner check accepts
// and not writable by group or others (see openTrusted), and the program a
// regular file with one link, owned and moded the same way. It is hashed
// through the descriptor that was checked.
func (s *Switcher) overrideSum(hostPath string) (string, error) {
	rel := strings.TrimPrefix(hostPath, "/")
	full := filepath.Join(s.cfg.Root, rel)
	missing := func(err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return failf(CodeNoAICheckMissing, "no-AI check %s is missing", q(hostPath))
		}
		return err
	}
	dir, err := s.openTrusted(path.Dir(rel), false)
	if err != nil {
		return "", missing(err)
	}
	defer dir.Close()
	f, info, err := openFileIn(dir, path.Base(rel))
	if err != nil {
		return "", missing(err)
	}
	defer f.Close()
	if err := s.trusted(full, info, false); err != nil {
		return "", err
	}
	if links(info) != 1 {
		return "", failf(CodeUntrusted, "%s has more than one link", q(full))
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxCheckProgram+1))
	if err != nil {
		return "", failf(CodeReadFailed, "%s: %s", q(full), q(err.Error()))
	}
	if n != info.Size() || n > maxCheckProgram {
		return "", failf(CodeFileChanged, "%s changed while it was read, or is larger than %d bytes", q(full), maxCheckProgram)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkPath is the program c names, for rel, checked again just before it
// runs: the release's own must be in it (the release was verified), and an
// override must still be protected and hash to the SHA-256 its transaction
// journaled; otherwise it is not run.
func (s *Switcher) checkPath(c checkRef, rel *release) (string, error) {
	if c.prog == "" {
		ok, err := rel.exists(NoAICheckProgram)
		if err == nil && !ok {
			err = failf(CodeNoAICheckMissing, "%s missing in the release", NoAICheckProgram)
		}
		if err != nil {
			return "", err
		}
		return filepath.Join(rel.dir, NoAICheckProgram), nil
	}
	sum, err := s.overrideSum(c.prog)
	if err != nil {
		return "", err
	}
	if sum != c.sum {
		return "", failf(CodeNoAICheckChanged, "no-AI check %s is not the program its transaction checked (its SHA-256 changed); it is not run", q(c.prog))
	}
	return filepath.Join(s.cfg.Root, strings.TrimPrefix(c.prog, "/")), nil
}

// preCheck is step 4: the no-AI check c of the release before it is
// activated, which must exit 0:
//
//	CHECK --phase pre --root ROOT --release DIR --require-policy
func (s *Switcher) preCheck(ctx context.Context, a *attempt, c checkRef, rel *release) error {
	check, err := s.checkPath(c, rel)
	if err != nil {
		return err
	}
	argv := []string{check, "--phase", CheckPhasePre, "--root", s.cfg.Root, "--release", rel.dir, "--require-policy"}
	if s.cfg.Dry {
		s.step(4, "would run %s", qargv(argv))
		return nil
	}
	if _, err := s.cfg.Runner.Run(ctx, Command{Argv: argv, Timeout: NoAICheckTimeout}); err != nil {
		a.rec.Check = CheckFailed
		return failf(CodeNoAICheckFailed, "%s (run it by hand to see its hits)", summary(err))
	}
	a.rec.Check = CheckPassed
	s.step(4, "passed (phase pre)")
	return nil
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
func (s *Switcher) postCheck(ctx context.Context, c checkRef, rel *release) error {
	check, err := s.checkPath(c, rel)
	if err != nil {
		return err
	}
	relDir := rel.dir
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
