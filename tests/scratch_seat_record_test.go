package tests

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Tests of prepare.sh --record-trial, the owner's record of the phone trial's
// unit before step 1 (docs/scratch-seat-proof.md, step f). The user manager
// is the systemctl stub of the kit, which answers "show" only, and the main
// process is a fake /proc entry in the kit's folder (PREPARE_PROC). The
// restart script it writes for a transient unit is run here only with a
// systemd-run stub that records its arguments.

const scratchSeatInvocation = "0123456789abcdef0123456789abcdef"

// procSays writes the fake /proc entry of a process: its cmdline, as given.
func (k scratchSeatKit) procSays(t *testing.T, pid string, cmdline []byte) {
	t.Helper()
	dir := filepath.Join(k.dir, "proc", pid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), cmdline, 0o600); err != nil {
		t.Fatal(err)
	}
}

// procEnv is the environment that points the script at the kit's /proc.
func (k scratchSeatKit) procEnv() []string {
	return []string{"PREPARE_PROC=" + filepath.Join(k.dir, "proc")}
}

// nulJoin is a command line as /proc/PID/cmdline holds it.
func nulJoin(args []string) []byte {
	var b []byte
	for _, a := range args {
		b = append(b, a...)
		b = append(b, 0)
	}
	return b
}

// transientUnit is what the user manager says of a running transient unit
// made by "systemd-run --user --unit NAME --description TEXT --collect":
// the systemctl stub's lines, with the changes given (PROP VALUE).
func (k scratchSeatKit) transientUnit(prog, desc string, change ...string) []string {
	props := map[string]string{
		"Id": "sample-trial.service", "Description": desc, "Transient": "yes", "UnitFileState": "transient",
		"FragmentPath": filepath.Join(k.dir, "run", "systemd", "transient", "sample-trial.service"),
		"CollectMode":  "inactive-or-failed", "LoadState": "loaded", "ActiveState": "active", "SubState": "running",
		"InvocationID": scratchSeatInvocation, "MainPID": "4242", "Restart": "no",
		"ExecStart": "{ path=" + prog + " ; argv[]=" + prog + " run ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=4242 ; code=(null) ; status=0/0 }",
	}
	return unitLines(props, change...)
}

// fileUnit is what the user manager says of a running unit of a unit file
// in the user's configuration folder.
func (k scratchSeatKit) fileUnit(change ...string) []string {
	props := map[string]string{
		"Id": "sample-trial.service", "Description": "Sample trial", "Transient": "no", "UnitFileState": "disabled",
		"FragmentPath": filepath.Join(k.dir, "config", "systemd", "user", "sample-trial.service"),
		"CollectMode":  "inactive", "LoadState": "loaded", "ActiveState": "active", "SubState": "running",
		"InvocationID": scratchSeatInvocation, "MainPID": "4243", "Restart": "no",
		"ExecStart": "{ path=/srv/sample/bin/ataniqd ; argv[]=/srv/sample/bin/ataniqd run ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=4243 ; code=(null) ; status=0/0 }",
	}
	return unitLines(props, change...)
}

func unitLines(props map[string]string, change ...string) []string {
	for _, c := range change {
		p, v, _ := strings.Cut(c, " ")
		props[p] = v
	}
	var lines []string
	for p, v := range props {
		lines = append(lines, "sample-trial.service "+p+" "+v)
	}
	slices.Sort(lines)
	return lines
}

// recordOutputs are the names --record-trial may write in a root.
func recordOutputs(r string) []string {
	return []string{"trial-unit", "evidence/trial-unit-before.txt", "trial-restart.sh",
		"trial-unit.new", "evidence/trial-unit-before.txt.new", "trial-restart.sh.new"}
}

