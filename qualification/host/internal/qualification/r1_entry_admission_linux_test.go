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
	"testing"
	"time"
)

// The lifecycle transcripts exercise the production admission observer too.
// Only public command replies and direct kernel metadata are substituted.
func entryAdmissionFixture(t *testing.T, f *entryFixture) {
	t.Helper()
	a, h := f.a, &f.h
	root := filepath.Dir(f.parent.Boundary.Path)
	controller := root + "/controller.service"
	h.entryMetadata = func(pid int) (entryIdentity, error) {
		p := entryIdentity{PID: pid, UID: uint32(os.Getuid()), Boot: a.Boot, Start: 123, Cgroup: root + "/init.scope"}
		if pid == 1 {
			p.UID, p.Cgroup = 0, "/init.scope"
		}
		if pid == os.Getpid() {
			self, err := h.process(pid)
			if err != nil {
				return p, err
			}
			p.Start, p.Cgroup, p.Boot = self.Start, self.Cgroup, self.Boot
		}
		return p, nil
	}
	h.entryFile = func(path string, _ int64) ([]byte, error) {
		if path == "/proc/meminfo" {
			return []byte("MemAvailable: 1048576 kB\n"), nil
		}
		if path == fmt.Sprintf("/proc/%d/cmdline", os.Getpid()) {
			return []byte(strings.Join([]string{a.Config.Driver, "run", "--allow-host-operations", "--config", "/qualification/config.json", ""}, "\x00")), nil
		}
		return nil, errors.New("unexpected admission kernel file")
	}
	bound := h.boundary
	h.boundary = func(path string) (Boundary, error) {
		if path == f.parent.Boundary.Path || path == f.service.Boundary.Path {
			return bound(path)
		}
		return Boundary{path, FileIdentity{Device: 7, Inode: uint64(len(path) + 100)}}, nil
	}
	h.entryRead = func(path, key string) ([]byte, error) {
		max, tasks, group := "max", "max", "0"
		switch path {
		case controller:
			max, tasks = "536870912", "128"
		case f.parent.Boundary.Path:
			max, tasks = "268435456", "64"
		case f.service.Boundary.Path:
			max, tasks, group = "100663296", "32", "1"
		}
		value := map[string]string{"memory.max": max, "memory.current": "67108864", "memory.oom.group": group, "memory.swap.max": "0", "memory.high": "max", "pids.max": tasks, "cpu.max": "100000 100000"}[key]
		if value == "" {
			return nil, errors.New("unexpected admission cgroup read")
		}
		return []byte(value + "\n"), nil
	}
	call := h.call
	h.call = func(tool string, args []string) (string, error) {
		if tool == a.Config.Busctl && len(args) >= 7 && args[3] == "org.freedesktop.DBus" {
			f.calls = append(f.calls, strings.Join(args, " "))
			system := args[0] == "--system"
			if !system && args[0] != "--address=unix:path=/run/user/"+strconv.Itoa(os.Getuid())+"/bus" {
				t.Fatal("private bus selected", args)
			}
			switch args[6] {
			case "GetId":
				return `s "` + strings.Repeat("a", 32) + `"`, nil
			case "GetNameOwner":
				return `s ":1.0"`, nil
			case "GetConnectionUnixProcessID":
				if system {
					return "u 1", nil
				}
				return "u 50", nil
			case "GetConnectionUnixUser":
				if system {
					return "u 0", nil
				}
				return "u " + strconv.Itoa(os.Getuid()), nil
			}
			t.Fatal("unexpected public bus operation", args)
		}
		if tool != a.Config.Systemctl {
			return call(tool, args)
		}
		required := ""
		for _, arg := range args {
			if strings.HasPrefix(arg, "--property=") {
				required = strings.TrimPrefix(arg, "--property=")
			}
		}
		if required == "" {
			return call(tool, args)
		}
		name := args[len(args)-1]
		if name == a.Slice || name == a.Service {
			return call(tool, args)
		}
		f.calls = append(f.calls, strings.Join(args, " "))
		path := "/"
		system := args[0] == "--system"
		if name == "controller.service" {
			path = controller
		} else if name == "-.slice" && !system {
			path = root
		} else if name != "-.slice" && name != "systemd-oomd.service" {
			paths, _ := entryAncestry(root)
			for _, p := range paths {
				if filepath.Base(p) == name {
					path = p
				}
			}
		}
		p := map[string]string{"Id": name, "LoadState": "loaded", "ActiveState": "active", "Job": "0", "InvocationID": strings.Repeat("1", 32), "ControlGroup": path, "ManagedOOMSwap": "auto", "ManagedOOMMemoryPressure": "auto", "OOMRules": "", "Version": "fixed-test-manager", "OOMPolicy": "continue"}
		if name == "systemd-oomd.service" {
			p["ControlGroup"], p["StateChangeTimestampMonotonic"] = "/system.slice/systemd-oomd.service", "1234"
		}
		if name == "controller.service" {
			keys := strings.Split(entryControllerProperties, ",")
			settings := entryControllerSettings(os.Getpid())
			j := 0
			for _, key := range keys {
				if key != "ExecStart" {
					p[key] = settings[j]
					j++
				}
			}
			p["ExecStart"] = "{ path=" + a.Config.Driver + " ; argv[]=" + a.Config.Driver + " run --allow-host-operations --config /qualification/config.json ; }"
		}
		var b strings.Builder
		for _, key := range strings.Split(required, ",") {
			fmt.Fprintf(&b, "%s=%s\n", key, p[key])
		}
		return b.String(), nil
	}
}

