//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type entryFixture struct {
	*hostFixture
	clock                                                         time.Time
	parentStarted, serviceStarted, parentStopped, observed, wrote bool
	members                                                       []Process
	endpoints                                                     R1EntryEndpoints
	ready, failure                                                R1Evidence
	output                                                        func([]byte) []byte
	serviceResult                                                 string
	preflightErr                                                  error
}

func entryTestFixture(t *testing.T) *entryFixture {
	t.Helper()
	x := testHost(t)
	f := &entryFixture{hostFixture: x, clock: time.Now(), serviceResult: "exit-code"}
	a := x.a
	a.Config.Case, a.Config.Role = R1EntryCase, "candidate"
	a.Config.Busctl = filepath.Join(filepath.Dir(a.Config.Systemctl), "busctl")
	if err := os.WriteFile(a.Config.Busctl, []byte("busctl"), 0500); err != nil {
		t.Fatal(err)
	}
	s, _, err := subject(a.Config.Busctl, true, false, false, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	a.Subjects["busctl"] = s
	a.Profile, a.DescriptorPolicy = R1EntryProfile, FinalDescriptorPolicy
	path := filepath.Join(x.c.Path, a.ID)
	if err := os.Mkdir(filepath.Join(path, "r1-control"), 0700); err != nil {
		t.Fatal(err)
	}
	d, id, err := ownedDir(filepath.Join(path, "r1-control"))
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	r := R1Request{1, a.Config.RunID, a.Config.RequestID, a.ID, R1Profile, a.FixtureNonce}
	b, _ := json.Marshal(r)
	null := R1NullDevice{FileIdentity{7, 900, 0}, syscall.S_IFCHR | 0666, 0, 259}
	a.EntryStatic = &R1EntryStatic{R1EntryProfile, r, b, syntheticDigest(b), id, null, map[string]ELFMetadata{"systemctl": {}, "systemd-run": {}, "busctl": {}}}
	if err := os.WriteFile(filepath.Join(path, "request.json"), b, 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "output"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	a.Request = hostRequest(a)
	a.Command, a.TargetCommand, a.Launch = command(a, path), targetCommand(a, path), launch(a, path)
	a.PlanSHA256 = planDigest(a)
	root := "/user.slice/user-1000.slice/user@" + strconv.Itoa(os.Getuid()) + ".service"
	x.parent.Boundary.Path = root + "/" + a.Slice
	x.parent.Properties["ControlGroup"] = x.parent.Boundary.Path
	x.parent.Properties["Description"] = "ataniq-entry-" + a.ID
	for k, v := range map[string]string{"Transient": "yes", "MemoryMax": "268435456", "MemorySwapMax": "0", "MemoryHigh": "infinity", "TasksMax": "64", "CPUQuotaPerSecUSec": "1s"} {
		x.parent.Properties[k] = v
	}
	x.service.Boundary.Path = x.parent.Boundary.Path + "/" + a.Service
	x.service.Properties["ControlGroup"] = x.service.Boundary.Path
	x.service.Properties["MemoryMax"], x.service.Properties["RuntimeMaxUSec"] = "100663296", "30s"
	x.service.Properties["MemorySwapMax"], x.service.Properties["Delegate"] = "0", "no"
	x.service.Properties["MemoryHigh"], x.service.Properties["OOMPolicy"] = "infinity", "kill"
	x.service.Main.Cgroup = x.service.Boundary.Path
	i := Injection{Version: 1, Attempt: a.ID, Invocation: x.service.Properties["InvocationID"], Process: *x.service.Main, Boundary: x.service.Boundary, Fixture: a.Fixture, Descriptors: expectedDescriptors(a), Profile: a.Profile, RunID: a.Config.RunID, RequestID: a.Config.RequestID, RoleID: a.Config.RoleID, DescriptorPolicy: a.DescriptorPolicy}
	b, _ = json.Marshal(i)
	if err := os.Remove(filepath.Join(path, "injection.json")); err != nil {
		t.Fatal(err)
	}
	if err := exclusiveFile(filepath.Join(path, "injection.json"), b, 0400); err != nil {
		t.Fatal(err)
	}
	monitor := *x.service.Main
	monitor.Executable = a.Snapshots["bwrap"].Identity
	monitor.Status += "PPid:\t50\n"
	main := fixtureProcess(101, a.Snapshots["probe"].Identity, x.service.Boundary.Path)
	main.Status = "NSpid:\t101\t2\nPPid:\t100\nNoNewPrivs:\t1\nSeccomp:\t2\n"
	for _, n := range []string{"CapInh", "CapAmb", "CapEff", "CapPrm", "CapBnd"} {
		main.Status += n + ":\t0000000000000000\n"
	}
	for n, id := range main.Namespaces {
		id.Inode += 100
		main.Namespaces[n] = id
	}
	main.Mounts.Probe = "2 1 0:2 / /probe ro - ext4 fixture ro"
	child := main
	child.PID = 102
	child.Start = 124
	child.Status = strings.Replace(strings.Replace(main.Status, "101\t2", "102\t3", 1), "PPid:\t100", "PPid:\t101", 1)
	f.members = []Process{monitor, main, child}
	f.endpoints = R1EntryEndpoints{Null: null, Control: id, NullMount: "10 1 0:7 /null /dev/null rw,nosuid - devtmpfs devtmpfs rw", DevMount: "1 0 0:1 /dev /dev ro - tmpfs tmpfs ro", ControlMount: "11 1 0:8 /owned /r1-control ro,nosuid,nodev - tmpfs tmpfs rw", NullUID: 0}
	// Host root uid is mapped in this synthetic projection; no sandbox-UID0
	// assumption is made by the production observer.
	for j := range f.members {
		if j > 0 {
			f.members[j].UIDMap = "0 0 1\n"
		}
	}
	for fd := 0; fd < 3; fd++ {
		flags := uint64(syscall.O_WRONLY)
		if fd == 0 {
			flags = syscall.O_RDONLY
		}
		f.endpoints.ChildStdio = append(f.endpoints.ChildStdio, R1EntryFD{fd, null.Identity, null.Mode, null.Rdev, flags, 10})
	}
	pipe := FileIdentity{12, 345, uint32(os.Getuid())}
	f.endpoints.ChildToken = R1EntryFD{4, pipe, syscall.S_IFIFO | 0600, 0, syscall.O_RDONLY | syscall.O_NONBLOCK, 20}
	f.endpoints.ParentToken = R1EntryFD{7, pipe, syscall.S_IFIFO | 0600, 0, syscall.O_WRONLY | syscall.O_NONBLOCK, 20}
	f.endpoints.MainOutput = R1EntryFD{1, a.Streams["output"], syscall.S_IFREG | 0600, 0, syscall.O_WRONLY | syscall.O_APPEND, 30}
	x.h.now = func() time.Time { return f.clock }
	x.h.entryNull = func() (R1NullDevice, error) { return null, nil }
	x.h.entryTree = func(parent, tool Boundary) error {
		if parent != x.parent.Boundary || tool != x.service.Boundary {
			return errors.New("wrong tree identity")
		}
		return nil
	}
	x.h.entrySafety = func(a *Attempt, _ *Unit) error {
		a.EntryRuntime.Host = &R1EntryHost{Kernel: "fixed-test-kernel", Client: "systemd fixed-test-version", Manager: "fixed-test-manager", AvailableKB: 1 << 20, Ancestors: []R1EntryAncestor{{Boundary: Boundary{root, FileIdentity{Device: 7, Inode: 9}}, Max: "max", Current: 64 << 20}}}
		return f.preflightErr
	}
	x.h.entryMembers = func(want Boundary) ([]Process, error) {
		if want != x.service.Boundary {
			t.Fatal("wrong member boundary")
		}
		return f.members, nil
	}
	x.h.entryEndpoints = func(main, child Process) (R1EntryEndpoints, error) { f.observed = true; return f.endpoints, nil }
	x.h.process = func(pid int) (Process, error) {
		if pid == os.Getpid() {
			return fixtureProcess(pid, FileIdentity{Inode: 999}, root+"/app.slice/controller.service"), nil
		}
		if pid == 100 && !f.wrote {
			return *x.service.Main, nil
		}
		for _, p := range f.members {
			if p.PID == pid {
				return p, nil
			}
		}
		return Process{}, syscall.ENOENT
	}
	x.h.handle = func(p Process) (*os.File, error) { return os.Open(filepath.Join(path, "output")) }
	x.h.exit = func(*os.File) (bool, error) { return x.dead, nil }
	x.h.ended = func(Process) (bool, error) { return x.dead, nil }
	x.h.population = func(want Boundary) (bool, error) {
		if want != x.parent.Boundary {
			return false, syscall.ENODEV
		}
		return f.serviceStarted && !x.dead, nil
	}
	x.h.pause = func() {
		f.clock = f.clock.Add(10 * time.Millisecond)
		if _, err := os.Stat(filepath.Join(path, "release.json")); err == nil {
			if !f.wrote {
				f.writeFrames(t)
			} else if f.observed {
				x.dead = true
			}
		}
	}
	x.h.call = func(tool string, args []string) (string, error) {
		call := strings.Join(args, " ")
		x.calls = append(x.calls, call)
		if len(args) == 1 && args[0] == "--version" {
			return "systemd fixed-test-version\n", nil
		}
		if args[len(args)-1] == "--property=Version" {
			return "Version=fixed-test-manager\n", nil
		}
		if tool == a.Config.Busctl {
			f.requireIntent(t, "launching-slice")
			if !reflect.DeepEqual(args, entryParentArgs(a)) {
				t.Fatal("parent command changed", args)
			}
			f.parentStarted = true
			return `o "/org/freedesktop/systemd1/job/1"` + "\n", nil
		}
		if tool == a.Config.SystemdRun {
			f.requireIntent(t, "launching-service")
			if !reflect.DeepEqual(args, a.Launch) {
				t.Fatal("tool argv changed")
			}
			f.serviceStarted = true
			if x.ackError {
				return "", errors.New("lost tool acknowledgement")
			}
			return "", nil
		}
		if len(args) > 0 && args[0] == "--system" {
			return "LoadState=not-found\nActiveState=inactive\nJob=0\n", nil
		}
		if strings.Contains(call, " stop ") {
			if args[len(args)-1] == a.Service {
				f.requireIntent(t, "cancelling")
				x.dead = true
			} else {
				f.requireIntent(t, "empty")
				f.parentStopped = true
			}
			if x.stopError {
				return "", errors.New("stop failed")
			}
			return "", nil
		}
		if !strings.Contains(call, " show ") {
			t.Fatal("unexpected operation", call)
		}
		required := ""
		for _, arg := range args {
			if strings.HasPrefix(arg, "--property=") {
				required = strings.TrimPrefix(arg, "--property=")
			}
		}
		name := args[len(args)-1]
		props := x.parent.Properties
		started := f.parentStarted
		if name == a.Service {
			props = x.service.Properties
			started = f.serviceStarted
		}
		var out strings.Builder
		for _, k := range strings.Split(required, ",") {
			v := props[k]
			if !started {
				v = ""
				switch k {
				case "Id":
					v = name
				case "LoadState":
					v = "not-found"
				case "ActiveState":
					v = "inactive"
				case "Job":
					v = "0"
				}
			}
			if name == a.Service && x.dead {
				switch k {
				case "ActiveState":
					v = "failed"
				case "MainPID":
					v = "0"
				case "ControlGroup":
					v = ""
				case "Result":
					v = f.serviceResult
				case "ExecMainCode":
					v = "1"
				case "ExecMainStatus":
					v = "1"
				}
			}
			if name == a.Slice && f.parentStopped {
				switch k {
				case "ActiveState":
					v = "inactive"
				case "ControlGroup":
					v = ""
				}
			}
			fmt.Fprintf(&out, "%s=%s\n", k, v)
		}
		return out.String(), nil
	}
	if err := x.c.Save(a, "prepared", ""); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *entryFixture) requireIntent(t *testing.T, phase string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.c.Path, f.a.ID, "intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	var a Attempt
	if err := decodeHostRecord(bytes.NewReader(b), &a); err != nil {
		t.Fatal(err)
	}
	if a.Phase != phase {
		t.Fatal("effect without durable authority", a.Phase, phase)
	}
}

func (f *entryFixture) writeFrames(t *testing.T) {
	t.Helper()
	a := f.a
	binding := a.EntryRuntime.Binding
	id := func(p Process) R1ActorIdentity {
		pid, err := entryNSPID(p)
		if err != nil {
			t.Fatal(err)
		}
		return R1ActorIdentity{pid, p.Start, objectID(p.Executable), objectID(p.Namespaces["pid"])}
	}
	f.ready = R1Evidence{R1Binding: binding, Stage: "ready", Sequence: 0, Ready: &R1Ready{objectID(a.EntryStatic.Control), id(f.members[1]), id(f.members[2])}}
	code := 20
	child := f.ready.Ready.Child
	f.failure = R1Evidence{R1Binding: binding, Stage: "failure", Sequence: 1, Failure: &R1Failure{Stage: "release", Error: "deadline", Cleanup: "not-allocated", Child: R1ChildResult{Identity: &child, Termination: "exited", Reap: "reaped", ExitCode: &code, Token: "abort"}}}
	var b bytes.Buffer
	if err := EncodeR1Evidence(&b, f.ready); err != nil {
		t.Fatal(err)
	}
	if err := EncodeR1Evidence(&b, f.failure); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	if f.output != nil {
		data = f.output(data)
	}
	path := filepath.Join(f.c.Path, a.ID)
	if err := os.WriteFile(filepath.Join(path, "output"), data, 0600); err != nil {
		t.Fatal(err)
	}
	r, _ := json.Marshal(descriptorReceipt(a))
	if err := exclusiveFile(filepath.Join(path, "descriptor-policy.json"), r, 0400); err != nil {
		t.Fatal(err)
	}
	f.wrote = true
}

func TestR1EntryPositiveNormalLifecycle(t *testing.T) {
	f := entryTestFixture(t)
	if err := f.c.run(f.a, f.h); err != nil {
		t.Fatal(err, f.a.Phase, f.a.Stage)
	}
	if !entryObserved(f.a) || f.c.Blocked || !f.parentStopped {
		t.Fatal("entry/timeout/cleanup evidence incomplete", f.a)
	}
	if !f.a.EntryRuntime.WrapperIntent || f.a.EntryRuntime.MarginNS <= 0 {
		t.Fatal("missing entry timing/intent")
	}
	assertNoEntryPressure(t, f)
	if err := validatePlan(f.a, filepath.Join(f.c.Path, f.a.ID)); err != nil {
		t.Fatal(err)
	}
	f.c.Close()
	c, err := OpenCampaign(f.c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Blocked || !entryObserved(c.Records[f.a.ID]) {
		t.Fatal("normal inspection lost entry result")
	}
	var out bytes.Buffer
	c.Close()
	if err := InspectHost(f.c.Path, f.a.ID, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"entry_static"`) || !strings.Contains(out.String(), `"expected_exit":true`) {
		t.Fatal("inspect omitted entry evidence")
	}
}

func assertNoEntryPressure(t *testing.T, f *entryFixture) {
	t.Helper()
	for _, p := range []string{"r1-control/release.json", "r1-control/finish.json", "finish.json"} {
		if _, err := os.Lstat(filepath.Join(f.c.Path, f.a.ID, p)); !os.IsNotExist(err) {
			t.Fatal("prohibited publication", p, err)
		}
	}
	for _, call := range f.calls {
		if strings.Contains(call, "set-property") || strings.Contains(call, "release.json") || strings.Contains(call, "finish.json") {
			t.Fatal("unexpected authority", call)
		}
	}
}

func TestR1EntrySelectionAndImmutableLaunch(t *testing.T) {
	f := entryTestFixture(t)
	a := f.a
	if err := a.Config.validate(); err != nil {
		t.Fatal(err)
	}
	wantParent := []string{"--user", "--timeout=5s", "call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager", "StartTransientUnit", "ssa(sv)a(sa(sv))", a.Slice, "fail", "8", "Description", "s", "ataniq-entry-" + a.ID, "StopWhenUnneeded", "b", "false", "MemoryMax", "t", "268435456", "MemorySwapMax", "t", "0", "MemoryHigh", "t", "18446744073709551615", "TasksMax", "t", "64", "CPUQuotaPerSecUSec", "t", "1000000", "DefaultDependencies", "b", "true", "0"}
	if !reflect.DeepEqual(entryParentArgs(a), wantParent) {
		t.Fatal("fixed parent authority changed")
	}
	b, _ := json.Marshal(a.Config)
	for _, extra := range []string{`,"allow_pressure":true}`, `,"memory_bytes":33554432}`, `,"command":["arbitrary"]}`} {
		if _, err := DecodeHostConfig(bytes.NewReader(append(append([]byte(nil), b[:len(b)-1]...), extra...))); err == nil {
			t.Fatal("arbitrary authority selector accepted", extra)
		}
	}
	for _, mutate := range []func(*HostConfig){func(c *HostConfig) { c.Case = "R1" }, func(c *HostConfig) { c.Role = "direct" }, func(c *HostConfig) { c.Busctl = "" }, func(c *HostConfig) { c.DirectControl = strings.Repeat("a", 32) }, func(c *HostConfig) { c.DescriptorProfile = FD1TypedProfile }} {
		c := a.Config
		mutate(&c)
		if c.validate() == nil {
			t.Fatal("unsupported entry selector accepted", c)
		}
	}
	args := strings.Join(a.TargetCommand, " ")
	if !strings.HasSuffix(args, "-- /probe --r1-memory") || strings.Contains(args, "fixture-root") || !strings.Contains(args, "--dev-bind /dev/null /dev/null") || strings.Contains(args, "--dev /dev") {
		t.Fatal(args)
	}
	for _, want := range []string{"RuntimeMaxSec=30s", "TimeoutStopSec=3s", "MemoryMax=100663296", "MemorySwapMax=0", "MemoryHigh=infinity", "OOMPolicy=kill", "Delegate=no"} {
		if !strings.Contains(strings.Join(a.Launch, " "), want) {
			t.Fatal(want)
		}
	}
	digest := a.PlanSHA256
	a.EntryStatic.Request.Challenge = strings.Repeat("0", 32)
	if planDigest(a) == digest || validatePlan(a, filepath.Join(f.c.Path, a.ID)) == nil {
		t.Fatal("entry request not immutable")
	}
}

func TestR1EntryProhibitedActionsAndControls(t *testing.T) {
	for _, name := range []string{"pressure-release", "finish", "release", "arbitrary"} {
		f := entryTestFixture(t)
		if entryAction(f.a, name) == nil {
			t.Fatal(name)
		}
	}
	for _, file := range []string{"r1-control/release.json", "r1-control/finish.json", "finish.json", "release.json"} {
		t.Run(file, func(t *testing.T) {
			f := entryTestFixture(t)
			if err := exclusiveFile(filepath.Join(f.c.Path, f.a.ID, file), []byte("inert"), 0400); err != nil {
				t.Fatal(err)
			}
			if f.c.run(f.a, f.h) == nil || f.a.EntryIntegrityFailure == "" {
				t.Fatal("preexisting control admitted")
			}
			if f.serviceStarted {
				t.Fatal("launch after prohibited control")
			}
		})
	}
	f := entryTestFixture(t)
	if err := f.c.run(f.a, f.h); err != nil {
		t.Fatal(err)
	}
	calls := len(f.calls)
	if f.c.run(f.a, f.h) == nil || f.c.wrapperEntry(f.a) == nil || f.c.publishEntryContext(f.a) == nil || len(f.calls) != calls {
		t.Fatal("entry publication replay")
	}
}