// The record of a transient unit: the evidence of the unit as it runs, the
// kind record and the restart script, which gives systemd-run the unit's
// name, description, --collect and the command line of the main process,
// byte for byte, whatever an argument holds (a space, a quote, a $, a
// backslash, a tab, an empty one). Nothing but systemctl's "show" runs, and
// a second record is refused with nothing changed.
func TestScratchSeatPrepareRecordTrialTransient(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 0 {
		t.Fatalf("--write: exit %d:\n%s", code, out)
	}
	prog := "/srv/sample/bin/ataniqd"
	argv := []string{prog, "run", "--state-dir", "/srv/sample/a b", "--x=it's", "$HOME", "${HOME}", `back\slash`, `"dq"`, "",
		"tab\there", "`id`", "%s%d%%", "-", "'", "''", "end\\"}
	desc := `Phone trial: it's "$HOME" \ ok`
	k.unitsSay(t, k.transientUnit(prog, desc)...)
	k.procSays(t, "4242", nulJoin(argv))
	rec := []string{"--record-trial", "--state", k.state, "--root", r, "--unit", "sample-trial.service"}
	out, code := k.run(t, k.procEnv(), rec...)
	if code != 0 || !strings.Contains(out, "recorded the trial's unit: transient") || !strings.Contains(out, "17 arguments") {
		t.Fatalf("--record-trial: exit %d:\n%s", code, out)
	}
	k.onlyShows(t)
	for rel, mode := range map[string]os.FileMode{"trial-unit": 0o600, "evidence/trial-unit-before.txt": 0o600, "trial-restart.sh": 0o700} {
		st, err := os.Lstat(filepath.Join(r, rel))
		if err != nil || st.Mode() != mode {
			t.Errorf("%s: %v %v, want %v", rel, st, err, mode)
		}
	}
	before := string(mustRead(t, filepath.Join(r, "evidence", "trial-unit-before.txt")))
	for _, p := range []string{"Id", "Description", "Transient", "UnitFileState", "FragmentPath", "CollectMode", "LoadState", "ActiveState",
		"SubState", "TriggeredBy", "WantedBy", "RequiredBy", "BoundBy", "InvocationID", "MainPID", "ExecStart"} {
		if !strings.Contains("\n"+before, "\n"+p+"=") {
			t.Errorf("the evidence lacks %s:\n%s", p, before)
		}
	}
	if !strings.Contains(before, "\nTransient=yes\n") || !strings.Contains(before, "\nMainPID=4242\n") || !strings.Contains(before, "\nInvocationID="+scratchSeatInvocation+"\n") {
		t.Errorf("the evidence:\n%s", before)
	}
	kind := string(mustRead(t, filepath.Join(r, "trial-unit")))
	if kind != "trial-unit 1\nunit sample-trial.service\nkind transient\ninvocation "+scratchSeatInvocation+"\nrestart "+filepath.Join(r, "trial-restart.sh")+"\n" {
		t.Errorf("the kind record: %q", kind)
	}
	script := string(mustRead(t, filepath.Join(r, "trial-restart.sh")))
	if !strings.HasPrefix(script, "#!/bin/sh\n") || !strings.Contains(script, "\nset -eu\n") || !strings.Contains(script, "exec systemd-run --user --unit \"$name\" --description ") {
		t.Errorf("the restart script:\n%s", script)
	}

	// The script, run with a systemd-run stub that records its arguments,
	// gives exactly the unit's name, description, --collect and command line.
	stubs := filepath.Join(k.dir, "restart-stubs")
	got := filepath.Join(k.dir, "systemd-run-args")
	if err := os.Mkdir(stubs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stubs, "systemd-run"), []byte("#!/bin/sh\nfor a; do printf '%s\\0' \"$a\"; done >'"+got+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", filepath.Join(r, "trial-restart.sh"))
	cmd.Env = []string{"PATH=" + stubs + ":/usr/bin:/bin", "HOME=/nonexistent"}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the restart script with a stub: %v\n%s", err, b)
	}
	want := append([]string{"--user", "--unit", "sample-trial.service", "--description", desc, "--collect", "--"}, argv...)
	if b := mustRead(t, got); !bytes.Equal(b, nulJoin(want)) {
		t.Errorf("systemd-run got %q, want %q", strings.Split(string(b), "\x00"), want)
	}

	// --status names the kind and the script.
	if out, code := k.run(t, nil, "--status", "--state", k.state, "--root", r); code != 0 ||
		!strings.Contains(out, "trial-unit: unit sample-trial.service, transient (made by systemd-run; step 15 re-creates it with "+filepath.Join(r, "trial-restart.sh")+")") {
		t.Errorf("--status: exit %d:\n%s", code, out)
	}
	// A second record is refused, and the first is kept as it is.
	files := scratchSeatFiles(t, r)
	if out, code := k.run(t, k.procEnv(), rec...); code != 1 || !strings.Contains(out, "is there already") {
		t.Errorf("a second --record-trial: exit %d:\n%s", code, out)
	}
	if !sameScratchSeatFiles(files, scratchSeatFiles(t, r)) {
		t.Error("a refused --record-trial changed the root")
	}
	// Without a collect mode of inactive-or-failed, no --collect.
	k2 := newScratchSeatKit(t)
	r2 := filepath.Join(k2.dir, "proof")
	if out, code := k2.run(t, nil, k2.args("--write", r2, false)...); code != 0 {
		t.Fatalf("--write: exit %d:\n%s", code, out)
	}
	k2.unitsSay(t, k2.transientUnit(prog, "", "CollectMode inactive")...)
	k2.procSays(t, "4242", nulJoin([]string{prog, "run"}))
	if out, code := k2.run(t, k2.procEnv(), "--record-trial", "--state", k2.state, "--root", r2, "--unit", "sample-trial.service"); code != 0 {
		t.Fatalf("--record-trial: exit %d:\n%s", code, out)
	}
	if s := string(mustRead(t, filepath.Join(r2, "trial-restart.sh"))); !strings.Contains(s, "--unit \"$name\" -- '"+prog+"' 'run'\n") {
		t.Errorf("the restart script without --collect or a description:\n%s", s)
	}
}