func TestEntryAdmissionRealBusEnvironment(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/private/bus")
	t.Setenv("XDG_RUNTIME_DIR", "/private")
	env := strings.Join(entryManagerEnvironment(), "\n")
	if strings.Contains(env, "/private") || !strings.Contains(env, "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/"+strconv.Itoa(os.Getuid())+"/bus") {
		t.Fatal(env)
	}
	path := filepath.Join(t.TempDir(), "manager-environment")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n%s\\n' \"$DBUS_SESSION_BUS_ADDRESS\" \"$XDG_RUNTIME_DIR\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := hostCallBus(path, nil, time.Now().Add(time.Second), true)
	want := "unix:path=/run/user/" + strconv.Itoa(os.Getuid()) + "/bus\n/run/user/" + strconv.Itoa(os.Getuid()) + "\n"
	if err != nil || got != want {
		t.Fatal("entry command inherited private bus", got, err)
	}
	got, err = hostCallBus(path, nil, time.Now().Add(time.Second), false)
	if err != nil || got != "unix:path=/private/bus\n/private\n" {
		t.Fatal("legacy connection changed", got, err)
	}
}

func TestEntryAdmissionWalk(t *testing.T) {
	for _, path := range []string{"/", "/app.slice", "/app.slice-extra"} {
		paths, err := entryAncestry(path)
		if err != nil || paths[0] != path || paths[len(paths)-1] != "/" {
			t.Fatal(paths, err)
		}
	}
	for _, path := range []string{"/a/../b", "//a", "/" + strings.Repeat("a/", 32) + "b"} {
		if _, err := entryAncestry(path); err == nil {
			t.Fatal("accepted", path)
		}
	}
	f := entryTestFixture(t)
	root := filepath.Dir(f.parent.Boundary.Path)
	if _, err := entryLocations(f.a, root+"/a.slice/a.slice/controller.service", root, false); err == nil {
		t.Fatal("duplicate unit mapping accepted")
	}
}

