//go:build linux && amd64

package qualification

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type hostFixture struct {
	c         *Campaign
	a         *Attempt
	h         hostIO
	parent    *Unit
	service   *Unit
	calls     []string
	stopError bool
	removed   bool
	pop       bool
	dead      bool
	ackError  bool
	fresh     bool
	hold      bool
}

var syntheticFile = fixtureBytes(strings.Repeat("e", 32))

func fixtureProcess(pid int, exe FileIdentity, cgroup string) Process {
	ns := map[string]FileIdentity{}
	for i, n := range []string{"pid", "mnt", "net", "user", "ipc", "uts"} {
		ns[n] = FileIdentity{Device: 1, Inode: uint64(i + 10)}
	}
	return Process{Boot: "00000000-0000-0000-0000-000000000000", PID: pid, Start: 123, Executable: exe, Cgroup: cgroup, Namespaces: ns, Status: fmt.Sprintf("NSpid:\t%d\nNoNewPrivs:\t1\n", pid), UIDMap: "0 1000 1\n", GIDMap: "0 1000 1\n", Mounts: MountState{Root: "1 0 0:1 / / ro - tmpfs tmpfs ro"}}
}

func testHost(t *testing.T) *hostFixture {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := OpenCampaign(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	x := &hostFixture{c: c, pop: true}
	config := HostConfig{Version: 1, Campaign: path, Role: "direct", RoleID: "0011223344556677", RunID: "0123456789abcdef", RequestID: "fedcba9876543210", Case: "inventory", ReviewedBPF: true}
	tools := t.TempDir()
	for _, n := range []string{"driver", "probe", "bwrap", "bpf", "systemctl", "systemd-run"} {
		if err := os.WriteFile(filepath.Join(tools, n), []byte(n), 0500); err != nil {
			t.Fatal(err)
		}
	}
	config.Driver = filepath.Join(tools, "driver")
	config.Probe = filepath.Join(tools, "probe")
	config.Bwrap = filepath.Join(tools, "bwrap")
	config.BPF = filepath.Join(tools, "bpf")
	config.Systemctl = filepath.Join(tools, "systemctl")
	config.SystemdRun = filepath.Join(tools, "systemd-run")
	a, err := c.fresh(config)
	if err != nil {
		t.Fatal(err)
	}
	x.a = a
	a.Boot = "00000000-0000-0000-0000-000000000000"
	a.FixtureNonce = strings.Repeat("e", 32)
	for n, p := range selected(config) {
		s, _, err := subject(p, n != "bpf", false, false, 65536)
		if err != nil {
			t.Fatal(err)
		}
		a.Subjects[n] = s
	}
	p := filepath.Join(path, a.ID)
	a.Command = command(a, p)
	a.Launch = launch(a, p)
	a.Cleanup = []string{"stop " + a.Service, "stop " + a.Slice}
	if err = c.create(a); err != nil {
		t.Fatal(err)
	}
	a.Snapshots = map[string]Subject{}
	for _, n := range []string{"driver", "probe", "bwrap", "bpf"} {
		if err := exclusiveFile(filepath.Join(p, n), []byte(n), 0500); err != nil {
			t.Fatal(err)
		}
		s, _, err := subject(filepath.Join(p, n), true, false, true, 65536)
		if err != nil {
			t.Fatal(err)
		}
		a.Snapshots[n] = s
	}
	if err = os.Mkdir(filepath.Join(p, "fixtures"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = exclusiveFile(filepath.Join(p, "fixtures", "fake-file"), syntheticFile, 0400); err != nil {
		t.Fatal(err)
	}
	a.Fixture, _, err = subject(filepath.Join(p, "fixtures", "fake-file"), false, false, true, 4096)
	if err != nil {
		t.Fatal(err)
	}
	a.Request = Request{Version: 1, RunID: config.RunID, RequestID: config.RequestID, Case: CaseInventory, MaxDurationMS: 1500, MaxBytes: 4096}
	parentPath := "/owned/" + a.Slice
	pp := map[string]string{"Id": a.Slice, "LoadState": "loaded", "ActiveState": "active", "StopWhenUnneeded": "no", "ControlGroup": parentPath, "InvocationID": strings.Repeat("a", 32)}
	pp["Job"] = "0"
	x.parent = &Unit{Properties: pp, Boundary: Boundary{parentPath, FileIdentity{Device: 7, Inode: 10}}}
	sp := map[string]string{"Id": a.Service, "LoadState": "loaded", "ActiveState": "active", "ControlGroup": parentPath + "/" + a.Service, "Slice": a.Slice, "InvocationID": strings.Repeat("b", 32), "Description": "ataniq-qualification-" + a.ID, "Type": "exec", "Transient": "yes", "KillMode": "control-group", "NoNewPrivileges": "yes", "RuntimeMaxUSec": "20s", "TimeoutStopUSec": "3s", "MemoryMax": "67108864", "TasksMax": "32", "CPUQuotaPerSecUSec": "1s", "LimitNOFILE": "1024", "LimitFSIZE": "131072", "StandardInput": "file", "StandardOutput": "append", "StandardError": "append", "MainPID": "100"}
	sp["ExecStart"] = "{ path=" + filepath.Join(p, "driver") + " ; argv[]=" + filepath.Join(p, "driver") + " private-inject " + p + " ; ignore_errors=no ; }"
	sp["Restart"] = "no"
	sp["RemainAfterExit"] = "no"
	sp["Job"] = "0"
	main := fixtureProcess(100, a.Snapshots["driver"].Identity, sp["ControlGroup"])
	x.service = &Unit{Properties: sp, Boundary: Boundary{sp["ControlGroup"], FileIdentity{Device: 7, Inode: 11}}, Main: &main}
	a.Streams = map[string]FileIdentity{}
	for _, name := range []string{"request", "output", "stderr"} {
		file := name
		if name == "request" {
			file = "request.json"
		}
		if err = exclusiveFile(filepath.Join(p, file), nil, 0600); err != nil {
			t.Fatal(err)
		}
		s, _, e := subject(filepath.Join(p, file), false, false, true, MaxOutputBytes)
		if e != nil {
			t.Fatal(e)
		}
		a.Streams[name] = s.Identity
	}
	i := Injection{1, a.ID, sp["InvocationID"], *x.service.Main, x.service.Boundary, a.Fixture, expectedDescriptors(a), a.Profile, config.RunID, config.RequestID, config.RoleID, a.DescriptorPolicy}
	b, _ := json.Marshal(i)
	if err = exclusiveFile(filepath.Join(p, "injection.json"), b, 0400); err != nil {
		t.Fatal(err)
	}
	o := Observation{Version: 1, RunID: config.RunID, RequestID: config.RequestID, Case: CaseInventory, Status: "observed", Facts: []Fact{}}
	var output bytes.Buffer
	if err = EncodeObservation(&output, o); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p, "output"), output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	a.PlanSHA256 = planDigest(a)
	if err = c.Save(a, "prepared", ""); err != nil {
		t.Fatal(err)
	}
	x.h = hostIO{
		call: func(_ string, args []string) (string, error) {
			call := strings.Join(args, " ")
			x.calls = append(x.calls, call)
			if strings.Contains(call, " show ") {
				u := x.parent
				if args[len(args)-1] == a.Service {
					u = x.service
				}
				if u == nil {
					return "", errors.New("missing/collected unit")
				}
				var b strings.Builder
				required := strings.TrimPrefix(args[4], "--property=")
				if required == freshProperties && !x.fresh && a.Parent == nil {
					load := "not-found"
					if args[len(args)-1] == a.Slice {
						load = "loaded"
					}
					for _, k := range strings.Split(required, ",") {
						v := ""
						switch k {
						case "Id":
							v = args[len(args)-1]
						case "LoadState":
							v = load
						case "ActiveState":
							v = "inactive"
						case "Job":
							v = "0"
						}
						fmt.Fprintf(&b, "%s=%s\n", k, v)
					}
					return b.String(), nil
				}
				_, released := os.Stat(filepath.Join(p, "release.json"))
				for _, k := range strings.Split(required, ",") {
					v := u.Properties[k]
					if u == x.service && (x.dead || released == nil && !x.hold) {
						switch k {
						case "ExecStart":
							v = strings.Replace(v, "ignore_errors=no", "ignore_errors=no ; stop_time=[synthetic terminal]", 1)
						case "MainPID":
							v = "0"
						case "ActiveState":
							v = "inactive"
						case "ControlGroup":
							v = ""
						}
					}
					fmt.Fprintf(&b, "%s=%s\n", k, v)
				}
				return b.String(), nil
			}
			if strings.Contains(call, " stop ") {
				if x.stopError {
					return "", errors.New("cleanupfailure")
				}
				x.pop = false
				x.dead = true
			}
			if strings.Contains(call, "--service-type=exec") {
				if x.ackError {
					return "", errors.New("lost ack")
				}
			}
			return "", nil
		},
		boundary: func(p string) (Boundary, error) {
			if x.removed {
				return Boundary{}, syscall.ENODEV
			}
			if p == x.parent.Boundary.Path {
				return x.parent.Boundary, nil
			}
			return x.service.Boundary, nil
		},
		population: func(b Boundary) (bool, error) {
			if x.removed || b != x.parent.Boundary {
				return false, syscall.ENODEV
			}
			if _, err := os.Stat(filepath.Join(p, "release.json")); err == nil && !x.hold {
				return false, nil
			}
			return x.pop, nil
		},
		process: func(pid int) (Process, error) {
			if pid == os.Getpid() {
				return fixtureProcess(pid, FileIdentity{Inode: 99}, "/controller"), nil
			}
			return *x.service.Main, nil
		},
		ended:  func(Process) (bool, error) { return x.dead, nil },
		handle: func(Process) (*os.File, error) { return os.Open(filepath.Join(p, "output")) },
		exit: func(*os.File) (bool, error) {
			if _, err := os.Stat(filepath.Join(p, "release.json")); err == nil && !x.hold {
				return true, nil
			}
			return x.dead, nil
		},
		helper: func(Boundary, FileIdentity) (Process, error) { return Process{}, errors.New("no helper") },
		budget: func(*Unit) error { return nil }, pause: func() {},
	}
	return x
}

func TestCampaignLockAndStrictRestartFence(t *testing.T) {
	x := testHost(t)
	if _, err := OpenCampaign(x.c.Path); err == nil {
		t.Fatal("second controller acquired lock")
	}
	x.c.Close()
	c, err := OpenCampaign(x.c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Blocked || c.Records[x.a.ID] == nil {
		t.Fatal("prepared restart not fenced")
	}
	if _, err = c.Prepare(x.a.Config); err == nil {
		t.Fatal("new start accepted")
	}
}

func TestLostAckIsDurableAndNeverRetried(t *testing.T) {
	x := testHost(t)
	x.ackError = true
	if err := x.c.run(x.a, x.h); err == nil {
		t.Fatal("lost acknowledgement succeeded")
	}
	if x.a.Phase != "launch-unknown" || !x.c.Blocked {
		t.Fatal("not fenced")
	}
	starts := 0
	for _, call := range x.calls {
		if strings.Contains(call, "--service-type=exec") {
			starts++
		}
	}
	if starts != 1 {
		t.Fatal("retry")
	}
	x.c.Close()
	c, err := OpenCampaign(x.c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Blocked || c.Records[x.a.ID].Phase != "launch-unknown" {
		t.Fatal("restart lost unknown")
	}
}

func bindFixture(t *testing.T, x *hostFixture) {
	t.Helper()
	i, err := readInjection(filepath.Join(x.c.Path, x.a.ID, "injection.json"))
	if err != nil {
		t.Fatal(err)
	}
	x.a.Parent = x.parent
	x.a.Workload = x.service
	x.a.Injector = &i
	p := i.Process
	x.a.Target = &p
	x.a.PopulatedLive = true
	if err = x.c.Save(x.a, "bound", ""); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileIdentityFailuresNeverSignal(t *testing.T) {
	for _, which := range []string{"wrong-invocation", "wrong-cgroup", "wrong-process", "missing-unit", "removed-cgroup", "wrong-parent", "no-retention"} {
		t.Run(which, func(t *testing.T) {
			x := testHost(t)
			bindFixture(t, x)
			// Persisted evidence must not alias a changing fixture snapshot.
			b, _ := json.Marshal(x.a)
			var copy Attempt
			json.Unmarshal(b, &copy)
			x.a = &copy
			switch which {
			case "wrong-invocation":
				x.service.Properties["InvocationID"] = strings.Repeat("c", 32)
			case "wrong-cgroup":
				x.service.Boundary.Identity.Inode++
			case "wrong-process":
				x.service.Main.Start++
			case "missing-unit":
				x.service = nil
			case "removed-cgroup":
				x.removed = true
			case "wrong-parent":
				x.parent.Boundary.Identity.Inode++
			case "no-retention":
				x.parent.Properties["StopWhenUnneeded"] = "yes"
			}
			if err := x.c.reconcile(x.a, x.h); err == nil {
				t.Fatal("uncertain state reconciled")
			}
			for _, call := range x.calls {
				if strings.Contains(call, " stop ") || strings.Contains(call, " start ") || strings.Contains(call, "--service-type") {
					t.Fatal("foreign or replacement action", call)
				}
			}
			if !x.c.Blocked || x.a.Phase != "cleanup-unknown" {
				t.Fatal("not fenced")
			}
		})
	}
}

func TestScopedReconcileAndCleanupFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			x := testHost(t)
			bindFixture(t, x)
			x.stopError = fail
			err := x.c.reconcile(x.a, x.h)
			if fail && (err == nil || x.a.Phase != "cleanup-unknown") {
				t.Fatal("cleanupfailure accepted")
			}
			if !fail && (err != nil || x.a.Phase != "complete" || !x.a.Empty || !x.a.TargetExited) {
				t.Fatalf("cleanup: %v %s", err, x.a.Phase)
			}
			for _, call := range x.calls {
				if strings.Contains(call, " start ") || strings.Contains(call, "--service-type") {
					t.Fatal("reconcile launched")
				}
			}
		})
	}
}

func TestNoEmptyProofFromRemovedBoundaryOrExitZero(t *testing.T) {
	for _, which := range []string{"removed", "still-populated", "target-alive"} {
		t.Run(which, func(t *testing.T) {
			x := testHost(t)
			bindFixture(t, x)
			x.dead = true
			x.pop = false
			switch which {
			case "removed":
				x.removed = true
			case "still-populated":
				x.pop = true
			case "target-alive":
				x.dead = false
			}
			if err := x.c.finishFast(x.a, x.h, nil); err == nil || x.a.Empty || x.a.Phase != "cleanup-unknown" {
				t.Fatal("empty falsely established")
			}
		})
	}
}

func TestSyncFailuresFenceBeforeDispatch(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			x := testHost(t)
			fail := func(*os.File) error { return errors.New("sync failed") }
			if kind == "file" {
				x.c.syncFile = fail
			} else {
				x.c.syncDir = fail
			}
			if x.c.run(x.a, x.h) == nil || len(x.calls) != 0 || !x.c.Blocked {
				t.Fatal("dispatch after failed intent sync")
			}
			x.c.Close()
			c, err := OpenCampaign(x.c.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if !c.Blocked {
				t.Fatal("restart cleared failed sync")
			}
		})
	}
}

