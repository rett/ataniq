//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ataniq/internal/leadunit"
)

// These tests drive the limit checks against the in-memory leadunit.Fake and a
// fake host. No unit is started, systemctl is not run and no control group is
// read.

const (
	testCgroupRoot = "/cg"
	testRuntimeDir = "/rt"
)

// sampleLimits and sampleLowered are the numbers of these tests; no test and no
// code of the harness takes them from a constant of the command.
func sampleLimits() leadunit.Limits {
	return leadunit.Limits{MemoryMax: 2 << 30, SwapMax: 0, TasksMax: 200}
}

const sampleLowered = uint64(1 << 30)

// fakeHost stands in for systemctl and the files of the machine. It keeps the
// three limits of the unit and answers show, set-property and the file reads
// from them; its fields make it answer wrongly.
type fakeHost struct {
	mu   sync.Mutex
	fake *leadunit.Fake
	unit string
	cg   string

	cmds [][]string
	vals limitValues
	set  bool // vals holds the unit's values

	showAs      func(vals limitValues) string // replaces the show output
	fileAs      map[string]string             // replaces a file's content, by file name
	setErr      error                         // set-property fails
	ignoreSet   bool                          // set-property succeeds and changes nothing
	showIgnore  bool                          // show keeps the first values after set-property
	leaveDropIn bool                          // user.control keeps the unit's folder after the stop
	noRuntime   bool                          // the runtime folder is not there
	listErr     error                         // listing user.control fails
}

func (h *fakeHost) load() {
	if h.set {
		return
	}
	if l, ok := h.fake.UnitLimits(h.unit); ok {
		h.vals, h.set = valuesOf(l), true
	}
}

func (h *fakeHost) Run(_ context.Context, argv []string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cmds = append(h.cmds, append([]string(nil), argv...))
	h.load()
	if len(argv) < 3 || argv[0] != "systemctl" || argv[1] != "--user" {
		return "", errors.New("unexpected command")
	}
	switch argv[2] {
	case "show":
		if h.showAs != nil {
			return h.showAs(h.vals), nil
		}
		var b strings.Builder
		for i, p := range limitProps {
			b.WriteString(p.prop + "=" + showNumber(h.vals[i]) + "\n")
		}
		return b.String(), nil
	case "set-property":
		if h.setErr != nil {
			return "", h.setErr
		}
		if !h.ignoreSet {
			n, err := strconv.ParseUint(strings.TrimPrefix(argv[len(argv)-1], "MemoryMax="), 10, 64)
			if err != nil {
				return "", err
			}
			h.vals[0] = n
		}
		return "", nil
	}
	return "", errors.New("unexpected systemctl verb")
}