func TestEntryAdmissionNumericLimits(t *testing.T) {
	for _, tc := range []struct {
		max          string
		used         uint64
		shared, want bool
	}{
		{"max", ^uint64(0), true, true},
		{"536870912", 128 << 20, true, true},
		{"536870912", (128 << 20) + 1, true, false},
		{"100663296", 64 << 20, false, true},
		{"100663296", 64 << 20, true, false},
		{"0", ^uint64(0), false, false},
		{"1", 2, true, false},
		{"-1", 0, true, false},
		{"18446744073709551616", 0, false, false},
	} {
		if got := entryMemorySafe(tc.max, tc.used, tc.shared); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	for _, input := range []string{"", "MemAvailable: 524287 kB\n", "MemAvailable: -1 kB\n", "MemAvailable: 18446744073709551616 kB\n", "MemAvailable: 0 kB\nMemAvailable: 524288 kB\n", "MemAvailable: 524288 bytes\n"} {
		if _, err := entryAvailable([]byte(input)); err == nil {
			t.Fatal("bad global headroom", input)
		}
	}
	if available, err := entryAvailable([]byte("MemAvailable: 524288 kB\n")); err != nil || available != 512<<10 {
		t.Fatal(available, err)
	}
}

func TestEntryControllerFixedContract(t *testing.T) {
	for _, key := range append(strings.Split(entryControllerProperties, ","), "InvocationID", "ControlGroup") {
		t.Run(key, func(t *testing.T) {
			f := entryTestFixture(t)
			call := f.h.call
			f.h.call = func(tool string, args []string) (string, error) {
				b, err := call(tool, args)
				if args[len(args)-1] == "controller.service" {
					lines := strings.Split(b, "\n")
					for j, line := range lines {
						if strings.HasPrefix(line, key+"=") {
							lines[j] = key + "=invalid"
						}
					}
					b = strings.Join(lines, "\n")
				}
				return b, err
			}
			if err := f.c.run(f.a, f.h); err == nil || f.parentStarted {
				t.Fatal("controller contract ignored", key, err)
			}
		})
	}
	for _, key := range []string{"memory.max", "memory.swap.max", "memory.high", "memory.oom.group", "pids.max", "cpu.max"} {
		t.Run("effective-"+key, func(t *testing.T) {
			f := entryTestFixture(t)
			read := f.h.entryRead
			f.h.entryRead = func(path, k string) ([]byte, error) {
				if strings.HasSuffix(path, "controller.service") && k == key {
					return []byte("invalid"), nil
				}
				return read(path, k)
			}
			if err := f.c.run(f.a, f.h); err == nil || f.parentStarted {
				t.Fatal("effective controller cap ignored", key, err)
			}
		})
	}
}

func TestEntryControllerTerminationPolicy(t *testing.T) {
	for _, tc := range []struct {
		key, value string
	}{
		{"KillMode", "none"},
		{"KillMode", "process"},
		{"KillMode", "mixed"},
		{"SendSIGKILL", "no"},
		{"FinalKillSignal", "15"},
		{"FinalKillSignal", "SIGKILL"}, // Readback must use the numeric representation.
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			f := entryTestFixture(t)
			call := f.h.call
			f.h.call = func(tool string, args []string) (string, error) {
				b, err := call(tool, args)
				if args[len(args)-1] == "controller.service" {
					lines := strings.Split(b, "\n")
					for j, line := range lines {
						if strings.HasPrefix(line, tc.key+"=") {
							lines[j] = tc.key + "=" + tc.value
						}
					}
					b = strings.Join(lines, "\n")
				}
				return b, err
			}
			if err := f.c.run(f.a, f.h); err == nil || f.parentStarted || entryObserved(f.a) {
				t.Fatal("unsafe controller termination policy admitted", err)
			}
			assertNoEntryPressure(t, f)
		})
	}
	for _, key := range []string{"KillMode", "SendSIGKILL", "FinalKillSignal"} {
		for _, fault := range []string{"missing", "duplicate", "truncated"} {
			t.Run(key+"/"+fault, func(t *testing.T) {
				f := entryTestFixture(t)
				call := f.h.call
				f.h.call = func(tool string, args []string) (string, error) {
					b, err := call(tool, args)
					if args[len(args)-1] == "controller.service" {
						lines := strings.SplitAfter(b, "\n")
						for j, line := range lines {
							if strings.HasPrefix(line, key+"=") {
								switch fault {
								case "missing":
									lines[j] = ""
								case "duplicate":
									lines[j] += line
								case "truncated":
									lines[j] = key
								}
							}
						}
						b = strings.Join(lines, "")
					}
					return b, err
				}
				if err := f.c.run(f.a, f.h); err == nil || f.parentStarted || entryObserved(f.a) {
					t.Fatal("unknown controller termination policy admitted", err)
				}
				assertNoEntryPressure(t, f)
			})
		}
	}
	t.Run("control-group-final-SIGKILL", func(t *testing.T) {
		f := entryTestFixture(t)
		if err := f.c.run(f.a, f.h); err != nil || !entryObserved(f.a) {
			t.Fatal("fixed controller termination policy refused", err)
		}
		if got := f.a.EntryRuntime.Admission.Controller.Settings[14:]; !reflect.DeepEqual(got, []string{"control-group", "yes", "9"}) {
			t.Fatal("termination policy missing from receipt", got)
		}
	})
}

