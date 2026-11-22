package switcher

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func verifier(v Verifier) func(*Config) { return func(c *Config) { c.Verifier = v } }

// signRelease puts a signature file in a release and rewrites nothing else:
// the signature is not listed in SHA256SUMS.
func (e *env) signRelease(dir string) {
	e.t.Helper()
	os.Chmod(dir, 0o755)
	e.write(filepath.Join(dir, SignatureFile), "sig", 0o644)
}

func TestNoPolicyIsManual(t *testing.T) {
	e := newEnv(t)
	dir := e.release(rel1, "s")
	e.signRelease(dir)
	v := &fakeVerifier{err: errors.New("never asked")}
	if code := e.switchTo(rel1, verifier(v)); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if v.calls != 0 {
		t.Errorf("verifier called without a policy")
	}
	if rec := e.record(); rec.Mode != ModeManual || rec.Trust != ModeManual {
		t.Errorf("record: mode %q, trust %q", rec.Mode, rec.Trust)
	}
	if got := e.status(); !strings.Contains(got, "trust: manual: no switch policy, unsigned releases are accepted") {
		t.Errorf("status:\n%s", got)
	}
}

func TestSignedPolicy(t *testing.T) {
	e := newEnv(t)
	e.policy(signedPolicy)
	dir := e.release(rel1, "s")
	e.signRelease(dir)
	v := &fakeVerifier{}
	if code := e.switchTo(rel1, verifier(v)); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	sums, _ := os.ReadFile(filepath.Join(dir, SumsFile))
	if v.calls != 1 || string(v.data) != string(sums) || v.principal != "release@example.invalid" ||
		v.signers != filepath.Join(e.root, "etc/authority-host/allowed_signers") {
		t.Errorf("verifier: %d calls, principal %q, signers %q", v.calls, v.principal, v.signers)
	}
	if rec := e.record(); rec.Mode != ModeSigned || rec.Trust != ModeSigned {
		t.Errorf("record: mode %q, trust %q", rec.Mode, rec.Trust)
	}
	if got := e.status(); !strings.Contains(got, "trust: signed: releases must be signed by release@example.invalid") {
		t.Errorf("status:\n%s", got)
	}

	t.Run("unsigned refused", func(t *testing.T) {
		e.release(rel2, "s")
		if code := e.switchTo(rel2, verifier(v)); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		e.unchanged("releases/"+rel1, "verify")
		if rec := e.record(); rec.Error != CodeSignatureMissing {
			t.Errorf("record: %+v", rec)
		}
	})
	t.Run("bad signature refused", func(t *testing.T) {
		e.signRelease(e.relDir(rel2))
		bad := &fakeVerifier{err: errors.New("bad")}
		if code := e.switchTo(rel2, verifier(bad)); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		e.unchanged("releases/"+rel1, "verify")
	})
	t.Run("--mode manual refused", func(t *testing.T) {
		before := digest(t, e.root)
		if code := e.switchTo(rel2, verifier(v), func(c *Config) { c.Mode = ModeManual }); code != ExitUsage {
			t.Fatalf("exit %d", code)
		}
		if digest(t, e.root) != before {
			t.Errorf("a refused mode changed the tree")
		}
	})
}

// TestRollbackFollowsPolicy: a rollback is verified as the policy says, so
// an altered previous release with recomputed sums is refused on a signed
// host.
func TestRollbackFollowsPolicy(t *testing.T) {
	e := newEnv(t)
	e.policy(signedPolicy)
	for _, id := range []string{rel1, rel2} {
		e.signRelease(e.release(id, "s"))
	}
	good := &fakeVerifier{}
	for _, id := range []string{rel1, rel2} {
		if code := e.switchTo(id, verifier(good)); code != ExitOK {
			t.Fatalf("switch to %s: exit %d", id, code)
		}
	}
	bad := &fakeVerifier{err: errors.New("the sums of rel1 were recomputed")}
	code := e.switcher(verifier(bad)).RollBack(t.Context(), "previous")
	e.out.Reset()
	if code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "verify")
	if bad.calls != 1 || !strings.HasSuffix(bad.sigs[0], rel1+"/"+SignatureFile) {
		t.Errorf("the rollback target was not verified: %+v", bad)
	}
	code = e.switcher(verifier(good)).RollBack(t.Context(), "previous")
	e.out.Reset()
	if code != ExitOK || e.record().Mode != ModeSigned {
		t.Fatalf("good rollback: exit %d, %+v", code, e.record())
	}
}

func TestManualPolicyAllowsSigned(t *testing.T) {
	e := newEnv(t)
	e.policy(`{"min_mode": "manual", "allowed_signers": "/etc/authority-host/allowed_signers", "principal": "release@example.invalid"}`)
	e.signRelease(e.release(rel1, "s"))
	v := &fakeVerifier{}
	if code := e.switchTo(rel1, verifier(v), func(c *Config) { c.Mode = ModeSigned }); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if rec := e.record(); v.calls != 1 || rec.Mode != ModeSigned || rec.Trust != ModeManual {
		t.Errorf("verifier calls %d, record %+v", v.calls, rec)
	}
	if got := e.status(); !strings.Contains(got, "trust: manual: the switch policy accepts unsigned releases") {
		t.Errorf("status:\n%s", got)
	}
}

func TestPolicyRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy string
		setup  func(e *env)
		mod    func(*Config)
		code   int
	}{
		{"unknown key", `{"min_mode": "signed", "allowed_signers": "/etc/authority-host/allowed_signers", "principal": "release@example.invalid", "extra": 1}`, nil, nil, ExitFailed},
		{"bad mode", `{"min_mode": "trust"}`, nil, nil, ExitFailed},
		// A second object after the first: the first alone would choose
		// manual.
		{"trailing object", `{"min_mode": "manual"}` + "\n" + signedPolicy, nil, nil, ExitFailed},
		{"trailing word", `{"min_mode": "manual"} x`, nil, nil, ExitFailed},
		// encoding/json keeps the last of two equal keys.
		{"duplicate min_mode", `{"min_mode": "signed", "allowed_signers": "/etc/authority-host/allowed_signers", "principal": "release@example.invalid", "min_mode": "manual"}`, nil, nil, ExitFailed},
		{"duplicate principal", `{"min_mode": "signed", "allowed_signers": "/etc/authority-host/allowed_signers", "principal": "release@example.invalid", "principal": "other@example.invalid"}`, nil, nil, ExitFailed},
		{"not an object", `["signed"]`, nil, nil, ExitFailed},
		{"number value", `{"min_mode": 1}`, nil, nil, ExitFailed},
		{"signed without principal", `{"min_mode": "signed", "allowed_signers": "/etc/authority-host/allowed_signers"}`, nil, nil, ExitFailed},
		{"principal with a space", `{"min_mode": "signed", "allowed_signers": "/etc/authority-host/allowed_signers", "principal": "a b"}`, nil, nil, ExitFailed},
		{"principal an option", `{"min_mode": "signed", "allowed_signers": "/etc/authority-host/allowed_signers", "principal": "-Ox"}`, nil, nil, ExitFailed},
		{"relative signers", `{"min_mode": "signed", "allowed_signers": "etc/authority-host/allowed_signers", "principal": "p"}`, nil, nil, ExitFailed},
		{"signers missing", `{"min_mode": "signed", "allowed_signers": "/etc/authority-host/none", "principal": "p"}`, nil, nil, ExitFailed},
		{"policy writable by others", signedPolicy, func(e *env) {
			os.Chmod(filepath.Join(e.root, PolicyDir, PolicyName), 0o666)
		}, nil, ExitFailed},
		{"policy folder writable by its group", signedPolicy, func(e *env) {
			os.Chmod(filepath.Join(e.root, PolicyDir), 0o775)
		}, nil, ExitFailed},
		{"signers writable by others", signedPolicy, func(e *env) {
			os.Chmod(filepath.Join(e.root, "etc/authority-host/allowed_signers"), 0o646)
		}, nil, ExitFailed},
		{"policy a link", signedPolicy, func(e *env) {
			p := filepath.Join(e.root, PolicyDir, PolicyName)
			os.Rename(p, p+".real")
			os.Symlink(p+".real", p)
		}, nil, ExitFailed},
		{"policy not root's", signedPolicy, nil, func(c *Config) {
			c.Owner = func(info fs.FileInfo) error {
				if info.Name() == PolicyName {
					return errors.New("owned by uid 1000, not root")
				}
				return DefaultOwnerCheck(info)
			}
		}, ExitFailed},
		{"signed mode, no signers in the policy", `{"min_mode": "manual"}`, nil, func(c *Config) { c.Mode = ModeSigned }, ExitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.policy(tc.policy)
			e.signRelease(e.release(rel1, "s"))
			if tc.setup != nil {
				tc.setup(e)
			}
			before := digest(t, e.root)
			mods := []func(*Config){verifier(&fakeVerifier{})}
			if tc.mod != nil {
				mods = append(mods, tc.mod)
			}
			if code := e.switchTo(rel1, mods...); code != tc.code {
				t.Fatalf("exit %d, want %d", code, tc.code)
			}
			if digest(t, e.root) != before {
				t.Errorf("a refused policy changed the tree")
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
			if tc.code == ExitFailed {
				if code := e.switcher(mods...).Status(); code != ExitFailed {
					t.Errorf("status with a refused policy: exit %d", code)
				}
				e.out.Reset()
			}
		})
	}
}

// TestCheckOneObject: the token walk under every JSON file the switch reads.
func TestCheckOneObject(t *testing.T) {
	for _, tc := range []struct {
		data string
		ok   bool
	}{
		{`{}`, true},
		{` {"a": 1, "b": {"a": 2}, "c": [1, {"a": 3}]} ` + "\n", true},
		{`{"a": 1, "a": 1}`, false},
		{`{"b": {"a": 1, "a": 2}}`, false},
		{`{"c": [{"a": 1, "a": 2}]}`, false},
		{`{} {}`, false},
		{`{}]`, false},
		{`[]`, false},
		{`"x"`, false},
		{``, false},
		{`{"a": ` + strings.Repeat("[", 20) + strings.Repeat("]", 20) + `}`, false},
	} {
		if err := checkOneObject([]byte(tc.data)); (err == nil) != tc.ok {
			t.Errorf("checkOneObject(%q) = %v", tc.data, err)
		}
	}
}
