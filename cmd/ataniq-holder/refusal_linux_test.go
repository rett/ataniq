//go:build linux

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadholder/session"
)

// nothingStarted checks that a refused start left nothing: no state file, no
// event log, no sockets, no final record, and no agent output.
func nothingStarted(t *testing.T, r *rig) {
	t.Helper()
	for _, p := range []string{
		filepath.Join(r.state, StateFileName), filepath.Join(r.state, eventlog.FileName),
		filepath.Join(r.state, session.FinalName),
		filepath.Join(r.runtime, serve.AttachSocket), filepath.Join(r.runtime, serve.ControlSocket),
	} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("a refused start left %s", p)
		}
	}
}

// Each start-up refusal exits 2, says why on stderr, never echoes an argument,
// and leaves nothing behind.
func TestStartUpRefusals(t *testing.T) {
	const marker = "ARGMARKER-31337"
	tests := []struct {
		name  string
		setup func(r *rig, a *argSpec)
		want  string // in stderr
	}{
		{"no --scratch", func(r *rig, a *argSpec) { a.scratch = false }, "only a scratch agent"},
		{"no --scratch and nothing else", func(r *rig, a *argSpec) { a.scratch = false; a.flags = nil }, "sandbox is not built"},
		{"legacy_tiocsti is 1", func(r *rig, a *argSpec) { r.setTIOCSTI("1\n") }, "legacy_tiocsti is 1"},
		{"legacy_tiocsti is unknown", func(r *rig, a *argSpec) { r.setTIOCSTI("2\n") }, "cannot be read as 0 or 1"},
		{"legacy_tiocsti is missing", func(r *rig, a *argSpec) { r.setTIOCSTI("") }, "cannot be read as 0 or 1"},
		{"state folder with a loose mode", func(r *rig, a *argSpec) { os.Chmod(r.state, 0o755) }, "state folder"},
		{"state folder with a group mode", func(r *rig, a *argSpec) { os.Chmod(r.state, 0o770) }, "0770"},
		{"runtime folder with a loose mode", func(r *rig, a *argSpec) { os.Chmod(r.runtime, 0o750) }, "runtime folder"},
		{"state folder missing", func(r *rig, a *argSpec) { a.set("state-dir", filepath.Join(r.root, "absent")) }, "does not exist"},
		{"runtime folder is a file", func(r *rig, a *argSpec) {
			f := filepath.Join(r.root, "file")
			os.WriteFile(f, nil, 0o700)
			a.set("runtime-dir", f)
		}, "not a real directory"},
		{"state folder is a link", func(r *rig, a *argSpec) {
			l := filepath.Join(r.root, "link")
			os.Symlink(r.state, l)
			a.set("state-dir", l)
		}, "not a real directory"},
		{"tree is not a directory", func(r *rig, a *argSpec) { a.set("tree", filepath.Join(r.root, "absent")) }, "work tree"},
		{"a signal the agent needs is ignored", func(r *rig, a *argSpec) { r.ignored = []syscall.Signal{syscall.SIGPIPE} }, "ignores SIGPIPE"},
		{"SIGHUP ignored", func(r *rig, a *argSpec) { r.ignored = []syscall.Signal{syscall.SIGUSR1, syscall.SIGHUP} }, "ignores SIGHUP"},
		{"SIGINT ignored", func(r *rig, a *argSpec) { r.ignored = []syscall.Signal{syscall.SIGINT} }, "ignores SIGINT"},
		{"SIGTERM ignored", func(r *rig, a *argSpec) { r.ignored = []syscall.Signal{syscall.SIGTERM} }, "ignores SIGTERM"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			a := r.spec("raw; say SHOULD-NOT-RUN; wait")
			a.agent = append(a.agent, marker)
			tc.setup(r, a)
			code := r.runNow(a.build())
			if code != exitRefuse {
				t.Fatalf("exit %d, want %d; stderr: %s", code, exitRefuse, r.errbuf.String())
			}
			if !strings.Contains(r.errbuf.String(), tc.want) {
				t.Errorf("stderr %q does not say %q", r.errbuf.String(), tc.want)
			}
			if strings.Contains(r.errbuf.String(), marker) {
				t.Errorf("stderr echoes an argument: %q", r.errbuf.String())
			}
			if strings.Contains(tc.name, "state folder") || strings.Contains(tc.name, "runtime folder") || strings.Contains(tc.name, "link") {
				return // the folder itself is what is wrong
			}
			nothingStarted(t, r)
		})
	}
}

