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

	t.Run("dry never calls ssh", func(t *testing.T) {
		rel := ahFakeRelease(t, ahTestID)
		out, code := run(withHost(), "--release", rel, "--ah-release", stubRelease, "--dry")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if got := called("ssh"); got != nil {
			t.Fatalf("a dry run called ssh: %v", got)
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

// testAuthorityHostDeployDoc checks the manual: its links resolve and it names
// the steps, the scripts' flags and the owner-only items.
func testAuthorityHostDeployDoc(t *testing.T) {
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
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "AUTHORITY_HOST_SSH=") && !strings.Contains(line, "USER@HOST") {
			t.Errorf("DEPLOY.md sets the host variable to a value: %s", line)
		}
	}
}
