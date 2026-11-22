package tests

// The deploy driver's checks, run by TestAuthorityHostDeployScripts. The
// driver gets a PATH whose ssh, rsync and ah-release only record their
// arguments (see ahStubs), so no test reaches a host, and the host name the
// driver reads from AUTHORITY_HOST_SSH is a reserved one that the output must
// never show.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	ahTestHost = "tester@host.example.invalid"
	ahTestID   = "0007-ab12cd3"
)

// ahFakeRelease makes a release folder named id with a MANIFEST that says
// so, and returns its path.
func ahFakeRelease(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"MANIFEST":    "id: " + id + "\ncounter: 7\n",
		"SHA256SUMS":  "",
		"bin/program": "x\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func testAuthorityHostDeploy(t *testing.T) {
	bin, calls := ahStubs(t, "ssh", "rsync", "ah-release")
	stubRelease := filepath.Join(bin, "ah-release")
	workDir := t.TempDir()

	env := func(extra ...string) []string {
		return append([]string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + workDir}, extra...)
	}
	withHost := func(extra ...string) []string {
		return env(append([]string{"AUTHORITY_HOST_SSH=" + ahTestHost}, extra...)...)
	}
	run := func(env []string, args ...string) (string, int) {
		t.Helper()
		_ = os.Remove(calls)
		out, code := ahRun(t, ahDeployScript, workDir, env, args...)
		for _, private := range []string{"example.invalid", "tester@"} {
			if strings.Contains(out, private) {
				t.Errorf("the output names the host (%q):\n%s", private, out)
			}
		}
		return out, code
	}
	called := func(prefix string) []string {
		var got []string
		for _, line := range ahCalls(t, calls) {
			if strings.HasPrefix(line, prefix+" ") {
				got = append(got, line)
			}
		}
		return got
	}
	names := func() []string {
		var got []string
		for _, line := range ahCalls(t, calls) {
			got = append(got, strings.SplitN(line, " ", 2)[0])
		}
		return got
	}

	t.Run("refuses without AUTHORITY_HOST_SSH", func(t *testing.T) {
		rel := ahFakeRelease(t, ahTestID)
		for _, mode := range [][]string{{"--dry"}, {"--go", "--confirm", ahTestID}, {}} {
			args := append([]string{"--release", rel, "--ah-release", stubRelease}, mode...)
			out, code := run(env(), args...)
			if code != 2 || !strings.Contains(out, "AUTHORITY_HOST_SSH is not set") {
				t.Errorf("%v: exit %d\n%s", mode, code, out)
			}
			if got := ahCalls(t, calls); got != nil {
				t.Errorf("%v: something was called: %v", mode, got)
			}
		}
		// An empty value is the same as none.
		if out, code := run(env("AUTHORITY_HOST_SSH="), "--release", rel, "--dry"); code != 2 || !strings.Contains(out, "is not set") {
			t.Errorf("empty: exit %d\n%s", code, out)
		}
		// --self-test needs none.
		out, code := run(env(), "--release", rel, "--self-test")
		if code != 0 || !strings.Contains(out, "self-test: computed") {
			t.Errorf("self-test: exit %d\n%s", code, out)
		}
		if got := ahCalls(t, calls); got != nil {
			t.Errorf("self-test called: %v", got)
		}
	})

	t.Run("refuses a bad host value", func(t *testing.T) {
		rel := ahFakeRelease(t, ahTestID)
		for _, bad := range []string{"-oProxyCommand=x", "a b", "a;b", "a'b", "-x@y"} {
			out, code := run(env("AUTHORITY_HOST_SSH="+bad), "--release", rel, "--dry")
			if code != 2 || !strings.Contains(out, "AUTHORITY_HOST_SSH must be") {
				t.Errorf("%q: exit %d\n%s", bad, code, out)
			}
			if got := ahCalls(t, calls); got != nil {
				t.Errorf("%q: called %v", bad, got)
			}
		}
	})

	t.Run("refuses a bad id", func(t *testing.T) {
		for _, id := range []string{
			"latest", "7-ab12cd3", "0007-AB12CD3", "0007-ab12cd", "0007-ab12cd3x", "0007_ab12cd3",
			"0007-" + strings.Repeat("a", 41), "00007", "-0007-ab12cd3", "0007-ab12cd3 x", "0007-ab12cd3;rm",
		} {
			rel := ahFakeRelease(t, id)
			for _, mode := range [][]string{{"--dry"}, {"--go", "--confirm", id}, {"--self-test"}} {
				out, code := run(withHost(), append([]string{"--release", rel}, mode...)...)
				if code != 2 || !strings.Contains(out, "must be a release id") {
					t.Errorf("%q %v: exit %d\n%s", id, mode, code, out)
				}
				if got := ahCalls(t, calls); got != nil {
					t.Errorf("%q %v: called %v", id, mode, got)
				}
			}
		}
		// Good ids of other shapes are accepted.
		for _, id := range []string{"0007-ab12cd3", "12345-0123456789abcdef0123456789abcdef01234567"} {
			if out, code := run(withHost(), "--release", ahFakeRelease(t, id), "--self-test"); code != 0 {
				t.Errorf("%q: exit %d\n%s", id, code, out)
			}
		}
		// The folder must be what its name says, and a real folder.
		rel := ahFakeRelease(t, ahTestID)
		if err := os.WriteFile(filepath.Join(rel, "MANIFEST"), []byte("id: 0008-ab12cd3\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, code := run(withHost(), "--release", rel, "--dry"); code != 2 || !strings.Contains(out, "does not say id") {
			t.Errorf("manifest id: exit %d\n%s", code, out)
		}
		link := filepath.Join(t.TempDir(), ahTestID)
		if err := os.Symlink(ahFakeRelease(t, ahTestID), link); err != nil {
			t.Fatal(err)
		}
		if out, code := run(withHost(), "--release", link, "--dry"); code != 2 || !strings.Contains(out, "not a link") {
			t.Errorf("linked folder: exit %d\n%s", code, out)
		}
		if out, code := run(withHost(), "--release", filepath.Join(t.TempDir(), ahTestID), "--dry"); code != 2 {
			t.Errorf("missing folder: exit %d\n%s", code, out)
		}
	})

	// The stub rsync only records its arguments. The real `rsync -n` to
	// USER@HOST:PATH starts ssh and a remote rsync by itself to read the
	// host's listing, so --dry does contact the host (read-only); this test
	// only shows that the driver runs no ssh command of its own, and so never
	// the switch. --self-test is the mode that starts nothing.
	t.Run("dry lists with rsync -n and runs no ssh command of its own", func(t *testing.T) {
		rel := ahFakeRelease(t, ahTestID)
		out, code := run(withHost(), "--release", rel, "--ah-release", stubRelease, "--dry")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if got := called("ssh"); got != nil {
			t.Fatalf("a dry run ran an ssh command of its own: %v", got)
		}
		if got := names(); !slices.Equal(got, []string{"ah-release", "rsync"}) {
			t.Fatalf("calls: %v", got)
		}
		if got := called("ah-release")[0]; got != "ah-release [check] [--no-noaicheck] ["+rel+"]" {
			t.Errorf("ah-release: %s", got)
		}
		want := "rsync [-a] [-n] [--delete] [--itemize-changes] [-e] [ssh -o BatchMode=yes] [--] [" + rel + "/] [" + ahTestHost + ":incoming/" + ahTestID + "/]"
		if got := called("rsync")[0]; got != want {
			t.Errorf("rsync:\n got %s\nwant %s", got, want)
		}
		if !strings.Contains(out, "sudo /usr/local/sbin/ah-switch --from") || !strings.Contains(out, "the authority host") ||
			!strings.Contains(out, "dry run, nothing was copied and nothing was switched") {
			t.Errorf("the dry output lacks the ssh command or the host's name for it:\n%s", out)
		}
		if again, code := run(withHost(), "--release", rel, "--ah-release", stubRelease); code != 0 || again != out {
			t.Errorf("--dry is not the default: exit %d\n%s", code, again)
		}
	})

	t.Run("go requires a matching confirm", func(t *testing.T) {
		rel := ahFakeRelease(t, ahTestID)
		for _, confirm := range [][]string{nil, {"--confirm", ""}, {"--confirm", "0008-ab12cd3"}, {"--confirm", "0007-ab12cd"}, {"--confirm", ahTestID + " "}} {
			args := append([]string{"--release", rel, "--ah-release", stubRelease, "--go"}, confirm...)
			out, code := run(withHost(), args...)
			if code != 2 || !strings.Contains(out, "--confirm") {
				t.Errorf("%v: exit %d\n%s", confirm, code, out)
			}
			if got := ahCalls(t, calls); got != nil {
				t.Errorf("%v: something was called before the confirm: %v", confirm, got)
			}
		}

		out, code := run(withHost(), "--release", rel, "--ah-release", stubRelease, "--go", "--confirm", ahTestID)
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if got := names(); !slices.Equal(got, []string{"ah-release", "rsync", "ssh"}) {
			t.Fatalf("calls: %v", got)
		}
		if got := called("rsync")[0]; strings.Contains(got, "[-n]") || !strings.Contains(got, "[--delete] [--itemize-changes]") {
			t.Errorf("rsync: %s", got)
		}
		wantSSH := "ssh [-o] [BatchMode=yes] [--] [" + ahTestHost + "] [sudo /usr/local/sbin/ah-switch --from \"$HOME/incoming/" + ahTestID + "\" " + ahTestID + "]"
		if got := called("ssh")[0]; got != wantSSH {
			t.Errorf("ssh:\n got %s\nwant %s", got, wantSSH)
		}
		if !strings.Contains(out, "switched") {
			t.Errorf("the go output lacks the verdict:\n%s", out)
		}
	})

	t.Run("a failed step stops the later ones", func(t *testing.T) {
		rel := ahFakeRelease(t, ahTestID)
		args := []string{"--release", rel, "--ah-release", stubRelease, "--go", "--confirm", ahTestID}
		for _, c := range []struct {
			fail string
			want []string
		}{
			{"ah-release", []string{"ah-release"}},
			{"rsync", []string{"ah-release", "rsync"}},
			{"ssh", []string{"ah-release", "rsync", "ssh"}},
		} {
			out, code := run(withHost("STUB_FAIL="+c.fail), args...)
			if code != 1 || strings.Contains(out, "switched;") {
				t.Errorf("%s: exit %d\n%s", c.fail, code, out)
			}
			if got := names(); !slices.Equal(got, c.want) {
				t.Errorf("%s: calls %v, want %v", c.fail, got, c.want)
			}
		}
		// A failing dry rsync stops the dry run too.
		if out, code := run(withHost("STUB_FAIL=rsync"), "--release", rel, "--ah-release", stubRelease, "--dry"); code != 1 {
			t.Errorf("dry rsync failure: exit %d\n%s", code, out)
		}
	})

	t.Run("skip-check and the release tool", func(t *testing.T) {
		rel := ahFakeRelease(t, ahTestID)
		out, code := run(withHost(), "--release", rel, "--skip-check", "--dry")
		if code != 0 || !strings.Contains(out, "warning: --skip-check") {
			t.Errorf("skip-check: exit %d\n%s", code, out)
		}
		if got := names(); !slices.Equal(got, []string{"rsync"}) {
			t.Errorf("skip-check calls: %v", got)
		}
		// Without --ah-release the tool comes from PATH: found there, it is run.
		if out, code := run(withHost(), "--release", rel, "--dry"); code != 0 {
			t.Errorf("ah-release in PATH: exit %d\n%s", code, out)
		}
		if got := names(); !slices.Equal(got, []string{"ah-release", "rsync"}) {
			t.Errorf("ah-release in PATH: calls %v", got)
		}
		// With none there, or a missing file named, the driver refuses.
		noTool := []string{"PATH=/usr/bin:/bin", "HOME=" + workDir, "AUTHORITY_HOST_SSH=" + ahTestHost}
		if out, code := run(noTool, "--release", rel, "--dry"); code != 2 || !strings.Contains(out, "ah-release is not in PATH") {
			t.Errorf("no ah-release: exit %d\n%s", code, out)
		}
		if got := ahCalls(t, calls); got != nil {
			t.Errorf("no ah-release: called %v", got)
		}
		if out, code := run(withHost(), "--release", rel, "--ah-release", filepath.Join(workDir, "missing"), "--dry"); code != 2 {
			t.Errorf("missing --ah-release: exit %d\n%s", code, out)
		}
	})

	t.Run("self-test computes without running anything", func(t *testing.T) {
		rel := ahFakeRelease(t, ahTestID)
		out, code := run(env(), "--release", rel, "--incoming-base", "/srv/incoming", "--self-test")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if got := ahCalls(t, calls); got != nil {
			t.Fatalf("self-test called %v", got)
		}
		for _, want := range []string{
			"step 1: ah-release check --no-noaicheck " + rel,
			"rsync -a -n --delete --itemize-changes -e 'ssh -o BatchMode=yes' -- " + rel + "/ <the authority host>:/srv/incoming/" + ahTestID + "/",
			"step 3: ssh -o BatchMode=yes -- <the authority host> ",
			"/usr/local/sbin/ah-switch --from /srv/incoming/" + ahTestID + " " + ahTestID,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the self-test output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("incoming base", func(t *testing.T) {
		rel := ahFakeRelease(t, ahTestID)
		for _, bad := range []string{
			"relative/path", "", "/", "/opt/authority-host", "/opt/authority-host/releases", "/opt/authority-host/incoming",
			"/a/../b", "/a/./b", "/a//b", "/a b", "/a;b", "/a/", "~", "~/", "~/../x", "~user/x", "/a$HOME", "/a'b",
		} {
			out, code := run(withHost(), "--release", rel, "--ah-release", stubRelease, "--incoming-base", bad, "--dry")
			if code != 2 || !strings.Contains(out, "--incoming-base must be") {
				t.Errorf("%q: exit %d\n%s", bad, code, out)
			}
			if got := ahCalls(t, calls); got != nil {
				t.Errorf("%q: called %v", bad, got)
			}
		}
		out, code := run(withHost(), "--release", rel, "--ah-release", stubRelease, "--incoming-base", "/srv/in-1/x_y", "--dry")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if got := called("rsync")[0]; !strings.HasSuffix(got, "["+ahTestHost+":/srv/in-1/x_y/"+ahTestID+"/]") {
			t.Errorf("rsync: %s", got)
		}
	})

	t.Run("check never runs the no-AI check on the development host", func(t *testing.T) {
		rel := ahFakeRelease(t, ahTestID)
		for _, mode := range [][]string{{"--dry"}, {"--go", "--confirm", ahTestID}} {
			out, code := run(withHost(), append([]string{"--release", rel, "--ah-release", stubRelease}, mode...)...)
			if code != 0 {
				t.Fatalf("%v: exit %d\n%s", mode, code, out)
			}
			if got := called("ah-release"); len(got) != 1 || got[0] != "ah-release [check] [--no-noaicheck] ["+rel+"]" {
				t.Errorf("%v: ah-release calls %v", mode, got)
			}
			if strings.Contains(out, "ah-release check "+rel) {
				t.Errorf("%v: the plan shows a check without --no-noaicheck:\n%s", mode, out)
			}
		}
	})

	t.Run("no step executes a file from the incoming folder", func(t *testing.T) {
		// The incoming folder belongs to the deploying user, so a program in
		// it must never be run as root before the switch has verified the
		// copy: the only command sent to the host is the installed switch.
		// The switch itself runs the no-AI check, on the verified copy.
		rel := ahFakeRelease(t, ahTestID)
		wantSwitch := "sudo /usr/local/sbin/ah-switch --from \"$HOME/incoming/" + ahTestID + "\" " + ahTestID
		for _, mode := range [][]string{{"--dry"}, {"--go", "--confirm", ahTestID}, {"--self-test"}} {
			out, code := run(withHost(), append([]string{"--release", rel, "--ah-release", stubRelease}, mode...)...)
			if code != 0 {
				t.Fatalf("%v: exit %d\n%s", mode, code, out)
			}
			if strings.Contains(out, "ah-noaicheck") || strings.Contains(out, "remote no-AI check") {
				t.Errorf("%v: the plan runs a program of the incoming folder:\n%s", mode, out)
			}
			for _, line := range called("ssh") {
				if want := "ssh [-o] [BatchMode=yes] [--] [" + ahTestHost + "] [" + wantSwitch + "]"; line != want {
					t.Errorf("%v: an ssh call other than the switch:\n got %s\nwant %s", mode, line, want)
				}
			}
		}
		// --go is exactly the copy and then the switch.
		if _, code := run(withHost(), "--release", rel, "--ah-release", stubRelease, "--go", "--confirm", ahTestID); code != 0 {
			t.Fatalf("go: exit %d", code)
		}
		if got := names(); !slices.Equal(got, []string{"ah-release", "rsync", "ssh"}) {
			t.Errorf("go calls: %v", got)
		}
		// The removed flag is refused, before anything runs.
		out, code := run(withHost(), "--release", rel, "--ah-release", stubRelease, "--go", "--confirm", ahTestID, "--remote-check")
		if code != 2 || !strings.Contains(out, "usage:") {
			t.Errorf("--remote-check: exit %d\n%s", code, out)
		}
		if got := ahCalls(t, calls); got != nil {
			t.Errorf("--remote-check: something was called: %v", got)
		}
		// The self-test of an absolute base shows the switch alone, as step 3.
		out, code = run(env(), "--release", rel, "--incoming-base", "/srv/incoming", "--self-test")
		if code != 0 {
			t.Fatalf("self-test: exit %d\n%s", code, out)
		}
		want := "step 3: ssh -o BatchMode=yes -- <the authority host> 'sudo /usr/local/sbin/ah-switch --from /srv/incoming/" + ahTestID + " " + ahTestID + "'"
		if !strings.Contains(out, want) || strings.Contains(out, "step 4") {
			t.Errorf("the self-test output lacks %q:\n%s", want, out)
		}
	})

	t.Run("a hostile release path is one argument", func(t *testing.T) {
		rel := filepath.Join(t.TempDir(), "a dir; touch pwned '$(x)'")
		release := filepath.Join(rel, ahTestID)
		if err := os.MkdirAll(release, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(release, "MANIFEST"), []byte("id: "+ahTestID+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := run(withHost(), "--release", release, "--ah-release", stubRelease, "--dry")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if got := called("ah-release")[0]; got != "ah-release [check] [--no-noaicheck] ["+release+"]" {
			t.Errorf("ah-release: %s", got)
		}
		if _, err := os.Stat(filepath.Join(workDir, "pwned")); err == nil {
			t.Error("the path was run as a command")
		}
	})
}

// testAuthorityHostDeployHelp checks that the driver's own help text is as
// honest as the manual about what --dry and --self-test start.
func testAuthorityHostDeployHelp(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), ahDeployScript))
	if err != nil {
		t.Fatal(err)
	}
	flat := strings.Join(strings.Fields(strings.ReplaceAll(string(data), "\n#", "\n")), " ")
	for _, want := range []string{
		"contacts the host, read-only (ssh and rsync -n)",
		"starts nothing",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("deploy.sh's help does not say %q", want)
		}
	}
}

// ahFlat joins the lines of a Markdown text into one and drops the code marks.
func ahFlat(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " ")
}

