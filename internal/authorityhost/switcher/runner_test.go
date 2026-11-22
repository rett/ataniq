package switcher

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecRunnerCleanEnvironment(t *testing.T) {
	t.Setenv("AH_SWITCH_LEAK", "from the caller")
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	prog := script(t, dir, "show-env", "echo \"PATH=$PATH\"\necho \"LEAK=${AH_SWITCH_LEAK-unset}\"\npwd\n")
	out, err := ExecRunner{}.Run(context.Background(), Command{Argv: []string{prog}, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("%v: %s", err, out.Stderr)
	}
	want := "PATH=/usr/sbin:/usr/bin:/sbin:/bin\nLEAK=unset\n/\n"
	if string(out.Stdout) != want {
		t.Errorf("output %q, want %q", out.Stdout, want)
	}
}

func TestExecRunnerExitStatusAndStdin(t *testing.T) {
	dir := t.TempDir()
	prog := script(t, dir, "fail", "echo nope >&2\nexit 4\n")
	out, err := ExecRunner{}.Run(context.Background(), Command{Argv: []string{prog}, Timeout: 10 * time.Second})
	if err == nil || err.Error() != "exit status 4" || strings.TrimSpace(string(out.Stderr)) != "nope" {
		t.Errorf("err %v, stderr %q", err, out.Stderr)
	}
	out, err = ExecRunner{}.Run(context.Background(), Command{Argv: []string{"cat"}, Stdin: []byte("data\n"), Timeout: 10 * time.Second})
	if err != nil || string(out.Stdout) != "data\n" {
		t.Errorf("cat: %q, %v", out.Stdout, err)
	}
	out, err = ExecRunner{}.Run(context.Background(), Command{Argv: []string{"cat"}, Timeout: 10 * time.Second})
	if err != nil || len(out.Stdout) != 0 {
		t.Errorf("cat without stdin: %q, %v", out.Stdout, err)
	}
}

func TestExecRunnerTimeoutKillsTheGroup(t *testing.T) {
	dir := t.TempDir()
	// A child in the background keeps the output pipe open; killing only
	// the script would leave Run waiting for it.
	prog := script(t, dir, "hang", "sleep 30 &\nsleep 30\n")
	start := time.Now()
	_, err := ExecRunner{}.Run(context.Background(), Command{Argv: []string{prog}, Timeout: 300 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Errorf("err %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Run took %s after a 300ms limit", d)
	}
}

// fakeNFT makes the search path a folder whose nft prints listing, for the
// length of the test.
func fakeNFT(t *testing.T, listing string) {
	t.Helper()
	dir := t.TempDir()
	script(t, dir, "nft", "cat <<'EOF'\n"+listing+"EOF\n")
	saved := SearchPath
	SearchPath = append([]string{dir}, saved...)
	t.Cleanup(func() { SearchPath = saved })
}

func TestLookPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	p, err := LookPath("sh")
	if err != nil || !filepath.IsAbs(p) {
		t.Errorf("LookPath(sh) = %q, %v", p, err)
	}
	for _, bad := range []string{"", "bin/sh", "./sh", "no-such-program-for-ah-switch"} {
		if _, err := LookPath(bad); err == nil {
			t.Errorf("LookPath(%q) accepted", bad)
		}
	}
}

// TestSwitchWithExecRunner runs a switch with real programs: the no-AI check,
// a validator and the health command are scripts.
func TestSwitchWithExecRunner(t *testing.T) {
	e := newEnv(t)
	marks := t.TempDir()
	fakeNFT(t, "table inet filter {\n}\n")
	dir := e.relDir(rel1)
	e.release(rel1, "s")
	// The check is given its phase, the root, the release and
	// --require-policy, and after activation a readable listing of the
	// firewall.
	e.write(filepath.Join(dir, NoAICheckProgram), "#!/bin/sh\n"+
		"case \"$2\" in\n"+
		"pre) [ \"$*\" = \"--phase pre --root "+e.root+" --release "+dir+" --require-policy\" ] && touch "+marks+"/checked-pre ;;\n"+
		"post) [ \"$1 $2 $3 $4 $5 $6 $7 $8\" = \"--phase post --root "+e.root+" --release "+dir+" --require-policy --nft-listing\" ] && grep -q 'table inet filter' \"$9\" && touch "+marks+"/checked-post ;;\n"+
		"*) exit 3 ;;\nesac\n", 0o755)
	e.write(filepath.Join(dir, "bin/validate"), "#!/bin/sh\ntouch "+marks+"/validated\n", 0o755)
	e.write(filepath.Join(dir, ValidatorsList), "{release}/bin/validate\n", 0o644)
	health := script(t, marks, "health", "exit 0\n")
	e.write(filepath.Join(dir, ReloadList), health+"\n", 0o644)
	e.write(filepath.Join(dir, HealthCmd), health+"\n", 0o644)
	e.sums(dir)
	code := e.switchTo(rel1, func(c *Config) { c.Runner = ExecRunner{} })
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	for _, m := range []string{"checked-pre", "checked-post", "validated"} {
		if _, err := os.Stat(filepath.Join(marks, m)); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}

	// A failing check from a script blocks the next switch.
	e.release(rel2, "s")
	fail := script(t, marks, "noai-fail", "echo 'an AI tool is installed' >&2\nexit 1\n")
	code = e.switchTo(rel2, func(c *Config) { c.Runner = ExecRunner{}; c.NoAICheck = fail })
	if code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel1, "noaicheck")
	if rec := e.record(); rec.Error != CodeNoAICheckFailed || rec.Detail != "exit status 1 (run it by hand to see its hits)" {
		t.Errorf("error: %s: %s", rec.Error, rec.Detail)
	}
	// What the check wrote is neither recorded nor printed: a diagnostic
	// can hold a secret.
	data, err := os.ReadFile(filepath.Join(e.root, StateDir, DeployFile))
	if err != nil || strings.Contains(string(data), "AI tool") {
		t.Errorf("deploy.json carries the check's output: %s, %v", data, err)
	}
}

