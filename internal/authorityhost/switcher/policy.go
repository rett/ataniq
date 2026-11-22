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

// maxSigners bounds the allowed signers file.
const maxSigners = 1 << 20

// checkSigners checks that the allowed signers file (a path on the host) and
// every folder above it are trusted.
func (s *Switcher) checkSigners(hostPath string) error {
	_, err := s.readSigners(hostPath)
	return err
}

// readSigners reads the allowed signers file (a path on the host) after
// checking it and every folder above it, from the descriptor it checked.
func (s *Switcher) readSigners(hostPath string) ([]byte, error) {
	rel := strings.TrimPrefix(hostPath, "/")
	dir, err := s.openTrusted(path.Dir(rel), false)
	var data []byte
	if err == nil {
		defer dir.Close()
		data, err = s.readTrustedFile(dir, filepath.Join(s.cfg.Root, rel), path.Base(rel), maxSigners)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, failf(CodePolicyInvalid, "allowed signers file %s is missing", q(hostPath))
	}
	return data, err
}

// readTrustedFile reads the regular file name of the trusted folder r, of at
// most max bytes, after checking it as trusted checks a folder: a regular
// file, not writable by group or others, of an owner the owner check
// accepts. It reads the descriptor it checked, never the name again, so
// what it returns is what was checked. full names it in messages.
func (s *Switcher) readTrustedFile(r *os.Root, full, name string, max int64) ([]byte, error) {
	f, info, err := openFileIn(r, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := s.trusted(full, info, false); err != nil {
		return nil, err
	}
	return readOpened(f, info, name, max)
}

// privateCopy writes data to a new file of mode 0600 in a new temporary
// folder of mode 0700, for a program that needs a path: what it reads is
// then what the switch checked. The returned func removes both.
func privateCopy(name string, data []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", "ah-switch-")
	if err != nil {
		return "", nil, err
	}
	done := func() { os.RemoveAll(dir) }
	if err := os.Chmod(dir, 0o700); err != nil {
		done()
		return "", nil, err
	}
	p := filepath.Join(dir, name)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		done()
		return "", nil, err
	}
	return p, done, nil
}

// applyPolicy reads the policy and chooses the mode of this switch: the
// policy's minimum, or --mode when that is at least as strict. A mode weaker
// than the policy, or signed mode without a signers file and principal, is a
// usage error (usage, with its exit code); a policy that cannot be trusted
// or read is returned as blocked, for the command to report once it has
// run, under the lock, the stops a journal still owes (see stopFirst).
// Nothing is changed or recorded either way.
func (s *Switcher) applyPolicy() (code int, usage bool, blocked error) {
	p, err := s.readPolicy()
	if err != nil {
		s.policy, s.mode, s.trust = Policy{}, ModeManual, trust{}
		return 0, false, failf(codeOrPolicy(err), "switch policy %s: %s", q(filepath.Join(s.cfg.Root, PolicyDir, PolicyName)), describeDetail(err))
	}
	s.policy = p
	s.mode = p.MinMode
	switch {
	case s.cfg.Mode == ModeManual && p.MinMode == ModeSigned:
		s.errorf("--mode manual is weaker than the switch policy, which requires signed releases")
		return ExitUsage, true, nil
	case s.cfg.Mode == ModeSigned:
		if p.AllowedSigners == "" || p.Principal == "" {
			s.errorf("--mode signed needs allowed_signers and principal in %s", filepath.Join("/", PolicyDir, PolicyName))
			return ExitUsage, true, nil
		}
		s.mode = ModeSigned
	}
	s.trust = trust{}
	if s.mode == ModeSigned {
		s.trust.signed = []signer{{signers: p.AllowedSigners, principal: p.Principal}}
	}
	return 0, false, nil
}

// codeOrPolicy is the code of err, a failure to read the policy.
func codeOrPolicy(err error) string {
	code, _ := codeOf(err)
	return code
}

// describeDetail is the detail of err, or its code when it has none.
func describeDetail(err error) string {
	code, detail := codeOf(err)
	if detail == "" {
		return code
	}
	return detail
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