// What --record-trial refuses, writing nothing: a unit that is not active or
// not loaded, an alias, a transient unit without a main process (MainPID 0)
// or whose main process runs another program than ExecStart's, a command
// line that cannot be read, is empty, holds a newline in an argument or does
// not end with a NUL, an ExecStart of two commands, a collect mode the
// script cannot give, and a root that has no evidence folder or has swapped.
func TestScratchSeatPrepareRecordTrialRefuses(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 0 {
		t.Fatalf("--write: exit %d:\n%s", code, out)
	}
	prog := "/srv/sample/bin/ataniqd"
	good := nulJoin([]string{prog, "run", "--state-dir", "/srv/sample/state"})
	rec := []string{"--record-trial", "--state", k.state, "--root", r, "--unit", "sample-trial.service"}
	two := "{ path=" + prog + " ; argv[]=" + prog + " run ; pid=4242 } { path=" + prog + " ; argv[]=" + prog + " other ; pid=0 }"
	for _, c := range []struct {
		name    string
		units   []string
		cmdline []byte
		want    string
	}{
		{"MainPID 0", k.transientUnit(prog, "Sample", "MainPID 0"), good, "no main process (MainPID=0)"},
		{"no MainPID", k.transientUnit(prog, "Sample", "MainPID "), good, "no main process"},
		{"another program", k.transientUnit(prog, "Sample"), nulJoin([]string{"/srv/sample/bin/other", "run"}), "not ExecStart's program"},
		{"a newline in an argument", k.transientUnit(prog, "Sample"), nulJoin([]string{prog, "run", "a\nb"}), "newline"},
		{"no cmdline", k.transientUnit(prog, "Sample", "MainPID 4343"), nil, "cannot be read"},
		{"an empty cmdline", k.transientUnit(prog, "Sample"), []byte{}, "is empty"},
		{"a cmdline without its last NUL", k.transientUnit(prog, "Sample"), []byte(prog + "\x00run"), "does not end with a NUL"},
		{"an inactive unit", k.transientUnit(prog, "Sample", "ActiveState inactive"), good, "not active"},
		{"a unit not loaded", k.transientUnit(prog, "Sample", "LoadState not-found"), good, "not loaded"},
		{"an alias", k.transientUnit(prog, "Sample", "Id other.service"), good, "an alias"},
		{"two commands", k.transientUnit(prog, "Sample", "ExecStart "+two), good, "more than one command"},
		{"a collect mode", k.transientUnit(prog, "Sample", "CollectMode sometimes"), good, "CollectMode=sometimes"},
		{"no invocation", k.transientUnit(prog, "Sample", "InvocationID "), good, "no invocation id"},
		{"a transient unit outside the transient folder", k.transientUnit(prog, "Sample", "FragmentPath /srv/sample/unit.service"), good, "not in a transient folder"},
		{"a unit without a file", k.fileUnit("FragmentPath "), good, "no unit file"},
	} {
		os.RemoveAll(filepath.Join(k.dir, "proc"))
		k.unitsSay(t, c.units...)
		if c.cmdline != nil {
			k.procSays(t, "4242", c.cmdline)
		}
		files := scratchSeatFiles(t, r)
		out, code := k.run(t, k.procEnv(), rec...)
		if code != 1 || !strings.Contains(out, c.want) {
			t.Errorf("%s: exit %d, want 1 and %q:\n%s", c.name, code, c.want, out)
		}
		if !sameScratchSeatFiles(files, scratchSeatFiles(t, r)) {
			t.Errorf("%s: the refused record changed the root", c.name)
		}
		for _, rel := range recordOutputs(r) {
			if _, err := os.Lstat(filepath.Join(r, rel)); err == nil {
				t.Errorf("%s: %s was written", c.name, rel)
			}
		}
	}
	k.onlyShows(t)
	// A root without its evidence folder, and one that has swapped.
	k.unitsSay(t, k.transientUnit(prog, "Sample")...)
	k.procSays(t, "4242", good)
	if out, code := k.run(t, k.procEnv(), "--record-trial", "--state", k.state, "--root", filepath.Join(k.dir, "not-there"), "--unit", "sample-trial.service"); code != 1 || !strings.Contains(out, "not a folder made by --write") {
		t.Errorf("a missing root: exit %d:\n%s", code, out)
	}
	if err := os.WriteFile(filepath.Join(r, "swap-record"), []byte("swap-record 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := k.run(t, k.procEnv(), rec...); code != 1 || !strings.Contains(out, "has swapped already") {
		t.Errorf("a root that has swapped: exit %d:\n%s", code, out)
	}
}

// The record of a unit file: its kind and file, the evidence, and no restart
// script; the main process is not read.
func TestScratchSeatPrepareRecordTrialUnitFile(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 0 {
		t.Fatalf("--write: exit %d:\n%s", code, out)
	}
	k.unitsSay(t, k.fileUnit("UnitFileState enabled", "WantedBy default.target")...)
	out, code := k.run(t, k.procEnv(), "--record-trial", "--state", k.state, "--root", r, "--unit", "sample-trial.service")
	if code != 0 || !strings.Contains(out, "recorded the trial's unit: a unit file") {
		t.Fatalf("--record-trial: exit %d:\n%s", code, out)
	}
	frag := filepath.Join(k.dir, "config", "systemd", "user", "sample-trial.service")
	if kind := string(mustRead(t, filepath.Join(r, "trial-unit"))); kind != "trial-unit 1\nunit sample-trial.service\nkind file\ninvocation "+scratchSeatInvocation+"\nfragment "+frag+"\n" {
		t.Errorf("the kind record: %q", kind)
	}
	if _, err := os.Lstat(filepath.Join(r, "trial-restart.sh")); err == nil {
		t.Error("a restart script was written for a unit file")
	}
	if before := string(mustRead(t, filepath.Join(r, "evidence", "trial-unit-before.txt"))); !strings.Contains(before, "\nWantedBy=default.target\n") || !strings.Contains(before, "\nUnitFileState=enabled\n") {
		t.Errorf("the evidence:\n%s", before)
	}
	k.onlyShows(t)
}

// recordTrial runs --record-trial for the root, with the user manager
// saying the lines given and, for a transient unit, a main process that
// runs the sample program.
func (k scratchSeatKit) recordTrial(t *testing.T, r string, units []string) {
	t.Helper()
	k.unitsSay(t, units...)
	k.procSays(t, "4242", nulJoin([]string{"/srv/sample/bin/ataniqd", "run", "--state-dir", k.state}))
	if out, code := k.run(t, k.procEnv(), "--record-trial", "--state", k.state, "--root", r, "--unit", "sample-trial.service"); code != 0 {
		t.Fatalf("--record-trial: exit %d:\n%s", code, out)
	}
}

// --swap-in by the kind of the trial's unit. A transient unit is taken once
// it is gone (inactive and not-found) and refused while it runs or stays
// loaded; a unit file is taken masked, or, when the runtime mask did not
// take and was taken back, without an activation source (the fallback is
// written in the evidence), and refused with one, or with the runtime mask
// still there. What the guard found is in evidence/trial-unit-guard.txt;
// --status and --swap-out name step 15 for the kind.
func TestScratchSeatPrepareSwapInGuard(t *testing.T) {
	const unit = "sample-trial.service "
	mask := func(k scratchSeatKit) func() {
		dir := filepath.Join(k.dir, "run", "systemd", "user")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/dev/null", filepath.Join(dir, "sample-trial.service")); err != nil {
			t.Fatal(err)
		}
		return func() {}
	}
	for _, c := range []struct {
		name      string
		transient bool
		units     []string
		setup     func(k scratchSeatKit) func()
		want      string // the refusal, or the guard's evidence when it is taken
		ok        bool
	}{
		{"a transient unit gone", true, []string{unit + "LoadState not-found"}, nil,
			"kind transient, ActiveState=inactive, LoadState=not-found: gone; only trial-restart.sh makes it again: accepted", true},
		{"a transient unit masked", true, []string{unit + "LoadState masked"}, nil, "kind transient, ActiveState=inactive, LoadState=masked", true},
		{"a transient unit that runs", true, []string{unit + "ActiveState active", unit + "LoadState loaded"}, nil, "is active, not inactive", false},
		{"a transient unit still loaded", true, []string{unit + "ActiveState inactive", unit + "LoadState loaded"}, nil, "not gone (not-found)", false},
		{"a unit file masked", false, []string{unit + "LoadState masked"}, nil, "kind file, ActiveState=inactive, LoadState=masked: the runtime mask took: accepted", true},
		{"a mask that did not take, no activation source", false, []string{unit + "LoadState loaded", unit + "UnitFileState disabled"}, nil,
			"the runtime mask did not take (a unit file of higher priority shadows it) and was taken back; fallback: no activation source (TriggeredBy, WantedBy, RequiredBy, BoundBy and UpheldBy empty, UnitFileState=disabled): accepted", true},
		{"a mask that did not take, still there", false, []string{unit + "LoadState loaded"}, mask, "its runtime mask is there", false},
		{"a mask that did not take, a socket", false, []string{unit + "LoadState loaded", unit + "TriggeredBy sample-trial.socket"}, nil,
			"can be started: TriggeredBy=sample-trial.socket", false},
		{"a mask that did not take, enabled", false, []string{unit + "LoadState loaded", unit + "UnitFileState enabled", unit + "WantedBy default.target"}, nil,
			"can be started: WantedBy=default.target UnitFileState=enabled", false},
		{"a mask that did not take, bound", false, []string{unit + "LoadState loaded", unit + "BoundBy other.service", unit + "RequiredBy more.service"}, nil,
			"can be started: RequiredBy=more.service BoundBy=other.service", false},
		{"a mask that did not take, linked", false, []string{unit + "LoadState loaded", unit + "UnitFileState linked"}, nil, "can be started: UnitFileState=linked", false},
		{"a unit file that is gone", false, []string{unit + "LoadState not-found"}, nil, "neither masked nor loaded", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newScratchSeatKit(t)
			r := filepath.Join(k.dir, "proof")
			if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 0 {
				t.Fatalf("--write: exit %d:\n%s", code, out)
			}
			if c.transient {
				k.recordTrial(t, r, k.transientUnit("/srv/sample/bin/ataniqd", "Sample trial"))
			} else {
				k.recordTrial(t, r, k.fileUnit())
			}
			k.unitsSay(t, c.units...)
			if c.setup != nil {
				c.setup(k)
			}
			gate := "192.0.2.10:" + freeTCPPort(t)
			vals := []string{"--state", k.state, "--root", r}
			canon := filepath.Join(k.state, "owner", "scratch.json")
			trial := mustRead(t, canon)
			out, code := k.run(t, nil, append(append([]string{"--swap-in"}, vals...), "--gate", gate, "--sitting", "3", "--unit", "sample-trial.service")...)
			guard := filepath.Join(r, "evidence", "trial-unit-guard.txt")
			if !c.ok {
				if code != 1 || !strings.Contains(out, c.want) {
					t.Fatalf("--swap-in: exit %d, want 1 and %q:\n%s", code, c.want, out)
				}
				if !bytes.Equal(mustRead(t, canon), trial) {
					t.Fatal("a refused --swap-in moved the trial's settings")
				}
				for _, p := range []string{guard, filepath.Join(r, "swap-record")} {
					if _, err := os.Lstat(p); err == nil {
						t.Fatalf("a refused --swap-in wrote %s", p)
					}
				}
				k.onlyShows(t)
				return
			}
			if code != 0 || !strings.Contains(out, "status: swapped in") {
				t.Fatalf("--swap-in: exit %d:\n%s", code, out)
			}
			if g := string(mustRead(t, guard)); !strings.HasPrefix(g, "swap-in, sitting 3, unit sample-trial.service: ") || !strings.Contains(g, c.want) || strings.Count(g, "\n") != 1 {
				t.Errorf("the guard's evidence: %q, want %q", g, c.want)
			}
			out, code = k.run(t, nil, append(append([]string{"--swap-out"}, vals...), "--gate", gate, "--unit", "sample-trial.service")...)
			step15 := "unmask the trial's unit if it is masked and start it again (step 15)"
			if c.transient {
				step15 = "re-create the trial's transient unit: OWNER ONLY: sh " + filepath.Join(r, "trial-restart.sh") + " (step 15)"
			}
			if code != 0 || !strings.Contains(out, "status: trial in place (the way back is done)") || !strings.Contains(out, "next: "+step15) {
				t.Fatalf("--swap-out: exit %d, want %q:\n%s", code, step15, out)
			}
			k.onlyShows(t)
		})
	}
}