// TestSSHVerifierWithRealKey signs a release's sums with a fresh key and
// verifies it with ssh-keygen. It is skipped where ssh-keygen is absent.
func TestSSHVerifierWithRealKey(t *testing.T) {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen not installed")
	}
	keys := t.TempDir()
	key := filepath.Join(keys, "key")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(keygen, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen %v: %v\n%s", args, err, out)
		}
	}
	run("-q", "-t", "ed25519", "-N", "", "-C", "release-signer", "-f", key)
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	// A second identity in the same signers file: a signature by its key
	// is not one by the principal the policy names.
	other := filepath.Join(keys, "other")
	run("-q", "-t", "ed25519", "-N", "", "-C", "other-signer", "-f", other)
	otherPub, err := os.ReadFile(other + ".pub")
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t)
	e.policy(signedPolicy)
	e.write(filepath.Join(e.root, "etc/authority-host/allowed_signers"),
		"release@example.invalid "+string(pub)+"other@example.invalid "+string(otherPub), 0o644)
	dir := e.release(rel1, "s")
	run("-Y", "sign", "-q", "-f", key, "-n", SignatureNamespace, filepath.Join(dir, SumsFile))
	e.stamp(dir)
	signed := func(c *Config) {
		c.Verifier = SSHVerifier{Runner: ExecRunner{}, Program: keygen}
	}
	if code := e.switchTo(rel1, signed); code != ExitOK {
		t.Fatalf("good signature: exit %d", code)
	}

	// A release whose sums were changed after signing is refused.
	dir = e.release(rel2, "s")
	run("-Y", "sign", "-q", "-f", key, "-n", SignatureNamespace, filepath.Join(dir, SumsFile))
	e.write(filepath.Join(dir, "bin/app"), "changed after signing\n", 0o755)
	e.sums(dir)
	if code := e.switchTo(rel2, signed); code != ExitFailed {
		t.Fatalf("changed sums: exit %d", code)
	}
	e.unchanged("releases/"+rel1, "verify")

	// A signature in another namespace is refused.
	dir = e.release(rel3, "s")
	run("-Y", "sign", "-q", "-f", key, "-n", "git", filepath.Join(dir, SumsFile))
	e.stamp(dir)
	if code := e.switchTo(rel3, signed); code != ExitFailed {
		t.Fatalf("other namespace: exit %d", code)
	}
	e.unchanged("releases/"+rel1, "verify")

	// The other identity of the signers file is not the principal.
	os.Remove(filepath.Join(dir, SignatureFile))
	run("-Y", "sign", "-q", "-f", other, "-n", SignatureNamespace, filepath.Join(dir, SumsFile))
	e.stamp(dir)
	if code := e.switchTo(rel3, signed); code != ExitFailed {
		t.Fatalf("other listed signer: exit %d", code)
	}
	e.unchanged("releases/"+rel1, "verify")

	// A key not in the allowed signers is refused.
	stranger := filepath.Join(keys, "stranger")
	run("-q", "-t", "ed25519", "-N", "", "-C", "stranger", "-f", stranger)
	os.Remove(filepath.Join(dir, SignatureFile))
	run("-Y", "sign", "-q", "-f", stranger, "-n", SignatureNamespace, filepath.Join(dir, SumsFile))
	e.stamp(dir)
	if code := e.switchTo(rel3, signed); code != ExitFailed {
		t.Fatalf("unknown signer: exit %d", code)
	}
	e.unchanged("releases/"+rel1, "verify")
}
