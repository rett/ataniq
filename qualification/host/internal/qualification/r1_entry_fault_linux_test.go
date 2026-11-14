//go:build linux && amd64

package qualification

import (
	"bytes"
	"debug/elf"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestR1EntryRejectsSelectedMetadata(t *testing.T) {
	for _, which := range []string{"null-type", "null-rdev", "null-surrogate", "null-stdin-mode", "null-stdout-mode", "null-mount-id", "nodev", "null-suid", "dev-writable", "control-writable", "control-alternate", "control-replaced", "pipe-kind", "pipe-peer", "pipe-mode", "main-output", "fd-denied", "duplicate-host", "namespace", "duplicate-nspid", "missing-nspid", "pid-reused", "unknown-member", "unknown-branch", "margin", "pidfd", "member-ended", "preflight", "dispatch-null"} {
		t.Run(which, func(t *testing.T) {
			f := entryTestFixture(t)
			switch which {
			case "null-type":
				f.endpoints.ChildStdio[0].Mode = syscall.S_IFREG | 0666
			case "null-rdev":
				f.endpoints.ChildStdio[0].Rdev++
			case "null-surrogate":
				f.endpoints.ChildStdio[0].Identity.Inode++
			case "null-stdin-mode":
				f.endpoints.ChildStdio[0].Flags = syscall.O_WRONLY
			case "null-stdout-mode":
				f.endpoints.ChildStdio[1].Flags = syscall.O_RDONLY
			case "null-mount-id":
				f.endpoints.ChildStdio[2].MountID++
			case "nodev":
				f.endpoints.NullMount = strings.Replace(f.endpoints.NullMount, "rw,nosuid", "rw,nosuid,nodev", 1)
			case "null-suid":
				f.endpoints.NullMount = strings.Replace(f.endpoints.NullMount, "rw,nosuid", "rw", 1)
			case "dev-writable":
				f.endpoints.DevMount = strings.Replace(f.endpoints.DevMount, " ro ", " rw ", 1)
			case "control-writable":
				f.endpoints.ControlMount = strings.Replace(f.endpoints.ControlMount, "ro,nosuid", "rw,nosuid", 1)
			case "control-alternate":
				f.endpoints.ControlMount = strings.Replace(f.endpoints.ControlMount, "/r1-control", "/other", 1)
			case "control-replaced":
				f.endpoints.Control.Inode++
			case "pipe-kind":
				f.endpoints.ChildToken.Mode = syscall.S_IFREG | 0600
			case "pipe-peer":
				f.endpoints.ParentToken.Identity.Inode++
			case "pipe-mode":
				f.endpoints.ChildToken.Flags = syscall.O_RDWR
			case "main-output":
				f.endpoints.MainOutput.Identity.Inode++
			case "fd-denied":
				f.h.entryEndpoints = func(Process, Process) (R1EntryEndpoints, error) { return R1EntryEndpoints{}, syscall.EACCES }
			case "duplicate-host":
				old := f.h.entryMembers
				f.h.entryMembers = func(b Boundary) ([]Process, error) { m, e := old(b); return append(m, m[1]), e }
			case "namespace":
				old := f.h.entryMembers
				f.h.entryMembers = func(b Boundary) ([]Process, error) {
					m, e := old(b)
					copy := append([]Process(nil), m...)
					copy[2].Namespaces = map[string]FileIdentity{}
					return copy, e
				}
			case "duplicate-nspid":
				old := f.h.entryMembers
				f.h.entryMembers = func(b Boundary) ([]Process, error) {
					m, e := old(b)
					copy := append([]Process(nil), m...)
					copy[1].Status += "NSpid:\t101\t2\n"
					return copy, e
				}
			case "missing-nspid":
				old := f.h.entryMembers
				f.h.entryMembers = func(b Boundary) ([]Process, error) {
					m, e := old(b)
					copy := append([]Process(nil), m...)
					copy[1].Status = strings.Replace(copy[1].Status, "NSpid:\t101\t2\n", "", 1)
					return copy, e
				}
			case "pid-reused":
				old := f.h.entryMembers
				f.h.entryMembers = func(b Boundary) ([]Process, error) {
					m, e := old(b)
					copy := append([]Process(nil), m...)
					copy[1].Start++
					return copy, e
				}
			case "unknown-member":
				f.members[0].Executable = FileIdentity{1, 999, 1000}
			case "unknown-branch":
				f.members[1].Status = strings.Replace(f.members[1].Status, "PPid:\t100", "PPid:\t777", 1)
			case "margin":
				old := f.h.entryEndpoints
				f.h.entryEndpoints = func(p, q Process) (R1EntryEndpoints, error) {
					e, err := old(p, q)
					f.clock = f.clock.Add(2 * time.Second)
					return e, err
				}
			case "pidfd":
				old := f.h.handle
				f.h.handle = func(p Process) (*os.File, error) {
					if p.PID == 102 {
						return nil, syscall.EPERM
					}
					return old(p)
				}
			case "member-ended":
				f.h.exit = func(*os.File) (bool, error) { return true, nil }
			case "preflight":
				f.preflightErr = errors.New("unsafe ancestor")
			case "dispatch-null":
				f.h.entryNull = func() (R1NullDevice, error) { n := f.a.EntryStatic.Null; n.Identity.Inode++; return n, nil }
			}
			if err := f.c.run(f.a, f.h); err == nil || entryObserved(f.a) {
				t.Fatal("unproved metadata observed", err)
			}
			if f.serviceStarted && !f.a.Empty {
				t.Fatal("bound cleanup did not complete", f.a.Phase, f.a.Reason)
			}
			assertNoEntryPressure(t, f)
		})
	}
}

func TestR1EntryIncompleteOrUnexpectedTraceCannotPass(t *testing.T) {
	for _, which := range []string{"partial", "missing-failure", "missing-ready", "bad-hash", "duplicate-ready", "abort-timeout", "wrong-exit", "missing-child", "pressure", "retained", "completion", "service-timeout", "output-replaced"} {
		t.Run(which, func(t *testing.T) {
			f := entryTestFixture(t)
			f.output = func(b []byte) []byte {
				lines := bytes.Split(b, []byte{'\n'})
				switch which {
				case "partial":
					return b[:len(b)-1]
				case "missing-failure":
					return append(append([]byte(nil), lines[0]...), '\n')
				case "missing-ready":
					return append(append([]byte(nil), lines[1]...), '\n')
				case "bad-hash":
					return bytes.ReplaceAll(b, []byte(f.a.PlanSHA256), []byte(strings.Repeat("0", 64)))
				case "duplicate-ready":
					return append(b, append(append([]byte(nil), lines[0]...), '\n')...)
				case "abort-timeout":
					e := f.failure
					e.Failure = &R1Failure{Stage: "release", Error: "deadline", Cleanup: "not-allocated", Child: R1ChildResult{Token: "abort", Termination: "unknown", Reap: "unknown", Identity: &f.ready.Ready.Child}}
					var out bytes.Buffer
					EncodeR1Evidence(&out, f.ready)
					EncodeR1Evidence(&out, e)
					return out.Bytes()
				case "wrong-exit":
					return bytes.Replace(b, []byte(`"exit_code":20`), []byte(`"exit_code":21`), 1)
				case "missing-child":
					return bytes.Replace(b, []byte(`"identity":`), []byte(`"wrong_identity":`), 1)
				case "pressure", "retained", "completion":
					e := R1Evidence{R1Binding: f.ready.R1Binding, Stage: "pressure-start", Sequence: 1, Pressure: &R1Pressure{strings.Repeat("a", 64)}}
					if which == "retained" {
						e.Stage = "retained"
						e.Sequence = 2
						e.Pressure = nil
						e.Touched = &R1Touched{strings.Repeat("a", 64), R1MemoryBytes, R1MemoryBytes, 4096, true}
					}
					if which == "completion" {
						e.Stage = "completion"
						e.Sequence = 3
						e.Pressure = nil
						ch := f.failure.Failure.Child
						code := 0
						ch.ExitCode = &code
						ch.Token = "finish"
						e.Completion = &R1Completion{strings.Repeat("a", 64), strings.Repeat("b", 64), R1MemoryBytes, true, ch, "completed"}
					}
					var out bytes.Buffer
					EncodeR1Evidence(&out, f.ready)
					EncodeR1Evidence(&out, e)
					return out.Bytes()
				}
				return b
			}
			if which == "service-timeout" {
				f.serviceResult = "timeout"
			}
			if which == "output-replaced" {
				old := f.h.entryEndpoints
				f.h.entryEndpoints = func(p, q Process) (R1EntryEndpoints, error) {
					path := filepath.Join(f.c.Path, f.a.ID, "output")
					b, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := exclusiveFile(path, b, 0600); err != nil {
						t.Fatal(err)
					}
					return old(p, q)
				}
			}
			if err := f.c.run(f.a, f.h); err == nil || entryObserved(f.a) {
				t.Fatal("incomplete/pressure trace observed", err)
			}
			if !f.a.Empty || !f.parentStopped {
				t.Fatal("trace failure lost independent cleanup", f.a.Phase, f.a.Reason)
			}
			if (which == "pressure" || which == "retained" || which == "completion" || which == "bad-hash" || which == "output-replaced") && f.a.EntryIntegrityFailure == "" {
				t.Fatal("integrity failure not sticky")
			}
			assertNoEntryPressure(t, f)
		})
	}
}

func TestR1EntryIncrementalReadyAndExpectedFailure(t *testing.T) {
	f := entryTestFixture(t)
	var rest []byte
	f.output = func(b []byte) []byte {
		cut := bytes.IndexByte(b, '\n') / 2
		rest = append([]byte(nil), b[cut:]...)
		return b[:cut]
	}
	pause := f.h.pause
	f.h.pause = func() {
		was := f.wrote
		pause()
		if was && len(rest) > 0 {
			file, err := os.OpenFile(filepath.Join(f.c.Path, f.a.ID, "output"), os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write(rest); err != nil {
				t.Fatal(err)
			}
			file.Close()
			rest = nil
		}
	}
	if err := f.c.run(f.a, f.h); err != nil || !entryObserved(f.a) {
		t.Fatal("split readiness failed", err)
	}
}

func TestR1EntryPublicationFaultsNeverReplay(t *testing.T) {
	for _, which := range []string{"before-context", "context-renamed", "context-save", "before-wrapper", "wrapper-renamed", "actor-save"} {
		t.Run(which, func(t *testing.T) {
			f := entryTestFixture(t)
			injected := errors.New("injected entry durability error")
			publish := f.c.entryPublish
			f.c.entryPublish = func(dir, name string, b []byte) error {
				if which == "before-context" && name == R1ContextName || which == "before-wrapper" && name == "release.json" {
					return injected
				}
				if err := publish(dir, name, b); err != nil {
					return err
				}
				if which == "context-renamed" && name == R1ContextName || which == "wrapper-renamed" && name == "release.json" {
					return injected
				}
				if which == "context-save" && name == R1ContextName {
					f.c.syncFile = func(*os.File) error { return injected }
				}
				return nil
			}
			if which == "actor-save" {
				old := f.h.entryEndpoints
				f.h.entryEndpoints = func(p, q Process) (R1EntryEndpoints, error) {
					f.c.syncFile = func(*os.File) error { return injected }
					return old(p, q)
				}
			}
			err := f.c.run(f.a, f.h)
			if err == nil || !errors.Is(err, injected) || entryObserved(f.a) {
				t.Fatal("fault lost", err)
			}
			if which == "actor-save" && (f.a.EntryRuntime.Main == nil || f.a.Injector == nil || f.a.Parent == nil) {
				t.Fatal("in-memory authority lost before failed save")
			}
			before := len(f.calls)
			f.c.Close()
			c, err := OpenCampaign(f.c.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if !c.Blocked {
				t.Fatal("restart lost delivery/durability fence")
			}
			if a := c.Records[f.a.ID]; a != nil {
				_ = c.reconcile(a, f.h)
			}
			for _, call := range f.calls[before:] {
				if strings.Contains(call, "StartTransientUnit") || strings.Contains(call, "--service-type") || strings.Contains(call, " start ") {
					t.Fatal("restart repeated effect", call)
				}
			}
			assertNoEntryPressure(t, f)
		})
	}
}

func TestR1EntryCrashRecoveryIsCleanupOnly(t *testing.T) {
	for _, which := range []string{"lost-ack", "before-release", "before-pidfd", "actors-exited", "changed-boot", "changed-parent", "missing-original", "reap-unknown", "prelaunch-parent"} {
		t.Run(which, func(t *testing.T) {
			f := entryTestFixture(t)
			if err := f.c.publishEntryContext(f.a); err != nil {
				t.Fatal(err)
			}
			f.parentStarted = true
			f.a.Parent = f.parent
			if which == "prelaunch-parent" {
				if err := f.c.Save(f.a, "slice-bound", ""); err != nil {
					t.Fatal(err)
				}
			} else {
				f.serviceStarted = true
				if which != "lost-ack" {
					i, err := readInjection(filepath.Join(f.c.Path, f.a.ID, "injection.json"))
					if err != nil {
						t.Fatal(err)
					}
					f.a.Injector = &i
					f.a.Target = &i.Process
					f.a.Workload = f.service
					f.a.PopulatedLive = true
				}
				if err := f.c.Save(f.a, "launching-service", ""); err != nil {
					t.Fatal(err)
				}
			}
			if which == "actors-exited" {
				f.dead = true
			}
			f.c.Close()
			c, err := OpenCampaign(f.c.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			a := c.Records[f.a.ID]
			if a == nil {
				t.Fatal("original authority unreadable")
			}
			f.a = a
			f.c = c
			if which == "changed-boot" {
				old := f.h.process
				f.h.process = func(pid int) (Process, error) {
					p, e := old(pid)
					p.Boot = "11111111-1111-1111-1111-111111111111"
					return p, e
				}
			}
			if which == "changed-parent" {
				f.parent.Boundary.Identity.Inode++
			}
			if which == "missing-original" {
				a.Target = nil
			}
			if which == "reap-unknown" {
				f.dead = true
				f.h.ended = func(Process) (bool, error) { return false, syscall.EACCES }
			}
			err = c.reconcile(a, f.h)
			pass := which != "changed-boot" && which != "changed-parent" && which != "missing-original" && which != "reap-unknown"
			if pass && (err != nil || a.Phase != "complete" || !a.Empty || !a.SliceStopped) {
				t.Fatal("owned cleanup failed", err, a.Phase)
			}
			if !pass && (err == nil || !c.Blocked) {
				t.Fatal("uncertain cleanup accepted", err)
			}
			if entryObserved(a) || a.EntryRuntime.Failure != nil {
				t.Fatal("recovery promoted missing observation")
			}
			for _, call := range f.calls {
				if strings.Contains(call, "StartTransientUnit") || strings.Contains(call, "--service-type") || strings.Contains(call, " start ") {
					t.Fatal("recovery launched", call)
				}
			}
			if _, err := os.Lstat(filepath.Join(c.Path, a.ID, "release.json")); !os.IsNotExist(err) {
				t.Fatal("recovery published wrapper gate")
			}
			assertNoEntryPressure(t, f)
		})
	}
}

func TestR1EntryIntegritySurvivesRestorationAndSaveRaces(t *testing.T) {
	for _, which := range []string{"context-restored", "forbidden-restored", "final-save-race", "incident-save-fails"} {
		t.Run(which, func(t *testing.T) {
			f := entryTestFixture(t)
			if err := f.c.run(f.a, f.h); err != nil {
				t.Fatal(err)
			}
			context := filepath.Join(f.c.Path, f.a.ID, "r1-control", R1ContextName)
			b, err := os.ReadFile(context)
			if err != nil {
				t.Fatal(err)
			}
			if which == "forbidden-restored" {
				p := filepath.Join(filepath.Dir(context), R1ReleaseName)
				if err := exclusiveFile(p, []byte("inert"), 0400); err != nil {
					t.Fatal(err)
				}
				f.c.checkEntryIntegrity(f.a)
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			} else if which == "final-save-race" {
				sync := f.c.syncFile
				hit := false
				f.c.syncFile = func(file *os.File) error {
					if !hit {
						hit = true
						if err := os.Remove(context); err != nil {
							t.Fatal(err)
						}
					}
					return sync(file)
				}
				if f.c.collectEntry(f.a) == nil {
					t.Fatal("post-check race accepted")
				}
			} else {
				if err := os.Remove(context); err != nil {
					t.Fatal(err)
				}
				f.c.checkEntryIntegrity(f.a)
			}
			if which != "forbidden-restored" {
				if err := exclusiveFile(context, b, 0400); err != nil {
					t.Fatal(err)
				}
			}
			if which == "incident-save-fails" {
				f.c.syncFile = func(*os.File) error { return errors.New("incident save failed") }
			}
			_ = f.c.Save(f.a, "complete", "")
			if f.a.EntryIntegrityFailure == "" || entryObserved(f.a) {
				t.Fatal("restoration cleared integrity")
			}
			f.c.Close()
			c, err := OpenCampaign(f.c.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if !c.Blocked {
				t.Fatal("integrity/durability fence lost on reopen")
			}
			if a := c.Records[f.a.ID]; a != nil && a.EntryIntegrityFailure == "" {
				t.Fatal("restored bytes promoted reopened attempt")
			}
		})
	}
}

func TestR1EntryExpectedTranscriptStrictness(t *testing.T) {
	f := entryTestFixture(t)
	if err := f.c.run(f.a, f.h); err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"sequence", "deadline", "cleanup", "bytes", "page", "release", "finish", "child-id", "token", "termination", "reap", "exit"} {
		t.Run(which, func(t *testing.T) {
			b, _ := json.Marshal(f.failure)
			var e R1Evidence
			json.Unmarshal(b, &e)
			switch which {
			case "sequence":
				e.Sequence = 0
			case "deadline":
				e.Failure.Error = "child"
			case "cleanup":
				e.Failure.Cleanup = "failed"
			case "bytes":
				e.Failure.TouchedBytes = 4096
			case "page":
				e.Failure.PageSize = 4096
			case "release":
				e.Failure.ReleaseSHA256 = strings.Repeat("a", 64)
			case "finish":
				e.Failure.FinishSHA256 = strings.Repeat("a", 64)
			case "child-id":
				e.Failure.Child.Identity.Start++
			case "token":
				e.Failure.Child.Token = "finish"
			case "termination":
				e.Failure.Child.Termination = "unknown"
			case "reap":
				e.Failure.Child.Reap = "unknown"
			case "exit":
				*e.Failure.Child.ExitCode = 0
			}
			if expectedEntryTrace(f.a, f.ready, e) == nil {
				t.Fatal("unexpected trace accepted")
			}
		})
	}
}

func TestR1EntryFDInfoAndNullMetadata(t *testing.T) {
	if flags, mount, err := entryFDInfo([]byte("pos:\t0\nflags:\t0100001\nmnt_id:\t10\nino:\t1\n")); err != nil || flags != 0100001 || mount != 10 {
		t.Fatal(flags, mount, err)
	}
	for _, bad := range []string{"flags: 1\n", "mnt_id: 1\n", "flags: 1\nflags: 1\nmnt_id: 1\n", "flags: z\nmnt_id: 1\n", "flags: 1\nmnt_id: 0\n"} {
		if _, _, err := entryFDInfo([]byte(bad)); err == nil {
			t.Fatal("invalid fdinfo admitted", bad)
		}
	}
	base := R1NullDevice{FileIdentity{1, 2, 0}, syscall.S_IFCHR | 0666, 0, 259}
	if !validEntryNull(base) {
		t.Fatal("null metadata rejected")
	}
	for _, mutate := range []func(*R1NullDevice){func(n *R1NullDevice) { n.Mode = syscall.S_IFREG | 0666 }, func(n *R1NullDevice) { n.Rdev = 258 }, func(n *R1NullDevice) { n.Identity.UID = 1000 }, func(n *R1NullDevice) { n.Mode = syscall.S_IFCHR | 0660 }, func(n *R1NullDevice) { n.GID = 1000 }} {
		n := base
		mutate(&n)
		if validEntryNull(n) {
			t.Fatal("null surrogate admitted")
		}
	}
	if uid, err := projectedUID(1000, "0 1000 1\n", 65534); err != nil || uid != 0 {
		t.Fatal("UID projection", uid, err)
	}
	if uid, err := projectedUID(0, "0 1000 1\n", 12345); err != nil || uid != 12345 {
		t.Fatal("recorded unmapped UID projection", uid, err)
	}
}

func TestR1EntryOldEmptyDevStartupNegative(t *testing.T) {
	f := newR1Fixture(t)
	f.autoRelease, f.autoFinish = false, false
	cause := &os.PathError{Op: "open", Path: "/dev/null", Err: syscall.ENOENT}
	f.d.spawn = func() (r1Child, error) { f.spawned++; return nil, cause }
	if err := runR1(f.d); err == nil {
		t.Fatal("old empty device startup succeeded")
	}
	if f.spawned != 1 || f.allocated != 0 || len(f.frames) != 1 || f.frames[0].Ready != nil {
		t.Fatal("startup failure reached readiness/allocation")
	}
	failure := f.last(t)
	if failure.Stage != "child-ready" || failure.Error != "child" || failure.Cleanup != "not-allocated" {
		t.Fatal(failure)
	}
	// The trusted spawn seam retains ENOENT; the public protocol deliberately
	// retains only its fixed child failure vocabulary.
	if !errors.Is(cause, syscall.ENOENT) || bytes.Contains(f.out.Bytes(), []byte("ENOENT")) || bytes.Contains(f.out.Bytes(), []byte("/dev/null")) {
		t.Fatal("private cause leaked or lost")
	}
	entry := entryTestFixture(t)
	entry.output = func([]byte) []byte { return append(r1JSON(f.frames[0]), '\n') }
	if entry.c.run(entry.a, entry.h) == nil || entryObserved(entry.a) || !entry.a.Empty {
		t.Fatal("startup-only transcript qualified or cleanup lost")
	}
}

func TestR1EntryActorProtocolUsesZeroAllocator(t *testing.T) {
	entry := entryTestFixture(t)
	entry.output = func([]byte) []byte {
		actor := newR1Fixture(t)
		actor.autoRelease, actor.autoFinish = false, false
		actor.d.read = bytes.NewReader(entry.a.EntryStatic.RequestBytes).Read
		actor.store.id = objectID(entry.a.EntryStatic.Control)
		_, context := entryContext(entry.a)
		actor.store.files[R1ContextName] = r1File{context, "entry-owned-context"}
		actor.d.self = func() (R1ActorIdentity, error) { return entry.ready.Ready.Main, nil }
		actor.child.id = entry.ready.Ready.Child
		actor.child.code = 20
		if runR1(actor.d) == nil || actor.allocated != 0 || actor.spawned != 1 || len(actor.frames) != 2 || len(actor.child.ends) != 1 || actor.child.ends[0] != "abort" {
			t.Fatal("entry actor allocated pressure or lost bounded abort")
		}
		return actor.out.Bytes()
	}
	if err := entry.c.run(entry.a, entry.h); err != nil || !entryObserved(entry.a) {
		t.Fatal("real protocol under deterministic controller failed", err)
	}
}

func TestR1EntryLeavesLegacyInspectionAndHashes(t *testing.T) {
	x := testHost(t)
	digest := x.a.PlanSHA256
	x.c.Close()
	var out bytes.Buffer
	if err := InspectHost(x.c.Path, x.a.ID, &out); err != nil {
		t.Fatal(err)
	}
	var a Attempt
	if err := decodeHostRecord(bytes.NewReader(out.Bytes()), &a); err != nil {
		t.Fatal(err)
	}
	if a.PlanSHA256 != digest || strings.Contains(out.String(), `"entry_static"`) || strings.Contains(out.String(), `"entry_runtime"`) || strings.Contains(out.String(), `"busctl"`) {
		t.Fatal("legacy normal inspection changed")
	}
	// The independently captured literal digests include historical direct and
	// candidate plans before/after final-fds, not a new digest of itself.
	legacy := capturedFD1LegacyPlan("candidate", FinalDescriptorPolicy)
	if got := planDigest(legacy); got != "a3d8eac54b9c81f8ed6339219127ad15578930d63717c7b7a5d9f84e4c419f74" {
		t.Fatal("captured historical digest changed", got)
	}
}

func TestR1EntryObservationGuardsAndLedger(t *testing.T) {
	for _, stage := range []string{"pressure-release", "finish", "retained", "completion"} {
		f := entryTestFixture(t)
		f.a.Stage = stage
		if f.c.Save(f.a, "prepared", "") == nil {
			t.Fatal("prohibited stage persisted", stage)
		}
	}
	f := entryTestFixture(t)
	h := entryOperationIO(f.h, f.clock.Add(time.Second))
	f.clock = f.clock.Add(2 * time.Second)
	h.call = entryOperationIO(h, f.clock.Add(15*time.Second)).call
	if _, err := h.call("unused", nil); err == nil {
		t.Fatal("nested operation widened frozen deadline")
	}
	// /dev is a directory on the root tmpfs in the real fixed argv, rather
	// than a distinct mount. The nearest readonly containing mount is valid.
	data := []byte("1 0 0:1 / / ro - tmpfs tmpfs ro\n10 1 0:7 /null /dev/null rw,nosuid - devtmpfs devtmpfs rw\n11 1 0:8 /owned /r1-control ro - tmpfs tmpfs rw\n")
	_, dev, _, err := entryMounts(data)
	if err != nil || !readOnlyMount(dev) || strings.Fields(dev)[4] != "/" {
		t.Fatal("readonly root containing /dev lost", err)
	}
	if _, _, _, err := entryMounts(append(data, []byte("12 11 0:9 / /r1-control/alternate ro - tmpfs tmpfs rw\n")...)); err == nil {
		t.Fatal("control submount admitted")
	}
}

func TestR1EntryOriginalManagerELFInventory(t *testing.T) {
	e := launcherTranscript(t, "/lib64/ld-linux-x86-64.so.2", elf.DT_RUNPATH, "$ORIGIN/../lib")
	meta, err := managerELF(e)
	if err != nil || meta.RunPath != "$ORIGIN/../lib" || meta.DependencyClosureComplete {
		t.Fatal("manager inventory lost original paths or invented closure", meta, err)
	}
	if _, err := launcherELF(e); err == nil {
		t.Fatal("relocated bwrap search policy broadened")
	}
}

func TestR1EntryLostParentAcknowledgementAdoptsBeforeCleanup(t *testing.T) {
	f := entryTestFixture(t)
	call := f.h.call
	f.h.call = func(path string, args []string) (string, error) {
		out, err := call(path, args)
		if path == f.a.Config.Busctl && strings.Contains(strings.Join(args, " "), "StartTransientUnit") && err == nil {
			return "", errors.New("lost parent acknowledgement")
		}
		return out, err
	}
	if err := f.c.run(f.a, f.h); err == nil || !f.parentStarted || f.serviceStarted || f.a.Parent != nil {
		t.Fatal("parent delivery not fenced", err)
	}
	f.c.Close()
	c, err := OpenCampaign(f.c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	f.c = c
	f.a = c.Records[f.a.ID]
	if f.a == nil {
		t.Fatal("lost parent intent unavailable")
	}
	if err := c.reconcile(f.a, f.h); err != nil || !f.a.EntryRuntime.ParentOnly || !f.parentStopped || f.a.Phase != "complete" {
		t.Fatal("prelaunch parent cleanup failed", err)
	}
	if entryObserved(f.a) || f.serviceStarted {
		t.Fatal("recovery launched or promoted entry")
	}
}

func TestR1EntrySafetyReadbackFailureNeverReleasesWrapper(t *testing.T) {
	for _, which := range []string{"tool-memory", "tool-swap", "tool-high", "tool-oom", "tool-runtime", "tool-delegate", "parent-memory", "parent-swap", "parent-high", "parent-tasks", "parent-cpu", "effective", "tree"} {
		t.Run(which, func(t *testing.T) {
			f := entryTestFixture(t)
			switch which {
			case "tool-memory":
				f.service.Properties["MemoryMax"] = "67108864"
			case "tool-swap":
				f.service.Properties["MemorySwapMax"] = "max"
			case "tool-high":
				f.service.Properties["MemoryHigh"] = "100663296"
			case "tool-oom":
				f.service.Properties["OOMPolicy"] = "continue"
			case "tool-runtime":
				f.service.Properties["RuntimeMaxUSec"] = "20s"
			case "tool-delegate":
				f.service.Properties["Delegate"] = "yes"
			case "parent-memory":
				f.parent.Properties["MemoryMax"] = "max"
			case "parent-swap":
				f.parent.Properties["MemorySwapMax"] = "max"
			case "parent-high":
				f.parent.Properties["MemoryHigh"] = "268435456"
			case "parent-tasks":
				f.parent.Properties["TasksMax"] = "infinity"
			case "parent-cpu":
				f.parent.Properties["CPUQuotaPerSecUSec"] = "infinity"
			case "effective":
				f.h.budget = func(*Unit) error { return errors.New("kernel budget differs") }
			case "tree":
				f.h.entryTree = func(Boundary, Boundary) error { return errors.New("unknown parent branch") }
			}
			if err := f.c.run(f.a, f.h); err == nil || entryObserved(f.a) {
				t.Fatal("unsafe entry admitted", err)
			}
			if _, err := os.Lstat(filepath.Join(f.c.Path, f.a.ID, "release.json")); !os.IsNotExist(err) {
				t.Fatal("unsafe wrapper released")
			}
			assertNoEntryPressure(t, f)
		})
	}
}

func TestR1EntryLostToolIdentityDoesNotBecomePrelaunchCleanup(t *testing.T) {
	f := entryTestFixture(t)
	if err := f.c.publishEntryContext(f.a); err != nil {
		t.Fatal(err)
	}
	f.parentStarted = true
	f.a.Parent = f.parent
	f.a.Stage = "tool-start-intent"
	if err := f.c.Save(f.a, "launch-unknown", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.c.reconcile(f.a, f.h); err == nil || f.parentStopped || !f.c.Blocked {
		t.Fatal("absent original Tool identity was guessed", err)
	}
}

func TestR1EntrySavedEmptyRecoversLostParentStopAck(t *testing.T) {
	f := entryTestFixture(t)
	call := f.h.call
	lost := false
	f.h.call = func(path string, args []string) (string, error) {
		out, err := call(path, args)
		if !lost && strings.Contains(strings.Join(args, " "), " stop ") && args[len(args)-1] == f.a.Slice {
			lost = true
			return "", errors.New("lost parent stop acknowledgement")
		}
		return out, err
	}
	if err := f.c.run(f.a, f.h); err == nil || !lost || f.a.Phase != "complete" || !f.a.SliceStopped || entryObserved(f.a) {
		t.Fatal("lost stop acknowledgement corrupted cleanup/entry separation", err, f.a.Phase)
	}
	assertNoEntryPressure(t, f)
}