// testAuthorityHostDeployDoc checks the manual: its links resolve and it names
// the steps, the scripts' flags and the owner-only items.
func testAuthorityHostDeployDoc(t *testing.T) {
	t.Run("help", testAuthorityHostDeployHelp)
	path := filepath.Join(root(t), "deploy/authority-host/DEPLOY.md")
	if err := checkDoc(root(t), path); err != nil {
		t.Error(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"CGO_ENABLED=0", "ah-release build", "host-bootstrap.sh --dry", "host-bootstrap.sh --apply",
		"--switch-sha256", "--link-caddy", "deploy.sh --dry", "deploy.sh --go", "--confirm",
		"ah-switch status", "/healthz", "--rollback-to previous", "Owner-only items", "signers file",
		"When a step fails", "AUTHORITY_HOST_SSH",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("DEPLOY.md does not mention %q", want)
		}
	}
	// "Who may switch": the sudo rule is limited to the switch form, and the
	// owner-only verbs stay with the owner's own root session. Wrapped text is
	// compared as one line, without code marks.
	flat := ahFlat(string(data))
	// --dry reads the host through rsync -n (ssh and a remote rsync, read-only);
	// only --self-test starts nothing.
	for _, want := range []string{
		"--dry contacts the host, read-only (ssh and rsync -n)",
		"--self-test starts nothing",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("DEPLOY.md does not say %q", want)
		}
	}
	if strings.Contains(flat, "never call ssh") || strings.Contains(flat, "never calls ssh") {
		t.Error("DEPLOY.md says that a dry run never calls ssh")
	}
	for _, want := range []string{
		"NOPASSWD: /usr/local/sbin/ah-switch ^--from /home/DEPLOYUSER/incoming/",
		"sudo -l -U DEPLOYUSER",
		"inhibit-clear, ack and recover run only from the owner's own root session",
		"NOPASSWD: ALL",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("DEPLOY.md does not say %q", want)
		}
	}
	// The first deploy: the shipped release restarts caddy in the switch and
	// needs /healthz from it, so the Caddyfile link comes before the first
	// switch (step 3, not after step 4), and the reported outcome is
	// acknowledged from the owner's session before a later different switch
	// or rollback, which the switch refuses while an outcome is owed.
	section := func(from, to string) string {
		i := strings.Index(flat, from)
		j := strings.Index(flat, to)
		if i < 0 || j < i {
			t.Fatalf("DEPLOY.md lacks the sections %q to %q in order", from, to)
		}
		return flat[i:j]
	}
	if !strings.Contains(section("### 3.", "### 4."), "Link the Caddyfile before the first switch") {
		t.Error("DEPLOY.md step 3 does not put the Caddyfile link before the first switch")
	}
	if strings.Contains(flat, "Do not use it before the first switch") || strings.Contains(flat, "link it now") {
		t.Error("DEPLOY.md still has the Caddyfile linked after the first switch")
	}
	if !strings.Contains(section("### 5.", "### 6."), "ah-switch ack --asked") {
		t.Error("DEPLOY.md step 5 does not acknowledge the outcome from the owner's session")
	}
	rollback := section("### 6.", "## Owner-only items")
	if ack, back := strings.Index(rollback, "ah-switch ack --asked"), strings.Index(rollback, "--rollback-to previous"); ack < 0 || back < 0 || ack > back {
		t.Error("DEPLOY.md step 6 does not acknowledge the outcome before the rollback")
	}
	t.Run("bootstrap copies are root's own", func(t *testing.T) { testAuthorityHostRootCopies(t, section) })
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "NOPASSWD: /usr/local/sbin/ah-switch") {
			t.Errorf("DEPLOY.md grants the whole switch program: %s", line)
		}
	}
	readme, err := os.ReadFile(filepath.Join(root(t), "deploy/authority-host/README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ahFlat(string(readme)), "inhibit-clear, ack and recover run only from the owner's own root session") {
		t.Error("README.md does not say that inhibit-clear, ack and recover are the owner's own")
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "AUTHORITY_HOST_SSH=") && !strings.Contains(line, "USER@HOST") {
			t.Errorf("DEPLOY.md sets the host variable to a value: %s", line)
		}
	}
}

