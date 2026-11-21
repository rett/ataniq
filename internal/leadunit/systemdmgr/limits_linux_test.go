package systemdmgr

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"ataniq/internal/leadunit"
)

// limitSpec is a valid spec with limits inside every bound, for tests only.
func limitSpec() leadunit.Spec {
	s := validSpec()
	s.Limits = &leadunit.Limits{MemoryMax: 4 << 30, SwapMax: 0, TasksMax: 512}
	return s
}

func TestStartArgvLimitsGolden(t *testing.T) {
	got, err := startArgv(limitSpec(), DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"busctl", "--user", "--timeout=5s", "--", "call",
		"org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager",
		"StartTransientUnit", "ssa(sv)a(sa(sv))", unitName, "fail", "12",
		"Type", "s", "exec",
		"CollectMode", "s", "inactive-or-failed",
		"KillMode", "s", "control-group",
		"Restart", "s", "no",
		"TimeoutStopUSec", "t", "2000000",
		"MemoryMax", "t", "4294967296",
		"MemorySwapMax", "t", "0",
		"TasksMax", "t", "512",
		"Description", "s", "ataniq lead epoch=3 intent=intent-7",
		"WorkingDirectory", "s", "/srv/work",
		"Environment", "as", "2", "A=1", "PATH=/usr/bin:/bin",
		"ExecStartEx", "a(sasas)", "1", "/usr/bin/lead", "5", "/usr/bin/lead", "--name", "a b", "$HOME", "%h", "1", "no-env-expand",
		"0",
	}
	mustEqual(t, got, want)
}

func TestStartArgvLimitsAfterTheBoundProperties(t *testing.T) {
	s := limitSpec()
	s.Parent = "ataniq-lead-parent.service"
	got, err := startArgv(s, DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	after := indexOf(got, "After")
	mem := indexOf(got, "MemoryMax")
	if after < 0 || mem < 0 || mem < after {
		t.Fatalf("the limits do not follow the bound properties: %q", got)
	}
	i := indexOf(got, "fail")
	if got[i+1] != "15" {
		t.Fatalf("property count = %s, want 15", got[i+1])
	}
}

func TestStartArgvWithoutLimitsCarriesNoneOfThem(t *testing.T) {
	got, err := startArgv(validSpec(), DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"MemoryMax", "MemorySwapMax", "TasksMax"} {
		if indexOf(got, name) >= 0 {
			t.Errorf("%s in the call of a unit with no limits: %q", name, got)
		}
	}
}

func TestStartArgvCarriesEveryLimitProperty(t *testing.T) {
	s := limitSpec()
	argv, err := startArgv(s, DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range leadunit.LimitProperties(*s.Limits) {
		parts, err := limitProperty(p)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		if len(parts) != 3 || parts[1] != "t" {
			t.Errorf("%s is not rendered as an unsigned 64-bit number: %q", p.Name, parts)
		}
		if !containsRun(argv, parts) {
			t.Errorf("limit property %s=%s is not in the call as %q", p.Name, p.Value, parts)
		}
	}
	n := 4 + len(leadunit.FixedProperties()) + len(leadunit.LimitProperties(*s.Limits))
	i := indexOf(argv, "fail")
	if i < 0 || argv[i+1] != fmt.Sprint(n) {
		t.Fatalf("property count after the mode: %q, want %d", argv[i+1:i+2], n)
	}
}

func TestLimitPropertyRefusesWhatItDoesNotKnow(t *testing.T) {
	for _, p := range []leadunit.Property{
		{Name: "MemoryHigh", Value: "1"},
		{Name: "MemoryMax", Value: "1G"},
		{Name: "MemoryMax", Value: "infinity"},
		{Name: "TasksMax", Value: ""},
		{Name: "TasksMax", Value: "-1"},
		{Name: "TasksMax", Value: "007"},
		{Name: "MemorySwapMax", Value: "18446744073709551616"},
	} {
		if _, err := limitProperty(p); err == nil {
			t.Errorf("%v accepted", p)
		}
	}
}

func TestStartWithLimitsSendsThemInTheCall(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Start(context.Background(), limitSpec()); err != nil {
		t.Fatal(err)
	}
	calls := r.run.recorded()
	if len(calls) == 0 {
		t.Fatal("no call")
	}
	if !containsRun(calls[0], []string{"MemoryMax", "t", "4294967296", "MemorySwapMax", "t", "0", "TasksMax", "t", "512"}) {
		t.Fatalf("the start call does not carry the limits: %q", calls[0])
	}
}

func TestStartRefusesLimitsOutsideTheBoundsBeforeAnyCall(t *testing.T) {
	r := newRig(t)
	s := limitSpec()
	s.Limits.TasksMax = leadunit.MaxTasksMax + 1
	if _, err := r.m.Start(context.Background(), s); !errors.Is(err, leadunit.ErrInvalidSpec) {
		t.Fatalf("err = %v, want ErrInvalidSpec", err)
	}
	if n := len(r.run.recorded()); n != 0 {
		t.Fatalf("%d calls for a refused spec", n)
	}
}