// The usage errors: a wrong epoch, a relative ARGV[0], a missing flag. Each
// prints a short diagnostic and the usage, exits 2 and echoes no argument.
func TestUsageErrors(t *testing.T) {
	const marker = "ARGMARKER-4242"
	tests := []struct {
		name  string
		setup func(a *argSpec)
		want  string
	}{
		{"epoch 0", func(a *argSpec) { a.set("epoch", "0") }, "--epoch"},
		{"epoch negative", func(a *argSpec) { a.set("epoch", "-1") }, "--epoch"},
		{"epoch text", func(a *argSpec) { a.set("epoch", marker) }, "--epoch"},
		{"epoch with a plus", func(a *argSpec) { a.set("epoch", "+3") }, "--epoch"},
		{"epoch with a leading zero", func(a *argSpec) { a.set("epoch", "07") }, "--epoch"},
		{"epoch too large", func(a *argSpec) { a.set("epoch", "18446744073709551616") }, "--epoch"},
		{"epoch empty", func(a *argSpec) { a.set("epoch", "") }, "--epoch"},
		{"relative ARGV[0]", func(a *argSpec) { a.agent = []string{"bin/" + marker, "x"} }, "absolute path"},
		{"bare ARGV[0]", func(a *argSpec) { a.agent = []string{marker} }, "absolute path"},
		{"no agent", func(a *argSpec) { a.agent = nil }, "agent command"},
		{"cols too small", func(a *argSpec) { a.set("cols", "19") }, "--cols"},
		{"rows too large", func(a *argSpec) { a.set("rows", "201") }, "--rows"},
		{"cols text", func(a *argSpec) { a.set("cols", marker) }, "--cols"},
		{"role with a control byte", func(a *argSpec) { a.set("role", "a\x07"+marker) }, "--role"},
		{"role empty", func(a *argSpec) { a.set("role", "") }, "--role"},
		{"role too long", func(a *argSpec) { a.set("role", strings.Repeat("r", 129)) }, "--role"},
		{"runtime too long", func(a *argSpec) { a.set("runtime", strings.Repeat("r", 65)) }, "--runtime"},
		{"runtime version too long", func(a *argSpec) { a.set("runtime-version", strings.Repeat("1", 65)) }, "--runtime-version"},
		{"launch intent with a space", func(a *argSpec) { a.set("launch-intent", "a b"+marker) }, "--launch-intent"},
		{"profile digest with a quote", func(a *argSpec) { a.set("profile-digest", `a"`+marker) }, "--profile-digest"},
		{"relative tree", func(a *argSpec) { a.set("tree", "tree-"+marker) }, "--tree"},
		{"relative state folder", func(a *argSpec) { a.set("state-dir", "state-"+marker) }, "--state-dir"},
		{"relative role lock", func(a *argSpec) { a.set("role-lock", "lock-"+marker) }, "--role-lock"},
		{"same state and runtime folder", func(a *argSpec) { a.set("runtime-dir", a.flagValue("state-dir")+"/") }, "different folders"},
		{"env without equals", func(a *argSpec) { a.env = append(a.env, "NOEQUALS"+marker) }, "--env"},
		{"env with a bad key", func(a *argSpec) { a.env = append(a.env, "1BAD="+marker) }, "--env"},
		{"env with a control byte", func(a *argSpec) { a.env = append(a.env, "K=a\nb"+marker) }, "--env"},
		{"env key twice", func(a *argSpec) { a.env = append(a.env, "K=1", "K=2") }, "twice"},
		{"flag twice", func(a *argSpec) { a.flags = append(a.flags, [2]string{"role", marker}) }, "--role was given twice"},
		{"unknown flag", func(a *argSpec) { a.flags = append(a.flags, [2]string{"nonesuch-" + marker, "1"}) }, "invalid flags"},
		{"positional argument", func(a *argSpec) { a.extraBefore = []string{marker} }, "expected flags"},
	}
	for _, name := range valueFlags {
		name := name
		tests = append(tests, struct {
			name  string
			setup func(a *argSpec)
			want  string
		}{"missing --" + name, func(a *argSpec) { a.drop(name) }, "--" + name + " is required"})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			a := r.spec("raw; say SHOULD-NOT-RUN; wait")
			tc.setup(a)
			code := r.runNow(a.build())
			errText := r.errbuf.String()
			if code != exitRefuse {
				t.Fatalf("exit %d, want %d; stderr: %s", code, exitRefuse, errText)
			}
			if !strings.Contains(errText, tc.want) {
				t.Errorf("stderr %q does not say %q", errText, tc.want)
			}
			if !strings.Contains(errText, "Usage:") {
				t.Errorf("no usage in %q", errText)
			}
			if strings.Contains(errText, marker) {
				t.Errorf("stderr echoes an argument: %q", errText)
			}
			if i := strings.Index(errText, "Usage:"); i > 400 {
				t.Errorf("the diagnostic before the usage is %d bytes", i)
			}
			nothingStarted(t, r)
		})
	}
}