// testAuthorityHostRootCopies checks that the manual never has root read,
// judge or run a file of the deploying user's incoming folder in place: the
// copies go into a new root-only folder first, their hashes are compared with
// the ones typed from the build host, and only the copies run, even for
// --dry. The baseline procedure does the same for the release's checker.
func testAuthorityHostRootCopies(t *testing.T, section func(from, to string) string) {
	step1 := section("### 1.", "### 2.")
	if !strings.Contains(step1, "sha256sum build/authority-host/ah-switch deploy/authority-host/bootstrap/host-bootstrap.sh") {
		t.Error("DEPLOY.md step 1 does not record a hash of the bootstrap script as well as of the switch")
	}
	step3 := section("### 3.", "### 4.")
	mk := strings.Index(step3, "mkdir -m 0700 /root/ah-bootstrap")
	cp := strings.Index(step3, "cp /home/DEPLOYUSER/incoming/host-bootstrap.sh")
	sum := strings.Index(step3, "sha256sum /root/ah-bootstrap/host-bootstrap.sh /root/ah-bootstrap/ah-switch")
	dry := strings.Index(step3, "sh /root/ah-bootstrap/host-bootstrap.sh --dry")
	if mk < 0 || cp < mk || sum < cp || dry < sum {
		t.Errorf("DEPLOY.md step 3 does not copy into a new root-only folder, hash the copies, then run them (positions %d %d %d %d)", mk, cp, sum, dry)
	}
	for _, bad := range []string{"read the script, copy both files", "sh /root/host-bootstrap.sh", "--switch-bin /root/ah-switch"} {
		if strings.Contains(step3, bad) {
			t.Errorf("DEPLOY.md step 3 still has %q", bad)
		}
	}
	readme, err := os.ReadFile(filepath.Join(root(t), "deploy/authority-host/README.md"))
	if err != nil {
		t.Fatal(err)
	}
	rf := ahFlat(string(readme))
	i := strings.Index(rf, "### Filling the allowlists")
	j := strings.Index(rf, "### The source tree of the first release")
	if i < 0 || j < i {
		t.Fatal("README.md lacks the section Filling the allowlists")
	}
	fill := rf[i:j]
	hash := strings.Index(fill, "sha256sum RELEASE/SHA256SUMS")
	verify := strings.Index(fill, "sha256sum -c SHA256SUMS")
	run := strings.Index(fill, "sudo RELEASE/bin/ah-noaicheck")
	if !strings.Contains(fill, "mkdir -m 0700 /root/ah-baseline") || hash < 0 || verify < hash || run < verify {
		t.Errorf("README.md baseline does not verify a root-owned copy of the release before running its checker (positions %d %d %d)", hash, verify, run)
	}
}