func (h *fakeHost) ReadFile(path string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	dir := filepath.Join(testCgroupRoot, h.cg)
	if filepath.Dir(path) != dir {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	name := filepath.Base(path)
	if s, ok := h.fileAs[name]; ok {
		return []byte(s), nil
	}
	for i, p := range limitProps {
		if p.file != name {
			continue
		}
		v := h.vals[i]
		if v == infinite {
			return []byte("max\n"), nil
		}
		return []byte(strconv.FormatUint(v, 10) + "\n"), nil
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
}

func (h *fakeHost) ReadDir(path string) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.noRuntime {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	child := strings.TrimSuffix(h.unit, ".service") + "-child.service"
	switch path {
	case filepath.Join(testRuntimeDir, "systemd"):
		return []string{"transient", "generator"}, nil
	case filepath.Join(testRuntimeDir, "systemd/transient"):
		var names []string
		for _, u := range []string{h.unit, child} {
			if h.fake.Loaded(u) {
				names = append(names, u)
			}
		}
		return append(names, "unrelated.service"), nil
	case filepath.Join(testRuntimeDir, "systemd/user.control"):
		if h.listErr != nil {
			return nil, h.listErr
		}
		var names []string
		if h.leaveDropIn || h.fake.Loaded(h.unit) {
			names = append(names, h.unit+".d")
		}
		return names, nil
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
}

func newLimitRig(t *testing.T) (*qrig, *fakeHost) {
	t.Helper()
	r := newQRig(t)
	l := sampleLimits()
	unit := "ataniq-qualify-" + testSuffix + ".service"
	h := &fakeHost{fake: r.fake, unit: unit, cg: "/fake.slice/" + unit}
	r.q.limits, r.q.lowered = &l, sampleLowered
	r.q.host = h
	r.q.cgroupRoot, r.q.runtimeDir = testCgroupRoot, testRuntimeDir
	return r, h
}

func checkNames(rep report) string {
	var names []string
	for _, c := range rep.Checks {
		names = append(names, c.Name)
	}
	return strings.Join(names, " ")
}

func TestLimitChecksPassAndAreNamedInTheReport(t *testing.T) {
	r, h := newLimitRig(t)
	rep := r.q.qualify(context.Background())
	for _, c := range rep.Checks {
		if c.Result != resultPass {
			t.Errorf("%+v", c)
		}
	}
	if rep.Verdict != resultPass {
		t.Fatalf("%+v", rep)
	}
	want := "record-unit-name start unit-active description-read-back child-ready arguments-reach-the-child-unchanged second-start-refused inspect-matches " +
		"limits-show-read-back limits-cgroup-read-back memory-limit-lowered lowered-memory-limit-show-read-back lowered-memory-limit-cgroup-read-back " +
		"bound-child-started sigterm-leaves-group-populated sigkill-sent empty-true unit-not-loaded bound-child-stops-with-parent " +
		"runtime-folders-clean-after-stop empty-true-for-a-fresh-name cleanup"
	if got := checkNames(rep); got != want {
		t.Fatalf("checks\n got %s\nwant %s", got, want)
	}

	// The unit was started with the limits, and the bound child with none.
	specs := r.fake.StartedSpecs()
	if len(specs) != 2 || specs[0].Limits == nil || *specs[0].Limits != sampleLimits() || specs[1].Limits != nil {
		t.Fatalf("specs %+v", specs)
	}
	if err := specs[0].Validate(); err != nil {
		t.Fatal(err)
	}

	// The two commands the harness runs through the host, exactly.
	unit := rep.Unit
	var shows, sets [][]string
	for _, c := range h.cmds {
		switch c[2] {
		case "show":
			shows = append(shows, c)
		case "set-property":
			sets = append(sets, c)
		}
	}
	wantShow := []string{"systemctl", "--user", "show", "--all", "--property=MemoryMax,MemorySwapMax,TasksMax", "--", unit}
	if len(shows) != 2 || strings.Join(shows[0], " ") != strings.Join(wantShow, " ") {
		t.Fatalf("show commands %q", shows)
	}
	wantSet := []string{"systemctl", "--user", "set-property", "--runtime", "--", unit, "MemoryMax=" + strconv.FormatUint(sampleLowered, 10)}
	if len(sets) != 1 || strings.Join(sets[0], " ") != strings.Join(wantSet, " ") {
		t.Fatalf("set-property commands %q", sets)
	}
	// The detail of the check that lowers the limit says what the folders held.
	for _, c := range rep.Checks {
		if c.Name == "memory-limit-lowered" && !strings.Contains(c.Detail, "systemd/user.control holds") {
			t.Errorf("no folder note: %q", c.Detail)
		}
	}
}

func TestLimitChecksCompareBothWaysWithTheSpecification(t *testing.T) {
	cases := map[string]struct {
		setup func(h *fakeHost)
		fail  string
		skip  string
	}{
		"show memory": {
			func(h *fakeHost) {
				h.showAs = func(v limitValues) string {
					return "MemoryMax=1\nMemorySwapMax=" + showNumber(v[1]) + "\nTasksMax=" + showNumber(v[2]) + "\n"
				}
			},
			"limits-show-read-back", "limits-cgroup-read-back",
		},
		"show swap is infinity": {
			func(h *fakeHost) {
				h.showAs = func(v limitValues) string {
					return "MemoryMax=" + showNumber(v[0]) + "\nMemorySwapMax=infinity\nTasksMax=" + showNumber(v[2]) + "\n"
				}
			},
			"limits-show-read-back", "limits-cgroup-read-back",
		},
		"show is not parsable": {
			func(h *fakeHost) { h.showAs = func(limitValues) string { return "nonsense\n" } },
			"limits-show-read-back", "limits-cgroup-read-back",
		},
		"cgroup memory": {
			func(h *fakeHost) { h.fileAs = map[string]string{"memory.max": "4096\n"} },
			"limits-cgroup-read-back", "memory-limit-lowered",
		},
		"cgroup swap is max": {
			func(h *fakeHost) { h.fileAs = map[string]string{"memory.swap.max": "max\n"} },
			"limits-cgroup-read-back", "memory-limit-lowered",
		},
		"cgroup pids": {
			func(h *fakeHost) { h.fileAs = map[string]string{"pids.max": "201\n"} },
			"limits-cgroup-read-back", "memory-limit-lowered",
		},
		"cgroup file missing": {
			func(h *fakeHost) { h.fileAs = nil; h.cg = "/elsewhere" },
			"limits-cgroup-read-back", "memory-limit-lowered",
		},
	}
	for name, c := range cases {
		r, h := newLimitRig(t)
		c.setup(h)
		rep := r.q.qualify(context.Background())
		res := results(rep)
		if rep.Verdict != resultFail || res[c.fail] != resultFail || res[c.skip] != resultSkipped || res["cleanup"] != resultPass {
			t.Errorf("%s: %+v", name, rep)
		}
		if r.fake.Loaded(rep.Unit) {
			t.Errorf("%s: left running", name)
		}
	}
}

func TestLoweredMemoryLimitIsReadBackBothWays(t *testing.T) {
	// A set-property that succeeds and changes nothing fails the show read-back.
	r, h := newLimitRig(t)
	h.ignoreSet = true
	rep := r.q.qualify(context.Background())
	res := results(rep)
	if rep.Verdict != resultFail || res["memory-limit-lowered"] != resultPass || res["lowered-memory-limit-show-read-back"] != resultFail ||
		res["lowered-memory-limit-cgroup-read-back"] != resultSkipped || res["cleanup"] != resultPass {
		t.Fatalf("%+v", rep)
	}

	// A set-property that works for systemctl and not for the control group
	// fails the second read-back alone.
	r, h = newLimitRig(t)
	r.q.host = &splitHost{fakeHost: h, memoryMaxStays: true}
	rep = r.q.qualify(context.Background())
	res = results(rep)
	if rep.Verdict != resultFail || res["lowered-memory-limit-show-read-back"] != resultPass || res["lowered-memory-limit-cgroup-read-back"] != resultFail {
		t.Fatalf("%+v", rep)
	}

	// A set-property that fails fails its check, and the rest is skipped.
	r, h = newLimitRig(t)
	h.setErr = errors.New("access denied")
	rep = r.q.qualify(context.Background())
	res = results(rep)
	if res["memory-limit-lowered"] != resultFail || res["lowered-memory-limit-show-read-back"] != resultSkipped || res["cleanup"] != resultPass {
		t.Fatalf("%+v", rep)
	}

	// A lowered value that is not below the specified one is refused before
	// anything is set.
	r, h = newLimitRig(t)
	r.q.lowered = r.q.limits.MemoryMax
	rep = r.q.qualify(context.Background())
	if results(rep)["memory-limit-lowered"] != resultFail {
		t.Fatalf("%+v", rep)
	}
	for _, c := range h.cmds {
		if c[2] == "set-property" {
			t.Fatalf("set-property ran: %q", c)
		}
	}
}

// splitHost makes the control group file memory.max keep the value the unit
// was started with, while systemctl show follows the change.
type splitHost struct {
	*fakeHost
	memoryMaxStays bool
}

func (s *splitHost) ReadFile(path string) ([]byte, error) {
	if s.memoryMaxStays && filepath.Base(path) == "memory.max" {
		return []byte(strconv.FormatUint(sampleLimits().MemoryMax, 10) + "\n"), nil
	}
	return s.fakeHost.ReadFile(path)
}

// The control group files are compared exactly: a file that holds the limit
// rounded either way, which is what a size that is not a whole number of pages
// would read back as, is a difference, whichever direction the kernel rounds.
func TestControlGroupFilesAreComparedExactly(t *testing.T) {
	for name, file := range map[string]string{
		"rounded down": strconv.FormatUint(sampleLimits().MemoryMax-4096, 10) + "\n",
		"rounded up":   strconv.FormatUint(sampleLimits().MemoryMax+4096, 10) + "\n",
	} {
		r, h := newLimitRig(t)
		h.fileAs = map[string]string{"memory.max": file}
		rep := r.q.qualify(context.Background())
		if results(rep)["limits-cgroup-read-back"] != resultFail || rep.Verdict != resultFail {
			t.Errorf("%s: %+v", name, rep)
		}
	}
	// A size that is not a whole number of pages that the files read back as
	// given (a host that keeps it exactly) passes: nothing is allowed for.
	r, _ := newLimitRig(t)
	r.q.limits.MemoryMax = 2<<30 + 1
	r.q.lowered = 1<<30 + 5
	if rep := r.q.qualify(context.Background()); rep.Verdict != resultPass {
		t.Fatalf("%+v", rep)
	}
}

func TestMemoryAndSwapSizesMustBeWholePages(t *testing.T) {
	for _, args := range [][]string{
		{"--memory-max", "2147483649"}, {"--memory-max", "262145K"}, {"--swap-max", "4097"}, {"--swap-max", "1M", "--memory-lowered", "805306369"},
		{"--memory-lowered", "805306369"},
	} {
		_, err := parseArgs(args)
		if err == nil || !strings.Contains(err.Error(), "whole number of 4096-byte pages") {
			t.Errorf("%q: %v", args, err)
		}
		var out, errb bytes.Buffer
		if code := runWith(append([]string{"--run", "--state-dir", "/state"}, args...), &out, &errb, refuseManager); code != exitUsage {
			t.Errorf("%q: exit %d", args, code)
		}
	}
	for _, args := range [][]string{
		{"--memory-max", "2147483648"}, {"--memory-max", "1048580K"}, {"--swap-max", "4096"}, {"--swap-max", "0"}, {"--memory-lowered", "805306368"},
	} {
		if _, err := parseArgs(args); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
}

func TestRuntimeFoldersAfterTheStop(t *testing.T) {
	r, h := newLimitRig(t)
	h.leaveDropIn = true
	rep := r.q.qualify(context.Background())
	res := results(rep)
	if rep.Verdict != resultFail || res["runtime-folders-clean-after-stop"] != resultFail {
		t.Fatalf("%+v", rep)
	}
	for _, c := range rep.Checks {
		if c.Name == "runtime-folders-clean-after-stop" && !strings.Contains(c.Detail, "systemd/user.control/"+rep.Unit+".d") {
			t.Errorf("the entry that is left is not named: %q", c.Detail)
		}
	}
	if res["empty-true-for-a-fresh-name"] != resultSkipped || res["cleanup"] != resultPass {
		t.Fatalf("%+v", rep)
	}

	// An entry of another unit is not the run's.
	r, _ = newLimitRig(t)
	if rep := r.q.qualify(context.Background()); results(rep)["runtime-folders-clean-after-stop"] != resultPass {
		t.Fatalf("%+v", rep)
	}

	// A runtime folder that is not there, or a listing that fails, cannot pass.
	r, h = newLimitRig(t)
	h.noRuntime = true
	rep = r.q.qualify(context.Background())
	if results(rep)["runtime-folders-clean-after-stop"] != resultFail {
		t.Fatalf("%+v", rep)
	}
	r, h = newLimitRig(t)
	h.listErr = errors.New("permission denied")
	rep = r.q.qualify(context.Background())
	if results(rep)["runtime-folders-clean-after-stop"] != resultFail {
		t.Fatalf("%+v", rep)
	}
}

func TestLimitChecksNeedTheHost(t *testing.T) {
	r, _ := newLimitRig(t)
	r.q.host = nil
	rep := r.q.qualify(context.Background())
	res := results(rep)
	if rep.Verdict != resultFail || res["limits-show-read-back"] != resultFail || res["cleanup"] != resultPass {
		t.Fatalf("%+v", rep)
	}
	for _, c := range rep.Checks {
		if c.Name == "limits-show-read-back" && !strings.Contains(c.Detail, "no access to systemctl") {
			t.Errorf("%q", c.Detail)
		}
	}
}

func TestQualifierWithoutLimitsStartsNoneAndChecksNone(t *testing.T) {
	r := newQRig(t)
	rep := r.q.qualify(context.Background())
	if rep.Verdict != resultPass || strings.Contains(checkNames(rep), "limits") || strings.Contains(checkNames(rep), "runtime-folders") {
		t.Fatalf("%+v", rep)
	}
	if specs := r.fake.StartedSpecs(); specs[0].Limits != nil {
		t.Fatalf("limits %+v", specs[0].Limits)
	}
}

func TestQualifierSpecDoesNotShareItsLimits(t *testing.T) {
	r, _ := newLimitRig(t)
	s := r.q.spec("x")
	s.Limits.TasksMax = 1
	if r.q.limits.TasksMax != sampleLimits().TasksMax {
		t.Fatal("the spec shares the qualifier's limits")
	}
}

func TestParseNumberForms(t *testing.T) {
	good := map[string]uint64{
		"0": 0, "128": 128, "infinity": infinite, "max": infinite, "max ": infinite,
		"18446744073709551615": infinite, "4K": 4096, "1M": 1 << 20, "2G": 2 << 30, "1T": 1 << 40, "1P": 1 << 50, "15E": 15 << 60,
		"1073741824\n": 1 << 30,
	}
	for in, want := range good {
		if got, err := parseNumber(in); err != nil || got != want {
			t.Errorf("%q: %d, %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-1", "+1", "1.5G", "G", "abc", "50%", "1 G", "18446744073709551616", "16E", "[not set]", "0x10"} {
		if got, err := parseNumber(in); err == nil {
			t.Errorf("%q accepted as %d", in, got)
		}
	}
}

func TestParseShow(t *testing.T) {
	v, err := parseShow("MemoryMax=infinity\nMemorySwapMax=0\nTasksMax=4K\n")
	if err != nil || v != (limitValues{infinite, 0, 4096}) {
		t.Fatalf("%v %v", v, err)
	}
	for name, in := range map[string]string{
		"missing":  "MemoryMax=1\nMemorySwapMax=0\n",
		"repeated": "MemoryMax=1\nMemoryMax=1\nMemorySwapMax=0\nTasksMax=1\n",
		"extra":    "MemoryMax=1\nMemorySwapMax=0\nTasksMax=1\nMemoryHigh=1\n",
		"no equal": "MemoryMax=1\nMemorySwapMax=0\nTasksMax\n",
		"percent":  "MemoryMax=1\nMemorySwapMax=0\nTasksMax=15%\n",
		"empty":    "",
	} {
		if _, err := parseShow(in); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestLimitFlagsAndTheirDefaults(t *testing.T) {
	o, err := parseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	l, lowered, err := o.limits()
	if err != nil || l.MemoryMax != 1<<30 || l.SwapMax != 0 || l.TasksMax != 128 || lowered != 768<<20 {
		t.Fatalf("defaults %+v %d %v", l, lowered, err)
	}
	o, err = parseArgs([]string{"--memory-max", "3G", "--swap-max=64M", "--tasks-max", "999", "--memory-lowered", "2G"})
	if err != nil {
		t.Fatal(err)
	}
	l, lowered, err = o.limits()
	if err != nil || l != (leadunit.Limits{MemoryMax: 3 << 30, SwapMax: 64 << 20, TasksMax: 999}) || lowered != 2<<30 {
		t.Fatalf("%+v %d %v", l, lowered, err)
	}
	for _, args := range [][]string{
		{"--memory-max", "255M"}, {"--memory-max", "65G"}, {"--memory-max", "x"}, {"--memory-max", "-1G"},
		{"--swap-max", "65G"}, {"--tasks-max", "15"}, {"--tasks-max", "65537"}, {"--tasks-max", "1G"},
		{"--memory-lowered", "255M"}, {"--memory-lowered", "1G"}, {"--memory-max", "512M", "--memory-lowered", "512M"},
		{"--memory-max", "300M"}, // the default lowered value is no longer below it
	} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
	if err := func() error { _, err := parseArgs([]string{"--memory-max", "1G", "--tasks-max", "x"}); return err }(); err == nil {
		t.Error("a task limit that is not a number accepted")
	}
}

func TestHelpAndDryRunStateTheLimitsAndTheirDefaults(t *testing.T) {
	var help bytes.Buffer
	if code := runWith([]string{"-h"}, &help, new(bytes.Buffer), refuseManager); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{
		"--memory-max SIZE", "--swap-max SIZE", "--tasks-max N", "--memory-lowered SIZE",
		"default " + defaultMemoryMax, "default " + defaultSwapMax, "default " + defaultTasksMax, "default " + defaultMemoryLowered,
		"lowered", "control group files", "runtime folders",
		"whole number of 4096-byte pages", "usage error",
	} {
		if !strings.Contains(help.String(), want) {
			t.Errorf("help lacks %q", want)
		}
	}
	var out bytes.Buffer
	if code := runWith([]string{"--memory-max", "2G", "--memory-lowered", "1G", "--tasks-max", "64", "--swap-max", "128M"}, &out, new(bytes.Buffer), refuseManager); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{
		"MemoryMax=2147483648, MemorySwapMax=134217728 and TasksMax=64",
		"lower the memory limit once to 1073741824",
		"set-property --runtime",
		"memory.max, memory.swap.max and pids.max",
		"nothing for the two units remains under systemd/transient and systemd/user.control",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run lacks %q:\n%s", want, out.String())
		}
	}
	// A limit that is refused is a usage error.
	var errb bytes.Buffer
	if code := runWith([]string{"--memory-max", "1M"}, new(bytes.Buffer), &errb, refuseManager); code != exitUsage || !strings.Contains(errb.String(), "--memory-max") {
		t.Fatalf("exit %d %q", code, errb.String())
	}
}

func TestTheRealHostRunsOnlySystemctl(t *testing.T) {
	// A zero realLife has no runner: the refusal comes before it is used.
	for _, argv := range [][]string{nil, {"busctl", "--user", "call"}, {"/usr/bin/systemctl", "--user", "show"}} {
		if _, err := (realLife{}).Run(context.Background(), argv); err == nil || !strings.Contains(err.Error(), "only systemctl") {
			t.Errorf("%q: %v", argv, err)
		}
	}
}

func TestUserRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/4242")
	if got := userRuntimeDir(); got != "/run/user/4242" {
		t.Fatal(got)
	}
	for _, bad := range []string{"", "relative", "/", "/run/user/1/"} {
		t.Setenv("XDG_RUNTIME_DIR", bad)
		if got := userRuntimeDir(); !strings.HasPrefix(got, "/run/user/") || got == bad {
			t.Errorf("%q gave %q", bad, got)
		}
	}
}