func TestEntryConfiguredPolicyAdmission(t *testing.T) {
	for _, which := range []string{"active-sibling", "controller-in-app", "ancestor-kill", "ancestor-rules", "leaf-rules", "missing-root", "private-manager", "manager-uid", "manager-pid", "reused-controller", "controller-invocation", "inode", "mapping", "pending-job", "daemon-generation", "daemon-failed", "daemon-transition", "daemon-malformed", "manager-generation", "missing-property", "duplicate-property", "truncated-property", "timeout", "controller-cap", "controller-last-policy", "parent-policy-generation", "headroom-underflow", "shared-group"} {
		t.Run(which, func(t *testing.T) {
			f := entryTestFixture(t)
			call := f.h.call
			controllerReads := 0
			f.h.call = func(tool string, args []string) (string, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "-- app.slice") {
					t.Fatal("sibling must have no applicable selection path")
				}
				b, err := call(tool, args)
				final := f.serviceStarted
				if which == "timeout" && f.a.Workload != nil {
					return "", errors.New("operation timeout")
				}
				if which == "private-manager" && strings.Contains(joined, "GetId") && final {
					b = strings.ReplaceAll(b, strings.Repeat("a", 32), strings.Repeat("b", 32))
				}
				if which == "manager-uid" && strings.Contains(joined, "GetConnectionUnixUser") && args[0] != "--system" {
					b = "u 0"
				}
				if which == "manager-pid" && strings.Contains(joined, "GetConnectionUnixProcessID") && args[0] == "--system" {
					b = "u 2"
				}
				if which == "manager-generation" && strings.Contains(joined, "GetNameOwner") && final {
					b = `s ":1.1"`
				}
				if tool != f.a.Config.Systemctl {
					return b, err
				}
				ancestor := args[len(args)-1] == "user.slice"
				controller := args[len(args)-1] == "controller.service"
				if controller && strings.Contains(joined, "MainPID") {
					controllerReads++
				}
				switch which {
				case "ancestor-kill":
					if ancestor {
						b = strings.ReplaceAll(b, "ManagedOOMSwap=auto", "ManagedOOMSwap=kill")
					}
				case "ancestor-rules":
					if ancestor {
						b = strings.ReplaceAll(b, "OOMRules=\n", "OOMRules=kill-all\n")
					}
				case "leaf-rules":
					if args[len(args)-1] == f.a.Service {
						b = strings.ReplaceAll(b, "OOMRules=\n", "OOMRules=kill-all\n")
					}
				case "missing-root":
					if args[0] == "--system" && args[len(args)-1] == "-.slice" {
						return "", errors.New("root unavailable")
					}
				case "controller-invocation":
					if controller && final {
						b = strings.ReplaceAll(b, strings.Repeat("1", 32), strings.Repeat("2", 32))
					}
				case "mapping":
					if ancestor {
						b = strings.ReplaceAll(b, "ControlGroup=/user.slice\n", "ControlGroup=/app.slice-extra\n")
					}
				case "pending-job":
					if ancestor {
						b = strings.ReplaceAll(b, "Job=0", "Job=42")
					}
				case "daemon-generation":
					if final && args[len(args)-1] == "systemd-oomd.service" {
						b = strings.ReplaceAll(b, "1234", "1235")
					}
				case "daemon-failed", "daemon-transition":
					if args[len(args)-1] == "systemd-oomd.service" {
						state := "failed"
						if which == "daemon-transition" {
							state = "activating"
						}
						b = strings.ReplaceAll(b, "ActiveState=active", "ActiveState="+state)
					}
				case "daemon-malformed":
					if args[len(args)-1] == "systemd-oomd.service" {
						b = strings.ReplaceAll(b, "1234", "unknown")
					}
				case "controller-last-policy":
					if controller && controllerReads == 2 {
						b = strings.ReplaceAll(b, "ManagedOOMSwap=auto", "ManagedOOMSwap=kill")
					}
				case "parent-policy-generation":
					if args[len(args)-1] == f.a.Slice && strings.Contains(joined, "--property="+entryPolicyProperties) {
						b = strings.ReplaceAll(b, strings.Repeat("a", 32), strings.Repeat("b", 32))
					}
				case "missing-property":
					if ancestor {
						b = strings.ReplaceAll(b, "OOMRules=\n", "")
					}
				case "duplicate-property":
					if ancestor {
						b += "OOMRules=\n"
					}
				case "truncated-property":
					if ancestor {
						b = strings.TrimSuffix(b, "\n") + "invalid"
					}
				case "controller-cap":
					if controller {
						b = strings.ReplaceAll(b, "MemoryMax=536870912", "MemoryMax=268435456")
					}
				}
				return b, err
			}
			proc := f.h.process
			f.h.process = func(pid int) (Process, error) {
				p, err := proc(pid)
				if pid == os.Getpid() {
					if which == "controller-in-app" {
						p.Cgroup = filepath.Dir(p.Cgroup) + "/app.slice/controller.service"
					}
					if which == "reused-controller" && f.serviceStarted {
						p.Start++
					}
				}
				return p, err
			}
			bound := f.h.boundary
			f.h.boundary = func(path string) (Boundary, error) {
				b, err := bound(path)
				if which == "inode" && f.serviceStarted && strings.HasSuffix(path, "controller.service") {
					b.Identity.Inode++
				}
				return b, err
			}
			read := f.h.entryRead
			f.h.entryRead = func(path, key string) ([]byte, error) {
				if which == "headroom-underflow" && key == "memory.max" && path == "/user.slice" {
					return []byte("1"), nil
				}
				if which == "shared-group" && key == "memory.oom.group" && strings.HasSuffix(path, ".service") && !strings.HasSuffix(path, "controller.service") {
					return []byte("1"), nil
				}
				return read(path, key)
			}
			err := f.c.run(f.a, f.h)
			if which == "active-sibling" {
				if err != nil || !entryObserved(f.a) {
					t.Fatal(err, f.a.Reason)
				}
				if !entryAdmitted(f.a) {
					t.Fatal("missing complete admission")
				}
				return
			}
			if err == nil || f.a.EntryRuntime.WrapperIntent || entryObserved(f.a) {
				t.Fatal("unsafe admission", err)
			}
			if f.serviceStarted && f.a.Workload == nil {
				t.Fatal("lost original cleanup authority")
			}
		})
	}
}

