package switcher

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// The switch policy, below the root: owner-local, root-owned, never part of
// a release.
const (
	PolicyDir  = "etc/authority-host"
	PolicyName = "switch-policy.json"
)

// Policy is the switch policy. Without the file the policy is manual: any
// release whose sums match is accepted, signed or not.
type Policy struct {
	// MinMode is the weakest mode a switch may use: manual or signed.
	MinMode string `json:"min_mode"`
	// AllowedSigners is the ssh allowed signers file (an absolute path on
	// the host, below the root), required for signed mode.
	AllowedSigners string `json:"allowed_signers"`
	// Principal is the identity a release must be signed by, given to
	// ssh-keygen -Y verify -I; required for signed mode.
	Principal string `json:"principal"`

	present bool // the file was there
}

// Trust is what deploy.json records as trust: the policy's minimum mode, or
// manual without a policy.
func (p Policy) Trust() string {
	if p.MinMode == "" {
		return ModeManual
	}
	return p.MinMode
}

var principalPattern = regexp.MustCompile(`^[A-Za-z0-9._@+][A-Za-z0-9._@+-]{0,127}$`)

// readPolicy reads the switch policy. A missing policy folder or file is the
// manual policy. The folder and every folder above it must be trusted, as the
// state folder is, and so must the file and the allowed signers file it
// names; a file that is not exactly one JSON object, a key given twice, not
// spelled exactly as declared or unknown, a mode other than manual or
// signed, or a signed policy without its signers file and principal is an
// error.
func (s *Switcher) readPolicy() (Policy, error) {
	dir, err := s.openTrusted(PolicyDir, false)
	if errors.Is(err, fs.ErrNotExist) {
		return Policy{MinMode: ModeManual}, nil
	}
	if err != nil {
		return Policy{}, err
	}
	defer dir.Close()
	data, err := s.readTrustedFile(dir, filepath.Join(s.cfg.Root, PolicyDir, PolicyName), PolicyName, 64<<10)
	if errors.Is(err, fs.ErrNotExist) {
		return Policy{MinMode: ModeManual}, nil
	}
	if err != nil {
		return Policy{}, err
	}
	var p Policy
	if err := decodeStrict(data, &p); err != nil {
		return Policy{}, failf(CodePolicyInvalid, "%s is not one JSON object with each of its keys once and spelled exactly (keys: min_mode, allowed_signers, principal)", PolicyName)
	}
	p.present = true
	switch p.MinMode {
	case ModeManual, ModeSigned:
	default:
		return Policy{}, failf(CodePolicyInvalid, "%s: min_mode %s is neither manual nor signed", PolicyName, q(p.MinMode))
	}
	if p.Principal != "" && !principalPattern.MatchString(p.Principal) {
		return Policy{}, failf(CodePolicyInvalid, "%s: principal %s is not one word of letters, digits and ._@+-", PolicyName, q(p.Principal))
	}
	if p.AllowedSigners != "" {
		clean := path.Clean(p.AllowedSigners)
		if !path.IsAbs(p.AllowedSigners) || clean != p.AllowedSigners || clean == "/" {
			return Policy{}, failf(CodePolicyInvalid, "%s: allowed_signers %s is not a clean absolute path", PolicyName, q(p.AllowedSigners))
		}
		if err := s.checkSigners(p.AllowedSigners); err != nil {
			return Policy{}, err
		}
	}
	if p.MinMode == ModeSigned && (p.AllowedSigners == "" || p.Principal == "") {
		return Policy{}, failf(CodePolicyInvalid, "%s: min_mode signed needs allowed_signers and principal", PolicyName)
	}
	return p, nil
}

// checkSigners checks that the allowed signers file (a path on the host) and
// every folder above it are trusted.
func (s *Switcher) checkSigners(hostPath string) error {
	rel := strings.TrimPrefix(hostPath, "/")
	dir, err := s.openTrusted(path.Dir(rel), false)
	if err == nil {
		defer dir.Close()
		_, err = s.readTrustedFile(dir, filepath.Join(s.cfg.Root, rel), path.Base(rel), 1<<20)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return failf(CodePolicyInvalid, "allowed signers file %s is missing", q(hostPath))
	}
	return err
}

// readTrustedFile reads the regular file name of the trusted folder r, of at
// most max bytes, after checking it as trusted checks a folder: a regular
// file, not writable by group or others, of an owner the owner check
// accepts. full names it in messages.
func (s *Switcher) readTrustedFile(r *os.Root, full, name string, max int64) ([]byte, error) {
	f, info, err := openFileIn(r, name)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := s.trusted(full, info, false); err != nil {
		return nil, err
	}
	return readFileIn(r, name, max)
}

// applyPolicy reads the policy and chooses the mode of this switch: the
// policy's minimum, or --mode when that is at least as strict. A mode weaker
// than the policy, or signed mode without a signers file and principal, is a
// usage error; a policy that cannot be trusted or read fails the switch.
// Nothing is changed or recorded either way.
func (s *Switcher) applyPolicy() (int, bool) {
	p, err := s.readPolicy()
	if err != nil {
		s.errorf("switch policy %s: %s; nothing done", q(filepath.Join(s.cfg.Root, PolicyDir, PolicyName)), describe(err))
		return ExitFailed, true
	}
	s.policy = p
	s.mode = p.MinMode
	switch {
	case s.cfg.Mode == ModeManual && p.MinMode == ModeSigned:
		s.errorf("--mode manual is weaker than the switch policy, which requires signed releases")
		return ExitUsage, true
	case s.cfg.Mode == ModeSigned:
		if p.AllowedSigners == "" || p.Principal == "" {
			s.errorf("--mode signed needs allowed_signers and principal in %s", filepath.Join("/", PolicyDir, PolicyName))
			return ExitUsage, true
		}
		s.mode = ModeSigned
	}
	s.trust = trust{}
	if s.mode == ModeSigned {
		s.trust.signed = []signer{{signers: p.AllowedSigners, principal: p.Principal}}
	}
	return 0, false
}

// signer is one signature a release must carry: by principal, as the
// allowed signers file signers (a path on the host) lists it.
type signer struct{ signers, principal string }

// trust is what a release must carry beyond its sums: a signature for each
// signer. None is the manual mode.
type trust struct{ signed []signer }

func (t trust) mode() string {
	if len(t.signed) == 0 {
		return ModeManual
	}
	return ModeSigned
}

// with is t and o together: every signature either asks for.
func (t trust) with(o trust) trust {
	out := trust{signed: append([]signer(nil), t.signed...)}
	for _, sg := range o.signed {
		dup := false
		for _, have := range out.signed {
			dup = dup || have == sg
		}
		if !dup {
			out.signed = append(out.signed, sg)
		}
	}
	return out
}

// signersPath is the allowed signers file hostPath as the verifier opens
// it.
func (s *Switcher) signersPath(hostPath string) string {
	return filepath.Join(s.cfg.Root, strings.TrimPrefix(hostPath, "/"))
}