func TestMalformedCampaignRecordsBlock(t *testing.T) {
	for _, bad := range []string{"{", `{"version":1,"version":1}`, `{"Version":1}`, `{"version":null}`, `{"version":999}`, strings.Repeat("x", MaxInputBytes+1)} {
		t.Run(fmt.Sprint(len(bad)), func(t *testing.T) {
			x := testHost(t)
			x.c.Close()
			if err := os.WriteFile(filepath.Join(x.c.Path, x.a.ID, "intent.json"), []byte(bad), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := OpenCampaign(x.c.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if !c.Blocked {
				t.Fatal("malformed startup accepted")
			}
		})
	}
}

func TestSparseFDAndFakeIdentity(t *testing.T) {
	for _, used := range []map[int]bool{{FakeFD: true}, {BPFFD: true}} {
		if vacantFDs(1024, used) == nil {
			t.Fatal("collision accepted")
		}
	}
	if vacantFDs(513, nil) == nil || vacantFDs(1024, map[int]bool{0: true, 1: true, 2: true}) != nil {
		t.Fatal("bad rlimit check")
	}
	p := filepath.Join(t.TempDir(), "fake-file")
	if err := os.WriteFile(p, syntheticFile, 0400); err != nil {
		t.Fatal(err)
	}
	s, _, err := subject(p, false, false, true, 4096)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = verifyFakeFD(int(f.Fd()), s); err != nil {
		t.Fatal(err)
	}
	s.Identity.Inode++
	if verifyFakeFD(int(f.Fd()), s) == nil {
		t.Fatal("identity mismatch accepted")
	}
}

func TestHoldReadyBeforeEOFAndExactlyTwoFrames(t *testing.T) {
	r := Request{Version: 1, RunID: "0123456789abcdef", RequestID: "fedcba9876543210", Case: CaseInventory, MaxDurationMS: 1000, MaxBytes: 4096}
	p := fixtureProcess(42, FileIdentity{Device: 1, Inode: 99}, "/owned")
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	release := make(chan struct{})
	go func() {
		done <- readyHold(r, writer, p, func() string { <-release; return "cancelled" })
		writer.Close()
	}()
	ready, err := ReadHoldReady(reader)
	if err != nil || ready.Kind != "ready" {
		t.Fatal(err)
	}
	close(release)
	b, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	final, err := decodeHold(bytes.NewReader(b))
	if err != nil || final.Kind != "final" || final.Reason != "cancelled" {
		t.Fatal(err)
	}
	for _, bad := range []string{"", `{"version":1}`, strings.Repeat("x", 8193)} {
		if _, err = ReadHoldReady(strings.NewReader(bad)); err == nil {
			t.Fatal("bad readiness accepted")
		}
	}
}

func TestExplicitSubjectsRejectLinksAndNonregular(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "file")
	if err := os.WriteFile(p, []byte("fixture"), 0400); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := subject(link, false, false, true, 4096); err == nil {
		t.Fatal("link accepted")
	}
	if _, _, err := subject(d, false, false, true, 4096); err == nil {
		t.Fatal("directory accepted")
	}
	if err := os.Link(p, filepath.Join(d, "hard")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := subject(p, false, false, true, 4096); err == nil {
		t.Fatal("hardlinks accepted")
	}
}

func TestFastCaseBindsBeforeReleaseAndCollectsAfterEmpty(t *testing.T) {
	x := testHost(t)
	var populations []bool
	observe := x.h.population
	x.h.population = func(b Boundary) (bool, error) { p, e := observe(b); populations = append(populations, p); return p, e }
	if err := x.c.run(x.a, x.h); err != nil {
		t.Fatal(err)
	}
	if x.a.Phase != "complete" || x.a.Observation == nil || !x.a.Empty || len(populations) < 2 || !populations[0] || populations[len(populations)-1] {
		t.Fatal("missing live-to-empty evidence")
	}
	x.c.Close()
	c, err := OpenCampaign(x.c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Blocked || c.Records[x.a.ID] == nil {
		t.Fatal("valid completion not retained")
	}
}

func TestEveryUpdateCrashWindowBlocksRestart(t *testing.T) {
	for _, kind := range []string{"file", "rename", "directory"} {
		for step := 1; step <= 10; step++ {
			t.Run(fmt.Sprintf("%s-%d", kind, step), func(t *testing.T) {
				x := testHost(t)
				n := 0
				fail := func(f *os.File) error {
					n++
					if n == step {
						return errors.New("syncfailure")
					}
					return f.Sync()
				}
				switch kind {
				case "file":
					x.c.syncFile = fail
				case "directory":
					x.c.syncDir = fail
				case "rename":
					x.c.rename = func(a int, b string, c int, d string) error {
						n++
						if n == step {
							return errors.New("renamefailure")
						}
						return syscall.Renameat(a, b, c, d)
					}
				}
				if err := x.c.run(x.a, x.h); err == nil {
					t.Fatal("update failure disappeared")
				}
				x.c.Close()
				c, err := OpenCampaign(x.c.Path)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if !c.Blocked {
					t.Fatal("restart accepted failed durability")
				}
			})
		}
	}
}

func TestLaunchPartialAndMissingAfterLostAckRemainUnknown(t *testing.T) {
	for _, which := range []string{"slice-only", "collected-service", "removed-parent", "late-service"} {
		t.Run(which, func(t *testing.T) {
			x := testHost(t)
			x.ackError = true
			if x.c.run(x.a, x.h) == nil {
				t.Fatal("expected launch-unknown")
			}
			switch which {
			case "slice-only", "collected-service":
				x.service = nil
			case "removed-parent":
				x.removed = true
			case "late-service":
				x.service.Properties["Description"] = "foreign"
			}
			before := len(x.calls)
			if x.c.reconcile(x.a, x.h) == nil || !x.c.Blocked {
				t.Fatal("unknown cleared")
			}
			for _, call := range x.calls[before:] {
				if strings.Contains(call, " stop ") || strings.Contains(call, " start ") || strings.Contains(call, "--service-type") {
					t.Fatal("unbound effect", call)
				}
			}
		})
	}
}

func TestInjectionAndReadyMismatchAreInconclusive(t *testing.T) {
	x := testHost(t)
	bindFixture(t, x)
	for _, which := range []string{"invocation", "fixture", "fd", "cloexec", "role", "start"} {
		t.Run(which, func(t *testing.T) {
			b, _ := json.Marshal(x.a.Injector)
			var i Injection
			json.Unmarshal(b, &i)
			switch which {
			case "invocation":
				i.Invocation = strings.Repeat("d", 32)
			case "fixture":
				i.Fixture.Identity.Inode++
			case "fd":
				i.Descriptors[3].FD = BPFFD
			case "cloexec":
				i.Descriptors[3].Cloexec = true
			case "role":
				i.RoleID = "deadbeef"
			case "start":
				i.Process.Start++
			}
			if validateInjection(x.a, i, x.service) == nil {
				t.Fatal("invalid injection accepted")
			}
		})
	}
	p := fixtureProcess(100, x.a.Snapshots["probe"].Identity, x.service.Boundary.Path)
	f := HoldFrame{Version: 1, RunID: x.a.Config.RunID, RequestID: x.a.Config.RequestID, Kind: "ready", PID: p.PID, Start: p.Start, Executable: p.Executable, PIDNamespace: p.Namespaces["pid"]}
	if validateReady(x.a, f, p) != nil {
		t.Fatal("valid direct readiness")
	}
	f.Start++
	if validateReady(x.a, f, p) == nil {
		t.Fatal("stale process readiness accepted")
	}
}

func TestHostConfigStrictAndHandshakeExclusive(t *testing.T) {
	x := testHost(t)
	b, _ := json.Marshal(x.a.Config)
	if _, err := DecodeHostConfig(bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(b), `"version":1`, `"version":1,"version":1`, 1), strings.Replace(string(b), `"version"`, `"Version"`, 1), string(b) + ` {}`, strings.Replace(string(b), `"version":1`, `"version":null`, 1)} {
		if _, err := DecodeHostConfig(strings.NewReader(bad)); err == nil {
			t.Fatal("malformed config accepted")
		}
	}
	d := t.TempDir()
	if err := publishOwned(d, "release.json", []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := publishOwned(d, "release.json", []byte(`{"version":2}`)); err == nil {
		t.Fatal("marker reused")
	}
	got, err := readOwned(filepath.Join(d, "release.json"), 4096)
	if err != nil || string(got) != `{"version":1}` {
		t.Fatal("publication replaced", err)
	}
}

func TestCandidateReadyRequiresActualNamespacesPrivilegesAndMounts(t *testing.T) {
	x := testHost(t)
	bindFixture(t, x)
	x.a.Config.Role = "candidate"
	p := fixtureProcess(101, x.a.Snapshots["probe"].Identity, x.service.Boundary.Path)
	for name, ns := range p.Namespaces {
		ns.Inode += 100
		p.Namespaces[name] = ns
	}
	p.Status = "NSpid:\t101\t2\nNoNewPrivs:\t1\nSeccomp:\t2\n"
	for _, name := range []string{"CapInh", "CapAmb", "CapEff", "CapPrm", "CapBnd"} {
		p.Status += name + ":\t0000000000000000\n"
	}
	p.Mounts = MountState{Root: "1 0 0:1 / / ro - tmpfs tmpfs ro", Probe: "2 1 0:2 / /probe ro - ext4 fixture ro", Fixture: "3 1 0:2 / /fixtures/fake-file ro - ext4 fixture ro"}
	f := HoldFrame{Version: 1, RunID: x.a.Config.RunID, RequestID: x.a.Config.RequestID, Kind: "ready", PID: 2, Start: p.Start, Executable: p.Executable, PIDNamespace: p.Namespaces["pid"]}
	if err := validateReady(x.a, f, p); err != nil {
		t.Fatal(err)
	}
	p.Mounts.Root = strings.Replace(p.Mounts.Root, " ro ", " rw ", 1)
	if validateReady(x.a, f, p) == nil {
		t.Fatal("writable root accepted")
	}
	p.Mounts.Root = "1 0 0:1 / / ro - tmpfs tmpfs ro"
	p.Namespaces["net"] = x.a.Injector.Process.Namespaces["net"]
	if validateReady(x.a, f, p) == nil {
		t.Fatal("shared network namespace accepted")
	}
}

func TestMountStateRejectsAmbiguity(t *testing.T) {
	line := "1 0 0:1 / / ro - tmpfs tmpfs ro\n"
	if m, err := mountState([]byte(line)); err != nil || !readOnlyMount(m.Root) {
		t.Fatal(err)
	}
	for _, bad := range []string{line + line, "truncated\n", strings.Replace(line, " / / ", " / /other ", 1)} {
		if _, err := mountState([]byte(bad)); err == nil {
			t.Fatal("ambiguous/missing root accepted")
		}
	}
}

func TestPidfdTerminalEvents(t *testing.T) {
	for _, v := range []int16{0, 1, 16, 17, 8, 32, 9, 33} {
		dead, err := pidfdEvents(v)
		bad := v&^int16(17) != 0
		if (err != nil) != bad || !bad && dead != (v != 0) {
			t.Fatalf("poll events %d: %v %v", v, dead, err)
		}
	}
}

func TestRecoveryUsesStarttimeNotExecutable(t *testing.T) {
	for _, tc := range []struct {
		state string
		start uint64
		dead  bool
	}{{"S", 123, false}, {"Z", 123, true}, {"X", 123, true}, {"R", 124, true}} {
		fields := make([]string, 20)
		for i := range fields {
			fields[i] = "0"
		}
		fields[0] = tc.state
		fields[19] = fmt.Sprint(tc.start)
		dead, err := originalEnded(123, "100 (synthetic ) comm) "+strings.Join(fields, " "))
		if err != nil || dead != tc.dead {
			t.Fatalf("%+v: %v %v", tc, dead, err)
		}
	}
	for _, b := range []string{"", "100 (x) R", "100 malformed"} {
		if _, err := originalEnded(123, b); err == nil {
			t.Fatal("malformed recovery accepted")
		}
	}
}

func TestRestartAfterBoundCompletionIsCleanupOnly(t *testing.T) {
	for _, which := range []string{"collected", "inactive", "reused-pid", "boot-changed", "parent-replaced", "pending-job", "restart", "live-original", "permission", "unbound"} {
		t.Run(which, func(t *testing.T) {
			x := testHost(t)
			bindFixture(t, x)
			if which == "unbound" {
				x.a.Injector = nil
				x.a.Target = nil
				x.a.Workload = nil
				x.a.PopulatedLive = false
			}
			if err := x.c.Save(x.a, "cleanup-unknown", ""); err != nil {
				t.Fatal(err)
			}
			x.c.Close()
			c, err := OpenCampaign(x.c.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			x.c = c
			x.a = c.Records[x.a.ID]
			x.pop = false
			x.dead = true
			call := x.h.call
			x.h.call = func(path string, args []string) (string, error) {
				if which == "collected" && strings.Contains(strings.Join(args, " "), " show ") && args[len(args)-1] == x.a.Service {
					x.calls = append(x.calls, strings.Join(args, " "))
					return "Id=" + x.a.Service + "\nLoadState=not-found\nActiveState=inactive\nControlGroup=\nFragmentPath=\nJob=0\n", nil
				}
				return call(path, args)
			}
			switch which {
			case "reused-pid":
				x.h.ended = func(p Process) (bool, error) {
					return originalEnded(p.Start, "100 (other exe) S "+strings.Repeat("0 ", 18)+"124")
				}
			case "boot-changed":
				old := x.h.process
				x.h.process = func(pid int) (Process, error) {
					p, e := old(pid)
					p.Boot = "11111111-1111-1111-1111-111111111111"
					return p, e
				}
			case "parent-replaced":
				x.parent.Boundary.Identity.Inode++
			case "pending-job":
				x.service.Properties["Job"] = "37"
			case "restart":
				x.service.Properties["Restart"] = "always"
			case "live-original":
				x.h.ended = func(Process) (bool, error) { return false, nil }
			case "permission":
				x.h.ended = func(Process) (bool, error) { return false, syscall.EACCES }
			}
			err = c.reconcile(x.a, x.h)
			pass := which == "collected" || which == "inactive" || which == "reused-pid"
			if pass && (err != nil || x.a.Phase != "complete" || c.Blocked || x.a.Observation != nil || x.a.Final != nil) {
				t.Fatalf("cleanup recovery failed: %v %+v", err, x.a)
			}
			if !pass && (err == nil || !c.Blocked) {
				t.Fatal("uncertain recovery accepted")
			}
			for _, v := range x.calls {
				if strings.Contains(v, " start ") || strings.Contains(v, "--service-type") || strings.Contains(v, " stop ") && (!pass || !strings.HasSuffix(v, x.a.Slice)) {
					t.Fatal("unauthorized effect", v)
				}
			}
		})
	}
}

func TestFreshNamesAndSeparateUnitShapes(t *testing.T) {
	x := testHost(t)
	if err := x.h.freshNames(x.a); err != nil {
		t.Fatal(err)
	}
	if x.a.FreshSlice["LoadState"] != "loaded" || x.a.FreshService["LoadState"] != "not-found" {
		t.Fatal("fresh generated slice rejected")
	}
	// A realistic Slice transcript omits Service-only properties entirely.
	s := "Id=" + x.a.Slice + "\nLoadState=loaded\nActiveState=active\nSubState=active\nInvocationID=" + strings.Repeat("a", 32) + "\nControlGroup=" + x.parent.Boundary.Path + "\nStopWhenUnneeded=no\nJob=0\n"
	x.h.call = func(_ string, args []string) (string, error) {
		if !strings.Contains(strings.Join(args, " "), "--all --property="+parentProperties) {
			t.Fatal("wrong slice query", args)
		}
		return s, nil
	}
	if _, err := x.h.parent(x.a); err != nil {
		t.Fatal(err)
	}
	if _, err := parseProperties(s, properties); err == nil {
		t.Fatal("service union accepted a slice")
	}
	for _, which := range []string{"existing-service", "active-slice", "slice-fragment", "pending-start"} {
		t.Run(which, func(t *testing.T) {
			y := testHost(t)
			y.fresh = true
			y.service.Properties["LoadState"] = "not-found"
			y.service.Properties["ActiveState"] = "inactive"
			y.service.Properties["ControlGroup"] = ""
			y.parent.Properties["ActiveState"] = "inactive"
			y.parent.Properties["ControlGroup"] = ""
			switch which {
			case "existing-service":
				y.service.Properties["LoadState"] = "loaded"
			case "active-slice":
				y.parent.Properties["ActiveState"] = "active"
			case "slice-fragment":
				y.parent.Properties["FragmentPath"] = "/owned/collision.slice"
			case "pending-start":
				y.service.Properties["Job"] = "18"
			}
			if y.h.freshNames(y.a) == nil {
				t.Fatal("collision accepted")
			}
		})
	}
}

// Build an inert ELF transcript; no selected launcher is executed or resolved.
func launcherBytes(interp string, tag elf.DynTag, value string) []byte {
	b := make([]byte, 1024)
	copy(b, "\x7fELF\x02\x01\x01")
	put16 := func(o int, n uint16) { binary.LittleEndian.PutUint16(b[o:], n) }
	put64 := func(o int, n uint64) { binary.LittleEndian.PutUint64(b[o:], n) }
	put16(16, 3)
	put16(18, uint16(elf.EM_X86_64))
	binary.LittleEndian.PutUint32(b[20:], 1)
	put64(32, 64)
	put64(40, 768)
	put16(52, 64)
	put16(54, 56)
	put16(56, 3)
	put16(58, 64)
	put16(60, 3)
	// Loader-visible extents, independent of the optional section headers.
	binary.LittleEndian.PutUint32(b[64:], uint32(elf.PT_LOAD))
	put64(80, 0x400000)
	put64(96, uint64(len(b)))
	put64(104, uint64(len(b)))
	if interp != "" {
		binary.LittleEndian.PutUint32(b[120:], uint32(elf.PT_INTERP))
		put64(128, 320)
		put64(152, uint64(len(interp)+1))
		put64(160, uint64(len(interp)+1))
		copy(b[320:], interp)
	}
	binary.LittleEndian.PutUint32(b[176:], uint32(elf.PT_DYNAMIC))
	put64(184, 512)
	put64(192, 0x400200)
	put64(208, 64)
	put64(216, 64)
	put64(512, uint64(elf.DT_STRTAB))
	put64(520, 0x400280)
	put64(528, uint64(elf.DT_STRSZ))
	put64(536, uint64(len(value)+2))
	put64(544, uint64(tag))
	put64(552, 1)
	// Congruent section view for the original regression cases.
	binary.LittleEndian.PutUint32(b[768+64+4:], uint32(elf.SHT_DYNAMIC))
	put64(768+64+24, 512)
	put64(768+64+32, 64)
	binary.LittleEndian.PutUint32(b[768+64+40:], 2)
	put64(768+64+56, 16)
	binary.LittleEndian.PutUint32(b[768+128+4:], uint32(elf.SHT_STRTAB))
	put64(768+128+24, 640)
	put64(768+128+32, uint64(len(value)+2))
	copy(b[641:], value)
	return b
}

func launcherTranscript(t *testing.T, interp string, tag elf.DynTag, value string) *elf.File {
	t.Helper()
	e, err := elf.NewFile(bytes.NewReader(launcherBytes(interp, tag, value)))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestBwrapDynamicMetadataWithoutDependencyClaims(t *testing.T) {
	for _, tc := range []struct {
		interp string
		tag    elf.DynTag
		value  string
		ok     bool
	}{{"/lib64/ld-linux-x86-64.so.2", elf.DT_NEEDED, "libc.so.6", true}, {"", elf.DT_NULL, "", true}, {"relative-loader", elf.DT_NULL, "", false}, {"/lib/loader", elf.DT_RPATH, "$ORIGIN", false}, {"/lib/loader", elf.DT_RUNPATH, "/lib", false}, {"/lib/loader", elf.DT_NEEDED, "./relative.so", false}} {
		m, err := launcherELF(launcherTranscript(t, tc.interp, tc.tag, tc.value))
		if (err == nil) != tc.ok {
			t.Fatalf("%+v: %v", tc, err)
		}
		if tc.ok && (m.Interpreter != tc.interp || m.DependencyClosureComplete != (tc.interp == "")) {
			t.Fatal("false dependency closure", m)
		}
	}
	args := strings.Join(BubblewrapArgs("/owned/probe", "/owned/fake"), " ")
	if !strings.Contains(args, "--unshare-all --unshare-user ") {
		t.Fatal("explicit userns missing")
	}
}

func TestDirectControlScopeAndFreshSyntheticNonce(t *testing.T) {
	x := testHost(t)
	bindFixture(t, x)
	d := x.a
	d.DescriptorPolicy = FinalDescriptorPolicy
	d.Config.Case = "fake-fd"
	d.Phase = "complete"
	d.Empty = true
	d.TargetExited = true
	d.SliceStopped = true
	v := "true"
	d.Observation = &Observation{Version: 1, RunID: d.Config.RunID, RequestID: d.Config.RequestID, Case: CaseFakeFD, Status: "observed", Facts: []Fact{{Name: "digest_match", State: "known", Value: &v}}}
	r := descriptorReceipt(d)
	d.DescriptorReceipt = &r
	a := *d
	a.Config.Role = "candidate"
	if err := validateControl(&a, d, d.Created+1); err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"campaign", "run", "boot", "age", "profile", "chain", "injection", "legacy-policy", "missing-receipt"} {
		b, _ := json.Marshal(d)
		var c Attempt
		json.Unmarshal(b, &c)
		switch which {
		case "campaign":
			c.Config.Campaign = "/different"
		case "run":
			c.Config.RunID = "1111111111111111"
		case "boot":
			c.Boot = "11111111-1111-1111-1111-111111111111"
		case "age":
			c.Created -= 301
		case "profile":
			c.Profile = "other"
		case "chain":
			s := c.Subjects["bpf"]
			s.SHA256 = strings.Repeat("0", 64)
			c.Subjects["bpf"] = s
		case "injection":
			c.Injector = nil
		case "legacy-policy":
			c.DescriptorPolicy = ""
		case "missing-receipt":
			c.DescriptorReceipt = nil
		}
		if validateControl(&a, &c, d.Created+1) == nil {
			t.Fatal("unscoped control", which)
		}
	}
	n, err := randomID()
	if err != nil || !attemptID(n) || bytes.Equal(fixtureBytes(n), syntheticFile) {
		t.Fatal("fresh nonce unavailable", err)
	}
}

func TestHoldDriverUsesActualHelperAndRequiresCancelledFinal(t *testing.T) {
	for _, role := range []string{"direct", "candidate"} {
		for _, finalReason := range []string{"cancelled", "self-lifetime"} {
			t.Run(role+"-"+finalReason, func(t *testing.T) {
				x := testHost(t)
				x.hold = true
				x.a.Config.Case = "hold"
				x.a.Config.Role = role
				path := filepath.Join(x.c.Path, x.a.ID)
				x.a.Command = command(x.a, path)
				x.a.Launch = launch(x.a, path)
				x.a.PlanSHA256 = planDigest(x.a)
				p := fixtureProcess(101, x.a.Snapshots["probe"].Identity, x.service.Boundary.Path)
				p.Status = "NSpid:\t101\t2\nNoNewPrivs:\t1\nSeccomp:\t2\n"
				for _, name := range []string{"CapInh", "CapAmb", "CapEff", "CapPrm", "CapBnd"} {
					p.Status += name + ":\t0000000000000000\n"
				}
				if role == "candidate" {
					for name, id := range p.Namespaces {
						id.Inode += 100
						p.Namespaces[name] = id
					}
				}
				p.Mounts = MountState{Root: "1 0 0:1 / / ro - tmpfs tmpfs ro", Probe: "2 1 0:2 / /probe ro - ext4 fixture ro", Fixture: "3 1 0:2 / /fixtures/fake-file ro - ext4 fixture ro"}
				r := HoldFrame{Version: 1, RunID: x.a.Config.RunID, RequestID: x.a.Config.RequestID, Kind: "ready", PID: 2, Start: p.Start, Executable: p.Executable, PIDNamespace: p.Namespaces["pid"]}
				f := r
				f.Kind = "final"
				f.Reason = finalReason
				var output bytes.Buffer
				if err := emitHold(&output, r); err != nil {
					t.Fatal(err)
				}
				if err := emitHold(&output, f); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "output"), output.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				proc := x.h.process
				x.h.process = func(pid int) (Process, error) {
					if pid == p.PID {
						return p, nil
					}
					v, e := proc(pid)
					if pid == 100 {
						if _, err := os.Stat(filepath.Join(path, "release.json")); err == nil {
							v.Executable = x.a.Snapshots["bwrap"].Identity
						}
					}
					return v, e
				}
				x.h.helper = func(b Boundary, id FileIdentity) (Process, error) {
					if b != x.service.Boundary || id != p.Executable {
						t.Fatal("unscoped helper enumeration")
					}
					return p, nil
				}
				err := x.c.run(x.a, x.h)
				if (err == nil) != (finalReason == "cancelled") || !x.a.Empty || !x.a.TargetExited {
					t.Fatalf("hold evidence: %v", err)
				}
				if x.a.Target.PID != 101 || x.a.Workload.Main.PID != 100 || x.a.ExecMain == nil || x.a.ExecMain.Executable != x.a.Snapshots["bwrap"].Identity {
					t.Fatal("helper confused with MainPID")
				}
				if (x.a.Final != nil) != (finalReason == "cancelled") {
					t.Fatal("self lifetime qualified cancellation")
				}
			})
		}
	}
}