func TestEntryAdmissionCompatibilityAndSize(t *testing.T) {
	f := entryTestFixture(t)
	plan, context := planDigest(f.a), func() []byte { _, b := entryContext(f.a); return b }()
	if err := f.c.run(f.a, f.h); err != nil {
		t.Fatal(err)
	}
	if planDigest(f.a) != plan {
		t.Fatal("runtime admission changed immutable plan")
	}
	_, after := entryContext(f.a)
	if !bytes.Equal(context, after) {
		t.Fatal("runtime admission changed immutable context")
	}
	b, _ := json.Marshal(f.a)
	t.Logf("complete positive persisted attempt: %d / %d bytes", len(b), MaxInputBytes)
	if len(b) > int(MaxInputBytes) {
		t.Fatal("complete positive record exceeds decoder cap")
	}
	var reopened Attempt
	if err := decodeHostRecord(bytes.NewReader(b), &reopened); err != nil || validateStored(&reopened) != nil || !entryObserved(&reopened) {
		t.Fatal("new record", err, validateStored(&reopened))
	}
	old := f.a.EntryRuntime.Admission
	f.a.EntryRuntime.Admission = nil
	b, _ = json.Marshal(f.a)
	reopened = Attempt{}
	if err := decodeHostRecord(bytes.NewReader(b), &reopened); err != nil || validateStored(&reopened) != nil || entryObserved(&reopened) {
		t.Fatal("old cleanup record compatibility", err)
	}
	f.a.EntryRuntime.Admission = old
	for _, mutate := range []func(*entryAdmission){
		func(e *entryAdmission) { e.Snapshots = e.Snapshots[:1] },
		func(e *entryAdmission) { e.Snapshots[1].Units = e.Snapshots[1].Units[1:] },
		func(e *entryAdmission) { e.Snapshots[1].Units[1].Rules = "kill" },
		func(e *entryAdmission) { e.Controller.Settings[7] = "1" },
		func(e *entryAdmission) { e.Controller.Settings = e.Controller.Settings[:14] },
		func(e *entryAdmission) { e.Controller.Settings[14] = "mixed" },
		func(e *entryAdmission) { e.Controller.Settings[15] = "no" },
		func(e *entryAdmission) { e.Controller.Settings[16] = "15" },
	} {
		b, _ := json.Marshal(old)
		var copy entryAdmission
		json.Unmarshal(b, &copy)
		mutate(&copy)
		f.a.EntryRuntime.Admission = &copy
		if entryAdmitted(f.a) || entryObserved(f.a) {
			t.Fatal("incomplete evidence admitted")
		}
	}
}

