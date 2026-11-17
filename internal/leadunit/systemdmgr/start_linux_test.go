package systemdmgr

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadunit"
)

func TestStartArgvGolden(t *testing.T) {
	got, err := startArgv(validSpec(), DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"busctl", "--user", "--timeout=5s", "call",
		"org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager",
		"StartTransientUnit", "ssa(sv)a(sa(sv))", unitName, "fail", "10",
		"Type", "s", "exec",
		"CollectMode", "s", "inactive-or-failed",
		"KillMode", "s", "control-group",
		"Restart", "s", "no",
		"TimeoutStopUSec", "t", "2000000",
		"NoNewPrivileges", "b", "true",
		"Description", "s", "ataniq lead epoch=3 intent=intent-7",
		"WorkingDirectory", "s", "/srv/work",
		"Environment", "as", "2", "A=1", "PATH=/usr/bin:/bin",
		"ExecStartEx", "a(sasas)", "1", "/usr/bin/lead", "5", "/usr/bin/lead", "--name", "a b", "$HOME", "%h", "1", "no-env-expand",
		"0",
	}
	mustEqual(t, got, want)
}

// The command's arguments reach the call as single elements and the
// expansion of $VAR is switched off with the flag, in the rendered arguments.
func TestStartArgvExecStartExDisablesExpansion(t *testing.T) {
	argv, err := startArgv(validSpec(), DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	i := indexOf(argv, "ExecStartEx")
	if i < 0 || argv[i+1] != "a(sasas)" {
		t.Fatalf("no ExecStartEx a(sasas) in %q", argv)
	}
	if argv[len(argv)-3] != "1" || argv[len(argv)-2] != "no-env-expand" || argv[len(argv)-1] != "0" {
		t.Fatalf("flags are not 1 no-env-expand before the empty aux list: %q", argv[len(argv)-4:])
	}
	if indexOf(argv, "ExecStart") >= 0 {
		t.Fatal("plain ExecStart expands environment variables")
	}
	if !contains(argv, "$HOME") {
		t.Fatal("the argument is not carried literally")
	}
}

func TestStartArgvEmptyEnvironmentAndTimeout(t *testing.T) {
	s := validSpec()
	s.Env = nil
	s.Argv = []string{"/bin/x"}
	argv, err := startArgv(s, 1500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if argv[2] != "--timeout=2s" {
		t.Fatalf("timeout %q", argv[2])
	}
	i := indexOf(argv, "Environment")
	mustEqual(t, argv[i:i+3], []string{"Environment", "as", "0"})
	mustEqual(t, argv[len(argv)-9:], []string{"ExecStartEx", "a(sasas)", "1", "/bin/x", "1", "/bin/x", "1", "no-env-expand", "0"})
}

func TestStartArgvCarriesEveryFixedProperty(t *testing.T) {
	argv, err := startArgv(validSpec(), DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range leadunit.FixedProperties() {
		parts, err := fixedProperty(p)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		if !containsRun(argv, parts) {
			t.Errorf("fixed property %s=%s is not in the call as %q", p.Name, p.Value, parts)
		}
	}
	// Exactly the fixed ones, plus Description, WorkingDirectory, Environment
	// and ExecStartEx.
	n := 4 + len(leadunit.FixedProperties())
	if argv[11] != fmt.Sprint(n) {
		t.Fatalf("property count %q, want %d", argv[11], n)
	}
}

func TestFixedPropertyRefusesWhatItDoesNotKnow(t *testing.T) {
	for _, p := range []leadunit.Property{
		{Name: "MemoryMax", Value: "1G"},
		{Name: "NoNewPrivileges", Value: "maybe"},
		{Name: "TimeoutStopSec", Value: "soon"},
		{Name: "TimeoutStopSec", Value: "0s"},
	} {
		if _, err := fixedProperty(p); err == nil {
			t.Errorf("%v accepted", p)
		}
	}
}

func TestStartSuccessInspectsOnceAndReturnsItsState(t *testing.T) {
	r := newRig(t)
	st, err := r.m.Start(context.Background(), validSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Loaded || st.InvocationID != inv1 || st.Epoch != 3 || st.Intent != "intent-7" || !st.Ours || st.BootID != bootID {
		t.Fatalf("state %+v", st)
	}
	if st.ControlGroup != cgUnit || st.ControlGroupID != groupID(idA) || st.MainPID != 321 || st.Active != leadunit.ActiveActive {
		t.Fatalf("state %+v", st)
	}
	mustEqual(t, r.callNames(), []string{"busctl", "systemctl show"})
	mustEqual(t, r.run.recorded()[1], []string{"systemctl", "--user", "show", "--all", "--property=" + showProperties, "--", unitName})
}

func TestStartAcceptedJobWithUnitAlreadyGoneReturnsItsState(t *testing.T) {
	r := newRig(t)
	r.setUnit(notFoundProps())
	st, err := r.m.Start(context.Background(), validSpec())
	if err != nil || st.Loaded {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestStartInvalidSpecCallsNothing(t *testing.T) {
	r := newRig(t)
	s := validSpec()
	s.Epoch = 0
	_, err := r.m.Start(context.Background(), s)
	if !errors.Is(err, leadunit.ErrInvalidSpec) || len(r.run.recorded()) != 0 {
		t.Fatalf("%v %v", err, r.run.recorded())
	}
}

func TestStartContextDoneBeforeCallsNothing(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.m.Start(ctx, validSpec())
	if !errors.Is(err, context.Canceled) || errors.Is(err, leadunit.ErrReplyLost) || len(r.run.recorded()) != 0 {
		t.Fatalf("%v %v", err, r.run.recorded())
	}
}

func TestStartUnitExists(t *testing.T) {
	for _, msg := range []string{
		"Call failed: Unit " + unitName + " was already loaded or has a fragment file.\n",
		"Call failed: Unit " + unitName + " already exists.\n",
	} {
		r := newRig(t)
		r.start = func() (string, error) { return msg, exitErr() }
		_, err := r.m.Start(context.Background(), validSpec())
		if !errors.Is(err, leadunit.ErrUnitExists) || errors.Is(err, leadunit.ErrReplyLost) {
			t.Fatalf("%q: %v", msg, err)
		}
		mustEqual(t, r.callNames(), []string{"busctl"})
	}
}

// A message about another unit, or with more text, is not recognised as the
// refusal for this one and degrades to a lost reply.
func TestStartUnitExistsOnlyForThisUnit(t *testing.T) {
	for _, msg := range []string{
		"Call failed: Unit other.service was already loaded or has a fragment file.\n",
		"Call failed: Unit " + unitName + " was already loaded or has a fragment file.\nmore\n",
	} {
		r := newRig(t)
		r.start = func() (string, error) { return msg, exitErr() }
		_, err := r.m.Start(context.Background(), validSpec())
		if !errors.Is(err, leadunit.ErrReplyLost) || errors.Is(err, leadunit.ErrUnitExists) {
			t.Fatalf("%q: %v", msg, err)
		}
	}
}

func TestStartManagerUnreachableBeforeTheRequest(t *testing.T) {
	cases := map[string]func() (string, error){
		"connect failure": func() (string, error) {
			return "Failed to connect to bus: No medium found\n", exitErr()
		},
		"not launched": func() (string, error) {
			return "", fmt.Errorf("%w: %w", ErrNotLaunched, fs.ErrNotExist)
		},
	}
	for name, h := range cases {
		r := newRig(t)
		r.start = h
		_, err := r.m.Start(context.Background(), validSpec())
		if !errors.Is(err, leadunit.ErrUnavailable) || errors.Is(err, leadunit.ErrReplyLost) {
			t.Fatalf("%s: %v", name, err)
		}
		if len(r.run.recorded()) != 1 {
			t.Fatalf("%s: retried: %v", name, r.callNames())
		}
	}
}

func TestStartFailuresAfterTheRequestAreLostReplies(t *testing.T) {
	cases := map[string]func() (string, error){
		"exit status": func() (string, error) { return "", exitErr() },
		"bus timeout": func() (string, error) { return "Call failed: Connection timed out\n", exitErr() },
		"no reply": func() (string, error) {
			return "Call failed: Message recipient disconnected from message bus without replying\n", exitErr()
		},
		"bus refusal":    func() (string, error) { return "Call failed: Access denied\n", exitErr() },
		"runner timeout": func() (string, error) { return "", context.DeadlineExceeded },
		"runner cancel":  func() (string, error) { return "", context.Canceled },
		"killed":         func() (string, error) { return "o \"/org/freedesktop/systemd1/job/4", errors.New("signal: killed") },
		"other error":    func() (string, error) { return "", errBoom },
		"empty reply":    func() (string, error) { return "", nil },
		"partial reply":  func() (string, error) { return "o \"/org/freedesktop/systemd1/job/", nil },
		"unparsable":     func() (string, error) { return "garbage\n", nil },
		"not a job":      func() (string, error) { return "o \"/org/freedesktop/systemd1/unit/x\"\n", nil },
		"job number":     func() (string, error) { return "o \"/org/freedesktop/systemd1/job/x1\"\n", nil },
		"two replies":    func() (string, error) { return jobReply + jobReply, nil },
	}
	for name, h := range cases {
		r := newRig(t)
		r.start = h
		st, err := r.m.Start(context.Background(), validSpec())
		if !errors.Is(err, leadunit.ErrReplyLost) || st != (leadunit.State{}) {
			t.Fatalf("%s: %+v %v", name, st, err)
		}
		if errors.Is(err, leadunit.ErrUnitExists) || errors.Is(err, leadunit.ErrUnavailable) {
			t.Fatalf("%s: wrong class: %v", name, err)
		}
		mustEqual(t, r.callNames(), []string{"busctl"}) // no inspection, no retry
	}
}

func TestStartTimeoutAfterTheRequestIsALostReply(t *testing.T) {
	run := &blockingRunner{}
	m := newManager(run, newFakeKernel(), 30*time.Millisecond)
	_, err := m.Start(context.Background(), validSpec())
	if !errors.Is(err, leadunit.ErrReplyLost) || !errors.Is(err, context.DeadlineExceeded) || run.calls != 1 {
		t.Fatalf("%v calls=%d", err, run.calls)
	}
}

func TestStartCallerContextEndsDuringTheCallIsALostReply(t *testing.T) {
	run := &blockingRunner{}
	m := newManager(run, newFakeKernel(), 0)
	ctx := deadlineCtx(t, 30*time.Millisecond)
	_, err := m.Start(ctx, validSpec())
	if !errors.Is(err, leadunit.ErrReplyLost) || run.calls != 1 {
		t.Fatalf("%v calls=%d", err, run.calls)
	}
}

func TestStartInspectionFailureAfterAcceptedJobIsALostReply(t *testing.T) {
	cases := map[string]func(r *rig){
		"show fails":      func(r *rig) { r.showFn = func() (string, error) { return "", errBoom } },
		"show partial":    func(r *rig) { r.showFn = func() (string, error) { return "Id=" + unitName + "\nLoadState=loa", nil } },
		"cgroup unread":   func(r *rig) { r.k.lookupErr[cgUnit] = errors.New("permission denied") },
		"cgroup missing":  func(r *rig) { delete(r.k.groups, cgUnit) },
		"boot id missing": func(r *rig) { r.k.bootErr = errBoom },
	}
	for name, setup := range cases {
		r := newRig(t)
		setup(r)
		st, err := r.m.Start(context.Background(), validSpec())
		if !errors.Is(err, leadunit.ErrReplyLost) || st != (leadunit.State{}) {
			t.Fatalf("%s: %+v %v", name, st, err)
		}
		if n := len(r.run.recorded()); n != 2 {
			t.Fatalf("%s: %d calls (retried?): %v", name, n, r.callNames())
		}
	}
}

func TestStartReadsBackAForeignDescriptionAsNotOurs(t *testing.T) {
	// The job was accepted and the unit reads back with another description:
	// the state says so and the caller decides.
	r := newRig(t)
	r.setUnit(with(loadedProps(), "Description", "something else"))
	st, err := r.m.Start(context.Background(), validSpec())
	if err != nil || st.Ours || st.Epoch != 0 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestStartNeverCarriesUnitTextInTheCommandLine(t *testing.T) {
	r := newRig(t)
	r.setUnit(with(loadedProps(), "Description", "x; touch /tmp/marker"))
	if _, err := r.m.Start(context.Background(), validSpec()); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.run.recorded() {
		if strings.Contains(strings.Join(c, " "), "touch") {
			t.Fatalf("text of the unit in %q", c)
		}
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func contains(s []string, v string) bool { return indexOf(s, v) >= 0 }

func containsRun(s, run []string) bool {
	for i := 0; i+len(run) <= len(s); i++ {
		ok := true
		for j := range run {
			if s[i+j] != run[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