// -h prints the usage and exits 0 without starting anything.
func TestHelp(t *testing.T) {
	for _, h := range []string{"-h", "--help"} {
		r := newRig(t)
		if code := r.runNow([]string{h}); code != exitOK {
			t.Errorf("%s: exit %d", h, code)
		}
		if !strings.Contains(r.errbuf.String(), "Usage:") {
			t.Errorf("%s: %q", h, r.errbuf.String())
		}
		nothingStarted(t, r)
	}
}

// The flag set the command accepts is the fixed command line the launcher
// builds, as a literal list: a drift in either fails here.
func TestFlagSetIsTheFixedCommandLine(t *testing.T) {
	want := []string{
		"cols", "daemon-lock", "env", "epoch", "launch-intent", "profile-digest", "role",
		"role-lock", "rows", "runtime", "runtime-dir", "runtime-version", "scratch",
		"state-dir", "tree",
	}
	got := flagNames()
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the flag set is\n%v\nwant\n%v", got, want)
	}
	// Every flag but --env is required.
	if len(valueFlags) != len(want)-2 {
		t.Errorf("%d required value flags, want %d", len(valueFlags), len(want)-2)
	}
}

// The launcher's own source names the same flags: ataniq-scratch builds the
// holder's argument vector in holderArgs, and a flag added or renamed there
// without here (or the other way) fails this test. The packages are two
// commands, so the source is read.
func TestFlagSetMatchesTheLaunchersCommandLine(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "ataniq-scratch", "launch.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	i := strings.Index(text, "func holderArgs(")
	if i < 0 {
		t.Fatal("holderArgs is not in the launcher's source")
	}
	body := text[i:]
	if j := strings.Index(body, "\n}\n"); j >= 0 {
		body = body[:j]
	}
	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`"--([a-z][a-z-]*)"`).FindAllStringSubmatch(body, -1) {
		found[m[1]] = true
	}
	var launcher []string
	for n := range found {
		launcher = append(launcher, n)
	}
	sort.Strings(launcher)
	got := flagNames()
	sort.Strings(got)
	if strings.Join(launcher, " ") != strings.Join(got, " ") {
		t.Errorf("the launcher builds\n%v\nthe holder accepts\n%v", launcher, got)
	}
}