func TestEntryAdmissionNoManagerReadinessCalls(t *testing.T) {
	f := entryTestFixture(t)
	call := f.h.call
	f.h.call = func(tool string, args []string) (string, error) {
		if f.wrote && !f.dead {
			t.Fatal("manager command in actor readiness window", args)
		}
		return call(tool, args)
	}
	if err := f.c.run(f.a, f.h); err != nil {
		t.Fatal(err)
	}
}

func TestEntryAdmissionDeadline(t *testing.T) {
	f := entryTestFixture(t)
	f.h.deadlineCall = func(string, []string, time.Time) (string, error) { return "", errors.New("deadline") }
	if err := f.c.run(f.a, f.h); err == nil || f.parentStarted {
		t.Fatal("deadline crossed parent intent", err)
	}
}

func TestEntryManagerIdentityMetadata(t *testing.T) {
	p, err := entryKernelIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if p.PID != os.Getpid() || p.UID != uint32(os.Getuid()) || p.Start == 0 || !validBoot(p.Boot) {
		t.Fatal(p)
	}
	if reflect.DeepEqual(p, entryIdentity{}) {
		t.Fatal("empty identity")
	}
}

func TestEntryAdmissionEveryObservationFailure(t *testing.T) {
	install := func(f *entryFixture, fail int) *int {
		call, count := f.h.call, new(int)
		f.h.call = func(tool string, args []string) (string, error) {
			if (f.a.EntryRuntime == nil || !f.a.EntryRuntime.WrapperIntent) && (strings.Contains(strings.Join(args, " "), " show ") || strings.Contains(strings.Join(args, " "), "org.freedesktop.DBus Get") || reflect.DeepEqual(args, []string{"--version"})) {
				*count++
				if *count == fail {
					return "", errors.New("selected admission observation failed")
				}
			}
			return call(tool, args)
		}
		return count
	}
	f := entryTestFixture(t)
	count := install(f, 0)
	if err := f.c.run(f.a, f.h); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= *count; n++ {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			f := entryTestFixture(t)
			install(f, n)
			if err := f.c.run(f.a, f.h); err == nil || entryObserved(f.a) {
				t.Fatal("failed observation admitted", n, err)
			}
			if f.a.EntryRuntime != nil && f.a.EntryRuntime.WrapperIntent {
				t.Fatal("wrapper intent after observation failure")
			}
			if f.serviceStarted && (f.a.Parent == nil || f.a.Workload == nil || !f.dead) {
				t.Fatal("original generation cleanup lost")
			}
			assertNoEntryPressure(t, f)
			reopenEntryFixture(t, f)
			if entryObserved(f.a) {
				t.Fatal("reopen promoted failed admission")
			}
		})
	}
}

func TestEntryAdmissionSaveFailures(t *testing.T) {
	for _, stage := range []string{"before-parent", "parent-binding", "tool-binding", "final-snapshot", "wrapper-intent"} {
		t.Run(stage, func(t *testing.T) {
			f := entryTestFixture(t)
			sync := f.c.syncFile
			failed := false
			f.c.syncFile = func(file *os.File) error {
				r := f.a.EntryRuntime
				selected := r != nil && (stage == "before-parent" && r.ParentStartIntent && !f.parentStarted || stage == "parent-binding" && f.a.Parent != nil && !r.ToolDispatchIntent || stage == "tool-binding" && f.a.Workload != nil && f.a.Phase == "launching-service" || stage == "final-snapshot" && r.Admission != nil && len(r.Admission.Snapshots) == 2 || stage == "wrapper-intent" && r.WrapperIntent)
				if selected && !failed {
					failed = true
					return errors.New("admission save failed")
				}
				return sync(file)
			}
			if err := f.c.run(f.a, f.h); err == nil || !failed || entryObserved(f.a) {
				t.Fatal("save failure escaped", err, failed)
			}
			if _, err := os.Stat(filepath.Join(f.c.Path, f.a.ID, "release.json")); !os.IsNotExist(err) {
				t.Fatal("wrapper published after failed save", err)
			}
			if f.serviceStarted && (f.a.Workload == nil || f.a.Injector == nil || !f.dead) {
				t.Fatal("in-memory original authority lost")
			}
			assertNoEntryPressure(t, f)
			// The existing durability marker intentionally fences automatic reopen.
			// The structurally valid original-generation bytes remain on disk.
			b, err := os.ReadFile(filepath.Join(f.c.Path, f.a.ID, "intent.json"))
			var saved Attempt
			if err != nil || decodeHostRecord(bytes.NewReader(b), &saved) != nil || validateStored(&saved) != nil || entryObserved(&saved) {
				t.Fatal("original cleanup record lost", err)
			}
			if f.serviceStarted && (saved.Workload == nil || !sameUnit(saved.Workload, f.a.Workload)) {
				t.Fatal("saved service authority lost")
			}
			f.c.Close()
			c, err := OpenCampaign(f.c.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if !c.Blocked {
				t.Fatal("durability fence lost on restart")
			}
		})
	}
}

func TestEntryControllerLoss(t *testing.T) {
	for _, when := range []string{"before-wrapper", "after-wrapper", "after-observation"} {
		t.Run(when, func(t *testing.T) {
			f := entryTestFixture(t)
			metadata := f.h.entryMetadata
			f.h.entryMetadata = func(pid int) (entryIdentity, error) {
				p, err := metadata(pid)
				if pid == os.Getpid() && (when == "before-wrapper" && f.a.Workload != nil || when == "after-wrapper" && f.wrote || when == "after-observation" && f.observed) {
					return p, errors.New("controller lost")
				}
				return p, err
			}
			if err := f.c.run(f.a, f.h); err == nil || entryObserved(f.a) {
				t.Fatal("lost controller admitted", err)
			}
			if !f.dead || !f.parentStopped || f.a.Workload == nil {
				t.Fatal("controller loss suppressed original cleanup")
			}
			reopenEntryFixture(t, f)
			if entryObserved(f.a) {
				t.Fatal("controller loss promoted on reopening")
			}
			assertNoEntryPressure(t, f)
		})
	}
}

func TestEntryCleanupIgnoresRuntimeAdmission(t *testing.T) {
	for _, kind := range []string{"old-absent", "old-14-settings", "incomplete", "unsupported-version"} {
		t.Run(kind, func(t *testing.T) {
			f := entryTestFixture(t)
			if err := f.c.publishEntryContext(f.a); err != nil {
				t.Fatal(err)
			}
			if err := f.h.observeEntryAdmission(f.a, false); err != nil {
				t.Fatal(err)
			}
			if kind == "old-absent" {
				f.a.EntryRuntime.Admission = nil
			}
			if kind == "unsupported-version" {
				f.a.EntryRuntime.Admission.Version = 999
			}
			f.a.EntryRuntime.ParentStartIntent, f.a.EntryRuntime.ToolDispatchIntent = true, true
			f.parentStarted, f.serviceStarted = true, true
			f.a.Parent, f.a.Workload = f.parent, f.service
			i, err := readInjection(filepath.Join(f.c.Path, f.a.ID, "injection.json"))
			if err != nil {
				t.Fatal(err)
			}
			f.a.Injector, f.a.Target = &i, &i.Process
			if kind == "old-14-settings" {
				if err := f.h.observeEntryAdmission(f.a, true); err != nil || !entryAdmitted(f.a) {
					t.Fatal("complete admission fixture required", err)
				}
				// Preserve the original version-1 encoding and its 14 settings.
				f.a.EntryRuntime.Admission.Controller.Settings = []string{strconv.Itoa(os.Getpid()), "-.slice", "yes", "exec", "no", "1min 30s", "5s", "536870912", "0", "infinity", "128", "1s", "continue", "no"}
			}
			// Historical unit bindings did not carry the new policy properties.
			for _, u := range []*Unit{f.a.Parent, f.a.Workload} {
				for _, key := range []string{"ManagedOOMSwap", "ManagedOOMMemoryPressure", "OOMRules"} {
					delete(u.Properties, key)
				}
			}
			phase := "launching-service"
			if kind == "old-14-settings" {
				phase = "bound"
			}
			if err := f.c.Save(f.a, phase, ""); err != nil {
				t.Fatal(err)
			}
			reopenEntryFixture(t, f)
			if kind == "old-14-settings" {
				if got := f.a.EntryRuntime.Admission; got == nil || got.Version != 1 || len(got.Controller.Settings) != 14 || len(got.Snapshots) != 2 {
					t.Fatal("historical receipt encoding lost", got)
				}
				if entryAdmitted(f.a) || entryObserved(f.a) || entryAction(f.a, "wrapper-entry") == nil {
					t.Fatal("historical receipt granted new entry authority")
				}
			}
			if err := f.c.reconcile(f.a, f.h); err != nil || !f.dead || !f.parentStopped || f.a.Phase != "complete" {
				t.Fatal("runtime policy evidence blocked original cleanup", err)
			}
			requireEntryCleanupOnly(t, f)
			reopenEntryFixture(t, f)
			if f.a.Phase != "complete" || entryObserved(f.a) {
				t.Fatal("cleanup changed on second reopen")
			}
		})
	}
}
